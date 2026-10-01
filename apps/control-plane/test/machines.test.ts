/**
 * Machine sizes through the public API: who may change them, what is
 * refused (off-step values, too big for a known host), the one default,
 * roles and previews naming them, and removing one in use — everything
 * that named it moved in the same transaction.
 *
 * Against a database of its own, migrated as a release is, and a stand-in
 * for the orchestrator that answers lux's pools as the test sets them.
 *
 * Requires DATABASE_URL: a role that can create databases (the owner).
 *
 * The tests run in order on one database and share its state: Half and
 * Large are made in "sizes" and used by the describes after it, so a test
 * picked alone with -t fails. A database per test would cost a migration each.
 */

import { afterAll, beforeAll, describe, expect, test } from "bun:test";
import { SQL } from "bun";
import { join } from "node:path";
import { GIB, promptRoleSchema, type MachinePool } from "@dude/domain";
import { closePool, setPool } from "../src/db/client.ts";
import { buildRouter } from "../src/index.ts";
import { Config, useConfig } from "../src/config.ts";
import type { Router } from "../src/api/router.ts";
import { createApiKey } from "../src/api/auth.ts";

const OWNER_URL = process.env.DATABASE_URL ?? "postgres://dude:dude@localhost:5433/dude";
const ROOT = join(import.meta.dir, "../../..");
const NAME = `dude_machines_test_${Bun.randomUUIDv7("hex").slice(-12)}`;
const ORG = "org_mach";
const OTHER = "org_mach_other";
const PROJECT = "prj_mach";

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

const pool = (name: string, host: MachinePool["hostSize"], isDefault = false): MachinePool => ({
  name, isDefault, platform: false, provider: "ec2", instanceType: "c7a.4xlarge", hostSize: host,
  hostSizeFrom: host ? "running" : null, hostsRunning: host ? 1 : 0,
});
/** What the orchestrator says lux's pools are; null: it cannot reach lux. */
let luxPools: MachinePool[] | null = [
  pool("default", { cpus: 16, memory: 32 * GIB, disk: 180 * GIB }, true),
  pool("big", { cpus: 32, memory: 64 * GIB, disk: 380 * GIB }),
  pool("fresh", null),
];
/** When set, how the orchestrator answers GET /internal/lux/pools instead. */
let poolsReply: (() => Response) | null = null;

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
  await owner`INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES (${PROJECT}, ${ORG}, 'Checkout', 'checkout', 'CO')`;
  app = new SQL(databaseUrl(true));
  setPool(app);
  adminKey = (await createApiKey({ organizationId: ORG, name: "Ana" })).key;
  memberKey = (await createApiKey({ organizationId: ORG, name: "Bo" })).key;
  otherKey = (await createApiKey({ organizationId: OTHER, name: "Cy" })).key;
  orchestratorServer = Bun.serve({
    port: 0,
    fetch(req) {
      const path = new URL(req.url).pathname;
      if (path === "/internal/lux/pools") {
        if (poolsReply) return poolsReply();
        return luxPools === null
          ? Response.json({ pools: [], readAt: new Date().toISOString(), problem: "lux is unreachable" })
          : Response.json({ pools: luxPools, readAt: new Date().toISOString(), problem: null });
      }
      if (path.endsWith("builtin")) return Response.json(Object.fromEntries(promptRoleSchema.options.map((r) => [r, "Built-in prompt"])));
      return Response.json({ requiredReviewers: ["correctness"], blockingSeverities: ["blocking"], maxReviewIterations: 3,
        maxAttemptsPerFinding: 2, maxPrFixIterations: 3, simplify: true, test: false, parkAfterMinutes: 10, idleNudgeMinutes: 0 });
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

const sizes = async (key = adminKey) => (await body(await call(key, "GET", "/v1/machines/sizes"))).sizes as Json[];
const byName = async (name: string) => (await sizes()).find((s) => s.name === name);
const LARGE = { name: "Large", cpus: 8, memoryMiB: 16384, diskGiB: 80, pool: null };

describe("sizes", () => {
  test("an organization starts with Standard, its default; everyone reads, only admins change", async () => {
    const res = await body(await call(memberKey, "GET", "/v1/machines/sizes"));
    expect(res.canEdit).toBe(false);
    expect(res.sizes).toHaveLength(1);
    expect(res.sizes[0]).toMatchObject({ name: "Standard", cpus: 2, memoryMiB: 8192, diskGiB: 20, pool: null, isDefault: true, usedBy: [] });
    expect((await call(memberKey, "POST", "/v1/machines/sizes", LARGE)).status).toBe(403);
    expect((await body(await call(adminKey, "GET", "/v1/machines/sizes"))).canEdit).toBe(true);
  });

  test("an admin adds one with half steps; an off-step value is refused naming the step", async () => {
    const added = await call(adminKey, "POST", "/v1/machines/sizes", { name: "Half", cpus: 6.5, memoryMiB: 23040, diskGiB: 120, pool: null });
    expect(added.status).toBe(201);
    expect((await body(added)).sizes.find((s: Json) => s.name === "Half")).toMatchObject({ cpus: 6.5, memoryMiB: 23040, isDefault: false });

    const off = await call(adminKey, "POST", "/v1/machines/sizes", { ...LARGE, cpus: 2.3 });
    expect(off.status).toBe(400);
    expect(JSON.stringify((await body(off)).error.details)).toContain("Whole or half CPUs");
    const taken = await call(adminKey, "POST", "/v1/machines/sizes", { ...LARGE, name: "half" });
    expect(taken.status).toBe(409);
  });

  test("too big for a host lux knows is refused, naming what does not fit; an unknown host is allowed", async () => {
    const big = await call(adminKey, "POST", "/v1/machines/sizes", { name: "XL", cpus: 16, memoryMiB: 72 * 1024, diskGiB: 200, pool: "big" });
    expect(big.status).toBe(422);
    expect((await body(big)).error.message).toBe("No host in ‘big’ can hold this: 72 GiB memory (it offers 64)");
    // Pool null is the pool lux marks as the default: 40 CPUs is more than its 16.
    expect((await call(adminKey, "POST", "/v1/machines/sizes", { ...LARGE, name: "Wide", cpus: 40 })).status).toBe(422);
    // A pool that never had a host, and lux unreachable: allowed.
    expect((await call(adminKey, "POST", "/v1/machines/sizes", { name: "Huge", cpus: 64, memoryMiB: 512, diskGiB: 5, pool: "fresh" })).status).toBe(201);
    luxPools = null;
    try {
      expect((await call(adminKey, "POST", "/v1/machines/sizes", { ...LARGE, name: "Blind", cpus: 40 })).status).toBe(201);
      const pools = await body(await call(memberKey, "GET", "/v1/machines/pools"));
      expect(pools).toMatchObject({ pools: [], problem: "lux is unreachable" });
    } finally {
      luxPools = [pool("default", { cpus: 16, memory: 32 * GIB, disk: 180 * GIB }, true), pool("big", { cpus: 32, memory: 64 * GIB, disk: 380 * GIB }), pool("fresh", null)];
    }
    for (const name of ["Huge", "Blind"]) {
      expect((await call(adminKey, "DELETE", `/v1/machines/sizes/${(await byName(name)).id}`, { replacement: null })).status).toBe(200);
    }
  });

  test("members read lux's pools", async () => {
    const pools = await body(await call(memberKey, "GET", "/v1/machines/pools"));
    expect(pools.problem).toBeNull();
    expect(pools.pools.map((p: Json) => p.name)).toEqual(["default", "big", "fresh"]);
  });

  test("there is always exactly one default, moved by making another one", async () => {
    await call(adminKey, "POST", "/v1/machines/sizes", LARGE);
    const large = await byName("Large");
    expect((await call(adminKey, "POST", `/v1/machines/sizes/${large.id}/default`)).status).toBe(200);
    expect((await sizes()).filter((s) => s.isDefault).map((s) => s.name)).toEqual(["Large"]);
    const standard = await byName("Standard");
    expect((await call(adminKey, "PUT", `/v1/machines/sizes/${standard.id}`, { ...standard, id: undefined, usedBy: undefined, updatedAt: undefined, updatedBy: undefined, isDefault: true })).status).toBe(200);
    expect((await sizes()).filter((s) => s.isDefault).map((s) => s.name)).toEqual(["Standard"]);
  });

  test("the default cannot be removed", async () => {
    const standard = await byName("Standard");
    const res = await call(adminKey, "DELETE", `/v1/machines/sizes/${standard.id}`, { replacement: null });
    expect(res.status).toBe(409);
    expect((await body(res)).error.message).toContain("make another the default first");
  });

  test("another organization sees none of it", async () => {
    const large = await byName("Large");
    expect((await sizes(otherKey)).map((s) => s.name)).toEqual(["Standard"]);
    expect((await call(otherKey, "PATCH", "/v1/settings/organization", { roles: { reviewer: { machineSize: large.id } } })).status).toBe(400);
  });
});

/** A size as PUT takes it: what GET says, less what only the API writes. */
const asInput = ({ id: _id, usedBy: _usedBy, updatedAt: _at, updatedBy: _by, ...input }: Json) => input;

describe("changing a size", () => {
  test("one that does not exist is 404", async () => {
    expect((await call(adminKey, "PUT", "/v1/machines/sizes/msz_nope", LARGE)).status).toBe(404);
    expect((await call(adminKey, "POST", "/v1/machines/sizes/msz_nope/default")).status).toBe(404);
  });

  test("renamed onto a name taken, whatever its case, is 409 and changes nothing", async () => {
    const large = await byName("Large");
    const res = await call(adminKey, "PUT", `/v1/machines/sizes/${large.id}`, { ...asInput(large), name: "HALF" });
    expect(res.status).toBe(409);
    expect((await body(res)).error.message).toBe("there is already a size named HALF");
    expect(await byName("Large")).toMatchObject({ cpus: 8, memoryMiB: 16384 });
  });

  test("made too big for its pool's host is 422 and changes nothing", async () => {
    const large = await byName("Large");
    const res = await call(adminKey, "PUT", `/v1/machines/sizes/${large.id}`, { ...asInput(large), memoryMiB: 72 * 1024, pool: "big" });
    expect(res.status).toBe(422);
    expect((await body(res)).error.message).toBe("No host in ‘big’ can hold this: 72 GiB memory (it offers 64)");
    expect(await byName("Large")).toMatchObject({ memoryMiB: 16384, pool: null });
  });

  test("unticking the default leaves it the default", async () => {
    const standard = await byName("Standard");
    expect((await call(adminKey, "PUT", `/v1/machines/sizes/${standard.id}`, { ...asInput(standard), isDefault: false })).status).toBe(200);
    expect((await sizes()).filter((s) => s.isDefault).map((s) => s.name)).toEqual(["Standard"]);
  });

  test("another organization's admin cannot edit, remove or default one: 404, nothing changed", async () => {
    const large = await byName("Large");
    for (const [method, path, payload] of [
      ["PUT", `/v1/machines/sizes/${large.id}`, { ...LARGE, name: "Taken over", cpus: 1 }],
      ["DELETE", `/v1/machines/sizes/${large.id}`, { replacement: null }],
      ["POST", `/v1/machines/sizes/${large.id}/default`, undefined],
    ] as const) {
      expect((await call(otherKey, method, path, payload)).status).toBe(404);
    }
    expect(await byName("Large")).toMatchObject({ name: "Large", cpus: 8, isDefault: false });
    expect((await sizes()).filter((s) => s.isDefault).map((s) => s.name)).toEqual(["Standard"]);
    expect((await sizes(otherKey)).map((s) => s.name)).toEqual(["Standard"]);
  });
});

describe("lux's pools when the orchestrator cannot give them", () => {
  test("an error with a message: no pools, its message; a size can still be added", async () => {
    poolsReply = () => Response.json({ error: { code: "unavailable", message: "lux answered 503" } }, { status: 503 });
    try {
      expect(await body(await call(memberKey, "GET", "/v1/machines/pools"))).toMatchObject({ pools: [], problem: "lux answered 503" });
      expect((await call(adminKey, "POST", "/v1/machines/sizes", { ...LARGE, name: "Unchecked", cpus: 40 })).status).toBe(201);
    } finally {
      poolsReply = null;
    }
    expect((await call(adminKey, "DELETE", `/v1/machines/sizes/${(await byName("Unchecked")).id}`, { replacement: null })).status).toBe(200);
  });

  test("a body that is not JSON: no pools, the status", async () => {
    poolsReply = () => new Response("<html>bad gateway</html>", { status: 502, headers: { "content-type": "text/html" } });
    try {
      expect(await body(await call(memberKey, "GET", "/v1/machines/pools"))).toMatchObject({ pools: [], problem: "the orchestrator answered 502" });
    } finally {
      poolsReply = null;
    }
  });
});

describe("roles and previews name a size", () => {
  test("set at the organization, overridden in a project, Reset; the fixer follows the implementer", async () => {
    const large = await byName("Large");
    const half = await byName("Half");
    const org = await body(await call(adminKey, "PATCH", "/v1/settings/organization", { roles: { implementer: { machineSize: large.id } } }));
    expect(org.roles.implementer.machineSize).toEqual({ value: large.id, source: "organization" });
    expect(org.roles.fixer.machineSize).toEqual({ value: large.id, source: "organization", followsImplementer: true });
    expect(org.roles.investigator.machineSize).toEqual({ value: null, source: "organization" });

    const project = await body(await call(adminKey, "PATCH", `/v1/projects/${PROJECT}/settings`, { roles: { implementer: { machineSize: half.id } } }));
    expect(project.roles.implementer.machineSize).toEqual({ value: half.id, source: "project", organization: large.id });
    const [stored] = await owner`SELECT agent_models FROM projects WHERE id = ${PROJECT}`;
    expect(stored.agent_models).toEqual({ implementer: { machineSize: half.id } });

    const reset = await body(await call(adminKey, "PATCH", `/v1/projects/${PROJECT}/settings`, { roles: { implementer: { machineSize: null } } }));
    expect(reset.roles.implementer.machineSize).toEqual({ value: large.id, source: "organization", organization: large.id });
    const [after] = await owner`SELECT agent_models FROM projects WHERE id = ${PROJECT}`;
    expect(after.agent_models).toEqual({});

    expect((await call(adminKey, "PATCH", `/v1/projects/${PROJECT}/settings`, { roles: { reviewer: { machineSize: "msz_nope" } } })).status).toBe(400);
  });

  test("a project's fixer follows the project's implementer, unless the organization gives the fixer one", async () => {
    const large = await byName("Large");
    const half = await byName("Half");
    const standard = await byName("Standard");
    try {
      let project = await body(await call(adminKey, "PATCH", `/v1/projects/${PROJECT}/settings`, { roles: { implementer: { machineSize: half.id } } }));
      // Under it, the organization's fixer is the organization's implementer's.
      expect(project.roles.fixer.machineSize).toEqual({ value: half.id, source: "organization", followsImplementer: true, organization: large.id });
      expect((await byName("Half")).usedBy).toContainEqual({ kind: "project", role: "fixer", project: { id: PROJECT, name: "Checkout", imageUrl: null }, inherited: true });

      await call(adminKey, "PATCH", "/v1/settings/organization", { roles: { fixer: { machineSize: standard.id } } });
      project = await body(await call(adminKey, "GET", `/v1/projects/${PROJECT}/settings`));
      expect(project.roles.fixer.machineSize).toEqual({ value: standard.id, source: "organization", followsImplementer: false, organization: standard.id });
      expect((await byName("Half")).usedBy.filter((u: Json) => u.role === "fixer")).toEqual([]);
    } finally {
      await call(adminKey, "PATCH", "/v1/settings/organization", { roles: { fixer: { machineSize: null } } });
      await call(adminKey, "PATCH", `/v1/projects/${PROJECT}/settings`, { roles: { implementer: { machineSize: null } } });
    }
  });

  test("a project's previews name one; one the organization lacks is refused", async () => {
    const half = await byName("Half");
    const res = await call(adminKey, "PUT", `/v1/projects/${PROJECT}/preview-settings`, { machineSize: half.id });
    expect(res.status).toBe(200);
    expect((await body(res)).machineSize).toBe(half.id);
    expect((await call(adminKey, "PUT", `/v1/projects/${PROJECT}/preview-settings`, { machineSize: "msz_nope" })).status).toBe(400);
  });

  test("the sizes say who uses them", async () => {
    const half = await byName("Half");
    await call(adminKey, "PATCH", `/v1/projects/${PROJECT}/settings`, { roles: { reviewer: { machineSize: half.id } } });
    const uses = (await byName("Half")).usedBy;
    expect(uses).toContainEqual({ kind: "project", role: "reviewer", project: { id: PROJECT, name: "Checkout", imageUrl: null } });
    expect(uses).toContainEqual({ kind: "preview", role: null, project: { id: PROJECT, name: "Checkout", imageUrl: null } });
    expect((await byName("Large")).usedBy).toEqual([
      { kind: "organization", role: "implementer", project: null },
      { kind: "organization", role: "fixer", project: null, inherited: true },
    ]);
  });
});

describe("removing a size in use", () => {
  test("moves everything that named it to the replacement, in one go", async () => {
    const half = await byName("Half");
    const large = await byName("Large");
    await call(adminKey, "PATCH", "/v1/settings/organization", { roles: { simplifier: { machineSize: half.id, effort: "low" } } });
    const res = await call(adminKey, "DELETE", `/v1/machines/sizes/${half.id}`, { replacement: large.id });
    expect(res.status).toBe(200);
    expect((await body(res)).sizes.map((s: Json) => s.name)).not.toContain("Half");
    const [org] = await owner`SELECT default_agent_models FROM organizations WHERE id = ${ORG}`;
    expect(org.default_agent_models.simplifier).toEqual({ machineSize: large.id, effort: "low" });
    const [p] = await owner`SELECT agent_models, preview_settings FROM projects WHERE id = ${PROJECT}`;
    expect(p.agent_models.reviewer).toEqual({ machineSize: large.id });
    expect(p.preview_settings.machineSize).toBe(large.id);
  });

  test("or to no size, following the default: the keys go", async () => {
    const large = await byName("Large");
    const res = await call(adminKey, "DELETE", `/v1/machines/sizes/${large.id}`, { replacement: null });
    expect(res.status).toBe(200);
    const [org] = await owner`SELECT default_agent_models FROM organizations WHERE id = ${ORG}`;
    expect(org.default_agent_models).toEqual({ simplifier: { effort: "low" } });
    const [p] = await owner`SELECT agent_models, preview_settings FROM projects WHERE id = ${PROJECT}`;
    expect(p.agent_models).toEqual({});
    expect(p.preview_settings.machineSize).toBeUndefined();
  });

  test("a replacement must be another of the organization's sizes", async () => {
    await call(adminKey, "POST", "/v1/machines/sizes", LARGE);
    const large = await byName("Large");
    expect((await call(adminKey, "DELETE", `/v1/machines/sizes/${large.id}`, { replacement: large.id })).status).toBe(400);
    expect((await call(adminKey, "DELETE", `/v1/machines/sizes/${large.id}`, { replacement: "msz_nope" })).status).toBe(400);
    expect((await call(memberKey, "DELETE", `/v1/machines/sizes/${large.id}`, { replacement: null })).status).toBe(403);
    expect(await byName("Large")).toBeDefined();
  });
});
