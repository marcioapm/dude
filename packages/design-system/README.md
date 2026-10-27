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

The screen they have open all day is the **chat transcript** of an agent
session (`ChatTranscript` and friends): the agent's narrative, its tool calls, the
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
   column. Roles have a hue *and* a glyph *and* (for the conversation
   agents, the conductor and the brainstorm) a shape. Grayscale the gallery
   and nothing is lost.
3. **Dark is primary, light is a peer.** The operator spends hours here, often
   at night, on a big monitor. Dark mode gets the most careful contrast work
   and is the default. Light is designed alongside it, not derived from it:
   dark elevates with lighter surfaces, light with white on an off-white
   canvas and shadow.
4. **Numbers are read, compared and summed.** Every metric, cost, count,
   duration and timestamp is tabular. Costs, tokens and durations each have
   exactly one formatter, so a value in a tile and the same value in a table
   never disagree.
5. **`awaiting_input` is the only loud thing.** It is the single state where
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
   `danger` (red text; `solid` only inside its own confirmation; `quiet`,
   transparent until hovered, where it sits among quiet actions, as a task
   header's Stop beside Edit).
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
| Roles | `--ds-role-{conductor,investigator,implementer,reviewer,simplifier,qa-browser,brainstorm,subagent}-{fg,bg,solid,on-solid}` | Categorical identity, fixed order, never used for status. Six colour slots: `brainstorm` holds the conductor's values, `subagent` (a child session, a visual role only) the investigator's. |
| Identity | `--ds-identity-{0…7}-{fg,bg}` | Eight muted slots for human avatars, picked by hashing the person's id. About half the chroma of a role colour. |
| Diff | `--ds-diff-{add,del}-{bg,bg-strong,fg}`, `--ds-diff-hunk-{bg,fg}` | Softer than the tones; read for minutes. |
| Merged | `--ds-merged-{fg,bg}` | GitHub's violet, for a merged pull request and nothing else. Not a tone. |
| Peer | `--ds-peer-solid`, `--ds-peer-wash`, `--ds-peer-wash-hover`, `--ds-tint-peer{,-hover}` | Teal-cyan (OKLCH h 192) for a message between agents, drawn only as its wash (8%, 10% hovered). Not a tone, not a role. |
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
while every `fg` stays ≥ 4.5:1 on the surface. There are seven roles and
six role colour slots: the brainstorm wears the conductor's colour
(`ROLE_COLOUR_OF` in `palette.ts`), because no seventh hue at the slots'
chroma and lightness clears the bar against all six in both modes, and the
free hues near 80 sit on the attention tone, which a role must never read
as. Both are conversation agents, so they share the round shape too; the
glyph and the label tell them apart. The subagent (a child session) wears
the investigator's for the same reason: searched again over every hue at
chroma 0–0.2 and the full lightness range, only near-white (dark, L 0.95)
and near-black (light, L 0.30) clear the pair bar, and those read as text,
not identity; the best chromatic candidates miss it (dark `#ffa332` at
hue 66 sits next to the attention tone; light `#6c5ec9` is 9.7/7.1 protan/
deutan from the investigator). It is square, with the fork glyph. The six slots clear the same bar
across all 15 pairs, and every role `fg` clears 4.5:1 on its theme's
surface. If you change a hue or a surface, re-run the search; do not nudge
by eye. `test/paletteDistance.test.ts` enforces the pair bar (OKLab ΔE×100,
Machado 2009 protan and deutan at severity 1.0, slots that share a colour
counted once) and keeps every role colour more than ΔE 3 from the attention
fg, mark and solid (every way needs-you is drawn) in normal vision. `test/palette.test.ts` holds the WCAG half: the text
ladder, tone and role foregrounds, link, focus ring and button label.

The **peer** slot (`peer` in `palette.ts`) is the background of a message
between agents, and nothing else. The other message backgrounds were
taken: accent (a person's steer), attention (a waiting question, the
needs-you block), danger (a failed tool call) and the neutral row hover.
The free bands sit between the role hues; of the two candidates, teal-cyan
near h 192 (between the implementer and the investigator) beat
violet-magenta near h 320 (between the conductor and the QA pink): at its
best tuning the violet wash lands ΔE 0.4 from the steer wash for protans
(teal's worst is 1.1), and it sits on the merged and conductor violets.
Teal-cyan's solid is ΔE ≥ 9.8 from success's solid in normal vision. The
solid is held to normal ≥ 9, protan/deutan ≥ 7 against every tone solid,
the accent and every role — below the roles' own 15 / 8 / 8, because it is
never a mark of identity. The wash is 8% over the surface or raised thread,
10% hovered (`tints.peer`/`peerHover`). The resting 8% wash composited on
surface measures ≥ ΔE 1 from the resting steer wash, question wash and
failed fill in all three simulations, and ≥ 2.5 off the surface in normal
vision. The background supplements the role rail, face and Peer/Reply
label; this floor does not guarantee background-only recognition under
CVD. The highlight's 9 / 12% was too strong: at 12% the dark "Not sent"
danger ink and the tag's secondary ink on the raised thread fall under 4.5:1.

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

- Use `StatusBadge` for every Run, agent session and Task state. Never a
  `Badge` with a hand-picked tone, never a coloured dot with a tooltip.
- The mapping from status to tone/glyph/emphasis lives in
  `src/tokens/status.ts`. That table is the contract. Add a state there or it
  cannot be rendered.
- Tones mean: **neutral** = nothing happening or deliberately stopped;
  **info** = the system is working; **attention** = a person is needed or
  something is paused; **success** = finished well; **danger** = finished badly.
- `aborted` is neutral, not danger. An operator stopping a run is a decision,
  not a failure, and must not look like one in a list of failures.
- Only `awaiting_input` ("Needs you") is solid by default. Do not
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
  colour (`FORCE_COLOR`, `CLICOLOR_FORCE`, `TERM=xterm-256color`; git is
  left plain). `parseAnsi` (`src/util/ansi.ts`) turns it into
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
- **An agent that is starting says where it is**, where its first turn
  will be: `StartProgress` on the turn grid, its face in the gutter, the
  `starting` mark with its `verb` — "Starting" for a fresh agent or
  "Resuming" for one taken back up (the default, from `kind`), or a move's
  own, such as "Moving to another machine" — the stage in words ("Checking
  out repositories"), the stage's clock and, once a stage has passed, the
  total ("00:31 in all"). Its `steps` come from `startSteps`, which takes
  lux's stage and a **stage list** from the caller and the words from one
  map, `START_STAGE_WORDS` in `@dude/domain`, shared with branch previews:
  `FRESH_START_STAGES` (waiting, image, volumes, repositories, container,
  running), `RESUME_STAGES` (the same six, as lux announces each on every
  resume) or `MOVE_STAGES`
  (led by `park`, "Stopping it, keeping its work" / "Stopped, work kept");
  a stage lux reports that the list leaves out is drawn in its place. An
  optional `pool` names where it waits: `waiting` reads "Waiting for a
  machine in highmem" while it is current, and "Machine found" once done. Its line opens the stages as a
  stepper (done with their times, the current one live, the rest hollow,
  `aria-current="step"`). With no stage (an older lux) it is the verb and
  the total clock, nothing to open. Past the app's threshold, one calm
  line in secondary ink says why it can take a while, with lux's reason in
  muted mono; never amber. A failed start is the transcript's danger
  `Callout` naming the stage and lux's reason, with Try again, the stages
  folded under it, the failed one crossed: "Did not start: failed while
  getting the image ready: `pull failed`. Nothing was spent on the
  model." With no stage it is "Did not start: <reason>", and a reason
  that already ends in `.`, `!` or `?` gets no second stop. The reason is
  the runtime's raw words, in mono; `failure.plain` (optional) sets it in
  body text, for a sentence the app wrote itself. A polite live region says each
  stage once; the clocks are `Duration dot={false}` timers outside it. It
  is replaced by the agent's first turn, and the finished start is the
  resume line's tooltip, in the same words.

### Human intervention

- The ways a person acts on an agent session are distinct on four channels in
  `ChatComposer`: focus tint, context line, button label, button colour.
  **Steer** (agent session running) is
  accent-toned. A steer lands at the agent's next step — the harness takes
  it while a tool runs and the model reads it before its next call, in the
  same turn, nothing cancelled — so sending one costs nothing and plain
  Enter sends it. `landsHint` says where, beside the keys: "Lands after the
  current tool", "Lands at the agent's next step", or "Lands when the turn
  ends" for a harness that reads only between turns. **Interrupt now** —
  the costly one, which stops the turn — is a checkbox, never a key, and
  never a fallback the app takes on its own. Shift+Enter is a new line in
  both. The action row says who it is sent as (`sentAs`). The row wraps
  whenever its items do not fit, at any width, the button staying at its
  end: nothing in it is ever squeezed under its neighbour (the welcome's
  link chips, "To **Brainstorm**" and its model chip take a line of their
  own when they must). Narrower than 640px (a panel open beside the
  transcript) the key hints go before where a steer lands does.
- **An answer is given in the question's own turn**, never in the
  composer. While the agent waits on the reader (`waitingFor`) the composer
  steps back to one quiet line on the hover wash — "Implement is waiting
  for your answer above" — with **Write to the agent instead** (accent
  link), which brings the composer back for a message that does not answer;
  the question stays open. It lasts for that question: `waitingKey` (the
  question's id) says which, and the next question steps the composer back
  again, even when it is asked in the same breath as the answer.
- **The question waiting on the reader is in view.** `ChatTranscript
  keepInView` names its turn (a selector): following the tail stops at
  its top, so a transcript opens on the question rather than on the
  replies and notices after it, and what arrives later does not push it
  out. The reader's own scroll, or Jump to latest, lets it go; the next
  question named is scrolled to again. Editable fields and control activation
  keys do not release it; unconsumed scrolling keys from descendants do.
  In the live gallery, Show held question opens an ask with later notices;
  Add later turn demonstrates the hold, manual scrolling releases it, and
  Next question selects a new ask.
- The question itself is a turn: `QuestionCard`. While it waits it is the
  one loud turn a transcript is allowed, and it is loud once: the attention
  wash and 2px bar. In grayscale it is still the only barred, tinted turn.
  With `onSubmit` the turn **is the answer form**, laid on the turn's own
  grid — no card, no box:
  - **One question** (`text` + `options`, or one `items` entry): no tabs.
    Choices are rows whose mark (a radio dot) carries the pick, the label
    in prose and a one-line `description` under it in secondary ink. A
    click on a choice answers it at once. **Something else…** is always the
    last row — the design system adds it, the agent never lists it — and
    opens a field in place, focused; Enter or Answer sends. A question with
    no choices is the field alone. `multiple` ticks boxes, then Answer.
  - **Several** (`items`, 2–4): tabs as plain words under a rule tinted
    toward attention (a real `tablist`), a filled success check on each
    answered tab, and a last **Send · n/N** tab. Picking does not send;
    Next (or Enter) moves on, Back goes back, tabs are clickable. The last
    tab reviews every answer with **Change**, takes an optional note — with
    images, in the composer's tray and paperclip — and **Send answers**,
    off until every question is answered. Focus goes to the new tab's
    choices on Next/Back, into the field when it opens.
  - A choice the agent `recommended` says "agent suggests" in muted ink; it
    is never preselected. For a one-answer question own words replace the
    pick; for a `multiple` one they add to the ticks.
  - **Keys**, outside a field: 1–9 pick the choice with that number (the
    number shows on hover or while the form has focus), ←/→ move between
    questions, Enter is Next / Send. Typing in a field never triggers them,
    and none of them act on a focused button or tab (Change, Back, Send, a
    question's tab), except ←/→ on a tab, which move between tabs, and
    Enter, which is that button. A one-answer question's choices are a radio group
    with one tab stop (the pick, else the first row); ↑/↓ move focus within
    it without picking, Space picks. ←/→ are not the group's arrows: they
    stay the question tabs'. Several-answer choices are a stop each.
  - Answer and Send take the attention fill (the hue of needs-you: they
    close it); Next is secondary until its question is answered.
  - The form's state is the app's to keep (`draft` in, `onDraftChange`
    out), so a reload does not lose picks; nothing reaches the agent before
    Send. `onSubmit` resolving `false` or rejecting leaves the form as it was.
  `answeredAt` settles it: no wash, "✓ Answered · by Ana, after 4m 12s"
  (`answeredBy`), and with `answers` the turn **is the record** — each
  question in secondary ink with its answer under it after a short dash,
  own words in quotes marked "in Ana's words". No separate answer turn: a
  note sent with several answers is the person's own message after it.
  `dismissed` is for a question the agent session died on: "No longer
  needed", settled, and it never rings. `waitingOn` (someone else's) shows
  the questions with their choices muted and how to take it over — no form.
  The waiting turn is a polite live region announced once; the clock sits
  outside it.
- The same tints mark the human turns in the transcript (`ChatMessage
  intent="answer" | "steer"`), so interventions are scannable in a long
  conversation. `intent="answer"` remains for answers kept before the
  question's turn became their record.
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
- **What dude told an agent** that no one typed — where a machine move left
  it, what lux kept it on, who owns a session now — is `ChatMessage
  role="system" intent="told"`:
  the briefing's grammar (dude's face and name, the prompt's neutral frame
  and clamp) tagged "Told", just before the agent's first words after it.
  A person's message read on the same resume stays their own turn.
- **A decision with no agent to ask it** sits in the task's notices, in the
  escalation's grammar, not as a `QuestionCard`: a `QuestionCard` is a turn
  and needs a transcript. An adopted branch reviewed under Deliver waits on
  its owner before dude publishes it: an attention `Callout`, "**Open the
  pull request?** Review is done on `stack/top`. dude opens its pull request
  into `stack/base` once you say how.", branch and base in mono, then
  **Open** (primary) and **Draft** (secondary). Only the owner is offered
  them; anyone else reads "Waiting for Ana to decide.". With a conductor
  deciding, the same question is its gate question in Chat.

### A task's Chat (the conductor)

- A task's **Chat** is its conversation with its conductor, the agent
  people talk to about a task — any task, delivered weeks ago or not
  started. It is a transcript like an agent session's (`ChatTranscript`,
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
  The conductor answers as `role="conductor"`: its round face. dude's
  notices there name him (`ChatNotice by`): "El Duderino: Parked while
  nobody is writing". A parked conductor is quiet: nothing about it is
  amber, and nothing counts it as needing you; its question is the usual
  `QuestionCard`, loud as any. A task that has had several conductors
  shows each conversation in order, one after the other in the same
  transcript: an ended one's turns with nothing to answer, closed by
  dude's notice that it ended; only the latest has the composer.
- **A conducted task.** When the conductor takes the task's decisions,
  each Run it started is a **`ChatRunLine`** where it started: one
  collapsed line on a 2px rail in the Run's role colour — face, role,
  what it is, `StatusMark`, its facts — the whole line a button that opens
  the Run's agent session. A decision the delivery waits on is dude's notice,
  `ChatNotice kind="decision"`; something that wakes nobody (an approval, a
  green check) is `kind="notice"`. **`DeciderLine`** sits above the
  composer on the raised shade: who decides ("The conductor decides ·
  waiting on it: …", or "Deliver decides"), and one quiet action — Let
  Deliver finish it.
- **Starting is a choice.** **`StartChoice`** lays the ways to start side
  by side, equal cards on the raised shade, each its own button: no
  primary, nothing preselected. Narrow, they stack. Where the cards show
  (a task's Overview), its header does not repeat them; on any other tab
  the header carries the same two, equal.
- **The composer is `mode="chat"`**: "Ask about this task…", Send, the
  accent's focus, no interrupt (a message starts the conductor's next
  turn, never cuts one short), and `to` — "To **Conductor** · read-only" —
  where "Sent as" would be. While the conductor asks, it steps back to the
  waiting line, as in an agent session; the answer is given in its
  question's turn.
- **Attempts.** **`AttemptDivider`** marks where one attempt ends and the
  next begins in a task's Chat: ChatNotice's hairline (`border-subtle`)
  either side of "Attempt 2 · started by Ana · 1m ago" (the age a
  `Duration`), and the note quoted under it. Only the first rule is a
  separator, named "Attempt 2, started by Ana"; the words and the note are
  plain text, so nothing flattens them away. The earlier attempt's turns sit
  above it inside **`EarlierTurns`** (a `group` named "Attempt 1's
  conversation"), muted but whole: its text is reachable and selectable,
  never `inert`. Nothing in it can be answered or opened, by what renders:
  `EarlierTurns` provides `useEarlier()`, and a `ChatRunLine` inside is a
  plain line (no button, no chevron) while a `QuestionCard` shows no form or choices.

### Peers (agents on an epic)

- A task's agent can read and write to its peers: the other tasks on its
  epic (and, once links exist, tasks linked to it by "depends on"). A
  **`PeerMessage`** is the turn for it: on the **peer wash**
  (`--ds-peer-wash`, `--ds-peer-wash-hover` hovered), the sender's role
  face and a 2px rail in the sender's role colour (the thread rule, as
  `ChatRunLine`): the wash says "between agents", the rail says who. In
  the transcript and in a Coordination thread alike, so a peer message
  never reads as the implementer's own turn.
  "ABS-4 Implementer → ABS-2", a neutral **Peer** or **Reply** tag, and its
  delivery in a steer's words ("sent 13:43 · read 13:43:52, after Bash";
  queued, "Lands at ABS-2's next step. Nothing is interrupted.", the rail
  dashed; no Run going, "ABS-3 has no Run going; it will see this when its
  next Run starts."). In the sender's transcript it says "Sent · carrying
  on": sending never blocks. A refused one says "Not sent: …" in danger
  ink where the delivery line would be. A peer message wears the peer
  wash; its rail stays its sender's role colour; never accent or
  attention, never a person's face.
- **A thread between agents is read only for people.** `PeerThread` has no
  composer and no Reply; its head says the pair (`TaskChip showRole`) and
  its state (waiting, "No reply from ABS-1 yet · nobody waits", answered),
  its foot who writes in it. Under 520px the chips drop their role words.
- **Waiting on a peer is not needs-you.** Where the Run stopped,
  `PeerWaitLine` says it and why ("ran `dude wait pm_1`", or "turn ended
  with its question to ABS-2 unanswered") and when it parks; once over, how
  it ended (answered, the peer's task ended without replying). The Run's
  header carries `Badge icon="clock"` "Waiting on ABS-2 · parks in 10 min"
  (`statusNote`), the tree and board say it as activity, the epic card
  carries **`PeerWaitMark`** in `EpicCard waiting`. None takes a tone or
  the peer wash (it is a line about a message, not the message), or
  counts in Waiting on you. `PeerWaitLine.now` accepts a Date, ISO string
  or epoch milliseconds (defaults to `Date.now()` at render); pass a ticking
  value with the header, and a fixed value in specimens/tests.
- **`TaskChip`** names a task inline wherever peers are named: status mark,
  mono key, role face, on the neutral active wash (also inside a peer
  message: it names a task, it is not one); a button that says what it opens ("Open ABS-2, …:
  its implementer"), or an image with that name. A peer by link carries a
  neutral "depends on ABS-2 · project jervasion" chip in `PeerList`.
- **`PeerList`** is what `list_tasks --peers` returns, as rows: `tool` (a
  tool call's result: branch @ head, files by churn, what it last said in
  quotes, "1 message for you"), `page` (the Coordination view's tasks, on
  the raised shade) and `rail` (chip, title, phase). The caller's own row
  says "you" and nothing else. It is named by `label`.
- **Epic notes** are numbered and never edited, at most 10 at a time:
  `Section limit` ("Epic notes 3 of 10", named "3 of at most 10 notes"; the
  standalone `EpicNoteCount` says the same). **`EpicNote`** on the epic's
  Coordination view says who pinned it (a person; an agent on its person's
  face, "Implementer on ABS-2"; or "From a brainstorm session, filed by
  Márcio"), when, "replaces note 3", "read by 3 of 4 agents", and a person's
  two quiet actions, Replace and Retire. Replaced or retired, it is history:
  "Replaced by note 4 at 13:51", "Retired by Márcio at 15:26", no actions,
  folded under the current ones by **`EpicNoteHistory`** (one button line,
  `aria-expanded` / `aria-controls`). **`EpicNoteRow`** is an agent's own
  `note` call in its aside. The `pin` glyph is theirs alone.
- **A note added, replaced or retired is an `EpicNoteNotice`** in every
  transcript on the epic, where the agent was told: one quiet line on the
  text column with the pin, never a face, a rail, a tag or the peer wash,
  so it never reads as someone speaking. A replace shows the words that changed and what they
  were (`noteWordChange` finds them from the two texts); a retire, "no
  longer in force:" in muted ink.
- **A conductor's digest** is **`SinceDigest`**, folded, just before the
  briefing that told it: what the conductor was told of its epic, never a
  wake, never a tone.
- **A brainstorm proposes notes; it never pins them.** `ProposalCard`'s
  epic item takes `notes`, drawn under its description on the hover wash,
  headed by the pin and "2 epic notes, pinned when it is filed"; a `note`
  item ("Note on …", "Replace note 3 on …", its texts as Now and After)
  counts as "note" in `proposalSummary`.

### Brainstorm sessions

- A **brainstorm session** is a conversation with one agent that belongs
  to its members, not a task: it reads the projects linked to it and
  proposes work, and a member files it. It is `sessions` in the code; an
  agent's session inside a Run is an *agent session* (above).
- **The brainstorm's face** is `AgentAvatar role="brainstorm"`: the
  conductor's colour and round shape, its own bulb glyph and the label
  "Brainstorm". Both are conversation agents; the glyph tells them apart,
  and no hue of its own sits near the attention tone, so the face never
  reads as needs-you.
- Its transcript is a task's Chat's (`ChatTranscript`, `ChatMessage
  role="brainstorm"`, `ChatComposer mode="chat"`); what is new is around it.
  A session has no task, so its composer's placeholder is its own,
  "Message the brainstorm…", never the task Chat's "Ask about this task…".
  A question put to you is answered in its turn, the whole ask at once.
- **The same bar as an agent session**, under the header: a `Segmented`
  switch between **Conversation** and **Events** (with its count), from
  the same parts as the Run screen's. There is no Changes: a session
  changes no code. **Events** is the session's ledger — every event of
  every Run it had, and its own `session.*` events — in the `EventStream`
  of `EventRow`s an agent session's Events uses, each opening onto its
  payload. Its members' alone: anyone else is told the session does not
  exist.
- **A session starts untitled and names itself.** Until it is named it
  reads **"Untitled session"** everywhere it is listed or headed (`sessionTitle`
  in `@dude/domain`). Its agent names it once the subject is clear
  (`name_session`, one line of at most 60 characters), and again only if
  the subject clearly changes.
- **dude opens on the welcome** (`Welcome`), and **New session opens it**
  too, the list's and the sidebar's. **A session is made by its first
  message, never empty**: nothing exists until the person sends, and the
  send makes the session with that message and what it reads, in one call,
  and opens it. Opening the page and leaving makes nothing.
- **What a session will read is said in its composer before it starts**:
  `ComposerLinks` in the composer's `leading`, each linked project's face
  and name with a close, then "+ Link"; "Reads memory only" and "+ Link a
  project" when nothing is linked. After the first send, the rail's
  Linked block carries it, and its owner links from there.
- **What a session's agent runs on is chosen in its composer before it
  starts**: `ModelPicker` in the composer's `toAside`, right after "To
  **Brainstorm**", a quiet chip reading the effective pair ("Claude (High)
  · Claude Code"). **A session follows the organisation unless set**: each
  half left alone is the organisation's Brainstorm setting, and while both
  are, the chip says "default" in muted ink. Its menu has two groups,
  **Model tier** and **Harness**, each led by "Organisation default
  (…)", which sets that half back. **Any harness takes any tier**: the
  LLM proxy translates, so no item is refused or annotated for the pair.
  Only a tier that names no model is disabled, its reason in place of its
  model, read with the item ("names no model yet"). After the first
  send the rail's **Model** block carries the
  same picker: its owner changes it, everyone else reads it
  (`readOnly`). **A change applies from the next message**, never
  mid-turn: the app then has a fresh agent take over with the
  conversation so far, and the rail says so under the picker ("Applies
  from your next message: a fresh agent takes over with the conversation
  so far."). The Chat says so too (`ChatNotice`, "Ana set the model to … ; it
  applies from the next message", then "Started a fresh agent on … ; it has
  the conversation so far.").
- **A starter fills the composer; it never sends.** `StarterPills` write
  the start of a sentence ("I want to plan an epic for ") with the caret at
  its end, for the person to finish.
- The welcome's column is 720px, a little above the middle: dude's face,
  the greeting (the time of day on the reader's clock and their first
  name), one line, the composer **raised** (`ChatComposer variant="stage"`:
  `shadow-2`, the one place a composer is) with a taller field, the
  starters, then `RecentSessions` —
  or, with none yet, one muted line saying what a session does
  (`WelcomeNote`). In an organisation with no projects the footer also
  offers New project, to those who may make one; a session needs no
  project, so the composer works as ever. Compact takes its air in: 6vh above where comfortable
  has 12vh, a 48px face (64), a 52px field (72), and six recent sessions
  where comfortable has four (`RECENT_SESSIONS_SHOWN`; the app picks by
  density). Under 640px the greeting takes `3xl`, the stage less padding,
  and the recent rows drop their summary. The face is dude's mark, the
  brand, not an illustration: the welcome is a landing page, and its
  first-time line is the only empty-state text in it.
- **`SessionTitle`** is the name in the session's header: untitled, "New
  session" in muted ink. For a member who can chat it is a button that
  edits the name in place — Enter saves, Escape cancels, and either gives
  the focus back to the name (a blur cancels and leaves it where it went); a reader's is
  plain words. **A person's name wins**: once someone has named it, the
  agent's `name_session` refuses. In the header (`ScreenHeader fillTitle`)
  the name takes the line's free width, shown and edited: it is cut, with
  an ellipsis and the whole name in its tooltip, only when the line is
  genuinely too short for it. Under 560px (a phone) the meta and actions,
  which never shrink, wrap onto a line of their own under the title, so a
  shared session's marker, model and Share cannot squeeze it to nothing.
  The wash behind a nameable title sits past
  its edge by a translate, not a negative margin, which would lay the
  button out narrower than its words and cut even "Untitled session". The Chat says who named it, a
  `ChatNotice kind="renamed"`: signed **Brainstorm**, "Named it “…”", or
  unsigned, "Ana renamed it “…”".
- **`ProposalCard`** is what the agent proposes: epics, tasks, edits and
  comments, each saying where it goes, an edit with its text Now and
  After. Filing acts as whoever presses File (`filingAs`): the footer says
  "Filing as Ana: 2 tasks, 1 edit. Nothing starts." An item only someone
  else may file is dimmed and says whose ("Only Márcio can file this: it's
  his task"); a filed one says who filed it as what ("Ana filed BL-61").
  A reader (`readOnly`) sees the folded line, opens it to read, and files
  nothing. Nothing on the
  card names the session: the work filed is the person's, as if typed.
  With nothing ticked, **File stays, disabled**, and the footer beside it
  says "Tick what to file.": the way to act is never missing, and never
  greyed without saying why (the button reads "File", not "Nothing to
  file", which reads as a fact about the card rather than the ticks). The
  footer is the disabled button's `aria-describedby`, so a screen reader
  hears the reason too.
- **A filed card folds to a line in its place in time.** Once nothing on
  it is left for the person looking to file — each item filed, or someone
  else's to file (`blockedFor: "you"`: its task's owner's, or, for a
  reader, a member's) — `ProposalCard` draws one
  muted line where the card was proposed, in the margin grammar of a
  `ChatEvent` row: the list glyph, "Proposed work · 2 epics, 3 tasks",
  then "all filed: Usage metering, BILL-1, BILL-2, DASH-1, +1 more · by Ana
  and Márcio" (in the card's order: keys, an epic by its title, four names
  then "+N more"), or "3
  filed · 2 for others to file". No frame, no tone. The line is a toggle
  (`aria-expanded`, the chevron at its end) that opens the whole card, read
  only; `defaultOpen` sets where the toggle starts. The fold is derived
  from the items (`filed`, `canFile`, `blockedFor`), so a card
  filed in this tab folds as soon as the app reads it again; only the line
  being open is the component's own state. The Chat's `session.filed`
  notice ("Ana filed BILL-1, …") stays where it happened: the line is the
  proposal, the notice the moment it was filed.
- **An item nobody can file keeps the card open.** One blocked for
  everyone (`blockedFor: "everyone"`, or unset: its task has started, its
  project is no longer linked) is a problem someone may need to act on,
  not something done: the card stays full size with that item's `why`
  showing, and is never counted "for others to file". With nothing on it
  yours to file, its footer reads "Nothing here is yours to file now: each
  item says why." beside the disabled File.
- **`QuestionCard onlyThey`** is the agent's question to one member: "Only
  Ana can answer this one", and no "Take over this task" hint, because
  there is no task to take over.
- **The rail** is `SessionRail` with a `SessionRailBlock` per part:
  `SessionPeople` (owner first, each with their role in words — Owner, Can
  chat, Can read, Invited; a member with `left: <org>`, the owner of record
  of a session its admins own, reads "Left <org> · No access"; gallery `bs-access`), `LinkedProjects` (each project and its
  repositories, read only; "No project linked yet" when none), **Model**
  (`ModelPicker`; read only but for the owner), **Files**
  and `Capabilities` (what it can and cannot do, each line a check or a
  cross as well as words).
- **A session's files are what its agent published** — a design note, a
  diagram, a table, a CSV — with `dude publish` as any agent
  does, and listed for its members alone. The rail's **Files** block has
  their count beside its label and **`PublishedFiles`**: each file's kind
  glyph and its own name (the folder in the tooltip), "v3" when published
  again, and under the name, muted, its `description` — the one line the
  agent said it is for — when it gave one; newest first, "N more" past eight; Download all (a zip) once
  there are two. Picking one opens the **same viewer as a task's Files**
  (`FileViewer` over an `ArtifactPreview`), with its versions. Nothing
  published yet says so in the block. The agent says in Chat what it
  published; the Chat shows no file card of its own, as a task's does not.
- **Presence is said in words, not colour alone.** Who has the session
  open now takes the online dot on their face *and* "· here" after their
  role.
- **`SessionRow`** is a session in the list: the bulb face, its title,
  what it filed in a line, the projects it reads as chips, its state and
  age in words. **`SharedMark`** marks one someone else is in too: the
  shared glyph, and the owner's face when it is not yours, so it reads
  without colour. An organisation session shows the organisation mark in
  place of the shared one (`access="organisation"`, `org`); `origin` adds a
  line under its summary saying where it came from — for a chat-born
  session, `OriginFact compact` ("<glyph> From Slack · #support"; the
  gallery's "Chat providers" shows it). Under 600px its chips go under the
  text and the state spans both rows.
- **`SharedMark scope="organisation"`** is the building glyph and "Everyone
  in <org>" in the info ink, a `role=img` named with those words;
  `showLabel` writes them beside the glyph, as the session header does.
  With `owner` it adds the owner's face after the glyph and names it
  ("Everyone in Acme, Gus's"), as the Open to everyone list's rows do
  (`SessionRow access="organisation" shared={{ owner }}`).
  **`AdminOwned`** marks a session whose owner left the organisation: the
  building glyph and "Owner left <org> — owned by <org>'s admins" in the
  attention ink, in the header meta and on top of the rail's People.
- **`SidebarSessions`** sits above Projects in the sidebar: only the
  sessions you are in, the latest three by last activity, each with the
  bulb glyph and the `SharedMark`'s parts (the building glyph for one open
  to the organisation). With `organisation`, a last row "Open to
  everyone" — the building glyph and a muted count of the active
  organisation sessions you have joined — opens their own list; it is a
  place, not a session, and stays when the list is folded. Its heading folds the list
  (the app remembers it), opens the whole list, and has a "+" for a new
  session, as the Projects heading has for a project. The app passes it as `Sidebar`'s `sessions`
  slot, so the sidebar draws it in place and knows nothing of sessions.
- **Archiving a session is yours alone**, from existing parts: a `quiet`
  **Archive** (or **Unarchive**) in the session header's actions, before
  Share; an archived session's header meta carries `Badge size="sm"
  icon="archive"` "Archived". The list's header has a `Segmented size="toolbar"`
  of **Yours | Archived**; the sidebar never lists an archived session.
- **`Sidebar waitingExtra`** adds what waits on you outside the tree —
  session invitations, brainstorm questions put to you, and @-mentions of
  you in a session — to the "Waiting on you" count, so one number answers
  "what needs me". A mention's `WaitingRow` has the mentioner's face, "<First>
  mentioned you in **<session>**: “…”", the access mark and "you're not in it
  yet" when you were never added, and Open.
- **General access is one way.** `GeneralAccessRow` sits above a session's
  people in the Share dialog: who may open it without being added. On a
  private session the owner gets a `Select` whose Everyone option says
  "Can't be undone"; choosing it shows the confirm (`Callout` attention,
  `oneWay(org)`, "Open to everyone in <org>" / "Keep it private"). Once
  `organisation`, the row is read-only for everyone, the owner included,
  with "Every active member of <org> can open it and chat, without being
  added. This can't be undone." and "Can chat".
- **@-mentions.** `MentionMenu` is the listbox under the composer when "@"
  starts a word: face, name, and what mentioning them means ("In this
  session · can chat", "Not in it yet · can open it and chat", or, in a
  private session, a locked "Not in this session" in the attention ink for
  an `outside` person), with the keys under it. The composer keeps focus
  (`aria-activedescendant`); a pointer press picks without taking it.
  `MentionChip` is a mention in a sent message: the face and "@First" on the
  info wash, named "Mention: <name>"; `you` when it is the reader. It sits
  in the sentence, in place of the "@Name" the composer wrote:
  `ChatMessage mentions` (and `Markdown mentions`) take `{ name, chip }` per
  person and draw the chip for each "@<name>" that starts a word in plain
  text (not in code or a link), the longest name first; the name is never
  shown twice.
  `MentionShareConfirm` sits under the composer (its `below` slot) after a
  pick of someone who can't open a private session: the owner gets **Share
  and send**, **Send without sharing** (the name stays as text; nobody is
  notified) and Cancel; anyone else is told who can share it and gets **Send
  as plain text** and Cancel. A `role=group` named for the person, ringed in
  the attention border, `shadow-1` in light.
- **A session's name never leaves it.** The tab title and the presence
  word teammates see beside your face are the fixed "A session", never its
  title — named or "Untitled session" — because a private session's name must
  not reach people outside it. The same words stand for where a question
  waits in the inbox.

### Images in a conversation

- A steer, an answer and a task's prompt take images the same way: the
  `ChatComposer` tray. The app passes `onAttachFiles` and the composer shows
  the paperclip first in the action row, its tooltip (`attachHint`) giving
  the limits so nobody learns them from an error; pasting an image into the
  field attaches it, pasting text does not. `attachDisabledReason` turns the
  paperclip off and says why ("Image storage isn't set up").
- **The design system never reads, scales or uploads a file.** The app does,
  and passes each image as a `ComposerAttachment`: `uploading` with a
  `progress` ring (a sweep when unknown), `ready` with a size badge and the
  uploaded `attachmentId`, or `error` with a short reason on the chip
  ("38 MB · max 10") and a longer one for the warning line.
- Send waits while a chip uploads ("Uploading 1 of 2…" takes the lands
  hint's place) and stays off while a chip cannot be sent; Enter during an
  upload sends once the images are up. A message may be images alone. The
  submission carries `attachmentIds`.
- `AttachDropZone` makes a whole conversation the drop target, so a drop
  is never missed. While files are over it, an overlay — an accent inset
  ring, not a border — says how many and, in the steer's own words, who
  gets them and when (`detail`).
- A sent turn's images are `MessageImages` in `ChatMessage`'s `attachments`
  slot, under its words: one shows large (up to 360px on its long side), several as a row of
  150px tiles, name · size on hover. The turn's receipt covers them; a
  failed one keeps them, with Retry.
- `ImageViewer` opens over everything: ← → between the message's images,
  Esc closes, Download, and "Original W×H" when the agent got a scaled
  copy. Its line says what the agent got — "The agent got 1200×760 PNG ·
  412 KB, scaled from 2400×1520 · 1.9 MB · read at 15:52:40" — leaving out
  "scaled from" when it was not.

### Picking a stopped task back up

- A task that stopped — a person aborted it, its agent died, or delivery
  stopped and its owner said Stop — says so where an escalation would: a
  `Callout` under the header (attention for an abort, danger for a
  failure), in the escalation's grammar. First line: **who stopped what,
  and why**, quoted ("Ana aborted the implementer 1h ago: “…”"), with the
  way to the agent session. Second line, secondary ink: what it left — the commit
  it pushed (mono) and its counts, and whether its workspace and
  conversation are still kept and until when ("kept until Thu 8 Oct"), or
  that they are gone.
- Then the **three ways back**, as buttons, the one that fits first and
  primary: **Resume…** (the same agent, while it is kept), **Try again…**
  (a new agent on the same branch, from what was pushed), **Start over…**
  (a new attempt on a new branch). They sit in **`CalloutActions`**: a list
  named "Ways to pick it back up", each button beside one muted sentence
  saying what it does (its `aria-describedby`, `calloutActionLineId`); the
  pairs stack, button over sentence, on the callout's own width (a
  container query), so a narrow main pane stacks them too. Each opens one `Dialog` whose
  `ChoiceList` holds all three, so the choice is made with what each keeps
  and throws away in view: under it, one line per thing the task has —
  conversation, workspace, branch, agent session (labelled "Session") —
  marked *kept*, *new* or
  *gone*. A way that is not possible stays in the list with its reason
  (`disabledReason`: "No longer kept: lux keeps a stopped session 7
  days", the words shown); it is never simply missing. A note for the agents (optional)
  follows — on Resume it is the agent's next message, otherwise one of the
  task's decisions.
- Only the task's owner picks it back up; anyone else reads, in the
  notice's place for the buttons, whom it waits on and how to make it
  theirs ("Only Márcio, its owner, can pick it back up. Take over the task
  to do it yourself.").
- A stopped agent session's end strip (`RunEnded`) offers the same: **Resume…**
  while it is kept, and **Other ways…**, both opening the dialog.
- **Nothing of an earlier attempt is hidden or lost.** The pipeline shows
  the current attempt ("Pipeline · attempt 2") with one muted line under it
  for each earlier one — how it stopped, and **Show attempt 1**, which
  folds it open: its steps as they ended, who stopped it and why, who set
  it aside and their note, its branch and last commit, and what lux still
  keeps. The Sessions tab's title selector lists the shown attempt's agent
  sessions, plus the task's conductor, grouped as Conductor, Started by …,
  and Delivered automatically. A set-aside agent session reads as it ended,
  its strip saying it was set aside and the way to where the work goes on.
  Activity is one timeline across attempts.
- **Adopted work is picked back up where it is.** A task that took over an
  existing pull request or branch keeps it on every way back, Start over
  included, and the dialog says so: Start over reads "The same pull request
  and branch, reviewed again from the start." (or "The same branch, …"),
  its Branch line is *kept* — "`feature/greet`, and pull request #12 on it,
  which stays open." — and its Workspace is a clean checkout of that branch
  as GitHub has it. Nothing promises a new `dude/…` branch or a checkout of
  the default branch, and for an adopted pull request nothing promises it
  closed. An adopted branch differs on one point: a pull request dude
  opened from it is closed on Start over, and its Branch line says so —
  "`stack/top`, into `stack/base`. A pull request dude opened from it is
  closed." The adopted pull request is every attempt's: an earlier attempt
  shows it too, only to read, and its note stays even if someone closed it
  on GitHub.
- **Adopted work that changed on GitHub** is an ordinary escalation with
  its own words: "**Adopted work changed on GitHub.**" and the
  orchestrator's reason in full (what changed, "Put it back on GitHub and
  Try again, or stop this task and adopt it in a new one."), then **Try
  again** and **Stop**. No banner of its own.

### Nesting

- A subagent's conversation nests inside its parent's (`ChatThread`). The
  2px rail is the child's **role** colour, so depth is a row of differently
  coloured rails, not shades of grey. Indent is 12px then 8px; depth 2 starts
  collapsed; depth 3+ shows only its header with an Open action. A collapsed
  live thread keeps its activity in its header.
- **A tool call's child session** (a task or spawn call's subagent: an
  explore, general or custom agent, not a dude role) is a `ChatThread`
  with `role="subagent"` directly under the call's `ToolCallCard`, one per
  child, side by side when one turn started several. Its title is the
  description the parent gave and the subagent type; inside, its tool calls
  and messages (never its thinking). In dude it is always a folded row:
  the rail's Subagents block opens its transcript in a side panel; its own
  children are folded rows there, navigating that same panel.
  The gallery's child-session examples mount only once they near the screen,
  so offscreen examples add no animation load to unrelated examples.
- **Waiting.** A live child blocked on something nothing in the Run can
  answer (a permission its harness never forwarded) is the thread's one
  attention area: `waiting` turns the rail and wash to the attention mark
  and bg (`--rail`, `--rail-bg`), and the header reads "waiting 1h 57m on
  a permission" with the pause glyph. The stuck call's `ToolCallCard` takes
  a `note` naming the permission, and a `ChatNotice kind="notice"` says
  "Nothing in this Run can answer it." It is a thread state, not an
  activity: the thread is live and keeps its live semantics.
- Only the watched agent session pins its plan (`AgentPlan sticky` under the
  transcript header). A child's plan lives inside its own thread, flat and
  collapsed. Two pinned plans would be two competing answers to "what is it
  doing".

### Triage (the sidebar)

- `src/tokens/triage.ts` maps every domain status to one of six buckets:
  **needs_you**, **active**, **ready**, **failed**, **waiting**, **done**.
  Keyed on the domain unions, so a new status is a compile error until it is
  placed. Only the first four are *counted*; waiting and done are the calm
  majority and are never rolled up.
- A task's bucket is the most urgent of its own status and the agent sessions
  of its current run (`taskTriage`). A `running` task whose reviewer
  is `awaiting_input` needs you, whatever the macro state says.
- **Amber once per region.** In the sidebar the pinned needs-you block (tint
  and bar) is the one amber area; the filter chip, the tree row's pill and
  the roll-up counts are neutral ink, and the
  diamond marks carry the hue. In the transcript it is the header's
  Needs-you badge and the waiting question's highlight.
- The tree shows three levels — Project → Epic → Task. A task is a leaf: its
  runs and agent sessions are on its page, the agent working now rides on
  its owner's face, and they still roll up into its triage. Selecting a
  session (a link, the inbox, a board card) selects its task's row.
- **Only work under way is listed.** Unsearched — and under Mine, which
  is not a search — the tree lists the tasks in the board's middle columns:
  In progress (running or awaiting input), In review, Ready to merge. Backlog
  (received, intake, awaiting confirmation, queued) and Done (done, failed,
  aborted) are the board's. A search query or a triage filter lists every
  status, as before. The selected task is listed whatever its status, so
  opening a done task from the board still highlights it. An epic with
  nothing listed is left out. Unfiltered, a project keeps its header (it is
  where its board opens) with no chevron; under Mine a project with nothing
  of yours listed is left out too. The roll-up counts still count every task.
- Levels differ in row grammar, not just indent (16px): projects are sticky
  small-caps headers, epics carry the layers glyph and a total, tasks
  lead with a mono key and trail with their one state. A tree three deep
  still reads in grayscale.
  The epic's glyph leads its row, after the chevron, as in `Breadcrumb` and
  the board's lane header; a task row never draws it.
- Default open state is derived from triage and never needs two clicks: a
  project opens if anything inside is counted; an epic if anything needs you
  or is active. The user's toggles override these per row and survive refreshes,
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
- **Collapsed** (`Sidebar collapsed`, 1000px and up; narrower, it is the
  drawer either way), the sidebar is a 56px rail on the same chrome shade: its rows
  with their words folded into tooltips to the right, each a
  `row-comfortable` square with the nav row's washes, `aria-current` on the
  one open. **The rail keeps the one loud count** — Waiting on you is the
  `NeedsYouCount` pill itself when something waits, and its name carries
  the count ("Waiting on you: 2"); the inbox glyph when not — **and marks
  each project that waits on you** with the needs-you
  diamond on its face's corner, cut out of the chrome; its tooltip gives
  its counts in words ("1 needs you · 4 running · 1 failed").
  **Nothing else in it takes a tone.** The tree is not in it: a project's
  face opens its board, and expanding gives the tree back. Its search
  expands the sidebar with the field focused, and so does `/` outside a
  field while it is the rail (only ⌘ or Ctrl, not Shift, Option or AltGr,
  make `/` something else).

### Board (the overview)

- `Board` is what the main pane shows when the sidebar selection is a
  project or an epic; a task or agent session opens the transcript. It takes
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
  what it cost. The epic keeps at least 8ch: on a card too narrow for it
  beside the key and a needs-you mark, the mark wraps under them. Running cards show the working roles and the
  deepest live activity. Needs-you cards show the asker and the question in
  attention ink and take the attention wash and bar. Nothing else on the
  surface is coloured.
- Time in lane is `Duration format="age"` — one coarse unit (`45m`, `4h`,
  `3d`), one clock per board ticking once a minute. Seconds on a board are
  noise, and fifty cards must not own fifty timers.
- **Nothing drags.** Every transition between lanes is the workflow's — the
  scheduler starts work, the agent opens the PR, the checks make it ready,
  the merge closes it — and the two a person performs (confirm a plan, abort
  a run) are decisions with context, taken in the transcript. Clicking a
  needs-you card lands on the asking agent session.
- A card is a way in, not a handle: it opens the task, and nothing on it
  moves the task between lanes. **The one exception is a Backlog card's
  start** (`cardStart`): a sibling of the card, never inside it, that
  offers the same ways to start as the task's header, as a menu (never a
  split button: neither way is the default). It is shown only to people
  who may start the task, only in Backlog, and only for a task not
  started. Blocked, its ways are disabled with the reason and its menu
  offers Start when unblocked and Start anyway. Nothing else on a card
  acts: confirming a plan, aborting, merging stay decisions taken on the
  task. The start is tabbable only beside the card holding the board's
  focus (the board owns that rule): Tab from a card reaches its start,
  Shift+Tab returns, ↑↓←→ never land on one.
- **Relations on a card** (`cardMarks`, defaulting to `RelationChip`s and
  `AutoStartMark` from the `NavTask`): a lock and "Blocked by CP-41" under
  the title, the quieter "Blocks 2" on a card that holds others up. Blocked
  waits on work, not a person: neutral ink, never the attention wash.
  Hover or focus on a card lights its relations: its own accent bar, theirs
  on the active wash, the rest at 35% opacity, and a polite live region
  saying it in words. Nothing is added to the board's layout, so the cards
  don't move under the pointer. "Can start" and "Blocked" are a `Segmented`
  filter in the header (counts in the labels), not a triage bucket: blocked
  needs nobody.
- **Cards are memoised.** A card re-renders only when its own props change,
  so a hover redraws the cards whose lit state changes and no others. Each
  card calls `cardMarks` and `cardStart` itself, so pass **stable
  references** (`useCallback`, or a function defined once): a new closure on
  every render of the board's parent redraws every card.
- **Where a card came from** (`cardLead`): on its first line, before its
  key, a small chip with its own neutral fill and edge over the card's chrome
  — a chat-born task's provider glyph
  and name (`ProviderGlyph inline`). The board lays whatever the lead holds
  on the key's line. Nothing for a task typed in dude. A stable reference
  too, as `cardMarks`.
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
- **Archived tasks are not on the board**; the header's quiet "3 archived"
  (`archivedHint`, the app's link) opens the archived list. A closed task
  in Closed says its resolution on its marks line (`ResolutionMark`), and
  its foot says "closed" where a stopped one says "stopped".

### Stopping a task

- **`LiveWork`** says what a stop would stop, at the top of its
  confirmation: one sentence, then a face per live Run — the live ring on
  those at work, none on those queued or a conductor between turns — with
  who it is and what it is doing. A list named "Live now". Wraps; never
  scrolls. `liveWords(items)` gives the sentence's list ("the conductor, 2
  reviewers and a queued fixer"). The Stop dialog and `CloseDialog` show the
  same list.
- **`DeciderField`** is who decides the next attempt, inside a dialog that
  starts one. A labelled `Segmented` of two, "The conductor" and "Deliver",
  each with its driver's glyph
  (`DriverIcons`); the one that decided the current attempt says "as now"
  and is the default. One muted line under it says what the chosen one
  does ("A new conductor, briefed with your note and what attempt 1 did.
  Each finished step comes back to it, in Chat, with you."). The group's
  accessible name is its label; the hint is its
  description. To change who decides the attempt running now, use
  `DriverSwitch`.

### Lifecycle (closed, archived)

- **Closed, archived.** Neutral, muted, never a tone: neither needs anyone.
  A closed task's resolution (`ResolutionMark`, a neutral small badge: Won't
  do, Duplicate of CP-62, Can't reproduce, Nothing to merge) sits beside its
  status, which still says what delivery did. `ArchivedMark` is the
  `archive` glyph and "Archived" in muted ink.
- **The task's `…` menu carries the lifecycle** (`lifecycleMenuItems`):
  Close… (Reopen on a closed task), Archive… (Unarchive on an archived one),
  each shown to everyone and disabled with its reason, checked in order:
  who may not ("Only Ana, the owner, or an admin can do this"), then the
  task's own state ("Already ended"; "Unarchive it first, then reopen it").
  Agents working and a pull request open do not disable them: the dialog
  says what closing stops and asks about the pull request.
- **`CloseDialog`** is the decision: four resolutions as a `ChoiceList`, two
  disabled with why (Nothing to merge is Mark done's; Can't reproduce where
  the caller says so). Duplicate opens a `SearchPicker` that refuses the task
  itself and a duplicate ("pick that one") and says what the handover does.
  Any other resolution with something waiting on the task says it keeps
  waiting, with **Remove the relation**. **`live`**: with anything live the
  dialog turns danger, lists it first (`LiveWork`, "Closing stops the
  conductor and 1 reviewer. Nothing is kept to resume."), and its primary
  says **Stop and close**; with nothing live it is the plain dialog.
  **`pullRequests`**: every open pull request the close would touch, in one
  `Checkbox` row: "Close pull request #412 on GitHub", or "Close pull
  requests #12 and #14 on GitHub" for several. Ticked when dude opened every
  one, unticked when any was the one the task was started from, which is
  named with its own hint ("#9 was opened by someone else; it stays open
  unless you tick this"); unticked, the task closes and the PRs stay open.
  `andArchive` makes it
  Close and archive: nothing chosen, one primary that does both ("Stop,
  close and archive" when live). `refused` disables the primary with whose
  decision it is.
- **`ArchiveDialog`** is the plain confirmation for an ended task; Keep it
  holds the focus. **`ArchiveEpicDialog`** refuses an epic with unended
  tasks, listing them, and offers to archive its ended ones.

### Task relations (blocked by, blocks, related)

- One verb pair everywhere: **Blocked by** / **Blocks**, and **Related**
  for a link that gates nothing. A task is blocked until each blocker is
  done *and* merged; a blocker marked done with nothing merged still blocks,
  and says "done, nothing merged".
- Icons: `lock` (blocked), `blocks` (holds others up), `link` (related).
  They take their words' ink; blocked is never attention-toned.
- **Every start refuses for the same reason.** `StartGate` replaces the
  header's ways while a task is blocked: the reason in words, naming each
  open blocker, then Start when unblocked (owner only) and Start anyway.
  `StartMenu` is the card's form of the same choice.
- **A disabled control the reader can still reach.** A disabled `<button>`
  takes no focus or pointer, so its tooltip would never open. Each refused
  way in `StartGate` (and a refused Start when unblocked…) is wrapped in a
  focusable span that is the control assistive tech meets: `role="button"`,
  `aria-disabled="true"`, `aria-label` the way's label ("Deliver", "Start
  when unblocked…"), and `aria-describedby` naming one description, the
  reason. The inner button carries no description of its own, so the reason
  is read once.
- **Start anyway** (`StartAnywayDialog`) names each blocker and what starting
  now means; Keep waiting holds the focus, the confirm is primary. Only a
  person overrides.
- **Start when unblocked** (`AutoStartDialog`) preselects no way: Set stays
  disabled until one is picked. Once set, `AutoStartMark` says who set it and
  when; the owner can cancel it. Refused, with the reason, when a blocker
  will never merge.
- `RelationList` groups a task's relations as Blocked by, Blocks, Related;
  a settled blocker stays listed, struck through as merged. A row its reader
  may not remove says whose it is (`removeHint`) instead of a disabled ✕.
  A closed duplicate adds **Duplicate of** / **Duplicated by** (`copy`
  glyph): Close writes them and Reopen removes them, so their rows are never
  removable, and a group's `note` says the handover under it ("CP-63's
  blocking came here with it: CP-47 now waits on CP-62.").
  `RelationPicker` refuses a loop and names it ("That would make a loop:
  CP-40 → CP-41 → CP-40"), offering Make it Related instead. Its search
  takes the focus when it opens; the gallery passes `autoFocus={false}`, so its
  static picker does not take focus. This does not suppress the static forced-open
  menu previews' autofocus. The gallery mounts the Task relations section's blocks
  when nearing the viewport (`WhenNear`) or when a deferred valid-hash request
  names one of its 13 `#rel-*` blocks. A requested block scrolls into view after
  mounting; unrelated fragments alone leave the blocks deferred.
  Without IntersectionObserver they render immediately. Drawn from the
  start, its 8,000 elements and 190 running animations held other sections'
  animations back, enough that a tooltip elsewhere took seconds to fade in.
- `DependencyStrip` is an epic's order, in steps, with what can start now;
  a single step draws nothing.
- `ProposalCard` numbers items in dependency order and lists each item's
  relations under its goal; ticking an item ticks what it waits on, saying
  why.

### Agent sessions (live work on a task)

An agent session is one agent's work inside a task's Run (`agent_sessions`
in the code). A brainstorm session (`sessions`) belongs to its members,
not a task; see *Brainstorm sessions*. Components keep their names
(`SessionList`, `SessionHeader`, `SessionRail`, …) but show agent sessions
here.

- The task's **Sessions** tab selects its agent session through the open
  `SessionHeader` title, using `SearchSelector`: a quiet chevron, a Radix
  popover and SearchPicker's combobox/listbox. Search matches real role labels,
  model and status; conductor/delivery grouping and newest-first order come
  from the app. The current option (`aria-current`) sits on the active wash
  whichever row the keyboard is on; the keyboard's row adds the hover wash.
  The selector remains reachable with one session; no sessions is a meaningful
  empty state. There is no separate selection column. A link (`#/session/<id>`) opens it on that tab, not a
  separate page: the task's people, pull requests and findings stay one
  tab away while you watch an agent. Only a session whose task cannot be
  learned stands alone; when it ends, it says the way to its task.
- `SessionHeader` heads the open agent session on every view: whose agent,
  for whom, model, status, cost, tokens, elapsed, Pause / Abort, and the
  terminal where the rail is absent. The numbers never depend on the rail.
  Then **one bar**: a `Segmented` switch at the density's control size
  between **Conversation**, **Changes** and **Events**
  (debugging, last), and on Changes the diff's own controls after it
  (`LiveDiff`'s `leading`). One row, one left edge, whichever view is
  shown: never a row of tabs over a row of tools. Each view fills the same
  place under the bar; none opens over the page.
- **Servers live on the task's tab**, not in an agent session: a Run's
  servers are the task's while that Run serves it, so the bar has no
  Servers toggle and nothing opens beside the conversation for them. A
  branch preview's agent session, which has no agent, says so in place of
  the composer and links to its task's Servers tab (`#/task/<id>/servers`).
- **Changes** in the switch carries its file count and, while the agent is
  changing files, the breathing dot. That dot is the one "live" on the bar:
  the header's status already says Running, so the diff has no Live pill.
- Beside the conversation, a `SessionRail` on the chrome shade: what the
  header does not say (agent, attempt), the tools it used (`ToolUsage`:
  the harness's by its own name, a harness call of one of dude's tools as
  "dude update task (called)" when prefixed or paired with a completed dude
  record (Claude Code and Codex record bare `update_task`), successful
  recorded dude actions as "dude update task (completed)". These are separate,
  overlapping sources, not an additive total. The note is "Harness calls
  include failures and may repeat dude's completed actions." It appears when
  the conversation has any completed dude action, even if that row falls
  outside the six most-used tools shown. The names share
  one column, as wide as the longest up to 60% of the rail, so the bars
  start together; a longer name wraps, never truncated, so "(called)" and
  "(completed)" stay readable), and the files it has
  changed so far (`ChangedFiles`, a breathing dot on the label while live).
  Picking a file there opens Changes on that file alone, as picking it in
  the diff's own list does.
  The rail is 360px; at 920px of session width or less the conversation
  keeps the width and the header keeps the terminal.
- **The terminal is the rail's.** While the Run is alive and running
  (not paused, starting or ended) and lux gave it a terminal, the rail's
  Session block ends with `TerminalLink` ("Open terminal in lux", a new
  tab). Where the rail is not — the agent session 920px wide or less,
  or on Changes or Events — the header keeps the same link as a
  terminal icon (`LinkButton iconOnly`), chosen by the same container
  query on the agent session's width, so the terminal is never unreachable
  and never shown twice. A branch preview has no agent to hold it: its
  terminal stays in the run line of the task's Servers tab.
- With no agent session asked for, the one shown is picked once (running,
  else newest) and kept: a phase ending must not swap it under someone
  reading.
- **Two edges.** Everything in an agent session shares one outer edge and
  one inner edge. Bands — a turn's wash and hover, the pinned plan — run edge
  to edge across the transcript's column. Content sits on the chat inset
  (`--ds-space-chat-pad-x`): the header's face, the bar's switch,
  the plan's icon (`--plan-pad-x`), every turn's face, the composer, the
  Changes and Events views. Nothing in an agent session uses the page's
  24px pad.
- A turn's text spans its column: chat turns have no measure (`Markdown
  unmeasured`: messages, thoughts, questions), so a long line wraps at the
  column's edge, not beside empty space.
- What an agent does between its messages — a run of tool calls,
  thoughts, and the events and progress it records — is one `ChatAside`:
  on the message text column (past the avatar gutter, so its left edge
  lines up with the words above), 4px between its items, and a turn's air
  above and below the run. Never one of them on its own at the
  transcript's edge. Inside it every row starts on the column and insets
  its glyph 8px, so a thought, an event and a tool call line up.
- The task Sessions selector anchors under the header and sizes to the viewport.
- **Changes is `LiveDiff`**: the agent's checkout against the commit the
  agent session started from, uncommitted work included. Its toolbar reads
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
  itself goes behind a `Dialog tone="danger"`. An item's `leading` puts a
  face (project, person) in the glyph's place, for a menu of things — in a
  radio item, after the tick; `trailing` puts a count that belongs to the
  item at its end (the switcher's needs-you pill);
  `container` portals it into a themed pane, gallery previews only, like
  `forceMount`.
- `NavTree` / `Sidebar` take `menuItems={(row) => items | null}` (or a
  `menu` render prop for full control); a row that returns nothing draws no
  trigger. The trigger is out of the tab order and visible on hover, focus,
   or while open, so the tree's ↑↓ → ← Home End `/` are untouched. No row
   reserves space for the menu: it replaces trailing people or an epic's
   count on hover/focus or while open. Each ancestor level has
   a 1px border-colour guide through its chevron's centre, continuous across
   the row gap, driven by `--depth` and `--indent`.
- **Input** renders its own label, hint and error, tied to the field by
  `aria-describedby`. A refusal with a known fix ("BILL is already the key
  of Billing API; pick another") puts the fix beside the error as
  `errorAction={{ label: "Use BLED", onClick }}`: a small quiet button,
  after the error, never submitting. One fix, named by what it puts in
  the field; anything longer belongs in a `Callout`.
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
  of one thing inside a bar with other controls (the agent session's bar, the
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
  `Badge` — a resolved blocking finding reads as both. Its acceptance
  criterion ("Done when", first), description, suggested fix and
  resolution note expand under the row. Settled rows dim;
  nothing is struck through. `FindingGroup` puts open findings first, most
  severe first (`sortFindings`), and counts what is still open; `fixedIn` is
  a slot for the app's link to the fix run.
- **CriterionRow** is the only way the tester's verdict on an acceptance
  criterion is drawn: the verdict as glyph + word in its tone
  (`CRITERION_STATE_SPECS`, keyed on the domain's `VerdictStatus` plus a
  person's `waived` / `accepted`), the criterion's number and text, then
  the evidence (the app's links), what was seen, why dude disagreed, and
  who decided and why. A decision is neutral and names the verdict it
  settled: an accepted failure still reads as one. `VerificationGroup`
  names the commits tested and marks the verdict **Stale** (an attention
  `Badge`) once the branch has moved past them. Never a pass in a colour
  alone, and never a `StatusBadge`: these are not Run or Task states.
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
  is for a value you change. A page whose sub-pages are its children (Chat,
  with one page per provider and Requests) sets `page`: it opens itself, and
  the child pages carry a `Breadcrumb size="sm"` back to it. Without `page`,
  a group opens its first sub-page.
- **A `SettingRow` with long free text** (routing instructions, a prompt)
  sets `fill`: the field and its Save stack at the control column's full
  width. When the settings page's column (`SettingsLayout`'s main, a size
  container) is 640px or narrower — a phone, or a laptop beside the app's
  sidebar and the settings menu — every `SettingRow` stacks: its label and
  help above, the control the column's width, so nothing is squeezed to a
  letter a line. Outside a settings page a row stacks under a 720px window.
- **SearchResultRow** is the one way a search result is drawn (Memory's
  search): ArtifactRow's anatomy and open shade — a 32px row, chevron,
  rank, a `RefLead` for what it is, and the title; at the end, as muted
  facts, which search found it ("words and meaning", "words only",
  "meaning only") and where it lives. Why it ranked where it did is behind
  the click, as a `KeyValueList` in mono. A score is never a chip or a bar,
  and matched words are not highlighted in a hue. `SearchResultList` is the
  ordered list of them. The widths before the lead are the row's own
  custom properties, so the body indents with them.
- **The guide (Help)** is not drawn with `SearchResultRow`: a guide section
  has no rank worth showing, no lead in the sidebar's grammar (`RefLead`
  has no doc type, and a section is not a thing you act on) and its snippet
  is the point, not a detail behind a click; a 248px column cannot hold the
  row's trailing facts. Help uses three pieces instead:
  - **SearchField** (primitive) — the sidebar's "Find work…" box, made one
    component: a wash at rest, a hairline on hover, the accent ring on focus,
    a ✕ once there are words or the `shortcut` before. Named by `label`, never
    its placeholder. The caller owns the keys (`onKeyDown`) and when to
    search. `Sidebar`'s search is one.
  - **DocOutline** — the guide's pages; under the current one, its `##`
    sections (`currentSection` in bold, `aria-current="location"`). Every
    entry is a link (`href`), so a modified click opens a tab; a plain click
    is the caller's (`onPage`, `onSection`), which scrolls and names the
    section in the URL. For a document's own headings, `outline` in
    `util/markdown.ts` builds the list; the guide's sections come from its
    API, already split.
  - **DocSearchResults** — what a guide search found, in the list's place:
    "N sections" (a `status`, so it is read out), then a listbox of
    sections — heading, page title, a three-line snippet — with the query's
    words in `<mark>` on the selection tint (`markWords`: a word matched by
    its stem's first letters, as the search's English stemming finds it). The
    field drives it: ↑↓ move (`aria-activedescendant`), Enter opens, Escape
    clears; a press opens without taking the field's focus. Nothing found
    says so in words.
- **RefLead** is what a thing is, in the sidebar's grammar, wherever tasks,
  epics, projects and memories are named side by side: a task's
  `StatusMark` and its key in mono, the `layers` glyph for an epic, a
  project's face, the `memory` glyph. `named` says the name after it.
  The app passes a spec (`{ type, taskKey, status, name }`), never glyphs.
- **EntityLine** is something named in a row: a face or glyph, its name in
  strong ink (`--ds-color-text-strong`: the one ink stronger than primary,
  for a name) over one muted line of detail, and badges at the end. A
  member in a table, a memory in a list. `wrap` lets name and detail run
  onto more lines instead of ending in an ellipsis, for a sentence (a
  judgement's question) rather than a name. `PersonLine` is it with a face;
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
- **A pull request's base is named in mono** in PullRequestPanel's base
  line ("Conflicts with `main`", "Up to date with `main`"). A conflict has
  three states, told by glyph, tone and words: for its owner, the danger
  line ends in the app's action (Resolve conflicts); while dude resolves
  it (`conflict={{ resolving: true }}`), "Conflicts with `main` · **dude is
  resolving it**" with no action, in the panel's in-progress convention
  (attention with `circle-dotted`, as for running checks); for anyone else
  (`conflict={{ waitingFor: "Ana" }}`), the danger line ends in "Waiting
  for Ana" in muted ink, never a control that is refused when pressed.
- **Checks dude cannot read are said plainly**: "GitHub won't show dude
  this repository's checks", beneath it that a fine-grained token cannot
  read check runs and which token can, and a link to
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
  agents are squares (the conductor and the brainstorm round), show a
  glyph, and take a vivid role colour. Nothing about a person is ever a
  role colour or a tone.
- Identity colour is `identitySlot(person)` — a hash of the id, so the same
  person is the same colour on every screen with no profile record. A
  `photoUrl` replaces the initials and nothing else changes.
- `PersonAvatarStack` overlaps by a fifth and puts the *first* person on
  top: order the list by relevance (the one it waits on, then the
  requester). A covered face's initials clear the face on top and its ring
  (the online ring too) and centre in the part of the face that shows. In
  a stack, initials are set at 0.29×size so two wide capitals ("WW") fit
  behind an online face and its ring; alone, 0.38×size. An agent
  tile on the face on top can still cover the next face's initials. Past
  `max`, a "+N" chip in the same shape stands for the rest
  with the full list in the title.
- **Where a task starts from** is a `Fieldset` "Starts from" in the task
  dialog, a `Segmented` of New work (the default), Existing PR and
  Existing branch. Existing work is picked from the project's repositories
  the task changes, in a `Select` whose repositories dude may not push to
  stay listed, disabled, with GitHub's reason as their `description`. A
  pull request is an `Input` (number or URL); a branch and its base are
  each a `SearchPicker` over the repository's branches, the pick shown in a
  read-only mono `Input` above it, the base's placeholder the default
  branch. When the listing is cut short the hint says "Not every branch is
  listed: type a branch's exact name to find it." What dude found is a
  neutral `Callout`: "**#12** Greet in Portuguese · `feature/greet` →
  `main`"; its refusal is the field's `error`, and Save stays off until
  the lookup succeeds. A repository the task only reads says so in an
  attention `Callout` ("This task only reads web, which it starts from: set
  it to Changes it, or start from New work."). Once delivery has started
  the fieldset is disabled, its hint "Delivery has started: where it
  started from is fixed.", and the saved source shown as found.
  Under what dude found, a `ChoiceList` "What dude does first": **Continue
  the work** ("An implementer builds on it first, then review.") or
  **Review it as is** ("Straight to review: nothing is implemented
  first."), pre-selected from what the source says (a draft pull request or
  a branch continues, a pull request marked ready is reviewed); started, it
  shows the saved choice and does not change.

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
  measure and an optional outline. `variant="prompt"` is an agent's
  instructions in settings (a role's prompt, read or edited in
  `MarkdownDocument`, and an organisation's read-only copy): UI body size,
  a compact rhythm, `{{variables}}` as chips, and no measure
  (`--md-measure: none`) — a working document fills its container, as the
  role's fields above it do. A role's page passes `SettingsLayout wide`,
  which lifts the settings column's 1100px cap so the prompt reaches the
  pane's own gutter; every other settings page keeps the cap.
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
| sidebar and transcript rows (`size-row-item` / `-item-sm`) | 32 / 28 | 26 / 24 | tasks, projects, tool calls / epics, thoughts, peer and start-progress steps |
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
- Every agent role has its own glyph (`ROLE_ICON` in `AgentAvatar`), the
  brainstorm's a bulb (`brainstorm`), also on each brainstorm session in a
  list. `shared` (two people) marks a brainstorm session shared with named
  people, `building` one open to the whole organisation (`SharedMark`,
  `SidebarSessions`, `AdminOwned`). `pin` is an epic note's and
  nothing else's (`EpicNote`, `EpicNoteNotice`, `EpicNoteRow`, a proposal's
  notes).
- `slack` is the Slack provider's monochrome glyph (`ProviderGlyph`, never
  with Slack's colours); a provider without its own glyph uses `integration`.

### Numbers and identifiers

- Anything numeric that appears in a column or ticks live is tabular
  (`ds-tnum` or the component does it).
- Money via `CostDisplay` / `formatUsd`: `$0.0042`, `$0.42`, `$12.34`,
  `$1,284`. Full precision in the `title`. Budgets colour the value at 80%
  and 100%.
- Time via `Duration` / `formatDuration`: two units maximum (`3m 12s`,
  `2h 04m`). Live durations tick once a second, never faster. `format="age"`
  is one coarse unit (`4h`) for how long something has sat in a state.
  A live `Duration` draws the breathing dot; `dot={false}` beside a mark
  that is already live, so a line has one.
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

- A machine an agent raised says so with `MachineChip raised="for this task"`
  (or `"for this session"`): the arrow and words follow the spec and are in
  its accessible name. The tooltip names the requesting Run, role, time and
  reason, the settings' size, and where Reset is available. No approval or price.
- **MachineRaises** is the Overview aside's Machines panel, on the chrome
  shade in ServersSummary's grammar. Controlled `raises` carry role, label,
  size, settings, detail and optional `onReset`/`busy`. A following role
  (`follows="implementer"`) has no independent Reset. Empty draws nothing;
  `loading` and `error` explicitly say what is happening.
- An agent's `request_machine` is a `ToolCallCard` with the chip glyph,
  “Asked for a larger machine: Standard → Large” as its summary and the
  answer as its result; a refusal is a failed call.

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
  chrome shade (a note, or a link to where the options are managed). The
  list is at most 560px wide, and 16px narrower than the room left on a
  phone: a long description wraps instead of running off the screen.
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
- **The machine an agent session runs on is a `MachineChip`** in the
  agent session's header (`SessionHeader`),
  after the model and effort: the `chip` glyph, the size's name strong,
  its spec muted, on the raised shade at the chip height. Its tooltip
  (`MachineTip`) says where the size came from, that it is fixed for the
  agent session, and what the container actually got when lux says. It opens on
  focus and hover and stays open on a press; the chip acts on nothing.
- **The Machines settings page** (an organisation's): its note says only
  admins change sizes and that a change reaches agents that start after
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
- **A repository's setup script is its own document**, opened from
  `Setup script…` in the repository's `RowMenu` or from the
  `SetupScriptMark` after its name; the repository's own dialog stays its
  four fields. The script is a `CodeEditor language="shell"` with a
  `placeholder` and `maxLines`, its footer the line count, any warning
  count and a `SizeMeter` against the server's limit; Save is disabled
  over the limit, and a cleared script saves as none. A line that moves
  git's hooks away from dude's (`core.hooksPath`, `--no-verify`,
  `HUSKY=0`) is a warning diagnostic, never a block. A reader (no row
  menu) opens the mark and reads it read-only with Close. Gallery:
  Settings → Repository setup script (`#s-repository-setup-script`).
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
- **"Can run containers" is a property of each image version**, saved
  with its draft like the Containerfile. The image page asks it with a
  `Checkbox` under the `CodeEditor` (a form value saved later, so never a
  `Switch`); a draft that flips it says so in the editor's footer ("Can
  run containers turned on", accent text, `cube` 12px).
- **Badges show the published version's value only**, never a draft's:
  `CanRunContainersBadge` (`cube`, neutral, tinted) beside "Default base"
  in the image's header at `size="md"`, in a list row at `sm`, and in the
  picker's trail `lower` ("can run containers", as the picker writes
  "default"). `ImageChoiceView.canRunContainers` puts it there.
- **A Run that can start containers says so in its header**: the same
  `CanRunContainersBadge` at `sm`, after the image chip (or where it would
  be, for an image typed by hand or dude's fallback), given `tooltip`: then
  it is a focusable trigger, as `MachineChip` is, in `Tooltip side="bottom"
  keepOnPress`, brighter on hover and while its tip is open, with the
  `aria-label` "Can run containers" and the tip "This Run can start
  containers inside it. Set when the session started; resuming keeps it."
  Never a native `title`. It shows what the Run recorded when it was
  submitted; nothing when it cannot, or for a Run from before that was
  recorded — never a guess from today's settings.
- **What the box needs said beside it is a muted line, not a `Callout`**:
  the `warning` glyph at 12px in attention ink, 22px in under the
  description, xs secondary text. Two of them: the hint (box on, nothing
  named installs podman or Docker, the base cannot either) and the off
  warning (box off on a published version that can, where previews run
  it). Each has an id the `Checkbox` names in `aria-describedby`, so the
  box is announced with them; its label alone names it.
- **History says when it flipped**: `ImageHistoryVersion.canRunContainers`
  (absent where an image says nothing of it) adds "Can run containers
  turned on/off" to a version's item when it differs from the next older
  version that was not cancelled, and a strip on the chrome shade above
  the diff: "Can run containers off → on" (before in deletion ink, after
  in addition ink), or the value alone for a whole version. No strip when
  it did not change.
- **The check is one optional `BuildStages` cell**, "Check containers",
  between Built and Published, only for a version marked so: current
  while it checks, failed with what is missing (the stage after it
  "Not published · Not pushed · vN is still live"), done with what it
  found. A check that passed stays done when the push after it fails; the
  failure is the last cell's.
- **A Run lux cannot place yet says why**: an attention `Callout` where
  `PreparingImage` sits (pinned over the transcript; under
  `PreviewStages` on a task's Servers tab), the lead bold, lux's reason
  in dude's words when it is about nested containers, else as lux wrote
  it, one period at its end. The Run's header says where its start is
  with a bare `StartChip` beside its status (`ChatTranscriptSession.statusNote`),
  never among the facts under the title.

### Do / Don't

| Do | Don't |
|---|---|
| `<StatusBadge status="awaiting_input" />` | `<Badge tone="attention">Blocked</Badge>` |
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
| `<ChatComposer waitingFor="Implement" waitingKey={questionId} />` while the agent waits on you | an answer box in the composer, apart from the question it answers |
| `<QuestionCard items={items} onSubmit={…} draft={…} onDraftChange={…} />` until `answeredAt`, then `answers={…} answeredBy="Ana"` | the question only in the composer, an "other" choice the agent wrote, or a second turn quoting the question to say its answer |
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
| a run's servers on its task's Servers tab; `<TerminalLink>` in the agent session's rail | a servers panel or drawer inside an agent session |
| `<Tab count={on} tooltip={<ServersTabTip summary={s} />}>` | a native `title` on a tab, or "N ready" beside it |
| `<NumberInput step={0.5} min={0.5} unit="CPUs" error="Whole or half CPUs: 0.5, 1, 1.5…" />` | `<input type="number" step="0.5">`, rounding what was typed without saying |
| `<Select options={[{ value, label: "Large", meta: "8 CPUs · 16 GiB · 80 GiB" }]} />` | a label string with the spec glued on in the same ink |
| `<ProportionBar segments={[{ kind: "reserved", … }, …]} legend={…} />` | a chart library, or an app-local bar in its own CSS |
| `<ChoiceList options={[{ value, label, description, icon }, { …, disabledReason: "No longer kept" }]} />` for one of a few ways to act | radio buttons hand-rolled in the app, or an option silently left out |
| a stopped task's `Callout` with Resume… / Try again… / Start over…, each opening the one dialog | a "Retry" button that guesses which of the three was meant |
| `<RelationChip kind="blocked-by" tasks={…} />` under a card's title; `<StartGate>` in the task header while blocked | `<Badge tone="attention">Blocked</Badge>`, or a disabled Deliver with no reason |
| `<ImagePicker images={…} value={id} onChange={…} />` wherever an image is asked for | `<Input mono placeholder="ghcr.io/…">` for an image reference |
| `<CodeEditor language="dockerfile" diagnostics={lint(text)} complete={…} />`, imported where it is used | a `Textarea` with a hand-rolled highlighter, or CodeMirror in the main bundle |
| `<SetupScriptMark repository="api" lines={9} action="view" />` after the name, and `<SizeMeter bytes={n} limit={max} />` in the editor's footer | a wide Setup column, a coloured "has script" badge, or a byte count in a tone that never changes |
| `<BuildStages stages={…} />` and figures only when measured | a progress bar that guesses |
| `<ProposalCard items={…} />` and let it fold once nothing is yours to file; merge it into the Chat by instant | a card whose unfiled items are all someone else's kept full size, or placed by the text of its time; a "Nothing to file" button; an item nobody can file folded away as "for others" |
| New session opens `Welcome`; its first send makes the session with the message and its links | a click that makes an empty, untitled session |
| `<StarterPills onPick={(s) => setText(s.prompt)} />`, the caret at the end | a starter that sends its words, or opens a dialog |
| `<Sidebar collapsed onCollapsedChange railMark railSessions railFooter />` | a second, hand-drawn rail beside the sidebar, or a rail on a phone |
| `<ModelPicker tiers organization value onChange />` in the composer's `toAside` and the rail's Model block, a tier with no model disabled with its reason | a `Select` of every tier × harness |
| `<OrgSwitcher you current memberships invitations />` as the band's footer and `variant="rail"` in the rail | a second organisation menu in the header, or a `Select` of organisations |
| `<EmailChipsInput entries views={emailChipViews(entries, classify)} />` | a `Textarea` of comma-separated addresses, or a chip that says "invalid" in red alone |

## Components

`src/primitives/` — Button (`danger` with `solid` or `quiet`), IconButton,
RefusedButton (a control that can't be used now and still says why: the
Button drawn disabled inside a focusable wrapper with the control's focus
ring and radius, its `reason` the tooltip and the accessible description,
its `label` the name; StartGate's gated starts and a task header's refused
Stop), Input (`size="title"` for a
document's heading), NumberInput (`step`, `min`, `max`, `unit`; `aria-label`
when no `label` shows), Textarea,
MarkdownEditor (`fill`), Select (options with `meta` and `description`, a
`footer`, `onOpenChange`), Checkbox (named by its `label`; its
`description` and `aria-describedby`, the ids of hints beside it, describe
it), ChoiceList (a radio group of a few ways to act, each
with a sentence; `disabledReason` says why one cannot be chosen),
Badge, Card, Table (THead/TBody/Tr/Th/Td/TableEmpty), Tabs (underline for a
page, segmented in a toolbar; a tab can carry a count, a trailing mark and
a `tooltip`), Dialog (`size=
"document"` with `aside`, `context`, `headerActions` and `reading`), DiscardConfirm, Toast, Tooltip,
RowMenu (+ `rowMenuOpeners`; a `kind: "radio"` item is one choice of
several under a small caps heading, `keepOpen` to pick in several groups at
once; an item's `descriptionMono` and `descriptionWraps` set its second
line in mono or let a sentence wrap; a `kind: "note"` is a muted line that
is not an item, for what the menu cannot list, and the menu's description
for a screen reader, whose menu mode reads items alone), Skeleton/SkeletonLines/Spinner, EmptyState,
ScrollArea, Section (`count`, and `limit` for "3 of 10"; `note`, muted words beside the label and out of its capitals, gallery `pe-note`), FormStack (`fill`), Kbd/KeyHint (+ `modKey`)/HelpList/
MarkdownCheatsheet.

`src/components/` — the factory vocabulary:

- **StatusBadge** — every domain status; badge, small, icon-only and dot
  variants.
- **AgentAvatar** — conductor, investigator, implementer, reviewer,
  simplifier, qa_browser, brainstorm (the conductor's colour and round
  shape, the bulb glyph), subagent (a child session: the investigator's
  colour, square, the fork glyph; a visual role only, not a dude role),
  plus human / system / integration actors.
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
  role-coloured rail, collapsible, depth-aware. A tool call's child session
  is `role="subagent"` under the call's `ToolCallCard`. `waiting={{on,
  since, detail?}}` is a live thread blocked on what nothing in it can
  answer: attention rail and wash, "waiting <dur> on <on>" with the pause
  glyph in its header, `data-waiting="true"`; not an activity kind.
  `openOnly collapsed onOpen={…}` renders a folded row at every depth:
  click, Enter and Space open the separate transcript, never disclose a body.
  The whole row, summary included, is `data-open-only="true"` with the
  pointer cursor and hover wash. There is no disclosure chevron or nested Open button; waiting detail belongs
  in the separate transcript. Other callers retain the depth-aware disclosure.
- **QuestionCard** — an agent's question (or up to four, `items`) to a
  person as a turn. Waiting it is the loudest turn in the transcript
  (attention wash and bar, live avatar, a muted ticking wait clock; no badge
  of its own), and with `onSubmit` it is the answer form (see Human
  intervention): one question's choice answers in a click, "Something
  else…" is always offered, several are tabs with a review and Send.
  Answered it becomes a plain row, "Answered · by Ana, after 4m 12s", and
  the record of each question and its answer (`answers`); dismissed, "No
  longer needed". `kind="request"` keeps its one-click choices (`onChoose`).
  Waiting on someone else (`waitingOn`), the note says so and how to make
  it yours, in words everyone sees: "Waiting for Ana to answer · Take over
  this task to answer" (`kind="request"`: "…to decide"). The
  choices are shown muted and do nothing; with a mouse, hovering them says
  it again (`Tooltip keepOnPress`: a press leaves it up). The note is what
  reaches keyboard, touch and screen readers. No tab stop that does nothing.
- **ChatComposer** — steer (lands at the agent's next step; `landsHint`
  says where; interrupt now is a tick) vs prompt vs chat (a task's
  conductor; `to`, and `toAside` right after it for a control about who it
  goes to, shown only with `to`), visibly different; while the agent waits on the reader
  (`waitingFor`), one line pointing to the question above, with "Write to
  the agent instead". The words leave the field only once `onSubmit`
  confirms them: resolving `false`, or rejecting, keeps them to send again
  (the caller shows why). A person's draft is never lost to a failed send.
  `variant="stage"` is for the page's one thing (the welcome): raised
  (`shadow-2`), a 72px field (52 compact) in `text-prose`, and no inset of
  its own, since its host is the frame. A host never restyles a composer
  from outside. With `mentionables`, "@" at the start of a word opens
  `MentionMenu` (the field becomes a `combobox`); ↑/↓ move, Enter or Tab
  pick, Escape closes; a pick writes "@Name " and the submission's
  `mentions` lists the people still named in it, for the server to notify.
  Enter picks while the menu is open, and sends otherwise. An `outside`
  pick is written but not mentioned; `onMention` tells the caller, which
  shows its confirm in `below`, and `submitRef` sends with `shareWith`.
- **TaskHistory** — a task's history in one line: how it went, what ran,
  what it came to. Heads a task's Chat.
- **ChatRunLine** — a Run the conductor started, one line in its Chat on
  its role's rail, opening its agent session.
- **DeciderLine** — who takes a task's decisions, and the way to hand
  them back.
- **StartChoice** — the ways to start a task, equal, side by side.

`src/components/Peer.tsx` — agents on an epic (the rules are under *Peers*):

- **TaskChip** — a task named inline: status mark, mono key, role face;
  a button (`onOpen`) or a named image. `showRole` adds the role's word.
- **PeerMessage** — a message between two tasks' agents as a turn
  (`msg`, `viewer`, `compact`, `replyToText`, `waiting`, `carryingOn`,
  `refused`); `delivery` queued / read / inbox.
- **PeerThread** — two tasks' thread, read only: subject, pair, state
  (`waiting` / `open` / `answered`), messages, foot.
- **PeerWaitLine** — where a Run stopped to wait on a peer (`how` wait /
  turn-ended, `parksAt`), and how the wait `ended`.
- **EpicNote / EpicNoteCount / EpicNoteHistory / EpicNoteRow** — a pinned
  note with Replace and Retire, the count against its bound, the folded
  history, and an agent's own `note` call.
- **EpicNoteNotice** — a note added, replaced or retired, where an agent
  was told (`noteWordChange`, `noteLine`, `noteTitle` are its words).
- **SinceDigest** — a conductor's "since you last looked", folded.
- **PeerWaitMark** — "ABS-4 waits on ABS-2", every pair in its name.
- **PeerList** — `list_tasks --peers` as rows: `tool`, `page`, `rail`.
- **Markdown** — untrusted Markdown to React from a typed AST; streaming-safe;
  `message` and `document` variants; ```` ```diff ```` hands off to `DiffView`.
  `title` renders a plain-text name as the first `h1` (blank: `untitled`,
  muted); `source` may be a list of sections, each parsed on its own.
  `parseMarkdown` / `safeUrl` are exported for consumers that need the AST.

`src/components/` — navigation (the other half of the screen):

- **Sidebar** — header (the brand, who else is online, collapse), search
  (`/`), Waiting on you, the sessions slot, the tree, a footer.
  `online` is drawn in the brand row: the others only (never `you`, who is
  in the profile band), 20px faces, at most three then "+N", named in full
  by the group's `aria-label` and its tooltip, absent when nobody else is
  online; the brand's words truncate first. There is no "Waiting on
  others" row: the inbox's Others tab is where that list lives.
  Loading (skeleton rows), empty, and no-match states. Search and filter are
  controlled or uncontrolled. `collapsed` / `onCollapsedChange` fold it to
  the rail (a chevron in the header collapses it, `[` in the app); the rail
  takes `railMark` (home, `onHome`, `homeSelected`), `railSessions` (New
  session and Sessions, so the sidebar still knows nothing of sessions;
  `organisationCount` adds "2 open to everyone you joined" to the Sessions
  tooltip) and
  `railFooter` (the band's `SidebarRailItem`s). `projectCountWords(counts)`
  is a project's `projectCounts` in words.
- **SidebarRailItem** — one square of the rail, named, its words in a
  tooltip to the right.
- **SidebarProfile** — you, at the foot of the sidebar: face, name, a gear,
  the whole row opening your settings, said in a `Tooltip` on hover and
  focus; `actions` after it (an icon-only sign-out, given its own `Tooltip`
  by the app, as every icon-only action in a band is).
- **NavTree** — Project → Epic → Task, flat with `aria-level`,
  full keyboard navigation, per-row open/closed overrides (controlled via
  `expanded` / `onExpandedChange` so the app can persist them), triage-derived
  defaults, and a search that forces ancestors open ("Mine" only opens
  them by default; its chevrons still fold). Unsearched it lists only
  running, in-review and ready-to-merge tasks plus the `selected` one; a
  query or triage filter lists Backlog and Done too. A task row is a leaf
  (→ does nothing on it); a selected session highlights its task.
- The view model is pure and exported from `src/util/navModel.ts`:
  `flattenNav` (its filter takes `selected`, which keeps that task listed),
  `attentionItems`, `globalCounts`, `projectCounts`,
  `taskTriage`, `workingRoles`, `ancestorKeys`. The app maps domain
  records to `NavProject[]` (joining people, activity and titles) and hands
  it over; nothing here fetches.

`src/components/` — the overview:

- **Board** — the project or epic board: header with scope, counts and
  spend; five lifecycle lanes; three-line cards; needs-you first; keyboard
  grid; "N more" past the cap; quiet, empty and loading states. No drag.
- Its view model is `src/util/boardModel.ts`: `boardColumns`, `boardCards`,
  `boardSwimlanes`, `boardScope`, `liveActivity`, `BOARD_COLUMN_FOR_STATUS`,
  and for relations `boardFilter`, `boardFilterCounts`, `isBlocked`,
  `isStartable`, `neighbourKeys`, `dependencySteps`.
- **RelationChip / StartMenu / StartGate / AutoStartMark / RelationList /
  RelationRow / DependencyStrip** (`Relations.tsx`) and **StartAnywayDialog /
  AutoStartDialog / RelationPicker** (`RelationDialogs.tsx`) — task
  relations; see *Task relations*.

`src/components/` — management:

- **Breadcrumb** — Project › Epic › KEY; links or buttons, middle-elided.
- **Segmented** (with `ScreenHeader`) — two or three views of one thing;
  `tabs="<id>"` makes it a tablist; `size="toolbar"` sits level with `sm`
  buttons.
- **FindingRow / FindingGroup** — a review finding, and the open-first list
  of them; `FINDING_SEVERITY_SPECS` / `FINDING_STATUS_SPECS` are the
  vocabulary.
- **ReviewQuestionList** — questions reviewers asked a person instead of
  guessing: the question, who can answer it, why it matters, what was
  checked, and the finding it is about. Given `onAnswer` and `onWithdraw`,
  each open one has an answer box with Answer and a Withdraw that asks why
  first; `notAllowed` shows the form disabled with that sentence (the
  reader is not the owner). An answered one folds to its row and its
  answer, by a person (named) or the conductor; withdrawn ones are left
  out; nothing when none is left to show.
- **CriterionRow / VerificationGroup** — the tester's verdict per
  acceptance criterion, under the commits tested; `CRITERION_STATE_SPECS`
  is the vocabulary.
- **ArtifactRow / ArtifactGroup / ArtifactPreview** — files an agent
  published, expandable to their content; `artifactKind` and
  `ARTIFACT_KIND_SPECS` are the vocabulary.
- **SearchResultRow / SearchResultList** — a ranked search result, in
  ArtifactRow's anatomy, expandable to why it ranked where it did.
- **RefLead** — a task, epic, project or memory, as the sidebar draws it.
- **EpicCard / EpicRow** — an epic in progress as a card (progress by lane,
  pull requests, people, cost; `needsYou`, and `waiting` after the title for
  a `PeerWaitMark`; `actions` in the corner), planned and done ones as rows.
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
  out of an `href`. `ServerRow.onOpen(url)` optionally opens the URL the
  application's way: a plain left click on the URL, its open icon or
  Preview calls it, and true handles the click; false, a modified click or
  middle-click leaves it to the real `<a href>`. `openLabel` is the open
  icon's accessible name and title, and Preview's title, while `onOpen`
  handles them; without it they say "in a new tab".
- **ServersRunLine / ServersPanel** — the run the servers live on as a
  line, and the panel: the task's Servers tab (a container: under 560px,
  a phone, its rows stack). An agent session has no servers panel; its servers
  are its task's. A live run's line offers Start all, Stop all and Add
  server; a branch preview's adds Stop preview, which ends the run, where
  Stop all leaves it live.
- **PreviewStages / ServersMoved** — a branch preview's stages, and the
  notice with Start all after a run moved host. Infrastructure uses lux's
  stage in the agents' words (`START_STAGE_WORDS`): Getting the image
  ready, Restoring its workspace, Cloning, Starting the container;
  Stopping is a single step, not a completed startup. The active step has
  `aria-current="step"`. Elapsed is optional: only supply it with an
  authoritative stage timestamp, never the Run's original start.
- **ServersSummary** — the overview aside's block, in PullRequestPanel's
  grammar. `ServersSummaryRow.onOpen(url)` is `ServerRow.onOpen` for its
  URL and eye Preview; `openLabel` is the eye's accessible name while
  `onOpen` handles it.
- **ServerRecipeTable / ServerRecipeDialog / EnvVarRows / HostChips** —
  project settings: the definitions, the editor with validation in words,
  and a preview's egress allowlist as chips. `HostChips readOnly` lists
  hosts changed elsewhere (an organisation's, on a project's page) with no
  remove and no field; `muted` for what is always reachable and never
  listed.
- **HostPresets / RefusedHosts / NetworkRefusedNote** — the agent network:
  a toolchain's hosts added by name, ticked when the list already reaches
  them all; the hosts agents were refused, in how many Runs, with Allow on
  each and Allow all; and, in a Run, the note a tool call whose output names a refused
  host carries as `ToolCallCard`'s `note` — under the card, folded or
  open, with Allow for the project and Settings. A switch that lifts the
  list for one kind of agent (an organisation's **Brainstorm sessions**:
  any host, to read the web) is a `Switch` in a `SettingRow`, its label
  saying both states in words ("On — …" / "Off — …"), and a `SettingsMeta`
  line under it saying who a change reaches (agents started after it; one
  already started keeps what it had); while on, a
  `Callout tone="danger"` under it says in plain words what it risks (what
  a page can steer the agent into, and what the container holds that could
  be sent out). Never a badge or an icon alone.
- **LinkButton** (a primitive) — a real link drawn as a button, for a way
  out among actions. **TerminalLink** is it for a run's lux terminal
  ("Open terminal in lux ↗"): in an agent session's rail, and in a branch
  preview's run line; the overview's `ServersSummary` carries it short,
  as "Terminal".

`src/components/` — machines (what an agent runs on):

- **FitBar** — how much of one host a size takes, or "Unknown".
- **ProportionBar / ReservedSwatch** — a whole split into shares, the part
  nobody gets hatched; the memory of one host between Linux and its runs.
- **MachineChip / MachineTip** — the machine in an agent session's header, and
  its tooltip. The rules are under *Machines*.
- **TierLine / TierMark** — a model tier in a table or a picker: its mark
  (a glyph on its tone's tint), name, and what it is for under it.
- **FlowSteps** — how something works, in steps side by side on the chrome
  shade, small-caps titles; they stack when narrow. Once per page.
- **NameChips** — suggestions under a field that takes any name, mono, the
  chosen one on the info tint; say in words that they are suggestions.
- **TierChip / TierTip** — the model in an agent session's header: the tier, then
  the model it requested in mono, then the effort it asked for (secondary
  ink, only when set). Its tooltip says that is what dude asked
  for when the agent session started; never what the proxy served.
- **UsedBy** — who uses something: small faces (agents' tiles, projects'
  squares), then the words.
- **SettingsExplainer** (with the Settings pieces) — how something a
  settings page depends on works, explained once: a titled box on the
  chrome shade at the prose measure, a figure after the words.

`src/components/` — images (what an agent runs in):

- **CodeEditor** — CodeMirror 6, lazy: `language`, `diagnostics`,
  `complete`, `header`, `after` (read-only, under the text) and `footer`.
  `CodeEditorCore` is the chunk it loads; nothing imports it directly.
  `language` is `dockerfile`, `shell` or `plain`; `shell` uses the legacy
  shell stream parser and is for short bash scripts. `placeholder` is muted
  guidance, never a submitted value. `maxLines` caps the text viewport so a
  long document keeps its header and footer visible; content scrolls inside
  the frame after the cap.
- **SetupScriptMark** — a repository that has a setup script: a terminal
  glyph and `setup` on the raised shade after its name (shadow in light).
  It is a button because it opens the script for both editors and readers;
  its accessible label says the repository, the action (`edit` | `view`)
  and the line count. It is not a status colour, and absent when there is
  no script. `textLines` counts the lines it names.
- **SizeMeter** — a code document footer's tabular byte count against a
  caller-supplied `limit` ("284 B of 16 KB", `formatBytes`). It changes to
  attention past 90% of the limit and danger over it (`sizeLevel`); the
  caller owns validation and the save action.
- **ImagePicker / ImageMark / ImageStatusBadge / CanRunContainersBadge** —
  the image combobox, an image's cube mark, a newer version's state as a
  badge, and "Can run containers" (`size` `sm` | `md`, `lower` for the
  picker's lowercase) for an image whose published version can, and
  `tooltip` (a focusable trigger with its tip) for a Run's header, where it
  is the Run's.
- **BuildQueueStrip / BuildStages / ImageState** — the builder's queue in a
  line, a build's stages, an image's state in a row.
- **ImageHistory** — versions and their Containerfile diffs, with Publish
  again, and "Can run containers" where a version flipped it
  (`canRunContainers`). The rules are under *Images*.

`src/components/` — live work:

- **LiveDiff** — a working agent's checkout against where it started, as
  it changes (the rules are under *Agent sessions*): files with status and
  counts, sticky file headers, Unified / Split (`splitRows` pairs each
  removed run with the added run after it), fresh lines flashing, Follow
  the agent, `leading` for the page's controls first in its toolbar, `toolbarIn` to draw its controls into a bar the page keeps mounted,
  `lastChange` as `{ face, tool, path, when }` (the face stays whole; the
  tool gives way before the file name), `onOpenFile` for the viewer,
  `selected` / `onSelectedChange`
  to pick the file shown alone from outside, `fileList={false}` for one
  file on its own, and `commenting` for people's feedback on its lines (see
  *People's feedback*).
- **DiffStat** — "+12 −3" in the diff's colours; every count of lines
  added and removed.
- **SessionHeader** — the transcript's header on its own, for an agent
  session whose views sit under it. `titleSelector` optionally wraps only the
  title, leaving owner/model/machine tips independent; standalone and
  brainstorm headers keep plain titles by default. `statusNote` sits beside the status;
  the title row and the facts under it wrap rather than clip each other.
- **While its agent starts, the header says where**: `StartChip` in a
  session's meta ("◐ Resuming · Checking out repositories · 00:12") and
  as a Run's `statusNote`, `bare` beside its Starting status. It replaces
  the "Waiting for a host" badge; lux's capacity reason is the
  transcript's slow note. Failed, it reads "✕ Did not start · Failed
  while …". It takes the same `verb`/`kind` and step as `StartProgress`.
- **StartProgress / StartChip / startSteps** — an agent starting, resuming
  or moving (*Activity*): the row where its first turn will be, and the
  header's line.
- **`.ds-live-dot`** (base.css) — the one breathing dot, on
  `--ds-color-live`: beside what is changing now (a view's name, a label).
- **SessionRail / SessionRailBlock / SessionFacts / ToolUsage /
  ChangedFiles** — the column beside an agent session's conversation; its
  facts are a `KeyValueList`, values to the right. A brainstorm session's
  rail is the same `SessionRail` and `SessionRailBlock`.
- **SessionList / SessionItem / SessionItemContent** — a task's agent
  sessions, the open one `current`; content is reusable inside a listbox.
- **SearchSelector** — a title-button selector over `SearchPicker`. Inside a
  `SessionHeader` its popup anchors (`SearchSelectorAnchor`) to the bottom-left
  of the whole header, so the owner/model/machine line stays visible; elsewhere
  it hangs below its title. Gallery `#l-selector` shows populated, empty, current, search and focus states.
  Uncontrolled by default; pass `open` with `onOpenChange` to own the open state
  when the selector can remount while open (a title rendered in a different
  place once its content loads). `onOpenChange` reports the title press, Escape,
  an outside press and a pick; the popup follows `open` alone.
- **FileGallery / FileViewer** — a task's files and their versions. A
  `FileVersion`'s optional `description` (what its agent said the file is
  for) is a secondary line under the name, in a card and a row alike.
- **Cost** — a total, with the tokens / machine split as a hairline, and
  optionally where each part came from and whether lux has settled it.

`src/components/` — brainstorm sessions (the rules are under *Brainstorm
sessions*):

- **ProposalCard** (+ `proposalSummary`) — what the agent proposes, filed
  as whoever presses File; with nothing left for the person looking to
  file, one line in the Chat (what became of it, who filed it) that opens
  the card read only (`defaultOpen` starts it open). An item nobody can
  file keeps the card open. An epic item's `notes` are drawn under it,
  pinned when it is filed; a `note` item adds or replaces one.
- **SessionTitle** — the session's name in its header: "Untitled session"
  muted until named, renamed in place by a member who can chat.
- **SessionPeople / LinkedProjects / Capabilities** — the rail's parts:
  who is in it, what it reads, what it can do.
- **PublishedFiles** — the rail's Files: what the session's agent
  published, each with its `description` under the name when it has one,
  each opening in `FileViewer`.
- **SessionRow / SharedMark** — a brainstorm session in the list, its
  origin, and the marker for one shared with people or open to the
  organisation. **AdminOwned** — the owner-left mark.
- **GeneralAccessRow** — the Share dialog's one-way General access row.
- **MentionMenu / MentionChip / MentionShareConfirm** — @-mentions in a
  session: the composer's picker, the sent chip, and the private session's
  share confirm.
- **SidebarSessions** — your latest brainstorm sessions above the
  projects, in `Sidebar`'s `sessions` slot: `limit` (3) by `lastActivityAt`,
  newest first; the heading is a fold chevron (`folded` / `onFoldedChange`,
  for the app to keep), Sessions (`onOpenList`) and a "+" `IconButton`
  "New session" (`onNew`); `waitingExtra` counts their invitations and
  questions in Waiting on you. `organisation` adds the Open to everyone row.
- **Welcome** — the first screen's stage: `mark` (the app's face; the
  design system has none), `greeting`, `line`, `composer` (a `ChatComposer
  variant="stage"`), `starters`, `footer`. **WelcomeNote** is the first-time
  line. `WELCOME_STARTERS` and `WELCOME_FIRST_TIME` are the words, one copy
  for the app and the gallery.
- **StarterPills** — ways to start, `{ id, icon, title, detail, prompt }`,
  `onPick(starter)`; `detail` is the tooltip. The glyph takes the label's
  ink (secondary, brightening with it on hover): these are actions, not
  the brainstorm's role glyph.
- **ComposerLinks** — what a new session will read, for `ChatComposer`'s
  `leading`: chips with a close, and "+ Link" opening `RowMenu` (whose
  items take a `leading` face); read only without `onLink` / `onUnlink`.
- **ModelPicker** (+ `effectiveModel`, `modelWords`) — the model tier and
  harness a session's agent runs on, for `ChatComposer`'s `toAside` and
  the rail's Model block: a chip with the effective pair (named "Model:
  Claude (High) on Claude Code"), "default" while it follows the
  organisation, and a `RowMenu` of two `kind: "radio"` groups that stay
  open as both halves are picked; `onChange({ tier, harness })`, null for
  "Organisation default". A tier naming no model is disabled ("names no
  model yet"); every harness is offered with every tier.
  `menuNote`: a muted line atop the menu ("Could not load the tiers");
  `onOpenChange` lets the app read the tiers on the first opening.
  `readOnly`: the chip alone.
- **RecentSessions** — the welcome's short list: one line each at
  `row-default` height, the bulb (the role glyph, in the brainstorm's role
  colour), title, `SharedMark`, summary, age; "All sessions". Its own compact row,
  not `SessionRow`'s, whose two lines, chips and state column are the
  list page's. `RECENT_SESSIONS_SHOWN` is how many per density.

`src/components/` — accounts (being in several organisations; the flows
are in `docs/design/accounts.md`):

- **OrgFace** — an organisation's face. Organisations have no image or
  colour slot, so it is `ProjectAvatar`'s rounded square with the
  organisation's initials on the identity colour its id hashes to: the
  same organisation is the same colour everywhere. Named by the
  organisation; `aria-hidden` where its name is beside it.
- **OrgSwitcher** — you at the foot of the sidebar, and the one menu for
  you. `variant="band"` (the expanded sidebar's footer) is
  `SidebarProfile`'s anatomy: your face and name, the organisation's face,
  name and your role under it, an info dot while an invitation waits, a
  sort chevron. `variant="rail"` (the collapsed rail's `railFooter`) is
  your face with the organisation's in its corner, the dot on its top
  corner, named in a tooltip. The menu is `RowMenu`: your organisations as
  `kind: "radio"` items, each with its face (`leading`), your role and
  what waits on you there as the description and the needs-you pill at
  the end (`trailing`), the current one ticked; **Join <org>…** per
  invitation (`onJoin`); **Your settings** (`settingsLabel`); **All
  organisations** with `superAdmin` (`adminLabel`, and `adminNote` under
  it); **Sign out** with `signOutNote`. With `onOrgSettings` and `onHelp`,
  **Organisation settings** (building) and **Help** (book-open) follow Your
  settings: the profile menu is where they live, not rows in the band. The
  trigger's name says
  who, where, your role and what it opens. Enter or Space opens, arrows
  move, Enter picks, Escape closes and returns focus. Picking the current
  organisation does nothing; switching is the app's navigation
  (`onSwitch`). `switcherItems` is the menu's items, for a host that draws
  its own trigger; `profileItems` is its places and Sign out alone.
- **ProfileMenu** — the same band and the same places (Your settings ·
  Organisation settings · Help · Sign out) where there are no
  organisations to switch between (API-key sign-in), so every sign-in mode
  reaches them through the one menu.
- **EmailChipsInput** — email addresses as chips, for an invite: `Input`'s
  field box holding chips and a bare input. Enter, a comma, a semicolon, a
  space or leaving the field make chips of what is typed; a paste is split
  by line, comma and semicolon (`parseEmailEntries`): `Name <email>` keeps
  the name, a tab-separated row's cell with an `@` is the address and its
  first other cell the name; addresses are trimmed and lower-cased.
  Backspace in the empty field takes the last chip back; each chip has its
  ✕ (named "Remove <address>"). The caller says what each chip means with
  `views` (`emailChipViews(entries, classify)`, which decides `invalid`
  and `duplicate` itself): `valid` ✓, `renew` (expired, renewed with the
  same link), `invalid` (danger), `member` and `duplicate` (struck
  through), `invited` (with one inline `action`, "Copy their link"). Each
  is told by glyph and words, and a screen reader hears "will be
  invited" or "left out". The line under the field (`emailChipSummary`,
  live, describing the field) counts them: "2 new · 1 renewed · 1 not an
  email … The other 4 are left out."
- **InvitationLinkList** — each invitee with their link, to send
  themselves: name over address, role and "renewed, same link"; the link
  in mono; **Copy** named "Copy <who>'s link". On the sunken shade, rows a
  hairline inset apart; under 640px the link drops under the name.
  `invitationLinesText` is Copy all's text, `Name, email, link` a line.
  Used by an invite's result and by Create organisation.
- **KeyShownOnce** — a secret, shown this once (moved from the web app): the
  key in a read-only mono field that selects itself on focus, Copy
  (Copied) at its end, and a `usage` slot for how to use it (a string is
  set in a square sunken well) under `usageHeading` ("How to use it"),
  with a muted `usageNote`. Done closes it;
  nothing shows it again. `copy` replaces the clipboard write in tests.
  `doneLabel?` (default "Done") names the closing button, e.g. "I've stored it" for an incoming webhook's secret; `label?` (default "Key") names the field.
- **FrontDoor / FrontDoorMessage / FrontDoorLink / HowList** — a page that
  is one centred column: the app's `mark`, a heading (the page's `h1`) and
  its `line`, cards, and fine print (`footer`) with an inline
  `FrontDoorLink` ("Sign out"). `variant="outside"` (no organisation's:
  No organisation, You're invited, an invitation's page) is 640px on the
  canvas with no sidebar; `inside` (an organisation's first page) is 720px
  on the surface beside the sidebar. One column on a phone, the heading a
  step smaller. `FrontDoorMessage` is one message in a card: a face or a
  glyph on a sunken disc, an `h1`, a line and its `facts`; the card's
  footer holds its actions. `HowList` is how something works in a few
  named lines: a glyph, a bold lead-in, a muted sentence.

### Chat providers (`components/Chat.tsx`)

Shared surfaces never name a provider in words of their own: each takes a
`provider: ChatProvider` (`{ id, name, glyph }`) and draws its glyph and
its `name` from props, so a second provider costs an entry, not a
component. Gallery: "Chat providers" (`gallery/sections/ChatProviders.tsx`).

- **ProviderGlyph** — a chat provider's monochrome glyph in the ink of the
  text beside it; `label` names it when it stands alone; `inline` puts the
  glyph and the provider's name in a line of text (a Requests row's where,
  a board card's lead) without breaking the line. `providerGlyph(id,
  ICON_NAMES)` maps a server's id to the set, `integration` when it has none.
- **RouteBadge** — where a mention went: a `Badge` with the route's glyph and
  word (`ROUTE_SPEC`), so it reads without colour. Refusals and failures are
  `subtle`; routes that made or linked something are `tinted`. `fallback`
  adds "was Task" in muted text when dude overrode an answer under the 75%
  bar.
- **OriginFact** — where a session, task or epic came from outside dude:
  "<glyph> From <provider> · #support · thread by Márcia". The whole fact is
  the link to the permalink, with the external glyph; `compact` drops
  "thread by" for list rows.
- **ExternalThread** (+ **ExternalFace**) — a conversation from outside dude,
  quoted as external text: the chrome shade and a strong left rule, as
  `Timeline`'s quote. A linked author shows their dude face and name; anyone
  else shows their name on neutral initials (`ExternalFace`, never an
  identity colour; punctuation is no initial), and "not in dude". Text is
  never rendered as Markdown. Its head reads "Quoted from <provider> ·
  #place · N messages"; "Open in <provider>" links out with the external
  glyph; `clip` shows the last messages with "Show N earlier". A chat-born
  task's overview quotes its thread under the goal, as filed.
- **RoutingLine** — dude's margin line in a chat-born transcript: "Routed as
  a brainstorm · 82% sure", with the `ChatNotice` rule on either side. It is
  a `<details>`; its reasoning and `model` (how the model was asked) sit
  under it. With `shared` (a private channel or group), the line adds
  "Shared with 4 people from #design-private; 2 are not connected", and the
  body groups them in two rows: "Added, can chat" (faces and names) and
  "Skipped, not connected" (`ExternalFace` and display name).
- **FactList** — rules that are not settings, read as cases: a glyph and the
  case on the left, what happens on the right; a `<dl>` named by `label`,
  stacked under 720px. For what an admin should know but cannot change
  ("Who gets the session", on the Chat page).
- **ConnectionCard** — one account dude acts with elsewhere: a title and
  glyph (any node, so `ProviderGlyph`), an intro, facts, the callback's
  `outcome`, Connected or Not connected; Connect is primary until connected
  (or while `reconnect`), Disconnect only when connected. A `Card`'s
  surface and shadow, square as every panel, so it pairs with the GitHub
  `Card` above it. Your settings → Connections (a `SettingsSection` with
  its intro): GitHub, then one per chat provider the organisation
  installed.
- **IdentityPair** — a provider account beside the dude person it is linked
  to, the left label the provider's name, named for assistive tech as one
  sentence. `size="lg"` is the connect page's: two square sunken panels
  (provider handle, display name, workspace ↔ dude face, organisation,
  email) with the link glyph between, stacked under 480px. Its `external`
  is the account being linked: on the connect page, the one the Connect
  prompt was sent to. `null` when no account is known yet: the provider's
  glyph sits in the face's place over "Your <provider> account · Signed in
  next" and the `workspace` — never an invented handle or initials.
- **PromptText** — instructions a model is told, read-only: a sunken mono
  well, named by `label` and reachable by keyboard, wrapped at its column
  with the text's own line breaks kept, scrolling down past 24 lines and
  never sideways. Not `Markdown` and not a code block: the text is shown as
  written, with no copy button or language chrome (the Chat page's built-in
  instructions).
- **InstructionStack** — what a model is told, in order: a numbered `<ol>`,
  one row per layer with whose it is, its length and where it is changed.
  `here` tints the layer edited on this page (info wash, "This page" badge);
  `collapsed` shows only its head (the built-in instructions); an empty
  layer says "Nothing added". Read-only: the layers are edited where they
  live.
- **ProviderList** — the Chat page's providers: a list of rows, each a
  button with the provider's glyph in a tile, its name and detail
  (`EntityLine`), Installed (success) or Available (neutral), and a chevron
  to its child page.

## Page rails

`PageWithRail` is opt-in layout, not the app shell. Pass main content as
`children` and `<PageRail aria-label="…">…</PageRail>` as `rail`. Main content
fills the remaining width without a prose/page measure. At container widths
of 1080px and above, the right column uses `--ds-size-page-rail` (360px,
canonical `size.pageRail` in `src/tokens/scale.ts`, regenerated with `bun run
tokens`). This leaves at least 720px for the main column. Below that threshold
there is no reserved column: the same rail is above the main content, with
facets wrapping into compact controls. Controls are never hidden or remounted,
so actions, selection and keyboard focus remain available. The rail comes
first in DOM and focus order at every width, so its filters and actions are
reached before the main content. The host owns page
scrolling; the layout imposes no fixed height or independent width cap. In a
flex-column host it grows to the remaining height, so the desktop rail's shade
reaches the foot of the page however short its blocks are.

Rail pages pass `RelationList wide` and `Timeline wide` to remove their default
820px measure, and `MetricGroup joined fill` to spread tiles evenly across the
main column; callers without those props keep their existing measure.

`PageRail` uses the chrome shade and 16px padding in both densities.
`PageRailBlock label={…}` names a section through its heading; `live` adds the
shared live dot. Blocks are separated by space, never rules. `SessionRail`
and `SessionRailBlock` are compatible aliases of these generic parts; session
facts, tool usage and files retain their existing APIs.

`FacetList` is a controlled multi-select group: `label`, `options` (`value`,
`label`, optional `count`, `disabled`, `description`), `value: string[]`, and
`onValueChange`. No selected options means what the caller defines (usually
all). Zero counts remain visible and do not disable a choice. Counts are
caller-supplied facts: calculate them from a documented base set, not from the
accidentally filtered result. The DS neither filters records nor infers scope.
`empty` supplies the meaningful empty line. Each choice uses the existing
Checkbox's label, description, disabled and keyboard behavior. Gallery
`#rail-populated`, `#rail-empty`, and `#rail-narrow` show selected, zero-count,
disabled, interactive focus and action states in both themes and densities.

### Task tab data mapping

The app opts Overview, Findings, Files and Activity into `PageWithRail`; Servers
moves its serving-run facts and bulk actions into the same rail. PR panels retain
checks, reviewers and permission-aware actions. Overview facts use the real task
owner and decider and the header's selected attempt; cost stays in Time & cost.
Finding status/severity counts use all findings in the header-selected attempt,
before composing filters. Status vocabulary is Open, Resolved, Superseded and
Accepted (there is no separate dismissed status); Note is a severity. Expanded
finding Markdown is unmeasured; document previews keep their document measure.
Reviewer facts come from review Runs, and fix counts count actual fix Runs.

Files facets count distinct paths by their latest version's kind and publishing
phase, bounded to the header-selected attempt. Versions remain in the viewer.
Download all builds a ZIP of exactly the displayed latest-version IDs via
authenticated content reads, not the whole-task ZIP endpoint. This client-side
ZIP holds the selected bytes in memory; a failed content read fails the download
rather than silently omitting a file. `FileGallery controls={false}` leaves
filtering and downloads to the host rail while preserving row/viewer actions.
Activity facets use event types, structured actors and `eventAttempts`, never
rendered sentences; counts cover the complete displayed timeline before filters,
including every attempt. Empty selections mean all; dimensions compose with AND,
multiple values within one dimension with OR.

A task that took over existing work says so with the components already
here. Overview's "This task" block adds **Starts from**: "#12 Greet in
Portuguese · `feature/greet` → `main`", the pull request a link to GitHub,
or for a branch "`stack/top` → `stack/base`"; new work has no such line.
Its pipeline starts at Review: no Implement step is drawn, not even as
skipped. The pull request step of a pull request it took over reads
"Pull request · #12 already open" (a state, not a step dude ran); an
adopted branch's pull request, once opened, is the ordinary step. That
pull request's `PullRequestPanel` carries, as its `note`, "Existing pull
request: dude pushes to it and opened none", above why merge waits.
Starts from ends with the saved choice: "· Continue the work" or "· Review
it as is". Work to continue keeps its Implement step, as new work does; an
implementer that committed nothing on it reads "Implement · nothing to add",
an ordinary completed step, and review follows on the unchanged head.

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
- The application shell and navigation placement. `PageWithRail` supplies an
  opt-in content/right-rail region; the app still decides where its sidebar,
  headers and page regions sit. Non-rail pages need not adopt it.
- Drag-and-drop on the board. The workflow owns every lane transition; see
  the Board rules.

## Preview overlay

The preview feedback overlay is a non-React consumer of this design system. It runs in a closed shadow root on pages dude does not own and cannot load `tokens.css`, so `apps/web/overlay/build-tokens.ts` generates the small set of `--ds-*` properties it uses from this package's `themes.ts` and `scale.ts`; `apps/web/overlay/build.ts` regenerates that module before every IIFE build. The committed copy has a staleness test. This keeps the overlay self-contained and keeps its surfaces, text ladder, tones, accent, focus ring, type, radii and shadows on the same source of truth.

The overlay maps the picked element to the **accent** role: the README defines accent as interaction, focus and selection, and a picked element is a selection. Its outline and primary action use `color-accent`, its focus uses `color-focus-ring`, and its selection wash uses `color-selection`; status words and pins use the success, attention, danger, info and neutral tone roles. It never uses a role colour or merged violet for interaction. Its panel, toolbar, field and disclosure follow the same overlay/chrome/field primitives as the app, without importing React.

Before dude answers, the overlay follows `prefers-color-scheme` and updates when that preference changes. A `session` message may then carry dude's resolved `theme`; once present, that explicit light/dark value wins over the preview page's OS preference. The field is optional for one-release compatibility with older overlay/dude pairs. The overlay keeps the existing origin, source, size and strict-schema guards and makes no network request.


`src/components/FeedbackCard.tsx`, and `LiveDiff`'s `commenting` prop.
Gallery: *People's feedback* (`#fb-diff`, `#fb-cards`, `#fb-editor`,
`#fb-tray`).

In the web app, commenting lives on the task page: a Run's Changes take
comments when opened there, never on the standalone Run page.

- **Commenting is props on `LiveDiff`, not a second diff.** `commenting`
  takes `canStart`, `maxLines`, the `selection` (the app's to keep) with
  `onSelectionChange(range, done)`, `renderAfterLine(path, side, line)`,
  `renderFileHead(path)` and `fileMark(path)`. With `canStart`, every line
  with a number on its side has a "+" in its gutter: a real `<button>`
  named "Comment on line N" ("(removed)" on the old side), shown on the
  hovered line and on focus. A press on the "+" or a line number starts a
  range; moving over lines of the same side and hunk stretches it, capped
  at `maxLines` from where it started; letting go anywhere ends it (`done`).
  By key, Enter or Space on a "+" picks its line and Shift stretches the
  range to it. The range is on the selection wash with the accent's 2px
  bar; while dragging a line under it says how many and the cap. What
  `renderAfterLine` returns sits on the chrome shade under that line —
  in Split, under that side's column — and a unified context line asks for
  both its sides.
- **`FeedbackCard`** is one item: author, when, where its batch went, its
  status (`feedbackStatusSpec`: tone, glyph and words — never hue alone),
  what it is on (`FeedbackAnchorLine`), its words, its thread
  (`FeedbackThread`, on the thread's 2px bar) and the actions its status
  allows: Reply and Resolve while it needs handling; Reopen once answered
  or resolved; while its commit is unpublished, Reply off with why and
  Confirm resolved. A draft is on the surface with a steer's dashed bar
  and "Only you see this until you submit", Edit and Delete. `inline` drops
  the anchor (its lines are above it); `placement="outdated"` or
  `"unchanged"` shows the original excerpt (`FeedbackExcerpt`) with the
  words that say why it is not on lines. `readOnly` has no actions.
  A refused action keeps what was written and shows the API's words.
  Preview anchors render `FeedbackPreview`: Page, element, component and
  optional `imageUrl`. `FeedbackPreview.onOpen(url)` (passed through as
  `FeedbackCard.onOpenPage`) handles a plain left click on Page when it
  returns true; false leaves it to the real link. Modified clicks remain
  the browser's. The app uses this to open the item's exact page with
  commenting when its origin belongs to a commentable preview server;
  `onJump` remains the separate Show on preview action.
- **`CommentEditor`**: ⌘/Ctrl+Enter saves, Escape cancels, "Private draft
  until you submit"; `full` turns the field off and says why.
- **`DraftsTray`** sits at the foot of a task's pages (`position: sticky`)
  while there are drafts: the count, "only you see them until you
  submit", where a submit goes now, Review (the list: jump, edit, delete)
  and **Submit N…**. At `max` it says so. `submitRefused` turns Submit off
  with the API's reason. **`SubmitFeedbackDialog`** says what goes, where,
  and what it holds until handled; Keep as drafts, Submit N.
- **Holding the merge is not needs-you.** Nothing here is attention-toned:
  an item waits on an agent or on its author, not on the reader.

## Project secrets

`src/components/Secrets.tsx`: `GivenTo`, `GivenToFieldset`, `SecretsChip`,
`OverriddenBy`, `SecretName`, `SecretPicker`, `PresetSecrets`. Gallery:
Settings → *Project secrets* (`#s-secrets`), *OverriddenBy / SecretName*
(`#secrets-overridden`) and *SecretPicker / PresetSecrets*
(`#secrets-picker`), in both themes and densities and at 390.

- **Project secrets have their own SettingsSection.** Its title is
  "Secrets" and its note says who gets values, as environment variables.
  The compact `Table` keeps Name in mono, Given to as neutral `Badge`s
  (`GivenTo`), Value as a muted mono hint, and a `RowMenu`: Change value…,
  Change who gets it…, a separator, then Remove in danger behind a danger
  `Dialog`. Nobody is muted words, never an empty cell. Value is
  `hideWhenNarrow`; a phone keeps the name and recipients. A member who
  cannot edit sees names and recipients, a note saying who can change
  them, and no menu or Add. Branch previews keeps one row: how many reach
  previews, and a Manage link to the section.
- **GivenTo** (`given`, `none?`): a secret's recipients in a table cell:
  neutral badges, or, when none, `none` in muted words ("Nobody" by
  default; an organisation secret's table says "Only automations that
  pick it").
- **Who gets a secret is separate from its value.** Add asks for Name,
  Value, then the "Given to" `GivenToFieldset`: one `Checkbox` per
  recipient (Previews, Tester, Implementer, Fixer, Simplifier, Reviewer)
  with its one-line description; Previews and Tester start checked. Change
  who gets it asks only for the fieldset, seeded with the current
  recipients; Change value asks only for the value. Saving needs at least
  one recipient: with none, the fieldset's hint becomes "Give it to at
  least one, or don't add it yet" and the save is disabled. A
  `SettingsNote` says the conductor and brainstorm never get secrets
  (they read text anyone can write), another recommends pairing a secret
  with the network allowlist; the hint says sessions and previews that
  start after saving get it. A value shorter than dude redacts says so in
  its hint; it is not refused.
- **Organisation secrets are the same section one level up**
  (Organisation settings → Secrets). The table adds **Picked by** (how
  many automations pick it, `hideWhenNarrow`) and, after the name, a muted
  aside naming the projects that have their own. An organisation secret
  may be ticked for nobody; its Given to then reads "Only automations that
  pick it". A project's table lists the organisation's after its own, each
  marked `SettingSource` "From Acme" with no menu (a screen-reader line
  says it is read-only here) and a note linking to where they change.
- **OverriddenBy** (`name`, `by?`): on a secret that a more specific one
  replaces, the layers glyph and "Overridden by this project's `GH_TOKEN`"
  in muted xs text, the name in mono. It takes the place of the row's
  Given to (or a checkbox's description); the row's own name is struck
  through in muted ink. Glyph and words, never colour alone.
- **SecretName** (`name`, `struck?`, `from?`, `aside?`): a secret's name
  in a table cell, mono. `struck` strikes it through in muted ink (the row
  `OverriddenBy` explains); `from` follows it with `SettingSource` "From
  Acme" on an inherited row; `aside` follows it with muted xs words, such
  as "platform has its own". The marker and aside wrap under a long name.
  The app styles no secret name itself.
- **SecretPicker** (`orgName`, `org`, `project?`, `value`, `onChange`,
  `disabled?`): an automation's secrets, as checkbox groups "Acme's" and,
  for a project automation, "platform's", side by side and stacked when
  narrow. Each checkbox is the mono name, with `…last4` as its
  description, and is named "GH_TOKEN (Acme)" so the two GH_TOKENs differ.
  A name the project also holds cannot be ticked under the organisation:
  it is disabled, struck through and described by `OverriddenBy`. An
  organisation pick of such a name shows as the project's row ticked and
  is passed back as the project's (`@dude/domain`'s `secretPicksInForce`): that is the
  value its runs get, and unticking it removes the name. The value records
  the source as well as the name.
- **PresetSecrets** (`role`, `given`, `none?`): one of dude's agents has
  no picker; in its place, read-only, the `lock` glyph and "Tester gets
  `NPM_TOKEN`, `SENTRY_TOKEN` from Acme · `DATABASE_SEED_URL` from
  platform", the owners muted, each name mono. With none, "<role> gets"
  and `none` ("no secrets" by default; the Investigator says why).
- **The secrets an agent session got are a `SecretsChip`** beside
  `MachineChip` in its header: the `lock` glyph and "N secrets" on the
  raised shade, at the chip height. Its tooltip lists the names only and
  says "Given by project and organisation settings; values are never
  shown". It opens on focus and hover and stays open on a press; it acts
  on nothing. Names come from what the session recorded when it started,
  never today's settings. A session given none — a conductor, a
  brainstorm — shows no chip. Its accessible name is "Secrets: N
  secrets, " and the names.
- **Redaction shows as `[REDACTED:NAME]`** in the transcript, tool output
  and diffs, as dude stored it; nothing styles it. The redaction note says
  it is defence in depth, not a guarantee.

## Automations

`src/components/Automations.tsx`: `KindMark`, `ScopeChip`, `OwnerMark`,
`TriggerSummary`, `SessionMark`, `ListSwitch`, `NextRuns`, `DayToggles`,
`SchedulePicker`, `ChipsInput`, `RepoFilterField`, `AgentTools`, and the helpers
`describeCron`, `scheduleOf`, `cronOf`, `repoGlob`, `repoFilterCount`,
`cityOf`; `src/components/Incoming.tsx`: `ConditionRows`, `DeliveryVerdict`.
Gallery: Automations (`#automations`, `#au-marks`, `#au-owner`,
`#au-schedule`, `#au-chips`, `#au-repo-filter`, `#au-conditions`,
`#au-delivery-verdict`, `#au-tools`), in both themes and
densities. No tone of their own: an automation's state is its runs'
(`StatusMark`), so `paletteDistance` is untouched.

- **ConditionRows** (`value`, `onChange?`, `results?`, `paths?`): an incoming webhook's conditions on its JSON body, AND'd. Each row is a mono path `Input`, an operator `Select` (is, is not, one of, contains, exists) and its value: an `Input`, a `ChipsInput` for one of (values OR'd), nothing for exists. Rows lead with "When"/"and"; with `results` the lead is a ✓ (success tone) or ✗ (danger tone) mark named "Holds"/"Does not hold", and a missed row says what the body had. "+ Condition" adds a row; each row's remove button is named "Remove condition N". Below 720px a row stacks path and operator over its value.
- **DeliveryVerdict** (`verdict`, `onRun?`, `detail?`): what dude did with one delivery, as a chip at the chip height: Started run (success, play glyph), Sent to running agent (info, send), Skipped (neutral, dotted circle) and the condition that missed, Refused · Bad signature / Not JSON / Too large / Rate limited / Turned off (danger, circle-x); Waiting (neutral, clock) while dude has not read it yet. With `onRun` the first two are buttons named "<verdict>: open the run" when they have a run; a sent verdict with none is a plain chip. Tones are the existing five.

- **KindMark** (`kind`, `role?`): the kind as glyph and word (Agent,
  Command, Server, Send a webhook); an agent that is one of dude's roles adds
  "· Tester" in muted text.
- **ScopeChip** (`org`, `project?`, `projectId?`): where an automation lives:
  "Organisation" with a building glyph on the info tint, or the project's
  `ProjectAvatar` and name on the neutral tint, at the chip height.
- **OwnerMark** (`owner`, `org`, `avatarOnly?`, `left?`): who owns an
  automation — `PersonAvatar` and name, or only the avatar with the name for
  screen readers (list cells). `left` (or no owner): the organisation's
  admins hold it — a building glyph in info tone and "Acme's admins".
- **TriggerSummary** (`trigger`, `wrap`): one line, or with `wrap` words
  that wrap in a narrow cell, the glyph staying beside the first word; a long event name breaks under the glyph rather than being cut off, and a line never ends on the "·" alone. A schedule in words when it is a
  preset ("Daily at 03:00", "Weekdays at 09:30", "Every 15 minutes") else
  the cron line, then the zone's city muted; an event in mono and its
  actions, or "any action"; an incoming webhook as "Incoming" and, muted in mono, its first condition and "+N" for the rest, or "any JSON".
- **SessionMark** (`mode`, `expiryDays?`): Ephemeral, or Resumable and its
  expiry in days.
- **ListSwitch** (`checked`, `onCheckedChange`, `label`): the settings
  `Switch`'s track and thumb (its very classes) with no words beside it, for
  a list row. Its accessible name is "<label>: on/off"; a press does not
  open the row.
- **NextRuns** (`times`, `tz`): the next runs as a list of `time`s in the
  schedule's zone; none says it never runs.
- **DayToggles** (`days`, `onChange`): seven pressed/unpressed buttons,
  Monday first; the last one on cannot be turned off.
- **SchedulePicker** (`cron`, `tz`, `zones`, `onChange`, `next?`,
  `problem?`, `zoneSource?`): a `Segmented` Minutes / Hourly / Daily /
  Weekly / cron, the mode's fields in the DS `Select`, `NumberInput` and
  `Input` (every N minutes, minute of the hour, a 24-hour `HH:MM` time, days,
  or the cron line in mono), and the time zone (`zoneSource` says whose it
  is). Under it, "As cron … · Advanced: write a cron expression". `next` is
  shown under that (the app's `NextRuns`, from the orchestrator's parser);
  `problem` is an alert line in danger text, and marks the cron field invalid.
- **ChipsInput** (`values`, `onChange`, `label`, `matches?`,
  `suggestions?`): filter values as chips in a field box. Enter or comma
  adds, Backspace in an empty field takes the last back, each chip has a
  Remove button named for its value. A chip with `*` is info-toned and,
  with `matches`, counts what it matches. With `suggestions` the field is
  a combobox whose listbox (raised surface, `shadow-1`) groups values by
  heading, filtered by what is typed.
- **RepoFilterField** (`value`, `onChange`, `org`, `groups`, `disabled?`):
  which of the organisation's repositories an organisation automation
  covers. A `Segmented` picks All repositories in <org>, Only these or All
  except these; the last two show a `ChipsInput` of owner/name values and
  globs (`acme/svc-*`), suggested from the projects' repositories grouped by
  project. Under it a status line: "Matches N of M repositories" (", and
  any added later" on All).
- **AgentTools** (`tools`, `unavailable?`): the tools an automation's agent
  can call. Each is a wrench glyph, the mono name, an info "New" badge when
  `fresh`, and its one line, in two columns that stack when it is under
  400px of its own width. An unavailable tool keeps its row and gets an
  attention `Callout` under it with the reason and the fix as a link.
- **Settings rows stack below 720px.** `SettingRow` puts its label over its
  control and `SettingsHeader`'s actions wrap under the title, so a page's
  Run now and switch never squeeze its title on a phone.
