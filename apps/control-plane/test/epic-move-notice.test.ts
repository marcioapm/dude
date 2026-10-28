/**
 * A task moved out of a chat-filed epic can leave the epic's current tasks
 * all ended: the move finishes the epic (chat_epic_notice, migration 221),
 * once, with the counts of the tasks still in it.
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
const NAME = `dude_epic_move_test_${Bun.randomUUIDv7("hex").slice(-12)}`;
const ORG = "org_epicmove";
const PROJECT = "prj_epicmove";
const CHAT_EPIC = "epc_chat";
const WEB_EPIC = "epc_web";
const ORIGIN = { provider: "slack", workspaceId: "T1", conversationId: "C1", conversationName: "#support", threadId: "1.0" };

function databaseUrl(user: string): string {
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
let ana: { key: string };

beforeAll(async () => {
  admin = new SQL(OWNER_URL);
  await admin.unsafe(`CREATE DATABASE "${NAME}"`);
  const migrate = Bun.spawnSync([process.execPath, "run", "apps/control-plane/src/db/migrate.ts"], {
    cwd: ROOT,
    env: { ...process.env, DATABASE_URL: databaseUrl("owner") },
  });
  if (migrate.exitCode !== 0) throw new Error(`migrate: ${migrate.stderr.toString()}`);
  owner = new SQL(databaseUrl("owner"));
  await owner`INSERT INTO organizations (id, name, slug) VALUES (${ORG}, 'Acme', 'acme')`;
  await owner`INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES (${PROJECT}, ${ORG}, 'Billing', 'bil', 'BIL')`;
  await owner`INSERT INTO epics (id, organization_id, project_id, title, origin) VALUES
    (${CHAT_EPIC}, ${ORG}, ${PROJECT}, 'CSV exports', ${ORIGIN}::jsonb),
    (${WEB_EPIC}, ${ORG}, ${PROJECT}, 'Elsewhere', NULL)`;
  app = new SQL(databaseUrl("app"));
  setPool(app);
  ana = await createApiKey({ organizationId: ORG, name: "Ana" });
  router = buildRouter("");
});

afterAll(async () => {
  await closePool(app);
  await owner?.end();
  await admin?.unsafe(`DROP DATABASE IF EXISTS "${NAME}" WITH (FORCE)`);
  await admin?.end();
});

const move = (id: string, epicId: string | null) => router.handle(new Request(`http://dude.test/v1/tasks/${id}`, {
  method: "PATCH", headers: { authorization: `Bearer ${ana.key}`, "content-type": "application/json" }, body: JSON.stringify({ epicId }),
}));
let number = 0;
async function task(status: string, epic = CHAT_EPIC): Promise<string> {
  number += 1;
  const id = `wi_move_${number}`;
  await owner`INSERT INTO tasks (id, organization_id, project_id, number, epic_id, title, goal, status)
    VALUES (${id}, ${ORG}, ${PROJECT}, ${number}, ${epic}, ${`Task ${number}`}, 'A goal long enough to keep.', ${status}::task_status)`;
  return id;
}
const finished = async () => [...await owner`SELECT detail, trigger_task_id AS "triggerTaskId", thread_id AS "threadId"
  FROM chat_notices WHERE epic_id = ${CHAT_EPIC} AND kind = 'epic_finished'`];

test("moving an epic's last running task out finishes the epic, once, with what is left", async () => {
  await task("done");
  const running = await task("running");
  // Saved into the epic it is in: no move out, nothing finishes.
  expect((await move(running, CHAT_EPIC)).status).toBe(200);
  expect(await finished()).toEqual([]);
  expect((await move(running, WEB_EPIC)).status).toBe(200);
  const once = [{ detail: "1 done, 0 failed, 0 cancelled", triggerTaskId: running, threadId: "1.0" }];
  expect(await finished()).toEqual(once);
  // Moved back and out again, or another ended task out: said once ever.
  expect((await move(running, CHAT_EPIC)).status).toBe(200);
  const failed = await task("failed");
  expect((await move(running, null)).status).toBe(200);
  expect((await move(failed, null)).status).toBe(200);
  expect(await finished()).toEqual(once);
});

test("a task moved out of an epic made in dude records nothing", async () => {
  // An ended task stays, so only the epic's having no conversation stops the finish.
  await task("done", WEB_EPIC);
  const left = await task("running", WEB_EPIC);
  expect((await move(left, null)).status).toBe(200);
  expect([...await owner`SELECT count(*)::int AS n FROM chat_notices WHERE epic_id = ${WEB_EPIC}`]).toEqual([{ n: 0 }]);
});
