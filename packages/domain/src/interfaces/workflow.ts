/**
 * WorkflowRuntime — plan §21 Option A.
 *
 * v1 implements this on PostgreSQL (state rows, transactional outbox,
 * signal inbox, durable timers, leased jobs via FOR UPDATE SKIP LOCKED).
 * The interface exists so Temporal or another durable engine can replace
 * the implementation without touching callers.
 *
 * The contract that matters: waiting costs nothing. A workflow parked on a
 * human question or a webhook holds no process and spends no tokens.
 */

export interface StartWorkflowOptions {
  workflowType: string;
  organizationId: string;
  /** Deduplicates starts; a repeat returns the original run. */
  idempotencyKey: string;
  input: Record<string, unknown>;
  workItemId?: string | undefined;
  runId?: string | undefined;
}

export interface WorkflowRunRef {
  workflowRunId: string;
  /** True when an existing run was returned instead of a new one. */
  deduplicated: boolean;
}

export type WorkflowRunStatus =
  | "running"
  | "waiting"
  | "completed"
  | "failed"
  | "dead_lettered"
  | "aborted";

export interface WorkflowRunState {
  workflowRunId: string;
  workflowType: string;
  organizationId: string;
  status: WorkflowRunStatus;
  /** Current durable step; how a resumed run knows where it was. */
  step: string;
  state: Record<string, unknown>;
  attempt: number;
  lastError: string | null;
  /** When set, the run is parked until this time. */
  wakeAt: string | null;
  createdAt: string;
  updatedAt: string;
}

/** A signal delivered into a parked workflow (answer, webhook, steer). */
export interface WorkflowSignal {
  signalId: string;
  workflowRunId: string;
  name: string;
  payload: Record<string, unknown>;
  receivedAt: string;
}

export interface WorkflowRuntime {
  start(options: StartWorkflowOptions): Promise<WorkflowRunRef>;

  /**
   * Deliver a signal. Safe to call for a workflow that is not currently
   * parked — signals queue in the inbox and are consumed on next step.
   */
  signal(
    workflowRunId: string,
    name: string,
    payload: Record<string, unknown>,
    idempotencyKey?: string,
  ): Promise<void>;

  /** Park until `wakeAt`, releasing the worker. */
  sleepUntil(workflowRunId: string, wakeAt: Date): Promise<void>;

  abort(workflowRunId: string, reason: string): Promise<void>;

  get(workflowRunId: string): Promise<WorkflowRunState | null>;
}

/**
 * A workflow definition is a set of named steps. Each step is a pure-ish
 * function from (state, signals) to the next step — durable because the
 * runtime persists the result of every transition before acting on it.
 */
export interface WorkflowStepResult {
  /** Next step name, or null to complete the workflow. */
  next: string | null;
  state?: Record<string, unknown>;
  /** Park until this time before running `next`. */
  sleepUntil?: Date;
  /** Park until one of these signals arrives. */
  awaitSignals?: string[];
}

export interface WorkflowStepContext {
  workflowRunId: string;
  organizationId: string;
  state: Record<string, unknown>;
  /** Signals received since the last step ran. */
  signals: WorkflowSignal[];
  attempt: number;
}

export interface WorkflowDefinition {
  type: string;
  initialStep: string;
  steps: Record<string, (ctx: WorkflowStepContext) => Promise<WorkflowStepResult>>;
  /** Attempts before a step is dead-lettered. */
  maxAttempts?: number;
}
