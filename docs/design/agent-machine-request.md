# An agent moves itself to a larger machine

**Status: mockup.** Nothing is built.

> **Revised 2026-10-12**, to Márcio's decisions. The agent drives the whole
> process and no person is in the loop. `request_machine` applies at once,
> so the approval card, Approve/Decline, the automatic mode and its ceiling
> are gone. Prices (`machine_sizes.usd_per_hour`) and the memory-peak meter
> are gone too: lux reports no peak, and the agent's reason is the evidence.
> Sessions cross pools by #112's fresh-agent takeover. A raise survives
> Start over. Reset moves the running agent back down now, and anyone in the
> project may use it. The move's row is the design system's `StartProgress`
> (agent-startup, being built), not a component of its own.
>
> **Round 3 (2026-10-12 17:41):** no setting: every agent may ask, the
> conductor included, also across pools (a new conductor, briefed). 3 moves
> per Run, and a Reset gives none back. Reset takes an optional note to the
> agent. Across pools `request_machine` pushes the agent's commits itself
> and refuses only on uncommitted changes, so `dude push` and `--fresh` are
> gone.

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
}
type requestMachineOut struct {
    Size   string `json:"size"`
    Apply  string `json:"apply"` // resize | restart | takeover
    Pushed string `json:"pushed,omitempty"` // across pools: the commit the replacement starts from
    Answer string `json:"answer"`
}
```

**CLI:** `dude machine request SIZE --reason "…"` prints the tool's JSON.
`dude machine sizes` lists the organisation's sizes and marks the agent's
own size, the larger ones and their pools (`list_machine_sizes`, a read
tool, offered with the request).

**Who gets it:** every agent with a machine of its own: implementer, fixer,
tester (`qa_browser`), simplifier, reviewer, the conductor and a session's
brainstorm. There is no switch (below).

**It applies at once and ends the turn.** No one decides. The answer says
what happens:

- **Same pool:** "Moved: you run on Large (4 CPUs · 16 GiB · 40 GiB) from
  your next step; your conversation and checkout are kept. Later implementer
  and fixer Runs in this task run on Large too. End your turn now; you are
  resumed on Large."
- **Another pool** (the tool's description says this too): "Pushed 2
  commits (a41c9e2) to dude/BILL-42/run-run_7f3k, then: XL is in another
  pool, so you will be replaced by a fresh implementer starting from
  a41c9e2, with your reason as its note. Later implementer and fixer Runs in
  this task run on XL too. End your turn now."

**Refusals.** Each is worded so the agent can act on it:

| Case | Answer |
|---|---|
| Not one of the organisation's sizes | "no size named XXL; larger than yours: Large (4 · 16), XL (8 · 32, another pool)" |
| Not larger than its own | "you run on Standard (2 CPUs · 8 GiB); ask for one larger: Large, XL". Larger means strictly more memory, or the same memory and more CPUs. |
| Already the largest | "You run on XL (8 CPUs · 32 GiB · 80 GiB), Acme's largest size: there is none larger to ask for. Use fewer workers or less data, and say in your work what did not fit." (screen 3) |
| Empty reason, or over 2000 characters | "say what ran out: the command, its error or exit code" |
| A move under way | "you are being moved to Large now; ask again once you are there, if it is still too small" |
| Past the Run's limit (guards) | "This Run has moved 3 times, the limit for one Run; a Reset does not give a move back. Carry on on Standard, or say in your work what did not fit." (screen 3) |
| Another pool, with uncommitted changes | below (screen 3) |
| Another pool, and lux's push did not land | "XL is in another pool, and pushing your commits first failed: <lux's reason>. You stay on Standard." |
| The size's pool is gone from lux, or the size fits no host of its pool (`machineFit`) | "XL's pool has no host it fits; ask for another size" |

### Work across pools: the tool pushes, and refuses only on uncommitted changes

A move across pools replaces the agent, and with no person in the loop the
agent is the only one who can lose work. Commits are pushed for it; what it
has not committed is the only thing it must act on.

1. **The live check.** The call runs the live diff's script
   (`phases/livediff.go`'s `diffScript`, through lux's exec) on the Run's
   checkout. It does not read the last `run_diffs` snapshot, which can be
   seconds old.
2. **Uncommitted changes refuse the move, naming the files:**

   > XL is in another pool: you would be replaced by a fresh implementer
   > starting from your last commit. Uncommitted: src/export/stream.ts
   > (M +42 −7), src/export/sort.test.ts (A +88). Commit them first; what
   > you commit is pushed for you. Then ask again.

3. **Otherwise dude pushes.** It calls `lux.Client.Push` (`POST
   /v1/runs/{id}/push`, the call the conductor's publish makes in
   `phases/publish.go` `askPush`) with a request id of the move's
   (`mmv_…`). lux pushes the Run's checkout to its `git.push` branch,
   `dude/<task>/run-<run>`, and reports the outcome as a `git.push` event
   with that request id (`lux.PushResult`, one `PushRepoResult` per
   repository).
4. **It waits for that event,** read as `awaitPush` reads a publish's
   (the Run's lux events from where its follower stood, by request id):
   - every repository `pushed` or `up-to-date` → the replacement starts
     from each `Commit` (below), and the answer begins "Pushed 2 commits
     (a41c9e2) to …";
   - any `failed` or `refused` (`lux.PushRefused`: a merge or rebase in
     progress), or a push lux rejects outright (a non-retryable
     `lux.AsError`) → the move is refused with lux's reason, and the agent
     stays where it is. Nothing is written to the task layer.

The call holds the agent's turn for the push, about 10 s (the mockup's
9.8 s). The move row in the ledger records the pushed commits.

The push does not move the task branch, which stays the delivery's job.
Rebasing or merging in progress refuses the same way, from lux's
`operation`.

**The brainstorm** has no push branch: a session's Run is submitted without
`git.push`, so there is nothing to push and nothing to refuse. A cross-pool
session move keeps the conversation only (below).

## No setting yet

No setting yet: every agent may ask. It becomes a capability ('Ask for a larger machine', agent-wide, Reach) when capabilities ships; see agent-capabilities.md.

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
- **The conductor** takes this path too (screen 4): a parked conductor is
  already resumed by the syncer (`Parkable`), so a same-pool move is a park
  with `'machine'` and a sized resume, and Chat's conversation stays in its
  harness.

**2. Another pool: a fresh Run in the same slot.** lux binds a Run to its
pool at submit, and `PlanResize` says "a stopped Run cannot change pools".

- `delivery.Restart` gains `Machine string` and `BaseRefs map[string]string`.
  `RestartRunTx` aborts the Run and creates its replacement in the same slot
  of `PendingRunIDs`.
  - `BaseRefs` is what the move pushed (step 4 above). A fresh Run normally
    starts from the task's heads (`d.State.Heads`), which would miss the
    agent's commits on its own Run branch.
  - The restart note is "Restarted on XL (8 CPUs · 32 GiB), which your
    predecessor run_7f3k asked for: "<reason>"".
- The old Run's line in Chat reads **Replaced** (`runStatusLabel`, today's
  "Restarted"). The new Run's line follows it (screen 2).

**Restart coverage** (verified on github/main 8b54a4fe):

- Implement, fix (including pull-request fix: `awaitPRFix`), simplify and
  test (`qa_browser`) Runs are all in the delivery's `PendingRunIDs` while
  it waits on them. So `RestartRunTx` covers a cross-pool move for every
  task phase that holds code. A review Run is too; it has nothing to push,
  and starts again from the same heads.
- **The investigator needs nothing now.** The `investigate` phase and its
  role exist (`RoleForPhase`, `PromptRoleForPhase`), but no delivery step
  creates such a Run. `start_phase` offers implement / review / fix /
  simplify / test (`decisions.go`), and the start point offers implement
  only. When a step does create one, it will be in `PendingRunIDs` like the
  others.
- **The conductor** is not a phase Run: `RestartRunTx` refuses it. It has
  its own path (next).

### The conductor across pools: a new conductor, briefed

dude already replaces conductors. `delivery.EndConductor` completes one that
can't be resumed and calls `HandOver(ctx, tx, ref, true)`, which gives what
it was sent and never read to the task's live conductor, or starts one
(`handTo` → `StartConductor`), briefed from the task by `briefing`. The
briefing carries the Chat so far (`Replay(Talker{TaskID})`), so a new
conductor already reads the conversation it takes over.

A cross-pool conductor move uses that path, in one transaction under
`LockChat`, after the push above:

1. **End it.** `EndConductor` only ends a Run that is `Ending` (lux ended
   it). The move asks lux to stop the Run first (`lux_stop_reason =
   'machine'`), then ends it with the same steps: `RefuseUnmovedTx` (its
   publishes that moved nothing are refused, as when any conductor ends),
   `status = 'completed'`, `run.completed` with reason "moved to XL". This
   is a sibling, `ReplaceConductorTx(ctx, tx, ref, size, reason, bases)`,
   not a change to `EndConductor`'s `Ending` guard.
2. **Start the new one on the new size.** `startConductor` gains a reason
   to be briefed, `byMachine` beside `byPerson`, `byWake` and `rebrief`.
   Its opening line: "Conductor, you asked for XL in another pool, so dude
   started you there: "<reason>". You are this task's conductor. Below is
   what dude knows about the task, in short; read more with your tools."
   Then the task and the Chat so far, as every briefing. Its checkout
   starts from what the move pushed (`bases`), not `conductorBases`.
3. **Hand over.** `HandOver(ctx, tx, ref, true)` then finds the new
   conductor live (`LiveConductor`) and queues every unread message to it,
   images included, as it does today.

The task layer's `conductor` key makes the new conductor, and every later
one in the task (a person's next message after this one ends), start on
XL.

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

**What is kept:** the conversation only. A session's Run has no push
branch, so its working files (its checkout, scratch databases, downloads)
are lost unless it published them. The tool's answer and the notice say so
(screen 7): "A session has no branch to push to, so files you wrote here
(archive.db) are not kept." Nothing refuses: they are scratch.

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
- **It moves the running agent back down now,** after a confirmation
  (`Dialog size="sm"`) with a one-line consequence and an optional note:
  - Same pool: "Move the implementer back to Standard now? It keeps its work
    (same pool). Later implementer Runs in this task run on Standard too."
    The confirm is primary, "Move it to Standard". It is resized in place
    (a park and resume, `StartProgress verb="Moving to Standard"`). A disk
    smaller than its saved state is kept, as lux does today.
  - Another pool: the title is the same, then "XL is in another pool: a
    fresh implementer starts from what it pushed; what it has not pushed is
    lost." The dialog has `tone="danger"` and the confirm is `danger solid`,
    "Replace it on Standard". The replacement goes through `RestartRunTx`
    (the conductor: `ReplaceConductorTx`) from the old Run's last push.
    Reset does not push for it: a person can't see the agent's checkout, and
    the warning says what is lost.
  - **The note:** a `Textarea` labelled "Note to the implementer (optional)",
    up to 2000 characters, whose hint says what the agent is told (screen 6,
    filled in both dialogs).
- **What it does:**
  - The task-level size is cleared: the key is dropped, and later Runs go
    back to the settings' size.
  - The agent is told "Moved back to Standard by Márcio: <note>", or with
    no note "Moved back to Standard by Márcio." A replacement gets the same
    line as its restart note (the conductor: in its briefing). With nothing
    running, only the layer is cleared.
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
  apply           text NOT NULL CHECK (apply IN ('resize', 'restart', 'replace', 'takeover', 'none')),
  reason          text CHECK (length(reason) <= 2000),       -- the agent's; a Reset's optional note
  pushed          jsonb,                                     -- across pools: {repo: commit} lux reported
  by_person       text,                                      -- a Reset's
  new_run_id      text REFERENCES runs(id),                  -- restart / replace / takeover
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

- `apply = 'replace'` is the conductor's cross-pool path; `'restart'` a
  phase Run's.
- RLS and grants follow `020_repository_requests.sql`.
- Every new column is catalog-only, as 109's are.

**Routes:**

- The tool lives in the orchestrator's agenttools. Its writes are one
  transaction after the push: the move row, the layer, then the park, the
  restart, the conductor's replacement or the takeover.
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
- **`machine.raised`**, on the Run, when the layer is written and the move
  starts: `{ moveId, role, sizeId, apply: "resize" | "restart" | "replace" |
  "takeover", pushed?: {repo: commit}, newRunId? }`.
- **`machine.reset`**, on the task or session: `{ moveId, role?, sizeId (back
  to), by, note?, apply: "resize" | "restart" | "replace" | "takeover" |
  "none" }`. `note` is the person's, as the agent was told it.
- **`run.resized`** and **`run.restarted`** stay as they are.
  `run.restarted` gains `machine?: sizeId` beside `tier?`.

| Event | Notice in the task's Chat (or the session) |
|---|---|
| `machine.raised` resize | "Moving the implementer to Large: it keeps its conversation and work. Later implementer Runs in this task run on Large too." |
| `machine.raised` resize, the conductor | "Moving the conductor to Large: it keeps this conversation and its checkout. Later conductors in this task run on Large too." |
| `machine.raised` restart | "XL is in another pool, so a fresh implementer starts on XL from a41c9e2, which the move pushed, with the reason run_7f3k gave as its note. Later implementer Runs in this task run on XL too." |
| `machine.raised` replace | "XL is in another pool, so a new conductor starts on XL, briefed with this conversation and the reason it gave. Messages it had not read go to the new one." |
| `machine.raised` takeover | "The brainstorm asked for XL, in another pool: started a fresh agent on XL; it has the conversation so far. Files the last one wrote (archive.db) were not kept." |
| `run.resized` after a move | "The implementer moved to Large in 41s, with its conversation and work." (`unparked`) |
| `machine.reset` | "Márcio moved the implementer back to Standard: "<note>"." / "…: a fresh implementer starts on Standard from what it pushed." |

## The prompt line

In `delivery/prompts.go` (and `ConductorPrompt`), for every role with a
machine, after the role's context:

> You run on Standard (2 CPUs · 8 GiB · 20 GiB). Larger sizes you may move
> to: Large (4 · 16), XL (8 · 32, another pool: you would be replaced by a
> fresh implementer starting from your commits, which are pushed for you).
> If something you need to run is killed for memory (exit 137, "Killed", an
> out-of-memory error) or is far too slow for its CPUs, do not call it
> flaky: run it again with fewer workers if that is enough, and otherwise
> move with `dude machine request SIZE --reason "…"`, saying what ran out.
> Before a size in another pool, commit what you want to keep.

- When raised, the first sentence becomes "You run on Large (4 CPUs ·
  16 GiB · 40 GiB), raised for this task after the implementer's jest run
  ran out of memory." That is what the fixer and attempt 2 read (screens 5
  and 8).
- The brainstorm's version says instead: "XL (another pool: a fresh agent
  takes over with this conversation; files you wrote are not kept)."
- The tool's description repeats the sizes and the cross-pool sentence, so a
  harness that drops the system prompt's tail still has them.

## Guards

- **3 moves per Run** (`machineMovesPerRun`), enough for Standard → Large →
  XL with one to spare. Refusals do not count. A cross-pool move's new Run
  starts its own count.
- **A Reset does not give a move back.** A Run moved 3 times and Reset
  stays at 3 (screen 3).
- **One at a time,** by the partial unique index: a move is settled when
  the Run resumes or the new one starts.
- **Larger sizes only.** Going down is Reset.
- **A reason is required.** It shows in the call, the notice, the chip's
  tooltip and Machines on the Overview.
- **Fit:** a size that fits no host of its pool is refused, as Save refuses
  it in the size dialog.
- **Across pools, nothing uncommitted** and a push lux reported as landed.
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
  `ChatRunLine` (with `statusLabel` "Replaced" / "Moving to Large"),
  `ChatNotice` (`notice`, `restart`, `unparked`), `TaskHistory`,
  `DeciderLine`, `ChatComposer`.
- `SessionHeader`, `TierChip`, `MachineChip`, `MachineTip`, `ScreenHeader`,
  `SessionRail`, `SessionFacts`, `SessionPeople`.
- `StepList`/`StepRow`, `Breadcrumb`, `PersonLine`, `StatusMark`.
- `Dialog` and `DialogClose`, `Textarea`, `Button`, `Callout`, `Tooltip`.

### `request_machine` in a transcript: a `ToolCallCard`, no new component

It is `ToolCallCard name="request_machine" icon="chip"` with:

- `summary`: "Asked for a larger machine: Standard → **Large**", plus
  " · highmem pool" across pools;
- `args`: size, reason;
- `result`: the answer (across pools it opens with what was pushed), or
  `status="failed"` with `error` for a refusal (open, its first line in the
  row).

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

The README already says a destructive confirm is `Dialog tone="danger"`
with a `danger solid` confirm, and the gallery's "Abort run 14?" is the
pattern.

- **Same pool:** no tone. Cancel is "Keep Large" (quiet), the confirm is
  "Move it to Standard" (primary).
- **Another pool:** `tone="danger"`, and the confirm is "Replace it on
  Standard" (`danger solid`).
- The description is the one-line consequence.
- The body is the DS `Textarea` (`label="Note to the implementer
  (optional)"`, `rows={2}`, `maxRows={6}`, `maxLength={2000}`), its `hint`
  saying what the agent is told: "It is told "Moved back to Standard by
  Márcio:" and then your note.", or with it empty "It is told "Moved back
  to Standard by Márcio."".

The mockup's `ResetMachineConfirm` is this composition. Nothing is added to
the README.

### The move: `StartProgress` (agent-startup), no new component

A move is the design system's `StartProgress` (and `StartChip` in the Run
header) from the agent-startup build. Its `steps: StartStep[]` come from
that design's **shared helper**, which builds them from the one stage-words
map (`park` included). This design passes only a verb and stage ids:

| Case | `verb` | stage ids |
|---|---|---|
| Same pool, raised (any role, the conductor included) | `"Moving to Large"` | `park, waiting, volumes, container, running` |
| Another pool (a fresh Run, or a new conductor) | `"Starting"` (the default) | the normal Starting list |
| Reset, same pool | `"Moving to Standard"` | `park, waiting, volumes, container, running` |
| Reset, another pool (a fresh Run) | `"Starting"` | the normal Starting list |

`image` and `repositories` don't occur on a resume, so the same-pool list
leaves them out. The README's *Starting* paragraph already covers the verb
and the stage list, so this design adds no words to it. The mockup's
`StartProgress` and `startSteps` stand in for the DS component and helper,
which it can't import yet, with the same props.

### `ChatNotice`

No new kind. Raises are `notice`, cross-pool raises, a conductor's
replacement and takeovers are `restart`, and a finished move is `unparked`.

## Screens

Dark and light at 1440 comfortable, plus dark compact, unless noted.

| # | Screen | Files |
|---|---|---|
| 1 | Task Chat, same pool: the implementer's `request_machine` (Standard → Large, its reason, the answer), dude's notice, "Moving to Large" with its stages (1440×1100) | `1-move-{dark,light,dark-compact}.png` |
| 2 | Task Chat, another pool: `request_machine` (Standard → XL · highmem) whose result says it pushed 2 commits (a41c9e2), the old line "Replaced", the notice, the fresh implementer "Starting" (1440×1100) | `2-move-pool-*.png` |
| 3 | Refused in the implementer's Run: XL across pools with two uncommitted files (named), then it commits and asks again, and the tool pushes and moves it (1440×1300); "already at the largest" on XL; "3 moves already" after a Reset | `3-refused-uncommitted-*.png`, `3-refused-largest-*.png`, `3-refused-moves-*.png` |
| 4 | Task Chat: the conductor asks Standard → Large to run the backend suite (same pool), its notice and "Moving to Large" (1440×1100) | `4-conductor-*.png` |
| 5 | The fixer's Run header: "Large · raised for this task", its tooltip "Asked for by run_7f3k (the implementer), 1h ago: …" | `5-run-*.png`, `5-run-tip-{dark,light}.png` |
| 6 | Task Overview, Machines with Reset; Reset's confirm with its note filled in, in the same pool, and across pools (danger) | `6-task-*.png`, `6-reset-*.png`, `6-reset-pool-*.png` |
| 7 | A brainstorm session Small → XL (highmem): its call, the takeover notice, the conversation kept and its scratch file not | `7-session-*.png` |
| 8 | After Start over: attempt 2's first implementer Run header still on Large, raised for this task, and its tooltip | `8-start-over-*.png`, `8-start-over-tip-{dark,light}.png` |
| 9 | Screen 1 at 390px | `9-phone-move-{dark,light}.png` |

## Open questions

1. **The conductor's new checkout across pools.** This design starts the new
   conductor from what the move pushed to the old one's Run branch.
   `conductorBases` starts every other conductor from the delivery's heads,
   and a later conductor (after this one ends) would go back to those, not
   to the moved conductor's unpublished commits. Is that right, or should
   the conductor's pushed commits count as the task's head until it
   publishes?
2. **The push's wait.** The call holds the agent's turn until lux's
   `git.push` arrives. What bound, and what answer at the bound: refuse
   ("lux did not report the push in 60s; ask again") or move from what lux
   last reported?
