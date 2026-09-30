import { afterAll, beforeAll, expect, test } from "bun:test";
import { SQL } from "bun";
import { join } from "node:path";
import { closePool, setPool } from "../src/db/client.ts";
import { buildRouter } from "../src/index.ts";
import type { Router } from "../src/api/router.ts";
import { createApiKey } from "../src/api/auth.ts";

const OWNER_URL = process.env.DATABASE_URL ?? "postgres://dude:dude@localhost:5433/dude";
const NAME = `dude_models_test_${Bun.randomUUIDv7("hex").slice(-12)}`;
const ORG = "org_models";
const PROJECT = "prj_models";
let admin: SQL;
let owner: SQL;
let app: SQL;
let router: Router;
let key: string;
let server: ReturnType<typeof Bun.serve>;
const oldUrl = process.env.DUDE_ORCHESTRATOR_URL;
const oldToken = process.env.DUDE_ORCHESTRATOR_TOKEN;

function databaseUrl(appRole = false) {
  const url = new URL(OWNER_URL);
  url.pathname = `/${NAME}`;
  if (appRole) {
    url.username = "dude_app";
    url.password = "dude_app";
  }
  return url.toString();
}

// Responses are asserted below rather than trusted as domain objects.
const body = async (res: Response): Promise<any> => res.json();

function call(method: string, path: string, body?: unknown) {
  return router.handle(new Request(`http://dude.test${path}`, {
    method,
    headers: { authorization: `Bearer ${key}`, "content-type": "application/json" },
    ...(body === undefined ? {} : { body: JSON.stringify(body) }),
  }));
}

beforeAll(async () => {
  admin = new SQL(OWNER_URL);
  await admin.unsafe(`CREATE DATABASE "${NAME}"`);
  const migrate = Bun.spawnSync(["bun", "run", "apps/control-plane/src/db/migrate.ts"], {
    cwd: join(import.meta.dir, "../../.."),
    env: { ...process.env, DATABASE_URL: databaseUrl() },
  });
  if (migrate.exitCode !== 0) throw new Error(migrate.stderr.toString());
  owner = new SQL(databaseUrl());
  await owner`INSERT INTO organizations (id, name, slug) VALUES (${ORG}, 'Models', 'models')`;
  await owner`INSERT INTO projects (id, organization_id, name, slug, key_prefix)
              VALUES (${PROJECT}, ${ORG}, 'Models', 'models', 'MOD')`;
  app = new SQL(databaseUrl(true));
  setPool(app);
  key = (await createApiKey({ organizationId: ORG, name: "Admin" })).key;
  server = Bun.serve({ port: 0, fetch(req) {
    return Response.json(new URL(req.url).pathname.endsWith("builtin") ? {} : {
      requiredReviewers: ["correctness"], blockingSeverities: ["blocking"], maxReviewIterations: 3,
      maxAttemptsPerFinding: 2, maxPrFixIterations: 3, simplify: true, test: false,
      parkAfterMinutes: 10, idleNudgeMinutes: 0,
    });
  } });
  process.env.DUDE_ORCHESTRATOR_URL = `http://localhost:${server.port}`;
  process.env.DUDE_ORCHESTRATOR_TOKEN = "models-test";
  router = buildRouter("");
});

afterAll(async () => {
  await server?.stop(true);
  if (oldUrl === undefined) delete process.env.DUDE_ORCHESTRATOR_URL;
  else process.env.DUDE_ORCHESTRATOR_URL = oldUrl;
  if (oldToken === undefined) delete process.env.DUDE_ORCHESTRATOR_TOKEN;
  else process.env.DUDE_ORCHESTRATOR_TOKEN = oldToken;
  if (app) await closePool(app);
  await owner?.end();
  await admin?.unsafe(`DROP DATABASE IF EXISTS "${NAME}" WITH (FORCE)`);
  await admin?.end();
});

const paths = ["/v1/settings/organization", `/v1/projects/${PROJECT}/settings`];
const invalid = ["anthropic/claude", "openai/gpt", "llmproxy/model", "model", "llm-anthropic/", "llm-openai/",
  "llm-openai/a/b", "llm-anthropic/a b", " llm-openai/gpt", "llm-openai/gpt\n", "fake/unknown", "", "llm-openai/" + "x".repeat(190)];
const valid = ["llm-anthropic/claude-sonnet-5", "llm-openai/new-model:latest", "llm-openai/" + "x".repeat(189),
  "fake/scripted", "fake/hang", "fake/tools", "fake/request", "fake/wait", "fake/live", "fake/ask"];

for (const path of paths) {
  test(`${path} rejects invalid model writes atomically with an actionable 400`, async () => {
    const before = await (await call("GET", path)).json();
    const [countBefore] = await owner`SELECT count(*)::int AS n FROM events WHERE event_type = 'settings.updated'`;
    for (const model of invalid) {
      const res = await call("PATCH", path, { roles: { implementer: { model }, reviewer: { effort: "high" } } });
      expect(res.status).toBe(400);
      const error = (await body(res)).error;
      expect(error.code).toBe("bad_request");
      expect(error.message).toContain("roles.implementer.model");
      expect(error.message).toContain("llm-anthropic/<model>");
      expect(error.message).toContain("llm-openai/<model>");
    }
    expect(await (await call("GET", path)).json()).toEqual(before);
    const [countAfter] = await owner`SELECT count(*)::int AS n FROM events WHERE event_type = 'settings.updated'`;
    expect(countAfter.n).toBe(countBefore.n);
  });

  test(`${path} accepts providers and explicit harness models, then resets`, async () => {
    for (const model of valid) {
      const res = await call("PATCH", path, { roles: { implementer: { model } } });
      expect(res.status).toBe(200);
      expect((await body(res)).roles.implementer.model.value).toBe(model);
    }
    const reset = await call("PATCH", path, { roles: { implementer: { model: null } } });
    expect(reset.status).toBe(200);
    expect((await body(reset)).roles.implementer.model.value).toBeNull();
  });
}

test("project update rejects invalid models without changing other fields", async () => {
  const before = await body(await call("GET", `/v1/projects/${PROJECT}`));
  for (const model of invalid) {
    const update = await call("PATCH", `/v1/projects/${PROJECT}`, { name: "Bad", agentModels: { investigator: { model } } });
    expect(update.status).toBe(400);
    expect((await body(update)).error.message).toContain("llm-anthropic/<model>");
  }
  expect(await body(await call("GET", `/v1/projects/${PROJECT}`))).toEqual(before);
});

test("project create and update use hierarchy validation, including non-settings roles", async () => {
  for (const model of invalid) {
    const create = await call("POST", "/v1/projects", { name: "Bad", slug: "bad-model", agentModels: { orchestrator: { model } } });
    expect(create.status).toBe(400);
    expect((await body(create)).error.message).toContain("llm-openai/<model>");
    const update = await call("PATCH", `/v1/projects/${PROJECT}`, { name: "Bad", agentModels: { investigator: { model } } });
    expect(update.status).toBe(400);
    expect((await body(update)).error.message).toContain("llm-anthropic/<model>");
  }
  const [absent] = await owner`SELECT count(*)::int AS n FROM projects WHERE slug = 'bad-model'`;
  expect(absent.n).toBe(0);
  expect((await body(await call("GET", `/v1/projects/${PROJECT}`))).name).toBe("Models");
  for (const [i, model] of valid.entries()) {
    const models = { orchestrator: { model }, investigator: { effort: "high" }, fixer: { model } };
    const create = await call("POST", "/v1/projects", { name: "Good", slug: `good-model-${i}`, agentModels: models });
    expect(create.status).toBe(201);
    expect((await body(create)).agentModels).toEqual(models);
    const update = await call("PATCH", `/v1/projects/${PROJECT}`, { agentModels: models });
    expect(update.status).toBe(200);
    expect((await body(update)).agentModels).toEqual(models);
  }
});

test("legacy invalid stored strings read safely, allow unrelated patches, correction and reset", async () => {
  const legacy = { implementer: { model: "old-provider/old-model" } };
  await owner`UPDATE organizations SET default_agent_models = ${legacy}::jsonb WHERE id = ${ORG}`;
  await owner`UPDATE projects SET agent_models = ${legacy}::jsonb WHERE id = ${PROJECT}`;
  const projectRead = await call("GET", `/v1/projects/${PROJECT}`);
  expect(projectRead.status).toBe(200);
  expect((await body(projectRead)).agentModels).toEqual(legacy);
  const renamed = await call("PATCH", `/v1/projects/${PROJECT}`, { name: "Renamed" });
  expect(renamed.status).toBe(200);
  expect((await body(renamed)).agentModels).toEqual(legacy);
  for (const path of paths) {
    const read = await call("GET", path);
    expect(read.status).toBe(200);
    expect((await body(read)).roles.implementer.model.value).toBe("old-provider/old-model");
    const patch = await call("PATCH", path, { roles: { implementer: { effort: "low" } } });
    expect(patch.status).toBe(200);
    expect((await body(patch)).roles.implementer.model.value).toBe("old-provider/old-model");
    const corrected = await call("PATCH", path, { roles: { implementer: { model: "llm-openai/gpt" } } });
    expect(corrected.status).toBe(200);
    expect((await body(corrected)).roles.implementer.model.value).toBe("llm-openai/gpt");
    expect((await body(await call("GET", path))).roles.implementer.model.value).toBe("llm-openai/gpt");
    const reset = await call("PATCH", path, { roles: { implementer: { model: null } } });
    expect(reset.status).toBe(200);
    const resetModel = (await body(reset)).roles.implementer.model;
    expect(resetModel.value).not.toBe("llm-openai/gpt");
    expect((await body(await call("GET", path))).roles.implementer.model).toEqual(resetModel);
  }
  expect((await call("PATCH", `/v1/projects/${PROJECT}`, { name: "Renamed" })).status).toBe(200);
});
