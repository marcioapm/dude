"""lux's server resource and event feed (lux#41), as dude relies on them:
the same assertions against the fake lux (every run) and, with
`run_tests.py --lux`, against the real one, so the fake cannot drift.

Field names are lux's openapi's (TenantServer, FeedEvent); dude's client
(orchestrator/internal/lux/servers.go) reads these.
"""

from __future__ import annotations

import json
import os
import time

import pytest
import requests

from helpers import wait_until

pytestmark = pytest.mark.both_luxes


class Lux:
    def __init__(self, url: str, key: str):
        self.url, self.key = url, key

    def req(self, method: str, path: str, body=None, headers=None) -> requests.Response:
        return requests.request(method, self.url + path, json=body, timeout=30,
                                headers={"Authorization": f"Bearer {self.key}", **(headers or {})})

    def domain(self) -> str | None:
        return self.req("GET", "/v1/whoami").json().get("previewDomain")

    def feed(self, after: int) -> list[dict]:
        """The feed's events after an id, as SSE, without following."""
        r = requests.get(self.url + "/v1/events?follow=false", timeout=30, stream=True,
                         headers={"Authorization": f"Bearer {self.key}", "Last-Event-ID": str(after),
                                  "Accept": "text/event-stream"})
        assert r.status_code == 200 and r.headers["Content-Type"].startswith("text/event-stream"), r.status_code
        out, ev_id, kind = [], None, None
        for line in r.iter_lines(decode_unicode=True):
            if line.startswith("id: "):
                ev_id = int(line[4:])
            elif line.startswith("event: "):
                kind = line[7:]
            elif line.startswith("data: ") and kind == "lux":
                e = json.loads(line[6:])
                assert e["id"] == ev_id, (e["id"], ev_id)
                out.append(e)
        return out


@pytest.fixture
def lux(env) -> Lux:
    lx = Lux(env.lux_url, env.lux_key)
    if not lx.domain():
        pytest.skip("this lux serves no previews (preview.domain unset)")
    return lx


def last_id(lux: Lux) -> int:
    """The newest event id now: the feed from here holds only what follows."""
    r = requests.get(lux.url + "/v1/events?follow=false&last=1", timeout=30, stream=True,
                     headers={"Authorization": f"Bearer {lux.key}"})
    ids = [int(line[4:]) for line in r.iter_lines(decode_unicode=True) if line.startswith("id: ")]
    return ids[-1] if ids else 0


def test_a_wakeable_server_is_created_found_and_deleted_as_dude_expects(lux: Lux):
    domain = lux.domain()
    label = f"contract-{os.urandom(4).hex()}"
    hostname = f"{label}.{domain}"
    before = last_id(lux)
    body = {"name": "web", "port": 8080, "command": ["true"], "hostname": hostname, "wake": "request",
            "lifetime": "owner", "idleAfter": "45m", "wakeTimeout": "5m", "expireAfter": "720h",
            "labels": {"dude.preview": label, "dude.kind": "preview"}}
    r = lux.req("POST", "/v1/servers", body)
    assert r.status_code == 201, r.text
    sv = r.json()
    try:
        for key in ("id", "name", "hostname", "url", "state", "process", "labels", "runId", "wake", "lifetime",
                    "idleAfter", "wakeTimeout", "expireAfter", "lastRequestAt", "wakes"):
            assert key in sv, (key, sv)
        assert sv["id"].startswith("srv_") and sv["hostname"] == hostname, sv
        assert sv["url"].split("://", 1)[1].split(":")[0] == hostname, sv["url"]
        assert sv["state"] == "asleep" and sv["process"] == "stopped" and sv["runId"] is None, sv
        assert sv["wake"] == "request" and sv["lifetime"] == "owner" and sv["wakes"] == 0, sv
        assert sv["idleAfter"] in ("45m0s", "45m") and sv["expireAfter"] in ("720h0m0s", "720h"), sv
        assert sv["labels"]["dude.preview"] == label

        # The same hostname again, in any case and with a trailing dot: taken.
        again = lux.req("POST", "/v1/servers", {**body, "hostname": hostname.upper() + "."})
        assert again.status_code == 409 and again.json()["error"]["code"] == "hostname_taken", again.text
        # Outside the preview domain: refused.
        outside = lux.req("POST", "/v1/servers", {**body, "hostname": f"{label}.elsewhere.invalid"})
        assert outside.status_code == 422 and outside.json()["error"]["code"] == "invalid_server", outside.text

        # Found by hostname and by label; GET by id.
        by_host = lux.req("GET", f"/v1/servers?hostname={hostname}").json()["servers"]
        assert [s["id"] for s in by_host] == [sv["id"]]
        by_label = lux.req("GET", f"/v1/servers?label=dude.preview={label}").json()["servers"]
        assert [s["id"] for s in by_label] == [sv["id"]]
        assert lux.req("GET", f"/v1/servers/{sv['id']}").json()["id"] == sv["id"]

        # Attach to a Run that does not exist: 404, nothing changes.
        assert lux.req("POST", f"/v1/servers/{sv['id']}/attach", {"runId": "run_doesnotexist"}).status_code == 404
    finally:
        assert lux.req("DELETE", f"/v1/servers/{sv['id']}").status_code == 204
    assert lux.req("GET", f"/v1/servers/{sv['id']}").status_code == 404
    assert lux.req("DELETE", f"/v1/servers/{sv['id']}").status_code == 404

    # The feed has its created and deleted events, with dude's fields, in
    # id order, after Last-Event-ID.
    def mine():
        evs = [e for e in lux.feed(before) if e.get("serverId") == sv["id"]]
        return evs if any(e["type"] == "server.deleted" for e in evs) else None
    evs = wait_until(mine, timeout=30, interval=1, message="no server.deleted on the feed")
    types = [e["type"] for e in evs]
    assert types[0] == "server.created" and types[-1] == "server.deleted", types
    for e in evs:
        assert e["id"] > before and e["runId"] is None, e
        d = e["data"]
        assert d["serverId"] == sv["id"] and d["name"] == "web" and d["labels"]["dude.preview"] == label, d
        assert d["hostname"] == hostname and d["url"] == sv["url"], d
    assert [e["id"] for e in evs] == sorted(e["id"] for e in evs)
    # Resuming after the last one: none of them again.
    assert not [e for e in lux.feed(evs[-1]["id"]) if e.get("serverId") == sv["id"]]


def test_a_sync_of_a_run_that_is_not_running_is_refused(lux: Lux):
    """dude's push-while-asleep path relies on this: lux answers 409 and dude
    leaves the sync to the next wake."""
    r = lux.req("POST", "/v1/runs/run_doesnotexist/sync", {"requestId": "x", "sync": [{"repo": "app", "ref": "main"}]})
    assert r.status_code == 404, r.text
