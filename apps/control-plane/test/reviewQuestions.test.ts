/**
 * A task's reviewer questions through the public API, against a database
 * of its own: read-only, the task's alone, and nothing of another
 * organization's task.
 *
 * Requires DATABASE_URL: a role that can create databases (the owner).
 */

import { afterAll, beforeAll, expect, test } from "bun:test";
import { SQL } from "bun";
import { join } from "node:path";
import { closePool, setPool, withOrg } from "../src/db/client.ts";
import { buildRouter } from "../src/index.ts";
import { Config, useConfig } from "../src/config.ts";
import type { Router } from "../src/api/router.ts";
import { createApiKey } from "../src/api/auth.ts";

const OWNER_URL = process.env.DATABASE_URL ?? "postgres://dude:dude@localhost:5433/dude";
const ROOT = join(import.meta.dir, "../../..");
const NAME = `dude_rq_test_${Bun.randomUUIDv7("hex").slice(-12)}`;
const ORG = "org_rq";
const OTHER = "org_rq_other";

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
let ana: { key: string };
let cy: { key: string };

function get(key: string, path: string): Promise<Response> {
  return router.handle(new Request(`http://dude.test${path}`, { headers: { authorization: `Bearer ${key}` } }));
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
  for (const id of [ORG, OTHER]) await owner`INSERT INTO organizations (id, name, slug) VALUES (${id}, ${id}, ${id})`;
  await owner`INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES
    ('prj_rq', ${ORG}, 'Billing', 'billing', 'BILL'), ('prj_rq_o', ${OTHER}, 'Other', 'other', 'OTH')`;
  await owner`INSERT INTO tasks (id, organization_id, project_id, number, title, status) VALUES
    ('wi_rq', ${ORG}, 'prj_rq', 1, 'Export', 'running'),
    ('wi_rq2', ${ORG}, 'prj_rq', 2, 'Another', 'running'),
    ('wi_o', ${OTHER}, 'prj_rq_o', 1, 'Theirs', 'running')`;
  await owner`INSERT INTO runs (id, organization_id, project_id, task_id, attempt, status, phase, role, category, kind) VALUES
    ('run_rq', ${ORG}, 'prj_rq', 'wi_rq', 1, 'completed', 'review', 'reviewer', 'correctness', 'agent'),
    ('run_rq2', ${ORG}, 'prj_rq', 'wi_rq2', 1, 'completed', 'review', 'reviewer', 'correctness', 'agent'),
    ('run_o', ${OTHER}, 'prj_rq_o', 'wi_o', 1, 'completed', 'review', 'reviewer', 'correctness', 'agent')`;
  await owner`INSERT INTO review_findings (id, organization_id, task_id, run_id, category, severity, title, description, suggested_fix)
    VALUES ('find_rq', ${ORG}, 'wi_rq', 'run_rq', 'correctness', 'blocking', 'Totals off by a cent', 'D', 'F')`;
  await owner`INSERT INTO review_questions (id, organization_id, task_id, run_id, n, category, finding_id, question, why_it_matters,
      evidence_checked, needed_from, created_at) VALUES
    ('rvq_1', ${ORG}, 'wi_rq', 'run_rq', 1, 'correctness', 'find_rq', 'Do invoices close in UTC?', 'Decides F1.', 'Read config.', 'product', '2026-01-02 10:00Z'),
    ('rvq_2', ${ORG}, 'wi_rq', 'run_rq', 2, 'correctness', NULL, 'Is a retry expected?', '', '', 'operations', '2026-01-02 10:01Z'),
    ('rvq_3', ${ORG}, 'wi_rq2', 'run_rq2', 1, 'correctness', NULL, 'Another task''s', '', '', '', '2026-01-02 10:02Z'),
    ('rvq_o', ${OTHER}, 'wi_o', 'run_o', 1, 'correctness', NULL, 'Theirs', '', '', '', '2026-01-02 10:03Z')`;
  app = new SQL(databaseUrl("app", NAME));
  setPool(app);
  ana = await createApiKey({ organizationId: ORG, name: "Ana" });
  cy = await createApiKey({ organizationId: OTHER, name: "Cy" });
  useConfig(Config.load({ env: { ...process.env } }));
  router = buildRouter("");
});

afterAll(async () => {
  useConfig(null);
  await closePool(app);
  await owner?.end();
  await admin?.unsafe(`DROP DATABASE IF EXISTS "${NAME}" WITH (FORCE)`);
  await admin?.end();
});

test("a task's questions are listed, oldest first, with the fields a reviewer gave", async () => {
  const res = await get(ana.key, "/v1/tasks/wi_rq/review-questions");
  expect(res.status).toBe(200);
  const { questions } = await res.json() as { questions: Array<Record<string, unknown>> };
  expect(questions.map((q) => [q.id, q.findingId, q.status])).toEqual([["rvq_1", "find_rq", "open"], ["rvq_2", null, "open"]]);
  expect(questions[0]).toMatchObject({
    taskId: "wi_rq", runId: "run_rq", category: "correctness", question: "Do invoices close in UTC?",
    whyItMatters: "Decides F1.", evidenceChecked: "Read config.", neededFrom: "product", answer: null,
  });
});

test("another organization's task is not found, and its questions are not listed", async () => {
  expect((await get(cy.key, "/v1/tasks/wi_rq/review-questions")).status).toBe(404);
  expect((await get(ana.key, "/v1/tasks/wi_o/review-questions")).status).toBe(404);
  const theirs = await (await get(cy.key, "/v1/tasks/wi_o/review-questions")).json() as { questions: Array<{ id: string }> };
  expect(theirs.questions.map((q) => q.id)).toEqual(["rvq_o"]);
});

test("the app's role reads only its own organization's questions, whatever it asks for", async () => {
  const ids = async (org: string) => (await withOrg(org, (scope) => scope.sql`SELECT id FROM review_questions ORDER BY id`) as Array<{ id: string }>)
    .map((r) => r.id);
  expect(await ids(ORG)).toEqual(["rvq_1", "rvq_2", "rvq_3"]);
  expect(await ids(OTHER)).toEqual(["rvq_o"]);
});
