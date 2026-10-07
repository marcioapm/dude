/**
 * A project's preview secrets through the public API: write-only (no
 * answer ever carries a value, only its hint), changed by the project's
 * editors, read by anyone in the organization, refused where a name would
 * clash with dude's own, lux's or a server's env, and audited by name and
 * action alone.
 *
 * Against a database of its own, migrated as a release is.
 *
 * Requires DATABASE_URL: a role that can create databases (the owner).
 */

import { afterAll, beforeAll, beforeEach, describe, expect, test } from "bun:test";
import { SQL } from "bun";
import { join } from "node:path";
import { closePool, setPool } from "../src/db/client.ts";
import { buildRouter } from "../src/index.ts";
import { Config, useConfig } from "../src/config.ts";
import type { Router } from "../src/api/router.ts";
import { createApiKey } from "../src/api/auth.ts";

const OWNER_URL = process.env.DATABASE_URL ?? "postgres://dude:dude@localhost:5433/dude";
const ROOT = join(import.meta.dir, "../../..");
const NAME = `dude_secrets_test_${Bun.randomUUIDv7("hex").slice(-12)}`;
const ORG = "org_sec";
const OTHER = "org_sec_other";
const PROJECT = "prj_sec";
const OTHER_PROJECT = "prj_sec_other";

// Values no answer may carry. Each has a distinctive middle, so a leak of
// any part longer than the hint is caught, not only the whole.
const VALUE = "sk-test-LEAKCANARY-7c1e-0d5b-3f9a";
const REPLACED = "sk-live-LEAKCANARY-REPLACED-e2b8";
const PEM = "-----BEGIN PRIVATE KEY-----\nMIIEvQIBADANBgkqhkiG9w0BLEAKCANARYPEMAQEFAASCBKcwggSjAgEAAoIBAQC7\nk3Lq9bF0rT2yVf8mWJxQ1s0ZpN4cD6eH5aR7uGvKtYwE9iL3oM\n-----END PRIVATE KEY-----\n";
const VALUES = [VALUE, REPLACED, PEM];

function databaseUrl(user: string, name: string): string {
  const url = new URL(OWNER_URL);
  if (user === "app") {
    url.username = "dude_app";
    url.password = "dude_app";
  }
  url.pathname = `/${name}`;
  return url.toString();
}

let admin: SQL;
let owner: SQL;
let app: SQL;
let router: Router;
let adminKey: string;
let memberKey: string;
let otherKey: string;

type Json = any; // eslint-disable-line @typescript-eslint/no-explicit-any

/**
 * One request through the public router. Every answer is checked as it
 * arrives: no value, and no part of one, in its body, whatever the route
 * or the status.
 */
async function call(key: string, method: string, path: string, body?: unknown): Promise<{ status: number; json: Json }> {
  const res = await router.handle(
    new Request(`http://dude.test${path}`, {
      method,
      headers: { authorization: `Bearer ${key}`, "content-type": "application/json" },
      ...(body === undefined ? {} : { body: JSON.stringify(body) }),
    }),
  );
  const text = await res.text();
  const what = `${method} ${path} ${res.status}`;
  for (const v of VALUES) expect(`${what}: ${text.includes(v)}`).toBe(`${what}: false`);
  // Escaped as JSON, or in part.
  expect(`${what}: ${text.includes("LEAKCANARY")}`).toBe(`${what}: false`);
  return { status: res.status, json: text ? JSON.parse(text) : null };
}

/** A secret the scenario starts with, added as an editor would. */
async function seed(name: string, value: string): Promise<Json> {
  const added = await call(adminKey, "POST", secretsPath(), { name, value });
  expect(added.status).toBe(201);
  return added.json;
}

async function storedValue(name: string): Promise<string | undefined> {
  const [row] = await owner`SELECT value FROM project_secrets WHERE project_id = ${PROJECT} AND name = ${name}`;
  return row?.value;
}

const secretsPath = (project = PROJECT) => `/v1/projects/${project}/secrets`;

beforeAll(async () => {
  admin = new SQL(OWNER_URL);
  await admin.unsafe(`CREATE DATABASE "${NAME}"`);
  const migrate = Bun.spawnSync(["bun", "run", "apps/control-plane/src/db/migrate.ts"], {
    cwd: ROOT,
    env: { ...process.env, DATABASE_URL: databaseUrl("owner", NAME) },
  });
  if (migrate.exitCode !== 0) throw new Error(`migrate: ${migrate.stderr.toString()}`);
  owner = new SQL(databaseUrl("owner", NAME));
  for (const id of [ORG, OTHER]) await owner`INSERT INTO organizations (id, name, slug) VALUES (${id}, ${id}, ${id})`;
  await owner`INSERT INTO projects (id, organization_id, name, slug, key_prefix, runtime_image)
              VALUES (${PROJECT}, ${ORG}, 'Web', 'web', 'WEB', 'node:22')`;
  await owner`INSERT INTO projects (id, organization_id, name, slug, key_prefix, runtime_image)
              VALUES (${OTHER_PROJECT}, ${OTHER}, 'Theirs', 'theirs', 'THR', 'node:22')`;

  app = new SQL(databaseUrl("app", NAME));
  setPool(app);
  adminKey = (await createApiKey({ organizationId: ORG, name: "Ana" })).key;
  memberKey = (await createApiKey({ organizationId: ORG, name: "Bo" })).key;
  otherKey = (await createApiKey({ organizationId: OTHER, name: "Cy" })).key;
  useConfig(Config.load({ env: { ...process.env, DUDE_ORCHESTRATOR_URL: "http://127.0.0.1:9", DUDE_ORCHESTRATOR_TOKEN: "svc" } }));
  router = buildRouter("");

  const web = { name: "web", port: 3000, command: "npm run dev", env: [{ name: "PORT", value: "3000" }], autostartInPreviews: true };
  expect((await call(adminKey, "PUT", `/v1/projects/${PROJECT}/servers/web`, web)).status).toBe(200);
});

afterAll(async () => {
  useConfig(null);
  await closePool(app);
  await owner?.end();
  await admin?.unsafe(`DROP DATABASE IF EXISTS "${NAME}" WITH (FORCE)`);
  await admin?.end();
});

// Every test starts from the project as beforeAll left it: no secret, and
// the one server, web, whose env sets PORT.
beforeEach(async () => {
  await owner`DELETE FROM project_secrets WHERE project_id = ${PROJECT}`;
  await owner`DELETE FROM project_servers WHERE project_id = ${PROJECT} AND name <> 'web'`;
});

describe("a project's secrets", () => {
  test("start empty", async () => {
    expect(await call(memberKey, "GET", secretsPath())).toEqual({ status: 200, json: { secrets: [] } });
  });

  test("an editor adds one, and everyone reads its name and hint, never its value", async () => {
    const added = await call(adminKey, "POST", secretsPath(), { name: "SEED_LLM_KEY", value: VALUE });
    expect(added.status).toBe(201);
    expect(added.json).toMatchObject({ name: "SEED_LLM_KEY", hint: "3f9a", updatedBy: { name: "Ana" } });
    expect(Object.keys(added.json).sort()).toEqual(["hint", "name", "updatedAt", "updatedBy"]);

    const listed = await call(memberKey, "GET", secretsPath());
    expect(listed.json.secrets).toEqual([added.json]);

    // Stored exactly as given.
    expect(await storedValue("SEED_LLM_KEY")).toBe(VALUE);
  });

  test("a PEM keeps its newlines, and its hint is its body's", async () => {
    const added = await call(adminKey, "POST", secretsPath(), { name: "SIGNING_KEY_PEM", value: PEM });
    expect(added.status).toBe(201);
    expect(added.json.hint).toBe("L3oM");
    expect(await storedValue("SIGNING_KEY_PEM")).toBe(PEM);
  });

  test("an editor replaces a value; the hint follows it", async () => {
    await seed("SEED_LLM_KEY", VALUE);
    const replaced = await call(adminKey, "PUT", `${secretsPath()}/SEED_LLM_KEY`, { value: REPLACED });
    expect(replaced.status).toBe(200);
    expect(replaced.json).toMatchObject({ name: "SEED_LLM_KEY", hint: "e2b8" });
    expect(await storedValue("SEED_LLM_KEY")).toBe(REPLACED);
  });

  test("a member may read but not add, replace or remove", async () => {
    await seed("SEED_LLM_KEY", VALUE);
    for (const [method, path, body] of [
      ["POST", secretsPath(), { name: "MEMBER_KEY", value: REPLACED }],
      ["PUT", `${secretsPath()}/SEED_LLM_KEY`, { value: REPLACED }],
      ["DELETE", `${secretsPath()}/SEED_LLM_KEY`, undefined],
    ] as const) {
      expect((await call(memberKey, method, path, body)).status).toBe(403);
    }
    const listed = await call(memberKey, "GET", secretsPath());
    expect(listed.status).toBe(200);
    expect(listed.json.secrets.map((s: { name: string }) => s.name)).toEqual(["SEED_LLM_KEY"]);
    expect(await storedValue("SEED_LLM_KEY")).toBe(VALUE);
  });

  test("another organization's project is not found, whoever asks", async () => {
    await seed("SEED_LLM_KEY", VALUE);
    expect((await call(otherKey, "GET", secretsPath())).status).toBe(404);
    expect((await call(otherKey, "POST", secretsPath(), { name: "X", value: REPLACED })).status).toBe(404);
    expect((await call(otherKey, "PUT", `${secretsPath()}/SEED_LLM_KEY`, { value: REPLACED })).status).toBe(404);
    expect((await call(otherKey, "DELETE", `${secretsPath()}/SEED_LLM_KEY`)).status).toBe(404);
    expect((await call(adminKey, "GET", secretsPath(OTHER_PROJECT))).status).toBe(404);
    expect((await call(adminKey, "POST", secretsPath(OTHER_PROJECT), { name: "X", value: REPLACED })).status).toBe(404);
    expect(await storedValue("SEED_LLM_KEY")).toBe(VALUE);
    const [{ n }] = await owner`SELECT count(*)::int AS n FROM project_secrets WHERE name = 'X'`;
    expect(n).toBe(0);
  });

  test("a name the project has is a conflict; replacing or removing one it lacks is not found", async () => {
    await seed("SEED_LLM_KEY", VALUE);
    const dup = await call(adminKey, "POST", secretsPath(), { name: "SEED_LLM_KEY", value: REPLACED });
    expect(dup.status).toBe(409);
    expect(dup.json.error.message).toBe("There is already a SEED_LLM_KEY. Replace its value instead.");
    expect(await storedValue("SEED_LLM_KEY")).toBe(VALUE);
    expect((await call(adminKey, "PUT", `${secretsPath()}/NOPE`, { value: VALUE })).status).toBe(404);
    expect((await call(adminKey, "DELETE", `${secretsPath()}/NOPE`)).status).toBe(404);
  });

  test("a name a server's env sets is a conflict naming the server", async () => {
    const res = await call(adminKey, "POST", secretsPath(), { name: "PORT", value: VALUE });
    expect(res.status).toBe(409);
    expect(res.json.error.message).toBe("Server web sets PORT in its own environment, which would override this. Rename one of them.");
    expect(await storedValue("PORT")).toBeUndefined();
  });

  test("what cannot be a name or a value is refused", async () => {
    await seed("SEED_LLM_KEY", VALUE);
    for (const body of [
      { name: "SEED-LLM-KEY", value: VALUE },
      { name: "LUX_TOKEN", value: VALUE },
      { name: "lux_token", value: VALUE },
      { name: "GIT_TOKEN", value: VALUE },
      { name: "DUDE_TOOLS_AUTH", value: VALUE },
      { name: "L".repeat(64), value: VALUE },
      { name: "EMPTY", value: "" },
      { name: "NUL", value: `a\0${VALUE}` },
      { name: "BIG", value: VALUE + "x".repeat(32 * 1024) },
      { name: "EXTRA", value: VALUE, as: "file" },
    ]) {
      expect(`${body.name} ${(await call(adminKey, "POST", secretsPath(), body)).status}`).toBe(`${body.name} 400`);
    }
    expect((await call(adminKey, "PUT", `${secretsPath()}/SEED_LLM_KEY`, { value: "" })).status).toBe(400);
    const names = (await call(memberKey, "GET", secretsPath())).json.secrets.map((s: { name: string }) => s.name);
    expect(names).toEqual(["SEED_LLM_KEY"]);
    expect(await storedValue("SEED_LLM_KEY")).toBe(VALUE);
  });

  test("a recipe whose env sets a secret's name is refused, naming the server and the variable", async () => {
    await seed("SEED_LLM_KEY", VALUE);
    const api = { name: "api", port: 8080, command: "go run .", env: [{ name: "SEED_LLM_KEY", value: "x" }] };
    const res = await call(adminKey, "PUT", `/v1/projects/${PROJECT}/servers/api`, api);
    expect(res.status).toBe(409);
    expect(res.json.error.message).toContain("server api");
    expect(res.json.error.message).toContain("SEED_LLM_KEY");
    // Not saved, and a recipe that sets something else is.
    expect((await call(memberKey, "GET", `/v1/projects/${PROJECT}/servers`)).json.servers.map((s: { name: string }) => s.name)).toEqual(["web"]);
    expect((await call(adminKey, "PUT", `/v1/projects/${PROJECT}/servers/api`, { ...api, env: [{ name: "API_PORT", value: "8080" }] })).status).toBe(200);
  });

  test("an editor removes one", async () => {
    await seed("SEED_LLM_KEY", VALUE);
    await seed("SIGNING_KEY_PEM", PEM);
    expect((await call(adminKey, "DELETE", `${secretsPath()}/SIGNING_KEY_PEM`)).status).toBe(204);
    expect((await call(memberKey, "GET", secretsPath())).json.secrets.map((s: { name: string }) => s.name)).toEqual(["SEED_LLM_KEY"]);
    expect(await storedValue("SIGNING_KEY_PEM")).toBeUndefined();
  });

  // One sequence, in order: an add, another, a replace and a remove, each
  // audited as it happens.
  test("each change is audited by name and action alone", async () => {
    const [{ since }] = await owner`SELECT COALESCE(max(cursor), 0) AS since FROM events`;
    await seed("SEED_LLM_KEY", VALUE);
    await seed("SIGNING_KEY_PEM", PEM);
    expect((await call(adminKey, "PUT", `${secretsPath()}/SEED_LLM_KEY`, { value: REPLACED })).status).toBe(200);
    expect((await call(adminKey, "DELETE", `${secretsPath()}/SIGNING_KEY_PEM`)).status).toBe(204);
    const rows = await owner`SELECT payload FROM events WHERE project_id = ${PROJECT} AND event_type = 'settings.updated'
                             AND payload->'changed' ? 'secrets' AND cursor > ${since} ORDER BY cursor`;
    expect(rows.map((r: { payload: Json }) => r.payload.changed)).toEqual([
      { secrets: { SEED_LLM_KEY: "added" } },
      { secrets: { SIGNING_KEY_PEM: "added" } },
      { secrets: { SEED_LLM_KEY: "replaced" } },
      { secrets: { SIGNING_KEY_PEM: "removed" } },
    ]);
    const all = JSON.stringify(await owner`SELECT payload FROM events WHERE cursor > ${since}`);
    for (const v of [...VALUES, "LEAKCANARY", "3f9a", "e2b8", "L3oM"]) expect(all).not.toContain(v);
  });

  test("no answer of any route carried a value, or any part of one", async () => {
    // call checks each answer; here, the project's reads with secrets in it.
    await seed("SEED_LLM_KEY", VALUE);
    await seed("SIGNING_KEY_PEM", PEM);
    for (const path of [secretsPath(), `/v1/projects/${PROJECT}/servers`, `/v1/projects/${PROJECT}`, `/v1/projects/${PROJECT}/settings`]) {
      // Whatever it answers (the settings read asks the orchestrator, not
      // running here), its body carries no value.
      await call(memberKey, "GET", path);
    }
    expect((await call(memberKey, "GET", secretsPath())).json.secrets.map((s: { name: string }) => s.name)).toEqual(["SEED_LLM_KEY", "SIGNING_KEY_PEM"]);
  });
});

describe("migration 081", () => {
  test("secrets are the organization's alone", async () => {
    await seed("SEED_LLM_KEY", VALUE);
    const seen = await app.begin(async (tx) => {
      await tx`SELECT set_config('app.organization_id', ${OTHER}, true)`;
      return tx`SELECT name FROM project_secrets`;
    });
    expect(seen).toHaveLength(0);
    const own = await app.begin(async (tx) => {
      await tx`SELECT set_config('app.organization_id', ${ORG}, true)`;
      return tx`SELECT name FROM project_secrets`;
    });
    expect(own.map((r: { name: string }) => r.name)).toEqual(["SEED_LLM_KEY"]);
  });

  test("the sweepers cannot read them", async () => {
    await expect(owner.begin(async (tx) => {
      await tx`SET LOCAL ROLE dude_sweeper`;
      return tx`SELECT name FROM project_secrets`;
    })).rejects.toThrow(/permission denied/);
  });

  test("the database refuses a name lux would, whoever writes it", async () => {
    for (const name of ["LUX_X", "lux_x", "1X", "A-B", "A".repeat(64)]) {
      await expect((async () => owner`INSERT INTO project_secrets (project_id, organization_id, name, value, hint)
                         VALUES (${PROJECT}, ${ORG}, ${name}, 'v', 'v')`)()).rejects.toThrow();
    }
  });

  test("a secret belongs to its project's organization: another's cannot attach one to it", async () => {
    // As the other organization, under its own policy: its organization id,
    // this organization's project.
    const poisoned = app.begin(async (tx) => {
      await tx`SELECT set_config('app.organization_id', ${OTHER}, true)`;
      await tx`INSERT INTO project_secrets (project_id, organization_id, name, value, hint)
               VALUES (${PROJECT}, ${OTHER}, 'TENANT_POISON', 'poison-value', 'alue')`;
    });
    await expect(poisoned).rejects.toMatchObject({ errno: "23503" });
    const [{ n }] = await owner`SELECT count(*)::int AS n FROM project_secrets WHERE name = 'TENANT_POISON'`;
    expect(n).toBe(0);
    // The project's own organization can still add that name.
    await app.begin(async (tx) => {
      await tx`SELECT set_config('app.organization_id', ${ORG}, true)`;
      await tx`INSERT INTO project_secrets (project_id, organization_id, name, value, hint)
               VALUES (${PROJECT}, ${ORG}, 'TENANT_POISON', 'rightful-value', 'alue')`;
      await tx`DELETE FROM project_secrets WHERE project_id = ${PROJECT} AND name = 'TENANT_POISON'`;
    });
  });
});
