"""The conductor acts: it asks a person to raise a limit, and has a commit verified.

Through the deployed processes, against the stand-in GitHub. The scripted
conductor carries out the "tool:" lines of a person's Chat message, as a
real conductor would decide to.

In the first, the delivery policy allows no pull request fix rounds; the
conductor's fix is refused at that bound, it asks with request_limit, the
organisation's admin approves through the question's answer route, and
the fix it starts next runs. In the second, it has the pull request's head
verified with two commands, and is woken with the result.
"""

from __future__ import annotations

import json

from fake_github import FakeGitHub
from helpers import ApiClient, query, wait_until


def _call(name: str, **args) -> str:
    return f"tool: {name} {json.dumps(args)}"


def _said(client: ApiClient, run_id: str) -> list[str]:
    return [e["payload"]["text"] for e in client.events(runId=run_id, limit=1000) if e["eventType"] == "agent.message"]


def _conducted_pr(client: ApiClient, forge_project: dict, delivery: dict, title: str) -> tuple[dict, dict, str]:
    """A task delivered to an open pull request, then handed to the scripted conductor."""
    models = {**forge_project["agentModels"], **client.on_models({"conductor": "fake/scripted", "investigator": "fake/scripted"})}
    assert client.patch(f"/v1/projects/{forge_project['id']}", {"agentModels": models}).status_code == 200
    assert client.patch(f"/v1/projects/{forge_project['id']}/settings",
                        {"delivery": {"conductorWarmMinutes": 30, **delivery}}).status_code == 200
    task = client.create_task(forge_project["id"], title)
    assert client.post(f"/v1/tasks/{task['id']}/deliver").status_code == 201
    pr = wait_until(lambda: client.get("/v1/pull-requests", params={"taskId": task["id"]}).json()["pullRequests"],
                    timeout=90, message="no pull request opened")[0]
    wait_until(lambda: client.get(f"/v1/tasks/{task['id']}").json()["status"] == "review", timeout=60,
               message="the task never reached review")
    resp = client.post(f"/v1/tasks/{task['id']}/chat", {"text": "I'll steer this from here."})
    assert resp.status_code == 201 and resp.json()["decider"] == "conductor", resp.text
    conductor = resp.json()["runId"]
    wait_until(lambda: _said(client, conductor), timeout=60, message="the conductor never answered")
    return task, pr, conductor


def test_the_conductor_asks_to_raise_the_fix_rounds_and_the_next_fix_runs(client: ApiClient, forge_project: dict,
                                                                         fake_github: FakeGitHub, owner_dsn: str):
    task, _, conductor = _conducted_pr(client, forge_project, {"maxPrFixIterations": 0}, "Greet, past the fix bound")
    resp = client.post(f"/v1/tasks/{task['id']}/chat", {"text": "\n".join([
        "Rename greet() to hello(), please.",
        _call("start_phase", phase="fix", note="Rename greet() to hello()."),
        _call("request_limit", limit="perReviewRound", to=1, reason="The person asked for one rename after the review."),
    ])})
    assert resp.status_code == 200, resp.text

    question = wait_until(lambda: next(iter(query(owner_dsn, "SELECT id, items FROM questions WHERE run_id = %s AND status = 'open'",
                                                  (conductor,))), None),
                          timeout=90, message="the conductor never asked to raise the limit")
    labels = [c["label"] for c in question["items"][0]["choices"]]
    assert labels == ["Allow 1 more fix round per review round for this task", "No"], labels
    assert query(owner_dsn, "SELECT count(*) AS n FROM events WHERE task_id = %s AND event_type = 'agent.tool.dude' "
                 "AND payload->>'tool' = 'start_phase'", (task["id"],))[0]["n"] == 0, "the refused fix was recorded as a call"
    fixes = len([r for r in client.task_runs(task["id"]) if r.get("phase") == "fix"])

    # The organisation's admin (the suite's key) approves through the question's own route.
    resp = client.post(f"/v1/questions/{question['id']}/answer", {"answers": [{"choices": [0]}]})
    assert resp.status_code == 200, resp.text
    granted = query(owner_dsn, "SELECT payload FROM events WHERE task_id = %s AND event_type = 'limit.granted'", (task["id"],))
    assert len(granted) == 1 and granted[0]["payload"]["extra"] == 1, granted

    said = len(_said(client, conductor))
    resp = client.post(f"/v1/tasks/{task['id']}/chat", {"text": "\n".join([
        "Go ahead with the rename.",
        _call("limits"),
        _call("say", text="Round: {{last.fixRounds.perReviewRound.line}}"),
        _call("start_phase", phase="fix", note="Rename greet() to hello()."),
    ])})
    assert resp.status_code == 200, resp.text
    wait_until(lambda: len([r for r in client.task_runs(task["id"]) if r.get("phase") == "fix"]) > fixes, timeout=90,
               message="the granted fix round never started")
    line = wait_until(lambda: next((t for t in _said(client, conductor)[said:] if "Round: " in t), None), timeout=60,
                      message="the conductor never said the limit")
    assert "Round: delivery policy 0 + 1 granted by " in line and " for this task = 1" in line, line


def test_the_conductor_has_the_pull_requests_head_verified(client: ApiClient, forge_project: dict, fake_github: FakeGitHub,
                                                           owner_dsn: str):
    task, pr, conductor = _conducted_pr(client, forge_project, {}, "Greet, verified")
    head = fake_github.branch_sha(fake_github.pulls[pr["number"]].head)
    assert head, "the pull request's branch has no head"
    said = len(_said(client, conductor))
    resp = client.post(f"/v1/tasks/{task['id']}/chat", {"text": "\n".join([
        "Did anyone run the checks on this?",
        _call("verify", commands=["test -f FACTORY.md", "echo broken >&2; exit 4"], note="the implementer did not say"),
    ])})
    assert resp.status_code == 200, resp.text

    child = wait_until(lambda: next(iter(query(owner_dsn, "SELECT id, child_capabilities, base_refs FROM runs "
                                                "WHERE parent_run_id = %s AND source_kind = 'task_conductor'", (conductor,))), None),
                       timeout=60, message="no verifier started")
    assert child["child_capabilities"]["publishCode"] is False, child
    assert child["base_refs"]["greeter"] == head, child

    woke = wait_until(lambda: next((t for t in _said(client, conductor)[said:] if "Your verification " + child["id"] in t), None),
                      timeout=120, message="the verifier's result never woke the conductor")
    assert "failed: 1 of 2 commands exited 0." in woke, woke
    report = query(owner_dsn, "SELECT verify FROM child_results WHERE child_run_id = %s", (child["id"],))[0]["verify"]
    assert report["status"] == "failed" and [r["exit"] for r in report["results"]] == [0, 4], report
    assert "broken" in report["results"][1]["tail"], report
    assert query(owner_dsn, "SELECT push_branch FROM runs WHERE id = %s", (child["id"],))[0]["push_branch"] is None
