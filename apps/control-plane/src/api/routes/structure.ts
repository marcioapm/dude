/**
 * How a project's work is organised: its repositories, its epics, and
 * editing the tasks within them.
 *
 * Structure is metadata, so it lives here, in the backend, not in the
 * orchestrator: nothing runs when an epic is renamed. Every change is a
 * ledger event, so the history of how work was organised is as auditable
 * as the work itself.
 */

import { z } from "zod";
import { EventTypes, epicState, epicStateSchema, newId, type EpicState } from "@dude/domain";
import { withOrg, type OrgScope } from "../../db/client.ts";
import { appendInScope } from "../../events/ledger.ts";
import { badRequest, conflict, json, noContent, notFound, parseBody } from "../http.ts";
import type { RequestContext, Router } from "../router.ts";
import { REPOSITORIES_JSON, setTaskRepositories, taskRepositoriesInput } from "./taskRepositories.ts";
import { isPerson, ownerJson } from "./people.ts";

const REPOSITORY_SELECT = `id, project_id AS "projectId", name, url, default_branch AS "defaultBranch", trust,
  created_at AS "createdAt"`;
// An epic's state is stored only when a person sets it; otherwise it is what
// its tasks say (epicState, @dude/domain).
const EPIC_SELECT = `id, project_id AS "projectId", title, description, position, state AS "storedState",
  (SELECT COALESCE(array_agg(t.status::text), '{}') FROM tasks t WHERE t.epic_id = epics.id) AS "taskStatuses",
  created_at AS "createdAt", updated_at AS "updatedAt"`;

/** An epic as the API gives it: its state resolved, the statuses it came from dropped. */
function epicJson({ taskStatuses, ...row }: Record<string, unknown>) {
  return { ...row, state: epicState(row.storedState as EpicState | null, taskStatuses as string[]) };
}

/** Record a structural change as the person who made it. */
function record(scope: OrgScope, ctx: RequestContext, eventType: string, projectId: string,
  payload: Record<string, unknown>, taskId: string | null = null) {
  return appendInScope(scope, {
    eventType,
    organizationId: ctx.principal.organizationId,
    projectId,
    taskId,
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
    SELECT 1 FROM workflow_runs w
      JOIN task_repositories wr ON wr.task_id = w.task_id
      WHERE w.status IN ('running', 'waiting') AND wr.repository_id = ${repositoryId}
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
  /** Omitted: as its tasks say. */
  state: epicStateSchema.nullable().default(null),
});
const updateEpicInput = z.object({
  title: epicInput.shape.title,
  description: z.string().max(10_000),
  /** null: back to what its tasks say. */
  state: epicStateSchema.nullable(),
  /** Where it sits among its project's epics, 0 first. */
  position: z.number().int().min(0),
}).partial();

async function listEpics(ctx: RequestContext): Promise<Response> {
  const projectId = ctx.params.id!;
  const epics = await withOrg(ctx.principal.organizationId, async (scope) => {
    return (await scope.sql`
      SELECT ${scope.sql.unsafe(EPIC_SELECT)} FROM epics WHERE project_id = ${projectId} ORDER BY position, created_at`) as Array<
      Record<string, unknown>
    >;
  });
  return json({ epics: epics.map(epicJson) });
}

async function createEpic(ctx: RequestContext): Promise<Response> {
  const input = await parseBody(ctx.request, epicInput);
  const projectId = ctx.params.id!;
  const result = await withOrg(ctx.principal.organizationId, async (scope) => {
    // The project row serialises changes to its epics' order.
    if ((await scope.sql`SELECT 1 FROM projects WHERE id = ${projectId} FOR UPDATE`).length === 0) return null;
    const rows = (await scope.sql`
      INSERT INTO epics (id, organization_id, project_id, title, description, state, position)
      VALUES (${newId("epic")}, ${ctx.principal.organizationId}, ${projectId}, ${input.title}, ${input.description}, ${input.state},
              (SELECT COALESCE(max(position) + 1, 0) FROM epics WHERE project_id = ${projectId}))
      RETURNING ${scope.sql.unsafe(EPIC_SELECT)}`) as Array<Record<string, unknown>>;
    await record(scope, ctx, EventTypes.EpicCreated, projectId, { epicId: rows[0]!.id, title: input.title });
    return rows[0]!;
  });
  if (!result) throw notFound(`project ${projectId} not found`);
  return json(epicJson(result), 201);
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
        state = CASE WHEN ${input.state !== undefined} THEN ${input.state ?? null} ELSE state END,
        updated_at = now()
      WHERE id = ${id}
      RETURNING ${scope.sql.unsafe(EPIC_SELECT)}`) as Array<Record<string, unknown>>;
    await record(scope, ctx, EventTypes.EpicUpdated, projectId, { epicId: id, ...input });
    return rows[0]!;
  });
  if (!epic) throw notFound(`epic ${id} not found`);
  return json(epicJson(epic));
}

/**
 * Delete an epic. Its tasks are kept and move to the project itself:
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
    await scope.sql`UPDATE tasks SET epic_id = NULL, updated_at = now() WHERE epic_id = ${id}`;
    await scope.sql`DELETE FROM epics WHERE id = ${id}`;
    await record(scope, ctx, EventTypes.EpicDeleted, rows[0].projectId, { epicId: id, title: rows[0].title });
    return true;
  });
  if (!found) throw notFound(`epic ${id} not found`);
  return noContent();
}

// ---------------------------------------------------------------------------
// Tasks
// ---------------------------------------------------------------------------

const updateTaskInput = z.object({
  title: z.string().trim().min(1).max(500),
  goal: z.string().max(10_000),
  acceptanceCriteria: z.array(z.string().max(2000)),
  /** Move it into an epic of its project, or out of any (null). */
  epicId: z.string().min(1).nullable(),
  /** The repositories it works on; fixed, like the task, once delivery starts. */
  repositories: taskRepositoriesInput,
  /** Hand it to another of the organization's people (a user key's id). */
  ownerId: z.string().min(1),
}).partial();

/**
 * Edit a task: what it asks for, where it sits, and who drives it. What
 * it asks for is fixed once delivery starts — agents are working to it — so
 * editing the goal or criteria then is refused; moving it between epics, or
 * handing it to someone else, never is.
 */
async function updateTask(ctx: RequestContext): Promise<Response> {
  const { ownerId, ...input } = await parseBody(ctx.request, updateTaskInput);
  const id = ctx.params.id!;
  const result = await withOrg(ctx.principal.organizationId, async (scope) => {
    const current = (await scope.sql`
      SELECT project_id AS "projectId", status, owner_key_id AS "ownerId" FROM tasks WHERE id = ${id} FOR UPDATE`) as Array<{
      projectId: string;
      status: string;
      ownerId: string | null;
    }>;
    if (!current[0]) return { missing: true as const };
    const { projectId, status } = current[0];
    const changesTheTask = input.title !== undefined || input.goal !== undefined ||
      input.acceptanceCriteria !== undefined || input.repositories !== undefined;
    // Started means a delivery exists, whatever the status says yet: the
    // orchestrator moves the status on its own schedule.
    const delivering = await scope.sql`SELECT 1 FROM workflow_runs WHERE task_id = ${id} LIMIT 1`;
    if (changesTheTask && delivering.length > 0) return { started: status };
    if (input.epicId) {
      const epic = await scope.sql`SELECT 1 FROM epics WHERE id = ${input.epicId} AND project_id = ${projectId}`;
      if (epic.length === 0) return { noEpic: input.epicId };
    }
    if (ownerId !== undefined && !(await isPerson(scope, ownerId))) return { noPerson: ownerId };
    if (Object.keys(input).length === 0 && ownerId === undefined) return { unchanged: true as const };
    if (input.repositories) {
      const missing = await setTaskRepositories(scope, ctx.principal.organizationId, projectId, id, input.repositories);
      if (missing) return { noRepository: missing };
    }
    const rows = (await scope.sql`
      UPDATE tasks SET
        title = COALESCE(${input.title ?? null}, title),
        goal = COALESCE(${input.goal ?? null}, goal),
        acceptance_criteria = CASE WHEN ${input.acceptanceCriteria !== undefined}
                                   THEN ${input.acceptanceCriteria ?? []}::jsonb ELSE acceptance_criteria END,
        epic_id = CASE WHEN ${input.epicId !== undefined} THEN ${input.epicId ?? null} ELSE epic_id END,
        owner_key_id = COALESCE(${ownerId ?? null}, owner_key_id),
        updated_at = now()
      WHERE id = ${id}
      RETURNING id, project_id AS "projectId", epic_id AS "epicId", ${scope.sql.unsafe(REPOSITORIES_JSON)}, title, goal,
        acceptance_criteria AS "acceptanceCriteria", status, ${scope.sql.unsafe(ownerJson())},
        updated_at AS "updatedAt"`) as Array<Record<string, unknown>>;
    if (Object.keys(input).length > 0) await record(scope, ctx, EventTypes.TaskUpdated, projectId, { ...input }, id);
    // Its own event: who drives a task is not what it asks for, and the
    // history of who held it is worth reading on its own.
    if (ownerId !== undefined && ownerId !== current[0].ownerId) {
      await record(scope, ctx, EventTypes.TaskOwnerChanged, projectId, { from: current[0].ownerId, to: ownerId }, id);
    }
    return { task: rows[0]! };
  });
  if ("missing" in result) throw notFound(`task ${id} not found`);
  if ("started" in result) {
    throw conflict(`delivery has started (${result.started}); what it asks for can no longer change — abort and create a new one`);
  }
  if ("noEpic" in result) throw notFound(`epic ${result.noEpic} is not in this task's project`);
  if ("noRepository" in result) throw notFound(`repository ${result.noRepository} is not in this task's project`);
  if ("noPerson" in result) throw notFound(`${result.noPerson} is not one of this organization's people`);
  if ("unchanged" in result) throw badRequest("nothing to change");
  return json(result.task);
}

export function registerStructureRoutes(router: Router): void {
  router.post("/v1/projects/:id/repositories", addRepository);
  router.patch("/v1/repositories/:id", updateRepository);
  router.delete("/v1/repositories/:id", removeRepository);

  router.get("/v1/projects/:id/epics", listEpics);
  router.post("/v1/projects/:id/epics", createEpic);
  router.patch("/v1/epics/:id", updateEpic);
  router.delete("/v1/epics/:id", deleteEpic);

  router.patch("/v1/tasks/:id", updateTask);
}
