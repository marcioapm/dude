/**
 * Project and repository routes.
 *
 * Projects own the per-role agent model configuration, which is the knob that
 * decides which model runs as orchestrator, implementer, reviewer and so on.
 */

import { z } from "zod";
import { agentModelsSchema, deliveryPolicySchema, newId, EventTypes } from "@dude/domain";
import { withOrg } from "../../db/client.ts";
import { appendInScope } from "../../events/ledger.ts";
import { conflict, json, notFound, parseBody } from "../http.ts";
import type { RequestContext, Router } from "../router.ts";
import { repositoryFields } from "./structure.ts";
import { registerRepositoryWebhook } from "./pullRequests.ts";
import { orchestrator } from "../../orchestrator/client.ts";

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
  runtimeImage: z.string().nullable().default(null),
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
  deliveryPolicy: Record<string, unknown>;
  createdAt: string;
}

const PROJECT_SELECT = `
  id, organization_id AS "organizationId", name, slug, description,
  agent_models AS "agentModels", runtime_image AS "runtimeImage",
  delivery_policy AS "deliveryPolicy", created_at AS "createdAt"`;

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

    const projectId = newId("project");
    const rows = (await scope.sql`
      INSERT INTO projects (id, organization_id, name, slug, key_prefix, description, agent_models, runtime_image, delivery_policy)
      VALUES (${projectId}, ${organizationId}, ${input.name}, ${input.slug}, ${keyPrefix(input.slug)}, ${input.description},
              ${input.agentModels ?? {}}::jsonb, ${input.runtimeImage}, ${input.deliveryPolicy ?? {}}::jsonb)
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
      actor: { type: "human", id: ctx.principal.apiKeyId },
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
    const rows = (await scope.sql`
      UPDATE projects SET
        name          = COALESCE(${input.name ?? null}, name),
        description   = COALESCE(${input.description ?? null}, description),
        agent_models  = COALESCE(${input.agentModels ?? null}::jsonb,
                                 agent_models),
        delivery_policy = COALESCE(${input.deliveryPolicy ?? null}::jsonb, delivery_policy),
        runtime_image = CASE WHEN ${input.runtimeImage !== undefined} THEN ${input.runtimeImage ?? null}
                             ELSE runtime_image END
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

export function registerProjectRoutes(router: Router): void {
  router.get("/v1/delivery-defaults", deliveryDefaults);
  router.post("/v1/projects", createProject);
  router.get("/v1/projects", listProjects);
  router.get("/v1/projects/:id", getProject);
  router.patch("/v1/projects/:id", updateProject);
}
