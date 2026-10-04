"""@dude on a pull request, end to end through the deployed processes.

A conducted task with its pull request open on the fake GitHub. A person
who may wake dude comments "@dude why …?" there; GitHub's signed webhook
reaches dude, and the comment becomes a message in the task's Chat,
signed by its GitHub login, waking the conductor — no fixer, and who
decides is unchanged. The scripted conductor carries out the tool call
the comment names ("tool: reply_on_pull_request {...}"), as a real
conductor would decide to; the fake GitHub then has dude's reply on the
pull request, quoting the question, and read back it wakes nothing.
"""

from __future__ import annotations

import json

from playwright.sync_api import Page, expect

from fake_github import FakeGitHub
from helpers import ApiClient, sign_in, wait_until


def _task(client: ApiClient, task_id: str) -> dict:
    return client.get(f"/v1/tasks/{task_id}").json()


def _messages(client: ApiClient, task_id: str) -> list[dict]:
    return [e for e in client.events(taskId=task_id, limit=1000) if e["eventType"] == "chat.message"]


def test_a_mention_on_the_pull_request_is_answered_there(page: Page, web_url: str, client: ApiClient, org: dict,
                                                         forge_project: dict, fake_github: FakeGitHub, console_errors: list):
    models = {**forge_project["agentModels"], **client.on_models({"conductor": "fake/scripted"})}
    assert client.patch(f"/v1/projects/{forge_project['id']}", {"agentModels": models}).status_code == 200
    assert client.patch(f"/v1/projects/{forge_project['id']}/settings",
                        {"delivery": {"conductorWarmMinutes": 30}}).status_code == 200
    task = client.create_task(forge_project["id"], "Greet, asked about on GitHub")
    assert client.post(f"/v1/tasks/{task['id']}/deliver").status_code == 201
    pr = wait_until(lambda: client.get("/v1/pull-requests", params={"taskId": task["id"]}).json()["pullRequests"],
                    timeout=90, message="no pull request opened")[0]
    wait_until(lambda: _task(client, task["id"])["status"] == "review", timeout=60, message="the task never reached review")
    # Conducted: a person's first message in Chat hands it the decisions.
    resp = client.post(f"/v1/tasks/{task['id']}/chat", {"text": "I'll steer this from here."})
    assert resp.status_code == 201 and resp.json()["decider"] == "conductor", resp.text
    conductor = resp.json()["runId"]
    number = pr["number"]
    fixers = len([r for r in client.task_runs(task["id"]) if r["phase"] == "fix"])

    # The comment's id is the fake's next: the comment names it, so the
    # scripted conductor answers that comment.
    asked = fake_github._next_id + 1
    reply = json.dumps({"pr": str(number), "text": "Because the task asks for a greeting.",
                        "in_reply_to": f"issue-comment-{asked}"})
    assert fake_github.comment(number, f"@dude why greet()?\ntool: reply_on_pull_request {reply}", author="gus") == asked

    message = wait_until(lambda: next((m for m in _messages(client, task["id"]) if (m["payload"].get("github") or {}).get("login") == "gus"), None),
                         timeout=60, message="the mention never reached Chat")
    assert message["payload"]["text"].startswith("@dude why greet()?"), message["payload"]
    assert message["payload"]["github"]["feedbackId"] == f"issue-comment-{asked}"
    assert message["runId"] == conductor

    posted = wait_until(lambda: next((c for c in fake_github.pulls[number].comments if c["user"]["login"] == "dude-bot"), None),
                        timeout=90, message="the conductor never replied on the pull request")
    assert posted["body"].startswith("> @gus: @dude why greet()?\n\nBecause the task asks for a greeting."), posted["body"]
    recorded = wait_until(lambda: next((m for m in _messages(client, task["id"]) if m["payload"].get("by") == "conductor"), None),
                          timeout=30, message="the reply is not in Chat")
    assert recorded["payload"]["github"]["url"] == posted["html_url"]

    # Read back, the reply is dude's own; the mention started no fixer and
    # handed nothing over. (The one fixer before the pull request was the
    # review loop's.)
    wait_until(lambda: sum(1 for e in client.events(taskId=task["id"], limit=1000)
                           if e["eventType"] == "pull_request.commented" and e["payload"]["author"] == "dude-bot") == 1,
               timeout=30, message="the reply was never read back")
    assert len([r for r in client.task_runs(task["id"]) if r["phase"] == "fix"]) == fixers
    assert len([m for m in _messages(client, task["id"]) if (m["payload"].get("github") or {}).get("login")]) == 1
    assert _task(client, task["id"])["decider"] == "conductor"

    # Chat: gus's question, signed by his login and linked; the conductor's reply.
    sign_in(page, web_url, org["api_key"])
    page.goto(f"{web_url}#/task/{task['id']}")
    question = page.locator("[data-testid=human-turn][data-by=github]")
    expect(question).to_contain_text("gus (GitHub)", timeout=30_000)
    expect(question).to_contain_text("@dude why greet()?")
    answer = page.locator("[data-testid=human-turn][data-by=conductor]")
    expect(answer).to_contain_text("Because the task asks for a greeting.")
    expect(answer.get_by_test_id("github-source")).to_contain_text(f"Replied on greeter#{number}")
    assert console_errors == []
