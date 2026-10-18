"""A repository's setup script, edited in Project settings → Repositories.

An admin sets a script through its own dialog (the row menu's Setup
script…), sees the `setup` mark after the repository's name, reopens it
from the mark, and clears it, which removes it. A member, who cannot
change repositories, has no row menu and reads the script from the mark,
read-only. Each step is checked against what the API then returns.
"""

from __future__ import annotations

import os

import pytest
from playwright.sync_api import Page, expect

from helpers import ApiClient, provision_person, sign_in, toast

SCRIPT = 'ln -sf ../../scripts/pre-commit "$(git rev-parse --git-path hooks)/pre-commit"\nnpm ci --prefer-offline'


def _project_with_repositories(client: ApiClient) -> dict:
    project = client.create_project(name="Setup", slug=f"setup-{os.urandom(3).hex()}")
    for name in ("api", "web"):
        resp = client.post(f"/v1/projects/{project['id']}/repositories", {"name": name, "url": f"https://github.com/acme/{name}.git"})
        assert resp.status_code == 201, resp.text
    return project


def _repository(client: ApiClient, project: dict, name: str) -> dict:
    return next(r for r in client.get(f"/v1/projects/{project['id']}").json()["repositories"] if r["name"] == name)


def _type_script(page: Page, text: str) -> None:
    """Replace the editor's text as a person would: select all, then type."""
    content = page.get_by_test_id("setup-script-editor").locator(".cm-content")
    content.click()
    page.keyboard.press("ControlOrMeta+a")
    page.keyboard.press("Delete")
    if text:
        page.keyboard.insert_text(text)


@pytest.mark.ui
def test_a_setup_script_is_set_marked_reopened_and_cleared(page: Page, web_url: str, client: ApiClient, org: dict,
                                                         console_errors: list):
    project = _project_with_repositories(client)
    sign_in(page, web_url, org["api_key"], at=f"#/project/{project['id']}/settings/repositories")
    settings = page.get_by_test_id("project-settings")
    api_row = settings.get_by_test_id("repository-row-api")
    expect(api_row).to_be_visible()
    expect(settings.get_by_test_id("setup-script-mark")).to_have_count(0)

    settings.get_by_role("button", name="Actions for api").click()
    page.get_by_role("menuitem", name="Setup script…").click()
    dialog = page.get_by_role("dialog")
    expect(dialog).to_contain_text("Setup script for api")
    expect(dialog).to_contain_text("Time limit: 15 minutes.")
    expect(dialog.get_by_test_id("size-meter")).to_have_text("0 B of 16 KB")
    # The placeholder is guidance only: nothing to save yet.
    expect(dialog.locator(".cm-placeholder")).to_contain_text("git rev-parse --git-path hooks")
    expect(dialog.get_by_test_id("setup-script-save")).to_be_disabled()

    _type_script(page, SCRIPT)
    expect(dialog.get_by_test_id("setup-script-lines")).to_have_text("2 lines")
    shell_string = '"$(git rev-parse --git-path hooks)/pre-commit"'
    expect(dialog.locator(".cm-content span").filter(has_text=shell_string)).to_have_count(1)
    dialog.get_by_test_id("setup-script-save").click()
    expect(toast(page, "api’s setup script saved")).to_be_visible()
    expect(page.get_by_role("dialog")).to_have_count(0)
    assert _repository(client, project, "api")["setupScript"] == SCRIPT

    mark = api_row.get_by_role("button", name="Edit api’s setup script, 2 lines")
    expect(mark).to_be_visible()
    expect(settings.get_by_test_id("repository-row-web").get_by_test_id("setup-script-mark")).to_have_count(0)

    # A bypass is warned about, never refused.
    mark.click()
    dialog = page.get_by_role("dialog")
    expect(dialog.locator(".cm-content")).to_contain_text("npm ci --prefer-offline")
    _type_script(page, "git config core.hooksPath scripts/hooks")
    expect(dialog.get_by_test_id("setup-script-bypass-count")).to_have_text("1 line bypasses hooks")
    expect(dialog.get_by_test_id("setup-script-save")).to_be_enabled()

    # Cleared, it is removed.
    _type_script(page, "")
    save = dialog.get_by_test_id("setup-script-save")
    expect(save).to_have_text("Remove script")
    save.click()
    expect(toast(page, "api’s setup script removed")).to_be_visible()
    expect(settings.get_by_test_id("setup-script-mark")).to_have_count(0)
    assert _repository(client, project, "api")["setupScript"] is None
    assert console_errors == []
def test_editor_scrolls_after_max_lines(page: Page, web_url: str, client: ApiClient, org: dict, console_errors: list):
    project = _project_with_repositories(client)
    sign_in(page, web_url, org["api_key"], at=f"#/project/{project['id']}/settings/repositories")
    settings = page.get_by_test_id("project-settings")
    settings.get_by_role("button", name="Actions for api").click()
    page.get_by_role("menuitem", name="Setup script…").click()
    dialog = page.get_by_role("dialog")
    _type_script(page, "\n".join(f"echo {i}" for i in range(40)))
    scroller = dialog.locator(".cm-scroller")
    expect(scroller).to_have_count(1)
    assert scroller.evaluate("el => el.scrollHeight > el.clientHeight")
    assert console_errors == []
@pytest.mark.ui
def test_over_the_limit_save_is_disabled(page: Page, web_url: str, client: ApiClient, org: dict, console_errors: list):
    project = _project_with_repositories(client)
    sign_in(page, web_url, org["api_key"], at=f"#/project/{project['id']}/settings/repositories")
    settings = page.get_by_test_id("project-settings")
    settings.get_by_role("button", name="Actions for web").click()
    page.get_by_role("menuitem", name="Setup script…").click()
    dialog = page.get_by_role("dialog")
    _type_script(page, "#" * 16_385)
    expect(dialog.get_by_test_id("size-meter")).to_have_attribute("data-level", "danger")
    expect(dialog.get_by_test_id("setup-script-invalid")).to_contain_text("over the 16 KB limit")
    expect(dialog.get_by_test_id("setup-script-save")).to_be_disabled()
    assert _repository(client, project, "web")["setupScript"] is None
    assert console_errors == []


@pytest.mark.ui
def test_a_member_reads_the_script_read_only(page: Page, web_url: str, client: ApiClient, org: dict, env,
                                             console_errors: list):
    project = _project_with_repositories(client)
    api = _repository(client, project, "api")
    assert client.patch(f"/v1/repositories/{api['id']}", {"setupScript": SCRIPT}).status_code == 200
    _, member = provision_person(client, env, "Bo", f"bo-{os.urandom(2).hex()}@acme.dev")
    sign_in(page, web_url, member.api_key, at=f"#/project/{project['id']}/settings/repositories")
    settings = page.get_by_test_id("project-settings")
    expect(settings.get_by_test_id("repository-row-api")).to_be_visible()
    expect(settings.get_by_role("button", name="Actions for api")).to_have_count(0)

    settings.get_by_role("button", name="Read api’s setup script, 2 lines").click()
    dialog = page.get_by_role("dialog")
    expect(dialog).to_contain_text("read-only")
    content = dialog.locator(".cm-content")
    expect(content).to_contain_text("npm ci --prefer-offline")
    expect(content).to_have_attribute("contenteditable", "false")
    expect(dialog.get_by_test_id("setup-script-save")).to_have_count(0)
    dialog.get_by_test_id("setup-script-close").click()
    expect(page.get_by_role("dialog")).to_have_count(0)
    assert _repository(client, project, "api")["setupScript"] == SCRIPT
    assert console_errors == []
