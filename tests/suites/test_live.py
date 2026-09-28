"""Live work: a working agent's diff, what its session saved, and what it cost.

The scripted agent's fake/live implementer writes files into its checkout
without committing them and keeps working; it saves notes and a
screenshot as it goes. So while it works, the orchestrator's reads of its
checkout (through lux's exec) are its diff; pausing it stops its container,
which runs the Run's beforeStop hook — the final diff — and collects what
it saved; resumed, it finishes, saving its notes again, a second version.

Everything here is through the public API and the built web app, against
the fake lux (which runs the hook and exec in a real git checkout).
"""

from __future__ import annotations

import io
import json
import threading
import zipfile

import pytest
import requests
from playwright.sync_api import Page, expect

from helpers import ApiClient, wait_until

LIVE_MODELS = {"implementer": {"model": "fake/live"}, "reviewer": {"model": "fake/scripted"},
               "simplifier": {"model": "fake/scripted"}}


def _live_task(client: ApiClient, project: dict, title: str) -> tuple[dict, dict]:
    """A task whose implementer is at work, its edits in its checkout."""
    client.patch(f"/v1/projects/{project['id']}", {"agentModels": LIVE_MODELS})
    task = client.create_task(project["id"], title)
    assert client.post(f"/v1/tasks/{task['id']}/deliver").status_code == 201
    run = wait_until(lambda: next((r for r in client.task_runs(task["id"]) if r["phase"] == "implement"), None),
                     timeout=30, message="no implementer")
    return task, run


def _diff_paths(client: ApiClient, run_id: str) -> dict[str, str]:
    return {f["path"]: f["status"] for f in client.get(f"/v1/runs/{run_id}/diff").json()["files"]}


def _listen(client: ApiClient, run_id: str, types: set[str], found: list, stop: threading.Event) -> None:
    """Collect events of `types` from the run's live stream, as a browser would hear them."""
    with requests.get(f"{client.base_url}/v1/events/stream", params={"runId": run_id, "key": client.api_key},
                      stream=True, timeout=(5, 60)) as res:
        for raw in res.iter_lines(decode_unicode=True):
            if stop.is_set():
                return
            if raw and raw.startswith("data:"):
                event = json.loads(raw[5:])
                if event["eventType"] in types:
                    found.append(event)


def test_a_working_agents_uncommitted_edits_are_its_diff_while_it_works(client: ApiClient, forge_project: dict):
    task, run = _live_task(client, forge_project, "Watch it work")

    # Heard on the stream as it happens: a summary with no lines.
    found: list = []
    stop = threading.Event()
    listener = threading.Thread(target=_listen, args=(client, run["id"], {"run.diff.updated"}, found, stop), daemon=True)
    listener.start()

    wait_until(lambda: _diff_paths(client, run["id"]) == {"LIVE.md": "A", "README.md": "M"},
               timeout=30, message="the edits never became the run's diff")
    diff = client.get(f"/v1/runs/{run['id']}/diff").json()
    assert diff["final"] is False and len(diff["base"]) == 40 and diff["checksum"]
    readme = next(f for f in diff["files"] if f["path"] == "README.md")
    assert readme["additions"] >= 1 and readme["deletions"] == 1
    added = [line["text"] for h in readme["hunks"] for line in h["lines"] if line["kind"] == "+"]
    assert "Changed while the agent works." in added
    for line in (line for h in readme["hunks"] for line in h["lines"]):
        assert (line["old"] is None) == (line["kind"] == "+") and (line["new"] is None) == (line["kind"] == "-")
    assert client.get_run(run["id"])["status"] == "running"

    event = wait_until(lambda: found[:1], timeout=30, message="no run.diff.updated on the stream")[0]
    stop.set()
    assert event["payload"]["checksum"] == diff["checksum"]
    assert {f["path"] for f in event["payload"]["files"]} == {"LIVE.md", "README.md"}
    assert all("hunks" not in f for f in event["payload"]["files"])

    # Another organization sees no such run.
    other = ApiClient(client.base_url, "nope")
    assert other.get(f"/v1/runs/{run['id']}/diff").status_code == 401


def test_a_pause_leaves_the_final_diff_and_what_the_session_saved_with_its_versions(
    client: ApiClient, second_org: dict, forge_project: dict
):
    task, run = _live_task(client, forge_project, "Save things")
    wait_until(lambda: _diff_paths(client, run["id"]), timeout=30, message="no live diff")

    # A pause stops the container: lux's beforeStop hook leaves the final
    # diff, and what it saved so far is collected.
    assert client.post(f"/v1/runs/{run['id']}/pause", {}).status_code in (200, 202)
    wait_until(lambda: client.get(f"/v1/runs/{run['id']}/diff").json()["final"], timeout=30,
               message="no final diff after the pause")
    first = wait_until(lambda: {a["name"]: a for a in client.get("/v1/artifacts", params={"taskId": task["id"]}).json()["artifacts"]}
                       if len(client.get("/v1/artifacts", params={"taskId": task["id"]}).json()["artifacts"]) >= 3 else None,
                       timeout=60, message="what it saved was never collected")
    assert set(first) == {"NOTES.md", "screenshot.png", "coverage.html"}, first
    assert not any(name.startswith(".dude") for name in first)
    assert first["screenshot.png"]["contentType"] == "image/png"

    # Resumed, it finishes and saves its notes again: two versions of one file.
    assert client.post(f"/v1/runs/{run['id']}/resume", {}).status_code in (200, 202)
    wait_until(lambda: sum(a["name"] == "NOTES.md" for a in client.get("/v1/artifacts", params={"taskId": task["id"]}).json()["artifacts"]) == 2,
               timeout=90, message="the second version was never collected")
    notes = [a for a in client.get("/v1/artifacts", params={"taskId": task["id"]}).json()["artifacts"] if a["name"] == "NOTES.md"]
    assert [(a["version"], a["versions"]) for a in notes] == [(2, 2), (1, 2)]
    assert "Finished." in client.get(f"/v1/artifacts/{notes[0]['id']}/content").text
    assert "Still working." in client.get(f"/v1/artifacts/{notes[1]['id']}/content").text
    # After the resume the final diff has the work it finished with.
    wait_until(lambda: "DONE.md" in _diff_paths(client, run["id"]), timeout=60, message="the finishing edit never reached the diff")

    # The latest of each, as one zip.
    res = client.get(f"/v1/tasks/{task['id']}/artifacts.zip")
    assert res.status_code == 200 and res.headers["content-type"] == "application/zip"
    assert "attachment" in res.headers["content-disposition"]
    archive = zipfile.ZipFile(io.BytesIO(res.content))
    assert archive.testzip() is None
    assert sorted(archive.namelist()) == ["NOTES.md", "coverage.html", "screenshot.png"]
    assert "Finished." in archive.read("NOTES.md").decode()
    assert archive.read("screenshot.png").startswith(b"\x89PNG")
    assert second_org["client"].get(f"/v1/tasks/{task['id']}/artifacts.zip").status_code == 404


def test_a_page_an_agent_saved_is_never_served_as_one_of_ours(client: ApiClient, forge_project: dict):
    """HTML is an attachment unless asked for inline, and always sandboxed."""
    task, run = _live_task(client, forge_project, "Coverage")
    wait_until(lambda: _diff_paths(client, run["id"]), timeout=30, message="no live diff")
    assert client.post(f"/v1/runs/{run['id']}/pause", {}).status_code in (200, 202)
    page = wait_until(lambda: next((a for a in client.get("/v1/artifacts", params={"taskId": task["id"]}).json()["artifacts"]
                                    if a["name"] == "coverage.html"), None), timeout=60, message="the page was never collected")
    assert page["contentType"].startswith("text/html")
    res = client.get(f"/v1/artifacts/{page['id']}/content")
    assert res.headers["content-type"].startswith("text/html")
    assert res.headers["content-disposition"].startswith("attachment")
    assert "sandbox" in res.headers["content-security-policy"]
    assert res.headers["x-content-type-options"] == "nosniff"
    inline = client.get(f"/v1/artifacts/{page['id']}/content", params={"inline": "1"})
    assert "content-disposition" not in inline.headers
    assert "sandbox" in inline.headers["content-security-policy"]


def _sign_in(page: Page, web_url: str, api_key: str) -> None:
    page.goto(web_url)
    page.evaluate("localStorage.clear()")
    page.goto(web_url)
    page.fill('input[type="password"]', api_key)
    page.click('button[type="submit"]')
    expect(page.get_by_test_id("shell")).to_be_visible()


@pytest.mark.ui
def test_a_sessions_changes_update_as_the_agent_works(
    page: Page, web_url: str, client: ApiClient, org: dict, forge_project: dict, console_errors: list
):
    """Changes shows the agent's checkout as it changes, without a reload."""
    task, run = _live_task(client, forge_project, "Watch the changes")
    wait_until(lambda: _diff_paths(client, run["id"]), timeout=30, message="no live diff")

    _sign_in(page, web_url, org["api_key"])
    page.goto(f"{web_url}#/session/{run['id']}")
    page.get_by_role("tab", name="Changes").click()
    changes = page.get_by_test_id("changes")
    expect(changes.get_by_test_id("live-pill")).to_be_visible()
    expect(changes.get_by_test_id("diff-file")).to_have_count(2)
    expect(changes.get_by_test_id("diff-section").filter(has_text="README.md")).to_contain_text("Changed while the agent works.")
    expect(changes.get_by_test_id("follow")).to_have_attribute("aria-checked", "true")

    # Picking a file shows it alone, and the person has taken over.
    changes.get_by_test_id("diff-file").filter(has_text="LIVE.md").click()
    expect(changes.get_by_test_id("diff-section")).to_have_count(1)
    expect(changes.get_by_test_id("follow")).to_have_attribute("aria-checked", "false")
    changes.get_by_test_id("follow").click()
    expect(changes.get_by_test_id("diff-section")).to_have_count(2)

    # Paused and resumed, the agent finishes with one more file: it arrives.
    client.post(f"/v1/runs/{run['id']}/pause", {})
    wait_until(lambda: client.get_run(run["id"])["status"] == "paused", timeout=30, message="never paused")
    client.post(f"/v1/runs/{run['id']}/resume", {})
    expect(changes.get_by_test_id("diff-file").filter(has_text="DONE.md")).to_be_visible(timeout=60_000)
    assert console_errors == []


@pytest.mark.ui
def test_a_tasks_files_open_in_a_viewer_with_their_versions(
    page: Page, web_url: str, client: ApiClient, org: dict, forge_project: dict, console_errors: list
):
    task, run = _live_task(client, forge_project, "Look at the files")
    wait_until(lambda: _diff_paths(client, run["id"]), timeout=30, message="no live diff")
    client.post(f"/v1/runs/{run['id']}/pause", {})
    wait_until(lambda: client.get_run(run["id"])["status"] == "paused", timeout=30, message="never paused")
    client.post(f"/v1/runs/{run['id']}/resume", {})
    wait_until(lambda: sum(a["name"] == "NOTES.md" for a in client.get("/v1/artifacts", params={"taskId": task["id"]}).json()["artifacts"]) == 2,
               timeout=90, message="the second version was never collected")

    _sign_in(page, web_url, org["api_key"])
    page.goto(f"{web_url}#/task/{task['id']}")
    page.get_by_role("tab", name="Files").click()
    files = page.get_by_test_id("files")
    expect(files.get_by_test_id("file-card")).to_have_count(1)
    expect(files.get_by_test_id("file-card")).to_contain_text("screenshot.png")
    expect(files.get_by_test_id("file-row")).to_have_count(2)
    expect(files.get_by_test_id("file-row").filter(has_text="NOTES.md")).to_contain_text("v2")

    # The viewer: the latest, its versions, the one before, and the next file.
    files.get_by_test_id("file-row").filter(has_text="NOTES.md").get_by_role("button").first.click()
    viewer = page.get_by_test_id("file-viewer")
    expect(viewer.get_by_role("heading", name="NOTES.md", exact=True)).to_be_visible()
    expect(viewer).to_contain_text("Finished.")
    expect(viewer.get_by_test_id("viewer-version")).to_have_count(2)
    viewer.get_by_test_id("viewer-version").nth(1).click()
    expect(viewer).to_contain_text("Still working.")
    with page.expect_download() as download:
        viewer.get_by_test_id("viewer-download").click()
    assert download.value.suggested_filename == "NOTES.md"
    # Neighbours as the page shows them: the picture first, then documents.
    expect(viewer).to_contain_text("2 of 3")
    page.keyboard.press("ArrowLeft")
    expect(viewer.get_by_role("heading", name="screenshot.png", exact=True)).to_be_visible()
    expect(viewer).to_contain_text("1 of 3")
    page.keyboard.press("Escape")
    expect(viewer).to_have_count(0)

    # Everything at once.
    with page.expect_download() as download:
        files.get_by_test_id("download-all").click()
    assert download.value.suggested_filename.endswith("-files.zip")
    assert console_errors == []
