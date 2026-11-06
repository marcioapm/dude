/**
 * Several questions in one ask: what the backend reads back of them —
 * /v1/questions exposes what was asked and answered, and the board's line
 * for an agent asking several names how many and their headers, while one
 * question still reads as its text.
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
const NAME = `dude_questions_test_${Bun.randomUUIDv7("hex").slice(-12)}`;
const ORG = "org_q";
const PROJECT = "prj_q";

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

async function get<T>(path: string): Promise<T> {
  const res = await router.handle(new Request(`http://dude.test${path}`, { headers: { authorization: `Bearer ${key}` } }));
  expect(res.status).toBe(200);
  return (await res.json()) as T;
}

const ITEMS = [
  { header: "Retry scope", question: "Which failures should the payment call retry?", multiple: false,
    choices: [{ label: "5xx only", description: "A 4xx is our bug.", recommended: true }, { label: "Everything", description: "", recommended: false }] },
  { header: "Old route", question: "What happens to /pay?", multiple: false, choices: [] },
  { header: "Tests", question: "Which layers?", multiple: true,
    choices: [{ label: "Unit", description: "", recommended: false }, { label: "API", description: "", recommended: false }] },
];

type Session = { id: string; activity?: string };
type Nav = { projects: Array<{ tasks: Array<{ id: string; runs: Array<{ sessions: Session[] }> }> }> };

function sessionsOf(nav: Nav, task: string): Session[] {
  const t = nav.projects.flatMap((p) => p.tasks).find((x) => x.id === task);
  return t?.runs.flatMap((r) => r.sessions) ?? [];
}

test("an agent asking several questions reads as how many and their headers; one, as its text", async () => {
  await owner`INSERT INTO tasks (id, organization_id, project_id, number, title, status)
              VALUES ('wi_many', ${ORG}, ${PROJECT}, 1, 'Split', 'awaiting_input'), ('wi_one', ${ORG}, ${PROJECT}, 2, 'One', 'awaiting_input')`;
  await owner`INSERT INTO runs (id, organization_id, project_id, task_id, attempt, status, phase, role)
              VALUES ('run_many', ${ORG}, ${PROJECT}, 'wi_many', 1, 'running', 'implement', 'implementer'),
                     ('run_one', ${ORG}, ${PROJECT}, 'wi_one', 1, 'running', 'implement', 'implementer')`;
  await owner`INSERT INTO questions (id, organization_id, task_id, run_id, prompt, options, items)
              VALUES ('q_many', ${ORG}, 'wi_many', 'run_many', '3 questions: Retry scope, Old route, Tests', '[]', (${JSON.stringify(ITEMS)}::text)::jsonb)`;
  await owner`INSERT INTO questions (id, organization_id, task_id, run_id, prompt, options)
              VALUES ('q_one', ${ORG}, 'wi_one', 'run_one', 'Proceed?', '["Yes","No"]')`;

  const nav = await get<Nav>("/v1/navigation");
  expect(sessionsOf(nav, "wi_many").map((s) => s.activity)).toEqual(["asks 3 questions · Retry scope, Old route, Tests"]);
  expect(sessionsOf(nav, "wi_one").map((s) => s.activity)).toEqual(["Proceed?"]);

  const { questions } = await get<{ questions: Array<{ id: string; items: typeof ITEMS; answers: unknown }> }>("/v1/questions?taskId=wi_many");
  expect(questions).toHaveLength(1);
  expect(questions[0]!.items).toEqual(ITEMS);
  expect(questions[0]!.answers).toBeNull();

  await owner`UPDATE questions SET status = 'answered', answer = 'x',
              answers = (${JSON.stringify([{ choices: [0], text: "" }, { choices: [], text: "Leave it" }, { choices: [0, 1], text: "" }])}::text)::jsonb
              WHERE id = 'q_many'`;
  const after = await get<{ questions: Array<{ answers: Array<{ choices: number[]; text: string }> }> }>("/v1/questions?taskId=wi_many");
  expect(after.questions[0]!.answers[1]).toEqual({ choices: [], text: "Leave it" });
  // A question asked before items existed has its one item.
  const one = await get<{ questions: Array<{ items: typeof ITEMS }> }>("/v1/questions?taskId=wi_one");
  expect(one.questions[0]!.items).toEqual([{ header: "", question: "Proceed?", multiple: false,
    choices: [{ label: "Yes", description: "", recommended: false }, { label: "No", description: "", recommended: false }] }]);
});

/**
 * A writer's question for its conductor (questions.addressee 'conductor',
 * ask_conductor) asks nothing of a person: not on the board's sessions, not
 * in Needs you, not in the open list of what needs a person — whether the
 * writer still runs or dude parked it on that question. A person's
 * question (the fallback with no conductor, or the conductor's ask_person
 * relay) still does.
 */
test("a writer's question for its conductor is not a person's; a person's still is", async () => {
  await owner`INSERT INTO tasks (id, organization_id, project_id, number, title, status)
              VALUES ('wi_cq_run', ${ORG}, ${PROJECT}, 10, 'Routed, running', 'running'),
                     ('wi_cq_park', ${ORG}, ${PROJECT}, 11, 'Routed, parked', 'running'),
                     ('wi_pq', ${ORG}, ${PROJECT}, 12, 'Relayed to a person', 'running'),
                     ('wi_pq_park', ${ORG}, ${PROJECT}, 13, 'Person, parked', 'running')`;
  await owner`INSERT INTO runs (id, organization_id, project_id, task_id, attempt, status, phase, role)
              VALUES ('run_cq_cond', ${ORG}, ${PROJECT}, 'wi_cq_run', 1, 'running', NULL, 'conductor'),
                     ('run_cq_run', ${ORG}, ${PROJECT}, 'wi_cq_run', 1, 'running', 'implement', 'implementer'),
                     ('run_cq_park_cond', ${ORG}, ${PROJECT}, 'wi_cq_park', 1, 'running', NULL, 'conductor'),
                     ('run_pq_cond', ${ORG}, ${PROJECT}, 'wi_pq', 1, 'running', NULL, 'conductor'),
                     ('run_pq', ${ORG}, ${PROJECT}, 'wi_pq', 1, 'running', 'implement', 'implementer')`;
  await owner`INSERT INTO runs (id, organization_id, project_id, task_id, attempt, status, phase, role, dude_pause)
              VALUES ('run_cq_park', ${ORG}, ${PROJECT}, 'wi_cq_park', 1, 'paused', 'implement', 'implementer', 'person'),
                     ('run_pq_park', ${ORG}, ${PROJECT}, 'wi_pq_park', 1, 'paused', 'implement', 'implementer', 'person')`;
  await owner`INSERT INTO questions (id, organization_id, task_id, run_id, prompt, options, addressee)
              VALUES ('q_cq_run', ${ORG}, 'wi_cq_run', 'run_cq_run', 'Routed?', '[]', 'conductor'),
                     ('q_cq_park', ${ORG}, 'wi_cq_park', 'run_cq_park', 'Routed and parked?', '[]', 'conductor'),
                     ('q_pq', ${ORG}, 'wi_pq', 'run_pq', 'For a person?', '[]', 'person'),
                     ('q_pq_park', ${ORG}, 'wi_pq_park', 'run_pq_park', 'Parked for a person?', '[]', 'person')`;

  const nav = await get<Nav>("/v1/navigation");
  const rows = (task: string) => (sessionsOf(nav, task) as Array<Session & { role: string; status: string }>)
    .map((s) => [s.role, s.status, s.activity ?? null]);
  expect(rows("wi_cq_run")).toEqual([["conductor", "running", null], ["implementer", "running", null]]);
  // Parked on its conductor: the conductor owes it, as a peer wait — not awaiting_input.
  expect(rows("wi_cq_park")).toEqual([["conductor", "running", null], ["implementer", "pending", null]]);
  expect(rows("wi_pq")).toEqual([["conductor", "running", null], ["implementer", "awaiting_input", "For a person?"]]);
  expect(rows("wi_pq_park")).toEqual([["implementer", "awaiting_input", "Parked for a person?"]]);
  // Needs you, by the board's own rule.
  const { countTasks } = await import("../../../packages/design-system/src/util/navModel.ts");
  const tasks = nav.projects.flatMap((p) => p.tasks);
  const needs = (id: string) => countTasks(tasks.filter((t) => t.id === id) as never).needs_you;
  expect([needs("wi_cq_run"), needs("wi_cq_park"), needs("wi_pq"), needs("wi_pq_park")]).toEqual([0, 0, 1, 1]);

  // A person's own pause (dude_pause NULL) stays theirs, even on an open
  // question for its conductor: paused as before, not the conductor's wait.
  await owner`INSERT INTO tasks (id, organization_id, project_id, number, title, status)
              VALUES ('wi_cq_mine', ${ORG}, ${PROJECT}, 14, 'Routed, paused by a person', 'running')`;
  await owner`INSERT INTO runs (id, organization_id, project_id, task_id, attempt, status, phase, role, dude_pause)
              VALUES ('run_cq_mine_cond', ${ORG}, ${PROJECT}, 'wi_cq_mine', 1, 'running', NULL, 'conductor', NULL),
                     ('run_cq_mine', ${ORG}, ${PROJECT}, 'wi_cq_mine', 1, 'paused', 'implement', 'implementer', NULL)`;
  await owner`INSERT INTO questions (id, organization_id, task_id, run_id, prompt, options, addressee)
              VALUES ('q_cq_mine', ${ORG}, 'wi_cq_mine', 'run_cq_mine', 'Routed, then paused?', '[]', 'conductor')`;
  // Of a person's open questions, the newest reads.
  await owner`INSERT INTO questions (id, organization_id, task_id, run_id, prompt, options, addressee, asked_at)
              VALUES ('q_pq_older', ${ORG}, 'wi_pq', 'run_pq', 'Asked before?', '[]', 'person', now() - interval '1 hour')`;
  const again = await get<Nav>("/v1/navigation");
  expect(sessionsOf(again, "wi_pq").map((s) => s.activity ?? null)).toEqual([null, "For a person?"]);
  const mine = sessionsOf(again, "wi_cq_mine") as Array<Session & { role: string; status: string }>;
  expect(mine.map((s) => [s.role, s.status, s.activity ?? null]))
    .toEqual([["conductor", "running", null], ["implementer", "awaiting_input", null]]);
  // Parked by dude with no open question at all: no conductor owes it,
  // paused as before.
  await owner`INSERT INTO tasks (id, organization_id, project_id, number, title, status)
              VALUES ('wi_park_none', ${ORG}, ${PROJECT}, 15, 'Parked, nothing asked', 'running')`;
  await owner`INSERT INTO runs (id, organization_id, project_id, task_id, attempt, status, phase, role, dude_pause)
              VALUES ('run_park_none', ${ORG}, ${PROJECT}, 'wi_park_none', 1, 'paused', 'implement', 'implementer', 'person')`;
  const none = sessionsOf(await get<Nav>("/v1/navigation"), "wi_park_none") as Array<Session & { role: string; status: string }>;
  expect(none.map((s) => [s.role, s.status, s.activity ?? null])).toEqual([["implementer", "awaiting_input", null]]);

  // The open list: what needs a person.
  const questionIds = async (query = "") =>
    (await get<{ questions: Array<{ id: string }> }>(`/v1/questions${query}`)).questions.map((q) => q.id);
  const ids = await questionIds();
  expect(ids).toContain("q_pq");
  expect(ids).toContain("q_pq_park");
  expect(ids).not.toContain("q_cq_run");
  expect(ids).not.toContain("q_cq_park");
  // A Run's or task's own list still has every question it asked.
  expect(await questionIds("?runId=run_cq_run")).toEqual(["q_cq_run"]);
  expect(await questionIds("?taskId=wi_cq_park")).toEqual(["q_cq_park"]);
});

/**
 * dude hands a writer's question its conductor did not take to the person
 * (the same row, addressee flipped to 'person'): the parked writer reads as
 * waiting on you with its prompt, no longer as its conductor's wait, and
 * the question is in Needs you and the open list. While the conductor's
 * relay of a writer's question is open, the person has exactly one
 * question: the relay; the writer still reads as its conductor's wait.
 */
test("a handed-over writer question needs you; a relayed one is asked once", async () => {
  await owner`INSERT INTO tasks (id, organization_id, project_id, number, title, status)
              VALUES ('wi_ho', ${ORG}, ${PROJECT}, 20, 'Handed over', 'awaiting_input'),
                     ('wi_rl', ${ORG}, ${PROJECT}, 21, 'Relayed', 'awaiting_input')`;
  await owner`INSERT INTO runs (id, organization_id, project_id, task_id, attempt, status, phase, role, dude_pause)
              VALUES ('run_ho_cond', ${ORG}, ${PROJECT}, 'wi_ho', 1, 'running', NULL, 'conductor', NULL),
                     ('run_ho', ${ORG}, ${PROJECT}, 'wi_ho', 1, 'paused', 'implement', 'implementer', 'person'),
                     ('run_rl_cond', ${ORG}, ${PROJECT}, 'wi_rl', 1, 'running', NULL, 'conductor', NULL),
                     ('run_rl', ${ORG}, ${PROJECT}, 'wi_rl', 1, 'paused', 'implement', 'implementer', 'person')`;
  await owner`INSERT INTO questions (id, organization_id, task_id, run_id, prompt, options, addressee)
              VALUES ('q_ho', ${ORG}, 'wi_ho', 'run_ho', 'Handed to you?', '[]', 'conductor'),
                     ('q_rl', ${ORG}, 'wi_rl', 'run_rl', 'Writer asks?', '[]', 'conductor')`;
  await owner`INSERT INTO questions (id, organization_id, task_id, run_id, prompt, options, addressee, relays)
              VALUES ('q_rl_relay', ${ORG}, 'wi_rl', 'run_rl_cond', 'Owner: A or B?', '[]', 'person', 'q_rl')`;
  await owner`UPDATE questions SET addressee = 'person' WHERE id = 'q_ho'`;


  const nav = await get<Nav>("/v1/navigation");
  const rows = (task: string) => (sessionsOf(nav, task) as Array<Session & { role: string; status: string }>)
    .map((s) => [s.role, s.status, s.activity ?? null]);
  expect(rows("wi_ho")).toEqual([["conductor", "running", null], ["implementer", "awaiting_input", "Handed to you?"]]);
  expect(rows("wi_rl")).toEqual([["conductor", "awaiting_input", "Owner: A or B?"], ["implementer", "pending", null]]);
  const { countTasks } = await import("../../../packages/design-system/src/util/navModel.ts");
  const tasks = nav.projects.flatMap((p) => p.tasks);
  const needs = (id: string) => countTasks(tasks.filter((t) => t.id === id) as never).needs_you;
  expect([needs("wi_ho"), needs("wi_rl")]).toEqual([1, 1]);
  const open = (await get<{ questions: Array<{ id: string }> }>("/v1/questions")).questions.map((q) => q.id);
  expect(open).toContain("q_ho");
  expect(open).toContain("q_rl_relay");
  expect(open).not.toContain("q_rl");
});

/**
 * Conductor-first (A10, A11): held reviewer/repository asks are not a person's.
 */
test("a repository request or reviewer question its conductor holds is not a person's", async () => {
  await owner`INSERT INTO repositories (id, organization_id, project_id, name, url, default_branch)
              VALUES ('repo_q_web', ${ORG}, ${PROJECT}, 'web', 'git://h/web.git', 'main')`;
  await owner`INSERT INTO tasks (id, organization_id, project_id, number, title, status)
              VALUES ('wi_rr_held', ${ORG}, ${PROJECT}, 30, 'Request held', 'running'),
                     ('wi_rr_park', ${ORG}, ${PROJECT}, 31, 'Request held, parked', 'running'),
                     ('wi_rr_fwd', ${ORG}, ${PROJECT}, 32, 'Request forwarded', 'running'),
                     ('wi_rvq_held', ${ORG}, ${PROJECT}, 33, 'Reviewer question held', 'running'),
                     ('wi_rvq_answered', ${ORG}, ${PROJECT}, 34, 'Reviewer question answered', 'running'),
                     ('wi_rvq_stopped', ${ORG}, ${PROJECT}, 35, 'Reviewer question, task stopped', 'aborted'),
                     ('wi_rvq_old', ${ORG}, ${PROJECT}, 36, 'Reviewer question of an earlier attempt', 'running')`;
  // A live delivery for each but the stopped one (aborted: kept for a pick-up).
  await owner`INSERT INTO workflow_runs (id, organization_id, task_id, workflow_type, idempotency_key, status, step, state)
              VALUES ('wf_rvq_held', ${ORG}, 'wi_rvq_held', 'task.delivery', 'rvq_held', 'waiting', 'review', '{"attempt":1}'),
                     ('wf_rvq_answered', ${ORG}, 'wi_rvq_answered', 'task.delivery', 'rvq_answered', 'waiting', 'review', '{"attempt":1}'),
                     ('wf_rvq_stopped', ${ORG}, 'wi_rvq_stopped', 'task.delivery', 'rvq_stopped', 'aborted', 'review', '{"attempt":1}'),
                     ('wf_rvq_old', ${ORG}, 'wi_rvq_old', 'task.delivery', 'rvq_old', 'waiting', 'review', '{"attempt":2}')`;
  await owner`INSERT INTO runs (id, organization_id, project_id, task_id, attempt, status, phase, role)
              VALUES ('run_rr_held', ${ORG}, ${PROJECT}, 'wi_rr_held', 1, 'running', 'implement', 'implementer'),
                     ('run_rr_fwd', ${ORG}, ${PROJECT}, 'wi_rr_fwd', 1, 'running', 'implement', 'implementer'),
                     ('run_rvq_rev', ${ORG}, ${PROJECT}, 'wi_rvq_held', 1, 'completed', 'review', 'reviewer'),
                     ('run_rvq_ans', ${ORG}, ${PROJECT}, 'wi_rvq_answered', 1, 'completed', 'review', 'reviewer'),
                     ('run_rvq_stop', ${ORG}, ${PROJECT}, 'wi_rvq_stopped', 1, 'completed', 'review', 'reviewer'),
                     ('run_rvq_old', ${ORG}, ${PROJECT}, 'wi_rvq_old', 1, 'completed', 'review', 'reviewer')`;
  await owner`INSERT INTO runs (id, organization_id, project_id, task_id, attempt, status, phase, role, dude_pause)
              VALUES ('run_rr_park', ${ORG}, ${PROJECT}, 'wi_rr_park', 1, 'paused', 'implement', 'implementer', 'person')`;
  await owner`INSERT INTO repository_requests (id, organization_id, task_id, run_id, repository_id, access, reason, blocking, addressee)
              VALUES ('rrq_held', ${ORG}, 'wi_rr_held', 'run_rr_held', 'repo_q_web', 'read', 'r', false, 'conductor'),
                     ('rrq_park', ${ORG}, 'wi_rr_park', 'run_rr_park', 'repo_q_web', 'write', 'r', true, 'conductor'),
                     ('rrq_fwd', ${ORG}, 'wi_rr_fwd', 'run_rr_fwd', 'repo_q_web', 'write', 'r', false, 'person')`;
  await owner`INSERT INTO review_questions (id, organization_id, task_id, run_id, n, category, question, addressee)
              VALUES ('rvq_held', ${ORG}, 'wi_rvq_held', 'run_rvq_rev', 1, 'correctness', 'UTC?', 'conductor'),
                     ('rvq_stop', ${ORG}, 'wi_rvq_stopped', 'run_rvq_stop', 1, 'correctness', 'UTC?', 'conductor'),
                     ('rvq_old', ${ORG}, 'wi_rvq_old', 'run_rvq_old', 1, 'correctness', 'UTC?', 'conductor')`;
  await owner`INSERT INTO review_questions (id, organization_id, task_id, run_id, n, category, question, addressee, status, answer, answered_at)
              VALUES ('rvq_ans', ${ORG}, 'wi_rvq_answered', 'run_rvq_ans', 1, 'correctness', 'UTC?', 'conductor', 'answered', 'yes', now())`;

  const nav = await get<Nav>("/v1/navigation");
  const rows = (task: string) => (sessionsOf(nav, task) as Array<Session & { role: string; status: string }>)
    .map((s) => [s.role, s.status, s.activity ?? null]);
  expect(rows("wi_rr_held")).toEqual([["implementer", "running", null]]);
  expect(rows("wi_rr_park")).toEqual([["implementer", "pending", null]]);
  expect(rows("wi_rr_fwd")).toEqual([["implementer", "awaiting_input", "Change web?"]]);
  const tasks = nav.projects.flatMap((p) => p.tasks) as Array<{ id: string; conductorHeld?: string }>;
  const held = (id: string) => tasks.find((t) => t.id === id)?.conductorHeld ?? null;
  expect([held("wi_rr_held"), held("wi_rr_park"), held("wi_rr_fwd"), held("wi_rvq_held")])
    .toEqual(["repository_request", "repository_request", null, "review_question"]);
  // Answered, of a stopped task, or of an earlier attempt: nobody's to hear now.
  expect([held("wi_rvq_answered"), held("wi_rvq_stopped"), held("wi_rvq_old")]).toEqual([null, null, null]);
  const { countTasks } = await import("../../../packages/design-system/src/util/navModel.ts");
  const needs = (id: string) => countTasks(tasks.filter((t) => t.id === id) as never).needs_you;
  expect([needs("wi_rr_held"), needs("wi_rr_park"), needs("wi_rr_fwd"), needs("wi_rvq_held")]).toEqual([0, 0, 1, 0]);

  const open = (await get<{ repositoryRequests: Array<{ id: string }> }>("/v1/repository-requests")).repositoryRequests.map((r) => r.id);
  expect(open).toContain("rrq_fwd");
  expect(open).not.toContain("rrq_held");
  expect(open).not.toContain("rrq_park");
  // A task's own list still has every request.
  expect((await get<{ repositoryRequests: Array<{ id: string }> }>("/v1/repository-requests?taskId=wi_rr_held")).repositoryRequests.map((r) => r.id))
    .toEqual(["rrq_held"]);
});
