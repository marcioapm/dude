/**
 * Editing a task whose delivery stopped: what it asks for may change before
 * it is picked back up, where it works may not, and a save that leaves the
 * words as they were records no change to them — a resumed agent is told
 * its task changed only when it did.
 */

import { afterAll, beforeAll, expect, test } from "bun:test";
import { SQL } from "bun";
import { createApiKey, type Principal } from "../src/api/auth.ts";
import { Router } from "../src/api/router.ts";
import { registerStructureRoutes } from "../src/api/routes/structure.ts";
import { closePool, setPool } from "../src/db/client.ts";

const org = `org_stopped_${Bun.randomUUIDv7("hex").slice(-12)}`;
const project = `${org}_p`;
const task = `${org}_t`;
let owner: SQL;
let app: SQL;
let router: Router;

function call(method: string, path: string, body?: unknown) {
  return router.handle(new Request(`http://dude.test${path}`, {
    method, headers: { authorization: "key", "content-type": "application/json" },
    ...(body === undefined ? {} : { body: JSON.stringify(body) }),
  }));
}

beforeAll(async () => {
  owner = new SQL(process.env.DATABASE_URL!);
  const url = new URL(process.env.DATABASE_URL!);
  url.username = "dude_app";
  url.password = "dude_app";
  app = new SQL(url.toString());
  setPool(app);
  await owner`INSERT INTO organizations (id, name, slug) VALUES (${org}, ${org}, ${org})`;
  const made = await createApiKey({ organizationId: org, name: "Owner" });
  await owner`UPDATE people SET role = 'admin' WHERE id = ${made.personId}`;
  const principal: Principal = { credentialKind: "api_key", apiKeyId: made.id, personId: made.personId,
    organizationId: org, kind: "user", role: "admin", name: "Owner" };
  router = new Router(async (credential) => (credential === "key" ? principal : null));
  registerStructureRoutes(router);
  await owner`INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES (${project}, ${org}, 'P', ${project}, 'P')`;
  await owner`INSERT INTO tasks (id, organization_id, project_id, number, title, goal, acceptance_criteria, status)
    VALUES (${task}, ${org}, ${project}, 1, 'Greet', 'Say hello', '["it greets"]'::jsonb, 'aborted')`;
  await owner`INSERT INTO workflow_runs (id, organization_id, workflow_type, idempotency_key, status, step, task_id)
    VALUES (${`${org}_wf`}, ${org}, 'task.delivery', ${`delivery:${task}`}, 'aborted', 'awaitImplement', ${task})`;
});

afterAll(async () => {
  await closePool(app);
  await owner`DELETE FROM organizations WHERE id = ${org}`;
  await owner.end();
});

const edits = async () =>
  (await owner`SELECT payload FROM events WHERE task_id = ${task} AND event_type = 'task.updated' ORDER BY cursor`) as Array<{ payload: Record<string, unknown> }>;

test("a stopped task's goal can change", async () => {
  const res = await call("PATCH", `/v1/tasks/${task}`, { title: "Greet", goal: "Say goodbye", acceptanceCriteria: ["it greets"] });
  expect(res.status).toBe(200);
  const [row] = (await owner`SELECT goal FROM tasks WHERE id = ${task}`) as Array<{ goal: string }>;
  expect(row?.goal).toBe("Say goodbye");
  // Only what changed is recorded.
  expect((await edits()).at(-1)?.payload).toEqual({ goal: "Say goodbye" });
});

test("a save that changes nothing it asks for records no change to it", async () => {
  const before = (await edits()).length;
  const res = await call("PATCH", `/v1/tasks/${task}`, { title: "Greet", goal: "Say goodbye", acceptanceCriteria: ["it greets"] });
  expect(res.status).toBe(200);
  expect((await edits()).length).toBe(before);
});

test("a stopped task's repositories cannot change", async () => {
  const res = await call("PATCH", `/v1/tasks/${task}`, { repositories: [] });
  expect(res.status).toBe(409);
});

test("a running task's goal cannot change", async () => {
  await owner`UPDATE tasks SET status = 'running' WHERE id = ${task}`;
  const res = await call("PATCH", `/v1/tasks/${task}`, { goal: "Say something else" });
  expect(res.status).toBe(409);
});
