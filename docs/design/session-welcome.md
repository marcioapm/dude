# The welcome page

**Status: chosen direction (Márcio, 2026-10-10): option A with a short list
of recent sessions, as dude's initial page.** Not built yet. Mockup:
`docs/design/mockups/welcome/index.html` (opens from disk, interactive;
source in `apps/web/mockups/welcome`, built with
`MOCKUP=welcome bunx vite build --config mockups/vite.config.ts`).
Screens in `docs/design/mockups/welcome/screens/`.

## The problem

New session made a session and opened it with the composer focused. What
it showed was the Chat of a conversation that had not happened: one muted
line at the top, a rail of five blocks each saying it was empty, and the
composer at the foot of a tall blank panel. Nothing said what a session is
for or how to start, and the first thing to decide — what it reads — sat
in the rail, away from where you write. And opening dude with nothing
selected jumped to the first project's board, which is rarely where
anyone meant to go.

## The page

- **dude opens on it.** With nothing in the URL, the app shows the welcome
  (`#/`), not the first project's board. The sidebar's brand (dude's face
  and "El Duderino") is a link home to it, and the tab reads "dude".
- **New session opens it too**, the sidebar's and the Sessions list's.
  **Nothing is made until the first message is sent**: sending makes the
  session with that message and what it reads, in one call, and opens it.
  No more untitled empty sessions left behind by a click.
- **What it shows, top to bottom**, in a 720px column a little above the
  middle of the page:
  - dude's face (`DudeMark`, 64px), the greeting — the time of day on the
    reader's clock and their first name, "Afternoon, Márcio" — and one line,
    "What are we working out today?";
  - **the composer**, raised (`shadow-2`), taller (72px, 16px text), placeholder
    "Start a session: an idea, a question, a plan…", focused;
  - **what it will read, in the composer**: `leading` link chips, each
    linked project's face and name with a close, then "+ Link" (a float
    menu of the organisation's projects); "Reads memory only" and "+ Link a
    project" when there are none;
  - **four starters**, as pills: *Plan an epic*, *Shape a task*, *Ask the
    code*, *Triage what's open*. A starter **fills, never sends**: it writes
    the start of a sentence ("I want to plan an epic for ") with the caret at
    its end;
  - **Recent sessions**: your four most recent (the same ones the sidebar
    lists), each the bulb glyph, its title, the shared mark when shared,
    what came of it in muted ink ("Filed 1 epic, 4 tasks"), and its age;
    "All sessions" opens the list. With no sessions yet, a muted line
    instead: "A session reads what you link, asks what it needs and
    proposes work. It changes nothing: you file what you want."
- **After the first send it is the session as now**: transcript, rail,
  composer at the foot. The rail's Linked block keeps the detail. Linking
  later is the owner's, from the rail, as now.
- **A phone** (under 640px): the greeting takes a size down (`3xl`), the
  stage less air, and the recent rows drop their summary.

## Comfortable and compact

The welcome follows the density rule (README *Density*): compact takes the
big spaces in, never the text below readable.

| | comfortable | compact |
|---|---|---|
| space above the column | 12vh | 6vh |
| gap between its parts (`space-panel-gap`) | 24 | 16 |
| dude's face | 64 | 48 |
| greeting / line | 34 / 16 | 34 / 16 (unchanged: one per page) |
| composer field height, text (`text-prose`) | 72, 16 | 52, 15 |
| recent sessions shown | 4 | 6 |
| recent row (`size-row-comfortable`) | 40 | 36 |

At 1440×900 both fit without scrolling; compact shows two more sessions.

## A collapsible sidebar

On a wide screen (1000px and up) the sidebar folds to a **56px rail** and
back; under 1000px it stays the drawer it is today.

- **Collapse** with a quiet chevron-left beside the sidebar's title, or
  **`[`** from anywhere outside a field; **expand** with the chevron-right
  under the rail's face, or `[` again. The choice is the person's and is
  remembered (`dude.sidebar` in localStorage), as the density is.
- **The rail is the sidebar's rows with their words folded away**, in the
  same order and on the same chrome shade, each a 40px square with the nav
  row's hover and current washes and its name in a tooltip to the right:
  dude's face (home: the welcome), expand, search (expands with the field
  focused, `/`), **Waiting on you** (the inbox glyph, or the `NeedsYouCount`
  pill itself when there is something — the one loud thing stays loud),
  New session, Sessions (the bulb; your recent ones in its tooltip), then
  **each project's face** — pressed, its board; a project with something
  waiting on you wears the needs-you diamond on its corner, cut out of the
  chrome, and its tooltip gives its counts in words ("1 needs you · 4
  running · 1 failed") — and the band: organisation settings and your face.
- **The tree is not in the rail.** Epics, tasks and agents need their words;
  a project's face opens its board, and expanding gives the tree back.
- Density moves the rail's rows as it moves the sidebar's
  (`size-row-comfortable` 40 → 36, `space-nav-row-gap`, `-nav-section-gap`);
  its width stays 56.

## What changes outside the web app

- **API**: `POST /sessions` takes the first message and the projects to
  link (`{ message, attachments?, links? }`) and does create + link + chat
  in one transaction, so a failed send leaves no empty session. The old
  bare create stays only if something else uses it.
- **Recent sessions** is the existing `GET /sessions` (newest activity
  first); it already carries title, filed count, shared and owner.

## What the design system gains

In `packages/design-system` (README, gallery in both themes and densities,
with the build):

- **`Welcome`** — the stage: face, greeting, line, a composer slot, a
  starters slot and a recent slot; centres its column, and narrows on a
  phone.
- **`StarterPills`** — the starters: `raised` pills with a full radius,
  their glyph in the brainstorm's role colour (these are the brainstorm's).
  No border: shade in dark, `shadow-1` in light, a `secondary-hover` wash on
  hover. Each takes `{ id, icon, title, detail, prompt }`; `onPick(prompt)`.
- **`ComposerLinks`** — the link chips for `ChatComposer leading`: neutral
  wash chips with the project's face and a 12px close, a quiet "+ Link"
  opening a float menu (`float` radius, `overlay`, `shadow-3`); read only
  without `onLink`/`onUnlink`.
- **`Sidebar collapsed` / `onCollapsedChange`** — the rail, drawn by the
  sidebar itself from what it already has (projects, counts, sessions
  slot, footer), so the app passes one flag; a `SidebarCollapse` button for
  the header. `SIDEBAR_DRAWER_QUERY` still decides drawer vs rail.
- **`RecentSessions`** — the short list: rows of `row-comfortable` height
  told apart by space, a `row-hover` wash, the age tabular.

Rules to add to the README's *Brainstorm sessions* (and, for the rail,
*Triage (the sidebar)*: collapsed, the rail keeps the one loud count and
marks each project that waits on you; nothing else in it takes a tone):

- dude opens on the welcome; New session opens it; a session is made by
  its first message, never empty.
- What a session will read is said in its composer before it starts.
- A starter fills the composer; it never sends.

Nothing here adds a token, a tone or a radius; `borders.test.ts` and
`radius.test.ts` need no entries (the composer's field keeps its own edge).

## Options considered

- **B · Starters**: composer docked at the foot, starters as tiles. Explains
  more, but reads least like a place to start a conversation.
- **C · From your work**: A plus lead cards (the question waiting on you, a
  failing task, an epic in review). Worth revisiting once the welcome is in.

## Open

- The greeting's line: plain, or one of dude's ("The Dude abides.")?
