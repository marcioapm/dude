# @dude/design-system

The visual language for the dude control plane: a live, operational
console for watching autonomous coding agents work, read for hours at a
time. Closer to Discord or Obsidian than to a trading terminal or a
marketing site: generous type, regions told apart by shade, one quiet
accent.

Run the living gallery to see every token, primitive and component in every
state, dark and light side by side, in either density or both:

```bash
cd packages/design-system
bun run gallery          # dev server at http://localhost:5199
bun run gallery:build    # static build to dist/gallery
bun run typecheck
bun run tokens           # regenerate src/tokens/tokens.css after editing tokens
```

## Philosophy

The operator has one screen, many agents, and five questions: **what is
running, what is stuck, what needs me, what did it cost, what did the agent
do**. Every decision below serves those questions.

The screen they have open all day is the **chat transcript** of a Session
(`ChatTranscript` and friends): the agent's narrative, its tool calls, the
subagents it delegates to, its plan, and the composer through which a human
answers or steers. Beside it, always, is the **sidebar** (`Sidebar`,
`NavTree`): what exists, what is active, what needs a person, who is on
what. Selecting a project or an epic there opens the **board** (`Board`)
in place of the transcript: the same tasks by lifecycle lane. The
event ledger (`EventRow`) and `LogStream` are the debugging and audit tools
behind all of these, reached when something looks off.

1. **Calm under load.** Readable is the goal; noise is the enemy. Readable
   means 15px UI body, 16/22px transcript text, 1.5 for long-form Markdown,
   32px rows, square structure, a 40px avatar on every speaker, and air between
   speakers but not within one — turns are separated by whitespace, not boxes.
   Calm means one accent color, charcoal surfaces told apart by small, even
   shade steps rather than lines, text in close shades rather than white on
   black, and status color only where it means something. A screen with
   200 events on it should look *quiet* until one of them needs you.
   `compact` takes the big spacing in for operators who want more on the
   screen; it never takes the text below readable.
2. **Meaning never lives in hue alone.** Every status has a tone *and* a glyph
   *and* a label. Diffs have background *and* gutter color *and* a sign
   column. Roles have a hue *and* a glyph *and* (for the conductor) a
   shape. Grayscale the gallery and nothing is lost.
3. **Dark is primary, light is a peer.** The operator spends hours here, often
   at night, on a big monitor. Dark mode gets the most careful contrast work
   and is the default. Light is designed alongside it, not derived from it:
   dark elevates with lighter surfaces, light with white on an off-white
   canvas and shadow.
4. **Numbers are read, compared and summed.** Every metric, cost, count,
   duration and timestamp is tabular. Costs, tokens and durations each have
   exactly one formatter, so a value in a tile and the same value in a table
   never disagree.
5. **`awaiting_human` is the only loud thing.** It is the single state where
   the system is blocked on a person. It alone defaults to a solid fill with a
   slow expanding ring. Nothing else may be promoted to solid; if it were, the
   signal would be gone.
6. **Live means alive, not busy.** Running states breathe at 2.4s. Nothing
   spins except the running spinner; nothing flashes except a new row, once.
   Reduced motion turns "live" into "still", never into "invisible".
7. **Activity has a rhythm, not just a colour.** In the transcript, what a
   turn is doing right now is told by tone *and* glyph *and* the cadence of
   its motion: thinking drifts, writing blinks, a tool sweeps, a retry counts
   down, needs-you rings. Two states never share a rhythm, so they separate
   in peripheral vision and in grayscale.
8. **Structure is square; only what you touch or what floats is round.**
   Panels, board columns, cards, tables, the pipeline and the transcript
   have no radius. Buttons, fields and menu rows are `control` (6px);
   dialogs, popovers, menus and toasts are `float` (10px); a kbd, a code span
   or a checkbox is `mark` (3px). `test/radius.test.ts` fails on anything
   else.
9. **Lines are for fields and focus, not for separating things.** Regions
   and rows are told apart by shade and space. A border is kept only where
   it is the thing's shape or meaning: a field's edge, a checkbox, a diff's
   gutter, a bar down one side that carries a tone or a thread.
   `test/borders.test.ts` lists each one and why, and fails on the rest.
10. **Every person has a face, and every action a name.** People are
   circles (a photo, or initials on their identity colour); projects are
   rounded squares (an image, or initials on theirs); agents are rounder
   squares that always carry their role's glyph, never letters. When an
   agent works for a person it sits on that person's avatar. "Human" and
   "a person" never appear where a name is known.
11. **Clickable things say so.** Anything that acts on a click shows a
   pointer (`base.css` sets it for every button, link, tab and
   `role="button"`), and nothing takes it away. Links navigate; buttons
   act. Buttons come in four kinds: `primary` (one per view or panel),
   `secondary` (any other action), `quiet` (row and toolbar actions) and
   `danger` (red text; `solid` only inside its own confirmation).
12. **A cost is a total.** Every cost shown is model tokens plus machine
   time; the number is the sum and its tooltip gives the parts. Given
   where each part came from (`tokensFrom`, `machineFrom`, `settled`), the
   tooltip says it: only a figure lux has made final is "reported by lux";
   one lux is still settling, the harness's, and a machine-rate estimate are
   each called an estimate. Without them it says nothing of an origin.

## Styling approach

**CSS custom properties + CSS Modules.** Tokens are authored in TypeScript
(OKLCH), resolved to hex by `scripts/build-tokens.ts`, and shipped as plain
`tokens.css`. Components style themselves with CSS Modules that reference only
`--ds-*` properties.

Why: it is the most portable option for a product that will also ship inside a
Tauri WebKit/WebView2 window. No runtime style engine, no browser-only
compilation step, no dependency on `oklch()` or `color-mix()` support beyond
a couple of hover tints. Theme switching is one attribute on the root. Any
future consumer that is not React (a native menu, a canvas chart, a CLI
colour table) can import the typed values from `@dude/design-system/tokens`.

Behavioural primitives (Select, Dialog, Tabs, Tooltip, Toast, Checkbox,
ScrollArea, RowMenu) sit on Radix, which is unstyled and does keyboard/ARIA
correctly. Everything else is hand-rolled.

Shared keyframes live in `styles/base.css`; a CSS module must reference
them as `animation: global(ds-name) …` or the name is hashed to nothing and
a Radix popup waiting on its exit animation never unmounts (a test enforces
this). Popups that must sit above a dialog use `--ds-z-popover`.

## Consuming the package

```ts
// once, at the app root — order matters
import "@dude/design-system/tokens.css";
import "@dude/design-system/base.css";

import { ThemeProvider, TooltipProvider, ToastProvider } from "@dude/design-system";

<ThemeProvider>            // stamps data-theme and data-density on <html>; optional — CSS follows the OS on its own
  <TooltipProvider>
    <ToastProvider>
      <App />
```

```ts
import { StatusBadge, EventRow, CostDisplay, Table, Th, Td } from "@dude/design-system";
import { formatUsd, STATUS_SPECS, themeColors } from "@dude/design-system";
```

Peer deps: `react` and `react-dom` 19. Fonts: the sans stack is the platform
UI face first (`ui-sans-serif, -apple-system, BlinkMacSystemFont, system-ui,
"Segoe UI", Roboto`, then Inter as a fallback), the same order Obsidian uses —
SF Pro on macOS, the distro face (Noto Sans on most) on Linux, Segoe UI on
Windows. Nothing needs loading for it. Mono is JetBrains Mono with system mono
fallbacks; ship its files in the host app if you want it everywhere. The
gallery loads no webfont.

Theme control: the CSS honours `prefers-color-scheme` and
`prefers-reduced-motion` by itself. `data-theme="light|dark"` on the root
overrides the OS; `data-reduced-motion="true"` forces reduced motion.
`data-density="comfortable|compact"` picks the density (comfortable when
absent). `ThemeProvider` manages all three and persists the theme and
density choices (`density` / `setDensity` on `useTheme()`).

## Tokens

All under the `--ds-` prefix. See `src/tokens/*.ts` for the source and the
gallery for every value.

| Group | Examples | Notes |
|---|---|---|
| Surfaces | `--ds-color-canvas`, `-surface`, `-raised`, `-overlay`, `-sunken`, `-field-bg`, `-chrome` | Back to front. Dark is charcoal, not black: canvas `#1d1e20`, surface `#252728`, raised `#2e2f31`, overlay `#37383a`, even steps. In light, canvas is off-white and the rest are white. `chrome` is the bar inside a panel (header, toolbar, footer): raised in dark, `#f7f8fa` in light. |
| Borders | `--ds-color-border-subtle`, `-border`, `-border-strong` | For fields, focus, frames that carry a state, and rules in diffs and tables. Not for separating regions: shade does that. |
| Text | `--ds-color-text-primary`, `-secondary`, `-muted`, `-disabled`, `-inverse` | On surface, dark: 11.3 / 7.5 / 5.7:1; light: 12.7 / 7.3 / 5.0:1. Muted clears 4.5:1 on canvas, raised and chrome too. |
| Interaction | `--ds-color-accent`, `-accent-hover/active/subtle/text`, `-focus-ring`, `-selection`, `-hover-wash`, `-active-wash` | One blue. Same hue as the info tone. |
| Tones | `--ds-tone-{neutral,info,attention,success,danger}-{fg,bg,border,solid,on-solid}` | The only status colours. |
| Roles | `--ds-role-{conductor,…,qa-browser}-{fg,bg,solid,on-solid}` | Categorical identity, fixed order, never used for status. |
| Identity | `--ds-identity-{0…7}-{fg,bg}` | Eight muted slots for human avatars, picked by hashing the person's id. About half the chroma of a role colour. |
| Diff | `--ds-diff-{add,del}-{bg,bg-strong,fg}`, `--ds-diff-hunk-{bg,fg}` | Softer than the tones; read for minutes. |
| Merged | `--ds-merged-{fg,bg}` | GitHub's violet, for a merged pull request and nothing else. Not a tone. |
| Elevation | `--ds-shadow-1/2/3` | Includes the hairline ring. Theme-dependent. |
| Type | `--ds-font-sans/mono`, `--ds-text-2xs…4xl`, `--ds-text-nav`, `--ds-text-prose`, `--ds-text-mono`, `--ds-weight-*`, `--ds-leading-*`, `--ds-tracking-*` | UI body `text-md` 15px (14 compact); `sm`/`xs` 13/12 and `nav` 14 in both densities; prose 16px (15 compact). Transcript text runs at `leading-chat` 1.375 (22px at 16px); documents and multi-block Markdown at `leading-prose` 1.5; headings at `leading-tight` 1.3. `2xs` 11px for small-caps labels, badges and key caps only; `mono` 13px. Headings `lg…4xl` are 16/20/22/26/34. |
| Space | `--ds-space-0…64`, `--ds-space-{main-pad,card-pad,chat-pad-x,chat-gap,chat-avatar-gap,panel-gap,tree-indent,nav-row-gap}` | 4px grid plus 2 and 6. The named spaces are layout: main pane 24, board card 12, chat turn 16 across and 17 between speakers, avatar gap 16 (so transcript text starts at 16 + 40 + 16 = 72px), panel gap 24, tree indent 16, 2 between sidebar rows. |
| Radius | `--ds-radius-{none,mark,control,float,full}`, `--ds-radius-face-{agent,project}` | Roles, not sizes: structure `none`, inline marks `mark` 3, controls `control` 6 (5 compact), floats `float` 10, people and dots `full`. Agent and project faces take a share of their size (28%, 22%). |
| Size | `--ds-size-control-sm/md/lg`, `--ds-size-row-compact/default/comfortable`, `--ds-size-avatar-{xs,sm,md,lg,chat}`, `--ds-size-badge-{sm,md}`, `--ds-size-chip`, `--ds-size-icon-*` | Controls 28/32/36, rows 28/32/40. Avatars 16/20/24/32 and 40 for the transcript's own. Badges 16/18, chips 22, in both densities. |
| Motion | `--ds-duration-fast/base/slow/deliberate`, `--ds-ease-*`, `--ds-motion-live`, `--ds-cadence-{spin,breathe,drift,sweep,blink}` | Reduced motion zeroes durations and sets `motion-live` to 0. Cadences are the periods of the live loops; every loop divides by `motion-live`. |
| Measure | `--ds-measure-message`, `--ds-measure-document` | Prose outside the transcript (help text, settings) 70ch (72ch compact); documents 700px in both. Chat turns have none: they span the transcript's column. |
| Density | `data-density="compact"` | Overrides the tokens listed under Density below; everything else is shared. |
| Layers | `--ds-z-base…tooltip` | |

### How the colours were chosen

Tones and role colours are OKLCH with per-slot lightness found by search, then
validated (protan/deutan simulation, normal-vision distance, WCAG contrast)
rather than eyeballed. The four chromatic tone foregrounds clear ΔE ≥ 8 under
CVD simulation and ΔE ≥ 15 in normal vision for every pair, in both modes,
while every `fg` stays ≥ 4.5:1 on the surface. The six role colours clear the
same bar across all 15 pairs, and every role `fg` clears 4.5:1 on its
theme's surface. If you change a hue or a surface, re-run the search; do not
nudge by eye. `test/palette.test.ts` holds the WCAG half: the text ladder,
tone and role foregrounds, link, focus ring and button label.

The neutrals are the same idea turned down. Surfaces are grey with the
faintest cool cast (chroma 0.0035) and stand ΔL 0.035 apart in dark, close
enough to read as layers of one thing rather than boxes. The text ladder is
set against them in close steps: primary is `#dee0e1`, not white — Discord's
`#dbdee1` on `#313338` is 9.4:1, Obsidian's is 12.3:1, ours is 11.3:1 — then
secondary and muted about two ratio-points apart each. Hierarchy comes from
size and shade, not weight: body 400, names and labels 500, headings at most
600.

## Rules

### Status

- Use `StatusBadge` for every Run, Session and Task state. Never a
  `Badge` with a hand-picked tone, never a coloured dot with a tooltip.
- The mapping from status to tone/glyph/emphasis lives in
  `src/tokens/status.ts`. That table is the contract. Add a state there or it
  cannot be rendered.
- Tones mean: **neutral** = nothing happening or deliberately stopped;
  **info** = the system is working; **attention** = a person is needed or
  something is paused; **success** = finished well; **danger** = finished badly.
- `aborted` is neutral, not danger. An operator stopping a run is a decision,
  not a failure, and must not look like one in a list of failures.
- Only `awaiting_human` / `waiting_on_human` are solid by default. Do not
  override `emphasis` to solid for anything else.
- In dense lists (trees, kanban cards) use `variant="dot"`: shape still carries
  meaning (hollow = pending, round = active, square = terminal, diamond =
  needs you).
- Toasts are for the outcome of *your* action. An agent asking a question is
  not a toast; it is a status change and an event, and it persists until dealt
  with.
- Each toast carries `data-toast="<tone>"`. Find one by it, not by its text
  alone: for its first second Radix also renders a hidden copy of the text
  for screen readers, so the text matches twice.

### Activity (the transcript)

- `src/tokens/activity.ts` is the vocabulary for what a turn is doing *now*:
  `thinking`, `streaming`, `tool`, `retrying`, `awaiting_input`, and the three
  terminal states. It is finer than `SessionStatus` and maps from it
  (`ACTIVITY_FOR_SESSION_STATUS`, keyed on the domain union so a new status
  is a compile error until it is placed).
- Each state owns one rhythm and no two share it: **thinking** drifts a
  dashed ring (3.2s); **streaming** blinks a block caret (1s, stepped);
  **tool** sweeps a 2px track under a ticking clock; **retrying** depletes a
  countdown ring once a second beside "attempt N of M · next in Ns";
  **awaiting_input** reuses the `StatusBadge` needs-you ring unchanged, so it
  stays the loudest thing on the screen.
- A tool call that has run longer than `TOOL_SLOW_AFTER_MS` (20s) is promoted
  to "slow": its clock and track turn attention-toned and its label becomes
  "Still running". A 40-second `bash` must never look like a 200ms `read`.
- Under reduced motion every loop freezes in a legible pose (the ring stays
  dashed, the caret stays lit, the sweep becomes a stripe) and the clocks keep
  ticking as text. Motion here is state, not decoration.
- Errors are never behind a click: a failed `ToolCallCard` opens by default
  and repeats the error's first line, in primary ink on the danger tint, in
  its collapsed row. A non-zero exit code is a danger chip in the collapsed
  row whatever the harness said the status was — except the plain `exit 1`
  of a failed call, which the ✕ already says; open, the output block always
  shows the code.
- Tool output arrives capped by the backend (4 KB per stream; longer output
  keeps the first and last 2 KB). `ToolCallCard output={{ head, tail,
  omittedBytes }}` draws the dropped middle as a labelled dashed line —
  never a silent join. stderr, when reported apart (`stderr` prop), is a
  second block marked by label and rail, not by tinting the text; a harness
  that merges the streams (OpenCode) passes one `output`.
- Tool output carries the tool's own escape codes — the container forces
  colour (`FORCE_COLOR`, `CLICOLOR_FORCE`, `TERM=xterm-256color`,
  `git color.ui=always`). `parseAnsi` (`src/util/ansi.ts`) turns it into
  styled runs and `AnsiString` renders them as spans; no HTML string exists
  anywhere in the path. SGR is kept (reset, bold, dim, italic, underline,
  inverse, the 16 colours, 256-colour, truecolor); every other sequence
  (cursor moves, erase, modes, OSC titles and hyperlinks) is stripped, not
  shown; an OSC that never closes on its line loses only its introducer,
  never the lines after it. Carriage returns are applied as a terminal
  would: on each line only the text after the last `\r` shows, so a
  progress bar collapses to its last frame. A sequence the cap cut in half is
  dropped whole: at the end of the head always, at the start of the tail
  when the caller says so (`cutStart`) and the fragment plausibly is one
  (`10ms` and `5m ago` are text), so `[01;3` never appears. Head
  and tail are parsed apart, so a style never carries across the elision;
  each `LogStream` line is parsed alone for the same reason.
- Inside tool output the tool's colours *are* the content: pytest's red
  `FAILED` is pytest's, not a status of ours, so the 16 colours map onto
  their own `--ds-ansi-*` tokens (`ansiColors` in `palette.ts`), not the
  tones. Each slot clears 4.5:1 on the field background in both modes
  (the test checks): "black" on dark is a mid grey, "white" and "bright
  black" on light are text-secondary greys, so a tool that dims a path
  never makes it vanish. 256-colour and truecolor foregrounds keep their
  hue and chroma but have their OKLCH lightness clamped into the theme's
  text band (`ansiForegroundLightness`), emitted as a `light-dark()`
  pair; backgrounds pass through, and so does the ink a tool chose for its
  own fill — the clamp is for text on *our* field. Bold is
  weight, dim is opacity, inverse swaps ink and fill. The card's own
  chrome — rail, chips, labels — still never means anything by hue alone.
- The model's reasoning is a `ThinkingBlock`: no avatar, no frame, muted
  ink, a `row-compact` row — quieter than a message and distinct from a tool call.
  Collapsed by default to brain · label · one-line preview · duration.
  While streaming the brain sits inside the `thinking` rhythm's drifting
  ring and the preview follows the latest line. Dozens in a row must read
  as a faint ledger.
- What an agent records with `dude event` takes the same muted line.
  `ChatEvent` is zap · the type in mono · one line of the data (a scalar
  as-is; an object as up to three `key=value` pairs then "…";
  `summarizeEventData`) · the recording role's avatar · the time, and
  expands to the data pretty-printed (`prettyJson`). `ChatProgress` is the
  line with a 2px bar under it — determinate with "n of m" when `done` and
  `of` are known, a sweep otherwise — updated in place, never appended.
  While running the fill breathes and the sweep moves on the shared
  cadences; `ended` freezes both where they got to, with a check when
  complete and a stop mark when not, so a finished or stopped bar never
  reads as still going. It is a `role="progressbar"` with values only when
  determinate.
- Every agent turn can carry its context size (`contextTokens`, shown as
  `ctx 15.2k / 744k` against `contextWindowTokens`, attention ink at 80%
  and danger at 100% — the same thresholds a cost takes against its
  budget) and its output tokens (`outputTokens`, `out 1.2k`). A cost the
  harness does not report is `costUsd={null}` and renders as `—` with a
  title, never as `$0.00`: zero is a price, unknown is not. Tokens still
  show when cost is unknown.

### Human intervention

- The two ways a person acts on a session are distinct on four channels in
  `ChatComposer`: focus tint, context line, button label, button colour.
  **Answer** (session blocked on a question) has an attention-filled button —
  the same hue as needs-you, so the answer visibly closes it. Its context line ("Answering
  Orchestrator: …") and the offered choices are neutral at rest; a choice
  chip takes the attention tint only on hover. **Steer** (session running) is
  accent-toned. A steer lands at the agent's next step — the harness takes
  it while a tool runs and the model reads it before its next call, in the
  same turn, nothing cancelled — so sending one costs nothing and plain
  Enter sends it. `landsHint` says where, beside the keys: "Lands after the
  current tool", "Lands at the agent's next step", or "Lands when the turn
  ends" for a harness that reads only between turns. **Interrupt now** —
  the costly one, which stops the turn — is a checkbox, never a key, and
  never a fallback the app takes on its own. Shift+Enter is a new line in
  both. The action row says who it is sent as (`sentAs`). Narrower than
  640px (a panel open beside the transcript) the row wraps, the button
  stays at its end, and the key hints go before where a steer lands does.
- The question itself is a turn: `QuestionCard`. While it waits it is the
  one loud turn a transcript is allowed, and it is loud once: the attention
  wash and 2px bar. Inside it the ink is neutral — the transcript header's
  Needs-you badge already names the state, the wait clock is muted, and the
  avatar is marked live. In grayscale it is still the only barred, tinted
  turn. The offered choices are shown once, as one-click chips in the
  composer; the card lists them only with `onChoose` (then they are its own
  buttons) or once it has settled, as the record of what was offered.
  `answeredAt` settles it: no wash, "Answered · after 4m 12s", and the answer follows as
  its own `intent="answer"` turn — the card never quotes it, so nothing is
  said twice. `dismissed` is for a question the session died on: "Not
  answered", settled, and it never rings. The waiting card is a polite live
  region announced once; the clock sits outside it.
- The same tints mark the human turns in the transcript (`ChatMessage
  intent="answer" | "steer"`), so interventions are scannable in a long
  conversation.
- A steer the agent has not read yet is queued. `ChatMessage
  deliveredAt={null}` shows it on three channels — a dashed bar, a
  "Queued" chip with a clock, and one line under the body saying where it
  lands (`pendingReason`): "Lands after **Bash** finishes." (the running
  tool, named), "Lands at the agent's next step.", "This agent reads
  messages only between turns — lands when this turn ends.", "Lands when
  the run resumes.", "Lands when the agent starts.". `onInterrupt` puts
  "Interrupt now" on that line: re-sending it to be heard at once is a
  person's named choice, never something the app falls back to. The intent
  tint is kept throughout: it is still a steer.
- Once read (`deliveredAt` with `read`) it loses the queued treatment and
  **sits in the transcript where the agent read it**: between the tool call
  it waited for and the next one, not where it was typed. Its header says
  "sent 14:30 · read 14:30:41, after Bash" (`readAfter`). A lux that reports
  only the handoff gives `deliveredAt` without `read`: the header says
  "delivered" and claims no read time, and the turn stays where it was typed.
- `failed` replaces the queued line with a danger one, "Not delivered:
  <reason>", and `onRetry` puts Retry on it. It is not queued any more:
  nothing will land.
- The task prompt is usually authored by dude, not a person. It is
  `ChatMessage role="system" intent="prompt"`: the neutral prompt frame,
  dude's face (`avatar`, which the app gives — the design system carries no
  brand image) and the "Task" tag say who wrote it without a third tint.
  It is signed with dude's name for the task: one of The Dude, El Duderino,
  His Dudeness, Duder, picked by the task's id so a task always hears from
  the same one. His other acts on a task (the pull request he opens, a Run
  he aborts) wear the same face and name. Prompts clamp at eight lines with
  a "Show all" control, measured after layout so a short prompt gets no
  control (`maxLines` overrides).

### A task's Chat (the conductor)

- A task's **Chat** is its conversation with its conductor, the agent
  people talk to about a task — any task, delivered weeks ago or not
  started. It is a transcript like a session's (`ChatTranscript`,
  `ChatMessage`, `ChatAside`, `QuestionCard`), with three differences.
- **`TaskHistory`** heads it, pinned: the task's history in one line on
  the raised shade — how it went, what ran (a fan-out folded, "reviewers
  ×3", arrows muted between), what it came to (findings, cost). Before
  anyone has written it is all there is, over an empty composer.
- **The turns.** A person's message is `intent="message"`: signed, no tag
  and no tint — talking, not intervening. dude's briefing of the conductor
  is `role="system" intent="briefing"`: the prompt's frame, clamp and
  face, tagged "Briefing", signed with the task's dude name; the message
  it ends with is the person's own turn just before it, never said twice.
  The conductor answers as `role="conductor"`: its round violet face. dude's
  notices there name him (`ChatNotice by`): "El Duderino: Parked while
  nobody is writing". A parked conductor is quiet: nothing about it is
  amber, and nothing counts it as needing you; its question is the usual
  `QuestionCard`, loud as any. A task that has had several conductors
  shows each conversation in order, one after the other in the same
  transcript: an ended one's turns with nothing to answer, closed by
  dude's notice that it ended; only the latest has the composer.
- **The composer is `mode="chat"`**: "Ask about this task…", Send, the
  accent's focus, no interrupt (a message starts the conductor's next
  turn, never cuts one short), and `to` — "To **Conductor** · read-only" —
  where "Sent as" would be. While the conductor asks, it is the answer
  composer, as in a session.

### Picking a stopped task back up

- A task that stopped — a person aborted it, its agent died, or delivery
  stopped and its owner said Stop — says so where an escalation would: a
  `Callout` under the header (attention for an abort, danger for a
  failure), in the escalation's grammar. First line: **who stopped what,
  and why**, quoted ("Ana aborted the implementer 1h ago: “…”"), with the
  way to the session. Second line, secondary ink: what it left — the commit
  it pushed (mono) and its counts, and whether its workspace and
  conversation are still kept and until when ("kept until Thu 8 Oct"), or
  that they are gone.
- Then the **three ways back**, as buttons, the one that fits first and
  primary: **Resume…** (the same agent, while it is kept), **Try again…**
  (a new agent on the same branch, from what was pushed), **Start over…**
  (a new attempt on a new branch). Each opens one `Dialog` whose
  `ChoiceList` holds all three, so the choice is made with what each keeps
  and throws away in view: under it, one line per thing the task has —
  conversation, workspace, branch, session — marked *kept*, *new* or
  *gone*. A way that is not possible stays in the list with its reason
  (`disabledReason`: "No longer kept: lux keeps a stopped session 7
  days"); it is never simply missing. A note for the agents (optional)
  follows — on Resume it is the agent's next message, otherwise one of the
  task's decisions.
- Only the task's owner picks it back up; anyone else reads, in the
  notice's place for the buttons, whom it waits on and how to make it
  theirs ("Only Márcio, its owner, can pick it back up. Take over the task
  to do it yourself.").
- A stopped session's end strip (`RunEnded`) offers the same: **Resume…**
  while it is kept, and **Other ways…**, both opening the dialog.
- **Nothing of an earlier attempt is hidden or lost.** The pipeline shows
  the current attempt ("Pipeline · attempt 2") with one muted line under it
  for each earlier one — how it stopped, and **Show attempt 1**, which
  folds it open: its steps as they ended, who stopped it and why, who set
  it aside and their note, its branch and last commit, and what lux still
  keeps. The Sessions list groups by attempt under small-caps heads
  ("Attempt 2 · current", "Attempt 1 · set aside") once there is more than
  one; a set-aside session reads as it ended, its strip saying it was set
  aside and the way to where the work goes on. Activity is one timeline
  across attempts.

### Nesting

- A subagent's conversation nests inside its parent's (`ChatThread`). The
  2px rail is the child's **role** colour, so depth is a row of differently
  coloured rails, not shades of grey. Indent is 12px then 8px; depth 2 starts
  collapsed; depth 3+ shows only its header with an Open action. A collapsed
  live thread keeps its activity in its header.
- Only the watched session pins its plan (`AgentPlan sticky` under the
  transcript header). A child's plan lives inside its own thread, flat and
  collapsed. Two pinned plans would be two competing answers to "what is it
  doing".

### Triage (the sidebar)

- `src/tokens/triage.ts` maps every domain status to one of six buckets:
  **needs_you**, **active**, **ready**, **failed**, **waiting**, **done**.
  Keyed on the domain unions, so a new status is a compile error until it is
  placed. Only the first four are *counted*; waiting and done are the calm
  majority and are never rolled up.
- A task's bucket is the most urgent of its own status and the sessions
  of its current run (`taskTriage`). A `running` task whose reviewer
  is `awaiting_input` needs you, whatever the macro state says.
- **Amber once per region.** In the sidebar the pinned needs-you block (tint
  and bar) is the one amber area; the filter chip, the tree row's pill, the
  asking session's activity and the roll-up counts are neutral ink, and the
  diamond marks carry the hue. In the transcript it is the header's
  Needs-you badge and the waiting question's highlight.
- The tree shows four levels — Project → Epic → Task → Session — and
  folds Runs into their task: the current run's sessions sit directly
  under it; earlier attempts fold into one "Attempt n" row each. Retrying is
  rare and must not cost every task a level.
- Levels differ in row grammar, not just indent (16px): projects are sticky
  small-caps headers, epics carry the layers glyph and a total, tasks
  lead with a status dot and a mono key, sessions sit on a guide line behind
  a role avatar. A tree four deep still reads in grayscale.
- Default open state is derived from triage and never needs three clicks: a
  project opens if anything inside is counted; an epic if anything needs you
  or is active; a task only if it needs you, down to the asking
  session. The user's toggles override these per row and survive refreshes,
  so a newly blocked item still opens its ancestors unless the operator
  explicitly folded them.
- "What needs me" must be answerable without expanding anything. The
  `Sidebar` pins a **Needs you** list across every project — task,
  who is asking and what, who it waits on; where it lives is the row's
  tooltip — above the tree; the needs-you
  filter chip shows the same set in place; and every collapsed ancestor
  carries the count. Three routes, one source (`attentionItems`).
- Selection and focus are separate (the ARIA tree pattern): ↑↓ move, →
  opens or steps in, ← closes or steps out, Home/End, Enter selects, `/`
  jumps to the search and ↓ from the search enters the tree.

### Board (the overview)

- `Board` is what the main pane shows when the sidebar selection is a
  project or an epic; a task or session opens the transcript. It takes
  the same `NavProject` / `NavEpic` the sidebar takes — `boardScope` maps a
  `NavRef` to one or the other — so the two can never disagree about what
  exists or what needs you.
- `src/util/boardModel.ts` folds the eleven task statuses into **five
  lanes**: Intake (received, intake, confirm plan), Queued, In progress
  (running, needs you), Review (in review, ready to merge), Closed (done,
  failed, aborted). Keyed on the domain union, so a new status is a compile
  error until it is placed. All five lanes are always drawn, in that order;
  an empty lane folds to a labelled rail rather than an empty box.
- **Needs-you is not a lane.** It strikes in Intake (a plan to confirm) and
  In progress (an agent asking), so it is a card treatment and a sort order,
  exactly as it is a row treatment in the tree. Within a lane, cards sort by
  triage rank — needs you, active, ready, failed — then keep their order.
  Failed sits in Closed with its danger mark; aborted stays neutral.
- A card is three lines and nothing more: status dot, mono key, epic (project
  boards only) and time in lane; the title, clamped to two lines; who and
  what it cost. Running cards show the working roles and the
  deepest live activity. Needs-you cards show the asker and the question in
  attention ink and take the attention wash and bar. Nothing else on the
  surface is coloured.
- Time in lane is `Duration format="age"` — one coarse unit (`45m`, `4h`,
  `3d`), one clock per board ticking once a minute. Seconds on a board are
  noise, and fifty cards must not own fifty timers.
- **Nothing drags.** Every transition between lanes is the workflow's — the
  scheduler starts work, the agent opens the PR, the checks make it ready,
  the merge closes it — and the two a person performs (confirm a plan, abort
  a run) are decisions with context, taken in the transcript. A card is a way
  in, not a handle; clicking a needs-you card lands on the asking session.
- One tab stop per board: ↑↓ move within a lane, ←→ across (same row, or the
  last one there is; folded lanes are skipped), Home/End, Enter/Space open.
  Past `cap` cards a lane shows "N more"; since urgent cards sort first, what
  folds is only ever the calm tail.
- `groupBy="epic"` turns a project board into **swimlanes**
  (`boardSwimlanes`): a row per epic in the project's order — the order the
  operator set — then *No epic*, each across the same five lanes under one
  shared head row. The lane header is the epic title with the layers glyph,
  a count, the roll-up and the spend; it folds the row to its header
  (`collapsed` / `onCollapsedChange`, keyed `epic:<id>` / `none`). An empty
  epic keeps its row so its position stays visible. Cards drop their epic
  line; ↑↓ walk a column across rows. The header's "…" is the app's
  `RowMenu` via `laneMenu`.

### Sessions (live work on a task)

- A task's sessions and the one open share its page: the **Sessions** tab
  is the task's sessions down the left (`SessionList`, newest first, the
  open one `current`) and the open session beside them. There is no
  separate session page: a link to a session (`#/session/<id>`) opens its
  task on that tab with that session open, so the task's people, pull
  requests and findings stay one tab away while you watch an agent. Only a
  session whose task cannot be learned stands on its own, and says the way
  to its task when it ends.
- The open session is `SessionHeader` (whose agent, for whom, its model,
  status, cost, tokens and elapsed, Pause / Abort, and the terminal where
  the rail is not — on every view, so the
  numbers never depend on the rail being there), then **one bar**: a
  `Segmented` switch at the control size (it follows the density) between **Conversation**, **Changes** and **Events**
  (debugging, last), and on Changes the diff's own controls after it
  (`LiveDiff`'s `leading`). One row, one left edge, whichever view is
  shown: never a row of tabs over a row of tools. Each view fills the same
  place under the bar; none opens over the page.
- **Servers live on the task's tab**, not in a session: a Run's servers
  are the task's while that Run serves it, so the session bar has no
  Servers toggle and nothing opens beside the conversation for them. A
  branch preview's session, which has no agent, says so in place of the
  composer and links to its task's Servers tab (`#/task/<id>/servers`).
- **Changes** in the switch carries its file count and, while the agent is
  changing files, the breathing dot. That dot is the one "live" on the bar:
  the header's status already says Running, so the diff has no Live pill.
- Beside the conversation, a `SessionRail` on the chrome shade: what the
  header does not say (agent, attempt), the tools it used (`ToolUsage`), and the files it has
  changed so far (`ChangedFiles`, a breathing dot on the label while live).
  Picking a file there opens Changes on that file alone, as picking it in
  the diff's own list does. When the session is narrower than about 820px
  the rail goes and the conversation keeps the width. Narrower than 900px
  the sessions list sits above the session.
- **The terminal is the rail's.** While the Run is alive and running
  (not paused, starting or ended) and lux gave it a terminal, the rail's
  Session block ends with `TerminalLink` ("Open terminal in lux", a new
  tab). Where the rail is not — the session narrower than about 820px, or
  on Changes or Events — the header keeps the same link as a terminal icon
  (`LinkButton iconOnly`), chosen by the same container query on the
  session's width, so the terminal is never unreachable and never shown
  twice. A branch preview has no agent session to hold it: its terminal
  stays in the run line of the task's Servers tab.
- With no session asked for, the one shown is picked once (running, else
  newest) and kept: a phase ending must not swap it under someone reading.
- **Two edges.** Everything in a session shares one outer edge and one
  inner edge. Bands — a turn's wash and hover, the pinned plan — run edge
  to edge across the transcript's column. Content sits on the chat inset
  (`--ds-space-chat-pad-x`): the session header's face, the bar's switch,
  the plan's icon (`--plan-pad-x`), every turn's face, the composer, the
  Changes and Events views. Nothing in a session uses the page's 24px pad.
- A turn's text spans its column: chat turns have no measure (`Markdown
  unmeasured`: messages, thoughts, questions), so a long line wraps at the
  column's edge, not beside empty space.
- What an agent does between its messages — a run of tool calls and
  thoughts — is one `ChatAside`: on the message text column (past the
  avatar gutter, so its left edge lines up with the words above), 4px
  between its items, and a turn's air above and below the run. Never a
  tool call on its own at the transcript's edge.
- The sessions list is 260px (200px below 1280px).
- **Changes is `LiveDiff`**: the agent's checkout against the commit the
  session started from, uncommitted work included. Its toolbar reads
  *Since abc1234 · N files +a −d*, then *Follow the agent* and Unified /
  Split (`Segmented`). It wraps before it truncates: the summary is never
  cut to "3 fil…". With nothing changed there is no summary, only the empty
  message. The last change, with the agent's face (*[face] Write
  `revenue.ts`*), heads the file list, where the change lands.
- Files down the left with their status letter (M / A / D / R on its tone)
  and counts; each file's diff under a header that sticks, with an **Open
  in the viewer** icon button (not on a deleted file). The viewer is the
  file's diff alone, split, in a `Dialog` — dude keeps a Run's diff, not its
  files, so the diff is what there is to open.
- Hunk headers are quiet (the hover wash, muted text): the changes are the
  loudest thing in a diff, never the `@@`. Added and removed lines use the
  `--ds-diff-*` tokens, with a sign and gutter as well as the tint.
- A line new since the last update flashes (`ds-flash`, once) and keeps an
  info mark down its side; the file it is in flashes in the list. With
  Follow on, the diff scrolls to the newest change. Picking a file — in the
  list or the rail — shows it alone and turns Follow off: the person has
  taken over. Turning Follow on shows all again.

### Management (menus, forms, findings)

- **RowMenu** is the one overflow menu: behind a "more" `IconButton` on a
  tree row, a board or lane header, a table row. The DS never knows the
  actions — the app passes `items` (icon, label, shortcut hint, `tone:
  "danger"`, `disabled` + `disabledReason`, separators, submenus for "Move
  to epic ›"). It opens on click, and a row that spreads `rowMenuOpeners`
  also opens it on right-click and Shift+F10 / the context-menu key. Focus
  returns to the row on close (`onCloseAutoFocus`) so arrow keys keep
  working. A danger item is still just a request: the destructive action
  itself goes behind a `Dialog tone="danger"`.
- `NavTree` / `Sidebar` take `menuItems={(row) => items | null}` (or a
  `menu` render prop for full control); a row that returns nothing draws no
  trigger. The trigger is out of the tab order and visible on hover, focus,
  or while open, so the tree's ↑↓ → ← Home End `/` are untouched.
- **Textarea** has the `Input` anatomy (label, hint, error,
  `aria-describedby`) and grows from `rows` to `maxRows` (3 → 12) then
  scrolls; never a resize handle. `mono` for commands and config.
- **MarkdownEditor** is for writing one Markdown document that is read
  rendered — a task's goal, its acceptance criteria. A note, a command or
  a reason stays a `Textarea`. It has Textarea's anatomy (label, hint,
  error, `aria-describedby`, `maxLength`, the caller's `data-testid` on the
  textarea) and `Input`'s `labelNote` ("required"), except that the hint
  sits beside the label, not under the field — the frame's footer is under
  it; the error stays under the frame.
  Empty, Preview says "Nothing to preview yet." In Preview the formatting
  buttons hide (and leave no band when they had wrapped). The field is a
  frame whose edge is its one line: a Write / Preview `Segmented tabs
  size="toolbar"` (tablist and tabpanels, one Tab stop, ← → Home End;
  Ctrl/⌘+Shift+P), and quiet
  formatting on the chrome shade — no rule under it — then the source in
  mono on the code pitch, then a footer: "Markdown", the caller's `summary`
  and `notice`, and `n / max` (attention past 90%, danger over).
  - **Grows, never scrolls.** The source grows from `minRows` with its
    content; the dialog or page scrolls. No resize handle.
  - **`fill` takes the room that is left.** In a flex column — a
    `FormStack fill` in a `Dialog size="document"` — the frame flexes into
    the height its siblings leave, with `minRows` as the floor below which
    the column scrolls instead. Content taller than that still grows it, and
    the column scrolls. One field per stack fills (a task's Goal); the
    others keep a modest `minRows`, so the empty document opens with no
    scroll.
  - **Preview is the safe path, and reads as the text will be read.** It is
    `Markdown unmeasured` in the caller's `variant` (default `message`: the
    long rhythm, 1.25em above headings) — the variant of the screen that
    shows it — and nothing else: no HTML string, no second renderer. It
    keeps at least the source's height, so toggling does not jump.
    `document` is for an editor whose output is published as an artifact.
    `breaks` passes through to it: a person writes here, so it is on
    wherever the rendered text is theirs (a task's goal and criteria).
  - **Edits stay undoable.** Ctrl/⌘+B I K E and the buttons wrap the
    selection (a placeholder, selected, when there is none); Enter
    continues a list (`- `, `2.` after `1.`, `- [ ] `) and ends it on an
    empty item. All of it goes through `execCommand("insertText")`, with
    `setRangeText` where that is gone, so Undo takes it back.
  - **Locked opens in Preview.** `locked` / `disabled`: Write is disabled
    and the caller's `hint` says why. Narrow, Quote is the first button to go.
- **A dialog that holds writing asks before losing it.** When more than a
  few words would be lost, every way out — Escape, ×, Cancel, a click
  outside — opens a `DiscardConfirm` (a `Dialog size="sm" tone="danger"`):
  the question ("Discard this task?"), what would be lost ("You have
  written 195 words…" when creating; "Your changes haven't been saved."
  when editing), **Keep writing** (`quiet`, focused on open) and
  **Discard** (`danger solid`, inside its own confirmation). Saving never
  asks. The app decides the threshold (dude's: more than 20 words in
  fields that changed).
- **Segmented or Tabs.** `Segmented` switches between two or three views
  of one thing inside a bar with other controls (the session bar, the
  editor's Write / Preview; `size="toolbar"` in a bar of `sm` buttons).
  With `tabs="<id>"` it is a tablist whose tabs control
  `<id>-<value>-panel`s the caller renders: one Tab stop, ← → Home End.
  `Tabs` is for a page's sections (underline). Do not use `TabList
  variant="segmented"` for new work.
- **Key hints** are `KeyHint keys={["mod", "Enter"]}` (`Kbd` for one cap;
  `mod` is ⌘ on Apple devices, Ctrl elsewhere), muted, at a dialog's
  `footerStart`. Help beside a form is a `HelpList` and, for Markdown,
  `MarkdownCheatsheet`.
- **Breadcrumb** says where you are: Project › Epic › KEY, each crumb but
  the last a link or button, the last `aria-current`. Middle crumbs elide in
  the middle (`elideMiddle`) so head and tail survive; the last never does.
  It replaces a Back button in the task and transcript headers.
  `size="sm"` with `current={false}` is a dialog's `context`: where the
  thing sits, as a plain path — no links, no `aria-current`, no landmark.
- **FindingRow** is the only way a review finding is drawn: severity as
  glyph + word in its tone (`FINDING_SEVERITY_SPECS`, keyed on the domain
  union), category, title, `file:line` in mono, and the status as a neutral
  `Badge` — a resolved blocking finding reads as both. Description,
  suggested fix and resolution note expand under the row. Settled rows dim;
  nothing is struck through. `FindingGroup` puts open findings first, most
  severe first (`sortFindings`), and counts what is still open; `fixedIn` is
  a slot for the app's link to the fix run.
- **ArtifactRow** is a file an agent published: a glyph for its kind
  (`artifactKind` — content type first, extension when the type is generic,
  because agents are careless with media types), the path in mono, the
  size (`formatBytes`), the producer as a role avatar with its phase, the
  age, and the app's `<a href download>`. "New" / "Updated" since the
  previous run is a neutral badge. With a `preview` the leading part of the
  row is a disclosure button (`aria-expanded`); the download link stays a
  sibling so Enter on it downloads. **ArtifactGroup** lists them under
  "Artifacts · N" and shows "No artifacts yet" only when given `empty`
  — a task without artifacts must not grow a section to say so.
- **Settings pages with sub-pages list them in the settings menu**, under
  their page, as the roles sit under Agents and Search, Memories and Index
  under Memory. Never tabs inside a settings page: the menu already says
  where you are, and a second navigation inside it is two answers to one
  question. Read-only facts about a connection (GitHub, the embedder) are a
  `Card` with a `KeyValueList` and its actions in the footer; `SettingRow`
  is for a value you change.
- **SearchResultRow** is the one way a search result is drawn (Memory's
  search): ArtifactRow's anatomy and open shade — a 32px row, chevron,
  rank, a `RefLead` for what it is, and the title; at the end, as muted
  facts, which search found it ("words and meaning", "words only",
  "meaning only") and where it lives. Why it ranked where it did is behind
  the click, as a `KeyValueList` in mono. A score is never a chip or a bar,
  and matched words are not highlighted in a hue. `SearchResultList` is the
  ordered list of them. The widths before the lead are the row's own
  custom properties, so the body indents with them.
- **RefLead** is what a thing is, in the sidebar's grammar, wherever tasks,
  epics, projects and memories are named side by side: a task's
  `StatusMark` and its key in mono, the `layers` glyph for an epic, a
  project's face, the `memory` glyph. `named` says the name after it.
  The app passes a spec (`{ type, taskKey, status, name }`), never glyphs.
- **EntityLine** is something named in a row: a face or glyph, its name in
  strong ink (`--ds-color-text-strong`: the one ink stronger than primary,
  for a name) over one muted line of detail, and badges at the end. A
  member in a table, a memory in a list. `PersonLine` is it with a face;
  **AuthorLine** is whoever wrote something: a person as themselves, an
  agent as the person it worked for with its role's tile on their face and
  "Role on KEY" beneath, and dude as the `system` avatar and why it wrote
  it — at a person's size, so a column of authors lines up. The app draws
  none of these itself.
- **RemovableList** is what something is attached to or about, each with
  a quiet remove button: compact rows 2px apart, a wash on hover, no lines.
- **SearchPicker** finds one thing by name: an `Input` that is a combobox
  over a listbox in the menu's row grammar — ↑↓ move, Enter picks, Escape
  closes the list, then the picker. The app finds (`find`), the picker
  debounces and keeps only the latest answer. Build a lookup this way, not
  from a `Select` or a list of bare buttons.
  To pick several (reviewers), it suggests before any words
  (`findOnEmpty`), heads its options in groups (`group`: "Suggested by
  GitHub", "People", "Teams"), shows one it will not pick with why
  (`optionDisabled` → a badge, "Already asked"), and empties after a pick
  (`clearOnPick`), hiding what is already picked (`exclude`) rather than
  asking again. The picks are a `RemovableList` above the field;
  Backspace in the empty field drops the last (`onBackspaceEmpty`) and
  ⌘/Ctrl+Enter sends them (`onSubmit`).
- **Ask for a GitHub login with a SearchPicker of who can review**, never
  a text field of comma-separated logins: a person does not know them by
  heart. Each option is a **GitHubUserLine** — GitHub's avatar, the name in
  strong ink, the login in mono and why GitHub suggests them beneath. A
  team is the same line with a rounded-square face (ProjectAvatar's
  shape): only people are circles.
- **A pull request's reviewers each keep a line** in PullRequestPanel:
  approved, requested changes, commented, review requested (a team too),
  and "asked again · approved before" — attention, their earlier word
  muted — for one asked again after a verdict. Faces are GitHub's.
- **Checks dude cannot read are said plainly**: "GitHub won't show dude
  this repository's checks", what the token lacks beneath, and a link to
  where it is fixed (`diagnosticAction`). Never a bare "CI unavailable".
- A page's own note: a settings page with its own audience (Memory: who
  may add, who may change) renders its own `SettingsNote`; the frame's
  default is for the rest. "From <organisation>" is `SettingSource`
  wherever it appears, a table cell too.
- **ArtifactPreview** renders by kind: Markdown as `Markdown
  variant="document"`, text and JSON in mono (JSON pretty-printed when it
  parses, as typed when it does not — a half-written result is still worth
  reading), images on a checkerboard so transparency has edges, and "No
  preview for <type>" plus the download link for anything else. Long
  content clamps at 400px behind "Show all", measured after layout so short
  content gets no control. The app fetches text and passes URLs; nothing
  here fetches.

- `PersonAvatar` is for an identified person; `AgentAvatar role="human"` is
  the anonymous human *actor* glyph in event rows. Do not use one for the
  other.
- Humans and agents differ on three channels at once: a human is a full
  circle with a ring, shows initials, and takes a muted identity colour;
  agents are squares (the conductor a round glyph), show a glyph, and
  take a vivid role colour. Nothing about a person is ever a role colour or
  a tone.
- Identity colour is `identitySlot(person)` — a hash of the id, so the same
  person is the same colour on every screen with no profile record. A
  `photoUrl` replaces the initials and nothing else changes.
- `PersonAvatarStack` overlaps by a quarter and puts the *first* person on
  top: order the list by relevance (the one it waits on, then the
  requester). Past `max`, a "+N" chip in the same shape stands for the rest
  with the full list in the title.

### Markdown

- `Markdown` renders from a typed AST (`src/util/markdown.ts`) to React
  elements. There is no HTML string anywhere in the path: raw HTML in the
  source shows literally, `javascript:`/`data:` URLs are dropped, images are
  rendered as links rather than fetched. Content is untrusted (models,
  repository files — plan §25.1).
- With `streaming`, unterminated constructs at the end of input are treated
  as open — an unclosed fence is still a code block, an open `**` is still
  bold — so nothing flickers when the closer lands. Finished messages parse
  strictly.
- `breaks` (`parseMarkdown`'s `breaks` option) renders a single newline
  inside a paragraph, list item or quote as a line break, as
  the person who pressed Enter meant, rather than CommonMark's space. Code
  blocks, code spans and headings are unaffected; the two-space and `\`
  hard breaks work either way. **The rule: on where a person writes the
  Markdown, off for agent output.** On: a task's goal and criteria (the
  editor's Preview, Read, the task screen), a person's `ChatMessage` (it
  keys on its own resolved `kind === "human"`: prompts, steers, answers),
  prompts (`MarkdownDocument breaks`, `PromptHistory`). Off (the default):
  agent turns, `ThinkingBlock`, `QuestionCard`, `ArtifactPreview`,
  `FindingRow` and anything else a model wrote, which is written to the
  standard.
- `variant="message"` (default) is a chat turn. One block sits on the chat
  line (16/22). Two or more switch to long-form rhythm: 1.5 leading, 0.75em
  between blocks, headings 1.25em above and one step smaller than in a
  document. `variant="document"` is for published artifacts: 1em between
  blocks, 2.5em above headings, h1/h2/h3 at 1.618/1.462/1.318em, a 700px
  measure and an optional outline.
- Inline code: 0.85em mono on the sunken fill, `mark` radius, a subtle inset
  ring. Code blocks: a square sunken well, 12×16 padding, no frame; the
  language and copy control appear on hover; long lines scroll sideways
  under a thin scrollbar and a shadow on the clipped edge, never wrap or
  clip. Blockquotes: a 2px muted bar and secondary ink. Lists: 1.5em
  hanging indent. Tables: no rules; a muted small header and air between
  rows.
- Code blocks share their type with `LogStream`; a ```` ```diff ```` fence hands
  off to `DiffView`, so diff colouring exists in one place.

### Density

There are two, and `comfortable` is the default. It is what the rest of
this document describes: 15px UI body, 16/22px transcript text, 32px rows,
a 40px avatar on each speaker. `compact` is for an
operator who wants more on the screen, and is opted into with
`data-density="compact"` on the root or any subtree (`ThemeProvider`
`setDensity`).

Compact is not a smaller copy of comfortable. It takes space from the few
places that have a lot of it and leaves small things alone, because
shrinking something already small makes it cramped, not dense:

| | comfortable | compact | why |
|---|---|---|---|
| main pane padding (`space-main-pad`) | 24 | 12 | big layout space: where density comes from |
| panel gap (`space-panel-gap`) | 24 | 16 | |
| space between speakers (`space-chat-gap`) | 17 | 8 | turns from one author stay 2px apart in both |
| chat turn padding (`space-chat-pad-x`) | 16 | 12 | |
| chat avatar (`size-avatar-chat`) | 40 | 32 | the gutter follows it |
| avatar gap (`space-chat-avatar-gap`) | 16 | 12 | text column at 72px, 56px compact |
| chat leading (`leading-chat`) / long-form (`leading-prose`) | 1.375 / 1.5 | 1.333 / 1.4 | 16/22 → 15/20; Markdown block gap (`md-gap`) 0.75em → 0.5em |
| thought / tool call padding (`space-aside-y`) | 4 | 2 | |
| waiting-question padding (`space-highlight-y`) | 8 | 4 | |
| code block padding (`space-code-y` / `-x`), line pitch (`size-code-line`) | 12 / 16, 20 | 8 / 12, 18 | mono text stays 13px |
| table cell padding (`space-cell-y`) | 6 | 3 | |
| composer padding / gap / field padding | 8 / 6 / 10 | 4 / 4 / 6 | |
| board card padding and gap (`space-card-pad`) | 12 | 6 | |
| rows (`size-row-default` / `-comfortable`) | 32 / 40 | 28 / 36 | board lanes, tables, menus |
| sidebar and transcript rows (`size-row-item` / `-item-sm`) | 32 / 28 | 26 / 24 | tasks, epics, projects, tool calls / sessions, thoughts, needs-you header |
| needs-you row padding (`space-attention-row-pad-y`) | 6 | 3 | |
| sidebar row gap / project gap (`space-nav-row-gap`, `-nav-section-gap`) | 2 / 12 | 1 / 6 | |
| controls (`size-control-md` / `-lg`) | 32 / 36 | 30 / 34 | a couple of px |
| body, prose (`text-md`, `text-prose`) | 15 / 16 | 14 / 15 | 1px |
| control radius (`radius-control`) | 6 | 5 | 1px |
| prose measure (`measure-message`) | 70ch | 72ch | `ch` follows the font, so two more characters a line; not chat turns, which span their column |

Everything else is shared: icons, `control-sm` and `row-compact` (28, the
floor for anything standing on its own; a `Segmented` option sits inside a
track that is the control's height, as a button is, so its option is that
less the track: 28 comfortable, 26 compact), `text-2xs`/`xs`/`sm`/`nav` (11/12/13/14) and `text-mono` (13), badge
and chip heights and padding, the document measure, the
2–6px inner gaps, the tree indent, the mark and float radii, focus rings.
`src/tokens/density.ts` is the list; `test/density.test.ts` holds it to
the rule — compact is never roomier, text and radius move at most 1px,
small tokens are not listed, the big spacing moves at least 4px and the
big-ticket padding loses at least a third. At 1440×900 compact fits 26%
more transcript lines (37.8 vs 30.1) and 35% more sidebar tree rows (23 vs
17) than comfortable.

- Body text 15px (14 compact); captions 12–13px; nothing smaller than 11px
  and only in small-caps labels, badges and key caps.
- Rows 32px by default. Use `row-compact` (28px) for logs, event streams and
  anything the operator scans rather than reads. Use `row-comfortable` (40px)
  only for rows with two lines of content — an `EntityLine` with a detail
  (members, memories) is one: `<Table density="comfortable">`. In a default
  table its two lines outgrow the 32px row in both densities, so compact
  shows no difference at all. A list of things you act on (a
  `RemovableList`, a picker's options) takes the default row and the
  default control, so it follows the density; `row-compact` and `control-sm`
  hold in both and are for what one scans or for toolbars.
- A `Table` too wide for its frame scrolls sideways inside it, never the
  page. On a phone, mark the columns that can go — `hideWhenNarrow` on the
  `Th` and on each of its `Td`s — and they are hidden while the table's
  own frame is under 560px (a container query, so a table in a narrow
  column hides them at any window size). There a `fit` label cell may
  take a second line (a name over its badge); right-aligned numbers stay
  on one. Keep what identifies a row and its numbers; hide what explains
  them (pool, fit, who uses it).
- One `primary` button per view. Most actions are `secondary` or `quiet`.
- Dialogs are for decisions and small forms — and for writing one
  document: `Dialog size="document"` is a fixed min(1120, 100vw − 48) ×
  min(900, 100vh − 48), so it never resizes while its fields grow, and the
  whole screen, square, under 640px. `aside` puts a 300px column on the
  chrome shade beside the body (where the thing sits, help for writing
  it), each scrolling on its own; under 960px it follows the body in one
  scroll. `context` puts where the thing sits above the title. Settings
  and anything browsed stay screens. The document's title field is
  `Input size="title"` — `labelNote="required"` says it after the label.
  The fields stack in `FormStack fill` (the column's width, `space-panel-gap`
  apart, and the column's height, so a `MarkdownEditor fill` in it takes
  what the other fields leave; under 960px the writing takes at least the
  whole view before the aside follows), and the document's insets are
  `space-panel-gap` too, so compact
  tightens them. Key hints go in `footerStart`, and hide under 640px, where
  there is rarely a keyboard; a failed save shows there instead, at every
  width. In a document, Ctrl/⌘+Enter submits and plain Enter never does.
  `headerActions` puts quiet actions before Close — a document's **Read**
  (`book-open`, "Back to writing" with `edit` while reading), whose key is
  Ctrl/⌘+Shift+R (browsers let a page take it; it is named in
  `footerStart`). `reading` shows the whole thing as one document in place
  of the writing and the aside: a centred column at `measure-document`,
  scrolling on its own, the footer kept (saving from Read is allowed).
  Build it as one `Markdown`, in the variant the text is read in (a task's:
  `message`, as its screen and its Preview show it; the column sets the
  width, not the variant), with `title` (the name as
  its `h1`, plain text; blank reads `untitled`, muted) and `source` as a
  list of sections, each parsed on its own so an open fence in one cannot
  swallow the next. The writing stays laid out, hidden and inert,
  underneath, so its scroll, its editors' modes and selections survive, and
  focus returns to the field that had it. Escape while reading calls
  `onCloseReading` — back to writing, never closing or asking to discard.
- Cards do not nest. A card's header and footer are told from its body by
  shade (`chrome`), not a rule.
- Empty states are one line of text and a hint, never an illustration.
- Whitespace is a signal, not a default: a section gets `space-24` above it
  because it *is* a new section, not to look airy.

### Surfaces and lines

- Regions are told apart by shade. The sidebar sits on `canvas`, the
  transcript and board panels on `surface`, cards on `raised`, and a
  panel's header, toolbar or footer on `chrome`. A bar on a different shade
  from the body under it gets no border.
- Transcript turns are not boxes. Speakers are separated by whitespace;
  a hovered row takes a full-width `row-hover` wash (3% ink) and shows its
  actions. Tool calls are a quiet filled row; status is in the glyphs.
- One emphasis pattern: a full-width tint of the tone (`--ds-tint-*`, from
  `tokens/tints.ts`) and a 2px bar in the tone at the left edge, with the
  corners on the bar side square so the bar runs straight. A steer (accent), a waiting question and the
  sidebar's needs-you block (attention), a failed tool call (danger) use
  it. Nothing else gets a coloured edge.
- A line is kept only where it is the thing's shape or meaning: a field's
  edge (the composer's is the one raised field), focus, a checkbox, a
  diff's gutter, a document's own `---` rule, a selected tab's underline.
  Table rows, dates, lanes and headers are separated by space and shade.
  `test/borders.test.ts` is the list.
- Badges, chips and pills centre their label on cap height: the label
  carries `ds-cap` (`text-box: trim-both cap alphabetic`), so capitals and
  digits sit in the optical middle instead of 1–1.5px low.

### Icons

- Icons take the ink of the text they label, usually secondary or muted,
  and brighten only on hover, selection or when active. Status and role
  glyphs keep their tone and role colours.
- Size them to the text beside them: 14px next to 14–15px text, 16px in an
  icon button, 11–12px only inside chips and badges. Strokes are 1.5 on the
  16px grid and 1.75 at 20px and up.

### Numbers and identifiers

- Anything numeric that appears in a column or ticks live is tabular
  (`ds-tnum` or the component does it).
- Money via `CostDisplay` / `formatUsd`: `$0.0042`, `$0.42`, `$12.34`,
  `$1,284`. Full precision in the `title`. Budgets colour the value at 80%
  and 100%.
- Time via `Duration` / `formatDuration`: two units maximum (`3m 12s`,
  `2h 04m`). Live durations tick once a second, never faster. `format="age"`
  is one coarse unit (`4h`) for how long something has sat in a state.
- Timestamps in event streams are `HH:MM:SS.mmm`; events within one second
  are common.
- IDs, SHAs, paths, event types, model names: monospace. The mono stack sets
  `tnum` and slashed zero.

### Colour

- Text is always a text token (`text-strong` for a name, `text-primary/secondary/muted`), never a tone
  or role colour, except inside a badge/avatar where the component owns it.
- Role colours identify *who*; tones identify *what state*. Never swap them.
- The accent is for interaction (links, focus, selection, the one primary
  button). Do not use it to mean "info".
- `success`/`danger` tones are not diff colours; use the `--ds-diff-*` tokens.

### Motion

- Only these things move: the running spinner, the 2.4s breathe on live
  states, the expanding ring on needs-you, a one-shot flash on a row that just
  arrived, enter transitions on overlays, and the activity rhythms in the
  transcript (drift, blink, sweep, countdown).
- Everything that loops must be multiplied by `--ds-motion-live` (see
  `StatusBadge.module.css`) so reduced motion freezes it in a legible state.
  Loop periods come from `--ds-cadence-*`, never from a literal.

### Tabs

- A page's tabs are `Tabs` > `TabList` > `Tab`. A tab says how many with
  `count` (a number, nothing at zero), and more than a number with
  `trailing` — one mark, a `ServerStateDot` or a `.ds-live-dot`.
- What the count stands for goes in `tooltip`: the tab is wrapped in a
  `Tooltip` below it, which opens on hover and on keyboard focus and
  closes on a press. The tab keeps its role, its selected state (its own
  `data-state`, not the tooltip's) and its place in the ←→ walk; a focus
  that selects it also shows the tip. The tooltip is supplementary: the
  name and count are still the tab's label. Never a native `title`.
- A tooltip may come and go with its data (the Servers tab has none until
  its servers load). Every `Tab` sits in the same `Tooltip` wrapper, held
  shut (`Tooltip disabled`) when it has no `tooltip`, so the button is
  never remounted and a keyboard user on it keeps focus. A tip that comes
  back stays shut until a fresh focus or hover on its tab.

### Servers

- **The task's Servers tab counts what is on**: servers with a process
  running, `starting`, `ready` or `unreachable` (`isOn`). The label is
  "Servers" and that number as the tab's count, with no count at zero.
  After it, one mark: the first `exited` or `unreachable` server's danger
  or attention dot; else the breathing `starting` dot while a server
  starts or a branch preview comes up; else the attention dot for servers
  a move stopped.
- Its tooltip (`ServersTabTip`) is headed "N servers on" or "No servers
  on", then a line per server on — its mark (icon only), its name in
  mono, its state's word and `:port` — then "Off: a, b" in muted ink for
  the rest (an exited one says so). With no run, the project's servers
  are all off. With no run and no servers defined the tab has no tooltip.
  Count, mark and tip come from one `summarizeTaskServers`, so they never
  disagree.

### Machines

- **A number moved in steps is a `NumberInput`**: − value + inside one
  field's edge, the unit muted after the number, ↑ ↓ a step (Page Up /
  Down ten, Home / End the bounds). − and + stop at `min` and `max` and
  are not tab stops. What a person types stays as typed; the caller
  refuses an off-step value with `error`, and the error names the step
  ("Whole or half CPUs: 0.5, 1, 1.5…"), in place of the hint ("In steps
  of 0.5"). A step from an off-step value lands back on the grid. Never an
  `<input type="number">`: its spinners are the browser's, not ours, and
  it throws away what it cannot parse.
- **A choice with a spec is a `Select` with `meta`**: the option's label,
  then its facts muted on the same line ("8 CPUs · 16 GiB · 80 GiB"),
  which follow it into the closed trigger and are cut before the label is.
  `description` is a line under an option in the list only ("Follows
  whichever size is the default"); `footer` sits under the list on the
  chrome shade (a note, or a link to where the options are managed).
- **How much of a host a size takes is a `FitBar`**: a 40px track in the
  success tone and the words ("50% of a host"). An unknown share draws no
  track, only "Unknown" in muted ink: nothing known must not look like
  nothing used.
- **A whole split into shares is a `ProportionBar`**: square segments as
  wide as their share, a 2px gap between them, labels inside where they
  fit. What nobody gets (`kind: "reserved"`) is hatched on the sunken
  shade; the legend under the bar repeats the hatch (`ReservedSwatch`) and
  names it, and says what the whole is on the right. Given parts take the
  info tint. It is a figure with an `aria-label` that says the same in
  words.
- **The machine a session runs on is a `MachineChip`** in its header,
  after the model and effort: the `chip` glyph, the size's name strong,
  its spec muted, on the raised shade at the chip height. Its tooltip
  (`MachineTip`) says where the size came from, that it is fixed for the
  session, and what the container actually got when lux says. It opens on
  focus and hover and stays open on a press; the chip acts on nothing.
- **The Machines settings page** (an organisation's): its note says only
  admins change sizes and that a change reaches sessions that start after
  it. Sizes are a `Table` — the name with a Default badge, CPUs, memory
  and disk right-aligned and tabular, the pool in mono ("Default pool" for
  none; a pool the runtime no longer has is a danger `Badge` with the
  warning glyph, its fit "—"), a `FitBar`, who uses it (faces and words), and a `RowMenu` (Edit,
  Make default, Remove…; Remove disabled with its reason on the default).
  lux's pools are a second `Table` with a "read … ago" meta line; then the
  memory explainer, a `Callout`-shaped box around a `ProportionBar`. On a
  phone both tables keep the name and the numbers: pool, fit and who uses
  a size, and a pool's machines and where its size is known from, are
  `hideWhenNarrow`. A
  member sees the same page with no Add and no row menu. A size is edited
  in a `Dialog` of `NumberInput`s and the pool `Select`, with the fit as a
  `Callout` under them (success, danger, or neutral when unknown), and
  removed through a `Dialog tone="danger"` that says who uses it and where
  they move.

### Images

- **Every field that takes a container image is an `ImagePicker`**, never
  an `Input`: a combobox over the organisation's images (SearchPicker's
  keys — ↑ ↓, Enter, Escape) whose list floats under the field. A row is
  the image's cube mark (on the accent tint for the default base), its
  name in mono, its description muted, a `default` badge, a newer
  version's state as a badge ("v3 waiting", "v5 building" with a dot,
  "v4 failed" in danger), and the published version on the right. It
  stores the id; there is no version to choose: whoever names an image
  runs its latest published version. Closed, it shows the chosen name and
  "v7 now". `allowNone` and `noneLabel` say what choosing none means
  there ("Use Acme's · acme-base"). Words that match nothing offer "Make
  an image FROM <words>". An archived image is listed only while it is the
  one chosen, with an Archived badge.
- **A Containerfile is edited in a `CodeEditor`**: CodeMirror 6 in a chunk
  of its own, loaded the first time one renders (a skeleton of its lines
  meanwhile), so a page without one pays nothing. Tokens colour it — no
  CodeMirror theme — so light, dark and compact follow the page. Lint marks
  are a wavy underline in the tone and a gutter dot, the reason on hover;
  completions float in the overlay's grammar, matched letters in the
  accent. The frame is a field: a header and a footer on the chrome shade
  (its name and version; its line count and what won't build), and what is
  added after the text — the dude layer — read-only on the sunken shade
  under it, told apart by shade.
- **The builder's queue is a `BuildQueueStrip`** over the images list, on
  the chrome shade: what builds now with the live dot, how many wait and
  which, Open, and the limits every build has on the right ("Rootless ·
  1.5 CPU · 1.5 GB · One at a time"). With builds off it says why in their
  place.
- **A build's progress is `BuildStages`**: square cells, waiting →
  building → pushed and published, each with its glyph and words; the
  current one on the info tint with the live dot, a failed one on
  danger's with its sentence ("ran out of memory (1.5 GB) at step 3").
  CPU and memory figures appear only when something measured them; never
  a made-up bar. The log under it is a `LogStream`.
- **An image's state in a row is `ImageState`**: words in the tone with a
  dot — "Published · 2h ago" (success), "Building v5 · 2m" (live dot),
  "Waiting · 2nd" (hollow), "v4 failed · v3 still live" (danger), "Draft
  not built" (attention).
- **History is `ImageHistory`**, PromptHistory's anatomy: every version
  newest first on the chrome shade — the draft, failed ones, rebuilds by
  dude — with who, why, when and what it was built on; the selected one's
  Containerfile in a `DiffFile` against the one before it or against the
  published one; "Publish vN again" only on a built version that is not the
  published one, and only through a confirming `Dialog`.

### Do / Don't

| Do | Don't |
|---|---|
| `<StatusBadge status="awaiting_human" />` | `<Badge tone="attention">Blocked</Badge>` |
| `<CostDisplay usd={run.costUsd} budgetUsd={run.budgetUsd} />` | `${run.costUsd.toFixed(2)}` |
| `<Td align="right" mono><Duration ms={ms} /></Td>` | `<Td>{ms / 1000}s</Td>` |
| `<AgentAvatar role="reviewer" name="reviewer-2" />` | a coloured circle with an initial |
| `variant="danger"` that opens a `Dialog tone="danger"` whose confirm is `variant="danger" solid` | a red button that acts immediately |
| square panels and cards told apart by shade | a rounded card with a hairline inside a rounded panel |
| `cursor: pointer` on everything that acts | a clickable row with the arrow cursor |
| Event stripe only for `success` / `attention` / `danger` | a stripe on every row |
| `EmptyState title="Nothing needs you"` | an SVG of a mailbox |
| `<ActivityIndicator kind="retrying" attempt={2} retryAt={t} />` | a spinner with "retrying…" |
| `<ToolCallCard name="bash" status="failed" error={err} />` | an error hidden behind an expander |
| `<ToolCallCard output={{ head, tail, omittedBytes }} exitCode={1} />` | joining head and tail as if nothing was dropped |
| `<ThinkingBlock text={reasoning} streaming />` between turns | reasoning styled as an agent message |
| `<ChatMessage contextTokens={n} contextWindowTokens={w} costUsd={null} />` | `$0.00` for a cost nobody reported |
| `<ChatMessage role="system" intent="prompt" content={phasePrompt} />` | the factory's prompt shown as a person's, unclamped |
| `<ChatMessage intent="steer" deliveredAt={null} pendingReason={<>Lands after <b>Bash</b> finishes.</>} onInterrupt={…} />` until the agent reads it, then `read readAfter="Bash"` where it was read | a steer that looks read before the agent has it, a fixed "waiting for the turn" line whatever the harness does, or an interrupt taken for the person |
| `<ChatComposer question={q} />` for a blocking question | one generic text box for everything |
| `<QuestionCard role="implementer" text={q} options={opts} askedAt={t} />` until `answeredAt` lands | the question only in the composer, gone from the history once answered |
| `<Markdown source={text} streaming />` while tokens arrive | re-parsing strictly on every token |
| `<PersonAvatarStack people={[waitingOn, requester]} />` | a row of role-coloured circles with letters |
| `<Sidebar projects={nav} selected={ref} />` and let defaults open the blocked item | expanding three levels to find "Needs you" |
| `<Board project={p} epic={e} selected={ref} />` with needs-you sorted first | eleven columns, or a draggable card for a transition the workflow owns |
| `<RowMenu items={[…, { id: "delete", tone: "danger", disabled, disabledReason }]} />` | a row of icon buttons, or a greyed item that does not say why |
| `<FindingRow severity="blocking" status="resolved" … />` | `f.severity.toUpperCase()` in red, struck through when done |
| `<Breadcrumb items={[project, epic, key]} />` in the header | a ghost `Back` button under the content |
| `<MarkdownEditor label="Goal" value={goal} onChange={setGoal} fill breaks minRows={4} maxLength={65_536} />` | a `Textarea` for Markdown with a hand-rolled preview beside it, or a `minRows={12}` that scrolls the empty dialog |
| `<Dialog size="document" aside={…} context={…}>` with a `DiscardConfirm` for writing a task | a 400px dialog that loses three paragraphs to a stray Escape |
| `<Dialog reading={<Markdown title={title} breaks source={[goal, "## Acceptance criteria", list]} />}>`, in the variant the task screen reads it in | a second modal over the first to show the same text, one string joined from the parts, or `variant="document"` spacing on text that is read as a message |
| a settings page's sub-pages as `items` of its `SettingsNavItem` | `Tabs` inside a settings page |
| `<SearchResultRow rank={1} lead={{ type: "memory" }} facts={["words and meaning"]} />` | a score chip and a progress bar on every result |
| `<EntityLine lead={face} name={…} detail={…} />`, `<AuthorLine author={…} />` | a face and two spans styled in the app's CSS |
| `<SearchPicker find={…} onPick={…} />` | an `Input` over a list of bare buttons |
| `<SearchPicker findOnEmpty group={…} optionDisabled={…} clearOnPick />` over a `RemovableList` of `GitHubUserLine`s | `<Input placeholder="logins, comma-separated">` |
| a run's servers on its task's Servers tab; `<TerminalLink>` in the session's rail | a servers panel or drawer inside a session |
| `<Tab count={on} tooltip={<ServersTabTip summary={s} />}>` | a native `title` on a tab, or "N ready" beside it |
| `<NumberInput step={0.5} min={0.5} unit="CPUs" error="Whole or half CPUs: 0.5, 1, 1.5…" />` | `<input type="number" step="0.5">`, rounding what was typed without saying |
| `<Select options={[{ value, label: "Large", meta: "8 CPUs · 16 GiB · 80 GiB" }]} />` | a label string with the spec glued on in the same ink |
| `<ProportionBar segments={[{ kind: "reserved", … }, …]} legend={…} />` | a chart library, or an app-local bar in its own CSS |
| `<ChoiceList options={[{ value, label, description, icon }, { …, disabledReason: "No longer kept" }]} />` for one of a few ways to act | radio buttons hand-rolled in the app, or an option silently left out |
| a stopped task's `Callout` with Resume… / Try again… / Start over…, each opening the one dialog | a "Retry" button that guesses which of the three was meant |
| `<ImagePicker images={…} value={id} onChange={…} />` wherever an image is asked for | `<Input mono placeholder="ghcr.io/…">` for an image reference |
| `<CodeEditor language="dockerfile" diagnostics={lint(text)} complete={…} />`, imported where it is used | a `Textarea` with a hand-rolled highlighter, or CodeMirror in the main bundle |
| `<BuildStages stages={…} />` and figures only when measured | a progress bar that guesses |

## Components

`src/primitives/` — Button, IconButton, Input (`size="title"` for a
document's heading), NumberInput (`step`, `min`, `max`, `unit`), Textarea,
MarkdownEditor (`fill`), Select (options with `meta` and `description`, a
`footer`), Checkbox, ChoiceList (a radio group of a few ways to act, each
with a sentence; `disabledReason` says why one cannot be chosen),
Badge, Card, Table (THead/TBody/Tr/Th/Td/TableEmpty), Tabs (underline for a
page, segmented in a toolbar; a tab can carry a count, a trailing mark and
a `tooltip`), Dialog (`size=
"document"` with `aside`, `context`, `headerActions` and `reading`), DiscardConfirm, Toast, Tooltip,
RowMenu (+ `rowMenuOpeners`), Skeleton/SkeletonLines/Spinner, EmptyState,
ScrollArea, FormStack (`fill`), Kbd/KeyHint (+ `modKey`)/HelpList/
MarkdownCheatsheet.

`src/components/` — the factory vocabulary:

- **StatusBadge** — every domain status; badge, small, icon-only and dot
  variants.
- **AgentAvatar** — conductor, investigator, implementer, reviewer,
  simplifier, qa_browser, plus human / system / integration actors.
- **CostDisplay, TokenCount, Duration** — the three formatters as components,
  with live ticking and budget colouring.
- **MetricTile / MetricGroup** — label, big tabular value, delta with a
  `goodDirection`, optional budget bar.
- **EventRow / EventStream / EventDayDivider** — fixed-column ledger rows with
  expandable detail and a one-shot flash for new rows.
- **SessionTreeNode / SessionTree** — recursive conductor → subagent tree
  with guide lines, live activity, and an aligned cost column.
- **DiffView / DiffFile / parseUnifiedDiff** — per-file unified diffs with
  sticky gutters, kind badges and auto-collapse for large files.
- **LogStream** — monospace, follows the tail until you scroll, then offers
  "Jump to latest"; 2000-line render window.

`src/components/` — the transcript (the operator's day-to-day screen):

- **ChatTranscript** — header with enough context to need no other panel
  (task, role, model, repo/branch, status, cost vs budget, elapsed), a
  pinned slot for the plan, the scrolling turns, and a footer slot for the
  composer. Follows the tail; stops the moment you scroll up and offers
  "N new turns · Jump to latest". A ResizeObserver keeps streaming text in
  view without a revision bump.
- **ChatMessage** — one turn: gutter + column, not a bubble. Agent turns
  carry model, elapsed, context and output tokens, cost and the live
  activity in the foot; turns addressed to the agent are framed and tinted
  by intent (task / answer / steer, and in a task's Chat briefing / message), can be queued (with where it lands),
  read (sent · read, after what), or failed, and clamp when long;
  system turns are a hairline with a label. Body is `Markdown` and grows in
  place. `avatar` puts a face of the app's own in the gutter (dude's).
- **ActivityIndicator** — thinking / streaming / tool / retrying /
  awaiting_input / completed / failed / aborted, as a full-width line or a
  badge. Distinct rhythm per state; slow-tool promotion; retry countdown.
- **ToolCallCard** — one row per call (`size-row-default`) with expandable arguments, error,
  diff (via `DiffView`), capped output with a marked elision, optional
  separate stderr, and the exit code. Running calls sweep and tick; failed
  calls and non-zero exits open by default with the error / exit in the row.
- **ThinkingBlock** — the model's reasoning between messages and tool
  calls. Collapsed to one quiet `row-compact` line with a preview and duration;
  streams with the thinking rhythm; expands to Markdown or plain text.
- **ChatEvent / ChatProgress** — what the agent records with `dude event`:
  a typed event with a one-line summary that expands to JSON, and a
  progress bar (determinate or sweep) that updates in place and freezes
  on `ended`.
- **AgentPlan** — the agent's `todowrite` list rendered in place with "N of
  M", a segmented bar, and a one-shot flash/pop when an item changes state.
  Collapsed, it shows the current item. `sticky` pins it under the header.
  `--plan-pad-x` sets its text's inset (the transcript sets the chat inset,
  so the plan lines up with the turns).
- **ChatThread** — a subagent's conversation nested in its parent's, with a
  role-coloured rail, collapsible, depth-aware.
- **QuestionCard** — an agent's question to a person as a turn. Waiting it
  is the loudest turn in the transcript (attention wash and bar, live
  avatar, a muted ticking wait clock; no badge of its own); answered or
  dismissed it becomes a plain row with an "Answered" / "Not answered" tag
  and how long it waited. While waiting, choices are chips only with
  `onChoose`; otherwise they live in the composer. Settled, they are listed.
  Waiting on someone else (`waitingOn`), the note says so and how to make
  it yours, in words everyone sees: "Waiting for Ana to answer · Take over
  this task to answer" (`kind="request"`: "…to decide"). The
  choices are shown muted and do nothing; with a mouse, hovering them says
  it again (`Tooltip keepOnPress`: a press leaves it up). The note is what
  reaches keyboard, touch and screen readers. No tab stop that does nothing.
- **ChatComposer** — answer (blocked on a question, with one-click options)
  vs steer (lands at the agent's next step; `landsHint` says where;
  interrupt now is a tick) vs prompt vs chat (a task's conductor; `to`),
  visibly different. The words leave the field only once `onSubmit`
  confirms them: resolving `false`, or rejecting, keeps them to send again
  (the caller shows why). A person's draft is never lost to a failed send.
- **TaskHistory** — a task's history in one line: how it went, what ran,
  what it came to. Heads a task's Chat.
- **Markdown** — untrusted Markdown to React from a typed AST; streaming-safe;
  `message` and `document` variants; ```` ```diff ```` hands off to `DiffView`.
  `title` renders a plain-text name as the first `h1` (blank: `untitled`,
  muted); `source` may be a list of sections, each parsed on its own.
  `parseMarkdown` / `safeUrl` are exported for consumers that need the AST.

`src/components/` — navigation (the other half of the screen):

- **Sidebar** — header, search (`/`), four triage chips with global counts,
  the pinned Needs-you list across every project, the tree, a footer.
  Loading (skeleton rows), empty, and no-match states. Search and filter are
  controlled or uncontrolled.
- **NavTree** — Project → Epic → Task → Session, flat with `aria-level`,
  full keyboard navigation, per-row open/closed overrides (controlled via
  `expanded` / `onExpandedChange` so the app can persist them), triage-derived
  defaults, and a filter that forces ancestors open. Earlier runs fold into
  "Attempt n" rows.
- The view model is pure and exported from `src/util/navModel.ts`:
  `flattenNav`, `attentionItems`, `globalCounts`, `projectCounts`,
  `taskTriage`, `workingRoles`, `ancestorKeys`. The app maps domain
  records to `NavProject[]` (joining people, activity and titles) and hands
  it over; nothing here fetches.

`src/components/` — the overview:

- **Board** — the project or epic board: header with scope, counts and
  spend; five lifecycle lanes; three-line cards; needs-you first; keyboard
  grid; "N more" past the cap; quiet, empty and loading states. No drag.
- Its view model is `src/util/boardModel.ts`: `boardColumns`, `boardCards`,
  `boardSwimlanes`, `boardScope`, `liveActivity`, `BOARD_COLUMN_FOR_STATUS`.

`src/components/` — management:

- **Breadcrumb** — Project › Epic › KEY; links or buttons, middle-elided.
- **Segmented** (with `ScreenHeader`) — two or three views of one thing;
  `tabs="<id>"` makes it a tablist; `size="toolbar"` sits level with `sm`
  buttons.
- **FindingRow / FindingGroup** — a review finding, and the open-first list
  of them; `FINDING_SEVERITY_SPECS` / `FINDING_STATUS_SPECS` are the
  vocabulary.
- **ArtifactRow / ArtifactGroup / ArtifactPreview** — files an agent
  published, expandable to their content; `artifactKind` and
  `ARTIFACT_KIND_SPECS` are the vocabulary.
- **SearchResultRow / SearchResultList** — a ranked search result, in
  ArtifactRow's anatomy, expandable to why it ranked where it did.
- **RefLead** — a task, epic, project or memory, as the sidebar draws it.
- **EntityLine / PersonLine / AuthorLine** — a named thing, a person, an
  author (person, agent for a person, or dude) in a row.
- **RemovableList** — attached things, each removable.
- **SearchPicker** — a combobox that finds one thing by name, or several
  (suggestions, groups, options it will not pick).
- **GitHubUserLine / GitHubFace** — someone on GitHub, or a team, in a row:
  avatar, name, mono login, why.

`src/components/` — servers and previews (what a run serves):

- **ServerStateMark / ServerStateDot** — a server's state in StatusMark's
  grammar; `src/tokens/servers.ts` is the vocabulary (lux's five states
  and `waiting`, for a preview's spec server before its turn).
- **ServersTabTip** and `summarizeTaskServers` — the task's Servers tab:
  one reading of its servers (which are on, which are off, the first bad
  one, whether something is starting) gives the tab its count and mark and
  the tooltip its lines. The rules are under *Servers*.
- **ServerRow / ServerList / ServerRecipeRow** — one row per server in
  StepList's grammar: mono name and port, the state with what it means
  (`describeServer` turns lux's Server into the words, in one place), the
  URL to copy or open, the actions its state allows, its log folded under
  it as `server:<name>`. Preview opens the server's URL in a new tab:
  there is no in-app frame (a preview's sign-in cookie would not reach a
  cross-site iframe). `safeServerUrl` keeps anything but an `https://` URL
  out of an `href`.
- **ServersRunLine / ServersPanel** — the run the servers live on as a
  line, and the panel: the task's Servers tab (a container: under 560px,
  a phone, its rows stack). A session has no servers panel; its servers
  are its task's. A live run's line offers Start all, Stop all and Add
  server; a branch preview's adds Stop preview, which ends the run, where
  Stop all leaves it live.
- **PreviewStages / ServersMoved** — a branch preview's stages, and the
  notice with Start all after a run moved host.
- **ServersSummary** — the overview aside's block, in PullRequestPanel's
  grammar.
- **ServerRecipeTable / ServerRecipeDialog / EnvVarRows / HostChips** —
  project settings: the definitions, the editor with validation in words,
  and a preview's egress allowlist as chips.
- **LinkButton** (a primitive) — a real link drawn as a button, for a way
  out among actions. **TerminalLink** is it for a run's lux terminal
  ("Open terminal in lux ↗"): in a session's rail, and in a branch
  preview's run line; the overview's `ServersSummary` carries it short,
  as "Terminal".

`src/components/` — machines (what an agent runs on):

- **FitBar** — how much of one host a size takes, or "Unknown".
- **ProportionBar / ReservedSwatch** — a whole split into shares, the part
  nobody gets hatched; the memory of one host between Linux and its runs.
- **MachineChip / MachineTip** — the machine in a session's header, and
  its tooltip. The rules are under *Machines*.
- **TierLine / TierMark** — a model tier in a table or a picker: its mark
  (a glyph on its tone's tint), name, and what it is for under it.
- **FlowSteps** — how something works, in steps side by side on the chrome
  shade, small-caps titles; they stack when narrow. Once per page.
- **NameChips** — suggestions under a field that takes any name, mono, the
  chosen one on the info tint; say in words that they are suggestions.
- **TierChip / TierTip** — the model in a session's header: the tier, then
  the model it requested in mono. Its tooltip says that is what dude asked
  for when the session started; never what the proxy served.
- **UsedBy** — who uses something: small faces (agents' tiles, projects'
  squares), then the words.
- **SettingsExplainer** (with the Settings pieces) — how something a
  settings page depends on works, explained once: a titled box on the
  chrome shade at the prose measure, a figure after the words.

`src/components/` — images (what an agent runs in):

- **CodeEditor** — CodeMirror 6, lazy: `language`, `diagnostics`,
  `complete`, `header`, `after` (read-only, under the text) and `footer`.
  `CodeEditorCore` is the chunk it loads; nothing imports it directly.
- **ImagePicker / ImageMark / ImageStatusBadge** — the image combobox, an
  image's cube mark, and a newer version's state as a badge.
- **BuildQueueStrip / BuildStages / ImageState** — the builder's queue in a
  line, a build's stages, an image's state in a row.
- **ImageHistory** — versions and their Containerfile diffs, with Publish
  again. The rules are under *Images*.

`src/components/` — live work:

- **LiveDiff** — a working agent's checkout against where it started, as
  it changes (the rules are under *Sessions*): files with status and
  counts, sticky file headers, Unified / Split (`splitRows` pairs each
  removed run with the added run after it), fresh lines flashing, Follow
  the agent, `leading` for the page's controls first in its toolbar, `toolbarIn` to draw its controls into a bar the page keeps mounted,
  `lastChange` as `{ face, tool, path, when }` (the face stays whole; the
  tool gives way before the file name), `onOpenFile` for the viewer,
  `selected` / `onSelectedChange`
  to pick the file shown alone from outside, `fileList={false}` for one
  file on its own.
- **DiffStat** — "+12 −3" in the diff's colours; every count of lines
  added and removed.
- **SessionHeader** — the transcript's header on its own, for a session
  whose views sit under it.
- **`.ds-live-dot`** (base.css) — the one breathing dot, on
  `--ds-color-live`: beside what is changing now (a view's name, a label).
- **SessionRail / SessionRailBlock / SessionFacts / ToolUsage /
  ChangedFiles** — the column beside a session's conversation; its facts
  are a `KeyValueList`, values to the right.
- **SessionList / SessionItem** — a task's sessions, the open one `current`.
- **FileGallery / FileViewer** — a task's files and their versions.
- **Cost** — a total, with the tokens / machine split as a hairline, and
  optionally where each part came from and whether lux has settled it.

## What is deliberately not here

- A Markdown *parser dependency*. The hand-rolled one in `src/util/markdown.ts`
  covers what agents write (headings, lists incl. tasks, fences, quotes, pipe
  tables, links, emphasis) and nothing that would need an HTML path (raw
  HTML, footnotes, reference links, setext headings, indented code). If a
  consumer needs more, it should still emit an AST, not HTML.
- Syntax highlighting in code blocks. It would need a grammar dependency and
  a second colour system; the mono type and the fence language label carry
  enough for a transcript. Revisit for the document variant. (`CodeEditor`
  highlights what it edits with CodeMirror's grammar in the tones, in its
  own lazy chunk; it is not used to render code that is only read.)
- Charts. When they arrive, series colours must come from a validated
  categorical palette, not the tones or role colours. The tokens module
  exports the raw OKLCH helpers for that.
- Virtualisation. `Table`, `EventStream`, `LogStream` and `NavTree` render
  plain DOM so any row virtualiser can be applied by the consumer; the tree
  is already a flat list of rows for that reason.
- A side panel or page layout. Those are app concerns; they compose the
  pieces here. The `Sidebar` and `Board` are chrome, not a layout: the app
  decides how they sit beside the transcript and how wide they are.
- Drag-and-drop on the board. The workflow owns every lane transition; see
  the Board rules.
