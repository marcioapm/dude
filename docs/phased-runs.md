# Phased Runs: implementer, reviewer and tester as first-class agents

**Status:** mostly built. This was the design; it is kept as the record of
why the delivery workflow is shaped the way it is. For how the built system
works, see [`CONTRIBUTING.md`](CONTRIBUTING.md#delivery-end-to-end).

| Part | State |
| --- | --- |
| Phases as Runs, git handoff via `base_ref`, parallel review fan-out | built |
| Reviewer executes but does not publish; findings as rows | built |
| Review → fix loop with declared bounds; simplifier | built |
| Per-project, per-role prompt context | built (`agentModels.<role>.context`) |
| PR opened by the workflow; PR feedback wakes a fixer | built |
| Tester phase (browser QA in its own container) | not built — the phase exists, the agent does not |
| Screenshot/video evidence and S3 artifact storage | not built |

Where the build departed from the design below: the schema landed as
migrations `012_phases_and_findings.sql` and `013_pr_feedback.sql` (not 011,
which became pull requests); the workflow lives at
`apps/control-plane/src/workflow/delivery.workflow.ts` rather than a
`definitions/` directory; and a *fix* phase joined the list, since fixing
review findings and fixing PR feedback are both implementer Runs with a
different prompt.

---

## The question

> "How can we make the reviewer and tester be their own agents? The orchestrator
> launches the developer, then the reviewer, then the tester… each should get
> its own sandbox and worktree, in isolation. Currently we rely on subagents
> from OpenCode, which is fine, but the step of each having their own worktree
> is not clear to me how we can make that work."

## The short answer

**The unit that already owns a sandbox is the Run.** Today the runner pins one
container (`runtime.Manager.Create`, keyed on `ContainerName(runID)`) and one
workspace (`workspace.Manager.Create(org, runID, repos)`) per Run. It also owns
a lease, a node affinity (`home_worker_id`, `workspace_portable`), a control
channel, and a place in the event ledger.

So "give each agent its own sandbox" does not need a new concept. It needs the
**phase to be a Run** — sibling Runs under one Work Item — instead of a subagent
inside one Run.

## On "worktree" specifically

Worth knowing: the codebase already rejected git worktrees, deliberately, and
the plan agrees (§61, `workspace.go:196`):

> An isolated checkout rather than a git worktree: worktrees share the parent
> repository's object store and refs, so an agent running destructive git
> commands could damage the cache other Runs depend on.

What we have instead is a node-local bare mirror → `git clone --local` per Run.
Cheap (hardlinked objects, no network) and fully isolated. The plan's own
`RepoMaterializer` interface already anticipates the missing piece:

```ts
materialize(cached, destination, revision)   // ← revision
```

Our `materialize` hardcodes the default branch. **Each phase Run gets its own
clone, checked out at the previous phase's commit.** That is the whole of
"its own worktree", and it is a small change.

---

## Decisions

### Handoff is via git branches

The implementer commits and pushes to `dude/wi-<id>/attempt-<n>`. The next phase
clones that branch from the node-local mirror into a fresh workspace.

Phases stay decoupled — a reviewer can run while the implementer's node is busy —
the handoff is an inspectable commit, and it composes with the affinity work
already in place: a phase Run is portable because its input is a ref, not a
directory. Same-node workspace reuse stays available later as an optimization
behind the same interface.

### The reviewer executes but does not commit

The reviewer gets a full sandbox: it can run the code, run the test suite, write
probe scripts, poke at things. What it cannot do is push to the branch. Its
output is structured findings; a fix is a separate implementer Run with its own
sandbox.

This is the interesting case, and it is why the reviewer needs to be a *Run* and
not a read-only view: a reviewer that can only read diffs cannot tell you whether
the thing actually works. One that can execute can.

Mechanically "cannot commit" is a property of the phase, not a hope: the phase
config decides whether the runner's commit-and-push step runs at all, so the
reviewer's workspace is discarded rather than published. Its scratch work still
reaches you as artifacts.

**Deferred:** reproduction scripts as a first-class review output — a reviewer
attaching `repro.sh` that the implementer can run verbatim. Valuable, but not in
the first implementation.

### Running tests belongs to the implementer

The implementer runs lint, types, unit and integration tests as part of doing its
job. That is not a separate phase; an implementer that hands over red code has
not finished.

### Review runs in parallel, fanned out by the orchestrator

The orchestrator decides which reviewer flavours a change warrants (§11.1 lists
correctness, security, performance, frontend, database, API) and creates that
many review Runs at once. They are independent — each gets its own container and
its own clone of the same ref — so they cost wall-clock once rather than N times.

This is the case that most justifies phases-as-Runs: N parallel reviewers is
natural when a phase is a Run with its own sandbox, and awkward when it is a
subagent sharing one.

Their findings merge into one set before the fix Run starts, so the implementer
sees the whole picture rather than being steered N times.

### The tester is a browser QA agent

The `tester` phase is not "run the test suite" — it is the agent that exercises
the product the way a person would: driving a real browser, clicking through the
feature, reading the console, catching what a unit test cannot. This is the
`qa_browser` role that already exists in the `agent_role` enum, and plan §12.2
specifies its tool surface (`qa.open`, `qa.click`, `qa.screenshot`,
`qa.read_console`, `qa.start_recording`…) over Playwright.

Plan §12.1 orders the layers cheapest-first — formatting, lint, types, unit,
integration, existing E2E, deterministic Playwright, then model-driven
exploration. The implementer covers 1–6; the tester starts at 7.

---

## Evidence: can the implementer prove it works with video?

**Yes, and the plan already specifies how (§12.3).** Playwright records
`video.webm` natively per context; a trace zip gives a frame-by-frame timeline.
Most of the plumbing exists:

- `artifacts` table — `kind`, `content_type`, `storage_key`, `sha256`.
- `artifact.created` event, so evidence appears in the timeline.
- `artifacts-staging/` in every workspace (`workspace.go:32`), already created.
- We already drive Playwright against system Chrome on this machine.

Missing: a browser in the runtime image, and upload from staging to storage.

Evidence bundle per plan §12.3:

```text
qa/
  scenario.json
  screenshots/
  video.webm
  playwright-trace.zip
  console.log
  network-errors.json
  test-report.json
```

**The implementer should prove it works too.** "I wrote the code and the unit
tests pass" is a weaker claim than "here is the feature doing the thing you
asked for", and the second one is what a person actually wants to see on a PR.
An implementer that cannot demonstrate its own work probably has not finished.

Which means the capability is not tester-specific. Running the app and driving a
browser is a **capability a phase either has or does not**, not a phase of its
own:

| phase | runs the app | drives a browser | records |
|---|---|---|---|
| implement | yes | yes, to demonstrate | on UI change |
| review | yes | yes, to check a claim | on request |
| test | yes | yes, to explore | always |

The container work, the app-startup config and the artifact upload are the same
for all three. Only the *prompt* and the *publishing* rights differ — which is
the whole argument for phases being Runs with a policy row rather than three
bespoke code paths.

**What still differs is whose evidence means what.** An implementer choosing its
own scenario and recording the path it knows works is grading its own homework:
excellent as *demonstration*, weak as *verification*. The tester is verification
precisely because it did not write the code. Both are artifacts; they should
carry different `kind` values so the UI can say which is which, and so a policy
can require the second before merge rather than accepting the first.

Video costs storage and wall-clock, so it should be policy-driven rather than
always-on (plan §12.3 suggests `mode: ui_changes_only`).

---

## What changes

### 1. Schema — `migrations/011_phase_runs.sql`

```sql
CREATE TYPE run_phase AS ENUM (
  'investigate', 'implement', 'review', 'simplify', 'test'
);

ALTER TABLE runs
  ADD COLUMN phase         run_phase,
  ADD COLUMN role          agent_role,
  ADD COLUMN parent_run_id text REFERENCES runs(id) ON DELETE SET NULL,
  -- The ref this Run's workspace is materialized at. NULL = repo default.
  ADD COLUMN base_ref      text,
  -- What it produced, for the next phase to build on. NULL for phases that
  -- do not publish (review, test).
  ADD COLUMN head_sha      text,
  ADD COLUMN branch        text;
```

`attempt` keeps its meaning (a retry of the whole Work Item); `phase` is the step
within it, so `UNIQUE (work_item_id, attempt)` becomes
`UNIQUE (work_item_id, attempt, phase)`.

`role` moves onto the Run. Today `runner.ts:244` hardcodes `DEFAULT_RUN_ROLE`
with the comment *"role is policy, and policy belongs to the control plane"* —
this is that policy becoming real.

`agent_role` needs `tester` (or we use the existing `qa_browser`; `qa_browser` is
more precise and already there).

### 2. Phase policy — one table, not scattered conditionals

Each phase declares what it may do:

| phase | role | publishes | browser |
|---|---|---|---|
| investigate | investigator | no | no |
| implement | implementer | **yes** | optional (demo video) |
| review | reviewer | no | no |
| simplify | simplifier | **yes** | no |
| test | qa_browser | no | **yes** |

"Publishes" gates the runner's commit-and-push step. That is what makes "the
reviewer cannot commit" structural rather than a prompt instruction.

### 3. A workflow definition — `apps/control-plane/src/workflow/definitions/`

`apps/control-plane/src/workflow/` has `runtime.ts` and `sweepers.ts` and **no
workflow definitions at all**. The phase sequence lives nowhere today; this is
where it goes:

```
investigate → implement → review → (fix ⟲) → simplify → test → PR
```

Each step creates a phase Run and awaits its terminal event. Review findings feed
a fix Run; the loop is bounded by policy. The durable runtime already has the
signal inbox and retries to express this.

### 4. `materialize` takes a ref — `runner/internal/workspace/workspace.go`

```go
func (m *Manager) materialize(ctx, wsPath string, repo Repository, ref string)
```

`git clone --local` then `git checkout <ref>`, where `ref` is `run.base_ref`. The
"already materialized, reuse it" branch must verify the checkout is at the right
ref rather than assume it.

### 5. The runner reports what it produced

At the end of a publishing phase, commit + push and report `head_sha`/`branch`
through the lease call so the control plane can seed the next phase.
`git.commit_created` already carries `commits` and `baseSha`.

### 6. Artifact storage — S3-compatible from the start

`artifacts-staging/` → object storage → `artifacts` row → `artifact.created`.

S3-compatible rather than local files, because a video per test Run is exactly
the payload a filesystem is worst at: the control plane and the runners are
already separate processes that will be separate machines, so "the artifact is
on the node that made it" stops working the moment there are two nodes.

No new dependency: Bun ships `Bun.S3Client`. MinIO in the dev/test environment
gives the same API as the eventual bucket, so nothing is stubbed.

The `artifacts` table is already shaped for this — `storage_key`, not a URL, with
the comment *"Storage key only; never a credentialed URL (plan §66)"*. Reads go
through a control-plane endpoint that issues a short-lived presigned URL, so a
key in the ledger never grants access on its own.

Uploads come from the runner, which means the runner needs bucket credentials.
The agent container must not: it writes to `artifacts-staging/` on the workspace
mount, and the runner uploads after the turn. That keeps storage credentials out
of reach of the model.

### 7. The tester's environment

The tester builds and serves the branch **inside its own container** and points
a browser at `localhost`. No preview deployment, no shared environment.

This keeps the isolation properties we already have — the container is the
boundary, nothing leaks between Runs, and a tester cannot reach another Run's
app. It also means the test target is exactly the code under review, at the ref
the phase was seeded with.

Consequences:

- The runtime image needs a browser (Playwright + Chromium) and whatever the
  project needs to build. Likely a *tester* image distinct from the implementer
  image, since most Runs should not carry a browser.
- `NetworkMode` is already per-Run (`bridge` / `none`), so the tester can have
  loopback without outbound access.
- The project must declare how to start itself — see open questions.

### 8. UI

`navModel.ts` already models `NavWorkItem → NavRun[] → NavSession[]` and folds
older attempts. Phase Runs slot in as the current attempt's children — the tree
gains `Work Item → [implement ✓, review ●, test ○]` without a new level. The
kanban board becomes the natural overview of that.

---

## What I am *not* proposing

- **Not** removing OpenCode subagents. Within a phase, an implementer spawning
  helpers is fine. The change is that *phase boundaries* — where isolation and
  auditability matter — become Runs.
- **Not** a new isolation mechanism. Container-per-Run and clone-per-Run both
  already exist and are tested.

## Still open

1. **How many review Runs?** §11.1 lists six reviewer flavours (correctness,
   security, performance, frontend, database, API). Parallel Runs are fast but
   multiply cost. Start with one, make it policy?
2. **How does a project declare how to start itself?** The tester needs to know
   the build command, the serve command, the port and a readiness check. That is
   per-project configuration we do not have a home for yet — probably alongside
   `agent_models` and `runtime_image` on `projects`.
3. **Does the tester need seeded data?** A browser agent clicking through a
   feature usually needs an account to log in with. Fixtures are a project
   concern, but the shape of "here is how to get a usable instance" is ours.
