/**
 * Review findings: what a reviewer reports, and what the loop acts on.
 *
 * A finding is a row rather than a passage of prose, because the delivery
 * workflow has to decide things about it — does it block, has the fixer
 * already failed at it twice — and a paragraph cannot be queried. The
 * reviewer describes what it found; policy decides what that means
 * (plan §11.2).
 *
 * Recorded by the orchestrator from what a reviewer said; here a person
 * lists them, and may accept or reopen one.
 */

import { z } from "zod";
import { EventTypes } from "@dude/domain";
import { withOrg } from "../../db/client.ts";
import { appendInScope } from "../../events/ledger.ts";
import { json, notFound, parseBody } from "../http.ts";
import { auditActor } from "../auth.ts";
import { orchestrator } from "../../orchestrator/client.ts";
import type { RequestContext, Router } from "../router.ts";

const FINDING_SELECT = `
  id, organization_id AS "organizationId", task_id AS "taskId",
  run_id AS "runId", category, topic, severity, status, repo, file, line,
  title, description, suggested_fix AS "suggestedFix", acceptance,
  resolved_by_run_id AS "resolvedByRunId", resolution_note AS "resolutionNote",
  fix_attempts AS "fixAttempts",
  trivial, introduced, evidence_kind AS "evidenceKind", evidence, scenario, unverified,
  created_at AS "createdAt", updated_at AS "updatedAt"`;


async function listFindings(ctx: RequestContext): Promise<Response> {
  const taskId = ctx.url.searchParams.get("taskId");
  const runId = ctx.url.searchParams.get("runId");
  const status = ctx.url.searchParams.get("status");

  const findings = await withOrg(ctx.principal.organizationId, async (scope) => {
    return (await scope.sql`
      SELECT ${scope.sql.unsafe(FINDING_SELECT)} FROM review_findings
      WHERE (${taskId}::text IS NULL OR task_id = ${taskId})
        AND (${runId}::text IS NULL OR run_id = ${runId})
        AND (${status}::text IS NULL OR status::text = ${status})
      ORDER BY
        CASE severity
          WHEN 'blocking' THEN 1 WHEN 'high' THEN 2 WHEN 'medium' THEN 3
          WHEN 'low' THEN 4 ELSE 5
        END,
        created_at
      LIMIT 500`) as Array<Record<string, unknown>>;
  });

  return json({ findings });
}

const resolveInput = z.object({
  status: z.enum(["resolved", "accepted", "open"]),
  note: z.string().max(2000).default(""),
});

/**
 * Change a finding's status.
 *
 * `accepted` is the interesting one: a human deciding to ship a known
 * problem. Recorded as a decision with a note rather than by deleting the
 * finding, so the next person to read the code can see it was considered.
 */
async function resolveFinding(ctx: RequestContext): Promise<Response> {
  const id = ctx.params.id!;
  const input = await parseBody(ctx.request, resolveInput);
  const { organizationId } = ctx.principal;

  const result = await withOrg(organizationId, async (scope) => {
    const rows = (await scope.sql`
      UPDATE review_findings SET
        status = ${input.status}::finding_status,
        resolution_note = ${input.note ?? ""},
        updated_at = now()
      WHERE id = ${id}
      RETURNING ${scope.sql.unsafe(FINDING_SELECT)}`) as Array<Record<string, unknown>>;
    const finding = rows[0];
    if (!finding) return { missing: true as const };

    const event = await appendInScope(scope, {
      eventType: EventTypes.FindingResolved,
      organizationId,
      projectId: null,
      taskId: finding.taskId as string,
      runId: (finding.runId as string | null) ?? null,
      actor: { type: auditActor(ctx.principal).kind, id: auditActor(ctx.principal).id },
      source: "control-plane",
      correlationId: finding.taskId as string,
      payload: { status: input.status, note: input.note, title: finding.title },
    });

    return { finding, event };
  });

  if ("missing" in result) throw notFound(`finding ${id} not found`);
  return json(result.finding);
}


/**
 * A task's reviewer questions (review_questions), oldest first. The
 * orchestrator records them from a reviewer's reply, and an answer or a
 * withdrawal. A task another organization owns is not found, as RLS hides it.
 */
async function listReviewQuestions(ctx: RequestContext): Promise<Response> {
  const taskId = ctx.params.id!;
  const questions = await withOrg(ctx.principal.organizationId, async (scope) => {
    const [task] = await scope.sql`SELECT id FROM tasks WHERE id = ${taskId}`;
    if (!task) return null;
    return (await scope.sql`
      SELECT id, task_id AS "taskId", run_id AS "runId", category, finding_id AS "findingId", question,
        why_it_matters AS "whyItMatters", evidence_checked AS "evidenceChecked", needed_from AS "neededFrom",
        status, answer,
        CASE WHEN status = 'open' THEN NULL WHEN answered_by_run IS NOT NULL THEN 'conductor' ELSE 'person' END AS "answeredBy",
        answered_at AS "answeredAt", created_at AS "createdAt"
      FROM review_questions WHERE task_id = ${taskId}
      ORDER BY created_at, run_id, n
      LIMIT 500`) as Array<Record<string, unknown>>;
  });
  if (!questions) throw notFound(`task ${taskId} not found`);
  return json({ questions });
}

/**
 * Answer (`{answer}`) or withdraw (`{reason}`) a reviewer's question: the
 * orchestrator settles it, the task's owner only, and its answer is what
 * the API returns. A question this organization cannot see is not found here.
 */
function settleReviewQuestion(verb: "answer" | "withdraw") {
  return async (ctx: RequestContext): Promise<Response> => {
    const id = ctx.params.id!;
    const seen = await withOrg(ctx.principal.organizationId, (scope) => scope.sql`SELECT id FROM review_questions WHERE id = ${id}`);
    if (seen.length === 0) throw notFound(`reviewer question ${id} not found`);
    return orchestrator(ctx.principal.organizationId, "POST", `/internal/review-questions/${encodeURIComponent(id)}/${verb}`,
      await ctx.request.text(), ctx.principal);
  };
}

export function registerFindingRoutes(router: Router): void {
  router.get("/v1/findings", listFindings);
  router.post("/v1/findings/:id/resolve", resolveFinding);
  router.get("/v1/tasks/:id/review-questions", listReviewQuestions);
  router.post("/v1/review-questions/:id/answer", settleReviewQuestion("answer"));
  router.post("/v1/review-questions/:id/withdraw", settleReviewQuestion("withdraw"));
}
