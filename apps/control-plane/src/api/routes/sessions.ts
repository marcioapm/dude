/**
 * Brainstorm sessions: a conversation with an agent that belongs to the
 * organisation and its members (docs: orchestrator/internal/api/sessions.go).
 *
 * The orchestrator owns them — membership, sharing, the agent, filing — and
 * checks on every route that the person is an accepted member; these routes
 * authenticate and forward, naming the person. Under /v1/brainstorms:
 * /v1/sessions/:id is an agent's session inside a Run.
 *
 * The one thing kept here is whether a member has the session open (its
 * page's heartbeat), which goes to its members only, never the organisation.
 */

import { z } from "zod";
import { withOrg } from "../../db/client.ts";
import { orchestrator } from "../../orchestrator/client.ts";
import { conflict, json, notFound, parseBody } from "../http.ts";
import type { RequestContext, Router } from "../router.ts";

const text = z.object({ text: z.string().trim().min(1).max(16_384) }).strict();
const link = z.object({
  projectId: z.string().min(1),
  repositoryIds: z.array(z.string().min(1)).max(100).default([]),
}).strict();
const create = z.object({ title: z.string().trim().min(1).max(200), projects: z.array(link).max(50).default([]) }).strict();
const links = z.object({ projects: z.array(link).max(50) }).strict();
const invite = z.object({ people: z.array(z.string().min(1)).min(1).max(50), role: z.enum(["chat", "read"]).default("chat") }).strict();
const role = z.object({ role: z.enum(["chat", "read"]) }).strict();
const handover = z.object({ person: z.string().min(1), keep: z.enum(["chat", "read", "leave"]).default("chat") }).strict();
const file = z.object({ proposalId: z.string().min(1), items: z.array(z.number().int().min(0)).min(1).max(50) }).strict();
const open = z.object({ open: z.boolean() }).strict();

/** Forward to the orchestrator's session route, as the person. */
function forward(method: "GET" | "POST", path: (ctx: RequestContext) => string, schema?: z.ZodTypeAny) {
  return async (ctx: RequestContext): Promise<Response> => {
    const body = schema ? JSON.stringify(await parseBody(ctx.request, schema)) : "{}";
    return orchestrator(ctx.principal.organizationId, method, path(ctx), body, ctx.principal);
  };
}

/** forward, for a body that sets the session's links: refused first if two share a key (distinctKeys). */
function forwardLinks(path: (ctx: RequestContext) => string, schema: typeof create | typeof links) {
  return async (ctx: RequestContext): Promise<Response> => {
    const body: { projects?: Array<{ projectId: string }> } = await parseBody(ctx.request, schema as z.ZodTypeAny);
    await distinctKeys(ctx, (body.projects ?? []).map((p) => p.projectId));
    return orchestrator(ctx.principal.organizationId, "POST", path(ctx), JSON.stringify(body), ctx.principal);
  };
}

/**
 * A session's projects must have distinct keys: its checkouts
 * (repos/<KEY>/<name>), their names in the lux spec, and the projects its
 * agent proposes work in are all told apart by key, and keys are only the
 * slug's first letters (billing-api and billing-worker are both BILL). The
 * links given are the session's whole set, so they are checked alone.
 * Projects not found are left for the orchestrator to refuse.
 */
async function distinctKeys(ctx: RequestContext, ids: string[]): Promise<void> {
  if (ids.length < 2) return;
  const rows = await withOrg(ctx.principal.organizationId, ({ sql }) => sql`
    SELECT id, name, upper(key_prefix) AS key FROM projects WHERE id = ANY(${sql.array(ids, "text")}::text[])`) as
    Array<{ id: string; name: string; key: string }>;
  const byId = new Map(rows.map((r) => [r.id, r]));
  const seen = new Map<string, { name: string }>();
  for (const id of ids) {
    const p = byId.get(id);
    if (!p) continue;
    const first = seen.get(p.key);
    if (first) {
      const [a, b] = [first.name, p.name].sort();
      throw conflict(`${a} and ${b} both use the key ${p.key}; a session tells its projects apart by key, so link one of them.`);
    }
    seen.set(p.key, p);
  }
}

const at = (suffix = "") => (ctx: RequestContext) => `/internal/sessions/${encodeURIComponent(ctx.params.id!)}${suffix}`;

/**
 * A member's page says it has the session open (every minute or so) or
 * closed it: recorded on their membership and announced live to the
 * session's members only. Anyone else is told it does not exist.
 */
async function setOpen(ctx: RequestContext): Promise<Response> {
  const input = await parseBody(ctx.request, open);
  const id = ctx.params.id!;
  const { organizationId, personId } = ctx.principal;
  const ok = await withOrg(organizationId, async ({ sql }) => {
    const rows = (await sql`
      UPDATE session_people SET open_at = CASE WHEN ${input.open} THEN now() END
      WHERE session_id = ${id} AND person_id = ${personId} AND accepted_at IS NOT NULL
      RETURNING 1`) as unknown[];
    if (rows.length === 0) return false;
    // Heard by every backend's live stream (events/listen.ts), on commit;
    // each stream sends it only to the session's members.
    await sql`SELECT pg_notify('dude_events', ${JSON.stringify({
      organizationId, sessionOpen: { sessionId: id, personId, open: input.open },
    })})`;
    return true;
  });
  if (!ok) throw notFound(`session ${id} not found`);
  return json({ open: input.open });
}

export function registerSessionRoutes(router: Router): void {
  router.get("/v1/brainstorms", forward("GET", () => "/internal/sessions"));
  router.post("/v1/brainstorms", forwardLinks(() => "/internal/sessions", create));
  router.get("/v1/brainstorms/:id", forward("GET", at()));
  router.post("/v1/brainstorms/:id/chat", forward("POST", at("/chat"), text));
  router.post("/v1/brainstorms/:id/link", forwardLinks(at("/link"), links));
  router.post("/v1/brainstorms/:id/people", forward("POST", at("/people"), invite));
  router.post("/v1/brainstorms/:id/people/:person/role",
    forward("POST", (ctx) => `${at()(ctx)}/people/${encodeURIComponent(ctx.params.person!)}/role`, role));
  router.post("/v1/brainstorms/:id/people/:person/remove",
    forward("POST", (ctx) => `${at()(ctx)}/people/${encodeURIComponent(ctx.params.person!)}/remove`));
  router.post("/v1/brainstorms/:id/owner", forward("POST", at("/owner"), handover));
  router.post("/v1/brainstorms/:id/accept", forward("POST", at("/accept")));
  router.post("/v1/brainstorms/:id/decline", forward("POST", at("/decline")));
  router.post("/v1/brainstorms/:id/file", forward("POST", at("/file"), file));
  router.post("/v1/brainstorms/:id/open", setOpen);
}
