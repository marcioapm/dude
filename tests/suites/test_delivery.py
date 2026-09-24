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


def test_delivery_requires_a_repository(client: ApiClient, project: dict):
    """The orchestrator's refusal reaches the user, in the API's own shape."""
    work_item = client.create_work_item(project["id"], "Nowhere to push")

    resp = client.post(f"/v1/work-items/{work_item['id']}/deliver")
    assert resp.status_code == 400
    assert "repository" in resp.json()["error"]["message"].lower()


def test_delivering_a_missing_work_item_is_a_404(client: ApiClient):
    resp = client.post("/v1/work-items/wi_does_not_exist/deliver")
    assert resp.status_code == 404


def test_another_organization_cannot_deliver_my_work_item(
    client: ApiClient, forge_project: dict, second_org: dict
):
    """The backend names the user's organization; the orchestrator's queries
    are confined to it, so someone else's work item does not exist for them."""
    work_item = client.create_work_item(forge_project["id"], "Mine")
    resp = second_org["client"].post(f"/v1/work-items/{work_item['id']}/deliver")
    assert resp.status_code == 404


def test_delivering_twice_joins_the_first(client: ApiClient, forge_project: dict):
    """The work item is the idempotency key, so a second call must not race."""
    work_item = client.create_work_item(forge_project["id"], "Deliver me once")

    first = client.post(f"/v1/work-items/{work_item['id']}/deliver")
    assert first.status_code == 201, first.text

    second = client.post(f"/v1/work-items/{work_item['id']}/deliver")
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
    work_item = client.create_work_item(forge_project["id"], "Loop until clean")
    client.post(f"/v1/work-items/{work_item['id']}/deliver")

    wait_until(
        lambda: any(r["phase"] == "simplify" and r["status"] == "completed" for r in client.work_item_runs(work_item["id"])),
        timeout=60,
        message="the loop never converged to simplify",
    )
    phases = [(r["phase"], r["status"]) for r in client.work_item_runs(work_item["id"])]
    assert phases == [
        ("implement", "completed"),
        ("review", "completed"),
        ("fix", "completed"),
        ("review", "completed"),
        ("simplify", "completed"),
    ], phases

    findings = client.get("/v1/findings", params={"workItemId": work_item["id"]}).json()["findings"]
    assert [f["severity"] for f in findings] == ["blocking"]
    # Resolved by the clean re-review after the fix, which is what let the
    # loop converge rather than stop at its bound.
    assert findings[0]["status"] == "resolved"

    implement = next(r for r in client.work_item_runs(work_item["id"]) if r["phase"] == "implement")
    types = [e["eventType"] for e in client.events(runId=implement["id"])]
    for expected in ("agent.session.started", "agent.message", "git.commit_created", "run.completed"):
        assert expected in types, f"{expected} missing from the implementer's timeline: {types}"


def test_a_project_names_the_reviewers_every_delivery_runs(client: ApiClient, forge_project: dict):
    """Set on the project in the backend, honoured by the orchestrator."""
    resp = client.patch(f"/v1/projects/{forge_project['id']}",
                        {"deliveryPolicy": {"requiredReviewers": ["correctness", "security"]}})
    assert resp.status_code == 200, resp.text
    assert resp.json()["deliveryPolicy"] == {"requiredReviewers": ["correctness", "security"]}

    work_item = client.create_work_item(forge_project["id"], "Reviewed twice over")
    client.post(f"/v1/work-items/{work_item['id']}/deliver")
    wait_until(
        lambda: {r["category"] for r in client.work_item_runs(work_item["id"]) if r["phase"] == "review"}
        >= {"correctness", "security"},
        timeout=60,
        message="the project's required security reviewer never ran",
    )


def test_a_project_policy_names_only_reviewers_the_factory_has(client: ApiClient, forge_project: dict):
    resp = client.patch(f"/v1/projects/{forge_project['id']}",
                        {"deliveryPolicy": {"requiredReviewers": ["vibes"]}})
    assert resp.status_code == 400, resp.text


def test_an_agent_asks_a_person_waits_and_carries_on_with_the_answer(client: ApiClient, forge_project: dict):
    """The question reaches the API and the sidebar; the answer reaches the agent."""
    resp = client.patch(f"/v1/projects/{forge_project['id']}", {"agentModels": {
        "implementer": {"model": "fake/ask"}, "reviewer": {"model": "fake/scripted"},
        "simplifier": {"model": "fake/scripted"}}})
    assert resp.status_code == 200, resp.text
    work_item = client.create_work_item(forge_project["id"], "Ask first")
    client.post(f"/v1/work-items/{work_item['id']}/deliver")

    def open_questions():
        return client.get("/v1/questions").json()["questions"]

    wait_until(lambda: open_questions(), timeout=30, message="the agent's question never reached the API")
    question = open_questions()[0]
    assert question["prompt"] == "Should FACTORY.md be in English?"
    assert question["options"] == ["yes", "no"]
    assert client.get(f"/v1/work-items/{work_item['id']}").json()["status"] == "awaiting_input"

    # The sidebar shows who is asking, and what, without opening the chat.
    nav = client.get("/v1/navigation").json()
    sessions = [s for p in nav["projects"] for wi in p.get("workItems", []) if wi["id"] == work_item["id"]
                for run in wi["runs"] for s in run["sessions"]]
    assert any(s["status"] == "awaiting_input" and s.get("activity") == question["prompt"] for s in sessions), sessions

    resp = client.post(f"/v1/questions/{question['id']}/answer", {"text": "yes"})
    assert resp.status_code == 200, resp.text
    assert open_questions() == []
    wait_until(
        lambda: any(r["phase"] == "implement" and r["status"] == "completed" for r in client.work_item_runs(work_item["id"])),
        timeout=30, message="the implementer never carried on after the answer",
    )


def test_steer_pause_resume_and_abort_reach_the_agent(client: ApiClient, forge_project: dict):
    """Run control goes user → backend → orchestrator → lux, and back as events."""
    # An agent that never finishes its turn, to have something live to control.
    client.patch(f"/v1/projects/{forge_project['id']}", {"agentModels": {"implementer": {"model": "fake/hang"}}})
    work_item = client.create_work_item(forge_project["id"], "Hold on")
    client.post(f"/v1/work-items/{work_item['id']}/deliver")

    run = wait_until(
        lambda: next((r for r in client.work_item_runs(work_item["id"]) if r["status"] == "running"), None),
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
    assert client.get(f"/v1/work-items/{work_item['id']}").json()["status"] == "aborted"
    types = [e["eventType"] for e in client.events(runId=run["id"])]
    for expected in ("run.steered", "run.directive.delivered", "run.paused", "run.resumed", "run.aborted"):
        assert expected in types, f"{expected} missing: {types}"


def test_a_runner_key_cannot_reach_the_product_api(env, org: dict):
    """Runner keys belonged to the retired runner protocol. One left behind
    must not work as a user key."""
    from helpers import create_api_key

    runner = ApiClient(env.control_plane_url, create_api_key(env.owner_dsn, org["id"], kind="runner"))
    assert runner.get("/v1/navigation").status_code == 401


@pytest.mark.parametrize("path", ["/internal/work-items/x/deliver", "/internal/kick"])
def test_the_orchestrator_refuses_callers_without_the_service_token(env, path: str):
    import requests

    resp = requests.post(env.orchestrator_url + path, json={}, headers={"x-dude-organization": "org"}, timeout=5)
    assert resp.status_code == 401
