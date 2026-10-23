"""Automations end to end: the deployed control plane, orchestrator and fake lux.

An organisation automation on `push` is created through the API and turned
on; the fake GitHub delivers a signed push to dude's webhook receiver, which
stores it; the orchestrator's automations loop matches it and runs the
command, whose log carries the event. A written test event that matches is
accepted (202) and one the filters would skip is refused (422, `filtered`).
When the owner leaves the organisation, the automation is held by its admins
and stays on.
"""

from __future__ import annotations

import os

from helpers import ApiClient, provision_person, wait_until


def _runs(who: ApiClient, automation_id: str) -> list[dict]:
    resp = who.get(f"/v1/automations/{automation_id}/runs")
    assert resp.status_code == 200, resp.text
    return resp.json()["runs"]


def test_a_signed_push_runs_an_automation_and_a_leaving_owner_leaves_it_on(
    client: ApiClient, env, forge_project: dict, fake_github
):
    _, ana = provision_person(client, env, "Ana", f"ana-{os.urandom(3).hex()}@example.com", role="admin")
    bo_person, bo = provision_person(client, env, "Bo", f"bo-{os.urandom(3).hex()}@example.com", role="admin")
    ana_person = ana.get("/v1/me").json()["person"]

    created = ana.post("/v1/automations", {"name": "Push echo", "kind": "command", "trigger": "github"})
    assert created.status_code == 201, created.text
    automation = created.json()
    assert automation["owner"]["id"] == ana_person["id"] and automation["enabled"] is False
    settings = {
        "name": "Push echo",
        "kind": "command",
        "trigger": {"kind": "github", "event": "push", "actions": [], "filters": {"author": ["!dependabot[bot]"]}},
        "command": 'echo "event=$DUDE_EVENT_NAME delivery=$DUDE_DELIVERY_ID"',
    }
    saved = ana.put(f"/v1/automations/{automation['id']}", settings)
    assert saved.status_code == 200, saved.text
    on = ana.put(f"/v1/automations/{automation['id']}/enabled", {"enabled": True})
    assert on.status_code == 200 and on.json()["enabled"] is True, on.text

    # A signed push from the fake GitHub: stored (202, the orchestrator acts on it), matched, run.
    assert fake_github.send_webhook("push", {"ref": "refs/heads/main", "sender": {"login": "ana"}}) == 202
    run = wait_until(
        lambda: next((r for r in _runs(ana, automation["id"]) if r["status"] in ("succeeded", "failed")), None),
        timeout=90, message="the push's run never ended",
    )
    assert run["status"] == "succeeded", run
    assert run["source"] == "event"
    detail = ana.get(f"/v1/automations/{automation['id']}/runs/{run['id']}").json()
    assert "event=push delivery=" in (detail.get("logTail") or ""), detail

    # A written test event: matching, accepted; one the author filter skips, refused.
    accepted = ana.post(f"/v1/automations/{automation['id']}/runs", {"testEvent": {
        "event": "push", "deliveryId": "test-e2e-1",
        "body": {"ref": "refs/heads/main", "repository": {"full_name": "acme/web"}, "sender": {"login": "ana"}}}})
    assert accepted.status_code == 202, accepted.text
    assert accepted.json()["runId"].startswith("arn_")
    refused = ana.post(f"/v1/automations/{automation['id']}/runs", {"testEvent": {
        "event": "push", "deliveryId": "test-e2e-2",
        "body": {"ref": "refs/heads/main", "repository": {"full_name": "acme/web"}, "sender": {"login": "dependabot[bot]"}}}})
    assert refused.status_code == 422, refused.text
    assert refused.json()["error"]["code"] == "filtered"

    # The owner leaves: the admins hold it, and it stays on.
    removed = bo.delete(f"/v1/people/{ana_person['id']}")
    assert removed.status_code in (200, 204), removed.text
    held = bo.get(f"/v1/automations/{automation['id']}").json()
    assert held["ownerLeftAt"] is not None and held["enabled"] is True, held
    assert bo_person["id"] != ana_person["id"]
