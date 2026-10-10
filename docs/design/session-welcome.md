# A new session's first screen

**Status: proposal, three options. Nothing is built.** Mockup:
`docs/design/mockups/welcome/index.html` (opens from disk; source in
`apps/web/mockups/welcome`, built with
`MOCKUP=welcome bunx vite build --config mockups/vite.config.ts`).
Screens in `docs/design/mockups/welcome/screens/`.

## The problem

New session makes a session and opens it with the composer focused. What
it shows is the Chat of a conversation that has not happened: one muted
line at the top, a rail of five blocks that each say they are empty, and
the composer at the foot of a tall blank panel. Nothing says what a
session is for, nothing suggests how to start, and the one thing you must
do first — say what it reads — is in the rail, away from where you write.

## What all three options share

- **Until the first message, the page is the composer's.** No empty
  transcript, no rail. The rail comes with the conversation; everything it
  would say empty is said once, in a sentence, or not at all.
- **What it reads goes in the composer.** The linked projects are chips in
  the composer's `leading` slot ("Reads control-plane · web · + Link"),
  removable, with "Reads memory only" when nothing is linked. Linking is
  the owner's, so for anyone else the chips are read-only. This stays after
  the first message too; the rail's Linked block keeps the detail
  (repositories, where they are checked out).
- **Starters fill, never send.** Each writes the start of a sentence in the
  composer with the caret at its end ("I want to plan an epic for "), so
  the person finishes it. The four: *Plan an epic*, *Shape a task*, *Ask
  the code*, *Triage what's open* — the things a brainstorm is for.
- **After the first message it is today's session**: transcript, rail,
  composer at the foot. The welcome does not come back once anyone has
  written; a reader who opens a session nobody has written in sees
  today's "Nobody has written here yet."
- **The greeting** is the time of day on the reader's clock and their first
  name ("Morning, Márcio"). One per page, not a heading per section.

## The options

**A · Centred** (closest to ChatGPT / Claude). dude's face (`DudeMark`,
64px), the greeting, one line, the composer in the middle of the page,
raised (`shadow-2`, taller field, 16px text), the starters as a row of
pills under it, and the promise in muted small print: "It reads what you
link, asks what it needs and proposes work. It changes nothing: you file
what you want." The calmest; says least.

**B · Starters.** The composer stays at the foot, where it will be once the
conversation starts, so nothing moves on the first send. Above it, left
aligned: the brainstorm's face, the greeting, the promise as the subtitle,
the four starters as tiles with a sentence each, and your recent sessions
(`SessionRow`). Most explanatory; best for someone new to sessions; the
least like a chat app.

**C · From your work.** A, plus **leads**: what dude already knows is
worth talking through, each a card that fills the composer with a prompt
about it. The question waiting on you (in the needs-you tint and bar,
since it is), a task that failed twice, an epic with work in review, your
last session (which opens it rather than filling). The subtitle names the
two that matter most. Most useful day to day and the most dude-specific
— no other tool knows what is stuck in your factory — but it needs a
"leads" read on the API, and it must stay quiet: four at most, nothing
loud but the one thing that is already loud (needs-you).

My pick is **C, built as A first**: A is a change to the session screen
alone; C's leads follow with one endpoint and no new layout.

## What the design system gains

New, in `packages/design-system` (README, gallery, both themes, both
densities, with the build):

- **`SessionWelcome`** — the stage: greeting, subtitle, the composer slot,
  starters, and an optional `leads` slot. `layout="centred" | "docked"`
  (A/C vs B). Under 640px the greeting takes a size down (`3xl`) and the
  stage less air.
- **`StarterPills`** / **`StarterTiles`** — the starters. Pills are `raised`
  with a full radius (they are pressed, like chips); tiles are `raised`
  `control`-radius cards with the brainstorm-role square glyph. No borders:
  shade in dark, `shadow-1` in light, a `secondary-hover` wash on hover.
  The glyphs take the brainstorm role colour — role colour identifies who,
  and these are the brainstorm's.
- **`LeadCard`** — one lead: a `StatusMark` or a quiet glyph, a heading line
  (what and how long), the project's face and name, a title and a line.
  A needs-you lead takes the one emphasis pattern (`tint-needs-you`, the 2px
  attention bar, square on the bar side); nothing else is tinted.
- **`ComposerLinks`** — the link chips for `ChatComposer leading`: neutral
  wash chips with the project's face, a 12px close, and a quiet "+ Link".

Rules to add to the README's *Brainstorm sessions*:

- A session nobody has written in opens on its welcome, not on an empty
  Chat; the rail appears with the first message.
- What a session reads is said in its composer as well as its rail.
- A starter or a lead fills the composer; it never sends.
- The welcome's only loud thing is a lead that is already needs-you.

Nothing here adds a token, a tone or a radius; `borders.test.ts` and
`radius.test.ts` need no new entries (the composer's field keeps its own
edge, as now).

## Open questions

1. Which option (or which mix)?
2. C's leads: which kinds, and from whom — only yours, or the project's?
3. Should New task's dialog get the same starters? (Out of scope here.)
4. Greeting: time-of-day, or one of dude's lines ("The Dude abides. What
   are we working out?") — he has a voice elsewhere (`dudeName`).
