/**
 * A task's time and cost for one attempt, through GET /v1/tasks/:id/metrics?attempt=N:
 * every figure is that attempt's Runs' alone; without it, the whole task's.
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
const NAME = `dude_attmetrics_test_${Bun.randomUUIDv7("hex").slice(-12)}`;
const ORG = "org_att";
const PROJECT = "prj_att";

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
  await owner`INSERT INTO tasks (id, organization_id, project_id, number, title, created_at)
              VALUES ('wi_1', ${ORG}, ${PROJECT}, 1, 'Started over', now() - interval '5 hours')`;
  // Attempt 1: an implementer and a reviewer, 3 h ago for an hour, set aside.
  await owner`INSERT INTO runs (id, organization_id, project_id, task_id, attempt, agent_cost_usd, input_tokens, output_tokens,
                                created_at, started_at, ended_at, status)
              VALUES ('run_a1_impl', ${ORG}, ${PROJECT}, 'wi_1', 1, 1.25, 1000, 100,
                      now() - interval '3 hours', now() - interval '3 hours', now() - interval '150 minutes', 'completed'),
                     ('run_a1_rev', ${ORG}, ${PROJECT}, 'wi_1', 1, 0.50, 400, 40,
                      now() - interval '150 minutes', now() - interval '150 minutes', now() - interval '2 hours', 'aborted')`;
  // Attempt 2: one implementer, from an hour ago, still working.
  await owner`INSERT INTO runs (id, organization_id, project_id, task_id, attempt, agent_cost_usd, input_tokens, output_tokens,
                                created_at, started_at, status)
              VALUES ('run_a2_impl', ${ORG}, ${PROJECT}, 'wi_1', 2, 2.00, 5000, 500,
                      now() - interval '1 hour', now() - interval '1 hour', 'running')`;
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

type Metrics = {
  leadMs: number;
  activeMs: number;
  costUsd: number;
  tokens: { input: number; output: number };
  runs: Array<{ id: string }>;
};

const call = (path: string) => router.handle(new Request(`http://dude.test${path}`, { headers: { authorization: `Bearer ${key}` } }));

async function get(path: string): Promise<Metrics> {
  const res = await call(path);
  expect(res.status).toBe(200);
  return (await res.json()) as Metrics;
}

const HOUR = 3600_000;

test("an attempt's figures are its Runs' alone", async () => {
  const one = await get("/v1/tasks/wi_1/metrics?attempt=1");
  expect(one.runs.map((r) => r.id)).toEqual(["run_a1_impl", "run_a1_rev"]);
  expect(one.costUsd).toBeCloseTo(1.75, 9);
  expect(one.tokens).toEqual({ input: 1400, output: 140 });
  expect(one.activeMs).toBeGreaterThan(HOUR - 5000);
  expect(one.activeMs).toBeLessThan(HOUR + 5000);
  // From its first Run until attempt 2 began: two hours.
  expect(one.leadMs).toBeGreaterThan(2 * HOUR - 5000);
  expect(one.leadMs).toBeLessThan(2 * HOUR + 5000);

  const two = await get("/v1/tasks/wi_1/metrics?attempt=2");
  expect(two.runs.map((r) => r.id)).toEqual(["run_a2_impl"]);
  expect(two.costUsd).toBeCloseTo(2.0, 9);
  expect(two.tokens).toEqual({ input: 5000, output: 500 });
  expect(two.leadMs).toBeGreaterThan(HOUR - 5000);
  expect(two.leadMs).toBeLessThan(HOUR + 60_000);
});

test("without an attempt, the whole task as before", async () => {
  const all = await get("/v1/tasks/wi_1/metrics");
  expect(all.runs.map((r) => r.id)).toEqual(["run_a1_impl", "run_a1_rev", "run_a2_impl"]);
  expect(all.costUsd).toBeCloseTo(3.75, 9);
  expect(all.tokens).toEqual({ input: 6400, output: 640 });
  // From the task's creation: five hours.
  expect(all.leadMs).toBeGreaterThan(5 * HOUR - 5000);
});

test("an attempt with no Runs has nothing to count", async () => {
  const none = await get("/v1/tasks/wi_1/metrics?attempt=7");
  expect(none.runs).toEqual([]);
  expect(none.costUsd).toBe(0);
  expect(none.leadMs).toBe(0);
});

test("an attempt that is not a positive whole number is refused", async () => {
  for (const bad of ["0", "-1", "1.5", "two", "", "1e3"]) {
    const res = await call(`/v1/tasks/wi_1/metrics?attempt=${bad}`);
    expect(res.status).toBe(422);
    expect(((await res.json()) as { error: { code: string } }).error.code).toBe("invalid_attempt");
  }
});
