/**
 * The delivery workflow: a Work Item from request to open pull request.
 *
 *   implement → review ⟲ fix → simplify → test → PR → PR review ⟲ fix → done
 *
 * Each phase is a Run, because a Run already owns a container, a workspace, a
 * lease and a node affinity — everything a phase needs to be isolated. Review
 * fans out into several parallel Runs over the same ref; their findings merge
 * into one set before a fixer sees them, so the implementer gets the whole
 * picture rather than being steered N times.
 *
 * Two properties this file exists to guarantee:
 *
 * **Every loop terminates on a declared bound.** Not on a model deciding it is
 * finished. The bounds live in `policy.ts` and the exits are named, because
 * "done", "this finding is stuck" and "the loop ran out of budget" mean
 * different things to whoever is woken up.
 *
 * **Waking an agent is a decision, not a reflex.** A webhook arriving is not a
 * reason to spend tokens (plan §13.3): an approval, a green check and a
 * resolved thread all just update state. Only actionable review text or a
 * failing check is worth a fix Run, and those batch.
 */

import type { WorkflowDefinition, WorkflowStepContext, WorkflowStepResult } from "@dude/domain";
import type { PrSignal } from "../forge/classify.ts";
import { EventTypes } from "@dude/domain";
import { withOrg } from "../db/client.ts";
import { appendInScope } from "../events/ledger.ts";
import { eventBus } from "../events/bus.ts";
import {
  DEFAULT_DELIVERY_POLICY,
  loopExit,
  reviewersFor,
  type DeliveryPolicy,
  type ReviewCategory,
} from "./policy.ts";
import {
  createPhaseRun,
  findingsFor,
  markFindingAttempted,
  openPullRequestFor,
  phaseOutcome,
  setWorkItemStatus,
  supersedeStaleFindings,
} from "./delivery.ts";

export const DELIVERY_WORKFLOW_TYPE = "work_item.delivery";

/** Signals the workflow parks on, named once so both sides agree. */
export const Signals = {
  /** A phase Run reached a terminal state. Payload: { runId, status }. */
  PhaseFinished: "phase.finished",
  /** A forge event the classifier judged worth acting on. */
  PrFeedback: "pr.feedback",
  /** A human answered an escalation. */
  HumanDecision: "human.decision",
} as const;

/**
 * Everything the workflow remembers between steps.
 *
 * An index signature because the runtime persists state as jsonb and hands it
 * back as an open record; the named fields are what this workflow actually
 * reads.
 */
interface DeliveryState extends Record<string, unknown> {
  workItemId: string;
  projectId: string;
  repositoryId: string;
  policy: DeliveryPolicy;

  /** The Run whose output the next phase builds on. */
  headRunId?: string;
  branch?: string;
  headSha?: string;
  changedPaths?: string[];

  /** Phase Runs this workflow is waiting for. */
  pendingRunIds?: string[];
  /** Review → fix cycles spent so far. */
  iteration?: number;
  /** PR feedback → fix cycles spent so far. */
  prIteration?: number;
  pullRequestId?: string;
  /** Why the workflow stopped, when it stopped early. */
  escalation?: { reason: string; detail?: unknown };
}

function stateOf(ctx: WorkflowStepContext): DeliveryState {
  return ctx.state as unknown as DeliveryState;
}

/**
 * Which of the Runs we were waiting for have finished.
 *
 * Signals are consumed once, so a step that parks on several Runs has to
 * fold each arrival into state rather than re-reading them.
 */
function settle(state: DeliveryState, ctx: WorkflowStepContext): string[] {
  const finished = new Set(
    ctx.signals
      .filter((s) => s.name === Signals.PhaseFinished)
      .map((s) => String(s.payload.runId)),
  );
  return (state.pendingRunIds ?? []).filter((id) => !finished.has(id));
}

async function emit(
  organizationId: string,
  eventType: string,
  state: DeliveryState,
  payload: Record<string, unknown>,
): Promise<void> {
  const event = await withOrg(organizationId, (scope) =>
    appendInScope(scope, {
      eventType,
      organizationId,
      projectId: state.projectId,
      workItemId: state.workItemId,
      actor: { type: "system", id: "workflow" },
      source: "control-plane",
      correlationId: state.workItemId,
      payload,
    }),
  );
  eventBus.publish(event);
}

// ---------------------------------------------------------------------------
// Steps
// ---------------------------------------------------------------------------

/** Create the implementer Run and park until it finishes. */
async function implement(ctx: WorkflowStepContext): Promise<WorkflowStepResult> {
  const state = stateOf(ctx);
  await setWorkItemStatus(ctx.organizationId, state, "running", "implementing");
  const runId = await createPhaseRun(ctx.organizationId, {
    workItemId: state.workItemId,
    phase: "implement",
    baseRef: null,
    parentRunId: null,
  });

  return {
    next: "awaitImplement",
    state: { ...state, pendingRunIds: [runId], iteration: 0 },
    awaitSignals: [Signals.PhaseFinished],
  };
}

async function awaitImplement(ctx: WorkflowStepContext): Promise<WorkflowStepResult> {
  const state = stateOf(ctx);
  const pending = settle(state, ctx);
  if (pending.length > 0) {
    return { next: "awaitImplement", state: { ...state, pendingRunIds: pending }, awaitSignals: [Signals.PhaseFinished] };
  }

  const runId = state.pendingRunIds![0]!;
  const outcome = await phaseOutcome(ctx.organizationId, runId);

  if (!outcome.succeeded) {
    return escalate(ctx, state, "implement_failed", { runId, error: outcome.error });
  }
  // An implementer that changed nothing has not done the work, and there is
  // nothing for a reviewer to look at.
  if (outcome.changedPaths.length === 0) {
    return escalate(ctx, state, "no_changes", { runId });
  }

  return {
    next: "review",
    state: {
      ...state,
      headRunId: runId,
      branch: outcome.branch,
      headSha: outcome.headSha,
      changedPaths: outcome.changedPaths,
      pendingRunIds: [],
    },
  };
}

/**
 * Fan out reviewers over the current head.
 *
 * Parallel Runs rather than one reviewer with a long prompt: they cost
 * wall-clock once instead of N times, and a security reviewer that finds
 * nothing has not polluted the correctness reviewer's context.
 */
async function review(ctx: WorkflowStepContext): Promise<WorkflowStepResult> {
  const state = stateOf(ctx);
  await setWorkItemStatus(ctx.organizationId, state, "review", "agent review");
  const categories = reviewersFor(state.policy, state.changedPaths ?? []);

  const runIds = await Promise.all(
    categories.map((category: ReviewCategory) =>
      createPhaseRun(ctx.organizationId, {
        workItemId: state.workItemId,
        phase: "review",
        baseRef: state.headSha ?? null,
        parentRunId: state.headRunId ?? null,
        category,
      }),
    ),
  );

  await emit(ctx.organizationId, EventTypes.RunCreated, state, {
    phase: "review",
    reviewers: categories,
    iteration: state.iteration ?? 0,
  });

  return {
    next: "awaitReview",
    state: { ...state, pendingRunIds: runIds },
    awaitSignals: [Signals.PhaseFinished],
  };
}

async function awaitReview(ctx: WorkflowStepContext): Promise<WorkflowStepResult> {
  const state = stateOf(ctx);
  const pending = settle(state, ctx);
  // Every reviewer must report before the findings are a complete set; acting
  // on a partial set would send the fixer back for the rest immediately.
  if (pending.length > 0) {
    return { next: "awaitReview", state: { ...state, pendingRunIds: pending }, awaitSignals: [Signals.PhaseFinished] };
  }

  const findings = await findingsFor(ctx.organizationId, state.workItemId);
  const iteration = (state.iteration ?? 0) + 1;
  const exit = loopExit(state.policy, findings, iteration);

  if (exit === null) {
    return { next: "fix", state: { ...state, iteration, pendingRunIds: [] } };
  }
  if (exit.reason === "clear") {
    return { next: state.policy.simplify ? "simplify" : "test", state: { ...state, iteration, pendingRunIds: [] } };
  }
  return escalate(ctx, { ...state, iteration }, exit.reason, exit);
}

/**
 * One fix Run for all unresolved findings.
 *
 * One rather than one-per-finding: findings overlap, and a fixer that sees
 * the whole set can make a change that resolves several. It also keeps the
 * loop bound meaningful — N parallel fixers racing on one branch would
 * conflict.
 */
async function fix(ctx: WorkflowStepContext): Promise<WorkflowStepResult> {
  const state = stateOf(ctx);
  await setWorkItemStatus(ctx.organizationId, state, "running", "fixing review findings");
  const findings = await findingsFor(ctx.organizationId, state.workItemId);
  const unresolved = findings.filter((f) => f.status === "open");

  // Counted before the attempt, so a fixer that crashes still consumes one.
  await markFindingAttempted(ctx.organizationId, unresolved.map((f) => f.id));

  const runId = await createPhaseRun(ctx.organizationId, {
    workItemId: state.workItemId,
    phase: "fix",
    baseRef: state.headSha ?? null,
    parentRunId: state.headRunId ?? null,
    findingIds: unresolved.map((f) => f.id),
  });

  return {
    next: "awaitFix",
    state: { ...state, pendingRunIds: [runId] },
    awaitSignals: [Signals.PhaseFinished],
  };
}

async function awaitFix(ctx: WorkflowStepContext): Promise<WorkflowStepResult> {
  const state = stateOf(ctx);
  const pending = settle(state, ctx);
  if (pending.length > 0) {
    return { next: "awaitFix", state: { ...state, pendingRunIds: pending }, awaitSignals: [Signals.PhaseFinished] };
  }

  const runId = state.pendingRunIds![0]!;
  const outcome = await phaseOutcome(ctx.organizationId, runId);
  if (!outcome.succeeded) {
    return escalate(ctx, state, "fix_failed", { runId, error: outcome.error });
  }

  /*
   * Retire findings about files this fix rewrote.
   *
   * Here rather than after a review, because it is a *fix* that can make a
   * finding moot. Doing it after a review would retire the findings that
   * review just raised — every one of them names a file the previous phase
   * had touched, which is why it was reviewed.
   *
   * Superseding is not the same as resolving: the re-review that follows
   * raises the problem again if it is still there. This only stops a fixer
   * being sent back for a finding whose code no longer exists in that form.
   */
  if (outcome.headSha) {
    await supersedeStaleFindings(ctx.organizationId, state.workItemId, outcome.headSha);
  }

  // Back to review, over the fix's output. The re-review sees the delta, and
  // `loopExit` is what stops this going around forever.
  return {
    next: "review",
    state: {
      ...state,
      headRunId: runId,
      headSha: outcome.headSha ?? state.headSha,
      changedPaths: outcome.changedPaths,
      pendingRunIds: [],
    },
  };
}

/**
 * Simplify, then check the simplifier's own diff.
 *
 * Runs only once blocking findings are clear (plan §11.4). It may commit —
 * removing complexity is a change — but its scope is narrow: no behaviour
 * change, no widening.
 */
async function simplify(ctx: WorkflowStepContext): Promise<WorkflowStepResult> {
  const state = stateOf(ctx);
  await setWorkItemStatus(ctx.organizationId, state, "running", "simplifying");
  const runId = await createPhaseRun(ctx.organizationId, {
    workItemId: state.workItemId,
    phase: "simplify",
    baseRef: state.headSha ?? null,
    parentRunId: state.headRunId ?? null,
  });

  return {
    next: "awaitSimplify",
    state: { ...state, pendingRunIds: [runId] },
    awaitSignals: [Signals.PhaseFinished],
  };
}

async function awaitSimplify(ctx: WorkflowStepContext): Promise<WorkflowStepResult> {
  const state = stateOf(ctx);
  const pending = settle(state, ctx);
  if (pending.length > 0) {
    return { next: "awaitSimplify", state: { ...state, pendingRunIds: pending }, awaitSignals: [Signals.PhaseFinished] };
  }

  const runId = state.pendingRunIds![0]!;
  const outcome = await phaseOutcome(ctx.organizationId, runId);

  /*
   * A failed or empty simplification is not a failure of the work item: the
   * code was already simple enough, or the simplifier could not improve it.
   * Carry on with what we had.
   */
  if (!outcome.succeeded || outcome.changedPaths.length === 0) {
    return { next: "test", state: { ...state, pendingRunIds: [] } };
  }

  return {
    next: "test",
    state: {
      ...state,
      headRunId: runId,
      headSha: outcome.headSha ?? state.headSha,
      pendingRunIds: [],
    },
  };
}

async function test(ctx: WorkflowStepContext): Promise<WorkflowStepResult> {
  const state = stateOf(ctx);
  if (!state.policy.test) return { next: "openPullRequest", state };

  const runId = await createPhaseRun(ctx.organizationId, {
    workItemId: state.workItemId,
    phase: "test",
    baseRef: state.headSha ?? null,
    parentRunId: state.headRunId ?? null,
  });

  return {
    next: "awaitTest",
    state: { ...state, pendingRunIds: [runId] },
    awaitSignals: [Signals.PhaseFinished],
  };
}

async function awaitTest(ctx: WorkflowStepContext): Promise<WorkflowStepResult> {
  const state = stateOf(ctx);
  const pending = settle(state, ctx);
  if (pending.length > 0) {
    return { next: "awaitTest", state: { ...state, pendingRunIds: pending }, awaitSignals: [Signals.PhaseFinished] };
  }

  const runId = state.pendingRunIds![0]!;
  const outcome = await phaseOutcome(ctx.organizationId, runId);
  if (!outcome.succeeded) {
    // A tester that found a real problem should not be papered over by
    // opening the PR anyway.
    return escalate(ctx, state, "test_failed", { runId, error: outcome.error });
  }

  return { next: "openPullRequest", state: { ...state, pendingRunIds: [] } };
}

/**
 * Open the PR once the pre-PR gates have passed (plan §13.2).
 *
 * The body is rendered from what the workflow already knows — no extra model
 * call to describe work the ledger already recorded.
 */
async function openPullRequest(ctx: WorkflowStepContext): Promise<WorkflowStepResult> {
  const state = stateOf(ctx);
  const pullRequestId = await openPullRequestFor(ctx.organizationId, {
    workItemId: state.workItemId,
    runId: state.headRunId!,
    repositoryId: state.repositoryId,
  });
  // Waiting on people now: the agents are done until someone comments.
  await setWorkItemStatus(ctx.organizationId, state, "review", "pull request open");

  return {
    next: "awaitPullRequest",
    state: { ...state, pullRequestId, prIteration: 0 },
    awaitSignals: [Signals.PrFeedback, Signals.HumanDecision],
  };
}

/**
 * Wait on the PR, and wake a fixer only when there is something to fix.
 *
 * The classifier (see `forge/classify.ts`) has already decided an arriving
 * event is actionable before it becomes a `pr.feedback` signal — an approval,
 * a green check and a resolved thread never get here (plan §13.3).
 */
async function awaitPullRequest(ctx: WorkflowStepContext): Promise<WorkflowStepResult> {
  const state = stateOf(ctx);
  const feedback = ctx.signals.filter((s) => s.name === Signals.PrFeedback);

  // Merged or closed: the work item is done either way, and which one it was
  // is already in the ledger.
  const signals = feedback.map((s) => s.payload as unknown as PrSignal);
  const terminal = signals.find((s) => s.kind === "terminal");
  if (terminal) {
    // Closed without merging is someone deciding not to take the change,
    // which is an abort of the work item rather than a failure of it.
    const merged = terminal.state === "merged";
    await setWorkItemStatus(
      ctx.organizationId,
      state,
      merged ? "done" : "aborted",
      merged ? "pull request merged" : "pull request closed without merging",
    );
    return { next: null, state: { ...state, pendingRunIds: [] } };
  }

  const actionable = signals.filter((s) => s.kind === "actionable");
  if (actionable.length === 0) {
    return { next: "awaitPullRequest", state, awaitSignals: [Signals.PrFeedback, Signals.HumanDecision] };
  }

  const prIteration = (state.prIteration ?? 0) + 1;
  if (prIteration > state.policy.maxPrFixIterations) {
    return escalate(ctx, { ...state, prIteration }, "pr_loop_exhausted", { iterations: prIteration });
  }

  // Several comments arriving together cost one fix Run, not one each: the
  // signals are flattened into one list of things to address.
  const prFeedback = actionable.flatMap((s) => s.feedback);
  await setWorkItemStatus(ctx.organizationId, state, "running", "addressing pull request feedback");
  const runId = await createPhaseRun(ctx.organizationId, {
    workItemId: state.workItemId,
    phase: "fix",
    baseRef: state.headSha ?? null,
    parentRunId: state.headRunId ?? null,
    prFeedback,
  });

  return {
    next: "awaitPrFix",
    state: { ...state, prIteration, pendingRunIds: [runId] },
    awaitSignals: [Signals.PhaseFinished],
  };
}

async function awaitPrFix(ctx: WorkflowStepContext): Promise<WorkflowStepResult> {
  const state = stateOf(ctx);
  const pending = settle(state, ctx);
  if (pending.length > 0) {
    return { next: "awaitPrFix", state: { ...state, pendingRunIds: pending }, awaitSignals: [Signals.PhaseFinished] };
  }

  const runId = state.pendingRunIds![0]!;
  const outcome = await phaseOutcome(ctx.organizationId, runId);
  if (!outcome.succeeded) {
    return escalate(ctx, state, "pr_fix_failed", { runId, error: outcome.error });
  }
  // The push updated the PR; it is back with the reviewers.
  await setWorkItemStatus(ctx.organizationId, state, "review", "pull request updated");

  // The push updates the existing PR; no new PR is opened.
  return {
    next: "awaitPullRequest",
    state: {
      ...state,
      headRunId: runId,
      headSha: outcome.headSha ?? state.headSha,
      pendingRunIds: [],
    },
    awaitSignals: [Signals.PrFeedback, Signals.HumanDecision],
  };
}

/**
 * Stop and ask for a person.
 *
 * A terminal step rather than a park: the work item needs a decision, and
 * holding a workflow open pretending to make progress would hide that. The
 * human's answer starts a new attempt.
 */
async function escalate(
  ctx: WorkflowStepContext,
  state: DeliveryState,
  reason: string,
  detail: unknown,
): Promise<WorkflowStepResult> {
  await emit(ctx.organizationId, EventTypes.QuestionAsked, state, {
    kind: "escalation",
    reason,
    detail,
  });
  await setWorkItemStatus(ctx.organizationId, state, "awaiting_input", reason);
  return { next: null, state: { ...state, escalation: { reason, detail } } };
}

export const deliveryWorkflow: WorkflowDefinition = {
  type: DELIVERY_WORKFLOW_TYPE,
  initialStep: "implement",
  steps: {
    implement,
    awaitImplement,
    review,
    awaitReview,
    fix,
    awaitFix,
    simplify,
    awaitSimplify,
    test,
    awaitTest,
    openPullRequest,
    awaitPullRequest,
    awaitPrFix,
  },
};

export { DEFAULT_DELIVERY_POLICY };
