#!/usr/bin/env bash
# go-test.sh runs every Go test in orchestrator/, here or as one of CI's go
# runners:
#
#   [GO_TEST_LANES=N] [GO_TEST_MACHINE=I/M] scripts/go-test.sh
#   [GO_TEST_LANES=N] [GO_TEST_MACHINE=I/M] scripts/go-test.sh --partition
#
# Tests within one process run one at a time and spend most of it waiting on
# Postgres, so the script runs many test processes at once: N lanes (default
# the CPU count), each a queue of test processes run one after another. Every
# test package is compiled once and its Test functions listed;
# scripts/go_partition.py cuts each package into chunks, round-robin by
# sorted name, and places every chunk in one lane of one of M machines. A
# package whose TestMain checks the whole run (cmd/dude-cli, agenttools) is
# never cut: it runs as one process without -test.run. forge and phases also
# run once more, whole, under the race detector, as a chunk of their own.
# Each test gets its own database clone (internal/dbtest), so processes do
# not share state. The partition depends only on the listed tests, M and N:
# CI's runners, given the same GO_TEST_LANES, compute the same one and each
# runs slice I. --partition prints it (machine, lane, order, variant, whole,
# package, test) and runs nothing.
#
# The run fails when any process fails, when a test skips for want of a
# database (DUDE_TEST_PG, default localhost:5433), or when a test of this
# slice has no top-level result line or more than one. Output streams live,
# each line prefixed with its lane ([lane3]); every process's output goes, one
# after the other, to $RUNNER_TEMP/go-test.log, the partition to
# go-test-partition.tsv and the results seen to go-test-ran.txt beside it,
# from which CI checks that its runners together ran every test once.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cpus() { nproc 2>/dev/null || getconf _NPROCESSORS_ONLN; }
LANES="${GO_TEST_LANES:-$(cpus)}"
MACHINE="${GO_TEST_MACHINE:-1/1}"
[[ $LANES =~ ^[1-9][0-9]*$ ]] || { echo "GO_TEST_LANES must be a positive integer, not '$LANES'" >&2; exit 1; }
[[ $MACHINE =~ ^([1-9][0-9]*)/([1-9][0-9]*)$ ]] && ((BASH_REMATCH[1] <= BASH_REMATCH[2])) ||
  { echo "GO_TEST_MACHINE must be I/M with 1 <= I <= M, not '$MACHINE'" >&2; exit 1; }
I=${BASH_REMATCH[1]} M=${BASH_REMATCH[2]}
PRINT=0
case "${1:-}" in --partition) PRINT=1 ;; "") ;; *) echo "usage: $0 [--partition]" >&2; exit 2 ;; esac
# A TestMain that checks properties of the whole run: cut, it would skip the check.
WHOLE=(cmd/dude-cli internal/agenttools)
# forge's concurrency tests assert only under the race detector; phases runs
# its followers on goroutines of their own.
RACE=(internal/forge internal/phases)
partition_args=(--machines "$M" --lanes "$LANES")
for p in "${WHOLE[@]}"; do partition_args+=(--whole "$p"); done
for p in "${RACE[@]}"; do partition_args+=(--race "$p"); done

cd "$ROOT/orchestrator"
out="${RUNNER_TEMP:-$(mktemp -d)}"
work="$(mktemp -d "$out/go-test.XXXXXX")"
trap 'rm -rf "$work/bin" "$work/race"' EXIT
log() { if ((PRINT)); then echo "$@" >&2; else echo "$@"; fi; }
log "cpus: $(cpus); machine $I of $M; lanes: $LANES; logs: $work"

# go test kills a test binary a minute after -timeout, with SIGQUIT for a
# stack dump. Run directly, the binary's own alarm covers only the tests,
# not TestMain's work around them (dbtest.Main's setup and cleanup), so
# limit DURATION CMD... does the killing; without timeout(1) (stock macOS)
# it just runs CMD.
limit() {
  if command -v timeout >/dev/null; then timeout -s QUIT -k 1m "$@"; else "${@:2}"; fi
}

# Packages by path relative to orchestrator/ ("." for the root), with the
# directory a test binary runs in, as go test runs it.
module="$(go list -m)"
go list -f '{{if or .TestGoFiles .XTestGoFiles}}{{.ImportPath}}{{"\t"}}{{.Dir}}{{end}}' ./... |
  sed -e "s|^$module\t|.\t|" -e "s|^$module/||" >"$work/packages"
[[ -s $work/packages ]] || { echo "go list found no test package" >&2; exit 1; }
# Binaries are named after the last path element; two alike would collide.
if cut -f1 "$work/packages" | sed 's|.*/||' | LC_ALL=C sort | uniq -d | grep . >&2; then
  echo "test packages above share a last path element, so their test binaries' names" >&2
  exit 1
fi
bin() { if [[ $1 == . ]]; then echo "$2/orchestrator.test"; else echo "$2/${1##*/}.test"; fi; }

start=$SECONDS
go test -c -o "$work/bin/" ./... >&2
log "compiled $(wc -l <"$work/packages") test binaries in $((SECONDS - start)) s"

# List each package's top-level tests. -test.run matching nothing keeps the
# whole-run TestMains from checking a run that did not happen.
: >"$work/listed.tsv"
while IFS=$'\t' read -r pkg dir; do
  (cd "$dir" && limit 5m "$(bin "$pkg" "$work/bin")" -test.list . -test.run '^$') >"$work/list" ||
    { echo "listing $pkg's tests failed or ran out of time (exit $?)" >&2; exit 1; }
  whole=0
  for p in "${WHOLE[@]}"; do [[ $p == "$pkg" ]] && whole=1; done
  # A cut package runs Test functions only, so an Example or a Fuzz seed would go unrun.
  if ((!whole)) && grep -E '^(Example|Fuzz)' "$work/list" >&2; then
    echo "$pkg has the functions above, which this script runs only in a package it does not cut" >&2
    exit 1
  fi
  grep '^Test' "$work/list" | sed "s|^|$pkg\t|" >>"$work/listed.tsv" || true
done <"$work/packages"

python3 "$ROOT/scripts/go_partition.py" split "${partition_args[@]}" <"$work/listed.tsv" >"$work/partition.tsv"
python3 "$ROOT/scripts/go_partition.py" check "${partition_args[@]}" --listed "$work/listed.tsv" <"$work/partition.tsv" ||
  { echo "the partition does not place every test exactly once" >&2; exit 1; }
cp "$work/partition.tsv" "$out/go-test-partition.tsv"
if ((PRINT)); then
  cat "$work/partition.tsv"
  exit 0
fi
log "listed $(wc -l <"$work/listed.tsv") tests in $(cut -f1 "$work/listed.tsv" | LC_ALL=C sort -u | wc -l) packages;" \
  "this machine runs $(awk -F'\t' -v m="$I" '$1 == m' "$work/partition.tsv" | wc -l) of $(wc -l <"$work/partition.tsv") (test, variant) pairs"

# The race binaries, only on the machine that runs them.
if awk -F'\t' -v m="$I" '$1 == m && $4 == "race" {f = 1} END {exit !f}' "$work/partition.tsv"; then
  start=$SECONDS
  race_pkgs=()
  for p in "${RACE[@]}"; do race_pkgs+=("./$p"); done
  go test -race -c -o "$work/race/" "${race_pkgs[@]}" >&2
  log "compiled the race binaries in $((SECONDS - start)) s"
fi

# One directory per chunk this machine runs: chunks/LANE/ORDER/{meta,tests}.
awk -F'\t' -v m="$I" -v w="$work/chunks" '$1 == m {
    d = w "/" $2 "/" sprintf("%04d", $3)
    if (!(d in seen)) { seen[d] = 1; system("mkdir -p " d); print $4 "\t" $5 "\t" $6 > (d "/meta") }
    print $7 > (d "/tests")
  }' "$work/partition.tsv"

# chunk LANE DIR runs one test process and prints its own log's result
# lines into DIR/ran as VARIANT PACKAGE TEST.
chunk() {
  local lane=$1 d=$2 variant whole pkg dir b name s=0 start=$SECONDS
  IFS=$'\t' read -r variant whole pkg <"$d/meta"
  name=$pkg
  [[ $variant == race ]] && name+=" (race)"
  dir="$(awk -F'\t' -v p="$pkg" '$1 == p {print $2}' "$work/packages")"
  if [[ $variant == race ]]; then b="$(bin "$pkg" "$work/race")"; else b="$(bin "$pkg" "$work/bin")"; fi
  local args=(-test.v -test.count=1 -test.timeout 25m -test.paniconexit0)
  ((whole)) || args+=(-test.run "^($(paste -sd'|' "$d/tests"))\$")
  echo "== lane$lane: $name, $(wc -l <"$d/tests") tests" >"$d/log"
  (cd "$dir" && limit 26m "$b" "${args[@]}") 2>&1 | tee -a "$d/log" | sed -u "s/^/[lane$lane] /" || s=$?
  echo "== lane$lane: $name: exit $s, $((SECONDS - start)) s" | tee -a "$d/log"
  # A top-level result has no '/' in its name; a subtest's has. Unanchored,
  # so a test's output without a final newline still leaves its result findable.
  { grep -ohE -- '--- (PASS|FAIL|SKIP): Test[^ /]+ \(' "$d/log" || true; } |
    awk -v v="$variant" -v p="$pkg" '{print v " " p " " $3}' >"$d/ran"
  return "$s"
}

# lane LANE runs its chunks in order, every one even after a failure, and
# fails when any did.
lane() {
  local l=$1 s=0 start=$SECONDS d
  for d in "$work/chunks/$l"/*/; do
    chunk "$l" "${d%/}" || { s=1; touch "${d%/}/failed"; }
  done
  echo "lane$l: exit $s, $((SECONDS - start)) s"
  return "$s"
}

lanes=() pids=()
for d in "$work/chunks"/*/; do
  l="$(basename "$d")"
  echo "lane$l: $(cat "$d"/*/tests | wc -l) tests in $(ls "$d" | wc -l) processes"
  lane "$l" &
  lanes+=("$l") pids+=($!)
done
failed=()
for k in "${!lanes[@]}"; do wait "${pids[k]}" || failed+=("${lanes[k]}"); done

for d in "$work/chunks"/*/*/; do cat "$d/log"; done >"$out/go-test.log"
cat "$work/chunks"/*/*/ran 2>/dev/null | LC_ALL=C sort >"$out/go-test-ran.txt"

status=0
if ((${#failed[@]})); then
  echo "failed: ${failed[*]/#/lane}" >&2
  for l in "${failed[@]}"; do
    for d in "$work/chunks/$l"/*/; do
      [[ -e $d/failed ]] || continue
      echo "$(head -n 1 "$d/log")" >&2
      grep -E '^(--- FAIL|FAIL|panic:)' "$d/log" >&2 || true
      echo "last test started: $(grep '^=== RUN' "$d/log" | tail -n 1 || true)" >&2
      echo "last lines:" >&2
      tail -n 4 "$d/log" >&2
    done
  done
  status=1
fi
if grep -F 'no test database' "$out/go-test.log"; then
  echo "tests skipped without a database: Postgres was not reachable at ${DUDE_TEST_PG:-localhost:5433}" >&2
  status=1
fi
awk -F'\t' -v m="$I" '$1 == m {print $4 " " $6 " " $7}' "$work/partition.tsv" | LC_ALL=C sort >"$work/expected"
if ! cmp -s "$work/expected" "$out/go-test-ran.txt"; then
  echo "tests in this slice: $(wc -l <"$work/expected"); results reported: $(wc -l <"$out/go-test-ran.txt")" >&2
  echo "listed without a result:" >&2
  LC_ALL=C comm -23 "$work/expected" <(LC_ALL=C sort -u "$out/go-test-ran.txt") >&2
  echo "reported but not listed, or more than once:" >&2
  { LC_ALL=C comm -13 "$work/expected" <(LC_ALL=C sort -u "$out/go-test-ran.txt"); uniq -d "$out/go-test-ran.txt"; } >&2
  status=1
fi
exit "$status"
