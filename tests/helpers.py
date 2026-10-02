"""Test helpers: an API client and direct-database assertions.

The client speaks only the public HTTP API. Database access exists for
*assertions* about state the API does not expose (and for seeding
organizations, which is an operator action rather than an API one).
"""

from __future__ import annotations

import hashlib
import secrets
import time
from dataclasses import dataclass
from typing import Any

import psycopg
import requests

KEY_PREFIX = "dude_sk_"

# ---------------------------------------------------------------------------
# Direct database seeding
# ---------------------------------------------------------------------------


def new_id(kind: str) -> str:
    """Mirror the control plane's prefixed-id convention."""
    return f"{kind}_{secrets.token_hex(12)}"


def create_organization(dsn: str, name: str, default_agent_models: dict | None = None) -> str:
    """Create an organization. Not an API operation — orgs are provisioned."""
    org_id = new_id("org")
    with psycopg.connect(dsn, autocommit=True) as conn:
        conn.execute(
            "INSERT INTO organizations (id, name, slug, default_agent_models) "
            "VALUES (%s, %s, %s, %s::jsonb)",
            (org_id, name, name, psycopg.types.json.Json(default_agent_models or {})),
        )
    return org_id


def webhook_secret(dsn: str, organization_id: str) -> str:
    """The secret dude signs-checks webhooks with. GitHub is given it at hook
    creation; the suite's fake GitHub reads it here."""
    with psycopg.connect(dsn) as conn:
        row = conn.execute(
            "SELECT webhook_secret FROM forge_credentials WHERE organization_id = %s", (organization_id,)
        ).fetchone()
    return row[0]


def create_api_key(dsn: str, organization_id: str, kind: str = "user", name: str | None = None) -> str:
    """Create an API key and return the plaintext.

    Inserted directly because key creation is an administrative bootstrap, and
    the suite needs a key before it can call anything.
    """
    raw = KEY_PREFIX + secrets.token_urlsafe(24)
    key_hash = hashlib.sha256(raw.encode()).hexdigest()

    with psycopg.connect(dsn, autocommit=True) as conn:
        # RLS applies to api_keys, but this connects as the owner, which is
        # how a deployment would provision the first key.
        conn.execute(
            "INSERT INTO api_keys (id, organization_id, name, key_hash, key_prefix, kind) "
            "VALUES (%s, %s, %s, %s, %s, %s)",
            (new_id("key"), organization_id, name or f"e2e {kind}", key_hash, raw[:16], kind),
        )
    return raw


def query(dsn: str, sql: str, params: tuple = ()) -> list[dict[str, Any]]:
    """Run a read query as the owner, for assertions about stored state."""
    with psycopg.connect(dsn, autocommit=True) as conn:
        cur = conn.execute(sql, params)
        columns = [d.name for d in cur.description or []]
        return [dict(zip(columns, row)) for row in cur.fetchall()]


def execute(dsn: str, sql: str, params: tuple = ()) -> None:
    """Run a write as the owner, for seeding what no API creates yet."""
    with psycopg.connect(dsn, autocommit=True) as conn:
        conn.execute(sql, params)


# ---------------------------------------------------------------------------
# API client
# ---------------------------------------------------------------------------


@dataclass
class ApiClient:
    """Thin wrapper over the public HTTP API."""

    base_url: str
    api_key: str

    @property
    def _headers(self) -> dict[str, str]:
        return {
            "authorization": f"Bearer {self.api_key}",
            "content-type": "application/json",
        }

    def request(self, method: str, path: str, **kwargs) -> requests.Response:
        return requests.request(
            method, f"{self.base_url}{path}", headers=self._headers, timeout=30, **kwargs
        )

    def get(self, path: str, **kwargs) -> requests.Response:
        return self.request("GET", path, **kwargs)

    def post(self, path: str, json: dict | None = None) -> requests.Response:
        return self.request("POST", path, json=json or {})

    def patch(self, path: str, json: dict | None = None) -> requests.Response:
        return self.request("PATCH", path, json=json or {})

    def put(self, path: str, json: dict | None = None) -> requests.Response:
        return self.request("PUT", path, json=json or {})

    def delete(self, path: str) -> requests.Response:
        return self.request("DELETE", path)

    # -- convenience wrappers, raising on unexpected failures ---------------

    def create_project(self, **body) -> dict:
        resp = self.post("/v1/projects", body)
        assert resp.status_code == 201, f"create project failed: {resp.status_code} {resp.text}"
        return resp.json()

    def create_task(self, project_id: str, title: str, **body) -> dict:
        resp = self.post("/v1/tasks", {"projectId": project_id, "title": title, **body})
        assert resp.status_code == 201, f"create task failed: {resp.status_code} {resp.text}"
        return resp.json()

    def create_run(self, task_id: str) -> dict:
        resp = self.post(f"/v1/tasks/{task_id}/runs")
        assert resp.status_code == 201, f"create run failed: {resp.status_code} {resp.text}"
        return resp.json()

    def create_session(self, run_id: str, role: str, **body) -> requests.Response:
        # Returns the raw response: several tests assert on rejection.
        return self.post(f"/v1/runs/{run_id}/sessions", {"role": role, **body})

    def events(self, **params) -> list[dict]:
        resp = self.get("/v1/events", params=params)
        assert resp.status_code == 200, f"events failed: {resp.status_code} {resp.text}"
        return resp.json()["events"]

    def task_runs(self, task_id: str) -> list[dict]:
        """A task's Runs, oldest first."""
        runs = self.get(f"/v1/tasks/{task_id}").json().get("runs", [])
        return sorted(runs, key=lambda r: r["createdAt"])

    def get_run(self, run_id: str) -> dict:
        resp = self.get(f"/v1/runs/{run_id}")
        assert resp.status_code == 200, f"get run failed: {resp.status_code} {resp.text}"
        return resp.json()


# ---------------------------------------------------------------------------
# Resume timing (run_resumes and run.resume.timed)
# ---------------------------------------------------------------------------

# Each phase of run.resume.timed, in order, and the row's stamps it spans.
RESUME_PHASE_PAIRS = (
    ("react", "woken_at", "requested_at"),
    ("schedule", "requested_at", "assigned_at"),
    ("image", "assigned_at", "image_ready_at"),
    ("restore", "image_ready_at", "volumes_restored_at"),
    ("start", "volumes_restored_at", "workload_started_at"),
    ("reload", "workload_started_at", "running_at"),
    ("take", "running_at", "busy_at"),
    ("firstOutput", "busy_at", "first_output_at"),
)

# The columns of a run_resumes row lux fills, by placement, as lux names
# each field: the new placement's start and host, the stopped one's end.
RESUME_STARTED = (("assigned_at", "assignedAt"), ("image_ready_at", "imageReadyAt"),
                  ("volumes_restored_at", "volumesRestoredAt"), ("container_started_at", "containerStartedAt"),
                  ("workload_started_at", "workloadStartedAt"), ("host_name", "hostName"))
RESUME_STOPPED = (("stop_requested_at", "stopRequestedAt"), ("exited_at", "exitedAt"),
                  ("snapshot_done_at", "snapshotDoneAt"), ("uploaded_at", "uploadedAt"),
                  ("snapshot_bytes", "snapshotBytes"), ("stopped_host_name", "hostName"))


def whole_ms(start, end) -> int:
    """end − start in whole milliseconds as the orchestrator rounds it
    (Go's math.Round: half away from zero), from integer microseconds."""
    delta = end - start
    us = (delta.days * 86_400 + delta.seconds) * 1_000_000 + delta.microseconds
    ms = (abs(us) + 500) // 1000
    return -ms if us < 0 else ms


def assert_timed_is_its_row(payload: dict, row: dict) -> None:
    """A run.resume.timed says what its run_resumes row says: its epoch,
    cause, move and host, and every phase and total exactly the difference
    of the two stamps it spans, rounded as the orchestrator rounds it. A
    phase across two clocks (schedule, reload) may be negative; one whose
    stamps are not both known is absent."""
    assert (payload["epoch"], payload["cause"], payload["moved"], payload["hostName"]) == (
        row["epoch"], row["cause"], row["moved"], row["host_name"]), (payload, row)
    phases = payload["phases"]
    for name, start, end in RESUME_PHASE_PAIRS:
        if row[start] is None or row[end] is None:
            assert name not in phases, (name, phases, row)
            continue
        assert phases[name] == whole_ms(row[start], row[end]), (name, phases[name], row[start], row[end])
    for key, end in (("totalMs", "first_output_at"), ("untilBusyMs", "busy_at")):
        assert payload[key] == whole_ms(row["woken_at"], row[end]), (key, payload[key], row)


def lux_stamp(value: str | None):
    """A lux timestamp as Postgres stores it: to the microsecond, below
    which it is dropped."""
    from datetime import datetime

    return None if value is None else datetime.fromisoformat(value.replace("Z", "+00:00"))


# ---------------------------------------------------------------------------
# Waiting
# ---------------------------------------------------------------------------


def sign_in(page, web_url: str, api_key: str) -> None:
    """Sign the web app in with a key, from a clean slate.

    The key is stored before the app's first script runs, once per tab: the
    app first asks /v1/me with no key (for a Cloudflare Access session), and
    that expected 401 would count as a console error in every test. The key
    prompt's own path is covered by the sign-in tests.
    """
    import json
    import uuid

    from playwright.sync_api import expect

    # Init scripts accumulate on a page; each sign-in stores its key once,
    # on the next document only, and later documents keep what the app did.
    page.add_init_script(
        """((key, token) => {
          if (sessionStorage.getItem("dude.test.signedIn:" + token)) return;
          sessionStorage.setItem("dude.test.signedIn:" + token, "1");
          localStorage.clear();
          localStorage.setItem("dude.apiKey", key);
        })(%s, %s)""" % (json.dumps(api_key), json.dumps(uuid.uuid4().hex)),
    )
    page.goto(web_url)
    expect(page.get_by_test_id("shell")).to_be_visible()


def toast(page, text: str):
    """A toast, by its text: the design system marks each one (data-toast),
    since Radix also renders a hidden copy of the text for a second."""
    return page.locator("[data-toast]").filter(has_text=text)


def wait_until(predicate, timeout: float = 30.0, interval: float = 0.25, message: str = ""):
    """Poll until `predicate` returns a truthy value, or fail the test.

    Returns the truthy value so callers can use what they waited for.
    """
    deadline = time.time() + timeout
    last = None
    while time.time() < deadline:
        last = predicate()
        if last:
            return last
        time.sleep(interval)
    raise AssertionError(message or f"condition not met within {timeout}s (last value: {last!r})")


def wait_for_run_status(client: ApiClient, run_id: str, status: str, timeout: float = 60.0) -> dict:
    """Wait for a Run to reach a status, failing with the actual one."""

    def check():
        run = client.get_run(run_id)
        return run if run["status"] == status else None

    try:
        return wait_until(check, timeout=timeout)
    except AssertionError:
        actual = client.get_run(run_id)
        raise AssertionError(
            f"run {run_id} did not reach {status!r} within {timeout}s; "
            f"status={actual['status']!r} error={actual.get('error')!r}"
        ) from None

