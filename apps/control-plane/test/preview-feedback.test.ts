import { afterAll, beforeAll, expect, test } from "bun:test";
import { SQL } from "bun";
import { closePool, setPool, withOrg } from "../src/db/client.ts";
import { buildRouter } from "../src/index.ts";
import { createApiKey } from "../src/api/auth.ts";
import type { Router } from "../src/api/router.ts";
import { Config, useConfig } from "../src/config.ts";
import { sweepAttachments } from "../src/sweeper.ts";

const URL = process.env.DATABASE_URL ?? "postgres://dude:dude@localhost:5433/dude";
const NAME = `dude_preview_feedback_${Bun.randomUUIDv7("hex").slice(-12)}`;
const dbUrl = (app = false) => {
  const u = new globalThis.URL(URL);
  u.pathname = `/${NAME}`;
  if (app) { u.username = "dude_app"; u.password = "dude_app"; }
  return u.toString();
};
let admin: SQL, owner: SQL, app: SQL, router: Router;
// Match the existing API fixtures: assertions below validate these JSON shapes.
type JsonResponse = Omit<Response, "json"> & { json(): Promise<any> };
let ana: { key: string; personId: string }, bo: { key: string; personId: string }, other: { key: string; personId: string };
const objects = new Map<string, Uint8Array>();
let down = false;
let putGate: (() => Promise<void>) | null = null;
const s3 = Bun.serve({ port: 0, async fetch(req) {
  const key = decodeURIComponent(new globalThis.URL(req.url).pathname).replace(/^\/[^/]+\//, "");
  if (down) return new Response("<Error><Code>ServiceUnavailable</Code></Error>", { status: 503 });
  if (req.method === "PUT") {
    objects.set(key, new Uint8Array(await req.arrayBuffer()));
    if (putGate) await putGate();
    return new Response(null);
  }
  if (req.method === "DELETE") { objects.delete(key); return new Response(null, { status: 204 }); }
  return objects.has(key) ? new Response(objects.get(key)) : new Response(null, { status: 404 });
} });

function call(key: string, method: string, path: string, body?: unknown) {
  return router.handle(new Request(`http://dude.test${path}`, { method,
    headers: { authorization: `Bearer ${key}`, "content-type": "application/json" },
    ...(body === undefined ? {} : { body: JSON.stringify(body) }) })) as Promise<JsonResponse>;
}
async function preview(id: string, org = "org_pf", project = "prj_pf") {
  await owner`INSERT INTO runs (id, organization_id, project_id, preview_branch, attempt, status, kind, wakeable)
    VALUES (${id}, ${org}, ${project}, ${id}, 1, 'paused', 'preview', true)`;
  for (const name of ["web", "api"]) await owner`INSERT INTO preview_servers (run_id, organization_id, name, lux_server_id, hostname, url)
    VALUES (${id}, ${org}, ${name}, ${id + name}, ${id + name}, ${`https://${id}-${name}.preview.test`})`;
}
const input = (id: string, key: string = crypto.randomUUID(), extra = {}) => ({ body: "The button overlaps", submissionKey: key,
  anchor: { kind: "preview", url: `https://${id}-web.preview.test/cart`, selector: "#pay", component: { name: "PayButton" }, ...extra } });
const submit = (id: string, body = input(id), key?: string) => call(key ?? ana.key, "POST", `/v1/branch-previews/${id}/servers/web/feedback`, body);
async function upload(id: string, key = ana.key, server = "web") {
  const bytes = new Uint8Array(33);
  bytes.set([0x89, 0x50, 0x4e, 0x47, 13, 10, 26, 10, 0, 0, 0, 13, 73, 72, 68, 82]);
  new DataView(bytes.buffer).setUint32(16, 10); new DataView(bytes.buffer).setUint32(20, 10);
  const form = new FormData();
  for (const part of ["original", "delivered"]) {
    const variant = bytes.slice(); variant[32] = part === "original" ? 1 : 2;
    form.set(part, new File([variant], "shot.png", { type: "image/png" })); form.set(`${part}Type`, "image/png");
  }
  return router.handle(new Request(`http://dude.test/v1/branch-previews/${id}/servers/${server}/attachments`, {
    method: "POST", headers: { authorization: `Bearer ${key}` }, body: form })) as Promise<JsonResponse>;
}
beforeAll(async () => {
  admin = new SQL(URL); await admin.unsafe(`CREATE DATABASE "${NAME}"`);
  const migrate = Bun.spawnSync(["bun", "run", "apps/control-plane/src/db/migrate.ts"], { cwd: `${import.meta.dir}/../../..`,
    env: { ...process.env, DATABASE_URL: dbUrl() } });
  if (migrate.exitCode) throw new Error(migrate.stderr.toString());
  owner = new SQL(dbUrl()); app = new SQL(dbUrl(true)); setPool(app);
  for (const org of ["org_pf", "org_pf_other"]) {
    await owner`INSERT INTO organizations (id, name, slug) VALUES (${org}, 'Acme', ${org})`;
  }
  await owner`INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES
    ('prj_pf', 'org_pf', 'Acme', 'acme', 'ACME'), ('prj_pf_two', 'org_pf', 'Second', 'second', 'SEC'),
    ('prj_pf_other', 'org_pf_other', 'Other', 'other', 'OTH')`;
  ana = await createApiKey({ organizationId: "org_pf", name: "Ana" });
  bo = await createApiKey({ organizationId: "org_pf", name: "Bo" });
  other = await createApiKey({ organizationId: "org_pf_other", name: "Cy" });
  useConfig(Config.load({ env: { ...process.env, DUDE_S3_BUCKET: "preview-test", DUDE_S3_ENDPOINT: `http://localhost:${s3.port}`,
    DUDE_S3_ACCESS_KEY: "test", DUDE_S3_SECRET_KEY: "test-secret" } }));
  router = buildRouter("");
});

async function blocked(query: string) {
  const deadline = Date.now() + 4000;
  while (Date.now() < deadline) {
    const rows = await owner`SELECT pid FROM pg_stat_activity WHERE datname = ${NAME}
      AND pid <> pg_backend_pid() AND query LIKE ${`%${query}%`} AND cardinality(pg_blocking_pids(pid)) > 0`;
    if (rows.length) return;
    await new Promise<void>((resolve) => setImmediate(resolve));
  }
  throw new Error(`no observed blocked transaction: ${query}`);
}

async function readable(a: { id: string }) {
  for (const suffix of ["", "?variant=original"]) {
    const response = await call(ana.key, "GET", `/v1/attachments/${a.id}${suffix}`);
    expect(response.status).toBe(200);
    const bytes = new Uint8Array(await response.arrayBuffer());
    expect(bytes.length).toBe(33);
    expect([...bytes.slice(0, 8)]).toEqual([137, 80, 78, 71, 13, 10, 26, 10]);
    expect(bytes[32]).toBe(suffix ? 1 : 2);
  }
}

test("aged screenshot submission refreshes retention before a waiting sweep rechecks the row", async () => {
  const id = "run_pf_gc_race"; await preview(id);
  const a = await (await upload(id)).json();
  await owner`UPDATE attachments SET created_at = now() - interval '25 hours' WHERE id = ${a.id}`;
  await owner.unsafe(`CREATE FUNCTION pf_submit_barrier() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
    PERFORM pg_advisory_xact_lock(812349); RETURN NEW; END $$;
    CREATE TRIGGER pf_submit_barrier BEFORE INSERT ON preview_feedback FOR EACH ROW EXECUTE FUNCTION pf_submit_barrier()`);
  let release!: () => void, entered!: () => void;
  const gate = new Promise<void>((r) => { release = r; });
  const ready = new Promise<void>((r) => { entered = r; });
  const barrier = owner.begin(async (tx) => { await tx`SELECT pg_advisory_xact_lock(812349)`; entered(); await gate; });
  await ready;
  const pending = submit(id, input(id, "gc-race", { attachmentId: a.id }));
  let sweep: ReturnType<typeof sweepAttachments> | undefined;
  try {
    await blocked("INSERT INTO preview_feedback");
    sweep = sweepAttachments();
    await blocked("DELETE FROM attachments a");
  } finally { release(); await barrier; }
  expect((await pending).status).toBe(201);
  await sweep;
  await owner.unsafe("DROP TRIGGER pf_submit_barrier ON preview_feedback; DROP FUNCTION pf_submit_barrier()");
  expect((await owner`SELECT id FROM attachments WHERE id = ${a.id}`).map((r: { id: string }) => r.id)).toEqual([a.id]);
  await readable(a);
});

test("sweeper keeps old open and resolved screenshots and expires only the unreferenced control", async () => {
  const id = "run_pf_gc_keep"; await preview(id);
  const retained: string[] = [];
  for (const status of ["open", "resolved"]) {
    const a = await (await upload(id)).json(); retained.push(a.id);
    const f = await (await submit(id, input(id, status, { attachmentId: a.id }))).json();
    if (status === "resolved") expect((await call(ana.key, "POST", `/v1/preview-feedback/${f.id}/resolve`, {})).status).toBe(200);
  }
  const control = await (await upload(id)).json();
  await owner`UPDATE attachments SET created_at = now() - interval '25 hours', detached_at = NULL WHERE preview_run_id = ${id}`;
  expect((await sweepAttachments()).expired).toBe(1);
  expect((await owner`SELECT id FROM attachments WHERE preview_run_id = ${id} ORDER BY id`).map((r: { id: string }) => r.id)).toEqual(retained.sort());
  for (const id of retained) await readable({ id });
  expect((await call(ana.key, "GET", `/v1/attachments/${control.id}`)).status).toBe(404);
});

test("server removal purges only that server, preserving sibling history and another project", async () => {
  for (const tombstone of [false, true]) {
    const id = `run_pf_siblings_${tombstone}`; await preview(id);
    const control = `${id}_control`; await preview(control, "org_pf", "prj_pf_two");
    const kept: string[] = [], removed: string[] = [];
    for (const run of [id, control]) for (const server of ["web", "api"]) for (const status of ["open", "resolved"]) {
      const a = await (await upload(run, ana.key, server)).json();
      const response = await call(ana.key, "POST", `/v1/branch-previews/${run}/servers/${server}/feedback`,
        input(run, `${server}-${status}`, { url: `https://${run}-${server}.preview.test/cart`, attachmentId: a.id }));
      expect(response.status).toBe(201); const f = await response.json();
      if (status === "resolved") await call(ana.key, "POST", `/v1/preview-feedback/${f.id}/resolve`, { note: "verified" });
      (run === id && server === "web" ? removed : kept).push(a.id);
    }
    const keys = await owner`SELECT object_key, original_key FROM attachments WHERE id = ANY(${owner.array(removed, "TEXT")})`;
    if (tombstone) await owner`UPDATE preview_servers SET deleted_at = now() WHERE run_id = ${id} AND name = 'web'`;
    else await owner`DELETE FROM preview_servers WHERE run_id = ${id} AND name = 'web'`;
    expect((await owner`SELECT id FROM attachments WHERE preview_run_id = ANY(${owner.array([id, control], "TEXT")}) ORDER BY id`).map((r: { id: string }) => r.id)).toEqual(kept.sort());
    expect((await owner`SELECT 1 FROM preview_feedback WHERE preview_run_id = ANY(${owner.array([id, control], "TEXT")})`).length).toBe(6);
    expect((await owner`SELECT history FROM preview_feedback WHERE preview_run_id = ${id} AND status = 'resolved'`)[0]!.history).toHaveLength(1);
    await sweepAttachments();
    for (const row of keys) { expect(objects.has(row.object_key)).toBe(false); expect(objects.has(row.original_key)).toBe(false); }
    for (const id of kept) await readable({ id });
  }
});

test("submission keys are independent across authors, servers, Runs and projects; screenshots are not", async () => {
  const id = "run_pf_key_scope"; await preview(id);
  const ids: string[] = [];
  for (const key of [ana.key, bo.key]) {
    const response = await submit(id, input(id, "shared-key"), key); expect(response.status).toBe(201);
    const f = await response.json(); ids.push(f.id);
    expect(f.author.id).toBe(key === ana.key ? ana.personId : bo.personId);
  }
  for (const [run, server, project] of [[id, "api", "prj_pf"], ["run_pf_key_other", "web", "prj_pf"], ["run_pf_key_project", "web", "prj_pf_two"]]) {
    if (run !== id) await preview(run!, "org_pf", project);
    const response = await call(ana.key, "POST", `/v1/branch-previews/${run}/servers/${server}/feedback`,
      input(run!, "shared-key", { url: `https://${run}-${server}.preview.test/cart` }));
    expect(response.status).toBe(201); ids.push((await response.json()).id);
  }
  expect(new Set(ids).size).toBe(5);
  const wrongServer = await (await upload(id, ana.key, "api")).json();
  const wrongRun = await (await upload("run_pf_key_other")).json();
  await owner`INSERT INTO tasks (id, organization_id, project_id, number, title, goal) VALUES ('wi_pf_shot', 'org_pf', 'prj_pf', 1, 'Example', 'Example')`;
  const taskShot = await (await upload(id)).json();
  await owner`UPDATE attachments SET task_id = 'wi_pf_shot', preview_run_id = NULL, preview_server_name = NULL WHERE id = ${taskShot.id}`;
  for (const a of [wrongServer, wrongRun, taskShot]) expect((await submit(id, input(id, crypto.randomUUID(), { attachmentId: a.id }))).status).toBe(400);
});
afterAll(async () => {
  s3.stop(true); useConfig(null); await closePool(app); await owner?.end();
  await admin?.unsafe(`DROP DATABASE IF EXISTS "${NAME}" WITH (FORCE)`); await admin?.end();
});

test("server feedback is idempotent, immutable and separate from task delivery", async () => {
  await preview("run_pf_idempotent");
  const payload = input("run_pf_idempotent", "retry-key");
  const results = await Promise.all([submit("run_pf_idempotent", payload), submit("run_pf_idempotent", payload)]);
  expect(results.map((r) => r.status)).toEqual([201, 201]);
  const [a, b] = await Promise.all(results.map((r) => r.json())); expect(a.id).toBe(b.id);
  expect(a).toMatchObject({ status: "open", captureSha: null, projectId: "prj_pf", serverName: "web", history: [] });
  expect((await submit("run_pf_idempotent", { ...payload, body: "different" })).status).toBe(409);
  expect((await owner`SELECT 1 FROM feedback_items`).length).toBe(0);
  // Separate from task delivery: one project-scoped event for the one item, nothing on a task or run.
  const events = await owner`SELECT event_type, project_id, task_id, run_id, payload FROM events WHERE payload->>'previewRunId' = 'run_pf_idempotent'`;
  expect(events.map((e: { event_type: string; project_id: string; task_id: string | null; run_id: string | null; payload: { feedbackId: string } }) =>
    [e.event_type, e.project_id, e.task_id, e.run_id, e.payload.feedbackId])).toEqual([["preview_feedback.submitted", "prj_pf", null, null, a.id]]);
  expect((await owner`SELECT 1 FROM events WHERE event_type NOT LIKE 'preview_feedback.%'`).length).toBe(0);
});

test("reconnect retry with the same screenshot bytes is the same submission", async () => {
  const id = "run_pf_picture_retry"; await preview(id);
  const first = await (await upload(id)).json(); const second = await (await upload(id)).json();
  expect(first.id).toMatch(/^att_/); expect(second.id).toMatch(/^att_/);
  const a = await submit(id, input(id, "same-shot", { attachmentId: first.id }));
  const b = await submit(id, input(id, "same-shot", { attachmentId: second.id }));
  expect(a.status).toBe(201); expect(b.status).toBe(201);
  expect((await a.json()).id).toBe((await b.json()).id);
});

test("branch origin lookup is scoped, live and discriminated without leaking saved feedback", async () => {
  const id = "run_pf_lookup"; await preview(id);
  const path = `/v1/preview-servers/lookup?origin=https://${id}-web.preview.test`;
  const found = await call(ana.key, "GET", path); expect(found.status).toBe(200);
  expect(await found.json()).toEqual({ kind: "branch", organizationId: "org_pf", organizationSlug: "org_pf",
    projectId: "prj_pf", previewRunId: id, branch: id, server: "web" });
  expect((await call(other.key, "GET", path)).status).toBe(404);
  const known = `/v1/branch-previews/${id}/servers/web/owner?origin=https://${id}-web.preview.test`;
  expect(await (await call(ana.key, "GET", known)).json()).toEqual(await (await call(ana.key, "GET", path)).json());
  expect((await call(other.key, "GET", known)).status).toBe(404);
  expect((await call(ana.key, "GET", known.replace("-web.preview", "-api.preview"))).status).toBe(404);
  await owner`UPDATE runs SET status = 'completed' WHERE id = ${id}`;
  expect((await call(ana.key, "GET", path)).status).toBe(404);
  expect((await call(ana.key, "GET", known)).status).toBe(404);
});

test("tenant RLS, project consistency, live server and screenshot ownership are enforced", async () => {
  await preview("run_pf_scope");
  const f = await (await submit("run_pf_scope")).json();
  for (const path of [`/v1/preview-feedback/${f.id}`, "/v1/branch-previews/run_pf_scope/feedback"]) {
    expect((await call(other.key, "GET", path)).status).toBe(404);
  }
  expect((await submit("run_pf_scope", input("run_pf_scope"), other.key)).status).toBe(404);
  for (const action of ["resolve", "reopen"]) expect((await call(other.key, "POST", `/v1/preview-feedback/${f.id}/${action}`, {})).status).toBe(404);
  expect(await withOrg("org_pf_other", async ({ sql }) => (await sql`SELECT 1 FROM preview_feedback`).length)).toBe(0);
  await expect(withOrg("org_pf", async ({ sql }) => sql`INSERT INTO preview_feedback
    (id, organization_id, project_id, preview_run_id, preview_server_name, author_person_id, submission_key, body, anchor)
    VALUES ('fbi_bad', 'org_pf', 'prj_pf_two', 'run_pf_scope', 'web', ${ana.personId}, 'bad', 'x', '{"kind":"preview"}')`)).rejects.toThrow();
  expect((await submit("run_pf_scope", input("run_pf_scope", "url", { url: "https://elsewhere.test" }))).status).toBe(400);
  const a = await (await upload("run_pf_scope", bo.key)).json();
  expect(a.id).toMatch(/^att_/);
  expect((await submit("run_pf_scope", input("run_pf_scope", "att", { attachmentId: a.id }))).status).toBe(400);
  expect((await submit("run_missing")).status).toBe(404);
});

test("bounded pagination, default open filter and idempotent resolve/reopen retain context and notes", async () => {
  const id = "run_pf_pages"; await preview(id);
  const ids: string[] = [];
  for (let i = 0; i < 5; i++) { ids.push((await (await submit(id)).json()).id); await Bun.sleep(2); }
  for (const action of ["resolve", "resolve", "reopen", "reopen", "resolve"]) {
    const r = await call(bo.key, "POST", `/v1/preview-feedback/${ids[0]}/${action}`, { note: "checked" }); expect(r.status).toBe(200);
  }
  const shown = await (await call(ana.key, "GET", `/v1/preview-feedback/${ids[0]}`)).json();
  expect(shown.history.length).toBe(3); expect(shown.resolutionNote).toBe("checked"); expect(shown.anchor.selector).toBe("#pay");
  const base = `/v1/branch-previews/${id}/feedback`;
  expect((await (await call(ana.key, "GET", base)).json()).feedback.length).toBe(4);
  const page = await (await call(ana.key, "GET", `${base}?status=all&limit=2`)).json();
  const next = await (await call(ana.key, "GET", `${base}?status=all&limit=2&after=${page.nextCursor}`)).json();
  expect(page.feedback.map((f: { id: string }) => f.id)).toEqual(ids.slice(0, 2));
  expect(next.feedback.map((f: { id: string }) => f.id)).toEqual(ids.slice(2, 4));
  expect((await (await call(ana.key, "GET", `${base}?server=api`)).json()).feedback).toEqual([]);
  for (const q of ["limit=0", "limit=201", "status=solved", "after=invalid"]) expect((await call(ana.key, "GET", `${base}?${q}`)).status).toBe(400);
});

test("generic filtered cursor plans seek sparse open feedback using ordered indexes", async () => {
  const id = "run_pf_sparse"; await preview(id);
  await owner`INSERT INTO preview_feedback (id, organization_id, project_id, preview_run_id, preview_server_name,
    author_person_id, submission_key, body, anchor, status, resolved_at, resolved_by)
    SELECT 'fbi_sparse_' || lpad(n::text, 6, '0'), 'org_pf', 'prj_pf', ${id}, 'web', ${ana.personId}, n::text,
      'Example', '{"kind":"preview"}', CASE WHEN n <= 20000 THEN 'resolved' ELSE 'open' END,
      CASE WHEN n <= 20000 THEN now() END, CASE WHEN n <= 20000 THEN ${ana.personId} END
    FROM generate_series(1, 20010) n`;
  await owner`ANALYZE preview_feedback`;
  await owner.begin(async (tx) => {
    await tx`SET LOCAL plan_cache_mode = force_generic_plan`;
    await tx.unsafe(`PREPARE pf_old(text,text,text,text,int) AS SELECT id FROM preview_feedback
      WHERE preview_run_id=$1 AND ($2 IS NULL OR preview_server_name=$2) AND ($3='all' OR status=$3)
      AND id>$4 ORDER BY id LIMIT $5`);
    const before = await tx.unsafe(`EXPLAIN (ANALYZE, BUFFERS) EXECUTE pf_old('${id}','web','open','',5)`);
    console.log("EXPLAIN BEFORE", before.map((r: Record<string, string>) => r["QUERY PLAN"]).join("\n"));
    for (const [name, predicates] of [["run", "status=$2"], ["server", "preview_server_name=$2"],
      ["both", "preview_server_name=$2 AND status=$3"]]) {
      const both = name === "both";
      await tx.unsafe(`PREPARE pf_${name}(text,text${both ? ",text" : ""},text,int) AS SELECT id FROM preview_feedback
        WHERE preview_run_id=$1 AND ${predicates} AND id>$${both ? 4 : 3} ORDER BY id LIMIT $${both ? 5 : 4}`);
      const rows = await tx.unsafe(`EXPLAIN (ANALYZE, BUFFERS) EXECUTE pf_${name}('${id}','${name === "run" ? "open" : "web"}'${both ? ",'open'" : ""},'',5)`);
      const plan = rows.map((r: Record<string, string>) => r["QUERY PLAN"]).join("\n");
      console.log(`EXPLAIN AFTER ${name}`, plan);
      expect(plan).toMatch(/Index (Only )?Scan using preview_feedback_(status|server_status|server)_idx/);
      expect(plan).not.toContain("Rows Removed by Filter");
      expect(plan).not.toContain("Sort");
    }
    for (const name of ["old", "run", "server", "both"]) await tx.unsafe(`DEALLOCATE pf_${name}`);
  });
});

test("pause/wake/push and recoverable transport replacement preserve open and resolved feedback", async () => {
  const id = "run_pf_preserve"; await preview(id);
  const a = await (await submit(id)).json(); const b = await (await submit(id)).json();
  await call(ana.key, "POST", `/v1/preview-feedback/${b.id}/resolve`, {});
  await owner`UPDATE runs SET status = 'running', lux_run_id = 'lux-old', base_shas = '{"web":"old"}' WHERE id = ${id}`;
  await owner`UPDATE runs SET status = 'paused', lux_state = 'failed' WHERE id = ${id}`;
  await owner`UPDATE runs SET lux_run_id = NULL, lux_generation = lux_generation + 1 WHERE id = ${id}`;
  await owner`UPDATE runs SET status = 'running', lux_run_id = 'lux-new', sync_wanted_at = now(), base_shas = '{"web":"new"}' WHERE id = ${id}`;
  expect((await (await call(ana.key, "GET", `/v1/branch-previews/${id}/feedback?status=all`)).json()).feedback.map((f: { id: string }) => f.id).sort()).toEqual([a.id, b.id].sort());
  expect((await (await call(ana.key, "GET", `/v1/preview-feedback/${a.id}`)).json()).captureSha).toBeNull();
});

test("all logical deletion paths revoke screenshots immediately and queue retryable physical GC", async () => {
  for (const action of ["stop", "branch-delete", "expiry", "server-delete", "tombstone"]) {
    const id = `run_pf_${action}`; await preview(id);
    const response = await upload(id); expect(response.status).toBe(201); const a = await response.json();
    const read = await call(ana.key, "GET", `/v1/attachments/${a.id}`); expect(read.status).toBe(200); expect(read.headers.get("cache-control")).toBe("private, no-store");
    const f = await (await submit(id, input(id, "picture", { attachmentId: a.id }))).json();
    await call(ana.key, "POST", `/v1/preview-feedback/${f.id}/resolve`, {});
    const [keys] = await owner`SELECT object_key, original_key FROM attachments WHERE id = ${a.id}`;
    if (action === "server-delete") await owner`DELETE FROM preview_servers WHERE run_id = ${id} AND name = 'web'`;
    else if (action === "tombstone") await owner`UPDATE preview_servers SET deleted_at = now() WHERE run_id = ${id} AND name = 'web'`;
    else await owner`UPDATE runs SET status = 'completed', ended_at = now() WHERE id = ${id}`;
    expect((await call(ana.key, "GET", `/v1/preview-feedback/${f.id}`)).status).toBe(404);
    expect((await call(ana.key, "GET", `/v1/attachments/${a.id}`)).status).toBe(404);
    expect((await submit(id)).status).toBe(404);
    expect((await owner`SELECT 1 FROM attachment_object_deletions WHERE object_key = ${keys.object_key}`).length).toBe(1);
    expect(objects.has(keys.object_key)).toBe(true);
  }
  down = true; expect((await sweepAttachments()).failed).toBeGreaterThan(0); down = false;
  expect((await sweepAttachments()).deleted).toBeGreaterThan(0);
  await preview("run_pf_new_incarnation");
  expect((await (await call(ana.key, "GET", "/v1/branch-previews/run_pf_new_incarnation/feedback?status=all")).json()).feedback).toEqual([]);
});

test("upload completion racing termination cannot resurrect metadata and queues both objects", async () => {
  const id = "run_pf_upload_race"; await preview(id);
  let entered!: () => void, release!: () => void;
  const started = new Promise<void>((r) => { entered = r; }); const gate = new Promise<void>((r) => { release = r; });
  putGate = async () => { entered(); await gate; };
  const pending = upload(id); await started;
  await owner`UPDATE runs SET status = 'completed' WHERE id = ${id}`; release();
  const result = await pending; putGate = null; expect(result.status).toBe(404);
  expect((await owner`SELECT 1 FROM attachments WHERE preview_run_id = ${id}`).length).toBe(0);
  expect((await owner`SELECT 1 FROM attachment_object_deletions WHERE object_key LIKE ${`%/${id}/%`}`).length).toBe(2);
});

test("submit and resolve racing termination cannot leave feedback behind", async () => {
  for (let i = 0; i < 8; i++) {
    const id = `run_pf_race${i}`; await preview(id);
    const f = await (await submit(id)).json();
    const results = await Promise.all([submit(id), call(ana.key, "POST", `/v1/preview-feedback/${f.id}/resolve`, {}),
      owner`UPDATE runs SET status = 'completed' WHERE id = ${id}`]);
    for (const r of results.slice(0, 2) as Response[]) expect([200, 201, 404]).toContain(r.status);
    expect((await owner`SELECT 1 FROM preview_feedback WHERE preview_run_id = ${id}`).length).toBe(0);
  }
});

test("submit and resolve versus Run end or server tombstone have deterministic winners in both orders", async () => {
  for (const end of ["run", "server"]) for (const action of ["submit", "resolve"]) for (const first of ["end", "mutation"]) {
    const id = `run_pf_barrier_${end}_${action}_${first}`; await preview(id);
    const f = await (await submit(id)).json();
    const mutate = () => action === "submit" ? submit(id) : call(ana.key, "POST", `/v1/preview-feedback/${f.id}/resolve`, {});
    const endQuery = (tx: SQL) => end === "run" ? tx`UPDATE runs SET status = 'completed' WHERE id = ${id}`
      : tx`UPDATE preview_servers SET deleted_at = now() WHERE run_id = ${id} AND name = 'web'`;
    let entered!: () => void, release!: () => void;
    const ready = new Promise<void>((r) => { entered = r; }); const gate = new Promise<void>((r) => { release = r; });
    if (first === "end") {
      const ending = owner.begin(async (tx) => { await endQuery(tx as unknown as SQL); entered(); await gate; });
      await ready; const mutation = mutate();
      try { await blocked("FOR SHARE"); } finally { release(); await ending; }
      expect((await mutation).status).toBe(404);
    } else {
      await owner.unsafe(`CREATE FUNCTION pf_mutation_barrier() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
        PERFORM pg_advisory_xact_lock(812350); RETURN NEW; END $$;
        CREATE TRIGGER pf_mutation_barrier BEFORE INSERT OR UPDATE ON preview_feedback FOR EACH ROW EXECUTE FUNCTION pf_mutation_barrier()`);
      const barrier = owner.begin(async (tx) => { await tx`SELECT pg_advisory_xact_lock(812350)`; entered(); await gate; });
      await ready; const mutation = mutate(); let ending: Promise<unknown> | undefined;
      try {
        await blocked(action === "submit" ? "INSERT INTO preview_feedback" : "UPDATE preview_feedback SET status");
        ending = Promise.resolve(owner.begin(async (tx) => { await endQuery(tx as unknown as SQL); }));
        await blocked(end === "run" ? "UPDATE runs SET status" : "UPDATE preview_servers SET deleted_at");
      } finally { release(); await barrier; }
      expect((await mutation).status).toBe(action === "submit" ? 201 : 200); await ending;
      await owner.unsafe("DROP TRIGGER pf_mutation_barrier ON preview_feedback; DROP FUNCTION pf_mutation_barrier()");
    }
    expect((await owner`SELECT id FROM preview_feedback WHERE preview_run_id = ${id}`).length).toBe(0);
    expect((await submit(id)).status).toBe(404);
  }
});

test("submission does not invert the wake transaction's Run then server locks", async () => {
  const id = "run_pf_lock_order"; await preview(id);
  let locked!: () => void, proceed!: () => void;
  const ready = new Promise<void>((r) => { locked = r; }); const gate = new Promise<void>((r) => { proceed = r; });
  const wake = owner.begin(async (tx) => {
    await tx`SELECT id FROM runs WHERE id = ${id} FOR UPDATE`;
    locked(); await gate;
    await tx`UPDATE preview_servers SET idle_at = NULL WHERE run_id = ${id}`;
  });
  await ready;
  const pending = submit(id);
  await Bun.sleep(30); proceed();
  await wake;
  expect((await pending).status).toBe(201);
});
