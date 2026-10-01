"""Servers and branch previews, through the public API against the deployed
backend, orchestrator and fake lux.

A project's recipes and preview settings are the backend's; a task's
servers are lux's, reached through the orchestrator. The fake lux moves
servers as lux does: starting, then ready a moment later; its previews wake
on request, through its test hooks for a signed-in request and idleness.
"""

from __future__ import annotations

import json
import threading

import requests

from env import FAKE_PREVIEW_DOMAIN
from helpers import ApiClient, wait_until

WEB = {
    "name": "web",
    "port": 3000,
    "command": "npm run dev -- --port 3000",
    "workdir": "",
    "setup": "npm ci",
    "env": [{"name": "PORT", "value": "3000"}],
    "autostartInPreviews": True,
}


def test_a_project_defines_its_servers_and_a_preview_serves_them(client: ApiClient, forge_project: dict, env):
    pid = forge_project["id"]
    # The org's first key is its admin: a maintainer.
    saved = client.put(f"/v1/projects/{pid}/servers/web", WEB)
    assert saved.status_code == 200, saved.text
    assert client.put(f"/v1/projects/{pid}/servers/Web", {**WEB, "name": "Web"}).status_code == 400
    settings = client.put(f"/v1/projects/{pid}/preview-settings", {"egress": ["registry.npmjs.org"], "idleTimeoutMinutes": 30})
    assert settings.status_code == 200 and settings.json()["egress"] == ["registry.npmjs.org"]

    task = client.create_task(pid, "Preview it")
    before = client.get(f"/v1/tasks/{task['id']}/servers").json()
    assert before["run"] is None and [r["name"] for r in before["recipes"]] == ["web"]

    # servers.changed is heard on the task's stream as the preview moves on.
    heard: list[dict] = []
    stop = threading.Event()

    def listen() -> None:
        with requests.get(f"{client.base_url}/v1/events/stream", params={"taskId": task["id"], "key": client.api_key},
                          stream=True, timeout=(5, 60)) as res:
            for raw in res.iter_lines(decode_unicode=True):
                if stop.is_set():
                    return
                if raw and raw.startswith("data:"):
                    event = json.loads(raw[5:])
                    if event["eventType"] == "servers.changed":
                        heard.append(event["payload"])

    threading.Thread(target=listen, daemon=True).start()

    started = client.post(f"/v1/tasks/{task['id']}/preview")
    assert started.status_code == 201, started.text
    run = started.json()["run"]
    assert run["kind"] == "preview" and run["label"] == "Branch preview" and run["parksAfterMinutes"] == 30
    assert client.post(f"/v1/tasks/{task['id']}/preview").status_code == 409

    # Declared: its server exists in lux, at one label under the preview
    # domain, asleep; nothing runs.
    asleep = wait_until(lambda: (s := client.get(f"/v1/tasks/{task['id']}/servers").json())["run"].get("asleep") and s,
                        timeout=60, message="the preview never went to sleep")
    assert asleep["run"]["wakeable"] is True and asleep["run"]["previewStage"] is None, asleep["run"]
    [web] = asleep["servers"]
    host = web["url"].removeprefix("https://")
    label, _, domain = host.partition(".")
    assert domain == FAKE_PREVIEW_DOMAIN and label.startswith("web-") and len(label) <= 63, host
    assert web["serverState"] == "asleep" and web["state"] == "stopped", web
    luxed = lux_get(env, f"/v1/servers?hostname={host}")["servers"]
    assert [s["id"] for s in luxed] == [web["id"]] and luxed[0]["labels"]["dude.task"] == task["id"], luxed

    # Someone opens it: lux asks dude, dude starts it, it serves.
    assert lux_fake(env, f"/fake/servers/{web['id']}/request?path=/")["served"] is False
    ready = wait_until(lambda: (s := client.get(f"/v1/tasks/{task['id']}/servers").json())["run"]["previewStage"] == "ready" and s,
                       timeout=60, message="the preview never woke")
    assert [s["name"] for s in ready["servers"]] == ["web"] and ready["servers"][0]["state"] == "ready"
    assert ready["servers"][0]["url"] == web["url"], "the URL changed on waking"
    assert ready["run"]["terminalUrl"].endswith(f"/runs/{ready['run']['luxRunId']}/terminal")
    assert lux_fake(env, f"/fake/servers/{web['id']}/request?path=/")["served"] is True
    wait_until(lambda: any(p.get("server") == "web" and p.get("state") == "ready" for p in heard), timeout=60,
               message="no servers.changed for web ready")
    stop.set()
    first_lux_run = ready["run"]["luxRunId"]

    # Unused: lux says so, dude stops the Run, the preview sleeps.
    assert lux_fake(env, f"/fake/servers/{web['id']}/idle")["idle"] is True
    wait_until(lambda: client.get(f"/v1/tasks/{task['id']}/servers").json()["run"].get("asleep"), timeout=60,
               message="the idle preview was never put to sleep")
    # dude records the park before asking lux, so lux catches up after.
    wait_until(lambda: lux_get(env, f"/v1/runs/{first_lux_run}")["state"] == "stopped", timeout=60,
               message="dude never stopped the idle preview's Run")

    # Opened again: the same Run resumed.
    lux_fake(env, f"/fake/servers/{web['id']}/request?path=/")
    again = wait_until(lambda: (s := client.get(f"/v1/tasks/{task['id']}/servers").json())["run"]["previewStage"] == "ready" and s,
                       timeout=60, message="the preview never woke again")
    assert again["run"]["luxRunId"] == first_lux_run

    run_id = again["run"]["id"]
    # Starting the server of a running preview is lux's, as for any Run.
    assert client.post(f"/v1/runs/{run_id}/servers/web/start").status_code == 200

    # A preview is not an agent: the sidebar leaves it out, and it is not aborted.
    assert client.post(f"/v1/runs/{run_id}/abort", {}).status_code == 409

    # Stopped: its server deleted in lux (its URL gone), its Run cancelled.
    assert client.delete(f"/v1/tasks/{task['id']}/preview").status_code == 200
    assert client.get(f"/v1/tasks/{task['id']}/servers").json()["run"] is None
    wait_until(lambda: lux_get(env, f"/v1/servers?hostname={host}")["servers"] == [], timeout=60,
               message="the preview's server was never deleted in lux")
    wait_until(lambda: lux_get(env, f"/v1/runs/{first_lux_run}")["state"] == "cancelled", timeout=60,
               message="the preview's Run was never cancelled")


def lux_get(env, path: str) -> dict:
    res = requests.get(env.lux_url + path, headers={"Authorization": f"Bearer {env.lux_key}"}, timeout=10)
    res.raise_for_status()
    return res.json()


def lux_fake(env, path: str) -> dict:
    """The fake lux's test hooks: a signed-in request, lux finding a server idle."""
    res = requests.post(env.lux_url + path, headers={"Authorization": f"Bearer {env.lux_key}"}, timeout=10)
    res.raise_for_status()
    return res.json()


def test_another_organization_sees_no_servers(client: ApiClient, second_org: dict, forge_project: dict):
    assert client.put(f"/v1/projects/{forge_project['id']}/servers/web", WEB).status_code == 200
    other: ApiClient = second_org["client"]
    assert other.get(f"/v1/projects/{forge_project['id']}/servers").status_code == 404
    task = client.create_task(forge_project["id"], "Mine")
    assert other.get(f"/v1/tasks/{task['id']}/servers").status_code == 404
