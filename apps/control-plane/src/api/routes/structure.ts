/**
 * How a project's work is organised: its repositories, its epics, and
 * editing the work items within them.
 *
 * Structure is metadata, so it lives here, in the backend, not in the
 * orchestrator: nothing runs when an epic is renamed. Every change is a
 * ledger event, so the history of how work was organised is as auditable
 * as the work itself.
 */

import { z } from "zod";
import { EventTypes, newId } from "@dude/domain";
import { withOrg, type OrgScope } from "../../db/client.ts";
import { appendInScope } from "../../events/ledger.ts";
import { badRequest, conflict, json, noContent, notFound, parseBody } from "../http.ts";
import type { RequestContext, Router } from "../router.ts";

const REPOSITORY_SELECT = `id, project_id AS "projectId", name, url, default_branch AS "defaultBranch", trust,
  created_at AS "createdAt"`;
const EPIC_SELECT = `id, project_id AS "projectId", title, description, position,
  created_at AS "createdAt", updated_at AS "updatedAt"`;

/** Record a structural change as the person who made it. */
function record(scope: OrgScope, ctx: RequestContext, eventType: string, projectId: string,
  payload: Record<string, unknown>, workItemId: string | null = null) {
  return appendInScope(scope, {
    eventType,
    organizationId: ctx.principal.organizationId,
    projectId,
    workItemId,
    actor: { type: "human", id: ctx.principal.apiKeyId },
    source: "control-plane",
    payload,
  });
}

// ---------------------------------------------------------------------------
// Repositories
// ---------------------------------------------------------------------------

/**
 * What a repository may be. The URL reaches `git clone`, so only the forms a
 * git server serves: https, git://, or ssh (scp-like or ssh://). No
 * `file://`, no `ext::`, nothing git could read as an option.
 */
export const repositoryFields = {
  name: z.string().min(1).max(100).regex(/^[A-Za-z0-9][A-Za-z0-9._-]*$/, "name must be a plain directory name"),
  url: z
    .string()
    .min(1)
    .max(2000)
    .regex(/^((https|ssh|git):\/\/[^\s]+|[A-Za-z0-9._-]+@[A-Za-z0-9.-]+:[^\s]+)$/, "url must be an https, ssh or git:// URL"),
  defaultBranch: z.string().min(1).max(200),
  trust: z.enum(["trusted_internal", "untrusted_external"]),
};
const addRepositoryInput = z.object({
  ...repositoryFields,
  defaultBranch: repositoryFields.defaultBranch.default("main"),
  trust: repositoryFields.trust.default("trusted_internal"),
});
const updateRepositoryInput = z.object(repositoryFields).partial();

async function addRepository(ctx: RequestContext): Promise<Response> {
  const input = await parseBody(ctx.request, addRepositoryInput);
  const projectId = ctx.params.id!;
  const result = await withOrg(ctx.principal.organizationId, async (scope) => {
    // The project row serialises changes to its repositories.
    if ((await scope.sql`SELECT 1 FROM projects WHERE id = ${projectId} FOR UPDATE`).length === 0) return { missing: true as const };
    if ((await scope.sql`SELECT 1 FROM repositories WHERE project_id = ${projectId} AND name = ${input.name}`).length > 0) {
      return { taken: true as const };
    }
    const rows = (await scope.sql`
      INSERT INTO repositories (id, organization_id, project_id, name, url, default_branch, trust)
      VALUES (${newId("repository")}, ${ctx.principal.organizationId}, ${projectId}, ${input.name}, ${input.url},
              ${input.defaultBranch}, ${input.trust})
      RETURNING ${scope.sql.unsafe(REPOSITORY_SELECT)}`) as Array<Record<string, unknown>>;
    await record(scope, ctx, EventTypes.RepositoryAdded, projectId, { repositoryId: rows[0]!.id, name: input.name });
    return { repository: rows[0]! };
  });
  if ("missing" in result) throw notFound(`project ${projectId} not found`);
  if ("taken" in result) throw conflict(`the project already has a repository named "${input.name}"`);
  return json(result.repository, 201);
}

/**
 * Deliveries still working with a repository: a Run on it, or a workflow
 * waiting on a pull request to it. Changing or removing the repository
 * under them would clone one place and open the pull request in another,
 * or lose the pull request's history.
 */
async function inUse(scope: OrgScope, repositoryId: string): Promise<boolean> {
  const rows = await scope.sql`
    SELECT 1 FROM runs WHERE repository_id = ${repositoryId}
      AND status IN ('pending', 'scheduled', 'starting', 'running', 'paused')
    UNION ALL
    SELECT 1 FROM workflow_runs w
      WHERE w.status IN ('running', 'waiting')
        AND w.state->>'repositoryId' = ${repositoryId}
    LIMIT 1`;
  return rows.length > 0;
}

async function updateRepository(ctx: RequestContext): Promise<Response> {
  const input = await parseBody(ctx.request, updateRepositoryInput);
  const id = ctx.params.id!;
  const result = await withOrg(ctx.principal.organizationId, async (scope) => {
    const current = (await scope.sql`
      SELECT project_id AS "projectId" FROM repositories WHERE id = ${id} FOR UPDATE`) as Array<{ projectId: string }>;
    if (!current[0]) return { missing: true as const };
    const movesIt = input.url !== undefined || input.defaultBranch !== undefined || input.name !== undefined;
    if (movesIt && (await inUse(scope, id))) return { busy: true as const };
    if (input.name !== undefined) {
      const taken = await scope.sql`
        SELECT 1 FROM repositories WHERE project_id = ${current[0].projectId} AND name = ${input.name} AND id <> ${id}`;
      if (taken.length > 0) return { taken: true as const };
    }
    const rows = (await scope.sql`
      UPDATE repositories SET
        name = COALESCE(${input.name ?? null}, name),
        url = COALESCE(${input.url ?? null}, url),
        default_branch = COALESCE(${input.defaultBranch ?? null}, default_branch),
        trust = COALESCE(${input.trust ?? null}::trust_class, trust)
      WHERE id = ${id}
      RETURNING ${scope.sql.unsafe(REPOSITORY_SELECT)}`) as Array<Record<string, unknown>>;
    await record(scope, ctx, EventTypes.RepositoryUpdated, current[0].projectId, { repositoryId: id, ...input });
    return { repository: rows[0]! };
  });
  if ("missing" in result) throw notFound(`repository ${id} not found`);
  if ("busy" in result) throw conflict("work is being delivered to this repository; wait for it or abort it first");
  if ("taken" in result) throw conflict(`the project already has a repository named "${input.name}"`);
  return json(result.repository);
}

/**
 * Remove a repository. Refused while work is being delivered to it: its
 * Runs would lose the checkout they are working in.
 */
async function removeRepository(ctx: RequestContext): Promise<Response> {
  const id = ctx.params.id!;
  const result = await withOrg(ctx.principal.organizationId, async (scope) => {
    const repo = (await scope.sql`
      SELECT project_id AS "projectId", name FROM repositories WHERE id = ${id} FOR UPDATE`) as Array<{
      projectId: string;
      name: string;
    }>;
    if (!repo[0]) return { missing: true as const };
    if (await inUse(scope, id)) return { busy: true as const };
    // Its pull requests are the record of work done; a repository with any
    // keeps them, and cannot be removed.
    if ((await scope.sql`SELECT 1 FROM pull_requests WHERE repository_id = ${id} LIMIT 1`).length > 0) {
      return { history: true as const };
    }
    await scope.sql`DELETE FROM repositories WHERE id = ${id}`;
    await record(scope, ctx, EventTypes.RepositoryRemoved, repo[0].projectId, { repositoryId: id, name: repo[0].name });
    return { ok: true as const };
  });
  if ("missing" in result) throw notFound(`repository ${id} not found`);
  if ("busy" in result) throw conflict("work is being delivered to this repository; wait for it or abort it first");
  if ("history" in result) throw conflict("this repository has pull requests from past work, which removing it would lose");
  return noContent();
}

// ---------------------------------------------------------------------------
// Epics
// ---------------------------------------------------------------------------

const epicInput = z.object({
  title: z.string().trim().min(1).max(200),
  description: z.string().max(10_000).default(""),
});
const updateEpicInput = z.object({
  title: epicInput.shape.title,
  description: z.string().max(10_000),
  /** Where it sits among its project's epics, 0 first. */
  position: z.number().int().min(0),
}).partial();

async function listEpics(ctx: RequestContext): Promise<Response> {
  const projectId = ctx.params.id!;
  const epics = await withOrg(ctx.principal.organizationId, async (scope) => {
    return await scope.sql`
      SELECT ${scope.sql.unsafe(EPIC_SELECT)} FROM epics WHERE project_id = ${projectId} ORDER BY position, created_at`;
  });
  return json({ epics });
}

async function createEpic(ctx: RequestContext): Promise<Response> {
  const input = await parseBody(ctx.request, epicInput);
  const projectId = ctx.params.id!;
  const result = await withOrg(ctx.principal.organizationId, async (scope) => {
    // The project row serialises changes to its epics' order.
    if ((await scope.sql`SELECT 1 FROM projects WHERE id = ${projectId} FOR UPDATE`).length === 0) return null;
    const rows = (await scope.sql`
      INSERT INTO epics (id, organization_id, project_id, title, description, position)
      VALUES (${newId("epic")}, ${ctx.principal.organizationId}, ${projectId}, ${input.title}, ${input.description},
              (SELECT COALESCE(max(position) + 1, 0) FROM epics WHERE project_id = ${projectId}))
      RETURNING ${scope.sql.unsafe(EPIC_SELECT)}`) as Array<Record<string, unknown>>;
    await record(scope, ctx, EventTypes.EpicCreated, projectId, { epicId: rows[0]!.id, title: input.title });
    return rows[0]!;
  });
  if (!result) throw notFound(`project ${projectId} not found`);
  return json(result, 201);
}

/** Edit an epic; a new position moves it, and the others close up around it. */
async function updateEpic(ctx: RequestContext): Promise<Response> {
  const input = await parseBody(ctx.request, updateEpicInput);
  const id = ctx.params.id!;
  const epic = await withOrg(ctx.principal.organizationId, async (scope) => {
    const current = (await scope.sql`SELECT project_id AS "projectId" FROM epics WHERE id = ${id}`) as Array<{
      projectId: string;
    }>;
    if (!current[0]) return null;
    const { projectId } = current[0];
    // Two reorders at once would each rank the list as it was.
    await scope.sql`SELECT 1 FROM projects WHERE id = ${projectId} FOR UPDATE`;
    if (input.position !== undefined) {
      // Renumber the project's epics with this one at its new place.
      await scope.sql`
        WITH others AS (
          SELECT id, row_number() OVER (ORDER BY position, created_at) - 1 AS n
          FROM epics WHERE project_id = ${projectId} AND id <> ${id}
        )
        UPDATE epics e SET position = CASE WHEN o.n >= ${input.position} THEN o.n + 1 ELSE o.n END
        FROM others o WHERE e.id = o.id`;
      const count = (await scope.sql`SELECT count(*)::int AS n FROM epics WHERE project_id = ${projectId}`) as Array<{ n: number }>;
      await scope.sql`UPDATE epics SET position = ${Math.min(input.position, count[0]!.n - 1)} WHERE id = ${id}`;
    }
    const rows = (await scope.sql`
      UPDATE epics SET
        title = COALESCE(${input.title ?? null}, title),
        description = COALESCE(${input.description ?? null}, description),
        updated_at = now()
      WHERE id = ${id}
      RETURNING ${scope.sql.unsafe(EPIC_SELECT)}`) as Array<Record<string, unknown>>;
    await record(scope, ctx, EventTypes.EpicUpdated, projectId, { epicId: id, ...input });
    return rows[0]!;
  });
  if (!epic) throw notFound(`epic ${id} not found`);
  return json(epic);
}

/**
 * Delete an epic. Its work items are kept and move to the project itself:
 * an epic is a grouping, and removing a group must not remove the work.
 */
async function deleteEpic(ctx: RequestContext): Promise<Response> {
  const id = ctx.params.id!;
  const found = await withOrg(ctx.principal.organizationId, async (scope) => {
    const rows = (await scope.sql`SELECT project_id AS "projectId", title FROM epics WHERE id = ${id}`) as Array<{
      projectId: string;
      title: string;
    }>;
    if (!rows[0]) return false;
    await scope.sql`UPDATE work_items SET epic_id = NULL, updated_at = now() WHERE epic_id = ${id}`;
    await scope.sql`DELETE FROM epics WHERE id = ${id}`;
    await record(scope, ctx, EventTypes.EpicDeleted, rows[0].projectId, { epicId: id, title: rows[0].title });
    return true;
  });
  if (!found) throw notFound(`epic ${id} not found`);
  return noContent();
}

// ---------------------------------------------------------------------------
// Work items
// ---------------------------------------------------------------------------

const updateWorkItemInput = z.object({
  title: z.string().trim().min(1).max(500),
  goal: z.string().max(10_000),
  acceptanceCriteria: z.array(z.string().max(2000)),
  /** Move it into an epic of its project, or out of any (null). */
  epicId: z.string().min(1).nullable(),
  /** The repository it changes; fixed, like the task, once delivery starts. */
  repositoryId: z.string().min(1).nullable(),
}).partial();

/**
 * Edit a work item: what it asks for, and where it sits. What it asks for
 * is fixed once delivery starts — agents are working to it — so editing the
 * goal or criteria then is refused; moving it between epics never is.
 */
async function updateWorkItem(ctx: RequestContext): Promise<Response> {
  const input = await parseBody(ctx.request, updateWorkItemInput);
  const id = ctx.params.id!;
  const result = await withOrg(ctx.principal.organizationId, async (scope) => {
    const current = (await scope.sql`
      SELECT project_id AS "projectId", status FROM work_items WHERE id = ${id} FOR UPDATE`) as Array<{
      projectId: string;
      status: string;
    }>;
    if (!current[0]) return { missing: true as const };
    const { projectId, status } = current[0];
    const changesTheTask = input.title !== undefined || input.goal !== undefined ||
      input.acceptanceCriteria !== undefined || input.repositoryId !== undefined;
    // Started means a delivery exists, whatever the status says yet: the
    // orchestrator moves the status on its own schedule.
    const delivering = await scope.sql`SELECT 1 FROM workflow_runs WHERE work_item_id = ${id} LIMIT 1`;
    if (changesTheTask && delivering.length > 0) return { started: status };
    if (input.epicId) {
      const epic = await scope.sql`SELECT 1 FROM epics WHERE id = ${input.epicId} AND project_id = ${projectId}`;
      if (epic.length === 0) return { noEpic: input.epicId };
    }
    if (input.repositoryId) {
      const repo = await scope.sql`SELECT 1 FROM repositories WHERE id = ${input.repositoryId} AND project_id = ${projectId}`;
      if (repo.length === 0) return { noRepository: input.repositoryId };
    }
    if (Object.keys(input).length === 0) return { unchanged: true as const };
    const rows = (await scope.sql`
      UPDATE work_items SET
        title = COALESCE(${input.title ?? null}, title),
        goal = COALESCE(${input.goal ?? null}, goal),
        acceptance_criteria = CASE WHEN ${input.acceptanceCriteria !== undefined}
                                   THEN ${input.acceptanceCriteria ?? []}::jsonb ELSE acceptance_criteria END,
        epic_id = CASE WHEN ${input.epicId !== undefined} THEN ${input.epicId ?? null} ELSE epic_id END,
        repository_id = CASE WHEN ${input.repositoryId !== undefined} THEN ${input.repositoryId ?? null} ELSE repository_id END,
        updated_at = now()
      WHERE id = ${id}
      RETURNING id, project_id AS "projectId", epic_id AS "epicId", repository_id AS "repositoryId", title, goal,
        acceptance_criteria AS "acceptanceCriteria", status, updated_at AS "updatedAt"`) as Array<Record<string, unknown>>;
    await record(scope, ctx, EventTypes.WorkItemUpdated, projectId, { ...input }, id);
    return { workItem: rows[0]! };
  });
  if ("missing" in result) throw notFound(`work item ${id} not found`);
  if ("started" in result) {
    throw conflict(`delivery has started (${result.started}); what it asks for can no longer change — abort and create a new one`);
  }
  if ("noEpic" in result) throw notFound(`epic ${result.noEpic} is not in this work item's project`);
  if ("noRepository" in result) throw notFound(`repository ${result.noRepository} is not in this work item's project`);
  if ("unchanged" in result) throw badRequest("nothing to change");
  return json(result.workItem);
}

export function registerStructureRoutes(router: Router): void {
  router.post("/v1/projects/:id/repositories", addRepository);
  router.patch("/v1/repositories/:id", updateRepository);
  router.delete("/v1/repositories/:id", removeRepository);

  router.get("/v1/projects/:id/epics", listEpics);
  router.post("/v1/projects/:id/epics", createEpic);
  router.patch("/v1/epics/:id", updateEpic);
  router.delete("/v1/epics/:id", deleteEpic);

  router.patch("/v1/work-items/:id", updateWorkItem);
}
