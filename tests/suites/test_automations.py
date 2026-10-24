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

import hashlib
import hmac
import json
import os
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

from helpers import ApiClient, execute, provision_person, query, wait_until


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


def _make(who: ApiClient, kind: str, settings: dict, trigger: str) -> dict:
    created = who.post("/v1/automations", {"name": settings["name"], "kind": kind, "trigger": trigger})
    assert created.status_code == 201, created.text
    a = created.json()
    saved = who.put(f"/v1/automations/{a['id']}", {"kind": kind, **settings})
    assert saved.status_code == 200, saved.text
    on = who.put(f"/v1/automations/{a['id']}/enabled", {"enabled": True})
    assert on.status_code == 200, on.text
    return on.json()


class _Receiver:
    """A webhook receiver: each POST's headers and body, answered by `answers` in turn (then 200)."""

    def __init__(self, answers: list[int]):
        self.got: list[tuple[dict, bytes]] = []
        answers = list(answers)
        outer = self

        class Handler(BaseHTTPRequestHandler):
            def do_POST(self):
                body = self.rfile.read(int(self.headers.get("content-length", "0")))
                outer.got.append(({k.lower(): v for k, v in self.headers.items()}, body))
                self.send_response(answers.pop(0) if answers else 200)
                self.send_header("content-length", "0")
                self.end_headers()

            def log_message(self, *_args):
                pass

        self.server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        threading.Thread(target=self.server.serve_forever, daemon=True).start()
        self.url = f"http://127.0.0.1:{self.server.server_port}/hook"

    def close(self):
        self.server.shutdown()


def test_a_command_s_paths_filter_reads_the_pull_request_s_files(client: ApiClient, env, forge_project: dict, fake_github):
    """paths reads the pull request's files from GitHub: a pull request
    changing a file under docs/ runs; one changing only other files is
    filtered out (no run); a push is about no pull request's files."""
    a = _make(client, "command", {"name": "Docs changed", "trigger": {"kind": "github", "event": "pull_request", "actions": ["opened"],
                                                                      "filters": {"paths": ["docs/"]}},
                                  "command": 'echo "pr=$(grep -o \'"number": *[0-9]*\' "$DUDE_EVENT_PATH" | head -1)"'}, "github")
    fake_github.write_file("main", "README.md", "base\n", "base")
    fake_github.open_pull("feat/code")
    fake_github.write_file("feat/code", "src/app.py", "print(1)\n", "code only")
    fake_github.open_pull("feat/docs")
    fake_github.write_file("feat/docs", "docs/guide.md", "# Guide\n", "docs")
    code, docs = 1, 2
    for n, head in ((code, "feat/code"), (docs, "feat/docs")):
        assert fake_github.send_webhook("pull_request", {"action": "opened", "number": n, "sender": {"login": "ana"},
                                                         "pull_request": {"number": n, "head": {"ref": head}, "labels": []}}) == 202
    run = wait_until(lambda: next((r for r in _runs(client, a["id"]) if r["status"] in ("succeeded", "failed")), None),
                     timeout=60, message="the docs pull request's command never ended")
    assert run["status"] == "succeeded", run
    detail = client.get(f"/v1/automations/{a['id']}/runs/{run['id']}").json()
    assert f'"number": {docs}' in (detail["logTail"] or ""), detail
    # The code-only pull request was filtered: still one run once both deliveries were read.
    wait_until(lambda: not query(env.owner_dsn, "SELECT 1 FROM webhook_deliveries WHERE organization_id = %s AND automations_at IS NULL",
                                 (forge_project["organizationId"],)), timeout=30, message="deliveries never read")
    assert [r["id"] for r in _runs(client, a["id"])] == [run["id"]]
    evaluated = client.post(f"/v1/automations/{a['id']}/test-event/evaluate", {"body": {
        "action": "opened", "number": code, "repository": {"full_name": f"{fake_github.owner}/{fake_github.repo}"},
        "pull_request": {"number": code, "head": {"ref": "feat/code"}, "labels": []}}})
    assert evaluated.status_code == 200 and evaluated.json()["filter"] == "paths", evaluated.text


def test_a_webhook_is_signed_with_its_secret_and_retried_once_then_fails(client: ApiClient, env, forge_project: dict, fake_github):
    receiver = _Receiver([200, 503, 502])
    try:
        a = _make(client, "webhook", {"name": "Signed", "trigger": {"kind": "github", "event": "push", "actions": [], "filters": {}},
                                      "webhook": {"url": receiver.url}}, "github")
        secret = query(env.owner_dsn, "SELECT webhook_secret FROM automations WHERE id = %s", (a["id"],))[0]["webhook_secret"]
        assert a["webhookSecretHint"] == secret[-4:] and "webhookSecret" not in a, a

        # First push: one POST, 200, the signature the stored secret's HMAC of the body.
        assert fake_github.send_webhook("push", {"ref": "refs/heads/main", "sender": {"login": "ana"}}) == 202
        first = wait_until(lambda: next((r for r in _runs(client, a["id"]) if r["status"] in ("succeeded", "failed")), None),
                           timeout=60, message="the webhook run never ended")
        assert first["status"] == "succeeded", first
        headers, body = receiver.got[0]
        want = "sha256=" + hmac.new(secret.encode(), body, hashlib.sha256).hexdigest()
        assert headers["x-dude-signature-256"] == want, headers
        assert headers["x-dude-automation"] == a["id"] and headers["x-dude-run"] == first["id"], headers
        assert headers["x-github-event"] == "push" and headers["x-github-delivery"] == fake_github.last_delivery_id, headers
        assert json.loads(body)["ref"] == "refs/heads/main"

        # Second push: 503, retried once, 502: the run fails, two request rows.
        assert fake_github.send_webhook("push", {"ref": "refs/heads/main", "sender": {"login": "ana"}}) == 202
        second = wait_until(lambda: next((r for r in _runs(client, a["id"]) if r["id"] != first["id"]
                                          and r["status"] in ("succeeded", "failed")), None), timeout=60, message="never ended")
        detail = client.get(f"/v1/automations/{a['id']}/runs/{second['id']}").json()
        assert second["status"] == "failed" and detail["reason"] == "the webhook did not answer 2xx", detail
        assert [(q["try"], q["httpStatus"]) for q in detail["requests"]] == [(1, 503), (2, 502)], detail["requests"]
        assert len(receiver.got) == 3
        # Both tries carry the same signature over the same body.
        assert receiver.got[1][0]["x-dude-signature-256"] == receiver.got[2][0]["x-dude-signature-256"]
    finally:
        receiver.close()


def test_a_minutely_schedule_fires_stops_when_off_and_run_now_queues_behind_a_live_run(client: ApiClient, env, forge_project: dict):
    # The fake lux kills a command after 10 s: 6 s is long enough to be live when Run now arrives.
    a = _make(client, "command", {"name": "Every minute", "trigger": {"kind": "schedule", "cron": "* * * * *", "tz": "UTC"},
                                  "command": 'echo "tick"; sleep 6'}, "schedule")
    # It fires within about 70 s: the next minute's boundary, then the loop's pass.
    fired = wait_until(lambda: next((r for r in _runs(client, a["id"]) if r["source"] == "schedule"), None),
                       timeout=75, interval=1, message="the minutely schedule never fired")
    wait_until(lambda: next((r for r in _runs(client, a["id"]) if r["id"] == fired["id"] and r["status"] == "running"), None),
               timeout=30, interval=0.5, message="the scheduled run never ran")
    # Run now while it runs: queued behind it (Q7), not started beside it.
    now = client.post(f"/v1/automations/{a['id']}/runs", {})
    assert now.status_code == 202, now.text
    queued = next(r for r in _runs(client, a["id"]) if r["id"] == now.json()["runId"])
    assert queued["status"] == "queued" and queued["source"] == "run_now", queued
    assert queued["reason"] == "waiting for the run before it", queued
    # Off: no further firing.
    assert client.put(f"/v1/automations/{a['id']}/enabled", {"enabled": False}).status_code == 200
    ended = wait_until(lambda: next((r for r in _runs(client, a["id"]) if r["id"] == queued["id"] and r["status"] in ("succeeded", "failed")), None),
                       timeout=90, interval=1, message="the run now never ran after the scheduled one")
    first = next(r for r in _runs(client, a["id"]) if r["id"] == fired["id"])
    assert first["status"] == "succeeded" and ended["status"] == "succeeded" and ended["startedAt"] >= first["endedAt"], (first, ended)
    count = len([r for r in _runs(client, a["id"]) if r["source"] == "schedule"])
    # Past the next minute's boundary, off: nothing more fired.
    execute(env.owner_dsn, "UPDATE automations SET next_run_at = now() - interval '1 second' WHERE id = %s", (a["id"],))
    import time
    time.sleep(5)
    assert len([r for r in _runs(client, a["id"]) if r["source"] == "schedule"]) == count
