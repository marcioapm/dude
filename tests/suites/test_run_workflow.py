"""run_workflow, end to end: the conductor dispatches a GitHub Actions
workflow, and dude follows its run to the end.

Through the deployed processes, against the stand-in GitHub
(`fake_github.py`). A scripted conductor makes the tool calls a person's
Chat message names. The run completes on GitHub with no webhook sent: dude
notices it by reading the run (level-triggered), and wakes the conductor
with the conclusion.
"""

from __future__ import annotations

import json

from fake_github import FakeGitHub
from helpers import ApiClient, query, wait_until

CI = "name: CI\non:\n  push:\n  pull_request:\n  workflow_dispatch:\njobs: {}\n"
LINT = "name: Lint\non: [push, pull_request]\njobs: {}\n"


def _call(name: str, **args) -> str:
    return f"tool: {name} {json.dumps(args)}"


def _say(client: ApiClient, task_id: str, *lines: str) -> None:
    resp = client.post(f"/v1/tasks/{task_id}/chat", {"text": "\n".join(lines)})
    assert resp.status_code in (200, 201), resp.text


def _approved(client: ApiClient, owner_dsn: str, task_id: str, conductor: str, workflow: str, ref: str) -> str:
    """The conductor asks the person with run_workflow's two choices, and
    the person picks the approving one: the question's id. With no task
    branch yet, a workflow on main is not the task's own CI."""
    approve = f"Run {workflow} on {ref}"
    _say(client, task_id, _call("ask_person", question=f"May I run {workflow} on {ref}?", choices=[approve, "Do not run it"]))
    question = wait_until(lambda: next(iter(query(owner_dsn, "SELECT id FROM questions WHERE run_id = %s AND status = 'open'",
                                                  (conductor,))), None), timeout=90, message="the conductor never asked")
    resp = client.post(f"/v1/questions/{question['id']}/answer", {"text": approve})
    assert resp.status_code == 200, resp.text
    return question["id"]


def _events(client: ApiClient, task_id: str, kind: str) -> list[dict]:
    return [e["payload"] for e in client.events(taskId=task_id, limit=1000) if e["eventType"] == kind]


def test_the_conductor_dispatches_a_workflow_and_is_woken_with_its_conclusion(
    client: ApiClient, forge_project: dict, fake_github: FakeGitHub, owner_dsn: str
):
    models = {**forge_project["agentModels"], **client.on_models({"conductor": "fake/defaults"})}
    assert client.patch(f"/v1/projects/{forge_project['id']}", {"agentModels": models}).status_code == 200
    assert client.patch(f"/v1/projects/{forge_project['id']}/settings",
                        {"delivery": {"conductorWarmMinutes": 30}}).status_code == 200
    fake_github.write_file("main", ".github/workflows/ci.yml", CI, "CI")
    fake_github.write_file("main", ".github/workflows/lint.yml", LINT, "Lint")
    task = client.create_task(forge_project["id"], "Run CI by hand")
    resp = client.post(f"/v1/tasks/{task['id']}/chat", {"text": "Let's talk first."})
    assert resp.status_code in (200, 201) and resp.json()["decider"] == "conductor", resp.text
    approved = _approved(client, owner_dsn, task["id"], resp.json()["runId"], "ci.yml", "main")

    # No workflow_dispatch: refused, nothing dispatched. ci.yml on main,
    # approved, it starts in the same turn, then waits on it.
    _say(client, task["id"], _call("run_workflow", workflow="lint.yml", ref="main"),
         _call("run_workflow", workflow="ci.yml", ref="main", questionId=approved),
         'tool: wait_for {"conditions": [{"workflowEnded": "{{last.dispatchId}}"}], "deadline": "1h"}')
    dispatched = wait_until(lambda: _events(client, task["id"], "workflow.dispatched"), timeout=90,
                            message="no workflow.dispatched event")[0]
    assert [d["workflow"] for d in fake_github.dispatches] == ["ci.yml"], fake_github.dispatches
    assert fake_github.dispatches[0]["ref"] == "main" and dispatched["workflow"] == "ci.yml"
    # GitHub named the run: bound at the dispatch, no guess.
    assert dispatched["runId"] == max(fake_github.dispatched_runs), dispatched
    wait_until(lambda: _events(client, task["id"], "conductor.waiting"), timeout=60, message="the conductor never waited")
    run_id = max(fake_github.dispatched_runs)

    # The run completes on GitHub; no webhook is sent.
    fake_github.complete_dispatched_run(run_id, "failure")
    woken = wait_until(lambda: next((p["text"] for p in _events(client, task["id"], "conductor.woken")
                                     if dispatched["dispatchId"] in p.get("text", "") and "completed: failure" in p["text"]), None),
                       timeout=120, message="the conductor was never woken with the run's conclusion")
    assert f"/actions/runs/{run_id}" in woken



def test_an_older_github_without_run_details_binds_the_one_new_run(
    client: ApiClient, forge_project: dict, fake_github: FakeGitHub, owner_dsn: str
):
    """GitHub (or GHES) answering the dispatch 204 with no run id: dude
    binds the one run that is new since the dispatch, and the conductor is
    woken with its conclusion the same way."""
    models = {**forge_project["agentModels"], **client.on_models({"conductor": "fake/defaults"})}
    assert client.patch(f"/v1/projects/{forge_project['id']}", {"agentModels": models}).status_code == 200
    assert client.patch(f"/v1/projects/{forge_project['id']}/settings",
                        {"delivery": {"conductorWarmMinutes": 30}}).status_code == 200
    fake_github.dispatch_legacy = True
    fake_github.write_file("main", ".github/workflows/ci.yml", CI, "CI")
    task = client.create_task(forge_project["id"], "Run CI by hand on an older GitHub")
    resp = client.post(f"/v1/tasks/{task['id']}/chat", {"text": "Let's talk first."})
    assert resp.status_code in (200, 201) and resp.json()["decider"] == "conductor", resp.text
    approved = _approved(client, owner_dsn, task["id"], resp.json()["runId"], "ci.yml", "main")
    _say(client, task["id"], _call("run_workflow", workflow="ci.yml", ref="main", questionId=approved))
    dispatched = wait_until(lambda: _events(client, task["id"], "workflow.dispatched"), timeout=90,
                            message="no workflow.dispatched event")[0]
    assert "runId" not in dispatched, dispatched
    assert fake_github.dispatches[0]["run_details"] is True, fake_github.dispatches
    run_id = max(fake_github.dispatched_runs)
    fake_github.complete_dispatched_run(run_id, "success")
    woken = wait_until(lambda: next((p["text"] for p in _events(client, task["id"], "conductor.woken")
                                     if dispatched["dispatchId"] in p.get("text", "") and "completed: success" in p["text"]), None),
                       timeout=120, message="the conductor was never woken with the bound run's conclusion")
    assert f"/actions/runs/{run_id}" in woken
