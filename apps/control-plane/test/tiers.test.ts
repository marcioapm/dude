/**
 * Model tiers through the public API: who may change them, what is
 * refused (a name taken, a model the proxy would not take by that name), a
 * role naming a tier on two layers with Reset and the fixer following the
 * implementer, removing one in use — everything that named it moved in the
 * same transaction — the upgrade's notes, the proxy's models and a test
 * message passed on from the orchestrator.
 *
 * Against a database of its own, migrated as a release is, and a stand-in
 * for the orchestrator that answers the proxy's models and test messages as
 * the test sets them.
 *
 * Requires DATABASE_URL: a role that can create databases (the owner).
 *
 * The tests run in order on one database and share its state: a test picked
 * alone with -t may fail. A database per test would cost a migration each.
 */

import { afterAll, beforeAll, describe, expect, test } from "bun:test";
import { SQL } from "bun";
import { join } from "node:path";
import { promptRoleSchema } from "@dude/domain";
import { closePool, setPool } from "../src/db/client.ts";
import { buildRouter } from "../src/index.ts";
import { Config, useConfig } from "../src/config.ts";
import type { Router } from "../src/api/router.ts";
import { createApiKey } from "../src/api/auth.ts";

const OWNER_URL = process.env.DATABASE_URL ?? "postgres://dude:dude@localhost:5433/dude";
const ROOT = join(import.meta.dir, "../../..");
const NAME = `dude_tiers_api_test_${Bun.randomUUIDv7("hex").slice(-12)}`;
const ORG = "org_tiers";
const OTHER = "org_tiers_other";
const PROJECT = "prj_tiers";
/** The project as the API shows it where it names one. */
const DOCS_SITE = { id: PROJECT, name: "Docs site", imageUrl: null };

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
/** What the stand-in orchestrator was asked to test, in order. */
const tested: unknown[] = [];
/** How long the backend waits on the orchestrator: shortened, so a slow answer costs the tests little. */
const timeouts = { callMs: 200, testMessageMs: 1_000 };
/** How long the stand-in takes to answer a test of the model "slow", or GET /internal/push/key: between the two. */
const slowMs = 500;
/** How it answers GET /internal/llm/models. */
const listedModels = () => Response.json({ models: ["claude-opus-5-5", "gpt-5.6-sol"], source: "https://llm.example/v1", problem: null });
let modelsReply = listedModels;

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
  await owner`INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES (${PROJECT}, ${ORG}, 'Docs site', 'docs', 'DS')`;
  app = new SQL(databaseUrl(true));
  setPool(app);
  adminKey = (await createApiKey({ organizationId: ORG, name: "Ana" })).key;
  memberKey = (await createApiKey({ organizationId: ORG, name: "Bo" })).key;
  otherKey = (await createApiKey({ organizationId: OTHER, name: "Cy" })).key;
  orchestratorServer = Bun.serve({
    port: 0,
    async fetch(req) {
      const path = new URL(req.url).pathname;
      if (path === "/internal/llm/models") return modelsReply();
      if (path === "/internal/push/key") {
        await Bun.sleep(slowMs);
        return Response.json({ publicKey: "k" });
      }
      if (path === "/internal/llm/test") {
        const asked = await req.json() as { model: string; efforts: Array<string | null> };
        tested.push(asked);
        if (asked.model === "slow") await Bun.sleep(slowMs);
        const refused = asked.model === "nope";
        return Response.json({ model: asked.model, results: asked.efforts.map((effort) => refused
          ? { efforts: [effort], sent: effort, ok: false, latencyMs: 12, status: 404, error: "model nope is not served here" }
          : { efforts: [effort], sent: effort, ok: true, latencyMs: 800, status: 200, error: null }) });
      }
      if (path === "/internal/network/defaults") return Response.json({ operator: [], always: ["dude’s tools"], model: null });
      if (path.endsWith("builtin")) return Response.json(Object.fromEntries(promptRoleSchema.options.map((r) => [r, "Built-in prompt"])));
      return Response.json({ requiredReviewers: ["correctness"], blockingSeverities: ["blocking"], maxReviewIterations: 3,
        maxAttemptsPerFinding: 2, maxPrFixIterations: 3, simplify: true, test: false, parkAfterMinutes: 10, idleNudgeMinutes: 0 });
    },
  });
  useConfig(Config.load({ env: { ...process.env,
    DUDE_ORCHESTRATOR_URL: `http://localhost:${orchestratorServer.port}`, DUDE_ORCHESTRATOR_TOKEN: "svc" }, orchestratorTimeouts: timeouts }));
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

const tiers = async (key = adminKey) => (await body(await call(key, "GET", "/v1/models/tiers"))).tiers as Json[];
const byName = async (name: string) => (await tiers()).find((t) => t.name === name);
const orgModels = async () => (await owner`SELECT default_agent_models AS m FROM organizations WHERE id = ${ORG}`)[0].m;
const projectModels = async () => (await owner`SELECT agent_models AS m FROM projects WHERE id = ${PROJECT}`)[0].m;

describe("tiers", () => {
  test("an organization starts with Thinker, Coder and Fast, naming no model; everyone reads, only admins change", async () => {
    const res = await body(await call(memberKey, "GET", "/v1/models/tiers"));
    expect(res.canEdit).toBe(false);
    expect(res.upgrade).toEqual([]);
    expect(res.tiers.map((t: Json) => [t.name, t.model])).toEqual([["Thinker", null], ["Coder", null], ["Fast", null]]);
    const thinker = res.tiers[0];
    expect(thinker.usedBy.map((u: Json) => u.role).sort()).toEqual(["brainstorm", "conductor", "investigator", "qa_browser", "reviewer", "simplifier"]);
    expect(res.tiers[1].usedBy).toEqual([
      { kind: "organization", role: "implementer", project: null, effort: null },
      { kind: "organization", role: "fixer", project: null, inherited: true, effort: null },
    ]);
    expect((await call(memberKey, "POST", "/v1/models/tiers", { name: "Cheap" })).status).toBe(403);
    expect((await call(memberKey, "PUT", `/v1/models/tiers/${thinker.id}`, { name: "Thinker", model: "x" })).status).toBe(403);
    expect((await call(memberKey, "DELETE", `/v1/models/tiers/${thinker.id}`, { replacement: null })).status).toBe(403);
    expect((await call(memberKey, "PUT", "/v1/models/tiers/order", { ids: [thinker.id] })).status).toBe(403);
    expect((await call(memberKey, "POST", "/v1/models/test", { model: "x" })).status).toBe(403);
    expect((await call(memberKey, "POST", "/v1/models/upgrade/dismiss")).status).toBe(403);
    expect((await body(await call(adminKey, "GET", "/v1/models/tiers"))).canEdit).toBe(true);
  });

  test("an admin sets a tier's model, as the proxy names it", async () => {
    const coder = await byName("Coder");
    const res = await call(adminKey, "PUT", `/v1/models/tiers/${coder.id}`, { name: "Coder", description: coder.description, model: "claude-opus-5-5" });
    expect(res.status).toBe(200);
    expect((await body(res)).tiers.find((t: Json) => t.id === coder.id)).toMatchObject({ model: "claude-opus-5-5", updatedBy: { name: "Ana" } });
  });

  test("a model with its provider, a space, or too long is refused saying how the proxy names it", async () => {
    const coder = await byName("Coder");
    for (const model of ["llm-anthropic/claude-opus-5-5", "a b", "", "x".repeat(201)]) {
      const res = await call(adminKey, "PUT", `/v1/models/tiers/${coder.id}`, { name: "Coder", model });
      expect(res.status).toBe(400);
      expect((await body(res)).error.message).toBe("request body failed validation: model: The model as the proxy names it: no spaces or slashes, at most 200 characters");
    }
    expect((await byName("Coder")).model).toBe("claude-opus-5-5");
  });

  test("an admin adds one, at the end; a name taken whatever its case is 409", async () => {
    const res = await call(adminKey, "POST", "/v1/models/tiers", { name: "Cheap", description: "Bulk, low-stakes work at the lowest price.", model: "gpt-5.6-luna" });
    expect(res.status).toBe(201);
    expect((await body(res)).tiers.map((t: Json) => t.name)).toEqual(["Thinker", "Coder", "Fast", "Cheap"]);
    const taken = await call(adminKey, "POST", "/v1/models/tiers", { name: "cheap" });
    expect(taken.status).toBe(409);
    expect((await body(taken)).error.message).toBe("there is already a tier named cheap");
    const fast = await byName("Fast");
    expect((await call(adminKey, "PUT", `/v1/models/tiers/${fast.id}`, { name: "CODER" })).status).toBe(409);
    expect((await byName("Fast")).name).toBe("Fast");
  });

  test("an admin reorders them; an order that leaves one out is refused", async () => {
    const ids = (await tiers()).map((t) => t.id);
    const reversed = [...ids].reverse();
    expect((await body(await call(adminKey, "PUT", "/v1/models/tiers/order", { ids: reversed }))).tiers.map((t: Json) => t.id)).toEqual(reversed);
    expect((await call(adminKey, "PUT", "/v1/models/tiers/order", { ids: ids.slice(1) })).status).toBe(400);
    await call(adminKey, "PUT", "/v1/models/tiers/order", { ids });
    expect((await tiers()).map((t) => t.id)).toEqual(ids);
  });

  test("one that does not exist is 404", async () => {
    expect((await call(adminKey, "PUT", "/v1/models/tiers/mtr_nope", { name: "X" })).status).toBe(404);
    expect((await call(adminKey, "DELETE", "/v1/models/tiers/mtr_nope", { replacement: null })).status).toBe(404);
  });

  test("another organization sees none of it, and cannot change or name it", async () => {
    const coder = await byName("Coder");
    expect((await tiers(otherKey)).map((t) => t.model)).toEqual([null, null, null]);
    expect((await call(otherKey, "PUT", `/v1/models/tiers/${coder.id}`, { name: "Mine", model: "x" })).status).toBe(404);
    expect((await call(otherKey, "DELETE", `/v1/models/tiers/${coder.id}`, { replacement: null })).status).toBe(404);
    expect((await call(otherKey, "PATCH", "/v1/settings/organization", { roles: { reviewer: { tier: coder.id } } })).status).toBe(400);
    expect(await byName("Coder")).toMatchObject({ name: "Coder", model: "claude-opus-5-5" });
  });
});

describe("roles name a tier", () => {
  test("set at the organization, overridden in a project, Reset; the fixer follows the implementer", async () => {
    const coder = await byName("Coder");
    const cheap = await byName("Cheap");
    const org = await body(await call(adminKey, "GET", "/v1/settings/organization"));
    expect(org.roles.implementer.tier).toEqual({ value: coder.id, source: "organization" });
    expect(org.roles.fixer.tier).toEqual({ value: coder.id, source: "organization", followsImplementer: true });
    expect(org.roles.implementer.model).toBeUndefined();

    const project = await body(await call(adminKey, "PATCH", `/v1/projects/${PROJECT}/settings`, { roles: { implementer: { tier: cheap.id } } }));
    expect(project.roles.implementer.tier).toEqual({ value: cheap.id, source: "project", organization: coder.id });
    expect(project.roles.fixer.tier).toEqual({ value: cheap.id, source: "organization", followsImplementer: true, organization: coder.id });
    expect(await projectModels()).toEqual({ implementer: { tier: cheap.id } });

    const reset = await body(await call(adminKey, "PATCH", `/v1/projects/${PROJECT}/settings`, { roles: { implementer: { tier: null } } }));
    expect(reset.roles.implementer.tier).toEqual({ value: coder.id, source: "organization", organization: coder.id });
    expect(await projectModels()).toEqual({});
  });

  test("a tier the organization lacks is refused; a model is refused saying to name a tier", async () => {
    expect((await call(adminKey, "PATCH", `/v1/projects/${PROJECT}/settings`, { roles: { reviewer: { tier: "mtr_nope" } } })).status).toBe(400);
    for (const path of ["/v1/settings/organization", `/v1/projects/${PROJECT}/settings`]) {
      const res = await call(adminKey, "PATCH", path, { roles: { implementer: { model: "claude-opus-5-5" } } });
      expect(res.status).toBe(400);
      expect((await body(res)).error.message).toContain("roles.implementer.model: a role names a model tier");
    }
    const create = await call(adminKey, "POST", "/v1/projects", { name: "Bad", slug: "bad", agentModels: { conductor: { model: "fake/scripted" } } });
    expect(create.status).toBe(400);
    expect((await call(adminKey, "POST", "/v1/projects", { name: "Bad", slug: "bad", agentModels: { conductor: { tier: "mtr_nope" } } })).status).toBe(400);
    expect((await owner`SELECT count(*)::int AS n FROM projects WHERE slug = 'bad'`)[0].n).toBe(0);
    expect((await call(adminKey, "PATCH", `/v1/projects/${PROJECT}`, { agentModels: { conductor: { tier: "mtr_nope" } } })).status).toBe(400);
  });

  test("the tiers say who uses them, at which effort", async () => {
    const cheap = await byName("Cheap");
    await call(adminKey, "PATCH", `/v1/projects/${PROJECT}/settings`, { roles: { reviewer: { tier: cheap.id, effort: "low" } } });
    await call(adminKey, "PATCH", "/v1/settings/organization", { roles: { simplifier: { tier: cheap.id, effort: "high" } } });
    expect((await byName("Cheap")).usedBy).toEqual([
      { kind: "organization", role: "simplifier", project: null, effort: "high" },
      { kind: "project", role: "reviewer", project: DOCS_SITE, effort: "low" },
    ]);
  });

  test("a project's fixer following the project's implementer is listed as using its tier", async () => {
    const cheap = await byName("Cheap");
    const before = await projectModels();
    try {
      await call(adminKey, "PATCH", `/v1/projects/${PROJECT}/settings`, { roles: { implementer: { tier: cheap.id, effort: "medium" } } });
      const project = DOCS_SITE;
      expect((await byName("Cheap")).usedBy.filter((u: Json) => u.kind === "project")).toEqual([
        { kind: "project", role: "reviewer", project, effort: "low" },
        { kind: "project", role: "implementer", project, effort: "medium" },
        { kind: "project", role: "fixer", project, inherited: true, effort: "medium" },
      ]);
    } finally {
      await owner`UPDATE projects SET agent_models = ${before}::jsonb WHERE id = ${PROJECT}`;
    }
  });
});

describe("a test message", () => {
  test("goes to the orchestrator once per distinct effort of the tier's agents; none for a new tier", async () => {
    const cheap = await byName("Cheap");
    tested.length = 0;
    const res = await call(adminKey, "POST", "/v1/models/test", { model: "gpt-5.6-sol", tierId: cheap.id });
    expect(res.status).toBe(200);
    expect((await body(res)).results.map((r: Json) => [r.efforts, r.ok])).toEqual([[["high"], true], [["low"], true]]);
    expect((await body(await call(adminKey, "POST", "/v1/models/test", { model: "gpt-5.6-sol" }))).results.map((r: Json) => r.efforts)).toEqual([[null]]);
    expect(tested).toEqual([{ model: "gpt-5.6-sol", efforts: ["high", "low"] }, { model: "gpt-5.6-sol", efforts: [null] }]);
  });

  test("an effort several of the tier's agents share is asked for once", async () => {
    const cheap = await byName("Cheap");
    const before = { org: await orgModels(), project: await projectModels() };
    try {
      // Two more roles at high, and one at none: five uses, three efforts.
      await call(adminKey, "PATCH", "/v1/settings/organization", { roles: { qa_browser: { tier: cheap.id, effort: "high" } } });
      await call(adminKey, "PATCH", `/v1/projects/${PROJECT}/settings`, {
        roles: { simplifier: { tier: cheap.id, effort: "high" }, investigator: { tier: cheap.id } },
      });
      expect((await byName("Cheap")).usedBy.map((u: Json) => u.effort).sort()).toEqual(["high", "high", "high", "low", null]);
      tested.length = 0;
      expect((await call(adminKey, "POST", "/v1/models/test", { model: "gpt-5.6-sol", tierId: cheap.id })).status).toBe(200);
      expect(tested).toHaveLength(1);
      const { efforts } = tested[0] as { efforts: Array<string | null> };
      expect([...efforts].sort()).toEqual(["high", "low", null]);
    } finally {
      await owner`UPDATE organizations SET default_agent_models = ${before.org}::jsonb WHERE id = ${ORG}`;
      await owner`UPDATE projects SET agent_models = ${before.project}::jsonb WHERE id = ${PROJECT}`;
    }
  });

  test("a member is refused before the orchestrator is asked; an admin's reaches it", async () => {
    tested.length = 0;
    const refused = await call(memberKey, "POST", "/v1/models/test", { model: "gpt-5.6-sol" });
    expect(refused.status).toBe(403);
    expect(tested).toEqual([]);
    expect((await call(adminKey, "POST", "/v1/models/test", { model: "gpt-5.6-sol" })).status).toBe(200);
    expect(tested).toEqual([{ model: "gpt-5.6-sol", efforts: [null] }]);
  });

  test("an answer slower than other orchestrator calls may take still comes back", async () => {
    const res = await call(adminKey, "POST", "/v1/models/test", { model: "slow" });
    expect(res.status).toBe(200);
    expect((await body(res)).results.map((r: Json) => r.ok)).toEqual([true]);
  });

  test("another orchestrator call as slow is given up on", async () => {
    const res = await call(adminKey, "GET", "/v1/push/key");
    expect(res.status).toBe(503);
    expect((await body(res)).error.message).toStartWith("the orchestrator is unreachable: ");
  });

  test("the proxy's refusal is passed on as it came", async () => {
    const res = await body(await call(adminKey, "POST", "/v1/models/test", { model: "nope" }));
    expect(res.results).toEqual([{ efforts: [null], sent: null, ok: false, latencyMs: 12, status: 404, error: "model nope is not served here" }]);
  });
});

describe("the proxy's models", () => {
  test("members read them as suggestions", async () => {
    expect(await body(await call(memberKey, "GET", "/v1/models/proxy")))
      .toEqual({ models: ["claude-opus-5-5", "gpt-5.6-sol"], source: "https://llm.example/v1", problem: null });
  });

  test("unreadable: none, and why; a tier still takes any name", async () => {
    modelsReply = () => new Response("<html>bad gateway</html>", { status: 502 });
    try {
      expect(await body(await call(memberKey, "GET", "/v1/models/proxy"))).toEqual({ models: [], source: null, problem: "the orchestrator answered 502" });
      const fast = await byName("Fast");
      expect((await call(adminKey, "PUT", `/v1/models/tiers/${fast.id}`, { name: "Fast", model: "not-listed-anywhere" })).status).toBe(200);
    } finally {
      modelsReply = listedModels;
    }
  });
});

describe("removing a tier", () => {
  test("in use, with no replacement, is refused and changes nothing", async () => {
    const cheap = await byName("Cheap");
    const res = await call(adminKey, "DELETE", `/v1/models/tiers/${cheap.id}`, { replacement: null });
    expect(res.status).toBe(409);
    expect((await body(res)).error.code).toBe("in_use");
    expect(await byName("Cheap")).toBeDefined();
  });

  test("a replacement must be another of the organization's tiers", async () => {
    const cheap = await byName("Cheap");
    expect((await call(adminKey, "DELETE", `/v1/models/tiers/${cheap.id}`, { replacement: cheap.id })).status).toBe(400);
    expect((await call(adminKey, "DELETE", `/v1/models/tiers/${cheap.id}`, { replacement: "mtr_nope" })).status).toBe(400);
    const otherThinker = (await tiers(otherKey))[0];
    expect((await call(adminKey, "DELETE", `/v1/models/tiers/${cheap.id}`, { replacement: otherThinker.id })).status).toBe(400);
    expect(await byName("Cheap")).toBeDefined();
  });

  test("in use, moves every organization role and project override to the replacement, in one go", async () => {
    const cheap = await byName("Cheap");
    const thinker = await byName("Thinker");
    const res = await call(adminKey, "DELETE", `/v1/models/tiers/${cheap.id}`, { replacement: thinker.id });
    expect(res.status).toBe(200);
    expect((await body(res)).tiers.map((t: Json) => t.name)).not.toContain("Cheap");
    expect((await orgModels()).simplifier).toEqual({ tier: thinker.id, effort: "high" });
    expect(await projectModels()).toEqual({ reviewer: { tier: thinker.id, effort: "low" } });
  });

  test("one nothing uses goes with no replacement; the last one cannot go", async () => {
    const fast = await byName("Fast");
    expect((await call(adminKey, "DELETE", `/v1/models/tiers/${fast.id}`, { replacement: null })).status).toBe(200);
    const coder = await byName("Coder");
    const thinker = await byName("Thinker");
    expect((await call(adminKey, "DELETE", `/v1/models/tiers/${coder.id}`, { replacement: thinker.id })).status).toBe(200);
    expect((await orgModels()).implementer).toEqual({ tier: thinker.id });
    const last = await call(adminKey, "DELETE", `/v1/models/tiers/${thinker.id}`, { replacement: null });
    expect(last.status).toBe(409);
    expect((await body(last)).error.message).toBe("the last tier cannot be removed: every agent needs one");
    expect((await tiers()).map((t) => t.name)).toEqual(["Thinker"]);
  });
});

describe("the upgrade's notes", () => {
  test("shown to admins until one dismisses them; never to members", async () => {
    const [thinker] = await tiers();
    await owner`INSERT INTO model_tier_upgrade_notes (organization_id, project_id, role, old_model, tier_id, tier_name, model_changed)
      VALUES (${ORG}, NULL, 'reviewer', 'llm-anthropic/claude-sonnet-5-5', ${thinker.id}, 'Thinker', true),
             (${ORG}, ${PROJECT}, 'implementer', 'llm-openai/gpt-5.6-sol', NULL, 'gpt-5.6-sol', false),
             (${OTHER}, NULL, 'reviewer', 'llm-anthropic/x', NULL, 'Thinker', false)`;
    const notes = (await body(await call(adminKey, "GET", "/v1/models/tiers"))).upgrade;
    expect(notes.map((n: Json) => ({ ...n, id: 0 }))).toEqual([
      { id: 0, role: "reviewer", project: null, oldModel: "llm-anthropic/claude-sonnet-5-5", tierId: thinker.id, tierName: "Thinker", newTier: false, modelChanged: true },
      { id: 0, role: "implementer", project: DOCS_SITE, oldModel: "llm-openai/gpt-5.6-sol", tierId: null, tierName: "gpt-5.6-sol", newTier: false, modelChanged: false },
    ]);
    expect((await body(await call(memberKey, "GET", "/v1/models/tiers"))).upgrade).toEqual([]);
    const done = await call(adminKey, "POST", "/v1/models/upgrade/dismiss");
    expect(done.status).toBe(200);
    expect((await body(done)).upgrade).toEqual([]);
    // Another organization's notes are its own.
    expect((await owner`SELECT count(*)::int AS n FROM model_tier_upgrade_notes WHERE organization_id = ${OTHER} AND dismissed_at IS NULL`)[0].n).toBe(1);
  });
});

/**
 * Requests that lock the same rows, interleaved. A third transaction holds
 * a row (the organization's, unless a test says another), so each request
 * stops at the first lock it cannot take; which one queued first decides
 * the order. Whatever the order: no deadlock, and nothing left naming a
 * tier that is gone.
 */
describe("removing a tier while a project's role is set to it", () => {
  // Backends of the app role waiting on a lock they have not been granted.
  const waiting = async () => (await owner`
    SELECT count(DISTINCT l.pid)::int AS n FROM pg_locks l JOIN pg_stat_activity a ON a.pid = l.pid
    WHERE NOT l.granted AND a.usename = 'dude_app' AND a.datname = current_database()`)[0].n as number;
  async function waitFor(ready: () => Promise<boolean>, ms = 5_000) {
    const until = Date.now() + ms;
    while (!(await ready())) {
      if (Date.now() > until) throw new Error("condition not reached");
      await Bun.sleep(20);
    }
  }

  type Hold = (tx: SQL) => Promise<unknown>;
  const holdOrganization: Hold = (tx) => tx`SELECT 1 FROM organizations WHERE id = ${ORG} FOR UPDATE`;
  const holdTier = (id: string): Hold => (tx) => tx`SELECT 1 FROM model_tiers WHERE id = ${id} FOR SHARE`;
  const holdProject: Hold = (tx) => tx`SELECT 1 FROM projects WHERE id = ${PROJECT} FOR UPDATE`;

  /** Runs `first`, then `second`, each once the one before is queued on a lock, with `hold` taken. */
  async function interleaved(first: () => Promise<Response>, second: () => Promise<Response>, hold = holdOrganization) {
    const held = Promise.withResolvers<void>();
    const release = Promise.withResolvers<void>();
    const holder = owner.begin(async (tx) => {
      await hold(tx);
      held.resolve();
      await release.promise;
    });
    let a: Promise<Response> | undefined;
    let b: Promise<Response> | undefined;
    try {
      await held.promise;
      a = first();
      await waitFor(async () => (await waiting()) === 1);
      b = second();
      await waitFor(async () => (await waiting()) === 2);
      release.resolve();
      await holder;
      return [await a, await b] as const;
    } finally {
      release.resolve();
      await holder.catch(() => {});
      // A request left running after a failed wait would change what the next test sees.
      await Promise.allSettled([a, b].filter(Boolean));
    }
  }

  const freshTier = async (name: string) =>
    (await body(await call(adminKey, "POST", "/v1/models/tiers", { name }))).tiers.find((t: Json) => t.name === name).id as string;

  test("the removal queued first: it completes and the patch naming the tier is refused", async () => {
    const [thinker] = await tiers();
    const gone = await freshTier("Racing A");
    await owner`UPDATE projects SET agent_models = '{}' WHERE id = ${PROJECT}`;
    const [removal, patch] = await interleaved(
      () => call(adminKey, "DELETE", `/v1/models/tiers/${gone}`, { replacement: thinker.id }),
      () => call(adminKey, "PATCH", `/v1/projects/${PROJECT}/settings`, { roles: { reviewer: { tier: gone } } }),
    );
    expect(removal.status).toBe(200);
    expect(patch.status).toBe(400);
    expect((await body(patch)).error.message).toBe(`there is no model tier ${gone}`);
    expect(await projectModels()).toEqual({});
    expect((await tiers()).map((t) => t.id)).not.toContain(gone);
  }, 15_000);

  test("the patch queued first: it completes and the removal moves what it set", async () => {
    const [thinker] = await tiers();
    const gone = await freshTier("Racing B");
    await owner`UPDATE projects SET agent_models = '{}' WHERE id = ${PROJECT}`;
    const [patch, removal] = await interleaved(
      () => call(adminKey, "PATCH", `/v1/projects/${PROJECT}/settings`, { roles: { reviewer: { tier: gone } } }),
      () => call(adminKey, "DELETE", `/v1/models/tiers/${gone}`, { replacement: thinker.id }),
    );
    expect(patch.status).toBe(200);
    expect(removal.status).toBe(200);
    expect(await projectModels()).toEqual({ reviewer: { tier: thinker.id } });
    expect((await tiers()).map((t) => t.id)).not.toContain(gone);
  }, 15_000);

  test("the organization's patch queued first: it completes and the removal moves what it set", async () => {
    const [thinker] = await tiers();
    const gone = await freshTier("Racing C");
    const [patch, removal] = await interleaved(
      () => call(adminKey, "PATCH", "/v1/settings/organization", { roles: { reviewer: { tier: gone } } }),
      () => call(adminKey, "DELETE", `/v1/models/tiers/${gone}`, { replacement: thinker.id }),
    );
    expect([patch.status, removal.status]).toEqual([200, 200]);
    expect((await orgModels()).reviewer).toEqual({ tier: thinker.id });
    expect((await tiers()).map((t) => t.id)).not.toContain(gone);
  }, 15_000);

  // The epic takes the project row, then its insert needs the organization's
  // row FOR KEY SHARE; the removal holds that row by then, and wants the
  // project's next. Only a lock on the organization that lets KEY SHARE
  // through (NO KEY UPDATE) keeps the two from deadlocking.
  test("an epic added to a project naming the tier while it is removed: both complete", async () => {
    const [thinker] = await tiers();
    const gone = await freshTier("Racing F");
    await owner`UPDATE projects SET agent_models = ${{ reviewer: { tier: gone } }}::jsonb WHERE id = ${PROJECT}`;
    const [epic, removal] = await interleaved(
      () => call(adminKey, "POST", `/v1/projects/${PROJECT}/epics`, { title: "Racing" }),
      () => call(adminKey, "DELETE", `/v1/models/tiers/${gone}`, { replacement: thinker.id }),
      holdProject,
    );
    expect([epic.status, removal.status]).toEqual([201, 200]);
    expect(await projectModels()).toEqual({ reviewer: { tier: thinker.id } });
  }, 15_000);

  /**
   * Three new tiers, ids a < b < c, written to the table and named in the
   * opposite order: locked in id order they are taken a, b, c; by a
   * statement with no ORDER BY, read through the table or the name index,
   * c, b, a. The test checks the table order it relies on.
   */
  async function threeTiers(tag: string): Promise<[string, string, string]> {
    const ids = ["a", "b", "c"].map((x) => `mtr_racing_${tag}_${x}`);
    for (const [i, id] of [...ids].reverse().entries()) {
      await owner`INSERT INTO model_tiers (id, organization_id, name, position)
        VALUES (${id}, ${ORG}, ${`Racing ${tag} ${i}`}, (SELECT max(position) + 1 FROM model_tiers WHERE organization_id = ${ORG}))`;
    }
    const inTable = (await owner`SELECT id FROM model_tiers WHERE id IN ${owner(ids)} ORDER BY ctid`).map((r: Json) => r.id);
    expect(inTable).toEqual([...ids].reverse());
    return ids as [string, string, string];
  }

  // Each test holds tier b while a reorder and a request locking a and c
  // (named in the order given) run. In id order for every statement, the
  // reorder takes a and queues on b, and the other request queues on a.
  // A reorder locking c first, or a request locking c before a, holds c
  // while the other holds a: once b is free, each waits on the other.
  // "a, c" pins the reorder's order; "c, a" the other request's.
  for (const [first, second] of [["a", "c"], ["c", "a"]] as const) {
    test(`a removal while the tiers are reordered, the removed and its replacement ${first}, ${second}: both complete`, async () => {
      const [a, b, c] = await threeTiers(`d${first}`);
      const pick = { a, c };
      const order = (await tiers()).map((t) => t.id).reverse();
      const [reorder, removal] = await interleaved(
        () => call(adminKey, "PUT", "/v1/models/tiers/order", { ids: order }),
        () => call(adminKey, "DELETE", `/v1/models/tiers/${pick[first]}`, { replacement: pick[second] }),
        holdTier(b),
      );
      expect([reorder.status, removal.status]).toEqual([200, 200]);
      expect((await tiers()).map((t) => t.id)).toEqual(order.filter((id) => id !== pick[first]));
    }, 15_000);

    test(`a patch naming tiers ${first}, ${second} while the tiers are reordered: both complete`, async () => {
      const [a, b, c] = await threeTiers(`e${first}`);
      const pick = { a, c };
      await owner`UPDATE projects SET agent_models = '{}' WHERE id = ${PROJECT}`;
      const order = (await tiers()).map((t) => t.id).reverse();
      const roles = { implementer: { tier: pick[first] }, reviewer: { tier: pick[second] } };
      const [reorder, patch] = await interleaved(
        () => call(adminKey, "PUT", "/v1/models/tiers/order", { ids: order }),
        () => call(adminKey, "PATCH", `/v1/projects/${PROJECT}/settings`, { roles }),
        holdTier(b),
      );
      expect([reorder.status, patch.status]).toEqual([200, 200]);
      expect(await projectModels()).toEqual(roles);
      expect((await tiers()).map((t) => t.id)).toEqual(order);
    }, 15_000);
  }

  // Archiving an image locks its row FOR UPDATE, then its event's insert
  // needs the organization's row FOR KEY SHARE. A patch naming the image
  // must lock the image before the organization's row, or each waits on
  // the other.
  describe("archiving an image while a project's role is set to it", () => {
    const freshImage = async (name: string) => {
      const id = `img_racing_${name}`;
      await owner`INSERT INTO images (id, organization_id, name) VALUES (${id}, ${ORG}, ${name})`;
      return id;
    };
    const archived = async (id: string) => (await owner`SELECT archived_at IS NOT NULL AS a FROM images WHERE id = ${id}`)[0].a as boolean;

    test("the archive queued first: it completes and the patch naming the image is refused", async () => {
      const image = await freshImage("racing-a");
      await owner`UPDATE projects SET agent_models = '{}' WHERE id = ${PROJECT}`;
      const [archive, patch] = await interleaved(
        () => call(adminKey, "PATCH", `/v1/images/${image}`, { archived: true }),
        () => call(adminKey, "PATCH", `/v1/projects/${PROJECT}/settings`, { roles: { reviewer: { image } } }),
      );
      expect(archive.status).toBe(200);
      expect(patch.status).toBe(400);
      expect((await body(patch)).error.message).toBe("racing-a is archived: pick another image");
      expect(await projectModels()).toEqual({});
      expect(await archived(image)).toBe(true);
    }, 15_000);

    test("the patch queued first: both complete, and the role keeps naming the archived image", async () => {
      const image = await freshImage("racing-b");
      await owner`UPDATE projects SET agent_models = '{}' WHERE id = ${PROJECT}`;
      const [patch, archive] = await interleaved(
        () => call(adminKey, "PATCH", `/v1/projects/${PROJECT}/settings`, { roles: { reviewer: { image } } }),
        () => call(adminKey, "PATCH", `/v1/images/${image}`, { archived: true }),
      );
      expect([patch.status, archive.status]).toEqual([200, 200]);
      expect(await projectModels()).toEqual({ reviewer: { image } });
      expect(await archived(image)).toBe(true);
    }, 15_000);

    test("the organization's patch queued first: both complete", async () => {
      const image = await freshImage("racing-c");
      const [patch, archive] = await interleaved(
        () => call(adminKey, "PATCH", "/v1/settings/organization", { roles: { reviewer: { image } } }),
        () => call(adminKey, "PATCH", `/v1/images/${image}`, { archived: true }),
      );
      expect([patch.status, archive.status]).toEqual([200, 200]);
      expect((await orgModels()).reviewer.image).toBe(image);
      expect(await archived(image)).toBe(true);
    }, 15_000);
  });
});
