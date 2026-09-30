# Machine sizes

Every agent session and every branch preview runs on a named machine
size. This is how sizes are kept, chosen, sent to lux and shown.

## The model

- **Sizes are the organisation's.** Each is `{id, name, cpus, memoryMiB,
  diskGiB, pool, isDefault}` (`machine_sizes`, migration 063). Everyone
  in the organisation reads them; only its admins change them.
- **Steps.** CPUs in 0.5 steps, memory in 0.5 GiB steps (stored in MiB,
  a multiple of 512), disk in 5 GiB steps; the least of each is one step.
  `machineSizeInputSchema` refuses an off-step value naming the step, and
  the table's CHECK constraints refuse the same, so an illegal size cannot
  exist whichever way it is written. A name is unique in its organisation
  whatever its case, at most 40 characters.
- **One default.** Exactly one size per organisation is the default: a
  deferrable exclusion constraint (checked at the end of a statement, so
  the API moves the default in one UPDATE), and the API refuses to remove
  the default (409, "make another the default first").
- **Seeded.** Every organisation starts with **Standard, 2 CPUs · 8 GiB ·
  20 GiB, default pool, default** — lux's own built-in default, so
  deploying this changes what nothing runs on. Organisations there before
  the migration get it from the migration, new ones from a trigger.
- **The pool.** `pool` names a lux pool; null is the organisation's
  default pool in lux, and then dude sends no `placement.pool`.

## Who runs on what

- **Every agent role names a size**: `machineSize` in a role's settings,
  on the same two layers as its model, effort and time limit —
  `organizations.default_agent_models[role]` and
  `projects.agent_models[role]` — resolved a field at a time. The fixer
  follows the implementer where it names none (`modelFallback`). Unset at
  every layer, or naming a size that is gone, is the organisation's
  default. The settings response carries it as a `Setting` with its source;
  on a project also what the organisation's layer names (what Reset goes
  back to), and for the fixer whether its value is the implementer's.
- **The investigator** is a configurable role like the others (model,
  effort, time limit, machine) and always on. Before, it had no settings
  page; the phase already resolved its model through the same layers, so
  nothing changes for an organisation that sets nothing.
- **Branch previews** name a size in `projects.preview_settings.machineSize`
  (unset: the default). Servers someone starts from a session run inside
  that agent's Run and need nothing.
- **Editing a size** applies to sessions that start after it. **Removing a
  size in use** takes a replacement — another size, or none (follow the
  default) — and in one transaction rewrites every organisation and
  project `agent_models[*].machineSize` and `preview_settings.machineSize`
  that named it, then deletes it. A settings change naming a size holds
  that row `FOR SHARE`; the removal locks it `FOR UPDATE`, so the two are
  ordered and nothing is left naming a size that is gone.

## What lux is sent, and what a Run keeps

- The orchestrator puts the size on the RunSpec as `resources: {cpus,
  memory, disk}` — memory and disk in bytes, which lux takes as well as
  its size strings — and `placement: {pool}` when the size names one, for
  phase Runs (`phases.buildSpec`) and previews (`servers.Previews.spec`)
  alike.
- **Each Run snapshots what it ran on** when its spec is built:
  `runs.machine = {sizeId, name, cpus, memoryMiB, diskGiB, pool, from}`,
  never rewritten once lux has the Run. History stays true after the size
  is edited or removed. The session header shows it as a chip (the size's
  name and spec) with a tooltip saying where it came from and that it is
  fixed for the session.

## Pools, from lux

- `lux.Client.Pools` reads `GET /v1/pools` with dude's key (its `run`
  scope includes `read`). A newer lux adds to each pool `hostSize: {cpus,
  memory, disk} | null`, `hostSizeFrom: "running" | "history"`,
  `instanceType`, `isDefault` and `hostsRunning`; all are optional, and
  dude works against a lux that sends none of them.
- The orchestrator serves them as `GET /internal/lux/pools`; the backend
  passes them to the web as `GET /v1/machines/pools`, readable by members.
  lux out of reach is an empty list with a `problem`; sizes still work.
- **The fit check** runs on save and live in the dialog
  (`machineFit`): a size must fit one host of its pool — CPUs and memory
  no more than the host's, and disk too where the host reserves any. Too
  big for a host lux knows is refused (422, naming what does not fit). A
  host nobody knows (an older lux, a pool that never had a host, lux
  unreachable) is allowed, with a note. Pool null is the pool lux marks
  `isDefault`, else unknown.

## The memory share

A size is in the machine's own terms: a 32 GiB machine takes runs asking
for 32 GiB in total. Linux and the host keep a little of every machine,
so lux gives each run on a host the same share of what it asked for —
`requested × (MemTotal − headroom) ÷ gross` — and no run pays more than
another. dude never shows the factor per pool; the Machines page explains
it once (a `ProportionBar`: the part Linux and the host keep hatched, then
the runs), the size dialog says "a session gets a little less than N
GiB", and the session's tooltip says what the run actually got when lux
reports the placement's `memoryLimit` ("asked for 48 GiB, got 45.6").
That field is from a newer lux and optional.

## Screens

- **Organisation › Machines**: the note (only admins change these; a
  change reaches sessions that start after it), the Sizes table (name and
  Default badge, CPUs, memory, disk, pool, how much of a host it takes,
  who uses it, a row menu), Add size, lux's pools, and the explainer. A
  member sees it read-only.
- **Add / Edit size**: name, CPUs / memory / disk as `NumberInput`s, the
  pool from lux's list with each host's size, the fit as a callout, "Make
  it the default". Edit says who uses the size.
- **Remove**: who uses the size (org roles, project overrides, previews;
  a fixer that inherits "follows the implementer"), "Move them to", and a
  danger "Remove and move them".
- **Agents › each role**: Machine after Time limit. **Project › Servers**:
  Branch previews › Machine. **Session header**: the machine chip.

The design system pieces (NumberInput, a Select option's `meta`,
FitBar, ProportionBar, MachineChip, SettingsExplainer, UsedBy) and the
Machines page pattern are in the design system's README, under
*Machines*.
