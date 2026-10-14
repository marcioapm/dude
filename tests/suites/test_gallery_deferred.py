"""The gallery's deferred sections, in the built gallery in a real browser.

The task relations, the Starting examples and a tool call's child sessions
mount only when scrolled near or named by a fragment, so the first load stays
quick. These check that each still mounts and lands where it is asked for:
a deep link, the sidebar, a hash change, and that unrelated fragments leave
them deferred.

Marked `ui`; skip with `-m "not ui"`.
"""

from __future__ import annotations

import pytest
from playwright.sync_api import Page, expect

pytestmark = pytest.mark.ui


RELATION_BLOCK_IDS = (
    "rel-icons", "rel-chip", "rel-board", "rel-board-lit", "rel-board-filter",
    "rel-startmenu", "rel-gate", "rel-auto", "rel-dialogs", "rel-list",
    "rel-picker", "rel-strip", "rel-proposal",
)


def _expect_fragment_at_top(page: Page, target: str):
    expect(page.locator(f"#{target}")).to_have_count(1)
    page.wait_for_function(
        "id => { const el = document.getElementById(id); "
        "return el && Math.abs(el.getBoundingClientRect().top) <= 32; }",
        arg=target, timeout=5000,
    )


# These take a blank `page`, not `gallery_page`: each test's first load is
# itself under test, so a gallery loaded before it would be thrown away.
@pytest.mark.parametrize("target", RELATION_BLOCK_IDS)
def test_relation_fragment_navigation(page: Page, gallery_url: str, console_errors: list, target: str):
    """A fresh fragment must mount its deferred target and explicitly land on it."""
    page.goto(f"{gallery_url}#{target}", wait_until="networkidle")
    _expect_fragment_at_top(page, target)
    expect(page.locator("[data-testid=relations-pending]")).to_have_count(0)
    # Once mounted, later fragments use ordinary native navigation.
    for later in ("rel-picker", "rel-board", target):
        page.evaluate("id => { location.hash = id; }", later)
        _expect_fragment_at_top(page, later)
    assert console_errors == []


def test_relation_sidebar_navigation(page: Page, gallery_url: str, console_errors: list):
    """The sidebar must work before intersection ever mounts the relation blocks."""
    page.goto(gallery_url, wait_until="networkidle")
    expect(page.locator("[data-testid=relations-pending]")).to_have_count(1)
    page.locator('nav a[href="#rel-board"]').click()
    _expect_fragment_at_top(page, "rel-board")
    assert console_errors == []


def test_unrelated_fragment_keeps_relations_deferred(page: Page, gallery_url: str, console_errors: list):
    """Unrelated, unknown and malformed fragments must not request the costly mount."""
    for fragment in ("p-markdown-editor", "rel-unknown", "rel-%ZZ"):
        page.goto("about:blank")
        page.goto(f"{gallery_url}#{fragment}", wait_until="networkidle")
        expect(page.locator("[data-testid=relations-pending]")).to_have_count(1)
        expect(page.locator("#rel-picker")).to_have_count(0)
    page.goto("about:blank")
    page.goto(f"{gallery_url}#%72el-picker", wait_until="networkidle")
    _expect_fragment_at_top(page, "rel-picker")
    page.goto("about:blank")
    page.goto(f"{gallery_url}#relations", wait_until="networkidle")
    page.locator("#relations").evaluate("el => el.scrollIntoView()")
    expect(page.locator("#rel-picker")).to_have_count(1)
    _expect_fragment_at_top(page, "relations")
    assert console_errors == []


def test_relation_intersection_preserves_scroll_with_old_fragment(
    page: Page, gallery_url: str, console_errors: list,
) -> None:
    page.goto(f"{gallery_url}#p-markdown-editor", wait_until="networkidle")
    expect(page.locator("[data-testid=relations-pending]")).to_have_count(1)
    intended_y = page.evaluate("""() => {
        const y = document.getElementById('relations').getBoundingClientRect().top
            + window.scrollY - 1000;
        window.scrollTo(0, y);
        return y;
    }""")
    expect(page.locator("#rel-picker")).to_have_count(1)
    page.wait_for_function(
        "y => Math.abs(window.scrollY - y) <= 2", arg=intended_y, timeout=5000,
    )
    assert console_errors == []


@pytest.mark.parametrize("anchor,name", [
    ("st-play", "A fresh start"),
    ("st-states", "Resumed, slow, older lux"),
    ("st-failed", "Failed"),
    ("st-move", "A move"),
    ("st-chip", "StartChip"),
])
def test_starting_nav_reaches_an_unmounted_example(gallery_page: Page, anchor: str, name: str):
    """A far-off section must mount and scroll when its example is selected."""
    expect(gallery_page.locator("#st-play")).to_have_count(0)
    gallery_page.get_by_role("link", name=name, exact=True).click()
    expect(gallery_page.locator(f"#{anchor}")).to_be_in_viewport()
    gallery_page.get_by_role("link", name="Status vocabulary", exact=True).click()
    gallery_page.get_by_role("link", name="StartChip", exact=True).click()
    expect(gallery_page.locator("#st-chip")).to_be_in_viewport()


def test_starting_deep_link_mounts_and_scrolls(
    page: Page, gallery_url: str, console_errors: list,
):
    """Loading a bookmark must not leave its example permanently absent."""
    page.goto(f"{gallery_url}#st-failed", wait_until="networkidle")
    expect(page.locator("#st-failed")).to_be_in_viewport()
    assert console_errors == []


def test_child_examples_mount_after_hash_navigation(gallery_page: Page, console_errors: list):
    block = gallery_page.locator("#ch-children")
    expect(block.get_by_role("heading", name="ChatThread — a tool call's child session", exact=True)).to_have_count(1)
    expect(block.get_by_test_id("child-examples-pending")).to_have_count(1)
    expect(block.locator("section[data-session-id]")).to_have_count(0)
    gallery_page.evaluate("location.hash = 'ch-children'")
    expect(block.get_by_test_id("child-examples-pending")).to_have_count(0)
    # Where the navigation itself lands, before any scroll of our own: the
    # placeholder gave way to the examples, and the first is in view.
    expect(block.locator("section[data-session-id]").first).to_be_in_viewport()
    for theme in ("dark", "light"):
        pane = block.locator(f"[data-theme='{theme}']")
        threads = pane.locator("section[data-session-id]")
        expect(threads).to_have_count(5)
        assert threads.evaluate_all("els => els.map(e => e.dataset.status + (e.dataset.waiting ? ' waiting' : ''))") == [
            "running", "running waiting", "completed", "running", "completed",
        ]
        expect(pane).to_contain_text("Nothing in this Run can answer it.")
    assert gallery_page.evaluate("location.hash") == "#ch-children"
    assert console_errors == []
