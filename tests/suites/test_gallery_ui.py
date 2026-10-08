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


def test_a_select_list_with_long_descriptions_fits_a_phone(gallery_page: Page, console_errors: list):
    """At 390px the list stays on the screen and its descriptions wrap; on a wide screen it stops at 560px."""
    for width, most in ((390, 390 - 16), (1440, 560)):
        gallery_page.set_viewport_size({"width": width, "height": 844})
        gallery_page.get_by_role("link", name="Select").click()
        # The light pane's: the dark pane's row runs under it at some widths.
        trigger = gallery_page.locator("[data-testid=gallery-select-long]").last
        trigger.click()
        expect(trigger).to_have_attribute("aria-expanded", "true")
        listbox = gallery_page.locator(f"[id='{trigger.get_attribute('aria-controls')}']")
        expect(listbox).to_be_visible()
        box = listbox.bounding_box()
        assert box is not None
        assert box["width"] <= most + 0.5, f"list is {box['width']}px wide at {width}px"
        assert box["x"] >= 0 and box["x"] + box["width"] <= width, f"list runs off a {width}px screen: {box}"
        gallery_page.keyboard.press("Escape")
        expect(listbox).to_be_hidden()
    assert console_errors == []


def test_segmented_tabs_move_with_home_and_end(gallery_page: Page, console_errors: list):
    """Write / Preview is a tablist: Home and End reach its ends, as ← → do its neighbours."""
    gallery_page.get_by_role("link", name="MarkdownEditor").click()
    view = gallery_page.locator("#p-markdown-editor").get_by_role("tablist", name="Write view").first
    write = view.get_by_role("tab", name="Write")
    preview = view.get_by_role("tab", name="Preview")
    write.focus()
    gallery_page.keyboard.press("End")
    expect(preview).to_have_attribute("aria-selected", "true")
    expect(preview).to_be_focused()
    gallery_page.keyboard.press("Home")
    expect(write).to_have_attribute("aria-selected", "true")
    expect(write).to_be_focused()
    # Level with the toolbar's sm buttons.
    bold = gallery_page.locator("#p-markdown-editor").get_by_role("button", name="Bold").first
    assert view.evaluate("el => el.getBoundingClientRect().height") == bold.evaluate("el => el.getBoundingClientRect().height")
    assert console_errors == []


def test_tooltips_draw_a_key_list_as_caps_and_a_string_as_it_is(gallery_page: Page, console_errors: list):
    """The editor's Bold names its shortcut as caps, the platform's modifier first."""
    gallery_page.get_by_role("link", name="MarkdownEditor").click()
    bold = gallery_page.locator("#p-markdown-editor").get_by_role("button", name="Bold").first
    bold.hover()
    tip = gallery_page.get_by_role("tooltip", name="Bold")
    caps = tip.locator("kbd")
    expect(tip).to_be_visible()
    modifier = gallery_page.evaluate(
        "/mac|iphone|ipad|ipod/i.test(navigator.userAgentData?.platform || navigator.platform) ? '⌘' : 'Ctrl'")
    expect(caps).to_have_text([modifier, "B"])

    gallery_page.get_by_role("link", name="Tooltip").click()
    gallery_page.locator("#p-tooltip").get_by_role("button", name="Hover me").first.hover()
    string_tip = gallery_page.get_by_role("tooltip").filter(has_text="Open the session in a side panel")
    expect(string_tip).to_be_visible()
    expect(string_tip.locator("kbd")).to_have_text(["⏎"])
    assert console_errors == []


# The space between the children of the nearest common ancestor of two elements that hold `a` and `b`.
_GAP_BETWEEN = """([a, b]) => {
  let child = a;
  while (!child.parentElement.contains(b)) child = child.parentElement;
  let next = b;
  while (next.parentElement !== child.parentElement) next = next.parentElement;
  return next.getBoundingClientRect().top - child.getBoundingClientRect().bottom;
}"""


def test_compact_tightens_a_filled_form_stack_and_leaves_a_plain_one(gallery_page: Page, console_errors: list):
    """The task dialog's fields are a filled FormStack; compact draws them closer. The Form layout example is not filled."""
    gaps = {}
    for density in ("comfortable", "compact"):
        gallery_page.locator("nav").get_by_role("combobox", name="Density").click()
        gallery_page.get_by_role("option", name=density.capitalize(), exact=True).click()
        expect(gallery_page.locator("#p-dialog [data-density]").first).to_have_attribute("data-density", density)

        gallery_page.get_by_role("link", name="Dialog", exact=True).click()
        gallery_page.locator("#p-dialog").get_by_role("button", name="Task dialog").first.click()
        dialog = gallery_page.get_by_role("dialog", name="New task")
        expect(dialog).to_be_visible()
        # The dialog pops in with a scale; measure once it has settled.
        gallery_page.wait_for_function(
            "document.querySelector('[role=dialog]').getAnimations().every(a => a.playState === 'finished')")
        title = dialog.get_by_role("textbox", name="Title")
        goal = dialog.get_by_role("textbox", name="Goal")
        filled = gallery_page.evaluate(_GAP_BETWEEN, [title.element_handle(), goal.element_handle()])
        gallery_page.keyboard.press("Escape")
        expect(dialog).to_have_count(0)

        form = gallery_page.locator("#p-form")
        callout = form.get_by_text("url must be an https, ssh or git:// URL").first
        rounds = form.get_by_role("textbox", name="Review rounds").first
        plain = gallery_page.evaluate(_GAP_BETWEEN, [callout.element_handle(), rounds.element_handle()])
        gaps[density] = (filled, plain)

    assert gaps["compact"][0] < gaps["comfortable"][0], gaps
    assert gaps["compact"][1] == gaps["comfortable"][1], gaps
    assert console_errors == []


def test_a_filling_field_fills_a_document_dialog_without_an_aside(gallery_page: Page, console_errors: list):
    """The body is the one column: the Note fills it to the body's foot, and grows past it with its text."""
    gallery_page.set_viewport_size({"width": 1280, "height": 900})
    gallery_page.get_by_role("link", name="Dialog", exact=True).click()
    gallery_page.locator("#p-dialog").get_by_role("button", name="Note dialog").first.click()
    dialog = gallery_page.get_by_role("dialog", name="New note")
    expect(dialog).to_be_visible()
    gallery_page.wait_for_function(
        "document.querySelector('[role=dialog]').getAnimations().every(a => a.playState === 'finished')")
    measure = """() => {
      const note = document.querySelector('[data-testid="note-body"]');
      const frame = note.closest('[data-mode]');
      let body = frame.parentElement;
      while (getComputedStyle(body).overflowY !== 'auto') body = body.parentElement;
      const b = body.getBoundingClientRect(), cs = getComputedStyle(body);
      return { frameBottom: frame.getBoundingClientRect().bottom, bodyInnerBottom: b.top + body.clientHeight - parseFloat(cs.paddingBottom),
               frameHeight: frame.getBoundingClientRect().height, scrollHeight: body.scrollHeight, clientHeight: body.clientHeight };
    }"""
    m = gallery_page.evaluate(measure)
    assert m["scrollHeight"] <= m["clientHeight"], m
    assert abs(m["bodyInnerBottom"] - m["frameBottom"]) <= 2, m
    assert m["frameHeight"] > 500, m
    dialog.get_by_role("textbox", name="Note").fill("\n".join(f"Line {i}" for i in range(80)))
    m = gallery_page.evaluate(measure)
    assert m["scrollHeight"] > m["clientHeight"], m
    gallery_page.keyboard.press("Escape")
    assert console_errors == []


def test_the_image_states_render_and_the_viewer_opens_from_a_turn(gallery_page: Page, console_errors: list):
    """Every tray state, the drop target and a turn's images; a click opens
    the viewer on that image, Esc closes it."""
    import os
    from pathlib import Path

    gallery_page.set_viewport_size({"width": 1440, "height": 1000})
    gallery_page.get_by_role("link", name="Images", exact=True).click()
    block = gallery_page.locator("#ch-images")
    expect(block.get_by_test_id("attachment-chip").first).to_be_visible()
    for state in ("uploading", "ready", "error"):
        expect(block.locator(f'[data-testid="attachment-chip"][data-state="{state}"]').first).to_be_visible()
    expect(block.get_by_test_id("attachment-warning").first).to_contain_text("2 can't be sent")
    expect(block.get_by_test_id("attach-button").filter(has=gallery_page.locator("[disabled]")).or_(
        block.locator('[data-testid="attach-button"][disabled]')).first).to_be_visible()
    shots = Path(os.environ.get("DUDE_TEST_SHOTS", "/var/tmp/cimg-dude-shots"))
    shots.mkdir(parents=True, exist_ok=True)
    block.screenshot(path=str(shots / "gallery-images.png"))
    block.get_by_test_id("message-image").first.click()
    viewer = gallery_page.get_by_test_id("image-viewer")
    expect(viewer).to_be_visible()
    expect(gallery_page.get_by_test_id("viewer-meta")).to_contain_text("scaled from 2400×1520")
    gallery_page.keyboard.press("Escape")
    expect(viewer).to_have_count(0)
    assert console_errors == []


def test_a_session_title_is_renamed_in_place_with_enter_and_escape(gallery_page: Page, console_errors: list):
    """SessionTitle: pressing a session's name opens it for editing in place;
    Escape keeps the name, Enter saves the new one."""
    gallery_page.get_by_role("link", name="SessionTitle", exact=True).click()
    pane = gallery_page.locator("#bs-title [data-theme]").first
    titles = pane.get_by_test_id("session-title")
    expect(titles.nth(0)).to_have_text("New session")
    titles.nth(1).click()
    field = pane.get_by_test_id("session-title-input")
    expect(field).to_be_focused()
    field.fill("Dropped")
    gallery_page.keyboard.press("Escape")
    expect(field).to_have_count(0)
    expect(titles.nth(1)).to_have_text("Usage-based billing")
    titles.nth(1).click()
    pane.get_by_test_id("session-title-input").fill("Billing v2")
    gallery_page.keyboard.press("Enter")
    expect(titles.nth(1)).to_have_text("Billing v2")
    assert console_errors == []


def _title_fits(title) -> dict:
    """A SessionTitle's words, as Chrome laid them out: whether they are cut, and the space around them."""
    return title.evaluate("""el => {
        const words = el.querySelector('[data-title-words]');
        const header = el.closest('header');
        return {text: words.textContent, cut: words.scrollWidth > words.clientWidth, tip: words.getAttribute('title'),
                words: words.getBoundingClientRect().width, header: header.getBoundingClientRect().width};
    }""")


def test_a_session_title_takes_the_headers_width_and_cuts_only_a_title_too_long(gallery_page: Page, console_errors: list):
    """In a screen's header at desktop width, a short title and "New session" are never cut, shown or
    edited; a title longer than the line is cut with an ellipsis, all of it in the tooltip."""
    gallery_page.set_viewport_size({"width": 1440, "height": 1000})
    gallery_page.get_by_role("link", name="SessionTitle", exact=True).click()
    headers = gallery_page.locator("#bs-title [data-theme]").first.get_by_test_id("title-in-header")
    titles = headers.get_by_test_id("session-title")
    short, untitled, long = (_title_fits(titles.nth(i)) for i in range(3))
    assert (short["text"], short["cut"]) == ("Billing v2", False), short
    assert (untitled["text"], untitled["cut"]) == ("New session", False), untitled
    assert long["cut"] and long["tip"] == long["text"], long
    # Cut only for want of room: the long title takes the header's line.
    assert long["words"] > long["header"] * 0.6, long
    # Edited, the field is as wide as the name needs and no narrower than a short one's room.
    titles.nth(0).click()
    field = headers.get_by_test_id("session-title-input")
    expect(field).to_be_focused()
    fits = field.evaluate("el => el.scrollWidth <= el.clientWidth")
    assert fits, "the name field cuts a short name"
    gallery_page.keyboard.press("Escape")
    assert console_errors == []


def test_a_shared_session_header_on_a_phone_keeps_a_line_for_its_title(gallery_page: Page, console_errors: list):
    """At 375px a shared header's marker, model and Share do not shrink; they wrap under the title,
    which keeps the first line, with room for at least "New session"."""
    gallery_page.get_by_role("link", name="SessionTitle", exact=True).click()
    gallery_page.set_viewport_size({"width": 375, "height": 900})
    pane = gallery_page.locator("#bs-title [data-theme]").first
    # The gallery's own navigation takes 220px of a phone: the header is measured as a phone's screen
    # draws it, alone at the viewport's width (a copy, inside the pane for its theme).
    m = pane.get_by_test_id("title-in-header").locator("header").first.evaluate("""el => {
        const h = el.cloneNode(true);
        h.style.cssText = 'position:fixed;left:0;top:0;width:375px;box-sizing:border-box;z-index:10';
        el.parentElement.append(h);
        const title = h.querySelector('h1'), words = h.querySelector('[data-title-words]');
        const probe = words.cloneNode(false);
        probe.textContent = 'New session';
        probe.style.cssText = 'position:absolute;visibility:hidden;width:max-content';
        h.append(probe);
        const r = {header: h.getBoundingClientRect().width, overflow: h.scrollWidth - h.clientWidth,
                   room: title.getBoundingClientRect().width, need: probe.getBoundingClientRect().width,
                   words: words.getBoundingClientRect().width, cut: words.scrollWidth > words.clientWidth,
                   text: words.textContent, titleBottom: title.getBoundingClientRect().bottom,
                   metaTop: title.nextElementSibling.getBoundingClientRect().top,
                   actionsRight: h.lastElementChild.previousElementSibling.getBoundingClientRect().right};
        h.remove();
        return r;
    }""")
    assert m["header"] == 375 and m["overflow"] <= 0, m
    assert (m["text"], m["cut"]) == ("Billing v2", False) and m["words"] > 0, m
    assert m["room"] > m["need"] > 0, m
    assert m["metaTop"] >= m["titleBottom"] and m["actionsRight"] <= 375, m
    assert console_errors == []


def test_published_files_name_each_file_and_cap_the_list(gallery_page: Page, console_errors: list):
    """PublishedFiles: the rail's Files, each by its own name with the folder in its tooltip, then N more."""
    gallery_page.get_by_role("link", name="PublishedFiles", exact=True).click()
    pane = gallery_page.locator("#bs-files [data-theme]").first
    lists = pane.get_by_test_id("published-files")
    first = lists.nth(0).locator('[data-name="design/metering.md"]')
    expect(first).to_contain_text("metering.md")
    expect(first).to_have_attribute("title", "design/metering.md")
    expect(first).to_contain_text("v3")
    expect(lists.nth(1)).to_contain_text("1 more")
    assert console_errors == []
