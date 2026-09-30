"""Settings in two layers, prompts with a history, and epics with a state.

The organization sets the defaults every project starts from; a project
stores only what it overrides, and its settings say where each value comes
from. Prompts keep every save, and a Run records the version it ran with.
Driven through the public API, as the settings screens use it.
"""

from __future__ import annotations

import os

from helpers import ApiClient, execute, query, wait_until


def test_an_organizations_default_is_overridden_by_a_project_and_reset(client: ApiClient, project: dict):
    org = client.patch("/v1/settings/organization", {"roles": {"reviewer": {"model": "llm-anthropic/org-review", "effort": "high"}}})
    assert org.status_code == 200, org.text
    assert org.json()["roles"]["reviewer"]["model"] == {"value": "llm-anthropic/org-review", "source": "organization"}

    # The project follows it until it says otherwise.
    settings = client.get(f"/v1/projects/{project['id']}/settings").json()
    assert settings["roles"]["reviewer"]["model"] == {"value": "llm-anthropic/org-review", "source": "organization"}
    assert settings["roles"]["reviewer"]["effort"] == {"value": "high", "source": "organization"}

    # An override of one field leaves the others inherited.
    changed = client.patch(f"/v1/projects/{project['id']}/settings", {"roles": {"reviewer": {"effort": "low"}}}).json()
    assert changed["roles"]["reviewer"]["effort"] == {"value": "low", "source": "project"}
    assert changed["roles"]["reviewer"]["model"] == {"value": "llm-anthropic/org-review", "source": "organization"}
    # Stored as an override only.
    assert client.get(f"/v1/projects/{project['id']}").json()["agentModels"]["reviewer"] == {"effort": "low"}

    # The organization's later change still reaches what the project did not override.
    client.patch("/v1/settings/organization", {"roles": {"reviewer": {"model": "llm-anthropic/org-review-2"}}})
    settings = client.get(f"/v1/projects/{project['id']}/settings").json()
    assert settings["roles"]["reviewer"]["model"]["value"] == "llm-anthropic/org-review-2"

    # Reset is a delete: the value is the organization's again, and the
    # project stores nothing for the role.
    reset = client.patch(f"/v1/projects/{project['id']}/settings", {"roles": {"reviewer": {"effort": None}}}).json()
    assert reset["roles"]["reviewer"]["effort"] == {"value": "high", "source": "organization"}
    assert "reviewer" not in client.get(f"/v1/projects/{project['id']}").json()["agentModels"]


def test_the_fixer_follows_the_implementer_without_calling_it_its_own(client: ApiClient, project: dict):
    # The project overrides the implementer's model; the fixer runs it too,
    # but has nothing of its own to reset.
    settings = client.patch(f"/v1/projects/{project['id']}/settings", {"roles": {"implementer": {"model": "llm-openai/proj-impl"}}}).json()
    assert settings["roles"]["implementer"]["model"] == {"value": "llm-openai/proj-impl", "source": "project"}
    assert settings["roles"]["fixer"]["model"] == {"value": "llm-openai/proj-impl", "source": "organization"}

    settings = client.patch(f"/v1/projects/{project['id']}/settings", {"roles": {"fixer": {"model": "llm-openai/proj-fix"}}}).json()
    assert settings["roles"]["fixer"]["model"] == {"value": "llm-openai/proj-fix", "source": "project"}


def test_delivery_is_the_factorys_then_the_organizations_then_the_projects(client: ApiClient, project: dict):
    factory = client.get("/v1/delivery-defaults").json()
    settings = client.get(f"/v1/projects/{project['id']}/settings").json()
    assert settings["delivery"]["maxReviewIterations"] == {"value": factory["maxReviewIterations"], "source": "organization"}

    client.patch("/v1/settings/organization", {"delivery": {"maxReviewIterations": 3}})
    client.patch(f"/v1/projects/{project['id']}/settings", {"delivery": {"test": True}})
    settings = client.get(f"/v1/projects/{project['id']}/settings").json()
    assert settings["delivery"]["maxReviewIterations"] == {"value": 3, "source": "organization"}
    assert settings["delivery"]["test"] == {"value": True, "source": "project"}
    # A role that can be turned off says so from the delivery setting that does it.
    assert settings["roles"]["qa_browser"]["enabled"] == {"value": True, "source": "project"}
    assert settings["roles"]["implementer"]["enabled"] is None

    # Turning a role off is that delivery setting.
    off = client.patch(f"/v1/projects/{project['id']}/settings", {"roles": {"simplifier": {"enabled": False}}}).json()
    assert off["delivery"]["simplify"] == {"value": False, "source": "project"}
    # The implementer always runs.
    assert client.patch(f"/v1/projects/{project['id']}/settings",
                        {"roles": {"implementer": {"enabled": False}}}).status_code == 400
    # Values are checked as the delivery would read them.
    assert client.patch("/v1/settings/organization", {"delivery": {"maxReviewIterations": 0}}).status_code == 400
    assert client.patch("/v1/settings/organization", {"roles": {"reviewer": {"effort": "extreme"}}}).status_code == 400


def test_a_prompt_is_saved_kept_in_history_and_restored(client: ApiClient):
    before = client.get("/v1/settings/organization").json()["roles"]["implementer"]["prompt"]["organization"]
    # Never edited: dude's own prompt, and no history yet.
    assert before["versionId"] is None and before["versions"] == 0
    builtin = before["body"]
    assert builtin.startswith("Implement this task.")

    saved = client.post("/v1/prompts/implementer", {"body": "# Implementer\n\nWrite the change.", "note": "Shorter"})
    assert saved.status_code == 201, saved.text
    current = saved.json()["roles"]["implementer"]["prompt"]["organization"]
    assert current["body"] == "# Implementer\n\nWrite the change." and current["versions"] == 2

    client.post("/v1/prompts/implementer", {"body": "# Implementer\n\nWrite the change and its tests.", "note": "Tests too"})
    history = client.get("/v1/prompts/implementer/history").json()
    # The first save kept dude's built-in text as where the history starts.
    assert [v["number"] for v in history["versions"]] == [3, 2, 1]
    assert history["versions"][-1]["body"] == builtin and history["versions"][-1]["createdBy"] is None
    assert history["versions"][0]["current"] is True and history["versions"][0]["note"] == "Tests too"
    assert history["versions"][1]["createdBy"]["name"]

    # Saving the same text again is no new version.
    same = client.post("/v1/prompts/implementer", {"body": "# Implementer\n\nWrite the change and its tests."})
    assert same.status_code == 200
    assert len(client.get("/v1/prompts/implementer/history").json()["versions"]) == 3

    # Restoring an old version makes a new one, saying which it restored.
    v2 = history["versions"][1]
    restored = client.post(f"/v1/prompts/versions/{v2['id']}/restore")
    assert restored.status_code == 200, restored.text
    assert restored.json()["roles"]["implementer"]["prompt"]["organization"]["body"] == v2["body"]
    history = client.get("/v1/prompts/implementer/history").json()
    assert history["versions"][0]["restoredFrom"] == v2["id"] and history["versions"][0]["note"] == "Restored v2"

    assert client.post("/v1/prompts/nobody", {"body": "x"}).status_code == 404
    assert client.post("/v1/prompts/versions/pv_missing/restore").status_code == 404


def test_a_project_adds_to_replaces_or_uses_the_organizations_prompt(client: ApiClient, project: dict):
    pid = project["id"]
    settings = client.get(f"/v1/projects/{pid}/settings").json()
    assert settings["roles"]["reviewer"]["prompt"]["project"]["mode"] == "inherit"

    added = client.post("/v1/prompts/reviewer", {"projectId": pid, "mode": "add", "body": "Money is formatted with formatUsd."})
    assert added.status_code == 201, added.text
    assert added.json()["roles"]["reviewer"]["prompt"]["project"]["mode"] == "add"
    replaced = client.post("/v1/prompts/reviewer", {"projectId": pid, "mode": "replace", "body": "Review only for money."}).json()
    assert replaced["roles"]["reviewer"]["prompt"]["project"]["mode"] == "replace"
    inherited = client.post("/v1/prompts/reviewer", {"projectId": pid, "mode": "inherit"}).json()
    assert inherited["roles"]["reviewer"]["prompt"]["project"]["mode"] == "inherit"

    history = client.get("/v1/prompts/reviewer/history", params={"projectId": pid}).json()
    assert [v["mode"] for v in history["versions"]] == ["add", "replace", "add"]
    # The organization's history is its own.
    assert client.get("/v1/prompts/reviewer/history").json()["versions"] == []

    # A project's prompt says how it goes with the organization's.
    assert client.post("/v1/prompts/reviewer", {"projectId": pid, "body": "x"}).status_code == 400
    assert client.post("/v1/prompts/reviewer", {"projectId": pid, "mode": "replace", "body": " "}).status_code == 400


def test_another_organization_sees_none_of_it(client: ApiClient, project: dict, second_org: dict):
    client.post("/v1/prompts/implementer", {"body": "Ours."})
    other = second_org["client"]
    assert other.get(f"/v1/projects/{project['id']}/settings").status_code == 404
    assert other.get("/v1/prompts/implementer/history").json()["versions"] == []
    version = client.get("/v1/prompts/implementer/history").json()["versions"][0]
    assert other.post(f"/v1/prompts/versions/{version['id']}/restore").status_code == 404


def test_epics_are_planned_active_or_done(client: ApiClient, project: dict, owner_dsn: str):
    pid = project["id"]
    planned = client.post(f"/v1/projects/{pid}/epics", {"title": "Exports", "state": "planned"}).json()
    assert planned["state"] == "planned"
    working = client.post(f"/v1/projects/{pid}/epics", {"title": "Charts"}).json()
    # With nothing set, an epic is active until all its tasks are finished.
    assert working["state"] == "active"

    task = client.create_task(pid, "Port the chart", epicId=working["id"])
    execute(owner_dsn, "UPDATE tasks SET status = 'aborted' WHERE id = %s", (task["id"],))
    epics = {e["title"]: e for e in client.get(f"/v1/projects/{pid}/epics").json()["epics"]}
    assert epics["Charts"]["state"] == "done"

    # A person's choice stands, until it is cleared.
    assert client.patch(f"/v1/epics/{working['id']}", {"state": "active"}).json()["state"] == "active"
    assert client.patch(f"/v1/epics/{working['id']}", {"state": None}).json()["state"] == "done"
    assert client.patch(f"/v1/epics/{working['id']}", {"state": "someday"}).status_code == 400

    overview = client.get(f"/v1/projects/{pid}/overview").json()
    by_title = {e["title"]: e for e in overview["epics"]}
    assert by_title["Exports"]["state"] == "planned" and by_title["Exports"]["tasks"] == 0
    assert by_title["Charts"]["lanes"]["done"] == 1 and by_title["Charts"]["tasks"] == 1


def test_a_run_records_the_prompt_version_it_ran_with(client: ApiClient, owner_dsn: str):
    """The scripted agent runs the phases; each Run records the prompt
    versions current when it started, and the history counts them."""
    project = client.create_project(
        name="Notes", slug=f"notes-{os.urandom(3).hex()}", runtimeImage="dude-runtime:test",
        agentModels={r: {"model": "fake/scripted"} for r in ("implementer", "reviewer", "simplifier")},
    )
    client.post("/v1/prompts/implementer", {"body": "Implement it, carefully."})
    client.post("/v1/prompts/implementer", {"projectId": project["id"], "mode": "add", "body": "Notes go in NOTES.md."})
    org_version = client.get("/v1/prompts/implementer/history").json()["versions"][0]["id"]
    project_version = client.get("/v1/prompts/implementer/history", params={"projectId": project["id"]}).json()["versions"][0]["id"]

    task = client.create_task(project["id"], "Write up the options")
    assert client.post(f"/v1/tasks/{task['id']}/deliver").status_code == 201

    def implement_run():
        rows = query(owner_dsn, "SELECT prompt_version_id, project_prompt_version_id FROM runs "
                                "WHERE task_id = %s AND phase = 'implement' AND lux_run_id IS NOT NULL", (task["id"],))
        return rows[0] if rows else None

    run = wait_until(implement_run, timeout=60, message="the implementer never started")
    assert run["prompt_version_id"] == org_version
    assert run["project_prompt_version_id"] == project_version

    history = client.get("/v1/prompts/implementer/history").json()
    used = history["versions"][0]["sessions"]
    assert used["count"] >= 1 and used["recent"][0]["taskId"] == task["id"]


# ---------------------------------------------------------------------------
# In the browser
# ---------------------------------------------------------------------------

import pytest  # noqa: E402
from playwright.sync_api import Page, expect  # noqa: E402

from helpers import sign_in, toast  # noqa: E402


def _sign_in(page: Page, web_url: str, api_key: str) -> None:
    sign_in(page, web_url, api_key)


@pytest.mark.ui
def test_a_prompt_is_edited_saved_and_cancelled_in_place(page: Page, web_url: str, client: ApiClient, org: dict,
                                                        console_errors: list):
    _sign_in(page, web_url, org["api_key"])
    page.goto(f"{web_url}#/org/settings/implementer")
    doc = page.get_by_test_id("prompt-document")
    # It reads rendered: dude's own prompt, never edited.
    expect(doc.get_by_test_id("markdown-view")).to_contain_text("Implement this task.")
    expect(page.get_by_test_id("org-settings")).to_contain_text("dude’s built-in prompt")

    # Edit is the source, in place; Cancel puts it back untouched.
    doc.get_by_test_id("markdown-edit").click()
    source = doc.get_by_test_id("markdown-source")
    expect(source).to_be_focused()
    source.fill("# Implementer\n\nThrown away.")
    doc.get_by_test_id("markdown-cancel").click()
    expect(doc.get_by_test_id("markdown-view")).to_contain_text("Implement this task.")
    assert client.get("/v1/prompts/implementer/history").json()["versions"] == []

    # Save keeps it, and it reads rendered again. The bar formats the
    # selection and inserts a variable at the caret.
    doc.get_by_test_id("markdown-edit").click()
    source = doc.get_by_test_id("markdown-source")
    source.fill("# Implementer\n\nWrite the change and its tests.\n\nGoal: ")
    source.evaluate("e => { const i = e.value.indexOf('its tests'); e.setSelectionRange(i, i + 9); }")
    doc.get_by_test_id("markdown-bold").click()
    expect(source).to_have_value("# Implementer\n\nWrite the change and **its tests**.\n\nGoal: ")
    source.evaluate("e => e.setSelectionRange(e.value.length, e.value.length)")
    doc.get_by_test_id("markdown-variable").click()
    page.get_by_test_id("rowmenu-task.goal").click()
    expect(source).to_have_value("# Implementer\n\nWrite the change and **its tests**.\n\nGoal: {{task.goal}}")
    doc.get_by_test_id("markdown-save").click()
    expect(doc.get_by_test_id("markdown-view").locator("h1")).to_have_text("Implementer")
    expect(doc.get_by_test_id("markdown-view").locator("strong")).to_have_text("its tests")
    # A variable reads as what it is, not as braces.
    expect(doc.get_by_test_id("markdown-view")).to_contain_text("Goal: task.goal")
    expect(doc.get_by_test_id("markdown-view")).not_to_contain_text("{{")
    # The bar is one stop to the keyboard: arrows move along it.
    doc.get_by_test_id("markdown-edit").click()
    expect(doc.get_by_test_id("markdown-source")).to_be_focused()
    doc.get_by_test_id("markdown-heading").focus()
    page.keyboard.press("ArrowRight")
    expect(doc.get_by_test_id("markdown-bold")).to_be_focused()
    page.keyboard.press("End")
    expect(doc.get_by_test_id("markdown-variable")).to_be_focused()
    # And Tab leaves it for the next control, not its next button.
    page.keyboard.press("Tab")
    expect(doc.get_by_test_id("markdown-cancel")).to_be_focused()
    doc.get_by_test_id("markdown-cancel").click()
    history = client.get("/v1/prompts/implementer/history").json()["versions"]
    assert [v["number"] for v in history] == [2, 1]

    # History: the versions, what changed, and restoring the first.
    page.get_by_test_id("prompt-history").click()
    dialog = page.get_by_test_id("prompt-history-dialog")
    expect(dialog).to_contain_text("v2")
    expect(dialog).to_contain_text("its tests")
    dialog.locator("[data-version='1']").click()
    dialog.get_by_test_id("prompt-restore").click()
    expect(dialog.locator("[data-version='3']")).to_be_visible()
    page.keyboard.press("Escape")
    expect(doc.get_by_test_id("markdown-view")).to_contain_text("Implement this task.")
    assert console_errors == []


@pytest.mark.ui
def test_a_project_shows_inherited_and_overridden_values_and_resets_them(
    page: Page, web_url: str, client: ApiClient, org: dict, project: dict, console_errors: list
):
    client.patch("/v1/settings/organization", {"roles": {"reviewer": {"effort": "high"}}})
    _sign_in(page, web_url, org["api_key"])

    # From the project's menu in the sidebar.
    page.locator(f"[data-nav-key='project:{project['id']}']").click(button="right")
    page.get_by_role("menuitem", name="Agents & prompts").click()
    expect(page.get_by_test_id("project-settings")).to_be_visible()
    page.locator("[data-settings-nav='reviewer']").click()
    settings = page.get_by_test_id("project-settings")
    # Everything follows the organization until it is changed here.
    expect(settings.locator("[data-source='project']")).to_have_count(0)
    expect(settings.locator("[data-source='organization']").first).to_contain_text("From ")

    settings.get_by_label("Reasoning effort").click()
    page.get_by_role("option", name="Low").click()
    expect(toast(page, "Effort saved")).to_be_visible()
    overridden = settings.locator("[data-source='project']")
    expect(overridden).to_have_count(1)
    expect(overridden).to_contain_text("Overridden")
    assert client.get(f"/v1/projects/{project['id']}/settings").json()["roles"]["reviewer"]["effort"] == \
        {"value": "low", "source": "project"}
    expect(page.locator("[data-settings-nav='reviewer']")).to_contain_text("changed")

    # Reset: the organization's value again.
    overridden.get_by_role("button", name="Reset").click()
    expect(settings.locator("[data-source='project']")).to_have_count(0)
    assert client.get(f"/v1/projects/{project['id']}/settings").json()["roles"]["reviewer"]["effort"] == \
        {"value": "high", "source": "organization"}

    # A project's own prompt: added to the organization's.
    settings.get_by_role("group", name="Prompt").get_by_role("button", name="Add to").click()
    doc = settings.get_by_test_id("prompt-document")
    doc.get_by_test_id("markdown-source").fill("Money is formatted with `formatUsd`.")
    doc.get_by_test_id("markdown-save").click()
    expect(doc.get_by_test_id("markdown-view")).to_contain_text("Money is formatted")
    assert client.get(f"/v1/projects/{project['id']}/settings").json()["roles"]["reviewer"]["prompt"]["project"]["mode"] == "add"
    assert console_errors == []


@pytest.mark.ui
def test_the_project_page_shows_epics_by_state(page: Page, web_url: str, client: ApiClient, org: dict,
                                               forge_project: dict, console_errors: list):
    pid = forge_project["id"]
    client.post(f"/v1/projects/{pid}/epics", {"title": "Exports", "state": "planned"})
    charts = client.post(f"/v1/projects/{pid}/epics", {"title": "Charts"}).json()
    client.create_task(pid, "Port the chart", epicId=charts["id"])
    _sign_in(page, web_url, org["api_key"])
    epics = page.get_by_test_id("project-epics")
    expect(epics.locator("[data-epic-state='active']")).to_contain_text("Charts")
    expect(epics.locator("[data-epic-state='active']")).to_contain_text("1 backlog")
    expect(epics.locator("[data-epic-state='planned']")).to_contain_text("Exports")

    # Set it done by hand.
    epics.get_by_role("button", name="State of Charts").click()
    page.get_by_role("menuitem", name="Done").click()
    expect(epics.locator("[data-epic-state='done']")).to_contain_text("Charts")
    assert {e["title"]: e["state"] for e in client.get(f"/v1/projects/{pid}/epics").json()["epics"]}["Charts"] == "done"
    assert console_errors == []
