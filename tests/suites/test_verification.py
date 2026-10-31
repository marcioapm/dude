"""The verification gate, end to end: a delivery reaches its pull request
only when the tester has shown every acceptance criterion working, with
evidence a person can open on the task — or a person waived what could not
be checked, with a reason.

The tester is the scripted one (`fakeagent.Tester`): `fake/test-fail-once`
fails the first criterion until a fix after the review loop's own is in
the tree, `fake/test-blocked` cannot start the application.
"""

from __future__ import annotations

import json
import subprocess

import pytest
import requests
from playwright.sync_api import Page, expect

from fake_github import FakeGitHub
from helpers import ApiClient, create_api_key, open_overview_section, sign_in, wait_for_escalation, wait_until

CRITERIA = ["The page greets the visitor by name", "The page says goodbye on the way out"]


def _deliver_with_tester(client: ApiClient, project: dict, tester: str, title: str) -> dict:
    # forge_project pins the tester off; these turn it on, as the factory does.
    # The scripted writers' Markdown files stand for product changes: every fix is tested.
    client.patch(f"/v1/projects/{project['id']}", {
        "deliveryPolicy": {"requiredReviewers": ["correctness"], "test": True, "nonProductPaths": [".github/**"], "pullRequestFixes": "candidate"},
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


@pytest.mark.ui
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


@pytest.mark.ui
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


START, END = "<!-- dude:verification -->", "<!-- /dude:verification -->"


def _outside_section(body: str) -> tuple[str, str]:
    """What a pull request body holds before dude's section and after it."""
    assert body.count(START) == 1 and body.count(END) == 1, body
    return body[:body.index(START)], body[body.index(END) + len(END):]


def test_a_new_verdict_refreshes_only_dudes_section_of_the_pull_request_body(
    client: ApiClient, forge_project: dict, fake_github: FakeGitHub
):
    """A pull request dude opened carries its verification section. A
    person edits the description on GitHub; then they push, and the
    conductor has the work tested again while the pull request waits. dude
    rewrites its section for the new head and leaves every byte the person
    wrote where it was."""
    client.patch(f"/v1/projects/{forge_project['id']}", {
        "deliveryPolicy": {"requiredReviewers": ["correctness"], "test": True, "nonProductPaths": [".github/**"], "pullRequestFixes": "candidate"},
        "agentModels": client.on_models({"implementer": "fake/scripted", "reviewer": "fake/scripted",
                                         "simplifier": "fake/scripted", "qa_browser": "fake/scripted",
                                         "conductor": "fake/scripted"}),
    })
    # Warm throughout: the second message reaches a live conductor.
    assert client.patch(f"/v1/projects/{forge_project['id']}/settings",
                        {"delivery": {"conductorWarmMinutes": 30}}).status_code == 200
    task = client.create_task(forge_project["id"], "Greet and say goodbye", acceptanceCriteria=CRITERIA)
    assert client.post(f"/v1/tasks/{task['id']}/deliver").status_code == 201
    pr = _pull_request(client, task["id"])
    number = pr["number"]
    wait_until(lambda: client.get(f"/v1/tasks/{task['id']}").json()["status"] == "review", timeout=60,
               message="the delivery never waited on its pull request")
    opened = fake_github.pulls[number].body
    tested = fake_github.branch_sha(pr["headBranch"]) or ""
    assert f"{START}\n## Verification\nTested at `{tested[:7]}`." in opened, opened
    assert fake_github.body_edits == [], fake_github.body_edits

    # The person's edit, through GitHub's API as their browser makes it: a
    # paragraph above dude's section, and a line after it.
    mine = ("Context for reviewers — façade first, then the 👋 copy.  \n"
            "Trailing spaces and a [link](https://example.test/a_b) stay as typed.\n\n")
    before, after = _outside_section(opened)
    edited = before + mine + opened[len(before):] + "\n— Ana\n"
    resp = requests.patch(f"{fake_github.api_url}/repos/{fake_github.owner}/{fake_github.repo}/issues/{number}",
                          json={"body": edited}, headers={"authorization": "token fake-token"}, timeout=10)
    assert resp.status_code == 200 and resp.json()["body"] == edited, resp.text
    assert fake_github.pulls[number].body == edited
    theirs = _outside_section(edited)

    pushed = fake_github.commit(pr["headBranch"], "A person's change")
    resp = client.post(f"/v1/tasks/{task['id']}/chat", {"text": "I'll steer from here."})
    assert resp.status_code in (200, 201) and resp.json()["decider"] == "conductor", resp.text
    tests = [r for r in client.task_runs(task["id"]) if r["phase"] == "test"]
    resp = client.post(f"/v1/tasks/{task['id']}/chat",
                       {"text": "Test it again.\ntool: start_phase " + json.dumps({"phase": "test"})})
    assert resp.status_code in (200, 201), resp.text
    wait_until(lambda: len([r for r in client.task_runs(task["id"]) if r["phase"] == "test" and r["status"] == "completed"])
               > len(tests), timeout=120, message="the conductor's test never completed")

    def refreshed() -> bool:
        return any(e["payload"].get("number") == number and e["payload"].get("head") == pushed
                   and "error" not in e["payload"]
                   for e in client.events(taskId=task["id"], limit=1000)
                   if e["eventType"] == "pull_request.verification_updated")
    wait_until(refreshed, timeout=60, message="dude never refreshed the section for the new head")

    body = fake_github.pulls[number].body
    assert f"Tested at `{pushed[:7]}`." in body and f"Tested at `{tested[:7]}`." not in body, body
    assert _outside_section(body) == theirs, body
    # The person's edit and dude's one, which sent the body alone.
    assert [n for n, _ in fake_github.body_edits] == [number, number], fake_github.body_edits
    assert list(fake_github.body_edits[1][1]) == ["body"], fake_github.body_edits


@pytest.mark.ui
def test_a_pull_request_fix_has_fresh_verification_at_its_promoted_head(
    page: Page, web_url: str, client: ApiClient, org: dict, forge_project: dict, fake_github: FakeGitHub, console_errors: list
):
    """A comment brings a fixer whose fix is reviewed and tested on a
    candidate. At the promoted head, the task page and PR have fresh
    verification. The Go integration tests check the strict ordering of
    the completed test and the branch move."""
    task = _deliver_with_tester(client, forge_project, "fake/scripted", "Greet, then rename it")
    pr = _pull_request(client, task["id"])
    wait_until(lambda: client.get(f"/v1/tasks/{task['id']}").json()["status"] == "review", timeout=60,
               message="the task never waited on the pull request")
    before = fake_github.branch_sha(pr["headBranch"])
    tests_before = [r["id"] for r in client.task_runs(task["id"]) if r["phase"] == "test"]

    fake_github.comment(pr["number"], "Please rename the greeting.", author="alice")

    # While the candidate is checked the pull request still holds `before`:
    # its verdict there is never stale, whatever the candidate's heads, and
    # a verdict at the candidate's current head is the candidate's.
    while_checked: list[bool] = []
    at_candidate: list[dict] = []

    def moved():
        if (sha := fake_github.branch_sha(pr["headBranch"])) != before:
            return sha
        if candidate := fake_github.branch_sha(f"{pr['headBranch']}-candidate"):
            verdicts = client.get(f"/v1/tasks/{task['id']}/verification").json()["verifications"]
            at_pr = next((v for v in verdicts if v["heads"].get("greeter") == before), None)
            at_head = next((v for v in verdicts if v["heads"].get("greeter") == candidate), None)
            if fake_github.branch_sha(pr["headBranch"]) == before and \
                    fake_github.branch_sha(f"{pr['headBranch']}-candidate") == candidate:
                if at_pr is not None:
                    while_checked.append(at_pr["stale"])
                if at_head is not None and candidate != before:
                    at_candidate.append(at_head)
        return None

    head = wait_until(moved, timeout=180, interval=0.2, message="the fix never reached the pull request")
    assert while_checked and not any(while_checked), while_checked
    assert all(v["candidate"] is True for v in at_candidate), at_candidate
    runs = client.task_runs(task["id"])
    fix = next(r for r in runs if r["phase"] == "fix" and r["createdAt"] > next(x for x in runs if x["id"] == tests_before[-1])["createdAt"])
    assert fix["branch"] == f"{pr['headBranch']}-candidate", fix
    assert any(r["phase"] == "review" and r["reviewOf"] == "pull_request_fix" for r in runs), [r["phase"] for r in runs]

    latest = wait_until(lambda: next((v for v in client.get(f"/v1/tasks/{task['id']}/verification").json()["verifications"]
                                      if v["heads"].get("greeter") == head), None), timeout=30,
                        message="no verdict at the pull request's new head")
    assert latest["stale"] is False, latest
    # The verdict at the new head is the candidate's, recorded before the
    # move; the step after the move refreshes the description.
    fresh = f"Tested at `{head[:7]}`."
    body = wait_until(lambda: (b if fresh in (b := fake_github.pulls[pr["number"]].body) else None), timeout=30,
                      message="the pull request's description was not refreshed at its new head")
    assert "Stale" not in body, body

    sign_in(page, web_url, org["api_key"])
    page.goto(f"{web_url}#/task/{task['id']}")
    open_overview_section(page, "pipeline")
    expect(page.get_by_test_id("phase").filter(has_text="of the pull request's fix").first).to_be_visible()
    page.get_by_role("tab", name="Findings").click()
    section = page.get_by_test_id("verification")
    expect(section.get_by_test_id("verification-heads")).to_have_text(f"tested at {head[:7]}")
    expect(section.get_by_test_id("verification-stale")).to_have_count(0)
    assert console_errors == []
