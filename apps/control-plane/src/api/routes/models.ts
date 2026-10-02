/**
 * Models: the organization's model tiers, the models its LLM proxy lists,
 * and a test message to one.
 *
 * Tiers are the organization's (model_tiers, migration 066); anyone in it
 * reads them, only its admins change them. Every agent role names one in
 * its settings (settings.ts); a tier names the model dude requests from the
 * proxy. What the proxy serves for it is the proxy's business: dude neither
 * sees nor claims it.
 *
 * The proxy's key is the orchestrator's alone: the list of models it
 * serves (GET /internal/llm/models) and a test message
 * (POST /internal/llm/test) are asked of the orchestrator.
 */

import {
  EventTypes,
  modelTierInputSchema,
  newId,
  removeModelTierSchema,
  reorderModelTiersSchema,
  replaceTier,
  resolveEffort,
  resolveTier,
  testModelSchema,
  type AgentModels,
  type ModelTestResult,
  type ModelTier,
  type ModelTierInput,
  type ModelTiersResponse,
  type ModelTierUpgradeNote,
  type ModelTierUse,
  type ProxyModels,
} from "@dude/domain";
import { SQL } from "bun";
import { withOrg, type OrgScope } from "../../db/client.ts";
import { appendInScope } from "../../events/ledger.ts";
import { orchestrator } from "../../orchestrator/client.ts";
import { isOrgAdmin, requireOrgAdmin } from "../access.ts";
import { auditActor } from "../auth.ts";
import { badRequest, conflict, HttpError, json, notFound, parseBody } from "../http.ts";
import type { RequestContext, Router } from "../router.ts";
import { PROJECT_IMAGE_URL } from "./projects.ts";

type Json = Record<string, unknown>;

// The orchestrator bounds a test message at 30 s (llm.TestTimeout) and
// reports a slow proxy itself; the call waits that long and a little more.
const TEST_TIMEOUT_MS = 35_000;

/** The organization's tiers, in the order admins gave them. */
export async function listTiers(scope: OrgScope): Promise<ModelTier[]> {
  return (await scope.sql`SELECT model_tier(t) AS tier FROM model_tiers t ORDER BY t.position, lower(t.name)`)
    .map((r: { tier: ModelTier }) => r.tier);
}

/**
 * Every tier a layer's agent models name must be the organization's. The
 * rows are held FOR SHARE to the end of the caller's transaction: a
 * removal, which locks them FOR UPDATE, then either waits for the patch and
 * moves what it set, or has gone and the patch is refused. Every statement
 * that locks several tiers takes them in id order, so none can deadlock.
 */
export async function checkTiers(scope: OrgScope, models: Record<string, { tier?: string | null | undefined } | undefined>): Promise<void> {
  const ids = [...new Set(Object.values(models).flatMap((change) => (change?.tier ? [change.tier] : [])))];
  if (ids.length === 0) return;
  const found = (await scope.sql`
    SELECT id FROM model_tiers WHERE id = ANY(${scope.sql.array(ids, "text")}::text[]) ORDER BY id FOR SHARE`)
    .map((r: { id: string }) => r.id) as string[];
  const missing = ids.find((id) => !found.includes(id));
  if (missing) throw badRequest(`there is no model tier ${missing}`);
}

/**
 * Who names each tier, with the effort each runs at: the organization's
 * roles and each project's overrides. A fixer that names none of its own
 * runs on the implementer's (resolveTier), and is listed under the layer
 * whose implementer it takes.
 */
async function usage(scope: OrgScope, tiers: readonly ModelTier[]): Promise<Map<string, ModelTierUse[]>> {
  const [org] = (await scope.sql`SELECT default_agent_models AS models FROM organizations WHERE id = ${scope.organizationId}`) as Array<{ models: AgentModels }>;
  const projects = (await scope.sql`
    SELECT id, name, ${scope.sql.unsafe(PROJECT_IMAGE_URL)} AS "imageUrl", agent_models AS models
    FROM projects ORDER BY name`) as Array<{ id: string; name: string; imageUrl: string | null; models: AgentModels }>;
  const out = new Map<string, ModelTierUse[]>();
  const add = (id: unknown, use: ModelTierUse) => {
    if (typeof id !== "string") return;
    const uses = out.get(id);
    if (uses) uses.push(use);
    else out.set(id, [use]);
  };
  const roles = (models: AgentModels | undefined) => (models ?? {}) as Record<string, { tier?: string } | undefined>;
  const orgLayers = { organization: org?.models };
  for (const [role, config] of Object.entries(roles(org?.models))) {
    add(config?.tier, { kind: "organization", role, project: null, effort: resolveEffort(role, orgLayers) });
  }
  const orgFixer = resolveTier("fixer", orgLayers, tiers);
  if (orgFixer.from === "implementer") {
    add(orgFixer.tierId, { kind: "organization", role: "fixer", project: null, inherited: true, effort: resolveEffort("fixer", orgLayers) });
  }
  for (const p of projects) {
    const project = { id: p.id, name: p.name, imageUrl: p.imageUrl };
    const layers = { project: p.models, organization: org?.models };
    for (const [role, config] of Object.entries(roles(p.models))) {
      add(config?.tier, { kind: "project", role, project, effort: resolveEffort(role, layers) });
    }
    const fixer = resolveTier("fixer", layers, tiers);
    if (fixer.from === "implementer" && resolveTier("implementer", layers, tiers).from === "project") {
      add(fixer.tierId, { kind: "project", role: "fixer", project, inherited: true, effort: resolveEffort("fixer", layers) });
    }
  }
  return out;
}

async function upgradeNotes(scope: OrgScope): Promise<ModelTierUpgradeNote[]> {
  return (await scope.sql`
    SELECT n.id::int AS id, n.role, n.old_model AS "oldModel", n.tier_id AS "tierId",
      COALESCE(t.name, n.tier_name) AS "tierName", n.new_tier AS "newTier", n.model_changed AS "modelChanged",
      (SELECT json_build_object('id', id, 'name', name, 'imageUrl', ${scope.sql.unsafe(PROJECT_IMAGE_URL)})
        FROM projects WHERE projects.id = n.project_id) AS project
    FROM model_tier_upgrade_notes n
      LEFT JOIN model_tiers t ON t.id = n.tier_id
    WHERE n.dismissed_at IS NULL
    ORDER BY n.project_id NULLS FIRST, n.role`) as ModelTierUpgradeNote[];
}

async function tiersResponse(ctx: RequestContext): Promise<ModelTiersResponse> {
  const canEdit = await isOrgAdmin(ctx);
  return withOrg(ctx.principal.organizationId, async (scope) => {
    const tiers = await listTiers(scope);
    const uses = await usage(scope, tiers);
    return {
      tiers: tiers.map((t) => ({ ...t, usedBy: uses.get(t.id) ?? [] })),
      canEdit,
      upgrade: canEdit ? await upgradeNotes(scope) : [],
    };
  });
}

// ---------------------------------------------------------------------------
// Changing tiers
// ---------------------------------------------------------------------------

async function record(scope: OrgScope, ctx: RequestContext, changed: Json) {
  const actor = auditActor(ctx.principal);
  await appendInScope(scope, {
    eventType: EventTypes.SettingsUpdated,
    organizationId: ctx.principal.organizationId,
    projectId: null,
    actor: { type: actor.kind, id: actor.id },
    source: "control-plane",
    payload: { scope: "organization", changed: { modelTiers: changed } },
  });
}

/** A name taken, as the unique index refuses it (SQLSTATE 23505, which Bun puts in errno). */
function nameTaken(err: unknown, name: string): never {
  if (err instanceof SQL.PostgresError && err.errno === "23505" && err.constraint === "model_tiers_name_idx") {
    throw conflict(`there is already a tier named ${name}`);
  }
  throw err;
}

const tierInput = async (ctx: RequestContext): Promise<ModelTierInput> =>
  (await parseBody(ctx.request, modelTierInputSchema)) as ModelTierInput;

async function createTier(ctx: RequestContext): Promise<Response> {
  await requireOrgAdmin(ctx);
  const input = await tierInput(ctx);
  const id = newId("modelTier");
  await withOrg(ctx.principal.organizationId, async (scope) => {
    await scope.sql`
      INSERT INTO model_tiers (id, organization_id, name, description, model, position, updated_by)
      VALUES (${id}, ${scope.organizationId}, ${input.name}, ${input.description}, ${input.model},
              (SELECT COALESCE(max(position) + 1, 0) FROM model_tiers), ${ctx.principal.personId})`
      .catch((err: unknown) => nameTaken(err, input.name));
    await record(scope, ctx, { [id]: { added: input } });
  });
  return json(await tiersResponse(ctx), 201);
}

/** Editing a tier's model moves every agent on it from its next session; running ones keep theirs. */
async function updateTier(ctx: RequestContext): Promise<Response> {
  await requireOrgAdmin(ctx);
  const id = ctx.params.id!;
  const input = await tierInput(ctx);
  await withOrg(ctx.principal.organizationId, async (scope) => {
    const rows = await scope.sql`
      UPDATE model_tiers SET name = ${input.name}, description = ${input.description}, model = ${input.model},
        updated_at = now(), updated_by = ${ctx.principal.personId}
      WHERE id = ${id} RETURNING id`.catch((err: unknown) => nameTaken(err, input.name));
    if (rows.length === 0) throw notFound(`no model tier ${id}`);
    await record(scope, ctx, { [id]: { edited: input } });
  });
  return json(await tiersResponse(ctx));
}

async function reorderTiers(ctx: RequestContext): Promise<Response> {
  await requireOrgAdmin(ctx);
  const { ids } = await parseBody(ctx.request, reorderModelTiersSchema);
  await withOrg(ctx.principal.organizationId, async (scope) => {
    const have = (await scope.sql`SELECT id FROM model_tiers ORDER BY id FOR UPDATE`).map((r: { id: string }) => r.id) as string[];
    if (ids.length !== have.length || new Set(ids).size !== ids.length || !ids.every((id) => have.includes(id))) {
      throw badRequest("the order names every tier of the organization, once each");
    }
    await scope.sql`
      UPDATE model_tiers t SET position = o.n - 1
      FROM unnest(${scope.sql.array(ids, "text")}::text[]) WITH ORDINALITY AS o(id, n) WHERE t.id = o.id`;
    await record(scope, ctx, { order: ids });
  });
  return json(await tiersResponse(ctx));
}

/**
 * Remove a tier, moving everything that names it — the organization's
 * roles, every project's overrides — to a replacement tier in the same
 * transaction. Sessions already running on it keep their model; their
 * Runs say which tier they were on.
 */
async function removeTier(ctx: RequestContext): Promise<Response> {
  await requireOrgAdmin(ctx);
  const id = ctx.params.id!;
  const replacement = (await parseBody(ctx.request, removeModelTierSchema)).replacement ?? null;
  await withOrg(ctx.principal.organizationId, async (scope) => {
    // The tier and its replacement, in id order, before the organization and
    // project rows: the order a settings patch takes them in (checkTiers,
    // then loadLayers). A patch naming it either committed first, and is
    // moved below, or waits here and is then refused.
    const locked = (await scope.sql`
      SELECT id FROM model_tiers WHERE id IN (${id}, ${replacement ?? id}) ORDER BY id FOR UPDATE`)
      .map((r: { id: string }) => r.id) as string[];
    if (!locked.includes(id)) throw notFound(`no model tier ${id}`);
    const [count] = (await scope.sql`SELECT count(*)::int AS n FROM model_tiers`) as Array<{ n: number }>;
    if (count!.n <= 1) throw conflict("the last tier cannot be removed: every agent needs one");
    if (replacement === id) throw badRequest("a tier cannot replace itself");
    if (replacement !== null && !locked.includes(replacement)) throw badRequest(`there is no model tier ${replacement}`);

    // NO KEY UPDATE: still serialises with a settings patch's FOR UPDATE, but
    // lets a child insert's FK check (FOR KEY SHARE) through, so a
    // transaction that holds a project row and then adds an epic or a
    // prompt does not deadlock with this one.
    const [org] = (await scope.sql`SELECT default_agent_models AS models FROM organizations WHERE id = ${scope.organizationId} FOR NO KEY UPDATE`) as Array<{ models: AgentModels }>;
    const projects = (await scope.sql`
      SELECT id, agent_models AS models FROM projects
      WHERE EXISTS (SELECT 1 FROM jsonb_each(agent_models) r WHERE r.value->>'tier' = ${id})
      FOR UPDATE`) as Array<{ id: string; models: AgentModels }>;
    const orgNamesIt = replaceTier(org!.models, id, id) !== org!.models;
    if ((orgNamesIt || projects.length > 0) && replacement === null) {
      throw new HttpError(409, "this tier is in use: choose the tier its agents move to", "in_use");
    }
    if (replacement !== null) {
      if (orgNamesIt) {
        await scope.sql`UPDATE organizations SET default_agent_models = ${replaceTier(org!.models, id, replacement)}::jsonb, updated_at = now()
          WHERE id = ${scope.organizationId}`;
      }
      for (const p of projects) {
        await scope.sql`UPDATE projects SET agent_models = ${replaceTier(p.models, id, replacement)}::jsonb, updated_at = now() WHERE id = ${p.id}`;
      }
    }
    await scope.sql`DELETE FROM model_tiers WHERE id = ${id}`;
    await record(scope, ctx, { [id]: { removed: true, movedTo: replacement, projects: projects.map((p) => p.id) } });
  });
  return json(await tiersResponse(ctx));
}

async function dismissUpgrade(ctx: RequestContext): Promise<Response> {
  await requireOrgAdmin(ctx);
  await withOrg(ctx.principal.organizationId, (scope) =>
    scope.sql`UPDATE model_tier_upgrade_notes SET dismissed_at = now() WHERE dismissed_at IS NULL`);
  return json(await tiersResponse(ctx));
}

// ---------------------------------------------------------------------------
// The proxy
// ---------------------------------------------------------------------------

/**
 * The models the proxy lists, as the orchestrator read them. Never an
 * error: without the orchestrator or the proxy there are none, and
 * `problem` says why — a tier still takes any name.
 */
async function proxyModels(ctx: RequestContext): Promise<ProxyModels> {
  try {
    const res = await orchestrator(ctx.principal.organizationId, "GET", "/internal/llm/models");
    const body = (await res.json().catch(() => null)) as (ProxyModels & { error?: { message?: string } }) | null;
    if (!res.ok || !body) return { models: [], source: null, problem: body?.error?.message ?? `the orchestrator answered ${res.status}` };
    return { models: body.models ?? [], source: body.source ?? null, problem: body.problem ?? null };
  } catch (err) {
    return { models: [], source: null, problem: err instanceof Error ? err.message : String(err) };
  }
}

/**
 * One small request to the proxy for a model, for each distinct request the
 * tier's agents send (the orchestrator folds efforts that go out alike), or
 * once without an effort for a tier none use. A check, never a gate: saving
 * a tier does not depend on it.
 */
async function testModel(ctx: RequestContext): Promise<Response> {
  await requireOrgAdmin(ctx);
  const input = await parseBody(ctx.request, testModelSchema);
  const efforts = await withOrg(ctx.principal.organizationId, async (scope) => {
    if (!input.tierId) return [null];
    const tiers = await listTiers(scope);
    if (!tiers.some((t) => t.id === input.tierId)) throw notFound(`no model tier ${input.tierId}`);
    const uses = (await usage(scope, tiers)).get(input.tierId) ?? [];
    const distinct = [...new Set(uses.map((u) => u.effort))];
    return distinct.length ? distinct : [null];
  });
  const res = await orchestrator(ctx.principal.organizationId, "POST", "/internal/llm/test",
    JSON.stringify({ model: input.model, efforts }), undefined, TEST_TIMEOUT_MS);
  if (!res.ok) return res;
  return json((await res.json()) as { model: string; results: ModelTestResult[] });
}

export function registerModelRoutes(router: Router): void {
  router.get("/v1/models/tiers", async (ctx) => json(await tiersResponse(ctx)));
  router.post("/v1/models/tiers", createTier);
  // Before PUT /v1/models/tiers/:id, which would read "order" as an id.
  router.put("/v1/models/tiers/order", reorderTiers);
  router.put("/v1/models/tiers/:id", updateTier);
  router.delete("/v1/models/tiers/:id", removeTier);
  router.post("/v1/models/upgrade/dismiss", dismissUpgrade);
  router.get("/v1/models/proxy", async (ctx) => json(await proxyModels(ctx)));
  router.post("/v1/models/test", testModel);
}
