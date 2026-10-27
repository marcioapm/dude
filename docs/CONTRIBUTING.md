# Working on dude

This is the guide for picking the project up: how the pieces fit, which
decisions are settled, where the traps are, and what to build next. The
[README](../README.md) covers running it; the
[plan](../software-factory-plan-v0.9.md) is the long-form design this
implements, and section numbers below (§21, §81…) refer to it.

## The shape of the system

```
               ┌─────────── backend (Bun) ───────────┐
  web UI ─────▶│ public API, auth, projects, board,  │◀──── GitHub webhooks
  (React)      │ settings, live events (SSE)         │      (verified, stored)
               └──────┬───────────────▲──────────────┘
          run control │               │ NOTIFY on every event
     (service token)  ▼               │
               ┌──────────────────────┴──────────────┐
               │ orchestrator (Go) — no users         │
               │ workflow runtime, delivery, policy, │──── GitHub API
               │ prompts, findings, PR sync          │     (PRs, branches)
               └──────┬──────────────────────────────┘
                      │ submit, follow output, steer,
                      │ stop, resume, push (HTTP + SSE)
               ┌──────▼──────────────────────────────┐
               │ lux — containers on Podman hosts,   │
               │ checkouts, leased pushes, agents    │
               └─────────────────────────────────────┘
                 (a separate project: ~/git/lux)

   Both dude processes share one Postgres, with row-level security.
```

**The backend** (`apps/control-plane`, Bun) is the only thing users and
GitHub talk to. It owns display and management: auth, organizations,
projects, tasks, the board and sidebar (`/v1/navigation`), findings,
forge settings, and the live event stream. It runs no background work.
What changes what runs — deliver, steer, pause, resume, abort, stop — it forwards
to the orchestrator (`src/orchestrator/client.ts`) with a service token, the
user's organization and who asked, and passes the answer straight back.
GitHub webhooks land here, are verified against a per-organization secret
and stored; the orchestrator processes them.

**The orchestrator** (`orchestrator/`, Go) does all the work and serves
no users. Its internal API (`internal/api`) is called by the backend. Its
loops (`cmd/dude-orchestrator/main.go`):

| Loop | What it does |
| --- | --- |
| `workflow` | Advances durable workflows that are runnable or were signalled, each step in its own slot. |
| `phase-sync` | Drives each phase Run on lux: submit, follow its output into the ledger, deliver directives, pause, resume, cancel, and when the agent's turn ends, push, fast-forward the branch, record findings, stop. |
| `phase-notifier` | Turns a finished phase Run into a `phase.finished` signal for its workflow. |
| `webhooks` | Acts on stored GitHub deliveries: syncs the pull request each is about. |
| `automations` | Fires due schedules, matches stored deliveries against event automations, starts queued runs, follows running ones (an agent's turn, a command's exit, a Server's POSTs and idle stop), expires kept sessions. |
| `automations-retention` | Hourly: deletes deliveries after 30 days, empties automation runs' payloads at 30 days and logs at 90. |
| `chat` | Routes stored chat provider deliveries (mentions, kept mentions, clicked choices, an app's removal): replies, records each in Requests. |
| `previews` | Drives branch previews on lux: creates a wakeable preview's lux servers, wakes it (resume with a sync, or submit and attach) when lux's feed says someone opened it, stops it when every server is idle, syncs a running one when its branch moves, reaps unused ones, deletes the servers and cancels the Run of an ended one. Previews from before lux#41 keep the old park-after-idle path. |
| `lux-feed` (goroutine) | Follows lux's tenant event feed (`GET /v1/events`) from the cursor in `lux_feed`, recording wakes, idleness and gone servers of wakeable previews (`servers.Feed`). |
| `pr-reconciler` | Re-reads open pull requests not seen for 15 minutes — the backstop for webhooks GitHub never sent. |

**lux** is the runtime (`~/git/lux`, its own repository and docs). It runs
each phase Run in a container on a Podman host, checks out the repository
at the Run's base commit, runs the agent through an adapter (OpenCode over
ACP), streams what happens, steers and stops it, and pushes its commits
with a credential the container never sees. dude talks to it only through
`orchestrator/internal/lux`.

**The web app** (`apps/web`) talks only to the backend's public API.

**The design system** (`packages/design-system`) is product-agnostic and has
its own [README](../packages/design-system/README.md) with its rules. Browse it
with `bun run gallery` in that package.

### Hierarchy

`Organization → Project → Epic → Task → Run → Session` (§39). A **Run**
is one phase of work, executed as one lux Run. A single *attempt* at a work
item is several Runs — one per phase — and each is shown as an agent in the
UI.

**A task's key** is its project's key and a number counting up within the
project (`BILL-12`). People, agents and a brainstorm session's checkouts
(`repos/<KEY>/<name>`) name a project by its key alone, so **a project's key
is unique in its organisation**, ignoring case (`projects_key_idx`,
migration 095). It is 2 to 6 letters or digits starting with a letter
(`PROJECT_KEY` in `packages/domain/src/projectKeys.ts`), chosen when the
project is made and fixed after; no route changes it. Creating a project
with a key another has is a 409 that names the holder and suggests a free
one. With no key given, dude takes the first free one of
`projectKeyCandidates(slug)`: the slug's first four letters (`billing-api`:
`BILL`); then its words' initials padded with the letters after the last
(`billing-worker`: `BWOR`); then the first with 2 to 9, then 10 to 99
(`BILL2`). The new-project dialog prefills the same key and offers the
suggestion on a 409.

A task gets a new attempt only by **Start over** (a new branch, the whole
pipeline again); **Try again** is a new Run in the same attempt. The task
page shows one attempt at a time, picked in its header when there is more
than one: the branch, pull requests, pipeline, time and cost
(`/v1/tasks/:id/metrics?attempt=N`), findings, sessions and files are that
attempt's, and an earlier one is read-only. A finding, file or pull request
belongs to the attempt of the Run that made it. A pull request whose Run is
unknown goes to the attempt its branch names (`dude/<task>/attempt-N`), else
the current one; a finding or file whose Run is unknown, to the current
attempt (`apps/web/src/attempts.ts`). Chat, Activity and Servers
are the whole task's. Each conductor belongs to its recorded Run attempt,
including an attempt with no phase Run yet. Its session is listed on that
attempt, and an older attempt's conductor is read-only. Historical Runs
retain their recorded attempt; they are not reassigned to the newest one.
The URL names the attempt (`?attempt=1`) only when it is not the current one.

**Stopping a task, and picking it back up.** A person (the owner or an
admin) stops a task with **Stop** (`POST /internal/tasks/{id}/stop`,
`api/stop.go`), whoever decides it: every live Run of the task stops in
one transaction (`delivery.StopAllTx`) — every live phase Run whatever its
attempt (`StopPhaseRunsTx` selects by task, not attempt, so a live Run left
on an older attempt is stopped too) and the live conductor, aborted and kept
in lux for a resume (`runs.keep`; `phases.Syncer.end` keeps an aborted
conductor too), the conductors' children aborted as `AbortChildTx` does — the
delivery is aborted with `State.Halt` naming the conductor and the step it
waited at, and the task is `aborted` (`task.stopped`). While `Halt` stands,
no wake reaches the conductor (`delivery.Wakeable`): Resume takes the kept
one back up and it hears what queued meanwhile. The PR is untouched.
`andRestart` starts the next attempt in the same request. Aborting one Run
(`/internal/runs/{id}/abort`) is still there: a phase Run stops its step's
Runs and the task as Stop does (`delivery.StopPhaseRunsTx`, shared); the
conductor alone changes nothing about the task, and is not kept. A stopped
task is picked back up (`api/recover.go`) by **Resume** (the same Runs, the
stopped conductor among them, its whole conversation), **Try again** (the
step again; under the conductor, a new conductor on the attempt, briefed
with the note and what the stopped one did), or **Start over**. Start over
takes a decider (default: whoever decided the stopped attempt); under the
conductor attempt N+1 gets a new conductor Run of its own, briefed with the
note and an account of attempt N (`delivery.StartPickedUpConductor`). A PR
dude opened is closed; an adopted one (task source `pr`) stays open and N+1
works on its head branch.

**Close and Archive stop what is live.** `delivery.CloseTx` and
`ArchiveTaskTx` refuse nothing for agents working or a PR open: in the same
transaction they stop every live Run of the task, any attempt's
(`StopAllTx`, nothing kept), let go at
once what a Stop kept, end the delivery, then close (`task.closed` names
what it `stopped`). `closePullRequest` closes the task's open PRs on GitHub
after the commit, each with "Closed with <KEY> as <resolution>: <note>",
adopted ones included; without it they stay open. `task.closed` records
only the request (`pullRequestCloseRequested`); what GitHub did is a
`pull_request.action` `close` per pull request, by the person who closed,
with `closed: true` or the `error`, and the sync's `pull_request.closed`
follows only a close that happened. The owner or an admin.

### Delivery, end to end

`orchestrator/internal/delivery/workflow.go` is the state machine;
`store.go` holds its side effects; `policy.go` holds every bound;
`prompts.go` composes what each agent is told from the Markdown in
`agents/`.

```
implement → review (fan-out) ⟲ fix → simplify → [review of its diff ⟲ fix] → [test] → open PR → wait ⟲ (fix on the candidate → review ⟲ fix → simplify → [review of its diff ⟲ fix] → test → move the PR branch) → done
```

- Each phase is a Run with `phase`, `role`, `base_ref` (the commit it starts
  from) and, for reviews, `category`. `internal/phases/spec.go` turns it into
  a lux RunSpec: image, adapter, prompt, model and its tier's effort (inline OpenCode
  config in `OPENCODE_CONFIG_CONTENT`), the LLM's URL and key (`DUDE_LLM_URL`
  env, `DUDE_LLM_KEY` env secret), repository at `base_ref`, egress to the
  LLM's host and the organisation's and project's lists, and the agent's
  home as a state volume so a resume keeps the
  conversation.
- **Handoff is via git.** Each publishing phase pushes to a branch of its
  own (`dude/<task>/run-<run>`) — lux lets a Run's first push go only to
  a branch that does not exist — and the orchestrator fast-forwards the work
  item's branch to it through GitHub, never forcing. The next phase checks
  out that commit.
- **A task branch only ever fast-forwards**, but for one case
  (`internal/advance`: a phase's publish, a conductor's, and the promotion
  of a pull request's checked candidate): a writer refused only because
  its base was merged into the branch under it — dude's own update, a
  person's **Update branch**, GitHub's — is merged in on GitHub
  (`/merges`), and that merge is the next phase's start, or the pull
  request's head. "Only merges" is read from GitHub's compares. Every commit on the
  branch's first-parent line since the writer's start must be a
  two-parent merge. The base must contain its second parent. It must be
  GitHub's verified `web-flow` merge. Its changes must be exactly its
  automatic merge's, with no extra edit. A manually resolved or unknown
  merge is refused. Anything else under the writer is refused as before:
  a person's commit, a rebase or a force-push. The PR
  syncer asks for no automatic update in a sync that has just signalled
  the workflow: fewer such merges, not none.
- **Every Run is on a machine size.** The organisation's sizes
  (`machine_sizes`) are named by each role's settings and a project's
  previews; the spec carries the size as `resources` and its pool, by
  lux's id, as `placement.poolId`, and `runs.machine` keeps what it runs on.
  A parked Run resumes on the size its settings name now (lux#51), in the
  same pool; `runs.machine` then records what lux applied. Design:
  [`design/machine-sizes.md`](design/machine-sizes.md).
- **Every agent runs on a model tier, never a model.** The organisation's
  tiers (`model_tiers`) are named by each role's settings; a tier names the
  model dude requests from the LLM proxy and its reasoning effort. The spec
  declares that model under `llm-anthropic` (`claude-*`) or `llm-openai`
  in `OPENCODE_CONFIG_CONTENT`, with the tier's effort as model options,
  and `runs.model_tier` / `runs.model` / `runs.effort` keep what was
  requested. A role on no
  tier, or on one with no model, fails its Run saying so. Design:
  [`design/model-tiers.md`](design/model-tiers.md).
- **dude can also call a model directly**, with no agent and no tools:
  `llm.Client.Complete` sends one OpenAI-compatible Chat Completions
  request to the proxy (`/chat/completions`, the same for every provider),
  and every such call is recorded, redacted and without its headers, in
  `model_calls`. On it, `internal/judge` asks the organisation's judgement
  tier (`organizations.judgement_tier_id`, Off by default) one question
  and reads back yes, no or unsure with a confidence, failing closed.
  Nothing calls it yet. Design: [`design/judgements.md`](design/judgements.md).
- **Publishing is a property of the phase** (`Publishes`): a reviewer's
  container is never pushed.
- **Project secrets reach the phases each names** (`phases/projectsecrets.go`),
  never the conductor or brainstorm. **Organisation secrets**
  (`organization_secrets`, migration 210) are the same one level up: each
  ticks its recipients for every project, may be ticked for none (then only
  automations that pick it get it), and a project secret of the same name
  wins in that project. Every reader that gives values goes through
  `secrets_in_force(org, project)`: phase Runs, previews, the round-resume
  checks. The judge's redactor reads both tables instead, so an overridden
  organisation value is redacted too. One redactor
  (`internal/redact`) runs
  where agent text enters dude: `ledger.Append`, dude's tool calls,
  `run_diffs`, served text artifacts; and the work is scanned before lux
  pushes it (`phases/pushscan.go`). A Run's values come from
  `run_secret_values` (migrations 130, 210), organisation values included.
  Routes: `/v1/organizations/:id/secrets[/:name]` mirror the project's
  (`api/routes/orgSecrets.ts`; admins change, members read, "Picked by" for
  admins). Operator view:
  [operations](operations.md#project-secrets),
  [organisation secrets](operations.md#organisation-secrets).
- **A turn is done when the agent goes busy then idle**, as lux's shim
  reports it in the output stream, in order with the agent's messages.
  The exception is a turn that ends with something open for a person: a
  question (`ask_person`), or a repository requested with `wait: true`.
  Then the Run is **waiting** (`runs.waiting_since`), and the answer starts
  its next turn.
- **Reviewers report findings** as YAML in their reply; `delivery/findings.go`
  parses it. Only review and test Runs may report. A reviewer (not the
  tester) may instead ask a narrow factual question, a `question:`
  document (`ParseQuestions`): kept in `review_questions` across retried
  reports, shown to the next reviewer of its category in the same attempt
  and listed on the task's Findings tab. It is never a finding and never
  reaches a fixer. The task's owner answers or withdraws it there
  (`delivery.AnswerReviewQuestionTx`/`WithdrawReviewQuestionTx`, the one
  path); under the conductor it is woken with it and may answer it
  (`answer_review_question`), which is not a person's decision. A person's
  answer is one of the task's decisions. An open one holds the pull
  request's opening and a checked fix's promotion (`holdForQuestions`,
  task `awaiting_input`) until it is settled (`review.question_settled`).
- **The tester is on by default and gates the pull request**
  (`delivery/verification.go`). It ends its reply with one YAML document
  per acceptance criterion (`ParseAcceptance`); dude records each in
  `acceptance_verdicts` with the heads the test Run checked out. A `pass`
  whose evidence names no file that Run published is `unverified`; a
  criterion it left out is `blocked`. A failed criterion or a blocking
  finding of the tester's goes back to the fixer, then review, simplify
  and test again, at most `MaxTestFixRounds` (2) times, then
  `test_failed_criteria`; a blocked or unverified one, or a `Test
  environment:` finding, escalates `test_blocked` with no fix. `accept`
  and `waive` on those two need a note and may be taken by the owner or an
  organization admin; they are recorded on the verdict. The tester's
  findings are judged by the next test, never by a reviewer. A verdict
  whose heads what the branch holds has moved past is shown stale; while a
  pull request's fix is checked, that is the heads the candidate started
  from (`candidate.from`), and a verdict at the candidate's own heads is
  shown as the candidate's, not on the pull request yet.
- **Nothing reaches the tester or the pull request unreviewed.** A
  simplifier that changed something is followed by a review round over its
  diff alone (`runs.review_range`, `{of: "simplify", base}`: the reviewer is
  told `<base>..HEAD` in place of `git merge-base`), by the policy's required
  reviewers; its findings go through the fixer and the usual loop, which
  then goes to the test, never to the simplifier again. The round starts
  whoever decides; under the conductor its findings are the conductor's to
  triage, as any round's are.
- **A fix for an open pull request is checked before the pull request shows
  it.** `prFix` starts from the pull request's heads on GitHub, and the fix
  Run, and every fix of it, publishes to the candidate branch
  (`dude/<task>/attempt-N-candidate`, `runs.branch`), not the task branch.
  The fix's range is reviewed (`{of: "pull_request_fix"}`), the review loop
  runs with its bounds, the simplifier runs on the candidate (when the
  policy's `simplify` is on) and its diff alone is reviewed (`{of:
  "simplify"}`, from the candidate's head before it), as for new work; a
  fix of either review stays on the candidate. A failed test's fix is
  reviewed over the fix's range, then simplified again. The tester checks
  every criterion, and only once the
  gate passes or a person settles it does `promoteCandidate` fast-forward
  each pull request's branch to the candidate (`gh.FastForwardExisting`,
  never forced) and refresh, for every open pull request of the task, the
  description's marked Review and Verification sections (`refreshBodies`,
  the verdict refresh's machinery), or the verdict comment of an adopted
  one; a pull request whose refresh GitHub failed is tried again, twice
  more, and the last failure is recorded. Each try is counted when its
  step commits, so a crash or lost lease during a try's GitHub calls
  replays that try uncounted, and a replayed last try can record its
  failure again: the tries are at least once each, not at most three.
  When a push race lets only some branches move, only those are
  refreshed. A candidate's verdict never reaches a pull request before that.
  A branch someone pushed to meanwhile is not moved: `pull_request_moved`
  asks a person (retry from their commit, or wait). Reviews are asked again
  after the move. The round counts once against `MaxPRFixIterations`, in
  `prFix`, and once against `FixRoundsPerPR` of each open pull request
  whose repository its fix published commits to, in `awaitPRFix`: a fix
  that commits nothing spends none of the pull requests' budget, and
  `prFix` refuses only a pull request whose budget is already spent. A
  retry gives the budget back only for a round that was charged
  (`pull_request_moved`, not `pr_fix_failed`). These steps are mechanics under the
  conductor too; it is told only at escalations. A conductor's publish is
  refused while a candidate is checked. `ci_stuck` counts from when the
  current head's checks went pending; finished checks close it as dude's `wait`.
- **What an open pull request may show.** While a pull request is open, its
  branch moves only by `promoteCandidate`, to a candidate head that was
  reviewed and, when the policy tests, tested. This holds for a pull request
  fix, an open pull request's integration, the integration fixer of a base
  merge that conflicts before another repository's pull request opens while
  one is already open (that fixer may change the open one too), and every
  review-loop writer (implement, fix, simplify) that starts while a pull
  request is open on the task branch (`writerCandidate`): an adopted pull
  request's first loop, a review or test the conductor starts at the pull
  request wait and the loop after it, and a fix after a failed test of a
  clean base merge before another repository's pull request opens. A
  person's push meanwhile refuses the promotion (`pull_request_moved`); the
  pull request stays at their commit. An adopted pull request is reviewed
  whole once; with resumed reviewers (`resumeRounds`) each later round
  normally reviews what changed since the head the reviewer last reviewed,
  a person's pushes included. It falls back to a full, fresh review when
  rounds do not resume, the reviewer's conversation cannot be resumed or
  replayed, GitHub cannot measure the change, or the change is over
  `MaxResumeDeltaLines` (300 lines). A refused loop candidate's retry is
  the writer the round opened with, and the findings it resolved only on
  the discarded heads are open again for it. A conductor's publish and its writer
  children are refused while a pull request is open; `start_phase fix` is
  the way to change it. Deliberately outside it are base merges GitHub
  makes: dude's `update-branch` when a pull request is behind, a person's
  **Update branch**, and a promotion merged onto a head GitHub already
  merged the base into. For those, GitHub's "Dismiss stale pull request
  approvals when new commits are pushed" is the recommended branch
  protection setting. A clean base merge GitHub makes before the pull
  request opens (`syncBeforePR`) counts as reviewed: it is tested, and the
  pull request opens at it. A person's Chat message may lead the conductor
  to request reviewers, re-run checks, mark a pull request ready or resolve
  threads, within the conductor's limits; it can never merge a pull request
  or press **Update branch**. As a backstop, `Syncer.publish` fails any
  writer Run whose publish would move a branch with a pull request open
  (as recorded when the publish starts), before it moves anything ("an
  open pull request's branch moves only by promotion of a reviewed
  candidate"), such as a conductor's writer child already running when the
  pull request opened; its conductor's child result says why. A Run that
  committed nothing completes. Under `whenBehind: tell`, a pull request
  dude opens while a conductor's writer child is still publishing is not
  seen by that publish; under `update` (the default) `syncBeforePR` waits
  for live writers, so the window does not exist there.
- **Every loop ends on a declared bound**: `MaxReviewIterations`,
  `MaxAttemptsPerFinding`, `MaxStillRounds`, `MaxRegressionRounds`,
  `MaxPRFixIterations`. Convergence asks for the owner's direction when a
  blocking criterion stays unmet or fixes keep causing blockers; retry and
  amend need direction, and accepting the named risks needs a note. A stuck
  retry also needs direction. These note requirements do not grant admin
  access. An escalation stops the workflow and sets the task to `awaiting_input`.
- **Who decides** is the delivery's `State.Decider`: `policy` (Deliver: the
  rules above) or `conductor` (`delivery/decisions.go`, `conduct.go`). Under
  the conductor the workflow still does the mechanics, and where it would
  apply a rule it parks on `conductor.decision` and records a reason to wake
  the conductor (`conductor_wakes`); the syncer delivers a task's reasons as
  one bounded note (`delivery/wakes.go`). The same bounds refuse the
  conductor's `start_phase` and `decide`. A person's first Chat message
  hands a delivery in progress to the conductor; Talk it through starts one
  it decides; "Let Deliver finish it" hands it back. The conductor may also
  steer a live phase Run of its task's current attempt (`steer`, whoever
  decides): the same `delivery.Steer` a person's steer goes through, marked
  the conductor's (`directives.conductor_run_id`, `run.steered` by its Run);
  its read or failure wakes it (`steer_read`, `steer_failed`).
- **An escalation is still a person's** under the conductor, which is told
  to explain and propose. Its `ask_person` while the delivery waits at
  `decide` is the escalation's question (`questions.escalation`, each
  choice naming an action): the owner picking a choice decides it as the
  banner does (`delivery.DecideEscalationTx`, the one path for both). A
  choice whose action needs the person's note (`NeedsNote`) carries the
  conductor's proposed `note` (`questions.items`), its label composed as
  "Waive: <note>"; the owner's pick decides with that note as theirs
  (`EscalationAnswerTx`). A free answer of the owner lets the conductor
  decide this escalation once
  (`decide_escalation`), recorded as its decision on that answer. The banner
  closes an open question about it (`question.closed`).
- **A task keeps up with its base by merging, never rebasing**
  (`delivery/integration.go`). Writers (implementer, fixer, simplifier,
  conductor) have `sync_base`: lux fetches the base into the checkout as
  `lux/<base>` without moving it, and the agent merges it. Before a pull
  request opens, with `whenBehind` `update`, the workflow asks GitHub to
  merge the moved base into the task branch (`/merges`, `merge-base` on the
  activity) and tests the new head again. A conflict, there or on an open
  pull request, starts one integration Run per head/base pair
  (`State.IntegrationPairs`): a fixer told to `git merge lux/<base>` and
  resolve keeping both sides' intent, then an internal review that runs
  `git show --remerge-diff` on the merge, then the policy's test; the
  conductor never runs tests. On an open pull request the integration is
  checked as a pull request fix is: the integration Run, and every fix of
  its review and test, publish to the candidate branch, and
  `promoteCandidate` fast-forwards the pull request to the merge only once
  its review (and the policy's test) passed; whether it still conflicts is
  read after that. A pair already tried, an integration Run
  that finishes without publishing to the conflicting repository (no
  merge: nothing to review), or a pull request
  still conflicting afterwards, escalates `pull_request_conflict` with
  `integration: true` (retry or wait). An integration Run spends none of
  the pull request's fix budget. With `whenBehind` `tell` nothing
  integrates by itself; the PR card's **Resolve conflicts**
  (`resolve-conflicts`, `delivery.ResolveConflictsTx`, owner only; anyone
  else reads whom it waits on, and while dude resolves it the card says
  so) signals the waiting workflow or decides its conflict escalation, and
  creates no phase itself. Every publish, a phase Run's and a conductor's,
  is counted from GitHub's compares alone (`phases/trustedbase.go`);
  nothing in the checkout is read. The base left out is the commit lux
  reported fetching (`runs.synced_bases`), only if GitHub's base contains
  it and the push contains it: a file whose new name the base changed
  counts as `compare base...push` shows it, under its old name too as
  that compare lists it (each path once), so a pure import counts nothing; any other
  file counts as `compare start...push` shows it. No
  such base, or a compare refused or cut short at 300 files, counts
  `start...push` in full. A file GitHub shows no patch for is binary
  unless it is empty. The activity's integration lines name no
  resolved files, a decision: nothing records which ones conflicted.
  When a pull request merges, other conductors on that base are told once
  (`base_moved`); a parked one hears it with its next note. Design:
  [`design/task-relations.md`](design/task-relations.md), keeping a task
  current with its base.
- A re-review judges every open finding **a fixer has already attempted**:
  each goes to the reviewer of its category (the flavour of the review Run
  that reported it; the reviewer's own word is its `topic`), or, when this
  round has none, to the policy's required reviewer the round has, else its
  first. Those it judges fixed are resolved (`phases.RecordFindings`). That
  rule is what lets the loop converge.
- **A later review round's reviewers come from its own change**
  (`RoundReviewers`, `steps.roundReviewers`): after the attempt's first
  completed review round, a round runs `alwaysReviewers`, the categories of
  its attempted findings, the conditional rules matched against the paths
  changed since the previous review round's checkouts (`Store.RoundDelta`,
  one GitHub compare per repository, cached in `roundDelta` and shared with
  the resumption's line count), and `requiredReviewers` when one of those
  paths is outside `nonProductPaths` (matched against the in-repo path
  only, unlike conditional rules, so `docs/**` does not cover a repository
  named `docs`). Unmeasurable (no forge, refused, truncated) or over
  `MaxResumeDeltaLines`: the first round's `ReviewersFor`. A retry, a
  directed review and the simplifier's range keep their own categories.
  The round's `run.created` carries `reasons`; a round with no reviewer
  goes straight to `awaitReview`, unless a gate waits on a completed review
  at its heads (`st.Candidate`, or an untested conductor head): then
  `requiredReviewers` run, else the first round's (`steps.gateReviewers`).
  The choice is kept in the Owned `reviewChoice` with the round's creation
  key and heads, so a replayed step at those heads creates the same round.
- **Before the pull request** is the project's `pullRequest` policy (`open`,
  the default; `draft`; `ask`), never the organisation's
  (`ProjectOnlyPolicyKeys`: layer 0 drops it, the API refuses it), and a
  task may override it (`tasks.pull_request_choice`, migration 203; NULL
  is the project's). Only the task's owner changes it
  (`SetPullRequestChoiceTx`, `task.pull_request_choice_changed`), and only
  until a pull request of the attempt is on record. `openPullRequest` reads
  it for the first pull request alone (`PullRequestChoiceAtOpening`; once
  one is on record a retried opening follows its draft and never holds):
  `draft` opens a draft; `ask` holds the delivery at the gate
  the adopted branch uses (`ChoiceGate`, `parkAtGate`, task
  `awaiting_input` "Ready for your review", `State.Hold` with round, heads,
  commits since the owner last looked and per-repository +/- from GitHub's
  compares). The hold takes `human.feedback`: open items start a fix round
  (`feedbackAtGate`) that comes back to the hold at the new heads, and Open
  or Draft is refused while any is open (`feedback_open`). Changing the
  choice while held releases or re-reads it (`HeldByChoice`); heads that
  moved under the hold ask again. **Done without a pull request**
  (`DoneWithoutPullRequestTx`, owner only, refused once one is on record)
  stops what runs, keeps the branch, records
  `task.done_without_pull_request` and finishes the task `done` with
  `nothing_to_merge`. Feedback at the hold never reaches GitHub. Under the
  conductor the choice is not read: every conducted delivery enters the
  gate (`GateRequired`, `ChoiceGate` false), the person answers Open or
  Draft to its gate question, and the project's `draft` is not applied to
  an Open there; `draft` and `ask` are Deliver's own.
- **The PR loop** (`internal/prs`, `forge/classify.go`): a webhook says which
  PR changed; the orchestrator reads that PR, records what changed, and
  signals the workflow only for what the classifier deems actionable — a
  change request or a failing check. Merged → task `done`; closed →
  `aborted`. Once none is left open, the end reaches the delivery at
  whatever step it is parked on (`delivery/pr_ended.go`): live phase Runs
  are aborted, a pending conductor decision dropped and an escalation
  withdrawn (`task.decided` by the system), and a finished fix is dropped
  rather than opened or promoted. One closed beside one still open is the
  pull request wait's to ask a person about (or, held by a reviewer's
  question, that hold's); the step at work goes on. A person who chooses
  wait is not asked about those closes again (`State.WaitedPastClosed`):
  not by the close's own signal, not by a later round on the open one, and
  a later fix opens no new pull request where they let one go; a further
  close is asked about.
  dude's own closes (Start over, Close) reach no live
  delivery: the syncer signals only the one on the pull request's branch.
  Under the conductor, ready to merge (or no longer) is dude's
  line in Chat (`chat.notice`) and wakes nobody; a pull request merged or
  closed wakes it once (`pr_merged`, `pr_closed`). A comment that
  mentions `@dude` (or a factory login), by someone who may wake a fixer,
  is not fixer feedback: it is a `chat.message` to the task's conductor,
  once per comment (`delivery.MentionTx`), which changes nobody's
  decisions; the conductor answers on the pull request
  (`reply_on_pull_request`, `dude pr reply`), and its replies, marked
  `forge.ReplyMarker`, are its own when read back.
- **A fix dude pushes goes back to its reviewers.** GitHub takes a reviewer
  off `requested_reviewers` once they submit, so after the PR loop's fix
  lands (`awaitPRFix`) dude asks again everyone whose latest review is on
  an older head and who is not asked already (`forge.StaleReviewers`,
  `delivery.RerequestReviews`): never the author, never an App's account
  (`[bot]`, which reviews every push unasked), never after a head someone
  else pushed on top. Best effort, recorded as
  `pull_request.review_rerequested` with whom GitHub took and refused. It
  runs before the fix opens a pull request for a newly changed repository,
  so nothing that opening waits on leaves the open ones unasked. One
  caller per pull request and head at a time: a session advisory lock held
  across the whole GitHub sequence, and a `pull_request_rerequests` row
  written before GitHub is contacted and finished with its event (see
  `RerequestReviews` for what an interrupted holder can still repeat).
- **People's feedback lives in dude** (migration 127, `delivery/feedback.go`,
  `routes/feedback.ts`). A person drafts items privately — only their
  author reads, changes or removes one, and drafting writes no event — then
  submits several as a batch (a submission key makes a retry the same
  batch). An item is general, on lines of a Run's stored diff (excerpt,
  base and head read from `run_diffs`, never the client), or on a preview
  page; it belongs to the task's current attempt. Who hears first is who
  drives: the conductor when it decides (a `feedback` wake reason, ids
  only); else the current attempt's implementer or fixer still in its turn,
  as a non-interrupting directive dude renders with each item's words; else
  the delivery: the pull request wait and the reader's wait
  (`awaitRead`, where work with no pull request now waits until marked
  done) take `human.feedback` to a fix, and the review loop, `openPullRequest`,
  the conductor's `open_pull_request`, readiness and dude's merge treat
  open and delivered items as blocking whatever the severities. Every
  implement, fix and pull request fix is given the open items
  (`runs.feedback_item_ids`), each a fix attempt bounded as a finding's.
  A pull request fix checked on its candidate gives its fixers the open
  items, but the candidate's review loop weighs its findings alone: what
  its fixers leave open is taken up after promotion (or a fix that changed
  nothing) by another `prFix`, within `MaxPRFixIterations`. An
  agent replies (`replied` holds nothing) or resolves with a commit, which
  holds until its Run publishes and GitHub's compare shows the commit in
  the published head and new since the Run's base. A resolution on a
  candidate is checked against the candidate's head as its Run finishes
  (reopened if it does not hold), but becomes verified only at promotion
  (`CandidateMovedTx`, in the repositories moved): until then it blocks as
  an open item does. A round that ends without promotion (refused by a
  person's push, a retry's `prFix`, a person's stop or a Run abort)
  reopens it for the next fixer. A candidate is promoted even while an item
  its fixers were given is still open; the gates above hold that item. A
  merge on GitHub's own page is not blocked.
- **Steering** goes to the agent as a message, read at its next step: the
  harness takes it while a tool runs and the model reads it before its
  next call, in the same turn, without cancelling the tool. A harness that
  reads messages only between turns takes it when the turn ends.
  `interrupt: true` stops the turn so it is heard now; it is only ever a
  person's explicit choice. lux answers each input once with a `lux.input`
  record, `{requestId, phase: "accepted", lands, receipt, text?}` or
  `{requestId, phase: "failed", error}` (never accepted); what follows an
  acceptance has record types of its own, `lux.input.consumed
  {requestId}` (only when `receipt`) or `lux.input.failed {requestId,
  error}`, so a reader of `lux.input` alone sees one answer per input. An
  older lux writes one `lux.input` with no phase, on handoff (`text`) or
  with an `error`. A directive is `sent` when lux has it, `accepted` when
  the harness took it (`run.directive.accepted`, with `lands`:
  `next_step` or `next_turn`), and `delivered` when the agent's step has
  it (`run.directive.delivered`, from `lux.input.consumed`; at once for an
  acceptance with `receipt: false`). An older lux's handoff counts as
  delivered. `failed` (`run.directive.failed`, with lux's `error`) is a
  steer lux says will not reach the agent. Each is recorded once per
  directive and Run. Precedence: delivered is final, and
  a later failure changes nothing; a failure is final against `accepted`;
  a read receipt (or an older lux's handoff) after a failure wins, since it
  is the agent's own report that it has the words: the failure and its
  error are cleared and `run.directive.delivered` is written.
  "Interrupt now" on a queued steer is a directive superseding it with the
  same words and `interrupt: true`. Its `resends` names the root
  instruction (an Interrupt now on an Interrupt now names the first), so
  repeated clicks are one instruction. Whether it carries the words is
  decided at its first send attempt and then fixed (`interrupt_only`), so
  every retry sends the same request: if a directive carrying the words is
  with lux and not failed, lux is sent the interrupt alone; if the root
  failed or was never sent, the interrupt carries the words, and the agent
  hears them once. lux sends no receipt for an interrupt alone: it is
  delivered in the same transaction as the directive carrying its words
  (consumed, or a legacy handoff), or when sent if that one was already
  read, each time with `run.directive.delivered` `{interruptOnly: true}`
  (never `read`). If the words fail and no directive left carries them, the
  interrupt fails with the same error, one `run.directive.failed` each. lux
  carries steers an interrupt left unread into the next turn, under the same
  request ids, so the root is normally consumed in the turn after the
  interrupt; they fail only when the Run stops or the harness errors. An
  older lux fails them on the cancel: the root fails, the interrupt fails
  with it, and the transcript shows one failed steer with Retry. The
  transcript folds every attempt into the root's turn, at the position and
  time it was read, even when the click came after the read.
- **Pause** stops the lux Run, keeping its workspace and session; **resume**
  continues it, on any host, with the agent's conversation intact.
- **A starting agent says where it is.** lux's `stage` events on an agent
  Run's stream become `run.stage` `{stage, since, epoch, reason?}`, one per
  change while it starts (`phases/stage.go`); a move's stop is `park`; a
  start that fails puts its stage and lux's reason in `run.failed`. The
  web's projection folds them into `Conversation.start`, which the chat's
  row, the header's chip and the composer read. Design:
  [`design/agent-startup.md`](design/agent-startup.md).
- **Parking.** dude pauses a Run itself (`runs.dude_pause`) so that one
  waiting on a person holds no host. The syncer resumes it on its own when
  the reason is over:
  - `person`: waiting past the project's `parkAfterMinutes` (default 10). An
    answer or a decision resumes it.
  - `idle`: quiet mid-turn for `idleNudgeMinutes` (off by default), nudged
    once, then quiet as long again. The task goes to awaiting input
    (not for a conductor, which changes nothing about its task),
    and only a person's Resume takes it up.
  - `repository`: stopped a moment so the resume can bring in a repository
    a person approved.

  A person's own pause always wins. The chat shows each park and resume
  (`run.parked`, `run.unparked`). A phase Run has a **hard limit of
  running time**: dude sends lux `timeout` (`agent.timeout`, 4h), lux
  counts running time only, and a Run past it fails. The conductor has none.
  lux sums it over every resume of the Run, and a resume cannot raise it,
  so a next round (a reviewer's, a tester's, a fixer's going on from the
  last writer) goes on from its predecessor's lux Run only while what is
  left is at least half the limit and at least the chain's longest round;
  with less it is replayed on a lux Run of its own (`run.created` `fresh:
  time_budget`; delivery's `timeSpentTx`). As a backstop, a resumed round
  lux stops at its limit is replayed in its slot once (phases'
  `holdTimedOut`); a replay or a first round lux stops there fails, not
  kept, so Try again starts the step on a new lux Run.
- **Asks die with their Run.** Questions and repository requests still open
  when a Run ends are cancelled (triggers). Answering one returns a 409 that
  says so. Design: [`design/agent-tools.md`](design/agent-tools.md).

### Servers and branch previews

`orchestrator/internal/servers`; the API is `apps/control-plane/src/api/routes/servers.ts`.

- **A project's servers are recipes** (`project_servers`): a port, a
  command, a workdir in the repository, a setup step. A person adds one to
  a task's Run, and lux runs it as `sh -c "[setup &&] <command>"` in
  `/workspace/repos/<repo>/<workdir>`, giving it a URL. The command is a
  shell line as typed (`PORT=3000 npm start`, `cd web && npm run dev`), not
  exec'd: lux stops a server by signalling its whole process group.
- **lux owns a server's state.** dude keeps none: it asks lux, and passes
  lux's Server object on as it came. lux's `server.*` events on a Run's
  stream become `servers.changed`, and the browser reads again.
- **Which Run a task shows**: its agent at work (the publishing one), else
  its live branch preview, else none. A live preview behind an agent at
  work is still named (`preview` in a task's servers), so it can be stopped.
- **A migration stops servers.** lux does not restart them after a move
  (only a spec's servers start on every start); `moved` says so, and a
  person starts them again. A move is also not the agent dying: a Run lux
  stopped with a move's reason (`lux.Moved`) is left running.
- **A branch preview is a Run with no agent** (`runs.kind = 'preview'`, no
  phase). One per task. Steer, pause, resume and abort refuse it; it is
  stopped with `DELETE /v1/tasks/:id/preview`, or ends when its task does
  (done, failed, aborted). A stopped preview's lux Run is cancelled, a lost
  one too (lux keeps a lost Run to resume).
- **A wakeable preview** (`runs.wakeable`, lux#41) is lux servers of its
  own (`preview_servers`), one per recipe marked to start in previews, at
  `<server>-<task key>-<project slug>.<preview domain>` — one DNS label
  (`servers.PreviewLabel`), chosen once when the server is created and
  kept: a server is found again by its `dude.preview` label, never by
  hostname. lux owns the URL and says on its feed when
  someone opens one (`server.wake_requested`) or none is used
  (`server.idle`); `servers.Feed` records it and the preview loop acts.
  Its Run is servers-only (no `workload.servers`), resumed with `sync` on
  every wake, or replaced by a new one when its last start failed before it
  ran (`runs.start_failures`, bounded). See [operations](operations.md#branch-previews).
  Its first wake waits up to `firstWakeTimeoutMinutes` (default 15); after
  a server has been ready once, `wakeTimeoutMinutes` (default 5). Both are
  project settings, whole minutes 1..120. `runs.preview_ready_at` preserves
  that fact across sleeps and replacement lux Runs; the sweep patches lux
  servers' timeouts, never shortening an open wake.
- **An old-style preview** (`wakeable = false`: before lux#41, or a lux with
  no preview domain) serves the recipes as the spec's `workload.servers`.
  It is parked (`dude_pause = 'unused'`) after the project's idle timeout
  without a request, by lux's `lastRequestAt`; starting a server on it
  resumes it.
- **Previews open in a new tab**, never in a frame: a preview's sign-in
  cookie is SameSite=Lax and does not reach a cross-site iframe. Only an
  `https://` server URL becomes a link.
- **Commenting on a preview** (`servers/commenting.go`): every wakeable
  preview server carries lux's `inject`, one tag loading
  `<auth.public_url>/feedback/overlay.js`, derived from the origin at each
  create and sync and never stored; only when `auth.public_url` is set and
  lux's whoami says `serverInject`, otherwise no `inject` is sent. The
  servers read gives each such server `commenting` and `needsRestart`
  (lux's reported `inject` is not the current tag while its Run is up). See
  [operations](operations.md#commenting-on-previews).
- **Egress entries are what lux takes**: `*`, a hostname, an address, a
  CIDR range, or a wildcard `*.example.com` (lux#68: `*.` then a domain of
  at least two labels, no port or path; it matches names under the domain,
  not the domain itself). The API refuses others (`egressProblem` in
  `@dude/domain`), the fake lux refuses them in lux's words, and the
  orchestrator leaves out a preview's saved before it did and refuses to
  start on one in `agent.egress` (`lux.ParseEgressRule`).
  Previews and agents share the rule; an agent's list is its organisation's
  and project's (operations, "Agent network").
- **Online and last seen mean a person used dude**: a shown, focused tab
  that had a pointer, key, wheel or touch, focus, or being shown
  again, in the last five minutes (`ACTIVE_WINDOW_MS`, apps/web
  `activity.ts`; opening the page counts). Only then does the web app send
  `x-dude-active: 1`, and only a person-session request carrying it touches
  `people.last_seen_at` and `last_seen_where` and announces `person.seen`
  (`router.ts`, `presence.ts`). API-key requests never establish presence,
  even with that header; `api_keys.last_used_at` records key use separately.
  Signing in does not record a presence timestamp. Ordinary automation and
  the event stream do not send the header; presence test fixtures may.
  `online` is `last_seen_at` within five minutes (`person_ref()`), so someone
  goes offline five to ten minutes after their last input. While a tab is in
  use the people's minute read is the heartbeat; background polling stops
  while hidden (`useVisibleInterval`), not every event-driven request.

### Organisation sessions, admin-owned sessions and mentions

A brainstorm session's **general access** (`sessions.access`, migration 190)
is `private` (its accepted people) or `organisation` (every active member of
its organisation can open it and chat). The change is one way: `PUT
/internal/sessions/{id}/access` takes only `organisation` (anything else is
409), and the `sessions_access_one_way` trigger refuses going back whoever
writes the row. Who may do what is `session_role()` alone, which the
orchestrator (`delivery.SessionRole`), the ledger's visibility, artifacts and
the notifier all read: in an organisation session a `read` member counts as
`chat` and any active member without a row is `chat`; someone removed, or of
another organisation, has no role whatever rows they left. Writing as a
non-member inserts their `chat` row under the session's lock
(`delivery.JoinByWriting`, `session.joined` with `via: organisation`).
`GET /internal/sessions/open` lists the open sessions with `joined` per row;
the sidebar's Open to everyone count is those joined and not archived.

**When the owner is removed** from the organisation (`removePerson` →
`passOnSessions`, in the removal's transaction), each session of theirs that
someone else can reach — open to the organisation, or with any other row —
gets `owned_by_admins` (`session.owner_left`); a private one nobody else is in
is archived with them. While it is set every active admin is `owner` in
`session_role()` (and may open it); `POST …/take-ownership` makes one of them
the owner row and clears it (`session.owner_taken`), else 409. Handing it on
waits until an admin has taken it. `removePerson` takes the `session:`
advisory lock (`lockOwnedSessions`, before the person's row) of each owned
session its leaving changes, at most 500 a transaction (the rest pass in
transactions of their own), and voids the departed owner's pending handover.
A removal that fails between batches is finished by `DELETE` again (200,
`{passed}`); otherwise an already removed person is a 404. The backend never
waits on the orchestrator here: it only kicks it (unawaited, 1 s). The
orchestrator's owner sweep (`notify/owners.go`, in the notify loop, its own
watermark `session_owner_sweep` and done set `session_owner_done`, migration
192) reads `session.owner_left`, `session.owner_returned` and `person.removed`
from the ledger: it tells each session's live agent
(`notices/told/session-owner-left` / `-returned`) once, unless an owner change
was recorded for the session since, and withdraws every open question put to
a removed person by name, in any session. A change that keeps failing is
counted (`session_owner_attempts`, migration 194) and given up, logged as
`session owner change given up` with its cursor and error, once it has failed
five times and first failed over ten minutes ago. The orchestrator's
handover, accept and take-ownership hold the new owner's `people` row FOR
SHARE under the session lock, so a removal either refuses them or sees the
session as theirs; take-ownership also holds the departed owner's row FOR
SHARE, so an invitation bringing them back is waited for (409 `owner_active`).
Take-ownership withdraws what was put to the departed
owner and tells the agent through `notices/told/session-owner`. An owner
invited back while its admins still own the session owns it again: the
invitation's transaction takes the `session:` locks of at most 500 of those
sessions before the person's row, clears `owned_by_admins` and records
`session.owner_returned` (which the owner sweep tells the agent); the rest
follow in transactions of their own of at most 500, each stopping if the
person was removed again meanwhile, plus one more pass after coming back for
sessions a removal batch passed while the accept waited on the row. A return
that fails between batches (a multiple of 500 back, the person active, the
rest still `owned_by_admins`) is finished by accepting the same invitation
again, as a removal is by `DELETE` again, or by an admin taking each session
over: nothing periodic resumes it. Writing a message, answering, filing and
Try again join an organisation session; renaming takes an owner or chat row
(or an admin while its admins own it).

**Mentions** ride on a session message (`mentions`, `shareWith`: person ids).
`shareWith` is the owner's only (403, nothing sent) and adds each as accepted
`chat` in the message's transaction. Each mention of someone `session_role()`
admits (others are dropped silently) is a `session_mentions` row, listed in
`GET /internal/sessions`'s `mentions` while unseen and while the person does
not have the session open; their page's open heartbeat (`setOpen`, which also
covers non-members through `session_viewers`) marks it seen. A
`session.mentioned` event is written only when they did not have it open.
The notifier pushes it from a sweep of its own (`notify/sessions.go`: its
own watermark `push_config.session_after_cursor` and sent set
`push_session_sent`, and index `events_session_mentioned_idx`, migration
191), which also sends a question put to nobody by name to every active
admin while the session's admins own it. The agent reads the words as written; `mentions`
is metadata on the `chat.message`, for the chips.

### Automations

Work nobody started by hand (migration 183; `orchestrator/internal/automations`,
the API `apps/control-plane/src/api/routes/automations.ts`, the pages
`apps/web/src/screens/AutomationsSettings.tsx`). An automation belongs to
the organisation (`project_id` NULL) or a project, and is one of four kinds:
an **agent**, a **command** that runs and exits, a **server** that answers
events, or **Send a webhook** (`webhook`: one signed POST). New ones start off, owned by
their creator.

- **Triggers.** `{kind: schedule, cron, tz}` — five-field cron in an IANA
  zone (`ParseCron`/`Next`: no dependency; a minute a spring-forward skips
  never fires, one a fall-back repeats fires once) — or `{kind: github,
  event, actions[], filters{branch,label,paths,author}}`. The event is any
  GitHub event name, so an App permission added later needs no code, or
  `{kind: incoming, …}` (below). Values
  in a filter are OR'd, filters AND'd; `!login` excludes an author; paths
  read the pull request's files from GitHub and are checked last (`Match`).
  An event dude has no code for still matches on event, action, repository
  and author. The event picker lists what the organisation is subscribed to
  (`/v1/automations/subscriptions`: the App's `events` from `GET /app`, a
  PAT's repository webhook's, kept an hour; else the events seen, saying so).
- **Repositories.** A project automation takes events from its project's
  repositories and checks out all, none or some of them. An organisation's
  `repo_filter` (all / only / except, `*` within a segment) is evaluated at
  match time, so a repository added later is covered at once; on a schedule
  it decides what each run checks out. Projects cannot opt out: a project's
  list shows the organisation's that reach it as "Also running here".
- **Every GitHub delivery is stored** (`receiveWebhook`); the pull request
  sync marks those that cannot change a pull request done in one statement
  (`ProcessDeliveries`), and the automations loop reads each delivery once
  (`webhook_deliveries.automations_at`).
- **Kinds.** An agent is its own (harness, tier, time limit, machine,
  image, prompt) or one of dude's roles that run without a task — Tester,
  Reviewer (`pull_request*` events only), Investigator — resolved for the
  automation's scope as a task's would be (`ResolveRole`, `Sizes.ForRole`,
  `RoleImage`), then the automation's overrides; the role's later changes
  reach every value not overridden. Its prompt is the role's, then the
  automation's text as the task brief. `{{event.name}}`, `{{event.action}}`,
  `{{event.delivery_id}}`, `{{event.payload}}`, `{{pr.number|title|url}}`,
  `{{repository}}`, `{{branch}}`, `{{sender}}` and `{{schedule.time}}` are
  substituted (`Substitute`). Its checkouts use the dude App's tokens and
  bot. A command is `sh -c` in a generic lux Run; the event is at
  `$DUDE_EVENT_PATH` with `DUDE_EVENT_NAME`, `DUDE_EVENT_ACTION` and
  `DUDE_DELIVERY_ID`. An automation's lux Run is not a dude Run (`runs`): it
  has no task or session, so the loop follows it from
  `automation_runs.lux_run_id`.
- **Preset overrides.** Harness, tier, time limit without progress, machine
  and image each show the role's resolved value, "From <role> · <org|project>",
  and "Override for this automation". An override shows "Overridden",
  what the role's value is and Reset (back to following it). Each field is
  independent; `spec()` applies `agent.preset.overrides` after the role's settings.
- **Agent tools.** A run gets exactly `create_task`, `github_search`,
  `github_issue`, `github_pull_request`, `github_check_log`, `github_comment`
  and `post_to_chat`, listed on its page's Agent section as the Tools card
  (DS `AgentTools`, app `automationTools.tsx`).
  The runner issues a stable per-run tools token (`RunToken`, retried starts
  use the same one) into the MCP service and CLI's `DUDE_TOOLS_AUTH` secret;
  its hash and scope live on `automation_runs`. The tool server's sibling
  `lookup_automation_by_mcp_token` answers without a `runs` row; a finished
  run's token is dead. Reads and comments are restricted to the covered
  repositories (project's, or the organisation filter's). `github_comment`
  uses `forge.GitHub.Comment` as the App, on the triggering issue/PR by default
  or a given repo/number. `create_task` files in the automation's project,
  or, for an organisation automation, the project owning the triggering
  repository; with none (for example on a schedule), it refuses clearly.
  The task's `created_by_automation_run_id` and `task.created` event name
  the automation run, automation and owner it acts for.
- **post_to_chat.** Every automation agent has it (no per-automation switch,
  no channel allowlist): `{channel, text}` posts Slack mrkdwn as a new message
  (no thread) through the organisation's Slack installation, as its bot, and
  answers `{channel, channelId, ts, permalink}` (`agenttools/post_to_chat.go`):
  `channel` as people read it (`#engineering`), `channelId` Slack's id, to
  post by again. `channel` is a name (`#engineering` or `engineering`, found
  with `conversations.list`, `slack.FindChannel`, which remembers a channel
  the bot is in for 5 minutes and, past 50 pages of 200, asks for the id
  rather than saying there is none) or an id. The channel is looked up in the
  tool's prepare step (`.before(resolvePostChannel)`), outside the call's
  transaction. `text` is 1–40,000 characters of the agent's own text (Slack's
  limit; past a block's 3,000 it goes as the message's plain text,
  `slack.PostText`); like every tool call's arguments it is redacted with the
  run's secret values before it leaves dude. **10 posts a run**: the 11th is
  refused ("post_to_chat is limited to 10 messages per run"). Only a call
  that reached `chat.postMessage` counts, including one Slack refused
  (`countingAttempts`, counted from the ledger under a lock on the run's row);
  a channel the lookup refused (unknown, the bot not in it, the lookup cut
  short) and a refusal for want of Slack spend none. Refusals say how to fix
  them: an unknown channel or one the bot is not in names `/invite @dude`.
  Each post is a `slack.message.posted` event (channel, channel id, ts,
  permalink) and each refused post a `slack.message.refused` one (its words),
  on the run. Slack's failures count on the installation's health
  (`chat.NoteFailure`, as the chat service's own calls). `chat:write`,
  already among `slack.BotScopes`, covers posting in a channel the bot is in;
  no scope changes.
- **Without Slack.** A run still starts (dude cannot tell from a prompt
  whether it will post). Its `post_to_chat` is refused with "Slack is not
  connected for <org>: an organisation admin adds dude's Slack app in
  Organisation settings → Chat", recorded with code `not_connected` and the
  structured `reason` "Slack is not connected for <org>"; so is a post whose
  installation Slack answers `invalid_auth`, `token_revoked` or
  `account_inactive`. When the agent's turn ends the run ends **failed** with
  "post_to_chat failed: " and that reason (`automations/post_to_chat.go`),
  unless a post went after the last such refusal (Slack connected during the
  run), and its owner is told as for any failure. The refusal it failed on is
  recorded as an `automation.run.failed_on_post` event (`eventId`); a run
  that failed for another reason (timed out, crashed, cancelled) has none.
  The page's Tools card shows the attention callout on post_to_chat's row,
  linking Organisation settings → Chat.
- **A run's tool calls.** `GET /v1/automations/:id/runs/:runId` carries
  `toolCalls`, read by the run's `run_id` from its ledger events
  (`agent.tool.dude`, the attempt before it for its duration, and the
  post/refusal events above), and `failedCallId`, the refusal the run failed
  on (that event's `eventId`, else null); a post's text is never repeated
  there. The run detail shows them under Log, each a `ToolCallCard` beside
  its time (`ToolCallRow`): a post with `icon="slack"`, its answer when
  opened, and "Posted to **#channel** · Open the message in Slack", a refused
  call with its error; the call named by `failedCallId` gets a danger callout
  under Run, saying "Nothing was posted" when no call on the run posted and
  "That message was not posted" otherwise. The Tools card's list is
  the app's copy of the orchestrator's (`tool-lists-automation.golden.md`).
- **Prompt editor and history.** Own prompt and preset brief both use
  `MarkdownDocument`, `{{ }}` inserting `AUTOMATION_PROMPT_VARIABLES`.
  `automation_prompt_versions` (migration 183, the `prompt_versions` pattern)
  keeps every changed save's text, who and when; the first change also keeps
  what it had before. `GET /v1/automations/:id/prompt-history` lists newest
  first; `POST …/prompt-history/restore {versionId}` (editors) saves an old
  version again as the newest. The role pages' `PromptHistory` shows diff,
  whole text and Restore. A read-only viewer reads without Edit/Restore.
- **Delete and Duplicate.** The header's ⋯ menu: Duplicate copies the settings
  off, named "<name> (copy)", owned by the caller, with no runs, kept session
  or prompt history; a webhook copy gets its own signing secret.
  Delete confirms how many runs' history/logs/payloads go with it and whether
  a kept session is ended; the API calls the orchestrator's Start fresh to
  cancel its kept lux Run before deleting the row (and refuses to delete
  if that fails). Both actions require the scope's editor permission.
- **Secrets.** An automation picks secrets by source:
  `secrets: [{source: organization|project, name}]` (migration 210 turned
  the old name lists into project refs). An own agent, a command or a server
  gets exactly its picks: an organisation automation the organisation's, a
  project automation those and its project's. A project automation's
  organisation pick whose name the project also holds resolves to the
  project's value at run start (`automation_secrets_given`); the picker
  shows that row disabled. One of dude's agents picks none: it gets what its
  role is ticked for at both levels (Tester → tester, Reviewer → reviewer;
  the Investigator is no recipient), shown read-only. Values are given as
  environment variables and redact the run's tool calls
  (`automation_run_secret_values`); a resume gives the current value of
  every one again.
- **Checked out None** on a scheduled organisation automation (`repos =
  "none"`) runs in an empty workspace with its image's tools; Some keeps the
  repository filter deciding.
- **Sessions.** Ephemeral (a fresh Run, cancelled at the end) or Resumable:
  one session per automation (`automation_sessions`), stopped and kept at
  the end of a run and resumed by the next firing, until `expiry_days`
  (1–30) pass without a run — whether or not the automation is on. "Start
  fresh now" lets it go. lux keeps no secret's value, so a resume gives
  every secret the kept Run declares again: forge tokens minted afresh, the
  rest (event file, setup manifest, project secrets) from the spec a new
  Run would get, so the event file holds the new event. A Run's env stays as
  first submitted, so `DUDE_AUTOMATION_RUN`, `DUDE_DELIVERY_ID`,
  `DUDE_EVENT_NAME` and `DUDE_EVENT_ACTION` are also written to
  `/workspace/.dude/env.sh` (`EnvPath`), a file secret the resume gives again,
  and the command or server line sources it first: a resumed container sees
  this firing's values. A process that outlives the resume (one the command
  started in the background) keeps the values it started with; it must read
  the event file or `env.sh` again.
- **Concurrency** (`Arrive`): queue (default), merge (the waiting run gets
  every payload, as a list), skip (a skipped run row with the reason),
  cancel and restart, send to the running agent (a resumable agent), or run
  in parallel up to N (ephemeral). Run now on a schedule queues behind a
  live run. A test run (a past delivery or a written test event) is always
  a run of its own, with a fresh ephemeral session, untouched by
  concurrency; it is a real run.
- **Server POST contract.** On an event dude starts (or resumes) the
  container with the command as a lux server, waits for it to be healthy
  (GET its health path, else the port; at most 60 s), and POSTs each event
  to `<path>` with `Content-Type: application/json`, `X-Dude-Automation`,
  `X-Dude-Run`, `X-GitHub-Event`, `X-GitHub-Delivery` and, with a secret,
  `X-Dude-Signature-256`. It reaches the container at the server's lux
  preview URL, whose listener answers 401 without its sign-in cookie: on a
  401 dude mints a preview ticket (`POST /v1/runs/{id}/tickets`), spends it
  on `<url>/.lux/auth` and sends the request again with the cookie, kept per
  lux Run (`previewDo`). Events arriving while it is up go to the same
  container. A 5xx or no answer within 30 s is tried once more; a 4xx is
  recorded; one `automation_run_requests` row per POST. With nothing in
  flight and no request for 30 s it is stopped and the run ends (`idle`).
- **Webhook signing.** `X-Dude-Signature-256: sha256=` and the hex
  HMAC-SHA256 of the body with the automation's secret, as GitHub signs;
  one run row per POST, retried once on a 5xx or no answer. The API never
  returns the secret, only its last four characters.
- **Ownership.** The creator owns it; its owner and the organisation's
  admins transfer it to a current member, who need not be able to edit it.
  Org admins edit organisation automations, a project's editors its own;
  everyone reads; the owner and editors run it by hand. When the owner is
  removed (`removePerson`), the organisation's admins hold it together
  (`owner_id` NULL, `owner_left_at` set): it stays on and its page says so.
  A failed run records `automation.failed` naming the owner, or every current
  admin while held. The Web Push notifier includes it in `asks` (and the
  `events_asks_v2_idx` predicate, migration 184); recipients come from the
  payload's `to`. The message names the automation and reason, linking to
  that run under its organisation's or project's settings.
- **Incoming webhooks** (migration 212; `routes/incoming.ts`,
  `internal/automations/incoming.go`, `screens/automationIncoming.tsx`). A
  third trigger, `{kind: incoming, hookId, verify, conditions[], groupBy,
  ratePerHour}`: the automation's own URL, `POST /v1/hooks/<hookId>`, public
  like GitHub's. Create (and a save that turns a trigger incoming) makes the
  hook id and a verification secret (32 random bytes, base64url, `whsec_`),
  answered once (`incomingSecret`, shown by `SecretOnce` = `KeyShownOnce`
  with "I've stored it"); after that only its last four (`incomingSecretHint`).
  Rotate (`POST …/incoming-secret/rotate`) replaces it at once. A save never
  changes the hook id; Duplicate makes a new one and a new secret.
  - **Verification**, one of three: **Signature** — the hex HMAC-SHA256 of
    the raw body with the secret in a named header, after an optional prefix
    (`sha256=`); **Header token** — the secret in a named header (a `Bearer `
    before it is taken); **URL query parameter** — `?<param>=<secret>`. Each
    compares in constant time (digests of equal length, `timingSafeEqual`).
  - **The endpoint, in order:** unknown hook → 404, nothing stored; secret
    wrong or missing → 401; body over 1 MiB → 413; not a JSON object → 415;
    automation off → 403; over `ratePerHour` (1–600, default 30) in the
    rolling hour → 429. Each refusal stores a row (`incoming_deliveries`,
    verdict `refused`, its code) with no body and answers one line of JSON;
    an accepted request stores its body and answers 202 `{delivery}`. The
    rate counts the hour's deliveries that started a run or were sent to a
    live one, and those the loop has not matched yet (each holds a place
    until it is decided, so a burst is limited before matching); skipped and
    refused deliveries take no place. One delivery counts at a time, under a
    per-automation lock. A header or query secret is checked before the body
    is read. At most 100 refused rows an automation a clock hour are stored;
    past that a refusal only adds to `incoming_refusals` (automation, hour,
    code, n), answered the same, and the Deliveries tab says "N more refused
    this hour". A `bad_signature` row keeps no headers. Headers are kept with the values of
    the configured verification header (by its exact name), `x-amz-*`, and
    any name holding `token`, `secret`, `signature`, `cookie`, `key`,
    `auth`, `password` or `credential` replaced by `••••`.
  - **Matching** (`ArriveIncoming`, a step of the automations loop): a
    delivery whose group key (the value at `groupBy`) is a live run's of the
    automation is **sent to that run** whatever the concurrency, its
    conditions unchecked (a resolved alert reaches the agent that is on it).
    Otherwise every condition must hold — `path op values`, `op` one of is,
    is not, one of (values OR'd), contains (a substring, or an array's
    element), exists; the path is dot keys with all-digit segments indexing
    arrays (`alerts.0.status`); a non-string value compares as its JSON — and
    it fires as an event does (`Fire`: concurrency decides). The verdict
    (`started`, `sent_to_run`, `skipped` with the missed condition, or the
    concurrency's skip) is written on the delivery. A delivery added to a
    live or waiting run (its group's, or by Merge or Send) is kept there by
    reference (`holdFollowUp`; the body is read from `incoming_deliveries`
    when the run uses it), and a run holds at most 50 (`FollowUpCap`): a
    later one is skipped, "the live run already holds 50 deliveries". An
    agent run starts from its first delivery and gets the rest as inputs; a
    command run's event file is the list of all of them.
    `packages/domain/test/incoming-conditions.json` holds the cases the web's
    live check (`conditionHolds`) and the loop's (`Holds`) both pass.
  - **Group by** is required with Send to the running agent (the save says
    so on the field). An **organisation** incoming automation names one or
    more `projectIds` (save refused otherwise): `create_task` files in the
    first; its runs check out those projects' repositories (all, none or
    some).
  - **Variables.** `{{payload.<path>}}`, `{{event.payload}}` and
    `{{event.group_key}}` are the sender's words: quoted as external text
    (the prompt saying so first), each cut at 8 KiB with "… (truncated, N
    bytes)" inside its quote, all of them together at 64 KiB.
    `{{event.delivery_id}}` is dude's and is not quoted.
  - **Deliveries tab** (incoming and GitHub automations): every delivery of
    the last 30 days, its `DeliveryVerdict`, group key (or event) and size;
    a delivery's detail shows each condition's ✓/✗ (`ConditionRows` with
    results), its headers and body, and Run with this delivery. A GitHub
    automation's verdicts come from `automation_github_verdicts`, written as
    the matcher reads each delivery. Run now on an incoming automation takes
    a past delivery's body or a written one (`{payload}`), checked live
    against the conditions (`test-event/evaluate` answers per condition).
    A run's detail names the delivery that started it, its group, and the
    later deliveries sent to it.
- **Retention.** Deliveries and runs' payloads are kept 30 days, run logs 90;
  an incoming delivery's body and headers 30 days (then its row is deleted
  unless a run holds it), a refused one's row 7 and the per-hour refusal
  counts 7 (`automations.Retain`, hourly).

### Chat providers (Slack first)

Someone writes `@dude …` in a team chat and dude starts a session, files a
task, opens an epic with its tasks, resumes a session or asks first, and
replies in the thread with a link. Migration 195 holds it all, keyed by
`provider` (an adapter's id): `chat_installations` (one workspace ↔ one
organisation, unique across tenants; the bot token treated as the forge
token is — behind RLS, read only by `dude_app` (the sweeper's grant names
the routing columns alone), returned by no route, kept out of agents,
logs, stored errors and model-call records (the Slack adapter redacts it,
and the app's secrets, from every error it returns), and deleted with the
installation on a disconnect, an uninstall or a revoked bot token; like
the forge token it is plaintext at rest, see What's next), `chat_identities`
(a provider account linked once to one account per workspace, never by
email), `chat_connect_states` (one-time, 15 minutes, as sha256),
`chat_events` (deliveries, keyed on the provider's event id), `chat_threads`
(a thread bound to one session, task or epic), `chat_requests` (the routing
log, 30 days, one per delivery), `organizations.chat_instructions`, and
`origin` on tasks, epics and sessions.

**The provider interface.** `orchestrator/internal/chat` is provider-neutral:
a `Mention`, `Event`, `Interaction`, conversation kinds (`public`,
`private`, `group`, `dm`), `Reply`, `Question`, `Reaction`, and the
`Provider` interface (verify and parse webhooks; read a thread, a page of a
thread or channel, the channel before a message, a
conversation's kind and members, a user; post replies, ephemerals,
questions; update a question or a reply; react; OAuth install and identity URLs and
exchanges; revoke). Routing, the checks, binding, session access and
Requests (`route.go`, `make.go`, `decide.go`) use only those. The Slack
adapter is `internal/chat/slack`; `internal/fakeslack` fakes its Web API and
OAuth pages as strictly as Slack and signs payloads (`cmd/fake-slack` serves
it for the E2E suite). The web draws a provider from its entry (name and
glyph id, `GET /v1/organization/chat`); shared components take it as props.

**Adding a provider**: an adapter implementing `chat.Provider` in
`internal/chat/<id>`, registered in `cmd/dude-orchestrator/main.go`'s
`chat.Registry` under its id; its config keys (as `slack.*`); a glyph in the
DS icon set (or it draws `integration`); its name in `apps/web/src/chatOrigin.tsx`'s
`KNOWN_NAMES`; a fake for its tests. Nothing else changes: routes, tables,
settings pages and Requests are by `{provider}`.

**The flow.** The backend is still the only public surface: a provider's
webhook (`POST /v1/webhooks/chat/{provider}/events|interactions`) is passed
to the orchestrator with its body and its own headers, bounded at 2.5 s;
`chat.Service.Receive` verifies it (Slack: v0 signature, 5-minute skew),
answers URL verification, stores it in `chat_events` (a retry is the same
row) and answers at once. The `chat` loop then, per delivery: installed?
(else ignored: nothing to reply with) → linked? (else a private Connect
button to `#/o/<org>/connect/<provider>?state=`, the mention kept against
the state and routed once they link) → an active member? (else a generic
private refusal) → a bound thread? (a task or epic: its link, no model; a
session: the message and the thread since, group members joining on each
mention) → one routing judgement (`judge.Service.Structured`: the Judgements
tier, its redaction and `model_calls` record, asker `chat.route`), told the
built-in instructions, the organisation's, then the provider's, with the
thread (non-members marked), the projects and epics and the person's chat
sessions of 14 days → checks (project, epic, session, no cycle, at most 12
tasks, 2–4 choices; under 75% or a failed check asks while choices can be
built and fewer than 2 were asked, else a brainstorm, as any failed call)
→ make it (public: an organisation session; private or group: shared as
chat with each connected member, the rest recorded skipped; DM: private,
never bound; tasks in the Backlog, not started, with origin and blocked-by)
→ bind the thread, reply with the link, record the request. A
delivery handled twice makes nothing twice (`chat_requests` is unique per
delivery). An install or a link is begun by a signed-in browser (an HttpOnly
cookie whose hash the state holds) and finished only in that browser.

**What routing reads (`chat/context.go`).** Thread mentions use `FetchThread`;
top-level mentions use `FetchHistory`: at most 30 messages from the preceding
24 hours, oldest first under `## The channel before it`. `quoteMessages`
drops dude's own messages, names other apps as apps (never people), and clips
each message to 2000 characters. Slack text includes attachment and block
text and links, with repeats removed (`chat/slack/text.go`). Channel context
keeps the newest messages within 12 000 characters and counts those omitted.
The retained context is stored on the decision for retries, hashed into
`context_hash`, recorded with the routing call, and quoted as untrusted text
in the session briefing. Origins and goals retain only that bounded context;
task lists and navigation strip quoted thread/channel text from origins.
A refused channel read is noted on the installation and routing continues
without it; transient failures retry the delivery, as thread reads do.

**Hints, never rules.** Routing sees the projects of the conversation's last
20 requests (`chat_requests_conversation_idx`) and its usual projects
(`chat_channel_projects`). The message and context override both hints.
An admin sets the usual projects on the provider's settings page
(`PUT /v1/organization/chat/:provider/channels/:conversation/projects`) or
from chat (`@dude this channel is usually about Acme`, the `channel_projects`
route; anyone else is refused privately and nothing changes).

**Link a project (`chat/link.go`, migration 203).** A mention routed to a
brainstorm that ends up with no project linked gets a row under its reply:
a choice per candidate (at most 3, labelled `KEY · Name`) and a link to
the session page ("Choose in dude"). Candidates, in order and once each:
the conversation's usual projects, its learned hint (most first), then the
routing answer's `maybe_projects` (read for a brainstorm with no project
only, checked against the routing world like `project`, at most 3). The
world holds no hidden project, so none is ever offered. The provider-neutral
shape is `Reply.Row` (`Choice`s, a link, and `Linked` lines); Slack renders
it as buttons (`blocks` in `chat/slack`) and replaces it with
`UpdateReply` (`chat.update`). What was offered is kept on the request
(`chat_requests.link_offer`) and the reply's message id with it
(`question_message_id`). A click comes back as an `Interaction` whose value
is `link:<request id>:<index>`: the index is resolved against the request's
offer server-side (the request must be the click's conversation's), so a
forged value links nothing that was not offered. Only the person who asked
or an admin of the organisation may link; anyone else, a non-member or an
unconnected user, is told so privately and nothing changes. The link goes
through `delivery.AddSessionProject` (the project with all its
repositories, beside the session's links, through `SetSessionLinks`, the
same rules as the web's link: fenced, organisation-scoped), so a project
hidden after the row was posted is refused at the click. It records
`session.linked` (payload `linked`, `via`, `viaName`: the session's Chat
reads "Márcia linked ABS from Slack.") and a `chat_request_links` row, once
per request and project (a second click or a retry links and posts
nothing twice; the click's own retry still posts the update once), which
Requests shows as "Márcia linked ABS from Slack". The session's agent gets
the repositories as the web's link gives them: a Run not yet submitted
checks them out, and a live or parked one is resumed by the syncer with
them added (`sessionRepoMissing`, `sessionResume`), told where each is.
The 👀/✅ marks are unchanged.

**Hidden projects (`chat_hidden_projects`, migration 196).** An admin hides
projects from a provider on its settings page only (`PUT
/v1/organization/chat/:provider/hidden-projects`), never from chat. Hiding
applies to new work:
- A session started from that provider's chat (`sessions.origin.provider`)
  never sees a hidden project, nor do the investigators its agent starts:
  `SessionProjects`, `OrgProjects` and `SessionRepositories` leave it out
  (so the session's tools, briefing, checkout and credentials do, and an
  investigator's `search_memory`/`get_memory` are scoped to them), as do
  `settings` and filing a card. A parked agent still holding a checkout of a
  project hidden since is not resumed with it (lux cannot remove a
  repository from a Run): it is ended and a fresh agent, without it, takes
  the next message.
- Nothing new from chat is filed into or linked to a hidden project: it is
  out of the routing world and the hints, a decision naming one is refused
  as an invented project is, and a member's link by hand is refused (409
  `hidden_from_chat`).
- A task or epic whose `origin.provider` it is, and whose own project is
  hidden later, keeps working on its own project: all its tools, Runs,
  checkout and delivery go on. It never reaches any other hidden project:
  its repositories, tasks, relations (`RelationsOf` leaves the other side
  out), peers, memory, search, settings and `run_chat` there are refused or
  left out.

SQL reads use `delivery.HiddenProjectSQL` or `SessionHidesSQL`; Go checks
use `delivery.FenceOf`. Agent tools' `fenceOf` and delivery's `taskFence`
exempt a task's own project. The provider comes from origin, so hiding takes
effect on subsequent reads and survives reinstalling the integration.

**Session tools for chat.** `chat_history` (capability `chat.read`, a
brainstorm's by default) reads more of the conversation a session came from
— its thread, or the channel for a top-level mention — 30 messages a page
before or after a message id, at most 30 reads a session an hour, recorded
as `chat.read` with counts only, quoted as external text. A page that starts
where Slack's order does (the latest, or before a message on a channel;
after one in a thread) is one call; the other way it pages Slack's cursor up
to 1 000 messages and, past that, tells the agent to use a nearer message
rather than give a page that is not next to the one named. The conversation
is the session's origin's, never one the agent names. `github_run_jobs`
(`github.read`) lists a GitHub Actions run's jobs from its id or link, for
`github_check_log` to read the failed job's log; it counts against the same
hourly GitHub reads. Those counts, and `chat_history`'s, read the
tool-agnostic indexes of counted attempts (`event_type =
'agent.tool.attempted'` by session, task or automation and time, migrations
197–199, which replace the tool-list indexes 200–202 drop); the tool list
is a filter in the query, so a new counted tool needs no migration. The
migrator records a `CREATE INDEX CONCURRENTLY` file only once its index is
valid, and drops (concurrently) one an interrupted build left invalid, so
the next migrate builds it afresh.

**Reactions follow the work.** 👀 goes on the mention as the loop's first
stage of a delivery (never in the webhook request), once per delivery:
`chat_events.working_at` is claimed before it is put. A mention kept for a
Connect loses its 👀 with the Connect prompt and gets it back when the kept
delivery is routed. Then, by what the mention made: a session (new,
shared, private, or a message into a bound or resumed one) keeps 👀 after
the reply with the link, and the `chat-reactions` loop
(`Service.SettleMarks`, 50 waiting requests a pass) follows the session's
own agent (its brainstorm Runs, not the investigators it starts):
- ⏳ in place of 👀 while that agent boots (pending, scheduled or starting;
  paused and due back for the message; or resumed and lux not yet running
  it), once per request (`chat_requests.booting_marked_at`). An agent
  already running goes straight from 👀 to ✅;
- ✅ on the agent's first `agent.message` or `agent.tool.called` after the
  mention's message (`chat_requests.awaits_session_after`, read through
  `events_session_cursor_idx`);
- ❌ when no brainstorm Run is live and none holds an unread message the
  syncer will hand to a successor;
- the mark left as it is after 24 hours of neither.

Each is recorded (`mark_settled`, `booting_marked_at`) before the provider is
called, so it is put once. A task, an epic or a bound thread's
link: nothing runs, ✅ with the reply. A clarifying question: the question
is the answer, the 👀 is cleared. Refusals: cleared, with the private note.
A mark is cosmetic: no route or outcome depends on it, and a failed one is
noted for the installation's health, never retried.

**Configuration**: `slack.signing_secret`, `slack.client_id`,
`slack.client_secret` (both secrets), `slack.api_url` (default
`https://slack.com/api`; the fake's in tests). The Slack app's Event
Subscriptions and Interactivity URLs are `<public_url>/v1/webhooks/chat/slack/events`
and `…/interactions`; its redirect URLs `<public_url>/v1/chat/slack/install/callback`
and `…/connect/callback`; its bot scopes `slack.BotScopes`. An automation's
`post_to_chat` uses the same installation and scopes: `chat:write` to post in
a channel the bot is in, `channels:read`/`groups:read` to find a channel by
name (`conversations.list`).

## Decisions already made

These were settled deliberately. Change them on purpose, not by accident.

- **Build order** follows the plan's §81 self-hosting bootstrap, not the §50
  v1 slice.
- **Two processes.** The Go orchestrator owns everything that drives work;
  the Bun backend owns display and management and is the only public
  surface. One Postgres, shared; one writer per table is a guideline, not
  enforced.
- **lux is the runtime.** dude does not run containers. It tells lux what
  to run and decides what the result means.
- **Workflow runtime** is Postgres-backed and hand-built (§21 option A) —
  *not* Temporal — and lives in the orchestrator.
- **PR observability is by webhook, never polling.** GitHub's rate limits
  rule polling out. A reconciler re-reads open PRs every 15 minutes as a
  backstop.
- **Multi-tenant from the first migration**, enforced by row-level security,
  in both processes.
- **Models are configured per project, per role, as tiers**, falling back to
  the organization; the organization's tiers say which model each requests.
  Per-role `context` is appended to that role's prompt.
- **DB access**: `Bun.sql` in the backend, `pgx` in the orchestrator, raw SQL
  in both; migrations are `.sql` files run by `bun run migrate`.
- **Phases are Runs**, not agent subagents. The reviewer executes but never
  publishes. Tests belong to the implementer; the *tester* is a browser QA
  agent, on for every project unless its policy says `test: false`.
- **GitHub**: a PAT first, the GitHub App later behind the same client.
  **The factory never auto-merges** — it stops at an open PR.
- **Artifacts** will be S3-compatible (lux stores them; not wired up yet).
- **Chat is the primary surface.** The event timeline and logs are debugging
  tools, one tab away.
- **Quality passes as you go**: a simplify/review pass after each meaningful
  piece of work, not at the end.

## Conventions

- **Comments explain why, not what.** Match the density of the surrounding
  code, which is high on purpose.
- **Commit messages explain the decision and what was found**, in prose. Look
  at `git log` for the house style.
- **Tests pin behaviour that matters**, and each new guard was checked by
  breaking it and watching the right test fail. Keep doing that.
- **Event types are a contract** between the processes and the browser:
  `packages/domain/src/events/types.ts` is the vocabulary, and the Go
  constants must match it.
- **The web app never talks to the database** and never reaches around the
  API (§117).

## Traps

Each of these cost real time.

- **The fakes must be as awkward as the real thing.** The fake lux sends
  lifecycle events *after* the agent's records, queues input to a busy
  agent, and resumes an agent idle — because real lux does, and code that
  assumed otherwise passed against a friendlier fake and failed against lux.
  When the contract suite (`run_tests.py --lux`) disagrees with the fake,
  fix the fake first.
- **A `--serve` lux environment runs the lux-fake it was built with.** After
  lux-fake changes, restart it.
- **lux's lifecycle events trail the agent's records.** The Run's recorded
  `lux_state` can say "scheduled" after the agent has finished; only a state
  lux has already reported as over rules anything out.
- **Don't run `bun test` with a dev backend up** against the same database;
  keep test and dev databases apart. The Go tests make a database of their
  own per binary, cloned from a migrated template.
- **Migration 014 is still being edited** until it ships. A database that
  applied an earlier version needs its checksum updated in
  `schema_migrations` (or recreate it).
- **`networkidle` never fires in the web app** — it holds an open SSE
  connection. Wait on elements in Playwright.
- **A pushed demo leaves state on GitHub.** A merged PR changes `main`, which
  changes what the next run sees.
- **Every time the API sends is UTC.** The browser merges times from
  different sources (a proposal card's, the ledger's), and on a host outside
  UTC pgx scans a `timestamptz` in the process's zone (`+01:00`). Both pools
  and the migration runner set the session `TimeZone` to UTC, the
  orchestrator's scans into UTC (`db.Open`, `openPool`, `migrate.ts`), and a
  time decoded from lux's JSON is passed on in UTC (`servers.previewProgress`,
  `servers.moved`, and a server's `since`, `readySince` and `lastRequestAt`
  in `lux.Server`'s JSON); the web app still compares instants, never the
  text (`apps/web/src/instant.ts`). CI and production are UTC, so only a
  test that sets `time.Local` or the database's zone sees a slip.

## Testing strategy

| Layer | Where | What it proves |
| --- | --- | --- |
| Go unit | `orchestrator/internal/*` | Workflow runtime, policy, classifier, parsers — against a real Postgres |
| Go integration | `orchestrator/delivery_test.go` | Whole deliveries through the orchestrator's real code, against a fake lux and a fake GitHub backed by real git |
| TS unit | `apps/control-plane/test` | The ledger, the live stream, webhook signatures; `cli-routes.test.ts`: every authenticated `/v1/` route has a dude CLI command or an `apiOnly` reason in `orchestrator/cmd/dude-cli/routes.json`, and `cli-bodies.test.ts`: the CLI's recorded automation request bodies parse with their routes' schemas (neither needs a database) |
| E2E | `tests/suites` | The deployed processes through the public API and the built UI, with the fake lux and a fake GitHub that delivers signed webhooks |
| Contract | `tests/suites/test_lux_contract.py` (`--lux`) | The same flows against a real lux |
| Browser | `tests/suites/test_web_ui.py`, `test_gallery_ui.py` | The web app and the design system, in system Chrome (Playwright's bundled Chromium with `DUDE_TEST_BROWSER_CHANNEL=` empty, where there is no Chrome) |
| Browser, Access mode | `tests/suites/test_accounts_access.py` (conftest `access`) | The sign-in gate, invitations and organisations as Cloudflare Access fronts them: a second backend with `auth.provider = cloudflare_access` verifies tokens a local stand-in team (`tests/access-edge.ts`) signs for any email; no API key signs anyone in |
| Browser, no backend | `apps/web/test/browser_*.py` (`python3 apps/web/test/browser_terminal.py`) | Layout and sign-in the web app decides alone, against the Vite dev server and the fixture client or routed API answers, in system Chrome |

**An element's absence is asserted as a count**:
`expect(root.querySelectorAll(sel).length).toBe(0)`, never
`expect(<a DOM query>).toBeNull()`. Bun's `toBeNull` has passed a present
mounted React/happy-dom element in a large object graph (a stale "thinking"
span before b5b4a85b's fix). The reproduction supports failure-message
formatting as a hypothesis, not an established root cause; not all happy-dom
elements are affected. A zero count reliably rejects a present element
matching the same selector. A finder that returns an element or null
gets a counting sibling for its absence checks.

The scripted agent (`orchestrator/internal/fakeagent`, model `fake/scripted`,
requested by a tier like any other model; the suites' `ApiClient.on_models`
puts roles on such tiers)
plays every phase deterministically: the implementer commits, the reviewer
raises one blocking finding unless the fixer's file is in its tree, the fixer
and simplifier commit, and the tester passes every criterion with a published
video (`fakeagent.Tester`; `fake/test-fail`, `fake/test-fail-once`,
`fake/test-fail-quiet`, `fake/test-blocked`, `fake/test-unverified` and
`fake/test-silent` play the gate's other outcomes). `fake/hang` never finishes
its turn, for steering and pausing. The fake lux plays it directly; a real lux
runs it as a lux-fake script.

### What CI runs

`.github/workflows/ci.yml` runs on every pull request and every push to
`main`; a newer push to the same pull request or branch cancels the older run.
The `go`, `ts` and `e2e` jobs each have their own
`pgvector/pgvector:pg17-trixie` on `localhost:5433`.

| Job | Runs |
| --- | --- |
| `go` | In `orchestrator`: `gofmt -l .` must list nothing, `go vet ./...`, `go test -p 4 -timeout 25m ./...` (the root package alone took about 9.5 minutes on CI, close to go test's default 10-minute limit per binary), then `go test -race -count=1 ./internal/forge ./internal/phases` (forge's concurrency tests assert only under the race detector; phases runs its followers on goroutines of their own). The step fails if any test skipped with dbtest's `no test database`: in CI an unreachable Postgres is broken setup, not a pass. |
| `ts` | `bun run typecheck`, `bun run migrate` then `bun test` with `DATABASE_URL` at that Postgres, and `tests/suites/test_shard.py` (the shard split, no environment). |
| `e2e (n/6)` | `tests/run_tests.py --shard n/6`, with Google Chrome for the browser suites and Docker for the test S3. The log directory is uploaded when a shard fails. |
| `image-builder` | `scripts/test-image-builder.sh`, after delegating the `cpu` and `memory` cgroup controllers to the runner's user manager, in a scope of that user's systemd. |
| `cli` (`cli.yml`, only when the CLI, `apps/control-plane/src/api`, `packages/domain/src` or the `cli-*` tests change) | No database: `go vet` (linux and windows) and `go test` of `./cmd/dude-cli/...`, then `cli-routes.test.ts` and `cli-bodies.test.ts`; and `go vet` + `go test` on macOS (`cli-macos`). Windows is built and vetted, not run. 5-minute limit. |

Expected skips in `go`: the podman tests in `internal/images` (they need
`DUDE_PODMAN_TEST_REGISTRY`, set by `scripts/test-image-builder.sh`) and
`TestRealEmbedderFindsByMeaning` (needs `DUDE_EMBEDDINGS_URL`/`_KEY`). In
`e2e`: `test_publishing_preflight_contacts_test_gateway` (needs
`DUDE_TEST_GITHUB_GIT_HOST`).

**Shards** are whole suite files, split by `tests/shard.py` into six shares of
the durations in `tests/.suite-seconds.json`; a suite missing from it weighs
30 s. After adding a suite or changing one's length a lot, refresh the file
from JUnit reports that together cover every suite, one shard at a time:

```bash
cd tests
for n in 1 2 3 4 5 6; do uv run python run_tests.py --shard $n/6 --junitxml=/tmp/dude-e2e-$n.xml; done
uv run python shard.py /tmp/dude-e2e-*.xml
```

Shard by job, never with `-n`: each xdist worker would start its own Vite on
the same strict `DUDE_TEST_WEB_PORT`.

**Manual, not in CI:**

- The contract suites against a real lux (`run_tests.py --lux`).
- `apps/web/test/browser_*.py`, against the Vite dev server.
- Anything needing a real model (`DUDE_LLM_KEY`) or real embeddings.

A hosted runner is not a build host: when a change touches the image builder,
also run `scripts/test-image-builder.sh` by hand on one (rootless podman, cgroup
v2, `cpu` and `memory` delegated to the user).

### Manual browser sign-in

Manual key submission verifies the candidate through `/v1/me` before storing
it. A successful submission uses `location.replace` to open a new document at
the current same-origin path and hash, dropping query parameters. It does not
mount the authenticated app in the password form's document. Invalid keys are
not stored; failed checks leave the form available for retry. Key-mode sign-out
clears this browser's credential without revoking the API key.

The document boundary replaces extension-injected DOM as well as the form.
Browser regressions prove one navigation after successful verification and no
navigation or persistence on refusal. They do **not** prove the reported
Bitwarden residue is fixed: its cause has not been reproduced, and real
extension verification remains manual. No extension DOM manipulation, synthetic
Escape, CSS suppression, or shared input changes are involved.

Cloudflare Access is not implemented yet. Enabling keyless sign-in requires the
coordinated person-principal, ownership, push and attribution migration in both
processes first; a person ID must never be substituted for an API-key ID.
Eventual aiverse rendering must supply the `[auth]` settings in dude's
configuration file (or their `DUDE_AUTH_*` variables), including the fixed
trusted `public_url` and Access team/audience, without changing the default
API-key mode. No aiverse wiring is included here.

**Configuration** is one TOML file for both processes plus the environment
(docs/operations.md, "Configuration"). The key table lives once per language
— `orchestrator/internal/config` and `apps/control-plane/src/config.ts` —
and both suites compare it to `tests/fixtures/config/keys.json` and resolve
`full.toml` to `full.json`: a new setting is a row in all three and a line
in `full.toml`, `full.json` and `docs/dude.example.toml`. Every file in
`tests/fixtures/config/invalid/` must be refused by both suites with the
error its first comment line names. Read sites take
values from the resolved config, never `os.Getenv`/`process.env`.

**Configuration stays compatible across one release**, because a
deployment upgrades by running the new release on the old release's
configuration (and refuses to switch when its `validate` fails):

- **A new setting always has a default or is optional**, so a new release
  starts on the previous release's configuration. A setting that must be
  required waits a release: optional first, required once deployments have it.
- **A removed setting is accepted for one more release with a warning.**
  Move it from the key table to the `retired` list — `retired` in
  `orchestrator/internal/config`, `RETIRED` in `apps/control-plane/src/config.ts`,
  and `"retired"` in `keys.json` (file key and variable). Both loaders
  accept it from the file or the environment, ignore its value and warn
  `retired: <key>; remove it`. Delete the entry in the release after.

## What's next

In rough priority order.

1. **Real models end to end.** Everything has been proven with the scripted
   agent; run real tasks with OpenCode through lux, read the
   transcripts, and tune prompts, the findings parser and policy.
2. **Telling a person something waits on them**: a notification (Slack,
   email) for a question or a request. Today they are only on the board.
3. **Budgets as loop bounds** — a cost cap per Run and per task.
4. **The tester phase** (browser QA with recorded evidence) and artifacts
   from lux. Design in [`phased-runs.md`](phased-runs.md).
5. **Merging as the GitHub App.** An organization can connect an App
   ([github.md](github.md#github-app)); a merge it asks for goes through the
   same client as everything else, and has not been tried against GitHub.
6. **lux push with an expected base commit**, which would remove the
   per-Run branch and the GitHub fast-forward.
7. **Forge credentials are stored in plaintext.** Encrypt at rest before any
   non-local use.
8. **Chat bot tokens are too** (`chat_installations.bot_token`): treated as
   forge credentials are, and the same encryption should cover both.

## Where things are

| You want to… | Look at |
| --- | --- |
| Change the delivery sequence or its bounds | `orchestrator/internal/delivery/workflow.go`, `policy.go` |
| Change what an agent is told | `orchestrator/internal/delivery/agents/` (its README), composed by `prompts.go` |
| Change what lux is asked to run | `orchestrator/internal/phases/spec.go` |
| Change what a branch preview runs, or when it parks | `orchestrator/internal/servers/previews.go` |
| Change what an agent's output becomes in the ledger | `orchestrator/internal/phases/translate.go` |
| Change how a finished phase is collected | `finish` and `publish` in `orchestrator/internal/phases/sync.go` |
| Change what wakes an agent on a PR | `orchestrator/internal/forge/classify.go` |
| Change how findings are parsed | `orchestrator/internal/delivery/findings.go` |
| Change the scripted agent | `orchestrator/internal/fakeagent` |
| Add an event type | `packages/domain/src/events/types.ts` and the Go constants that write it |
| Change the sidebar/board data | `apps/control-plane/src/api/routes/navigation.ts` |
| Change who may read or chat in a session | `session_role()` (`migrations/190_org_sessions.sql`), `orchestrator/internal/delivery/session_access.go` |
| Change what a session mention does | `orchestrator/internal/delivery/session_mentions.go`, `notify.go`'s `asks` |

| Change what an automation matches, when it fires, or how it runs | `orchestrator/internal/automations/` (`match.go`, `cron.go`, `concurrency.go`, `runner.go`); its spec, `orchestrator/internal/phases/automation.go` |
| Change automations' API or who may change them | `apps/control-plane/src/api/routes/automations.ts`, rules in `packages/domain/src/automations.ts` |
| Add or change an API route | also `orchestrator/cmd/dude-cli/routes.json`: the CLI command, or `apiOnly` and why (docs/cli.md, "Adding a command") |
| Change how a chat mention is routed | `orchestrator/internal/chat/route.go`, `decide.go`, `make.go` |
| Add or change a chat provider | `orchestrator/internal/chat/chat.go` (the interface), `internal/chat/slack`, `internal/fakeslack` |
| Change the chat settings pages | `apps/web/src/screens/ChatSettings.tsx`, `apps/control-plane/src/api/routes/chat.ts` |
| Change a screen | `apps/web/src/screens/` |
| Change a component | `packages/design-system/src/components/` |
