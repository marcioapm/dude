# Calmer, for a team: the plan

How the mockup (`mockups/calmer-team-v8.html`, the last of eight rounds;
the earlier ones are on the `design/calmer-mockups` branch) becomes the product, and how the design system keeps them honest.

## Two tracks

The mockups ask for two kinds of work that move at different speeds:

- **The design system and the screens**: shapes, faces, PR state, the
  plan, the editor, the live diff. Frontend only, over data dude mostly has.
- **What the screens need that dude doesn't do yet**: people and roles,
  several people per task, presence, a GitHub App and merging, project
  settings that override the organisation's, prompt history, the live diff's
  source. Backend, migrations, lux.

The design-system track goes first, and each screen ships as soon as its
data exists, with the rest behind it. Nothing waits for everything.

## Phase 0 — the rules become code (first, before any screen)

Change the design system's foundations, and add the guards that keep every
later change inside them.

1. **Tokens.**
   - Radius becomes three named roles: `--ds-radius-control` 6 (buttons, fields,
     menu rows), `--ds-radius-float` 10 (dialogs, popovers, toasts, the viewer),
     and `--ds-radius-face-project` (22% of size).
   - Structure (panels, columns, cards, tables, the pipeline, the chat) has no radius.
   - Add `--ds-merged` (violet), and let identity colours cover projects as well as people.
2. **Guard tests** in `packages/design-system/test/`, the same kind as
   `tokenName.test.ts` and `keyframes.test.ts`, run by `bun test` and in CI:
   - `radius.test.ts`: every `border-radius` in a CSS module is 0, a radius
     role token, `50%` (a person's face), or `inherit`. The old
     `--ds-radius-md/lg/xl` are gone, so using one fails to type-check.
   - `borders.test.ts`: a `border`/`border-*` declaration is allowed only
     in field, focus and diff modules (an allow-list, each entry with its
     reason). Regions are told apart by shade.
   - `raw-values.test.ts`: no hex, `rgb()` or `px` colours or shadows
     outside `tokens/`; the app (`apps/web`) has no CSS values at all beyond
     layout.
   - `pointer.test.tsx`: renders every interactive primitive and component
     in the gallery's fixtures and checks `cursor: pointer` on anything with
     an `onClick`, a link, or `role="button"`.
   - `buttons.test.tsx`: `Button` takes `variant: primary | secondary |
     quiet | danger` and nothing else; a view with two primaries fails the
     gallery's screen checks.
   - `status.test.ts`: every status has a tone, a glyph and a label
     (extends the existing "meaning never in hue alone" rule).
3. **The gallery is the spec.** Each new component lands in the gallery
   first, in every state, dark and light, both densities. The existing
   `test_gallery_ui.py` gains screenshot comparisons per section, so an
   unintended visual change fails CI with a before/after image.
4. **Docs.** The README's Philosophy gains the four rules from the mockups'
   Rules screen (square structure, lines for fields and focus, status as
   glyph and word, faces), the button kinds, the PR-state priority and
   the cost rule. The README is what a reviewer checks against.

**Done when** the guards fail on today's code where it breaks the rules (the
list is the Phase 1 to-do), and pass after it.

Measured on `main` (56591d2), the guards would flag today:

| Rule | Today |
|---|---|
| a radius that isn't 0, a role, or a face | 128 declarations |
| a border outside fields, focus and diffs | 112 declarations in 38 files (some will be allow-listed) |
| a raw colour outside `tokens/` | 6 |
| button variants | 5 (`primary`, `secondary`, `ghost`, `destructive`, `destructive-outline`) → 4 |

So the guards land **with** the fixes, in one PR per rule, each small enough
to review: the guard, then the files it names, then green.

## Phase 1 — the new look, on the screens that exist

No backend changes. Every current screen moves to the new shapes, buttons,
status and faces.

- Primitives: `Button` variants, `StatusMark` (glyph + word) replacing the
  bordered status badges, `Avatar` (person, round) and `ProjectAvatar`
  (square), agent tiles unchanged.
- Components: `PrChip` with the priority order and its tooltip; `Cost` (a
  total with the tokens/machine split, tooltip only until machine cost
  exists); `AgentPlan` pinned and folded; `MarkdownDocument` with
  Edit → source → Save/Cancel.
- Screens: sidebar (running agents only, profile band, ⋯ menu), board,
  task page (PR panel, findings, sessions with the pinned plan), inbox.
- Fixes from the readiness review that are purely frontend: signed steers
  (the actor is already in the ledger), Abort confirmation, conflict
  notices that name the person, a reconnect indicator.

## Phase 2 — people (backend + screens)

The assessment's blockers for a team, in the order they unblock each other:

1. Members: invite, list, revoke, roles (organisation admin / member;
   project admin / member). A person has many keys and is one person.
   Google sign-in.
2. "Waiting on you" filtered to the viewer; others' items under "Waiting on
   others" with Take over.
3. Several people per task (`task_people`, owner first); agents record who
   they work for.
4. Presence: `last_seen_at` per person, touched at most once a minute by
   any request, pushed over the existing SSE stream. No Redis at this size.
5. Profile photos and project images, in an S3 bucket (`DUDE_S3_*`), served
   by the backend under a token.

## Phase 3 — projects, epics, settings

- Epic states (planned / in progress / done) and the project page.
- Settings split: yours / organisation / project. Project values override
  the organisation's, stored as overrides only (a missing key means "from
  the organisation"), so "Reset" is a delete.
- Agents per role with the Markdown prompt; a project can add to, replace
  or use the organisation's prompt.
- Prompt history: every save is a row (`prompt_versions`), sessions record
  the version they ran with.

## Phase 4 — GitHub, deeper

From the GitHub review, most important first:

1. Register webhooks (GitHub App, or `EnsureWebhook` with the secret shown).
2. Only collaborators wake a fixer.
3. A fix starts from the PR's head, so a person's push doesn't break it.
4. Mergeable state and conflicts; "Update branch".
5. The PR state the chip shows, from real data: checks by name with links,
   reviews by person, unresolved threads; CI failures passed to the fixer
   with the failing check's log.
6. Merge from dude.

## Phase 5 — live work

- **Live diff**: dude runs `git diff <base>` through lux's exec after each
  Edit or Write the stream reports (it already sees them), and pushes the
  result over SSE. Poll first if that's quicker; the screen doesn't change.
- **Files**: the viewer, versions and zip download over the artifacts dude
  already records.
- **Machine cost**: a per-host rate × a Run's time, which lux reports; the
  `Cost` component shows the split once both halves are real.

## How we keep to it

- **The guards** (Phase 0) fail CI on the rules a machine can check.
- **The gallery** is where a component is designed; the screenshot diff is
  what makes a change deliberate.
- **Review** checks what the guards can't: the README's rules, one primary
  per view, names over "Human", nothing loud but Needs you. dude's own
  reviewer has a `design-system` category that runs when a diff touches
  `apps/web` or `packages/design-system`, with the README as its brief.
- **Process**: each phase lands as its own PRs, after a clean code review
  and a simplify pass, then a browser pass over every screen it touched,
  dark and light, before it's called done.
