/**
 * Task relations through the public API (docs/design/task-relations.md):
 * the backend's own start path (a bare Run, path 8) refused while the task
 * waits on unmerged work, and allowed once its blocker merged.
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
const NAME = `dude_relations_test_${Bun.randomUUIDv7("hex").slice(-12)}`;
const ORG = "org_rel";
const PROJECT = "prj_rel";

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
let ana: { key: string; id: string; personId: string };
let bo: { key: string; id: string; personId: string };
let orchestrator: ReturnType<typeof Bun.serve>;
const forwarded: Array<{ path: string; body: unknown }> = [];

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
  await owner`INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES (${PROJECT}, ${ORG}, 'Control plane', 'cp', 'CP')`;
  await owner`INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES ('prj_web', ${ORG}, 'Web', 'web', 'WEB')`;
  await owner`INSERT INTO repositories (id, organization_id, project_id, name, url, default_branch)
    VALUES ('repo_rel', ${ORG}, ${PROJECT}, 'cp', 'https://github.com/acme/cp', 'main')`;
  app = new SQL(databaseUrl("app", NAME));
  setPool(app);
  ana = await createApiKey({ organizationId: ORG, name: "Ana" });
  bo = await createApiKey({ organizationId: ORG, name: "Bo" });
  orchestrator = Bun.serve({ port: 0, async fetch(request) {
    forwarded.push({ path: new URL(request.url).pathname, body: await request.json().catch(() => null) });
    return Response.json({ ok: true }, { status: 201 });
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

async function call(as: { key: string }, method: string, path: string, body?: unknown): Promise<Response> {
  return router.handle(new Request(`http://dude.test${path}`, {
    method, headers: { authorization: `Bearer ${as.key}`, "content-type": "application/json" },
    ...(body === undefined ? {} : { body: JSON.stringify(body) }),
  }));
}

let number = 0;
/** A task in CP (or another project), owned by `by`; returns its id. */
async function task(by: { personId: string }, status = "received", project = PROJECT): Promise<string> {
  number += 1;
  const id = `wi_rel_${number}`;
  await owner`INSERT INTO tasks (id, organization_id, project_id, number, title, goal, status)
    VALUES (${id}, ${ORG}, ${project}, ${number}, ${`Task ${number}`}, 'A goal long enough to save.', ${status}::task_status)`;
  await owner`INSERT INTO task_people (task_id, person_id, organization_id, position) VALUES (${id}, ${by.personId}, ${ORG}, 0)`;
  return id;
}

const key = (id: string) => `CP-${id.replace("wi_rel_", "")}`;

async function blocks(from: string, to: string): Promise<void> {
  await owner`INSERT INTO task_relations (organization_id, from_task_id, to_task_id, kind, created_by_person)
    VALUES (${ORG}, ${from}, ${to}, 'blocks', ${ana.personId})`;
}

async function merged(id: string): Promise<void> {
  await owner`UPDATE tasks SET status = 'done' WHERE id = ${id}`;
  await owner`INSERT INTO pull_requests (id, organization_id, project_id, task_id, repository_id, number, url, head_branch,
      base_branch, head_sha, title, state)
    VALUES (${`pr_${id}`}, ${ORG}, ${PROJECT}, ${id}, 'repo_rel', ${Number(id.replace("wi_rel_", "")) + 100}, 'u', 'h', 'main', 's', 't', 'merged')`;
}

test("path 8: a bare Run on a blocked task is refused with the start paths' words, and allowed once the blocker merged", async () => {
  const blocker = await task(ana, "review");
  const blocked = await task(ana);
  await blocks(blocker, blocked);
  const res = await call(ana, "POST", `/v1/tasks/${blocked}/runs`);
  expect(res.status).toBe(409);
  const body = (await res.json()) as { error: { code: string; message: string; details: { blockers: unknown[] } } };
  expect(body.error.code).toBe("blocked");
  expect(body.error.message).toBe(`${key(blocked)} is blocked by ${key(blocker)} (in review): it can start once that's merged.`);
  expect(body.error.details.blockers).toEqual([{ key: key(blocker), status: "review" }]);
  const [{ n }] = await owner`SELECT count(*)::int AS n FROM runs WHERE task_id = ${blocked}`;
  expect(n).toBe(0);
  const [{ refusals }] = await owner`SELECT count(*)::int AS refusals FROM events WHERE task_id = ${blocked}
    AND event_type = 'task.start_refused' AND payload->>'path' = 'create_run'`;
  expect(refusals).toBe(1);

  await merged(blocker);
  expect((await call(ana, "POST", `/v1/tasks/${blocked}/runs`)).status).toBe(201);
});

test("path 8: a blocker done with nothing merged still blocks a bare Run", async () => {
  const blocker = await task(ana, "done");
  const blocked = await task(ana);
  await blocks(blocker, blocked);
  const res = await call(ana, "POST", `/v1/tasks/${blocked}/runs`);
  expect(res.status).toBe(409);
  expect(((await res.json()) as { error: { message: string } }).error.message)
    .toBe(`${key(blocked)} is blocked by ${key(blocker)} (done, nothing merged): it can start once that's merged.`);
});

type Relations = {
  blockedBy: Array<{ task: { key: string; project: string | null; unmerged: boolean; merged: boolean }; canRemove: boolean; removableBy: string | null; why: string | null }>;
  blocks: Array<{ task: { key: string }; canRemove: boolean; removableBy: string | null }>;
  related: Array<{ task: { key: string }; canRemove: boolean }>;
  autoStart: { how: string; by: { name: string } } | null;
};

async function relations(as: { key: string }, id: string): Promise<Relations> {
  const res = await call(as, "GET", `/v1/tasks/${id}/relations`);
  expect(res.status).toBe(200);
  return (await res.json()) as Relations;
}

test("any member adds a relation by key, on both tasks' history; the reader is told what they may remove", async () => {
  const a = await task(ana);
  const b = await task(ana);
  const web = await task(bo, "running", "prj_web");
  let res = await call(bo, "POST", `/v1/tasks/${b}/relations`, { kind: "blocked_by", task: key(a), why: "needs the meter" });
  expect(res.status).toBe(201);
  res = await call(bo, "POST", `/v1/tasks/${b}/relations`, { kind: "blocked_by", task: web });
  expect(res.status).toBe(201);
  const [{ n }] = await owner`SELECT count(*)::int AS n FROM events WHERE event_type = 'task.relation_added' AND task_id IN (${a}, ${b}, ${web})`;
  expect(n).toBe(4);

  // Bo reads Ana's task: the edges hold Ana's task up, so they are hers to remove.
  const asBo = await relations(bo, b);
  expect(asBo.blockedBy.map((r) => r.task.key)).toEqual([key(a), "WEB-" + web.replace("wi_rel_", "")]);
  expect(asBo.blockedBy[0]!.why).toBe("needs the meter");
  expect(asBo.blockedBy[1]!.task.project).toBe("Web");
  expect(asBo.blockedBy.every((r) => !r.canRemove && r.removableBy === "Only Ana, the owner, can remove this")).toBe(true);
  // On the blocker's page, the same edge is still the blocked task's owner's.
  const onA = await relations(bo, a);
  expect(onA.blocks[0]!.canRemove).toBe(false);
  expect(onA.blocks[0]!.removableBy).toBe(`Only Ana, ${key(b)}'s owner, can remove this`);
  expect((await relations(ana, b)).blockedBy.every((r) => r.canRemove)).toBe(true);
});

test("removing is the blocked task's owner's for blocks, either owner's for related", async () => {
  const a = await task(ana);
  const b = await task(bo);
  expect((await call(ana, "POST", `/v1/tasks/${a}/relations`, { kind: "blocks", task: b })).status).toBe(201);
  // Ana's task blocks Bo's: Bo's to remove, not Ana's, from either page.
  let res = await call(ana, "DELETE", `/v1/tasks/${a}/relations/blocks/${b}`);
  expect(res.status).toBe(403);
  expect(((await res.json()) as { error: { code: string; message: string } }).error)
    .toEqual({ code: "not_owner", message: "only Bo can remove this relation — it holds up their task" });
  expect((await call(bo, "DELETE", `/v1/tasks/${b}/relations/blocked_by/${a}`)).status).toBe(204);

  // A related edge holds nothing up: it is on their task.
  const c = await task(bo);
  const d = await task(bo);
  expect((await call(bo, "POST", `/v1/tasks/${c}/relations`, { kind: "related", task: d })).status).toBe(201);
  res = await call(ana, "DELETE", `/v1/tasks/${c}/relations/related/${d}`);
  expect(res.status).toBe(403);
  expect(((await res.json()) as { error: { message: string } }).error.message).toBe("only Bo can remove this relation — it's on their task");

  expect((await call(ana, "POST", `/v1/tasks/${b}/relations`, { kind: "related", task: a })).status).toBe(201);
  expect((await relations(ana, b)).related[0]!.canRemove).toBe(true);
  expect((await call(ana, "DELETE", `/v1/tasks/${b}/relations/related/${a}`)).status).toBe(204);
});

test("a loop is refused naming it, a self-relation and a duplicate too", async () => {
  const [a, b, c] = [await task(ana), await task(ana), await task(ana)];
  await call(ana, "POST", `/v1/tasks/${a}/relations`, { kind: "blocks", task: b });
  await call(ana, "POST", `/v1/tasks/${b}/relations`, { kind: "blocks", task: c });
  let res = await call(ana, "POST", `/v1/tasks/${c}/relations`, { kind: "blocks", task: a });
  expect(res.status).toBe(409);
  const body = (await res.json()) as { error: { code: string; details: { loop: string[] } } };
  expect(body.error.code).toBe("cycle");
  expect(body.error.details.loop).toEqual([key(a), key(b), key(c), key(a)]);
  // Related gates nothing: never a loop.
  expect((await call(ana, "POST", `/v1/tasks/${c}/relations`, { kind: "related", task: a })).status).toBe(201);
  res = await call(ana, "POST", `/v1/tasks/${a}/relations`, { kind: "blocks", task: a });
  expect(res.status).toBe(400);
  expect(((await res.json()) as { error: { code: string } }).error.code).toBe("self");
  res = await call(ana, "POST", `/v1/tasks/${a}/relations`, { kind: "blocks", task: b });
  expect(res.status).toBe(409);
  expect(((await res.json()) as { error: { code: string } }).error.code).toBe("duplicate");
});

test("start when unblocked is the owner's, only on a task blocked by something that can merge", async () => {
  const blocker = await task(ana, "review");
  const t = await task(ana);
  const free = await task(ana);
  await blocks(blocker, t);
  let res = await call(bo, "PUT", `/v1/tasks/${t}/auto-start`, { how: "deliver" });
  expect(res.status).toBe(403);
  expect(((await res.json()) as { error: { message: string } }).error.message).toBe("only Ana, its owner, can set this");
  expect((await call(ana, "PUT", `/v1/tasks/${free}/auto-start`, { how: "deliver" })).status).toBe(409);
  expect((await call(ana, "PUT", `/v1/tasks/${t}/auto-start`, { how: "deliver" })).status).toBe(200);
  expect((await relations(bo, t)).autoStart).toMatchObject({ how: "deliver", by: { name: "Ana" } });
  expect((await call(bo, "DELETE", `/v1/tasks/${t}/auto-start`)).status).toBe(403);
  expect((await call(ana, "DELETE", `/v1/tasks/${t}/auto-start`)).status).toBe(204);
  const [{ why }] = await owner`SELECT payload->>'why' AS why FROM events WHERE task_id = ${t} AND event_type = 'task.auto_start_cancelled'`;
  expect(why).toBe("owner");

  // A blocker done with nothing merged will never merge: refused.
  const spike = await task(ana, "done");
  const after = await task(ana);
  await blocks(spike, after);
  res = await call(ana, "PUT", `/v1/tasks/${after}/auto-start`, { how: "talk" });
  expect(res.status).toBe(409);
  expect(((await res.json()) as { error: { code: string; message: string } }).error)
    .toMatchObject({ code: "never_merges", message: `${key(spike)} was marked done with nothing merged and will never merge, so this would never start it.` });
});

test("a change of owner drops the request, and removing the last blocker ends it without firing", async () => {
  const blocker = await task(ana, "review");
  const t = await task(ana);
  await blocks(blocker, t);
  expect((await call(ana, "PUT", `/v1/tasks/${t}/auto-start`, { how: "talk" })).status).toBe(200);
  const res = await call(ana, "PUT", `/v1/tasks/${t}/people`, { people: [bo.personId] });
  expect(res.status).toBeLessThan(300);
  let [{ why }] = await owner`SELECT payload->>'why' AS why FROM events WHERE task_id = ${t} AND event_type = 'task.auto_start_cancelled'`;
  expect(why).toBe("owner_changed");
  expect((await relations(bo, t)).autoStart).toBeNull();

  const t2 = await task(ana);
  await blocks(blocker, t2);
  expect((await call(ana, "PUT", `/v1/tasks/${t2}/auto-start`, { how: "deliver" })).status).toBe(200);
  expect((await call(ana, "DELETE", `/v1/tasks/${t2}/relations/blocked_by/${blocker}`)).status).toBe(204);
  [{ why }] = await owner`SELECT payload->>'why' AS why FROM events WHERE task_id = ${t2} AND event_type = 'task.auto_start_cancelled'`;
  expect(why).toBe("unblocked_by_removal");
  const [{ n }] = await owner`SELECT count(*)::int AS n FROM workflow_runs WHERE task_id = ${t2}`;
  expect(n).toBe(0);
});

test("deliver and talk pass a person's override and note to the orchestrator", async () => {
  forwarded.length = 0;
  const t = await task(ana);
  expect((await call(ana, "POST", `/v1/tasks/${t}/deliver`, { override: true, note: "enough" })).status).toBe(201);
  expect((await call(ana, "POST", `/v1/tasks/${t}/talk`, { override: true })).status).toBe(201);
  expect(forwarded.map((f) => f.body)).toEqual([{ override: true, note: "enough" }, { override: true }]);
  expect((await call(ana, "POST", `/v1/tasks/${t}/deliver`, { override: "yes" })).status).toBe(400);
});

type NavTaskOut = {
  key: string; merged: boolean; startable: boolean; canStart: boolean;
  blockedBy: Array<{ key: string; title: string; status: string; unmerged?: boolean; project?: string }>;
  blocks: Array<{ key: string }>;
  edges: { blockedBy: string[]; blocks: string[]; related: string[] };
  autoStart: { how: string; by: { name: string } } | null;
};

test("the navigation tree says what each task waits on, holds up and whether it can start", async () => {
  const meter = await task(ana, "review");
  const sync = await task(ana, "done");
  await merged(sync);
  const spike = await task(ana, "done");
  const page = await task(ana);
  const web = await task(ana, "received", "prj_web");
  await blocks(meter, page);
  await blocks(sync, page);
  await blocks(spike, web);
  await owner`INSERT INTO task_auto_starts (task_id, organization_id, how, set_by_person) VALUES (${page}, ${ORG}, 'deliver', ${ana.personId})`;
  const res = await call(bo, "GET", "/v1/navigation");
  const tree = (await res.json()) as { projects: Array<{ tasks: NavTaskOut[] }> };
  const all = tree.projects.flatMap((p) => p.tasks);
  const byKey = (k: string) => all.find((t) => t.key === k)!;
  const p = byKey(key(page));
  // The merged blocker no longer shows; the edge stays for the hover.
  expect(p.blockedBy.map((b) => b.key)).toEqual([key(meter)]);
  expect(p.edges.blockedBy).toEqual([key(meter), key(sync)]);
  expect(p.startable).toBe(false);
  expect(p.canStart).toBe(true);
  expect(p.autoStart).toMatchObject({ how: "deliver", by: { name: "Ana" } });
  expect(byKey(key(meter)).blocks.map((b) => b.key)).toEqual([key(page)]);
  expect(byKey(key(sync)).merged).toBe(true);
  expect(byKey(key(sync)).blocks).toEqual([]);
  // Done with nothing merged still blocks, across projects, said so.
  const w = byKey(`WEB-${web.replace("wi_rel_", "")}`);
  expect(w.blockedBy).toEqual([{ key: key(spike), title: expect.any(String), status: "done", project: "Control plane", unmerged: true }]);
  expect(byKey(key(spike)).merged).toBe(false);
  expect(byKey(key(spike)).blocks.length).toBe(1);
});

test("navigation's merged is done with a merged pull request: neither alone", async () => {
  const both = await task(ana, "done");
  await merged(both);
  const prOnly = await task(ana, "review");
  await owner`INSERT INTO pull_requests (id, organization_id, project_id, task_id, repository_id, number, url, head_branch,
      base_branch, head_sha, title, state)
    VALUES (${`pr_${prOnly}`}, ${ORG}, ${PROJECT}, ${prOnly}, 'repo_rel', ${Number(prOnly.replace("wi_rel_", "")) + 100}, 'u', 'h', 'main', 's', 't', 'merged')`;
  const doneOnly = await task(ana, "done");
  const tree = (await (await call(bo, "GET", "/v1/navigation")).json()) as { projects: Array<{ tasks: NavTaskOut[] }> };
  const all = tree.projects.flatMap((p) => p.tasks);
  const m = (id: string) => all.find((t) => t.key === key(id))!.merged;
  expect([m(both), m(prOnly), m(doneOnly)]).toEqual([true, false, false]);
});

test("removing the last blocker of a parked delivery signals it as the orchestrator's runtime does, and wakes it", async () => {
  const blocker = await task(ana, "review");
  const t = await task(ana, "queued");
  await blocks(blocker, t);
  const wf = `wfr_parked_${t}`;
  await owner`INSERT INTO workflow_runs (id, organization_id, workflow_type, idempotency_key, status, step, state, task_id, awaiting_signals, wake_at)
    VALUES (${wf}, ${ORG}, 'delivery', ${`delivery:${t}`}, 'waiting', 'implementRun', '{}'::jsonb, ${t}, '["relations.unblocked"]'::jsonb, now() + interval '5 minutes')`;
  // A delivery parked on something else is left alone.
  const other = await task(ana, "queued");
  await blocks(blocker, other);
  const wfOther = `wfr_other_${other}`;
  await owner`INSERT INTO workflow_runs (id, organization_id, workflow_type, idempotency_key, status, step, state, task_id, awaiting_signals)
    VALUES (${wfOther}, ${ORG}, 'delivery', ${`delivery:${other}`}, 'waiting', 'implementRun', '{}'::jsonb, ${other}, '["phase.finished"]'::jsonb)`;
  expect((await call(ana, "DELETE", `/v1/tasks/${t}/relations/blocked_by/${blocker}`)).status).toBe(204);
  expect((await call(ana, "DELETE", `/v1/tasks/${other}/relations/blocked_by/${blocker}`)).status).toBe(204);
  // The row workflow.SignalTx writes: a sig_ id, the run, the name, the payload, no idempotency key.
  const signals = await owner`SELECT id, organization_id AS org, workflow_run_id AS run, name, payload, idempotency_key AS idem
    FROM workflow_signals WHERE workflow_run_id IN (${wf}, ${wfOther})`;
  expect(signals.length).toBe(1);
  expect(signals[0]).toMatchObject({ org: ORG, run: wf, name: "relations.unblocked", payload: { removed: true }, idem: null });
  expect(String(signals[0].id)).toMatch(/^sig_[0-9a-z]{9}[0-9a-f]{16}$/);
  const runs = await owner`SELECT id, status, wake_at FROM workflow_runs WHERE id IN (${wf}, ${wfOther}) ORDER BY id`;
  const byId = new Map(runs.map((r: { id: string; status: string; wake_at: Date | null }) => [r.id, r]));
  expect(byId.get(wf)).toMatchObject({ status: "running", wake_at: null });
  expect(byId.get(wfOther)).toMatchObject({ status: "waiting" });
});
