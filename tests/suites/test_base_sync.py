"""Two tasks change the same lines; one merges, and the other's pull request
then conflicts with its base. dude merges the base into the second task's
branch with an integration Run — a fixer that calls sync_base, merges
lux/<base> and resolves keeping both sides — reviews the merge with
`git show --remerge-diff`, and waits on the pull request again, clean.

With `whenBehind` `update` nobody is asked. With `tell` the conflict
escalates, and a person's **Resolve conflicts** on the pull request card
starts the same integration.

The fake GitHub works mergeability out from git here
(`mergeability_from_git`), so the conflict and its resolution are real
merges of real branches, not flags.
"""

from __future__ import annotations

import pytest
from playwright.sync_api import Page, expect

from fake_github import FakeGitHub
from helpers import ApiClient, query, sign_in, wait_for_escalation, wait_until


def _pull_request(client: ApiClient, task_id: str) -> dict | None:
    prs = client.get("/v1/pull-requests", params={"taskId": task_id}).json()["pullRequests"]
    return prs[0] if prs else None


def _status(client: ApiClient, task_id: str) -> str:
    return client.get(f"/v1/tasks/{task_id}").json()["status"]


def _two_tasks_in_review(client: ApiClient, project: dict) -> tuple[dict, dict]:
    """Two tasks delivered at once; the scripted implementer writes
    FACTORY.md in each, so their branches change the same lines."""
    a = client.create_task(project["id"], "Greet in English")
    b = client.create_task(project["id"], "Greet in Portuguese")
    for t in (a, b):
        assert client.post(f"/v1/tasks/{t['id']}/deliver").status_code == 201
    for t in (a, b):
        wait_until(lambda t=t: _pull_request(client, t["id"]), timeout=120, message=f"no pull request for {t['key']}")
        wait_until(lambda t=t: _status(client, t["id"]) == "review", timeout=60, message=f"{t['key']} never reached review")
    return a, b


def _integrations(env, task_id: str) -> int:
    return query(env.owner_dsn, "SELECT count(*) AS n FROM runs WHERE task_id = %s AND pr_feedback::text LIKE %s",
                 (task_id, "%Base integration%"))[0]["n"]


def _assert_integrated(client: ApiClient, env, fake_github: FakeGitHub, task_id: str) -> None:
    """The pull request is clean against the moved base, its branch holds
    both tasks' lines, and an internal review read the merge."""
    pr0 = _pull_request(client, task_id)
    assert pr0 is not None
    wait_until(lambda: any(m.startswith("Merge remote-tracking branch 'lux/main'") for m in fake_github.branch_log(pr0["headBranch"])),
               timeout=180, message="the integration merge was not pushed")
    # fake-lux's git push is not a GitHub webhook; notify the same synchronize
    # event GitHub sends for the pushed integration head.
    fake_github.send_webhook("pull_request", {"action": "synchronize", "pull_request": {"number": pr0["number"]}})
    try:
        pr = wait_until(lambda: (p := _pull_request(client, task_id)) and p["mergeable"] == "clean" and p, timeout=180,
                        message="the pull request never went clean")
    finally:
        print("runs:", [(r["phase"], r["status"], r.get("error")) for r in client.task_runs(task_id)])
        print("pull request:", _pull_request(client, task_id))
        print("workflow:", query(env.owner_dsn, "SELECT step, status, last_error FROM workflow_runs WHERE task_id = %s", (task_id,)))
    wait_until(lambda: _status(client, task_id) == "review", timeout=60, message="the delivery never waited on the pull request again")
    branch = pr["headBranch"]
    content = fake_github.file_at(branch, "FACTORY.md") or ""
    base = fake_github.file_at("main", "FACTORY.md") or ""
    assert base.strip() and base.strip() in content, (base, content)
    assert len([line for line in content.splitlines() if line.strip() in {"A intent", "B intent"}]) == 2, content
    assert "<<<<<<<" not in content, content
    assert any(m.startswith("Merge remote-tracking branch 'lux/main'") for m in fake_github.branch_log(branch)), \
        fake_github.branch_log(branch)
    reviews = wait_until(lambda: query(env.owner_dsn, "SELECT count(*) AS n FROM runs WHERE task_id = %s AND phase = 'review' "
                         "AND status = 'completed' AND pr_feedback::text LIKE %s", (task_id, "%remerge-diff%"))[0]["n"],
                         timeout=90, message="the integration review did not complete")
    assert reviews == 1, reviews
    assert _integrations(env, task_id) == 1
    def actions_settled():
        actions = [e["payload"]["action"] for e in client.events(taskId=task_id, limit=1000)
                   if e["eventType"] == "pull_request.action"]
        return actions if "integration-started" in actions and "integration-completed" in actions else None
    actions = wait_until(actions_settled, timeout=60, message="integration activity did not settle")
    assert "integration-started" in actions and "integration-completed" in actions, actions
    assert client.get(f"/v1/tasks/{task_id}").json().get("escalation") is None


@pytest.fixture
def conflicting(client: ApiClient, forge_project: dict, fake_github: FakeGitHub):
    """Two tasks in review on the same lines; returns a starter that merges
    the first into main and returns both."""
    fake_github.mergeability_from_git = True

    def start(when_behind: str) -> tuple[dict, dict]:
        assert client.patch("/v1/forge/settings", {"whenBehind": when_behind}).status_code == 200
        a, b = _two_tasks_in_review(client, forge_project)
        first = _pull_request(client, a["id"])
        second = _pull_request(client, b["id"])
        assert first is not None and second is not None
        # Make the fixture's two branches edit the same line in opposite
        # ways; the initial scripted append is intentionally too mergeable.
        fake_github.commit_file(first["headBranch"], "FACTORY.md", "A intent\n", "A's intent")
        fake_github.commit_file(second["headBranch"], "FACTORY.md", "B intent\n", "B's intent")
        fake_github.merge_into_base(first["number"])
        wait_until(lambda: _status(client, a["id"]) == "done", timeout=60, message="the merged task never finished")
        return a, b

    return start


def test_a_conflict_after_another_task_merges_is_resolved_by_dude(
    client: ApiClient, env, fake_github: FakeGitHub, conflicting
):
    _, b = conflicting("update")
    _assert_integrated(client, env, fake_github, b["id"])


@pytest.mark.ui
def test_a_person_asks_dude_to_resolve_the_conflict(
    page: Page, web_url: str, client: ApiClient, org: dict, env, fake_github: FakeGitHub, conflicting, console_errors: list
):
    _, b = conflicting("tell")
    shown = wait_for_escalation(client, b["id"], timeout=60, message="the conflict never escalated")
    assert shown["escalation"]["reason"] == "pull_request_conflict"
    assert _integrations(env, b["id"]) == 0, "integrated though the organization asked only to be told"

    sign_in(page, web_url, org["api_key"])
    page.goto(f"{web_url}/#/task/{b['id']}")
    expect(page.get_by_test_id("escalation")).to_contain_text("ask dude to resolve it", timeout=30_000)
    expect(page.get_by_test_id("pr-panel")).to_contain_text("Conflicts with main")
    page.get_by_test_id("pr-resolve-conflicts").click()
    expect(page.get_by_test_id("escalation")).to_have_count(0, timeout=30_000)

    _assert_integrated(client, env, fake_github, b["id"])
    page.get_by_role("tab", name="Activity").click()
    expect(page.get_by_test_id("activity")).to_contain_text("asked dude to resolve the conflicts on", timeout=30_000)
    expect(page.get_by_test_id("activity")).to_contain_text("reviewed and tested", timeout=30_000)
    assert not console_errors, console_errors
