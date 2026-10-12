# An agent asks for a larger machine

**Status: mockup (2026-10-12).** Nothing is built. Asked for by Márcio:
"We want to add a tool so that an agent can ask to be restarted on a
larger machine, and that persists for all runs of this agent in this task.
Respect and update the design system, and give me mockups before we build
anything."

Mockup: `docs/design/mockups/bigmachine/index.html` opens from disk and is
interactive. Its source is in `apps/web/mockups/bigmachine`. Build it from
`apps/web` with `MOCKUP=bigmachine bunx vite build --config
mockups/vite.config.ts`. The screens are in
`docs/design/mockups/bigmachine/screens/`. URL parameters pick a state:
`?s=ask-fresh&theme=light&density=compact` (`&bar=0` hides the switcher).

## The problem

Two cases from production:

- **An implementer on Standard (2 CPUs · 8 GiB)** ran the full backend jest
  suite with `--maxWorkers 4`. It was OOM-killed (exit 137). The agent
  called it "load flakiness" and carried on.
- **Testers peaked at 10.3 GiB (p90 9.0) on 8 GiB**, without starting any
  stack.

The agent is usually the first to know it is short of memory: it sees the
`Killed`, the exit 137, DuckDB's "Out of Memory Error". It has no way to say
so. Today a person must notice, change the role's `machineSize` for the
whole project, and wait for a resume or a new Run. That change reaches
every task, not the one that needed it.

## The tool

`request_machine`, in `orchestrator/internal/agenttools`, following
`request_repository`:

```go
type requestMachineIn struct {
    Size   string `json:"size"   jsonschema:"one of the organisation's sizes larger than yours, by name (Large)"`
    Reason string `json:"reason" jsonschema:"what ran out: the command, its exit or error, the memory peak, the workers or threads it used"`
}
type requestMachineOut struct {
    RequestID string `json:"requestId"`
    Status    string `json:"status"` // pending | approved | declined
    Next      string `json:"next"`
}
```

There is no `wait` flag: **a machine request always ends the turn.**

- Approved in place, the Run is parked and resumed on the new size. The
  turn is over anyway.
- Approved across pools, the Run is replaced.
- Approved automatically, the move starts at once.

So the tool says "end your turn now; you are resumed on the size, or told
it was declined", and the Run is **waiting** (`runs.waiting_since`) like an
open `ask_person`. It gets the same grace period, then is parked
(`dude_pause = 'person'`). A request auto-approved within the ceiling
skips the wait: the park is the move's own (`dude_pause = 'machine'`).

**Who has it:** every role that runs code. That is the implementer (and so
the fixer, which runs as the implementer in agenttools), `qa_browser` (the
tester), the simplifier, the investigator and the brainstorm. The reviewer
does not have it: it reads code and runs nothing heavy, and a review is
short enough to rerun. The conductor gets a variant (below).

**CLI:** `dude machine request SIZE --reason "…"` prints the tool's JSON.
`dude machine sizes` lists the organisation's sizes, marking the agent's own
and the larger ones (`list_machine_sizes`, a read tool).

**Refusals**, each in words the agent can act on:

| Case | Answer |
|---|---|
| Size not one of the organisation's | "no size named XXL; larger than yours: Large (4 · 16), XL (8 · 32)" |
| Not larger than its own | "you run on Standard (2 CPUs · 8 GiB); ask for one larger: Large, XL". "Larger" means strictly more memory, or the same memory and more CPUs. |
| Empty reason, or longer than 2000 characters | "say what ran out: the command, its error or exit code, the memory peak" |
| One already pending | "you already asked for Large at 14:02; it waits on Márcio" |
| Past the limit per Run (3, `machineRequestsPerRun`) | "this Run has asked 3 times; carry on, or ask a person with ask_person" |
| The organisation or project has the setting Off | The tool is not listed at all, so there is nothing to refuse. |
| The size's pool is gone from lux (`machineFit` → `gone`) | "Large's pool no longer exists in lux; ask for another size" |

**Across pools**, the tool's answer says what will be lost before it is
decided. It reads the Run's last `run_diffs` snapshot:

> XL is in another pool: approving starts a fresh implementer from your
> branch's head. Uncommitted: src/export/stream.ts (+42 −7). Commit and push
> what you want kept, then end your turn.

The request is still made. The card shows the same.

**The conductor** gets `request_machine` with a `run` argument: a phase Run
of its task, alongside `restart_run`. It is only for a Run the delivery
waits on or that is live, and it goes through the same approval. Its reason
is its own words ("the tester was OOM-killed twice in the last hour"). It
asks for the role, not for itself: the conductor never runs code. The
conductor's request is "on behalf of" the Run in every record.

## Approval: a setting

**"Agents may ask for a larger machine"**, on **Machines** (organisation and
project). It is layered organisation → project like every role setting:

- **Off.** The tool is not listed.
- **Ask a person** (the default).
  - In a task, the task's owner decides, in its Chat, as a repository
    request (`QuestionCard kind="request"`'s grammar). A task with no
    owner is anyone's, as a repository request is today.
  - In a brainstorm session, its owner decides. Other members see "Waiting
    for Ana to decide · Only Ana can decide this one: it's their session".
- **Automatically, up to <size>.** A request for that size or a smaller
  one, **in the same pool**, is approved at once. A larger one, or **any
  request that would cross pools**, asks a person. A fresh Run loses work,
  and that is never automatic. The ceiling is a size id and follows the
  size's rename. If the size is removed, the removal (`removeSize` in
  `apps/control-plane/src/api/routes/machines.ts`) rewrites the
  ceiling to its replacement, as it does `machineSize`, or sets the mode
  back to "Ask a person" when the replacement is "follow the default".

**Not per role.** One setting per organisation or project keeps the page
short. Per role would mean seven more selects on each role's page for a
choice nobody has asked to vary. If it is needed later, the same `Setting`
goes into `agent_models[role]` with no change to the tool. See *Open
questions*.

**Where it lives, against agent capabilities.** The capabilities design
(`github/design/agent-capabilities`, `docs/design/agent-capabilities.md`)
puts each of dude's acting tools behind a per-role switch under **Acts in
dude**. `request_machine` belongs there too, as a capability, **Ask for a
larger machine**, on the roles listed above. The two settings stay apart:

- **The capability** says whether a role is offered the tool. It is on by
  default for the roles above, and a project can turn it off for one role.
- **The approval mode** on Machines says who decides and how far.

Widening does not apply. Asking is never dangerous, because a person or a
ceiling decides. The ceiling is the cost guard, and it lives with the sizes
it names. Until capabilities ships, the approval's **Off** is the only off
switch.

### The card

The request card (`MachineRequestCard`, screens 1, 2 and 9) shows:

- the **size change** (`SizeChange`): the names, then CPUs, memory and disk
  as "2 → 4" with the difference, and the pool when it changes;
- the **memory peak** dude saw, drawn against the new limit with a tick at
  the old one ("peak 7.9 of 8 GiB");
- the **price per hour** before and after, with the difference;
- the agent's **reason**, quoted, so its words cannot pass for the card's;
- across pools, a **danger `Callout`**: what is lost (the conversation,
  the uncommitted files with their counts), what is kept (the pushed
  commit), and that the new Run starts from the branch's head with the
  reason as its note. While uncommitted work exists, the card offers
  **Ask it to push first**. That answers the agent "push your work, then
  end your turn" and keeps the request open, so the person approves after
  the push. With nothing uncommitted, the Callout is attention, not danger;
- a **note** to the agent (optional, a `Textarea`), then **Approve Large**
  (attention fill, as a question's Answer) and **Decline**. A line beside
  them says what Approve does: "It moves now, keeping its conversation and
  uncommitted work. Later implementer Runs in this task run on Large too."

**Where the peak comes from.** lux does not report a memory peak today.
`lux.Usage` has `cpuSeconds` and network bytes only. Of memory it reports
`memoryLimit`, the container's limit. dude has no peak to show until lux
adds one: a per-placement `memoryPeakBytes` (cgroup `memory.peak`) on
`GET /v1/runs/{id}` and in `usage`, and `oomKills` from `memory.events`.
The mockup shows the peak as it would be. Until lux has it, the card draws
no meter (as `FitBar` draws none for an unknown share), and the agent's
reason is the evidence. This is a lux request: see *Open questions*.

**Where the price comes from.** dude has one machine rate,
`orchestrator.machine_usd_per_hour` (`DUDE_MACHINE_USD_PER_HOUR`, default
$0.20), recorded on each Run. It is per host-hour, not per size, so it
cannot tell Large from Standard. lux's cost plugins price a Run after the
fact (`GET /v1/runs/{id}/cost`, family `compute`), not a size ahead of it.
**The price row shows only when the size has a price.** The proposal adds
`machine_sizes.usd_per_hour numeric NULL`, set by an admin in the size
dialog. Unset, the card shows no price row, never "$0.00". The mockup's
prices ($0.12 Standard, $0.24 Large, $0.52 XL) are invented.

## Applying it: two paths

**1. The same pool: resized in place.** This is the default, and it covers
every request between the default pool's sizes. It is a "restart" only in
lux's sense: the container stops and starts, and nothing of the agent's is
lost.

- The approval writes the task layer (below), then parks the Run with a new
  `dude_pause = 'machine'`. That is `'repository'`'s mechanism: stop a
  moment so the resume can apply something. The Run resumes at once.
- The resume goes through `phases.Syncer.resume`, where `PlanResize`
  compares `runs.machine` with what `ForRole` resolves now. That is now the
  task's size, so `ResumeSized` sends `resources`. lux applies CPUs and
  memory up, and a larger disk always.
- The agent keeps its conversation and its uncommitted work. Its next
  message is dude's: "Márcio approved Large. You now run on Large (4 CPUs ·
  16 GiB · 40 GiB). Márcio: “…”." After that comes the turn it was in, or
  "carry on".
- `run.resized` records what lux applied. If lux kept less than asked (a
  409 under way, or a 422), the record's note says so, and the agent is
  told the truth: "You asked for Large; lux kept you on Standard: <why>".
  The task layer stays, so the next Run gets Large.

**2. Another pool: a fresh Run in the same slot.** lux binds a Run to its
pool at submit, so a resume cannot move it (`PlanResize`: "a stopped Run
cannot change pools").

- `RestartRunTx` gains `Machine string` beside `Tier`. It aborts the Run
  and creates its replacement in the same slot, from the task's head, with
  the note: "Restarted on XL (8 CPUs · 32 GiB), which <who> approved for
  your role in this task. Your predecessor asked for it: “<reason>”.
  <note>". The new Run's `ForRole` resolves to the task layer anyway, so
  `Machine` is the layer's id, passed through to say so in the note.
- **The uncommitted work is lost**, and so is the conversation. That is why
  the card warns, why Ask it to push first exists, and why this path is
  never automatic.
- **Only Runs `RestartRunTx` can restart** take this path: phase Runs the
  delivery waits on. A brainstorm session's Run, or a conductor, is never
  restarted this way. For those, the pool-crossing size is refused at the
  tool ("XL is in another pool; a session can move only within its pool:
  Large").

**3. Sessions.** A brainstorm session's Run is resized in place, the same
way, through `phases/session.go`'s resume. That already resolves the
session's size, and will read the session's layer (below). Since #112, a
session already knows how to "take over with a fresh agent and the
conversation". That would be the way across pools for a session. It is
left out of this proposal: see *Open questions*.

**Why two paths and not always restart.** A restart throws away the
agent's context and uncommitted work, which is exactly what an agent
mid-task wants to keep. In place is cheap: one park/resume, measured at
tens of seconds (`run.resume.timed`). It is lossless, and it already
exists (lux#51, `PlanResize`/`ResumeSized`). Restart is kept for the one
case lux cannot do in place.

### The move, on screen

The move reuses the agent-startup design (`github/design/agent-startup`,
`StartProgress`):

- The row sits where the agent's next turn will be, with the verb **"Moving
  to Large"** and the stages.
- The stages are the startup design's, led by the park: "Stopping it,
  keeping its work", "Waiting for a machine", "Restoring its workspace",
  "Starting the container", "Starting the agent". The `image` and
  `repositories` stages do not occur on a resume, so they are not drawn.
- The run line in Chat (`ChatRunLine`) reads "Moving to Large" as its
  status label.
- When the agent is up, the row folds into the existing resume line:
  "The implementer moved to Large in 41s, with its conversation and
  uncommitted work." This is `ChatNotice kind="unparked"`, with the stages'
  times in its tooltip, as the startup design does for a resume.

A fresh Run's start is the startup design's "Starting" row, unchanged.

## Persistence: the task layer

**Shape.** `tasks.agent_machines jsonb NOT NULL DEFAULT '{}'`, keyed by
settings role:

```json
{
  "implementer": {
    "sizeId": "ms_large",
    "requestId": "mreq_01J…",
    "askedBy": "run_7f3k",
    "approvedBy": "u_marcio",
    "auto": false,
    "at": "2026-10-12T14:05:11Z"
  }
}
```

`approvedBy` is null and `auto` is true when the ceiling approved it.
`askedBy` is the conductor's Run when the conductor asked, with
`onBehalfOf` naming the phase Run.

- The key is the settings role (`SETTINGS_ROLES`), not the agent role, so a
  fixer raised on its own is `fixer`.
- The `sizeId` references `machine_sizes.id` logically. A removed size is
  rewritten by `removeSize` to the replacement, or the key is dropped
  when the replacement is "follow the default", in the same transaction as
  the organisation's and projects' layers.
- A settings change naming a size holds that row `FOR SHARE`, so the
  approval does too.

**Resolution.** `delivery.Sizes.ForRole(role, task, project, org)` resolves
task → project → organisation, a field at a time as today, and the fixer
still follows the implementer. `From` gains `"task"`. The chain for the
fixer is `fixer@task, fixer@project, fixer@org, implementer@task,
implementer@project, implementer@org`.

**Later Runs.** Every later Run of that role in this task runs on the
raised size: fix rounds, the next attempt (Start over keeps the task, so it
keeps the layer), Try again, and a restart. That covers every Run that
reads `ForRole` with the task's id. It applies from the next Run or the
next resume; a running Run is moved only by its own approval.

**The fixer follows the implementer's raise.** The fixer already follows
the implementer when it names no size of its own (`modelFallback`). A
raise is the implementer's role saying "this task needs more", and the
fixer reruns the same suite. So `implementer@task` reaches the fixer
unless the fixer names a size at some layer. If a project names the fixer
"Small", the project's fixer wins over the implementer's raise. This
follows the existing chain, and the screen says why ("Follows the
implementer: it names no size of its own"). The reverse does not hold: a
fixer's raise is `fixer@task` and does not reach the implementer.

**Sessions.** `sessions.agent_machine jsonb NULL`: the same object without
a role key (a session has one agent). `phases/session.go`'s size
resolution becomes session → organisation. `From` gains `"session"`.

**The Run's record.** `runs.machine.from` gains `"task"` and `"session"`,
plus `raised: {requestId, askedBy, approvedBy, auto, at}`, copied from the
layer when the size came from it. The chip's tooltip reads it, so history
stays true after a Reset.

**Visible and reversible.**

- **Task Overview** (screen 7): `MachineRaises` in the aside, beside the
  pull request. It has one row per raised role: "Implementer runs on Large
  4 CPUs · 16 GiB", then "Asked by run_7f3k (jest OOM-killed at 7.9 of 8
  GiB) · approved by Márcio, 1h ago", then **Reset**. A fixer that follows
  shows its own row with no Reset.
  - Reset drops the key. Later Runs go back to the settings' size. A Run
    running now keeps its size until it ends (the tooltip says so); it is
    not resized down mid-turn.
  - Who may Reset: whoever may decide a request (the owner, or anyone on
    an unowned task) and admins.
- **The pipeline**: each step's meta shows its machine, "Large ↑", with the
  reason in its title.
- **Run header** (screen 6): `MachineChip` with `raised`: "Large 4 CPUs ·
  16 GiB · 40 GiB ↑ raised for this task". The tooltip says who asked, why,
  who approved, that the fixer follows the implementer, the settings' size,
  and where Reset is.
- **Settings** (screen 8): **Raised now** under the approval setting lists
  every task (and session, to admins, as "A session" since a session's name
  never leaves it) with a raise, with Reset. An admin can see what a ceiling
  is costing.

## Data, API and migration

One migration, `111_machine_requests.sql`:

```sql
CREATE TYPE machine_request_status AS ENUM ('pending', 'approved', 'declined', 'cancelled');

CREATE TABLE machine_requests (
  id              text PRIMARY KEY,                         -- mreq_…
  organization_id text NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  task_id         text REFERENCES tasks(id) ON DELETE CASCADE,
  session_id      text REFERENCES sessions(id) ON DELETE CASCADE,
  run_id          text NOT NULL REFERENCES runs(id) ON DELETE CASCADE,  -- the Run to move
  asked_by_run_id text NOT NULL REFERENCES runs(id),        -- itself, or the conductor's
  role            text NOT NULL,                            -- settings role
  from_size       jsonb NOT NULL,                           -- the runs.machine it had
  to_size_id      text NOT NULL REFERENCES machine_sizes(id),
  other_pool      boolean NOT NULL,
  reason          text NOT NULL CHECK (length(reason) BETWEEN 1 AND 2000),
  memory_peak_mib integer,                                  -- lux's, when it reports one
  status          machine_request_status NOT NULL DEFAULT 'pending',
  auto            boolean NOT NULL DEFAULT false,
  decided_by      text,
  decided_at      timestamptz,
  note            text,
  created_at      timestamptz NOT NULL DEFAULT now(),
  CHECK ((task_id IS NULL) <> (session_id IS NULL))
);
CREATE UNIQUE INDEX machine_requests_one_pending ON machine_requests (run_id) WHERE status = 'pending';

ALTER TABLE tasks    ADD COLUMN agent_machines jsonb NOT NULL DEFAULT '{}';
ALTER TABLE sessions ADD COLUMN agent_machine  jsonb;
ALTER TABLE organizations ADD COLUMN machine_requests jsonb NOT NULL DEFAULT '{"mode":"ask"}';
ALTER TABLE projects      ADD COLUMN machine_requests jsonb;   -- null: the organisation's
ALTER TABLE machine_sizes ADD COLUMN usd_per_hour numeric CHECK (usd_per_hour >= 0);
ALTER TABLE runs DROP CONSTRAINT runs_dude_pause_check;
ALTER TABLE runs ADD CONSTRAINT runs_dude_pause_check
  CHECK (dude_pause IN ('repository', 'person', 'idle', 'unused', 'conductor', 'session', 'machine'));
```

- RLS, grants, and a cancel-on-end trigger, as `020_repository_requests.sql`
  does: asks die with their Run.
- The `machine_requests` setting is `{"mode": "off" | "ask" | "auto",
  "upTo": "<sizeId>"}`. The partial unique index is the one-pending rule.
- Every new column is catalog-only (a constant default), as 109's are.

**Routes.**

- `POST /v1/machine-requests/:id/decision {approve, note?}`, mirroring
  `decideRepositoryRequest`. The backend forwards it to the orchestrator
  (`/internal/machine-requests/:id/decide`), which applies it in one
  transaction: the request, the layer, then the park or the restart.
  - 409 if the Run ended ("the run ended").
  - 409 if another pool and the Run is no longer one the delivery waits on.
  - 403 if not the owner.
- `POST /v1/machine-requests/:id/ask-to-push`.
- `DELETE /v1/tasks/:id/agent-machines/:role` and
  `DELETE /v1/sessions/:id/agent-machine` (Reset).
- `GET /v1/settings` and the project's settings carry `machineRequests` as
  a `Setting` with its source, like `machineSize`.

The task detail carries `agentMachines` with names resolved. The Run
detail's `machine` carries `from` and `raised`.

**The web.** `conversation.ts` folds `machine.requested` and
`machine.decided` into a `MachineRequestTurn`, as `RepositoryRequestTurn`.
`ChatSection` shows the conductor's own requests in the task's Chat, and
the phase Run's request appears there too: the task's owner decides in
Chat, so a phase Run's request is projected into the task Chat. That is
the one place it is decided, with a link to the Run.

## Ledger events

Add these to `packages/domain/src/events/types.ts` with Go constants:

- **`machine.requested`**, on the Run moved: `{ requestId, role, from:
  {sizeId, name, cpus, memoryMiB, diskGiB, pool}, to: {…}, otherPool,
  reason, memoryPeakMiB?, askedBy: runId, onBehalfOf?: runId }`.
- **`machine.decided`**: `{ requestId, approved, auto, by?: personId, note?,
  apply: "resize" | "restart" | null }`. `by` is absent when auto.
- **`machine.reset`**, on the task or session: `{ role?, sizeId, by }`.
- **`run.resized`** and **`run.restarted`** as today. `run.restarted` gains
  `machine?: sizeId` beside `tier?`.

ChatNotice in the task's Chat (or the session) for each:

| Event | Notice |
|---|---|
| auto `machine.decided` | "The implementer asked for Large (4 CPUs · 16 GiB): <reason's first line>. billing lets agents go up to Large, so it moves now, and later implementer and fixer Runs in this task run on Large." |
| approved | "Márcio approved Large for the implementer. It is moving now, with its conversation and uncommitted work; later implementer and fixer Runs in this task run on Large." |
| declined | "Told the implementer: declined, with Márcio's note. It carries on on Standard." |
| `run.resized` after a move | "The implementer moved to Large in 41s, with its conversation and uncommitted work." (`kind="unparked"`) |
| `run.restarted` across pools | The existing restart notice: "Restarted the implementer on XL: a fresh agent from a41c9e2." |
| `machine.reset` | "Márcio reset the implementer's machine: later Runs run on Standard." |

## The prompt line

In `delivery/prompts.go`, for every role with the tool, after the role's
context:

> You run on Standard (2 CPUs · 8 GiB · 20 GiB). Larger sizes you may ask
> for: Large (4 · 16), XL (8 · 32, another pool: you would start fresh).
> If something you need to run is killed for memory (exit 137, "Killed",
> an out-of-memory error) or is far too slow for its CPUs, do not call it
> flaky: run it again with fewer workers if that is enough, and otherwise
> ask with `dude machine request SIZE --reason "…"`, saying what ran out.
> Commit and push first if the size is in another pool.

- When raised: "You run on Large (4 CPUs · 16 GiB · 40 GiB), raised for
  this task after the implementer's jest run ran out of memory." That is
  what the fixer reads (screen 6's prompt).
- With the setting Off, only the first sentence, and the tool is not
  listed.
- The tool's description repeats the sizes, so a harness that drops the
  system prompt's tail still has them.

## Abuse and cost guards

- **One pending per Run**, by a unique index. **At most 3 requests per
  Run**, approved or not (`.limit(machineRequestsPerRun)`). That is enough
  for Standard → Large → XL with one decline.
- **Larger only.** A request for the same or a smaller size is refused,
  and nothing asks to go down. Going down is Reset.
- **A reason is required**, and it is shown quoted on the card. A person
  sees what the agent claims beside what dude measured (the peak, when lux
  has it).
- **The ceiling never crosses pools**, and never exceeds the ceiling. It
  is a size id, so an admin can see exactly what "automatic" allows.
- **The task layer is bounded by the task.** It does not leak into the
  project or other tasks. It ends when the task ends: the column stays for
  history, but no Run reads it.
- **Every raise is listed** in Machines › Raised now, with Reset, so an
  admin can find one a ceiling allowed.
- **Fit.** A size that does not fit a host of its pool (`machineFit`) is
  refused at the tool, as Save refuses it in the size dialog.
- **The conductor** cannot approve. Its `request_machine` goes through the
  same setting and the same person.
- The cost difference is on the card when known, and the Run's cost (model
  plus machine) already says what it came to.

## Design system

Built in `apps/web/mockups/bigmachine/components.tsx` and
`bigmachine.module.css` to the DS rules: tokens only, both themes and
densities, accessible names. The card, chip and panel reuse the
stylesheets of the components they extend (`QuestionCard.module.css`,
`Machines.module.css`, `ServersSummary.module.css`) rather than copying
them. Nothing in `packages/design-system` changes on this branch.

**Reused unchanged:**

- the frame: `ChatTranscript`, `ChatMessage`, `ChatAside`, `ToolCallCard`,
  `ChatRunLine`, `TaskHistory`, `DeciderLine`, `ChatComposer` (`waitingFor`
  while a request waits);
- notices: `ChatNotice` (`notice`, `unparked`);
- headers: `SessionHeader`, `TierChip`, `MachineChip`, `MachineTip`;
- the session: `ScreenHeader`, `SessionTitle`, `SessionRail`,
  `SessionRailBlock`, `SessionPeople`, `SessionFacts`;
- the task page: `StepList`/`StepRow`, `Breadcrumb`, `PersonLine`,
  `StatusMark`;
- settings: `SettingsLayout`, `SettingsSection`, `SettingRow`,
  `SettingSource`, `SettingsMeta`, `SettingsNote`, `ChoiceList`, `Select`
  (with `meta`), `Table`, `TextButton`;
- `Callout`, `Textarea`, `Button`, `Tooltip`, `Badge`.

### New: `SizeChange` (components, *Machines*)

| Prop | Type | |
|---|---|---|
| `from`, `to` | `{ name, cpus, memoryGiB, diskGiB, poolId, pool, usdPerHour }` | The two sizes. |
| `peakGiB` | `number?` | lux's measured peak. Draws the meter; absent, no meter. |
| `showCost` | `boolean?` | The price row, only when both sizes have a price. |
| `compact` | `boolean?` | One line, for a settled request. |

README, under *Machines*:

> - **A change of size is a `SizeChange`**: the two names with an arrow,
>   then CPUs, memory and disk each as "2 → 4" (the new value strong) with
>   the difference, the pool when it changes (with the warning glyph and
>   "another pool: a fresh Run"), and the price per hour when both sizes
>   have one, never "$0.00". With the Run's measured memory peak, a 64px
>   track under the new limit, filled to the peak in the attention tone,
>   with a tick where the old limit was and "peak 7.9 of 8 GiB" beside it.
>   No peak, no track. On the raised shade, square. `compact` is the one
>   line a settled request keeps. Narrower than 420px (a container query)
>   the differences go and the meter takes its own line. Its `aria-label`
>   says the whole change in words.

Gallery: *Machines › Size change*. It shows Standard → Large with a peak,
Standard → XL across pools, Small → Standard with no price and no peak,
and compact. Each is shown in dark, light and compact density, and at
390px.

### Changed: `QuestionCard` — `kind="machine"` and `detail`

| Prop | Type | |
|---|---|---|
| `kind` | `"question" \| "request" \| "machine"` | `machine`: "asks for a larger machine"; its verb is "decide". |
| `detail` | `ReactNode` | Under the header, before the text: the `SizeChange`. |
| `warning` | `ReactNode` | After the text: the cross-pool `Callout`. |
| `note` | `{ label, placeholder, value, onChange }?` | An optional note back with the decision (machine and request kinds). |
| `actions` | `ReactNode` | Replaces the one-click choices with buttons (Approve Large / Decline / Ask it to push first). |
| `decision` | `{ approved, by, at, note? }` | Settled: "✓ Approved" or "✕ Declined" · by Márcio, after 2m 20s, and the note. |

The mockup's `MachineRequestCard` is what these props would compose.

README, under *Human intervention*:

> - **An agent asking for a larger machine** is `QuestionCard
>   kind="machine"`: a request, as loud as a question while it waits on
>   you, and the one amber turn. Its `detail` is the `SizeChange`; its text
>   is the agent's reason, quoted; across pools its `warning` is a danger
>   `Callout` naming what is lost (the conversation, the uncommitted files
>   with their counts) and what is kept (the pushed commit); attention
>   when nothing is uncommitted. Then an optional note and **Approve
>   Large** (the attention fill, as Answer), **Decline** (secondary), and
>   across pools with uncommitted work **Ask it to push first** (quiet).
>   A line beside them says what Approve does and that later Runs of the
>   role run on it too. Someone else's (`waitingOn`) says whom it waits on
>   and offers nothing; a session's is `onlyThey`. Settled, it keeps the
>   change in one line, "Approved · by Márcio, after 2m 20s", and the
>   note.

Gallery: *Human intervention › Machine request*. It shows waiting (same
pool), waiting across pools with uncommitted work, someone else's (a
session), approved, and declined with a note, in both themes and both
densities.

### Changed: `MachineChip` — `raised`

| Prop | Type | |
|---|---|---|
| `raised` | `string?` | "for this task", "for this session": after the spec, the `arrow-up` glyph and "raised …" in secondary ink, and in the accessible name. |

README, under *Machines*, after the `MachineChip` paragraph:

> A machine raised for its task or session says so on the chip:
> `raised="for this task"` adds "↑ raised for this task" after the spec,
> and the tooltip says who asked, why, who approved (or that it was
> automatic, up to which size), the settings' size, and where Reset is.

### New: `MachineRaises` (components, *Machines*)

| Prop | Type | |
|---|---|---|
| `where` | `ReactNode` | After the title, muted: "raised in this task". |
| `raises` | `{ role, label, size, settings, detail, onReset? }[]` | One row each: the role's face, "Implementer runs on **Large** 4 CPUs · 16 GiB", the detail under it, Reset when `onReset`. |

README, under *Machines*:

> - **The machines raised in a task are a `MachineRaises`**, in the
>   Overview's aside beside the pull request, in `ServersSummary`'s
>   grammar on the chrome shade: one row per role, its face, the size
>   strong with its spec muted, who asked and approved beneath, and a
>   quiet **Reset** whose tooltip says later Runs go back to the settings'
>   size and that a Run running now keeps its machine. A role that follows
>   another ("Follows the implementer") has its row and no Reset. With
>   nothing raised the panel is not drawn.

Gallery: *Machines › Raised in a task*: two rows (one with Reset, one
following), and the panel after Reset.

### From the startup design: `StartProgress` with a verb

The mockup's `MoveProgress` is `StartProgress` (startup design) with
`verb="Moving to Large"` and a leading `park` stage ("Stopping it, keeping
its work" / "Stopped, work kept"). No new component is needed if
`StartProgress` lands first: the stage words map gains `park`, and the
README's *Starting* paragraph adds "or **Moving to Large**, for a Run
resized in place, whose stages begin with stopping it".

### `ChatNotice`

No new kind. The decision notices are `kind="notice"`, the finished move
is `kind="unparked"` (its glyph is retry, which reads as "came back"), and
a cross-pool restart is the existing `kind="restart"`.

## Screens

| # | Screen | Files |
|---|---|---|
| 1 | Task Chat: the implementer's request, Standard → Large, OOM reason, peak 7.9 of 8 GiB, price, note, Approve / Decline; composer waiting | `1-ask-{dark,light}.png`, `1-ask-dark-compact.png` |
| 2 | Cross-pool: Standard → XL (pool highmem), the danger Callout with the uncommitted file, Approve: start fresh on XL, Ask it to push first | `2-ask-fresh-{dark,light}.png`, `2-ask-fresh-dark-compact.png` |
| 3 | Approved: the card settled, dude's notice, "Moving to Large" with the stages open, the run line "Moving to Large" | `3-resizing-{dark,light}.png`, `3-resizing-dark-compact.png` |
| 4 | Automatic within the ceiling: two notices, no card | `4-auto-{dark,light}.png`, `4-auto-dark-compact.png` |
| 5 | Declined: settled card with Márcio's note; dude's notice that the agent was told | `5-declined-{dark,light}.png`, `5-declined-dark-compact.png` |
| 6 | The fixer's Run header: "Large · ↑ raised for this task"; the tooltip open | `6-run-{dark,light}.png`, `6-run-dark-compact.png`, `6-run-tip-{dark,light}.png` |
| 7 | Task Overview: the pipeline's machines, Machines in the aside with Reset | `7-task-{dark,light}.png`, `7-task-dark-compact.png` |
| 8 | Acme → Machines: sizes, Larger machines (Ask a person), Raised now; billing → Machines overriding to "Automatically, up to Large" with SettingSource and Reset | `8-settings-org-*.png`, `8-settings-project-*.png` (+ `-dark-compact`) |
| 9 | A brainstorm on Small asks for Standard; Márcio reads, Ana decides | `9-session-{dark,light}.png`, `9-session-dark-compact.png` |
| 10 | Screens 1 and 2 at 390px | `10-phone-ask-{dark,light}.png`, `10-phone-ask-fresh-{dark,light}.png` |

## Open questions

1. **Memory peak from lux.** The card's best evidence is a measured peak,
   and lux reports none (`Usage` has CPU seconds and network bytes). Ask
   lux for a per-placement `memoryPeakBytes` (cgroup `memory.peak`) and
   `oomKills` (`memory.events`). With `oomKills`, dude could also *suggest*
   a request: a notice "the implementer was OOM-killed twice; it may ask
   for a larger machine" without the agent noticing first. Is that wanted?
2. **Prices per size.** The proposal adds `machine_sizes.usd_per_hour`,
   set by hand. Should it come from lux's cost plugin instead (a rate per
   pool or resource), so it cannot drift from what lux bills?
3. **Per role or per organisation/project?** The setting is one per layer.
   Should a project allow automatic raises for the tester and ask for the
   implementer? If so it moves into `agent_models[role]`.
4. **Sessions across pools.** A session could take over with a fresh agent
   on the new pool and the conversation so far (#112's mechanism). That
   keeps the conversation, though not the workspace. Allow it, behind the
   same warning?
5. **Does a raise outlive its attempt?** The proposal keeps it across
   Start over, since the task is the same work. If Start over means "the
   approach was wrong", should it clear the layer?
6. **Reset while running.** Reset applies from the next Run. Should it
   offer "and move the running one back now" (a resize down at its next
   park)?
7. **Who decides on an unowned task.** As a repository request, anyone. Is
   that right for something that costs money, or should it fall to admins?
8. **The investigator and the tester** run outside a delivery's restart
   slots in some flows. Confirm `RestartRunTx` covers them before allowing
   cross-pool requests from those roles; otherwise refuse cross-pool for
   them as for sessions.
9. **Ceiling vs. the capability matrix.** If capabilities ships first,
   should the approval mode be a column in its matrix ("Ask for a larger
   machine: ask / up to Large / off") rather than on Machines?
