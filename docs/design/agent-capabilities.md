# Agent capabilities

**Status: mockup (2026-10-12).** Asked for by Márcio: "an agent
capabilities configuration, per agent, with organisation defaults
overridable by project, like most other settings." Mockup:
`docs/design/mockups/capabilities/index.html` (opens from disk,
interactive; source in `apps/web/mockups/capabilities`, built with
`MOCKUP=capabilities bunx vite build --config mockups/vite.config.ts` from
`apps/web`). Screens in `docs/design/mockups/capabilities/screens/`.

## The problem

What an agent may do is decided in four places, none of them its own:

- **How far it reaches** is the Network page: the organisation's hosts, a
  project's on top (or alone), the operator's floor. One list for every
  role, so an implementer that needs `registry.npmjs.org` gives it to the
  reviewer too, and a reviewer that should reach nothing cannot be told so.
- **The one exception** is #110's organisation switch, "Brainstorm sessions
  may reach any host". It is a capability of one role, on a page about
  hosts, with no project layer.
- **Whether it may start containers** is a property of its image (Images,
  "can run containers"). The image is per role and layered, but there is no
  way to say "this image can, and this role still may not".
- **Which of dude's tools it has** is hard-coded per role in
  `orchestrator/internal/agenttools` (`tools.go`, `brainstorm.go`).
  Nobody can say "our implementers do not write to shared memory" or "on
  billing, the investigator does not file tasks".
- **Web search** is whatever the harness does: Claude Code's WebSearch
  always (it runs at the provider), OpenCode's only when the Run reaches
  any host (`OPENCODE_ENABLE_EXA`), Codex's soon.

And no page says, for one agent, what it can do. People learn it from a
refusal.

## The model

### Capabilities

Every role's settings page gets **Capabilities**, under Harness / Model /
Time limit / Machine / Image, in three groups:

1. **Reach**
   - **Internet** — three levels, narrowest first:
     - **dude only**: the model's host and dude's tools. Nothing else.
     - **Your lists** (the default): today's Network lists, the
       organisation's and the project's.
     - **Any host**: unrestricted. The lists are kept but not applied.
   - **Web search** — on / off: the harness's own search tool. The hint
     says how the role's harness does it in a sentence, and the other two
     in a few words:
     - Claude Code: WebSearch runs at the model's provider, through dude's
       proxy; no host needed. WebFetch (opening a result) needs Internet
       beyond dude only.
     - OpenCode: websearch goes through Exa; dude adds `mcp.exa.ai` to the
       Run's hosts. Not possible with dude only: the switch is then
       disabled and says why.
     - Codex: web_search runs at OpenAI, through the proxy; no host needed.
   - **Containers** — on / off. Only when the role's image can run
     containers; otherwise the switch is off and disabled, with the image's
     name and a link to Images, as `RoleImageField`'s note does today.
2. **Acts in dude** — the tools that act outside the Run, each a switch
   with one line on what it does. **Only the ones the role has** are shown;
   a muted line under them names the rest ("Not the reviewer's to do: ask
   people, create tasks, open and reply on pull requests").

   | Capability | agenttools tools | Roles that have it |
   |---|---|---|
   | Ask people | `ask_person` | conductor, brainstorm, investigator, implementer, fixer |
   | Remember | `remember` (project memory; a brainstorm's is the session's) | all |
   | Create tasks / Propose work | `create_task`; `propose` for the brainstorm | conductor, investigator, implementer, fixer, brainstorm |
   | Request repositories | `request_repository` (write: implementer and fixer) | every task role |
   | Open and reply on pull requests | `decide` → `open_pull_request`, `reply_on_pull_request` | conductor |

   The fixer runs as the implementer in agenttools (`RoleForPhase[fix] =
   implementer`), so it has the implementer's tools; the matrix shows it
   with *Create tasks* off by default, the one place Acme's mock data
   narrows a role.
   Reading tools (`list_*`, `search_memory`, `get_memory`, `findings`,
   `pull_requests`, `run_diff`) and `emit_event`, `name_session` are not
   capabilities: they change nothing outside the Run.
3. **Fixed by role** — shown, never editable, so the page tells the whole
   story: "Pushes its branch" (implementer, fixer, simplifier), "Reads code
   only" (investigator, reviewer, tester), the conductor's "Publishes small
   changes", "Steers, starts and restarts the task's other agents", "Takes
   the delivery's decisions while the owner lets it"; the brainstorm's
   "never changes code" and "files nothing itself". Drawn with the
   existing `Capabilities` list (check / cross and words), in a
   `SettingRow` labelled "Code".

### Layering

Exactly like Harness, Model and Machine: each capability is a
`Setting<T>` — the organisation's value, a project's override, and on a
project `SettingSource` ("From Acme", or "Overridden · Acme: off · Reset").
The settings menu marks a role "changed" when a project overrides any of its
capabilities, and **"beyond Acme"** when one of them is wider than Acme's.

### Widening (decided)

**A project may go beyond its organisation**, narrower or wider, like every
other setting (Márcio, 2026-10-12). There is no ceiling. Instead, widening
is made visible wherever it is:

- **On the page.** A project's value wider than the organisation's (Any
  host over Your lists; Web search, Containers or an act on where Acme's
  is off) shows the `SettingSource` "Overridden" line, a **Beyond Acme**
  mark beside it (the `arrow-up` glyph and the words, in the danger tone's
  badge), and **the same danger `Callout`** the wide value shows on the
  organisation. Nothing about a project's wide value is quieter than
  Acme's own.
- **In the matrix.** A project cell that goes beyond Acme carries the same
  **Beyond Acme** mark under its Overridden line; the page's note counts
  them ("3 values are control-plane's own; 1 goes beyond Acme's").
- **In the menu.** The role's note reads "beyond Acme" instead of "changed".
- **On Network.** Acme's page names the projects whose agents go beyond its
  lists ("On projects, beyond Acme's: billing's Investigator").

"Wider" is defined per capability: Internet by its order (dude only <
Your lists < Any host); a switch when the project's is on and Acme's off.

### Dangerous values

Two values carry the danger `Callout`, in plain words, under their control:

- **Internet = Any host** — #110's wording, with what this role holds:
  "**Anything a page says can steer the agent.** The implementer has a
  writable checkout of the project's repositories, the token lux pushes its
  branch with, and the model's key, in its container. A malicious page
  could talk it into sending them to any host."
- **Web search on, with Internet short of Any host** — "**What a search
  returns can steer the agent.** Results reach the implementer as text it
  reads, and it has …. Its queries — words from your code and the task — go
  to the model's provider" (Exa for OpenCode, OpenAI for Codex). With Any
  host the first callout already says it, so only one shows.

The Any host level carries the warning glyph in the level choice too, named
"Widens what the agent can reach" for a screen reader.

## Screens

| # | Screen | Files |
|---|---|---|
| 1 | Acme → Agents → Brainstorm: Internet = Any host, the danger Callout, Web search on, Containers off (image can't), Ask / Remember / Propose work, fixed lines | `1-org-brainstorm-{dark,light}.png`, `1-org-brainstorm-dark-compact.png` |
| 2 | Acme → Agents → Implementer: the defaults; Containers disabled because `acme-base` can't, with the link to Images | `2-org-implementer-{dark,light}.png` |
| 3 | control-plane → Agents → Implementer: Web search overridden on — Overridden · Acme: off · Reset, **Beyond Acme**, and the web search danger Callout | `3-project-implementer-{dark,light}.png` |
| 4 | Agents → Capabilities: Acme's matrix; control-plane's (Implementer's web search beyond Acme, Reviewer narrowed to dude only, Investigator's acts narrowed); billing's (Investigator on Any host, beyond Acme; Brainstorm narrowed to the lists) | `4-org-matrix-*.png`, `4-project-matrix-*.png` (+ `-dark-compact`), `4-billing-matrix-*.png` |
| 5 | Acme → Network with the note on who goes beyond the lists | `5-org-network-{dark,light}.png` |
| 6 | A brainstorm session: header chip and the rail's "It can"; a Run's header chip, and its tooltip | `6-session-*.png`, `6-run-*.png`, `6-run-tip-*.png` |
| 7 | Screen 1 at 390px | `7-phone-brainstorm-{dark,light}.png`, `7-phone-brainstorm-risk-{dark,light}.png` |

## Where members see it

- **In a session**, the header's meta gets a `CapabilityChip` after the
  model: "Any host · Web search · No containers". Its tooltip is the full
  list (`Capabilities`, check / cross) and when it was fixed ("Acme's
  Brainstorm capabilities when this agent started; a change applies from
  the next agent"). The rail's "It can" block, which already exists, lists
  the same lines under its two fixed ones, then "Acme's Brainstorm
  settings, when this agent started."
- **On a Run's page**, the same chip in `SessionHeader`'s subtitle, after
  the model, machine and image chips, where `CanRunContainersBadge` sits
  today (the chip's "Containers" replaces that badge). Tooltip: "What this
  Run was started with: control-plane's Implementer capabilities. Resuming
  keeps them."
- **What it shows is what the Run recorded when it was submitted**, as
  machine, image and can-run-containers are today: never a guess from
  today's settings.
- **The agent is told too.** dude's part of the prompt gets one short
  paragraph per Run, from the same record: "You may reach: Acme's hosts and
  the project's. You have web search. You cannot start containers. You
  cannot ask people or create tasks in this project." An agent that knows
  `ask_person` is off ends its work with a note instead of trying; one that
  knows it is on dude only does not retry `npm install` five times. No
  mockup: it is prompt text, written next to `delivery/prompts.go`'s
  role leads.

## Data and API

Stored as role settings already are: field by field in the role's object
in `organizations.default_agent_models` and `projects.agent_models` (JSON,
`{role → config}`). A new key, `capabilities`, each field optional:

```jsonc
// organizations.default_agent_models.implementer.capabilities
{ "internet": "lists", "webSearch": false, "containers": false,
  "acts": { "ask": true, "remember": true, "create": true, "repos": true } }
// projects.agent_models.implementer.capabilities — only what it overrides
{ "webSearch": true }
```

Domain (`packages/domain/src/settings.ts`):

```ts
export const INTERNET_LEVELS = ["dude", "lists", "any"] as const;
export type InternetLevel = (typeof INTERNET_LEVELS)[number];
export const CAPABILITY_ACTS = ["ask", "remember", "create", "repos", "pulls"] as const;
export type CapabilityAct = (typeof CAPABILITY_ACTS)[number];

/** On a project, `organization` is what a Reset goes back to and `wider` whether this goes beyond it. */
type Layered<T> = Setting<T> & { organization?: T; wider?: boolean };

export interface RoleCapabilities {
  internet: Layered<InternetLevel>;
  webSearch: Layered<boolean>;
  /** False and `possible: false` when the role's image can't run containers. */
  containers: Layered<boolean> & { possible: boolean };
  /** Only the acts the role has (ROLE_ACTS). */
  acts: Partial<Record<CapabilityAct, Layered<boolean>>>;
}

export interface RoleSettings {
  // …harness, tier, timeLimitMinutes, machineSize, image, enabled, prompt
  capabilities: RoleCapabilities;
}

// settingsPatchSchema, roles.<role>:
capabilities: z.object({
  internet: nullable(z.enum(INTERNET_LEVELS)),
  webSearch: nullable(z.boolean()),
  containers: nullable(z.boolean()),
  acts: z.record(z.enum(CAPABILITY_ACTS), nullable(z.boolean())).optional(),
}).strict().optional(),
```

`null` clears a value, as everywhere: an organisation's goes back to dude's
default, a project's back to its organisation's. An act the role does not
have is refused (422, "the reviewer has no pull request tools"), not
ignored. `ROLE_ACTS: Record<SettingsRole, CapabilityAct[]>` lives in the
domain and is generated from, or tested against, agenttools' table so the
two cannot drift.

`NetworkSettings.brainstorm` goes; the Network page reads which roles go
beyond its lists from `roles.<role>.capabilities.internet` (and, for an
organisation, a per-project summary: `network.beyond: { project, role }[]`).

dude's defaults (no layer set): Internet *Your lists*, Web search off,
Containers on when the image can, every act the role has on — which is
today's behaviour for every role but the brainstorm (see migration).

The Run records what it was given (`runs.capabilities jsonb`), beside the
machine and image it records today; the Run page, the session, the prompt
and a resume all read that record.

## How each is enforced

| Capability | Where | How |
|---|---|---|
| Internet | `phases/spec.go` `runNetwork` | `dude`: `egress(c, nil)` with `toolsOnly`, so the list is the model's host and the tools only (the operator's floor still applies: it is a floor of what is *allowed* to be listed, and stays). `lists`: today's `egress(c, RunEgress(org, project, mode))`. `any`: `lux.Network{Unrestricted: true}` — today's `in.Anywhere`, now set from the role's capability for every role, not from `organizations.brainstorm_anywhere`. |
| Web search | harness flags in `spec.go` | Claude Code: WebSearch off with `--disallowedTools WebSearch` (WebFetch stays: it is bound by egress anyway). OpenCode: `OPENCODE_ENABLE_EXA=1` and `mcp.exa.ai` added to the Run's egress when on (today only with Any host). Codex: its `web_search` config off / on, once it lands. |
| Containers | `spec.go`, lux spec | Today a Run may start containers when its image can. It becomes `image.canRunContainers && capabilities.containers`; lux is asked for nested containers only then. The settings refuse `containers: true` where the image can't (the switch is disabled; the API answers 422). |
| Acts in dude | `agenttools` `tool.allowed(role)` | `allowed` takes the Run's recorded capabilities as well as its role: a tool whose act is off is not listed to the agent (MCP `tools/list`) and is refused if called (`404` as an unknown tool is today, with a sentence: "Ask people is off for implementers on control-plane"). |
| Fixed by role | `agenttools`, lux push | Unchanged: who pushes is decided by role (`ReadOnly` checkouts, `publish` for the conductor). Shown, not stored. |

## Migration from #110

One data migration: for every organisation with `brainstorm_anywhere =
true`, set `default_agent_models.brainstorm.capabilities.internet = "any"`
and `webSearch = true` (today an OpenCode brainstorm gets websearch exactly
when it may reach any host, and Claude Code's always); then drop the
column. `session.go`'s query reads the role's capability instead.
`network.brainstorm` leaves `SettingsPatch`; a client still sending it gets
the usual "removed" refusal pointing to `roles.brainstorm.capabilities`.

Note the behaviour change it implies: today every **Claude Code** role has
WebSearch whatever the settings say, because it runs at the provider. With
Web search off by default, that stops. Either the migration sets
`webSearch: true` for every role whose organisation runs it on Claude Code
(keeps behaviour, then people narrow), or it does not (narrows on deploy,
with a line in the release notes). The mockup assumes the second for Acme;
see Open questions.

## Design system changes

New pieces are built in `apps/web/mockups/capabilities/components.tsx` at DS
quality: tokens only, both themes and densities, accessible names. Nothing
in `packages/design-system` is changed in this branch.

### Reused as they are

`SettingsLayout`, `SettingsHeader`, `SettingsSection`, `SettingRow`,
`SettingFields` / `SettingField`, `SettingSource`, `SettingsMeta`,
`SettingsNote`, `TextButton`, `Switch`, `Callout`, `Select`, `ImagePicker`,
`Table` (`THead`, `TBody`, `Tr`, `Th`, `Td` with `wrap`, `fit`,
`hideWhenNarrow`), `Badge`, `AgentAvatar`, `Capabilities` (the brainstorm
rail's check / cross list, now also the fixed-by-role row and the chip's
tooltip), `SessionHeader`, `SessionRail`, `SessionRailBlock`,
`ScreenHeader`, `SessionTitle`, `ChatTranscript`, `ChatMessage`,
`ChatComposer`, `TierChip`, `MachineChip`, `HostChips`, `HostPresets`,
`Sidebar`, `Segmented` (the mock bar only).

### LevelChoice — proposed as `ChoiceList layout="row"`

Props (on top of `ChoiceList`'s `options`, `value`, `onChange`, `label`):

- `layout?: "column" | "row"` — `row` lays the options side by side, equal
  widths, and stacks them when its own frame is under 560px (a container
  query, as `Table`'s narrow columns).
- on an option: `tag?: ReactNode` — a muted word at the label's end ("Acme's",
  the value a project would inherit); `caution?: string` — the warning glyph
  in danger ink before the label, the string its accessible name.
- `disabled?: boolean` for the whole group.

README, under *Management*, after ChoiceList:

> **A level is a `ChoiceList layout="row"`**: two to four ordered options,
> narrowest first, side by side, each with its sentence; they stack under
> 560px. One that widens what an agent may do takes `caution`, the warning
> glyph before its label, named for a screen reader; its consequences are a
> `Callout tone="danger"` under the choice while it is chosen, never the
> glyph alone. On a project, `tag` marks the organisation's level. For two
> views of one thing, `Segmented`; a level is a setting, not a view.

Gallery: *Primitives → ChoiceList*: a "row" case with Internet's three
levels, one chosen, one with `caution`, one with a `tag`, one with
`disabledReason`; in a 400px frame to show the stack.

Why not `Segmented`: its options are a word each, and the three levels
differ by a sentence that has to be read before choosing. Why not a
`Select` with descriptions: the dangerous level would be hidden until
opened, and the one setting people must see whole would be one line.

### BeyondMark (new)

Props: `from: string`. A `Badge size="sm" tone="danger" icon="arrow-up"`
reading "Beyond Acme", `data-beyond`. It is a component, not a recipe, so
the words and glyph are the same on the page, in the matrix and anywhere
else.

README, under *Management*, after the `SettingSource` paragraph:

> **A project's value wider than its organisation's says so**: after its
> `SettingSource` "Overridden" line, `BeyondMark` — the arrow-up glyph and
> "Beyond Acme" on the danger tint — and under the control the same danger
> `Callout` the value shows on the organisation. Glyph and words, never the
> tone alone; a narrower override is plain "Overridden".

Gallery: *Settings → SettingSource*, a third case beside "From Acme" and
"Overridden": Overridden + BeyondMark.

### CapabilityCell (new)

Props: `value: ReactNode`, `icon: IconName`, `off?: boolean` (muted, for
nothing granted), `onOpen: () => void`, `label: string` (role, capability
and value, for the button's name), `source?: { source, from, inherited?,
onReset?, wider? }` (a project's; omitted on the organisation).

The value is a link-styled button that opens the role's page. On a project,
an overridden cell is strong ink with `SettingSource` under it — without
its "Acme: …" words, which go in the value's accessible name, so a column
stays narrow — and `BeyondMark` when wider. An inherited cell draws no
source (a column of "From Acme" is noise) and says it in its name.

README, under *Management*, a new bullet:

> **A matrix of settings is a `Table` of `CapabilityCell`s**: rows are what
> is configured (each role with its `AgentAvatar`), columns the settings;
> each value is a link to where it is changed. On a project only what
> differs is marked — strong ink, "Overridden · Reset", and `BeyondMark`
> when wider — so the eye finds the project's own values. The cells' `Td`s
> take `wrap`; the label column `fit`; columns that explain rather than
> identify take `hideWhenNarrow`.

Gallery: *Settings → Matrix*, the organisation's and a project's matrix
side by side, with one wider and one narrower override.

### CapabilityChip — proposed as `MachineChip icon="globe"` with `parts`

Props: `parts: string[]`, `tooltip?: ReactNode`, `data-testid`. MachineChip's
grammar (raised shade, chip height, `xs` text, a button only so the tooltip
opens on focus, brighter on hover and while open), with the globe glyph and
the parts joined by " · ", cut with an ellipsis. Its name is "It may: Any
host, Web search, No containers".

In the DS it is one more `icon` of `MachineChip` taking `parts` in place of
`name` and `spec`, not a new component.

README, under *Machines*, after MachineChip:

> **What an agent may do is a `MachineChip icon="globe"`** in a Run's
> header and a session's meta: Internet, Web search and Containers in
> words; its tooltip the whole list as `Capabilities` and when it was
> fixed. It replaces `CanRunContainersBadge` in a Run's header. It shows
> what the Run recorded, never today's settings.

Gallery: *Machines → MachineChip*, a globe case with and without its tooltip.

### SettingRow stacks on a phone (changed)

Today a `SettingRow` keeps its 180px label column at any width, so at 390px
its control column is a few pixels wide (the existing Network page has the
same bug). The mockup stacks it under 720px — where `SettingsLayout`
already puts its menu above the page — with a class on the row; in the DS
it is `Settings.module.css`'s `.row` media query.

README, *Management*, the settings paragraph gains: "Under 720px a
`SettingRow`'s label sits over its control."

Gallery: the settings page's existing phone frame.

## Open questions

1. **Claude Code's WebSearch today is on for every role.** Keep it on by
   migration (no change on deploy, people narrow later), or default Web
   search off and narrow on deploy? The mockup shows the second.
2. **Is the brainstorm's Internet still organisation-only in effect?** A
   session spans projects and runs under the organisation's settings, so a
   project's Brainstorm override applies only if the session links that
   project alone — or never. Proposal: never; the project's Brainstorm page
   shows Capabilities read-only, "Sessions follow Acme's". The mockup's
   billing override on Brainstorm shows the matrix behaviour only.
3. **Who may widen on a project?** Every other project setting is changed
   by anyone who may edit the project. Widening now needs only that too; an
   audit event (`settings.capability_widened`) and a notification to the
   organisation's admins may be enough, or it may want an admin.
4. **Should "dude only" also drop the operator's floor?** The proposal keeps
   the floor (the operator decides what is ever allowed); a role on dude
   only then still reaches what the operator lists. The level's description
   would need to say so on a dude whose operator lists hosts.
5. **Per-host lists per role** (the implementer gets `registry.npmjs.org`,
   the reviewer does not) are not in this proposal: the three levels cover
   the asks so far. If wanted, it would be a fourth level, "Its own list".
