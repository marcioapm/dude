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
    const url = new URL(request.url);
    if (request.method === "GET") {
      checks.push({ path: url.pathname, q: url.searchParams.get("q") });
      if (url.pathname.endsWith("/branches")) return Response.json({ branches: ["feature/greet"], more: false });
      return Response.json({ repositories: [{ id: REPO, name: "target", writable: false, reason: "Git push access denied" }] });
    }
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
      // #14 is a draft: unfinished work, which continues by default.
      return Response.json({ kind: "pr", repositoryId: REPO, repository: "target", number: Number(body.pullRequest),
        title: "Greet in Portuguese", headBranch: "feature/greet", baseBranch: "main",
        mode: body.pullRequest === "14" ? "continue" : "review" });
    }
    return Response.json({ kind: "branch", repositoryId: REPO, repository: "target", headBranch: body.branch,
      baseBranch: body.base || "main", mode: "continue" });
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
  SELECT kind, number, title, head_branch, base_branch, mode FROM task_sources WHERE task_id = ${taskId}`)[0] ?? null;

test("a task created from a pull request keeps it as GitHub said, and works on its repository", async () => {
  const res = await newTask({ kind: "pr", repositoryId: REPO, pullRequest: "12" });
  expect(res.status).toBe(201);
  const task = (await res.json()) as { id: string; source: unknown; repositories: unknown };
  expect(task.source).toEqual({ kind: "pr", repositoryId: REPO, repository: "target", number: 12, title: "Greet in Portuguese",
    headBranch: "feature/greet", baseBranch: "main", mode: "review" });
  expect(task.repositories).toEqual([{ id: REPO, access: "write" }]);
  expect(checks.at(-1)).toMatchObject({ path: `/internal/projects/${PROJECT}/sources/check`, kind: "pr", pullRequest: "12", taskId: "" });
  expect(await storedSource(task.id)).toEqual({ kind: "pr", number: 12, title: "Greet in Portuguese", head_branch: "feature/greet",
    base_branch: "main", mode: "review" });
  await owner`UPDATE tasks SET status = 'done' WHERE id = ${task.id}`;
});

test("a new source's mode is what it says unless the person chose one", async () => {
  const modeOf = async (source: unknown) => {
    const res = await newTask(source);
    expect(res.status).toBe(201);
    const task = (await res.json()) as { id: string; source: { mode: string } };
    const stored = (await storedSource(task.id))!.mode as string;
    await owner`UPDATE tasks SET status = 'done' WHERE id = ${task.id}`;
    expect(task.source.mode).toBe(stored);
    return stored;
  };
  expect(await modeOf({ kind: "pr", repositoryId: REPO, pullRequest: "12" })).toBe("review");
  expect(await modeOf({ kind: "pr", repositoryId: REPO, pullRequest: "14" })).toBe("continue");
  expect(await modeOf({ kind: "branch", repositoryId: REPO, branch: "feature/bare" })).toBe("continue");
  expect(await modeOf({ kind: "pr", repositoryId: REPO, pullRequest: "12", mode: "continue" })).toBe("continue");
  expect(await modeOf({ kind: "branch", repositoryId: REPO, branch: "feature/bare", mode: "review" })).toBe("review");
  const bad = await newTask({ kind: "pr", repositoryId: REPO, pullRequest: "12", mode: "rewrite" });
  expect(bad.status).toBe(400);
});

test("a source saved before modes reviews, as it did", async () => {
  const task = (await (await newTask()).json()) as { id: string };
  await owner`INSERT INTO task_sources (task_id, organization_id, repository_id, kind, head_branch, base_branch)
    VALUES (${task.id}, ${ORG}, ${REPO}, 'branch', 'feature/old', 'main')`;
  expect((await storedSource(task.id))!.mode).toBe("review");
  const read = await router.handle(new Request(`http://dude.test/v1/tasks/${task.id}`, { headers: { authorization: `Bearer ${key}` } }));
  expect(((await read.json()) as { source: { mode: string } }).source.mode).toBe("review");
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

test("the pickers ask the orchestrator, which holds the forge credential, with a bounded query", async () => {
  const repos = await router.handle(new Request(`http://dude.test/v1/projects/${PROJECT}/sources/repositories`,
    { headers: { authorization: `Bearer ${key}` } }));
  expect(repos.status).toBe(200);
  expect(await repos.json()).toEqual({ repositories: [{ id: REPO, name: "target", writable: false, reason: "Git push access denied" }] });
  const long = "x".repeat(500) + "/../&q=evil";
  const branches = await router.handle(new Request(
    `http://dude.test/v1/projects/${PROJECT}/sources/repositories/${REPO}/branches?q=${encodeURIComponent(long)}`,
    { headers: { authorization: `Bearer ${key}` } }));
  expect(branches.status).toBe(200);
  expect(checks.at(-1)).toEqual({ path: `/internal/projects/${PROJECT}/sources/repositories/${REPO}/branches`, q: long.slice(0, 200) });
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
  expect(await storedSource(task.id)).toMatchObject({ mode: "review" });

  // Its mode changes on its own, asking GitHub nothing; the same source sent with no mode keeps it.
  const beforeMode = checks.length;
  const switched = await call("PATCH", `/v1/tasks/${task.id}`, { source: { kind: "pr", repositoryId: REPO, pullRequest: "12", mode: "continue" } });
  expect(switched.status).toBe(200);
  expect(((await switched.json()) as { source: { mode: string } }).source.mode).toBe("continue");
  expect((await call("PATCH", `/v1/tasks/${task.id}`, { title: "Adopt the greeting",
    source: { kind: "pr", repositoryId: REPO, pullRequest: "12" } })).status).toBe(200);
  expect(checks.length).toBe(beforeMode);
  expect(await storedSource(task.id)).toMatchObject({ number: 12, mode: "continue" });

  await owner`INSERT INTO workflow_runs (id, organization_id, workflow_type, idempotency_key, task_id, status, step, state)
    VALUES (${"wf_" + task.id}, ${ORG}, 'task.delivery', ${"delivery:" + task.id}, ${task.id}, 'waiting', 'review', '{}'::jsonb)`;
  const locked = await call("PATCH", `/v1/tasks/${task.id}`, { source: { kind: "pr", repositoryId: REPO, pullRequest: "13" } });
  expect(locked.status).toBe(409);
  const cleared = await call("PATCH", `/v1/tasks/${task.id}`, { source: null });
  expect(cleared.status).toBe(409);
  const remoded = await call("PATCH", `/v1/tasks/${task.id}`, { source: { kind: "pr", repositoryId: REPO, pullRequest: "12", mode: "review" } });
  expect(remoded.status).toBe(409);
  expect(await storedSource(task.id)).toMatchObject({ number: 12, mode: "continue" });
});
