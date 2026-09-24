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

const QUESTION_SELECT = `
  id, organization_id AS "organizationId", work_item_id AS "workItemId",
  run_id AS "runId", prompt, options, status, answer,
  asked_at AS "askedAt", answered_at AS "answeredAt"`;

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

/**
 * Questions agents asked, newest first: those of a Run, of a work item, or
 * every one still open in the organization — what needs a person.
 */
async function listQuestions(ctx: RequestContext): Promise<Response> {
  const url = new URL(ctx.request.url);
  const runId = url.searchParams.get("runId");
  const workItemId = url.searchParams.get("workItemId");
  const questions = await withOrg(ctx.principal.organizationId, async (scope) => {
    return (await scope.sql`
      SELECT ${scope.sql.unsafe(QUESTION_SELECT)} FROM questions
      WHERE (${runId}::text IS NULL OR run_id = ${runId})
        AND (${workItemId}::text IS NULL OR work_item_id = ${workItemId})
        AND (${runId}::text IS NOT NULL OR ${workItemId}::text IS NOT NULL OR status = 'open')
      ORDER BY asked_at DESC LIMIT 200`) as Array<Record<string, unknown>>;
  });
  return json({ questions });
}

/** Answer a question: the orchestrator records it and gives it to the agent. */
async function answerQuestion(ctx: RequestContext): Promise<Response> {
  return orchestrator(ctx.principal.organizationId, "POST", `/internal/questions/${ctx.params.id}/answer`,
    await ctx.request.text(), ctx.principal.apiKeyId);
}

export function registerInterventionRoutes(router: Router): void {
  router.get("/v1/questions", listQuestions);
  router.post("/v1/questions/:id/answer", answerQuestion);

  router.post("/v1/runs/:id/steer", forward("steer"));
  router.get("/v1/runs/:id/directives", listDirectives);

  router.post("/v1/runs/:id/pause", forward("pause"));
  router.post("/v1/runs/:id/resume", forward("resume"));
  router.post("/v1/runs/:id/abort", forward("abort"));
}
