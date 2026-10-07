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

  // The image library (the backend writes the person's acts, dude-image-builder the builds')
  /** An image was added, described, archived or made the default base. Payload: `{ imageId, name, changed }`. */
  ImageUpdated: "image.updated",
  /** A version was queued to build. Payload: `{ imageId, name, versionId, version, buildId, source }`. */
  ImageBuildQueued: "image.build_queued",
  /** A build or finish started. Payload: `{ imageId, buildId, kind }`. */
  ImageBuildStarted: "image.build_started",
  /** A build or finish failed. Payload: `{ imageId, name, versionId, version, buildId, kind, error }`. */
  ImageBuildFailed: "image.build_failed",
  /** A finish succeeded. Payload: `{ imageId, versionId, buildId, layer, ref }`. */
  ImageFinished: "image.finished",
  /** A version became the one every user runs: built, or published again. Payload: `{ imageId, name, versionId, version, republished }`. */
  ImagePublished: "image.published",
  /** A Run waits, before lux, for its library image's dude layer (or first version). Payload: `{ buildId }`. */
  RunImagePreparing: "run.image_preparing",

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
  /**
   * A steer was queued for a Run's agent. Payload: `{ directiveId, text,
   * scope, supersedes, interrupt, attachments?, by?, conductorRunId? }`:
   * `by: "conductor"` for the task's conductor's steer (the actor is its
   * Run), with `conductorRunId`; absent for a person's.
   */
  RunSteered: "run.steered",
  /**
   * The harness took a steer. Payload: `{ directiveId, lands, receipt }`:
   * `lands` is `next_step` (the agent reads it at its next model step, in
   * this turn) or `next_turn` (only once the turn ends); `receipt` says
   * whether a delivery follows when it is read. Absent from an older lux.
   */
  DirectiveAccepted: "run.directive.accepted",
  /**
   * A steer reached the agent. Payload: `{ directiveId, read?, interruptOnly? }`:
   * `read` when lux reported the agent's step reading it, so the event's
   * time and place in the ledger are where it was read; without it (an
   * older lux, a harness with no read receipt) it was handed over then.
   * `interruptOnly`: an "Interrupt now" sent as the interrupt alone, settled
   * with the steer whose words it resends (in the same transaction, or when
   * sent if they were already read); it is not a second read.
   */
  DirectiveDelivered: "run.directive.delivered",
  /**
   * A steer will not reach the agent. Payload: `{ directiveId, error,
   * nextRunId? }`: `nextRunId` names the conductor a message to an ended
   * conductor was handed to instead.
   * Never after its delivery; a `DirectiveDelivered` after it (the agent
   * read it after all) supersedes it, and the failure is cleared. An
   * "Interrupt now" sent as the interrupt alone fails with the steer whose
   * words it resends, with the same error (an older lux fails that steer
   * when the interrupt cancels its turn).
   */
  DirectiveFailed: "run.directive.failed",
  RunPaused: "run.paused",
  /**
   * dude parked a Run itself: it waits on a person, went quiet, or its
   * preview is unused. Payload: `{ reason, message, taskStatus?, parkedAt? }`:
   * `parkedAt` is when the park began, as the database stored it in the
   * park's own transaction — the boundary an answer or decision ending it
   * is compared with, on the same clock. Absent from older parks and from
   * a preview's.
   */
  RunParked: "run.parked",
  /**
   * A person asked for a paused Run to be resumed. Payload: `{ reason,
   * requestedAt }`: `requestedAt` is when they asked, as the database
   * stored it — what the resume's `run.resume.timed` counts from.
   */
  RunResumed: "run.resumed",
  /**
   * How long a resume of the Run took, end to end, written once its agent
   * said something. Payload: `{ epoch, cause, moved, hostName, totalMs,
   * untilBusyMs, phases }`: `cause` is `answer`, `repository`, `person` or
   * `idle`; `moved` whether lux placed it on another host (null when
   * unknown); `totalMs` from when it became due to the agent's first
   * message, thought or tool call, `untilBusyMs` to it taking its input.
   * `phases`, in milliseconds, in order: `react` (dude asking lux),
   * `schedule` (lux placing it), `image`, `restore` (its volumes),
   * `start` (container and workload), `reload` (the agent loading its
   * session until lux reports it running), `take` (it taking its input),
   * `firstOutput`. A phase whose end is unknown is absent, never zero.
   */
  RunResumeTimed: "run.resume.timed",

  // Run lifecycle
  RunCreated: "run.created",
  RunStarted: "run.started",
  RunCompleted: "run.completed",
  RunFailed: "run.failed",
  RunAborted: "run.aborted",
  /**
   * A live phase Run made no progress for its window: a tool call open the
   * whole of it, or (a Run that changes code) no change to its files.
   * Payload: `{ stall: RunStall, conducted, text? }`; `text`, the report
   * in words, on a plain delivery, where it is its owner's (notified, and
   * the task's banner). The Run reads as stalled until what it was
   * reported for changes (run_stalled).
   */
  RunStalled: "run.stalled",
  /** The task's owner left a stalled Run as it is: it is not reported again. Payload: `{}`. */
  RunStallLeft: "run.stall_left",
  /**
   * A phase Run was stopped and a fresh one put in its slot. On the old
   * Run. Payload: `{ from, to, note, phase, category?, tier?, by? }`:
   * `by: "conductor"` for the conductor's restart_run (the actor is its
   * Run), absent for a person's.
   */
  RunRestarted: "run.restarted",
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
  /** lux clone outcome, at submit or resume. Payload: `{ repo, status, ref?, branch?, commit?, error?, requestId? }`. */
  GitClone: "git.clone",
  /** lux checkout's actual starting commit. Payload: `{ repo, ref, base, branch? }`. */
  GitCheckout: "git.checkout",
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
  /**
   * An agent asked a person (`kind` `agent`), or the workflow escalated
   * (`kind` `escalation`). An agent's question that moved its task to
   * awaiting input carries `taskStatus`, what it was, and `waitCursor`,
   * the cursor of that move's `task.status_changed`: an answer to a
   * conductor puts the status back only while that move is still the
   * task's latest and nothing else waits on a person.
   */
  QuestionAsked: "question.asked",
  QuestionAnswered: "question.answered",
  /**
   * A question closed unanswered because what it asked was settled
   * elsewhere: the conductor's question about an escalation, once the
   * escalation was decided on the banner. Payload: `{ questionId, by:
   * "decision", action }`.
   */
  QuestionClosed: "question.closed",
  /**
   * A message in a task's Chat, on its conductor's Run. Payload:
   * `{ text, directiveId?, github?, by? }`. A person's: the first has no
   * `directiveId` (it is the conductor's briefing, with dude's note before
   * it); each later one is delivered as the directive named. One from a
   * pull request comment addressed to dude (actor `integration`,
   * `github:<login>`) carries `github: { login, repo, number, feedbackId,
   * kind, url?, path? }`. The conductor's reply on a pull request is `by:
   * "conductor"` (actor its Run) with `github: { repo, number, feedbackId,
   * url, inReplyTo? }`.
   */
  ChatMessage: "chat.message",
  /**
   * dude's own line in a task's Chat, about something that wakes nobody:
   * a conducted task's pull requests ready to merge, or no longer. Payload:
   * `{ text, about }`, `about` "ready_to_merge" or "no_longer_ready".
   */
  ChatNotice: "chat.notice",
  /**
   * dude briefed a task's new conductor: `{ text }` is its first prompt —
   * dude's note on the task, then the first message. Written with the
   * Run, after that message's `chat.message`.
   */
  ConductorBriefed: "conductor.briefed",
  /**
   * Who takes a task's delivery decisions changed. Payload: `{ from, to, why }`,
   * each "policy" or "conductor" (`from` null for a delivery started by Talk
   * it through). A person's first message in Chat hands them to the conductor;
   * Let Deliver finish it hands them back.
   */
  TaskDeciderChanged: "task.decider_changed",
  /**
   * A conducted delivery parked on a decision for its conductor. Payload:
   * `{ point, policy, actions, phases, note }`: where, what Deliver would do,
   * what decide and start_phase may say, and the note's line.
   */
  ConductorDecisionAwaited: "conductor.decision_awaited",
  /**
   * The conductor took the decision its delivery waited on. Payload:
   * `{ point, action, phase?, categories?, findingIds?, note?, draft? }`.
   */
  ConductorDecided: "conductor.decided",
  /**
   * dude woke a task's conductor with a note listing why. Payload:
   * `{ text, reasons, directiveId?, started? }`: reasons are their kinds
   * (decision, escalation, question, steer_read, steer_failed, pr_merged,
   * pr_closed, published, publish_refused, publish_stalled, checkout,
   * stalled; safety in older notes); `started` when no conductor was live
   * and the note briefed a new one.
   */
  ConductorWoken: "conductor.woken",

  // Artifacts
  ArtifactCreated: "artifact.created",

  // Memory (docs/design/memory.md). Payload: `{ memoryId, title, kind? }`.
  MemoryCreated: "memory.created",
  MemoryUpdated: "memory.updated",
  MemoryArchived: "memory.archived",
  MemoryRestored: "memory.restored",

  // Cost
  CostSampled: "cost.sampled",
  /**
   * lux's cost for a Run changed, as its cost plugins priced it. Payload:
   * `{ aiUsd, computeUsd, status }`: USD amounts, each null while lux has
   * priced nothing in that family; status is lux's (pending, incomplete,
   * complete, final). The latest replaces the harness-reported model cost.
   */
  RunCostReported: "run.cost.reported",
  BudgetSoftLimitReached: "budget.soft_limit_reached",
  BudgetHardLimitReached: "budget.hard_limit_reached",
} as const;

export type EventType = (typeof EventTypes)[keyof typeof EventTypes];

export const ALL_EVENT_TYPES: readonly string[] = Object.freeze(Object.values(EventTypes));
