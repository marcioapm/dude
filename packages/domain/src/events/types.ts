/**
 * Event type catalogue — plan §18.2.
 *
 * Kept as a frozen object rather than a TS enum so the string values are
 * the contract: they are written to Postgres, streamed to the browser and
 * consumed by the Python E2E suite.
 */

export const EventTypes = {
  // Project lifecycle
  ProjectCreated: "project.created",
  ProjectUpdated: "project.updated",
  RepositoryAdded: "repository.added",
  RepositoryUpdated: "repository.updated",
  RepositoryRemoved: "repository.removed",
  EpicCreated: "epic.created",
  EpicUpdated: "epic.updated",
  EpicDeleted: "epic.deleted",
  /** Organization or project settings changed. Payload: `{ scope, projectId?, changed }`. */
  SettingsUpdated: "settings.updated",
  /** A role's prompt was saved or restored. Payload: `{ role, versionId, projectId?, restoredFrom? }`. */
  PromptSaved: "prompt.saved",

  // Task lifecycle
  TaskCreated: "task.created",
  TaskUpdated: "task.updated",
  TaskConfirmed: "task.confirmed",
  TaskPaused: "task.paused",
  TaskResumed: "task.resumed",
  TaskSteered: "task.steered",
  TaskAborted: "task.aborted",
  TaskStatusChanged: "task.status_changed",
  /** Handed to someone else to drive. Payload: `{ from, to }`, person ids. */
  TaskOwnerChanged: "task.owner_changed",
  /** Who is on it changed. Payload: `{ people }`, person ids, the owner first. */
  TaskPeopleChanged: "task.people_changed",

  // People
  /** An admin added someone. Payload: `{ personId, role }`. */
  PersonInvited: "person.invited",
  /** Payload: `{ personId, from, to }`. */
  PersonRoleChanged: "person.role_changed",
  /** Their keys were revoked with them. Payload: `{ personId }`. */
  PersonRemoved: "person.removed",
  /**
   * Someone was seen (any request of theirs, at most once a minute). Live
   * only, never in the ledger: no cursor. Payload: `{ person: PersonRef }`.
   */
  PersonSeen: "person.seen",

  // Workflow
  WorkflowTransitioned: "workflow.transitioned",
  WorkflowStepStarted: "workflow.step.started",
  WorkflowStepCompleted: "workflow.step.completed",
  WorkflowStepFailed: "workflow.step.failed",

  // Human intervention on a Run (plan §24)
  RunSteered: "run.steered",
  /**
   * The harness took a steer. Payload: `{ directiveId, lands, receipt }`:
   * `lands` is `next_step` (the agent reads it at its next model step, in
   * this turn) or `next_turn` (only once the turn ends); `receipt` says
   * whether a delivery follows when it is read. Absent from an older lux.
   */
  DirectiveAccepted: "run.directive.accepted",
  /**
   * A steer reached the agent. Payload: `{ directiveId, read? }`: `read`
   * when lux reported the agent's step reading it, so the event's time and
   * place in the ledger are where it was read; without it (an older lux, a
   * harness with no read receipt) it was handed over then.
   */
  DirectiveDelivered: "run.directive.delivered",
  /** A steer will not reach the agent. Payload: `{ directiveId, error }`. */
  DirectiveFailed: "run.directive.failed",
  RunPaused: "run.paused",
  RunResumed: "run.resumed",

  // Run lifecycle
  RunCreated: "run.created",
  RunStarted: "run.started",
  RunCompleted: "run.completed",
  RunFailed: "run.failed",
  RunAborted: "run.aborted",
  /**
   * A Run's checkout changed: its diff against the commit it started from,
   * read live or left by the beforeStop hook as it stopped. Payload: a
   * summary with no lines (`RunDiffSummary`); the lines are
   * `GET /v1/runs/:id/diff`.
   */
  RunDiffUpdated: "run.diff.updated",
  /**
   * A task's servers, or its branch preview, changed: lux reported a
   * server.* event on the Run, or the preview moved on (created, submitted,
   * lux state, parked, waking, resumed, stopped, failed). Read
   * `GET /v1/tasks/:taskId/servers` again. Payload: `ServersChanged`.
   */
  ServersChanged: "servers.changed",

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

  // Review
  ReviewCompleted: "review.completed",
  FindingResolved: "review.finding_resolved",

  // Pull requests — the factory's output, and the state it waits on.
  PullRequestOpened: "pull_request.opened",
  PullRequestUpdated: "pull_request.updated",
  PullRequestChecksChanged: "pull_request.checks_changed",
  PullRequestReviewed: "pull_request.reviewed",
  PullRequestCommented: "pull_request.commented",
  PullRequestMerged: "pull_request.merged",
  PullRequestClosed: "pull_request.closed",

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
  /** The model's reasoning between actions. Payload: `{ text }`. */
  AgentThought: "agent.thought",
  /**
   * The task, as the agent received it — recorded when the agent takes it,
   * not when dude sends it. Payload: `{ text, truncated?, lands? }`; `lands`
   * is where this harness takes a steer (see `DirectiveAccepted`).
   */
  PromptDelivered: "agent.prompt.delivered",
  /**
   * The agent rewrote its plan. Payload: `{ todos: [{ content, status }] }`.
   *
   * A semantic milestone rather than a tool call, so a reader does not have
   * to know that OpenCode spells it `todowrite` and the next harness spells
   * it something else. Each event carries the whole list; the agent rewrites
   * it wholesale.
   */
  PlanUpdated: "agent.plan.updated",

  // Human interaction
  QuestionAsked: "question.asked",
  QuestionAnswered: "question.answered",

  // Artifacts
  ArtifactCreated: "artifact.created",

  // Memory (docs/design/memory.md). Payload: `{ memoryId, title, kind? }`.
  MemoryCreated: "memory.created",
  MemoryUpdated: "memory.updated",
  MemoryArchived: "memory.archived",
  MemoryRestored: "memory.restored",

  // Cost
  CostSampled: "cost.sampled",
  BudgetSoftLimitReached: "budget.soft_limit_reached",
  BudgetHardLimitReached: "budget.hard_limit_reached",
} as const;

export type EventType = (typeof EventTypes)[keyof typeof EventTypes];

export const ALL_EVENT_TYPES: readonly string[] = Object.freeze(Object.values(EventTypes));
