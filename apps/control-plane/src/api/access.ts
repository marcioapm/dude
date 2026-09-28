/**
 * Who may change what.
 *
 * Organization settings are for organization admins; a project's settings
 * for them and the project's own admins. People and roles land with the
 * people track (migration 035): until then every key is an admin, and these
 * two functions are the only places that will change when they do.
 */

import type { RequestContext } from "./router.ts";
import { forbidden } from "./http.ts";

/** Whether the caller administers their organization. */
export async function isOrgAdmin(_ctx: RequestContext): Promise<boolean> {
  return true;
}

/** Whether the caller may change a project's settings: an organization admin, or one of the project's admins. */
export async function canEditProject(ctx: RequestContext, _projectId: string): Promise<boolean> {
  return isOrgAdmin(ctx);
}

export async function requireOrgAdmin(ctx: RequestContext): Promise<void> {
  if (!(await isOrgAdmin(ctx))) throw forbidden("only an organization admin can change this");
}

export async function requireProjectEditor(ctx: RequestContext, projectId: string): Promise<void> {
  if (!(await canEditProject(ctx, projectId))) throw forbidden("only an organization or project admin can change this");
}
