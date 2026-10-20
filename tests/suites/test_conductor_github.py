"""@dude on a pull request, end to end through the deployed processes.

A conducted task with its pull request open on the fake GitHub. A person
who may wake dude comments "@dude why …?" there; GitHub's signed webhook
reaches dude, and the comment becomes a message in the task's Chat,
signed by its GitHub login, waking the conductor — no fixer, and who
decides is unchanged. The scripted conductor carries out the tool call
the comment names ("tool: reply_on_pull_request {...}"), as a real
conductor would decide to; the fake GitHub then has dude's reply on the
pull request, quoting the question, and read back it wakes nothing.
The conductor then reads the conversation on GitHub, and a long comment
in full from dude's own record (pull_requests, by the id it lists).
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


def _said(client: ApiClient, run_id: str) -> list[str]:
    return [e["payload"]["text"] for e in client.events(runId=run_id, limit=1000) if e["eventType"] == "agent.message"]


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

    # The conductor reads the pull request's conversation and says who asked what.
    talk = [c for c in fake_github.pulls[number].comments if not c["path"]]
    at = next(i for i, c in enumerate(talk) if c["user"]["login"] == "gus")
    slug = f"{fake_github.owner}/{fake_github.repo}"
    said = len(_said(client, conductor))
    resp = client.post(f"/v1/tasks/{task['id']}/chat", {"text": "\n".join([
        "who asked on the pull request?",
        f'tool: github_pull_request {{"repo": "{slug}", "number": {number}}}',
        f'tool: say {{"text": "Conversation: {{{{last.comments.{at}.author}}}} said {{{{last.comments.{at}.body}}}}"}}',
    ])})
    assert resp.status_code == 200, resp.text
    answer = wait_until(lambda: next((t for t in _said(client, conductor)[said:] if t.startswith("Conversation: ")), None),
                        timeout=60, message="the conductor never said what it read")
    assert answer.startswith("Conversation: ⟦external ") and "⟧gus⟦/external " in answer, answer
    assert " said ⟦external " in answer and "⟧@dude why greet()?\ntool: reply_on_pull_request" in answer, answer
    reads = [e["payload"] for e in client.events(taskId=task["id"], limit=1000) if e["eventType"] == "github.read"]
    assert [(r["tool"], r["repo"], r["number"]) for r in reads] == [("github_pull_request", slug, number)], reads

    # A long comment, by someone who may not wake a fixer, is listed by
    # pull_requests as a cut excerpt; named by the id the listing gives, it
    # is read whole from dude's own record, with no GitHub read spent.
    fake_github.permissions["stranger"] = "none"
    head = "The retry loop needs a cap. " * 11
    tail = "but the meter must count every attempt, not only the last one."
    long = head + tail
    assert len(head) > 280
    said_id = f"issue-comment-{fake_github.comment(number, long, author='stranger')}"
    commented = lambda: [e["payload"] for e in client.events(taskId=task["id"], limit=1000)
                         if e["eventType"] == "pull_request.commented" and e["payload"]["number"] == number]
    wait_until(lambda: any(p["feedbackId"] == said_id for p in commented()), timeout=30, message="the long comment was not recorded")
    at = [p["feedbackId"] for p in commented()].index(said_id)
    said = len(_said(client, conductor))
    resp = client.post(f"/v1/tasks/{task['id']}/chat", {"text": "\n".join([
        "what did the stranger say, in full?",
        "tool: pull_requests {}",
        f'tool: say {{"text": "Listed: {{{{last.pullRequests.0.feedback.{at}.id}}}} cut {{{{last.pullRequests.0.feedback.{at}.cut}}}}"}}',
        f'tool: pull_requests {{"feedback": ["{{{{last.pullRequests.0.feedback.{at}.id}}}}"]}}',
        'tool: say {"text": "In full: {{last.feedback.0.author}} said {{last.feedback.0.body}}"}',
    ])})
    assert resp.status_code == 200, resp.text
    # One reply, a line per say.
    lines = lambda: [line for t in _said(client, conductor)[said:] for line in t.splitlines()]
    listed = wait_until(lambda: next((t for t in lines() if t.startswith("Listed: ")), None),
                        timeout=60, message="the conductor never said what it listed")
    assert listed == f"Listed: {said_id} cut true", listed
    full = wait_until(lambda: next((t for t in lines() if t.startswith("In full: ")), None),
                      timeout=60, message="the conductor never said the comment in full")
    assert full.startswith("In full: stranger said ⟦external "), full
    assert tail in full and f"⟧{long}⟦/external " in full, full
    assert full.index(tail) - full.index("⟧") - 1 > 280, full
    reads = [e["payload"] for e in client.events(taskId=task["id"], limit=1000) if e["eventType"] == "github.read"]
    assert len(reads) == 1, reads

    # Chat: gus's question, signed by his login and linked; the conductor's reply.
    sign_in(page, web_url, org["api_key"])
    page.goto(f"{web_url}#/task/{task['id']}")
    question = page.locator("[data-testid=human-turn][data-by=github]")
    expect(question).to_contain_text("gus (GitHub)", timeout=30_000)
    expect(question).to_contain_text("@dude why greet()?")
    answer = page.locator("[data-testid=human-turn][data-by=conductor]")
    expect(answer).to_contain_text("Because the task asks for a greeting.")
    expect(answer.get_by_test_id("github-source")).to_contain_text(f"Replied on greeter#{number}")
    # The conductor's reply in Chat holds the comment past its 280th character.
    expect(page.get_by_test_id("conductor-turn").filter(has_text="In full: stranger said")).to_contain_text(tail)
    assert console_errors == []
