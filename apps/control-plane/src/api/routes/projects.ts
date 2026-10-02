/**
 * Project and repository routes.
 *
 * Projects own the per-role agent configuration: which model tier runs as
 * orchestrator, implementer, reviewer and so on.
 */

import { z } from "zod";
import { agentModelsSchema, deliveryPolicySchema, newId, EventTypes } from "@dude/domain";
import { withOrg, withoutTenant } from "../../db/client.ts";
import { requireOrgAdmin, requireProjectEditor } from "../access.ts";
import { appendInScope } from "../../events/ledger.ts";
import { conflict, json, notFound, parseBody } from "../http.ts";
import { auditActor } from "../auth.ts";
import type { PublicContext, RequestContext, Router } from "../router.ts";
import { replaceImage, serveImage } from "../faces.ts";
import { deleteObject } from "../../storage.ts";
import { repositoryFields } from "./structure.ts";
import { registerRepositoryWebhook } from "./pullRequests.ts";
import { orchestrator } from "../../orchestrator/client.ts";
import { checkTiers } from "./models.ts";
import { requireImage } from "./images.ts";
import type { OrgScope } from "../../db/client.ts";

/**
 * Every image a project's role settings name must be the organization's.
 * Archived ones pass: agentModels is replaced whole, and a role may still
 * name one it named before it was archived.
 */
async function checkRoleImages(scope: OrgScope, models: Record<string, { image?: string | undefined } | undefined> | undefined) {
  for (const role of Object.values(models ?? {})) if (role?.image) await requireImage(scope, role.image, role.image);
}

const slugPattern = /^[a-z0-9][a-z0-9-]*$/;

const repositoryInput = z.object({
  name: repositoryFields.name,
  url: repositoryFields.url,
  defaultBranch: z.string().min(1).default("main"),
  trust: z.enum(["trusted_internal", "untrusted_external"]).default("trusted_internal"),
});

const createProjectInput = z.object({
  name: z.string().min(1).max(200),
  slug: z.string().min(1).max(100).regex(slugPattern, "slug must be lowercase alphanumeric with dashes"),
  description: z.string().max(2000).default(""),
  agentModels: agentModelsSchema,
  // A typed image is from before the library: it can only be cleared now.
  runtimeImage: z.null({ invalid_type_error: "a project's image is picked from the image library: send runtimeImageId" }).default(null),
  runtimeImageId: z.string().min(1).max(100).nullable().default(null),
  deliveryPolicy: deliveryPolicySchema.default({}),
  repositories: z.array(repositoryInput).default([]),
});

const updateProjectInput = createProjectInput
  .omit({ slug: true, repositories: true })
  .partial();

interface ProjectRow {
  id: string;
  organizationId: string;
  name: string;
  slug: string;
  description: string;
  agentModels: Record<string, unknown>;
  runtimeImage: string | null;
  runtimeImageId: string | null;
  deliveryPolicy: Record<string, unknown>;
  createdAt: string;
  imageUrl: string | null;
}

/** A project's image as its URL (served under its token), for the `projects` row in scope. */
export const PROJECT_IMAGE_URL = `CASE WHEN image_key IS NOT NULL THEN '/v1/projects/' || id || '/image?t=' || image_token END`;

const PROJECT_SELECT = `
  id, organization_id AS "organizationId", name, slug, description,
  agent_models AS "agentModels", runtime_image AS "runtimeImage", runtime_image_id AS "runtimeImageId",
  delivery_policy AS "deliveryPolicy", created_at AS "createdAt",
  ${PROJECT_IMAGE_URL} AS "imageUrl"`;

/** The start of a project's task keys (TK-12): the slug's first letters. */
export function keyPrefix(slug: string): string {
  return slug.replace(/[^a-zA-Z]/g, "").slice(0, 4).toUpperCase() || "WI";
}

async function createProject(ctx: RequestContext): Promise<Response> {
  const input = await parseBody(ctx.request, createProjectInput);
  const { organizationId } = ctx.principal;

  const result = await withOrg(organizationId, async (scope) => {
    const existing = await scope.sql`
      SELECT id FROM projects WHERE slug = ${input.slug} LIMIT 1`;
    if (existing.length > 0) return { conflict: true as const };
    await checkTiers(scope, input.agentModels ?? {});

    const projectId = newId("project");
    if (input.runtimeImageId) await requireImage(scope, input.runtimeImageId);
    await checkRoleImages(scope, input.agentModels);
    const rows = (await scope.sql`
      INSERT INTO projects (id, organization_id, name, slug, key_prefix, description, agent_models, runtime_image_id, delivery_policy)
      VALUES (${projectId}, ${organizationId}, ${input.name}, ${input.slug}, ${keyPrefix(input.slug)}, ${input.description},
              ${input.agentModels ?? {}}::jsonb, ${input.runtimeImageId}, ${input.deliveryPolicy ?? {}}::jsonb)
      RETURNING ${scope.sql.unsafe(PROJECT_SELECT)}`) as ProjectRow[];

    const repositories = [];
    for (const repo of input.repositories ?? []) {
      const repoRows = (await scope.sql`
        INSERT INTO repositories (id, organization_id, project_id, name, url, default_branch, trust)
        VALUES (${newId("repository")}, ${organizationId}, ${projectId}, ${repo.name}, ${repo.url},
                ${repo.defaultBranch}, ${repo.trust})
        RETURNING id, name, url, default_branch AS "defaultBranch", trust`) as Array<
        Record<string, unknown>
      >;
      if (repoRows[0]) repositories.push(repoRows[0]);
    }

    const event = await appendInScope(scope, {
      eventType: EventTypes.ProjectCreated,
      organizationId,
      projectId,
      actor: { type: auditActor(ctx.principal).kind, id: auditActor(ctx.principal).id },
      source: "control-plane",
      payload: { name: input.name, slug: input.slug },
    });

    return { project: { ...rows[0]!, repositories }, event };
  });

  if ("conflict" in result) throw conflict(`a project with slug "${input.slug}" already exists`);
  for (const repo of result.project.repositories) await registerRepositoryWebhook(ctx, repo.id as string);
  return json(result.project, 201);
}

async function listProjects(ctx: RequestContext): Promise<Response> {
  const projects = await withOrg(ctx.principal.organizationId, async (scope) => {
    return (await scope.sql`
      SELECT ${scope.sql.unsafe(PROJECT_SELECT)} FROM projects ORDER BY created_at DESC`) as ProjectRow[];
  });
  return json({ projects });
}

async function getProject(ctx: RequestContext): Promise<Response> {
  const projectId = ctx.params.id!;
  const project = await withOrg(ctx.principal.organizationId, async (scope) => {
    const rows = (await scope.sql`
      SELECT ${scope.sql.unsafe(PROJECT_SELECT)} FROM projects WHERE id = ${projectId}`) as ProjectRow[];
    if (!rows[0]) return null;

    const repositories = await scope.sql`
      SELECT id, name, url, default_branch AS "defaultBranch", trust
      FROM repositories WHERE project_id = ${projectId} ORDER BY name`;
    return { ...rows[0], repositories };
  });

  if (!project) throw notFound(`project ${projectId} not found`);
  return json(project);
}

/**
 * Patch a project. Agent model config and delivery policy are replaced
 * wholesale rather than merged, so removing a setting is expressible.
 */
async function updateProject(ctx: RequestContext): Promise<Response> {
  const input = await parseBody(ctx.request, updateProjectInput);
  const projectId = ctx.params.id!;

  const project = await withOrg(ctx.principal.organizationId, async (scope) => {
    await checkTiers(scope, input.agentModels ?? {});
    if (input.runtimeImageId) {
      const [current] = (await scope.sql`SELECT runtime_image_id AS id FROM projects WHERE id = ${projectId}`) as Array<{ id: string | null }>;
      await requireImage(scope, input.runtimeImageId, current?.id);
    }
    await checkRoleImages(scope, input.agentModels);
    const rows = (await scope.sql`
      UPDATE projects SET
        name          = COALESCE(${input.name ?? null}, name),
        description   = COALESCE(${input.description ?? null}, description),
        agent_models  = COALESCE(${input.agentModels ?? null}::jsonb,
                                 agent_models),
        delivery_policy = COALESCE(${input.deliveryPolicy ?? null}::jsonb, delivery_policy),
        runtime_image = CASE WHEN ${input.runtimeImage !== undefined} THEN NULL ELSE runtime_image END,
        runtime_image_id = CASE WHEN ${input.runtimeImageId !== undefined} THEN ${input.runtimeImageId ?? null}
                                ELSE runtime_image_id END
      WHERE id = ${projectId}
      RETURNING ${scope.sql.unsafe(PROJECT_SELECT)}`) as ProjectRow[];
    return rows[0] ?? null;
  });

  if (!project) throw notFound(`project ${projectId} not found`);
  return json(project);
}

/** The factory's delivery defaults, from the orchestrator that applies them. */
async function deliveryDefaults(ctx: RequestContext): Promise<Response> {
  return orchestrator(ctx.principal.organizationId, "GET", "/internal/delivery-defaults", undefined);
}

/** Set a project's image (key and token) or clear it (null); returns the project and the key it had. */
async function recordProjectImage(ctx: RequestContext, projectId: string, image: { key: string; token: string } | null) {
  const { organizationId } = ctx.principal;
  return withOrg(organizationId, async (scope) => {
    const [before] = (await scope.sql`
      SELECT image_key AS key FROM projects WHERE id = ${projectId} FOR UPDATE`) as Array<{ key: string | null }>;
    if (!before) throw notFound(`project ${projectId} not found`);
    const [project] = (await scope.sql`
      UPDATE projects SET image_key = ${image?.key ?? null}, image_token = ${image?.token ?? null}, updated_at = now()
      WHERE id = ${projectId}
      RETURNING ${scope.sql.unsafe(PROJECT_SELECT)}`) as ProjectRow[];
    await appendInScope(scope, {
      eventType: EventTypes.ProjectUpdated,
      organizationId,
      projectId,
      actor: { type: auditActor(ctx.principal).kind, id: auditActor(ctx.principal).id },
      source: "control-plane",
      payload: { changed: ["image"] },
    });
    return { result: project!, old: before.key };
  });
}

/** A project in the caller's organization, by id, or 404: its id is then safe to name storage by. */
async function existingProject(ctx: RequestContext): Promise<string> {
  const projectId = ctx.params.id!;
  const rows = await withOrg(ctx.principal.organizationId, (scope) => scope.sql`
    SELECT id FROM projects WHERE id = ${projectId}`) as Array<{ id: string }>;
  if (!rows[0]) throw notFound(`project ${projectId} not found`);
  return rows[0].id;
}

/** Replace a project's image with the one in the body (see api/faces.ts). */
async function uploadProjectImage(ctx: RequestContext): Promise<Response> {
  const projectId = await existingProject(ctx);
  await requireProjectEditor(ctx, projectId);
  return json(await replaceImage(ctx.request, `${ctx.principal.organizationId}/projects/${projectId}`,
    (image) => recordProjectImage(ctx, projectId, image)));
}

/** A project's initials again: its image, and the object, go. */
async function removeProjectImage(ctx: RequestContext): Promise<Response> {
  const projectId = await existingProject(ctx);
  await requireProjectEditor(ctx, projectId);
  const { result, old } = await recordProjectImage(ctx, projectId, null);
  if (old) await deleteObject(old);
  return json(result);
}

/** A project's image for an `<img>`: it sends no key, so the URL carries a token. */
async function getProjectImage(ctx: PublicContext): Promise<Response> {
  const token = ctx.url.searchParams.get("t") ?? "";
  const rows = await withoutTenant(async ({ sql }) =>
    (await sql`SELECT project_image(${ctx.params.id!}, ${token}) AS key`) as Array<{ key: string | null }>);
  return serveImage(rows[0]?.key);
}

export function registerProjectRoutes(router: Router): void {
  router.get("/v1/delivery-defaults", deliveryDefaults);
  // A project brings its own models, image, delivery policy and trusted
  // repositories: making one is changing them, so it is an admin's too.
  router.post("/v1/projects", async (ctx) => {
    await requireOrgAdmin(ctx);
    return createProject(ctx);
  });
  router.get("/v1/projects", listProjects);
  router.get("/v1/projects/:id", getProject);
  // A project's name, runtime image, models and delivery policy: the same
  // settings its settings route guards, so the same people may change them.
  router.patch("/v1/projects/:id", async (ctx) => {
    await requireProjectEditor(ctx, ctx.params.id!);
    return updateProject(ctx);
  });
  // Its face: an image, uploaded as the body, or initials again.
  router.put("/v1/projects/:id/image", uploadProjectImage);
  router.delete("/v1/projects/:id/image", removeProjectImage);
  router.publicRoute("GET", "/v1/projects/:id/image", getProjectImage);
}
