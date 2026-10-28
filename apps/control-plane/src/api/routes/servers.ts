/**
 * Servers: a project's server recipes and branch-preview settings (kept
 * here), and a task's or Run's servers and branch preview (the
 * orchestrator's to carry out, through lux).
 *
 * Who may do what: anyone in the organization reads; a project's editors
 * (api/access.ts — its maintainers, for now the organization's admins)
 * change its recipes, preview settings and preview secrets; any member starts, stops, adds
 * and removes a Run's servers, a task's preview and a project's branch
 * previews. Opening a terminal is lux's to allow, not dude's.
 */

import { z } from "zod";
import { auditActor } from "../auth.ts";
import {
  DEFAULT_SECRET_RECIPIENTS,
  EventTypes,
  addSecretSchema,
  changeSecretRecipientsSchema,
  addServerSchema,
  previewSettingsSchema,
  recipeInputSchema,
  recipeSecretClash,
  recipeOrgSecretClash,
  replaceSecretSchema,
  secretHint,
  secretNameProblem,
  serverNameSchema,
  type ProjectSecret,
  type PreviewSettings,
  type Recipe,
  type RecipeEnvNames,
} from "@dude/domain";
import { withOrg, type OrgScope } from "../../db/client.ts";
import { appendInScope } from "../../events/ledger.ts";
import { requireProjectEditor } from "../access.ts";
import { badRequest, conflict, intParam, json, noContent, notFound, parseBody } from "../http.ts";
import { orchestrator } from "../../orchestrator/client.ts";
import { requireSize } from "./machines.ts";
import { requireImage } from "./images.ts";
import type { RequestContext, Router } from "../router.ts";

/** A project's recipes and preview settings, as the API shows them (server_recipe, preview_settings: migration 055). */
async function projectServers(scope: OrgScope, projectId: string): Promise<{ servers: Recipe[]; previews: PreviewSettings }> {
  const [row] = (await scope.sql`
    SELECT preview_settings(p) AS previews,
      (SELECT COALESCE(json_agg(server_recipe(s) ORDER BY s.name), '[]') FROM project_servers s WHERE s.project_id = p.id) AS servers
    FROM projects p WHERE p.id = ${projectId}`) as Array<{ previews: PreviewSettings; servers: Recipe[] }>;
  if (!row) throw notFound(`project ${projectId} not found`);
  return row;
}

async function listProjectServers(ctx: RequestContext): Promise<Response> {
  return json(await withOrg(ctx.principal.organizationId, (scope) => projectServers(scope, ctx.params.id!)));
}

/** Who changed a project's servers, recorded like its other settings. */
async function recordChange(scope: OrgScope, ctx: RequestContext, projectId: string, changed: Record<string, unknown>) {
  const actor = auditActor(ctx.principal);
  await appendInScope(scope, {
    eventType: EventTypes.SettingsUpdated,
    organizationId: ctx.principal.organizationId,
    projectId,
    actor: { type: actor.kind, id: actor.id },
    source: "control-plane",
    payload: { scope: "project", projectId, changed },
  });
}

/**
 * Create or replace a recipe. A body naming another name renames it: the
 * old one goes, and the new name must be free.
 */
async function putProjectServer(ctx: RequestContext): Promise<Response> {
  const projectId = ctx.params.id!;
  await requireProjectEditor(ctx, projectId);
  const current = ctx.params.name!;
  const input = await parseBody(ctx.request, recipeInputSchema);
  const out = await withOrg(ctx.principal.organizationId, async (scope) => {
    await lockProject(scope, projectId); // 404 for another organization's
    const recipe = { name: input.name, env: input.env ?? [] };
    const clash = recipeSecretClash(recipe, await secretNames(scope, projectId)) ?? await recipeOrgClash(scope, recipe, projectId);
    if (clash) throw conflict(clash);
    if (input.name !== current) {
      const taken = await scope.sql`SELECT 1 FROM project_servers WHERE project_id = ${projectId} AND name = ${input.name}`;
      if (taken.length > 0) throw conflict(`the project already has a server named ${input.name}`);
      await scope.sql`DELETE FROM project_servers WHERE project_id = ${projectId} AND name = ${current}`;
    }
    const setup = input.setup?.trim() ? input.setup : null;
    await scope.sql`
      INSERT INTO project_servers (project_id, organization_id, name, port, command, workdir, setup, env,
                                   autostart_in_previews, updated_at, updated_by)
      VALUES (${projectId}, ${scope.organizationId}, ${input.name}, ${input.port}, ${input.command}, ${input.workdir},
              ${setup}, ${input.env}::jsonb, ${input.autostartInPreviews}, now(), ${ctx.principal.personId})
      ON CONFLICT (project_id, name) DO UPDATE SET port = EXCLUDED.port, command = EXCLUDED.command,
        workdir = EXCLUDED.workdir, setup = EXCLUDED.setup, env = EXCLUDED.env,
        autostart_in_previews = EXCLUDED.autostart_in_previews, updated_at = now(), updated_by = EXCLUDED.updated_by`;
    await recordChange(scope, ctx, projectId, {
      servers: { [input.name]: "saved", ...(input.name !== current ? { [current]: "renamed" } : {}) },
    });
    const { servers } = await projectServers(scope, projectId);
    return servers.find((s) => s.name === input.name)!;
  });
  return json(out);
}

async function deleteProjectServer(ctx: RequestContext): Promise<Response> {
  const projectId = ctx.params.id!;
  await requireProjectEditor(ctx, projectId);
  const name = ctx.params.name!;
  await withOrg(ctx.principal.organizationId, async (scope) => {
    const gone = await scope.sql`DELETE FROM project_servers WHERE project_id = ${projectId} AND name = ${name} RETURNING name`;
    if (gone.length === 0) throw notFound(`project ${projectId} has no server ${name}`);
    await recordChange(scope, ctx, projectId, { servers: { [name]: "removed" } });
  });
  return noContent();
}

/**
 * Replace how the project's branch previews run; unset fields take their
 * defaults. A typed image from before the library is kept as it is, or
 * cleared (null); a new one is refused — previews pick from the library.
 */
async function putPreviewSettings(ctx: RequestContext): Promise<Response> {
  const projectId = ctx.params.id!;
  await requireProjectEditor(ctx, projectId);
  const input = await parseBody(ctx.request, previewSettingsSchema);
  const previews = await withOrg(ctx.principal.organizationId, async (scope) => {
    const [current] = (await scope.sql`
      SELECT preview_settings->>'image' AS image, preview_image_id AS "imageId" FROM projects WHERE id = ${projectId} FOR UPDATE`) as Array<{
      image: string | null;
      imageId: string | null;
    }>;
    if (!current) throw notFound(`project ${projectId} not found`);
    if (input.image !== null && input.image !== current.image) {
      throw badRequest("a preview's image is picked from the image library: send imageId");
    }
    const stored = {
      ...(input.image ? { image: input.image } : {}),
      egress: [...new Set(input.egress)],
      idleTimeoutMinutes: input.idleTimeoutMinutes,
      firstWakeTimeoutMinutes: input.firstWakeTimeoutMinutes,
      wakeTimeoutMinutes: input.wakeTimeoutMinutes,
      ...(input.machineSize ? { machineSize: input.machineSize } : {}),
    };
    if (input.machineSize) await requireSize(scope, input.machineSize);
    if (input.imageId) await requireImage(scope, input.imageId, current.imageId);
    await scope.sql`
      UPDATE projects SET preview_settings = ${stored}::jsonb, preview_image_id = ${input.imageId}, updated_at = now()
      WHERE id = ${projectId}`;
    await recordChange(scope, ctx, projectId, { previews: { ...stored, imageId: input.imageId } });
    return (await projectServers(scope, projectId)).previews;
  });
  return json(previews);
}

// ---------------------------------------------------------------------------
// A project's preview secrets: write-only. No query here selects a value;
// every answer carries the hint stored beside it.
// ---------------------------------------------------------------------------

/**
 * Holds the project's row for the transaction (404 for another
 * organization's): a secret's name and a recipe's env are checked against
 * each other, so the two writes take turns.
 */
async function lockProject(scope: OrgScope, projectId: string): Promise<void> {
  const [row] = await scope.sql`SELECT 1 FROM projects WHERE id = ${projectId} FOR UPDATE`;
  if (!row) throw notFound(`project ${projectId} not found`);
}

async function secretNames(scope: OrgScope, projectId: string): Promise<string[]> {
  const rows = (await scope.sql`SELECT name FROM project_secrets WHERE project_id = ${projectId}`) as Array<{ name: string }>;
  return rows.map((r) => r.name);
}

/**
 * Why a recipe would override one of the organisation's secrets that reach
 * this project's previews, or null. Shares the organisation's row lock with
 * its secret writes (routes/orgSecrets.ts), so the two checks take turns.
 */
async function recipeOrgClash(scope: OrgScope, recipe: RecipeEnvNames, projectId: string): Promise<string | null> {
  const [org] = (await scope.sql`SELECT name FROM organizations WHERE id = ${scope.organizationId} FOR SHARE`) as Array<{ name: string }>;
  const rows = (await scope.sql`
    SELECT name FROM secrets_in_force(${scope.organizationId}, ${projectId})
    WHERE source = 'organization' AND 'previews' = ANY (recipients)`) as Array<{ name: string }>;
  return recipeOrgSecretClash(recipe, org?.name ?? scope.organizationId, rows.map((r) => r.name));
}

async function listSecrets(scope: OrgScope, projectId: string, name?: string): Promise<ProjectSecret[]> {
  return (await scope.sql`
    SELECT s.name, s.hint, s.recipients, s.updated_at AS "updatedAt",
      (SELECT json_build_object('id', p.id, 'name', p.name) FROM people p WHERE p.id = s.updated_by) AS "updatedBy"
    FROM project_secrets s
    WHERE s.project_id = ${projectId} AND (${name ?? null}::text IS NULL OR s.name = ${name ?? null})
    ORDER BY s.name`) as ProjectSecret[];
}

async function getProjectSecrets(ctx: RequestContext): Promise<Response> {
  const projectId = ctx.params.id!;
  const secrets = await withOrg(ctx.principal.organizationId, async (scope) => {
    const [project] = await scope.sql`SELECT 1 FROM projects WHERE id = ${projectId}`;
    if (!project) throw notFound(`project ${projectId} not found`);
    return listSecrets(scope, projectId);
  });
  return json({ secrets });
}

async function addProjectSecret(ctx: RequestContext): Promise<Response> {
  const projectId = ctx.params.id!;
  await requireProjectEditor(ctx, projectId);
  const input = await parseBody(ctx.request, addSecretSchema);
  const secret = await withOrg(ctx.principal.organizationId, async (scope) => {
    await lockProject(scope, projectId);
    const { servers } = await projectServers(scope, projectId);
    const problem = secretNameProblem(input.name, { secrets: await secretNames(scope, projectId), recipes: servers });
    if (problem) throw problem.kind === "conflict" ? conflict(problem.message) : badRequest(problem.message);
    await scope.sql`
      INSERT INTO project_secrets (project_id, organization_id, name, value, hint, updated_by, recipients)
      VALUES (${projectId}, ${scope.organizationId}, ${input.name}, ${input.value}, ${secretHint(input.value)}, ${ctx.principal.personId}, ${scope.sql.array(input.recipients ?? [...DEFAULT_SECRET_RECIPIENTS], "text")}::text[])`;
    await recordChange(scope, ctx, projectId, { secrets: { [input.name]: "added" } });
    return (await listSecrets(scope, projectId, input.name))[0]!;
  });
  return json(secret, 201);
}

async function replaceProjectSecret(ctx: RequestContext): Promise<Response> {
  const projectId = ctx.params.id!;
  await requireProjectEditor(ctx, projectId);
  const name = ctx.params.name!;
  const input = await parseBody(ctx.request, replaceSecretSchema);
  const secret = await withOrg(ctx.principal.organizationId, async (scope) => {
    const changed = await scope.sql`
      UPDATE project_secrets SET value = ${input.value}, hint = ${secretHint(input.value)}, updated_at = now(),
        updated_by = ${ctx.principal.personId}
      WHERE project_id = ${projectId} AND name = ${name} RETURNING name`;
    if (changed.length === 0) throw notFound(`project ${projectId} has no secret ${name}`);
    await recordChange(scope, ctx, projectId, { secrets: { [name]: "replaced" } });
    return (await listSecrets(scope, projectId, name))[0]!;
  });
  return json(secret);
}

async function changeProjectSecretRecipients(ctx: RequestContext): Promise<Response> {
  const projectId = ctx.params.id!;
  await requireProjectEditor(ctx, projectId);
  const name = ctx.params.name!;
  const input = await parseBody(ctx.request, changeSecretRecipientsSchema);
  const secret = await withOrg(ctx.principal.organizationId, async (scope) => {
    const changed = await scope.sql`
      UPDATE project_secrets SET recipients = ${scope.sql.array(input.recipients, "text")}::text[], updated_at = now(),
        updated_by = ${ctx.principal.personId}
      WHERE project_id = ${projectId} AND name = ${name} RETURNING name`;
    if (changed.length === 0) throw notFound(`project ${projectId} has no secret ${name}`);
    await recordChange(scope, ctx, projectId, { secrets: { [name]: { recipients: input.recipients } } });
    return (await listSecrets(scope, projectId, name))[0]!;
  });
  return json(secret);
}

async function removeProjectSecret(ctx: RequestContext): Promise<Response> {
  const projectId = ctx.params.id!;
  await requireProjectEditor(ctx, projectId);
  const name = ctx.params.name!;
  await withOrg(ctx.principal.organizationId, async (scope) => {
    const gone = await scope.sql`DELETE FROM project_secrets WHERE project_id = ${projectId} AND name = ${name} RETURNING name`;
    if (gone.length === 0) throw notFound(`project ${projectId} has no secret ${name}`);
    await recordChange(scope, ctx, projectId, { secrets: { [name]: "removed" } });
  });
  return noContent();
}

// ---------------------------------------------------------------------------
// A task's and a Run's servers: the orchestrator's, through lux
// ---------------------------------------------------------------------------

/** Forward to the orchestrator as this person, passing its answer back. */
function forward(ctx: RequestContext, method: string, path: string, body?: string): Promise<Response> {
  return orchestrator(ctx.principal.organizationId, method, path, body, ctx.principal);
}

const runPath = (ctx: RequestContext) => `/internal/runs/${encodeURIComponent(ctx.params.runId!)}/servers`;
const taskPath = (ctx: RequestContext, rest: string) => `/internal/tasks/${encodeURIComponent(ctx.params.id!)}/${rest}`;

/** A server's name from the path, checked as lux would, so nothing odd is passed on. */
function serverName(ctx: RequestContext): string {
  const parsed = serverNameSchema.safeParse(ctx.params.name);
  if (!parsed.success) throw notFound(`no server ${ctx.params.name}`);
  return encodeURIComponent(parsed.data);
}

async function addRunServer(ctx: RequestContext): Promise<Response> {
  const input = await parseBody(ctx.request, addServerSchema);
  return forward(ctx, "POST", runPath(ctx), JSON.stringify(input));
}

function serverAction(action: "start" | "stop" | "restart") {
  return (ctx: RequestContext) => forward(ctx, "POST", `${runPath(ctx)}/${serverName(ctx)}/${action}`);
}

function allServers(action: "start-all" | "stop-all") {
  return (ctx: RequestContext) => forward(ctx, "POST", `${runPath(ctx)}/${action}`);
}

async function serverLog(ctx: RequestContext): Promise<Response> {
  const tail = intParam(ctx.url, "tail", { min: 1, max: 10_000 }) ?? 200;
  return forward(ctx, "GET", `${runPath(ctx)}/${serverName(ctx)}/log?tail=${tail}`);
}

function removeRunServer(ctx: RequestContext): Promise<Response> {
  return forward(ctx, "DELETE", `${runPath(ctx)}/${serverName(ctx)}`);
}

async function startPreview(ctx: RequestContext): Promise<Response> {
  const body = (await ctx.request.text()).trim();
  if (body && body !== "{}") throw badRequest("a preview takes no options");
  return forward(ctx, "POST", taskPath(ctx, "preview"));
}

/** A branch preview's start: the branch of the project it previews (the orchestrator checks it on GitHub). */
export const branchPreviewInput = z.object({ branch: z.string().trim().min(1).max(255) }).strict();

const projectPreviewsPath = (ctx: RequestContext) => `/internal/projects/${encodeURIComponent(ctx.params.id!)}/branch-previews`;
const branchPreviewPath = (ctx: RequestContext) => `/internal/branch-previews/${encodeURIComponent(ctx.params.id!)}`;

/** Starts a branch preview, or answers the project's live one of that branch (200). Any member, as a task's preview. */
async function startBranchPreview(ctx: RequestContext): Promise<Response> {
  const input = await parseBody(ctx.request, branchPreviewInput);
  return forward(ctx, "POST", projectPreviewsPath(ctx), JSON.stringify(input));
}

/** `origin` from the query: an https origin exactly as a browser writes one (no path, no trailing slash), or 400. */
function previewOrigin(ctx: RequestContext): string {
  const given = ctx.url.searchParams.get("origin") ?? "";
  let origin: string | null = null;
  try {
    const u = new URL(given);
    origin = u.protocol === "https:" ? u.origin : null;
  } catch {
    origin = null;
  }
  if (given.length > 300 || origin === null || origin !== given) throw badRequest("origin must be an https origin, such as https://web-shop-1.preview.example.test");
  return origin;
}

/**
 * Which task's live preview is served at an origin: the preview page's
 * Connect to dude asks, through the dude tab it opened, so that tab can go to
 * the task and adopt the page. Looked up in the caller's organisations only,
 * each under its own row-level security as /v1/locate does; a server is
 * found by the URL lux gave it, never by parsing its hostname.
 */
async function lookupPreviewServer(ctx: RequestContext): Promise<Response> {
  const origin = previewOrigin(ctx);
  const memberships = ctx.principal.access?.memberships ??
    [{ organization_id: ctx.principal.organizationId, person_id: ctx.principal.personId }];
  for (const m of memberships) {
    const found = await withOrg(m.organization_id, async ({ sql }) => {
      const rows = (await sql`
        SELECT o.slug AS "organizationSlug", t.id AS "taskId", p.key_prefix || '-' || t.number AS "taskKey", s.name AS server, s.url
        FROM preview_servers s
        JOIN runs r ON r.id = s.run_id
        JOIN tasks t ON t.id = r.task_id
        JOIN projects p ON p.id = t.project_id
        JOIN organizations o ON o.id = s.organization_id
        WHERE s.deleted_at IS NULL AND r.kind = 'preview' AND (s.url = ${origin} OR starts_with(s.url, ${`${origin}/`}))
        ORDER BY s.created_at DESC`) as Array<{ organizationSlug: string; taskId: string; taskKey: string; server: string; url: string }>;
      return rows.find((r) => URL.canParse(r.url) && new URL(r.url).origin === origin) ?? null;
    });
    if (found) {
      const { url: _url, ...out } = found;
      return json(out);
    }
  }
  throw notFound(`no task of yours has a preview at ${origin}`);
}

export function registerServerRoutes(router: Router): void {
  router.get("/v1/projects/:id/servers", listProjectServers);
  router.put("/v1/projects/:id/servers/:name", putProjectServer);
  router.delete("/v1/projects/:id/servers/:name", deleteProjectServer);
  router.put("/v1/projects/:id/preview-settings", putPreviewSettings);
  router.get("/v1/projects/:id/secrets", getProjectSecrets);
  router.post("/v1/projects/:id/secrets", addProjectSecret);
  router.put("/v1/projects/:id/secrets/:name/recipients", changeProjectSecretRecipients);
  router.put("/v1/projects/:id/secrets/:name", replaceProjectSecret);
  router.delete("/v1/projects/:id/secrets/:name", removeProjectSecret);

  router.get("/v1/tasks/:id/servers", (ctx) => forward(ctx, "GET", taskPath(ctx, "servers")));
  router.post("/v1/tasks/:id/preview", startPreview);
  router.delete("/v1/tasks/:id/preview", (ctx) => forward(ctx, "DELETE", taskPath(ctx, "preview")));
  router.get("/v1/preview-servers/lookup", lookupPreviewServer);

  // A project's branch previews: a branch's, with no task. Each is addressed by its id, never by its branch.
  router.post("/v1/projects/:id/branch-previews", startBranchPreview);
  router.get("/v1/projects/:id/branch-previews", (ctx) => forward(ctx, "GET", projectPreviewsPath(ctx)));
  router.get("/v1/branch-previews/:id", (ctx) => forward(ctx, "GET", branchPreviewPath(ctx)));
  router.delete("/v1/branch-previews/:id", (ctx) => forward(ctx, "DELETE", branchPreviewPath(ctx)));

  router.get("/v1/runs/:runId/servers", (ctx) => forward(ctx, "GET", runPath(ctx)));
  router.post("/v1/runs/:runId/servers", addRunServer);
  router.post("/v1/runs/:runId/servers/start-all", allServers("start-all"));
  router.post("/v1/runs/:runId/servers/stop-all", allServers("stop-all"));
  router.post("/v1/runs/:runId/servers/:name/start", serverAction("start"));
  router.post("/v1/runs/:runId/servers/:name/stop", serverAction("stop"));
  router.post("/v1/runs/:runId/servers/:name/restart", serverAction("restart"));
  router.delete("/v1/runs/:runId/servers/:name", removeRunServer);
  router.get("/v1/runs/:runId/servers/:name/log", serverLog);
}
