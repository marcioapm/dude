import { afterAll, beforeAll, expect, test } from "bun:test";
import { SQL } from "bun";
import { actorTypeSchema, type PromptHistory, type SettingsResponse } from "@dude/domain";
import { createApiKey, type Principal } from "../src/api/auth.ts";
import { Router } from "../src/api/router.ts";
import { registerProjectRoutes } from "../src/api/routes/projects.ts";
import { registerSettingsRoutes } from "../src/api/routes/settings.ts";
import { registerFindingRoutes } from "../src/api/routes/findings.ts";
import { closePool, setPool } from "../src/db/client.ts";
import { append, query } from "../src/events/ledger.ts";
import { seenEvent } from "../src/events/listen.ts";

const org = `org_attribution_${Bun.randomUUIDv7("hex").slice(-12)}`;
const other = `${org}_other`;
let owner: SQL;
let app: SQL;
let keyed: Extract<Principal, { credentialKind: "api_key" }>;
let person: Extract<Principal, { credentialKind: "person" }>;
let router: Router;
let defaults: ReturnType<typeof Bun.serve>;
const oldUrl = process.env.DUDE_ORCHESTRATOR_URL;
const oldToken = process.env.DUDE_ORCHESTRATOR_TOKEN;

async function call(who: "key" | "person", method: string, path: string, body?: unknown) {
  const response = await router.handle(new Request(`http://dude.test${path}`, {
    method, headers: { authorization: who, "content-type": "application/json" },
    ...(body === undefined ? {} : { body: JSON.stringify(body) }),
  }));
  expect(response.status).toBeLessThan(400);
  return response;
}

beforeAll(async () => {
  owner = new SQL(process.env.DATABASE_URL!);
  const url = new URL(process.env.DATABASE_URL!);
  url.username = "dude_app";
  url.password = "dude_app";
  app = new SQL(url.toString());
  setPool(app);
  for (const id of [org, other]) {
    await owner`INSERT INTO organizations (id, name, slug) VALUES (${id}, ${id}, ${id})`;
  }
  const made = await createApiKey({ organizationId: org, name: "Legacy author" });
  await owner`UPDATE people SET role = 'admin' WHERE id = ${made.personId}`;
  keyed = { credentialKind: "api_key", apiKeyId: made.id, personId: made.personId,
    organizationId: org, kind: "user", role: "admin", name: "Legacy author" };
  const personId = `${org}_person`;
  await owner`INSERT INTO people (id, organization_id, name, role) VALUES (${personId}, ${org}, 'Direct author', 'admin')`;
  person = { credentialKind: "person", personId, organizationId: org, kind: "user", role: "admin", name: "Direct author" };
  router = new Router(async credential => credential === "key" ? keyed : credential === "person" ? person : null);
  registerProjectRoutes(router);
  registerSettingsRoutes(router);
  registerFindingRoutes(router);
  defaults = Bun.serve({ port: 0, fetch(request) {
    return Response.json(new URL(request.url).pathname.endsWith("builtin") ? { implementer: "Built-in prompt" } : {});
  } });
  process.env.DUDE_ORCHESTRATOR_URL = `http://localhost:${defaults.port}`;
  process.env.DUDE_ORCHESTRATOR_TOKEN = "test";
});

afterAll(async () => {
  defaults?.stop(true);
  if (oldUrl === undefined) delete process.env.DUDE_ORCHESTRATOR_URL;
  else process.env.DUDE_ORCHESTRATOR_URL = oldUrl;
  if (oldToken === undefined) delete process.env.DUDE_ORCHESTRATOR_TOKEN;
  else process.env.DUDE_ORCHESTRATOR_TOKEN = oldToken;
  await closePool(app);
  await owner`DELETE FROM organizations WHERE id IN (${org}, ${other})`;
  await owner.end();
});

test("legacy key and direct person audits resolve without inventing a key", async () => {
  for (const who of ["key", "person"] as const) {
    const principal = who === "key" ? keyed : person;
    const project = await (await call(who, "POST", "/v1/projects", { name: who, slug: who })).json() as { id: string };
    await call(who, "DELETE", `/v1/projects/${project.id}/image`);
    await call(who, "PATCH", `/v1/projects/${project.id}/settings`, { roles: { implementer: { model: "test/model" } } });
    await call(who, "PATCH", "/v1/settings/organization", { roles: { implementer: { model: "test/model" } } });
    const task = `${project.id}_task`;
    const finding = `${project.id}_finding`;
    await owner`INSERT INTO tasks (id, organization_id, project_id, number, title) VALUES (${task}, ${org}, ${project.id}, 1, 'Task')`;
    await owner`INSERT INTO review_findings (id, organization_id, task_id, category, severity, title)
      VALUES (${finding}, ${org}, ${task}, 'correctness', 'high', 'Finding')`;
    await call(who, "POST", `/v1/findings/${finding}/resolve`, { status: "accepted", note: "Considered" });
    const rows = await owner`SELECT actor_type, actor_id FROM events WHERE organization_id = ${org}
      AND (project_id = ${project.id} OR task_id = ${task}) ORDER BY cursor`;
    expect(rows).toHaveLength(4);
    for (const row of rows) expect(row).toEqual({ actor_type: who === "key" ? "human" : "person",
      actor_id: who === "key" ? keyed.apiKeyId : person.personId });
    const events = await query(org, { projectId: project.id });
    expect(events).toHaveLength(3);
    for (const event of events) {
      expect(actorTypeSchema.parse(event.actor.type)).toBe(who === "key" ? "human" : "person");
      expect(event.actor.id).toBe(principal.personId);
      expect(event.actor.name).toBe(principal.name);
      expect(event.actor.keyId).toBe(who === "key" ? keyed.apiKeyId : undefined);
    }
  }
  const settings = await owner`SELECT actor_type, actor_id FROM events WHERE organization_id = ${org}
    AND event_type = 'settings.updated' AND project_id IS NULL ORDER BY cursor`;
  expect(settings).toEqual([{ actor_type: "human", actor_id: keyed.apiKeyId }, { actor_type: "person", actor_id: person.personId }]);
  expect(await owner`SELECT id FROM api_keys WHERE person_id = ${person.personId}`).toHaveLength(0);
});

test("prompt save, history, current state and restore read key IDs and person IDs from created_by", async () => {
  await call("key", "POST", "/v1/prompts/implementer", { body: "Legacy prompt" });
  const state = await (await call("person", "POST", "/v1/prompts/implementer", { body: "Direct prompt" })).json() as SettingsResponse;
  expect(state.roles.implementer.prompt.organization.updatedBy).toEqual({ id: person.personId, name: person.name });
  const history = await (await call("person", "GET", "/v1/prompts/implementer/history")).json() as PromptHistory;
  const direct = history.versions.find(v => v.body === "Direct prompt")!;
  const legacy = history.versions.find(v => v.body === "Legacy prompt")!;
  expect(direct.createdBy).toEqual({ id: person.personId, name: person.name });
  expect(legacy.createdBy).toEqual({ id: keyed.personId, name: keyed.name });
  expect(history.versions.find(v => v.body === "Built-in prompt")!.createdBy).toBeNull();
  const stored = await owner`SELECT created_by FROM prompt_versions WHERE id IN (${direct.id}, ${legacy.id}) ORDER BY body`;
  expect(stored).toEqual([{ created_by: person.personId }, { created_by: keyed.apiKeyId }]);
  const restored = await (await call("person", "POST", `/v1/prompts/versions/${legacy.id}/restore`)).json() as SettingsResponse;
  expect(restored.roles.implementer.prompt.organization).toMatchObject({ body: "Legacy prompt", updatedBy: { id: person.personId, name: person.name } });
  const [row] = await owner`SELECT created_by, restored_from FROM prompt_versions WHERE id = ${restored.roles.implementer.prompt.organization.versionId}`;
  expect(row).toEqual({ created_by: person.personId, restored_from: legacy.id });
  const events = (await query(org)).filter(event => event.eventType === "prompt.saved");
  expect(events.map(e => [e.actor.type, e.actor.id, e.actor.keyId])).toEqual([
    ["human", keyed.personId, keyed.apiKeyId], ["person", person.personId, undefined], ["person", person.personId, undefined],
  ]);
});

test("readers retain unresolved identities and do not enrich foreign people", async () => {
  const foreign = `${org}_foreign`;
  await owner`INSERT INTO people (id, organization_id, name) VALUES (${foreign}, ${other}, 'Foreign')`;
  for (const actor of [{ type: "human", id: "missing-key" }, { type: "person", id: "missing-person" }, { type: "person", id: foreign }] as const) {
    const written = await append({ organizationId: org, eventType: "test.unresolved", actor, source: "control-plane" });
    expect(written.actor).toEqual(actor);
    const [read] = await query(org, { after: written.cursor - 1, limit: 1 });
    expect(read!.actor).toEqual(actor);
  }
  expect(await query(other)).toHaveLength(0);
  expect(seenEvent(org, { id: person.personId, name: person.name, photoUrl: null, online: true, where: null }).actor)
    .toMatchObject({ type: "person", id: person.personId });
});
