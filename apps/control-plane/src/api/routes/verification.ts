/**
 * What the tester showed: its verdict on each acceptance criterion, per
 * test Run, recorded by the orchestrator (acceptance_verdicts). Read only:
 * a waiver or acceptance is an escalation decision (POST /v1/tasks/:id/decide).
 *
 * A verdict covers the heads it tested. `stale` says the task's delivery
 * of that attempt has moved past them since (the simplifier, a pull
 * request fix): what was shown working is not what the branch holds now.
 */

import { withOrg } from "../../db/client.ts";
import { json } from "../http.ts";
import type { RequestContext, Router } from "../router.ts";

async function listVerification(ctx: RequestContext): Promise<Response> {
  const taskId = ctx.params.id!;
  const rows = await withOrg(ctx.principal.organizationId, async (scope) => (await scope.sql`
    SELECT v.run_id AS "runId", r.attempt, v.heads, v.created_at AS "createdAt",
      COALESCE((SELECT w.state->'heads' FROM workflow_runs w WHERE w.task_id = v.task_id
        AND COALESCE((w.state->>'attempt')::int, r.attempt) = r.attempt ORDER BY w.created_at DESC LIMIT 1), '{}') AS "now",
      v.criterion, v.text, v.status, v.evidence, v.observed, v.problem, v.decision,
      CASE WHEN p.id IS NULL THEN NULL ELSE json_build_object('id', p.id, 'name', p.name) END AS "decidedBy",
      v.decision_note AS "decisionNote"
    FROM acceptance_verdicts v JOIN runs r ON r.id = v.run_id LEFT JOIN people p ON p.id = v.decided_by
    WHERE v.task_id = ${taskId}
    ORDER BY v.created_at DESC, v.run_id, v.criterion
    LIMIT 500`) as Array<Record<string, unknown>>);

  const byRun = new Map<string, Record<string, unknown> & { criteria: unknown[] }>();
  for (const row of rows) {
    const runId = row.runId as string;
    let v = byRun.get(runId);
    if (!v) {
      const heads = (row.heads ?? {}) as Record<string, string>;
      const now = (row.now ?? {}) as Record<string, string>;
      v = { runId, attempt: row.attempt, heads, stale: isStale(heads, now), createdAt: row.createdAt, criteria: [] };
      byRun.set(runId, v);
    }
    v.criteria.push({
      criterion: row.criterion, text: row.text, status: row.status, evidence: row.evidence ?? [], observed: row.observed,
      problem: row.problem, decision: row.decision ?? null, decidedBy: row.decidedBy ?? null, decisionNote: row.decisionNote ?? null,
    });
  }
  return json({ verifications: [...byRun.values()] });
}

/** The work has moved past a verdict: a repository at another commit, or changed and never tested. */
export function isStale(tested: Record<string, string>, now: Record<string, string>): boolean {
  return Object.entries(now).some(([repo, sha]) => tested[repo] !== sha);
}

export function registerVerificationRoutes(router: Router): void {
  router.get("/v1/tasks/:id/verification", listVerification);
}
