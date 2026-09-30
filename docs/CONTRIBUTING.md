# Working on dude

This is the guide for picking the project up: how the pieces fit, which
decisions are settled, where the traps are, and what to build next. The
[README](../README.md) covers running it; the
[plan](../software-factory-plan-v0.9.md) is the long-form design this
implements, and section numbers below (§21, §81…) refer to it.

## The shape of the system

```
               ┌─────────── backend (Bun) ───────────┐
  web UI ─────▶│ public API, auth, projects, board,  │◀──── GitHub webhooks
  (React)      │ settings, live events (SSE)         │      (verified, stored)
               └──────┬───────────────▲──────────────┘
          run control │               │ NOTIFY on every event
     (service token)  ▼               │
               ┌──────────────────────┴──────────────┐
               │ orchestrator (Go) — no users         │
               │ workflow runtime, delivery, policy, │──── GitHub API
               │ prompts, findings, PR sync          │     (PRs, branches)
               └──────┬──────────────────────────────┘
                      │ submit, follow output, steer,
                      │ stop, resume, push (HTTP + SSE)
               ┌──────▼──────────────────────────────┐
               │ lux — containers on Podman hosts,   │
               │ checkouts, leased pushes, agents    │
               └─────────────────────────────────────┘
                 (a separate project: ~/git/lux)

   Both dude processes share one Postgres, with row-level security.
```

**The backend** (`apps/control-plane`, Bun) is the only thing users and
GitHub talk to. It owns display and management: auth, organizations,
projects, tasks, the board and sidebar (`/v1/navigation`), findings,
forge settings, and the live event stream. It runs no background work.
What changes what runs — deliver, steer, pause, resume, abort — it forwards
to the orchestrator (`src/orchestrator/client.ts`) with a service token, the
user's organization and who asked, and passes the answer straight back.
GitHub webhooks land here, are verified against a per-organization secret
and stored; the orchestrator processes them.

**The orchestrator** (`orchestrator/`, Go) does all the work and serves
no users. Its internal API (`internal/api`) is called by the backend. Its
loops (`cmd/dude-orchestrator/main.go`):

| Loop | What it does |
| --- | --- |
| `workflow` | Advances durable workflows that are runnable or were signalled, each step in its own slot. |
| `phase-sync` | Drives each phase Run on lux: submit, follow its output into the ledger, deliver directives, pause, resume, cancel, and when the agent's turn ends, push, fast-forward the branch, record findings, stop. |
| `phase-notifier` | Turns a finished phase Run into a `phase.finished` signal for its workflow. |
| `webhooks` | Acts on stored GitHub deliveries: syncs the pull request each is about. |
| `previews` | Drives branch previews on lux: submit, follow their state and servers, park one nobody has opened for its project's idle timeout, resume a parked one a person starts a server on, cancel a stopped one. |
| `pr-reconciler` | Re-reads open pull requests not seen for 15 minutes — the backstop for webhooks GitHub never sent. |

**lux** is the runtime (`~/git/lux`, its own repository and docs). It runs
each phase Run in a container on a Podman host, checks out the repository
at the Run's base commit, runs the agent through an adapter (OpenCode over
ACP), streams what happens, steers and stops it, and pushes its commits
with a credential the container never sees. dude talks to it only through
`orchestrator/internal/lux`.

**The web app** (`apps/web`) talks only to the backend's public API.

**The design system** (`packages/design-system`) is product-agnostic and has
its own [README](../packages/design-system/README.md) with its rules. Browse it
with `bun run gallery` in that package.

### Hierarchy

`Organization → Project → Epic → Task → Run → Session` (§39). A **Run**
is one phase of work, executed as one lux Run. A single *attempt* at a work
item is several Runs — one per phase — and each is shown as an agent in the
UI.

### Delivery, end to end

`orchestrator/internal/delivery/workflow.go` is the state machine;
`store.go` holds its side effects; `policy.go` holds every bound;
`prompts.go` what each agent is told.

```
implement → review (fan-out) ⟲ fix → simplify → [test] → open PR → wait ⟲ fix → done
```

- Each phase is a Run with `phase`, `role`, `base_ref` (the commit it starts
  from) and, for reviews, `category`. `internal/phases/spec.go` turns it into
  a lux RunSpec: image, adapter, prompt, model and effort (inline OpenCode
  config in `OPENCODE_CONFIG_CONTENT`), the LLM's URL and key (`DUDE_LLM_URL`
  env, `DUDE_LLM_KEY` env secret), repository at `base_ref`, egress to the
  LLM's host, and the agent's home as a state volume so a resume keeps the
  conversation.
- **Handoff is via git.** Each publishing phase pushes to a branch of its
  own (`dude/<task>/run-<run>`) — lux lets a Run's first push go only to
  a branch that does not exist — and the orchestrator fast-forwards the work
  item's branch to it through GitHub, never forcing. The next phase checks
  out that commit.
- **Publishing is a property of the phase** (`Publishes`): a reviewer's
  container is never pushed.
- **A turn is done when the agent goes busy then idle**, as lux's shim
  reports it in the output stream, in order with the agent's messages.
  The exception is a turn that ends with something open for a person: a
  question (`ask_person`), or a repository requested with `wait: true`.
  Then the Run is **waiting** (`runs.waiting_since`), and the answer starts
  its next turn.
- **Reviewers report findings** as YAML in their reply; `delivery/findings.go`
  parses it. Only review and test Runs may report.
- **Every loop ends on a declared bound**: `MaxReviewIterations`,
  `MaxAttemptsPerFinding`, `MaxPRFixIterations`. An escalation stops the
  workflow and sets the task to `awaiting_input`.
- A clean re-review resolves open findings **of its own category that a fixer
  has already attempted** (`phases.RecordFindings`). That rule is what lets
  the loop converge.
- **The PR loop** (`internal/prs`, `forge/classify.go`): a webhook says which
  PR changed; the orchestrator reads that PR, records what changed, and
  signals the workflow only for what the classifier deems actionable — a
  change request or a failing check. Merged → task `done`; closed →
  `aborted`.
- **Steering** goes to the agent as a message, read at its next step: the
  harness takes it while a tool runs and the model reads it before its
  next call, in the same turn, without cancelling the tool. A harness that
  reads messages only between turns takes it when the turn ends.
  `interrupt: true` stops the turn so it is heard now; it is only ever a
  person's explicit choice. lux answers each input once with a `lux.input`
  record, `{requestId, phase: "accepted", lands, receipt, text?}` or
  `{requestId, phase: "failed", error}` (never accepted); what follows an
  acceptance has record types of its own, `lux.input.consumed
  {requestId}` (only when `receipt`) or `lux.input.failed {requestId,
  error}`, so a reader of `lux.input` alone sees one answer per input. An
  older lux writes one `lux.input` with no phase, on handoff (`text`) or
  with an `error`. A directive is `sent` when lux has it, `accepted` when
  the harness took it (`run.directive.accepted`, with `lands`:
  `next_step` or `next_turn`), and `delivered` when the agent's step has
  it (`run.directive.delivered`, from `lux.input.consumed`; at once for an
  acceptance with `receipt: false`). An older lux's handoff counts as
  delivered. `failed` (`run.directive.failed`, with lux's `error`) is a
  steer lux says will not reach the agent. Each is recorded once per
  directive and Run. Precedence: delivered is final, and
  a later failure changes nothing; a failure is final against `accepted`;
  a read receipt (or an older lux's handoff) after a failure wins, since it
  is the agent's own report that it has the words: the failure and its
  error are cleared and `run.directive.delivered` is written.
  "Interrupt now" on a queued steer is a directive superseding it with the
  same words and `interrupt: true`. Its `resends` names the root
  instruction (an Interrupt now on an Interrupt now names the first), so
  repeated clicks are one instruction. Whether it carries the words is
  decided at its first send attempt and then fixed (`interrupt_only`), so
  every retry sends the same request: if a directive carrying the words is
  with lux and not failed, lux is sent the interrupt alone; if the root
  failed or was never sent, the interrupt carries the words, and the agent
  hears them once. lux sends no receipt for an interrupt alone: it is
  delivered in the same transaction as the directive carrying its words
  (consumed, or a legacy handoff), or when sent if that one was already
  read, each time with `run.directive.delivered` `{interruptOnly: true}`
  (never `read`). If the words fail and no directive left carries them, the
  interrupt fails with the same error, one `run.directive.failed` each. lux
  carries steers an interrupt left unread into the next turn, under the same
  request ids, so the root is normally consumed in the turn after the
  interrupt; they fail only when the Run stops or the harness errors. An
  older lux fails them on the cancel: the root fails, the interrupt fails
  with it, and the transcript shows one failed steer with Retry. The
  transcript folds every attempt into the root's turn, at the position and
  time it was read, even when the click came after the read.
- **Pause** stops the lux Run, keeping its workspace and session; **resume**
  continues it, on any host, with the agent's conversation intact.
- **Parking.** dude pauses a Run itself (`runs.dude_pause`) so that one
  waiting on a person holds no host. The syncer resumes it on its own when
  the reason is over:
  - `person`: waiting past the project's `parkAfterMinutes` (default 10). An
    answer or a decision resumes it.
  - `idle`: quiet mid-turn for `idleNudgeMinutes` (off by default), nudged
    once, then quiet as long again. The task goes to awaiting input,
    and only a person's Resume takes it up.
  - `repository`: stopped a moment so the resume can bring in a repository
    a person approved.

  A person's own pause always wins. The chat shows each park and resume
  (`run.parked`, `run.unparked`). A Run has **no wall-clock limit**: dude
  sends no timeout, and lux counts a set one as running time only.
- **Asks die with their Run.** Questions and repository requests still open
  when a Run ends are cancelled (triggers). Answering one returns a 409 that
  says so. Design: [`design/agent-tools.md`](design/agent-tools.md).

### Servers and branch previews

`orchestrator/internal/servers`; the API is `apps/control-plane/src/api/routes/servers.ts`.

- **A project's servers are recipes** (`project_servers`): a port, a
  command, a workdir in the repository, a setup step. A person adds one to
  a task's Run, and lux runs it as `sh -c "[setup &&] <command>"` in
  `/workspace/repos/<repo>/<workdir>`, giving it a URL. The command is a
  shell line as typed (`PORT=3000 npm start`, `cd web && npm run dev`), not
  exec'd: lux stops a server by signalling its whole process group.
- **lux owns a server's state.** dude keeps none: it asks lux, and passes
  lux's Server object on as it came. lux's `server.*` events on a Run's
  stream become `servers.changed`, and the browser reads again.
- **Which Run a task shows**: its agent at work (the publishing one), else
  its live branch preview, else none. A live preview behind an agent at
  work is still named (`preview` in a task's servers), so it can be stopped.
- **A migration stops servers.** lux does not restart them after a move
  (only a spec's servers start on every start); `moved` says so, and a
  person starts them again. A move is also not the agent dying: a Run lux
  stopped with a move's reason (`lux.Moved`) is left running.
- **A branch preview is a Run with no agent** (`runs.kind = 'preview'`, no
  phase), serving the task's branch with the recipes marked to start in
  previews as the spec's `workload.servers`. It is parked (`dude_pause =
  'unused'`) after the project's idle timeout without a request, by lux's
  `lastRequestAt`; starting a server on it resumes it. One per task.
  Steer, pause, resume and abort refuse it; it is stopped with `DELETE
  /v1/tasks/:id/preview`, or ends when its task does (done, failed,
  aborted). A stopped preview's lux Run is cancelled, a lost one too (lux
  keeps a lost Run to resume).
- **Previews open in a new tab**, never in a frame: a preview's sign-in
  cookie is SameSite=Lax and does not reach a cross-site iframe. Only an
  `https://` server URL becomes a link.
- **Egress entries are what lux takes**: `*`, a hostname (no wildcards —
  lux resolves each), an address or a CIDR range. The API refuses others;
  the orchestrator leaves out any saved before it did.

## Decisions already made

These were settled deliberately. Change them on purpose, not by accident.

- **Build order** follows the plan's §81 self-hosting bootstrap, not the §50
  v1 slice.
- **Two processes.** The Go orchestrator owns everything that drives work;
  the Bun backend owns display and management and is the only public
  surface. One Postgres, shared; one writer per table is a guideline, not
  enforced.
- **lux is the runtime.** dude does not run containers. It tells lux what
  to run and decides what the result means.
- **Workflow runtime** is Postgres-backed and hand-built (§21 option A) —
  *not* Temporal — and lives in the orchestrator.
- **PR observability is by webhook, never polling.** GitHub's rate limits
  rule polling out. A reconciler re-reads open PRs every 15 minutes as a
  backstop.
- **Multi-tenant from the first migration**, enforced by row-level security,
  in both processes.
- **Models are configured per project, per role**, falling back to the
  organization. Per-role `context` is appended to that role's prompt.
- **DB access**: `Bun.sql` in the backend, `pgx` in the orchestrator, raw SQL
  in both; migrations are `.sql` files run by `bun run migrate`.
- **Phases are Runs**, not agent subagents. The reviewer executes but never
  publishes. Tests belong to the implementer; the *tester* is a browser QA
  agent (not built yet).
- **GitHub**: a PAT first, the GitHub App later behind the same client.
  **The factory never auto-merges** — it stops at an open PR.
- **Artifacts** will be S3-compatible (lux stores them; not wired up yet).
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
- **Event types are a contract** between the processes and the browser:
  `packages/domain/src/events/types.ts` is the vocabulary, and the Go
  constants must match it.
- **The web app never talks to the database** and never reaches around the
  API (§117).

## Traps

Each of these cost real time.

- **The fakes must be as awkward as the real thing.** The fake lux sends
  lifecycle events *after* the agent's records, queues input to a busy
  agent, and resumes an agent idle — because real lux does, and code that
  assumed otherwise passed against a friendlier fake and failed against lux.
  When the contract suite (`run_tests.py --lux`) disagrees with the fake,
  fix the fake first.
- **A `--serve` lux environment runs the lux-fake it was built with.** After
  lux-fake changes, restart it.
- **lux's lifecycle events trail the agent's records.** The Run's recorded
  `lux_state` can say "scheduled" after the agent has finished; only a state
  lux has already reported as over rules anything out.
- **Don't run `bun test` with a dev backend up** against the same database;
  keep test and dev databases apart. The Go tests make a database of their
  own per binary, cloned from a migrated template.
- **Migration 014 is still being edited** until it ships. A database that
  applied an earlier version needs its checksum updated in
  `schema_migrations` (or recreate it).
- **`networkidle` never fires in the web app** — it holds an open SSE
  connection. Wait on elements in Playwright.
- **A pushed demo leaves state on GitHub.** A merged PR changes `main`, which
  changes what the next run sees.

## Testing strategy

| Layer | Where | What it proves |
| --- | --- | --- |
| Go unit | `orchestrator/internal/*` | Workflow runtime, policy, classifier, parsers — against a real Postgres |
| Go integration | `orchestrator/delivery_test.go` | Whole deliveries through the orchestrator's real code, against a fake lux and a fake GitHub backed by real git |
| TS unit | `apps/control-plane/test` | The ledger, the live stream, webhook signatures |
| E2E | `tests/suites` | The deployed processes through the public API and the built UI, with the fake lux and a fake GitHub that delivers signed webhooks |
| Contract | `tests/suites/test_lux_contract.py` (`--lux`) | The same flows against a real lux |
| Browser | `tests/suites/test_web_ui.py`, `test_gallery_ui.py` | The web app and the design system, in system Chrome |

The scripted agent (`orchestrator/internal/fakeagent`, model `fake/scripted`)
plays every phase deterministically: the implementer commits, the reviewer
raises one blocking finding unless the fixer's file is in its tree, the fixer
and simplifier commit. `fake/hang` never finishes its turn, for steering and
pausing. The fake lux plays it directly; a real lux runs it as a lux-fake
script.

### Manual browser sign-in

Manual key submission verifies the candidate through `/v1/me` before storing
it. A successful submission uses `location.replace` to open a new document at
the current same-origin path and hash, dropping query parameters. It does not
mount the authenticated app in the password form's document. Invalid keys are
not stored; failed checks leave the form available for retry. Key-mode sign-out
clears this browser's credential without revoking the API key.

The document boundary replaces extension-injected DOM as well as the form.
Browser regressions prove one navigation after successful verification and no
navigation or persistence on refusal. They do **not** prove the reported
Bitwarden residue is fixed: its cause has not been reproduced, and real
extension verification remains manual. No extension DOM manipulation, synthetic
Escape, CSS suppression, or shared input changes are involved.

Cloudflare Access is not implemented yet. Enabling keyless sign-in requires the
coordinated person-principal, ownership, push and attribution migration in both
processes first; a person ID must never be substituted for an API-key ID.
Eventual aiverse rendering must supply the `[auth]` settings in dude's
configuration file (or their `DUDE_AUTH_*` variables), including the fixed
trusted `public_url` and Access team/audience, without changing the default
API-key mode. No aiverse wiring is included here.

**Configuration** is one TOML file for both processes plus the environment
(docs/operations.md, "Configuration"). The key table lives once per language
— `orchestrator/internal/config` and `apps/control-plane/src/config.ts` —
and both suites compare it to `tests/fixtures/config/keys.json` and resolve
`full.toml` to `full.json`: a new setting is a row in all three and a line
in `full.toml`, `full.json` and `docs/dude.example.toml`. Every file in
`tests/fixtures/config/invalid/` must be refused by both suites with the
error its first comment line names. Read sites take
values from the resolved config, never `os.Getenv`/`process.env`.

## What's next

In rough priority order.

1. **Real models end to end.** Everything has been proven with the scripted
   agent; run real tasks with OpenCode through lux, read the
   transcripts, and tune prompts, the findings parser and policy.
2. **Telling a person something waits on them**: a notification (Slack,
   email) for a question or a request. Today they are only on the board.
3. **Budgets as loop bounds** — a cost cap per Run and per task.
4. **The tester phase** (browser QA with recorded evidence) and artifacts
   from lux. Design in [`phased-runs.md`](phased-runs.md).
5. **GitHub App** in place of the PAT, and registering webhooks
   automatically when a project is added (`forge.EnsureWebhook` exists).
6. **lux push with an expected base commit**, which would remove the
   per-Run branch and the GitHub fast-forward.
7. **Forge credentials are stored in plaintext.** Encrypt at rest before any
   non-local use.

## Where things are

| You want to… | Look at |
| --- | --- |
| Change the delivery sequence or its bounds | `orchestrator/internal/delivery/workflow.go`, `policy.go` |
| Change what an agent is told | `orchestrator/internal/delivery/prompts.go` |
| Change what lux is asked to run | `orchestrator/internal/phases/spec.go` |
| Change what a branch preview runs, or when it parks | `orchestrator/internal/servers/previews.go` |
| Change what an agent's output becomes in the ledger | `orchestrator/internal/phases/translate.go` |
| Change how a finished phase is collected | `finish` and `publish` in `orchestrator/internal/phases/sync.go` |
| Change what wakes an agent on a PR | `orchestrator/internal/forge/classify.go` |
| Change how findings are parsed | `orchestrator/internal/delivery/findings.go` |
| Change the scripted agent | `orchestrator/internal/fakeagent` |
| Add an event type | `packages/domain/src/events/types.ts` and the Go constants that write it |
| Change the sidebar/board data | `apps/control-plane/src/api/routes/navigation.ts` |
| Change a screen | `apps/web/src/screens/` |
| Change a component | `packages/design-system/src/components/` |
