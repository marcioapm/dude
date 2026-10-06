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


def _woken_texts(client: ApiClient, task_id: str, words: str) -> list[str]:
    return [t for e in _events(client, task_id, "conductor.woken") if words in (t := e["payload"].get("text", ""))]


def test_a_conductor_stopped_mid_rebase_is_resumed_into_it_and_publishes_once_it_is_finished(
        client: ApiClient, owner_dsn: str, forge_project: dict, fake_github: FakeGitHub):
    """The conductor's checkout stopped mid-rebase, through a park and the
    resume a Chat message makes: lux keeps the rebase, dude tells the
    conductor how to finish it, and publish is refused until it has."""
    models = {**forge_project["agentModels"], **client.on_models({"conductor": "fake/scripted"})}
    assert client.patch(f"/v1/projects/{forge_project['id']}", {"agentModels": models}).status_code == 200
    settings = f"/v1/projects/{forge_project['id']}/settings"
    assert client.patch(settings, {"delivery": {"conductorWarmMinutes": 30}}).status_code == 200
    task = client.create_task(forge_project["id"], "Greet, rebased by the conductor")
    resp = client.post(f"/v1/tasks/{task['id']}/talk")
    assert resp.status_code == 201, resp.text
    conductor = resp.json()["runId"]
    wait_until(lambda: _point(client, task["id"]) == "start", timeout=90, message="never waited on the start")
    _say(client, task["id"], "Go.", _tool("start_phase", {"phase": "implement"}))
    wait_until(lambda: _point(client, task["id"]) == "after_implement", timeout=120, message="the implementer never finished")
    branch = f"dude/{task['id']}/attempt-1"
    head = wait_until(lambda: fake_github.branch_sha(branch), timeout=30, message="no task branch")
    rebase = "a rebase is in progress in your checkout"

    # Its own FACTORY.md from before the implementer's, rebased onto the
    # task branch: both add the file, so the rebase stops on the conflict.
    # Published so, it is refused, and nothing moves.
    _say(client, task["id"], "Rebase your version onto the task branch, and publish.",
         _tool("git", {"args": ["switch", "-q", "-C", "mine", f"lux/{branch}~1"]}),
         _tool("write", {"path": "FACTORY.md", "content": "the conductor's\n"}),
         _tool("commit", {"message": "The conductor's FACTORY.md"}),
         _tool("git", {"args": ["rebase", f"lux/{branch}"]}),
         _tool("publish", {"message": "mid-rebase"}))
    refused = wait_until(lambda: _woken_texts(client, task["id"], "was refused, nothing moved"), timeout=90,
                         message="the publish mid-rebase was never refused")
    assert f"greeter: {rebase}: finish or abort it, then publish." in refused[0], refused
    publishes = query(owner_dsn, "SELECT status, error FROM conductor_publishes WHERE task_id = %s", (task["id"],))
    assert [p["status"] for p in publishes] == ["refused"], publishes
    assert fake_github.branch_sha(branch) == head

    # Stopped: parked past its warm period (the suite's seconds). The task
    # branch moves on meanwhile, so the rebase is behind it.
    told = len(_woken_texts(client, task["id"], rebase))
    assert client.patch(settings, {"delivery": {"conductorWarmMinutes": None}}).status_code == 200
    wait_until(lambda: (r := client.get_run(conductor))["status"] == "paused" and r["dudePause"] == "conductor",
               timeout=60, message="the conductor was never parked")
    assert client.patch(settings, {"delivery": {"conductorWarmMinutes": 30}}).status_code == 200
    moved_on = fake_github.commit(branch, "A person's commit")

    # A Chat message resumes the same conductor, and the resume's sync
    # tells it the rebase is still there, behind, and how to finish it.
    resp = client.post(f"/v1/tasks/{task['id']}/chat", {"text": "Where were we?"})
    assert resp.status_code == 200 and resp.json()["runId"] == conductor, resp.text
    after = wait_until(lambda: _woken_texts(client, task["id"], rebase)[told:], timeout=90,
                       message="the resumed conductor was never told of its rebase")
    want = (f"greeter: {rebase} (1 behind the task branch): resolve and `git rebase --continue`, "
            f"or `git rebase --abort`; then `git merge lux/{branch}`.")
    assert any(want in t for t in after), after
    assert all("switch" not in t for t in after), after
    assert [r["cause"] for r in query(owner_dsn, "SELECT cause FROM run_resumes WHERE run_id = %s", (conductor,))] == ["conductor"]

    # Still mid-rebase: refused again.
    _say(client, task["id"], "Publish as it is.", _tool("publish", {}))
    wait_until(lambda: len(_woken_texts(client, task["id"], "was refused, nothing moved")) == 2, timeout=90,
               message="the publish after the resume was never refused")
    assert fake_github.branch_sha(branch) == moved_on

    # Resolved, continued, the task branch merged in: published.
    _say(client, task["id"], "Resolve it, finish the rebase, take the branch in and publish.",
         _tool("write", {"path": "FACTORY.md", "content": "both\n"}),
         _tool("git", {"args": ["add", "FACTORY.md"]}),
         _tool("git", {"args": ["-c", "core.editor=true", "rebase", "--continue"]}),
         _tool("git", {"args": ["merge", "-q", "--no-edit", f"lux/{branch}"]}),
         _tool("publish", {"message": "the rebased FACTORY.md"}))
    wait_until(lambda: _woken_with(client, task["id"], "is on the task branch"), timeout=90,
               message="the finished rebase was never published")
    assert fake_github.branch_sha(branch) not in (head, moved_on)
    assert "The conductor's FACTORY.md" in fake_github.branch_log(branch)
    assert fake_github.file_at(branch, "FACTORY.md") == "both\n"
