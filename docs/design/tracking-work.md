# Tracking work: lifecycle, classification, finding things

*Decided: the build spec. Clickable mockup: [`mockups/tracking/index.html`](mockups/tracking/index.html)
(`?screen=…&theme=dark|light&density=comfortable|compact&role=owner|member`), built from
`apps/web/mockups/tracking/`. Epic 1 (Lifecycle) is built; see *Epic 1 as built*. Epics 2 and
3 are not.*

Throughout, **Decided** marks what Márcio fixed, in the brief or in *Decisions* (2026-10-17);
each names the alternative it rejected. Where the brief's first wording turned out to need a
change to be buildable, the change is called out as **Correction**.

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
- **An epic looks like a task in the sidebar.** The design system's README (*Navigation*, "epics
  carry the layers glyph and a total"), `NavTree.tsx`'s header comment and the gallery's
  `#nav-tree` note all say so, but `NavTreeRow`'s epic branch (`NavTree.tsx:385-394`) draws only
  the title, the needs-you count and the total. An epic row reads as a task row without a key.

Jira solves these for people with statuses, resolutions, issue types, priorities, labels,
components, custom fields and JQL. dude needs a much smaller set, because most of what those
fields carry, delivery already knows. This document adds three things that share one vocabulary:

1. **Lifecycle**: end work with a reason (**Close**), put work away (**Archive**, closing it on
   the way when it hasn't ended).
2. **Classification**: a **kind** (bug, feature, chore, spike) that changes what the agents are
   told and what review checks; a **priority**; a **bug report** with what caused it.
3. **Finding things**: a list beside the board with fixed views, filters on the fields above,
   `GET /v1/tasks` that can answer them, and Find work that reads `kind:bug p0`.

Plus one fix the third epic leans on: the sidebar's epic rows get the glyph the design system
already promises them (*`NavTree` epic rows (fix)*). What was considered and left out is in
*Not here*.

One document, because the third epic filters on exactly the fields the first two add, and
because every field must reach the same dozen surfaces (see *Field × surface*).

## Words

*Decided.* Each word is one a person already uses for the thing, and none is taken by
something else in dude.

| Word | Means | Rejected, and why |
|---|---|---|
| **Close** | a person ends a task, saying why | "Resolve" (implies it was fixed), "Cancel" (wrong for a duplicate), "Abort" (already delivery's word for a stopped run) |
| **Reopen** | undo Close | — |
| **Won't do** / **Duplicate** / **Can't reproduce** / **Nothing to merge** | the four resolutions | "Obsolete" (folded into Won't do: the distinction isn't acted on), "Done" as a resolution (that is a status) |
| **Archive** / **Unarchive** | take work out of every everyday view, and back | "Hide" (sounds per-person), "Delete" (it isn't) |
| **Close and archive** | Archive on a task that hasn't ended: one dialog, one confirmation, both | a refusal ("Close it first"): two dialogs for one intent |
| **Bug**, **Feature**, **Chore**, **Spike** | the four kinds | "Task" as a kind (every one is a task), "Story" (product jargon), "Investigation" (spike is shorter and known) |
| **P0**–**P3** | priority | words ("Urgent", "High") as the stored value: the numbers sort, compare and fit a card; the words are their tooltips |
| **Introduced by** | the task that caused a bug | "Regressed by" (only right if it once worked), "Caused by" (too sure for an agent's guess) |

## Epic 1 — Lifecycle

### Model

Two independent facts on a task beside its status, neither of them a status:

```
status            what delivery did        received … done | failed | aborted     (unchanged)
resolution        why a person ended it    null | wont_do | duplicate | cant_reproduce | nothing_to_merge
archived          out of everyday views    archived_at, archived_by
```

They compose: a task can be closed as Won't do and archived; a bug, done, with nothing to
merge. **Status stays the only thing delivery reads and writes.** Lifecycle writes its own
columns, and one status transition each for Close and Reopen.

**Ended**, for lifecycle, means `done`, or `aborted` with a resolution. A task that stopped
(`failed`, or `aborted` by Stop) without one has not ended: Close is still offered on it, and
Archive closes it on the way.

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
  with no resolution). *Decided:* not for a running one ("Stop its agents first"). Stopping is
  already a decision with its own dialog; folding it into Close would make one click end live
  work. Not for `review` or `ready_to_merge` either ("It has a pull request open"): closing that
  is GitHub's or Mark done's.
- **Nothing to merge is not offered in Close** ("Only for work that ran: Mark done gives this,
  once there is a write-up or an answer"). `markDone` writes it: the route stays, and from now on
  stores `resolution = 'nothing_to_merge'` on the task, so today's "done, nothing merged" gets
  its name and the board, list and API say it the same way. Existing done-without-a-merge tasks
  get it in the migration.
- **Can't reproduce is a bug's** (refused on other kinds, with that reason): there is nothing to
  reproduce in a feature.
- **Reopen** clears the resolution and puts the task back to `received`, keeping its runs. A
  duplicate's edge is removed on reopen, and what was handed over goes back. On an archived task
  Reopen is refused ("Unarchive it first, then reopen it"): the page's one button is Unarchive,
  and Reopen follows it. The two stay separate steps.
- **Why a Won't do still blocks.** *Decided* (Q1). The relations doc's rule is "only merged work
  releases what waits on it". A task nobody will do hasn't produced what the waiting one needed;
  releasing its dependants would start work on a missing foundation. When something waits on
  the task, the close dialog says so for every resolution but Duplicate, which hands the
  blocking over ("**CP-47 waits on this.** It will keep waiting: a task closed without merging
  releases nothing."), with a **Remove the relation** checkbox. Checked, the close also deletes
  those `blocks` edges in the same transaction (`removeRelations: true`; one
  `task.relation_removed` each, as Remove relation writes), and the callout says the waiting
  task can start without it.

#### Duplicate: the `duplicates` relation, built

`task-relations.md` pencilled `duplicates` for a future round. It is built here, as a third
`task_relation_kind`: `duplicates`, from the closed task to the one it duplicates, written only
by Close (never by Add relation) and removed only by Reopen. It shows as **Duplicate of** on the
closed task and **Duplicated by** on the other, its own group in `RelationList`, with the `copy`
glyph.

**A duplicate blocker hands its blocking to the task it duplicates.** *Decided,* with the
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
an epic with unfinished tasks refused, offering to archive its ended ones; refused with
running agents; owner or admin; Activity both ways.

- **Archiving an unended task closes it first.** *Decided* (Q2). Archive… on a task that hasn't
  ended (Backlog, or stopped without a resolution) opens the Close dialog as **Close and
  archive** (screen *archive-open-task*): title "Close and archive CP-44", no resolution chosen,
  the primary "Close and archive" disabled until one is. The Close rules hold unchanged: the
  Duplicate picker and its handover, Can't reproduce for bugs only, Nothing to merge not offered,
  the *waits on this* callout with Remove the relation. One confirmation closes the task with
  that resolution and archives it in one transaction: `task.closed` then `task.archived`, one
  ledger batch, and **one** Activity line ("**Ana** closed **CP-44** as Won't do and archived
  it"). An archived task is therefore always ended, and nothing archived can start
  (`tasks_archived_ended` in the migration holds it).
- **On an ended task, Archive… is the plain confirmation** (screen *archive-task*, CP-46).
- **A running task, or one with a pull request open, can't be archived**, exactly as it can't be
  closed: the menu item is disabled with "Stop its agents first" (`running`, `awaiting_input`)
  or "It has a pull request open" (`review`, `ready_to_merge`).
- **Back out in two steps.** An archived, closed task shows Unarchive; once unarchived, Reopen.
  *Rejected:* one "Unarchive and reopen": the two undo different decisions, and most unarchives
  (an ended task wanted back in a count) don't want the reopen.
- **Archiving an epic archives nothing by itself.** With unfinished tasks it is refused
  (screen *archive-epic-refused*), listing them; the dialog's one action is "Archive its N ended
  tasks", which archives those tasks (one `task.archived` each, and one Activity line on the
  epic) and leaves the epic. With every task ended, Archive archives the epic and its tasks
  together, in one transaction. The epic doesn't get close-all: each unended task needs its own
  resolution, and N resolutions can't be picked honestly in one dialog.
- **Archived tasks keep everything**, including relations: an archived blocker still blocks (the
  guard doesn't read the blocker's `archived_at`). An archived task can't be started or picked
  back up until it is unarchived (*Start paths*, path 7).
- The hint on the board ("3 archived", quiet, beside New task) opens the list's Archived view.

### UX

| Screen | What it shows |
|---|---|
| `close-dialog` | Close CP-63 on Won't do: four resolutions as a `ChoiceList`, two disabled with why, and the *CP-47 waits on this* callout with its **Remove the relation** checkbox. `&why=duplicate`: the same dialog on Duplicate, its `SearchPicker` and what the handover does |
| `closed-wontdo` | CP-46 closed as Won't do: the resolution beside the status, a `Callout` with Reopen and Archive |
| `closed-duplicate` | CP-63 and CP-62 side by side: Duplicate of / Duplicated by, and CP-47's blocking handed to CP-62 |
| `archive-task` | the plain Archive confirmation for CP-46, which has ended |
| `archive-open-task` | Archive… on CP-44, in Backlog: the Close dialog as **Close and archive CP-44**, no resolution chosen, its primary "Close and archive" disabled until one is; as a member, the primary disabled with whose decision it is |
| `archive-epic-refused` | Webhook reliability refused, its 4 unended tasks listed, "Archive its 2 ended tasks" |
| `archived-view` | the tasks list's Archived view, read by a member: every action in the row menu disabled with whose it is |
| `activity-lifecycle` | every new Activity line, both ways, and the one line Close and archive writes (see *Events*) |

- **Lifecycle actions live in the task's `…` menu** (the existing `RowMenu` in the header, the
  tree row, the list row): Close… (Reopen on a closed task), Archive… / Unarchive. Each is shown
  to everyone, **disabled with its reason** for a reader who may not ("Only Ana, the owner, or an
  admin can do this") or when the task's state refuses it, per the README's rule that an option
  is never greyed out without its reason (`RowMenu disabledReason`, `ChoiceList disabledReason`)
  and *A disabled control the reader can still reach* (Task relations). On the page, the one
  action that matters now is a button too: Reopen on a closed one, Unarchive on an archived one.
- **The menu's `disabledReason`s**, in the order checked:

  | Item | Who may not | `running`, `awaiting_input` | `review`, `ready_to_merge` | ended | archived |
  |---|---|---|---|---|---|
  | Close… | "Only Ana, the owner, or an admin can do this" | "Stop its agents first" | "It has a pull request open" | "Already ended" (the item reads "Closed"; on a closed task it is Reopen) | — |
  | Reopen | same | — | — | — | "Unarchive it first, then reopen it" |
  | Archive… | same | "Stop its agents first" | "It has a pull request open" | enabled: plain confirmation | (Unarchive instead) |

  Archive… on a task not started or stopped without a resolution is enabled and opens Close and
  archive.
- **Closed cards** in Done show their resolution in the card's marks line, where today's says
  "stopped".

### Agents and CLI

- `list_tasks` leaves archived tasks out unless `archived: true`, and returns each task's
  `resolution`.
- Close and Archive are not agent tools. A conductor whose start is refused as archived gets the
  refusal's words and its prompt says, as for blocked: tell the person and stop.
- `cmd/dude`: `dude task list --archived | --closed`.

### Start paths: one rule

*Decided:* `RefuseBlockedTx` becomes **`delivery.RefuseStartTx(ctx, tx, taskID) error`**, the
one guard every start path already calls (through `GuardStartTx` or directly). In order, after
locking the task row, three steps:

1. **Archived** → `ArchivedError` ("CP-29 is archived: unarchive it to start it."). Not
   overridable. An archived task has always ended, so only the recovery paths (6, 7) can reach
   it; the check costs nothing elsewhere.
2. **Closed** → `ClosedError` ("CP-34 was closed as Can't reproduce: reopen it to start it.").
   Not overridable. A closed task is `aborted`, and `stoppedTask` (`api/recover.go:141`) treats
   every `aborted` task with a workflow as one to pick back up; without this step, Resume would
   restart a task a person closed, past Reopen. See *Decisions*.
3. **Blocked** → `BlockedError`, exactly as today, overridable by a person.

`RefuseBlockedTx` stays as a wrapper while callers move; the API maps `ArchivedError` to 409
`archived` and `ClosedError` to 409 `closed`, recorded as `task.start_refused {path, reason}`.
The backend's own gate (path 8) calls a SQL function `task_start_refusal(task)` (returns
`archived` | `closed` | `blocked` | null) beside `task_blocked_by`, so both processes keep one
rule.

The table is the relations doc's, with the current lines (they have moved since) and two paths
it didn't list:

| # | path | where it starts work | lifecycle guard |
|---|---|---|---|
| 1 | **Deliver**, Create and deliver | `deliver` `orchestrator/internal/api/api.go:455` → `delivery.StartTx` `api.go:484` → `GuardStartTx` `delivery/start.go:109` | `RefuseStartTx` in `GuardStartTx`: Archived and Closed refused even with `override`. "Create and deliver" makes the task and says it was not started (`footerProblem`), as for blocked |
| 2 | **Talk it through** | `talk` `api/chat.go:67` → `talkThrough` `chat.go:300` → `StartTx` `chat.go:301` | same, through `StartTx` |
| 3 | **A first message in an unstarted task's Chat** | `chat` `chat.go:30` → `decideFor` → `talkThrough` `chat.go:277` | same; the composer keeps the words |
| 4 | **The conductor's first `start_phase implement` / `decide` at `PointStart`** | `ConductStartPhase` `delivery/conduct.go:193` → `refuseBlockedStart` `conduct.go:211`; `ConductDecide` `conduct.go:371` → `conduct.go:386` | `refuseBlockedStart` calls `RefuseStartTx`; an Archived or Closed refusal is a `Refusal` whatever `st.Overridden` says |
| 5 | **The workflow's `implement` step** | `implementRun` `delivery/workflow.go:470` → `parkIfBlocked` `workflow.go:478` (`delivery/relations.go:144`) | unchanged: parks as for blocked, only before the first implementer of the attempt. Archived and Closed can't reach it: Close and Archive are refused while agents run |
| 6 | **Start over** | `recoverTask` `api/recover.go:206` → `GuardStartTx` `recover.go:252` → `startOver` `recover.go:353` | `RefuseStartTx`; Start over *anyway* passes neither Archived nor Closed |
| 7 | **Resume / Try again** | `recoverTask` → `pickUp` `api/recover.go:286` (`recover.go:322`) | **newly guarded for Archived and Closed only** (`RefuseStartTx` with blocked skipped). A closed task that once ran is `aborted` with a workflow, so `stoppedTask` offers it; it is refused ("CP-34 was closed: reopen it to pick it back up"), and the task page offers Reopen instead of Resume. Blocked stays unguarded here, as the relations doc decided |
| 8 | **`POST /v1/tasks/:id/runs`** | `createRun` `apps/control-plane/src/api/routes/work.ts:365`, blockers at `work.ts:382` | `task_start_refusal(task)` before `task_blocked_by`, same 409s |
| 9 | **Filing a proposal** | `fileProposal` `api/sessions.go:778` → `CreateTaskTx` `sessions.go:968` (`delivery/filing.go:57`) | starts nothing |
| 10 | **`create_task`** | `createTask` `agenttools/tools.go:283` | starts nothing |
| 11 | **Start when unblocked** | `AutoStarts.Sweep` `delivery/unblock.go:227` → `RefuseBlockedTx` `unblock.go:387` → `StartTx` `unblock.go:425` | `RefuseStartTx` at fire time. Close (and so Close and archive) ends the task's request in its transaction (`EndAutoStartTx` `unblock.go:180`, why `"closed"`, a new reason beside `"owner"`, `"started"`, …): a closed task holds none |
| 12 | **A first feedback batch on a never-delivered task** *(not in the relations doc)* | `startForFeedbackTx` `delivery/feedback.go:528` → `StartTx` `feedback.go:533` | through `StartTx`: a never-delivered task closed, or closed and archived, refuses the batch's start |

Confirming a plan (`awaiting_confirmation`) still has no route; any future one calls the guard.

### Data, API, events

Routes (control plane, forwarded to the orchestrator like `done`, so the guard and the status
write stay in one process):

```
POST /v1/tasks/:id/close      {resolution: wont_do|duplicate|cant_reproduce, duplicateOf?, note?, removeRelations?}   owner | admin
POST /v1/tasks/:id/reopen     {}                              owner | admin   → 409 archived when archived
POST /v1/tasks/:id/archive    {resolution?, duplicateOf?, note?, removeRelations?}                                  owner | admin
POST /v1/tasks/:id/unarchive  {}                              owner | admin
POST /v1/epics/:id/archive    {withEndedTasks?: true}   → 409 {code: "unfinished", tasks:[…]} without it
POST /v1/epics/:id/unarchive
```

**One archive route, the resolution optional.** *Decided.* On an ended task `archive` takes no
body and archives. On a task that hasn't ended it requires `resolution` (with `duplicateOf` for
a duplicate) and runs Close's own code (`closeTx`, the same validation, refusals and handover)
then archives, in one transaction; without a resolution it is refused 409 `not_ended`
("CP-44 hasn't ended: archiving it needs a resolution"). *Rejected:* the client calling
`close` then `archive`. Two requests are two transactions: a failure or a dropped connection
between them leaves a task closed but not archived, which the person never asked for, and two
ledger batches whose Activity can't honestly be the single "closed … and archived it" line. The
server already owns the close rules; one route keeps "closed and archived" one fact, refused or
written whole. `close` stays its own route for Close.

The shape is brainstorm sessions' (`POST /v1/brainstorms/:id/archive|unarchive`,
`routes/sessions.ts:136`) and memories' (`/archive`), not a `PATCH {archived}`: each is a
decision with its own refusal and its own ledger event. Refusals are 409 with a `code`
(`running`, `review`, `not_ended`, `kind`, `loop`, `unfinished`, `archived`) and a sentence;
`running` and `review` carry the menu's words ("Stop its agents first", "It has a pull request
open").

Events (ledger; `packages/domain/src/events/types.ts` and the Go constants):

| Event | Payload | Activity line |
|---|---|---|
| `task.closed` | `{resolution, duplicateOf?, note?, handedOver?: [keys], removedRelations?: [keys], andArchived?: true}` | **Ana** closed **CP-63** as a duplicate of **CP-62** — **CP-47**, which waited on CP-63, now waits on CP-62 |
| `task.closed` + `task.archived` (one batch) | `task.archived {viaClose: true}` | **Ana** closed **CP-44** as Won't do and archived it (one line for the batch; the note under it) |
| `task.reopened` | `{was: resolution}` | **Ana** reopened **CP-59** (was Can't reproduce) |
| `task.archived` / `task.unarchived` | `{viaEpic?: id, viaClose?: true}` | **Márcio** archived **CP-34** / **Ana** unarchived **CP-29** |
| `epic.archived` / `epic.unarchived` | as the task's, plus `{tasks: n}` | **Ana** archived the epic **Webhook reliability** and its 6 tasks |
| `task.start_refused` | gains `reason: "archived" \| "closed"` | never a line (as today) |

`task.status_changed` still records the status move Close and Reopen make, with reason
"closed as won't do by Ana". Activity folds a `task.closed` with `andArchived` and the
`task.archived {viaClose}` of the same batch into the one line; each alone keeps its own.

## Epic 2 — Classification

### Kind

*Decided:* `bug`, `feature`, `chore`, `spike`, default `feature`; a small mark on cards, rows,
the header and the proposal card; a workflow difference, specified per kind.

*Correction to management.md:* its pencilled `kind: "change" | "document"` (§ *Task kinds*) is
superseded. A document is what a spike produces; "code change" is every other kind.

| Kind | Brief (implementer) | Review gate | Proposal card | Ends |
|---|---|---|---|---|
| **Bug** | `kindInstructions["bug"]` after the role's lead (`builtinInstructions["implementer"]`, `delivery/prompts.go:539`): reproduce with the steps, commit a failing test named for the bug with its key in a comment ("Failing test for CP-62"), then fix in a second commit; can't reproduce → stop and say so. `PromptInput.task()` (`prompts.go:164`) appends the bug report after the goal. The investigator and the conductor get the bug report too (the conductor's briefing quotes the task: `ConductorPrompt`, `prompts.go:644`) | `reviewFocus["tests"]` (`prompts.go:198`) gains `bugGate`: find the commit adding the reproducing test before the fix; run it there (must fail) and at the head (must pass); missing, passing before, or unrelated to the steps → **blocking** finding "No failing test for CP-62", category `tests`. *Decided* (Q4): when `tests` is not among the project's required reviewers (`requiredReviewers`), the same `bugGate` paragraph is added to `reviewFocus["correctness"]`, so a bug is never delivered without its failing test. No project setting turns it off | "Bug" tag, P-mark, the steps as its detail | as any change: merged |
| **Feature** | unchanged | unchanged | "Feature" tag (no glyph on cards and rows: the default says nothing) | merged |
| **Chore** | unchanged | unchanged | "Chore" tag | merged |
| **Spike** | `kindInstructions["spike"]`: answer the question in a write-up published as an artifact (`answer.md`, the existing artifact publishing, `phases/artifacts.go`); commit no code to the task branch | review runs as configured; with no diff, there is nothing to review and no pull request opens: `openPullRequest` opens none and the task waits in review, "ready to read" (`awaitRead`, `delivery/workflow.go:1081`) | "Spike" tag | its owner's **Mark done** (`markDone`, `api.go:1153`), now writing `resolution = nothing_to_merge` |

*Decided:* a spike runs the same workflow, not a second one. The path a spike needs ("nothing
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
  caller's own bug only; investigator or conductor). How it finds a candidate, *decided*:
  dude knows the order merges landed on the default branch (`pull_requests.merged_at` and the
  merge commit). The agent bisects **by merge order**: runs the reproduction at the merge before
  and after candidates, narrowing to one merge whose commit flips it from passing to failing.
  The tool refuses a proposal without evidence naming two commits. That is the chip's "Why"
  ("passes at a41c9e2 … fails at 7d03b11, its merge").
- **Shown as "Likely CP-30 · merged 2 Oct"**, a dashed chip (`IntroducedByChip
  state="proposed"`): **Confirm** / **Clear** for the owner; for anyone else, whose call it is.
  Confirmed, the chip is solid and says who confirmed it. The owner can set it by hand
  (`PATCH … {introducedBy}`), which is `confirmed` at once.
- *Decided:* **no automatic proposal.** dude could guess from file overlap with recent merges,
  but a guess that looks like a fact is worse than none; a candidate needs a reproduction run.

### Priority

*Decided:* `P0`–`P3`, default `P2`; tasks sort by priority within their lane, then age; no
position on tasks.

- **Shown quietly**, *decided*: **P0** with an up arrow and **P1** in strong ink, semibold,
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
| `brief-bug` | the implementer's brief and the tests reviewer's gate, feature beside bug (with where the gate goes when `tests` isn't required: `correctness`), and a spike's |

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
  &archived=exclude|only|include     default exclude
  &needs=you                         an agent of yours asking, or a plan waiting for you
  &q=<words>                         key (exact, "CP-41" or "41"), title and goal
  &sort=priority|created|updated     default priority (then created desc)
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
  CREATE INDEX tasks_archived_idx  ON tasks (project_id, archived_at DESC) WHERE archived_at IS NOT NULL;
  CREATE INDEX tasks_search_idx    ON tasks USING gin (search);
  ```
  Owner is `task_people` at position 0 (`056_person_ownership.sql`); `owner=` joins it on its
  existing primary key. Status filters use the existing `work_items_project_status_idx`.
- **`list_tasks`** gains the same filters as parameters (`kind`, `priority`, `owner: "me"|name`,
  `status`, `archived`, `epic`, `q` replacing `text`, which stays as an alias), returns
  `kind`, `priority`, `resolution` per task, and pages at 100 with `cursor`. Its query
  becomes the backend's (one SQL function `task_list(...)` both call), so an agent and a person
  never see a different list.
- **`cmd/dude task list`** (`orchestrator/cmd/dude/main.go:136`): `--kind bug --priority P0,P1
  --mine --status open --archived --epic "Billing v2" --q csv`, and `dude task list
  "kind:bug p0 csv"`, which reads the same tokens as Find work.

### The tasks list

*Decided:* a list per project, dense rows, filter bar, fixed views: Mine, Bugs, Needs you,
Archived, Closed.

- **Route** *(decided)*: `#/project/:id/tasks?view=bugs` beside management.md's
  `#/project/:id` (the board). The board's header gains a `Segmented` **Board · List**, the same
  control in the same place on both, so one click switches with the project kept. The tree's
  project row opens whichever was last used. Filters are in the hash (`&kind=bug&priority=P0`),
  so a filtered list is a link.
- **Views** *(decided, in this order)*: **Open** (the first view: not ended, not archived),
  **Mine**, **Bugs**, **Needs you**, **Closed**, **Archived**, as one
  `Segmented` with counts. They are fixed: everyone's Bugs is the same, and there is nothing to
  name, save or share. *Decided:* **Open** is added and opens first; without it the list's first
  screen would be one of the narrow views.
- **Filter bar**: Search (key, title, goal), then chips: Kind, Priority, Owner, Epic,
  Repository (`TaskFilterBar`). A chip is a `RowMenu` of radio items; set, it says its value and
  has its own ✕. A view sets its own filter (Bugs is `kind=bug`); the chip shows it set, and
  clearing it leaves the view (to Open), so the two can't disagree.
- **Rows** (`TaskRow`): status glyph and key, kind glyph (none for a feature), title, then
  resolution / Archived marks, epic, priority (none for P2), owner's face, age, and the
  row's `…` with the lifecycle actions. 32px, compact 28px. Under 720px of its own width the
  epic, priority and age drop, as `Table`'s `hideWhenNarrow` does.
- **Paging**: the next 50 load as the list nears its end; the foot says how many are shown and
  the order. No page numbers: keyset paging has none.

### Find work

*Decided:* one input; `kind:bug p0 owner:ana #CP-41` tokens plus words; archived results marked.

- **Tokens** *(decided spelling)*: `kind:bug|feature|chore|spike`, `p0`…`p3`, `owner:<first
  name>` and `is:mine`, `#CP-41` (or a bare `CP-41`), `is:archived`, `is:closed`, `is:open`,
  `is:blocked`, `epic:"Billing v2"`. Everything else is words. Tokens are case-insensitive; an
  unknown `x:y` is a word.
- **One input.** The field stays the Sidebar's search; what it understood shows **under** it, as
  chips ("kind:bug owner:ana" then “billing”) with a sentence ("Bugs · owned by Ana · with
  “billing”: the epic Billing v2, then its 4 bugs"), never as editable pills inside it. Backspace
  edits text, as in any field.
- **Results**: with any token, or any word that isn't in the loaded tree, Find work asks the
  server (`GET /v1/tasks?…&archived=include`, the tokens mapped one to one) and shows results
  **in place of the tree**: matches first, then, when the words narrowed it, the same query
  without them, so "where else is it" is one glance. Closed and archived results come last, in
  secondary ink, marked (`ResolutionMark`, `ArchivedMark`). With words only, the tree filters as
  today (that is instant and stays).
- **Results use the tree's row grammar.** An epic whose title matches the words is a result too,
  first, and **carries the layers glyph and its total**, as its tree row does after the fix
  (*`NavTree` epic rows (fix)*); a task result leads with its status mark and mono key and never
  carries the glyph.
- **Keys**: ↓ from the field enters the results, Enter opens, Esc clears (today's grammar).

### Board filters

*Decided:* a chip row (kind, priority, mine) beside the existing relations filter.

- In the board header, after **All · Can start · Blocked** (the existing `Segmented`): **Kind**,
  **Priority** (`TaskFilterBar` chips) and **Mine** (a pressed toggle, as the sidebar's
  Everyone · Mine). The relations filter's counts follow the chips: "All 4 · Can start 1 ·
  Blocked 0" with Kind: Bug and Mine set.
- Persisted per project in the hash (`#/project/cp?kind=bug&mine=1`), like the list's.

### Finding screens

| Screen | What it shows |
|---|---|
| `tasks-list` | the Open view: segmented views with counts, search, chips, rows |
| `tasks-list-view-bugs` | the Bugs view: P0 first |
| `tasks-list-archived` | the Archived view |
| `find-work-tokens` | `kind:bug owner:ana billing`: what it understood, the epic Billing v2 with its layers glyph, its bugs (the duplicate CP-63 last, marked), then Ana's other bugs, the closed and archived ones last and marked |
| `board-filters` | the board with Kind: Bug and Mine set |
| `sidebar-epics` | the fixed sidebar (Billing v2 open, Webhook reliability folded with its needs-you count, Usage alerts folded), beside the tree as it is today and with the fix; also compact, and in the narrow drawer |
| `sidebar-epic-variants` | what was considered: (a) glyph only, chosen, beside (b) glyph and roll-up word, rejected |

## Field × surface

✓ shown or set there; R read only; — not there, deliberately; *(why)* where it isn't obvious.

| Surface | kind | priority | bug report | introduced by | resolution | archived |
|---|---|---|---|---|---|---|
| Task dialog, new | ✓ aside | ✓ aside | ✓ (bug) | ✓ aside | — | — |
| Task dialog, edit | ✓ until delivery starts | ✓ | ✓ | ✓ | — | — |
| Task page header | ✓ word | ✓ always | — | — | ✓ beside status | ✓ mark |
| Task page notices | — | — | — | — | ✓ Callout + Reopen (archived: "Unarchive it first to reopen it") | ✓ Callout + Unarchive |
| Task page Overview / rail | ✓ rail, what it changes | ✓ rail | ✓ block | ✓ chip, Confirm/Clear | ✓ rail | — |
| Task `…` menu (header, tree, row) | — | — | — | — | ✓ Close… / Reopen, each `disabledReason` as in *UX* | ✓ Archive… (unended: opens Close and archive) / Unarchive |
| Close dialog | — | — | — | — | ✓ set; as Close and archive, set with the archive | ✓ set, as Close and archive |
| Board card | ✓ glyph (not feature) | ✓ P0 P1 P3 | — | — | ✓ marks line | — *(not on the board)* |
| Board header | ✓ chip | ✓ chip | — | — | — | ✓ "3 archived" |
| Sidebar tree row | — *(28px row: key and status suffice; an epic row's one glyph is `layers`, see the NavTree fix)* | — | — | — | — | — *(not in the tree)* |
| Find work results | ✓ glyph | ✓ | — | — | ✓ marked, last | ✓ marked, last |
| List row | ✓ glyph | ✓ | — | — | ✓ | ✓ mark |
| List filter / view | ✓ chip, Bugs | ✓ chip | — | — | ✓ Closed | ✓ Archived |
| Proposal card | ✓ tag + glyph | ✓ tag | ✓ detail | — | — | — |
| `create_task` | ✓ | ✓ | ✓ | — | — | — |
| `update_task` | ✓ own, before start | ✓ own | ✓ own | ✓ propose, own bug | — | — |
| `list_tasks` | ✓ filter + field | ✓ | — | ✓ field | ✓ field | ✓ filter |
| `propose` | ✓ | ✓ | ✓ | — | — | — |
| `cmd/dude` | ✓ | ✓ | ✓ `--steps` … | — | ✓ `--closed` | ✓ `--archived` |
| `GET /v1/tasks` | ✓ | ✓ | — | — | ✓ | ✓ |
| `/v1/navigation` (`NavTask`) | ✓ | ✓ | — | — | ✓ | — *(archived not sent)* |
| Brief / review | ✓ | — *(priority isn't a delivery input)* | ✓ | — | — | — |
| Ledger | `task.kind_changed` | `task.priority_changed` | `task.updated` | `task.introduced_by_proposed` / `_confirmed` / `_cleared` | `task.closed` / `task.reopened` | `task.archived` / `task.unarchived` |
| Activity line | “changed CP-61 from Feature to Bug” | “…and its priority from P2 to P1” | — *(the text's edit line)* | “proposed that CP-30 introduced CP-60” / “confirmed …” | ✓; with an archive in the same batch, one line “closed CP-44 as Won't do and archived it” | ✓ both ways |

The sidebar tells an epic from a task by glyph alone: an epic row and an epic result in Find
work carry `layers` and a total; a task row and a task result carry a status mark and a mono key.

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

### `ResolutionMark`, `ArchivedMark` *(new)*

`ResolutionMark {resolution, of?: key}` is a neutral `Badge size="sm"` ("Duplicate of CP-62").
`ArchivedMark` is the `archive` glyph and "Archived" in muted ink. Gallery `#lifecycle-marks`.

> **Closed, archived.** Neutral, muted, never a tone: neither needs anyone. A closed task's
> resolution sits beside its status, which still says what delivery did.

### `Board` *(changed)*

- `cardLead?: (card) => ReactNode` — after the key on a card's first line; defaults to
  `KindMark quietDefault` and `PriorityMark` from `NavTask.kind` / `.priority`.
- `chips?: ReactNode` beside the relations filter (`TaskFilterBar`).
- `boardModel.ts`: within a lane, triage rank, then `PriorityRank`, then the caller's order.
- `cardMarks` default: a closed task's `ResolutionMark`; `DoingLine` says "closed" for one with a
  resolution instead of "stopped".

README, *Board*, new bullet:

> **Archived tasks are not on the board**; the header's quiet "3 archived" opens the list's
> Archived view. The chips filter the cards and the relations filter's counts follow them.

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
sibling. Gallery `#task-row`: open, closed (each resolution), archived, narrow.

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
search and `/` are unchanged. Results use the tree's row grammar: an epic result carries the
`layers` glyph and its total, a task result its status mark and key.

### `NavTree` epic rows (fix)

- **The rule, as written.** README *Navigation* (`packages/design-system/README.md:955`):
  "projects are sticky small-caps headers, **epics carry the layers glyph and a total**, tasks
  lead with a status dot and a mono key". `NavTree.tsx`'s header comment (line 76) says the same,
  and so does the gallery's `#nav-tree` note (`gallery/sections/Navigation.tsx:51`).
- **Where it is honoured.** `Breadcrumb` draws `layers` for an epic (`icon`, 14px, `.glyph` in
  `--ds-color-text-muted`), and the board's epic lane header draws it after its chevron
  (`Board.tsx:431`, 14px, `.laneGlyph`, the same muted ink; README:1056).
- **Where it isn't.** `NavTreeRow`'s epic branch (`NavTree.tsx:385-394`) renders chevron, title,
  needs-you count, total: no glyph. An epic row reads as a task row without a key. This is a
  regression against the stated rule, not a new rule.
- **The change.** One element in the epic branch, between `{chevron}` and the title:
  `<Icon name="layers" size={14} className={styles["epicGlyph"]} />`, and one CSS rule,
  `.epicGlyph { flex-shrink: 0; width: 14px; color: var(--ds-color-text-muted); }`, with the
  row's hover and selected states lifting it to `--ds-color-text-secondary` as `.epicCount`
  already does (muted falls under 4.5:1 on the washes in light). It leads the row, where
  Breadcrumb and the lane header put the same glyph; its 14px matches the `.mark` cell a task's
  status dot uses. No hue, no new token, no new icon (`layers` is in `icons/index.tsx:208`).
- **One discrepancy to know.** The README says tasks "lead with a status dot", but `NavTreeRow`
  draws a task's dot in the `.mark` cell at the row's right edge, after the title. The epic glyph
  leads anyway: at the right it would sit between the needs-you count and the total and read as
  a state, and leading is where every other surface draws it. The README's task wording is the
  pre-existing inaccuracy; it is reported, not changed here.
- **README.** The rule's line needs no change. Add, after it: "The epic's glyph leads its row,
  after the chevron, as in `Breadcrumb` and the board's lane header; a task row never draws it."
- **Gallery.** `#nav-tree` already shows epics; with the fix they carry the glyph. Add
  `#nav-tree-epics`: an epic open and one folded with a needs-you count, selected and hovered,
  comfortable and compact, light and dark, and the ≤220px container width where the people drop.
- **Test.** `test/components.test.tsx` renders `NavTreeRow` for an epic row and for a task row
  and asserts on the DOM: the epic row holds exactly one `svg[data-icon="layers"]`, the task row
  none. The mockup's shooter makes the same check on every screen's tree.
- **Glyph only.** *Decided* (Q5): (a), the rule as written; the glyph alone tells the levels
  apart (screen `sidebar-epic-variants`, kept as what was considered).
  Rejected: (b), a roll-up word on every epic row, which repeats "Active" and crowds the needs-you count.

### `NavTask` *(changed)*

`kind`, `priority`, `resolution?`. Archived tasks are not sent.

## Migration sketch

Written before 132–138 were taken. Epic 1 shipped its part as `139` and `140` (see *Epic 1 as
built*); Epic 2 and 3's columns take the next free numbers.

```sql
-- 132_tracking_work.sql — Close, Archive; kind, priority, a bug's report;
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
  ADD CONSTRAINT tasks_resolution_status CHECK (resolution IS NULL OR status IN ('done', 'aborted')),
  ADD CONSTRAINT tasks_archived_ended    CHECK (archived_at IS NULL OR status = 'done' OR resolution IS NOT NULL),
  ADD CONSTRAINT tasks_bug_fields CHECK (kind = 'bug' OR (bug_steps IS NULL AND introduced_by_task_id IS NULL)),
  ADD CONSTRAINT tasks_bug_lengths CHECK (length(bug_steps) <= 8192 AND length(bug_expected) <= 2000
                                          AND length(bug_actual) <= 2000 AND length(bug_environment) <= 120);

ALTER TABLE epics
  ADD COLUMN archived_at timestamptz, ADD COLUMN archived_by text;

-- Today's "done, nothing merged" gets its name.
UPDATE tasks t SET resolution = 'nothing_to_merge'
 WHERE t.status = 'done'
   AND NOT EXISTS (SELECT 1 FROM pull_requests pr WHERE pr.task_id = t.id AND pr.state = 'merged');

-- Indexes as in "GET /v1/tasks" above, plus:
CREATE INDEX tasks_introduced_by_idx ON tasks (introduced_by_task_id) WHERE introduced_by_task_id IS NOT NULL;

-- task_blocked_by follows a duplicate's blocking to its canonical task (one hop);
-- task_start_refusal(task) → 'archived' | 'closed' | 'blocked' | NULL, both processes' one rule;
-- task_relation_would_cycle walks 'duplicates' as 'blocks'.
-- (CREATE OR REPLACE of the first and third, as in 111.)
```

- **Lock cost.** `ADD COLUMN … DEFAULT` with a constant is metadata-only; the `GENERATED … STORED`
  column rewrites `tasks` once, so the migration takes `tasks`' `ACCESS EXCLUSIVE` lock for that
  rewrite. dude's `tasks` is small (thousands of rows per organisation); if it is not, the
  `search` column moves to a follow-up migration filled in batches by a trigger.
- **`ALTER TYPE … ADD VALUE`** can't run in the same transaction as a use of the new value: the
  functions that use `duplicates` go in `133`.
- Row-level security: unchanged (the columns sit on tables that have it). No new worker.

## Decisions

Márcio's answers, 2026-10-17. Numbered as the questions were; Q3 left with Later (see *Not here*).

- **Q1. Does a Won't do blocker keep blocking?** Yes. The close dialog says what waits on the
  task and offers **Remove the relation** in the same step. (2026-10-17)
- **Q2. Archive only ended tasks, or anything not running?** Anything not running and without a
  pull request open; a task that hasn't ended is closed on the way, in one dialog and one
  transaction (**Close and archive**). One route, `POST /v1/tasks/:id/archive` with an optional
  resolution. Unarchive and Reopen stay two steps. Epics unchanged. (2026-10-17)
- **Q4. Where does the bug gate go when `tests` isn't a required reviewer?** Into
  `reviewFocus["correctness"]`. No project switch. (2026-10-17)
- **Q5. The sidebar's epic row: glyph only, or glyph and roll-up word?** Glyph only (a).
  (2026-10-17)
- **Resume on a closed task.** Refused. `RefuseStartTx` refuses a closed task (`ClosedError`,
  409 `closed`) on every path, recovery included; the task page hides the recovery bar and
  offers Reopen (*Start paths*, step 2). *Rejected:* Resume implying Reopen, which would let a
  click on the recovery bar undo a person's Close without saying so. (2026-10-17)

## Open

None for Epic 1.

## Epic 1 as built

Where the build differs from, or settles, what the sections above say:

- **Migrations** are `139_task_lifecycle.sql` (types, columns, constraints, the
  `nothing_to_merge` backfill) and `140_task_lifecycle_rules.sql` (`task_blocked_by`,
  `task_start_refusal`, `task_relation_would_cycle`), not one `132`: other migrations took
  132–138, and `ADD VALUE 'duplicates'` can't be used in the transaction that adds it. Epic 1
  adds only its own columns; kind, priority, a bug's report and `search` are Epic 2 and 3's.
- **Closed** means `resolution IS NOT NULL AND resolution <> 'nothing_to_merge'`. **Ended**
  means `done`, or `aborted` with a resolution.
- **Close writes** `status = 'aborted'`, `closed_by`, and a `task.status_changed` with the reason
  "closed as won't do by Ana". Close is refused `running` while the task is `running` or
  `awaiting_input` or a phase agent's run is live, and `review` while it is `review` or
  `ready_to_merge`. Close on a parked live delivery completes its workflow.
- **The loop check** walks handed-over edges (the canonical task → each task the duplicate
  blocks), not `duplicates` read as `blocks`.
- **Release on merge.** When the canonical task merges, the tasks its duplicates blocked are
  released as its own dependants are (`delivery/unblock.go`).
- **Reopen** returns the task to `received`, deletes its `duplicates` edge, and is refused
  `archived` while archived. Its next start is a fresh attempt (a new delivery idempotency key,
  `delivery:<id>:attempt-N`), whether through Deliver or Talk it through.
- **Path 3** is refused by `RefuseLifecycleTx` in `decideFor`, before a chat turn is decided.
  **Path 5** stays blocked-only. **Path 7** answers "CP-34 was closed: reopen it to pick it back
  up."; `GET …/recovery` returns no actions and `refused: "closed" | "archived"`. **Path 11**
  ends the request with why `closed` or `archived` and records `task.start_refused` with path
  `auto_start`. **Path 12**: a feedback submit on a closed or archived task is refused 409 with
  the rule's sentence and "Your drafts are kept."
- **Epics.** Archiving an epic needs an admin or the owner of every task it archives.
  `epic.archived` / `epic.unarchived` carry the project and `{epicId, title, tasks}`.
  Unarchiving a task is refused `archived` while its epic is archived; unarchiving the epic
  restores the tasks archived with it (those whose `archived_at` is the epic's).
- **`GET /v1/tasks`** leaves archived tasks out unless `archived=1`, which returns only them;
  the board's "N archived" hint opens that list on the project page.
- **CLI.** `dude task close|reopen|archive|unarchive` exist and refuse locally: they are a
  person's decisions, not an agent's.
- **`cant_reproduce`** is accepted on every task until Epic 2 adds kind.

## Not here

- **Later** (putting a task or an epic off until a date): deferred, not built now; see
  *Later (deferred)* below for the design as it stood.
- **Intake** (external bug reports, GitHub issues becoming tasks): deferred; when it comes, an
  issue becomes a task with kind and bug report filled, and these fields are its target.
- **Planning** (milestones, due dates, roadmap): deferred.
- **Labels**: not planned (see *No labels*).
- **Custom fields**: not planned; a new dimension is a fixed-value field decided for everyone.
- **Subtasks**: not planned; an epic is the group, and `blocks` is the order.
- **Bulk edit** (close, archive, re-prioritise many rows at once): not now; the list's rows are
  built to take a checkbox column when it comes.
- **User-defined saved views**: not planned; the views are fixed (decided).
- **Hard delete of a task**: not planned. Close is the way out; an org admin's delete stays the
  database's.

## Later (deferred)

**Not built now.** Kept so it can be picked up without redoing the thinking; nothing above
depends on it, and adding it changes no column or route above.

- **Shelf, not a status or a lane.** `deferred_at timestamptz` (set = on Later), `deferred_until
  date` (`NULL` = no date), `deferred_by text`, on `tasks` and `epics`: `deferred_until` alone
  can't tell "not on Later" from "no date". The day starts at 00:00 UTC.
- **Who and what.** Only a task not started (`delivery.NotStarted`); owner or admin; an agent only
  its own task, with a reason. An epic only when none of its tasks runs; its tasks inherit the
  refusal through `COALESCE(task.deferred_at, epic.deferred_at)`.
- **The refusal.** `RefuseStartTx` gains a step between Archived and Blocked: `LaterError` ("CP-62
  is on Later until 3 Nov: bring it back to start it."), **not overridable**; 409 `later`;
  `task_start_refusal()` returns `'later'`. Path 5 parks on a `later.returned` signal; path 11
  keeps the auto-start request set, not due; path 12's batch waits.
- **The worker.** `delivery.LaterReturns.Sweep`, beside `AutoStarts.Sweep`: each minute, rows with
  `deferred_until <= current_date`, `FOR UPDATE SKIP LOCKED`, one transaction each; clears the
  shelf, appends `task.came_back` / `epic.came_back`, marks a waiting auto-start due; starts
  nothing itself. `tasks_later_idx` / `epics_later_idx` on `(deferred_until) WHERE deferred_at IS NOT NULL`.
- **Bring it back.** `POST /v1/tasks/:id/back` and `/v1/epics/:id/back`, the escape from the
  refusal; put on with `POST …/later {until?, why?}` (`task.deferred`, `epic.deferred`).
- **UX.** A folded shelf under the board header, outside the lanes and every count, an epic on it
  as one row ("Usage alerts · 2 tasks · 1 Dec"); `LaterMark`, `LaterGate` (no Start anyway),
  `StartMenu later`, a tree row, a list view, `is:later`. Round 1's mockup (`d05f7965`) has it all.
- **Open then:** a shelf beside the board, or a board filter that shows Later cards in Backlog.
