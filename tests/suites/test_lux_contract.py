"""The contract with lux: dude against a real lux, not the stand-in.

Everything else in the suite runs against `fake-lux`, which is what makes it
fast and deterministic. That is only sound if the stand-in behaves like lux
where dude depends on it: the spec it accepts, the order its stream reports
things in, what a push reports, what stop and resume do. These tests run the
same flows through a real lux — containers on Podman hosts, lux-fake as the
agent — so a drift between the two shows up here first.

Opt-in, and in an environment of its own: `run_tests.py --lux` points the
orchestrator at a real lux (by default the latest `run_tests.py --serve` in
lux's repository; DUDE_TEST_LUX_ENV names another) and runs only these.
"""

from __future__ import annotations

import json
import os

import pytest
import requests

from fake_github import FakeGitHub
from helpers import ApiClient, wait_until

pytestmark = pytest.mark.lux

# lux-fake, preloaded on every lux host. dude writes its scripts
# (fakeScript in orchestrator/internal/phases/spec.go).
FAKE_IMAGE = "localhost/lux-fake:test"


@pytest.fixture
def lux_project(client: ApiClient, env) -> tuple[dict, FakeGitHub]:
    """A project whose repository lux's hosts can reach: they are containers,
    and see this machine at their network's gateway."""
    gh = FakeGitHub(env.git_root, owner=f"o{os.urandom(4).hex()}", listen=env.real_lux["gateway"])
    gh.start()
    resp = client.post("/v1/forge/credential", {"auth": "pat", "secret": "fake-token", "apiBaseUrl": gh.api_url})
    assert resp.status_code == 200, resp.text
    project = client.create_project(
        name="On lux", slug=f"lux-{os.urandom(3).hex()}", runtimeImage=FAKE_IMAGE,
        agentModels={r: {"model": "fake/scripted"} for r in ("implementer", "reviewer", "simplifier")},
        repositories=[{"name": "target", "url": gh.clone_url, "defaultBranch": "main"}],
    )
    yield project, gh
    gh.stop()


def test_a_delivery_runs_on_real_lux(client: ApiClient, lux_project):
    """Implement → review → fix → review → simplify → PR, every agent a lux Run.

    lux-fake commits for real inside its container; lux pushes over git; dude
    fast-forwards the task's branch and opens the PR.
    """
    project, gh = lux_project
    task = client.create_task(project["id"], "Deliver on lux")
    assert client.post(f"/v1/tasks/{task['id']}/deliver").status_code == 201

    def pr_open():
        return client.get("/v1/pull-requests", params={"taskId": task["id"]}).json()["pullRequests"]

    try:
        pr = wait_until(pr_open, timeout=300, interval=2, message="no pull request on real lux")[0]
    finally:
        print("phases:", [(r["phase"], r["status"], r.get("error")) for r in client.task_runs(task["id"])])

    phases = [(r["phase"], r["status"]) for r in client.task_runs(task["id"])]
    assert phases == [("implement", "completed"), ("review", "completed"), ("fix", "completed"),
                      ("review", "completed"), ("simplify", "completed")], phases

    # The commits lux-fake made inside the containers are on the branch.
    log = gh.branch_log(pr["headBranch"])
    assert any("Add FACTORY.md" in m for m in log), log
    assert any("Address review findings" in m for m in log), log

    # The review's findings came through lux's stream intact.
    findings = client.get("/v1/findings", params={"taskId": task["id"]}).json()["findings"]
    assert [(f["severity"], f["status"]) for f in findings] == [("blocking", "resolved")], findings

    # What dude recorded from the stream is a conversation, not lux internals.
    implement = client.task_runs(task["id"])[0]
    events = client.events(runId=implement["id"])
    types = {e["eventType"] for e in events}
    assert {"agent.session.started", "agent.message", "run.completed", "git.commit_created"} <= types, types

    # lux acknowledges the task itself, with what the agent received: the
    # scripted agent's prompt is its script.
    prompts = [e["payload"] for e in events if e["eventType"] == "agent.prompt.delivered"]
    assert len(prompts) == 1 and "commit" in prompts[0]["text"], prompts
    # And relays the turn's token usage, which only the prompt's response carries.
    usage = client.get(f"/v1/runs/{implement['id']}").json()["tokens"]
    assert usage["output"] > 0, usage

    # What the agent wrote into $LUX_ARTIFACTS, collected by lux when its
    # container stopped, recorded by dude, and read back through lux.
    def notes():
        found = client.get("/v1/artifacts", params={"taskId": task["id"]}).json()["artifacts"]
        return [a for a in found if a["name"] == "NOTES.md"]

    art = wait_until(notes, timeout=60, interval=1, message="lux's artifacts never reached dude")[0]
    assert art["runId"] == implement["id"] and art["sizeBytes"] > 0, art
    content = client.get(f"/v1/artifacts/{art['id']}/content")
    assert content.status_code == 200 and content.text.startswith("# What changed"), (content.status_code, content.text)


def test_steering_pause_and_resume_on_real_lux(client: ApiClient, lux_project):
    """A live agent on lux hears a directive, stops, and continues its session."""
    project, _ = lux_project
    # An agent that never finishes its turn, to have something live to control.
    client.patch(f"/v1/projects/{project['id']}", {"agentModels": {"implementer": {"model": "fake/hang"}}})
    task = client.create_task(project["id"], "Hold")
    assert client.post(f"/v1/tasks/{task['id']}/deliver").status_code == 201

    run = wait_until(
        lambda: next((r for r in client.task_runs(task["id"]) if r["status"] == "running"), None),
        timeout=120, interval=1, message="the agent never started on lux",
    )
    # The agent is mid-turn and speaks ACP, which cannot take a message then:
    # interrupting is how it hears the directive now.
    assert client.post(f"/v1/runs/{run['id']}/steer", {"text": "echo steered", "interrupt": True}).status_code == 201
    wait_until(
        lambda: any(d["deliveredAt"] for d in client.get(f"/v1/runs/{run['id']}/directives").json()["directives"]),
        timeout=60, interval=1, message="lux never acknowledged the directive",
    )

    assert client.post(f"/v1/runs/{run['id']}/pause", {}).status_code == 200
    wait_until(lambda: client.get(f"/v1/runs/{run['id']}").json()["status"] == "paused",
               timeout=90, interval=1, message="the run never paused on lux")

    assert client.post(f"/v1/runs/{run['id']}/resume", {}).status_code == 200
    wait_until(lambda: client.get(f"/v1/runs/{run['id']}").json()["status"] == "running",
               timeout=120, interval=1, message="the run never resumed on lux")

    # One session across both placements: the resume continued it.
    events = client.events(runId=run["id"])
    assert sum(e["eventType"] == "agent.session.started" for e in events) == 1

    assert client.post(f"/v1/runs/{run['id']}/abort", {}).status_code == 200


@pytest.mark.skipif(not os.environ.get("DUDE_TEST_TOOLS_HOST"), reason="needs DUDE_TEST_TOOLS_HOST: an address of this machine lux's hosts can reach")
def test_an_agent_on_real_lux_calls_dudes_tools(client: ApiClient, lux_project):
    """lux hands the agent dude's MCP server, authenticated as its Run; the
    agent (lux-fake's MCP client) calls list_tasks, and dude records the
    call on that Run and answers with the project's work."""
    project, _ = lux_project
    client.patch(f"/v1/projects/{project['id']}", {"agentModels": {
        "implementer": {"model": "fake/tools"}, "reviewer": {"model": "fake/scripted"}, "simplifier": {"model": "fake/scripted"}}})
    task = client.create_task(project["id"], "Use the tools")
    assert client.post(f"/v1/tasks/{task['id']}/deliver").status_code == 201

    def called():
        runs = client.task_runs(task["id"])
        if not runs:
            return None
        return [e for e in client.events(runId=runs[0]["id"]) if e["eventType"] == "agent.tool.dude"] or None

    try:
        calls = wait_until(called, timeout=120, interval=1, message="the agent never called dude's tools on lux")
    finally:
        print("phases:", [(r["phase"], r["status"], r.get("error")) for r in client.task_runs(task["id"])])
    assert calls[0]["payload"]["tool"] == "list_tasks"
    assert "Use the tools" in json.dumps(calls[0]["payload"]["result"])
    # And through lux's service socket, as the dude CLI calls: lux added the
    # Run's token; the container never held it.
    progress = wait_until(lambda: [e for e in client.events(runId=calls[0]["runId"])
                                   if e["eventType"] == "agent.custom.progress"] or None,
                          timeout=60, interval=1, message="nothing came through lux's service socket")
    assert progress[0]["payload"]["data"]["step"] == "through the socket"
    # And the agent heard the answer: its reply, which comes after the
    # socket call, carries the task's key.
    implement = client.task_runs(task["id"])[0]

    def replied():
        text = " ".join(e["payload"].get("text", "") for e in client.events(runId=implement["id"])
                        if e["eventType"] == "agent.message")
        return text if task["key"] in text else None

    wait_until(replied, timeout=60, interval=1, message="the agent's reply never named the task")


@pytest.mark.skipif(not os.environ.get("DUDE_TEST_TOOLS_HOST"), reason="needs DUDE_TEST_TOOLS_HOST: an address of this machine lux's hosts can reach")
def test_an_agent_waiting_on_a_person_is_parked_on_real_lux_and_resumed_by_the_answer(client: ApiClient, lux_project):
    """The agent asks with ask_person through lux's service socket and ends
    its turn; past the grace period dude stops the lux Run (nothing held);
    the answer resumes the same lux Run, in the same agent session."""
    project, _ = lux_project
    client.patch(f"/v1/projects/{project['id']}", {"agentModels": {
        "implementer": {"model": "fake/ask"}, "reviewer": {"model": "fake/scripted"}, "simplifier": {"model": "fake/scripted"}}})
    task = client.create_task(project["id"], "Ask on lux")
    assert client.post(f"/v1/tasks/{task['id']}/deliver").status_code == 201

    def implementer():
        return next((r for r in client.task_runs(task["id"]) if r["phase"] == "implement"), None)

    try:
        wait_until(lambda: (implementer() or {}).get("status") == "paused", timeout=180, interval=1,
                   message="the waiting agent was never parked on lux")
        question = client.get("/v1/questions").json()["questions"]
        question = next(q for q in question if q["taskId"] == task["id"])
        assert client.post(f"/v1/questions/{question['id']}/answer", {"text": "yes"}).status_code == 200
        wait_until(lambda: implementer()["status"] == "completed", timeout=180, interval=1,
                   message="the answer did not resume the parked agent on lux")
    finally:
        print("phases:", [(r["phase"], r["status"], r.get("error")) for r in client.task_runs(task["id"])])
    types = [e["eventType"] for e in client.events(runId=implementer()["id"])]
    assert "run.parked" in types and "run.unparked" in types, types
    # One agent session, continued: resumed, not started over.
    assert types.count("agent.session.started") == 1, types
    messages = " ".join(e["payload"].get("text", "") for e in client.events(runId=implementer()["id"])
                        if e["eventType"] == "agent.message")
    assert "Answer to your question" in messages, messages


# ---------------------------------------------------------------------------
# Machine sizes: what dude sends lux as a Run's size and pool, and how it
# reads lux's pools. Straight to lux's API with dude's key, encoded as the
# orchestrator encodes them (lux.Resources: memory and disk as JSON numbers
# of bytes; lux.PlacementSpec: placement.pool).
# ---------------------------------------------------------------------------

GIB = 1 << 30


def _lux(env, method: str, path: str, body: dict | None = None, key: str | None = None) -> requests.Response:
    headers = {"authorization": f"Bearer {env.real_lux['api_key']}"}
    if key:
        headers["idempotency-key"] = key
    return requests.request(method, env.real_lux["luxd_url"] + path, json=body, headers=headers, timeout=30)


def _sleeper(name: str, **extra) -> dict:
    """A Run that only waits, as a branch preview's workload does."""
    return {"name": name, "labels": {"dude.kind": "contract"}, "image": {"ref": FAKE_IMAGE},
            "workload": {"adapter": "generic", "command": ["sleep", "infinity"], "workdir": "/tmp"}, **extra}


def test_a_run_sized_as_dude_sizes_it_reaches_running_on_real_lux(env):
    """lux's spec.Bytes takes a JSON number of bytes as well as "8Gi"
    (docs/runspec.md: "Sizes accept 512Mi, 8Gi, 1G, or bytes"); this is
    the number form dude sends. Its hosts must have 2 CPUs and 8 GiB free."""
    resources = {"cpus": 2, "memory": 8 * GIB, "disk": 20 * GIB}
    res = _lux(env, "POST", "/v1/runs", _sleeper("contract sized", resources=resources), key=f"contract-{os.urandom(6).hex()}")
    assert res.status_code in (200, 201, 202), res.text
    run_id = res.json()["id"]
    try:
        run = wait_until(lambda: (r := _lux(env, "GET", f"/v1/runs/{run_id}").json())["state"] == "running" and r,
                         timeout=180, interval=1, message="a Run with dude's resources never ran on lux")
        # lux kept the size it was given, in bytes.
        assert run["spec"]["resources"]["memory"] == 8 * GIB and run["spec"]["resources"]["disk"] == 20 * GIB, run["spec"]
    finally:
        _lux(env, "POST", f"/v1/runs/{run_id}/cancel")


def test_a_pool_lux_does_not_have_is_taken_and_never_placed(env):
    """What lux does with a size whose pool is gone (removed from lux after
    an admin chose it). lux's spec.Normalize does not check the pool, so the
    submit is taken (201) and the scheduler leaves the Run submitted, saying
    no host matches: there is no refusal code. dude records such a Run as
    scheduled and it waits; if lux starts refusing it with a 4xx other than
    429, dude fails the Run instead (lux.Error.Retryable), and this test
    says the contract moved."""
    pool = f"no-such-pool-{os.urandom(3).hex()}"
    res = _lux(env, "POST", "/v1/runs", _sleeper("contract no pool", placement={"pool": pool}), key=f"contract-{os.urandom(6).hex()}")
    assert res.status_code == 201, (res.status_code, res.text)
    run_id = res.json()["id"]
    try:
        run = wait_until(lambda: (r := _lux(env, "GET", f"/v1/runs/{run_id}").json())["stateReason"] and r,
                         timeout=60, interval=1, message="lux never said why the Run waits")
        assert (run["state"], run["stateReason"]) == ("submitted", "no host matches"), run
        assert run["spec"]["placement"]["pool"] == pool, run["spec"]
    finally:
        _lux(env, "POST", f"/v1/runs/{run_id}/cancel")


def test_luxs_pools_decode_as_dude_reads_them(env, client: ApiClient):
    res = _lux(env, "GET", "/v1/pools")
    assert res.status_code == 200, res.text
    pools = res.json()["pools"]
    assert all(isinstance(p["name"], str) and p["name"] for p in pools), pools
    for p in pools:
        # Absent from an older lux; when there, one host's size: CPUs, and
        # memory and disk in bytes (0: disk not reserved).
        host = p.get("hostSize")
        if host is not None:
            assert isinstance(host["cpus"], (int, float)) and host["cpus"] > 0, p
            assert isinstance(host["memory"], int) and host["memory"] > 0, p
            assert isinstance(host["disk"], int) and host["disk"] >= 0, p
        assert p.get("hostSizeFrom") in (None, "", "running", "history"), p
        assert isinstance(p.get("isDefault", False), bool) and isinstance(p.get("hostsRunning", 0), (int, type(None))), p
    # And through the orchestrator's lux.Client and the backend, as the
    # Machines page reads them: a decoding failure would be a `problem`.
    served = client.get("/v1/machines/pools")
    assert served.status_code == 200 and served.json()["problem"] is None, served.text
    assert sorted(p["name"] for p in served.json()["pools"]) == sorted(p["name"] for p in pools)
