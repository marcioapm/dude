"""The conductor sees: another Run's question, and the limits in force.

Through the deployed processes, against the stand-in GitHub. A person's
pull request comment wakes a fixer, and the scripted fixer carries out the
ask_person the comment names, so it waits on a question. A person hands
the task to the scripted conductor, which reads the fixer's Chat and
question with run_chat and the task's limits with limits, and says what it
read: the question it would relay, and both fix-round bounds with where
each is set.
"""

from __future__ import annotations

import json

from fake_github import FakeGitHub
from helpers import ApiClient, wait_until


def _call(name: str, **args) -> str:
    return f"tool: {name} {json.dumps(args)}"


def _said(client: ApiClient, run_id: str) -> list[str]:
    return [e["payload"]["text"] for e in client.events(runId=run_id, limit=1000) if e["eventType"] == "agent.message"]


def test_the_conductor_relays_a_fixers_question_and_reads_the_limits(client: ApiClient, forge_project: dict,
                                                                     fake_github: FakeGitHub):
    models = {**forge_project["agentModels"], **client.on_models({"conductor": "fake/scripted"})}
    assert client.patch(f"/v1/projects/{forge_project['id']}", {"agentModels": models}).status_code == 200
    assert client.patch(f"/v1/projects/{forge_project['id']}/settings",
                        {"delivery": {"conductorWarmMinutes": 30, "maxPrFixIterations": 5}}).status_code == 200
    assert client.patch("/v1/forge/settings", {"fixRoundsPerPr": 8}).status_code == 200
    task = client.create_task(forge_project["id"], "Greet, with a fixer who asks")
    assert client.post(f"/v1/tasks/{task['id']}/deliver").status_code == 201
    pr = wait_until(lambda: client.get("/v1/pull-requests", params={"taskId": task["id"]}).json()["pullRequests"],
                    timeout=90, message="no pull request opened")[0]
    wait_until(lambda: client.get(f"/v1/tasks/{task['id']}").json()["status"] == "review", timeout=60,
               message="the task never reached review")

    # The fixer the comment wakes asks what the comment names, and waits.
    # The fixer's prompt puts its own words after the comment's last line,
    # so the call is not the comment's last.
    fake_github.comment(pr["number"], "\n".join([
        "Rename greet() to hello().",
        _call("ask_person", question="Keep greet() as an alias of hello()?", choices=["Keep it", "Drop it"]),
        "Thanks.",
    ]), author="ana")
    fixer = wait_until(lambda: next((e["runId"] for e in client.events(taskId=task["id"], limit=1000)
                                     if e["eventType"] == "question.asked" and e.get("runId")
                                     and "alias" in e["payload"].get("prompt", "")), None),
                       timeout=120, message="the fixer never asked")

    resp = client.post(f"/v1/tasks/{task['id']}/chat", {"text": "I'll steer this from here."})
    assert resp.status_code == 201 and resp.json()["decider"] == "conductor", resp.text
    conductor = resp.json()["runId"]
    said = len(_said(client, conductor))
    resp = client.post(f"/v1/tasks/{task['id']}/chat", {"text": "\n".join([
        "Is anyone waiting on us? And how many fix rounds are left?",
        _call("run_chat", run=fixer),
        _call("say", text="Waiting: {{last.openQuestion.prompt}} for {{last.openQuestion.for}}"),
        _call("limits"),
        _call("say", text="Rounds: {{last.fixRounds.perReviewRound.limit}} per round ({{last.fixRounds.perReviewRound.source}}), "
                          "{{last.fixRounds.perPullRequest.limit}} per pull request ({{last.fixRounds.perPullRequest.source}}), "
                          "binding {{last.fixRounds.perPullRequest.pullRequests.0.binding}}"),
    ])})
    assert resp.status_code == 200, resp.text
    said_now = wait_until(lambda: next((t for t in _said(client, conductor)[said:] if "Rounds: " in t), None),
                          timeout=90, message="the conductor never said what it read")
    lines = said_now.split("\n")
    assert "Waiting: Keep greet() as an alias of hello()? for the task's people" in lines, lines
    rounds = next(t for t in lines if t.startswith("Rounds: "))
    assert rounds == ("Rounds: 5 per round (delivery policy), 8 per pull request (organisation GitHub settings), "
                      "binding delivery policy"), rounds

    calls = [e["payload"]["tool"] for e in client.events(runId=conductor, limit=1000) if e["eventType"] == "agent.tool.dude"]
    assert "run_chat" in calls and "limits" in calls, calls
