#!/usr/bin/env bash
# runtime-image.sh builds an example of the image agents run in
# (images/runtime) into the local docker image store, first building the
# dude CLI it carries for the image's architecture. Nothing is pushed.
#
#   scripts/runtime-image.sh                    # tags dude-runtime:dev
#   ARCH=amd64 VERSION=v1.2.3 scripts/runtime-image.sh dude-runtime:v1.2.3
#
# ARCH (arm64 or amd64) defaults to this machine's architecture.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CONTEXT="$ROOT/images/runtime"
TAG="${1:-dude-runtime:dev}"
VERSION="${VERSION:-dev}"
LDFLAGS="-s -w -X github.com/marciomartins/dude/orchestrator/internal/version.Version=$VERSION"
if [[ -z "${ARCH:-}" ]]; then
  case "$(uname -m)" in
    arm64 | aarch64) ARCH=arm64 ;;
    x86_64 | amd64) ARCH=amd64 ;;
    *) echo "unknown architecture $(uname -m): set ARCH" >&2; exit 1 ;;
  esac
fi
case "$ARCH" in
  arm64 | amd64) ;;
  *) echo "unsupported ARCH $ARCH: arm64 or amd64" >&2; exit 1 ;;
esac

rm -rf "${CONTEXT:?}/bin"
echo "building dude for linux/$ARCH"
(cd "$ROOT/orchestrator" && CGO_ENABLED=0 GOOS=linux GOARCH="$ARCH" \
  go build -trimpath -ldflags "$LDFLAGS" -o "$CONTEXT/bin/linux-$ARCH/dude" ./cmd/dude)

docker build --platform "linux/$ARCH" -t "$TAG" "$CONTEXT"
