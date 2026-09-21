/**
 * Event type catalogue — plan §18.2.
 *
 * Kept as a frozen object rather than a TS enum so the string values are
 * the contract: they are written to Postgres, streamed to the browser and
 * consumed by the Python E2E suite.
 */

export const EventTypes = {
  // Work item lifecycle
  WorkItemCreated: "work_item.created",
  WorkItemConfirmed: "work_item.confirmed",
  WorkItemPaused: "work_item.paused",
  WorkItemResumed: "work_item.resumed",
  WorkItemSteered: "work_item.steered",
  WorkItemAborted: "work_item.aborted",

  // Workflow
  WorkflowTransitioned: "workflow.transitioned",
  WorkflowStepStarted: "workflow.step.started",
  WorkflowStepCompleted: "workflow.step.completed",
  WorkflowStepFailed: "workflow.step.failed",

  // Run lifecycle
  RunCreated: "run.created",
  RunStarted: "run.started",
  RunCompleted: "run.completed",
  RunFailed: "run.failed",
  RunAborted: "run.aborted",

  // Worker / capacity
  WorkerRegistered: "worker.registered",
  WorkerHeartbeat: "worker.heartbeat",
  WorkerLost: "worker.lost",
  RunLeaseAcquired: "run.lease.acquired",
  RunLeaseReleased: "run.lease.released",

  // Runtime instance (container)
  RuntimeCreating: "runtime.creating",
  RuntimeStarted: "runtime.started",
  RuntimeStopped: "runtime.stopped",
  RuntimeDestroyed: "runtime.destroyed",

  // Workspace / git
  WorkspaceCreated: "workspace.created",
  RepoMaterialized: "repo.materialized",
  RepoBranchCreated: "repo.branch_created",
  GitCommitCreated: "git.commit_created",
  GitPushCompleted: "git.push_completed",

  // Agent sessions
  SessionStarted: "agent.session.started",
  SessionStopped: "agent.session.stopped",
  SubagentStarted: "agent.subagent.started",
  SubagentStopped: "agent.subagent.stopped",
  ToolCalled: "agent.tool.called",
  ToolCompleted: "agent.tool.completed",
  ModelRequestStarted: "agent.model.request.started",
  ModelRequestCompleted: "agent.model.request.completed",
  AgentMessage: "agent.message",

  // Human interaction
  QuestionAsked: "question.asked",
  QuestionAnswered: "question.answered",

  // Artifacts
  ArtifactCreated: "artifact.created",

  // Cost
  CostSampled: "cost.sampled",
  BudgetSoftLimitReached: "budget.soft_limit_reached",
  BudgetHardLimitReached: "budget.hard_limit_reached",
} as const;

export type EventType = (typeof EventTypes)[keyof typeof EventTypes];

export const ALL_EVENT_TYPES: readonly string[] = Object.freeze(Object.values(EventTypes));
