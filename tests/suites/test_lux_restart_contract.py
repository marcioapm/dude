"""A talker's agent across a restart, end to end against a real lux.

A talker is a brainstorm (a session's agent) or a task's conductor. When
its container stops without dude asking — the agent's process dies, the
container is killed, its host is lost — dude parks it and the next message
resumes the same lux Run in the same harness session. lux ending the Run
for good (terminate) ends the talker, and the next message starts a new
one. A failed turn parks it, at most twice in a row. A compaction is
recorded once, with lux's summary. A resume that comes back in a new
harness session is recorded, and the agent is briefed again first.

Every stop is caused by lux's own means: the agent's container killed on
its host (podman kill), the host's runner killed and the host frozen (lux
reports `lost`), `POST /v1/runs/{id}/terminate`, or the agent's own exit.
Nothing is written to lux's database.

With lux-fake as the agent, opt-in with `run_tests.py --lux`. The
real-model tests also need DUDE_LLM_KEY and DUDE_LLM_URL, and dude's
runtime image preloaded on lux's hosts as DUDE_TEST_RUNTIME_IMAGE
(default localhost/dude-runtime:bkeep; lux's `run_tests.py --serve --image`).
Screenshots of the notices go to DUDE_RESTART_SHOTS
(default /var/tmp/bkeep-e2e/shots).
"""

from __future__ import annotations

import json
import os
import re
import subprocess
import time
from pathlib import Path

import pytest
import requests
from playwright.sync_api import Page, expect

from env import TEST_LAYER
from fake_github import FakeGitHub
from helpers import ApiClient, execute, query, sign_in, wait_until

pytestmark = pytest.mark.lux

SHOTS = Path(os.environ.get("DUDE_RESTART_SHOTS", "/var/tmp/bkeep-e2e/shots"))
RUNTIME_IMAGE = os.environ.get("DUDE_TEST_RUNTIME_IMAGE", "localhost/dude-runtime:bkeep")
needs_model = pytest.mark.skipif(not os.environ.get("DUDE_LLM_KEY"), reason="needs DUDE_LLM_KEY and DUDE_LLM_URL: a real model")

KINDS = ("brainstorm", "conductor")

# ---------------------------------------------------------------------------
# lux, through its API and its hosts
# ---------------------------------------------------------------------------


def _lux(env, method: str, path: str, body: dict | None = None) -> requests.Response:
    return requests.request(method, env.real_lux["luxd_url"] + path, json=body, timeout=30,
                            headers={"authorization": f"Bearer {env.real_lux['api_key']}"})


def _lux_run(env, lux_id: str) -> dict:
    res = _lux(env, "GET", f"/v1/runs/{lux_id}")
    assert res.status_code == 200, res.text
    return res.json()


def _records(env, lux_id: str) -> list[dict]:
    """Every record of a lux Run's output, as {type, data, epoch}."""
    res = _lux(env, "GET", f"/v1/runs/{lux_id}/output")
    assert res.status_code == 200, res.text
    out, event = [], ""
    for line in res.content.decode("utf-8").splitlines():
        if line.startswith("event:"):
            event = line[6:].strip()
        elif line.startswith("data:") and event == "record":
            rec = json.loads(line[5:])
            ev = rec.get("event") or {}
            if ev.get("type"):
                out.append({"type": ev["type"], "data": ev.get("data") or {}, "epoch": rec.get("epoch")})
    return out


def _host(env, name: str) -> dict:
    return next(h for h in env.real_lux["hosts"] if h["name"] == name)


def _docker(*args: str, check: bool = True) -> str:
    res = subprocess.run(["docker", *args], capture_output=True, text=True, timeout=120)
    if check and res.returncode != 0:
        raise AssertionError(f"docker {' '.join(args)}: {res.returncode}: {res.stderr}")
    return res.stdout


def _agent_container(env, lux_id: str) -> tuple[dict, str]:
    """The host a running lux Run is on, and its agent's container there."""
    def found():
        run = _lux_run(env, lux_id)
        if run["state"] != "running" or not run.get("host"):
            return None
        host = _host(env, run["host"])
        ids = _docker("exec", host["container"], "podman", "ps", "-q", "--filter", f"label=lux.run={lux_id}").split()
        return (host, ids[0]) if ids else None
    return wait_until(found, timeout=120, interval=1, message=f"no running container for {lux_id}")


def kill_container(env, lux_id: str) -> int:
    """The agent's container killed on its host, as a crash would: lux sees
    the workload exit and ends the placement failed. The epoch it died in."""
    epoch = _lux_run(env, lux_id)["epoch"]
    host, ctr = _agent_container(env, lux_id)
    _docker("exec", host["container"], "podman", "kill", ctr)
    wait_until(lambda: _lux_run(env, lux_id)["state"] in ("failed", "succeeded", "stopped"),
               timeout=120, interval=1, message="lux never saw the container die")
    return epoch


class LostHost:
    """The Run's host lost: its runner killed and the machine frozen, so it
    stops heartbeating while the Run is live (lux: lost), as lux's own
    test_snapshots does it. back() unfreezes it and starts its runner again
    with the command line and environment it had, read from the process
    before it was killed (never printed: it holds the host's token)."""

    def __init__(self, env, lux_id: str):
        self.epoch = _lux_run(env, lux_id)["epoch"]
        self.host, _ = _agent_container(env, lux_id)
        c = self.host["container"]
        pid = _docker("exec", c, "cat", "/run/lux-runner.pid").strip()
        self.argv = [a for a in _docker("exec", c, "cat", f"/proc/{pid}/cmdline").split("\0") if a]
        environ = [a for a in _docker("exec", c, "cat", f"/proc/{pid}/environ").split("\0") if a]
        self.environ = [kv for kv in environ if kv.split("=", 1)[0] in ("LUX_URL", "LUX_HOST_TOKEN")]
        _docker("exec", c, "kill", "-KILL", pid, check=False)
        _docker("pause", c)
        self.paused = True
        wait_until(lambda: _lux_run(env, lux_id)["state"] == "lost", timeout=120, interval=2,
                   message="lux never reported the Run lost")

    def back(self) -> None:
        if not self.paused:
            return
        c = self.host["container"]
        _docker("unpause", c, check=False)
        self.paused = False
        args = ["exec", "-d"]
        for kv in self.environ:
            args += ["-e", kv]
        _docker(*args, c, "sh", "-c", 'echo $$ > /run/lux-runner.pid; exec "$@" >> /var/log/lux-runner-restarted.log 2>&1',
                "lux-runner", *self.argv)


# ---------------------------------------------------------------------------
# dude: the talker of each kind
# ---------------------------------------------------------------------------


class Talker:
    """A brainstorm or a task's conductor, written to through its Chat."""

    def __init__(self, client: ApiClient, env, kind: str, project: dict | None):
        self.client, self.env, self.kind, self.project = client, env, kind, project
        self.session = self.task = None
        if kind == "brainstorm":
            res = client.post("/v1/brainstorms", {"title": f"Restart {os.urandom(2).hex()}"})
            assert res.status_code == 201, res.text
            self.session = res.json()["id"]
        else:
            self.task = client.create_task(project["id"], f"Restart {os.urandom(2).hex()}")

    def say(self, text: str) -> dict:
        path = f"/v1/brainstorms/{self.session}/chat" if self.session else f"/v1/tasks/{self.task['id']}/chat"
        res = self.client.post(path, {"text": text})
        assert res.status_code in (200, 201), res.text
        return {**res.json(), "status": res.status_code}

    def events(self) -> list[dict]:
        scope = {"sessionId": self.session} if self.session else {"taskId": self.task["id"]}
        return self.client.events(**scope, limit=1000)

    def run_events(self, run_id: str) -> list[dict]:
        return self.client.events(runId=run_id, limit=1000)

    def said(self, run_id: str) -> list[str]:
        return [e["payload"].get("text", "") for e in self.run_events(run_id) if e["eventType"] == "agent.message"]

    def runs_created(self) -> list[str]:
        return [e["runId"] for e in self.events() if e["eventType"] == "run.created"
                and e["payload"].get("role") in ("brainstorm", "conductor")]

    def answered(self, run_id: str, marker: str, timeout: float = 180) -> str:
        return wait_until(lambda: next((t for t in self.said(run_id) if marker in t), None), timeout=timeout, interval=1,
                          message=f"{self.kind} {run_id} never answered {marker!r}; said {self.said(run_id)[-5:]}")

    def lux_id(self, run_id: str) -> str:
        return wait_until(lambda: query(self.env.owner_dsn, "SELECT lux_run_id FROM runs WHERE id = %s", (run_id,))[0]["lux_run_id"],
                          timeout=120, interval=1, message="the talker never reached lux")

    def row(self, run_id: str) -> dict:
        return query(self.env.owner_dsn, """SELECT status::text, dude_pause, lux_state, lux_stop_reason, agent_session_epoch,
            harness_state FROM runs WHERE id = %s""", (run_id,))[0]

    def lux_running(self, run_id: str) -> None:
        wait_until(lambda: query(self.env.owner_dsn, "SELECT 1 FROM runs WHERE id = %s AND lux_state = 'running' AND agent_session_epoch > 0",
                                 (run_id,)), timeout=180, interval=1, message="the talker's agent never ran on lux")

    def session_ids(self, run_id: str) -> list[str]:
        return [e["payload"].get("externalSessionId") for e in self.run_events(run_id) if e["eventType"] == "agent.session.started"]

    def page_url(self, web_url: str) -> str:
        return f"{web_url}#/sessions/{self.session}" if self.session else f"{web_url}#/task/{self.task['id']}"


def _first_line(kind: str, text: str) -> str:
    """A later message as lux-fake's script: the text as the agent reads it,
    one command a line. A member's message reaches a brainstorm as "Name:
    text" (the API trims it first), so its script starts on a second line,
    after a first lux-fake only echoes."""
    return text if kind == "conductor" else "script:\n" + text


def _setup_org(client: ApiClient, harness: str = "opencode") -> None:
    """The brainstorm role on the scripted agent, and talkers kept warm for
    half an hour, so only the stops a test causes park them."""
    brainstorm = {"tier": client.tier_for("fake/scripted")}
    if harness != "opencode":
        brainstorm["harness"] = harness
    res = client.patch("/v1/settings/organization", {"roles": {"brainstorm": brainstorm}, "delivery": {"conductorWarmMinutes": 30}})
    assert res.status_code == 200, res.text


@pytest.fixture(autouse=True)
def _terminated_after(env, org: dict):
    """Every lux Run the test's organization made is terminated after it: a
    talker stays running when its test ends, and lux's hosts have a Run
    limit the next tests would wait on."""
    yield
    for row in query(env.owner_dsn, "SELECT lux_run_id FROM runs WHERE organization_id = %s AND lux_run_id IS NOT NULL", (org["id"],)):
        _lux(env, "POST", f"/v1/runs/{row['lux_run_id']}/terminate")


@pytest.fixture
def lux_project(client: ApiClient, env) -> tuple[dict, FakeGitHub]:
    """A project lux's hosts can reach (they see this machine at their
    network's gateway), its conductor scripted."""
    gh = FakeGitHub(env.git_root, owner=f"o{os.urandom(4).hex()}", listen=env.real_lux["gateway"])
    gh.start()
    res = client.post("/v1/forge/credential", {"auth": "pat", "secret": "fake-token", "apiBaseUrl": gh.api_url})
    assert res.status_code == 200, res.text
    project = client.create_project(
        name="Restarts", slug=f"rst-{os.urandom(3).hex()}",
        agentModels=client.on_models({r: "fake/scripted" for r in ("implementer", "reviewer", "simplifier", "conductor")}),
        repositories=[{"name": "target", "url": gh.clone_url, "defaultBranch": "main"}],
    )
    yield project, gh
    gh.stop()


def _with_harness(client: ApiClient, project: dict, harness: str) -> None:
    if harness == "opencode":
        return
    models = {**client.get(f"/v1/projects/{project['id']}").json()["agentModels"],
              **client.on_models({"conductor": {"model": "fake/scripted", "harness": harness}})}
    res = client.patch(f"/v1/projects/{project['id']}", {"agentModels": models})
    assert res.status_code == 200, res.text


def _talker(client: ApiClient, env, kind: str, lux_project, harness: str = "opencode") -> Talker:
    _setup_org(client, harness)
    project, _ = lux_project
    _with_harness(client, project, harness)
    return Talker(client, env, kind, project)


def _started(t: Talker) -> str:
    """The talker's first message and answer: its Run, live on lux."""
    first = t.say("hello there, first message")
    assert first["status"] == 201 and first.get("created"), first
    run = first["runId"]
    t.answered(run, "first message")
    t.lux_running(run)
    return run


def _order(events: list[dict], types: tuple[str, ...]) -> list[str]:
    return [e["eventType"] for e in events if e["eventType"] in types]


def _log(name: str, data) -> None:
    print(f"[{name}]", json.dumps(data, default=str))


# ---------------------------------------------------------------------------
# 1. The container dies on its own: parked, resumed in the same lux Run
# ---------------------------------------------------------------------------


def _stop_on_its_own(env, lux_id: str, stop: str) -> tuple[int, LostHost | None]:
    """The talker's container stops without dude asking, as `stop` says:
    killed on its host (lux: failed), stopped through lux's API by someone
    other than dude (stopped, no dude stop reason), or its host lost (lost).
    The epoch it stopped in, and the lost host to bring back."""
    if stop == "killed":
        return kill_container(env, lux_id), None
    if stop == "stopped":
        epoch = _lux_run(env, lux_id)["epoch"]
        res = _lux(env, "POST", f"/v1/runs/{lux_id}/stop")
        assert res.status_code in (200, 202), res.text
        wait_until(lambda: _lux_run(env, lux_id)["state"] == "stopped", timeout=120, interval=1, message="lux never stopped it")
        return epoch, None
    lost = LostHost(env, lux_id)
    return lost.epoch, lost


def _resumes_in_place(t: Talker, env, run: str, lux_id: str, kind: str, stop: str, text: str) -> dict:
    """The stopped talker parked with lux's state, and the next message
    resumes the same lux Run in the same harness session. What it saw."""
    sessions_before = t.session_ids(run)
    lux_session_before = _lux_run(env, lux_id).get("sessionId")
    parks_before = len([e for e in t.run_events(run) if e["eventType"] == "run.parked"])
    died_in, lost = _stop_on_its_own(env, lux_id, stop)
    try:
        lux_state = _lux_run(env, lux_id)["state"]

        def parked():
            ps = [e for e in t.run_events(run) if e["eventType"] == "run.parked"]
            return ps[parks_before:] or None
        park = wait_until(parked, timeout=120, interval=1,
                          message=f"the talker whose container stopped ({lux_state}) was never parked: {t.row(run)}")[0]
        assert park["payload"].get("stopped") == lux_state, park
        assert t.row(run)["status"] == "paused", t.row(run)
        reply = t.say(_first_line(kind, f"echo {text}"))
        assert reply["status"] == 200 and reply["runId"] == run, reply
        t.answered(run, text, timeout=300)
    finally:
        if lost:
            lost.back()
    after = _lux_run(env, lux_id)
    seen = {"stop": stop, "luxStateAtStop": lux_state, "stoppedInEpoch": died_in, "epochAfter": after["epoch"],
            "luxSessionBefore": lux_session_before, "luxSessionAfter": after.get("sessionId"),
            "dudeSessions": t.session_ids(run), "parked": park["payload"],
            "placements": [(p["epoch"], p.get("hostName"), p.get("state")) for p in after.get("placements", [])]}
    assert query(env.owner_dsn, "SELECT lux_run_id FROM runs WHERE id = %s", (run,))[0]["lux_run_id"] == lux_id
    assert after["epoch"] > died_in, seen
    assert after.get("sessionId") == lux_session_before, seen
    assert t.session_ids(run) == sessions_before and len(sessions_before) == 1, seen
    return seen


@pytest.mark.timeout(900)
@pytest.mark.parametrize("kind", KINDS)
@pytest.mark.parametrize("stop", ["killed", "stopped", "lost"])
def test_restart_a_talker_whose_container_dies_is_parked_and_resumed_in_the_same_lux_run(
        client: ApiClient, env, lux_project, kind: str, stop: str):
    t = _talker(client, env, kind, lux_project)
    run = _started(t)
    lux_id = t.lux_id(run)
    stops = []
    if stop == "lost":
        # lux resumes a lost Run from the last snapshot before the lost
        # placement: one stop and resume first takes it, with the session.
        stops.append(_resumes_in_place(t, env, run, lux_id, kind, "stopped", "before the host was lost"))
    stops.append(_resumes_in_place(t, env, run, lux_id, kind, stop, "after the restart"))
    events = t.run_events(run)
    _log(f"1/{kind}/{stop}", {"run": run, "luxRunId": lux_id, "stops": stops,
                              "order": _order(events, ("run.parked", "run.unparked", "agent.session.started", "agent.session.replaced",
                                                       "agent.warning", "run.failed", "run.completed"))})
    types = [e["eventType"] for e in events]
    assert "agent.session.replaced" not in types and "run.completed" not in types and "run.failed" not in types, types
    assert t.runs_created() == [run], t.runs_created()


# ---------------------------------------------------------------------------
# 2. A message accepted but unread at the crash is answered after the resume
# ---------------------------------------------------------------------------


@pytest.mark.timeout(600)
@pytest.mark.parametrize("kind", KINDS)
def test_restart_a_message_accepted_but_unread_at_the_crash_is_answered_once_after_the_resume(
        client: ApiClient, env, lux_project, kind: str):
    # Claude Code's protocol: a message sent mid-turn is accepted at once
    # (lux.input accepted, with a receipt) and read at the turn's next step.
    t = _talker(client, env, kind, lux_project, harness="claude-code")
    run = _started(t)
    lux_id = t.lux_id(run)
    t.say(_first_line(kind, "sleep 600"))
    wait_until(lambda: query(env.owner_dsn, "SELECT 1 FROM runs WHERE id = %s AND agent_busy_at IS NOT NULL AND turn_done_at IS NULL", (run,)),
               timeout=60, interval=0.5, message="the long turn never started")
    unread = t.say(_first_line(kind, "echo the unread one"))
    directive = unread["directiveId"]
    accepted = wait_until(lambda: query(env.owner_dsn, """SELECT accepted_at, sent_at FROM directives
        WHERE id = %s AND accepted_at IS NOT NULL AND delivered_at IS NULL""", (directive,)),
                          timeout=60, interval=0.5, message="lux never accepted the message")
    died_in = kill_container(env, lux_id)
    t.answered(run, "the unread one", timeout=240)

    records = _records(env, lux_id)
    inputs = [(r["type"], r["data"].get("phase"), r["epoch"]) for r in records
              if r["type"].startswith("lux.input") and r["data"].get("requestId") == directive]
    replies = [m for m in t.said(run) if "the unread one" in m]
    _log(f"2/{kind}", {"run": run, "luxRunId": lux_id, "diedInEpoch": died_in, "epochAfter": _lux_run(env, lux_id)["epoch"],
                       "directive": directive, "acceptedBeforeCrash": accepted[0], "luxInputs": inputs, "replies": replies})
    assert len(replies) == 1, replies
    consumed = [i for i in inputs if i[0] == "lux.input.consumed"]
    assert len(consumed) == 1 and consumed[0][2] > died_in, inputs
    assert query(env.owner_dsn, "SELECT delivered_at IS NOT NULL AS d FROM directives WHERE id = %s", (directive,))[0]["d"]
    assert t.runs_created() == [run]


# ---------------------------------------------------------------------------
# 3. Terminated: ended for good, the next message starts a new Run
# ---------------------------------------------------------------------------


@pytest.mark.timeout(600)
@pytest.mark.parametrize("kind", KINDS)
def test_restart_a_talker_lux_terminates_ends_and_the_next_message_starts_a_new_run(
        client: ApiClient, env, lux_project, kind: str):
    t = _talker(client, env, kind, lux_project)
    run = _started(t)
    lux_id = t.lux_id(run)
    res = _lux(env, "POST", f"/v1/runs/{lux_id}/terminate")
    assert res.status_code in (200, 202), res.text
    wait_until(lambda: _lux_run(env, lux_id)["state"] == "terminated", timeout=120, interval=1, message="lux never terminated it")
    wait_until(lambda: t.row(run)["status"] == "completed", timeout=120, interval=1,
               message=f"the terminated talker was never ended: {t.row(run)}")
    nxt = t.say("again, after it ended")
    assert nxt["status"] == 201 and nxt["runId"] != run, nxt
    t.answered(nxt["runId"], "again, after it ended")
    types = [e["eventType"] for e in t.run_events(run)]
    _log(f"3/{kind}", {"run": run, "luxRunId": lux_id, "next": nxt["runId"], "nextLux": t.lux_id(nxt["runId"]),
                       "order": _order(t.run_events(run), ("run.parked", "run.completed", "run.failed"))})
    assert "run.parked" not in types, types
    assert t.runs_created() == [run, nxt["runId"]]


# ---------------------------------------------------------------------------
# 4. A failed turn parks; twice; the third ends it
# ---------------------------------------------------------------------------


@pytest.mark.timeout(900)
@pytest.mark.parametrize("kind", KINDS)
def test_restart_a_talkers_failed_turn_parks_it_twice_and_the_third_ends_it(
        client: ApiClient, env, lux_project, kind: str):
    """lux-fake's `exit` ends its process mid-turn: the turn's prompt is
    answered with an error ("process exited"), which dude reads as a failed
    turn."""
    t = _talker(client, env, kind, lux_project)
    run = _started(t)
    lux_id = t.lux_id(run)
    seen = []
    for n in (1, 2):
        reply = t.say(_first_line(kind, "exit 3"))
        assert reply["runId"] == run, reply
        parked = wait_until(lambda: [e for e in t.run_events(run) if e["eventType"] == "run.parked"
                                     and e["payload"].get("failedTurns") == n], timeout=180, interval=1,
                            message=f"failure {n} never parked it: {t.row(run)}; "
                                    f"{_order(t.run_events(run), ('run.failed', 'run.parked', 'run.unparked'))}")
        wait_until(lambda: t.row(run)["status"] == "paused", timeout=120, interval=1, message=f"not paused after failure {n}")
        seen.append({"failure": n, "parked": parked[0]["payload"], "epoch": _lux_run(env, lux_id)["epoch"], "row": t.row(run)})
    third = t.say(_first_line(kind, "exit 3"))
    assert third["runId"] == run, third
    wait_until(lambda: t.row(run)["status"] == "failed", timeout=240, interval=1,
               message=f"the third failure did not end it: {t.row(run)}")
    nxt = t.say("after three failures")
    assert nxt["status"] == 201 and nxt["runId"] != run, nxt
    t.answered(nxt["runId"], "after three failures")
    events = t.run_events(run)
    failed = [e["payload"] for e in events if e["eventType"] == "run.failed"]
    _log(f"4/{kind}", {"run": run, "luxRunId": lux_id, "parks": seen, "failed": failed, "next": nxt["runId"],
                       "order": _order(events, ("run.failed", "run.parked", "run.unparked", "run.completed"))})
    assert [f.get("failedTurns") for f in failed if f.get("kept")][:2] == [1, 2], failed
    assert all("error" in f for f in failed), failed
    assert t.runs_created() == [run, nxt["runId"]]


# ---------------------------------------------------------------------------
# 5. lux.compacted end to end
# ---------------------------------------------------------------------------


def _compacted(t: Talker, run: str) -> list[dict]:
    return [e for e in t.run_events(run) if e["eventType"] == "agent.context.compacted"]


@pytest.mark.timeout(600)
@pytest.mark.parametrize("kind", KINDS)
@pytest.mark.parametrize("harness", ["claude-code", "codex"])
def test_restart_a_compaction_is_recorded_once_with_luxs_summary(
        client: ApiClient, env, lux_project, kind: str, harness: str):
    t = _talker(client, env, kind, lux_project, harness=harness)
    run = _started(t)
    lux_id = t.lux_id(run)
    t.say(_first_line(kind, "echo remember periwinkle"))
    t.answered(run, "remember periwinkle")
    t.say(_first_line(kind, "compact\necho compacted now"))
    t.answered(run, "compacted now")
    # lux writes its record up to 10 s after Codex's item.
    got = wait_until(lambda: _compacted(t, run), timeout=60, interval=1, message="no agent.context.compacted")
    time.sleep(12)
    got = _compacted(t, run)
    luxs = [r["data"] for r in _records(env, lux_id) if r["type"] == "lux.compacted"]
    _log(f"5/{kind}/{harness}", {"run": run, "luxRunId": lux_id, "luxCompacted": luxs,
                                 "dude": [e["payload"] for e in got]})
    assert len(luxs) == 1 and len(got) == 1, (luxs, got)
    payload = got[0]["payload"]
    assert payload.get("summary") == luxs[0]["summary"] and "remember periwinkle" in payload["summary"], (payload, luxs)
    if harness == "claude-code":
        assert (payload.get("trigger"), payload.get("preTokens"), payload.get("postTokens")) == ("manual", 36663, 972), payload


@pytest.mark.ui
@pytest.mark.timeout(600)
def test_restart_the_compaction_notice_shows_in_the_sessions_chat(
        client: ApiClient, env, org: dict, lux_project, page: Page, web_url: str):
    t = _talker(client, env, "brainstorm", lux_project, harness="claude-code")
    run = _started(t)
    t.say(_first_line("brainstorm", "compact\necho compacted now"))
    t.answered(run, "compacted now")
    wait_until(lambda: _compacted(t, run), timeout=60, interval=1, message="no agent.context.compacted")
    SHOTS.mkdir(parents=True, exist_ok=True)
    sign_in(page, web_url, org["api_key"])
    page.goto(t.page_url(web_url))
    notice = page.get_by_role("note").filter(has_text="The agent compacted its context.")
    expect(notice).to_have_count(1, timeout=30_000)
    expect(page.get_by_text("Primary Request and Intent")).to_have_count(0)
    notice.scroll_into_view_if_needed()
    page.screenshot(path=str(SHOTS / "5-brainstorm-compacted.png"))


# ---------------------------------------------------------------------------
# 6. A resume in a new harness session
# ---------------------------------------------------------------------------


def _lose_transcript(env, lux_id: str) -> None:
    """lux-fake keeps its conversation under $HOME/.lux-fake, the agent's
    home (/home/agent, dude's home state volume); removed, session/load
    finds nothing and lux starts a new session. Removed inside the
    container (podman exec runs as root, whose HOME is not the agent's),
    then the container is killed."""
    host, ctr = _agent_container(env, lux_id)
    _docker("exec", host["container"], "podman", "exec", ctr, "rm", "-rf", "/home/agent/.lux-fake")


@pytest.mark.timeout(600)
@pytest.mark.parametrize("kind", KINDS)
def test_restart_a_resume_in_a_new_session_is_recorded_and_briefed_again_first(
        client: ApiClient, env, lux_project, kind: str):
    t = _talker(client, env, kind, lux_project)
    run = _started(t)
    lux_id = t.lux_id(run)
    before = t.session_ids(run)
    _lose_transcript(env, lux_id)
    kill_container(env, lux_id)
    wait_until(lambda: t.row(run)["status"] == "paused", timeout=120, interval=1, message="never parked")
    msg = t.say(_first_line(kind, "echo after the new session"))
    assert msg["runId"] == run, msg
    t.answered(run, "after the new session", timeout=240)
    replaced = wait_until(lambda: [e for e in t.run_events(run) if e["eventType"] == "agent.session.replaced"],
                          timeout=60, interval=1, message="no agent.session.replaced")
    warnings = [e["payload"] for e in t.run_events(run) if e["eventType"] == "agent.warning"]
    brief = replaced[0]["payload"].get("directiveId")
    assert brief, replaced
    wait_until(lambda: query(env.owner_dsn, "SELECT 1 FROM directives WHERE id = %s AND delivered_at IS NOT NULL", (brief,)),
               timeout=120, interval=1, message="the re-brief was never delivered")
    records = _records(env, lux_id)
    resumed_epoch = _lux_run(env, lux_id)["epoch"]
    inputs = [(r["data"].get("requestId"), r["data"].get("phase"), r["epoch"]) for r in records
              if r["type"] == "lux.input" and r["epoch"] == resumed_epoch]
    order = [i[0] for i in inputs if i[1] == "accepted"]
    brief_text = query(env.owner_dsn, "SELECT text FROM directives WHERE id = %s", (brief,))[0]["text"]
    _log(f"6/{kind}", {"run": run, "luxRunId": lux_id, "sessionsBefore": before, "replaced": replaced[0]["payload"],
                       "warnings": warnings, "acceptedInResumedEpoch": order, "message": msg["directiveId"],
                       "events": _order(t.run_events(run), ("run.parked", "run.unparked", "agent.warning", "agent.session.started",
                                                            "agent.session.replaced", "run.directive.delivered"))})
    assert replaced[0]["payload"]["from"] == before[0] and replaced[0]["payload"]["to"] != before[0], replaced
    assert any("session/load failed" in w.get("message", "") for w in warnings), warnings
    assert "session/load failed" in replaced[0]["payload"].get("reason", ""), replaced
    assert "earlier conversation is lost" in brief_text or "restarted without its earlier conversation" in brief_text, brief_text
    assert brief in order and msg["directiveId"] in order, order
    assert order.index(brief) < order.index(msg["directiveId"]), order


@pytest.mark.ui
@pytest.mark.timeout(600)
@pytest.mark.parametrize("kind", KINDS)
def test_restart_the_restarted_without_its_conversation_notice_shows_in_chat(
        client: ApiClient, env, org: dict, lux_project, page: Page, web_url: str, kind: str):
    t = _talker(client, env, kind, lux_project)
    run = _started(t)
    lux_id = t.lux_id(run)
    _lose_transcript(env, lux_id)
    kill_container(env, lux_id)
    wait_until(lambda: t.row(run)["status"] == "paused", timeout=120, interval=1, message="never parked")
    t.say(_first_line(kind, "echo after the new session"))
    t.answered(run, "after the new session", timeout=240)
    wait_until(lambda: [e for e in t.run_events(run) if e["eventType"] == "agent.session.replaced"],
               timeout=60, interval=1, message="no agent.session.replaced")
    SHOTS.mkdir(parents=True, exist_ok=True)
    sign_in(page, web_url, org["api_key"])
    page.goto(t.page_url(web_url))
    notice = page.get_by_role("note").filter(has_text="The agent restarted without its earlier conversation.")
    expect(notice).to_have_count(1, timeout=30_000)
    expect(page.get_by_role("note").filter(has_text="Its container stopped")).to_have_count(1)
    notice.scroll_into_view_if_needed()
    page.screenshot(path=str(SHOTS / f"6-{kind}-session-replaced.png"))


# ---------------------------------------------------------------------------
# Real models: memory across a restart and a compaction
# ---------------------------------------------------------------------------

# The cheapest model the proxy serves for each harness.
HARNESSES = {"claude-code": "claude-haiku-4.5", "codex": "gpt-5.6-luna", "opencode": "claude-haiku-4.5"}


def _runtime_image(client: ApiClient, owner_dsn: str) -> str:
    """dude's runtime image as a library image whose dude layer is done:
    what dude-image-builder would record, written as test_images.py writes
    it, its final the image lux's hosts were preloaded with. (The
    orchestrator's DUDE_AGENT_IMAGE is lux-fake's in the contract suite, and
    a library image is what a role can name instead.)"""
    made = client.post("/v1/images", {"name": f"bkeep-{os.urandom(3).hex()}", "containerfile": f"FROM {RUNTIME_IMAGE}\n", "note": "e2e"})
    assert made.status_code == 201, made.text
    image = made.json()["image"]["id"]
    queued = client.post(f"/v1/images/{image}/build")
    assert queued.status_code == 201, queued.text
    version = queued.json()["versionId"]
    execute(owner_dsn, "UPDATE image_versions SET user_ref = %s, built_at = now() WHERE id = %s", (RUNTIME_IMAGE, version))
    execute(owner_dsn, """UPDATE image_builds SET state = 'succeeded', finished_at = now(), started_at = now(), layer_ref = %s
        WHERE image_version_id = %s AND kind = 'build'""", (TEST_LAYER, version))
    execute(owner_dsn, """INSERT INTO image_finals (organization_id, image_version_id, layer_ref, final_ref)
        SELECT organization_id, id, %s, %s FROM image_versions WHERE id = %s""", (TEST_LAYER, RUNTIME_IMAGE, version))
    query(owner_dsn, "SELECT * FROM image_publish(%s)", (version,))
    return image


def _real_tier(client: ApiClient, harness: str, options: dict | None = None) -> str:
    """A tier of its own requesting the harness's model, with options."""
    name = f"R {harness[:6]} {os.urandom(2).hex()}"
    res = client.post("/v1/models/tiers", {"name": name, "model": HARNESSES[harness], **({"options": options} if options else {})})
    assert res.status_code == 201, res.text
    return next(x["id"] for x in res.json()["tiers"] if x["name"] == name)


def _real_brainstorm(client: ApiClient, env, harness: str) -> Talker:
    """The brainstorm role on harness and its model, in dude's runtime image."""
    tier, image = _real_tier(client, harness), _runtime_image(client, env.owner_dsn)
    res = client.patch("/v1/settings/organization", {"roles": {"brainstorm": {"tier": tier, "harness": harness, "image": image}},
                                                     "delivery": {"conductorWarmMinutes": 60}})
    assert res.status_code == 200, res.text
    return Talker(client, env, "brainstorm", None)


def _real_conductor(client: ApiClient, env, lux_project, harness: str, options: dict | None = None) -> Talker:
    """A task's conductor on harness and its model, in dude's runtime image.
    A conductor, not a brainstorm, where a message must reach the harness
    as written (a slash command): a member's message to a brainstorm is
    "Name: text"."""
    project, _ = lux_project
    tier, image = _real_tier(client, harness, options), _runtime_image(client, env.owner_dsn)
    res = client.patch(f"/v1/projects/{project['id']}/settings", {"roles": {"conductor": {"tier": tier, "harness": harness, "image": image}},
                                                                  "delivery": {"conductorWarmMinutes": 60}})
    assert res.status_code == 200, res.text
    return Talker(client, env, "conductor", project)


def _turn_over(t: Talker, run: str, timeout: float = 900) -> None:
    """The agent's turn is over (idle), with no message of the person's unread."""
    wait_until(lambda: query(t.env.owner_dsn, """SELECT 1 FROM runs r WHERE r.id = %s AND r.turn_done_at IS NOT NULL
        AND NOT EXISTS (SELECT 1 FROM directives d WHERE d.run_id = r.id AND d.delivered_at IS NULL AND d.failed_at IS NULL)""", (run,)),
               timeout=timeout, interval=3, message=f"the turn never ended: {t.row(run)}; said {t.said(run)[-2:]}")


def _answers_word(t: Talker, run: str, after: int, word: str, timeout: float = 600) -> str:
    def said():
        texts = t.said(run)[after:]
        return next((x for x in texts if word.lower() in x.lower()), None)
    return wait_until(said, timeout=timeout, interval=3,
                      message=f"the agent never said {word!r}; said {t.said(run)[after:]}; row {t.row(run)}")


WORD = "TANGERINE"
ASK_WORD = "What is the code word I asked you to remember? Answer with the word only."


@needs_model
@pytest.mark.timeout(1500)
@pytest.mark.parametrize("harness", list(HARNESSES))
def test_restart_a_real_model_remembers_the_word_across_a_container_kill(client: ApiClient, env, harness: str):
    started = time.time()
    t = _real_brainstorm(client, env, harness)
    first = t.say(f"Remember the code word {WORD}. Reply with just: noted.")
    run = first["runId"]
    t.lux_running(run)
    _turn_over(t, run)
    lux_id = t.lux_id(run)
    before = _lux_run(env, lux_id)
    sessions = t.session_ids(run)
    died_in = kill_container(env, lux_id)
    wait_until(lambda: t.row(run)["status"] == "paused", timeout=120, interval=1, message=f"never parked: {t.row(run)}")
    n = len(t.said(run))
    reply = t.say(ASK_WORD)
    assert reply["status"] == 200 and reply["runId"] == run, reply
    said = _answers_word(t, run, n, WORD)
    after = _lux_run(env, lux_id)
    _log(f"7/{harness}", {"run": run, "luxRunId": lux_id, "epochBefore": before["epoch"], "diedIn": died_in, "epochAfter": after["epoch"],
                          "luxStateAtKill": "failed", "luxSession": [before.get("sessionId"), after.get("sessionId")],
                          "dudeSessions": t.session_ids(run), "firstAnswer": t.said(run)[:n], "answer": said,
                          "seconds": round(time.time() - started),
                          "order": _order(t.run_events(run), ("run.parked", "run.unparked", "agent.session.started",
                                                               "agent.session.replaced", "agent.warning"))})
    assert after["epoch"] > died_in and after.get("sessionId") == before.get("sessionId"), (before, after)
    assert t.session_ids(run) == sessions, (sessions, t.session_ids(run))
    assert "agent.session.replaced" not in [e["eventType"] for e in t.run_events(run)]
    assert t.runs_created() == [run]
