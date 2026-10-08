#!/usr/bin/env bash
# Real OpenCode, real proxy, real thinking: runs `opencode run --format json`
# on a small read-only question in this repository, for a Claude tier and a
# gpt-6-sol tier at an effort, each with OPENCODE_CONFIG_CONTENT exactly as
# buildSpec makes it (orchestrator/cmd/tier-config) layered over
# images/runtime/opencode.json as the image does (OPENCODE_CONFIG), and
# prints each run's reasoning parts. `--thinking` is what makes
# `opencode run` print reasoning parts at all.
#
# Not a CI test: it needs the proxy's key.
#
#   DUDE_LLM_KEY=… scripts/real-thinking.sh [EFFORT] [CLAUDE_MODEL] [GPT_MODEL]
#
# DUDE_LLM_URL defaults to https://llmproxy.absmartly-dev.com/v1. The key is
# never printed. The user's own OpenCode config, auth and data are kept out
# (XDG_* point at a scratch directory), so only the image's providers exist.
set -euo pipefail

root=$(git -C "$(dirname "$0")" rev-parse --show-toplevel)
effort=${1:-high}
claude=${2:-claude-sonnet-5}
gpt=${3:-gpt-6-sol}
: "${DUDE_LLM_KEY:?set DUDE_LLM_KEY to the key of the proxy}"
export DUDE_LLM_KEY
export DUDE_LLM_URL=${DUDE_LLM_URL:-https://llmproxy.absmartly-dev.com/v1}

scratch=$(mktemp -d)
trap 'rm -rf "$scratch"' EXIT
go -C "$root/orchestrator" build -o "$scratch/tier-config" ./cmd/tier-config

# A question that needs thought on the agent's own turn: adaptive thinking
# skips a trivial one, and gpt-6-sol sends a reasoning item with no summary
# when it has little to reason about.
question="Read migrations/096_tier_effort.sql. Reason it through step by step before answering: which of these headers objects does model_tier_headers_valid accept, and why each? {\"X-Team\":\"a\"}, {\"X Team\":\"a\"}, {\"a\":1}, {\"a\":\"b\\nc\"}, {\"~ok\":\"\"}. Change nothing."
status=0
for model in "$claude" "$gpt"; do
  config=$("$scratch/tier-config" "$model" "$effort")
  echo "=== $model at $effort"
  echo "OPENCODE_CONFIG_CONTENT=$config"
  home="$scratch/$model"
  # A model decides per turn whether to summarise its reasoning (gpt-6-sol
  # sometimes sends a reasoning item with no summary on a tool-using turn):
  # a few tries, each shown.
  ok=1
  for try in 1 2 3; do
    rm -rf "$home"
    mkdir -p "$home/config" "$home/data" "$home/cache" "$home/state"
    XDG_CONFIG_HOME="$home/config" XDG_DATA_HOME="$home/data" XDG_CACHE_HOME="$home/cache" XDG_STATE_HOME="$home/state" \
      OPENCODE_CONFIG="$root/images/runtime/opencode.json" OPENCODE_CONFIG_CONTENT="$config" \
      opencode run --format json --thinking --dir "$root" "$question" > "$home/events.jsonl" 2> "$home/stderr.log" || true
    echo "--- try $try"
    if python3 "$root/scripts/reasoning_parts.py" "$home/events.jsonl" "$model"; then
      ok=0
      break
    fi
  done
  [ $ok -eq 0 ] || status=1
done
exit $status
