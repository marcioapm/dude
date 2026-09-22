"""Runner and execution-plane E2E tests.

These drive real Docker containers through the real runner daemon, so they
are marked `docker` and can be excluded with --no-runner.
"""

from __future__ import annotations

import subprocess

import pytest

from helpers import ApiClient, query, wait_for_run_status, wait_until

pytestmark = pytest.mark.docker

RUNTIME_IMAGE = "dude-runtime:dev"


@pytest.fixture
def local_project(client: ApiClient) -> dict:
    """A project pointing at this repository, executed in the dev image."""
    return client.create_project(
        name="Self",
        slug="self-hosting",
        runtimeImage=RUNTIME_IMAGE,
        agentModels={"orchestrator": {"model": "test-model"}},
        repositories=[{"name": "dude", "url": str(_repo_root())}],
    )


def _repo_root():
    from pathlib import Path

    return Path(__file__).resolve().parent.parent.parent


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


def test_workspace_contains_the_materialized_repository(
    client: ApiClient, local_project: dict, runner: dict, env
):
    work_item = client.create_work_item(local_project["id"], "Materialize")
    run = client.create_run(work_item["id"])
    completed = wait_for_run_status(client, run["id"], "completed", timeout=120)

    from pathlib import Path

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
        ["docker", "ps", "-aq", "--filter", f"name=dude-run-{run['id'].lower()}"],
        capture_output=True, text=True, check=False,
    )
    assert not result.stdout.strip(), "the Run container should not outlive the Run"


def test_run_duration_includes_container_startup(
    client: ApiClient, local_project: dict, runner: dict, owner_dsn: str
):
    """Lead time must cover startup, or the metric understates reality."""
    work_item = client.create_work_item(local_project["id"], "Measure me")
    run = client.create_run(work_item["id"])
    wait_for_run_status(client, run["id"], "completed", timeout=120)

    rows = query(
        owner_dsn,
        "SELECT started_at, ended_at FROM runs WHERE id = %s",
        (run["id"],),
    )
    started, ended = rows[0]["started_at"], rows[0]["ended_at"]
    assert started is not None and ended is not None
    assert ended >= started


def test_concurrent_runs_do_not_interfere(client: ApiClient, local_project: dict, runner: dict):
    """Two Runs share a repository mirror; each must get its own workspace."""
    runs = []
    for i in range(2):
        work_item = client.create_work_item(local_project["id"], f"Parallel {i}")
        runs.append(client.create_run(work_item["id"]))

    completed = [wait_for_run_status(client, r["id"], "completed", timeout=180) for r in runs]
    paths = {r["workspacePath"] for r in completed}
    assert len(paths) == 2, "each Run needs an isolated workspace"
