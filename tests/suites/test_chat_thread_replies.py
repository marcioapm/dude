"""What a Slack thread hears back once dude has routed a mention, end to end
through the deployed processes, the fake Slack, the fake GitHub and the
scripted agents.

A mention routed to a brainstorm: the session's agent decides what goes
back, with `reply_in_thread`, to the thread the turn's message came from.
The Slack mention records its conversation and thread on its message into
the session. A mention's text reaches dude as one line, after the person's
name, so the scripted agent is told what to call by a later mention in the
bound thread: the thread's newer messages are quoted first, and the
mention's own text is then a line of its own. That turn came from Slack, so
the reply is posted in the thread. A turn typed in dude gets no thread
reply: the call is refused and the thread is unchanged.

A mention routed to a task: dude itself posts the task's progress in the
thread, once each and in order, as its fake delivery starts, opens a pull
request on the fake GitHub, and is merged.

Routing is tests/env.py's judge stub: "file a task" files one in the first
project listed, anything else is a brainstorm.
"""

from __future__ import annotations

import json
import os

from fake_github import FakeGitHub
from helpers import ApiClient, provision_person, query, wait_until
from suites.test_chat_providers import Slack, _oauth, _requests


def _tool(name: str, args: dict) -> str:
    return f"tool: {name} {json.dumps(args)}"


def _installed(client: ApiClient, env, project: dict | None = None) -> dict:
    """Slack installed for the organisation (Márcia, admin, U1, who links her
    account), #support with her and the bot, the scripted brainstorm, and
    the Judgements tier on."""
    scripted = client.tier_for("fake/scripted")
    assert client.patch("/v1/settings/organization", {"roles": {"brainstorm": {"tier": scripted}}}).status_code == 200
    thinker = {t["name"]: t for t in client.get("/v1/models/tiers").json()["tiers"]}["Thinker"]["id"]
    assert client.put("/v1/judgements/settings", {"tierId": thinker}).status_code == 200
    team = "T" + os.urandom(3).hex().upper()
    slack = Slack(env, team)
    u1 = f"U1{team}"
    slack.user(u1, "marcia", "Márcia Costa")
    slack.conversation(f"C{team}", "support", "public", [u1])
    tag = os.urandom(2).hex()
    _, marcia = provision_person(client, env, "Márcia Costa", f"marcia-{tag}@example.com", "admin")
    slack.authorize_as(installer=u1)
    assert "outcome=installed" in _oauth(marcia, "/v1/chat/slack/install")
    slack.authorize_as(signer=u1)
    assert "outcome=connected" in _oauth(marcia, "/v1/chat/slack/connect")
    if project is None:
        project = client.create_project(name="Clock", slug=f"clock-{tag}", key=f"K{tag[:3].upper()}")
    return {"slack": slack, "team": team, "channel": f"C{team}", "u1": u1, "marcia": marcia, "project": project}


def _thread(slack: Slack, channel: str, root: str) -> list[dict]:
    return [m for m in slack.bot_messages(channel) if m["ThreadTS"] == root]


def test_a_brainstorm_answers_in_the_thread_the_mention_came_from(client: ApiClient, env, owner_dsn: str, org: dict):
    n = _installed(client, env)
    slack, channel = n["slack"], n["channel"]
    root = slack.mention(channel, "", n["u1"], "what time is it in Berlin?")
    [req] = _requests(n["marcia"], 1)
    assert req["route"] == "brainstorm" and req["result"]["kind"] == "session", req
    session = req["result"]["id"]
    wait_until(lambda: query(owner_dsn, "SELECT 1 FROM events WHERE session_id = %s AND event_type = 'agent.message'", (session,)),
               timeout=60, message="the session's agent never answered")
    # The Slack mention recorded where it was said: that turn's replies go there.
    [said_in] = query(owner_dsn, """SELECT payload->>'conversationId' AS conversation, payload->>'threadId' AS thread FROM events
        WHERE session_id = %s AND event_type = 'chat.message' AND actor_type = 'person' ORDER BY cursor""", (session,))
    assert said_in == {"conversation": channel, "thread": root}, said_in
    # From Slack: a message in the bound thread, then a mention there. The
    # quoted message comes first, so the mention's text is a line of its
    # own, and the scripted agent calls the tool. That turn came from the
    # thread, so the reply is posted in it.
    slack.say(channel, root, n["u1"], "one more thing")
    slack.mention(channel, root, n["u1"], _tool("reply_in_thread", {"text": "It is 22:15 in Berlin"}))
    try:
        posted = wait_until(lambda: query(owner_dsn, """SELECT payload->>'conversationId' AS c, payload->>'threadId' AS t FROM events
            WHERE session_id = %s AND event_type = 'chat.reply.posted'""", (session,)) or None, timeout=90)
    except AssertionError:
        events = query(owner_dsn, "SELECT event_type, left(payload::text, 300) AS p FROM events WHERE session_id = %s ORDER BY cursor", (session,))
        raise AssertionError(f"the agent's reply was never posted: {events}") from None
    assert posted == [{"c": channel, "t": root}], posted
    assert "It is 22:15 in Berlin" in [m["Text"] for m in _thread(slack, channel, root)], _thread(slack, channel, root)
    before = len(_thread(slack, channel, root))

    # Typed in dude: that turn has no thread to answer, and the reply is refused.
    lines = ["answer in the thread", _tool("reply_in_thread", {"text": "It is 22:16 in Berlin"})]
    assert n["marcia"].post(f"/v1/brainstorms/{session}/chat", {"text": "\n".join(lines)}).status_code in (200, 201)

    try:
        refused = wait_until(lambda: query(owner_dsn, "SELECT payload->>'error' AS error FROM events WHERE session_id = %s AND event_type = 'chat.reply.refused'",
                                           (session,)) or None, timeout=60)
    except AssertionError:
        events = query(owner_dsn, "SELECT event_type, left(payload::text, 300) AS p FROM events WHERE session_id = %s ORDER BY cursor", (session,))
        raise AssertionError(f"the agent's reply was never refused: {events}") from None
    assert len(refused) == 1 and "typed in dude, not in the chat" in refused[0]["error"], refused
    thread = _thread(slack, channel, root)
    # Nothing more in the thread: the Slack turn's one post stands alone.
    assert len(thread) == before and thread[0]["Text"].startswith("Started a session"), thread
    assert "It is 22:16 in Berlin" not in [m["Text"] for m in thread], thread
    assert query(owner_dsn, "SELECT count(*) AS n FROM events WHERE session_id = %s AND event_type = 'chat.reply.posted'", (session,)) == [{"n": 1}]
    # Nothing in any other conversation.
    assert all(m["Channel"] == channel for m in slack.state()["messages"] or [] if m.get("Bot"))


def test_a_task_routed_from_slack_tells_the_thread_it_started_opened_a_pull_request_and_is_done(
        client: ApiClient, env, owner_dsn: str, org: dict, forge_project: dict, fake_github: FakeGitHub):
    n = _installed(client, env, forge_project)
    slack, channel = n["slack"], n["channel"]
    root = slack.mention(channel, "", n["u1"], "please file a task to tidy the export page")
    [req] = _requests(n["marcia"], 1)
    assert req["route"] == "task" and req["result"]["kind"] == "task", req
    task = req["result"]["id"]
    key = client.get(f"/v1/tasks/{task}").json()["key"]
    wait_until(lambda: _thread(slack, channel, root), timeout=30, message="the routing reply")
    assert client.post(f"/v1/tasks/{task}/deliver").status_code == 201

    pr = wait_until(lambda: client.get("/v1/pull-requests", params={"taskId": task}).json()["pullRequests"], timeout=90,
                    message="the delivery never opened a pull request")[0]
    fake_github.merge(pr["number"])
    wait_until(lambda: client.get(f"/v1/tasks/{task}").json()["status"] == "done", timeout=60, message="the merge never finished the task")

    def notices():
        got = [m["Text"] for m in _thread(slack, channel, root)[1:]]
        return got if len(got) >= 3 else None
    got = wait_until(notices, timeout=30, message="dude's notices in the thread")
    title = "Tidy the export page"
    # The pull request's URL names the repository: the thread gets dude's task link (the button) alone.
    assert got == [f"Started {key} {title}.", f"Opened a pull request for {key} {title}.", f"Done: {key} {title}."], got
    assert not [m for m in _thread(slack, channel, root) if pr["url"] in json.dumps(m)], _thread(slack, channel, root)
    rows = query(owner_dsn, "SELECT kind, attempts FROM chat_notices WHERE task_id = %s ORDER BY created_at", (task,))
    assert rows == [{"kind": "started", "attempts": 1}, {"kind": "pr_opened", "attempts": 1}, {"kind": "done", "attempts": 1}], rows
