/**
 * Who may change what.
 *
 * Organization settings, the GitHub connection and a project's settings
 * are for the organization's admins (people.role, migration 035). There is
 * no project-level admin role yet: when there is, canEditProject is the one
 * place it lands.
 */

import { withOrg } from "../db/client.ts";
import type { RequestContext } from "./router.ts";
import { HttpError } from "./http.ts";

/** Whether the caller administers their organization: a person on it, not removed, whose role is admin. */
export async function isOrgAdmin(ctx: RequestContext): Promise<boolean> {
  const rows = (await withOrg(ctx.principal.organizationId, (scope) => scope.sql`
    SELECT 1 FROM people WHERE id = ${ctx.principal.personId} AND role = 'admin' AND removed_at IS NULL`)) as unknown[];
  return rows.length > 0;
}

/** Whether the caller may change a project's settings: for now, an organization admin. */
export async function canEditProject(ctx: RequestContext, _projectId: string): Promise<boolean> {
  return isOrgAdmin(ctx);
}

export async function requireOrgAdmin(ctx: RequestContext): Promise<void> {
  if (!(await isOrgAdmin(ctx))) throw new HttpError(403, "only an organization admin can change this", "not_admin");
}

export async function requireProjectEditor(ctx: RequestContext, projectId: string): Promise<void> {
  if (!(await canEditProject(ctx, projectId))) throw new HttpError(403, "only an organization admin can change this", "not_admin");
}
