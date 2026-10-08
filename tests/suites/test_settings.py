"""Settings in two layers, prompts with a history, epics with a state, and
machine sizes.

The organization sets the defaults every project starts from; a project
stores only what it overrides, and its settings say where each value comes
from. Prompts keep every save, and a Run records the version it ran with.
Every agent and preview runs on one of the organization's machine sizes.
Driven through the public API, as the settings screens use it.
"""

from __future__ import annotations

import os
import re

import requests

from helpers import ApiClient, execute, query, wait_until


def test_an_organizations_default_is_overridden_by_a_project_and_reset(client: ApiClient, project: dict):
    review, review2 = client.tier_for("org-review"), client.tier_for("org-review-2")
    org = client.patch("/v1/settings/organization", {"roles": {"reviewer": {"tier": review, "timeLimitMinutes": 60}}})
    assert org.status_code == 200, org.text
    assert org.json()["roles"]["reviewer"]["tier"] == {"value": review, "source": "organization"}

    # The project follows it until it says otherwise.
    settings = client.get(f"/v1/projects/{project['id']}/settings").json()
    assert settings["roles"]["reviewer"]["tier"] == {"value": review, "source": "organization", "organization": review}
    assert settings["roles"]["reviewer"]["timeLimitMinutes"] == {"value": 60, "source": "organization"}

    # An override of one field leaves the others inherited.
    changed = client.patch(f"/v1/projects/{project['id']}/settings", {"roles": {"reviewer": {"timeLimitMinutes": 45}}}).json()
    assert changed["roles"]["reviewer"]["timeLimitMinutes"] == {"value": 45, "source": "project"}
    assert changed["roles"]["reviewer"]["tier"] == {"value": review, "source": "organization", "organization": review}
    # Stored as an override only.
    assert client.get(f"/v1/projects/{project['id']}").json()["agentModels"]["reviewer"] == {"timeLimitMinutes": 45}

    # The organization's later change still reaches what the project did not override.
    client.patch("/v1/settings/organization", {"roles": {"reviewer": {"tier": review2}}})
    settings = client.get(f"/v1/projects/{project['id']}/settings").json()
    assert settings["roles"]["reviewer"]["tier"]["value"] == review2

    # Reset is a delete: the value is the organization's again, and the
    # project stores nothing for the role.
    reset = client.patch(f"/v1/projects/{project['id']}/settings", {"roles": {"reviewer": {"timeLimitMinutes": None}}}).json()
    assert reset["roles"]["reviewer"]["timeLimitMinutes"] == {"value": 60, "source": "organization"}
    assert "reviewer" not in client.get(f"/v1/projects/{project['id']}").json()["agentModels"]


def test_the_fixer_follows_the_implementer_without_calling_it_its_own(client: ApiClient, project: dict):
    # The project overrides the implementer's tier; the fixer runs on it too,
    # but has nothing of its own to reset.
    impl, fix = client.tier_for("proj-impl"), client.tier_for("proj-fix")
    settings = client.patch(f"/v1/projects/{project['id']}/settings", {"roles": {"implementer": {"tier": impl}}}).json()
    assert settings["roles"]["implementer"]["tier"]["value"] == impl
    assert settings["roles"]["implementer"]["tier"]["source"] == "project"
    assert settings["roles"]["fixer"]["tier"]["value"] == impl
    assert settings["roles"]["fixer"]["tier"]["source"] == "organization"
    assert settings["roles"]["fixer"]["tier"]["followsImplementer"] is True

    settings = client.patch(f"/v1/projects/{project['id']}/settings", {"roles": {"fixer": {"tier": fix}}}).json()
    assert settings["roles"]["fixer"]["tier"]["value"] == fix
    assert settings["roles"]["fixer"]["tier"]["source"] == "project"


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
    # A role names no effort: that is its tier's.
    assert client.patch("/v1/settings/organization", {"roles": {"reviewer": {"effort": "high"}}}).status_code == 400


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
        name="Notes", slug=f"notes-{os.urandom(3).hex()}",
        agentModels=client.on_models({r: "fake/scripted" for r in ("implementer", "reviewer", "simplifier")}),
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
    client.patch("/v1/settings/organization", {"roles": {"reviewer": {"timeLimitMinutes": 60}}})
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
    # A role has no effort of its own: that is its tier's.
    expect(settings.get_by_label("Reasoning effort")).to_have_count(0)

    settings.get_by_label("Time limit without progress").click()
    page.get_by_role("option", name="45 min").click()
    expect(toast(page, "Time limit saved")).to_be_visible()
    overridden = settings.locator("[data-source='project']")
    expect(overridden).to_have_count(1)
    expect(overridden).to_contain_text("Overridden")
    assert client.get(f"/v1/projects/{project['id']}/settings").json()["roles"]["reviewer"]["timeLimitMinutes"] == \
        {"value": 45, "source": "project"}
    expect(page.locator("[data-settings-nav='reviewer']")).to_contain_text("changed")

    # Reset: the organization's value again.
    overridden.get_by_role("button", name="Reset").click()
    expect(settings.locator("[data-source='project']")).to_have_count(0)
    assert client.get(f"/v1/projects/{project['id']}/settings").json()["roles"]["reviewer"]["timeLimitMinutes"] == \
        {"value": 60, "source": "organization"}

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


# ---------------------------------------------------------------------------
# Machine sizes
# ---------------------------------------------------------------------------


def _sizes(client: ApiClient) -> dict:
    return {s["name"]: s for s in client.get("/v1/machines/sizes").json()["sizes"]}


def _pool_id(client: ApiClient, name: str) -> str:
    """A lux pool's id, as the fake lux lists it now."""
    return next(p["id"] for p in client.get("/v1/machines/pools").json()["pools"] if p["name"] == name)


def _fake_lux(env, method: str, path: str, body: dict | None = None) -> requests.Response:
    """Change the fake lux's pools as lux's own API would (POST /v1/pools, DELETE /v1/pools/{name})."""
    return requests.request(method, env.fake_lux_url + path, json=body, headers={"authorization": f"Bearer {env.lux_key}"}, timeout=10)


def _invite_member(admin: ApiClient, env, name: str) -> tuple[ApiClient, str]:
    resp = admin.post("/v1/people", {"name": name, "email": f"{name.lower()}-{os.urandom(2).hex()}@acme.dev", "role": "member"})
    assert resp.status_code == 201, resp.text
    key = resp.json()["key"]
    return ApiClient(env.control_plane_url, key), key


def _pick(page: Page, trigger, label: str, meta: str = "") -> None:
    """Open a Select and choose the one option whose text is exactly `label`
    then `meta` (a size's spec): anchored and case-sensitive, so neither
    another size whose spec ends the same nor an option that quotes the spec
    in its own meta is taken."""
    trigger.click()
    text = re.compile(rf"^{re.escape(label)}\s*{re.escape(meta)}$")
    page.get_by_role("option").filter(has_text=text).click()


@pytest.mark.ui
def test_an_admin_adds_a_size_in_half_steps_and_one_off_step_or_too_big_is_refused(
    page: Page, web_url: str, client: ApiClient, org: dict, console_errors: list
):
    _sign_in(page, web_url, org["api_key"])
    page.goto(f"{web_url}#/org/settings/machines")
    machines = page.get_by_test_id("machines-page")
    # Every organisation starts with lux's own default, and sees lux's pools.
    expect(machines.locator("[data-size='Standard']")).to_contain_text("Default")
    expect(machines.locator("[data-size='Standard']")).to_contain_text("2 CPUs")
    expect(machines.get_by_test_id("machine-pools").locator("[data-pool='big']")).to_contain_text("c7a.8xlarge")
    expect(machines.get_by_test_id("memory-share")).to_contain_text("Run A · asks 16 · gets 15")

    machines.get_by_role("button", name="Add size", exact=True).click()
    dialog = page.get_by_role("dialog")
    dialog.get_by_label("Name", exact=True).fill("Large (Java)")
    cpus = page.get_by_test_id("machine-size-cpus")
    # ↑ is a step.
    cpus.fill("6")
    cpus.press("ArrowUp")
    expect(cpus).to_have_value("6.5")
    page.get_by_test_id("machine-size-memory").fill("22.5")
    page.get_by_test_id("machine-size-disk").fill("120")
    expect(page.get_by_test_id("machine-fit")).to_have_attribute("data-fit", "fits")
    expect(page.get_by_test_id("machine-fit")).to_contain_text("Fits ‘default’")

    # Off the step: refused in place, naming it.
    cpus.fill("2.3")
    expect(page.get_by_role("dialog")).to_contain_text("Whole or half CPUs: 0.5, 1, 1.5…")
    expect(dialog.get_by_role("button", name="Add size", exact=True)).to_be_disabled()
    cpus.fill("6.5")

    # Too big for the pool's hosts: refused, saying what does not fit.
    _pick(page, page.get_by_test_id("machine-size-pool"), "big — EC2 · c7a.8xlarge · 32 CPUs · 64 GiB · 380 GiB")
    page.get_by_test_id("machine-size-memory").fill("72")
    expect(page.get_by_test_id("machine-fit")).to_have_attribute("data-fit", "too_big")
    expect(page.get_by_test_id("machine-fit")).to_contain_text("72 GiB memory (it offers 64)")
    expect(page.get_by_role("dialog")).to_contain_text("Most a big host has: 64 GiB")
    expect(dialog.get_by_role("button", name="Add size", exact=True)).to_be_disabled()
    # The API refuses it too, whatever a browser sends.
    big = _pool_id(client, "big")
    too_big = client.post("/v1/machines/sizes", {"name": "Huge", "cpus": 16, "memoryMiB": 72 * 1024, "diskGiB": 200, "poolId": big})
    assert too_big.status_code == 422 and "72 GiB memory (it offers 64)" in too_big.json()["error"]["message"]

    page.get_by_test_id("machine-size-memory").fill("22.5")
    dialog.get_by_role("button", name="Add size", exact=True).click()
    expect(toast(page, "Large (Java) added")).to_be_visible()
    row = machines.locator("[data-size='Large (Java)']")
    expect(row).to_contain_text("6.5 CPUs")
    expect(row).to_contain_text("22.5 GiB")
    # Its pool by lux's current name.
    expect(row.locator("[data-pool-cell]")).to_have_text("big")
    size = _sizes(client)["Large (Java)"]
    # Stored by lux's id, not its name.
    assert (size["cpus"], size["memoryMiB"], size["diskGiB"], size["poolId"], size["poolName"]) == (6.5, 23040, 120, big, "big")
    assert big.startswith("pool_") and big != "big"
    # The menu counts it at once: Standard, the conductor's Small and Large (Java).
    expect(page.locator("[data-settings-nav='machines']")).to_have_text(re.compile(r"^Machines\s*3$"))
    assert console_errors == []


@pytest.mark.ui
def test_a_size_is_set_on_the_implementer_overridden_in_a_project_and_reset(
    page: Page, web_url: str, client: ApiClient, org: dict, project: dict, console_errors: list
):
    for body in ({"name": "Large", "cpus": 8, "memoryMiB": 16384, "diskGiB": 80},
                 {"name": "XL", "cpus": 16, "memoryMiB": 49152, "diskGiB": 200, "poolId": _pool_id(client, "big")}):
        assert client.post("/v1/machines/sizes", body).status_code == 201
    sizes = _sizes(client)
    org_name = client.get("/v1/settings/organization").json()["organization"]["name"]
    _sign_in(page, web_url, org["api_key"])

    # The organisation's implementer: Large. The fixer follows it.
    page.goto(f"{web_url}#/org/settings/implementer")
    settings = page.get_by_test_id("org-settings")
    _pick(page, settings.get_by_test_id("role-machine"), "Large", "8 CPUs · 16 GiB · 80 GiB")
    expect(toast(page, "Machine saved")).to_be_visible()
    assert client.get("/v1/settings/organization").json()["roles"]["implementer"]["machineSize"]["value"] == sizes["Large"]["id"]
    page.locator("[data-settings-nav='fixer']").click()
    expect(settings.get_by_test_id("role-machine")).to_contain_text("The implementer’s")
    expect(settings.get_by_test_id("role-machine")).to_contain_text("Large")
    # The organisation's toast closes (after 5 s) before the project's opens,
    # so the next "Machine saved" can only be the project's.
    expect(toast(page, "Machine saved")).to_have_count(0, timeout=10_000)

    # The project overrides it with XL, says so, and Reset puts the organisation's back.
    page.goto(f"{web_url}#/project/{project['id']}/settings/implementer")
    ps = page.get_by_test_id("project-settings")
    # Inherited: the organisation's Large, named as such.
    expect(ps.get_by_test_id("role-machine")).to_have_text(re.compile(rf"^From {re.escape(org_name)}\s*Large · 8 CPUs · 16 GiB · 80 GiB$"))
    _pick(page, ps.get_by_test_id("role-machine"), "XL", "16 CPUs · 48 GiB · 200 GiB · big")
    expect(toast(page, "Machine saved")).to_be_visible()
    # "Overridden", then what the organisation says, then Reset.
    was_large = re.compile(rf"^Overridden\s*{re.escape(org_name)}: Large\s*Reset$")
    overridden = ps.locator("[data-source='project']").filter(has_text=was_large)
    expect(overridden).to_contain_text("Overridden")
    assert client.get(f"/v1/projects/{project['id']}").json()["agentModels"]["implementer"]["machineSize"] == sizes["XL"]["id"]
    overridden.get_by_role("button", name="Reset", exact=True).click()
    expect(ps.locator("[data-source='project']").filter(has_text=was_large)).to_have_count(0)
    assert "machineSize" not in client.get(f"/v1/projects/{project['id']}").json()["agentModels"].get("implementer", {})

    # The investigator is configurable like the others.
    page.goto(f"{web_url}#/org/settings/investigator")
    expect(page.get_by_test_id("org-settings").get_by_test_id("role-machine")).to_contain_text("Default")
    assert console_errors == []


@pytest.mark.ui
def test_removing_a_size_in_use_moves_what_named_it(
    page: Page, web_url: str, client: ApiClient, org: dict, project: dict, console_errors: list
):
    for body in ({"name": "Large", "cpus": 8, "memoryMiB": 16384, "diskGiB": 80}, {"name": "XL", "cpus": 16, "memoryMiB": 32768, "diskGiB": 100}):
        assert client.post("/v1/machines/sizes", body).status_code == 201
    sizes = _sizes(client)
    client.patch("/v1/settings/organization", {"roles": {"implementer": {"machineSize": sizes["Large"]["id"]}}})
    client.patch(f"/v1/projects/{project['id']}/settings", {"roles": {"reviewer": {"machineSize": sizes["Large"]["id"]}}})
    _sign_in(page, web_url, org["api_key"])
    page.goto(f"{web_url}#/org/settings/machines")
    machines = page.get_by_test_id("machines-page")
    # The implementer, the fixer that follows it, and a project's reviewer.
    expect(machines.locator("[data-size='Large']")).to_contain_text("2 agents · 1 project")

    # The default cannot be removed: its menu says why.
    machines.get_by_role("button", name="Actions for Standard").click()
    expect(page.get_by_role("menuitem", name="Remove…")).to_be_disabled()
    page.keyboard.press("Escape")

    machines.get_by_role("button", name="Actions for Large").click()
    page.get_by_role("menuitem", name="Remove…").click()
    uses = page.get_by_test_id("machine-size-uses")
    expect(uses).to_contain_text("Implementer")
    expect(uses).to_contain_text("follows the implementer")
    expect(uses).to_contain_text("E2E Project · Reviewer")
    _pick(page, page.get_by_test_id("machine-size-move"), "XL", "16 CPUs · 32 GiB · 100 GiB")
    page.get_by_role("button", name="Remove and move them", exact=True).click()
    expect(toast(page, "Large removed")).to_be_visible()
    expect(machines.locator("[data-size='Large']")).to_have_count(0)
    # The menu counts what is left: Standard, the conductor's Small and XL.
    expect(page.locator("[data-settings-nav='machines']")).to_have_text(re.compile(r"^Machines\s*3$"))
    assert client.get("/v1/settings/organization").json()["roles"]["implementer"]["machineSize"]["value"] == sizes["XL"]["id"]
    assert client.get(f"/v1/projects/{project['id']}").json()["agentModels"]["reviewer"]["machineSize"] == sizes["XL"]["id"]
    assert console_errors == []


@pytest.mark.ui
def test_a_member_sees_machines_read_only(page: Page, web_url: str, client: ApiClient, env, console_errors: list):
    _, key = _invite_member(client, env, "Bo")
    _sign_in(page, web_url, key)
    page.goto(f"{web_url}#/org/settings/machines")
    machines = page.get_by_test_id("machines-page")
    expect(machines.locator("[data-size='Standard']")).to_be_visible()
    expect(machines.get_by_role("button", name="Add size", exact=True)).to_have_count(0)
    expect(machines.get_by_role("button", name="Actions for Standard")).to_have_count(0)
    assert console_errors == []


def test_a_phase_run_goes_to_lux_on_its_roles_size_and_pool(client: ApiClient, env, owner_dsn: str):
    """The scripted agent runs the implementer; the spec the fake lux was
    sent carries the size as resources and its pool by lux's id, and the
    Run records it with the pool's name then."""
    project = client.create_project(
        name="Sized", slug=f"sized-{os.urandom(3).hex()}",
        agentModels=client.on_models({r: "fake/scripted" for r in ("implementer", "reviewer", "simplifier")}),
    )
    big = _pool_id(client, "big")
    size = client.post("/v1/machines/sizes", {"name": "Half", "cpus": 6.5, "memoryMiB": 23040, "diskGiB": 120, "poolId": big}).json()
    half = next(s for s in size["sizes"] if s["name"] == "Half")
    client.patch(f"/v1/projects/{project['id']}/settings", {"roles": {"implementer": {"machineSize": half["id"]}}})

    task = client.create_task(project["id"], "Write it up")
    assert client.post(f"/v1/tasks/{task['id']}/deliver").status_code == 201

    def submitted():
        rows = query(owner_dsn, "SELECT id, lux_run_id, machine FROM runs WHERE task_id = %s AND phase = 'implement' AND lux_run_id IS NOT NULL", (task["id"],))
        return rows[0] if rows else None

    run = wait_until(submitted, timeout=60, message="the implementer never reached lux")
    spec = requests.get(f"{env.fake_lux_url}/v1/runs/{run['lux_run_id']}", headers={"authorization": f"Bearer {env.lux_key}"}, timeout=10).json()["spec"]
    assert spec["resources"] == {"cpus": 6.5, "memory": 23040 * 1024 * 1024, "disk": 120 * 1024 ** 3}
    assert spec["placement"] == {"poolId": big}
    assert run["machine"]["name"] == "Half" and run["machine"]["from"] == "project"
    assert (run["machine"]["poolId"], run["machine"]["pool"]) == (big, "big")
    assert client.get(f"/v1/runs/{run['id']}").json()["machine"]["cpus"] == 6.5


@pytest.mark.ui
def test_a_size_whose_pool_vanished_from_lux_says_so_and_asks_for_another(
    page: Page, web_url: str, client: ApiClient, org: dict, env, console_errors: list
):
    # A pool of its own, so removing it from the fake lux leaves every other test's pools alone.
    scratch = f"scratch-{os.urandom(3).hex()}"
    made = _fake_lux(env, "POST", "/v1/pools", {"name": scratch, "provider": "static"})
    assert made.status_code == 200, made.text
    pool_id = made.json()["id"]
    added = client.post("/v1/machines/sizes", {"name": "Scratch", "cpus": 2, "memoryMiB": 4096, "diskGiB": 20, "poolId": pool_id})
    assert added.status_code == 201, added.text
    assert _fake_lux(env, "DELETE", f"/v1/pools/{scratch}").status_code == 204
    assert _sizes(client)["Scratch"]["poolName"] is None

    _sign_in(page, web_url, org["api_key"])
    page.goto(f"{web_url}#/org/settings/machines")
    machines = page.get_by_test_id("machines-page")
    row = machines.locator("[data-size='Scratch']")
    expect(row.locator("[data-pool-gone]")).to_have_text("Pool gone from lux")
    expect(row.locator("[data-fit-cell]")).to_have_text("—")

    # Edit says so, and Save is refused until another pool is chosen.
    machines.get_by_role("button", name="Actions for Scratch").click()
    page.get_by_role("menuitem", name="Edit").click()
    expect(page.get_by_test_id("machine-pool-gone")).to_contain_text("Its pool is gone from lux.")
    save = page.get_by_test_id("machine-size-save")
    expect(save).to_be_disabled()
    _pick(page, page.get_by_test_id("machine-size-pool"), "big — EC2 · c7a.8xlarge · 32 CPUs · 64 GiB · 380 GiB")
    expect(page.get_by_test_id("machine-pool-gone")).to_have_count(0)
    expect(save).to_be_enabled()
    save.click()
    expect(toast(page, "Scratch saved")).to_be_visible()
    expect(row.locator("[data-pool-gone]")).to_have_count(0)
    expect(row.locator("[data-pool-cell]")).to_have_text("big")
    # 2 of big's 32 CPUs and 4 of its 64 GiB: 6% of one host.
    expect(row.locator("[data-fit-cell]")).to_have_text("6% of a host")
    assert _sizes(client)["Scratch"]["poolId"] == _pool_id(client, "big")
    assert console_errors == []


# ---------------------------------------------------------------------------
# Model tiers
# ---------------------------------------------------------------------------


def _tiers(client: ApiClient) -> dict:
    return {t["name"]: t for t in client.get("/v1/models/tiers").json()["tiers"]}


@pytest.mark.ui
def test_an_admin_adds_a_tier_and_changes_one_and_the_role_page_follows(
    page: Page, web_url: str, client: ApiClient, org: dict, console_errors: list
):
    _sign_in(page, web_url, org["api_key"])
    page.goto(f"{web_url}#/org/settings/models")
    models = page.get_by_test_id("models-page")
    # Every organisation starts with three tiers; Fast names no model yet.
    expect(models.locator("[data-tier='Thinker']")).to_contain_text("Reads, plans, judges and tidies.")
    expect(models.locator("[data-tier='Fast'] [data-model-cell]")).to_have_text("Not set")
    expect(page.locator("[data-settings-nav='models']")).to_have_text(re.compile(r"^Models\s*3$"))
    expect(models.get_by_test_id("models-explainer")).to_contain_text("dude doesn’t see which.")

    # Add a tier. The suite's orchestrator has no proxy, so there is no list
    # to suggest from, and it says so; any name is taken.
    models.get_by_role("button", name="Add tier", exact=True).click()
    dialog = page.get_by_role("dialog")
    expect(dialog).to_contain_text("The proxy’s list of models could not be read")
    dialog.get_by_label("Name", exact=True).fill("Cheap")
    dialog.get_by_label("What it’s for", exact=True).fill("Bulk, low-stakes work at the lowest price.")
    model = dialog.get_by_label("Model to request", exact=True)
    model.fill("llm-openai/gpt-5.6-luna")
    expect(dialog).to_contain_text("The model as the proxy names it: no spaces or slashes")
    expect(dialog.get_by_test_id("model-tier-save")).to_be_disabled()
    model.fill("gpt-5.6-luna")
    dialog.get_by_test_id("model-tier-save").click()
    expect(toast(page, "Cheap added")).to_be_visible()
    expect(models.locator("[data-tier='Cheap'] [data-model-cell]")).to_have_text("gpt-5.6-luna")
    expect(page.locator("[data-settings-nav='models']")).to_have_text(re.compile(r"^Models\s*4$"))
    assert _tiers(client)["Cheap"]["description"] == "Bulk, low-stakes work at the lowest price."

    # Change what Coder requests and how hard it thinks, and the implementer's page says so.
    models.get_by_role("button", name="Actions for Coder").click()
    page.get_by_role("menuitem", name="Change model…").click()
    dialog = page.get_by_role("dialog")
    expect(dialog.get_by_test_id("model-tier-users")).to_contain_text("On Coder now: Implementer, Fixer.")
    dialog.get_by_label("Model to request", exact=True).fill("claude-opus-5-5")
    effort = dialog.get_by_label("Reasoning effort")
    expect(effort).to_have_text(re.compile(r"^Medium"))
    expect(dialog).to_contain_text("None turns thinking off, so the chat shows none.")
    effort.click()
    page.get_by_role("option", name="Max", exact=True).click()
    # The advanced fields take JSON objects only, as the API does.
    dialog.get_by_text("Advanced: OpenCode model options and request headers").click()
    headers = dialog.get_by_label("Request headers")
    headers.fill('{"X Team": "dude"}')
    expect(dialog).to_contain_text("Header names are letters, digits")
    expect(dialog.get_by_test_id("model-tier-save")).to_be_disabled()
    headers.fill('{"X-Team": "dude"}')
    dialog.get_by_label("OpenCode model options").fill('{"sendReasoning": true}')
    dialog.get_by_test_id("model-tier-save").click()
    expect(toast(page, "Coder saved")).to_be_visible()
    expect(models.locator("[data-tier='Coder'] [data-model-cell]")).to_have_text("claude-opus-5-5")
    expect(models.locator("[data-tier='Coder'] [data-effort-cell]")).to_have_text("Max")
    expect(models.locator("[data-tier='Fast'] [data-effort-cell]")).to_have_text("Model’s default")
    coder = _tiers(client)["Coder"]
    assert (coder["effort"], coder["options"], coder["headers"]) == ("max", {"sendReasoning": True}, {"X-Team": "dude"})

    page.goto(f"{web_url}#/org/settings/implementer")
    settings = page.get_by_test_id("org-settings")
    tier = settings.get_by_test_id("role-tier")
    expect(tier).to_contain_text("Coder")
    expect(settings.get_by_test_id("role-tier-requests")).to_have_text("Requests claude-opus-5-5 · max")
    expect(settings.get_by_label("Reasoning effort")).to_have_count(0)
    # Picking another tier: the implementer now requests Cheap's model, at the model's default.
    tier.click()
    page.get_by_role("option").filter(has_text=re.compile(r"^Cheap")).click()
    expect(toast(page, "Model saved")).to_be_visible()
    expect(settings.get_by_test_id("role-tier-requests")).to_have_text("Requests gpt-5.6-luna · model’s default")
    assert client.get("/v1/settings/organization").json()["roles"]["implementer"]["tier"]["value"] == _tiers(client)["Cheap"]["id"]
    assert console_errors == []


@pytest.mark.ui
def test_a_project_overrides_a_roles_tier_and_resets_it(
    page: Page, web_url: str, client: ApiClient, org: dict, project: dict, console_errors: list
):
    tiers = _tiers(client)
    org_name = client.get("/v1/settings/organization").json()["organization"]["name"]
    _sign_in(page, web_url, org["api_key"])
    page.goto(f"{web_url}#/project/{project['id']}/settings/reviewer")
    ps = page.get_by_test_id("project-settings")
    tier = ps.get_by_test_id("role-tier")
    # Inherited: the organisation's Thinker, named as such.
    expect(tier).to_have_text(re.compile(rf"^From {re.escape(org_name)} · Thinker"))
    tier.click()
    # The tiers are the organisation's: its admin is offered its Models page.
    expect(page.get_by_role("listbox").get_by_role("button", name="Manage tiers in Models")).to_be_visible()
    page.get_by_role("option").filter(has_text=re.compile(r"^Fast")).click()
    expect(toast(page, "Model saved")).to_be_visible()
    was_thinker = re.compile(rf"^Overridden\s*{re.escape(org_name)}: Thinker\s*Reset$")
    overridden = ps.locator("[data-source='project']").filter(has_text=was_thinker)
    expect(overridden).to_be_visible()
    expect(page.locator("[data-settings-nav='reviewer']")).to_contain_text("changed")
    assert client.get(f"/v1/projects/{project['id']}").json()["agentModels"]["reviewer"] == {"tier": tiers["Fast"]["id"]}

    overridden.get_by_role("button", name="Reset", exact=True).click()
    expect(ps.locator("[data-source='project']").filter(has_text=was_thinker)).to_have_count(0)
    expect(tier).to_have_text(re.compile(rf"^From {re.escape(org_name)} · Thinker"))
    assert "reviewer" not in client.get(f"/v1/projects/{project['id']}").json()["agentModels"]
    assert console_errors == []


@pytest.mark.ui
def test_removing_a_tier_in_use_moves_what_named_it(
    page: Page, web_url: str, client: ApiClient, org: dict, project: dict, console_errors: list
):
    tiers = _tiers(client)
    client.patch(f"/v1/projects/{project['id']}/settings", {"roles": {"reviewer": {"tier": tiers["Fast"]["id"], "timeLimitMinutes": 45}}})
    client.patch("/v1/settings/organization", {"roles": {"simplifier": {"tier": tiers["Fast"]["id"]}}})
    _sign_in(page, web_url, org["api_key"])
    page.goto(f"{web_url}#/org/settings/models")
    models = page.get_by_test_id("models-page")
    expect(models.locator("[data-tier='Fast']")).to_contain_text("1 agent · 1 project")
    count = len(tiers)
    expect(page.locator("[data-settings-nav='models']")).to_have_text(re.compile(rf"^Models\s*{count}$"))

    models.get_by_role("button", name="Actions for Fast").click()
    page.get_by_role("menuitem", name="Remove…").click()
    uses = page.get_by_test_id("model-tier-uses")
    expect(uses).to_contain_text("Simplifier")
    expect(uses).to_contain_text("E2E Project · Reviewer")
    expect(uses).to_contain_text("project override")
    page.get_by_test_id("model-tier-move").click()
    page.get_by_role("option").filter(has_text=re.compile(r"^Coder")).click()
    page.get_by_role("button", name="Remove and move them", exact=True).click()
    expect(toast(page, "Fast removed")).to_be_visible()
    expect(models.locator("[data-tier='Fast']")).to_have_count(0)
    # The menu counts what is left at once.
    expect(page.locator("[data-settings-nav='models']")).to_have_text(re.compile(rf"^Models\s*{count - 1}$"))
    coder = tiers["Coder"]["id"]
    assert client.get("/v1/settings/organization").json()["roles"]["simplifier"]["tier"]["value"] == coder
    assert client.get(f"/v1/projects/{project['id']}").json()["agentModels"]["reviewer"] == {"tier": coder, "timeLimitMinutes": 45}

    # One nothing uses goes on a plain confirm.
    assert client.post("/v1/models/tiers", {"name": "Spare"}).status_code == 201
    page.reload()
    models.get_by_role("button", name="Actions for Spare").click()
    page.get_by_role("menuitem", name="Remove…").click()
    expect(page.get_by_role("dialog")).to_contain_text("Nothing uses it.")
    page.get_by_role("button", name="Remove", exact=True).click()
    expect(toast(page, "Spare removed")).to_be_visible()
    assert "Spare" not in _tiers(client)
    assert console_errors == []


@pytest.mark.ui
def test_a_member_sees_models_read_only(page: Page, web_url: str, client: ApiClient, env, console_errors: list):
    _, key = _invite_member(client, env, "Bo")
    _sign_in(page, web_url, key)
    page.goto(f"{web_url}#/org/settings/models")
    models = page.get_by_test_id("models-page")
    expect(models.locator("[data-tier='Coder']")).to_be_visible()
    expect(models.get_by_role("button", name="Add tier", exact=True)).to_have_count(0)
    expect(models.get_by_role("button", name="Actions for Coder")).to_have_count(0)
    assert console_errors == []


@pytest.mark.ui
def test_the_session_header_says_the_tier_and_the_model_it_requested(
    page: Page, web_url: str, client: ApiClient, org: dict, forge_project: dict, owner_dsn: str, console_errors: list
):
    """The scripted agent runs the implementer on a tier, and stays at work
    (fake/hang); the Run records the tier's name and the model it requested
    at the submit, and the header says both. A later edit of the tier changes
    neither."""
    project = forge_project
    client.patch(f"/v1/projects/{project['id']}", {"agentModels": client.on_models({"implementer": "fake/hang"})})
    tier_id = client.get(f"/v1/projects/{project['id']}").json()["agentModels"]["implementer"]["tier"]
    assert client.put(f"/v1/models/tiers/{tier_id}", {"name": "Scripted", "model": "fake/hang"}).status_code == 200
    task = client.create_task(project["id"], "Write it up")
    assert client.post(f"/v1/tasks/{task['id']}/deliver").status_code == 201

    def submitted():
        rows = query(owner_dsn, "SELECT id, model, model_tier FROM runs WHERE task_id = %s AND phase = 'implement' AND lux_run_id IS NOT NULL",
                     (task["id"],))
        return rows[0] if rows else None

    run = wait_until(submitted, timeout=60, message="the implementer never reached lux")
    assert (run["model"], run["model_tier"]) == ("fake/hang", "Scripted")
    assert client.put(f"/v1/models/tiers/{tier_id}", {"name": "Renamed", "model": "fake/hang"}).status_code == 200
    assert client.get(f"/v1/runs/{run['id']}").json()["modelTier"] == "Scripted"

    _sign_in(page, web_url, org["api_key"])
    page.goto(f"{web_url}#/run/{run['id']}")
    chip = page.get_by_test_id("run-model")
    expect(chip).to_have_attribute("aria-label", "Model: Scripted, requests fake/hang")
    chip.focus()
    tip = page.get_by_role("tooltip")
    expect(tip).to_contain_text("When this session started, Scripted asked the proxy for fake/hang")
    expect(tip).to_contain_text("changing Scripted now changes the next session, not this one.")
    expect(tip).to_contain_text("how the proxy served it is the proxy’s to say")
    assert console_errors == []
