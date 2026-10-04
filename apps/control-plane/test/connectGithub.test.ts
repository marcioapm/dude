/**
 * Connecting GitHub with where GitHub reaches dude: the token is saved and
 * the answer comes back without waiting on GitHub, the orchestrator asked
 * to register the organization's hooks in the background.
 *
 * Against a database of its own, migrated as a release is, and a stand-in
 * orchestrator that takes seconds to answer a registration that waits.
 * Requires DATABASE_URL: a role that can create databases.
 */

import { afterAll, beforeAll, expect, test } from "bun:test";
import { SQL } from "bun";
import { join } from "node:path";
import { closePool, setPool } from "../src/db/client.ts";
import { buildRouter } from "../src/index.ts";
import type { Router } from "../src/api/router.ts";
import { createApiKey } from "../src/api/auth.ts";
import { Config, useConfig } from "../src/config.ts";

const OWNER_URL = process.env.DATABASE_URL ?? "postgres://dude:dude@localhost:5433/dude";
const ROOT = join(import.meta.dir, "../../..");
const NAME = `dude_connect_test_${Bun.randomUUIDv7("hex").slice(-12)}`;
const ORG = "org_connect";

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
let key: { key: string };
let orchestrator: ReturnType<typeof Bun.serve>;
const forwarded: Array<{ path: string; body: Record<string, unknown> }> = [];

beforeAll(async () => {
  admin = new SQL(OWNER_URL);
  await admin.unsafe(`CREATE DATABASE "${NAME}"`);
  const migrate = Bun.spawnSync(["bun", "run", "apps/control-plane/src/db/migrate.ts"], {
    cwd: ROOT,
    env: { ...process.env, DATABASE_URL: databaseUrl("owner", NAME) },
  });
  if (migrate.exitCode !== 0) throw new Error(`migrate: ${migrate.stderr.toString()}`);
  owner = new SQL(databaseUrl("owner", NAME));
  await owner`INSERT INTO organizations (id, name, slug) VALUES (${ORG}, ${ORG}, ${ORG})`;
  app = new SQL(databaseUrl("app", NAME));
  setPool(app);
  const made = await createApiKey({ organizationId: ORG, name: "Ana" });
  await owner`UPDATE people SET role = 'admin' WHERE id = ${made.personId}`;
  key = made;
  orchestrator = Bun.serve({ port: 0, async fetch(request) {
    const body = (await request.json().catch(() => ({}))) as Record<string, unknown>;
    forwarded.push({ path: new URL(request.url).pathname, body });
    // A registration that waits takes as long as forty repositories would.
    if (!body.background) await Bun.sleep(3_000);
    return Response.json(body.background ? { background: true } : { repositories: [] }, { status: body.background ? 202 : 200 });
  } });
  useConfig(Config.load({ env: { ...process.env, DUDE_ORCHESTRATOR_URL: `http://localhost:${orchestrator.port}`, DUDE_ORCHESTRATOR_TOKEN: "t" } }));
  router = buildRouter("");
});

afterAll(async () => {
  orchestrator?.stop(true);
  useConfig(null);
  await closePool(app);
  await owner?.end();
  await admin?.unsafe(`DROP DATABASE IF EXISTS "${NAME}" WITH (FORCE)`);
  await admin?.end();
});

test("connecting GitHub saves the token and asks for the hooks in the background, without waiting on GitHub", async () => {
  const started = Date.now();
  const res = await router.handle(new Request("http://dude.test/v1/forge/credential", {
    method: "POST",
    headers: { authorization: `Bearer ${key.key}`, "content-type": "application/json" },
    body: JSON.stringify({ auth: "pat", secret: "ghp_new", publicUrl: "https://dude.example.com/" }),
  }));
  expect(res.status).toBe(200);
  expect(Date.now() - started).toBeLessThan(1_500);
  expect((await res.json()).registered).toEqual({ background: true });
  expect(forwarded).toEqual([{ path: "/internal/webhooks/register",
    body: { url: `https://dude.example.com/v1/webhooks/github/${ORG}`, background: true } }]);
  const [cred] = await owner`SELECT secret, public_url FROM forge_credentials WHERE organization_id = ${ORG}`;
  expect(cred).toEqual({ secret: "ghp_new", public_url: "https://dude.example.com" });
});
