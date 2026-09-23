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
    types = {e["eventType"] for e in client.events(runId=implement["id"])}
    assert {"agent.session.started", "agent.message", "run.completed", "git.commit_created"} <= types, types


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
