"""Lifecycle, end to end through the deployed processes
(docs/design/tracking-work.md, Epic 1).

A person closes a task with a resolution and puts work away. Close and
Archive are forwarded by the backend to the orchestrator, which owns their
rules; every start path refuses a closed or archived task through the one
guard. These pin what a person relies on: each resolution closes, a
duplicate hands its blocking over, Close and archive is one write, every
refusal says why by its code, Reopen brings a task back, and archived work
leaves navigation and the agents' list_tasks.
"""

from __future__ import annotations

import json

from helpers import ApiClient, execute, query, wait_until


def _task(client: ApiClient, project: dict, title: str) -> dict:
    return client.create_task(project["id"], title)


def _get(client: ApiClient, task_id: str) -> dict:
    return client.get(f"/v1/tasks/{task_id}").json()


def _refused(resp, status: int, code: str) -> dict:
    assert resp.status_code == status, resp.text
    error = resp.json()["error"]
    assert error["code"] == code, error
    return error


def _blocks(owner_dsn: str, org: str, blocker: str, task: str) -> None:
    execute(owner_dsn, """INSERT INTO task_relations (organization_id, from_task_id, to_task_id, kind, created_by_person)
        SELECT %s, %s, %s, 'blocks', person_id FROM task_people WHERE task_id = %s AND position = 0""", (org, blocker, task, task))


def _nav_keys(client: ApiClient) -> set[str]:
    keys: set[str] = set()
    for p in client.get("/v1/navigation").json()["projects"]:
        for t in p["tasks"] + [t for e in p["epics"] for t in e["tasks"]]:
            keys.add(t["key"])
    return keys


def test_each_resolution_closes_and_a_duplicate_hands_its_blocking_over(client: ApiClient, project: dict, org: dict, owner_dsn: str):
    wont, cant = _task(client, project, "Not needed"), _task(client, project, "Doesn't happen")
    assert client.post(f"/v1/tasks/{wont['id']}/close", {"resolution": "wont_do", "note": "superseded"}).status_code == 200
    assert client.post(f"/v1/tasks/{cant['id']}/close", {"resolution": "cant_reproduce"}).status_code == 200
    assert (_get(client, wont["id"])["status"], _get(client, wont["id"])["resolution"]) == ("aborted", "wont_do")
    assert _get(client, cant["id"])["resolution"] == "cant_reproduce"
    # Nothing to merge is Mark done's, not Close's.
    _refused(client.post(f"/v1/tasks/{_task(client, project, 'x')['id']}/close", {"resolution": "nothing_to_merge"}), 400, "bad_request")

    canonical, dup, waiting = _task(client, project, "CSV drops rows"), _task(client, project, "CSV again"), _task(client, project, "Export")
    _blocks(owner_dsn, org["id"], dup["id"], waiting["id"])
    resp = client.post(f"/v1/tasks/{dup['id']}/close", {"resolution": "duplicate", "duplicateOf": canonical["key"]})
    assert resp.status_code == 200, resp.text
    assert resp.json()["handedOver"] == [waiting["key"]]
    assert _get(client, dup["id"])["duplicateOf"] == canonical["key"]
    # What waited on the duplicate now waits on the task it duplicates: a start says so.
    blocked = _refused(client.post(f"/v1/tasks/{waiting['id']}/deliver", {}), 409, "blocked")
    assert blocked["details"]["blockers"] == [{"key": canonical["key"], "status": "received"}]
    relations = client.get(f"/v1/tasks/{canonical['id']}/relations").json()
    assert [d["task"]["key"] for d in relations["duplicatedBy"]] == [dup["key"]]
    assert relations["duplicatedBy"][0]["handedOver"] == [waiting["key"]]
    closed = [e for e in client.events(taskId=dup["id"], limit=200) if e["eventType"] == "task.closed"]
    assert closed[0]["payload"] == {"resolution": "duplicate", "duplicateOf": canonical["key"], "handedOver": [waiting["key"]]}

    # Reopen: back to Backlog, the edge gone, the dependant waits on the duplicate itself again.
    assert client.post(f"/v1/tasks/{dup['id']}/reopen").status_code == 200
    task = _get(client, dup["id"])
    assert (task["status"], task["resolution"], task["duplicateOf"]) == ("received", None, None)
    blocked = _refused(client.post(f"/v1/tasks/{waiting['id']}/deliver", {}), 409, "blocked")
    assert blocked["details"]["blockers"] == [{"key": dup["key"], "status": "received"}]


def test_close_and_archive_is_one_write(client: ApiClient, project: dict, owner_dsn: str):
    task = _task(client, project, "Billing page")
    _refused(client.post(f"/v1/tasks/{task['id']}/archive", {}), 409, "not_ended")
    assert _get(client, task["id"])["archivedAt"] is None
    resp = client.post(f"/v1/tasks/{task['id']}/archive", {"resolution": "wont_do", "note": "not this quarter"})
    assert resp.status_code == 200, resp.text
    got = _get(client, task["id"])
    assert (got["status"], got["resolution"]) == ("aborted", "wont_do") and got["archivedAt"]
    events = [e for e in client.events(taskId=task["id"], limit=200) if e["eventType"] in ("task.closed", "task.archived")]
    assert [e["eventType"] for e in events] == ["task.closed", "task.archived"]
    assert events[0]["payload"]["andArchived"] is True
    assert events[1]["payload"] == {"viaClose": True} and events[1]["causationId"] == events[0]["eventId"]
    # One writing transaction: both events carry its id.
    writes = query(owner_dsn, """SELECT count(DISTINCT xmin::text) AS n FROM events WHERE task_id = %s
        AND event_type IN ('task.closed', 'task.archived')""", (task["id"],))
    assert writes == [{"n": 1}]
    # Archived: Reopen waits for Unarchive; the two are separate steps.
    _refused(client.post(f"/v1/tasks/{task['id']}/reopen"), 409, "archived")
    assert client.post(f"/v1/tasks/{task['id']}/unarchive").status_code == 200
    assert _get(client, task["id"])["resolution"] == "wont_do"
    assert client.post(f"/v1/tasks/{task['id']}/reopen").status_code == 200
    assert _get(client, task["id"])["status"] == "received"


def test_every_refusal_says_why_by_its_code(client: ApiClient, project: dict, org: dict, owner_dsn: str):
    # Running and in review are not refused: Close and Archive stop what is live and close.
    running, review = _task(client, project, "Running"), _task(client, project, "In review")
    execute(owner_dsn, "UPDATE tasks SET status = 'running' WHERE id = %s", (running["id"],))
    execute(owner_dsn, "UPDATE tasks SET status = 'review' WHERE id = %s", (review["id"],))
    assert client.post(f"/v1/tasks/{running['id']}/close", {"resolution": "wont_do"}).status_code == 200
    assert client.post(f"/v1/tasks/{review['id']}/archive", {"resolution": "wont_do"}).status_code == 200
    assert (_get(client, running["id"])["resolution"], _get(client, review["id"])["resolution"]) == ("wont_do", "wont_do")

    # loop: closing as a duplicate whose handover would make a task wait on itself.
    canonical, dup, waiting = _task(client, project, "A"), _task(client, project, "B"), _task(client, project, "C")
    _blocks(owner_dsn, org["id"], dup["id"], waiting["id"])
    _blocks(owner_dsn, org["id"], waiting["id"], canonical["id"])
    loop = _refused(client.post(f"/v1/tasks/{dup['id']}/close", {"resolution": "duplicate", "duplicateOf": canonical["key"]}), 409, "loop")
    assert loop["details"]["loop"][0] == loop["details"]["loop"][-1] and canonical["key"] in loop["details"]["loop"]

    # unfinished: an epic with a task not ended; withEndedTasks archives the ended ones and keeps the epic.
    epic = client.post(f"/v1/projects/{project['id']}/epics", {"title": "Webhooks"}).json()
    done, open_ = _task(client, project, "Done one"), _task(client, project, "Open one")
    for t in (done, open_):
        assert client.patch(f"/v1/tasks/{t['id']}", {"epicId": epic["id"]}).status_code == 200
    assert client.post(f"/v1/tasks/{done['id']}/close", {"resolution": "wont_do"}).status_code == 200
    unfinished = _refused(client.post(f"/v1/epics/{epic['id']}/archive", {}), 409, "unfinished")
    assert [t["key"] for t in unfinished["details"]["tasks"]] == [open_["key"]]
    resp = client.post(f"/v1/epics/{epic['id']}/archive", {"withEndedTasks": True})
    assert resp.status_code == 200 and resp.json() == {"epicId": epic["id"], "archived": False, "tasks": [done["key"]]}

    # archived: a closed and archived task is not closed again.
    _refused(client.post(f"/v1/tasks/{done['id']}/close", {"resolution": "wont_do"}), 409, "archived")


def test_a_start_is_refused_on_a_closed_or_archived_task(client: ApiClient, project: dict):
    closed, archived = _task(client, project, "Closed"), _task(client, project, "Archived")
    assert client.post(f"/v1/tasks/{closed['id']}/close", {"resolution": "wont_do"}).status_code == 200
    assert client.post(f"/v1/tasks/{archived['id']}/archive", {"resolution": "cant_reproduce"}).status_code == 200
    words = f"{closed['key']} was closed as Won't do: reopen it to start it."
    for path, body in ((f"/v1/tasks/{closed['id']}/deliver", {}), (f"/v1/tasks/{closed['id']}/deliver", {"override": True}),
                       (f"/v1/tasks/{closed['id']}/talk", {}), (f"/v1/tasks/{closed['id']}/runs", None)):
        assert _refused(client.post(path, body), 409, "closed")["message"] == words
    for path in (f"/v1/tasks/{archived['id']}/deliver", f"/v1/tasks/{archived['id']}/talk", f"/v1/tasks/{archived['id']}/runs"):
        assert _refused(client.post(path, {}), 409, "archived")["message"] == f"{archived['key']} is archived: unarchive it to start it."
    refusals = [e["payload"] for e in client.events(taskId=closed["id"], limit=200) if e["eventType"] == "task.start_refused"]
    assert {(r["path"], r["reason"]) for r in refusals} == {("deliver", "closed"), ("talk", "closed"), ("create_run", "closed")}
    assert client.task_runs(closed["id"]) == []


def test_archived_tasks_leave_navigation_and_list_tasks(client: ApiClient, forge_project: dict, owner_dsn: str):
    models = {**forge_project["agentModels"], **client.on_models({"conductor": "fake/scripted"})}
    assert client.patch(f"/v1/projects/{forge_project['id']}", {"agentModels": models}).status_code == 200
    archived, closed, here = (_task(client, forge_project, t) for t in ("Put away", "Closed, not archived", "The conductor's"))
    assert client.post(f"/v1/tasks/{archived['id']}/archive", {"resolution": "wont_do"}).status_code == 200
    assert client.post(f"/v1/tasks/{closed['id']}/close", {"resolution": "cant_reproduce"}).status_code == 200

    keys = _nav_keys(client)
    assert archived["key"] not in keys and closed["key"] in keys
    nav = client.get("/v1/navigation").json()["projects"]
    project = next(p for p in nav if p["id"] == forge_project["id"])
    assert project["archivedCount"] == 1
    assert next(t for t in project["tasks"] if t["key"] == closed["key"])["resolution"] == "cant_reproduce"
    listed = client.get("/v1/tasks", params={"projectId": forge_project["id"]}).json()["tasks"]
    assert archived["key"] not in {t["key"] for t in listed}
    assert [t["key"] for t in client.get("/v1/tasks", params={"projectId": forge_project["id"], "archived": "1"}).json()["tasks"]] == [archived["key"]]

    # The agents' list_tasks: archived left out unless asked, each task's resolution said.
    resp = client.post(f"/v1/tasks/{here['id']}/chat", {"text": "\n".join([
        "Look around.", 'tool: list_tasks {}', 'tool: list_tasks {"archived": true}'])})
    assert resp.status_code in (200, 201), resp.text
    runs = [resp.json()["runId"]]

    def calls():
        found = [e for e in client.events(runId=runs[0], limit=1000) if e["eventType"] == "agent.tool.dude" and e["payload"]["tool"] == "list_tasks"]
        return found if len(found) >= 2 else None

    found = wait_until(calls, timeout=90, message="the conductor never called list_tasks twice")
    everyday, everything = (json.loads(json.dumps(c["payload"]["result"])) for c in found[:2])
    keyed = lambda r: {t["key"]: t for t in r["tasks"]}
    assert archived["key"] not in keyed(everyday)
    assert keyed(everyday)[closed["key"]]["resolution"] == "cant_reproduce"
    assert keyed(everything)[archived["key"]]["archived"] is True
    assert keyed(everything)[archived["key"]]["resolution"] == "wont_do"
    assert query(owner_dsn, "SELECT count(*) AS n FROM tasks WHERE id = %s AND archived_at IS NOT NULL", (archived["id"],))[0]["n"] == 1
