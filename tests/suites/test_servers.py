"""Servers and branch previews, through the public API against the deployed
backend, orchestrator and fake lux.

A project's recipes and preview settings are the backend's; a task's
servers are lux's, reached through the orchestrator. The fake lux moves
servers as lux does: starting, then ready a moment later.
"""

from __future__ import annotations

import json
import re
import threading

import pytest
import requests
from playwright.sync_api import Page, expect

from helpers import ApiClient, sign_in, toast, wait_until

WEB = {
    "name": "web",
    "port": 3000,
    "command": "npm run dev -- --port 3000",
    "workdir": "",
    "setup": "npm ci",
    "env": [{"name": "PORT", "value": "3000"}],
    "autostartInPreviews": True,
}


def test_a_project_defines_its_servers_and_a_preview_serves_them(client: ApiClient, forge_project: dict):
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

    ready = wait_until(lambda: (s := client.get(f"/v1/tasks/{task['id']}/servers").json())["run"]["previewStage"] == "ready" and s,
                       timeout=30, message="the preview never served")
    assert [s["name"] for s in ready["servers"]] == ["web"] and ready["servers"][0]["state"] == "ready"
    assert ready["run"]["terminalUrl"].endswith(f"/runs/{ready['run']['luxRunId']}/terminal")
    wait_until(lambda: any(p.get("server") == "web" and p.get("state") == "ready" for p in heard), timeout=30,
               message="no servers.changed for web ready")
    stop.set()

    run_id = ready["run"]["id"]
    # A server of a person's own, stopped, its log, removed.
    assert client.post(f"/v1/runs/{run_id}/servers", {"name": "vite", "port": 5173}).status_code == 201
    stopped = client.post(f"/v1/runs/{run_id}/servers/web/stop")
    assert stopped.status_code == 200 and stopped.json()["state"] == "stopped"
    assert "lines" in client.get(f"/v1/runs/{run_id}/servers/web/log?tail=5").json()
    assert client.delete(f"/v1/runs/{run_id}/servers/vite").status_code == 204

    # A preview is not an agent: the sidebar leaves it out, and it is not aborted.
    assert client.post(f"/v1/runs/{run_id}/abort", {}).status_code == 409

    assert client.delete(f"/v1/tasks/{task['id']}/preview").status_code == 200
    assert client.get(f"/v1/tasks/{task['id']}/servers").json()["run"] is None


def test_another_organization_sees_no_servers(client: ApiClient, second_org: dict, forge_project: dict):
    assert client.put(f"/v1/projects/{forge_project['id']}/servers/web", WEB).status_code == 200
    other: ApiClient = second_org["client"]
    assert other.get(f"/v1/projects/{forge_project['id']}/servers").status_code == 404
    task = client.create_task(forge_project["id"], "Mine")
    assert other.get(f"/v1/tasks/{task['id']}/servers").status_code == 404


# ---------------------------------------------------------------------------
# In the browser
# ---------------------------------------------------------------------------


@pytest.mark.ui
def test_branch_previews_run_on_the_size_a_project_picks_and_reset_follows_the_default(
    page: Page, web_url: str, client: ApiClient, org: dict, project: dict, console_errors: list
):
    pid = project["id"]
    created = client.post("/v1/machines/sizes", {"name": "Large", "cpus": 8, "memoryMiB": 16384, "diskGiB": 80})
    assert created.status_code == 201, created.text
    large = next(s for s in created.json()["sizes"] if s["name"] == "Large")
    org_name = client.get("/v1/settings/organization").json()["organization"]["name"]
    sign_in(page, web_url, org["api_key"])
    page.goto(f"{web_url}#/project/{pid}/settings/servers")
    previews = page.get_by_test_id("preview-settings")
    machine = previews.get_by_test_id("preview-machine")
    # Naming none: the organisation's default size, Standard.
    expect(machine).to_have_text(re.compile(rf"^{re.escape(org_name)}’s default\s*Standard · 2 CPUs · 8 GiB · 20 GiB$"))

    machine.click()
    page.get_by_role("option").filter(has_text=re.compile(r"^Large\s*8 CPUs · 16 GiB · 80 GiB$")).click()
    expect(toast(page, "Machine saved")).to_be_visible()
    assert client.get(f"/v1/projects/{pid}/servers").json()["previews"]["machineSize"] == large["id"]
    expect(machine).to_have_text(re.compile(r"^Large\s*8 CPUs · 16 GiB · 80 GiB$"))

    overridden = previews.locator("[data-source='project']")
    expect(overridden).to_contain_text("default size, Standard")
    overridden.get_by_role("button", name="Reset", exact=True).click()
    expect(toast(page, "Machine reset")).to_be_visible()
    assert client.get(f"/v1/projects/{pid}/servers").json()["previews"]["machineSize"] is None
    expect(previews.get_by_test_id("preview-machine-row").locator("[data-source='organization']")).to_have_text(f"From {org_name}’s default size")
    assert console_errors == []
