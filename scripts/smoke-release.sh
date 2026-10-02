#!/usr/bin/env bash
# smoke-release.sh checks a release tarball on the machine it runs on, which
# must match the tarball's architecture:
#
#   scripts/smoke-release.sh dist/dude_vX.Y.Z_linux_amd64.tar.gz
#
# It unpacks the tarball into a temporary prefix and checks that
#   - every bin/* --version prints the version, with DATABASE_URL unset;
#   - FEATURES lists validate and image-builder, and dude-orchestrator,
#     dude-backend and dude-image-builder validate accept a good file and
#     refuse a bad one;
#   - the tarball holds no .sql files;
#   - bin/dude-migrate, run from a directory with no SQL in it, applies
#     every migration in this repository's migrations/, reports them all
#     applied, and a second run changes nothing;
#   - bin/dude-backend, as the app role, serving share/dude/web, answers
#     /health with {"status":"ok"} and / with HTML.
#
# SMOKE_OWNER_URL  a superuser DSN for an empty database (required)
# SMOKE_APP_URL    the same database as dude_app (required)
# VERSION          the expected version; by default, the one in the file name
# SMOKE_PORT       where dude-backend listens (3999)
#
# Run it from the checkout the tarball was built from. Needs curl and tar.
set -euo pipefail

tarball="${1:?usage: smoke-release.sh <tarball>}"
OWNER_URL="${SMOKE_OWNER_URL:?set SMOKE_OWNER_URL to a superuser DSN}"
APP_URL="${SMOKE_APP_URL:?set SMOKE_APP_URL to the dude_app DSN}"
PORT="${SMOKE_PORT:-3999}"
name="$(basename "$tarball")"
if [[ -z "${VERSION:-}" ]]; then
  [[ "$name" =~ ^dude_(.+)_linux_(amd64|arm64)\.tar\.gz$ ]] || {
    echo "cannot read a version from $name; set VERSION" >&2
    exit 1
  }
  VERSION="${BASH_REMATCH[1]}"
fi

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
prefix="$(mktemp -d "${TMPDIR:-/tmp}/dude-smoke.XXXXXX")"
backend_pid=""
cleanup() {
  if [[ -n "$backend_pid" ]]; then
    kill "$backend_pid" 2>/dev/null || true
    wait "$backend_pid" 2>/dev/null || true
  fi
  rm -rf "$prefix"
}
trap cleanup EXIT

fail() {
  echo "FAIL: $*" >&2
  exit 1
}

echo "unpacking $name into $prefix"
tar -xzf "$tarball" -C "$prefix"

sql="$(tar -tzf "$tarball" | grep -i '\.sql$' || true)"
[[ -z "$sql" ]] || fail "the tarball ships SQL; dude-migrate embeds it: $sql"
echo "ok  the tarball holds no .sql files"

for cmd in dude-orchestrator dude dude-image-builder dude-backend dude-migrate; do
  bin="$prefix/bin/$cmd"
  [[ -x "$bin" ]] || fail "bin/$cmd is missing or not executable"
  got="$(env -u DATABASE_URL "$bin" --version)" || fail "bin/$cmd --version exited $?"
  [[ "$got" == "$VERSION" ]] || fail "bin/$cmd --version printed '$got', want '$VERSION'"
  echo "ok  bin/$cmd --version = $got"
done

[[ -f "$prefix/FEATURES" ]] || fail "the tarball has no FEATURES"
grep -qx validate "$prefix/FEATURES" || fail "FEATURES does not list validate: $(cat "$prefix/FEATURES")"
echo "ok  FEATURES lists validate"
grep -qx image-builder "$prefix/FEATURES" || fail "FEATURES does not list image-builder: $(cat "$prefix/FEATURES")"
echo "ok  FEATURES lists image-builder"

# validate, on a good and a bad file, with none of this machine's settings.
# Port 1 on loopback: were validate to connect, it would fail loudly.
good="$prefix/good.toml"
bad="$prefix/bad.toml"
printf '[database]\nurl = "postgres://dude_app:x@127.0.0.1:1/dude"\n[orchestrator]\ntoken = "t"\n[lux]\nurl = "https://lux.invalid"\napi_key = "k"\n' > "$good"
printf '[database]\nurl = "postgres://dude_app:x@127.0.0.1:1/dude"\n[lux]\nurll = "x"\n' > "$bad"
chmod 0600 "$good" "$bad"
for cmd in dude-orchestrator dude-backend; do
  got="$(env -i PATH="$PATH" DUDE_CONFIG="$good" "$prefix/bin/$cmd" validate)" || fail "bin/$cmd validate on a good file exited $?"
  [[ "$got" == "ok: $good" ]] || fail "bin/$cmd validate printed '$got', want 'ok: $good'"
  set +e
  err="$(env -i PATH="$PATH" DUDE_CONFIG="$bad" "$prefix/bin/$cmd" validate 2>&1 >/dev/null)"
  code=$?
  set -e
  [[ $code == 1 && "$err" == *"unknown key lux.urll"* ]] || fail "bin/$cmd validate on a bad file: exit $code, '$err'"
  echo "ok  bin/$cmd validate: good file accepted, bad refused"
done
# dude-image-builder validates its own keys: the good file above lacks them.
builder_good="$prefix/builder.toml"
printf '[images]\nlayer = "r.invalid/dude/layer@sha256:%s"\n[builder]\ndatabase_url = "postgres://dude_builder:x@127.0.0.1:1/dude"\nrepository = "r.invalid/dude/custom"\n' \
  "$(printf '0%.0s' $(seq 1 64))" > "$builder_good"
chmod 0600 "$builder_good"
got="$(env -i PATH="$PATH" DUDE_CONFIG="$builder_good" "$prefix/bin/dude-image-builder" validate)" || fail "bin/dude-image-builder validate on a good file exited $?"
[[ "$got" == "ok: $builder_good" ]] || fail "bin/dude-image-builder validate printed '$got'"
set +e
err="$(env -i PATH="$PATH" DUDE_CONFIG="$bad" "$prefix/bin/dude-image-builder" validate 2>&1 >/dev/null)"
code=$?
set -e
[[ $code == 1 && "$err" == *"unknown key lux.urll"* ]] || fail "bin/dude-image-builder validate on a bad file: exit $code, '$err'"
echo "ok  bin/dude-image-builder validate: good file accepted, bad refused"

# From an empty directory, so no SQL beside it or under it can be read.
migrate() {
  (cd "$prefix/empty" && DATABASE_URL="$OWNER_URL" "$prefix/bin/dude-migrate" "$@")
}
mkdir "$prefix/empty"

migrate || fail "dude-migrate exited $?"

want="$(find "$ROOT/migrations" -maxdepth 1 -name '*.sql' -exec basename {} \; | LC_ALL=C sort | sed 's/^/applied  /')"
[[ -n "$want" ]] || fail "no migrations in $ROOT/migrations"
got="$(migrate --status)" || fail "dude-migrate --status exited $?"
if [[ "$got" != "$want" ]]; then
  diff <(echo "$want") <(echo "$got") >&2 || true
  fail "dude-migrate --status does not report every migration in migrations/ applied"
fi
echo "ok  dude-migrate --status: $(echo "$got" | wc -l | tr -d ' ') migrations applied"

got="$(migrate)" || fail "second dude-migrate run exited $?"
[[ "$got" == "up to date" ]] || fail "second dude-migrate run was not a no-op: $got"
echo "ok  second dude-migrate run: $got"

DATABASE_URL="$APP_URL" DUDE_WEB_DIR="$prefix/share/dude/web" PORT="$PORT" \
  "$prefix/bin/dude-backend" &
backend_pid=$!

base="http://127.0.0.1:$PORT"
for _ in $(seq 60); do
  curl -fsS -o /dev/null "$base/health" 2>/dev/null && break
  kill -0 "$backend_pid" 2>/dev/null || fail "dude-backend exited before answering"
  sleep 0.5
done

got="$(curl -fsS "$base/health")" || fail "/health did not answer 200"
[[ "$got" == '{"status":"ok"}' ]] || fail "/health answered $got"
echo "ok  /health = $got"

headers="$(mktemp "$prefix/headers.XXXXXX")"
body="$(curl -fsS -D "$headers" "$base/")" || fail "/ did not answer 200"
grep -qi '^content-type: text/html' "$headers" || fail "/ is not text/html: $(grep -i '^content-type' "$headers")"
[[ "${body,,}" == "<!doctype html>"* ]] || fail "/ is not an HTML document: ${body:0:80}"
echo "ok  / is HTML ($(echo -n "$body" | wc -c | tr -d ' ') bytes)"

echo "smoke test passed: $name"
