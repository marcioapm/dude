"""dude's GitHub actions on a task's own pull request, end to end.

Through the deployed processes and the public API, against the stand-in
GitHub (`fake_github.py`). A scripted conductor carries out the tool calls a
person's Chat message names ("tool: NAME {json}"), and a scripted fixer
those its pull request feedback names: each goes through dude's tool
server to GitHub, or is refused there, as a real agent's call would.
"""

from __future__ import annotations

import json

from fake_github import FakeGitHub
from helpers import ApiClient, wait_until


def _actions(client: ApiClient, task_id: str) -> list[dict]:
    return [e["payload"] for e in client.events(taskId=task_id, limit=1000) if e["eventType"] == "pull_request.action"]


def _call(name: str, **args) -> str:
    return f"tool: {name} {json.dumps(args)}"


def _conducted_pull_request(client: ApiClient, forge_project: dict) -> tuple[dict, int]:
    """A task delivered to its pull request, then handed to a scripted conductor."""
    models = {**forge_project["agentModels"], **client.on_models({"conductor": "fake/scripted"})}
    assert client.patch(f"/v1/projects/{forge_project['id']}", {"agentModels": models}).status_code == 200
    assert client.patch(f"/v1/projects/{forge_project['id']}/settings",
                        {"delivery": {"conductorWarmMinutes": 30}}).status_code == 200
    task = client.create_task(forge_project["id"], "Greet, looked after by the conductor")
    assert client.post(f"/v1/tasks/{task['id']}/deliver").status_code == 201
    pr = wait_until(lambda: client.get("/v1/pull-requests", params={"taskId": task["id"]}).json()["pullRequests"],
                    timeout=90, message="no pull request opened")[0]
    wait_until(lambda: client.get(f"/v1/tasks/{task['id']}").json()["status"] == "review", timeout=60,
               message="the task never reached review")
    resp = client.post(f"/v1/tasks/{task['id']}/chat", {"text": "I'll steer this from here."})
    assert resp.status_code == 201 and resp.json()["decider"] == "conductor", resp.text
    return task, pr["number"]


def _say(client: ApiClient, task_id: str, *lines: str) -> None:
    resp = client.post(f"/v1/tasks/{task_id}/chat", {"text": "\n".join(lines)})
    assert resp.status_code in (200, 201), resp.text


def test_the_conductor_acts_on_its_own_pull_request_and_nowhere_else(
    client: ApiClient, forge_project: dict, fake_github: FakeGitHub
):
    task, number = _conducted_pull_request(client, forge_project)
    other = fake_github.open_pull("feature/someone-else", title="Not this task's", author="gus")
    other_number = len(fake_github.pulls)
    assert other_number != number and other

    # Ask for a review; ask on another pull request (refused, never reaches GitHub).
    _say(client, task["id"], _call("request_review", pr=str(number), logins=["ana"]),
         _call("request_review", pr=str(other_number), logins=["ana"]))
    wait_until(lambda: (number, ["ana"]) in fake_github.review_requests, timeout=60,
               message="the review request never reached GitHub")
    assert all(n != other_number for n, _ in fake_github.review_requests), fake_github.review_requests

    # Resolve a thread on its pull request; one of the other's is refused.
    mine = fake_github.comment(number, "Say why.", author="ana", path="README.md")
    theirs = fake_github.comment(other_number, "Elsewhere.", author="ana", path="README.md")
    _say(client, task["id"], _call("resolve_thread", pr=str(number), thread=f"line-comment-{mine}"),
         _call("resolve_thread", pr=str(number), thread=fake_github.thread_id(theirs)))
    wait_until(lambda: fake_github.thread_id(mine) in fake_github.resolved_threads, timeout=60,
               message="the thread was never resolved")
    assert fake_github.thread_id(theirs) not in fake_github.resolved_threads

    # A draft marked ready.
    fake_github.pulls[number].draft = True
    fake_github.send_webhook("pull_request", {"action": "converted_to_draft", "pull_request": {"number": number}})
    wait_until(lambda: client.get("/v1/pull-requests", params={"taskId": task["id"]}).json()["pullRequests"][0]["state"] == "draft",
               timeout=30, message="dude never saw the draft")
    _say(client, task["id"], _call("mark_ready", pr=str(number)), _call("mark_ready", pr=str(other_number)))
    wait_until(lambda: fake_github.marked_ready == [number], timeout=60, message="the draft was never marked ready")

    # Failed checks run again, once for this head however often asked.
    fake_github.set_check(number, "ci", conclusion="failure")
    wait_until(lambda: client.get("/v1/pull-requests", params={"taskId": task["id"]}).json()["pullRequests"][0]["checkState"] == "failing",
               timeout=30, message="dude never saw the failing check")
    _say(client, task["id"], _call("rerun_failed_checks", pr=str(number)), _call("rerun_failed_checks", pr=str(number)),
         _call("rerun_failed_checks", pr=str(other_number)))
    wait_until(lambda: len(fake_github.jobs_rerun) == 1, timeout=60, message="the failed check never ran again")

    done = wait_until(lambda: (lambda a: a if len(a) >= 4 else None)(_actions(client, task["id"])), timeout=30,
                      message="the actions were not recorded")
    assert [a["action"] for a in done] == ["request_review", "resolve_thread", "mark_ready", "rerun_failed_checks"], done
    assert all(a["number"] == number for a in done), done
    assert len(fake_github.jobs_rerun) == 1


def test_a_fixer_answers_and_resolves_only_its_own_batch(
    client: ApiClient, forge_project: dict, fake_github: FakeGitHub
):
    task = client.create_task(forge_project["id"], "Greet, with review threads")
    assert client.post(f"/v1/tasks/{task['id']}/deliver").status_code == 201
    pr = wait_until(lambda: client.get("/v1/pull-requests", params={"taskId": task["id"]}).json()["pullRequests"],
                    timeout=90, message="no pull request opened")[0]
    number = pr["number"]
    wait_until(lambda: client.get(f"/v1/tasks/{task['id']}").json()["status"] == "review", timeout=60,
               message="the task never reached review")

    earlier = fake_github.comment(number, "Rename greet.", author="ana", path="README.md")
    wait_until(lambda: sum(1 for r in client.task_runs(task["id"]) if r["phase"] == "fix" and r["status"] == "completed") >= 2,
               timeout=90, message="the first feedback was never fixed")
    wait_until(lambda: client.get(f"/v1/tasks/{task['id']}").json()["status"] in ("review", "ready_to_merge"), timeout=60,
               message="the task never went back to review")

    # The comment names its own id: the fixer's batch is this comment alone.
    own = fake_github._next_id + 1
    body = "\n".join(["Please explain the greeting.",
                      _call("reply_on_pull_request", pr=str(number), text="Explained it.", in_reply_to=f"line-comment-{own}"),
                      _call("resolve_thread", pr=str(number), thread=f"line-comment-{own}"),
                      _call("resolve_thread", pr=str(number), thread=f"line-comment-{earlier}")])
    assert fake_github.comment(number, body, author="ana", path="README.md") == own
    wait_until(lambda: fake_github.thread_id(own) in fake_github.resolved_threads, timeout=90,
               message="the fixer never resolved its thread")
    reply = next(c for c in fake_github.pulls[number].comments if c.get("in_reply_to_id") == own)
    assert reply["user"]["login"] == "dude-bot" and "Explained it." in reply["body"], reply
    assert fake_github.thread_id(earlier) not in fake_github.resolved_threads


def test_a_requested_reviewer_is_asked_again_after_dudes_fix_once(
    client: ApiClient, forge_project: dict, fake_github: FakeGitHub
):
    """jerv reviewed the person's head: dude's fix makes it stale, and jerv is asked again, once."""
    fake_github.open_pull("feature/again", title="Ask again", author="gus")
    task = client.create_task(forge_project["id"], "Adopt and ask again", goal="Take over the open pull request.",
                              source={"kind": "pr", "repositoryId": forge_project["repositories"][0]["id"], "pullRequest": "1"})
    fake_github.review(1, "COMMENTED", body="Looks fine so far.", reviewer="jerv")
    assert client.post(f"/v1/tasks/{task['id']}/deliver").status_code == 201
    wait_until(lambda: any(r["phase"] == "simplify" and r["status"] == "completed" for r in client.task_runs(task["id"]))
               and client.get(f"/v1/tasks/{task['id']}").json()["status"] == "review",
               timeout=120, message="the adopted pull request never got back to review")
    wait_until(lambda: (1, ["jerv"]) in fake_github.review_requests, timeout=30, message="jerv was never asked again")
    assert fake_github.review_requests.count((1, ["jerv"])) == 1, fake_github.review_requests
    assert len(fake_github.pulls) == 1


def test_the_stand_ins_thread_queries_agree(fake_github: FakeGitHub):
    """The count-only query and the thread-node query say the same unresolved count, through resolution."""
    import requests

    fake_github.open_pull("feature/threads", title="Threads")
    roots = [fake_github.comment(1, f"Line note {i}.", path="README.md") for i in range(2)]
    fake_github.set_unresolved(1, 0)

    def unresolved() -> tuple[int, int]:
        vars = {"owner": fake_github.owner, "name": fake_github.repo, "number": 1}
        counted = requests.post(f"{fake_github.api_url}/graphql", timeout=5, json={
            "query": "query{pullRequest{reviewThreads{nodes{isResolved}}}}", "variables": vars}).json()
        listed = requests.post(f"{fake_github.api_url}/graphql", timeout=5, json={
            "query": "query{pullRequest{reviewThreads{nodes{id isResolved comments{nodes{databaseId}}}}}}", "variables": vars}).json()
        return tuple(sum(not t["isResolved"] for t in out["data"]["repository"]["pullRequest"]["reviewThreads"]["nodes"])
                     for out in (counted, listed))

    def resolve(root: int) -> None:
        resp = requests.post(f"{fake_github.api_url}/graphql", timeout=5, json={
            "query": "mutation($id:ID!){resolveReviewThread(input:{threadId:$id}){thread{id isResolved}}}",
            "variables": {"id": fake_github.thread_id(root)}})
        assert resp.json()["data"]["resolveReviewThread"]["thread"]["isResolved"] is True

    assert unresolved() == (2, 2)
    resolve(roots[0])
    assert unresolved() == (1, 1)
    resolve(roots[0])
    assert unresolved() == (1, 1)
