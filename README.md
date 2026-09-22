# dude

A software factory: a deterministic control plane that drives disposable AI
agent workers through investigation, implementation, review, simplification,
testing and pull-request delivery.

The guiding rule, from `software-factory-plan-v0.9.md`:

> The LLM is a worker, not the workflow engine and not the source of truth.

Long waits — human questions, CI, PR reviews, capacity, timers — happen in the
control plane with no agent running and no tokens burning. Agents are woken
only when there is genuinely new information that needs judgment.

## Layout

```
apps/control-plane/   Bun + TypeScript control plane (API, workflow, events)
apps/web/             Web UI (primary control surface)
packages/domain/      Shared types, event schema, core interfaces
runner/               Go worker daemon: Docker-per-Run execution
migrations/           Raw SQL migrations (source of truth for schema)
tests/                Python E2E suite driving the public API
agents/               Agent role definitions
```

## Development

```bash
bun install
docker run -d --name dude-postgres \
  -e POSTGRES_USER=dude -e POSTGRES_PASSWORD=dude -e POSTGRES_DB=dude \
  -p 5433:5432 postgres:17

export DATABASE_URL="postgres://dude:dude@localhost:5433/dude"
bun run migrate            # apply migrations as the owner role
bun test                   # TypeScript unit tests
```

The control plane connects as `dude_app`, a role with neither `SUPERUSER` nor
`BYPASSRLS`, so the row-level security policies that isolate tenants are a real
boundary. Migrations run as the owner; runtime traffic does not.

```bash
export DATABASE_URL="postgres://dude_app:dude_app@localhost:5433/dude"
bun run dev
```

### Running the execution plane

The runner is the same binary locally and on a worker node, so development
exercises registration, leases, container lifecycle and reconciliation rather
than a shortcut path that only exists in dev.

```bash
docker build -t dude-runtime:dev runner/images/runtime
cd runner && go build -o bin/factory-runner ./cmd/factory-runner

DUDE_CONTROL_PLANE=http://localhost:3000 \
DUDE_RUNNER_KEY=<a runner-kind API key> \
  ./bin/factory-runner --workspace-root /tmp/dude-runner
```

### Tests

Unit tests live beside the code in each language. The E2E suite is a
deliberate external client: Python 3.14 driving the public HTTP API, never
importing the Bun implementation, so it tests the deployed system.

```bash
bun test                                   # TypeScript unit tests
cd runner && go test ./...                 # Go unit tests

cd tests
uv venv --python 3.14 .venv && uv pip install --python .venv -e .
.venv/bin/python run_tests.py              # full E2E suite
.venv/bin/python run_tests.py --no-runner  # skip suites needing Docker
.venv/bin/python run_tests.py --keep       # keep the environment to debug
```

Each E2E run creates its own database and picks free ports, so runs do not
collide with each other or with a development instance.

## Status

Bootstrap 1 — execution kernel (plan §81). The goal of this phase is that the
factory can modify its own repository.

| Component | State |
| --- | --- |
| Domain model, event schema, core interfaces | done |
| Multi-tenant schema with row-level security | done |
| Control plane API, event ledger, SSE | done |
| Durable workflow runtime (Postgres) | done |
| Go runner, Docker-per-Run, workspace cache | done |
| OpenCode harness + fake harness | done |
| Python E2E suite | done |
| Design system | in progress |
| Web UI | not started |

