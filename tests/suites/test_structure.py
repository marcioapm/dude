"""Organising work: a project's repositories, its epics, and editing work items.

Metadata, served by the backend alone; these pin the rules a person relies
on — nothing is lost when a group goes away, what an agent is working to
cannot change under it, and work cannot be moved where it does not belong.
"""

from __future__ import annotations

import os

import pytest

from helpers import ApiClient, wait_until


def _project(client: ApiClient) -> dict:
    return client.create_project(name="Structure", slug=f"structure-{os.urandom(3).hex()}")


def _repo(client: ApiClient, project: dict, name: str) -> dict:
    resp = client.post(f"/v1/projects/{project['id']}/repositories",
                       {"name": name, "url": f"https://github.com/acme/{name}.git"})
    assert resp.status_code == 201, resp.text
    return resp.json()


def test_repositories_can_be_added_renamed_and_removed(client: ApiClient):
    project = _project(client)
    repo = _repo(client, project, "api")
    assert repo["defaultBranch"] == "main"
    other = _repo(client, project, "web")

    assert client.post(f"/v1/projects/{project['id']}/repositories",
                       {"name": "api", "url": "https://github.com/acme/other.git"}).status_code == 409
    # Renaming onto a name in use is the same conflict, not a crash.
    assert client.patch(f"/v1/repositories/{other['id']}", {"name": "api"}).status_code == 409

    resp = client.patch(f"/v1/repositories/{repo['id']}", {"defaultBranch": "develop", "trust": "untrusted_external"})
    assert resp.status_code == 200 and resp.json()["defaultBranch"] == "develop"
    assert resp.json()["trust"] == "untrusted_external"

    assert client.request("DELETE", f"/v1/repositories/{repo['id']}").status_code == 204
    assert [r["name"] for r in client.get(f"/v1/projects/{project['id']}").json()["repositories"]] == ["web"]


@pytest.mark.parametrize("name,url", [
    ("../etc", "https://github.com/acme/x.git"),       # a path, not a name
    ("x", "file:///etc/passwd"),                        # a local read
    ("x", "ext::sh -c touch% /tmp/pwned"),              # git's command transport
    ("x", "--upload-pack=touch /tmp/pwned"),            # an option
])
def test_a_repository_is_a_name_and_a_forge_url(client: ApiClient, name: str, url: str):
    project = _project(client)
    assert client.post(f"/v1/projects/{project['id']}/repositories", {"name": name, "url": url}).status_code == 400
    assert client.post("/v1/projects", {"name": "P", "slug": f"p-{os.urandom(3).hex()}",
                                        "repositories": [{"name": name, "url": url}]}).status_code == 400


def test_a_repository_in_use_cannot_be_moved_or_removed(client: ApiClient, forge_project: dict):
    repo = forge_project["repositories"][0]
    item = client.create_work_item(forge_project["id"], "Keep the repository")
    assert client.post(f"/v1/work-items/{item['id']}/deliver").status_code == 201
    wait_until(lambda: client.work_item_runs(item["id"]), timeout=15, message="the delivery never started")
    assert client.patch(f"/v1/repositories/{repo['id']}", {"url": "https://github.com/acme/elsewhere.git"}).status_code == 409
    assert client.request("DELETE", f"/v1/repositories/{repo['id']}").status_code == 409


def test_epics_are_ordered_and_deleting_one_keeps_its_work(client: ApiClient):
    project = _project(client)
    one, two, three, four = (
        client.post(f"/v1/projects/{project['id']}/epics", {"title": t}).json() for t in ("1", "2", "3", "4")
    )

    def order():
        return [e["title"] for e in client.get(f"/v1/projects/{project['id']}/epics").json()["epics"]]

    assert client.patch(f"/v1/epics/{four['id']}", {"position": 0}).status_code == 200
    assert order() == ["4", "1", "2", "3"]
    assert client.patch(f"/v1/epics/{four['id']}", {"position": 2}).status_code == 200
    assert order() == ["1", "2", "4", "3"]
    # Past the end is the end.
    assert client.patch(f"/v1/epics/{one['id']}", {"position": 99}).status_code == 200
    assert order() == ["2", "4", "3", "1"]

    item = client.post("/v1/work-items", {"projectId": project["id"], "epicId": two["id"], "title": "Keep me"}).json()
    assert client.request("DELETE", f"/v1/epics/{two['id']}").status_code == 204
    kept = client.get(f"/v1/work-items/{item['id']}").json()
    assert kept["title"] == "Keep me" and kept["epicId"] is None
    assert order() == ["4", "3", "1"]
    assert three["id"]


def test_a_work_item_can_be_edited_and_moved_until_delivery_starts(client: ApiClient, forge_project: dict):
    epic = client.post(f"/v1/projects/{forge_project['id']}/epics", {"title": "Later"}).json()
    item = client.post("/v1/work-items", {"projectId": forge_project["id"], "title": "Draft"}).json()

    resp = client.patch(f"/v1/work-items/{item['id']}", {"title": "Final", "acceptanceCriteria": ["it works"],
                                                           "epicId": epic["id"]})
    assert resp.status_code == 200, resp.text
    assert resp.json()["acceptanceCriteria"] == ["it works"] and resp.json()["epicId"] == epic["id"]
    assert client.patch(f"/v1/work-items/{item['id']}", {}).status_code == 400

    other = client.post(f"/v1/projects/{_project(client)['id']}/epics", {"title": "Elsewhere"}).json()
    assert client.patch(f"/v1/work-items/{item['id']}", {"epicId": other["id"]}).status_code == 404

    assert client.post(f"/v1/work-items/{item['id']}/deliver").status_code == 201
    # Agents are working to it now: what it asks for is fixed.
    assert client.patch(f"/v1/work-items/{item['id']}", {"goal": "something else"}).status_code == 409
    # Where it sits is not.
    assert client.patch(f"/v1/work-items/{item['id']}", {"epicId": None}).status_code == 200


def test_a_work_item_names_its_repository_and_delivery_uses_it(client: ApiClient, forge_project: dict):
    """With two repositories, delivery needs to know which; the work item says."""
    second = _repo(client, forge_project, "second")
    item = client.post("/v1/work-items", {"projectId": forge_project["id"], "title": "Where",
                                          "repositoryId": second["id"]}).json()
    assert item["repositoryId"] == second["id"]
    assert client.post(f"/v1/work-items/{item['id']}/deliver").status_code == 201
    wait_until(lambda: client.work_item_runs(item["id"]), timeout=15, message="the delivery never started")
    assert client.patch(f"/v1/work-items/{item['id']}", {"repositoryId": None}).status_code == 409


def test_another_organization_cannot_touch_my_structure(client: ApiClient, second_org: dict):
    project = _project(client)
    repo = _repo(client, project, "api")
    epic = client.post(f"/v1/projects/{project['id']}/epics", {"title": "Mine"}).json()
    stranger = second_org["client"]
    assert stranger.patch(f"/v1/repositories/{repo['id']}", {"defaultBranch": "x"}).status_code == 404
    assert stranger.request("DELETE", f"/v1/epics/{epic['id']}").status_code == 404
    assert stranger.post(f"/v1/projects/{project['id']}/epics", {"title": "Theirs"}).status_code == 404


def test_the_factorys_delivery_defaults_are_readable(client: ApiClient):
    defaults = client.get("/v1/delivery-defaults").json()
    assert defaults["requiredReviewers"] == ["correctness"]
    assert defaults["blockingSeverities"] == ["blocking", "high"]
    assert defaults["maxReviewIterations"] >= 1


def test_the_github_connection_is_shown_masked_and_can_be_verified(client: ApiClient, forge_project: dict):
    conn = client.get("/v1/forge/credential").json()
    assert conn["connected"] is True and conn["auth"] == "pat"
    assert conn["secretHint"] == "oken"  # the fixture's token ends in "token"
    assert "secret" not in conn
    assert conn["webhookPath"].startswith("/v1/webhooks/github/")
    assert client.post("/v1/forge/credential/verify").json() == {"ok": True, "login": "dude-bot", "scopes": None}

    client.post("/v1/forge/credential", {"auth": "pat", "secret": "wrong", "apiBaseUrl": conn["apiBaseUrl"]})
    assert client.post("/v1/forge/credential/verify").json() == {"ok": False, "reason": "GitHub rejected the token"}


def test_an_organization_without_github_says_so(client: ApiClient):
    assert client.get("/v1/forge/credential").json() == {"connected": False}
