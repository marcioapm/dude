import { afterAll, beforeAll, expect, test } from "bun:test";
import { SQL } from "bun";
import { join } from "node:path";
import { createHash } from "node:crypto";
import { listMigrationFiles, migrate } from "../src/db/migrate.ts";
import { closePool, setPool, withOrg } from "../src/db/client.ts";
import { authenticate, createApiKey, personPrincipal, type Principal } from "../src/api/auth.ts";
import { Router, type RequestContext } from "../src/api/router.ts";
import { ownerJson, registerPeopleRoutes, setTaskPeople } from "../src/api/routes/people.ts";
import { registerPushRoutes } from "../src/api/routes/push.ts";

const ownerUrl = process.env.DATABASE_URL!;
const name = `dude_people_push_${Bun.randomUUIDv7("hex").slice(-12)}`;
const org = "org_people_push";
const other = "org_people_push_other";
const person = "per_keyless";
const project = "prj_people_push";
let admin: SQL;
let owner: SQL;
let app: SQL;
let keyed: Extract<Principal, { credentialKind: "api_key" }>;
let keyless: Principal;
let adminPerson: Principal;
let adminKey: string;
const router = new Router(credential => credential?.startsWith("Person ")
  ? personPrincipal(org, credential.slice(7)) : authenticate(credential));
registerPeopleRoutes(router);
registerPushRoutes(router);

function ctx(principal: Principal, method = "GET", path = "/v1/me", body?: unknown, params = {}): RequestContext {
  const url = new URL(`http://dude.test${path}`);
  return { principal, url, params, request: new Request(url.toString(), { method,
    headers: { "content-type": "application/json" }, ...(body === undefined ? {} : { body: JSON.stringify(body) }) }) };
}
function call(principal: Principal, method: string, path: string, body?: unknown, params: Record<string, string> = {}) {
  for (const [key, value] of Object.entries(params)) path = path.replace(`:${key}`, value);
  return router.handle(new Request(`http://dude.test${path}`, { method,
    headers: { "content-type": "application/json", authorization: principal.credentialKind === "person"
      ? `Person ${principal.personId}` : `Bearer ${adminKey}` },
    ...(body === undefined ? {} : { body: JSON.stringify(body) }),
  }));
}
function dbUrl(appRole = false) {
  const url = new URL(ownerUrl);
  url.pathname = `/${name}`;
  if (appRole) { url.username = "dude_app"; url.password = "dude_app"; }
  return url.toString();
}

beforeAll(async () => {
  admin = new SQL(ownerUrl);
  await admin.unsafe(`CREATE DATABASE "${name}"`);
  const migrated = Bun.spawnSync(["bun", "run", "apps/control-plane/src/db/migrate.ts"], {
    cwd: join(import.meta.dir, "../../.."), env: { ...process.env, DATABASE_URL: dbUrl() },
  });
  if (migrated.exitCode) throw new Error(migrated.stderr.toString());
  owner = new SQL(dbUrl());
  for (const id of [org, other]) await owner`INSERT INTO organizations (id, name, slug) VALUES (${id}, ${id}, ${id})`;
  await owner`INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES (${project}, ${org}, 'People', 'people', 'PP')`;
  await owner`INSERT INTO people (id, organization_id, name) VALUES (${person}, ${org}, 'Keyless')`;
  app = new SQL(dbUrl(true));
  setPool(app);
  const made = await createApiKey({ organizationId: org, name: "Admin" });
  adminKey = made.key;
  const resolved = await authenticate(`Bearer ${made.key}`);
  if (!resolved || resolved.credentialKind !== "api_key") throw new Error("fixture key did not authenticate");
  keyed = resolved;
  keyless = (await personPrincipal(org, person))!;
  adminPerson = (await personPrincipal(org, made.personId))!;
  expect(adminPerson.role).toBe("admin");
});
afterAll(async () => {
  await closePool(app);
  await owner?.end();
  await admin?.unsafe(`DROP DATABASE IF EXISTS "${name}" WITH (FORCE)`);
  await admin?.end();
});

test("keyless task ownership is displayed and audited without a legacy key", async () => {
  await owner`INSERT INTO tasks (id, organization_id, project_id, number, title) VALUES ('tsk_keyless', ${org}, ${project}, 1, 'Keyless task')`;
  await withOrg(org, scope => setTaskPeople(scope, ctx(keyless), "tsk_keyless", project, [person, keyed.personId], false));
  const [row] = await withOrg(org, async ({ sql }) => sql`SELECT ${sql.unsafe(ownerJson())}, owner_key_id FROM tasks WHERE id = 'tsk_keyless'`);
  expect(row.owner.id).toBe(person);
  expect(row.owner_key_id).toBeNull();
  const [event] = await owner`SELECT actor_type, actor_id, payload FROM events WHERE event_type = 'task.owner_changed' ORDER BY cursor DESC LIMIT 1`;
  expect(event.actor_type).toBe("person");
  expect(event.actor_id).toBe(person);
  expect(event.payload.to).toBe(person);
  expect(await owner`SELECT id FROM api_keys WHERE person_id = ${person}`).toHaveLength(0);
});

test("last-key revocation requires a person credential, not a key credential", async () => {
  expect((await call(keyed, "DELETE", "/v1/me/keys/:id", undefined, { id: keyed.apiKeyId })).status).toBe(409);
  const principal = adminPerson;
  const list = await (await call(principal, "GET", "/v1/me/keys")).json() as { keys: Array<{ current: boolean }> };
  expect(list.keys.every((key: { current: boolean }) => !key.current)).toBe(true);
  expect((await call(principal, "DELETE", "/v1/me/keys/:id", undefined, { id: keyed.apiKeyId })).status).toBe(204);
});

test("push claims retain person ownership across key revocation and browser deletion", async () => {
  const sub = { endpoint: "https://push.example.test/keyless", keys: { p256dh: "public", auth: "secret" } };
  expect((await call(keyless, "POST", "/v1/push/subscriptions", sub)).status).toBe(201);
  const [saved] = await owner`SELECT person_id, api_key_id FROM push_subscriptions WHERE endpoint = ${sub.endpoint}`;
  expect(saved).toEqual({ person_id: person, api_key_id: null });
  const made = await createApiKey({ organizationId: org, name: "Temporary", personId: person });
  await owner`UPDATE api_keys SET revoked_at = now() WHERE id = ${made.id}`;
  expect(await owner`SELECT endpoint FROM push_subscriptions WHERE endpoint = ${sub.endpoint}`).toHaveLength(1);
  await withOrg(other, async ({ sql }) => {
    expect(await sql`SELECT endpoint FROM push_subscriptions`).toHaveLength(0);
    await sql`DELETE FROM push_subscriptions WHERE endpoint = ${sub.endpoint}`;
  });
  expect((await call(keyless, "POST", "/v1/push/subscriptions/remove", { endpoint: sub.endpoint })).status).toBe(204);
  expect(await owner`SELECT endpoint FROM push_subscriptions WHERE endpoint = ${sub.endpoint}`).toHaveLength(0);
});

test("the existing claim boundary transfers only the named browser endpoint", async () => {
  const foreign = "per_foreign_push";
  await owner`INSERT INTO people (id, organization_id, name) VALUES (${foreign}, ${other}, 'Foreign')`;
  await withOrg(other, async ({ sql }) => {
    await sql`SELECT claim_push_subscription('https://push.example.test/moving', ${other}, ${foreign}, NULL, 'old', 'old')`;
    await sql`SELECT claim_push_subscription('https://push.example.test/staying', ${other}, ${foreign}, NULL, 'old', 'old')`;
  });
  await withOrg(org, async ({ sql }) => {
    await sql`SELECT claim_push_subscription('https://push.example.test/moving', ${org}, ${person}, NULL, 'new', 'new')`;
    const rows = await sql`SELECT endpoint, person_id, api_key_id, p256dh FROM push_subscriptions ORDER BY endpoint`;
    expect(rows).toEqual([{ endpoint: 'https://push.example.test/moving', person_id: person, api_key_id: null, p256dh: 'new' }]);
  });
  const [staying] = await owner`SELECT organization_id, person_id FROM push_subscriptions WHERE endpoint = 'https://push.example.test/staying'`;
  expect(staying).toEqual({ organization_id: other, person_id: foreign });
});

test("removal hands ownership to a keyless person and deletes their browser subscriptions", async () => {
  const departing = "per_departing_push";
  await owner`INSERT INTO people (id, organization_id, name) VALUES (${departing}, ${org}, 'Departing')`;
  await owner`INSERT INTO tasks (id, organization_id, project_id, number, title) VALUES ('tsk_handoff', ${org}, ${project}, 2, 'Handoff')`;
  const principal = adminPerson;
  await withOrg(org, async scope => {
    await setTaskPeople(scope, ctx(principal), "tsk_handoff", project, [departing, person], false);
    await scope.sql`SELECT claim_push_subscription('https://push.example.test/departing', ${org}, ${departing}, NULL, 'public', 'secret')`;
  });
  const departingPrincipal = (await personPrincipal(org, departing))!;
  expect((await call(principal, "DELETE", "/v1/people/:id", undefined, { id: departing })).status).toBe(204);
  const [task] = await withOrg(org, async ({ sql }) => sql`SELECT ${sql.unsafe(ownerJson())}, owner_key_id FROM tasks WHERE id = 'tsk_handoff'`);
  expect(task.owner.id).toBe(person);
  expect(task.owner_key_id).toBeNull();
  expect(await owner`SELECT endpoint FROM push_subscriptions WHERE person_id = ${departing}`).toHaveLength(0);
  const [event] = await owner`SELECT payload FROM events WHERE task_id = 'tsk_handoff' AND event_type = 'task.owner_changed' ORDER BY cursor DESC LIMIT 1`;
  expect(event.payload).toEqual({ from: departing, to: person });
  expect((await call(departingPrincipal, "POST", "/v1/push/subscriptions", {
    endpoint: "https://push.example.test/departing", keys: { p256dh: "public", auth: "secret" },
  })).status).toBe(401);
});

test("member management rechecks the current person role", async () => {
  const stale: Principal = { ...keyless, role: "admin" };
  const staleRouter = new Router(async () => stale);
  registerPeopleRoutes(staleRouter);
  const response = await staleRouter.handle(ctx(stale, "POST", "/v1/people", { name: "No", email: "no@example.test" }).request);
  expect(response.status).toBe(403);
  expect(await owner`SELECT id FROM people WHERE email = 'no@example.test'`).toHaveLength(0);
});

for (const baseline of ["055", "056"]) {
  test(`upgrade ${baseline} to 057 preserves attributed subscriptions and distinguishes revocation from removal`, async () => {
    const upgradeName = `${name}_upgrade_${baseline}`;
    const url = new URL(ownerUrl);
    url.pathname = `/${upgradeName}`;
    await admin.unsafe(`CREATE DATABASE "${upgradeName}"`);
    const legacy = new SQL(url.toString());
    let upgradeApp: SQL | undefined;
    try {
      await legacy`CREATE TABLE schema_migrations (version text PRIMARY KEY, name text NOT NULL,
        checksum text NOT NULL, applied_at timestamptz NOT NULL DEFAULT now())`;
      for (const file of await listMigrationFiles()) {
        if (file.version > baseline) continue;
        const contents = await file.contents();
        await legacy.begin(async tx => {
          await tx.unsafe(contents);
          await tx`INSERT INTO schema_migrations (version, name, checksum)
            VALUES (${file.version}, ${file.name}, ${createHash("sha256").update(contents).digest("hex")})`;
        });
      }
      await legacy`INSERT INTO organizations (id, name, slug) VALUES ('org_upgrade', 'Upgrade', 'upgrade')`;
      for (const id of ["key_upgrade_active", "key_upgrade_revoked", "key_upgrade_removed"]) {
        await legacy`INSERT INTO api_keys (id, organization_id, name, key_hash, key_prefix)
          VALUES (${id}, 'org_upgrade', ${id}, ${id}, 'dude_sk_')`;
        await legacy`INSERT INTO push_subscriptions (endpoint, organization_id, api_key_id, p256dh, auth, created_at)
          VALUES (${"https://push.example.test/" + id}, 'org_upgrade', ${id}, ${"public_" + id}, ${"auth_" + id}, '2026-01-02T03:04:05Z')`;
      }
      await legacy`UPDATE api_keys SET revoked_at = now() WHERE id = 'key_upgrade_revoked'`;
      await legacy`UPDATE people SET removed_at = now() WHERE id =
        (SELECT person_id FROM api_keys WHERE id = 'key_upgrade_removed')`;
      await legacy`INSERT INTO push_subscriptions (endpoint, organization_id, p256dh, auth)
        VALUES ('https://push.example.test/anonymous', 'org_upgrade', 'anonymous', 'anonymous')`;
      const before = await legacy`SELECT s.*, k.person_id FROM push_subscriptions s
        JOIN api_keys k ON k.id = s.api_key_id ORDER BY s.endpoint` as Array<{
          endpoint: string; organization_id: string; api_key_id: string; person_id: string;
          p256dh: string; auth: string; created_at: Date;
        }>;
      await migrate(url.toString(), { log() {} });
      const after = await legacy`SELECT * FROM push_subscriptions ORDER BY endpoint`;
      expect(after).toEqual(before);
      expect(after).toHaveLength(3);
      url.username = "dude_app";
      url.password = "dude_app";
      upgradeApp = new SQL(url.toString());
      setPool(upgradeApp);
      const active = (await personPrincipal("org_upgrade", before.find(row => row.api_key_id === "key_upgrade_active")!.person_id))!;
      expect(active.role).toBe("admin");
      const revoked = before.find(row => row.api_key_id === "key_upgrade_revoked")!;
      expect(await personPrincipal("org_upgrade", revoked.person_id)).not.toBeNull();
      const eligible = async () => withOrg("org_upgrade", async ({ sql }) => sql`
        SELECT s.api_key_id FROM push_subscriptions s JOIN people p ON p.id = s.person_id
          AND p.organization_id = s.organization_id WHERE p.removed_at IS NULL ORDER BY s.api_key_id`);
      expect(await eligible()).toEqual([{ api_key_id: "key_upgrade_active" }, { api_key_id: "key_upgrade_revoked" }]);
      const upgradeRouter = new Router(() => personPrincipal("org_upgrade", active.personId));
      registerPeopleRoutes(upgradeRouter);
      const revoke = await upgradeRouter.handle(new Request(`http://dude.test/v1/me/keys/key_upgrade_active`, { method: "DELETE" }));
      expect(revoke.status).toBe(204);
      const afterRevoke = await legacy`SELECT * FROM push_subscriptions ORDER BY endpoint` as typeof before;
      expect(afterRevoke).toEqual(before);
      expect(await eligible()).toHaveLength(2);
      const remove = await upgradeRouter.handle(new Request(`http://dude.test/v1/people/${revoked.person_id}`, { method: "DELETE" }));
      expect(remove.status).toBe(204);
      expect(await personPrincipal("org_upgrade", revoked.person_id)).toBeNull();
      expect(await legacy`SELECT endpoint FROM push_subscriptions WHERE person_id = ${revoked.person_id}`).toHaveLength(0);
      expect(await eligible()).toEqual([{ api_key_id: "key_upgrade_active" }]);
    } finally {
      setPool(app);
      await upgradeApp?.end();
      await legacy.end();
      await admin.unsafe(`DROP DATABASE IF EXISTS "${upgradeName}" WITH (FORCE)`);
    }
  });
}

test("claim rejects foreign and removed people and mismatched source keys", async () => {
  for (const [tenant, who, key] of [[other, person, null], [org, person, keyed.apiKeyId]] as const) {
    await expect(withOrg(tenant, async ({ sql }) => sql`SELECT claim_push_subscription('https://push.example.test/refused', ${tenant}, ${who}, ${key}, 'public', 'secret')`)).rejects.toBeDefined();
  }
  await owner`INSERT INTO people (id, organization_id, name, removed_at) VALUES ('per_removed_push', ${org}, 'Removed', now())`;
  await expect(withOrg(org, async ({ sql }) => sql`SELECT claim_push_subscription('https://push.example.test/refused', ${org}, 'per_removed_push', NULL, 'public', 'secret')`)).rejects.toBeDefined();
  await expect(withOrg(org, async ({ sql }) => sql`SELECT claim_push_subscription('https://push.example.test/refused', ${other}, ${person}, NULL, 'public', 'secret')`)).rejects.toBeDefined();
  expect(await owner`SELECT endpoint FROM push_subscriptions WHERE endpoint = 'https://push.example.test/refused'`).toHaveLength(0);
});
