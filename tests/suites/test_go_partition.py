"""scripts/go_partition.py: how scripts/go-test.sh splits the Go tests over
CI's runners (machines) and each one's test processes (lanes).

Pure checks; they need no environment:

    uv run --directory tests pytest suites/test_go_partition.py
"""
import importlib.util
import subprocess
import sys
from collections import Counter
from pathlib import Path

import pytest

SCRIPT = Path(__file__).resolve().parents[2] / "scripts" / "go_partition.py"
spec = importlib.util.spec_from_file_location("go_partition", SCRIPT)
gp = importlib.util.module_from_spec(spec)
spec.loader.exec_module(gp)

WHOLE = ["cmd/dude-cli", "internal/agenttools"]
RACE = ["internal/forge", "internal/phases"]
SIZES = {".": 1709, "internal/delivery": 592, "internal/phases": 471, "internal/agenttools": 285,
         "internal/chat": 173, "internal/forge": 121, "cmd/dude-cli": 95, "internal/images": 73,
         "internal/servers": 43, "internal/prs": 2, "internal/capabilities": 2, "internal/refusalgen": 1}
LISTED = [(pkg, f"Test{pkg.replace('/', '_').replace('.', 'Root')}{i:04d}") for pkg, n in SIZES.items() for i in range(n)]


def split(m, lanes, listed=LISTED):
    return gp.split(listed, m, lanes, WHOLE, RACE)


@pytest.mark.parametrize("m,lanes", [(1, 1), (1, 4), (1, 8), (1, 12), (3, 4), (4, 4), (5, 4), (8, 6), (40, 4)])
def test_every_test_lands_in_exactly_one_slot_per_variant(m, lanes):
    rows = split(m, lanes)
    assert gp.check(rows, LISTED, m, lanes, WHOLE, RACE) == []
    plain = Counter((r[5], r[6]) for r in rows if r[3] == "-")
    assert plain == Counter(LISTED)
    race = Counter((r[5], r[6]) for r in rows if r[3] == "race")
    assert race == Counter(t for t in LISTED if t[0] in RACE)
    slots = {(r[0], r[1]) for r in rows}
    assert all(1 <= a <= m and 1 <= b <= lanes for a, b in slots)


def test_the_split_does_not_depend_on_the_order_tests_come_in():
    assert split(4, 4) == split(4, 4, list(reversed(LISTED)))
    assert split(4, 4) == split(4, 4, LISTED[1::2] + LISTED[::2])


def test_one_machine_with_one_lane_runs_everything_in_one_place():
    rows = split(1, 1)
    assert {(r[0], r[1]) for r in rows} == {(1, 1)}
    assert len(rows) == len(LISTED) + sum(SIZES[p] for p in RACE)


def test_one_machine_runs_every_test_whatever_its_lanes():
    for lanes in (1, 3, 8):
        assert {r[0] for r in split(1, lanes)} == {1}


@pytest.mark.parametrize("m,lanes", [(1, 8), (4, 4), (8, 6)])
def test_whole_run_packages_are_never_cut(m, lanes):
    rows = split(m, lanes)
    for pkg in WHOLE:
        where = {(r[0], r[1], r[2]) for r in rows if r[5] == pkg}
        assert len(where) == 1, pkg
        assert all(r[4] == 1 for r in rows if r[5] == pkg)


def test_large_packages_are_cut_across_lanes():
    rows = split(1, 8)
    for pkg, variant in ((".", "-"), ("internal/delivery", "-"), ("internal/phases", "-"),
                         ("internal/chat", "-"), ("internal/phases", "race")):
        processes = {(r[1], r[2]) for r in rows if r[5] == pkg and r[3] == variant}
        assert len(processes) == 8, pkg
        assert len({lane for lane, _ in processes}) > 1, pkg


def test_lanes_carry_about_the_same_number_of_tests():
    for m, lanes in ((1, 8), (4, 4)):
        per = Counter((r[0], r[1]) for r in split(m, lanes))
        assert len(per) == m * lanes
        assert max(per.values()) - min(per.values()) <= gp.MAX_CHUNK, (m, lanes, per)


def test_round_robin_by_sorted_name():
    rows = split(1, 8)
    root = sorted(t for p, t in LISTED if p == ".")
    lane_of = {r[6]: r[1] for r in rows if r[5] == "." and r[3] == "-"}
    for i, t in enumerate(root):
        assert lane_of[t] == lane_of[root[i % 8]]


def test_machines_carry_about_the_same_number_of_tests():
    rows = split(4, 4)
    per = Counter(r[0] for r in rows)
    assert max(per.values()) - min(per.values()) <= max(SIZES[p] for p in WHOLE)


def test_check_catches_a_dropped_a_doubled_and_a_cut_whole_test():
    rows = split(2, 3)
    assert gp.check(rows[1:], LISTED, 2, 3, WHOLE, RACE)
    assert gp.check(rows + rows[:1], LISTED, 2, 3, WHOLE, RACE)
    cut = [(r[0], r[1], r[2] + (r[6].endswith("1") and r[5] == WHOLE[0]), *r[3:]) for r in rows]
    assert gp.check(cut, LISTED, 2, 3, WHOLE, RACE)


def test_union_proves_the_machines_ran_every_test_once():
    listed = ["- . TestA", "- . TestB", "race internal/forge TestC"]
    assert gp.union(listed, [["- . TestA"], ["- . TestB", "race internal/forge TestC"]]) == []
    assert gp.union(listed, [["- . TestA"], ["- . TestB"]])
    assert gp.union(listed, [["- . TestA", "- . TestB"], ["- . TestB", "race internal/forge TestC"]])


def test_the_command_line_round_trips(tmp_path):
    listed = tmp_path / "listed.tsv"
    listed.write_text("".join(f"{p}\t{t}\n" for p, t in LISTED))
    args = ["--machines", "3", "--lanes", "4"] + [x for p in WHOLE for x in ("--whole", p)] + \
        [x for p in RACE for x in ("--race", p)]
    out = subprocess.run([sys.executable, SCRIPT, "split", *args], input=listed.read_text(),
                         capture_output=True, text=True, check=True).stdout
    subprocess.run([sys.executable, SCRIPT, "check", *args, "--listed", listed], input=out,
                   text=True, check=True)
    short = "".join(out.splitlines(keepends=True)[1:])
    bad = subprocess.run([sys.executable, SCRIPT, "check", *args, "--listed", listed], input=short,
                         capture_output=True, text=True)
    assert bad.returncode == 1 and "in 0 slots, want 1" in bad.stderr
