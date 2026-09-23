"""Multi-tenant isolation.

The most important property in the system: one organization must never
observe or affect another's data. These tests exist because row-level
security that is silently inert looks exactly like RLS that works — until it
does not.
"""

from __future__ import annotations

import psycopg
import pytest

from helpers import ApiClient


def test_projects_are_not_visible_across_organizations(client: ApiClient, second_org: dict):
    project = client.create_project(name="Private", slug="private-a")

    other: ApiClient = second_org["client"]
    listed = other.get("/v1/projects").json()["projects"]
    assert all(p["id"] != project["id"] for p in listed)


def test_project_cannot_be_fetched_by_id_across_organizations(client: ApiClient, second_org: dict):
    # Guessing an id must not help: RLS filters by row, not by listing.
    project = client.create_project(name="Private", slug="private-b")

    other: ApiClient = second_org["client"]
    assert other.get(f"/v1/projects/{project['id']}").status_code == 404


def test_work_items_cannot_be_created_in_another_organizations_project(
    client: ApiClient, second_org: dict
):
    project = client.create_project(name="Private", slug="private-c")

    other: ApiClient = second_org["client"]
    resp = other.post("/v1/work-items", {"projectId": project["id"], "title": "intrusion"})
    # 404 rather than 403: the project's existence is itself not disclosed.
    assert resp.status_code == 404


def test_events_are_not_visible_across_organizations(client: ApiClient, second_org: dict):
    client.create_project(name="Audited", slug="audited")

    other: ApiClient = second_org["client"]
    assert all(e["eventType"] != "project.created" for e in other.events())


def test_runs_are_not_visible_across_organizations(client: ApiClient, project: dict, second_org: dict):
    work_item = client.create_work_item(project["id"], "Private work")
    run = client.create_run(work_item["id"])

    other: ApiClient = second_org["client"]
    assert other.get(f"/v1/runs/{run['id']}").status_code == 404


def test_unauthenticated_requests_are_rejected(env):
    anonymous = ApiClient(env.control_plane_url, "")
    assert anonymous.get("/v1/projects").status_code == 401


def test_invalid_key_is_rejected(env):
    bogus = ApiClient(env.control_plane_url, "dude_sk_totally-made-up")
    assert bogus.get("/v1/projects").status_code == 401


def test_app_role_cannot_bypass_row_level_security(env):
    """RLS is only a boundary if the connecting role cannot bypass it.

    A superuser silently ignores every policy, so this asserts the property
    directly rather than trusting the policies exist.
    """
    with psycopg.connect(env.app_dsn, autocommit=True) as conn:
        row = conn.execute(
            "SELECT rolsuper, rolbypassrls FROM pg_roles WHERE rolname = current_user"
        ).fetchone()
        assert row == (False, False), "the application role must not be able to bypass RLS"


def test_app_role_sees_nothing_without_tenant_context(env, client: ApiClient):
    """With no organization set, tenant tables must return zero rows."""
    client.create_project(name="Exists", slug="exists")

    with psycopg.connect(env.app_dsn, autocommit=True) as conn:
        count = conn.execute("SELECT count(*) FROM projects").fetchone()[0]
        assert count == 0, "queries without tenant context must fail closed, not open"


def test_event_ledger_is_append_only(env, client: ApiClient):
    """History must not be rewritable by the application role."""
    client.create_project(name="Audited", slug="audit-trail")

    with psycopg.connect(env.app_dsn, autocommit=True) as conn:
        with pytest.raises(psycopg.errors.InsufficientPrivilege):
            conn.execute("UPDATE events SET event_type = 'tampered'")

    with psycopg.connect(env.app_dsn, autocommit=True) as conn:
        with pytest.raises(psycopg.errors.InsufficientPrivilege):
            conn.execute("DELETE FROM events")
