"""Independent brainstorm children publish into their session's existing Files."""

import pytest
from playwright.sync_api import expect

from helpers import query, sign_in, wait_until
from suites.test_sessions import _person, _said, _tool


@pytest.mark.ui
def test_brainstorm_child_file_and_result_are_member_only(client, owner_dsn, page, web_url, org, env):
    tier = client.tier_for("fake/scripted")
    response = client.patch("/v1/settings/organization", {
        "roles": {"brainstorm": {"tier": tier}, "investigator": {"tier": tier}},
    })
    assert response.status_code == 200, response.text
    outsider, outsider_client = _person(client, env, "Outside Research")
    session = client.post("/v1/brainstorms", {"title": "Independent research"}).json()["id"]
    response = client.post(f"/v1/brainstorms/{session}/chat", {
        "text": "Investigate independently.\n" + _tool("start_agent", {"profile": "investigator", "key": "report"}),
    })
    assert response.status_code in (200, 201), response.text
    parent = response.json()["runId"]
    child = wait_until(lambda: query(owner_dsn, """SELECT id FROM runs
        WHERE session_id=%s AND source_kind='session' AND parent_run_id=%s""", (session, parent)),
        timeout=60, message="the brainstorm never started an investigator")[0]["id"]
    artifact = wait_until(lambda: next((a for a in client.get("/v1/artifacts", params={"sessionId": session}).json()["artifacts"]
        if a["runId"] == child), None), timeout=90, message="the child's file never reached Files")
    assert artifact["name"] == "REPORT.md"
    assert artifact["sessionId"] == session and artifact["taskId"] is None
    assert f"Investigation findings for {child}" in client.get(f"/v1/artifacts/{artifact['id']}/content").text
    wait_until(lambda: query(owner_dsn, """SELECT d.id FROM child_result_outbox o
        JOIN directives d ON d.id=o.directive_id
        WHERE o.child_run_id=%s AND d.run_id=%s AND d.delivered_at IS NOT NULL""", (child, parent)),
        timeout=90, message="the brainstorm never received the child result")
    wait_until(lambda: any("child" in text.lower() for text in _said(client, session)),
        timeout=60, message="the brainstorm never answered its result")

    sign_in(page, web_url, org["api_key"], at=f"#/sessions/{session}")
    files = page.get_by_test_id("session-files")
    expect(files.get_by_test_id("session-files-count")).to_have_text("1", timeout=15_000)
    files.locator('[data-name="REPORT.md"]').click()
    expect(page.get_by_test_id("file-viewer").get_by_test_id("file-content")).to_contain_text(child)

    assert outsider_client.get(f"/v1/runs/{child}").status_code == 404
    assert outsider_client.get("/v1/events", params={"sessionId": session}).status_code == 404
    assert outsider_client.get("/v1/artifacts", params={"sessionId": session}).status_code == 404
    assert outsider_client.get(f"/v1/artifacts/{artifact['id']}/content").status_code == 404
    assert outsider_client.get(f"/v1/brainstorms/{session}/artifacts.zip").status_code == 404
