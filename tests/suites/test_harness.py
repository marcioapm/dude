"""The suite's own harness: browser channel and the ui auto-mark.

Pure checks; they need no environment, browser or services:

    uv run --directory tests pytest suites/test_harness.py
"""

import pytest

import conftest
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
