#!/usr/bin/env bash
# build-migrate.sh compiles bin/dude-migrate with every migrations/*.sql
# embedded in it, so it needs no SQL on disk:
#
#   scripts/build-migrate.sh <outfile> [bun build flags, e.g. --target=…]
#
# The SQL files are extra entrypoints of `bun build --compile`; they reach
# the binary as Bun.embeddedFiles, under their own names ([name].[ext]
# rather than Bun's default hashed names). DUDE_EMBEDDED_MIGRATIONS tells
# apps/control-plane/src/db/migrate.ts to read them instead of the repo's
# directory.
set -euo pipefail

out="${1:?usage: build-migrate.sh <outfile> [bun build flags]}"
shift
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

shopt -s nullglob
sql=("$ROOT"/migrations/*.sql)
[[ ${#sql[@]} -gt 0 ]] || { echo "no migrations in $ROOT/migrations" >&2; exit 1; }

bun build --compile "$@" --define DUDE_EMBEDDED_MIGRATIONS=true \
  --asset-naming='[name].[ext]' \
  "$ROOT/apps/control-plane/src/db/migrate.ts" "${sql[@]}" --outfile "$out"
