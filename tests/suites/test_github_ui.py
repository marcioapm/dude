"""The task's pull request panel and Settings → GitHub, in a browser.

A pull request is opened by a delivery against the fake GitHub; the
person side (checks, reviews, falling behind) is played on the fake, and
the panel is expected to show it and act on it: Re-run failed, Update
branch, Request review, Merge — off, with why, until it is ready.
"""

from __future__ import annotations

import pytest
from playwright.sync_api import Page, expect

from fake_github import FakeGitHub
from helpers import ApiClient, sign_in, wait_until

pytestmark = pytest.mark.ui


def _sign_in(page: Page, web_url: str, api_key: str) -> None:
    sign_in(page, web_url, api_key)


def _open_pr(client: ApiClient, project: dict) -> tuple[dict, dict]:
    task = client.create_task(project["id"], "Chart library: visx, and the revenue chart")
    assert client.post(f"/v1/tasks/{task['id']}/deliver").status_code == 201
    pr = wait_until(lambda: client.get("/v1/pull-requests", params={"taskId": task["id"]}).json()["pullRequests"], timeout=60,
                    message="no pull request opened")[0]
    wait_until(lambda: client.get(f"/v1/tasks/{task['id']}").json()["status"] == "review", timeout=30,
               message="task never reached review")
    return task, pr


def test_the_pull_request_panel_shows_github_and_acts_on_it(
    page: Page, web_url: str, client: ApiClient, org: dict, forge_project: dict, fake_github: FakeGitHub, console_errors: list
):
    client.patch("/v1/forge/settings", {"whenBehind": "tell"})
    task, pr = _open_pr(client, forge_project)
    n = pr["number"]
    # Cy asks for a change; a fixer answers it, and it lands.
    fake_github.review(n, "CHANGES_REQUESTED", "Please keep fetchRevenue.", reviewer="cy")
    wait_until(lambda: [r for r in client.task_runs(task["id"]) if r["phase"] == "fix" and r["status"] == "completed"
                        and r["createdAt"] > pr["createdAt"]], timeout=90, message="the change request was not fixed")
    wait_until(lambda: client.get(f"/v1/tasks/{task['id']}").json()["status"] == "review", timeout=60, message="the fix did not land")
    fake_github.commit("main", "Someone else's work")
    fake_github.set_unresolved(n, 2)

    _sign_in(page, web_url, org["api_key"])
    page.goto(f"{web_url}/#/task/{task['id']}")
    panel = page.get_by_test_id("pr-panel")
    expect(panel).to_contain_text("cy requested changes", timeout=30_000)
    expect(panel).to_contain_text("2 unresolved comments")
    expect(panel).to_contain_text("1 commit behind main · no conflicts")
    page.get_by_test_id("pr-update-branch").click()
    expect(panel).to_contain_text("Up to date with main", timeout=30_000)
    assert fake_github.updates == [n]

    # CI on the updated head: one check red. Re-run it from here.
    fake_github.set_check(n, "unit", conclusion="success")
    fake_github.set_check(n, "e2e (chrome)", conclusion="failure")
    expect(panel).to_contain_text("1 of 2 checks failing", timeout=30_000)
    expect(panel).to_contain_text("e2e (chrome)")
    expect(page.get_by_test_id("pr-merge")).to_be_disabled()
    expect(page.get_by_test_id("pr-blocked")).to_contain_text("e2e (chrome) failing")
    page.get_by_test_id("pr-rerun").click()
    wait_until(lambda: len(fake_github.jobs_rerun) == 1, timeout=10, message="Re-run failed did not reach GitHub")
    # The failure woke a fixer too: its fix lands before anything is merged.
    wait_until(lambda: client.get(f"/v1/tasks/{task['id']}").json()["status"] == "review", timeout=90, message="the CI fix did not land")

    page.get_by_test_id("pr-request-review").click()
    page.get_by_test_id("pr-review-logins").fill("bo")
    page.get_by_test_id("pr-review-send").click()
    expect(panel).to_contain_text("bo · review requested", timeout=30_000)

    # Everything seen to on GitHub: ready, and merged from here.
    fake_github.set_check(n, "unit", conclusion="success")
    fake_github.set_check(n, "e2e (chrome)", conclusion="success")
    fake_github.set_unresolved(n, 0)
    fake_github.review(n, "APPROVED", reviewer="cy")
    expect(page.get_by_test_id("pr-merge")).to_be_enabled(timeout=30_000)
    page.get_by_test_id("pr-merge").click()
    expect(page.get_by_test_id("task-header")).to_contain_text("Done", timeout=30_000)
    assert fake_github.merges == [{"number": n, "method": "squash"}]

    page.get_by_role("tab", name="Activity").click()
    activity = page.get_by_test_id("activity")
    expect(activity).to_contain_text("cy requested changes on #1")
    expect(activity).to_contain_text("CI e2e (chrome) failed on #1")
    expect(activity).to_contain_text("merged #1 (squash)")
    expect(activity).to_contain_text("updated #1's branch")
    assert console_errors == []


def test_settings_show_webhook_health_and_save_how_dude_behaves_on_github(
    page: Page, web_url: str, client: ApiClient, org: dict, forge_project: dict, fake_github: FakeGitHub, env, console_errors: list
):
    _sign_in(page, web_url, org["api_key"])
    page.get_by_test_id("org-settings-button").click()
    page.locator('[data-settings-nav="github"]').click()
    expect(page.get_by_test_id("webhook-summary")).to_contain_text("0 of 1 repository registered")
    page.get_by_test_id("webhook-register").click()
    expect(page.get_by_test_id("webhook-summary")).to_contain_text("1 of 1 repository registered", timeout=15_000)
    assert len(fake_github.hooks) == 1

    page.get_by_test_id("setting-merge-method").get_by_text("Rebase").click()
    page.get_by_test_id("setting-fix-rounds").fill("3")
    page.get_by_test_id("github-save").click()
    wait_until(lambda: client.get("/v1/forge/settings").json()["mergeMethod"] == "rebase", timeout=10,
               message="the merge method was not saved")
    assert client.get("/v1/forge/settings").json()["fixRoundsPerPr"] == 3
    assert console_errors == []
