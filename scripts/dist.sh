#!/usr/bin/env bash
# dist.sh builds the release tarballs, plus SHA256SUMS, into dist/.
# Called by `VERSION=vX.Y.Z bun run dist`.
#
#   dude_<version>_linux_{arm64,amd64}.tar.gz
#     bin/dude-orchestrator    Go, static
#     bin/dude                 the agent CLI, Go, static
#     bin/dude-backend         the backend, bun --compile
#     bin/dude-migrate         the migration runner, bun --compile, with
#                              migrations/*.sql embedded (build-migrate.sh)
#     share/dude/web/          the built web app (DUDE_WEB_DIR)
#     share/dude/third-party/  licences of code bundled into the binaries
#     FEATURES                 what this release supports, one per line:
#                              a deployment tool reads it before relying on
#                              a subcommand an older release lacks
#
# Unpacking a tarball into a prefix gives that layout under it.
# The bun binaries link glibc dynamically; the Go ones link nothing.
set -euo pipefail

VERSION="${VERSION:?set VERSION=vX.Y.Z}"
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
DIST="$ROOT/dist"
GO_LDFLAGS="-s -w -X github.com/marciomartins/dude/orchestrator/internal/version.Version=$VERSION"
# Read by apps/control-plane/src/build.ts.
BUN_DEFINES=(--define "DUDE_BUILD_VERSION=\"$VERSION\"")
# The archive's FEATURES file, one per line; see below.
FEATURES=(validate)
# Reproducible tarballs: root-owned regardless of the builder's uid, sorted,
# and a fixed mtime (the commit's own date, so a rebuild of the same tag has
# the same timestamps) rather than each build's wall clock. GNU tar only:
# on macOS, gtar from Homebrew.
MTIME="$(git -C "$ROOT" log -1 --format=%cI 2>/dev/null || date -u +%Y-%m-%dT%H:%M:%SZ)"
TAR="${TAR:-$(command -v gtar || command -v tar)}"
"$TAR" --version 2>/dev/null | grep -q 'GNU tar' || { echo "dist.sh needs GNU tar (set TAR=)" >&2; exit 1; }
TAR_REPRO_FLAGS=(--owner=0 --group=0 --numeric-owner --sort=name --mtime="$MTIME")
if command -v sha256sum >/dev/null; then SHA256=(sha256sum); else SHA256=(shasum -a 256); fi
# The binaries embed the Bun that builds them, and Bun < 1.4.0 cannot upload
# photos to a store that answers Connection: close (versitygw). The floor is
# package.json's engines.bun; Bun.semver orders a pre-release of it below it.
(cd "$ROOT" && bun -e '
  const floor = require("./package.json").engines.bun.replace(/^>=\s*/, "");
  if (Bun.semver.order(Bun.version, floor) < 0) {
    console.error(`dist.sh needs Bun >= ${floor}, found ${Bun.version}: earlier Bun fails S3 uploads to versitygw`);
    process.exit(1);
  }
') || exit 1

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
  "$ROOT/scripts/build-migrate.sh" "$work/bin/dude-migrate" \
    --target="bun-linux-$bun_arch" "${BUN_DEFINES[@]}"

  cp -R "$ROOT/apps/web/dist" "$work/share/dude/web"
  cp "$ROOT/LICENSE" "$work/share/dude/LICENSE"
  # dude-backend bundles jose (MIT), and dude-orchestrator links go-toml
  # (MIT); both licences ask for their notice in copies.
  mkdir -p "$work/share/dude/third-party"
  cp "$ROOT/apps/control-plane/node_modules/jose/LICENSE.md" "$work/share/dude/third-party/jose-LICENSE.md"
  cp "$(cd "$ROOT/orchestrator" && go list -m -f '{{.Dir}}' github.com/pelletier/go-toml/v2)/LICENSE" \
    "$work/share/dude/third-party/go-toml-LICENSE"

  chmod 0755 "$work/bin/"*
  chmod -R u+rwX,go+rX,go-w "$work/share"
  # validate: `dude-orchestrator validate` and `dude-backend validate` check
  # the configuration and exit. An older release has no such subcommand, and
  # its orchestrator would start instead.
  printf '%s\n' "${FEATURES[@]}" > "$work/FEATURES"
  chmod 0644 "$work/FEATURES"
  # gzip -n: no name or timestamp in the gzip header, which tar -z leaves
  # to whichever gzip is installed.
  "$TAR" "${TAR_REPRO_FLAGS[@]}" -C "$work" -cf - FEATURES bin share | gzip -9n > "$DIST/dude_${VERSION}_linux_${arch}.tar.gz"
  rm -rf "$work"
done

(cd "$DIST" && "${SHA256[@]}" dude_*.tar.gz > SHA256SUMS)

echo "dist/:"
ls -la "$DIST"
