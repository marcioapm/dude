#!/usr/bin/env bash
# test-image-builder.sh runs dude-image-builder's tests against a real
# rootless podman and a throwaway registry, on a Linux host whose user has
# rootless podman with cgroup v2 cpu and memory delegated (vibes has):
#
#   scripts/test-image-builder.sh
#
# It starts registry:2 on localhost (REGISTRY_PORT, 5000), runs the
# podman tests in orchestrator/internal/images (build within 1.5 GB, an
# over-allocation killed and reported, push, digest and removal, the dude
# layer finished onto debian:bookworm-slim and node:24-bookworm-slim with
# HOME and the git identity the agent's, an image with no shell refused,
# and the cgroup controllers the builder needs at start), then removes the
# registry and every image the tests made.
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
  [[ " $controllers " == *" $c "* ]] || echo "warning: the $c controller is not delegated to $(id -un) ($controllers); the builder will refuse to start" >&2
done

podman run -d --rm --name "$NAME" -p "127.0.0.1:$PORT:5000" docker.io/library/registry:2 >/dev/null
cleanup() {
  podman stop -t 2 "$NAME" >/dev/null 2>&1 || true
  # What the tests built and pulled back, by tag or by digest only.
  podman images --format '{{.Repository}} {{.ID}}' | awk -v r="localhost:$PORT/" 'index($1, r) == 1 {print $2}' |
    sort -u | xargs -r podman rmi -f >/dev/null 2>&1 || true
}
trap cleanup EXIT
for _ in $(seq 1 30); do
  curl -fsS "http://127.0.0.1:$PORT/v2/" >/dev/null 2>&1 && break
  sleep 0.5
done

# Compiled, then run from this shell: podman reports the cgroup controllers
# of the cgroup it is started in, and `go test` (as a snap) starts it in a
# scope of its own that may lack cpu, as the builder's service would not.
BIN="$(mktemp -d)/images.test"
go -C "$ROOT/orchestrator" test -c -o "$BIN" ./internal/images/
(cd "$ROOT/orchestrator/internal/images" && DUDE_PODMAN_TEST_REGISTRY="localhost:$PORT" "$BIN" -test.count=1 -test.v -test.timeout 30m "$@")
rm -rf "$(dirname "$BIN")"
