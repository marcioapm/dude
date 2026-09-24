/**
 * The repositories a work item works on: none, one, or several, each one it
 * may change (`write`) or only read (`read`). None is work that changes no
 * code. In a project with one repository, a work item naming none is given
 * it when delivered (by the orchestrator), so the rule lives in one place
 * (docs/design/multi-repo.md).
 */

import { z } from "zod";
import { workItemRepositorySchema } from "@dude/domain";
import type { OrgScope } from "../../db/client.ts";

export const workItemRepositoriesInput = z
  .array(workItemRepositorySchema)
  .max(20)
  .refine((repos) => new Set(repos.map((r) => r.id)).size === repos.length, "a repository is named twice");

export type WorkItemRepositoriesInput = z.input<typeof workItemRepositoriesInput>;

/** The SELECT expression for a work item's repositories, as JSON, for `work_items` rows. */
export const REPOSITORIES_JSON = `COALESCE((
  SELECT json_agg(json_build_object('id', wr.repository_id, 'access', wr.access) ORDER BY r.name)
  FROM work_item_repositories wr JOIN repositories r ON r.id = wr.repository_id
  WHERE wr.work_item_id = work_items.id), '[]'::json) AS repositories`;

/**
 * Replace a work item's repositories. Returns the id of one that is not in
 * its project, or null when all were set.
 */
export async function setWorkItemRepositories(
  scope: OrgScope,
  organizationId: string,
  projectId: string,
  workItemId: string,
  repos: WorkItemRepositoriesInput,
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
  await scope.sql`DELETE FROM work_item_repositories WHERE work_item_id = ${workItemId}`;
  for (const r of repos) {
    await scope.sql`
      INSERT INTO work_item_repositories (organization_id, work_item_id, repository_id, access)
      VALUES (${organizationId}, ${workItemId}, ${r.id}, ${r.access ?? "write"}::repository_access)`;
  }
  return null;
}
