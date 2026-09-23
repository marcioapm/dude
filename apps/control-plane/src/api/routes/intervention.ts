/**
 * Human intervention routes — plan §24.
 *
 * Steer, pause, resume and abort change what runs, so the orchestrator
 * carries them out; these routes authenticate the user and forward. The
 * orchestrator records each as a durable domain object and a ledger event, so
 * a person redirecting an agent is as auditable as the agent's own actions.
 *
 * Reading directives stays here: it changes nothing.
 */

import { withOrg } from "../../db/client.ts";
import { json } from "../http.ts";
import { orchestrator } from "../../orchestrator/client.ts";
import type { RequestContext, Router } from "../router.ts";

const DIRECTIVE_SELECT = `
  id, organization_id AS "organizationId", work_item_id AS "workItemId",
  run_id AS "runId", text, scope, created_by AS "createdBy",
  created_at AS "createdAt", supersedes, delivered_at AS "deliveredAt"`;

/** Forward a run-control request, naming the person who made it. */
function forward(action: "steer" | "pause" | "resume" | "abort") {
  return async (ctx: RequestContext): Promise<Response> =>
    orchestrator(ctx.principal.organizationId, "POST", `/internal/runs/${ctx.params.id}/${action}`,
      await ctx.request.text(), ctx.principal.apiKeyId);
}

/** Directives issued for a Run, newest first. */
async function listDirectives(ctx: RequestContext): Promise<Response> {
  const runId = ctx.params.id!;
  const directives = await withOrg(ctx.principal.organizationId, async (scope) => {
    return (await scope.sql`
      SELECT ${scope.sql.unsafe(DIRECTIVE_SELECT)} FROM directives
      WHERE run_id = ${runId}
      ORDER BY created_at DESC`) as Array<Record<string, unknown>>;
  });
  return json({ directives });
}

export function registerInterventionRoutes(router: Router): void {
  router.post("/v1/runs/:id/steer", forward("steer"));
  router.get("/v1/runs/:id/directives", listDirectives);

  router.post("/v1/runs/:id/pause", forward("pause"));
  router.post("/v1/runs/:id/resume", forward("resume"));
  router.post("/v1/runs/:id/abort", forward("abort"));
}
