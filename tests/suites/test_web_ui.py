"""The web app, driven in a browser, delivering a work item to a pull request.

What an operator actually does: open the board, create a work item, press
Deliver, and watch the pipeline advance until a pull request exists — then
leave a comment on the forge and watch a fixer answer it. Every step is a
click or a read of the page; nothing reaches around the UI except the forge
side, which is where a person would comment and merge.

Runs against the local stand-in for GitHub, so it needs no network and no
token.
"""

from __future__ import annotations

import pytest
from playwright.sync_api import Page, expect

from fake_github import FakeGitHub
from helpers import ApiClient

pytestmark = pytest.mark.ui


def _sign_in(page: Page, web_url: str, api_key: str) -> None:
    page.goto(web_url)
    page.evaluate("localStorage.clear()")
    page.goto(web_url)
    page.fill('input[type="password"]', api_key)
    page.click('button[type="submit"]')
    expect(page.get_by_test_id("shell")).to_be_visible()


def test_the_board_shows_projects_and_opens_work_items(
    page: Page, web_url: str, client: ApiClient, forge_project: dict, org: dict, console_errors: list
):
    client.create_work_item(forge_project["id"], "Already queued up")
    _sign_in(page, web_url, org["api_key"])

    # With nothing selected, the first project's board is what opens.
    expect(page.get_by_text("Greeter").first).to_be_visible()
    card = page.get_by_text("Already queued up").last
    expect(card).to_be_visible()

    card.click()
    expect(page.get_by_test_id("work-item-screen")).to_be_visible()
    expect(page.get_by_test_id("deliver")).to_be_visible()
    assert console_errors == []


def test_delivering_from_the_ui_reaches_a_pull_request_and_back(
    page: Page,
    web_url: str,
    org: dict,
    forge_project: dict,
    fake_github: FakeGitHub,
    console_errors: list,
):
    _sign_in(page, web_url, org["api_key"])

    # Create the work item from the board, as an operator would.
    page.get_by_test_id("new-work-item").click()
    page.get_by_test_id("work-item-title").fill("Greet people by their full name")
    page.get_by_test_id("work-item-goal").fill("Use the full name, not just the first.")
    page.get_by_label("Criterion 1").fill("Greets with the full name")
    page.get_by_test_id("work-item-create-deliver").click()
    expect(page.get_by_test_id("work-item-screen")).to_be_visible()

    pipeline = page.get_by_test_id("pipeline")
    expect(pipeline).to_contain_text("Implement", timeout=60_000)
    # The reviewer raises something, the loop answers it, and a clean
    # re-review lets it through — all visible without a reload.
    expect(pipeline).to_contain_text("blocking", timeout=120_000)
    expect(page.get_by_test_id("findings")).to_be_visible()
    expect(pipeline).to_contain_text("no findings", timeout=120_000)
    expect(page.get_by_test_id("finding-status").first).to_have_text("resolved")

    # The PR appears as the last step, linked to the forge.
    expect(page.get_by_test_id("pr-step")).to_be_visible(timeout=180_000)
    pr_number = int(page.get_by_test_id("pr-link").get_attribute("href").rsplit("/", 1)[-1])
    assert pr_number in fake_github.pulls

    # Every agent in the pipeline opens its own conversation.
    page.get_by_test_id("phase").nth(1).click()
    expect(page.get_by_text("Reviewer").first).to_be_visible()
    page.get_by_role("button", name="Back").click()
    expect(page.get_by_test_id("work-item-screen")).to_be_visible()

    # A person comments on the forge; the page shows a fixer answering.
    phases_before = page.get_by_test_id("phase").count()
    fake_github.comment(pr_number, "Please also handle an empty name.")
    expect(page.get_by_test_id("phase")).to_have_count(phases_before + 1, timeout=90_000)
    expect(page.get_by_test_id("phase").last).to_contain_text("Completed", timeout=120_000)

    # Merging on the forge finishes the work item.
    fake_github.merge(pr_number)
    expect(page.locator(".wiHeader")).to_contain_text("Done", timeout=90_000)

    assert console_errors == []


def test_a_work_item_is_edited_and_moved_from_its_screen(
    page: Page, web_url: str, client: ApiClient, org: dict, forge_project: dict, console_errors: list
):
    epic = client.post(f"/v1/projects/{forge_project['id']}/epics", {"title": "Greetings"}).json()
    item = client.create_work_item(forge_project["id"], "Draft title")
    _sign_in(page, web_url, org["api_key"])
    page.get_by_text("Draft title").first.click()
    expect(page.get_by_test_id("work-item-screen")).to_be_visible()

    page.get_by_test_id("edit-work-item").click()
    page.get_by_test_id("work-item-title").fill("Greet by full name")
    page.get_by_label("Criterion 1").fill("Uses the full name")
    page.get_by_role("combobox", name="Epic").click()
    page.get_by_role("listbox").get_by_text("Greetings").click()
    page.get_by_test_id("work-item-save").click()

    expect(page.locator(".wiTitle")).to_have_text("Greet by full name")
    expect(page.locator(".wiCriteria")).to_contain_text("Uses the full name")
    saved = client.get(f"/v1/work-items/{item['id']}").json()
    assert saved["epicId"] == epic["id"]
    assert console_errors == []


def test_project_settings_manage_repositories_and_delivery(
    page: Page, web_url: str, client: ApiClient, org: dict, forge_project: dict, console_errors: list
):
    _sign_in(page, web_url, org["api_key"])
    page.get_by_test_id("project-settings-button").click()
    expect(page.get_by_test_id("project-settings")).to_be_visible()

    # A second repository, from the Repositories tab.
    page.get_by_test_id("add-repository").click()
    page.get_by_test_id("repository-name").fill("docs")
    page.get_by_test_id("repository-url").fill("https://github.com/acme/docs.git")
    page.get_by_test_id("repository-save").click()
    expect(page.get_by_role("cell", name="docs", exact=True)).to_be_visible()
    # A URL git could be tricked by is refused, and the dialog says why.
    page.get_by_test_id("add-repository").click()
    page.get_by_test_id("repository-name").fill("bad")
    page.get_by_test_id("repository-url").fill("ext::sh -c id")
    page.get_by_test_id("repository-save").click()
    expect(page.get_by_role("alert")).to_contain_text("url must be an https, ssh or git:// URL")
    page.keyboard.press("Escape")
    # That refusal is the only error the page saw.
    assert console_errors == ["Failed to load resource: the server responded with a status of 400 (Bad Request)"]
    console_errors.clear()

    # Security joins correctness on every delivery.
    page.get_by_role("tab", name="Delivery").click()
    page.get_by_role("checkbox", name="security").click()
    page.get_by_test_id("delivery-save").click()
    expect(page.get_by_text("Delivery saved")).to_be_visible()

    project = client.get(f"/v1/projects/{forge_project['id']}").json()
    assert "docs" in [r["name"] for r in project["repositories"]]
    assert "bad" not in [r["name"] for r in project["repositories"]]
    # Only what differs from the factory's defaults is stored: the rest keeps
    # following them.
    assert project["deliveryPolicy"] == {"requiredReviewers": ["correctness", "security"]}
    assert console_errors == []
