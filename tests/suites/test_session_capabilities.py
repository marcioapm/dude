"""A brainstorm proposes a project to link, and asks again for a checkout that failed.

Through the deployed processes, against the stand-in GitHub and fake lux.
The scripted agent carries out the "tool:" lines of a member's message, as
a real agent would decide to, and says what a tool answered with
"say {{last.PATH}}".

In the first, a session linked to one project is asked about another; its
agent proposes linking it, the owner confirms the card through the API,
and the agent's next turn reads the project checked out. In the second, a
repository linked to a running session cannot be cloned; the agent is told
so without asking, the cause is fixed, and its refresh_checkout gets it
checked out at the next resume.
"""

from __future__ import annotations

import json
import os

from fake_github import FakeGitHub
from helpers import ApiClient, execute, query, wait_until


def _tool(name: str, args: dict) -> str:
    return f"tool: {name} {json.dumps(args)}"


def _said(client: ApiClient, session: str) -> list[str]:
    return [e["payload"]["text"] for e in client.get("/v1/events", params={"sessionId": session, "limit": 1000}).json()["events"]
            if e["eventType"] == "agent.message"]


def _scripted_brainstorm(client: ApiClient) -> None:
    tier = client.tier_for("fake/scripted")
    resp = client.patch("/v1/settings/organization", {"roles": {"brainstorm": {"tier": tier}}})
    assert resp.status_code == 200, resp.text


def _project(client: ApiClient, name: str, key: str, clone_url: str) -> dict:
    project = client.create_project(name=name, slug=f"{key.lower()}-{int.from_bytes(os.urandom(3)):08d}",
                                    repositories=[{"name": name, "url": clone_url, "defaultBranch": "main"}])
    return client.get(f"/v1/projects/{project['id']}").json()


def _turn(client: ApiClient, session: str, text: str, marker: str) -> str:
    """Writes text to the session and answers the agent's reply that has marker: a resume's own notice (a
    repository linked, or asked for again) may be a turn of its own before it."""
    said = len(_said(client, session))
    resp = client.post(f"/v1/brainstorms/{session}/chat", {"text": text})
    assert resp.status_code in (200, 201), resp.text
    return wait_until(lambda: next((t for t in _said(client, session)[said:] if marker in t), None), timeout=90,
                      message=f"the session's agent never said {marker!r}: {_said(client, session)[said:]}")


def _held(owner_dsn: str, run: str) -> list[str]:
    return query(owner_dsn, "SELECT lux_repositories FROM runs WHERE id = %s", (run,))[0]["lux_repositories"]


def test_a_brainstorm_proposes_a_link_the_owner_confirms_and_its_next_turn_reads_it(
        client: ApiClient, owner_dsn: str, fake_github: FakeGitHub):
    _scripted_brainstorm(client)
    billing = _project(client, "billing", "BL", fake_github.clone_url)
    web = _project(client, "web-console", "WC", fake_github.add_repository("web").clone_url)
    session = client.post("/v1/brainstorms", {"title": "Console usage", "projects": [
        {"projectId": billing["id"], "repositoryIds": [billing["repositories"][0]["id"]]}]}).json()["id"]
    run = client.post(f"/v1/brainstorms/{session}/chat", {"text": "where does the console show usage?"}).json()["runId"]
    wait_until(lambda: _said(client, session), timeout=60, message="the agent never answered")
    briefing = query(owner_dsn, "SELECT prompt FROM runs WHERE id = %s", (run,))[0]["prompt"]
    assert "## The organisation's other projects" in briefing and "- WC (web-console)" in briefing, briefing

    _turn(client, session, "it is in the console\n" + _tool("propose", {"items": [
        {"kind": "link_project", "project": "WC", "reason": "The usage panel lives in the console."}]}), "it is in the console")
    card = wait_until(lambda: client.get(f"/v1/brainstorms/{session}").json()["proposals"], timeout=30, message="no card")[-1]
    assert card["items"] == [{"kind": "link_project", "project": "WC", "projectName": "web-console",
                              "reason": "The usage panel lives in the console."}], card
    assert card["status"] == [{"canFile": True}], card
    assert query(owner_dsn, "SELECT count(*) AS n FROM session_projects WHERE session_id = %s", (session,))[0]["n"] == 1

    # The owner confirms it on the card: the session links WC, through the link API's own write.
    results = client.post(f"/v1/brainstorms/{session}/file", {"proposalId": card["id"], "items": [0]}).json()["results"]
    assert results == [{"item": 0, "status": "filed", "key": "WC", "kind": "link_project"}], results
    assert sorted(p["key"] for p in client.get(f"/v1/brainstorms/{session}").json()["session"]["projects"]) == ["BL", "WC"]
    linked = [e for e in client.get("/v1/events", params={"sessionId": session, "limit": 1000}).json()["events"]
              if e["eventType"] == "session.linked"]
    assert len(linked) == 1 and len(linked[0]["payload"]["projects"]) == 2, linked

    # Its next turn reads WC, checked out where it is told: the resume that brings it says so.
    reply = _turn(client, session, "what is in the console?\n" + _tool("list_repositories", {"project": "WC"}) + "\n" +
                  _tool("say", {"text": "WC path: {{last.0.path}}"}), "WC path:")
    assert "WC path: /workspace/repos/WC/web-console" in reply, reply
    assert any(h.startswith("wc-web") for h in _held(owner_dsn, run)), _held(owner_dsn, run)
    assert any("A member linked WC/web-console; it is checked out at /workspace/repos/WC/web-console" in t
               for t in _said(client, session)), _said(client, session)


def test_a_failed_checkout_is_told_and_refresh_checkout_recovers_it(client: ApiClient, owner_dsn: str, fake_github: FakeGitHub):
    _scripted_brainstorm(client)
    billing = _project(client, "billing", "BL", fake_github.clone_url)
    web = _project(client, "web-console", "WC", fake_github.add_repository("web").clone_url)
    repo = web["repositories"][0]["id"]
    session = client.post("/v1/brainstorms", {"title": "Console usage", "projects": [
        {"projectId": billing["id"], "repositoryIds": [billing["repositories"][0]["id"]]}]}).json()["id"]
    run = client.post(f"/v1/brainstorms/{session}/chat", {"text": "start"}).json()["runId"]
    wait_until(lambda: _said(client, session), timeout=60, message="the agent never answered")

    # A branch the repository does not have: lux's clone fails, and the agent is told without asking.
    execute(owner_dsn, "UPDATE repositories SET default_branch = 'no-such-branch' WHERE id = %s", (repo,))
    resp = client.post(f"/v1/brainstorms/{session}/link", {"projects": [
        {"projectId": billing["id"], "repositoryIds": [billing["repositories"][0]["id"]]},
        {"projectId": web["id"], "repositoryIds": [repo]}]})
    assert resp.status_code == 200, resp.text
    wait_until(lambda: query(owner_dsn, "SELECT 1 FROM events WHERE run_id = %s AND event_type = 'repository.clone_failed'", (run,)),
               timeout=90, message="the clone never failed")
    told = wait_until(lambda: [e["payload"]["text"] for e in client.get("/v1/events", params={"sessionId": session, "limit": 1000})
                               .json()["events"] if e["eventType"] == "session.told"], timeout=30, message="the agent was not told")
    assert told[-1] == ("WC/web-console could not be checked out (ref not found). It is not in /workspace; a member can check the "
                        "repository's URL and access and link it again, or you can ask for it again with `refresh_checkout` "
                        "once the cause is fixed."), told
    assert query(owner_dsn, "SELECT count(*) AS n FROM session_repositories WHERE repository_id = %s", (repo,))[0]["n"] == 0

    # The cause fixed, the agent asks again: it is told why it failed, and is checked out at its next resume.
    execute(owner_dsn, "UPDATE repositories SET default_branch = 'main' WHERE id = %s", (repo,))
    reply = _turn(client, session, "try the console again\n" + _tool("refresh_checkout", {"project": "WC"}) + "\n" +
                  _tool("say", {"text": "status {{last.repositories.0.status}}; git {{last.repositories.0.lastFailure.git}}"}),
                  "status ")
    assert "status requested; git ⟦external " in reply and "ref not found" in reply, reply
    # Its next turn holds it: the resume dude asked for clones it, telling the agent it was asked for again.
    reply = _turn(client, session, "and now?\n" + _tool("list_repositories", {"project": "WC"}) + "\n" +
                  _tool("say", {"text": "WC path: {{last.0.path}}"}), "WC path:")
    assert "WC path: /workspace/repos/WC/web-console" in reply, reply
    assert any(h.startswith("wc-web") for h in _held(owner_dsn, run)), _held(owner_dsn, run)
    assert any("dude asked again for WC/web-console, as you asked" in t for t in _said(client, session)), _said(client, session)
    assert query(owner_dsn, "SELECT count(*) AS n FROM events WHERE session_id = %s AND event_type = 'session.checkout.requested'",
                 (session,))[0]["n"] == 1
    assert query(owner_dsn, "SELECT count(*) AS n FROM events WHERE run_id = %s AND event_type = 'repository.clone_failed'",
                 (run,))[0]["n"] == 1
