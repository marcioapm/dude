/**
 * Incoming webhooks through the public API: the endpoint's verification
 * (each mode, constant-time), its refusals (stored with no body, answered
 * with their status), the rate limit, header redaction, and the routes an
 * automation's page reads (create's one-time secret, rotate, deliveries,
 * the organisation automation's projects, group-by with Send).
 *
 * Requires DATABASE_URL: a role that can create databases (the owner).
 */

import { afterAll, beforeAll, describe, expect, test } from "bun:test";
import { SQL } from "bun";
import { createHmac } from "node:crypto";
import { join } from "node:path";
import { INCOMING_NEEDS_PROJECT, INCOMING_SEND_NEEDS_GROUP } from "@dude/domain";
import { closePool, setPool } from "../src/db/client.ts";
import { buildRouter } from "../src/index.ts";
import { Config, useConfig } from "../src/config.ts";
import type { Router } from "../src/api/router.ts";
import { createApiKey } from "../src/api/auth.ts";

const OWNER_URL = process.env.DATABASE_URL ?? "postgres://dude:dude@localhost:5433/dude";
const ROOT = join(import.meta.dir, "../../..");
const NAME = `dude_incoming_test_${Bun.randomUUIDv7("hex").slice(-12)}`;
const ORG = "org_inc";
const PRJ = "prj_inc";

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
let orchestratorServer: ReturnType<typeof Bun.serve>;
let key = "";

type Json = any; // eslint-disable-line @typescript-eslint/no-explicit-any

function call(method: string, path: string, payload?: unknown) {
  return router.handle(new Request(`http://dude.test${path}`, {
    method, headers: { authorization: `Bearer ${key}`, "content-type": "application/json" },
    ...(payload === undefined ? {} : { body: JSON.stringify(payload) }),
  }));
}

function post(hook: string, body: string | Uint8Array, headers: Record<string, string> = {}, query = "") {
  return router.handle(new Request(`http://dude.test/v1/hooks/${hook}${query}`, {
    method: "POST", headers: { "content-type": "application/json", ...headers }, body,
  }));
}

const sign = (secret: string, body: string) => createHmac("sha256", secret).update(body).digest("hex");

beforeAll(async () => {
  admin = new SQL(OWNER_URL);
  await admin.unsafe(`CREATE DATABASE "${NAME}"`);
  const migrate = Bun.spawnSync(["bun", "run", "apps/control-plane/src/db/migrate.ts"], {
    cwd: ROOT, env: { ...process.env, DATABASE_URL: databaseUrl("owner", NAME) },
  });
  if (migrate.exitCode !== 0) throw new Error(`migrate: ${migrate.stderr.toString()}`);
  owner = new SQL(databaseUrl("owner", NAME));
  await owner`INSERT INTO organizations (id, name, slug) VALUES (${ORG}, 'Acme', 'acme')`;
  await owner`INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES (${PRJ}, ${ORG}, 'platform', 'platform', 'PLAT')`;
  app = new SQL(databaseUrl("app", NAME));
  setPool(app);
  orchestratorServer = Bun.serve({ port: 0, fetch: () => new Response(null, { status: 204 }) });
  key = (await createApiKey({ organizationId: ORG, name: "ana" })).key;
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

/** A project incoming automation, turned on, with this verification; its hook and secret. */
async function hookWith(verify: Record<string, unknown>, extra: Record<string, unknown> = {}): Promise<{ id: string; hook: string; secret: string; a: Json }> {
  const res = await call("POST", "/v1/automations", { name: "Investigate", kind: "agent", trigger: "incoming", projectId: PRJ });
  expect(res.status).toBe(201);
  const created = await res.json() as Json;
  expect(created.incomingSecret).toMatch(/^whsec_[A-Za-z0-9_-]{43}$/);
  expect(created.incomingSecretHint).toBe(created.incomingSecret.slice(-4));
  const { id, projectId: _p, projectName: _n, enabled: _e, owner: _o, ownerLeftAt: _l, webhookSecretHint: _w, incomingSecretHint: _i,
    nextRunAt: _x, lastRun: _r, canEdit: _c, canTransfer: _t, createdAt: _ca, updatedAt: _u, incomingSecret: _s, ...settings } = created;
  const saved = await call("PUT", `/v1/automations/${id}`, { ...settings, trigger: { ...settings.trigger, verify, ratePerHour: 5 }, ...extra });
  expect(saved.status).toBe(200);
  expect((await saved.json() as Json).incomingSecret).toBeUndefined();
  await call("PUT", `/v1/automations/${id}/enabled`, { enabled: true });
  return { id, hook: created.trigger.hookId, secret: created.incomingSecret, a: settings };
}

const rows = async (automation: string) => [...await owner`SELECT verdict, reason, payload, headers, bytes FROM incoming_deliveries
  WHERE automation_id = ${automation} ORDER BY received_at, id`] as Json[];

describe("the public endpoint", () => {
  test("an unknown hook answers 404 and stores nothing", async () => {
    const before = (await owner`SELECT count(*)::int AS n FROM incoming_deliveries`)[0].n;
    const res = await post("hk_nothinghere00000", "{}");
    expect(res.status).toBe(404);
    expect((await owner`SELECT count(*)::int AS n FROM incoming_deliveries`)[0].n).toBe(before);
  });

  test("a signature: the right HMAC of the raw body after its prefix is taken; a wrong, unprefixed or missing one is refused", async () => {
    const { id, hook, secret } = await hookWith({ mode: "signature", header: "X-Signature", prefix: "sha256=" });
    const body = `{"status":"firing","groupKey":"g"}`;
    const ok = await post(hook, body, { "x-signature": `sha256=${sign(secret, body)}` });
    expect(ok.status).toBe(202);
    expect((await ok.json() as Json).delivery).toMatch(/^dlv_/);
    for (const headers of [{ "x-signature": `sha256=${sign("whsec_wrong", body)}` }, { "x-signature": sign(secret, body) }, {},
      { "x-signature": `sha256=${sign(secret, body + " ")}` }]) {
      const res = await post(hook, body, headers);
      expect(res.status).toBe(401);
      expect(await res.json()).toMatchObject({ error: { code: "bad_signature" } });
    }
    const stored = await rows(id);
    expect(stored.map((r) => r.verdict)).toEqual([null, "refused", "refused", "refused", "refused"]);
    expect(stored[0].payload).toEqual({ status: "firing", groupKey: "g" });
    expect(stored.slice(1).every((r) => r.payload === null && r.reason === "bad_signature")).toBe(true);
    // The signature header is kept as ••••, never its value.
    expect(stored[0].headers["X-Signature"]).toBe("••••");
  });

  test("a header token: the secret, bare or as Bearer, is taken; another is refused", async () => {
    const { id, hook, secret } = await hookWith({ mode: "header", header: "Authorization" });
    expect((await post(hook, "{}", { authorization: secret })).status).toBe(202);
    expect((await post(hook, "{}", { authorization: `Bearer ${secret}` })).status).toBe(202);
    expect((await post(hook, "{}", { authorization: `Bearer ${secret}x` })).status).toBe(401);
    expect((await post(hook, "{}", { authorization: secret.slice(0, -1) })).status).toBe(401);
    expect((await post(hook, "{}")).status).toBe(401);
    const stored = await rows(id);
    expect(stored.map((r) => r.verdict)).toEqual([null, null, "refused", "refused", "refused"]);
    expect(stored.every((r) => r.headers.Authorization === undefined || r.headers.Authorization === "••••")).toBe(true);
  });

  test("a query parameter: ?token=<secret> is taken; another value or none is refused", async () => {
    const { id, hook, secret } = await hookWith({ mode: "query", param: "token" });
    expect((await post(hook, "{}", {}, `?token=${encodeURIComponent(secret)}`)).status).toBe(202);
    expect((await post(hook, "{}", {}, "?token=nope")).status).toBe(401);
    expect((await post(hook, "{}", {}, `?other=${encodeURIComponent(secret)}`)).status).toBe(401);
    expect((await rows(id)).map((r) => r.verdict)).toEqual([null, "refused", "refused"]);
  });

  test("too large is 413, not JSON is 415, a JSON array is 415: each stored refused with no body", async () => {
    const { id, hook, secret } = await hookWith({ mode: "header", header: "X-Token" });
    const big = `{"a":"${"x".repeat(1_048_576)}"}`;
    const res = await post(hook, big, { "x-token": secret });
    expect(res.status).toBe(413);
    expect(await res.json()).toMatchObject({ error: { code: "too_large" } });
    expect((await post(hook, "not json", { "x-token": secret })).status).toBe(415);
    expect((await post(hook, "[1,2]", { "x-token": secret })).status).toBe(415);
    const stored = await rows(id);
    expect(stored.map((r) => [r.verdict, r.reason, r.payload])).toEqual([
      ["refused", "too_large", null], ["refused", "not_json", null], ["refused", "not_json", null]]);
    expect(stored[0].bytes).toBeGreaterThan(1_048_576);
    // X-Token is the token header: its value is not kept.
    expect(stored[1].headers["X-Token"]).toBe("••••");
  });

  test("a body JSON allows but Postgres refuses, or invalid UTF-8, is 415 with a bodyless row; a huge declared size is clamped", async () => {
    const { id, hook, secret } = await hookWith({ mode: "header", header: "Authorization" });
    const nul = await post(hook, `{"a":"\\u0000"}`, { authorization: secret });
    expect(nul.status).toBe(415);
    expect(await nul.json()).toMatchObject({ error: { code: "not_json" } });
    const latin = await post(hook, Uint8Array.of(0x7b, 0x22, 0x61, 0x22, 0x3a, 0x22, 0xff, 0x22, 0x7d), { authorization: secret });
    expect(latin.status).toBe(415);
    const huge = await post(hook, "{}", { authorization: "wrong", "content-length": "3000000000" });
    expect(huge.status).toBe(401);
    expect((await rows(id)).map((r) => [r.verdict, r.reason, r.payload, r.bytes])).toEqual([
      ["refused", "not_json", null, 14], ["refused", "not_json", null, 9], ["refused", "bad_signature", null, 1_048_577]]);
  });

  test("the rate limit counts deliveries not matched yet and those sent to a run, and refuses the next with 429", async () => {
    const { id, hook, secret } = await hookWith({ mode: "header", header: "Authorization" });
    // Five an hour (hookWith). Three still waiting for the loop and two sent to a run fill it.
    for (let i = 0; i < 5; i++) expect((await post(hook, `{"i":${i}}`, { authorization: secret })).status).toBe(202);
    await owner`UPDATE incoming_deliveries SET verdict = 'sent_to_run' WHERE automation_id = ${id} AND payload->>'i' IN ('0', '1')`;
    const over = await post(hook, `{"i":5}`, { authorization: secret });
    expect(over.status).toBe(429);
    expect(await over.json()).toMatchObject({ error: { code: "rate_limited" } });
    // A refused row is not counted; an hour on, there is room again.
    await owner`UPDATE incoming_deliveries SET received_at = received_at - interval '61 minutes' WHERE automation_id = ${id} AND payload->>'i' = '0'`;
    expect((await post(hook, `{"i":6}`, { authorization: secret })).status).toBe(202);
    const stored = await rows(id);
    expect(stored.filter((r) => r.verdict === "refused").map((r) => [r.reason, r.payload])).toEqual([["rate_limited", null]]);
  });

  test("skipped deliveries leave the hour's places free; started and sent-to-run ones use them up (429)", async () => {
    const { id, hook, secret } = await hookWith({ mode: "header", header: "Authorization" });
    // Five an hour (hookWith). Five that matched no condition take no place.
    for (let i = 0; i < 5; i++) expect((await post(hook, `{"i":${i}}`, { authorization: secret })).status).toBe(202);
    await owner`UPDATE incoming_deliveries SET verdict = 'skipped', reason = 'status is resolved, not firing' WHERE automation_id = ${id}`;
    for (let i = 5; i < 10; i++) expect((await post(hook, `{"i":${i}}`, { authorization: secret })).status).toBe(202);
    // Those five started a run or were sent to it: the hour is full.
    await owner`UPDATE incoming_deliveries SET verdict = CASE WHEN (payload->>'i')::int < 8 THEN 'started' ELSE 'sent_to_run' END
      WHERE automation_id = ${id} AND verdict IS NULL`;
    const over = await post(hook, `{"i":10}`, { authorization: secret });
    expect(over.status).toBe(429);
    expect(await over.json()).toMatchObject({ error: { code: "rate_limited" } });
    expect((await rows(id)).map((r) => r.verdict)).toEqual([...Array(5).fill("skipped"), "started", "started", "started",
      "sent_to_run", "sent_to_run", "refused"]);
  });

  test("an automation that is off refuses, keeping no body", async () => {
    const { id, hook, secret } = await hookWith({ mode: "header", header: "Authorization" });
    await call("PUT", `/v1/automations/${id}/enabled`, { enabled: false });
    expect((await post(hook, "{}", { authorization: secret })).status).toBe(403);
    expect((await rows(id)).map((r) => [r.verdict, r.reason, r.payload])).toEqual([["refused", "disabled", null]]);
  });

  test("a header or query secret is checked before the body is read; a bad signature keeps no headers", async () => {
    const { id, hook } = await hookWith({ mode: "header", header: "X-Hook-Key" });
    let pulled = false;
    // highWaterMark 0: the stream is pulled only when the body is read.
    const stream = new ReadableStream({ pull(c) { pulled = true; c.enqueue(new TextEncoder().encode("{}")); c.close(); } }, { highWaterMark: 0 });
    const res = await router.handle(new Request(`http://dude.test/v1/hooks/${hook}`, {
      method: "POST", headers: { "content-type": "application/json", "x-hook-key": "wrong", "user-agent": "curl/8" }, body: stream,
      duplex: "half" } as RequestInit));
    expect(res.status).toBe(401);
    expect(pulled).toBe(false);
    expect((await rows(id)).map((r) => [r.reason, r.headers])).toEqual([["bad_signature", {}]]);
  });

  test("past 100 refused rows in an hour, a refusal is counted, not stored, and answered the same", async () => {
    const { id, hook, secret } = await hookWith({ mode: "header", header: "Authorization" });
    await owner`INSERT INTO incoming_deliveries (id, organization_id, automation_id, verdict, reason, bytes, received_at)
      SELECT 'dlv_flood' || n, ${ORG}, ${id}, 'refused', 'bad_signature', 2, date_trunc('hour', now()) FROM generate_series(1, 100) n`;
    expect((await post(hook, "{}", { authorization: "wrong" })).status).toBe(401);
    expect((await post(hook, "{}", { authorization: "wrong" })).status).toBe(401);
    expect((await post(hook, "nope", { authorization: secret })).status).toBe(415);
    expect((await rows(id)).length).toBe(100);
    const counted = [...await owner`SELECT code, n FROM incoming_refusals WHERE automation_id = ${id} ORDER BY code`] as Json[];
    expect(counted.map((r) => [r.code, r.n])).toEqual([["bad_signature", 2], ["not_json", 1]]);
    // An accepted delivery is still stored.
    expect((await post(hook, "{}", { authorization: secret })).status).toBe(202);
    expect((await rows(id)).length).toBe(101);
    const listed = await (await call("GET", `/v1/automations/${id}/deliveries`)).json() as Json;
    expect(listed.refusedNotStored).toBe(3);
  });
});

describe("an incoming automation's routes", () => {
  test("Rotate answers a new secret once; the old one stops working", async () => {
    const { id, hook, secret } = await hookWith({ mode: "header", header: "Authorization" });
    const res = await call("POST", `/v1/automations/${id}/incoming-secret/rotate`);
    const rotated = await res.json() as Json;
    expect(rotated.incomingSecret).not.toBe(secret);
    expect(rotated.incomingSecretHint).toBe(rotated.incomingSecret.slice(-4));
    expect((await (await call("GET", `/v1/automations/${id}`)).json() as Json).incomingSecret).toBeUndefined();
    expect((await post(hook, "{}", { authorization: secret })).status).toBe(401);
    expect((await post(hook, "{}", { authorization: rotated.incomingSecret })).status).toBe(202);
  });

  test("a save keeps the hook: the URL never changes by a settings save", async () => {
    const { id, hook, a } = await hookWith({ mode: "header", header: "Authorization" });
    const res = await call("PUT", `/v1/automations/${id}`, { ...a, trigger: { ...a.trigger, hookId: "hk_attackerchosen000" } });
    expect((await res.json() as Json).trigger.hookId).toBe(hook);
  });

  test("Send to the running agent needs a group-by path", async () => {
    const { id, a } = await hookWith({ mode: "header", header: "Authorization" });
    const res = await call("PUT", `/v1/automations/${id}`, { ...a, sessionMode: "resumable", expiryDays: 1, concurrency: "send",
      trigger: { ...a.trigger, groupBy: "" } });
    expect(res.status).toBe(400);
    expect((await res.json() as Json).error.details.fieldErrors.trigger).toEqual([INCOMING_SEND_NEEDS_GROUP]);
  });

  test("an organisation incoming automation is created, then refused a save until it names a project", async () => {
    const res = await call("POST", "/v1/automations", { name: "Page triage", kind: "agent", trigger: "incoming" });
    expect(res.status).toBe(201);
    const { id, projectId: _p, projectName: _n, enabled: _e, owner: _o, ownerLeftAt: _l, webhookSecretHint: _w, incomingSecretHint: _i,
      nextRunAt: _x, lastRun: _r, canEdit: _c, canTransfer: _t, createdAt: _ca, updatedAt: _u, incomingSecret: _s, ...settings } = await res.json() as Json;
    const refused = await call("PUT", `/v1/automations/${id}`, settings);
    expect(refused.status).toBe(400);
    expect((await refused.json() as Json).error.details.fieldErrors.projectIds).toEqual([INCOMING_NEEDS_PROJECT]);
    // Nor is it turned on while it names none.
    const on = await call("PUT", `/v1/automations/${id}/enabled`, { enabled: true });
    expect(on.status).toBe(400);
    expect((await on.json() as Json).error).toMatchObject({ code: "incoming_needs_project", message: INCOMING_NEEDS_PROJECT });
    expect((await (await call("GET", `/v1/automations/${id}`)).json() as Json).enabled).toBe(false);
    expect((await call("PUT", `/v1/automations/${id}/enabled`, { enabled: false })).status).toBe(200);
    const saved = await call("PUT", `/v1/automations/${id}`, { ...settings, projectIds: [PRJ] });
    expect(saved.status).toBe(200);
    expect((await saved.json() as Json).projectIds).toEqual([PRJ]);
    expect((await call("PUT", `/v1/automations/${id}/enabled`, { enabled: true })).status).toBe(200);
    expect((await call("PUT", `/v1/automations/${id}`, { ...settings, projectIds: ["prj_nope"] })).status).toBe(400);
  });

  test("a run's detail lists the delivery that started it and those sent to it, oldest first", async () => {
    const { id, hook, secret } = await hookWith({ mode: "header", header: "Authorization" });
    for (const status of ["firing", "resolved"]) await post(hook, `{"status":"${status}"}`, { authorization: secret });
    const [first, second] = [...await owner`SELECT id FROM incoming_deliveries WHERE automation_id = ${id} ORDER BY received_at, id`] as Json[];
    await owner`INSERT INTO automation_runs (id, organization_id, automation_id, status, source) VALUES ('arn_inc', ${ORG}, ${id}, 'running', 'event')`;
    await owner`UPDATE incoming_deliveries SET verdict = 'started', run_id = 'arn_inc', group_key = 'g' WHERE id = ${first.id}`;
    await owner`UPDATE incoming_deliveries SET verdict = 'sent_to_run', run_id = 'arn_inc', group_key = 'g' WHERE id = ${second.id}`;
    const run = await (await call("GET", `/v1/automations/${id}/runs/arn_inc`)).json() as Json;
    expect(run.deliveries.map((d: Json) => [d.id, d.verdict, d.groupKey])).toEqual([[first.id, "started", "g"], [second.id, "sent_to_run", "g"]]);
  });

  test("Deliveries lists what came, newest first, and a delivery's detail keeps its headers redacted", async () => {
    const { id, hook, secret } = await hookWith({ mode: "header", header: "Authorization" });
    await post(hook, `{"status":"firing"}`, { authorization: secret, "user-agent": "Grafana/12.2.0", "x-api-secret": "s3" });
    await post(hook, "nope", { authorization: secret });
    const { deliveries } = await (await call("GET", `/v1/automations/${id}/deliveries`)).json() as Json;
    expect(deliveries.map((d: Json) => [d.verdict, d.reason, d.event])).toEqual([["refused", "not_json", "incoming"], [null, null, "incoming"]]);
    const detail = await (await call("GET", `/v1/automations/${id}/deliveries/${deliveries[1].id}`)).json() as Json;
    expect(detail.body).toEqual({ status: "firing" });
    expect(detail.headers).toMatchObject({ Authorization: "••••", "User-Agent": "Grafana/12.2.0", "X-Api-Secret": "••••" });
    expect((await call("GET", `/v1/automations/${id}/deliveries/dlv_nope`)).status).toBe(404);
  });

  test("the list carries no bodies and filters by verdict on the server, past a burst of refusals", async () => {
    const { id, hook, secret } = await hookWith({ mode: "header", header: "Authorization" });
    await post(hook, `{"status":"firing","pad":"${"x".repeat(5000)}"}`, { authorization: secret });
    for (let i = 0; i < 101; i++) await post(hook, "{}", { authorization: "wrong" });
    const all = (await (await call("GET", `/v1/automations/${id}/deliveries`)).json() as Json).deliveries;
    expect(all).toHaveLength(100);
    expect(all.every((d: Json) => d.verdict === "refused" && !("body" in d))).toBe(true);
    const pending = (await (await call("GET", `/v1/automations/${id}/deliveries?verdict=pending`)).json() as Json).deliveries;
    expect(pending.map((d: Json) => [d.verdict, "body" in d])).toEqual([[null, false]]);
    expect(pending[0].bytes).toBeGreaterThan(5000);
    expect((await call("GET", `/v1/automations/${id}/deliveries?verdict=nope`)).status).toBe(400);
  });
});
