/**
 * Feedback drafts through the public API, against a database of its own:
 * a draft is its author's alone, a submitted item is everyone's in the
 * organization, a code anchor is read from the Run's stored diff, and an
 * earlier attempt or a done task takes none.
 *
 * Requires DATABASE_URL: a role that can create databases (the owner).
 */

import { afterAll, beforeAll, describe, expect, test } from "bun:test";
import { SQL } from "bun";
import { join } from "node:path";
import { closePool, setPool } from "../src/db/client.ts";
import { buildRouter } from "../src/index.ts";
import { Config, useConfig } from "../src/config.ts";
import type { Router } from "../src/api/router.ts";
import { createApiKey } from "../src/api/auth.ts";
import { excerptOf } from "../src/api/routes/feedback.ts";

const OWNER_URL = process.env.DATABASE_URL ?? "postgres://dude:dude@localhost:5433/dude";
const ROOT = join(import.meta.dir, "../../..");
const NAME = `dude_feedback_test_${Bun.randomUUIDv7("hex").slice(-12)}`;
const ORG = "org_fb";
const OTHER = "org_fb_other";

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
let ana: { key: string; personId: string };
let bo: { key: string; personId: string };
let cy: { key: string; personId: string };
let orchestrator: ReturnType<typeof Bun.serve>;
const forwarded: Array<{ path: string; body: unknown; person: string | null }> = [];

type Json = any; // eslint-disable-line @typescript-eslint/no-explicit-any

function call(key: string, method: string, path: string, body?: unknown): Promise<Response> {
  return router.handle(new Request(`http://dude.test${path}`, {
    method,
    headers: { authorization: `Bearer ${key}`, "content-type": "application/json" },
    ...(body === undefined ? {} : { body: typeof body === "string" ? body : JSON.stringify(body) }),
  }));
}

const DIFF = [{
  path: "src/vat.ts", status: "M", additions: 2, deletions: 1,
  hunks: [{ header: "@@ -10,3 +10,4 @@", lines: [
    { kind: " ", old: 10, new: 10, text: "export function vat(n) {" },
    { kind: "-", old: 11, new: null, text: "  return n * 0.2;" },
    { kind: "+", old: null, new: 11, text: "  const rate = 0.23;" },
    { kind: "+", old: null, new: 12, text: "  return n * rate;" },
    { kind: " ", old: 12, new: 13, text: "}" },
  ] }],
}];

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
  await owner`INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES
    ('prj_fb', ${ORG}, 'Billing', 'billing', 'BILL'), ('prj_fb_o', ${OTHER}, 'Other', 'other', 'OTH')`;
  await owner`INSERT INTO tasks (id, organization_id, project_id, number, title, status) VALUES
    ('wi_fb', ${ORG}, 'prj_fb', 1, 'Fix VAT', 'running'),
    ('wi_fb2', ${ORG}, 'prj_fb', 2, 'Another', 'running'),
    ('wi_done', ${ORG}, 'prj_fb', 3, 'Done one', 'done'),
    ('wi_new', ${ORG}, 'prj_fb', 4, 'Not started', 'queued'),
    ('wi_o', ${OTHER}, 'prj_fb_o', 1, 'Theirs', 'running')`;
  await owner`INSERT INTO runs (id, organization_id, project_id, task_id, attempt, status, phase, role, kind, base_shas, heads) VALUES
    ('run_old', ${ORG}, 'prj_fb', 'wi_fb', 1, 'completed', 'implement', 'implementer', 'agent', '{"api":"aaa"}', '{"api":{"sha":"a1"}}'),
    ('run_live', ${ORG}, 'prj_fb', 'wi_fb', 2, 'running', 'implement', 'implementer', 'agent', '{"api":"bbb"}', '{}'),
    ('run_pub', ${ORG}, 'prj_fb', 'wi_fb', 2, 'completed', 'fix', 'implementer', 'agent', '{"api":"bbb"}', '{"api":{"sha":"c3"}}'),
    ('run_other_task', ${ORG}, 'prj_fb', 'wi_fb2', 1, 'running', 'implement', 'implementer', 'agent', '{"api":"x"}', '{}'),
    ('run_o', ${OTHER}, 'prj_fb_o', 'wi_o', 1, 'running', 'implement', 'implementer', 'agent', '{"api":"x"}', '{}')`;
  for (const [run, org] of [["run_old", ORG], ["run_live", ORG], ["run_pub", ORG], ["run_other_task", ORG], ["run_o", OTHER]] as const) {
    await owner`INSERT INTO run_diffs (run_id, organization_id, base, files, checksum)
      VALUES (${run}, ${org}, 'bbb', ${DIFF}::jsonb, ${"sum-" + run})`;
  }
  app = new SQL(databaseUrl("app", NAME));
  setPool(app);
  ana = await createApiKey({ organizationId: ORG, name: "Ana" });
  bo = await createApiKey({ organizationId: ORG, name: "Bo" });
  cy = await createApiKey({ organizationId: OTHER, name: "Cy" });
  await owner`INSERT INTO attachments (id, organization_id, task_id, uploaded_by, name, content_type, width, height, bytes, sha256,
      object_key, original_content_type, original_width, original_height, original_bytes, original_key) VALUES
    ('att_ana', ${ORG}, 'wi_fb', ${ana.personId}, 'a.png', 'image/png', 1, 1, 1, 'x', 'k1', 'image/png', 1, 1, 1, 'k2'),
    ('att_bo', ${ORG}, 'wi_fb', ${bo.personId}, 'b.png', 'image/png', 1, 1, 1, 'x', 'k3', 'image/png', 1, 1, 1, 'k4')`;
  orchestrator = Bun.serve({ port: 0, async fetch(request) {
    forwarded.push({ path: new URL(request.url).pathname, body: await request.json().catch(() => null),
      person: request.headers.get("x-dude-person") });
    return Response.json({ batchId: "fbt_x", itemIds: [], routedTo: "workflow" }, { status: 201 });
  } });
  useConfig(Config.load({ env: { ...process.env, DUDE_ORCHESTRATOR_URL: `http://localhost:${orchestrator.port}`, DUDE_ORCHESTRATOR_TOKEN: "t" } }));
  router = buildRouter("");
});

afterAll(async () => {
  orchestrator?.stop(true);
  useConfig(null);
  await closePool(app);
  await owner?.end();
  await admin?.unsafe(`DROP DATABASE IF EXISTS "${NAME}" WITH (FORCE)`);
  await admin?.end();
});

async function draft(key: string, task: string, body: unknown): Promise<Response> {
  return call(key, "POST", `/v1/tasks/${task}/feedback/drafts`, body);
}

async function listed(key: string, task: string, query = ""): Promise<Json> {
  const res = await call(key, "GET", `/v1/tasks/${task}/feedback${query}`);
  expect(res.status).toBe(200);
  return res.json();
}

describe("a draft is its author's alone", () => {
  test("another person in the organization neither lists, changes nor removes it", async () => {
    const res = await draft(ana.key, "wi_fb", { body: "The VAT rate is wrong for Portugal", anchor: { kind: "general" } });
    expect(res.status).toBe(201);
    const item = await res.json() as Json;
    expect(item).toMatchObject({ status: "draft", attempt: 2, batchId: null, body: "The VAT rate is wrong for Portugal",
      author: { id: ana.personId, name: "Ana" }, anchor: { kind: "general" }, history: [] });

    expect((await listed(ana.key, "wi_fb")).feedback.map((f: Json) => f.id)).toContain(item.id);
    const seen = await listed(bo.key, "wi_fb");
    expect(seen.feedback.map((f: Json) => f.id)).not.toContain(item.id);
    expect(JSON.stringify(seen)).not.toContain("Portugal");

    expect((await call(bo.key, "PATCH", `/v1/feedback/${item.id}`, { body: "hijack", anchor: { kind: "general" } })).status).toBe(404);
    expect((await call(bo.key, "DELETE", `/v1/feedback/${item.id}`)).status).toBe(404);
    const [kept] = await owner`SELECT body FROM feedback_items WHERE id = ${item.id}`;
    expect(kept.body).toBe("The VAT rate is wrong for Portugal");

    const edited = await call(ana.key, "PATCH", `/v1/feedback/${item.id}`, { body: "VAT for PT is 23%", anchor: { kind: "general" } });
    expect(edited.status).toBe(200);
    expect(((await edited.json()) as Json).body).toBe("VAT for PT is 23%");
    expect((await call(ana.key, "DELETE", `/v1/feedback/${item.id}`)).status).toBe(204);
    expect((await owner`SELECT 1 FROM feedback_items WHERE id = ${item.id}`).length).toBe(0);
  });

  test("drafting writes nothing to the task's ledger", async () => {
    const before = (await owner`SELECT count(*)::int AS n FROM events WHERE task_id = 'wi_fb'`)[0].n;
    const res = await draft(ana.key, "wi_fb", { body: "secret-draft-words", anchor: { kind: "general" } });
    const item = await res.json() as Json;
    await call(ana.key, "PATCH", `/v1/feedback/${item.id}`, { body: "secret-draft-words 2", anchor: { kind: "general" } });
    await call(ana.key, "DELETE", `/v1/feedback/${item.id}`);
    const after = (await owner`SELECT count(*)::int AS n FROM events WHERE task_id = 'wi_fb'`)[0].n;
    expect(after).toBe(before);
    const leaked = await owner`SELECT 1 FROM events WHERE payload::text LIKE '%secret-draft-words%' OR payload::text LIKE ${"%" + item.id + "%"}`;
    expect(leaked.length).toBe(0);
  });

  test("a submitted item is everyone's to read, and nobody's to edit or delete", async () => {
    const item = await (await draft(ana.key, "wi_fb", { body: "submitted words", anchor: { kind: "general" } })).json() as Json;
    await owner`INSERT INTO feedback_batches (id, organization_id, task_id, attempt, author_person_id, submission_key)
      VALUES ('fbt_1', ${ORG}, 'wi_fb', 2, ${ana.personId}, 'k1')`;
    await owner`UPDATE feedback_items SET status = 'open', batch_id = 'fbt_1', submitted_at = now() WHERE id = ${item.id}`;
    const seen = await listed(bo.key, "wi_fb");
    expect(seen.feedback.find((f: Json) => f.id === item.id)).toMatchObject({ status: "open", body: "submitted words", batchId: "fbt_1" });
    expect(seen.blocking).toBeGreaterThanOrEqual(1);
    for (const key of [ana.key, bo.key]) {
      expect((await call(key, "PATCH", `/v1/feedback/${item.id}`, { body: "changed", anchor: { kind: "general" } })).status).toBe(404);
      expect((await call(key, "DELETE", `/v1/feedback/${item.id}`)).status).toBe(404);
    }
    expect((await owner`SELECT body FROM feedback_items WHERE id = ${item.id}`)[0].body).toBe("submitted words");
  });

  test("another organization sees nothing and cannot draft on the task", async () => {
    const res = await call(cy.key, "GET", "/v1/tasks/wi_fb/feedback");
    expect(res.status).toBe(404);
    expect((await draft(cy.key, "wi_fb", { body: "x", anchor: { kind: "general" } })).status).toBe(404);
    const mine = await (await draft(ana.key, "wi_fb", { body: "not for cy", anchor: { kind: "general" } })).json() as Json;
    expect((await call(cy.key, "PATCH", `/v1/feedback/${mine.id}`, { body: "x", anchor: { kind: "general" } })).status).toBe(404);
    expect((await call(cy.key, "DELETE", `/v1/feedback/${mine.id}`)).status).toBe(404);
  });
});

describe("attempts and ended tasks", () => {
  test("an earlier attempt is listed read-only, and drafts go on the current one", async () => {
    await owner`INSERT INTO feedback_items (id, organization_id, task_id, attempt, author_person_id, body, anchor_kind)
      VALUES ('fbi_old_draft', ${ORG}, 'wi_fb', 1, ${ana.personId}, 'old attempt', 'general')`;
    const old = await listed(ana.key, "wi_fb", "?attempt=1");
    expect(old.feedback.map((f: Json) => f.id)).toEqual(["fbi_old_draft"]);
    expect((await listed(ana.key, "wi_fb")).feedback.map((f: Json) => f.id)).not.toContain("fbi_old_draft");
    expect((await call(ana.key, "PATCH", "/v1/feedback/fbi_old_draft", { body: "x", anchor: { kind: "general" } })).status).toBe(409);
    expect((await call(ana.key, "DELETE", "/v1/feedback/fbi_old_draft")).status).toBe(409);
  });

  test("a task with no Run yet takes drafts on attempt 1; a done task takes none", async () => {
    const res = await draft(ana.key, "wi_new", { body: "before any work", anchor: { kind: "general" } });
    expect(res.status).toBe(201);
    expect(((await res.json()) as Json).attempt).toBe(1);
    const done = await draft(ana.key, "wi_done", { body: "too late", anchor: { kind: "general" } });
    expect(done.status).toBe(409);
    expect(((await done.json()) as Json).error.message).toContain("follow-up");
  });
});

describe("what is refused", () => {
  test("malformed bodies, attempts and sizes are 4xx, never a 500", async () => {
    expect((await call(ana.key, "GET", "/v1/tasks/wi_fb/feedback?attempt=abc")).status).toBe(400);
    expect((await call(ana.key, "GET", "/v1/tasks/wi_fb/feedback?attempt=0")).status).toBe(400);
    for (const body of ["not json", "null", "[]", { body: "" }, { body: "  " }, { body: 5, anchor: { kind: "general" } },
      { body: "x", anchor: { kind: "elsewhere" } }, { body: "x", anchor: { kind: "general", runId: "run_live" } },
      { body: "x", anchor: { kind: "general" }, status: "resolved" }, { body: "x".repeat(16_001), anchor: { kind: "general" } }]) {
      const res = await draft(ana.key, "wi_fb", body);
      expect(res.status).toBe(400);
    }
    const huge = await draft(ana.key, "wi_fb", { body: "x".repeat(200_000), anchor: { kind: "general" } });
    expect(huge.status).toBe(413);
  });

  test("preview anchors are capped, http(s) only, and name only the person's own upload on this task", async () => {
    const ok = await draft(ana.key, "wi_fb", { body: "button overlaps", anchor: { kind: "preview", url: "https://web-bill-1.preview.test/cart",
      viewport: { width: 390, height: 844 }, selector: "#pay", rect: { x: 1, y: 2, width: 3, height: 4 }, text: "Pay",
      component: { name: "PayButton", file: "web/src/Pay.tsx", line: 12 }, attachmentId: "att_ana" } });
    expect(ok.status).toBe(201);
    expect(((await ok.json()) as Json).anchor).toMatchObject({ kind: "preview", selector: "#pay", attachmentId: "att_ana" });
    const bad = [
      { kind: "preview", url: "javascript:alert(1)" },
      { kind: "preview", url: "https://x.test", text: "t".repeat(4_001) },
      { kind: "preview", url: "https://x.test", html: "h".repeat(16_001) },
      { kind: "preview", url: "https://x.test", screenshot: "data:..." },
    ];
    for (const anchor of bad) expect((await draft(ana.key, "wi_fb", { body: "x", anchor })).status).toBe(400);
    expect((await draft(ana.key, "wi_fb", { body: "x", anchor: { kind: "preview", url: "https://x.test", attachmentId: "att_bo" } })).status).toBe(400);
    expect((await draft(ana.key, "wi_fb2", { body: "x", anchor: { kind: "preview", url: "https://x.test", attachmentId: "att_ana" } })).status).toBe(400);
  });

  test("a person has at most 50 drafts on a task", async () => {
    const [{ n }] = await owner`SELECT count(*)::int AS n FROM feedback_items WHERE task_id = 'wi_fb2' AND author_person_id = ${bo.personId}`;
    for (let i = n; i < 50; i++) expect((await draft(bo.key, "wi_fb2", { body: `d${i}`, anchor: { kind: "general" } })).status).toBe(201);
    expect((await draft(bo.key, "wi_fb2", { body: "51st", anchor: { kind: "general" } })).status).toBe(409);
    // Ana's own count is hers alone.
    expect((await draft(ana.key, "wi_fb2", { body: "mine", anchor: { kind: "general" } })).status).toBe(201);
  });
});

describe("what changes agents' work goes to the orchestrator", () => {
  test("submit, reply, resolve and reopen are validated here and forwarded with the person", async () => {
    forwarded.length = 0;
    expect((await call(ana.key, "POST", "/v1/tasks/wi_fb/feedback/submit", { draftIds: ["fbi_1", "fbi_2"], submissionKey: "k-1" })).status).toBe(201);
    expect(forwarded).toEqual([{ path: "/internal/tasks/wi_fb/feedback/submit", person: ana.personId,
      body: { draftIds: ["fbi_1", "fbi_2"], submissionKey: "k-1" } }]);
    for (const bad of [{ draftIds: [], submissionKey: "k" }, { draftIds: ["a", "a"], submissionKey: "k" }, { draftIds: "fbi_1", submissionKey: "k" },
      { draftIds: [1], submissionKey: "k" }, { draftIds: ["a"] }, { draftIds: ["a"], submissionKey: " " }, null]) {
      expect((await call(ana.key, "POST", "/v1/tasks/wi_fb/feedback/submit", bad)).status).toBe(400);
    }
    expect((await call(ana.key, "POST", "/v1/feedback/fbi_1/reply", {})).status).toBe(400);
    expect(forwarded.length).toBe(1);
    expect((await call(bo.key, "POST", "/v1/feedback/fbi_1/reply", { body: "no" })).status).toBe(201);
    expect((await call(bo.key, "POST", "/v1/feedback/fbi_1/resolve", {})).status).toBe(201);
    expect((await call(bo.key, "POST", "/v1/feedback/fbi_1/reopen", { body: "again" })).status).toBe(201);
    expect(forwarded.slice(1).map((f) => [f.path, f.person])).toEqual([
      ["/internal/feedback/fbi_1/reply", bo.personId], ["/internal/feedback/fbi_1/resolve", bo.personId],
      ["/internal/feedback/fbi_1/reopen", bo.personId]]);
  });
});

describe("a code anchor is pinned to the Run's stored diff", () => {
  test("its excerpt, base, checksum and head come from the snapshot, not the client", async () => {
    const res = await draft(ana.key, "wi_fb", { body: "use the rate table", anchor: {
      kind: "code", runId: "run_live", path: "src/vat.ts", side: "new", startLine: 11, endLine: 12, diffChecksum: "sum-run_live" } });
    expect(res.status).toBe(201);
    const { anchor } = await res.json() as Json;
    expect(anchor).toEqual({ kind: "code", runId: "run_live", repo: "api", path: "src/vat.ts", side: "new", startLine: 11, endLine: 12,
      baseSha: "bbb", headSha: null, diffChecksum: "sum-run_live", excerpt: [
        { kind: "+", old: null, new: 11, text: "  const rate = 0.23;" },
        { kind: "+", old: null, new: 12, text: "  return n * rate;" }] });
    const published = await (await draft(ana.key, "wi_fb", { body: "old line", anchor: {
      kind: "code", runId: "run_pub", path: "src/vat.ts", side: "old", startLine: 11, endLine: 11 } })).json() as Json;
    expect(published.anchor).toMatchObject({ headSha: "c3", excerpt: [{ kind: "-", old: 11, new: null, text: "  return n * 0.2;" }] });
  });

  test("a Run of another task, organization or attempt, a stale snapshot or lines not in it are refused", async () => {
    const code = (over: Record<string, unknown>) => ({ body: "x", anchor: {
      kind: "code", runId: "run_live", path: "src/vat.ts", side: "new", startLine: 11, endLine: 12, ...over } });
    expect((await draft(ana.key, "wi_fb", code({ runId: "run_other_task" }))).status).toBe(400);
    expect((await draft(ana.key, "wi_fb", code({ runId: "run_o" }))).status).toBe(400);
    expect((await draft(ana.key, "wi_fb", code({ runId: "run_old" }))).status).toBe(409);
    expect((await draft(ana.key, "wi_fb", code({ diffChecksum: "stale" }))).status).toBe(409);
    expect((await draft(ana.key, "wi_fb", code({ path: "src/nope.ts" }))).status).toBe(400);
    expect((await draft(ana.key, "wi_fb", code({ startLine: 11, endLine: 40 }))).status).toBe(400);
    expect((await draft(ana.key, "wi_fb", code({ startLine: 12, endLine: 11 }))).status).toBe(400);
    expect((await draft(ana.key, "wi_fb", code({ side: "old", startLine: 12, endLine: 13 }))).status).toBe(400);
    expect((await draft(ana.key, "wi_fb", code({ startLine: 1, endLine: 500 }))).status).toBe(400);
    expect((await draft(ana.key, "wi_fb", code({ excerpt: [{ text: "forged" }] }))).status).toBe(400);
  });

  test("a range is one hunk's lines on one side", () => {
    const file = DIFF[0] as Json;
    expect(excerptOf(file, "new", 10, 13)?.map((l) => l.text)).toEqual([
      "export function vat(n) {", "  return n * 0.2;", "  const rate = 0.23;", "  return n * rate;", "}"]);
    expect(excerptOf(file, "old", 10, 12)?.length).toBe(5);
    expect(excerptOf(file, "new", 9, 10)).toBeNull();
  });
});
