/**
 * Where a task starts from: new work, or an existing pull request or branch
 * (adopted work). Checked against the project and GitHub by the
 * orchestrator, which holds the forge credential, and kept here with the
 * task in `task_sources`. Fixed once the task's delivery has started, as
 * the goal is.
 */

import { taskSourceInputSchema, type TaskSource, type TaskSourceInput } from "@dude/domain";
import type { OrgScope } from "../../db/client.ts";
import { HttpError } from "../http.ts";
import { orchestrator } from "../../orchestrator/client.ts";
import type { RequestContext } from "../router.ts";

/** A source in a create or update: one, or null for new work. */
export const taskSourceInput = taskSourceInputSchema.nullable();

/** The SELECT expression for a task's source, as JSON or null, for `tasks` rows. */
export const SOURCE_JSON = `(SELECT json_strip_nulls(json_build_object('kind', s.kind, 'repositoryId', s.repository_id, 'repository', r.name,
    'number', s.number, 'title', s.title, 'headBranch', s.head_branch, 'baseBranch', s.base_branch))
  FROM task_sources s JOIN repositories r ON r.id = s.repository_id WHERE s.task_id = tasks.id) AS source`;

/**
 * The orchestrator's verdict on a source: resolved, or why not, as the
 * error the API answers (422 with the rule's code; 502 when GitHub could
 * not be asked). taskId names the task it is for, so its own branch is not
 * another task's.
 */
export async function checkTaskSource(ctx: RequestContext, projectId: string, source: TaskSourceInput,
  taskId: string | null): Promise<TaskSource> {
  const body = {
    kind: source.kind, repositoryId: source.repositoryId, taskId: taskId ?? "",
    ...(source.kind === "pr" ? { pullRequest: source.pullRequest } : { branch: source.branch, base: source.base ?? "" }),
  };
  const res = await orchestrator(ctx.principal.organizationId, "POST",
    `/internal/projects/${encodeURIComponent(projectId)}/sources/check`, JSON.stringify(body), ctx.principal);
  const out = (await res.json().catch(() => ({}))) as Record<string, unknown> & { error?: { code?: string; message?: string } };
  if (!res.ok) {
    throw new HttpError(res.status, out.error?.message ?? "GitHub could not be asked about the source", out.error?.code ?? "error");
  }
  return {
    kind: out.kind as TaskSource["kind"], repositoryId: out.repositoryId as string, repository: out.repository as string,
    ...(typeof out.number === "number" ? { number: out.number, title: (out.title as string) ?? "" } : {}),
    headBranch: out.headBranch as string, baseBranch: out.baseBranch as string,
  };
}

/**
 * The source a request names is the one kept: a dialog sends every field,
 * and saving it unchanged is no change (and asks GitHub nothing).
 */
export function sameSource(stored: TaskSource | null, given: TaskSourceInput | null): boolean {
  if (!stored || !given) return !stored && !given;
  if (stored.kind !== given.kind || stored.repositoryId !== given.repositoryId) return false;
  if (given.kind === "pr") {
    const n = /^(?:#?(\d+)|https?:\/\/\S+\/pull\/(\d+)\/?(?:[?#].*)?)$/.exec(given.pullRequest);
    return n !== null && Number(n[1] ?? n[2]) === stored.number;
  }
  return given.branch === stored.headBranch && (given.base === undefined || given.base === stored.baseBranch);
}

/**
 * Keep the task's source, replacing any: null clears it. The repository a
 * source is in is one the task works on, with write access.
 */
export async function setTaskSource(scope: OrgScope, organizationId: string, taskId: string, source: TaskSource | null) {
  await scope.sql`DELETE FROM task_sources WHERE task_id = ${taskId}`;
  if (!source) return;
  await scope.sql`
    INSERT INTO task_sources (task_id, organization_id, repository_id, kind, number, title, head_branch, base_branch)
    VALUES (${taskId}, ${organizationId}, ${source.repositoryId}, ${source.kind}, ${source.number ?? null},
            ${source.title ?? null}, ${source.headBranch}, ${source.baseBranch})`;
  await scope.sql`
    INSERT INTO task_repositories (organization_id, task_id, repository_id, access)
    VALUES (${organizationId}, ${taskId}, ${source.repositoryId}, 'write')
    ON CONFLICT (task_id, repository_id) DO UPDATE SET access = 'write'`;
}
