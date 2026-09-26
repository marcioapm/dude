# Overnight, 2026-09-24

> A dated record. What it calls a "work item" is a **task** since 2026-09-26
> (migration 026).

What was built while you slept, what was decided on your behalf, and what
is left. Everything below is committed on `main` (not pushed), tested, and
reviewed by at least one independent code-review pass; the real stack
(orchestrator :3110, backend :3010, web :5190 on the `dude_real` database,
lux `d6841fbc`) runs the latest code.

## Built

**Findings are judged by the reviewer** (`e7e2fe0`). A re-review is shown
the findings its category's fixer was sent and answers for each, fixed or
still. The two old shortcuts — a fix touching the file "supersedes" a
finding, a re-review not mentioning it "resolves" it — are gone.

**Colour in tool output** (`a8cb138`, `3117f89`, `44336ea`). Agents' tools
emit colour (forced in their environment) and the chat renders it, safely:
the parser produces styled text, never markup, and survives truncated and
malformed escape codes. Seen on a real run: `git diff` and pytest in colour
(`docs/images/coloured-tool-output.png`).

**Dialogs with dropdowns no longer freeze** (`eb89ea1`). CSS modules had
renamed every shared animation, so none ran — and Radix waits for a popup's
exit animation before removing it. A test now guards every stylesheet.

**Managing work** — from the design proposal in
`docs/design/management.md`:

- API (`e57aa47`, `79df3ea`, `443d324`): repositories add/edit/remove
  (git-safe URLs only; a repository in use or with PR history is
  protected), epics create/edit/reorder/delete (deleting keeps the work),
  work items edit/move, a work item names its repository (multi-repo
  projects can deliver now), work item keys like `TEXT-12`, the GitHub
  connection shown masked and verifiable.
- UI (`15fd97a`, `6896fb6`, `e524b1e`):
  - a work item dialog (goal, acceptance criteria, epic, repository,
    "Create and deliver"; Edit/Move from the work item screen);
  - project settings — Repositories, Agents, Delivery (reviewers,
    blocking severities, rounds), General; only choices that differ from
    the factory's defaults are stored;
  - a New project dialog (sidebar and the empty screen);
  - organization settings: the GitHub connection with Verify and Replace.
- Epics and moving work (`8bdf48c`, `c04f793`): every row in the sidebar
  has a "…" menu (also Shift+F10 or the context-menu key) — new work item,
  new epic, settings on a project; edit, move up/down and delete on an
  epic; edit and "Move to epic" on a work item. The board groups by epic
  (a toggle that is remembered), each lane with its own menu. Breadcrumbs
  (project › epic › `TEXT-10` › agent) replace the Back buttons, and
  review findings are grouped rows with their severity, file and state.
- Polish from looking at it on the real stack (`76a487a`):
  - the "No epic" lane rendered empty: one long card title stretched
    every column, pushing Review off the screen;
  - a spacing token that never existed made several gaps zero (a test now
    checks that every token used is defined);
  - Back, Forward and pasted links now move the app;
  - a proper settings icon.
- A final review of the management UI (`f1adbf8`) found, and
  fixed: editing an epic blanked its description; a slow load could send
  an edit to the wrong work item; Back after deleting an epic led nowhere;
  focus was lost after a dialog opened from a row menu. Each has a test.

## Decided on your behalf (from the design proposal; easy to revisit)

1. **Epics are ordered groups only** — no settings of their own. Policy
   lives at two layers, project and work item.
2. **A work item changes one repository.** Work across several is split
   into sibling work items.
3. **"Task" is not a noun in the UI.** A work item is what a person asks
   for; the prompt dude writes for an agent is its task.
4. **Projects are archived, never deleted** (archive not built yet).
5. **Work item keys** use the slug's first four letters (`textkit` →
   `TEXT`). Not editable yet.
6. **What a work item asks for is fixed once delivery starts**; where it
   sits (its epic) is not.

## Left for daylight

- Work items without a repository (a brainstorm producing a document) and
  the artifacts that carry their output: designed, not built.
- Organization-level defaults for models and delivery (the proposal's
  "inherited" everywhere).
- Archiving a project, splitting a work item, editable key prefixes.
- Dragging cards between lanes (moving is by menu for now).
- Markdown streaming tuning (task #20).

## Tests at the end of the night

TypeScript typecheck clean; design system 100 tests; control plane 20;
Go (database tests really running, not skipped); end to end 97 including
8 in the browser.
