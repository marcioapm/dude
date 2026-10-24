/**
 * Chat providers through the public API: a provider's webhook passed on
 * with its own headers and body; an admin's install and a member's link
 * begun with a browser cookie whose hash the orchestrator is given, and the
 * callbacks landing the browser by outcome; the settings reads (providers,
 * an installation's facts, health for admins only), routing instructions
 * (everyone reads, admins write, 4000 characters), and Requests (admins
 * only, filtered by provider and route).
 *
 * Against a database of its own, migrated as a release is, and a stand-in
 * orchestrator. Requires DATABASE_URL: a role that can create databases.
 */

import { afterAll, beforeAll, describe, expect, test } from "bun:test";
import { SQL } from "bun";
import { createHash } from "node:crypto";
import { join } from "node:path";
import { closePool, setPool } from "../src/db/client.ts";
import { buildRouter } from "../src/index.ts";
import { Config, useConfig } from "../src/config.ts";
import type { Router } from "../src/api/router.ts";
import { createApiKey } from "../src/api/auth.ts";

const OWNER_URL = process.env.DATABASE_URL ?? "postgres://dude:dude@localhost:5433/dude";
const ROOT = join(import.meta.dir, "../../..");
const NAME = `dude_chat_providers_test_${Bun.randomUUIDv7("hex").slice(-12)}`;
const ORG = "org_chat";

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
let adminPerson: string;
let memberPerson: string;
let orchestratorServer: ReturnType<typeof Bun.serve>;
/** What the stand-in orchestrator was sent, in order. */
const sent: Array<{ path: string; headers: Record<string, string>; body: string }> = [];
let callbackAnswer: { outcome: string; slug: string; returnTo: string } = { outcome: "connected", slug: "acme", returnTo: "settings" };
/** How the stand-in answers a webhook: as it should, refusing the signature, hanging, or failing a callback. */
let webhookMode: "ok" | "unsigned" | "hang" = "ok";
let callbackFails = false;

type Json = any; // eslint-disable-line @typescript-eslint/no-explicit-any

function call(key: string | null, method: string, path: string, payload?: unknown, headers: Record<string, string> = {}) {
  return router.handle(new Request(`http://dude.test${path}`, {
    method,
    headers: { ...(key ? { authorization: `Bearer ${key}` } : {}), "content-type": "application/json", ...headers },
    ...(payload === undefined ? {} : { body: typeof payload === "string" ? payload : JSON.stringify(payload) }),
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
  await owner`INSERT INTO organizations (id, name, slug) VALUES (${ORG}, 'Acme', 'acme')`;
  app = new SQL(databaseUrl(true));
  setPool(app);
  adminKey = (await createApiKey({ organizationId: ORG, name: "Márcia" })).key;
  memberKey = (await createApiKey({ organizationId: ORG, name: "Eli" })).key;
  [adminPerson, memberPerson] = (await owner`SELECT id FROM people WHERE organization_id = ${ORG} ORDER BY role = 'admin' DESC, name`)
    .map((r: { id: string }) => r.id);
  for (const [person, email] of [[adminPerson, "marcia@example.com"], [memberPerson, "eli@example.com"]] as const) {
    await owner`INSERT INTO accounts (id, email, name) VALUES (${`acc_${person}`}, ${email}, ${email})`;
    await owner`UPDATE people SET account_id = ${`acc_${person}`} WHERE id = ${person}`;
  }
  orchestratorServer = Bun.serve({
    port: 0,
    async fetch(req) {
      const path = new URL(req.url).pathname + new URL(req.url).search;
      const body = await req.text();
      sent.push({ path, headers: Object.fromEntries(req.headers), body });
      if (path === "/internal/chat/providers") {
        return Response.json({ providers: [{ id: "slack", name: "Slack", glyph: "slack", workspaceWord: "workspace", configured: true }],
          builtIn: "You route a message.", facts: { confidenceBar: 0.75, clarifyRounds: 2, epicTasksMax: 12 } });
      }
      if (path.startsWith("/internal/chat/discord")) return Response.json({ error: { code: "not_found", message: "no such chat provider" } }, { status: 404 });
      if (path.endsWith("/install") || path.endsWith("/connect")) return Response.json({ url: "https://slack.example.com/oauth?state=s" });
      if (path.endsWith("/callback")) {
        if (callbackFails) return Response.json({ error: { code: "internal", message: "internal error" } }, { status: 500 });
        return Response.json(callbackAnswer);
      }
      if (path.endsWith("/events") || path.endsWith("/interactions")) {
        if (webhookMode === "unsigned") return Response.json({ error: { code: "unauthorized", message: "not signed" } }, { status: 401 });
        if (webhookMode === "hang") await Bun.sleep(4_000);
        return Response.json(path.endsWith("/events") ? { challenge: "c-1" } : { ok: true });
      }
      if (path.startsWith("/internal/chat/slack/connect/state")) return Response.json({ status: "ready", workspaceName: "Acme Slack" });
      if (req.method === "DELETE") return new Response(null, { status: 204 });
      return Response.json({});
    },
  });
  useConfig(Config.load({ env: { ...process.env, DUDE_AUTH_PUBLIC_URL: "https://dude.example.com",
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

const last = () => sent.at(-1)!;

describe("webhooks", () => {
  test("a provider's delivery reaches the orchestrator with its body and its own headers, and its answer comes back", async () => {
    const raw = `{"type":"url_verification","challenge":"c-1"}`;
    const res = await call(null, "POST", "/v1/webhooks/chat/slack/events", raw,
      { "x-slack-signature": "v0=abc", "x-slack-request-timestamp": "1700000000", "x-dude-person": "per_forged" });
    expect(res.status).toBe(200);
    expect(await res.json()).toEqual({ challenge: "c-1" });
    const got = last();
    expect(got.path).toBe("/internal/chat/slack/events");
    expect(got.body).toBe(raw);
    expect(got.headers["x-chat-header-x-slack-signature"]).toBe("v0=abc");
    expect(got.headers["x-chat-header-x-slack-request-timestamp"]).toBe("1700000000");
    expect(got.headers.authorization).toBe("Bearer svc");
    // Nothing the caller sends becomes who is asking.
    expect(Object.keys(got.headers).filter((h) => h.includes("dude-person")).length).toBe(0);
  });

  test("any provider's x- headers are passed on, named by no provider here, the backend's own not", async () => {
    await call(null, "POST", "/v1/webhooks/chat/slack/events", "{}",
      { "x-other-signature": "sig", "x-forwarded-for": "10.0.0.1", "x-request-id": "r-1" });
    const got = last().headers;
    expect(got["x-chat-header-x-other-signature"]).toBe("sig");
    expect(got["x-chat-header-x-forwarded-for"]).toBeUndefined();
    expect(got["x-chat-header-x-request-id"]).toBeUndefined();
  });

  test("a click reaches the orchestrator's interactions route as the form it is", async () => {
    const form = "payload=%7B%22type%22%3A%22block_actions%22%7D";
    const res = await call(null, "POST", "/v1/webhooks/chat/slack/interactions", form,
      { "content-type": "application/x-www-form-urlencoded", "x-slack-signature": "v0=1" });
    expect(res.status).toBe(200);
    expect(last().path).toBe("/internal/chat/slack/interactions");
    expect(last().body).toBe(form);
    expect(last().headers["x-chat-header-content-type"]).toBe("application/x-www-form-urlencoded");
  });

  test("the orchestrator's refusal comes back as it was, and one too slow is a 503 in under 3 s", async () => {
    webhookMode = "unsigned";
    try {
      expect((await call(null, "POST", "/v1/webhooks/chat/slack/events", "{}")).status).toBe(401);
      expect((await call(null, "POST", "/v1/webhooks/chat/slack/interactions", "payload=x")).status).toBe(401);
      webhookMode = "hang";
      const started = Date.now();
      const res = await call(null, "POST", "/v1/webhooks/chat/slack/events", "{}");
      expect(res.status).toBe(503);
      expect(Date.now() - started).toBeLessThan(3_000);
    } finally {
      webhookMode = "ok";
    }
    expect((await call(null, "POST", "/v1/webhooks/chat/discord/events", "{}")).status).toBe(404);
  });

  test("the connect page's state is read as the signed-in person", async () => {
    const res = await call(memberKey, "GET", "/v1/chat/slack/connect/state?state=p%2B1");
    expect(res.status).toBe(200);
    expect(last().path).toBe("/internal/chat/slack/connect/state?state=p%2B1");
    expect(last().headers["x-dude-person"]).toBe(memberPerson);
  });

  test("a Connect link's state is located in its organisation, and nothing else of it", async () => {
    const state = "prompt-state-1";
    const hash = createHash("sha256").update(state).digest("hex");
    await owner`INSERT INTO chat_connect_states (state_hash, organization_id, kind, provider, external_workspace_id, external_user_id, expires_at)
      VALUES (${hash}, ${ORG}, 'prompt', 'slack', 'T1', 'U9', now() + interval '15 minutes')`;
    const found = await call(memberKey, "GET", `/v1/locate/${state}`);
    expect(found.status).toBe(200);
    expect(await found.json()).toEqual({ organization: { id: ORG, name: "Acme", slug: "acme" }, kind: "connect", id: state });
    expect((await call(memberKey, "GET", "/v1/locate/not-a-state")).status).toBe(404);
    await owner`DELETE FROM chat_connect_states WHERE state_hash = ${hash}`;
  });
});

describe("install and connect", () => {
  test("only an admin begins an install; the orchestrator gets the cookie's hash and the public callback", async () => {
    expect((await call(memberKey, "POST", "/v1/chat/slack/install", {})).status).toBe(403);
    const res = await call(adminKey, "POST", "/v1/chat/slack/install", {});
    expect(res.status).toBe(200);
    expect(await res.json()).toEqual({ url: "https://slack.example.com/oauth?state=s" });
    const cookie = /dude_chat_oauth=([^;]+)/.exec(res.headers.get("set-cookie") ?? "")![1]!;
    expect(res.headers.get("set-cookie")).toContain("HttpOnly");
    const begun = JSON.parse(last().body);
    expect(begun).toEqual({ browserHash: createHash("sha256").update(cookie).digest("hex"),
      redirect: "https://dude.example.com/v1/chat/slack/install/callback" });
    expect(last().headers["x-dude-person"]).toBe(adminPerson);
  });

  test("a member begins a link, from the connect page with its prompt", async () => {
    const res = await call(memberKey, "POST", "/v1/chat/slack/connect", { prompt: "p-1" });
    expect(res.status).toBe(200);
    expect(JSON.parse(last().body).prompt).toBe("p-1");
    expect(JSON.parse(last().body).redirect).toBe("https://dude.example.com/v1/chat/slack/connect/callback");
    expect(last().headers["x-dude-person"]).toBe(memberPerson);
  });

  test("a callback passes the state, code and this browser's cookie hash, and lands by outcome", async () => {
    callbackAnswer = { outcome: "connected", slug: "acme", returnTo: "settings" };
    let res = await call(null, "GET", "/v1/chat/slack/connect/callback?state=st&code=cd", undefined, { cookie: "x=1; dude_chat_oauth=ck" });
    expect(res.status).toBe(303);
    expect(res.headers.get("location")).toBe("/?chat=slack&outcome=connected#/o/acme/me/settings");
    expect(JSON.parse(last().body)).toEqual({ state: "st", code: "cd", error: "", browserHash: createHash("sha256").update("ck").digest("hex") });
    callbackAnswer = { outcome: "already_linked", slug: "acme", returnTo: "connect" };
    res = await call(null, "GET", "/v1/chat/slack/connect/callback?state=st&error=access_denied");
    expect(res.headers.get("location")).toBe("/?chat=slack&outcome=already_linked#/o/acme/connect/slack");
    // No cookie: no hash, which the orchestrator refuses as invalid_or_expired.
    expect(JSON.parse(last().body).browserHash).toBe("");
    callbackAnswer = { outcome: "workspace_taken", slug: "acme", returnTo: "install" };
    res = await call(null, "GET", "/v1/chat/slack/install/callback?state=st&code=cd");
    expect(res.headers.get("location")).toBe("/?chatInstall=slack&outcome=workspace_taken#/o/acme/org/settings/chat/slack");
    expect((await call(null, "GET", "/v1/chat/discord/install/callback?state=st")).status).toBe(404);
    // The orchestrator failing: the landing says so.
    callbackFails = true;
    try {
      res = await call(null, "GET", "/v1/chat/slack/connect/callback?state=st&code=cd");
      expect(res.status).toBe(303);
      expect(res.headers.get("location")).toBe("/?chat=slack&outcome=failed#/me/settings");
    } finally {
      callbackFails = false;
    }
  });
});

describe("settings", () => {
  test("providers and an installation: facts for everyone, health and people for admins", async () => {
    let overview = await (await call(memberKey, "GET", "/v1/organization/chat")).json() as Json;
    expect(overview.providers).toEqual([{ provider: { id: "slack", name: "Slack", glyph: "slack", workspaceWord: "workspace" },
      installed: false, workspaceName: null, installedAt: null, connected: null }]);
    expect(overview.canEdit).toBe(false);
    expect(overview.admins.map((a: Json) => a.id)).toEqual([adminPerson]);

    await owner`INSERT INTO chat_installations (id, organization_id, provider, external_workspace_id, workspace_name, workspace_domain,
      bot_token, installed_by, failed_today, failures_day, last_failure)
      VALUES ('chi_1', ${ORG}, 'slack', 'T1', 'Acme Slack', 'acme', 'xoxb-secret', ${adminPerson}, 2, current_date, 'slack chat.postMessage: not_in_channel')`;
    const account = (await owner`SELECT account_id FROM people WHERE id = ${memberPerson}`)[0].account_id;
    await owner`INSERT INTO chat_identities (organization_id, provider, external_workspace_id, external_user_id, account_id, person_id, handle, display_name)
      VALUES (${ORG}, 'slack', 'T1', 'U2', ${account}, ${memberPerson}, 'eli', 'Eli Park')`;
    overview = await (await call(memberKey, "GET", "/v1/organization/chat")).json() as Json;
    expect(overview.providers[0].installed).toBe(true);
    expect(overview.providers[0].connected.handle).toBe("eli");

    const asMember = await (await call(memberKey, "GET", "/v1/organization/chat/slack")).json() as Json;
    expect(asMember.workspaceName).toBe("Acme Slack");
    expect(asMember.connected).toEqual({ handle: "eli", displayName: "Eli Park", linkedAt: expect.any(String) });
    expect(asMember.health).toBeNull();
    expect(asMember.people).toBeNull();
    expect(JSON.stringify(asMember)).not.toContain("xoxb-secret");
    const asAdmin = await (await call(adminKey, "GET", "/v1/organization/chat/slack")).json() as Json;
    expect(asAdmin.health).toEqual({ lastEventAt: null, failedToday: 2, lastFailure: "slack chat.postMessage: not_in_channel" });
    expect(asAdmin.people.map((p: Json) => p.handle)).toEqual(["eli"]);
    expect(asAdmin.connected).toBeNull();
    expect(JSON.stringify(asAdmin)).not.toContain("xoxb-secret");
    expect((await call(memberKey, "GET", "/v1/organization/chat/discord")).status).toBe(404);
    expect((await (await call(memberKey, "GET", "/v1/me/chat/slack")).json() as Json).connected.handle).toBe("eli");
  });

  test("routing instructions: everyone reads, admins write, the provider's layer after the organisation's", async () => {
    expect(await (await call(memberKey, "GET", "/v1/organization/routing-instructions")).json())
      .toEqual({ builtIn: "You route a message.", additional: "", canEdit: false });
    expect((await call(memberKey, "PUT", "/v1/organization/routing-instructions", { additional: "x" })).status).toBe(403);
    expect((await call(adminKey, "PUT", "/v1/organization/routing-instructions", { additional: "x".repeat(4001) })).status).toBe(400);
    const put = await call(adminKey, "PUT", "/v1/organization/routing-instructions", { additional: "  Billing work goes in ABS.  " });
    expect((await put.json() as Json).additional).toBe("Billing work goes in ABS.");
    expect((await call(memberKey, "PUT", "/v1/organization/chat/slack/routing-instructions", { additional: "y" })).status).toBe(403);
    await call(adminKey, "PUT", "/v1/organization/chat/slack/routing-instructions", { additional: "Prefer sessions." });
    expect(await (await call(memberKey, "GET", "/v1/organization/chat/slack/routing-instructions")).json()).toEqual({
      builtIn: "You route a message.", organisation: "Billing work goes in ABS.", additional: "Prefer sessions.", installed: true, canEdit: false });
    expect((await owner`SELECT settings->>'additional_instructions' AS a FROM chat_installations WHERE id = 'chi_1'`)[0].a).toBe("Prefer sessions.");
  });

  test("Requests are an admin's, newest first, filtered by provider and route", async () => {
    const insert = (id: string, route: string, minutes: number, extra: { result_kind?: string; result_id?: string } = {}) => owner`
      INSERT INTO chat_requests (id, organization_id, provider, event_id, external_workspace_id, conversation_id, conversation_kind,
        conversation_name, external_user_id, external_user_name, person_id, text, route, confidence, reasoning, shared_added, shared_skipped,
        result_kind, result_id, created_at)
      VALUES (${id}, ${ORG}, 'slack', ${id}, 'T1', 'C1', 'private', '#design', 'U2', 'Eli Park', ${memberPerson}, 'help', ${route}, 0.8, 'why',
        ${`{${adminPerson}}`}::text[], '{U9}'::text[], ${extra.result_kind ?? null}, ${extra.result_id ?? null}, now() - make_interval(mins => ${minutes}))`;
    await owner`INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES ('prj_1', ${ORG}, 'Billing', 'abs', 'ABS')`;
    await owner`INSERT INTO tasks (id, organization_id, project_id, number, title) VALUES ('wi_1', ${ORG}, 'prj_1', 7, 'Stream it')`;
    await insert("chr_1", "task", 5, { result_kind: "task", result_id: "wi_1" });
    await insert("chr_2", "not_connected", 1);
    await insert("chr_old", "brainstorm", 60 * 24 * 31);
    expect((await call(memberKey, "GET", "/v1/organization/chat/requests")).status).toBe(403);
    const all = await (await call(adminKey, "GET", "/v1/organization/chat/requests")).json() as Json;
    expect(all.requests.map((r: Json) => r.id)).toEqual(["chr_2", "chr_1"]);
    const task = all.requests[1];
    expect(task.result).toEqual({ kind: "task", id: "wi_1", label: "ABS-7 Stream it" });
    expect(task.person.id).toBe(memberPerson);
    expect(task.sharedAdded.map((p: Json) => p.id)).toEqual([adminPerson]);
    expect(task.sharedSkipped).toEqual(["U9"]);
    expect(task.confidence).toBeCloseTo(0.8);
    const filtered = await (await call(adminKey, "GET", "/v1/organization/chat/requests?route=task&provider=slack")).json() as Json;
    expect(filtered.requests.map((r: Json) => r.id)).toEqual(["chr_1"]);
    expect((await (await call(adminKey, "GET", "/v1/organization/chat/requests?provider=discord")).json() as Json).requests).toEqual([]);
    expect((await call(adminKey, "GET", "/v1/organization/chat/requests?route=shout")).status).toBe(400);
    // Each filter statement filters on what it names: a request from another provider on the same route.
    await insert("chr_d", "task", 3);
    await owner`UPDATE chat_requests SET provider = 'discord' WHERE id = 'chr_d'`;
    const ids = async (qs: string) =>
      ((await (await call(adminKey, "GET", `/v1/organization/chat/requests${qs}`)).json() as Json).requests as Json[]).map((r) => r.id);
    expect(await ids("?route=task&provider=slack")).toEqual(["chr_1"]);
    expect(await ids("?route=task")).toEqual(["chr_d", "chr_1"]);
    expect(await ids("?route=not_connected")).toEqual(["chr_2"]);
    expect(await ids("?provider=discord")).toEqual(["chr_d"]);
    // A page of the newest few, and the week's count over every match, not over the page.
    expect(await ids("?limit=2")).toEqual(["chr_2", "chr_d"]);
    expect(await ids("?route=task&limit=1")).toEqual(["chr_d"]);
    const page = await (await call(adminKey, "GET", "/v1/organization/chat/requests?limit=1&summary=1")).json() as Json;
    expect(page.requests).toHaveLength(1);
    expect(page.week).toEqual({ mentions: 3, asked: 3 });
    expect((await (await call(adminKey, "GET", "/v1/organization/chat/requests?route=task&summary=1")).json() as Json).week)
      .toEqual({ mentions: 2, asked: 2 });
    expect((await call(adminKey, "GET", "/v1/organization/chat/requests?limit=201")).status).toBe(400);
  });

  test("Requests list at most 200, newest first", async () => {
    await owner`
      INSERT INTO chat_requests (id, organization_id, provider, event_id, external_workspace_id, conversation_id, external_user_id, route, created_at)
      SELECT 'chr_n' || lpad(g::text, 3, '0'), ${ORG}, 'slack', 'ev_n' || g, 'T1', 'C1', 'U2', 'task', now() - make_interval(secs => g / 1000.0)
      FROM generate_series(1, 201) g`;
    for (const qs of ["", "?route=task", "?provider=slack", "?route=task&provider=slack"]) {
      const rows = ((await (await call(adminKey, "GET", `/v1/organization/chat/requests${qs}`)).json() as Json).requests as Json[]);
      expect([qs, rows.length, rows[0]?.id, rows[199]?.id]).toEqual([qs, 200, "chr_n001", "chr_n200"]);
    }
  });

  test("disconnect is an admin's, and unlinking one's own", async () => {
    expect((await call(memberKey, "DELETE", "/v1/organization/chat/slack")).status).toBe(403);
    expect((await call(adminKey, "DELETE", "/v1/organization/chat/slack")).status).toBe(204);
    expect(last().path).toBe("/internal/chat/slack");
    expect((await call(memberKey, "DELETE", "/v1/me/chat/slack")).status).toBe(204);
    expect(last().path).toBe("/internal/me/chat/slack");
    expect(last().headers["x-dude-person"]).toBe(memberPerson);
  });
});
