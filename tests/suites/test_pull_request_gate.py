"""Before the pull request: the project's choice, a task's override, and
Deliver's hold for its owner under Ask me first.

Through the public API, with the fake lux's scripted agents and the fake
GitHub (`fake_github.py`). The owner is the key the `client` fixture holds;
Bo is another person in the organisation.
"""

from __future__ import annotations

from fake_github import FakeGitHub
from helpers import ApiClient, create_api_key, query, wait_until


def _pull_requests(client: ApiClient, task_id: str) -> list[dict]:
    return client.get("/v1/pull-requests", params={"taskId": task_id}).json()["pullRequests"]


def _task(client: ApiClient, task_id: str) -> dict:
    return client.get(f"/v1/tasks/{task_id}").json()


def _held(client: ApiClient, task_id: str) -> dict:
    return wait_until(
        lambda: (t := _task(client, task_id))["status"] == "awaiting_input"
        and (t.get("awaitingDecision") or {}).get("point") == "before_pull_request" and t,
        timeout=120, message="Deliver never held before the pull request",
    )


def _set_project_choice(client: ApiClient, project_id: str, choice: str | None) -> None:
    resp = client.patch(f"/v1/projects/{project_id}/settings", {"delivery": {"pullRequest": choice}})
    assert resp.status_code == 200, resp.text


def test_the_choice_is_the_projects_and_never_the_organisations(client: ApiClient, forge_project: dict):
    resp = client.patch("/v1/settings/organization", {"delivery": {"pullRequest": "ask"}})
    assert resp.status_code == 400, resp.text
    read = client.get(f"/v1/projects/{forge_project['id']}/settings").json()["delivery"]["pullRequest"]
    assert read == {"value": "open", "source": "default"}
    _set_project_choice(client, forge_project["id"], "draft")
    read = client.get(f"/v1/projects/{forge_project['id']}/settings").json()["delivery"]["pullRequest"]
    assert read == {"value": "draft", "source": "project"}


def test_open_as_draft_opens_a_draft_and_asks_nobody(client: ApiClient, forge_project: dict, fake_github: FakeGitHub):
    _set_project_choice(client, forge_project["id"], "draft")
    task = client.create_task(forge_project["id"], "Draft it")
    assert client.post(f"/v1/tasks/{task['id']}/deliver").status_code == 201
    pr = wait_until(lambda: _pull_requests(client, task["id"]), timeout=90, message="no pull request")[0]
    assert fake_github.pulls[pr["number"]].draft is True
    assert _task(client, task["id"])["pullRequestOpened"] is True
    awaited = [e for e in client.events(taskId=task["id"]) if e["eventType"] == "conductor.decision_awaited"]
    assert awaited == [], awaited


def test_ask_me_first_holds_until_its_owner_opens_it_and_feedback_goes_round_first(
    client: ApiClient, env, org: dict, forge_project: dict, fake_github: FakeGitHub
):
    """Held with nothing on GitHub; Bo cannot answer; Bo's feedback goes to a fixer and back to the hold; then Open."""
    task = client.create_task(forge_project["id"], "Hold it for me", pullRequestChoice="ask")
    assert task["pullRequestChoice"] == "ask"
    assert client.post(f"/v1/tasks/{task['id']}/deliver").status_code == 201

    held = _held(client, task["id"])
    assert held["hold"]["round"] == 1, held["hold"]
    assert [r["name"] for r in held["hold"]["repos"]] == ["greeter"], held["hold"]
    assert _pull_requests(client, task["id"]) == [] and len(fake_github.pulls) == 0

    bo = ApiClient(env.control_plane_url, create_api_key(env.owner_dsn, org["id"], name="Bo"))
    refused = bo.post(f"/v1/tasks/{task['id']}/decider", {"decider": "policy", "openPullRequest": True, "draft": False})
    assert refused.status_code == 403, refused.text
    refused = bo.put(f"/v1/tasks/{task['id']}/pull-request-choice", {"choice": "open"})
    assert refused.status_code == 403, refused.text

    # Bo's feedback at the hold goes to a fixer; nothing reaches GitHub.
    fixes_before = len([r for r in client.task_runs(task["id"]) if r["phase"] == "fix"])
    draft = bo.post(f"/v1/tasks/{task['id']}/feedback/drafts",
                    {"body": "Say hello in French too.", "anchor": {"kind": "general"}})
    assert draft.status_code == 201, draft.text
    item = draft.json()
    submitted = bo.post(f"/v1/tasks/{task['id']}/feedback/submit", {"draftIds": [item["id"]], "submissionKey": "gate-1"})
    assert submitted.status_code == 201, submitted.text

    def fixers_given() -> list[dict]:
        return [r for r in client.task_runs(task["id"]) if r["phase"] == "fix"
                and query(env.owner_dsn, "SELECT 1 FROM runs WHERE id = %s AND %s = ANY(feedback_item_ids)", (r["id"], item["id"]))]

    wait_until(fixers_given, timeout=60, message="feedback at the hold was given to no fixer")
    assert len([r for r in client.task_runs(task["id"]) if r["phase"] == "fix"]) > fixes_before
    assert _task(client, task["id"])["status"] == "running"
    # The scripted fixer commits but never resolves the item through dude_feedback_resolve, so a person's reply
    # is what settles it: without it the review/fix loop runs out and the hold becomes an escalation
    # (orchestrator TestAFixerThatLeavesTheItemOpenEscalatesFromTheHold).
    assert bo.post(f"/v1/feedback/{item['id']}/reply", {"body": "Bonjour is enough."}).status_code == 200

    # Back at the hold, round 2, once the item is handled.
    again = wait_until(
        lambda: (t := _task(client, task["id"]))["status"] == "awaiting_input" and (t.get("hold") or {}).get("round") == 2 and t,
        timeout=180, message="the fix round never came back to the hold",
    )
    assert again["hold"]["commitsSince"] >= 1, again["hold"]
    assert len(fake_github.pulls) == 0

    resp = client.post(f"/v1/tasks/{task['id']}/decider", {"decider": "policy", "openPullRequest": True, "draft": False})
    assert resp.status_code == 200, resp.text
    pr = wait_until(lambda: _pull_requests(client, task["id"]), timeout=90, message="no pull request after Open")[0]
    assert fake_github.pulls[pr["number"]].draft is False
    locked = client.put(f"/v1/tasks/{task['id']}/pull-request-choice", {"choice": "draft"})
    assert locked.status_code == 409, locked.text


def test_done_without_a_pull_request_keeps_the_branch(client: ApiClient, env, org: dict, forge_project: dict,
                                                      fake_github: FakeGitHub):
    _set_project_choice(client, forge_project["id"], "ask")
    task = client.create_task(forge_project["id"], "Nothing to open")
    assert client.post(f"/v1/tasks/{task['id']}/deliver").status_code == 201
    _held(client, task["id"])
    branch = f"dude/{task['id']}/attempt-1"
    head = fake_github.branch_sha(branch)
    assert head

    bo = ApiClient(env.control_plane_url, create_api_key(env.owner_dsn, org["id"], name="Bo"))
    assert bo.post(f"/v1/tasks/{task['id']}/done-without-pull-request").status_code == 403

    resp = client.post(f"/v1/tasks/{task['id']}/done-without-pull-request")
    assert resp.status_code == 200, resp.text
    done = wait_until(lambda: (t := _task(client, task["id"]))["status"] == "done" and t, timeout=30,
                      message="the task never finished")
    assert done["resolution"] == "nothing_to_merge"
    assert fake_github.branch_sha(branch) == head
    assert len(fake_github.pulls) == 0
    assert [e for e in client.events(taskId=task["id"]) if e["eventType"] == "task.done_without_pull_request"]


def test_changing_ask_me_first_while_held_opens_the_pull_request(client: ApiClient, forge_project: dict,
                                                                 fake_github: FakeGitHub):
    task = client.create_task(forge_project["id"], "Changed my mind", pullRequestChoice="ask")
    assert client.post(f"/v1/tasks/{task['id']}/deliver").status_code == 201
    _held(client, task["id"])
    resp = client.put(f"/v1/tasks/{task['id']}/pull-request-choice", {"choice": "draft"})
    assert resp.status_code == 200, resp.text
    pr = wait_until(lambda: _pull_requests(client, task["id"]), timeout=90, message="no pull request after the change")[0]
    assert fake_github.pulls[pr["number"]].draft is True
