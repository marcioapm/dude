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
