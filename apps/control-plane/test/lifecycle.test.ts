/**
 * Lifecycle through the public API (docs/design/tracking-work.md, Epic 1):
 * the backend's own start path (a bare Run, path 8) refused for an archived
 * or closed task whatever its blockers; the lifecycle routes forwarded to
 * the orchestrator, which owns their rules; archived work out of the
 * navigation tree, the counts and the task list unless asked for.
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
const NAME = `dude_lifecycle_test_${Bun.randomUUIDv7("hex").slice(-12)}`;
const ORG = "org_lc";
const PROJECT = "prj_lc";

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
let orchestrator: ReturnType<typeof Bun.serve>;
const forwarded: Array<{ method: string; path: string; body: unknown }> = [];

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
  await owner`INSERT INTO repositories (id, organization_id, project_id, name, url, default_branch)
    VALUES ('repo_lc', ${ORG}, ${PROJECT}, 'cp', 'https://github.com/acme/cp', 'main')`;
  app = new SQL(databaseUrl("app", NAME));
  setPool(app);
  ana = await createApiKey({ organizationId: ORG, name: "Ana" });
  orchestrator = Bun.serve({ port: 0, async fetch(request) {
    const url = new URL(request.url);
    forwarded.push({ method: request.method, path: url.pathname, body: await request.json().catch(() => null) });
    if (url.pathname.endsWith("/archive")) {
      return Response.json({ error: { code: "not_ended", message: "CP-1 hasn't ended: archiving it needs a resolution" } }, { status: 409 });
    }
    return Response.json({ ok: true }, { status: 200 });
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

async function call(method: string, path: string, body?: unknown): Promise<Response> {
  return router.handle(new Request(`http://dude.test${path}`, {
    method, headers: { authorization: `Bearer ${ana.key}`, "content-type": "application/json" },
    ...(body === undefined ? {} : { body: JSON.stringify(body) }),
  }));
}

let number = 0;
async function task(status = "received", epic: string | null = null): Promise<string> {
  number += 1;
  const id = `wi_lc_${number}`;
  await owner`INSERT INTO tasks (id, organization_id, project_id, number, title, goal, status, epic_id)
    VALUES (${id}, ${ORG}, ${PROJECT}, ${number}, ${`Task ${number}`}, 'A goal long enough to save.', ${status}::task_status, ${epic})`;
  await owner`INSERT INTO task_people (task_id, person_id, organization_id, position) VALUES (${id}, ${ana.personId}, ${ORG}, 0)`;
  return id;
}
const key = (id: string) => `CP-${id.replace("wi_lc_", "")}`;

async function blocks(from: string, to: string): Promise<void> {
  await owner`INSERT INTO task_relations (organization_id, from_task_id, to_task_id, kind, created_by_person)
    VALUES (${ORG}, ${from}, ${to}, 'blocks', ${ana.personId})`;
}

async function refused(id: string): Promise<{ status: number; code: string; message: string }> {
  const res = await call("POST", `/v1/tasks/${id}/runs`);
  const body = (await res.json()) as { error?: { code: string; message: string } };
  return { status: res.status, code: body.error?.code ?? "", message: body.error?.message ?? "" };
}

const refusals = async (id: string, reason: string) =>
  ((await owner`SELECT count(*)::int AS n FROM events WHERE task_id = ${id} AND event_type = 'task.start_refused'
    AND payload->>'path' = 'create_run' AND payload->>'reason' = ${reason}`)[0] as { n: number }).n;

test("the lifecycle routes forward the person's decision to the orchestrator, and answer as it answered", async () => {
  forwarded.length = 0;
  const t = await task();
  expect((await call("POST", `/v1/tasks/${t}/close`, { resolution: "duplicate", duplicateOf: "CP-9", removeRelations: false })).status).toBe(200);
  expect((await call("POST", `/v1/tasks/${t}/reopen`)).status).toBe(200);
  const archived = await call("POST", `/v1/tasks/${t}/archive`);
  expect(archived.status).toBe(409);
  expect(((await archived.json()) as { error: { code: string } }).error.code).toBe("not_ended");
  expect((await call("POST", `/v1/tasks/${t}/unarchive`)).status).toBe(200);
  expect((await call("POST", `/v1/epics/epc_x/archive`, { withEndedTasks: true })).status).toBe(409);
  expect((await call("POST", `/v1/epics/epc_x/unarchive`)).status).toBe(200);
  expect(forwarded.map((f) => `${f.method} ${f.path}`)).toEqual([
    `POST /internal/tasks/${t}/close`, `POST /internal/tasks/${t}/reopen`, `POST /internal/tasks/${t}/archive`,
    `POST /internal/tasks/${t}/unarchive`, "POST /internal/epics/epc_x/archive", "POST /internal/epics/epc_x/unarchive",
  ]);
  expect(forwarded[0]!.body).toEqual({ resolution: "duplicate", duplicateOf: "CP-9", removeRelations: false });
  expect(forwarded[4]!.body).toEqual({ withEndedTasks: true });
});

test("what Close could never accept is refused before the orchestrator is asked", async () => {
  forwarded.length = 0;
  const t = await task();
  for (const body of [{ resolution: "nothing_to_merge" }, { resolution: "obsolete" }, {}, { resolution: "wont_do", note: "x".repeat(2001) }]) {
    expect((await call("POST", `/v1/tasks/${t}/close`, body)).status).toBe(400);
  }
  expect((await call("POST", `/v1/tasks/${t}/archive`, { resolution: "nothing_to_merge" })).status).toBe(400);
  expect(forwarded).toEqual([]);
});

test("path 8: a closed task's bare Run is refused as closed before its blockers, and allowed once reopened", async () => {
  const blocker = await task();
  const t = await task();
  await blocks(blocker, t);
  await owner`UPDATE tasks SET status = 'aborted', resolution = 'cant_reproduce' WHERE id = ${t}`;
  expect(await refused(t)).toEqual({ status: 409, code: "closed", message: `${key(t)} was closed as Can't reproduce: reopen it to start it.` });
  expect(await refusals(t, "closed")).toBe(1);
  expect(((await owner`SELECT count(*)::int AS n FROM runs WHERE task_id = ${t}`)[0] as { n: number }).n).toBe(0);
  // Reopened, as the orchestrator leaves it: no resolution, received. Blocked again, as itself.
  await owner`UPDATE tasks SET status = 'received', resolution = NULL WHERE id = ${t}`;
  expect((await refused(t)).code).toBe("blocked");
  await owner`DELETE FROM task_relations WHERE to_task_id = ${t}`;
  expect((await call("POST", `/v1/tasks/${t}/runs`)).status).toBe(201);
});

test("path 8: an archived task's bare Run is refused as archived, and allowed once unarchived and reopened", async () => {
  const t = await task();
  await owner`UPDATE tasks SET status = 'aborted', resolution = 'wont_do', archived_at = now(), archived_by = ${ana.personId} WHERE id = ${t}`;
  expect(await refused(t)).toEqual({ status: 409, code: "archived", message: `${key(t)} is archived: unarchive it to start it.` });
  expect(await refusals(t, "archived")).toBe(1);
  await owner`UPDATE tasks SET archived_at = NULL, archived_by = NULL WHERE id = ${t}`;
  expect((await refused(t)).code).toBe("closed");
  await owner`UPDATE tasks SET status = 'received', resolution = NULL WHERE id = ${t}`;
  expect((await call("POST", `/v1/tasks/${t}/runs`)).status).toBe(201);
});
