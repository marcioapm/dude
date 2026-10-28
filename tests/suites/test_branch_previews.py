"""Branch previews (a project's branch, no task), through the public API
against the deployed backend, orchestrator and fake lux, with GitHub's
webhooks signed and delivered as GitHub sends them.

Started on a branch → asleep, its server named by the branch → woken by
starting its server with no browser → a push to the branch (a push
webhook) syncs the running preview to the new commit → the branch deleted
(a delete webhook) ends it: its server gone, its Run cancelled.
"""

from __future__ import annotations

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


def lux_get(env, path: str) -> dict:
    res = requests.get(env.lux_url + path, headers={"Authorization": f"Bearer {env.lux_key}"}, timeout=10)
    res.raise_for_status()
    return res.json()


def lux_events(env, run_id: str) -> list[dict]:
    return lux_get(env, f"/v1/runs/{run_id}/events")["events"]
