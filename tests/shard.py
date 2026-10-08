"""Splitting the E2E suites into shards that take about as long as each other.

A shard is a set of whole suite files: a suite's session fixtures (the web
app, the gallery) start once per shard that has it, and every shard gets its
own environment from run_tests.py.

Refresh the recorded durations from JUnit reports that together cover every
suite, e.g. one per shard (each run is shorter than the whole suite at once):

    uv run python run_tests.py --shard 1/6 --junitxml=/tmp/dude-e2e-1.xml
    ...
    uv run python shard.py /tmp/dude-e2e-*.xml
"""

from __future__ import annotations

import json
import sys
import xml.etree.ElementTree as ET
from collections import defaultdict
from collections.abc import Mapping
from pathlib import Path

TESTS_DIR = Path(__file__).resolve().parent
SUITE_SECONDS_FILE = TESTS_DIR / ".suite-seconds.json"
# A suite with no recorded duration: a new one, typically. Weighted like a
# mid-sized suite so it neither swamps a shard nor lands on one for free.
DEFAULT_SECONDS = 30.0


def all_suites(tests_dir: Path = TESTS_DIR) -> list[str]:
    return sorted(str(p.relative_to(tests_dir)) for p in (tests_dir / "suites").glob("test_*.py"))


def suite_seconds(path: Path = SUITE_SECONDS_FILE) -> dict[str, float]:
    return json.loads(path.read_text()) if path.exists() else {}


def parse_shard(spec: str) -> tuple[int, int]:
    """`N/M` as (N, M); SystemExit unless 1 <= N <= M."""
    try:
        n, m = (int(x) for x in spec.split("/"))
    except ValueError:
        raise SystemExit(f"--shard {spec}: expected N/M, e.g. 2/6")
    if not 1 <= n <= m:
        raise SystemExit(f"--shard {spec}: need 1 <= N <= M")
    return n, m


def split(suites: list[str], m: int, seconds: Mapping[str, float],
          default: float = DEFAULT_SECONDS) -> list[list[str]]:
    """M shards of suites, longest first into the lightest shard.

    Deterministic for the same inputs: ties break on the suite's name and the
    shard's index, never on dict or set order. Each shard is sorted.
    """
    bins: list[list[str]] = [[] for _ in range(m)]
    load = [0.0] * m
    for suite in sorted(suites, key=lambda s: (-seconds.get(s, default), s)):
        i = min(range(m), key=lambda j: (load[j], j))
        bins[i].append(suite)
        load[i] += seconds.get(suite, default)
    return [sorted(b) for b in bins]


def seconds_from_junit(xml_path: Path) -> dict[str, float]:
    """Seconds per suite file from pytest's JUnit report (`classname` is
    `suites.test_x` or `suites.test_x.TestClass`)."""
    totals: dict[str, float] = defaultdict(float)
    for case in ET.parse(xml_path).getroot().iter("testcase"):
        parts = case.get("classname", "").split(".")
        if len(parts) >= 2 and parts[0] == "suites":
            totals[f"suites/{parts[1]}.py"] += float(case.get("time", "0"))
    return {k: round(v, 1) for k, v in sorted(totals.items())}


def seconds_from_junits(xml_paths: list[Path]) -> dict[str, float]:
    """seconds_from_junit over several reports, e.g. one per group of suites."""
    totals: dict[str, float] = defaultdict(float)
    for p in xml_paths:
        for suite, s in seconds_from_junit(p).items():
            totals[suite] += s
    return {k: round(v, 1) for k, v in sorted(totals.items())}


if __name__ == "__main__":
    if len(sys.argv) < 2:
        sys.exit("usage: shard.py JUNIT_XML...  (writes .suite-seconds.json)")
    measured = seconds_from_junits([Path(a) for a in sys.argv[1:]])
    # A suite in no report ran no test (the lux-only contract suites under
    # `-m "not lux"`): it costs a shard nothing, not DEFAULT_SECONDS.
    measured = dict(sorted({**{s: 0.0 for s in all_suites()}, **measured}.items()))
    SUITE_SECONDS_FILE.write_text(json.dumps(measured, indent=2) + "\n")
    print(f"{len(measured)} suites, {sum(measured.values()):.0f}s, written to {SUITE_SECONDS_FILE}")
