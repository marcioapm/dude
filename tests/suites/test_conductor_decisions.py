"""The conductor's decisions, end to end through the deployed processes.

The scripted conductor (fakeagent) carries out the tool calls a message
names — a line "tool: NAME {json}" — as a real conductor would decide to
from what the person said: so the test talks to it in Chat, and it
decides. Between its decisions the delivery does the mechanics and wakes
it with a note.

Talk it through: a task not started gets a delivery its conductor decides,
waiting on the start; the person and the conductor agree a criterion,
written into the task; the conductor starts the implementer, then a
review, triages, and before the pull request asks the gate's question;
the person answers Open, and the pull request opens. Taking over: the
first message in a delivered task's Chat hands its next decision to the
conductor, which the pull request's feedback then waits on.
"""

from __future__ import annotations

import json

from fake_github import FakeGitHub
from helpers import ApiClient, query, wait_until


def _conducting(client: ApiClient, forge_project: dict) -> dict:
    models = {**forge_project["agentModels"], **client.on_models({"conductor": "fake/scripted"})}
    resp = client.patch(f"/v1/projects/{forge_project['id']}", {"agentModels": models})
    assert resp.status_code == 200, resp.text
    # Warm throughout: each decision reaches a live conductor.
    assert client.patch(f"/v1/projects/{forge_project['id']}/settings",
                        {"delivery": {"conductorWarmMinutes": 30}}).status_code == 200
    return resp.json()


def _tool(name: str, args: dict | None = None) -> str:
    return f"tool: {name} {json.dumps(args or {})}"


def _say(client: ApiClient, task_id: str, *lines: str) -> dict:
    resp = client.post(f"/v1/tasks/{task_id}/chat", {"text": "\n".join(lines)})
    assert resp.status_code in (200, 201), resp.text
    return resp.json()


def _task(client: ApiClient, task_id: str) -> dict:
    return client.get(f"/v1/tasks/{task_id}").json()


def _waiting_on(client: ApiClient, task_id: str, point: str, message: str) -> None:
    wait_until(lambda: (_task(client, task_id)["awaitingDecision"] or {}).get("point") == point,
               timeout=90, message=message)


def _phases(client: ApiClient, task_id: str, phase: str) -> list[dict]:
    return [r for r in client.task_runs(task_id) if r["phase"] == phase]


def _woken(client: ApiClient, task_id: str) -> list[str]:
    return [e["payload"]["text"] for e in client.events(taskId=task_id, limit=1000) if e["eventType"] == "conductor.woken"]


def _said(client: ApiClient, run_id: str) -> list[str]:
    return [e["payload"]["text"] for e in client.events(runId=run_id, limit=1000) if e["eventType"] == "agent.message"]


def test_talk_it_through_to_a_pull_request(client: ApiClient, forge_project: dict, fake_github: FakeGitHub, owner_dsn: str):
    project = _conducting(client, forge_project)
    task = client.create_task(project["id"], "Greet in Portuguese")

    # Talk it through: the conductor, a delivery it decides, and nothing built.
    resp = client.post(f"/v1/tasks/{task['id']}/talk")
    assert resp.status_code == 201, resp.text
    conductor = resp.json()["runId"]
    assert resp.json()["decider"] == "conductor"
    _waiting_on(client, task["id"], "start", "the delivery never waited on the conductor to start")
    assert _task(client, task["id"])["decider"] == "conductor"
    assert [r for r in client.task_runs(task["id"]) if r["phase"]] == []

    # Planned: the agreed criterion is written into the task, then the implementer starts.
    _say(client, task["id"], "Agreed: it says olá.",
         _tool("update_task", {"acceptanceCriteria": ["it says olá"]}),
         _tool("start_phase", {"phase": "implement", "note": "one file"}))
    wait_until(lambda: _phases(client, task["id"], "implement"), timeout=60, message="the implementer never started")
    assert _task(client, task["id"])["acceptanceCriteria"] == ["it says olá"]
    implementer = _phases(client, task["id"], "implement")[0]
    assert implementer["conductorRunId"] == conductor

    # After implement: dude wakes the conductor with a bounded note; it asks for a review.
    _waiting_on(client, task["id"], "after_implement", "the delivery never came back after the implementer")
    wait_until(lambda: any("after implement" in n for n in _woken(client, task["id"])), timeout=60,
               message="the conductor was never woken after implement")
    note = next(n for n in _woken(client, task["id"]) if "after implement" in n)
    assert implementer["id"] in note and "FACTORY.md" not in note, note
    _say(client, task["id"], "Review it.", _tool("decide", {"action": "next"}))

    # A review round: the conductor fixes the finding, and has it reviewed again.
    _waiting_on(client, task["id"], "after_review", "the review round never came back to the conductor")
    _say(client, task["id"], "Fix it.", _tool("start_phase", {"phase": "fix"}))
    _waiting_on(client, task["id"], "after_fix", "the fix never came back to the conductor")
    _say(client, task["id"], "Again.", _tool("start_phase", {"phase": "review", "categories": ["correctness"]}))
    wait_until(lambda: len(_phases(client, task["id"], "review")) == 2, timeout=60, message="no second review")
    _waiting_on(client, task["id"], "after_review", "the clean round never came back to the conductor")
    _say(client, task["id"], "Clean; go on.", _tool("decide", {"action": "next"}))

    # Before the pull request: refused without the person's word; the gate's question; Open.
    _waiting_on(client, task["id"], "before_pull_request", "the delivery never waited before the pull request")
    _say(client, task["id"], "Open it.", _tool("decide", {"action": "open_pull_request"}))
    # Refused (the fake says the tool answered 422): the delivery still waits, and nothing opened.
    wait_until(lambda: any("dude decide answered 422" in t for t in _said(client, conductor)), timeout=60,
               message="the refused open never came back")
    assert (_task(client, task["id"])["awaitingDecision"] or {}).get("point") == "before_pull_request"
    assert client.get("/v1/pull-requests", params={"taskId": task["id"]}).json()["pullRequests"] == []
    _say(client, task["id"], "Ask me.", _tool("decide", {"action": "ask_person", "note": "Reviewed clean."}))
    question = wait_until(
        lambda: query(owner_dsn, "SELECT id, options FROM questions WHERE task_id = %s AND pr_gate_heads IS NOT NULL", (task["id"],)),
        timeout=60, message="the gate's question was never asked")[0]
    assert question["options"] == ["Open", "Draft", "Show me the diff", "Another round"]
    assert _say(client, task["id"], "Open")["questionId"] == question["id"]
    _say(client, task["id"], "Opening.", _tool("decide", {"action": "open_pull_request"}))
    prs = wait_until(lambda: client.get("/v1/pull-requests", params={"taskId": task["id"]}).json()["pullRequests"],
                     timeout=90, message="the pull request never opened")
    assert len(prs) == 1
    wait_until(lambda: _task(client, task["id"])["status"] == "review", timeout=60, message="the task never went to review")
    # Every phase Run was the conductor's, and each decision was recorded.
    assert all(r["conductorRunId"] == conductor for r in client.task_runs(task["id"]) if r["phase"])
    decided = [e["payload"]["action"] for e in client.events(taskId=task["id"], limit=1000) if e["eventType"] == "conductor.decided"]
    assert decided == ["start_phase", "next", "start_phase", "start_phase", "next", "open_pull_request"], decided


def test_taking_over_a_delivered_task(client: ApiClient, forge_project: dict, fake_github: FakeGitHub):
    project = _conducting(client, forge_project)
    task = client.create_task(project["id"], "Respond to review, with the conductor")
    assert client.post(f"/v1/tasks/{task['id']}/deliver").status_code == 201
    pr = wait_until(lambda: client.get("/v1/pull-requests", params={"taskId": task["id"]}).json()["pullRequests"],
                    timeout=90, message="the delivery never opened a pull request")[0]
    wait_until(lambda: _task(client, task["id"])["status"] == "review", timeout=60, message="never in review")
    assert _task(client, task["id"])["decider"] == "policy"

    # The first message takes it over.
    resp = _say(client, task["id"], "I'll steer from here.")
    assert resp["decider"] == "conductor"
    assert _task(client, task["id"])["decider"] == "conductor"
    changes = [e["payload"] for e in client.events(taskId=task["id"], limit=1000) if e["eventType"] == "task.decider_changed"]
    assert changes == [{"from": "policy", "to": "conductor", "why": "a person wrote in Chat"}]

    # Feedback now waits on the conductor: no fixer until it decides.
    fixes = len(_phases(client, task["id"], "fix"))
    fake_github.comment(pr["number"], "Please also note the date in FIXED.md.", path="FIXED.md")
    _waiting_on(client, task["id"], "pull_request_feedback", "the feedback never waited on the conductor")
    assert len(_phases(client, task["id"], "fix")) == fixes
    wait_until(lambda: any("pull request feedback" in n for n in _woken(client, task["id"])), timeout=60,
               message="the conductor was never woken for the feedback")
    _say(client, task["id"], "Fix it.", _tool("start_phase", {"phase": "fix"}))
    wait_until(lambda: len(_phases(client, task["id"], "fix")) == fixes + 1, timeout=60, message="the conductor's fix never ran")

    # Let Deliver finish it: the next feedback is the policy's again.
    wait_until(lambda: _task(client, task["id"])["status"] == "review", timeout=60, message="not back in review")
    assert client.post(f"/v1/tasks/{task['id']}/decider", {"decider": "policy"}).status_code == 200
    assert _task(client, task["id"])["decider"] == "policy"
    fake_github.comment(pr["number"], "And the time, please.", path="FIXED.md")
    wait_until(lambda: len(_phases(client, task["id"], "fix")) == fixes + 2, timeout=60,
               message="after the hand-back, Deliver never fixed the feedback")
