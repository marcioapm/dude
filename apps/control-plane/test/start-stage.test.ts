/**
 * A Run's start stage (startStage), as a task read gives it: while lux has
 * not reported the Run running, its latest stage; once lux says running,
 * only after a setup script (dude's stage, which lux reports running
 * through), until the agent's first record. The orchestrator's twin is
 * phases.StartStageJSON.
 */

import { afterAll, beforeAll, expect, test } from "bun:test";
import { SQL } from "bun";
import { personPrincipal } from "../src/api/auth.ts";
import { Router } from "../src/api/router.ts";
import { registerWorkRoutes } from "../src/api/routes/work.ts";
import { closePool, setPool } from "../src/db/client.ts";

const org = `org_startstage_${Bun.randomUUIDv7("hex").slice(-12)}`;
const person = `${org}_person`;
const project = `${org}_p`;
let owner: SQL;
let app: SQL;
let router: Router;
let tasks = 0;
let events = 0;

beforeAll(async () => {
  owner = new SQL(process.env.DATABASE_URL!);
  const url = new URL(process.env.DATABASE_URL!);
  url.username = "dude_app";
  url.password = "dude_app";
  app = new SQL(url.toString());
  setPool(app);
  await owner`INSERT INTO organizations (id, name, slug) VALUES (${org}, ${org}, ${org})`;
  await owner`INSERT INTO people (id, organization_id, name, role) VALUES (${person}, ${org}, 'Person', 'admin')`;
  await owner`INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES (${project}, ${org}, 'P', ${project}, 'P')`;
  const principal = await personPrincipal(org, person);
  router = new Router(async () => principal);
  registerWorkRoutes(router);
});

afterAll(async () => {
  await closePool(app);
  await owner`DELETE FROM organizations WHERE id = ${org}`;
  await owner.end();
});

/** A task with one Run that lux reports running; its id and its Run's. */
async function runningRun(): Promise<{ task: string; run: string }> {
  tasks += 1;
  const task = `${org}_t${tasks}`;
  const run = `${org}_r${tasks}`;
  await owner`INSERT INTO tasks (id, organization_id, project_id, number, title, goal)
    VALUES (${task}, ${org}, ${project}, ${tasks}, 'T', 'Greet each person who comes in')`;
  await owner`INSERT INTO runs (id, organization_id, project_id, task_id, attempt, phase, status, lux_run_id, lux_state)
    VALUES (${run}, ${org}, ${project}, ${task}, 1, 'implement', 'running', ${`lrun_${tasks}`}, 'running')`;
  return { task, run };
}

async function record(ids: { task: string; run: string }, type: string, payload: Record<string, unknown>) {
  events += 1;
  await owner`INSERT INTO events (id, organization_id, event_type, task_id, run_id, actor_type, actor_id, source, payload)
    VALUES (${`${org}_ev${events}`}, ${org}, ${type}, ${ids.task}, ${ids.run}, 'system', 'system', 'orchestrator', ${payload})`;
}

const stage = (ids: { task: string; run: string }, name: string, more: Record<string, unknown> = {}) =>
  record(ids, "run.stage", { stage: name, since: "2026-11-24T12:00:00Z", epoch: 1, ...more });

async function startStage(ids: { task: string; run: string }): Promise<string | null> {
  const res = await router.handle(new Request(`http://dude.test/v1/tasks/${ids.task}`));
  expect(res.status).toBe(200);
  const body = await res.json() as { runs: Array<{ id: string; startStage: { stage: string } | null }> };
  const run = body.runs.find((r) => r.id === ids.run)!;
  return run.startStage?.stage ?? null;
}

test("a setup script's start lasts, though lux says running, until the agent's first record", async () => {
  const ids = await runningRun();
  await stage(ids, "container");
  await stage(ids, "running");
  expect(await startStage(ids)).toBeNull();
  await stage(ids, "setup", { repository: "abs" });
  expect(await startStage(ids)).toBe("setup");
  await record(ids, "run.setup.progress", { repository: "abs", line: "npm ci", epoch: 1 });
  expect(await startStage(ids)).toBe("setup");
  await stage(ids, "running", { setup: true });
  expect(await startStage(ids)).toBe("running");
  await record(ids, "agent.session.started", { role: "implement" });
  expect(await startStage(ids)).toBeNull();
});

test("a plain running stage written once lux says running is no start", async () => {
  const ids = await runningRun();
  await stage(ids, "container");
  await stage(ids, "running");
  expect(await startStage(ids)).toBeNull();
});
