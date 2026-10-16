/**
 * What the tester showed: its verdict on each acceptance criterion, per
 * test Run, recorded by the orchestrator (acceptance_verdicts). Read only:
 * a waiver or acceptance is an escalation decision (POST /v1/tasks/:id/decide).
 *
 * A verdict covers the heads it tested. `stale` says what the branch
 * holds has moved past them since (the simplifier, a pull request fix):
 * what was shown working is not what the branch holds now. While a pull
 * request's fix is checked on its candidate branch (the delivery state's
 * `candidate`), the pull requests still hold the heads it started from
 * (`candidate.from`): staleness is measured against those, and a verdict
 * at the candidate's own heads is `candidate`, neither stale nor the pull
 * request's.
 */

import { withOrg } from "../../db/client.ts";
import { json } from "../http.ts";
import type { RequestContext, Router } from "../router.ts";

async function listVerification(ctx: RequestContext): Promise<Response> {
  const taskId = ctx.params.id!;
  const rows = await withOrg(ctx.principal.organizationId, async (scope) => (await scope.sql`
    SELECT v.run_id AS "runId", r.attempt, v.heads, v.created_at AS "createdAt", wk.work AS "work",
      v.criterion, v.text, v.status, v.evidence, v.observed, v.problem, v.decision,
      CASE WHEN p.id IS NULL THEN NULL ELSE json_build_object('id', p.id, 'name', p.name) END AS "decidedBy",
      v.decision_note AS "decisionNote"
    FROM acceptance_verdicts v JOIN runs r ON r.id = v.run_id
    -- Lateral rather than a scalar subquery: the planner memoizes it per (task, attempt).
    LEFT JOIN LATERAL (
      SELECT jsonb_build_object('heads', w.state->'heads', 'from', w.state->'candidate'->'from') AS work FROM workflow_runs w
      WHERE w.task_id = v.task_id AND COALESCE((w.state->>'attempt')::int, r.attempt) = r.attempt
      ORDER BY w.created_at DESC LIMIT 1) wk ON true
    LEFT JOIN people p ON p.id = v.decided_by
    WHERE v.task_id = ${taskId}
    ORDER BY v.created_at DESC, v.run_id, v.criterion
    LIMIT 500`) as Array<Record<string, unknown>>);

  const byRun = new Map<string, Record<string, unknown> & { criteria: unknown[] }>();
  for (const row of rows) {
    const runId = row.runId as string;
    let v = byRun.get(runId);
    if (!v) {
      const heads = (row.heads ?? {}) as Record<string, string>;
      const { stale, candidate } = standing(heads, (row.work ?? {}) as Work);
      v = { runId, attempt: row.attempt, heads, stale, candidate, createdAt: row.createdAt, criteria: [] };
      byRun.set(runId, v);
    }
    v.criteria.push({
      criterion: row.criterion, text: row.text, status: row.status, evidence: row.evidence ?? [], observed: row.observed,
      problem: row.problem, decision: row.decision ?? null, decidedBy: row.decidedBy ?? null, decisionNote: row.decisionNote ?? null,
    });
  }
  return json({ verifications: [...byRun.values()] });
}

/** The delivery's working heads, and the pull requests' while a candidate is checked. */
type Work = { heads?: Record<string, string> | null; from?: Record<string, string> | null };

/**
 * A verdict against what the branch holds: stale once it moved past the
 * verdict's heads; while a candidate is checked, the branch holds the
 * candidate's starting heads, and a verdict at the candidate's own heads
 * is the candidate's.
 */
export function standing(tested: Record<string, string>, work: Work): { stale: boolean; candidate: boolean } {
  const now = work.heads ?? {};
  if (!work.from) return { stale: isStale(tested, now), candidate: false };
  const candidate = isStale(work.from, now) && !isStale(tested, now);
  return { stale: !candidate && isStale(tested, work.from), candidate };
}

/** The work has moved past a verdict: a repository at another commit, or changed and never tested. */
export function isStale(tested: Record<string, string>, now: Record<string, string>): boolean {
  return Object.entries(now).some(([repo, sha]) => tested[repo] !== sha);
}

export function registerVerificationRoutes(router: Router): void {
  router.get("/v1/tasks/:id/verification", listVerification);
}
