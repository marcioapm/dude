"""Chat (Slack, through the fake Slack), end to end through the deployed processes.

An admin installs the app in a workspace (OAuth v2 at the fake's consent
page, back through the backend's public callback); a member links their
Slack account by signing in with it while signed in to dude; then mentions
of @dude, delivered as Slack signs Events API payloads, are routed by the
orchestrator: a public channel's into a session open to the organisation
with a reply linking it, a private channel's into a session shared with
the connected members of the channel, a mention in a task's bound thread
to that task's link with no model call, an "open an epic" into an epic
with its tasks in the Backlog, and a Slack user dude has never linked is
refused privately with a Connect button.

Routing is deterministic: the suite's judgement proxy (env.py's
`_chat_route`) answers the routing call from the mention's words. The
fake Slack (orchestrator/cmd/fake-slack) keeps every message, ephemeral
and reaction dude makes, which the tests read through its /_fake/ controls.
"""

from __future__ import annotations

import hashlib
import hmac
import json
import os
import time
from urllib.parse import parse_qs, urlencode, urlparse

import requests

from env import SLACK_SIGNING_SECRET
from helpers import ApiClient, provision_person, query, wait_until


class Slack:
    """The fake Slack, as a test drives it: one workspace of its own."""

    def __init__(self, env, team: str):
        self.base = env.fake_slack_url
        self.team = team
        self.events = f"{env.control_plane_url}/v1/webhooks/chat/slack/events"
        self.seq = 0
        self._post("team", {"ID": team, "Name": f"Acme {team}"})

    def _post(self, what: str, body: dict) -> dict:
        resp = requests.post(f"{self.base}/_fake/{what}", json=body, timeout=10)
        assert resp.ok, resp.text
        return resp.json()

    def user(self, uid: str, name: str, real: str) -> None:
        self._post("user", {"ID": uid, "Team": self.team, "Name": name, "RealName": real})

    def conversation(self, cid: str, name: str, kind: str, members: list[str]) -> None:
        self._post("conversation", {"ID": cid, "Team": self.team, "Name": name, "Kind": kind, "Members": members + [f"UB{self.team}"]})

    def authorize_as(self, installer: str = "", signer: str = "") -> None:
        self._post("authorize-as", {"Installer": installer, "Signer": signer})

    def say(self, channel: str, thread: str, user: str, text: str) -> str:
        return self._post("post", {"Channel": channel, "Thread": thread, "User": user, "Text": text})["ts"]

    def state(self) -> dict:
        resp = requests.get(f"{self.base}/_fake/state", timeout=10)
        assert resp.ok, resp.text
        return resp.json()

    def bot_messages(self, channel: str) -> list[dict]:
        return [m for m in (self.state()["messages"] or []) if m["Channel"] == channel]

    def ephemerals(self, user: str) -> list[dict]:
        return [e for e in (self.state()["ephemerals"] or []) if e["User"] == user]

    def deliver(self, payload: dict) -> requests.Response:
        """POST an Events API payload to dude, signed as Slack signs it."""
        return self._signed(self.events, "application/json", json.dumps(payload).encode())

    def interact(self, payload: dict) -> requests.Response:
        """POST an interactivity payload (a button click) to dude, as Slack sends and signs it."""
        return self._signed(self.events.replace("/events", "/interactions"), "application/x-www-form-urlencoded",
                            urlencode({"payload": json.dumps(payload)}).encode())

    def _signed(self, url: str, content_type: str, body: bytes) -> requests.Response:
        ts = str(int(time.time()))
        sig = "v0=" + hmac.new(SLACK_SIGNING_SECRET.encode(), f"v0:{ts}:".encode() + body, hashlib.sha256).hexdigest()
        return requests.post(url, data=body, timeout=10, headers={
            "content-type": content_type, "x-slack-request-timestamp": ts, "x-slack-signature": sig})

    def mention(self, channel: str, thread: str, user: str, text: str) -> str:
        """A person's message mentioning @dude, in Slack and delivered to dude; its ts."""
        ts = self.say(channel, thread, user, f"<@UB{self.team}> {text}")
        self.seq += 1
        event = {"type": "app_mention", "user": user, "text": f"<@UB{self.team}> {text}", "ts": ts, "channel": channel}
        if thread and thread != ts:
            event["thread_ts"] = thread
        resp = self.deliver({"type": "event_callback", "team_id": self.team, "event_id": f"Ev{self.team}{self.seq}", "event": event})
        assert resp.status_code == 200, resp.text
        return ts


def _authorized(client: ApiClient, method: str, path: str, **kwargs) -> requests.Response:
    return requests.request(method, f"{client.base_url}{path}", timeout=30,
                            headers={"authorization": f"Bearer {client.api_key}", "content-type": "application/json", **kwargs.pop("headers", {})},
                            **kwargs)


def _oauth(client: ApiClient, begin: str, body: dict | None = None) -> str:
    """Begin an install or a link as `client`, consent at the fake Slack, and
    follow its redirect back through the backend's callback with this
    browser's cookie: where the backend lands the browser."""
    started = _authorized(client, "POST", begin, json=body or {})
    assert started.status_code == 200, started.text
    cookie = started.headers["set-cookie"].split(";", 1)[0]
    consent = requests.get(started.json()["url"], allow_redirects=False, timeout=10)
    assert consent.status_code == 302, consent.text
    back = requests.get(consent.headers["location"], allow_redirects=False, timeout=30, headers={"cookie": cookie})
    assert back.status_code == 303, back.text
    return back.headers["location"]


def _requests(client: ApiClient, count: int) -> list[dict]:
    def enough():
        rows = client.get("/v1/organization/chat/requests").json()["requests"]
        return rows if len(rows) >= count else None
    return list(reversed(wait_until(enough, timeout=30, message=f"{count} chat request(s)")))


def _acme(client: ApiClient, env, owner_dsn: str, org: dict) -> dict:
    """Acme with Slack: an admin (Márcia, U1) who installs it, a member (Eli,
    U2) who links his account, a member who never links (Gus, U3), the
    Judgements tier on, and a project to route to."""
    team = "T" + os.urandom(3).hex().upper()
    slack = Slack(env, team)
    for uid, name, real in (("U1", "marcia", "Márcia Costa"), ("U2", "eli", "Eli Park"), ("U3", "gus", "Gus Lee"), ("U9", "zed", "Zed Outside")):
        slack.user(f"{uid}{team}", name, real)
    u = {k: f"{k}{team}" for k in ("U1", "U2", "U3", "U9")}
    slack.conversation(f"C{team}", "support", "public", list(u.values()))
    slack.conversation(f"G{team}", "design-private", "private", list(u.values()))
    tag = os.urandom(2).hex()
    _, marcia = provision_person(client, env, "Márcia Costa", f"marcia-{tag}@example.com", "admin")
    _, eli = provision_person(client, env, "Eli Park", f"eli-{tag}@example.com")
    _, gus = provision_person(client, env, "Gus Lee", f"gus-{tag}@example.com")
    thinker = {t["name"]: t for t in client.get("/v1/models/tiers").json()["tiers"]}["Thinker"]["id"]
    assert client.put("/v1/judgements/settings", {"tierId": thinker}).status_code == 200
    project = client.create_project(name="Billing", slug=f"abs-{tag}", key=f"A{tag[:3].upper()}")

    slack.authorize_as(installer=u["U1"])
    landed = _oauth(marcia, "/v1/chat/slack/install")
    assert "chatInstall=slack&outcome=installed" in landed, landed
    for who, uid in ((marcia, u["U1"]), (eli, u["U2"])):
        slack.authorize_as(signer=uid)
        landed = _oauth(who, "/v1/chat/slack/connect")
        assert "chat=slack&outcome=connected" in landed, landed
    return {"slack": slack, "u": u, "team": team, "marcia": marcia, "eli": eli, "gus": gus, "project": project, "org": org}


def test_an_admin_installs_one_workspace_and_members_link_their_own_accounts(client: ApiClient, env, owner_dsn: str, org: dict, second_org: dict):
    acme = _acme(client, env, owner_dsn, org)
    view = acme["marcia"].get("/v1/organization/chat/slack").json()
    assert view["installed"] and view["workspaceName"] == f"Acme {acme['team']}"
    assert sorted(p["handle"] for p in view["people"]) == ["eli", "marcia"]
    assert "xoxb" not in json.dumps(view)
    # A member reads the facts, not the health or the people.
    eli_view = acme["eli"].get("/v1/organization/chat/slack").json()
    assert eli_view["health"] is None and eli_view["people"] is None and eli_view["connected"]["handle"] == "eli"
    # A member may not install; another organisation may not take the workspace.
    assert _authorized(acme["eli"], "POST", "/v1/chat/slack/install", json={}).status_code == 403
    _, theirs = provision_person(second_org["client"], env, "Other Admin", f"other-{os.urandom(2).hex()}@example.com", "admin")
    acme["slack"].authorize_as(installer=acme["u"]["U1"])
    assert "outcome=workspace_taken" in _oauth(theirs, "/v1/chat/slack/install")
    # Gus signing in as Eli's Slack account: already linked to Eli.
    acme["slack"].authorize_as(signer=acme["u"]["U2"])
    assert "outcome=already_linked" in _oauth(acme["gus"], "/v1/chat/slack/connect")


def test_a_public_channel_mention_opens_an_organisation_session_and_replies_with_its_link(client: ApiClient, env, owner_dsn: str, org: dict):
    acme = _acme(client, env, owner_dsn, org)
    slack, u, team = acme["slack"], acme["u"], acme["team"]
    root = slack.say(f"C{team}", "", u["U9"], "CSV export times out for 40 meters")
    ts = slack.mention(f"C{team}", root, u["U1"], "can you reproduce this?")
    [req] = _requests(acme["marcia"], 1)
    assert req["route"] == "brainstorm" and req["result"]["kind"] == "session" and req["conversationKind"] == "public"
    session = req["result"]["id"]
    [row] = query(owner_dsn, "SELECT access, origin->>'conversationName' AS place FROM sessions WHERE id = %s", (session,))
    assert row == {"access": "organisation", "place": "#support"}
    reply = wait_until(lambda: [m for m in slack.bot_messages(f"C{team}") if session in json.dumps(m.get("Blocks"))], message="the reply")[0]
    assert reply["ThreadTS"] == root and "open to everyone" in reply["Text"]
    assert f"#/o/{org['client'].get('/v1/me').json()['organization']['slug']}/sessions/{session}" in json.dumps(reply["Blocks"])
    assert wait_until(lambda: slack.state()["reactions"].get(f"C{team}:{ts}") == ["white_check_mark"], message="✅")
    # Eli, who was never added, reads it as an organisation member (the web's session page reads this).
    assert acme["eli"].get(f"/v1/brainstorms/{session}").status_code == 200


def test_a_private_channel_mention_shares_the_session_with_the_connected_members(client: ApiClient, env, owner_dsn: str, org: dict):
    acme = _acme(client, env, owner_dsn, org)
    slack, u, team = acme["slack"], acme["u"], acme["team"]
    slack.mention(f"G{team}", "", u["U1"], "let's think about the export screen")
    [req] = _requests(acme["marcia"], 1)
    assert req["conversationKind"] == "private" and req["route"] == "brainstorm"
    session = req["result"]["id"]
    assert query(owner_dsn, "SELECT access FROM sessions WHERE id = %s", (session,)) == [{"access": "private"}]
    # Eli (connected) is in it as chat; Gus and Zed (not connected) are skipped.
    assert [p["name"] for p in req["sharedAdded"]] == ["Eli Park"]
    assert sorted(req["sharedSkipped"]) == sorted([u["U3"], u["U9"]])
    assert acme["eli"].get(f"/v1/brainstorms/{session}").status_code == 200
    assert acme["gus"].get(f"/v1/brainstorms/{session}").status_code == 404


def test_a_task_bound_thread_answers_with_the_tasks_link_and_no_model(client: ApiClient, env, owner_dsn: str, org: dict):
    acme = _acme(client, env, owner_dsn, org)
    slack, u, team = acme["slack"], acme["u"], acme["team"]
    root = slack.mention(f"C{team}", "", u["U2"], "please file a task to tidy the export page")
    [req] = _requests(acme["marcia"], 1)
    assert req["route"] == "task" and req["result"]["kind"] == "task"
    task = req["result"]["id"]
    [t] = query(owner_dsn, "SELECT status::text AS status, origin->>'provider' AS provider FROM tasks WHERE id = %s", (task,))
    assert t == {"status": "received", "provider": "slack"}
    calls = query(owner_dsn, "SELECT count(*) AS n FROM model_calls WHERE organization_id = %s AND asker = 'chat.route'", (org["id"],))[0]["n"]
    slack.mention(f"C{team}", root, u["U1"], "status?")
    second = _requests(acme["marcia"], 2)[1]
    assert second["route"] == "task_link" and second["result"]["id"] == task
    assert query(owner_dsn, "SELECT count(*) AS n FROM model_calls WHERE organization_id = %s AND asker = 'chat.route'", (org["id"],))[0]["n"] == calls
    links = wait_until(lambda: [m for m in slack.bot_messages(f"C{team}") if m["Text"].startswith("This thread is")], message="the link")
    assert task in json.dumps(links[0]["Blocks"])


def test_an_epic_route_files_the_epic_and_its_tasks_in_the_backlog(client: ApiClient, env, owner_dsn: str, org: dict):
    acme = _acme(client, env, owner_dsn, org)
    slack, u, team = acme["slack"], acme["u"], acme["team"]
    slack.mention(f"C{team}", "", u["U1"], "open an epic so CSV exports stop timing out")
    [req] = _requests(acme["marcia"], 1)
    assert req["route"] == "epic" and req["result"]["kind"] == "epic"
    epic = req["result"]["id"]
    tasks = query(owner_dsn, "SELECT title, status::text AS status FROM tasks WHERE epic_id = %s ORDER BY title", (epic,))
    assert [t["title"] for t in tasks] == ["Batch the readings query", "Measure export time", "Stream the CSV response"]
    assert {t["status"] for t in tasks} == {"received"}
    edges = query(owner_dsn, """SELECT f.title AS blocker, t.title AS blocked FROM task_relations r JOIN tasks f ON f.id = r.from_task_id
        JOIN tasks t ON t.id = r.to_task_id WHERE t.epic_id = %s AND r.kind = 'blocks' ORDER BY t.title""", (epic,))
    assert edges == [{"blocker": "Measure export time", "blocked": "Batch the readings query"},
                     {"blocker": "Measure export time", "blocked": "Stream the CSV response"}]
    assert wait_until(lambda: [m for m in slack.bot_messages(f"C{team}") if m["Text"].startswith("Opened epic")], message="the reply")


def test_someone_dude_never_linked_is_refused_privately_with_a_connect_button(client: ApiClient, env, owner_dsn: str, org: dict):
    acme = _acme(client, env, owner_dsn, org)
    slack, u, team = acme["slack"], acme["u"], acme["team"]
    ts = slack.mention(f"C{team}", "", u["U9"], "help me")
    [req] = _requests(acme["marcia"], 1)
    assert req["route"] == "not_connected" and req["result"] is None
    eph = wait_until(lambda: slack.ephemerals(u["U9"]), message="the Connect prompt")
    assert "Connect your Slack account" in eph[0]["Text"]
    link = next(e["url"] for b in eph[0]["Blocks"] if b["type"] == "actions" for e in b["elements"])
    state = parse_qs(urlparse(link.replace("#", "", 1)).query)["state"][0]
    assert state
    # Nothing was said in public, nothing made, and the 👀 was taken off.
    assert slack.bot_messages(f"C{team}") == []
    assert wait_until(lambda: not slack.state()["reactions"].get(f"C{team}:{ts}"), message="the 👀 taken off")
    assert query(owner_dsn, "SELECT count(*) AS n FROM sessions WHERE organization_id = %s", (org["id"],))[0]["n"] == 0


def test_an_unsure_mention_asks_and_the_askers_click_files_the_task(client: ApiClient, env, owner_dsn: str, org: dict):
    acme = _acme(client, env, owner_dsn, org)
    slack, u, team = acme["slack"], acme["u"], acme["team"]
    root = slack.mention(f"C{team}", "", u["U2"], "maybe a task to tidy the export page")
    [req] = _requests(acme["marcia"], 1)
    assert req["route"] == "clarify" and req["askedRoute"] == "task"
    question = wait_until(lambda: [m for m in slack.bot_messages(f"C{team}") if m["Text"].startswith("Should I file")], message="the question")[0]
    value = next(e["value"] for b in question["Blocks"] if b["type"] == "actions" for e in b["elements"])
    click = {"type": "block_actions", "team": {"id": team}, "user": {"id": u["U2"], "username": "eli"},
             "container": {"type": "message", "channel_id": f"C{team}", "message_ts": question["TS"], "thread_ts": root},
             "message": {"ts": question["TS"], "thread_ts": root},
             "actions": [{"action_id": "choice_0", "value": value, "action_ts": str(time.time())}]}
    resp = slack.interact(click)
    assert resp.status_code == 200, resp.text
    second = _requests(acme["marcia"], 2)[1]
    assert second["route"] == "task" and second["result"]["kind"] == "task"
    assert wait_until(lambda: [x for x in slack.state()["updates"] or [] if x["Text"].startswith("Eli Park chose")], message="the question updated")


def test_the_routing_stub_read_every_routing_call(env):
    """Runs last in the file: a change to the routing call's opening or
    headings fails here by name, not as a confusing route elsewhere."""
    assert env.chat_route_mismatches == []
