"""Independent conductor children through the deployed tool and phase paths."""

from helpers import ApiClient, query, wait_until
from fake_github import FakeGitHub
from suites.test_conductor_edits import _point, _say, _tool, _said


def _world(client, forge_project):
    models = {**forge_project["agentModels"], **client.on_models({
        role: "fake/scripted" for role in ("conductor", "investigator", "implementer", "reviewer")})}
    assert client.patch(f"/v1/projects/{forge_project['id']}", {"agentModels": models}).status_code == 200
    task = client.create_task(forge_project["id"], "Independent conductor children")
    response = client.post(f"/v1/tasks/{task['id']}/talk")
    assert response.status_code == 201, response.text
    wait_until(lambda: _point(client, task["id"]) == "start", timeout=90)
    _say(client, task["id"], "Begin the delivery.", _tool("start_phase", {"phase": "implement"}))
    wait_until(lambda: _point(client, task["id"]) == "after_implement", timeout=120)
    return task, response.json()["runId"]


def _children(owner_dsn, task):
    return query(owner_dsn, "SELECT id, status, phase, heads FROM runs WHERE task_id=%s AND source_kind='task_conductor' ORDER BY created_at", (task["id"],))


def _started(client, owner_dsn, task, parent):
    rows = _children(owner_dsn, task)
    messages = _said(client, parent)
    for text in messages:
        if "start_agent answered" in text:
            parent_state = query(owner_dsn, "SELECT attempt,status,phase,source_kind FROM runs WHERE id=%s", (parent,))
            delivery = query(owner_dsn, "SELECT status,step,state FROM workflow_runs WHERE task_id=%s", (task["id"],))
            raise AssertionError((text, parent_state, delivery))
    return rows and rows[0]


def test_child_report_returns_once_without_moving_delivery(client: ApiClient, owner_dsn, forge_project):
    task, parent = _world(client, forge_project)
    before = client.get(f"/v1/tasks/{task['id']}").json()
    _say(client, task["id"], "Investigate separately.", _tool("start_agent", {"profile": "investigator", "key": "report"}))
    child = wait_until(lambda: _started(client, owner_dsn, task, parent), timeout=60)
    wait_until(lambda: query(owner_dsn, "SELECT * FROM child_results WHERE child_run_id=%s", (child["id"],)), timeout=120)
    wait_until(lambda: query(owner_dsn, "SELECT * FROM child_result_outbox WHERE child_run_id=%s AND consumed_at IS NOT NULL", (child["id"],)), timeout=90)
    after = client.get(f"/v1/tasks/{task['id']}").json()
    assert after["status"] == before["status"]
    assert _point(client, task["id"]) == "after_implement"
    results = query(owner_dsn, "SELECT * FROM child_results WHERE child_run_id=%s", (child["id"],))
    assert len(results) == 1
    assert query(owner_dsn, "SELECT id FROM artifacts WHERE run_id=%s", (child["id"],))
    assert len(query(owner_dsn, "SELECT * FROM child_result_outbox WHERE child_run_id=%s", (child["id"],))) == 1


def test_child_writer_moves_branch_and_returns_commit(client: ApiClient, owner_dsn, forge_project, fake_github: FakeGitHub):
    task, parent = _world(client, forge_project)
    _say(client, task["id"], "Delegate implementation.", _tool("start_agent", {"profile": "implementer", "key": "write"}))
    child = wait_until(lambda: _started(client, owner_dsn, task, parent), timeout=60)
    wait_until(lambda: client.get_run(child["id"])["status"] in ("completed", "failed"), timeout=120)
    assert client.get_run(child["id"])["status"] == "completed"
    branch = f"dude/{task['id']}/attempt-1"
    head = wait_until(lambda: fake_github.branch_sha(branch), timeout=60)
    result = wait_until(lambda: query(owner_dsn, "SELECT commits FROM child_results WHERE child_run_id=%s", (child["id"],)), timeout=120)[0]
    assert head in result["commits"].values()
    wait_until(lambda: query(owner_dsn, "SELECT * FROM child_result_outbox WHERE child_run_id=%s AND consumed_at IS NOT NULL", (child["id"],)), timeout=90)
    assert _point(client, task["id"]) == "after_implement"


def test_child_writer_excludes_another_but_allows_reports_and_selective_abort(client: ApiClient, owner_dsn, forge_project):
    task, parent = _world(client, forge_project)
    models = {**forge_project["agentModels"], **client.on_models({"conductor": "fake/scripted", "implementer": "fake/hang", "investigator": "fake/hang"})}
    assert client.patch(f"/v1/projects/{forge_project['id']}", {"agentModels": models}).status_code == 200
    _say(client, task["id"], "Work in parallel.",
         _tool("start_agent", {"profile": "implementer", "key": "first"}),
         _tool("start_agent", {"profile": "implementer", "key": "second"}),
         _tool("start_agent", {"profile": "investigator", "key": "report"}))
    rows = wait_until(lambda: (rows := _children(owner_dsn, task)) and len(rows) == 2 and rows, timeout=90)
    wait_until(lambda: any("implementer is working" in text for text in _said(client, parent)), timeout=60)
    writer = next(row for row in rows if row["phase"] == "implement")
    sibling = next(row for row in rows if row["phase"] == "investigate")
    response = client.post(f"/v1/runs/{writer['id']}/abort", {})
    assert response.status_code == 200, response.text
    wait_until(lambda: client.get_run(writer["id"])["status"] == "aborted", timeout=60)
    assert client.get_run(sibling["id"])["status"] not in ("aborted", "failed", "completed")
    assert client.get(f"/v1/tasks/{task['id']}").json()["status"] not in ("aborted", "failed", "done")
