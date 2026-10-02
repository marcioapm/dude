import { afterAll, beforeAll, expect, test } from "bun:test";
import { SQL } from "bun";
import { join } from "node:path";
import { closePool, setPool } from "../src/db/client.ts";
import { buildRouter } from "../src/index.ts";
import type { Router } from "../src/api/router.ts";
import { createApiKey } from "../src/api/auth.ts";
import { promptRoleSchema, ROLE_MODEL_REMOVED } from "@dude/domain";

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
    // Every prompt role gets a body: the control plane caches this answer
    // for the process, and later suites in the same run save prompts on it.
    return Response.json(new URL(req.url).pathname.endsWith("builtin")
      ? Object.fromEntries(promptRoleSchema.options.map((role) => [role, "Built-in prompt"])) : {
      requiredReviewers: ["correctness"], blockingSeverities: ["blocking"], maxReviewIterations: 3,
      maxAttemptsPerFinding: 2, maxPrFixIterations: 3, simplify: true, test: false,
      parkAfterMinutes: 10, idleNudgeMinutes: 0, conductorWarmMinutes: 5,
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
// Every form a role's model ever took: none is taken now, a role names a tier.
const models = ["llm-anthropic/claude-sonnet-5", "claude-opus-5-5", "fake/scripted", ""];

for (const path of paths) {
  test(`${path} refuses a role's model atomically, saying to name a tier`, async () => {
    const before = await (await call("GET", path)).json();
    const [countBefore] = await owner`SELECT count(*)::int AS n FROM events WHERE event_type = 'settings.updated'`;
    for (const model of models) {
      const res = await call("PATCH", path, { roles: { implementer: { model }, reviewer: { effort: "high" } } });
      expect(res.status).toBe(400);
      const error = (await body(res)).error;
      expect(error.code).toBe("bad_request");
      expect(error.message).toBe(`request body failed validation: roles.implementer.model: ${ROLE_MODEL_REMOVED}`);
    }
    expect(await (await call("GET", path)).json()).toEqual(before);
    const [countAfter] = await owner`SELECT count(*)::int AS n FROM events WHERE event_type = 'settings.updated'`;
    expect(countAfter.n).toBe(countBefore.n);
  });

  test(`${path} takes one of the organization's tiers, then resets`, async () => {
    const tiers = (await body(await call("GET", "/v1/models/tiers"))).tiers;
    const fast = tiers.find((t: { name: string }) => t.name === "Fast");
    const res = await call("PATCH", path, { roles: { implementer: { tier: fast.id } } });
    expect(res.status).toBe(200);
    expect((await body(res)).roles.implementer.tier.value).toBe(fast.id);
    const reset = await call("PATCH", path, { roles: { implementer: { tier: null } } });
    expect(reset.status).toBe(200);
    // The organization's reset ran first: neither layer names a tier for it now.
    expect((await body(reset)).roles.implementer.tier.value).toBeNull();
  });
}

test("project create and update refuse a role's model without changing anything", async () => {
  const before = await body(await call("GET", `/v1/projects/${PROJECT}`));
  for (const model of models) {
    const update = await call("PATCH", `/v1/projects/${PROJECT}`, { name: "Bad", agentModels: { investigator: { model } } });
    expect(update.status).toBe(400);
    expect((await body(update)).error.message).toContain(ROLE_MODEL_REMOVED);
    const create = await call("POST", "/v1/projects", { name: "Bad", slug: "bad-model", agentModels: { conductor: { model } } });
    expect(create.status).toBe(400);
  }
  expect(await body(await call("GET", `/v1/projects/${PROJECT}`))).toEqual(before);
  const [absent] = await owner`SELECT count(*)::int AS n FROM projects WHERE slug = 'bad-model'`;
  expect(absent.n).toBe(0);
});

test("a stored model left over reads safely and is ignored: the role's tier is what it runs on", async () => {
  const legacy = { implementer: { model: "old-provider/old-model" } };
  await owner`UPDATE projects SET agent_models = ${legacy}::jsonb WHERE id = ${PROJECT}`;
  const read = await call("GET", `/v1/projects/${PROJECT}/settings`);
  expect(read.status).toBe(200);
  expect((await body(read)).roles.implementer.model).toBeUndefined();
  expect((await call("PATCH", `/v1/projects/${PROJECT}/settings`, { roles: { implementer: { effort: "low" } } })).status).toBe(200);
  expect((await call("PATCH", `/v1/projects/${PROJECT}`, { name: "Renamed" })).status).toBe(200);
});
