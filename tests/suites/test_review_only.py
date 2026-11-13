"""Review only: dude reviews a person's pull request and changes no code.

A review-only task on a pull request on the stand-in GitHub (`fake_github.py`)
runs its lenses at the head, starts no writer, and posts its verdict as one
GitHub review pinned to that head, then waits on the pull request.
"""

from __future__ import annotations

from fake_github import FakeGitHub
from helpers import ApiClient, wait_until


def test_a_clean_pull_request_with_green_ci_is_approved_at_its_head(
    client: ApiClient, forge_project: dict, fake_github: FakeGitHub
):
    """Lenses find nothing, CI green at the head → one APPROVE review at that head, no writer, never ready to merge."""
    resp = client.post("/v1/forge/credential", {"auth": "pat", "secret": "fake-token",
                                                "apiBaseUrl": fake_github.api_url, "publicUrl": "https://dude.example.com"})
    assert resp.status_code == 200, resp.text
    resp = client.patch(f"/v1/projects/{forge_project['id']}", {
        "deliveryPolicy": {"requiredReviewers": ["correctness"]},
        # fake/done's reviewer finds nothing.
        "agentModels": client.on_models({"reviewer": "fake/done"}),
    })
    assert resp.status_code == 200, resp.text
    head = fake_github.open_pull("feature/greet", title="Greet in Portuguese", author="gus")
    fake_github.add_check_run(head)
    task = client.create_task(
        forge_project["id"], "Review the Portuguese greeting", goal="Review the pull request and change nothing.",
        source={"kind": "pr", "repositoryId": forge_project["repositories"][0]["id"], "pullRequest": "#1",
                "mode": "review_only"},
    )
    assert task["source"]["mode"] == "review_only"
    resp = client.post(f"/v1/tasks/{task['id']}/deliver")
    assert resp.status_code == 201, resp.text

    marker = f"<!-- dude:review task={task['id']} "
    review = wait_until(
        lambda: next((r for r in fake_github.pulls[1].reviews if marker in r["body"]), None),
        timeout=120, message="dude never posted its review",
    )
    assert review["state"] == "APPROVED", review
    assert review["commit_id"] == head, review
    assert review["user"]["login"] == fake_github.token_login, review
    assert f"Approved at {head[:7]}" in review["body"], review["body"]

    wait_until(lambda: client.get(f"/v1/tasks/{task['id']}").json()["status"] == "review", timeout=60,
               message="the task never went back to waiting on the pull request")
    ours = [r for r in fake_github.pulls[1].reviews if marker in r["body"]]
    assert len(ours) == 1, ours
    phases = [r["phase"] for r in client.task_runs(task["id"]) if r["phase"]]
    assert phases and set(phases) <= {"review"}, phases
    assert fake_github.branch_sha("feature/greet") == head, "dude changed the author's branch"
    # One status comment, dude's own.
    status = [c for c in fake_github.pulls[1].comments if f"<!-- dude:verdict task={task['id']} -->" in c["body"]]
    assert len(status) == 1 and "Approved at" in status[0]["body"], fake_github.pulls[1].comments


def test_the_authors_fix_is_re_reviewed_and_approved_with_dudes_thread_resolved(
    client: ApiClient, forge_project: dict, fake_github: FakeGitHub
):
    """A finding → REQUEST_CHANGES at H1; the author pushes the fix (H2); after the debounce the lens says F1: fixed → APPROVE at H2, dude's thread on F1 answered and resolved."""
    resp = client.post("/v1/forge/credential", {"auth": "pat", "secret": "fake-token",
                                                "apiBaseUrl": fake_github.api_url, "publicUrl": "https://dude.example.com"})
    assert resp.status_code == 200, resp.text
    resp = client.patch(f"/v1/projects/{forge_project['id']}", {
        "deliveryPolicy": {"requiredReviewers": ["correctness"]},
        # fake/scripted's reviewer files one finding on FACTORY.md:1, and
        # answers `F1: fixed` once FIXED.md is in the tree it checks out.
        "agentModels": client.on_models({"reviewer": "fake/scripted"}),
    })
    assert resp.status_code == 200, resp.text
    fake_github.open_pull("feature/greet", title="Greet in Portuguese", author="gus")
    h1 = fake_github.commit_file("feature/greet", "FACTORY.md", "Olá\n", "Greet in Portuguese")
    fake_github.add_check_run(h1)
    task = client.create_task(
        forge_project["id"], "Review the Portuguese greeting", goal="Review the pull request and change nothing.",
        source={"kind": "pr", "repositoryId": forge_project["repositories"][0]["id"], "pullRequest": "#1",
                "mode": "review_only"},
    )
    resp = client.post(f"/v1/tasks/{task['id']}/deliver")
    assert resp.status_code == 201, resp.text

    marker = f"<!-- dude:review task={task['id']} "

    def ours(head: str) -> list[dict]:
        return [r for r in fake_github.pulls[1].reviews if marker in r["body"] and r["commit_id"] == head]

    first = wait_until(lambda: next(iter(ours(h1)), None), timeout=120, message="dude never reviewed H1")
    assert first["state"] == "CHANGES_REQUESTED", first
    roots = [c for c in fake_github.pulls[1].comments
             if c.get("path") and c.get("pull_request_review_id") == first["id"] and not c.get("in_reply_to_id")]
    assert len(roots) == 1, fake_github.pulls[1].comments
    wait_until(lambda: client.get(f"/v1/tasks/{task['id']}").json()["status"] == "review", timeout=60,
               message="the task never went back to waiting on the pull request")

    h2 = fake_github.commit_file("feature/greet", "FIXED.md", "the greeting is recorded\n", "Record the fix")
    fake_github.add_check_run(h2)
    second = wait_until(lambda: next(iter(ours(h2)), None), timeout=120, message="dude never re-reviewed H2")
    assert second["state"] == "APPROVED", second
    assert len(ours(h1)) == 1 and len(ours(h2)) == 1, fake_github.pulls[1].reviews
    replies = wait_until(
        lambda: [c for c in fake_github.pulls[1].comments if c.get("in_reply_to_id") == roots[0]["id"]] or None,
        timeout=30, message="dude never answered in its thread")
    assert [c["body"] for c in replies] == [f"Fixed at {h2[:7]}."], replies
    wait_until(lambda: fake_github.thread_id(roots[0]["id"]) in fake_github.resolved_threads, timeout=30,
               message="dude's thread on the fixed finding was not resolved")
    phases = [r["phase"] for r in client.task_runs(task["id"]) if r["phase"]]
    assert phases and set(phases) <= {"review"}, phases
