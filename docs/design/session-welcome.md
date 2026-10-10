# The welcome page

**Status: built (2026-10-10).** Chosen by Márcio: option A with a short
list of recent sessions, as dude's initial page, and a sidebar that
collapses. Mockup: `docs/design/mockups/welcome/index.html` (opens from
disk, interactive; source in `apps/web/mockups/welcome`, built with
`MOCKUP=welcome bunx vite build --config mockups/vite.config.ts`).
Screens in `docs/design/mockups/welcome/screens/`. The decisions taken
while building it are under *Built* at the end.

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
| recent row (`size-row-default`) | 32 | 28 |

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

- **API**: `POST /v1/brainstorms` (orchestrator `POST /internal/sessions`)
  takes an optional `message` (the first message: 1 to 16 384 bytes in
  UTF-8 after trimming, the orchestrator's limit and the same bounds as a
  message in Chat; the backend checks the same byte length) beside `projects`.
  Given one, it does create + link + start the agent in one transaction,
  so a failed send leaves no empty session, and answers with `runId` as
  well as `id`. Without one it is the bare create it was: the API is
  additive. Image attachments on the first message are not taken yet.
- **Recent sessions** is the existing `GET /sessions` (newest activity
  first); it already carries title, filed count, shared and owner.

## What the design system gains

In `packages/design-system` (README, gallery in both themes and densities,
with the build):

- **`Welcome`** — the stage: face, greeting, line, a composer slot, a
  starters slot and a recent slot; centres its column, and narrows on a
  phone.
- **`StarterPills`** — the starters: `raised` pills with a full radius,
  their glyph in the label's ink (secondary, brightening on hover): they
  are actions, not the brainstorm's role glyph.
  No border: shade in dark, `shadow-1` in light, a `secondary-hover` wash on
  hover. Each takes `{ id, icon, title, detail, prompt }`; `onPick(starter)`.
- **`ComposerLinks`** — the link chips for `ChatComposer leading`: neutral
  wash chips with the project's face and a 12px close, a quiet "+ Link"
  opening a float menu (`float` radius, `overlay`, `shadow-3`); read only
  without `onLink`/`onUnlink`.
- **`Sidebar collapsed` / `onCollapsedChange`** — the rail, drawn by the
  sidebar itself from what it already has (projects, counts), plus three
  small props for what only the app knows: `railMark` (with `onHome`,
  `homeSelected`), `railSessions` (New session and Sessions) and
  `railFooter` (the band's `SidebarRailItem`s). The collapse chevron is the
  sidebar's own, shown when `onCollapsedChange` is given.
  `SIDEBAR_DRAWER_QUERY` still decides drawer vs rail.
- **`RecentSessions`** — the short list: one-line rows of `row-default`
  height told apart by space, a `row-hover` wash, the age tabular.

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

## Built

What the build decided where this page left it open:

- **Linking from the welcome links the whole project**: every repository
  it has, read with `getProject` as soon as the project is picked (and
  again at send time if that read had not come back). The rail's Link
  dialog narrows it afterwards.
- **The link menu is `RowMenu`** (Radix: arrows, Enter, Escape, portaled
  over the raised composer), whose items gained a `leading` face; no
  hand-rolled menu. Its float takes `shadow-3` here.
- **`RecentSessions` is its own one-line row**, not `SessionRow`: the
  list's row has two lines, project chips and a state column that the
  welcome's short list leaves out. It reuses `SharedMark`.
- **How many recent sessions** is the app's choice, by the density in force
  (`RECENT_SESSIONS_SHOWN`: 4 comfortable, 6 compact); `RecentSessions`
  draws what it is given.
- **The composer is raised by a prop of its own, not by its host**:
  `ChatComposer variant="stage"` (raised `shadow-2`, a 72px field, 52
  compact, `text-prose`, no inset), which the app passes; `Welcome` does not
  reach into its child.
- **An organisation with no projects** gets the welcome too: a session
  needs no project ("Reads memory only"), so New session always works. Its
  footer also offers New project, under the recent sessions or the
  first-time line, to an admin (who may make projects), and only once the
  tree has loaded and is empty, so nothing flashes while it loads.
- **The tab reads "dude"** on the welcome, as with nothing selected before.
- **`[`** is ignored in an input, textarea, select or contenteditable,
  with ⌘ or Ctrl held (AltGr, Ctrl+Alt, still types it; Shift and Option
  are how some layouts type `[` and `/`, so they do not refuse it), and
  under 1000px, where there is no rail. Nothing
  else bound it. **`/`** on the rail, under the same guards, unfolds the
  sidebar with its search focused, as the rail's search item does; the
  `Sidebar` handles it, since it owns the field.
- **The brand's accessible name is "El Duderino, home"**: its visible
  words, then where it goes.
- **A session made through the API without a message can be empty**; its
  writers keep the "Write to start…" line and a focused composer, its
  readers "Nobody has written here yet."
- No token, tone, radius or border exception was added.

## Model and harness

A session's agent runs on the organisation's **Brainstorm** setting
(Settings → Agents) unless the session chooses otherwise. The welcome's
composer says which, after "To **Brainstorm**": `ModelPicker` in
`ChatComposer`'s `toAside`, a quiet chip reading the effective pair
("Thinker · Claude Code", "default" while both follow the organisation).
Its menu has two groups, **Model tier** and **Harness**, each led by
"Organisation default (…)".

- **Each half is the session's own or the organisation's**, independently
  (`sessions.tier`, `sessions.harness`, migration 105; NULL follows). The
  welcome sends only what was chosen, so a session left alone keeps
  following the organisation when its setting changes.
- **An invalid pair is never offered.** An item whose harness cannot run
  the tier's model is disabled with the reason ("Claude Code takes an
  Anthropic model (claude-…); Sol requests gpt-6-sol"), judged by
  `harnessMisfit` against the other half as it stands, and worded with
  `harnessWants` (the orchestrator's `harnessWants` says the same). A tier
  naming no model is disabled too ("names no model yet"). The orchestrator
  checks the pair the next start would use and refuses a misfit, or a tier
  with no model, with a 400, on create and on `/model`. A session that
  chooses neither half is not checked: a misfit there is the
  organisation's, and its Run fails saying so, in the admin's words.
- **A change applies at the agent's next start, never mid-turn.** The
  rail's **Model** block carries the same picker for the owner (everyone
  else reads it); a change records `session.model.changed`, which the Chat
  shows ("Ana set the model to Opus (High) · Claude Code; it applies the
  next time the agent starts"). A live or parked agent resumes on what it
  was submitted with. The owner's chip is a button at once; its menu reads
  the organisation's tiers when it first opens, and says "Could not load
  the tiers" when it cannot. A pick shows at once and the detail takes
  over once the last pick is answered; a failed pick cancels the picks
  queued behind it, which carried its half.
- **A tier removed falls back.** A trigger on `model_tiers` sets each
  session that chose it back to the organisation's tier, under the row's
  lock, and records `session.model.fallback` on each one it changed, so
  its members are told (`ON DELETE SET NULL` stays as the net).
- **A pair that stops fitting later** (its tier removed, or the
  organisation's half changed) is said where it is seen: the detail's
  `model.misfit`, under the chip with a danger mark, and the Run's failure,
  both in the session's words ("…Choose another harness or tier in the
  session's Model.").
- **The header** says "Brainstorm · <tier> · <harness>": a live agent's as
  its Run records them; with none live, what the next start would use,
  "(organisation default)" when it follows.
- Out of scope: a session's own effort (a tier carries it), machine size or
  image.
