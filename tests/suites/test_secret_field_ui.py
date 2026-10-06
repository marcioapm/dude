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
