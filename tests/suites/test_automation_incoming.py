"""Incoming webhooks end to end: the deployed control plane, orchestrator and fake lux.

A project's agent automation on its own URL, verified by a signature,
grouped by Grafana's `groupKey`, run only while `status is firing`, its
concurrency Queue. A Grafana-shaped firing alert POSTed with the right
HMAC starts a run whose agent is told the alert's values (quoted as text
from outside dude: the task it files carries them); the `resolved`
notification of the same group, which the condition alone would skip, is
sent to that run as a new input whatever the concurrency; another group's
alert gets a run of its own (queued behind the live one); a request with a
wrong signature is refused 401 and stored as a refused row with no body.
"""

from __future__ import annotations

import hashlib
import hmac
import json
import os

import requests

from helpers import ApiClient, query, wait_until


def _alert(status: str, service: str = "api") -> dict:
    return {
        "receiver": "dude",
        "status": status,
        "groupKey": f'{{}}/{{alertname="HighErrorRate"}}:{{service="{service}"}}',
        "commonLabels": {"alertname": "HighErrorRate", "service": service, "severity": "critical"},
        "externalURL": "https://grafana.example.com/d/api-errors",
        "alerts": [{"status": status, "labels": {"alertname": "HighErrorRate", "service": service}}],
    }


def _post(env, hook: str, body: dict, secret: str | None) -> requests.Response:
    raw = json.dumps(body).encode()
    headers = {"content-type": "application/json", "user-agent": "Grafana/12.2.0"}
    if secret is not None:
        headers["x-grafana-signature"] = "sha256=" + hmac.new(secret.encode(), raw, hashlib.sha256).hexdigest()
    return requests.post(f"{env.control_plane_url}/v1/hooks/{hook}", data=raw, headers=headers, timeout=10)


def _delivery(env, delivery_id: str) -> dict:
    rows = query(env.owner_dsn, "SELECT verdict, reason, group_key, run_id, payload, headers FROM incoming_deliveries WHERE id = %s",
                 (delivery_id,))
    return rows[0] if rows else {}


def _inputs(env, lux_run: str) -> list[str]:
    """What the run's agent was given, in order: its prompt, then each input."""
    res = requests.get(f"{env.lux_url}/v1/runs/{lux_run}/events", headers={"Authorization": f"Bearer {env.lux_key}"}, timeout=10)
    res.raise_for_status()
    out = []
    for e in res.json()["events"]:
        data = e["data"] if isinstance(e["data"], dict) else json.loads(e["data"])
        if e["type"] == "lux.input" and data.get("text"):
            out.append(data["text"])
    return out


def _lux_run(env, run_id: str) -> str:
    rows = query(env.owner_dsn, "SELECT lux_run_id FROM automation_runs WHERE id = %s", (run_id,))
    return (rows[0]["lux_run_id"] if rows else None) or ""


def test_an_alert_starts_a_run_its_resolution_reaches_it_and_a_bad_signature_is_refused(client: ApiClient, env, project: dict):
    tier = client.tier_for("fake/hang")
    created = client.post("/v1/automations", {"name": "Investigate the alert", "kind": "agent", "trigger": "incoming",
                                              "projectId": project["id"]})
    assert created.status_code == 201, created.text
    a = created.json()
    secret, hook = a["incomingSecret"], a["trigger"]["hookId"]
    assert a["incomingSecretHint"] == secret[-4:]
    assert "incomingSecret" not in client.get(f"/v1/automations/{a['id']}").json()
    settings = {
        "name": "Investigate the alert", "kind": "agent", "concurrency": "queue",
        "repos": "none",
        "trigger": {"kind": "incoming", "hookId": hook, "verify": {"mode": "signature", "header": "X-Grafana-Signature", "prefix": "sha256="},
                    "conditions": [{"path": "status", "op": "is", "values": ["firing"]}], "groupBy": "groupKey", "ratePerHour": 30},
        "agent": {"own": {"tierId": tier, "prompt": "\n".join([
            "Alert {{payload.commonLabels.alertname}} on {{payload.commonLabels.service}} is {{payload.status}}",
            # The scripted agent files this task: its title and goal carry the alert's values as substituted.
            'tool: create_task {"title": "Investigate {{payload.commonLabels.alertname}} on {{payload.commonLabels.service}}", '
            '"goal": "Find why {{payload.commonLabels.alertname}} is {{payload.status}} on {{payload.commonLabels.service}} and propose a fix."}',
        ])}},
    }
    saved = client.put(f"/v1/automations/{a['id']}", settings)
    assert saved.status_code == 200, saved.text
    assert client.put(f"/v1/automations/{a['id']}/enabled", {"enabled": True}).status_code == 200

    # A firing alert, signed: accepted at once, then started a run.
    fired = _post(env, hook, _alert("firing"), secret)
    assert fired.status_code == 202, fired.text
    first = fired.json()["delivery"]
    started = wait_until(lambda: (lambda d: d if d.get("verdict") else None)(_delivery(env, first)), timeout=60,
                         message="the firing delivery was never read")
    assert started["verdict"] == "started", started
    assert started["group_key"] == '{}/{alertname="HighErrorRate"}:{service="api"}'
    run_id = started["run_id"]
    lux_run = wait_until(lambda: _lux_run(env, run_id) or None, timeout=90, message="the run never reached lux")
    # The agent was told the alert's values, each quoted as text from outside dude.
    task = wait_until(lambda: next(iter(query(env.owner_dsn, "SELECT title, goal FROM tasks WHERE created_by_automation_run_id = %s",
                                              (run_id,))), None), timeout=90, message="the agent never filed its task")
    for text in (task["title"], task["goal"]):
        assert "HighErrorRate⟦/external " in text and "api⟦/external " in text and "⟦external " in text, task
    assert "firing⟦/external " in task["goal"], task

    # Its resolution, same group: "status is firing" does not hold, but a run of its group is going.
    resolved = _post(env, hook, _alert("resolved"), secret)
    assert resolved.status_code == 202, resolved.text
    sent = wait_until(lambda: (lambda d: d if d.get("verdict") else None)(_delivery(env, resolved.json()["delivery"])), timeout=60,
                      message="the resolved delivery was never read")
    assert sent["verdict"] == "sent_to_run" and sent["run_id"] == run_id, sent
    # The orchestrator gave it to the running agent as its next input.
    given = lambda: query(env.owner_dsn, "SELECT inputs_sent, jsonb_array_length(payloads) AS n FROM automation_runs WHERE id = %s", (run_id,))[0]
    wait_until(lambda: given()["inputs_sent"] >= 2, timeout=60,
               message=f"the running agent never heard the resolution: {given()} {_inputs(env, lux_run)}")

    # Another group's alert: a run of its own.
    other = _post(env, hook, _alert("firing", service="db"), secret)
    assert other.status_code == 202, other.text
    own = wait_until(lambda: (lambda d: d if d.get("verdict") else None)(_delivery(env, other.json()["delivery"])), timeout=60,
                     message="the other group's delivery was never read")
    assert own["run_id"] and own["run_id"] != run_id, own

    # A wrong signature: 401, a refused row with no body; the signature header kept as ••••.
    bad = _post(env, hook, _alert("firing"), "whsec_not-the-secret")
    assert bad.status_code == 401, bad.text
    assert bad.json()["error"]["code"] == "bad_signature"
    refused = _delivery(env, bad.json()["delivery"])
    assert refused["verdict"] == "refused" and refused["reason"] == "bad_signature" and refused["payload"] is None, refused
    assert refused["headers"].get("X-Grafana-Signature") == "••••", refused["headers"]

    # The Deliveries tab lists all four, newest first.
    listed = client.get(f"/v1/automations/{a['id']}/deliveries").json()["deliveries"]
    assert [d["verdict"] for d in listed[:4]] == ["refused", "started", "sent_to_run", "started"], listed
    # An unknown hook is not found and stores nothing.
    assert _post(env, "hk_" + os.urandom(8).hex(), _alert("firing"), secret).status_code == 404
