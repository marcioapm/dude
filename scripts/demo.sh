#!/usr/bin/env bash
#
# Bring up the whole factory locally and watch it do one piece of work.
#
#   scripts/demo.sh            # fake task against a scratch repo
#   scripts/demo.sh --self     # point it at this repository instead
#
# Starts Postgres, the control plane and the runner, creates a project and a
# work item, then streams the event ledger while a real agent executes it in a
# container. Ctrl-C stops everything it started.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO_ROOT"

PG_CONTAINER="${DUDE_PG_CONTAINER:-dude-postgres}"
PG_PORT="${DUDE_PG_PORT:-5433}"
CP_PORT="${DUDE_CP_PORT:-3000}"
RUNTIME_IMAGE="${DUDE_RUNTIME_IMAGE:-dude-runtime:dev}"
WORKSPACE_ROOT="${DUDE_WORKSPACE_ROOT:-/tmp/dude-demo}"

OWNER_DSN="postgres://dude:dude@localhost:${PG_PORT}/dude"
APP_DSN="postgres://dude_app:dude_app@localhost:${PG_PORT}/dude"

TARGET_REPO=""
[[ "${1:-}" == "--self" ]] && TARGET_REPO="$REPO_ROOT"

say() { printf '\n\033[1;36m==> %s\033[0m\n' "$*"; }
note() { printf '    %s\n' "$*"; }

cleanup() {
  say "stopping"
  [[ -n "${RUNNER_PID:-}" ]] && kill "$RUNNER_PID" 2>/dev/null || true
  [[ -n "${CP_PID:-}" ]] && kill "$CP_PID" 2>/dev/null || true
  # Containers are labelled, so this cannot catch anything it did not create.
  docker ps -aq --filter "label=dude.managed=true" | xargs -r docker rm -f >/dev/null 2>&1 || true
  note "Postgres container '$PG_CONTAINER' left running (docker stop $PG_CONTAINER)"
}
trap cleanup EXIT INT TERM

# -- prerequisites ----------------------------------------------------------

say "checking prerequisites"
for tool in bun go docker git python3; do
  command -v "$tool" >/dev/null || { echo "missing: $tool" >&2; exit 1; }
done
docker info >/dev/null 2>&1 || { echo "docker daemon is not reachable" >&2; exit 1; }

# If OpenCode is configured on this machine, reuse its provider config and
# credentials so the demo works without a separate setup step. Explicit
# environment variables always win.
OC_CONFIG="${HOME}/.config/opencode/opencode.json"
OC_AUTH="${HOME}/.local/share/opencode/auth.json"

if [[ -z "${OPENCODE_CONFIG_CONTENT:-}" && -f "$OC_CONFIG" ]]; then
  OPENCODE_CONFIG_CONTENT="$(python3 -c '
import json, sys
cfg = json.load(open(sys.argv[1]))
print(json.dumps({"provider": cfg.get("provider", {})}))
' "$OC_CONFIG")"
  export OPENCODE_CONFIG_CONTENT
fi

if [[ -z "${ANTHROPIC_API_KEY:-}" && -f "$OC_AUTH" ]]; then
  ANTHROPIC_API_KEY="$(python3 -c '
import json, sys
auth = json.load(open(sys.argv[1]))
# Prefer an Anthropic-shaped provider; any api-type entry will do.
for name, entry in auth.items():
    if isinstance(entry, dict) and entry.get("type") == "api" and entry.get("key"):
        if "anthropic" in name or "claude" in name:
            print(entry["key"]); break
else:
    for entry in auth.values():
        if isinstance(entry, dict) and entry.get("key"):
            print(entry["key"]); break
' "$OC_AUTH")"
  [[ -n "$ANTHROPIC_API_KEY" ]] && export ANTHROPIC_API_KEY
fi

if [[ -z "${ANTHROPIC_API_KEY:-}${OPENAI_API_KEY:-}" ]]; then
  cat >&2 <<'MSG'
No model credentials found.

The agent runs a real model, so either configure OpenCode on this machine
(`opencode auth login`) or export a key directly:

    export ANTHROPIC_API_KEY=...
    # optionally, for a gateway or proxy:
    export ANTHROPIC_BASE_URL=https://...

MSG
  exit 1
fi
note "model credentials: present"

# -- postgres ---------------------------------------------------------------

say "starting postgres"
if ! docker ps --format '{{.Names}}' | grep -qx "$PG_CONTAINER"; then
  if docker ps -aq --filter "name=^${PG_CONTAINER}$" | grep -q .; then
    docker start "$PG_CONTAINER" >/dev/null
  else
    docker run -d --name "$PG_CONTAINER" \
      -e POSTGRES_USER=dude -e POSTGRES_PASSWORD=dude -e POSTGRES_DB=dude \
      -p "${PG_PORT}:5432" postgres:17 >/dev/null
  fi
fi
until docker exec "$PG_CONTAINER" pg_isready -U dude >/dev/null 2>&1; do sleep 0.5; done
note "ready on port $PG_PORT"

# -- build ------------------------------------------------------------------

say "building"
bun install --silent
DATABASE_URL="$OWNER_DSN" bun run apps/control-plane/src/db/migrate.ts
(cd runner && go build -o bin/factory-runner ./cmd/factory-runner)
docker image inspect "$RUNTIME_IMAGE" >/dev/null 2>&1 \
  || docker build -q -t "$RUNTIME_IMAGE" runner/images/runtime >/dev/null
note "runtime image: $RUNTIME_IMAGE"

# -- seed -------------------------------------------------------------------

say "seeding organization and keys"
SEED_JSON="$(DATABASE_URL="$APP_DSN" OWNER_DSN="$OWNER_DSN" bun run scripts/seed-demo.ts)"
USER_KEY="$(echo "$SEED_JSON" | grep -o '"userKey":"[^"]*"' | cut -d'"' -f4)"
RUNNER_KEY="$(echo "$SEED_JSON" | grep -o '"runnerKey":"[^"]*"' | cut -d'"' -f4)"
note "organization seeded"

# -- target repository ------------------------------------------------------

if [[ -z "$TARGET_REPO" ]]; then
  TARGET_REPO="$(mktemp -d)/sample"
  say "creating a scratch repository"
  mkdir -p "$TARGET_REPO"
  git -C "$TARGET_REPO" init -q --initial-branch=main
  git -C "$TARGET_REPO" config user.email demo@localhost
  git -C "$TARGET_REPO" config user.name "Demo"
  cat > "$TARGET_REPO/README.md" <<'MSG'
# sample

A tiny repository for the dude demo.
MSG
  git -C "$TARGET_REPO" add -A
  git -C "$TARGET_REPO" commit -qm "initial commit"
  note "$TARGET_REPO"
else
  say "targeting this repository"
  note "$TARGET_REPO (the agent works on a clone, never your working tree)"
fi

# -- control plane ----------------------------------------------------------

say "starting control plane"
DATABASE_URL="$APP_DSN" PORT="$CP_PORT" bun run apps/control-plane/src/index.ts \
  > /tmp/dude-demo-control-plane.log 2>&1 &
CP_PID=$!
until curl -fsS "http://localhost:${CP_PORT}/health" >/dev/null 2>&1; do
  kill -0 "$CP_PID" 2>/dev/null || { echo "control plane died:"; tail -20 /tmp/dude-demo-control-plane.log; exit 1; }
  sleep 0.3
done
note "http://localhost:${CP_PORT}  (log: /tmp/dude-demo-control-plane.log)"

# -- runner -----------------------------------------------------------------

say "starting runner"
DUDE_CONTROL_PLANE="http://localhost:${CP_PORT}" \
DUDE_RUNNER_KEY="$RUNNER_KEY" \
  runner/bin/factory-runner --workspace-root "$WORKSPACE_ROOT" --max-runs 1 \
  > /tmp/dude-demo-runner.log 2>&1 &
RUNNER_PID=$!
sleep 2
kill -0 "$RUNNER_PID" 2>/dev/null || { echo "runner died:"; tail -20 /tmp/dude-demo-runner.log; exit 1; }
note "workspaces under $WORKSPACE_ROOT  (log: /tmp/dude-demo-runner.log)"

# -- create the work --------------------------------------------------------

api() {
  curl -fsS -X "$1" "http://localhost:${CP_PORT}$2" \
    -H "authorization: Bearer $USER_KEY" -H "content-type: application/json" \
    ${3:+-d "$3"}
}

# Pick a model the local provider config actually declares, so the demo does
# not fail on a name that only exists in someone else's setup.
MODEL="${DUDE_MODEL:-}"
if [[ -z "$MODEL" && -n "${OPENCODE_CONFIG_CONTENT:-}" ]]; then
  MODEL="$(python3 -c '
import json, os
cfg = json.loads(os.environ["OPENCODE_CONFIG_CONTENT"])
for provider, spec in cfg.get("provider", {}).items():
    for model in spec.get("models", {}):
        if "sonnet" in model:
            print(f"{provider}/{model}"); raise SystemExit
for provider, spec in cfg.get("provider", {}).items():
    for model in spec.get("models", {}):
        print(f"{provider}/{model}"); raise SystemExit
')"
fi
MODEL="${MODEL:-anthropic/claude-sonnet-5}"

say "creating project and work item"
note "model: $MODEL"
PROJECT="$(api POST /v1/projects "$(cat <<JSON
{
  "name": "Demo",
  "slug": "demo-$(date +%s)",
  "runtimeImage": "$RUNTIME_IMAGE",
  "agentModels": { "orchestrator": { "model": "$MODEL" } },
  "repositories": [{ "name": "target", "url": "$TARGET_REPO" }]
}
JSON
)")"
PROJECT_ID="$(echo "$PROJECT" | grep -o '"id":"prj_[^"]*"' | head -1 | cut -d'"' -f4)"
note "project $PROJECT_ID"

TASK="${DUDE_TASK:-Add a CONTRIBUTING.md explaining how to run the tests. Commit it.}"
WORK_ITEM="$(api POST /v1/work-items "$(cat <<JSON
{
  "projectId": "$PROJECT_ID",
  "title": "$TASK",
  "goal": "$TASK",
  "acceptanceCriteria": ["the change is committed to git"]
}
JSON
)")"
WORK_ITEM_ID="$(echo "$WORK_ITEM" | grep -o '"id":"wi_[^"]*"' | head -1 | cut -d'"' -f4)"
note "work item $WORK_ITEM_ID"
note "task: $TASK"

RUN="$(api POST "/v1/work-items/${WORK_ITEM_ID}/runs" "{}")"
RUN_ID="$(echo "$RUN" | grep -o '"id":"run_[^"]*"' | head -1 | cut -d'"' -f4)"
note "run $RUN_ID"

# -- watch ------------------------------------------------------------------

say "streaming the event ledger (Ctrl-C to stop)"
echo
curl -fsSN "http://localhost:${CP_PORT}/v1/events/stream?runId=${RUN_ID}" \
  -H "authorization: Bearer $USER_KEY" 2>/dev/null |
while IFS= read -r line; do
  case "$line" in
    data:*)
      python3 - "${line#data: }" <<'PY'
import json, sys
try:
    e = json.loads(sys.argv[1])
except Exception:
    sys.exit()
t = e.get("eventType", "")
p = e.get("payload") or {}
detail = ""
if t == "agent.tool.called":
    detail = str(p.get("tool", ""))
elif t == "agent.message":
    text = json.dumps(p)[:100]
    detail = text
elif t == "git.commit_created":
    detail = (p.get("commits") or p.get("diffstat") or "").splitlines()[:1]
    detail = detail[0] if detail else "changes"
elif t in ("run.failed", "agent.session.stopped"):
    detail = str(p.get("error") or p.get("reason") or "")
print(f"  {e.get('cursor'):>4}  {t:<28} {detail[:90]}")
PY
      ;;
  esac
done
