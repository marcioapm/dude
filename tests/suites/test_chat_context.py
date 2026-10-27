"""The live case, end to end: a CI bot's nightly report in a Slack channel,
then a top-level `@dude investigate last night's failures above`.

Through the deployed processes, the fake Slack and the fake GitHub: an
admin says from Slack what the channel is usually about; a CI bot posts a
failure report whose GitHub Actions run link is only in an attachment; a
member mentions dude at the channel's top level (not in a thread). Routing
reads the channel before the mention (the report included, by the bot's
name) and the channel's hint, and starts a session with that project
linked. The session's agent (the scripted fake agent, told by a member's
"tool:" lines) lists the run's jobs with `github_run_jobs` from the link and
reads the failed job's log with `github_check_log`.

A project hidden from Slack never reaches routing, a session started there
cannot link it, and its repository is refused to that session's tools.

Routing here is tests/env.py's judge stub, not a model: for an unspecific
message it picks the project the routing input's "## This channel is
usually about" hint names. So this suite proves the plumbing (the channel,
the app's attachment link and the hint reach the recorded routing input;
the session's tools read the job log), not a real model's choice; the Go
tests in internal/chat assert the routing input's sections.
"""

from __future__ import annotations

import json
import os

from fake_github import FakeGitHub
from helpers import ApiClient, provision_person, query, wait_until
from suites.test_chat_providers import Slack, _authorized, _oauth, _requests


def _said(client: ApiClient, session: str) -> list[str]:
    return [e["payload"]["text"] for e in client.events(sessionId=session, limit=1000) if e["eventType"] == "agent.message"]


def _tool(name: str, args: dict) -> str:
    return f"tool: {name} {json.dumps(args)}"


def _nightly(client: ApiClient, env, org: dict, fake_github: FakeGitHub) -> dict:
    """Acme with Slack installed (Márcia, admin, U1; Eli, member, U2), GitHub
    on the fake, the scripted brainstorm, and two projects: Nightly API (its
    repository the fake GitHub's, with a failing run) and Vault (another
    repository of the same owner)."""
    resp = client.post("/v1/forge/credential", {"auth": "pat", "secret": "fake-token", "apiBaseUrl": fake_github.api_url})
    assert resp.status_code == 200, resp.text
    scripted = client.tier_for("fake/scripted")
    assert client.patch("/v1/settings/organization", {"roles": {"brainstorm": {"tier": scripted}}}).status_code == 200
    thinker = {t["name"]: t for t in client.get("/v1/models/tiers").json()["tiers"]}["Thinker"]["id"]
    assert client.put("/v1/judgements/settings", {"tierId": thinker}).status_code == 200

    team = "T" + os.urandom(3).hex().upper()
    slack = Slack(env, team)
    u = {k: f"{k}{team}" for k in ("U1", "U2")}
    slack.user(u["U1"], "marcia", "Márcia Costa")
    slack.user(u["U2"], "eli", "Eli Park")
    slack.conversation(f"C{team}", "nightly", "public", list(u.values()))
    tag = os.urandom(2).hex()
    _, marcia = provision_person(client, env, "Márcia Costa", f"marcia-{tag}@example.com", "admin")
    _, eli = provision_person(client, env, "Eli Park", f"eli-{tag}@example.com")
    slack.authorize_as(installer=u["U1"])
    assert "outcome=installed" in _oauth(marcia, "/v1/chat/slack/install")
    for who, uid in ((marcia, u["U1"]), (eli, u["U2"])):
        slack.authorize_as(signer=uid)
        assert "outcome=connected" in _oauth(who, "/v1/chat/slack/connect")

    fake_github.add_repository("vault")
    keys = {"api": f"N{tag[:3].upper()}", "vault": f"V{tag[:3].upper()}"}
    api = client.create_project(name="Nightly API", slug=f"nightly-{tag}", key=keys["api"],
                                repositories=[{"name": fake_github.repo, "url": fake_github.clone_url, "defaultBranch": "main"}])
    vault_url = fake_github.clone_url.replace(f"/{fake_github.repo}.git", "/vault.git")
    vault = client.create_project(name="Vault", slug=f"vault-{tag}", key=keys["vault"],
                                  repositories=[{"name": "vault", "url": vault_url, "defaultBranch": "main"}])
    return {"slack": slack, "u": u, "team": team, "marcia": marcia, "eli": eli, "api": api, "vault": vault, "keys": keys, "org": org}


def _report(n: dict, link: str) -> None:
    """The CI bot's nightly report: the run's link only in an attachment."""
    n["slack"]._post("post-app", {"Channel": f"C{n['team']}", "BotID": "BCIBOT", "Name": "CI Bot", "Text": "Nightly build failed",
                                  "Attachments": [{"title": "Nightly #812 failed", "title_link": link,
                                                   "text": "1 of 2 jobs failed: integration", "footer": "GitHub Actions"}],
                                  "Blocks": [{"type": "section", "text": {"type": "mrkdwn", "text": "Nightly build failed"}}]})


def test_a_top_level_mention_under_a_ci_report_opens_a_session_that_reads_the_failed_jobs_log(
        client: ApiClient, env, owner_dsn: str, org: dict, fake_github: FakeGitHub):
    n = _nightly(client, env, org, fake_github)
    slack, u, team, keys = n["slack"], n["u"], n["team"], n["keys"]
    channel = f"C{team}"
    # The channel is usually about Nightly API, said from Slack by an admin.
    slack.mention(channel, "", u["U1"], f"this channel is usually about {keys['api']}")
    [usual] = _requests(n["marcia"], 1)
    assert usual["route"] == "channel_projects" and usual["result"] is None
    view = n["eli"].get("/v1/organization/chat/slack/projects").json()
    assert [p["key"] for c in view["channels"] if c["conversationId"] == channel for p in c["projects"]] == [keys["api"]]

    link = fake_github.add_actions_run(812, "Nightly", [
        {"id": 9001, "name": "lint", "conclusion": "success"},
        {"id": 9002, "name": "integration", "conclusion": "failure", "failed_steps": ["Run tests"],
         "log": "2026-10-26T03:00:01.000Z FAIL test_export: timed out after 30s\n"
                "2026-10-26T03:00:02.000Z ##[error]Process completed with exit code 1.\n"}])
    _report(n, link)
    # At the channel's top level, not in a thread.
    slack.mention(channel, "", u["U2"], "can you investigate last night's failures above?")
    req = _requests(n["marcia"], 2)[1]
    assert req["route"] == "brainstorm" and req["result"]["kind"] == "session", req
    session = req["result"]["id"]

    # Routing read the channel before the mention, the bot's report and its link included, and the hint.
    [call] = query(owner_dsn, """SELECT request::text AS request FROM model_calls m JOIN chat_requests r ON r.model_call_id = m.id
        WHERE r.result_id = %s""", (session,))
    for want in ("## The channel before it", "CI Bot (an app): Nightly build failed", link, "## This channel is usually about"):
        assert want in call["request"], want
    # The session has the project linked, and its briefing quotes the channel with the link.
    linked = query(owner_dsn, "SELECT project_id FROM session_projects WHERE session_id = %s", (session,))
    assert linked == [{"project_id": n["api"]["id"]}]
    [briefing] = query(owner_dsn, "SELECT prompt FROM runs WHERE session_id = %s ORDER BY created_at LIMIT 1", (session,))
    assert "Earlier in the channel, before this message" in briefing["prompt"] and link in briefing["prompt"]
    wait_until(lambda: _said(n["eli"], session), timeout=60, message="the session's agent never answered")

    # Its agent reads the run from the report's link, then the failed job's log.
    said = len(_said(n["eli"], session))
    lines = ["what failed last night?",
             _tool("github_run_jobs", {"runId": link}),
             'tool: say {"text": "Jobs: {{last.jobs.1.name}} {{last.jobs.1.conclusion}} at {{last.jobs.1.failedSteps.0}}"}',
             'tool: github_check_log {"repo": "' + f"{fake_github.owner}/{fake_github.repo}" + '", "checkId": {{last.jobs.1.id}}}',
             'tool: say {"text": "Log: {{last.log}}"}']
    resp = n["eli"].post(f"/v1/brainstorms/{session}/chat", {"text": "\n".join(lines)})
    assert resp.status_code in (200, 201), resp.text
    answer = wait_until(lambda: "\n".join(_said(n["eli"], session)[said:]) if "Log:" in "\n".join(_said(n["eli"], session)[said:]) else None,
                        timeout=60, message="the agent never said what it read")
    assert "dude github_" not in answer, answer  # no tool failed
    jobs = next(line for line in answer.splitlines() if line.startswith("Jobs:"))
    assert "integration" in jobs and "failure" in jobs and "Run tests" in jobs, jobs
    log = next(line for line in answer.splitlines() if line.startswith("Log:"))
    assert "FAIL test_export: timed out after 30s" in log and "⟦external " in log, log
    reads = [e["payload"] for e in n["eli"].events(sessionId=session, limit=1000) if e["eventType"] == "github.read"]
    assert [r["tool"] for r in reads] == ["github_run_jobs", "github_check_log"], reads
    assert "timed out" not in json.dumps(reads)


def test_a_project_hidden_from_slack_is_never_routed_to_linked_or_read(
        client: ApiClient, env, owner_dsn: str, org: dict, fake_github: FakeGitHub):
    n = _nightly(client, env, org, fake_github)
    slack, u, team, keys = n["slack"], n["u"], n["team"], n["keys"]
    channel = f"C{team}"
    # Only an admin hides a project, on the settings page.
    hide = {"projectIds": [n["vault"]["id"]]}
    assert _authorized(n["eli"], "PUT", "/v1/organization/chat/slack/hidden-projects", json=hide).status_code == 403
    assert _authorized(n["marcia"], "PUT", "/v1/organization/chat/slack/hidden-projects", json=hide).status_code == 200
    link = fake_github._for_repo("vault").add_actions_run(77, "Vault nightly", [
        {"id": 7701, "name": "rotate", "conclusion": "failure", "log": "the vault's secrets\n"}])

    slack.mention(channel, "", u["U2"], f"what is wrong with {keys['vault']} and {link}?")
    [req] = _requests(n["marcia"], 1)
    session = req["result"]["id"]
    [call] = query(owner_dsn, "SELECT request::text AS request FROM model_calls WHERE id = (SELECT model_call_id FROM chat_requests WHERE result_id = %s)",
                   (session,))
    assert f"- {keys['vault']} (Vault)" not in call["request"] and f"- {keys['api']} (Nightly API)" in call["request"]
    assert query(owner_dsn, "SELECT count(*) AS n FROM session_projects WHERE session_id = %s AND project_id = %s",
                 (session, n["vault"]["id"])) == [{"n": 0}]
    # A member linking it by hand is refused, saying why.
    linked = n["eli"].post(f"/v1/brainstorms/{session}/link", {"projects": [
        {"projectId": n["vault"]["id"], "repositoryIds": [n["vault"]["repositories"][0]["id"]]}]})
    assert linked.status_code in (403, 404, 409), linked.text
    if linked.status_code == 409:
        assert "hidden" in linked.text, linked.text
    assert query(owner_dsn, "SELECT count(*) AS n FROM session_projects WHERE session_id = %s AND project_id = %s",
                 (session, n["vault"]["id"])) == [{"n": 0}]

    wait_until(lambda: _said(n["eli"], session), timeout=60, message="the session's agent never answered")
    said = len(_said(n["eli"], session))
    vault_slug = f"{fake_github.owner}/vault"
    lines = ["read the vault's run",
             _tool("github_run_jobs", {"runId": link}),
             _tool("github_check_log", {"repo": vault_slug, "checkId": 7701}),
             _tool("list_tasks", {"project": keys["vault"]}),
             'tool: say {"text": "done"}']
    assert n["eli"].post(f"/v1/brainstorms/{session}/chat", {"text": "\n".join(lines)}).status_code in (200, 201)
    answer = wait_until(lambda: "\n".join(_said(n["eli"], session)[said:]) if "done" in "\n".join(_said(n["eli"], session)[said:]) else None,
                        timeout=60, message="the agent never finished")
    for tool in ("github_run_jobs", "github_check_log", "list_tasks"):
        assert f"dude {tool} answered 422" in answer, (tool, answer)
    # Nothing reached GitHub for the hidden repository.
    assert not [e for e in n["eli"].events(sessionId=session, limit=1000) if e["eventType"] == "github.read"]
