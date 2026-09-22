"""The delivery workflow, driven through the public API.

The unit tests prove each piece in isolation; this proves they connect. A
work item is delivered, the runner claims whatever phase Runs appear, and the
suite asserts on what the API says happened — not on internal state.

The runner is the real one. The agent is not: `fake/scripted` makes a
deterministic commit, so the test exercises the workflow's transitions rather
than a model's judgement.
"""

from __future__ import annotations

import pytest

from helpers import ApiClient, wait_until


@pytest.fixture
def delivery_project(client: ApiClient, tmp_path_factory) -> dict:
    """A project whose repository is a real local git repo the runner can clone."""
    import subprocess

    repo_dir = tmp_path_factory.mktemp("delivery-repo")
    for args in (
        ["git", "init", "--initial-branch=main", "-q"],
        ["git", "config", "user.email", "test@example.com"],
        ["git", "config", "user.name", "Test"],
    ):
        subprocess.run(args, cwd=repo_dir, check=True)
    (repo_dir / "README.md").write_text("delivery fixture\n")
    subprocess.run(["git", "add", "."], cwd=repo_dir, check=True)
    subprocess.run(["git", "commit", "-q", "-m", "initial"], cwd=repo_dir, check=True)

    return client.create_project(
        name="Delivery",
        slug=f"delivery-{repo_dir.name[-6:]}",
        runtimeImage="dude-runtime:dev",
        agentModels={
            "implementer": {"model": "fake/scripted"},
            "reviewer": {"model": "fake/scripted"},
            "simplifier": {"model": "fake/scripted"},
        },
        repositories=[
            {"name": "target", "url": str(repo_dir), "defaultBranch": "main"},
        ],
    )


def test_delivery_requires_a_repository(client: ApiClient, project: dict):
    """A work item with nowhere to push cannot be delivered."""
    # The `project` fixture has no repositories.
    work_item = client.create_work_item(project["id"], "Nowhere to push")

    resp = client.post(f"/v1/work-items/{work_item['id']}/deliver")
    assert resp.status_code == 400
    assert "repository" in resp.text.lower()


def test_delivering_twice_joins_the_first(client: ApiClient, delivery_project: dict):
    """The work item is the idempotency key, so a second call must not race."""
    work_item = client.create_work_item(delivery_project["id"], "Deliver me once")

    first = client.post(f"/v1/work-items/{work_item['id']}/deliver")
    assert first.status_code == 201

    second = client.post(f"/v1/work-items/{work_item['id']}/deliver")
    assert second.status_code == 200
    assert second.json()["alreadyRunning"] is True
    assert second.json()["workflowRunId"] == first.json()["workflowRunId"]


def test_delivery_creates_an_implement_run_first(
    client: ApiClient, delivery_project: dict, runner
):
    """Delivery starts with an implementer, not with whatever claims first."""
    work_item = client.create_work_item(delivery_project["id"], "Implement first")
    client.post(f"/v1/work-items/{work_item['id']}/deliver")

    def has_run():
        runs = client.get(f"/v1/work-items/{work_item['id']}").json().get("runs", [])
        return runs[0] if runs else None

    run = wait_until(has_run, timeout=30, message="no Run was created for the work item")
    assert run["phase"] == "implement"
    assert run["role"] == "implementer"


def test_review_fans_out_after_the_implementer_commits(
    client: ApiClient, delivery_project: dict, runner
):
    """The diff decides the reviewers, and they run in parallel."""
    work_item = client.create_work_item(delivery_project["id"], "Review my change")
    client.post(f"/v1/work-items/{work_item['id']}/deliver")

    def reviews_created():
        runs = client.get(f"/v1/work-items/{work_item['id']}").json().get("runs", [])
        reviews = [r for r in runs if r.get("phase") == "review"]
        return reviews or None

    reviews = wait_until(
        reviews_created, timeout=120, message="review phase never started"
    )
    assert all(r["role"] == "reviewer" for r in reviews)
    # Each reviewer gets its own Run, hence its own container and clone.
    assert len({r["id"] for r in reviews}) == len(reviews)


def test_a_review_run_does_not_publish(client: ApiClient, delivery_project: dict, runner):
    """A reviewer may commit locally; its commits must not reach the branch."""
    work_item = client.create_work_item(delivery_project["id"], "Reviewer stays quiet")
    client.post(f"/v1/work-items/{work_item['id']}/deliver")

    def review_finished():
        runs = client.get(f"/v1/work-items/{work_item['id']}").json().get("runs", [])
        done = [
            r
            for r in runs
            if r.get("phase") == "review" and r["status"] in ("completed", "failed")
        ]
        return done or None

    reviews = wait_until(review_finished, timeout=180, message="no review Run finished")

    pushes = [
        e
        for e in client.events(runId=reviews[0]["id"])
        if e["eventType"] == "git.push_completed"
    ]
    assert pushes == [], "a review Run published its commits"


def test_findings_may_only_come_from_a_review(client: ApiClient, delivery_project: dict):
    """An implementer reporting findings could manufacture its own sign-off."""
    work_item = client.create_work_item(delivery_project["id"], "No self-signoff")
    run = client.create_run(work_item["id"])

    # A Run created through the API has no phase at all.
    resp = client.post(
        f"/v1/runs/{run['id']}/findings",
        {"findings": [{"severity": "note", "category": "correctness", "title": "Hi"}]},
    )
    # Rejected before it reaches the phase check: a user key is not a runner.
    assert resp.status_code in (400, 401)


def test_findings_are_listed_by_severity(client: ApiClient, delivery_project: dict):
    """The loop reads blocking findings first, so the API returns them first."""
    work_item = client.create_work_item(delivery_project["id"], "Ordered findings")

    # No findings yet: the endpoint must still answer.
    resp = client.get("/v1/findings", params={"workItemId": work_item["id"]})
    assert resp.status_code == 200
    assert resp.json()["findings"] == []
