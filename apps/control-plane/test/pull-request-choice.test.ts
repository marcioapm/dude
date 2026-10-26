/**
 * Before the pull request (`pullRequest`) through the settings API: a
 * project's setting only. Its source on a project is "default" until the
 * project sets it, Reset takes it back there, the organisation's settings
 * refuse it, and a value an organisation stored is not read.
 *
 * Against a database of its own, migrated as a release is, and a stand-in
 * for the orchestrator that answers its defaults.
 *
 * Requires DATABASE_URL: a role that can create databases (the owner).
 */

import { afterAll, beforeAll, describe, expect, test } from "bun:test";
import { SQL } from "bun";
import { join } from "node:path";
import { promptRoleSchema } from "@dude/domain";
import { closePool, setPool } from "../src/db/client.ts";
import { buildRouter } from "../src/index.ts";
import { Config, useConfig } from "../src/config.ts";
import type { Router } from "../src/api/router.ts";
import { createApiKey } from "../src/api/auth.ts";

const OWNER_URL = process.env.DATABASE_URL ?? "postgres://dude:dude@localhost:5433/dude";
const ROOT = join(import.meta.dir, "../../..");
const NAME = `dude_prchoice_test_${Bun.randomUUIDv7("hex").slice(-12)}`;
const ORG = "org_prc";
const PROJECT = "prj_prc";

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
let adminKey: string;
let orchestratorServer: ReturnType<typeof Bun.serve>;

type Json = any; // eslint-disable-line @typescript-eslint/no-explicit-any

function call(method: string, path: string, payload?: unknown) {
  return router.handle(new Request(`http://dude.test${path}`, {
    method,
    headers: { authorization: `Bearer ${adminKey}`, "content-type": "application/json" },
    ...(payload === undefined ? {} : { body: JSON.stringify(payload) }),
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
  await owner`INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES (${PROJECT}, ${ORG}, 'web', 'web', 'WEB')`;
  app = new SQL(databaseUrl(true));
  setPool(app);
  adminKey = (await createApiKey({ organizationId: ORG, name: "Ana" })).key;
  orchestratorServer = Bun.serve({
    port: 0,
    fetch(req) {
      const path = new URL(req.url).pathname;
      if (path === "/internal/network/defaults") return Response.json({ operator: [], always: [], model: null });
      if (path.endsWith("builtin")) return Response.json(Object.fromEntries(promptRoleSchema.options.map((r) => [r, "Built-in prompt"])));
      return Response.json({ requiredReviewers: ["correctness"], blockingSeverities: ["blocking"], maxReviewIterations: 3,
        maxAttemptsPerFinding: 2, maxPrFixIterations: 3, simplify: true, test: true, parkAfterMinutes: 10, idleNudgeMinutes: 0,
        conductorWarmMinutes: 5, pullRequest: "open" });
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

const projectChoice = async () => ((await (await call("GET", `/v1/projects/${PROJECT}/settings`)).json()) as Json).delivery.pullRequest;

describe("before the pull request", () => {
  test("a project reads the factory's Open it, from the default", async () => {
    expect(await projectChoice()).toEqual({ value: "open", source: "default" });
  });

  test("an organisation cannot set it, and a value it stored is not read", async () => {
    const res = await call("PATCH", "/v1/settings/organization", { delivery: { pullRequest: "ask" } });
    expect(res.status).toBe(400);
    expect(JSON.stringify(await res.json())).toContain("project's setting");
    await owner`UPDATE organizations SET delivery_policy = '{"pullRequest":"ask"}' WHERE id = ${ORG}`;
    expect(await projectChoice()).toEqual({ value: "open", source: "default" });
  });

  test("a project sets it, and Reset puts the default back", async () => {
    const res = await call("PATCH", `/v1/projects/${PROJECT}/settings`, { delivery: { pullRequest: "ask" } });
    expect(res.status).toBe(200);
    expect(((await res.json()) as Json).delivery.pullRequest).toEqual({ value: "ask", source: "project" });
    const [{ delivery_policy }] = await owner`SELECT delivery_policy FROM projects WHERE id = ${PROJECT}`;
    expect(delivery_policy).toEqual({ pullRequest: "ask" });
    expect((await call("PATCH", `/v1/projects/${PROJECT}/settings`, { delivery: { pullRequest: "later" } })).status).toBe(400);
    const reset = await call("PATCH", `/v1/projects/${PROJECT}/settings`, { delivery: { pullRequest: null } });
    expect(((await reset.json()) as Json).delivery.pullRequest).toEqual({ value: "open", source: "default" });
  });
});
