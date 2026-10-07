"""Brainstorm sessions, end to end through the deployed processes.

A session belongs to its members. Its agent (the scripted one, on the
organisation's brainstorm role) reads the projects linked to it and
proposes work; a member files what it proposes, as themselves, with
nothing on the filed work naming the session. The owner shares it: an
invitee sees only the invitation until they accept, then the whole
conversation, each message signed. A question put to one member is
theirs to answer; a reader writes nothing and files nothing; a handover
keeps the same Run.

The scripted agent answers each input quoting it, and carries out the
"tool: NAME {json}" lines of a message, so a test drives its tools
through the session's chat as a real agent would decide to.
"""

from __future__ import annotations

import json
import os

from fake_github import FakeGitHub
from helpers import ApiClient, query, wait_until


def _person(admin: ApiClient, env, name: str) -> tuple[dict, ApiClient]:
    email = name.split()[0].lower().encode("ascii", "ignore").decode()
    resp = admin.post("/v1/people", {"name": name, "email": f"{email}@acme.dev", "role": "member"})
    assert resp.status_code == 201, resp.text
    body = resp.json()
    return body["person"], ApiClient(env.control_plane_url, body["key"])


def _scripted_brainstorm(client: ApiClient) -> None:
    tier = client.tier_for("fake/scripted")
    resp = client.patch("/v1/settings/organization", {"roles": {"brainstorm": {"tier": tier}}})
    assert resp.status_code == 200, resp.text


def _project(client: ApiClient, name: str, key: str, clone_url: str) -> dict:
    # A project's key is its slug's letters (BL-12): digits after them add none.
    project = client.create_project(name=name, slug=f"{key.lower()}-{int.from_bytes(os.urandom(3)):08d}",
                                    repositories=[{"name": name, "url": clone_url, "defaultBranch": "main"}])
    return client.get(f"/v1/projects/{project['id']}").json()


def _said(client: ApiClient, session: str) -> list[str]:
    return [e["payload"]["text"] for e in client.get("/v1/events", params={"sessionId": session, "limit": 1000}).json()["events"]
            if e["eventType"] == "agent.message"]


def _tool(name: str, args: dict) -> str:
    return f"tool: {name} {json.dumps(args)}"


def test_a_session_reads_two_projects_proposes_and_files_as_the_person_with_no_trace(client: ApiClient, env, owner_dsn: str,
                                                                                    fake_github: FakeGitHub):
    _scripted_brainstorm(client)
    billing = _project(client, "billing", "BL", fake_github.clone_url)
    web = _project(client, "web-console", "WC", fake_github.add_repository("web").clone_url)
    keys = query(owner_dsn, "SELECT key_prefix FROM projects WHERE id = ANY(%s) ORDER BY key_prefix", ([billing["id"], web["id"]],))
    assert [k["key_prefix"] for k in keys] == ["BL", "WC"]
    links = [{"projectId": billing["id"], "repositoryIds": [billing["repositories"][0]["id"]]},
             {"projectId": web["id"], "repositoryIds": [web["repositories"][0]["id"]]}]
    resp = client.post("/v1/brainstorms", {"title": "Usage-based billing", "projects": links})
    assert resp.status_code == 201, resp.text
    session = resp.json()["id"]
    assert session.startswith("ssn_")

    # The first message starts its agent, on no task and no project, briefed with both projects.
    proposal = {"items": [
        {"kind": "epic", "project": "BL", "title": "Usage metering", "description": "Count runs per org per day."},
        {"kind": "task", "project": "BL", "epic": "Usage metering", "title": "Dedupe runs on run id",
         "goal": "The rollup counts each run id once, whatever the meter's key window.", "acceptanceCriteria": ["counted once"]},
        {"kind": "task", "project": "WC", "title": "Usage panel shows a cost estimate",
         "goal": "The usage panel shows an estimated cost per kind, labelled as an estimate.", "acceptanceCriteria": ["labelled"]},
    ]}
    resp = client.post(f"/v1/brainstorms/{session}/chat", {"text": "where would metering live?\n" + _tool("propose", proposal)})
    assert resp.status_code == 201, resp.text
    run = client.get_run(resp.json()["runId"])
    assert (run["taskId"], run["projectId"], run["role"]) == (None, None, "brainstorm")
    wait_until(lambda: _said(client, session), timeout=60, message="the session's agent never answered")
    assert "where would metering live?" in _said(client, session)[0]
    briefed = [e for e in client.get("/v1/events", params={"sessionId": session}).json()["events"] if e["eventType"] == "session.briefed"]
    assert len(briefed) == 1 and "BL" in briefed[0]["payload"]["text"] and "WC" in briefed[0]["payload"]["text"]

    # Its proposal is the session's card: nothing filed, nothing created.
    card = wait_until(lambda: client.get(f"/v1/brainstorms/{session}").json()["proposals"], timeout=30, message="no proposal card")[0]
    assert [i["kind"] for i in card["items"]] == ["epic", "task", "task"]
    assert all(s["canFile"] for s in card["status"])
    assert query(owner_dsn, "SELECT count(*) AS n FROM tasks WHERE project_id = ANY(%s)", ([billing["id"], web["id"]],))[0]["n"] == 0

    # File: the epic first, its task under it, as the person who pressed File.
    filed = client.post(f"/v1/brainstorms/{session}/file", {"proposalId": card["id"], "items": [0, 1, 2]}).json()["results"]
    assert [r["status"] for r in filed] == ["filed"] * 3, filed
    me = client.get("/v1/me").json()["person"]["id"]
    tasks = query(owner_dsn, """SELECT t.id, t.title, t.goal, e.title AS epic, t.created_by_run_id,
        (SELECT person_id FROM task_people tp WHERE tp.task_id = t.id AND position = 0) AS owner
        FROM tasks t LEFT JOIN epics e ON e.id = t.epic_id WHERE t.project_id = ANY(%s) ORDER BY t.title""",
                  ([billing["id"], web["id"]],))
    assert [(t["title"], t["epic"], t["owner"], t["created_by_run_id"]) for t in tasks] == [
        ("Dedupe runs on run id", "Usage metering", me, None), ("Usage panel shows a cost estimate", None, me, None)]

    # Nothing filed names the session: not its tasks, epics, events or anything a project's ledger shows.
    for t in tasks:
        text = json.dumps(client.get(f"/v1/tasks/{t['id']}").json())
        assert session not in text and "Usage-based billing" not in text and "brainstorm" not in text.lower()
    for project in (billing, web):
        events = client.get("/v1/events", params={"projectId": project["id"], "limit": 1000}).json()["events"]
        assert events and all(e["sessionId"] is None for e in events)
        assert session not in json.dumps(events) and run["id"] not in json.dumps(events)

    # Filed once: a second press refuses it.
    again = client.post(f"/v1/brainstorms/{session}/file", {"proposalId": card["id"], "items": [1]}).json()["results"]
    assert again == [{"item": 1, "status": "refused", "why": "already filed"}]


def test_a_shared_session_signs_every_message_and_a_question_to_one_member_waits_for_them(client: ApiClient, env, owner_dsn: str):
    _scripted_brainstorm(client)
    # Linked with none of its repositories: it reads the project's tasks, nothing is checked out.
    billing = _project(client, "billing", "BL", "git://127.0.0.1:1/none/billing.git")
    ana, ana_client = _person(client, env, "Ana Nunes")
    joao, joao_client = _person(client, env, "João Reis")
    session = client.post("/v1/brainstorms", {"title": "Usage-based billing",
                                              "projects": [{"projectId": billing["id"], "repositoryIds": []}]}).json()["id"]
    client.post(f"/v1/brainstorms/{session}/chat", {"text": "Ana owns billing, she'll know"})
    wait_until(lambda: _said(client, session), timeout=60, message="the agent never answered")

    # Shared: until they accept, the invitees see the invitation and not a word.
    assert client.post(f"/v1/brainstorms/{session}/people", {"people": [ana["id"]], "role": "chat"}).status_code == 200
    assert client.post(f"/v1/brainstorms/{session}/people", {"people": [joao["id"]], "role": "read"}).status_code == 200
    listed = ana_client.get("/v1/brainstorms").json()
    assert listed["sessions"] == [] and [i["id"] for i in listed["invitations"]] == [session]
    assert "Ana owns billing" not in json.dumps(listed)
    assert ana_client.get(f"/v1/brainstorms/{session}").status_code == 404
    assert ana_client.get("/v1/events", params={"sessionId": session}).json()["events"] == []

    # Accepted: the whole history, from the first message.
    assert ana_client.post(f"/v1/brainstorms/{session}/accept").status_code == 200
    assert joao_client.post(f"/v1/brainstorms/{session}/accept").status_code == 200
    seen = [e["payload"].get("text") for e in ana_client.get("/v1/events", params={"sessionId": session}).json()["events"]
            if e["eventType"] == "chat.message"]
    assert seen[0] == "Ana owns billing, she'll know"

    # Every message reaches the agent signed with its writer's name.
    assert ana_client.post(f"/v1/brainstorms/{session}/chat", {"text": "keys expire after 24h"}).status_code == 200
    wait_until(lambda: any("Ana Nunes: keys expire after 24h" in s for s in _said(client, session)), timeout=60,
               message="Ana's message never reached the agent signed")

    # A reader writes nothing and files nothing.
    assert joao_client.post(f"/v1/brainstorms/{session}/chat", {"text": "me too"}).status_code == 403

    # A question put to Ana: only she answers it; Márcio's message waits for her answer.
    ask = _tool("ask_person", {"question": "Grow the window for every kind?", "choices": ["Every kind", "Experiment runs only"], "to": "Ana Nunes"})
    client.post(f"/v1/brainstorms/{session}/chat", {"text": "ask Ana\n" + ask})
    question = wait_until(lambda: client.get(f"/v1/brainstorms/{session}").json()["question"], timeout=60, message="it never asked Ana")
    assert question["to"]["id"] == ana["id"] and question["yours"] is False
    assert ana_client.get(f"/v1/brainstorms/{session}").json()["question"]["yours"] is True
    assert [q["sessionId"] for q in ana_client.get("/v1/brainstorms").json()["questions"]] == [session]
    assert client.get("/v1/brainstorms").json()["questions"] == []
    held = client.post(f"/v1/brainstorms/{session}/chat", {"text": "also, cost estimates?"}).json()
    assert "questionId" not in held
    said = len(_said(client, session))
    answered = ana_client.post(f"/v1/brainstorms/{session}/chat", {"text": "Experiment runs only"}).json()
    assert answered["questionId"] == question["id"]
    wait_until(lambda: len(_said(client, session)) >= said + 2, timeout=60, message="the agent never heard Ana's answer and the held message")
    # The scripted agent quotes an input's first line: Ana's answer first, then the message held for it, signed.
    after = _said(client, session)[said:]
    assert "Ana Nunes answered your question" in after[0], after
    assert "e2e user: also, cost estimates?" in after[1], after
    answer = query(owner_dsn, "SELECT answer, answered_by_person FROM questions WHERE id = %s", (question["id"],))
    assert answer == [{"answer": "Experiment runs only", "answered_by_person": ana["id"]}]


def test_handing_over_keeps_the_run_and_the_new_owner_decides(client: ApiClient, env):
    _scripted_brainstorm(client)
    ana, ana_client = _person(client, env, "Ana Nunes")
    session = client.post("/v1/brainstorms", {"title": "Meter v2", "projects": []}).json()["id"]
    run = client.post(f"/v1/brainstorms/{session}/chat", {"text": "start"}).json()["runId"]
    wait_until(lambda: _said(client, session), timeout=60, message="the agent never answered")
    client.post(f"/v1/brainstorms/{session}/people", {"people": [ana["id"]], "role": "chat"})
    ana_client.post(f"/v1/brainstorms/{session}/accept")

    # Only the owner shares; then Márcio hands it over, keeping read.
    assert ana_client.post(f"/v1/brainstorms/{session}/owner", {"person": ana["id"], "keep": "chat"}).status_code == 403
    resp = client.post(f"/v1/brainstorms/{session}/owner", {"person": ana["id"], "keep": "read"})
    assert resp.status_code == 200, resp.text
    detail = ana_client.get(f"/v1/brainstorms/{session}").json()
    assert detail["you"]["role"] == "owner"
    assert detail["session"]["run"]["id"] == run
    assert client.get(f"/v1/brainstorms/{session}").json()["you"]["role"] == "read"
    # The agent is told who owns it now.
    told = wait_until(lambda: [e for e in client.get("/v1/events", params={"sessionId": session}).json()["events"]
                               if e["eventType"] == "session.told"], timeout=30, message="the agent was never told")
    assert "Ana Nunes owns this session now" in told[0]["payload"]["text"]
    # Márcio, now a reader, writes nothing; Ana decides who is in it.
    assert client.post(f"/v1/brainstorms/{session}/chat", {"text": "hi"}).status_code == 403
    me = client.get("/v1/me").json()["person"]["id"]
    assert ana_client.post(f"/v1/brainstorms/{session}/people/{me}/remove").status_code == 200
    assert client.get(f"/v1/brainstorms/{session}").status_code == 404
    assert [s["id"] for s in client.get("/v1/brainstorms").json()["sessions"]] == []
