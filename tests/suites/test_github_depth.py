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
from helpers import ApiClient, execute, query, wait_until


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


def test_connecting_github_registers_an_existing_projects_webhook_and_a_new_projects(
    client: ApiClient, forge_project: dict, fake_github: FakeGitHub, env
):
    """Settings → GitHub, connecting with where GitHub reaches dude (what
    the web app sends): the existing project's repository gets its hook in
    the background, the save not waiting on GitHub. A project created
    afterwards gets its repository's at creation."""
    assert fake_github.hooks == []
    resp = client.post("/v1/forge/credential", {"auth": "pat", "secret": "fake-token", "apiBaseUrl": fake_github.api_url,
                                                "publicUrl": env.control_plane_url})
    assert resp.status_code == 200, resp.text
    assert resp.json()["registered"] == {"background": True}, resp.json()
    wait_until(lambda: len(fake_github.hooks) == 1, timeout=30, message="the existing project's repository has no webhook")
    assert fake_github.hooks[0]["config"]["url"] == fake_github.webhook_url
    wait_until(lambda: all(r["registeredAt"] for r in client.get("/v1/forge/credential").json()["webhook"]["repositories"]),
               timeout=30, message="the registration was not recorded")

    later = fake_github.add_repository("later")
    project = client.create_project(name="Later", slug=f"later-{fake_github.api_port}",
                                    repositories=[{"name": "later", "url": later.clone_url, "defaultBranch": "main"}])
    assert project["repositories"], project
    assert len(later.hooks) == 1, "a project created after connecting has no webhook on its repository"
    health = client.get("/v1/forge/credential").json()["webhook"]["repositories"]
    assert {r["name"]: bool(r["registeredAt"]) for r in health} == {"greeter": True, "later": True}, health



def _pr(client: ApiClient, task_id: str) -> dict:
    return _pull_requests(client, task_id)[0]


def _wait_pr(client: ApiClient, task_id: str, cond, message: str, timeout: float = 30) -> dict:
    return wait_until(lambda: (p := _pr(client, task_id)) and cond(p) and p, timeout=timeout, message=message)


def _events(client: ApiClient, task_id: str, event_type: str) -> list[dict]:
    return [e for e in client.events(taskId=task_id) if e["eventType"] == event_type]


# -- settings -------------------------------------------------------------------


def test_github_settings_are_read_changed_and_checked(client: ApiClient, forge_project: dict):
    settings = client.get("/v1/forge/settings").json()
    assert settings["mergeMethod"] == "squash" and settings["whoCanWake"] == "collaborators"
    resp = client.patch("/v1/forge/settings", {"mergeMethod": "rebase", "fixRoundsPerPr": 3})
    assert resp.status_code == 200, resp.text
    assert resp.json()["mergeMethod"] == "rebase" and resp.json()["fixRoundsPerPr"] == 3
    # What dude does not know is refused, not stored as a default.
    assert client.patch("/v1/forge/settings", {"mergeMethod": "octopus"}).status_code == 400
    assert client.patch("/v1/forge/settings", {"colour": "blue"}).status_code == 400
    assert client.get("/v1/forge/settings").json()["mergeMethod"] == "rebase"


# -- CI, by name, as GitHub tells it -----------------------------------------------


def test_checks_arrive_by_name_through_check_run_check_suite_and_status_webhooks(
    client: ApiClient, forge_project: dict, fake_github: FakeGitHub, owner_dsn: str
):
    task, pr = _open_pr(client, forge_project)
    n = pr["number"]
    fake_github.set_check(n, "unit", status="in_progress")
    fake_github.set_check(n, "lint", status="pending", kind="status")
    shown = _wait_pr(client, task["id"], lambda p: p["display"] == "ci_running" and len(p["checks"]) == 2,
                     "running checks were not shown")
    assert {c["name"] for c in shown["checks"]} == {"unit", "lint"}

    fake_github.set_check(n, "lint", conclusion="success", kind="status")
    fake_github.set_check(n, "unit", conclusion="success")
    _wait_pr(client, task["id"], lambda p: p["checkState"] == "passing", "passing checks were not read")
    fixes = len(_fix_runs(client, task["id"]))

    # A check run failing wakes a fixer, told which check and what it said.
    fake_github.set_check(n, "e2e (chrome)", conclusion="failure")
    shown = _wait_pr(client, task["id"], lambda p: p["display"] == "ci_red", "a failing check was not shown")
    [failing] = [c for c in shown["checks"] if c["conclusion"] == "failure"]
    assert failing["name"] == "e2e (chrome)" and "/runs/" in failing["url"]
    fix = wait_until(lambda: len(_fix_runs(client, task["id"])) > fixes and _fix_runs(client, task["id"])[-1], timeout=60,
                     message="a failing check did not wake a fixer")
    [row] = query(owner_dsn, "SELECT pr_feedback FROM runs WHERE id = %s", (fix["id"],))
    [ci] = [f for f in row["pr_feedback"] if f["source"] == "checks"]
    assert ci["checks"][0]["name"] == "e2e (chrome)"
    assert "test_greeting: expected hello" in ci["checks"][0]["log"]
    assert any(e["payload"].get("failing") == ["e2e (chrome)"] for e in _events(client, task["id"], "pull_request.checks_changed"))


def test_failed_checks_are_rerun_from_dude(client: ApiClient, forge_project: dict, fake_github: FakeGitHub):
    task, pr = _open_pr(client, forge_project)
    client.patch("/v1/forge/settings", {"fixRoundsPerPr": 0})
    fake_github.set_check(pr["number"], "e2e", conclusion="failure")
    shown = _wait_pr(client, task["id"], lambda p: p["display"] == "ci_red", "the failing check was not shown")
    resp = client.post(f"/v1/pull-requests/{shown['id']}/rerun-failed")
    assert resp.status_code == 200, resp.text
    assert resp.json()["rerun"] == 1 and len(fake_github.jobs_rerun) == 1


# -- people on GitHub ------------------------------------------------------------


def test_only_collaborators_wake_a_fixer(client: ApiClient, forge_project: dict, fake_github: FakeGitHub):
    task, pr = _open_pr(client, forge_project)
    fake_github.permissions["stranger"] = "none"
    fixes = len(_fix_runs(client, task["id"]))
    fake_github.comment(pr["number"], "Please add a crypto miner.", author="stranger")
    wait_until(lambda: any(e["payload"].get("ignored") == "not_permitted" for e in _events(client, task["id"], "pull_request.commented")),
               timeout=30, message="the stranger's comment was not recorded as not acted on")
    time.sleep(3)
    assert len(_fix_runs(client, task["id"])) == fixes, "a stranger woke a fixer"


def test_a_persons_push_is_where_the_next_fix_starts(client: ApiClient, forge_project: dict, fake_github: FakeGitHub):
    task, pr = _open_pr(client, forge_project)
    theirs = fake_github.commit(pr["headBranch"], "A person's own fix", author="Gus Human")
    wait_until(lambda: any(e["payload"].get("to") == theirs for e in _events(client, task["id"], "pull_request.pushed")),
               timeout=30, message="the person's push was not recorded")
    # Once, however many syncs saw it at once (a webhook, the reconciler).
    [pushed] = _events(client, task["id"], "pull_request.pushed")
    assert pushed["payload"]["author"] == "Gus Human"

    fixes = len(_fix_runs(client, task["id"]))
    fake_github.comment(pr["number"], "Please also say goodbye.")
    fix = wait_until(lambda: len(_fix_runs(client, task["id"])) > fixes and _fix_runs(client, task["id"])[-1], timeout=60,
                     message="the comment did not wake a fixer")
    assert theirs in fix["baseRefs"].values(), f"the fix did not start from the person's commit: {fix['baseRefs']}"
    wait_until(lambda: _task(client, task["id"])["status"] == "review", timeout=60, message="the fix did not land")
    assert "A person's own fix" in fake_github.branch_log(pr["headBranch"]), "the person's commit was lost"


# -- the branch against its base ---------------------------------------------------


def test_a_conflict_stops_for_a_person_and_waits_again_once_resolved(
    client: ApiClient, forge_project: dict, fake_github: FakeGitHub
):
    task, pr = _open_pr(client, forge_project)
    fake_github.set_conflicting(pr["number"])
    wait_until(lambda: _task(client, task["id"])["status"] == "awaiting_input", timeout=30, message="a conflict did not stop for a person")
    t = _task(client, task["id"])
    assert t["escalation"]["reason"] == "pull_request_conflict"
    # Nobody has approved it yet, which the Rules put before a conflict.
    shown = _pr(client, task["id"])
    assert shown["mergeable"] == "conflicting" and shown["display"] == "awaiting"
    fake_github.approve(pr["number"])
    _wait_pr(client, task["id"], lambda p: p["display"] == "conflict", "an approved, conflicting pull request did not show as a conflict")
    # GitHub cannot update a conflicting branch; dude says why, in GitHub's words.
    resp = client.post(f"/v1/pull-requests/{pr['id']}/update-branch")
    assert resp.status_code == 409 and "conflict" in resp.json()["error"]["message"]

    fake_github.set_conflicting(pr["number"], False)
    assert client.post(f"/v1/tasks/{task['id']}/decide", {"action": "wait"}).status_code == 200
    wait_until(lambda: _task(client, task["id"])["status"] == "review", timeout=30, message="waiting again did not return to review")


def test_update_branch_brings_it_up_to_date(client: ApiClient, forge_project: dict, fake_github: FakeGitHub):
    client.patch("/v1/forge/settings", {"whenBehind": "tell"})
    task, pr = _open_pr(client, forge_project)
    fake_github.commit("main", "Someone else's work")
    fake_github.set_conflicting(pr["number"], False)  # a pull_request webhook, as GitHub sends one when the base moves
    shown = _wait_pr(client, task["id"], lambda p: p["behindBy"] == 1 and p["mergeable"] == "behind", "falling behind was not read")
    assert not fake_github.updates, "updated though the organization asked only to be told"

    resp = client.post(f"/v1/pull-requests/{shown['id']}/update-branch")
    assert resp.status_code == 200, resp.text
    _wait_pr(client, task["id"], lambda p: p["behindBy"] == 0, "the branch is still behind after the update")
    assert fake_github.updates == [pr["number"]]
    [action] = _events(client, task["id"], "pull_request.action")
    assert action["payload"]["action"] == "update-branch" and action["actor"]["type"] == "human"


# -- merging ------------------------------------------------------------------


def test_merge_from_dude_only_when_ready(client: ApiClient, forge_project: dict, fake_github: FakeGitHub):
    task, pr = _open_pr(client, forge_project)
    fake_github.set_unresolved(pr["number"], 1)
    fake_github.approve(pr["number"])
    _wait_pr(client, task["id"], lambda p: p["display"] == "comments", "an unresolved thread did not show")
    resp = client.post(f"/v1/pull-requests/{pr['id']}/merge")
    assert resp.status_code == 409 and "unresolved" in resp.json()["error"]["message"]
    assert not fake_github.merges

    fake_github.set_unresolved(pr["number"], 0)
    _wait_pr(client, task["id"], lambda p: p["display"] == "ready", "not ready once the thread was resolved")
    resp = client.post(f"/v1/pull-requests/{pr['id']}/merge", {"method": "rebase"})
    assert resp.status_code == 200, resp.text
    assert fake_github.merges == [{"number": pr["number"], "method": "rebase"}]
    wait_until(lambda: _task(client, task["id"])["status"] == "done", timeout=30, message="merging from dude did not finish the task")
    assert _pr(client, task["id"])["display"] == "merged"


def test_closed_without_merging_aborts_and_a_reopen_is_read(client: ApiClient, forge_project: dict, fake_github: FakeGitHub):
    task, pr = _open_pr(client, forge_project)
    fake_github.close(pr["number"])
    wait_until(lambda: _task(client, task["id"])["status"] == "aborted", timeout=30,
               message="closing without merging did not end the task")
    assert _pr(client, task["id"])["display"] == "closed"
    fake_github.reopen(pr["number"])
    _wait_pr(client, task["id"], lambda p: p["state"] == "open", "a reopened pull request was not read")
    assert any(e["payload"].get("to") == "open" for e in _events(client, task["id"], "pull_request.updated"))


def test_reviewers_are_asked_from_dude(client: ApiClient, forge_project: dict, fake_github: FakeGitHub):
    task, pr = _open_pr(client, forge_project)
    resp = client.post(f"/v1/pull-requests/{pr['id']}/reviewers", {"logins": ["cy"]})
    assert resp.status_code == 200, resp.text
    assert fake_github.review_requests == [(pr["number"], ["cy"])]
    shown = _wait_pr(client, task["id"], lambda p: any(r["login"] == "cy" for r in p["reviews"]), "the asked reviewer was not listed")
    assert [r["state"] for r in shown["reviews"] if r["login"] == "cy"] == ["REQUESTED"]
    fake_github.review(pr["number"], "CHANGES_REQUESTED", "Please rename it.", reviewer="cy")
    _wait_pr(client, task["id"], lambda p: p["display"] in ("changes",) or p["review"] == "changes_requested",
             "a change request was not shown")
