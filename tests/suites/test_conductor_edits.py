"""The conductor's edits, end to end through the deployed processes.

A conducted task (Talk it through, then the implementer). The scripted
conductor carries out what a person's message names, as a real conductor
would decide to: "tool: NAME {json}" calls dude's tool, and its own work in
its checkout — git, write, commit — runs there. Started before the task
branch existed, its checkout is not on it, which a wake says; it switches
to lux/<branch>, edits one file, commits and publishes. The task branch
moves to its commit, and Chat shows the commit as the conductor's. A
second publish, past the limit, is refused with "delegate this". The pull
request gate then refuses the head until a review Run has finished on it.
"""

from __future__ import annotations

import json

from playwright.sync_api import Page, expect

from fake_github import FakeGitHub
from helpers import ApiClient, query, sign_in, wait_until


def _said(client: ApiClient, run_id: str) -> list[str]:
    return [e["payload"]["text"] for e in client.events(runId=run_id, limit=1000) if e["eventType"] == "agent.message"]


def _tool(name: str, args: dict | None = None) -> str:
    return f"tool: {name} {json.dumps(args or {})}"


def _say(client: ApiClient, task_id: str, *lines: str) -> dict:
    resp = client.post(f"/v1/tasks/{task_id}/chat", {"text": "\n".join(lines)})
    assert resp.status_code in (200, 201), resp.text
    return resp.json()


def _point(client: ApiClient, task_id: str) -> str | None:
    return (client.get(f"/v1/tasks/{task_id}").json()["awaitingDecision"] or {}).get("point")


def _events(client: ApiClient, task_id: str, kind: str) -> list[dict]:
    return [e for e in client.events(taskId=task_id, limit=1000) if e["eventType"] == kind]


def _woken_with(client: ApiClient, task_id: str, words: str) -> bool:
    return any(words in e["payload"].get("text", "") for e in _events(client, task_id, "conductor.woken"))


def test_the_conductor_edits_publishes_and_is_reviewed(page: Page, web_url: str, client: ApiClient, org: dict, owner_dsn: str,
                                                      forge_project: dict, fake_github: FakeGitHub, console_errors: list):
    models = {**forge_project["agentModels"], **client.on_models({"conductor": "fake/scripted"})}
    assert client.patch(f"/v1/projects/{forge_project['id']}", {"agentModels": models}).status_code == 200
    assert client.patch(f"/v1/projects/{forge_project['id']}/settings",
                        {"delivery": {"conductorWarmMinutes": 30}}).status_code == 200
    task = client.create_task(forge_project["id"], "Greet, edited by the conductor")
    resp = client.post(f"/v1/tasks/{task['id']}/talk")
    assert resp.status_code == 201, resp.text
    conductor = resp.json()["runId"]
    wait_until(lambda: _point(client, task["id"]) == "start", timeout=90, message="never waited on the start")
    _say(client, task["id"], "Go.", _tool("start_phase", {"phase": "implement"}))
    wait_until(lambda: _point(client, task["id"]) == "after_implement", timeout=120, message="the implementer never finished")
    branch = f"dude/{task['id']}/attempt-1"
    head = wait_until(lambda: fake_github.branch_sha(branch), timeout=30, message="no task branch")

    # On the task branch, one file edited, committed, published.
    _say(client, task["id"], "Fix the README's greeting yourself.",
         _tool("git", {"args": ["switch", "-q", "-C", branch, f"lux/{branch}"]}),
         _tool("write", {"path": "README.md", "content": "# target\n\nOlá.\n"}),
         _tool("commit", {"message": "Greet in Portuguese"}),
         _tool("publish", {"message": "the README's greeting"}))
    moved = wait_until(lambda: (sha := fake_github.branch_sha(branch)) != head and sha, timeout=90,
                       message="the task branch never moved to the conductor's commit")
    assert fake_github.branch_log(branch)[0].startswith("Greet in Portuguese")
    commit = wait_until(lambda: [e for e in _events(client, task["id"], "git.commit_created")
                                 if e["payload"].get("by") == "conductor"], timeout=30, message="no git.commit_created")
    assert commit[0]["payload"]["headSha"] == moved and commit[0]["payload"]["changedPaths"] == ["README.md"]
    wait_until(lambda: _woken_with(client, task["id"], "is on the task branch"), timeout=90,
               message="the conductor was never told it published")

    # Past the limit: refused, delegate this, nothing moved.
    big = "".join(f"line {i}\n" for i in range(80))
    _say(client, task["id"], "Now the big one.",
         _tool("write", {"path": "BIG.md", "content": big}),
         _tool("commit", {"message": "A big change"}),
         _tool("publish", {}))
    wait_until(lambda: _woken_with(client, task["id"], "delegate this"), timeout=90,
               message="the publish past the limit was never refused")
    assert fake_github.branch_sha(branch) == moved

    # Straight to the gate: refused until a review Run finishes on the head.
    _say(client, task["id"], "Simplify, then ask me.", _tool("start_phase", {"phase": "simplify"}))
    wait_until(lambda: _point(client, task["id"]) == "before_pull_request", timeout=120, message="never reached the gate")
    # The simplifier's head: the conductor's commit is under it, reviewed or not; publish again on top.
    _say(client, task["id"], "One more nit.",
         _tool("git", {"args": ["reset", "-q", "--hard", f"lux/{branch}"]}),
         _tool("write", {"path": "NIT.md", "content": "nit\n"}),
         _tool("commit", {"message": "A nit"}),
         _tool("publish", {}))
    wait_until(lambda: fake_github.branch_log(branch)[0].startswith("A nit"), timeout=90, message="the nit was never published")
    _say(client, task["id"], "Shall we open it?", _tool("decide", {"action": "ask_person"}))
    wait_until(lambda: any("dude decide answered 422" in t for t in _said(client, conductor)), timeout=60,
               message="the gate's question was not refused")
    assert not query(owner_dsn, "SELECT id FROM questions WHERE task_id = %s AND pr_gate_heads IS NOT NULL", (task["id"],))

    # A review of the head; its finding dismissed; then the gate asks.
    _say(client, task["id"], "Review it.", _tool("start_phase", {"phase": "review", "categories": ["correctness"]}))
    wait_until(lambda: _point(client, task["id"]) == "after_review", timeout=120, message="the review never finished")
    assert any(r["phase"] == "review" and r["status"] == "completed" for r in client.task_runs(task["id"]))
    for f in query(owner_dsn, "SELECT id FROM review_findings WHERE task_id = %s AND status = 'open'", (task["id"],)):
        _say(client, task["id"], "Fine as it is.", _tool("dismiss_finding", {"id": f["id"], "reason": "fine as it is"}))
    wait_until(lambda: not query(owner_dsn, "SELECT id FROM review_findings WHERE task_id = %s AND status = 'open'", (task["id"],)),
               timeout=60, message="the finding was never dismissed")
    _say(client, task["id"], "On.", _tool("decide", {"action": "next"}))
    wait_until(lambda: _point(client, task["id"]) == "before_pull_request", timeout=120, message="never back at the gate")
    _say(client, task["id"], "Shall we open it now?", _tool("decide", {"action": "ask_person"}))
    wait_until(lambda: query(owner_dsn, "SELECT id FROM questions WHERE task_id = %s AND pr_gate_heads IS NOT NULL", (task["id"],)),
               timeout=60, message="the gate never asked after the review")

    sign_in(page, web_url, org["api_key"])
    page.goto(f"{web_url}#/task/{task['id']}")
    line = page.locator(f"[data-testid=chat-commit][data-sha='{moved[:7]}']")
    expect(line).to_contain_text("Conductor", timeout=30_000)
    expect(line).to_contain_text("README.md")
    assert console_errors == []
