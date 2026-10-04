"""The conductor steering a Run it conducts, end to end through the deployed processes.

A conducted task (Talk it through, then start_phase implement). The
implementer runs a long command (fake/command) and never finishes its
turn. The scripted conductor carries out the steer a person's message
names ("tool: steer {...}"), as a real conductor would decide to. lux
takes the steer at once; when the command finishes, the implementer's
next step reads it, in the same turn. Its session shows the steer read,
signed by the conductor; the conductor's Chat shows it under the
implementer's line, read; and the conductor is woken saying so.
"""

from __future__ import annotations

import json

import requests
from playwright.sync_api import Page, expect

from helpers import ApiClient, query, sign_in, wait_until


def _tool(name: str, args: dict) -> str:
    return f"tool: {name} {json.dumps(args)}"


def _say(client: ApiClient, task_id: str, *lines: str) -> dict:
    resp = client.post(f"/v1/tasks/{task_id}/chat", {"text": "\n".join(lines)})
    assert resp.status_code in (200, 201), resp.text
    return resp.json()


def test_the_conductor_steers_its_running_implementer(page: Page, web_url: str, env, client: ApiClient, org: dict,
                                                      forge_project: dict, owner_dsn: str, console_errors: list):
    models = {**forge_project["agentModels"], **client.on_models({"conductor": "fake/scripted", "implementer": "fake/command"})}
    assert client.patch(f"/v1/projects/{forge_project['id']}", {"agentModels": models}).status_code == 200
    assert client.patch(f"/v1/projects/{forge_project['id']}/settings",
                        {"delivery": {"conductorWarmMinutes": 30}}).status_code == 200
    task = client.create_task(forge_project["id"], "Greet, steered")

    resp = client.post(f"/v1/tasks/{task['id']}/talk")
    assert resp.status_code == 201, resp.text
    conductor = resp.json()["runId"]
    wait_until(lambda: (client.get(f"/v1/tasks/{task['id']}").json()["awaitingDecision"] or {}).get("point") == "start",
               timeout=90, message="the delivery never waited on the conductor to start")
    _say(client, task["id"], "Go.", _tool("start_phase", {"phase": "implement"}))
    implementer = wait_until(lambda: next((r for r in client.task_runs(task["id"])
                                           if r["phase"] == "implement" and r["status"] == "running"), None),
                             timeout=60, message="the implementer never ran")
    wait_until(lambda: any(e["eventType"] == "agent.tool.called" for e in client.events(runId=implementer["id"], limit=1000)),
               timeout=30, message="the implementer never started its command")

    # The person says something; the conductor steers the implementer with it.
    words = "Keep the greeting in one file."
    _say(client, task["id"], "Tell the implementer to keep it in one file.",
         _tool("steer", {"run": implementer["id"], "text": words}))
    steered = wait_until(lambda: [e for e in client.events(runId=implementer["id"], limit=1000) if e["eventType"] == "run.steered"],
                         timeout=60, message="the conductor's steer never reached the ledger")
    assert steered[0]["payload"]["text"] == words and steered[0]["payload"]["by"] == "conductor", steered[0]
    assert steered[0]["actor"]["type"] == "agent" and steered[0]["actor"]["id"] == conductor, steered[0]["actor"]
    directive = steered[0]["payload"]["directiveId"]
    wait_until(lambda: any(e["eventType"] == "run.directive.accepted" and e["payload"]["directiveId"] == directive
                           for e in client.events(runId=implementer["id"], limit=1000)),
               timeout=30, message="lux never took the steer")

    # The command finishes: the implementer's next step reads it, in the same turn.
    lux_run = query(owner_dsn, "SELECT lux_run_id FROM runs WHERE id = %s", (implementer["id"],))[0]["lux_run_id"]
    requests.post(f"{env.fake_lux_url}/fake/runs/{lux_run}/finish-tools",
                  headers={"authorization": f"Bearer {env.lux_key}"}, timeout=10).raise_for_status()
    wait_until(lambda: any(e["eventType"] == "run.directive.delivered" and e["payload"]["directiveId"] == directive
                           and e["payload"].get("read") for e in client.events(runId=implementer["id"], limit=1000)),
               timeout=30, message="the implementer never read the steer")
    assert client.get_run(implementer["id"])["status"] == "running"
    wait_until(lambda: any(f"read your steer {directive}" in e["payload"]["text"]
                           for e in client.events(taskId=task["id"], limit=1000) if e["eventType"] == "conductor.woken"),
               timeout=60, message="the conductor was never woken with its steer read")

    sign_in(page, web_url, org["api_key"])
    # Its session: the steer read, signed by the conductor.
    page.goto(f"{web_url}#/session/{implementer['id']}")
    turn = page.locator("[data-testid=human-turn][data-by=conductor]")
    expect(turn).to_contain_text(words, timeout=30_000)
    expect(turn).to_contain_text("Conductor")
    expect(turn).to_contain_text("read")
    expect(turn).to_have_attribute("data-role", "conductor")
    # The conductor's Chat: under the implementer's line, read.
    page.goto(f"{web_url}#/task/{task['id']}")
    line = page.locator(f"[data-testid=chat-run][data-run='{implementer['id']}']")
    steer = line.get_by_test_id("conductor-steer")
    expect(steer).to_contain_text(words, timeout=30_000)
    expect(steer).to_contain_text("read")
    expect(steer).not_to_have_attribute("data-pending", "true")
    assert console_errors == []
