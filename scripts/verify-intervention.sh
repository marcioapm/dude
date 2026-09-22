#!/usr/bin/env bash
#
# Prove an intervention actually stops a running agent.
#
# Starts the stack, gives an agent a task long enough to catch mid-flight,
# then aborts it and checks that the container is gone, the Run is aborted,
# and the ledger explains why.
set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/.."

PG=dude-postgres
PORT=3399
OWNER_DSN="postgres://dude:dude@localhost:5433/dude"
APP_DSN="postgres://dude_app:dude_app@localhost:5433/dude"

say() { printf '\n\033[1;36m==> %s\033[0m\n' "$*"; }

cleanup() {
  [[ -n "${RUNNER_PID:-}" ]] && kill "$RUNNER_PID" 2>/dev/null || true
  [[ -n "${CP_PID:-}" ]] && kill "$CP_PID" 2>/dev/null || true
  docker ps -aq --filter "label=dude.managed=true" | xargs -r docker rm -f >/dev/null 2>&1 || true
}
trap cleanup EXIT INT TERM

# Reuse the demo's credential discovery.
OC_CONFIG="${HOME}/.config/opencode/opencode.json"
OC_AUTH="${HOME}/.local/share/opencode/auth.json"
export OPENCODE_CONFIG_CONTENT="$(python3 -c '
import json,sys; print(json.dumps({"provider": json.load(open(sys.argv[1])).get("provider", {})}))' "$OC_CONFIG")"
export ANTHROPIC_API_KEY="$(python3 -c '
import json,sys
for e in json.load(open(sys.argv[1])).values():
    if isinstance(e, dict) and e.get("key"): print(e["key"]); break' "$OC_AUTH")"
MODEL="$(python3 -c '
import json,os
cfg = json.loads(os.environ["OPENCODE_CONFIG_CONTENT"])
for p, s in cfg.get("provider", {}).items():
    for m in s.get("models", {}):
        if "sonnet" in m: print(f"{p}/{m}"); raise SystemExit')"

say "building"
DATABASE_URL="$OWNER_DSN" bun run apps/control-plane/src/db/migrate.ts >/dev/null
(cd runner && go build -o bin/factory-runner ./cmd/factory-runner)

say "seeding"
SEED="$(DATABASE_URL="$APP_DSN" OWNER_DSN="$OWNER_DSN" bun run scripts/seed-demo.ts)"
USER_KEY="$(echo "$SEED" | python3 -c 'import json,sys; print(json.load(sys.stdin)["userKey"])')"
RUNNER_KEY="$(echo "$SEED" | python3 -c 'import json,sys; print(json.load(sys.stdin)["runnerKey"])')"

TARGET="$(mktemp -d)/repo"
mkdir -p "$TARGET" && cd "$TARGET"
git init -q --initial-branch=main && git config user.email t@t && git config user.name T
echo "# sample" > README.md && git add -A && git commit -qm init
cd - >/dev/null

say "starting control plane and runner"
DATABASE_URL="$APP_DSN" PORT=$PORT bun run apps/control-plane/src/index.ts >/tmp/iv-cp.log 2>&1 &
CP_PID=$!
until curl -fsS "http://localhost:$PORT/health" >/dev/null 2>&1; do sleep 0.3; done

DUDE_CONTROL_PLANE="http://localhost:$PORT" DUDE_RUNNER_KEY="$RUNNER_KEY" \
  runner/bin/factory-runner --workspace-root /tmp/dude-iv --max-runs 1 >/tmp/iv-runner.log 2>&1 &
RUNNER_PID=$!
sleep 2

api() { curl -fsS -X "$1" "http://localhost:$PORT$2" -H "authorization: Bearer $USER_KEY" \
  -H "content-type: application/json" ${3:+-d "$3"}; }

say "creating a long-running task"
PRJ="$(api POST /v1/projects "{\"name\":\"IV\",\"slug\":\"iv-$(date +%s)\",\"runtimeImage\":\"dude-runtime:dev\",
  \"agentModels\":{\"orchestrator\":{\"model\":\"$MODEL\"}},
  \"repositories\":[{\"name\":\"target\",\"url\":\"$TARGET\"}]}" \
  | python3 -c 'import json,sys; print(json.load(sys.stdin)["id"])')"

WI="$(api POST /v1/work-items "{\"projectId\":\"$PRJ\",
  \"title\":\"Carefully review every file, then write a detailed report in REPORT.md, then refine it three times\",
  \"goal\":\"Take your time and be thorough.\"}" \
  | python3 -c 'import json,sys; print(json.load(sys.stdin)["id"])')"

RUN="$(api POST "/v1/work-items/$WI/runs" '{}' | python3 -c 'import json,sys; print(json.load(sys.stdin)["id"])')"
echo "    run $RUN"

say "waiting for the agent to start working"
for i in $(seq 1 60); do
  STATUS="$(api GET "/v1/runs/$RUN" | python3 -c 'import json,sys; print(json.load(sys.stdin)["status"])')"
  CONTAINER="$(docker ps -q --filter "label=dude.run_id=$RUN" | head -1)"
  [[ "$STATUS" == "running" && -n "$CONTAINER" ]] && break
  sleep 1
done
echo "    status=$STATUS container=${CONTAINER:0:12}"
[[ -z "$CONTAINER" ]] && { echo "agent never started"; tail -20 /tmp/iv-runner.log; exit 1; }

say "steering it mid-flight"
api POST "/v1/runs/$RUN/steer" '{"text":"Keep the report to one paragraph."}' >/dev/null
echo "    directive sent"

sleep 3
say "aborting it"
api POST "/v1/runs/$RUN/abort" '{"reason":"demonstrating intervention"}' >/dev/null

say "verifying"
for i in $(seq 1 45); do
  GONE="$(docker ps -q --filter "label=dude.run_id=$RUN" | head -1)"
  [[ -z "$GONE" ]] && break
  sleep 1
done

FINAL="$(api GET "/v1/runs/$RUN")"
printf '    run status:  %s\n' "$(echo "$FINAL" | python3 -c 'import json,sys; print(json.load(sys.stdin)["status"])')"
printf '    container:   %s\n' "$([[ -z "$GONE" ]] && echo 'stopped' || echo "STILL RUNNING ($GONE)")"
printf '    ledger:      %s\n' "$(curl -fsS "http://localhost:$PORT/v1/events?runId=$RUN" \
  -H "authorization: Bearer $USER_KEY" \
  | python3 -c 'import json,sys; print(" → ".join(e["eventType"] for e in json.load(sys.stdin)["events"]))')"

say "done"
