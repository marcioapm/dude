/**
 * Settings: the organization's defaults, and each project's overrides.
 *
 * Two things are configured this way: how each agent role runs (model
 * tier, time limit, machine, whether it runs at all, and its prompt) and
 * how work is delivered. The organization's live on its row
 * (default_agent_models, delivery_policy), a project's on its own
 * (agent_models, delivery_policy) — and a project stores only what it
 * changes, so a missing key is "from the organization" and Reset is a
 * delete. Under both sit the factory's defaults, which the orchestrator
 * owns and applies (GET /internal/delivery-defaults, /internal/prompts/builtin).
 *
 * Prompts keep every save (prompt_versions): the current one is the latest,
 * and a Run records the versions it was told with.
 */

import {
  allowNamesSchema,
  clampTimeLimit,
  deliveryPolicySchema,
  egressAllows,
  EventTypes,
  newId,
  promptRoleSchema,
  resolveMachineSize,
  resolveRoleImage,
  resolveTier,
  ROLE_ENABLED_BY,
  savePromptSchema,
  SETTINGS_ROLES,
  settingsPatchSchema,
  type AgentModels,
  type AgentEgressMode,
  type DeliverySettings,
  type FullDeliveryPolicy,
  type NetworkSettings,
  type ProjectPromptMode,
  type PromptHistory,
  type PromptRole,
  type PromptState,
  type PromptVersion,
  type RefusedName,
  type RoleSettings,
  type SettingSource,
  type SettingsPatch,
  type SettingsResponse,
  type SettingsRole,
} from "@dude/domain";
import { withOrg, type OrgScope } from "../../db/client.ts";
import { appendInScope } from "../../events/ledger.ts";
import { canEditProject, isOrgAdmin, requireOrgAdmin, requireProjectEditor } from "../access.ts";
import { badRequest, HttpError, json, notFound, parseBody } from "../http.ts";
import { auditActor } from "../auth.ts";
import type { RequestContext, Router } from "../router.ts";
import { orchestrator } from "../../orchestrator/client.ts";
import { config } from "../../config.ts";
import { checkSizes, listSizes } from "./machines.ts";
import { checkTiers, listTiers } from "./models.ts";
import { checkImages, imageIds } from "./images.ts";

type Json = Record<string, unknown>;

/** A value from the orchestrator, which owns the factory's defaults. */
async function fromOrchestrator<T>(ctx: RequestContext, path: string): Promise<T> {
  const res = await orchestrator(ctx.principal.organizationId, "GET", path);
  if (!res.ok) throw new HttpError(res.status, `the orchestrator could not say: ${await res.text()}`, "unavailable");
  return (await res.json()) as T;
}

/**
 * The factory's policy and dude's built-in prompts are constants of the
 * orchestrator's build, so one read serves every request; a failed one is
 * not kept.
 */
function constant<T>(path: string): (ctx: RequestContext) => Promise<T> {
  let cached: Promise<T> | null = null;
  return (ctx) => {
    cached ??= fromOrchestrator<T>(ctx, path).catch((err: unknown) => {
      cached = null;
      throw err;
    });
    return cached;
  };
}

const factoryPolicy = constant<FullDeliveryPolicy>("/internal/delivery-defaults");

type NetworkDefaults = { operator: string[]; always: string[]; model: string | null };
let lastDefaults: { from: string; at: number; value: NetworkDefaults } | null = null;
/**
 * Under every list: the operator's (agent.egress), and the model's host and
 * dude's tools. Configuration, which changes only when the orchestrator
 * restarts: read again after a minute, and while the orchestrator cannot
 * answer its last answer serves, so every settings page and save does not
 * fail with it.
 */
async function networkDefaults(ctx: RequestContext): Promise<NetworkDefaults> {
  const from = config().string("DUDE_ORCHESTRATOR_URL") ?? "";
  const last = lastDefaults?.from === from ? lastDefaults : null;
  if (last && Date.now() - last.at < 60_000) return last.value;
  try {
    const value = await fromOrchestrator<NetworkDefaults>(ctx, "/internal/network/defaults");
    lastDefaults = { from, at: Date.now(), value };
    return value;
  } catch (err) {
    if (!last) throw err;
    // Ask again only in a minute: an orchestrator that hangs then delays one request a minute, not each.
    lastDefaults = { ...last, at: Date.now() };
    return last.value;
  }
}
const builtinPrompts = constant<Record<PromptRole, string>>("/internal/prompts/builtin");

export interface Layers {
  org: { id: string; name: string; agentModels: AgentModels; deliveryPolicy: Json; agentEgress: string[] };
  project?: { id: string; name: string; agentModels: AgentModels; deliveryPolicy: Json; agentEgress: string[]; agentEgressMode: AgentEgressMode };
}

async function loadLayers(scope: OrgScope, projectId?: string, lock = false): Promise<Layers | null> {
  const forUpdate = scope.sql.unsafe(lock ? "FOR UPDATE" : "");
  const orgs = (await scope.sql`
    SELECT id, name, default_agent_models AS "agentModels", delivery_policy AS "deliveryPolicy", agent_egress AS "agentEgress"
    FROM organizations WHERE id = ${scope.organizationId} ${forUpdate}`) as Array<Layers["org"]>;
  if (!orgs[0]) return null;
  if (!projectId) return { org: orgs[0] };
  const projects = (await scope.sql`
    SELECT id, name, agent_models AS "agentModels", delivery_policy AS "deliveryPolicy", agent_egress AS "agentEgress",
      agent_egress_mode AS "agentEgressMode"
    FROM projects WHERE id = ${projectId} ${forUpdate}`) as Array<NonNullable<Layers["project"]>>;
  return projects[0] ? { org: orgs[0], project: projects[0] } : null;
}

// ---------------------------------------------------------------------------
// Prompts
// ---------------------------------------------------------------------------

interface VersionRow {
  id: string;
  projectId: string | null;
  role: PromptRole;
  mode: "add" | "replace" | null;
  body: string;
  note: string;
  createdAt: string;
  createdById: string | null;
  createdByName: string | null;
  restoredFrom: string | null;
  number: number;
  total: number;
}

const VERSION_SELECT = `v.id, v.project_id AS "projectId", v.role, v.mode, v.body, v.note, v.created_at AS "createdAt",
  COALESCE(k.person_id, v.created_by) AS "createdById", COALESCE(p.name, k.name) AS "createdByName", v.restored_from AS "restoredFrom",
  (row_number() OVER w)::int AS number, (count(*) OVER (PARTITION BY v.role, v.project_id))::int AS total`;
const VERSION_FROM = `prompt_versions v LEFT JOIN api_keys k ON k.id = v.created_by LEFT JOIN people p ON p.id = COALESCE(k.person_id, v.created_by)`;
const VERSION_WINDOW = `WINDOW w AS (PARTITION BY v.role, v.project_id ORDER BY v.created_at, v.id)`;

/** Every version of the prompts at one layer (the organization's with no project), newest first. */
async function versions(scope: OrgScope, projectId: string | null, role?: PromptRole): Promise<VersionRow[]> {
  return (await scope.sql`
    SELECT ${scope.sql.unsafe(VERSION_SELECT)} FROM ${scope.sql.unsafe(VERSION_FROM)}
    WHERE v.project_id IS NOT DISTINCT FROM ${projectId} AND (${role ?? null}::text IS NULL OR v.role = ${role ?? null})
    ${scope.sql.unsafe(VERSION_WINDOW)}
    ORDER BY v.created_at DESC, v.id DESC`) as VersionRow[];
}

function promptState(current: VersionRow | undefined, builtin: string): PromptState {
  return current
    ? {
        versionId: current.id,
        body: current.body,
        updatedAt: current.createdAt,
        updatedBy: current.createdById ? { id: current.createdById, name: current.createdByName ?? "Someone" } : null,
        versions: current.total,
      }
    : { versionId: null, body: builtin, updatedAt: null, updatedBy: null, versions: 0 };
}

/** A project's prompt: an empty addition is "use the organization's". */
function projectMode(v: VersionRow | undefined): ProjectPromptMode {
  if (!v || (v.mode === "add" && !v.body.trim())) return "inherit";
  return v.mode ?? "inherit";
}

// ---------------------------------------------------------------------------
// Reading settings
// ---------------------------------------------------------------------------

/**
 * What an agent's Run may reach, as the orchestrator resolves it
 * (phases.RunEgress, egress): the operator's list, the organisation's and
 * the project's ("add"), or the project's alone ("only"), each once; ["*"]
 * when any says anywhere, or when nothing is listed anywhere and there is
 * no model to restrict to. A project's own list or mode is its override.
 */
export function network(layers: Layers, defaults: NetworkDefaults): NetworkSettings {
  const { org, project } = layers;
  const own = project ? project.agentEgress : org.agentEgress;
  const runs = project?.agentEgressMode === "only" ? project.agentEgress : [...org.agentEgress, ...(project?.agentEgress ?? [])];
  const effective = [...new Set([...defaults.operator, ...runs])];
  const anywhere = effective.includes("*") || (effective.length === 0 && defaults.model === null);
  return {
    egress: { value: own, source: project && project.agentEgress.length ? "project" : "organization" },
    ...(project
      ? {
          mode: { value: project.agentEgressMode, source: project.agentEgressMode === "only" ? "project" : "organization" },
          organizationEgress: org.agentEgress,
        }
      : {}),
    operator: defaults.operator,
    always: defaults.always,
    effective: anywhere ? ["*"] : effective,
  };
}

async function settingsResponse(ctx: RequestContext, projectId?: string): Promise<SettingsResponse> {
  const [factory, builtin, defaults] = await Promise.all([factoryPolicy(ctx), builtinPrompts(ctx), networkDefaults(ctx)]);
  return withOrg(ctx.principal.organizationId, async (scope) => {
    const layers = await loadLayers(scope, projectId);
    if (!layers) throw notFound(projectId ? `project ${projectId} not found` : "organization not found");
    const orgPrompts = await versions(scope, null);
    const projectPrompts = projectId ? await versions(scope, projectId) : [];
    const latest = (rows: VersionRow[], role: string) => rows.find((v) => v.role === role);

    /** A value: the project's if it sets one, else the organization's (else the fallback). */
    const pick = <T>(project: unknown, org: unknown, fallback: T): { value: T; source: SettingSource } =>
      project !== undefined && layers.project
        ? { value: project as T, source: "project" }
        : { value: (org === undefined ? fallback : org) as T, source: "organization" };

    const orgPolicy = layers.org.deliveryPolicy;
    const projectPolicy = layers.project?.deliveryPolicy ?? {};
    const delivery = Object.fromEntries(
      // The policy a person sets: the factory's has more (conditional
      // reviewers) that nobody edits here.
      (Object.keys(deliveryPolicySchema.shape) as Array<keyof FullDeliveryPolicy>).map((k) => [
        k,
        pick(projectPolicy[k], orgPolicy[k], factory[k]),
      ]),
    ) as DeliverySettings;

    const roleLayer = (models: AgentModels | undefined, role: string) => (models as Record<string, Json> | undefined)?.[role] ?? {};
    /**
     * A role's field from the first layer that sets it: the project's, then
     * the organization's — and for the fixer, which is the implementer told
     * something else, then the implementer's (as the orchestrator resolves it).
     * What the fixer takes from the implementer is not the fixer's own
     * setting, so it is never the project's to reset there.
     */
    const field = (role: SettingsRole, key: string) => {
      const chain = role === "fixer" ? ["fixer", "implementer"] : [role];
      for (const r of chain) {
        for (const [models, source] of [[layers.project?.agentModels, "project"], [layers.org.agentModels, "organization"]] as const) {
          const v = roleLayer(models, r)[key];
          if (v !== undefined) {
            return { value: v, source: r === role ? source : "organization" } as { value: never; source: SettingSource };
          }
        }
      }
      return { value: null as never, source: "organization" as SettingSource };
    };
    /** A time limit as it is read: one stored below today's minimum is the minimum. */
    const timeLimit = (s: { value: number | null; source: SettingSource }) =>
      s.value === null ? s : { ...s, value: clampTimeLimit(s.value) };
    /**
     * The size, by the one rule (resolveMachineSize): null when it is the
     * default's. With what the organization says under a project's, and
     * whether the fixer's is the implementer's.
     */
    const sizes = await listSizes(scope);
    const images = await imageIds(scope);
    /** A role's image, by the one rule (resolveRoleImage), shaped as machineSize is. */
    const image = (role: SettingsRole): RoleSettings["image"] => {
      const { imageId, from } = resolveRoleImage(role, { project: layers.project?.agentModels, organization: layers.org.agentModels }, images);
      const orgOnly = resolveRoleImage(role, { organization: layers.org.agentModels }, images);
      return {
        value: imageId,
        source: from === "project" ? "project" : "organization",
        ...(role === "fixer" ? { followsImplementer: from === "implementer" } : {}),
        ...(layers.project ? { organization: orgOnly.imageId } : {}),
      };
    };
    const machine = (role: SettingsRole): RoleSettings["machineSize"] => {
      const { sizeId, from } = resolveMachineSize(role, { project: layers.project?.agentModels, organization: layers.org.agentModels }, sizes);
      const orgOnly = resolveMachineSize(role, { organization: layers.org.agentModels }, sizes);
      return {
        value: from === "default" ? null : sizeId,
        source: from === "project" ? "project" : "organization",
        ...(role === "fixer" ? { followsImplementer: from === "implementer" } : {}),
        ...(layers.project ? { organization: orgOnly.from === "default" ? null : orgOnly.sizeId } : {}),
      };
    };
    /** The tier, by the one rule (resolveTier), shaped as the machine is. */
    const tiers = await listTiers(scope);
    const tier = (role: SettingsRole): RoleSettings["tier"] => {
      const { tierId, from } = resolveTier(role, { project: layers.project?.agentModels, organization: layers.org.agentModels }, tiers);
      return {
        value: tierId,
        source: from === "project" ? "project" : "organization",
        ...(role === "fixer" ? { followsImplementer: from === "implementer" } : {}),
        ...(layers.project ? { organization: resolveTier(role, { organization: layers.org.agentModels }, tiers).tierId } : {}),
      };
    };
    const roles = Object.fromEntries(
      SETTINGS_ROLES.map((role): [SettingsRole, RoleSettings] => {
        const enabledBy = ROLE_ENABLED_BY[role as keyof typeof ROLE_ENABLED_BY];
        const orgCurrent = latest(orgPrompts, role);
        const projectCurrent = latest(projectPrompts, role);
        return [
          role,
          {
            tier: tier(role),
            timeLimitMinutes: timeLimit(field(role, "timeLimitMinutes")),
            machineSize: machine(role),
            image: image(role),
            enabled: enabledBy ? delivery[enabledBy] : null,
            prompt: {
              organization: promptState(orgCurrent, builtin[role]),
              ...(projectId ? { project: { ...promptState(projectCurrent, ""), mode: projectMode(projectCurrent) } } : {}),
            },
          },
        ];
      }),
    ) as Record<SettingsRole, RoleSettings>;

    return {
      organization: { id: layers.org.id, name: layers.org.name },
      ...(layers.project ? { project: { id: layers.project.id, name: layers.project.name } } : {}),
      roles,
      delivery,
      network: network(layers, defaults),
      canEdit: projectId ? await canEditProject(ctx, projectId) : await isOrgAdmin(ctx),
    };
  });
}

// ---------------------------------------------------------------------------
// Changing settings
// ---------------------------------------------------------------------------

/** Set or delete (null) keys of a JSON object, leaving the rest as they were. */
function applyKeys(target: Json, changes: Json): Json {
  const out = { ...target };
  for (const [k, v] of Object.entries(changes)) {
    if (v === undefined) continue;
    if (v === null) delete out[k];
    else out[k] = v;
  }
  return out;
}

/**
 * A patch applied to one layer's stored JSON: role fields into its agent
 * models (a role left with nothing is dropped), delivery into its policy.
 * A role's `enabled` is a delivery setting: the simplifier's pass, the
 * tester's.
 */
function applyPatch(models: AgentModels, policy: Json, patch: SettingsPatch): { models: AgentModels; policy: Json } {
  const nextModels = { ...(models as Record<string, Json>) };
  const policyChanges: Json = { ...patch.delivery };
  for (const [role, change] of Object.entries(patch.roles ?? {})) {
    const { enabled, ...fields } = change!;
    if (enabled !== undefined) {
      const key = ROLE_ENABLED_BY[role as keyof typeof ROLE_ENABLED_BY];
      if (!key) throw badRequest(`the ${role} always runs; it cannot be turned off`);
      policyChanges[key] = enabled;
    }
    const next = applyKeys(nextModels[role] ?? {}, fields);
    if (Object.keys(next).length) nextModels[role] = next;
    else delete nextModels[role];
  }
  return { models: nextModels as AgentModels, policy: applyKeys(policy, policyChanges) };
}

/**
 * Every machine size, model tier and image a patch names must be the
 * organization's, and an image must not be archived. Each table's rows are
 * held FOR SHARE in id order, before the caller locks the organization or
 * project row: removing a tier and archiving or publishing an image lock
 * their own row first and the organization's after.
 */
async function checkReferences(scope: OrgScope, patch: SettingsPatch) {
  const changes = Object.values(patch.roles ?? {});
  await checkSizes(scope, changes.flatMap((change) => (change?.machineSize ? [change.machineSize] : [])));
  await checkTiers(scope, patch.roles ?? {});
  await checkImages(scope, changes.flatMap((change) => (change?.image ? [change.image] : [])));
}

async function recordSettings(scope: OrgScope, ctx: RequestContext, projectId: string | null, patch: SettingsPatch) {
  await appendInScope(scope, {
    eventType: EventTypes.SettingsUpdated,
    organizationId: ctx.principal.organizationId,
    projectId,
    actor: { type: auditActor(ctx.principal).kind, id: auditActor(ctx.principal).id },
    source: "control-plane",
    payload: { scope: projectId ? "project" : "organization", ...(projectId ? { projectId } : {}), changed: patch },
  });
}

async function getOrganizationSettings(ctx: RequestContext): Promise<Response> {
  return json(await settingsResponse(ctx));
}

async function patchOrganizationSettings(ctx: RequestContext): Promise<Response> {
  await requireOrgAdmin(ctx);
  const patch = await parseBody(ctx.request, settingsPatchSchema);
  await withOrg(ctx.principal.organizationId, async (scope) => {
    // Sizes, tiers and images before the organization row: the order their removal or archiving locks them in.
    await checkReferences(scope, patch);
    const layers = await loadLayers(scope, undefined, true);
    if (!layers) throw notFound("organization not found");
    if (patch.network?.mode) throw badRequest("an organisation's list has no mode: only a project's adds to it or replaces it");
    const next = applyPatch(layers.org.agentModels, layers.org.deliveryPolicy, patch);
    await scope.sql`
      UPDATE organizations SET default_agent_models = ${next.models}::jsonb, delivery_policy = ${next.policy}::jsonb,
        agent_egress = ${scope.sql.array(patch.network?.egress ?? layers.org.agentEgress, "text")}::text[], updated_at = now()
      WHERE id = ${scope.organizationId}`;
    await recordSettings(scope, ctx, null, patch);
  });
  return json(await settingsResponse(ctx));
}

async function getProjectSettings(ctx: RequestContext): Promise<Response> {
  return json(await settingsResponse(ctx, ctx.params.id!));
}

async function patchProjectSettings(ctx: RequestContext): Promise<Response> {
  const projectId = ctx.params.id!;
  await requireProjectEditor(ctx, projectId);
  const patch = await parseBody(ctx.request, settingsPatchSchema);
  await withOrg(ctx.principal.organizationId, async (scope) => {
    // Sizes, tiers and images before the organization and project rows: the order their removal or archiving locks them in.
    await checkReferences(scope, patch);
    const layers = await loadLayers(scope, projectId, true);
    if (!layers?.project) throw notFound(`project ${projectId} not found`);
    const next = applyPatch(layers.project.agentModels, layers.project.deliveryPolicy, patch);
    await scope.sql`
      UPDATE projects SET agent_models = ${next.models}::jsonb, delivery_policy = ${next.policy}::jsonb,
        agent_egress = ${scope.sql.array(patch.network?.egress ?? layers.project.agentEgress, "text")}::text[],
        agent_egress_mode = ${patch.network?.mode ?? layers.project.agentEgressMode}, updated_at = now()
      WHERE id = ${projectId}`;
    await recordSettings(scope, ctx, projectId, patch);
  });
  return json(await settingsResponse(ctx, projectId));
}

// ---------------------------------------------------------------------------
// Prompt history
// ---------------------------------------------------------------------------

function roleParam(ctx: RequestContext): PromptRole {
  const parsed = promptRoleSchema.safeParse(ctx.params.role);
  if (!parsed.success) throw notFound(`no prompt for role ${ctx.params.role}`);
  return parsed.data;
}

const RECENT_SESSIONS = 5;

async function promptHistory(ctx: RequestContext): Promise<Response> {
  const role = roleParam(ctx);
  const projectId = ctx.url.searchParams.get("projectId") || null;
  const builtin = (await builtinPrompts(ctx))[role];
  const history = await withOrg(ctx.principal.organizationId, async (scope): Promise<PromptHistory> => {
    if (projectId && (await scope.sql`SELECT 1 FROM projects WHERE id = ${projectId}`).length === 0) {
      throw notFound(`project ${projectId} not found`);
    }
    const rows = await versions(scope, projectId, role);
    // The sessions each version told: a Run records the organization's
    // version and its project's apart.
    const column = scope.sql.unsafe(projectId ? "r.project_prompt_version_id" : "r.prompt_version_id");
    const counts = (await scope.sql`
      SELECT ${column} AS id, count(*)::int AS n
      FROM runs r JOIN prompt_versions v ON v.id = ${column}
      WHERE v.role = ${role} AND v.project_id IS NOT DISTINCT FROM ${projectId}
      GROUP BY 1`) as Array<{ id: string; n: number }>;
    const recent = (await scope.sql`
      SELECT id, "runId", "taskId", "taskKey", "taskTitle", phase, "createdAt" FROM (
        SELECT ${column} AS id, r.id AS "runId", t.id AS "taskId", p.key_prefix || '-' || t.number AS "taskKey",
               t.title AS "taskTitle", r.phase::text AS phase, r.created_at AS "createdAt",
               row_number() OVER (PARTITION BY ${column} ORDER BY r.created_at DESC) AS n
        FROM runs r JOIN prompt_versions v ON v.id = ${column}
          JOIN tasks t ON t.id = r.task_id JOIN projects p ON p.id = t.project_id
        WHERE v.role = ${role} AND v.project_id IS NOT DISTINCT FROM ${projectId}) ranked
      WHERE n <= ${RECENT_SESSIONS} ORDER BY "createdAt" DESC`) as Array<PromptVersion["sessions"]["recent"][number] & { id: string }>;
    const countOf = new Map(counts.map((c) => [c.id, c.n]));
    return {
      role,
      projectId,
      builtin,
      versions: rows.map((v, i) => ({
        id: v.id,
        number: v.number,
        body: v.body,
        note: v.note,
        mode: v.mode,
        createdAt: v.createdAt,
        createdBy: v.createdById ? { id: v.createdById, name: v.createdByName ?? "Someone" } : null,
        restoredFrom: v.restoredFrom,
        current: i === 0,
        sessions: {
          count: countOf.get(v.id) ?? 0,
          recent: recent.filter((r) => r.id === v.id).map(({ id: _id, ...r }) => r),
        },
      })),
    };
  });
  return json(history);
}

/**
 * Save a new version of a role's prompt. An organization's first save
 * records dude's built-in prompt first, so its history starts from what
 * its agents were told until then.
 */
async function insertVersion(
  scope: OrgScope,
  ctx: RequestContext,
  role: PromptRole,
  projectId: string | null,
  mode: "add" | "replace" | null,
  body: string,
  note: string,
  restoredFrom: string | null,
  builtin: string,
): Promise<{ id: string; unchanged: boolean }> {
  // One save at a time per layer: the row that owns it serialises them.
  if (projectId) {
    if ((await scope.sql`SELECT 1 FROM projects WHERE id = ${projectId} FOR UPDATE`).length === 0) {
      throw notFound(`project ${projectId} not found`);
    }
  } else {
    await scope.sql`SELECT 1 FROM organizations WHERE id = ${scope.organizationId} FOR UPDATE`;
  }
  const current = (await versions(scope, projectId, role))[0];
  if (current && current.body === body && current.mode === mode) return { id: current.id, unchanged: true };
  if (!current && !projectId) {
    if (builtin === body) return { id: "", unchanged: true };
    await scope.sql`
      INSERT INTO prompt_versions (id, organization_id, project_id, role, mode, body, note, created_by)
      VALUES (${newId("promptVersion")}, ${scope.organizationId}, NULL, ${role}, NULL, ${builtin}, 'dude’s built-in prompt', NULL)`;
  }
  const id = newId("promptVersion");
  await scope.sql`
    INSERT INTO prompt_versions (id, organization_id, project_id, role, mode, body, note, created_by, restored_from)
    VALUES (${id}, ${scope.organizationId}, ${projectId}, ${role}, ${mode}, ${body}, ${note}, ${auditActor(ctx.principal).id}, ${restoredFrom})`;
  await appendInScope(scope, {
    eventType: EventTypes.PromptSaved,
    organizationId: scope.organizationId,
    projectId,
    actor: { type: auditActor(ctx.principal).kind, id: auditActor(ctx.principal).id },
    source: "control-plane",
    payload: { role, versionId: id, ...(projectId ? { projectId, mode } : {}), ...(restoredFrom ? { restoredFrom } : {}) },
  });
  return { id, unchanged: false };
}

async function savePrompt(ctx: RequestContext): Promise<Response> {
  const role = roleParam(ctx);
  const input = await parseBody(ctx.request, savePromptSchema);
  const projectId = input.projectId ?? null;
  if (projectId) await requireProjectEditor(ctx, projectId);
  else await requireOrgAdmin(ctx);

  let mode: "add" | "replace" | null = null;
  let body = input.body ?? "";
  if (projectId) {
    if (!input.mode) throw badRequest("a project's prompt says how it goes with its organization's: mode add, replace or inherit");
    // Using the organization's as is: an empty addition.
    mode = input.mode === "inherit" ? "add" : input.mode;
    if (input.mode === "inherit") body = "";
  } else if (input.mode) {
    throw badRequest("an organization's prompt has no mode");
  }
  if (!body.trim() && mode !== "add") throw badRequest("a prompt that replaces another needs text");

  const builtin = (await builtinPrompts(ctx))[role];
  const saved = await withOrg(ctx.principal.organizationId, (scope) =>
    insertVersion(scope, ctx, role, projectId, mode, body, input.note ?? "", null, builtin),
  );
  return json(await settingsResponse(ctx, projectId ?? undefined), saved.unchanged ? 200 : 201);
}

async function restorePrompt(ctx: RequestContext): Promise<Response> {
  const id = ctx.params.id!;
  const builtin = await builtinPrompts(ctx);
  const restored = await withOrg(ctx.principal.organizationId, async (scope) => {
    const rows = (await scope.sql`
      SELECT ${scope.sql.unsafe(VERSION_SELECT)} FROM ${scope.sql.unsafe(VERSION_FROM)}
      JOIN prompt_versions it ON it.id = ${id} AND it.role = v.role AND it.project_id IS NOT DISTINCT FROM v.project_id
      ${scope.sql.unsafe(VERSION_WINDOW)}`) as VersionRow[];
    const version = rows.find((r) => r.id === id);
    if (!version) throw notFound(`prompt version ${id} not found`);
    if (version.projectId) await requireProjectEditor(ctx, version.projectId);
    else await requireOrgAdmin(ctx);
    await insertVersion(scope, ctx, version.role, version.projectId, version.mode, version.body, `Restored v${version.number}`, id, builtin[version.role]);
    return version.projectId;
  });
  return json(await settingsResponse(ctx, restored ?? undefined));
}

// ---------------------------------------------------------------------------
// The agent network: names refused, and Allow
// ---------------------------------------------------------------------------

/**
 * The names a project's agents looked up in the last `days` and lux
 * refused, refused in the most Runs first: one row per name. A name the
 * project's Runs would now reach (listed since, or under a wildcard) is
 * left out: it is no longer refused. At most the first 200 are listed,
 * read from the first 500 so names allowed since still leave 200 to show.
 */
async function refusedNames(ctx: RequestContext): Promise<Response> {
  const projectId = ctx.params.id!;
  const days = Number(ctx.url.searchParams.get("days") ?? "7");
  if (!Number.isInteger(days) || days < 1 || days > 90) throw badRequest("days is a whole number from 1 to 90");
  const [defaults, settings] = await Promise.all([networkDefaults(ctx), settingsLayers(ctx, projectId)]);
  const reach = network(settings, defaults).effective;
  const rows = await withOrg(ctx.principal.organizationId, (scope) => scope.sql`
    SELECT name, count(*)::int AS runs, array_agg(DISTINCT role ORDER BY role) AS roles, max(refused_at) AS "lastAt"
    FROM agent_egress_refusals WHERE project_id = ${projectId} AND refused_at > now() - make_interval(days => ${days})
    GROUP BY name ORDER BY runs DESC, name LIMIT 500`) as RefusedName[];
  return json({ refused: rows.filter((r) => !egressAllows(reach, r.name)).slice(0, 200) });
}

async function settingsLayers(ctx: RequestContext, projectId: string): Promise<Layers> {
  const layers = await withOrg(ctx.principal.organizationId, (scope) => loadLayers(scope, projectId));
  if (!layers?.project) throw notFound(`project ${projectId} not found`);
  return layers;
}

/** One-click Allow: the names go on the end of the project's own list, each once. */
async function allowNames(ctx: RequestContext): Promise<Response> {
  const projectId = ctx.params.id!;
  await settingsLayers(ctx, projectId);
  await requireProjectEditor(ctx, projectId);
  const { names } = await parseBody(ctx.request, allowNamesSchema);
  await withOrg(ctx.principal.organizationId, async (scope) => {
    const layers = await loadLayers(scope, projectId, true);
    if (!layers?.project) throw notFound(`project ${projectId} not found`);
    const egress = [...new Set([...layers.project.agentEgress, ...names])];
    if (egress.length > 200) throw badRequest("a project lists at most 200 hosts");
    await scope.sql`UPDATE projects SET agent_egress = ${scope.sql.array(egress, "text")}::text[], updated_at = now() WHERE id = ${projectId}`;
    await recordSettings(scope, ctx, projectId, { network: { egress } });
  });
  return json(await settingsResponse(ctx, projectId));
}

export function registerSettingsRoutes(router: Router): void {
  router.get("/v1/projects/:id/network/refused", refusedNames);
  router.post("/v1/projects/:id/network/allow", allowNames);
  router.get("/v1/settings/organization", getOrganizationSettings);
  router.patch("/v1/settings/organization", patchOrganizationSettings);
  router.get("/v1/projects/:id/settings", getProjectSettings);
  router.patch("/v1/projects/:id/settings", patchProjectSettings);
  router.get("/v1/prompts/:role/history", promptHistory);
  // Before POST /v1/prompts/:role, which would read "versions" as a role.
  router.post("/v1/prompts/versions/:id/restore", restorePrompt);
  router.post("/v1/prompts/:role", savePrompt);
}
