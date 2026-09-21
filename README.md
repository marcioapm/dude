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

## Status

Bootstrap 1 (execution kernel) per plan §81 — in progress.
