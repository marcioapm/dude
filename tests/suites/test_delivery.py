"""Delivery, end to end through the deployed processes.

The backend receives the request, the orchestrator runs the workflow, agents
run on a stand-in for lux, and their work lands on a stand-in for GitHub
backed by a real git repository. What happens inside the orchestrator is
pinned by its own tests (orchestrator/delivery_test.go); this suite pins the
seams between the processes: that a user's request reaches the orchestrator,
that the orchestrator's refusals reach the user, and that what the
orchestrator records is what the API and the live stream show.
"""

from __future__ import annotations

import pytest

from fake_github import FakeGitHub
from helpers import ApiClient, wait_until


def test_work_on_no_repository_is_delivered_as_what_the_agents_publish(client: ApiClient):
    """A task that names no repository, in a project with none, is work
    that changes no code: it runs, and ends with what the agents published
    for a person to read, not a pull request."""
    import os

    project = client.create_project(
        name="Notes", slug=f"notes-{os.urandom(3).hex()}", runtimeImage="dude-runtime:test",
        agentModels={r: {"model": "fake/scripted"} for r in ("implementer", "reviewer", "simplifier")},
    )
    task = client.create_task(project["id"], "Write up the options")
    assert client.post(f"/v1/tasks/{task['id']}/deliver").status_code == 201
    wait_until(lambda: client.get("/v1/artifacts", params={"taskId": task["id"]}).json()["artifacts"],
               timeout=60, message="nothing was published")
    assert client.get("/v1/pull-requests", params={"taskId": task["id"]}).json()["pullRequests"] == []

    # A person reads it and says it is done; there is nothing to merge.
    wait_until(lambda: client.get(f"/v1/tasks/{task['id']}").json()["status"] == "review",
               timeout=30, message="the work never came to review")
    wait_until(lambda: client.post(f"/v1/tasks/{task['id']}/done").status_code == 200,
               timeout=30, message="it could not be marked done")
    assert client.get(f"/v1/tasks/{task['id']}").json()["status"] == "done"


def test_delivering_a_missing_task_is_a_404(client: ApiClient):
    resp = client.post("/v1/tasks/wi_does_not_exist/deliver")
    assert resp.status_code == 404


def test_another_organization_cannot_deliver_my_task(
    client: ApiClient, forge_project: dict, second_org: dict
):
    """The backend names the user's organization; the orchestrator's queries
    are confined to it, so someone else's task does not exist for them."""
    task = client.create_task(forge_project["id"], "Mine")
    resp = second_org["client"].post(f"/v1/tasks/{task['id']}/deliver")
    assert resp.status_code == 404


def test_delivering_twice_joins_the_first(client: ApiClient, forge_project: dict):
    """The task is the idempotency key, so a second call must not race."""
    task = client.create_task(forge_project["id"], "Deliver me once")

    first = client.post(f"/v1/tasks/{task['id']}/deliver")
    assert first.status_code == 201, first.text

    second = client.post(f"/v1/tasks/{task['id']}/deliver")
    assert second.status_code == 200
    assert second.json()["alreadyRunning"] is True
    assert second.json()["workflowRunId"] == first.json()["workflowRunId"]


def test_the_review_fix_loop_converges_and_the_ledger_shows_it(
    client: ApiClient, forge_project: dict, fake_github: FakeGitHub
):
    """Implement → review (a blocking finding) → fix → clean review → simplify.

    Everything the UI renders comes from the API and the event stream; the
    orchestrator writes it. So this reads it the way the UI does.
    """
    task = client.create_task(forge_project["id"], "Loop until clean")
    client.post(f"/v1/tasks/{task['id']}/deliver")

    wait_until(
        lambda: any(r["phase"] == "simplify" and r["status"] == "completed" for r in client.task_runs(task["id"])),
        timeout=60,
        message="the loop never converged to simplify",
    )
    phases = [(r["phase"], r["status"]) for r in client.task_runs(task["id"])]
    assert phases == [
        ("implement", "completed"),
        ("review", "completed"),
        ("fix", "completed"),
        ("review", "completed"),
        ("simplify", "completed"),
    ], phases

    findings = client.get("/v1/findings", params={"taskId": task["id"]}).json()["findings"]
    assert [f["severity"] for f in findings] == ["blocking"]
    # Resolved by the clean re-review after the fix, which is what let the
    # loop converge rather than stop at its bound.
    assert findings[0]["status"] == "resolved"

    implement = next(r for r in client.task_runs(task["id"]) if r["phase"] == "implement")
    types = [e["eventType"] for e in client.events(runId=implement["id"])]
    for expected in ("agent.session.started", "agent.message", "git.commit_created", "run.completed"):
        assert expected in types, f"{expected} missing from the implementer's timeline: {types}"


def test_work_across_two_repositories_opens_a_pull_request_in_each(
    client: ApiClient, forge_project: dict, fake_github: FakeGitHub
):
    """One task, two repositories it changes: the implementer is given
    both, commits in each, and each gets its own pull request, naming the
    other. The task is done only when both are merged."""
    web = fake_github.add_repository("web")
    repo = client.post(f"/v1/projects/{forge_project['id']}/repositories",
                       {"name": "web", "url": web.clone_url, "defaultBranch": "main"}).json()
    api = client.get(f"/v1/projects/{forge_project['id']}").json()["repositories"]
    target = next(r for r in api if r["name"] != "web")
    item = client.create_task(forge_project["id"], "Across two", repositories=[
        {"id": target["id"], "access": "write"}, {"id": repo["id"], "access": "write"}])
    assert client.post(f"/v1/tasks/{item['id']}/deliver").status_code == 201

    def both_open():
        prs = client.get("/v1/pull-requests", params={"taskId": item["id"]}).json()["pullRequests"]
        return prs if len(prs) == 2 else None

    prs = wait_until(both_open, timeout=90, interval=0.5, message="no pull request in each repository")
    assert {p["repositoryId"] for p in prs} == {target["id"], repo["id"]}
    assert {p["repositoryName"] for p in prs} == {target["name"], "web"}
    assert len(fake_github.pulls) == 1 and len(web.pulls) == 1
    assert "web" in fake_github.pulls[1].body and fake_github.owner in web.pulls[1].body

    def state_of(repository_id: str) -> str:
        prs = client.get("/v1/pull-requests", params={"taskId": item["id"]}).json()["pullRequests"]
        return next(p["state"] for p in prs if p["repositoryId"] == repository_id)

    web.merge(1)
    wait_until(lambda: state_of(repo["id"]) == "merged", timeout=30, message="web's merge never registered")
    assert client.get(f"/v1/tasks/{item['id']}").json()["status"] != "done"
    fake_github.merge(1)
    wait_until(lambda: client.get(f"/v1/tasks/{item['id']}").json()["status"] == "done",
               timeout=30, message="the task never finished with both merged")


def test_what_an_agent_publishes_is_listed_and_read_only_by_its_organization(
    client: ApiClient, forge_project: dict, second_org: dict
):
    """An agent writes a file into $LUX_ARTIFACTS; lux collects it when the
    container exits; the orchestrator records it; the API lists it with the
    task and streams its bytes from lux."""
    task = client.create_task(forge_project["id"], "Leave notes")
    client.post(f"/v1/tasks/{task['id']}/deliver")

    def published():
        found = client.get("/v1/artifacts", params={"taskId": task["id"]}).json()["artifacts"]
        return found or None

    artifacts = wait_until(published, timeout=60, message="the implementer's notes were never recorded")
    assert [(a["name"], a["phase"], a["role"]) for a in artifacts] == [("NOTES.md", "implement", "implementer")], artifacts
    notes = artifacts[0]
    assert notes["contentType"].startswith("text/markdown") and notes["sizeBytes"] > 0

    content = client.get(f"/v1/artifacts/{notes['id']}/content")
    assert content.status_code == 200, content.text
    assert content.text.startswith("# What changed")
    assert int(content.headers["content-length"]) == notes["sizeBytes"]
    # An agent's file is never run as one of our pages.
    assert "sandbox" in content.headers["content-security-policy"]

    other = second_org["client"]
    assert other.get("/v1/artifacts", params={"taskId": task["id"]}).json()["artifacts"] == []
    assert other.get(f"/v1/artifacts/{notes['id']}/content").status_code == 404

    types = [e["eventType"] for e in client.events(taskId=task["id"])]
    assert "artifact.created" in types


def test_a_project_names_the_reviewers_every_delivery_runs(client: ApiClient, forge_project: dict):
    """Set on the project in the backend, honoured by the orchestrator."""
    resp = client.patch(f"/v1/projects/{forge_project['id']}",
                        {"deliveryPolicy": {"requiredReviewers": ["correctness", "security"]}})
    assert resp.status_code == 200, resp.text
    assert resp.json()["deliveryPolicy"] == {"requiredReviewers": ["correctness", "security"]}

    task = client.create_task(forge_project["id"], "Reviewed twice over")
    client.post(f"/v1/tasks/{task['id']}/deliver")
    wait_until(
        lambda: {r["category"] for r in client.task_runs(task["id"]) if r["phase"] == "review"}
        >= {"correctness", "security"},
        timeout=60,
        message="the project's required security reviewer never ran",
    )


def test_a_project_policy_names_only_reviewers_the_factory_has(client: ApiClient, forge_project: dict):
    resp = client.patch(f"/v1/projects/{forge_project['id']}",
                        {"deliveryPolicy": {"requiredReviewers": ["vibes"]}})
    assert resp.status_code == 400, resp.text


def test_an_agent_asks_a_person_waits_and_carries_on_with_the_answer(client: ApiClient, forge_project: dict):
    """The question reaches the API and the sidebar; parked while it waits, the answer resumes it."""
    # The suite parks a waiting agent after seconds (DUDE_PARK_AFTER).
    resp = client.patch(f"/v1/projects/{forge_project['id']}", {"agentModels": {
        "implementer": {"model": "fake/ask"}, "reviewer": {"model": "fake/scripted"},
        "simplifier": {"model": "fake/scripted"}}})
    assert resp.status_code == 200, resp.text
    task = client.create_task(forge_project["id"], "Ask first")
    client.post(f"/v1/tasks/{task['id']}/deliver")

    def open_questions():
        return client.get("/v1/questions").json()["questions"]

    wait_until(lambda: open_questions(), timeout=30, message="the agent's question never reached the API")
    question = open_questions()[0]
    assert question["prompt"] == "Should FACTORY.md be in English?"
    assert question["options"] == ["yes", "no"]
    assert client.get(f"/v1/tasks/{task['id']}").json()["status"] == "awaiting_input"

    # The sidebar shows who is asking, and what, without opening the chat.
    nav = client.get("/v1/navigation").json()
    sessions = [s for p in nav["projects"] for wi in p.get("tasks", []) if wi["id"] == task["id"]
                for run in wi["runs"] for s in run["sessions"]]
    assert any(s["status"] == "awaiting_input" and s.get("activity") == question["prompt"] for s in sessions), sessions

    # Not answered within the grace period: parked, holding nothing.
    wait_until(
        lambda: any(r["phase"] == "implement" and r["status"] == "paused" for r in client.task_runs(task["id"])),
        timeout=30, message="the waiting agent was never parked",
    )
    run = next(r for r in client.task_runs(task["id"]) if r["phase"] == "implement")
    types = [e.get("eventType", e.get("type")) for e in client.events(runId=run["id"])]
    assert "run.parked" in types, types

    resp = client.post(f"/v1/questions/{question['id']}/answer", {"text": "yes"})
    assert resp.status_code == 200, resp.text
    assert open_questions() == []
    wait_until(
        lambda: any(r["phase"] == "implement" and r["status"] == "completed" for r in client.task_runs(task["id"])),
        timeout=30, message="the implementer never carried on after the answer",
    )


def test_steer_pause_resume_and_abort_reach_the_agent(client: ApiClient, forge_project: dict):
    """Run control goes user → backend → orchestrator → lux, and back as events."""
    # An agent that never finishes its turn, to have something live to control.
    client.patch(f"/v1/projects/{forge_project['id']}", {"agentModels": {"implementer": {"model": "fake/hang"}}})
    task = client.create_task(forge_project["id"], "Hold on")
    client.post(f"/v1/tasks/{task['id']}/deliver")

    run = wait_until(
        lambda: next((r for r in client.task_runs(task["id"]) if r["status"] == "running"), None),
        timeout=30,
        message="the implementer never started",
    )

    # Interrupting: the agent is mid-turn, and without it would only hear
    # this when its turn ends.
    resp = client.post(f"/v1/runs/{run['id']}/steer", {"text": "also add a test", "interrupt": True})
    assert resp.status_code == 201, resp.text
    directive = resp.json()
    wait_until(
        lambda: any(d["deliveredAt"] for d in client.get(f"/v1/runs/{run['id']}/directives").json()["directives"]),
        timeout=20,
        message="the directive was never delivered to the agent",
    )
    assert directive["text"] == "also add a test"

    assert client.post(f"/v1/runs/{run['id']}/pause", {}).status_code == 200
    wait_until(
        lambda: client.get(f"/v1/runs/{run['id']}").json()["status"] == "paused",
        timeout=20,
        message="the run never paused",
    )
    # Refusals come from the orchestrator and keep their meaning.
    assert client.post(f"/v1/runs/{run['id']}/pause", {}).status_code == 409

    assert client.post(f"/v1/runs/{run['id']}/resume", {}).status_code == 200
    wait_until(
        lambda: client.get(f"/v1/runs/{run['id']}").json()["status"] == "running",
        timeout=20,
        message="the run never resumed",
    )

    assert client.post(f"/v1/runs/{run['id']}/abort", {"reason": "changed my mind"}).status_code == 200
    assert client.get(f"/v1/runs/{run['id']}").json()["status"] == "aborted"
    assert client.get(f"/v1/tasks/{task['id']}").json()["status"] == "aborted"
    types = [e["eventType"] for e in client.events(runId=run["id"])]
    for expected in ("run.steered", "run.directive.delivered", "run.paused", "run.resumed", "run.aborted"):
        assert expected in types, f"{expected} missing: {types}"


def test_a_runner_key_cannot_reach_the_product_api(env, org: dict):
    """Runner keys belonged to the retired runner protocol. One left behind
    must not work as a user key."""
    from helpers import create_api_key

    runner = ApiClient(env.control_plane_url, create_api_key(env.owner_dsn, org["id"], kind="runner"))
    assert runner.get("/v1/navigation").status_code == 401


@pytest.mark.parametrize("path", ["/internal/tasks/x/deliver", "/internal/kick"])
def test_the_orchestrator_refuses_callers_without_the_service_token(env, path: str):
    import requests

    resp = requests.post(env.orchestrator_url + path, json={}, headers={"x-dude-organization": "org"}, timeout=5)
    assert resp.status_code == 401
