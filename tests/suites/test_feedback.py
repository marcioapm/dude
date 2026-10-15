"""People's feedback on a task, kept in dude: drafted privately, submitted
together, routed to whoever drives the task, and holding the pull request
until each item is handled.

Through the public API, with the fake lux's scripted agents and the fake
GitHub that delivers signed webhooks.
"""

from __future__ import annotations

import time

from fake_github import FakeGitHub
from helpers import ApiClient, create_api_key, query, wait_until


def _pull_requests(client: ApiClient, task_id: str) -> list[dict]:
    return client.get("/v1/pull-requests", params={"taskId": task_id}).json()["pullRequests"]


def _task(client: ApiClient, task_id: str) -> dict:
    return client.get(f"/v1/tasks/{task_id}").json()


def _listed(client: ApiClient, task_id: str, item_id: str) -> dict | None:
    return next((f for f in client.get(f"/v1/tasks/{task_id}/feedback").json()["feedback"] if f["id"] == item_id), None)


def _item(client: ApiClient, task_id: str, item_id: str) -> dict:
    found = _listed(client, task_id, item_id)
    assert found is not None, f"{item_id} is not listed"
    return found


def test_a_draft_is_private_and_a_batch_at_the_pull_request_wait_holds_it_until_resolved(
    client: ApiClient, env, org: dict, forge_project: dict, fake_github: FakeGitHub
):
    task = client.create_task(forge_project["id"], "Take feedback in dude")
    assert client.post(f"/v1/tasks/{task['id']}/deliver").status_code == 201
    pr = wait_until(lambda: _pull_requests(client, task["id"]), timeout=60, message="no pull request")[0]
    wait_until(lambda: _task(client, task["id"])["status"] == "review", timeout=30, message="never in review")

    bo = ApiClient(env.control_plane_url, create_api_key(env.owner_dsn, org["id"], name="Bo"))
    draft = client.post(f"/v1/tasks/{task['id']}/feedback/drafts",
                        {"body": "Name the flag --dry-run, as git does.", "anchor": {"kind": "general"}})
    assert draft.status_code == 201, draft.text
    item = draft.json()
    # Bo neither sees nor submits Ana's draft.
    assert _listed(bo, task["id"], item["id"]) is None
    refused = bo.post(f"/v1/tasks/{task['id']}/feedback/submit", {"draftIds": [item["id"]], "submissionKey": "bo-1"})
    assert refused.status_code == 400, refused.text
    assert "Name the flag" not in refused.text

    fixes_before = len([r for r in client.task_runs(task["id"]) if r["phase"] == "fix"])
    submitted = client.post(f"/v1/tasks/{task['id']}/feedback/submit", {"draftIds": [item["id"]], "submissionKey": "s-1"})
    assert submitted.status_code == 201, submitted.text
    batch = submitted.json()
    # A retry of the same submit is the same batch.
    again = client.post(f"/v1/tasks/{task['id']}/feedback/submit", {"draftIds": [item["id"]], "submissionKey": "s-1"})
    assert again.status_code == 200 and again.json()["batchId"] == batch["batchId"], again.text
    assert batch["routedTo"] == "workflow"
    assert _item(bo, task["id"], item["id"])["body"] == "Name the flag --dry-run, as git does."

    # A fixer is given it, in the person's words.
    fixer = wait_until(lambda: next((r for r in client.task_runs(task["id"]) if r["phase"] == "fix"
                                     and query(env.owner_dsn, "SELECT 1 FROM runs WHERE id = %s AND %s = ANY(feedback_item_ids)",
                                               (r["id"], item["id"]))), None),
                       timeout=60, message="no fixer given the item")
    assert len([r for r in client.task_runs(task["id"]) if r["phase"] == "fix"]) == fixes_before + 1
    wait_until(lambda: client.get_run(fixer["id"])["status"] == "completed", timeout=60, message="the fixer never finished")

    # Approved, but the item is unanswered: not ready, and dude will not merge.
    fake_github.approve(pr["number"])
    wait_until(lambda: _item(client, task["id"], item["id"])["status"] == "open", timeout=30, message="item not open again")
    merge = client.post(f"/v1/pull-requests/{pr['id']}/merge")
    assert merge.status_code == 409 and "feedback" in merge.text, merge.text
    assert _task(client, task["id"])["status"] != "ready_to_merge"
    assert client.get(f"/v1/tasks/{task['id']}/feedback").json()["blocking"] == 1

    # Bo replies; it no longer holds anything.
    assert bo.post(f"/v1/feedback/{item['id']}/reply", {"body": "git calls it --dry-run too."}).status_code == 200
    wait_until(lambda: _task(client, task["id"])["status"] == "ready_to_merge", timeout=30,
               message="a replied item still holds the pull request")
    history = _item(client, task["id"], item["id"])["history"]
    assert [h["kind"] for h in history] == ["reply"], history

    # Nothing in the ledger carries an item's words.
    leaked = query(env.owner_dsn, "SELECT count(*) AS n FROM events WHERE task_id = %s AND payload::text LIKE %s",
                   (task["id"], "%Name the flag%"))
    assert leaked[0]["n"] == 0


def test_a_working_implementer_hears_feedback_at_its_next_step(client: ApiClient, env, project: dict):
    client.patch(f"/v1/projects/{project['id']}", {"agentModels": client.on_models({"implementer": "fake/hang"})})
    task = client.create_task(project["id"], "Hear feedback while working")
    assert client.post(f"/v1/tasks/{task['id']}/deliver").status_code == 201
    impl = wait_until(lambda: next((r for r in client.task_runs(task["id"]) if r["phase"] == "implement"
                                    and r["status"] == "running"), None), timeout=30, message="no implementer at work")
    item = client.post(f"/v1/tasks/{task['id']}/feedback/drafts",
                       {"body": "Keep the old endpoint as an alias.", "anchor": {"kind": "general"}}).json()
    submitted = client.post(f"/v1/tasks/{task['id']}/feedback/submit", {"draftIds": [item["id"]], "submissionKey": "w-1"})
    assert submitted.status_code == 201, submitted.text
    assert submitted.json()["routedTo"] == "steer" and submitted.json()["runId"] == impl["id"]
    directives = client.get(f"/v1/runs/{impl['id']}/directives").json()["directives"]
    assert len(directives) == 1 and "> Keep the old endpoint as an alias." in directives[0]["text"]
    assert item["id"] in directives[0]["text"]
    # Never interrupting: fake/hang never ends its turn, so it has not read
    # it, and the item is not counted as given to it until it does.
    assert query(env.owner_dsn, "SELECT interrupt FROM directives WHERE id = %s", (directives[0]["id"],))[0]["interrupt"] is False
    time.sleep(2)
    assert _item(client, task["id"], item["id"])["status"] == "open"
    assert client.get(f"/v1/tasks/{task['id']}/feedback").json()["blocking"] == 1
