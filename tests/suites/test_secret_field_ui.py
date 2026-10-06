"""SecretField and the block SettingRow in a real browser, the built gallery.

Masked, the value is a native password field: Chrome's accessibility tree
gives assistive technology no value for it, and the page's text has none.
Shown, it is a textarea whose value the tree carries. A paste while masked
keeps every line. A block SettingRow lays its table out at the control
column's full width.

Marked `ui`; skip with `-m "not ui"`.
"""

from __future__ import annotations

import pytest
from playwright.sync_api import Page, expect

pytestmark = pytest.mark.ui

PEM = (
    "-----BEGIN PRIVATE KEY-----\n"
    "MIIEvQIBADANBgkqhkiG9w0BAQEFAASCBKcwggSjAgEAAoIBAQC7\n"
    "k3Lq9bF0rT2yVf8mWJxQ1s0ZpN4cD6eH5aR7uGvKtYwE9iL3oM\n"
    "-----END PRIVATE KEY-----"
)
BODY_LINE = "k3Lq9bF0rT2yVf8mWJxQ1s0ZpN4cD6eH5aR7uGvKtYwE9iL3oM"


def single_pane(page: Page, width: int = 1440) -> None:
    """One pane (dark) rather than dark and light side by side: each demo once, at a page's width."""
    page.set_viewport_size({"width": width, "height": 900})
    page.evaluate("localStorage.setItem('dude.gallery.panes', 'dark')")
    page.reload()
    expect(page.get_by_role("heading", name="Tokens", exact=True)).to_be_visible()


def secret_block(page: Page):
    single_pane(page)
    page.get_by_role("link", name="SecretField", exact=True).click()
    return page.locator("#p-secret-field")


def ax_textboxes(page: Page, name: str) -> list[dict]:
    """The gallery's SecretFields of this name in the accessibility tree, as
    Chrome builds it: text fields described by the field's hint."""
    session = page.context.new_cdp_session(page)
    try:
        tree = session.send("Accessibility.getFullAXTree")
    finally:
        session.detach()
    return [
        node
        for node in tree["nodes"]
        if node.get("role", {}).get("value") == "textbox"
        and node.get("name", {}).get("value") == name
        and "Saved once." in str(node.get("description", {}).get("value", ""))
    ]


def ax_value(node: dict) -> str:
    return str(node.get("value", {}).get("value", ""))


def test_a_masked_secret_is_a_password_field_with_no_value_in_the_accessibility_tree(gallery_page: Page, console_errors: list):
    block = secret_block(gallery_page)
    # The gallery's second field labelled Value holds a PEM, masked; its
    # field (label, control, length line) is two levels above the control.
    field = block.get_by_label("Value", exact=True).nth(1)
    demo = field.locator("xpath=../..")
    expect(field).to_have_attribute("type", "password")
    expect(demo.get_by_text("4 lines · 157 characters", exact=True)).to_be_visible()
    field.focus()

    masked = ax_textboxes(gallery_page, "Value")
    assert len(masked) == 2, f"want the two masked Value fields in the accessibility tree, got {masked}"
    for node in masked:
        assert BODY_LINE not in ax_value(node), f"a masked field's value is in the accessibility tree: {node}"
    assert BODY_LINE not in gallery_page.locator("body").inner_text()

    show = demo.get_by_role("button", name="Show value", exact=True)
    expect(show).to_have_attribute("aria-pressed", "false")
    show.click()
    hide = demo.get_by_role("button", name="Hide value", exact=True)
    expect(hide).to_have_attribute("aria-pressed", "true")
    shown = demo.get_by_label("Value", exact=True)
    expect(shown).to_have_js_property("tagName", "TEXTAREA")
    expect(shown).to_have_value(PEM)
    expect(shown).to_be_focused()
    assert any(ax_value(n) == PEM for n in ax_textboxes(gallery_page, "Value")), "the shown value is not in the accessibility tree"
    expect(demo.get_by_text("4 lines · 157 characters", exact=True)).to_be_visible()

    hide.click()
    expect(demo.get_by_label("Value", exact=True)).to_have_attribute("type", "password")
    for node in ax_textboxes(gallery_page, "Value"):
        assert BODY_LINE not in ax_value(node), f"masked again, the value is in the accessibility tree: {node}"
    assert console_errors == [], console_errors


def test_a_paste_while_masked_keeps_every_line(gallery_page: Page, console_errors: list):
    block = secret_block(gallery_page)
    # The first field is empty and masked.
    field = block.get_by_label("Value", exact=True).first
    demo = field.locator("xpath=../..")
    expect(field).to_have_attribute("type", "password")
    field.focus()
    gallery_page.evaluate(
        """text => {
            const data = new DataTransfer();
            data.setData("text/plain", text);
            document.activeElement.dispatchEvent(new ClipboardEvent("paste", { clipboardData: data, bubbles: true, cancelable: true }));
        }""",
        PEM,
    )
    expect(demo.get_by_text("4 lines · 157 characters", exact=True)).to_be_visible()
    demo.get_by_role("button", name="Show value", exact=True).click()
    expect(demo.get_by_label("Value", exact=True)).to_have_value(PEM)
    assert console_errors == [], console_errors


def test_a_block_setting_row_lays_its_table_out_at_the_control_columns_width(gallery_page: Page):
    single_pane(gallery_page)
    gallery_page.get_by_role("link", name="Settings page", exact=True).click()
    layout = gallery_page.locator("#s-layout")
    table = layout.get_by_role("table", name="Secrets", exact=True)
    expect(table).to_be_visible()
    # The row's control column: the table's wrapper's parent.
    control = table.locator("xpath=../..")
    box, column = table.bounding_box(), control.bounding_box()
    assert box and column and column["width"] > 200, (box, column)
    # The table fills its column, and the Add button sits under it, not beside it.
    assert abs(box["width"] - column["width"]) <= 1, (box, column)
    add = layout.get_by_role("button", name="Add secret", exact=True)
    add_box = add.bounding_box()
    assert add_box and add_box["y"] >= box["y"] + box["height"] - 1, (add_box, box)


def masked_demo(page: Page, value: str, revealed: bool = False):
    """The gallery's first SecretField holding value, masked unless revealed:
    its demo block and its field."""
    block = secret_block(page)
    demo = block.get_by_label("Value", exact=True).first.locator("xpath=../..")
    demo.get_by_role("button", name="Show value", exact=True).click()
    demo.get_by_label("Value", exact=True).fill(value)
    if not revealed:
        demo.get_by_role("button", name="Hide value", exact=True).click()
    field = demo.get_by_label("Value", exact=True)
    field.focus()
    return demo, field


def compose(page: Page, steps: list[str]) -> None:
    """An IME composition through Chrome's own input pipeline: each step an
    update of the composed text, the last one committed."""
    session = page.context.new_cdp_session(page)
    try:
        for text in steps:
            session.send("Input.imeSetComposition", {"text": text, "selectionStart": len(text), "selectionEnd": len(text)})
        session.send("Input.insertText", {"text": steps[-1]})
    finally:
        session.detach()


# The masked field shows the value without its line breaks; offsets are the
# masked field's. Repeated characters make an edit's place ambiguous from the
# field's value alone, so these pin that the place edited is the one used.
@pytest.mark.parametrize(
    "value,start,end,action,want",
    [
        pytest.param("ab\ncd", 1, 1, "type:x", "axb\ncd", id="typing-unique"),
        pytest.param("a\na", 0, 0, "type:a", "aa\na", id="typing-repeated"),
        pytest.param("a\nb", 0, 0, "type:a", "aa\nb", id="typing-repeated-first-line"),
        pytest.param("one\ntwo", 6, 6, "type:!", "one\ntwo!", id="typing-at-the-end"),
        pytest.param("ab\ncd", 1, 3, "Backspace", "ad", id="selected-delete-across-a-break"),
        pytest.param("one\ntwo", 2, 4, "Backspace", "onwo", id="selected-delete-across-a-break-unique"),
        pytest.param("one\ntwo!", 1, 4, "Backspace", "owo!", id="selected-delete-across-a-break-control"),
        pytest.param("aa\naa", 1, 3, "Backspace", "aa", id="selected-delete-repeated"),
        pytest.param("a\na", 0, 1, "Backspace", "\na", id="selected-delete-first-of-two"),
        pytest.param("a\naa", 0, 2, "Backspace", "a", id="selected-delete-repeated-across-a-break"),
        pytest.param("a\na", 1, 1, "Backspace", "\na", id="backspace-before-a-break-keeps-it"),
        pytest.param("a\na", 1, 1, "Delete", "a\n", id="delete-after-a-break-keeps-it"),
        pytest.param("ab\nb", 2, 2, "Backspace", "a\nb", id="backspace-repeated-keeps-the-break"),
    ],
)
def test_a_masked_edit_applies_where_it_was_made(gallery_page: Page, console_errors: list, value, start, end, action, want):
    demo, field = masked_demo(gallery_page, value)
    field.evaluate("(el, range) => el.setSelectionRange(...range)", [start, end])
    if action.startswith("type:"):
        gallery_page.keyboard.type(action.removeprefix("type:"))
    else:
        field.press(action)
    expect(field).to_have_value(want.replace("\n", ""))
    demo.get_by_role("button", name="Show value", exact=True).click()
    expect(demo.get_by_label("Value", exact=True)).to_have_value(want)
    assert console_errors == [], console_errors


@pytest.mark.parametrize("revealed", [False, True], ids=["masked", "revealed"])
@pytest.mark.parametrize(
    "value,steps,want",
    [
        pytest.param("ab\ncd", ["x", "xy"], "xyab\ncd", id="unique"),
        pytest.param("a\na", ["a"], "aa\na", id="repeated"),
        pytest.param("a\na", ["a", "ab"], "aba\na", id="repeated-updated"),
    ],
)
def test_an_ime_composition_lands_at_the_caret(gallery_page: Page, console_errors: list, revealed: bool, value, steps, want):
    demo, field = masked_demo(gallery_page, value, revealed=revealed)
    field.evaluate("el => el.setSelectionRange(0, 0)")
    compose(gallery_page, steps)
    if not revealed:
        expect(field).to_have_value(want.replace("\n", ""))
        demo.get_by_role("button", name="Show value", exact=True).click()
    expect(demo.get_by_label("Value", exact=True)).to_have_value(want)
    assert console_errors == [], console_errors
