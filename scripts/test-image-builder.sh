#!/usr/bin/env bash
# test-image-builder.sh runs dude-image-builder's tests against a real
# rootless podman and a throwaway registry, on a Linux host whose user has
# rootless podman with cgroup v2 cpu and memory delegated (vibes has):
#
#   scripts/test-image-builder.sh
#
# It starts registry:2 on localhost (REGISTRY_PORT, 5000), runs the
# podman tests in orchestrator/internal/images (build within 1.5 GB, an
# over-allocation killed and reported, push and digest, the dude layer
# finished onto debian:bookworm-slim and node:24-bookworm-slim, and an
# image with no shell refused), then removes the registry.
#
# The queue tests in the same package need Postgres (DUDE_TEST_PG); they
# run too when it is reachable, and skip otherwise.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
PORT="${REGISTRY_PORT:-5000}"
NAME="dude-test-registry-$$"

command -v podman >/dev/null || { echo "podman is not on PATH" >&2; exit 1; }
if [[ "$(podman info --format '{{.Host.CgroupsVersion}}')" != "v2" ]]; then
  echo "podman is not on cgroup v2: --memory cannot apply to rootless builds" >&2
  exit 1
fi
controllers="$(cat "/sys/fs/cgroup/user.slice/user-$(id -u).slice/user@$(id -u).service/cgroup.controllers" 2>/dev/null || true)"
for c in cpu memory; do
  [[ " $controllers " == *" $c "* ]] || echo "warning: the $c controller is not delegated to $(id -un) ($controllers)" >&2
done

podman run -d --rm --name "$NAME" -p "127.0.0.1:$PORT:5000" docker.io/library/registry:2 >/dev/null
trap 'podman stop -t 2 "$NAME" >/dev/null 2>&1 || true' EXIT
for _ in $(seq 1 30); do
  curl -fsS "http://127.0.0.1:$PORT/v2/" >/dev/null 2>&1 && break
  sleep 0.5
done

DUDE_PODMAN_TEST_REGISTRY="localhost:$PORT" go -C "$ROOT/orchestrator" test -count=1 -v -timeout 30m ./internal/images/ "$@"
