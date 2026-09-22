/**
 * PostgreSQL-backed durable workflow runtime — plan §21 Option A.
 *
 * The property that matters: **waiting is free**. A workflow parked on a human
 * question, a CI run or a PR review holds no process, no connection and no
 * model context. It is a row. When the awaited thing happens, a signal lands in
 * the inbox and a poller picks the workflow up where it left off.
 *
 * Durability comes from committing the *decision* before acting on it:
 * each step returns a transition, the transition is persisted, and only then
 * do side effects dispatch (through the outbox, at-least-once). A crash
 * between any two points resumes from the last committed step.
 *
 * Concurrency safety rests on three things:
 *   - `FOR UPDATE SKIP LOCKED` so N pollers never claim the same run;
 *   - a lease (`locked_by`/`locked_until`) so a poller that dies releases work;
 *   - `UNIQUE (organization_id, workflow_type, idempotency_key)` so a repeated
 *     start returns the original run instead of forking a second one.
 */

import type {
  StartWorkflowOptions,
  WorkflowDefinition,
  WorkflowRunRef,
  WorkflowRunState,
  WorkflowRuntime,
  WorkflowSignal,
  WorkflowStepContext,
} from "@dude/domain";
import { newId } from "@dude/domain";
import { withOrg } from "../db/client.ts";

/**
 * Column list aliased to WorkflowRunState.
 *
 * Takes the table alias because it is used both in a plain SELECT and in an
 * `UPDATE ... FROM` RETURNING, where an unqualified `id` is ambiguous against
 * the joined CTE.
 */
const workflowSelect = (t: string) => `
  ${t}.id                AS "workflowRunId",
  ${t}.workflow_type     AS "workflowType",
  ${t}.organization_id   AS "organizationId",
  ${t}.status,
  ${t}.step,
  ${t}.state,
  ${t}.attempt,
  ${t}.last_error        AS "lastError",
  ${t}.wake_at           AS "wakeAt",
  ${t}.awaiting_signals  AS "awaitingSignals",
  ${t}.created_at        AS "createdAt",
  ${t}.updated_at        AS "updatedAt"`;

const DEFAULT_MAX_ATTEMPTS = 5;
/** How long a poller may hold a claimed run before others may steal it. */
const LEASE_SECONDS = 60;

function toState(row: Record<string, unknown>): WorkflowRunState {
  const iso = (v: unknown) => (v instanceof Date ? v.toISOString() : ((v as string | null) ?? null));
  return {
    workflowRunId: row.workflowRunId as string,
    workflowType: row.workflowType as string,
    organizationId: row.organizationId as string,
    status: row.status as WorkflowRunState["status"],
    step: row.step as string,
    state: (row.state ?? {}) as Record<string, unknown>,
    attempt: Number(row.attempt ?? 0),
    lastError: (row.lastError as string | null) ?? null,
    wakeAt: iso(row.wakeAt),
    awaitingSignals: (row.awaitingSignals ?? []) as string[],
    createdAt: iso(row.createdAt)!,
    updatedAt: iso(row.updatedAt)!,
  };
}

/** Exponential backoff with a ceiling, in milliseconds. */
export function backoffMs(attempt: number): number {
  return Math.min(1000 * 2 ** Math.max(0, attempt - 1), 60_000);
}

export class PostgresWorkflowRuntime implements WorkflowRuntime {
  readonly #definitions = new Map<string, WorkflowDefinition>();
  /** Identifies this poller in lease rows; aids debugging a stuck run. */
  readonly #pollerId: string;

  constructor(pollerId = `poller_${Bun.randomUUIDv7("hex").slice(0, 8)}`) {
    this.#pollerId = pollerId;
  }

  register(definition: WorkflowDefinition): this {
    this.#definitions.set(definition.type, definition);
    return this;
  }

  /**
   * Start a workflow, or return the existing run for the same idempotency key.
   *
   * The insert relies on the unique constraint rather than a read-then-write,
   * so two concurrent starts cannot both create a run.
   */
  async start(options: StartWorkflowOptions): Promise<WorkflowRunRef> {
    const definition = this.#definitions.get(options.workflowType);
    if (!definition) throw new Error(`unknown workflow type: ${options.workflowType}`);

    return withOrg(options.organizationId, async ({ sql }) => {
      const rows = (await sql`
        INSERT INTO workflow_runs (
          id, organization_id, workflow_type, idempotency_key, status, step, state,
          work_item_id, run_id
        ) VALUES (
          ${newId("workflowRun")}, ${options.organizationId}, ${options.workflowType},
          ${options.idempotencyKey}, 'running', ${definition.initialStep},
          ${options.input ?? {}}::jsonb,
          ${options.workItemId ?? null}, ${options.runId ?? null}
        )
        ON CONFLICT (organization_id, workflow_type, idempotency_key) DO NOTHING
        RETURNING id`) as Array<{ id: string }>;

      if (rows[0]) return { workflowRunId: rows[0].id, deduplicated: false };

      const existing = (await sql`
        SELECT id FROM workflow_runs
        WHERE workflow_type = ${options.workflowType}
          AND idempotency_key = ${options.idempotencyKey}`) as Array<{ id: string }>;
      const found = existing[0];
      if (!found) {
        // The conflicting row is not visible: either the tenant context is
        // wrong, or it was deleted between the INSERT and this read. Say so
        // rather than crashing on an undefined property.
        throw new Error(
          `workflow start for ${options.workflowType}/${options.idempotencyKey} conflicted, ` +
            `but the existing run is not visible to organization ${options.organizationId}`,
        );
      }
      return { workflowRunId: found.id, deduplicated: true };
    });
  }

  /**
   * Deliver a signal.
   *
   * Signals are durable and order-independent: one may arrive before the
   * workflow parks to wait for it, so it is stored and consumed on the next
   * step rather than dropped.
   *
   * Scoped to the caller's organization. A run id alone must not confer
   * authority, since ids appear in webhook URLs and events.
   */
  async signal(
    organizationId: string,
    workflowRunId: string,
    name: string,
    payload: Record<string, unknown>,
    idempotencyKey?: string,
  ): Promise<void> {
    await withOrg(organizationId, async ({ sql }) => {
      // RLS confines this to the caller's tenant, so a run owned by another
      // organization simply does not exist here.
      const owned = (await sql`
        SELECT id FROM workflow_runs WHERE id = ${workflowRunId}`) as Array<{ id: string }>;
      if (!owned[0]) {
        throw new Error(`workflow run ${workflowRunId} not found`);
      }

      await sql`
        INSERT INTO workflow_signals (id, organization_id, workflow_run_id, name, payload, idempotency_key)
        VALUES (${newId("workflowSignal")}, ${organizationId}, ${workflowRunId},
                ${name}, ${payload}::jsonb, ${idempotencyKey ?? null})
        ON CONFLICT (workflow_run_id, idempotency_key) WHERE idempotency_key IS NOT NULL
        DO NOTHING`;

      // Wake a run parked on this signal. Runs parked on a timer are left
      // alone: their wake_at still governs when they resume.
      await sql`
        UPDATE workflow_runs
        SET status = 'running', wake_at = NULL
        WHERE id = ${workflowRunId}
          AND status = 'waiting'
          AND awaiting_signals ? ${name}`;
    });
  }

  /**
   * Park a live run until `wakeAt`.
   *
   * Restricted to live statuses: without that guard this resurrects a
   * completed or aborted run, making it re-claimable and re-executing a
   * terminal step.
   */
  async sleepUntil(organizationId: string, workflowRunId: string, wakeAt: Date): Promise<void> {
    await withOrg(organizationId, async ({ sql }) => {
      await sql`
        UPDATE workflow_runs
        SET status = 'waiting', wake_at = ${wakeAt.toISOString()}, awaiting_signals = '[]'::jsonb
        WHERE id = ${workflowRunId}
          AND status IN ('running', 'waiting')`;
    });
  }

  async abort(organizationId: string, workflowRunId: string, reason: string): Promise<void> {
    await withOrg(organizationId, async ({ sql }) => {
      await sql`
        UPDATE workflow_runs
        SET status = 'aborted', last_error = ${reason}, wake_at = NULL,
            locked_by = NULL, locked_until = NULL
        WHERE id = ${workflowRunId} AND status IN ('running', 'waiting')`;
    });
  }

  async get(organizationId: string, workflowRunId: string): Promise<WorkflowRunState | null> {
    return withOrg(organizationId, async ({ sql }) => {
      const rows = (await sql`
        SELECT ${sql.unsafe(workflowSelect("workflow_runs"))} FROM workflow_runs
        WHERE id = ${workflowRunId}`) as Array<Record<string, unknown>>;
      return rows[0] ? toState(rows[0]) : null;
    });
  }

  /**
   * Claim and advance at most `limit` runnable workflows.
   *
   * Claimed runs advance concurrently. Advancing them serially would make a
   * batch's later runs wait behind the earlier ones' model calls, easily
   * exceeding the lease before they even start, at which point another poller
   * could claim a run this one still intends to execute.
   *
   * Returns how many were advanced, so a caller can loop until idle.
   */
  async tick(organizationId: string, limit = 10): Promise<number> {
    const claimed = await this.#claim(organizationId, limit);
    // A step failure is recorded per-run by #advance; allSettled keeps one
    // bad run from aborting the rest of the batch.
    await Promise.allSettled(claimed.map((run) => this.#advanceWithLease(run)));
    return claimed.length;
  }

  /**
   * Advance a run, renewing its lease while the step executes.
   *
   * A step that calls a model, CI or a PR review can easily outlast a fixed
   * lease. Renewal keeps ownership for as long as this poller is actually
   * working, while still releasing the run promptly if the process dies.
   */
  async #advanceWithLease(run: WorkflowRunState): Promise<void> {
    let renewing = false;
    const renew = setInterval(() => {
      // Skip if the previous renewal is still in flight, so a slow database
      // cannot queue up overlapping transactions on the pool.
      if (renewing) return;
      renewing = true;
      void withOrg(run.organizationId, async ({ sql }) => {
        await sql`
          UPDATE workflow_runs
          SET locked_until = now() + ${`${LEASE_SECONDS} seconds`}::interval
          WHERE id = ${run.workflowRunId} AND locked_by = ${this.#pollerId}`;
      })
        .catch(() => {
          // A failed renewal is not fatal: the write-back is guarded on the
          // lease, so losing it makes the transition a no-op rather than a
          // double-apply.
        })
        .finally(() => {
          renewing = false;
        });
    }, (LEASE_SECONDS * 1000) / 3);
    // Do not hold the process open for a renewal timer.
    renew.unref?.();

    try {
      await this.#advance(run);
    } finally {
      clearInterval(renew);
    }
  }

  /**
   * Claim runnable workflows.
   *
   * `SKIP LOCKED` lets concurrent pollers take disjoint sets without blocking
   * each other. A run is runnable when it is not parked on a future timer and
   * either holds no lease or holds an expired one.
   */
  async #claim(organizationId: string, limit: number): Promise<WorkflowRunState[]> {
    return withOrg(organizationId, async ({ sql }) => {
      const rows = (await sql`
        WITH claimed AS (
          SELECT id FROM workflow_runs
          WHERE status IN ('running', 'waiting')
            AND (locked_until IS NULL OR locked_until < now())
            AND (
              -- Runnable now.
              status = 'running'
              -- Or parked on a timer that has elapsed: a backoff retry, or a
              -- sleepUntil whose deadline passed. Such runs await no signal,
              -- so this cannot resume one that is still blocked on a human.
              OR (wake_at IS NOT NULL AND wake_at <= now())
              -- Or parked on a signal that is already in the inbox.
              OR EXISTS (
                SELECT 1 FROM workflow_signals s
                WHERE s.workflow_run_id = workflow_runs.id
                  AND s.consumed_at IS NULL
                  AND workflow_runs.awaiting_signals @> to_jsonb(s.name)::jsonb
              )
            )
          ORDER BY wake_at NULLS FIRST, created_at
          LIMIT ${limit}
          FOR UPDATE SKIP LOCKED
        )
        UPDATE workflow_runs w
        SET locked_by = ${this.#pollerId},
            locked_until = now() + ${`${LEASE_SECONDS} seconds`}::interval,
            status = 'running'
        FROM claimed
        WHERE w.id = claimed.id
        RETURNING ${sql.unsafe(workflowSelect("w"))}`) as Array<
        Record<string, unknown>
      >;
      return rows.map(toState);
    });
  }

  /** Run one step of a claimed workflow and persist the transition. */
  async #advance(run: WorkflowRunState): Promise<void> {
    const definition = this.#definitions.get(run.workflowType);
    if (!definition) {
      await this.#fail(run, `unknown workflow type: ${run.workflowType}`, true);
      return;
    }

    const step = definition.steps[run.step];
    if (!step) {
      await this.#fail(run, `unknown step: ${run.step}`, true);
      return;
    }

    // Read pending signals without consuming them. They are marked consumed
    // only once the step's transition commits, so a step that throws leaves
    // them in the inbox for the retry — otherwise a human's approval or a CI
    // webhook would be silently discarded by a transient failure.
    const signals = await this.#peekSignals(run);

    const ctx: WorkflowStepContext = {
      workflowRunId: run.workflowRunId,
      organizationId: run.organizationId,
      state: run.state,
      signals,
      attempt: run.attempt,
    };

    try {
      const result = await step(ctx);

      const committed = await withOrg(run.organizationId, async ({ sql }) => {
        // Every write-back is guarded on this poller still holding the lease
        // and the run still being live. That makes the transition a no-op if
        // the run was aborted mid-step, or if the lease expired and another
        // poller took over — last-writer-wins would otherwise resurrect an
        // aborted run or double-apply a step.
        const rows =
          result.next === null
            ? ((await sql`
                UPDATE workflow_runs
                SET status = 'completed', step = ${run.step}, wake_at = NULL,
                    awaiting_signals = '[]'::jsonb, attempt = 0,
                    locked_by = NULL, locked_until = NULL,
                    state = COALESCE(${result.state ?? null}::jsonb, state)
                WHERE id = ${run.workflowRunId}
                  AND status IN ('running', 'waiting')
                  AND locked_by = ${this.#pollerId}
                  AND locked_until > now()
                RETURNING id`) as Array<{ id: string }>)
            : ((await sql`
                UPDATE workflow_runs
                SET step = ${result.next},
                    state = COALESCE(${result.state ?? null}::jsonb, state),
                    status = ${result.sleepUntil || result.awaitSignals?.length ? "waiting" : "running"},
                    wake_at = ${result.sleepUntil?.toISOString() ?? null},
                    awaiting_signals = ${result.awaitSignals ?? []}::jsonb,
                    -- A successful step clears the retry counter for the next.
                    attempt = 0,
                    last_error = NULL,
                    locked_by = NULL,
                    locked_until = NULL
                WHERE id = ${run.workflowRunId}
                  AND status IN ('running', 'waiting')
                  AND locked_by = ${this.#pollerId}
                  AND locked_until > now()
                RETURNING id`) as Array<{ id: string }>);

        if (!rows[0]) return false;

        // The transition is durable, so the signals that produced it can be
        // retired. Same transaction: a crash between the two would either
        // re-deliver the signals or lose them.
        if (signals.length > 0) {
          // `IN` over a values list rather than `= ANY($1::text[])`: the
          // driver renders a JS array as a Postgres array *literal*, which a
          // text[] cast then rejects.
          await sql`
            UPDATE workflow_signals SET consumed_at = now()
            WHERE id IN ${sql(signals.map((s) => s.signalId))}
              AND consumed_at IS NULL`;
        }
        return true;
      });

      if (!committed) {
        // Aborted, or the lease was lost. Either way this poller must not
        // also record a failure for a run it no longer owns.
        return;
      }
    } catch (err) {
      await this.#fail(run, err instanceof Error ? err.message : String(err), false, definition);
    }
  }

  /**
   * Read the pending signals this run is currently awaiting, without
   * consuming them.
   *
   * Scoped to `awaiting_signals` on purpose. A signal may arrive before the
   * workflow reaches the step that waits for it — reading indiscriminately
   * would let an earlier step swallow it, and the workflow would then park
   * forever on a signal that had already been delivered. Unawaited signals
   * stay in the inbox until a step asks for them.
   */
  async #peekSignals(run: WorkflowRunState): Promise<WorkflowSignal[]> {
    return withOrg(run.organizationId, async ({ sql }) => {
      const rows = (await sql`
        SELECT s.id AS "signalId", s.workflow_run_id AS "workflowRunId", s.name, s.payload,
               s.received_at AS "receivedAt"
        FROM workflow_signals s
        JOIN workflow_runs w ON w.id = s.workflow_run_id
        WHERE s.workflow_run_id = ${run.workflowRunId}
          AND s.consumed_at IS NULL
          AND w.awaiting_signals ? s.name
        ORDER BY s.received_at`) as Array<Record<string, unknown>>;

      return rows.map((r) => ({
        signalId: r.signalId as string,
        workflowRunId: r.workflowRunId as string,
        name: r.name as string,
        payload: (r.payload ?? {}) as Record<string, unknown>,
        receivedAt:
          r.receivedAt instanceof Date ? r.receivedAt.toISOString() : (r.receivedAt as string),
      }));
    });
  }

  /**
   * Record a step failure: retry with backoff, or dead-letter once the
   * attempt budget is exhausted. Dead-lettered runs stay inspectable rather
   * than disappearing, so a human can see why work stopped.
   */
  async #fail(
    run: WorkflowRunState,
    error: string,
    fatal: boolean,
    definition?: WorkflowDefinition,
  ): Promise<void> {
    const maxAttempts = definition?.maxAttempts ?? DEFAULT_MAX_ATTEMPTS;
    const attempt = run.attempt + 1;
    const exhausted = fatal || attempt >= maxAttempts;

    await withOrg(run.organizationId, async ({ sql }) => {
      if (exhausted) {
        await sql`
          UPDATE workflow_runs
          SET status = 'dead_lettered', attempt = ${attempt}, last_error = ${error},
              wake_at = NULL, locked_by = NULL, locked_until = NULL
          WHERE id = ${run.workflowRunId}
            AND status IN ('running', 'waiting')`;
        return;
      }

      // The awaiting set is preserved across a retry. Clearing it would leave
      // a run that had parked on, say, ["pr_approved","pr_rejected"] unable to
      // be woken by either signal again, reachable only by its backoff timer.
      await sql`
        UPDATE workflow_runs
        SET status = 'waiting', attempt = ${attempt}, last_error = ${error},
            wake_at = now() + ${`${backoffMs(attempt)} milliseconds`}::interval,
            awaiting_signals = ${run.awaitingSignals}::jsonb,
            locked_by = NULL, locked_until = NULL
        WHERE id = ${run.workflowRunId}
          AND status IN ('running', 'waiting')`;
    });
  }
}
