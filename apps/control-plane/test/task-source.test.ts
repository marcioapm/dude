/**
 * A task's source — the existing pull request or branch it starts from —
 * on create and update: kept as the orchestrator resolved it, refused in
 * the orchestrator's words, and fixed once the task's delivery has started.
 *
 * Against a database of its own and a stand-in orchestrator that answers
 * the source check as GitHub would through it. Requires DATABASE_URL: a
 * role that can create databases.
 */

import { afterAll, beforeAll, expect, test } from "bun:test";
import { SQL } from "bun";
import { closePool, setPool } from "../src/db/client.ts";
import { migrate } from "../src/db/migrate.ts";
import { buildRouter } from "../src/index.ts";
import type { Router } from "../src/api/router.ts";
import { createApiKey } from "../src/api/auth.ts";
import { Config, useConfig } from "../src/config.ts";

const OWNER_URL = process.env.DATABASE_URL ?? "postgres://dude:dude@localhost:5433/dude";
const NAME = `dude_task_source_${Bun.randomUUIDv7("hex").slice(-12)}`;
const ORG = "org_task_source";
const PROJECT = "prj_task_source";
const REPO = "repo_task_source";

function databaseUrl(user: "owner" | "app"): string {
  const url = new URL(OWNER_URL);
  if (user === "app") {
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
let key: string;
let orchestrator: ReturnType<typeof Bun.serve>;
// What the stand-in was asked, newest last.
const checks: Array<Record<string, unknown>> = [];

beforeAll(async () => {
  admin = new SQL(OWNER_URL);
  await admin.unsafe(`CREATE DATABASE "${NAME}"`);
  await migrate(databaseUrl("owner"), { log() {} });
  owner = new SQL(databaseUrl("owner"));
  await owner`INSERT INTO organizations (id, name, slug) VALUES (${ORG}, ${ORG}, ${ORG})`;
  await owner`INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES (${PROJECT}, ${ORG}, 'Src', 'src', 'SRC')`;
  await owner`INSERT INTO repositories (id, organization_id, project_id, name, url, default_branch)
    VALUES (${REPO}, ${ORG}, ${PROJECT}, 'target', 'https://github.com/acme/target', 'main')`;
  app = new SQL(databaseUrl("app"));
  setPool(app);
  key = (await createApiKey({ organizationId: ORG, name: "Ana" })).key;
  orchestrator = Bun.serve({ port: 0, async fetch(request) {
    const body = (await request.json()) as Record<string, unknown>;
    checks.push({ path: new URL(request.url).pathname, ...body });
    if (body.pullRequest === "7") {
      return Response.json({ error: { code: "pr_merged", message: "pull request #7 is merged: only an open one can be adopted" } },
        { status: 422 });
    }
    if (body.pullRequest === "8") {
      return Response.json({ error: { code: "github_unavailable", message: "GitHub could not be asked about it: down" } }, { status: 502 });
    }
    if (body.kind === "pr") {
      return Response.json({ kind: "pr", repositoryId: REPO, repository: "target", number: Number(body.pullRequest),
        title: "Greet in Portuguese", headBranch: "feature/greet", baseBranch: "main" });
    }
    return Response.json({ kind: "branch", repositoryId: REPO, repository: "target", headBranch: body.branch,
      baseBranch: body.base || "main" });
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

const call = (method: string, path: string, body: unknown) => router.handle(new Request(`http://dude.test${path}`, {
  method, headers: { authorization: `Bearer ${key}`, "content-type": "application/json" }, body: JSON.stringify(body),
}));

const newTask = (source?: unknown) => call("POST", "/v1/tasks", {
  projectId: PROJECT, title: "Adopt", goal: "Take over the greeting work already open", ...(source === undefined ? {} : { source }),
});

const storedSource = async (taskId: string) => (await owner`
  SELECT kind, number, title, head_branch, base_branch FROM task_sources WHERE task_id = ${taskId}`)[0] ?? null;

test("a task created from a pull request keeps it as GitHub said, and works on its repository", async () => {
  const res = await newTask({ kind: "pr", repositoryId: REPO, pullRequest: "12" });
  expect(res.status).toBe(201);
  const task = (await res.json()) as { id: string; source: unknown; repositories: unknown };
  expect(task.source).toEqual({ kind: "pr", repositoryId: REPO, repository: "target", number: 12, title: "Greet in Portuguese",
    headBranch: "feature/greet", baseBranch: "main" });
  expect(task.repositories).toEqual([{ id: REPO, access: "write" }]);
  expect(checks.at(-1)).toMatchObject({ path: `/internal/projects/${PROJECT}/sources/check`, kind: "pr", pullRequest: "12", taskId: "" });
  expect(await storedSource(task.id)).toEqual({ kind: "pr", number: 12, title: "Greet in Portuguese", head_branch: "feature/greet",
    base_branch: "main" });
  await owner`UPDATE tasks SET status = 'done' WHERE id = ${task.id}`;
});

test("simultaneous sources reserve one live task per head while different heads work", async () => {
  const source = { kind: "branch", repositoryId: REPO, branch: "feature/concurrent", base: "main" };
  const results = await Promise.all([newTask(source), newTask(source)]);
  expect(results.map(r => r.status).sort()).toEqual([201, 422]);
  const refused = results.find(r => r.status === 422)!;
  expect(((await refused.json()) as { error: { code: string } }).error.code).toBe("branch_taken");
  const different = await Promise.all([
    newTask({ ...source, branch: "feature/one" }), newTask({ ...source, branch: "feature/two" }),
  ]);
  expect(different.map(r => r.status)).toEqual([201, 201]);
  const tasks = await Promise.all([newTask(), newTask()]);
  const ids = await Promise.all(tasks.map(async r => ((await r.json()) as { id: string }).id));
  const attached = await Promise.all(ids.map(id => call("PATCH", `/v1/tasks/${id}`, {
    source: { ...source, branch: "feature/attach" },
  })));
  expect(attached.map(r => r.status).sort()).toEqual([200, 422]);
});

test("a refused source is the orchestrator's answer, and no task is made", async () => {
  const before = (await owner`SELECT count(*)::int AS n FROM tasks`)[0].n as number;
  const merged = await newTask({ kind: "pr", repositoryId: REPO, pullRequest: "7" });
  expect(merged.status).toBe(422);
  expect(((await merged.json()) as { error: { code: string; message: string } }).error)
    .toEqual({ code: "pr_merged", message: "pull request #7 is merged: only an open one can be adopted" });
  const down = await newTask({ kind: "pr", repositoryId: REPO, pullRequest: "8" });
  expect(down.status).toBe(502);
  expect(((await down.json()) as { error: { code: string } }).error.code).toBe("github_unavailable");
  expect((await owner`SELECT count(*)::int AS n FROM tasks`)[0].n).toBe(before);
});

test("a task's source changes, or clears, until its delivery starts; then it is fixed", async () => {
  const task = (await (await newTask()).json()) as { id: string; source: unknown };
  expect(task.source).toBeNull();

  const set = await call("PATCH", `/v1/tasks/${task.id}`, { source: { kind: "branch", repositoryId: REPO, branch: "stack/top", base: "stack/base" } });
  expect(set.status).toBe(200);
  expect(((await set.json()) as { source: unknown }).source).toMatchObject({ kind: "branch", headBranch: "stack/top", baseBranch: "stack/base" });
  expect(checks.at(-1)).toMatchObject({ taskId: task.id, branch: "stack/top", base: "stack/base" });

  // The same source again is no change: GitHub is not asked.
  const asked = checks.length;
  const again = await call("PATCH", `/v1/tasks/${task.id}`, { title: "Adopt the stack",
    source: { kind: "branch", repositoryId: REPO, branch: "stack/top", base: "stack/base" } });
  expect(again.status).toBe(200);
  expect(checks.length).toBe(asked);

  expect((await call("PATCH", `/v1/tasks/${task.id}`, { source: null })).status).toBe(200);
  expect(await storedSource(task.id)).toBeNull();
  expect((await call("PATCH", `/v1/tasks/${task.id}`, { source: { kind: "pr", repositoryId: REPO, pullRequest: "12" } })).status).toBe(200);

  await owner`INSERT INTO workflow_runs (id, organization_id, workflow_type, idempotency_key, task_id, status, step, state)
    VALUES (${"wf_" + task.id}, ${ORG}, 'task.delivery', ${"delivery:" + task.id}, ${task.id}, 'waiting', 'review', '{}'::jsonb)`;
  const locked = await call("PATCH", `/v1/tasks/${task.id}`, { source: { kind: "pr", repositoryId: REPO, pullRequest: "13" } });
  expect(locked.status).toBe(409);
  const cleared = await call("PATCH", `/v1/tasks/${task.id}`, { source: null });
  expect(cleared.status).toBe(409);
  expect(await storedSource(task.id)).toMatchObject({ number: 12 });
});
