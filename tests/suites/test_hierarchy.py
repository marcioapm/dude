"""Product hierarchy: projects, work items, runs and sessions.

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
    models = {"orchestrator": {"model": "claude-opus-5", "costLimitUsd": 5}}
    created = client.create_project(name="Models", slug="models", agentModels=models)

    assert isinstance(created["agentModels"], dict)
    assert created["agentModels"]["orchestrator"]["model"] == "claude-opus-5"


def test_agent_models_can_be_replaced(client: ApiClient):
    project = client.create_project(
        name="Models", slug="models-update", agentModels={"orchestrator": {"model": "old"}}
    )

    resp = client.patch(
        f"/v1/projects/{project['id']}", {"agentModels": {"orchestrator": {"model": "new"}}}
    )
    assert resp.status_code == 200
    assert resp.json()["agentModels"]["orchestrator"]["model"] == "new"


# ---------------------------------------------------------------------------
# Work items and runs
# ---------------------------------------------------------------------------


def test_work_item_starts_in_received(client: ApiClient, project: dict):
    work_item = client.create_work_item(
        project["id"], "Add a health endpoint", goal="expose /health",
        acceptanceCriteria=["returns 200", "has a test"],
    )
    assert work_item["status"] == "received"
    assert work_item["acceptanceCriteria"] == ["returns 200", "has a test"]


def test_creating_a_run_queues_the_work_item(client: ApiClient, project: dict):
    work_item = client.create_work_item(project["id"], "Queue me")
    run = client.create_run(work_item["id"])

    assert run["attempt"] == 1
    assert run["status"] == "pending"
    assert client.get(f"/v1/work-items/{work_item['id']}").json()["status"] == "queued"


def test_retrying_creates_a_new_attempt_without_erasing_the_first(client: ApiClient, project: dict):
    """A Run is one attempt; retrying must preserve the prior one for
    inspection and for attempt-level cost and duration (plan §39)."""
    work_item = client.create_work_item(project["id"], "Retry me")

    first = client.create_run(work_item["id"])
    second = client.create_run(work_item["id"])

    assert (first["attempt"], second["attempt"]) == (1, 2)

    runs = client.get(f"/v1/work-items/{work_item['id']}").json()["runs"]
    assert {r["id"] for r in runs} == {first["id"], second["id"]}


def test_work_item_for_unknown_project_is_rejected(client: ApiClient):
    resp = client.post("/v1/work-items", {"projectId": "prj_nonexistent", "title": "orphan"})
    assert resp.status_code == 404


def test_work_items_can_be_filtered_by_project(client: ApiClient, project: dict):
    other = client.create_project(name="Other", slug="other-proj")
    client.create_work_item(project["id"], "In project")
    client.create_work_item(other["id"], "In other")

    listed = client.get("/v1/work-items", params={"projectId": project["id"]}).json()["workItems"]
    assert [w["title"] for w in listed] == ["In project"]


# ---------------------------------------------------------------------------
# Per-role model resolution
# ---------------------------------------------------------------------------


def test_session_uses_the_project_model_for_the_role(client: ApiClient, project: dict):
    work_item = client.create_work_item(project["id"], "Model resolution")
    run = client.create_run(work_item["id"])

    resp = client.create_session(run["id"], "orchestrator")
    assert resp.status_code == 201
    assert resp.json()["model"] == "project-orchestrator"


def test_session_falls_back_to_the_organization_default(client: ApiClient, project: dict):
    """The project configures no reviewer, so the org default applies."""
    work_item = client.create_work_item(project["id"], "Fallback")
    run = client.create_run(work_item["id"])

    resp = client.create_session(run["id"], "reviewer")
    assert resp.status_code == 201
    assert resp.json()["model"] == "org-default-reviewer"


def test_unconfigured_role_is_rejected_with_a_useful_message(client: ApiClient, project: dict):
    """Better to refuse than to silently pick an arbitrary model."""
    work_item = client.create_work_item(project["id"], "Unconfigured")
    run = client.create_run(work_item["id"])

    resp = client.create_session(run["id"], "qa_browser")
    assert resp.status_code == 400
    assert "qa_browser" in resp.json()["error"]["message"]


def test_explicit_model_overrides_configuration(client: ApiClient, project: dict):
    work_item = client.create_work_item(project["id"], "Override")
    run = client.create_run(work_item["id"])

    resp = client.create_session(run["id"], "orchestrator", model="explicit-model")
    assert resp.json()["model"] == "explicit-model"


def test_project_harness_preference_is_honoured(client: ApiClient, project: dict):
    work_item = client.create_work_item(project["id"], "Harness")
    run = client.create_run(work_item["id"])

    resp = client.create_session(run["id"], "implementer")
    assert resp.json()["harness"] == "opencode"


def test_invalid_role_is_rejected(client: ApiClient, project: dict):
    work_item = client.create_work_item(project["id"], "Bad role")
    run = client.create_run(work_item["id"])

    assert client.create_session(run["id"], "not_a_role").status_code == 400


# ---------------------------------------------------------------------------
# Session tree
# ---------------------------------------------------------------------------


def test_subagents_are_linked_to_their_parent(client: ApiClient, project: dict):
    work_item = client.create_work_item(project["id"], "Session tree")
    run = client.create_run(work_item["id"])

    parent = client.create_session(run["id"], "orchestrator").json()
    child = client.create_session(
        run["id"], "reviewer", parentSessionId=parent["id"]
    ).json()

    fetched = client.get(f"/v1/sessions/{parent['id']}").json()
    assert [c["id"] for c in fetched["children"]] == [child["id"]]


def test_run_exposes_its_sessions(client: ApiClient, project: dict):
    work_item = client.create_work_item(project["id"], "Run sessions")
    run = client.create_run(work_item["id"])
    client.create_session(run["id"], "orchestrator")
    client.create_session(run["id"], "implementer")

    sessions = client.get_run(run["id"])["sessions"]
    assert {s["role"] for s in sessions} == {"orchestrator", "implementer"}
