/**
 * Machines: the organization's machine sizes, and the lux pools they run in.
 *
 * Sizes are the organization's (machine_sizes, migration 064); anyone in
 * it reads them, only its admins change them. Every agent role names one
 * in its settings (settings.ts) and a project's branch previews in their
 * preview settings (servers.ts); what names none runs on the default.
 *
 * Pools are lux's: the orchestrator reads them with dude's lux key
 * (GET /internal/lux/pools) and this passes them on. A size names its pool
 * by lux's id; the name shown is lux's current one. On save a named pool
 * must be in lux's list (422 unknown_pool, or 503 when lux cannot be read
 * to check), and a size must fit one host of its pool: too big for a host
 * lux knows is refused; a host nobody knows (an older lux, a pool that
 * never had one, lux unreachable for the default pool) is allowed.
 */

import {
  EventTypes,
  fitProblem,
  machineFit,
  machineSizeInputSchema,
  newId,
  removeMachineSizeSchema,
  replaceMachineSize,
  replacePreviewMachineSize,
  resolveMachineSize,
  sizePool,
  type AgentModels,
  type MachinePools,
  type MachineSize,
  type MachineSizeInput,
  type MachineSizeUse,
  type MachineSizeWithUse,
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

/** The organization's sizes, default first then by size. */
export async function listSizes(scope: OrgScope): Promise<MachineSize[]> {
  return (await scope.sql`
    SELECT machine_size(s) AS size FROM machine_sizes s
    ORDER BY s.cpus, s.memory_mib, s.disk_gib, lower(s.name)`).map((r: { size: MachineSize }) => r.size);
}

/**
 * Refuse a size id the organization does not have (a settings patch naming
 * one). The row is held FOR SHARE to the end of the caller's transaction:
 * a removal, which locks it FOR UPDATE, then either waits for the patch and
 * moves what it set, or has gone and the patch is refused.
 */
export async function requireSize(scope: OrgScope, id: string): Promise<void> {
  if ((await scope.sql`SELECT 1 FROM machine_sizes WHERE id = ${id} FOR SHARE`).length === 0) {
    throw badRequest(`there is no machine size ${id}`);
  }
}

/**
 * Who names each size: the organization's roles, each project's overrides,
 * and each project's previews. A fixer that names none of its own runs on
 * the implementer's (resolveMachineSize), and is listed under the layer
 * whose implementer it takes.
 */
async function usage(scope: OrgScope, sizes: readonly MachineSize[]): Promise<Map<string, MachineSizeUse[]>> {
  const [org] = (await scope.sql`SELECT default_agent_models AS models FROM organizations WHERE id = ${scope.organizationId}`) as Array<{ models: AgentModels }>;
  const projects = (await scope.sql`
    SELECT id, name, ${scope.sql.unsafe(PROJECT_IMAGE_URL)} AS "imageUrl", agent_models AS models, preview_settings AS previews
    FROM projects ORDER BY name`) as Array<{ id: string; name: string; imageUrl: string | null; models: AgentModels; previews: Json }>;
  const out = new Map<string, MachineSizeUse[]>();
  const add = (id: unknown, use: MachineSizeUse) => {
    if (typeof id !== "string") return;
    const uses = out.get(id);
    if (uses) uses.push(use);
    else out.set(id, [use]);
  };
  const roles = (models: AgentModels | undefined) => (models ?? {}) as Record<string, { machineSize?: string } | undefined>;
  const orgModels = roles(org?.models);
  for (const [role, config] of Object.entries(orgModels)) add(config?.machineSize, { kind: "organization", role, project: null });
  const orgFixer = resolveMachineSize("fixer", { organization: org?.models }, sizes);
  if (orgFixer.from === "implementer") add(orgFixer.sizeId, { kind: "organization", role: "fixer", project: null, inherited: true });
  for (const p of projects) {
    const project = { id: p.id, name: p.name, imageUrl: p.imageUrl };
    const own = roles(p.models);
    for (const [role, config] of Object.entries(own)) add(config?.machineSize, { kind: "project", role, project });
    const layers = { project: p.models, organization: org?.models };
    const fixer = resolveMachineSize("fixer", layers, sizes);
    if (fixer.from === "implementer" && resolveMachineSize("implementer", layers, sizes).from === "project") {
      add(fixer.sizeId, { kind: "project", role: "fixer", project, inherited: true });
    }
    add(p.previews?.["machineSize"], { kind: "preview", role: null, project });
  }
  return out;
}

async function sizesResponse(ctx: RequestContext, read?: MachinePools): Promise<{ sizes: MachineSizeWithUse[]; canEdit: boolean }> {
  const luxPools = (read ?? (await pools(ctx))).pools;
  const sizes = await withOrg(ctx.principal.organizationId, async (scope) => {
    const sizes = await listSizes(scope);
    const uses = await usage(scope, sizes);
    return sizes.map((s) => ({
      ...s,
      poolName: s.poolId === null ? null : (sizePool(s.poolId, luxPools)?.name ?? null),
      usedBy: uses.get(s.id) ?? [],
    }));
  });
  return { sizes, canEdit: await isOrgAdmin(ctx) };
}

// ---------------------------------------------------------------------------
// Pools
// ---------------------------------------------------------------------------

/**
 * lux's pools, as the orchestrator read them. Never an error: without the
 * orchestrator or lux there are none, and `problem` says why — sizes still
 * work, their fit unknown.
 */
export async function pools(ctx: RequestContext): Promise<MachinePools> {
  const readAt = new Date().toISOString();
  try {
    const res = await orchestrator(ctx.principal.organizationId, "GET", "/internal/lux/pools");
    const body = (await res.json().catch(() => null)) as (MachinePools & { error?: { message?: string } }) | null;
    if (!res.ok || !body) return { pools: [], readAt, problem: body?.error?.message ?? `the orchestrator answered ${res.status}` };
    return { pools: body.pools ?? [], readAt: body.readAt ?? readAt, problem: body.problem ?? null };
  } catch (err) {
    return { pools: [], readAt, problem: err instanceof Error ? err.message : String(err) };
  }
}

/**
 * A size is checked against lux's pools as they are now: a pool lux does
 * not list for the organisation is refused, as is one lux cannot be read to
 * check; a size too big for one host of its pool is refused, naming what
 * does not fit. Returns the pools read, for the answer.
 */
async function checkPool(ctx: RequestContext, size: MachineSizeInput): Promise<MachinePools> {
  const read = await pools(ctx);
  const unread = read.problem !== null;
  if (size.poolId !== null && unread) {
    throw new HttpError(503, `can't check the pool with lux: ${read.problem}`, "unavailable");
  }
  const fit = machineFit(size, unread ? null : read.pools);
  if (fit.kind === "gone") {
    throw new HttpError(422, `lux has no pool with id ${fit.poolId} for this organisation`, "unknown_pool", { poolId: fit.poolId });
  }
  if (fit.kind === "too_big") {
    throw new HttpError(422, `No host in ‘${fit.pool.name}’ can hold this: ${fitProblem(fit.over)}`, "too_big", { over: fit.over, pool: fit.pool.name });
  }
  return read;
}

// ---------------------------------------------------------------------------
// Changing sizes
// ---------------------------------------------------------------------------

async function record(scope: OrgScope, ctx: RequestContext, changed: Json) {
  const actor = auditActor(ctx.principal);
  await appendInScope(scope, {
    eventType: EventTypes.SettingsUpdated,
    organizationId: ctx.principal.organizationId,
    projectId: null,
    actor: { type: actor.kind, id: actor.id },
    source: "control-plane",
    payload: { scope: "organization", changed: { machineSizes: changed } },
  });
}

/**
 * A name taken, as the unique index refuses it: a 409 a person can read.
 * Bun's driver puts Postgres's SQLSTATE (23505, unique_violation) in
 * `errno`; its `code` is Bun's own.
 */
function nameTaken(err: unknown, name: string): never {
  if (err instanceof SQL.PostgresError && err.errno === "23505" && err.constraint === "machine_sizes_name_idx") {
    throw conflict(`there is already a size named ${name}`);
  }
  throw err;
}

/** Make `id` the default, in one statement: the constraint is checked at its end. */
async function makeDefault(scope: OrgScope, id: string) {
  await scope.sql`UPDATE machine_sizes SET is_default = (id = ${id}) WHERE is_default OR id = ${id}`;
}

/** A size from a request body, its defaults filled in. */
const sizeInput = async (ctx: RequestContext): Promise<MachineSizeInput> =>
  (await parseBody(ctx.request, machineSizeInputSchema)) as MachineSizeInput;

async function createSize(ctx: RequestContext): Promise<Response> {
  await requireOrgAdmin(ctx);
  const input = await sizeInput(ctx);
  const read = await checkPool(ctx, input);
  const id = newId("machineSize");
  await withOrg(ctx.principal.organizationId, async (scope) => {
    await scope.sql`
      INSERT INTO machine_sizes (id, organization_id, name, cpus, memory_mib, disk_gib, pool_id, is_default, updated_by)
      VALUES (${id}, ${scope.organizationId}, ${input.name}, ${input.cpus}, ${input.memoryMiB}, ${input.diskGiB}, ${input.poolId},
              false, ${ctx.principal.personId})`.catch((err: unknown) => nameTaken(err, input.name));
    if (input.isDefault) await makeDefault(scope, id);
    await record(scope, ctx, { [id]: { added: input } });
  });
  return json(await sizesResponse(ctx, read), 201);
}

async function updateSize(ctx: RequestContext): Promise<Response> {
  await requireOrgAdmin(ctx);
  const id = ctx.params.id!;
  const input = await sizeInput(ctx);
  const read = await checkPool(ctx, input);
  await withOrg(ctx.principal.organizationId, async (scope) => {
    const rows = await scope.sql`
      UPDATE machine_sizes SET name = ${input.name}, cpus = ${input.cpus}, memory_mib = ${input.memoryMiB},
        disk_gib = ${input.diskGiB}, pool_id = ${input.poolId}, updated_at = now(), updated_by = ${ctx.principal.personId}
      WHERE id = ${id} RETURNING is_default AS "isDefault"`.catch((err: unknown) => nameTaken(err, input.name));
    if (rows.length === 0) throw notFound(`no machine size ${id}`);
    // The default is moved by making another the default, never by unticking it.
    if (input.isDefault && !rows[0].isDefault) await makeDefault(scope, id);
    await record(scope, ctx, { [id]: { edited: input } });
  });
  return json(await sizesResponse(ctx, read));
}

async function setDefault(ctx: RequestContext): Promise<Response> {
  await requireOrgAdmin(ctx);
  const id = ctx.params.id!;
  await withOrg(ctx.principal.organizationId, async (scope) => {
    await requireSizeFound(scope, id);
    await makeDefault(scope, id);
    await record(scope, ctx, { [id]: "default" });
  });
  return json(await sizesResponse(ctx));
}

async function requireSizeFound(scope: OrgScope, id: string): Promise<{ isDefault: boolean }> {
  const [row] = (await scope.sql`SELECT is_default AS "isDefault" FROM machine_sizes WHERE id = ${id} FOR UPDATE`) as Array<{ isDefault: boolean }>;
  if (!row) throw notFound(`no machine size ${id}`);
  return row;
}

/**
 * Remove a size, moving everything that names it — the organization's
 * roles, every project's overrides and previews — to a replacement, or to
 * no size (following the default), in the same transaction. Sessions
 * already running on it keep it; their Runs' machine says what it was.
 */
async function removeSize(ctx: RequestContext): Promise<Response> {
  await requireOrgAdmin(ctx);
  const id = ctx.params.id!;
  const replacement = (await parseBody(ctx.request, removeMachineSizeSchema)).replacement ?? null;
  await withOrg(ctx.principal.organizationId, async (scope) => {
    // Locked FOR UPDATE: a settings change naming it (requireSize, FOR
    // SHARE) either finished first, and is moved below, or waits and is
    // then refused.
    const size = await requireSizeFound(scope, id);
    if (size.isDefault) throw conflict("the default size cannot be removed: make another the default first");
    if (replacement === id) throw badRequest("a size cannot replace itself");
    if (replacement !== null) await requireSize(scope, replacement);

    const [org] = (await scope.sql`SELECT default_agent_models AS models FROM organizations WHERE id = ${scope.organizationId}`) as Array<{ models: AgentModels }>;
    const orgNext = replaceMachineSize(org!.models, id, replacement);
    if (orgNext !== org!.models) {
      await scope.sql`UPDATE organizations SET default_agent_models = ${orgNext}::jsonb, updated_at = now() WHERE id = ${scope.organizationId}`;
    }
    const projects = (await scope.sql`
      SELECT id, agent_models AS models, preview_settings AS previews FROM projects
      WHERE preview_settings->>'machineSize' = ${id}
         OR EXISTS (SELECT 1 FROM jsonb_each(agent_models) r WHERE r.value->>'machineSize' = ${id})
      FOR UPDATE`) as Array<{ id: string; models: AgentModels; previews: Json }>;
    for (const p of projects) {
      await scope.sql`
        UPDATE projects SET agent_models = ${replaceMachineSize(p.models, id, replacement)}::jsonb,
          preview_settings = ${replacePreviewMachineSize(p.previews, id, replacement)}::jsonb, updated_at = now()
        WHERE id = ${p.id}`;
    }
    await scope.sql`DELETE FROM machine_sizes WHERE id = ${id}`;
    await record(scope, ctx, { [id]: { removed: true, movedTo: replacement, projects: projects.map((p) => p.id) } });
  });
  return json(await sizesResponse(ctx));
}

export function registerMachineRoutes(router: Router): void {
  router.get("/v1/machines/sizes", async (ctx) => json(await sizesResponse(ctx)));
  router.post("/v1/machines/sizes", createSize);
  router.put("/v1/machines/sizes/:id", updateSize);
  router.post("/v1/machines/sizes/:id/default", setDefault);
  router.delete("/v1/machines/sizes/:id", removeSize);
  router.get("/v1/machines/pools", async (ctx) => json(await pools(ctx)));
}
