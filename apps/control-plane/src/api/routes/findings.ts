/**
 * Review findings: what a reviewer reports, and what the loop acts on.
 *
 * A finding is a row rather than a passage of prose, because the delivery
 * workflow has to decide things about it — does it block, has the fixer
 * already failed at it twice — and a paragraph cannot be queried. The
 * reviewer describes what it found; policy decides what that means
 * (plan §11.2).
 *
 * Reported by the runner on behalf of a review Run, which is why these
 * routes accept a runner key: the agent produced the text, the runner parsed
 * it, and the control plane is the only thing that decides what happens next.
 */

import { z } from "zod";
import { EventTypes, findingSeveritySchema, newId } from "@dude/domain";
import { withOrg } from "../../db/client.ts";
import { appendInScope } from "../../events/ledger.ts";
import { eventBus } from "../../events/bus.ts";
import { badRequest, json, notFound, parseBody } from "../http.ts";
import type { RequestContext, Router } from "../router.ts";

const FINDING_SELECT = `
  id, organization_id AS "organizationId", work_item_id AS "workItemId",
  run_id AS "runId", category, severity, status, repo, file, line,
  title, description, suggested_fix AS "suggestedFix",
  resolved_by_run_id AS "resolvedByRunId", resolution_note AS "resolutionNote",
  fix_attempts AS "fixAttempts",
  created_at AS "createdAt", updated_at AS "updatedAt"`;

const findingInput = z.object({
  severity: findingSeveritySchema,
  category: z.string().min(1).max(50),
  title: z.string().min(1).max(300),
  description: z.string().max(10_000).default(""),
  suggestedFix: z.string().max(10_000).default(""),
  repo: z.string().max(200).nullable().default(null),
  file: z.string().max(500).nullable().default(null),
  line: z.number().int().positive().nullable().default(null),
});

/**
 * A whole review's findings in one call.
 *
 * All of them together rather than one per request: a review either reported
 * or it did not, and a partial set would let the workflow act on half a
 * review if the runner died mid-report.
 */
const reportInput = z.object({
  findings: z.array(findingInput).max(100),
});

async function reportFindings(ctx: RequestContext): Promise<Response> {
  const runId = ctx.params.id!;
  const input = await parseBody(ctx.request, reportInput);
  const { organizationId } = ctx.principal;

  const result = await withOrg(organizationId, async (scope) => {
    const runs = (await scope.sql`
      SELECT id, project_id, work_item_id, phase, category FROM runs WHERE id = ${runId}`) as Array<{
      id: string;
      project_id: string;
      work_item_id: string;
      phase: string | null;
      category: string | null;
    }>;
    const run = runs[0];
    if (!run) return { missing: true as const };

    /*
     * Only a review or test Run may report findings. A fix Run reporting
     * them would let an implementer manufacture the evidence that its own
     * work is finished.
     */
    if (run.phase !== "review" && run.phase !== "test") {
      return { wrongPhase: run.phase ?? "none" };
    }

    // Reporting twice replaces rather than accumulates: a retried report
    // must not double a review's findings.
    await scope.sql`DELETE FROM review_findings WHERE run_id = ${runId}`;

    /*
     * A re-review resolves what an earlier review of the same kind raised.
     *
     * This is what lets the loop converge. Without it a finding from the
     * first review stays open forever — a clean second review reports
     * nothing, and "nothing" cannot close a row it never mentions — so the
     * loop can only ever end at its policy bound, having proved only that
     * the bound works.
     *
     * Scoped to the same category: a security re-review saying nothing is
     * not evidence that a correctness finding was fixed. And only findings a
     * fix Run has already attempted, because a finding the fixer has not
     * touched yet was not resolved by anything — the re-review simply did
     * not raise it again, which a flaky reviewer does all the time.
     *
     * Findings this review raises *again* are inserted fresh below, so a
     * problem that survived the fix reappears as open rather than being
     * quietly closed here.
     */
    const categories = [...new Set(input.findings.map((f) => f.category))];
    const covered = run.category ? [run.category, ...categories] : categories;

    if (covered.length > 0 && run.phase === "review") {
      await scope.sql`
        UPDATE review_findings SET
          status = 'resolved',
          resolved_by_run_id = ${runId},
          resolution_note = 'not raised again by a re-review after a fix',
          updated_at = now()
        WHERE work_item_id = ${run.work_item_id}
          AND run_id <> ${runId}
          AND status = 'open'
          AND fix_attempts > 0
          AND category IN ${scope.sql(covered)}`;
    }

    const rows: Array<Record<string, unknown>> = [];
    for (const finding of input.findings) {
      const inserted = (await scope.sql`
        INSERT INTO review_findings (
          id, organization_id, work_item_id, run_id, category, severity,
          repo, file, line, title, description, suggested_fix)
        VALUES (
          ${newId("finding")}, ${organizationId}, ${run.work_item_id}, ${runId},
          ${finding.category}, ${finding.severity}::finding_severity,
          ${finding.repo}, ${finding.file}, ${finding.line},
          ${finding.title}, ${finding.description ?? ""}, ${finding.suggestedFix ?? ""})
        RETURNING ${scope.sql.unsafe(FINDING_SELECT)}`) as Array<Record<string, unknown>>;
      rows.push(inserted[0]!);
    }

    const event = await appendInScope(scope, {
      eventType: EventTypes.ReviewCompleted,
      organizationId,
      projectId: run.project_id,
      workItemId: run.work_item_id,
      runId,
      actor: { type: "agent", id: runId },
      source: "runner",
      correlationId: run.work_item_id,
      payload: {
        phase: run.phase,
        count: input.findings.length,
        bySeverity: countBy(input.findings, (f) => f.severity),
      },
    });

    return { findings: rows, event };
  });

  if ("missing" in result) throw notFound(`run ${runId} not found`);
  if ("wrongPhase" in result) {
    throw badRequest(`a ${result.wrongPhase} run may not report review findings`);
  }

  eventBus.publish(result.event);
  return json({ findings: result.findings }, 201);
}

async function listFindings(ctx: RequestContext): Promise<Response> {
  const workItemId = ctx.url.searchParams.get("workItemId");
  const runId = ctx.url.searchParams.get("runId");
  const status = ctx.url.searchParams.get("status");

  const findings = await withOrg(ctx.principal.organizationId, async (scope) => {
    return (await scope.sql`
      SELECT ${scope.sql.unsafe(FINDING_SELECT)} FROM review_findings
      WHERE (${workItemId}::text IS NULL OR work_item_id = ${workItemId})
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
      workItemId: finding.workItemId as string,
      runId: (finding.runId as string | null) ?? null,
      actor: { type: "human", id: ctx.principal.apiKeyId },
      source: "control-plane",
      correlationId: finding.workItemId as string,
      payload: { status: input.status, note: input.note, title: finding.title },
    });

    return { finding, event };
  });

  if ("missing" in result) throw notFound(`finding ${id} not found`);
  eventBus.publish(result.event);
  return json(result.finding);
}

function countBy<T>(items: readonly T[], key: (item: T) => string): Record<string, number> {
  const counts: Record<string, number> = {};
  for (const item of items) {
    const k = key(item);
    counts[k] = (counts[k] ?? 0) + 1;
  }
  return counts;
}

export function registerFindingRoutes(router: Router): void {
  // Reported by the runner on the review Run's behalf.
  router.post("/v1/runs/:id/findings", reportFindings, { requireKind: "runner" });
  router.get("/v1/findings", listFindings);
  router.post("/v1/findings/:id/resolve", resolveFinding);
}
