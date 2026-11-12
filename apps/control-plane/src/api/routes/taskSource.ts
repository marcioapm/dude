/**
 * Where a task starts from: new work, or an existing pull request or branch
 * (adopted work). Checked against the project and GitHub by the
 * orchestrator, which holds the forge credential, and kept here with the
 * task in `task_sources`. Fixed once the task's delivery has started, as
 * the goal is.
 */

import { z } from "zod";
import { taskSourceInputSchema, taskSourceModeSchema, type TaskSource, type TaskSourceInput } from "@dude/domain";
import type { OrgScope } from "../../db/client.ts";
import { HttpError } from "../http.ts";
import { orchestrator } from "../../orchestrator/client.ts";
import type { RequestContext } from "../router.ts";

/** A source in a create or update: one, or null for new work. */
export const taskSourceInput = taskSourceInputSchema.nullable();

/** The SELECT expression for a task's source, as JSON or null, for `tasks` rows. */
export const SOURCE_JSON = `(SELECT json_strip_nulls(json_build_object('kind', s.kind, 'repositoryId', s.repository_id, 'repository', r.name,
    'number', s.number, 'title', s.title, 'headBranch', s.head_branch, 'baseBranch', s.base_branch, 'mode', s.mode))
  FROM task_sources s JOIN repositories r ON r.id = s.repository_id WHERE s.task_id = tasks.id) AS source`;

/** A live task holding the branch a source names: what superseded_required names, and what a confirmation stops. */
export interface SourceHolder { id: string; key: string; mode: string }

/**
 * A person's answer to superseded_required: true stops whichever live task
 * holds the branch, a task id only that one (refused again if the holder
 * changed since they were asked).
 */
export const supersedeInput = z.union([z.literal(true), z.string().min(1)]).optional();
export type Supersede = z.infer<typeof supersedeInput>;

/**
 * The orchestrator's verdict on a source: resolved, or why not, as the
 * error the API answers (422 with the rule's code; 409 superseded_required
 * naming the live task that holds its branch, in details.holder; 502 when
 * GitHub could not be asked). taskId names the task it is for, so its own
 * branch is not another task's. Its mode is the one asked for, else the
 * orchestrator's default for what the source is. With supersede, the
 * holder confirmed comes back as `supersedes`.
 */
export async function checkTaskSource(ctx: RequestContext, projectId: string, source: TaskSourceInput,
  taskId: string | null, supersede?: Supersede): Promise<TaskSource & { supersedes?: SourceHolder }> {
  const body = {
    kind: source.kind, repositoryId: source.repositoryId, taskId: taskId ?? "", ...(source.mode ? { mode: source.mode } : {}),
    ...(supersede === true ? { supersede: true } : supersede ? { supersedeTask: supersede } : {}),
    ...(source.kind === "pr" ? { pullRequest: source.pullRequest } : { branch: source.branch, base: source.base ?? "" }),
  };
  const res = await orchestrator(ctx.principal.organizationId, "POST",
    `/internal/projects/${encodeURIComponent(projectId)}/sources/check`, JSON.stringify(body), ctx.principal);
  const out = (await res.json().catch(() => ({}))) as Record<string, unknown> & { error?: { code?: string; message?: string; details?: unknown } };
  if (!res.ok) {
    throw new HttpError(res.status, out.error?.message ?? "GitHub could not be asked about the source", out.error?.code ?? "error",
      out.error?.details);
  }
  const found = taskSourceModeSchema.safeParse(out.mode);
  return {
    kind: out.kind as TaskSource["kind"], repositoryId: out.repositoryId as string, repository: out.repository as string,
    ...(typeof out.number === "number" ? { number: out.number, title: (out.title as string) ?? "" } : {}),
    headBranch: out.headBranch as string, baseBranch: out.baseBranch as string,
    mode: source.mode ?? (found.success ? found.data : "review"),
    ...(out.supersedes ? { supersedes: out.supersedes as SourceHolder } : {}),
  };
}

/**
 * Supersede, as a person confirmed: the orchestrator stops the holder as
 * their Stop does, attaches the source to the task (made from `create`,
 * or `taskId`'s, not yet started) and links the two, in one transaction.
 * Its answer is the task's id.
 */
export async function supersedeWith(ctx: RequestContext, projectId: string, source: TaskSource & { supersedes?: SourceHolder },
  target: { taskId: string } | { create: Record<string, unknown> }): Promise<string> {
  const { supersedes, ...kept } = source;
  const res = await orchestrator(ctx.principal.organizationId, "POST",
    `/internal/projects/${encodeURIComponent(projectId)}/tasks/supersede`,
    JSON.stringify({ holder: supersedes!.id, source: kept, ...target }), ctx.principal);
  const out = (await res.json().catch(() => ({}))) as { taskId?: string; error?: { code?: string; message?: string; details?: unknown } };
  if (!res.ok) throw new HttpError(res.status, out.error?.message ?? "the task could not be superseded", out.error?.code ?? "error", out.error?.details);
  return out.taskId!;
}

/**
 * The source a request names is the one kept: a dialog sends every field,
 * and saving it unchanged is no change (and asks GitHub nothing). Its mode
 * is not part of where it is (sourceMode).
 */
export function sameSource(stored: TaskSource | null, given: TaskSourceInput | null): boolean {
  if (!stored || !given) return !stored && !given;
  if (stored.kind !== given.kind || stored.repositoryId !== given.repositoryId) return false;
  if (given.kind === "pr") {
    const n = /^#?(\d+)$/.exec(given.pullRequest.trim());
    return n !== null && Number(n[1]) === stored.number;
  }
  return given.branch === stored.headBranch && (given.base === undefined || given.base === stored.baseBranch);
}

/**
 * A resolved source the request named another way (a pull request's URL)
 * that is the stored one keeps the stored mode unless the request named one.
 */
export function keepMode(stored: TaskSource | null, given: TaskSourceInput, resolved: TaskSource): TaskSource {
  if (!stored || given.mode || stored.kind !== resolved.kind || stored.repositoryId !== resolved.repositoryId) return resolved;
  const same = resolved.kind === "pr" ? resolved.number === stored.number
    : resolved.headBranch === stored.headBranch && resolved.baseBranch === stored.baseBranch;
  return same ? { ...resolved, mode: stored.mode } : resolved;
}

/** The same source with another mode, when the request names one: else null (no change). */
export function sourceMode(stored: TaskSource, given: TaskSourceInput): TaskSource | null {
  return given.mode && given.mode !== stored.mode ? { ...stored, mode: given.mode } : null;
}

/**
 * Keep the task's source, replacing any: null clears it. The repository a
 * source is in is one the task works on, with write access, or read for
 * review only. Another live task holding its branch is refused
 * (superseded_required, naming it): superseding is the orchestrator's
 * (supersedeWith). The holder read is the orchestrator's branchHolder
 * (delivery/source.go): keep the two the same.
 */
export async function setTaskSource(scope: OrgScope, organizationId: string, taskId: string, source: TaskSource | null) {
  if (source) {
    await scope.sql`SELECT id FROM repositories WHERE id = ${source.repositoryId} FOR UPDATE`;
    const holders = (await scope.sql`
      SELECT t.id, p.key_prefix || '-' || t.number AS key,
        COALESCE((SELECT s.mode FROM task_sources s WHERE s.task_id = t.id), '') AS mode
      FROM tasks t JOIN projects p ON p.id = t.project_id
      WHERE t.id <> ${taskId} AND t.status NOT IN ('done', 'aborted', 'failed')
        AND (EXISTS (SELECT 1 FROM task_sources s WHERE s.task_id = t.id
                     AND s.repository_id = ${source.repositoryId} AND s.head_branch = ${source.headBranch})
          OR EXISTS (SELECT 1 FROM workflow_runs w JOIN task_repositories tr ON tr.task_id = w.task_id
                     WHERE w.task_id = t.id AND tr.repository_id = ${source.repositoryId}
                       AND w.status IN ('running', 'waiting') AND w.state->>'branch' = ${source.headBranch}))
      ORDER BY t.created_at LIMIT 1`) as SourceHolder[];
    const holder = holders[0];
    if (holder) {
      throw new HttpError(409, `${source.headBranch} is already the branch of ${holder.key}, which is still in progress: ` +
        `confirm to stop ${holder.key} and take the work over in this task`, "superseded_required", { holder });
    }
  }
  await scope.sql`DELETE FROM task_sources WHERE task_id = ${taskId}`;
  if (!source) return;
  const access = source.mode === "review_only" ? "read" : "write";
  await scope.sql`
    INSERT INTO task_sources (task_id, organization_id, repository_id, kind, number, title, head_branch, base_branch, mode)
    VALUES (${taskId}, ${organizationId}, ${source.repositoryId}, ${source.kind}, ${source.number ?? null},
            ${source.title ?? null}, ${source.headBranch}, ${source.baseBranch}, ${source.mode})`;
  await scope.sql`
    INSERT INTO task_repositories (organization_id, task_id, repository_id, access)
    VALUES (${organizationId}, ${taskId}, ${source.repositoryId}, ${access}::repository_access)
    ON CONFLICT (task_id, repository_id) DO UPDATE SET access = EXCLUDED.access`;
}
