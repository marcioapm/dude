/**
 * Bridge finished phase Runs to the workflows waiting on them.
 *
 * The delivery workflow parks on a `phase.finished` signal rather than polling
 * Run status, which is what makes waiting free (plan §21). Something has to
 * notice a Run reached a terminal state and deliver that signal.
 *
 * A sweeper rather than a hook on the status update, for one reason that
 * matters: a Run abandoned by a dead worker is terminated by the lease
 * reaper, not by anything that would fire a hook. A workflow waiting on that
 * Run must not hang because the node that owned it disappeared without
 * reporting.
 */

import { withSystemScope } from "../db/client.ts";
import { Signals } from "./delivery.workflow.ts";
import type { PostgresWorkflowRuntime } from "./runtime.ts";
import type { SweepResult } from "./sweepers.ts";

/** How many finished Runs to notify per sweep. */
const BATCH = 50;

interface FinishedRun {
  id: string;
  organization_id: string;
  status: string;
  workflow_run_id: string;
}

/**
 * Signal every workflow whose phase Run has finished but not yet been
 * reported.
 *
 * Cross-tenant, because a sweeper serves every organization — hence
 * `withSystemScope` rather than `withOrg`. The signal itself is delivered
 * with the Run's own organization, so the runtime's tenant check still has
 * something to verify.
 *
 * Idempotency comes from the signal's key: the runtime drops a duplicate, so
 * a sweep that crashes between signalling and marking is safe to repeat.
 */
export async function notifyPhaseFinished(
  workflow: PostgresWorkflowRuntime,
): Promise<SweepResult> {
  const finished = await withSystemScope<FinishedRun[]>("phase-notifier", async ({ sql }) => {
    return (await sql`
      SELECT r.id, r.organization_id, r.status, w.id AS workflow_run_id
      FROM runs r
      JOIN workflow_runs w
        ON w.work_item_id = r.work_item_id
       AND w.organization_id = r.organization_id
      WHERE r.phase IS NOT NULL
        AND r.status IN ('completed', 'failed', 'aborted')
        AND r.phase_notified_at IS NULL
        AND w.status = 'waiting'
      ORDER BY r.ended_at
      LIMIT ${BATCH}`) as FinishedRun[];
  });

  let handled = 0;
  for (const run of finished) {
    try {
      await workflow.signal(
        run.organization_id,
        run.workflow_run_id,
        Signals.PhaseFinished,
        { runId: run.id, status: run.status },
        // One signal per Run per workflow, however many times this sweeps.
        `phase-finished:${run.id}`,
      );

      await withSystemScope("phase-notifier", async ({ sql }) => {
        await sql`UPDATE runs SET phase_notified_at = now() WHERE id = ${run.id}`;
      });
      handled += 1;
    } catch (err) {
      // One workflow that cannot be signalled must not stall the rest; the
      // next sweep retries it, and the Run stays unmarked until it works.
      console.error("phase notification failed", { runId: run.id, error: String(err) });
    }
  }

  return { handled };
}
