# @dude/design-system

The visual language for the dude control plane: a dense, live, operational
console for watching autonomous coding agents work. Closer to a trading
terminal than a marketing site.

Run the living gallery to see every token, primitive and component in every
state, dark and light side by side:

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
in place of the transcript: the same work items by lifecycle lane. The
event ledger (`EventRow`) and `LogStream` are the debugging and audit tools
behind all of these, reached when something looks off.

1. **Calm under load.** Density is the goal; noise is the enemy. Dense means
   13px body, 28px rows, 4px radii, hairline borders. Calm means one accent
   color, mostly-neutral surfaces, and status color only where it means
   something. A screen with 200 events on it should look *quiet* until one
   of them needs you.
2. **Meaning never lives in hue alone.** Every status has a tone *and* a glyph
   *and* a label. Diffs have background *and* gutter color *and* a sign
   column. Roles have a hue *and* a glyph *and* (for the orchestrator) a
   shape. Grayscale the gallery and nothing is lost.
3. **Dark is primary, light is a peer.** The operator spends hours here, often
   at night, on a big monitor. Dark mode gets the most careful contrast work
   and is the default. Light is designed alongside it, not derived from it:
   dark elevates with lighter surfaces + hairlines, light elevates with shadow.
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

<ThemeProvider>            // stamps data-theme on <html>; optional — CSS follows the OS on its own
  <TooltipProvider>
    <ToastProvider>
      <App />
```

```ts
import { StatusBadge, EventRow, CostDisplay, Table, Th, Td } from "@dude/design-system";
import { formatUsd, STATUS_SPECS, themeColors } from "@dude/design-system";
```

Peer deps: `react` and `react-dom` 19. Fonts: Inter and JetBrains Mono are
named first in the stacks with system fallbacks; load them in the host app (the
gallery loads Inter from rsms.me for convenience — do not do that in the product).

Theme control: the CSS honours `prefers-color-scheme` and
`prefers-reduced-motion` by itself. `data-theme="light|dark"` on the root
overrides the OS; `data-reduced-motion="true"` forces reduced motion.
`ThemeProvider` manages both and persists the choice.

## Tokens

All under the `--ds-` prefix. See `src/tokens/*.ts` for the source and the
gallery for every value.

| Group | Examples | Notes |
|---|---|---|
| Surfaces | `--ds-color-canvas`, `-surface`, `-raised`, `-overlay`, `-sunken`, `-field-bg` | Back to front. In dark they are neutral steps 1–4; in light, canvas is off-white and the rest are white. |
| Borders | `--ds-color-border-subtle`, `-border`, `-border-strong` | Hairlines are how dark mode separates surfaces. |
| Text | `--ds-color-text-primary`, `-secondary`, `-muted`, `-disabled`, `-inverse` | primary ≥ 14:1, secondary ≥ 5:1, muted ≥ 3:1 on surface. |
| Interaction | `--ds-color-accent`, `-accent-hover/active/subtle/text`, `-focus-ring`, `-selection`, `-hover-wash`, `-active-wash` | One blue. Same hue as the info tone. |
| Tones | `--ds-tone-{neutral,info,attention,success,danger}-{fg,bg,border,solid,on-solid}` | The only status colours. |
| Roles | `--ds-role-{orchestrator,…,qa-browser}-{fg,bg,solid,on-solid}` | Categorical identity, fixed order, never used for status. |
| Identity | `--ds-identity-{0…7}-{fg,bg}` | Eight muted slots for human avatars, picked by hashing the person's id. About half the chroma of a role colour. |
| Diff | `--ds-diff-{add,del}-{bg,bg-strong,fg}`, `--ds-diff-hunk-{bg,fg}` | Softer than the tones; read for minutes. |
| Elevation | `--ds-shadow-1/2/3` | Includes the hairline ring. Theme-dependent. |
| Type | `--ds-font-sans/mono`, `--ds-text-2xs…4xl`, `--ds-weight-*`, `--ds-leading-*`, `--ds-tracking-*` | Body is `text-md` = 13px. |
| Space | `--ds-space-0…64` | 4px grid plus 2 and 6. |
| Radius | `--ds-radius-xs…xl, full` | `md` = 4px is the default. |
| Size | `--ds-size-control-sm/md/lg`, `--ds-size-row-compact/default/comfortable` | 24/28/32 and 24/28/36. |
| Motion | `--ds-duration-fast/base/slow/deliberate`, `--ds-ease-*`, `--ds-motion-live`, `--ds-cadence-{spin,breathe,drift,sweep,blink}` | Reduced motion zeroes durations and sets `motion-live` to 0. Cadences are the periods of the live loops; every loop divides by `motion-live`. |
| Measure | `--ds-measure-message` (72ch), `--ds-measure-document` (84ch) | Prose widths for chat turns and published documents. |
| Layers | `--ds-z-base…tooltip` | |

### How the colours were chosen

Tones and role colours are OKLCH with per-slot lightness found by search, then
validated (protan/deutan simulation, normal-vision distance, WCAG contrast)
rather than eyeballed. The four chromatic tone foregrounds clear ΔE ≥ 8 under
CVD simulation and ΔE ≥ 15 in normal vision for every pair, in both modes,
while every `fg` stays ≥ 4.5:1 on the surface. The six role colours clear the
same bar across all 15 pairs. If you change a hue, re-run the search; do not
nudge by eye.

## Rules

### Status

- Use `StatusBadge` for every Run, Session and Work item state. Never a
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
  and repeats the error's first line in its collapsed row. A non-zero exit
  code is a danger chip in the collapsed row whatever the harness said the
  status was.
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
  ink, a 24px row — quieter than a message and distinct from a tool call.
  Collapsed by default to brain · label · one-line preview · duration.
  While streaming the brain sits inside the `thinking` rhythm's drifting
  ring and the preview follows the latest line. Dozens in a row must read
  as a faint ledger.
- Every agent turn can carry its context size (`contextTokens`, shown as
  `ctx 15.2k / 744k` against `contextWindowTokens`, attention ink at 80%
  and danger at 100% — the same thresholds a cost takes against its
  budget) and its output tokens (`outputTokens`, `out 1.2k`). A cost the
  harness does not report is `costUsd={null}` and renders as `—` with a
  title, never as `$0.00`: zero is a price, unknown is not. Tokens still
  show when cost is unknown.

### Human intervention

- The two ways a person acts on a session are distinct on four channels in
  `ChatComposer`: frame tint, hint text, button label, button icon.
  **Answer** (session blocked on a question) is attention-toned — the same
  hue as needs-you, so the answer visibly closes it — and plain Enter submits
  because the agent is waiting. **Steer** (session running) is accent-toned,
  says plainly that it interrupts the current turn, and requires ⌘/Ctrl+Enter
  because an accidental interrupt costs a turn.
- The question itself is a turn: `QuestionCard`. While it waits it is the
  one loud thing a transcript is allowed — the needs-you badge with its
  ring, the attention wash and 2px bar the tree row and board card use for
  the same state, the asking avatar marked live, and a wait clock ticking in
  attention ink — so an operator scanning a long chat lands on it at once,
  and in grayscale it is still the only framed turn with a solid badge, a
  bar and a clock. The offered choices are numbered chips so the question
  reads in full; they become one-click replies only with `onChoose`, since
  the composer already has them as buttons. `answeredAt` settles it: a
  hairline, no wash, "Answered · after 4m 12s", and the answer follows as
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
- A work item's bucket is the most urgent of its own status and the sessions
  of its current run (`workItemTriage`). A `running` work item whose reviewer
  is `awaiting_input` needs you, whatever the macro state says.
- `TriageRollup` is the one way a collapsed parent says what is inside it:
  a `StatusBadge` dot per non-empty counted bucket, most urgent first. It
  reuses the dot shapes (diamond = needs you, round = active, square =
  failed), so the roll-up never invents a second mark. Needs-you is the only
  count in attention ink.
- The tree shows four levels — Project → Epic → Work item → Session — and
  folds Runs into their work item: the current run's sessions sit directly
  under it; earlier attempts fold into one "Attempt n" row each. Retrying is
  rare and must not cost every work item a level.
- Levels differ in row grammar, not just indent (12px): projects are sticky
  small-caps headers, epics carry the layers glyph and a total, work items
  lead with a status dot and a mono key, sessions sit on a guide line behind
  a role avatar. A tree four deep still reads in grayscale.
- Default open state is derived from triage and never needs three clicks: a
  project opens if anything inside is counted; an epic if anything needs you
  or is active; a work item only if it needs you, down to the asking
  session. The user's toggles override these per row and survive refreshes,
  so a newly blocked item still opens its ancestors unless the operator
  explicitly folded them.
- "What needs me" must be answerable without expanding anything. The
  `Sidebar` pins a **Needs you** list across every project — work item,
  who is asking, who it waits on, where — above the tree; the needs-you
  filter chip shows the same set in place; and every collapsed ancestor
  carries the count. Three routes, one source (`attentionItems`).
- Selection and focus are separate (the ARIA tree pattern): ↑↓ move, →
  opens or steps in, ← closes or steps out, Home/End, Enter selects, `/`
  jumps to the search and ↓ from the search enters the tree.

### Board (the overview)

- `Board` is what the main pane shows when the sidebar selection is a
  project or an epic; a work item or session opens the transcript. It takes
  the same `NavProject` / `NavEpic` the sidebar takes — `boardScope` maps a
  `NavRef` to one or the other — so the two can never disagree about what
  exists or what needs you.
- `src/util/boardModel.ts` folds the eleven work item statuses into **five
  lanes**: Intake (received, intake, confirm plan), Queued, In progress
  (running, needs you), Review (in review, ready to merge), Closed (done,
  failed, aborted). Keyed on the domain union, so a new status is a compile
  error until it is placed. All five lanes are always drawn, in that order;
  an empty lane folds to a 28px labelled rail rather than an empty box.
- **Needs-you is not a lane.** It strikes in Intake (a plan to confirm) and
  In progress (an agent asking), so it is a card treatment and a sort order,
  exactly as it is a row treatment in the tree. Within a lane, cards sort by
  triage rank — needs you, active, ready, failed — then keep their order.
  Failed sits in Closed with its danger mark; aborted stays neutral.
- A card is three lines and nothing more: status dot, mono key, epic (project
  boards only) and time in lane; the title, clamped to two lines; who and
  what it cost. Running cards show the working roles (`RoleStack`) and the
  deepest live activity. Needs-you cards show the asker and the question in
  attention ink and take the tree row's wash and bar. Nothing else on the
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
  a count, the roll-up and the spend; it folds the row to 28px
  (`collapsed` / `onCollapsedChange`, keyed `epic:<id>` / `none`). An empty
  epic keeps its row so its position stays visible. Cards drop their epic
  line; ↑↓ walk a column across rows. The header's "…" is the app's
  `RowMenu` via `laneMenu`.

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
  It replaces a Back button in the work-item and transcript headers.
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
  — a work item without artifacts must not grow a section to say so.
- **ArtifactPreview** renders by kind: Markdown as `Markdown
  variant="document"`, text and JSON in mono (JSON pretty-printed when it
  parses, as typed when it does not — a half-written result is still worth
  reading), images on a checkerboard so transparency has edges, and "No
  preview for <type>" plus the download link for anything else. Long
  content clamps at 400px behind "Show all", measured after layout so short
  content gets no control. The app fetches text and passes URLs; nothing
  here fetches.


- `HumanAvatar` is for an identified person; `AgentAvatar role="human"` is
  the anonymous human *actor* glyph in event rows. Do not use one for the
  other.
- Humans and agents differ on three channels at once: a human is a full
  circle with a ring, shows initials, and takes a muted identity colour;
  agents are squares (the orchestrator a round glyph), show a glyph, and
  take a vivid role colour. Nothing about a person is ever a role colour or
  a tone.
- Identity colour is `identitySlot(person)` — a hash of the id, so the same
  person is the same colour on every screen with no profile record. Profile
  images are not in the product yet; `imageUrl` replaces the initials when
  they arrive and nothing else changes.
- `HumanAvatarStack` overlaps by a quarter and puts the *first* person on
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
- `variant="message"` (default) is the chat rhythm: 13px, 72ch, 6px between
  blocks. `variant="document"` is for published artifacts: 84ch, more air,
  an optional outline. Neither is a blog theme.
- Code blocks share their type with `LogStream`; a ```` ```diff ```` fence hands
  off to `DiffView`, so diff colouring exists in one place.

### Density

- Body text 13px; captions 11–12px; nothing smaller than 10px and only in
  badges.
- Rows 28px by default. Use `compact` (24px) for logs, event streams and
  anything the operator scans rather than reads. Use `comfortable` (36px)
  only for rows with two lines of content.
- One `primary` button per view. Most actions are `secondary` or `ghost`.
- Cards do not nest. Divide with `CardHeader`/`CardFooter` borders instead.
- Empty states are one line of text and a hint, never an illustration.
- Whitespace is a signal, not a default: a section gets `space-24` above it
  because it *is* a new section, not to look airy.

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
| `variant="destructive"` behind a `Dialog tone="danger"` | a red button that acts immediately |
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
| `<TriageRollup counts={projectCounts(p)} />` on a collapsed project | "12 items" |
| `<HumanAvatarStack people={[waitingOn, requester]} />` | a row of role-coloured circles with letters |
| `<Sidebar projects={nav} selected={ref} />` and let defaults open the blocked item | expanding three levels to find "Needs you" |
| `<Board project={p} epic={e} selected={ref} />` with needs-you sorted first | eleven columns, or a draggable card for a transition the workflow owns |
| `<RowMenu items={[…, { id: "delete", tone: "danger", disabled, disabledReason }]} />` | a row of icon buttons, or a greyed item that does not say why |
| `<FindingRow severity="blocking" status="resolved" … />` | `f.severity.toUpperCase()` in red, struck through when done |
| `<Breadcrumb items={[project, epic, key]} />` in the header | a ghost `Back` button under the content |

## Components

`src/primitives/` — Button, IconButton, Input, Textarea, Select, Checkbox,
Badge, Card, Table (THead/TBody/Tr/Th/Td/TableEmpty), Tabs, Dialog, Toast,
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
  (work item, role, model, repo/branch, status, cost vs budget, elapsed), a
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
- **ToolCallCard** — one 28px row per call with expandable arguments, error,
  diff (via `DiffView`), capped output with a marked elision, optional
  separate stderr, and the exit code. Running calls sweep and tick; failed
  calls and non-zero exits open by default with the error / exit in the row.
- **ThinkingBlock** — the model's reasoning between messages and tool
  calls. Collapsed to one quiet 24px line with a preview and duration;
  streams with the thinking rhythm; expands to Markdown or plain text.
- **AgentPlan** — the agent's `todowrite` list rendered in place with "N of
  M", a segmented bar, and a one-shot flash/pop when an item changes state.
  Collapsed, it shows the current item. `sticky` pins it under the header.
- **ChatThread** — a subagent's conversation nested in its parent's, with a
  role-coloured rail, collapsible, depth-aware.
- **QuestionCard** — an agent's question to a person as a turn. Waiting it
  is the loudest turn in the transcript (needs-you badge, attention wash
  and bar, live avatar, ticking wait clock); answered or dismissed it
  settles to a hairline with when and how long it waited. Choices shown as
  chips, one-click only with `onChoose`.
- **ChatComposer** — answer (blocked on a question, with one-click options)
  vs steer (interrupts a running turn) vs prompt, visibly different.
- **Markdown** — untrusted Markdown to React from a typed AST; streaming-safe;
  `message` and `document` variants; ```` ```diff ```` hands off to `DiffView`.
  `parseMarkdown` / `safeUrl` are exported for consumers that need the AST.

`src/components/` — navigation (the other half of the screen):

- **Sidebar** — header, search (`/`), four triage chips with global counts,
  the pinned Needs-you list across every project, the tree, a footer.
  Loading (skeleton rows), empty, and no-match states. Search and filter are
  controlled or uncontrolled. `AttentionList` is exported on its own.
- **NavTree** — Project → Epic → Work item → Session, flat with `aria-level`,
  full keyboard navigation, per-row open/closed overrides (controlled via
  `expanded` / `onExpandedChange` so the app can persist them), triage-derived
  defaults, and a filter that forces ancestors open. Earlier runs fold into
  "Attempt n" rows.
- **TriageRollup** — the counted buckets of a subtree as `StatusBadge` dots
  with counts, most urgent first.
- **HumanAvatar / HumanAvatarStack** — a person by initials and a hashed
  identity colour; stacks overflow to "+N". `identitySlot` and `initialsOf`
  are exported.
- **RoleStack** — the roles working on something right now, as xs agent
  avatars side by side (never overlapped: each must stay readable).
- The view model is pure and exported from `src/util/navModel.ts`:
  `flattenNav`, `attentionItems`, `globalCounts`, `projectCounts`,
  `workItemTriage`, `workingRoles`, `ancestorKeys`. The app maps domain
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
