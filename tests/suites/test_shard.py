"""tests/shard.py: how run_tests.py --shard splits the suites.

Pure checks; they need no environment:

    uv run --directory tests pytest suites/test_shard.py
"""
import json

import pytest

import shard

SUITES = [f"suites/test_{c}.py" for c in "abcdefghij"]
SECONDS = {"suites/test_a.py": 300, "suites/test_b.py": 120, "suites/test_c.py": 90, "suites/test_d.py": 60,
           "suites/test_e.py": 5, "suites/test_f.py": 1}


@pytest.mark.parametrize("m", [1, 2, 3, 6, 10, 13])
def test_every_suite_lands_in_exactly_one_shard(m):
    shards = shard.split(SUITES, m, SECONDS)
    assert len(shards) == m
    placed = [s for b in shards for s in b]
    assert sorted(placed) == sorted(SUITES)


def test_the_split_does_not_depend_on_the_order_suites_or_durations_come_in():
    a = shard.split(SUITES, 4, SECONDS)
    b = shard.split(list(reversed(SUITES)), 4, dict(reversed(list(SECONDS.items()))))
    assert a == b


def test_the_longest_suite_gets_a_shard_to_itself_when_it_outweighs_the_rest():
    shards = shard.split(SUITES, 2, {**SECONDS, "suites/test_a.py": 10_000})
    assert ["suites/test_a.py"] in shards


def test_a_suite_with_no_recorded_duration_weighs_30_seconds():
    # Weighed at 30 s the new suite balances the two 20 s ones; weighed at 0 or 1 s
    # it would join one of them.
    suites = ["suites/test_a.py", "suites/test_b.py", "suites/test_new.py"]
    shards = shard.split(suites, 2, {"suites/test_a.py": 20, "suites/test_b.py": 20})
    assert sorted(shards) == [["suites/test_a.py", "suites/test_b.py"], ["suites/test_new.py"]]


def test_shards_are_close_in_recorded_time():
    seconds = {s: float(i * 7 % 11 + 1) for i, s in enumerate(SUITES)}
    loads = [sum(seconds[s] for s in b) for b in shard.split(SUITES, 3, seconds)]
    # Greedy into the lightest bin: no shard exceeds another by more than its largest suite.
    assert max(loads) - min(loads) <= max(seconds.values())


@pytest.mark.parametrize("spec,want", [("1/1", (1, 1)), ("3/6", (3, 6)), ("6/6", (6, 6))])
def test_a_shard_is_n_of_m(spec, want):
    assert shard.parse_shard(spec) == want


@pytest.mark.parametrize("spec", ["0/6", "7/6", "-1/6", "1/0", "2/1"])
def test_a_shard_out_of_range_is_refused(spec):
    with pytest.raises(SystemExit, match="need 1 <= N <= M"):
        shard.parse_shard(spec)


@pytest.mark.parametrize("spec", ["", "3", "a/b", "1/2/3", "1-6"])
def test_a_shard_that_is_not_n_of_m_is_refused(spec):
    with pytest.raises(SystemExit, match="expected N/M"):
        shard.parse_shard(spec)


def test_every_suite_on_disk_is_in_some_shard_whatever_the_durations_file_says(tmp_path):
    (tmp_path / "suites").mkdir()
    for name in ("test_a.py", "test_new.py", "helpers.py"):
        (tmp_path / "suites" / name).write_text("")
    durations = tmp_path / "seconds.json"
    durations.write_text(json.dumps({"suites/test_a.py": 50, "suites/test_gone.py": 99}))
    suites = shard.all_suites(tmp_path)
    assert suites == ["suites/test_a.py", "suites/test_new.py"]
    placed = [s for b in shard.split(suites, 3, shard.suite_seconds(durations)) for s in b]
    assert sorted(placed) == suites


def test_no_durations_file_means_no_recorded_durations(tmp_path):
    assert shard.suite_seconds(tmp_path / "missing.json") == {}


@pytest.mark.parametrize("args", [
    ["--junitxml", "/tmp/r.xml"], ["--junitxml=/tmp/r.xml"], ["--basetemp", "/tmp"], ["--junitxml", "suites-report.xml"],
    ["-k", "chat"], ["-x", "-q"], [],
])
def test_option_values_are_not_suites_named_beside_a_shard(args):
    assert shard.shard_suites("2/3", args, SUITES, SECONDS) == shard.split(SUITES, 3, SECONDS)[1]


@pytest.mark.parametrize("arg", ["suites", "suites/", "./suites", "./suites/", str(shard.TESTS_DIR / "suites"),
                                 "../tests/suites", f"{shard.TESTS_DIR}/../tests/suites", f"{shard.TESTS_DIR}/../../{shard.TESTS_DIR.parent.name}/tests/suites",
                                 "suites/test_a.py", "suites/test_a.py::test_x", "test_a.py"])
def test_a_suite_named_beside_a_shard_is_refused(arg):
    with pytest.raises(SystemExit, match="picks its own suites"):
        shard.shard_suites("1/2", ["-x", arg], SUITES, SECONDS)


def test_shard_n_is_the_nth_share():
    shares = shard.split(SUITES, 4, SECONDS)
    assert [shard.shard_suites(f"{n}/4", [], SUITES, SECONDS) for n in (1, 2, 3, 4)] == shares


def test_seconds_are_summed_per_suite_file_from_a_junit_report(tmp_path):
    report = tmp_path / "r.xml"
    report.write_text(
        '<testsuites><testsuite>'
        '<testcase classname="suites.test_a" name="x" time="1.5"/>'
        '<testcase classname="suites.test_a.TestThing" name="y" time="2"/>'
        '<testcase classname="suites.test_b" name="z" time="0.5"/>'
        '</testsuite></testsuites>')
    assert shard.seconds_from_junit(report) == {"suites/test_a.py": 3.5, "suites/test_b.py": 0.5}


def test_seconds_add_up_across_several_junit_reports(tmp_path):
    one, two = tmp_path / "1.xml", tmp_path / "2.xml"
    one.write_text('<testsuite><testcase classname="suites.test_a" name="x" time="1"/></testsuite>')
    two.write_text('<testsuite><testcase classname="suites.test_a" name="y" time="2"/>'
                   '<testcase classname="suites.test_b" name="z" time="4"/></testsuite>')
    assert shard.seconds_from_junits([one, two]) == {"suites/test_a.py": 3.0, "suites/test_b.py": 4.0}
