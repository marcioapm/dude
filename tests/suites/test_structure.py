"""Organising work: a project's repositories, its epics, and editing tasks.

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
    item = client.create_task(forge_project["id"], "Keep the repository")
    assert client.post(f"/v1/tasks/{item['id']}/deliver").status_code == 201
    wait_until(lambda: client.task_runs(item["id"]), timeout=15, message="the delivery never started")
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

    item = client.post("/v1/tasks", {"projectId": project["id"], "epicId": two["id"], "title": "Keep me"}).json()
    assert client.request("DELETE", f"/v1/epics/{two['id']}").status_code == 204
    kept = client.get(f"/v1/tasks/{item['id']}").json()
    assert kept["title"] == "Keep me" and kept["epicId"] is None
    assert order() == ["4", "3", "1"]
    assert three["id"]


def test_a_task_can_be_edited_and_moved_until_delivery_starts(client: ApiClient, forge_project: dict):
    epic = client.post(f"/v1/projects/{forge_project['id']}/epics", {"title": "Later"}).json()
    item = client.post("/v1/tasks", {"projectId": forge_project["id"], "title": "Draft"}).json()

    resp = client.patch(f"/v1/tasks/{item['id']}", {"title": "Final", "acceptanceCriteria": ["it works"],
                                                           "epicId": epic["id"]})
    assert resp.status_code == 200, resp.text
    assert resp.json()["acceptanceCriteria"] == ["it works"] and resp.json()["epicId"] == epic["id"]
    assert client.patch(f"/v1/tasks/{item['id']}", {}).status_code == 400

    other = client.post(f"/v1/projects/{_project(client)['id']}/epics", {"title": "Elsewhere"}).json()
    assert client.patch(f"/v1/tasks/{item['id']}", {"epicId": other["id"]}).status_code == 404

    assert client.post(f"/v1/tasks/{item['id']}/deliver").status_code == 201
    # Agents are working to it now: what it asks for is fixed.
    assert client.patch(f"/v1/tasks/{item['id']}", {"goal": "something else"}).status_code == 409
    # Where it sits is not.
    assert client.patch(f"/v1/tasks/{item['id']}", {"epicId": None}).status_code == 200


def test_a_task_names_its_repositories_each_changed_or_read(client: ApiClient, forge_project: dict):
    """A task names the repositories it touches — each one it changes or
    only reads — and they are fixed, like what it asks for, once it is delivered."""
    first = client.get(f"/v1/projects/{forge_project['id']}").json()["repositories"][0]
    second = _repo(client, forge_project, "second")
    wanted = [{"id": first["id"], "access": "write"}, {"id": second["id"], "access": "read"}]
    item = client.post("/v1/tasks", {"projectId": forge_project["id"], "title": "Where", "repositories": wanted}).json()
    assert sorted(item["repositories"], key=lambda r: r["id"]) == sorted(wanted, key=lambda r: r["id"])

    # One not in the project, or named twice, is refused.
    assert client.patch(f"/v1/tasks/{item['id']}", {"repositories": [{"id": "repo_nope"}]}).status_code == 404
    twice = [{"id": first["id"]}, {"id": first["id"], "access": "read"}]
    assert client.patch(f"/v1/tasks/{item['id']}", {"repositories": twice}).status_code == 400
    # Changing them keeps what was given; none is work that changes no code.
    changed = client.patch(f"/v1/tasks/{item['id']}", {"repositories": [{"id": second["id"]}]}).json()
    assert changed["repositories"] == [{"id": second["id"], "access": "write"}]

    assert client.post(f"/v1/tasks/{item['id']}/deliver").status_code == 201
    wait_until(lambda: client.task_runs(item["id"]), timeout=15, message="the delivery never started")
    assert client.patch(f"/v1/tasks/{item['id']}", {"repositories": []}).status_code == 409


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


def test_tasks_are_numbered_within_their_project(client: ApiClient):
    project = client.create_project(name="Text Kit", slug=f"textkit-{os.urandom(3).hex()}")
    first = client.post("/v1/tasks", {"projectId": project["id"], "title": "One"}).json()
    second = client.post("/v1/tasks", {"projectId": project["id"], "title": "Two"}).json()
    assert (first["key"], second["key"]) == ("TEXT-1", "TEXT-2")
    assert client.get(f"/v1/tasks/{second['id']}").json()["key"] == "TEXT-2"
    nav = client.get("/v1/navigation").json()
    keys = [wi["key"] for p in nav["projects"] if p["id"] == project["id"] for wi in p["tasks"]]
    assert sorted(keys) == ["TEXT-1", "TEXT-2"]
