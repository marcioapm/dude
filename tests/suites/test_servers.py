"""Servers and branch previews, through the public API against the deployed
backend, orchestrator and fake lux.

A project's recipes and preview settings are the backend's; a task's
servers are lux's, reached through the orchestrator. The fake lux moves
servers as lux does: starting, then ready a moment later; its previews wake
on request, through its test hooks for a signed-in request and idleness.
"""

from __future__ import annotations

import json
import re
import threading

import pytest
import requests
from playwright.sync_api import Page, expect

from env import FAKE_PREVIEW_DOMAIN
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


def test_a_project_defines_its_servers_and_a_preview_serves_them(client: ApiClient, forge_project: dict, env):
    pid = forge_project["id"]
    # The org's first key is its admin: a maintainer.
    saved = client.put(f"/v1/projects/{pid}/servers/web", WEB)
    assert saved.status_code == 200, saved.text
    assert client.put(f"/v1/projects/{pid}/servers/Web", {**WEB, "name": "Web"}).status_code == 400
    big = next(p["id"] for p in client.get("/v1/machines/pools").json()["pools"] if p["name"] == "big")
    sized = client.post("/v1/machines/sizes", {"name": "Preview", "cpus": 3, "memoryMiB": 6144, "diskGiB": 25, "poolId": big})
    assert sized.status_code == 201, sized.text
    size = next(s for s in sized.json()["sizes"] if s["name"] == "Preview")
    settings = client.put(f"/v1/projects/{pid}/preview-settings",
                          {"egress": ["registry.npmjs.org"], "idleTimeoutMinutes": 30, "machineSize": size["id"]})
    assert settings.status_code == 200 and settings.json()["egress"] == ["registry.npmjs.org"]
    assert settings.json()["machineSize"] == size["id"]

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
    # domain, <server>-<task key>-<project slug>, asleep; nothing runs.
    asleep = wait_until(lambda: (s := client.get(f"/v1/tasks/{task['id']}/servers").json())["run"].get("asleep") and s,
                        timeout=60, message="the preview never went to sleep")
    assert asleep["run"]["wakeable"] is True and asleep["run"]["previewStage"] is None, asleep["run"]
    [web] = asleep["servers"]
    host = web["url"].removeprefix("https://")
    label, _, domain = host.partition(".")
    assert domain == FAKE_PREVIEW_DOMAIN and label == f"web-{task['key']}-{forge_project['slug']}".lower(), (host, task["key"])
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
    # Woken on the project's preview size, in its pool by lux's id.
    woken_spec = lux_get(env, f"/v1/runs/{first_lux_run}")["spec"]
    assert woken_spec["resources"] == {"cpus": 3, "memory": 6144 * 1024 * 1024, "disk": 25 * 1024 ** 3}, woken_spec
    assert woken_spec["placement"] == {"poolId": big}, woken_spec
    assert not woken_spec["workload"].get("servers"), woken_spec

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
    resumed_spec = lux_get(env, f"/v1/runs/{first_lux_run}")["spec"]
    assert (resumed_spec["resources"], resumed_spec["placement"]) == (woken_spec["resources"], woken_spec["placement"])

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


def _asleep_preview(client: ApiClient, project_id: str, title: str) -> tuple[dict, dict, dict]:
    """A declared preview of `web`, asleep: its task, its run and its server."""
    assert client.put(f"/v1/projects/{project_id}/servers/web", WEB).status_code == 200
    task = client.create_task(project_id, title)
    started = client.post(f"/v1/tasks/{task['id']}/preview")
    assert started.status_code == 201, started.text
    asleep = wait_until(lambda: (s := client.get(f"/v1/tasks/{task['id']}/servers").json())["run"].get("asleep") and s,
                        timeout=60, message="the preview never went to sleep")
    return task, asleep["run"], asleep["servers"][0]


def test_a_preview_whose_resumed_run_fails_to_start_wakes_on_a_new_run(client: ApiClient, forge_project: dict, env):
    task, run, web = _asleep_preview(client, forge_project["id"], "Recover my preview")
    servers = lambda: client.get(f"/v1/tasks/{task['id']}/servers").json()  # noqa: E731
    lux_fake(env, f"/fake/servers/{web['id']}/request?path=/")
    first = wait_until(lambda: (s := servers())["run"]["previewStage"] == "ready" and s["run"]["luxRunId"],
                       timeout=60, message="the preview never woke")
    assert lux_fake(env, f"/fake/servers/{web['id']}/idle")["idle"] is True
    wait_until(lambda: lux_get(env, f"/v1/runs/{first}")["state"] == "stopped" and servers()["run"].get("asleep"),
               timeout=60, message="the idle preview was never put to sleep")

    # lux accepts the resume; the Run then fails before it runs. One request,
    # and it serves on a new Run; the failed one is cancelled.
    lux_fake(env, f"/fake/fail-starts?label=dude.preview%3D{run['id']}&n=1")
    lux_fake(env, f"/fake/servers/{web['id']}/request?path=/")
    again = wait_until(lambda: (s := servers())["run"]["previewStage"] == "ready" and s["run"],
                       timeout=60, message="the preview never recovered from the failed start")
    assert again["luxRunId"] != first and again.get("error") is None, again
    assert lux_get(env, f"/v1/runs/{first}")["state"] == "cancelled"
    assert lux_fake(env, f"/fake/servers/{web['id']}/request?path=/")["served"] is True


def test_a_preview_whose_run_never_starts_stops_trying_and_says_why(client: ApiClient, forge_project: dict, env):
    task, run, web = _asleep_preview(client, forge_project["id"], "Never starts")
    lux_fake(env, f"/fake/fail-starts?label=dude.preview%3D{run['id']}&n=100")
    try:
        lux_fake(env, f"/fake/servers/{web['id']}/request?path=/")
        given_up = wait_until(lambda: (r := client.get(f"/v1/tasks/{task['id']}/servers").json()["run"]).get("asleep")
                              and "3 times in a row" in (r.get("error") or "") and r,
                              timeout=60, message="dude never stopped trying, or never said why")
        assert given_up["previewStage"] is None and "failed to start" in given_up["error"], given_up
        # The Run it holds is the last one tried, failed in lux; the bound's
        # count of Runs is pinned by the Go tests, which see every lux Run.
        assert lux_get(env, f"/v1/runs/{given_up['luxRunId']}")["state"] == "failed"
    finally:
        lux_fake(env, f"/fake/fail-starts?label=dude.preview%3D{run['id']}&n=0")


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
