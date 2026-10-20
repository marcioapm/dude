#!/usr/bin/env bash
# go-test.sh runs every Go test in orchestrator/, as CI's go job does:
#
#   [SHARDS=2] scripts/go-test.sh
#
# The root package's tests run one at a time and take far longer than any
# other package, so its test binary runs as SHARDS processes, each given
# every SHARDS-th test by sorted name, alongside `go test -p 4` over the
# other packages. The step fails when any process fails, when a test skips
# for want of a database (DUDE_TEST_PG, default localhost:5433), or when a
# listed root test has no top-level result line. Output streams live, each
# line prefixed with its process ([shard1], [others]), and every process's
# output goes, one after the other, to $RUNNER_TEMP/go-test.log.
set -euo pipefail

SHARDS="${SHARDS:-2}"
[[ $SHARDS =~ ^[1-9][0-9]*$ ]] || { echo "SHARDS must be a positive integer, not '$SHARDS'" >&2; exit 1; }
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT/orchestrator"
out="${RUNNER_TEMP:-$(mktemp -d)}"
work="$(mktemp -d "$out/go-test.XXXXXX")"
trap 'rm -f "$work/root.test"' EXIT
echo "nproc: $(nproc); root shards: $SHARDS; logs: $work"

# go test kills a test binary a minute after -timeout, with SIGQUIT for a
# stack dump. Run directly, the binary's own alarm covers only the tests,
# not TestMain's work around them (dbtest.Main's setup and cleanup), so
# limit DURATION CMD... does the killing; without timeout(1) (stock macOS)
# it just runs CMD.
limit() {
  if command -v timeout >/dev/null; then timeout -s QUIT -k 1m "$@"; else "${@:2}"; fi
}

go test -c -o "$work/root.test" .
limit 5m "$work/root.test" -test.list . >"$work/all" ||
  { echo "listing the root package's tests failed or ran out of time (exit $?)" >&2; exit 1; }
# Shards run Test functions only, so an Example or a Fuzz seed would go unrun.
if grep -E '^(Example|Fuzz)' "$work/all" >&2; then
  echo "the root package has the functions above, which this script does not run" >&2
  exit 1
fi
grep '^Test' "$work/all" | LC_ALL=C sort >"$work/listed"
for ((i = 1; i <= SHARDS; i++)); do
  awk -v n="$SHARDS" -v i="$i" 'NR % n == i % n' "$work/listed" >"$work/shard$i.list"
done

# Each listed test in exactly one shard.
dups="$(cat "$work"/shard*.list | LC_ALL=C sort | uniq -d | paste -sd' ')"
missing="$(cat "$work"/shard*.list | LC_ALL=C sort -u | LC_ALL=C comm -23 "$work/listed" - | paste -sd' ')"
if [[ -n "$dups$missing" ]]; then
  echo "shards do not partition the root's tests; in no shard: ${missing:-none}; in two: ${dups:-none}" >&2
  exit 1
fi

others=()
while read -r pkg; do others+=("$pkg"); done < <(go list ./... | grep -vxF "$(go list .)")
((${#others[@]})) || { echo "go list found no package besides the root" >&2; exit 1; }

# run NAME CMD... runs CMD, streaming its output prefixed with [NAME] and
# keeping it in $work/NAME.log; it returns CMD's exit status.
run() {
  local name=$1 start=$SECONDS s=0
  shift
  "$@" 2>&1 | tee "$work/$name.log" | sed -u "s/^/[$name] /" || s=$?
  echo "$name: exit $s, $((SECONDS - start)) s"
  return "$s"
}

names=() pids=()
for ((i = 1; i <= SHARDS; i++)); do
  echo "shard$i: $(wc -l <"$work/shard$i.list") root tests"
  pattern="^($(paste -sd'|' "$work/shard$i.list"))\$"
  run "shard$i" limit 26m "$work/root.test" -test.run "$pattern" \
    -test.v -test.count=1 -test.timeout 25m -test.paniconexit0 &
  names+=("shard$i") pids+=($!)
done
run others go test -p 4 -count=1 -timeout 25m -v "${others[@]}" &
names+=(others) pids+=($!)

failed=()
for k in "${!names[@]}"; do
  wait "${pids[k]}" || failed+=("${names[k]}")
  cat "$work/${names[k]}.log"
done >"$out/go-test.log"

status=0
if ((${#failed[@]})); then
  echo "failed: ${failed[*]}" >&2
  for name in "${failed[@]}"; do
    echo "== $name" >&2
    grep -E '^(--- FAIL|FAIL|panic:)' "$work/$name.log" >&2 || true
    echo "last test started: $(grep '^=== RUN' "$work/$name.log" | tail -n 1 || true)" >&2
    echo "last lines:" >&2
    tail -n 3 "$work/$name.log" >&2
  done
  status=1
fi
if grep -F 'no test database' "$out/go-test.log"; then
  echo "tests skipped without a database: Postgres was not reachable at ${DUDE_TEST_PG:-localhost:5433}" >&2
  status=1
fi
# A top-level result has no '/' in its name; a subtest's has. Unanchored, so
# a test's output without a final newline still leaves its result findable.
{ grep -ohE -- '--- (PASS|FAIL|SKIP): Test[^ /]+ \(' "$work"/shard*.log || true; } |
  awk '{print $3}' | LC_ALL=C sort >"$work/reported"
if ! cmp -s "$work/listed" "$work/reported"; then
  echo "root tests listed: $(wc -l <"$work/listed"); results reported: $(wc -l <"$work/reported")" >&2
  echo "listed without a result:" >&2
  LC_ALL=C comm -23 "$work/listed" <(LC_ALL=C sort -u "$work/reported") >&2
  echo "reported more than once:" >&2
  uniq -d "$work/reported" >&2
  status=1
fi
exit "$status"
