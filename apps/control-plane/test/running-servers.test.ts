/**
 * A project's running servers (`GET /v1/projects/:id/servers/running`):
 * the orchestrator's live Runs, each server with its feedback counted here.
 * Two people's feedback is counted once each; drafts are nobody's count;
 * another organisation's project is a 404 that never reaches the orchestrator.
 *
 * Requires DATABASE_URL: a role that can create databases (the owner).
 */

import { afterAll, beforeAll, expect, test } from "bun:test";
import { SQL } from "bun";
import { closePool, setPool } from "../src/db/client.ts";
import { buildRouter } from "../src/index.ts";
import { Config, useConfig } from "../src/config.ts";
import type { Router } from "../src/api/router.ts";
import { createApiKey } from "../src/api/auth.ts";

const OWNER_URL = process.env.DATABASE_URL ?? "postgres://dude:dude@localhost:5433/dude";
const NAME = `dude_running_servers_${Bun.randomUUIDv7("hex").slice(-12)}`;
const dbUrl = (app = false) => {
  const u = new URL(OWNER_URL);
  u.pathname = `/${NAME}`;
  if (app) { u.username = "dude_app"; u.password = "dude_app"; }
  return u.toString();
};

type Person = { key: string; personId: string };
// The API's JSON, read as the tests assert on it.
type Json = any; // eslint-disable-line @typescript-eslint/no-explicit-any
let admin: SQL, owner: SQL, app: SQL, router: Router;
let ana: Person, bo: Person, cy: Person;
let orchestratorServer: ReturnType<typeof Bun.serve>;
const asked: string[] = [];

const PREVIEW_URL = "https://web-pv.preview.test";
const TASK_URL = "https://web-wi.preview.test";
const runs = [
  { id: "run_rs_pv", kind: "preview", task: null, branch: "feat/x", state: "running", startedAt: null, startedBy: null,
    servers: [{ name: "web", port: 5173, url: PREVIEW_URL, state: "ready", since: null, readySince: null },
      { name: "api", port: 8080, url: "https://api-pv.preview.test", state: "ready", since: null, readySince: null }] },
  { id: "run_rs_task", kind: "task", task: { id: "wi_rs", key: "RS-1", title: "Fix it" }, branch: "dude/RS-1", state: "running",
    startedAt: null, startedBy: null,
    servers: [{ name: "web", port: 5173, url: TASK_URL, state: "ready", since: null, readySince: null }] },
];

const call = (key: string, path: string) => router.handle(new Request(`http://dude.test${path}`, {
  headers: { authorization: `Bearer ${key}` } }));

beforeAll(async () => {
  admin = new SQL(OWNER_URL);
  await admin.unsafe(`CREATE DATABASE "${NAME}"`);
  const migrate = Bun.spawnSync(["bun", "run", "apps/control-plane/src/db/migrate.ts"], { cwd: `${import.meta.dir}/../../..`,
    env: { ...process.env, DATABASE_URL: dbUrl() } });
  if (migrate.exitCode) throw new Error(migrate.stderr.toString());
  owner = new SQL(dbUrl()); app = new SQL(dbUrl(true)); setPool(app);
  for (const org of ["org_rs", "org_rs_other"]) await owner`INSERT INTO organizations (id, name, slug) VALUES (${org}, ${org}, ${org})`;
  await owner`INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES
    ('prj_rs', 'org_rs', 'Web', 'web', 'RS'), ('prj_rs_other', 'org_rs_other', 'Other', 'other', 'OTH')`;
  ana = await createApiKey({ organizationId: "org_rs", name: "Ana" });
  bo = await createApiKey({ organizationId: "org_rs", name: "Bo" });
  cy = await createApiKey({ organizationId: "org_rs_other", name: "Cy" });

  await owner`INSERT INTO runs (id, organization_id, project_id, preview_branch, attempt, status, kind, wakeable)
    VALUES ('run_rs_pv', 'org_rs', 'prj_rs', 'feat/x', 1, 'running', 'preview', true)`;
  for (const [name, url] of [["web", PREVIEW_URL], ["api", "https://api-pv.preview.test"]] as const) {
    await owner`INSERT INTO preview_servers (run_id, organization_id, name, lux_server_id, hostname, url)
      VALUES ('run_rs_pv', 'org_rs', ${name}, ${`lux_${name}`}, ${name}, ${url})`;
  }
  const feedback = async (id: string, author: Person, status: "open" | "resolved") => {
    await owner`INSERT INTO preview_feedback (id, organization_id, project_id, preview_run_id, preview_server_name, author_person_id,
      submission_key, body, anchor, status, resolved_at, resolved_by)
      VALUES (${id}, 'org_rs', 'prj_rs', 'run_rs_pv', 'web', ${author.personId}, ${id}, 'words',
        ${{ kind: "preview", url: `${PREVIEW_URL}/cart` }}::jsonb, ${status},
        ${status === "resolved" ? new Date() : null}, ${status === "resolved" ? author.personId : null})`;
  };
  await feedback("fbi_rs_1", ana, "open");
  await feedback("fbi_rs_2", ana, "resolved");
  await feedback("fbi_rs_3", bo, "open");

  await owner`INSERT INTO tasks (id, organization_id, project_id, number, title) VALUES ('wi_rs', 'org_rs', 'prj_rs', 1, 'Fix it')`;
  await owner`INSERT INTO feedback_batches (id, organization_id, task_id, attempt, author_person_id, submission_key)
    VALUES ('fbb_rs_a', 'org_rs', 'wi_rs', 1, ${ana.personId}, 'a'), ('fbb_rs_b', 'org_rs', 'wi_rs', 1, ${bo.personId}, 'b')`;
  const item = async (id: string, author: Person, status: string, url: string, batch: string | null) => {
    await owner`INSERT INTO feedback_items (id, organization_id, task_id, attempt, batch_id, author_person_id, body, anchor_kind, anchor,
      status, submitted_at, resolved_at, resolved_verified_at)
      VALUES (${id}, 'org_rs', 'wi_rs', 1, ${batch}, ${author.personId}, 'words', 'preview', ${{ kind: "preview", url }}::jsonb,
        ${status}, ${batch ? new Date() : null}, ${status === "resolved" ? new Date() : null}, ${status === "resolved" ? new Date() : null})`;
  };
  await item("fbi_rs_t1", ana, "open", `${TASK_URL}/chat`, "fbb_rs_a");
  await item("fbi_rs_t2", bo, "delivered", `${TASK_URL}/chat?x=1`, "fbb_rs_b");
  await item("fbi_rs_t3", bo, "resolved", `${TASK_URL}/`, "fbb_rs_b");
  await item("fbi_rs_t4", ana, "replied", `${TASK_URL}/chat`, "fbb_rs_a");
  // Another server's page, and a draft: neither counts on this row.
  await item("fbi_rs_t5", ana, "open", "https://elsewhere.preview.test/chat", "fbb_rs_a");
  await item("fbi_rs_t6", ana, "draft", `${TASK_URL}/chat`, null);
  await item("fbi_rs_t7", bo, "draft", `${TASK_URL}/cart`, null);
  const dee = await createApiKey({ organizationId: "org_rs", name: "Dee" });
  await item("fbi_rs_t8", dee, "draft", `${TASK_URL}/chat`, null);

  orchestratorServer = Bun.serve({ port: 0, fetch(req) {
    const url = new URL(req.url);
    asked.push(`${req.headers.get("x-dude-organization")} ${url.pathname}`);
    return Response.json({ runs });
  } });
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

test("lists the orchestrator's running Runs with each server's feedback counted, two people once each", async () => {
  const res = await call(bo.key, "/v1/projects/prj_rs/servers/running");
  expect(res.status).toBe(200);
  const out: Json = await res.json();
  expect(asked).toContain("org_rs /internal/projects/prj_rs/servers/running");
  const preview = out.runs.find((r: Json) => r.id === "run_rs_pv");
  expect(preview.servers.find((s: Json) => s.name === "web").feedback).toEqual({ open: 2, resolved: 1, authors: 2 });
  expect(preview.servers.find((s: Json) => s.name === "api").feedback).toEqual({ open: 0, resolved: 0, authors: 0 });
  const task = out.runs.find((r: Json) => r.id === "run_rs_task");
  expect(task.task).toEqual({ id: "wi_rs", key: "RS-1", title: "Fix it" });
  // open + delivered open; resolved + replied done; the other origin's and the draft not at all.
  expect(task.servers[0].feedback).toEqual({ open: 2, resolved: 2, authors: 2 });
});

test("another organisation's project and an unknown one are 404s that never reach the orchestrator", async () => {
  const before = asked.length;
  expect((await call(cy.key, "/v1/projects/prj_rs/servers/running")).status).toBe(404);
  expect((await call(ana.key, "/v1/projects/prj_rs_other/servers/running")).status).toBe(404);
  expect((await call(ana.key, "/v1/projects/prj_nope/servers/running")).status).toBe(404);
  expect(asked.length).toBe(before);
});

test("one server's task feedback: everyone's submitted items on its origin, and only the caller's own drafts", async () => {
  const read = async (who: Person) => {
    const res = await call(who.key, `/v1/tasks/wi_rs/feedback?origin=${encodeURIComponent(TASK_URL)}`);
    expect(res.status).toBe(200);
    return ((await res.json()) as Json).feedback.map((f: Json) => `${f.id}:${f.status}`);
  };
  const submitted = ["fbi_rs_t1:open", "fbi_rs_t2:delivered", "fbi_rs_t3:resolved", "fbi_rs_t4:replied"];
  const forAna = await read(ana);
  const forBo = await read(bo);
  expect([...forAna].sort()).toEqual([...submitted, "fbi_rs_t6:draft"].sort());
  expect([...forBo].sort()).toEqual([...submitted, "fbi_rs_t7:draft"].sort());
  // Never the other person's draft, never another origin's page.
  expect(forBo.some((s: string) => s.startsWith("fbi_rs_t6"))).toBe(false);
  expect(forAna.some((s: string) => s.startsWith("fbi_rs_t7") || s.startsWith("fbi_rs_t5"))).toBe(false);
  expect((await call(ana.key, "/v1/tasks/wi_rs/feedback?origin=https%3A%2F%2Fweb-wi.preview.test%2Fchat")).status).toBe(400);
  expect((await call(cy.key, `/v1/tasks/wi_rs/feedback?origin=${encodeURIComponent(TASK_URL)}`)).status).toBe(404);
});
