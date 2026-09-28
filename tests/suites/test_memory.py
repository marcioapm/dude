"""Memory: what people, dude and agents remember, and one search over it.

Driven through the public API as the settings pages use it, and in a
browser as a person does. The suite's orchestrator has no embedder
(DUDE_EMBEDDINGS_URL is unset), so search here is by words alone and says
so; search by meaning is the orchestrator's own tests', with a fake.
"""

from __future__ import annotations

import pytest
from playwright.sync_api import Page, expect

from helpers import ApiClient, create_api_key, execute, query, sign_in, toast


def test_a_memory_is_found_listed_edited_and_archived(client: ApiClient, project: dict):
    added = client.post("/v1/memory/memories", {
        "projectId": project["id"], "title": "Run the tests against a throwaway database",
        "content": "docker run postgres, migrate, then bun test.", "kind": "procedure",
    })
    assert added.status_code == 201, added.text
    memory = added.json()
    assert memory["author"]["kind"] == "person" and memory["index"] == "waiting"

    found = client.get("/v1/memory/search", params={"q": "throwaway database tests", "project": project["id"]}).json()
    assert found["mode"] == "words"
    assert [r["id"] for r in found["results"]][:1] == [memory["id"]]
    assert found["results"][0]["textRank"] == 1 and found["results"][0]["vectorRank"] == 0

    edited = client.patch(f"/v1/memory/memories/{memory['id']}", {"title": "Run tests on a throwaway database"})
    assert edited.status_code == 200 and edited.json()["title"] == "Run tests on a throwaway database"

    assert client.post(f"/v1/memory/memories/{memory['id']}/archive").status_code == 200
    assert client.get("/v1/memory/memories", params={"project": project["id"]}).json()["memories"] == []
    found = client.get("/v1/memory/search", params={"q": "throwaway database", "project": project["id"]}).json()
    assert memory["id"] not in [r["id"] for r in found["results"]]


def test_tasks_epics_and_projects_are_searchable_as_they_are_written(client: ApiClient, project: dict):
    task = client.create_task(project["id"], "Dedupe webhook deliveries", goal="A redelivery is processed once.")
    found = client.get("/v1/memory/search", params={"q": "redelivery", "project": project["id"]}).json()["results"]
    assert found and found[0]["id"] == task["id"] and found[0]["key"] and found[0]["status"]


def test_only_the_author_or_an_admin_changes_a_memory(client: ApiClient, org: dict, env, project: dict):
    memory = client.post("/v1/memory/memories", {"title": "Commit messages are prose", "content": "No feat: prefixes."}).json()
    # A member of the same organization, not an admin.
    member = ApiClient(env.control_plane_url, create_api_key(env.owner_dsn, org["id"], name="member"))
    execute(env.owner_dsn, "UPDATE people SET role = 'member' WHERE id = (SELECT person_id FROM api_keys WHERE name = 'member' AND organization_id = %s)", (org["id"],))
    assert member.patch(f"/v1/memory/memories/{memory['id']}", {"title": "Mine now"}).status_code == 403
    assert member.post(f"/v1/memory/memories/{memory['id']}/archive").status_code == 403
    assert member.post("/v1/memory/index/reindex").status_code == 403
    # They add their own, and change it.
    theirs = member.post("/v1/memory/memories", {"title": "Mine", "content": "x"}).json()
    assert member.patch(f"/v1/memory/memories/{theirs['id']}", {"content": "y"}).status_code == 200


def test_another_organizations_memory_is_not_found(client: ApiClient, second_org: dict, project: dict):
    memory = client.post("/v1/memory/memories", {"title": "Acme's secret", "content": "only for acme"}).json()
    other = second_org["client"]
    assert other.get(f"/v1/memory/memories/{memory['id']}").status_code == 404
    assert other.get("/v1/memory/search", params={"q": "secret"}).json()["results"] == []


def test_the_index_says_what_is_waiting_without_an_embedder(client: ApiClient, project: dict):
    client.post("/v1/memory/memories", {"title": "Something", "content": "worth knowing"})
    status = client.get("/v1/memory/index").json()
    assert "model" not in status
    memories = next(k for k in status["kinds"] if k["type"] == "memory")
    assert memories["total"] >= 1 and memories["embedded"] == 0


@pytest.mark.ui
def test_memory_is_searched_and_added_from_the_settings(page: Page, web_url: str, client: ApiClient, org: dict, project: dict, console_errors: list):
    client.create_task(project["id"], "Dedupe webhook deliveries", goal="A redelivery is processed once.")
    sign_in(page, web_url, org["api_key"])
    page.get_by_test_id("org-settings-button").click()
    page.locator("[data-settings-nav='memory']").click()
    # The group opens on its first page: Search.
    expect(page.locator("[data-settings-nav='memory-search']")).to_have_attribute("aria-current", "page")
    page.get_by_test_id("memory-query").fill("redelivery")
    results = page.get_by_test_id("memory-results")
    expect(results).to_contain_text("Dedupe webhook deliveries")
    expect(results).to_contain_text("words only")
    expect(page.get_by_text("Searched by words only")).to_be_visible()

    page.locator("[data-settings-nav='memory-list']").click()
    page.get_by_test_id("memory-add").click()
    page.get_by_test_id("memory-title").fill("GitHub redelivers a webhook for 3 days")
    page.get_by_test_id("memory-content").fill("Dedupe on X-GitHub-Delivery.")
    page.get_by_test_id("memory-save").click()
    expect(toast(page, "Memory added")).to_be_visible()
    expect(page.get_by_test_id("memories")).to_contain_text("GitHub redelivers a webhook for 3 days")
    assert console_errors == []
