"""The embedded guide through the real API and system Chrome, without a model."""

import os
from pathlib import Path
from urllib.parse import quote

import pytest
from playwright.sync_api import Page, expect

from helpers import ApiClient, open_help, sign_in

pytestmark = pytest.mark.ui


def test_help_search_sections_and_history(page: Page, web_url: str, client: ApiClient, console_errors: list):
    page.set_viewport_size({"width": 1920, "height": 1080})
    page.emulate_media(color_scheme="light")
    sign_in(page, web_url, client.api_key, board=False)
    with page.expect_response(lambda r: r.url.endswith("/v1/help") and r.request.method == "GET") as index_response:
        open_help(page)
    assert index_response.value.status == 200
    pages = index_response.value.json()["pages"]
    assert pages
    help_base = page.url.rstrip("/")
    expect(page.get_by_test_id("help-page")).to_have_attribute("data-page", "overview")
    search = page.get_by_role("combobox", name="Search the guide")
    with page.expect_response(lambda r: "/v1/memory/search?" in r.url) as search_response:
        search.fill("documentation reviewer")
    assert search_response.value.status == 200
    results = search_response.value.json()["results"]
    docs = [r for r in results if r["type"] == "doc"]
    assert docs, results
    first = docs[0]
    slug, _, anchor = first["id"].partition("#")
    assert anchor
    result = page.get_by_test_id("help-search-result").first
    expect(result).to_have_attribute("data-page", slug)
    expect(result).to_have_attribute("data-anchor", anchor)
    expect(result).to_contain_text(first["snippet"])
    search.press("Enter")
    target = f"{help_base}/{quote(slug)}/{quote(anchor)}"
    expect(page).to_have_url(target)
    article = page.get_by_test_id("help-page")
    expect(article).to_have_attribute("data-page", slug)
    section = article.locator(f'section[data-anchor="{anchor}"]')
    expect(section).to_be_in_viewport()
    search.press("Escape")
    expect(search).to_have_value("")
    outline = page.get_by_role("navigation", name="Guide pages")
    expect(outline).to_be_visible()
    expect(outline.locator(f'[data-anchor="{anchor}"]')).to_have_attribute("aria-current", "location")

    guide = client.get(f"/v1/help/{slug}")
    assert guide.status_code == 200
    sections = [s for s in guide.json()["sections"] if s["heading"]]
    last = sections[-1]
    previous = next(s for s in sections if s["anchor"] != last["anchor"])
    last_url = f'{help_base}/{quote(slug)}/{quote(last["anchor"])}'
    last_link = outline.locator(f'[data-anchor="{last["anchor"]}"]')
    last_heading = article.get_by_role("heading", name=last["heading"], exact=True)
    previous_link = outline.locator(f'[data-anchor="{previous["anchor"]}"]')
    page.goto(last_url)
    # A first load at the section, not the same document moving its hash.
    page.reload()
    expect(article).to_have_attribute("data-page", slug)
    expect(last_link).to_have_attribute("aria-current", "location")
    expect(last_heading).to_be_in_viewport()
    previous_link.click()
    expect(page).to_have_url(f'{help_base}/{quote(slug)}/{quote(previous["anchor"])}')
    expect(previous_link).to_have_attribute("aria-current", "location")
    page.go_back()
    expect(page).to_have_url(last_url)
    expect(last_link).to_have_attribute("aria-current", "location")
    expect(last_heading).to_be_in_viewport()
    assert console_errors == []
    print(f"Help API: {len(pages)} pages; {len(docs)} guide hits; first={slug}#{anchor}; last={last['anchor']}; console/page errors={console_errors}")
    if directory := os.environ.get("DUDE_HELP_ARTIFACT_DIR"):
        page.screenshot(path=str(Path(directory) / "help-light-1920x1080.png"))
