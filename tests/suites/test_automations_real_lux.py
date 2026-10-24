"""Automations against a real lux: containers on Podman hosts, lux-fake as
the agent, previews on lux's own domain.

The fake-lux suite (test_automations.py) proves what dude decides; this one
proves that what it decides happens in real containers: a Command's event
file and environment, a Server automation that actually serves, an agent's
tools through the real tool server, resumed and expired sessions, and the
concurrency modes ending and feeding real Runs.

Opt-in with `run_tests.py --lux` against a lux started with
`--serve --preview-local PORT` (so it has a preview domain). Agent tools
also need DUDE_TEST_TOOLS_HOST: an address of this machine lux's hosts reach.
"""

from __future__ import annotations

import json
import os
import time

import pytest
import requests

from fake_github import FakeGitHub
from helpers import ApiClient, execute, query, wait_until, webhook_secret

pytestmark = pytest.mark.lux

FAKE_IMAGE = "localhost/lux-fake:test"
ENDED = ("succeeded", "failed", "skipped", "cancelled")


def _lux(env, method: str, path: str, body=None) -> requests.Response:
    return requests.request(method, env.real_lux["luxd_url"] + path, json=body, timeout=30,
                            headers={"authorization": f"Bearer {env.real_lux['api_key']}"})


def _lux_state(env, run_id: str) -> str:
    return _lux(env, "GET", f"/v1/runs/{run_id}").json()["state"]


@pytest.fixture
def lux_forge(client: ApiClient, env, org: dict):
    """The stand-in GitHub on the address lux's hosts reach, delivering signed
    webhooks to dude, and a project on its repository."""
    gh = FakeGitHub(env.git_root, owner=f"o{os.urandom(4).hex()}", listen=env.real_lux["gateway"])
    gh.start()
    resp = client.post("/v1/forge/credential", {"auth": "pat", "secret": "fake-token", "apiBaseUrl": gh.api_url})
    assert resp.status_code == 200, resp.text
    gh.webhook_url = env.control_plane_url + resp.json()["webhookPath"]
    gh.webhook_secret = webhook_secret(env.owner_dsn, org["id"])
    project = client.create_project(
        name="Automated", slug=f"auto-{os.urandom(3).hex()}",
        agentModels=client.on_models({r: "fake/scripted" for r in ("implementer", "reviewer", "simplifier")}),
        deliveryPolicy={"requiredReviewers": ["correctness"], "test": False},
        repositories=[{"name": "target", "url": gh.clone_url, "defaultBranch": "main"}],
    )
    yield project, gh
    gh.stop()


def _automation(client: ApiClient, kind: str, settings: dict, trigger: str = "github", project_id: str | None = None) -> dict:
    body = {"name": settings["name"], "kind": kind, "trigger": trigger}
    if project_id:
        body["projectId"] = project_id
    created = client.post("/v1/automations", body)
    assert created.status_code == 201, created.text
    a = created.json()
    saved = client.put(f"/v1/automations/{a['id']}", {"kind": kind, **settings})
    assert saved.status_code == 200, saved.text
    on = client.put(f"/v1/automations/{a['id']}/enabled", {"enabled": True})
    assert on.status_code == 200, on.text
    return on.json()


def _runs(client: ApiClient, automation_id: str) -> list[dict]:
    resp = client.get(f"/v1/automations/{automation_id}/runs")
    assert resp.status_code == 200, resp.text
    return resp.json()["runs"]


def _detail(client: ApiClient, automation_id: str, run_id: str) -> dict:
    resp = client.get(f"/v1/automations/{automation_id}/runs/{run_id}")
    assert resp.status_code == 200, resp.text
    return resp.json()


def _lux_run_of(env, run_id: str) -> str:
    rows = query(env.owner_dsn, "SELECT lux_run_id FROM automation_runs WHERE id = %s", (run_id,))
    return (rows[0]["lux_run_id"] if rows else None) or ""


def _ended(client: ApiClient, automation_id: str, run_id: str, timeout: float = 180) -> dict:
    return wait_until(lambda: next((r for r in _runs(client, automation_id) if r["id"] == run_id and r["status"] in ENDED), None),
                      timeout=timeout, interval=1, message=f"run {run_id} never ended")


def _push(gh: FakeGitHub, branch: str = "main", sender: str = "ana") -> str:
    """A signed push; its delivery id (the GUID dude stores it by)."""
    assert gh.send_webhook("push", {"ref": f"refs/heads/{branch}", "sender": {"login": sender}}) == 202
    return gh.last_delivery_id


# ---------------------------------------------------------------------------
# 1. Command on push
# ---------------------------------------------------------------------------

COMMAND = ('echo "name=$DUDE_EVENT_NAME action=[$DUDE_EVENT_ACTION] delivery=$DUDE_DELIVERY_ID path=$DUDE_EVENT_PATH '
           'automation=$DUDE_AUTOMATION run=$DUDE_AUTOMATION_RUN"; echo "ref=$(grep -o \'refs/heads/[a-z/]*\' "$DUDE_EVENT_PATH")"; '
           'echo "cwd=$(pwd)"; ls')


def test_a_command_on_push_sees_its_event_file_and_env_in_a_real_container(client: ApiClient, env, lux_forge):
    project, gh = lux_forge
    a = _automation(client, "command", {
        "name": "Push env",
        "trigger": {"kind": "github", "event": "push", "actions": [], "filters": {"branch": ["main"]}},
        "command": COMMAND,
    }, project_id=project["id"])

    # A push to another branch: the branch filter skips it (no run at all).
    _push(gh, branch="feature/x")
    delivery = _push(gh, branch="main")

    run = wait_until(lambda: next((r for r in _runs(client, a["id"]) if r["status"] in ENDED), None),
                     timeout=240, interval=2, message="the push's command never ended on lux")
    detail = _detail(client, a["id"], run["id"])
    log = detail.get("logTail") or ""
    assert run["status"] == "succeeded", (run, log)
    assert f"name=push action=[] delivery={delivery} path=/workspace/.dude/event.json" in log, log
    assert f"automation={a['id']} run={run['id']}" in log, log
    assert "ref=refs/heads/main" in log, log
    # It ran in the project's checkout: the repository's README is there.
    assert "cwd=/workspace/repos/target" in log and "README" in log, log
    # Only the main push ran: the feature push was filtered out.
    time.sleep(3)
    runs = _runs(client, a["id"])
    assert [r["id"] for r in runs] == [run["id"]], runs
    # The container really ended.
    lux_run = _lux_run_of(env, run["id"])
    assert _lux_state(env, lux_run) in ("succeeded", "cancelled", "terminated"), lux_run


# ---------------------------------------------------------------------------
# 2. Server on push
# ---------------------------------------------------------------------------

# A server in busybox: every request is answered 200 by a handler that keeps
# the request line and body in /tmp/got (for the run's own requests).
SERVER = (
    "printf '%s\\n' '#!/bin/sh' "
    "'read -r line; len=0; while read -r h; do h=$(printf %s \"$h\" | tr -d \"\\r\"); [ -z \"$h\" ] && break; "
    "case \"$h\" in [Cc]ontent-[Ll]ength:*) len=${h#*: };; esac; done; "
    "body=$(head -c \"$len\"); echo \"$line $body\" >> /tmp/got; "
    "printf \"HTTP/1.1 200 OK\\r\\nContent-Length: 2\\r\\nConnection: close\\r\\n\\r\\nok\"' > /tmp/h.sh; "
    "chmod +x /tmp/h.sh; while true; do nc -l -p 8080 -e /tmp/h.sh; done"
)


@pytest.mark.parametrize("session", ["ephemeral", "resumable"])
def test_a_server_automation_serves_on_real_lux_and_stops_when_idle(client: ApiClient, env, lux_forge, session: str):
    project, gh = lux_forge
    a = _automation(client, "server", {
        "name": f"Push server {session}",
        "trigger": {"kind": "github", "event": "push", "actions": [], "filters": {}},
        "server": {"command": SERVER, "port": 8080, "path": "/hook", "health": "/health"},
        "sessionMode": session, **({"expiryDays": 7} if session == "resumable" else {}),
    }, project_id=project["id"])

    first = _push(gh)

    def posted():
        runs = _runs(client, a["id"])
        if not runs:
            return None
        d = _detail(client, a["id"], runs[0]["id"])
        if d["status"] in ENDED and not d["requests"]:
            raise AssertionError(f"the server run ended without a request: {d['status']} {d.get('reason')}")
        return d if d["requests"] else None

    try:
        detail = wait_until(posted, timeout=240, interval=2, message="dude never POSTed the push to the server")
    finally:
        runs = _runs(client, a["id"])
        print("runs:", [(r["status"], r.get("reason")) for r in runs])
    req = detail["requests"][0]
    assert req["deliveryId"] == first and 200 <= (req["httpStatus"] or 0) < 300, detail["requests"]
    assert req["responseExcerpt"] == "ok", req
    assert detail["containerStartedAt"], detail

    # Idle 30 s after the last request: the container is stopped, the run ends.
    t_last = time.time()
    ended = _ended(client, a["id"], detail["id"], timeout=120)
    idle_for = time.time() - t_last
    assert ended["status"] == "succeeded", ended
    detail = _detail(client, a["id"], detail["id"])
    assert detail["stopReason"] == "idle" and detail["containerStoppedAt"], detail
    assert idle_for >= 25, idle_for
    lux_run = _lux_run_of(env, detail["id"])
    want = ("stopped",) if session == "resumable" else ("cancelled", "terminated")
    wait_until(lambda: _lux_state(env, lux_run) in want, timeout=120, interval=1,
               message=f"the server's lux Run never reached {want}: {_lux_state(env, lux_run)}")

    # A second push starts it again: the kept container, when resumable.
    second = _push(gh)
    run2 = wait_until(lambda: next((r for r in _runs(client, a["id"]) if r["id"] != detail["id"]), None),
                      timeout=30, interval=1, message="the second push started nothing")
    d2 = wait_until(lambda: (lambda d: d if d["requests"] else None)(_detail(client, a["id"], run2["id"])),
                    timeout=240, interval=2, message="the second push was never POSTed")
    assert d2["requests"][0]["deliveryId"] == second and 200 <= (d2["requests"][0]["httpStatus"] or 0) < 300, d2
    if session == "resumable":
        assert d2["session"] == "resumed" and _lux_run_of(env, run2["id"]) == lux_run, d2
    else:
        assert d2["session"] == "ephemeral" and _lux_run_of(env, run2["id"]) != lux_run, d2
    _ended(client, a["id"], run2["id"], timeout=120)


# ---------------------------------------------------------------------------
# 3. An agent calling its tools
# ---------------------------------------------------------------------------

needs_tools = pytest.mark.skipif(not os.environ.get("DUDE_TEST_TOOLS_HOST"),
                                 reason="needs DUDE_TEST_TOOLS_HOST: an address of this machine lux's hosts can reach")


def _agent(client: ApiClient, project_id: str, name: str, prompt: str, event: str = "issues", **settings) -> dict:
    tier = client.tier_for("fake/scripted")
    return _automation(client, "agent", {
        "name": name,
        "trigger": {"kind": "github", "event": event, "actions": [], "filters": {}},
        "agent": {"own": {"tierId": tier, "prompt": prompt}},
        **settings,
    }, project_id=project_id)


def _tool_events(env, run_id: str) -> list[dict]:
    return query(env.owner_dsn, """SELECT event_type, payload FROM events WHERE payload->>'automationRunId' = %s
        AND event_type IN ('agent.tool.dude', 'agent.tool.attempted') ORDER BY occurred_at, id""", (run_id,))


@needs_tools
def test_an_agent_automation_calls_its_tools_on_real_lux_scoped_to_its_repositories(client: ApiClient, env, lux_forge):
    project, gh = lux_forge
    issue = gh.open_issue("The greeting is wrong", body="It says hullo.")
    # The tools' calls, as the scripted agent's prompt names them (fakeagent.AutomationScript).
    prompt = "\n".join([
        "Triage issue {{event.payload}}",
        'tool: github_issue {"repo": "%s/%s", "number": %d}' % (gh.owner, gh.repo, issue),
        'tool: create_task {"title": "Fix the greeting", "goal": "The greeting says hello, not hullo, on the home page."}',
        'tool: github_comment {"body": "Filed a task for this."}',
        'tool: github_comment {"repo": "elsewhere/outside", "number": 7, "body": "Out of scope."}',
    ])
    a = _agent(client, project["id"], "Issue triage", prompt)
    assert gh.send_webhook("issues", {"action": "opened", "issue": {"number": issue, "title": "The greeting is wrong"},
                                      "sender": {"login": "ana"}}) == 202
    run = wait_until(lambda: next((r for r in _runs(client, a["id"]) if r["status"] in ENDED), None),
                     timeout=240, interval=2, message="the agent's run never ended on lux")
    calls = _tool_events(env, run["id"])
    try:
        assert run["status"] == "succeeded", (run, _detail(client, a["id"], run["id"]))
        done = {(c["payload"]["tool"], bool(c["payload"].get("error"))) for c in calls if c["event_type"] == "agent.tool.dude"}
        assert ("github_issue", False) in done and ("create_task", False) in done, calls
    finally:
        print("calls:", [(c["event_type"], c["payload"].get("tool"), c["payload"].get("error")) for c in calls])

    # The task is filed in the automation's project, by this run.
    tasks = query(env.owner_dsn, "SELECT title, created_by_automation_run_id FROM tasks WHERE project_id = %s", (project["id"],))
    assert ("Fix the greeting", run["id"]) in [(t["title"], t["created_by_automation_run_id"]) for t in tasks], tasks
    # The comment reached the stand-in GitHub, as dude, on the triggering issue.
    comments = gh.issues[issue].comments + [c for pr in gh.pulls.values() for c in pr.comments]
    assert [c["body"] for c in comments if c["user"]["login"] == gh.token_login] == ["Filed a task for this."], comments
    # The out-of-scope comment was refused before it reached GitHub (the
    # stand-in has no such repository, and posted only the one above): the
    # agent was answered 422, naming why, as its log shows.
    log = _detail(client, a["id"], run["id"])["logTail"] or ""
    assert "status 422" in log and "elsewhere/outside is outside this automation's covered repositories" in log, log
    assert log.count(AUTOMATION_REPLY) == 1, log


AUTOMATION_REPLY = "Automation done."


# ---------------------------------------------------------------------------
# 4. A resumable agent: resumed, Start fresh, the expiry sweep
# ---------------------------------------------------------------------------

def test_a_resumable_agent_resumes_starts_fresh_and_expires_on_real_lux(client: ApiClient, env, lux_forge):
    project, gh = lux_forge
    a = _agent(client, project["id"], "Keeps its session", "Look at {{event.action}}", sessionMode="resumable", expiryDays=3)

    def fire(n: int) -> dict:
        before = {r["id"] for r in _runs(client, a["id"])}
        assert gh.send_webhook("issues", {"action": "opened", "issue": {"number": 100 + n}, "sender": {"login": "ana"}}) == 202
        run = wait_until(lambda: next((r for r in _runs(client, a["id"]) if r["id"] not in before), None),
                         timeout=30, interval=1, message=f"firing {n} made no run")
        return _ended(client, a["id"], run["id"], timeout=240)

    first = fire(1)
    assert first["status"] == "succeeded" and first["session"] == "new", first
    kept = _lux_run_of(env, first["id"])
    wait_until(lambda: _lux_state(env, kept) == "stopped", timeout=120, interval=1, message="the kept session was not stopped")

    second = fire(2)
    assert second["status"] == "succeeded" and second["session"] == "resumed", second
    assert _lux_run_of(env, second["id"]) == kept
    listed = client.get(f"/v1/automations/{a['id']}/runs").json()
    assert listed["session"] and listed["session"]["resumes"] == 1, listed["session"]
    wait_until(lambda: _lux_state(env, kept) == "stopped", timeout=120, interval=1, message="the resumed session was not stopped again")

    # Start fresh: the kept container is ended; the next run is new.
    assert client.post(f"/v1/automations/{a['id']}/start-fresh").status_code in (200, 204)
    wait_until(lambda: _lux_state(env, kept) in ("cancelled", "terminated"), timeout=120, interval=1,
               message="Start fresh left the kept container")
    assert client.get(f"/v1/automations/{a['id']}/runs").json()["session"] is None
    third = fire(3)
    assert third["status"] == "succeeded" and third["session"] == "new" and _lux_run_of(env, third["id"]) != kept, third
    kept = _lux_run_of(env, third["id"])
    wait_until(lambda: _lux_state(env, kept) == "stopped", timeout=120, interval=1, message="the new session was not stopped")

    # Its clock moved past its 3 days: the sweep ends it, though nothing else changed.
    execute(env.owner_dsn, "UPDATE automation_sessions SET last_run_at = now() - interval '3 days 1 minute' WHERE automation_id = %s",
            (a["id"],))
    wait_until(lambda: not query(env.owner_dsn, "SELECT 1 FROM automation_sessions WHERE automation_id = %s", (a["id"],)),
               timeout=60, interval=1, message="the expiry sweep kept the session")
    wait_until(lambda: _lux_state(env, kept) in ("cancelled", "terminated"), timeout=120, interval=1,
               message="the expired session's container was not ended")


# ---------------------------------------------------------------------------
# 5. Concurrency with real containers
# ---------------------------------------------------------------------------

# A command that takes a while, so the next event finds it running.
SLOW = 'echo "start $DUDE_DELIVERY_ID"; sleep 25; echo "end $DUDE_DELIVERY_ID"'


def _slow_command(client: ApiClient, project_id: str, concurrency: str, **extra) -> dict:
    return _automation(client, "command", {
        "name": f"Slow {concurrency}",
        "trigger": {"kind": "github", "event": "push", "actions": [], "filters": {}},
        "command": SLOW, "concurrency": concurrency, **extra,
    }, project_id=project_id)


def _running(client: ApiClient, automation_id: str, n: int = 1, timeout: float = 180) -> list[dict]:
    return wait_until(lambda: (lambda rs: rs if len(rs) >= n else None)(
        [r for r in _runs(client, automation_id) if r["status"] == "running" and r.get("session")]),
        timeout=timeout, interval=1, message=f"never {n} running")


def test_queue_waits_for_the_running_container_then_runs(client: ApiClient, env, lux_forge):
    project, gh = lux_forge
    a = _slow_command(client, project["id"], "queue")
    d1 = _push(gh)
    first = _running(client, a["id"])[0]
    d2 = _push(gh)
    queued = wait_until(lambda: next((r for r in _runs(client, a["id"]) if r["status"] == "queued"), None),
                        timeout=30, interval=1, message="the second push did not queue")
    assert queued["reason"] == "waiting for the run before it", queued
    first = _ended(client, a["id"], first["id"], timeout=240)
    second = _ended(client, a["id"], queued["id"], timeout=240)
    assert first["status"] == second["status"] == "succeeded", (first, second)
    assert second["startedAt"] >= first["endedAt"], (first, second)
    assert f"end {d1}" in _detail(client, a["id"], first["id"])["logTail"]
    assert f"end {d2}" in _detail(client, a["id"], second["id"])["logTail"]


def test_skip_records_a_skipped_run_with_its_reason(client: ApiClient, env, lux_forge):
    project, gh = lux_forge
    a = _slow_command(client, project["id"], "skip")
    _push(gh)
    first = _running(client, a["id"])[0]
    _push(gh)
    skipped = wait_until(lambda: next((r for r in _runs(client, a["id"]) if r["status"] == "skipped"), None),
                         timeout=30, interval=1, message="the second push was not skipped")
    assert skipped["reason"] == "a run was still going (Concurrency: skip)", skipped
    assert _ended(client, a["id"], first["id"], timeout=240)["status"] == "succeeded"


def test_cancel_really_ends_the_first_container(client: ApiClient, env, lux_forge):
    project, gh = lux_forge
    a = _slow_command(client, project["id"], "cancel")
    _push(gh)
    first = _running(client, a["id"])[0]
    lux_first = _lux_run_of(env, first["id"])
    wait_until(lambda: _lux_state(env, lux_first) == "running", timeout=120, interval=1, message="the first container never ran")
    d2 = _push(gh)
    cancelled = _ended(client, a["id"], first["id"], timeout=30)
    assert cancelled["status"] == "cancelled", cancelled
    wait_until(lambda: _lux_state(env, lux_first) in ("cancelled", "terminated"), timeout=60, interval=1,
               message="the cancelled run's container kept running")
    second = next(r for r in _runs(client, a["id"]) if r["id"] != first["id"])
    second = _ended(client, a["id"], second["id"], timeout=240)
    assert second["status"] == "succeeded" and f"end {d2}" in _detail(client, a["id"], second["id"])["logTail"], second


def test_parallel_runs_up_to_n_containers_at_once(client: ApiClient, env, lux_forge):
    project, gh = lux_forge
    a = _slow_command(client, project["id"], "parallel", parallelMax=2)
    for _ in range(3):
        _push(gh)
    running = _running(client, a["id"], n=2)
    assert len({_lux_run_of(env, r["id"]) for r in running}) == 2
    for r in running:
        wait_until(lambda r=r: _lux_state(env, _lux_run_of(env, r["id"])) == "running", timeout=120, interval=1,
                   message="a parallel container never ran")
    runs = _runs(client, a["id"])
    assert [r["status"] for r in runs].count("queued") == 1, runs
    assert [r["status"] for r in runs].count("running") == 2, runs
    for r in runs:
        assert _ended(client, a["id"], r["id"], timeout=300)["status"] == "succeeded"


def test_send_gives_the_running_agent_the_second_event(client: ApiClient, env, lux_forge):
    project, gh = lux_forge
    # A turn of 20 s, so the second event finds the agent working.
    a = _agent(client, project["id"], "Hears more", "Look at {{event.action}} {{sender}}\nsleep: 20",
               sessionMode="resumable", expiryDays=3, concurrency="send")
    assert gh.send_webhook("issues", {"action": "opened", "issue": {"number": 1}, "sender": {"login": "ana"}}) == 202
    first = _running(client, a["id"])[0]
    lux_run = _lux_run_of(env, first["id"])
    wait_until(lambda: _lux_state(env, lux_run) == "running", timeout=120, interval=1, message="the agent never ran")
    assert gh.send_webhook("issues", {"action": "edited", "issue": {"number": 1}, "sender": {"login": "bo"}}) == 202
    ended = _ended(client, a["id"], first["id"], timeout=240)
    runs = _runs(client, a["id"])
    assert len(runs) == 1 and ended["status"] == "succeeded" and ended["payloadCount"] == 2, runs
    # The running agent heard the second event: its prompt, echoed by lux-fake.
    log = _detail(client, a["id"], first["id"])["logTail"] or ""
    assert "edited bo" in log, log


# ---------------------------------------------------------------------------
# 8. A test event while an event run is live
# ---------------------------------------------------------------------------

def test_a_test_event_runs_on_its_own_and_leaves_the_kept_session(client: ApiClient, env, lux_forge):
    project, gh = lux_forge
    a = _agent(client, project["id"], "Live and tested", "Look at {{event.action}}\nsleep: 20",
               sessionMode="resumable", expiryDays=3)
    assert gh.send_webhook("issues", {"action": "opened", "issue": {"number": 1}, "sender": {"login": "ana"}}) == 202
    live = _running(client, a["id"])[0]
    kept = _lux_run_of(env, live["id"])
    resp = client.post(f"/v1/automations/{a['id']}/runs", {"testEvent": {
        "event": "issues", "deliveryId": "test-real-1",
        "body": {"action": "opened", "issue": {"number": 2}, "repository": {"full_name": f"{gh.owner}/{gh.repo}"},
                 "sender": {"login": "ana"}}}})
    assert resp.status_code == 202, resp.text
    test_run = resp.json()["runId"]
    # It starts at once, alongside the live run, in a container of its own.
    started = wait_until(lambda: next((r for r in _runs(client, a["id"]) if r["id"] == test_run and r.get("session")), None),
                         timeout=60, interval=1, message="the test run waited")
    assert started["source"] == "test" and started["session"] == "ephemeral", started
    assert _lux_run_of(env, test_run) != kept
    assert next(r for r in _runs(client, a["id"]) if r["id"] == live["id"])["status"] == "running"
    assert _ended(client, a["id"], test_run, timeout=240)["status"] == "succeeded"
    assert _ended(client, a["id"], live["id"], timeout=240)["status"] == "succeeded"
    # The kept session is the event run's, untouched by the test.
    rows = query(env.owner_dsn, "SELECT lux_run_id FROM automation_sessions WHERE automation_id = %s", (a["id"],))
    assert [r["lux_run_id"] for r in rows] == [kept], rows
    wait_until(lambda: _lux_state(env, kept) == "stopped", timeout=120, interval=1, message="the kept session was not kept")
    wait_until(lambda: _lux_state(env, _lux_run_of(env, test_run)) in ("cancelled", "terminated"), timeout=120, interval=1,
               message="the test run's container was not ended")


# ---------------------------------------------------------------------------
# 6, 7. Webhook and schedule: no container, nothing lux-specific
# ---------------------------------------------------------------------------

def test_webhook_and_schedule_need_no_container():
    pytest.skip("a Webhook-only automation runs no container and a schedule's firing is the orchestrator's own clock: "
                "both are proven against the fake lux in test_automations.py, whose orchestrator is the same binary")
