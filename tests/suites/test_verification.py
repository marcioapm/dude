"""The verification gate, end to end: a delivery reaches its pull request
only when the tester has shown every acceptance criterion working, with
evidence a person can open on the task — or a person waived what could not
be checked, with a reason.

The tester is the scripted one (`fakeagent.Tester`): `fake/test-fail-once`
fails the first criterion until a fix after the review loop's own is in
the tree, `fake/test-blocked` cannot start the application.
"""

from __future__ import annotations

import subprocess

from playwright.sync_api import Page, expect

from fake_github import FakeGitHub
from helpers import ApiClient, create_api_key, sign_in, wait_for_escalation, wait_until

CRITERIA = ["The page greets the visitor by name", "The page says goodbye on the way out"]


def _deliver_with_tester(client: ApiClient, project: dict, tester: str, title: str) -> dict:
    # forge_project pins the tester off; these turn it on, as the factory does.
    client.patch(f"/v1/projects/{project['id']}", {
        "deliveryPolicy": {"requiredReviewers": ["correctness"], "test": True},
        "agentModels": client.on_models({"implementer": "fake/scripted", "reviewer": "fake/scripted",
                                         "simplifier": "fake/scripted", "qa_browser": tester}),
    })
    task = client.create_task(project["id"], title, acceptanceCriteria=CRITERIA)
    resp = client.post(f"/v1/tasks/{task['id']}/deliver")
    assert resp.status_code == 201, resp.text
    return task


def _pull_request(client: ApiClient, task_id: str) -> dict:
    return wait_until(lambda: next(iter(client.get("/v1/pull-requests", params={"taskId": task_id}).json()["pullRequests"]), None),
                      timeout=120, message="delivery never opened a pull request")


def test_a_criterion_that_fails_is_fixed_tested_again_and_shown_passing(
    page: Page, web_url: str, client: ApiClient, org: dict, forge_project: dict, fake_github: FakeGitHub, console_errors: list
):
    """The first test fails a criterion: the fixer gets it, the reviewers
    check the fix, and the second test passes it. The task's page shows each
    criterion passing with its video, at the commit the pull request opened
    with; the pull request says the same."""
    task = _deliver_with_tester(client, forge_project, "fake/test-fail-once", "Greet and say goodbye")
    pr = _pull_request(client, task["id"])
    runs = client.task_runs(task["id"])
    assert [r["phase"] for r in runs].count("test") == 2, [r["phase"] for r in runs]
    assert [r["phase"] for r in runs].count("fix") == 2, [r["phase"] for r in runs]

    verification = client.get(f"/v1/tasks/{task['id']}/verification").json()["verifications"]
    assert [[c["status"] for c in v["criteria"]] for v in verification] == [["pass", "pass"], ["fail", "pass"]]
    latest = verification[0]
    assert latest["stale"] is False
    head = latest["heads"]["greeter"]
    # What was tested is what the pull request opened at.
    tip = subprocess.run(["git", "rev-parse", f"refs/heads/{pr['headBranch']}"], cwd=fake_github.bare,
                         capture_output=True, text=True, check=True).stdout.strip()
    assert head == tip, (head, tip)

    body = fake_github.pulls[pr["number"]].body
    assert "## Verification" in body, body
    assert f"Tested at `{head[:7]}`." in body, body
    for c in CRITERIA:
        assert f"- **pass** — {c}" in body, body
    assert "Stale" not in body, body

    sign_in(page, web_url, org["api_key"])
    page.goto(f"{web_url}#/task/{task['id']}")
    page.get_by_role("tab", name="Findings").click()
    section = page.get_by_test_id("verification")
    expect(section.get_by_test_id("criterion")).to_have_count(2)
    for i, c in enumerate(CRITERIA):
        row = section.get_by_test_id("criterion").nth(i)
        expect(row).to_have_attribute("data-state", "pass")
        expect(row).to_contain_text(c)
    expect(section.get_by_test_id("verification-heads")).to_have_text(f"tested at {head[:7]}")
    expect(section.get_by_test_id("verification-stale")).to_have_count(0)
    # The evidence opens the video that test published.
    section.get_by_test_id("evidence").first.click()
    expect(page.get_by_role("dialog")).to_contain_text("walkthrough.webm")
    assert console_errors == []


def test_a_test_that_cannot_run_the_app_waits_for_a_person_to_waive_it(
    page: Page, web_url: str, env, client: ApiClient, org: dict, forge_project: dict, fake_github: FakeGitHub, console_errors: list
):
    """A tester that cannot start the application blocks every criterion:
    no fix is sent, the delivery waits on a person. The banner names what
    was blocked and asks for a reason before a waiver; a colleague who is
    neither owner nor admin is refused it. Waived, the pull request opens,
    saying who waived what and why."""
    task = _deliver_with_tester(client, forge_project, "fake/test-blocked", "Greet behind a login")
    stopped = wait_for_escalation(client, task["id"], timeout=120, message="the blocked test never stopped for a person")
    assert stopped["escalation"]["reason"] == "test_blocked", stopped["escalation"]
    assert stopped["escalation"]["actions"] == ["retry", "waive", "stop"]
    assert [r["phase"] for r in stopped["runs"]].count("fix") == 1, "a blocked test must not send a fix"

    bo = ApiClient(env.control_plane_url, create_api_key(env.owner_dsn, org["id"], name="Bo"))
    refused = bo.post(f"/v1/tasks/{task['id']}/decide", {"action": "waive", "note": "Looks fine to me."})
    assert refused.status_code == 403, refused.text

    sign_in(page, web_url, org["api_key"])
    page.goto(f"{web_url}#/task/{task['id']}")
    escalation = page.get_by_test_id("escalation")
    expect(escalation).to_contain_text("Test blocked")
    expect(escalation.get_by_test_id("escalation-criterion")).to_have_count(2)
    escalation.get_by_test_id("escalation-waive").click()
    # Not sent: it asks why first.
    expect(escalation.get_by_test_id("escalation-note")).to_have_attribute("required", "")
    expect(escalation).to_contain_text("Say why: it is kept with the test's verdict and on the pull request.")
    assert client.get(f"/v1/tasks/{task['id']}").json()["status"] == "awaiting_input"
    escalation.get_by_test_id("escalation-note").fill("Checked by hand on staging.")
    escalation.get_by_test_id("escalation-waive").click()
    expect(page.get_by_test_id("escalation")).to_have_count(0)

    pr = _pull_request(client, task["id"])
    body = fake_github.pulls[pr["number"]].body
    assert "- **blocked, waived by e2e user: Checked by hand on staging.**" in body, body

    page.get_by_role("tab", name="Findings").click()
    rows = page.get_by_test_id("verification").get_by_test_id("criterion")
    expect(rows).to_have_count(2)
    expect(rows.nth(0)).to_have_attribute("data-state", "waived")
    expect(rows.nth(0).get_by_test_id("criterion-decision")).to_have_text("Waived by e2e user: Checked by hand on staging.")
    assert console_errors == []
