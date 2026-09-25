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


def _toast(page: Page, text: str):
    """A toast, by its text. Not get_by_text alone: for its first second Radix
    also renders a hidden copy of the text for screen readers, so the text
    matches twice and the check fails at once — only when it runs in that
    second, which is why it failed now and then."""
    return page.get_by_role("region", name="Notifications").get_by_role("listitem").filter(has_text=text)


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
    expect(page.get_by_test_id("finding").first).to_have_attribute("data-status", "resolved")

    # What the implementer published is there to read, rendered.
    artifact = page.get_by_test_id("artifact").filter(has_text="NOTES.md")
    expect(artifact).to_be_visible(timeout=60_000)
    artifact.get_by_role("button", expanded=False).click()
    expect(artifact.get_by_role("heading", name="What changed")).to_be_visible()

    # The PR appears as the last step, linked to the forge.
    expect(page.get_by_test_id("pr-step")).to_be_visible(timeout=180_000)
    pr_number = int(page.get_by_test_id("pr-link").get_attribute("href").rsplit("/", 1)[-1])
    assert pr_number in fake_github.pulls

    # Every agent in the pipeline opens its own conversation.
    page.get_by_test_id("phase").nth(1).click()
    expect(page.get_by_text("Reviewer").first).to_be_visible()
    # Back up to the work item through the breadcrumb, by its key.
    page.get_by_role("navigation", name="Breadcrumb").get_by_role("button", name="GREE-1").click()
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


def test_a_work_item_names_the_repositories_it_changes_and_reads(
    page: Page, web_url: str, client: ApiClient, org: dict, forge_project: dict, console_errors: list
):
    target = forge_project["repositories"][0]
    docs = client.post(f"/v1/projects/{forge_project['id']}/repositories",
                       {"name": "docs", "url": "https://github.com/acme/docs.git"}).json()
    _sign_in(page, web_url, org["api_key"])
    page.get_by_test_id("new-work-item").click()
    page.get_by_test_id("work-item-title").fill("Document the greeting")
    chooser = page.get_by_test_id("work-item-repositories")
    # Nothing chosen says what that means.
    expect(chooser).to_contain_text("changes no code")
    chooser.get_by_role("checkbox", name=target["name"]).click()
    chooser.get_by_role("checkbox", name="docs").click()
    chooser.get_by_role("combobox", name="What the work does in docs").click()
    page.get_by_role("listbox").get_by_text("Reads it").click()
    expect(chooser).to_contain_text("own pull request")
    page.get_by_test_id("work-item-save").click()
    expect(page.get_by_test_id("work-item-screen")).to_be_visible()

    items = client.get("/v1/work-items", params={"projectId": forge_project["id"]}).json()["workItems"]
    saved = next(i for i in items if i["title"] == "Document the greeting")
    assert sorted((r["id"], r["access"]) for r in saved["repositories"]) == sorted(
        [(target["id"], "write"), (docs["id"], "read")])
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
    expect(_toast(page, "Delivery saved")).to_be_visible()

    project = client.get(f"/v1/projects/{forge_project['id']}").json()
    assert "docs" in [r["name"] for r in project["repositories"]]
    assert "bad" not in [r["name"] for r in project["repositories"]]
    # Only what differs from the factory's defaults is stored: the rest keeps
    # following them.
    assert project["deliveryPolicy"] == {"requiredReviewers": ["correctness", "security"]}
    assert console_errors == []


def test_a_new_project_starts_from_the_empty_screen(page: Page, web_url: str, client: ApiClient, org: dict, console_errors: list):
    """An organization with nothing yet is offered a project, not told to use the API."""
    _sign_in(page, web_url, org["api_key"])
    page.get_by_test_id("new-project-empty").click()
    page.get_by_test_id("project-name").fill("Payments API")
    page.get_by_test_id("project-repository").fill("https://github.com/acme/payments-api.git")
    page.get_by_test_id("project-create").click()

    # It lands on the new project's settings, with the repository in place.
    expect(page.get_by_test_id("project-settings")).to_be_visible()
    expect(page.get_by_role("cell", name="payments-api", exact=True)).to_be_visible()
    project = client.get("/v1/projects").json()["projects"][0]
    assert (project["name"], project["slug"]) == ("Payments API", "payments-api")
    assert console_errors == []


def test_the_github_connection_is_checked_and_replaced_in_settings(
    page: Page, web_url: str, client: ApiClient, org: dict, forge_project: dict, console_errors: list
):
    _sign_in(page, web_url, org["api_key"])
    page.get_by_test_id("org-settings-button").click()
    expect(page.get_by_test_id("org-settings")).to_contain_text("Connected")
    expect(page.get_by_test_id("org-settings")).to_contain_text("…oken")

    page.get_by_test_id("forge-verify").click()
    expect(page.get_by_test_id("forge-verdict")).to_have_text("Connected as dude-bot")

    # A wrong token is caught here, not by an agent failing to open a PR.
    api_base = client.get("/v1/forge/credential").json()["apiBaseUrl"]
    page.get_by_test_id("forge-connect").click()
    page.get_by_test_id("forge-token").fill("wrong-token")
    page.get_by_label("API base URL").fill(api_base)
    page.get_by_test_id("forge-save").click()
    expect(page.get_by_test_id("org-settings")).to_contain_text("…oken")
    page.get_by_test_id("forge-verify").click()
    expect(page.get_by_test_id("forge-verdict")).to_have_text("GitHub rejected the token")
    assert console_errors == []


def test_epics_are_made_ordered_and_removed_from_the_sidebar_and_board(
    page: Page, web_url: str, client: ApiClient, org: dict, forge_project: dict, console_errors: list
):
    item = client.create_work_item(forge_project["id"], "Loose work")
    _sign_in(page, web_url, org["api_key"])

    # Two epics, from the board. A new one opens, which reveals it in the tree.
    for title in ("Onboarding", "Billing"):
        page.get_by_role("treeitem", name="Greeter").click()
        page.get_by_test_id("new-epic").click()
        page.get_by_test_id("epic-title").fill(title)
        page.get_by_test_id("epic-save").click()
        expect(page.get_by_role("treeitem", name=title)).to_be_visible()

    # Move the loose work item into Billing from its row's menu.
    row = page.get_by_role("treeitem", name="Loose work", exact=False)
    row.focus()
    page.keyboard.press("Shift+F10")
    page.get_by_role("menuitem", name="Move to epic").focus()
    page.keyboard.press("ArrowRight")
    page.get_by_role("menuitem", name="Billing").click()
    page.wait_for_timeout(500)
    epics = client.get(f"/v1/projects/{forge_project['id']}/epics").json()["epics"]
    billing = next(e for e in epics if e["title"] == "Billing")
    assert client.get(f"/v1/work-items/{item['id']}").json()["epicId"] == billing["id"]

    # Billing moves above Onboarding.
    billing_row = page.get_by_role("treeitem", name="Billing")
    billing_row.hover()
    page.get_by_role("button", name="Actions for Billing").click()
    page.get_by_role("menuitem", name="Move up").click()
    page.wait_for_timeout(500)
    assert [e["title"] for e in client.get(f"/v1/projects/{forge_project['id']}/epics").json()["epics"]] == ["Billing", "Onboarding"]

    # The project's board, grouped by epic, shows them in that order.
    page.get_by_role("treeitem", name="Greeter").click()
    page.get_by_test_id("group-by-epic").click()
    expect(page.locator("[data-lane]").first).to_contain_text("Billing")

    # Editing the title keeps the description the tree never showed; the
    # dialog hands focus back to the row it was opened from.
    client.patch(f"/v1/epics/{billing['id']}", {"description": "Invoices and refunds"})
    billing_row.focus()
    page.keyboard.press("Shift+F10")
    page.get_by_role("menuitem", name="Edit epic").click()
    expect(page.get_by_label("Description")).to_have_value("Invoices and refunds")
    page.get_by_test_id("epic-title").fill("Billing and refunds")
    page.get_by_test_id("epic-save").click()
    expect(page.get_by_role("dialog")).to_have_count(0)
    billing_row = page.get_by_role("treeitem", name="Billing and refunds")
    expect(billing_row).to_be_focused()
    saved = next(e for e in client.get(f"/v1/projects/{forge_project['id']}/epics").json()["epics"] if e["id"] == billing["id"])
    assert saved["description"] == "Invoices and refunds"

    # Deleting Billing keeps its work.
    billing_row.focus()
    page.keyboard.press("Shift+F10")
    page.get_by_role("menuitem", name="Delete epic").click()
    expect(page.get_by_role("dialog")).to_contain_text("1 work item will stay in the project")
    page.get_by_test_id("epic-delete").click()
    expect(page.get_by_role("dialog")).to_have_count(0)
    expect(_toast(page, "Billing and refunds deleted")).to_be_visible()
    expect(page.get_by_role("treeitem", name="Billing and refunds")).to_have_count(0)
    assert [e["title"] for e in client.get(f"/v1/projects/{forge_project['id']}/epics").json()["epics"]] == ["Onboarding"]
    assert client.get(f"/v1/work-items/{item['id']}").json()["epicId"] is None
    assert console_errors == []


def test_back_and_forward_move_between_places(
    page: Page, web_url: str, client: ApiClient, forge_project: dict, org: dict, console_errors: list
):
    client.create_work_item(forge_project["id"], "Somewhere to go")
    _sign_in(page, web_url, org["api_key"])
    page.get_by_text("Somewhere to go").last.click()
    expect(page.get_by_test_id("work-item-screen")).to_be_visible()
    page.get_by_test_id("org-settings-button").click()
    expect(page.get_by_test_id("org-settings")).to_be_visible()

    page.go_back()
    expect(page.get_by_test_id("work-item-screen")).to_contain_text("Somewhere to go")
    page.go_back()
    expect(page.get_by_text("Somewhere to go").last).to_be_visible()
    expect(page.get_by_test_id("work-item-screen")).to_have_count(0)
    page.go_forward()
    expect(page.get_by_test_id("work-item-screen")).to_be_visible()

    # A pasted link lands where it points.
    page.goto(f"{web_url}#/org/settings")
    expect(page.get_by_test_id("org-settings")).to_be_visible()
    assert console_errors == []


def test_an_agents_progress_shows_in_its_chat(
    page: Page, web_url: str, client: ApiClient, org: dict, forge_project: dict, console_errors: list
):
    """The implementer reports progress with `dude event progress`; its
    chat shows one progress row that moved, not a line per update."""
    client.patch(f"/v1/projects/{forge_project['id']}", {"agentModels": {
        "implementer": {"model": "fake/tools"}, "reviewer": {"model": "fake/scripted"}, "simplifier": {"model": "fake/scripted"}}})
    item = client.create_work_item(forge_project["id"], "Report progress")
    assert client.post(f"/v1/work-items/{item['id']}/deliver").status_code == 201
    from helpers import wait_until
    implement = wait_until(lambda: next((r for r in client.work_item_runs(item["id"]) if r["phase"] == "implement"), None),
                           timeout=30, message="no implementer")
    wait_until(lambda: [e for e in client.events(runId=implement["id"]) if e["eventType"] == "agent.custom.progress"][1:],
               timeout=30, message="the progress never reached the ledger")

    _sign_in(page, web_url, org["api_key"])
    page.goto(f"{web_url}#/session/{implement['id']}")
    progress = page.get_by_test_id("chat-progress")
    expect(progress).to_have_count(1)
    expect(progress).to_contain_text("2 of 2")
    expect(progress).to_contain_text("committing")
    assert console_errors == []


def test_a_person_approves_a_repository_an_agent_asked_for(
    page: Page, web_url: str, client: ApiClient, org: dict, forge_project: dict, fake_github, console_errors: list
):
    """The implementer asks for another repository; its chat shows the
    request, and approving it brings the repository into the running agent."""
    web = fake_github.add_repository("web")
    client.post(f"/v1/projects/{forge_project['id']}/repositories", {"name": "web", "url": web.clone_url})
    target = next(r for r in client.get(f"/v1/projects/{forge_project['id']}").json()["repositories"] if r["name"] != "web")
    client.patch(f"/v1/projects/{forge_project['id']}", {"agentModels": {
        "implementer": {"model": "fake/request"}, "reviewer": {"model": "fake/scripted"}, "simplifier": {"model": "fake/scripted"}}})
    item = client.create_work_item(forge_project["id"], "Needs the client", repositories=[{"id": target["id"]}])
    assert client.post(f"/v1/work-items/{item['id']}/deliver").status_code == 201
    from helpers import wait_until
    implement = wait_until(lambda: next((r for r in client.work_item_runs(item["id"]) if r["phase"] == "implement"), None),
                           timeout=30, message="no implementer")
    wait_until(lambda: client.get("/v1/repository-requests", params={"runId": implement["id"]}).json()["repositoryRequests"],
               timeout=30, message="the agent never asked")

    _sign_in(page, web_url, org["api_key"])
    page.goto(f"{web_url}#/session/{implement['id']}")
    card = page.get_by_test_id("repository-request")
    expect(card).to_contain_text("Read web")
    expect(card).to_contain_text("the client calls this API")
    card.get_by_role("button", name="Approve").click()

    wait_until(lambda: client.get("/v1/repository-requests", params={"runId": implement["id"]}).json()
               ["repositoryRequests"][0]["status"] == "cloned", timeout=30, message="the repository never reached the run")
    names = sorted(r["id"] for r in client.get(f"/v1/work-items/{item['id']}").json()["repositories"])
    assert len(names) == 2, names
    assert console_errors == []


def test_a_parked_agent_is_answered_from_its_chat(
    page: Page, web_url: str, client: ApiClient, org: dict, forge_project: dict, console_errors: list
):
    """An agent that asked and was not answered in time is parked: its chat
    says so, and answering there resumes it."""
    client.patch(f"/v1/projects/{forge_project['id']}", {"agentModels": {
        "implementer": {"model": "fake/ask"}, "reviewer": {"model": "fake/scripted"}, "simplifier": {"model": "fake/scripted"}}})
    item = client.create_work_item(forge_project["id"], "Ask, then wait")
    assert client.post(f"/v1/work-items/{item['id']}/deliver").status_code == 201
    from helpers import wait_until
    implement = wait_until(
        lambda: next((r for r in client.work_item_runs(item["id"]) if r["phase"] == "implement" and r["status"] == "paused"), None),
        timeout=30, message="the waiting agent was never parked")

    _sign_in(page, web_url, org["api_key"])
    page.goto(f"{web_url}#/session/{implement['id']}")
    expect(page.get_by_test_id("chat-notice")).to_contain_text("Parked while it waits for you")
    # Paused, yet the composer takes the answer: that is what resumes it.
    page.get_by_role("group", name="Answer with one of").get_by_role("button", name="yes").click()
    wait_until(lambda: next(r for r in client.work_item_runs(item["id"]) if r["id"] == implement["id"])["status"] == "completed",
               timeout=30, message="the answer did not resume the parked agent")
    expect(page.get_by_test_id("chat-notice").last).to_contain_text("Taken back up")
    assert console_errors == []


def test_an_agent_parked_on_a_repository_request_says_what_resumes_it(
    page: Page, web_url: str, client: ApiClient, org: dict, forge_project: dict, fake_github, console_errors: list
):
    """An agent that cannot go on without a repository ends its turn on the
    request and is parked; its chat says so, and approving resumes it."""
    web = fake_github.add_repository("web")
    client.post(f"/v1/projects/{forge_project['id']}/repositories", {"name": "web", "url": web.clone_url})
    target = next(r for r in client.get(f"/v1/projects/{forge_project['id']}").json()["repositories"] if r["name"] != "web")
    client.patch(f"/v1/projects/{forge_project['id']}", {"agentModels": {
        "implementer": {"model": "fake/wait"}, "reviewer": {"model": "fake/scripted"}, "simplifier": {"model": "fake/scripted"}}})
    item = client.create_work_item(forge_project["id"], "Needs the client first", repositories=[{"id": target["id"]}])
    assert client.post(f"/v1/work-items/{item['id']}/deliver").status_code == 201
    from helpers import wait_until
    implement = wait_until(
        lambda: next((r for r in client.work_item_runs(item["id"]) if r["phase"] == "implement" and r.get("dudePause") == "person"), None),
        timeout=30, message="the waiting agent was never parked")

    _sign_in(page, web_url, org["api_key"])
    page.goto(f"{web_url}#/session/{implement['id']}")
    expect(page.get_by_test_id("chat-notice")).to_contain_text("Parked while it waits for you")
    # No question to answer: the composer says what does resume it.
    expect(page.get_by_placeholder("decide its request above")).to_be_visible()
    page.get_by_test_id("repository-request").get_by_role("button", name="Approve").click()
    wait_until(lambda: next(r for r in client.work_item_runs(item["id"]) if r["id"] == implement["id"])["status"] == "completed",
               timeout=30, message="the approval did not resume the parked agent")
    assert console_errors == []
