"""The web app, driven in a browser, delivering a task to a pull request.

What an operator actually does: open the board, create a task, press
Deliver, and watch the pipeline advance until a pull request exists — then
leave a comment on the forge and watch a fixer answer it. Every step is a
click or a read of the page; nothing reaches around the UI except the forge
side, which is where a person would comment and merge.

Runs against the local stand-in for GitHub, so it needs no network and no
token.
"""

from __future__ import annotations

import re

import pytest
from playwright.sync_api import Page, expect

from fake_github import FakeGitHub
from helpers import ApiClient, create_api_key, sign_in, toast, wait_until

pytestmark = pytest.mark.ui


def test_the_board_shows_projects_and_opens_tasks(
    page: Page, web_url: str, client: ApiClient, forge_project: dict, org: dict, console_errors: list
):
    client.create_task(forge_project["id"], "Already queued up")
    sign_in(page, web_url, org["api_key"])

    # With nothing selected, the first project's board is what opens.
    expect(page.get_by_text("Greeter").first).to_be_visible()
    card = page.get_by_text("Already queued up").last
    expect(card).to_be_visible()

    card.click()
    expect(page.get_by_test_id("task-screen")).to_be_visible()
    expect(page.get_by_test_id("deliver")).to_be_visible()
    assert console_errors == []


def test_delivering_from_the_ui_reaches_a_pull_request_and_back(
    page: Page,
    web_url: str,
    org: dict,
    forge_project: dict,
    fake_github: FakeGitHub,
    console_errors: list,
):
    sign_in(page, web_url, org["api_key"])

    # Create the task from the board, as an operator would.
    page.get_by_test_id("new-task").click()
    page.get_by_test_id("task-title").fill("Greet people by their full name")
    page.get_by_test_id("task-goal").fill("Use the full name, not just the first.")
    page.get_by_test_id("task-criteria").fill("- [ ] Greets with the full name")
    page.get_by_test_id("task-create-deliver").click()
    expect(page.get_by_test_id("task-screen")).to_be_visible()

    pipeline = page.get_by_test_id("pipeline")
    expect(pipeline).to_contain_text("Implement", timeout=60_000)
    # The reviewer raises something, the loop answers it, and a clean
    # re-review lets it through — all visible without a reload.
    expect(pipeline).to_contain_text("blocking", timeout=120_000)
    expect(pipeline).to_contain_text("no findings", timeout=120_000)
    # The fix says what woke it.
    expect(page.get_by_test_id("phase").filter(has_text="Fix")).to_contain_text("for the review")
    tabs = page.get_by_role("tablist", name="Task")
    tabs.get_by_role("tab", name="Findings").click()
    expect(page.get_by_test_id("findings")).to_be_visible()
    expect(page.get_by_test_id("finding").first).to_have_attribute("data-status", "resolved")

    # What the implementer published is there to read, rendered.
    tabs.get_by_role("tab", name="Files").click()
    notes = page.get_by_test_id("file-row").filter(has_text="NOTES.md")
    expect(notes).to_be_visible(timeout=60_000)
    notes.get_by_role("button").first.click()
    expect(page.get_by_test_id("file-viewer").get_by_role("heading", name="What changed")).to_be_visible()
    page.keyboard.press("Escape")

    # The PR appears as the last step, linked to the forge, and its one
    # state is a chip in the header that links there too.
    tabs.get_by_role("tab", name="Overview").click()
    expect(page.get_by_test_id("pr-step")).to_be_visible(timeout=180_000)
    chip = page.get_by_test_id("pr-link")
    expect(chip).to_have_attribute("data-pr-state", "awaiting")
    expect(chip).to_contain_text("Awaiting approval")
    pr_number = int(chip.get_attribute("href").rsplit("/", 1)[-1])
    assert pr_number in fake_github.pulls
    # The same state is on its row in the tree.
    expect(page.get_by_role("tree").locator('[data-pr-state="awaiting"]')).to_have_count(1)

    # Every agent in the pipeline opens its own conversation, on the task's
    # Sessions tab beside the others.
    page.get_by_test_id("phase").nth(1).click()
    expect(tabs.get_by_role("tab", name="Sessions")).to_have_attribute("aria-selected", "true")
    sessions = page.get_by_test_id("sessions")
    expect(sessions.locator('[aria-current="true"]')).to_contain_text("Review")
    expect(page.get_by_test_id("run-screen")).to_contain_text("Reviewer")
    # Back to the overview, and the URL says the task again.
    tabs.get_by_role("tab", name="Overview").click()
    expect(page).to_have_url(re.compile(r"#/task/"))
    # From a session's URL, going to its task (the breadcrumb, Back) is the overview.
    page.get_by_test_id("phase").nth(1).click()
    expect(tabs.get_by_role("tab", name="Sessions")).to_have_attribute("aria-selected", "true")
    page.go_back()
    expect(tabs.get_by_role("tab", name="Overview")).to_have_attribute("aria-selected", "true")

    # A person comments on the forge; the page shows a fixer answering.
    phases_before = page.get_by_test_id("phase").count()
    fake_github.comment(pr_number, "Please also handle an empty name.")
    expect(page.get_by_test_id("phase")).to_have_count(phases_before + 1, timeout=90_000)
    expect(page.get_by_test_id("phase").last).to_contain_text("Completed", timeout=120_000)

    # Merging on the forge finishes the task.
    fake_github.merge(pr_number)
    expect(page.get_by_test_id("task-header")).to_contain_text("Done", timeout=90_000)

    assert console_errors == []


def test_a_task_is_edited_and_moved_from_its_screen(
    page: Page, web_url: str, client: ApiClient, org: dict, forge_project: dict, console_errors: list
):
    epic = client.post(f"/v1/projects/{forge_project['id']}/epics", {"title": "Greetings"}).json()
    item = client.create_task(forge_project["id"], "Draft title")
    sign_in(page, web_url, org["api_key"])
    page.get_by_text("Draft title").first.click()
    expect(page.get_by_test_id("task-screen")).to_be_visible()

    page.get_by_test_id("edit-task").click()
    page.get_by_test_id("task-title").fill("Greet by full name")
    page.get_by_test_id("task-criteria").fill("- Uses the full name")
    page.get_by_role("combobox", name="Epic").click()
    page.get_by_role("listbox").get_by_text("Greetings").click()
    page.get_by_test_id("task-save").click()

    screen = page.get_by_test_id("task-screen")
    expect(screen.get_by_role("heading", level=1)).to_have_text("Greet by full name")
    expect(screen.get_by_role("list", name="Acceptance criteria")).to_contain_text("Uses the full name")
    saved = client.get(f"/v1/tasks/{item['id']}").json()
    assert saved["epicId"] == epic["id"]
    assert console_errors == []


GOAL_MARKDOWN = """Checkout dropped **SEPA** from the payment step.

## What exists today
- The old flow lives behind `checkout_v2`.
- Methods come from `GET /v1/billing/methods`."""


def test_a_task_is_written_in_markdown_and_its_criteria_are_the_list_items(
    page: Page, web_url: str, client: ApiClient, org: dict, forge_project: dict, console_errors: list
):
    sign_in(page, web_url, org["api_key"])
    page.get_by_test_id("new-task").click()
    page.get_by_test_id("task-title").fill("Keep SEPA at checkout")
    goal = page.get_by_test_id("task-goal")
    selected = "el => el.value.slice(el.selectionStart, el.selectionEnd)"

    # The toolbar's Bold wraps the selection, which stays selected.
    goal.fill("make it red")
    goal.evaluate("el => el.setSelectionRange(8, 11)")
    page.get_by_role("toolbar", name="Formatting").first.get_by_role("button", name="Bold", exact=True).click()
    expect(goal).to_have_value("make it **red**")
    assert goal.evaluate(selected) == "red"
    expect(goal).to_be_focused()
    # Ctrl+B again takes it off.
    goal.press("Control+b")
    expect(goal).to_have_value("make it red")
    assert goal.evaluate(selected) == "red"
    # Ctrl+B on, then Undo: the text is as it was.
    goal.press("Control+b")
    expect(goal).to_have_value("make it **red**")
    goal.press("ControlOrMeta+z")
    expect(goal).to_have_value("make it red")

    goal.fill(GOAL_MARKDOWN)
    criteria = page.get_by_test_id("task-criteria")
    criteria.fill("- [ ] SEPA appears on the payment step\n- Invoice only for **annual** plans")
    expect(page.get_by_test_id("task-criteria-count")).to_have_text("2 criteria")

    # Preview renders the goal through the safe Markdown path, heading and list.
    goal_view = page.get_by_role("tablist", name="Goal view")
    goal_view.get_by_role("tab", name="Preview").click()
    preview = page.get_by_test_id("task-goal-preview")
    expect(preview.get_by_role("heading", name="What exists today")).to_be_visible()
    expect(preview.get_by_role("listitem")).to_have_count(2)
    expect(preview.locator("strong")).to_have_text("SEPA")
    heading_style = "el => { const s = getComputedStyle(el); return [s.fontSize, s.marginTop, s.lineHeight]; }"
    previewed = preview.get_by_role("heading", name="What exists today").evaluate(heading_style)
    # ← goes back to Write, and the source is as it was typed.
    goal_view.get_by_role("tab", name="Preview").press("ArrowLeft")
    expect(goal_view.get_by_role("tab", name="Write")).to_have_attribute("aria-selected", "true")
    expect(page.get_by_test_id("task-goal")).to_have_value(GOAL_MARKDOWN)

    # Typing a list continues it: Enter after a task item opens the next one.
    criteria.focus()
    criteria.evaluate("el => el.setSelectionRange(el.value.length, el.value.length)")
    criteria.press("Enter")
    expect(criteria).to_have_value("- [ ] SEPA appears on the payment step\n- Invoice only for **annual** plans\n- ")
    criteria.press("Enter")
    expect(criteria).to_have_value("- [ ] SEPA appears on the payment step\n- Invoice only for **annual** plans\n")

    page.get_by_test_id("task-save").click()
    expect(page.get_by_test_id("task-screen")).to_be_visible()

    items = client.get("/v1/tasks", params={"projectId": forge_project["id"]}).json()["tasks"]
    task_id = next(i["id"] for i in items if i["title"] == "Keep SEPA at checkout")
    saved = client.get(f"/v1/tasks/{task_id}").json()
    assert saved["acceptanceCriteria"] == ["SEPA appears on the payment step", "Invoice only for **annual** plans"]
    assert saved["goal"] == GOAL_MARKDOWN

    # The task shows what the preview showed: the goal's heading, each criterion.
    screen = page.get_by_test_id("task-screen")
    expect(screen.get_by_role("heading", name="What exists today")).to_be_visible()
    # Set as it was previewed: the same size and the same space above it.
    assert screen.get_by_role("heading", name="What exists today").evaluate(heading_style) == previewed
    criteria_list = screen.get_by_role("list", name="Acceptance criteria")
    expect(criteria_list.locator(":scope > li")).to_have_count(2)
    expect(criteria_list.locator("strong")).to_have_text("annual")
    # The criteria are a section of their own, their items where the goal's list items are.
    expect(screen.get_by_role("region", name="Acceptance criteria").get_by_role("heading", name="Acceptance criteria")).to_be_visible()
    goal_item = screen.get_by_role("region", name="Goal").get_by_role("listitem").first
    left = "el => Math.round(el.getBoundingClientRect().left)"
    assert criteria_list.locator(":scope > li").first.evaluate(left) == goal_item.evaluate(left)

    # Editing opens the criteria as the list they were saved from.
    page.get_by_test_id("edit-task").click()
    expect(page.get_by_test_id("task-criteria")).to_have_value(
        "- [ ] SEPA appears on the payment step\n- [ ] Invoice only for **annual** plans")
    expect(page.get_by_test_id("task-goal")).to_have_value(GOAL_MARKDOWN)
    assert console_errors == []


BROKEN_GOAL = "Keep SEPA at checkout.\nKeep Invoice for annual plans.\n\nA second paragraph."
BROKEN_CRITERIA = "- [ ] SEPA appears\n  on the payment step"


def test_a_single_newline_a_person_types_is_a_line_break_in_preview_read_and_on_the_task(
    page: Page, web_url: str, client: ApiClient, org: dict, forge_project: dict, console_errors: list
):
    first_paragraph = "p:has-text('Keep SEPA at checkout.')"
    lines = "el => el.innerText.split('\\n')"
    sign_in(page, web_url, org["api_key"])
    page.get_by_test_id("new-task").click()
    page.get_by_test_id("task-title").fill("Line breaks")
    page.get_by_test_id("task-goal").fill(BROKEN_GOAL)
    page.get_by_test_id("task-criteria").fill(BROKEN_CRITERIA)

    page.get_by_role("tablist", name="Goal view").get_by_role("tab", name="Preview").click()
    para = page.get_by_test_id("task-goal-preview").locator(first_paragraph)
    expect(para.locator("br")).to_have_count(1)
    assert para.evaluate(lines) == ["Keep SEPA at checkout.", "Keep Invoice for annual plans."]
    criteria_view = page.get_by_role("tablist", name="Acceptance criteria view")
    criteria_view.get_by_role("tab", name="Preview").click()
    expect(page.get_by_test_id("task-criteria-preview").locator("li br")).to_have_count(1)

    page.get_by_test_id("task-read").click()
    doc = page.get_by_test_id("task-reading")
    expect(doc.locator(first_paragraph).locator("br")).to_have_count(1)
    expect(doc.locator("li br")).to_have_count(1)
    page.keyboard.press("Escape")

    page.get_by_test_id("task-save").click()
    screen = page.get_by_test_id("task-screen")
    expect(screen).to_be_visible()
    goal = screen.get_by_role("region", name="Goal").locator(first_paragraph)
    expect(goal.locator("br")).to_have_count(1)
    assert goal.evaluate(lines) == ["Keep SEPA at checkout.", "Keep Invoice for annual plans."]
    expect(screen.get_by_role("list", name="Acceptance criteria").locator("li br")).to_have_count(1)
    items = client.get("/v1/tasks", params={"projectId": forge_project["id"]}).json()["tasks"]
    saved = client.get(f"/v1/tasks/{next(i['id'] for i in items if i['title'] == 'Line breaks')}").json()
    assert saved["goal"] == BROKEN_GOAL
    assert saved["acceptanceCriteria"] == ["SEPA appears\non the payment step"]
    assert console_errors == []


def test_enter_in_the_title_moves_to_the_goal_and_does_not_create(
    page: Page, web_url: str, client: ApiClient, org: dict, forge_project: dict, console_errors: list
):
    sign_in(page, web_url, org["api_key"])
    page.get_by_test_id("new-task").click()
    title = page.get_by_test_id("task-title")
    page.get_by_test_id("task-goal").fill("Enter in the title must not create the task.")
    title.fill("Not yet")
    expect(page.get_by_test_id("task-save")).to_be_enabled()
    title.press("Enter")
    expect(page.get_by_test_id("task-goal")).to_be_focused()
    expect(title).to_have_value("Not yet")
    items = client.get("/v1/tasks", params={"projectId": forge_project["id"]}).json()["tasks"]
    assert [i for i in items if i["title"] == "Not yet"] == []
    # With the goal in Preview, Enter moves to its preview, not past it.
    page.get_by_role("tablist", name="Goal view").get_by_role("tab", name="Preview").click()
    title.focus()
    title.press("Enter")
    expect(page.get_by_test_id("task-goal-preview")).to_be_focused()
    assert console_errors == []


def test_creating_a_task_needs_a_goal_of_at_least_16_characters(
    page: Page, web_url: str, client: ApiClient, org: dict, forge_project: dict, console_errors: list
):
    sign_in(page, web_url, org["api_key"])
    page.get_by_test_id("new-task").click()
    dialog = page.get_by_role("dialog", name="New task")
    expect(dialog.locator("label", has_text="Goal")).to_have_text("Goal · required")
    page.get_by_test_id("task-title").fill("Show invoices in euros")
    create, deliver = page.get_by_test_id("task-save"), page.get_by_test_id("task-create-deliver")
    # Untouched, the goal says only that it is required.
    expect(create).to_be_disabled()
    expect(deliver).to_be_disabled()
    expect(dialog).not_to_contain_text("more character")

    goal = page.get_by_test_id("task-goal")
    goal.fill("Bill in euros")
    expect(dialog.get_by_text("3 more characters to save: why it matters and what should change.", exact=True)).to_be_visible()
    expect(create).to_be_disabled()
    # Whitespace around it does not count; nor does Ctrl/⌘+Enter save it.
    goal.fill("   Bill in euros \n\n")
    expect(create).to_be_disabled()
    goal.press("ControlOrMeta+Enter")
    goal.fill("Bill EU customers")
    expect(dialog).not_to_contain_text("more character")
    expect(create).to_be_enabled()
    expect(deliver).to_be_enabled()
    create.click()
    expect(page.get_by_test_id("task-screen")).to_be_visible()
    items = client.get("/v1/tasks", params={"projectId": forge_project["id"]}).json()["tasks"]
    assert [i["goal"] for i in items if i["title"] == "Show invoices in euros"] == ["Bill EU customers"]
    assert console_errors == []


# The task dialog's main column, its Goal frame and its Criteria field, measured.
_TASK_COLUMN = """() => {
  const main = document.querySelector('[data-testid="task-title"]').closest('form').parentElement;
  const goal = document.querySelector('[data-testid="task-goal"]').closest('[data-mode]');
  const criteria = document.querySelector('[data-testid="task-criteria"]').closest('[data-mode]').parentElement;
  const stack = criteria.parentElement;
  const cs = getComputedStyle(main);
  return {
    scrollHeight: main.scrollHeight, clientHeight: main.clientHeight,
    gap: parseFloat(getComputedStyle(stack).rowGap),
    goalBottom: goal.getBoundingClientRect().bottom,
    criteriaTop: criteria.getBoundingClientRect().top,
    criteriaBottom: criteria.getBoundingClientRect().bottom,
    columnInnerBottom: main.getBoundingClientRect().top + main.clientHeight - parseFloat(cs.paddingBottom),
  };
}"""


@pytest.mark.parametrize("height,density", [(900, "comfortable"), (720, "comfortable"), (720, "compact")])
def test_the_empty_task_dialog_opens_without_scrolling_and_its_goal_fills_the_column(
    page: Page, web_url: str, org: dict, forge_project: dict, console_errors: list, height: int, density: str
):
    page.set_viewport_size({"width": 1280, "height": height})
    sign_in(page, web_url, org["api_key"])
    page.evaluate(f"localStorage.setItem('dude.density', '{density}')")
    page.reload()
    expect(page.locator(f"[data-density={density}]").first).to_be_attached()
    page.get_by_test_id("new-task").click()
    dialog = page.get_by_role("dialog", name="New task")
    expect(dialog.get_by_text(forge_project["name"], exact=True)).to_be_visible()
    page.wait_for_function(
        "document.querySelector('[role=dialog]').getAnimations().every(a => a.playState === 'finished')")

    m = page.evaluate(_TASK_COLUMN)
    assert m["scrollHeight"] <= m["clientHeight"], m
    # The Goal takes what is left: it ends a gap above the criteria, which end at the column's foot.
    assert abs(m["criteriaTop"] - m["gap"] - m["goalBottom"]) <= 2, m
    assert abs(m["columnInnerBottom"] - m["criteriaBottom"]) <= 2, m

    # Past what the column holds, the Goal grows with its text and the column scrolls; the editor does not.
    goal = page.get_by_test_id("task-goal")
    goal.fill("\n".join(f"Line {i}" for i in range(80)))
    m = page.evaluate(_TASK_COLUMN)
    assert m["scrollHeight"] > m["clientHeight"], m
    assert goal.evaluate("el => el.scrollHeight <= el.clientHeight"), "the Goal scrolls inside itself"
    assert console_errors == []


def test_on_a_phone_the_task_dialogs_writing_fills_the_screen_before_the_aside(
    page: Page, web_url: str, org: dict, forge_project: dict, console_errors: list
):
    page.set_viewport_size({"width": 375, "height": 812})
    sign_in(page, web_url, org["api_key"])
    page.get_by_test_id("new-task").click()
    expect(page.get_by_role("dialog", name="New task").get_by_text(forge_project["name"], exact=True)).to_be_visible()
    page.wait_for_function(
        "document.querySelector('[role=dialog]').getAnimations().every(a => a.playState === 'finished')")
    m = page.evaluate(_TASK_COLUMN)
    # One scroll for the writing and the aside after it; the writing is the first screenful, filled.
    scroller = page.evaluate("""() => {
      const main = document.querySelector('[data-testid="task-title"]').closest('form').parentElement;
      return { main: main.getBoundingClientRect().height, view: main.parentElement.clientHeight };
    }""")
    assert abs(scroller["main"] - scroller["view"]) <= 2, scroller
    assert abs(m["criteriaTop"] - m["gap"] - m["goalBottom"]) <= 2, m
    assert abs(m["columnInnerBottom"] - m["criteriaBottom"]) <= 2, m
    assert console_errors == []


def test_the_task_dialog_refits_when_the_window_shrinks_in_write_and_in_preview(
    page: Page, web_url: str, org: dict, forge_project: dict, console_errors: list
):
    page.set_viewport_size({"width": 1280, "height": 900})
    sign_in(page, web_url, org["api_key"])
    page.get_by_test_id("new-task").click()
    expect(page.get_by_role("dialog", name="New task").get_by_text(forge_project["name"], exact=True)).to_be_visible()
    page.wait_for_function(
        "document.querySelector('[role=dialog]').getAnimations().every(a => a.playState === 'finished')")

    def fits() -> dict:
        m = page.evaluate(_TASK_COLUMN)
        assert m["scrollHeight"] <= m["clientHeight"], m
        assert abs(m["criteriaTop"] - m["gap"] - m["goalBottom"]) <= 2, m
        assert abs(m["columnInnerBottom"] - m["criteriaBottom"]) <= 2, m
        return m

    # Write, in the same open dialog: 900 → 720 → 900.
    tall = fits()
    page.set_viewport_size({"width": 1280, "height": 720})
    short = fits()
    assert short["goalBottom"] < tall["goalBottom"] - 100
    page.set_viewport_size({"width": 1280, "height": 900})
    fits()

    # Preview entered at 900, then 720: it gives the surplus back, and fills again at 900.
    goal_view = page.get_by_role("tablist", name="Goal view")
    page.get_by_test_id("task-goal").fill("A goal of one short line.")
    goal_view.get_by_role("tab", name="Preview").click()
    expect(page.get_by_test_id("task-goal-preview")).to_contain_text("A goal of one short line.")
    fits()
    page.set_viewport_size({"width": 1280, "height": 720})
    fits()
    page.set_viewport_size({"width": 1280, "height": 900})
    fits()

    # A long source still keeps its height in Preview; the column scrolls.
    goal_view.get_by_role("tab", name="Write").click()
    page.get_by_test_id("task-goal").fill("\n\n".join(f"Line {i}" for i in range(40)))
    source = page.get_by_test_id("task-goal").evaluate("el => el.offsetHeight")
    goal_view.get_by_role("tab", name="Preview").click()
    preview = page.get_by_test_id("task-goal-preview")
    expect(preview).to_contain_text("Line 39")
    assert preview.evaluate("el => el.offsetHeight") >= source
    m = page.evaluate(_TASK_COLUMN)
    assert m["scrollHeight"] > m["clientHeight"], m
    assert console_errors == []


READ_CRITERIA = "- [ ] SEPA appears on the payment step\n- Invoice only for annual plans,\nnever monthly ones"


def _assert_reads_as_the_task(page: Page, title: str) -> None:
    doc = page.get_by_test_id("task-reading")
    expect(doc).to_be_visible()
    expect(doc.get_by_role("heading", level=1)).to_have_text(title)
    expect(doc.get_by_role("heading", name="What exists today")).to_be_visible()
    expect(doc.get_by_role("heading", name="Acceptance criteria")).to_be_visible()
    # The criteria as a checklist of what would be saved: the lazy line is part of the second, on a line of its own.
    checklist = doc.locator("li:has(> [aria-hidden] svg)")
    expect(checklist).to_have_count(2)
    expect(checklist.nth(0)).to_have_text("SEPA appears on the payment step")
    expect(checklist.nth(1).locator("br")).to_have_count(1)
    assert checklist.nth(1).evaluate("el => el.innerText.trim().split('\\n')") == [
        "Invoice only for annual plans,", "never monthly ones"]
    # The fields and the aside are out of sight, and out of reach.
    expect(page.get_by_test_id("task-goal")).not_to_be_visible()
    expect(page.get_by_test_id("task-criteria")).not_to_be_visible()
    expect(page.get_by_test_id("task-title")).not_to_be_visible()
    expect(page.get_by_role("complementary", name="Where it sits")).to_have_count(0)


def test_read_shows_the_task_as_one_document_and_escape_goes_back_to_writing(
    page: Page, web_url: str, org: dict, forge_project: dict, console_errors: list
):
    sign_in(page, web_url, org["api_key"])
    page.get_by_test_id("new-task").click()
    dialog = page.get_by_role("dialog", name="New task")
    page.get_by_test_id("task-title").fill("Keep SEPA at checkout")
    goal = page.get_by_test_id("task-goal")
    goal.fill(GOAL_MARKDOWN + "\n" + "\n".join(f"Line {i}" for i in range(60)))
    criteria = page.get_by_test_id("task-criteria")
    criteria.fill(READ_CRITERIA)
    # The goal in Preview, the column scrolled, the caret in the criteria: all of it comes back.
    page.get_by_role("tablist", name="Goal view").get_by_role("tab", name="Preview").click()
    criteria.focus()
    criteria.evaluate("el => el.setSelectionRange(6, 10)")
    column = "() => document.querySelector('[data-testid=task-title]').closest('form').parentElement.scrollTop"
    scrolled = page.evaluate(column)
    assert scrolled > 0
    heading_style = "el => { const s = getComputedStyle(el); return [s.fontSize, s.marginTop, s.lineHeight]; }"
    previewed = page.get_by_test_id("task-goal-preview").get_by_role("heading", name="What exists today").evaluate(heading_style)

    read = page.get_by_test_id("task-read")
    expect(read).to_have_text("Read")
    read.click()
    expect(read).to_have_text("Back to writing")
    _assert_reads_as_the_task(page, "Keep SEPA at checkout")
    # Read is set as the Goal's Preview (and the task screen) are: the message rhythm.
    assert page.get_by_test_id("task-reading").get_by_role("heading", name="What exists today").evaluate(heading_style) == previewed
    expect(dialog).to_contain_text("back to writing")

    # Escape leaves Read, not the dialog, and asks nothing.
    page.keyboard.press("Escape")
    expect(page.get_by_test_id("task-reading")).to_have_count(0)
    expect(dialog).to_be_visible()
    expect(page.get_by_role("dialog", name="Discard this task?")).to_have_count(0)
    expect(read).to_have_text("Read")
    expect(criteria).to_have_value(READ_CRITERIA)
    expect(page.get_by_role("tablist", name="Goal view").get_by_role("tab", name="Preview")).to_have_attribute("aria-selected", "true")
    assert page.evaluate(column) == scrolled
    expect(criteria).to_be_focused()
    assert criteria.evaluate("el => [el.selectionStart, el.selectionEnd]") == [6, 10]

    # The shortcut toggles both ways, from inside a field.
    criteria.press("ControlOrMeta+Shift+r")
    _assert_reads_as_the_task(page, "Keep SEPA at checkout")
    page.keyboard.press("ControlOrMeta+Shift+r")
    expect(page.get_by_test_id("task-reading")).to_have_count(0)
    expect(criteria).to_be_focused()

    # An empty title reads as untitled; an empty goal and no criteria leave their sections out.
    page.get_by_test_id("task-title").fill("")
    goal_view = page.get_by_role("tablist", name="Goal view")
    goal_view.get_by_role("tab", name="Write").click()
    goal.fill("")
    criteria.fill("")
    read.click()
    doc = page.get_by_test_id("task-reading")
    expect(doc.get_by_role("heading")).to_have_count(1)
    expect(doc.get_by_role("heading", level=1)).to_have_text("Untitled task")
    assert console_errors == []


def test_a_task_saves_from_read_and_a_locked_task_reads_too(
    page: Page, web_url: str, client: ApiClient, org: dict, forge_project: dict, console_errors: list
):
    sign_in(page, web_url, org["api_key"])
    page.get_by_test_id("new-task").click()
    page.get_by_test_id("task-title").fill("Read then saved")
    page.get_by_test_id("task-goal").fill(GOAL_MARKDOWN)
    page.get_by_test_id("task-criteria").fill(READ_CRITERIA)
    page.get_by_test_id("task-read").click()
    expect(page.get_by_test_id("task-reading")).to_be_visible()
    # Ctrl/⌘+Enter saves from Read, as it does from the fields.
    page.keyboard.press("ControlOrMeta+Enter")
    expect(page.get_by_test_id("task-screen")).to_be_visible()
    items = client.get("/v1/tasks", params={"projectId": forge_project["id"]}).json()["tasks"]
    task_id = next(i["id"] for i in items if i["title"] == "Read then saved")
    saved = client.get(f"/v1/tasks/{task_id}").json()
    assert saved["acceptanceCriteria"] == ["SEPA appears on the payment step", "Invoice only for annual plans,\nnever monthly ones"]

    # Editing, the footer's Save saves from Read too: its form is under the reading, inert, and still submits.
    page.get_by_test_id("edit-task").click()
    page.get_by_test_id("task-title").fill("Read then saved again")
    page.get_by_test_id("task-goal").fill("Saved with the mouse, from Read.")
    page.get_by_test_id("task-read").click()
    expect(page.get_by_test_id("task-reading")).to_be_visible()
    page.get_by_test_id("task-save").click()
    expect(page.get_by_role("dialog", name="Edit task")).to_have_count(0)
    edited = client.get(f"/v1/tasks/{task_id}").json()
    assert (edited["title"], edited["goal"]) == ("Read then saved again", "Saved with the mouse, from Read.")
    assert edited["acceptanceCriteria"] == saved["acceptanceCriteria"]
    page.get_by_test_id("edit-task").click()
    page.get_by_test_id("task-title").fill("Read then saved")
    page.get_by_test_id("task-goal").fill(GOAL_MARKDOWN)
    page.get_by_test_id("task-save").click()
    expect(page.get_by_role("dialog", name="Edit task")).to_have_count(0)

    # Once delivery has started it is fixed, and still reads.
    assert client.post(f"/v1/tasks/{task_id}/deliver").status_code == 201
    page.reload()
    expect(page.get_by_test_id("task-screen")).to_be_visible()
    expect(page.get_by_test_id("phase").first).to_be_visible(timeout=30_000)
    page.get_by_test_id("edit-task").click()
    dialog = page.get_by_role("dialog", name="Edit task")
    expect(dialog).to_contain_text("Delivery has started")
    page.get_by_test_id("task-read").click()
    _assert_reads_as_the_task(page, "Read then saved")
    page.keyboard.press("Escape")
    expect(page.get_by_test_id("task-reading")).to_have_count(0)
    expect(dialog).to_be_visible()
    expect(page.get_by_test_id("task-goal-preview")).to_be_visible()
    assert console_errors == []


def test_read_on_a_phone_is_the_same_document(
    page: Page, web_url: str, org: dict, forge_project: dict, console_errors: list
):
    page.set_viewport_size({"width": 375, "height": 812})
    sign_in(page, web_url, org["api_key"])
    page.get_by_test_id("new-task").click()
    page.get_by_test_id("task-title").fill("Keep SEPA at checkout")
    page.get_by_test_id("task-goal").fill(GOAL_MARKDOWN)
    page.get_by_test_id("task-criteria").fill(READ_CRITERIA)
    page.get_by_test_id("task-read").click()
    _assert_reads_as_the_task(page, "Keep SEPA at checkout")
    # The document fits the screen's width, and the buttons stay on screen.
    doc = page.get_by_test_id("task-reading")
    assert doc.evaluate("el => el.getBoundingClientRect().right") <= 375
    expect(page.get_by_test_id("task-save")).to_be_in_viewport()
    page.keyboard.press("Escape")
    expect(page.get_by_test_id("task-title")).to_have_value("Keep SEPA at checkout")
    expect(page.get_by_test_id("task-title")).to_be_visible()
    assert console_errors == []


def test_the_read_shortcut_waits_for_an_open_select_to_close(
    page: Page, web_url: str, client: ApiClient, org: dict, forge_project: dict, console_errors: list
):
    client.post(f"/v1/projects/{forge_project['id']}/epics", {"title": "Epic A"})
    client.post(f"/v1/projects/{forge_project['id']}/epics", {"title": "Epic B"})
    sign_in(page, web_url, org["api_key"])
    page.get_by_test_id("new-task").click()
    page.get_by_test_id("task-title").fill("Read over a popup")
    epic = page.get_by_role("combobox", name="Epic")
    epic.click()
    listbox = page.get_by_role("listbox")
    option = listbox.get_by_role("option", name="Epic A")
    option.focus()
    expect(option).to_be_focused()

    # The listbox lives outside the dialog: the shortcut is taken (no reload) but Read waits.
    page.evaluate("""() => { window.readKey = null;
      document.addEventListener('keydown', e => { if (e.key.toLowerCase() === 'r') window.readKey = e.defaultPrevented; }); }""")
    page.keyboard.press("ControlOrMeta+Shift+r")
    assert page.evaluate("window.readKey") is True
    expect(page.get_by_test_id("task-reading")).to_have_count(0)
    expect(listbox).to_be_visible()
    expect(page.get_by_test_id("task-read")).to_have_text("Read")

    # Closed, the Select has focus back; Read and back to writing return to it, not to the option.
    page.keyboard.press("Escape")
    expect(listbox).to_have_count(0)
    expect(page.get_by_role("dialog", name="New task")).to_be_visible()
    expect(epic).to_be_focused()
    page.keyboard.press("ControlOrMeta+Shift+r")
    expect(page.get_by_test_id("task-reading")).to_be_visible()
    page.keyboard.press("Escape")
    expect(page.get_by_test_id("task-reading")).to_have_count(0)
    expect(epic).to_be_focused()
    expect(epic).to_have_text("No epic")
    assert console_errors == []


def test_the_task_dialog_does_not_move_when_where_it_sits_arrives(
    page: Page, web_url: str, org: dict, forge_project: dict, console_errors: list
):
    sign_in(page, web_url, org["api_key"])
    held = []
    page.route(f"**/v1/projects/{forge_project['id']}",
               lambda route: held.append(route) if route.request.method == "GET" else route.continue_())
    page.get_by_test_id("new-task").click()
    title = page.get_by_test_id("task-title")
    expect(title).to_be_visible()
    # Route handlers run while Playwright is called, so poll through it.
    for _ in range(100):
        if held:
            break
        page.wait_for_timeout(50)
    assert held, "the project was never asked for"
    # The dialog pops in with a scale; measure once it has settled.
    page.wait_for_function(
        "document.querySelector('[role=dialog]').getAnimations().every(a => a.playState === 'finished')")
    before = title.bounding_box()["y"]
    for route in held:
        route.continue_()
    dialog = page.get_by_role("dialog", name="New task")
    expect(dialog.get_by_text(forge_project["name"], exact=True)).to_be_visible()
    assert title.bounding_box()["y"] == before
    # A plain path: nothing to navigate, so no landmark.
    expect(dialog.get_by_role("navigation")).to_have_count(0)
    assert console_errors == []


def test_a_task_dialog_whose_project_fails_to_load_shows_no_loading_line(
    page: Page, web_url: str, org: dict, forge_project: dict, console_errors: list
):
    sign_in(page, web_url, org["api_key"])
    held = []
    page.route(f"**/v1/projects/{forge_project['id']}",
               lambda route: held.append(route) if route.request.method == "GET" else route.continue_())
    page.get_by_test_id("new-task").click()
    dialog = page.get_by_role("dialog", name="New task")
    title = page.get_by_test_id("task-title")
    expect(title).to_be_visible()
    for _ in range(100):
        if held:
            break
        page.wait_for_timeout(50)
    assert held, "the project was never asked for"
    page.wait_for_function(
        "document.querySelector('[role=dialog]').getAnimations().every(a => a.playState === 'finished')")
    running = "d => d.getAnimations({ subtree: true }).filter(a => a.playState === 'running').length"
    # While it loads, the context line pulses.
    assert dialog.evaluate(running) > 0
    before = title.bounding_box()["y"]
    reason = "The project is not available right now."
    for route in held:
        route.fulfill(status=500, content_type="application/json",
                      body='{"error":{"code":"internal","message":"%s"}}' % reason)
    alert = dialog.get_by_role("alert")
    expect(alert).to_have_text(reason)
    # Once it has failed nothing says it is still loading, and the fields stay where they were.
    assert dialog.evaluate(running) == 0
    expect(dialog.locator("[aria-busy=true]")).to_have_count(0)
    assert title.bounding_box()["y"] == before
    assert all("500" in e for e in console_errors), console_errors


def test_a_task_that_fails_to_save_says_why_beside_the_buttons(
    page: Page, web_url: str, org: dict, forge_project: dict, console_errors: list
):
    """The document's column scrolls, so the reason shows in the footer, where the button was pressed."""
    sign_in(page, web_url, org["api_key"])
    page.get_by_test_id("new-task").click()
    page.get_by_test_id("task-title").fill("Refused")
    page.get_by_test_id("task-goal").fill("\n".join(f"Line {i}" for i in range(60)))
    reason = "Tasks cannot be created in this project right now."

    def refuse(route):
        if route.request.method == "POST":
            route.fulfill(status=400, content_type="application/json",
                          body='{"error":{"code":"invalid","message":"%s"}}' % reason)
        else:
            route.continue_()

    page.route("**/v1/tasks", refuse)
    page.get_by_test_id("task-save").click()
    alert = page.get_by_role("dialog", name="New task").get_by_role("alert")
    expect(alert).to_have_text(reason)
    expect(alert).to_be_in_viewport()
    assert alert.get_attribute("title") == reason
    # The key hints give way to it.
    expect(page.get_by_role("dialog", name="New task")).not_to_contain_text("toggle preview")
    # On a phone the hints are gone, but the reason and the button stay on screen.
    page.set_viewport_size({"width": 375, "height": 812})
    expect(alert).to_have_text(reason)
    expect(alert).to_be_in_viewport()
    expect(page.get_by_test_id("task-save")).to_be_in_viewport()
    assert all("400" in e for e in console_errors), console_errors


def test_a_task_takes_a_64k_goal_and_16k_of_criteria_and_no_more(
    page: Page, web_url: str, client: ApiClient, org: dict, forge_project: dict, console_errors: list
):
    def titled(title: str) -> list[dict]:
        items = client.get("/v1/tasks", params={"projectId": forge_project["id"]}).json()["tasks"]
        return [i for i in items if i["title"] == title]

    sign_in(page, web_url, org["api_key"])
    page.get_by_test_id("new-task").click()
    page.get_by_test_id("task-title").fill("Long criterion")
    goal = page.get_by_test_id("task-goal")
    criteria = page.get_by_test_id("task-criteria")

    # The editors stop at their limits: a longer goal or criteria source is cut there, and typing adds nothing.
    goal.fill("g" * 65_537)
    assert goal.evaluate("el => el.value.length") == 65_536
    goal.press("End")
    page.keyboard.insert_text("more")
    assert goal.evaluate("el => el.value.length") == 65_536
    criteria.fill("- " + "c" * 16_400)
    assert criteria.evaluate("el => el.value.length") == 16_384

    # One criterion far past the old 2,000 each saves whole, with a goal at exactly the limit.
    criteria.fill("- " + "x" * 3000)
    expect(page.get_by_test_id("task-save")).to_be_enabled()
    criteria.press("ControlOrMeta+Enter")
    expect(page.get_by_test_id("task-screen")).to_be_visible()
    saved = titled("Long criterion")
    assert len(saved) == 1
    detail = client.get(f"/v1/tasks/{saved[0]['id']}").json()
    assert detail["acceptanceCriteria"] == ["x" * 3000]
    assert len(detail["goal"]) == 65_536
    assert detail["runs"] == []
    assert console_errors == []


def test_criteria_that_open_over_the_editors_limit_block_saving_until_they_fit(
    page: Page, web_url: str, client: ApiClient, org: dict, forge_project: dict, console_errors: list
):
    """Saved criteria within 16K open as a list whose markers take it past the editor's limit."""
    item = client.create_task(forge_project["id"], "Near the limit", acceptanceCriteria=["y" * 2048] * 8)
    sign_in(page, web_url, org["api_key"])
    page.get_by_text("Near the limit").first.click()
    page.get_by_test_id("edit-task").click()
    criteria = page.get_by_test_id("task-criteria")
    # Eight "- [ ] " and seven newlines: 16,384 + 55.
    assert criteria.evaluate("el => el.value.length") == 16_439
    expect(page.get_by_text("55 characters over the limit; shorten it to save.", exact=True)).to_be_visible()
    expect(page.get_by_role("dialog", name="Edit task").get_by_text("16,439 / 16,384")).to_be_visible()
    expect(page.get_by_test_id("task-save")).to_be_disabled()
    task_url = f"/v1/tasks/{item['id']}"
    attempts = []

    def record_patch(request):
        if request.method == "PATCH" and request.url.endswith(task_url):
            attempts.append(request)

    page.on("request", record_patch)
    criteria.press("ControlOrMeta+Enter")
    # The save handler sends synchronously; two frames drain its request event, with no fixed wait.
    page.evaluate("() => new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve)))")
    assert attempts == []
    expect(criteria).to_be_visible()
    page.remove_listener("request", record_patch)

    # Shortened to fit, it saves.
    criteria.evaluate("el => el.setSelectionRange(el.value.length - 55, el.value.length)")
    criteria.press("Delete")
    expect(page.get_by_test_id("task-save")).to_be_enabled()
    page.get_by_test_id("task-save").click()
    expect(criteria).to_have_count(0)
    saved = client.get(f"/v1/tasks/{item['id']}").json()["acceptanceCriteria"]
    assert len(saved) == 8 and saved[-1] == "y" * (2048 - 55)
    assert console_errors == []


def test_closing_a_task_with_writing_in_it_asks_first(
    page: Page, web_url: str, org: dict, forge_project: dict, console_errors: list
):
    sign_in(page, web_url, org["api_key"])
    page.get_by_test_id("new-task").click()
    # A few words close as before.
    page.get_by_test_id("task-title").fill("Short")
    page.keyboard.press("Escape")
    expect(page.get_by_test_id("task-title")).to_have_count(0)

    page.get_by_test_id("new-task").click()
    page.get_by_test_id("task-title").fill("Keep SEPA at checkout")
    goal = " ".join(f"word{i}" for i in range(25))
    page.get_by_test_id("task-goal").fill(goal)
    page.keyboard.press("Escape")
    confirm = page.get_by_role("dialog", name="Discard this task?")
    expect(confirm).to_be_visible()
    expect(confirm).to_contain_text("You have written 29 words that haven't been saved.")
    expect(page.get_by_test_id("discard-keep")).to_be_focused()
    page.get_by_test_id("discard-keep").click()
    expect(confirm).to_have_count(0)
    expect(page.get_by_test_id("task-goal")).to_have_value(goal)

    # Escape inside the confirmation closes only it; the writing stays, and closing asks again.
    page.keyboard.press("Escape")
    expect(confirm).to_be_visible()
    page.keyboard.press("Escape")
    expect(confirm).to_have_count(0)
    expect(page.get_by_role("dialog", name="New task")).to_be_visible()
    expect(page.get_by_test_id("task-goal")).to_have_value(goal)

    # The Read shortcut inside the confirmation is taken from the browser (no hard reload), and does nothing else.
    page.keyboard.press("Escape")
    expect(confirm).to_be_visible()
    page.evaluate("""() => { window.keys = [];
      document.addEventListener('keydown', e => window.keys.push([e.key.toLowerCase(), e.defaultPrevented])); }""")
    for keys in ("Control+Shift+r", "Meta+Shift+r"):
        page.keyboard.press(keys)
    assert [k for k in page.evaluate("window.keys") if k[0] == "r"] == [["r", True], ["r", True]]
    expect(confirm).to_be_visible()
    expect(page.get_by_test_id("discard-keep")).to_be_focused()
    expect(page.get_by_test_id("task-reading")).to_have_count(0)
    page.get_by_test_id("discard-keep").click()
    expect(confirm).to_have_count(0)
    expect(page.get_by_test_id("task-reading")).to_have_count(0)
    expect(page.get_by_test_id("task-goal")).to_have_value(goal)
    expect(page.get_by_test_id("task-title")).to_have_value("Keep SEPA at checkout")

    # Cancel asks too, and Discard closes without saving.
    page.get_by_role("button", name="Cancel").click()
    page.get_by_test_id("discard-confirm").click()
    expect(page.get_by_test_id("task-title")).to_have_count(0)
    assert console_errors == []


def test_closing_an_edited_task_says_its_changes_are_unsaved_without_a_count(
    page: Page, web_url: str, client: ApiClient, org: dict, forge_project: dict, console_errors: list
):
    # Most of the goal was there already: a count of every word in it would be false.
    long_goal = " ".join(f"word{i}" for i in range(60))
    client.create_task(forge_project["id"], "Edited goal", goal=long_goal)
    sign_in(page, web_url, org["api_key"])
    page.get_by_text("Edited goal").first.click()
    page.get_by_test_id("edit-task").click()
    page.get_by_test_id("task-goal").fill(long_goal + " more")
    page.keyboard.press("Escape")
    confirm = page.get_by_role("dialog", name="Discard your changes to this task?")
    expect(confirm).to_be_visible()
    expect(confirm.get_by_text("Your changes to this task haven't been saved.", exact=True)).to_be_visible()
    expect(confirm).not_to_contain_text("words")
    assert console_errors == []


def test_a_task_names_the_repositories_it_changes_and_reads(
    page: Page, web_url: str, client: ApiClient, org: dict, forge_project: dict, console_errors: list
):
    target = forge_project["repositories"][0]
    docs = client.post(f"/v1/projects/{forge_project['id']}/repositories",
                       {"name": "docs", "url": "https://github.com/acme/docs.git"}).json()
    sign_in(page, web_url, org["api_key"])
    page.get_by_test_id("new-task").click()
    page.get_by_test_id("task-title").fill("Document the greeting")
    page.get_by_test_id("task-goal").fill("Say in the docs how the greeting picks a name.")
    chooser = page.get_by_test_id("task-repositories")
    # Nothing chosen says what that means.
    expect(chooser).to_contain_text("changes no code")
    chooser.get_by_role("checkbox", name=target["name"]).click()
    chooser.get_by_role("checkbox", name="docs").click()
    chooser.get_by_role("combobox", name="What the work does in docs").click()
    page.get_by_role("listbox").get_by_text("Reads it").click()
    expect(chooser).to_contain_text("own pull request")
    page.get_by_test_id("task-save").click()
    expect(page.get_by_test_id("task-screen")).to_be_visible()

    items = client.get("/v1/tasks", params={"projectId": forge_project["id"]}).json()["tasks"]
    saved = next(i for i in items if i["title"] == "Document the greeting")
    assert sorted((r["id"], r["access"]) for r in saved["repositories"]) == sorted(
        [(target["id"], "write"), (docs["id"], "read")])
    assert console_errors == []


def test_project_settings_manage_repositories_and_delivery(
    page: Page, web_url: str, client: ApiClient, org: dict, forge_project: dict, console_errors: list
):
    sign_in(page, web_url, org["api_key"])
    page.get_by_test_id("project-settings-button").click()
    expect(page.get_by_test_id("project-settings")).to_be_visible()

    # A second repository, from the Repositories tab.
    page.get_by_test_id("add-repository").click()
    page.get_by_test_id("repository-name").fill("docs")
    page.get_by_test_id("repository-url").fill("https://github.com/acme/docs.git")
    page.get_by_test_id("repository-save").click()
    expect(page.get_by_role("cell", name="docs", exact=True)).to_be_visible()
    # A URL git could be tricked by is refused, and the dialog says why.
    page.get_by_test_id("add-repository").click()
    page.get_by_test_id("repository-name").fill("bad")
    page.get_by_test_id("repository-url").fill("ext::sh -c id")
    page.get_by_test_id("repository-save").click()
    expect(page.get_by_role("alert")).to_contain_text("url must be an https, ssh or git:// URL")
    page.keyboard.press("Escape")
    # That refusal is the only error the page saw.
    assert console_errors == ["Failed to load resource: the server responded with a status of 400 (Bad Request)"]
    console_errors.clear()

    # Security joins correctness on every delivery.
    page.locator("[data-settings-nav='delivery']").click()
    page.get_by_role("checkbox", name="security").click()
    page.get_by_test_id("delivery-save").click()
    expect(toast(page, "Delivery saved")).to_be_visible()

    project = client.get(f"/v1/projects/{forge_project['id']}").json()
    assert "docs" in [r["name"] for r in project["repositories"]]
    assert "bad" not in [r["name"] for r in project["repositories"]]
    # Only what differs from the factory's defaults is stored: the rest keeps
    # following them.
    assert project["deliveryPolicy"] == {"requiredReviewers": ["correctness", "security"]}
    assert console_errors == []

    # Its face: an image picked in General, resized in the browser, stored,
    # and shown wherever the project is, the sidebar included.
    page.locator("[data-settings-nav='general']").click()
    png = bytes.fromhex("89504e470d0a1a0a0000000d49484452000000010000000108060000001f15c489"
                        "0000000d49444154789c63f8cfc0f01f00050001ff89993d1d0000000049454e44ae426082")
    page.get_by_test_id("project-image-file").set_input_files({"name": "logo.png", "mimeType": "image/png", "buffer": png})
    expect(toast(page, "Image updated")).to_be_visible()
    expect(page.get_by_test_id("project-face").locator("img")).to_have_count(1)
    expect(page.locator(f"[data-nav-key='project:{forge_project['id']}'] img")).to_have_count(1)
    assert client.get(f"/v1/projects/{forge_project['id']}").json()["imageUrl"].startswith("/v1/projects/")
    assert console_errors == []


def test_a_new_project_starts_from_the_empty_screen(page: Page, web_url: str, client: ApiClient, org: dict, console_errors: list):
    """An organization with nothing yet is offered a project, not told to use the API."""
    sign_in(page, web_url, org["api_key"])
    page.get_by_test_id("new-project-empty").click()
    page.get_by_test_id("project-name").fill("Payments API")
    # Prefilled as dude would derive it from the slug.
    expect(page.get_by_test_id("project-key")).to_have_value("PAYM")
    page.get_by_test_id("project-repository").fill("https://github.com/acme/payments-api.git")
    page.get_by_test_id("project-create").click()

    # It lands on the new project's settings, with the repository in place.
    expect(page.get_by_test_id("project-settings")).to_be_visible()
    expect(page.get_by_role("cell", name="payments-api", exact=True)).to_be_visible()
    project = client.get("/v1/projects").json()["projects"][0]
    assert (project["name"], project["slug"], project["key"]) == ("Payments API", "payments-api", "PAYM")
    assert console_errors == []


def test_the_github_connection_is_checked_and_replaced_in_settings(
    page: Page, web_url: str, client: ApiClient, org: dict, forge_project: dict, console_errors: list
):
    sign_in(page, web_url, org["api_key"])
    page.get_by_test_id("org-settings-button").click()
    # Settings open on Members; GitHub is the next page.
    page.locator('[data-settings-nav="github"]').click()
    expect(page.get_by_test_id("org-settings")).to_contain_text("Connected")
    expect(page.get_by_test_id("org-settings")).to_contain_text("…oken")

    page.get_by_test_id("forge-verify").click()
    expect(page.get_by_test_id("forge-verdict")).to_have_text("Connected as dude-bot")
    # Under it, each repository's permissions that are not plainly granted, with why.
    repo = page.get_by_test_id("forge-permission-repo")
    expect(repo).to_have_count(1)
    expect(repo).to_contain_text("Greeter / greeter")
    expect(repo.locator('[data-outcome="untested"]', has_text="Checks: Read")).to_contain_text(
        "Could not test: no recent commit has check runs")
    expect(repo.locator('[data-outcome="missing"]')).to_have_count(0)

    # A wrong token is caught here, not by an agent failing to open a PR.
    api_base = client.get("/v1/forge/credential").json()["apiBaseUrl"]
    page.get_by_test_id("forge-connect").click()
    page.get_by_test_id("forge-token").fill("wrong-token")
    page.get_by_label("API base URL").fill(api_base)
    page.get_by_test_id("forge-save").click()
    expect(page.get_by_test_id("org-settings")).to_contain_text("…oken")
    page.get_by_test_id("forge-verify").click()
    expect(page.get_by_test_id("forge-verdict")).to_have_text("GitHub rejected the token")
    assert console_errors == []


def test_epics_are_made_ordered_and_removed_from_the_sidebar_and_board(
    page: Page, web_url: str, client: ApiClient, org: dict, forge_project: dict, console_errors: list
):
    item = client.create_task(forge_project["id"], "Loose work")
    sign_in(page, web_url, org["api_key"])

    # Two epics, from the board. A new one opens, which reveals it in the tree.
    for title in ("Onboarding", "Billing"):
        page.get_by_role("treeitem", name="Greeter").click()
        page.get_by_test_id("new-epic").click()
        page.get_by_test_id("epic-title").fill(title)
        page.get_by_test_id("epic-save").click()
        expect(page.get_by_role("treeitem", name=title)).to_be_visible()

    # Move the loose task into Billing from its row's menu.
    row = page.get_by_role("treeitem", name="Loose work", exact=False)
    row.focus()
    page.keyboard.press("Shift+F10")
    page.get_by_role("menuitem", name="Move to epic").focus()
    page.keyboard.press("ArrowRight")
    page.get_by_role("menuitem", name="Billing").click()
    page.wait_for_timeout(500)
    epics = client.get(f"/v1/projects/{forge_project['id']}/epics").json()["epics"]
    billing = next(e for e in epics if e["title"] == "Billing")
    assert client.get(f"/v1/tasks/{item['id']}").json()["epicId"] == billing["id"]

    # Billing moves above Onboarding.
    billing_row = page.get_by_role("treeitem", name="Billing")
    billing_row.hover()
    page.get_by_role("button", name="Actions for Billing").click()
    page.get_by_role("menuitem", name="Move up").click()
    page.wait_for_timeout(500)
    assert [e["title"] for e in client.get(f"/v1/projects/{forge_project['id']}/epics").json()["epics"]] == ["Billing", "Onboarding"]

    # The project's board, grouped by epic, shows them in that order.
    page.get_by_role("treeitem", name="Greeter").click()
    page.get_by_test_id("group-by-epic").click()
    expect(page.locator("[data-lane]").first).to_contain_text("Billing")

    # Editing the title keeps the description the tree never showed; the
    # dialog hands focus back to the row it was opened from.
    client.patch(f"/v1/epics/{billing['id']}", {"description": "Invoices and refunds"})
    billing_row.focus()
    page.keyboard.press("Shift+F10")
    page.get_by_role("menuitem", name="Edit epic").click()
    expect(page.get_by_label("Description")).to_have_value("Invoices and refunds")
    page.get_by_test_id("epic-title").fill("Billing and refunds")
    page.get_by_test_id("epic-save").click()
    expect(page.get_by_role("dialog")).to_have_count(0)
    billing_row = page.get_by_role("treeitem", name="Billing and refunds")
    expect(billing_row).to_be_focused()
    saved = next(e for e in client.get(f"/v1/projects/{forge_project['id']}/epics").json()["epics"] if e["id"] == billing["id"])
    assert saved["description"] == "Invoices and refunds"

    # Deleting Billing keeps its work.
    billing_row.focus()
    page.keyboard.press("Shift+F10")
    page.get_by_role("menuitem", name="Delete epic").click()
    expect(page.get_by_role("dialog")).to_contain_text("1 task will stay in the project")
    page.get_by_test_id("epic-delete").click()
    expect(page.get_by_role("dialog")).to_have_count(0)
    expect(toast(page, "Billing and refunds deleted")).to_be_visible()
    expect(page.get_by_role("treeitem", name="Billing and refunds")).to_have_count(0)
    assert [e["title"] for e in client.get(f"/v1/projects/{forge_project['id']}/epics").json()["epics"]] == ["Onboarding"]
    assert client.get(f"/v1/tasks/{item['id']}").json()["epicId"] is None
    assert console_errors == []


def test_back_and_forward_move_between_places(
    page: Page, web_url: str, client: ApiClient, forge_project: dict, org: dict, console_errors: list
):
    client.create_task(forge_project["id"], "Somewhere to go")
    sign_in(page, web_url, org["api_key"])
    page.get_by_text("Somewhere to go").last.click()
    expect(page.get_by_test_id("task-screen")).to_be_visible()
    page.get_by_test_id("org-settings-button").click()
    expect(page.get_by_test_id("org-settings")).to_be_visible()

    page.go_back()
    expect(page.get_by_test_id("task-screen")).to_contain_text("Somewhere to go")
    page.go_back()
    expect(page.get_by_text("Somewhere to go").last).to_be_visible()
    expect(page.get_by_test_id("task-screen")).to_have_count(0)
    page.go_forward()
    expect(page.get_by_test_id("task-screen")).to_be_visible()

    # A pasted link lands where it points.
    page.goto(f"{web_url}#/org/settings")
    expect(page.get_by_test_id("org-settings")).to_be_visible()
    assert console_errors == []


def test_an_agents_progress_shows_in_its_chat(
    page: Page, web_url: str, client: ApiClient, org: dict, forge_project: dict, console_errors: list
):
    """The implementer reports progress with `dude event progress`; its
    chat shows one progress row that moved, not a line per update. What it
    thought before acting is in its events and in its chat, as a thought."""
    client.patch(f"/v1/projects/{forge_project['id']}", {"agentModels": client.on_models({
        "implementer": {"model": "fake/tools"}, "reviewer": "fake/scripted", "simplifier": "fake/scripted"})})
    item = client.create_task(forge_project["id"], "Report progress")
    assert client.post(f"/v1/tasks/{item['id']}/deliver").status_code == 201
    implement = wait_until(lambda: next((r for r in client.task_runs(item["id"]) if r["phase"] == "implement"), None),
                           timeout=30, message="no implementer")
    wait_until(lambda: [e for e in client.events(runId=implement["id"]) if e["eventType"] == "agent.custom.progress"][1:],
               timeout=30, message="the progress never reached the ledger")
    thought = "Progress first, then the commit: the person watching should see each step land."
    thoughts = wait_until(lambda: [e for e in client.events(runId=implement["id"]) if e["eventType"] == "agent.thought"],
                          timeout=30, message="the thought never reached the ledger")
    assert [e["payload"]["text"] for e in thoughts] == [thought]

    sign_in(page, web_url, org["api_key"])
    page.goto(f"{web_url}#/session/{implement['id']}")
    progress = page.get_by_test_id("chat-progress")
    expect(progress).to_have_count(1)
    expect(progress).to_contain_text("2 of 2")
    expect(progress).to_contain_text("committing")
    shown = page.get_by_role("button").filter(has_text=re.compile(r"^Thought")).filter(has_text="Progress first, then the commit")
    expect(shown).to_have_count(1)
    shown.click()
    expect(page.get_by_text(thought, exact=True)).to_be_visible()
    assert console_errors == []


def test_a_person_approves_a_repository_an_agent_asked_for(
    page: Page, web_url: str, client: ApiClient, org: dict, forge_project: dict, fake_github, console_errors: list
):
    """The implementer asks for another repository; its chat shows the
    request, and approving it brings the repository into the running agent."""
    web = fake_github.add_repository("web")
    client.post(f"/v1/projects/{forge_project['id']}/repositories", {"name": "web", "url": web.clone_url})
    target = next(r for r in client.get(f"/v1/projects/{forge_project['id']}").json()["repositories"] if r["name"] != "web")
    client.patch(f"/v1/projects/{forge_project['id']}", {"agentModels": client.on_models({
        "implementer": {"model": "fake/request"}, "reviewer": "fake/scripted", "simplifier": "fake/scripted"})})
    item = client.create_task(forge_project["id"], "Needs the client", repositories=[{"id": target["id"]}])
    assert client.post(f"/v1/tasks/{item['id']}/deliver").status_code == 201
    implement = wait_until(lambda: next((r for r in client.task_runs(item["id"]) if r["phase"] == "implement"), None),
                           timeout=30, message="no implementer")
    wait_until(lambda: client.get("/v1/repository-requests", params={"runId": implement["id"]}).json()["repositoryRequests"],
               timeout=30, message="the agent never asked")

    sign_in(page, web_url, org["api_key"])
    page.goto(f"{web_url}#/session/{implement['id']}")
    card = page.get_by_test_id("repository-request")
    expect(card).to_contain_text("Read web")
    expect(card).to_contain_text("the client calls this API")
    card.get_by_role("button", name="Approve").click()

    wait_until(lambda: client.get("/v1/repository-requests", params={"runId": implement["id"]}).json()
               ["repositoryRequests"][0]["status"] == "cloned", timeout=30, message="the repository never reached the run")
    names = sorted(r["id"] for r in client.get(f"/v1/tasks/{item['id']}").json()["repositories"])
    assert len(names) == 2, names
    assert console_errors == []


def test_a_parked_agent_is_answered_from_its_chat(
    page: Page, web_url: str, client: ApiClient, org: dict, forge_project: dict, console_errors: list
):
    """An agent that asked and was not answered in time is parked: its chat
    says so, and answering there resumes it."""
    client.patch(f"/v1/projects/{forge_project['id']}", {"agentModels": client.on_models({
        "implementer": {"model": "fake/ask"}, "reviewer": "fake/scripted", "simplifier": "fake/scripted"})})
    item = client.create_task(forge_project["id"], "Ask, then wait")
    assert client.post(f"/v1/tasks/{item['id']}/deliver").status_code == 201
    implement = wait_until(
        lambda: next((r for r in client.task_runs(item["id"]) if r["phase"] == "implement" and r["status"] == "paused"), None),
        timeout=30, message="the waiting agent was never parked")

    sign_in(page, web_url, org["api_key"])
    page.goto(f"{web_url}#/session/{implement['id']}")
    expect(page.get_by_test_id("chat-notice")).to_contain_text("Parked while it waits for you")
    # Paused, yet the composer takes the answer: that is what resumes it.
    page.get_by_role("group", name="Answer with one of").get_by_role("button", name="yes").click()
    wait_until(lambda: client.get_run(implement["id"])["status"] == "completed",
               timeout=30, message="the answer did not resume the parked agent")
    # Once timed, its return says how long it took, its phases on hover:
    # exactly the numbers of its run.resume.timed.
    _expect_resume_notice(page, client, implement["id"], "Taken back up", "Taken back up")
    assert console_errors == []


# A resume's phases, in order, as the notice's hover names them.
_RESUME_LABELS = (("react", "dude asked lux"), ("schedule", "lux placed it"), ("image", "image ready"),
                  ("restore", "restored"), ("start", "started"), ("reload", "agent reloaded"),
                  ("take", "took its input"), ("firstOutput", "first words"))


def _duration(ms: float) -> str:
    """The design system's formatDuration, short style, as far as a resume
    reaches (under a day): JavaScript's rounding, half away from zero."""
    from decimal import ROUND_HALF_UP, Decimal

    if ms < 0:
        return "—"
    if ms == 0:
        return "0s"
    if ms < 0.5:
        return "<1ms"
    if ms < 999.5:
        return f"{int(Decimal(ms).quantize(Decimal(1), ROUND_HALF_UP))}ms"
    s = ms / 1000
    if s < 60:
        return f"{Decimal(s).quantize(Decimal('0.1'), ROUND_HALF_UP)}s" if s < 10 else \
            f"{int(Decimal(s).quantize(Decimal(1), ROUND_HALF_UP))}s"
    m = int(s // 60)
    if m < 60:
        return f"{m}m {int(s % 60):02d}s"
    return f"{m // 60}h {m % 60:02d}m"


def _resume_notice(payload: dict, lead: str) -> tuple[str, str]:
    """The notice a run.resume.timed gives: its sentence and its hover."""
    where = ", on another host" if payload["moved"] is True else ""
    text = f"{lead} in {_duration(payload['totalMs'])}{where}."
    title = "\n".join(f"{label} {_duration(payload['phases'][key])}"
                      for key, label in _RESUME_LABELS if key in payload["phases"])
    return text, title


def _expect_resume_notice(page: Page, client: ApiClient, run_id: str, lead: str, has_text: str,
                          count_timeout: float | None = None) -> None:
    """The Run's one run.resume.timed is said by exactly one chat notice
    with has_text: its sentence, led by lead, and its hover."""
    timed = wait_until(lambda: [e for e in client.events(runId=run_id) if e["eventType"] == "run.resume.timed"],
                       timeout=30, message="the resume was never timed")
    assert len(timed) == 1, timed
    text, title = _resume_notice(timed[0]["payload"], lead)
    notice = page.get_by_test_id("chat-notice").filter(has_text=has_text)
    expect(notice).to_have_count(1, timeout=count_timeout)
    expect(notice).to_contain_text(text)
    expect(notice).to_have_attribute("title", title)


def test_a_persons_resume_says_how_long_it_took(
    page: Page, web_url: str, client: ApiClient, org: dict, forge_project: dict, console_errors: list
):
    """A person pauses a working agent and resumes it: its chat says how long
    the resume took once the agent speaks, with the phases on hover."""
    client.patch(f"/v1/projects/{forge_project['id']}", {"agentModels": {
        "implementer": {"model": "fake/live"}, "reviewer": {"model": "fake/scripted"}, "simplifier": {"model": "fake/scripted"}}})
    item = client.create_task(forge_project["id"], "Pause, then carry on")
    assert client.post(f"/v1/tasks/{item['id']}/deliver").status_code == 201
    run = wait_until(lambda: next((r for r in client.task_runs(item["id"]) if r["status"] == "running"), None),
                     timeout=30, message="the implementer never started")
    sign_in(page, web_url, org["api_key"])
    page.goto(f"{web_url}#/session/{run['id']}")
    wait_until(lambda: any(e["eventType"] == "agent.tool.called" for e in client.events(runId=run["id"])),
               timeout=30, message="the agent never started working")
    assert client.post(f"/v1/runs/{run['id']}/pause", {}).status_code == 200
    wait_until(lambda: any(e["eventType"] == "run.paused" and e["payload"].get("confirmed")
                           for e in client.events(runId=run["id"])), timeout=30, message="the run never paused")
    assert client.post(f"/v1/runs/{run['id']}/resume", {}).status_code == 200
    _expect_resume_notice(page, client, run["id"], "Resumed", "Resumed in", count_timeout=30_000)
    assert console_errors == []


def test_an_agent_parked_on_a_repository_request_says_what_resumes_it(
    page: Page, web_url: str, client: ApiClient, org: dict, forge_project: dict, fake_github, console_errors: list
):
    """An agent that cannot go on without a repository ends its turn on the
    request and is parked; its chat says so, and approving resumes it."""
    web = fake_github.add_repository("web")
    client.post(f"/v1/projects/{forge_project['id']}/repositories", {"name": "web", "url": web.clone_url})
    target = next(r for r in client.get(f"/v1/projects/{forge_project['id']}").json()["repositories"] if r["name"] != "web")
    client.patch(f"/v1/projects/{forge_project['id']}", {"agentModels": client.on_models({
        "implementer": {"model": "fake/wait"}, "reviewer": "fake/scripted", "simplifier": "fake/scripted"})})
    item = client.create_task(forge_project["id"], "Needs the client first", repositories=[{"id": target["id"]}])
    assert client.post(f"/v1/tasks/{item['id']}/deliver").status_code == 201
    implement = wait_until(
        lambda: next((r for r in client.task_runs(item["id"]) if r["phase"] == "implement" and r.get("dudePause") == "person"), None),
        timeout=30, message="the waiting agent was never parked")

    sign_in(page, web_url, org["api_key"])
    page.goto(f"{web_url}#/session/{implement['id']}")
    expect(page.get_by_test_id("chat-notice")).to_contain_text("Parked while it waits for you")
    # No question to answer: the composer says what does resume it.
    expect(page.get_by_placeholder("decide its request above")).to_be_visible()
    page.get_by_test_id("repository-request").get_by_role("button", name="Approve").click()
    wait_until(lambda: client.get_run(implement["id"])["status"] == "completed",
               timeout=30, message="the approval did not resume the parked agent")
    assert console_errors == []


def test_theme_and_density_are_this_browsers_and_remembered(page: Page, web_url: str, org: dict, console_errors: list):
    """You choose how dude looks; it stays so after a reload."""
    sign_in(page, web_url, org["api_key"])
    page.get_by_test_id("my-settings-button").click()
    expect(page.get_by_test_id("my-settings")).to_be_visible()
    html = page.locator("html")
    expect(html).to_have_attribute("data-density", "comfortable")

    page.get_by_role("combobox", name="Density").click()
    page.get_by_role("option", name="Compact — more on screen").click()
    expect(html).to_have_attribute("data-density", "compact")
    page.get_by_role("combobox", name="Theme").click()
    page.get_by_role("option", name="Light").click()
    expect(html).to_have_attribute("data-theme", "light")

    page.reload()
    expect(page.get_by_test_id("my-settings")).to_be_visible()
    expect(html).to_have_attribute("data-density", "compact")
    expect(html).to_have_attribute("data-theme", "light")
    assert console_errors == []


def test_a_browser_turns_notifications_on_and_off(
    page: Page, web_url: str, client: ApiClient, org: dict, owner_dsn: str, console_errors: list
):
    """Notify me: the browser is asked, subscribes with dude's key, and dude
    keeps the subscription; turning off forgets it. (Headless Chromium has no
    push service to subscribe with, so that part is stood in for; sending to
    a subscription is tested in the orchestrator.)"""
    page.context.grant_permissions(["notifications"], origin=web_url.rstrip("/"))
    # The real service worker registers and runs; only the push service
    # headless Chromium lacks is stood in for.
    page.add_init_script("""
      const endpoint = "https://push.example/" + Math.random().toString(36).slice(2);
      let sub = null;
      const fake = () => ({ endpoint, toJSON: () => ({ endpoint, keys: { p256dh: "BPk", auth: "au" } }),
                            unsubscribe: async () => { sub = null; return true; } });
      PushManager.prototype.getSubscription = async function () { return sub; };
      PushManager.prototype.subscribe = async function (o) { window.__pushKey = o.applicationServerKey; sub = fake(); return sub; };
    """)
    sign_in(page, web_url, org["api_key"])
    page.get_by_test_id("my-settings-button").click()
    state = page.get_by_test_id("push-state")
    expect(state).to_have_attribute("data-state", "off")

    page.get_by_test_id("push-on").click()
    expect(state).to_have_attribute("data-state", "on")
    # Subscribed with dude's public key (an uncompressed P-256 point), and
    # kept for the organization, where the notifier sends.
    assert page.evaluate("() => window.__pushKey.length") == 65
    import psycopg

    def subscriptions() -> int:
        with psycopg.connect(owner_dsn) as conn:
            return conn.execute("SELECT count(*) FROM push_subscriptions WHERE organization_id = %s",
                                (org["id"],)).fetchone()[0]
    assert subscriptions() == 1

    page.get_by_test_id("push-off").click()
    expect(state).to_have_attribute("data-state", "off")
    assert subscriptions() == 0
    # The real service worker is what the page registered.
    assert page.evaluate("async () => (await navigator.serviceWorker.getRegistration('/'))?.active?.scriptURL").endswith("/sw.js")
    assert console_errors == []


def test_everything_waiting_on_you_is_in_one_place(
    page: Page, web_url: str, client: ApiClient, org: dict, forge_project: dict, fake_github, console_errors: list
):
    """A question and a repository request, from two agents: both in "Waiting
    on you", oldest first, each opening the agent that asked."""
    web = fake_github.add_repository("web")
    client.post(f"/v1/projects/{forge_project['id']}/repositories", {"name": "web", "url": web.clone_url})
    target = next(r for r in client.get(f"/v1/projects/{forge_project['id']}").json()["repositories"] if r["name"] != "web")
    client.patch(f"/v1/projects/{forge_project['id']}", {"agentModels": client.on_models({
        "implementer": {"model": "fake/ask"}, "reviewer": "fake/scripted", "simplifier": "fake/scripted"})})
    asking = client.create_task(forge_project["id"], "Ask first", repositories=[{"id": target["id"]}])
    client.post(f"/v1/tasks/{asking['id']}/deliver")
    wait_until(lambda: client.get("/v1/questions").json()["questions"], timeout=30, message="no question")
    client.patch(f"/v1/projects/{forge_project['id']}", {"agentModels": client.on_models({"implementer": "fake/wait"})})
    requesting = client.create_task(forge_project["id"], "Needs the client", repositories=[{"id": target["id"]}])
    client.post(f"/v1/tasks/{requesting['id']}/deliver")
    wait_until(lambda: client.get("/v1/repository-requests").json()["repositoryRequests"], timeout=30, message="no request")

    sign_in(page, web_url, org["api_key"])
    page.goto(f"{web_url}#/waiting")
    inbox = page.get_by_test_id("inbox")
    rows = inbox.get_by_role("listitem")
    expect(rows).to_have_count(2)
    expect(rows.nth(0)).to_contain_text("Ask first")
    expect(rows.nth(0)).to_contain_text("Should FACTORY.md be in English?")
    expect(rows.nth(1)).to_contain_text("Needs the client")
    expect(rows.nth(1)).to_contain_text("Read web?")
    # Both are yours (you own them): highlighted, each with its one action.
    expect(inbox.locator("[data-mine]")).to_have_count(2)
    rows.nth(1).get_by_role("button", name="Answer").click()
    expect(page.get_by_test_id("repository-request")).to_contain_text("Read web")
    assert console_errors == []


def test_only_a_tasks_owner_answers_and_anyone_can_take_it_over(
    page: Page, web_url: str, env, client: ApiClient, org: dict, forge_project: dict, console_errors: list
):
    """Whoever creates a task drives it: a colleague sees its agent's
    question, but not the choices — they wait on the owner. Taking the task
    over from its page makes the question theirs to answer."""
    client.patch(f"/v1/projects/{forge_project['id']}", {"agentModels": client.on_models({
        "implementer": {"model": "fake/ask"}, "reviewer": "fake/scripted", "simplifier": "fake/scripted"})})
    item = client.create_task(forge_project["id"], "Ask the owner")
    assert item["owner"]["name"] == "e2e user"
    assert client.post(f"/v1/tasks/{item['id']}/deliver").status_code == 201
    question = wait_until(lambda: next(iter(client.get("/v1/questions", params={"taskId": item["id"]}).json()["questions"]), None),
                          timeout=30, message="the agent never asked")
    bo_key = create_api_key(env.owner_dsn, org["id"], name="Bo")
    bo = ApiClient(env.control_plane_url, bo_key)
    people = bo.get("/v1/people").json()["people"]
    assert sorted(p["name"] for p in people) == ["Bo", "e2e user"]

    # Not Bo's to answer, and the refusal says whose it is.
    refused = bo.post(f"/v1/questions/{question['id']}/answer", {"text": "yes"})
    assert refused.status_code == 403, refused.text
    assert refused.json()["error"]["code"] == "not_owner"
    assert "only e2e user can answer this" in refused.json()["error"]["message"]

    sign_in(page, web_url, bo_key)
    page.goto(f"{web_url}#/session/{question['runId']}")
    expect(page.get_by_test_id("waiting-on")).to_contain_text("Waiting for e2e user to answer")
    expect(page.get_by_test_id("take-over")).to_have_text("· Take over this task to answer")
    expect(page.get_by_role("group", name="Answer with one of")).to_have_count(0)
    expect(page.get_by_placeholder("Waiting for e2e user to answer.")).to_be_disabled()
    # The choices shown are not buttons for Bo; hovering says how to make
    # them his, and clicking one leaves that said rather than closing it.
    choices = page.get_by_test_id("choices-someone-else")
    expect(choices.get_by_role("button")).to_have_count(0)
    choices.get_by_text("yes").hover()
    expect(page.get_by_role("tooltip")).to_have_text("Take over this task to answer")
    # Watch the trigger across the press: it must never close, not merely be open again after.
    choices.evaluate("""el => { window.__closed = false;
        new MutationObserver(() => { if (el.dataset.state === 'closed') window.__closed = true; })
          .observe(el, { attributes: true, attributeFilter: ['data-state'] }); }""")
    choices.get_by_text("yes").click()
    expect(page.get_by_role("tooltip")).to_have_text("Take over this task to answer")
    assert page.evaluate("window.__closed") is False

    # Bo takes it over from the task's page.
    page.goto(f"{web_url}#/task/{item['id']}")
    owner = page.get_by_test_id("task-owner")
    expect(owner).to_have_attribute("data-owner", "e2e user")
    owner.get_by_role("combobox", name="Owner").click()
    page.get_by_role("listbox").get_by_text("Bo").click()
    expect(owner).to_have_attribute("data-owner", "Bo")
    assert client.get(f"/v1/tasks/{item['id']}").json()["owner"]["name"] == "Bo"
    changed = [e for e in client.events(taskId=item["id"]) if e["eventType"] == "task.owner_changed"]
    assert len(changed) == 1 and changed[0]["payload"]["to"] != changed[0]["payload"]["from"]
    # The board and sidebar name its owner too.
    tasks = [t for p in client.get("/v1/navigation").json()["projects"] for t in p["tasks"]]
    assert next(t for t in tasks if t["id"] == item["id"])["people"][0]["name"] == "Bo"

    # Now it is Bo's to answer, from the chat.
    page.goto(f"{web_url}#/session/{question['runId']}")
    expect(page.get_by_test_id("waiting-on")).to_have_count(0)
    page.get_by_role("group", name="Answer with one of").get_by_role("button", name="yes").click()
    wait_until(lambda: client.get("/v1/questions", params={"taskId": item["id"]}).json()["questions"][0]["status"] == "answered",
               timeout=30, message="Bo's answer was not taken")
    assert console_errors == []


def test_a_tasks_time_and_cost_show_on_its_page_and_its_epics(
    page: Page, web_url: str, client: ApiClient, org: dict, forge_project: dict, console_errors: list
):
    """A delivered task's page says how long its agents worked and waited
    and what it cost, run by run; its epic's board totals them."""
    epic = client.post(f"/v1/projects/{forge_project['id']}/epics", {"title": "Metrics"}).json()
    task = client.create_task(forge_project["id"], "Measure me", epicId=epic["id"])
    client.post(f"/v1/tasks/{task['id']}/deliver")
    wait_until(lambda: any(r["phase"] == "review" and r["status"] == "completed" for r in client.task_runs(task["id"])),
               timeout=60, message="no finished review")

    sign_in(page, web_url, org["api_key"])
    page.goto(f"{web_url}#/task/{task['id']}")
    metrics = page.get_by_test_id("task-metrics")
    expect(metrics).to_contain_text("Agents working")
    expect(metrics).to_contain_text("Cost")
    expect(page.get_by_test_id("run-metrics").get_by_role("row").filter(has_text="Implement")).to_have_count(1)

    page.goto(f"{web_url}#/epic/{epic['id']}")
    expect(page.get_by_test_id("epic-metrics")).to_contain_text("of 1 task")
    assert console_errors == []


def test_a_project_can_have_its_changes_tested_in_a_browser(
    page: Page, web_url: str, client: ApiClient, org: dict, forge_project: dict, console_errors: list
):
    """The tester phase is a project's choice, off by default."""
    sign_in(page, web_url, org["api_key"])
    page.goto(f"{web_url}#/project/{forge_project['id']}/settings")
    page.locator("[data-settings-nav='delivery']").click()
    box = page.get_by_test_id("delivery-test").get_by_role("switch")
    expect(box).to_have_attribute("aria-checked", "false")
    box.click()
    page.get_by_test_id("delivery-save").click()
    expect(toast(page, "Delivery saved")).to_be_visible()
    assert client.get(f"/v1/projects/{forge_project['id']}").json()["deliveryPolicy"]["test"] is True
    assert console_errors == []


def test_a_failed_implementer_says_why_on_its_task_and_its_chat(
    page: Page, web_url: str, client: ApiClient, org: dict, forge_project: dict, console_errors: list
):
    """With no model for the implementer its Run fails, and delivery stops for
    a person. The task says why at the top and on the failed step, "Waiting on
    you" gives the reason, and the Run's chat ends on the error rather than a
    composer nobody would hear."""
    client.patch(f"/v1/projects/{forge_project['id']}", {"agentModels": client.on_models({"reviewer": "fake/scripted"})})
    item = client.create_task(forge_project["id"], "Nobody to implement it")
    assert client.post(f"/v1/tasks/{item['id']}/deliver").status_code == 201
    wait_until(lambda: client.get(f"/v1/tasks/{item['id']}").json()["status"] == "awaiting_input",
               timeout=60, message="delivery never stopped for a person")
    task = client.get(f"/v1/tasks/{item['id']}").json()
    assert task["escalation"]["reason"] == "implement_failed", task["escalation"]
    implement = next(r for r in task["runs"] if r["phase"] == "implement")

    sign_in(page, web_url, org["api_key"])
    page.goto(f"{web_url}#/task/{item['id']}")
    escalation = page.get_by_test_id("escalation")
    expect(escalation).to_contain_text("Implementer failed")
    expect(escalation).to_contain_text("The Implementer runs on Coder, which names no model yet. An admin sets it in Models.")
    expect(page.get_by_test_id("phase").first).to_contain_text("which names no model yet")
    expect(page).to_have_title("GREE-1 · Nobody to implement it — dude")

    page.goto(f"{web_url}#/waiting")
    expect(page.get_by_test_id("inbox").get_by_role("listitem").first).to_contain_text("Implementer failed")

    page.goto(f"{web_url}#/task/{item['id']}")
    escalation.get_by_test_id("escalation-run").click()
    ended = page.get_by_test_id("run-ended")
    expect(ended).to_have_attribute("data-outcome", "failed")
    # Why is the transcript's last line, just above; the strip says only how it ended.
    expect(page.get_by_test_id("chat-ended")).to_contain_text("which names no model yet")
    # No composer: a finished run hears nothing. It is on its task's page,
    # so there is no way back to offer: the task is right there.
    expect(page.get_by_test_id("run-screen").locator("textarea")).to_have_count(0)
    expect(ended.get_by_test_id("run-ended-task")).to_have_count(0)
    expect(page.get_by_test_id("task-screen")).to_be_visible()
    assert implement["status"] == "failed"
    assert console_errors == []


def test_a_person_decides_how_a_stopped_delivery_goes_on(
    page: Page, web_url: str, client: ApiClient, org: dict, forge_project: dict, console_errors: list
):
    """Delivery stopped for a person offers what fits: here, a failed
    implementer — try again, or stop. A note goes with the decision; trying
    again runs the step afresh, and with its model back it goes on."""
    client.patch(f"/v1/projects/{forge_project['id']}", {"agentModels": client.on_models({"reviewer": "fake/scripted"})})
    item = client.create_task(forge_project["id"], "Implement it once it can")
    assert client.post(f"/v1/tasks/{item['id']}/deliver").status_code == 201
    wait_until(lambda: client.get(f"/v1/tasks/{item['id']}").json()["status"] == "awaiting_input",
               timeout=60, message="delivery never stopped for a person")
    assert client.get(f"/v1/tasks/{item['id']}").json()["escalation"]["actions"] == ["retry", "stop"]

    sign_in(page, web_url, org["api_key"])
    page.goto(f"{web_url}#/task/{item['id']}")
    escalation = page.get_by_test_id("escalation")
    expect(escalation.get_by_test_id("escalation-retry")).to_have_text("Try again")
    expect(escalation.get_by_test_id("escalation-stop")).to_be_visible()
    expect(escalation.get_by_test_id("escalation-accept")).to_have_count(0)

    # Its model back, and a word for the agent.
    client.patch(f"/v1/projects/{forge_project['id']}",
                 {"agentModels": client.on_models({"implementer": "fake/scripted", "reviewer": "fake/scripted"})})
    escalation.get_by_test_id("escalation-note").fill("The model is configured now.")
    escalation.get_by_test_id("escalation-retry").click()
    expect(page.get_by_test_id("escalation")).to_have_count(0)
    wait_until(lambda: len([r for r in client.get(f"/v1/tasks/{item['id']}").json()["runs"] if r["phase"] == "implement"]) == 2,
               timeout=60, message="trying again made no second implementer")
    decided = [e for e in client.events(taskId=item["id"]) if e["eventType"] == "task.decided"]
    assert decided and decided[0]["payload"] == {"reason": "implement_failed", "action": "retry",
                                                 "note": "The model is configured now."}, decided
    assert console_errors == []


def test_a_task_started_over_shows_one_attempt_at_a_time(
    page: Page, web_url: str, client: ApiClient, org: dict, forge_project: dict, fake_github: FakeGitHub, console_errors: list
):
    """Attempt 1 reaches a pull request with a finding, its pull request is
    closed, and the task is started over; attempt 2 reaches its own. The page
    opens on attempt 2 with a picker in its header and attempt 2's findings
    and Merge; picking attempt 1 shows its findings, branch and closed pull
    request and says it was set aside, read-only; Activity shows both
    attempts either way."""
    item = client.create_task(forge_project["id"], "Start me over")
    assert client.post(f"/v1/tasks/{item['id']}/deliver").status_code == 201
    pr = wait_until(lambda: client.get("/v1/pull-requests", params={"taskId": item["id"]}).json()["pullRequests"],
                    timeout=180, message="attempt 1 opened no pull request")[0]
    fake_github.close(pr["number"])
    wait_until(lambda: client.get(f"/v1/tasks/{item['id']}").json()["status"] == "aborted",
               timeout=60, message="closing the pull request did not stop the task")
    resp = client.post(f"/v1/tasks/{item['id']}/recover", {"action": "restart", "note": "Smaller this time."})
    assert resp.status_code == 200, resp.text
    wait_until(lambda: any(r["attempt"] == 2 for r in client.task_runs(item["id"])),
               timeout=60, message="starting over made no attempt 2")
    # Attempt 2 runs its own review and opens its own pull request: once it
    # is open, every finding of either attempt is in.
    second = wait_until(lambda: [p for p in client.get("/v1/pull-requests", params={"taskId": item["id"]}).json()["pullRequests"]
                                 if p["headBranch"].endswith("/attempt-2") and p["state"] == "open"],
                        timeout=180, message="attempt 2 opened no pull request")[0]
    attempt_of = {r["id"]: r["attempt"] for r in client.task_runs(item["id"])}
    findings = client.get("/v1/findings", params={"taskId": item["id"]}).json()["findings"]
    first_findings = [f for f in findings if attempt_of.get(f["runId"]) == 1]
    second_findings = [f for f in findings if attempt_of.get(f["runId"]) == 2]
    assert first_findings, "attempt 1's reviewer raised nothing"
    assert second_findings, "attempt 2's reviewer raised nothing"
    assert len(first_findings) + len(second_findings) == len(findings), findings

    sign_in(page, web_url, org["api_key"])
    page.goto(f"{web_url}#/task/{item['id']}")
    picker = page.get_by_test_id("attempt-picker")
    expect(picker).to_contain_text("Attempt 2")
    expect(picker).to_contain_text("current")
    expect(page.get_by_test_id("earlier-bar")).to_have_count(0)
    expect(page.get_by_test_id("branch")).to_contain_text("attempt-2", timeout=30_000)
    # Attempt 2's own pull request is open, and can be merged from here.
    expect(page.locator(f"[data-testid=pr-panel][data-pr='{second['id']}']").get_by_test_id("pr-merge")).to_have_count(1)

    picker.click()
    page.get_by_role("option").filter(has_text="Attempt 1").click()
    expect(page).to_have_url(re.compile(r"\?attempt=1$"))
    bar = page.get_by_test_id("earlier-bar")
    expect(bar).to_contain_text("Attempt 1 was set aside")
    expect(bar).to_contain_text("started over: “Smaller this time.”")
    expect(page.get_by_test_id("branch")).to_contain_text("attempt-1")
    # Its pull request, closed on GitHub before the start over: no Merge, and not said to be dude's close.
    first_panel = page.locator(f"[data-testid=pr-panel][data-pr='{pr['id']}']")
    expect(first_panel).to_contain_text("Closed. Attempt 1 was set aside")
    expect(first_panel.get_by_test_id("pr-merge")).to_have_count(0)
    expect(page.get_by_test_id("pr-panel")).to_have_count(1)
    tabs = page.get_by_role("tablist", name="Task")
    tabs.get_by_role("tab", name="Findings").click()
    expect(page).to_have_url(re.compile(r"/findings\?attempt=1$"))
    expect(page.get_by_test_id("finding")).to_have_count(len(first_findings))
    for f in first_findings:
        expect(page.get_by_test_id("finding").filter(has_text=f["title"])).to_have_count(1)

    # The way back to the current attempt, and Back to attempt 1 again.
    bar.get_by_test_id("go-current").click()
    expect(page.get_by_test_id("earlier-bar")).to_have_count(0)
    expect(page).to_have_url(re.compile(rf"#/task/{item['id']}/findings$"))
    expect(page.get_by_test_id("finding")).to_have_count(len(second_findings))
    page.go_back()
    expect(page.get_by_test_id("earlier-bar")).to_be_visible()
    expect(page.get_by_test_id("finding")).to_have_count(len(first_findings))

    tabs.get_by_role("tab", name="Activity").click()
    expect(page).to_have_url(re.compile(rf"#/task/{item['id']}/activity$"))
    lines = page.get_by_test_id("activity-item")
    expect(page.locator("[data-testid=activity-item][data-attempt='1']").first).to_be_visible()
    expect(page.locator("[data-testid=activity-item][data-attempt='2']").first).to_be_visible()
    expect(lines.filter(has_text="started over as attempt 2")).to_contain_text("· attempt 2")
    assert console_errors == []


def test_a_refused_key_asks_for_another(page: Page, web_url: str, org: dict, console_errors: list):
    """A key the server does not take (mistyped, revoked) goes back to the key
    prompt, saying so — not an error above a spinner that never ends."""
    page.goto(web_url)
    page.evaluate("localStorage.clear()")
    page.goto(web_url)
    page.evaluate("window.loginDocumentMarker = 'original'")
    documents = []
    page.on("request", lambda request: documents.append(request.url) if request.is_navigation_request() else None)
    page.fill('input[type="password"]', "dude_sk_not-a-key")
    page.click('button[type="submit"]')
    expect(page.get_by_text("That key was not accepted")).to_be_visible()
    assert page.evaluate("localStorage.getItem('dude.apiKey')") is None
    assert page.evaluate("window.loginDocumentMarker") == "original"
    assert documents == []

    # The right one creates one new document, without a credential in its URL.
    page.fill('input[type="password"]', org["api_key"])
    page.click('button[type="submit"]')
    expect(page.get_by_test_id("shell")).to_be_visible()
    assert page.evaluate("window.loginDocumentMarker") is None
    assert page.evaluate("localStorage.getItem('dude.apiKey')") == org["api_key"]
    assert documents == [web_url + "/"]
    assert all("401" in e for e in console_errors), console_errors


@pytest.mark.parametrize("failure", ["html_401", "html_503", "network"])
def test_manual_login_check_failure_can_retry(page: Page, web_url: str, org: dict, failure: str):
    page.goto(web_url)
    page.evaluate("localStorage.clear()")
    page.goto(web_url)
    page.evaluate("window.loginDocumentMarker = 'original'")
    if failure == "network":
        page.route("**/v1/me", lambda route: route.abort())
    else:
        page.route("**/v1/me", lambda route: route.fulfill(
            status=401 if failure == "html_401" else 503,
            content_type="text/html", body="<h1>Unavailable</h1>"))
    page.fill('input[type="password"]', org["api_key"])
    page.click('button[type="submit"]')
    message = "That key was not accepted" if failure == "html_401" else "Could not check that key. Please try again."
    expect(page.get_by_text(message)).to_be_visible()
    assert page.evaluate("localStorage.getItem('dude.apiKey')") is None
    assert page.evaluate("window.loginDocumentMarker") == "original"
    expect(page.get_by_role("button", name="Continue")).to_be_enabled()
    page.unroute("**/v1/me")
    page.click('button[type="submit"]')
    expect(page.get_by_test_id("shell")).to_be_visible()


def test_a_link_to_something_gone_says_so(
    page: Page, web_url: str, client: ApiClient, org: dict, forge_project: dict, console_errors: list
):
    """A task, run or epic that does not exist is said to, with a way back."""
    sign_in(page, web_url, org["api_key"])
    for kind in ("task", "run", "epic"):
        page.goto(f"{web_url}#/{kind}/{kind}_doesnotexist")
        expect(page.get_by_test_id("not-found")).to_contain_text(f"This {kind} doesn't exist")
    page.get_by_test_id("not-found-back").click()
    expect(page.get_by_test_id("new-task")).to_be_visible()
    # The server's 404s for the task and the run are the only errors.
    assert all("404" in e for e in console_errors), console_errors


def _hanging_run(client: ApiClient, forge_project: dict, title: str) -> dict:
    """A task whose implementer is working and never finishes its turn."""
    client.patch(f"/v1/projects/{forge_project['id']}", {"agentModels": client.on_models({
        "implementer": {"model": "fake/hang"}, "reviewer": "fake/scripted", "simplifier": "fake/scripted"})})
    item = client.create_task(forge_project["id"], title)
    assert client.post(f"/v1/tasks/{item['id']}/deliver").status_code == 201
    return wait_until(lambda: next((r for r in client.task_runs(item["id"]) if r["phase"] == "implement" and r["status"] == "running"), None),
                      timeout=60, message="the implementer never started")


def test_a_steer_is_sent_with_enter_and_signed_with_a_name(
    page: Page, web_url: str, env, client: ApiClient, org: dict, forge_project: dict, console_errors: list
):
    """Enter sends a steer (Shift+Enter is a new line); the chat and the
    event log say who sent it, by name, not "Human"."""
    run = _hanging_run(client, forge_project, "Steer me")
    sign_in(page, web_url, org["api_key"])
    page.goto(f"{web_url}#/session/{run['id']}")
    field = page.get_by_placeholder("Steer the agent…")
    field.fill("first line")
    field.press("Shift+Enter")
    field.type("second line")
    expect(field).to_have_value("first line\nsecond line")
    expect(page.get_by_text("Sent as e2e")).to_be_visible()
    field.press("Enter")
    turn = page.get_by_test_id("human-turn").last
    expect(turn).to_contain_text("e2e user")
    expect(turn).to_contain_text("second line")
    expect(page.get_by_test_id("human-turn")).not_to_contain_text("Human")
    steered = wait_until(lambda: [e for e in client.events(runId=run["id"]) if e["eventType"] == "run.steered"],
                         timeout=15, message="no steer in the ledger")
    assert steered[0]["payload"]["text"] == "first line\nsecond line" and steered[0]["payload"]["interrupt"] is False

    # Someone else's steer is signed with their name.
    bo = ApiClient(env.control_plane_url, create_api_key(env.owner_dsn, org["id"], name="Bo"))
    assert bo.post(f"/v1/runs/{run['id']}/steer", {"text": "from Bo"}).status_code == 201
    page.reload()
    expect(page.get_by_test_id("human-turn").filter(has_text="from Bo")).to_contain_text("Bo")
    assert console_errors == []


def test_abort_asks_first_and_offers_to_pause_instead(
    page: Page, web_url: str, client: ApiClient, org: dict, forge_project: dict, console_errors: list
):
    """Abort is a request, confirmed in a dialog whose one solid button
    aborts; "Pause instead" pauses. The chat then says who stopped it."""
    run = _hanging_run(client, forge_project, "Stop me")
    sign_in(page, web_url, org["api_key"])
    page.goto(f"{web_url}#/session/{run['id']}")

    page.get_by_test_id("abort").click()
    dialog = page.get_by_role("dialog")
    expect(dialog).to_contain_text("cannot be resumed")
    dialog.get_by_test_id("abort-pause-instead").click()
    expect(dialog).to_have_count(0)
    wait_until(lambda: client.get_run(run["id"])["status"] == "paused", timeout=30, message="Pause instead did not pause")
    assert client.get_run(run["id"])["status"] != "aborted"

    page.get_by_test_id("abort").click()
    page.get_by_role("dialog").get_by_label("Why (optional)").fill("wrong task")
    page.get_by_role("dialog").get_by_test_id("abort-confirm").click()
    wait_until(lambda: client.get_run(run["id"])["status"] == "aborted", timeout=30, message="the confirmed abort did not abort")
    expect(page.get_by_test_id("chat-ended")).to_contain_text("Aborted by e2e user: wrong task")
    assert console_errors == []


def test_acting_second_is_a_calm_notice_naming_who_acted_first(
    page: Page, web_url: str, env, client: ApiClient, org: dict, forge_project: dict, console_errors: list
):
    """Two people on one agent: Bo pauses it while you look; your Pause is
    refused (409), and the page says Bo did it — not an error — until the
    page catches up."""
    run = _hanging_run(client, forge_project, "Two hands")
    sign_in(page, web_url, org["api_key"])
    page.goto(f"{web_url}#/session/{run['id']}")
    expect(page.get_by_role("button", name="Pause")).to_be_visible()
    # Bo acts first; the page is told nothing yet: its live stream is cut,
    # while its own requests still go through.
    page.route("**/v1/events/stream**", lambda route: route.abort())
    page.evaluate("window.dispatchEvent(new Event('offline'))")
    bo = ApiClient(env.control_plane_url, create_api_key(env.owner_dsn, org["id"], name="Bo"))
    assert bo.post(f"/v1/runs/{run['id']}/pause").status_code in (200, 201, 202)
    wait_until(lambda: client.get_run(run["id"])["status"] == "paused", timeout=30, message="Bo's pause did not land")
    page.get_by_role("button", name="Pause").click()
    notice = page.get_by_test_id("conflict-notice")
    expect(notice).to_contain_text("Bo paused it first")
    expect(page.get_by_role("alert")).to_have_count(0)
    # The refusal is the only error the page saw.
    assert all("409" in e or "ERR_FAILED" in e for e in console_errors), console_errors


def test_a_dropped_stream_says_so_and_catches_up(
    page: Page, web_url: str, client: ApiClient, org: dict, forge_project: dict, console_errors: list
):
    """The live stream drops: the page says it is reconnecting, and when it
    is back it re-reads what it missed."""
    client.create_task(forge_project["id"], "Before")
    sign_in(page, web_url, org["api_key"])
    expect(page.get_by_text("Before").first).to_be_visible()
    page.context.set_offline(True)
    expect(page.get_by_test_id("reconnecting")).to_be_visible(timeout=20_000)
    # One banner, from the shell, with a way out if it never comes back.
    expect(page.get_by_test_id("reconnecting")).to_have_count(1)
    expect(page.get_by_test_id("reconnecting-reload")).to_be_visible()
    client.create_task(forge_project["id"], "While away")
    page.context.set_offline(False)
    expect(page.get_by_test_id("reconnecting")).to_have_count(0, timeout=30_000)
    expect(page.get_by_text("While away").first).to_be_visible(timeout=15_000)
    assert all("ERR_INTERNET_DISCONNECTED" in e for e in console_errors), console_errors


def test_a_delivered_tasks_chat_asks_its_conductor_and_shows_the_answer(
    page: Page, web_url: str, client: ApiClient, org: dict, forge_project: dict, console_errors: list
):
    """Chat is the task's first tab: before anyone wrote, the task's history
    in a line over an empty composer; a message starts the conductor, whose
    briefing El Duderino signs and whose answer carries its own face."""
    client.patch(f"/v1/projects/{forge_project['id']}", {"agentModels": {
        **forge_project["agentModels"], **client.on_models({"conductor": "fake/scripted"})}})
    task = client.create_task(forge_project["id"], "Chat about me")
    assert client.post(f"/v1/tasks/{task['id']}/deliver").status_code == 201
    wait_until(lambda: client.get("/v1/pull-requests", params={"taskId": task["id"]}).json()["pullRequests"],
               timeout=90, message="the delivery never opened a pull request")

    sign_in(page, web_url, org["api_key"])
    page.goto(f"{web_url}#/task/{task['id']}")
    tabs = page.get_by_role("tablist", name="Task").get_by_role("tab")
    expect(tabs.first).to_have_text("Chat")
    tabs.first.click()
    history = page.get_by_test_id("chat-history")
    expect(history).to_contain_text("Delivered automatically")
    expect(history).to_contain_text("implementer → reviewer → fixer → reviewer → simplifier → PR #")
    composer = page.get_by_test_id("task-chat").get_by_placeholder("Ask about this task…")
    expect(composer).to_be_empty()

    composer.fill("why does it greet like that?")
    composer.press("Enter")

    briefing = page.get_by_test_id("chat-briefing")
    expect(briefing).to_contain_text("Briefing", timeout=30_000)
    dude = briefing.locator("header").inner_text().split("\n")[0]
    assert dude in ("The Dude", "El Duderino", "His Dudeness", "Duder"), dude
    answer = page.get_by_test_id("conductor-turn").first
    expect(answer).to_contain_text("why does it greet like that?", timeout=30_000)
    expect(answer).to_contain_text(task["id"])
    expect(answer).to_have_attribute("data-role", "conductor")
    expect(answer.get_by_role("img", name="Conductor")).to_be_visible()
    # It lives on: the page opens on Chat from now on.
    page.goto(f"{web_url}#/task/{task['id']}")
    expect(page.get_by_role("tablist", name="Task").get_by_role("tab", selected=True)).to_have_text("Chat")
    assert console_errors == []
