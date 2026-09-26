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
two-and-a-half-minute recording of a task going from the board to a
merged pull request on GitHub, driven entirely through the UI.

**Picking this up?** Read [`docs/CONTRIBUTING.md`](docs/CONTRIBUTING.md) for how
the system fits together, the decisions already made, and what is next.

## What it does today

1. A task is created on the board and someone presses **Deliver**.
2. An **implementer** agent makes the change in its own container, on its own
   checkout, and commits. Its work is pushed and becomes the task's
   branch.
3. **Reviewers** fan out in parallel — which ones depends on what the diff
   touches — each in its own container at the implementer's commit. They can
   run the code but cannot publish; their output is structured findings.
4. If a finding blocks, a **fixer** gets exactly those findings, and the
   review runs again. The loop ends on a bound declared in policy, never on a
   model deciding it is finished.
5. A **simplifier** tidies the branch without changing behaviour.
6. dude opens a **pull request** whose body is rendered from the ledger, then
   waits. GitHub tells it what happens by webhook: a reviewer's comment wakes
   a fixer; an approval or a green check wakes nobody. Merging finishes the
   task.

A person can steer any agent while it works, pause it, and resume it later —
on another machine, with its conversation intact. Every step is an event in
an append-only ledger, streamed live to the UI.

## Layout

```
orchestrator/         Go: the workflow runtime, delivery, policy, prompts, lux client, PR sync
apps/control-plane/   Bun + TypeScript: the public API, the board, settings, live events, webhooks
apps/web/             React web app: sidebar, board, delivery view, agent chat
packages/domain/      Shared types and the event vocabulary
packages/design-system/  Tokens, primitives and components (see its README)
images/runtime/       The image agents run in
migrations/           Raw SQL migrations — the source of truth for the schema
tests/                Python E2E suite driving the public API and the browser
docs/                 Design notes, the contributor guide, the demo video
scripts/              Seeding helpers
```

Agents run on **lux** (`~/git/lux`), a separate project that runs work in
containers on Podman hosts and moves it between them. See
[`docs/CONTRIBUTING.md`](docs/CONTRIBUTING.md) for how the pieces fit.

## Development

### Prerequisites

Bun, Go 1.25, Docker, Python 3.14 with `uv`, git, and Google Chrome (the
browser tests drive the system Chrome; Playwright's bundled Chromium has no
build for Ubuntu 26.04). To run real agents, a lux (`~/git/lux`) and
OpenCode credentials.

```bash
bun install
docker run -d --name dude-postgres \
  -e POSTGRES_USER=dude -e POSTGRES_PASSWORD=dude -e POSTGRES_DB=dude \
  -p 5433:5432 postgres:17

DATABASE_URL="postgres://dude:dude@localhost:5433/dude" bun run migrate
(cd orchestrator && go build -o bin/ ./cmd/...)
```

Migrations run as the owner role. Everything else connects as `dude_app`,
which has neither `SUPERUSER` nor `BYPASSRLS`, so the row-level security that
isolates tenants is a real boundary rather than a convention.

### Running the whole thing

A lux, the orchestrator, the backend and the web app.

```bash
# 1. A lux to run agents on: two Podman hosts, a tenant and an API key
(cd ~/git/lux/tests && uv run python run_tests.py --serve --detach \
    --image dude-runtime:dev)       # prints luxd_url and api_key
docker build -t dude-runtime:dev images/runtime   # first, so --image can load it

# 2. The orchestrator: all background work, no users
DATABASE_URL="postgres://dude_app:dude_app@localhost:5433/dude" \
DUDE_ORCHESTRATOR_TOKEN=dev-token \
LUX_URL=<luxd_url> LUX_API_KEY=<api_key> \
DUDE_AGENT_IMAGE=docker.io/library/dude-runtime:dev \
  orchestrator/bin/dude-orchestrator         # internal API on 127.0.0.1:3100

# 3. The backend, on :3000
DATABASE_URL="postgres://dude_app:dude_app@localhost:5433/dude" \
DUDE_ORCHESTRATOR_URL=http://127.0.0.1:3100 DUDE_ORCHESTRATOR_TOKEN=dev-token \
  bun run dev

# 4. An organization and a user key (organizations are provisioned, not self-served)
OWNER_DSN="postgres://dude:dude@localhost:5433/dude" \
DATABASE_URL="postgres://dude_app:dude_app@localhost:5433/dude" \
  bun run scripts/seed-demo.ts            # prints { organizationId, userKey }

# 5. The web app on :5180, proxying /v1 to the backend
(cd apps/web && bun run dev)
```

Open http://localhost:5180 and sign in with the `userKey`.

The orchestrator gives agents this machine's OpenCode credentials
(`~/.local/share/opencode/auth.json` and the providers in
`~/.config/opencode/opencode.json`) unless `DUDE_OPENCODE_AUTH` and
`DUDE_OPENCODE_CONFIG` say otherwise. Set a role's model in the project's
`agentModels` (e.g. `{"implementer": {"model": "llmproxy-anthropic/claude-sonnet-5"}}`);
`fake/scripted` runs the whole pipeline without a model — deterministic,
free, and what the tests use.

To have delivery open real pull requests, store a GitHub token for the
organization and point a project at a repository you own:

```bash
curl -X POST localhost:3000/v1/forge/credential \
  -H "authorization: Bearer <userKey>" -H "content-type: application/json" \
  -d "{\"auth\":\"pat\",\"secret\":\"$(gh auth token)\"}"
```

The response names the webhook path (`/v1/webhooks/github/<org>`). GitHub
must reach it for dude to see PR activity; on a laptop, `gh webhook forward`
relays deliveries to localhost. Without webhooks, a reconciler still re-reads
open pull requests every 15 minutes.

`scripts/seed-github.ts` creates such a project in one step.

## Tests

Unit tests live beside the code in each language. The E2E suite is a
deliberate external client: Python driving the public HTTP API and the built
web app, never importing the implementation.

```bash
bun run typecheck
DATABASE_URL="postgres://dude:dude@localhost:5433/dude" bun test   # TypeScript
(cd orchestrator && go test ./...)                                  # Go

cd tests
uv run python run_tests.py              # full E2E suite (~2 minutes)
uv run python run_tests.py --no-ui      # skip browser suites
uv run python run_tests.py --keep       # keep the database to debug
uv run python run_tests.py --lux        # the contract suite, against a real lux
uv run python run_tests.py suites/test_web_ui.py   # one suite
```

Each E2E run gets its own database, free ports and a log directory (printed
at the start; set `DUDE_TEST_LOG_DIR` to choose it) holding the backend's,
orchestrator's, fake lux's and web server's output. Agents run on a stand-in
for lux, and pull requests on a stand-in for GitHub — a git daemon, the REST
endpoints dude calls, and signed webhook deliveries — so the suite needs no
network, no containers and no token. `--lux` runs the same flows against a
real lux (the latest `run_tests.py --serve` in lux's repository, or
`DUDE_TEST_LUX_ENV`), so a drift between the stand-in and lux shows up.

## Releases

`VERSION=vX.Y.Z bun run dist` (`scripts/dist.sh`, needs Go, Bun and GNU
tar) builds `dist/dude_<version>_linux_{arm64,amd64}.tar.gz` and
`SHA256SUMS` over them. Each holds `bin/{dude-orchestrator,dude,dude-backend,dude-migrate}`
and `share/dude/{web,migrations}`; unpack it into a prefix. The Go binaries
are static; the Bun ones are `bun build --compile` and need glibc. Every
binary answers `--version`. The tarballs are reproducible: the same commit
builds the same bytes.

## Status

Against the plan's self-hosting order (§81):

| Bootstrap | State |
| --- | --- |
| 1 — Execution kernel: API, orchestrator, agents on lux, ledger, web UI | done |
| 2 — Memory and artifacts: S3 artifact store, memory tables, search | not started |
| 3 — Human control: steer, pause, resume, abort, conversation | done, except `ask_user` |
| 4 — GitHub loop: push, PR, review/CI feedback, merge detection | done with a PAT and webhooks; the GitHub App remains |
| 5 — Review and test quality: reviewers, simplifier, E2E, browser QA, costs | reviewers, fix loop, simplifier and E2E done; browser QA not started |
| 6 — Remote execution, Slack/Jira | not started |

What is next, and why, is in [`docs/CONTRIBUTING.md`](docs/CONTRIBUTING.md#whats-next).
