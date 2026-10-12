# Task relations: blocks, related

**Status: mockups for review, 2026-10-12.** Asked for by Márcio after a
session with Jonas. Nothing is built: no table, route, tool or component
exists yet. Mockup: `docs/design/mockups/relations/index.html` (it opens
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
| `blocks` | one directed edge A → B | Blocks B | Blocked by A | yes: B can't start until A is resolved |
| `related` | one edge, stored once with the lower id as `from` | Related B | Related A | never |
| `duplicates` *(later)* | A → B | Duplicates B | Duplicated by A | not mocked; it would close A |

**"Depends on" is not stored.** It is `blocks` read from B's side. The
tools accept `dependsOn` / `blockedBy` as a convenience, and both write the
reverse `blocks` edge, so there is exactly one row per fact.

### Wording: "Blocked by" / "Blocks"

The UI uses one verb pair, **Blocked by** / **Blocks**, everywhere: the
board chip, the task's Relations groups, the Start tooltip, the proposal
card, the history lines and the agents' refusals. "Depends on" was
rejected for the UI for three reasons:

- It describes the *dependant's need*, while what the board and the start
  must say is the *state that stops the start*. "Blocked by CP-41" answers
  "why can't I start this?" without a second clause, and "Depends on CP-41"
  invites a further question ("…and is that done?").
- "Blocks 3" on the other end reads naturally. Its counterpart, "Is depended on by 3", does not.
- It keeps "blocked" a word about **work**, never about a person. The
  needs-you vocabulary is "Needs you", "waiting on", "asks", which keeps
  the two from blurring.

### Resolved

A blocker is **resolved when its status is `done`**, whether that came
from a merge (`pull requests merged`, `delivery/workflow.go:1152`), from
"Mark done" on work with nothing to merge (`api/api.go:939`), or from an
escalation's "done" (`workflow.go:1273`). The rule is `done` rather than
"its pull request merged" so that a task with no code to merge still
releases what waits on it.

- **`failed` and `aborted` keep blocking.** The work the blocked task
  needed did not land, so starting it anyway would be guessing. The page
  says so (screen *failed-blocker*): "CP-38 failed. It keeps blocking until
  someone picks it back up, removes the relation, or starts this anyway."
  Picking it back up (Resume / Try again / Start over) returns it to
  `running`, and it keeps blocking until it is done.
- **In review and ready to merge still block.** The code is not on `main`
  yet, so an agent started now would branch from a `main` without it.
- A blocker that is done stays in the Relations list, settled, with its
  status mark showing Done. The board's chip and counts list only
  **open** blockers.

### Cycles and self

Both are refused at write time, in the same transaction as the insert:

- `from_task = to_task` is a `CHECK`.
- A cycle is refused by a recursive walk from `to_task` along `blocks` edges
  looking for `from_task`, run under `SELECT … FOR UPDATE` on the
  organisation's relation lock (`pg_advisory_xact_lock(hashtext('task_relations:' || org))`),
  so two concurrent inserts can't each close half a loop. Chains are short
  (tens of edges), so the walk is cheap. The refusal names the loop:
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

## Gating: every start path

"Start" here means anything that moves a not-started task (`received`,
`intake`, `awaiting_confirmation`, `queued`: `notStarted` in
`orchestrator/internal/api/chat.go:251`) toward running an agent on it.
Every path found in the code is listed below, with where its guard sits.
**One function owns the rule**: `delivery.RefuseBlockedTx(ctx, tx, taskID)
error`. It locks the task row, reads the task's open `blocks` edges whose
`from` is not `done`, and returns a `Refusal` (`fail(409, "blocked", …)`
from the API) whose words are the same on every path:

> CP-44 is blocked by CP-41 (in review) and CP-43 (in progress): it can
> start once they are done.

| # | path | where it starts work today | guard |
|---|---|---|---|
| 1 | **Deliver** button (task header, `StartChoice`), and **Create and deliver** in the new-task dialog | web `TaskScreen.tsx:263`, `TaskDialog.tsx:291` → backend `deliverTask` `apps/control-plane/src/api/routes/work.ts:190` → orchestrator `deliver` `orchestrator/internal/api/api.go:270`, `Workflow.Start` at `api.go:306` | `RefuseBlockedTx` inside the `InOrg` at `api.go:281`, before `NameOnlyRepository`. For "Create and deliver", the create succeeds and the deliver is refused, so the dialog says the task was made but not started (its `footerProblem`) |
| 2 | **Talk it through** (button and `StartChoice`) | web `TaskScreen.tsx:278` → `talkTask` `work.ts:229` → `talk` `chat.go:69` → `converse` → `decideFor` `chat.go:211` → `talkThrough` `chat.go:262`, `Workflow.StartTx` at `chat.go:276` | in `talkThrough`, before `StartTx` |
| 3 | **A first message in an unstarted task's Chat**. It is Talk it through without the button | `chat` `chat.go:31` → `decideFor` → `talkThrough` | the same guard as 2. The message is refused with the reason, and the composer keeps the words (`onSubmit` resolving `false`) |
| 4 | **The conductor's first `start_phase implement`** (or `decide next` at `PointStart`) on a conducted delivery | agent tool `start_phase` `orchestrator/internal/agenttools/conductor.go:279` → `delivery.ConductStartPhase` `orchestrator/internal/delivery/conduct.go:185`; `decide` → `ConductDecide` `conduct.go:336`; carried out by `implementRun` `delivery/workflow.go:398` | in `ConductStartPhase` and `ConductDecide` when the decision point is `PointStart` (`decisions.go:34`). The conductor gets the refusal as a tool error, and its prompt tells it to tell the person and stop. Paths 2 and 3 already refuse to make the delivery, so this guard covers only a relation added *while* the conductor plans (screen *history*: "Ana's conductor added blocked by CP-40") |
| 5 | **The workflow's own `implement` step** (Deliver's first step, and a conducted delivery after its first decision) | `steps.implement` `delivery/workflow.go:380` → `implementRun` `workflow.go:398` → `CreatePhaseRun` | `RefuseBlockedTx` at the top of `implementRun`, **only when no implementer has run on this attempt** (`st.Retries == 0` and no implement Run in `st`). A refusal here doesn't fail the workflow: it parks on a new signal `relations.unblocked` and sets the task back to `queued` with reason "blocked by CP-41". This is the backstop for an edge added between Deliver and the step running |
| 6 | **Start over** (a new attempt) | web `PickUpDialog` → `recoverTask` `work.ts:251` → `recoverTask` `orchestrator/internal/api/recover.go:205` → `startOver` `recover.go:325` | **not guarded.** Start over is a decision about a task that already started. Its blockers were resolved or overridden at its first start, and a blocker that failed later doesn't undo the work. *(Open question 3.)* |
| 7 | **Resume / Try again** | `recoverTask` → `pickUp` `recover.go:294` | not guarded, for the same reason as 6 |
| 8 | **`POST /v1/tasks/:id/runs`** (a bare Run, status `queued`) | backend `createRun` `work.ts:301`, task set `queued` at `work.ts:326`; used by tests (`tests/helpers.py:145`) and `ApiClient.createRun` `apps/web/src/api/client.ts:641` (no screen calls it) | refused with the same 409 in the backend, in the `withOrg` transaction before the insert, by a SQL function `task_blocked_by(task_id)` that both processes call, so the backend need not reimplement the rule. Raw SQL is needed for this: neither `Bun.sql` nor pgx has a query builder for a recursive CTE here, and the repo writes its SQL raw by convention (CONTRIBUTING, *Decisions*) |
| 9 | **Filing a proposal** | `fileProposal` `orchestrator/internal/api/sessions.go:664` → `fileItem` `sessions.go:785` → `delivery.CreateTaskTx` `delivery/filing.go:57` | filing never starts work ("Nothing starts", `ProposalCard`), so there is nothing to gate. Filing does **write** the proposal's relations (see *Propose work*) |
| 10 | **`create_task`** by an agent | `createTask` `agenttools/tools.go:219`, status `received` (`tools.go:256`) | creates, never starts. It may write relations, and nothing is gated |

No auto-start was found: nothing in the orchestrator moves a `received`
or `queued` task to running on its own, because the workflow only begins
from paths 1 to 5. Confirming a plan (`awaiting_confirmation`) isn't
reachable from any route today (`chat.go:253` only lists it), so it has no
path of its own. When it is built, it goes through the same guard.

### Override: "Start anyway"

- **A person may override; an agent may not.** On a blocked task, Deliver
  and Talk it through stay in the header, **disabled, never removed**.
  Their tooltip and `aria-describedby` give the reason, and **Start anyway…**
  (`quiet`) follows them (screen *task*, *task-dark-reason*).
- It opens a `Dialog tone="attention" size="md"` titled "Start CP-44
  anyway?" (screen *override*). The dialog names each open blocker in the
  sidebar's grammar (`RefLead`, title, `StatusMark`, owner face with the
  agent at work) and says what overriding means: "Its agents start from
  `main` as it is now: what they change won't be there." It asks which
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

- **No auto-start (default).** When the last open blocker of a not-started
  task becomes `done`, the task becomes **startable**. It stays in Backlog
  as it was, its chip goes away, and Deliver / Talk it through come back.
  Nothing starts it, because starting is a choice between two ways
  (README, *Starting is a choice*), and the person who filed it may not be
  ready.
- **Told twice, quietly.** The task's Chat gets dude's notice
  (`ChatNotice kind="notice"`): "CP-41 merged — CP-42 can start. Nothing
  started it: Deliver or Talk it through when you're ready." Activity gets
  the system line "CP-41 merged (#431) — CP-42 can start" (screens
  *history-chat*, *history*). It is a notice, not a toast (README: toasts
  are for the outcome of *your* action) and not needs-you (nobody is
  blocked on a person). A browser notification to the task's owner fits
  `docs/design/notifications.md` when that exists: *open question 2*.
- **Where it is emitted**: in `delivery.RecordStatusTx`
  (`delivery/store.go:497`), the one place every status change is recorded.
  On `status = 'done'` it finds, in the same transaction, each task that
  this one blocks and that now has no open blocker, and appends
  `task.unblocked {by: <blocker key>}` on each. A delivery parked at path
  5's `relations.unblocked` signal is signalled there too, but stays parked
  until a person or its conductor acts (*open question 1*).

## The board

Screens: *board*, *board-blocked*, *board-hover*, *board-dark-chiptip*,
compact *board-*-compact*.

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
  "Blocks 3". A blocker that is done shows nothing.
- **No start button on the card.** The brief asked for a disabled start
  affordance on the card. I dropped it for two reasons: the board's rule
  that "a card is a way in, not a handle" (README, *Board*), and the fact
  that starting is a choice between two equal ways that the card has no
  room to offer. The lock chip says what a disabled button would have said,
  and the task page keeps the refused start (with its reason) and Start
  anyway. *Deviation from the brief, flagged.*
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

Screens: *task* (CP-44), *task-dark-reason* (tooltip on the disabled
Deliver), *failed-blocker* (CP-49), *editor*, *cycle*, *override*, phone
*task-phone-**.

- **Header.** Deliver and Talk it through are disabled with the reason
  (tooltip on a focusable wrapper, because a disabled button takes neither
  pointer nor focus). Their `aria-describedby` points at the same sentence,
  and they are followed by **Start anyway…**.
- **Notice** (`Callout tone="neutral"`, where an escalation would sit):
  "**Blocked by CP-41 (in review) and CP-43 (in progress).** It can start
  once they are done; nothing starts it on its own then." A failed blocker
  adds a second line saying it keeps blocking and how to clear it.
  The tone is neutral, not attention: no one is needed.
- **Pipeline** (the empty `StartChoice` slot) becomes an `EmptyState`
  "Waiting on its blockers", whose description notes that a person can
  start it anyway and an agent can't.
- **Relations**, in Overview after the acceptance criteria. It has three
  groups under small-caps heads with their glyph and count (lock **Blocked
  by**, bar-arrow **Blocks**, link **Related**). "Blocked by" says "1 of 2
  open" once one is done. Each row is in `RemovableList`'s grammar:
  `RefLead` (status mark and key), the title, the project if another,
  `StatusMark` in words, the owner's face with the working agent's tile on
  it, and a quiet remove ("Remove blocked by CP-41"). Empty groups are
  left out, and with none at all one line says what relations are for.
  **Add relation** (`quiet`, `plus`) is in the section head.
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

Screens: *history* (CP-42 Activity, the morning CP-41 merged),
*history-chat* (CP-42 Chat), *history-override* (CP-49 Activity).

- **Relation added or removed** is a `TimelineItem`. For a person it shows
  their face and "**Márcio** removed related **WEB-9**". For an agent it
  shows the person's face with the agent's role tile on it (the
  `AuthorLine` rule: an agent works for someone), named "**Ana's
  conductor** added blocked by **CP-40**", with the agent's stated reason
  quoted. When a person files a proposal's relation, the line credits both:
  "**Ana** filed it with blocked by **CP-41**, as **Ana's Brainstorm**
  proposed."
- **Unblocked** is a system line ("CP-41 merged (#431) — CP-42 can start.
  It waits for a person: nothing starts on its own.") plus dude's
  `ChatNotice` in Chat. `TaskHistory` at Chat's head reads "Not started ·
  ready to start · was blocked by CP-41 for 3d".
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
  chrome shade. It has a head ("ORDER · 1 can start · 4 blocked · 2 in
  progress or review · 1 done · 1 failed") and the epic's tasks laid out
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
| `update_task` | `addRelations: [{kind: "blocked_by"\|"blocks"\|"related", task: KEY, why?: string}]`, `removeRelations: [{kind, task}]`. Allowed on the caller's task **at any status** (unlike its text, which is frozen once an implementer starts), because learning mid-run that something else must land first is exactly when this is needed. `why` is shown quoted in the history line |
| `list_tasks` | each task gains `blockedBy: [{key, status}]`, `blocks: [{key, status}]`, `related: [key]` and `startable: boolean` (not started and nothing open in front of it). `text` filtering stays. A new `startable: true` input lists only those |
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
| conductor | yes | `create_task` and `update_task` on its own task, because it plans the task with the person and is the one that learns "this needs CP-40 first" |
| investigator | yes | `create_task` with relations, since it finds follow-ups that wait on the work it is looking at. No `update_task` |
| implementer, fixer, simplifier, reviewer, tester | yes, read only | **no**. An implementer that finds out mid-run that it needs other work says so through `ask_person` or `create_task` (for the missing work), and the conductor or the person relates them. Letting an implementer block its own task would let it bail out of hard work by declaring a dependency |

**No agent can override.** The override routes check for a human
principal, and no tool has an `override` parameter.

### What an agent is told when it tries

A conductor's `start_phase implement` / `decide next` at the start point,
on a blocked task:

> refused: CP-44 is blocked by CP-41 (in review) and CP-43 (in progress):
> it can start once they are done. You can't start it anyway; a person
> can. Tell the person, and end your turn: you are woken when it can start.

Its prompt (`delivery/prompts.go`, conductor) gains one line: "A task
blocked by another starts only once that one is done; if a start is refused
as blocked, tell the person what it waits on and stop." `steer` on a
blocked task's Runs is not affected, because a blocked task has no live
phase Run unless a person started it anyway.

The conductor is woken by a new wake reason `unblocked` (`conductor_wakes`,
`delivery/wakes.go`) when its task becomes startable, so it can tell the
person in Chat. It still doesn't start anything until the person agrees,
as before.

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
```

`task_blocked_by(task text) RETURNS TABLE(key text, status task_status)` is
a `STABLE` SQL function returning the open blockers. Both processes call it
(path 8 in the backend, `RefuseBlockedTx` in the orchestrator), so the rule
lives in one place. The cycle check is
`task_relation_would_cycle(from text, to text) RETURNS text[]`, a recursive
CTE returning the loop's keys or NULL, called after taking the advisory
lock above.

### Routes

- `GET /v1/tasks/:id/relations` returns `{blockedBy, blocks, related}`, each
  `[{task: NavTaskRef, createdBy, createdAt, why}]`.
- `POST /v1/tasks/:id/relations {kind: "blocked_by"|"blocks"|"related", task}`
  returns 201, 409 `cycle {loop: [keys]}`, 409 `duplicate`, or 400 `self`.
- `DELETE /v1/tasks/:id/relations/:kind/:other`.
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
| `task.relation_removed` | `{kind, from, to}` on both | person or agent Run |
| `task.unblocked` | `{by: key, blockers: []}` (the blocker that resolved last, and that none is left) | system, from `RecordStatusTx` |
| `task.start_refused` | `{path: "deliver"\|"talk"\|"chat"\|"start_phase"\|"implement"\|"create_run", blockers}` | whoever tried; makes refusals auditable, never shown as a line |
| `task.start_overridden` | `{blockers: [{key, status}], how, note}` | person |

The live stream (`relations.changed`, browser-only) tells a board or task
page to read again, as `servers.changed` does.

### View model

- `NavTask` (`packages/design-system/src/util/navModel.ts:53`) gains
  `blockedBy?: ReadonlyArray<NavRelated>` (open only),
  `blocks?: ReadonlyArray<NavRelated>` (not-started dependants only), and
  `startable?: boolean`, where `NavRelated = { key, title, status, project? }`.
  `/v1/navigation` fills them with one join per project.
- `BoardCard` (`boardModel.ts:60`) gains `blocked: boolean`, derived, plus
  `boardFilter(columns, "all"|"startable"|"blocked")`.
- `dependencySteps(epic): NavTask[][]` is new in `boardModel.ts`, pure and
  tested, and it puts a task whose in-epic edges form a cycle (impossible,
  but defended against) in the last step.

## Design-system changes

Everything below is built in the mockup folder at DS quality (tokens only,
both themes, both densities, accessible names). Nothing in
`packages/design-system` changes in this branch. Each piece gets a gallery
`Block` with every state in both themes (`Panes mode`).

### `Icon`: `lock`, `blocks`

- Paths on the 16 grid, stroked by `Icon`'s rule. `lock` is a padlock and
  `blocks` is an arrow into a bar. `link` already exists for Related.
- README, *Icons*: "`lock` marks a task that can't start until another is
  done, `blocks` one that holds another up, `link` a relation that gates
  nothing. They take the ink of their words, never a tone."
- Gallery: the icon grid in *Tokens*, which lists `ICON_NAMES`.

### `RelationChip`

```ts
interface RelationChipProps {
  kind: "blocked-by" | "blocks";
  tasks: ReadonlyArray<{ key: string; title: string; status: TaskStatus; project?: string }>; // open edges only
  className?: string;
}
export function relationWords(kind, tasks): string; // "Blocked by CP-41 (in review) and CP-43 (in progress)"
```

- README, *Board*: "A task that can't start says what it waits on: a
  `RelationChip` 'Blocked by' (the lock on the active wash, one blocker by
  status mark and key, several counted), and a task that holds others up
  the quieter 'Blocks N' (muted, no wash). Neither is a tone: blocked waits
  on work, not a person, and never takes the attention wash. The chip is
  named in words (`relationWords`) and is part of the card's name. A card
  that has started shows no 'Blocked by'; a blocker that is done shows
  nothing."
- Gallery, *Board · Relations*: blocked by one (each status: in review,
  running, failed, another project), blocked by 3, blocks 1, blocks 4, both
  on one card, the tooltip open, a narrow card where the key truncates
  last, and grayscale.

### `Board`: `cardMarks`, `highlight`, the filter

```ts
interface BoardProps {
  // …
  /** Under the title: what the card waits on and holds up. The board draws `RelationChip`s from `NavTask.blockedBy/blocks` when absent. */
  cardMarks?: (card: BoardCard) => ReactNode;
  /** Keys kept lit, the rest dimmed; null for none. Hover and focus set it when uncontrolled. */
  highlight?: ReadonlySet<string> | null;
  onHighlightChange?: (key: string | null) => void;
}
```

- README, *Board*: "Hover or focus on a card lights its relations: its
  own accent bar, theirs on the active wash, the rest at 35% opacity,
  and a polite live region saying it in words. Nothing is added to the
  board's layout, so the cards don't move under the pointer. 'Can start' and
  'Blocked' are a `Segmented` filter in the header (counts in the labels),
  not a triage bucket: blocked needs nobody."
- `test/borders.test.ts` needs no entry: the lit card reuses `selected`'s
  inset bar.
- Gallery, *Board*: the relations fixture lit on a blocker, on a blocked
  task, and on a card with none; the filter on each value; swimlanes with a
  chain across two epics.

### `RelationList` (with `RelationRow`)

```ts
interface RelationListProps {
  groups: ReadonlyArray<{ kind: "blocked-by" | "blocks" | "related"; rows: ReadonlyArray<RelationRowData> }>;
  onRemove?: (kind, key) => void;
  onAdd?: () => void;
  onOpen?: (key: string) => void;
  adding?: ReactNode; // the RelationPicker, under the head
}
interface RelationRowData { key; title; status; owner: Person; agent?: AgentRole; project?: string; settled?: boolean }
```

- README, *Management*: "A task's **Relations** are three groups under
  small-caps heads with their glyph and count — Blocked by ('1 of 2 open'
  once one is done), Blocks, Related — in `RemovableList`'s rows: status
  mark and key, title, project if another, status in words, the owner's
  face with the working agent on it, remove. An empty group is left out."
- Gallery: all three groups, a settled blocker, a cross-project row, a
  long title at 390px, read only (no `onRemove`), and empty.

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
  onStartAnyway?: () => void; // a person only; absent for a reader
}
```

- README, *A task's Chat*, after *Starting is a choice*: "A blocked task
  keeps its ways to start, **disabled, never gone**: the reason in their
  tooltip and description, and **Start anyway…** after them, which opens a
  `Dialog tone="attention"` naming each blocker and what starting now
  means. Keep waiting has the focus. An agent has no way to override."
- Gallery: one blocker, two, a failed blocker, and without
  `onStartAnyway`. The override dialog is shown in both start ways.

### `DependencyStrip`

```ts
interface DependencyStripProps {
  steps: ReadonlyArray<ReadonlyArray<StripTask>>; // from boardModel's dependencySteps
  counts: { startable; blocked; working; done; failed };
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
- Auto-start on unblock (open question 1).
- Relations between epics (an epic blocks an epic). Edges are task to task,
  and an epic's order is derived from its tasks.
- Drag to relate on the board. Nothing drags (README).

## Open questions for Márcio

1. **Auto-start on unblock.** The proposal is no by default. Should a task
   carry an opt-in "Start when unblocked (Deliver / Talk it through)",
   set by a person, so that a chain can run overnight? If yes, path 5's
   parked delivery is the natural place for it: it would resume on
   `relations.unblocked`.
2. **Notify on unblock.** Is a Chat notice and an Activity line enough, or
   should the owner get a browser notification (the "Telling a person
   something waits on them" work) when their task becomes startable?
3. **Start over / Try again on a task whose blocker has since failed or
   been reopened.** The proposal doesn't gate these, because the task
   already started. Is that right?
4. **"Done" or "merged"?** Resolved is `done` (which includes "Mark done"
   with nothing to merge). Should a *Mark done* blocker release its
   dependants, or only a merge?
5. **Who may remove a relation?** The proposal allows any member, recorded
   in history. Should removing a "blocked by" on someone else's task be the
   owner's only, as editing its text is?
6. **Cross-project chips.** The chip names another project only in its
   tooltip. Should the card say "Blocked by WEB-12" with the project face?
7. **The card's start affordance.** I dropped it (see *The board*). Do you
   want it back?
