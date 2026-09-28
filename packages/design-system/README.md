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
   column. Roles have a hue *and* a glyph *and* (for the orchestrator) a
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
   time; the number is the sum and its tooltip gives the parts.

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
| Roles | `--ds-role-{orchestrator,…,qa-browser}-{fg,bg,solid,on-solid}` | Categorical identity, fixed order, never used for status. |
| Identity | `--ds-identity-{0…7}-{fg,bg}` | Eight muted slots for human avatars, picked by hashing the person's id. About half the chroma of a role colour. |
| Diff | `--ds-diff-{add,del}-{bg,bg-strong,fg}`, `--ds-diff-hunk-{bg,fg}` | Softer than the tones; read for minutes. |
| Merged | `--ds-merged-{fg,bg}` | GitHub's violet, for a merged pull request and nothing else. Not a tone. |
| Elevation | `--ds-shadow-1/2/3` | Includes the hairline ring. Theme-dependent. |
| Type | `--ds-font-sans/mono`, `--ds-text-2xs…4xl`, `--ds-text-nav`, `--ds-text-prose`, `--ds-text-mono`, `--ds-weight-*`, `--ds-leading-*`, `--ds-tracking-*` | UI body `text-md` 15px (14 compact); `sm`/`xs` 13/12 and `nav` 14 in both densities; prose 16px (15 compact). Transcript text runs at `leading-chat` 1.375 (22px at 16px); documents and multi-block Markdown at `leading-prose` 1.5; headings at `leading-tight` 1.3. `2xs` 11px for small-caps labels only; `mono` 13px. Headings `lg…4xl` are 16/20/22/26/34. |
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
  accent-toned. A steer waits for the agent's turn to end, so sending one
  costs nothing and plain Enter sends it; **interrupt now** — the costly
  one, which stops the turn — is a checkbox, never a key. Shift+Enter is a
  new line in both. The action row says who it is sent as (`sentAs`).
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
- A steer sent mid-turn is held by lux until the turn ends. `ChatMessage
  pending` shows it as queued on three channels — a dashed frame, a
  "Queued" chip with a clock, and a line under the body saying why — and
  `deliveredAt` puts the delivery time in the header once the agent has
  it. The intent tint is kept throughout: it is still a steer.
- The task prompt is usually authored by dude, not a person. It is
  `ChatMessage role="system" intent="prompt"`: the neutral prompt frame,
  the system avatar and the "Task" tag say who wrote it without a third
  tint. Prompts clamp at eight lines with a "Show all" control, measured
  after layout so a short prompt gets no control (`maxLines` overrides).

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
  status, cost, tokens and elapsed, Pause / Abort — on every view, so the
  numbers never depend on the rail being there), then **one bar**: a small
  `Segmented` switch between **Conversation**, **Changes** and **Events**
  (debugging, last), and on Changes the diff's own controls after it
  (`LiveDiff`'s `leading`). One row, one left edge, whichever view is
  shown: never a row of tabs over a row of tools. Each view fills the same
  place under the bar; none opens over the page.
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
- With no session asked for, the one shown is picked once (running, else
  newest) and kept: a phase ending must not swap it under someone reading.
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
- **Breadcrumb** says where you are: Project › Epic › KEY, each crumb but
  the last a link or button, the last `aria-current`. Middle crumbs elide in
  the middle (`elideMiddle`) so head and tail survive; the last never does.
  It replaces a Back button in the task and transcript headers.
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
  agents are squares (the orchestrator a round glyph), show a glyph, and
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
floor), `text-2xs`/`xs`/`sm`/`nav` (11/12/13/14) and `text-mono` (13), badge
and chip heights and padding, the document measure, the
2–6px inner gaps, the tree indent, the mark and float radii, focus rings.
`src/tokens/density.ts` is the list; `test/density.test.ts` holds it to
the rule — compact is never roomier, text and radius move at most 1px,
small tokens are not listed, the big spacing moves at least 4px and the
big-ticket padding loses at least a third. At 1440×900 compact fits 26%
more transcript lines (37.8 vs 30.1) and 35% more sidebar tree rows (23 vs
17) than comfortable.

- Body text 15px (14 compact); captions 12–13px; nothing smaller than 11px
  and only in small-caps labels and badges.
- Rows 32px by default. Use `row-compact` (28px) for logs, event streams and
  anything the operator scans rather than reads. Use `row-comfortable` (40px)
  only for rows with two lines of content.
- One `primary` button per view. Most actions are `secondary` or `quiet`.
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

- Text is always a text token (`text-primary/secondary/muted`), never a tone
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
| `<ChatMessage intent="steer" pending />` until lux delivers it | a steer that looks read before the agent has it |
| `<ChatComposer question={q} />` for a blocking question | one generic text box for everything |
| `<QuestionCard role="implementer" text={q} options={opts} askedAt={t} />` until `answeredAt` lands | the question only in the composer, gone from the history once answered |
| `<Markdown source={text} streaming />` while tokens arrive | re-parsing strictly on every token |
| `<PersonAvatarStack people={[waitingOn, requester]} />` | a row of role-coloured circles with letters |
| `<Sidebar projects={nav} selected={ref} />` and let defaults open the blocked item | expanding three levels to find "Needs you" |
| `<Board project={p} epic={e} selected={ref} />` with needs-you sorted first | eleven columns, or a draggable card for a transition the workflow owns |
| `<RowMenu items={[…, { id: "delete", tone: "danger", disabled, disabledReason }]} />` | a row of icon buttons, or a greyed item that does not say why |
| `<FindingRow severity="blocking" status="resolved" … />` | `f.severity.toUpperCase()` in red, struck through when done |
| `<Breadcrumb items={[project, epic, key]} />` in the header | a ghost `Back` button under the content |

## Components

`src/primitives/` — Button, IconButton, Input, Textarea, Select, Checkbox,
Badge, Card, Table (THead/TBody/Tr/Th/Td/TableEmpty), Tabs (underline for a
page, segmented in a toolbar; a tab can carry a count), Dialog, Toast,
Tooltip, RowMenu (+ `rowMenuOpeners`), Skeleton/SkeletonLines/Spinner,
EmptyState, ScrollArea.

`src/components/` — the factory vocabulary:

- **StatusBadge** — every domain status; badge, small, icon-only and dot
  variants.
- **AgentAvatar** — orchestrator, investigator, implementer, reviewer,
  simplifier, qa_browser, plus human / system / integration actors.
- **CostDisplay, TokenCount, Duration** — the three formatters as components,
  with live ticking and budget colouring.
- **MetricTile / MetricGroup** — label, big tabular value, delta with a
  `goodDirection`, optional budget bar.
- **EventRow / EventStream / EventDayDivider** — fixed-column ledger rows with
  expandable detail and a one-shot flash for new rows.
- **SessionTreeNode / SessionTree** — recursive orchestrator → subagent tree
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
  by intent (task / answer / steer), can be queued, and clamp when long;
  system turns are a hairline with a label. Body is `Markdown` and grows in
  place.
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
- **ChatThread** — a subagent's conversation nested in its parent's, with a
  role-coloured rail, collapsible, depth-aware.
- **QuestionCard** — an agent's question to a person as a turn. Waiting it
  is the loudest turn in the transcript (attention wash and bar, live
  avatar, a muted ticking wait clock; no badge of its own); answered or
  dismissed it becomes a plain row with an "Answered" / "Not answered" tag
  and how long it waited. While waiting, choices are chips only with
  `onChoose`; otherwise they live in the composer. Settled, they are listed.
- **ChatComposer** — answer (blocked on a question, with one-click options)
  vs steer (interrupts a running turn) vs prompt, visibly different.
- **Markdown** — untrusted Markdown to React from a typed AST; streaming-safe;
  `message` and `document` variants; ```` ```diff ```` hands off to `DiffView`.
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
- **FindingRow / FindingGroup** — a review finding, and the open-first list
  of them; `FINDING_SEVERITY_SPECS` / `FINDING_STATUS_SPECS` are the
  vocabulary.
- **ArtifactRow / ArtifactGroup / ArtifactPreview** — files an agent
  published, expandable to their content; `artifactKind` and
  `ARTIFACT_KIND_SPECS` are the vocabulary.

`src/components/` — live work:

- **LiveDiff** — a working agent's checkout against where it started, as
  it changes (the rules are under *Sessions*): files with status and
  counts, sticky file headers, Unified / Split (`splitRows` pairs each
  removed run with the added run after it), fresh lines flashing, Follow
  the agent, `leading` for the page's controls first in its toolbar, `onOpenFile` for the viewer, `selected` / `onSelectedChange`
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
- **Cost** — a total, with the tokens / machine split as a hairline.

## What is deliberately not here

- A Markdown *parser dependency*. The hand-rolled one in `src/util/markdown.ts`
  covers what agents write (headings, lists incl. tasks, fences, quotes, pipe
  tables, links, emphasis) and nothing that would need an HTML path (raw
  HTML, footnotes, reference links, setext headings, indented code). If a
  consumer needs more, it should still emit an AST, not HTML.
- Syntax highlighting in code blocks. It would need a grammar dependency and
  a second colour system; the mono type and the fence language label carry
  enough for a transcript. Revisit for the document variant.
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
