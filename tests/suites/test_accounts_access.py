"""Accounts in a browser, behind Cloudflare Access.

These run against the Access-mode backend (conftest `access`): every
request is signed in by a CF_Authorization token that a local stand-in for
the Access team signed for the email the test names, verified against that
team's published keys, and admitted by the sign-in gate. Nothing here uses
an API key: the people are emails, as in production.

The flows are docs/design/accounts.md's: the super-admin makes an
organisation and gets its first admin's link; that admin joins and invites
two people with the chip editor; an invitee joins; another email sees only
the generic mismatch; an uninvited email sees No organisation; a person in
two organisations switches between them, each with its own address; and
keys are made per organisation.
"""

from __future__ import annotations

import os
import re

import pytest
import requests
from playwright.sync_api import Page, expect

from conftest import ACCESS_OPERATOR
from helpers import query

pytestmark = pytest.mark.ui

CODE = re.compile(r"#/invite/([0-9A-Za-z]{22})")


def _suffix() -> str:
    return os.urandom(3).hex()


def _organisation(access, name: str, slug: str, first_admin: str) -> dict:
    """An organisation made by the operator through the super-admin's API; returns it and the first admin's code."""
    resp = access.session(ACCESS_OPERATOR).post("/v1/admin/organizations", json={"name": name, "slug": slug, "email": first_admin})
    assert resp.status_code == 201, resp.text
    body = resp.json()
    return {**body["organization"], "code": body["invitation"]["code"]}


def _join(access, email: str, code: str, name: str | None = None) -> dict:
    resp = access.session(email, name).post(f"/v1/invitations/{code}/accept", json={})
    assert resp.status_code == 200, resp.text
    return resp.json()


def _not_loading_errors(errors: list[str]) -> list[str]:
    """Console errors, leaving out the browser's own line for a response the test expects to be refused."""
    return [e for e in errors if "Failed to load resource" not in e]


def test_the_operator_creates_an_organisation_and_gets_its_first_admins_link(page: Page, access, owner_dsn: str, console_errors: list):
    slug = f"initech-{_suffix()}"
    admin = f"bill-{slug}@initech.test"
    access.sign_in(page, ACCESS_OPERATOR)
    # A super-admin with no membership lands on this dude's organisations.
    expect(page.get_by_test_id("admin")).to_be_visible()
    expect(page).to_have_url(re.compile(r"#/admin$"))
    expect(page.get_by_test_id("admin")).to_contain_text("Outside every organisation")

    page.get_by_test_id("create-org").click()
    page.get_by_test_id("create-name").fill("Initech")
    page.get_by_test_id("create-slug").fill(slug)
    page.get_by_test_id("create-email").fill(admin)
    page.get_by_test_id("create-submit").click()
    link = page.get_by_test_id("create-link")
    expect(link).to_contain_text(admin)
    url = link.get_by_test_id("invitation-url").text_content() or ""
    code = CODE.search(url).group(1)
    assert url == f"{access.url}/#/invite/{code}"
    page.get_by_test_id("create-done").click()
    expect(page.locator(f"[data-testid=admin-org][data-slug='{slug}']")).to_contain_text("No admin yet")

    # Made with an admin invitation for that address, from the operator.
    rows = query(owner_dsn, "SELECT i.email::text AS email, i.role, i.invited_by FROM invitations i JOIN organizations o ON o.id = i.organization_id WHERE o.slug = %s", (slug,))
    assert rows == [{"email": admin, "role": "admin", "invited_by": None}]
    assert console_errors == []


def test_the_first_admin_joins_and_invites_two_people_with_the_chip_editor(page: Page, access, owner_dsn: str, console_errors: list):
    slug = f"initech-{_suffix()}"
    admin = f"bill-{slug}@initech.test"
    org = _organisation(access, "Initech", slug, admin)

    access.sign_in(page, admin, "Bill Lumbergh", at=f"/#/invite/{org['code']}")
    first = page.get_by_test_id("accept-first")
    expect(first.get_by_role("heading", level=1)).to_have_text("Join Initech as admin")
    expect(first).to_contain_text("You aren't in any organisation on this dude yet. Join makes your account")
    page.get_by_test_id("accept-join-button").click()
    expect(page).to_have_url(re.compile(rf"#/o/{slug}/welcome$"))
    expect(page.get_by_test_id("first-one").get_by_role("heading", level=1)).to_have_text("Welcome to Initech, Bill")

    page.get_by_test_id("org-settings-button").click()
    expect(page).to_have_url(re.compile(rf"#/o/{slug}/org/settings"))
    members = page.get_by_test_id("members")
    expect(members).to_contain_text("People join Initech by invitation.")
    expect(members).not_to_contain_text("not available yet")
    page.get_by_test_id("invite").click()
    chips = page.get_by_role("textbox", name="Emails")
    chips.fill(f"Peter Gibbons <peter-{slug}@initech.test>")
    chips.press("Enter")
    chips.fill(f"michael-{slug}@initech.test")
    chips.press(",")
    chips.fill(admin)
    chips.press("Enter")
    chips.fill("not-an-email")
    chips.press("Enter")
    expect(page.get_by_test_id("chip-summary")).to_have_text(
        "2 new · 1 not an email · 1 already in Initech. The other 2 are left out.")
    page.get_by_test_id("invite-submit").click()
    links = page.get_by_test_id("invite-links")
    expect(page.get_by_role("dialog").get_by_role("heading", level=2)).to_have_text("2 invitations to Initech")
    expect(links.locator("li")).to_have_count(2)
    expect(links).to_contain_text("Peter Gibbons")
    urls = links.get_by_test_id("invitation-url").all_text_contents()
    assert all(u.startswith(f"{access.url}/#/invite/") for u in urls), urls

    stored = query(owner_dsn, "SELECT i.email::text AS email, i.name, i.code, p.name AS inviter FROM invitations i "
                   "JOIN organizations o ON o.id = i.organization_id JOIN people p ON p.id = i.invited_by "
                   "WHERE o.slug = %s AND i.accepted_at IS NULL ORDER BY i.email", (slug,))
    assert [(r["email"], r["name"], r["inviter"]) for r in stored] == [
        (f"michael-{slug}@initech.test", None, "Bill Lumbergh"), (f"peter-{slug}@initech.test", "Peter Gibbons", "Bill Lumbergh")]
    assert sorted(u.rsplit("/", 1)[1] for u in urls) == sorted(r["code"] for r in stored)

    # The Invitations page lists them, pending.
    page.get_by_test_id("invite-done").click()
    page.locator("[data-settings-nav=invitations]").click()
    expect(page.locator("[data-testid=invitation-row][data-status=pending]")).to_have_count(2)
    assert console_errors == []


def test_an_invitee_joins_and_lands_in_the_organisation(page: Page, access, owner_dsn: str, console_errors: list):
    slug = f"initech-{_suffix()}"
    admin = f"bill-{slug}@initech.test"
    org = _organisation(access, "Initech", slug, admin)
    _join(access, admin, org["code"], "Bill Lumbergh")
    peter = f"peter-{slug}@initech.test"
    invited = access.session(admin).post("/v1/organization/invitations", headers={"x-dude-organization": org["id"]},
                                         json={"invitations": [{"email": peter, "name": "Peter Gibbons"}], "role": "member"}).json()
    code = invited["results"][0]["invitation"]["code"]

    access.sign_in(page, peter, "Peter Gibbons", at=f"/#/invite/{code}")
    first = page.get_by_test_id("accept-first")
    expect(first).to_contain_text("Invited by Bill Lumbergh")
    expect(first).to_contain_text(f"{peter} · you")
    page.get_by_test_id("accept-join-button").click()
    expect(page).to_have_url(re.compile(rf"#/o/{slug}/welcome$"))
    expect(page.get_by_test_id("first-one")).to_contain_text("You joined from Bill Lumbergh's invitation link, as a member.")
    expect(page.get_by_test_id("switcher")).to_contain_text("Initech · Member")

    # The membership is made, and its account; the invitation is accepted into it.
    rows = query(owner_dsn, "SELECT p.role, a.name FROM people p JOIN accounts a ON a.id = p.account_id "
                 "WHERE p.organization_id = %s AND a.email = %s AND p.removed_at IS NULL", (org["id"], peter))
    assert rows == [{"role": "member", "name": "Peter Gibbons"}]
    assert query(owner_dsn, "SELECT accepted_at IS NOT NULL AS done FROM invitations WHERE code = %s", (code,)) == [{"done": True}]
    # Opening the link again: already in.
    page.goto(f"{access.url}/#/invite/{code}")
    expect(page.get_by_test_id("accept-member").get_by_role("heading", level=1)).to_have_text("You're already in Initech")
    assert console_errors == []


def test_a_link_opened_with_another_email_shows_only_the_generic_page_and_accepts_nothing(page: Page, access, owner_dsn: str, console_errors: list):
    slug = f"initech-{_suffix()}"
    admin = f"bill-{slug}@initech.test"
    org = _organisation(access, "Initech", slug, admin)
    _join(access, admin, org["code"])
    invited = access.session(admin).post("/v1/organization/invitations", headers={"x-dude-organization": org["id"]},
                                         json={"invitations": [{"email": f"michael-{slug}@initech.test"}], "role": "member"}).json()
    code = invited["results"][0]["invitation"]["code"]

    other = f"samir-{_suffix()}@elsewhere.test"
    access.sign_in(page, other, at=f"/#/invite/{code}")
    wrong = page.get_by_test_id("accept-wrong")
    expect(wrong.get_by_role("heading", level=1)).to_have_text("This invitation is for a different email address.")
    expect(wrong).to_contain_text(f"Signed in as {other} with Cloudflare Access.")
    for secret in ("Initech", "Bill", "michael", "admin", "member", slug):
        expect(wrong).not_to_contain_text(secret)
    expect(page.get_by_test_id("accept-join-button")).to_have_count(0)

    # Its one action signs out of Access; nothing is accepted as the wrong person.
    accepts: list[str] = []
    page.on("request", lambda r: accepts.append(r.url) if r.method == "POST" and "/accept" in r.url else None)
    page.get_by_test_id("switch-account").click()
    page.wait_for_url(re.compile(r"/$|/#"))
    assert accepts == []
    assert not any(c["name"] == "CF_Authorization" and c["value"] for c in page.context.cookies())
    assert query(owner_dsn, "SELECT accepted_at FROM invitations WHERE code = %s", (code,)) == [{"accepted_at": None}]
    assert query(owner_dsn, "SELECT count(*)::int AS n FROM accounts WHERE email = %s", (other,)) == [{"n": 0}]
    assert _not_loading_errors(console_errors) == []


def test_an_uninvited_email_sees_no_organisation_and_nothing_is_recorded(page: Page, access, owner_dsn: str, console_errors: list):
    slug = f"initech-{_suffix()}"
    _organisation(access, "Initech", slug, f"bill-{slug}@initech.test")
    stranger = f"stranger-{_suffix()}@elsewhere.test"
    access.sign_in(page, stranger)
    none = page.get_by_test_id("no-org")
    expect(none.get_by_role("heading", level=1)).to_have_text("Your email isn't in any organisation on this dude")
    expect(none).to_contain_text(f"You signed in as {stranger}.")
    for secret in ("Initech", "operator", ACCESS_OPERATOR, slug, "super"):
        expect(none).not_to_contain_text(secret)
    page.get_by_test_id("check-again").click()
    expect(none).to_be_visible()
    # Every other route is refused before it reads anything, whatever it names.
    s = access.session(stranger)
    assert s.get("/v1/navigation").status_code == 403
    assert s.get("/v1/admin/organizations").json()["error"]["code"] in ("not_admitted", "super_admin_required")
    assert query(owner_dsn, "SELECT count(*)::int AS n FROM accounts WHERE email = %s", (stranger,)) == [{"n": 0}]
    assert console_errors == []


def test_a_person_in_two_organisations_switches_and_each_tab_keeps_its_own(page: Page, access, console_errors: list):
    s = _suffix()
    person = f"ana-{s}@both.test"
    initech = _organisation(access, "Initech", f"initech-{s}", person)
    northwind = _organisation(access, "Northwind", f"northwind-{s}", person)
    _join(access, person, initech["code"], "Ana Ribeiro")
    _join(access, person, northwind["code"])

    sent: list[tuple[str, str | None]] = []
    page.on("request", lambda r: sent.append((r.url, r.headers.get("x-dude-organization"))) if "/v1/navigation" in r.url or "/v1/events/stream" in r.url else None)
    access.sign_in(page, person, "Ana Ribeiro", at=f"/#/o/{initech['slug']}/")
    switcher = page.get_by_test_id("switcher")
    expect(switcher).to_contain_text("Initech · Admin")

    switcher.click()
    menu = page.get_by_role("menu")
    expect(menu.get_by_role("menuitemradio")).to_have_count(2)
    expect(menu.get_by_role("menuitemradio", name=re.compile("^Initech"))).to_have_attribute("aria-checked", "true")
    menu.get_by_role("menuitemradio", name=re.compile("^Northwind")).click()
    expect(page).to_have_url(re.compile(rf"#/o/{northwind['slug']}/$"))
    expect(page.get_by_test_id("switcher")).to_contain_text("Northwind · Admin")
    page.wait_for_timeout(300)
    nav = [h for u, h in sent if "/v1/navigation" in u]
    assert nav[0] == initech["id"] and nav[-1] == northwind["id"], sent
    streams = [u for u, _ in sent if "/v1/events/stream" in u]
    assert any(f"org={northwind['id']}" in u for u in streams), streams

    # A second tab in Initech: each keeps its own organisation and address.
    other = page.context.new_page()
    other.goto(f"{access.url}/#/o/{initech['slug']}/")
    expect(other.get_by_test_id("switcher")).to_contain_text("Initech · Admin")
    page.reload()
    expect(page.get_by_test_id("switcher")).to_contain_text("Northwind · Admin")
    assert page.url != other.url
    assert page.url.endswith(f"#/o/{northwind['slug']}/") and other.url.endswith(f"#/o/{initech['slug']}/")
    other.close()
    assert console_errors == []


def test_keys_are_made_per_organisation(page: Page, access, console_errors: list):
    s = _suffix()
    person = f"cy-{s}@both.test"
    initech = _organisation(access, "Initech", f"initech-{s}", person)
    northwind = _organisation(access, "Northwind", f"northwind-{s}", person)
    _join(access, person, initech["code"], "Cy Okafor")
    _join(access, person, northwind["code"])

    access.sign_in(page, person, "Cy Okafor", at=f"/#/o/{initech['slug']}/me/settings")
    keys = page.get_by_test_id("keys")
    groups = keys.get_by_test_id("key-group")
    expect(groups).to_have_count(2)
    group = keys.locator(f"[data-testid=key-group][data-org='{northwind['slug']}']")
    expect(group).to_contain_text("No keys for Northwind yet.")
    group.get_by_test_id("key-new").click()
    dialog = page.get_by_role("dialog")
    expect(dialog.get_by_role("heading", level=2)).to_have_text("New key for Northwind")
    page.get_by_test_id("key-name").fill("Laptop")
    page.get_by_test_id("key-create").click()
    secret = page.get_by_test_id("key-secret").input_value()
    expect(page.get_by_test_id("key-usage")).to_contain_text(f'the CLI keeps it as "{northwind["slug"]}"')
    page.get_by_test_id("key-done").click()
    expect(group.get_by_test_id("key-row")).to_have_count(1)
    expect(keys.locator(f"[data-testid=key-group][data-org='{initech['slug']}'] [data-testid=key-row]")).to_have_count(0)

    # The key acts in Northwind, and only there.
    me = requests.get(f"{access.url}/v1/me", headers={"authorization": f"Bearer {secret}"}, timeout=10).json()
    assert me["organization"]["id"] == northwind["id"]
    refused = requests.get(f"{access.url}/v1/navigation", headers={"authorization": f"Bearer {secret}", "x-dude-organization": initech["id"]}, timeout=10)
    assert refused.status_code == 403
    assert console_errors == []
