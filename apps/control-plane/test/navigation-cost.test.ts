/**
 * The board's spend per task, through GET /v1/navigation: the same cost
 * the task page shows (run_metrics), by one rule — lux's AI cost for a Run
 * once lux reported it, else the harness's; never the two added.
 *
 * Against a database of its own, migrated as a release is.
 *
 * Requires DATABASE_URL: a role that can create databases (the owner).
 */

import { afterAll, beforeAll, expect, test } from "bun:test";
import { SQL } from "bun";
import { join } from "node:path";
import { closePool, setPool } from "../src/db/client.ts";
import { buildRouter } from "../src/index.ts";
import type { Router } from "../src/api/router.ts";
import { createApiKey } from "../src/api/auth.ts";

const OWNER_URL = process.env.DATABASE_URL ?? "postgres://dude:dude@localhost:5433/dude";
const ROOT = join(import.meta.dir, "../../..");
const NAME = `dude_navcost_test_${Bun.randomUUIDv7("hex").slice(-12)}`;
const ORG = "org_nav";
const PROJECT = "prj_nav";

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
let key: string;

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
  await owner`INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES (${PROJECT}, ${ORG}, 'Web', 'web', 'WEB')`;
  app = new SQL(databaseUrl("app", NAME));
  setPool(app);
  key = (await createApiKey({ organizationId: ORG, name: "Ana" })).key;
  router = buildRouter("");
});

afterAll(async () => {
  await closePool(app);
  await owner?.end();
  await admin?.unsafe(`DROP DATABASE IF EXISTS "${NAME}" WITH (FORCE)`);
  await admin?.end();
});

test("a task's spend on the board is its agents' effective model cost, the task page's", async () => {
  await owner`INSERT INTO tasks (id, organization_id, project_id, number, title) VALUES ('wi_1', ${ORG}, ${PROJECT}, 1, 'Priced')`;
  // lux priced the implementer: its $1.81 replaces the harness's $0.30.
  await owner`INSERT INTO runs (id, organization_id, project_id, task_id, attempt, agent_cost_usd, lux_ai_usd, lux_cost_status)
              VALUES ('run_impl', ${ORG}, ${PROJECT}, 'wi_1', 1, 0.30, 1.810247, 'final')`;
  // Not yet priced by lux: the harness's figure stands.
  await owner`INSERT INTO runs (id, organization_id, project_id, task_id, attempt, agent_cost_usd)
              VALUES ('run_rev', ${ORG}, ${PROJECT}, 'wi_1', 1, 0.25)`;
  // A preview is not one of the task's agents.
  await owner`INSERT INTO runs (id, organization_id, project_id, task_id, attempt, kind, lux_ai_usd)
              VALUES ('run_prev', ${ORG}, ${PROJECT}, 'wi_1', 1, 'preview', 5)`;
  // The harness's per-request deltas in the ledger are the same tokens as
  // its total: the board must not count them again.
  await owner`INSERT INTO events (id, organization_id, event_type, task_id, run_id, actor_type, actor_id, source, payload)
              VALUES ('evt_1', ${ORG}, 'agent.model.request.completed', 'wi_1', 'run_impl', 'agent', 'run_impl', 'runner', '{"costUsd": 0.30}')`;

  const res = await router.handle(new Request("http://dude.test/v1/navigation", { headers: { authorization: `Bearer ${key}` } }));
  expect(res.status).toBe(200);
  const nav = (await res.json()) as { projects: Array<{ tasks: Array<{ id: string; costUsd: number }> }> };
  const board = nav.projects[0]!.tasks.find((t) => t.id === "wi_1")!;
  expect(board.costUsd).toBeCloseTo(1.810247 + 0.25, 9);

  const metrics = (await (await router.handle(new Request("http://dude.test/v1/tasks/wi_1/metrics",
    { headers: { authorization: `Bearer ${key}` } }))).json()) as { costUsd: number };
  expect(board.costUsd).toBeCloseTo(metrics.costUsd, 9);
});
