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

/** A routing call the orchestrator recorded, for a request to name as its model call. */
const routingCall = (id: string) => owner`INSERT INTO model_calls (id, organization_id, purpose, asker, tier_name, model, question,
    request, status, latency_ms) VALUES (${id}, ${ORG}, 'judgement', 'chat.route', 'Fast', 'judge-model', 'Route it', '{}'::jsonb, 'ok', 900)`;

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
    // An install's or a sign-in's state is never located: only a prompt's link is shown to a browser.
    for (const kind of ["install", "oauth"]) {
      const other = `${kind}-state-1`;
      await owner`INSERT INTO chat_connect_states (state_hash, organization_id, kind, provider, person_id, expires_at)
        VALUES (${createHash("sha256").update(other).digest("hex")}, ${ORG}, ${kind}, 'slack', ${memberPerson}, now() + interval '15 minutes')`;
      expect([kind, (await call(memberKey, "GET", `/v1/locate/${other}`)).status]).toEqual([kind, 404]);
    }
    await owner`DELETE FROM chat_connect_states WHERE organization_id = ${ORG}`;
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
      installed: false, workspaceName: null, workspaceDomain: null, installedAt: null, connected: null }]);
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
    expect(overview.providers[0].workspaceDomain).toBe("acme");
    // Their own mentions of the last 30 days on this provider, not anyone else's nor another provider's.
    await owner`INSERT INTO chat_requests (id, organization_id, provider, event_id, external_workspace_id, conversation_id, external_user_id, person_id, route, created_at)
      VALUES ('chr_m1', ${ORG}, 'slack', 'ev_m1', 'T1', 'C1', 'U2', ${memberPerson}, 'task', now()),
             ('chr_m2', ${ORG}, 'slack', 'ev_m2', 'T1', 'C1', 'U2', ${memberPerson}, 'task', now() - interval '31 days'),
             ('chr_m3', ${ORG}, 'slack', 'ev_m3', 'T1', 'C1', 'U1', ${adminPerson}, 'task', now()),
             ('chr_m4', ${ORG}, 'discord', 'ev_m4', 'T1', 'C1', 'U2', ${memberPerson}, 'task', now())`;
    overview = await (await call(memberKey, "GET", "/v1/organization/chat")).json() as Json;
    expect(overview.providers[0].connected.mentions).toBe(1);
    await owner`DELETE FROM chat_requests WHERE id IN ('chr_m1', 'chr_m2', 'chr_m3', 'chr_m4')`;

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
    // No route returns the bot token, to a member or to an admin. Requests
    // has a row carrying the installation's stored failure, as a reply
    // refused by the provider records it.
    await owner`INSERT INTO chat_requests (id, organization_id, provider, event_id, external_workspace_id, conversation_id, external_user_id,
        person_id, route, text, reasoning)
      SELECT 'chr_t1', ${ORG}, 'slack', 'ev_t1', 'T1', 'C1', 'U2', ${memberPerson}, 'failed', 'file it',
        '(its reply was not posted: ' || last_failure || ')' FROM chat_installations WHERE id = 'chi_1'`;
    const requests = await (await call(adminKey, "GET", "/v1/organization/chat/requests")).json() as Json;
    expect(requests.requests.length).toBe(1);
    expect(JSON.stringify(requests)).toContain("not_in_channel");
    for (const key of [memberKey, adminKey]) {
      const paths = ["/v1/organization/chat", "/v1/organization/chat/slack", "/v1/me/chat/slack"];
      for (const path of key === adminKey ? [...paths, "/v1/organization/chat/requests"] : paths) {
        const res = await call(key, "GET", path);
        expect(res.status).toBe(200);
        expect(await res.text()).not.toContain("xoxb");
      }
    }
    await owner`DELETE FROM chat_requests WHERE id = 'chr_t1'`;
    expect((await call(memberKey, "GET", "/v1/organization/chat/discord")).status).toBe(404);
    expect((await (await call(memberKey, "GET", "/v1/me/chat/slack")).json() as Json).connected.handle).toBe("eli");
  });

  test("routing instructions: everyone reads, admins write, the provider's layer after the organisation's", async () => {
    expect(await (await call(memberKey, "GET", "/v1/organization/routing-instructions")).json())
      .toEqual({ builtIn: "You route a message.", additional: "", canEdit: false });
    expect((await call(memberKey, "PUT", "/v1/organization/routing-instructions", { additional: "x" })).status).toBe(403);
    expect((await call(adminKey, "PUT", "/v1/organization/routing-instructions", { additional: "x".repeat(4001) })).status).toBe(400);
    const put = await call(adminKey, "PUT", "/v1/organization/routing-instructions", { additional: "  Billing work goes in BIL.  " });
    expect((await put.json() as Json).additional).toBe("Billing work goes in BIL.");
    expect((await call(memberKey, "PUT", "/v1/organization/chat/slack/routing-instructions", { additional: "y" })).status).toBe(403);
    await call(adminKey, "PUT", "/v1/organization/chat/slack/routing-instructions", { additional: "Prefer sessions." });
    expect(await (await call(memberKey, "GET", "/v1/organization/chat/slack/routing-instructions")).json()).toEqual({
      builtIn: "You route a message.", organisation: "Billing work goes in BIL.", additional: "Prefer sessions.", installed: true, canEdit: false });
    expect((await owner`SELECT settings->>'additional_instructions' AS a FROM chat_installations WHERE id = 'chi_1'`)[0].a).toBe("Prefer sessions.");
  });

  test("Requests are an admin's, newest first, filtered by provider and route", async () => {
    await routingCall("mcl_r");
    const insert = (id: string, route: string, minutes: number, extra: { result_kind?: string; result_id?: string } = {}) => owner`
      INSERT INTO chat_requests (id, organization_id, provider, event_id, external_workspace_id, conversation_id, conversation_kind,
        conversation_name, external_user_id, external_user_name, person_id, text, route, confidence, reasoning, shared_added, shared_skipped,
        result_kind, result_id, model_call_id, created_at)
      VALUES (${id}, ${ORG}, 'slack', ${id}, 'T1', 'C1', 'private', '#design', 'U2', 'Eli Park', ${memberPerson}, 'help', ${route}, 0.8, 'why',
        ${`{${adminPerson}}`}::text[], '{U9}'::text[], ${extra.result_kind ?? null}, ${extra.result_id ?? null}, 'mcl_r',
        now() - make_interval(mins => ${minutes}))`;
    await owner`INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES ('prj_1', ${ORG}, 'Billing', 'bil', 'BIL')`;
    await owner`INSERT INTO tasks (id, organization_id, project_id, number, title) VALUES ('wi_1', ${ORG}, 'prj_1', 7, 'Stream it')`;
    await insert("chr_1", "task", 5, { result_kind: "task", result_id: "wi_1" });
    await insert("chr_2", "not_connected", 1);
    await insert("chr_old", "brainstorm", 60 * 24 * 31);
    expect((await call(memberKey, "GET", "/v1/organization/chat/requests")).status).toBe(403);
    const all = await (await call(adminKey, "GET", "/v1/organization/chat/requests")).json() as Json;
    expect(all.requests.map((r: Json) => r.id)).toEqual(["chr_2", "chr_1"]);
    const task = all.requests[1];
    expect(task.result).toEqual({ kind: "task", id: "wi_1", label: "BIL-7 Stream it" });
    expect(task.person.id).toBe(memberPerson);
    expect(task.sharedAdded.map((p: Json) => p.id)).toEqual([adminPerson]);
    expect(task.sharedSkipped).toEqual(["U9"]);
    expect(task.linked).toEqual([]);
    // A project linked from the request's reply is on its row, by whom, oldest first.
    await owner`INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES ('prj_2', ${ORG}, 'Operations', 'ops', 'OPS')`;
    await owner`INSERT INTO chat_request_links (request_id, organization_id, project_id, project_key, project_name, linked_by, linked_by_name, event_id, created_at)
      VALUES ('chr_1', ${ORG}, 'prj_2', 'OPS', 'Operations', ${memberPerson}, 'Eli Park', 'ev_a', now() - interval '2 minutes'),
        ('chr_1', ${ORG}, 'prj_1', 'BIL', 'Billing', ${adminPerson}, 'Márcia', 'ev_b', now() - interval '1 minute')`;
    const withLinks = (await (await call(adminKey, "GET", "/v1/organization/chat/requests")).json() as Json).requests;
    expect(withLinks[1].linked).toEqual([{ key: "OPS", name: "Operations", by: "Eli Park" }, { key: "BIL", name: "Billing", by: "Márcia" }]);
    expect(withLinks[0].linked).toEqual([]);
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
    // The week asks the model only of rows whose routing called it (a model
    // call recorded), counts 7 days and not 8, and keeps to its provider.
    await insert("chr_nc", "not_connected", 2);
    await owner`UPDATE chat_requests SET confidence = NULL, model_call_id = NULL WHERE id = 'chr_nc'`;
    await insert("chr_tl", "task_link", 4);
    await owner`UPDATE chat_requests SET confidence = NULL, model_call_id = NULL WHERE id = 'chr_tl'`;
    await insert("chr_8d", "brainstorm", 60 * 24 * 8);
    const week = async (qs: string) =>
      ((await (await call(adminKey, "GET", `/v1/organization/chat/requests?summary=1${qs}`)).json() as Json).week);
    expect(await week("")).toEqual({ mentions: 5, asked: 3 });
    expect(await week("&provider=discord")).toEqual({ mentions: 1, asked: 1 });
    expect(await week("&provider=slack")).toEqual({ mentions: 4, asked: 2 });
  });

  test("a clarifying question's clicked choice did not ask the model again; its row is still 100% sure", async () => {
    await routingCall("mcl_m1");
    await routingCall("mcl_q1");
    const insert = (id: string, route: string, confidence: number, modelCall: string | null) => owner`
      INSERT INTO chat_requests (id, organization_id, provider, event_id, external_workspace_id, conversation_id, external_user_id,
        route, confidence, model_call_id, created_at)
      VALUES (${id}, ${ORG}, 'chatter', ${id}, 'T1', 'C1', 'U2', ${route}, ${confidence}, ${modelCall}, now() - interval '1 minute')`;
    await insert("chr_m1", "brainstorm", 0.9, "mcl_m1");
    await insert("chr_q1", "clarify", 0.6, "mcl_q1");
    // The asker's click, routed with confidence 1 and no call of its own.
    await insert("chr_c1", "task", 1, null);
    const page = await (await call(adminKey, "GET", "/v1/organization/chat/requests?provider=chatter&summary=1")).json() as Json;
    expect(page.week).toEqual({ mentions: 3, asked: 2 });
    expect(page.requests.find((r: Json) => r.id === "chr_c1").confidence).toBe(1);
  });

  test("a failed request's cause is its own field, an admin's alone, as the orchestrator stored it; a request that did not fail has none", async () => {
    // The orchestrator redacts before it stores; the route returns the text as it is.
    const insert = (id: string, route: string, reasoning: string, failure: string | null, result: string | null) => owner`
      INSERT INTO chat_requests (id, organization_id, provider, event_id, external_workspace_id, conversation_id, external_user_id,
        route, asked_route, reasoning, failure, result_kind, result_id, created_at)
      VALUES (${id}, ${ORG}, 'slack', ${id}, 'T1', 'C1', 'U2', ${route}, ${route === "failed" ? "task" : null}, ${reasoning}, ${failure},
        ${result ? "session" : null}, ${result}, now() - interval '3 hours')`;
    await owner`INSERT INTO sessions (id, organization_id, title) VALUES ('ses_fb', ${ORG}, 'Exports')`;
    await insert("chr_f1", "failed", "A clear task. (its reply was not posted: slack chat.postMessage: invalid_auth [REDACTED:bot_token])",
      "its reply was not posted: slack chat.postMessage: invalid_auth [REDACTED:bot_token]", null);
    await insert("chr_f2", "failed", "the routing call failed: the LLM proxy answered 400: Basic question about exports",
      "the routing call failed: the LLM proxy answered 400: Basic question about exports", "ses_fb");
    await insert("chr_f3", "task", "A clear task.", null, null);
    try {
      expect((await call(memberKey, "GET", "/v1/organization/chat/requests?route=failed")).status).toBe(403);
      const res = await call(adminKey, "GET", "/v1/organization/chat/requests?provider=slack");
      const byId = new Map(((await res.json() as Json).requests as Json[]).map((r) => [r.id, r]));
      expect(byId.get("chr_f1").failure).toBe("its reply was not posted: slack chat.postMessage: invalid_auth [REDACTED:bot_token]");
      expect(byId.get("chr_f1").reasoning).toBe("A clear task. (its reply was not posted: slack chat.postMessage: invalid_auth [REDACTED:bot_token])");
      expect(byId.get("chr_f1").result).toBeNull();
      expect(byId.get("chr_f2").failure).toBe("the routing call failed: the LLM proxy answered 400: Basic question about exports");
      expect(byId.get("chr_f2").result).toEqual({ kind: "session", id: "ses_fb", label: "Exports" });
      expect(byId.get("chr_f3").failure).toBeNull();
      expect(byId.get("chr_f3").reasoning).toBe("A clear task.");
      // The route filter's Failed finds both failures, with or without a fallback.
      const failed = (await (await call(adminKey, "GET", "/v1/organization/chat/requests?route=failed")).json() as Json).requests as Json[];
      expect(failed.map((r) => r.id).filter((id: string) => id.startsWith("chr_f")).sort()).toEqual(["chr_f1", "chr_f2"]);
    } finally {
      await owner`DELETE FROM chat_requests WHERE id IN ('chr_f1', 'chr_f2', 'chr_f3')`;
      await owner`DELETE FROM sessions WHERE id = 'ses_fb'`;
    }
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

  test("a channel's usual projects and the hidden ones: everyone reads, admins write, each change in the ledger", async () => {
    await owner`INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES ('prj_abs', ${ORG}, 'Acme Backend', 'abs', 'ABS'),
      ('prj_sec', ${ORG}, 'Secret', 'sec', 'SEC') ON CONFLICT DO NOTHING`;
    await owner`INSERT INTO chat_requests (id, organization_id, provider, event_id, external_workspace_id, conversation_id, conversation_kind,
      conversation_name, external_user_id, route) VALUES ('chr_ch1', ${ORG}, 'slack', 'EvCh1', 'T1', 'C77', 'public', '#nightly', 'U1', 'brainstorm')`;
    const view = await (await call(memberKey, "GET", "/v1/organization/chat/slack/projects")).json() as Json;
    expect(view.canEdit).toBe(false);
    expect(view.hidden).toEqual([]);
    expect(view.channels.find((c: Json) => c.conversationId === "C77")).toEqual({ conversationId: "C77", conversationName: "#nightly", projects: [] });
    expect(view.projects.map((p: Json) => p.key)).toContain("ABS");

    const usual = (key: string, ids: string[]) => call(key, "PUT", "/v1/organization/chat/slack/channels/C77/projects",
      { conversationName: "#nightly", projectIds: ids });
    expect((await usual(memberKey, ["prj_abs"])).status).toBe(403);
    expect((await usual(adminKey, ["prj_nope"])).status).toBe(404);
    const set = await (await usual(adminKey, ["prj_abs", "prj_sec"])).json() as Json;
    expect(set.channels.find((c: Json) => c.conversationId === "C77").projects.map((p: Json) => p.key)).toEqual(["ABS", "SEC"]);
    await usual(adminKey, ["prj_abs"]);
    expect([...await owner`SELECT project_id FROM chat_channel_projects WHERE conversation_id = 'C77'`]).toEqual([{ project_id: "prj_abs" }]);

    const hide = (key: string, ids: string[]) => call(key, "PUT", "/v1/organization/chat/slack/hidden-projects", { projectIds: ids });
    expect((await hide(memberKey, ["prj_sec"])).status).toBe(403);
    const hidden = await (await hide(adminKey, ["prj_sec"])).json() as Json;
    expect(hidden.hidden.map((p: Json) => p.key)).toEqual(["SEC"]);
    expect((await (await call(memberKey, "GET", "/v1/organization/chat/slack/projects")).json() as Json).hidden.map((p: Json) => p.key)).toEqual(["SEC"]);
    expect([...await owner`SELECT event_type, payload FROM events WHERE organization_id = ${ORG}
      AND event_type IN ('chat_channel.projects_set', 'chat_provider.hidden_projects_set') ORDER BY cursor`].map((e: Json) => [e.event_type, e.payload]))
      .toEqual([
        ["chat_channel.projects_set", { provider: "slack", conversationId: "C77", conversationName: "#nightly", added: ["prj_abs", "prj_sec"], removed: [], via: "settings" }],
        ["chat_channel.projects_set", { provider: "slack", conversationId: "C77", conversationName: "#nightly", added: [], removed: ["prj_sec"], via: "settings" }],
        ["chat_provider.hidden_projects_set", { provider: "slack", hidden: ["prj_sec"], shown: [] }],
      ]);
    await hide(adminKey, []);
  });

  test("a hidden project is no channel's usual project: left out of the read, refused on save, and back once shown", async () => {
    await owner`INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES ('prj_abs', ${ORG}, 'Acme Backend', 'abs', 'ABS'),
      ('prj_sec', ${ORG}, 'Secret', 'sec', 'SEC') ON CONFLICT DO NOTHING`;
    const usual = (ids: string[]) => call(adminKey, "PUT", "/v1/organization/chat/slack/channels/C78/projects",
      { conversationName: "#ops", projectIds: ids });
    const hide = (ids: string[]) => call(adminKey, "PUT", "/v1/organization/chat/slack/hidden-projects", { projectIds: ids });
    const keys = (view: Json) => view.channels.find((c: Json) => c.conversationId === "C78").projects.map((p: Json) => p.key);
    expect(keys(await (await usual(["prj_abs", "prj_sec"])).json() as Json)).toEqual(["ABS", "SEC"]);
    await hide(["prj_sec"]);
    // GET, as a member reads it: SEC is out.
    expect(keys(await (await call(memberKey, "GET", "/v1/organization/chat/slack/projects")).json() as Json)).toEqual(["ABS"]);
    // PUT naming it is refused, and changes nothing.
    const refused = await usual(["prj_abs", "prj_sec"]);
    expect(refused.status).toBe(409);
    expect((await refused.json() as Json).error.code).toBe("hidden_from_chat");
    // Saving the set as shown keeps the hidden row, which comes back once it is shown.
    expect(keys(await (await usual(["prj_abs"])).json() as Json)).toEqual(["ABS"]);
    await hide([]);
    expect(keys(await (await call(memberKey, "GET", "/v1/organization/chat/slack/projects")).json() as Json)).toEqual(["ABS", "SEC"]);
  });

  test("a task filed from a chat-born session is fenced by its origin, but is not shown as having come from chat", async () => {
    await owner`INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES ('prj_fil', ${ORG}, 'Filed', 'fil', 'FIL')
      ON CONFLICT DO NOTHING`;
    await owner`INSERT INTO tasks (id, organization_id, project_id, number, title, origin) VALUES
      ('wi_via', ${ORG}, 'prj_fil', 1, 'Through a session', '{"provider":"slack","via":"session","from":"ssn_x"}'::jsonb),
      ('wi_direct', ${ORG}, 'prj_fil', 2, 'From Slack', '{"provider":"slack","conversationName":"#nightly"}'::jsonb)`;
    expect((await (await call(adminKey, "GET", "/v1/tasks/wi_via")).json() as Json).origin).toBeNull();
    expect((await (await call(adminKey, "GET", "/v1/tasks/wi_direct")).json() as Json).origin.conversationName).toBe("#nightly");
    const nav = JSON.stringify(await (await call(adminKey, "GET", "/v1/navigation")).json());
    expect(nav).not.toContain("ssn_x");
  });

  test("a task's list row carries its origin without the conversation quoted on it; its own page has it", async () => {
    await owner`INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES ('prj_q', ${ORG}, 'Quoted', 'quo', 'QUO')
      ON CONFLICT DO NOTHING`;
    const channel = Array.from({ length: 6 }, (_, i) => ({ by: "CI Bot", app: true, text: `report ${i} ` + "z".repeat(1900) }));
    await owner`INSERT INTO tasks (id, organization_id, project_id, number, title, origin) VALUES ('wi_quoted', ${ORG}, 'prj_q', 1, 'Quoted',
      ${{ provider: "slack", conversationName: "#nightly", topLevel: true, channel, thread: [{ by: "Ana", text: "a thread line" }] }}::jsonb)`;
    const list = await (await call(adminKey, "GET", "/v1/tasks?projectId=prj_q")).json() as Json;
    const row = list.tasks.find((t: Json) => t.id === "wi_quoted");
    expect(row.origin).toEqual({ provider: "slack", conversationName: "#nightly", topLevel: true });
    expect(JSON.stringify(list).length).toBeLessThan(4000);
    const detail = await (await call(adminKey, "GET", "/v1/tasks/wi_quoted")).json() as Json;
    expect(detail.origin.channel.length).toBe(6);
    expect(detail.origin.thread[0].text).toBe("a thread line");
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
