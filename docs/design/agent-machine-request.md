# An agent moves itself to a larger machine

**Status: mockup.** Nothing is built.

> **Revised 2026-10-12**, to Márcio's decisions. The agent drives the whole
> process and no person is in the loop. `request_machine` applies at once,
> so the approval card, Approve/Decline, the automatic mode and its ceiling
> are gone. Prices (`machine_sizes.usd_per_hour`) and the memory-peak meter
> are gone too: lux reports no peak, and the agent's reason is the evidence.
> A raise survives Start over. Reset moves the running agent back down now,
> and anyone in the project may use it. The move's row is the design
> system's `StartProgress` (agent-startup, being built), not a component of
> its own.
>
> **Round 3 (2026-10-12 17:41):** no setting: every agent may ask, the
> conductor included. 3 moves per Run, and a Reset gives none back. Reset
> takes an optional note to the agent.
>
> **Round 5 (2026-10-12 18:15):** lux will let a resume name another pool
> (`placement.poolId` with `resources`, below), and saved state already
> follows a Run to any host. So every move, in its pool or to another, a
> raise or a Reset, of any role, the conductor or a session, is the same
> thing: park, then a sized resume. The fresh Run, the tool's push and its
> wait, every refusal about uncommitted work, the conductor's replacement,
> the session takeover and Reset's danger variant existed only to cross
> pools without lux; they are gone. What is left to refuse across pools is
> what lux refuses: a platform or a capability the pool lacks.
>
> **Round 6 (2026-10-12 18:37):** Márcio's answers to the last open
> questions. lux gets a dry run (`validateOnly: true` on resume), so every
> refusal comes before the park. A settings change that moves a size to
> another pool moves parked Runs on their next resume (kept). The image's
> platforms come from lux's own record of the Run's placements (lux#91),
> so pinned external images are filtered too.

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
    Size   string `json:"size"   jsonschema:"one of the sizes listed in this tool's description, by name (Large)"`
    Reason string `json:"reason" jsonschema:"what ran out: the command, its exit or error, the workers or threads it used"`
}
type requestMachineOut struct {
    Size   string `json:"size"`
    Pool   string `json:"pool,omitempty"` // set when the move changes pools
    Answer string `json:"answer"`
}
```

**CLI:** `dude machine request SIZE --reason "…"` prints the tool's JSON.
`dude machine sizes` lists the sizes this agent could move to, with their
pools (`list_machine_sizes`, a read tool, offered with the request).

**Who gets it:** every agent with a machine of its own: implementer, fixer,
tester (`qa_browser`), simplifier, reviewer, the conductor and a session's
brainstorm. There is no switch (below).

**It applies at once and ends the turn.** No one decides. The answer is the
same shape in any pool (screens 1, 2):

> Moved: you run on XL (8 CPUs · 32 GiB · 80 GiB) in the highmem pool from
> your next step. Your conversation and your checkout are kept, committed or
> not, with everything else on /workspace and your home. Running processes
> and /tmp are not: start your servers and watchers again. Later implementer
> and fixer Runs in this task run on XL too. End your turn now; you are
> resumed on XL.

In the same pool, "in the highmem pool" is left out.

**Refusals.** Each is worded so the agent can act on it:

| Case | Answer |
|---|---|
| Not one of the sizes it may move to | "no size named XXL; you may move to: Large (4 · 16), XL (8 · 32, highmem)" |
| Not larger than its own | "you run on Standard (2 CPUs · 8 GiB); ask for one larger: Large, XL". Larger means strictly more memory, or the same memory and more CPUs. |
| Already the largest | "You run on XL (8 CPUs · 32 GiB · 80 GiB), Acme's largest size: there is none larger to ask for. Use fewer workers or less data, and say in your work what did not fit." (screen 3) |
| Empty reason, or over 2000 characters | "say what ran out: the command, its error or exit code" |
| A move under way | "you are being moved to Large now; ask again once you are there, if it is still too small" |
| Past the Run's limit (guards) | "This Run has moved 3 times, the limit for one Run; a Reset does not give a move back. Carry on on Standard, or say in your work what did not fit." (screen 3) |
| lux: 422 `platform_mismatch` | "XL is in highmem, whose machines are linux/arm64; this agent's image is linux/amd64 only. Ask for another size, or ask a person to add an arm64 image." (screen 3) |
| lux: 422 `pool_unfit` | "XL is in highmem, which can't run nested containers, and this agent's image needs them. Ask for another size." With a size reason: "XL (8 CPUs · 32 GiB) is larger than any machine in highmem. Ask for another size." |
| The size's pool is gone from lux (`unknown_pool`), or the size fits no host of its pool (`machineFit`) | "XL's pool has no host it fits; ask for another size" |

The lux refusals are passed through: the tool maps lux's `code` and its
detail fields (the pool, the two platforms, the missing capability) into
these sentences, and never shows lux's raw message alone. When lux refuses,
the task layer is put back as it was and the move row is settled with
`refused` (below).

**The size list holds only sizes it could move to.** The tool's
description, `dude machine sizes` and the prompt line list the
organisation's sizes larger than the agent's, minus the ones dude already
knows lux would refuse:

- the pool's platform is not one of the image's;
- the Run needs nested containers (`spec.sandbox.nestedContainers`) and the
  pool does not offer them;
- the size fits no host of its pool (`machineFit`, as the size dialog checks).

Where dude reads this:

- **The pool's platform and capabilities: lux's pool list** (`GET
  /v1/pools`, `lux.Client.Pools`, `lux.Pool`). Today's `Pool` has `hostSize`
  and `instanceType`, but no platform and no capability. Proposed to lux:
  `platforms: ["linux/arm64"]` and `capabilities: {nestedContainers: bool}`
  on `Pool`. A lux without them filters nothing, and its 422 after the park
  is the answer (below).
- **The image's platforms: lux's record of the Run's placements.** Since
  lux#91 every placement reports `imagePlatforms` (every entry of the
  image's manifest list or index, or a single-arch image's own), read from
  `GET /v1/runs/{id}/placements`. That covers images dude builds and pinned
  external ones alike, with no manifest reading in dude. A Run with no
  placement reporting them yet (an older runner) filters nothing.

The filter is a courtesy, not the check: lux's dry run is.

## No setting yet

No setting yet: every agent may ask. It becomes a capability ('Ask for a larger machine', agent-wide, Reach) when capabilities ships; see agent-capabilities.md.

## Applying it: park, then a sized resume

One path for every move:

- The call writes the move row and the task layer (below) and parks the Run
  with a new `dude_pause = 'machine'`, the same way `'repository'` parks one
  to apply something. The Run resumes at once.
- The resume goes through `phases.Syncer.resume` (a session's through
  `phases/session.go`). `PlanResize` compares `runs.machine` with what
  `ForRole` resolves now, which is the task's size, and `ResumeSized` sends
  `resources`, plus `placement.poolId` when the target's pool differs from
  the Run's (next section). lux applies CPUs and memory, and the disk if it
  is larger.
- The agent's next input is "You now run on XL (8 CPUs · 32 GiB · 80 GiB)
  in highmem. Start again any server or watcher you had running. Carry on."
- `run.resized` records what lux applied. If lux kept less (a 409 under
  way), the agent is told so: "You asked for Large; lux kept you on
  Standard: <why>". The task layer stays, so the next Run gets Large.
- **The conductor** moves the same way (screen 4): a parked conductor is
  already resumed by the syncer (`Parkable`), and Chat's conversation stays
  in its harness.
- **A session** moves the same way (screen 7), keeping its conversation and
  its files (`archive.db`).

**Every refusal comes before the park.** Before parking anything, the
tool asks lux the exact resume it would send (`resources` and, when the
pool changes, `placement.poolId`) with `validateOnly: true`. lux runs every
check a real resume would, on the live Run, changes nothing and answers
`{valid: true, placement, resize}` or the same 4xx a real resume gives.

- **Refused:** the call fails at once with lux's code in plain words, and
  the agent never stops (screen 3, where the conductor told the tester to
  try XL and it named a size the list had left out).
- **Accepted:** dude parks and resumes with the same body. A refusal at the
  real resume (the pool changed in between) is still handled: the Run
  resumes where it was without `resources` or `placement` (as
  `ResumeSized` falls back today), its next input is the same sentence, and
  the move row is settled with `refused`.
- **An older lux** that does not know `validateOnly` answers 400; dude then
  goes straight to the park and the fallback above.

### What a move keeps

- **Kept:** the conversation and the checkout, committed or not, and
  everything else on the state volumes (`/workspace`, the home volume).
- **Not kept:** running processes, `/tmp` and the container's writable
  layer. The tool's answer says to start them again.

### `resize.go`

`PlanResize` (`orchestrator/internal/phases/resize.go`) stops saying "a
stopped Run cannot change pools":

- `poolOf`'s result becomes "send the pool" instead of "keep the size":
  `poolOther` and `poolNotDefault` set `p.Send` with the resources and a new
  `p.Pool` (the target's `PoolID`; for a target naming none, lux's default
  pool's id from the same pool list), and no `Note`.
- `ResumeSized` puts `p.Pool` in `ResumeInput.Placement` (new, `*lux.PlacementSpec`).
- `Outcome` records the pool lux reports for the resumed placement, and
  `Machine.OtherPool` is no longer written.
- The same plan now also moves a Run whose settings changed pools while it
  was parked: it is no longer kept on its old size. **Decided (Márcio):**
  keep this; it is what the settings say. The resume's dry run guards it
  the same way, and a refusal keeps the Run on its size with the Note.

**Knowing lux supports it.** Proposed, in order:

1. lux's OpenAPI (`docs/openapi.yaml`, `ResumeRequest`) gains `placement`.
   dude reads nothing at runtime from it; it is the contract.
2. A lux that does not know `placement` answers 400 (unknown field), as one
   from before lux#51 answers `resources` today. `ResumeSized` treats that
   400 like today's: it resumes without `placement` and without
   `resources`, and `keptNote` says "this lux cannot move a stopped Run to
   another pool". The tool's answer for that Run is the plain refusal "XL is
   in another pool, and this lux cannot move a Run between pools yet; ask
   for a size in yours (Large)". The 400 is remembered for the process, so
   the size list stops offering other pools until a restart.

If lux prefers a capability flag (`GET /v1/pools` or a `/v1/capabilities`
with `resumePlacement: true`), dude checks it before listing other pools
and keeps the 400 fallback for safety.

## The lux change

Being built on a lux branch. What dude relies on:

- **`POST /v1/runs/{id}/resume`** takes `placement: {poolId}` together with
  `resources`. A resume without `placement` stays in the Run's pool.
- **Checks**, in this order:
  - the tenant may use the pool (else 422 `unknown_pool`, as at submit);
  - the Run's image platform is one of the pool's (else 422
    `platform_mismatch`, with `pool`, `poolPlatforms`, `imagePlatforms`);
  - the pool can give what the spec needs: nested containers, and a host
    the resources fit (else 422 `pool_unfit`, with `pool` and `missing`:
    `nestedContainers` or `size`).
- **Saved state** (the `/workspace` checkout and the home volume) follows
  the Run to the new pool's host, as on any resume today.
- **Event:** lux emits `resume.requested.pool {from, to}` (pool ids) on the
  Run when a resume changes its pool. dude reads it on the Run's follower
  to confirm the placement; it is not shown.
- **Dry run:** `validateOnly: true` runs every check of that resume on a
  live Run (state is judged as if it were stopped), changes nothing, needs
  no secret values, and answers `{valid, placement, resize}` or the same
  4xx. CLI `lux resume --dry-run`.
- **An older lux** answers 400 to `placement` or `validateOnly`; dude falls
  back as above.

## Persistence

**The task layer:** `tasks.agent_machines jsonb NOT NULL DEFAULT '{}'`,
keyed by settings role, `conductor` included:

```json
{
  "implementer": { "sizeId": "ms_large", "moveId": "mmv_01J…", "askedBy": "run_7f3k", "reason": "…", "at": "2026-10-12T14:05:11Z" },
  "conductor":   { "sizeId": "ms_large", "moveId": "mmv_01K…", "askedBy": "run_c0n1", "reason": "…", "at": "2026-10-12T15:12:40Z" }
}
```

- **Resolution:** `delivery.Sizes.ForRole` resolves task → project →
  organisation, and `From` gains `"task"`. `StartConductor` resolves the
  conductor's size the same way.
- **The fixer follows the implementer.** The chain for the fixer is
  `fixer@task, fixer@project, fixer@org, implementer@task, implementer@project,
  implementer@org`, so `implementer@task` reaches the fixer unless the fixer
  names a size at some layer. A fixer's own raise is `fixer@task` and does
  not reach the implementer.
- **Later Runs:** every later Run of the role in the task runs on the raised
  size: fix rounds, Try again, restarts, the next conductor.
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

**The session keeps its own:** `sessions.agent_machine jsonb NULL`, the same
object with no role key. Session resolution becomes session → organisation.

**The Run's record:** `runs.machine.from` gains `"task"` and `"session"`,
plus `raised: {moveId, askedBy, reason, at}`, copied from the layer. The
chip's tooltip reads that, so history stays true after a Reset.

## Reset

- **Who may:** anyone who may act in the project, the same as starting a
  task. On a session: its members.
- **Where:** the task Overview's Machines panel (`MachineRaises`, screen 6),
  one Reset per raised role, the conductor's included.
- **It moves the running agent back down now,** the same way as any move,
  after one plain confirmation (`Dialog size="sm"`, no tone), whichever
  pools are involved:
  - Title "Move the implementer back to Standard now?", description "It
    keeps its conversation and checkout; running processes stop. Later
    implementer Runs in this task run on Standard too."
  - Cancel "Keep Large" (quiet), confirm "Move it to Standard" (primary).
  - **The note:** a `Textarea` labelled "Note to the implementer
    (optional)", up to 2000 characters, whose hint says what the agent is
    told.
  - A disk smaller than its saved state is kept, as lux does today.
- **What it does:**
  - The task-level size is cleared: the key is dropped, and later Runs go
    back to the settings' size.
  - The agent is told "Moved back to Standard by Márcio: <note>", or with
    no note "Moved back to Standard by Márcio." With nothing running, only
    the layer is cleared.
  - If lux refuses the way back (the settings' size's pool no longer suits
    the image), the layer is still cleared, the Run stays where it is, and
    the Overview says "The implementer stays on XL until its next Run: lux
    refused Standard's pool (<reason>)."
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
  role            text NOT NULL,                             -- settings role, conductor included
  kind            text NOT NULL CHECK (kind IN ('raise', 'reset')),
  from_size       jsonb NOT NULL,                            -- runs.machine before
  to_size_id      text NOT NULL REFERENCES machine_sizes(id),
  apply           text NOT NULL CHECK (apply IN ('resize', 'none')),  -- 'none': a Reset with nothing running
  pool            jsonb,                                     -- {from, to} lux pool ids, when the move changed pools
  reason          text CHECK (length(reason) <= 2000),       -- the agent's; a Reset's optional note
  by_person       text,                                      -- a Reset's
  refused         text,                                      -- lux's code, when the resume refused the move
  settled_at      timestamptz,                               -- resumed (moved, or refused and left where it was)
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

**Routes:**

- The tool lives in the orchestrator's agenttools. Its writes are one
  transaction: the move row, the layer, then the park.
- `DELETE /v1/tasks/:id/agent-machines/:role` (Reset), and
  `DELETE /v1/sessions/:id/agent-machine`, each with an optional body
  `{ note?: string }` (≤ 2000).
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
  calls; the conductor's shows under its own message (screen 4).

## Ledger events

Add these to `packages/domain/src/events/types.ts`, with Go constants:

- **`machine.requested`**, on the Run, when the agent calls:
  `{ moveId?, role, from: {sizeId, name, cpus, memoryMiB, diskGiB, pool},
  to: {…}, reason, refused?: string }`. A refusal is recorded too, with no
  `moveId`, so an admin's question "who asked for what" has one source.
  lux's refusals after the park update it with `refused: "platform_mismatch"`.
- **`machine.raised`**, on the Run, when the layer is written and the move
  starts: `{ moveId, role, from: sizeId, to: sizeId, pool?: {from, to} }`.
- **`machine.reset`**, on the task or session: `{ moveId, role?, from:
  sizeId, to: sizeId, by, pool?: {from, to}, note? }`. `note` is the
  person's, as the agent was told it.
- **`run.resized`** stays as it is, with the pool lux reported.

| Event | Notice in the task's Chat (or the session) |
|---|---|
| `machine.raised` | "Moving the implementer to Large: it keeps its conversation and checkout; running processes stop. Later implementer Runs in this task run on Large too." |
| `machine.raised` with `pool` | "Moving the implementer to XL, in the highmem pool: …" (the same sentence) |
| `machine.raised`, the conductor | "Moving the conductor to Large: it keeps this conversation and its checkout; running processes stop. Later conductors in this task run on Large too." |
| `run.resized` after a move | "The implementer moved to Large in 41s, with its conversation and work." (`unparked`) |
| `machine.reset` | "Márcio moved the implementer back to Standard: "<note>"." |

## The prompt line

In `delivery/prompts.go` (and `ConductorPrompt`), for every role with a
machine, after the role's context, listing only the sizes it could move to:

> You run on Standard (2 CPUs · 8 GiB · 20 GiB). Larger sizes you may move
> to: Large (4 · 16), XL (8 · 32, highmem pool). If something you need to
> run is killed for memory (exit 137, "Killed", an out-of-memory error) or
> is far too slow for its CPUs, do not call it flaky: run it again with
> fewer workers if that is enough, and otherwise move with `dude machine
> request SIZE --reason "…"`, saying what ran out. A move keeps your
> conversation and checkout; running processes and /tmp are not kept.

- When raised, the first sentence becomes "You run on Large (4 CPUs ·
  16 GiB · 40 GiB), raised for this task after the implementer's jest run
  ran out of memory." That is what the fixer and attempt 2 read (screens 5
  and 8).
- The tool's description repeats the sizes and the last sentence, so a
  harness that drops the system prompt's tail still has them.

## Guards

- **3 moves per Run** (`machineMovesPerRun`), enough for Standard → Large →
  XL with one to spare. Refusals do not count, lux's included.
- **A Reset does not give a move back.** A Run moved 3 times and Reset
  stays at 3 (screen 3).
- **One at a time,** by the partial unique index: a move is settled when
  the Run resumes.
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

**Removed** (no person decides now, and no setting):

- `MachineRequestCard` and the proposed `QuestionCard kind="machine"`
  (`detail`, `warning`, `note`, `actions`, `decision`).
- `SizeChange`, with its price row and peak meter. Screens 1 and 2 need
  only "Standard → Large", which is the call's summary.
- The role page's switch: no `SettingField` + `Switch` + `SettingSource`
  for it.

**Reused unchanged:**

- `ToolCallCard`, `ChatTranscript`, `ChatMessage`, `ChatAside`,
  `ChatRunLine` (with `statusLabel` "Moving to Large"),
  `ChatNotice` (`notice`, `unparked`), `TaskHistory`, `DeciderLine`,
  `ChatComposer`.
- `SessionHeader`, `TierChip`, `MachineChip`, `MachineTip`, `ScreenHeader`,
  `SessionRail`, `SessionFacts`, `SessionPeople`.
- `StepList`/`StepRow`, `Breadcrumb`, `PersonLine`, `StatusMark`.
- `Dialog` and `DialogClose`, `Textarea`, `Button`, `Callout`, `Tooltip`.

### `request_machine` in a transcript: a `ToolCallCard`, no new component

It is `ToolCallCard name="request_machine" icon="chip"` with:

- `summary`: "Asked for a larger machine: Standard → **Large**", plus
  " · highmem pool" when the pool changes;
- `args`: size, reason;
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

### Reset's confirmation: `Dialog size="sm"` with a `Textarea`, no new component

One variant: no tone, Cancel "Keep Large" (quiet), confirm "Move it to
Standard" (primary). The description is the one-line consequence. The body
is the DS `Textarea` (`label="Note to the implementer (optional)"`,
`rows={2}`, `maxRows={6}`, `maxLength={2000}`), its `hint` saying what the
agent is told: "It is told "Moved back to Standard by Márcio:" and then
your note.", or with it empty "It is told "Moved back to Standard by
Márcio."".

The mockup's `ResetMachineConfirm` is this composition. Nothing is added to
the README.

### The move: `StartProgress` (agent-startup), no new component

A move is the design system's `StartProgress` (and `StartChip` in the Run
header) from the agent-startup build. Its `steps: StartStep[]` come from
that design's **shared helper**, which builds them from the one stage-words
map (`park` included). This design passes only a verb, stage ids and, when
the pool changes, the pool's name:

| Case | `verb` | stage ids | `pool` |
|---|---|---|---|
| Raised, same pool (any role, the conductor and a session included) | `"Moving to Large"` | `park, waiting, image, volumes, repositories, container, running` | — |
| Raised, another pool | `"Moving to XL"` | the same | `"highmem"` |
| Reset | `"Moving to Standard"` | the same | the pool's name when it changes |

That is `park` followed by the shared `RESUME_STAGES` (the helper's
`MOVE_STAGES`): lux announces `image` and `repositories` on every resume, and
they are shown (Márcio, 2026-10-12).

**Proposed addition to the shared helper:** `startSteps(ids, {pool})`. With
`pool`, the `waiting` stage reads "Waiting for a machine in highmem" while
current and "Machine found in highmem" once done; the words stay in the
shared stage map, with the pool name as a parameter, not in this design.
Without `pool`, nothing changes. **This is a change to coordinate with the
startup build**, which owns the helper and the map; until it lands, a
cross-pool move would read the plain "Waiting for a machine". The mockup's
`StartProgress`, `startSteps` and `STAGE_WORDS.waiting.pooled` stand in for
them with the same props.

### `ChatNotice`

No new kind. Raises and Resets are `notice`, and a finished move is
`unparked`.

## Screens

Dark and light at 1440 comfortable, plus dark compact, unless noted.

| # | Screen | Files |
|---|---|---|
| 1 | Task Chat, same pool: the implementer's `request_machine` (Standard → Large, its reason, the answer with what is kept and that processes stop), dude's notice, "Moving to Large" with its stages (1440×1100) | `1-move-{dark,light,dark-compact}.png` |
| 2 | Task Chat, another pool: Standard → XL · highmem, the same row, "Moving to XL" with "Waiting for a machine in highmem" current (1440×1100) | `2-move-pool-*.png` |
| 3 | Refused in the Run: the tester names XL (the conductor's suggestion; its list had left XL out) and is refused before any park with lux's `platform_mismatch` in plain words, then moves to Large (1440×1100); "already at the largest" on XL; "3 moves already" after a Reset | `3-refused-platform-*.png`, `3-refused-largest-*.png`, `3-refused-moves-*.png` |
| 4 | Task Chat: the conductor asks Standard → Large to run the backend suite, its notice and "Moving to Large" (1440×1100) | `4-conductor-*.png` |
| 5 | The fixer's Run header: "Large · raised for this task", its tooltip "Asked for by run_7f3k (the implementer), 1h ago: …" | `5-run-*.png`, `5-run-tip-{dark,light}.png` |
| 6 | Task Overview, Machines with Reset; Reset's plain confirm with its note filled in | `6-task-*.png`, `6-reset-*.png` |
| 7 | A brainstorm session Small → XL · highmem: its call, the notice, the conversation and `archive.db` kept | `7-session-*.png` |
| 8 | After Start over: attempt 2's first implementer Run header still on Large, raised for this task, and its tooltip | `8-start-over-*.png`, `8-start-over-tip-{dark,light}.png` |
| 9 | Screen 1 at 390px | `9-phone-move-{dark,light}.png` |

## Open questions

None left. Decided on 2026-10-12 (Márcio): lux's dry run so every refusal
comes before the park; settings-driven resumes cross pools; image platforms
from lux's placements.
