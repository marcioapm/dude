"""Direct pytest collection/result receipts; no services or stdout parsing."""
from __future__ import annotations

import hashlib
import json
import os
import platform
import subprocess
import sys
from collections import Counter
from pathlib import Path

import pytest

from shard import DEFAULT_SECONDS, SUITE_SECONDS_FILE, TESTS_DIR, parse_shard, split, suite_seconds


def canonical(nodeid):
    # These existing parametrizations embed TESTS_DIR; other literal paths are test data.
    if nodeid.startswith("suites/test_shard.py::test_a_suite_named_beside_a_shard_is_refused["):
        return nodeid.replace(str(TESTS_DIR), "<TESTS_DIR>")
    return nodeid


def write(path, data):
    Path(path).write_text(json.dumps(data, indent=2, sort_keys=True) + "\n")


def assignment(raw, denominator):
    files = sorted({node.split("::", 1)[0] for node in raw})
    return split(files, denominator, suite_seconds())


def check_receipts(receipts, denominator):
    if len(receipts) != denominator:
        raise ValueError("missing receipt")
    reference = receipts[0]["plan"]
    raw, selected = reference["raw"], reference["selected"]
    if (raw != sorted(set(raw)) or selected != sorted(set(selected))
            or not set(selected).issubset(raw)
            or reference["assignment"] != assignment(raw, denominator)):
        raise ValueError("invalid universe or whole-file assignment")
    seen = Counter()
    for index, receipt in enumerate(sorted(receipts, key=lambda r: r["shard"]), 1):
        if receipt["shard"] != index or receipt["plan"] != reference:
            raise ValueError("different shard or plan inputs/universe/assignment")
        if reference["inputs"]["denominator"] != denominator:
            raise ValueError("wrong denominator")
        expected = [n for n in reference["selected"] if n.split("::", 1)[0] in reference["assignment"][index - 1]]
        results = receipt["results"]
        if (not expected or receipt.get("selected") != expected
                or Counter(r["nodeid"] for r in results) != Counter(expected)):
            raise ValueError("missing/duplicate/unexpected selected result")
        if (not receipt.get("complete") or not receipt.get("pytest_complete")
                or receipt.get("exit_code") != 0
                or any(r["outcome"] not in ("passed", "skipped") for r in results)):
            raise ValueError("failed or unfinished session")
        seen.update(r["nodeid"] for r in results)
    if seen != Counter(reference["selected"]):
        raise ValueError("receipt union does not cover selected universe exactly once")
    return reference


class Receipt:
    def __init__(self):
        self.path = os.environ["DUDE_E2E_RECEIPT"]
        self.planning = os.environ.get("DUDE_E2E_PLAN") == "1"
        self.spec = os.environ.get("DUDE_E2E_SHARD", "1/1")
        self.data = {"complete": False, "pytest_complete": False, "results": []}
        self.reports = {}

    @pytest.hookimpl(hookwrapper=True, tryfirst=True)
    def pytest_collection_modifyitems(self, config, items):
        raw = sorted(canonical(i.nodeid) for i in items)
        yield
        selected = sorted(canonical(i.nodeid) for i in items)
        n, m = parse_shard(self.spec)
        plan = {
            "inputs": {
                "commit": subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=TESTS_DIR, text=True).strip(),
                "marker": config.option.markexpr, "keyword": config.option.keyword,
                "browser": config.getoption("browser", default=[]),
                "browser_options": {key: config.getoption(key, default=None) for key in
                                    ("browser_channel", "headed", "device", "base_url")},
                "browser_channel": os.environ.get("DUDE_TEST_BROWSER_CHANNEL", "chrome"),
                "browser_executable": os.environ.get("DUDE_TEST_BROWSER_EXECUTABLE", ""),
                "platform": [platform.system(), platform.machine()], "denominator": m,
                "timing_sha256": hashlib.sha256(SUITE_SECONDS_FILE.read_bytes()).hexdigest(),
                "unknown_seconds": DEFAULT_SECONDS,
            },
            "raw": raw, "selected": selected, "assignment": assignment(raw, m),
        }
        if Counter(raw) != Counter(set(raw)):
            raise pytest.UsageError("duplicate collection IDs")
        expected = [node for node in selected if node.split("::", 1)[0] in plan["assignment"][n - 1]]
        self.data.update(plan=plan, shard=n, selected=expected)
        write(self.path, self.data)
        if not expected:
            raise pytest.UsageError("empty selected shard")
        if not self.planning:
            previous = json.loads(Path(os.environ["DUDE_E2E_CANONICAL_PLAN"]).read_text())
            if previous["plan"] != plan or previous["selected"] != expected:
                raise pytest.UsageError("execution collection differs from canonical plan")
            rejected = [i for i in items if canonical(i.nodeid) not in expected]
            items[:] = [i for i in items if canonical(i.nodeid) in expected]
            config.hook.pytest_deselected(items=rejected)

    def pytest_runtest_logreport(self, report):
        reports = self.reports.setdefault(report.nodeid, [])
        reports.append(report)
        if report.when == "teardown":
            bad = next((r for r in reports if r.failed), None)
            skipped = next((r for r in reports if r.skipped), None)
            self.data["results"].append({
                "nodeid": canonical(report.nodeid),
                "outcome": "failed" if bad else "skipped" if skipped else "passed",
                "reason": str(skipped.longrepr) if skipped else "",
                "seconds": sum(r.duration for r in reports),
            })
            write(self.path, self.data)

    @pytest.hookimpl(hookwrapper=True, tryfirst=True)
    def pytest_sessionfinish(self, session, exitstatus):
        yield  # includes pytest's session fixture teardown and other finish hooks
        self.data.update(pytest_complete=True, exit_code=int(session.exitstatus))
        write(self.path, self.data)


def pytest_configure(config):
    if os.environ.get("DUDE_E2E_RECEIPT"):
        config.pluginmanager.register(Receipt(), "dude-e2e-receipt")


if __name__ == "__main__":
    try:
        paths = list(Path(sys.argv[1]).rglob("receipt.json"))
        check_receipts([json.loads(p.read_text()) for p in paths], int(sys.argv[2]))
        print(f"coverage verified: {len(paths)} shards")
    except (ValueError, KeyError, OSError) as error:
        sys.exit(f"E2E coverage failed: {error}")
