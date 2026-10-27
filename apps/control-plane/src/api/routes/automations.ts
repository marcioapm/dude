/**
 * Automations: an organisation's and its projects' (migration 183). Kept
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
  DELIVERY_VERDICTS,
  enabledSchema,
  EventTypes,
  INCOMING_NEEDS_PROJECT,
  newAutomationSchema,
  newHookId,
  repoFilterAllows,
  repositoryFullName,
  restorePromptSchema,
  secretHint,
  transferOwnerSchema,
  webhookSecretSchema,
  type Automation,
  type AutomationInput,
  type AutomationPromptVersion,
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
import { orchestrator } from "../../orchestrator/client.ts";
import { readDeliveries, readDelivery, refusedNotStored, runDeliveries, type DeliveryFilter } from "./incoming.ts";
import type { RequestContext, Router } from "../router.ts";

/** An automation's row as every read selects it. */
export const COLUMNS = `a.id, a.project_id AS "projectId", p.name AS "projectName", a.name, a.description, a.kind, a.enabled,
  a.owner_id AS "ownerId", a.owner_left_at AS "ownerLeftAt", a.trigger, a.session_mode AS "sessionMode",
  a.expiry_days AS "expiryDays", a.concurrency, a.parallel_max AS "parallelMax", a.repos, a.repo_filter AS "repoFilter",
  a.agent, a.machine_size_id AS "machineSizeId", a.image_id AS "imageId", a.setup_script AS "setupScript", a.command,
  a.server, a.webhook, a.webhook_hint AS "webhookSecretHint", a.secrets, a.notify_on_failure AS "notifyOnFailure",
  a.next_run_at AS "nextRunAt", a.created_at AS "createdAt", a.updated_at AS "updatedAt",
  a.incoming_hint AS "incomingSecretHint", a.project_ids AS "projectIds",
  (SELECT f.public_url FROM forge_credentials f WHERE a.trigger->>'kind' = 'incoming' AND f.forge = 'github' AND f.public_url IS NOT NULL
     LIMIT 1) AS "publicOrigin",
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

/** A row as every read answers it. No query of its own: access reads ctx.principal. */
export async function shape(ctx: RequestContext, row: Row): Promise<Automation> {
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

export const listQuery = z.object({
  scope: z.enum(["organization", "project"]).optional(),
  project: z.string().min(1).optional(),
  trigger: z.enum(["schedule", "github", "incoming"]).optional(),
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

export const createSchema = newAutomationSchema.extend({
  projectId: z.string().min(1).nullable().default(null),
  tz: z.string().max(64).default("UTC"),
});

/** Create from the dialog's three answers: off, owned by its creator, with defaults to fill in. */
async function createAutomation(ctx: RequestContext): Promise<Response> {
  const input = (await parseBody(ctx.request, createSchema)) as z.output<typeof createSchema>;
  await requireEditor(ctx, input.projectId);
  const scope: AutomationScope = input.projectId ? "project" : "organization";
  const settings = automationDefaults(input, input.tz);
  // It starts off with no project; a save names one (INCOMING_NEEDS_PROJECT).
  const unsaved = scope === "organization" && settings.trigger.kind === "incoming" ? { ...settings, projectIds: ["unsaved"] } : settings;
  const parsed = automationInputSchema(scope).safeParse(unsaved);
  if (!parsed.success) throw badRequest("request body failed validation", parsed.error.flatten());
  const id = newId("automation");
  const row = await withOrg(ctx.principal.organizationId, async (scope) => {
    if (input.projectId) {
      const [p] = await scope.sql`SELECT 1 FROM projects WHERE id = ${input.projectId}`;
      if (!p) throw notFound(`project ${input.projectId} not found`);
    }
    const secret = settings.kind === "webhook" ? newSigningSecret() : null;
    const incoming = settings.trigger.kind === "incoming" ? newSigningSecret() : null;
    await scope.sql`
      INSERT INTO automations (id, organization_id, project_id, name, kind, owner_id, trigger, agent, command, server, webhook,
        webhook_secret, webhook_hint, incoming_secret, incoming_hint, created_by, updated_by)
      VALUES (${id}, ${scope.organizationId}, ${input.projectId}, ${settings.name}, ${settings.kind}, ${ctx.principal.personId},
        ${settings.trigger}::jsonb, ${settings.agent}::jsonb, ${settings.command}, ${settings.server}::jsonb, ${settings.webhook}::jsonb,
        ${secret}, ${secret ? secretHint(secret) : null}, ${incoming}, ${incoming ? secretHint(incoming) : null},
        ${ctx.principal.personId}, ${ctx.principal.personId})`;
    await record(scope, ctx, { id, name: settings.name, projectId: input.projectId }, { created: true });
    return { row: await readOne(scope, id), incoming };
  });
  // An incoming webhook's secret is answered this once (SecretOnce), never again.
  return json({ ...(await shape(ctx, row.row)), ...(row.incoming ? { incomingSecret: row.incoming } : {}) }, 201);
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
  if (input.projectIds.length > 0) {
    const found = (await scope.sql`SELECT id FROM projects WHERE id = ANY(${scope.sql.array(input.projectIds, "text")}::text[])`) as Array<{ id: string }>;
    const missing = input.projectIds.filter((p) => !found.some((f) => f.id === p));
    if (missing.length) throw badRequest(`there is no project ${missing[0]}`);
    if (Array.isArray(input.repos)) {
      const repos = (await scope.sql`SELECT id FROM repositories WHERE project_id = ANY(${scope.sql.array(input.projectIds, "text")}::text[])
        AND id = ANY(${scope.sql.array(input.repos, "text")}::text[])`) as Array<{ id: string }>;
      const outside = input.repos.filter((r) => !repos.some((f) => f.id === r));
      if (outside.length) throw badRequest(`repository ${outside[0]} is in none of the automation's projects`);
    }
  }
}

/** The trigger a save writes: an incoming one keeps its hook (its URL), or gets one when it becomes incoming. */
function savedTrigger(input: AutomationInput["trigger"], current: Row["trigger"]): AutomationInput["trigger"] {
  if (input.kind !== "incoming") return input;
  return { ...input, hookId: current.kind === "incoming" ? current.hookId : newHookId() };
}

/** Replace an automation's settings. Its on/off, owner and signing secret have routes of their own. */
async function putAutomation(ctx: RequestContext): Promise<Response> {
  const id = ctx.params.id!;
  const row = await withOrg(ctx.principal.organizationId, async (scope) => {
    const current = await readOne(scope, id);
    await requireEditor(ctx, current.projectId);
    const input = (await parseBody(ctx.request, automationInputSchema(current.projectId ? "project" : "organization"))) as AutomationInput;
    await checkReferences(scope, input, current);
    input.trigger = savedTrigger(input.trigger, current.trigger);
    const triggerChanged = JSON.stringify(input.trigger) !== JSON.stringify(current.trigger);
    // Becoming incoming (a new hook) makes its verification secret, answered this once; leaving it drops the secret.
    const fresh = input.trigger.kind === "incoming" && current.trigger.kind !== "incoming" ? newSigningSecret() : null;
    const incoming = input.trigger.kind === "incoming";
    await scope.sql`
      UPDATE automations SET name = ${input.name}, description = ${input.description}, kind = ${input.kind},
        trigger = ${input.trigger}::jsonb, session_mode = ${input.sessionMode}, expiry_days = ${input.expiryDays},
        concurrency = ${input.concurrency}, parallel_max = ${input.parallelMax}, repos = ${input.repos}::jsonb,
        repo_filter = ${input.repoFilter}::jsonb, agent = ${input.agent}::jsonb, machine_size_id = ${input.machineSizeId},
        image_id = ${input.imageId}, setup_script = ${input.setupScript}, command = ${input.command},
        server = ${input.server}::jsonb, webhook = ${input.webhook}::jsonb,
        webhook_secret = CASE WHEN ${input.kind} = 'webhook' THEN COALESCE(webhook_secret, ${newSigningSecret()}) ELSE webhook_secret END,
        incoming_secret = CASE WHEN ${incoming} THEN COALESCE(${fresh}, incoming_secret) END,
        incoming_hint = CASE WHEN ${incoming} THEN COALESCE(${fresh ? secretHint(fresh) : null}, incoming_hint) END,
        project_ids = ${scope.sql.array(input.projectIds, "text")}::text[],
        secrets = ${input.secrets}::jsonb, notify_on_failure = ${input.notifyOnFailure},
        next_run_at = CASE WHEN ${triggerChanged} THEN NULL ELSE next_run_at END,
        updated_by = ${ctx.principal.personId}, updated_at = now()
      WHERE id = ${id}`;
    await scope.sql`UPDATE automations SET webhook_hint = right(webhook_secret, 4) WHERE id = ${id} AND webhook_hint IS NULL AND webhook_secret IS NOT NULL`;
    await keepPromptVersion(scope, ctx, id, current.agent, input.agent, null);
    await record(scope, ctx, { id, name: input.name, projectId: current.projectId }, { settings: true });
    return { row: await readOne(scope, id), fresh };
  });
  return json({ ...(await shape(ctx, row.row)), ...(row.fresh ? { incomingSecret: row.fresh } : {}) });
}

/**
 * Delete an automation and its runs' history. A kept session's container is
 * let go first (the orchestrator's Start fresh cancels its lux Run): the
 * session row goes with the automation, and nothing would end it after.
 */
async function deleteAutomation(ctx: RequestContext): Promise<Response> {
  const id = ctx.params.id!;
  const current = await withOrg(ctx.principal.organizationId, (scope) => readOne(scope, id));
  await requireEditor(ctx, current.projectId);
  const [kept] = await withOrg(ctx.principal.organizationId, ({ sql }) => sql`SELECT 1 FROM automation_sessions WHERE automation_id = ${id}`);
  if (kept) {
    const res = await forward(ctx, "POST", `/internal/automations/${encodeURIComponent(id)}/start-fresh`);
    if (!res.ok) throw new HttpError(502, "its kept session could not be ended; nothing was deleted", "session_not_ended");
  }
  await withOrg(ctx.principal.organizationId, async (scope) => {
    await scope.sql`DELETE FROM automations WHERE id = ${id}`;
    await record(scope, ctx, current, { deleted: true });
  });
  return noContent();
}

/**
 * Copy an automation's settings into a new one: off, "<name> (copy)", owned
 * by whoever copied it, with no runs, no kept session and no prompt history.
 * A webhook gets a signing secret of its own.
 */
async function duplicateAutomation(ctx: RequestContext): Promise<Response> {
  const id = ctx.params.id!;
  const copy = newId("automation");
  const row = await withOrg(ctx.principal.organizationId, async (scope) => {
    const a = await readOne(scope, id);
    await requireEditor(ctx, a.projectId);
    const name = `${a.name.slice(0, 120 - " (copy)".length)} (copy)`;
    const secret = a.kind === "webhook" ? newSigningSecret() : null;
    // A copy of an incoming webhook gets a URL and a secret of its own.
    const incoming = a.trigger.kind === "incoming" ? newSigningSecret() : null;
    const trigger = a.trigger.kind === "incoming" ? { ...a.trigger, hookId: newHookId() } : a.trigger;
    await scope.sql`
      INSERT INTO automations (id, organization_id, project_id, name, description, kind, enabled, owner_id, trigger, session_mode,
        expiry_days, concurrency, parallel_max, repos, repo_filter, agent, machine_size_id, image_id, setup_script, command, server,
        webhook, webhook_secret, webhook_hint, incoming_secret, incoming_hint, project_ids, secrets, notify_on_failure, created_by, updated_by)
      SELECT ${copy}, organization_id, project_id, ${name}, description, kind, false, ${ctx.principal.personId}, ${trigger}::jsonb, session_mode,
        expiry_days, concurrency, parallel_max, repos, repo_filter, agent, machine_size_id, image_id, setup_script, command, server,
        webhook, ${secret}, ${secret ? secretHint(secret) : null}, ${incoming}, ${incoming ? secretHint(incoming) : null}, project_ids,
        secrets, notify_on_failure, ${ctx.principal.personId}, ${ctx.principal.personId}
      FROM automations WHERE id = ${id}`;
    await record(scope, ctx, { id: copy, name, projectId: a.projectId }, { created: true, duplicateOf: id });
    return { row: await readOne(scope, copy), incoming };
  });
  return json({ ...(await shape(ctx, row.row)), ...(row.incoming ? { incomingSecret: row.incoming } : {}) }, 201);
}

async function setEnabled(ctx: RequestContext): Promise<Response> {
  const id = ctx.params.id!;
  const { enabled } = await parseBody(ctx.request, enabledSchema);
  const row = await withOrg(ctx.principal.organizationId, async (scope) => {
    const current = await readOne(scope, id);
    await requireEditor(ctx, current.projectId);
    // An organisation incoming automation runs in its projects: none named, nothing to file in or check out.
    if (enabled && current.projectId === null && current.trigger.kind === "incoming" && current.projectIds.length === 0) {
      throw new HttpError(400, INCOMING_NEEDS_PROJECT, "incoming_needs_project");
    }
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

// ---------------------------------------------------------------------------
// Runs
// ---------------------------------------------------------------------------

const RUN_COLUMNS = `r.id, r.automation_id AS "automationId", r.status, r.reason, r.source, r.session,
  r.lux_run_id AS "luxRunId", r.cost_usd::float8 AS "costUsd", r.created_at AS "createdAt", r.started_at AS "startedAt",
  r.ended_at AS "endedAt", r.payloads->0->>'event' AS event, r.payloads->0->>'action' AS action,
  jsonb_array_length(r.payloads) AS "payloadCount",
  (SELECT json_build_object('id', p.id, 'name', p.name, 'photoUrl', person_ref(p)->>'photoUrl') FROM people p WHERE p.id = r.started_by) AS "startedBy"`;

export const runsQuery = z.object({
  status: z.enum(["queued", "running", "succeeded", "failed", "skipped", "cancelled"]).optional(),
  limit: z.coerce.number().int().min(1).max(200).default(50),
});

function runOut(r: Record<string, unknown>) {
  const { luxRunId: _lux, ...rest } = r;
  return { ...rest, createdAt: iso(r.createdAt), startedAt: iso(r.startedAt), endedAt: iso(r.endedAt) };
}

/** An automation's runs, newest first: queued and skipped ones with their reason. */
async function listRuns(ctx: RequestContext): Promise<Response> {
  const parsed = runsQuery.safeParse(Object.fromEntries(ctx.url.searchParams));
  if (!parsed.success) throw badRequest("unknown run filter", parsed.error.flatten());
  const out = await withOrg(ctx.principal.organizationId, async (scope) => {
    const a = await readOne(scope, ctx.params.id!);
    const runs = (await scope.sql.unsafe(`SELECT ${RUN_COLUMNS} FROM automation_runs r
      WHERE r.automation_id = $1 AND ($2::text IS NULL OR r.status = $2) ORDER BY r.created_at DESC, r.id DESC LIMIT $3`,
    [a.id, parsed.data.status ?? null, parsed.data.limit])) as Array<Record<string, unknown>>;
    const [kept] = (await scope.sql`SELECT created_at AS "since", last_run_at AS "lastRunAt",
        (SELECT count(*)::int FROM automation_runs r WHERE r.automation_id = s.automation_id AND r.session = 'resumed') AS resumes
      FROM automation_sessions s WHERE s.automation_id = ${a.id}`) as Array<{ since: Date; lastRunAt: Date; resumes: number }>;
    const [{ total }] = (await scope.sql`SELECT count(*)::int AS total FROM automation_runs WHERE automation_id = ${a.id}`) as [{ total: number }];
    return {
      runs: runs.map(runOut),
      total,
      session: kept ? { since: iso(kept.since), lastRunAt: iso(kept.lastRunAt), resumes: kept.resumes,
        expiresAt: a.expiryDays ? new Date(kept.lastRunAt.getTime() + a.expiryDays * 86_400_000).toISOString() : null } : null,
    };
  });
  return json(out);
}

/** One run: what triggered it, the end of its log, and for a Server its container's timeline and requests. */
async function getRun(ctx: RequestContext): Promise<Response> {
  const out = await withOrg(ctx.principal.organizationId, async (scope) => {
    const a = await readOne(scope, ctx.params.id!);
    const [r] = (await scope.sql.unsafe(`SELECT ${RUN_COLUMNS}, r.payloads, r.payloads_purged_at IS NOT NULL AS "payloadsPurged",
        r.log_tail AS "logTail", r.exit_code AS "exitCode", r.container_started_at AS "containerStartedAt",
        r.container_stopped_at AS "containerStoppedAt", r.stop_reason AS "stopReason"
      FROM automation_runs r WHERE r.automation_id = $1 AND r.id = $2`, [a.id, ctx.params.runId!])) as Array<Record<string, unknown>>;
    if (!r) throw notFound(`automation ${a.id} has no run ${ctx.params.runId}`);
    const requests = (await scope.sql`SELECT id, delivery_id AS "deliveryId", try, http_status AS "httpStatus", ms, error,
        response_excerpt AS "responseExcerpt", at FROM automation_run_requests WHERE run_id = ${r.id as string} ORDER BY at, try`) as Array<Record<string, unknown>>;
    // An incoming run's deliveries: the one that started it and the later ones of its group sent to it.
    const deliveries = a.trigger.kind === "incoming" ? await runDeliveries(scope, a.id, r.id as string) : undefined;
    return { ...runOut(r), containerStartedAt: iso(r.containerStartedAt), containerStoppedAt: iso(r.containerStoppedAt),
      requests: requests.map((q) => ({ ...q, at: iso(q.at) })), ...(deliveries ? { deliveries } : {}),
      ...(await toolCalls(scope, r.id as string)) };
  });
  return json(out);
}

/** The ledger events an automation run's tool calls leave (orchestrator agenttools), and the call it failed on (automations). */
const TOOL_EVENTS = ["agent.tool.dude", "agent.tool.attempted", "slack.message.posted", "slack.message.refused", "automation.run.failed_on_post"];

/**
 * An automation run's calls to dude's tools, oldest first: each finished
 * call (`agent.tool.dude`), and each refused post_to_chat with its words. A
 * call that spent an attempt first is timed from it; a post carries its
 * channel and link. A post's text is left out: its message is in Slack.
 */
async function toolCalls(scope: OrgScope, runId: string) {
  // run_id is the automation run's id on every event its agent's calls write (events_run_cursor_idx).
  const rows = (await scope.sql`SELECT id, event_type AS type, occurred_at AS at, payload FROM events
    WHERE run_id = ${runId} AND event_type = ANY(${scope.sql.array(TOOL_EVENTS, "text")}::text[])
    ORDER BY occurred_at, cursor`) as Array<{ id: string; type: string; at: Date; payload: Record<string, unknown> }>;
  const attempts = new Map<string, Date[]>();
  const posts: Array<{ channel: string; channelId: string; ts: string; permalink: string }> = [];
  for (const e of rows) {
    const tool = String(e.payload.tool ?? "");
    const str = (k: string) => String(e.payload[k] ?? "");
    if (e.type === "agent.tool.attempted" && !e.payload.attempted) attempts.set(tool, [...(attempts.get(tool) ?? []), e.at]);
    if (e.type === "slack.message.posted") posts.push({ channel: str("channel"), channelId: str("channelId"), ts: str("ts"), permalink: str("permalink") });
  }
  // The attempt a call wrote before its outside work, when it wrote one: the
  // earliest of its tool's not yet taken that came before the call ended.
  const startOf = (tool: string, end: Date) => {
    const queue = attempts.get(tool);
    return queue && queue[0] && queue[0] <= end ? queue.shift()! : null;
  };
  const out = [];
  for (const e of rows) {
    if (e.type !== "agent.tool.dude" && e.type !== "slack.message.refused") continue;
    const tool = String(e.payload.tool ?? "");
    const { text: _text, body: _body, ...args } = (e.payload.arguments ?? {}) as Record<string, unknown>;
    const start = e.type === "agent.tool.dude" && !e.payload.attempted ? null : startOf(tool, e.at);
    const failed = e.type === "slack.message.refused";
    out.push({
      id: e.id, tool, at: iso(start ?? e.at)!, status: failed ? "failed" : "completed",
      args: failed ? { channel: e.payload.channel ?? null } : args,
      durationMs: start ? Math.max(0, e.at.getTime() - start.getTime()) : null,
      error: failed ? String(e.payload.error ?? "") : null,
      posted: !failed && tool === "post_to_chat" ? posts.shift() ?? null : null,
    });
  }
  // The refusal the orchestrator failed the run on, as it recorded it (automations.EvRunFailedOnPost).
  const failedOn = rows.find((e) => e.type === "automation.run.failed_on_post");
  return { toolCalls: out, failedCallId: failedOn ? String(failedOn.payload.eventId) : null };
}

/**
 * What reached the automation in the last 30 days and what dude did with
 * it: its Deliveries tab, and Run now's "A past delivery" list
 * (routes/incoming.ts reads them).
 */
async function listDeliveries(ctx: RequestContext): Promise<Response> {
  const verdict = ctx.url.searchParams.get("verdict");
  const filters = [...DELIVERY_VERDICTS, "pending", "accepted"];
  if (verdict !== null && !filters.includes(verdict)) throw badRequest(`unknown verdict ${verdict}: one of ${filters.join(", ")}`);
  const out = await withOrg(ctx.principal.organizationId, async (scope) => {
    const a = await readOne(scope, ctx.params.id!);
    if (a.trigger.kind === "schedule") throw badRequest("a scheduled automation runs on no event");
    return { deliveries: await readDeliveries(scope, a.id, a.trigger, verdict as DeliveryFilter | null),
      ...(a.trigger.kind === "incoming" ? { refusedNotStored: await refusedNotStored(scope, a.id) } : {}) };
  });
  return json(out);
}

async function getDelivery(ctx: RequestContext): Promise<Response> {
  const out = await withOrg(ctx.principal.organizationId, async (scope) => {
    const a = await readOne(scope, ctx.params.id!);
    if (a.trigger.kind === "schedule") throw badRequest("a scheduled automation runs on no event");
    const d = await readDelivery(scope, a.id, a.trigger, ctx.params.deliveryId!);
    if (!d) throw notFound(`automation ${a.id} has no delivery ${ctx.params.deliveryId}`);
    return d;
  });
  return json(out);
}

/** A new verification secret for an incoming webhook: the old one stops working now; the new one is answered this once. */
async function rotateIncomingSecret(ctx: RequestContext): Promise<Response> {
  const id = ctx.params.id!;
  const secret = newSigningSecret();
  const row = await withOrg(ctx.principal.organizationId, async (scope) => {
    const current = await readOne(scope, id);
    await requireEditor(ctx, current.projectId);
    if (current.trigger.kind !== "incoming") throw badRequest("only an incoming webhook has a verification secret");
    await scope.sql`UPDATE automations SET incoming_secret = ${secret}, incoming_hint = ${secretHint(secret)}, updated_at = now(),
      updated_by = ${ctx.principal.personId} WHERE id = ${id}`;
    await record(scope, ctx, current, { incomingSecret: "rotated" });
    return readOne(scope, id);
  });
  return json({ ...(await shape(ctx, row)), incomingSecret: secret });
}

// ---------------------------------------------------------------------------
// Prompt history
// ---------------------------------------------------------------------------

/** An agent's text: its own prompt, or a preset's brief; null for no agent. */
function promptOf(agent: AutomationInput["agent"] | Row["agent"]): string | null {
  if (!agent) return null;
  return "own" in agent ? agent.own.prompt : agent.brief;
}

/**
 * Keep the text a save leaves as a new version when it differs from the
 * last one kept. The first save of an automation with no history keeps the
 * text it had before too, so its first version is what it was created with.
 */
async function keepPromptVersion(scope: OrgScope, ctx: RequestContext, id: string, before: Row["agent"],
  after: AutomationInput["agent"], restoredFrom: string | null): Promise<void> {
  const body = promptOf(after);
  if (body === null) return;
  const [last] = (await scope.sql`SELECT number, body FROM automation_prompt_versions WHERE automation_id = ${id}
    ORDER BY number DESC LIMIT 1 FOR UPDATE`) as Array<{ number: number; body: string }>;
  let number = last?.number ?? 0;
  const old = promptOf(before);
  if (!last && old !== null && old !== body) {
    number += 1;
    await scope.sql`INSERT INTO automation_prompt_versions (id, organization_id, automation_id, number, body, created_by)
      VALUES (${newId("automationPromptVersion")}, ${scope.organizationId}, ${id}, ${number}, ${old}, NULL)`;
  }
  if ((last ? last.body : old) === body && !restoredFrom) return;
  await scope.sql`INSERT INTO automation_prompt_versions (id, organization_id, automation_id, number, body, created_by, restored_from)
    VALUES (${newId("automationPromptVersion")}, ${scope.organizationId}, ${id}, ${number + 1}, ${body}, ${ctx.principal.personId}, ${restoredFrom})`;
}

async function readPromptHistory(scope: OrgScope, id: string): Promise<AutomationPromptVersion[]> {
  const rows = (await scope.sql`SELECT v.id, v.number, v.body, v.created_at AS "createdAt",
      (SELECT json_build_object('id', p.id, 'name', p.name) FROM people p WHERE p.id = v.created_by) AS "createdBy",
      (SELECT r.number FROM automation_prompt_versions r WHERE r.id = v.restored_from) AS "restoredFrom"
    FROM automation_prompt_versions v WHERE v.automation_id = ${id} ORDER BY v.number DESC`) as Array<Omit<AutomationPromptVersion, "current">>;
  return rows.map((r, i) => ({ ...r, createdAt: iso(r.createdAt)!, current: i === 0 }));
}

async function promptHistory(ctx: RequestContext): Promise<Response> {
  const versions = await withOrg(ctx.principal.organizationId, async (scope) => {
    await readOne(scope, ctx.params.id!);
    return readPromptHistory(scope, ctx.params.id!);
  });
  return json({ versions });
}

/** Save an earlier version's text again, as the newest: the history only grows. */
async function restorePrompt(ctx: RequestContext): Promise<Response> {
  const id = ctx.params.id!;
  const { versionId } = await parseBody(ctx.request, restorePromptSchema);
  const row = await withOrg(ctx.principal.organizationId, async (scope) => {
    const current = await readOne(scope, id);
    await requireEditor(ctx, current.projectId);
    const [v] = (await scope.sql`SELECT body FROM automation_prompt_versions WHERE id = ${versionId} AND automation_id = ${id}`) as Array<{ body: string }>;
    if (!v) throw notFound(`no version ${versionId} of this automation's prompt`);
    if (!current.agent) throw badRequest("this automation has no agent prompt");
    const agent = "own" in current.agent ? { own: { ...current.agent.own, prompt: v.body } } : { ...current.agent, brief: v.body };
    await scope.sql`UPDATE automations SET agent = ${agent}::jsonb, updated_by = ${ctx.principal.personId}, updated_at = now() WHERE id = ${id}`;
    await keepPromptVersion(scope, ctx, id, current.agent, agent as AutomationInput["agent"], versionId);
    await record(scope, ctx, current, { promptRestored: versionId });
    return readOne(scope, id);
  });
  return json(await shape(ctx, row));
}

/** Who may run an automation by hand: whoever may edit it, and its owner. */
async function requireRunner(ctx: RequestContext, id: string): Promise<void> {
  const a = await withOrg(ctx.principal.organizationId, (scope) => readOne(scope, id));
  if (a.ownerId === ctx.principal.personId || (await canEdit(ctx, a.projectId))) return;
  throw new HttpError(403, "only the automation's editors and its owner run it by hand", "not_editor");
}

/** Forward to the orchestrator as this person, passing its answer back. */
function forward(ctx: RequestContext, method: string, path: string, body?: string): Promise<Response> {
  return orchestrator(ctx.principal.organizationId, method, path, body, ctx.principal);
}

export const runNowBody = z.union([
  z.object({}).strict(),
  z.object({ deliveryId: z.string().min(1).max(200) }).strict(),
  z.object({ testEvent: z.object({ event: z.string().regex(/^[a-z][a-z0-9_]{0,63}$/), action: z.string().max(64).nullable().optional(),
    deliveryId: z.string().min(1).max(200), body: z.record(z.unknown()) }).strict() }).strict(),
  z.object({ payload: z.record(z.unknown()) }).strict(),
]);

export const evaluateBody = z.object({ body: z.record(z.unknown()) }).strict();
export const scheduleNextBody = z.object({ cron: z.string().min(1).max(200), tz: z.string().min(1).max(64) }).strict();

async function runNow(ctx: RequestContext): Promise<Response> {
  const id = ctx.params.id!;
  await requireRunner(ctx, id);
  const input = await parseBody(ctx.request, runNowBody);
  return forward(ctx, "POST", `/internal/automations/${encodeURIComponent(id)}/runs`, JSON.stringify(input));
}

async function evaluateTestEvent(ctx: RequestContext): Promise<Response> {
  const input = await parseBody(ctx.request, evaluateBody);
  return forward(ctx, "POST", `/internal/automations/${encodeURIComponent(ctx.params.id!)}/test-event/evaluate`, JSON.stringify(input));
}

async function startFresh(ctx: RequestContext): Promise<Response> {
  const id = ctx.params.id!;
  await requireRunner(ctx, id);
  return forward(ctx, "POST", `/internal/automations/${encodeURIComponent(id)}/start-fresh`);
}

export function registerAutomationRoutes(router: Router): void {
  router.get("/v1/automations", listOrg);
  router.post("/v1/automations", createAutomation);
  router.get("/v1/automations/repositories", orgRepositories);
  // A schedule's next three times: the orchestrator's scheduler computes them.
  router.post("/v1/automations/schedule/next", async (ctx) => {
    const input = await parseBody(ctx.request, scheduleNextBody);
    return forward(ctx, "POST", "/internal/automations/schedule/next", JSON.stringify(input));
  });
  // The GitHub events this organisation is subscribed to: the orchestrator reads GitHub.
  router.get("/v1/automations/subscriptions", (ctx) =>
    orchestrator(ctx.principal.organizationId, "GET", "/internal/automations/subscriptions", undefined, ctx.principal));
  router.get("/v1/automations/:id", getAutomation);
  router.put("/v1/automations/:id", putAutomation);
  router.delete("/v1/automations/:id", deleteAutomation);
  router.post("/v1/automations/:id/duplicate", duplicateAutomation);
  router.put("/v1/automations/:id/enabled", setEnabled);
  router.put("/v1/automations/:id/owner", transferOwner);
  router.put("/v1/automations/:id/webhook-secret", setWebhookSecret);
  router.get("/v1/automations/:id/runs", listRuns);
  router.post("/v1/automations/:id/runs", runNow);
  router.get("/v1/automations/:id/runs/:runId", getRun);
  router.get("/v1/automations/:id/deliveries", listDeliveries);
  router.get("/v1/automations/:id/deliveries/:deliveryId", getDelivery);
  router.post("/v1/automations/:id/incoming-secret/rotate", rotateIncomingSecret);
  router.post("/v1/automations/:id/test-event/evaluate", evaluateTestEvent);
  router.post("/v1/automations/:id/start-fresh", startFresh);
  router.get("/v1/automations/:id/prompt-history", promptHistory);
  router.post("/v1/automations/:id/prompt-history/restore", restorePrompt);
  router.get("/v1/projects/:id/automations", listProject);
}
