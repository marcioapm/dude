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

/** A Run: id, attempt, created (= started) and ended so many minutes ago (ended null: still going), status. */
type RunRow = [id: string, attempt: number, startedMin: number, endedMin: number | null, status: string];
/** A `task.status_changed`: the status, so many minutes ago. Written in the order given, which is cursor order. */
type StatusRow = [status: string, min: number];

/** A task of agent Runs and the statuses it went through, all on its own clock. */
async function taskWith(id: string, number: number, title: string, status: string, createdMin: number, runs: RunRow[], statuses: StatusRow[]) {
  await owner`INSERT INTO tasks (id, organization_id, project_id, number, title, status, created_at)
              VALUES (${id}, ${ORG}, ${PROJECT}, ${number}, ${title}, ${status}::task_status, now() - make_interval(mins => ${createdMin}))`;
  for (const [run, attempt, startedMin, endedMin, runStatus] of runs) {
    await owner`INSERT INTO runs (id, organization_id, project_id, task_id, attempt, created_at, started_at, ended_at, status)
                VALUES (${run}, ${ORG}, ${PROJECT}, ${id}, ${attempt}, now() - make_interval(mins => ${startedMin}),
                        now() - make_interval(mins => ${startedMin}), now() - make_interval(mins => ${endedMin}::int), ${runStatus}::run_status)`;
  }
  for (const [n, [to, min]] of statuses.entries()) {
    await owner`INSERT INTO events (id, organization_id, event_type, task_id, actor_type, actor_id, source, payload, occurred_at)
                VALUES (${`evt_${id}_${n + 1}`}, ${ORG}, 'task.status_changed', ${id}, 'system', 'orchestrator', 'orchestrator',
                        jsonb_build_object('status', ${to}::text), now() - make_interval(mins => ${min}))`;
  }
}

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
  // Branch previews carry an attempt and cost machine time, but are not the
  // attempt's agents; attempt 2's started before attempt 2's first agent Run.
  await owner`INSERT INTO runs (id, organization_id, project_id, task_id, attempt, kind, agent_cost_usd,
                                created_at, started_at, ended_at, status)
              VALUES ('run_a1_prev', ${ORG}, ${PROJECT}, 'wi_1', 1, 'preview', 3.00,
                      now() - interval '160 minutes', now() - interval '160 minutes', now() - interval '140 minutes', 'completed'),
                     ('run_a2_prev', ${ORG}, ${PROJECT}, 'wi_1', 2, 'preview', 4.00,
                      now() - interval '90 minutes', now() - interval '90 minutes', now() - interval '80 minutes', 'completed')`;
  // A person took 10 minutes over attempt 1's reviewer's repository
  // request, and 20 over attempt 2's implementer's question.
  await owner`INSERT INTO repositories (id, organization_id, project_id, name, url)
              VALUES ('repo_att', ${ORG}, ${PROJECT}, 'web', 'https://example.test/web.git')`;
  await owner`INSERT INTO repository_requests (id, organization_id, task_id, run_id, repository_id, reason, status, created_at, decided_at)
              VALUES ('rr_a1', ${ORG}, 'wi_1', 'run_a1_rev', 'repo_att', 'needs the api', 'approved',
                      now() - interval '140 minutes', now() - interval '130 minutes')`;
  await owner`INSERT INTO questions (id, organization_id, task_id, run_id, prompt, status, asked_at, answered_at)
              VALUES ('q_a2', ${ORG}, 'wi_1', 'run_a2_impl', 'Which colour?', 'answered',
                      now() - interval '50 minutes', now() - interval '30 minutes')`;
  // In review from 90 to 30 minutes ago, across attempt 2's start an hour ago.
  await owner`INSERT INTO events (id, organization_id, event_type, task_id, actor_type, actor_id, source, payload, occurred_at)
              VALUES ('evt_rev', ${ORG}, 'task.status_changed', 'wi_1', 'system', 'orchestrator', 'orchestrator',
                      '{"status": "review"}', now() - interval '90 minutes'),
                     ('evt_run', ${ORG}, 'task.status_changed', 'wi_1', 'system', 'orchestrator', 'orchestrator',
                      '{"status": "running"}', now() - interval '30 minutes')`;

  // A task done after its second attempt; its first was aborted earlier.
  await taskWith("wi_2", 2, "Done on the second go", "done", 300,
    [["run_b1", 1, 240, 180, "aborted"], ["run_b2", 2, 120, 90, "completed"]],
    [["aborted", 180], ["running", 120], ["review", 90], ["done", 30]]);

  // Done after its second attempt, which was itself aborted and then tried
  // again (same attempt) before it went to review and finished.
  await taskWith("wi_3", 3, "Tried again, then done", "done", 360,
    [["run_c1", 1, 300, 240, "aborted"], ["run_c2", 2, 180, 150, "aborted"], ["run_c3", 2, 120, 90, "completed"]],
    [["aborted", 240], ["running", 180], ["aborted", 150], ["running", 120], ["review", 90], ["done", 30]]);

  // Its second attempt was aborted two hours ago and resumed an hour ago;
  // it is running now.
  await taskWith("wi_4", 4, "Resumed", "running", 360,
    [["run_d1", 1, 300, 240, "aborted"], ["run_d2", 2, 180, null, "running"]],
    [["aborted", 240], ["running", 180], ["aborted", 120], ["running", 60]]);

  // Attempt 2 is working, with a question asked 15 minutes ago and a
  // repository asked for 5 minutes ago, both still waiting on a person;
  // it was in review 50 to 40 minutes ago, then ready to merge until 20.
  await taskWith("wi_5", 5, "Waiting", "running", 240,
    [["run_e1", 1, 180, 120, "aborted"], ["run_e2", 2, 60, null, "running"]],
    [["review", 50], ["ready_to_merge", 40], ["running", 20]]);
  await owner`INSERT INTO questions (id, organization_id, task_id, run_id, prompt, status, asked_at)
              VALUES ('q_e2', ${ORG}, 'wi_5', 'run_e2', 'Which font?', 'open', now() - interval '15 minutes')`;
  await owner`INSERT INTO repository_requests (id, organization_id, task_id, run_id, repository_id, reason, status, created_at)
              VALUES ('rr_e2', ${ORG}, 'wi_5', 'run_e2', 'repo_att', 'needs the api', 'pending', now() - interval '5 minutes')`;

  // Aborted for good an hour ago, during its second attempt, after an hour in review.
  await taskWith("wi_6", 6, "Aborted", "aborted", 300,
    [["run_f1", 1, 240, 180, "aborted"], ["run_f2", 2, 180, 60, "aborted"]],
    [["aborted", 240], ["running", 180], ["review", 120], ["aborted", 60]]);

  // Its conductor was written to before anything was delivered (attempt 1,
  // 5 h ago), and a second conductor is recorded on attempt 2 at 200
  // minutes, before attempt 2's first agent 2 h ago. Attempt 1's
  // implementer ran 4 h to 3 h ago.
  await taskWith("wi_7", 7, "Asked first", "running", 360,
    [["run_g1", 1, 240, 180, "aborted"], ["run_g2", 2, 120, null, "running"]], []);
  await owner`INSERT INTO runs (id, organization_id, project_id, task_id, attempt, role, agent_cost_usd, input_tokens, output_tokens,
                                created_at, started_at, ended_at, status)
              VALUES ('run_g_cond1', ${ORG}, ${PROJECT}, 'wi_7', 1, 'conductor', 0.50, 300, 30,
                      now() - interval '300 minutes', now() - interval '300 minutes', now() - interval '290 minutes', 'completed'),
                     ('run_g_cond2', ${ORG}, ${PROJECT}, 'wi_7', 2, 'conductor', 0.25, 200, 20,
                      now() - interval '200 minutes', now() - interval '200 minutes', now() - interval '190 minutes', 'completed')`;
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
  humanWaitMs: number;
  reviewMs: number;
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
const MINUTE = 60_000;

const near = (actual: number, expected: number, slack = 5000) => {
  expect(actual).toBeGreaterThan(expected - slack);
  expect(actual).toBeLessThan(expected + slack);
};

test("an attempt's figures are its Runs' alone", async () => {
  const one = await get("/v1/tasks/wi_1/metrics?attempt=1");
  expect(one.runs.map((r) => r.id)).toEqual(["run_a1_impl", "run_a1_rev"]);
  expect(one.costUsd).toBeCloseTo(1.75, 9);
  expect(one.tokens).toEqual({ input: 1400, output: 140 });
  near(one.activeMs, HOUR);
  // From its first Run until attempt 2 began: two hours.
  near(one.leadMs, 2 * HOUR);

  const two = await get("/v1/tasks/wi_1/metrics?attempt=2");
  expect(two.runs.map((r) => r.id)).toEqual(["run_a2_impl"]);
  expect(two.costUsd).toBeCloseTo(2.0, 9);
  expect(two.tokens).toEqual({ input: 5000, output: 500 });
  expect(two.leadMs).toBeGreaterThan(HOUR - 5000);
  expect(two.leadMs).toBeLessThan(HOUR + 60_000);
});

test("an attempt waits on people only over its own Runs' questions and requests", async () => {
  near((await get("/v1/tasks/wi_1/metrics?attempt=1")).humanWaitMs, 10 * MINUTE);
  near((await get("/v1/tasks/wi_1/metrics?attempt=2")).humanWaitMs, 20 * MINUTE);
});

test("a review that ran across a start over counts on each attempt only for its own part", async () => {
  // In review from 90 to 30 minutes ago; attempt 2 began an hour ago.
  near((await get("/v1/tasks/wi_1/metrics?attempt=1")).reviewMs, 30 * MINUTE);
  near((await get("/v1/tasks/wi_1/metrics?attempt=2")).reviewMs, 30 * MINUTE);
});

test("a branch preview is not one of an attempt's agents, and does not end the attempt before it", async () => {
  const one = await get("/v1/tasks/wi_1/metrics?attempt=1");
  expect(one.costUsd).toBeCloseTo(1.75, 9);
  // Attempt 2's preview started 90 minutes ago; attempt 1 lasted until attempt 2's first agent, 1 h ago.
  near(one.leadMs, 2 * HOUR);
  expect((await get("/v1/tasks/wi_1/metrics?attempt=2")).costUsd).toBeCloseTo(2.0, 9);
});

test("the latest attempt of a finished task ends when the task did, not at an earlier attempt's abort", async () => {
  const two = await get("/v1/tasks/wi_2/metrics?attempt=2");
  near(two.leadMs, 90 * MINUTE);
  near(two.reviewMs, HOUR);
  const one = await get("/v1/tasks/wi_2/metrics?attempt=1");
  near(one.leadMs, 2 * HOUR);
  expect(one.reviewMs).toBe(0);
});

test("an attempt aborted, tried again and then done ends when it was done", async () => {
  // Attempt 2 began 3 h ago and was done 30 minutes ago, in review for the last hour of it.
  const two = await get("/v1/tasks/wi_3/metrics?attempt=2");
  near(two.leadMs, 150 * MINUTE);
  near(two.reviewMs, HOUR);
});

test("the latest attempt of an aborted task ends at the abort, with its review before it", async () => {
  const two = await get("/v1/tasks/wi_6/metrics?attempt=2");
  near(two.leadMs, 2 * HOUR);
  near(two.reviewMs, HOUR);
});

test("an attempt aborted and resumed, running now, runs until now", async () => {
  near((await get("/v1/tasks/wi_4/metrics?attempt=2")).leadMs, 3 * HOUR);
});

test("a conductor's Run is counted in its attempt but neither begins it nor ends the one before", async () => {
  const one = await get("/v1/tasks/wi_7/metrics?attempt=1");
  expect(one.runs.map((r) => r.id)).toEqual(["run_g_cond1", "run_g1"]);
  // From the implementer 4 h ago until attempt 2's first agent 2 h ago.
  near(one.leadMs, 2 * HOUR);
  // From attempt 2's first agent, 2 h ago, until now.
  near((await get("/v1/tasks/wi_7/metrics?attempt=2")).leadMs, 2 * HOUR);
});

test("a conductor's Run costs its own attempt, and no other", async () => {
  // The conductors cost $0.50 (attempt 1) and $0.25 (attempt 2); the agents cost nothing.
  const one = await get("/v1/tasks/wi_7/metrics?attempt=1");
  expect(one.costUsd).toBeCloseTo(0.5, 9);
  expect(one.tokens).toEqual({ input: 300, output: 30 });
  const two = await get("/v1/tasks/wi_7/metrics?attempt=2");
  expect(two.costUsd).toBeCloseTo(0.25, 9);
  expect(two.tokens).toEqual({ input: 200, output: 20 });
});

test("a question or a repository request still waiting on a person counts until now", async () => {
  near((await get("/v1/tasks/wi_5/metrics?attempt=2")).humanWaitMs, 20 * MINUTE);
});

test("ready to merge is time in review", async () => {
  near((await get("/v1/tasks/wi_5/metrics?attempt=2")).reviewMs, 30 * MINUTE);
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
  for (const bad of ["0", "-1", "1.5", "two", "2147483648", "9999999999"]) {
    const res = await call(`/v1/tasks/wi_1/metrics?attempt=${bad}`);
    expect(res.status).toBe(400);
    expect(((await res.json()) as { error: { code: string } }).error.code).toBe("bad_request");
  }
});

test("the largest attempt runs.attempt can hold is taken, and has no Runs", async () => {
  expect((await get("/v1/tasks/wi_1/metrics?attempt=2147483647")).runs).toEqual([]);
});

test("an empty attempt is the whole task, as for any number parameter", async () => {
  expect((await get("/v1/tasks/wi_1/metrics?attempt=")).runs).toHaveLength(3);
});
