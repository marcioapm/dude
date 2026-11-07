"""The suite's own harness: browser channel and the ui auto-mark.

Pure checks; they need no environment, browser or services:

    uv run --directory tests pytest suites/test_harness.py
"""

import copy
import json

import pytest

import conftest
import e2e_receipt
import run_tests
from helpers import browser_channel

pytest_plugins = ["pytester"]


@pytest.mark.parametrize(
    ("value", "want"),
    [(None, {"channel": "chrome"}), ("", {}), ("chromium", {"channel": "chromium"}), ("msedge", {"channel": "msedge"})],
)
def test_browser_channel_reads_dude_test_browser_channel(monkeypatch, value, want):
    monkeypatch.delenv("DUDE_TEST_BROWSER_CHANNEL", raising=False)
    if value is not None:
        monkeypatch.setenv("DUDE_TEST_BROWSER_CHANNEL", value)
    assert browser_channel() == want


@pytest.mark.parametrize(("value", "want"), [("", {"headless": True}), ("chromium", {"headless": True, "channel": "chromium"})])
def test_launch_args_drop_an_inherited_channel(monkeypatch, value, want):
    # pytest-playwright may already pass a channel (--browser-channel); the
    # override must replace it, and an empty value must leave none at all.
    monkeypatch.setenv("DUDE_TEST_BROWSER_CHANNEL", value)
    launch_args = conftest.browser_type_launch_args.__wrapped__
    assert launch_args({"channel": "chrome", "headless": True}) == want


class _RealHook:
    # The conftest's own hook object (with its tryfirst), handed to a throwaway suite.
    pytest_collection_modifyitems = staticmethod(conftest.pytest_collection_modifyitems)


@pytest.fixture
def hook_suite(pytester):
    pytester.makeini("[pytest]\nmarkers =\n    ui: drives a real browser\naddopts = --strict-markers -p no:playwright\n")
    pytester.makepyfile(
        test_hook="""
import pytest

@pytest.fixture
def page():
    raise AssertionError("a browser was opened under -m 'not ui'")

@pytest.fixture
def gallery(page):
    return page

def test_asks_for_a_page(page):
    pass

def test_asks_for_a_page_through_a_fixture(gallery):
    pass

def test_needs_no_browser():
    pass
"""
    )
    return pytester


def test_not_ui_leaves_out_every_test_that_needs_a_browser(hook_suite):
    result = hook_suite.runpytest_inprocess("-m", "not ui", plugins=[_RealHook()])
    result.assert_outcomes(passed=1, deselected=2)


def test_ui_selects_exactly_the_tests_that_need_a_browser(hook_suite):
    result = hook_suite.runpytest_inprocess("-m", "ui", "--collect-only", "-q", plugins=[_RealHook()])
    assert sorted(line.split("::")[1] for line in result.outlines if "::" in line) == [
        "test_asks_for_a_page",
        "test_asks_for_a_page_through_a_fixture",
    ]


# Behavioral receipt checks run pytest itself, including session fixture teardown.


@pytest.fixture
def receipt_suite(pytester, monkeypatch):
    pytester.makepyfile(test_receipt="""
import pytest
@pytest.fixture(scope='session')
def resource():
    yield
@pytest.mark.parametrize('value', ['one', 'two'])
def test_pass(value, resource):
    pass
@pytest.mark.skip(reason='platform unavailable')
def test_platform():
    pass
""")
    path = pytester.path / "receipt.json"
    monkeypatch.setenv("DUDE_E2E_RECEIPT", str(path))
    monkeypatch.setenv("DUDE_E2E_SHARD", "1/1")
    monkeypatch.setenv("DUDE_E2E_PLAN", "1")
    def run(*extra):
        return pytester.runpytest_inprocess("-q", "-p", "no:playwright", *extra, plugins=[e2e_receipt])
    run("--collect-only").assert_outcomes()
    plan = pytester.path / "plan.json"
    plan.write_text(path.read_text())
    monkeypatch.setenv("DUDE_E2E_CANONICAL_PLAN", str(plan))
    monkeypatch.setenv("DUDE_E2E_PLAN", "0")
    return pytester, path, run


def test_terminal_receipt_preserves_parameter_ids_and_platform_skip(receipt_suite):
    _, path, run = receipt_suite
    run().assert_outcomes(passed=2, skipped=1)
    receipt = json.loads(path.read_text())
    assert receipt["pytest_complete"] and not receipt["complete"]
    receipt["complete"] = True  # the harness, not pytest, owns the final fence
    e2e_receipt.check_receipts([receipt], 1)
    skipped = next(r for r in receipt["results"] if r["outcome"] == "skipped")
    assert "platform unavailable" in skipped["reason"]
    for mutation in ("drop", "duplicate", "parameter", "unfinished", "failed", "missing"):
        broken = copy.deepcopy(receipt)
        if mutation == "drop":
            broken["results"] = []
        elif mutation == "duplicate":
            broken["results"].append(broken["results"][0])
        elif mutation == "parameter":
            broken["results"][0]["nodeid"] += "[different]"
        elif mutation == "unfinished":
            broken["pytest_complete"] = False
        elif mutation == "failed":
            broken["exit_code"] = 1
        with pytest.raises(ValueError):
            e2e_receipt.check_receipts([] if mutation == "missing" else [broken], 1)
    # Two runners must agree on inputs; an entire missing/duplicate file is red too.
    pair = []
    raw = ["suites/test_a.py::test_x[p]", "suites/test_b.py::test_y[q]"]
    for index in (1, 2):
        part = copy.deepcopy(receipt)
        part["plan"].update(raw=raw, selected=raw, assignment=e2e_receipt.assignment(raw, 2))
        part["plan"]["inputs"]["denominator"] = 2
        part["shard"] = index
        part["selected"] = [n for n in raw if n.split("::")[0] in part["plan"]["assignment"][index - 1]]
        part["results"] = [{"nodeid": n, "outcome": "passed"} for n in part["selected"]]
        pair.append(part)
    e2e_receipt.check_receipts(pair, 2)
    for field in ("marker", "browser"):
        broken = copy.deepcopy(pair)
        broken[1]["plan"]["inputs"][field] = "different"
        with pytest.raises(ValueError, match="different"):
            e2e_receipt.check_receipts(broken, 2)
    for replacement in ([], pair[0]["results"]):
        broken = copy.deepcopy(pair)
        broken[1]["results"] = replacement
        with pytest.raises(ValueError):
            e2e_receipt.check_receipts(broken, 2)


def test_failing_test_and_session_teardown_remain_failures(receipt_suite):
    pytester, path, run = receipt_suite
    pytester.makepyfile(test_receipt="""
import pytest
@pytest.fixture(scope='session')
def resource():
    yield
    assert False, 'teardown failed'
@pytest.mark.parametrize('value', ['one', 'two'])
def test_pass(value, resource):
    assert False
@pytest.mark.skip(reason='platform unavailable')
def test_platform():
    pass
""")
    assert run().ret == 1
    receipt = json.loads(path.read_text())
    assert receipt["exit_code"] == 1
    assert any(r["outcome"] == "failed" for r in receipt["results"])


@pytest.mark.parametrize("mode", ["plan", "collection_error", "empty", "database", "test_failure", "teardown"])
def test_pipeline_precollects_and_fails_closed(monkeypatch, tmp_path, mode):
    path = tmp_path / "receipt.json"
    calls = []
    def subprocess_run(command, **kwargs):
        planning = kwargs["env"]["DUDE_E2E_PLAN"] == "1"
        calls.append("collect" if planning else "pytest")
        node = "suites/test_probe.py::test_probe"
        data = {"selected": [node], "plan": {}, "pytest_complete": not planning,
                "results": [{"nodeid": node, "outcome": "passed"}], "exit_code": 0}
        e2e_receipt.write(kwargs["env"]["DUDE_E2E_RECEIPT"], data)
        return type("Result", (), {"returncode": 2 if mode == "collection_error" else 5 if mode == "empty" else 1 if not planning and mode == "test_failure" else 0})()
    class FakeEnvironment:
        run_id = db_name = log_dir = lux_url = "probe"
        control_plane_port = orchestrator_port = gallery_port = web_port = 1
        def __init__(self, **kwargs):
            pass
        def setup(self):
            calls.append("setup")
            if mode == "database":
                raise RuntimeError("database unavailable")
        def wait_healthy(self, **kwargs):
            return True
        def teardown(self, **kwargs):
            calls.append("teardown")
            if mode == "teardown":
                raise RuntimeError("teardown interrupted")
    monkeypatch.setattr(run_tests.subprocess, "run", subprocess_run)
    monkeypatch.setattr(run_tests, "TestEnvironment", FakeEnvironment)
    for name in ("require_bun", "build", "build_gallery", "build_web"):
        monkeypatch.setattr(run_tests, name, lambda **kwargs: calls.append("build"))
    arguments = ["--receipt", str(path)] + (["--plan"] if mode == "plan" else [])
    code = run_tests.main(arguments)
    assert (code == 0) == (mode == "plan")
    assert calls[0] == "collect"
    if mode in ("plan", "collection_error", "empty"):
        assert calls == ["collect"]
    else:
        assert calls[-1] == "teardown"
    assert not json.loads(path.read_text())["complete"]
