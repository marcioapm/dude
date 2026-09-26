"""A task delivered all the way to a pull request, and around the PR loop.

The PR is opened through the same REST calls the factory makes to github.com,
against a local stand-in (see `fake_github.py`) backed by a real git
repository. The "human" side — the comment, the merge — happens where a
person would do it, and reaches dude the way GitHub would tell it: as a
signed webhook.
"""

from __future__ import annotations

import time

from fake_github import FakeGitHub
from helpers import ApiClient, wait_until


def _pull_requests(client: ApiClient, task_id: str) -> list[dict]:
    return client.get("/v1/pull-requests", params={"taskId": task_id}).json()["pullRequests"]


def _task(client: ApiClient, task_id: str) -> dict:
    return client.get(f"/v1/tasks/{task_id}").json()


def test_delivery_opens_a_pull_request(client: ApiClient, forge_project: dict, fake_github: FakeGitHub):
    """Implement → review → fix → review → simplify → an open PR, untouched by hand."""
    task = client.create_task(
        forge_project["id"],
        "Record which run wrote the factory file",
        goal="Make the change and open a pull request.",
        acceptanceCriteria=["FACTORY.md names the run that wrote it"],
    )
    resp = client.post(f"/v1/tasks/{task['id']}/deliver")
    assert resp.status_code == 201, resp.text

    pr = wait_until(
        lambda: _pull_requests(client, task["id"]),
        timeout=60,
        message="delivery never opened a pull request",
    )[0]
    assert pr["state"] == "open"
    assert pr["headBranch"] == f"dude/{task['id']}/attempt-1"

    # On the forge, not just in our database.
    forge_pr = fake_github.pulls[pr["number"]]
    assert forge_pr.head == pr["headBranch"]
    assert forge_pr.base == "main"
    # The body is rendered from what the ledger recorded, including the
    # review that happened before the PR existed.
    assert "Acceptance criteria" in forge_pr.body
    assert "## Review" in forge_pr.body

    # Every publishing phase landed on the branch, by fast-forward: the
    # implementer, the fixer and the simplifier each added a commit.
    log = fake_github.branch_log(pr["headBranch"])
    assert len(log) == 4, log

    # Each phase pushed a branch of its own, and dude tidied them away.
    leftovers = [b for b in fake_github.branches() if "/run-" in b]
    assert leftovers == [], leftovers

    wait_until(
        lambda: _task(client, task["id"])["status"] == "review",
        timeout=30,
        message="task did not move to review once the PR opened",
    )


def test_pr_feedback_wakes_a_fixer_and_a_merge_finishes(
    client: ApiClient, forge_project: dict, fake_github: FakeGitHub
):
    """A person's comment brings the factory back; merging ends the task."""
    task = client.create_task(forge_project["id"], "Respond to review")
    client.post(f"/v1/tasks/{task['id']}/deliver")

    pr = wait_until(
        lambda: _pull_requests(client, task["id"]),
        timeout=60,
        message="delivery never opened a pull request",
    )[0]
    head_before = fake_github.branch_sha(pr["headBranch"])

    def fix_runs():
        return [r for r in client.task_runs(task["id"]) if r["phase"] == "fix"]

    fixes_before = len(fix_runs())

    # A courtesy comment is recorded but wakes nobody.
    assert fake_github.comment(pr["number"], "LGTM so far, thanks!") is None
    wait_until(
        lambda: any(e["eventType"] == "pull_request.commented" for e in client.events(taskId=task["id"])),
        timeout=20,
        message="the webhook never led to the comment being recorded",
    )
    # A fixer woken by "LGTM" would appear well within this.
    time.sleep(3)
    assert len(fix_runs()) == fixes_before, "a courtesy comment woke a fixer"

    # A real request does.
    fake_github.comment(pr["number"], "Please also note the date in FIXED.md.", path="FIXED.md")
    wait_until(lambda: len(fix_runs()) > fixes_before, timeout=30, message="a change request did not wake a fixer")
    assert len(fix_runs()) == fixes_before + 1, "the courtesy comment also woke a fixer"

    # The fixer's work reaches the PR's branch.
    wait_until(
        lambda: fake_github.branch_sha(pr["headBranch"]) != head_before,
        timeout=30,
        message="the fix never reached the pull request branch",
    )
    assert fix_runs()[-1]["baseRefs"], "the PR fix did not start from the PR's head"
    wait_until(
        lambda: _task(client, task["id"])["status"] == "review",
        timeout=30,
        message="task did not return to review after the fix",
    )

    # Approved, and the fixture has no CI: ready to merge, and it is a
    # person's merge — the factory never does it.
    fake_github.approve(pr["number"])
    wait_until(
        lambda: _task(client, task["id"])["status"] == "ready_to_merge",
        timeout=30,
        message="an approved pull request did not make the task ready to merge",
    )
    assert _pull_requests(client, task["id"])[0]["state"] == "open"

    # Time and cost, from what was recorded: every phase ran, the agents
    # worked, and the change has sat in review.
    metrics = client.get(f"/v1/tasks/{task['id']}/metrics").json()
    assert metrics["leadMs"] > 0 and metrics["activeMs"] > 0 and metrics["reviewMs"] > 0, metrics
    assert {r["phase"] for r in metrics["runs"]} >= {"implement", "review", "simplify"}, metrics["runs"]
    assert abs(metrics["costUsd"] - sum(r["costUsd"] for r in metrics["runs"])) < 1e-9

    fake_github.merge(pr["number"])
    wait_until(
        lambda: _task(client, task["id"])["status"] == "done",
        timeout=30,
        message="a merged PR did not finish the task",
    )
    assert _pull_requests(client, task["id"])[0]["state"] == "merged"
    # Every delivery was accepted: signed with the secret dude minted.
    assert fake_github.deliveries and all(status == 202 for _, status in fake_github.deliveries), fake_github.deliveries


def test_a_webhook_with_a_bad_signature_is_refused(client: ApiClient, forge_project: dict, fake_github: FakeGitHub):
    """The signature is the webhook's only authentication."""
    status = fake_github.send_webhook("pull_request", {"pull_request": {"number": 1}}, secret="not-the-secret")
    assert status == 401


def test_a_webhook_for_an_organization_without_a_forge_is_refused(env, fake_github: FakeGitHub):
    fake_github.webhook_url = f"{env.control_plane_url}/v1/webhooks/github/org_nobody"
    fake_github.webhook_secret = "anything"
    assert fake_github.send_webhook("pull_request", {"pull_request": {"number": 1}}) == 404
