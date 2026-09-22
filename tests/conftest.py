"""pytest fixtures for the dude E2E suite."""

from __future__ import annotations

import os
import sys

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

import pytest

from env import TestEnvironment
from helpers import ApiClient, create_api_key, create_organization


def pytest_configure(config: pytest.Config) -> None:
    config.addinivalue_line("markers", "docker: needs Docker and the runner daemon")


@pytest.fixture(scope="session")
def env() -> TestEnvironment:
    """The environment set up by run_tests.py."""
    return TestEnvironment.from_env()


@pytest.fixture(scope="session")
def owner_dsn(env: TestEnvironment) -> str:
    """Owner-role DSN, for seeding and for assertions about stored state."""
    return env.owner_dsn


@pytest.fixture
def org(env: TestEnvironment) -> dict:
    """A fresh organization with a user API key.

    Per-test rather than per-session so suites cannot leak state into each
    other through shared projects or work items.
    """
    organization_id = create_organization(
        env.owner_dsn,
        f"org{os.urandom(4).hex()}",
        # A reviewer default, so tests can exercise the org fallback layer of
        # per-role model resolution.
        default_agent_models={"reviewer": {"model": "org-default-reviewer"}},
    )
    api_key = create_api_key(env.owner_dsn, organization_id)

    return {
        "id": organization_id,
        "api_key": api_key,
        "client": ApiClient(env.control_plane_url, api_key),
    }


@pytest.fixture
def client(org: dict) -> ApiClient:
    """API client scoped to the test's organization."""
    return org["client"]


@pytest.fixture
def second_org(env: TestEnvironment) -> dict:
    """A second organization, for tenant-isolation tests."""
    organization_id = create_organization(env.owner_dsn, f"org{os.urandom(4).hex()}")
    api_key = create_api_key(env.owner_dsn, organization_id)
    return {
        "id": organization_id,
        "api_key": api_key,
        "client": ApiClient(env.control_plane_url, api_key),
    }


@pytest.fixture
def project(client: ApiClient) -> dict:
    """A project with per-role agent models configured."""
    return client.create_project(
        name="E2E Project",
        slug=f"e2e-{os.urandom(3).hex()}",
        agentModels={
            "orchestrator": {"model": "project-orchestrator"},
            "implementer": {"model": "project-implementer", "harness": "opencode"},
        },
    )


@pytest.fixture
def runner(env: TestEnvironment, org: dict):
    """Start the Go runner inside the test's organization.

    The runner must share the organization whose Runs it executes: leases are
    tenant-scoped, so a runner in another organization would poll forever and
    see nothing.

    Function-scoped for that reason. Tests that need a runner are marked
    `docker` and are a small minority, so the startup cost is acceptable in
    exchange for each test getting a clean tenant.
    """
    runner_key = create_api_key(env.owner_dsn, org["id"], kind="runner")
    env.start_runner(runner_key)
    try:
        yield {"organization_id": org["id"], "api_key": runner_key}
    finally:
        proc = env.runner_proc
        if proc is not None:
            proc.terminate()
            try:
                proc.wait(timeout=10)
            except Exception:  # noqa: BLE001 - kill is the fallback
                proc.kill()
            env.runner_proc = None
