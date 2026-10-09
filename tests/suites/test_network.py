"""The agent network, through the public API against the deployed backend,
orchestrator and fake lux: what an organisation and a project list is what
a Run's spec allows, the names lux refused its agent are listed with Allow,
and the Network pages change both.

The fake lux validates network.egress as lux does (wildcards included) and
plays fakeagent's lookup model's lookups as dns events, decided by the
Run's own network.
"""

from __future__ import annotations

import pytest
import requests
from playwright.sync_api import Page, expect

from helpers import ApiClient, query, sign_in, toast, wait_until

# The suite's orchestrator's model (env.py DUDE_LLM_URL): always reachable.
LLM_HOST = "llm.dude.test"


def _implementer_on(client: ApiClient, project: dict, model: str) -> None:
    client.patch(f"/v1/projects/{project['id']}", {"agentModels": client.on_models({
        "implementer": model, "reviewer": "fake/hang", "simplifier": "fake/scripted"})})


def _first_spec(env, client: ApiClient, project: dict, owner_dsn: str) -> tuple[dict, dict]:
    """Delivers a task, and answers its implementer's Run and the spec dude submitted it with."""
    task = client.create_task(project["id"], "Install the dependencies")
    assert client.post(f"/v1/tasks/{task['id']}/deliver").status_code == 201
    run = wait_until(lambda: next((r for r in client.task_runs(task["id"]) if r["phase"] == "implement"), None),
                     timeout=60, message="no implementer")
    lux_run = wait_until(lambda: query(owner_dsn, "SELECT lux_run_id FROM runs WHERE id = %s", (run["id"],))[0]["lux_run_id"],
                         timeout=60, message="never submitted")
    res = requests.get(f"{env.lux_url}/v1/runs/{lux_run}", headers={"authorization": f"Bearer {env.lux_key}"}, timeout=10)
    res.raise_for_status()
    return run, res.json()["spec"]


def _rules(spec: dict) -> list[str]:
    return sorted(r.get("host") or r["cidr"] for r in spec["network"].get("egress", []))


def test_an_organisations_list_is_what_its_runs_may_reach(env, client: ApiClient, forge_project: dict, owner_dsn: str):
    saved = client.patch("/v1/settings/organization", {"network": {"egress": ["GitHub.com", "*.github.com", "10.60.0.0/16", "pypi.org"]}})
    assert saved.status_code == 200, saved.text
    network = saved.json()["network"]
    assert network["egress"]["value"] == ["github.com", "*.github.com", "10.60.0.0/16", "pypi.org"]
    assert network["always"] == [LLM_HOST, "dude’s tools"]
    _implementer_on(client, forge_project, "llm-impl")
    _, spec = _first_spec(env, client, forge_project, owner_dsn)
    rules = _rules(spec)
    # The tools' address is the suite's own (127.0.0.1): always there too.
    assert [r for r in rules if not r.startswith("127.")] == sorted(["*.github.com", "10.60.0.0/16", "github.com", LLM_HOST, "pypi.org"]), rules
    assert not spec["network"].get("unrestricted")


def test_a_project_adds_to_its_organisations_list_or_runs_on_its_own(env, client: ApiClient, forge_project: dict, owner_dsn: str):
    client.patch("/v1/settings/organization", {"network": {"egress": ["github.com"]}})
    pid = forge_project["id"]
    added = client.patch(f"/v1/projects/{pid}/settings", {"network": {"egress": ["registry.npmjs.org"]}})
    assert added.status_code == 200, added.text
    assert added.json()["network"]["effective"] == ["github.com", "registry.npmjs.org"]
    _implementer_on(client, forge_project, "llm-impl")
    _, spec = _first_spec(env, client, forge_project, owner_dsn)
    assert {"github.com", "registry.npmjs.org", LLM_HOST} <= set(_rules(spec))

    only = client.patch(f"/v1/projects/{pid}/settings", {"network": {"mode": "only"}}).json()["network"]
    assert only["mode"] == {"value": "only", "source": "project"} and only["effective"] == ["registry.npmjs.org"]
    _, spec = _first_spec(env, client, forge_project, owner_dsn)
    rules = _rules(spec)
    assert "registry.npmjs.org" in rules and LLM_HOST in rules and "github.com" not in rules, rules


def test_anywhere_on_the_organisation_is_unrestricted(env, client: ApiClient, forge_project: dict, owner_dsn: str):
    assert client.patch("/v1/settings/organization", {"network": {"egress": ["*", "github.com"]}}).status_code == 200
    _implementer_on(client, forge_project, "llm-impl")
    _, spec = _first_spec(env, client, forge_project, owner_dsn)
    assert spec["network"] == {"unrestricted": True}


def test_what_lux_would_refuse_is_refused(client: ApiClient, project: dict):
    for entry in ["*.com", "a.*.example.com", "*.example.com:443"]:
        res = client.patch("/v1/settings/organization", {"network": {"egress": ["pypi.org", entry]}})
        assert res.status_code == 400 and entry in res.text, res.text


def test_a_refused_lookup_is_listed_and_allow_clears_it(env, client: ApiClient, forge_project: dict, owner_dsn: str, second_org: dict):
    pid = forge_project["id"]
    # api.github.com is under the organisation's wildcard; the others are on no list.
    client.patch("/v1/settings/organization", {"network": {"egress": ["*.github.com"]}})
    _implementer_on(client, forge_project, "fake/lookup")
    run, _ = _first_spec(env, client, forge_project, owner_dsn)
    # Both names, which the follow loop may commit in separate batches.
    refused = wait_until(lambda: (p := [e["payload"] for e in client.events(runId=run["id"]) if e["eventType"] == "agent.network.refused"])
                         and len(p) >= 2 and p, timeout=60, message="not both names refused")
    assert sorted(p["name"] for p in refused) == ["files.pythonhosted.org", "registry.npmjs.org"]
    assert all(p["role"] == "implementer" for p in refused)

    listed = client.get(f"/v1/projects/{pid}/network/refused?days=7").json()["refused"]
    assert sorted((r["name"], r["runs"], r["roles"]) for r in listed) == [
        ("files.pythonhosted.org", 1, ["implementer"]), ("registry.npmjs.org", 1, ["implementer"])]
    assert second_org["client"].get(f"/v1/projects/{pid}/network/refused").status_code == 404

    allowed = client.post(f"/v1/projects/{pid}/network/allow", {"names": ["files.pythonhosted.org", "registry.npmjs.org"]})
    assert allowed.status_code == 200, allowed.text
    assert allowed.json()["network"]["egress"]["value"] == ["files.pythonhosted.org", "registry.npmjs.org"]
    assert client.get(f"/v1/projects/{pid}/network/refused").json()["refused"] == []


@pytest.mark.ui
def test_the_network_pages_save_hosts_and_show_where_each_comes_from(
    page: Page, web_url: str, client: ApiClient, org: dict, project: dict, console_errors: list
):
    pid = project["id"]
    sign_in(page, web_url, org["api_key"], at="#/org/settings/network")
    network = page.get_by_test_id("network-page")
    expect(network).to_be_visible()
    expect(page.get_by_test_id("network-empty")).to_contain_text("Agents can reach only their model.")
    field = network.get_by_label("Add a host agents may reach")
    field.fill("mirror.internal")
    field.press("Enter")
    expect(toast(page, "Network saved")).to_be_visible()
    network.locator("[data-preset='GitHub']").click()
    expect(toast(page, "GitHub added")).to_be_visible()
    expect(network.locator("[data-preset='GitHub']")).to_have_attribute("data-has", "true")
    assert client.get("/v1/settings/organization").json()["network"]["egress"]["value"] == [
        "mirror.internal", "github.com", "*.github.com", "objects.githubusercontent.com"]

    # The project: the organisation's hosts read-only, its own removable.
    page.goto(f"{web_url}#/project/{pid}/settings/network")
    network = page.get_by_test_id("network-page")
    org_hosts = network.get_by_test_id("network-org-egress")
    expect(org_hosts.locator("[data-host]")).to_have_count(4)
    expect(org_hosts.get_by_role("button")).to_have_count(0)
    expect(network.get_by_test_id("network-from")).to_contain_text("From ")
    own = network.get_by_label(f"Add a host {project['name']} may reach")
    own.fill("pypi.org")
    own.press("Enter")
    expect(network.get_by_role("button", name="Remove pypi.org")).to_be_visible()

    # Only its own list: an override, with Reset.
    expect(network.locator("[data-source='project']")).to_have_count(0)
    network.get_by_test_id("network-only-switch").click()
    overridden = network.locator("[data-source='project']")
    expect(overridden).to_contain_text("Overridden")
    expect(overridden).to_contain_text("4 hosts")
    assert client.get(f"/v1/projects/{pid}/settings").json()["network"]["effective"] == ["pypi.org"]
    overridden.get_by_role("button", name="Reset").click()
    expect(network.locator("[data-source='project']")).to_have_count(0)
    expect(network.get_by_test_id("network-org-egress")).to_be_visible()
    assert client.get(f"/v1/projects/{pid}/settings").json()["network"]["egress"]["value"] == []
    assert console_errors == []
