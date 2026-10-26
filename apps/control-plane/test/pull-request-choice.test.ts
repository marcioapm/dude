/**
 * Before the pull request (`pullRequest`) through the settings API: a
 * project's setting only. Its source on a project is "default" until the
 * project sets it, Reset takes it back there, the organisation's settings
 * refuse it, and a value an organisation stored is not read.
 *
 * Against a database of its own, migrated as a release is, and a stand-in
 * for the orchestrator that answers its defaults.
 *
 * Requires DATABASE_URL: a role that can create databases (the owner).
 */

import { afterAll, beforeAll, describe, expect, test } from "bun:test";
import { SQL } from "bun";
import { join } from "node:path";
import { promptRoleSchema } from "@dude/domain";
import { closePool, setPool } from "../src/db/client.ts";
import { buildRouter } from "../src/index.ts";
import { Config, useConfig } from "../src/config.ts";
import type { Router } from "../src/api/router.ts";
import { createApiKey } from "../src/api/auth.ts";

const OWNER_URL = process.env.DATABASE_URL ?? "postgres://dude:dude@localhost:5433/dude";
const ROOT = join(import.meta.dir, "../../..");
const NAME = `dude_prchoice_test_${Bun.randomUUIDv7("hex").slice(-12)}`;
const ORG = "org_prc";
const PROJECT = "prj_prc";
const GOAL = "Let people save the filters they use every day.";

function databaseUrl(appRole = false): string {
  const url = new URL(OWNER_URL);
  if (appRole) {
    url.username = "dude_app";
    url.password = "dude_app";
  }
  url.pathname = `/${NAME}`;
  return url.toString();
}

let admin: SQL;
let owner: SQL;
let app: SQL;
let router: Router;
let adminKey: string;
let orchestratorServer: ReturnType<typeof Bun.serve>;

type Json = any; // eslint-disable-line @typescript-eslint/no-explicit-any

function call(method: string, path: string, payload?: unknown) {
  return router.handle(new Request(`http://dude.test${path}`, {
    method,
    headers: { authorization: `Bearer ${adminKey}`, "content-type": "application/json" },
    ...(payload === undefined ? {} : { body: JSON.stringify(payload) }),
  }));
}

beforeAll(async () => {
  admin = new SQL(OWNER_URL);
  await admin.unsafe(`CREATE DATABASE "${NAME}"`);
  const migrate = Bun.spawnSync(["bun", "run", "apps/control-plane/src/db/migrate.ts"], {
    cwd: ROOT, env: { ...process.env, DATABASE_URL: databaseUrl() },
  });
  if (migrate.exitCode !== 0) throw new Error(`migrate: ${migrate.stderr.toString()}`);
  owner = new SQL(databaseUrl());
  await owner`INSERT INTO organizations (id, name, slug) VALUES (${ORG}, 'Acme', 'acme')`;
  await owner`INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES (${PROJECT}, ${ORG}, 'web', 'web', 'WEB')`;
  app = new SQL(databaseUrl(true));
  setPool(app);
  adminKey = (await createApiKey({ organizationId: ORG, name: "Ana" })).key;
  orchestratorServer = Bun.serve({
    port: 0,
    fetch(req) {
      const path = new URL(req.url).pathname;
      if (path === "/internal/network/defaults") return Response.json({ operator: [], always: [], model: null });
      if (path.endsWith("builtin")) return Response.json(Object.fromEntries(promptRoleSchema.options.map((r) => [r, "Built-in prompt"])));
      return Response.json({ requiredReviewers: ["correctness"], blockingSeverities: ["blocking"], maxReviewIterations: 3,
        maxAttemptsPerFinding: 2, maxPrFixIterations: 3, simplify: true, test: true, parkAfterMinutes: 10, idleNudgeMinutes: 0,
        conductorWarmMinutes: 5, pullRequest: "open" });
    },
  });
  useConfig(Config.load({ env: { ...process.env,
    DUDE_ORCHESTRATOR_URL: `http://localhost:${orchestratorServer.port}`, DUDE_ORCHESTRATOR_TOKEN: "svc" } }));
  router = buildRouter("");
});

afterAll(async () => {
  useConfig(null);
  await orchestratorServer?.stop(true);
  if (app) await closePool(app);
  await owner?.end();
  await admin?.unsafe(`DROP DATABASE IF EXISTS "${NAME}" WITH (FORCE)`);
  await admin?.end();
});

const projectChoice = async () => ((await (await call("GET", `/v1/projects/${PROJECT}/settings`)).json()) as Json).delivery.pullRequest;

describe("before the pull request", () => {
  test("a project reads the factory's Open it, from the default", async () => {
    expect(await projectChoice()).toEqual({ value: "open", source: "default" });
  });

  test("an organisation cannot set it, and a value it stored is not read", async () => {
    const res = await call("PATCH", "/v1/settings/organization", { delivery: { pullRequest: "ask" } });
    expect(res.status).toBe(400);
    expect(JSON.stringify(await res.json())).toContain("project's setting");
    await owner`UPDATE organizations SET delivery_policy = '{"pullRequest":"ask"}' WHERE id = ${ORG}`;
    expect(await projectChoice()).toEqual({ value: "open", source: "default" });
  });

  test("a project sets it, and Reset puts the default back", async () => {
    const res = await call("PATCH", `/v1/projects/${PROJECT}/settings`, { delivery: { pullRequest: "ask" } });
    expect(res.status).toBe(200);
    expect(((await res.json()) as Json).delivery.pullRequest).toEqual({ value: "ask", source: "project" });
    const [{ delivery_policy }] = await owner`SELECT delivery_policy FROM projects WHERE id = ${PROJECT}`;
    expect(delivery_policy).toEqual({ pullRequest: "ask" });
    expect((await call("PATCH", `/v1/projects/${PROJECT}/settings`, { delivery: { pullRequest: "later" } })).status).toBe(400);
    const reset = await call("PATCH", `/v1/projects/${PROJECT}/settings`, { delivery: { pullRequest: null } });
    expect(((await reset.json()) as Json).delivery.pullRequest).toEqual({ value: "open", source: "default" });
  });

  test("a task is made with its own choice, and reads it with the project's and whether a pull request is open", async () => {
    await call("PATCH", `/v1/projects/${PROJECT}/settings`, { delivery: { pullRequest: "draft" } });
    const made = await call("POST", "/v1/tasks", { projectId: PROJECT, title: "Saved filters", goal: GOAL, pullRequestChoice: "ask" });
    if (made.status !== 201) throw new Error(await made.text());
    const task = (await made.json()) as Json;
    expect(task).toMatchObject({ pullRequestChoice: "ask", projectPullRequest: "draft", pullRequestOpened: false });
    const [{ pull_request_choice }] = await owner`SELECT pull_request_choice FROM tasks WHERE id = ${task.id}`;
    expect(pull_request_choice).toBe("ask");
    const plain = (await (await call("POST", "/v1/tasks", { projectId: PROJECT, title: "Plain", goal: GOAL })).json()) as Json;
    expect(plain.pullRequestChoice).toBeNull();
    expect((await call("POST", "/v1/tasks", { projectId: PROJECT, title: "Bad", goal: GOAL, pullRequestChoice: "later" })).status).toBe(400);

    await owner`INSERT INTO repositories (id, organization_id, project_id, name, url) VALUES ('repo_prc', ${ORG}, ${PROJECT}, 'web', 'https://github.com/acme/web.git')`;
    await owner`INSERT INTO pull_requests (id, organization_id, project_id, task_id, repository_id, number, url, head_branch, base_branch, head_sha, title)
      VALUES ('pr_prc', ${ORG}, ${PROJECT}, ${task.id}, 'repo_prc', 1, 'https://x/1', 'b', 'main', 'h', 'T')`;
    const read = (await (await call("GET", `/v1/tasks/${task.id}`)).json()) as Json;
    expect(read.pullRequestOpened).toBe(true);
  });

  test("a change on the task is checked before it is forwarded", async () => {
    expect((await call("PUT", "/v1/tasks/wi_x/pull-request-choice", { choice: "later" })).status).toBe(400);
    expect((await call("PUT", "/v1/tasks/wi_x/pull-request-choice", {})).status).toBe(400);
  });

  test("a held task reads its hold and since when from the delivery; once a pull request is on record, no hold", async () => {
    const task = (await (await call("POST", "/v1/tasks", { projectId: PROJECT, title: "Held", goal: GOAL })).json()) as Json;
    const since = "2026-10-26T09:30:00.123456Z";
    const hold = { round: 1, heads: { web: "abc" }, since, repos: [{ name: "web", head: "dude/x", base: "main", additions: 3, deletions: 1 }] };
    await owner`INSERT INTO workflow_runs (id, organization_id, workflow_type, idempotency_key, task_id, status, step, state)
      VALUES ('wf_prc_hold', ${ORG}, 'task.delivery', 'prc-hold', ${task.id}, 'waiting', 'conductorDecision',
              ${JSON.stringify({ decision: { point: "before_pull_request" }, hold })}::jsonb)`;
    const held = (await (await call("GET", `/v1/tasks/${task.id}`)).json()) as Json;
    expect(held.hold.round).toBe(1);
    expect(Date.parse(held.hold.since)).toBe(Date.parse(since));
    await owner`UPDATE workflow_runs SET state = state || '{"pullRequestIds":["pr_x"]}'::jsonb WHERE id = 'wf_prc_hold'`;
    const opened = (await (await call("GET", `/v1/tasks/${task.id}`)).json()) as Json;
    expect(opened.hold).toBeNull();
  });
});
