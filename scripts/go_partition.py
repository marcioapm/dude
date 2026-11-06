#!/usr/bin/env python3
"""go_partition.py: which machine and lane runs each Go test, for go-test.sh.

    go_partition.py split --machines M --lanes L [--whole PKG]... [--race PKG]... <tests
    go_partition.py check --machines M --lanes L [--whole PKG]... [--race PKG]... <partition
    go_partition.py union LISTED RAN...

The input of split is one `PACKAGE<TAB>TEST` line per top-level Test
function, in any order. The output is one line per test and variant:

    MACHINE  LANE  ORDER  VARIANT  WHOLE  PACKAGE  TEST

M machines (CI runners) times L lanes (test processes on each) make M*L
slots. A package is cut into chunks of tests, each chunk one test process
(ORDER is its place in its lane's queue): a package whose TestMain checks
the whole run (--whole) is one chunk run without -test.run (WHOLE is 1);
every other package is cut, round-robin by sorted test name, into as many
chunks as there are slots besides the whole packages' (or all, when slots
are few), and no chunk holds more than MAX_CHUNK tests,
so no process runs near go test's time limit. A --race package is cut the
same way once more for its run under the race detector (VARIANT race). Chunks go, largest
first, to the slot with the fewest tests so far among those holding no other
chunk of their package; ties break on names and slot numbers. So the result is a pure function of the set of tests, M, L
and the two package lists: every machine computes the same partition and
runs its own slice of it.

check reads split's output and fails unless every test is in exactly one
slot per variant, every chunk sits in one slot, and no --whole package is
cut, under either variant. union fails unless the RAN files (each a sorted `VARIANT PACKAGE TEST`
list of the results one machine saw) together hold LISTED's lines exactly
once: it is how CI's go job proves that its runners, between them, ran every
test once.
"""

from __future__ import annotations

import argparse
import sys
from collections import Counter, defaultdict
from collections.abc import Iterable

# A chunk is one test process with one -test.timeout. The root package's
# tests average about 2.4 s each on CI; 300 of them keep a process near
# 12 minutes even with lanes=1.
MAX_CHUNK = 300
# Each test process migrates its own template database (a second or two);
# a chunk smaller than this costs more in setup than it saves.
MIN_CHUNK = 20


def chunks(tests: dict[str, list[str]], slots: int, whole: set[str], race: set[str]):
    """(variant, whole, package, tests) per chunk, deterministic."""
    out = []
    # A package run whole holds its slot for long (agenttools: 285 tests, the
    # slowest process on CI), so a cut package is cut for the other slots.
    whole_here = len(whole & tests.keys())
    room = slots - whole_here if slots > 2 * whole_here else slots
    for pkg in sorted(tests):
        names = sorted(set(tests[pkg]))
        for variant in ("-", "race") if pkg in race else ("-",):
            if pkg in whole:
                out.append((variant, True, pkg, names))
                continue
            k = max(-(-len(names) // MAX_CHUNK), min(room, len(names) // MIN_CHUNK), 1)
            for i in range(min(k, len(names))):
                out.append((variant, False, pkg, names[i::k]))
    return out


def split(rows: Iterable[tuple[str, str]], machines: int, lanes: int,
          whole: Iterable[str] = (), race: Iterable[str] = ()) -> list[tuple]:
    """Rows of (machine, lane, order, variant, whole, package, test), sorted."""
    if machines < 1 or lanes < 1:
        raise ValueError("machines and lanes must be at least 1")
    tests: dict[str, list[str]] = defaultdict(list)
    for pkg, test in rows:
        tests[pkg].append(test)
    slots = machines * lanes
    load = [0] * slots
    queue: list[list] = [[] for _ in range(slots)]
    # Weighed by test count, largest chunk first, so a package run whole
    # (agenttools: 285 tests) takes a slot before the cut ones fill in. A
    # chunk avoids slots holding a chunk of its own package and variant while
    # any is left: the root package's tests are slower than the rest (about
    # 1.4 s each against 0.3 on 8 lanes), and two root chunks in one lane
    # made it the last by minutes.
    used: dict[tuple, set[int]] = defaultdict(set)
    for c in sorted(chunks(tests, slots, set(whole), set(race)),
                    key=lambda c: (-len(c[3]), c[2], c[0], c[3])):
        mine = used[(c[0], c[2])]
        free = [s for s in range(slots) if s not in mine] or list(range(slots))
        s = min(free, key=lambda s: (load[s], s))
        mine.add(s)
        load[s] += len(c[3])
        queue[s].append(c)
    out = []
    for s in range(slots):
        for order, (variant, w, pkg, names) in enumerate(queue[s], 1):
            for test in names:
                out.append((s % machines + 1, s // machines + 1, order, variant, int(w), pkg, test))
    out.sort()
    return out


def check(rows: list[tuple], listed: Iterable[tuple[str, str]], machines: int, lanes: int,
          whole: Iterable[str] = (), race: Iterable[str] = ()) -> list[str]:
    """Problems with a partition; empty when it is sound."""
    problems = []
    want = Counter((("-", p, t) for p, t in set(listed)))
    want.update(("race", p, t) for p, t in set(listed) if p in set(race))
    got = Counter((r[3], r[5], r[6]) for r in rows)
    for key in sorted(want.keys() | got.keys()):
        if got[key] != want[key]:
            problems.append(f"{' '.join(key)}: in {got[key]} slots, want {want[key]}")
    slots_of = defaultdict(set)
    for m, lane, order, variant, w, pkg, _ in rows:
        if not (1 <= m <= machines and 1 <= lane <= lanes):
            problems.append(f"{pkg}: slot {m}/{lane} outside {machines} machines x {lanes} lanes")
        slots_of[(variant, pkg)].add((m, lane, order))
        if pkg in set(whole) and not w:
            problems.append(f"{pkg}: a whole-run package run with -test.run")
    for (variant, pkg), where in sorted(slots_of.items()):
        if pkg in set(whole) and len(where) != 1:
            problems.append(f"{variant} {pkg}: runs whole, yet cut into {len(where)} processes")
    return problems


def union(listed: list[str], ran: list[list[str]]) -> list[str]:
    want = Counter(listed)
    got = Counter(line for r in ran for line in r)
    return [f"{line}: ran {got[line]} times, want {want[line]}"
            for line in sorted(want.keys() | got.keys()) if got[line] != want[line]]


def lines(f) -> list[str]:
    return [l.rstrip("\n") for l in f if l.strip()]


def main(argv: list[str]) -> int:
    ap = argparse.ArgumentParser(description=__doc__.split("\n")[0])
    sub = ap.add_subparsers(dest="cmd", required=True)
    for name in ("split", "check"):
        p = sub.add_parser(name)
        p.add_argument("--machines", type=int, required=True)
        p.add_argument("--lanes", type=int, required=True)
        p.add_argument("--whole", action="append", default=[])
        p.add_argument("--race", action="append", default=[])
        if name == "check":
            p.add_argument("--listed", required=True, help="the PACKAGE<TAB>TEST list split was given")
    u = sub.add_parser("union")
    u.add_argument("listed")
    u.add_argument("ran", nargs="*")
    a = ap.parse_args(argv)
    if a.cmd == "split":
        rows = [tuple(l.split("\t")) for l in lines(sys.stdin)]
        for r in split(rows, a.machines, a.lanes, a.whole, a.race):
            print("\t".join(map(str, r)))
        return 0
    if a.cmd == "check":
        with open(a.listed) as f:
            listed = [tuple(l.split("\t")) for l in lines(f)]
        rows = []
        for l in lines(sys.stdin):
            m, lane, order, variant, w, pkg, test = l.split("\t")
            rows.append((int(m), int(lane), int(order), variant, int(w), pkg, test))
        problems = check(rows, listed, a.machines, a.lanes, a.whole, a.race)
    else:
        with open(a.listed) as f:
            listed = lines(f)
        ran = []
        for path in a.ran:
            with open(path) as f:
                ran.append(lines(f))
        problems = union(listed, ran)
    for p in problems:
        print(p, file=sys.stderr)
    return 1 if problems else 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
