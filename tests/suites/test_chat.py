"""A task's Chat, end to end through the deployed processes.

A delivered task's first message in Chat starts its conductor, briefed by
dude; the scripted conductor (fakeagent) answers each input with a line
that quotes its briefing's task line, so the test sees the briefing
arrived. A second message reaches the same conductor; it parks after its
warm period (DUDE_CONDUCTOR_WARM, seconds in this suite); a third message
resumes it, timed, and it answers. Throughout, the task's workflow, Runs
and branch stay as delivery left them.
"""

from __future__ import annotations

from fake_github import FakeGitHub
from helpers import ApiClient, query, wait_until


def _conductor_project(client: ApiClient, forge_project: dict) -> dict:
    models = {**forge_project["agentModels"], **client.on_models({"conductor": "fake/scripted"})}
    resp = client.patch(f"/v1/projects/{forge_project['id']}", {"agentModels": models})
    assert resp.status_code == 200, resp.text
    return resp.json()


def _delivered(client: ApiClient, project: dict, fake_github: FakeGitHub) -> dict:
    task = client.create_task(project["id"], "Retry on 429 with backoff")
    assert client.post(f"/v1/tasks/{task['id']}/deliver").status_code == 201
    wait_until(lambda: client.get("/v1/pull-requests", params={"taskId": task["id"]}).json()["pullRequests"],
               timeout=90, message="the delivery never opened a pull request")
    return client.get(f"/v1/tasks/{task['id']}").json()


def _said(client: ApiClient, run_id: str) -> list[str]:
    return [e["payload"]["text"] for e in client.events(runId=run_id, limit=1000) if e["eventType"] == "agent.message"]


def _state(client: ApiClient, owner_dsn: str, task_id: str, fake_github: FakeGitHub, branch: str) -> tuple:
    """What delivery left: the task's status, its phase Runs, its workflow and its branch."""
    runs = sorted((r["id"], r["phase"], r["status"]) for r in client.task_runs(task_id) if r["phase"])
    workflow = query(owner_dsn, "SELECT step, status::text FROM workflow_runs WHERE task_id = %s", (task_id,))
    return client.get(f"/v1/tasks/{task_id}").json()["status"], runs, workflow, fake_github.branch_sha(branch)


def test_a_delivered_tasks_chat_starts_its_conductor_which_answers_parks_and_wakes(
    client: ApiClient, forge_project: dict, fake_github: FakeGitHub, owner_dsn: str
):
    project = _conductor_project(client, forge_project)
    task = _delivered(client, project, fake_github)
    branch = next(r["branch"] for r in task["runs"] if r["branch"])
    before = _state(client, owner_dsn, task["id"], fake_github, branch)
    key = f"{task['key']} · {task['id']}"

    # The first message starts the conductor, briefed by dude.
    resp = client.post(f"/v1/tasks/{task['id']}/chat", {"text": "why is the max backoff 8s?"})
    assert resp.status_code == 201, resp.text
    conductor = resp.json()["runId"]
    run = client.get_run(conductor)
    assert (run["phase"], run["role"], run["kind"]) == (None, "conductor", "agent")
    wait_until(lambda: _said(client, conductor), timeout=30, message="the conductor never answered")
    first = _said(client, conductor)[0]
    assert key in first and "why is the max backoff 8s?" in first, first
    briefed = [e for e in client.events(runId=conductor) if e["eventType"] == "conductor.briefed"]
    assert len(briefed) == 1 and briefed[0]["payload"]["text"].endswith("why is the max backoff 8s?")

    # A second message reaches the same conductor.
    resp = client.post(f"/v1/tasks/{task['id']}/chat", {"text": "and does it retry POSTs?"})
    assert resp.status_code == 200 and resp.json()["runId"] == conductor, resp.text
    wait_until(lambda: len(_said(client, conductor)) == 2, timeout=30, message="the second answer never came")
    assert "and does it retry POSTs?" in _said(client, conductor)[1]

    # Past its warm period it is parked, quietly.
    wait_until(lambda: (r := client.get_run(conductor))["status"] == "paused" and r["dudePause"] == "conductor",
               timeout=30, message="the conductor was never parked")
    assert client.get(f"/v1/tasks/{task['id']}").json()["status"] == before[0]

    # A third message resumes it, timed as a conductor's resume, and it answers.
    resp = client.post(f"/v1/tasks/{task['id']}/chat", {"text": "what about the tests?"})
    assert resp.status_code == 200 and resp.json()["runId"] == conductor, resp.text
    wait_until(lambda: len(_said(client, conductor)) == 3, timeout=60, message="the parked conductor never answered")
    assert "what about the tests?" in _said(client, conductor)[2]
    resumes = query(owner_dsn, "SELECT cause FROM run_resumes WHERE run_id = %s", (conductor,))
    assert [r["cause"] for r in resumes] == ["conductor"]

    # Nothing about the task moved: its status, Runs, workflow and branch.
    assert _state(client, owner_dsn, task["id"], fake_github, branch) == before
    assert len([r for r in client.task_runs(task["id"]) if r["role"] == "conductor"]) == 1


def test_an_empty_message_is_refused(client: ApiClient, forge_project: dict):
    task = client.create_task(forge_project["id"], "Nothing to say")
    assert client.post(f"/v1/tasks/{task['id']}/chat", {"text": "  "}).status_code == 400
    assert [r for r in client.task_runs(task["id"]) if r["role"] == "conductor"] == []
