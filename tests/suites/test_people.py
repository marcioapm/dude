"""People: members, their keys and roles, who is on a task, who is online,
and what waits on whom.

A person is one of an organization's members; any number of API keys act
for them. The first key an organization is provisioned with is its admin.
"""

from __future__ import annotations

import json
import threading

import requests

from helpers import ApiClient, create_api_key, execute, query, wait_until


def _invite(admin: ApiClient, env, name: str, role: str = "member") -> tuple[dict, ApiClient]:
    resp = admin.post("/v1/people", {"name": name, "email": f"{name.lower()}@acme.dev", "role": role})
    assert resp.status_code == 201, resp.text
    body = resp.json()
    assert body["key"].startswith("dude_sk_")
    return body["person"], ApiClient(env.control_plane_url, body["key"])


def test_you_are_a_person_of_your_organization(client: ApiClient, org: dict):
    me = client.get("/v1/me").json()
    assert me["organization"]["id"] == org["id"]
    person = me["person"]
    assert person["id"].startswith("per_")
    assert person["name"] == "e2e user"
    # The organization's first person is its admin, and here now.
    assert person["role"] == "admin"
    assert person["online"] is True
    assert set(person) >= {"id", "name", "photoUrl", "online", "email", "role", "lastSeenAt"}

    listed = client.get("/v1/people").json()
    assert listed["you"] == person["id"]
    assert [p["name"] for p in listed["people"]] == ["e2e user"]


def test_an_admin_invites_and_the_key_is_shown_once(client: ApiClient, env):
    ana, ana_client = _invite(client, env, "Ana")
    assert ana["role"] == "member" and ana["email"] == "ana@acme.dev"
    # The key signs in as Ana.
    assert ana_client.get("/v1/me").json()["person"]["id"] == ana["id"]
    # Once: her key list shows its prefix, never the key.
    keys = ana_client.get("/v1/me/keys").json()["keys"]
    assert len(keys) == 1 and "key" not in keys[0] and keys[0]["current"] is True
    # The same address twice is one person.
    again = client.post("/v1/people", {"name": "Ana again", "email": "ANA@acme.dev"})
    assert again.status_code == 409, again.text


def test_only_admins_manage_members(client: ApiClient, env):
    bo, bo_client = _invite(client, env, "Bo")
    for resp in (
        bo_client.post("/v1/people", {"name": "Cy", "email": "cy@acme.dev"}),
        bo_client.patch(f"/v1/people/{bo['id']}", {"role": "admin"}),
        bo_client.delete(f"/v1/people/{client.get('/v1/me').json()['person']['id']}"),
    ):
        assert resp.status_code == 403, resp.text
        assert resp.json()["error"]["code"] == "not_admin"

    # Made an admin, Bo can.
    assert client.patch(f"/v1/people/{bo['id']}", {"role": "admin"}).json()["person"]["role"] == "admin"
    assert bo_client.post("/v1/people", {"name": "Cy", "email": "cy@acme.dev"}).status_code == 201


def test_the_last_admin_stays_and_nobody_removes_themselves(client: ApiClient, env):
    me = client.get("/v1/me").json()["person"]
    assert client.patch(f"/v1/people/{me['id']}", {"role": "member"}).status_code == 409
    assert client.delete(f"/v1/people/{me['id']}").status_code == 409


def test_removing_someone_revokes_all_their_keys(client: ApiClient, env, owner_dsn: str, project: dict):
    dee, dee_client = _invite(client, env, "Dee")
    second = dee_client.post("/v1/me/keys", {"name": "laptop"})
    assert second.status_code == 201, second.text
    laptop = ApiClient(env.control_plane_url, second.json()["key"])
    assert laptop.get("/v1/me").json()["person"]["id"] == dee["id"]
    task = dee_client.create_task(project["id"], "Dee's")
    assert task["owner"]["id"] == dee["id"]

    assert client.delete(f"/v1/people/{dee['id']}").status_code == 204
    assert dee_client.get("/v1/me").status_code == 401
    assert laptop.get("/v1/me").status_code == 401
    assert [p["name"] for p in client.get("/v1/people").json()["people"]] == ["e2e user"]
    # Their task is nobody's now, and what they did keeps their name.
    after = client.get(f"/v1/tasks/{task['id']}").json()
    assert after["owner"] is None and after["people"] == []
    created = next(e for e in client.events(taskId=task["id"]) if e["eventType"] == "task.created")
    assert created["actor"]["name"] == "Dee"


def test_your_keys_are_yours_to_make_and_revoke(client: ApiClient, env):
    made = client.post("/v1/me/keys", {"name": "CI"}).json()
    script = ApiClient(env.control_plane_url, made["key"])
    keys = client.get("/v1/me/keys").json()["keys"]
    assert {k["name"] for k in keys} == {"e2e user", "CI"}
    # Revoking another person's key is not yours to do.
    _, bo_client = _invite(client, env, "Bo")
    assert bo_client.delete(f"/v1/me/keys/{made['id']}").status_code == 404
    assert client.delete(f"/v1/me/keys/{made['id']}").status_code == 204
    assert script.get("/v1/me").status_code == 401
    # Your last key is how you sign in.
    only = client.get("/v1/me/keys").json()["keys"]
    assert client.delete(f"/v1/me/keys/{only[0]['id']}").status_code == 409


def test_a_profile_has_a_name_and_a_photo(client: ApiClient, env):
    pixel = "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg=="
    person = client.patch("/v1/me", {"name": "Ana Ribeiro", "photoUrl": pixel}).json()["person"]
    assert person["name"] == "Ana Ribeiro"
    # Lists carry a short URL, not the image; an <img> loads it with no key.
    assert person["photoUrl"].startswith(f"/v1/people/{person['id']}/photo?t=")
    img = requests.get(env.control_plane_url + person["photoUrl"], timeout=10)
    assert img.status_code == 200 and img.headers["content-type"] == "image/png"
    assert requests.get(f"{env.control_plane_url}/v1/people/{person['id']}/photo?t=guess", timeout=10).status_code == 404
    # An https photo is kept as given; anything else is refused.
    assert client.patch("/v1/me", {"photoUrl": "https://example.com/a.jpg"}).json()["person"]["photoUrl"] == "https://example.com/a.jpg"
    for bad in ("javascript:alert(1)", "http://example.com/a.jpg", "data:text/html;base64,PGI+"):
        assert client.patch("/v1/me", {"photoUrl": bad}).status_code == 400, bad
    assert client.patch("/v1/me", {"photoUrl": "data:image/png;base64," + "A" * 400_000}).status_code == 400
    assert client.patch("/v1/me", {"photoUrl": None}).json()["person"]["photoUrl"] is None


def test_presence_says_who_is_online_and_is_pushed_live(client: ApiClient, env, owner_dsn: str):
    ana, ana_client = _invite(client, env, "Ana")
    # Invited, never seen.
    people = {p["name"]: p for p in client.get("/v1/people").json()["people"]}
    assert people["Ana"]["online"] is False and people["Ana"]["lastSeenAt"] is None

    frames: list[dict] = []
    ready = threading.Event()

    def listen():
        with requests.get(f"{env.control_plane_url}/v1/events/stream?live=1",
                          headers={"authorization": f"Bearer {client.api_key}"}, stream=True, timeout=20) as resp:
            for raw in resp.iter_lines(decode_unicode=True):
                if raw and raw.startswith(":"):
                    ready.set()
                elif raw and raw.startswith("data:"):
                    frames.append(json.loads(raw[5:]))
                    if frames[-1]["eventType"] == "person.seen":
                        return

    reader = threading.Thread(target=listen, daemon=True)
    reader.start()
    assert ready.wait(10)
    requests.get(f"{env.control_plane_url}/v1/navigation", timeout=10,
                 headers={"authorization": f"Bearer {ana_client.api_key}", "x-dude-where": "TEXT-14"})
    reader.join(15)
    seen = next(f for f in frames if f["eventType"] == "person.seen")
    assert seen["payload"]["person"]["id"] == ana["id"]
    assert seen["payload"]["person"]["online"] is True
    assert seen["payload"]["person"]["where"] == "TEXT-14"
    people = {p["name"]: p for p in client.get("/v1/people").json()["people"]}
    assert people["Ana"]["online"] is True and people["Ana"]["lastSeenWhere"] == "TEXT-14"

    # Five minutes later, gone.
    execute(owner_dsn, "UPDATE people SET last_seen_at = now() - interval '6 minutes' WHERE id = %s", (ana["id"],))
    assert {p["name"]: p for p in client.get("/v1/people").json()["people"]}["Ana"]["online"] is False


def test_a_task_has_people_the_owner_first(client: ApiClient, env, project: dict):
    me = client.get("/v1/me").json()["person"]
    ana, ana_client = _invite(client, env, "Ana")
    bo, _ = _invite(client, env, "Bo")
    task = client.create_task(project["id"], "Together")
    assert [p["id"] for p in task["people"]] == [me["id"]]
    assert task["people"][0] == task["owner"]
    assert set(task["owner"]) == {"id", "name", "photoUrl", "online"}

    resp = client.put(f"/v1/tasks/{task['id']}/people", {"people": [ana["id"], me["id"], bo["id"]]})
    assert resp.status_code == 200, resp.text
    assert [p["name"] for p in resp.json()["people"]] == ["Ana", "e2e user", "Bo"]
    assert resp.json()["owner"]["id"] == ana["id"]
    detail = client.get(f"/v1/tasks/{task['id']}").json()
    assert detail["owner"]["id"] == ana["id"]
    nav = [t for p in client.get("/v1/navigation").json()["projects"] for t in p["tasks"] if t["id"] == task["id"]]
    assert [p["name"] for p in nav[0]["people"]] == ["Ana", "e2e user", "Bo"]
    kinds = [e["eventType"] for e in client.events(taskId=task["id"])]
    assert "task.owner_changed" in kinds and "task.people_changed" in kinds
    changed = next(e for e in client.events(taskId=task["id"]) if e["eventType"] == "task.people_changed")
    # A human actor is the person, not the key.
    assert changed["actor"]["id"] == me["id"] and changed["actor"]["name"] == "e2e user"
    assert changed["actor"]["keyId"].startswith("key_")

    # Taking it over puts you first and keeps the others.
    taken = ana_client.patch(f"/v1/tasks/{task['id']}", {"ownerId": bo["id"]}).json()
    assert [p["name"] for p in taken["people"]] == ["Bo", "Ana", "e2e user"]
    # Nobody who is not a member; never nobody.
    assert client.put(f"/v1/tasks/{task['id']}/people", {"people": ["per_nobody"]}).status_code == 404
    assert client.put(f"/v1/tasks/{task['id']}/people", {"people": []}).status_code == 400


def test_an_owner_answers_with_any_of_their_keys(client: ApiClient, env, forge_project: dict):
    """The orchestrator's owner check is by person: a second key of the
    owner's answers; someone else's is refused, naming the owner."""
    client.patch(f"/v1/projects/{forge_project['id']}", {"agentModels": {
        "implementer": {"model": "fake/ask"}, "reviewer": {"model": "fake/scripted"}, "simplifier": {"model": "fake/scripted"}}})
    task = client.create_task(forge_project["id"], "Ask me")
    assert client.post(f"/v1/tasks/{task['id']}/deliver").status_code == 201
    question = wait_until(lambda: next(iter(client.get("/v1/questions", params={"taskId": task["id"]}).json()["questions"]), None),
                          timeout=30, message="the agent never asked")
    _, bo = _invite(client, env, "Bo")
    refused = bo.post(f"/v1/questions/{question['id']}/answer", {"text": "yes"})
    assert refused.status_code == 403 and "only e2e user can answer" in refused.json()["error"]["message"]
    laptop = ApiClient(env.control_plane_url, client.post("/v1/me/keys", {"name": "laptop"}).json()["key"])
    assert laptop.post(f"/v1/questions/{question['id']}/answer", {"text": "yes"}).status_code == 200


def test_keys_from_before_people_are_people(env, org: dict):
    """A key provisioned straight into the database is a person of its own."""
    key = create_api_key(env.owner_dsn, org["id"], name="Provisioned")
    me = ApiClient(env.control_plane_url, key).get("/v1/me").json()["person"]
    assert me["name"] == "Provisioned" and me["role"] == "member"
    rows = query(env.owner_dsn, "SELECT count(*) AS n FROM people WHERE organization_id = %s", (org["id"],))
    assert rows[0]["n"] == 2
