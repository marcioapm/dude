# Design proposal: managing projects, epics, work items, repositories

## 1. Information architecture

**Nouns, and what they mean to a person.** *Project* = a codebase boundary with settings. *Epic* = an ordered, named group of work items inside a project; nothing more. *Work item* = one thing a person asked for; the unit with a status, a cost, a chat. *Task* never appears in the UI as a noun: it is the prompt dude writes for one agent Run (the "Task" tag on the prompt turn stays). *Repository* = a project-level record a work item points at. *Run/Session* stay execution detail.

**Where things live.** The shell stays two-pane. The sidebar is for finding and triage; the main pane shows one of five things, keyed by the URL hash:

| Hash | Main pane |
|---|---|
| `#/project/:id` | project board (existing) |
| `#/project/:id/settings` | project settings screen (new) |
| `#/epic/:id` | epic board (existing); edit is a dialog on top |
| `#/workItem/:id` | work item screen (existing, extended) |
| `#/run/:id` | transcript (existing) |
| `#/org/settings` | organization settings (new) |

Rules: **settings are screens, forms are dialogs.** Anything with tabs or a table (project settings, org settings) is a screen. Anything with under eight fields (new project, new/edit epic, new/edit work item, move, split) is a `Dialog` (the README already says: dialogs are for decisions and small forms). No third pane.

**Reaching management.** Three routes, same as triage: (a) a hover/focus `…` menu on project, epic and work item rows in the tree; (b) the board header's actions (`New work item` primary, plus `Settings` on a project board / `Edit epic` on an epic board); (c) a gear in the sidebar header for the organization.

```
┌─ sidebar ──────────────┐ ┌─ main ─────────────────────────────────────────┐
│ dude              ⚙ +  │ │ Customer Portal › ⧉ OAuth migration     [… ] [+ New] │
│ [/ Find work…        ] │ │ 7 work items ◆1 ●2 ■1 · $14.20    Group by epic ○  │
│ ◆2 ●3 ▲1 ■0            │ │ ┌Intake─┐┌Queued┐┌In progress┐┌Review┐┌Closed┐  │
│ NEEDS YOU              │ │ ...                                                │
│ ◆ CP-41 reviewer asks… │ └────────────────────────────────────────────────┘
│ CUSTOMER PORTAL   …    │
│  ⧉ OAuth migration  …  │   row "…" menus:
│    ● CP-41 Add PKCE    │   project: Settings · New epic · New work item · Archive
│      ▣ reviewer        │   epic:    Edit · New work item · Move up/down · Delete
│  ⧉ Q4 perf         …   │   item:    Edit · Move to epic › · Split · Delete
│  ○ CP-52 (no epic)     │
└────────────────────────┘
```

## 2. Management surfaces

### Project settings (`#/project/:id/settings`)
Tabs: **General · Repositories · Agents · Delivery**. Each tab saves on its own `Save` (one primary per view); dirty state shown by enabling the button; success is a toast (it is the outcome of your action).

```
Customer Portal › Settings
[General] [Repositories 2] [Agents] [Delivery]
── Repositories ─────────────────────────────────────────────
 name     url                              branch  trust      
 api      github.com/acme/api              main    internal   [Edit] 
 web      github.com/acme/web              main    internal   [Edit]
                                                    [+ Add repository]
── Agents ───────────────────────────────────────────────────
 role          model                     harness     cost cap   context
 ◉ orchestr.   claude-sonnet-4-5  ▾      opencode ▾  $ 5.00     [edit…]
 ▣ implementer (inherited: org default) ▾ …
 ▣ reviewer    …
── Delivery ─────────────────────────────────────────────────
 Required reviewers   ☑ correctness ☑ security ☐ database ☐ api ☐ frontend ☐ performance
 Blocking severities  ☑ blocking ☑ high ☐ medium ☐ low ☐ note
 Review rounds  [3]   PR fix rounds [2]   ☑ Run the simplifier
 Runtime image  [ghcr.io/acme/runtime:1.4        ]  (empty = system default)
```
Fields/validation: repo `name` `^[a-z0-9][a-z0-9-]*$`, unique in project; `url` non-empty; branch default `main`; trust as a checkbox "External (untrusted): no credentials, egress restricted". Agents: model from a `Select` with groups per harness (needs a model catalog endpoint; free text with `mono` until then), a `Badge tone="neutral"` reading *inherited from organization* when unset, with "Override" turning the row editable. Delivery: `requiredReviewers` min 1, severities min 1, rounds 1–20 / 0–20. Empty state for Repositories: "No repository yet — work items can't open pull requests until one is added." Reuse: `Tabs`, `Table`, `Input`, `Select`, `Checkbox`, `Button`, `Badge`, `AgentAvatar`, `Dialog` (repo add/edit), `useToast`. New: **Textarea** (there is none; `Input` is single-line), a **FormRow** wrapper (label/hint/error for `Select`/`Checkbox` groups the way `Input` already does it), **RowMenu** (Radix DropdownMenu behind an `IconButton icon="more"`; the DS has no menu primitive).

### Epic create / edit / reorder
`Dialog size="sm"`: title (required, ≤200), description (Textarea, Markdown). Reorder via **Move up / Move down** in the row menu and a `position` field on the epic; no drag, consistent with the board rule. Delete is `Dialog tone="danger"` with "Its N work items move to *No epic*"; a non-empty epic is never deleted silently. Epic board header gets `Edit epic` (secondary) and shows the description under the title, clamped to two lines.

### Work item create / edit / move / split
Replace the inline two-input form with a `Dialog size="md"`:
```
New work item                                               ✕
 Kind    (● Code change → pull request) (○ Document → artifact)
 Title   [                                              ]  required
 Epic    [OAuth migration            ▾]   (prefilled from scope; "No epic")
 Repo    [api                        ▾]   (change only; hidden if one repo; required if >1)
 Goal    [                                              ]
         [ textarea, Markdown, "why, and any detail"     ]
 Acceptance criteria
   • [Login works with PKCE                     ] ✕
   • [+ add criterion]
                                       [Cancel] [Create]  [Create and deliver]
```
Validation: title 1–500; goal ≤10k; criteria each ≤2000, blanks dropped; repo required at *deliver* time for kind=change (today a two-repo project 400s on Deliver with no UI to fix it). Edit reuses the same dialog: title/goal/criteria editable until the item is `done`; epic movable always; kind and repo locked once a run exists (hint says why). **Move** is the epic `Select` alone (`RowMenu › Move to epic ›`). **Split** dialog: a list editor of new titles, "close the original as *superseded*" checkbox; children land in the same epic with `splitFromId`. Delete: only for never-delivered items; otherwise the menu offers *Abort* and explains. New DS piece: **ListEditor** (criteria / split rows).

### Work item kinds
`kind: "change" | "document"`. A *change* ends in a PR (today's pipeline). A *document* runs investigate → write → review → publish; the work item screen shows an **Artifacts** section instead of the PR step: a row per artifact (name, media type, size, producer role, age) opening a `Markdown variant="document"` view inline for text, download otherwise. Board cards for documents show a `file` glyph after the key so the kind reads without colour.

### Organization settings (`#/org/settings`)
Sections: **General** (name), **GitHub** (connection card: forge, auth kind, masked secret `ghp_…a1b2`, installation id, API base URL, webhook URL with a copy button, `Verify` secondary that calls GitHub and shows the login and scopes, `Replace token` opening a `Dialog`), **Defaults** (the same Agents and Delivery forms as the project, labelled "projects inherit these"). Empty state for GitHub: "Not connected — agents can implement, but cannot open pull requests." Reuse `Card/CardHeader/CardFooter`, `Input mono`, `Badge`.

## 3. UX pass on what exists (ranked)

1. **Deliver breaks on multi-repo projects** (`api.go` returns 400 "name one with repositoryId"; no UI path). Fix: `repositoryId` on the work item; the button reads `Deliver to api/main`; a `Select` appears when unset.
2. **Empty states say "Create a project through the API, then reload"** (`App.tsx:94`, and the sidebar). Fix: `New project` primary in both, a `Dialog` (name, slug auto-derived and editable, first repo URL optional).
3. **"New work item" is two single-line inputs in the board header** — no epic, no repo, no criteria, goal truncated to one line. Fix: the dialog above; prefill epic from the board scope.
4. **Work item header shows the raw `wi_01H…` id, no epic, requester, spend or age.** Fix: per-project human key (`CP-41`, mono), breadcrumb `Project › Epic`, `HumanAvatar` for the requester, `CostDisplay` of total spend, `Duration format="age"`; the ulid goes to the `title` attribute.
5. **RunScreen's `Back` is a ghost button *below* the transcript** and "Reconnecting…" is loose text under it. Fix: breadcrumb in the `ChatTranscript` header (Project › Epic › `CP-41`), a neutral `Badge` "reconnecting" beside the status; delete the button.
6. **Nothing in the tree or board says projects and epics are things you can act on.** Fix: the `RowMenu` on hover/focus and via `Shift+F10`/context menu; `Settings` on the board header.
7. **Deliver has no summary of what will happen or cost.** Fix: a one-line derived sentence beside the button ("implementer → review: correctness, security → simplify → PR to api/main"), replacing the 30-word empty-state paragraph.
8. **Findings are hand-styled app CSS** (strike-through text, uppercase severity strings, no route to the fix run). Fix: a DS **FindingRow** (severity glyph + label, category, file:line mono, status as a neutral `Badge`, "fixed in › Fix" link), grouped open-first.
9. **The project board can't be read by epic.** Fix: a `Group by epic` toggle in the board header rendering swimlanes (rows = epics in `position` order, then *No epic*) inside the same five lanes; persisted per project. This is also where an operator sees the epic order they set.
10. **Sidebar footer is a bare `Sign out`; no person, no org, no connection state.** Fix: `HumanAvatar` + name + org, a stream-status dot (neutral/attention), `Sign out` inside its `RowMenu`.

## 4. API and data-model decisions

**Endpoints to add** (all under `/v1`, org-scoped by the principal):

```
POST   /projects/:id/repositories        {name,url,defaultBranch?,trust?}
PATCH  /repositories/:id                 {name?,url?,defaultBranch?,trust?}
DELETE /repositories/:id                 → 409 if any PR or work item references it
POST   /projects/:id/archive
POST   /epics                            {projectId,title,description?}
PATCH  /epics/:id                        {title?,description?}
PUT    /projects/:id/epic-order          {epicIds:[…]}
DELETE /epics/:id                        → members get epicId null
PATCH  /work-items/:id                   {title?,goal?,acceptanceCriteria?,epicId?,repositoryId?,kind?}
DELETE /work-items/:id                   → 409 if runs exist
POST   /work-items/:id/split             {items:[{title,goal?}],closeOriginal:boolean}
GET    /work-items/:id/artifacts ; GET /artifacts/:id/content
GET    /organization ; PATCH /organization  {name?,defaultAgentModels?,defaultDeliveryPolicy?,defaultRuntimeImage?}
GET    /forge/credential (masked) ; POST /forge/credential/verify ; DELETE /forge/credential
GET    /organization/members             (for requester avatars and "who is asking")
GET    /models                           (harness → model catalog for the Select)
```
`GET /navigation` gains per work item `key`, `kind`, `repositoryId`; per epic `position`, `description`; `GET /projects/:id` gains `archived`.

**Decisions the owner must take (with my recommendation):**
1. *Epics are ordered groups only* — title, description, position; no policy, no models. Policy has two layers (org, project) plus a per-item override; a third would be invisible at delivery time.
2. *A work item points at one repository (nullable)*. Cross-repo work is split into sibling items in one epic; plan §14 `TaskRepo` stays future.
3. *Add `kind` (`change` | `document`) and `work_item_id` on `artifacts`*; document delivery is a second workflow definition, not a flag on the first.
4. *Human keys*: `<PREFIX>-<n>` per project, prefix derived from the slug and editable in General.
5. *Persist per-item policy overrides* (`policyOverrides` on the work item) instead of only in the deliver body, so the screen can show them.
6. *Org gains `defaultDeliveryPolicy` and `defaultRuntimeImage`* so "inherited" is true at every layer.
7. *Deletion*: hard delete only for never-run work items and empty epics; projects archive, never delete.
8. *Epic status is derived from counts*; no manual open/closed for now.

## 5. Build order

1. **Keys and headers** (nav read model + UI, no schema change): `key`, breadcrumbs, work-item header, remove the Back button, findings as a DS row. Ships problems 4, 5, 8.
2. **Work item dialog + `PATCH /work-items/:id` + repositoryId** — create, edit, move to epic, deliver with a repo. Ships 1, 3.
3. **Epics**: create/edit/delete/order routes, `RowMenu` in the DS, tree menus, Group-by-epic board. Ships 6, 9.
4. **Project settings screen**: Repositories tab + repo routes, then Agents and Delivery on the existing `PATCH`.
5. **New project + empty states + org settings** (GitHub card, `GET/verify` credential, defaults). Ships 2, 10.
6. **Document kind**: schema, second workflow, Artifacts section.
7. **Split, delete, archive.**

Each slice is independently mergeable and leaves no half-built surface visible: a menu appears only when its actions exist.