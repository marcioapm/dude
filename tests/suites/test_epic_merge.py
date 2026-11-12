"""A merge on an epic wakes the other task's conductor, end to end through
the deployed backend, orchestrator and fake GitHub.

Two tasks on one epic are delivered to review; the scripted implementer
writes FACTORY.md in each, so their branches share it. Task A's pull
request merges on GitHub. Task B's conductor is woken once, told A's key,
its pull request, the merge commit and the shared FACTORY.md, in the note
it receives; A, merged, is not. Once with the merged side read from A's
stored snapshot, once from GitHub's listing of the pull request's files.
"""

from __future__ import annotations

from fake_github import FakeGitHub
from helpers import ApiClient, execute, query, wait_until


def _pull_request(client: ApiClient, task_id: str) -> dict | None:
    prs = client.get("/v1/pull-requests", params={"taskId": task_id}).json()["pullRequests"]
    return prs[0] if prs else None


def _status(client: ApiClient, task_id: str) -> str:
    return client.get(f"/v1/tasks/{task_id}").json()["status"]


def _epic_merge_wakes(env, task_id: str) -> list[dict]:
    return query(env.owner_dsn, "SELECT key, line, delivered_at FROM conductor_wakes WHERE task_id = %s AND kind = 'epic_merge'",
                 (task_id,))


def _note_received(env, task_id: str) -> str | None:
    """The note B's conductor got with its epic-merge wake: the directive
    that carried it, or the briefing of the conductor started with it."""
    rows = query(env.owner_dsn, """SELECT COALESCE(d.text, r.prompt) AS text FROM conductor_wakes w
        JOIN conductor_wake_attempts o ON o.wake_id = w.id
        LEFT JOIN directives d ON d.id = o.directive_id LEFT JOIN runs r ON r.id = o.conductor_run_id AND o.directive_id IS NULL
        WHERE w.task_id = %s AND w.kind = 'epic_merge' ORDER BY o.created_at LIMIT 1""", (task_id,))
    return rows[0]["text"] if rows and rows[0]["text"] else None


def _two_in_review(client: ApiClient, env, pid: str) -> tuple[dict, dict, dict]:
    """Two tasks on one epic, delivered to review, B's branch read; A's pull
    request."""
    epic = client.post(f"/v1/projects/{pid}/epics", {"title": "Greetings"}).json()
    a = client.create_task(pid, "Greet in English", epicId=epic["id"])
    b = client.create_task(pid, "Greet in Portuguese", epicId=epic["id"])
    for t in (a, b):
        assert client.post(f"/v1/tasks/{t['id']}/deliver").status_code == 201
    for t in (a, b):
        wait_until(lambda t=t: _pull_request(client, t["id"]), timeout=120, message=f"no pull request for {t['key']}")
        wait_until(lambda t=t: _status(client, t["id"]) == "review", timeout=60, message=f"{t['key']} never reached review")
    first = _pull_request(client, a["id"])
    assert first is not None
    # Both branches read at their pull requests' heads: the scripted
    # implementer, fixer and simplifier each wrote one file on each.
    for t in (a, b):
        wait_until(lambda t=t: _paths_at_head(env, t["id"]) == SCRIPTED, timeout=90,
                   message=f"{t['key']}'s branch was never read at its pull request's head")
    return a, b, first


# What the scripted agents write on each task's branch.
SCRIPTED = ["FACTORY.md", "FIXED.md", "SIMPLE.md"]


def _paths_at_head(env, task_id: str) -> list[str]:
    rows = query(env.owner_dsn, """SELECT COALESCE((SELECT array_agg(f->>'path' ORDER BY f->>'path' COLLATE "C")
            FROM jsonb_array_elements(s.files) f), '{}') AS paths
        FROM task_branch_snapshots s JOIN pull_requests p ON p.task_id = s.task_id AND p.repository_id = s.repository_id
        WHERE s.task_id = %s AND s.head_sha = p.head_sha ORDER BY s.captured_at DESC LIMIT 1""", (task_id,))
    return rows[0]["paths"] if rows else []


def _merge_and_check(client: ApiClient, env, fake_github: FakeGitHub, a: dict, b: dict, first: dict) -> None:
    """A merges; B's conductor is told once, the exact line, in its note."""
    [repo] = query(env.owner_dsn, "SELECT r.name FROM pull_requests p JOIN repositories r ON r.id = p.repository_id WHERE p.id = %s",
                   (first["id"],))
    sha = fake_github.merge_into_base(first["number"])

    [wake] = wait_until(lambda: _epic_merge_wakes(env, b["id"]) or None, timeout=60,
                        message="B's conductor was never woken by A's merge")
    want = (f"{a['key']} on your epic merged {repo['name']}#{first['number']} ({sha[:12]}). Your branch also changes `FACTORY.md`, `FIXED.md`, `SIMPLE.md`: "
            "bring `main` in (sync_base, or start_phase fix while a pull request is open) before you go on.")
    assert wake["line"] == want, wake["line"]
    assert wake["key"] == f"epic-merge:{first['id']}", wake
    # Delivered to B's conductor, in the note it received.
    note = wait_until(lambda: _note_received(env, b["id"]), timeout=60, message="the wake never reached B's conductor")
    assert f"\n- {want}\n" in f"\n{note}\n", note
    # Once: the worker's later passes and the sync's later reads add none,
    # and the merged task itself is not woken.
    wait_until(lambda: query(env.owner_dsn, "SELECT count(*) AS n FROM pull_requests WHERE id = %s AND epic_peers_told_at IS NOT NULL",
                             (first["id"],))[0]["n"] == 1, timeout=30, message="the merge was never marked told")
    assert len(_epic_merge_wakes(env, b["id"])) == 1
    assert _epic_merge_wakes(env, a["id"]) == []


def test_a_merge_on_the_epic_wakes_the_peer_with_the_overlap(client: ApiClient, env, forge_project: dict,
                                                             fake_github: FakeGitHub):
    """The merged side from A's stored snapshot at its pull request's head:
    GitHub is not asked for the merge's files."""
    a, b, first = _two_in_review(client, env, forge_project["id"])
    reads = fake_github.files_reads.get(first["number"], 0)
    _merge_and_check(client, env, fake_github, a, b, first)
    assert fake_github.files_reads.get(first["number"], 0) == reads, "the merge's files were read from GitHub"


def test_a_merge_with_no_stored_branch_reads_its_files_from_github(client: ApiClient, env, forge_project: dict,
                                                                   fake_github: FakeGitHub):
    """No snapshot of A at its pull request's head: the merged side comes
    from GitHub's listing of the pull request's files."""
    a, b, first = _two_in_review(client, env, forge_project["id"])
    execute(env.owner_dsn, "DELETE FROM task_branch_snapshots WHERE task_id = %s", (a["id"],))
    reads = fake_github.files_reads.get(first["number"], 0)
    _merge_and_check(client, env, fake_github, a, b, first)
    assert fake_github.files_reads.get(first["number"], 0) > reads, "the merge's files were never read from GitHub"
