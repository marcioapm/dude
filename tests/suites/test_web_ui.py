"""The web app, driven in a browser, delivering a task to a pull request.

What an operator actually does: open the board, create a task, press
Deliver, and watch the pipeline advance until a pull request exists — then
leave a comment on the forge and watch a fixer answer it. Every step is a
click or a read of the page; nothing reaches around the UI except the forge
side, which is where a person would comment and merge.

Runs against the local stand-in for GitHub, so it needs no network and no
token.
"""

from __future__ import annotations

import re

import pytest
from playwright.sync_api import Page, expect

from fake_github import FakeGitHub
from helpers import ApiClient, create_api_key, sign_in, toast, wait_until

pytestmark = pytest.mark.ui


def test_the_board_shows_projects_and_opens_tasks(
    page: Page, web_url: str, client: ApiClient, forge_project: dict, org: dict, console_errors: list
):
    client.create_task(forge_project["id"], "Already queued up")
    sign_in(page, web_url, org["api_key"])

    # With nothing selected, the first project's board is what opens.
    expect(page.get_by_text("Greeter").first).to_be_visible()
    card = page.get_by_text("Already queued up").last
    expect(card).to_be_visible()

    card.click()
    expect(page.get_by_test_id("task-screen")).to_be_visible()
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
    sign_in(page, web_url, org["api_key"])

    # Create the task from the board, as an operator would.
    page.get_by_test_id("new-task").click()
    page.get_by_test_id("task-title").fill("Greet people by their full name")
    page.get_by_test_id("task-goal").fill("Use the full name, not just the first.")
    page.get_by_label("Criterion 1").fill("Greets with the full name")
    page.get_by_test_id("task-create-deliver").click()
    expect(page.get_by_test_id("task-screen")).to_be_visible()

    pipeline = page.get_by_test_id("pipeline")
    expect(pipeline).to_contain_text("Implement", timeout=60_000)
    # The reviewer raises something, the loop answers it, and a clean
    # re-review lets it through — all visible without a reload.
    expect(pipeline).to_contain_text("blocking", timeout=120_000)
    expect(pipeline).to_contain_text("no findings", timeout=120_000)
    # The fix says what woke it.
    expect(page.get_by_test_id("phase").filter(has_text="Fix")).to_contain_text("for the review")
    tabs = page.get_by_role("tablist", name="Task")
    tabs.get_by_role("tab", name="Findings").click()
    expect(page.get_by_test_id("findings")).to_be_visible()
    expect(page.get_by_test_id("finding").first).to_have_attribute("data-status", "resolved")

    # What the implementer published is there to read, rendered.
    tabs.get_by_role("tab", name="Files").click()
    notes = page.get_by_test_id("file-row").filter(has_text="NOTES.md")
    expect(notes).to_be_visible(timeout=60_000)
    notes.get_by_role("button").first.click()
    expect(page.get_by_test_id("file-viewer").get_by_role("heading", name="What changed")).to_be_visible()
    page.keyboard.press("Escape")

    # The PR appears as the last step, linked to the forge, and its one
    # state is a chip in the header that links there too.
    tabs.get_by_role("tab", name="Overview").click()
    expect(page.get_by_test_id("pr-step")).to_be_visible(timeout=180_000)
    chip = page.get_by_test_id("pr-link")
    expect(chip).to_have_attribute("data-pr-state", "awaiting")
    expect(chip).to_contain_text("Awaiting approval")
    pr_number = int(chip.get_attribute("href").rsplit("/", 1)[-1])
    assert pr_number in fake_github.pulls
    # The same state is on its row in the tree.
    expect(page.get_by_role("tree").locator('[data-pr-state="awaiting"]')).to_have_count(1)

    # Every agent in the pipeline opens its own conversation, on the task's
    # Sessions tab beside the others.
    page.get_by_test_id("phase").nth(1).click()
    expect(tabs.get_by_role("tab", name="Sessions")).to_have_attribute("aria-selected", "true")
    sessions = page.get_by_test_id("sessions")
    expect(sessions.locator('[aria-current="true"]')).to_contain_text("Review")
    expect(page.get_by_test_id("run-screen")).to_contain_text("Reviewer")
    # Back to the overview, and the URL says the task again.
    tabs.get_by_role("tab", name="Overview").click()
    expect(page).to_have_url(re.compile(r"#/task/"))

    # A person comments on the forge; the page shows a fixer answering.
    phases_before = page.get_by_test_id("phase").count()
    fake_github.comment(pr_number, "Please also handle an empty name.")
    expect(page.get_by_test_id("phase")).to_have_count(phases_before + 1, timeout=90_000)
    expect(page.get_by_test_id("phase").last).to_contain_text("Completed", timeout=120_000)

    # Merging on the forge finishes the task.
    fake_github.merge(pr_number)
    expect(page.get_by_test_id("task-header")).to_contain_text("Done", timeout=90_000)

    assert console_errors == []


def test_a_task_is_edited_and_moved_from_its_screen(
    page: Page, web_url: str, client: ApiClient, org: dict, forge_project: dict, console_errors: list
):
    epic = client.post(f"/v1/projects/{forge_project['id']}/epics", {"title": "Greetings"}).json()
    item = client.create_task(forge_project["id"], "Draft title")
    sign_in(page, web_url, org["api_key"])
    page.get_by_text("Draft title").first.click()
    expect(page.get_by_test_id("task-screen")).to_be_visible()

    page.get_by_test_id("edit-task").click()
    page.get_by_test_id("task-title").fill("Greet by full name")
    page.get_by_label("Criterion 1").fill("Uses the full name")
    page.get_by_role("combobox", name="Epic").click()
    page.get_by_role("listbox").get_by_text("Greetings").click()
    page.get_by_test_id("task-save").click()

    screen = page.get_by_test_id("task-screen")
    expect(screen.get_by_role("heading", level=1)).to_have_text("Greet by full name")
    expect(screen.get_by_role("list", name="Acceptance criteria")).to_contain_text("Uses the full name")
    saved = client.get(f"/v1/tasks/{item['id']}").json()
    assert saved["epicId"] == epic["id"]
    assert console_errors == []


def test_a_task_names_the_repositories_it_changes_and_reads(
    page: Page, web_url: str, client: ApiClient, org: dict, forge_project: dict, console_errors: list
):
    target = forge_project["repositories"][0]
    docs = client.post(f"/v1/projects/{forge_project['id']}/repositories",
                       {"name": "docs", "url": "https://github.com/acme/docs.git"}).json()
    sign_in(page, web_url, org["api_key"])
    page.get_by_test_id("new-task").click()
    page.get_by_test_id("task-title").fill("Document the greeting")
    chooser = page.get_by_test_id("task-repositories")
    # Nothing chosen says what that means.
    expect(chooser).to_contain_text("changes no code")
    chooser.get_by_role("checkbox", name=target["name"]).click()
    chooser.get_by_role("checkbox", name="docs").click()
    chooser.get_by_role("combobox", name="What the work does in docs").click()
    page.get_by_role("listbox").get_by_text("Reads it").click()
    expect(chooser).to_contain_text("own pull request")
    page.get_by_test_id("task-save").click()
    expect(page.get_by_test_id("task-screen")).to_be_visible()

    items = client.get("/v1/tasks", params={"projectId": forge_project["id"]}).json()["tasks"]
    saved = next(i for i in items if i["title"] == "Document the greeting")
    assert sorted((r["id"], r["access"]) for r in saved["repositories"]) == sorted(
        [(target["id"], "write"), (docs["id"], "read")])
    assert console_errors == []


def test_project_settings_manage_repositories_and_delivery(
    page: Page, web_url: str, client: ApiClient, org: dict, forge_project: dict, console_errors: list
):
    sign_in(page, web_url, org["api_key"])
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
    page.locator("[data-settings-nav='delivery']").click()
    page.get_by_role("checkbox", name="security").click()
    page.get_by_test_id("delivery-save").click()
    expect(toast(page, "Delivery saved")).to_be_visible()

    project = client.get(f"/v1/projects/{forge_project['id']}").json()
    assert "docs" in [r["name"] for r in project["repositories"]]
    assert "bad" not in [r["name"] for r in project["repositories"]]
    # Only what differs from the factory's defaults is stored: the rest keeps
    # following them.
    assert project["deliveryPolicy"] == {"requiredReviewers": ["correctness", "security"]}
    assert console_errors == []

    # Its face: an image picked in General, resized in the browser, stored,
    # and shown wherever the project is, the sidebar included.
    page.locator("[data-settings-nav='general']").click()
    png = bytes.fromhex("89504e470d0a1a0a0000000d49484452000000010000000108060000001f15c489"
                        "0000000d49444154789c63f8cfc0f01f00050001ff89993d1d0000000049454e44ae426082")
    page.get_by_test_id("project-image-file").set_input_files({"name": "logo.png", "mimeType": "image/png", "buffer": png})
    expect(toast(page, "Image updated")).to_be_visible()
    expect(page.get_by_test_id("project-face").locator("img")).to_have_count(1)
    expect(page.locator(f"[data-nav-key='project:{forge_project['id']}'] img")).to_have_count(1)
    assert client.get(f"/v1/projects/{forge_project['id']}").json()["imageUrl"].startswith("/v1/projects/")
    assert console_errors == []


def test_a_new_project_starts_from_the_empty_screen(page: Page, web_url: str, client: ApiClient, org: dict, console_errors: list):
    """An organization with nothing yet is offered a project, not told to use the API."""
    sign_in(page, web_url, org["api_key"])
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
    sign_in(page, web_url, org["api_key"])
    page.get_by_test_id("org-settings-button").click()
    # Settings open on Members; GitHub is the next page.
    page.locator('[data-settings-nav="github"]').click()
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
    item = client.create_task(forge_project["id"], "Loose work")
    sign_in(page, web_url, org["api_key"])

    # Two epics, from the board. A new one opens, which reveals it in the tree.
    for title in ("Onboarding", "Billing"):
        page.get_by_role("treeitem", name="Greeter").click()
        page.get_by_test_id("new-epic").click()
        page.get_by_test_id("epic-title").fill(title)
        page.get_by_test_id("epic-save").click()
        expect(page.get_by_role("treeitem", name=title)).to_be_visible()

    # Move the loose task into Billing from its row's menu.
    row = page.get_by_role("treeitem", name="Loose work", exact=False)
    row.focus()
    page.keyboard.press("Shift+F10")
    page.get_by_role("menuitem", name="Move to epic").focus()
    page.keyboard.press("ArrowRight")
    page.get_by_role("menuitem", name="Billing").click()
    page.wait_for_timeout(500)
    epics = client.get(f"/v1/projects/{forge_project['id']}/epics").json()["epics"]
    billing = next(e for e in epics if e["title"] == "Billing")
    assert client.get(f"/v1/tasks/{item['id']}").json()["epicId"] == billing["id"]

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
    expect(page.get_by_role("dialog")).to_contain_text("1 task will stay in the project")
    page.get_by_test_id("epic-delete").click()
    expect(page.get_by_role("dialog")).to_have_count(0)
    expect(toast(page, "Billing and refunds deleted")).to_be_visible()
    expect(page.get_by_role("treeitem", name="Billing and refunds")).to_have_count(0)
    assert [e["title"] for e in client.get(f"/v1/projects/{forge_project['id']}/epics").json()["epics"]] == ["Onboarding"]
    assert client.get(f"/v1/tasks/{item['id']}").json()["epicId"] is None
    assert console_errors == []


def test_back_and_forward_move_between_places(
    page: Page, web_url: str, client: ApiClient, forge_project: dict, org: dict, console_errors: list
):
    client.create_task(forge_project["id"], "Somewhere to go")
    sign_in(page, web_url, org["api_key"])
    page.get_by_text("Somewhere to go").last.click()
    expect(page.get_by_test_id("task-screen")).to_be_visible()
    page.get_by_test_id("org-settings-button").click()
    expect(page.get_by_test_id("org-settings")).to_be_visible()

    page.go_back()
    expect(page.get_by_test_id("task-screen")).to_contain_text("Somewhere to go")
    page.go_back()
    expect(page.get_by_text("Somewhere to go").last).to_be_visible()
    expect(page.get_by_test_id("task-screen")).to_have_count(0)
    page.go_forward()
    expect(page.get_by_test_id("task-screen")).to_be_visible()

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
    item = client.create_task(forge_project["id"], "Report progress")
    assert client.post(f"/v1/tasks/{item['id']}/deliver").status_code == 201
    implement = wait_until(lambda: next((r for r in client.task_runs(item["id"]) if r["phase"] == "implement"), None),
                           timeout=30, message="no implementer")
    wait_until(lambda: [e for e in client.events(runId=implement["id"]) if e["eventType"] == "agent.custom.progress"][1:],
               timeout=30, message="the progress never reached the ledger")

    sign_in(page, web_url, org["api_key"])
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
    item = client.create_task(forge_project["id"], "Needs the client", repositories=[{"id": target["id"]}])
    assert client.post(f"/v1/tasks/{item['id']}/deliver").status_code == 201
    implement = wait_until(lambda: next((r for r in client.task_runs(item["id"]) if r["phase"] == "implement"), None),
                           timeout=30, message="no implementer")
    wait_until(lambda: client.get("/v1/repository-requests", params={"runId": implement["id"]}).json()["repositoryRequests"],
               timeout=30, message="the agent never asked")

    sign_in(page, web_url, org["api_key"])
    page.goto(f"{web_url}#/session/{implement['id']}")
    card = page.get_by_test_id("repository-request")
    expect(card).to_contain_text("Read web")
    expect(card).to_contain_text("the client calls this API")
    card.get_by_role("button", name="Approve").click()

    wait_until(lambda: client.get("/v1/repository-requests", params={"runId": implement["id"]}).json()
               ["repositoryRequests"][0]["status"] == "cloned", timeout=30, message="the repository never reached the run")
    names = sorted(r["id"] for r in client.get(f"/v1/tasks/{item['id']}").json()["repositories"])
    assert len(names) == 2, names
    assert console_errors == []


def test_a_parked_agent_is_answered_from_its_chat(
    page: Page, web_url: str, client: ApiClient, org: dict, forge_project: dict, console_errors: list
):
    """An agent that asked and was not answered in time is parked: its chat
    says so, and answering there resumes it."""
    client.patch(f"/v1/projects/{forge_project['id']}", {"agentModels": {
        "implementer": {"model": "fake/ask"}, "reviewer": {"model": "fake/scripted"}, "simplifier": {"model": "fake/scripted"}}})
    item = client.create_task(forge_project["id"], "Ask, then wait")
    assert client.post(f"/v1/tasks/{item['id']}/deliver").status_code == 201
    implement = wait_until(
        lambda: next((r for r in client.task_runs(item["id"]) if r["phase"] == "implement" and r["status"] == "paused"), None),
        timeout=30, message="the waiting agent was never parked")

    sign_in(page, web_url, org["api_key"])
    page.goto(f"{web_url}#/session/{implement['id']}")
    expect(page.get_by_test_id("chat-notice")).to_contain_text("Parked while it waits for you")
    # Paused, yet the composer takes the answer: that is what resumes it.
    page.get_by_role("group", name="Answer with one of").get_by_role("button", name="yes").click()
    wait_until(lambda: client.get_run(implement["id"])["status"] == "completed",
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
    item = client.create_task(forge_project["id"], "Needs the client first", repositories=[{"id": target["id"]}])
    assert client.post(f"/v1/tasks/{item['id']}/deliver").status_code == 201
    implement = wait_until(
        lambda: next((r for r in client.task_runs(item["id"]) if r["phase"] == "implement" and r.get("dudePause") == "person"), None),
        timeout=30, message="the waiting agent was never parked")

    sign_in(page, web_url, org["api_key"])
    page.goto(f"{web_url}#/session/{implement['id']}")
    expect(page.get_by_test_id("chat-notice")).to_contain_text("Parked while it waits for you")
    # No question to answer: the composer says what does resume it.
    expect(page.get_by_placeholder("decide its request above")).to_be_visible()
    page.get_by_test_id("repository-request").get_by_role("button", name="Approve").click()
    wait_until(lambda: client.get_run(implement["id"])["status"] == "completed",
               timeout=30, message="the approval did not resume the parked agent")
    assert console_errors == []


def test_theme_and_density_are_this_browsers_and_remembered(page: Page, web_url: str, org: dict, console_errors: list):
    """You choose how dude looks; it stays so after a reload."""
    sign_in(page, web_url, org["api_key"])
    page.get_by_test_id("my-settings-button").click()
    expect(page.get_by_test_id("my-settings")).to_be_visible()
    html = page.locator("html")
    expect(html).to_have_attribute("data-density", "comfortable")

    page.get_by_role("combobox", name="Density").click()
    page.get_by_role("option", name="Compact — more on screen").click()
    expect(html).to_have_attribute("data-density", "compact")
    page.get_by_role("combobox", name="Theme").click()
    page.get_by_role("option", name="Light").click()
    expect(html).to_have_attribute("data-theme", "light")

    page.reload()
    expect(page.get_by_test_id("my-settings")).to_be_visible()
    expect(html).to_have_attribute("data-density", "compact")
    expect(html).to_have_attribute("data-theme", "light")
    assert console_errors == []


def test_a_browser_turns_notifications_on_and_off(
    page: Page, web_url: str, client: ApiClient, org: dict, owner_dsn: str, console_errors: list
):
    """Notify me: the browser is asked, subscribes with dude's key, and dude
    keeps the subscription; turning off forgets it. (Headless Chromium has no
    push service to subscribe with, so that part is stood in for; sending to
    a subscription is tested in the orchestrator.)"""
    page.context.grant_permissions(["notifications"], origin=web_url.rstrip("/"))
    # The real service worker registers and runs; only the push service
    # headless Chromium lacks is stood in for.
    page.add_init_script("""
      const endpoint = "https://push.example/" + Math.random().toString(36).slice(2);
      let sub = null;
      const fake = () => ({ endpoint, toJSON: () => ({ endpoint, keys: { p256dh: "BPk", auth: "au" } }),
                            unsubscribe: async () => { sub = null; return true; } });
      PushManager.prototype.getSubscription = async function () { return sub; };
      PushManager.prototype.subscribe = async function (o) { window.__pushKey = o.applicationServerKey; sub = fake(); return sub; };
    """)
    sign_in(page, web_url, org["api_key"])
    page.get_by_test_id("my-settings-button").click()
    state = page.get_by_test_id("push-state")
    expect(state).to_have_attribute("data-state", "off")

    page.get_by_test_id("push-on").click()
    expect(state).to_have_attribute("data-state", "on")
    # Subscribed with dude's public key (an uncompressed P-256 point), and
    # kept for the organization, where the notifier sends.
    assert page.evaluate("() => window.__pushKey.length") == 65
    import psycopg

    def subscriptions() -> int:
        with psycopg.connect(owner_dsn) as conn:
            return conn.execute("SELECT count(*) FROM push_subscriptions WHERE organization_id = %s",
                                (org["id"],)).fetchone()[0]
    assert subscriptions() == 1

    page.get_by_test_id("push-off").click()
    expect(state).to_have_attribute("data-state", "off")
    assert subscriptions() == 0
    # The real service worker is what the page registered.
    assert page.evaluate("async () => (await navigator.serviceWorker.getRegistration('/'))?.active?.scriptURL").endswith("/sw.js")
    assert console_errors == []


def test_everything_waiting_on_you_is_in_one_place(
    page: Page, web_url: str, client: ApiClient, org: dict, forge_project: dict, fake_github, console_errors: list
):
    """A question and a repository request, from two agents: both in "Waiting
    on you", oldest first, each opening the agent that asked."""
    web = fake_github.add_repository("web")
    client.post(f"/v1/projects/{forge_project['id']}/repositories", {"name": "web", "url": web.clone_url})
    target = next(r for r in client.get(f"/v1/projects/{forge_project['id']}").json()["repositories"] if r["name"] != "web")
    client.patch(f"/v1/projects/{forge_project['id']}", {"agentModels": {
        "implementer": {"model": "fake/ask"}, "reviewer": {"model": "fake/scripted"}, "simplifier": {"model": "fake/scripted"}}})
    asking = client.create_task(forge_project["id"], "Ask first", repositories=[{"id": target["id"]}])
    client.post(f"/v1/tasks/{asking['id']}/deliver")
    wait_until(lambda: client.get("/v1/questions").json()["questions"], timeout=30, message="no question")
    client.patch(f"/v1/projects/{forge_project['id']}", {"agentModels": {"implementer": {"model": "fake/wait"}}})
    requesting = client.create_task(forge_project["id"], "Needs the client", repositories=[{"id": target["id"]}])
    client.post(f"/v1/tasks/{requesting['id']}/deliver")
    wait_until(lambda: client.get("/v1/repository-requests").json()["repositoryRequests"], timeout=30, message="no request")

    sign_in(page, web_url, org["api_key"])
    page.goto(f"{web_url}#/waiting")
    inbox = page.get_by_test_id("inbox")
    rows = inbox.get_by_role("listitem")
    expect(rows).to_have_count(2)
    expect(rows.nth(0)).to_contain_text("Ask first")
    expect(rows.nth(0)).to_contain_text("Should FACTORY.md be in English?")
    expect(rows.nth(1)).to_contain_text("Needs the client")
    expect(rows.nth(1)).to_contain_text("Read web?")
    # Both are yours (you own them): highlighted, each with its one action.
    expect(inbox.locator("[data-mine]")).to_have_count(2)
    rows.nth(1).get_by_role("button", name="Answer").click()
    expect(page.get_by_test_id("repository-request")).to_contain_text("Read web")
    assert console_errors == []


def test_only_a_tasks_owner_answers_and_anyone_can_take_it_over(
    page: Page, web_url: str, env, client: ApiClient, org: dict, forge_project: dict, console_errors: list
):
    """Whoever creates a task drives it: a colleague sees its agent's
    question, but not the choices — they wait on the owner. Taking the task
    over from its page makes the question theirs to answer."""
    client.patch(f"/v1/projects/{forge_project['id']}", {"agentModels": {
        "implementer": {"model": "fake/ask"}, "reviewer": {"model": "fake/scripted"}, "simplifier": {"model": "fake/scripted"}}})
    item = client.create_task(forge_project["id"], "Ask the owner")
    assert item["owner"]["name"] == "e2e user"
    assert client.post(f"/v1/tasks/{item['id']}/deliver").status_code == 201
    question = wait_until(lambda: next(iter(client.get("/v1/questions", params={"taskId": item["id"]}).json()["questions"]), None),
                          timeout=30, message="the agent never asked")
    bo_key = create_api_key(env.owner_dsn, org["id"], name="Bo")
    bo = ApiClient(env.control_plane_url, bo_key)
    people = bo.get("/v1/people").json()["people"]
    assert sorted(p["name"] for p in people) == ["Bo", "e2e user"]

    # Not Bo's to answer, and the refusal says whose it is.
    refused = bo.post(f"/v1/questions/{question['id']}/answer", {"text": "yes"})
    assert refused.status_code == 403, refused.text
    assert refused.json()["error"]["code"] == "not_owner"
    assert "only e2e user can answer this" in refused.json()["error"]["message"]

    sign_in(page, web_url, bo_key)
    page.goto(f"{web_url}#/session/{question['runId']}")
    expect(page.get_by_test_id("waiting-on")).to_have_text("Waiting for e2e user to answer")
    expect(page.get_by_role("group", name="Answer with one of")).to_have_count(0)
    expect(page.get_by_placeholder("Waiting for e2e user to answer.")).to_be_disabled()

    # Bo takes it over from the task's page.
    page.goto(f"{web_url}#/task/{item['id']}")
    owner = page.get_by_test_id("task-owner")
    expect(owner).to_have_attribute("data-owner", "e2e user")
    owner.get_by_role("combobox", name="Owner").click()
    page.get_by_role("listbox").get_by_text("Bo").click()
    expect(owner).to_have_attribute("data-owner", "Bo")
    assert client.get(f"/v1/tasks/{item['id']}").json()["owner"]["name"] == "Bo"
    changed = [e for e in client.events(taskId=item["id"]) if e["eventType"] == "task.owner_changed"]
    assert len(changed) == 1 and changed[0]["payload"]["to"] != changed[0]["payload"]["from"]
    # The board and sidebar name its owner too.
    tasks = [t for p in client.get("/v1/navigation").json()["projects"] for t in p["tasks"]]
    assert next(t for t in tasks if t["id"] == item["id"])["people"][0]["name"] == "Bo"

    # Now it is Bo's to answer, from the chat.
    page.goto(f"{web_url}#/session/{question['runId']}")
    expect(page.get_by_test_id("waiting-on")).to_have_count(0)
    page.get_by_role("group", name="Answer with one of").get_by_role("button", name="yes").click()
    wait_until(lambda: client.get("/v1/questions", params={"taskId": item["id"]}).json()["questions"][0]["status"] == "answered",
               timeout=30, message="Bo's answer was not taken")
    assert console_errors == []


def test_a_tasks_time_and_cost_show_on_its_page_and_its_epics(
    page: Page, web_url: str, client: ApiClient, org: dict, forge_project: dict, console_errors: list
):
    """A delivered task's page says how long its agents worked and waited
    and what it cost, run by run; its epic's board totals them."""
    epic = client.post(f"/v1/projects/{forge_project['id']}/epics", {"title": "Metrics"}).json()
    task = client.create_task(forge_project["id"], "Measure me", epicId=epic["id"])
    client.post(f"/v1/tasks/{task['id']}/deliver")
    wait_until(lambda: any(r["phase"] == "review" and r["status"] == "completed" for r in client.task_runs(task["id"])),
               timeout=60, message="no finished review")

    sign_in(page, web_url, org["api_key"])
    page.goto(f"{web_url}#/task/{task['id']}")
    metrics = page.get_by_test_id("task-metrics")
    expect(metrics).to_contain_text("Agents working")
    expect(metrics).to_contain_text("Cost")
    expect(page.get_by_test_id("run-metrics").get_by_role("row").filter(has_text="Implement")).to_have_count(1)

    page.goto(f"{web_url}#/epic/{epic['id']}")
    expect(page.get_by_test_id("epic-metrics")).to_contain_text("of 1 task")
    assert console_errors == []


def test_a_project_can_have_its_changes_tested_in_a_browser(
    page: Page, web_url: str, client: ApiClient, org: dict, forge_project: dict, console_errors: list
):
    """The tester phase is a project's choice, off by default."""
    sign_in(page, web_url, org["api_key"])
    page.goto(f"{web_url}#/project/{forge_project['id']}/settings")
    page.locator("[data-settings-nav='delivery']").click()
    box = page.get_by_test_id("delivery-test").get_by_role("switch")
    expect(box).to_have_attribute("aria-checked", "false")
    box.click()
    page.get_by_test_id("delivery-save").click()
    expect(toast(page, "Delivery saved")).to_be_visible()
    assert client.get(f"/v1/projects/{forge_project['id']}").json()["deliveryPolicy"]["test"] is True
    assert console_errors == []


def test_a_failed_implementer_says_why_on_its_task_and_its_chat(
    page: Page, web_url: str, client: ApiClient, org: dict, forge_project: dict, console_errors: list
):
    """With no model for the implementer its Run fails, and delivery stops for
    a person. The task says why at the top and on the failed step, "Waiting on
    you" gives the reason, and the Run's chat ends on the error rather than a
    composer nobody would hear."""
    client.patch(f"/v1/projects/{forge_project['id']}", {"agentModels": {"reviewer": {"model": "fake/scripted"}}})
    item = client.create_task(forge_project["id"], "Nobody to implement it")
    assert client.post(f"/v1/tasks/{item['id']}/deliver").status_code == 201
    wait_until(lambda: client.get(f"/v1/tasks/{item['id']}").json()["status"] == "awaiting_input",
               timeout=60, message="delivery never stopped for a person")
    task = client.get(f"/v1/tasks/{item['id']}").json()
    assert task["escalation"]["reason"] == "implement_failed", task["escalation"]
    implement = next(r for r in task["runs"] if r["phase"] == "implement")

    sign_in(page, web_url, org["api_key"])
    page.goto(f"{web_url}#/task/{item['id']}")
    escalation = page.get_by_test_id("escalation")
    expect(escalation).to_contain_text("Implementer failed")
    expect(escalation).to_contain_text("no model is configured for the implementer role")
    expect(page.get_by_test_id("phase").first).to_contain_text("no model is configured")
    expect(page).to_have_title("GREE-1 · Nobody to implement it — dude")

    page.goto(f"{web_url}#/waiting")
    expect(page.get_by_test_id("inbox").get_by_role("listitem").first).to_contain_text("Implementer failed")

    page.goto(f"{web_url}#/task/{item['id']}")
    escalation.get_by_test_id("escalation-run").click()
    ended = page.get_by_test_id("run-ended")
    expect(ended).to_have_attribute("data-outcome", "failed")
    # Why is the transcript's last line, just above; the strip says only how it ended.
    expect(page.get_by_test_id("chat-ended")).to_contain_text("no model is configured")
    # No composer: a finished run hears nothing. It is on its task's page,
    # so there is no way back to offer: the task is right there.
    expect(page.get_by_test_id("run-screen").locator("textarea")).to_have_count(0)
    expect(ended.get_by_test_id("run-ended-task")).to_have_count(0)
    expect(page.get_by_test_id("task-screen")).to_be_visible()
    assert implement["status"] == "failed"
    assert console_errors == []


def test_a_person_decides_how_a_stopped_delivery_goes_on(
    page: Page, web_url: str, client: ApiClient, org: dict, forge_project: dict, console_errors: list
):
    """Delivery stopped for a person offers what fits: here, a failed
    implementer — try again, or stop. A note goes with the decision; trying
    again runs the step afresh, and with its model back it goes on."""
    client.patch(f"/v1/projects/{forge_project['id']}", {"agentModels": {"reviewer": {"model": "fake/scripted"}}})
    item = client.create_task(forge_project["id"], "Implement it once it can")
    assert client.post(f"/v1/tasks/{item['id']}/deliver").status_code == 201
    wait_until(lambda: client.get(f"/v1/tasks/{item['id']}").json()["status"] == "awaiting_input",
               timeout=60, message="delivery never stopped for a person")
    assert client.get(f"/v1/tasks/{item['id']}").json()["escalation"]["actions"] == ["retry", "stop"]

    sign_in(page, web_url, org["api_key"])
    page.goto(f"{web_url}#/task/{item['id']}")
    escalation = page.get_by_test_id("escalation")
    expect(escalation.get_by_test_id("escalation-retry")).to_have_text("Try again")
    expect(escalation.get_by_test_id("escalation-stop")).to_be_visible()
    expect(escalation.get_by_test_id("escalation-accept")).to_have_count(0)

    # Its model back, and a word for the agent.
    client.patch(f"/v1/projects/{forge_project['id']}",
                 {"agentModels": {"implementer": {"model": "fake/scripted"}, "reviewer": {"model": "fake/scripted"}}})
    escalation.get_by_test_id("escalation-note").fill("The model is configured now.")
    escalation.get_by_test_id("escalation-retry").click()
    expect(page.get_by_test_id("escalation")).to_have_count(0)
    wait_until(lambda: len([r for r in client.get(f"/v1/tasks/{item['id']}").json()["runs"] if r["phase"] == "implement"]) == 2,
               timeout=60, message="trying again made no second implementer")
    decided = [e for e in client.events(taskId=item["id"]) if e["eventType"] == "task.decided"]
    assert decided and decided[0]["payload"] == {"reason": "implement_failed", "action": "retry",
                                                 "note": "The model is configured now."}, decided
    assert console_errors == []


def test_a_refused_key_asks_for_another(page: Page, web_url: str, org: dict, console_errors: list):
    """A key the server does not take (mistyped, revoked) goes back to the key
    prompt, saying so — not an error above a spinner that never ends."""
    page.goto(web_url)
    page.evaluate("localStorage.clear()")
    page.goto(web_url)
    page.fill('input[type="password"]', "dude_sk_not-a-key")
    page.click('button[type="submit"]')
    expect(page.get_by_text("That key was not accepted")).to_be_visible()
    assert page.evaluate("localStorage.getItem('dude.apiKey')") is None

    # The right one lets you in.
    page.fill('input[type="password"]', org["api_key"])
    page.click('button[type="submit"]')
    expect(page.get_by_test_id("shell")).to_be_visible()
    assert all("401" in e for e in console_errors), console_errors


def test_a_link_to_something_gone_says_so(
    page: Page, web_url: str, client: ApiClient, org: dict, forge_project: dict, console_errors: list
):
    """A task, run or epic that does not exist is said to, with a way back."""
    sign_in(page, web_url, org["api_key"])
    for kind in ("task", "run", "epic"):
        page.goto(f"{web_url}#/{kind}/{kind}_doesnotexist")
        expect(page.get_by_test_id("not-found")).to_contain_text(f"This {kind} doesn't exist")
    page.get_by_test_id("not-found-back").click()
    expect(page.get_by_test_id("new-task")).to_be_visible()
    # The server's 404s for the task and the run are the only errors.
    assert all("404" in e for e in console_errors), console_errors


def _hanging_run(client: ApiClient, forge_project: dict, title: str) -> dict:
    """A task whose implementer is working and never finishes its turn."""
    client.patch(f"/v1/projects/{forge_project['id']}", {"agentModels": {
        "implementer": {"model": "fake/hang"}, "reviewer": {"model": "fake/scripted"}, "simplifier": {"model": "fake/scripted"}}})
    item = client.create_task(forge_project["id"], title)
    assert client.post(f"/v1/tasks/{item['id']}/deliver").status_code == 201
    return wait_until(lambda: next((r for r in client.task_runs(item["id"]) if r["phase"] == "implement" and r["status"] == "running"), None),
                      timeout=60, message="the implementer never started")


def test_a_steer_is_sent_with_enter_and_signed_with_a_name(
    page: Page, web_url: str, env, client: ApiClient, org: dict, forge_project: dict, console_errors: list
):
    """Enter sends a steer (Shift+Enter is a new line); the chat and the
    event log say who sent it, by name, not "Human"."""
    run = _hanging_run(client, forge_project, "Steer me")
    sign_in(page, web_url, org["api_key"])
    page.goto(f"{web_url}#/session/{run['id']}")
    field = page.get_by_placeholder("Steer the agent…")
    field.fill("first line")
    field.press("Shift+Enter")
    field.type("second line")
    expect(field).to_have_value("first line\nsecond line")
    expect(page.get_by_text("Sent as e2e")).to_be_visible()
    field.press("Enter")
    turn = page.get_by_test_id("human-turn").last
    expect(turn).to_contain_text("e2e user")
    expect(turn).to_contain_text("second line")
    expect(page.get_by_test_id("human-turn")).not_to_contain_text("Human")
    steered = wait_until(lambda: [e for e in client.events(runId=run["id"]) if e["eventType"] == "run.steered"],
                         timeout=15, message="no steer in the ledger")
    assert steered[0]["payload"]["text"] == "first line\nsecond line" and steered[0]["payload"]["interrupt"] is False

    # Someone else's steer is signed with their name.
    bo = ApiClient(env.control_plane_url, create_api_key(env.owner_dsn, org["id"], name="Bo"))
    assert bo.post(f"/v1/runs/{run['id']}/steer", {"text": "from Bo"}).status_code == 201
    page.reload()
    expect(page.get_by_test_id("human-turn").filter(has_text="from Bo")).to_contain_text("Bo")
    assert console_errors == []


def test_abort_asks_first_and_offers_to_pause_instead(
    page: Page, web_url: str, client: ApiClient, org: dict, forge_project: dict, console_errors: list
):
    """Abort is a request, confirmed in a dialog whose one solid button
    aborts; "Pause instead" pauses. The chat then says who stopped it."""
    run = _hanging_run(client, forge_project, "Stop me")
    sign_in(page, web_url, org["api_key"])
    page.goto(f"{web_url}#/session/{run['id']}")

    page.get_by_test_id("abort").click()
    dialog = page.get_by_role("dialog")
    expect(dialog).to_contain_text("cannot be resumed")
    dialog.get_by_test_id("abort-pause-instead").click()
    expect(dialog).to_have_count(0)
    wait_until(lambda: client.get_run(run["id"])["status"] == "paused", timeout=30, message="Pause instead did not pause")
    assert client.get_run(run["id"])["status"] != "aborted"

    page.get_by_test_id("abort").click()
    page.get_by_role("dialog").get_by_label("Why (optional)").fill("wrong task")
    page.get_by_role("dialog").get_by_test_id("abort-confirm").click()
    wait_until(lambda: client.get_run(run["id"])["status"] == "aborted", timeout=30, message="the confirmed abort did not abort")
    expect(page.get_by_test_id("chat-ended")).to_contain_text("Aborted by e2e user: wrong task")
    assert console_errors == []


def test_acting_second_is_a_calm_notice_naming_who_acted_first(
    page: Page, web_url: str, env, client: ApiClient, org: dict, forge_project: dict, console_errors: list
):
    """Two people on one agent: Bo pauses it while you look; your Pause is
    refused (409), and the page says Bo did it — not an error — until the
    page catches up."""
    run = _hanging_run(client, forge_project, "Two hands")
    sign_in(page, web_url, org["api_key"])
    page.goto(f"{web_url}#/session/{run['id']}")
    expect(page.get_by_role("button", name="Pause")).to_be_visible()
    # Bo acts first; the page is told nothing yet: its live stream is cut,
    # while its own requests still go through.
    page.route("**/v1/events/stream**", lambda route: route.abort())
    page.evaluate("window.dispatchEvent(new Event('offline'))")
    bo = ApiClient(env.control_plane_url, create_api_key(env.owner_dsn, org["id"], name="Bo"))
    assert bo.post(f"/v1/runs/{run['id']}/pause").status_code in (200, 201, 202)
    wait_until(lambda: client.get_run(run["id"])["status"] == "paused", timeout=30, message="Bo's pause did not land")
    page.get_by_role("button", name="Pause").click()
    notice = page.get_by_test_id("conflict-notice")
    expect(notice).to_contain_text("Bo paused it first")
    expect(page.get_by_role("alert")).to_have_count(0)
    # The refusal is the only error the page saw.
    assert all("409" in e or "ERR_FAILED" in e for e in console_errors), console_errors


def test_a_dropped_stream_says_so_and_catches_up(
    page: Page, web_url: str, client: ApiClient, org: dict, forge_project: dict, console_errors: list
):
    """The live stream drops: the page says it is reconnecting, and when it
    is back it re-reads what it missed."""
    client.create_task(forge_project["id"], "Before")
    sign_in(page, web_url, org["api_key"])
    expect(page.get_by_text("Before").first).to_be_visible()
    page.context.set_offline(True)
    expect(page.get_by_test_id("reconnecting")).to_be_visible(timeout=20_000)
    # One banner, from the shell, with a way out if it never comes back.
    expect(page.get_by_test_id("reconnecting")).to_have_count(1)
    expect(page.get_by_test_id("reconnecting-reload")).to_be_visible()
    client.create_task(forge_project["id"], "While away")
    page.context.set_offline(False)
    expect(page.get_by_test_id("reconnecting")).to_have_count(0, timeout=30_000)
    expect(page.get_by_text("While away").first).to_be_visible(timeout=15_000)
    assert all("ERR_INTERNET_DISCONNECTED" in e for e in console_errors), console_errors
