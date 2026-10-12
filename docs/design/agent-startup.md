# The agent is starting

**Status: built (2026-10-12).** The mockup and its screens are on the
`design/agent-startup` branch (`docs/design/mockups/startup/`, source in
`apps/web/mockups/startup`); they are not carried here. *As built*, at the
end, says what Márcio decided on the open questions and where the build
differs from this proposal.

## The problem

When a session's message wakes its agent, or a task's phase is submitted,
the agent is not there yet. lux has to find a host, get the image, restore
the state volumes, check out the repositories, create the container and
bring the harness up. That takes from a few seconds to a few minutes, and
a resume of a parked brainstorm commonly takes tens of seconds.

During that time a session shows the person's message and nothing under
it. The header says nothing either. It looks stale. A Run's header shows a
"Waiting for a host" badge only while lux reports a capacity reason
(`statusNote`), and nothing for the stages after it. Previews already show
lux's stage live (`PreviewStages` with a `Duration`, from
`previewStage` / `previewStageSince`). Agents ignore it.

The cue has to answer three questions: **is anything happening, what is
it waiting on, and for how long?**

## States and words

lux reports a starting Run's `stage` and `stageSince` on `GET
/v1/runs/{id}`, plus a `stage` event `{stage, since, epoch, reason?}` once
per change (lux `docs/concepts.md`, *Stages*). The words come from **one
map, shared with branch previews** (`STAGE_WORDS` in the mockup's
`data.ts`):

| lux `stage` | Now (current step, header, row) | Done (stepper, resume tooltip) |
| --- | --- | --- |
| `waiting` | Waiting for a machine | Machine found |
| `image` | Getting the image ready | Image ready |
| `volumes` | Restoring its workspace | Workspace restored |
| `repositories` | Checking out repositories | Repositories checked out |
| `container` | Starting the container | Container started |
| `running` (lux state still `starting`) | Starting the agent | Agent started |

- **The verb says which kind of start it is.** A fresh agent is
  "Starting". A parked agent taken back up is "Resuming". This uses the
  `starting` status's own mark (`StatusMark status="starting"`, the
  half-circle in the info tone, breathing).
- **Two clocks.** The current stage's clock counts from lux's
  `stageSince`. The total ("00:31 in all") counts from when this start
  began: the Run's submit, or the resume's `requested_at`. The total
  shows only once a stage has passed, since before that it would repeat
  the stage clock. Clocks use the `clock` style (`00:12`) and tick once a
  second.
- **Done stages keep their took-times** ("Image ready 1.2s"), in
  `Duration`'s `short` format, the same as the finished-resume tooltip.
- **`running` with lux state `starting` is "Starting the agent".** It ends
  when lux's state is `running`. Then the cue goes, and the agent's first
  output takes its place.

## Placement

The mockup shows all three places. **The chat row is the primary one.**

1. **In the chat, where the agent's next turn will appear (primary).**
   `StartProgress` sits on the turn grid, with the agent's face in the
   gutter and the words on the text column. It is directly under the
   queued message, in the slot the "working" indicator
   (`ChatMessage activity`) takes once the agent runs.
   - The eye goes there after sending.
   - It shows what the message is waiting on, right where the message
     sits.
   - It is replaced in place by the agent's first words, so nothing moves
     elsewhere.
   - The header is read once on arrival; the bottom of the chat is
     watched.
2. **In the header (secondary, always visible).** `StartChip` sits in the
   session's `ScreenHeader` meta, after the model: "◐ Resuming · Checking
   out repositories · 00:12". In a Run's `SessionHeader` it is the
   `statusNote`, beside the status that already says Starting, so it drops
   its own mark: "Starting the container · 00:03". It replaces today's
   "Waiting for a host" badge with the stage-aware words. lux's capacity
   reason moves to the slow note (below). The header keeps the state
   visible when the chat is scrolled up, or on Events.
3. **The composer and the queued message.** The queued turn's
   `pendingReason` says **"Will be read when the agent starts."**, or
   **"Will be read when the agent is back."** for a resume. The composer
   says the same after "To **Brainstorm**" (chat mode, in `toAside`), or
   as the steer's `landsHint` on a Run. That replaces "Lands when the
   agent starts." (`steerWait` → `RunScreen.tsx:889`), so the queued line
   and the composer use one sentence.

The rail is not used. It is gone below about 820px, and the cue must
survive a phone (screen `2-resume-phone-*`).

## Expanding for detail

The row's first line is the disclosure button (`aria-expanded`,
`aria-controls`, a chevron). Opened, it shows the stages as a small
vertical stepper, in StepList's and PreviewStages' glyph grammar:

- **done**: a success-tone check, the done words in secondary ink, and
  the took-time on the right, muted;
- **current**: the `starting` half-circle in the info tone, breathing,
  with the words and the live clock in primary ink;
- **pending**: a dotted hollow circle in muted ink, with no time.

A 1px guide line joins the glyphs. The list is an `ol` with
`aria-current="step"` on the current stage. The row starts folded. The
mockup opens it in scenario 2 to show the stepper (`2-resume-*`) and
after a click in `*-open-*`.

**Once the agent runs, the stepper folds into the existing resume line.**
"Taken back up in 14s." (`ChatNotice kind="unparked"`, from
`run.resume.timed`) keeps its place. Its tooltip lists the same stages
with the same done words ("Machine found 9.4s, Image ready 1.2s, …").
`RESUME_PHASES` reads its labels for lux's stages from the shared map and
keeps its own words for dude's phases ("dude asked lux", "agent
reloaded", "first words"). A fresh start has no such line today; see
*Open questions*.

## Slow and failed

- **Slow.** Past a threshold per stage (`SLOW_AFTER_MS`: `waiting` 60s,
  `image` 120s, `repositories` 90s, the rest 60s), a calm line appears
  under the row, in secondary ink with a muted clock glyph:
  - The line says "No machine free yet: dude is waiting for one. Your
    message is kept; nothing is spent meanwhile."
  - Under it, in muted mono, is lux's own reason when it gave one: "lux:
    waiting for capacity: 2 hosts in its pool lack cpus (requested 4)".
  - It is not amber and not a `Callout`. Nothing is wrong yet, and amber
    is reserved for needs-you (README *Status*). The row's clock keeps
    counting.
- **Failed.** This is the transcript's existing failure (the `ended`
  turn's danger `Callout`), now naming the stage:
  - **"Did not start: failed while getting the image ready: `pull failed:
    manifest unknown`. Nothing was spent on the model."**, with **Try
    again** (`secondary`, the retry glyph).
  - The stages fold under it ("Show the stages"), with the failed stage
    marked by a danger cross and the word "failed".
  - The queued message turns to `ChatMessage failed`: "Not delivered: the
    agent did not start. Try again starts it with this message."
  - The header chip reads "✕ Did not start · Failed while getting the
    image ready".
  - The Callout is `role="alert"`, so the failure is announced once.

  **Which reason is shown:** lux sets `stageReason` only while
  `stopping`. A failed start's reason is the `state` event's `reason` (the
  Run's `stateReason`), recorded against the stage it was in. The task
  says "`stageReason` if lux gives one". In practice that field will be
  empty here, and `stateReason` is the one to use.

## Accessibility and motion

- **One polite live region per row** (`role="status"`, `aria-live="polite"`,
  `aria-atomic`). It holds only "Resuming: Checking out repositories."
  It changes once per stage. The clocks are outside it, as `role="timer"`,
  which is not announced while it ticks. A failure is announced by its
  `role="alert"` Callout.
- **Text carries the state.** The verb, the stage's words and the clock
  are all text. The status mark is StatusMark's glyph-and-word. The
  stepper's states have glyph, ink and a visually hidden ", now" / ", to
  come" / ", failed". Grayscale loses nothing.
- **Reduced motion.** The only loop is the `starting` mark's breathe (and
  the current step's glyph), divided by `--ds-motion-live`. It freezes in
  place, and the clocks keep ticking as text. Nothing spins.
- **One live mark per line.** `Duration`'s live clock draws its own green
  dot. Three clocks on one line would be three dots beside the breathing
  mark. The mockup's clocks are `formatDuration(…, "clock")` without the
  dot (see *Design system*).

## Data and API

**Orchestrator.**

- `phases.translator.luxEvent` gains `case "stage":`, as
  `servers.previews` has (`previews.go:653`). It appends one ledger event,
  **`run.stage`** `{stage, since, epoch, reason?}`, with `since` in UTC.
  The event is written only while the Run is starting: on a stage before
  `running`, and on `running` while `lux_state` is not yet `running`.
  `stopping` and the resting stages are not written; the state events
  already cover them.
- On `state` `failed` or `lost` before the Run ever ran, the existing
  `run.failed` payload gains `stage` (the last `run.stage` of that epoch)
  and keeps `reason` as lux's `stateReason`.
- **No column and no migration.** The ledger is the source, as for
  everything the conversation projection shows. An opened page replays
  its events and has the stage at once, and a live page gets the event on
  the stream it already follows. `runs` keeps `lux_state` as today.
- **lux's waiting reason** (`stateReason` while `waiting`) is already
  read for `waitingReason`. It goes in the same event's `reason` when lux
  sends one, or the slow note reads it from the Run detail as today.

**Domain.** `EventTypes.RunStage = "run.stage"` in
`packages/domain/src/events/types.ts`, the matching Go constant, and
`START_STAGE_WORDS` (the table above) in `@dude/domain`. `PreviewStages`
and agents both read it (see *Reuse with previews*).

**Web.** `conversation.ts`'s projection folds `run.stage` into
`Conversation.start: { kind, stage, stageSince, startedAt, done[] } |
null`:

- `kind` is `resume` after a `run.unparked` or `run.resumed` of the same
  epoch, otherwise `start`.
- `done[]` comes from consecutive `since`s within the epoch. A stage's
  took-time is the next stage's `since` minus its own. Both times are
  lux's, so there is no skew.
- `start` clears on lux `state` `running`, on the agent's first record,
  or on a new park.

`SessionScreen` and `RunScreen` then render the chip, the row and the
composer hint from `conversation.start`, the way `ServersSection` renders
`previewStage`. `steerWait` keeps `{kind:"starting"}` and `pendingReason`
gets the new words.

**Cost per stage event.** One ledger insert and its `NOTIFY`, inside the
transaction the stream handler already holds. That is about five or six
per start: one per stage, since lux announces each change once. There is
no lux call, because the stage is in the event, while previews re-read
the Run from lux on every `servers.changed`. There is no extra row
update, and the browser does no re-fetch: the projection folds the event
it already receives. Ticking is client-side, from `since`.

**Reuse with previews.** One map of stage words (`START_STAGE_WORDS`).
`PreviewStages`' "Preparing image / Restoring volumes / Cloning / Starting
container" become the same "Getting the image ready / Restoring its
workspace / Checking out repositories / Starting the container", so a
person reads one vocabulary for agents and previews. The preview keeps
its own later steps (Setup, Starting servers, Ready), which agents do not
have.

## Design system

New components, built to the DS rules (tokens only, both themes, both
densities, accessible names, reduced motion), in
`packages/design-system/src/components/StartProgress.tsx`, with the
gallery's *Starting* section.

### `StartProgress` (components, the transcript)

| Prop | Type | |
| --- | --- | --- |
| `verb` | `string` | "Starting" or "Resuming"; also names the region. |
| `steps` | `StartStep[]` | `{ id, label, doneLabel, state: "done" \| "current" \| "pending" \| "failed", tookMs?, since? }` |
| `startedAt` | instant | The total clock's start. |
| `avatar` | `ReactNode` | The agent's face (`AgentAvatar size="chat"`). |
| `slowNote` | `ReactNode` | The calm line; the app decides when. |
| `failure` | `{ reason: string \| null; onRetry? }` | Renders the danger Callout in place of the row. |
| `defaultOpen` / `open` / `onOpenChange` | | The stepper's disclosure. |

README, under *Activity (the transcript)*:

> - **An agent that is starting says where it is**, where its first turn
>   will be: `StartProgress` on the turn grid, its face in the gutter, the
>   `starting` mark with "Starting" or "Resuming", the stage in words
>   ("Checking out repositories"), the stage's clock and, once a stage
>   has passed, the total ("00:31 in all"). Its line opens the stages as
>   a stepper (done with their times, the current one live, the rest
>   hollow, `aria-current="step"`). Past the app's threshold, one calm
>   line in secondary ink says why it can take a while, with lux's reason
>   in muted mono; never amber. A failed start is the transcript's danger
>   `Callout` naming the stage and lux's reason, with Try again, the
>   stages folded under it. A polite live region says each stage once;
>   the clocks are timers outside it. It is replaced by the agent's first
>   turn, and the finished start is the resume line's tooltip, in the same
>   words.

Gallery: a *Starting* section under the transcript. It shows the six
stages one after another (a play button, like the welcome's replay), a
resume with three done stages open, slow `waiting`, and failed in
`image`, each in dark, light and compact.

### `StartChip` (components, headers)

| Prop | Type | |
| --- | --- | --- |
| `verb` | `string` | As above. |
| `step` | `StartStep` | The current (or failed) step. |
| `bare` | `boolean` | No mark: the header's status already says Starting (a Run's `statusNote`). |

README, under *Agent sessions* (after `statusNote`):

> - **While its agent starts, the header says where**: `StartChip` in a
>   session's meta ("◐ Resuming · Checking out repositories · 00:12") and
>   as a Run's `statusNote`, bare beside its Starting status. It replaces
>   the "Waiting for a host" badge; lux's capacity reason is the
>   transcript's slow note. Failed, it reads "✕ Did not start · Failed
>   while …".

### Changed

- **`Duration`** gains `dot={false}`. A clock beside a mark that is
  already live must not draw a second breathing dot. The mockup uses its
  own `Clock` for that; the component would be `Duration format="clock"
  dot={false}` with `role="timer"`.
- **`PreviewStages`** takes its words from the shared map (see above).
- **`ChatMessage` `pendingReason`** words in the README's list: "Will be
  read when the agent starts." / "…when the agent is back." in place of
  "Lands when the agent starts.".
- **Image readiness (`PreparingImage`)** is unchanged. It comes before
  lux: dude's own builder, before submit. It stays the pinned Callout. The
  row appears once the Run is with lux.

## Scenarios and screens

| # | Scenario | Screens |
| --- | --- | --- |
| 1 | Fresh session, first message: `waiting` (plays: 5s), then `image` | `1-fresh-waiting-{dark,light}`, `1-fresh-image-{dark,light}`, `1-fresh-image-open-{dark,light}`, compact `1-fresh-*-dark-compact` |
| 2 | Parked brainstorm resumed: `repositories` at 0:12, three stages done | `2-resume-{dark,light}`, `2-resume-dark-compact`, `2-resume-phone-{dark,light}` (390) |
| 3 | Slow `waiting` 1:40 with the calm note and lux's reason | `3-slow-{dark,light}`, `3-slow-open-{dark,light}`, `3-slow-dark-compact` |
| 4 | Failed in `image`: "pull failed: manifest unknown" | `4-failed-{dark,light}`, `4-failed-dark-compact` |
| 5 | Running: the cue gone, the message read, "Taken back up in 14s.", the first words streaming | `5-running-{dark,light}`, `5-running-dark-compact` |
| 6 | A task's Run page, starting: chip as `statusNote`, the row, a queued steer, the steer hint | `6-run-{dark,light}`, `6-run-open-{dark,light}`, `6-run-dark-compact` |

## Open questions

1. **A fresh start's summary.** A resume ends in "Taken back up in 14s."
   A first start has no line. Should one be added ("Started in 31s.",
   with the same tooltip), or should the stages simply vanish? The
   proposal adds none: the agent's first turn is the news.
2. **Thresholds.** Are 60s for `waiting` and 120s for `image` right? A
   first pull of a large image on a cold host can take minutes. Should
   the image threshold follow the image's size, which lux does not report
   today?
3. **Try again on a failed start.** For a session, is it a fresh agent
   with the queued message (what the mockup says)? For a task's phase, is
   it the existing "Try again…" dialog (a new Run on the same branch)?
4. **Conductor and task Chat.** The same row fits a conductor waking.
   Should it also show there, in the conductor's Chat, at the same time?
5. **lux without stages.** An older lux sends no `stage`. Then the row
   shows "Starting" with the total clock only, and no stepper. Is that
   enough, or should it fall back to `lux_state` words (scheduled →
   "Waiting for a machine", starting → "Starting")?
6. **A move.** A drain or preempt is `stopping`, then `waiting` again on
   another host. Should the row say "Moving to another machine" for
   that epoch?

## As built

Márcio's answers, all as proposed:

1. A fresh start ends in **"Started in 31s."** (`ChatNotice`, its stages
   in the hover), folded from the start's own `run.stage`s when the agent
   is first heard from. A start lux staged none of (an older lux) gets
   no line.
2. `SLOW_AFTER_MS` stands: `waiting` 60s, `image` 120s, `repositories`
   90s, the rest 60s (`apps/web/src/startCue.tsx`).
3. **Try again**: in a session, `POST /v1/brainstorms/:id/retry` →
   `delivery.RetryBrainstorm`, a fresh agent briefed with the failed one's
   briefing (the message it was started for, read once) and its unread
   messages handed on (`SessionHandOver`); its `run.created` carries
   `retries`, so Chat shows the message queued again rather than twice.
   On a task's phase it opens the task's restart dialog at *Try again*
   (`StoppedRun.onPickUp("retry")`), where the task page offers one.
4. The row shows on a conductor's Chat as it wakes ("Resuming" once it
   has run), with the composer's hint.
5. An older lux: the row is the verb and the total clock, nothing to open;
   the header's chip the same.
6. A drain, preempt or migrate reads **"Moving to another machine"**, its
   stages led by `park`.

Where it differs from the proposal:

- **`park` is written.** lux's `stopping` with a move's reason (`drain`,
  `preempt`, `migrate`, `lux.Moved`) is recorded as `run.stage` `park`;
  a plain stop is still not written. The fake lux reports it on
  `Migrate`. That is the seam the machine-request thread uses
  ("Moving to Large", `MOVE_STAGES`).
- **The words live in `@dude/domain`** (`START_STAGE_WORDS`,
  `FRESH_START_STAGES`, `RESUME_STAGES`, `MOVE_STAGES`), not the design
  system: the projection, `PreviewStages` and `RESUME_PHASES` all read
  them. The DS's `startSteps` builds `StartStep[]` from them and a
  caller's stage list; a stage lux reports that the list leaves out is
  drawn in lux's order.
- **The resume's stage list** is `waiting → volumes → container →
  running`; the fake lux (like lux) still announces `image` and
  `repositories` on a resume, and those are then drawn.
- **A resume's `waiting` carries the epoch it left** (lux moves the epoch
  at assignment); the projection follows the stream's order, not the
  epoch, so this changes nothing on screen.
- **`run.failed` carries `stage` and `reason`** (lux's `stateReason`)
  when a start fails before lux reported the Run running; `error` stays
  dude's sentence.
- **APIs**: `GET /v1/runs/:id` has `startStage`, a session's `run.stage`,
  `{stage, since, epoch}` from the latest `run.stage` while the Run is
  live and not yet running to lux; read from the ledger, no column, no
  migration. The screens use the projection.
- **Previews' words changed** to the agents' ("Getting the image ready",
  "Restoring its workspace", "Starting the container").
- **The Run page keeps `WaitingForHost`** pinned only for a pool with no
  host that can run containers (an admin's to fix); any other lux wait
  reason is the row's slow note.
