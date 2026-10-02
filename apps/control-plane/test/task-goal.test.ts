/**
 * A task is saved with a goal of at least TASK_GOAL_MIN characters, trimmed:
 * on create, and on an edit that sets the goal. Edits that leave the goal
 * alone still work on a task saved before the rule, and a started task asked
 * for a new goal still hears that delivery has started.
 */

import { afterAll, beforeAll, expect, test } from "bun:test";
import { SQL } from "bun";
import { personPrincipal } from "../src/api/auth.ts";
import { Router } from "../src/api/router.ts";
import { registerStructureRoutes } from "../src/api/routes/structure.ts";
import { registerWorkRoutes } from "../src/api/routes/work.ts";
import { closePool, setPool } from "../src/db/client.ts";

const org = `org_goalmin_${Bun.randomUUIDv7("hex").slice(-12)}`;
const person = `${org}_person`;
const project = `${org}_project`;
const epic = `${org}_epic`;
const TOO_SHORT = "a task needs a goal of at least 16 characters: why it matters and what should change";
let owner: SQL;
let app: SQL;
let router: Router;

beforeAll(async () => {
  owner = new SQL(process.env.DATABASE_URL!);
  const url = new URL(process.env.DATABASE_URL!);
  url.username = "dude_app"; url.password = "dude_app";
  app = new SQL(url.toString()); setPool(app);
  await owner`INSERT INTO organizations (id, name, slug) VALUES (${org}, ${org}, ${org})`;
  await owner`INSERT INTO people (id, organization_id, name, role) VALUES (${person}, ${org}, 'Person', 'admin')`;
  await owner`INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES (${project}, ${org}, 'Goals', 'goals', 'GL')`;
  await owner`INSERT INTO epics (id, organization_id, project_id, title) VALUES (${epic}, ${org}, ${project}, 'Elsewhere')`;
  const principal = await personPrincipal(org, person);
  router = new Router(async () => principal);
  registerWorkRoutes(router);
  registerStructureRoutes(router);
});
afterAll(async () => {
  await closePool(app);
  await owner`DELETE FROM organizations WHERE id = ${org}`;
  await owner.end();
});

const call = (method: string, path: string, body: Record<string, unknown>) => router.handle(new Request(`http://dude.test${path}`, {
  method, headers: { "content-type": "application/json" }, body: JSON.stringify(body),
}));
const create = (body: Record<string, unknown>) => call("POST", "/v1/tasks", { projectId: project, title: "A task", ...body });
const tasksNamed = async (title: string) => (await owner`SELECT goal FROM tasks WHERE project_id = ${project} AND title = ${title}`).length;

/** A task as one saved before the rule: straight into the table, with this goal. */
async function oldTask(id: string, goal: string, number: number): Promise<string> {
  await owner`INSERT INTO tasks (id, organization_id, project_id, number, title, goal) VALUES (${id}, ${org}, ${project}, ${number}, 'Old', ${goal})`;
  return id;
}

async function refusedForTheGoal(res: Response) {
  expect(res.status).toBe(400);
  const { error } = await res.json() as { error: { code: string; message: string; details: { fieldErrors: Record<string, string[]> } } };
  expect(error.code).toBe("bad_request");
  expect(error.message).toBe(TOO_SHORT);
  expect(error.details.fieldErrors.goal).toEqual([TOO_SHORT]);
}

test("creating a task with no goal, or one under 16 characters trimmed, is refused and saves nothing", async () => {
  await refusedForTheGoal(await create({ title: "No goal" }));
  await refusedForTheGoal(await create({ title: "Empty goal", goal: "" }));
  await refusedForTheGoal(await create({ title: "Fifteen", goal: "a".repeat(15) }));
  // 23 characters as sent, 15 inside the whitespace.
  await refusedForTheGoal(await create({ title: "Padded", goal: `  \n ${"a".repeat(15)}\t  ` }));
  // 7 emoji are 14 UTF-16 units, and one more character makes 15.
  await refusedForTheGoal(await create({ title: "Emoji", goal: `${"😀".repeat(7)}a` }));
  await refusedForTheGoal(await create({ title: "Blank", goal: " ".repeat(20) }));
  for (const title of ["No goal", "Empty goal", "Fifteen", "Padded", "Emoji", "Blank"]) expect(await tasksNamed(title)).toBe(0);
  expect(Array.from(await owner`SELECT next_task_number FROM projects WHERE id = ${project}`)).toEqual([{ next_task_number: 1 }]);
});

test("a goal of 16 characters is enough, and is saved as sent", async () => {
  const exactly = await create({ title: "Sixteen", goal: "a".repeat(16) });
  expect(exactly.status).toBe(201);
  const padded = `\n  ${"b".repeat(16)}  \n`;
  const res = await create({ title: "Sixteen padded", goal: padded });
  expect(res.status).toBe(201);
  expect(((await res.json()) as { goal: string }).goal).toBe(padded);
  expect((await create({ title: "Sixteen emoji", goal: "😀".repeat(8) })).status).toBe(201);
});

test("an edit that sets the goal under 16 characters is refused and changes nothing", async () => {
  const id = await oldTask(`${org}_edited`, "A goal long enough to keep.", 100);
  await refusedForTheGoal(await call("PATCH", `/v1/tasks/${id}`, { goal: "too short" }));
  await refusedForTheGoal(await call("PATCH", `/v1/tasks/${id}`, { goal: `   ${"a".repeat(15)}   `, epicId: epic }));
  expect(Array.from(await owner`SELECT goal, epic_id FROM tasks WHERE id = ${id}`))
    .toEqual([{ goal: "A goal long enough to keep.", epic_id: null }]);
  expect((await call("PATCH", `/v1/tasks/${id}`, { goal: "a".repeat(16) })).status).toBe(200);
});

test("a task saved with a short goal can still be moved, renamed and handed on without touching its goal", async () => {
  const id = await oldTask(`${org}_short`, "", 101);
  expect((await call("PATCH", `/v1/tasks/${id}`, { epicId: epic })).status).toBe(200);
  expect((await call("PATCH", `/v1/tasks/${id}`, { title: "Renamed" })).status).toBe(200);
  expect((await call("PATCH", `/v1/tasks/${id}`, { ownerId: person })).status).toBe(200);
  expect(Array.from(await owner`SELECT goal, epic_id, title FROM tasks WHERE id = ${id}`))
    .toEqual([{ goal: "", epic_id: epic, title: "Renamed" }]);
});

test("a started task asked for a short goal hears that delivery has started, not that the goal is short", async () => {
  const id = await oldTask(`${org}_started`, "", 102);
  await owner`INSERT INTO workflow_runs (id, organization_id, workflow_type, idempotency_key, step, task_id)
    VALUES (${`${org}_wf`}, ${org}, 'delivery', ${id}, 'implement', ${id})`;
  for (const goal of ["short", "a".repeat(16)]) {
    const res = await call("PATCH", `/v1/tasks/${id}`, { goal });
    expect(res.status).toBe(409);
    expect(((await res.json()) as { error: { message: string } }).error.message).toStartWith("delivery has started");
  }
  // Where it sits still moves.
  expect((await call("PATCH", `/v1/tasks/${id}`, { epicId: epic })).status).toBe(200);
});
