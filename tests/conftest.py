"""pytest fixtures for the dude E2E suite."""

from __future__ import annotations

import os
import sys
import time

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

import pytest
import requests
from playwright.sync_api import Page

from env import TestEnvironment
from helpers import ApiClient, create_api_key, create_organization, wait_until, webhook_secret


def pytest_configure(config: pytest.Config) -> None:
    config.addinivalue_line("markers", "ui: drives a real browser via Playwright")


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


# ---------------------------------------------------------------------------
# UI testing
# ---------------------------------------------------------------------------


@pytest.fixture(scope="session")
def browser_type_launch_args(browser_type_launch_args):
    """Use the system Chrome.

    Playwright's bundled Chromium has no build for this platform, and pinning
    to the installed browser keeps CI and local runs on the same engine.
    """
    return {**browser_type_launch_args, "channel": "chrome"}


@pytest.fixture(scope="session")
def gallery_url(env: TestEnvironment) -> str:
    """Serve the built gallery for the duration of the session."""
    url = env.start_gallery()

    def reachable() -> bool:
        try:
            return requests.get(url, timeout=1).status_code == 200
        except requests.RequestException:
            return False

    wait_until(reachable, timeout=20, interval=0.1, message=f"gallery did not start at {url}")
    yield url


@pytest.fixture
def fake_github(env: TestEnvironment):
    """A local stand-in for GitHub: a git daemon to push to, and its API.

    Its repositories live under the environment's git root, which is where
    the fake lux pushes, so an agent's commits land where the forge sees them.
    Each test gets its own owner, so their repositories never collide.
    """
    from fake_github import FakeGitHub

    fake = FakeGitHub(env.git_root, owner=f"o{os.urandom(4).hex()}")
    fake.start()
    yield fake
    fake.stop()


@pytest.fixture
def forge_project(client: ApiClient, org: dict, env: TestEnvironment, fake_github) -> dict:
    """A project on the fake forge, run end to end by scripted fake agents.

    The forge credential points at the fake's API, so the factory opens pull
    requests there through the same REST calls it makes to github.com, and the
    fake delivers webhooks to dude signed with the secret dude minted.
    """
    resp = client.post(
        "/v1/forge/credential",
        {"auth": "pat", "secret": "fake-token", "apiBaseUrl": fake_github.api_url},
    )
    assert resp.status_code == 200, resp.text
    fake_github.webhook_url = env.control_plane_url + resp.json()["webhookPath"]
    fake_github.webhook_secret = webhook_secret(env.owner_dsn, org["id"])
    return client.create_project(
        name="Greeter",
        slug=f"greeter-{fake_github.api_port}",
        runtimeImage="dude-runtime:test",
        agentModels={
            "implementer": {"model": "fake/scripted"},
            "reviewer": {"model": "fake/scripted"},
            "simplifier": {"model": "fake/scripted"},
        },
        repositories=[
            {"name": "greeter", "url": fake_github.clone_url, "defaultBranch": "main"}
        ],
    )


@pytest.fixture(scope="session")
def web_url(env: TestEnvironment) -> str:
    """The built web app, served against this run's control plane."""
    url = env.start_web()
    yield url
    if env.web_proc:
        env.web_proc.terminate()


@pytest.fixture
def console_errors(page: Page) -> list[str]:
    """Collect console errors and page exceptions for the current test.

    Returned as a live list so a test can assert on it after interacting; an
    error raised during render shows up here rather than silently passing.
    """
    errors: list[str] = []

    def record(text: str) -> None:
        # The gallery loads a webfont from a CDN for convenience; the product
        # ships its own fonts, so a failure to reach it is not a UI defect.
        if "fonts" in text or "rsms.me" in text:
            return
        errors.append(text)

    page.on("console", lambda m: record(m.text) if m.type == "error" else None)
    page.on("pageerror", lambda e: record(str(e)))
    return errors


@pytest.fixture
def gallery_page(page: Page, gallery_url: str, console_errors: list[str]) -> Page:
    """The gallery, loaded and settled.

    Depends on `console_errors` so the listener is attached before navigation
    and catches errors thrown during the first render.
    """
    page.goto(gallery_url, wait_until="networkidle")
    page.wait_for_timeout(300)
    return page
