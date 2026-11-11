"""A task's Chat, end to end through the deployed processes.

A delivered task's first message in Chat starts its conductor, briefed by
dude; the scripted conductor (fakeagent) answers each input with a line
that quotes its briefing's task line, so the test sees the briefing
arrived. A second message reaches the same conductor, kept warm by its
project's conductorWarmMinutes so it is live however slow the runner;
with that reset it parks after the suite's warm period
(DUDE_CONDUCTOR_WARM, seconds); a third message resumes it, timed, and it
answers. Throughout, the task's workflow, Runs and branch stay as
delivery left them.
"""

from __future__ import annotations

from fake_github import FakeGitHub
from helpers import ApiClient, query, wait_until


def _conductor_project(client: ApiClient, forge_project: dict) -> dict:
    models = {**forge_project["agentModels"], **client.on_models({"conductor": "fake/defaults"})}
    resp = client.patch(f"/v1/projects/{forge_project['id']}", {"agentModels": models})
    assert resp.status_code == 200, resp.text
    return resp.json()


def _delivered(client: ApiClient, project: dict, fake_github: FakeGitHub) -> dict:
    """A delivered task, settled where delivery leaves it: its pull request
    open and the task ready to merge. The fixture's repository has no CI
    and its protection asks for no approval, so the ready gate passes as
    soon as the pull request is weighed; status read any earlier would be
    a moment in delivery, not where it rests."""
    task = client.create_task(project["id"], "Retry on 429 with backoff")
    assert client.post(f"/v1/tasks/{task['id']}/deliver").status_code == 201
    wait_until(lambda: client.get("/v1/pull-requests", params={"taskId": task["id"]}).json()["pullRequests"],
               timeout=90, message="the delivery never opened a pull request")
    return wait_until(lambda: (t := client.get(f"/v1/tasks/{task['id']}").json())["status"] == "ready_to_merge" and t,
                      timeout=60, message="the delivered task never settled at ready to merge")


def _said(client: ApiClient, run_id: str) -> list[str]:
    return [e["payload"]["text"] for e in client.events(runId=run_id, limit=1000) if e["eventType"] == "agent.message"]


def _answer(client: ApiClient, run_id: str, since: int, asked: str) -> str | None:
    """The conductor's answer quoting asked, among what it said after the first since."""
    return next((t for t in _said(client, run_id)[since:] if asked in t), None)


def _state(client: ApiClient, owner_dsn: str, task_id: str, fake_github: FakeGitHub, branch: str) -> tuple:
    """What delivery left: the task's status, its phase Runs, its workflow and its branch."""
    runs = sorted((r["id"], r["phase"], r["status"]) for r in client.task_runs(task_id) if r["phase"])
    workflow = query(owner_dsn, "SELECT step, status::text FROM workflow_runs WHERE task_id = %s", (task_id,))
    return client.get(f"/v1/tasks/{task_id}").json()["status"], runs, workflow, fake_github.branch_sha(branch)


def test_a_delivered_tasks_chat_starts_its_conductor_which_answers_parks_and_wakes(
    client: ApiClient, forge_project: dict, fake_github: FakeGitHub, owner_dsn: str
):
    project = _conductor_project(client, forge_project)
    # Warm for ten minutes while the first two messages are live, however
    # slow the runner: the second must reach a running conductor, not
    # resume a parked one. Reset below, to the suite's seconds, to park it.
    settings = f"/v1/projects/{project['id']}/settings"
    assert client.patch(settings, {"delivery": {"conductorWarmMinutes": 10}}).status_code == 200
    task = _delivered(client, project, fake_github)
    branch = next(r["branch"] for r in task["runs"] if r["branch"])
    # The workflow too has come to rest, waiting on the pull request.
    before = wait_until(lambda: (s := _state(client, owner_dsn, task["id"], fake_github, branch))[2]
                        == [{"step": "awaitPullRequest", "status": "waiting"}] and s,
                        timeout=30, message="the delivery's workflow never came to wait on the pull request")
    assert before[0] == "ready_to_merge", before
    key = f"{task['key']} · {task['id']}"

    # Every delivery has a conductor: Deliver's, quiet, briefed by dude when
    # it was first woken. The first message is its next input, and makes it
    # chatty.
    quiet = next(r["id"] for r in client.task_runs(task["id"]) if r["role"] == "conductor")
    briefed = [e for e in client.events(runId=quiet) if e["eventType"] == "conductor.briefed"]
    assert len(briefed) == 1 and key in briefed[0]["payload"]["text"], briefed
    said = len(_said(client, quiet))
    resp = client.post(f"/v1/tasks/{task['id']}/chat", {"text": "why is the max backoff 8s?"})
    assert resp.status_code == 200 and resp.json()["runId"] == quiet, resp.text
    assert client.get(f"/v1/tasks/{task['id']}").json()["voice"] == "chatty"
    conductor = quiet
    run = client.get_run(conductor)
    assert (run["phase"], run["role"], run["kind"]) == (None, "conductor", "agent")
    # Answers are matched by what they quote: a decision's wake may come
    # between the messages (its conductor decides the delivery too).
    first = wait_until(lambda: _answer(client, conductor, said, "why is the max backoff 8s?"), timeout=30,
                       message="the conductor never answered")
    assert key in first, first

    # A second message reaches the same conductor, still warm: no resume.
    assert client.get_run(conductor)["status"] == "running"
    resp = client.post(f"/v1/tasks/{task['id']}/chat", {"text": "and does it retry POSTs?"})
    assert resp.status_code == 200 and resp.json()["runId"] == conductor, resp.text
    wait_until(lambda: _answer(client, conductor, said, "and does it retry POSTs?"), timeout=30,
               message="the second answer never came")
    resumed = len(query(owner_dsn, "SELECT cause FROM run_resumes WHERE run_id = %s", (conductor,)))

    # Past its warm period — the project's setting reset, the suite's
    # seconds (DUDE_CONDUCTOR_WARM) — it is parked, quietly.
    assert client.patch(settings, {"delivery": {"conductorWarmMinutes": None}}).status_code == 200
    wait_until(lambda: (r := client.get_run(conductor))["status"] == "paused" and r["dudePause"] == "conductor",
               timeout=30, message="the conductor was never parked")
    assert client.get(f"/v1/tasks/{task['id']}").json()["status"] == before[0]

    # A third message resumes it, timed as a conductor's resume, and it answers.
    resp = client.post(f"/v1/tasks/{task['id']}/chat", {"text": "what about the tests?"})
    assert resp.status_code == 200 and resp.json()["runId"] == conductor, resp.text
    wait_until(lambda: _answer(client, conductor, said, "what about the tests?"), timeout=60,
               message="the parked conductor never answered")
    resumes = query(owner_dsn, "SELECT cause FROM run_resumes WHERE run_id = %s", (conductor,))
    assert [r["cause"] for r in resumes][resumed:] and {r["cause"] for r in resumes[resumed:]} == {"conductor"}, resumes

    # Nothing about the task moved: its status, Runs, workflow and branch.
    assert _state(client, owner_dsn, task["id"], fake_github, branch) == before
    assert len([r for r in client.task_runs(task["id"]) if r["role"] == "conductor"]) == 1


def test_an_empty_message_is_refused(client: ApiClient, forge_project: dict):
    task = client.create_task(forge_project["id"], "Nothing to say")
    assert client.post(f"/v1/tasks/{task['id']}/chat", {"text": "  "}).status_code == 400
    assert [r for r in client.task_runs(task["id"]) if r["role"] == "conductor"] == []
