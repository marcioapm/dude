/**
 * Agents coordinating on an epic, as the control plane serves it: an
 * epic's notes (current and history), its threads between tasks' agents
 * with their waits, and each task's peer state — read here, scoped to the
 * caller's organisation (another's epic is 404); pinning and retiring a
 * note forwarded to the orchestrator as the person who asked, after
 * checking the note; and no route for a person to write in a thread.
 *
 * Against a database of its own, migrated as a release is, and a stand-in
 * orchestrator. Requires DATABASE_URL: a role that can create databases.
 */

import { afterAll, beforeAll, expect, test } from "bun:test";
import { SQL } from "bun";
import { join } from "node:path";
import type { EpicNotes, EpicPeers, EpicThreads } from "@dude/domain";
import { closePool, setPool } from "../src/db/client.ts";
import { buildRouter } from "../src/index.ts";
import type { Router } from "../src/api/router.ts";
import { createApiKey } from "../src/api/auth.ts";
import { Config, useConfig } from "../src/config.ts";

const OWNER_URL = process.env.DATABASE_URL ?? "postgres://dude:dude@localhost:5433/dude";
const ROOT = join(import.meta.dir, "../../..");
const NAME = `dude_coordination_test_${Bun.randomUUIDv7("hex").slice(-12)}`;
const ORG = "org_coord";
const OTHER = "org_coord_other";

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
const forwarded: Array<{ path: string; body: unknown; org: string | null; person: string | null }> = [];

beforeAll(async () => {
  admin = new SQL(OWNER_URL);
  await admin.unsafe(`CREATE DATABASE "${NAME}"`);
  const migrate = Bun.spawnSync(["bun", "run", "apps/control-plane/src/db/migrate.ts"], {
    cwd: ROOT,
    env: { ...process.env, DATABASE_URL: databaseUrl("owner", NAME) },
  });
  if (migrate.exitCode !== 0) throw new Error(`migrate: ${migrate.stderr.toString()}`);
  owner = new SQL(databaseUrl("owner", NAME));
  for (const org of [ORG, OTHER]) await owner`INSERT INTO organizations (id, name, slug) VALUES (${org}, ${org}, ${org})`;
  await owner`INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES ('prj_c', ${ORG}, 'Abs', 'abs', 'ABS'),
    ('prj_o', ${OTHER}, 'Other', 'other', 'OTH')`;
  await owner`INSERT INTO epics (id, organization_id, project_id, title) VALUES ('epc_c', ${ORG}, 'prj_c', 'Metric preview'),
    ('epc_o', ${OTHER}, 'prj_o', 'Theirs')`;
  await owner`INSERT INTO tasks (id, organization_id, project_id, number, epic_id, title, status) VALUES
    ('wi_1', ${ORG}, 'prj_c', 1, 'epc_c', 'Engine endpoint', 'running'),
    ('wi_2', ${ORG}, 'prj_c', 2, 'epc_c', 'Preview endpoints', 'running'),
    ('wi_4', ${ORG}, 'prj_c', 4, 'epc_c', 'Preview panel', 'running'),
    ('wi_o', ${OTHER}, 'prj_o', 1, 'epc_o', 'Theirs', 'running')`;
  await owner`INSERT INTO runs (id, organization_id, project_id, task_id, attempt, status, phase, role, kind) VALUES
    ('run_1', ${ORG}, 'prj_c', 'wi_1', 1, 'completed', 'implement', 'implementer', 'agent'),
    ('run_2', ${ORG}, 'prj_c', 'wi_2', 1, 'running', 'implement', 'implementer', 'agent'),
    ('run_4', ${ORG}, 'prj_c', 'wi_4', 1, 'paused', 'implement', 'implementer', 'agent'),
    ('run_o', ${OTHER}, 'prj_o', 'wi_o', 1, 'running', 'implement', 'implementer', 'agent')`;
  // ABS-4 asks ABS-2 and waits; ABS-2 answers; ABS-4 asks ABS-1, which has no Run going.
  await owner`INSERT INTO directives (id, organization_id, task_id, run_id, text) VALUES
    ('dir_a', ${ORG}, 'wi_2', 'run_2', 'q'), ('dir_b', ${ORG}, 'wi_4', 'run_4', 'a')`;
  await owner`INSERT INTO peer_messages (id, organization_id, from_task_id, from_run_id, to_task_id, reply_to, text, created_at,
      directive_id, delivered_run_id, read_at) VALUES
    ('pm_q', ${ORG}, 'wi_4', 'run_4', 'wi_2', NULL, 'Which body?', now() - interval '10 minutes', 'dir_a', 'run_2', now() - interval '9 minutes'),
    ('pm_a', ${ORG}, 'wi_2', 'run_2', 'wi_4', 'pm_q', '{metric, from}', now() - interval '5 minutes', 'dir_b', 'run_4', NULL),
    ('pm_h', ${ORG}, 'wi_4', 'run_4', 'wi_1', NULL, 'Which path?', now() - interval '2 minutes', NULL, NULL, NULL)`;
  await owner`INSERT INTO peer_waits (id, organization_id, run_id, message_id, how, since, closed_at, closed_by) VALUES
    ('pw_1', ${ORG}, 'run_4', 'pm_q', 'wait', now() - interval '8 minutes', now() - interval '5 minutes', 'reply'),
    ('pw_2', ${ORG}, 'run_4', 'pm_h', 'turn_ended', now() - interval '1 minute', NULL, NULL)`;
  await owner`INSERT INTO people (id, organization_id, name) VALUES ('per_m', ${ORG}, 'Márcio Martins')`;
  await owner`INSERT INTO epic_notes (id, organization_id, epic_id, n, text, by_person, by_label, replaced_at) VALUES
    ('epn_1', ${ORG}, 'epc_c', 1, 'ABS-1 serves the engine.', 'per_m', 'Márcio Martins', now())`;
  await owner`INSERT INTO epic_notes (id, organization_id, epic_id, n, text, by_run_id, by_label, replaces_id) VALUES
    ('epn_2', ${ORG}, 'epc_c', 2, 'ABS-1 serves POST /v1/query.', 'run_2', 'ABS-2''s implementer', 'epn_1')`;
  await owner`INSERT INTO epic_notes (id, organization_id, epic_id, n, text, by_person, by_label, retired_by, retired_at) VALUES
    ('epn_3', ${ORG}, 'epc_c', 3, 'Read the docs.', 'per_m', 'Márcio Martins', 'per_m', now())`;
  await owner`INSERT INTO epic_note_reads (note_id, run_id, change, organization_id, read_at) VALUES
    ('epn_2', 'run_2', 'briefing', ${ORG}, now()), ('epn_2', 'run_1', 'briefing', ${ORG}, now())`;
  await owner`INSERT INTO repositories (id, organization_id, project_id, name, url) VALUES ('repo_c', ${ORG}, 'prj_c', 'web', 'https://github.com/test/web')`;
  await owner`INSERT INTO task_repositories (task_id, repository_id, organization_id) VALUES ('wi_2', 'repo_c', ${ORG})`;
  await owner`UPDATE runs SET branch = 'dude/wi_2/attempt-1', heads = '{"web":{"sha":"abcdef123"}}'::jsonb WHERE id = 'run_2'`;
  await owner`INSERT INTO runs (id, organization_id, project_id, task_id, attempt, status, phase, role, kind, created_at) VALUES
    ('run_2_old', ${ORG}, 'prj_c', 'wi_2', 1, 'completed', 'implement', 'implementer', 'agent', now() - interval '1 day')`;
  await owner`INSERT INTO run_diffs (run_id, organization_id, base, files, checksum) VALUES ('run_2_old', ${ORG}, 'main',
    '[{"path":"old.ts","additions":999,"deletions":0}]'::jsonb, 'old')`;
  await owner`INSERT INTO run_diffs (run_id, organization_id, base, files, checksum) VALUES ('run_2', ${ORG}, 'main',
    '[{"path":"a.ts","additions":1,"deletions":0},{"path":"z.ts","additions":20,"deletions":5}]'::jsonb, 'test')`;
  await owner`INSERT INTO events (id, organization_id, run_id, event_type, actor_type, actor_id, source, correlation_id, payload)
    VALUES ('evt_said', ${ORG}, 'run_2', 'agent.message', 'agent', 'run_2', 'lux', 'wi_2', jsonb_build_object('text', ${"x".repeat(700) + "\n\nNot this paragraph."}::text))`;
  app = new SQL(databaseUrl("app", NAME));
  setPool(app);
  key = await createApiKey({ organizationId: ORG, name: "Ana" });
  orchestrator = Bun.serve({ port: 0, async fetch(request) {
    const path = new URL(request.url).pathname;
    forwarded.push({ path, body: await request.json().catch(() => null), org: request.headers.get("x-dude-organization"),
      person: request.headers.get("x-dude-person") });
    return Response.json({ id: "epn_new", n: 4, told: 2, current: [] }, { status: 201 });
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

async function call(method: string, path: string, body?: unknown, auth = true): Promise<Response> {
  return router.handle(new Request(`http://dude.test${path}`, {
    method, headers: { ...(auth ? { authorization: `Bearer ${key.key}` } : {}), "content-type": "application/json" },
    ...(body === undefined ? {} : { body: JSON.stringify(body) }),
  }));
}

test("an epic's notes: current, with who pinned each and how many live agents read it, then the history", async () => {
  const res = await call("GET", "/v1/epics/epc_c/notes");
  expect(res.status).toBe(200);
  const notes = (await res.json()) as EpicNotes;
  expect(notes.limit).toBe(10);
  expect(notes.live).toBe(2);
  expect(notes.current.map((n) => n.n)).toEqual([2]);
  const [two] = notes.current;
  expect(two!.by).toEqual({ kind: "agent", label: "ABS-2's implementer", runId: "run_2",
    task: { taskId: "wi_2", key: "ABS-2", title: "Preview endpoints" }, personId: null, fromSessionId: null });
  expect(two!.replaces).toBe(1);
  // run_1 has ended: only live agents count.
  expect(two!.readBy).toBe(1);
  expect(notes.history.map((n) => n.n)).toEqual([3, 1]);
  const [three, one] = notes.history;
  expect(three!.retired?.by).toEqual({ personId: "per_m", name: "Márcio Martins" });
  expect(one!.replacedBy?.n).toBe(2);
  expect(one!.by.kind).toBe("person");
});

test("threads: a reply chain answered and its closed wait, and a question to a task with no Run, waiting", async () => {
  const res = await call("GET", "/v1/epics/epc_c/threads");
  expect(res.status).toBe(200);
  const body = (await res.json()) as EpicThreads;
  expect(body.waiting).toBe(1);
  expect(body.threads.map((t) => t.id)).toEqual(["pm_h", "pm_q"]);
  const [held, answered] = body.threads;
  expect(held!.state).toBe("waiting");
  expect(held!.pair.map((p) => p.key)).toEqual(["ABS-4", "ABS-1"]);
  expect(held!.wait).toMatchObject({ runId: "run_4", messageId: "pm_h", how: "turn_ended", closedAt: null, on: { key: "ABS-1" } });
  expect(held!.messages[0]).toMatchObject({ lands: "next_run", readAt: null, fromRole: "implementer", fromPhase: "implement" });
  expect(answered!.state).toBe("answered");
  expect(answered!.messages.map((m) => m.id)).toEqual(["pm_q", "pm_a"]);
  expect(answered!.messages[0]).toMatchObject({ from: { key: "ABS-4" }, to: { key: "ABS-2" }, lands: "next_step", deliveredRunId: "run_2" });
  expect(answered!.messages[0]!.readAt).not.toBeNull();
  expect(answered!.wait).toMatchObject({ closedBy: "reply", how: "wait" });
});

test("the waiting count is small and agrees with live open waits", async () => {
  await owner`INSERT INTO peer_waits (id, organization_id, run_id, message_id, how) VALUES ('pw_stale', ${ORG}, 'run_1', 'pm_h', 'wait')`;
  const res = await call("GET", "/v1/epics/epc_c/waiting");
  await owner`DELETE FROM peer_waits WHERE id = 'pw_stale'`;
  expect(res.status).toBe(200);
  expect(await res.json()).toEqual({ epicId: "epc_c", waiting: 1 });
  expect((await call("GET", "/v1/epics/epc_o/waiting")).status).toBe(404);
  expect((await call("GET", "/v1/epics/epc_c/waiting", undefined, false)).status).toBe(401);
});

test("peer state: each task's phase, live or parked or no Run, whom it waits on and its unread messages", async () => {
  const res = await call("GET", "/v1/epics/epc_c/peers");
  expect(res.status).toBe(200);
  const { tasks } = (await res.json()) as EpicPeers;
  const by = Object.fromEntries(tasks.map((t) => [t.key, t]));
  expect(Object.keys(by)).toEqual(["ABS-1", "ABS-2", "ABS-4"]);
  expect(by["ABS-1"]).toMatchObject({ run: "none", runId: null, phase: "implement", inbox: 1, waitingOn: null });
  expect(by["ABS-2"]).toMatchObject({ run: "live", runId: "run_2", role: "implementer", inbox: 0, waitingOn: null });
  expect(by["ABS-2"]).toMatchObject({ branch: "dude/wi_2/attempt-1", head: "abcdef123", lastSaid: "x".repeat(599) + "…",
    files: [{ path: "z.ts", additions: 20, deletions: 5 }, { path: "a.ts", additions: 1, deletions: 0 }] });
  expect(by["ABS-4"]).toMatchObject({ run: "parked", runId: "run_4", inbox: 1,
    waitingOn: { task: { key: "ABS-1" }, messageId: "pm_h", how: "turn_ended" } });
});

test("peer heads prefer the PR over the phase and conductor heads", async () => {
  await owner`INSERT INTO conductor_publishes (id, organization_id, task_id, run_id, request_id, status, heads, settled_at)
    VALUES ('cp_head', ${ORG}, 'wi_2', 'run_2', 'head', 'published', '{"web":{"sha":"conductor"}}'::jsonb, now())`;
  await owner`INSERT INTO pull_requests (id, organization_id, project_id, task_id, repository_id, number, url, head_branch, base_branch, head_sha, title)
    VALUES ('pr_head', ${ORG}, 'prj_c', 'wi_2', 'repo_c', 1, 'https://github.com/test/web/pull/1', 'task', 'main', 'prhead123', 'head')`;
  try {
    const body = await (await call("GET", "/v1/epics/epc_c/peers")).json() as EpicPeers;
    expect(body.tasks.find(t => t.taskId === 'wi_2')!.head).toBe('prhead123');
  } finally {
    await owner`DELETE FROM pull_requests WHERE id = 'pr_head'`;
    await owner`DELETE FROM conductor_publishes WHERE id = 'cp_head'`;
  }
});

test("a peer's branch is its newest Run with heads, while a reviewer with no branch is its latest Run", async () => {
  await owner`INSERT INTO runs (id, organization_id, project_id, task_id, attempt, status, phase, role, kind, created_at) VALUES
    ('run_2_rev', ${ORG}, 'prj_c', 'wi_2', 1, 'running', 'review', 'reviewer', 'agent', now() + interval '1 minute')`;
  try {
    const body = await (await call("GET", "/v1/epics/epc_c/peers")).json() as EpicPeers;
    expect(body.tasks.find((t) => t.taskId === "wi_2")).toMatchObject({ runId: "run_2_rev", role: "reviewer",
      branch: "dude/wi_2/attempt-1", head: "abcdef123" });
  } finally {
    await owner`DELETE FROM runs WHERE id = 'run_2_rev'`;
  }
});

test("another organisation's epic is not found, and nothing reads without a key", async () => {
  for (const path of ["/v1/epics/epc_o/notes", "/v1/epics/epc_o/threads", "/v1/epics/epc_o/peers", "/v1/epics/nope/notes"]) {
    expect((await call("GET", path)).status).toBe(404);
  }
  expect((await call("GET", "/v1/epics/epc_c/notes", undefined, false)).status).toBe(401);
  expect((await call("POST", "/v1/epics/epc_c/notes", { text: "x" }, false)).status).toBe(401);
});

test("pin and retire go to the orchestrator as the person who asked; a bad note is refused here", async () => {
  forwarded.length = 0;
  let res = await call("POST", "/v1/epics/epc_c/notes", { text: "  Body is {metric}.  ", replaces: 2 });
  expect(res.status).toBe(201);
  res = await call("POST", "/v1/epics/epc_c/notes/2/retire");
  expect(res.status).toBe(201);
  expect(forwarded).toEqual([
    { path: "/internal/epics/epc_c/notes", body: { text: "Body is {metric}.", replaces: 2 }, org: ORG, person: key.personId },
    { path: "/internal/epics/epc_c/notes/2/retire", body: {}, org: ORG, person: key.personId },
  ]);
  for (const bad of [{ text: "" }, { text: "x".repeat(2001) }, { text: "x", replaces: 0 }, { text: "x", retire: true }]) {
    expect((await call("POST", "/v1/epics/epc_c/notes", bad)).status).toBe(400);
  }
  expect((await call("POST", "/v1/epics/epc_c/notes/zero/retire")).status).toBe(404);
  expect(forwarded.length).toBe(2);
});

test("in the tree, a Run waiting on a peer says on whom, and parked on it it does not need a person", async () => {
  const res = await call("GET", "/v1/navigation");
  expect(res.status).toBe(200);
  const { projects } = (await res.json()) as { projects: Array<{ id: string; epics: Array<{ tasks: Array<{ key: string;
    runs: Array<{ sessions: Array<{ id: string; role: string; title: string; status: string; activity?: string }> }> }> }> }> };
  const sessions = Object.fromEntries(projects.find((p) => p.id === "prj_c")!.epics[0]!.tasks
    .flatMap((t) => t.runs.flatMap((r) => r.sessions)).map((s) => [s.id, s]));
  expect(sessions["run_4"]).toEqual({ id: "run_4", role: "implementer", status: "pending", title: expect.any(String), activity: "Waiting on ABS-1" });
  // Its earlier wait closed with the reply: ABS-2 waits on nobody.
  expect(sessions["run_2"]!.activity).toBeUndefined();
  expect(sessions["run_2"]!.status).toBe("running");
});

test("the project page marks an epic whose agents wait on each other, from live Runs' open waits", async () => {
  const res = await call("GET", "/v1/projects/prj_c/overview");
  expect(res.status).toBe(200);
  const { epics } = (await res.json()) as { epics: Array<{ id: string; waiting: Array<[string, string]>; needsYou: number }> };
  const epic = epics.find((e) => e.id === "epc_c")!;
  expect(epic.waiting).toEqual([["ABS-4", "ABS-1"]]);
  expect(epic.needsYou).toBe(0);
  // A wait whose Run ended is no one waiting.
  await owner`UPDATE runs SET status = 'aborted' WHERE id = 'run_4'`;
  try {
    const after = (await (await call("GET", "/v1/projects/prj_c/overview")).json()) as { epics: Array<{ id: string; waiting: unknown[] }> };
    expect(after.epics.find((e) => e.id === "epc_c")!.waiting).toEqual([]);
  } finally {
    await owner`UPDATE runs SET status = 'paused' WHERE id = 'run_4'`;
  }
});

test("there is no route for a person to write in a thread", async () => {
  for (const [method, path] of [["POST", "/v1/epics/epc_c/threads"], ["POST", "/v1/epics/epc_c/threads/pm_q/reply"],
    ["POST", "/v1/epics/epc_c/messages"]] as const) {
    expect((await call(method, path, { text: "hi" })).status).toBe(404);
  }
});
