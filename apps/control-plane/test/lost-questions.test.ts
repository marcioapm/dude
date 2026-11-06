/**
 * A task's questions as the server holds them (#404): GET /v1/tasks/:id
 * says which of its current attempt's questions are open — what it waits on
 * a person for, wherever the Run holding it is — and which were lost with
 * its latest conductor's failed Run; GET /v1/tasks/:id/recover passes the
 * orchestrator's conductor field through.
 */

import { afterAll, beforeAll, expect, test } from "bun:test";
import { SQL } from "bun";
import { createApiKey, type Principal } from "../src/api/auth.ts";
import { Router } from "../src/api/router.ts";
import { registerWorkRoutes } from "../src/api/routes/work.ts";
import { closePool, setPool } from "../src/db/client.ts";
import { Config, useConfig } from "../src/config.ts";
import { recoveryOptionsSchema, taskQuestionSchema } from "@dude/domain";

const org = `org_lostq_${Bun.randomUUIDv7("hex").slice(-12)}`;
const project = `${org}_p`;
const task = `${org}_t`;
let owner: SQL;
let app: SQL;
let router: Router;
let orch: ReturnType<typeof Bun.serve>;
const conductor = {
  runId: `${org}_r2`, status: "failed", error: "lux no longer has this Run",
  lostQuestion: { id: `${org}_q2`, prompt: "Open?" }, retry: true,
};

function call(method: string, path: string) {
  return router.handle(new Request(`http://dude.test${path}`, { method, headers: { authorization: "key" } }));
}

beforeAll(async () => {
  owner = new SQL(process.env.DATABASE_URL!);
  const url = new URL(process.env.DATABASE_URL!);
  url.username = "dude_app";
  url.password = "dude_app";
  app = new SQL(url.toString());
  setPool(app);
  await owner`INSERT INTO organizations (id, name, slug) VALUES (${org}, ${org}, ${org})`;
  const made = await createApiKey({ organizationId: org, name: "Owner" });
  await owner`UPDATE people SET role = 'admin' WHERE id = ${made.personId}`;
  const principal: Principal = { credentialKind: "api_key", apiKeyId: made.id, personId: made.personId,
    organizationId: org, kind: "user", role: "admin", name: "Owner" };
  router = new Router(async (credential) => (credential === "key" ? principal : null));
  registerWorkRoutes(router);
  orch = Bun.serve({ port: 0, fetch(request) {
    const path = new URL(request.url).pathname;
    if (path === `/internal/tasks/${task}/recover`) {
      return Response.json({ taskId: task, actions: [], attempt: 1, keptUntil: null, refused: null, retryNeedsNote: false, conductor });
    }
    return Response.json({});
  } });
  useConfig(Config.load({ env: { ...process.env,
    DUDE_ORCHESTRATOR_URL: `http://localhost:${orch.port}`, DUDE_ORCHESTRATOR_TOKEN: "test" } }));
  await owner`INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES (${project}, ${org}, 'P', ${project}, 'LQ')`;
  await owner`INSERT INTO tasks (id, organization_id, project_id, number, title, goal, acceptance_criteria, status)
    VALUES (${task}, ${org}, ${project}, 1, 'Greet', 'Say hello', '["it greets"]'::jsonb, 'awaiting_input')`;
});

afterAll(async () => {
  orch?.stop(true);
  useConfig(null);
  await closePool(app);
  await owner`DELETE FROM organizations WHERE id = ${org}`;
  await owner.end();
});

async function questions(): Promise<Array<Record<string, unknown>>> {
  const res = await call("GET", `/v1/tasks/${task}`);
  expect(res.status).toBe(200);
  const body = (await res.json()) as { questions: Array<Record<string, unknown>> };
  for (const q of body.questions) taskQuestionSchema.parse(q);
  return body.questions;
}

const run = (id: string, status: string, error: string | null = null) =>
  owner`INSERT INTO runs (id, organization_id, project_id, task_id, attempt, status, kind, role, error, created_at)
    VALUES (${id}, ${org}, ${project}, ${task}, 1, ${status}, 'agent', 'conductor', ${error}, clock_timestamp())`;
const ask = (id: string, runId: string) =>
  owner`INSERT INTO questions (id, organization_id, task_id, run_id, prompt) VALUES (${id}, ${org}, ${task}, ${runId}, 'Open?')`;

test("an open question re-bound to a placeholder is the task's; one lost with its failed conductor says how", async () => {
  // A conductor lux lost, its question moved to the placeholder in its slot.
  await run(`${org}_r0`, "paused");
  await ask(`${org}_q0`, `${org}_r0`);
  await owner.begin(async (tx) => {
    await tx`UPDATE runs SET status = 'failed', error = 'lux lost this Run while it waited on Owner', lux_lost_at = now() WHERE id = ${`${org}_r0`}`;
    await tx`INSERT INTO runs (id, organization_id, project_id, task_id, attempt, status, kind, role, created_at)
      VALUES (${`${org}_r1`}, ${org}, ${project}, ${task}, 1, 'paused', 'agent', 'conductor', clock_timestamp())`;
    await tx`UPDATE questions SET run_id = ${`${org}_r1`} WHERE id = ${`${org}_q0`}`;
  });
  let got = await questions();
  expect(got).toEqual([expect.objectContaining({ id: `${org}_q0`, status: "open", runId: `${org}_r1`, addressee: "person", endedBy: null })]);
  expect(got[0]!.addressee).toBe("person");
  expect(got[0]!.endedBy).toBeNull();

  // The placeholder failed in turn, its question cancelled with a question.closed {by: lost}.
  await owner`UPDATE questions SET status = 'answered' WHERE id = ${`${org}_q0`}`;
  await owner`UPDATE runs SET status = 'completed' WHERE id = ${`${org}_r1`}`;
  await run(`${org}_r2`, "running");
  await ask(`${org}_q2`, `${org}_r2`);
  await owner`UPDATE runs SET status = 'failed', error = 'lux no longer has this Run' WHERE id = ${`${org}_r2`}`;
  await owner`INSERT INTO events (id, organization_id, project_id, task_id, run_id, event_type, actor_type, actor_id, source, payload)
    VALUES (${`${org}_ev`}, ${org}, ${project}, ${task}, ${`${org}_r2`}, 'question.closed', 'system', 'dude', 'orchestrator',
      ${{ questionId: `${org}_q2`, by: "lost", runId: `${org}_r2` }})`;
  got = await questions();
  expect(got).toEqual([expect.objectContaining({ id: `${org}_q2`, status: "cancelled", endedBy: "lost", askerError: "lux no longer has this Run" })]);

  // Once a newer conductor runs, the old one's cancelled question is no one's.
  await run(`${org}_r3`, "running");
  expect(await questions()).toEqual([]);
});

test("questions exclude completed cancellations and previous attempts, and correlate the latest close by run and question", async () => {
  await owner`UPDATE runs SET status='completed' WHERE task_id=${task} AND status IN ('running','paused')`;
  await run(`${org}_completed`, "running");
  await ask(`${org}_completed_q`, `${org}_completed`);
  await owner`UPDATE runs SET status='completed' WHERE id=${`${org}_completed`}`;
  let response = await call("GET", `/v1/tasks/${task}`);
  let body = await response.json() as { questions: unknown[] };
  expect(body.questions).toEqual([]);

  await run(`${org}_previous`, "completed");
  // A retained previous-attempt open question must not become current.
  await ask(`${org}_previous_q`, `${org}_previous`);
  await run(`${org}_latest`, "running");
  await owner`UPDATE runs SET attempt=2 WHERE id=${`${org}_latest`}`;
  await ask(`${org}_a`, `${org}_latest`);
  await ask(`${org}_b`, `${org}_latest`);
  await owner`UPDATE runs SET status='failed', error='failed now' WHERE id=${`${org}_latest`}`;
  const close = async (suffix: string, runId: string, questionId: string, by: string) => {
    await owner`INSERT INTO events (id, organization_id, project_id, task_id, run_id, event_type, actor_type, actor_id, source, payload)
      VALUES (${`${org}_${suffix}`}, ${org}, ${project}, ${task}, ${runId}, 'question.closed', 'system', 'dude', 'orchestrator', ${ { questionId, by } })`;
  };
  await close("ca1", `${org}_latest`, `${org}_a`, "obsolete");
  await close("cb", `${org}_latest`, `${org}_b`, "run_failed");
  await close("ca2", `${org}_latest`, `${org}_a`, "lost");
  await close("unrelated", `${org}_latest`, "not-this-question", "withdrawn");
  await close("other_run", `${org}_previous`, `${org}_a`, "wrong-run");
  response = await call("GET", `/v1/tasks/${task}`);
  body = await response.json() as { questions: unknown[] };
  expect(body.questions).toEqual([
    expect.objectContaining({ id: `${org}_a`, runId: `${org}_latest`, status: "cancelled", endedBy: "lost" }),
    expect.objectContaining({ id: `${org}_b`, runId: `${org}_latest`, status: "cancelled", endedBy: "run_failed" }),
  ]);
});

test("/recover passes the orchestrator's conductor through", async () => {
  const res = await call("GET", `/v1/tasks/${task}/recover`);
  expect(res.status).toBe(200);
  const body = recoveryOptionsSchema.parse(await res.json());
  expect(body.conductor).toEqual(conductor);
});
