"""A Run that makes no progress, end to end through the deployed processes.

fake/stall's first reviewer of a task hangs inside an open `task` call that
never settles, as on run 2; every later reviewer reviews. The window is
hours long, so the test back-dates when the call opened; the orchestrator's
own sweep then finds it. Under a conductor: the conductor is woken once
with the facts and restarts the reviewer with restart_run; the round goes
on with the new Run. A plain delivery: its owner sees the banner, restarts
it, and the delivery goes on.
"""

from __future__ import annotations

import json

from playwright.sync_api import Page, expect

from helpers import ApiClient, execute, query, sign_in, wait_until

# Past both windows: a conductor's (30 minutes) and a role's default (2 hours).
_OPENED = "3 hours"


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


def _hung_reviewer(client: ApiClient, owner_dsn: str, task_id: str) -> dict:
    """The task's first reviewer, once its open call is in the ledger, with
    the call back-dated past every window."""
    run = wait_until(lambda: next((r for r in client.task_runs(task_id) if r["phase"] == "review" and r["status"] == "running"), None),
                     timeout=90, message="the reviewer never ran")
    wait_until(lambda: query(owner_dsn, "SELECT 1 FROM runs WHERE id = %s AND open_tool_calls_at <> '{}'::jsonb", (run["id"],)),
               timeout=60, message="the reviewer's open call never reached its Run")
    execute(owner_dsn, """UPDATE runs SET open_tool_calls_at = (SELECT jsonb_object_agg(k, to_jsonb(now() - %s::interval))
                          FROM jsonb_object_keys(open_tool_calls_at) k) WHERE id = %s""", (_OPENED, run["id"]))
    return run


def _stalled(client: ApiClient, run_id: str) -> list[dict]:
    return [e for e in client.events(runId=run_id, limit=1000) if e["eventType"] == "run.stalled"]


def test_the_conductor_is_told_once_and_restarts_the_stalled_reviewer(client: ApiClient, forge_project: dict, owner_dsn: str):
    models = {**forge_project["agentModels"], **client.on_models({"conductor": "fake/scripted", "reviewer": "fake/stall"})}
    assert client.patch(f"/v1/projects/{forge_project['id']}", {"agentModels": models}).status_code == 200
    assert client.patch(f"/v1/projects/{forge_project['id']}/settings",
                        {"delivery": {"conductorWarmMinutes": 30}}).status_code == 200
    task = client.create_task(forge_project["id"], "Greet, past a stalled review")
    conductor = client.post(f"/v1/tasks/{task['id']}/talk").json()["runId"]
    _waiting_on(client, task["id"], "start", "the delivery never waited on the conductor to start")
    _say(client, task["id"], "Go.", _tool("start_phase", {"phase": "implement"}))
    _waiting_on(client, task["id"], "after_implement", "the implementer never came back")
    _say(client, task["id"], "Review it.", _tool("decide", {"action": "next"}))
    hung = _hung_reviewer(client, owner_dsn, task["id"])

    # The conductor is woken with the facts: the open call, what it was asked.
    woken = wait_until(lambda: [e["payload"]["text"] for e in client.events(taskId=task["id"], limit=1000)
                                if e["eventType"] == "conductor.woken" and hung["id"] in e["payload"]["text"]
                                and "made no progress" in e["payload"]["text"]],
                       timeout=60, message="the conductor was never told of the stalled reviewer")
    assert "`task`" in woken[0] and "Review worker state behavior" in woken[0], woken[0]
    assert "restart_run" in woken[0], woken[0]
    assert len(woken) == 1, woken
    assert len(_stalled(client, hung["id"])) == 1
    # Told once: the facts unchanged, the next sweeps say nothing more.
    wait_until(lambda: client.get_run(hung["id"])["stalled"] is not None, timeout=30, message="the Run never showed as stalled")
    assert not (client.get_run(hung["id"])["stalled"] or {}).get("owner")

    _say(client, task["id"], "Restart it.", _tool("restart_run", {"run": hung["id"], "note": "Read the worker yourself."}))
    # The conductor's own call of the tool, as its Run recorded it.
    called = wait_until(lambda: [e for e in client.events(runId=conductor, limit=1000)
                                 if e["eventType"] == "agent.tool.dude" and e["payload"].get("tool") == "restart_run"],
                        timeout=60, message="the conductor never called restart_run")
    assert called[0]["payload"]["arguments"]["run"] == hung["id"], called[0]
    restarted = wait_until(lambda: [e for e in client.events(runId=hung["id"], limit=1000) if e["eventType"] == "run.restarted"],
                           timeout=60, message="the conductor's restart never reached the ledger")
    fresh = restarted[0]["payload"]["to"]
    assert restarted[0]["payload"]["by"] == "conductor" and restarted[0]["actor"]["id"] == conductor, restarted[0]
    assert client.get_run(hung["id"])["replacedBy"] == fresh
    assert client.get_run(hung["id"])["status"] == "aborted"

    # The round goes on with the new Run, back to the conductor.
    _waiting_on(client, task["id"], "after_review", "the restarted review never came back to the conductor")
    assert client.get_run(fresh)["status"] == "completed"
    assert len(_stalled(client, hung["id"])) == 1
    escalations = [e for e in client.events(taskId=task["id"], limit=1000)
                   if e["eventType"] == "question.asked" and e["payload"].get("reason") == "review_failed"]
    assert escalations == [], escalations
    # One stalled wake in all, the round over.
    stalled_wakes = [e for e in client.events(taskId=task["id"], limit=1000)
                     if e["eventType"] == "conductor.woken" and "made no progress" in e["payload"]["text"]]
    assert len(stalled_wakes) == 1, [e["payload"]["text"] for e in stalled_wakes]


def test_an_owner_restarts_a_stalled_reviewer_from_its_banner(page: Page, web_url: str, client: ApiClient, org: dict,
                                                             forge_project: dict, owner_dsn: str, console_errors: list):
    models = {**forge_project["agentModels"], **client.on_models({"reviewer": "fake/stall"})}
    assert client.patch(f"/v1/projects/{forge_project['id']}", {"agentModels": models}).status_code == 200
    task = client.create_task(forge_project["id"], "Greet, past a stalled review, delivered")
    assert client.post(f"/v1/tasks/{task['id']}/deliver").status_code == 201
    hung = _hung_reviewer(client, owner_dsn, task["id"])
    wait_until(lambda: (client.get_run(hung["id"])["stalled"] or {}).get("owner"), timeout=60,
               message="the stalled reviewer was never reported to its owner")
    assert len(_stalled(client, hung["id"])) == 1

    sign_in(page, web_url, org["api_key"])
    page.goto(f"{web_url}#/task/{task['id']}")
    banner = page.get_by_test_id("stalled")
    expect(banner).to_contain_text("has made no progress", timeout=30_000)
    expect(banner).to_contain_text("Review worker state behavior")
    page.get_by_test_id("stalled-note").fill("Read the worker yourself.")
    page.get_by_test_id("stalled-restart").click()
    expect(banner).to_have_count(0, timeout=30_000)

    restarted = wait_until(lambda: [e for e in client.events(runId=hung["id"], limit=1000) if e["eventType"] == "run.restarted"],
                           timeout=60, message="the owner's restart never reached the ledger")
    fresh = restarted[0]["payload"]["to"]
    assert restarted[0]["payload"]["note"] == "Read the worker yourself.", restarted[0]
    assert "by" not in restarted[0]["payload"], restarted[0]
    me = client.get("/v1/me").json()["person"]
    assert restarted[0]["actor"]["id"] == me["id"], restarted[0]["actor"]
    assert client.get_run(hung["id"])["status"] == "aborted"
    assert client.get_run(hung["id"])["replacedBy"] == fresh
    # The delivery goes on with the new Run.
    wait_until(lambda: client.get_run(fresh)["status"] == "completed", timeout=90, message="the restarted reviewer never finished")
    wait_until(lambda: any(r["phase"] == "simplify" for r in client.task_runs(task["id"])), timeout=120,
               message="the delivery never went on past the restarted review")
    assert console_errors == []
