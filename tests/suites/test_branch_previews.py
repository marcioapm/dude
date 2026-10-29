"""Branch previews (a project's branch, no task), through the public API
against the deployed backend, orchestrator and fake lux, with GitHub's
webhooks signed and delivered as GitHub sends them.

Started on a branch → asleep, its server named by the branch → woken by
starting its server with no browser → a push to the branch (a push
webhook) syncs the running preview to the new commit → the branch deleted
(a delete webhook) ends it: its server gone, its Run cancelled.
"""

from __future__ import annotations

import json
import os
import subprocess
from pathlib import Path

import requests

from helpers import ApiClient, wait_until


def test_a_branch_preview_follows_its_branch_and_ends_with_it(client: ApiClient, forge_project: dict, fake_github, env):
    pid = forge_project["id"]
    recipe = {"name": "web", "port": 3000, "command": "npm run dev", "workdir": "", "env": [], "autostartInPreviews": True}
    assert client.put(f"/v1/projects/{pid}/servers/web", recipe).status_code == 200

    missing = client.post(f"/v1/projects/{pid}/branch-previews", {"branch": "feature/nope"})
    assert missing.status_code == 422 and missing.json()["error"]["code"] == "branch_not_found", missing.text

    fake_github.push_branch("feature/login")
    started = client.post(f"/v1/projects/{pid}/branch-previews", {"branch": "feature/login"})
    assert started.status_code == 201, started.text
    preview = started.json()
    again = client.post(f"/v1/projects/{pid}/branch-previews", {"branch": "feature/login"})
    assert again.status_code == 200 and again.json()["id"] == preview["id"], again.text

    # Asleep, its server made and named by the branch.
    asleep = wait_until(lambda: (p := client.get(f"/v1/branch-previews/{preview['id']}").json())["status"] == "paused"
                        and p["servers"] and p, timeout=60, message="the branch preview never went to sleep")
    [web] = asleep["servers"]
    assert "web-feature-login-" in (web["url"] or ""), web
    listed = client.get(f"/v1/projects/{pid}/branch-previews").json()["previews"]
    assert [p["id"] for p in listed] == [preview["id"]], listed

    # Woken without a browser: a server started on its Run.
    woke = client.post(f"/v1/runs/{preview['id']}/servers/web/start")
    assert woke.status_code == 200, woke.text
    wait_until(lambda: client.get(f"/v1/branch-previews/{preview['id']}").json()["status"] == "running",
               timeout=60, message="the branch preview never woke")
    lux_run = client.get(f"/v1/runs/{preview['id']}/servers").json()["run"]["luxRunId"]
    spec = lux_get(env, f"/v1/runs/{lux_run}")["spec"]
    assert [r["ref"] for r in spec["git"]["repositories"]] == ["feature/login"], spec["git"]
    assert "dude.task" not in spec["labels"], spec["labels"]

    # A push to the branch: GitHub's push webhook syncs the running preview.
    expected_sha = fake_github.commit_file("feature/login", "login.txt", "second\n", "Login page, again")
    assert fake_github.send_webhook("push", {"ref": "refs/heads/feature/login", "deleted": False}) in (200, 202)
    def pushed_commit_synced():
        return any(
            e["type"] == "git.sync"
            and e["data"].get("repo") == "greeter"
            and e["data"].get("ref") == "feature/login"
            and e["data"].get("status") == "fast-forward"
            and e["data"].get("to") == expected_sha
            for e in lux_events(env, lux_run)
        )

    wait_until(pushed_commit_synced, timeout=60,
               message="the push never synced the preview to the exact pushed branch and commit")

    # The branch deleted: GitHub's delete webhook ends it.
    fake_github._git_out("update-ref", "-d", "refs/heads/feature/login")
    assert fake_github.send_webhook("delete", {"ref": "feature/login", "ref_type": "branch"}) in (200, 202)
    ended = wait_until(lambda: (p := client.get(f"/v1/branch-previews/{preview['id']}").json())["status"] == "completed" and p,
                       timeout=60, message="deleting the branch never ended the preview")
    assert ended["endedAt"], ended
    wait_until(lambda: lux_get(env, f"/v1/runs/{lux_run}")["state"] in ("cancelled", "terminated"), timeout=60,
               message="the ended preview's Run was never cancelled")
    assert client.get(f"/v1/projects/{pid}/branch-previews").json()["previews"] == []


def test_cli_branch_preview_lifecycle(client: ApiClient, forge_project: dict, fake_github, env, tmp_path):
    binary = Path(__file__).resolve().parents[2] / "orchestrator" / "bin" / "dude-cli"
    home = tmp_path / "home"
    home.mkdir()
    config = tmp_path / "config"
    config.mkdir()
    cli_env = {k: v for k, v in os.environ.items() if not k.startswith("DUDE_")}
    cli_env.update(HOME=str(home), XDG_CONFIG_HOME=str(config),
                   DUDE_URL=env.control_plane_url, DUDE_API_KEY=client.api_key)

    def cli(*args):
        result = subprocess.run([str(binary), "preview", *args], cwd=tmp_path, env=cli_env,
                                input="", capture_output=True, text=True, timeout=45)
        assert result.returncode == 0, (args, result.returncode, result.stdout, result.stderr)
        return json.loads(result.stdout)

    pid = forge_project["id"]
    project = forge_project["key"]
    recipe = {"name": "web", "port": 3000, "command": "npm run dev", "workdir": "", "env": [], "autostartInPreviews": True}
    assert client.put(f"/v1/projects/{pid}/servers/web", recipe).status_code == 200
    ids = []
    assert client.put(f"/v1/projects/{pid}/servers/api", {**recipe, "name": "api", "port": 3001}).status_code == 200
    for branch in ("feature/acme-login", "feature/acme-settings"):
        fake_github.push_branch(branch)
        started = cli("create", project, branch)
        preview_id = started["id"]
        assert preview_id and preview_id not in ids, started
        ids.append(preview_id)
        assert started["branch"] == branch, started
        web = next(s for s in started["servers"] if s["name"] == "web")
        assert web["url"] and f"web-{branch.replace('/', '-')}-" in web["url"], web
        again = cli("create", project, branch)
        assert again["id"] == preview_id, again

        # A paused preview with servers proves the real orchestrator reconciled the CLI's request.
        asleep = wait_until(lambda: (p := client.get(f"/v1/branch-previews/{preview_id}").json())["status"] == "paused"
                            and p["servers"] and p, timeout=60, message="the CLI preview never went to sleep")
        shown = cli("show", preview_id)
        assert shown["id"] == preview_id and shown["branch"] == branch and shown["status"] == "paused", shown
        assert shown["servers"] == asleep["servers"], shown
        listed = cli("list", project)["previews"]
        assert [p["id"] for p in listed] == [preview_id], listed
        assert listed[0]["branch"] == branch and listed[0]["status"] == "paused", listed
        assert listed[0]["servers"] == shown["servers"], listed

        live = client.get(f"/v1/runs/{preview_id}/servers").json()
        assert live["run"]["asleep"] is True and not live["run"]["luxRunId"], live
        assert all(s["state"] != "ready" for s in live["servers"]), live

        # Both address forms wake the existing preview, never allocate a new one.
        address = (preview_id,) if len(ids) == 1 else (project, branch)
        waking = cli("start", *address)
        assert waking["id"] == preview_id and "ready" in waking, waking
        ready = cli("start", *address, "--wait", "30s")
        assert ready["id"] == preview_id and ready["ready"] is True, ready
        assert ready["run"]["asleep"] is False and ready["run"]["luxState"] == "running", ready
        assert {s["name"] for s in ready["servers"] if s["state"] == "ready"} == {"web", "api"}, ready
        assert all(s["serverState"] == "ready" for s in ready["servers"]), ready

        if len(ids) == 1:
            cli("stop", preview_id)
        else:
            cli("stop", project, branch)
        ended = wait_until(lambda: (p := cli("show", preview_id))["status"] == "completed" and p,
                           timeout=60, message="the CLI stop never ended the preview")
        assert ended["id"] == preview_id and ended["branch"] == branch and ended["endedAt"], ended
        assert cli("list", project)["previews"] == []

    fake_github.push_branch("feature/create-start")
    ready = cli("create", project, "feature/create-start", "--start", "--wait", "30s")
    assert ready["ready"] is True and ready["servers"][0]["state"] == "ready", ready
    assert ready["servers"][0]["serverState"] == "ready", ready
    cli("stop", ready["id"])

    # The supported fakeLux never-ready recipe leaves a real running Run
    # with allocated URLs. The actual CLI must still time out, not succeed.
    assert client.put(f"/v1/projects/{pid}/servers/api", {**recipe, "name": "api", "port": 3001,
                                                       "command": "fakelux-never-ready"}).status_code == 200
    fake_github.push_branch("feature/not-ready")
    pending = cli("create", project, "feature/not-ready", "--start")
    preview_id = pending["id"]
    wait_until(lambda: (s := client.get(f"/v1/runs/{preview_id}/servers").json())["run"]["luxState"] == "running"
               and any(sv["name"] == "web" and sv["state"] == "ready" for sv in s["servers"]), timeout=30,
               message="the fixture never reached one-ready/one-starting")
    result = subprocess.run([str(binary), "preview", "start", preview_id, "--wait", "500ms"],
                            cwd=tmp_path, env=cli_env, input="", capture_output=True, text=True, timeout=5)
    assert result.returncode != 0 and preview_id in result.stderr and "api=starting" in result.stderr, result
    assert "deadline" in result.stderr or "timed out" in result.stderr, result
    cli("stop", preview_id)


def lux_get(env, path: str) -> dict:
    res = requests.get(env.lux_url + path, headers={"Authorization": f"Bearer {env.lux_key}"}, timeout=10)
    res.raise_for_status()
    return res.json()


def lux_events(env, run_id: str) -> list[dict]:
    return lux_get(env, f"/v1/runs/{run_id}/events")["events"]
