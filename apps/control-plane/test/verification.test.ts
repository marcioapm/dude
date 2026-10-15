/**
 * A task's verification through the public API: each test Run's verdict
 * on every criterion, with the heads it tested and whether the work has
 * moved since, a person's waiver with who and why, and nothing of another
 * organisation's. Against a database of its own, migrated as a release is.
 */

import { afterAll, beforeAll, expect, test } from "bun:test";
import { SQL } from "bun";
import { join } from "node:path";
import { closePool, setPool } from "../src/db/client.ts";
import { buildRouter } from "../src/index.ts";
import type { Router } from "../src/api/router.ts";
import { authenticate, createApiKey } from "../src/api/auth.ts";

const OWNER_URL = process.env.DATABASE_URL ?? "postgres://dude:dude@localhost:5433/dude";
const ROOT = join(import.meta.dir, "../../..");
const NAME = `dude_verification_test_${Bun.randomUUIDv7("hex").slice(-12)}`;

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
let ana: { key: string; personId: string };
let other: { key: string };

beforeAll(async () => {
  admin = new SQL(OWNER_URL);
  await admin.unsafe(`CREATE DATABASE "${NAME}"`);
  const migrate = Bun.spawnSync(["bun", "run", "apps/control-plane/src/db/migrate.ts"], {
    cwd: ROOT, env: { ...process.env, DATABASE_URL: databaseUrl("owner", NAME) },
  });
  if (migrate.exitCode !== 0) throw new Error(`migrate: ${migrate.stderr.toString()}`);
  owner = new SQL(databaseUrl("owner", NAME));
  for (const org of ["org_v", "org_w"]) await owner`INSERT INTO organizations (id, name, slug) VALUES (${org}, ${org}, ${org})`;
  app = new SQL(databaseUrl("app", NAME));
  setPool(app);
  ana = await createApiKey({ organizationId: "org_v", name: "Ana" });
  other = await createApiKey({ organizationId: "org_w", name: "Wes" });
  await owner`INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES ('prj_v', 'org_v', 'P', 'p', 'P')`;
  await owner`INSERT INTO tasks (id, organization_id, project_id, number, title) VALUES ('wi_v', 'org_v', 'prj_v', 1, 'Greet')`;
  for (const [run, at] of [["run_t1", "2026-01-01T00:00:00Z"], ["run_t2", "2026-01-02T00:00:00Z"]]) {
    await owner`INSERT INTO runs (id, organization_id, project_id, task_id, attempt, phase, role, status, created_at)
      VALUES (${run}, 'org_v', 'prj_v', 'wi_v', 1, 'test', 'qa_browser', 'completed', ${at})`;
  }
  // The first test, at the commit before the simplifier; the second waived.
  await owner`INSERT INTO acceptance_verdicts (organization_id, task_id, run_id, criterion, text, status, evidence, observed, heads, created_at)
    VALUES ('org_v', 'wi_v', 'run_t1', 1, 'it greets', 'fail', '{}', 'No greeting.', '{"api":"aaa111"}', '2026-01-01T00:00:00Z')`;
  await owner`INSERT INTO acceptance_verdicts (organization_id, task_id, run_id, criterion, text, status, evidence, observed, problem,
      heads, decision, decided_by, decision_note, decided_at, created_at)
    VALUES ('org_v', 'wi_v', 'run_t2', 1, 'it greets', 'unverified', '{walkthrough.webm}', 'It greets.', 'missing.png is not a file this test published.',
      '{"api":"bbb222"}', 'waived', ${ana.personId}, 'Checked by hand.', now(), '2026-01-02T00:00:00Z')`;
  await owner`INSERT INTO workflow_runs (id, organization_id, task_id, workflow_type, idempotency_key, status, step, state)
    VALUES ('wf_v', 'org_v', 'wi_v', 'task.delivery', 'delivery:wi_v', 'waiting', 'awaitPullRequest', '{"attempt":1,"heads":{"api":"bbb222"}}')`;
  router = buildRouter("", async (credential) => (credential === null ? null : authenticate(credential)));
});

afterAll(async () => {
  await closePool(app);
  await owner?.end();
  await admin?.unsafe(`DROP DATABASE IF EXISTS "${NAME}" WITH (FORCE)`);
  await admin?.end();
});

async function get(key: string, path: string): Promise<Response> {
  return router.handle(new Request(`http://dude.test${path}`, { headers: { authorization: `Bearer ${key}` } }));
}

test("a task's verification is each test's verdict, the latest first, stale once the work moved past it", async () => {
  const res = await get(ana.key, "/v1/tasks/wi_v/verification");
  expect(res.status).toBe(200);
  const { verifications } = (await res.json()) as { verifications: Array<Record<string, any>> };
  expect(verifications.map((v) => [v.runId, v.stale])).toEqual([["run_t2", false], ["run_t1", true]]);
  expect(verifications[0]!.heads).toEqual({ api: "bbb222" });
  expect(verifications[0]!.criteria).toEqual([{
    criterion: 1, text: "it greets", status: "unverified", evidence: ["walkthrough.webm"], observed: "It greets.",
    problem: "missing.png is not a file this test published.", decision: "waived",
    decidedBy: { id: ana.personId, name: "Ana" }, decisionNote: "Checked by hand.",
  }]);
});

test("another organisation sees none of it", async () => {
  const res = await get(other.key, "/v1/tasks/wi_v/verification");
  expect(res.status).toBe(200);
  expect(await res.json()).toEqual({ verifications: [] });
});
