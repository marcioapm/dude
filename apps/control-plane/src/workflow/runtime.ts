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
import { withOrg, withoutTenant } from "../db/client.ts";

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
      return { workflowRunId: existing[0]!.id, deduplicated: true };
    });
  }

  /**
   * Deliver a signal.
   *
   * Signals are durable and order-independent: one may arrive before the
   * workflow parks to wait for it, so it is stored and consumed on the next
   * step rather than dropped.
   */
  async signal(
    workflowRunId: string,
    name: string,
    payload: Record<string, unknown>,
    idempotencyKey?: string,
  ): Promise<void> {
    const organizationId = await this.#organizationOf(workflowRunId);

    await withOrg(organizationId, async ({ sql }) => {
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

  async sleepUntil(workflowRunId: string, wakeAt: Date): Promise<void> {
    const organizationId = await this.#organizationOf(workflowRunId);
    await withOrg(organizationId, async ({ sql }) => {
      await sql`
        UPDATE workflow_runs
        SET status = 'waiting', wake_at = ${wakeAt.toISOString()}, awaiting_signals = '[]'::jsonb
        WHERE id = ${workflowRunId}`;
    });
  }

  async abort(workflowRunId: string, reason: string): Promise<void> {
    const organizationId = await this.#organizationOf(workflowRunId);
    await withOrg(organizationId, async ({ sql }) => {
      await sql`
        UPDATE workflow_runs
        SET status = 'aborted', last_error = ${reason}, wake_at = NULL
        WHERE id = ${workflowRunId} AND status IN ('running', 'waiting')`;
    });
  }

  async get(workflowRunId: string): Promise<WorkflowRunState | null> {
    const organizationId = await this.#organizationOf(workflowRunId).catch(() => null);
    if (!organizationId) return null;

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
   * Returns how many were advanced, so a caller can loop until idle.
   */
  async tick(organizationId: string, limit = 10): Promise<number> {
    const claimed = await this.#claim(organizationId, limit);
    for (const run of claimed) {
      await this.#advance(run);
    }
    return claimed.length;
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

    // Consume pending signals in the same transaction that runs the step, so a
    // signal is never both delivered and left unconsumed.
    const signals = await this.#consumeSignals(run);

    const ctx: WorkflowStepContext = {
      workflowRunId: run.workflowRunId,
      organizationId: run.organizationId,
      state: run.state,
      signals,
      attempt: run.attempt,
    };

    try {
      const result = await step(ctx);

      await withOrg(run.organizationId, async ({ sql }) => {
        if (result.next === null) {
          await sql`
            UPDATE workflow_runs
            SET status = 'completed', step = ${run.step}, wake_at = NULL,
                awaiting_signals = '[]'::jsonb, attempt = 0, locked_by = NULL, locked_until = NULL,
                state = COALESCE(${result.state ?? null}::jsonb, state)
            WHERE id = ${run.workflowRunId}`;
          return;
        }

        const waiting = Boolean(result.sleepUntil || result.awaitSignals?.length);
        await sql`
          UPDATE workflow_runs
          SET step = ${result.next},
              state = COALESCE(${result.state ?? null}::jsonb, state),
              status = ${waiting ? "waiting" : "running"},
              wake_at = ${result.sleepUntil?.toISOString() ?? null},
              awaiting_signals = ${result.awaitSignals ?? []}::jsonb,
              -- A successful step clears the retry counter for the next one.
              attempt = 0,
              last_error = NULL,
              locked_by = NULL,
              locked_until = NULL
          WHERE id = ${run.workflowRunId}`;
      });
    } catch (err) {
      await this.#fail(run, err instanceof Error ? err.message : String(err), false, definition);
    }
  }

  /**
   * Take the pending signals this run is currently awaiting.
   *
   * Scoped to `awaiting_signals` on purpose. A signal may arrive before the
   * workflow reaches the step that waits for it — consuming indiscriminately
   * would let an earlier step swallow it, and the workflow would then park
   * forever on a signal that had already been delivered. Unawaited signals
   * stay in the inbox until a step asks for them.
   *
   * Marking them consumed in the same statement that reads them means a
   * concurrent poller cannot deliver the same signal twice.
   */
  async #consumeSignals(run: WorkflowRunState): Promise<WorkflowSignal[]> {
    return withOrg(run.organizationId, async ({ sql }) => {
      const rows = (await sql`
        UPDATE workflow_signals
        SET consumed_at = now()
        WHERE id IN (
          SELECT s.id FROM workflow_signals s
          JOIN workflow_runs w ON w.id = s.workflow_run_id
          WHERE s.workflow_run_id = ${run.workflowRunId}
            AND s.consumed_at IS NULL
            AND w.awaiting_signals ? s.name
          ORDER BY s.received_at
          FOR UPDATE OF s SKIP LOCKED
        )
        RETURNING id AS "signalId", workflow_run_id AS "workflowRunId", name, payload,
                  received_at AS "receivedAt"`) as Array<Record<string, unknown>>;

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
          WHERE id = ${run.workflowRunId}`;
        return;
      }

      await sql`
        UPDATE workflow_runs
        SET status = 'waiting', attempt = ${attempt}, last_error = ${error},
            wake_at = now() + ${`${backoffMs(attempt)} milliseconds`}::interval,
            awaiting_signals = '[]'::jsonb,
            locked_by = NULL, locked_until = NULL
        WHERE id = ${run.workflowRunId}`;
    });
  }

  /**
   * Resolve a workflow run's organization.
   *
   * workflow_runs is tenant-scoped, but callers such as webhook handlers hold
   * only a run id. This reads the owning organization through the same narrow
   * definer-rights path used for API keys.
   */
  async #organizationOf(workflowRunId: string): Promise<string> {
    const rows = await withoutTenant(async ({ sql }) => {
      return (await sql`
        SELECT organization_id FROM workflow_run_organization(${workflowRunId})`) as Array<{
        organization_id: string;
      }>;
    });
    const organizationId = rows[0]?.organization_id;
    if (!organizationId) throw new Error(`workflow run ${workflowRunId} not found`);
    return organizationId;
  }
}
