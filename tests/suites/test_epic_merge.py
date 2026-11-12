"""A merge on an epic wakes the other task's conductor, end to end through
the deployed backend, orchestrator and fake GitHub.

Two tasks on one epic are delivered to review; the scripted implementer
writes FACTORY.md in each, so their branches share it. Task A's pull
request merges on GitHub. Task B's conductor is woken once, told A's key,
its pull request, the merge commit and the shared FACTORY.md; A, merged,
is not.
"""

from __future__ import annotations

from fake_github import FakeGitHub
from helpers import ApiClient, query, wait_until


def _pull_request(client: ApiClient, task_id: str) -> dict | None:
    prs = client.get("/v1/pull-requests", params={"taskId": task_id}).json()["pullRequests"]
    return prs[0] if prs else None


def _status(client: ApiClient, task_id: str) -> str:
    return client.get(f"/v1/tasks/{task_id}").json()["status"]


def _epic_merge_wakes(env, task_id: str) -> list[dict]:
    return query(env.owner_dsn, "SELECT key, line, delivered_at FROM conductor_wakes WHERE task_id = %s AND kind = 'epic_merge'",
                 (task_id,))


def test_a_merge_on_the_epic_wakes_the_peer_with_the_overlap(client: ApiClient, env, forge_project: dict,
                                                             fake_github: FakeGitHub):
    pid = forge_project["id"]
    epic = client.post(f"/v1/projects/{pid}/epics", {"title": "Greetings"}).json()
    a = client.create_task(pid, "Greet in English", epicId=epic["id"])
    b = client.create_task(pid, "Greet in Portuguese", epicId=epic["id"])
    for t in (a, b):
        assert client.post(f"/v1/tasks/{t['id']}/deliver").status_code == 201
    for t in (a, b):
        wait_until(lambda t=t: _pull_request(client, t["id"]), timeout=120, message=f"no pull request for {t['key']}")
        wait_until(lambda t=t: _status(client, t["id"]) == "review", timeout=60, message=f"{t['key']} never reached review")
    # B's branch, read: what its overlap is computed from.
    wait_until(lambda: query(env.owner_dsn, "SELECT count(*) AS n FROM task_branch_snapshots WHERE task_id = %s",
                             (b["id"],))[0]["n"] > 0, timeout=60, message="B's branch was never read")
    first = _pull_request(client, a["id"])
    assert first is not None

    sha = fake_github.merge_into_base(first["number"])

    [wake] = wait_until(lambda: _epic_merge_wakes(env, b["id"]) or None, timeout=60,
                        message="B's conductor was never woken by A's merge")
    line = wake["line"]
    assert f"{a['key']} on your epic merged" in line, line
    assert f"#{first['number']} ({sha[:12]})" in line, line
    assert "`FACTORY.md`" in line, line
    assert "bring `main` in" in line, line
    assert wake["key"] == f"epic-merge:{first['id']}", wake
    # Delivered to B's conductor as a note.
    wait_until(lambda: all(w["delivered_at"] is not None for w in _epic_merge_wakes(env, b["id"])), timeout=60,
               message="the wake never reached B's conductor")
    # Once: the worker's later passes and the sync's later reads add none,
    # and the merged task itself is not woken.
    wait_until(lambda: query(env.owner_dsn, "SELECT count(*) AS n FROM pull_requests WHERE id = %s AND epic_peers_told_at IS NOT NULL",
                             (first["id"],))[0]["n"] == 1, timeout=30, message="the merge was never marked told")
    assert len(_epic_merge_wakes(env, b["id"])) == 1
    assert _epic_merge_wakes(env, a["id"]) == []
