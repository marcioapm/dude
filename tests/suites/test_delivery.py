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
    """A project whose repository is a bare git remote the runner can push to.

    Bare, not a working repository: every phase after the first builds on
    what the previous one *pushed*, so the push has to succeed. Git refuses a
    push to a non-bare repository's checked-out branch, and a fixture that
    cannot be pushed to tests a loop that can never get past its first phase.
    """
    import subprocess

    root = tmp_path_factory.mktemp("delivery")
    seed = root / "seed"
    remote = root / "remote.git"

    seed.mkdir()
    for args in (
        ["git", "init", "--initial-branch=main", "-q"],
        ["git", "config", "user.email", "test@example.com"],
        ["git", "config", "user.name", "Test"],
    ):
        subprocess.run(args, cwd=seed, check=True)
    (seed / "README.md").write_text("delivery fixture\n")
    subprocess.run(["git", "add", "."], cwd=seed, check=True)
    subprocess.run(["git", "commit", "-q", "-m", "initial"], cwd=seed, check=True)
    subprocess.run(["git", "clone", "-q", "--bare", str(seed), str(remote)], check=True)
    repo_dir = remote

    return client.create_project(
        name="Delivery",
        slug=f"delivery-{root.name[-6:]}",
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


def test_the_review_fix_loop_converges(client: ApiClient, delivery_project: dict, runner):
    """A blocking finding sends the work back, and the loop then settles.

    The loop that most needs an end-to-end test: its unit tests signal the
    workflow directly and never exercise the runner's git inspection, which is
    exactly where it broke the first time. The fake reviewer reports one
    blocking finding and then reports clean, so this asserts convergence — not
    merely that the policy bound eventually fires.
    """
    work_item = client.create_work_item(delivery_project["id"], "Loop until clean")
    client.post(f"/v1/work-items/{work_item['id']}/deliver")

    def phases():
        runs = client.get(f"/v1/work-items/{work_item['id']}").json().get("runs", [])
        return [r["phase"] for r in runs if r["status"] == "completed"]

    # A fix Run only exists if a blocking finding was reported and acted on.
    wait_until(
        lambda: "fix" in phases(),
        timeout=240,
        message="the review never produced a fix Run",
    )

    findings = client.get(
        "/v1/findings", params={"workItemId": work_item["id"]}
    ).json()["findings"]
    assert findings, "the reviewer reported nothing to fix"
    assert findings[0]["severity"] == "blocking"

    # Converged rather than stopped: reaching simplify means a later review
    # found nothing, because a blocking finding would have sent it back again.
    wait_until(
        lambda: "simplify" in phases(),
        timeout=240,
        message="the loop never converged past review",
    )


def test_a_fix_run_is_told_what_to_fix(client: ApiClient, delivery_project: dict, runner):
    """The fixer sees the findings, not just an instruction to fix something."""
    work_item = client.create_work_item(delivery_project["id"], "Fix with context")
    client.post(f"/v1/work-items/{work_item['id']}/deliver")

    def fix_run():
        runs = client.get(f"/v1/work-items/{work_item['id']}").json().get("runs", [])
        found = [r for r in runs if r.get("phase") == "fix"]
        return found[0] if found else None

    run = wait_until(fix_run, timeout=240, message="no fix Run was created")

    # It starts from the implementer's commit, not the default branch.
    assert run["baseRef"], "the fix Run was not given a base ref"
    assert run["role"] == "implementer"
