"""Event ledger and its live transport.

The ledger is the system's audit trail, so the properties that matter are
ordering, exact resume, and not losing an event in the gap between reading
history and attaching to the live stream.
"""

from __future__ import annotations

import json
import threading

import requests

from helpers import ApiClient


def test_actions_append_to_the_ledger(client: ApiClient, project: dict):
    task = client.create_task(project["id"], "Audited")
    client.create_run(task["id"])

    types = [e["eventType"] for e in client.events()]
    assert "project.created" in types
    assert "task.created" in types
    assert "run.created" in types


def test_cursors_increase_monotonically(client: ApiClient, project: dict):
    for i in range(3):
        client.create_task(project["id"], f"Item {i}")

    cursors = [e["cursor"] for e in client.events()]
    assert cursors == sorted(cursors)
    assert len(set(cursors)) == len(cursors)


def test_after_cursor_resumes_exactly(client: ApiClient, project: dict):
    client.create_task(project["id"], "First")
    seen = client.events()
    checkpoint = seen[-1]["cursor"]

    client.create_task(project["id"], "Second")

    resumed = client.events(after=checkpoint)
    # Exactly the events the client had not seen — no gaps, no repeats.
    assert all(e["cursor"] > checkpoint for e in resumed)
    assert any(e["eventType"] == "task.created" for e in resumed)


def test_next_cursor_is_usable_as_the_next_after(client: ApiClient, project: dict):
    client.create_task(project["id"], "Paging")

    body = client.get("/v1/events").json()
    assert body["nextCursor"] == body["events"][-1]["cursor"]

    # Polling again with that cursor yields nothing new.
    assert client.events(after=body["nextCursor"]) == []


def test_events_can_be_filtered_by_run(client: ApiClient, project: dict):
    task = client.create_task(project["id"], "Filtered")
    run = client.create_run(task["id"])

    scoped = client.events(runId=run["id"])
    assert scoped, "expected run-scoped events"
    assert all(e["runId"] == run["id"] for e in scoped)


def test_events_carry_actor_and_source(client: ApiClient, project: dict):
    client.create_task(project["id"], "Attributed")

    event = next(e for e in client.events() if e["eventType"] == "task.created")
    assert event["actor"]["type"] == "human"
    assert event["source"] == "control-plane"


def test_correlation_id_links_a_tasks_events(client: ApiClient, project: dict):
    task = client.create_task(project["id"], "Correlated")
    client.create_run(task["id"])

    correlated = [e for e in client.events() if e.get("correlationId") == task["id"]]
    assert {e["eventType"] for e in correlated} >= {"task.created", "run.created"}


def test_limit_is_bounded(client: ApiClient, project: dict):
    for i in range(5):
        client.create_task(project["id"], f"Bulk {i}")

    assert len(client.events(limit=2)) == 2
    # An absurd limit is clamped rather than accepted.
    assert client.get("/v1/events", params={"limit": 99999}).status_code == 400


def test_invalid_cursor_is_rejected(client: ApiClient):
    assert client.get("/v1/events", params={"after": "abc"}).status_code == 400
    assert client.get("/v1/events", params={"after": -5}).status_code == 400


# ---------------------------------------------------------------------------
# SSE
# ---------------------------------------------------------------------------


def _read_sse(url: str, api_key: str, want: int, ready: threading.Event, out: list) -> None:
    """Read SSE frames until `want` events arrive."""
    with requests.get(
        url, headers={"authorization": f"Bearer {api_key}"}, stream=True, timeout=20
    ) as resp:
        assert resp.status_code == 200
        assert "text/event-stream" in resp.headers["content-type"]

        cursor = None
        event_type = None
        for raw in resp.iter_lines(decode_unicode=True):
            if raw is None:
                continue
            line = raw.strip()

            # The server flushes a comment on connect, so a client knows it is
            # attached even when the backfill is empty.
            if line.startswith(":"):
                ready.set()
                continue

            if line.startswith("id:"):
                cursor = int(line[3:].strip())
            elif line.startswith("data:"):
                # Frames carry no `event:` name, so EventSource.onmessage
                # fires in the browser; the type is in the payload.
                payload = json.loads(line[5:].strip())
                out.append({
                    "cursor": cursor,
                    "eventType": payload.get("eventType"),
                    "data": payload,
                })
                if len(out) >= want:
                    return


def test_stream_delivers_live_events(client: ApiClient, project: dict, env):
    """An event created after the stream opens must arrive on it."""
    checkpoint = client.events()[-1]["cursor"]

    received: list = []
    ready = threading.Event()
    reader = threading.Thread(
        target=_read_sse,
        args=(
            f"{env.control_plane_url}/v1/events/stream?after={checkpoint}",
            client.api_key,
            1,
            ready,
            received,
        ),
        daemon=True,
    )
    reader.start()
    assert ready.wait(timeout=10), "stream did not signal that it was open"

    client.create_task(project["id"], "Live")
    reader.join(timeout=15)

    assert received, "no event delivered on the live stream"
    assert received[0]["data"]["eventType"] == "task.created"


def test_stream_backfills_missed_events(client: ApiClient, project: dict, env):
    """A client that was disconnected catches up from its last cursor."""
    checkpoint = client.events()[-1]["cursor"]

    # Happens while the client is "offline".
    client.create_task(project["id"], "Missed while away")

    received: list = []
    ready = threading.Event()
    reader = threading.Thread(
        target=_read_sse,
        args=(
            f"{env.control_plane_url}/v1/events/stream?after={checkpoint}",
            client.api_key,
            1,
            ready,
            received,
        ),
        daemon=True,
    )
    reader.start()
    reader.join(timeout=15)

    assert received, "backfill delivered nothing"
    assert received[0]["cursor"] > checkpoint


def test_stream_requires_authentication(env):
    resp = requests.get(f"{env.control_plane_url}/v1/events/stream", timeout=10)
    assert resp.status_code == 401
