"""UI regression tests for the design system gallery.

The gallery renders every token, primitive and component in every state, so
driving it in a real browser is the cheapest guard against the kind of
breakage that typechecking cannot catch: a component that throws on render, a
theme that resolves to unreadable colors, or motion that ignores the reduced-
motion preference.

These run against the *built* gallery, not the dev server, so they exercise
what would actually ship.

Marked `ui`; skip with `-m "not ui"`.
"""

from __future__ import annotations

import pytest
from playwright.sync_api import Page, expect

pytestmark = pytest.mark.ui


def test_gallery_renders_without_console_errors(gallery_page: Page, console_errors: list):
    """A component that throws on render fails here and nowhere else."""
    expect(gallery_page.get_by_role("heading", name="Tokens", exact=True)).to_be_visible()
    assert console_errors == [], f"console errors on load: {console_errors}"


def test_every_section_is_reachable_and_renders(gallery_page: Page, console_errors: list):
    """Walk the whole gallery, so one broken section cannot hide behind others."""
    links = gallery_page.locator("nav a")
    count = links.count()
    assert count > 15, f"expected the full gallery nav, found {count} links"

    for i in range(count):
        link = links.nth(i)
        name = (link.text_content() or "").strip()
        link.click()
        gallery_page.wait_for_timeout(60)
        assert console_errors == [], f"console errors after opening {name!r}: {console_errors}"


def test_status_vocabulary_is_fully_rendered(gallery_page: Page):
    """Every domain status must have a visible treatment.

    A status with no entry in the vocabulary would otherwise reach the UI as a
    raw enum string, which is exactly the drift this table exists to prevent.
    """
    gallery_page.get_by_role("link", name="Status vocabulary").click()
    body = gallery_page.locator("body")

    for status in (
        "awaiting_input",
        "awaiting_confirmation",
        "running",
        "completed",
        "failed",
        "aborted",
        "ready_to_merge",
    ):
        expect(body).to_contain_text(status)


def test_awaiting_input_is_the_loudest_state(gallery_page: Page):
    """`awaiting_input` means the system is blocked on the operator.

    It is the one state allowed a solid treatment, and the design falls apart
    if something else quietly takes that emphasis.
    """
    gallery_page.get_by_role("link", name="Status vocabulary").click()
    expect(gallery_page.locator("body")).to_contain_text("Needs you")


def test_both_themes_render(gallery_page: Page, console_errors: list):
    """Dark and light are peers here, not a default plus an afterthought."""
    for theme in ("dark", "light"):
        gallery_page.evaluate(
            "t => document.documentElement.setAttribute('data-theme', t)", theme
        )
        gallery_page.wait_for_timeout(120)

        background = gallery_page.evaluate(
            "getComputedStyle(document.documentElement)"
            ".getPropertyValue('--ds-color-canvas').trim()"
        )
        assert background, f"no canvas token resolved in {theme} theme"

    assert console_errors == [], f"console errors while switching themes: {console_errors}"


def test_themes_actually_differ(gallery_page: Page):
    """Guards against a theme that silently resolves to the other one."""

    def canvas() -> str:
        return gallery_page.evaluate(
            "getComputedStyle(document.documentElement)"
            ".getPropertyValue('--ds-color-canvas').trim()"
        )

    gallery_page.evaluate("document.documentElement.setAttribute('data-theme','dark')")
    gallery_page.wait_for_timeout(80)
    dark = canvas()

    gallery_page.evaluate("document.documentElement.setAttribute('data-theme','light')")
    gallery_page.wait_for_timeout(80)
    light = canvas()

    assert dark != light, f"both themes resolved to {dark}"


def test_text_is_readable_on_its_surface(gallery_page: Page):
    """Primary text must clear 4.5:1 against the canvas in both themes.

    Computed from what the browser actually resolved, so a token that is
    overridden somewhere in the cascade is still caught.
    """
    contrast_js = """
    () => {
      const s = getComputedStyle(document.documentElement);
      const parse = (v) => {
        const m = v.trim().match(/^#?([0-9a-f]{6})$/i);
        if (m) return [0,2,4].map(i => parseInt(m[1].slice(i,i+2),16));
        const rgb = v.match(/\\d+/g);
        return rgb ? rgb.slice(0,3).map(Number) : null;
      };
      const lum = (c) => {
        const [r,g,b] = c.map(v => {
          v /= 255;
          return v <= 0.03928 ? v/12.92 : Math.pow((v+0.055)/1.055, 2.4);
        });
        return 0.2126*r + 0.7152*g + 0.0722*b;
      };
      const fg = parse(s.getPropertyValue('--ds-color-text-primary'));
      const bg = parse(s.getPropertyValue('--ds-color-canvas'));
      if (!fg || !bg) return null;
      const [a,b] = [lum(fg), lum(bg)].sort((x,y) => y-x);
      return (a + 0.05) / (b + 0.05);
    }
    """

    for theme in ("dark", "light"):
        gallery_page.evaluate("t => document.documentElement.setAttribute('data-theme', t)", theme)
        gallery_page.wait_for_timeout(80)

        ratio = gallery_page.evaluate(contrast_js)
        assert ratio is not None, f"could not resolve text/canvas tokens in {theme}"
        assert ratio >= 4.5, f"{theme}: primary text contrast {ratio:.2f}:1 is below 4.5:1"


def test_reduced_motion_is_honoured(browser, gallery_url: str):
    """Live indicators must freeze legibly, not vanish.

    The preference is emulated at the browser level rather than toggled in the
    page, so this tests the media query the product actually ships.
    """
    context = browser.new_context(reduced_motion="reduce")
    page = context.new_page()
    try:
        page.goto(gallery_url, wait_until="networkidle")

        # The token multiplier is what every looping animation scales by.
        multiplier = page.evaluate(
            "getComputedStyle(document.documentElement)"
            ".getPropertyValue('--ds-motion-live').trim()"
        )
        assert multiplier in ("0", "0s", "0ms"), (
            f"reduced motion should stop live loops, got {multiplier!r}"
        )

        # Whatever is animated must still be on screen and readable.
        expect(page.get_by_role("heading", name="Tokens", exact=True)).to_be_visible()
    finally:
        context.close()


def test_interactive_primitives_respond(gallery_page: Page, console_errors: list):
    """Click through the interactive primitives; a broken handler throws here."""
    gallery_page.get_by_role("link", name="Checkbox").click()
    gallery_page.wait_for_timeout(80)

    # The accessible role, not a class: the wrapper div carries the module
    # class, while the control that actually holds state is the button.
    checkbox = gallery_page.locator("main").get_by_role("checkbox").first
    checkbox.wait_for(state="visible")

    before = checkbox.get_attribute("data-state") or checkbox.get_attribute("aria-checked")
    checkbox.click()
    gallery_page.wait_for_timeout(120)
    after = checkbox.get_attribute("data-state") or checkbox.get_attribute("aria-checked")

    assert before != after, f"checkbox state did not change on click ({before!r} -> {after!r})"
    assert console_errors == [], f"console errors interacting: {console_errors}"


def test_keyboard_focus_is_visible(gallery_page: Page):
    """An operator on the keyboard must always see where they are.

    Tabs to the control rather than calling `.focus()`: `:focus-visible` is
    about *how* focus arrived, so only real keyboard navigation exercises the
    rule the product actually ships.
    """
    gallery_page.get_by_role("link", name="Button").click()
    gallery_page.wait_for_timeout(120)

    found = None
    for _ in range(60):
        gallery_page.keyboard.press("Tab")
        found = gallery_page.evaluate(
            """() => {
                const el = document.activeElement;
                if (!el || !/Button-module/.test(el.className || "")) return null;
                const s = getComputedStyle(el);
                return {
                  cls: el.className,
                  outlineWidth: s.outlineWidth,
                  outlineStyle: s.outlineStyle,
                  boxShadow: s.boxShadow,
                };
            }"""
        )
        if found:
            break

    assert found, "tabbing never reached a design-system Button"

    has_ring = (
        found["outlineStyle"] not in ("none", "")
        and found["outlineWidth"] not in ("0px", "")
    ) or found["boxShadow"] not in ("none", "")
    assert has_ring, f"focused control has no visible focus indicator: {found}"


def test_board_keeps_needs_you_first_and_is_a_keyboard_grid(gallery_page: Page, console_errors: list):
    """The board is the overview; two things about it must not regress.

    A needs-you card sorted below a calm one would defeat the board's reason
    to exist, and a board that is only reachable by mouse fails the operator
    who lives on the keyboard. Both are checked on the realistic project
    board, which has a needs-you card in two different lanes.
    """
    gallery_page.get_by_role("link", name="Project board (realistic)").click()
    gallery_page.wait_for_timeout(120)

    board = gallery_page.locator("#board-project").get_by_role("region", name="control-plane board").first
    board.wait_for(state="visible")

    lanes = board.locator("section[data-column]")
    assert [lanes.nth(i).get_attribute("data-column") for i in range(lanes.count())] == [
        "backlog", "running", "review", "ready", "closed",
    ], "the five lanes must always be drawn, in lifecycle order"

    for lane in ("backlog", "running"):
        first = board.locator(f"section[data-column='{lane}'] [data-board-key]").first
        assert first.get_attribute("data-triage") == "needs_you", f"{lane}: needs-you card is not first"

    # One tab stop, on the selected card; arrows move focus without changing
    # selection. The tab stop roves with focus, so read the key first.
    stops = board.locator("[data-board-key][tabindex='0']")
    assert stops.count() == 1
    start = stops.first.get_attribute("data-board-key")
    stops.first.focus()
    gallery_page.keyboard.press("ArrowDown")
    moved = gallery_page.evaluate("document.activeElement?.getAttribute('data-board-key')")
    assert moved and moved != start, "ArrowDown did not move focus"
    assert board.locator("[aria-current='true']").get_attribute("data-board-key") == start, "moving focus must not change selection"

    assert console_errors == [], f"console errors on the board: {console_errors}"


def test_markdown_toolbar_keeps_its_tab_stop_when_quote_hides(gallery_page: Page, console_errors: list):
    """Quote hides when the editor narrows. If it held the toolbar's one Tab
    stop, the stop and focus move to a button still shown, so the toolbar
    stays reachable by Tab."""
    gallery_page.set_viewport_size({"width": 2400, "height": 900})
    gallery_page.get_by_role("link", name="MarkdownEditor").click()
    toolbar = gallery_page.locator("#p-markdown-editor").get_by_role("toolbar", name="Formatting").first
    toolbar.scroll_into_view_if_needed()
    expect(toolbar.locator('[data-format="quote"]')).to_be_visible()
    toolbar.locator('[data-format="heading"]').focus()
    for _ in range(5):
        gallery_page.keyboard.press("ArrowRight")
    assert gallery_page.evaluate("document.activeElement?.getAttribute('data-format')") == "quote"

    gallery_page.set_viewport_size({"width": 480, "height": 900})
    expect(toolbar.locator('[data-format="quote"]')).to_be_hidden()
    expect(toolbar.locator('[data-format="link"]')).to_be_focused()
    stops = toolbar.locator("button[tabindex='0']")
    expect(stops).to_have_count(1)
    assert stops.first.get_attribute("data-format") == "link"
    assert console_errors == []
