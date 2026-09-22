"""A work item delivered all the way to a pull request, and around the PR loop.

The last seam in the delivery chain: the workflow opens a PR against a forge,
waits on it, wakes a fixer when a person asks for a change, and finishes when
the PR merges. Driven against a local stand-in for GitHub (see
`fake_github.py`) so the push is a real `git push`, the PR is opened through the
same REST calls the factory makes to github.com, and the "human" side — the
comment, the merge — happens exactly where a person would do it.
"""

from __future__ import annotations

import pytest

from fake_github import FakeGitHub
from helpers import ApiClient, wait_until

# Delivery runs agents in containers, so it needs Docker and the runner.
pytestmark = pytest.mark.docker


def _pull_requests(client: ApiClient, work_item_id: str) -> list[dict]:
    return client.get("/v1/pull-requests", params={"workItemId": work_item_id}).json()["pullRequests"]


def _work_item(client: ApiClient, work_item_id: str) -> dict:
    return client.get(f"/v1/work-items/{work_item_id}").json()


def test_delivery_opens_a_pull_request(
    client: ApiClient, forge_project: dict, fake_github: FakeGitHub, runner
):
    """Implement → review → fix → review → simplify → an open PR, untouched by hand."""
    work_item = client.create_work_item(
        forge_project["id"],
        "Record which run wrote the factory file",
        goal="Make the change and open a pull request.",
        acceptanceCriteria=["FACTORY.md names the run that wrote it"],
    )
    resp = client.post(f"/v1/work-items/{work_item['id']}/deliver")
    assert resp.status_code == 201, resp.text

    prs = wait_until(
        lambda: _pull_requests(client, work_item["id"]),
        timeout=300,
        message="delivery never opened a pull request",
    )
    pr = prs[0]
    assert pr["state"] == "open"
    assert pr["headBranch"] == f"dude/{work_item['id']}/attempt-1"

    # On the forge, not just in our database.
    forge_pr = fake_github.pulls[pr["number"]]
    assert forge_pr.head == pr["headBranch"]
    assert forge_pr.base == "main"
    # The body is rendered from what the ledger recorded, including the
    # review that happened before the PR existed.
    assert "Acceptance criteria" in forge_pr.body
    assert "## Review" in forge_pr.body

    # Every publishing phase landed on the branch, in order.
    log = fake_github.branch_log(pr["headBranch"])
    assert any("Add FACTORY.md" in m for m in log), log
    assert any("Address review findings" in m for m in log), log
    assert any("Simplify" in m for m in log), log

    # The PR is open and waiting on people: the work item says so.
    wait_until(
        lambda: _work_item(client, work_item["id"])["status"] == "review",
        timeout=30,
        message="work item did not move to review once the PR opened",
    )


def test_pr_feedback_wakes_a_fixer_and_a_merge_finishes(
    client: ApiClient, forge_project: dict, fake_github: FakeGitHub, runner
):
    """A person's comment brings the factory back; merging ends the work item."""
    work_item = client.create_work_item(forge_project["id"], "Respond to review")
    client.post(f"/v1/work-items/{work_item['id']}/deliver")

    pr = wait_until(
        lambda: _pull_requests(client, work_item["id"]),
        timeout=300,
        message="delivery never opened a pull request",
    )[0]
    head_before = fake_github.branch_sha(pr["headBranch"])

    def fix_runs():
        runs = _work_item(client, work_item["id"]).get("runs", [])
        return [r for r in runs if r["phase"] == "fix"]

    fixes_before = len(fix_runs())

    # A courtesy comment is recorded but wakes nobody.
    fake_github.comment(pr["number"], "LGTM so far, thanks!")
    wait_until(
        lambda: any(
            e["eventType"] == "pull_request.commented"
            for e in client.events(workItemId=work_item["id"])
        ),
        timeout=30,
        message="the poller never recorded the comment",
    )
    # Give the workflow a few poll cycles to (wrongly) react before asserting
    # it did not: a fixer woken by "LGTM" would appear well within this.
    import time

    time.sleep(8)
    assert len(fix_runs()) == fixes_before, "a courtesy comment woke a fixer"

    # A real request does.
    fake_github.comment(pr["number"], "Please also note the date in FIXED.md.", path="FIXED.md")
    wait_until(
        lambda: len(fix_runs()) > fixes_before,
        timeout=60,
        message="a change request did not wake a fixer",
    )
    assert len(fix_runs()) == fixes_before + 1, "the courtesy comment also woke a fixer"

    # The fixer pushes to the same branch, updating the open PR.
    wait_until(
        lambda: fake_github.branch_sha(pr["headBranch"]) != head_before,
        timeout=120,
        message="the fix never reached the pull request branch",
    )
    new_fix = fix_runs()[-1]
    assert new_fix["baseRef"], "the PR fix did not start from the PR's head"

    wait_until(
        lambda: _work_item(client, work_item["id"])["status"] == "review",
        timeout=60,
        message="work item did not return to review after the fix",
    )

    fake_github.merge(pr["number"])
    wait_until(
        lambda: _work_item(client, work_item["id"])["status"] == "done",
        timeout=60,
        message="a merged PR did not finish the work item",
    )
    assert _pull_requests(client, work_item["id"])[0]["state"] == "merged"
