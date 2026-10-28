"""Independent conductor children through the deployed tool and phase paths."""

import json
import re

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


def _tool_answers(client, run_id, profile):
    """start_agent calls the conductor made that dude answered, for a profile."""
    answers = []
    for event in client.events(runId=run_id, limit=1000):
        payload = event["payload"]
        if event["eventType"] != "agent.tool.dude" or payload.get("tool") != "start_agent":
            continue
        if (payload.get("arguments") or {}).get("profile") == profile:
            result = payload.get("result") or {}
            answers.append(json.loads(result) if isinstance(result, str) else result)
    return answers


def _refused(client, run_id, tool):
    """The scripted conductor's replies that say dude refused a call of tool."""
    return [t for t in _said(client, run_id) if re.search(rf"dude {tool} answered 4\d\d", t)]


def test_child_report_returns_once_without_moving_delivery(client: ApiClient, owner_dsn, forge_project):
    task, parent = _world(client, forge_project)
    before = client.get(f"/v1/tasks/{task['id']}").json()
    _say(client, task["id"], "Investigate separately.", _tool("start_agent", {"profile": "investigator", "key": "report"}))
    child = wait_until(lambda: _started(client, owner_dsn, task, parent), timeout=60)
    wait_until(lambda: query(owner_dsn, "SELECT * FROM child_results WHERE child_run_id=%s", (child["id"],)), timeout=120)
    wait_until(lambda: query(owner_dsn, "SELECT * FROM child_result_outbox WHERE child_run_id=%s AND consumed_at IS NOT NULL", (child["id"],)), timeout=90)
    wait_until(lambda: query(owner_dsn, "SELECT d.id, d.text, d.delivered_at FROM child_result_outbox o JOIN directives d ON d.id=o.directive_id WHERE o.child_run_id=%s AND d.delivered_at IS NOT NULL", (child["id"],)), timeout=90)
    directive = query(owner_dsn, "SELECT d.id, d.text, d.delivered_at FROM child_result_outbox o JOIN directives d ON d.id=o.directive_id WHERE o.child_run_id=%s", (child["id"],))
    assert len(directive) == 1 and directive[0]["delivered_at"] is not None
    assert "childId" in directive[0]["text"] and "investigator" in directive[0]["text"]
    after = client.get(f"/v1/tasks/{task['id']}").json()
    assert after["status"] == before["status"]
    assert _point(client, task["id"]) == "after_implement"
    results = query(owner_dsn, "SELECT * FROM child_results WHERE child_run_id=%s", (child["id"],))
    assert len(results) == 1
    assert query(owner_dsn, "SELECT id FROM artifacts WHERE run_id=%s", (child["id"],))
    assert len(query(owner_dsn, "SELECT * FROM child_result_outbox WHERE child_run_id=%s", (child["id"],))) == 1
    assert len(query(owner_dsn, "SELECT d.id FROM child_result_outbox o JOIN directives d ON d.id=o.directive_id WHERE o.child_run_id=%s", (child["id"],))) == 1


def test_child_writer_moves_branch_and_returns_commit(client: ApiClient, owner_dsn, forge_project, fake_github: FakeGitHub):
    task, parent = _world(client, forge_project)
    _say(client, task["id"], "Delegate implementation.", _tool("start_agent", {"profile": "implementer", "key": "write"}))
    child = wait_until(lambda: _started(client, owner_dsn, task, parent), timeout=60)
    wait_until(lambda: client.get_run(child["id"])["status"] in ("completed", "failed"), timeout=120)
    assert client.get_run(child["id"])["status"] == "completed"
    branch = f"dude/{task['id']}/attempt-1"
    head = wait_until(lambda: fake_github.branch_sha(branch), timeout=60)
    result = wait_until(lambda: query(owner_dsn, "SELECT commits FROM child_results WHERE child_run_id=%s", (child["id"],)), timeout=120)[0]
    assert head in result["commits"].values(), query(owner_dsn, "SELECT phase,status,push_branch,lux_pushes,base_shas,push_result,heads FROM runs WHERE id=%s", (child["id"],))
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
    # dude's own answers: the first implementer and the investigator are
    # started, the second implementer is refused while the first holds the
    # task's one writer slot.
    investigator_answer = wait_until(lambda: _tool_answers(client, parent, "investigator"), timeout=90,
                                     message="the investigator start was never answered")[0]
    refusals = wait_until(lambda: _refused(client, parent, "start_agent"), timeout=60,
                          message="the second implementer was never refused")
    assert len(refusals) == 1, refusals
    implementer_answers = _tool_answers(client, parent, "implementer")
    assert len(implementer_answers) == 1, implementer_answers
    investigator_id = investigator_answer["runId"]
    assert investigator_answer["profile"] == "investigator", investigator_answer
    # The investigator hangs on purpose: it is the live sibling the abort of
    # the writer must leave running. Delivery of a result is the first test.
    writer = next(row for row in _children(owner_dsn, task) if row["phase"] == "implement")
    sibling = next(row for row in _children(owner_dsn, task) if row["id"] == investigator_id)
    response = client.post(f"/v1/runs/{writer['id']}/abort", {})
    assert response.status_code == 200, response.text
    wait_until(lambda: client.get_run(writer["id"])["status"] == "aborted", timeout=60)
    assert client.get_run(sibling["id"])["status"] not in ("aborted", "failed", "completed")
    assert client.get(f"/v1/tasks/{task['id']}").json()["status"] not in ("aborted", "failed", "done")


def test_cohort_staggered_completion_and_individual_inspection(client: ApiClient, owner_dsn, forge_project):
    task, parent = _world(client, forge_project)
    models = {**forge_project["agentModels"], **client.on_models({
        "conductor": "fake/scripted", "investigator": "fake/hang"})}
    assert client.patch(f"/v1/projects/{forge_project['id']}", {"agentModels": models}).status_code == 200
    _say(client, task["id"], "Run three independent investigations as one cohort.",
         _tool("start_agents", {"key": "staggered", "runs": [
             {"profile": "investigator", "prompt": f"Investigate area {i}; report evidence and stop."}
             for i in range(3)]}))
    members = wait_until(lambda: (rows if len(rows := _children(owner_dsn, task)) == 3 else None), timeout=60)
    wait_until(lambda: all(client.get_run(r["id"])["status"] == "running" for r in members), timeout=90)
    cohort = query(owner_dsn, "SELECT id FROM child_cohorts WHERE parent_run_id=%s", (parent,))[0]["id"]
    for i, member in enumerate(members):
        response = client.post(f"/v1/runs/{member['id']}/abort", {})
        assert response.status_code == 200, response.text
        wait_until(lambda: query(owner_dsn, "SELECT * FROM child_result_outbox WHERE child_run_id=%s AND consumed_at IS NOT NULL",
                                 (member["id"],)), timeout=90)
        directives = query(owner_dsn, "SELECT d.id,d.text,d.delivered_at FROM child_cohorts c JOIN directives d ON d.id=c.directive_id WHERE c.id=%s", (cohort,))
        assert len(directives) == (1 if i == 2 else 0), directives
        # The first two outbox receipts are consumed without parent inputs.
        receipts = query(owner_dsn, "SELECT directive_id FROM child_result_outbox WHERE child_run_id=%s", (member["id"],))
        assert bool(receipts[0]["directive_id"]) == (i == 2)
    wait_until(lambda: query(owner_dsn, "SELECT d.id FROM child_cohorts c JOIN directives d ON d.id=c.directive_id WHERE c.id=%s AND d.delivered_at IS NOT NULL", (cohort,)), timeout=90)
    for member in members:
        # read_run deliberately withholds its body from the tool ledger. The
        # scripted agent records selected fields from the actual tool answer.
        _say(client, task["id"], "Inspect the settled child evidence.",
             _tool("read_run", {"run": member["id"]}),
             _tool("emit_event", {"type": "cohort.inspected", "data": {
                 "run": "{{last.run}}", "status": "{{last.status}}",
                 "settled": "{{last.settled}}", "resultStatus": "{{last.result.status}}"}}))
        def inspected():
            for event in client.events(runId=parent, limit=1000):
                payload = event["payload"]
                if event["eventType"] == "agent.custom.cohort.inspected" and payload["data"].get("run") == member["id"]:
                    return payload["data"]
            return None
        result = wait_until(inspected, timeout=60)
        assert result["run"] == member["id"] and result["settled"] == "true", result
        assert result["status"] == result["resultStatus"] == "aborted", result
    assert len(query(owner_dsn, "SELECT id FROM directives WHERE run_id=%s AND text LIKE %s", (parent, f"Cohort {cohort} settled:%"))) == 1
    assert _point(client, task["id"]) == "after_implement"
