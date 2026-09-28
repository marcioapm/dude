/**
 * Who may change what.
 *
 * Organization settings, the GitHub connection and a project's settings
 * are for the organization's admins (people.role, migration 035). There is
 * no project-level admin role yet: when there is, canEditProject is the one
 * place it lands.
 */

import type { RequestContext } from "./router.ts";
import { HttpError } from "./http.ts";

/**
 * Whether the caller administers their organization: their role as the
 * key's lookup read it for this request (migration 052), no query of its own.
 */
export async function isOrgAdmin(ctx: RequestContext): Promise<boolean> {
  return ctx.principal.role === "admin";
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
