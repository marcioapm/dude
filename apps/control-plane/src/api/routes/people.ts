/**
 * The organization's people, and who drives each task.
 *
 * Until there are users (plan §53) a person is a user API key, by its name:
 * the owner of a task is one, and moves to a user when sign-in lands. The
 * owner is who is told when the task waits on someone, and the only one
 * who answers its agents (the orchestrator enforces that).
 */

import type { OrgScope } from "../../db/client.ts";
import { withOrg } from "../../db/client.ts";
import { json } from "../http.ts";
import type { RequestContext, Router } from "../router.ts";

/**
 * The SELECT expression for a task's owner, `{id, name}` or null, for `tasks`
 * rows. A revoked key can no longer answer, so its task is nobody's — as the
 * orchestrator sees it too.
 */
export const OWNER_JSON = `(SELECT json_build_object('id', k.id, 'name', k.name) FROM api_keys k
  WHERE k.id = tasks.owner_key_id AND k.revoked_at IS NULL) AS owner`;

/** Whether a key is one of the organization's people: a user key, not revoked. */
export async function isPerson(scope: OrgScope, keyId: string): Promise<boolean> {
  const rows = await scope.sql`
    SELECT 1 FROM api_keys WHERE id = ${keyId} AND kind = 'user' AND revoked_at IS NULL`;
  return rows.length > 0;
}

/** Everyone a task could be handed to, by name. */
async function listPeople(ctx: RequestContext): Promise<Response> {
  const people = await withOrg(ctx.principal.organizationId, async (scope) => {
    return await scope.sql`
      SELECT id, name FROM api_keys WHERE kind = 'user' AND revoked_at IS NULL ORDER BY name, created_at`;
  });
  return json({ people, you: ctx.principal.apiKeyId });
}

export function registerPeopleRoutes(router: Router): void {
  router.get("/v1/people", listPeople);
}
