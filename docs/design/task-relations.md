# Task relations: blocks, related

**Status: built, 2026-10-12.** Asked for by Márcio after a session with
Jonas; round 2 applied his answers to the first round's open questions, and
the build settled the three left open (see *Decided*). Migration 111 holds
the table and functions; the routes, agent tools, CLI flags, components and
screens below exist. Where the build departs from this text, *As built*
says so. Mockup: `docs/design/mockups/relations/index.html` (it opens
from disk and you can click through it; the source is in `apps/web/mockups/relations`, built with
`MOCKUP=relations bunx vite build --config mockups/vite.config.ts` from
`apps/web`). Screens are in `docs/design/mockups/relations/screens/`. The bar
above the app switches screen, theme and density, and is not part of the
proposal. Each screen also has its own address (`?s=board&theme=light&density=compact`).

## The problem

Tasks wait on each other, and dude does not know it. A billing page (CP-44)
needs the hourly meter (CP-41) and the Stripe sync (CP-43) to be merged
first. Today a person keeps that order in their head, and nothing stops
anyone (a person, a conductor, a brainstorm filing work) from starting
CP-44 early. If it starts early, its agents build on a `main` that lacks
what it needs, and the result is a wasted run or a branch built on guesses.

The goal is to say once, where agents can read and write it, that one task
**blocks** another, and to have every start path refuse a blocked start
for the same reason. The board, the task page, the brainstorm's proposal
card and the epic should show that order.

## Model

### Kinds

| kind | stored as | shown on A | shown on B | gates a start |
|---|---|---|---|---|
| `blocks` | one directed edge A → B | Blocks B | Blocked by A | yes: B can't start until A is merged |
| `related` | one edge, stored once with the lower id as `from` | Related B | Related A | never |
| `duplicates` *(later)* | A → B | Duplicates B | Duplicated by A | not mocked; it would close A |

**"Depends on" is not stored.** It is `blocks` read from B's side. The
tools accept `blockedBy` and write the reverse `blocks` edge, so there is
exactly one row per fact.

### Wording: "Blocked by" / "Blocks"

The UI uses one verb pair, **Blocked by** / **Blocks**, everywhere: the
board chip, the task's Relations groups, the Start tooltip, the proposal
card, the history lines and the agents' refusals. "Depends on" was
rejected for the UI for three reasons:

- It describes the *dependant's need*, while what the board and the start
  must say is the *state that stops the start*. "Blocked by CP-41" answers
  "why can't I start this?" without a second clause, and "Depends on CP-41"
  invites a further question ("…and is that merged?").
- "Blocks 3" on the other end reads naturally. Its counterpart, "Is depended on by 3", does not.
- It keeps "blocked" a word about **work**, never about a person. The
  needs-you vocabulary is "Needs you", "waiting on", "asks", which keeps
  the two from blurring.

### Resolved: merged, and only merged

*Decided (Márcio, round 2).* A blocker is **resolved when its status is
`done` and it has a pull request in state `merged`**: the change the
dependant needs is on `main`. Every path to `done` today goes through
`delivery.SetTaskStatusTx` → `RecordStatusTx`, but only one of them is a
merge:

| how a task reaches `done` | where | releases its dependants |
|---|---|---|
| its pull requests merged | `pullRequestEnded`, `delivery/workflow.go:1152` ("pull requests merged") | **yes** |
| an escalation's "done": a person took what was merged, the rest closed | `workflow.go:1273` ("a person took what was merged") | **yes**, if at least one of its pull requests is `merged`; otherwise no |
| **Mark done** on work with nothing to merge (a spike, a write-up) | `markDone`, `orchestrator/internal/api/api.go:918` | **no** |

- **Done with nothing merged keeps blocking, for good.** The task is
  settled for its owner, but nothing it did reached `main`, so a dependant
  started now would branch from a `main` without it. Because `done` is
  final, such a blocker will never release anything, so the page says that
  plainly and names the two ways out (screen *unmerged-blocker*, CP-51
  blocked by the spike CP-37):

  > **Blocked by CP-37 (done, nothing merged).** Only merged work releases
  > what waits on it. CP-37 was marked done by Tom with nothing merged, so
  > it will never release CP-51. Remove the relation if its answer is all
  > this needed, or start this anyway.

  The two ways out are **the owner removes the relation** (the spike's
  answer was all CP-51 needed) or **Start anyway** (any person). For a
  reader who isn't the owner, the second sentence reads "Márcio, the
  owner, can remove the relation; anyone can start this anyway."
  Activity gets the system line "CP-37 was marked done by Tom with nothing
  merged. It still blocks CP-51: only merged work releases what waits on it."
  (screen *history-unmerged*). Start when unblocked is refused on such a task
  (the item is disabled, saying that CP-37 will never merge), because it
  could never fire.
- Wherever such a blocker is named, it is named so: "CP-37 (done, nothing
  merged)" in the reason, "Blocked by CP-37 · not merged" on the chip (its
  green check is left off, so the chip never reads as released), "nothing
  merged" beside its row's Done in Relations, "done, nothing merged" in the
  card's foot and in the epic's counts.
- **`failed` and `aborted` keep blocking.** The work the blocked task
  needed did not land, so starting it anyway would be guessing. The page
  says so (screen *failed-blocker*): "CP-38 failed. It keeps blocking until
  someone picks it back up and it merges, the owner removes the relation, or
  someone starts this anyway." Picking it back up (Resume / Try again /
  Start over) returns it to `running`, and it keeps blocking until it merges.
- **In review and ready to merge still block.** The code is not on `main`
  yet, so an agent started now would branch from a `main` without it.
- A blocker that has merged stays in the Relations list, settled, with its
  status mark showing Done. The board's chip and counts list only
  **open** blockers.

Every piece of wording follows: "it can start once they're merged" (never
"done"), "Can't start until these are merged" in the chip's tooltip, "This
can't start until it is merged" in the relation editor's kind, and the
epic's counts say "merged" and "done, nothing merged" apart.

### Cycles and self

Both are refused at write time, in the same transaction as the insert:

- `from_task = to_task` is a `CHECK`.
- A cycle is refused by deduplicated reachability from `to_task` along
  `blocks` edges, looking for `from_task`, under the organisation's
  advisory relation lock (`pg_advisory_xact_lock(hashtext('task_relations:' || org))`).
  Two concurrent inserts cannot each close half a loop. Only a reachable
  source triggers breadth-first witness reconstruction. The refusal names the loop:
  `CP-40 → CP-41 → CP-44 → CP-40` (screen *cycle*). The person is offered a
  one-click fix, "Make it Related instead" (`Input`'s `errorAction`
  grammar). An agent gets the same words as a tool refusal.
- `related` is never checked for cycles, because it gates nothing.

### Across epics and projects

An edge may join any two tasks of the organisation: across epics, and
across projects (CP-46 is blocked by WEB-12). Row-level security is
per organisation, so an edge can never cross one. A row from another
project shows its project's name (muted) after its title, in the Relations
list and in the chip's tooltip.

*Decided (Márcio, round 2):* the card's chip does **not** name the other
project. It reads "Blocked by ◔ WEB-12" like any other, and the project is
said only in its tooltip (and in the Relations row and the card's
accessible name). A key's prefix already tells the projects apart, and a
project face on the chip would crowd the one line it has.

### Who may add and remove a relation

*Decided (Márcio, round 2).*

- **Adding is open**: any member of the organisation, and any agent whose
  role may write relations (see *Agents*), may add an edge to any task.
  Adding only ever makes a start wait, and it is in the history with who
  and why.
- **Removing is the owner's.** Only the owner of the task whose relation
  it is may remove it. For `blocks` that is the **blocked** task's owner
  (the edge A → B is B's: it is B's start that it holds), whichever page it
  is removed from: on CP-41's page, the "Blocks CP-44" row is Márcio's to
  remove, not Ana's. A `related` edge belongs to both tasks, so either
  owner may remove it; when a task has no owner, any member may remove its
  relation. The backend's `removal` helper computes the same permission for
  DELETE and for `canRemove` in the reader's view.
- **Anyone else sees no remove.** The DS's rule is that a way to act is
  **disabled with its reason, never missing**, when the reader could act
  but not now (README, *Brainstorm sessions*: "the way to act is never
  missing, and never greyed without saying why"; *Do / Don't*: "a greyed
  item that does not say why" is a Don't). When the action is **not the
  reader's to take**, the README's grammar is the other one: a reader
  reads, and is told whose it is in words ("Only Márcio, its owner, can pick
  it back up", *Picking a stopped task back up*; "Only Márcio can file
  this: it's his task", *Brainstorm sessions*), with no control drawn for
  them. Removing someone else's relation is the second case, so the ✕ is
  not drawn, and its place in the row is kept empty so the columns stay
  aligned.
- **The hint.** Hovering or focusing such a row shows, in muted ink at the
  end of its group's head, "Only Ana, the owner, can remove this" (or
  "Only Márcio, CP-44's owner, can remove this" when the edge is the
  other task's). It sits in the head, not the row, so nothing in the row
  moves under the pointer. The same words are the row's
  `aria-describedby`, so a screen reader hears them on the row's
  focus without hovering. Screen *task-other*: Márcio reading Ana's CP-42.
- **Agents remove less than people.** An agent may remove only an edge that
  **an agent of the same task created** (`created_by_run_id` is a Run of
  that task). This is a good rule, and I'd keep Márcio's default: a
  conductor that added "blocked by CP-40" while planning and then learned
  it was wrong must be able to take back its own word without asking, and
  the same rule keeps an agent from deleting a person's edge, or another
  task's agent's, to unblock itself, which is the one abuse removal opens.
  The edge's history line names the agent and its quoted `why`, so the take-back
  is visible.

## Gating: every start path

"Start" here means anything that moves a not-started task (`received`,
`intake`, `awaiting_confirmation`, `queued`: `delivery.NotStarted` in
`orchestrator/internal/delivery/unblock.go`) toward running an agent on it.
Every path found in the code is listed below, with where its guard sits.
**One function owns the rule**: `delivery.RefuseBlockedTx(ctx, tx, taskID)
error`. It locks the task row, reads the task's open blockers through
`task_blocked_by` (every `blocks` edge whose `from` has not merged), and
returns a `BlockedError` (mapped to 409 `blocked` by the API and to a
`Refusal` by path 4) whose words
are the same on every path:

> CP-44 is blocked by CP-41 (in review) and CP-43 (in progress): it can
> start once they're merged.

| # | path | where it starts work today | guard |
|---|---|---|---|
| 1 | **Deliver** button (task header, `StartChoice`), and **Create and deliver** in the new-task dialog | web `TaskScreen.tsx:263`, `TaskDialog.tsx:291` → backend `deliverTask` `apps/control-plane/src/api/routes/work.ts:190` → orchestrator `deliver` `orchestrator/internal/api/api.go:270`, `Workflow.Start` at `api.go:306` | `RefuseBlockedTx` inside the `InOrg` at `api.go:281`, before `NameOnlyRepository`. For "Create and deliver", the create succeeds and the deliver is refused, so the dialog says the task was made but not started (its `footerProblem`) |
| 2 | **Talk it through** (button and `StartChoice`) | web `TaskScreen.tsx:278` → `talkTask` `work.ts:229` → `talk` `chat.go:69` → `converse` → `decideFor` `chat.go:211` → `talkThrough` `chat.go:262`, `Workflow.StartTx` at `chat.go:276` | in `talkThrough`, before `StartTx` |
| 3 | **A first message in an unstarted task's Chat**. It is Talk it through without the button | `chat` `chat.go:31` → `decideFor` → `talkThrough` | the same guard as 2. The message is refused with the reason, and the composer keeps the words (`onSubmit` resolving `false`) |
| 4 | **The conductor's first `start_phase implement`** (or `decide next` at `PointStart`) on a conducted delivery | agent tool `start_phase` `orchestrator/internal/agenttools/conductor.go:279` → `delivery.ConductStartPhase` `orchestrator/internal/delivery/conduct.go:185`; `decide` → `ConductDecide` `conduct.go:336`; carried out by `implementRun` `delivery/workflow.go:398` | in `ConductStartPhase` and `ConductDecide` when the decision point is `PointStart` (`decisions.go:34`). The conductor gets the refusal as a tool error, and its prompt tells it to tell the person and stop. Paths 2 and 3 already refuse to make the delivery, so this guard covers only a relation added *while* the conductor plans (screen *history*: "Ana's conductor added blocked by CP-40") |
| 5 | **The workflow's own `implement` step** (Deliver's first step, and a conducted delivery after its first decision) | `steps.implement` `delivery/workflow.go:380` → `implementRun` `workflow.go:398` → `CreatePhaseRun` | `RefuseBlockedTx` at the top of `implementRun`, **only when no implementer has run on this attempt** (`st.Retries == 0` and no implement Run in `st`). A refusal here doesn't fail the workflow: it parks on a new signal `relations.unblocked` and sets the task back to `queued` with reason "blocked by CP-41". This is the backstop for an edge added between Deliver and the step running |
| 6 | **Start over** (a new attempt) | web `PickUpDialog` → `recoverTask` `work.ts:251` → `recoverTask` `orchestrator/internal/api/recover.go:205` → `startOver` `recover.go:325` | **guarded**: `GuardStartTx` in `recoverTask` before `startOver`, refused with the same 409 and recorded as `task.start_refused {path: "start_over"}`. A person's **Start over anyway** (`override: true`) records `task.start_overridden` and marks the new attempt overridden, so its implement step does not park (see *Start over and Try again*) |
| 7 | **Resume / Try again** | `recoverTask` → `pickUp` `recover.go:294` | **not guarded**: they carry on the same attempt, whose blockers were merged or overridden at its first start (see *Start over and Try again*) |
| 8 | **`POST /v1/tasks/:id/runs`** (a bare Run, status `queued`) | backend `createRun` `work.ts:301`, task set `queued` at `work.ts:326`; used by tests (`tests/helpers.py:145`) and `ApiClient.createRun` `apps/web/src/api/client.ts:641` (no screen calls it) | refused with the same 409 in the backend, in the `withOrg` transaction before the insert, by a SQL function `task_blocked_by(task_id)` that both processes call, so the backend need not reimplement the rule. Raw SQL is needed for this: neither `Bun.sql` nor pgx has a query builder for a recursive CTE here, and the repo writes its SQL raw by convention (CONTRIBUTING, *Decisions*) |
| 9 | **Filing a proposal** | `fileProposal` `orchestrator/internal/api/sessions.go:664` → `fileItem` `sessions.go:785` → `delivery.CreateTaskTx` `delivery/filing.go:57` | filing never starts work ("Nothing starts", `ProposalCard`), so there is nothing to gate. Filing does **write** the proposal's relations (see *Propose work*) |
| 10 | **`create_task`** by an agent | `createTask` `agenttools/tools.go:219`, status `received` (`tools.go:256`) | creates, never starts. It may write relations, and nothing is gated |
| 11 | **Start when unblocked** *(new)*: the owner's standing request, released by the merge of the task's last blocker | the merge (`RecordStatusTx` → `releaseDependantsTx`) only marks the request due (`due_at`); **after it commits**, the auto-start worker (`delivery.AutoStarts.Sweep`, `delivery/unblock.go`) takes each due request `FOR UPDATE SKIP LOCKED`, in a transaction of its own, and starts it through `delivery.StartTx`, the same start as path 1 (Deliver) or path 2 (Talk it through), as dude for the person who set it | `RefuseBlockedTx` again, **at fire time**, re-reading every blocker: an edge added since it was set, or a second blocker still open, means nothing starts and the request stays set, no longer due. `StartTx`'s own guard follows. A start that refuses for a reason of its own ends the request (`task.auto_start_cancelled {why: "failed", error}`) and says so in Chat; a database or connection failure leaves it due for the next sweep, and any other error does too, up to five fires. Never in the merge's transaction: a failed start must not roll a merge back |

Path 11 is the opt-in auto-start added by this build. Confirming a plan
(`awaiting_confirmation`) has no route of its own; any future route must
use the same guard.

### Start over and Try again

*Decided at build.* Try again and Resume stay unguarded;
Start over is guarded, with Start anyway.

- **Try again and Resume continue the same attempt**, on its branch, from
  what it pushed. Its blockers were merged (or overridden) at its first
  start, and the work is already shaped by them: guarding it would refuse
  to finish work that is half done. They stay **unguarded**.
- **Start over is a fresh attempt from `main`** (`startOver`,
  `recover.go:325`, a new branch `dude/<task>/attempt-N`). It is a start in
  every sense that matters to the rule: its agents read a `main` that must
  hold what the task waits on. It gets **the same guard** (`GuardStartTx`
  in `recoverTask`, before the new attempt starts) and the **Start over
  anyway escape** in `PickUpDialog`. Its confirm is not disabled up front:
  a refused confirmation names the blockers in the shared words and offers
  Start over anyway in its footer, sending the same request with
  `override: true`. No second dialog.

### Override: "Start anyway"

- **A person may override; an agent may not.** On a blocked task, Deliver
  and Talk it through stay in the header, **disabled, never removed**.
  Their tooltip and `aria-describedby` give the reason. After them come
  **Start when unblocked…** (its owner only; see below) and **Start anyway…**
  (`quiet`, any person) (screens *task*, *task-dark-reason*). The board card's
  start menu offers the same two (see *The board*).
- It opens a `Dialog tone="attention" size="md"` titled "Start CP-44
  anyway?" (screen *override*). The dialog names each open blocker in the
  sidebar's grammar (`RefLead`, title, `StatusMark`, owner face with the
  agent at work; "done, nothing merged" in words in place of the mark) and
  says what overriding means: "It waits on these 2, which aren't merged. Its
  agents start from `main` as it is now: what they change won't be there."
  It asks which
  way to start (a `Select` with Talk it through or Deliver, each with its
  `meta`) and offers an optional note. The safe action, **Keep waiting**,
  takes focus on open. The confirm button reads "Talk it through anyway" or
  "Deliver anyway". It is `primary`, not danger, because overriding is a
  person's legitimate call, not a destructive one.
- The routes take `override: true` (`deliver`, `talk`). Only a request whose
  principal is a person is honoured. `principalOf(r).ActorType == human`
  is checked, and a service or agent principal gets 403 "only a person can
  start a blocked task". The override is recorded as `task.start_overridden`
  `{blockers: [{key, status}], how, note}` by the person, and the Activity
  line reads "Tom started it anyway, past CP-38 (failed) — Talk it through"
  with the note quoted (screen *history-override*).
- The relations stay. The started task no longer shows a "Blocked by" chip
  (a chip is only for a task that hasn't started), but its Relations list
  keeps the edges.

### Unblocking

- **The event fires on a merge, not on every move to done.** In
  `delivery.RecordStatusTx` (`delivery/store.go:498`, which calls
  `releaseDependantsTx` in `delivery/unblock.go`), the one place every
  status change is recorded: when `status = 'done'` **and** the task has a
  pull request whose `state = 'merged'` (the `pull_requests` rows are
  written by the PR syncer before `pullRequestEnded` reads them, so they are
  current in this transaction), it finds each task this one blocks that now
  has no open blocker, and appends `task.unblocked {by: <blocker key>, pr:
  <number>}` on each. A move to `done` with nothing merged (Mark done, an
  escalation's "done" with nothing merged) appends nothing: it released
  nobody (see *Resolved*).
- **Without a request, nothing starts.** The task becomes **startable**: it
  stays in Backlog as it was, its chip goes away, and Deliver / Talk it
  through come back. Starting is a choice between two ways (README,
  *Starting is a choice*), and the person who filed it may not be ready.
- **With one, it starts** the way its owner chose: see *Start when
  unblocked*.
- **Told twice, quietly, and nothing more.** *Decided (Márcio, round 2): a
  Chat notice and an Activity line are enough; no browser push.* The task's
  Chat gets dude's notice (`ChatNotice kind="notice"`): "CP-41 merged —
  CP-42 can start. Nothing started it: Deliver or Talk it through when
  you're ready." Activity gets the system line "CP-41 merged (#431) — CP-42
  can start." When a request fires, both say so instead (screens
  *history-chat*, *history*). It is a notice, not a toast (README: toasts
  are for the outcome of *your* action) and not needs-you (nobody is
  blocked on a person). Nothing is sent outside the app.
- **A delivery parked at path 5** (started by a person, then caught by a
  relation added before its implementer ran) is signalled on
  `relations.unblocked` there too, and **resumes**: a person already chose
  its way when they pressed Deliver, and the park was only the guard
  catching a late edge. It re-runs `RefuseBlockedTx` before it resumes, as a
  request does. Removing its last blocker signals it too, from either
  writer (the backend's route, an agent's `update_task`).

### Start when unblocked

*Decided (Márcio, round 2): yes, opt-in per task, off by default.* This is a
**new start path** (path 11 above).

**What it is.** A standing request, set by the task's owner on a blocked
task: "when the last thing in front of it merges, start it this way". The
way is chosen up front, **Deliver** or **Talk it through**, because a start
is a choice between the two and nobody will be there to make it when it
fires. Nothing is preselected in the dialog; the confirm reads "Deliver when
they merge" once a way is picked, and stays disabled ("Set", with "Pick how
it starts." beside it) until then (screen *auto-dialog*).

**Who may set it: the task's owner.** It commits the organisation's agents
and spend at a moment nobody chose, so it is the call of the person the task
belongs to, as picking a stopped task back up is (`ownerOnly`,
`api/api.go:412`). Anyone else sees the menu item disabled, with "Only Ana,
its owner, can set this." **No agent may set or cancel it** (Márcio's
default, and I agree): an agent may not start a blocked task, and a request
that starts one later is the same act deferred. A conductor that thinks it
would help says so in Chat, and the owner sets it.

**Where it is set.**

- On the blocked task's page, in the header after the disabled Deliver
  and Talk it through: **Start when unblocked…** (`quiet`, play glyph), the
  owner only (screen *task*).
- In the board card's start menu, as an item after the two disabled ways
  (screen *board-menu*; see *The board*).
- It is refused, as a disabled item or no button, when it could never fire:
  a blocker marked done with nothing merged will never merge ("CP-37 was
  marked done with nothing merged and will never merge, so this would never
  start it.").

**How it reads once set.**

- **Task page** (screen *task-auto*): the blocked notice under the header
  keeps its first line ("**Blocked by CP-41 (in review) and CP-43 (in
  progress).**") and gains `AutoStartMark` beneath it: the play glyph on the
  active wash, "Starts when CP-41 and CP-43 merge · Deliver", then muted
  "You set it 10m ago" (or "Ana set it 2d ago"), then, for the owner, a quiet
  **Cancel**. Its tooltip (screen *task-auto-*-tip*) says that every blocker
  is checked again when it fires. The header drops "Start when unblocked…"
  (it is set) and keeps Start anyway. The Pipeline empty state reads
  "Deliver starts here once CP-41 and CP-43 merge. A person can start it
  sooner; an agent can't." A reader who isn't the owner sees the same line
  without Cancel (screen *task-other*).
- **Board card**: one muted line under the chips, "▷ Starts when CP-41
  merges · Deliver" (screens *board*, *board-menu*, CP-42). The card's
  accessible name ends "…; starts when CP-41 merges, Deliver". The card's
  start menu shows it as a note at its foot, "Starts when CP-41 merges ·
  Deliver, as Ana set 2d ago.", with **Cancel start when unblocked** (the
  owner's; disabled with "Only Ana, its owner, can cancel this." for anyone
  else).

**When it fires** (path 11).

1. The merge's transaction (`RecordStatusTx` → `releaseDependantsTx`)
   first takes an advisory lock per dependant, in id order
   (`pg_advisory_xact_lock(hashtext('task_release:' || id))`), so two
   blockers merging in overlapping transactions are serialised and the
   second sees the first. No start path takes it: the dependants' task rows
   would be taken before their deliveries and auto-start rows, the reverse
   of every start (`LockEscalationTx`, the worker), and deadlock with them;
   then it appends `task.unblocked` on a released task and, if the task has
   a row in `task_auto_starts`, marks it due (`due_at = now()`). **Nothing
   starts in that transaction**: a start that fails must never roll back a
   merge's status.
2. The orchestrator's auto-start loop (`delivery.AutoStarts.Sweep`, every
   second in `cmd/dude-orchestrator`) takes due rows `FOR UPDATE SKIP
   LOCKED`, one transaction per task, which makes the request durable
   across a restart and run exactly once.
3. In that transaction, **`RefuseBlockedTx` runs again and re-checks every
   blocker**, not only the one that merged: an edge added since the request
   was set, or a second blocker still open, refuses it. A refusal leaves the
   row in place (no longer due), so the request waits for the next merge.
   (`StartTx`'s guard reads them again, but it skips that when joining a
   live delivery, which a delivery parked at path 5 is; the fire-time check
   does not.)
4. If it passes, it starts through **the same function the routes use**,
   `delivery.StartTx(ctx, tx, starter, StartInput{…})`, which `deliver`,
   `talkThrough` and the worker all call, so a fired start is the same start
   a person makes, with the same guard at its top. The writer is the system
   actor, and the person who set it is carried in the payload. The row is
   deleted.
5. If the start refuses for a reason of its own (the task gone, a policy
   that does not decode, a write the data refuses), firing again would meet
   it again: the request ends (`task.auto_start_cancelled {why: "failed",
   error}`), and Chat says "CP-41 merged, but CP-42 could not start:
   <error>. Start it from its page." Kept standing, hidden behind a task
   that shows as startable, it would fire on some unrelated later merge.
   A failure of the database or the connection (a deadlock, a serialisation
   failure, a statement timeout, a dropped connection) is not the start's:
   the request stays due and a later sweep fires it, saying nothing. Any
   other error is retried the same way, counted on the row
   (`fire_attempts`, each wait 5 s longer), and at the fifth the request
   ends as failed with that error. Settlement locks and checks the UUID
   `generation` claimed by the fire before changing or ending the request.
   Every owner PUT generates a new UUID, including repeated identical PUTs;
   a stale failure or an error before claiming a row settles nothing.
   `set_at` is transaction-time `now()`, retained for display, not identity.

**What it says when it fires** (screens *history-chat*, *history*):

- Chat: "CP-41 merged — CP-42 started (Deliver), as Ana asked. Nothing else
  was waiting in front of it." `TaskHistory` at its head reads "Started when
  unblocked · Deliver · implementer · was blocked by CP-41 for 3d".
- Activity: "CP-41 merged (#431) — CP-42 started (Deliver), as Ana asked.
  Every blocker was checked again: none open." with Ana's face, because it
  is her act deferred. The earlier line "Ana set it to start when
  unblocked — Deliver" stays below it.
- Refused at fire time: "CP-41 merged, but CP-42 is still blocked by CP-40
  (in review): it starts when that merges, as Ana asked." Nothing else.
- Failed at fire time: "CP-41 merged, but CP-42 could not start: <error>.
  Start it from its page." Activity: "Start when unblocked could not start
  it: <error>. It is no longer set: start it from here."

**What ends a request without firing.** The owner's Cancel
(`task.auto_start_cancelled`); any start of the task (Deliver, Talk it
through, Start anyway: the request is consumed); the task changing owner (the
new owner didn't ask for it; Activity says it was dropped); removing the
task's last open blocker relation (the owner is present and acting, so the
task becomes startable and the request ends, saying so); and a start that
refused at fire time for a reason of its own, or failed five times for one
not known to be transient.

### With agent comms

Screen *override-comms*, labelled in the switcher **"3 · Start anyway —
Later: with agent comms"**, and with the badge "Later: with agent comms" in
the dialog's title. It depends on another thread's work (messages between
agents on different tasks) and is not part of this proposal's first build.

With that messaging, a blocked task could start **in parallel** with its
blocker if their agents coordinate. The Start anyway dialog gains one
`ChoiceList`, **How it starts**:

- **Start in parallel — CP-42's agents talk to CP-41's** (preselected in
  this variant, since that is why it was opened): "They tell each other what
  they are changing while both run. CP-41 still has to merge before CP-42's
  pull request can."
- **Start on its own**: today's override, from `main`, no channel.

and, for a parallel start with one blocker, **Start from**: **CP-41's
branch (stacked)**, preselected, or **`main`**. The confirm reads "Talk it
through in parallel" / "Deliver in parallel".

What the design commits to:

- **Relations become the address book.** The agents of a task may message
  the agents of the tasks it is related to (`blocks` either way, and
  `related`), and no others. Who talks to whom is then visible on the task
  page and the board, needs no second list, and changes when a person
  changes the relations. An edge's removal closes the channel.
- **"Blocks" stays a gate for code that has to be merged first.** A
  parallel start does not weaken it: the dependant's pull request is held
  (`ready_to_merge` refused, the same `task_blocked_by` read) until the
  blocker merges, so `main` never takes the dependant before what it
  stands on. The relation still means "after", for the merge.
- **A parallel start is a recorded override with a channel attached.** It
  is Start anyway (a person only, `task.start_overridden`) with
  `{mode: "parallel", base: "stacked"|"main", channel: [blockerKeys]}`; the
  Activity line reads "Ana started it in parallel with CP-41, its agents
  talking to CP-41's — Talk it through". No agent can start one.
- **Stacking on the blocker's branch is the safer base.** The dependant's
  agents see what the blocker has pushed from the first minute, so the
  messages are about intent, not about reconstructing code; when the
  blocker merges, the dependant's branch is rebased onto `main` (the
  blocker's commits drop out as already applied). From `main`, both sides
  must describe their changes in words until the merge, and the conflict is
  found later. With several blockers, stacking is offered only when they
  share one branch; otherwise the base is `main`.

#### Keeping a task current with its base (built)

Stacking is still unbuilt: no start takes a blocker's branch as its base.
A dependant gets its blocker's work by integration once the blocker merges
into the base they share.

The rebase above is not what was built: every move of a task branch is a
fast-forward, so a task takes its base in by **merging**, never rebasing or
forcing (`orchestrator/internal/delivery/integration.go`).

- **The tool.** Writers (implementer, fixer, simplifier, conductor) call
  `sync_base`: lux fetches the task's base — the repository's default, or
  the base of the branch the task continues (`State.Bases`) — into the
  checkout as `lux/<base>`, and the agent runs `git merge lux/<base>`. When
  a pull request merges, other conductors on the same base are told once
  (`base_moved`, a `chat.notice` and a `checkout` wake): one running hears
  it at once; one parked hears it with its next note, and is not resumed
  for it.
- **Automatic, with `whenBehind` `update`.** Before a pull request opens,
  the workflow asks GitHub to merge the moved base into the branch (a
  `merge-base` line on the activity) and tests the new head again. A
  conflict, there or on an open pull request, starts an **integration
  Run**: a fixer told to merge and resolve keeping both sides' intent, then
  an internal review that reads `git show --remerge-diff` of the merge,
  then the policy's test. The conductor never runs tests.
- **Bounds.** One integration Run per head/base pair
  (`State.IntegrationPairs`); a pair tried already, a failed Run, or a pull
  request still conflicting after it escalates `pull_request_conflict`
  with `integration: true`, offering retry or wait. Retry is one more
  attempt; nothing loops by itself. An integration Run spends none of the
  pull request's fix budget. Every publish, a phase Run's and a conductor's,
  counts only the task's own changes by exact commit
  (`phases/accounting.go`), against the base the checkout holds
  (`lux/<base>`): without it every commit counts as the task's. A lux that
  cannot answer is asked again; evidence that is bad fails the Run or
  refuses the publish.
- **Explicit, with `whenBehind` `tell`.** Nothing integrates by itself; the
  pull request card's **Resolve conflicts** (owner only) signals the
  workflow waiting on that pull request, or decides its conflict
  escalation. The request starts no phase itself, and is refused while
  dude is busy with the task.
- **Not yet shown against real lux.** `sync_base` and the integration
  Run's merge are exercised against the fake lux; real lux's `fetch` sync
  mode and its `git.sync` reports are taken from its contract, not tested
  end to end here.

## The board

Screens: *board*, *board-menu*, *board-blocked*, *board-hover*,
*board-dark-chiptip*, *board-*-starttip*, compact *board-*-compact*,
*board-menu-*-compact*.

- **A blocked card** (not started, with at least one open blocker) carries
  a `RelationChip kind="blocked-by"` between its title and its foot. With
  one blocker it shows lock · "Blocked by" · the blocker's status mark ·
  its key ("Blocked by ◉ CP-41", so you can see CP-41 is in review without
  hovering). With several it shows a count: "Blocked by 2". The tooltip
  lists each blocker with `RefLead`, title, and project when it is
  another. The card's accessible name becomes "CP-44 Billing page: …,
  blocked by CP-41 (in review) and CP-43 (in progress)", and the chip is
  `role="img"` with the same words, so a screen reader hears it without
  the glyph.
- **A blocking card** (any card that holds up a not-started task) carries
  the quieter `RelationChip kind="blocks"`: no wash, muted ink, a
  bar-arrow glyph. With one task it shows "Blocks ◌ CP-44", with more
  "Blocks 3". A blocker that has merged shows nothing. One marked done with
  nothing merged still shows "Blocks", and its foot reads "done, nothing
  merged" where a merged one reads "merged" (CP-37).
- **A Backlog card has a start** *(decided, Márcio, round 2)*. Round 1
  left it out; it is back, as a compact control at the right of the card's
  foot: **`StartMenu`**, a quiet `sm` button reading "▷ Start ⌄".
  - **Menu, not split button.** A start is a choice between Deliver and
    Talk it through, and README *Starting is a choice* says neither is the
    default ("no primary, nothing preselected"). A split button's face is
    one way preselected, so it would break that rule. The DS has no split
    button, and it has a menu: **`RowMenu`** (`primitives/RowMenu.tsx`),
    whose items take an icon, a description, `disabled` + `disabledReason`
    (shown as a tooltip and read to screen readers), separators and notes,
    and whose `trigger` prop takes a custom button. `StartMenu` is
    `RowMenu` with the two ways as items, each with its one-line
    description ("The conductor plans with you first", "The automatic
    pipeline"), in the same order as the task header's. Nothing new is
    drawn for the menu itself.
  - **Unblocked card** (CP-47): "▷ Start ⌄" opens the two ways, both
    enabled. Picking one starts it exactly as the task header's button
    does (paths 1 and 2).
  - **Blocked card** (CP-44, CP-49, CP-51): the trigger shows the lock in
    place of the play glyph, in muted ink, and its tooltip is the reason
    ("Blocked by CP-41 (in review) and CP-43 (in progress): it can start
    once they're merged.", screen *board-*-starttip*). The two ways are
    **disabled items with that reason** (screen *board-menu*, the reason
    beside the hovered Deliver). For a person the menu then offers, after a
    separator, **Start when unblocked…** (the owner's; disabled saying
    whose for anyone else, or refused when a blocker will never merge) and
    **Start anyway…**, which opens the same dialog as the task page's.
    Because those two are live, the trigger stays operable: it is the ways
    *in* the menu that are disabled, never the way to reach them.
  - **Set to start when unblocked** (CP-42): the card carries the muted
    line "▷ Starts when CP-41 merges · Deliver" under its chips, and the
    menu's foot has the note and the Cancel item.
  - **Only people who may start it see it.** A person who may start the
    task (an organisation member; the backend's `deliver` / `talk` routes'
    caller) gets the control; a reader with no right to start, or an
    agent's view, gets none (the card keeps its chips, which say why it
    waits). This is the README's reader rule, not a disabled control: there
    is nothing for that reader to do.
  - **Not nested interactive.** The card stays what it is: one
    `role="button"` element that opens the task, with one tab stop for the
    board's ↑↓←→ (README, *Board*). The start is **not inside it**. It is a
    **sibling of the card in the card's `<li>`**, positioned over the
    foot's right end (the foot keeps its room with padding, so nothing
    overlaps), so neither is a descendant of the other, and the card's
    accessible name is not polluted by the menu's. Its focus stop: the
    start is `tabIndex=-1` except on the card that holds the board's roving
    focus, where it is `0`, so **Tab from a card goes to that card's start,
    and Shift+Tab back**; ↑↓←→ on the card still move between cards and
    never land on a start. Enter/Space on the start opens its menu (the
    `RowMenu` keys), Escape closes it and returns focus to the start. A
    click on the start never reaches the card, because it is not inside
    it. In `Board` this is a new prop, `cardStart?: (card) => ReactNode`,
    rendered after the card in its `<li>`; the board owns the tab-index
    rule, so an app cannot get it wrong.
- **The README's rule changes.** It now says "A card is a way in, not a
  handle" with no exceptions (*Board*). The proposed text:

  > A card is a way in, not a handle: it opens the task, and nothing on it
  > moves the task between lanes. **The one exception is a Backlog card's
  > start** (`cardStart`): a sibling of the card, never inside it, that
  > offers the same ways to start as the task's header, as a menu (never a
  > split button: neither way is the default). It is shown only to people
  > who may start the task, only in Backlog, and only for a task not
  > started. Blocked, its ways are disabled with the reason and its menu
  > offers Start when unblocked and Start anyway. Nothing else on a card
  > acts: confirming a plan, aborting, merging stay decisions taken on the
  > task.

  Its limits, so the exception does not grow: only starting (the one
  transition a person makes from Backlog without needing the task's
  context, since the card says what it waits on); only Backlog; only a
  menu of the task header's own ways, so the card can never offer
  something the page doesn't.
- **Hover or focus lights a card's relations.** The card gets the accent
  bar (as `selected` does), its relations get the active wash, and every
  other card dims to 35% opacity. A live region says the same in words
  ("CP-41's relations: blocked by CP-40; blocks CP-42, CP-44, CP-45").
  Leaving the board puts it out. Screen *board-hover* shows CP-41 lit: CP-40
  in Done, CP-42 and CP-44 in Backlog. Nothing visible is added above the
  board, because a line there would shift the cards under the pointer.
- **The filter is "All · Can start · Blocked"**, a `Segmented size="sm"` in
  the board's `headerActions`, counts in the labels. It is **not a triage
  bucket**, for three reasons:
  - Triage answers "what needs me", and only four buckets are counted
    (README, *Triage*). A blocked task needs nobody until its blocker
    lands. Counting it would put "6" beside every project in the sidebar
    for something no one can act on.
  - A bucket would make it compete with needs-you for the sidebar's one
    amber region.
  - The Segmented is a view, it is reversible, and it is the same control
    the page already uses for Everyone/Mine. "Can start" is the more
    useful of the two filters for a planner ("what can I hand out now?").
- **The chain across columns** is visible without hovering: CP-41 sits
  In review with "Blocks 3", and CP-42 and CP-44 sit in Backlog with
  "Blocked by ◉ CP-41". The status mark in the chip makes "in review"
  readable from Backlog.
- **The blocked tone.** No new tone or colour was added. "Blocked by" is
  `text-primary` on `--ds-color-active-wash`, and "Blocks" is
  `text-muted` with no wash. I checked this against the README's bars with
  the same maths as `test/paletteDistance.test.ts` (OKLab ΔE×100, Machado
  2009 protan/deutan at severity 1.0), comparing the neutral tone's fg (the
  ink the chip resolves closest to) with every way needs-you is drawn:

  | mode | vs attention fg | vs attention mark | vs attention solid | closest role fg |
  |---|---|---|---|---|
  | dark | 18.9 / 17.4 / 19.4 | 18.9 / 17.4 / 19.4 | 18.1 / 16.8 / 18.5 | implementer 12.1 / 4.5 / 4.0 |
  | light | 11.6 / 10.7 / 11.9 | 24.1 / 20.0 / 24.7 | 37.2 / 32.9 / 38.4 | implementer 8.3 / 4.2 / 2.1 |

  (normal / protan / deutan). Against the attention tone it clears the
  pair bar (15 normal, 8 CVD) in dark, and in light it clears the CVD bar
  and is 11.6 in normal vision, which is above the README's ΔE 3
  attention floor by a wide margin. The neutral is near some role
  colours under CVD (implementer, qa_browser), but that is true of every
  neutral ink already in the system. The bar applies to *chromatic*
  slots, and the chip is never a role colour: it carries a lock and the
  words "Blocked by", so in grayscale it is still the only lock on the card.
  This is why **no new tone was generated**: a chromatic "blocked" tone would
  need a free hue that clears the pair bar against the four tones and six
  role slots, and the README already records that the free hues near 80 sit
  on attention. If one is ever wanted, it goes through `palette.ts`'s
  search and `bun run tokens`, never by hand.

## The task page

Screens: *task* (CP-44, owner), *task-dark-reason* (tooltip on the disabled
Deliver), *task-auto* (CP-44 set to start when unblocked, and its tooltip
*task-auto-dark-tip*), *task-other* (CP-42 read by Márcio, who doesn't own
it, with the remove hint showing), *unmerged-blocker* (CP-51),
*failed-blocker* (CP-49), *auto-dialog*, *editor*, *cycle*, *override*,
*override-comms*, phone *task-phone-**.

- **Header.** Deliver and Talk it through are disabled with the reason
  (tooltip on a focusable wrapper, because a disabled button takes neither
  pointer nor focus). Their `aria-describedby` points at the same sentence,
  and they are followed by **Start when unblocked…** (the owner, until it is
  set) and **Start anyway…**.
- **Notice** (`Callout tone="neutral"`, where an escalation would sit):
  "**Blocked by CP-41 (in review) and CP-43 (in progress).** It can start
  once they're merged; nothing starts it on its own then." Set to start when
  unblocked, its second line is the `AutoStartMark` instead (see *Start
  when unblocked*). A failed blocker adds a line saying it keeps blocking
  and how to clear it, and a blocker done with nothing merged adds the line
  in *Resolved*. The tone is neutral, not attention: no one is needed.
- **Pipeline** (the empty `StartChoice` slot) becomes an `EmptyState`
  "Waiting on its blockers", whose description notes that a person can
  start it anyway and an agent can't.
- **Relations**, in Overview after the acceptance criteria. It has three
  groups under small-caps heads with their glyph and count (lock **Blocked
  by**, bar-arrow **Blocks**, link **Related**). "Blocked by" says "1 of 2
  open" once one has merged. Each row is in `RemovableList`'s grammar:
  `RefLead` (status mark and key), the title, the project if another,
  "nothing merged" for a blocker marked done without a merge, `StatusMark`
  in words, the owner's face with the working agent's tile on it, and a
  quiet remove ("Remove blocked by CP-41") **for the edge's owner only**:
  anyone else gets no ✕ and the hint described in *Who may add and remove
  a relation*. Empty groups are left out, and with none at all one line says
  what relations are for. **Add relation** (`quiet`, `plus`) is in the
  section head, for everyone.
- **The editor** (screen *editor*) opens under the head on the chrome
  shade. It has a `Select` for the kind (Blocked by / Blocks / Related to,
  each with a one-line description of what it gates) and a `SearchPicker`
  over the organisation's tasks: `findOnEmpty` suggestions, `group` by
  "this epic", then the project, then other projects, and `optionDisabled`
  showing "This task" and "Already related" badges. Each option is
  `RefLead`, title and `StatusMark`. Enter adds it and the picker stays open
  for the next. Escape closes the list, then the editor.
- **The cycle refusal** (screen *cycle*) is a danger line under the picker
  (`role="alert"`). It names the loop with the bar-arrow glyph between keys
  (`aria-label` "CP-40 blocks CP-41, which blocks CP-44") and offers
  **Make it Related instead**.
- **Phone (390)**. The header's actions wrap to their own line under the
  breadcrumb, and Relations rows truncate the title before anything else.
  The status word and face stay, so the row still says whether it blocks.

## History

Screens: *history* (CP-42 Activity, the morning CP-41 merged and it
started as Ana had asked), *history-chat* (CP-42 Chat), *history-override*
(CP-49 Activity), *history-unmerged* (CP-51 Activity).

- **Relation added or removed** is a `TimelineItem`. For a person it shows
  their face and "**Márcio** removed related **WEB-9**". For an agent it
  shows the person's face with the agent's role tile on it (the
  `AuthorLine` rule: an agent works for someone), named "**Ana's
  conductor** added blocked by **CP-40**", with the agent's stated reason
  quoted. When a person files a proposal's relation, the line credits both:
  "**Ana** filed it with blocked by **CP-41**, as **Ana's Brainstorm**
  proposed."
- **Start when unblocked set** (who, when, which way): "**Ana** set it to
  start when unblocked — Deliver", with her face; Chat gets "Ana set this to
  start when it's unblocked, with Deliver. Nothing starts before CP-41
  merges." Cancelled: "**Ana** cancelled start when unblocked".
- **Fired**: "**CP-41** merged (#431) — **CP-42** started (Deliver), as
  **Ana** asked. Every blocker was checked again: none open.", with Ana's face;
  in Chat, "CP-41 merged — CP-42 started (Deliver), as Ana asked. Nothing
  else was waiting in front of it." `TaskHistory` at Chat's head reads
  "Started when unblocked · Deliver · implementer · was blocked by CP-41 for
  3d".
- **Unblocked, with nothing set**: a system line ("CP-41 merged (#431) —
  CP-42 can start.") plus dude's `ChatNotice` in Chat ("Nothing started it:
  Deliver or Talk it through when you're ready."), and `TaskHistory` reads
  "Not started · ready to start · was blocked by CP-41 for 3d". No other
  channel (*Decided*: no browser push).
- **Done with nothing merged**: "**CP-37** was marked done by **Tom** with
  nothing merged. It still blocks **CP-51**: only merged work releases what
  waits on it."
- **Override**: "**Tom** started it anyway, past **CP-38** (failed) — Talk
  it through", with his note quoted.

## Propose work

Screens: *proposal*, *proposal-refused*.

- **Items are numbered** (a small `mark` chip, 1, 2, 3) and **rendered in
  dependency order**. The head carries an **Order** hint, "1 → 2 3", where
  items in the same step sit side by side. I chose this over indentation
  because `ProposalCard` already indents a task under its new epic
  (`child`), and a second meaning for indentation would be ambiguous.
- **Each item lists its relations** under its goal: "lock Blocked by
  **item 1**" for an in-proposal edge, "lock Blocked by ◉ CP-41 · In
  review" for an existing task (with `RefLead` and `StatusMark`), and "link
  Related CP-44". The head counts them ("3 tasks, 4 relations").
- **Filing a blocked item files its blocker with it.** Ticking item 2 or 3
  ticks item 1 too, and item 1 says why: "Ticked with 2 and 3: they wait on
  it. Untick them to leave it out." The footer says "Filing as Márcio: 3
  tasks, 1 first, with their 4 relations. Nothing starts." I chose this
  over refusing because the person ticked 2 to get 2, and item 2 is useless
  without item 1. Filing items in order is already the rule for epics
  (`fileProposal` sorts new epics first, `sessions.go:704`), and this
  extends it with a topological sort over in-proposal `blocks`.
- **When the blocker can't be filed**, the dependant can't either. A new
  `cannotFile` reason, `blocker_unfiled` (the `blockedBy` union in
  `packages/domain/src/sessions.ts:114`), reads "Waits on item 1, which
  can't be filed: link data-pipeline again, or ask the brainstorm to
  propose it without item 1." It is decided *after* each item's own
  reasons, so item 1 says its own ("data-pipeline is no longer linked to
  this session") and items 2 and 3 say they wait on it. Like `unlinked`,
  it blocks everyone, so the card stays open (screen *proposal-refused*).
- **Filed items keep their relations as real edges**, written in the same
  transaction as `fileItem`, with `created_by` the person who filed and
  `proposed_by_run` the brainstorm's Run. An edge to an item that is
  already filed uses that item's key from `session_filings`. An edge to an
  existing task is checked for cycles like any other. A cycle among the
  proposal's own items is refused at `propose` time, so a card never shows
  one.

## The epic

Screen *epic* (and *epic-*-compact*).

- **`DependencyStrip`** sits in the epic board's `overview` slot on the
  chrome shade. It has a head ("ORDER · 1 can start · 5 blocked · 2 in
  progress or review · 1 merged · 1 done, nothing merged · 1 failed") and the epic's tasks laid out
  in **steps**: each task sits one step after the last of its in-epic
  blockers. A task is a small `surface` chip with `RefLead`. One that can
  start says **can start** in strong ink, and one that is blocked carries
  the lock. Each chip's tooltip names what it waits on, and clicking opens
  the task. Hovering a board card dims the strip too.
- The strip is deliberately not a graph editor. Steps encode the order,
  the tooltip carries the edges, and editing happens on the task. Across
  epics, only this epic's tasks are placed, and an outside blocker shows
  only as the lock and in the tooltip.
- `EpicCard` (`ProjectEpics.tsx`) gains one fact in its foot, "2 can start
  · 3 blocked", in the same words. Not mocked, because it is one more
  `<span>` in an existing row.

## Agents

### Tools and parameters

| tool | change |
|---|---|
| `create_task` | `blockedBy: string[]`, `blocks: string[]`, `related: string[]` (task keys in the organisation). Each edge is written with the task, in its transaction; a refused edge (unknown key, cycle) refuses the whole create, saying which |
| `propose` | each item takes `blockedBy`, `blocks`, `related`, whose entries are a key (`"CP-41"`) or an in-proposal reference (`{"item": 1}`, 1-based as the card numbers them). Cycles within the proposal are refused at `propose` |
| `update_task` | `addRelations: [{kind: "blocked_by"\|"blocks"\|"related", task: KEY, why?: string}]`, `removeRelations: [{kind, task}]`. Allowed on the caller's task **at any status** (unlike its text, which is frozen once an implementer starts), because learning mid-run that something else must land first is exactly when this is needed. `why` is shown quoted in the history line. `removeRelations` is refused ("you can remove only a relation this task's agents added; ask Ana, its owner") for an edge an agent of this task did not create (see *Who may add and remove a relation*). No parameter sets or cancels start when unblocked |
| `list_tasks` | each task gains `blockedBy: [{key, status, merged}]`, `blocks: [{key, status}]`, `related: [key]`, `startable: boolean` (not started and nothing unmerged in front of it) and `startsWhenUnblocked: "deliver"\|"talk"\|null`. `text` filtering stays. A new `startable: true` input lists only those |
| `relate` *(new, brainstorm only)* | `{from, to, kind, why?}` on existing tasks of linked projects, as a proposal item of kind `relation` that a member files. A brainstorm proposes and never writes directly, which keeps its "nothing changes until a person files" rule |

CLI (`orchestrator/cmd/dude/main.go`):

- `dude task create … --blocked-by CP-41 --blocks CP-48 --related CP-44` (repeatable, `many`).
- `dude task update [--block-on KEY]... [--unblock KEY]... [--blocks KEY]... [--relate KEY]... [--unrelate KEY]... [--why TEXT]`.
- `dude task list [--startable]`, with each task's relations on its line: `CP-44 · received · blocked by CP-41 (review), CP-43 (running)`.
- `dude relate KEY --blocks KEY | --related KEY` (brainstorm), filed as a proposal item.

### Who gets what

| role | read (`list_tasks`) | write |
|---|---|---|
| brainstorm | yes | through `propose` and `relate`, which are filed by a person. Its job is to work out the order, but its rule is that it changes nothing until a member files |
| conductor | yes | `create_task` and `update_task` on its own task, because it plans the task with the person and is the one that learns "this needs CP-40 first". It removes only edges its task's agents added |
| investigator | yes | `create_task` with relations, since it finds follow-ups that wait on the work it is looking at. No `update_task` |
| implementer, fixer, simplifier, reviewer, tester | yes, read only | **no**. An implementer that finds out mid-run that it needs other work says so through `ask_person` or `create_task` (for the missing work), and the conductor or the person relates them. Letting an implementer block its own task would let it bail out of hard work by declaring a dependency |

**No agent can override, and no agent can set or cancel start when
unblocked.** The override routes check for a human principal, the
auto-start route checks for the task's owner, and no tool has a parameter
for either.

### What an agent is told when it tries

A conductor's `start_phase implement` / `decide next` at the start point,
on a blocked task:

> refused: CP-44 is blocked by CP-41 (in review) and CP-43 (in progress):
> it can start once they're merged. You can't start it anyway; a person
> can. Tell the person, and end your turn: you are woken when it can start.

Its prompt (`delivery/prompts.go`, conductor) gains one line: "A task
blocked by another starts only once that one is merged; if a start is refused
as blocked, tell the person what it waits on and stop." `steer` on a
blocked task's Runs is not affected, because a blocked task has no live
phase Run unless a person started it anyway.

The conductor is woken by a new wake reason `unblocked` (`conductor_wakes`,
`delivery/wakes.go`) when its task becomes startable, so it can tell the
person in Chat. It still doesn't start anything until the person agrees,
as before; if its owner set start when unblocked, the task has started by
the time it reads the wake, and the wake says so.

## Data, API, events

### Table (one migration)

```sql
CREATE TYPE task_relation_kind AS ENUM ('blocks', 'related');

CREATE TABLE task_relations (
  organization_id  text NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  from_task_id     text NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
  to_task_id       text NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
  kind             task_relation_kind NOT NULL,
  created_by_person text REFERENCES people(id) ON DELETE SET NULL,
  created_by_run_id text REFERENCES runs(id) ON DELETE SET NULL,
  -- A brainstorm's proposal a person filed: who proposed it, beside who filed.
  proposed_by_run_id text REFERENCES runs(id) ON DELETE SET NULL,
  why              text,
  created_at       timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (from_task_id, to_task_id, kind),
  CHECK (from_task_id <> to_task_id),
  -- related is symmetric: stored once, the lower id first.
  CHECK (kind <> 'related' OR from_task_id < to_task_id),
  CHECK (created_by_person IS NOT NULL OR created_by_run_id IS NOT NULL)
);
CREATE INDEX task_relations_to_idx ON task_relations (to_task_id, kind);
-- RLS as task_repositories (018): isolation on organization_id, ENABLE + FORCE, GRANT to dude_app.

-- Start when unblocked: one standing request per task, its owner's.
CREATE TABLE task_auto_starts (
  task_id         text PRIMARY KEY REFERENCES tasks(id) ON DELETE CASCADE,
  organization_id text NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  how             text NOT NULL CHECK (how IN ('deliver', 'talk')),
  set_by_person   text NOT NULL REFERENCES people(id) ON DELETE CASCADE,
  set_at          timestamptz NOT NULL DEFAULT now(),
  generation      uuid NOT NULL DEFAULT gen_random_uuid(),
  -- Set by the merge that released the task's last blocker; the worker takes due rows.
  due_at          timestamptz,
  fire_attempts   integer NOT NULL DEFAULT 0
);
CREATE INDEX task_auto_starts_due_idx ON task_auto_starts (due_at) WHERE due_at IS NOT NULL;
-- RLS as above.
```

`task_blocked_by(task text) RETURNS TABLE(key text, status task_status, id text)`
is a `STABLE` SQL function returning the **open blockers**: every `blocks`
edge into `task` whose `from` task is not *merged*, where merged means
`status = 'done'` and at least one of its `pull_requests` rows has
`state = 'merged'`:

```sql
SELECT p.key_prefix || '-' || b.number AS key, b.status, b.id
  FROM task_relations r
  JOIN tasks b ON b.id = r.from_task_id
  JOIN projects p ON p.id = b.project_id
 WHERE r.to_task_id = $1 AND r.kind = 'blocks'
   AND NOT (b.status = 'done'
            AND EXISTS (SELECT 1 FROM pull_requests p
                         WHERE p.task_id = b.id AND p.state = 'merged'))
```

A blocker marked done with nothing merged is therefore returned with status
`done` (no open blocker can be `done` otherwise), and every caller names it
"done, nothing merged". Both processes call it (path 8 in the backend, `RefuseBlockedTx` in the
orchestrator, and the fire-time re-check of path 11), so the rule lives in
one place. The cycle check is
`task_relation_would_cycle(from text, to text) RETURNS text[]`, returning
the loop's keys or NULL, called after taking the advisory lock above. It
visits each task once: a `UNION` reachability walk from `to`, and only when
`from` is reached a breadth-first pass keeping one parent per task to name
the shortest loop. (Walking every path instead is exponential on a plan
shaped like a ladder, and would hold the organisation's lock while it
ran.) Both are raw SQL because neither `Bun.sql` nor pgx gives this
repo a query builder for a function body or a recursive CTE, and the repo
writes its SQL raw by convention (CONTRIBUTING, *Decisions*).

### Routes

- `GET /v1/tasks/:id/relations` returns `{blockedBy, blocks, related}`, each
  `[{task: NavTaskRef, createdBy, createdAt, why, canRemove}]`; `canRemove`
  is the reader's, so the page draws the ✕ or the hint without a rule of
  its own.
- `POST /v1/tasks/:id/relations {kind: "blocked_by"|"blocks"|"related", task}`
  returns 201, 409 `cycle {loop: [keys]}`, 409 `duplicate`, or 400 `self`.
  Any member.
- `DELETE /v1/tasks/:id/relations/:kind/:other`: the edge's owner only
  (the blocked task's for `blocks`, either task's for `related`); anyone
  else gets 403 `not_owner`: "only Ana can remove this relation — it holds
  up their task" for `blocks`, "… — it's on their task" for `related`, which
  holds nothing up.
- `PUT /v1/tasks/:id/auto-start {how: "deliver"|"talk"}` and `DELETE
  /v1/tasks/:id/auto-start`: the task's owner only (403 `not_owner`); 409
  `not_blocked` on a task with nothing open in front of it ("it can start
  now: Deliver or Talk it through"), 409 `never_merges {blockers}` when a
  blocker is done with nothing merged, 409 `started` on a started task.
- `GET /v1/tasks?…` and `/v1/navigation` gain the fields below.
- `deliver`, `talk`: `{override?: true, note?: string}`. Override is
  person-only, and without it a blocked task gets 409 `blocked {blockers}`.

Relations are display and management, so they are the **backend's** to
write (CONTRIBUTING, *the backend owns … tasks*). The orchestrator writes
them only through agent tools and filing, in the same SQL.

### Events (ledger, `packages/domain/src/events/types.ts` and the Go constants)

| type | payload | by |
|---|---|---|
| `task.relation_added` | `{kind, from: key, to: key, why?, proposedByRun?}` on **both** tasks (same correlation) | person or agent Run |
| `task.relation_removed` | `{kind, from, to}` on both | the edge's owner, or an agent Run of the task whose agents added it |
| `task.unblocked` | `{by: key, pr: number, blockers: []}` (the blocker whose merge released it last, and that none is left) | system, from `RecordStatusTx`, **only on a merge**: never on a move to `done` with nothing merged |
| `task.auto_start_set` | `{how}` | the task's owner |
| `task.auto_start_cancelled` | `{how, why: "owner"\|"started"\|"owner_changed"\|"unblocked_by_removal"\|"failed", error?}` (`error` with `failed`: the start failed at fire time for a reason other than a blocker) | the owner, or system |
| `task.auto_started` | `{how, by: key, pr?, setBy: person, setByName, setAt}` | system, for the person who set it; the start's own events follow in the same transaction |
| `task.auto_start_refused` | `{by, blockers}` (still open at fire time) | system |
| `task.start_refused` | `{path: "deliver"\|"talk"\|"chat"\|"start_phase"\|"implement"\|"create_run"\|"start_over"\|"auto_start", blockers}` | whoever tried; makes refusals auditable, never shown as a line. A refusal that rolls its call back (paths 1–4, 6) is recorded in a transaction of its own; `start_phase` covers both the conductor's `start_phase` and `decide next` |
| `task.start_overridden` | `{blockers: [{key, status}], how, note}` (later: `mode`, `base`, `channel`) | person |

The live stream (`relations.changed`, browser-only) tells a board or task
page to read again, as `servers.changed` does.

### View model

- `NavTask` (`packages/design-system/src/util/navModel.ts:53`) gains
  `blockedBy?: ReadonlyArray<NavRelated>` (open only: not merged),
  `blocks?: ReadonlyArray<NavRelated>` (not-started dependants only),
  `startable?: boolean`, `autoStart?: { how, by: Person, at } | null` and
  `canStart?: boolean` (the reader may start it: draws the card's start),
  where `NavRelated = { key, title, status, project?, unmerged? }`.
  `/v1/navigation` fills them with one join per project.
- `BoardCard` (`boardModel.ts:60`) gains `blocked: boolean`, derived, plus
  `boardFilter(columns, "all"|"startable"|"blocked")`.
- `dependencySteps(epic): NavTask[][]` is new in `boardModel.ts`, pure and
  tested, and it puts a task whose in-epic edges form a cycle (impossible,
  but defended against) in the last step.

## Design-system changes

The pieces below were drawn in the mockup folder at DS quality (tokens
only, both themes, both densities, accessible names) and are built in
`packages/design-system` in this branch (`components/Relations.tsx`,
`RelationDialogs.tsx`, the board's marks and card start in `Board.tsx`).
Each has a gallery `Block` with every state in both themes (`Panes mode`),
in the gallery's `relations` section.

### `Icon`: `lock`, `blocks`

- Paths on the 16 grid, stroked by `Icon`'s rule. `lock` is a padlock and
  `blocks` is an arrow into a bar. `link` already exists for Related.
- README, *Icons*: "`lock` marks a task that can't start until another is
  merged, `blocks` one that holds another up, `link` a relation that gates
  nothing. They take the ink of their words, never a tone."
- Gallery: the icon grid in *Tokens*, which lists `ICON_NAMES`.

### `RelationChip`

```ts
interface RelationChipProps {
  kind: "blocked-by" | "blocks";
  tasks: ReadonlyArray<{ key: string; title: string; status: TaskStatus; project?: string; unmerged?: boolean }>; // open edges only
  className?: string;
}
export function relationWords(kind, tasks): string; // "Blocked by CP-41 (in review) and CP-37 (done, nothing merged)"
```

- README, *Board*: "A task that can't start says what it waits on: a
  `RelationChip` 'Blocked by' (the lock on the active wash, one blocker by
  status mark and key, several counted), and a task that holds others up
  the quieter 'Blocks N' (muted, no wash). Neither is a tone: blocked waits
  on work, not a person, and never takes the attention wash. The chip is
  named in words (`relationWords`) and is part of the card's name. A card
  that has started shows no 'Blocked by'; a blocker that has merged shows
  nothing. A blocker done with nothing merged still blocks, and its chip
  says 'not merged' with no status mark: its check would read as released.
  Another project is said in the tooltip, never on the chip."
- Gallery, *Board · Relations*: blocked by one (each status: in review,
  running, failed, done with nothing merged, another project), blocked by
  3, blocks 1, blocks 4, both on one card, the tooltip open, a narrow card
  where the key truncates last, and grayscale.

### `Board`: `cardMarks`, `cardStart`, `highlight`, the filter

```ts
interface BoardProps {
  // …
  /** Under the title: what the card waits on, holds up, and when it starts on its own. The board draws `RelationChip`s and `AutoStartMark` from `NavTask` when absent. */
  cardMarks?: (card: BoardCard) => ReactNode;
  /**
   * A Backlog card's start (`StartMenu`), drawn after the card in its list
   * item: a sibling, never inside it. Null for a card the reader may not
   * start. Tabbable only on the card that holds the board's focus.
   */
  cardStart?: (card: BoardCard, focus: { tabIndex: 0 | -1 }) => ReactNode;
  /** Keys kept lit, the rest dimmed; null for none. Hover and focus set it when uncontrolled. */
  highlight?: ReadonlySet<string> | null;
  onHighlightChange?: (key: string | null) => void;
}
```

- README, *Board*: the card-start paragraph quoted in *The board* above,
  replacing "A card is a way in, not a handle" with its one exception, and
  "Hover or focus on a card lights its relations: its
  own accent bar, theirs on the active wash, the rest at 35% opacity,
  and a polite live region saying it in words. Nothing is added to the
  board's layout, so the cards don't move under the pointer. 'Can start' and
  'Blocked' are a `Segmented` filter in the header (counts in the labels),
  not a triage bucket: blocked needs nobody."
- `test/borders.test.ts` needs no entry: the lit card reuses `selected`'s
  inset bar.
- A hover re-renders only the cards whose lit state changes: each card is
  memoised, its handlers stable and keyed, and `cardMarks`/`cardStart` are
  called inside it. The web app keeps the lit card out of its top-level
  state (`useLitCard`), so a hover redraws the epic's order and nothing
  else of the shell.
- Gallery, *Board*: the relations fixture lit on a blocker, on a blocked
  task, and on a card with none; the filter on each value; swimlanes with a
  chain across two epics; a Backlog with an unblocked card, a blocked card
  with its menu open (as owner and as not), and a card set to start when
  unblocked; a reader with no right to start (no starts); the keyboard
  path card → Tab → its start → Enter → menu → Escape → start → Shift+Tab → card.

### `StartMenu` *(new)*

```ts
interface StartMenuProps {
  taskKey: string;                          // names the trigger: "Start CP-44"
  blockers: ReadonlyArray<RelatedTask>;     // open; empty: the ways work
  onStart: (how: "deliver" | "talk") => void;
  onStartAnyway?: () => void;               // a person: opens the Start anyway dialog
  auto?: AutoStartView;                     // set: the note and Cancel at the menu's foot
  onStartWhenUnblocked?: () => void;        // the owner; absent: the item is disabled, saying whose
  startWhenUnblockedRefused?: string | null; // why even the owner can't: a blocker that will never merge
  ownerName: string;
  open?: boolean; onOpenChange?: (open: boolean) => void;
  tabIndex?: 0 | -1;                        // the board's roving rule
}
```

- It is `RowMenu` with its `trigger` slot filled: a quiet `sm` `Button`,
  "▷ Start ⌄", the lock in place of the play glyph when blocked, its
  tooltip the reason (the trigger and the tooltip share one element through
  `forwardRef`, so Radix's two `asChild` slots compose). Items: Talk it
  through, Deliver (icon, one-line description, `disabled` +
  `disabledReason` when blocked), then, blocked, a separator, Start when
  unblocked… (or, set, a `note` and Cancel), and Start anyway…. No new
  visual: the menu is `RowMenu`'s.
- Why not a split button: the DS has none, and should not grow one for
  this. A split button's face is the default action, and README *Starting is
  a choice* says the two ways are equal ("no primary, nothing
  preselected"). If a split button is ever wanted elsewhere, it is a
  separate proposal.
- README, *A task's Chat*, after *Starting is a choice*: "On a board card
  the choice is `StartMenu`: the same two ways, in a menu, neither first."
- Gallery, *Primitives · RowMenu*: unblocked, blocked (ways disabled with
  the reason), blocked as owner and as not, set to start when unblocked as
  owner and as not, a blocker that will never merge, compact.

### `AutoStartMark` *(new)*

```ts
interface AutoStartView { how: "deliver" | "talk"; by: string; ago: string; onCancel?: () => void }
function AutoStartMark(props: { auto: AutoStartView; blockers: ReadonlyArray<RelatedTask>; compact?: boolean }): JSX.Element;
export function startsWhen(blockers): string; // "Starts when CP-41 merges", "Starts when CP-41 and CP-43 merge"
```

- Full: the play glyph and "Starts when CP-41 merges · Deliver" on the
  active wash (a `control-sm`-tall chip, `radius-control`, a focus stop
  whose tooltip says who set it and that the blockers are re-checked), then
  muted "Ana set it 2d ago", then a quiet **Cancel** for the owner. Compact
  (a card): the same words in secondary ink, `text-xs`, no wash, no Cancel,
  wrapping under the chips. Neutral, never a tone: nothing needs anyone.
- README, *Board* and *A task's Chat*: "A blocked task its owner set to
  start when unblocked says so where it says it is blocked: 'Starts when
  CP-41 merges · Deliver', who set it, and Cancel for them. It is a
  promise about a start, not a status: no tone, no count."
- Gallery: one blocker, two, as owner (Cancel) and not, compact, and its
  tooltip open.

### `AutoStartDialog` *(new)*

- `Dialog size="md"` (no tone: setting it waits, it doesn't override),
  title "Start CP-44 when it's unblocked?", the blockers in Start anyway's
  list grammar, a `ChoiceList` of the two ways with what each does
  unattended (Talk it through: "Its conductor starts, reads the task and
  the merged code, and waits for you in Chat. Nothing is built until you
  agree."), nothing preselected, and the confirm disabled with "Pick how it
  starts." as its `aria-describedby` until a way is picked.
- Gallery: nothing picked, each way picked, two blockers.

### `RelationList` (with `RelationRow`)

```ts
interface RelationListProps {
  groups: ReadonlyArray<{ kind: "blocked-by" | "blocks" | "related"; rows: ReadonlyArray<RelationRowData> }>;
  onRemove?: (kind, key) => void;
  onAdd?: () => void;
  onOpen?: (key: string) => void;
  adding?: ReactNode; // the RelationPicker, under the head
}
interface RelationRowData { key; title; status; owner: Person; agent?: AgentRole; project?: string; settled?: boolean; unmerged?: boolean; removeHint?: string }
```

- `removeHint` set means the reader may not remove this row: no ✕ (its
  place kept), and the hint in the group's head on the row's hover or focus,
  and as the row's `aria-describedby`.
- README, *Management*: "A task's **Relations** are three groups under
  small-caps heads with their glyph and count — Blocked by ('1 of 2 open'
  once one has merged), Blocks, Related — in `RemovableList`'s rows: status
  mark and key, title, project if another, status in words, the owner's
  face with the working agent on it, remove. An empty group is left out.
  The remove is the edge's owner's: anyone else's row has none, and says
  whose it is on hover and focus ('Only Ana, the owner, can remove this'),
  as a stopped task says whose it is to pick back up."
- Gallery: all three groups, a settled blocker, a blocker done with nothing
  merged, a cross-project row, a long title at 390px, read by someone who
  isn't the owner (the hint showing), read only (no `onRemove`), and empty.

### `RelationPicker`

```ts
interface RelationPickerProps {
  here: { key: string };
  find: (q: string) => Promise<ReadonlyArray<PickableTask>>;
  related: ReadonlySet<string>; // already related: shown, disabled "Already related"
  onAdd: (kind, key) => Promise<void | { loop: string[] }>;
  onClose: () => void;
}
```

- It composes `Select` and `SearchPicker` (`findOnEmpty`, `group`,
  `optionDisabled`, `clearOnPick`). A `{loop}` answer renders the cycle
  line with `errorAction` "Make it Related instead".
- README, *Management*: "Pick what a task relates to with
  `RelationPicker`, never a field of keys. A loop is refused in words that
  name it ('CP-40 → CP-41 → CP-44 → CP-40'), with the one fix offered."
- Gallery: empty with suggestions, typing with groups and disabled options,
  the loop refused, and an unknown key.

### `StartGate`

```ts
interface StartGateProps {
  blockers: ReadonlyArray<RelatedTask>;
  starts: ReadonlyArray<{ id: string; label: string; icon: IconName }>;
  onStartAnyway?: () => void;        // a person only; absent for a reader
  onStartWhenUnblocked?: () => void; // the owner, until it is set; absent otherwise
}
```

- README, *A task's Chat*, after *Starting is a choice*: "A blocked task
  keeps its ways to start, **disabled, never gone**: the reason in their
  tooltip and description, then **Start when unblocked…** for its owner
  and **Start anyway…**, which opens a `Dialog tone="attention"` naming
  each blocker and what starting now means. Keep waiting has the focus. An
  agent has no way to override or to set a start for later."
- Each disabled way sits in a focusable wrapper (a disabled button takes
  no focus, so its tooltip would never open). The wrapper is the control
  assistive tech meets: `role="button" aria-disabled="true"`, the way's
  label, and the reason as its one description.
- Gallery: one blocker, two, a failed blocker, a blocker done with nothing
  merged, as owner and not, and without `onStartAnyway`. The override
  dialog is shown in both start ways.

### `DependencyStrip`

```ts
interface DependencyStripProps {
  steps: ReadonlyArray<ReadonlyArray<StripTask>>; // from boardModel's dependencySteps
  counts: { startable; blocked; working; merged; doneUnmerged; failed };
  onOpen?: (key: string) => void;
  highlight?: ReadonlySet<string> | null;
}
```

- README, *Board*: "An epic's board heads its lanes with its **order**: the
  `DependencyStrip`, steps left to right on the chrome shade, a task as its
  status mark and key with 'can start' or the lock, and the counts in words
  above. It isn't a graph: the steps are the order, and the tooltip names
  the edges."
- Gallery: a 4-step chain, a single step (no relations: the strip is
  omitted), a failed task mid-chain, compact, and a 12-task epic scrolling
  sideways.

### `ProposalCard`

```ts
interface ProposalCardItem {
  // …
  n?: number; // shown when the card has relations
  relations?: ReadonlyArray<{ kind: "blocked-by" | "related"; item?: number; task?: { key; status } }>;
  tickedWith?: ReadonlyArray<number>; // "Ticked with 2 and 3: they wait on it."
}
interface ProposalCardProps { /* … */ order?: ReadonlyArray<ReadonlyArray<number>> } // "Order 1 → 2 3"
```

- README, *Brainstorm sessions*: "A proposal whose items wait on each other
  numbers them, shows the order in its head, and lists each item's
  relations under its goal. Ticking an item ticks what it waits on, saying
  so. An item whose blocker can't be filed can't be filed either, and says
  which (`blocker_unfiled`). Filed, the relations are real."
- Gallery, *Brainstorm*: the three-item chain ready, ticked with, the
  blocker unfiled, all filed (the folded line says "with 4 relations"), and
  read only.

### `TaskHistory`, `ChatNotice`, `Timeline`

No change: the lines are built from existing parts.

## Not in this proposal

- `duplicates` (A duplicates B: close A, point to B).
- Parallel starts with agent comms: mocked as *Later* (see *With agent
  comms*), built when that messaging exists.
- Any notification outside the app on unblock (decided: none for now).
- Relations between epics (an epic blocks an epic). Edges are task to task,
  and an epic's order is derived from its tasks.
- Drag to relate on the board. Nothing drags (README).

## Decided (Márcio, round 2)

| # | question | answer | where |
|---|---|---|---|
| 1 | Opt-in "Start when unblocked"? | Yes: per task, off by default, the way chosen up front, recorded with who and when. The owner sets it; no agent may. | *Start when unblocked*, path 11 |
| 2 | Notify on unblock? | A Chat notice and an Activity line; no browser push. | *Unblocking*, *History* |
| 3 | Start over / Try again guarded? | Start over is guarded, with Start anyway; Try again and Resume continue the same attempt and stay unguarded. (Decided at build.) | *Start over and Try again* |
| 4 | Done or merged? | Merged only. Done without a merge does not release dependants. | *Resolved*, `task_blocked_by`, *Unblocking* |
| 5 | Who removes a relation? | The owner of the task whose relation it is; for "blocked by", the blocked task's owner. Adding stays open. Agents remove only what their task's agents added. | *Who may add and remove a relation* |
| 6 | Cross-project chip? | Tooltip only, as mocked. | *Across epics and projects* |
| 7 | Card start? | Yes: a compact menu on Backlog cards. | *The board*, `StartMenu` |
| 8 | Start when unblocked on a change of owner? | Dropped, and Activity says so (`task.auto_start_cancelled`, `why: owner_changed`): the new owner didn't ask for it. (Decided at build.) | *Start when unblocked* |
| 9 | Start when unblocked when the last blocker is removed? | The request ends without firing (`why: unblocked_by_removal`) and the task is simply startable: the person removing it is present and acting. (Decided at build.) | *Start when unblocked* |

Round 1 listed two deviations from the brief that these answers retire:
"done releases" (now merged only) and "no start on the card" (now back).
The two that remain: the blocked filter is a board view, not a triage
bucket, and the hover highlight adds nothing visible above the board.

## As built

Where the build departs from the text above:

- **The implement step's guard (path 5)** parks a blocked delivery
  (`AwaitSignals relations.unblocked`, re-reading its blockers every 5
  minutes) and sets the task queued, "blocked by …". A person's Start anyway
  releases it (`OverrideParkedTx`).
- **The conductor's first implementer (path 4)** is refused inside the
  tool's transaction, which rolls back; the tools server then records
  `task.start_refused {path: "start_phase"}` as the conductor's, in a
  transaction of its own, as the API's refusals are.
- **Mark done with nothing merged** records `task.blocker_unmerged {by,
  person?}` on each dependant, for Activity's "marked done by Tom with
  nothing merged" line.
- **`relations.changed`** is live only (`pg_notify`), not in the ledger.
- **The brainstorm's `relate`** proposes its own card item, `kind:
  "relation"`, rather than editing an existing task item.
- **The `unblocked` wake** is recorded only for a task with a live
  conductor: a wake would otherwise start one.

## Open questions for Márcio

None left from the proposal. Out of scope still: parallel starts with agent
comms, `duplicates`, relations between epics, push notifications.
