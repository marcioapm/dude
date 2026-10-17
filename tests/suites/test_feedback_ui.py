"""People's feedback in the web app, against the real API and the fake lux.

A fake/live implementer is at work with uncommitted edits, so its Run has a
live diff to comment on: a person comments on a range of it, which is a
private draft in the drafts tray; Submit sends the batch to the
implementer at work; the item then shows submitted on its lines and on
the Feedback tab, holds the merge while it is open, and is replied to and
resolved from its card. General feedback is written on the Feedback tab.

Screenshots of each surface are written when DUDE_FEEDBACK_SHOTS names a
directory (dark and light at 1440x900, and 390x844).
"""

from __future__ import annotations

import json
import os
import re
from pathlib import Path

import pytest
from playwright.sync_api import Page, expect

from helpers import ApiClient, execute, sign_in, wait_until

pytestmark = pytest.mark.ui

LIVE_MODELS = {"implementer": "fake/live", "reviewer": "fake/scripted", "simplifier": "fake/scripted"}
SHOTS = os.environ.get("DUDE_FEEDBACK_SHOTS")


def _feedback(client: ApiClient, task_id: str) -> dict:
    return client.get(f"/v1/tasks/{task_id}/feedback").json()


def _shoot(page: Page, name: str, subject=None) -> None:
    """Dark and light at 1440x900, then dark at 390x844, `subject` (a locator) scrolled into view at each size."""
    if not SHOTS:
        return
    out = Path(SHOTS)
    out.mkdir(parents=True, exist_ok=True)
    size = page.viewport_size or {"width": 1440, "height": 900}

    def take(path: str) -> None:
        if subject is not None:
            subject.scroll_into_view_if_needed()
        page.wait_for_timeout(200)
        page.screenshot(path=str(out / path))

    for theme in ("dark", "light"):
        page.evaluate("t => document.documentElement.setAttribute('data-theme', t)", theme)
        take(f"{name}-1440-{theme}.png")
    page.evaluate("() => document.documentElement.setAttribute('data-theme', 'dark')")
    page.set_viewport_size({"width": 390, "height": 844})
    take(f"{name}-390-dark.png")
    page.set_viewport_size(size)
    page.evaluate("() => document.documentElement.removeAttribute('data-theme')")
    page.wait_for_timeout(150)


def test_a_comment_on_a_runs_diff_is_a_draft_until_submitted_then_handled_from_its_card(
    page: Page, web_url: str, client: ApiClient, org: dict, forge_project: dict, console_errors: list
):
    client.patch(f"/v1/projects/{forge_project['id']}", {"agentModels": client.on_models(LIVE_MODELS)})
    task = client.create_task(forge_project["id"], "Pause graceful vs hard")
    assert client.post(f"/v1/tasks/{task['id']}/deliver").status_code == 201
    run = wait_until(lambda: next((r for r in client.task_runs(task["id"]) if r["phase"] == "implement" and r["status"] == "running"), None),
                     timeout=30, message="no implementer at work")
    wait_until(lambda: any(f["path"] == "LIVE.md" for f in client.get(f"/v1/runs/{run['id']}/diff").json()["files"]),
               timeout=30, message="no live diff")

    page.set_viewport_size({"width": 1440, "height": 900})
    sign_in(page, web_url, org["api_key"])
    page.goto(f"{web_url}#/session/{run['id']}")
    page.get_by_test_id("session-view").get_by_role("button", name="Changes").click()
    changes = page.get_by_test_id("changes")
    section = changes.get_by_test_id("diff-section").filter(has_text="LIVE.md")
    expect(section).to_be_visible()

    # Hovering a line shows its "+"; pressing it and dragging over the next
    # lines picks them, on one side.
    first = section.locator('[data-cside="new"][data-cline="1"]')
    first.hover()
    plus = first.get_by_role("button", name="Comment on line 1")
    expect(plus).to_be_visible()
    box = plus.bounding_box()
    assert box
    page.mouse.move(box["x"] + box["width"] / 2, box["y"] + box["height"] / 2)
    page.mouse.down()
    third = section.locator('[data-cside="new"][data-cline="3"]').bounding_box()
    assert third
    page.mouse.move(third["x"] + 200, third["y"] + third["height"] / 2, steps=6)
    expect(changes.get_by_test_id("drag-hint")).to_contain_text("3 lines · let go to comment")
    page.mouse.up()
    editor = changes.get_by_test_id("fb-editor")
    expect(editor).to_contain_text("Comment on lines 1–3")
    expect(editor).to_contain_text("Private draft until you submit")
    editor.get_by_role("textbox", name="Comment").fill("Say which pause it was: graceful or hard.")
    _shoot(page, "diff-editor", editor)
    # On a phone the file list stacks above the diff, so the editor has the width to write in.
    page.set_viewport_size({"width": 390, "height": 844})
    field = editor.get_by_role("textbox", name="Comment")
    field.scroll_into_view_if_needed()
    width = (field.bounding_box() or {"width": 0})["width"]
    assert width >= 250, f"the comment field is {width}px wide at 390"
    page.set_viewport_size({"width": 1440, "height": 900})
    editor.get_by_role("textbox", name="Comment").press("Control+Enter")

    # A private draft: on its lines, and in the tray.
    draft = changes.get_by_test_id("fb-draft")
    expect(draft).to_contain_text("Only you see this until you submit.")
    tray = page.get_by_test_id("fb-tray")
    expect(tray).to_have_attribute("data-count", "1")
    expect(tray).to_contain_text("only you see them until you submit")
    expect(tray.get_by_test_id("fb-tray-route")).to_contain_text("the implementer at work")
    listed = _feedback(client, task["id"])["feedback"]
    assert [(i["status"], i["anchor"]["path"], i["anchor"]["startLine"], i["anchor"]["endLine"]) for i in listed] == [("draft", "LIVE.md", 1, 3)]
    tray.get_by_role("button", name="Review").click()
    expect(tray.get_by_test_id("fb-tray-row")).to_have_count(1)
    _shoot(page, "tray-open", tray)

    # Submit asks first, then sends the batch to the implementer at work.
    tray.get_by_test_id("fb-submit").click()
    dialog = page.get_by_role("dialog", name="Submit 1 feedback item")
    expect(dialog.get_by_test_id("fb-confirm-route")).to_contain_text("The implementer at work gets them at its next step.")
    _shoot(page, "submit-dialog", dialog)
    # On a phone the confirmation still fits and its key hint gives the buttons the room.
    page.set_viewport_size({"width": 390, "height": 844})
    expect(dialog.get_by_test_id("fb-confirm")).to_be_in_viewport()
    expect(dialog.get_by_test_id("fb-confirm-keys")).not_to_be_visible()
    page.set_viewport_size({"width": 1440, "height": 900})
    expect(dialog.get_by_test_id("fb-confirm-keys")).to_be_visible()
    dialog.get_by_test_id("fb-confirm").click()
    expect(page.get_by_test_id("fb-tray")).to_have_count(0)
    item = wait_until(lambda: next((i for i in _feedback(client, task["id"])["feedback"] if i["status"] != "draft"), None),
                      timeout=10, message="nothing submitted")
    assert item["routedTo"] == "steer"

    # Submitted: on its lines with its status, and holding the merge.
    submitted = changes.get_by_test_id("fb-item").filter(has_text="Say which pause it was")
    expect(submitted.get_by_test_id("fb-status")).to_have_attribute("data-status", re.compile("open|delivered"))
    expect(page.get_by_test_id("merge-hold")).to_contain_text("1 feedback item holds the merge")

    # Replied to from its card in the diff.
    submitted.get_by_role("button", name="Reply").click()
    submitted.get_by_role("textbox", name="Reply").fill("Graceful by default, please.")
    submitted.get_by_test_id("fb-reply-send").click()
    wait_until(lambda: _feedback(client, task["id"])["feedback"][0]["status"] == "replied", timeout=10, message="the reply never landed")
    expect(page.get_by_test_id("merge-hold")).to_have_count(0)
    expect(submitted.get_by_test_id("fb-entry")).to_contain_text("answered")
    _shoot(page, "diff-thread", submitted)

    # Reopened, it holds the merge again; then resolved from the Feedback tab.
    submitted.get_by_test_id("fb-reopen").click()
    expect(page.get_by_test_id("merge-hold")).to_contain_text("1 feedback item holds the merge")
    page.get_by_test_id("merge-hold").click()
    tab = page.get_by_test_id("feedback-tab")
    card = tab.get_by_test_id("feedback-needs").get_by_test_id("fb-item")
    expect(card).to_contain_text("Say which pause it was")
    expect(card).to_contain_text("LIVE.md")
    # The tab's column fills the pane up to its rail, as every other rail tab's does: no measure of its own.
    edges = page.evaluate("""() => {
      const pane = document.querySelector('[data-testid=feedback-tab]').getBoundingClientRect();
      const rail = document.querySelector('[aria-label=Feedback]').getBoundingClientRect();
      const card = document.querySelector('[data-testid=feedback-needs] [data-testid=fb-item]').getBoundingClientRect();
      return { pane: pane.right, rail: rail.left, card: card.right, pad: parseFloat(getComputedStyle(document.querySelector('[data-testid=feedback-tab]')).paddingRight) };
    }""")
    assert abs(edges["pane"] - edges["rail"]) <= 1, edges
    assert abs(edges["card"] - (edges["rail"] - edges["pad"])) <= 1, edges
    _shoot(page, "feedback-tab", card)
    card.get_by_test_id("fb-resolve").click()
    wait_until(lambda: _feedback(client, task["id"])["blocking"] == 0, timeout=10, message="never resolved")
    expect(page.get_by_test_id("merge-hold")).to_have_count(0)
    tab.get_by_test_id("feedback-show-settled").click()
    expect(tab.get_by_test_id("feedback-settled").get_by_test_id("fb-entry").last).to_contain_text("resolved it")

    # General feedback, from the tab.
    tab.get_by_test_id("general-open").click()
    general = tab.get_by_test_id("fb-editor")
    general.get_by_role("textbox", name="Comment").fill("Add a test where the tool call never ends.")
    general.get_by_test_id("fb-save").click()
    expect(tab.get_by_test_id("feedback-drafts")).to_contain_text("Add a test where the tool call never ends.")
    expect(page.get_by_test_id("fb-tray")).to_have_attribute("data-count", "1")

    # Activity says who did what to how many, never the words.
    page.get_by_role("tab", name="Activity").click()
    activity = page.get_by_test_id("activity")
    expect(activity).to_contain_text("submitted 1 feedback item")
    expect(activity).to_contain_text("resolved 1 feedback item")
    expect(activity).not_to_contain_text("Say which pause it was")
    assert console_errors == [], console_errors


def test_an_item_on_lines_that_moved_follows_them_and_one_on_changed_lines_is_outdated(
    page: Page, web_url: str, client: ApiClient, org: dict, forge_project: dict, console_errors: list, env
):
    """Two comments on the live implementer's LIVE.md diff; its next diff
    shifts one line and rewrites the other. Drawn on that newer diff, the
    shifted one is on its new lines, the rewritten one at the file's head."""
    client.patch(f"/v1/projects/{forge_project['id']}", {"agentModels": client.on_models(LIVE_MODELS)})
    task = client.create_task(forge_project["id"], "Follow the lines")
    assert client.post(f"/v1/tasks/{task['id']}/deliver").status_code == 201
    run = wait_until(lambda: next((r for r in client.task_runs(task["id"]) if r["phase"] == "implement" and r["status"] == "running"), None),
                     timeout=30, message="no implementer at work")

    def live_diff() -> dict | None:
        d = client.get(f"/v1/runs/{run['id']}/diff").json()
        return d if any(f["path"] == "LIVE.md" for f in d["files"]) else None

    diff = wait_until(live_diff, timeout=30, message="no live diff")
    texts = [line["text"] for line in next(f for f in diff["files"] if f["path"] == "LIVE.md")["hunks"][0]["lines"]]
    assert texts[:3] == ["# Live", "", "Written while the agent works."], texts
    kept = client.post(f"/v1/tasks/{task['id']}/feedback/drafts", {"body": "This line moves.", "anchor": {
        "kind": "code", "runId": run["id"], "path": "LIVE.md", "side": "new", "startLine": 3, "endLine": 3, "diffChecksum": diff["checksum"]}})
    changed = client.post(f"/v1/tasks/{task['id']}/feedback/drafts", {"body": "This line is rewritten.", "anchor": {
        "kind": "code", "runId": run["id"], "path": "LIVE.md", "side": "new", "startLine": 1, "endLine": 1, "diffChecksum": diff["checksum"]}})
    assert kept.status_code == 201 and changed.status_code == 201, (kept.text, changed.text)

    # Paused, the agent stops changing its checkout and its final diff is kept;
    # then the Run's diff is made the next one this test needs: two lines
    # above line 3's text, and line 1's text rewritten.
    assert client.post(f"/v1/runs/{run['id']}/pause", {}).status_code in (200, 202)
    wait_until(lambda: client.get(f"/v1/runs/{run['id']}/diff").json()["final"], timeout=30, message="no final diff after the pause")
    new_lines = ["# Live, rewritten", "", "two new lines", "above it", *texts[2:]]
    files = [{"path": "LIVE.md", "status": "A", "additions": len(new_lines), "deletions": 0, "binary": False, "hunks": [{
        "header": f"@@ -0,0 +1,{len(new_lines)} @@",
        "lines": [{"kind": "+", "old": None, "new": n + 1, "text": t} for n, t in enumerate(new_lines)]}]}]
    execute(env.owner_dsn, "UPDATE run_diffs SET files = %s::jsonb, checksum = 'moved' WHERE run_id = %s",
          (json.dumps(files), run["id"]))
    moved_to = new_lines.index("Written while the agent works.") + 1

    page.set_viewport_size({"width": 1440, "height": 900})
    sign_in(page, web_url, org["api_key"])
    page.goto(f"{web_url}#/session/{run['id']}")
    page.get_by_test_id("session-view").get_by_role("button", name="Changes").click()
    section = page.get_by_test_id("changes").get_by_test_id("diff-section").filter(has_text="LIVE.md")
    expect(section).to_be_visible()
    shifted = section.locator(f'[data-testid=after-line][data-side=new][data-line="{moved_to}"]')
    expect(shifted.get_by_test_id("fb-draft")).to_contain_text("This line moves.")
    head = section.get_by_test_id("file-head")
    expect(head.get_by_test_id("fb-draft")).to_contain_text("This line is rewritten.")
    expect(head.get_by_test_id("fb-excerpt")).to_contain_text("The lines it was on have changed")
    expect(head.get_by_test_id("fb-excerpt")).to_contain_text(texts[0])
    _shoot(page, "shifted-item", shifted)
    _shoot(page, "outdated-item", head)
    assert console_errors == [], console_errors


def test_open_feedback_holds_the_pull_requests_merge_in_the_panel_and_the_header(
    page: Page, web_url: str, client: ApiClient, org: dict, forge_project: dict, console_errors: list
):
    task = client.create_task(forge_project["id"], "Hold the merge")
    assert client.post(f"/v1/tasks/{task['id']}/deliver").status_code == 201
    wait_until(lambda: client.get("/v1/pull-requests", params={"taskId": task["id"]}).json()["pullRequests"], timeout=60,
               message="no pull request")
    wait_until(lambda: client.get(f"/v1/tasks/{task['id']}").json()["status"] == "review", timeout=30, message="never in review")
    draft = client.post(f"/v1/tasks/{task['id']}/feedback/drafts", {"body": "Name the flag --dry-run.", "anchor": {"kind": "general"}}).json()
    assert client.post(f"/v1/tasks/{task['id']}/feedback/submit", {"draftIds": [draft["id"]], "submissionKey": "ui-hold"}).status_code == 201

    page.set_viewport_size({"width": 1440, "height": 900})
    sign_in(page, web_url, org["api_key"])
    page.goto(f"{web_url}#/task/{task['id']}")
    expect(page.get_by_test_id("merge-hold")).to_contain_text("1 feedback item holds the merge")
    panel = page.get_by_test_id("pr-panel")
    expect(panel.get_by_test_id("pr-merge")).to_be_disabled()
    expect(panel.get_by_test_id("pr-feedback-held")).to_contain_text("1 feedback item holds the merge.")
    _shoot(page, "merge-hold", panel)
    # Its way to the items is the Feedback tab.
    panel.get_by_test_id("pr-feedback-held").get_by_role("button", name="Show it").click()
    expect(page.get_by_test_id("feedback-needs")).to_contain_text("Name the flag --dry-run.")
    assert console_errors == [], console_errors
