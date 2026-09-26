"""The navigation tree the sidebar reads.

One request must return everything the panel renders across every project,
because the alternative — a request per task — is what makes a sidebar
feel slow at fifty items. These tests pin the shape, the nesting, and the
tenant boundary.
"""

from __future__ import annotations

from helpers import ApiClient, execute, new_id


def _navigation(client: ApiClient) -> list[dict]:
    resp = client.get("/v1/navigation")
    assert resp.status_code == 200, f"navigation failed: {resp.status_code} {resp.text}"
    return resp.json()["projects"]


def _find(projects: list[dict], project_id: str) -> dict:
    return next(p for p in projects if p["id"] == project_id)


def test_requires_authentication(env):
    import requests

    resp = requests.get(f"{env.control_plane_url}/v1/navigation", timeout=10)
    assert resp.status_code == 401


def test_returns_projects_with_their_tasks(client: ApiClient, project: dict):
    client.create_task(project["id"], "Visible in the tree")

    found = _find(_navigation(client), project["id"])
    assert found["name"] == project["name"]
    assert [w["title"] for w in found["tasks"]] == ["Visible in the tree"]


def test_tasks_carry_the_status_the_tree_triages_on(client: ApiClient, project: dict):
    task = client.create_task(project["id"], "Triaged")

    found = _find(_navigation(client), project["id"])
    row = next(w for w in found["tasks"] if w["id"] == task["id"])
    # The sidebar maps status onto a triage bucket; a missing status would
    # silently become the calm default.
    assert row["status"] == task["status"]


def test_epics_group_their_tasks(client: ApiClient, project: dict, owner_dsn: str, org: dict):
    # Epics have no create endpoint yet, so seed one directly.
    epic_id = new_id("epic")
    execute(
        owner_dsn,
        "INSERT INTO epics (id, organization_id, project_id, title) VALUES (%s, %s, %s, %s)",
        (epic_id, org["id"], project["id"], "Webhook reliability"),
    )

    grouped = client.create_task(project["id"], "Inside the epic", epicId=epic_id)
    client.create_task(project["id"], "Outside the epic")

    found = _find(_navigation(client), project["id"])
    assert len(found["epics"]) == 1
    epic = found["epics"][0]
    assert epic["title"] == "Webhook reliability"
    assert [w["id"] for w in epic["tasks"]] == [grouped["id"]]

    # A task in an epic must not also appear at the project level, or
    # the tree would show it twice and count it twice.
    assert grouped["id"] not in [w["id"] for w in found["tasks"]]
    assert "Outside the epic" in [w["title"] for w in found["tasks"]]


def test_each_run_of_an_attempt_is_an_agent_under_it(client: ApiClient, project: dict):
    """Phased delivery puts several Runs in one attempt; each is an agent row."""
    task = client.create_task(project["id"], "With a run")
    run = client.create_run(task["id"])

    found = _find(_navigation(client), project["id"])
    row = next(w for w in found["tasks"] if w["id"] == task["id"])

    assert [r["attempt"] for r in row["runs"]] == [1]
    agents = row["runs"][0]["sessions"]
    # The tree answers "who is working on this" from these rows, so each Run
    # must appear, with a role the sidebar can draw an avatar for.
    assert [a["id"] for a in agents] == [run["id"]]
    assert agents[0]["role"]
    assert agents[0]["status"] == "pending"


def test_tasks_carry_time_in_status_and_spend(client: ApiClient, project: dict):
    """The board shows how long a card has sat in its lane, and what it cost."""
    task = client.create_task(project["id"], "Timed")

    found = _find(_navigation(client), project["id"])
    row = next(w for w in found["tasks"] if w["id"] == task["id"])
    assert row["statusSince"]
    assert row["costUsd"] == 0


def test_attempts_are_ordered_oldest_first(client: ApiClient, project: dict):
    task = client.create_task(project["id"], "Retried")
    first = client.create_run(task["id"])
    second = client.create_run(task["id"])

    found = _find(_navigation(client), project["id"])
    row = next(w for w in found["tasks"] if w["id"] == task["id"])

    # The tree treats the last one as current and folds the earlier ones.
    assert [r["attempt"] for r in row["runs"]] == [1, 2]
    assert [r["sessions"][0]["id"] for r in row["runs"]] == [first["id"], second["id"]]


def test_does_not_leak_another_organizations_tree(
    client: ApiClient, project: dict, second_org: dict
):
    client.create_task(project["id"], "Private to the first org")

    # The second org has projects of its own; it must see none of these, and
    # an org with nothing gets an empty tree rather than an error.
    assert _navigation(second_org["client"]) == []
    assert _find(_navigation(client), project["id"])["tasks"]
