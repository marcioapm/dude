import { randomBytes } from "node:crypto";
import { z } from "zod";
import { EventTypes } from "@dude/domain";
import { withOrg, withoutTenant, type OrgScope } from "../../db/client.ts";
import { appendInScope } from "../../events/ledger.ts";
import { HttpError, json, noContent, parseBody } from "../http.ts";
import type { RequestContext, Router } from "../router.ts";

const unusable = () => new HttpError(404, "invitation_unusable", "invitation_unusable");
const mismatch = () => new HttpError(403, "invitation_for_another_email", "invitation_for_another_email");
const batch = z.object({ invitations: z.array(z.object({ email: z.string(), name: z.string().trim().max(100).optional() }).strict()).min(1).max(100),
  role: z.enum(["admin", "member"]).default("member") }).strict();
export const lockPeople = (scope: OrgScope) => scope.sql`SELECT pg_advisory_xact_lock(hashtext('people:' || ${scope.organizationId}))`;
export async function invitationAdmin(scope: OrgScope, ctx: RequestContext) {
  await lockPeople(scope);
  if (ctx.principal.access?.superAdmin && ctx.url.pathname.startsWith("/v1/admin/")) return;
  const [p] = await scope.sql`SELECT role FROM people WHERE id = ${ctx.principal.personId} AND removed_at IS NULL`;
  if (p?.role !== "admin") throw new HttpError(403, "not_admin", "not_admin");
}
function code(): string {
  const alphabet = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz";
  let value = BigInt(`0x${randomBytes(16).toString("hex")}`);
  let result = "";
  for (let i = 0; i < 22; i++) {
    result = alphabet[Number(value % 62n)] + result;
    value /= 62n;
  }
  return result;
}
export async function inviteBatch(scope: OrgScope, input: { invitations: Array<{ email: string; name?: string | undefined }>; role?: "admin" | "member" | undefined }, actor: string | null) {
  const seen = new Set<string>(), results = [];
  for (const entry of input.invitations) {
    const email = entry.email.trim().toLowerCase();
    if (!z.string().email().max(320).safeParse(email).success) { results.push({ email, result: "invalid" }); continue; }
    if (seen.has(email)) { results.push({ email, result: "duplicate" }); continue; } seen.add(email);
    const members = await scope.sql`SELECT id FROM people WHERE email = ${email}::citext AND removed_at IS NULL`;
    if (members.length) { results.push({ email, result: "member" }); continue; }
    let [inv] = await scope.sql`SELECT *, expires_at <= now() AS expired FROM invitations
      WHERE email = ${email}::citext AND accepted_at IS NULL AND revoked_at IS NULL FOR UPDATE`;
    let result = "existing";
    if (!inv) {
      [inv] = await scope.sql`INSERT INTO invitations (id, organization_id, code, email, name, role, invited_by)
        VALUES (${`inv_${crypto.randomUUID()}`}, ${scope.organizationId}, ${code()}, ${email}, ${entry.name ?? null}, ${input.role ?? "member"}, ${actor}) RETURNING *`;
      result = "created";
    } else if (inv.expired) {
      [inv] = await scope.sql`UPDATE invitations SET expires_at = now() + interval '14 days', renewed_at = now(), renewed_by = ${actor}
        WHERE id = ${inv.id} RETURNING *`; result = "renewed";
    }
    results.push({ email, result, invitation: { id: inv.id, code: inv.code, role: inv.role, expiresAt: inv.expires_at }, link: `#/invite/${inv.code}` });
  }
  return results;
}
export async function listInvitations(scope: OrgScope, url: URL) {
  const status = url.searchParams.get("status") ?? "pending", q = url.searchParams.get("q") ?? "", cursor = url.searchParams.get("cursor") ?? "";
  if (!["all", "pending", "expired", "accepted", "revoked"].includes(status)) throw new HttpError(400, "invalid status", "bad_request");
  const rows = await scope.sql`SELECT invitation_details(original) || jsonb_build_object('status', state,
      'acceptedAt', accepted_at, 'acceptedPersonId', accepted_person_id, 'revokedAt', revoked_at, 'renewedAt', renewed_at) AS invitation
    FROM (SELECT i AS original, i.*, CASE WHEN revoked_at IS NOT NULL THEN 'revoked' WHEN accepted_at IS NOT NULL THEN 'accepted'
      WHEN expires_at <= now() THEN 'expired' ELSE 'pending' END AS state FROM invitations i) i
    WHERE (${status} = 'all' OR state = ${status}) AND (email::text ILIKE ${`%${q}%`} OR name ILIKE ${`%${q}%`})
      AND id > ${cursor} ORDER BY id LIMIT 51`;
  return { invitations: rows.slice(0,50).map((r: { invitation: unknown }) => r.invitation), cursor: rows.length > 50 ? rows[49].invitation.id : null };
}
/** Every invitation of the organisation `scope` is in, by the status the list filters on, and in all. */
export async function invitationCounts(scope: OrgScope) {
  const [c] = await scope.sql`SELECT
      count(*) FILTER (WHERE revoked_at IS NULL AND accepted_at IS NULL AND expires_at > now())::int AS pending,
      count(*) FILTER (WHERE revoked_at IS NULL AND accepted_at IS NULL AND expires_at <= now())::int AS expired,
      count(*) FILTER (WHERE revoked_at IS NULL AND accepted_at IS NOT NULL)::int AS accepted,
      count(*) FILTER (WHERE revoked_at IS NOT NULL)::int AS revoked,
      count(*)::int AS "all"
    FROM invitations`;
  return c as { pending: number; expired: number; accepted: number; revoked: number; all: number };
}
export async function changeInvitation(scope: OrgScope, id: string, renew: boolean, actor: string | null) {
  const rows = renew ? await scope.sql`UPDATE invitations SET expires_at = now()+interval '14 days', renewed_at=now(), renewed_by=${actor}
    WHERE id=${id} AND accepted_at IS NULL AND revoked_at IS NULL RETURNING code` :
    await scope.sql`UPDATE invitations SET revoked_at=now(), revoked_by=${actor}
      WHERE id=${id} AND accepted_at IS NULL AND revoked_at IS NULL RETURNING code`;
  if (!rows.length) throw unusable(); return rows[0];
}
async function detail(ctx: RequestContext) {
  const access = ctx.principal.access; if (!access) throw new HttpError(403, "Access required", "access_required");
  const [r] = await withoutTenant(({ sql }) => sql`SELECT invitation_for_code(${ctx.params.code!}, ${access.email}::citext) AS invitation`);
  if (!r?.invitation) throw unusable(); if (r.invitation.state === "invitation_for_another_email") throw mismatch();
  return r.invitation;
}
export function registerInvitationRoutes(router: Router) {
  router.get("/v1/invitations/:code", async (ctx) => json(await detail(ctx)));
  router.post("/v1/invitations/:code/accept", async (ctx) => {
    const input = await parseBody(ctx.request, z.object({ name: z.string().trim().min(1).max(100).optional() }).strict());
    const preview = await detail(ctx), access = ctx.principal.access!;
    return json(await withOrg(preview.organization.id, async (scope) => {
      await lockPeople(scope);
      const [i] = await scope.sql`SELECT * FROM invitations WHERE code = ${ctx.params.code!} FOR UPDATE`;
      if (!i || i.revoked_at || (!i.accepted_at && new Date(i.expires_at).getTime() <= Date.now())) throw unusable();
      if (i.email.toLowerCase() !== access.email) throw mismatch();
      const [a] = await scope.sql`INSERT INTO accounts (id, email, name, photo_url)
        VALUES (${`acc_${crypto.randomUUID()}`}, ${access.email}, ${input.name ?? access.profile?.name ?? i.name ?? access.email}, ${access.profile?.picture ?? null})
        ON CONFLICT (email) DO UPDATE SET email = EXCLUDED.email RETURNING *`;
      let [person] = await scope.sql`SELECT * FROM people WHERE account_id=${a.id} ORDER BY (removed_at IS NULL) DESC, created_at DESC LIMIT 1 FOR UPDATE`;
      if (i.accepted_at && (!person || person.removed_at)) throw unusable();
      if (!person || person.removed_at) {
        if (person) [person] = await scope.sql`UPDATE people SET removed_at=NULL, role=${i.role}, name=${a.name}, email=${a.email},
          photo_url=${a.photo_url}, photo_key=${a.photo_key}, photo_token=${a.photo_token} WHERE id=${person.id} RETURNING *`;
        else [person] = await scope.sql`INSERT INTO people (id, organization_id, account_id, name, email, role, photo_url, photo_key, photo_token)
          VALUES (${`per_${crypto.randomUUID()}`}, ${scope.organizationId}, ${a.id}, ${a.name}, ${a.email}, ${i.role}, ${a.photo_url}, ${a.photo_key}, ${a.photo_token}) RETURNING *`;
        // The ledger's "joined" entry, attributed to whoever invited them (the operator: system).
        await appendInScope(scope, { eventType: EventTypes.PersonInvited, organizationId: scope.organizationId,
          projectId: null, taskId: null, correlationId: null, source: "control-plane",
          actor: i.invited_by ? { type: "person", id: i.invited_by } : { type: "system", id: "operator" },
          payload: { personId: person.id, role: i.role, invitationId: i.id } });
      }
      await scope.sql`UPDATE invitations SET accepted_at=COALESCE(accepted_at,now()), accepted_person_id=${person.id} WHERE id=${i.id}`;
      return { person: { id: person.id, role: person.role, name: person.name }, organization: preview.organization };
    }));
  });
  const path = "/v1/organization/invitations";
  router.get(path, async (ctx) => json(await withOrg(ctx.principal.organizationId, async (scope) => {
    await invitationAdmin(scope, ctx); return listInvitations(scope, ctx.url);
  })));
  router.get(`${path}/counts`, async (ctx) => json(await withOrg(ctx.principal.organizationId, async (scope) => {
    await invitationAdmin(scope, ctx); return invitationCounts(scope);
  })));
  router.post(path, async (ctx) => {
    const input = await parseBody(ctx.request, batch);
    return json({ results: await withOrg(ctx.principal.organizationId, async (scope) => {
      await invitationAdmin(scope, ctx); return inviteBatch(scope, input, ctx.principal.personId);
    }) });
  });
  router.post(`${path}/:id/renew`, async (ctx) => json(await withOrg(ctx.principal.organizationId, async (scope) => {
    await invitationAdmin(scope, ctx); return changeInvitation(scope, ctx.params.id!, true, ctx.principal.personId);
  })));
  router.delete(`${path}/:id`, async (ctx) => {
    await withOrg(ctx.principal.organizationId, async (scope) => { await invitationAdmin(scope, ctx); await changeInvitation(scope, ctx.params.id!, false, ctx.principal.personId); });
    return noContent();
  });
}
