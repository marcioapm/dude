# Plan: a person's words reach the agent; Resolve conflicts is never refused for being busy

Branch: `fix/owner-notes-and-queued-resolve` (from origin/main a7419dce8).

## Bug 1: a decision's note is dropped

Root cause: `settleChecksTx` (delivery/checks_escalation.go) queues the
escalation's own `detail.Message` on retry and never the note. The note
goes to `run.checks.decided`, to `questions` (RecordDecisionTx →
"Decided by people" section of a *fresh* Run's prompt), and to
`task.decided`. A held Run that is *resumed* with a directive gets no
prompt again, so the note never reaches it.

Design: one helper `ownerSaid(ctx, tx, taskID, person, note)` renders
`agents/contracts/owner-said.md`:

    The task's owner, <name>, said:
    <note verbatim>

("A person, <name>, said" when the decider is not the owner; "dude" never
gets this since system decisions are not a person's words). Placed ahead
of the gate text in the directive the next acting agent gets.

Paths to audit and change (before → after recorded in the report):

1. checks retry: directive = detail.Message → owner-said + "\n\n" + message.
2. checks waive (run scope and attempt scope): the Run goes on with no
   turn (the gate passes the turn and the work is pushed). No agent
   acts next on that Run; note stays in `questions` (later fresh Runs)
   and run.checks.judged message. If the Run has more turns later it
   is a resumed session → queue a directive? A waived Run pushes and
   finishes; no next turn exists. Leave, report.
3. DecideEscalationTx resume: ResumeKeptTx queues the note as a
   directive already (raw). Wrap it with owner-said so it is marked.
4. DecideEscalationTx retry/accept/amend/waive on other reasons: the
   workflow starts fresh Runs (retry → new Runs) whose prompts carry the
   `questions` decisions, but only as "Q: Delivery stopped (...) A: note",
   and a next round taken over by handOverTx (resumed writer) gets
   `sinceLastRoundTx` decisions. Check: does a decision note reach the
   next Run? Verify with tests per path; add owner-said where it does not.
5. Conductor as decider (ConductDecideEscalation): the conductor already
   has the answer (it decided on it). When a *person* decides on the
   banner/answer for a conducted task, the conductor's wake line
   (escalation-on-banner / escalation-answered) gets the note.
6. recover resume/retry/restart: recordPickUpNote → questions; resume
   also ResumeKeptTx directive; retry under conductor: StartPickedUpConductor
   note. Verify by test the next Run's prompt decisions include it; keep.

Tests (real Postgres, delivery package):
- checks retry with note → directive text contains note verbatim and the
  owner-said lead (red first).
- resume with note → directive marked as the owner's words.
- banner decision on conducted task → wake line contains the note.

## Bug 2: Resolve conflicts refused while busy

Design (smallest fit):
- `State.BaseSyncRequests []BaseSyncRequest{ID, Repo, Number, Base,
  BaseHead, Head, Person, At}` on delivery state.
- `ResolveConflictsTx`: existing awaitPullRequest / escalation branches
  unchanged. Otherwise (busy): record a request (dedupe same repo+number+
  baseHead), activity line `pull_request.action` {action:
  "base-sync-requested"}, and if a live writer Run with BaseSync on the
  task: queue a directive with the sync instruction. Return queued=true.
  API answers 202 {queued:true}.
- Base head: read by the API (forge BranchSHA) if possible, else the
  request id; signal key carries it (#327).
- Consumption: when the delivery creates a writer Run (fix / prFix /
  integration), its brief starts with the sync instruction for each
  pending request in its repos. Cleared once a Run merged the recorded
  base head (`git merge-base --is-ancestor`, as IntegrationMergeWhen) —
  checked from recorded heads via forge compare, or a person push moved
  past it.
- Web: PR panel shows "Bring main in: asked, waiting for dude's next
  step" while a request is pending.

Order: Bug 1 red → commit → green → commit. Then Bug 2 the same way.
