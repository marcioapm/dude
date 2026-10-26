/**
 * An organisation's secrets (migration 210): project secrets one level up.
 * Write-only as a project's are: no query here selects a value; every answer
 * carries the hint stored beside it. Anyone in the organisation reads names,
 * recipients and hints (a project's Secrets lists them as "From <org>"); its
 * admins change them and see which automations pick each one.
 */

import { auditActor } from "../auth.ts";
import {
  EventTypes,
  addOrgSecretSchema,
  changeOrgSecretRecipientsSchema,
  orgSecretNameProblem,
  replaceSecretSchema,
  secretHint,
  type OrgSecret,
  type OrgSecretListing,
  type OrgSecretsResponse,
} from "@dude/domain";
import { withOrg, type OrgScope } from "../../db/client.ts";
import { appendInScope } from "../../events/ledger.ts";
import { isOrgAdmin, requireOrgAdmin } from "../access.ts";
import { badRequest, conflict, json, noContent, notFound, parseBody } from "../http.ts";
import type { RequestContext, Router } from "../router.ts";

/** The path's organisation, which must be the caller's: another's is not found. */
function organizationOf(ctx: RequestContext): string {
  const id = ctx.params.id!;
  if (id !== ctx.principal.organizationId) throw notFound(`organization ${id} not found`);
  return id;
}

async function recordChange(scope: OrgScope, ctx: RequestContext, changed: Record<string, unknown>) {
  const actor = auditActor(ctx.principal);
  await appendInScope(scope, {
    eventType: EventTypes.SettingsUpdated,
    organizationId: ctx.principal.organizationId,
    actor: { type: actor.kind, id: actor.id },
    source: "control-plane",
    payload: { scope: "organization", changed: { organizationSecrets: changed } },
  });
}

async function listSecrets(scope: OrgScope, name?: string): Promise<OrgSecret[]> {
  return (await scope.sql`
    SELECT s.name, s.hint, s.recipients, s.updated_at AS "updatedAt",
      (SELECT json_build_object('id', p.id, 'name', p.name) FROM people p WHERE p.id = s.updated_by) AS "updatedBy"
    FROM organization_secrets s
    WHERE (${name ?? null}::text IS NULL OR s.name = ${name ?? null})
    ORDER BY s.name`) as OrgSecret[];
}

async function one(scope: OrgScope, name: string): Promise<OrgSecret> {
  const [s] = await listSecrets(scope, name);
  if (!s) throw notFound(`the organisation has no secret ${name}`);
  return s;
}

async function getOrgSecrets(ctx: RequestContext): Promise<Response> {
  const orgId = organizationOf(ctx);
  const admin = await isOrgAdmin(ctx);
  const out = await withOrg(orgId, async (scope): Promise<OrgSecretsResponse> => {
    const [org] = (await scope.sql`SELECT id, name FROM organizations WHERE id = ${orgId}`) as Array<{ id: string; name: string }>;
    if (!org) throw notFound(`organization ${orgId} not found`);
    const secrets = await listSecrets(scope);
    const own = (await scope.sql`
      SELECT s.name, p.id, p.name AS project FROM project_secrets s JOIN projects p ON p.id = s.project_id
      WHERE s.name IN (SELECT name FROM organization_secrets) ORDER BY lower(p.name)`) as Array<{ name: string; id: string; project: string }>;
    const picks = admin
      ? (await scope.sql`
        SELECT r->>'name' AS name, a.id, a.name AS automation, a.project_id AS "projectId"
        FROM automations a, jsonb_array_elements(a.secrets) r
        WHERE r->>'source' = 'organization' ORDER BY lower(a.name), a.id`) as Array<{ name: string; id: string; automation: string; projectId: string | null }>
      : null;
    const listed: OrgSecretListing[] = secrets.map((s) => ({
      ...s,
      overriddenIn: own.filter((o) => o.name === s.name).map((o) => ({ id: o.id, name: o.project })),
      pickedBy: picks ? picks.filter((p) => p.name === s.name).map((p) => ({ id: p.id, name: p.automation, projectId: p.projectId })) : null,
    }));
    return { organization: org, secrets: listed };
  });
  return json(out);
}

async function addOrgSecret(ctx: RequestContext): Promise<Response> {
  const orgId = organizationOf(ctx);
  await requireOrgAdmin(ctx);
  const input = await parseBody(ctx.request, addOrgSecretSchema);
  const secret = await withOrg(orgId, async (scope) => {
    // Adds take turns, so two of one name answer a conflict rather than a constraint error.
    await scope.sql`SELECT 1 FROM organizations WHERE id = ${orgId} FOR UPDATE`;
    const names = ((await scope.sql`SELECT name FROM organization_secrets`) as Array<{ name: string }>).map((r) => r.name);
    const problem = orgSecretNameProblem(input.name, names);
    if (problem) throw problem.kind === "conflict" ? conflict(problem.message) : badRequest(problem.message);
    await scope.sql`
      INSERT INTO organization_secrets (organization_id, name, value, hint, updated_by, recipients)
      VALUES (${orgId}, ${input.name}, ${input.value}, ${secretHint(input.value)}, ${ctx.principal.personId},
        ${scope.sql.array(input.recipients ?? [], "text")}::text[])`;
    await recordChange(scope, ctx, { [input.name]: "added" });
    return one(scope, input.name);
  });
  return json(secret, 201);
}

async function replaceOrgSecret(ctx: RequestContext): Promise<Response> {
  const orgId = organizationOf(ctx);
  await requireOrgAdmin(ctx);
  const name = ctx.params.name!;
  const input = await parseBody(ctx.request, replaceSecretSchema);
  const secret = await withOrg(orgId, async (scope) => {
    const changed = await scope.sql`
      UPDATE organization_secrets SET value = ${input.value}, hint = ${secretHint(input.value)}, updated_at = now(),
        updated_by = ${ctx.principal.personId}
      WHERE name = ${name} RETURNING name`;
    if (changed.length === 0) throw notFound(`the organisation has no secret ${name}`);
    await recordChange(scope, ctx, { [name]: "replaced" });
    return one(scope, name);
  });
  return json(secret);
}

async function changeOrgSecretRecipients(ctx: RequestContext): Promise<Response> {
  const orgId = organizationOf(ctx);
  await requireOrgAdmin(ctx);
  const name = ctx.params.name!;
  const input = await parseBody(ctx.request, changeOrgSecretRecipientsSchema);
  const secret = await withOrg(orgId, async (scope) => {
    const changed = await scope.sql`
      UPDATE organization_secrets SET recipients = ${scope.sql.array(input.recipients, "text")}::text[], updated_at = now(),
        updated_by = ${ctx.principal.personId}
      WHERE name = ${name} RETURNING name`;
    if (changed.length === 0) throw notFound(`the organisation has no secret ${name}`);
    await recordChange(scope, ctx, { [name]: { recipients: input.recipients } });
    return one(scope, name);
  });
  return json(secret);
}

async function removeOrgSecret(ctx: RequestContext): Promise<Response> {
  const orgId = organizationOf(ctx);
  await requireOrgAdmin(ctx);
  const name = ctx.params.name!;
  await withOrg(orgId, async (scope) => {
    const gone = await scope.sql`DELETE FROM organization_secrets WHERE name = ${name} RETURNING name`;
    if (gone.length === 0) throw notFound(`the organisation has no secret ${name}`);
    await recordChange(scope, ctx, { [name]: "removed" });
  });
  return noContent();
}

export function registerOrgSecretRoutes(router: Router): void {
  router.get("/v1/organizations/:id/secrets", getOrgSecrets);
  router.post("/v1/organizations/:id/secrets", addOrgSecret);
  router.put("/v1/organizations/:id/secrets/:name/recipients", changeOrgSecretRecipients);
  router.put("/v1/organizations/:id/secrets/:name", replaceOrgSecret);
  router.delete("/v1/organizations/:id/secrets/:name", removeOrgSecret);
}
