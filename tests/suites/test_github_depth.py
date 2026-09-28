"""GitHub, deeper: the webhooks a person can set up and trust, who may wake a
fixer, a fix that starts from a person's push, mergeability, checks by name,
and the pull request actions dude takes on a person's behalf.

Everything reaches dude as GitHub would tell it: signed webhooks from the
fake GitHub (`fake_github.py`), including the check_run, check_suite and
status deliveries CI sends.
"""

from __future__ import annotations

import time

from fake_github import FakeGitHub
from helpers import ApiClient, query, wait_until


def _pull_requests(client: ApiClient, task_id: str) -> list[dict]:
    return client.get("/v1/pull-requests", params={"taskId": task_id}).json()["pullRequests"]


def _task(client: ApiClient, task_id: str) -> dict:
    return client.get(f"/v1/tasks/{task_id}").json()


def _open_pr(client: ApiClient, project: dict, title: str = "Deliver a change") -> tuple[dict, dict]:
    task = client.create_task(project["id"], title)
    assert client.post(f"/v1/tasks/{task['id']}/deliver").status_code == 201
    pr = wait_until(lambda: _pull_requests(client, task["id"]), timeout=60, message="no pull request opened")[0]
    wait_until(lambda: _task(client, task["id"])["status"] == "review", timeout=30, message="task never reached review")
    return task, pr


def _fix_runs(client: ApiClient, task_id: str) -> list[dict]:
    return [r for r in client.task_runs(task_id) if r["phase"] == "fix"]


# -- webhooks -----------------------------------------------------------------


def test_webhooks_register_reveal_rotate_and_report_health(
    client: ApiClient, forge_project: dict, fake_github: FakeGitHub, env
):
    """Register: a hook on each repository with the secret dude shows; a
    rotation re-registers it, the old secret still accepted meanwhile; and
    settings say when the last delivery came and what failed."""
    cred = client.get("/v1/forge/credential").json()
    assert cred["webhook"]["lastDeliveryAt"] is None

    resp = client.post("/v1/forge/webhooks/register", {"url": env.control_plane_url})
    assert resp.status_code == 200, resp.text
    [registered] = resp.json()["repositories"]
    assert registered["slug"] == f"{fake_github.owner}/{fake_github.repo}" and not registered.get("error")
    [hook] = fake_github.hooks
    secret = client.get("/v1/forge/webhook-secret").json()["secret"]
    assert hook["config"]["secret"] == secret
    assert hook["config"]["url"].endswith(fake_github.webhook_url.split(env.control_plane_url)[1])
    assert set(hook["events"]) >= {"check_run", "check_suite", "status", "pull_request"}

    # Registering again reuses the hook.
    client.post("/v1/forge/webhooks/register", {"url": env.control_plane_url})
    assert len(fake_github.hooks) == 1

    # A signed delivery is health; a badly signed one a failure, with why.
    assert fake_github.send_webhook("ping", {}) == 200
    assert fake_github.send_webhook("ping", {}, secret="wrong") == 401
    health = client.get("/v1/forge/credential").json()["webhook"]
    assert health["lastDeliveryAt"] and health["failedToday"] == 1
    assert "signature" in health["lastFailure"]
    [repo] = health["repositories"]
    assert repo["registeredAt"] and repo["hookId"] == "1"

    rotated = client.post("/v1/forge/webhook-secret/rotate").json()
    assert rotated["secret"] != secret
    assert fake_github.hooks[0]["config"]["secret"] == rotated["secret"], "the hook kept the old secret"
    # Deliveries GitHub signed before it heard of the new one still count.
    assert fake_github.send_webhook("ping", {}, secret=secret) == 200
    fake_github.webhook_secret = rotated["secret"]
    assert fake_github.send_webhook("ping", {}) == 200


def test_a_repository_added_later_gets_its_webhook(client: ApiClient, forge_project: dict, fake_github: FakeGitHub, env):
    client.post("/v1/forge/webhooks/register", {"url": env.control_plane_url})
    sibling = fake_github.add_repository("second")
    resp = client.post(f"/v1/projects/{forge_project['id']}/repositories",
                       {"name": "second", "url": sibling.clone_url})
    assert resp.status_code == 201, resp.text
    assert len(sibling.hooks) == 1, "the new repository's webhook was not registered"
