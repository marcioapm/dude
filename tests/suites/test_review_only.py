"""Review only: dude reviews a person's pull request and changes no code.

A review-only task on a pull request on the stand-in GitHub (`fake_github.py`)
runs its lenses at the head, starts no writer, and posts its verdict as one
GitHub review pinned to that head, then waits on the pull request.
"""

from __future__ import annotations

import json

from fake_github import FakeGitHub
from helpers import ApiClient, query, wait_until


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
    assert len([r for r in fake_github.pulls[1].reviews if marker in r["body"]]) == 2, fake_github.pulls[1].reviews
    replies = wait_until(
        lambda: [c for c in fake_github.pulls[1].comments if c.get("in_reply_to_id") == roots[0]["id"]] or None,
        timeout=30, message="dude never answered in its thread")
    assert [c["body"] for c in replies] == [f"Fixed at {h2[:7]}."], replies
    wait_until(lambda: fake_github.thread_id(roots[0]["id"]) in fake_github.resolved_threads, timeout=30,
               message="dude's thread on the fixed finding was not resolved")
    phases = [r["phase"] for r in client.task_runs(task["id"]) if r["phase"]]
    assert phases and set(phases) <= {"review"}, phases


def test_the_authors_reply_in_dudes_thread_reaches_the_conductor_and_a_wrong_finding_is_withdrawn(
    client: ApiClient, forge_project: dict, fake_github: FakeGitHub, owner_dsn: str
):
    """REQUEST_CHANGES at H1; the author replies in dude's thread, no mention → the conductor hears it, naming the finding;
    the (scripted) conductor dismisses it as wrong → APPROVE at H1, no new round, dude's thread withdrawn and resolved."""
    resp = client.post("/v1/forge/credential", {"auth": "pat", "secret": "fake-token",
                                                "apiBaseUrl": fake_github.api_url, "publicUrl": "https://dude.example.com"})
    assert resp.status_code == 200, resp.text
    resp = client.patch(f"/v1/projects/{forge_project['id']}", {
        "deliveryPolicy": {"requiredReviewers": ["correctness"]},
        # fake/scripted's reviewer files one finding on FACTORY.md:1.
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

    def ours() -> list[dict]:
        return [r for r in fake_github.pulls[1].reviews if marker in r["body"] and r["commit_id"] == h1]

    first = wait_until(lambda: next(iter(ours()), None), timeout=120, message="dude never reviewed H1")
    assert first["state"] == "CHANGES_REQUESTED", first
    root = next(c for c in fake_github.pulls[1].comments
                if c.get("path") and c.get("pull_request_review_id") == first["id"] and not c.get("in_reply_to_id"))
    wait_until(lambda: client.get(f"/v1/tasks/{task['id']}").json()["status"] == "review", timeout=60,
               message="the task never went back to waiting on the pull request")
    finding = query(owner_dsn, "SELECT id FROM review_findings WHERE task_id = %s AND status = 'open'", (task["id"],))
    assert len(finding) == 1, finding
    reviews = len([r for r in client.task_runs(task["id"]) if r["phase"] == "review"])

    # The author's reply, no @mention. The scripted conductor carries out
    # the tool line it is told: what a real one decides on reading the code.
    dismiss = {"id": finding[0]["id"], "reason": "wrong: FACTORY.md:1 is the greeting the task asks for, as the author showed"}
    reply = fake_github.comment(1, "This is intentional: the greeting is meant to be Portuguese.\n\n"
                                f"tool: dismiss_finding {json.dumps(dismiss)}", author="gus", in_reply_to=root["id"])
    told = wait_until(
        lambda: next((e for e in client.events(taskId=task["id"]) if e["eventType"] == "chat.message"
                      and (e["payload"].get("github") or {}).get("feedbackId") == f"line-comment-{reply}"), None),
        timeout=60, message="the author's reply never reached the conductor")
    assert told["payload"]["github"]["login"] == "gus", told
    names = f"It replies in dude's thread on finding {finding[0]['id']}"
    assert query(owner_dsn, """SELECT 1 FROM runs WHERE task_id = %s AND role = 'conductor' AND strpos(prompt, %s) > 0
        UNION ALL SELECT 1 FROM directives WHERE task_id = %s AND strpos(text, %s) > 0""",
                 (task["id"], names, task["id"], names)), "the conductor was not told which finding the thread is about"

    second = wait_until(lambda: len(ours()) == 2 and ours()[1], timeout=120, message="dude never decided again at H1")
    assert second["state"] == "APPROVED", second
    assert len([r for r in client.task_runs(task["id"]) if r["phase"] == "review"]) == reviews, "a new round ran"
    replies = wait_until(
        lambda: [c["body"] for c in fake_github.pulls[1].comments
                 if c.get("in_reply_to_id") == root["id"] and c["user"]["login"] == fake_github.token_login] or None,
        timeout=30, message="dude never answered in its thread")
    assert replies == ["Withdrawn: FACTORY.md:1 is the greeting the task asks for, as the author showed."], replies
    wait_until(lambda: fake_github.thread_id(root["id"]) in fake_github.resolved_threads, timeout=30,
               message="dude's thread on the withdrawn finding was not resolved")
