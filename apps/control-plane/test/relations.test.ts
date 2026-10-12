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
