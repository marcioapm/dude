/**
 * Servers through the public API: a project's recipes and preview
 * settings (kept here — who may change them, what is refused), and a
 * task's or Run's servers (forwarded to the orchestrator as the person,
 * with its answer passed back as it came).
 *
 * Against a database of its own, migrated as a release is, and a stand-in
 * for the orchestrator that records what it was asked.
 *
 * Requires DATABASE_URL: a role that can create databases (the owner).
 */

import { afterAll, beforeAll, describe, expect, test } from "bun:test";
import { SQL } from "bun";
import { chmodSync, mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { closePool, getPool, setPool } from "../src/db/client.ts";
import { buildRouter, startServer } from "../src/index.ts";
import { Config, useConfig } from "../src/config.ts";
import type { Router } from "../src/api/router.ts";
import { createApiKey } from "../src/api/auth.ts";

const OWNER_URL = process.env.DATABASE_URL ?? "postgres://dude:dude@localhost:5433/dude";
const ROOT = join(import.meta.dir, "../../..");
const NAME = `dude_servers_test_${Bun.randomUUIDv7("hex").slice(-12)}`;
const ORG = "org_srv";
const OTHER = "org_srv_other";
const PROJECT = "prj_srv";

function databaseUrl(user: string, name: string): string {
  const url = new URL(OWNER_URL);
  if (user === "app") {
    url.username = "dude_app";
    url.password = "dude_app";
  }
  url.pathname = `/${name}`;
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
const asked: Array<{ method: string; path: string; org: string | null; actor: string | null; body: string }> = [];

/** A response body, as the tests read it: whatever the API said. */
type Json = any; // eslint-disable-line @typescript-eslint/no-explicit-any
const body = async (res: Response): Promise<Json> => res.json();

function call(key: string, method: string, path: string, body?: unknown) {
  return router.handle(
    new Request(`http://dude.test${path}`, {
      method,
      headers: { authorization: `Bearer ${key}`, "content-type": "application/json" },
      ...(body === undefined ? {} : { body: JSON.stringify(body) }),
    }),
  );
}

beforeAll(async () => {
  admin = new SQL(OWNER_URL);
  await admin.unsafe(`CREATE DATABASE "${NAME}"`);
  const migrate = Bun.spawnSync(["bun", "run", "apps/control-plane/src/db/migrate.ts"], {
    cwd: ROOT,
    env: { ...process.env, DATABASE_URL: databaseUrl("owner", NAME) },
  });
  if (migrate.exitCode !== 0) throw new Error(`migrate: ${migrate.stderr.toString()}`);
  owner = new SQL(databaseUrl("owner", NAME));
  for (const id of [ORG, OTHER]) await owner`INSERT INTO organizations (id, name, slug) VALUES (${id}, ${id}, ${id})`;
  await owner`INSERT INTO projects (id, organization_id, name, slug, key_prefix, runtime_image)
              VALUES (${PROJECT}, ${ORG}, 'Web', 'web', 'WEB', 'node:22')`;

  app = new SQL(databaseUrl("app", NAME));
  setPool(app);
  // The organization's first person is its admin; the next a member.
  adminKey = (await createApiKey({ organizationId: ORG, name: "Ana" })).key;
  memberKey = (await createApiKey({ organizationId: ORG, name: "Bo" })).key;
  otherKey = (await createApiKey({ organizationId: OTHER, name: "Cy" })).key;

  orchestratorServer = Bun.serve({
    port: 0,
    async fetch(req) {
      const url = new URL(req.url);
      asked.push({
        method: req.method,
        path: url.pathname + url.search,
        org: req.headers.get("x-dude-organization"),
        actor: req.headers.get("x-dude-actor"),
        body: await req.text(),
      });
      if (req.method === "DELETE" && url.pathname.includes("/servers/")) return new Response(null, { status: 204 });
      if (url.pathname.endsWith("/start")) {
        return Response.json({ error: { code: "not_running", message: "run is stopped" } }, { status: 409 });
      }
      const preview = url.pathname.startsWith("/internal/tasks/") ? { id: "run_p", luxRunId: "lux_p", state: "paused" } : null;
      return Response.json({ run: null, servers: [], moved: null, recipes: [], preview }, { status: req.method === "POST" ? 201 : 200 });
    },
  });
  useConfig(Config.load({ env: { ...process.env,
    DUDE_ORCHESTRATOR_URL: `http://localhost:${orchestratorServer.port}`, DUDE_ORCHESTRATOR_TOKEN: "svc" } }));
  router = buildRouter("");
});

afterAll(async () => {
  useConfig(null);
  await orchestratorServer?.stop(true);
  await closePool(app);
  await owner?.end();
  await admin?.unsafe(`DROP DATABASE IF EXISTS "${NAME}" WITH (FORCE)`);
  await admin?.end();
});

const web = {
  name: "web",
  port: 3000,
  command: "npm run dev -- --host 0.0.0.0 --port 3000",
  workdir: "apps/web",
  setup: "npm ci",
  env: [{ name: "VITE_API_URL", value: "http://localhost:8080" }],
  autostartInPreviews: true,
};

describe("a project's servers", () => {
  test("start empty, with the default preview settings", async () => {
    const res = await call(memberKey, "GET", `/v1/projects/${PROJECT}/servers`);
    expect(res.status).toBe(200);
    expect(await body(res)).toEqual({ servers: [], previews: { image: null, egress: [], idleTimeoutMinutes: 15, machineSize: null } });
  });

  test("a maintainer saves one, and everyone reads it with who changed it", async () => {
    const res = await call(adminKey, "PUT", `/v1/projects/${PROJECT}/servers/web`, web);
    expect(res.status).toBe(200);
    const saved = await body(res);
    expect(saved).toMatchObject({ ...web, updatedBy: { name: "Ana" } });
    expect(Date.parse(saved.updatedAt)).toBeGreaterThan(0);

    const list = await body(await call(memberKey, "GET", `/v1/projects/${PROJECT}/servers`));
    expect(list.servers).toEqual([saved]);
    const [event] = await owner`SELECT payload FROM events WHERE project_id = ${PROJECT} AND event_type = 'settings.updated'
                                ORDER BY cursor DESC LIMIT 1`;
    expect(event.payload.changed).toEqual({ servers: { web: "saved" } });
  });

  test("a member may not change them", async () => {
    for (const [method, path, body] of [
      ["PUT", `/v1/projects/${PROJECT}/servers/api`, { ...web, name: "api" }],
      ["DELETE", `/v1/projects/${PROJECT}/servers/web`, undefined],
      ["PUT", `/v1/projects/${PROJECT}/preview-settings`, { egress: [] }],
    ] as const) {
      const res = await call(memberKey, method, path, body);
      expect(res.status).toBe(403);
    }
  });

  test("what lux would refuse is refused here", async () => {
    for (const bad of [
      { ...web, name: "Web" },
      { ...web, name: "web-" },
      { ...web, name: "9web" },
      { ...web, name: "a".repeat(31) },
      { ...web, port: 0 },
      { ...web, port: 65536 },
      { ...web, port: 30.5 },
      { ...web, command: " " },
      { ...web, workdir: "/etc" },
      { ...web, workdir: "../other" },
      { ...web, env: [{ name: "1BAD", value: "x" }] },
      { ...web, env: [{ name: "A", value: "1" }, { name: "A", value: "2" }] },
      { ...web, env: [{ name: "LUX_TOKEN", value: "x" }] },
    ]) {
      const res = await call(adminKey, "PUT", `/v1/projects/${PROJECT}/servers/${bad.name}`, bad);
      expect(res.status).toBe(400);
    }
    // The longest name lux takes.
    const longest = "a".repeat(30);
    expect((await call(adminKey, "PUT", `/v1/projects/${PROJECT}/servers/${longest}`, { ...web, name: longest })).status).toBe(200);
    expect((await call(adminKey, "DELETE", `/v1/projects/${PROJECT}/servers/${longest}`)).status).toBe(204);
  });

  test("a body naming another name renames it, onto a free name only", async () => {
    await call(adminKey, "PUT", `/v1/projects/${PROJECT}/servers/api`, { ...web, name: "api", port: 8080, setup: null });
    const taken = await call(adminKey, "PUT", `/v1/projects/${PROJECT}/servers/api`, { ...web, name: "web" });
    expect(taken.status).toBe(409);

    const res = await call(adminKey, "PUT", `/v1/projects/${PROJECT}/servers/api`, { ...web, name: "backend", port: 8080 });
    expect(res.status).toBe(200);
    const names = (await body(await call(memberKey, "GET", `/v1/projects/${PROJECT}/servers`))).servers.map(
      (s: { name: string }) => s.name,
    );
    expect(names).toEqual(["backend", "web"]);
    expect((await call(adminKey, "DELETE", `/v1/projects/${PROJECT}/servers/backend`)).status).toBe(204);
    expect((await call(adminKey, "DELETE", `/v1/projects/${PROJECT}/servers/backend`)).status).toBe(404);
  });

  test("preview settings are replaced whole, unset ones taking their defaults", async () => {
    const res = await call(adminKey, "PUT", `/v1/projects/${PROJECT}/preview-settings`, {
      image: "ghcr.io/acme/runner:node22",
      egress: ["registry.npmjs.org", "registry.npmjs.org", "proxy.golang.org"],
      idleTimeoutMinutes: 15,
    });
    expect(res.status).toBe(200);
    expect(await body(res)).toEqual({
      image: "ghcr.io/acme/runner:node22",
      egress: ["registry.npmjs.org", "proxy.golang.org"],
      idleTimeoutMinutes: 15,
      machineSize: null,
    });
    const reset = await body(await call(adminKey, "PUT", `/v1/projects/${PROJECT}/preview-settings`, {}));
    expect(reset).toEqual({ image: null, egress: [], idleTimeoutMinutes: 15, machineSize: null });
    expect((await call(adminKey, "PUT", `/v1/projects/${PROJECT}/preview-settings`, { idleTimeoutMinutes: 0 })).status).toBe(400);
  });

  test("egress lux would refuse is refused, saying why", async () => {
    for (const entry of ["*.github.com", "10.0.0.0/33", "10.0.0/8", "not a host", "::1/129"]) {
      const res = await call(adminKey, "PUT", `/v1/projects/${PROJECT}/preview-settings`, { egress: ["github.com", entry] });
      expect(res.status).toBe(400);
      expect(JSON.stringify(await body(res))).toContain(entry);
    }
    const ok = ["*", "github.com", "10.0.0.5", "10.0.0.0/8", "2001:db8::/32", "2001:db8::1", "host_1.internal"];
    const res = await call(adminKey, "PUT", `/v1/projects/${PROJECT}/preview-settings`, { egress: ok });
    expect(res.status).toBe(200);
    expect((await body(res)).egress).toEqual(ok);
  });

  test("another organization sees no such project", async () => {
    expect((await call(otherKey, "GET", `/v1/projects/${PROJECT}/servers`)).status).toBe(404);
  });

  test("the database refuses a name lux would, whoever writes it", async () => {
    await expect((async () => owner`INSERT INTO project_servers (project_id, organization_id, name, port, command)
                       VALUES (${PROJECT}, ${ORG}, 'bad-', 1, 'x')`)()).rejects.toThrow();
    await expect((async () => owner`INSERT INTO project_servers (project_id, organization_id, name, port, command)
                       VALUES (${PROJECT}, ${ORG}, 'ok', 70000, 'x')`)()).rejects.toThrow();
  });
});

describe("a task's and a Run's servers", () => {
  test("are the orchestrator's, asked as the person, answered as it answered", async () => {
    asked.length = 0;
    const cases: Array<[string, string, unknown, number, string]> = [
      ["GET", "/v1/tasks/wi_1/servers", undefined, 200, "GET /internal/tasks/wi_1/servers"],
      ["GET", "/v1/runs/run_1/servers", undefined, 200, "GET /internal/runs/run_1/servers"],
      ["POST", "/v1/runs/run_1/servers", { recipe: "web" }, 201, "POST /internal/runs/run_1/servers"],
      ["POST", "/v1/runs/run_1/servers", { name: "vite", port: 5173 }, 201, "POST /internal/runs/run_1/servers"],
      ["POST", "/v1/runs/run_1/servers/web/stop", undefined, 201, "POST /internal/runs/run_1/servers/web/stop"],
      ["POST", "/v1/runs/run_1/servers/web/restart", undefined, 201, "POST /internal/runs/run_1/servers/web/restart"],
      ["POST", "/v1/runs/run_1/servers/web/start", undefined, 409, "POST /internal/runs/run_1/servers/web/start"],
      ["POST", "/v1/runs/run_1/servers/start-all", undefined, 201, "POST /internal/runs/run_1/servers/start-all"],
      ["POST", "/v1/runs/run_1/servers/stop-all", undefined, 201, "POST /internal/runs/run_1/servers/stop-all"],
      ["DELETE", "/v1/runs/run_1/servers/web", undefined, 204, "DELETE /internal/runs/run_1/servers/web"],
      ["GET", "/v1/runs/run_1/servers/web/log", undefined, 200, "GET /internal/runs/run_1/servers/web/log?tail=200"],
      ["GET", "/v1/runs/run_1/servers/web/log?tail=5", undefined, 200, "GET /internal/runs/run_1/servers/web/log?tail=5"],
      ["POST", "/v1/tasks/wi_1/preview", undefined, 201, "POST /internal/tasks/wi_1/preview"],
      ["DELETE", "/v1/tasks/wi_1/preview", undefined, 200, "DELETE /internal/tasks/wi_1/preview"],
    ];
    for (const [method, path, body, status, forwarded] of cases) {
      const res = await call(memberKey, method, path, body);
      expect(`${method} ${path} ${res.status}`).toBe(`${method} ${path} ${status}`);
      const last = asked.at(-1)!;
      expect(`${last.method} ${last.path}`).toBe(forwarded);
      expect(last.org).toBe(ORG);
      expect(last.actor).toStartWith("key_");
    }
    // The orchestrator's answer, as it came: a preview hidden behind an agent's run too.
    expect((await body(await call(memberKey, "GET", "/v1/tasks/wi_1/servers"))).preview).toEqual({ id: "run_p", luxRunId: "lux_p", state: "paused" });
    // lux's refusal, as it came.
    const refused = await call(memberKey, "POST", "/v1/runs/run_1/servers/web/start");
    expect((await body(refused)).error.code).toBe("not_running");
  });

  test("what cannot be a server is refused before the orchestrator is asked", async () => {
    asked.length = 0;
    for (const [method, path, body] of [
      ["POST", "/v1/runs/run_1/servers", { name: "Bad", port: 1 }],
      ["POST", "/v1/runs/run_1/servers", { name: "ok", port: 99999 }],
      ["POST", "/v1/runs/run_1/servers", { recipe: "web", port: 1 }],
      ["POST", "/v1/runs/run_1/servers", { name: "ok", port: 1, workdir: "../x" }],
      ["POST", "/v1/runs/run_1/servers", { name: "ok", port: 1, env: { LUX_TOKEN: "x" } }],
      ["POST", "/v1/runs/run_1/servers", { name: "ok", port: 1, env: { "1BAD": "x" } }],
      ["GET", "/v1/runs/run_1/servers/web/log?tail=0", undefined],
      ["POST", "/v1/tasks/wi_1/preview", { image: "x" }],
    ] as const) {
      expect((await call(memberKey, method, path, body)).status).toBe(400);
    }
    expect((await call(memberKey, "POST", "/v1/runs/run_1/servers/Not%20A%20Name/start")).status).toBe(404);
    expect(asked).toEqual([]);
  });

  test("need a key", async () => {
    expect((await call("dude_sk_nope", "GET", "/v1/tasks/wi_1/servers")).status).toBe(401);
  });
});

describe("migration 055", () => {
  test("a Run is an agent's unless it says otherwise, and a task has one live preview", async () => {
    await owner`INSERT INTO tasks (id, organization_id, project_id, number, title) VALUES ('wi_m', ${ORG}, ${PROJECT}, 1, 't')`;
    await owner`INSERT INTO runs (id, organization_id, project_id, task_id, attempt) VALUES ('run_a', ${ORG}, ${PROJECT}, 'wi_m', 1)`;
    const [row] = await owner`SELECT kind FROM runs WHERE id = 'run_a'`;
    expect(row.kind).toBe("agent");
    await owner`INSERT INTO runs (id, organization_id, project_id, task_id, attempt, kind) VALUES ('run_p1', ${ORG}, ${PROJECT}, 'wi_m', 1, 'preview')`;
    await expect((async () => owner`INSERT INTO runs (id, organization_id, project_id, task_id, attempt, kind)
                       VALUES ('run_p2', ${ORG}, ${PROJECT}, 'wi_m', 1, 'preview')`)()).rejects.toThrow(/runs_live_preview_idx/);
    await owner`UPDATE runs SET status = 'completed' WHERE id = 'run_p1'`;
    await owner`INSERT INTO runs (id, organization_id, project_id, task_id, attempt, kind) VALUES ('run_p2', ${ORG}, ${PROJECT}, 'wi_m', 1, 'preview')`;
    await expect((async () => owner`UPDATE runs SET kind = 'shell' WHERE id = 'run_a'`)()).rejects.toThrow();
  });

  test("recipes are the organization's alone", async () => {
    const seen = await app.begin(async (tx) => {
      await tx`SELECT set_config('app.organization_id', ${OTHER}, true)`;
      return tx`SELECT name FROM project_servers`;
    });
    expect(seen).toHaveLength(0);
  });
});

describe("settings from the config file", () => {
  // Resolves settings from a 0600 file and `vars` alone, runs `fn` with the
  // module pool unset so getPool() builds its own, then restores this suite's.
  async function withFile(text: string, vars: Record<string, string>, fn: () => Promise<void>) {
    const dir = mkdtempSync(join(tmpdir(), "dude-servers-"));
    const path = join(dir, "dude.toml");
    writeFileSync(path, text);
    chmodSync(path, 0o600);
    useConfig(Config.load({ env: { DUDE_CONFIG: path, ...vars }, defaultPath: join(dir, "absent.toml") }));
    setPool(null);
    try { await fn(); } finally {
      await closePool();
      setPool(app);
      useConfig(Config.load({ env: { ...process.env,
        DUDE_ORCHESTRATOR_URL: `http://localhost:${orchestratorServer.port}`, DUDE_ORCHESTRATOR_TOKEN: "svc" } }));
      rmSync(dir, { recursive: true, force: true });
    }
  }
  const who = async () => (await getPool()`SELECT current_database() AS db, current_user AS role`)[0];

  test("getPool connects to the file's database.url, and DATABASE_URL overrides it", async () => {
    const file = `[database]\nurl = "${databaseUrl("app", NAME)}"\n`;
    await withFile(file, {}, async () => {
      expect(await who()).toEqual({ db: NAME, role: "dude_app" });
    });
    await withFile(file, { DATABASE_URL: databaseUrl("owner", NAME) }, async () => {
      expect(await who()).toEqual({ db: NAME, role: new URL(OWNER_URL).username });
    });
  });

  test("startServer listens on the file's port 0 and answers health from the file's database", async () => {
    await withFile(`[backend]\nport = 0\n[database]\nurl = "${databaseUrl("app", NAME)}"\n`, {}, async () => {
      const server = startServer();
      try {
        // Port 0 is an ephemeral port, never the 3000 default.
        expect(server.port).toBeGreaterThan(0);
        expect(server.port).not.toBe(3000);
        const res = await fetch(`http://127.0.0.1:${server.port}/health`);
        expect(await res.json()).toEqual({ status: "ok" });
      } finally {
        await server.stop(true);
      }
    });
  });
});
