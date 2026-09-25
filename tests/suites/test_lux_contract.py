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
    fast-forwards the work item's branch and opens the PR.
    """
    project, gh = lux_project
    work_item = client.create_work_item(project["id"], "Deliver on lux")
    assert client.post(f"/v1/work-items/{work_item['id']}/deliver").status_code == 201

    def pr_open():
        return client.get("/v1/pull-requests", params={"workItemId": work_item["id"]}).json()["pullRequests"]

    try:
        pr = wait_until(pr_open, timeout=300, interval=2, message="no pull request on real lux")[0]
    finally:
        print("phases:", [(r["phase"], r["status"], r.get("error")) for r in client.work_item_runs(work_item["id"])])

    phases = [(r["phase"], r["status"]) for r in client.work_item_runs(work_item["id"])]
    assert phases == [("implement", "completed"), ("review", "completed"), ("fix", "completed"),
                      ("review", "completed"), ("simplify", "completed")], phases

    # The commits lux-fake made inside the containers are on the branch.
    log = gh.branch_log(pr["headBranch"])
    assert any("Add FACTORY.md" in m for m in log), log
    assert any("Address review findings" in m for m in log), log

    # The review's findings came through lux's stream intact.
    findings = client.get("/v1/findings", params={"workItemId": work_item["id"]}).json()["findings"]
    assert [(f["severity"], f["status"]) for f in findings] == [("blocking", "resolved")], findings

    # What dude recorded from the stream is a conversation, not lux internals.
    implement = client.work_item_runs(work_item["id"])[0]
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
        found = client.get("/v1/artifacts", params={"workItemId": work_item["id"]}).json()["artifacts"]
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
    work_item = client.create_work_item(project["id"], "Hold")
    assert client.post(f"/v1/work-items/{work_item['id']}/deliver").status_code == 201

    run = wait_until(
        lambda: next((r for r in client.work_item_runs(work_item["id"]) if r["status"] == "running"), None),
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
    agent (lux-fake's MCP client) calls list_work, and dude records the
    call on that Run and answers with the project's work."""
    project, _ = lux_project
    client.patch(f"/v1/projects/{project['id']}", {"agentModels": {
        "implementer": {"model": "fake/tools"}, "reviewer": {"model": "fake/scripted"}, "simplifier": {"model": "fake/scripted"}}})
    work_item = client.create_work_item(project["id"], "Use the tools")
    assert client.post(f"/v1/work-items/{work_item['id']}/deliver").status_code == 201

    def called():
        runs = client.work_item_runs(work_item["id"])
        if not runs:
            return None
        return [e for e in client.events(runId=runs[0]["id"]) if e["eventType"] == "agent.tool.dude"] or None

    try:
        calls = wait_until(called, timeout=120, interval=1, message="the agent never called dude's tools on lux")
    finally:
        print("phases:", [(r["phase"], r["status"], r.get("error")) for r in client.work_item_runs(work_item["id"])])
    assert calls[0]["payload"]["tool"] == "list_work"
    assert "Use the tools" in json.dumps(calls[0]["payload"]["result"])
    # And through lux's service socket, as the dude CLI calls: lux added the
    # Run's token; the container never held it.
    progress = wait_until(lambda: [e for e in client.events(runId=calls[0]["runId"])
                                   if e["eventType"] == "agent.custom.progress"] or None,
                          timeout=60, interval=1, message="nothing came through lux's service socket")
    assert progress[0]["payload"]["data"]["step"] == "through the socket"
    # And the agent heard the answer: its reply carries the work item's key.
    implement = client.work_item_runs(work_item["id"])[0]
    messages = " ".join(e["payload"].get("text", "") for e in client.events(runId=implement["id"])
                        if e["eventType"] == "agent.message")
    assert work_item["key"] in messages, messages
