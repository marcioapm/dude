# Working on dude

This is the guide for picking the project up: how the pieces fit, which
decisions are settled, where the traps are, and what to build next. The
[README](../README.md) covers running it; the
[plan](../software-factory-plan-v0.9.md) is the long-form design this
implements, and section numbers below (§21, §81…) refer to it.

## The shape of the system

```
            ┌──────────────────── control plane (Bun) ────────────────────┐
  web UI ──▶│ HTTP API ─▶ Postgres (RLS)  ◀── sweepers (background loops)  │◀── GitHub
  (React)   │    │           │   ▲                 │                      │   (polled)
            │    ▼           ▼   │                 ▼                      │
            │  event ledger ──▶ SSE          workflow runtime             │
            └──────────────────────────────────────┬──────────────────────┘
                                                   │ leases, push credentials,
                                                   │ events, findings (HTTP)
                                         ┌─────────▼─────────┐
                                         │  runner (Go)      │  one per node
                                         │  Docker-per-Run   │
                                         └─────────┬─────────┘
                                                   │
                                     container: workspace clone + agent (OpenCode)
```

**The control plane** (`apps/control-plane`) owns all state and every
decision. It is one Bun process: the HTTP API, plus background *sweepers*
started in `src/index.ts`:

| Sweeper | What it does |
| --- | --- |
| `workflow-poller` | Advances durable workflows that are runnable or were signalled. |
| `phase-notifier` | Turns a finished phase Run into a `phase.finished` signal for its workflow. |
| `pr-poller` | Syncs open pull requests from the forge and signals the workflow when something is actionable. |
| `outbox-dispatcher` | Delivers side effects queued in the transactional outbox. |
| `run-lease-reaper`, `worker-liveness-reaper` | Reclaim work from runners that stopped reporting. |

**The runner** (`runner/`, Go) claims Runs over HTTP, materializes a
workspace (a clone from a node-local bare mirror, at the Run's base ref),
starts a hardened container, runs the agent inside it, then inspects the repo,
pushes the branch if the phase publishes, and reports events and findings. It
never decides anything; the control plane tells it what the Run is.

**The web app** (`apps/web`) talks only to the public API. It reads
`/v1/navigation` for the sidebar and board, streams `/v1/events/stream` for
live updates, and re-reads rather than polling.

**The design system** (`packages/design-system`) is product-agnostic and has
its own [README](../packages/design-system/README.md) with its rules. Browse it
with `bun run gallery` in that package.

### Hierarchy

`Organization → Project → Epic → Work Item → Run → Session` (§39). A **Run**
is one execution in one container. With phased delivery a single *attempt* at
a work item is several Runs — one per phase — and each is shown as an agent in
the UI.

### Delivery, end to end

`apps/control-plane/src/workflow/delivery.workflow.ts` is the state machine;
`delivery.ts` holds its side effects; `policy.ts` holds every bound.

```
implement → review (fan-out) ⟲ fix → simplify → [test] → open PR → wait ⟲ fix → done
```

- Each phase is a Run with `phase`, `role`, `base_ref` (the commit it starts
  from) and, for reviews, `category`. The claim route
  (`api/routes/runner.ts`) turns those into the agent's role, model and
  prompt (`api/prompts.ts`).
- **Handoff is via git.** A phase's commits reach the next phase only by being
  pushed; the next phase clones the mirror at `base_ref`.
- **Publishing is a property of the phase** (`PHASE_PUBLISHES`), not a prompt
  instruction: the runner's push step simply does not run for a review.
- **Reviewers report findings** as YAML in their output; the runner parses it
  (`runner/cmd/factory-runner/findings.go`) and posts it. Only review and test
  Runs may report.
- **Every loop ends on a declared bound**: `maxReviewIterations`,
  `maxAttemptsPerFinding`, `maxPrFixIterations`. An escalation stops the
  workflow and sets the work item to `awaiting_input`.
- A clean re-review resolves open findings **of its own category that a fixer
  has already attempted** (`api/routes/findings.ts`). That rule is what lets
  the loop converge.
- **The PR loop** (`forge/sync.ts`, `forge/classify.ts`): the poller syncs each
  open PR, records changes as events, and signals the workflow only for what
  the classifier deems actionable — a change request or a failing check.
  Merged → work item `done`; closed → `aborted`.

## Decisions already made

These were settled deliberately. Change them on purpose, not by accident.

- **Build order** follows the plan's §81 self-hosting bootstrap, not the §50
  v1 slice.
- **Workflow runtime** is Postgres-backed and Bun-only (§21 option A) —
  explicitly *not* Temporal.
- **Isolation is real Docker-per-Run from day one.** No host-only shortcut.
  No Docker socket in the container, all capabilities dropped,
  `no-new-privileges`, CPU/memory/PID limits, the workspace as the only
  writable mount.
- **Containers have network** — the agent must reach its model. Only
  `untrusted_external` repositories get none. The **push happens on the host**
  so the forge credential never enters a filesystem the agent can read.
- **Multi-tenant from the first migration**, enforced by row-level security.
- **Models are configured per project, per role**, falling back to the
  organization, then the system default. Per-role `context` is appended to
  that role's prompt.
- **DB access** is `Bun.sql` with raw SQL; migrations are `.sql` files. No ORM.
- **Phases are Runs**, not harness subagents. The reviewer executes but never
  publishes. Tests belong to the implementer; the *tester* is a browser QA
  agent (not built yet).
- **GitHub**: a PAT first, the GitHub App later behind the same `Forge`
  interface. **The factory never auto-merges** — it stops at an open PR.
- **Artifacts** will be S3-compatible from the start (not built yet).
- **Chat is the primary surface.** The event timeline and logs are debugging
  tools, one tab away.
- **Quality passes as you go**: a simplify/review pass after each meaningful
  piece of work, not at the end.

## Conventions

- **Comments explain why, not what.** Match the density of the surrounding
  code, which is high on purpose.
- **Commit messages explain the decision and what was found**, in prose. Look
  at `git log` for the house style.
- **Tests pin behaviour that matters**, and each new guard was checked by
  breaking it and watching the right test fail. Keep doing that.
- **Status and vocabulary live in `packages/domain`.** A new status should be
  a compile error in every `Record<Status, …>` that has to handle it.
- **The web app never talks to the database** and never reaches around the
  API (§117).

## Traps

Each of these cost real time. They are the reason for some code that might
otherwise look over-careful.

- **Don't run `bun test` with a dev control plane up.** They share the `dude`
  database and the sweepers are cross-tenant, so they steal the tests' rows.
  The E2E suite is safe: it makes its own database.
- **The unit tests can't see the runner.** Integration tests signal the
  workflow directly; they never exercise git inspection, the push or findings
  parsing. Several real bugs lived only there. Anything touching what the
  runner does with git needs the Python E2E suite, or a live run.
- **The TypeScript harness adapters** in `apps/control-plane/src/harness/`
  are exercised only by their own tests. The live path is the Go runner's
  harness (`runner/internal/harness/opencode.go`). Change behaviour there.
- **Git and zsh**: `:r` in a refspec is a zsh history modifier. Reproduce git
  problems with `bash -c`.
- **Force-pushing between phases** uses an explicit
  `--force-with-lease=<ref>:<base_ref>`. The bare form fails with "stale
  info" when pushing to a URL, because there are no tracking refs.
- **Diffs are against the Run's base**, not `HEAD` — `git diff --stat HEAD`
  is empty once an agent commits, which once made every Run look like it
  changed nothing.
- **`networkidle` never fires in the web app** — it holds an open SSE
  connection. Wait on elements in Playwright.
- **A pushed demo leaves state on GitHub.** A merged PR changes `main`, which
  changes what the fake reviewer sees next time.

## Testing strategy

| Layer | Where | What it proves |
| --- | --- | --- |
| Unit / integration | `apps/*/test`, beside Go packages | Logic, SQL, RLS, workflow transitions, parsers — against a real Postgres |
| E2E | `tests/suites` | The deployed system through its public API and built UI: the runner, containers, git, the forge |
| Browser | `tests/suites/test_web_ui.py`, `test_gallery_ui.py` | The web app and the design system, in system Chrome |

The fake agent (`fake/scripted` model) plays every phase deterministically:
the implementer commits, the reviewer raises one blocking finding and then
reports clean once a fixer has been through, the fixer and simplifier commit.
That is what lets the whole pipeline run in tests without a model.

## What's next

In rough priority order, with the reason for each.

1. **Real models end to end.** Everything above has been proven with the fake
   agent; one real OpenCode session has run in a container, but never the full
   phased pipeline. Expect the reviewer's YAML findings to need prompt tuning
   and a more forgiving parser.
2. **`ask_user`** — the agent asking a person a question and blocking on the
   answer (Bootstrap 3). Events and the `awaiting_input` status exist; there
   is no route to answer through.
3. **Egress allowlist.** Containers currently have full network. The plan
   (§25.3) wants deny-by-default plus an allowlist (model provider, package
   registries, git host, control plane), with web search routed through the
   control plane as a tool.
4. **Nested Docker for projects whose tests need it.** Plain Docker-in-Docker
   needs `--privileged`, which undoes the isolation. The recommendation is
   **Sysbox** as an opt-in runtime per project now, and a disposable VM per
   worker later (§17.1).
5. **The tester phase** (browser QA with video/screenshot evidence) and
   **S3 artifact storage** (Bootstrap 2 and 5). Design in
   [`phased-runs.md`](phased-runs.md). The implementer should also produce a
   demonstration recording; label it differently from the tester's evidence.
6. **GitHub App and webhooks** in place of the PAT and polling.
7. **Resuming a Run continues its harness session** instead of starting a
   fresh one (`sessions.external_session_id` is stored but unused).
8. **Forge credentials are stored in plaintext.** Encrypt at rest before any
   non-local use.

## Where things are

| You want to… | Look at |
| --- | --- |
| Change the delivery sequence or its bounds | `workflow/delivery.workflow.ts`, `workflow/policy.ts` |
| Change what an agent is told | `api/prompts.ts` |
| Change what a phase Run receives | `api/routes/runner.ts` (claim) |
| Change what wakes an agent on a PR | `forge/classify.ts` |
| Change how the runner pushes | `runner/cmd/factory-runner/publish.go` |
| Change how findings are parsed | `runner/cmd/factory-runner/findings.go` |
| Change the fake agent | `fakeScript` in `runner/cmd/factory-runner/runlifecycle.go` |
| Add an event type | `packages/domain/src/events/types.ts` (and `runner/internal/protocol` if the runner emits it) |
| Change the sidebar/board data | `api/routes/navigation.ts` |
| Change a screen | `apps/web/src/screens/` |
| Change a component | `packages/design-system/src/components/` |
