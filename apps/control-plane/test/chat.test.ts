/**
 * A task's Chat through the public API: POST /v1/tasks/:id/chat forwards a
 * person's message to the orchestrator as who wrote it, and refuses an
 * empty one itself; and in the sidebar a task's conductor is listed first
 * among its sessions, parked quietly — never "needs you" — unless it asks.
 *
 * Against a database of its own, migrated as a release is, and a stand-in
 * orchestrator. Requires DATABASE_URL: a role that can create databases.
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
const NAME = `dude_chat_test_${Bun.randomUUIDv7("hex").slice(-12)}`;
const ORG = "org_chat";
const PROJECT = "prj_chat";

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
let key: { key: string; id: string; personId: string };
let orchestrator: ReturnType<typeof Bun.serve>;
const forwarded: Array<{ path: string; body: unknown; actor: string | null; person: string | null }> = [];

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
  key = await createApiKey({ organizationId: ORG, name: "Ana" });
  orchestrator = Bun.serve({ port: 0, async fetch(request) {
    const path = new URL(request.url).pathname;
    forwarded.push({ path, body: await request.json().catch(() => null), actor: request.headers.get("x-dude-actor"),
      person: request.headers.get("x-dude-person") });
    return Response.json({ runId: "run_c", taskId: "wi_1", created: true }, { status: 201 });
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

async function call(method: string, path: string, body?: unknown): Promise<Response> {
  return router.handle(new Request(`http://dude.test${path}`, {
    method, headers: { authorization: `Bearer ${key.key}`, "content-type": "application/json" },
    ...(body === undefined ? {} : { body: JSON.stringify(body) }),
  }));
}

test("a message in Chat goes to the orchestrator as who wrote it, and its answer comes back", async () => {
  const res = await call("POST", "/v1/tasks/wi_1/chat", { text: "  why 8s?  " });
  expect(res.status).toBe(201);
  expect(await res.json()).toEqual({ runId: "run_c", taskId: "wi_1", created: true });
  expect(forwarded).toEqual([{ path: "/internal/tasks/wi_1/chat", body: { text: "why 8s?" }, actor: key.id, person: key.personId }]);
});

test("an empty message, or anything else, is refused before the orchestrator hears of it", async () => {
  forwarded.length = 0;
  for (const body of [{ text: "   " }, {}, { text: "x", runId: "run_other" }, { text: "x".repeat(16_385) }]) {
    expect((await call("POST", "/v1/tasks/wi_1/chat", body)).status).toBe(400);
  }
  expect(forwarded).toEqual([]);
});

test("Talk it through and Let Deliver finish it go to the orchestrator as who asked", async () => {
  forwarded.length = 0;
  expect((await call("POST", "/v1/tasks/wi_1/talk")).status).toBe(201);
  expect((await call("POST", "/v1/tasks/wi_1/decider", { decider: "policy" })).status).toBe(201);
  expect(forwarded).toEqual([
    { path: "/internal/tasks/wi_1/talk", body: {}, actor: key.id, person: key.personId },
    { path: "/internal/tasks/wi_1/decider", body: { decider: "policy" }, actor: key.id, person: key.personId },
  ]);
  forwarded.length = 0;
  for (const body of [{}, { decider: "someone" }, { decider: "policy", extra: 1 }]) {
    expect((await call("POST", "/v1/tasks/wi_1/decider", body)).status).toBe(400);
  }
  expect(forwarded).toEqual([]);
});

test("a task says who decides, and the decision its conductor is asked for", async () => {
  await owner`INSERT INTO tasks (id, organization_id, project_id, number, title, status)
    VALUES ('wi_none', ${ORG}, ${PROJECT}, 3, 'Not started', 'received'),
           ('wi_dec', ${ORG}, ${PROJECT}, 4, 'Conducted', 'running')`;
  await owner`INSERT INTO runs (id, organization_id, project_id, task_id, attempt, status, phase, role, conductor_run_id)
    VALUES ('run_dc', ${ORG}, ${PROJECT}, 'wi_dec', 1, 'running', NULL, 'conductor', NULL),
           ('run_di', ${ORG}, ${PROJECT}, 'wi_dec', 1, 'completed', 'implement', 'implementer', 'run_dc')`;
  await owner`INSERT INTO workflow_runs (id, organization_id, workflow_type, idempotency_key, status, step, state, task_id)
    VALUES ('wfr_dec', ${ORG}, 'task.delivery', 'delivery:wi_dec', 'waiting', 'conductorDecision',
      '{"decider":"conductor","decision":{"point":"after_implement","policy":"review"}}', 'wi_dec')`;

  const none = (await (await call("GET", "/v1/tasks/wi_none")).json()) as Record<string, unknown>;
  expect([none.decider, none.awaitingDecision]).toEqual(["policy", null]);
  const conducted = (await (await call("GET", "/v1/tasks/wi_dec")).json()) as {
    decider: string; awaitingDecision: unknown; runs: Array<{ id: string; conductorRunId: string | null }>;
  };
  expect(conducted.decider).toBe("conductor");
  expect(conducted.awaitingDecision).toEqual({ point: "after_implement" });
  expect(conducted.runs.find((r) => r.id === "run_di")?.conductorRunId).toBe("run_dc");

  // Taken: carried out by the workflow, waited on no more.
  await owner`UPDATE workflow_runs SET state = jsonb_set(state, '{decision,taken}', '{"action":"next"}') WHERE id = 'wfr_dec'`;
  const taken = (await (await call("GET", "/v1/tasks/wi_dec")).json()) as Record<string, unknown>;
  expect(taken.awaitingDecision).toBeNull();
});

type NavSession = { id: string; role: string; status: string; title: string; activity?: string };
type NavTask = { id: string; runs: Array<{ status: string; sessions: NavSession[] }> };

async function navTask(id: string): Promise<NavTask> {
  const res = await call("GET", "/v1/navigation");
  expect(res.status).toBe(200);
  const { projects } = (await res.json()) as { projects: Array<{ epics: Array<{ tasks: NavTask[] }>; tasks: NavTask[] }> };
  const tasks = projects.flatMap((p) => [...p.tasks, ...p.epics.flatMap((e) => e.tasks)]);
  return tasks.find((t) => t.id === id)!;
}

test("the conductor is first in its task's sessions, quiet when parked, needs you only when it asks", async () => {
  await owner`INSERT INTO tasks (id, organization_id, project_id, number, title, status) VALUES ('wi_nav', ${ORG}, ${PROJECT}, 2, 'Done', 'done')`;
  await owner`INSERT INTO runs (id, organization_id, project_id, task_id, attempt, status, phase, role, created_at)
    VALUES ('run_impl', ${ORG}, ${PROJECT}, 'wi_nav', 1, 'completed', 'implement', 'implementer', now() - interval '1 day'),
           ('run_rev', ${ORG}, ${PROJECT}, 'wi_nav', 1, 'completed', 'review', 'reviewer', now() - interval '23 hours')`;
  await owner`INSERT INTO runs (id, organization_id, project_id, task_id, attempt, status, role, dude_pause)
    VALUES ('run_cond', ${ORG}, ${PROJECT}, 'wi_nav', 1, 'paused', 'conductor', 'conductor')`;

  let task = await navTask("wi_nav");
  expect(task.runs).toHaveLength(1);
  expect(task.runs[0]!.sessions.map((s) => [s.id, s.role, s.status, s.title])).toEqual([
    ["run_cond", "conductor", "pending", "Conductor"],
    ["run_impl", "implementer", "completed", "Implement"],
    ["run_rev", "reviewer", "completed", "Review"],
  ]);
  // The attempt is the delivery's, whatever the conductor is doing.
  expect(task.runs[0]!.status).toBe("completed");

  await owner`INSERT INTO questions (id, organization_id, task_id, run_id, prompt)
    VALUES ('q_c', ${ORG}, 'wi_nav', 'run_cond', 'Make it a follow-up task?')`;
  await owner`UPDATE runs SET status = 'running', dude_pause = NULL WHERE id = 'run_cond'`;
  task = await navTask("wi_nav");
  expect(task.runs[0]!.sessions[0]).toMatchObject({ id: "run_cond", status: "awaiting_input", activity: "Make it a follow-up task?" });
});
