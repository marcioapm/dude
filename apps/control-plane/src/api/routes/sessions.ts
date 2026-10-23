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

import { EventTypes, harnessSchema, newId, sessionAccessSchema, sessionMentionsSchema } from "@dude/domain";
import { z } from "zod";
import { withOrg, type OrgScope } from "../../db/client.ts";
import { appendInScope } from "../../events/ledger.ts";
import { orchestrator } from "../../orchestrator/client.ts";
import { auditActor } from "../auth.ts";
import { json, notFound, parseBody } from "../http.ts";
import { agentMachines } from "./machines.ts";
import type { RequestContext, Router } from "../router.ts";

// The orchestrator's bound (delivery.ChatMessageMax) is UTF-8 bytes, not characters.
// A UTF-16 code unit is 1 to 3 UTF-8 bytes, so the length alone settles most strings.
const CHAT_MESSAGE_MAX_BYTES = 16_384;
const encoder = new TextEncoder();
const withinChatBytes = (s: string) =>
  s.length <= CHAT_MESSAGE_MAX_BYTES && (s.length * 3 <= CHAT_MESSAGE_MAX_BYTES || encoder.encode(s).length <= CHAT_MESSAGE_MAX_BYTES);
const chatText = () => z.string().trim().min(1)
  .refine(withinChatBytes, `at most ${CHAT_MESSAGE_MAX_BYTES} bytes in UTF-8`);

export const text = z.object({ text: chatText(), aside: z.boolean().optional() }).strict();
// A session's message may carry images uploaded to it, and be images alone;
// the orchestrator checks they are the session's and unsent.
const sessionMessage = z.object({
  text: z.string().trim().refine(withinChatBytes, `at most ${CHAT_MESSAGE_MAX_BYTES} bytes in UTF-8`),
  aside: z.boolean().optional(),
  attachmentIds: z.array(z.string().min(1)).max(6).optional(),
}).merge(sessionMentionsSchema).strict()
  .refine((m) => m.text.trim().length > 0 || (m.attachmentIds?.length ?? 0) > 0, "text or an image is required");
// The question's form: one answer per question (picks and/or own words), a
// note; the orchestrator checks them against what was asked. A task's Chat
// takes them too.
export const answers = z.object({
  answers: z.array(z.object({ choices: z.array(z.number().int().min(0)).max(6).default([]), text: z.string().max(16_384).optional() }).strict())
    .min(1).max(4),
  note: z.string().max(16_384).optional(),
}).strict();
const link = z.object({
  projectId: z.string().min(1),
  repositoryIds: z.array(z.string().min(1)).max(100).default([]),
}).strict();
// A first message makes the session and starts its agent in one call. A
// tier and harness it names are its own; absent, it follows the
// organisation's Brainstorm setting. The orchestrator checks they fit.
const create = z.object({
  title: z.string().trim().max(200).optional(),
  projects: z.array(link).max(50).default([]),
  message: chatText().optional(),
  tier: z.string().min(1).optional(),
  harness: harnessSchema.optional(),
}).strict();
// null sets one back to the organisation's.
const model = z.object({ tier: z.string().min(1).nullable(), harness: harnessSchema.nullable() }).strict();
const rename = z.object({ title: z.string().trim().min(1).max(200) }).strict();
const links = z.object({ projects: z.array(link).max(50) }).strict();
const invite = z.object({ people: z.array(z.string().min(1)).min(1).max(50), role: z.enum(["chat", "read"]).default("chat") }).strict();
const role = z.object({ role: z.enum(["chat", "read"]) }).strict();
const handover = z.object({ person: z.string().min(1), keep: z.enum(["chat", "read", "leave"]).default("chat") }).strict();
const file = z.object({ proposalId: z.string().min(1), items: z.array(z.number().int().min(0)).min(1).max(50) }).strict();
const open = z.object({ open: z.boolean() }).strict();
// Only "organisation" goes through: "private" reaches the orchestrator, whose 409 says it is one way.
const access = z.object({ access: sessionAccessSchema }).strict();

/** Forward to the orchestrator's session route, as the person. */
function forward(method: "GET" | "POST" | "PUT", path: (ctx: RequestContext) => string, schema?: z.ZodTypeAny) {
  return async (ctx: RequestContext): Promise<Response> => {
    const body = schema ? JSON.stringify(await parseBody(ctx.request, schema)) : "{}";
    return orchestrator(ctx.principal.organizationId, method, path(ctx), body, ctx.principal);
  };
}

const at = (suffix = "") => (ctx: RequestContext) => `/internal/sessions/${encodeURIComponent(ctx.params.id!)}${suffix}`;

/**
 * A page says it has the session open (every minute or so) or closed it:
 * recorded on the person's membership, or for someone in an organisation
 * session who is not one of its people on `session_viewers`, and announced
 * live to those who can see the session only. Opening it marks the
 * mentions of the person in it seen. Anyone who cannot see it is told it
 * does not exist.
 */
async function setOpen(ctx: RequestContext): Promise<Response> {
  const input = await parseBody(ctx.request, open);
  const id = ctx.params.id!;
  const { organizationId, personId } = ctx.principal;
  const ok = await withOrg(organizationId, async ({ sql }) => {
    const [seen] = (await sql`SELECT session_role(${id}, ${personId}) IS NOT NULL AS ok`) as Array<{ ok: boolean }>;
    if (!seen?.ok) return false;
    const rows = (await sql`
      UPDATE session_people SET open_at = CASE WHEN ${input.open} THEN now() END
      WHERE session_id = ${id} AND person_id = ${personId} AND accepted_at IS NOT NULL
      RETURNING 1`) as unknown[];
    if (rows.length === 0) {
      await sql`INSERT INTO session_viewers (session_id, person_id, organization_id, open_at)
        VALUES (${id}, ${personId}, ${organizationId}, CASE WHEN ${input.open} THEN now() END)
        ON CONFLICT (session_id, person_id) DO UPDATE SET open_at = EXCLUDED.open_at`;
    }
    if (input.open) {
      await sql`UPDATE session_mentions SET seen_at = now() WHERE session_id = ${id} AND person_id = ${personId} AND seen_at IS NULL`;
    }
    // Heard by every backend's live stream (events/listen.ts), on commit;
    // each stream sends it only to those who can see the session.
    await sql`SELECT pg_notify('dude_events', ${JSON.stringify({
      organizationId, sessionOpen: { sessionId: id, personId, open: input.open },
    })})`;
    return true;
  });
  if (!ok) throw notFound(`session ${id} not found`);
  return json({ open: input.open });
}

/**
 * The orchestrator's lock on each session the person owns
 * (`delivery.LockSession`), in id order. `removePerson` takes these before
 * the person's row, the order the orchestrator takes them in (the session's
 * lock, then the person's row as it adds them to a session), so the two
 * never wait on each other in a circle.
 */
export async function lockOwnedSessions(scope: OrgScope, personId: string): Promise<void> {
  await scope.sql`SELECT pg_advisory_xact_lock(hashtext('session:' || s.session_id))
    FROM (SELECT session_id FROM session_people WHERE person_id = ${personId} AND role = 'owner' ORDER BY session_id) s`;
}

/** What a session's live agent is told when its owner left: who answers for the owner now. */
export function ownerLeftNotice(name: string): string {
  return `${name} left the organisation. Its admins own this session now: a question for its owner goes to them, until one of them makes themselves its owner.`;
}

/**
 * Someone is leaving the organisation (`removePerson`, in its transaction):
 * each session they own that anyone else can reach — open to the
 * organisation, or with other people in it or invited — passes to the
 * organisation's admins (`owned_by_admins`, `session.owner_left`), its
 * pending handover is void, and its live agent is told; their row stays as
 * its owner of record until an admin takes it over. A private session
 * nobody else is in is archived with them. Under each session's lock, so a
 * handover or a share in flight is settled first. Returns what passed.
 */
export async function passOnSessions(scope: OrgScope, ctx: RequestContext, personId: string): Promise<string[]> {
  await lockOwnedSessions(scope, personId);
  const owned = (await scope.sql`
    SELECT s.id, s.access, p.name,
      EXISTS (SELECT 1 FROM session_people o WHERE o.session_id = s.id AND o.person_id <> ${personId}) AS others
    FROM session_people me JOIN sessions s ON s.id = me.session_id JOIN people p ON p.id = me.person_id
    WHERE me.person_id = ${personId} AND me.role = 'owner'
    ORDER BY s.id`) as Array<{ id: string; access: string; name: string; others: boolean }>;
  const alone = owned.filter((s) => s.access !== "organisation" && !s.others).map((s) => s.id);
  const passed = owned.filter((s) => s.access === "organisation" || s.others);
  if (alone.length > 0) {
    await scope.sql`UPDATE session_people SET archived_at = COALESCE(archived_at, now())
      WHERE session_id = ANY(${scope.sql.array(alone, "TEXT")}) AND person_id = ${personId}`;
  }
  if (passed.length === 0) return [];
  const ids = passed.map((s) => s.id);
  await scope.sql`UPDATE sessions SET owned_by_admins = true, updated_at = now() WHERE id = ANY(${scope.sql.array(ids, "TEXT")})`;
  await scope.sql`UPDATE session_people SET becomes_owner = false, handover_keep = NULL WHERE session_id = ANY(${scope.sql.array(ids, "TEXT")}) AND becomes_owner`;
  const actor = auditActor(ctx.principal);
  for (const s of passed) {
    await appendInScope(scope, {
      eventType: EventTypes.BrainstormOwnerLeft, organizationId: scope.organizationId, sessionId: s.id,
      actor: { type: actor.kind, id: actor.id }, source: "control-plane", payload: { owner: personId, ownerName: s.name },
    });
  }
  // Each live agent gets the notice as dude's directive (the orchestrator's
  // TellBrainstorm), and a parked one is woken to read it.
  const live = (await scope.sql`
    SELECT r.id, r.session_id FROM runs r
    WHERE r.session_id = ANY(${scope.sql.array(ids, "TEXT")}) AND r.role = 'brainstorm' AND r.kind = 'agent'
      AND r.status IN ('pending', 'scheduled', 'starting', 'running', 'paused')
    ORDER BY r.session_id FOR NO KEY UPDATE`) as Array<{ id: string; session_id: string }>;
  for (const run of live) {
    const text = ownerLeftNotice(passed.find((s) => s.id === run.session_id)!.name);
    const directiveId = newId("directive");
    await scope.sql`INSERT INTO directives (id, organization_id, run_id, text, scope)
      VALUES (${directiveId}, ${scope.organizationId}, ${run.id}, ${text}, 'run')`;
    await scope.sql`UPDATE runs SET control = 'resume', control_requested_at = now(), control_reason = 'dude told it something'
      WHERE id = ${run.id} AND status = 'paused' AND (dude_pause IS NULL OR dude_pause = 'idle')`;
    await appendInScope(scope, {
      eventType: EventTypes.BrainstormTold, organizationId: scope.organizationId, sessionId: run.session_id, runId: run.id,
      actor: { type: "system", id: "dude" }, source: "control-plane", payload: { text, directiveId },
    });
  }
  return ids;
}

/**
 * A session as its members see it, with the machine its agent raised, names
 * resolved (`agentMachines`, empty when none). The orchestrator checks
 * membership; its refusal is passed on as it is.
 */
async function getBrainstorm(ctx: RequestContext): Promise<Response> {
  const res = await orchestrator(ctx.principal.organizationId, "GET", at()(ctx), "{}", ctx.principal);
  if (!res.ok) return res;
  const body = (await res.json()) as Record<string, unknown>;
  return json({ ...body, agentMachines: await agentMachines(ctx, { sessionId: ctx.params.id! }) });
}

export function registerSessionRoutes(router: Router): void {
  // ?archived=1 also lists the sessions the person archived; nothing else of the query travels.
  router.get("/v1/brainstorms", forward("GET", (ctx) =>
    ctx.url.searchParams.get("archived") === "1" ? "/internal/sessions?archived=1" : "/internal/sessions"));
  router.post("/v1/brainstorms", forward("POST", () => "/internal/sessions", create));
  // Every session open to the organisation, each with `joined`, and the joined count. Before :id.
  router.get("/v1/brainstorms/open", forward("GET", () => "/internal/sessions/open"));
  router.get("/v1/brainstorms/:id", getBrainstorm);
  router.put("/v1/brainstorms/:id/access", forward("PUT", at("/access"), access));
  router.post("/v1/brainstorms/:id/take-ownership", forward("POST", at("/take-ownership")));
  router.post("/v1/brainstorms/:id/title", forward("POST", at("/title"), rename));
  router.post("/v1/brainstorms/:id/chat", forward("POST", at("/chat"), sessionMessage));
  // Try again on an agent that did not start: a fresh one with the message it was started for.
  router.post("/v1/brainstorms/:id/retry", forward("POST", at("/retry")));
  router.post("/v1/brainstorms/:id/questions/:question/answer",
    forward("POST", (ctx) => `${at()(ctx)}/questions/${encodeURIComponent(ctx.params.question!)}/answer`, answers));
  router.post("/v1/brainstorms/:id/link", forward("POST", at("/link"), links));
  router.post("/v1/brainstorms/:id/people", forward("POST", at("/people"), invite));
  router.post("/v1/brainstorms/:id/people/:person/role",
    forward("POST", (ctx) => `${at()(ctx)}/people/${encodeURIComponent(ctx.params.person!)}/role`, role));
  router.post("/v1/brainstorms/:id/people/:person/remove",
    forward("POST", (ctx) => `${at()(ctx)}/people/${encodeURIComponent(ctx.params.person!)}/remove`));
  router.post("/v1/brainstorms/:id/owner", forward("POST", at("/owner"), handover));
  router.post("/v1/brainstorms/:id/accept", forward("POST", at("/accept")));
  router.post("/v1/brainstorms/:id/decline", forward("POST", at("/decline")));
  router.post("/v1/brainstorms/:id/file", forward("POST", at("/file"), file));
  router.post("/v1/brainstorms/:id/model", forward("POST", at("/model"), model));
  // The person's own archive mark: no body.
  router.post("/v1/brainstorms/:id/archive", forward("POST", at("/archive")));
  router.post("/v1/brainstorms/:id/unarchive", forward("POST", at("/unarchive")));
  router.post("/v1/brainstorms/:id/open", setOpen);
}
