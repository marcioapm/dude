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
ScrollArea) sit on Radix, which is unstyled and does keyboard/ARIA correctly.
Everything else is hand-rolled.

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
| Diff | `--ds-diff-{add,del}-{bg,bg-strong,fg}`, `--ds-diff-hunk-{bg,fg}` | Softer than the tones; read for minutes. |
| Elevation | `--ds-shadow-1/2/3` | Includes the hairline ring. Theme-dependent. |
| Type | `--ds-font-sans/mono`, `--ds-text-2xs…4xl`, `--ds-weight-*`, `--ds-leading-*`, `--ds-tracking-*` | Body is `text-md` = 13px. |
| Space | `--ds-space-0…64` | 4px grid plus 2 and 6. |
| Radius | `--ds-radius-xs…xl, full` | `md` = 4px is the default. |
| Size | `--ds-size-control-sm/md/lg`, `--ds-size-row-compact/default/comfortable` | 24/28/32 and 24/28/36. |
| Motion | `--ds-duration-fast/base/slow/deliberate`, `--ds-ease-*`, `--ds-motion-live` | Reduced motion zeroes durations and sets `motion-live` to 0. |
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
  `2h 04m`). Live durations tick once a second, never faster.
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
  arrived, and enter transitions on overlays.
- Everything that loops must be multiplied by `--ds-motion-live` (see
  `StatusBadge.module.css`) so reduced motion freezes it in a legible state.

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

## Components

`src/primitives/` — Button, IconButton, Input, Select, Checkbox, Badge, Card,
Table (THead/TBody/Tr/Th/Td/TableEmpty), Tabs, Dialog, Toast, Tooltip,
Skeleton/SkeletonLines/Spinner, EmptyState, ScrollArea.

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

## What is deliberately not here

- Charts. When they arrive, series colours must come from a validated
  categorical palette, not the tones or role colours. The tokens module
  exports the raw OKLCH helpers for that.
- Virtualisation. `Table`, `EventStream` and `LogStream` render plain DOM so
  any row virtualiser can be applied by the consumer.
- A Kanban board, side panel, or page layout. Those are app concerns; they
  compose the pieces here.
