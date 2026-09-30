"""People: members, their keys and roles, who is on a task, who is online,
and what waits on whom.

A person is one of an organization's members; any number of API keys act
for them. The first key an organization is provisioned with is its admin.
"""

from __future__ import annotations

import json
import threading

import requests

from helpers import ApiClient, create_api_key, execute, query, sign_in, wait_until


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


def test_a_removed_owners_task_passes_to_the_next_person_on_it(client: ApiClient, env, project: dict):
    """Its first person and its owner stay one: the next on it, not the one who left."""
    me = client.get("/v1/me").json()["person"]
    eli, eli_client = _invite(client, env, "Eli")
    task = eli_client.create_task(project["id"], "Eli's, shared")
    assert client.put(f"/v1/tasks/{task['id']}/people", {"people": [eli["id"], me["id"]]}).status_code == 200
    assert client.delete(f"/v1/people/{eli['id']}").status_code == 204
    after = client.get(f"/v1/tasks/{task['id']}").json()
    assert after["owner"]["id"] == me["id"]
    assert [p["id"] for p in after["people"]] == [me["id"]]


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


# A 1×1 red PNG.
PNG = bytes.fromhex(
    "89504e470d0a1a0a0000000d49484452000000010000000108060000001f15c489"
    "0000000d49444154789c63f8cfc0f01f00050001ff89993d1d0000000049454e44ae426082")


def _upload(client: ApiClient, path: str, body: bytes, content_type: str = "image/png") -> requests.Response:
    return requests.put(client.base_url + path, data=body, timeout=30,
                        headers={"authorization": f"Bearer {client.api_key}", "content-type": content_type})


def test_a_profile_has_a_name_and_a_photo_kept_in_storage(client: ApiClient, env, owner_dsn: str):
    person = client.patch("/v1/me", {"name": "Ana Ribeiro"}).json()["person"]
    assert person["name"] == "Ana Ribeiro"
    up = _upload(client, "/v1/me/photo", PNG)
    assert up.status_code == 200, up.text
    person = up.json()["person"]
    # Lists carry a short URL, not the image; an <img> loads it with no key.
    assert person["photoUrl"].startswith(f"/v1/people/{person['id']}/photo?t=")
    img = requests.get(env.control_plane_url + person["photoUrl"], timeout=10)
    assert img.status_code == 200 and img.headers["content-type"] == "image/png" and img.content == PNG
    assert requests.get(f"{env.control_plane_url}/v1/people/{person['id']}/photo?t=guess", timeout=10).status_code == 404
    # The image is in the bucket, never the database.
    [row] = query(owner_dsn, "SELECT photo_url, photo_key FROM people WHERE id = %s", (person["id"],))
    assert row["photo_url"] is None
    assert env.s3().get_object(Bucket=env.s3_bucket, Key=row["photo_key"])["Body"].read() == PNG

    # A new photo is a new object and a new URL; the old one goes.
    first_key, first_url = row["photo_key"], person["photoUrl"]
    second = _upload(client, "/v1/me/photo", PNG).json()["person"]
    assert second["photoUrl"] != first_url
    assert requests.get(env.control_plane_url + first_url, timeout=10).status_code == 404
    keys = [o["Key"] for o in env.s3().list_objects_v2(Bucket=env.s3_bucket).get("Contents", [])]
    assert first_key not in keys

    # Only images, and only small ones: what is served back is never anything a browser runs.
    assert _upload(client, "/v1/me/photo", b"<script>alert(1)</script>", "text/html").status_code == 400
    assert _upload(client, "/v1/me/photo", b"<svg onload=alert(1)>", "image/png").status_code == 400
    assert _upload(client, "/v1/me/photo", PNG[:8] + b"\0" * 600_000).status_code == 400
    # Sent without a length, a large body is cut off at the cap all the same.
    chunked = requests.put(client.base_url + "/v1/me/photo", data=(PNG[:8] + b"\0" * 65536 for _ in range(40)), timeout=30,
                           headers={"authorization": f"Bearer {client.api_key}", "content-type": "image/png"})
    assert chunked.status_code == 400 and "at most" in chunked.text, chunked.text

    # An https photo is kept as given; anything else, data: URLs included, is refused.
    assert client.patch("/v1/me", {"photoUrl": "https://example.com/a.jpg"}).json()["person"]["photoUrl"] == "https://example.com/a.jpg"
    for bad in ("javascript:alert(1)", "http://example.com/a.jpg", "data:image/png;base64,iVBORw0KGgo="):
        assert client.patch("/v1/me", {"photoUrl": bad}).status_code == 400, bad
    assert client.patch("/v1/me", {"photoUrl": None}).json()["person"]["photoUrl"] is None


def test_a_project_has_an_image_only_admins_set(client: ApiClient, env, project: dict):
    pid = project["id"]
    up = _upload(client, f"/v1/projects/{pid}/image", PNG)
    assert up.status_code == 200, up.text
    url = up.json()["imageUrl"]
    assert url.startswith(f"/v1/projects/{pid}/image?t=")
    assert requests.get(env.control_plane_url + url, timeout=10).content == PNG
    # The tree carries it, so the sidebar shows the face.
    nav = client.get("/v1/navigation").json()["projects"]
    assert next(p for p in nav if p["id"] == pid)["imageUrl"] == url
    # A member sees it and cannot change it.
    _, bo = _invite(client, env, "Bo")
    assert _upload(bo, f"/v1/projects/{pid}/image", PNG).status_code == 403
    assert bo.delete(f"/v1/projects/{pid}/image").status_code == 403
    # A project it cannot find is not one it stores anything for.
    assert _upload(client, "/v1/projects/..%2F..%2Fsomeone-else%2Fpeople%2Fx/image", PNG).status_code == 404
    # Back to initials; the object goes with it.
    assert client.delete(f"/v1/projects/{pid}/image").json()["imageUrl"] is None
    assert requests.get(env.control_plane_url + url, timeout=10).status_code == 404
    assert env.s3().list_objects_v2(Bucket=env.s3_bucket, Prefix=f"{project['organizationId']}/projects/").get("KeyCount", 0) == 0


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


# ---------------------------------------------------------------------------
# In the browser
# ---------------------------------------------------------------------------

import pytest  # noqa: E402
from playwright.sync_api import Page, expect  # noqa: E402


def _sign_in(page: Page, web_url: str, api_key: str) -> None:
    sign_in(page, web_url, api_key)


@pytest.mark.ui
def test_waiting_on_you_is_split_by_whose_it_is(
    page: Page, web_url: str, client: ApiClient, env, forge_project: dict, console_errors: list
):
    """Bo's agent asks Bo. Bo sees it as his, loud; the first person sees it
    under Waiting on others, quietly, and takes it over — then it is theirs."""
    client.patch(f"/v1/projects/{forge_project['id']}", {"agentModels": {
        "implementer": {"model": "fake/ask"}, "reviewer": {"model": "fake/scripted"}, "simplifier": {"model": "fake/scripted"}}})
    bo, bo_client = _invite(client, env, "Bo")
    task = bo_client.create_task(forge_project["id"], "Bo's question")
    assert bo_client.post(f"/v1/tasks/{task['id']}/deliver").status_code == 201
    wait_until(lambda: client.get("/v1/questions", params={"taskId": task["id"]}).json()["questions"], timeout=30,
               message="the agent never asked")

    # Bo: his, counted on "Waiting on you" and listed as his in the inbox.
    _sign_in(page, web_url, bo_client.api_key)
    expect(page.get_by_test_id("waiting-on-you")).to_contain_text("1")
    page.get_by_test_id("waiting-on-you").click()
    expect(page.get_by_role("region", name="Yours")).to_contain_text("Bo's question")
    expect(page.get_by_role("region", name="Waiting on others")).to_have_count(0)

    # The first person: not theirs, so calm, under Waiting on others.
    _sign_in(page, web_url, client.api_key)
    expect(page.get_by_test_id("waiting-on-others")).to_contain_text("1")
    page.get_by_test_id("waiting-on-others").click()
    others = page.get_by_role("region", name="Waiting on others")
    expect(others).to_contain_text("Bo's question")
    expect(others).to_contain_text("Bo's task")
    expect(page.get_by_role("region", name="Yours")).not_to_contain_text("Bo's question")

    others.get_by_role("button", name="Take over").click()
    expect(page.get_by_role("region", name="Yours")).to_contain_text("Bo's question")
    expect(page.get_by_role("region", name="Waiting on others")).to_have_count(0)
    after = client.get(f"/v1/tasks/{task['id']}").json()
    assert [p["name"] for p in after["people"]] == ["e2e user", "Bo"]
    assert console_errors == []


@pytest.mark.ui
def test_online_row_and_profile_band(page: Page, web_url: str, client: ApiClient, env, console_errors: list):
    _invite(client, env, "Ana")
    ana_client = ApiClient(env.control_plane_url, client.post("/v1/people", {"name": "Cy", "email": "cy@acme.dev"}).json()["key"])
    ana_client.get("/v1/me")  # Cy is here now.
    _sign_in(page, web_url, client.api_key)
    online = page.get_by_test_id("online")
    # You and Cy; Ana was never seen.
    expect(online).to_have_attribute("aria-label", "Online: e2e user, Cy")
    band = page.get_by_test_id("my-settings-button")
    expect(band).to_contain_text("e2e user")
    band.click()
    expect(page.get_by_test_id("my-settings")).to_be_visible()
    assert console_errors == []


@pytest.mark.ui
def test_your_profile_and_keys_in_your_settings(page: Page, web_url: str, client: ApiClient, env, console_errors: list):
    _sign_in(page, web_url, client.api_key)
    page.get_by_test_id("my-settings-button").click()
    page.get_by_test_id("profile-name").fill("Ana Ribeiro")
    page.get_by_test_id("profile-save").click()
    expect(page.get_by_test_id("my-settings-button")).to_contain_text("Ana Ribeiro")

    # A photo, resized in the browser, is your face everywhere.
    png = bytes.fromhex("89504e470d0a1a0a0000000d4948445200000002000000020802000000fdd49a730000001049444154789c63f8cfc000440c100a001fee03fd8b5f14d40000000049454e44ae426082")
    page.get_by_test_id("photo-file").set_input_files({"name": "me.png", "mimeType": "image/png", "buffer": png})
    expect(page.get_by_test_id("my-settings-button").locator("img")).to_have_count(1)
    photo = client.get("/v1/me").json()["person"]["photoUrl"]
    assert photo.startswith("/v1/people/")
    # Resized to a JPEG in the browser, stored, and served back.
    assert requests.get(env.control_plane_url + photo, timeout=10).headers["content-type"] == "image/jpeg"

    # A new key, shown once; revoked, it stops working.
    page.get_by_test_id("key-new").click()
    page.get_by_test_id("key-name").fill("Laptop CLI")
    page.get_by_test_id("key-create").click()
    secret = page.get_by_test_id("key-secret").input_value()
    assert ApiClient(env.control_plane_url, secret).get("/v1/me").status_code == 200
    page.get_by_test_id("key-done").click()
    row = page.locator('[data-testid="key-row"][data-key-name="Laptop CLI"]')
    row.get_by_test_id("key-revoke").click()
    page.get_by_test_id("key-revoke-confirm").click()
    expect(row).to_have_count(0)
    assert ApiClient(env.control_plane_url, secret).get("/v1/me").status_code == 401
    assert console_errors == []


@pytest.mark.ui
def test_an_admin_manages_members(page: Page, web_url: str, client: ApiClient, env, console_errors: list):
    _sign_in(page, web_url, client.api_key)
    page.get_by_test_id("org-settings-button").click()
    members = page.get_by_test_id("members")
    expect(members.get_by_test_id("member")).to_have_count(1)

    page.get_by_test_id("invite").click()
    page.get_by_test_id("invite-name").fill("Dee Marsh")
    page.get_by_test_id("invite-email").fill("dee@acme.dev")
    page.get_by_test_id("invite-submit").click()
    secret = page.get_by_test_id("key-secret").input_value()
    dee = ApiClient(env.control_plane_url, secret)
    assert dee.get("/v1/me").json()["person"]["name"] == "Dee Marsh"
    page.get_by_test_id("key-done").click()

    row = members.locator('[data-member="Dee Marsh"]')
    row.get_by_role("combobox", name="Role of Dee Marsh").click()
    page.get_by_role("option", name="Admin").click()
    wait_until(lambda: dee.get("/v1/me").json()["person"]["role"] == "admin", timeout=10, message="role not changed")

    row.get_by_test_id("member-remove").click()
    page.get_by_test_id("member-remove-confirm").click()
    expect(row).to_have_count(0)
    assert dee.get("/v1/me").status_code == 401

    # A member sees the list, and nothing to manage it with.
    _, cy = _invite(client, env, "Cy")
    _sign_in(page, web_url, cy.api_key)
    page.get_by_test_id("org-settings-button").click()
    expect(page.get_by_test_id("member")).to_have_count(2)
    expect(page.get_by_test_id("invite")).to_have_count(0)
    expect(page.get_by_test_id("member-remove")).to_have_count(0)
    assert console_errors == []


def test_a_member_cannot_change_what_only_admins_may(client: ApiClient, env, project: dict):
    """The organisation's settings, its prompts, a project's settings and the
    GitHub connection are an admin's: a member reads settings, changes none."""
    _, bo = _invite(client, env, "Bo")
    pid = project["id"]
    task = bo.create_task(pid, "Bo's own task")
    refused = [
        bo.patch("/v1/settings/organization", {"roles": {"implementer": {"model": "fake/scripted"}}}),
        bo.post("/v1/prompts/implementer", {"body": "# Mine now"}),
        bo.patch(f"/v1/projects/{pid}/settings", {"roles": {"reviewer": {"model": "fake/scripted"}}}),
        bo.post("/v1/prompts/implementer", {"projectId": pid, "mode": "add", "body": "More"}),
        bo.get("/v1/forge/webhook-secret"),
        bo.post("/v1/forge/webhook-secret/rotate", {}),
        bo.post("/v1/forge/webhooks/register", {}),
        bo.patch("/v1/forge/settings", {"whoCanWake": "anyone"}),
        bo.post("/v1/forge/credential", {"auth": "pat", "secret": "a-token-of-my-own"}),
        # The same settings by the project's own route, and its repositories.
        bo.patch(f"/v1/projects/{pid}", {"agentModels": {"implementer": {"model": "fake/scripted"}}}),
        bo.post(f"/v1/projects/{pid}/repositories", {"name": "extra", "url": "https://github.com/acme/extra.git"}),
        # Or a project of their own, or looser rules and other models for one task.
        bo.post("/v1/projects", {"name": "Mine", "slug": "mine", "agentModels": {"implementer": {"model": "fake/scripted"}}}),
        bo.post(f"/v1/tasks/{task['id']}/deliver", {"policy": {"requiredReviewers": []}}),
        bo.post("/v1/runs/run_any/sessions", {"role": "implementer", "model": "fake/scripted"}),
    ]
    assert [r.status_code for r in refused] == [403] * len(refused), [(r.request.url, r.status_code, r.text[:80]) for r in refused]
    assert all(r.json()["error"]["code"] == "not_admin" for r in refused), [r.text[:80] for r in refused]
    # Reading is anyone's; the screen shows the controls disabled.
    settings = bo.get("/v1/settings/organization")
    assert settings.status_code == 200 and settings.json()["canEdit"] is False, settings.text[:200]
    # The admin still may.
    assert client.post("/v1/prompts/implementer", {"body": "# Ours"}).status_code in (200, 201)
