/**
 * Help through the public API: any member of an organisation reads the
 * guide's pages and one page, as the orchestrator serves them; a page it
 * does not have is its 404, passed on; no key, no help.
 *
 * Against a database of its own, migrated as a release is, and a stand-in
 * for the orchestrator that records what it was asked.
 *
 * Requires DATABASE_URL: a role that can create databases (the owner).
 */

import { afterAll, beforeAll, expect, test } from "bun:test";
import { SQL } from "bun";
import { join } from "node:path";
import { closePool, setPool } from "../src/db/client.ts";
import { buildRouter } from "../src/index.ts";
import { Config, useConfig } from "../src/config.ts";
import type { Router } from "../src/api/router.ts";
import { createApiKey } from "../src/api/auth.ts";

const OWNER_URL = process.env.DATABASE_URL ?? "postgres://dude:dude@localhost:5433/dude";
const ROOT = join(import.meta.dir, "../../..");
const NAME = `dude_help_api_test_${Bun.randomUUIDv7("hex").slice(-12)}`;
const ORG = "org_help";

function databaseUrl(appRole = false): string {
  const url = new URL(OWNER_URL);
  if (appRole) {
    url.username = "dude_app";
    url.password = "dude_app";
  }
  url.pathname = `/${NAME}`;
  return url.toString();
}

let admin: SQL;
let owner: SQL;
let app: SQL;
let router: Router;
let memberKey: string;
let orchestratorServer: ReturnType<typeof Bun.serve>;
const asked: { path: string; org: string | null }[] = [];
type Json = any; // eslint-disable-line @typescript-eslint/no-explicit-any

function call(key: string | null, path: string) {
  return router.handle(new Request(`http://dude.test${path}`, {
    headers: key ? { authorization: `Bearer ${key}` } : {},
  }));
}

beforeAll(async () => {
  admin = new SQL(OWNER_URL);
  await admin.unsafe(`CREATE DATABASE "${NAME}"`);
  const migrate = Bun.spawnSync(["bun", "run", "apps/control-plane/src/db/migrate.ts"], {
    cwd: ROOT, env: { ...process.env, DATABASE_URL: databaseUrl() },
  });
  if (migrate.exitCode !== 0) throw new Error(`migrate: ${migrate.stderr.toString()}`);
  owner = new SQL(databaseUrl());
  await owner`INSERT INTO organizations (id, name, slug) VALUES (${ORG}, 'Acme', 'acme')`;
  app = new SQL(databaseUrl(true));
  setPool(app);
  await createApiKey({ organizationId: ORG, name: "Ana" });
  memberKey = (await createApiKey({ organizationId: ORG, name: "Bo" })).key;
  orchestratorServer = Bun.serve({
    port: 0,
    fetch(req) {
      const path = new URL(req.url).pathname;
      asked.push({ path, org: req.headers.get("x-dude-organization") });
      if (path === "/internal/help") return Response.json({ pages: [{ slug: "settings", title: "Settings", sections: [{ anchor: "delivery", heading: "Delivery" }] }] });
      if (path === "/internal/help/settings") return Response.json({ slug: "settings", title: "Settings", sections: [{ anchor: "delivery", heading: "Delivery", text: "Text." }] });
      return Response.json({ error: { code: "not_found", message: "no help page" } }, { status: 404 });
    },
  });
  useConfig(Config.load({ env: { ...process.env,
    DUDE_ORCHESTRATOR_URL: `http://localhost:${orchestratorServer.port}`, DUDE_ORCHESTRATOR_TOKEN: "svc" } }));
  router = buildRouter("");
});

afterAll(async () => {
  useConfig(null);
  await orchestratorServer?.stop(true);
  if (app) await closePool(app);
  await owner?.end();
  await admin?.unsafe(`DROP DATABASE IF EXISTS "${NAME}" WITH (FORCE)`);
  await admin?.end();
});

test("a member reads the guide's pages and one page, from the orchestrator", async () => {
  const list = await call(memberKey, "/v1/help");
  expect(list.status).toBe(200);
  expect(((await list.json()) as Json).pages[0].sections[0].anchor).toBe("delivery");
  const page = await call(memberKey, "/v1/help/settings");
  expect(page.status).toBe(200);
  expect(((await page.json()) as Json).sections[0].text).toBe("Text.");
  expect(asked.slice(-2)).toEqual([{ path: "/internal/help", org: ORG }, { path: "/internal/help/settings", org: ORG }]);
});

test("a page the guide does not have is a 404; no key is refused before the orchestrator", async () => {
  expect((await call(memberKey, "/v1/help/nope")).status).toBe(404);
  const before = asked.length;
  expect((await call(null, "/v1/help")).status).toBe(401);
  expect(asked.length).toBe(before);
});
