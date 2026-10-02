"""Images a person sends an agent, end to end.

Through the public API and the built web app, against the fake lux and the
suite's S3: an image is uploaded to a task, sent with a steer, an answer or
the task's prompt, read by the orchestrator from storage and given to lux
with its words; the fake lux records what each input carried (name, type,
size, sha256), and the transcript shows it. In the browser, every way in —
the paperclip, paste, a drop on the conversation — makes a chip; an image
that cannot go keeps Send off; the viewer says what the agent got.

Screenshots of each mockup screen, light and dark, go to
$DUDE_TEST_SHOTS (default /var/tmp/cimg-dude-shots/).
"""

from __future__ import annotations

import hashlib
import os
import struct
import zlib
from pathlib import Path

import pytest
import requests
from playwright.sync_api import Page, expect

from helpers import ApiClient, create_api_key, query, sign_in, wait_until

SHOTS = Path(os.environ.get("DUDE_TEST_SHOTS", "/var/tmp/cimg-dude-shots"))


def png(width: int, height: int, colour: tuple[int, int, int] = (40, 120, 200)) -> bytes:
    """A real PNG, solid with a stripe, made with the standard library."""
    row = b"".join(bytes(colour if (x // 40) % 2 else (240, 240, 240)) for x in range(width))
    raw = b"".join(b"\x00" + row for _ in range(height))

    def chunk(kind: bytes, data: bytes) -> bytes:
        return struct.pack(">I", len(data)) + kind + data + struct.pack(">I", zlib.crc32(kind + data) & 0xFFFFFFFF)

    ihdr = struct.pack(">IIBBBBB", width, height, 8, 2, 0, 0, 0)
    return b"\x89PNG\r\n\x1a\n" + chunk(b"IHDR", ihdr) + chunk(b"IDAT", zlib.compress(raw, 9)) + chunk(b"IEND", b"")


def upload(client: ApiClient, task_id: str, original: bytes, delivered: bytes, name: str = "shot.png") -> requests.Response:
    files = {
        "original": (name, original, "image/png"),
        "delivered": (name, delivered, "image/png"),
    }
    headers = {"authorization": f"Bearer {client.api_key}"}
    return requests.post(f"{client.base_url}/v1/tasks/{task_id}/attachments", files=files,
                         data={"originalType": "image/png", "deliveredType": "image/png", "name": name},
                         headers=headers, timeout=30)


def fake_lux_images(env, lux_run_id: str) -> dict:
    res = requests.get(f"{env.fake_lux_url}/fake/runs/{lux_run_id}/attachments",
                       headers={"authorization": f"Bearer {env.lux_key}"}, timeout=10)
    res.raise_for_status()
    return res.json()


def _hanging_run(client: ApiClient, project: dict, title: str, model: str = "fake/hang") -> tuple[dict, dict]:
    client.patch(f"/v1/projects/{project['id']}", {"agentModels": {
        "implementer": {"model": model}, "reviewer": {"model": "fake/scripted"}, "simplifier": {"model": "fake/scripted"}}})
    task = client.create_task(project["id"], title)
    assert client.post(f"/v1/tasks/{task['id']}/deliver").status_code == 201
    run = wait_until(lambda: next((r for r in client.task_runs(task["id"]) if r["phase"] == "implement" and r["status"] == "running"), None),
                     timeout=60, message="the implementer never started")
    return task, run


def lux_run_id(owner_dsn: str, run_id: str) -> str:
    return query(owner_dsn, "SELECT lux_run_id FROM runs WHERE id = %s", (run_id,))[0]["lux_run_id"]


# -- through the API ---------------------------------------------------------


def test_a_steers_image_reaches_lux_as_uploaded(env, client: ApiClient, forge_project: dict, owner_dsn: str, second_org: dict):
    task, run = _hanging_run(client, forge_project, "See this")
    delivered = png(1200, 760)
    res = upload(client, task["id"], png(2400, 1520), delivered, "checkout-yearly.png")
    assert res.status_code == 201, res.text
    image = res.json()
    assert image["width"] == 1200 and image["original"]["width"] == 2400

    # Another organisation cannot read it (sending it is refused in
    # orchestrator/images_test.go, TestASteerRefusesImagesThatAreNotItsTasksToSend).
    other = ApiClient(env.control_plane_url, second_org["api_key"])
    assert other.get(f"/v1/attachments/{image['id']}").status_code == 404
    assert client.get(f"/v1/attachments/{image['id']}").content == delivered

    steer = client.post(f"/v1/runs/{run['id']}/steer", {"text": "VAT stays at 0, see this", "attachmentIds": [image["id"]]})
    assert steer.status_code == 201, steer.text
    luxed = wait_until(lambda: fake_lux_images(env, lux_run_id(owner_dsn, run["id"])).get(steer.json()["id"]),
                       timeout=30, message="lux never got the image")
    assert luxed == [{"name": "checkout-yearly.png", "contentType": "image/png", "size": len(delivered),
                      "sha256": hashlib.sha256(delivered).hexdigest()}]

    # Sent once: it is the steer's now.
    again = client.post(f"/v1/runs/{run['id']}/steer", {"text": "again", "attachmentIds": [image["id"]]})
    assert again.status_code == 400 and again.json()["error"]["message"] == f"image {image['id']} was already sent"
    assert client.delete(f"/v1/attachments/{image['id']}").status_code == 409


def test_an_image_gone_from_storage_fails_its_steer_with_retry(env, client: ApiClient, forge_project: dict, owner_dsn: str):
    task, run = _hanging_run(client, forge_project, "Gone")
    image = upload(client, task["id"], png(20, 20), png(20, 20)).json()
    # Paused, so the steer waits; its object goes meanwhile.
    client.post(f"/v1/runs/{run['id']}/pause", {})
    wait_until(lambda: client.get_run(run["id"])["status"] == "paused", timeout=30, message="never paused")
    steer = client.post(f"/v1/runs/{run['id']}/steer", {"text": "look", "attachmentIds": [image["id"]]}).json()
    key = query(owner_dsn, "SELECT object_key FROM attachments WHERE id = %s", (image["id"],))[0]["object_key"]
    env.s3().delete_object(Bucket=env.s3_bucket, Key=key)
    client.post(f"/v1/runs/{run['id']}/resume", {})
    failed = wait_until(lambda: [e for e in client.events(runId=run["id"]) if e["eventType"] == "run.directive.failed"],
                        timeout=60, message="the steer never failed")
    assert failed[0]["payload"] == {"directiveId": steer["id"], "error": "its image shot.png is gone from storage"}


# -- in the browser ----------------------------------------------------------


def _shoot(page: Page, name: str) -> None:
    SHOTS.mkdir(parents=True, exist_ok=True)
    for theme in ("light", "dark"):
        page.evaluate("t => document.documentElement.setAttribute('data-theme', t)", theme)
        page.wait_for_timeout(150)
        page.screenshot(path=str(SHOTS / f"{name}-{theme}.png"))


def _files(page: Page, files: list[tuple[str, bytes, str]]):
    """A DataTransfer holding files, in the page."""
    return page.evaluate_handle("""(files) => {
      const dt = new DataTransfer();
      for (const [name, bytes, type] of files) dt.items.add(new File([new Uint8Array(bytes)], name, { type }));
      return dt;
    }""", [[n, list(b), t] for n, b, t in files])


def _paste(page: Page, files: list[tuple[str, bytes, str]]) -> None:
    _paste_into(page.get_by_placeholder("Steer the agent…"), _files(page, files))


def _paste_into(target, dt) -> bool:
    """Pastes `dt` on `target` as a person would; whether the page claimed it (preventDefault)."""
    return target.evaluate("""(el, dt) => {
      el.focus();
      const e = new ClipboardEvent("paste", { bubbles: true, cancelable: true, clipboardData: dt });
      return !el.dispatchEvent(e);
    }""", dt)


def _drag(target, kind: str, dt) -> bool:
    """Fires one drag event carrying `dt` on `target`; whether the page claimed it (preventDefault)."""
    return target.evaluate("""(el, [kind, dt]) => {
      const e = new DragEvent(kind, { bubbles: true, cancelable: true, dataTransfer: dt });
      return !el.dispatchEvent(e);
    }""", [kind, dt])


def _drop_on(target, dt, *, page: Page | None = None, shoot: str | None = None) -> bool:
    """Drags `dt` onto `target` and drops it, as the browser fires them; whether the drop was claimed.
    With `page`, the overlay is checked (and shot) while the files are over it."""
    _drag(target, "dragenter", dt)
    _drag(target, "dragover", dt)
    if page is not None:
        expect(page.get_by_test_id("drop-overlay")).to_be_visible()
        if shoot:
            _shoot(page, shoot)
    return _drag(target, "drop", dt)


@pytest.mark.ui
def test_paste_drop_and_paperclip_make_chips_that_go_with_a_steer(
    page: Page, web_url: str, env, client: ApiClient, org: dict, forge_project: dict, owner_dsn: str, console_errors: list
):
    task, run = _hanging_run(client, forge_project, "Checkout split")
    page.set_viewport_size({"width": 1440, "height": 900})
    sign_in(page, web_url, org["api_key"])
    page.goto(f"{web_url}#/session/{run['id']}")
    field = page.get_by_placeholder("Steer the agent…")
    expect(field).to_be_visible()

    # 1 · Attaching: the paperclip, its limits, and an upload held mid-way.
    held = []
    page.route("**/v1/tasks/*/attachments", lambda route: held.append(route))
    page.get_by_test_id("attach-input").set_input_files([
        {"name": "checkout-yearly.png", "mimeType": "image/png", "buffer": png(2400, 1520)},
    ])
    chips = page.get_by_test_id("attachment-chip")
    expect(chips).to_have_count(1)
    expect(chips.first).to_have_attribute("data-state", "uploading")
    wait_until(lambda: (page.wait_for_timeout(50), len(held))[1] == 1, timeout=20, message="the upload never started")
    field.fill("VAT stays at €0 when I switch to yearly — see the first one. It should match the design in the second.")
    expect(page.get_by_role("button", name="Steer")).to_be_disabled()
    expect(page.get_by_test_id("upload-hint")).to_have_text("Uploading 1 of 1…")
    _shoot(page, "1-attaching")
    page.get_by_test_id("attach-button").hover()
    expect(page.get_by_role("tooltip")).to_contain_text("up to 10 MB each")
    _shoot(page, "1-attaching-limits")
    page.mouse.move(900, 150)
    page.keyboard.press("Escape")
    expect(page.get_by_role("tooltip")).to_have_count(0)
    for route in held:
        route.continue_()
    page.unroute("**/v1/tasks/*/attachments")
    expect(chips.first).to_have_attribute("data-state", "ready", timeout=20_000)

    # Paste a second.
    _paste(page, [("image.png", png(900, 900, (160, 90, 250)), "image/png")])
    expect(chips).to_have_count(2)
    expect(chips.nth(1)).to_have_attribute("data-state", "ready", timeout=20_000)

    # 2 · Drop on the conversation: the overlay says who and when.
    dt = _files(page, [("console.png", png(1000, 560, (200, 40, 40)), "image/png")])
    chat = page.locator(".runDrop")
    chat.dispatch_event("dragenter", {"dataTransfer": dt})
    overlay = page.get_by_test_id("drop-overlay")
    expect(overlay).to_contain_text("Drop to attach the image")
    expect(overlay).to_contain_text("They go with your next steer to")
    _shoot(page, "2-drop")
    chat.dispatch_event("drop", {"dataTransfer": dt})
    expect(overlay).to_have_count(0)
    expect(chips).to_have_count(3)
    expect(chips.nth(2)).to_have_attribute("data-state", "ready", timeout=20_000)
    chips.nth(2).get_by_role("button", name="Remove console.png").click()
    expect(chips).to_have_count(2)

    # 3 · Sent: the turn shows its images; lux has them with the words.
    page.get_by_role("button", name="Steer").click()
    expect(chips).to_have_count(0)
    turn = page.get_by_test_id("human-turn").last
    expect(turn.get_by_test_id("message-image")).to_have_count(2)
    expect(turn.get_by_test_id("message-image").first.locator("img")).to_be_visible(timeout=20_000)
    steered = wait_until(lambda: [e for e in client.events(runId=run["id"]) if e["eventType"] == "run.steered"],
                         timeout=15, message="no steer in the ledger")
    ids = [a["id"] for a in steered[0]["payload"]["attachments"]]
    names = [a["name"] for a in steered[0]["payload"]["attachments"]]
    assert len(ids) == 2 and names[0] == "checkout-yearly.png"
    luxed = wait_until(lambda: fake_lux_images(env, lux_run_id(owner_dsn, run["id"])).get(steered[0]["payload"]["directiveId"]),
                       timeout=30, message="lux never got the images")
    assert [a["name"] for a in luxed] == names
    for a, image_id in zip(luxed, ids):
        assert a["sha256"] == hashlib.sha256(client.get(f"/v1/attachments/{image_id}").content).hexdigest()
    # The removed one is gone, row and (after the sweep) object.
    assert query(owner_dsn, "SELECT count(*) AS n FROM attachments WHERE task_id = %s", (task["id"],))[0]["n"] == 2
    page.mouse.move(0, 0)
    _shoot(page, "3-sent")

    # 4 · The viewer: what the agent got, scaled from what; arrows; Esc.
    turn.get_by_test_id("message-image").first.click()
    viewer = page.get_by_test_id("image-viewer")
    expect(viewer).to_be_visible()
    expect(page.get_by_test_id("viewer-meta")).to_contain_text("The agent got 2000×1267")
    expect(page.get_by_test_id("viewer-meta")).to_contain_text("scaled from 2400×1520")
    expect(page.get_by_test_id("viewer-original")).to_have_text("Original 2400×1520")
    expect(page.get_by_test_id("viewer-image")).to_be_visible(timeout=20_000)
    # The line under the image is on screen, not pushed off it by a tall image.
    expect(page.get_by_test_id("viewer-meta")).to_be_in_viewport(ratio=1)
    _shoot(page, "4-viewer")
    page.get_by_test_id("viewer-original").click()
    expect(page.get_by_test_id("viewer-original")).to_have_text("Sent 2000×1267")
    page.keyboard.press("ArrowRight")
    expect(page.get_by_test_id("viewer-meta")).not_to_contain_text("scaled from")
    page.keyboard.press("Escape")
    expect(viewer).to_have_count(0)
    assert console_errors == []


@pytest.mark.ui
def test_an_image_that_cannot_go_keeps_send_off(
    page: Page, web_url: str, client: ApiClient, org: dict, forge_project: dict, console_errors: list
):
    _, run = _hanging_run(client, forge_project, "Limits")
    page.set_viewport_size({"width": 1440, "height": 900})
    sign_in(page, web_url, org["api_key"])
    page.goto(f"{web_url}#/session/{run['id']}")
    page.get_by_placeholder("Steer the agent…").fill("Same thing on Safari:")
    page.get_by_test_id("attach-input").set_input_files([
        {"name": "console.png", "mimeType": "image/png", "buffer": png(400, 300)},
        {"name": "spec.pdf", "mimeType": "application/pdf", "buffer": b"%PDF-1.7\n" + b"x" * 200},
        {"name": "fake.png", "mimeType": "image/png", "buffer": b"<svg xmlns='http://www.w3.org/2000/svg'/>"},
    ])
    chips = page.get_by_test_id("attachment-chip")
    expect(chips).to_have_count(3)
    expect(chips.nth(1)).to_have_attribute("data-state", "error")
    expect(chips.nth(1)).to_contain_text("PDF not supported")
    expect(chips.nth(2)).to_have_attribute("data-state", "error")
    expect(chips.nth(0)).to_have_attribute("data-state", "ready", timeout=20_000)
    expect(page.get_by_test_id("attachment-warning")).to_have_text(
        "2 can't be sent: only PNG, JPEG, WebP and GIF can be sent. Remove them to send the rest.")
    expect(page.get_by_role("button", name="Steer")).to_be_disabled()
    _shoot(page, "6-limits")
    chips.nth(2).get_by_role("button").click()
    chips.nth(1).get_by_role("button").click()
    expect(page.get_by_role("button", name="Steer")).to_be_enabled()
    assert console_errors == []


@pytest.mark.ui
def test_a_failed_image_steer_keeps_its_image_with_retry(
    page: Page, web_url: str, env, client: ApiClient, org: dict, forge_project: dict, owner_dsn: str, console_errors: list
):
    task, run = _hanging_run(client, forge_project, "Failed delivery")
    image = upload(client, task["id"], png(1000, 560, (200, 40, 40)), png(1000, 560, (200, 40, 40)), "console.png").json()
    client.post(f"/v1/runs/{run['id']}/pause", {})
    wait_until(lambda: client.get_run(run["id"])["status"] == "paused", timeout=30, message="never paused")
    client.post(f"/v1/runs/{run['id']}/steer", {"text": "And here's the console error from Spain.", "attachmentIds": [image["id"]]})
    key = query(owner_dsn, "SELECT object_key FROM attachments WHERE id = %s", (image["id"],))[0]["object_key"]
    shown = env.s3().get_object(Bucket=env.s3_bucket, Key=key)["Body"].read()
    env.s3().delete_object(Bucket=env.s3_bucket, Key=key)
    client.post(f"/v1/runs/{run['id']}/resume", {})
    wait_until(lambda: [e for e in client.events(runId=run["id"]) if e["eventType"] == "run.directive.failed"],
               timeout=60, message="the steer never failed")
    # Back in storage, so the turn can show it.
    env.s3().put_object(Bucket=env.s3_bucket, Key=key, Body=shown, ContentType="image/png")
    page.set_viewport_size({"width": 1440, "height": 900})
    sign_in(page, web_url, org["api_key"])
    page.goto(f"{web_url}#/session/{run['id']}")
    turn = page.get_by_test_id("human-turn").last
    expect(turn).to_contain_text("Not delivered: its image console.png is gone from storage")
    expect(turn.get_by_role("button", name="Retry")).to_be_visible()
    expect(turn.get_by_test_id("message-image").locator("img")).to_be_visible(timeout=20_000)
    _shoot(page, "6-failed")
    # Retry sends the same words again, and the image with them.
    turn.get_by_role("button", name="Retry").click()
    retried = wait_until(lambda: [e for e in client.events(runId=run["id"]) if e["eventType"] == "run.steered"
                                  and e["payload"].get("supersedes")], timeout=15, message="Retry sent nothing")
    luxed = wait_until(lambda: fake_lux_images(env, lux_run_id(owner_dsn, run["id"])).get(retried[0]["payload"]["directiveId"]),
                       timeout=30, message="lux never got the image with the retry")
    assert [a["name"] for a in luxed] == ["console.png"]
    # The 404 of the object read before it was put back is the only error.
    assert all("404" in e for e in console_errors), console_errors


@pytest.mark.ui
def test_an_answer_carries_a_screenshot(
    page: Page, web_url: str, env, client: ApiClient, org: dict, forge_project: dict, owner_dsn: str, console_errors: list
):
    client.patch(f"/v1/projects/{forge_project['id']}", {"agentModels": {
        "implementer": {"model": "fake/ask"}, "reviewer": {"model": "fake/hang"}, "simplifier": {"model": "fake/scripted"}}})
    task = client.create_task(forge_project["id"], "Phone layout")
    assert client.post(f"/v1/tasks/{task['id']}/deliver").status_code == 201
    run = wait_until(lambda: next((r for r in client.task_runs(task["id"]) if r["phase"] == "implement"), None), timeout=60, message="no run")
    wait_until(lambda: client.get(f"/v1/questions?runId={run['id']}").json()["questions"], timeout=60, message="no question")
    page.set_viewport_size({"width": 1440, "height": 900})
    sign_in(page, web_url, org["api_key"])
    page.goto(f"{web_url}#/session/{run['id']}")
    field = page.get_by_placeholder("Type your answer…")
    expect(field).to_be_visible()
    field.fill("It overflows — VAT amount is cut off on the right.")
    page.get_by_test_id("attach-input").set_input_files([{"name": "phone.png", "mimeType": "image/png", "buffer": png(390, 844)}])
    expect(page.get_by_test_id("attachment-chip")).to_have_attribute("data-state", "ready", timeout=20_000)
    _shoot(page, "5-answer")
    page.get_by_role("button", name="Answer").click()
    answered = wait_until(lambda: [e for e in client.events(runId=run["id"]) if e["eventType"] == "question.answered"],
                          timeout=15, message="no answer")
    directive = answered[0]["payload"]["directiveId"]
    luxed = wait_until(lambda: fake_lux_images(env, lux_run_id(owner_dsn, run["id"])).get(directive), timeout=30, message="lux never got it")
    assert [a["name"] for a in luxed] == ["phone.png"]
    expect(page.get_by_test_id("human-turn").last.get_by_test_id("message-image")).to_have_count(1)
    assert console_errors == []


@pytest.mark.ui
def test_a_task_created_with_an_image_gives_it_to_its_first_agent(
    page: Page, web_url: str, env, client: ApiClient, org: dict, forge_project: dict, owner_dsn: str, console_errors: list
):
    client.patch(f"/v1/projects/{forge_project['id']}", {"agentModels": {
        "implementer": {"model": "fake/hang"}, "reviewer": {"model": "fake/scripted"}, "simplifier": {"model": "fake/scripted"}}})
    page.set_viewport_size({"width": 1440, "height": 900})
    sign_in(page, web_url, org["api_key"])
    page.get_by_test_id("new-task").click()
    page.get_by_test_id("task-title").fill("Build the summary from the design")
    page.get_by_test_id("task-goal").fill("Match the design attached.")
    page.get_by_test_id("task-attach-input").set_input_files([{"name": "Summary v3.png", "mimeType": "image/png", "buffer": png(900, 900, (160, 90, 250))}])
    expect(page.get_by_test_id("attachment-chip")).to_have_attribute("data-state", "ready", timeout=20_000)
    expect(page.get_by_test_id("task-save")).to_be_disabled()
    expect(page.get_by_test_id("task-create-deliver")).to_be_enabled()
    _shoot(page, "5-task-prompt")
    page.get_by_test_id("task-create-deliver").click()
    expect(page.get_by_test_id("task-screen")).to_be_visible(timeout=30_000)
    task_id = wait_until(lambda: (query(owner_dsn, "SELECT id FROM tasks WHERE title = %s", ("Build the summary from the design",)) or [None])[0],
                         timeout=15, message="no task")["id"]
    run = wait_until(lambda: next((r for r in client.task_runs(task_id) if r["phase"] == "implement" and r["status"] == "running"), None),
                     timeout=60, message="the implementer never started")
    images = wait_until(lambda: fake_lux_images(env, lux_run_id(owner_dsn, run["id"])).get("prompt"), timeout=30, message="no prompt images")
    assert [a["name"] for a in images] == ["Summary v3.png"] and images[0]["contentType"] == "image/png"
    page.goto(f"{web_url}#/session/{run['id']}")
    expect(page.get_by_test_id("message-image")).to_have_count(1, timeout=30_000)
    assert console_errors == []


def _open_new_task(page: Page, web_url: str, org: dict) -> None:
    """The New task dialog, on the board of the org's project (the `forge_project` fixture makes it)."""
    page.set_viewport_size({"width": 1440, "height": 900})
    sign_in(page, web_url, org["api_key"])
    page.get_by_test_id("new-task").click()
    expect(page.get_by_test_id("task-title")).to_be_visible()


@pytest.mark.ui
def test_an_image_dropped_on_the_goal_goes_to_the_tray(page: Page, web_url: str, org: dict, forge_project: dict, console_errors: list):
    _open_new_task(page, web_url, org)
    page.get_by_test_id("task-goal").fill("Match the design attached.")
    dt = _files(page, [("design.png", png(600, 400), "image/png")])
    assert _drop_on(page.get_by_test_id("task-goal"), dt, page=page, shoot="5-task-prompt-drop")
    expect(page.get_by_test_id("drop-overlay")).to_have_count(0)
    chips = page.get_by_test_id("attachment-chip")
    expect(chips).to_have_count(1)
    expect(chips.first).to_have_attribute("data-state", "ready", timeout=20_000)
    expect(page.get_by_test_id("task-goal")).to_have_value("Match the design attached.")
    assert console_errors == []


@pytest.mark.ui
def test_an_image_dropped_on_the_title_goes_to_the_tray(page: Page, web_url: str, org: dict, forge_project: dict, console_errors: list):
    _open_new_task(page, web_url, org)
    dt = _files(page, [("bug.png", png(300, 200, (200, 40, 40)), "image/png")])
    assert _drop_on(page.get_by_test_id("task-title"), dt, page=page)
    chips = page.get_by_test_id("attachment-chip")
    expect(chips).to_have_count(1)
    expect(chips.first).to_have_attribute("data-state", "ready", timeout=20_000)
    # A text drag is left to the field: no overlay, nothing claimed.
    text = page.evaluate_handle("() => { const dt = new DataTransfer(); dt.setData('text/plain', 'words'); return dt; }")
    assert not _drag(page.get_by_test_id("task-title"), "dragenter", text)
    expect(page.get_by_test_id("drop-overlay")).to_have_count(0)
    assert console_errors == []


@pytest.mark.ui
def test_an_image_pasted_in_the_title_goes_to_the_tray_and_text_does_not(page: Page, web_url: str, org: dict, forge_project: dict, console_errors: list):
    _open_new_task(page, web_url, org)
    title = page.get_by_test_id("task-title")
    title.fill("Summary")
    # Text alone: left to the field.
    text = page.evaluate_handle("() => { const dt = new DataTransfer(); dt.setData('text/plain', ' page'); return dt; }")
    assert not _paste_into(title, text)
    expect(page.get_by_test_id("attachment-chip")).to_have_count(0)
    expect(title).to_have_value("Summary")
    # An image: to the tray, the paste claimed so nothing lands in the title.
    assert _paste_into(title, _files(page, [("pasted.png", png(400, 300, (160, 90, 250)), "image/png")]))
    chips = page.get_by_test_id("attachment-chip")
    expect(chips).to_have_count(1)
    expect(chips.first).to_have_attribute("data-state", "ready", timeout=20_000)
    expect(title).to_have_value("Summary")
    # An image with text beside it (as some apps copy): the image is taken, the text let through.
    both = _files(page, [("both.png", png(200, 200), "image/png")])
    both.evaluate("dt => dt.setData('text/plain', 'both.png')")
    assert not _paste_into(page.get_by_test_id("task-goal"), both)
    expect(chips).to_have_count(2)
    assert console_errors == []


@pytest.mark.ui
def test_attach_images_is_a_labelled_button(page: Page, web_url: str, org: dict, forge_project: dict, console_errors: list):
    _open_new_task(page, web_url, org)
    attach = page.get_by_role("button", name="Attach images")
    expect(attach).to_be_visible()
    expect(attach).to_have_attribute("data-testid", "task-attach")
    expect(page.get_by_test_id("task-images")).to_contain_text("Paste, drop or attach.")
    _shoot(page, "5-task-prompt-empty")
    attach.hover()
    expect(page.get_by_role("tooltip")).to_contain_text("up to 10 MB each")
    assert console_errors == []


@pytest.mark.ui
def test_a_file_dropped_on_the_dialog_without_storage_is_refused_in_place(page: Page, web_url: str, org: dict, forge_project: dict):
    page.route("**/v1/attachment-limits", lambda route: route.fulfill(json={
        "enabled": False, "types": ["image/png"], "perMessage": 10, "originalBytes": 10_000_000, "maxSide": 2000}))
    _open_new_task(page, web_url, org)
    page.get_by_test_id("task-title").fill("Kept")
    expect(page.get_by_role("button", name="Attach images")).to_be_disabled()
    dt = _files(page, [("design.png", png(60, 40), "image/png")])
    goal = page.get_by_test_id("task-goal")
    _drag(goal, "dragenter", dt)
    assert _drag(goal, "dragover", dt)
    expect(page.get_by_test_id("drop-overlay")).to_contain_text("Image storage isn't set up")
    # Claimed, so the browser does not open the file in place of the page.
    assert _drag(goal, "drop", dt)
    expect(page.get_by_test_id("attachment-chip")).to_have_count(0)
    # Outside the dialog, on its backdrop, too.
    assert _drag(page.locator("body"), "drop", dt)
    expect(page.get_by_test_id("task-title")).to_have_value("Kept")


@pytest.mark.ui
def test_the_composer_takes_images_dropped_anywhere_on_the_session(
    page: Page, web_url: str, client: ApiClient, org: dict, forge_project: dict, console_errors: list
):
    _, run = _hanging_run(client, forge_project, "Anywhere")
    page.set_viewport_size({"width": 1440, "height": 900})
    sign_in(page, web_url, org["api_key"])
    page.goto(f"{web_url}#/session/{run['id']}")
    field = page.get_by_placeholder("Steer the agent…")
    expect(field).to_be_visible()
    chips = page.get_by_test_id("attachment-chip")
    places = [field, page.locator(".runChat [data-following]").first, page.locator("[data-testid=run-screen] header").first]
    for i, place in enumerate(places):
        assert _drop_on(place, _files(page, [(f"drop-{i}.png", png(120, 80), "image/png")]), page=page)
        expect(chips).to_have_count(i + 1)
    # Text pasted with an image goes into the field; the image to the tray.
    both = _files(page, [("both.png", png(200, 200), "image/png")])
    both.evaluate("dt => dt.setData('text/plain', 'both.png')")
    assert not _paste_into(field, both)
    expect(chips).to_have_count(4)
    _paste(page, [("pasted.png", png(90, 90), "image/png")])
    expect(chips).to_have_count(5)
    for i in range(5):
        expect(chips.nth(i)).to_have_attribute("data-state", "ready", timeout=20_000)
    assert console_errors == []
