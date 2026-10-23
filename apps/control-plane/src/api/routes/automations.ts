/**
 * Automations: an organisation's and its projects' (migration 180). Kept
 * here; running them is the orchestrator's (internal/automations).
 *
 * Who may do what: anyone in the organisation reads every automation. An
 * organisation automation is changed by the organisation's admins, a
 * project's by the project's editors (api/access.ts). Its owner — the
 * creator, until a transfer — and the admins transfer it, to a current
 * member of the organisation. A member who cannot edit it may own it: runs
 * act for them and failures notify them.
 */

import {
  automationDefaults,
  automationInputSchema,
  enabledSchema,
  EventTypes,
  newAutomationSchema,
  repoFilterAllows,
  repositoryFullName,
  secretHint,
  transferOwnerSchema,
  webhookSecretSchema,
  type Automation,
  type AutomationInput,
  type AutomationScope,
  type RepoFilter,
} from "@dude/domain";
import { newId } from "@dude/domain";
import { z } from "zod";
import { auditActor } from "../auth.ts";
import { withOrg, type OrgScope } from "../../db/client.ts";
import { appendInScope } from "../../events/ledger.ts";
import { canEditProject, isOrgAdmin } from "../access.ts";
import { badRequest, HttpError, json, noContent, notFound, parseBody } from "../http.ts";
import { requireSize } from "./machines.ts";
import { requireImage } from "./images.ts";
import type { RequestContext, Router } from "../router.ts";

/** An automation's row as every read selects it. */
const COLUMNS = `a.id, a.project_id AS "projectId", p.name AS "projectName", a.name, a.description, a.kind, a.enabled,
  a.owner_id AS "ownerId", a.owner_left_at AS "ownerLeftAt", a.trigger, a.session_mode AS "sessionMode",
  a.expiry_days AS "expiryDays", a.concurrency, a.parallel_max AS "parallelMax", a.repos, a.repo_filter AS "repoFilter",
  a.agent, a.machine_size_id AS "machineSizeId", a.image_id AS "imageId", a.setup_script AS "setupScript", a.command,
  a.server, a.webhook, a.webhook_hint AS "webhookSecretHint", a.secrets, a.notify_on_failure AS "notifyOnFailure",
  a.next_run_at AS "nextRunAt", a.created_at AS "createdAt", a.updated_at AS "updatedAt",
  (SELECT json_build_object('id', o.id, 'name', o.name, 'photoUrl', person_ref(o)->>'photoUrl') FROM people o WHERE o.id = a.owner_id) AS owner,
  (SELECT json_build_object('id', r.id, 'status', r.status, 'at', COALESCE(r.ended_at, r.started_at, r.created_at))
     FROM automation_runs r WHERE r.automation_id = a.id ORDER BY r.created_at DESC, r.id DESC LIMIT 1) AS "lastRun"`;
const FROM = `automations a LEFT JOIN projects p ON p.id = a.project_id`;

type Row = Omit<Automation, "canEdit" | "canTransfer"> & { ownerId: string | null };

const iso = (v: unknown) => (v instanceof Date ? v.toISOString() : (v as string | null));

/** Whether the caller may change an automation in this scope. */
async function canEdit(ctx: RequestContext, projectId: string | null): Promise<boolean> {
  return projectId ? canEditProject(ctx, projectId) : isOrgAdmin(ctx);
}

async function shape(ctx: RequestContext, row: Row): Promise<Automation> {
  const { ownerId, ...rest } = row;
  const admin = await isOrgAdmin(ctx);
  const lastRun = rest.lastRun ? { ...rest.lastRun, at: iso(rest.lastRun.at)! } : null;
  return {
    ...rest,
    lastRun,
    ownerLeftAt: iso(rest.ownerLeftAt),
    nextRunAt: iso(rest.nextRunAt),
    createdAt: iso(rest.createdAt)!,
    updatedAt: iso(rest.updatedAt)!,
    canEdit: await canEdit(ctx, rest.projectId),
    canTransfer: admin || (ownerId !== null && ownerId === ctx.principal.personId),
  };
}

async function readOne(scope: OrgScope, id: string): Promise<Row> {
  const [row] = (await scope.sql.unsafe(`SELECT ${COLUMNS} FROM ${FROM} WHERE a.id = $1`, [id])) as Row[];
  if (!row) throw notFound(`automation ${id} not found`);
  return row;
}

async function requireEditor(ctx: RequestContext, projectId: string | null): Promise<void> {
  if (await canEdit(ctx, projectId)) return;
  throw new HttpError(403, projectId
    ? "only the project's editors can change its automations"
    : "only an organisation admin can change the organisation's automations", "not_editor");
}

async function record(scope: OrgScope, ctx: RequestContext | null, a: { id: string; name: string; projectId: string | null },
  changed: Record<string, unknown>): Promise<void> {
  const actor = ctx ? auditActor(ctx.principal) : { kind: "system" as const, id: "dude" };
  await appendInScope(scope, {
    eventType: EventTypes.AutomationChanged,
    organizationId: scope.organizationId,
    projectId: a.projectId,
    actor: { type: actor.kind, id: actor.id },
    source: "control-plane",
    payload: { automationId: a.id, name: a.name, changed },
  });
}

// ---------------------------------------------------------------------------
// Lists
// ---------------------------------------------------------------------------

const listQuery = z.object({
  scope: z.enum(["organization", "project"]).optional(),
  project: z.string().min(1).optional(),
  trigger: z.enum(["schedule", "github"]).optional(),
  /** agent, command, server, webhook; or agent:own / agent:<role>. */
  kind: z.string().regex(/^(agent(:[a-z_]+)?|command|server|webhook)$/).optional(),
  enabled: z.enum(["true", "false"]).optional(),
  owner: z.enum(["mine"]).optional(),
  q: z.string().max(200).optional(),
});

function queryOf(ctx: RequestContext) {
  const parsed = listQuery.safeParse(Object.fromEntries(ctx.url.searchParams));
  if (!parsed.success) throw badRequest("unknown list filter", parsed.error.flatten());
  return parsed.data;
}

/** The organisation's automations and every project's, filtered. */
async function listOrg(ctx: RequestContext): Promise<Response> {
  const f = queryOf(ctx);
  const [kind, sub] = (f.kind ?? "").split(":");
  const rows = await withOrg(ctx.principal.organizationId, (scope) => scope.sql.unsafe(
    `SELECT ${COLUMNS} FROM ${FROM}
     WHERE ($1::text IS NULL OR ($1 = 'organization') = (a.project_id IS NULL))
       AND ($2::text IS NULL OR a.project_id = $2)
       AND ($3::text IS NULL OR a.trigger->>'kind' = $3)
       AND ($4::text IS NULL OR a.kind = $4)
       AND ($5::text IS NULL OR ($5 = 'own' AND a.agent ? 'own') OR a.agent #>> '{preset,role}' = $5)
       AND ($6::boolean IS NULL OR a.enabled = $6)
       AND ($7::text IS NULL OR a.owner_id = $7)
       AND ($8::text IS NULL OR a.name ILIKE '%' || $8 || '%')
     ORDER BY a.project_id NULLS FIRST, lower(a.name), a.id`,
    [f.scope ?? null, f.project ?? null, f.trigger ?? null, kind || null, sub || null,
      f.enabled === undefined ? null : f.enabled === "true", f.owner === "mine" ? ctx.principal.personId : null, f.q?.trim() || null],
  )) as Row[];
  return json({ automations: await Promise.all(rows.map((r) => shape(ctx, r))) });
}

/**
 * A project's own automations, and the organisation's whose repository
 * filter reaches one of its repositories ("Also running here", read-only:
 * a project cannot opt out). Evaluated now, so a glob covers a repository
 * added after it was written.
 */
async function listProject(ctx: RequestContext): Promise<Response> {
  const projectId = ctx.params.id!;
  const out = await withOrg(ctx.principal.organizationId, async (scope) => {
    const [project] = await scope.sql`SELECT 1 FROM projects WHERE id = ${projectId}`;
    if (!project) throw notFound(`project ${projectId} not found`);
    const own = (await scope.sql.unsafe(`SELECT ${COLUMNS} FROM ${FROM} WHERE a.project_id = $1 ORDER BY lower(a.name), a.id`, [projectId])) as Row[];
    const org = (await scope.sql.unsafe(`SELECT ${COLUMNS} FROM ${FROM} WHERE a.project_id IS NULL ORDER BY lower(a.name), a.id`)) as Row[];
    const names = await projectRepoNames(scope, projectId);
    const here = org.filter((a) => names.some((n) => repoFilterAllows(a.repoFilter as RepoFilter | null, n)));
    return { own, here };
  });
  return json({
    automations: await Promise.all(out.own.map((r) => shape(ctx, r))),
    alsoRunningHere: await Promise.all(out.here.map((r) => shape(ctx, r))),
  });
}

async function projectRepoNames(scope: OrgScope, projectId: string): Promise<string[]> {
  const rows = (await scope.sql`SELECT url FROM repositories WHERE project_id = ${projectId}`) as Array<{ url: string }>;
  return rows.map((r) => repositoryFullName(r.url)).filter((n): n is string => n !== null);
}

/** Every project's repositories, for the repository filter's suggestions and count. */
async function orgRepositories(ctx: RequestContext): Promise<Response> {
  const rows = await withOrg(ctx.principal.organizationId, ({ sql }) => sql`
    SELECT p.id AS "projectId", p.name AS project, r.url FROM repositories r JOIN projects p ON p.id = r.project_id
    ORDER BY lower(p.name), lower(r.name)`) as Array<{ projectId: string; project: string; url: string }>;
  const groups = new Map<string, { projectId: string; project: string; repos: string[] }>();
  for (const r of rows) {
    const name = repositoryFullName(r.url);
    if (!name) continue;
    const g = groups.get(r.projectId) ?? { projectId: r.projectId, project: r.project, repos: [] };
    if (!g.repos.includes(name)) g.repos.push(name);
    groups.set(r.projectId, g);
  }
  return json({ groups: [...groups.values()] });
}

// ---------------------------------------------------------------------------
// One automation
// ---------------------------------------------------------------------------

async function getAutomation(ctx: RequestContext): Promise<Response> {
  const row = await withOrg(ctx.principal.organizationId, (scope) => readOne(scope, ctx.params.id!));
  return json(await shape(ctx, row));
}

const createSchema = newAutomationSchema.extend({
  projectId: z.string().min(1).nullable().default(null),
  tz: z.string().max(64).default("UTC"),
});

/** Create from the dialog's three answers: off, owned by its creator, with defaults to fill in. */
async function createAutomation(ctx: RequestContext): Promise<Response> {
  const input = (await parseBody(ctx.request, createSchema)) as z.output<typeof createSchema>;
  await requireEditor(ctx, input.projectId);
  const scope: AutomationScope = input.projectId ? "project" : "organization";
  const settings = automationDefaults(input, input.tz);
  const parsed = automationInputSchema(scope).safeParse(settings);
  if (!parsed.success) throw badRequest("request body failed validation", parsed.error.flatten());
  const id = newId("automation");
  const row = await withOrg(ctx.principal.organizationId, async (scope) => {
    if (input.projectId) {
      const [p] = await scope.sql`SELECT 1 FROM projects WHERE id = ${input.projectId}`;
      if (!p) throw notFound(`project ${input.projectId} not found`);
    }
    const secret = settings.kind === "webhook" ? newSigningSecret() : null;
    await scope.sql`
      INSERT INTO automations (id, organization_id, project_id, name, kind, owner_id, trigger, agent, command, server, webhook,
        webhook_secret, webhook_hint, created_by, updated_by)
      VALUES (${id}, ${scope.organizationId}, ${input.projectId}, ${settings.name}, ${settings.kind}, ${ctx.principal.personId},
        ${settings.trigger}::jsonb, ${settings.agent}::jsonb, ${settings.command}, ${settings.server}::jsonb, ${settings.webhook}::jsonb,
        ${secret}, ${secret ? secretHint(secret) : null}, ${ctx.principal.personId}, ${ctx.principal.personId})`;
    await record(scope, ctx, { id, name: settings.name, projectId: input.projectId }, { created: true });
    return readOne(scope, id);
  });
  return json(await shape(ctx, row), 201);
}

function newSigningSecret(): string {
  const bytes = crypto.getRandomValues(new Uint8Array(32));
  return "whsec_" + Buffer.from(bytes).toString("base64url");
}

/** The columns a saved AutomationInput writes, by the API's names. */
async function checkReferences(scope: OrgScope, input: AutomationInput, current: Row): Promise<void> {
  const own = input.agent && "own" in input.agent ? input.agent.own : null;
  const overrides = input.agent && "preset" in input.agent ? input.agent.preset.overrides : null;
  for (const size of [input.machineSizeId, own?.machineSizeId, overrides?.machineSizeId]) {
    if (size) await requireSize(scope, size);
  }
  for (const image of [input.imageId, own?.imageId, overrides?.imageId]) {
    if (image) await requireImage(scope, image, current.imageId);
  }
  for (const tier of [own?.tierId, overrides?.tierId]) {
    if (tier && (await scope.sql`SELECT 1 FROM model_tiers WHERE id = ${tier}`).length === 0) throw badRequest(`there is no model tier ${tier}`);
  }
  if (Array.isArray(input.repos) && current.projectId) {
    const found = (await scope.sql`SELECT id FROM repositories WHERE project_id = ${current.projectId}
      AND id = ANY(${scope.sql.array(input.repos, "text")}::text[])`) as Array<{ id: string }>;
    const missing = input.repos.filter((r) => !found.some((f) => f.id === r));
    if (missing.length) throw badRequest(`the project has no repository ${missing[0]}`);
  }
}

/** Replace an automation's settings. Its on/off, owner and signing secret have routes of their own. */
async function putAutomation(ctx: RequestContext): Promise<Response> {
  const id = ctx.params.id!;
  const row = await withOrg(ctx.principal.organizationId, async (scope) => {
    const current = await readOne(scope, id);
    await requireEditor(ctx, current.projectId);
    const input = (await parseBody(ctx.request, automationInputSchema(current.projectId ? "project" : "organization"))) as AutomationInput;
    await checkReferences(scope, input, current);
    const triggerChanged = JSON.stringify(input.trigger) !== JSON.stringify(current.trigger);
    await scope.sql`
      UPDATE automations SET name = ${input.name}, description = ${input.description}, kind = ${input.kind},
        trigger = ${input.trigger}::jsonb, session_mode = ${input.sessionMode}, expiry_days = ${input.expiryDays},
        concurrency = ${input.concurrency}, parallel_max = ${input.parallelMax}, repos = ${JSON.stringify(input.repos)}::jsonb,
        repo_filter = ${input.repoFilter}::jsonb, agent = ${input.agent}::jsonb, machine_size_id = ${input.machineSizeId},
        image_id = ${input.imageId}, setup_script = ${input.setupScript}, command = ${input.command},
        server = ${input.server}::jsonb, webhook = ${input.webhook}::jsonb,
        webhook_secret = CASE WHEN ${input.kind} = 'webhook' THEN COALESCE(webhook_secret, ${newSigningSecret()}) ELSE webhook_secret END,
        secrets = ${scope.sql.array(input.secrets, "text")}::text[], notify_on_failure = ${input.notifyOnFailure},
        next_run_at = CASE WHEN ${triggerChanged} THEN NULL ELSE next_run_at END,
        updated_by = ${ctx.principal.personId}, updated_at = now()
      WHERE id = ${id}`;
    await scope.sql`UPDATE automations SET webhook_hint = right(webhook_secret, 4) WHERE id = ${id} AND webhook_hint IS NULL AND webhook_secret IS NOT NULL`;
    await record(scope, ctx, { id, name: input.name, projectId: current.projectId }, { settings: true });
    return readOne(scope, id);
  });
  return json(await shape(ctx, row));
}

async function deleteAutomation(ctx: RequestContext): Promise<Response> {
  const id = ctx.params.id!;
  await withOrg(ctx.principal.organizationId, async (scope) => {
    const current = await readOne(scope, id);
    await requireEditor(ctx, current.projectId);
    await scope.sql`DELETE FROM automations WHERE id = ${id}`;
    await record(scope, ctx, current, { deleted: true });
  });
  return noContent();
}

async function setEnabled(ctx: RequestContext): Promise<Response> {
  const id = ctx.params.id!;
  const { enabled } = await parseBody(ctx.request, enabledSchema);
  const row = await withOrg(ctx.principal.organizationId, async (scope) => {
    const current = await readOne(scope, id);
    await requireEditor(ctx, current.projectId);
    // Off does not touch the kept session or its expiry clock.
    await scope.sql`UPDATE automations SET enabled = ${enabled}, next_run_at = NULL, updated_by = ${ctx.principal.personId},
      updated_at = now() WHERE id = ${id}`;
    await record(scope, ctx, current, { enabled });
    return readOne(scope, id);
  });
  return json(await shape(ctx, row));
}

/**
 * Hand an automation to someone else: its owner or an admin may, to a
 * current member of the organisation (every member is a member of each of
 * its projects). Clears "held by the admins".
 */
async function transferOwner(ctx: RequestContext): Promise<Response> {
  const id = ctx.params.id!;
  const { personId } = await parseBody(ctx.request, transferOwnerSchema);
  const row = await withOrg(ctx.principal.organizationId, async (scope) => {
    const current = await readOne(scope, id);
    if (!(await isOrgAdmin(ctx)) && current.ownerId !== ctx.principal.personId) {
      throw new HttpError(403, "only the organisation's admins and the automation's owner transfer it", "not_owner");
    }
    const [to] = (await scope.sql`SELECT id FROM people WHERE id = ${personId} AND removed_at IS NULL`) as Array<{ id: string }>;
    if (!to) throw badRequest(`${personId} is not a member of this organisation`);
    await scope.sql`UPDATE automations SET owner_id = ${personId}, owner_left_at = NULL, updated_by = ${ctx.principal.personId},
      updated_at = now() WHERE id = ${id}`;
    await record(scope, ctx, current, { owner: { from: current.ownerId, to: personId } });
    return readOne(scope, id);
  });
  return json(await shape(ctx, row));
}

/** Replace a webhook automation's signing secret; it is never read back, only its last four characters. */
async function setWebhookSecret(ctx: RequestContext): Promise<Response> {
  const id = ctx.params.id!;
  const { secret } = await parseBody(ctx.request, webhookSecretSchema);
  const row = await withOrg(ctx.principal.organizationId, async (scope) => {
    const current = await readOne(scope, id);
    await requireEditor(ctx, current.projectId);
    if (current.kind !== "webhook") throw badRequest("only a webhook automation signs what it sends");
    await scope.sql`UPDATE automations SET webhook_secret = ${secret}, webhook_hint = ${secretHint(secret)}, updated_at = now(),
      updated_by = ${ctx.principal.personId} WHERE id = ${id}`;
    await record(scope, ctx, current, { webhookSecret: "replaced" });
    return readOne(scope, id);
  });
  return json(await shape(ctx, row));
}

/**
 * A person left the organisation: every automation they owned is held by
 * the organisation's admins together, stays on, and says so. In the
 * removal's transaction (people.ts removePerson).
 */
export async function automationsOwnerLeft(scope: OrgScope, ctx: RequestContext, personId: string): Promise<void> {
  const held = (await scope.sql`
    UPDATE automations SET owner_id = NULL, owner_left_at = now(), updated_at = now()
    WHERE owner_id = ${personId} RETURNING id, name, project_id AS "projectId"`) as Array<{ id: string; name: string; projectId: string | null }>;
  for (const a of held) await record(scope, ctx, a, { owner: { from: personId, to: null }, ownerLeft: true });
}

export function registerAutomationRoutes(router: Router): void {
  router.get("/v1/automations", listOrg);
  router.post("/v1/automations", createAutomation);
  router.get("/v1/automations/repositories", orgRepositories);
  router.get("/v1/automations/:id", getAutomation);
  router.put("/v1/automations/:id", putAutomation);
  router.delete("/v1/automations/:id", deleteAutomation);
  router.put("/v1/automations/:id/enabled", setEnabled);
  router.put("/v1/automations/:id/owner", transferOwner);
  router.put("/v1/automations/:id/webhook-secret", setWebhookSecret);
  router.get("/v1/projects/:id/automations", listProject);
}
