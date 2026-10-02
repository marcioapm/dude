"""Product hierarchy: projects, tasks, runs and sessions.

Covers the per-project, per-role agent model configuration, which is the knob
that decides which model runs in which role.
"""

from __future__ import annotations

from helpers import ApiClient


# ---------------------------------------------------------------------------
# Projects
# ---------------------------------------------------------------------------


def test_create_and_fetch_project(client: ApiClient):
    created = client.create_project(
        name="Customer Portal",
        slug="customer-portal",
        description="the portal",
        repositories=[{"name": "web", "url": "https://example.com/web.git"}],
    )

    fetched = client.get(f"/v1/projects/{created['id']}").json()
    assert fetched["name"] == "Customer Portal"
    assert [r["name"] for r in fetched["repositories"]] == ["web"]


def test_duplicate_slug_is_rejected(client: ApiClient):
    client.create_project(name="First", slug="taken")
    resp = client.post("/v1/projects", {"name": "Second", "slug": "taken"})
    assert resp.status_code == 409


def test_slug_must_be_url_safe(client: ApiClient):
    resp = client.post("/v1/projects", {"name": "Bad", "slug": "Not A Slug"})
    assert resp.status_code == 400


def test_agent_models_round_trip_as_an_object(client: ApiClient):
    """Guards against the config being stored double-encoded as a string.

    When that happens every per-role lookup silently misses and roles fall
    back to defaults, which is hard to notice and easy to reintroduce.
    """
    models = {"orchestrator": {"tier": client.tier_for("claude-opus-5"), "costLimitUsd": 5}}
    created = client.create_project(name="Models", slug="models", agentModels=models)

    assert isinstance(created["agentModels"], dict)
    assert created["agentModels"]["orchestrator"] == models["orchestrator"]


def test_agent_models_can_be_replaced(client: ApiClient):
    project = client.create_project(
        name="Models", slug="models-update", agentModels=client.on_models({"orchestrator": "old"})
    )

    new = client.on_models({"orchestrator": "new"})
    resp = client.patch(f"/v1/projects/{project['id']}", {"agentModels": new})
    assert resp.status_code == 200
    assert resp.json()["agentModels"] == new


def test_a_role_names_a_tier_never_a_model(client: ApiClient):
    resp = client.post("/v1/projects", {"name": "M", "slug": "m-model", "agentModels": {"orchestrator": {"model": "claude-opus-5"}}})
    assert resp.status_code == 400
    assert "a role names a model tier" in resp.json()["error"]["message"]


# ---------------------------------------------------------------------------
# Tasks and runs
# ---------------------------------------------------------------------------


def test_task_starts_in_received(client: ApiClient, project: dict):
    task = client.create_task(
        project["id"], "Add a health endpoint", goal="Expose /health so the load balancer can check the service.",
        acceptanceCriteria=["returns 200", "has a test"],
    )
    assert task["status"] == "received"
    assert task["acceptanceCriteria"] == ["returns 200", "has a test"]


def test_creating_a_run_queues_the_task(client: ApiClient, project: dict):
    task = client.create_task(project["id"], "Queue me")
    run = client.create_run(task["id"])

    assert run["attempt"] == 1
    assert run["status"] == "pending"
    assert client.get(f"/v1/tasks/{task['id']}").json()["status"] == "queued"


def test_retrying_creates_a_new_attempt_without_erasing_the_first(client: ApiClient, project: dict):
    """A Run is one attempt; retrying must preserve the prior one for
    inspection and for attempt-level cost and duration (plan §39)."""
    task = client.create_task(project["id"], "Retry me")

    first = client.create_run(task["id"])
    second = client.create_run(task["id"])

    assert (first["attempt"], second["attempt"]) == (1, 2)

    runs = client.get(f"/v1/tasks/{task['id']}").json()["runs"]
    assert {r["id"] for r in runs} == {first["id"], second["id"]}


def test_task_for_unknown_project_is_rejected(client: ApiClient):
    resp = client.post("/v1/tasks", {"projectId": "prj_nonexistent", "title": "orphan", "goal": client.DEFAULT_GOAL})
    assert resp.status_code == 404


def test_tasks_can_be_filtered_by_project(client: ApiClient, project: dict):
    other = client.create_project(name="Other", slug="other-proj")
    client.create_task(project["id"], "In project")
    client.create_task(other["id"], "In other")

    listed = client.get("/v1/tasks", params={"projectId": project["id"]}).json()["tasks"]
    assert [w["title"] for w in listed] == ["In project"]


# ---------------------------------------------------------------------------
# Per-role model resolution
# ---------------------------------------------------------------------------


def test_session_uses_the_project_model_for_the_role(client: ApiClient, project: dict):
    task = client.create_task(project["id"], "Model resolution")
    run = client.create_run(task["id"])

    resp = client.create_session(run["id"], "orchestrator")
    assert resp.status_code == 201
    assert resp.json()["model"] == "project-orchestrator"


def test_session_falls_back_to_the_organization_default(client: ApiClient, project: dict):
    """The project configures no reviewer, so the org default applies."""
    task = client.create_task(project["id"], "Fallback")
    run = client.create_run(task["id"])

    resp = client.create_session(run["id"], "reviewer")
    assert resp.status_code == 201
    assert resp.json()["model"] == "org-default-reviewer"


def test_a_role_on_a_tier_with_no_model_is_refused_saying_so(client: ApiClient, project: dict):
    """Better to refuse than to silently pick an arbitrary model."""
    task = client.create_task(project["id"], "Unconfigured")
    run = client.create_run(task["id"])
    # The project's implementer is on its own tier; the organization's Coder
    # names no model, so moving it there leaves it with none.
    coder = next(t for t in client.get("/v1/models/tiers").json()["tiers"] if t["name"] == "Coder")
    client.patch(f"/v1/projects/{project['id']}/settings", {"roles": {"implementer": {"tier": coder["id"]}}})

    resp = client.create_session(run["id"], "implementer")
    assert resp.status_code == 400
    assert resp.json()["error"]["message"] == "the implementer runs on Coder, which names no model yet. An admin sets it in Models."


def test_explicit_tier_overrides_configuration(client: ApiClient, project: dict):
    task = client.create_task(project["id"], "Override")
    run = client.create_run(task["id"])

    resp = client.create_session(run["id"], "orchestrator", tier=client.tier_for("explicit-model"))
    assert resp.json()["model"] == "explicit-model"


def test_project_harness_preference_is_honoured(client: ApiClient, project: dict):
    task = client.create_task(project["id"], "Harness")
    run = client.create_run(task["id"])

    resp = client.create_session(run["id"], "implementer")
    assert resp.json()["harness"] == "opencode"


def test_invalid_role_is_rejected(client: ApiClient, project: dict):
    task = client.create_task(project["id"], "Bad role")
    run = client.create_run(task["id"])

    assert client.create_session(run["id"], "not_a_role").status_code == 400


# ---------------------------------------------------------------------------
# Session tree
# ---------------------------------------------------------------------------


def test_subagents_are_linked_to_their_parent(client: ApiClient, project: dict):
    task = client.create_task(project["id"], "Session tree")
    run = client.create_run(task["id"])

    parent = client.create_session(run["id"], "orchestrator").json()
    child = client.create_session(
        run["id"], "reviewer", parentSessionId=parent["id"]
    ).json()

    fetched = client.get(f"/v1/sessions/{parent['id']}").json()
    assert [c["id"] for c in fetched["children"]] == [child["id"]]


def test_run_exposes_its_sessions(client: ApiClient, project: dict):
    task = client.create_task(project["id"], "Run sessions")
    run = client.create_run(task["id"])
    client.create_session(run["id"], "orchestrator")
    client.create_session(run["id"], "implementer")

    sessions = client.get_run(run["id"])["sessions"]
    assert {s["role"] for s in sessions} == {"orchestrator", "implementer"}
