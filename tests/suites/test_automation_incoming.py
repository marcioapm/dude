"""Incoming webhooks end to end: the deployed control plane, orchestrator and fake lux.

A project's agent automation on its own URL, verified by a signature,
grouped by Grafana's `groupKey`, run only while `status is firing`, its
concurrency Queue. A Grafana-shaped firing alert POSTed with the right
HMAC starts a run whose agent is told the alert's values (quoted as text
from outside dude: the task it files carries them); the `resolved`
notification of the same group, which the condition alone would skip, is
sent to that run as a new input whatever the concurrency; another group's
alert gets a run of its own (queued behind the live one); a quiet group's
resolved alert is skipped by the condition; a request with a wrong
signature is refused 401 and stored as a refused row with no body or
headers; an unknown hook stores nothing.
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
    """The text of each input the run's agent was given after its prompt, from its lux output's lux.input records.

    The output streams while the run lives: what is there is read for a second, then the stream is dropped.
    """
    out: list[str] = []
    event = ""
    with requests.get(f"{env.lux_url}/v1/runs/{lux_run}/output", headers={"Authorization": f"Bearer {env.lux_key}"},
                      stream=True, timeout=(5, 1)) as res:
        res.raise_for_status()
        try:
            for raw in res.iter_lines():
                line = raw.decode("utf-8")
                if line.startswith("event:"):
                    event = line[6:].strip()
                elif line.startswith("data:") and event == "record":
                    rec = json.loads(line[5:]).get("event") or {}
                    if rec.get("type") == "lux.input" and (rec.get("data") or {}).get("text"):
                        out.append(rec["data"]["text"])
        except requests.exceptions.RequestException:
            pass  # the read timeout, whichever class it surfaces as: everything recorded so far has been read
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
    # The signature header of an accepted request is kept as ••••.
    assert started["headers"].get("X-Grafana-Signature") == "••••", started["headers"]
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
    # The orchestrator gave it to the running agent as its next input: the resolution itself.
    given = lambda: query(env.owner_dsn, "SELECT inputs_sent, jsonb_array_length(payloads) AS n FROM automation_runs WHERE id = %s", (run_id,))[0]
    wait_until(lambda: given()["inputs_sent"] >= 2, timeout=60,
               message=f"the running agent never heard the resolution: {given()} {_inputs(env, lux_run)}")
    last = wait_until(lambda: (lambda i: i[-1] if i and "resolved⟦/external " in i[-1] else None)(_inputs(env, lux_run)), timeout=30,
                      message=f"the agent's last input is not the resolution: {_inputs(env, lux_run)}")
    assert "firing⟦/external " not in last, last

    # Another group's alert: a run of its own.
    other = _post(env, hook, _alert("firing", service="db"), secret)
    assert other.status_code == 202, other.text
    own = wait_until(lambda: (lambda d: d if d.get("verdict") else None)(_delivery(env, other.json()["delivery"])), timeout=60,
                     message="the other group's delivery was never read")
    assert own["verdict"] == "started" and own["run_id"] and own["run_id"] != run_id, own

    # A group with no run going whose alert does not hold the condition: skipped, saying which.
    quiet = _post(env, hook, _alert("resolved", service="web"), secret)
    assert quiet.status_code == 202, quiet.text
    skipped = wait_until(lambda: (lambda d: d if d.get("verdict") else None)(_delivery(env, quiet.json()["delivery"])), timeout=60,
                         message="the resolved alert of a quiet group was never read")
    assert skipped["verdict"] == "skipped" and skipped["reason"] == "status is resolved, not firing" and skipped["run_id"] is None, skipped

    # A wrong signature: 401, a refused row with no body and no headers.
    bad = _post(env, hook, _alert("firing"), "whsec_not-the-secret")
    assert bad.status_code == 401, bad.text
    assert bad.json()["error"]["code"] == "bad_signature"
    refused = _delivery(env, bad.json()["delivery"])
    assert refused["verdict"] == "refused" and refused["reason"] == "bad_signature" and refused["payload"] is None, refused
    assert refused["headers"] == {}, refused["headers"]

    # The Deliveries tab lists all five, newest first.
    listed = client.get(f"/v1/automations/{a['id']}/deliveries").json()["deliveries"]
    assert [d["verdict"] for d in listed[:5]] == ["refused", "skipped", "started", "sent_to_run", "started"], listed
    # An unknown hook is not found and stores nothing.
    count = lambda: query(env.owner_dsn, "SELECT count(*) AS n FROM incoming_deliveries")[0]["n"]
    before = count()
    assert _post(env, "hk_" + os.urandom(8).hex(), _alert("firing"), secret).status_code == 404
    assert count() == before
