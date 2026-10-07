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
  id, organization_id AS "organizationId", task_id AS "taskId",
  run_id AS "runId", prompt, options, status, answer,
  asked_at AS "askedAt", answered_at AS "answeredAt"`;

const DIRECTIVE_SELECT = `
  id, organization_id AS "organizationId", task_id AS "taskId",
  run_id AS "runId", text, scope, created_by AS "createdBy",
  created_at AS "createdAt", supersedes, delivered_at AS "deliveredAt"`;

/** Forward a run-control request, naming the person who made it. */
function forward(action: "steer" | "pause" | "resume" | "abort" | "restart" | "leave") {
  return async (ctx: RequestContext): Promise<Response> =>
    orchestrator(ctx.principal.organizationId, "POST", `/internal/runs/${ctx.params.id}/${action}`,
      await ctx.request.text(), ctx.principal);
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
 * Questions agents asked, newest first: those of a Run, of a task, or
 * every one still open in the organization — what needs a person.
 */
async function listQuestions(ctx: RequestContext): Promise<Response> {
  const url = new URL(ctx.request.url);
  const runId = url.searchParams.get("runId");
  const taskId = url.searchParams.get("taskId");
  const questions = await withOrg(ctx.principal.organizationId, async (scope) => {
    return (await scope.sql`
      SELECT ${scope.sql.unsafe(QUESTION_SELECT)} FROM questions
      WHERE (${runId}::text IS NULL OR run_id = ${runId})
        AND (${taskId}::text IS NULL OR task_id = ${taskId})
        AND (${runId}::text IS NOT NULL OR ${taskId}::text IS NOT NULL OR status = 'open')
      ORDER BY asked_at DESC LIMIT 200`) as Array<Record<string, unknown>>;
  });
  return json({ questions });
}

/** Answer a question: the orchestrator records it and gives it to the agent. */
async function answerQuestion(ctx: RequestContext): Promise<Response> {
  return orchestrator(ctx.principal.organizationId, "POST", `/internal/questions/${ctx.params.id}/answer`,
    await ctx.request.text(), ctx.principal);
}

/**
 * An agent's requests for repositories its task does not name: pending
 * ones wait for a person to approve or decline.
 */
async function listRepositoryRequests(ctx: RequestContext): Promise<Response> {
  const runId = ctx.url.searchParams.get("runId");
  const taskId = ctx.url.searchParams.get("taskId");
  const requests = await withOrg(ctx.principal.organizationId, async (scope) => {
    return (await scope.sql`
      SELECT q.id, q.run_id AS "runId", q.task_id AS "taskId", q.repository_id AS "repositoryId",
        r.name AS "repositoryName", q.access, q.reason, q.status, q.error, q.decided_at AS "decidedAt",
        q.created_at AS "createdAt"
      FROM repository_requests q JOIN repositories r ON r.id = q.repository_id
      WHERE (${runId}::text IS NULL OR q.run_id = ${runId})
        AND (${taskId}::text IS NULL OR q.task_id = ${taskId})
        AND (${runId}::text IS NOT NULL OR ${taskId}::text IS NOT NULL OR q.status = 'pending')
      ORDER BY q.created_at DESC LIMIT 200`) as Array<Record<string, unknown>>;
  });
  return json({ repositoryRequests: requests });
}

/** Approve or decline: the orchestrator records it and carries it out. */
async function decideRepositoryRequest(ctx: RequestContext): Promise<Response> {
  return orchestrator(ctx.principal.organizationId, "POST", `/internal/repository-requests/${ctx.params.id}/decide`,
    await ctx.request.text(), ctx.principal);
}

export function registerInterventionRoutes(router: Router): void {
  router.get("/v1/questions", listQuestions);
  router.post("/v1/questions/:id/answer", answerQuestion);
  router.get("/v1/repository-requests", listRepositoryRequests);
  router.post("/v1/repository-requests/:id/decide", decideRepositoryRequest);

  router.post("/v1/runs/:id/steer", forward("steer"));
  router.get("/v1/runs/:id/directives", listDirectives);

  router.post("/v1/runs/:id/pause", forward("pause"));
  router.post("/v1/runs/:id/resume", forward("resume"));
  router.post("/v1/runs/:id/abort", forward("abort"));
  // A stalled Run's banner: a fresh Run in its slot, or leave it as it is.
  router.post("/v1/runs/:id/restart", forward("restart"));
  router.post("/v1/runs/:id/leave", forward("leave"));
}
