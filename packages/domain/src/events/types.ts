/**
 * Event type catalogue — plan §18.2.
 *
 * Kept as a frozen object rather than a TS enum so the string values are
 * the contract: they are written to Postgres, streamed to the browser and
 * consumed by the Python E2E suite.
 */

export interface BrainstormTurnStopped {
  readonly runId: string;
  /** The overdue open call's tool; absent when the model went silent with no call open. */
  readonly tool?: string;
  readonly openSecs?: number;
  /** How long the agent had shown no activity, for a silent turn. */
  readonly silentSecs?: number;
  readonly directiveId: string;
}

export const EventTypes = {
  // Project lifecycle
  ProjectCreated: "project.created",
  ProjectUpdated: "project.updated",
  RepositoryAdded: "repository.added",
  RepositoryUpdated: "repository.updated",
  RepositoryRemoved: "repository.removed",
  /**
   * A repository a resume added to a Run could not be cloned; lux dropped
   * it and the agent goes on without it. Payload: `{ repository, error,
   * reason }` (the spec name, git's error, and why in plain words); for a
   * session's Run also `{ key, name }`, the repository as members know it.
   */
  RepositoryCloneFailed: "repository.clone_failed",
  EpicCreated: "epic.created",
  EpicUpdated: "epic.updated",
  EpicDeleted: "epic.deleted",
  /** Organization or project settings changed. Payload: `{ scope, projectId?, changed }`. */
  SettingsUpdated: "settings.updated",
  /** A role's prompt was saved or restored. Payload: `{ role, versionId, projectId?, restoredFrom? }`. */
  PromptSaved: "prompt.saved",
  /**
   * An automation was created, changed, turned on or off, transferred or
   * deleted, or its owner left. Payload: `{ automationId, name, changed }`;
   * `changed.owner` is `{ from, to }` (to null: the organisation's admins hold it).
   */
  AutomationChanged: "automation.changed",

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
  /** A person's comment on a task, shown in its Activity. Payload: `{ text }`. */
  TaskComment: "task.comment",

  // Task relations (docs/design/task-relations.md). An edge's events are on
  // both its tasks, in one correlation.
  /**
   * An edge was added. Payload: `{ kind: "blocks" | "related", from, to, why?, proposedByRun?, by? }`
   * (`from`/`to` task keys; `by` the agent's role for an agent's).
   */
  TaskRelationAdded: "task.relation_added",
  /** An edge was removed. Payload: `{ kind, from, to }`. */
  TaskRelationRemoved: "task.relation_removed",
  /**
   * The merge of `by` released the task's last open blocker. Payload:
   * `{ by, pr, blockers: [] }`. Only on a merge: never on a move to done
   * with nothing merged.
   */
  TaskUnblocked: "task.unblocked",
  /**
   * A blocker of the task was marked done with nothing merged: it still
   * blocks, for good. Payload: `{ by, actor? }` (`actor` who marked it done).
   */
  TaskBlockerUnmerged: "task.blocker_unmerged",
  /** The owner set start when unblocked. Payload: `{ how }`. */
  TaskAutoStartSet: "task.auto_start_set",
  /**
   * Start when unblocked ended without firing. Payload: `{ how, why, error? }`,
   * why `owner` | `started` | `owner_changed` | `unblocked_by_removal` |
   * `closed` | `archived` | `failed` (its start failed for a reason other
   * than a blocker: `error`).
   */
  TaskAutoStartCancelled: "task.auto_start_cancelled",
  /**
   * Start when unblocked fired. Payload: `{ how, by, pr?, setBy, setByName, setAt }`; the
   * start's own events follow.
   */
  TaskAutoStarted: "task.auto_started",
  /** Start when unblocked found a blocker still open at fire time. Payload: `{ by?, blockers }`. */
  TaskAutoStartRefused: "task.auto_start_refused",
  /**
   * A start refused. Payload: `{ path, reason: "archived" | "closed" | "blocked", blockers? }`
   * (`blockers` for blocked). Makes refusals auditable; never shown as a line.
   */
  TaskStartRefused: "task.start_refused",
  /** A person started a blocked task anyway. Payload: `{ blockers: [{ key, status }], how, note? }`. */
  TaskStartOverridden: "task.start_overridden",
  /**
   * Browser only, on the live stream: a task's relations or its start
   * when unblocked changed; a board or task page reads again.
   */
  RelationsChanged: "relations.changed",

  // Lifecycle (docs/design/tracking-work.md, Epic 1): a person's decisions
  // beside the status; the status move each makes is its own task.status_changed.
  /**
   * A person closed the task. Payload: `{ resolution, duplicateOf?, note?, handedOver?, removedRelations?, andArchived? }`
   * (`duplicateOf` and the lists are task keys; `andArchived` when a task.archived {viaClose} follows in the same batch).
   */
  TaskClosed: "task.closed",
  /** A person reopened it. Payload: `{ was }`, the resolution it had. */
  TaskReopened: "task.reopened",
  /** Archived. Payload: `{ viaEpic?, viaClose? }` (`viaEpic` the epic's id). */
  TaskArchived: "task.archived",
  /** Unarchived. Payload: `{ viaEpic? }`. */
  TaskUnarchived: "task.unarchived",
  /** An epic archived with its tasks. Payload: `{ epicId, title, tasks }`, `tasks` how many went with it. */
  EpicArchived: "epic.archived",
  /** An epic unarchived with the tasks archived with it. Payload: `{ epicId, title, tasks }`. */
  EpicUnarchived: "epic.unarchived",

  // Brainstorm sessions: on the session (events.session_id), its members' alone.
  /** Payload: `{ title }`. */
  BrainstormCreated: "session.created",
  /** The owner invited people. Payload: `{ people, role }`; role `owner` for a handover waiting on its acceptance. */
  BrainstormShared: "session.shared",
  /** Payload: `{ person, via? }`; `via: "organisation"` when a member of the organisation joined an organisation session by writing in it. */
  BrainstormJoined: "session.joined",
  /** Payload: `{ person }`. */
  BrainstormDeclined: "session.declined",
  /** Payload: `{ person, role }`. */
  BrainstormRoleChanged: "session.role_changed",
  /** Payload: `{ person }`. */
  BrainstormMemberRemoved: "session.member_removed",
  /** Payload: `{ from, to, keep, fromName, toName }`. */
  BrainstormOwnerChanged: "session.owner_changed",
  /** Payload: `{ projects }`. */
  BrainstormLinked: "session.linked",
  /**
   * The session's agent asked again for repositories whose clone failed
   * (refresh_checkout): linked again, cloned at its next resume.
   * Payload: `{ project, repositories: [{ id, name }] }`.
   */
  BrainstormCheckoutRequested: "session.checkout.requested",
  /** The agent filled the proposal card. Payload: `{ proposalId, items }`. */
  BrainstormProposed: "session.proposed",
  /** A member filed items from the card. Payload: `{ proposalId, by, filed: [{ item, key, kind }] }`. */
  BrainstormFiled: "session.filed",
  /** dude briefed the session's agent. Payload: `{ text, supersedes? }`, `supersedes` the ended agent's directive the briefing hands on. */
  BrainstormBriefed: "session.briefed",
  /** dude told the session's agent something (who owns it now). Payload: `{ text, directiveId }`. */
  BrainstormTold: "session.told",
  /** dude interrupted a turn with an overdue open call, or silent for 10 minutes with none. Payload: `BrainstormTurnStopped`. */
  BrainstormTurnStopped: "session.turn_stopped",
  /**
   * The session was named: by its agent (name_session, `by: "agent"`) or a
   * member who can chat (`by`: their person id). Payload: `{ title, by }`.
   */
  BrainstormRenamed: "session.renamed",
  /**
   * The owner chose the session's model tier and harness; each null follows
   * the organisation's Brainstorm setting. It applies from the next
   * member's message: a turn in progress finishes on the old model.
   * Payload: `{ by, tier: { id, name } | null, harness: Harness | null }`.
   */
  BrainstormModelChanged: "session.model.changed",
  /**
   * A member's message found the session's agent between turns on another
   * model than the session's: dude ended it and started a fresh one,
   * briefed with the conversation, the message its first input. Payload:
   * `{ from, to, tier: { name } | null, model, harness: Harness }`.
   */
  BrainstormModelApplied: "session.model.applied",
  /**
   * The tier the session chose was removed: it follows the organisation's
   * again. Written by the database (migration 106). Payload: `{ tier: { id, name } }`.
   */
  BrainstormModelFallback: "session.model.fallback",
  /**
   * A member has the session open, or no longer. Live only, never in the
   * ledger, and only to the session's members. Payload: `{ personId, open }`.
   */
  BrainstormOpen: "session.open",
  /** The owner opened the session to the organisation, one way. Payload: `{ access: "organisation" }`. */
  BrainstormAccessChanged: "session.access_changed",
  /** Its owner was removed from the organisation: its admins own it. Payload: `{ owner, ownerName }`. */
  BrainstormOwnerLeft: "session.owner_left",
  /** An admin took over an admin-owned session. Payload: `{ from, to, toName }`. */
  BrainstormOwnerTaken: "session.owner_taken",
  /** The owner who left came back while its admins still owned it: it is theirs again. By the system. Payload: `{ owner }`. */
  BrainstormOwnerReturned: "session.owner_returned",
  /**
   * A message mentioned someone not looking at the session: the notifier
   * sends them a Web Push. Payload: `{ person, by, byName, title, text }`.
   */
  BrainstormMentioned: "session.mentioned",

  // People
  /** An admin added someone. Payload: `{ personId, role }`. */
  PersonInvited: "person.invited",
  /** Payload: `{ personId, from, to }`. */
  PersonRoleChanged: "person.role_changed",
  /** Their keys were revoked with them. Payload: `{ personId }`. */
  PersonRemoved: "person.removed",
  /**
   * An active-tab person-session request was seen, at most once a minute. Live
   * only, never in the ledger: no cursor. Payload: `{ person: PersonRef }`.
   */
  PersonSeen: "person.seen",

  // Chat providers (orchestrator/internal/chat)
  /** An admin installed a provider's app in a workspace. Payload: `{ provider, workspaceId, workspaceName }`. */
  ChatProviderInstalled: "chat_provider.installed",
  /**
   * The installation went with its links and thread bindings: an admin
   * disconnected it, or the workspace removed the app or revoked its token.
   * Payload: `{ provider, workspaceId, workspaceName, reason }`.
   */
  ChatProviderRemoved: "chat_provider.removed",
  /** A person linked their provider account. Payload: `{ provider, personId, handle }`. */
  ChatIdentityLinked: "chat_identity.linked",
  /** A link went: unlinked by the person, or their sign-in revoked. Payload: `{ provider, personId, reason }`. */
  ChatIdentityUnlinked: "chat_identity.unlinked",

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
   * preview is unused; or a conductor's or session agent's container
   * stopped on its own, or its turn failed, and lux keeps it to resume.
   * Payload: `{ reason, message, taskStatus?, parkedAt?, stopped?,
   * failedTurns? }`: `parkedAt` is when the park began, as the database
   * stored it in the park's own transaction — the boundary an answer or
   * decision ending it is compared with, on the same clock. Absent from
   * older parks and from a preview's. `stopped` is lux's state of a
   * container that stopped on its own (`stopped`, `succeeded`, `failed`,
   * `lost`); `failedTurns` how many turns in a row failed, for a park
   * after a failed turn.
   */
  RunParked: "run.parked",
  /**
   * dude interrupted a conductor's turn that was busy with no tool call
   * open and no activity for 10 minutes (a harness retrying its model
   * request). Payload: `{ runId, silentSecs, directiveId }`.
   */
  RunTurnStopped: "run.turn_stopped",
  /**
   * A person asked for a paused Run to be resumed. Payload: `{ reason,
   * requestedAt }`: `requestedAt` is when they asked, as the database
   * stored it — what the resume's `run.resume.timed` counts from.
   */
  RunResumed: "run.resumed",
  /**
   * A resume changed what the Run records it is on (`runs.machine`): the
   * size its settings name now, as lux applied it, or why it kept the one
   * it had. Payload: `{ machine }`, the Run's `machine` as now recorded.
   */
  RunResized: "run.resized",
  /**
   * What dude told the agent of its machine move, once lux accepted the
   * resume carrying it; once per Run and epoch. Payload:
   * `RunToldPayload`: `{ epoch, moveId, kind, text }`, `text` the resume's
   * input exactly as lux took it.
   */
  RunTold: "run.told",
  /**
   * An agent asked for a larger machine (request_machine). Payload:
   * `MachineRequestedPayload`: whole machines, the reason, and for a
   * refusal before any park `refused` and `message`, with no `moveId`.
   */
  MachineRequested: "machine.requested",
  /**
   * The task's (or session's) layer names the larger size and the Run is
   * moving: parked, then resumed on it. On the Run. Payload:
   * `MachineRaisedPayload` (`pool` when the move changes pools).
   */
  MachineRaised: "machine.raised",
  /**
   * lux refused the real resume of a move: the Run resumed where it was,
   * told lux's exact message, and a raise's layer was put back. On the Run.
   * Payload: `MachineRefusedPayload`.
   */
  MachineRefused: "machine.refused",
  /**
   * A person moved a role back to its settings' size; on the task or the
   * session. Payload: `MachineResetPayload`; `note` is what the agent is
   * told after "Moved back to <size> by <name>".
   */
  MachineReset: "machine.reset",
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
  /**
   * Where an agent's Run is while it starts, as lux announced it, once per
   * change (about six per start). Payload: `{ stage, since, epoch,
   * reason? }`: `stage` is lux's (`waiting`, `image`, `volumes`,
   * `repositories`, `container`, `running` while lux's state is not yet
   * running), or `park` for a move's stop (a drain, a preemption, a
   * migrate); `since` lux's `stageSince` in UTC. Written only while the Run
   * starts. A start that fails says its stage in `run.failed`.
   */
  RunStage: "run.stage",
  /**
   * The project secrets an agent Run was given at submit, by the phase's
   * recipient (`project_secrets.recipients`). Once per Run. Payload:
   * `{ names }`, never a value. The conductor and brainstorm get none.
   */
  RunSecretsGiven: "run.secrets.given",
  /**
   * dude did not ask lux to push a Run's work: the work to be pushed holds
   * a value of a project secret the Run was given, in some form. The Run
   * fails, kept, and its task escalates. Payload: `{ secrets, files }`:
   * names and paths only, never a value.
   */
  RunPushRefused: "run.push.refused",

  // Run lifecycle
  RunCreated: "run.created",
  RunStarted: "run.started",
  RunCompleted: "run.completed",
  /** Checks-gate verdict for a Run turn: `{turn, round, verified, skipped?, waived?, unchanged?, epoch?, heads?, checks, message}`; `unchanged` is a turn that changed nothing it could push, so no report was judged. */
  RunChecksJudged: "run.checks.judged",
  /** A person's checks-gate decision: `{action, note}`. */
  RunChecksDecided: "run.checks.decided",
  /**
   * A test step started no tester: every path changed since the previous
   * round is outside the product and that round passed; its verdicts stand.
   * Payload: `{ fromRunId, from, heads, paths, criteria: [{criterion, status, decision}] }`.
   */
  TestSkipped: "test.skipped",
  /**
   * The Run failed. Payload: `{ status, error, kept?, failedTurns?, stage?,
   * reason? }`. `stage`: a start that failed before the agent ran, the
   * `run.stage` it was in, with `reason` lux's own (its `stateReason`).
   * `kept`: only a conductor's or session agent's turn failed, and the Run
   * goes on (parked, or stopped by a control already pending), so it reads
   * as the turn's failure, not the Run's; `failedTurns`, how many turns in
   * a row failed, when that failure parked it.
   */
  RunFailed: "run.failed",
  RunAborted: "run.aborted",
  /**
   * A live phase Run made no progress for its window: a tool call open the
   * whole of it, its agent silent the whole of it (no output, no tool
   * call), or (a Run that changes code) no change to its files.
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
   * Run), `by: "dude"` for a paused Run a project secret it was given is no
   * longer its phase's (it cannot resume), absent for a person's.
   */
  RunRestarted: "run.restarted",
  /**
   * An implementer on work a task continues (a source in mode `continue`)
   * finished committing nothing: the goal was met already, and the work is
   * reviewed on its unchanged head. On the Run. Payload: `{ heads }`.
   */
  RunNothingToAdd: "run.nothing_to_add",
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
  /** A phase Run is about to merge its commit into the task branch on GitHub. Payload: `{ repo, commit }`. */
  GitMergeAttempted: "git.merge_attempted",
  GitPushCompleted: "git.push_completed",

  // Review
  ReviewCompleted: "review.completed",
  FindingResolved: "review.finding_resolved",
  /** A reviewer's question stored, once per question. Payload: `{ questionId, n, category, findingId?, neededFrom }`. */
  ReviewQuestionRaised: "review.question_raised",
  /** A reviewer's open question answered. Payload: `{ questionId, by: "person" | "conductor", answer }`. */
  ReviewQuestionAnswered: "review.question_answered",
  /** A reviewer's open question withdrawn as moot. Payload: `{ questionId, by: "person" | "conductor", reason }`. */
  ReviewQuestionWithdrawn: "review.question_withdrawn",
  FeedbackSubmitted: "feedback.submitted",
  FeedbackDelivered: "feedback.delivered",
  FeedbackReplied: "feedback.replied",
  FeedbackResolved: "feedback.resolved",
  FeedbackReopened: "feedback.reopened",
  FeedbackOutdated: "feedback.outdated",

  // Pull requests — the factory's output, and the state it waits on.
  PullRequestOpened: "pull_request.opened",
  PullRequestUpdated: "pull_request.updated",
  PullRequestChecksChanged: "pull_request.checks_changed",
  PullRequestReviewed: "pull_request.reviewed",
  PullRequestCommented: "pull_request.commented",
  PullRequestMerged: "pull_request.merged",
  PullRequestClosed: "pull_request.closed",
  // Adopted PR verdict post/edit: { pullRequestId, repo, number, head, commentId?, url?, edited, error? }.
  PullRequestVerdictCommented: "pull_request.verdict_commented",
  /**
   * dude pushed a fix to an open pull request and asked again for a review
   * from those whose latest review was on an older head. Payload:
   * `{ number, repo, head, reviewersRequested?, reviewersRefused?, error? }`.
   */
  PullRequestReviewRerequested: "pull_request.review_rerequested",
  /**
   * dude moved an open pull request's branch to a fix for its feedback,
   * once the fix was reviewed and tested on the candidate branch. Payload:
   * `{ repo, number, from, to, candidate }`.
   */
  PullRequestFixMoved: "pull_request.fix_moved",
  /**
   * A session's agent or a task's conductor read GitHub through dude
   * (github_search, github_issue, github_pull_request, github_check_log), on its Run.
   * Payload: `{ tool, repo, number?, checkId?, query?, kind?, state?, files?, bytes }`:
   * what was asked and the result's size in bytes, never what GitHub said.
   * A read refused once checked has `{ refused, status, permission? }` in place of
   * `bytes`: dude's kind (moved, moved_item, redirect, rate_reserve, rate_limited,
   * not_found, too_large, forbidden, refused, unavailable) and the HTTP status;
   * a 403 whose X-Accepted-GitHub-Permissions named what it needs has it in
   * `permission` (`issues=read`; alternatives split by `; `).
   */
  GitHubRead: "github.read",
  /**
   * dude is about to merge a checked fix into an open pull request's branch
   * on GitHub, because GitHub merged the base into that branch while the fix
   * was checked. Payload: `{ repo, branch, candidate, round, commit }`.
   */
  PullRequestFixMergeAttempted: "pull_request.fix_merge_attempted",

  // Agent sessions
  SessionStarted: "agent.session.started",
  /**
   * A resumed agent is in a new harness session, not the one it had: lux
   * could not reload it (session/load, thread/resume failed) and started a
   * blank one. Payload: `{ from, to, reason, directiveId? }`: the harness
   * session ids, lux's warning (`""` for none), and for a conductor or a
   * session's agent the directive that briefs it again.
   */
  SessionReplaced: "agent.session.replaced",
  /** What lux warned about the Run's agent, in its words. Payload: `{ message }`. */
  AgentWarning: "agent.warning",
  /**
   * The agent's harness compacted its conversation, as lux recorded it
   * (`lux.compacted`; a lux from before it records none). Payload:
   * `{ trigger?, preTokens?, postTokens?, summary?, summaryTruncated? }`,
   * each only when known: the summary is the one the harness kept (whole,
   * up to lux's 64 KiB cap; `summaryTruncated` when cut).
   */
  ContextCompacted: "agent.context.compacted",
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
  /**
   * lux refused a name the Run's agent looked up: it is on no list the Run
   * has (its `dns` event, allowed false). Once per Run and name. Payload:
   * `{ name, role }`, `role` as its settings name it (fixer, implementer…).
   */
  NetworkRefused: "agent.network.refused",

  // Child sessions: a subagent the Run's agent started from a tool call
  // (docs/design/agent-tools.md, "Child sessions"). Every payload carries
  // `{ childId, callId }`: the harness's id for the child (session, thread
  // or task) and the `callId` of the parent's `agent.tool.called` that
  // started it; a child of a child carries `parentChildId` too.
  /**
   * Payload: `{ childId, callId, parentChildId?, kind, title, agentType?,
   * model? }`. `kind` is `task` (OpenCode, Claude Code) or `spawn` (Codex);
   * `title` the description the parent gave.
   */
  ChildStarted: "agent.child.started",
  /**
   * A child's tool call, as `agent.tool.called` (`{ tool, callId, input,
   * title }`, `callId` the child's own) plus `{ childId, parentCallId }`.
   */
  ChildToolCalled: "agent.child.tool.called",
  /** A child's tool call ended, as `agent.tool.completed` plus `{ childId, parentCallId }`. */
  ChildToolCompleted: "agent.child.tool.completed",
  /**
   * A child's completed message. Payload: `{ childId, callId, text,
   * costUsd?, tokens? }`, text cut at 2 KB. A child's reasoning is not
   * recorded.
   */
  ChildMessage: "agent.child.message",
  /**
   * A child waits on something nothing in the Run can answer. Payload:
   * `{ childId, callId, on: "permission", permission, patterns, answered,
   * held, childCallId? }`: `held` false for an ask lux answered or one for
   * a call already over, which holds nothing. The child's next event ends
   * the wait.
   */
  ChildWaiting: "agent.child.waiting",
  /**
   * Payload: `{ childId, callId, status, summary, costUsd?, tokens?,
   * durationSecs }`, status `completed`, `failed` or `interrupted`. A
   * child's cost is already in `run.cost.reported`: never add it.
   */
  ChildEnded: "agent.child.ended",

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
   * The conductor asked the task's people to raise one of its delivery's
   * limits for this task (request_limit), as its question `questionId`.
   * Payload: `{ questionId, limit, from, to, reason, repo?, number?, head? }`,
   * `limit` one of `perReviewRound`, `perPullRequest`, `rerun`.
   */
  LimitRequested: "limit.requested",
  /**
   * A person approved a limit request: the delivery's check allows `extra`
   * more from now on, for this task alone. Payload: limit.requested's and
   * `{ extra, by, byName }`; actor the approver.
   */
  LimitGranted: "limit.granted",
  /**
   * A message in a task's Chat, on its conductor's Run. Payload:
   * `{ text, directiveId?, supersedes?, github?, by? }`. A person's: the first has no
   * `directiveId` (it is the conductor's briefing, with dude's note before
   * it); each later one is delivered as the directive named. A message an
   * ended agent never read, handed on to the next, names that agent's
   * directive as `supersedes`. One from a
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
   * stalled, unblocked; safety in older notes); `started` when no conductor was live
   * and the note briefed a new one.
   */
  ConductorWoken: "conductor.woken",
  /**
   * What a conductor's digest told it of its epic, item by item, on its
   * Run, in the transaction that briefed or woke it. Payload: `{ since,
   * through, where: "briefing" | "wake", task, messages: [{ message, at,
   * from, to, text, replyTo, answeredAt }], more, notes: [{ at, n, change,
   * was?, line }], peers: [{ task, status, at }] }`.
   */
  ConductorDigest: "conductor.digest",

  // Agents coordinating on an epic (docs/design/agent-coordination.md)
  /**
   * A Run sent a peer task a message. On the sender's Run. Payload:
   * `{ message, to, toTaskId, reply_to, lands, text, notice? }`: `to` the
   * task's key; `lands` `next_step` or `next_run` (held, with `notice`).
   */
  AgentMessageSent: "agent.message.sent",
  /**
   * A Run read a peer's message. On the reading Run. Payload: `{ message,
   * from, directive, after }` at a step (`after`, the tool it had just
   * run); `{ message, from, via: "briefing" }` in its first prompt.
   */
  AgentMessageReceived: "agent.message.received",
  /**
   * A message refused. On the sender's Run. Payload: `{ reason: "rate" |
   * "not_a_peer" | "too_long" | …, to, retry_at }`.
   */
  AgentMessageRefused: "agent.message.refused",
  /**
   * A Run waits on a peer's reply. Payload: `{ message, on, how: "wait" |
   * "turn_ended", parks_at }`.
   */
  AgentPeerWaiting: "agent.peer.waiting",
  /**
   * A Run's wait ended. Payload: `{ message, on, by: "reply" | "self" |
   * "run_ended" | "peer_ended", status? }`: `status` the peer task's, when it
   * ended without replying.
   */
  AgentPeerWaitEnded: "agent.peer.wait_ended",
  /**
   * A note was pinned on an epic (on the pinning Run for an agent's; the
   * epic's correlation for a person's). Payload: `{ note, n, epicId, by,
   * text, replaces, from_session }`.
   */
  EpicNotePinned: "epic.note.pinned",
  /** A person retired a note. Payload: `{ note, n, epicId, by }`. */
  EpicNoteRetired: "epic.note.retired",
  /** A note refused past the limit. Payload: `{ reason: "limit", current, epicId }`. */
  EpicNoteRefused: "epic.note.refused",
  /**
   * A Run read a note change at its step: the EpicNoteNotice. Payload:
   * `{ note, n, change: "added" | "replaced" | "retired", was, after, directive }`.
   */
  AgentNoteTold: "agent.note.told",

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
