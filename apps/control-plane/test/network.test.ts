/**
 * The agent network through the public API: an organisation's list, a
 * project's additions or its own alone, what a Run started now gets, the
 * names lux refused a project's agents and the one-click Allow — who may
 * change what, what is refused, and that another organisation sees none of
 * it.
 *
 * Against a database of its own, migrated as a release is, and a stand-in
 * for the orchestrator that answers its defaults.
 *
 * Requires DATABASE_URL: a role that can create databases (the owner).
 * The tests run in order on one database and share its state.
 */

import { afterAll, beforeAll, describe, expect, setSystemTime, test } from "bun:test";
import { SQL } from "bun";
import { join } from "node:path";
import { promptRoleSchema } from "@dude/domain";
import { closePool, setPool } from "../src/db/client.ts";
import { buildRouter } from "../src/index.ts";
import { Config, useConfig } from "../src/config.ts";
import type { Router } from "../src/api/router.ts";
import { createApiKey } from "../src/api/auth.ts";
import { network, type Layers } from "../src/api/routes/settings.ts";

const OWNER_URL = process.env.DATABASE_URL ?? "postgres://dude:dude@localhost:5433/dude";
const ROOT = join(import.meta.dir, "../../..");
const NAME = `dude_network_test_${Bun.randomUUIDv7("hex").slice(-12)}`;
const ORG = "org_net";
const OTHER = "org_net_other";
const PROJECT = "prj_net";
const OTHER_PROJECT = "prj_net_other";

function databaseUrl(appRole = false): string {
  const url = new URL(OWNER_URL);
  if (appRole) {
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
let adminKey: string;
let memberKey: string;
let otherKey: string;
let orchestratorServer: ReturnType<typeof Bun.serve>;
/** What the orchestrator says sits under every list. */
const defaults = { operator: ["mirror.internal"], always: ["llm.example", "dude’s tools"], model: "llm.example" };
let answer = defaults;
let orchestratorDown = false;

type Json = any; // eslint-disable-line @typescript-eslint/no-explicit-any
const body = async (res: Response): Promise<Json> => res.json();

function call(key: string, method: string, path: string, payload?: unknown) {
  return router.handle(new Request(`http://dude.test${path}`, {
    method,
    headers: { authorization: `Bearer ${key}`, "content-type": "application/json" },
    ...(payload === undefined ? {} : { body: JSON.stringify(payload) }),
  }));
}

beforeAll(async () => {
  admin = new SQL(OWNER_URL);
  await admin.unsafe(`CREATE DATABASE "${NAME}"`);
  const migrate = Bun.spawnSync(["bun", "run", "apps/control-plane/src/db/migrate.ts"], {
    cwd: ROOT, env: { ...process.env, DATABASE_URL: databaseUrl() },
  });
  if (migrate.exitCode !== 0) throw new Error(`migrate: ${migrate.stderr.toString()}`);
  owner = new SQL(databaseUrl());
  for (const id of [ORG, OTHER]) await owner`INSERT INTO organizations (id, name, slug) VALUES (${id}, ${id === ORG ? "Acme" : id}, ${id})`;
  await owner`INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES (${PROJECT}, ${ORG}, 'jervasion', 'jervasion', 'JERV'),
    (${OTHER_PROJECT}, ${OTHER}, 'Other', 'other', 'OTH')`;
  await owner`INSERT INTO tasks (id, organization_id, project_id, number, title, goal) VALUES ('wi_net', ${ORG}, ${PROJECT}, 1, 'T', 'G'),
    ('wi_net_other', ${OTHER}, ${OTHER_PROJECT}, 1, 'T', 'G')`;
  await owner`INSERT INTO runs (id, organization_id, project_id, task_id, attempt) VALUES
    ('run_a', ${ORG}, ${PROJECT}, 'wi_net', 1), ('run_b', ${ORG}, ${PROJECT}, 'wi_net', 1),
    ('run_old', ${ORG}, ${PROJECT}, 'wi_net', 1), ('run_other', ${OTHER}, ${OTHER_PROJECT}, 'wi_net_other', 1)`;
  app = new SQL(databaseUrl(true));
  setPool(app);
  adminKey = (await createApiKey({ organizationId: ORG, name: "Ana" })).key;
  memberKey = (await createApiKey({ organizationId: ORG, name: "Bo" })).key;
  otherKey = (await createApiKey({ organizationId: OTHER, name: "Cy" })).key;
  orchestratorServer = Bun.serve({
    port: 0,
    fetch(req) {
      const path = new URL(req.url).pathname;
      if (path === "/internal/network/defaults") {
        return orchestratorDown ? new Response("restarting", { status: 503 }) : Response.json(answer);
      }
      if (path.endsWith("builtin")) return Response.json(Object.fromEntries(promptRoleSchema.options.map((r) => [r, "Built-in prompt"])));
      return Response.json({ requiredReviewers: ["correctness"], blockingSeverities: ["blocking"], maxReviewIterations: 3,
        maxAttemptsPerFinding: 2, maxPrFixIterations: 3, simplify: true, test: false, parkAfterMinutes: 10, idleNudgeMinutes: 0, conductorWarmMinutes: 5 });
    },
  });
  useConfig(Config.load({ env: { ...process.env,
    DUDE_ORCHESTRATOR_URL: `http://localhost:${orchestratorServer.port}`, DUDE_ORCHESTRATOR_TOKEN: "svc" } }));
  router = buildRouter("");
});

afterAll(async () => {
  useConfig(null);
  await orchestratorServer?.stop(true);
  if (app) await closePool(app);
  await owner?.end();
  await admin?.unsafe(`DROP DATABASE IF EXISTS "${NAME}" WITH (FORCE)`);
  await admin?.end();
});

const orgNetwork = async (key = adminKey) => (await body(await call(key, "GET", "/v1/settings/organization"))).network;
const projectNetwork = async (key = adminKey) => (await body(await call(key, "GET", `/v1/projects/${PROJECT}/settings`))).network;

describe("an organisation's list", () => {
  test("starts empty: a Run gets the operator's list, and the model and tools are always reachable", async () => {
    expect(await orgNetwork(memberKey)).toEqual({
      egress: { value: [], source: "organization" }, operator: ["mirror.internal"], always: ["llm.example", "dude’s tools"], effective: ["mirror.internal"],
    });
  });

  test("an admin sets it, lowercased and each entry once, with wildcards and ranges", async () => {
    const res = await call(adminKey, "PATCH", "/v1/settings/organization", { network: { egress: ["GitHub.com", "github.com", "*.github.com", "10.60.0.0/16"] } });
    expect(res.status).toBe(200);
    expect((await body(res)).network.egress.value).toEqual(["github.com", "*.github.com", "10.60.0.0/16"]);
    expect((await orgNetwork()).effective).toEqual(["mirror.internal", "github.com", "*.github.com", "10.60.0.0/16"]);
    const [stored] = await owner`SELECT agent_egress FROM organizations WHERE id = ${ORG}`;
    expect(stored.agent_egress).toEqual(["github.com", "*.github.com", "10.60.0.0/16"]);
  });

  test("an entry lux would refuse, a mode, or a member's change is refused", async () => {
    for (const entry of ["*.com", "a.*.example.com", "*.example.com:443", "not a host"]) {
      const res = await call(adminKey, "PATCH", "/v1/settings/organization", { network: { egress: ["pypi.org", entry] } });
      expect(res.status).toBe(400);
      expect(JSON.stringify(await body(res))).toContain(entry);
    }
    expect((await call(adminKey, "PATCH", "/v1/settings/organization", { network: { mode: "only" } })).status).toBe(400);
    expect((await call(adminKey, "PATCH", "/v1/settings/organization", { network: { egress: Array.from({ length: 201 }, (_, i) => `h${i}.example.com`) } })).status).toBe(400);
    const member = await call(memberKey, "PATCH", "/v1/settings/organization", { network: { egress: ["pypi.org"] } });
    expect(member.status).toBe(403);
    expect((await orgNetwork()).egress.value).toEqual(["github.com", "*.github.com", "10.60.0.0/16"]);
  });

  test("* is anywhere: everything a Run gets", async () => {
    await call(adminKey, "PATCH", "/v1/settings/organization", { network: { egress: ["github.com", "*"] } });
    expect((await orgNetwork()).effective).toEqual(["*"]);
    await call(adminKey, "PATCH", "/v1/settings/organization", { network: { egress: ["github.com", "*.github.com"] } });
  });
});

describe("a project's list", () => {
  test("follows its organisation's, adding nothing, until it adds its own", async () => {
    expect(await projectNetwork(memberKey)).toEqual({
      egress: { value: [], source: "organization" }, mode: { value: "add", source: "organization" },
      organizationEgress: ["github.com", "*.github.com"], operator: ["mirror.internal"], always: ["llm.example", "dude’s tools"],
      effective: ["mirror.internal", "github.com", "*.github.com"],
    });
    const res = await call(adminKey, "PATCH", `/v1/projects/${PROJECT}/settings`, { network: { egress: ["pypi.org", "github.com"] } });
    expect(res.status).toBe(200);
    const network = (await body(res)).network;
    expect(network.egress).toEqual({ value: ["pypi.org", "github.com"], source: "project" });
    expect(network.effective).toEqual(["mirror.internal", "github.com", "*.github.com", "pypi.org"]);
  });

  test("only its own list leaves the organisation's out, and Reset puts it back", async () => {
    const only = (await body(await call(adminKey, "PATCH", `/v1/projects/${PROJECT}/settings`, { network: { mode: "only" } }))).network;
    expect(only.mode).toEqual({ value: "only", source: "project" });
    expect(only.effective).toEqual(["mirror.internal", "pypi.org", "github.com"]);
    const reset = (await body(await call(adminKey, "PATCH", `/v1/projects/${PROJECT}/settings`, { network: { mode: "add", egress: [] } }))).network;
    expect(reset.mode).toEqual({ value: "add", source: "organization" });
    expect(reset.egress).toEqual({ value: [], source: "organization" });
    expect(reset.effective).toEqual(["mirror.internal", "github.com", "*.github.com"]);
    expect((await call(memberKey, "PATCH", `/v1/projects/${PROJECT}/settings`, { network: { mode: "only" } })).status).toBe(403);
  });
});

describe("names refused recently", () => {
  test("are each name the project's agents were refused in the window, in how many Runs and by whom, most Runs first", async () => {
    await owner`INSERT INTO agent_egress_refusals (run_id, name, organization_id, project_id, role, refused_at) VALUES
      ('run_a', 'files.pythonhosted.org', ${ORG}, ${PROJECT}, 'fixer', now() - interval '1 hour'),
      ('run_b', 'files.pythonhosted.org', ${ORG}, ${PROJECT}, 'implementer', now() - interval '2 hours'),
      ('run_a', 'registry.npmjs.org', ${ORG}, ${PROJECT}, 'reviewer', now() - interval '3 hours'),
      ('run_b', 'a.example.net', ${ORG}, ${PROJECT}, 'implementer', now() - interval '3 hours'),
      ('run_a', 'api.github.com', ${ORG}, ${PROJECT}, 'fixer', now()),
      ('run_old', 'old.example.com', ${ORG}, ${PROJECT}, 'fixer', now() - interval '8 days'),
      ('run_other', 'secret.other.example', ${OTHER}, ${OTHER_PROJECT}, 'fixer', now())`;
    const res = await call(memberKey, "GET", `/v1/projects/${PROJECT}/network/refused?days=7`);
    expect(res.status).toBe(200);
    const refused = (await body(res)).refused;
    // api.github.com is under *.github.com now: allowed since, not listed.
    expect(refused.map((r: Json) => [r.name, r.runs, r.roles])).toEqual([
      ["files.pythonhosted.org", 2, ["fixer", "implementer"]],
      ["a.example.net", 1, ["implementer"]],
      ["registry.npmjs.org", 1, ["reviewer"]],
    ]);
    expect(Date.parse(refused[0].lastAt)).toBeGreaterThan(0);
    const older = (await body(await call(memberKey, "GET", `/v1/projects/${PROJECT}/network/refused?days=30`))).refused;
    expect(older.map((r: Json) => r.name)).toContain("old.example.com");
    expect((await call(memberKey, "GET", `/v1/projects/${PROJECT}/network/refused?days=0`)).status).toBe(400);
  });

  test("Allow appends them to the project's list, and they are no longer refused", async () => {
    expect((await call(memberKey, "POST", `/v1/projects/${PROJECT}/network/allow`, { names: ["registry.npmjs.org"] })).status).toBe(403);
    expect((await call(adminKey, "POST", `/v1/projects/${PROJECT}/network/allow`, { names: ["*.com"] })).status).toBe(400);
    const res = await call(adminKey, "POST", `/v1/projects/${PROJECT}/network/allow`, { names: ["Registry.npmjs.org", "files.pythonhosted.org", "a.example.net"] });
    expect(res.status).toBe(200);
    expect((await body(res)).network.egress.value).toEqual(["registry.npmjs.org", "files.pythonhosted.org", "a.example.net"]);
    // Allowed again: listed once.
    await call(adminKey, "POST", `/v1/projects/${PROJECT}/network/allow`, { names: ["registry.npmjs.org"] });
    expect((await projectNetwork()).egress.value).toEqual(["registry.npmjs.org", "files.pythonhosted.org", "a.example.net"]);
    expect((await body(await call(memberKey, "GET", `/v1/projects/${PROJECT}/network/refused`))).refused).toEqual([]);
  });

  test("Allow that would take the project's list past 200 hosts is refused, and changes nothing", async () => {
    const [{ agent_egress: before }] = await owner`SELECT agent_egress FROM projects WHERE id = ${PROJECT}`;
    const full = Array.from({ length: 199 }, (_, i) => `h${i}.example.com`);
    await owner`UPDATE projects SET agent_egress = ${owner.array(full, "text")}::text[] WHERE id = ${PROJECT}`;
    try {
      const res = await call(adminKey, "POST", `/v1/projects/${PROJECT}/network/allow`, { names: ["a.example.com", "b.example.com"] });
      expect(res.status).toBe(400);
      const [{ agent_egress: after }] = await owner`SELECT agent_egress FROM projects WHERE id = ${PROJECT}`;
      expect(after).toEqual(full);
      // Exactly 200 is not past it.
      expect((await call(adminKey, "POST", `/v1/projects/${PROJECT}/network/allow`, { names: ["a.example.com"] })).status).toBe(200);
    } finally {
      await owner`UPDATE projects SET agent_egress = ${owner.array(before, "text")}::text[] WHERE id = ${PROJECT}`;
    }
  });

  test("another organisation reads none of them, nor the project", async () => {
    expect((await call(otherKey, "GET", `/v1/projects/${PROJECT}/network/refused`)).status).toBe(404);
    expect((await call(otherKey, "POST", `/v1/projects/${PROJECT}/network/allow`, { names: ["x.example.com"] })).status).toBe(404);
    // Its own project's are its own; under row-level security the app role sees no other organisation's rows at all.
    const own = (await body(await call(otherKey, "GET", `/v1/projects/${OTHER_PROJECT}/network/refused`))).refused;
    expect(own.map((r: Json) => r.name)).toEqual(["secret.other.example"]);
    const leaked = await app.begin(async (tx) => {
      await tx`SELECT set_config('app.organization_id', ${OTHER}, true)`;
      return tx`SELECT name FROM agent_egress_refusals WHERE organization_id = ${ORG}`;
    });
    expect(leaked.length).toBe(0);
  });

  test("are the 200 most refused, however many there were", async () => {
    await owner`INSERT INTO runs (id, organization_id, project_id, task_id, attempt) VALUES ('run_many', ${ORG}, ${PROJECT}, 'wi_net', 1)`;
    await owner`INSERT INTO agent_egress_refusals (run_id, name, organization_id, project_id, role)
      SELECT 'run_many', 'h' || lpad(i::text, 3, '0') || '.example.org', ${ORG}, ${PROJECT}, 'fixer' FROM generate_series(1, 600) i`;
    // Refused in two Runs, so first, and on the project's list since: never listed, and 200 others still are.
    const allowed = ["a1.example.net", "a2.example.net", "a3.example.net", "a4.example.net"];
    const [{ agent_egress: before }] = await owner`SELECT agent_egress FROM projects WHERE id = ${PROJECT}`;
    await owner`UPDATE projects SET agent_egress = ${owner.array([...before, ...allowed], "text")}::text[] WHERE id = ${PROJECT}`;
    await owner`INSERT INTO agent_egress_refusals (run_id, name, organization_id, project_id, role)
      SELECT r, n, ${ORG}, ${PROJECT}, 'fixer' FROM unnest(ARRAY['run_a', 'run_b']) r, unnest(${owner.array(allowed, "text")}::text[]) n`;
    try {
      const refused = (await body(await call(memberKey, "GET", `/v1/projects/${PROJECT}/network/refused`))).refused;
      expect(refused.map((r: Json) => r.name)).toEqual(Array.from({ length: 200 }, (_, i) => `h${String(i + 1).padStart(3, "0")}.example.org`));
    } finally {
      await owner`DELETE FROM runs WHERE id = 'run_many'`;
      await owner`DELETE FROM agent_egress_refusals WHERE name = ANY(${owner.array(allowed, "text")}::text[])`;
      await owner`UPDATE projects SET agent_egress = ${owner.array(before, "text")}::text[] WHERE id = ${PROJECT}`;
    }
  });
});

describe("the orchestrator's defaults", () => {
  test("are kept for a minute, then read again", async () => {
    const start = Date.now();
    await orgNetwork();
    answer = { ...defaults, operator: ["mirror2.internal"] };
    try {
      expect((await orgNetwork()).operator).toEqual(["mirror.internal"]);
      setSystemTime(new Date(start + 61_000));
      expect((await orgNetwork()).operator).toEqual(["mirror2.internal"]);
    } finally {
      // Read the original back, so the tests after this one see it.
      answer = defaults;
      setSystemTime(new Date(start + 122_000));
      await orgNetwork();
      setSystemTime();
    }
  });
});

describe("while the orchestrator is down", () => {
  test("settings and saves still answer, with the operator's list and what is always reachable as it last said", async () => {
    await orgNetwork();
    orchestratorDown = true;
    // Past the minute a read is kept: the next one fails, and the last serves.
    setSystemTime(new Date(Date.now() + 5 * 60_000));
    try {
      const res = await call(adminKey, "PATCH", `/v1/projects/${PROJECT}/settings`, { network: { egress: ["pypi.org"] } });
      expect(res.status).toBe(200);
      const network = (await body(res)).network;
      expect([network.operator, network.always]).toEqual([["mirror.internal"], ["llm.example", "dude’s tools"]]);
      expect((await call(memberKey, "GET", "/v1/settings/organization")).status).toBe(200);
    } finally {
      setSystemTime();
      orchestratorDown = false;
    }
  });
});

describe("what a Run gets", () => {
  const layers = (org: string[], project?: string[], mode: "add" | "only" = "add"): Layers => ({
    org: { id: ORG, name: "Acme", agentModels: {}, deliveryPolicy: {}, agentEgress: org },
    ...(project ? { project: { id: PROJECT, name: "p", agentModels: {}, deliveryPolicy: {}, agentEgress: project, agentEgressMode: mode } } : {}),
  });
  const none = { operator: [], always: ["dude’s tools"], model: null };

  test("is unrestricted with nothing listed anywhere and no model to restrict to", () => {
    expect(network(layers([], []), none).effective).toEqual(["*"]);
    // With a model, nothing listed is the model alone.
    expect(network(layers([], []), { ...none, model: "llm.example" }).effective).toEqual([]);
    expect(network(layers([], ["pypi.org"]), none).effective).toEqual(["pypi.org"]);
  });

  test("is the operator's floor with 'only', and anywhere when the operator says so", () => {
    expect(network(layers(["github.com"], ["pypi.org"], "only"), { ...none, operator: ["mirror.internal"] }).effective).toEqual(["mirror.internal", "pypi.org"]);
    expect(network(layers(["github.com"], ["pypi.org"], "only"), { ...none, operator: ["*"] }).effective).toEqual(["*"]);
    // The organisation's * is left out with the rest of its list.
    expect(network(layers(["*"], ["pypi.org"], "only"), { ...none, model: "llm.example" }).effective).toEqual(["pypi.org"]);
  });
});
