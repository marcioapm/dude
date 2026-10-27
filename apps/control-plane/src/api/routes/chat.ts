/**
 * Chat providers (Slack first) through the public API.
 *
 * The backend is the only thing a provider or a browser talks to: a
 * provider's webhooks and the OAuth callbacks land here and are passed to
 * the orchestrator, which holds the providers' secrets and tokens, verifies
 * and stores deliveries, and makes every call to the provider. What is read
 * for the settings pages (installations, links, Requests, routing
 * instructions) is read here, under row-level security.
 *
 * An install or a link is begun by a signed-in browser (a POST, which sets
 * a cookie and returns the provider's URL) and finished by a public
 * callback, which is that browser's only if it still carries the cookie:
 * the orchestrator compares the cookie's hash with the state's.
 */

import { createHash, randomBytes } from "node:crypto";
import {
  CHAT_REQUESTS_MAX,
  type ChatProvider,
  type ChatRequestsSummary,
  EventTypes,
  chatChannelProjectsInputSchema,
  chatHiddenProjectsInputSchema,
  chatRequestsQuerySchema,
  routingInstructionsInputSchema,
} from "@dude/domain";
import { type OrgScope, withOrg } from "../../db/client.ts";
import { config } from "../../config.ts";
import { appendInScope } from "../../events/ledger.ts";
import { orchestrator } from "../../orchestrator/client.ts";
import { isOrgAdmin, requireOrgAdmin } from "../access.ts";
import { auditActor } from "../auth.ts";
import { HttpError, json, noContent, notFound, parseBody } from "../http.ts";
import type { PublicContext, RequestContext, Router } from "../router.ts";

const COOKIE = "dude_chat_oauth";
const sha256 = (v: string) => createHash("sha256").update(v).digest("hex");

interface Providers {
  providers: Array<ChatProvider & { configured: boolean }>;
  builtIn: string;
  facts: { confidenceBar: number; clarifyRounds: number; epicTasksMax: number };
}

/** The providers the orchestrator has, with the built-in routing instructions. */
async function providers(ctx: RequestContext): Promise<Providers> {
  const res = await orchestrator(ctx.principal.organizationId, "GET", "/internal/chat/providers", undefined, ctx.principal);
  if (!res.ok) throw new HttpError(res.status, "the orchestrator did not list chat providers", "unavailable");
  return res.json() as Promise<Providers>;
}

async function providerOf(ctx: RequestContext): Promise<{ list: Providers; provider: ChatProvider & { configured: boolean } }> {
  const list = await providers(ctx);
  const provider = list.providers.find((p) => p.id === ctx.params.provider);
  if (!provider) throw notFound("no such chat provider");
  return { list, provider };
}

const strip = ({ configured: _, ...p }: ChatProvider & { configured: boolean }): ChatProvider => p;

/** The organisation's admins, for "Ask an admin". */
const ADMINS = `(SELECT COALESCE(json_agg(person_ref(p) ORDER BY p.name), '[]') FROM people p WHERE p.role = 'admin' AND p.removed_at IS NULL)`;

/** `GET /v1/organization/chat`: each provider, installed here or not, and the caller's own link. */
async function overview(ctx: RequestContext): Promise<Response> {
  const list = await providers(ctx);
  const rows = await withOrg(ctx.principal.organizationId, async ({ sql }) => {
    const installs = await sql`SELECT i.provider, i.workspace_name AS "workspaceName", i.workspace_domain AS "workspaceDomain",
        i.installed_at AS "installedAt",
        (SELECT json_build_object('handle', c.handle, 'displayName', c.display_name, 'linkedAt', c.linked_at,
           'mentions', (SELECT count(*) FROM chat_requests r WHERE r.provider = i.provider AND r.person_id = me.id
             AND r.created_at > now() - interval '30 days'))
         FROM chat_identities c JOIN people me ON me.account_id = c.account_id
         WHERE me.id = ${ctx.principal.personId} AND c.provider = i.provider AND c.external_workspace_id = i.external_workspace_id) AS connected
      FROM chat_installations i ORDER BY i.installed_at`;
    const [{ admins }] = await sql.unsafe(`SELECT ${ADMINS} AS admins`);
    return { installs, admins };
  });
  const byId = new Map(rows.installs.map((r: { provider: string }) => [r.provider, r]));
  return json({
    providers: list.providers.map((p) => {
      const i = byId.get(p.id) as { workspaceName: string; workspaceDomain: string | null; installedAt: string; connected: unknown } | undefined;
      return { provider: strip(p), installed: Boolean(i), workspaceName: i?.workspaceName ?? null, workspaceDomain: i?.workspaceDomain || null,
        installedAt: i?.installedAt ?? null, connected: i?.connected ?? null };
    }),
    canEdit: await isOrgAdmin(ctx),
    admins: rows.admins,
  });
}

/** `GET /v1/organization/chat/:provider`: facts for everyone; health and its people for admins. */
async function installation(ctx: RequestContext): Promise<Response> {
  const { provider } = await providerOf(ctx);
  const admin = await isOrgAdmin(ctx);
  const out = await withOrg(ctx.principal.organizationId, async ({ sql }) => {
    const [i] = await sql`SELECT i.external_workspace_id AS "workspaceId", i.workspace_name AS "workspaceName",
        i.workspace_domain AS "workspaceDomain", i.installed_at AS "installedAt", i.scopes, NULLIF(i.bot_user_id, '') AS "botUser",
        (SELECT person_ref(p) FROM people p WHERE p.id = i.installed_by) AS "installedBy",
        i.last_event_at AS "lastEventAt", CASE WHEN i.failures_day = current_date THEN i.failed_today ELSE 0 END AS "failedToday",
        i.last_failure AS "lastFailure"
      FROM chat_installations i WHERE i.provider = ${provider.id}`;
    const [{ admins }] = await sql.unsafe(`SELECT ${ADMINS} AS admins`);
    if (!i) return { installed: false, admins };
    const people = await sql`SELECT person_ref(p) AS person, c.handle, c.display_name AS "displayName", c.linked_at AS "linkedAt",
        p.id = ${ctx.principal.personId} AS mine
      FROM chat_identities c JOIN people p ON p.account_id = c.account_id AND p.removed_at IS NULL
      WHERE c.provider = ${provider.id} AND c.external_workspace_id = ${i.workspaceId} ORDER BY p.name`;
    const mine = people.find((p: { mine: boolean }) => p.mine);
    return {
      installed: true, admins, workspaceName: i.workspaceName, workspaceDomain: i.workspaceDomain, installedAt: i.installedAt, botUser: i.botUser,
      installedBy: i.installedBy, scopes: i.scopes, connectedCount: people.length,
      connected: mine ? { handle: mine.handle, displayName: mine.displayName, linkedAt: mine.linkedAt } : null,
      health: admin ? { lastEventAt: i.lastEventAt, failedToday: i.failedToday, lastFailure: i.lastFailure } : null,
      people: admin ? people.map(({ mine: _, ...p }: { mine: boolean }) => p) : null,
    };
  });
  return json({
    provider: strip(provider), canEdit: admin, configured: provider.configured,
    workspaceName: null, workspaceDomain: null, installedAt: null, installedBy: null, botUser: null, scopes: [], connectedCount: 0,
    connected: null, health: null, people: null, ...out,
  });
}

/** Where the provider sends the browser back, on the origin people open dude at. */
function callbackUrl(ctx: { url: URL }, provider: string, kind: "install" | "connect"): string {
  const origin = (config().string("DUDE_AUTH_PUBLIC_URL") || ctx.url.origin).replace(/\/+$/, "");
  return `${origin}/v1/chat/${encodeURIComponent(provider)}/${kind}/callback`;
}

/** Begins an install or a link: a cookie for this browser, and the provider's URL. */
function begin(kind: "install" | "connect") {
  return async (ctx: RequestContext): Promise<Response> => {
    if (ctx.principal.credentialKind !== "person" && ctx.principal.credentialKind !== "api_key") throw new HttpError(400, "sign in first", "bad_request");
    if (kind === "install") await requireOrgAdmin(ctx);
    const input = ctx.request.headers.get("content-type")?.includes("json") ? await ctx.request.json().catch(() => ({})) as { prompt?: unknown } : {};
    const cookie = randomBytes(32).toString("base64url");
    const provider = ctx.params.provider!;
    const res = await orchestrator(ctx.principal.organizationId, "POST", `/internal/chat/${encodeURIComponent(provider)}/${kind}`,
      JSON.stringify({ browserHash: sha256(cookie), redirect: callbackUrl(ctx, provider, kind),
        ...(typeof input.prompt === "string" && input.prompt ? { prompt: input.prompt } : {}) }), ctx.principal);
    if (!res.ok) return res;
    const { url } = await res.json() as { url: string };
    return json({ url }, 200, { "set-cookie": `${COOKIE}=${cookie}; Path=/v1/chat/; Max-Age=900; HttpOnly; SameSite=Lax${
      ctx.url.protocol === "https:" ? "; Secure" : ""}`, "cache-control": "no-store" });
  };
}

function cookieOf(request: Request): string {
  for (const part of (request.headers.get("cookie") ?? "").split(";")) {
    const [k, ...v] = part.trim().split("=");
    if (k === COOKIE) return v.join("=");
  }
  return "";
}

/** Where a callback lands the browser. */
function landing(kind: "install" | "connect", provider: string, outcome: string, slug: string, returnTo: string): Response {
  const o = encodeURIComponent(outcome);
  const p = encodeURIComponent(provider);
  const inOrg = (hash: string) => (slug ? `#/o/${encodeURIComponent(slug)}/${hash}` : `#/${hash}`);
  const location = kind === "install"
    ? `/?chatInstall=${p}&outcome=${o}${inOrg(`org/settings/chat/${p}`)}`
    : returnTo === "connect"
      ? `/?chat=${p}&outcome=${o}${inOrg(`connect/${p}`)}`
      : `/?chat=${p}&outcome=${o}${inOrg("me/settings")}`;
  return new Response(null, { status: 303, headers: {
    location, "cache-control": "no-store", "referrer-policy": "no-referrer",
    "set-cookie": `${COOKIE}=; Path=/v1/chat/; Max-Age=0; HttpOnly; SameSite=Lax`,
  } });
}

/** A provider sending the browser back from its consent page. Public: the state and this browser's cookie authenticate it. */
function callback(kind: "install" | "connect") {
  return async (ctx: PublicContext): Promise<Response> => {
    const provider = ctx.params.provider!;
    const q = ctx.url.searchParams;
    const cookie = cookieOf(ctx.request);
    let outcome = "failed";
    let slug = "";
    let returnTo = "settings";
    try {
      const res = await orchestrator("-", "POST", `/internal/chat/${encodeURIComponent(provider)}/${kind}/callback`,
        JSON.stringify({ state: q.get("state") ?? "", code: q.get("code") ?? "", error: q.get("error") ?? "",
          browserHash: cookie ? sha256(cookie) : "" }));
      if (res.status === 404) return json({ error: { code: "not_found", message: "no such chat provider" } }, 404);
      if (res.ok) ({ outcome, slug, returnTo } = await res.json() as { outcome: string; slug: string; returnTo: string });
    } catch {
      // The landing says it failed.
    }
    return landing(kind, provider, outcome, slug, returnTo);
  };
}

/** Headers a webhook never passes on: the connection's, and those the backend sets itself. */
const NOT_FORWARDED = new Set(["x-forwarded-for", "x-forwarded-host", "x-forwarded-proto", "x-real-ip", "x-request-id", "x-dude-person"]);

/**
 * A provider's webhook: the body as sent and its `x-*` and content-type
 * headers, passed on for the orchestrator to verify and store (it keeps
 * only those its provider names). It answers at once; the routing is the
 * orchestrator's chat loop's.
 */
function webhook(kind: "events" | "interactions") {
  return async (ctx: PublicContext): Promise<Response> => {
    const body = await ctx.request.text();
    const headers: Record<string, string> = {};
    for (const [k, v] of ctx.request.headers) {
      const name = k.toLowerCase();
      if ((name.startsWith("x-") && !NOT_FORWARDED.has(name)) || name === "content-type") headers[`x-chat-header-${name}`] = v;
    }
    const { url, token } = { url: config().string("DUDE_ORCHESTRATOR_URL"), token: config().string("DUDE_ORCHESTRATOR_TOKEN") };
    if (!url || !token) throw new HttpError(503, "the orchestrator is not configured", "unavailable");
    let res: Response;
    try {
      res = await fetch(`${url.replace(/\/+$/, "")}/internal/chat/${encodeURIComponent(ctx.params.provider!)}/${kind}`, {
        method: "POST", body, headers: { ...headers, authorization: `Bearer ${token}` }, signal: AbortSignal.timeout(2_500),
      });
    } catch (err) {
      throw new HttpError(503, `the orchestrator is unreachable: ${String(err)}`, "unavailable");
    }
    return new Response(await res.text(), { status: res.status, headers: { "content-type": "application/json" } });
  };
}

const BUILT_IN_ROUTE = "/v1/organization/routing-instructions";

/** `GET /v1/organization/routing-instructions`: built-in (read-only) and the organisation's. */
async function getInstructions(ctx: RequestContext): Promise<Response> {
  const { builtIn } = await providers(ctx);
  const [row] = await withOrg(ctx.principal.organizationId, ({ sql }) =>
    sql`SELECT chat_instructions AS additional FROM organizations WHERE id = current_organization_id()`);
  return json({ builtIn, additional: row?.additional ?? "", canEdit: await isOrgAdmin(ctx) });
}

async function putInstructions(ctx: RequestContext): Promise<Response> {
  await requireOrgAdmin(ctx);
  const { additional } = await parseBody(ctx.request, routingInstructionsInputSchema);
  await withOrg(ctx.principal.organizationId, ({ sql }) =>
    sql`UPDATE organizations SET chat_instructions = ${additional.trim()}, updated_at = now() WHERE id = current_organization_id()`);
  return getInstructions(ctx);
}

/** `GET /v1/organization/chat/:provider/routing-instructions`: the provider's layer, and the whole order. */
async function getProviderInstructions(ctx: RequestContext): Promise<Response> {
  const { list, provider } = await providerOf(ctx);
  const row = await withOrg(ctx.principal.organizationId, async ({ sql }) => {
    const [r] = await sql`SELECT o.chat_instructions AS organisation, COALESCE(i.settings->>'additional_instructions', '') AS additional,
        i.id IS NOT NULL AS installed
      FROM organizations o LEFT JOIN chat_installations i ON i.organization_id = o.id AND i.provider = ${provider.id}
      WHERE o.id = current_organization_id()`;
    return r;
  });
  return json({ builtIn: list.builtIn, organisation: row?.organisation ?? "", additional: row?.additional ?? "",
    installed: Boolean(row?.installed), canEdit: await isOrgAdmin(ctx) });
}

async function putProviderInstructions(ctx: RequestContext): Promise<Response> {
  await requireOrgAdmin(ctx);
  const { provider } = await providerOf(ctx);
  const { additional } = await parseBody(ctx.request, routingInstructionsInputSchema);
  const updated = await withOrg(ctx.principal.organizationId, ({ sql }) =>
    sql`UPDATE chat_installations SET settings = settings || jsonb_build_object('additional_instructions', ${additional.trim()}::text)
      WHERE provider = ${provider.id} RETURNING id`);
  if (updated.length === 0) throw new HttpError(409, `${provider.name} is not installed`, "not_installed");
  return getProviderInstructions(ctx);
}

/** `GET /v1/organization/chat/requests`: every routed mention, newest first. Admins only. */
async function listRequests(ctx: RequestContext): Promise<Response> {
  await requireOrgAdmin(ctx);
  const q = chatRequestsQuerySchema.safeParse(Object.fromEntries(ctx.url.searchParams));
  if (!q.success) throw new HttpError(400, "provider, route, limit and summary filter Requests", "bad_request");
  const { provider = null, route = null, limit = CHAT_REQUESTS_MAX, summary } = q.data;
  const { rows, week } = await withOrg(ctx.principal.organizationId, async ({ sql }) => {
    // One statement per filter, so each reads its own index range
    // (chat_requests_route_idx, _provider_idx, _created_idx) under a reused plan.
    const ids = (provider && route
      ? await sql`SELECT id FROM chat_requests WHERE route = ${route} AND provider = ${provider} AND created_at > now() - interval '30 days'
          ORDER BY created_at DESC, id DESC LIMIT ${limit}`
      : route
        ? await sql`SELECT id FROM chat_requests WHERE route = ${route} AND created_at > now() - interval '30 days'
            ORDER BY created_at DESC, id DESC LIMIT ${limit}`
        : provider
          ? await sql`SELECT id FROM chat_requests WHERE provider = ${provider} AND created_at > now() - interval '30 days'
              ORDER BY created_at DESC, id DESC LIMIT ${limit}`
          : await sql`SELECT id FROM chat_requests WHERE created_at > now() - interval '30 days'
              ORDER BY created_at DESC, id DESC LIMIT ${limit}`).map((r: { id: string }) => r.id);
    // Counted over the whole week, not over the rows listed. A routing call
    // is recorded on its request as model_call_id; a clarifying question's
    // clicked choice, a bound thread and a refusal have none.
    const week = summary
      ? (await sql`SELECT count(*)::int AS mentions, count(model_call_id)::int AS asked
          FROM chat_requests WHERE created_at > now() - interval '7 days'
            AND (${route}::text IS NULL OR route = ${route}) AND (${provider}::text IS NULL OR provider = ${provider})`)[0] as ChatRequestsSummary
      : null;
    const rows = await sql`
    SELECT r.id, r.provider, r.conversation_kind AS "conversationKind", r.conversation_name AS "conversationName",
      r.external_user_name AS "externalUserName", (SELECT person_ref(p) FROM people p WHERE p.id = r.person_id) AS person,
      r.text, r.permalink, r.route, r.asked_route AS "askedRoute", r.confidence::float8 AS confidence, r.reasoning, r.rounds,
      CASE WHEN r.result_kind IS NULL THEN NULL ELSE json_build_object('kind', r.result_kind, 'id', r.result_id, 'label',
        CASE r.result_kind
          WHEN 'session' THEN (SELECT s.title FROM sessions s WHERE s.id = r.result_id)
          WHEN 'task' THEN (SELECT p.key_prefix || '-' || t.number || ' ' || t.title FROM tasks t JOIN projects p ON p.id = t.project_id WHERE t.id = r.result_id)
          WHEN 'epic' THEN (SELECT p.key_prefix || ' › ' || e.title FROM epics e JOIN projects p ON p.id = e.project_id WHERE e.id = r.result_id)
        END) END AS result,
      (SELECT COALESCE(json_agg(person_ref(p)), '[]') FROM people p WHERE p.id = ANY(r.shared_added)) AS "sharedAdded",
      r.shared_skipped AS "sharedSkipped", r.created_at AS "createdAt",
      -- The projects linked from the request's reply ("Link a project"), oldest first.
      (SELECT COALESCE(json_agg(json_build_object('key', l.project_key, 'name', l.project_name, 'by', l.linked_by_name)
        ORDER BY l.created_at, l.project_key), '[]') FROM chat_request_links l WHERE l.request_id = r.id) AS linked
    FROM chat_requests r WHERE r.id = ANY(${sql.array(ids, "TEXT")})
    ORDER BY r.created_at DESC, r.id DESC`;
    return { rows, week };
  });
  return json(week ? { requests: rows, week } : { requests: rows });
}

/** `GET /v1/organization/chat/:provider/projects`: channels' usual projects and the hidden ones. Everyone reads; admins write. */
async function getProjects(ctx: RequestContext): Promise<Response> {
  const { provider } = await providerOf(ctx);
  const out = await withOrg(ctx.principal.organizationId, async ({ sql }) => {
    const [i] = await sql`SELECT external_workspace_id AS workspace FROM chat_installations WHERE provider = ${provider.id}`;
    const projects = await sql`SELECT id, key_prefix AS key, name FROM projects WHERE key_prefix IS NOT NULL ORDER BY key_prefix`;
    const hidden = await sql`SELECT p.id, p.key_prefix AS key, p.name FROM chat_hidden_projects h JOIN projects p ON p.id = h.project_id
      WHERE h.provider = ${provider.id} ORDER BY p.key_prefix`;
    // Every conversation with a hint, and those dude was mentioned in over the last 30 days, newest first.
    const channels = i ? await sql`
      WITH c AS (
        SELECT conversation_id, max(conversation_name) AS name, max(set_at) AS at FROM chat_channel_projects
          WHERE provider = ${provider.id} AND external_workspace_id = ${i.workspace} GROUP BY conversation_id
        UNION ALL
        SELECT conversation_id, max(conversation_name), max(created_at) FROM chat_requests
          WHERE provider = ${provider.id} AND external_workspace_id = ${i.workspace} AND conversation_kind IN ('public', 'private')
            AND created_at > now() - interval '30 days'
          GROUP BY conversation_id)
      SELECT c.conversation_id AS "conversationId", max(c.name) AS "conversationName",
        COALESCE((SELECT json_agg(json_build_object('id', p.id, 'key', p.key_prefix, 'name', p.name) ORDER BY p.key_prefix)
          FROM chat_channel_projects cp JOIN projects p ON p.id = cp.project_id
          WHERE cp.provider = ${provider.id} AND cp.external_workspace_id = ${i.workspace} AND cp.conversation_id = c.conversation_id
            -- A project hidden from the provider is no channel's hint: routing never reads it either.
            AND NOT EXISTS (SELECT 1 FROM chat_hidden_projects h WHERE h.provider = cp.provider AND h.project_id = cp.project_id)), '[]') AS projects
      FROM c GROUP BY c.conversation_id ORDER BY max(c.at) DESC LIMIT 200` : [];
    return { channels: [...channels], hidden: [...hidden], projects: [...projects], installed: Boolean(i) };
  });
  return json({ channels: out.channels, hidden: out.hidden, projects: out.projects, canEdit: await isOrgAdmin(ctx) });
}

/** The projects ids name, all of this organisation's, or a 404. */
async function knownProjects(sql: OrgScope["sql"], ids: string[]): Promise<string[]> {
  const unique = [...new Set(ids)];
  const found = (await sql`SELECT id FROM projects WHERE id = ANY(${sql.array(unique, "TEXT")})`).map((r: { id: string }) => r.id);
  if (found.length !== unique.length) throw notFound("a project given is not one of this organisation's");
  return unique;
}

/** `PUT /v1/organization/chat/:provider/channels/:conversation/projects`: what a conversation is usually about. Admins only. */
async function putChannelProjects(ctx: RequestContext): Promise<Response> {
  await requireOrgAdmin(ctx);
  const { provider } = await providerOf(ctx);
  const conversation = ctx.params.conversation!;
  const input = await parseBody(ctx.request, chatChannelProjectsInputSchema);
  await withOrg(ctx.principal.organizationId, async (scope) => {
    const { sql } = scope;
    const [i] = await sql`SELECT external_workspace_id AS workspace FROM chat_installations WHERE provider = ${provider.id}`;
    if (!i) throw new HttpError(409, `${provider.name} is not installed`, "not_installed");
    const ids = await knownProjects(sql, input.projectIds);
    const hiddenIds = (await sql`SELECT project_id FROM chat_hidden_projects WHERE provider = ${provider.id}
      AND project_id = ANY(${sql.array(ids, "TEXT")})`).map((r: { project_id: string }) => r.project_id);
    if (hiddenIds.length) {
      throw new HttpError(409, `${hiddenIds.join(", ")} ${hiddenIds.length === 1 ? "is" : "are"} hidden from ${provider.name}: ` +
        `a channel can't be usually about a hidden project`, "hidden_from_chat");
    }
    const name = input.conversationName?.trim() ?? "";
    // Rows of projects hidden since are kept (unhiding brings the hint
    // back) and left out of every read; saving the shown set keeps them.
    const removed = (await sql`DELETE FROM chat_channel_projects cp WHERE provider = ${provider.id} AND external_workspace_id = ${i.workspace}
      AND conversation_id = ${conversation} AND NOT (project_id = ANY(${sql.array(ids, "TEXT")}))
      AND NOT EXISTS (SELECT 1 FROM chat_hidden_projects h WHERE h.provider = cp.provider AND h.project_id = cp.project_id) RETURNING project_id`)
      .map((r: { project_id: string }) => r.project_id);
    const added: string[] = [];
    for (const id of ids) {
      const rows = await sql`INSERT INTO chat_channel_projects (organization_id, provider, external_workspace_id, conversation_id,
          conversation_name, project_id, set_by)
        VALUES (current_organization_id(), ${provider.id}, ${i.workspace}, ${conversation}, ${name}, ${id}, ${ctx.principal.personId ?? null})
        ON CONFLICT DO NOTHING RETURNING project_id`;
      if (rows.length) added.push(id);
    }
    if (name) {
      await sql`UPDATE chat_channel_projects SET conversation_name = ${name} WHERE provider = ${provider.id}
        AND external_workspace_id = ${i.workspace} AND conversation_id = ${conversation}`;
    }
    if (added.length || removed.length) {
      const actor = auditActor(ctx.principal);
      await appendInScope(scope, { eventType: EventTypes.ChatChannelProjectsSet, organizationId: scope.organizationId,
        actor: { type: actor.kind, id: actor.id }, source: "control-plane",
        payload: { provider: provider.id, conversationId: conversation, conversationName: name, added, removed, via: "settings" } });
    }
  });
  return getProjects(ctx);
}

/** `PUT /v1/organization/chat/:provider/hidden-projects`: the projects nothing this provider starts can see. Admins only, never from chat. */
async function putHiddenProjects(ctx: RequestContext): Promise<Response> {
  await requireOrgAdmin(ctx);
  const { provider } = await providerOf(ctx);
  const input = await parseBody(ctx.request, chatHiddenProjectsInputSchema);
  await withOrg(ctx.principal.organizationId, async (scope) => {
    const { sql } = scope;
    const ids = await knownProjects(sql, input.projectIds);
    const shown = (await sql`DELETE FROM chat_hidden_projects WHERE provider = ${provider.id}
      AND NOT (project_id = ANY(${sql.array(ids, "TEXT")})) RETURNING project_id`).map((r: { project_id: string }) => r.project_id);
    const hidden: string[] = [];
    for (const id of ids) {
      const rows = await sql`INSERT INTO chat_hidden_projects (organization_id, provider, project_id, hidden_by)
        VALUES (current_organization_id(), ${provider.id}, ${id}, ${ctx.principal.personId ?? null}) ON CONFLICT DO NOTHING RETURNING project_id`;
      if (rows.length) hidden.push(id);
    }
    if (hidden.length || shown.length) {
      const actor = auditActor(ctx.principal);
      await appendInScope(scope, { eventType: EventTypes.ChatHiddenProjectsSet, organizationId: scope.organizationId,
        actor: { type: actor.kind, id: actor.id }, source: "control-plane", payload: { provider: provider.id, hidden, shown } });
    }
  });
  return getProjects(ctx);
}

/** `GET /v1/me/chat/:provider`: the caller's own link. */
async function myLink(ctx: RequestContext): Promise<Response> {
  const { provider } = await providerOf(ctx);
  const [row] = await withOrg(ctx.principal.organizationId, ({ sql }) => sql`
    SELECT c.handle, c.display_name AS "displayName", c.linked_at AS "linkedAt" FROM chat_identities c
      JOIN people me ON me.account_id = c.account_id JOIN chat_installations i ON i.provider = c.provider AND i.external_workspace_id = c.external_workspace_id
    WHERE me.id = ${ctx.principal.personId} AND c.provider = ${provider.id}`);
  return json({ provider: strip(provider), connected: row ?? null });
}

/** Forwards to the orchestrator as the caller. */
function forward(method: string, path: (ctx: RequestContext) => string) {
  return async (ctx: RequestContext): Promise<Response> =>
    orchestrator(ctx.principal.organizationId, method, path(ctx), method === "GET" ? undefined : "{}", ctx.principal);
}

export function registerChatRoutes(router: Router): void {
  router.publicRoute("POST", "/v1/webhooks/chat/:provider/events", webhook("events"));
  router.publicRoute("POST", "/v1/webhooks/chat/:provider/interactions", webhook("interactions"));
  router.post("/v1/chat/:provider/install", begin("install"));
  router.post("/v1/chat/:provider/connect", begin("connect"));
  router.publicRoute("GET", "/v1/chat/:provider/install/callback", callback("install"));
  router.publicRoute("GET", "/v1/chat/:provider/connect/callback", callback("connect"));
  router.get("/v1/chat/:provider/connect/state", forward("GET", (ctx) =>
    `/internal/chat/${encodeURIComponent(ctx.params.provider!)}/connect/state?state=${encodeURIComponent(ctx.url.searchParams.get("state") ?? "")}`));
  router.get("/v1/organization/chat", overview);
  router.get("/v1/organization/chat/requests", listRequests);
  router.get("/v1/organization/chat/:provider", installation);
  router.delete("/v1/organization/chat/:provider", async (ctx) => {
    await requireOrgAdmin(ctx);
    return forward("DELETE", (c) => `/internal/chat/${encodeURIComponent(c.params.provider!)}`)(ctx);
  });
  router.get(BUILT_IN_ROUTE, getInstructions);
  router.put(BUILT_IN_ROUTE, putInstructions);
  router.get("/v1/organization/chat/:provider/routing-instructions", getProviderInstructions);
  router.put("/v1/organization/chat/:provider/routing-instructions", putProviderInstructions);
  router.get("/v1/organization/chat/:provider/projects", getProjects);
  router.put("/v1/organization/chat/:provider/channels/:conversation/projects", putChannelProjects);
  router.put("/v1/organization/chat/:provider/hidden-projects", putHiddenProjects);
  router.get("/v1/me/chat/:provider", myLink);
  router.delete("/v1/me/chat/:provider", async (ctx) => {
    const res = await forward("DELETE", (c) => `/internal/me/chat/${encodeURIComponent(c.params.provider!)}`)(ctx);
    return res.status === 204 ? noContent() : res;
  });
}
