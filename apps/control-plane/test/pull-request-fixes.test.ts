/**
 * Fixes on an open pull request through the settings API: every fix is
 * direct, so the retired `pullRequestFixes` an old client sends is ignored,
 * neither stored nor refused; and the narrow loop's bound
 * (`maxPrFixReviewIterations`), layered as any bound is.
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
const NAME = `dude_prfixes_test_${Bun.randomUUIDv7("hex").slice(-12)}`;
const ORG = "org_prf";
const PROJECT = "prj_prf";
const GOAL = "Let people save the filters they use every day.";

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
  await owner`INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES (${PROJECT}, ${ORG}, 'web', 'web', 'WEF')`;
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
        conductorWarmMinutes: 5, pullRequest: "open", maxPrFixReviewIterations: 2 });
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

const projectFixes = async () => ((await (await call("GET", `/v1/projects/${PROJECT}/settings`)).json()) as Json).delivery;

describe("fixes on an open pull request", () => {
  test("the retired pullRequestFixes an old client sends is ignored, on the organisation's and a project's", async () => {
    for (const path of ["/v1/settings/organization", `/v1/projects/${PROJECT}/settings`]) {
      const res = await call("PATCH", path, { delivery: { pullRequestFixes: "candidate", simplify: false } });
      expect(res.status).toBe(200);
      const delivery = ((await res.json()) as Json).delivery;
      expect(delivery.pullRequestFixes).toBeUndefined();
      expect(delivery.simplify.value).toBe(false);
    }
    const [{ delivery_policy }] = await owner`SELECT delivery_policy FROM projects WHERE id = ${PROJECT}`;
    expect(delivery_policy).toEqual({ simplify: false });
    const [{ delivery_policy: org }] = await owner`SELECT delivery_policy FROM organizations WHERE id = ${ORG}`;
    expect(org).toEqual({ simplify: false });
    await owner`UPDATE projects SET delivery_policy = '{}' WHERE id = ${PROJECT}`;
    await owner`UPDATE organizations SET delivery_policy = '{}' WHERE id = ${ORG}`;
  });

  test("the narrow loop's bound is a project's own over the factory's", async () => {
    expect((await projectFixes()).maxPrFixReviewIterations.source).toBe("organization");
    expect((await call("PATCH", `/v1/projects/${PROJECT}/settings`, { delivery: { maxPrFixReviewIterations: 0 } })).status).toBe(400);
    const res = await call("PATCH", `/v1/projects/${PROJECT}/settings`, { delivery: { maxPrFixReviewIterations: 4 } });
    expect(res.status).toBe(200);
    expect(((await res.json()) as Json).delivery.maxPrFixReviewIterations).toEqual({ value: 4, source: "project" });
  });
});
