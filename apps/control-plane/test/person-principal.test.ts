import { afterAll, beforeAll, expect, test } from "bun:test";
import { SQL } from "bun";
import { authenticate, auditActor, createApiKey, personPrincipal } from "../src/api/auth.ts";
import { Router } from "../src/api/router.ts";
import { registerWorkRoutes } from "../src/api/routes/work.ts";
import { closePool, setPool } from "../src/db/client.ts";

const org = `org_identity_${Bun.randomUUIDv7("hex").slice(-12)}`;
const person = `${org}_person`;
const creator = `${org}_creator`;
const project = `${org}_project`;
let owner: SQL;
let app: SQL;
beforeAll(async () => {
  owner = new SQL(process.env.DATABASE_URL!);
  const url = new URL(process.env.DATABASE_URL!);
  url.username = "dude_app"; url.password = "dude_app";
  app = new SQL(url.toString()); setPool(app);
  await owner`INSERT INTO organizations (id, name, slug) VALUES (${org}, ${org}, ${org})`;
  await owner`INSERT INTO people (id, organization_id, name, role) VALUES (${person}, ${org}, 'Person', 'admin')`;
  await owner`INSERT INTO people (id, organization_id, name, role) VALUES (${creator}, ${org}, 'Creator', 'member')`;
  await owner`INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES (${project}, ${org}, 'Keyless', 'keyless', 'KL')`;
});
afterAll(async () => {
  await closePool(app);
  await owner`DELETE FROM organizations WHERE id = ${org}`;
  await owner.end();
});
test("person auth dependency rereads current role and removal without creating keys", async () => {
  const identity = await personPrincipal(org, person);
  expect(identity).not.toBeNull();
  expect(identity).not.toHaveProperty("apiKeyId");
  expect(auditActor(identity!)).toEqual({ kind: "person", id: person });
  expect(await personPrincipal(`${org}_foreign`, person)).toBeNull();
  const router = new Router(async () => identity);
  router.get("/identity", ctx => Response.json(ctx.principal));
  await owner`UPDATE people SET role = 'member' WHERE id = ${person}`;
  const response = await router.handle(new Request("http://dude.test/identity"));
  expect((await response.json() as { role: string }).role).toBe("member");
  expect(await owner`SELECT id FROM api_keys WHERE person_id = ${person}`).toHaveLength(0);
  const production = new Router().get("/identity", () => Response.json({}));
  expect((await production.handle(new Request("http://dude.test/identity", { headers: { "x-dude-person": person, "x-dude-organization": org } }))).status).toBe(401);
  await owner`UPDATE people SET removed_at = now() WHERE id = ${person}`;
  expect((await router.handle(new Request("http://dude.test/identity"))).status).toBe(401);
});
test("real API key authentication keeps its actual credential and audit actor", async () => {
  const made = await createApiKey({ organizationId: org, name: "Key person" });
  const principal = await authenticate(`Bearer ${made.key}`);
  expect(principal?.credentialKind).toBe("api_key");
  expect(auditActor(principal!)).toEqual({ kind: "human", id: made.id });
  await owner`UPDATE api_keys SET revoked_at = now() WHERE id = ${made.id}`;
  expect(await authenticate(`Bearer ${made.key}`)).toBeNull();
});
test("a person with no key creates a task that is theirs alone, or nothing on bad input", async () => {
  const principal = await personPrincipal(org, creator);
  const router = new Router(async () => principal);
  registerWorkRoutes(router);
  const create = (body: Record<string, unknown>) => router.handle(new Request("http://dude.test/v1/tasks", {
    method: "POST", headers: { "content-type": "application/json" },
    body: JSON.stringify({ projectId: project, goal: "Work a person asked for without an API key.", ...body }),
  }));

  const res = await create({ title: "Keyless task" });
  expect(res.status).toBe(201);
  const task = await res.json() as { id: string; owner: { id: string } | null };
  expect(task.owner?.id).toBe(creator);
  expect(Array.from(await owner`SELECT person_id, position FROM task_people WHERE task_id = ${task.id}`))
    .toEqual([{ person_id: creator, position: 0 }]);
  expect(Array.from(await owner`SELECT actor_type, actor_id FROM events WHERE task_id = ${task.id} AND event_type = 'task.created'`))
    .toEqual([{ actor_type: "person", actor_id: creator }]);
  expect(await owner`SELECT id FROM api_keys WHERE person_id = ${creator}`).toHaveLength(0);

  const numberBefore = await owner`SELECT next_task_number FROM projects WHERE id = ${project}`;
  expect((await create({ title: "Bad repo", repositories: [{ id: `${org}_nowhere` }] })).status).toBe(404);
  expect(Array.from(await owner`SELECT id FROM tasks WHERE project_id = ${project}`)).toEqual([{ id: task.id }]);
  expect(Array.from(await owner`SELECT next_task_number FROM projects WHERE id = ${project}`)).toEqual(numberBefore);
  expect(Array.from(await owner`SELECT task_id FROM task_people WHERE organization_id = ${org}`)).toEqual([{ task_id: task.id }]);
});
