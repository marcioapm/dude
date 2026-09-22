/**
 * Background sweepers — the cross-tenant work the control plane must do.
 *
 * Plan §21 promises that waiting is free: a workflow parked on a human
 * question or a webhook holds no process and spends no tokens. That promise
 * only holds if something wakes it when the wait ends, and reclaims work from
 * components that disappear. These are those somethings.
 *
 * Each sweeper is a named loop that claims across all tenants, does a bounded
 * amount of work, and sleeps. They share one supervisor so the control plane
 * starts and stops them as a unit.
 */

import { withOrg, withSystemScope, type SweeperName } from "../db/client.ts";
import { appendInScope } from "../events/ledger.ts";
import { eventBus } from "../events/bus.ts";
import { EventTypes, newId } from "@dude/domain";

export interface SweeperOptions {
  /** How often the loop runs when it finds nothing to do. */
  intervalMs?: number;
  /** Upper bound on rows claimed per pass, so one tenant cannot monopolize. */
  batchSize?: number;
  log?: (message: string, detail?: Record<string, unknown>) => void;
}

/** What one pass accomplished, so a caller can loop until idle. */
export interface SweepResult {
  handled: number;
}

const DEFAULT_INTERVAL_MS = 1_000;
const DEFAULT_BATCH = 20;

/**
 * Reclaim Runs whose worker stopped renewing their lease.
 *
 * A worker that crashes or is killed leaves its Runs in `scheduled` or
 * `running` forever. The lease is what distinguishes "still working" from
 * "gone", so an expired one means the Run must be failed and its capacity
 * released — otherwise the work item waits on a worker that no longer exists.
 */
export async function reapExpiredRunLeases(batchSize = DEFAULT_BATCH): Promise<SweepResult> {
  const expired = await withSystemScope("run-lease-reaper", async ({ sql }) => {
    return (await sql`
      WITH candidate AS (
        SELECT id FROM runs
        WHERE lease_expires_at IS NOT NULL
          AND lease_expires_at < now()
          AND status IN ('scheduled', 'starting', 'running')
        ORDER BY lease_expires_at
        LIMIT ${batchSize}
        FOR UPDATE SKIP LOCKED
      )
      UPDATE runs r
      SET status = 'failed',
          error = 'worker lease expired; the run was abandoned',
          ended_at = now(),
          lease_expires_at = NULL
      FROM candidate
      WHERE r.id = candidate.id
      RETURNING r.id, r.organization_id, r.project_id, r.work_item_id, r.worker_id`) as Array<{
      id: string;
      organization_id: string;
      project_id: string;
      work_item_id: string;
      worker_id: string | null;
    }>;
  });

  // Record each reclamation in the owning tenant's ledger: a Run that failed
  // for infrastructure reasons must be as inspectable as one that failed on
  // its own merits.
  for (const run of expired) {
    const event = await withOrg(run.organization_id, (scope) =>
      appendInScope(scope, {
        eventType: EventTypes.RunFailed,
        organizationId: run.organization_id,
        projectId: run.project_id,
        workItemId: run.work_item_id,
        runId: run.id,
        actor: { type: "system", id: "run-lease-reaper" },
        source: "control-plane",
        payload: { reason: "lease_expired", workerId: run.worker_id },
      }),
    );
    eventBus.publish(event);
  }

  return { handled: expired.length };
}

/**
 * Mark workers that stopped heartbeating as lost.
 *
 * Distinct from reaping their Runs: a worker can be gone while its Runs are
 * still within lease, and capacity accounting needs to stop counting it
 * immediately rather than waiting for the longest lease to expire.
 */
export async function reapLostWorkers(
  staleAfterSeconds = 60,
  batchSize = DEFAULT_BATCH,
): Promise<SweepResult> {
  const lost = await withSystemScope("worker-liveness-reaper", async ({ sql }) => {
    return (await sql`
      WITH candidate AS (
        SELECT id FROM workers
        WHERE status IN ('ready', 'draining')
          AND last_heartbeat_at < now() - ${`${staleAfterSeconds} seconds`}::interval
        ORDER BY last_heartbeat_at
        LIMIT ${batchSize}
        FOR UPDATE SKIP LOCKED
      )
      UPDATE workers w
      SET status = 'lost', active_runs = 0
      FROM candidate
      WHERE w.id = candidate.id
      RETURNING w.id, w.organization_id, w.name`) as Array<{
      id: string;
      organization_id: string | null;
      name: string;
    }>;
  });

  for (const worker of lost) {
    // Shared workers have no organization, so there is no tenant ledger to
    // write to; the status change is the record in that case.
    if (!worker.organization_id) continue;

    const event = await withOrg(worker.organization_id, (scope) =>
      appendInScope(scope, {
        eventType: EventTypes.WorkerLost,
        organizationId: worker.organization_id!,
        actor: { type: "system", id: "worker-liveness-reaper" },
        source: "control-plane",
        payload: { workerId: worker.id, name: worker.name, staleAfterSeconds },
      }),
    );
    eventBus.publish(event);
  }

  return { handled: lost.length };
}

/**
 * Dispatch the transactional outbox.
 *
 * Side effects are committed with the state change that caused them and sent
 * afterwards, at-least-once. Without a dispatcher they are written and never
 * sent, which is worse than not having an outbox at all.
 */
export type OutboxHandler = (entry: {
  id: number;
  organizationId: string;
  workflowRunId: string | null;
  kind: string;
  payload: Record<string, unknown>;
}) => Promise<void>;

export async function dispatchOutbox(
  handlers: Record<string, OutboxHandler>,
  batchSize = DEFAULT_BATCH,
): Promise<SweepResult> {
  const pending = await withSystemScope("outbox-dispatcher", async ({ sql }) => {
    return (await sql`
      WITH candidate AS (
        SELECT id FROM workflow_outbox
        WHERE dispatched_at IS NULL AND next_attempt_at <= now()
        ORDER BY next_attempt_at
        LIMIT ${batchSize}
        FOR UPDATE SKIP LOCKED
      )
      UPDATE workflow_outbox o
      SET attempts = o.attempts + 1
      FROM candidate
      WHERE o.id = candidate.id
      RETURNING o.id, o.organization_id, o.workflow_run_id, o.kind, o.payload, o.attempts`) as Array<{
      id: number;
      organization_id: string;
      workflow_run_id: string | null;
      kind: string;
      payload: Record<string, unknown>;
      attempts: number;
    }>;
  });

  for (const entry of pending) {
    const handler = handlers[entry.kind];

    try {
      if (!handler) throw new Error(`no handler for outbox kind "${entry.kind}"`);
      await handler({
        id: entry.id,
        organizationId: entry.organization_id,
        workflowRunId: entry.workflow_run_id,
        kind: entry.kind,
        payload: entry.payload ?? {},
      });

      await withSystemScope("outbox-dispatcher", async ({ sql }) => {
        await sql`UPDATE workflow_outbox SET dispatched_at = now(), last_error = NULL
                  WHERE id = ${entry.id}`;
      });
    } catch (err) {
      // Retry with the same backoff shape the workflow runtime uses, so a
      // failing integration does not spin.
      const message = err instanceof Error ? err.message : String(err);
      const delayMs = Math.min(1000 * 2 ** Math.max(0, entry.attempts - 1), 60_000);

      await withSystemScope("outbox-dispatcher", async ({ sql }) => {
        await sql`
          UPDATE workflow_outbox
          SET last_error = ${message},
              next_attempt_at = now() + ${`${delayMs} milliseconds`}::interval
          WHERE id = ${entry.id}`;
      });
    }
  }

  return { handled: pending.length };
}

/**
 * A named loop around a sweep function.
 *
 * Keeps running until stopped, backing off to `intervalMs` when idle and
 * continuing immediately while there is work — so a burst drains promptly
 * without polling hard when the system is quiet.
 */
export class Sweeper {
  #running = false;
  #stopped: Promise<void> = Promise.resolve();
  /** Resolves the current idle sleep early when stop() is called. */
  #wake: (() => void) | null = null;

  constructor(
    readonly name: SweeperName,
    private readonly sweep: () => Promise<SweepResult>,
    private readonly options: SweeperOptions = {},
  ) {}

  start(): void {
    if (this.#running) return;
    this.#running = true;
    this.#stopped = this.#loop();
  }

  /**
   * Stop the loop and wait for the in-flight sweep to finish.
   *
   * Interrupts the idle sleep rather than waiting it out: a sweeper with a
   * 10-second interval would otherwise hold up shutdown for 10 seconds, and
   * the process would look hung.
   */
  async stop(): Promise<void> {
    this.#running = false;
    this.#wake?.();
    await this.#stopped;
  }

  /** Sleep, unless stop() interrupts first. */
  async #idle(ms: number): Promise<void> {
    await new Promise<void>((resolve) => {
      const timer = setTimeout(() => {
        this.#wake = null;
        resolve();
      }, ms);
      this.#wake = () => {
        clearTimeout(timer);
        this.#wake = null;
        resolve();
      };
    });
  }

  async #loop(): Promise<void> {
    const interval = this.options.intervalMs ?? DEFAULT_INTERVAL_MS;
    const log = this.options.log ?? (() => {});

    while (this.#running) {
      try {
        const { handled } = await this.sweep();
        if (handled > 0) {
          log(`${this.name} handled ${handled}`, { sweeper: this.name, handled });
          // More may be waiting; take the next batch without sleeping.
          continue;
        }
      } catch (err) {
        // A failing sweep must not kill the loop, or the system silently
        // stops reclaiming work.
        log(`${this.name} failed`, {
          sweeper: this.name,
          error: err instanceof Error ? err.message : String(err),
        });
      }
      if (!this.#running) break;
      await this.#idle(interval);
    }
  }
}

/** Placeholder id generator so outbox entries are traceable in logs. */
export function outboxEntryId(): string {
  return newId("event").replace("evt_", "obx_");
}
