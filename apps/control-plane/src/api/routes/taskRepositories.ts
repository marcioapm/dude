/**
 * The repositories a task works on: none, one, or several, each one it
 * may change (`write`) or only read (`read`). None is work that changes no
 * code. In a project with one repository, a task naming none is given
 * it when delivered (by the orchestrator), so the rule lives in one place
 * (docs/design/multi-repo.md).
 */

import { z } from "zod";
import { taskRepositorySchema } from "@dude/domain";
import type { OrgScope } from "../../db/client.ts";

export const taskRepositoriesInput = z
  .array(taskRepositorySchema)
  .max(20)
  .refine((repos) => new Set(repos.map((r) => r.id)).size === repos.length, "a repository is named twice");

export type TaskRepositoriesInput = z.input<typeof taskRepositoriesInput>;

/** The SELECT expression for a task's repositories, as JSON, for `tasks` rows. */
export const REPOSITORIES_JSON = `COALESCE((
  SELECT json_agg(json_build_object('id', wr.repository_id, 'access', wr.access) ORDER BY r.name)
  FROM task_repositories wr JOIN repositories r ON r.id = wr.repository_id
  WHERE wr.task_id = tasks.id), '[]'::json) AS repositories`;

/**
 * Replace a task's repositories. Returns the id of one that is not in
 * its project, or null when all were set.
 */
export async function setTaskRepositories(
  scope: OrgScope,
  organizationId: string,
  projectId: string,
  taskId: string,
  repos: TaskRepositoriesInput,
): Promise<string | null> {
  const ids = repos.map((r) => r.id);
  if (ids.length > 0) {
    const found = (await scope.sql`
      SELECT id FROM repositories WHERE project_id = ${projectId}
        AND id IN (SELECT jsonb_array_elements_text(${ids}::jsonb))`) as Array<{ id: string }>;
    const known = new Set(found.map((r) => r.id));
    const missing = ids.find((id) => !known.has(id));
    if (missing) return missing;
  }
  await scope.sql`DELETE FROM task_repositories WHERE task_id = ${taskId}`;
  for (const r of repos) {
    await scope.sql`
      INSERT INTO task_repositories (organization_id, task_id, repository_id, access)
      VALUES (${organizationId}, ${taskId}, ${r.id}, ${r.access ?? "write"}::repository_access)`;
  }
  return null;
}
