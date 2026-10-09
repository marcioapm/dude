# Machine sizes

Every agent session and every branch preview runs on a named machine
size. This is how sizes are kept, chosen, sent to lux and shown.

## The model

- **Sizes are the organisation's.** Each is `{id, name, cpus, memoryMiB,
  diskGiB, poolId, isDefault}` (`machine_sizes`, migration 064). Everyone
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
- **The pool, by lux's id.** `poolId` (`pool_id`, CHECK
  `^pool_[A-Za-z0-9_-]+$`) is the immutable `id` lux gives a pool in `GET
  /v1/pools`; null is the organisation's default pool in lux, and then
  dude sends no placement. A pool's name is never stored: lux can rename a
  pool, and its id stays. Every name shown comes from lux's live list — the
  API adds `poolName` to each size from it — so a rename shows up by itself.
- **Saving checks the pool with lux** (create and update). A `poolId` lux
  does not list for the organisation is refused, 422 `unknown_pool`, "lux
  has no pool with id … for this organisation", and nothing is saved. If
  lux cannot be read, a size naming a pool is refused too, 503, "can't
  check the pool with lux: <problem>". A size with no pool always saves.

## Who runs on what

- **Every agent role names a size**: `machineSize` in a role's settings,
  on the same two layers as its model tier and time limit —
  `organizations.default_agent_models[role]` and
  `projects.agent_models[role]` — resolved a field at a time. The fixer
  follows the implementer where it names none (`modelFallback`). Unset at
  every layer, or naming a size that is gone, is the organisation's
  default. The settings response carries it as a `Setting` with its source;
  on a project also what the organisation's layer names (what Reset goes
  back to), and for the fixer whether its value is the implementer's.
- **The investigator** is a configurable role like the others (model tier,
  time limit, machine) and always on. Before, it had no settings
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
  its size strings — and `placement: {poolId}` when the size names a
  pool, never `placement.pool`, for phase Runs (`phases.buildSpec`) and
  previews (`servers.Previews.spec`) alike.
- **A deleted pool fails the Run.** lux resolves `placement.poolId` at
  submit and refuses an id it has no pool for with 422 `unknown_pool`. A
  4xx from a submit already failed a phase Run or a preview without a
  retry (`lux.Error.Retryable`); for `unknown_pool` the reason is in words
  (`phases.PoolGone`): "Its machine size, Large, runs in a lux pool that no
  longer exists. Give Large another pool in Machines." Nothing waits for a
  pool that will not come back.
- **Each Run snapshots what it ran on** in the statement that records its
  lux Run id at submit:
  `runs.machine = {sizeId, name, cpus, memoryMiB, diskGiB, poolId, pool,
  from}` — `pool` is the pool's name in lux at the submit, read from its
  list (null if lux did not answer) —
  never rewritten once lux has the Run (a resume keeps it). History stays
  true after the size is edited or removed, or the pool renamed. The
  session header shows it as a chip (the size's
  name and spec) with a tooltip saying where it came from and that it is
  fixed for the session.

## Pools, from lux

- `lux.Client.Pools` reads `GET /v1/pools` with dude's key (its `run`
  scope includes `read`). Each pool has an `id` (`pool_…`), unchanged by a
  rename. A newer lux adds to each pool `hostSize: {cpus,
  memory, disk} | null`, `hostSizeFrom: "running" | "history"`,
  `instanceType`, `isDefault` and `hostsRunning`; all are optional, and
  dude works against a lux that sends none of them.
- The orchestrator serves them as `GET /internal/lux/pools`; the backend
  passes them to the web as `GET /v1/machines/pools`, readable by members.
  lux out of reach is an empty list with a `problem`; sizes in the default
  pool still save, sizes naming a pool do not.
- **The fit check** runs on save and live in the dialog
  (`machineFit`, matching the pool by id): a size must fit one host of its
  pool — CPUs and memory no more than the host's, and disk too where the
  host reserves any. Too big for a host lux knows is refused (422, naming
  what does not fit). A host nobody knows (an older lux, a pool that never
  had a host, lux unreachable for the default pool) is allowed, with a
  note. Pool null is the pool lux marks `isDefault`, else unknown. A
  `poolId` missing from lux's list is `gone`.

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
  Default badge, CPUs, memory, disk, pool by lux's current name, how much
  of a host it takes, who uses it, a row menu), Add size, lux's pools, and
  the explainer. A member sees it read-only. A size whose pool id lux no
  longer lists shows a danger "Pool gone from lux" badge in the Pool
  column and Fits "—".
- **Add / Edit size**: name, CPUs / memory / disk as `NumberInput`s, the
  pool from lux's list with each host's size (the option's value is the
  pool's id, its label lux's current name), the fit as a callout, "Make it
  the default". Edit says who uses the size. For a size whose pool is gone
  it says so in a danger callout, and Save stays disabled until another
  pool, or the default pool, is chosen.
- **Remove**: who uses the size (org roles, project overrides, previews;
  a fixer that inherits "follows the implementer"), "Move them to", and a
  danger "Remove and move them".
- **Agents › each role**: Machine after Time limit. **Project › Servers**:
  Branch previews › Machine. **Session header**: the machine chip.

The design system pieces (NumberInput, a Select option's `meta`,
FitBar, ProportionBar, MachineChip, SettingsExplainer, UsedBy) and the
Machines page pattern are in the design system's README, under
*Machines*.
