#!/usr/bin/env bash
# dist.sh builds the release tarballs, plus SHA256SUMS, into dist/.
# Called by `VERSION=vX.Y.Z bun run dist`.
#
#   dude_<version>_linux_{arm64,amd64}.tar.gz
#     bin/dude-orchestrator    Go, static
#     bin/dude                 the agent CLI, Go, static
#     bin/dude-backend         the backend, bun --compile
#     bin/dude-migrate         the migration runner, bun --compile
#     share/dude/web/          the built web app (DUDE_WEB_DIR)
#     share/dude/migrations/   the SQL dude-migrate applies by default
#
# Unpacking a tarball into a prefix gives that layout under it; dude-migrate
# finds ../share/dude/migrations relative to its own (resolved) path.
# The bun binaries link glibc dynamically; the Go ones link nothing.
set -euo pipefail

VERSION="${VERSION:?set VERSION=vX.Y.Z}"
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
DIST="$ROOT/dist"
GO_LDFLAGS="-s -w -X github.com/marciomartins/dude/orchestrator/internal/version.Version=$VERSION"
# Read by apps/control-plane/src/build.ts.
BUN_DEFINES=(--define "DUDE_BUILD_VERSION=\"$VERSION\"" --define "DUDE_BUILD_RELEASE=true")
# Reproducible tarballs: root-owned regardless of the builder's uid, sorted,
# and a fixed mtime (the commit's own date, so a rebuild of the same tag has
# the same timestamps) rather than each build's wall clock. GNU tar only:
# on macOS, gtar from Homebrew.
MTIME="$(git -C "$ROOT" log -1 --format=%cI 2>/dev/null || date -u +%Y-%m-%dT%H:%M:%SZ)"
TAR="${TAR:-$(command -v gtar || command -v tar)}"
"$TAR" --version 2>/dev/null | grep -q 'GNU tar' || { echo "dist.sh needs GNU tar (set TAR=)" >&2; exit 1; }
TAR_REPRO_FLAGS=(--owner=0 --group=0 --numeric-owner --sort=name --mtime="$MTIME")
if command -v sha256sum >/dev/null; then SHA256=(sha256sum); else SHA256=(shasum -a 256); fi

rm -rf "$DIST"
mkdir -p "$DIST"

echo "building the web app"
(cd "$ROOT" && bun install --frozen-lockfile)
(cd "$ROOT/apps/web" && bun run build)

for arch in arm64 amd64; do
  case $arch in
    arm64) bun_arch=arm64 ;;
    amd64) bun_arch=x64 ;;
  esac
  work="$DIST/work-linux-$arch"
  mkdir -p "$work/bin" "$work/share/dude"

  for cmd in dude-orchestrator dude; do
    echo "building $cmd for linux/$arch"
    (cd "$ROOT/orchestrator" && CGO_ENABLED=0 GOOS=linux GOARCH=$arch \
      go build -trimpath -ldflags "$GO_LDFLAGS" -o "$work/bin/$cmd" "./cmd/$cmd")
  done
  echo "building dude-backend and dude-migrate for linux/$arch"
  bun build --compile --target="bun-linux-$bun_arch" "${BUN_DEFINES[@]}" \
    "$ROOT/apps/control-plane/src/index.ts" --outfile "$work/bin/dude-backend"
  bun build --compile --target="bun-linux-$bun_arch" "${BUN_DEFINES[@]}" \
    "$ROOT/apps/control-plane/src/db/migrate.ts" --outfile "$work/bin/dude-migrate"

  cp -R "$ROOT/apps/web/dist" "$work/share/dude/web"
  mkdir -p "$work/share/dude/migrations"
  cp "$ROOT"/migrations/*.sql "$work/share/dude/migrations/"

  chmod 0755 "$work/bin/"*
  chmod -R u+rwX,go+rX,go-w "$work/share"
  # gzip -n: no name or timestamp in the gzip header, which tar -z leaves
  # to whichever gzip is installed.
  "$TAR" "${TAR_REPRO_FLAGS[@]}" -C "$work" -cf - bin share | gzip -9n > "$DIST/dude_${VERSION}_linux_${arch}.tar.gz"
  rm -rf "$work"
done

(cd "$DIST" && "${SHA256[@]}" dude_*.tar.gz > SHA256SUMS)

echo "dist/:"
ls -la "$DIST"
