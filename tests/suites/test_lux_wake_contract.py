"""A branch preview that wakes on request, end to end against a real lux.

dude declares a preview → lux has its server at dude's hostname (one
label under lux's preview domain), asleep → a signed-in request (lux's
preview ticket, as a browser signs in) gets the waking page and wakes it →
dude submits the preview's Run and attaches the server → it serves the
task's branch → unused, lux says idle → dude stops the Run → a new commit
on the branch → a request wakes it again → dude resumes it with a sync →
it serves the new commit, its state volume carried over.

Opt-in with `run_tests.py --lux`, against a lux started with
`run_tests.py --serve --preview-local <port>` (lux's docs/development.md,
"Try branch previews locally"). Screenshots of the waking page and of
dude's Servers tab go to DUDE_WAKE_SHOTS (default /var/tmp/dude-wake-shots).
"""

from __future__ import annotations

import os
import time
import urllib.parse
from pathlib import Path

import psycopg
import pytest
import requests

from fake_github import FakeGitHub
from helpers import ApiClient, sign_in, wait_until

pytestmark = pytest.mark.lux

FAKE_IMAGE = "localhost/lux-fake:test"
SHOTS = Path(os.environ.get("DUDE_WAKE_SHOTS", "/var/tmp/dude-wake-shots"))


def lux_api(env, method: str, path: str, body=None) -> requests.Response:
    return requests.request(method, env.lux_url + path, json=body, timeout=30,
                            headers={"Authorization": f"Bearer {env.lux_key}"})


class Browser:
    """A signed-in browser at a server's hostname, by Host header, as lux's
    own wake suite has one (tests/suites/test_server_wake.py)."""

    def __init__(self, env, sv: dict):
        self.env, self.sv = env, sv
        u = urllib.parse.urlsplit(sv["url"])
        port = u.port or 80
        self.base, self.host = f"http://127.0.0.1:{port}", u.netloc
        self.cookie = ""

    def get(self, path: str = "/") -> requests.Response:
        headers = {"Host": self.host, "Accept": "text/html"}
        if self.cookie:
            headers["Cookie"] = self.cookie
        return requests.get(self.base + path, headers=headers, allow_redirects=False, timeout=30)

    def sign_in(self) -> "Browser":
        t = lux_api(self.env, "POST", f"/v1/servers/{self.sv['id']}/tickets").json()["ticket"]
        r = self.get(f"/.lux/auth?ticket={t}&to=/")
        assert r.status_code == 302, (r.status_code, r.text)
        self.cookie = r.headers["Set-Cookie"].split(";")[0]
        return self

    def into_app(self, timeout: float = 240) -> requests.Response:
        def ready():
            r = self.get("/.lux/wait?to=%2F")
            return r.status_code == 303 and r
        wait_until(ready, timeout=timeout, interval=1, message=f"{self.host} never woke")
        return self.get("/")


def ticket_link(env, sv: dict, to: str = "/") -> str:
    t = lux_api(env, "POST", f"/v1/servers/{sv['id']}/tickets").json()["ticket"]
    return f"{sv['url']}/.lux/auth?ticket={t}&to={urllib.parse.quote(to)}"


@pytest.fixture
def preview_project(client: ApiClient, env, org: dict):
    gh = FakeGitHub(env.git_root, owner=f"o{os.urandom(4).hex()}", listen=env.real_lux["gateway"])
    gh.start()
    assert client.post("/v1/forge/credential", {"auth": "pat", "secret": "fake-token", "apiBaseUrl": gh.api_url}).status_code == 200
    project = client.create_project(
        name="Previews on lux", slug=f"pv-{os.urandom(3).hex()}", runtimeImage=FAKE_IMAGE,
        agentModels={r: {"model": "fake/scripted"} for r in ("implementer", "reviewer", "simplifier")},
        repositories=[{"name": "app", "url": gh.clone_url, "defaultBranch": "main"}],
    )
    # lux-fake's app: the checkout's message.txt and commit, a visit counter
    # on the workspace state volume.
    recipe = {"name": "web", "port": 8080, "command": "lux-fake app 8080 /workspace/repos/app /workspace/data/visits",
              "workdir": "", "setup": None, "env": [], "autostartInPreviews": True}
    assert client.put(f"/v1/projects/{project['id']}/servers/web", recipe).status_code == 200
    # Idle after 8 seconds, so the suite sees it: below what the API takes
    # (whole minutes), set in the database.
    with psycopg.connect(env.owner_dsn, autocommit=True) as conn:
        conn.execute("""UPDATE projects SET preview_settings = '{"idleTimeoutMinutes": 0.134}' WHERE id = %s""", (project["id"],))
    yield project, gh
    gh.stop()


def test_a_preview_sleeps_and_wakes_on_real_lux(client: ApiClient, env, org: dict, preview_project, page, web_url: str):
    project, gh = preview_project
    SHOTS.mkdir(parents=True, exist_ok=True)
    _write_message(gh, "hello from commit A")
    task = client.create_task(project["id"], "Preview me on lux")
    assert client.post(f"/v1/tasks/{task['id']}/preview").status_code == 201

    # Declared: asleep, a lux server at dude's hostname, nothing running.
    view = wait_until(lambda: (s := client.get(f"/v1/tasks/{task['id']}/servers").json())["run"].get("asleep") and s,
                      timeout=60, interval=1, message="the preview never went to sleep")
    [web] = view["servers"]
    domain = lux_api(env, "GET", "/v1/whoami").json()["previewDomain"]
    sv = lux_api(env, "GET", f"/v1/servers/{web['id']}").json()
    label, _, under = sv["hostname"].partition(".")
    # One label under lux's domain: <server>-<task>-<project>, '_' made '-'.
    want = f"web-{task['id']}-{project['id']}".replace("_", "-").lower()
    assert under == domain and label == want and len(label) <= 63, (sv["hostname"], want)
    assert sv["state"] == "asleep" and sv["runId"] is None and sv["labels"]["dude.task"] == task["id"], sv
    assert web["url"] == sv["url"]
    shot_dude(page, web_url, org, task, "0-dude-asleep.png")

    # A signed-in request: the waking page, and dude wakes it (a new Run, the server attached).
    page.goto(ticket_link(env, sv))
    page.wait_for_selector("ol.steps", timeout=30_000)
    page.screenshot(path=str(SHOTS / "1-lux-waking-page.png"))
    assert "Asked the orchestrator to start it" in page.content()
    shot_dude(page, web_url, org, task, "2-dude-waking.png")
    b = Browser(env, sv).sign_in()
    first = b.into_app()
    assert first.status_code == 200 and "commit A" in first.text, first.text
    commit_a = _app_commit(first.text)
    run_view = client.get(f"/v1/tasks/{task['id']}/servers").json()["run"]
    lux_run = run_view["luxRunId"]
    assert lux_api(env, "GET", f"/v1/servers/{sv['id']}").json()["runId"] == lux_run
    page.goto(ticket_link(env, sv))
    page.wait_for_selector("#commit", timeout=60_000)
    page.screenshot(path=str(SHOTS / "3-lux-app-commit-a.png"))
    wait_until(lambda: client.get(f"/v1/tasks/{task['id']}/servers").json()["run"]["previewStage"] == "ready",
               timeout=60, interval=1, message="dude never saw it ready")
    shot_dude(page, web_url, org, task, "4-dude-ready.png")

    # Unused: lux says idle, dude stops the Run.
    wait_until(lambda: lux_api(env, "GET", f"/v1/runs/{lux_run}").json()["state"] == "stopped",
               timeout=180, interval=2, message="dude never stopped the idle preview")
    wait_until(lambda: client.get(f"/v1/tasks/{task['id']}/servers").json()["run"].get("asleep"),
               timeout=60, interval=1, message="dude never showed it asleep")
    assert lux_api(env, "GET", f"/v1/servers/{sv['id']}").json()["state"] == "asleep"

    # A new commit on the branch while it sleeps; the next request wakes it on it.
    _write_message(gh, "hello from commit B")
    page.goto(ticket_link(env, sv))
    page.wait_for_selector("ol.steps", timeout=30_000)
    time.sleep(2)
    page.screenshot(path=str(SHOTS / "5-lux-waking-again.png"))
    b = Browser(env, sv).sign_in()
    again = b.into_app()
    assert "commit B" in again.text and _app_commit(again.text) != commit_a, again.text
    # The same Run, resumed with a sync; the visit counter carried on.
    assert client.get(f"/v1/tasks/{task['id']}/servers").json()["run"]["luxRunId"] == lux_run
    syncs = [e for e in lux_events(env, lux_run) if e["type"] == "git.sync"]
    assert syncs and syncs[-1]["data"]["repo"] == "app" and syncs[-1]["data"]["status"] in ("fast-forward", "reset"), syncs
    page.goto(ticket_link(env, sv))
    page.wait_for_selector("#commit", timeout=60_000)
    page.screenshot(path=str(SHOTS / "6-lux-app-commit-b.png"))
    shot_dude(page, web_url, org, task, "7-dude-ready-again.png")

    # Stopped in dude: the server deleted (lux's "gone" page), then the Run cancelled.
    assert client.delete(f"/v1/tasks/{task['id']}/preview").status_code == 200
    wait_until(lambda: lux_api(env, "GET", f"/v1/servers/{sv['id']}").status_code == 404,
               timeout=60, interval=1, message="the server was never deleted")
    wait_until(lambda: lux_api(env, "GET", f"/v1/runs/{lux_run}").json()["state"] == "cancelled",
               timeout=60, interval=1, message="the Run was never cancelled")
    page.goto(sv["url"] + "/")
    page.screenshot(path=str(SHOTS / "8-lux-gone.png"))
    assert "This preview is gone" in page.content()


def _write_message(gh: FakeGitHub, text: str) -> str:
    """A person pushes message.txt on main."""
    import subprocess
    import tempfile

    with tempfile.TemporaryDirectory() as d:
        subprocess.run(["git", "clone", "-q", str(gh.bare), d], check=True)
        Path(d, "message.txt").write_text(text + "\n")
        for args in (["add", "."], ["-c", "user.name=Gus", "-c", "user.email=g@x", "commit", "-qm", text], ["push", "-q", "origin", "HEAD:main"]):
            subprocess.run(["git", "-C", d, *args], check=True)
        return subprocess.run(["git", "-C", d, "rev-parse", "HEAD"], check=True, capture_output=True, text=True).stdout.strip()


def _app_commit(html: str) -> str:
    return html.split('<code id="commit">', 1)[1].split("<", 1)[0]


def lux_events(env, run_id: str) -> list[dict]:
    return lux_api(env, "GET", f"/v1/runs/{run_id}/events").json()["events"]


def shot_dude(page, url: str, org: dict, task: dict, name: str) -> None:
    """dude's Servers tab for the task, as a person sees it."""
    sign_in(page, url, org["api_key"])
    page.goto(f"{url}/#/task/{task['id']}/servers")
    page.wait_for_selector('[data-testid="servers-panel"]', timeout=30_000)
    time.sleep(1)
    page.screenshot(path=str(SHOTS / name), full_page=True)
