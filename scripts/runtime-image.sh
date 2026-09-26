#!/usr/bin/env bash
# runtime-image.sh builds the image agents run in (images/runtime), first
# building the dude CLI it carries for every platform it is built for.
#
#   scripts/runtime-image.sh -t dude-runtime:dev
#   PLATFORMS=linux/arm64,linux/amd64 VERSION=v1.2.3 \
#     scripts/runtime-image.sh --push -t ghcr.io/marcioapm/dude-runtime:v1.2.3
#
# PLATFORMS defaults to this machine's architecture, as linux; arguments go
# to `docker build` (one platform) or `docker buildx build` (several).
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CONTEXT="$ROOT/images/runtime"
VERSION="${VERSION:-dev}"
LDFLAGS="-s -w -X github.com/marciomartins/dude/orchestrator/internal/version.Version=$VERSION"
if [[ -z "${PLATFORMS:-}" ]]; then
  case "$(uname -m)" in
    arm64 | aarch64) PLATFORMS=linux/arm64 ;;
    x86_64 | amd64) PLATFORMS=linux/amd64 ;;
    *) echo "unknown architecture $(uname -m): set PLATFORMS" >&2; exit 1 ;;
  esac
fi

rm -rf "${CONTEXT:?}/bin"
IFS=, read -ra platforms <<<"$PLATFORMS"
for platform in "${platforms[@]}"; do
  arch="${platform#linux/}"
  [[ "$platform" == linux/* && "$arch" != */* ]] || { echo "unsupported platform $platform" >&2; exit 1; }
  echo "building dude for $platform"
  (cd "$ROOT/orchestrator" && CGO_ENABLED=0 GOOS=linux GOARCH="$arch" \
    go build -trimpath -ldflags "$LDFLAGS" -o "$CONTEXT/bin/linux-$arch/dude" ./cmd/dude)
done

if (( ${#platforms[@]} == 1 )); then
  docker build --platform "$PLATFORMS" "$@" "$CONTEXT"
else
  docker buildx build --platform "$PLATFORMS" "$@" "$CONTEXT"
fi
