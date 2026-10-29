"""Real cohort settlement and public-feed queued/delivered transcript, with a scripted model."""

import os
import re
from pathlib import Path

from playwright.sync_api import expect

from helpers import execute, query, sign_in, wait_until
from suites.test_conductor_agents import _world, _children
from suites.test_conductor_edits import _say, _tool


def test_cohort_result_waits_for_answer_then_updates_one_row(
    page, web_url, client, org, second_org, forge_project, owner_dsn
):
    task, parent = _world(client, forge_project)
    models = {**forge_project["agentModels"], **client.on_models({
        "conductor": "fake/scripted", "investigator": "fake/hang"})}
    assert client.patch(f"/v1/projects/{forge_project['id']}", {"agentModels": models}).status_code == 200
    _say(client, task["id"], "Investigate three areas together.",
         _tool("start_agents", {"key": "visible-cohort", "runs": [
             {"profile": "investigator", "prompt": f"Investigate area {i}."} for i in range(3)]}))
    members = wait_until(lambda: rows if len(rows := _children(owner_dsn, task)) == 3 else None, timeout=90)
    wait_until(lambda: all(client.get_run(m["id"])["status"] == "running" for m in members), timeout=90)
    _say(client, task["id"], "Ask the owner before continuing.",
         _tool("ask_person", {"question": "May I continue after reviewing these reports?", "choices": ["Continue", "Wait"]}))
    question = wait_until(lambda: client.get(f"/v1/questions?runId={parent}").json()["questions"], timeout=60)[0]
    assert client.patch(f"/v1/projects/{forge_project['id']}/settings", {"delivery": {"parkAfterMinutes": 1}}).status_code == 200
    # Deterministic clock fixture only: the real syncer performs the park.
    wait_until(lambda: query(owner_dsn, "SELECT 1 FROM runs WHERE id=%s AND waiting_since IS NOT NULL", (parent,)), timeout=60)
    execute(owner_dsn, "UPDATE runs SET waiting_since=now()-interval '2 minutes' WHERE id=%s", (parent,))
    wait_until(lambda: query(owner_dsn, "SELECT 1 FROM runs WHERE id=%s AND status='paused' AND dude_pause='person'", (parent,)), timeout=60)
    before_runs = query(owner_dsn, "SELECT id FROM runs WHERE task_id=%s", (task["id"],))
    for i, member in enumerate(members):
        assert client.post(f"/v1/runs/{member['id']}/abort", {}).status_code == 200
        wait_until(lambda: query(owner_dsn, "SELECT 1 FROM child_result_outbox WHERE child_run_id=%s AND consumed_at IS NOT NULL", (member["id"],)), timeout=90)
        queued = [e for e in client.events(runId=parent, limit=1000) if e["eventType"] == "run.cohort.queued"]
        assert len(queued) == (1 if i == 2 else 0)
    payload = queued[0]["payload"]
    assert set(payload) == {"cohortId", "parentRunId", "directiveId", "completed", "total", "members"}
    assert payload["completed"] == 0 and payload["total"] == 3
    assert {m["runId"] for m in payload["members"]} == {m["id"] for m in members}
    assert all(m["status"] == "aborted" and set(m) == {"runId", "status"} for m in payload["members"])
    assert client.get(f"/v1/questions?runId={parent}").json()["questions"][0]["status"] == "open"
    assert query(owner_dsn, "SELECT 1 FROM runs WHERE id=%s AND status='paused' AND dude_pause='person'", (parent,))
    assert len(query(owner_dsn, "SELECT id FROM runs WHERE task_id=%s", (task["id"],))) == len(before_runs)
    assert second_org["client"].events(runId=parent, limit=1000) == []
    other = client.create_task(forge_project["id"], "Unrelated task")
    assert not [e for e in client.events(taskId=other["id"], limit=1000) if e["eventType"] == "run.cohort.queued"]
    page.set_viewport_size({"width": 1920, "height": 1080})
    sign_in(page, web_url, org["api_key"], at=f"#/task/{task['id']}")
    row = page.get_by_test_id("cohort-result")
    expect(row).to_have_count(1)
    expect(row).to_contain_text("Completed 0/3 · Queued", timeout=30_000)
    expect(row).not_to_contain_text(re.compile(r"\b(?:read|verified|passed|pass)\b", re.IGNORECASE))
    expect(row).to_contain_text("Member reports are unverified claims.")
    expect(row.get_by_role("link")).to_have_count(3)
    for member in members:
        expect(row.get_by_role("link", name=f"Run {member['id']}")).to_have_attribute("href", f"#/session/{member['id']}")

    def screenshot(name):
        if path := os.environ.get("DUDE_TEST_SHOTS"):
            Path(path).mkdir(parents=True, exist_ok=True)
            row.scroll_into_view_if_needed()
            row.hover()
            page.evaluate("""label => {
                const caption = document.createElement('div');
                caption.id = 'cohort-evidence-caption';
                caption.textContent = label;
                caption.style.cssText = 'position:fixed;bottom:16px;left:16px;padding:12px 18px;background:#17202a;color:white;font:18px sans-serif;z-index:99999';
                document.body.append(caption);
            }""", f"Cohort lifecycle: {name}. Real settlement and receipts; scripted model; aged wait clock fixture.")
            page.screenshot(path=str(Path(path) / f"cohort-{name}.png"))
            page.locator("#cohort-evidence-caption").evaluate("el => el.remove()")

    screenshot("queued")
    page.reload()
    expect(row).to_have_count(1)
    expect(row).to_contain_text("Queued", timeout=30_000)
    # An actual owner answer, not a seeded receipt, lets the existing directive delivery proceed.
    response = client.post(f"/v1/questions/{question['id']}/answer", {"text": "Continue"})
    assert response.status_code == 200, response.text
    wait_until(lambda: [e for e in client.events(runId=parent, limit=1000)
                        if e["eventType"] == "run.directive.delivered" and e["payload"].get("directiveId") == payload["directiveId"]], timeout=90)
    expect(row).to_have_count(1)
    expect(row).to_contain_text("Completed 0/3 · Delivered", timeout=30_000)
    expect(row).not_to_contain_text(re.compile(r"\b(?:read|verified|passed|pass)\b", re.IGNORECASE))
    expect(row).to_contain_text("Member reports are unverified claims.")
    screenshot("delivered")
    page.set_viewport_size({"width": 390, "height": 844})
    expect(row).to_be_visible()
    page.reload()
    expect(row).to_have_count(1)
    expect(row).to_contain_text("Delivered", timeout=30_000)
