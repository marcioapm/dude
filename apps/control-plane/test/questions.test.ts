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
