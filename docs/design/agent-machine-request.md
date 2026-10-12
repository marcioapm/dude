# An agent moves itself to a larger machine

**Status: mockup.** Nothing is built.

> **Revised 2026-10-12**, to Márcio's decisions. The agent drives the whole
> process and no person is in the loop. `request_machine` applies at once,
> so the approval card, Approve/Decline, the automatic mode and its ceiling
> are gone. Prices (`machine_sizes.usd_per_hour`) and the memory-peak meter
> are gone too: lux reports no peak, and the agent's reason is the evidence.
> The only setting is a per-role switch on the role's page, layered
> organisation → project. It replaces the Machines-page "Larger machines"
> section. Sessions cross pools by #112's fresh-agent takeover. A raise
> survives Start over. Reset moves the running agent back down now, and
> anyone in the project may use it. The move's row is the design system's
> `StartProgress` (agent-startup, being built), not a component of its own.

Asked for by Márcio: "We want to add a tool so that an agent can ask to be
restarted on a larger machine, and that persists for all runs of this agent
in this task."

Mockup: `docs/design/mockups/bigmachine/index.html` opens from disk. Its
source is `apps/web/mockups/bigmachine`; build it from `apps/web` with
`MOCKUP=bigmachine bunx vite build --config mockups/vite.config.ts`. URL
parameters pick a state: `?s=move-pool&theme=light&density=compact`
(`&bar=0` hides the switcher). The screens are in
`docs/design/mockups/bigmachine/screens/` (table at the end).

## The problem

- An implementer on Standard (2 CPUs · 8 GiB) ran the backend jest suite
  with `--maxWorkers 4`. It was OOM-killed (exit 137), and the agent called
  it "load flakiness".
- Testers peaked at 10.3 GiB (p90 9.0) on 8 GiB.

The agent is the first to see the `Killed`, the exit 137 or DuckDB's "Out
of Memory Error", but it has no way to act on it. Today a person has to
notice, then change the role's `machineSize` for the whole project.

## The tool

`request_machine`, in `orchestrator/internal/agenttools`, beside
`request_repository`:

```go
type requestMachineIn struct {
    Size   string `json:"size"   jsonschema:"one of the organisation's sizes larger than yours, by name (Large)"`
    Reason string `json:"reason" jsonschema:"what ran out: the command, its exit or error, the workers or threads it used"`
    Fresh  bool   `json:"fresh,omitempty" jsonschema:"another pool only: be replaced even though your checkout has work you have not pushed, which is lost"`
}
type requestMachineOut struct {
    Size   string `json:"size"`
    Apply  string `json:"apply"` // resize | restart | takeover
    Answer string `json:"answer"`
}
```

**CLI:** `dude machine request SIZE --reason "…" [--fresh]` prints the
tool's JSON. `dude machine sizes` lists the organisation's sizes and marks
the agent's own size, the larger ones and their pools (`list_machine_sizes`,
a read tool, offered with the request).

**It applies at once and ends the turn.** No one decides. The answer says
what happens:

- **Same pool:** "Moved: you run on Large (4 CPUs · 16 GiB · 40 GiB) from
  your next step; your conversation and checkout are kept. Later implementer
  and fixer Runs in this task run on Large too. End your turn now; you are
  resumed on Large."
- **Another pool** (this sentence is also in the tool's description): "XL is
  in another pool: you will be replaced by a fresh implementer starting from
  what you pushed (a41c9e2), with your reason as its note. Nothing unpushed
  was found. End your turn now."

**Refusals.** Each is worded so the agent can act on it:

| Case | Answer |
|---|---|
| Not one of the organisation's sizes | "no size named XXL; larger than yours: Large (4 · 16), XL (8 · 32, another pool)" |
| Not larger than its own | "you run on Standard (2 CPUs · 8 GiB); ask for one larger: Large, XL". Larger means strictly more memory, or the same memory and more CPUs. |
| Already the largest | "You run on XL (8 CPUs · 32 GiB · 80 GiB), Acme's largest size: there is none larger to ask for. Use fewer workers or less data, and say in your work what did not fit." (screen 3) |
| Empty reason, or over 2000 characters | "say what ran out: the command, its error or exit code" |
| A move under way | "you are being moved to Large now; ask again once you are there, if it is still too small" |
| Past the Run's limit (guards) | "this Run has moved 2 times; carry on, or say in your work what did not fit" |
| Another pool, with unpushed work and no `fresh` | below (screen 3) |
| Another pool, and the conductor | "XL is in another pool; the conductor moves only within its pool: Large" |
| The size's pool is gone from lux, or the size fits no host of its pool (`machineFit`) | "XL's pool has no host it fits; ask for another size" |
| The role's switch is off | Nothing to refuse: the tool is not offered (below). |

### Unpushed work across pools: refuse (recommended)

A move across pools replaces the agent, and with no person in the loop the
agent is the only one who can lose work. **The tool refuses while the
checkout has commits not pushed or uncommitted changes, and names them,
unless the agent passes `fresh: true`:**

> XL is in another pool: you would be replaced by a fresh implementer
> starting from what you pushed; commit and push first. Not pushed: 1 commit
> (3f9e0b1 "Sort the export in chunks"), and uncommitted
> src/export/stream.ts (M +42 −7), src/export/sort.test.ts (A +88). Push
> with `dude push`, then ask again; or ask with --fresh to leave them behind.

Why refuse rather than warn:

- The refusal costs one round trip. The agent can always push and ask again.
- A loss can't be undone. A warning in an answer that also says "end your
  turn now" is easy to read past.
- Naming the files turns "commit and push first" into a checklist.
- `--fresh` keeps the choice with the agent ("that experiment is
  throwaway"), and the ledger records it.

The check runs the live diff's script (`phases/livediff.go`'s `diffScript`,
through lux's exec) at the call, plus `git log @{push}..` (or against the
base when nothing was pushed). It does not read the last `run_diffs`
snapshot, which can be seconds old.

**Pushing mid-Run needs `push`** (`dude push`). Today a phase Run's
checkout is pushed by lux only when it finishes (`phases/sync.go`
`finish`). `push` asks lux to push the Run's checkout to its own branch,
`dude/<task>/run-<run>` (`POST /v1/runs/{id}/push`, as `finish` does),
and answers with the commit once lux's `git.push` reports it. It does not
move the task branch, which is the delivery's job. The fresh Run's checkout
starts from that commit (below). The tool is offered with `request_machine`
and has nothing else to do with it.

## The setting: "May ask for a larger machine", per role

It is an on/off on each role's settings page (`RolePage`), next to its
Machine field. It is layered organisation → project, a field at a time,
with `SettingSource` and Reset like the role's other fields (screen 6).

- **Off:** the tool (and `push`, and `list_machine_sizes`) is not offered at
  all, and the prompt does not mention it. The role runs on its Machine.
- **On:** offered, with the prompt line below.

**Defaults**, factory values each organisation can change:

| Role | Default | Why |
|---|---|---|
| Implementer | **on** | It runs the suites, the production OOM case. |
| Fixer | follows the implementer | It reruns the same suite, so it follows the implementer's switch as it follows its machine, unless given its own. |
| Tester (`qa_browser`) | **on** | Testers peaked at 10.3 GiB on 8 GiB: the second production case. |
| Simplifier | **on** | It reruns the suite to show it changed nothing. |
| Brainstorm | **on** (organisation only: a session has no project) | It loads data to answer questions (DuckDB over a parquet export). |
| Reviewer | no setting | It reads and runs nothing heavy, and a review is short enough to rerun. It never gets the tool, so the switch is not on its page. |
| Investigator | no setting yet | No delivery step creates an `investigate` Run (below). The switch appears when one does. |
| Conductor | **off** | Its machine is small, it never runs code, and it can only resize in its own pool. An organisation may turn it on. |

The mockup shows Acme's Implementer **on**, and billing's **Simplifier
overridden off** with "Overridden · Acme: on · Reset". The reviewer has no
switch, so the override example is the simplifier.

**Not a setting:** the guards (below).

**When capabilities ships** (`github/design/agent-capabilities`), this
switch becomes a row of its **Reach** group, "Larger machine", and a column
of its matrix, with the same defaults and layering. Capabilities is not
first, so the switch lives on the role page now. Moving it there later is a
UI move: the stored value stays `agent_models[role].mayRaiseMachine`.

## Applying it: two paths

**1. Same pool: resized in place.** This is lossless.

- The call writes the task layer (below) and parks the Run with a new
  `dude_pause = 'machine'`, the same way `'repository'` parks one to apply
  something. The Run resumes at once.
- The resume goes through `phases.Syncer.resume`. `PlanResize` compares
  `runs.machine` with what `ForRole` resolves now, which is the task's size,
  and `ResumeSized` sends `resources`. lux applies CPUs and memory, and the
  disk if it is larger.
- The agent keeps its conversation and checkout. Its next input is "You now
  run on Large (4 CPUs · 16 GiB · 40 GiB). Carry on."
- `run.resized` records what lux applied. If lux kept less (a 409 under way,
  or a 422), the agent is told so: "You asked for Large; lux kept you on
  Standard: <why>". The task layer stays, so the next Run gets Large.

**2. Another pool: a fresh Run in the same slot.** lux binds a Run to its
pool at submit, and `PlanResize` says "a stopped Run cannot change pools".

- `delivery.Restart` gains `Machine string` and `BaseRefs map[string]string`.
  `RestartRunTx` aborts the Run and creates its replacement in the same slot
  of `PendingRunIDs`.
  - `BaseRefs` is the old Run's last push. A fresh Run normally starts from
    the task's heads (`d.State.Heads`), which would miss what the agent just
    pushed to its own Run branch.
  - The restart note is "Restarted on XL (8 CPUs · 32 GiB), which your
    predecessor run_7f3k asked for: "<reason>"".
- The old Run's line in Chat reads **Replaced** (`runStatusLabel`, today's
  "Restarted"). The new Run's line follows it (screen 2).

**Restart coverage** (verified on github/main 8b54a4fe):

- Implement, fix (including pull-request fix: `awaitPRFix`), simplify and
  test (`qa_browser`) Runs are all in the delivery's `PendingRunIDs` while
  it waits on them. So `RestartRunTx` covers a cross-pool move for every
  task phase that holds code.
- **The investigator needs nothing now.** The `investigate` phase and its
  role exist (`RoleForPhase`, `PromptRoleForPhase`), but no delivery step
  creates such a Run. `start_phase` offers implement / review / fix /
  simplify / test (`decisions.go`), and the start point offers implement
  only. When a step does create one, it will be in `PendingRunIDs` like the
  others, and the switch can appear on its page.
- **The conductor resizes in its own pool only.** `RestartRunTx` refuses it
  ("not a phase Run"), and replacing it would cut Chat's conversation. The
  tool refuses a cross-pool size for it with the in-pool alternative (table
  above). Its machine is small and rarely matters, so its switch is off by
  default.

**Why not always restart?** A restart throws away the context and
uncommitted work an agent mid-task wants to keep. In place is one
park/resume of tens of seconds (`run.resume.timed`), and it already exists
(lux#51, `PlanResize`/`ResumeSized`).

### On screen

dude's notice follows the call, which shows in the Run's line as a
`ToolCallCard` (screens 1 and 2):

- **Same pool:** "Moving the implementer to Large: it keeps its
  conversation and work. Later implementer Runs in this task run on Large
  too." (`ChatNotice kind="notice"`)
- **Another pool:** "XL is in another pool, so a fresh implementer starts
  on XL from a41c9e2, with the reason run_7f3k gave as its note. Later
  implementer Runs in this task run on XL too." (`kind="restart"`)

The move itself is the startup design's row (`StartProgress`, below):

- **Same pool:** `verb="Moving to Large"`. The Run line's status label
  reads the same. When the agent is up, the row folds into the resume line:
  "The implementer moved to Large in 41s, with its conversation and work."
  (`kind="unparked"`, with the stage times in its tooltip).
- **Another pool:** the fresh Run's ordinary `Starting` row.

## Sessions across pools

A brainstorm session's Run is resized in place the same way, through
`phases/session.go`'s resume. Across pools it uses #112's fresh-agent
takeover: `ReplaceBrainstorm` aborts the old Run and `StartBrainstorm`
starts a fresh one, and `SessionHandOver` briefs it with the conversation
and hands on what the old one never read. Two changes to that path:

- `BrainstormSwap` compares the machine as well as the model. A session
  whose layer names a size in another pool swaps at once rather than at the
  next member's message, because the agent asked and has ended its turn.
- `ReplaceBrainstorm` takes its reason and its event from the caller. It
  currently hard-codes "the session's model changed" and
  `session.model.applied`. This path passes "moved to XL" and
  `machine.raised`.

**What is kept:**

- The conversation is kept.
- The agent's working files (its checkout, scratch databases, downloads)
  are not, unless it pushed or published them. A brainstorm never pushes,
  so in practice they are lost.

The tool's answer and the notice say so (screen 7): "started a fresh agent
on XL; it has the conversation so far. Files the last one wrote (archive.db)
were not kept." The unpushed-work refusal does not apply to a session: it
has nothing to push, and its files are scratch. The answer names what will
be lost instead.

## Persistence

**The task layer:** `tasks.agent_machines jsonb NOT NULL DEFAULT '{}'`,
keyed by settings role:

```json
{ "implementer": { "sizeId": "ms_large", "moveId": "mmv_01J…", "askedBy": "run_7f3k", "reason": "…", "at": "2026-10-12T14:05:11Z" } }
```

- **Resolution:** `delivery.Sizes.ForRole` resolves task → project →
  organisation, and `From` gains `"task"`.
- **The fixer follows the implementer.** The chain for the fixer is
  `fixer@task, fixer@project, fixer@org, implementer@task, implementer@project,
  implementer@org`, so `implementer@task` reaches the fixer unless the fixer
  names a size at some layer. A fixer's own raise is `fixer@task` and does
  not reach the implementer.
- **Later Runs:** every later Run of the role in the task runs on the raised
  size: fix rounds, Try again, restarts.
- **Survives Start over.** Start over (`api/recover.go` `startOver`) is a new
  attempt of the same task: the task row and its layer stay, so attempt 2's
  first implementer starts on Large (screen 8). A new task does not inherit
  it.
- **Size removed:** `removeSize` (`apps/control-plane/src/api/routes/machines.ts`)
  rewrites `sizeId` to the replacement, or drops the key when the
  replacement is "follow the default", in the same transaction as the
  organisation's and projects' layers.
- **When the task ends** no Run reads the layer. The column stays for
  history.

**The session:** `sessions.agent_machine jsonb NULL`, the same object with
no role key. Session resolution becomes session → organisation.

**The Run's record:** `runs.machine.from` gains `"task"` and `"session"`,
plus `raised: {moveId, askedBy, reason, at}`, copied from the layer. The
chip's tooltip reads that, so history stays true after a Reset.

## Reset

- **Who may:** anyone who may act in the project, the same as starting a
  task. There is no one to "decide" any more. On a session: its members.
- **Where:** the task Overview's Machines panel (`MachineRaises`, screen 5),
  one Reset per raised role.
- **It moves the running agent back down now,** after a confirmation
  (`Dialog size="sm"`) with a one-line consequence:
  - Same pool: "Move the implementer back to Standard now? It keeps its work
    (same pool). Later implementer Runs in this task run on Standard too."
    The confirm is primary, "Move it to Standard". It is resized in place
    (a park and resume, `StartProgress verb="Moving to Standard"`). A disk
    smaller than its saved state is kept, as lux does today.
  - Another pool: the title is the same, then "XL is in another pool: a
    fresh implementer starts from what it pushed; what it has not pushed is
    lost." The dialog has `tone="danger"` and the confirm is `danger solid`,
    "Replace it on Standard". The replacement goes through `RestartRunTx`
    from the old Run's last push, like a cross-pool raise.
- **What it does:**
  - The task-level size is cleared: the key is dropped, and later Runs go
    back to the settings' size.
  - The agent is told: "Márcio reset your machine to Standard: <one line
    of why, if given>." With nothing running, only the layer is cleared.
- A fixer that follows the implementer has no Reset of its own. The
  implementer's Reset covers it.

## Data, API and migration

One migration, `111_agent_machine_moves.sql`:

```sql
CREATE TABLE machine_moves (
  id              text PRIMARY KEY,                          -- mmv_…
  organization_id text NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  task_id         text REFERENCES tasks(id) ON DELETE CASCADE,
  session_id      text REFERENCES sessions(id) ON DELETE CASCADE,
  run_id          text NOT NULL REFERENCES runs(id) ON DELETE CASCADE,  -- the Run moved
  role            text NOT NULL,                             -- settings role
  kind            text NOT NULL CHECK (kind IN ('raise', 'reset')),
  from_size       jsonb NOT NULL,                            -- runs.machine before
  to_size_id      text NOT NULL REFERENCES machine_sizes(id),
  apply           text NOT NULL CHECK (apply IN ('resize', 'restart', 'takeover', 'none')),
  reason          text CHECK (length(reason) <= 2000),       -- the agent's; a Reset's optional note
  fresh           boolean NOT NULL DEFAULT false,            -- left unpushed work behind
  by_person       text,                                      -- a Reset's
  new_run_id      text REFERENCES runs(id),                  -- restart / takeover
  settled_at      timestamptz,                               -- the move finished (resumed, or the new Run started)
  created_at      timestamptz NOT NULL DEFAULT now(),
  CHECK ((task_id IS NULL) <> (session_id IS NULL))
);
CREATE UNIQUE INDEX machine_moves_one_at_a_time ON machine_moves (run_id) WHERE settled_at IS NULL;

ALTER TABLE tasks    ADD COLUMN agent_machines jsonb NOT NULL DEFAULT '{}';
ALTER TABLE sessions ADD COLUMN agent_machine  jsonb;
ALTER TABLE runs DROP CONSTRAINT runs_dude_pause_check;
ALTER TABLE runs ADD CONSTRAINT runs_dude_pause_check
  CHECK (dude_pause IN ('repository', 'person', 'idle', 'unused', 'conductor', 'session', 'machine'));
```

- RLS and grants follow `020_repository_requests.sql`.
- Every new column is catalog-only, as 109's are.
- The switch needs no column. It is `mayRaiseMachine` in the existing
  `default_agent_models[role]` / `projects.agent_models[role]` jsonb.
  `RoleSettings` gains `mayRaiseMachine: Setting<boolean> &
  { organization?: boolean; followsImplementer?: boolean }`, absent for the
  reviewer and investigator, and `settingsPatchSchema` gains it per role.
  `roleChanged` counts it, so the menu shows "changed".

**Routes:**

- The tool and `push` live in the orchestrator's agenttools, in one
  transaction each: the move row, the layer, then the park, the restart or
  the takeover.
- `DELETE /v1/tasks/:id/agent-machines/:role` (Reset), and
  `DELETE /v1/sessions/:id/agent-machine`.
  - The backend forwards to the orchestrator
    (`/internal/agent-machines/reset`), which applies it.
  - 409 if a move of that Run is under way.
  - 404 if nothing is raised.
- The task detail carries `agentMachines` with names resolved, and the Run
  detail's `machine` carries `from` and `raised`.

**The web:**

- `conversation.ts` folds `machine.raised` and `machine.reset` into notices.
- The `request_machine` call is an ordinary tool call in the Run's
  transcript. Task Chat shows it under the Run's line, as it shows other
  calls.

## Ledger events

Add these to `packages/domain/src/events/types.ts`, with Go constants:

- **`machine.requested`**, on the Run, when the agent calls:
  `{ moveId?, role, from: {sizeId, name, cpus, memoryMiB, diskGiB, pool},
  to: {…}, reason, fresh, refused?: string }`. A refusal is recorded too,
  with no `moveId`, so the per-Run count and an admin's question "who asked
  for what" have one source.
- **`machine.raised`**, on the Run, when the layer is written and the move
  starts: `{ moveId, role, sizeId, apply: "resize" | "restart" | "takeover",
  newRunId? }`.
- **`machine.reset`**, on the task or session: `{ moveId, role?, sizeId (back
  to), by, apply: "resize" | "restart" | "takeover" | "none" }`.
- **`run.resized`** and **`run.restarted`** stay as they are.
  `run.restarted` gains `machine?: sizeId` beside `tier?`.

| Event | Notice in the task's Chat (or the session) |
|---|---|
| `machine.raised` resize | "Moving the implementer to Large: it keeps its conversation and work. Later implementer Runs in this task run on Large too." |
| `machine.raised` restart | "XL is in another pool, so a fresh implementer starts on XL from a41c9e2, with the reason run_7f3k gave as its note. Later implementer Runs in this task run on XL too." |
| `machine.raised` takeover | "The brainstorm asked for XL, in another pool: started a fresh agent on XL; it has the conversation so far. Files the last one wrote (archive.db) were not kept." |
| `run.resized` after a move | "The implementer moved to Large in 41s, with its conversation and work." (`unparked`) |
| `machine.reset` | "Márcio reset the implementer's machine to Standard: moving it now, keeping its work." / "…: a fresh implementer starts on Standard from what it pushed." |

## The prompt line

In `delivery/prompts.go`, for a role whose switch is on, after the role's
context:

> You run on Standard (2 CPUs · 8 GiB · 20 GiB). Larger sizes you may move
> to: Large (4 · 16), XL (8 · 32, another pool: you would be replaced by a
> fresh implementer starting from what you pushed). If something you need
> to run is killed for memory (exit 137, "Killed", an out-of-memory error)
> or is far too slow for its CPUs, do not call it flaky: run it again with
> fewer workers if that is enough, and otherwise move with
> `dude machine request SIZE --reason "…"`, saying what ran out. Before a
> size in another pool, commit and `dude push`.

- When raised, the first sentence becomes "You run on Large (4 CPUs ·
  16 GiB · 40 GiB), raised for this task after the implementer's jest run
  ran out of memory." That is what the fixer and attempt 2 read (screens 4
  and 8).
- With the switch off, the prompt has nothing about it, and the tool is not
  listed.
- The tool's description repeats the sizes and the cross-pool sentence, so a
  harness that drops the system prompt's tail still has them.

## Guards (not settings)

- **2 moves per Run** (`machineMovesPerRun`), enough for Standard → Large →
  XL. Refusals do not count.
- **One at a time,** by the partial unique index: a move is settled when
  the Run resumes or the new one starts.
- **Larger sizes only.** Going down is Reset.
- **A reason is required.** It shows in the call, the notice, the chip's
  tooltip and Machines on the Overview.
- **Fit:** a size that fits no host of its pool is refused, as Save refuses
  it in the size dialog.
- **Bounded by the task:** the layer does not leak into the project or other
  tasks.

The cost of a raise shows where it already does: the Run's cost (model plus
machine).

## Design system

Built in `apps/web/mockups/bigmachine/components.tsx` and
`bigmachine.module.css` to the DS rules: tokens only, both themes and
densities, accessible names. Nothing in `packages/design-system` changes on
this branch.

**Removed** (no person decides now):

- `MachineRequestCard` and the proposed `QuestionCard kind="machine"`
  (`detail`, `warning`, `note`, `actions`, `decision`).
- `SizeChange`, with its price row and peak meter. Screens 1 and 2 need
  only "Standard → Large", which is the call's summary.

**Reused unchanged:**

- `ToolCallCard`, `ChatTranscript`, `ChatMessage`, `ChatAside`,
  `ChatRunLine` (with `statusLabel` "Replaced" / "Moving to Large"),
  `ChatNotice` (`notice`, `restart`, `unparked`), `TaskHistory`,
  `DeciderLine`, `ChatComposer`.
- `SessionHeader`, `TierChip`, `MachineChip`, `MachineTip`, `ScreenHeader`,
  `SessionRail`, `SessionFacts`, `SessionPeople`.
- `StepList`/`StepRow`, `Breadcrumb`, `PersonLine`, `StatusMark`.
- `SettingsLayout`, `SettingsHeader`, `SettingFields`, `SettingField`,
  `Switch`, `SettingSource`, `SettingsNote`, `SettingsSection`, `Select`.
- `Dialog` and `DialogClose`, `Button`, `Callout`, `Tooltip`.

### `request_machine` in a transcript: a `ToolCallCard`, no new component

It is `ToolCallCard name="request_machine" icon="chip"` with:

- `summary`: "Asked for a larger machine: Standard → **Large**", plus
  " · highmem pool" across pools;
- `args`: size, reason, fresh;
- `result`: the answer, or `status="failed"` with `error` for a refusal
  (open, its first line in the row).

The only DS change is one entry in `ToolCallCard`'s `TOOL_ICON` map:
`/^(request_machine|machine)/ → "chip"`, so callers need not pass `icon`.
The mockup's `MachineRequestCall` only composes these props.

README, under *Machines*, one sentence:

> An agent's `request_machine` is a `ToolCallCard` with the chip glyph,
> "Asked for a larger machine: Standard → Large" as its summary, and the
> answer as its result; a refusal is a failed call.

### Changed: `MachineChip` gets `raised`

| Prop | Type | |
|---|---|---|
| `raised` | `string?` | "for this task", "for this session": after the spec, the `arrow-up` glyph and "raised …" in secondary ink, and in the accessible name. |

README, after the `MachineChip` paragraph:

> A machine an agent raised for its task or session says so on the chip:
> `raised="for this task"` adds "↑ raised for this task" after the spec. The
> tooltip says which Run asked for it, when and why ("Asked for by run_7f3k
> (the implementer), 1h ago: …"), the settings' size, and that Reset is on
> the task's Overview.

Gallery: *Machines › Chip*, a raised chip in both themes, with its tooltip.

### New: `MachineRaises` (components, *Machines*)

| Prop | Type | |
|---|---|---|
| `where` | `ReactNode` | After the title, muted: "raised in this task". |
| `raises` | `{ role, label, size, settings, detail, onReset? }[]` | One row each: the role's face, "Implementer runs on **Large** 4 CPUs · 16 GiB", the detail under it ("Asked for by run_7f3k, 1h ago: <reason>"), and Reset when `onReset` is given. |

README, under *Machines*:

> **The machines raised in a task are a `MachineRaises`**, in the
> Overview's aside, in `ServersSummary`'s grammar: one row per role, its
> face, the size strong with its spec muted, which Run asked and why
> beneath, and a quiet **Reset** that opens its confirmation. A role that
> follows another ("Follows the implementer") has a row and no Reset. With
> nothing raised, the panel is not drawn.

Gallery: *Machines › Raised in a task*, with two rows (one with Reset, one
following).

### Reset's confirmation: `Dialog size="sm"`, no new component

The README already says a destructive confirm is `Dialog tone="danger"`
with a `danger solid` confirm, and the gallery's "Abort run 14?" is the
pattern.

- **Same pool:** no tone. Cancel is "Keep Large" (quiet), the confirm is
  "Move it to Standard" (primary).
- **Another pool:** `tone="danger"`, and the confirm is "Replace it on
  Standard" (`danger solid`).
- The description is the one-line consequence.

The mockup's `ResetMachineConfirm` is this composition. Nothing is added to
the README.

### The move: `StartProgress` (agent-startup), no new component

A move is the design system's `StartProgress` (and `StartChip` in the Run
header) from the agent-startup build. Its `steps: StartStep[]` come from
that design's **shared helper**, which builds them from the one stage-words
map (`park` included). This design passes only a verb and stage ids:

| Case | `verb` | stage ids |
|---|---|---|
| Same pool, raised | `"Moving to Large"` | `park, waiting, volumes, container, running` |
| Another pool (a fresh Run) | `"Starting"` (the default) | the normal Starting list |
| Reset, same pool | `"Moving to Standard"` | `park, waiting, volumes, container, running` |
| Reset, another pool (a fresh Run) | `"Starting"` | the normal Starting list |

`image` and `repositories` don't occur on a resume, so the same-pool list
leaves them out. The README's *Starting* paragraph already covers the verb
and the stage list, so this design adds no words to it. The mockup's
`StartProgress` and `startSteps` stand in for the DS component and helper,
which it can't import yet, with the same props.

### `ChatNotice`

No new kind. Raises are `notice`, cross-pool raises and takeovers are
`restart`, and a finished move is `unparked`.

### The switch: no new component

It is a `SettingField` holding a `Switch` and a one-line hint, with
`SettingSource` on a project, beside Machine in the role's `SettingFields`.

## Screens

Dark and light at 1440 comfortable, plus dark compact, unless noted.

| # | Screen | Files |
|---|---|---|
| 1 | Task Chat, same pool: the implementer's `request_machine` (Standard → Large, its reason, the answer), dude's notice, "Moving to Large" with its stages (1440×1100) | `1-move-{dark,light,dark-compact}.png` |
| 2 | Task Chat, another pool: `push` then `request_machine` (Standard → XL · highmem), the old line "Replaced", the notice, the fresh implementer "Starting" (1440×1100) | `2-move-pool-{dark,light,dark-compact}.png` |
| 3 | Refused in the implementer's Run: XL across pools with an unpushed commit and two files (named), then it commits and pushes; and "already at the largest size" on XL | `3-refused-unpushed-*.png`, `3-refused-largest-*.png` |
| 4 | The fixer's Run header: "Large · raised for this task", its tooltip "Asked for by run_7f3k (the implementer), 1h ago: …" | `4-run-*.png`, `4-run-tip-{dark,light}.png` |
| 5 | Task Overview, Machines with Reset; Reset's confirm in the same pool, and across pools (danger) | `5-task-*.png`, `5-reset-*.png`, `5-reset-pool-*.png` |
| 6 | Acme → Implementer, "May ask for a larger machine" on; billing → Simplifier overridden off with SettingSource and Reset | `6-role-implementer-acme-*.png`, `6-role-simplifier-billing-*.png` |
| 7 | A brainstorm session Small → XL (highmem): its call, the takeover notice, the conversation kept and its scratch file not | `7-session-*.png` |
| 8 | After Start over: attempt 2's first implementer Run header still on Large, raised for this task, and its tooltip | `8-start-over-*.png`, `8-start-over-tip-{dark,light}.png` |
| 9 | Screen 1 at 390px | `9-phone-move-{dark,light}.png` |

## Open questions

1. **`push` mid-Run.** Phase agents can't push before they finish today.
   This design adds `dude push` (lux's push to the Run's own branch) and
   restarts from the old Run's last push, not the task's heads. Should
   `request_machine` push committed work itself instead, refusing only for
   uncommitted changes? That saves the agent a step. The reason against is
   that it's an implicit side effect.
2. **The per-Run limit of 2 moves.** Is it right, and should a Reset free
   one?
3. **Reset's note.** Should Reset offer an optional line to the agent
   (why), or is "Márcio reset your machine to Standard" enough?
4. **The conductor's switch.** Off by default with in-pool only, as
   proposed, or no switch at all until it has a reason to move?
