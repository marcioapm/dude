/**
 * Behind Cloudflare Access, as index.ts serves it: one account in two
 * organisations turns notifications on and off in each alone; its
 * memberships are welcomed one by one; and an admin's Invitations page
 * (and the super-admin's, for any organisation) counts every status past
 * one page and pages the list with its cursor.
 *
 * Each test makes the people, endpoints and organisations it reads, so any
 * one runs alone.
 */

import { afterAll, beforeAll, describe, expect, test } from "bun:test";
import { SQL } from "bun";
import { createHash } from "node:crypto";
import type { Router } from "../src/api/router.ts";
import { closePool, setPool } from "../src/db/client.ts";
import { listMigrationFiles, migrate, outsideTransaction } from "../src/db/migrate.ts";
import { Edge, OPERATOR, ORIGIN, cookie, databases, fullRouter, member, rsaKey, sign, type Key } from "./access-harness.ts";

const suffix = Bun.randomUUIDv7("hex").slice(-12);
const acme = `org_fu_${suffix}`;
const northwind = `${acme}_nw`;
const at = (local: string) => `${local}@${suffix}.fu.test`;
// A super-admin of this suite's own, so a membership given to it touches no other suite's operator.
const operator = at("operator");
let owner: SQL;
let app: SQL;
let signer: Key;
let router: Router;
type Json = any;

beforeAll(async () => {
  ({ owner, app } = databases());
  setPool(app);
  await owner`INSERT INTO organizations (id, name, slug) VALUES (${acme}, 'Acme', ${acme}), (${northwind}, 'Northwind', ${northwind})`;
  signer = await rsaKey("k1");
  const edge = new Edge();
  edge.keys = [signer.jwk];
  router = fullRouter(edge, { super_admins: [OPERATOR, operator] });
});
afterAll(async () => {
  await closePool(app);
  await owner`DELETE FROM organizations WHERE id LIKE ${`${acme}%`}`;
  await owner`DELETE FROM accounts WHERE email LIKE ${`%@${suffix}.fu.test`}`;
  await owner.end();
});

async function as(email: string, method: string, path: string, body?: unknown, organization: string | null = acme) {
  const headers: Record<string, string> = { ...cookie(await sign(signer, { email })), origin: ORIGIN, "content-type": "application/json" };
  if (organization) headers["x-dude-organization"] = organization;
  const res = await router.handle(new Request(ORIGIN + path, { method, headers, ...(body === undefined ? {} : { body: JSON.stringify(body) }) }));
  return { status: res.status, body: res.status === 204 ? null : await res.json().catch(() => null) as Json };
}

/** A name no other test uses. */
const unique = (what: string) => `${what}-${crypto.randomUUID().slice(0, 8)}`;

describe("a browser's notifications, per organisation", () => {
  /** A member of Acme and Northwind, and a browser endpoint of its own. */
  async function twoOrgs() {
    const email = at(unique("two"));
    await member(owner, acme, email);
    await member(owner, northwind, email);
    const endpoint = `https://push.example.test/${unique(suffix)}`;
    const sub = { endpoint, keys: { p256dh: "public", auth: "secret" } };
    const rows = () => owner`SELECT organization_id FROM push_subscriptions WHERE endpoint = ${endpoint} ORDER BY organization_id`
      .then((r: Array<{ organization_id: string }>) => r.map((x) => x.organization_id));
    const subscribe = async (org: string) => expect((await as(email, "POST", "/v1/push/subscriptions", sub, org)).status).toBe(201);
    return { email, endpoint, sub, rows, subscribe };
  }

  test("a member of two subscribes the same browser in both; each has its own row", async () => {
    const { email, endpoint, sub, rows, subscribe } = await twoOrgs();
    await subscribe(acme);
    await subscribe(northwind);
    expect(await rows()).toEqual([acme, northwind].sort());
    for (const org of [acme, northwind]) {
      expect((await as(email, "POST", "/v1/push/subscriptions/lookup", { endpoint }, org)).body).toEqual({ subscribed: true });
    }
    // Again is a refresh of its keys, not a second row.
    expect((await as(email, "POST", "/v1/push/subscriptions", { ...sub, keys: { p256dh: "rotated", auth: "secret" } }, acme)).status).toBe(201);
    expect(await rows()).toEqual([acme, northwind].sort());
    expect(await owner`SELECT p256dh FROM push_subscriptions WHERE endpoint = ${endpoint} AND organization_id = ${acme}`)
      .toEqual([{ p256dh: "rotated" }] as never);
  });

  test("unsubscribing in Northwind keeps Acme's", async () => {
    const { email, endpoint, rows, subscribe } = await twoOrgs();
    await subscribe(acme);
    await subscribe(northwind);
    expect((await as(email, "POST", "/v1/push/subscriptions/remove", { endpoint }, northwind)).status).toBe(204);
    expect(await rows()).toEqual([acme]);
    expect((await as(email, "POST", "/v1/push/subscriptions/lookup", { endpoint }, northwind)).body).toEqual({ subscribed: false });
    expect((await as(email, "POST", "/v1/push/subscriptions/lookup", { endpoint }, acme)).body).toEqual({ subscribed: true });
  });

  test("sign-out's removal, once per membership, leaves none", async () => {
    const { email, endpoint, rows, subscribe } = await twoOrgs();
    await subscribe(acme);
    await subscribe(northwind);
    for (const org of [acme, northwind]) expect((await as(email, "POST", "/v1/push/subscriptions/remove", { endpoint }, org)).status).toBe(204);
    expect(await rows()).toEqual([]);
  });

  test("another member's removal in Acme names the same endpoint and still touches only Acme", async () => {
    const { endpoint, rows, subscribe } = await twoOrgs();
    const other = at(unique("colleague"));
    await member(owner, acme, other);
    await subscribe(acme);
    await subscribe(northwind);
    // Not a Northwind member: its removal there is refused before any row is read.
    expect((await as(other, "POST", "/v1/push/subscriptions/remove", { endpoint }, northwind)).status).toBe(403);
    expect(await rows()).toEqual([acme, northwind].sort());
    // In Acme, where it is a member, the endpoint's Acme row goes and Northwind's stays.
    expect((await as(other, "POST", "/v1/push/subscriptions/remove", { endpoint }, acme)).status).toBe(204);
    expect(await rows()).toEqual([northwind]);
  });
});

describe("the welcome, once per membership", () => {
  const welcomedAt = async (email: string) => {
    const me = await as(email, "GET", "/v1/me", undefined, null);
    return Object.fromEntries((me.body.organizations as Json[]).map((o) => [o.id, o.welcomedAt]));
  };

  test("welcomed sets your own row's welcomedAt in that organisation only, and again keeps the first time", async () => {
    const email = at(unique("newcomer"));
    const inAcme = await member(owner, acme, email);
    await member(owner, northwind, email);
    const other = await member(owner, acme, at(unique("bystander")));
    expect(await welcomedAt(email)).toEqual({ [acme]: null, [northwind]: null });
    const first = await as(email, "POST", `/v1/me/memberships/${acme}/welcomed`, {}, acme);
    expect(first.status).toBe(200);
    const after = await welcomedAt(email);
    expect(after[acme]).not.toBeNull();
    expect(after[northwind]).toBeNull();
    const again = await as(email, "POST", `/v1/me/memberships/${acme}/welcomed`, {}, acme);
    expect(again.body.welcomedAt).toBe(first.body.welcomedAt);
    expect(await owner`SELECT id FROM people WHERE welcomed_at IS NOT NULL AND id IN (${inAcme}, ${other})`).toEqual([{ id: inAcme }] as never);
  });

  test("an organisation that is not the request's, or not yours, is refused", async () => {
    const email = at(unique("newcomer"));
    await member(owner, acme, email);
    await member(owner, northwind, email);
    expect((await as(email, "POST", `/v1/me/memberships/${northwind}/welcomed`, {}, acme)).status).toBe(403);
    const outsider = at(unique("outsider"));
    await member(owner, northwind, outsider);
    expect((await as(outsider, "POST", `/v1/me/memberships/${acme}/welcomed`, {}, acme)).status).toBe(403);
    expect(await welcomedAt(email)).toEqual({ [acme]: null, [northwind]: null });
  });
});

describe("the Invitations page past one page", () => {
  /**
   * An organisation of its own with 66 invitations, `bulk00`..`bulk65`: 57 pending, 4 expired,
   * 3 accepted, 2 revoked; and its admin.
   */
  async function bulkOrganization() {
    const id = `${acme}_${unique("bulk")}`;
    await owner`INSERT INTO organizations (id, name, slug) VALUES (${id}, 'Bulk', ${id})`;
    const day = 86_400_000;
    const rows = Array.from({ length: 66 }, (_, i) => {
      const n = String(i).padStart(2, "0");
      return { id: `inv_${id}_${n}`, organization_id: id, code: `code${id}${n}`, email: at(`bulk${n}`), role: "member",
        expires_at: new Date(Date.now() + (i >= 57 && i < 61 ? -day : day)),
        accepted_at: i >= 61 && i < 64 ? new Date() : null, revoked_at: i >= 64 ? new Date() : null };
    });
    await owner`INSERT INTO invitations ${owner(rows)}`;
    const admin = at(unique("admin"));
    await member(owner, id, admin, { role: "admin" });
    return { id, admin };
  }
  const ALL_66 = { pending: 57, expired: 4, accepted: 3, revoked: 2, all: 66 };

  test("counts every status of more than 50, under the organisation's rows only", async () => {
    const { id, admin } = await bulkOrganization();
    await owner`INSERT INTO invitations (id, organization_id, code, email, role) VALUES (${`inv_${id}_acme`}, ${acme}, ${`acme${id}`}, ${at("acme-only")}, 'member')`;
    expect(await as(admin, "GET", "/v1/organization/invitations/counts", undefined, id)).toEqual({ status: 200, body: ALL_66 });
  });

  test("the cursor pages the list to the end, and a search pages from its own start", async () => {
    const { id, admin } = await bulkOrganization();
    const page1 = await as(admin, "GET", "/v1/organization/invitations?status=pending", undefined, id);
    expect(page1.body.invitations).toHaveLength(50);
    const page2 = await as(admin, "GET", `/v1/organization/invitations?status=pending&cursor=${page1.body.cursor}`, undefined, id);
    expect(page2.body.invitations).toHaveLength(7);
    expect(page2.body.cursor).toBeNull();
    const ids = [...page1.body.invitations, ...page2.body.invitations].map((i: Json) => i.id);
    expect(new Set(ids).size).toBe(57);
    const searched = await as(admin, "GET", "/v1/organization/invitations?status=all&q=bulk6", undefined, id);
    expect(searched.body.invitations.map((i: Json) => i.email).sort()).toEqual([60, 61, 62, 63, 64, 65].map((i) => at(`bulk${i}`)));
    expect(searched.body.cursor).toBeNull();
  });

  test("a member is refused the counts", async () => {
    const { id } = await bulkOrganization();
    const plain = at(unique("plain"));
    await member(owner, id, plain);
    expect((await as(plain, "GET", "/v1/organization/invitations/counts", undefined, id)).status).toBe(403);
  });

  test("the super-admin counts any organisation's, one they are not in, while in another; its own admin is refused that route", async () => {
    const target = await bulkOrganization();
    // The super-admin's own organisation, with counts unlike the target's.
    await member(owner, acme, operator, { role: "admin" });
    const own = unique("own");
    await owner`INSERT INTO invitations (id, organization_id, code, email, role) VALUES (${`inv_${own}`}, ${acme}, ${own}, ${at(own)}, 'member')`;
    const ownCounts = await as(operator, "GET", "/v1/organization/invitations/counts", undefined, acme);
    expect(ownCounts.status).toBe(200);
    expect(ownCounts.body).not.toEqual(ALL_66);
    expect(await owner`SELECT 1 FROM people p JOIN accounts a ON a.id = p.account_id
      WHERE a.email = ${operator} AND p.organization_id = ${target.id}`).toEqual([] as never);
    const path = `/v1/admin/organizations/${target.id}/invitations/counts`;
    expect(await as(operator, "GET", path, undefined, acme)).toEqual({ status: 200, body: ALL_66 });
    // The target's own admin is no super-admin: refused the super-admin's route, even for their organisation.
    const refused = await as(target.admin, "GET", path, undefined, target.id);
    expect(refused).toEqual({ status: 403, body: { error: expect.objectContaining({ code: "super_admin_required" }) } });
  });
});

test("migration 122 keeps every row a database holds from before it, and the claim still works on them", async () => {
  const name = `dude_push122_${suffix}`;
  const admin = new SQL(process.env.DATABASE_URL!);
  await admin.unsafe(`CREATE DATABASE "${name}"`);
  const url = new URL(process.env.DATABASE_URL!);
  url.pathname = `/${name}`;
  const legacy = new SQL(url.toString(), { connection: { TimeZone: "UTC" } });
  try {
    await legacy`CREATE TABLE schema_migrations (version text PRIMARY KEY, name text NOT NULL,
      checksum text NOT NULL, applied_at timestamptz NOT NULL DEFAULT now())`;
    // As migrate() applies them, stopping before 122.
    for (const file of await listMigrationFiles()) {
      if (file.version >= "122") continue;
      const contents = await file.contents();
      const record = (sql: SQL) => sql`INSERT INTO schema_migrations (version, name, checksum)
        VALUES (${file.version}, ${file.name}, ${createHash("sha256").update(contents).digest("hex")})`;
      if (outsideTransaction(contents)) {
        await legacy.unsafe(contents);
        await record(legacy);
      } else {
        await legacy.begin(async (tx) => {
          await tx.unsafe(contents);
          await record(tx);
        });
      }
    }
    // Today's rows: one browser per organisation, as the endpoint-only key allowed.
    for (const org of ["org_a", "org_b"]) {
      await legacy`INSERT INTO organizations (id, name, slug) VALUES (${org}, ${org}, ${org})`;
      await legacy`INSERT INTO people (id, organization_id, name) VALUES (${`per_${org}`}, ${org}, 'P')`;
      await legacy`INSERT INTO push_subscriptions (endpoint, organization_id, person_id, p256dh, auth, created_at)
        VALUES (${`https://push.example.test/${org}`}, ${org}, ${`per_${org}`}, 'k', 'a', '2026-01-02T03:04:05Z')`;
    }
    const before = await legacy`SELECT * FROM push_subscriptions ORDER BY endpoint`;
    await migrate(url.toString(), { log() {} });
    expect(await legacy`SELECT * FROM push_subscriptions ORDER BY endpoint`).toEqual(before);
    const [key] = await legacy`SELECT array_agg(a.attname ORDER BY a.attname) AS cols FROM pg_index i
      JOIN pg_attribute a ON a.attrelid = i.indrelid AND a.attnum = ANY(i.indkey)
      WHERE i.indrelid = 'push_subscriptions'::regclass AND i.indisprimary`;
    expect(key.cols).toEqual(["endpoint", "organization_id"]);
    // org_a's browser registered in org_b too: a second row, the first untouched.
    await legacy.begin(async (tx) => {
      await tx`SET LOCAL ROLE dude_app`;
      await tx`SELECT set_config('app.organization_id', 'org_b', true)`;
      await tx`SELECT claim_push_subscription('https://push.example.test/org_a', 'org_b', 'per_org_b', NULL, 'k2', 'a2')`;
    });
    expect(await legacy`SELECT endpoint, organization_id, p256dh FROM push_subscriptions ORDER BY endpoint, organization_id`).toEqual([
      { endpoint: "https://push.example.test/org_a", organization_id: "org_a", p256dh: "k" },
      { endpoint: "https://push.example.test/org_a", organization_id: "org_b", p256dh: "k2" },
      { endpoint: "https://push.example.test/org_b", organization_id: "org_b", p256dh: "k" },
    ] as never);
  } finally {
    await legacy.end();
    await admin.unsafe(`DROP DATABASE IF EXISTS "${name}" WITH (FORCE)`);
    await admin.end();
  }
}, 120_000);
