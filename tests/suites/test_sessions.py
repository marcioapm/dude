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
import re
import threading

import pytest
import requests

from fake_github import FakeGitHub
from helpers import ApiClient, execute, query, sign_in, wait_until
from playwright.sync_api import Page, expect


def _person(admin: ApiClient, env, name: str, role: str = "member") -> tuple[dict, ApiClient]:
    email = name.split()[0].lower().encode("ascii", "ignore").decode()
    resp = admin.post("/v1/people", {"name": name, "email": f"{email}@acme.dev", "role": role})
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


def test_a_stuck_brainstorm_turn_stops_once_and_every_member_sees_the_notice(
        client: ApiClient, env, owner_dsn: str, page: Page, web_url: str, org: dict):
    tier = client.tier_for("fake/stall")
    assert client.patch("/v1/settings/organization", {"roles": {"brainstorm": {"tier": tier}}}).status_code == 200
    session = client.post("/v1/brainstorms", {"title": "Stuck tool call"}).json()["id"]
    reader, reader_client = _person(client, env, "Stuck Reader")
    assert client.post(f"/v1/brainstorms/{session}/people", {"people": [reader["id"]], "role": "read"}).status_code == 200
    assert reader_client.post(f"/v1/brainstorms/{session}/accept").status_code == 200
    run = client.post(f"/v1/brainstorms/{session}/chat", {"text": "Read the worker"}).json()["runId"]
    wait_until(lambda: query(owner_dsn, "SELECT 1 FROM runs WHERE id = %s AND open_tool_calls_at <> '{}'", (run,)),
               timeout=30, message="the brainstorm never hung in its tool call")
    # Like F6, advance the persisted clock instead of waiting ten real minutes.
    execute(owner_dsn, """UPDATE runs SET open_tool_calls_at =
        (SELECT jsonb_object_agg(k, to_jsonb(now() - interval '11 minutes')) FROM jsonb_object_keys(open_tool_calls_at) k)
        WHERE id = %s""", (run,))
    stopped = wait_until(lambda: [e for e in client.events(sessionId=session, limit=1000)
                                 if e["eventType"] == "session.turn_stopped"],
                         timeout=30, message="the stuck turn was not stopped within a sweep")
    assert len(stopped) == 1 and stopped[0]["payload"]["tool"] == "task"
    wait_until(lambda: query(owner_dsn, "SELECT 1 FROM runs WHERE id = %s AND turn_done_at IS NOT NULL", (run,)),
               timeout=30, message="the interrupted turn never ended")
    assert [e["eventId"] for e in reader_client.events(sessionId=session, limit=1000)
            if e["eventType"] == "session.turn_stopped"] == [stopped[0]["eventId"]]
    sign_in(page, web_url, org["api_key"], board=False)
    page.goto(f"{web_url}#/sessions/{session}")
    notice = page.locator('[data-kind="stopped"]')
    expect(notice).to_have_count(1, timeout=30_000)
    expect(notice).to_contain_text("Stopped Brainstorm's turn: task was open for 10 min.")


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

    # An edit to a task of mine not started, and a comment on it: filed as me, as if typed.
    mine = client.create_task(billing["id"], "Daily per-org usage rollup", goal="meter_daily(org, kind, day, count), backfilled.",
                              acceptanceCriteria=["backfilled"])
    key = client.get(f"/v1/tasks/{mine['id']}").json()["key"]
    edit = {"items": [
        {"kind": "edit", "task": key, "after": {"goal": "meter_daily(org, kind, day, count), each run id counted once.",
                                                "acceptanceCriteria": ["backfilled", "each run id once"]}},
        {"kind": "comment", "task": key, "text": "Retries can come days later: dedupe on run id."},
    ]}
    said = len(_said(client, session))
    client.post(f"/v1/brainstorms/{session}/chat", {"text": "fix the rollup\n" + _tool("propose", edit)})
    wait_until(lambda: len(_said(client, session)) > said, timeout=60, message="the agent never answered the edit")
    second = wait_until(lambda: [p for p in client.get(f"/v1/brainstorms/{session}").json()["proposals"] if p["id"] != card["id"]],
                        timeout=30, message="no second card")[0]
    filed = client.post(f"/v1/brainstorms/{session}/file", {"proposalId": second["id"], "items": [0, 1]}).json()["results"]
    assert [r["status"] for r in filed] == ["filed", "filed"], filed
    edited = client.get(f"/v1/tasks/{mine['id']}").json()
    assert edited["goal"] == "meter_daily(org, kind, day, count), each run id counted once."
    task_events = client.get("/v1/events", params={"taskId": mine["id"], "limit": 1000}).json()["events"]
    kinds = [e["eventType"] for e in task_events]
    assert "task.updated" in kinds and "task.comment" in kinds, kinds
    comment = next(e for e in task_events if e["eventType"] == "task.comment")
    assert comment["payload"]["text"] == "Retries can come days later: dedupe on run id."
    assert comment["sessionId"] is None and comment["runId"] is None, comment

    # Every public read of the filed work — each task, its ledger, the
    # project's ledger, the task list and navigation — names neither the
    # session, its title, its Run nor where the work came from.
    traces = [session, run["id"], "Usage-based billing", "ssn_", "prp_", "brainstorm", "proposal"]
    reads = [client.get(f"/v1/tasks/{t}").text for t in [mine["id"], *(t["id"] for t in tasks)]]
    reads += [client.get("/v1/events", params={"taskId": t, "limit": 1000}).text for t in [mine["id"], *(t["id"] for t in tasks)]]
    reads += [client.get("/v1/events", params={"projectId": p["id"], "limit": 1000}).text for p in (billing, web)]
    reads += [client.get("/v1/tasks", params={"projectId": p["id"]}).text for p in (billing, web)]
    reads += [client.get("/v1/navigation").text]
    for text in reads:
        for trace in traces:
            assert trace.lower() not in text.lower(), (trace, text[:400])
    rows = query(owner_dsn, "SELECT row_to_json(t)::text AS row FROM tasks t WHERE t.project_id = ANY(%s)", ([billing["id"], web["id"]],))
    for row in rows:
        for trace in traces:
            assert trace.lower() not in row["row"].lower(), (trace, row["row"])


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
    assert ana_client.get("/v1/events", params={"sessionId": session}).status_code == 404

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


# lux's rule for a spec's repository names (lux internal/spec/spec.go volumeRe).
LUX_NAME = re.compile(r"^[a-z0-9][a-z0-9_-]{0,31}$")


def test_a_session_linking_a_project_lux_would_refuse_by_name_starts(client: ApiClient, env, owner_dsn: str,
                                                                      fake_github: FakeGitHub):
    _scripted_brainstorm(client)
    # An uppercase key (BILL) and a repository name past lux's 32 characters: <key>-<name> breaks lux's rule twice.
    name = "Billing-API.payments-ledger-service"
    repo = fake_github.add_repository("billing-api-ledger")
    project = client.create_project(name="billing", slug=f"bill-{int.from_bytes(os.urandom(3)):08d}",
                                    repositories=[{"name": name, "url": repo.clone_url, "defaultBranch": "main"}])
    project = client.get(f"/v1/projects/{project['id']}").json()
    assert query(owner_dsn, "SELECT key_prefix FROM projects WHERE id = %s", (project["id"],)) == [{"key_prefix": "BILL"}]
    session = client.post("/v1/brainstorms", {"title": "Billing", "projects": [
        {"projectId": project["id"], "repositoryIds": [project["repositories"][0]["id"]]}]}).json()["id"]
    run = client.post(f"/v1/brainstorms/{session}/chat", {"text": "where does metering go?"}).json()["runId"]

    # The fake lux refuses a name real lux would; the agent starts and answers.
    wait_until(lambda: _said(client, session), timeout=60, message="the agent never answered: lux refused its spec?")
    # The scripted agent quotes the session's title: a session has no task to quote.
    assert _said(client, session)[0].startswith('In the session "Billing". You asked:'), _said(client, session)
    row = query(owner_dsn, "SELECT status::text AS status, lux_repositories FROM runs WHERE id = %s", (run,))[0]
    assert row["status"] != "failed"
    [held] = row["lux_repositories"]
    assert LUX_NAME.match(held) and held.startswith("bill-billing-api-"), held
    # Checked out where the agent is told, by the project's key and the repository's own name.
    briefing = query(owner_dsn, "SELECT prompt FROM runs WHERE id = %s", (run,))[0]["prompt"]
    assert f"/workspace/repos/BILL/{name}" in briefing


def test_billing_api_and_billing_worker_get_distinct_keys_and_one_session_reads_both(client: ApiClient, owner_dsn: str,
                                                                                      fake_github: FakeGitHub):
    _scripted_brainstorm(client)
    # Each holds a repository named api; their slugs both start with "billing".
    suffix = int.from_bytes(os.urandom(3))
    api, worker = (client.create_project(name=n, slug=f"{s}-{suffix:08d}",
                                         repositories=[{"name": "api", "url": fake_github.clone_url, "defaultBranch": "main"}])
                   for n, s in (("Billing API", "billing-api"), ("Billing Worker", "billing-worker")))
    # The first takes the slug's first letters; the second, the next free key: its words' initials and letters.
    assert (api["key"], worker["key"]) == ("BILL", "BWOR")
    assert client.post("/v1/projects", {"name": "Billing Ledger", "slug": f"billing-ledger-{suffix:08d}", "key": "bill"}).json() == {
        "error": {"code": "conflict", "message": "BILL is already the key of Billing API; pick another", "details": {"suggestion": "BLED"}}}

    link = lambda p: {"projectId": p["id"], "repositoryIds": [p["repositories"][0]["id"]]}  # noqa: E731
    resp = client.post("/v1/brainstorms", {"title": "Billing", "projects": [link(api), link(worker)]})
    assert resp.status_code == 201, resp.text
    session = resp.json()["id"]
    assert sorted(p["key"] for p in client.get(f"/v1/brainstorms/{session}").json()["session"]["projects"]) == ["BILL", "BWOR"]
    run = client.post(f"/v1/brainstorms/{session}/chat", {"text": "where does metering go?"}).json()["runId"]

    wait_until(lambda: _said(client, session), timeout=60, message="the session's agent never answered")
    assert _said(client, session)[0].startswith('In the session "Billing". You asked:'), _said(client, session)
    row = query(owner_dsn, "SELECT status::text AS status, lux_repositories FROM runs WHERE id = %s", (run,))[0]
    assert row["status"] != "failed"
    held = row["lux_repositories"]
    assert len(held) == 2 and len(set(held)) == 2 and all(LUX_NAME.match(h) for h in held), held
    briefing = query(owner_dsn, "SELECT prompt FROM runs WHERE id = %s", (run,))[0]["prompt"]
    assert "/workspace/repos/BILL/api" in briefing and "/workspace/repos/BWOR/api" in briefing


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


def test_a_session_starts_untitled_its_agent_names_it_and_a_persons_name_wins(client: ApiClient, env, owner_dsn: str):
    _scripted_brainstorm(client)
    ana, ana_client = _person(client, env, "Ana Nunes")
    reader, reader_client = _person(client, env, "Rita Reader")
    otto, otto_client = _person(client, env, "Otto Other")
    resp = client.post("/v1/brainstorms", {})
    assert resp.status_code == 201, resp.text
    session = resp.json()["id"]
    assert resp.json()["title"] is None
    for who in (ana, reader):
        role = "chat" if who is ana else "read"
        assert client.post(f"/v1/brainstorms/{session}/people", {"people": [who["id"]], "role": role}).status_code == 200
    assert ana_client.post(f"/v1/brainstorms/{session}/accept").status_code == 200
    assert reader_client.post(f"/v1/brainstorms/{session}/accept").status_code == 200
    listed = next(s for s in client.get("/v1/brainstorms").json()["sessions"] if s["id"] == session)
    assert listed["title"] is None

    # The agent names it once the subject is clear.
    client.post(f"/v1/brainstorms/{session}/chat",
                {"text": "metering for experiments\n" + _tool("name_session", {"title": "Usage metering"})})
    wait_until(lambda: client.get(f"/v1/brainstorms/{session}").json()["session"]["title"] == "Usage metering",
               timeout=60, message="the agent never named the session")
    detail = client.get(f"/v1/brainstorms/{session}").json()["session"]
    assert detail["titledBy"] == "agent"

    # A reader can't rename it; someone not in it is told it does not exist.
    assert reader_client.post(f"/v1/brainstorms/{session}/title", {"title": "Mine"}).status_code == 403
    assert otto_client.post(f"/v1/brainstorms/{session}/title", {"title": "Mine"}).status_code == 404
    # Ana can, and her name wins: the agent's next name_session is refused (the scripted agent says the 422).
    assert ana_client.post(f"/v1/brainstorms/{session}/title", {"title": "Billing v2"}).status_code == 200
    said = len(_said(client, session))
    client.post(f"/v1/brainstorms/{session}/chat", {"text": "rename\n" + _tool("name_session", {"title": "Something else"})})
    wait_until(lambda: len(_said(client, session)) > said, timeout=60, message="the agent never answered")
    assert any("dude name_session answered 422" in s for s in _said(client, session)[said:]), _said(client, session)[said:]
    assert client.get(f"/v1/brainstorms/{session}").json()["session"]["title"] == "Billing v2"
    renamed = [(e["payload"]["title"], e["payload"]["by"]) for e in client.events(sessionId=session, limit=1000)
               if e["eventType"] == "session.renamed"]
    assert renamed == [("Usage metering", "agent"), ("Billing v2", ana["id"])]
    # Members only, like every session event: anyone else is told the session does not exist.
    assert otto_client.get("/v1/events", params={"sessionId": session}).status_code == 404
    assert not [e for e in otto_client.events(limit=1000) if e["eventType"] == "session.renamed" and e["sessionId"] == session]


def _title_uncut(page: Page) -> None:
    """The session's name in its header is shown whole: its box is at least as wide as its words need."""
    fits = page.get_by_test_id("session-title").evaluate("""el => {
        const words = el.querySelector('[data-title-words]') ?? el.firstElementChild;
        const range = document.createRange();
        range.selectNodeContents(words);
        return {box: el.getBoundingClientRect().width, need: range.getBoundingClientRect().width,
                cut: words.scrollWidth > words.clientWidth, text: words.textContent};
    }""")
    assert not fits["cut"] and fits["box"] > fits["need"], fits


def _welcome_send(page: Page, text: str) -> str:
    """Write the first message in the welcome's composer and send it; the session it made, once opened."""
    composer = page.get_by_test_id("welcome").locator("textarea")
    composer.fill(text)
    composer.press("Enter")
    expect(page.get_by_test_id("session-screen")).to_be_visible(timeout=15_000)
    expect(page).to_have_url(re.compile(r"#/sessions/ssn_"))
    return page.url.split("#/sessions/")[1]


@pytest.mark.ui
def test_new_session_opens_untitled_with_the_composer_focused_and_its_header_renames_it(
        client: ApiClient, page: Page, web_url: str, org: dict, fake_github: FakeGitHub):
    """New session opens the welcome, its composer focused; the first message sent there makes the
    session, untitled, and opens it; its header renames it."""
    _scripted_brainstorm(client)
    # Warm for the whole test (the organisation's policy: a session has no project), so the agent is
    # not parked after its turn, and only the turn's end can clear Thinking.
    assert client.patch("/v1/settings/organization", {"delivery": {"conductorWarmMinutes": 30}}).status_code == 200
    _project(client, "billing", "BL", fake_github.clone_url)
    page.set_viewport_size({"width": 1440, "height": 900})
    sign_in(page, web_url, org["api_key"], at="#/sessions")
    page.get_by_test_id("sessions").get_by_test_id("new-session").click()
    # New session is the welcome: nothing is made until the first message is sent.
    expect(page.get_by_test_id("welcome")).to_be_visible()
    expect(page).to_have_url(re.compile(r"#/$"))
    expect(page.get_by_role("dialog")).to_have_count(0)
    assert client.get("/v1/brainstorms").json()["sessions"] == []
    composer = page.get_by_test_id("welcome").locator("textarea")
    expect(composer).to_be_focused()
    expect(composer).to_have_attribute("placeholder", "Start a session: an idea, a question, a plan…")
    # A starter fills the composer and sends nothing.
    page.locator("[data-starter=code]").click()
    expect(composer).to_have_value("How does ")
    expect(composer).to_be_focused()
    assert client.get("/v1/brainstorms").json()["sessions"] == []

    # The first message makes the session with it and opens it, untitled. The scripted agent's
    # reply names the untitled session (never an empty task); once its turn is over nothing thinks.
    session = _welcome_send(page, "where would metering live?")
    expect(page.get_by_test_id("session-title")).to_have_text("New session")
    _title_uncut(page)
    expect(page.get_by_test_id("human-turn").first).to_contain_text("where would metering live?")
    reply = page.get_by_text('In the session "New session". You asked:')
    expect(reply).to_be_visible(timeout=60_000)
    expect(page.get_by_text('Briefed on ""')).to_have_count(0)
    expect(page.get_by_test_id("session-screen").locator("[data-activity]")).to_have_count(0, timeout=15_000)
    # A session has no task: its composer says who it writes to.
    expect(page.get_by_test_id("session-composer").locator("textarea")).to_have_attribute("placeholder", "Message the brainstorm…")
    assert client.get(f"/v1/brainstorms/{session}").json()["session"]["title"] is None
    assert [s["id"] for s in client.get("/v1/brainstorms").json()["sessions"]] == [session]
    expect(page.get_by_test_id("sidebar-sessions").locator(f'[data-session="{session}"]')).to_contain_text("New session")

    # Escape cancels; Enter saves, and the Chat says who named it.
    page.get_by_test_id("session-title").click()
    page.get_by_test_id("session-title-input").fill("Throwaway")
    page.keyboard.press("Escape")
    expect(page.get_by_test_id("session-title")).to_have_text("New session")
    page.get_by_test_id("session-title").click()
    page.get_by_test_id("session-title-input").fill("Billing v2")
    page.keyboard.press("Enter")
    expect(page.get_by_test_id("session-title")).to_have_text("Billing v2")
    _title_uncut(page)
    expect(page.locator('[data-kind="renamed"]')).to_contain_text("renamed it “Billing v2”", timeout=15_000)
    assert client.get(f"/v1/brainstorms/{session}").json()["session"]["title"] == "Billing v2"
    # The tab never carries the name.
    assert "Billing" not in page.title()


@pytest.mark.ui
def test_a_project_linked_in_the_welcomes_composer_is_what_the_session_it_makes_reads(
        client: ApiClient, page: Page, web_url: str, org: dict, fake_github: FakeGitHub):
    _scripted_brainstorm(client)
    billing = _project(client, "billing", "BL", fake_github.clone_url)
    web = _project(client, "web-console", "WC", fake_github.add_repository("web").clone_url)
    page.set_viewport_size({"width": 1440, "height": 900})
    sign_in(page, web_url, org["api_key"], at="#/")
    links = page.get_by_test_id("composer-links")
    expect(links).to_contain_text("Reads memory only")
    # Linked, then unlinked, then linked: only what is linked at send time is read.
    for count, name in enumerate(("web-console", "billing"), start=1):
        page.get_by_test_id("composer-link").click()
        page.get_by_role("menuitem", name=name).click()
        # Picked and closed before the next press opens it again.
        expect(links.locator("[data-project]")).to_have_count(count)
        expect(page.get_by_role("menu")).to_have_count(0)
    page.get_by_role("button", name="Stop reading web-console").click()
    expect(links.locator("[data-project]")).to_have_count(1)
    expect(links).to_contain_text("billing")
    session = _welcome_send(page, "where does metering go?")
    detail = client.get(f"/v1/brainstorms/{session}").json()["session"]
    # The project with all its repositories; the session's agent was started by the same call.
    assert [(p["id"], [r["id"] for r in p["repositories"]]) for p in detail["projects"]] == \
        [(billing["id"], [r["id"] for r in billing["repositories"]])]
    assert web["id"] not in [p["id"] for p in detail["projects"]]
    assert detail["run"] is not None
    # The rail's Linked says so.
    linked = page.get_by_test_id("session-rail")
    expect(linked).to_contain_text("billing")
    expect(linked).not_to_contain_text("web-console")


@pytest.mark.ui
def test_a_shared_sessions_header_on_a_phone_shows_its_name_above_the_meta(
        client: ApiClient, env, page: Page, web_url: str, org: dict):
    """At 375px the shared marker, the model and Share wrap under the name, which keeps a line with
    room for at least "New session"."""
    _scripted_brainstorm(client)
    ana, ana_client = _person(client, env, "Ana Nunes")
    session = client.post("/v1/brainstorms", {}).json()["id"]
    assert client.post(f"/v1/brainstorms/{session}/people", {"people": [ana["id"]], "role": "chat"}).status_code == 200
    assert ana_client.post(f"/v1/brainstorms/{session}/accept").status_code == 200
    client.post(f"/v1/brainstorms/{session}/chat", {"text": "metering"})
    wait_until(lambda: client.get(f"/v1/brainstorms/{session}").json()["session"]["run"], timeout=60, message="no Run")
    page.set_viewport_size({"width": 375, "height": 800})
    sign_in(page, web_url, org["api_key"], at=f"#/sessions/{session}")
    header = page.get_by_test_id("session-screen").locator("header").first
    expect(header.get_by_test_id("share-open")).to_be_visible(timeout=15_000)
    expect(header.get_by_label("Shared with 1")).to_be_visible()
    expect(header).to_contain_text("Brainstorm · fake/scripted")
    m = header.evaluate("""h => {
        const title = h.querySelector('h1'), words = h.querySelector('[data-title-words]');
        const probe = words.cloneNode(false);
        probe.textContent = 'New session';
        probe.style.cssText = 'position:absolute;visibility:hidden;width:max-content';
        h.append(probe);
        const r = {header: h.getBoundingClientRect().width, overflow: h.scrollWidth - h.clientWidth,
                   room: title.getBoundingClientRect().width, need: probe.getBoundingClientRect().width,
                   words: words.getBoundingClientRect().width, cut: words.scrollWidth > words.clientWidth,
                   text: words.textContent, titleBottom: title.getBoundingClientRect().bottom,
                   metaTop: title.nextElementSibling.getBoundingClientRect().top,
                   shareRight: h.querySelector('[data-testid=share-open]').getBoundingClientRect().right};
        probe.remove();
        return r;
    }""")
    assert m["overflow"] <= 0, m
    assert (m["text"], m["cut"]) == ("New session", False) and m["words"] > 0, m
    assert m["room"] > m["need"] > 0, m
    assert m["metaTop"] >= m["titleBottom"] and m["shareRight"] <= m["header"], m


def _published(client: ApiClient, session: str, name: str) -> dict:
    """Ask the session's agent to publish a file, after its first turn, and wait until it is listed."""
    said = len(_said(client, session))
    client.post(f"/v1/brainstorms/{session}/chat",
                {"text": "write it up\n" + _tool("artifact", {"path": name, "content": "# Metering\n\nCount each run once."})})
    wait_until(lambda: len(_said(client, session)) > said, timeout=60, message="the agent never answered")
    return wait_until(lambda: next((a for a in client.get("/v1/artifacts", params={"sessionId": session}).json()["artifacts"]
                                    if a["name"] == name), None), timeout=90, message=f"{name} was never listed")


def test_a_file_published_again_is_its_next_version_with_its_own_description(client: ApiClient, env):
    """dude publish of a name already published is a new version, in lux and
    so in dude: both listed, newest first, each with what the agent said."""
    _scripted_brainstorm(client)
    session = client.post("/v1/brainstorms", {}).json()["id"]
    client.post(f"/v1/brainstorms/{session}/chat", {"text": "metering"})
    wait_until(lambda: _said(client, session), timeout=60, message="the agent never answered")
    client.post(f"/v1/brainstorms/{session}/chat", {"text": "write it up\n" +
        _tool("artifact", {"path": "design.md", "content": "# Draft", "description": "First draft"})})
    wait_until(lambda: client.get("/v1/artifacts", params={"sessionId": session}).json()["artifacts"], timeout=30,
               message="the first version never reached dude")
    client.post(f"/v1/brainstorms/{session}/chat", {"text": "again, with numbers\n" +
        _tool("artifact", {"path": "design.md", "content": "# Metering\n\nWith numbers.", "description": "With the numbers"})})

    def both():
        found = [a for a in client.get("/v1/artifacts", params={"sessionId": session}).json()["artifacts"] if a["name"] == "design.md"]
        return found if len(found) == 2 else None

    found = wait_until(both, timeout=30, message="the second version never reached dude")
    assert [(a["version"], a["versions"], a["description"]) for a in found] == [(2, 2, "With the numbers"), (1, 2, "First draft")]
    assert "With numbers." in client.get(f"/v1/artifacts/{found[0]['id']}/content").text


def test_a_sessions_files_are_what_its_agent_published_for_its_members_alone(client: ApiClient, env):
    _scripted_brainstorm(client)
    reader, reader_client = _person(client, env, "Rita Reader")
    otto, otto_client = _person(client, env, "Otto Other")
    boss, boss_client = _person(client, env, "Bea Boss", role="admin")
    session = client.post("/v1/brainstorms", {}).json()["id"]
    client.post(f"/v1/brainstorms/{session}/people", {"people": [reader["id"]], "role": "read"})
    reader_client.post(f"/v1/brainstorms/{session}/accept")
    client.post(f"/v1/brainstorms/{session}/chat", {"text": "metering"})
    wait_until(lambda: _said(client, session), timeout=60, message="the agent never answered")
    art = _published(client, session, "design.md")
    assert (art["sessionId"], art["taskId"], art["role"]) == (session, None, "brainstorm")

    # A member reads it, and the session's zip has it.
    for who in (client, reader_client):
        assert [a["name"] for a in who.get("/v1/artifacts", params={"sessionId": session}).json()["artifacts"]] == ["design.md"]
        assert "Count each run once." in who.get(f"/v1/artifacts/{art['id']}/content").text
        zipped = who.get(f"/v1/brainstorms/{session}/artifacts.zip")
        assert zipped.status_code == 200 and b"design.md" in zipped.content
    # Anyone else, an admin included, is told the session does not exist.
    for who in (otto_client, boss_client):
        for path, params in ((f"/v1/artifacts", {"sessionId": session}), (f"/v1/artifacts/{art['id']}/content", None),
                             (f"/v1/brainstorms/{session}/artifacts.zip", None)):
            res = who.get(path, params=params)
            assert res.status_code == 404, (path, res.status_code)
            assert "design.md" not in res.text and "Count each run" not in res.text
    # Its artifact.created is on the session: members only.
    assert [e for e in client.events(sessionId=session, limit=1000) if e["eventType"] == "artifact.created"]
    assert not [e for e in otto_client.events(limit=1000) if e["eventType"] == "artifact.created" and e["sessionId"] == session]


@pytest.mark.ui
def test_the_session_rail_lists_its_files_and_one_opens_in_the_viewer(client: ApiClient, page: Page, web_url: str, org: dict):
    _scripted_brainstorm(client)
    session = client.post("/v1/brainstorms", {}).json()["id"]
    client.post(f"/v1/brainstorms/{session}/chat", {"text": "metering"})
    wait_until(lambda: _said(client, session), timeout=60, message="the agent never answered")
    _published(client, session, "design.md")
    sign_in(page, web_url, org["api_key"], at=f"#/sessions/{session}")
    files = page.get_by_test_id("session-files")
    expect(files.get_by_test_id("session-files-count")).to_have_text("1", timeout=15_000)
    files.locator('[data-name="design.md"]').click()
    viewer = page.get_by_test_id("file-viewer")
    expect(viewer).to_be_visible()
    expect(viewer.get_by_test_id("file-content")).to_contain_text("Count each run once.")
    page.keyboard.press("Escape")
    expect(viewer).to_have_count(0)


def test_a_sessions_events_are_every_run_and_its_own_for_its_members_alone(client: ApiClient, env, owner_dsn: str):
    _scripted_brainstorm(client)
    otto, otto_client = _person(client, env, "Otto Other")
    session = client.post("/v1/brainstorms", {}).json()["id"]
    first = client.post(f"/v1/brainstorms/{session}/chat", {"text": "metering"}).json()["runId"]
    wait_until(lambda: _said(client, session), timeout=60, message="the agent never answered")
    # Its agent ends; the next message starts another Run of the same session.
    execute(owner_dsn, "UPDATE runs SET status = 'completed', ended_at = now() WHERE id = %s", (first,))
    second = client.post(f"/v1/brainstorms/{session}/chat", {"text": "again"}).json()["runId"]
    assert second != first
    wait_until(lambda: len(_said(client, session)) >= 2, timeout=60, message="the second Run never answered")
    events = client.events(sessionId=session, limit=1000)
    runs = {e["runId"] for e in events if e["runId"]}
    assert {first, second} <= runs
    assert "session.created" in [e["eventType"] for e in events]
    assert all(e["sessionId"] == session for e in events)
    assert [e["cursor"] for e in events] == sorted(e["cursor"] for e in events)
    # Anyone else: 404, on the history and the live stream alike.
    assert otto_client.get("/v1/events", params={"sessionId": session}).status_code == 404
    res = requests.get(f"{otto_client.base_url}/v1/events/stream", params={"sessionId": session, "key": otto_client.api_key}, timeout=10)
    assert res.status_code == 404


@pytest.mark.ui
def test_the_session_screen_switches_to_its_events(client: ApiClient, page: Page, web_url: str, org: dict):
    _scripted_brainstorm(client)
    session = client.post("/v1/brainstorms", {}).json()["id"]
    client.post(f"/v1/brainstorms/{session}/chat", {"text": "metering"})
    wait_until(lambda: _said(client, session), timeout=60, message="the agent never answered")
    sign_in(page, web_url, org["api_key"], at=f"#/sessions/{session}")
    switch = page.get_by_test_id("session-view")
    expect(switch.get_by_role("button")).to_have_count(2, timeout=15_000)
    expect(switch).not_to_contain_text("Changes")
    switch.get_by_role("button", name="Events").click()
    log = page.get_by_test_id("event-log")
    expect(log).to_contain_text("session.created")
    expect(log).to_contain_text("agent.message")
    expect(page.get_by_test_id("session-composer")).to_have_count(0)
    switch.get_by_role("button", name="Conversation").click()
    expect(page.get_by_test_id("session-composer")).to_be_visible()


@pytest.mark.ui
def test_archiving_a_session_takes_it_out_of_your_list_and_sidebar_until_you_unarchive_it(
        client: ApiClient, page: Page, web_url: str, org: dict):
    _scripted_brainstorm(client)
    kept = client.post("/v1/brainstorms", {"title": "Keep me"}).json()["id"]
    sign_in(page, web_url, org["api_key"], at="#/sessions")
    # New session is the welcome; its first message makes the session and opens it.
    page.get_by_test_id("sessions").get_by_test_id("new-session").click()
    expect(page.get_by_test_id("welcome")).to_be_visible()
    session = _welcome_send(page, "archive me after this")
    sidebar = page.get_by_test_id("sidebar-sessions")
    expect(sidebar.locator(f'[data-session="{session}"]')).to_have_count(1, timeout=15_000)

    page.get_by_test_id("session-archive").click()
    sessions = page.get_by_test_id("sessions")
    expect(sessions).to_be_visible()
    expect(sessions.locator(f'[data-testid=session-row][data-session="{kept}"]')).to_have_count(1)
    expect(sessions.locator(f'[data-testid=session-row][data-session="{session}"]')).to_have_count(0)
    expect(sidebar.locator(f'[data-session="{session}"]')).to_have_count(0)
    expect(sidebar.locator(f'[data-session="{kept}"]')).to_have_count(1)

    page.get_by_test_id("sessions-shown").get_by_role("button", name="Archived").click()
    row = sessions.locator(f'[data-testid=session-row][data-session="{session}"]')
    expect(row).to_have_count(1)
    expect(sessions.locator(f'[data-testid=session-row][data-session="{kept}"]')).to_have_count(0)
    row.get_by_role("button").first.click()
    expect(page.get_by_test_id("session-archived")).to_have_text("Archived")
    page.get_by_test_id("session-unarchive").click()
    expect(sidebar.locator(f'[data-session="{session}"]')).to_have_count(1)
    expect(page.get_by_test_id("session-archived")).to_have_count(0)
    # Read back as the API has it: listed again, not archived.
    listed = {s["id"]: s["archived"] for s in client.get("/v1/brainstorms").json()["sessions"]}
    assert listed.get(session) is False, listed


def _listen(who: ApiClient, frames: list, ready: threading.Event, stop: threading.Event) -> None:
    """Every frame of the organisation's live stream, as `who`'s browser hears it."""
    with requests.get(f"{who.base_url}/v1/events/stream", params={"live": "1", "key": who.api_key},
                      stream=True, timeout=(5, 60)) as res:
        for raw in res.iter_lines(decode_unicode=True):
            if raw and raw.startswith(":"):
                ready.set()
            if stop.is_set():
                return
            if raw and raw.startswith("data:"):
                frames.append(json.loads(raw[5:]))


def test_a_members_change_to_a_session_memory_reaches_no_one_else(client: ApiClient, env):
    """An admin who is in the session renames, archives and restores what its
    agent remembered: the ledger and the live stream give those changes,
    title and all, to the session's members and to nobody else — a pending
    invitee, someone else, an admin not in it."""
    _scripted_brainstorm(client)
    boss, boss_client = _person(client, env, "Bea Boss", role="admin")
    ana, ana_client = _person(client, env, "Ana Nunes")
    otto, otto_client = _person(client, env, "Otto Other")
    admin2, admin2_client = _person(client, env, "Ada Admin", role="admin")
    session = client.post("/v1/brainstorms", {"title": "Acquisitions", "projects": []}).json()["id"]
    client.post(f"/v1/brainstorms/{session}/people", {"people": [boss["id"]], "role": "chat"})
    assert boss_client.post(f"/v1/brainstorms/{session}/accept").status_code == 200
    client.post(f"/v1/brainstorms/{session}/people", {"people": [ana["id"]], "role": "chat"})
    remember = _tool("remember", {"title": "Confidential zephyr acquisition", "content": "Private terms."})
    client.post(f"/v1/brainstorms/{session}/chat", {"text": "note it\n" + remember})
    mid = wait_until(lambda: next((m["id"] for m in client.get("/v1/memory/memories", params={"q": "zephyr"}).json()["memories"]), None),
                     timeout=60, message="the agent never remembered")

    outsiders = {"a pending invitee": ana_client, "someone else": otto_client, "an admin not in it": admin2_client}
    listeners = {name: ([], threading.Event()) for name in [*outsiders, "a member"]}
    stop = threading.Event()
    threads = []
    for name, who in [*outsiders.items(), ("a member", client)]:
        frames, ready = listeners[name]
        th = threading.Thread(target=_listen, args=(who, frames, ready, stop), daemon=True)
        th.start()
        threads.append(th)
    for name, (_, ready) in listeners.items():
        assert ready.wait(10), f"{name}'s stream never opened"
    after = max([e["cursor"] for e in client.get("/v1/events", params={"limit": 1000}).json()["events"]] or [0])

    assert boss_client.patch(f"/v1/memory/memories/{mid}", {"title": "Confidential zephyr acquisition, signed"}).status_code == 200
    assert boss_client.post(f"/v1/memory/memories/{mid}/archive").status_code == 200
    assert boss_client.post(f"/v1/memory/memories/{mid}/restore").status_code == 200
    # A marker everyone hears, after the changes: a project made by the owner.
    marker = client.create_project(name="Marker", slug=f"mk-{os.urandom(3).hex()}")
    heard = lambda frames: any(e.get("projectId") == marker["id"] for e in frames)  # noqa: E731
    wait_until(lambda: all(heard(frames) for frames, _ in listeners.values()), timeout=30, message="the marker never reached every stream")
    stop.set()

    changes = {"memory.updated", "memory.archived", "memory.restored"}
    member = [e["eventType"] for e in listeners["a member"][0] if e.get("payload", {}).get("memoryId") == mid]
    assert sorted(member) == sorted(changes), member
    for name, who in outsiders.items():
        frames = listeners[name][0]
        assert not [e for e in frames if e.get("payload", {}).get("memoryId") == mid], (name, frames)
        assert "zephyr" not in json.dumps(frames).lower(), name
        history = who.get("/v1/events", params={"after": after, "limit": 1000}).json()["events"]
        assert history, f"{name} reads nothing after the changes: the check would be empty"
        assert not [e for e in history if e["eventType"] in changes], (name, history)
        assert "zephyr" not in json.dumps(history).lower(), name
    mine = client.get("/v1/events", params={"after": after, "limit": 1000}).json()["events"]
    assert sorted(e["eventType"] for e in mine if e["eventType"] in changes) == sorted(changes)
