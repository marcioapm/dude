# dude

A software factory: a deterministic control plane that drives disposable AI
agent workers through implementation, review, fixing, simplification and
pull-request delivery.

The guiding rule, from `software-factory-plan-v0.9.md`:

> The LLM is a worker, not the workflow engine and not the source of truth.

Long waits — human questions, CI, PR reviews, capacity, timers — happen in the
control plane with no agent running and no tokens burning. Agents are woken
only when there is genuinely new information that needs judgment.

**See it work:** [`docs/demo/delivery.mp4`](docs/demo/delivery.mp4) is a
two-and-a-half-minute recording of a work item going from the board to a
merged pull request on GitHub, driven entirely through the UI.

**Picking this up?** Read [`docs/CONTRIBUTING.md`](docs/CONTRIBUTING.md) for how
the system fits together, the decisions already made, and what is next.

## What it does today

1. A work item is created on the board and someone presses **Deliver**.
2. An **implementer** agent makes the change in its own container, on its own
   clone, and commits. The runner pushes the branch.
3. **Reviewers** fan out in parallel — which ones depends on what the diff
   touches — each in its own container at the implementer's commit. They can
   run the code but cannot publish; their output is structured findings.
4. If a finding blocks, a **fixer** gets exactly those findings, and the
   review runs again. The loop ends on a bound declared in policy, never on a
   model deciding it is finished.
5. A **simplifier** tidies the branch without changing behaviour.
6. The control plane opens a **pull request** whose body is rendered from the
   ledger, then waits. A reviewer's comment on the PR wakes a fixer; an
   approval or a green check wakes nobody. Merging finishes the work item.

Every step is an event in an append-only ledger, streamed live to the UI.

## Layout

```
apps/control-plane/   Bun + TypeScript: API, workflow runtime, event ledger, forge
apps/web/             React web app: sidebar, board, delivery view, agent chat
packages/domain/      Shared types, event vocabulary, core interfaces
packages/design-system/  Tokens, primitives and components (see its README)
runner/               Go worker daemon: Docker-per-Run execution, push, findings
migrations/           Raw SQL migrations — the source of truth for the schema
tests/                Python E2E suite driving the public API and the browser
docs/                 Design notes, the contributor guide, the demo video
scripts/              Local demo and seeding helpers
```

## Development

### Prerequisites

Bun, Go 1.25, Docker, Python 3.14 with `uv`, git, and Google Chrome (the
browser tests drive the system Chrome; Playwright's bundled Chromium has no
build for Ubuntu 26.04).

```bash
bun install
docker run -d --name dude-postgres \
  -e POSTGRES_USER=dude -e POSTGRES_PASSWORD=dude -e POSTGRES_DB=dude \
  -p 5433:5432 postgres:17

DATABASE_URL="postgres://dude:dude@localhost:5433/dude" bun run migrate
docker build -t dude-runtime:dev runner/images/runtime
(cd runner && go build -o bin/factory-runner ./cmd/factory-runner)
```

Migrations run as the owner role. Everything else connects as `dude_app`,
which has neither `SUPERUSER` nor `BYPASSRLS`, so the row-level security that
isolates tenants is a real boundary rather than a convention.

### Running the whole thing

Three processes: the control plane, a runner, and the web app.

```bash
# 1. Control plane (API + background sweepers) on :3000
DATABASE_URL="postgres://dude_app:dude_app@localhost:5433/dude" bun run dev

# 2. An organization and its keys (organizations are provisioned, not self-served)
OWNER_DSN="postgres://dude:dude@localhost:5433/dude" \
DATABASE_URL="postgres://dude_app:dude_app@localhost:5433/dude" \
  bun run scripts/seed-demo.ts            # prints { organizationId, userKey, runnerKey }

# 3. A runner, with the runner key
DUDE_CONTROL_PLANE=http://localhost:3000 DUDE_RUNNER_KEY=<runnerKey> \
  runner/bin/factory-runner --workspace-root /tmp/dude-runner

# 4. The web app on :5180, proxying /v1 to the control plane
(cd apps/web && bun run dev)
```

Open http://localhost:5180 and sign in with the `userKey`.

To have delivery open real pull requests, store a GitHub token for the
organization and point a project at a repository you own:

```bash
curl -X POST localhost:3000/v1/forge/credential \
  -H "authorization: Bearer <userKey>" -H "content-type: application/json" \
  -d "{\"auth\":\"pat\",\"secret\":\"$(gh auth token)\"}"
```

`scripts/seed-github.ts` creates such a project in one step. Set an agent's
model to `fake/scripted` in the project's `agentModels` to run the whole
pipeline without a model — deterministic, free, and what the tests use.

### Seeing a real agent

```bash
./scripts/demo.sh          # a scratch repository
./scripts/demo.sh --self   # this repository
```

Runs one real OpenCode agent in a container and streams the ledger. Reuses
local OpenCode credentials if present; otherwise export `ANTHROPIC_API_KEY`.
The agent works on a clone — never your working tree.

## Tests

Unit tests live beside the code in each language. The E2E suite is a
deliberate external client: Python driving the public HTTP API and the built
web app, never importing the Bun implementation.

```bash
bun run typecheck
DATABASE_URL="postgres://dude:dude@localhost:5433/dude" bun test   # TypeScript
(cd runner && go test ./...)                                        # Go

cd tests
uv run python run_tests.py              # full E2E suite (~9 minutes)
uv run python run_tests.py --no-runner  # skip suites needing Docker
uv run python run_tests.py --no-ui      # skip browser suites
uv run python run_tests.py --keep       # keep the database and workspaces to debug
uv run python run_tests.py suites/test_web_ui.py   # one suite
```

Each E2E run gets its own database, free ports and a log directory (printed
at the start; set `DUDE_TEST_LOG_DIR` to choose it) holding the control
plane's, runner's and web server's output. Pull-request tests run against a
local stand-in for GitHub — a git daemon plus the REST endpoints the factory
calls — so the suite needs no network and no token.

**Stop any dev control plane before `bun test`.** It shares the `dude`
database, and its sweepers are cross-tenant by design, so they will claim the
rows the tests just inserted and make unrelated tests fail at random.

## Status

Against the plan's self-hosting order (§81):

| Bootstrap | State |
| --- | --- |
| 1 — Execution kernel: API, runner, Docker-per-Run, workspace cache, harness, ledger, web UI | done |
| 2 — Memory and artifacts: S3 artifact store, memory tables, search | not started |
| 3 — Human control: steer, pause, resume, abort, conversation | done, except `ask_user` |
| 4 — GitHub loop: push, PR, review/CI feedback, merge detection | done with a PAT and polling; GitHub App and webhooks remain |
| 5 — Review and test quality: reviewers, simplifier, E2E, browser QA, costs | reviewers, fix loop, simplifier and E2E done; browser QA not started |
| 6 — Remote execution, Slack/Jira | not started |

What is next, and why, is in [`docs/CONTRIBUTING.md`](docs/CONTRIBUTING.md#whats-next).
