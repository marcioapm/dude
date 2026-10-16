# Tracking work: lifecycle, classification, finding things

*Proposal. Clickable mockup: [`mockups/tracking/index.html`](mockups/tracking/index.html)
(`?screen=…&theme=dark|light&density=comfortable|compact&role=owner|member`), built from
`apps/web/mockups/tracking/`. Nothing here is built.*

Throughout, **Decided** marks what Márcio fixed in the brief; **Proposed** is this document's
choice, with the alternative it rejected. Where the brief's default turned out to need a change
to be buildable, the change is called out as **Correction**.

## The problem

dude manages work that agents do, and it records very well what *delivery* did to a task: it
ran, it opened a pull request, it merged, it failed, a person stopped it. It records nothing of
what a *person decided* about a task. Concretely, on `origin/main`:

- **A task that never started has no way out.** `task_status` ends in `done`, `failed` or
  `aborted`, and each of those is written by delivery: a merge, `markDone`
  (`orchestrator/internal/api/api.go:1153`, only from `review`), a closed pull request
  (`delivery/workflow.go:1495`), a stopped run (`workflow.go:1617`). There is no
  `DELETE /v1/tasks/:id`, and `PATCH /v1/tasks/:id` (`routes/structure.ts` `updateTask`) edits
  text, epic, repositories, owner and source only. A duplicate, an idea nobody wants any more,
  a bug that no longer reproduces: each sits in Backlog for ever.
- **Nothing can wait on purpose.** "Not now, after the invoice work" can only be said by leaving
  a task in Backlog, where it counts, sorts, and can be started by anyone, including an agent.
- **Nothing can be put away.** Finished work stays on the board, the sidebar and every count.
  Epics can be deleted (`deleteEpic`), not archived. Memories, brainstorm sessions and images
  can be archived; tasks and epics can't.
- **Every task is the same kind of work.** A bug, a feature, an upgrade and a question get the
  same brief and the same review. A bug's fix is never required to come with the test that
  shows it was a bug.
- **Nothing says what matters most.** Epics have an order (`epics.position`, `016`); tasks don't.
- **Finding a task means scrolling.** `GET /v1/tasks` (`routes/work.ts:320` `listTasks`) filters
  by project and one status, `ORDER BY created_at DESC LIMIT 200`, no paging, no text. The
  sidebar's "Find work" matches titles in the tree that is already loaded. `list_tasks`
  (`agenttools/tools.go:165`) takes `text` and `startable`.

Jira solves these for people with statuses, resolutions, issue types, priorities, labels,
components, custom fields and JQL. dude needs a much smaller set, because most of what those
fields carry, delivery already knows. This proposal adds three things that share one vocabulary:

1. **Lifecycle**: put work off (**Later**), end it with a reason (**Close**), put ended work
   away (**Archive**).
2. **Classification**: a **kind** (bug, feature, chore, spike) that changes what the agents are
   told and what review checks; a **priority**; a **bug report** with what caused it.
3. **Finding things**: a list beside the board with fixed views, filters on the fields above,
   `GET /v1/tasks` that can answer them, and Find work that reads `kind:bug p0`.

One document, because the third epic filters on exactly the fields the first two add, and
because every field must reach the same dozen surfaces (see *Field × surface*).

## Words

*Proposed.* Each word is one a person already uses for the thing, and none is taken by
something else in dude.

| Word | Means | Rejected, and why |
|---|---|---|
| **Later** | the shelf a task or an epic is on until a date, or until someone brings it back | "Deferred" (the API's word, not a person's), "Snooze" (an inbox's: it implies it pings you), "Backlog" (already the first lane), "Someday" (says no date is coming even when one is) |
| **Put on Later** / **Bring it back** | the two moves | "Defer" / "Undefer" (no person says undefer), "Restore" (that is Unarchive's) |
| **Close** | a person ends a task, saying why | "Resolve" (implies it was fixed), "Cancel" (wrong for a duplicate), "Abort" (already delivery's word for a stopped run) |
| **Reopen** | undo Close | — |
| **Won't do** / **Duplicate** / **Can't reproduce** / **Nothing to merge** | the four resolutions | "Obsolete" (folded into Won't do: the distinction isn't acted on), "Done" as a resolution (that is a status) |
| **Archive** / **Unarchive** | take ended work out of every everyday view, and back | "Hide" (sounds per-person), "Delete" (it isn't) |
| **Bug**, **Feature**, **Chore**, **Spike** | the four kinds | "Task" as a kind (every one is a task), "Story" (product jargon), "Investigation" (spike is shorter and known) |
| **P0**–**P3** | priority | words ("Urgent", "High") as the stored value: the numbers sort, compare and fit a card; the words are their tooltips |
| **Introduced by** | the task that caused a bug | "Regressed by" (only right if it once worked), "Caused by" (too sure for a proposal) |

## Epic 1 — Lifecycle

### Model

Three independent facts on a task, none of them a status:

```
status            what delivery did        received … done | failed | aborted     (unchanged)
resolution        why a person ended it    null | wont_do | duplicate | cant_reproduce | nothing_to_merge
later             it may not start yet     deferred_at (null = not on Later), deferred_until (date | null)
archived          out of everyday views    archived_at, archived_by
```

They compose: a task can be on Later and P1; closed as Won't do and archived; a bug, done, with
nothing to merge. **Status stays the only thing delivery reads and writes.** Lifecycle writes
its own columns, and one status transition each for Close and Reopen.

#### Later

*Decided:* a shelf, not a status; a date or none; comes back on the date by a worker; every
start path refuses; "Bring it back" is the escape; epics too.

- **Correction.** `deferred_until` alone can't say "on Later with no date": `NULL` would mean
  both "not on Later" and "until someone brings it back". The shelf is `deferred_at` (set = on
  Later) with `deferred_until` (a date, or `NULL` for no date) and `deferred_by`. Bring it back
  clears all three.
- **Only a task not started goes on Later** (`received`, `intake`, `awaiting_confirmation`,
  `queued`: `delivery.NotStarted`). *Proposed:* a running task is refused ("Stop its agents
  first: work in progress can't go on the shelf"), as Archive is. Putting a task in review on
  the shelf would leave its pull request open and unwatched. An ended task has nothing to put off.
- **The date is a day, and the day starts at 00:00 UTC.** *Proposed:* stored as `date`, not
  `timestamptz`: "Later until 3 Nov" is a day, and a time zone per person would make it come
  back at a different moment for each reader. The dialog says "(UTC)" once.
- **Coming back starts nothing.** The task is back in Backlog, not started, and waits to be
  started as before. Its owner's existing "start when unblocked" request (`task_auto_starts`) is
  untouched while it is on Later and is re-checked when it comes back: if its blockers merged
  while it was on the shelf, the comeback marks it due, and the auto-start worker fires it through
  the guard (path 11). That is the only way a comeback leads to a start, and only because the
  owner asked for it.
- **The worker** is `delivery.LaterReturns.Sweep`, a sibling of `AutoStarts.Sweep`
  (`delivery/unblock.go:227`): once a minute it takes tasks and epics whose `deferred_until <=
  current_date` (UTC) `FOR UPDATE SKIP LOCKED`, one transaction each, clears the shelf, appends
  `task.came_back` / `epic.came_back`, and wakes any auto-start request. Idempotent: a row
  already brought back is no longer selected.
- **An epic on Later** hides its group from the board and the tree (it shows as one row in the
  Later shelf, "Usage alerts · 2 tasks · 1 Dec") and **its tasks inherit the refusal** without
  their own columns being written: the guard reads `COALESCE(task.deferred_at, epic.deferred_at)`.
  *Proposed:* an epic can go on Later only when none of its tasks is running ("CP-35 is running:
  stop it, or move it out of the epic, first"), for the same reason a task can't. Bringing the
  epic back brings back the epic, not the tasks with shelves of their own.
- **Who:** the task's owner or an org admin. **An agent** may put **its own task** on Later (a
  conductor that learns it must wait on something outside dude), with a reason that is quoted in
  Activity; it may not put another task on Later, nor bring any task back. *Decided default,
  kept:* an agent can't defer others; bringing work back is a person's decision to spend on it.
  Brainstorm proposals and `create_task` may file a task straight onto Later.

#### Close, and resolutions

*Decided:* resolutions, not new statuses; a person closes a never-started or stopped task; the
mapping is the brief's.

| Resolution | Status it ends in | Who sets it | Its blockers |
|---|---|---|---|
| **Won't do** | `aborted` | a person closing | keeps blocking, as an unmerged task does today |
| **Duplicate** (of a task) | `aborted` | a person closing; writes the `duplicates` edge | hands its blocking to the task it duplicates |
| **Can't reproduce** | `aborted` | a person closing, **bugs only** | keeps blocking |
| **Nothing to merge** | `done` | Mark done (`markDone`), unchanged route | keeps blocking ("done, nothing merged", as today) |

- **Close is offered** for a task not started, and for one that stopped (`failed`, `aborted`
  with no resolution). *Proposed:* not for a running one ("Stop its agents first, then close
  it"). Stopping is already a decision with its own dialog; folding it into Close would make one
  click end live work. Not for `review` either: a task in review has a pull request, and closing
  that is GitHub's or Mark done's.
- **Nothing to merge is not offered in Close** ("Only for work that ran: Mark done gives this,
  once there is a write-up or an answer"). `markDone` writes it: the route stays, and from now on
  stores `resolution = 'nothing_to_merge'` on the task, so today's "done, nothing merged" gets
  its name and the board, list and API say it the same way. Existing done-without-a-merge tasks
  get it in the migration.
- **Can't reproduce is a bug's** (refused on other kinds, with that reason): there is nothing to
  reproduce in a feature.
- **Reopen** clears the resolution and puts the task back to `received`, keeping its runs. A
  duplicate's edge is removed on reopen, and what was handed over goes back.
- **Why a Won't do still blocks.** The relations doc's rule is "only merged work releases what
  waits on it". A task nobody will do hasn't produced what the waiting one needed; releasing
  its dependants would start work on a missing foundation. The close dialog says so when
  something waits ("CP-47 waits on CP-46 and will keep waiting… Remove the relation if it no
  longer matters").

#### Duplicate: the `duplicates` relation, built

`task-relations.md` pencilled `duplicates` as "later". It is built here, as a third
`task_relation_kind`: `duplicates`, from the closed task to the one it duplicates, written only
by Close (never by Add relation) and removed only by Reopen. It shows as **Duplicate of** on the
closed task and **Duplicated by** on the other, its own group in `RelationList`, with the `copy`
glyph.

**A duplicate blocker hands its blocking to the task it duplicates.** *Proposed,* with the
tradeoff:

- `task_blocked_by` follows a `duplicates` edge: a `blocks` edge from a closed duplicate counts
  as a `blocks` edge from its canonical task (one hop; the canonical task can't itself be a
  duplicate, the picker refuses that: "Closed as a duplicate of CP-62: pick that one").
- So CP-47, blocked by CP-63, is blocked by CP-62 the moment CP-63 is closed as its duplicate,
  and starts when **CP-62** merges. Its Relations still list CP-63 (the edge is not rewritten),
  with "CP-63's blocking came here with it" under CP-62's group.
- *Rejected:* rewriting the edge to CP-62. Simpler to query, but it loses who added the edge and
  why, a Reopen can't put it back, and the history would claim someone related CP-47 to CP-62.
- *Rejected:* treating a duplicate like Won't do (still blocks). That's the strict reading of
  "only merged work releases", but it leaves CP-47 waiting on a task nobody will ever work on,
  which is exactly what Close exists to fix.
- **The cost:** the gate's SQL gains one join, and the cycle check
  (`task_relation_would_cycle`) must walk `duplicates` edges as `blocks` edges, so that "CP-62
  blocks CP-63's dependant, which blocks CP-62" is refused as a loop. Closing as a duplicate is
  refused when it would close a loop, naming it, as Add relation does.

#### Archive

*Decided:* `archived_at`/`archived_by` like memories; out of the board, tree, counts,
`list_tasks` and Find work unless asked; reachable by link and from Archived; unarchive restores;
an epic with unfinished tasks refused, offering to archive its finished ones; refused with
running agents; owner or admin; Activity both ways.

- **Only an ended task is archived** (`done`, `failed`, `aborted`). *Proposed:* this is stricter
  than "refused while agents are running": a task in Backlog is archived by closing it first. An
  archived task that could still start would be work nobody sees. Refusal reason on the menu
  item: "Close it first: only ended tasks are archived".
- **Archiving an epic archives nothing by itself.** With unfinished tasks it is refused
  (screen *archive-epic-refused*), listing them; the dialog's one action is "Archive its N ended
  tasks", which archives those tasks (one `task.archived` each, and one Activity line on the
  epic) and leaves the epic. With every task ended, Archive archives the epic and its tasks
  together, in one transaction.
- **Archived tasks keep everything**, including relations: an archived blocker still blocks (the
  guard doesn't read the blocker's `archived_at`). An archived task can't be started or picked
  back up until it is unarchived (*Start paths*, path 7).
- The hint on the board ("3 archived", quiet, beside New task) opens the list's Archived view.

### UX

| Screen | What it shows |
|---|---|
| `board-later` | the board with the **Later** shelf under its header, folded to one line by default (keys and dates), open here; each shelf card's Start is disabled with the reason and **Bring it back** beside it; the epic on Later as one row; "3 archived" in the header |
| `defer-dialog` | Put CP-62 on Later: `ChoiceList` On a date / No date, a date `Input` with quick picks, a Why, and what waits on it |
| `deferred-task` | CP-62's page: header's Talk it through and Deliver disabled, reachable, with the reason; **Bring it back**; a neutral `Callout` saying when it returns and that returning starts nothing |
| `close-dialog` | Close CP-63: four resolutions as a `ChoiceList`, two disabled with why; Duplicate's `SearchPicker` and what the handover does |
| `closed-wontdo` | CP-46 closed as Won't do: the resolution beside the status, a `Callout` with Reopen and Archive |
| `closed-duplicate` | CP-63 and CP-62 side by side: Duplicate of / Duplicated by, and CP-47's blocking handed to CP-62 |
| `archive-task` | the Archive confirmation for CP-46 |
| `archive-epic-refused` | Webhook reliability refused, its 4 unended tasks listed, "Archive its 2 ended tasks" |
| `archived-view` | the tasks list's Archived view, read by a member: every action in the row menu disabled with whose it is |
| `activity-lifecycle` | every new Activity line, both ways (see *Events*) |

- **Lifecycle actions live in the task's `…` menu** (the existing `RowMenu` in the header, the
  tree row, the list row): Later… / Bring it back, Close…, Archive… / Unarchive. Each is shown
  to everyone, **disabled with its reason** for a reader who may not ("Only Ana, the owner, or
  an admin can do this") or when the task's state refuses it, per the README's rule that an
  option is never greyed out without its reason (`RowMenu disabledReason`, `ChoiceList
  disabledReason`) and *A disabled control the reader can still reach* (Task relations). On the
  page, the one action that matters now is a button too: Bring it back on a task on Later,
  Reopen on a closed one, Unarchive on an archived one.
- **The shelf sits on the board, not as a lane.** A lane would say Later is a stage of the work,
  which it isn't, and the README fixes five lanes "always drawn, in that order". Its count is
  outside the board's counts and filters' counts. Folded, it is one 40px line.
- **The tree** shows a **Later** row after the project's epics (the `layers` slot, a clock),
  folded by default, with the same tasks. *Proposed:* never opened by triage (nothing on it
  needs anyone); the needs-you count never includes it.
- **Closed cards** in Done show their resolution in the card's marks line, where today's says
  "stopped".

### Agents and CLI

- `list_tasks` leaves archived tasks out unless `archived: true`, and marks tasks on Later
  (`later: {until}`), so an agent never proposes work that is on the shelf as "next".
- `create_task` and `propose` take `later: {until?: date}` (put it on the shelf as it is
  filed); `update_task` takes `later` and `laterReason` **for the caller's own task only**.
  Bring it back, Close and Archive are not agent tools. A conductor whose start is refused as on
  Later gets the refusal's words and its prompt says, as for blocked: tell the person and stop.
- `cmd/dude`: `dude task list --later | --archived`, `dude task later CP-62 [--until 2026-11-03]
  [--why …]` (own task only), and `dude task create --later`.

### Start paths: one rule

*Proposed:* `RefuseBlockedTx` becomes **`delivery.RefuseStartTx(ctx, tx, taskID) error`**, the
one guard every start path already calls (through `GuardStartTx` or directly). In order, after
locking the task row:

1. **Archived** → `ArchivedError` ("CP-29 is archived: unarchive it to start it."). Not
   overridable. Reached by picking an archived failed task back up (path 7); on the other paths
   an archived task has ended, and the check costs nothing.
2. **On Later** (its own or its epic's) → `LaterError` ("CP-62 is on Later until 3 Nov: bring it
   back to start it." / "…with no date…" / "CP-67 is in Usage alerts, which is on Later until
   1 Dec: bring the epic back to start it."). **Not overridable**, unlike blocked: *Start
   anyway* is for a blocker someone else controls; the shelf is the owner's own decision, and
   its escape is one click, Bring it back.
3. **Blocked** → `BlockedError`, exactly as today, overridable by a person.

`RefuseBlockedTx` stays as a wrapper while callers move; the API maps `ArchivedError` to 409
`archived`, `LaterError` to 409 `later`, both recorded as `task.start_refused {path, reason}`.
The backend's own gate (path 8) calls a SQL function `task_start_refusal(task)` (returns
`archived` | `later` | `blocked` | null) beside `task_blocked_by`, so both processes keep one
rule.

The table is the relations doc's, with the current lines (they have moved since) and two paths
it didn't list:

| # | path | where it starts work | lifecycle guard |
|---|---|---|---|
| 1 | **Deliver**, Create and deliver | `deliver` `orchestrator/internal/api/api.go:455` → `delivery.StartTx` `api.go:484` → `GuardStartTx` `delivery/start.go:109` | `RefuseStartTx` in `GuardStartTx`: Later and Archived refused even with `override`. "Create and deliver" makes the task and says it was not started (`footerProblem`), as for blocked |
| 2 | **Talk it through** | `talk` `api/chat.go:67` → `talkThrough` `chat.go:300` → `StartTx` `chat.go:301` | same, through `StartTx` |
| 3 | **A first message in an unstarted task's Chat** | `chat` `chat.go:30` → `decideFor` → `talkThrough` `chat.go:277` | same; the composer keeps the words |
| 4 | **The conductor's first `start_phase implement` / `decide` at `PointStart`** | `ConductStartPhase` `delivery/conduct.go:193` → `refuseBlockedStart` `conduct.go:211`; `ConductDecide` `conduct.go:371` → `conduct.go:386` | `refuseBlockedStart` calls `RefuseStartTx`; a Later or Archived refusal is a `Refusal` whatever `st.Overridden` says. Covers a task put on Later while its conductor plans |
| 5 | **The workflow's `implement` step** | `implementRun` `delivery/workflow.go:470` → `parkIfBlocked` `workflow.go:478` (`delivery/relations.go:144`) | parks as for blocked, status back to `queued` with reason "on Later until 3 Nov", on a new signal `later.returned` that the Later worker sends; only before the first implementer of the attempt, as today |
| 6 | **Start over** | `recoverTask` `api/recover.go:206` → `GuardStartTx` `recover.go:252` → `startOver` `recover.go:353` | `RefuseStartTx`; Start over *anyway* does not pass Later |
| 7 | **Resume / Try again** | `recoverTask` → `pickUp` `api/recover.go:286` (`recover.go:322`) | **newly guarded for Archived only** (`RefuseStartTx` with blocked skipped). Reachable: an archived failed or stopped task picked back up would run work nobody sees, so it is refused ("CP-34 is archived: unarchive it to pick it back up"). Later can't apply (only a task not started goes on Later). Blocked stays unguarded here, as the relations doc decided |
| 8 | **`POST /v1/tasks/:id/runs`** | `createRun` `apps/control-plane/src/api/routes/work.ts:365`, blockers at `work.ts:382` | `task_start_refusal(task)` before `task_blocked_by`, same 409s |
| 9 | **Filing a proposal** | `fileProposal` `api/sessions.go:778` → `CreateTaskTx` `sessions.go:968` (`delivery/filing.go:57`) | starts nothing; may file onto Later |
| 10 | **`create_task`** | `createTask` `agenttools/tools.go:283` | starts nothing; may file onto Later |
| 11 | **Start when unblocked** | `AutoStarts.Sweep` `delivery/unblock.go:227` → `RefuseBlockedTx` `unblock.go:387` → `StartTx` `unblock.go:425` | `RefuseStartTx` at fire time: a task on Later is **not fired and the request stays set, not due**; the Later worker marks it due again when the task comes back, if nothing blocks it |
| 12 | **A first feedback batch on a never-delivered task** *(not in the relations doc)* | `startForFeedbackTx` `delivery/feedback.go:528` → `StartTx` `feedback.go:533` | through `StartTx`. A Later refusal is handled like blocked: the batch stands and waits ("CP-62 is on Later until 3 Nov. Its first implementer is given them once it starts.") |

Confirming a plan (`awaiting_confirmation`) still has no route; any future one calls the guard.

### Data, API, events

Routes (control plane, forwarded to the orchestrator like `done`, so the guard and the status
write stay in one process):

```
POST /v1/tasks/:id/later      {until?: "2026-11-03", why?}   owner | admin | the task's own agent
POST /v1/tasks/:id/back       {}                              owner | admin
POST /v1/tasks/:id/close      {resolution: wont_do|duplicate|cant_reproduce, duplicateOf?, note?}   owner | admin
POST /v1/tasks/:id/reopen     {}                              owner | admin
POST /v1/tasks/:id/archive    {}      POST /v1/tasks/:id/unarchive  {}            owner | admin
POST /v1/epics/:id/later      {until?, why?}   POST /v1/epics/:id/back
POST /v1/epics/:id/archive    {withEndedTasks?: true}   → 409 {code: "unfinished", tasks:[…]} without it
POST /v1/epics/:id/unarchive
```

The shape is brainstorm sessions' (`POST /v1/brainstorms/:id/archive|unarchive`,
`routes/sessions.ts:136`) and memories' (`/archive`), not a `PATCH {archived}`: each is a
decision with its own refusal and its own ledger event. Refusals are 409 with a `code`
(`running`, `not_ended`, `not_started`, `kind`, `loop`, `unfinished`) and a sentence.

Events (ledger; `packages/domain/src/events/types.ts` and the Go constants):

| Event | Payload | Activity line |
|---|---|---|
| `task.deferred` | `{until: date\|null, why?}` | **Ana** put **CP-62** on Later until 3 Nov · *“why”* |
| `task.came_back` | `{by: "date"\|"person"\|"epic"}` | **CP-62** came back from Later, as **Ana** set it: it is in Backlog, not started / **Ana** brought **CP-47** back from Later |
| `task.closed` | `{resolution, duplicateOf?, note?, handedOver?: [keys]}` | **Ana** closed **CP-63** as a duplicate of **CP-62** — **CP-47**, which waited on CP-63, now waits on CP-62 |
| `task.reopened` | `{was: resolution}` | **Ana** reopened **CP-59** (was Can't reproduce) |
| `task.archived` / `task.unarchived` | `{viaEpic?: id}` | **Márcio** archived **CP-34** / **Ana** unarchived **CP-29** |
| `epic.deferred` / `epic.came_back` / `epic.archived` / `epic.unarchived` | as the task's, plus `{tasks: n}` | **Ana** put the epic **Usage alerts** on Later until 1 Dec — its 2 tasks can't start until then |
| `task.start_refused` | gains `reason: "later"\|"archived"` | never a line (as today); "A start of CP-64 by Ana's conductor was refused: it is on Later" in the mockup is the Chat notice the conductor's refusal already produces |

`task.status_changed` still records the status move Close and Reopen make, with reason
"closed as won't do by Ana".

## Epic 2 — Classification

### Kind

*Decided:* `bug`, `feature`, `chore`, `spike`, default `feature`; a small mark on cards, rows,
the header and the proposal card; a workflow difference, specified per kind.

*Correction to management.md:* its pencilled `kind: "change" | "document"` (§ *Task kinds*) is
superseded. A document is what a spike produces; "code change" is every other kind.

| Kind | Brief (implementer) | Review gate | Proposal card | Ends |
|---|---|---|---|---|
| **Bug** | `kindInstructions["bug"]` after the role's lead (`builtinInstructions["implementer"]`, `delivery/prompts.go:539`): reproduce with the steps, commit a failing test named for the bug with its key in a comment ("Failing test for CP-62"), then fix in a second commit; can't reproduce → stop and say so. `PromptInput.task()` (`prompts.go:164`) appends the bug report after the goal. The investigator and the conductor get the bug report too (the conductor's briefing quotes the task: `ConductorPrompt`, `prompts.go:644`) | `reviewFocus["tests"]` (`prompts.go:198`) gains `bugGate`: find the commit adding the reproducing test before the fix; run it there (must fail) and at the head (must pass); missing, passing before, or unrelated to the steps → **blocking** finding "No failing test for CP-62", category `tests`. Only when `tests` is among the project's required reviewers (`requiredReviewers`); when it isn't, the gate is added to `correctness` instead, so a bug is never delivered without it | "Bug" tag, P-mark, the steps as its detail | as any change: merged |
| **Feature** | unchanged | unchanged | "Feature" tag (no glyph on cards and rows: the default says nothing) | merged |
| **Chore** | unchanged | unchanged | "Chore" tag | merged |
| **Spike** | `kindInstructions["spike"]`: answer the question in a write-up published as an artifact (`answer.md`, the existing artifact publishing, `phases/artifacts.go`); commit no code to the task branch | review runs as configured; with no diff, there is nothing to review and no pull request opens: `openPullRequest` opens none and the task waits in review, "ready to read" (`awaitRead`, `delivery/workflow.go:1081`) | "Spike" tag | its owner's **Mark done** (`markDone`, `api.go:1153`), now writing `resolution = nothing_to_merge` |

*Proposed:* a spike runs the same workflow, not a second one. The path a spike needs ("nothing
changed → ready to read → Mark done") exists today and is how such work ends now; the kind only
tells the agent to aim for it. *Rejected:* a separate document workflow
(management.md's plan): more code for an outcome the current workflow already reaches.

- **The kind is fixed once delivery starts** ("Delivery has started: its kind is fixed"), as a
  task's source is: the brief already went out. Before, anyone who may edit the task changes it.
- **Glyphs**, from the existing set (no new icons): bug `alert`, spike `question`, chore
  `wrench`, feature `sparkle` (drawn only in the header, the dialog and the meaning page).
  Neutral ink; a bug's in strong ink. *Rejected:* a hue per kind (Jira's red bug): the README's
  "status colour only where it means something", and a bug is not an alarm.

### Bug report

*Decided:* steps, expected, actual, environment, `introduced_by`; in the dialog and on the page.

- Stored on the task as nullable columns, only when kind = bug: `bug_steps` (Markdown, ≤ 8,192),
  `bug_expected`, `bug_actual` (≤ 2,000 each), `bug_environment` (≤ 120). Steps are required to
  create or save a bug ("A bug needs its steps: how to make it happen"); the rest optional.
  Changing kind away from bug keeps them (unsaved for display) until the next save, so a slip
  of the Select loses nothing.
- **In the task dialog**: Kind and Priority are the aside's first two fields, above Epic. With
  Bug, a `Fieldset` "The bug" follows the Goal: Steps to reproduce (`MarkdownEditor`, numbered),
  Expected and Actual (`Textarea`s side by side), Environment (`Input mono`, short). Introduced by
  is in the aside, optional.
- **On the task page**: a "Bug report" block after the Goal, a two-column `dl`, Introduced by last.

#### Introduced by

- `introduced_by_task_id` (a task in the same project), `introduced_by_state`
  (`proposed`|`confirmed`), `introduced_by_actor` (who proposed or confirmed it), and the
  evidence as `introduced_by_note` (≤ 2,000). A pull request is named through its task: every
  pull request dude merged belongs to one.
- **An agent proposes it** (`update_task {introducedBy: {task: "CP-30", evidence}}`, the
  caller's own bug only; investigator or conductor). How it finds a candidate, *proposed*:
  dude knows the order merges landed on the default branch (`pull_requests.merged_at` and the
  merge commit). The agent bisects **by merge order**: runs the reproduction at the merge before
  and after candidates, narrowing to one merge whose commit flips it from passing to failing.
  The tool refuses a proposal without evidence naming two commits. That is the chip's "Why"
  ("passes at a41c9e2 … fails at 7d03b11, its merge").
- **Shown as "Likely CP-30 · merged 2 Oct"**, a dashed chip (`IntroducedByChip
  state="proposed"`): **Confirm** / **Clear** for the owner; for anyone else, whose call it is.
  Confirmed, the chip is solid and says who confirmed it. The owner can set it by hand
  (`PATCH … {introducedBy}`), which is `confirmed` at once.
- *Proposed:* **no automatic proposal.** dude could guess from file overlap with recent merges,
  but a guess that looks like a fact is worse than none; a candidate needs a reproduction run.

### Priority

*Decided:* `P0`–`P3`, default `P2`; tasks sort by priority within their lane, then age; no
position on tasks.

- **Shown quietly**, *proposed*: **P0** with an up arrow and **P1** in strong ink, semibold,
  mono; **P2 not drawn** on cards, rows and results (it is the default, and a mark on 80% of
  cards is noise); **P3** in muted ink. The header, the rail and the dialog always show it, P2
  too. *Rejected:* a coloured flag: P0 would compete with needs-you, the one loud thing.
- **Sort:** within a lane, triage first (needs you, active, ready, failed: unchanged), then
  priority, then newest. The board's model sorts; nothing drags.
- **Epics keep `position`** as their order; a task's priority doesn't roll up to its epic.

### No labels

*Decided.* Kind says what sort of work, priority how much it matters, epic what it belongs to,
repository where it lives. A label on top of those is a second, unchecked copy of one of them, or
a word one person made up and nobody else filters on. Free labels rot: "billing", "Billing",
"billing-v2" in a year. If a real need for an unfixed dimension appears, it is a field with a
fixed set of values, decided then (see *Not here*).

### Classification screens

| Screen | What it shows |
|---|---|
| `task-dialog-bug` | the task dialog with Kind = Bug, Priority P1, the bug fields, Introduced by in the aside |
| `bug-task` | CP-61, a running bug: the header's kind and priority, the bug report, **Introduced by CP-40**, confirmed |
| `introduced-by-proposed` | CP-60, a P0 bug: "Likely CP-30 · merged 2 Oct", Confirm / Clear, and the evidence |
| `board-kinds` | the board: kind glyphs and P0/P1/P3 after each card's key, cards in priority order |
| `proposal-card-kinds` | a brainstorm's `ProposalCard` with a bug, a spike, a feature and a chore |
| `brief-bug` | the implementer's brief and the tests reviewer's gate, feature beside bug, and a spike's |

## Epic 3 — Finding things

### `GET /v1/tasks`

*Decided:* the filters, keyset paging, sort; the same filters reach `list_tasks` and `cmd/dude`.

```
GET /v1/tasks
  ?project=<id>                      required unless q has #KEY (a key names its project)
  &status=open|closed|<status>,…     open = not done/failed/aborted; several, comma-separated
  &kind=bug,spike                    &priority=P0,P1
  &owner=<personId>|me               (mine = owner=me)
  &epic=<id>|none                    &repository=<id>
  &later=exclude|only|include        default exclude for open views, include otherwise
  &archived=exclude|only|include     default exclude
  &needs=you                         an agent of yours asking, or a plan waiting for you
  &q=<words>                         key (exact, "CP-41" or "41"), title and goal
  &sort=priority|created|updated|later   default priority (then created desc)
  &limit=50 (≤ 200)  &cursor=<opaque>
→ { tasks: [...TaskSummary], nextCursor: string | null }
```

- **Keyset paging.** The cursor encodes the sort key's last values and `id`: for the default
  sort, `(priority_rank, created_at, id)`; for `created`, `(created_at, id)` as decided.
  `WHERE (priority_rank, created_at, id) > ($rank, $created, $id)` in the order's direction.
  No `OFFSET`. The cursor is opaque (base64 of the tuple), so the sort can change without
  breaking clients that pass it back.
- **Text.** `q` matches `tasks.search` (a generated `tsvector`: key and title weight A, goal
  B, the `simple` and `english` configurations as `memory_chunks` does, `054_memory.sql:128`),
  `ORDER BY ts_rank` when no sort is given. A key in `q` is an exact match, ranked first.
  *Rejected:* `ILIKE '%q%'` (what `list_tasks` does today, `tools.go:187`): fine for one
  project's 200 tasks, a sequential scan over an organisation's.
- **The query** is built with the backend's `scope.sql` fragments, one `AND` per given filter,
  as `listTasks` already composes its two (`work.ts:325`). No string concatenation.
- **Indexes** (all partial on `archived_at IS NULL` where the view excludes archived, which is
  every default):

  ```sql
  CREATE INDEX tasks_list_idx      ON tasks (project_id, priority, created_at DESC, id) WHERE archived_at IS NULL;
  CREATE INDEX tasks_created_idx   ON tasks (project_id, created_at DESC, id);          -- archived=include, sort=created
  CREATE INDEX tasks_kind_idx      ON tasks (project_id, kind, priority) WHERE archived_at IS NULL AND kind <> 'feature';
  CREATE INDEX tasks_later_idx     ON tasks (deferred_until) WHERE deferred_at IS NOT NULL;  -- the worker, and the Later view
  CREATE INDEX tasks_archived_idx  ON tasks (project_id, archived_at DESC) WHERE archived_at IS NOT NULL;
  CREATE INDEX tasks_search_idx    ON tasks USING gin (search);
  CREATE INDEX epics_later_idx     ON epics (deferred_until) WHERE deferred_at IS NOT NULL;
  ```
  Owner is `task_people` at position 0 (`056_person_ownership.sql`); `owner=` joins it on its
  existing primary key. Status filters use the existing `work_items_project_status_idx`.
- **`list_tasks`** gains the same filters as parameters (`kind`, `priority`, `owner: "me"|name`,
  `status`, `later`, `archived`, `epic`, `q` replacing `text`, which stays as an alias), returns
  `kind`, `priority`, `later`, `resolution` per task, and pages at 100 with `cursor`. Its query
  becomes the backend's (one SQL function `task_list(...)` both call), so an agent and a person
  never see a different list.
- **`cmd/dude task list`** (`orchestrator/cmd/dude/main.go:136`): `--kind bug --priority P0,P1
  --mine --status open --later --archived --epic "Billing v2" --q csv`, and `dude task list
  "kind:bug p0 csv"`, which reads the same tokens as Find work.

### The tasks list

*Decided:* a list per project, dense rows, filter bar, fixed views: Mine, Bugs, Needs you, Later,
Archived, Closed.

- **Route** *(proposed)*: `#/project/:id/tasks?view=bugs` beside management.md's
  `#/project/:id` (the board). The board's header gains a `Segmented` **Board · List**, the same
  control in the same place on both, so one click switches with the project kept. The tree's
  project row opens whichever was last used. Filters are in the hash (`&kind=bug&priority=P0`),
  so a filtered list is a link.
- **Views** *(decided list, proposed order)*: **Open** (the default: not ended, not archived,
  not on Later), **Mine**, **Bugs**, **Needs you**, **Later**, **Closed**, **Archived**, as one
  `Segmented` with counts. They are fixed: everyone's Bugs is the same, and there is nothing to
  name, save or share. *Proposed:* **Open** added as the default; without it the list's first
  screen would be one of the narrow views.
- **Filter bar**: Search (key, title, goal), then chips: Kind, Priority, Owner, Epic,
  Repository (`TaskFilterBar`). A chip is a `RowMenu` of radio items; set, it says its value and
  has its own ✕. A view sets its own filter (Bugs is `kind=bug`); the chip shows it set, and
  clearing it leaves the view (to Open), so the two can't disagree.
- **Rows** (`TaskRow`): status glyph and key, kind glyph (none for a feature), title, then
  resolution / Later / Archived marks, epic, priority (none for P2), owner's face, age, and the
  row's `…` with the lifecycle actions. 32px, compact 28px. Under 720px of its own width the
  epic, priority and age drop, as `Table`'s `hideWhenNarrow` does.
- **Paging**: the next 50 load as the list nears its end; the foot says how many are shown and
  the order. No page numbers: keyset paging has none.

### Find work

*Decided:* one input; `kind:bug p0 owner:ana #CP-41 is:deferred` tokens plus words; archived
and deferred results marked.

- **Tokens** *(proposed spelling)*: `kind:bug|feature|chore|spike`, `p0`…`p3`, `owner:<first
  name>` and `is:mine`, `#CP-41` (or a bare `CP-41`), `is:later`, `is:archived`, `is:closed`,
  `is:open`, `is:blocked`, `epic:"Billing v2"`. Everything else is words. **`is:later`**, not
  the brief's `is:deferred`, because the word on screen is Later; `is:deferred` is accepted as
  an alias. Tokens are case-insensitive; an unknown `x:y` is a word.
- **One input.** The field stays the Sidebar's search; what it understood shows **under** it, as
  chips ("kind:bug owner:ana is:later" then “csv”) with a sentence ("Bugs · owned by Ana · on
  Later · with “csv”: 1 match"), never as editable pills inside it. Backspace edits text, as in
  any field.
- **Results**: with any token, or any word that isn't in the loaded tree, Find work asks the
  server (`GET /v1/tasks?…&later=include&archived=include`, the tokens mapped one to one) and
  shows results **in place of the tree**: matches first, then, when a token narrowed it, the
  same query without its `is:` token, so "where else is it" is one glance. Archived, Later and
  closed results come last, in secondary ink, marked (`ArchivedMark`, `LaterMark`,
  `ResolutionMark`). With words only, the tree filters as today (that is instant and stays).
- **Keys**: ↓ from the field enters the results, Enter opens, Esc clears (today's grammar).

### Board filters

*Decided:* a chip row (kind, priority, mine) beside the existing relations filter.

- In the board header, after **All · Can start · Blocked** (the existing `Segmented`): **Kind**,
  **Priority** (`TaskFilterBar` chips) and **Mine** (a pressed toggle, as the sidebar's
  Everyone · Mine). The relations filter's counts follow the chips: "All 4 · Can start 1 ·
  Blocked 0" with Kind: Bug and Mine set. The Later shelf follows them too.
- Persisted per project in the hash (`#/project/cp?kind=bug&mine=1`), like the list's.

### Finding screens

| Screen | What it shows |
|---|---|
| `tasks-list` | the Open view: segmented views with counts, search, chips, rows |
| `tasks-list-view-bugs` | the Bugs view: P0 first |
| `tasks-list-archived` | the Archived view |
| `find-work-tokens` | `kind:bug owner:ana is:later csv`: what it understood, 1 match, then Ana's other bugs marked |
| `board-filters` | the board with Kind: Bug and Mine set |

## Field × surface

✓ shown or set there; R read only; — not there, deliberately; *(why)* where it isn't obvious.

| Surface | kind | priority | bug report | introduced by | Later | resolution | archived |
|---|---|---|---|---|---|---|---|
| Task dialog, new | ✓ aside | ✓ aside | ✓ (bug) | ✓ aside | — *(put on Later after it exists)* | — | — |
| Task dialog, edit | ✓ until delivery starts | ✓ | ✓ | ✓ | — *(the menu)* | — | — |
| Task page header | ✓ word | ✓ always | — | — | ✓ mark + Bring it back | ✓ beside status | ✓ mark |
| Task page notices | — | — | — | — | ✓ Callout | ✓ Callout + Reopen | ✓ Callout + Unarchive |
| Task page Overview / rail | ✓ rail, what it changes | ✓ rail | ✓ block | ✓ chip, Confirm/Clear | ✓ rail | ✓ rail | — |
| Task `…` menu (header, tree, row) | — | — | — | — | ✓ Later… / Bring it back | ✓ Close… / Reopen | ✓ Archive… / Unarchive |
| Board card | ✓ glyph (not feature) | ✓ P0 P1 P3 | — | — | on the shelf | ✓ marks line | — *(not on the board)* |
| Board header | ✓ chip | ✓ chip | — | — | ✓ shelf | — | ✓ "3 archived" |
| Sidebar tree row | — *(28px row: key and status suffice)* | — | — | — | ✓ Later row | — | — *(not in the tree)* |
| Find work results | ✓ glyph | ✓ | — | — | ✓ marked, last | ✓ | ✓ marked, last |
| List row | ✓ glyph | ✓ | — | — | ✓ mark | ✓ | ✓ mark |
| List filter / view | ✓ chip, Bugs | ✓ chip | — | — | ✓ Later | ✓ Closed | ✓ Archived |
| Proposal card | ✓ tag + glyph | ✓ tag | ✓ detail | — | ✓ "files onto Later" | — | — |
| `create_task` | ✓ | ✓ | ✓ | — | ✓ | — | — |
| `update_task` | ✓ own, before start | ✓ own | ✓ own | ✓ propose, own bug | ✓ own task | — | — |
| `list_tasks` | ✓ filter + field | ✓ | — | ✓ field | ✓ filter + field | ✓ field | ✓ filter |
| `propose` | ✓ | ✓ | ✓ | — | ✓ | — | — |
| `cmd/dude` | ✓ | ✓ | ✓ `--steps` … | — | ✓ `task later` | — | ✓ `--archived` |
| `GET /v1/tasks` | ✓ | ✓ | — | — | ✓ | ✓ | ✓ |
| `/v1/navigation` (`NavTask`) | ✓ | ✓ | — | — | ✓ `later` | ✓ | — *(archived not sent)* |
| Brief / review | ✓ | — *(priority isn't a delivery input)* | ✓ | — | — | — | — |
| Ledger | `task.kind_changed` | `task.priority_changed` | `task.updated` | `task.introduced_by_proposed` / `_confirmed` / `_cleared` | `task.deferred` / `task.came_back` | `task.closed` / `task.reopened` | `task.archived` / `task.unarchived` |
| Activity line | “changed CP-61 from Feature to Bug” | “…and its priority from P2 to P1” | — *(the text's edit line)* | “proposed that CP-30 introduced CP-60” / “confirmed …” | ✓ both ways | ✓ | ✓ both ways |

## Design-system changes

Every new piece is drawn in `apps/web/mockups/tracking/Marks.tsx` with the real primitives and
tokens; nothing in `packages/design-system` changed. Each needs a gallery section showing every
state in both themes and densities.

### `KindMark` *(new)*

`{kind: "bug"|"feature"|"chore"|"spike", variant?: "glyph"|"word", quietDefault?: boolean}`.
Gallery `#kind-mark`: four kinds × glyph / word, `quietDefault` on a feature (renders nothing).

> **Kind.** A task's kind is a glyph from the set — `alert` a bug, `question` a spike, `wrench` a
> chore, `sparkle` a feature — in secondary ink, a bug's in strong ink, its word in the tooltip
> and the accessible name. Never a hue. Cards, rows and results draw no glyph for a feature
> (`quietDefault`): the default says nothing. The header and the dialog say the word.

### `PriorityMark` *(new)*

`{priority: "P0"…"P3", showDefault?: boolean}`. Gallery `#priority-mark`: four priorities ×
`showDefault`.

> **Priority.** `P0`–`P3` in mono, at the xs size. P0 (with an up arrow) and P1 in strong ink,
> semibold; P2 is not drawn unless `showDefault` (the header, the rail, the dialog); P3 muted.
> Never a tone: P0 must not compete with needs-you.

### `ResolutionMark`, `LaterMark`, `ArchivedMark` *(new)*

`ResolutionMark {resolution, of?: key}` is a neutral `Badge size="sm"` ("Duplicate of CP-62").
`LaterMark {until: string|null, compact?}` is the `clock` glyph and "Later · 3 Nov" in muted
ink; `ArchivedMark` the `archive` glyph and "Archived". Gallery `#lifecycle-marks`.

> **Later, closed, archived.** Neutral, muted, never a tone: none of them needs anyone. A
> closed task's resolution sits beside its status, which still says what delivery did.

### `LaterGate` *(new; `StartGate`'s sibling)*

`{taskKey, reason, starts: [{id, label, icon}], onBringBack?, bringBackHint?, size?}`. The
header's (and a shelf card's) start on a task on Later: every way to start stays, disabled,
each wrapped in the focusable span `StartGate` uses, `aria-describedby` the one reason; then
**Bring it back**, or, for a reader who may not, the same reachable disabled control with whose
it is. No *Start anyway*. Gallery `#later-gate`: owner, member, epic's shelf, `size="sm"`.

> **A task on Later can't start.** `LaterGate` takes `StartGate`'s place in the header: Deliver
> and Talk it through stay, disabled, reachable, the reason their description ("CP-62 is on Later
> until 3 Nov: bring it back to start it."), then Bring it back. There is no Start anyway: the
> shelf is its owner's decision, and Bring it back is one click.

### `StartMenu` *(changed)*

Gains `later?: {reason, onBringBack?, hint?}`: the trigger carries the `clock` glyph and the
reason; both ways disabled with it; the menu's foot is Bring it back. On the shelf a card's start
is drawn as `LaterGate size="sm"` (mockup), the same rule.

### `Board` *(changed)*

- `cardLead?: (card) => ReactNode` — after the key on a card's first line; defaults to
  `KindMark quietDefault` and `PriorityMark` from `NavTask.kind` / `.priority`.
- `later?: { tasks: NavTask[]; epics: NavEpicLater[]; open; onOpenChange }` — the **Later shelf**
  under the header: folded to one line (count, keys and dates), open to cards with their gate;
  outside the five lanes and every count.
- `chips?: ReactNode` beside the relations filter (`TaskFilterBar`).
- `boardModel.ts`: within a lane, triage rank, then `PriorityRank`, then the caller's order.
- `cardMarks` default: a closed task's `ResolutionMark`; `DoingLine` says "closed" for one with a
  resolution instead of "stopped".

README, *Board*, new bullet:

> **Later is a shelf, not a lane** (`later`): under the header, folded to one line by default
> ("Later 4 · CP-62 3 Nov · CP-64 no date · Usage alerts · 2 tasks"), open to a row of cards
> whose start is refused with its reason, Bring it back beside it. It is outside the five lanes,
> their counts and the filters' counts; the chips filter it too. Archived tasks are not on the
> board; the header's quiet "3 archived" opens the list's Archived view.

### `TaskFilterBar` *(new)*

`{chips: [{id, label, value?, items?: RowMenuItem[], onClear?, pressed?, onToggle?}], label?}`.
A chip is a `RowMenu` trigger (radio items); set, it shows "Kind: **Bug**" on the active wash
with its own ✕; a toggle chip (Mine) is `aria-pressed`. Gallery `#filter-bar`: unset, set,
pressed, open menu, wrapped narrow.

> **Filters are chips.** One per field, each a `RowMenu` of its values; set, it says the value
> and has its own clear. A chip row filters what it sits over and nothing else; the relations
> filter stays a `Segmented` (two or three views of the same cards).

### `TaskRow` / `TaskList` *(new)*

`TaskRow {row: TaskRowData, menu?, onOpen?}`; `TaskList` is the `<ul>` with the container query.
The `SearchResultRow` grammar: a 32px row (28 compact), one button that opens, the menu a
sibling. Gallery `#task-row`: open, on Later, closed (each resolution), archived, narrow.

> **A task in a list** is `TaskRow`: status glyph and key, kind glyph, title, its lifecycle marks,
> epic, priority, owner's face, age, then its `…`. The row opens the task; the menu is its
> sibling, never inside it. Under 720px of its own width it drops epic, priority and age.

### `IntroducedByChip` *(new)*

`{taskKey, title, merged, state: "proposed"|"confirmed", by, onConfirm?, onClear?, hint?}`.
Proposed: a dashed edge (the edge is the meaning: not yet a fact; listed in
`test/borders.test.ts` with that reason), "Likely CP-30 · merged 2 Oct", Confirm / Clear for the
owner. Confirmed: the chrome shade, solid. Gallery `#introduced-by`: proposed (owner, member),
confirmed, none.

### `RelationList` *(changed)*

A fourth group, **Duplicate of** / **Duplicated by**, `copy` glyph, its rows not removable (Reopen
removes it), and an optional line under it ("CP-63's blocking came here with it: CP-47 now waits
on CP-62."). `RelationGroupKind` gains `"duplicate-of" | "duplicated-by"`.

### `Sidebar` *(changed)*

`search?: { results: ReactNode; understood?: ReactNode }` — while given, the tree's head and tree
give way to `results`, `understood` (the token chips and sentence) under the field. The rail's
search and `/` are unchanged. A **Later** row in `NavProject` (`later?: NavTask[]`), drawn after
the epics with the `clock` glyph, never opened by triage, never counted.

### `NavTask` *(changed)*

`kind`, `priority`, `later?: {until: string|null}`, `resolution?`. Archived tasks are not sent.

## Migration sketch

Next free number: **132** (`131_fake_secret_models.sql` is the last).

```sql
-- 132_tracking_work.sql — Later, Close, Archive; kind, priority, a bug's report;
-- the list's search and indexes (docs/design/tracking-work.md).

CREATE TYPE task_kind       AS ENUM ('bug', 'feature', 'chore', 'spike');
CREATE TYPE task_priority   AS ENUM ('P0', 'P1', 'P2', 'P3');          -- enum order is sort order
CREATE TYPE task_resolution AS ENUM ('wont_do', 'duplicate', 'cant_reproduce', 'nothing_to_merge');
ALTER TYPE task_relation_kind ADD VALUE 'duplicates';

ALTER TABLE tasks
  ADD COLUMN kind      task_kind     NOT NULL DEFAULT 'feature',
  ADD COLUMN priority  task_priority NOT NULL DEFAULT 'P2',
  ADD COLUMN resolution task_resolution,
  ADD COLUMN closed_by  text REFERENCES people(id) ON DELETE SET NULL,
  ADD COLUMN deferred_at    timestamptz,
  ADD COLUMN deferred_until date,
  ADD COLUMN deferred_by    text,                                     -- a person id, or a run id for an agent
  ADD COLUMN archived_at timestamptz,
  ADD COLUMN archived_by text,                                        -- as memories.archived_by
  ADD COLUMN bug_steps text, ADD COLUMN bug_expected text, ADD COLUMN bug_actual text, ADD COLUMN bug_environment text,
  ADD COLUMN introduced_by_task_id text REFERENCES tasks(id) ON DELETE SET NULL,
  ADD COLUMN introduced_by_state   text CHECK (introduced_by_state IN ('proposed', 'confirmed')),
  ADD COLUMN introduced_by_actor   text,
  ADD COLUMN introduced_by_note    text CHECK (length(introduced_by_note) <= 2000),
  ADD COLUMN search tsvector GENERATED ALWAYS AS (
    setweight(to_tsvector('simple', title), 'A') || setweight(to_tsvector('english', title), 'A')
    || setweight(to_tsvector('english', coalesce(goal, '')), 'B')) STORED,
  ADD CONSTRAINT tasks_later_until  CHECK (deferred_until IS NULL OR deferred_at IS NOT NULL),
  ADD CONSTRAINT tasks_resolution_status CHECK (resolution IS NULL OR status IN ('done', 'aborted')),
  ADD CONSTRAINT tasks_archived_ended    CHECK (archived_at IS NULL OR status IN ('done', 'failed', 'aborted')),
  ADD CONSTRAINT tasks_bug_fields CHECK (kind = 'bug' OR (bug_steps IS NULL AND introduced_by_task_id IS NULL)),
  ADD CONSTRAINT tasks_bug_lengths CHECK (length(bug_steps) <= 8192 AND length(bug_expected) <= 2000
                                          AND length(bug_actual) <= 2000 AND length(bug_environment) <= 120);

ALTER TABLE epics
  ADD COLUMN deferred_at timestamptz, ADD COLUMN deferred_until date, ADD COLUMN deferred_by text,
  ADD COLUMN archived_at timestamptz, ADD COLUMN archived_by text;

-- Today's "done, nothing merged" gets its name.
UPDATE tasks t SET resolution = 'nothing_to_merge'
 WHERE t.status = 'done'
   AND NOT EXISTS (SELECT 1 FROM pull_requests pr WHERE pr.task_id = t.id AND pr.state = 'merged');

-- Indexes as in "GET /v1/tasks" above, plus:
CREATE INDEX tasks_introduced_by_idx ON tasks (introduced_by_task_id) WHERE introduced_by_task_id IS NOT NULL;

-- task_blocked_by follows a duplicate's blocking to its canonical task (one hop);
-- task_start_refusal(task) → 'archived' | 'later' | 'blocked' | NULL, both processes' one rule;
-- task_relation_would_cycle walks 'duplicates' as 'blocks'.
-- (CREATE OR REPLACE of the first and third, as in 111.)
```

- **Lock cost.** `ADD COLUMN … DEFAULT` with a constant is metadata-only; the `GENERATED … STORED`
  column rewrites `tasks` once, so the migration takes `tasks`' `ACCESS EXCLUSIVE` lock for that
  rewrite. dude's `tasks` is small (thousands of rows per organisation); if it is not, the
  `search` column moves to a follow-up migration filled in batches by a trigger.
- **`ALTER TYPE … ADD VALUE`** can't run in the same transaction as a use of the new value: the
  functions that use `duplicates` go in `133`.
- Row-level security: unchanged (the columns sit on tables that have it). The Later worker needs
  `dude_sweeper` `SELECT, UPDATE (deferred_at, deferred_until, deferred_by)` on `tasks` and `epics`.

## Open questions for Márcio

1. **Should a Won't do blocker keep blocking?** The rule says yes (only merged work releases),
   and the close dialog warns. The other reading: a person closing it has decided the work isn't
   needed, so neither is the wait. *Default: keeps blocking; the dialog offers to remove the
   relation in the same step.*
2. **Archive only ended tasks, or anything not running?** The brief refuses only running ones; this
   doc also refuses Backlog ("Close it first"), so nothing archived can start. *Default: ended only.*
3. **Is Later per task enough, or do you want "Later" on the board as a filter instead of a
   shelf?** A filter would show Later cards in Backlog when asked; the shelf keeps them out of
   Backlog always. *Default: the shelf.*
4. **The bug gate's home when `tests` isn't a required reviewer.** This doc moves it to
   `correctness` so a bug can't ship without its failing test. Some projects turned `tests` off on
   purpose. *Default: correctness carries it; a project can switch the gate off in Delivery
   ("Bugs need a failing test", on by default).*

## Not here

- **Intake** (external bug reports, GitHub issues becoming tasks): deferred; when it comes, an
  issue becomes a task with kind and bug report filled, and these fields are its target.
- **Planning** (milestones, due dates, roadmap): deferred; Later's date is when work *may*
  start, not when it is due, and must not be read as one.
- **Labels**: not planned (see *No labels*).
- **Custom fields**: not planned; a new dimension is a fixed-value field decided for everyone.
- **Subtasks**: not planned; an epic is the group, and `blocks` is the order.
- **Bulk edit** (close, defer, archive, re-prioritise many rows at once): later; the list's
  rows are built to take a checkbox column when it comes.
- **User-defined saved views**: not planned; the views are fixed (decided).
- **Hard delete of a task**: not planned. Close is the way out; an org admin's delete stays the
  database's.
