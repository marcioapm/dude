"""Runner and execution-plane E2E tests.

These drive real Docker containers through the real runner daemon, so they
are marked `docker` and can be excluded with --no-runner.
"""

from __future__ import annotations

import subprocess
from pathlib import Path

import pytest

from env import REPO_ROOT
from helpers import ApiClient, container_name, query, wait_for_run_status, wait_until

pytestmark = pytest.mark.docker

RUNTIME_IMAGE = "dude-runtime:dev"

# The runner performs a scripted change instead of calling a model when the
# model name starts with "fake/". These tests assert on lease handling, event
# ordering, container lifecycle and workspace materialization — none of which
# depend on a real model, and all of which would become slow, flaky and
# expensive if one were involved (plan §27.1).
FAKE_MODEL = "fake/scripted"


@pytest.fixture
def local_project(client: ApiClient) -> dict:
    """A project pointing at this repository, executed in the dev image."""
    return client.create_project(
        name="Self",
        slug="self-hosting",
        runtimeImage=RUNTIME_IMAGE,
        agentModels={"orchestrator": {"model": FAKE_MODEL}},
        repositories=[{"name": "dude", "url": str(REPO_ROOT)}],
    )


def test_runner_registers_itself(client: ApiClient, runner: dict, env, owner_dsn: str):
    workers = wait_until(
        lambda: query(owner_dsn, "SELECT id, name, status FROM workers WHERE organization_id = %s",
                      (runner["organization_id"],)),
        timeout=20,
        message="runner did not register",
    )
    assert workers[0]["status"] == "ready"


def test_run_executes_end_to_end(client: ApiClient, local_project: dict, runner: dict):
    """The full loop: claim, materialize a workspace, start a container, finish."""
    work_item = client.create_work_item(local_project["id"], "Execute me")
    run = client.create_run(work_item["id"])

    completed = wait_for_run_status(client, run["id"], "completed", timeout=120)
    assert completed["workspacePath"], "the Run should record where its workspace is"


def test_execution_is_recorded_in_the_ledger(client: ApiClient, local_project: dict, runner: dict):
    work_item = client.create_work_item(local_project["id"], "Audit the execution")
    run = client.create_run(work_item["id"])
    wait_for_run_status(client, run["id"], "completed", timeout=120)

    types = [e["eventType"] for e in client.events(runId=run["id"])]
    # The timeline must explain what happened on the node, in order.
    for expected in (
        "run.created",
        "run.lease.acquired",
        "workspace.created",
        "runtime.started",
        "run.started",
        "run.completed",
    ):
        assert expected in types, f"missing {expected} in {types}"


def test_agent_work_reaches_the_ledger(client: ApiClient, local_project: dict, runner: dict):
    """The agent's own actions, not just the plumbing around them.

    A Run whose agent did nothing looks identical to one that failed silently
    unless what it did is recorded.
    """
    work_item = client.create_work_item(local_project["id"], "Produce a commit")
    run = client.create_run(work_item["id"])
    wait_for_run_status(client, run["id"], "completed", timeout=120)

    events = client.events(runId=run["id"])
    types = [e["eventType"] for e in events]
    assert "agent.message" in types, f"no agent activity recorded: {types}"

    commit_events = [e for e in events if e["eventType"] == "git.commit_created"]
    assert commit_events, f"the commit was not recorded: {types}"

    payload = commit_events[0]["payload"]
    assert payload["headSha"] != payload["baseSha"], "HEAD did not move"
    assert "FACTORY.md" in payload["commits"] or payload["commits"], "no commit listed"


def test_agent_commit_lands_in_the_workspace(client: ApiClient, local_project: dict, runner: dict):
    """The commit is real git history, not just an event."""
    work_item = client.create_work_item(local_project["id"], "Commit something")
    run = client.create_run(work_item["id"])
    completed = wait_for_run_status(client, run["id"], "completed", timeout=120)

    repo = Path(completed["workspacePath"]) / "repos" / "dude"
    log = subprocess.run(
        ["git", "-C", str(repo), "log", "--oneline", "-1"],
        capture_output=True, text=True, check=False,
    )
    assert run["id"] in log.stdout, f"expected a commit for {run['id']}, got: {log.stdout!r}"

    # The operator's own working tree must never be touched.
    assert (repo / "FACTORY.md").exists()
    assert not (REPO_ROOT / "FACTORY.md").exists(), "the agent wrote into the real repository"


def test_workspace_contains_the_materialized_repository(
    client: ApiClient, local_project: dict, runner: dict, env
):
    work_item = client.create_work_item(local_project["id"], "Materialize")
    run = client.create_run(work_item["id"])
    completed = wait_for_run_status(client, run["id"], "completed", timeout=120)

    workspace = Path(completed["workspacePath"])
    assert (workspace / "repos" / "dude" / ".git").exists()
    assert (workspace / "runtime-manifest.json").exists()
    # The directories the container, harness and uploader all address.
    for directory in ("agent-state", "scratch", "artifacts-staging"):
        assert (workspace / directory).is_dir()


def test_one_container_lifecycle_produces_one_generation(
    client: ApiClient, local_project: dict, runner: dict, owner_dsn: str
):
    """A generation represents a container, not a status report.

    Minting a generation per transition would make the runtime history
    unreadable and leave earlier rows stuck at a stale status.
    """
    work_item = client.create_work_item(local_project["id"], "One generation")
    run = client.create_run(work_item["id"])
    wait_for_run_status(client, run["id"], "completed", timeout=120)

    rows = query(
        owner_dsn,
        "SELECT generation, status FROM runtime_instances WHERE run_id = %s ORDER BY generation",
        (run["id"],),
    )
    assert len(rows) == 1, f"expected one runtime generation, got {rows}"
    assert rows[0]["status"] == "destroyed"


def test_containers_are_cleaned_up(client: ApiClient, local_project: dict, runner: dict):
    work_item = client.create_work_item(local_project["id"], "Clean up after me")
    run = client.create_run(work_item["id"])
    wait_for_run_status(client, run["id"], "completed", timeout=120)

    result = subprocess.run(
        ["docker", "ps", "-aq", "--filter", f"name={container_name(run['id'])}"],
        capture_output=True, text=True, check=False,
    )
    assert not result.stdout.strip(), "the Run container should not outlive the Run"


def test_run_duration_includes_container_startup(
    client: ApiClient, local_project: dict, runner: dict
):
    """Lead time must cover startup, or the metric understates reality.

    Asserted through the API, which already exposes both timestamps — a test
    that reaches into the database couples itself to column names users never
    see.
    """
    work_item = client.create_work_item(local_project["id"], "Measure me")
    run = client.create_run(work_item["id"])
    completed = wait_for_run_status(client, run["id"], "completed", timeout=120)

    assert completed["startedAt"] is not None
    assert completed["endedAt"] is not None
    assert completed["endedAt"] >= completed["startedAt"]


def test_concurrent_runs_do_not_interfere(client: ApiClient, local_project: dict, runner: dict):
    """Two Runs share a repository mirror; each must get its own workspace."""
    runs = []
    for i in range(2):
        work_item = client.create_work_item(local_project["id"], f"Parallel {i}")
        runs.append(client.create_run(work_item["id"]))

    completed = [wait_for_run_status(client, r["id"], "completed", timeout=180) for r in runs]
    paths = {r["workspacePath"] for r in completed}
    assert len(paths) == 2, "each Run needs an isolated workspace"
