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
# listed root test has no top-level result line. Every process's output
# goes, one after the other, to $RUNNER_TEMP/go-test.log and stdout.
set -euo pipefail

SHARDS="${SHARDS:-2}"
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT/orchestrator"
out="${RUNNER_TEMP:-$(mktemp -d)}"
work="$(mktemp -d "$out/go-test.XXXXXX")"
trap 'rm -f "$work/root.test"' EXIT
echo "nproc: $(nproc); root shards: $SHARDS; logs: $work"

go test -c -o "$work/root.test" .
"$work/root.test" -test.list . >"$work/all"
# Shards run Test functions only, so an Example or a Fuzz seed would go unrun.
if grep -E '^(Example|Fuzz)' "$work/all" >&2; then
  echo "the root package has the functions above, which this script does not run" >&2
  exit 1
fi
grep '^Test' "$work/all" | LC_ALL=C sort >"$work/listed"
for ((i = 0; i < SHARDS; i++)); do
  awk -v n="$SHARDS" -v i="$i" 'NR % n == i' "$work/listed" >"$work/shard$i"
done

# Each listed test in exactly one shard.
dups="$(cat "$work"/shard* | LC_ALL=C sort | uniq -d | paste -sd' ')"
missing="$(cat "$work"/shard* | LC_ALL=C sort -u | LC_ALL=C comm -23 "$work/listed" - | paste -sd' ')"
if [[ -n "$dups$missing" ]]; then
  echo "shards do not partition the root's tests; in no shard: ${missing:-none}; in two: ${dups:-none}" >&2
  exit 1
fi

others=()
while read -r pkg; do others+=("$pkg"); done < <(go list ./... | grep -vxF "$(go list .)")

declare -A pids
for ((i = 0; i < SHARDS; i++)); do
  pattern="^($(paste -sd'|' "$work/shard$i"))\$"
  (start=$SECONDS; s=0
    "$work/root.test" -test.run "$pattern" -test.v -test.count=1 -test.timeout 25m >"$work/shard$i.log" 2>&1 || s=$?
    echo "root shard $((i + 1))/$SHARDS: $(wc -l <"$work/shard$i") tests, exit $s, $((SECONDS - start)) s"
    exit "$s") &
  pids[shard$i]=$!
done
(start=$SECONDS; s=0
  go test -p 4 -count=1 -timeout 25m -v "${others[@]}" >"$work/others.log" 2>&1 || s=$?
  echo "other packages: exit $s, $((SECONDS - start)) s"
  exit "$s") &
pids[others]=$!

failed=()
for name in "${!pids[@]}"; do
  wait "${pids[$name]}" || failed+=("$name")
done

for ((i = 0; i < SHARDS; i++)); do cat "$work/shard$i.log"; done | cat - "$work/others.log" | tee "$out/go-test.log"

status=0
if ((${#failed[@]})); then
  echo "failed: ${failed[*]}" >&2
  for name in "${failed[@]}"; do
    grep -E '^(--- FAIL|FAIL|panic:)' "$work/$name.log" >&2 || true
    echo "$name ended with:" >&2
    tail -n 3 "$work/$name.log" >&2
  done
  status=1
fi
if grep -F 'no test database' "$out/go-test.log"; then
  echo "tests skipped without a database: Postgres was not reachable at ${DUDE_TEST_PG:-localhost:5433}" >&2
  status=1
fi
# A top-level result has no '/' in its name; a subtest's has.
grep -ohE -- '^--- (PASS|FAIL|SKIP): Test[^ /]+ \(' "$work"/shard*.log | awk '{print $3}' | LC_ALL=C sort >"$work/reported"
if ! cmp -s "$work/listed" "$work/reported"; then
  echo "root tests listed: $(wc -l <"$work/listed"); results reported: $(wc -l <"$work/reported")" >&2
  echo "listed without a result:" >&2
  LC_ALL=C comm -23 "$work/listed" <(LC_ALL=C sort -u "$work/reported") >&2
  echo "reported more than once:" >&2
  uniq -d "$work/reported" >&2
  status=1
fi
exit "$status"
