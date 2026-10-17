"""Judgements: the organisation's judgement tier, on its own settings page.

Off by default; an admin picks a tier, the choice persists, and the Models
page lists Judgements among that tier's users; a member reads it all and
changes nothing. The suite's LLM proxy (https://llm.dude.test/v1) answers
nothing, so the test question's answer here is its failure, recorded
nowhere. Nothing in dude asks for a judgement yet, so Recent judgements is
empty.
"""

from __future__ import annotations

import os
import re

import pytest
from playwright.sync_api import Page, expect

from helpers import ApiClient, provision_person, query, sign_in, toast


def _tiers(client: ApiClient) -> dict:
    return {t["name"]: t for t in client.get("/v1/models/tiers").json()["tiers"]}


def test_the_setting_is_an_admins_and_names_one_of_the_organisations_tiers(client: ApiClient, env, second_org: dict):
    assert client.get("/v1/judgements/settings").json() == {"tierId": None, "canEdit": True}
    theirs = {t["name"]: t for t in second_org["client"].get("/v1/models/tiers").json()["tiers"]}["Fast"]["id"]
    refused = client.put("/v1/judgements/settings", {"tierId": theirs})
    assert refused.status_code == 400, refused.text
    fast = _tiers(client)["Fast"]["id"]
    _, member = provision_person(client, env, "Bo", f"bo-{os.urandom(2).hex()}@acme.dev")
    assert member.put("/v1/judgements/settings", {"tierId": fast}).status_code == 403
    assert member.post("/v1/judgements/test", {"tierId": fast}).status_code == 403
    assert client.put("/v1/judgements/settings", {"tierId": fast}).json() == {"tierId": fast, "canEdit": True}
    assert member.get("/v1/judgements/settings").json() == {"tierId": fast, "canEdit": False}
    assert second_org["client"].get("/v1/judgements/settings").json()["tierId"] is None


@pytest.mark.ui
def test_an_admin_picks_the_judgement_tier_and_it_shows_on_models(
    page: Page, web_url: str, client: ApiClient, org: dict, owner_dsn: str, console_errors: list
):
    sign_in(page, web_url, org["api_key"], board=False)
    page.goto(f"{web_url}#/org/settings/judgements")
    judgements = page.get_by_test_id("judgements-page")
    nav = page.locator("[data-settings-nav='judgements']")
    expect(nav).to_have_text(re.compile(r"^Judgements$"))
    tier = judgements.get_by_test_id("judgement-tier")
    expect(tier).to_have_text("Off — nothing is judged")
    expect(judgements.get_by_test_id("judgements-off")).to_contain_text("Judgements are off.")
    expect(judgements.get_by_test_id("judgement-test")).to_have_count(0)
    expect(judgements.get_by_test_id("recent-judgements")).to_contain_text("Nothing has asked for a judgement yet")

    # Thinker requests a model (the fixture's); Fast names none yet, and says so.
    tier.click()
    page.get_by_role("option").filter(has_text=re.compile(r"^Fast")).click()
    expect(toast(page, "Judgement tier saved")).to_be_visible()
    expect(judgements.get_by_test_id("judgement-tier-no-model")).to_contain_text("Fast names no model")
    expect(nav).to_contain_text("no model")
    tier.click()
    page.get_by_role("option").filter(has_text=re.compile(r"^Thinker")).click()
    expect(judgements.get_by_test_id("judgement-tier-requests")).to_have_text(
        "Requests org-default-reviewer · high effort · through the proxy’s /v1/chat/completions")
    expect(nav).to_have_text(re.compile(r"^Judgements$"))
    thinker = _tiers(client)["Thinker"]
    assert client.get("/v1/judgements/settings").json()["tierId"] == thinker["id"]

    # The choice persists across a reload.
    page.reload()
    expect(page.get_by_test_id("judgement-tier")).to_have_text(re.compile(r"^Thinker"))

    # The suite's proxy answers nothing: the test question fails, and says it was not recorded.
    page.get_by_test_id("judgement-test").click()
    result = page.get_by_test_id("judgement-test-result")
    expect(result).to_have_attribute("data-ok", "false", timeout=45_000)
    expect(result).to_contain_text("gave no answer")
    expect(result).to_contain_text("Not recorded in Recent judgements.")
    assert query(owner_dsn, "SELECT count(*) AS n FROM model_calls WHERE organization_id = %s", (org["id"],))[0]["n"] == 0

    # The Models page lists Judgements among Thinker's users.
    page.locator("[data-settings-nav='models']").click()
    expect(page.get_by_test_id("models-page").locator("[data-tier='Thinker']")).to_contain_text("Judgements")
    expect(page.get_by_test_id("models-page").locator("[data-tier='Fast']")).not_to_contain_text("Judgements")
    assert {"kind": "judgements"} in _tiers(client)["Thinker"]["usedBy"]
    assert console_errors == []


@pytest.mark.ui
def test_a_member_sees_judgements_read_only(page: Page, web_url: str, client: ApiClient, env, console_errors: list):
    thinker = _tiers(client)["Thinker"]["id"]
    client.put("/v1/judgements/settings", {"tierId": thinker})
    _, member = provision_person(client, env, "Cy", f"cy-{os.urandom(2).hex()}@acme.dev")
    sign_in(page, web_url, member.api_key, board=False)
    page.goto(f"{web_url}#/org/settings/judgements")
    judgements = page.get_by_test_id("judgements-page")
    tier = judgements.get_by_test_id("judgement-tier")
    expect(tier).to_have_text(re.compile(r"^Thinker"))
    expect(tier).to_be_disabled()
    expect(judgements.get_by_test_id("judgement-tier-requests")).to_contain_text("org-default-reviewer")
    expect(judgements.get_by_test_id("judgement-test")).to_have_count(0)
    expect(judgements.get_by_test_id("recent-judgements")).to_be_visible()
    assert console_errors == []
