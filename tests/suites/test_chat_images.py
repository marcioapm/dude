"""Images a person sends an agent, end to end.

Through the public API and the built web app, against the fake lux and the
suite's S3: an image is uploaded to a task, sent with a steer or an answer,
or shown in the task's goal or criteria, read by the orchestrator from
storage and given to lux with its words; the fake lux records what each
input carried (name, type, size, sha256), and the transcript shows it. In
the browser, every way into a steer — the paperclip, paste, a drop on the
conversation — makes a chip; an image that cannot go keeps Send off; the
viewer says what the agent got. In the task dialog, an image goes into the
text where it was pasted or dropped, and every agent is told where it was.

Screenshots of each mockup screen, light and dark, go to
$DUDE_TEST_SHOTS (default /var/tmp/cimg-dude-shots/).
"""

from __future__ import annotations

import hashlib
import os
import re
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
    client.patch(f"/v1/projects/{project['id']}", {"agentModels": client.on_models({
        "implementer": model, "reviewer": "fake/scripted", "simplifier": "fake/scripted"})})
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


def _files(page: Page, files: list[tuple[str, bytes, str]], text: str | None = None):
    """A DataTransfer holding files, and `text` as text/plain, in the page."""
    return page.evaluate_handle("""([files, text]) => {
      const dt = new DataTransfer();
      for (const [name, bytes, type] of files) dt.items.add(new File([new Uint8Array(bytes)], name, { type }));
      if (text !== null) dt.setData('text/plain', text);
      return dt;
    }""", [[[n, list(b), t] for n, b, t in files], text])


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
    client.patch(f"/v1/projects/{forge_project['id']}", {"agentModels": client.on_models({
        "implementer": "fake/ask", "reviewer": "fake/hang", "simplifier": "fake/scripted"})})
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


def lux_spec(env, lux_run: str) -> dict:
    """The spec dude submitted for a lux Run, as the fake lux keeps it."""
    res = requests.get(f"{env.lux_url}/v1/runs/{lux_run}", headers={"authorization": f"Bearer {env.lux_key}"}, timeout=10)
    res.raise_for_status()
    return res.json()["spec"]


def _real_models(client: ApiClient, project: dict, **roles: str) -> None:
    """Roles on models whose prompt is dude's own: the scripted agent's (fake/*) prompt is its
    script, while any other model gets the real prompt and is played by the fake lux as scripted.
    The implementer is on one unless told otherwise."""
    client.patch(f"/v1/projects/{project['id']}", {"agentModels": client.on_models({
        "implementer": "llm-impl", "reviewer": "fake/scripted", "simplifier": "fake/scripted", **roles})})


def _submitted(client: ApiClient, owner_dsn: str, task_id: str, phase: str) -> str:
    """The lux Run of the task's first Run of `phase`, once it is submitted."""
    def find():
        run = next((r for r in client.task_runs(task_id) if r["phase"] == phase), None)
        return run and lux_run_id(owner_dsn, run["id"])
    return wait_until(find, timeout=90, message=f"no {phase} Run submitted")


def _goal_value(page: Page) -> str:
    return page.get_by_test_id("task-goal").input_value()


def _references(text: str) -> list[str]:
    return re.findall(r"!\[[^\]]*\]\(attachment:(att_[A-Za-z0-9]+)\)", text)


def _attached(owner_dsn: str, task_id: str) -> list[str]:
    """The task's images its text references, as the backend attached them, in their order."""
    return [r["id"] for r in query(owner_dsn, "SELECT id FROM attachments WHERE task_id = %s AND for_prompt AND attached_at IS NOT NULL ORDER BY position",
                                   (task_id,))]


def _caret_after(page: Page, field: str, text: str) -> None:
    """Puts the caret in `field` right after the first `text`, as a click there would."""
    page.get_by_test_id(field).evaluate("(el, t) => { el.focus(); const at = el.value.indexOf(t) + t.length; el.setSelectionRange(at, at); }", text)


@pytest.mark.ui
def test_an_image_pasted_mid_goal_goes_in_at_the_caret_and_reaches_the_agent_where_it_was(
    page: Page, web_url: str, env, client: ApiClient, org: dict, forge_project: dict, owner_dsn: str, console_errors: list
):
    _real_models(client, forge_project)
    _open_new_task(page, web_url, org)
    page.get_by_test_id("task-title").fill("Build the summary from the design")
    page.get_by_test_id("task-goal").fill("The summary should look like this: and keep the totals right-aligned.")
    _caret_after(page, "task-goal", "like this:")
    # Pasted at the caret: the paste is claimed, the image goes in where the caret was.
    assert _paste_into(page.get_by_test_id("task-goal"), _files(page, [("Summary v3.png", png(900, 900, (160, 90, 250)), "image/png")]))
    expect(page.get_by_test_id("task-goal")).to_have_value(re.compile(r"!\[Summary v3\.png\]\(attachment:att_local\d+\)"), timeout=20_000)
    goal = _goal_value(page)
    assert re.fullmatch(r"The summary should look like this:\n\n!\[Summary v3\.png\]\(attachment:att_local\d+\)\n\nand keep the totals right-aligned\.", goal), goal
    # The tray is gone.
    expect(page.get_by_test_id("task-images")).to_have_count(0)
    expect(page.get_by_test_id("attachment-chip")).to_have_count(0)
    # Read shows the image, in its place in the text.
    page.get_by_test_id("task-read").click()
    reading = page.get_by_test_id("task-reading")
    image = reading.get_by_test_id("markdown-image").locator("img")
    expect(image).to_have_count(1)
    expect(image).to_have_attribute("src", re.compile(r"^blob:"))
    assert reading.evaluate("""el => {
      const img = el.querySelector('[data-testid=markdown-image]');
      const text = el.innerText;
      return text.indexOf('like this:') < text.indexOf('and keep') && img.closest('p') !== null;
    }""")
    _shoot(page, "5-task-inline-read")
    page.get_by_test_id("task-read").click()

    page.get_by_test_id("task-create-deliver").click()
    expect(page.get_by_test_id("task-screen")).to_be_visible(timeout=30_000)
    task_id = wait_until(lambda: (query(owner_dsn, "SELECT id FROM tasks WHERE title = %s", ("Build the summary from the design",)) or [None])[0],
                         timeout=15, message="no task")["id"]
    saved = client.get(f"/v1/tasks/{task_id}").json()["goal"]
    ids = _references(saved)
    assert len(ids) == 1 and not ids[0].startswith("att_local"), saved
    assert _attached(owner_dsn, task_id) == ids
    # The task screen shows it in place too, read through the API.
    expect(page.get_by_test_id("markdown-image").locator("img")).to_have_attribute("src", re.compile(r"^blob:"), timeout=20_000)

    lux_run = _submitted(client, owner_dsn, task_id, "implement")
    images = wait_until(lambda: fake_lux_images(env, lux_run).get("prompt"), timeout=30, message="no prompt images")
    assert [a["name"] for a in images] == ["Summary v3.png"] and images[0]["contentType"] == "image/png"
    prompt = lux_spec(env, lux_run)["workload"]["prompt"]
    assert "The summary should look like this:\n\n[Image 1: Summary v3.png]\n\nand keep the totals right-aligned." in prompt, prompt
    assert "attachment:" not in prompt
    assert console_errors == []


@pytest.mark.ui
def test_an_image_dropped_on_the_criteria_goes_to_the_reviewers(
    page: Page, web_url: str, env, client: ApiClient, org: dict, forge_project: dict, owner_dsn: str, console_errors: list
):
    # The implementer finishes, so reviewers run, on a model given dude's real prompt.
    _real_models(client, forge_project, implementer="fake/scripted", reviewer="llm-review")
    _open_new_task(page, web_url, org)
    page.get_by_test_id("task-title").fill("Totals line up")
    page.get_by_test_id("task-goal").fill("Totals in the summary should be right-aligned.")
    page.get_by_test_id("task-criteria").fill("- [ ] Totals line up, as in ")
    dt = _files(page, [("evidence.png", png(300, 200, (200, 40, 40)), "image/png")])
    _caret_after(page, "task-criteria", "as in ")
    assert _drop_on(page.get_by_test_id("task-criteria"), dt, page=page, shoot="5-task-inline-drop")
    expect(page.get_by_test_id("task-criteria")).to_have_value(re.compile(r"^- \[ \] Totals line up, as in !\[evidence\.png\]\(attachment:att_local\d+\)$"),
                                                               timeout=20_000)
    expect(page.get_by_test_id("task-goal")).to_have_value("Totals in the summary should be right-aligned.")
    # Off the dialog, on its backdrop, the window claims the drag too, so the browser does not open the file.
    body = page.locator("body")
    assert _drag(body, "dragover", dt)
    assert _drag(body, "drop", dt)
    page.get_by_test_id("task-create-deliver").click()
    expect(page.get_by_test_id("task-screen")).to_be_visible(timeout=30_000)
    task_id = wait_until(lambda: (query(owner_dsn, "SELECT id FROM tasks WHERE title = %s", ("Totals line up",)) or [None])[0],
                         timeout=15, message="no task")["id"]
    lux_run = _submitted(client, owner_dsn, task_id, "review")
    images = wait_until(lambda: fake_lux_images(env, lux_run).get("prompt"), timeout=30, message="the reviewer got no images")
    assert [a["name"] for a in images] == ["evidence.png"]
    assert "- Totals line up, as in [Image 1: evidence.png]" in lux_spec(env, lux_run)["workload"]["prompt"]
    assert console_errors == []


@pytest.mark.ui
def test_a_task_created_with_an_image_keeps_it_and_an_edit_can_take_it_out(
    page: Page, web_url: str, client: ApiClient, org: dict, forge_project: dict, owner_dsn: str, console_errors: list
):
    _open_new_task(page, web_url, org)
    page.get_by_test_id("task-title").fill("Saved with a picture")
    page.get_by_test_id("task-goal").fill("The header overlaps the menu on a narrow window.")
    page.get_by_test_id("task-goal").focus()
    with page.expect_file_chooser() as chooser:
        page.get_by_test_id("task-attach").click()
    chooser.value.set_files([{"name": "header.png", "mimeType": "image/png", "buffer": png(400, 120)}])
    expect(page.get_by_test_id("task-goal")).to_have_value(re.compile(r"attachment:att_local\d+"), timeout=20_000)
    # Plain Create, not delivered: the image stays with the saved task.
    expect(page.get_by_test_id("task-save")).to_be_enabled()
    page.get_by_test_id("task-save").click()
    expect(page.get_by_test_id("task-goal")).to_have_count(0, timeout=20_000)
    task_id = wait_until(lambda: (query(owner_dsn, "SELECT id FROM tasks WHERE title = %s", ("Saved with a picture",)) or [None])[0],
                         timeout=15, message="no task")["id"]
    ids = _references(client.get(f"/v1/tasks/{task_id}").json()["goal"])
    assert len(ids) == 1 and _attached(owner_dsn, task_id) == ids

    page.goto(f"{web_url}#/task/{task_id}")
    page.get_by_test_id("edit-task").click()
    expect(page.get_by_test_id("task-goal")).to_have_value(re.compile(re.escape(f"(attachment:{ids[0]})")))
    page.get_by_test_id("task-goal").fill("The header overlaps the menu on a narrow window, at any width under 600px.")
    page.get_by_test_id("task-save").click()
    expect(page.get_by_test_id("task-goal")).to_have_count(0, timeout=20_000)
    assert _attached(owner_dsn, task_id) == []
    row = query(owner_dsn, "SELECT attached_at, detached_at FROM attachments WHERE id = %s", (ids[0],))[0]
    assert row["attached_at"] is None and row["detached_at"] is not None
    assert console_errors == []


@pytest.mark.ui
def test_edit_task_takes_an_image_at_once_and_delivery_gives_it_to_the_agent(
    page: Page, web_url: str, env, client: ApiClient, org: dict, forge_project: dict, owner_dsn: str, console_errors: list
):
    # The implementer is given the real prompt and finishes; the reviewers hang, so the delivery runs on.
    _real_models(client, forge_project, reviewer="fake/hang")
    task = client.create_task(forge_project["id"], "Not started yet", goal="The checkout button is hidden behind the cookie banner.")
    page.set_viewport_size({"width": 1440, "height": 900})
    sign_in(page, web_url, org["api_key"])
    page.goto(f"{web_url}#/task/{task['id']}")
    page.get_by_test_id("edit-task").click()
    _caret_after(page, "task-goal", "hidden")
    dt = _files(page, [("banner.png", png(500, 300, (40, 160, 90)), "image/png")])
    assert _drop_on(page.get_by_test_id("task-goal"), dt, page=page)
    # Uploaded at once: the task exists, so the reference is a real attachment's.
    expect(page.get_by_test_id("task-goal")).to_have_value(re.compile(r"hidden\n\n!\[banner\.png\]\(attachment:att_(?!local)[A-Za-z0-9]+\)\n\nbehind"),
                                                           timeout=20_000)
    uploaded = query(owner_dsn, "SELECT id, attached_at FROM attachments WHERE task_id = %s", (task["id"],))
    assert len(uploaded) == 1 and uploaded[0]["attached_at"] is None
    page.get_by_test_id("task-save").click()
    expect(page.get_by_test_id("task-goal")).to_have_count(0, timeout=20_000)
    assert _attached(owner_dsn, task["id"]) == [uploaded[0]["id"]]

    assert client.post(f"/v1/tasks/{task['id']}/deliver").status_code == 201
    lux_run = _submitted(client, owner_dsn, task["id"], "implement")
    images = wait_until(lambda: fake_lux_images(env, lux_run).get("prompt"), timeout=30, message="no prompt images")
    assert [a["name"] for a in images] == ["banner.png"]
    assert "hidden\n\n[Image 1: banner.png]\n\nbehind" in lux_spec(env, lux_run)["workload"]["prompt"]
    run = wait_until(lambda: next((r for r in client.task_runs(task["id"]) if r["phase"] == "review" and r["status"] == "running"), None),
                     timeout=90, message="no reviewer running")

    # While it runs, the text and so its images are fixed: the drop zone, paste and button say so.
    page.reload()
    page.get_by_test_id("edit-task").click()
    attach = page.get_by_test_id("task-attach")
    expect(attach).to_be_disabled()
    reason = "Delivery is running, so what the task asks for is fixed, and its images too."
    attach.locator("xpath=..").hover()
    expect(page.get_by_role("tooltip")).to_contain_text(reason)
    title = page.get_by_test_id("task-title")
    dt = _files(page, [("late.png", png(50, 50), "image/png")])
    _drag(title, "dragenter", dt)
    assert _drag(title, "dragover", dt)
    expect(page.get_by_test_id("drop-overlay")).to_contain_text(reason)
    assert _drag(title, "drop", dt)
    expect(page.get_by_test_id("drop-overlay")).to_have_count(0)
    # A paste is not claimed (nothing taken), and nothing is uploaded.
    assert _paste_into(title, _files(page, [("late.png", png(50, 50), "image/png")])) is False
    assert len(query(owner_dsn, "SELECT id FROM attachments WHERE task_id = %s", (task["id"],))) == 1
    _shoot(page, "5-task-inline-locked")
    page.keyboard.press("Escape")

    # Aborted: what it asks for can change again, images included.
    assert client.post(f"/v1/runs/{run['id']}/abort", {}).status_code == 200
    wait_until(lambda: client.get(f"/v1/tasks/{task['id']}").json()["status"] == "aborted", timeout=30, message="never aborted")
    page.reload()
    page.get_by_test_id("edit-task").click()
    expect(page.get_by_test_id("task-attach")).to_be_enabled()
    page.get_by_test_id("task-goal").focus()
    page.get_by_test_id("task-goal").press("End")
    assert _drop_on(page.get_by_test_id("task-goal"), _files(page, [("after.png", png(60, 60), "image/png")]))
    expect(page.get_by_test_id("task-goal")).to_have_value(re.compile(r"!\[after\.png\]\(attachment:att_(?!local)[A-Za-z0-9]+\)"), timeout=20_000)
    page.get_by_test_id("task-save").click()
    expect(page.get_by_test_id("task-goal")).to_have_count(0, timeout=20_000)
    assert len(_attached(owner_dsn, task["id"])) == 2
    assert console_errors == []


def _image_drag(page: Page, source, target_panel, li_index: int) -> None:
    """Drags an image in Preview (HTML5 DnD, as the browser fires it) onto a criteria panel,
    level with the bottom of its `li_index`-th criterion: the slot under it."""
    page.evaluate("""([fig, panel, i]) => {
      const dt = new DataTransfer();
      const r = fig.getBoundingClientRect();
      fig.dispatchEvent(new DragEvent('dragstart', { bubbles: true, cancelable: true, dataTransfer: dt, clientX: r.left + 5, clientY: r.top + 5 }));
      const li = panel.querySelectorAll(':scope li')[i];
      const y = li.getBoundingClientRect().bottom;
      const x = panel.getBoundingClientRect().left + 40;
      panel.dispatchEvent(new DragEvent('dragenter', { bubbles: true, cancelable: true, dataTransfer: dt, clientX: x, clientY: y }));
      panel.dispatchEvent(new DragEvent('dragover', { bubbles: true, cancelable: true, dataTransfer: dt, clientX: x, clientY: y }));
      panel.dispatchEvent(new DragEvent('drop', { bubbles: true, cancelable: true, dataTransfer: dt, clientX: x, clientY: y }));
      fig.dispatchEvent(new DragEvent('dragend', { bubbles: true, dataTransfer: dt }));
    }""", [source.element_handle(), target_panel.element_handle(), li_index])


@pytest.mark.ui
def test_an_image_laid_out_in_preview_keeps_its_place_and_size_and_the_agent_reads_only_where_it_is(
    page: Page, web_url: str, env, client: ApiClient, org: dict, forge_project: dict, owner_dsn: str, console_errors: list
):
    # The implementer is given the real prompt and finishes; the reviewers hang, so the delivery runs on.
    _real_models(client, forge_project, reviewer="fake/hang")
    task = client.create_task(forge_project["id"], "Lay out the mock", goal="The receipt should look like the mock below.",
                              acceptanceCriteria=["Totals are right-aligned", "The logo sits top left", "Prints on one page"])
    res = upload(client, task["id"], png(600, 400, (30, 90, 200)), png(600, 400, (30, 90, 200)), "mock.png")
    assert res.status_code == 201, res.text
    att = res.json()["id"]
    goal = f"The receipt should look like the mock below.\n\n![mock.png](attachment:{att})\n\nKeep the paper size A4."
    assert client.patch(f"/v1/tasks/{task['id']}", {"goal": goal}).status_code == 200
    page.set_viewport_size({"width": 1440, "height": 1000})
    sign_in(page, web_url, org["api_key"])
    page.goto(f"{web_url}#/task/{task['id']}")
    page.get_by_test_id("edit-task").click()

    # Preview: select it, then Wrap right and Small, each a toolbar button.
    page.get_by_test_id("task-goal-preview").evaluate("el => document.getElementById(el.getAttribute('aria-labelledby')).click()")
    preview = page.get_by_test_id("task-goal-preview")
    figure = preview.locator("[data-image-n='0']")
    expect(figure.locator("img")).to_have_attribute("src", re.compile(r"^blob:"), timeout=20_000)
    figure.click()
    toolbar = page.get_by_role("toolbar", name="Image layout")
    expect(toolbar).to_be_visible()
    toolbar.get_by_role("button", name="Wrap right").click()
    toolbar.get_by_role("button", name="Small").click()
    expect(page.get_by_test_id("task-goal")).to_have_value(goal.replace(f"{att})", f'{att} "small right")'))
    _shoot(page, "6-image-layout-selected")
    page.get_by_test_id("task-save").click()
    expect(page.get_by_test_id("task-goal")).to_have_count(0, timeout=20_000)
    assert client.get(f"/v1/tasks/{task['id']}").json()["goal"] == goal.replace(f"{att})", f'{att} "small right")')
    assert _attached(owner_dsn, task["id"]) == [att]

    # Reloaded, the task screen draws it floated right at 200 px.
    page.reload()
    shown = page.get_by_test_id("task-screen").get_by_test_id("markdown-figure")
    expect(shown.locator("img")).to_have_attribute("src", re.compile(r"^blob:"), timeout=20_000)
    style = shown.evaluate("el => { const s = getComputedStyle(el); return [s.float, el.getBoundingClientRect().width]; }")
    assert style[0] == "right" and abs(style[1] - 200) < 1, style
    # A phone-wide column (under 480 px) wraps nothing: the same image stands on its own line.
    page.set_viewport_size({"width": 390, "height": 1000})
    expect(shown).to_have_css("float", "none")
    page.set_viewport_size({"width": 1440, "height": 1000})
    expect(shown).to_have_css("float", "right")

    # Dragged under criterion 2: a continuation line there; the list keeps its length.
    page.get_by_test_id("edit-task").click()
    page.get_by_test_id("task-goal-preview").evaluate("el => document.getElementById(el.getAttribute('aria-labelledby')).click()")
    page.get_by_test_id("task-criteria-preview").evaluate("el => document.getElementById(el.getAttribute('aria-labelledby')).click()")
    source = page.get_by_test_id("task-goal-preview").locator("[data-image-n='0']")
    expect(source.locator("img")).to_have_attribute("src", re.compile(r"^blob:"), timeout=20_000)
    _image_drag(page, source, page.get_by_test_id("task-criteria-preview"), 1)
    ref = f'![mock.png](attachment:{att} "small right")'
    expect(page.get_by_test_id("task-criteria")).to_have_value(
        f"- [ ] Totals are right-aligned\n- [ ] The logo sits top left\n  {ref}\n- [ ] Prints on one page")
    expect(page.get_by_test_id("task-goal")).to_have_value("The receipt should look like the mock below.\n\nKeep the paper size A4.")
    expect(page.get_by_test_id("task-criteria-count")).to_have_text("3 criteria")
    # Floated right in criterion 2, it ends inside that criterion: the next one starts below it.
    # Plain `- ` items, whose bodies are ordinary blocks (a `- [ ]` item is a flex row, which holds it anyway).
    page.get_by_test_id("task-criteria").evaluate("""el => {
      const set = Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, 'value').set;
      set.call(el, el.value.replaceAll('- [ ] ', '- '));
      el.dispatchEvent(new Event('input', { bubbles: true }));
    }""")
    expect(page.get_by_test_id("task-criteria-preview").locator("li").nth(2)).to_be_visible()
    fit = page.get_by_test_id("task-criteria-preview").evaluate("""el => {
      const fig = el.querySelector('[data-testid=markdown-figure]');
      const items = [...el.querySelectorAll('li')];
      const f = fig.getBoundingClientRect();
      return { float: getComputedStyle(fig).float, inItem: items.indexOf(fig.closest('li')),
               figBottom: f.bottom, itemBottom: items[1].getBoundingClientRect().bottom, nextTop: items[2].getBoundingClientRect().top };
    }""")
    assert fit["float"] == "right" and fit["inItem"] == 1, fit
    assert fit["figBottom"] <= fit["itemBottom"] + 0.5 and fit["figBottom"] <= fit["nextTop"] + 0.5, fit
    page.get_by_test_id("task-save").click()
    expect(page.get_by_test_id("task-goal")).to_have_count(0, timeout=20_000)
    saved = client.get(f"/v1/tasks/{task['id']}").json()
    assert saved["acceptanceCriteria"] == ["Totals are right-aligned", f"The logo sits top left\n{ref}", "Prints on one page"], saved
    assert _attached(owner_dsn, task["id"]) == [att]

    # Delivered: the agent reads the image inside criterion 2, with no layout words.
    assert client.post(f"/v1/tasks/{task['id']}/deliver").status_code == 201
    lux_run = _submitted(client, owner_dsn, task["id"], "implement")
    images = wait_until(lambda: fake_lux_images(env, lux_run).get("prompt"), timeout=30, message="no prompt images")
    assert [a["name"] for a in images] == ["mock.png"]
    prompt = lux_spec(env, lux_run)["workload"]["prompt"]
    assert "- The logo sits top left\n  [Image 1: mock.png]\n- Prints on one page" in prompt, prompt
    assert "small" not in prompt and "attachment:" not in prompt, prompt
    wait_until(lambda: next((r for r in client.task_runs(task["id"]) if r["phase"] == "review" and r["status"] == "running"), None),
               timeout=90, message="no reviewer running")

    # While it runs: a click opens the viewer, and no toolbar appears.
    page.reload()
    page.get_by_test_id("edit-task").click()
    locked = page.get_by_test_id("task-criteria-preview").get_by_test_id("markdown-figure")
    expect(locked.locator("img")).to_have_attribute("src", re.compile(r"^blob:"), timeout=20_000)
    expect(locked).not_to_have_attribute("draggable", "true")
    locked.locator("img").click()
    expect(page.get_by_test_id("image-viewer")).to_be_visible()
    expect(page.get_by_role("toolbar", name="Image layout")).to_have_count(0)
    assert console_errors == []


@pytest.mark.ui
def test_an_image_dragged_in_preview_by_the_browser_raises_no_drop_overlay(
    page: Page, web_url: str, client: ApiClient, org: dict, forge_project: dict, console_errors: list, tmp_path: Path
):
    intro = "The receipt should look like the mock below."
    task = client.create_task(forge_project["id"], "Move the mock", goal=intro,
                              acceptanceCriteria=["Totals are right-aligned", "The logo sits top left", "Prints on one page"])
    res = upload(client, task["id"], png(300, 200, (30, 90, 200)), png(300, 200, (30, 90, 200)), "mock.png")
    assert res.status_code == 201, res.text
    att = res.json()["id"]
    goal = f"{intro}\n\n![mock.png](attachment:{att})\n\nKeep the paper size A4."
    assert client.patch(f"/v1/tasks/{task['id']}", {"goal": goal}).status_code == 200
    page.set_viewport_size({"width": 1440, "height": 1000})
    sign_in(page, web_url, org["api_key"])
    page.goto(f"{web_url}#/task/{task['id']}")
    page.get_by_test_id("edit-task").click()
    for field in ("task-goal-preview", "task-criteria-preview"):
        page.get_by_test_id(field).evaluate("el => document.getElementById(el.getAttribute('aria-labelledby')).click()")
    source = page.get_by_test_id("task-goal-preview").locator("[data-image-n='0']")
    expect(source.locator("img")).to_have_attribute("src", re.compile(r"^blob:"), timeout=20_000)
    # Every overlay that appears, and the drag's types as the page sees them.
    page.evaluate("""() => {
      window.__drag = { overlays: 0, types: [] };
      new MutationObserver(() => {
        if (document.querySelector('[data-testid=drop-overlay]')) window.__drag.overlays++;
      }).observe(document.body, { childList: true, subtree: true });
      window.addEventListener('dragenter', (e) => { window.__drag.types = [...e.dataTransfer.types]; }, true);
    }""")
    crit = page.get_by_test_id("task-criteria-preview")
    box, li = crit.bounding_box(), crit.locator("li").nth(1).bounding_box()
    assert box and li
    source.drag_to(crit, target_position={"x": 40, "y": li["y"] + li["height"] - box["y"]})
    expect(page.get_by_test_id("task-criteria")).to_have_value(
        f"- [ ] Totals are right-aligned\n- [ ] The logo sits top left\n  ![mock.png](attachment:{att})\n- [ ] Prints on one page")
    expect(page.get_by_test_id("task-goal")).to_have_value(f"{intro}\n\nKeep the paper size A4.")
    drag = page.evaluate("window.__drag")
    # Playwright's drag lists no "Files"; Chrome's own image drags do.
    assert "application/x-dude-image" in drag["types"] and "Files" not in drag["types"], drag
    assert drag["overlays"] == 0, drag
    expect(page.get_by_test_id("drop-overlay")).to_have_count(0)

    # Back to the goal as Chrome drags an image: the browser's own drag data, with the image offered as a file.
    moved = page.get_by_test_id("task-criteria-preview").locator("[data-image-n='0']")
    expect(moved.locator("img")).to_have_attribute("src", re.compile(r"^blob:"), timeout=20_000)
    _drag_with_files(page, moved, page.get_by_test_id("task-goal-preview").locator("p").nth(1), tmp_path / "mock.png")
    expect(page.get_by_test_id("task-goal")).to_have_value(goal)
    expect(page.get_by_test_id("drop-overlay")).to_have_count(0)
    drag = page.evaluate("window.__drag")
    assert "Files" in drag["types"] and "application/x-dude-image" in drag["types"], drag
    assert drag["overlays"] == 0, drag
    assert console_errors == []


def _drag_with_files(page: Page, source, target, file: Path) -> None:
    """A real Chromium drag from `source` to the top of `target`, its data captured from the browser
    (Input.setInterceptDrags) and replayed with `file` added, so `types` include "Files"."""
    file.write_bytes(png(40, 40))
    cdp = page.context.new_cdp_session(page)
    captured: list[dict] = []
    cdp.on("Input.dragIntercepted", lambda e: captured.append(e["data"]))
    cdp.send("Input.setInterceptDrags", {"enabled": True})
    s, t = source.bounding_box(), target.bounding_box()
    assert s and t
    sx, sy = s["x"] + s["width"] / 2, s["y"] + s["height"] / 2
    tx, ty = t["x"] + 20, t["y"] + 1
    mouse = lambda kind, x, y: cdp.send("Input.dispatchMouseEvent", {"type": kind, "x": x, "y": y, "button": "left", "buttons": 1 if kind != "mouseReleased" else 0, "clickCount": 1})
    mouse("mousePressed", sx, sy)
    for i in range(1, 6):
        mouse("mouseMoved", sx, sy + i * 4)
    wait_until(lambda: captured, timeout=5, message="no drag intercepted")
    data = {**captured[0], "files": [str(file)]}
    for kind, x, y in (("dragEnter", tx, ty), ("dragOver", tx, ty), ("dragOver", tx, ty + 1), ("drop", tx, ty + 1)):
        cdp.send("Input.dispatchDragEvent", {"type": kind, "x": x, "y": y, "data": data})
    mouse("mouseReleased", tx, ty + 1)
    cdp.send("Input.setInterceptDrags", {"enabled": False})
    cdp.detach()


def _open_new_task(page: Page, web_url: str, org: dict) -> None:
    """The New task dialog, on the board of the org's project (the `forge_project` fixture makes it)."""
    page.set_viewport_size({"width": 1440, "height": 900})
    sign_in(page, web_url, org["api_key"])
    page.get_by_test_id("new-task").click()
    expect(page.get_by_test_id("task-title")).to_be_visible()


@pytest.mark.ui
def test_an_image_dropped_on_the_title_goes_to_the_end_of_the_goal(page: Page, web_url: str, org: dict, forge_project: dict, console_errors: list):
    _open_new_task(page, web_url, org)
    page.get_by_test_id("task-goal").fill("The menu overlaps the header.")
    dt = _files(page, [("bug.png", png(300, 200, (200, 40, 40)), "image/png")])
    assert _drop_on(page.get_by_test_id("task-title"), dt, page=page)
    expect(page.get_by_test_id("task-goal")).to_have_value(re.compile(r"^The menu overlaps the header\.\n\n!\[bug\.png\]\(attachment:att_local\d+\)$"),
                                                           timeout=20_000)
    # A text drag is left to the field: no overlay, nothing claimed.
    assert not _drag(page.get_by_test_id("task-title"), "dragenter", _files(page, [], "words"))
    expect(page.get_by_test_id("drop-overlay")).to_have_count(0)
    assert console_errors == []


@pytest.mark.ui
def test_text_alone_pastes_as_text_and_a_files_own_name_is_not_text(page: Page, web_url: str, org: dict, forge_project: dict, console_errors: list):
    _open_new_task(page, web_url, org)
    title = page.get_by_test_id("task-title")
    title.fill("Summary")
    # Text alone: left to the field.
    assert not _paste_into(title, _files(page, [], " page"))
    expect(title).to_have_value("Summary")
    goal = page.get_by_test_id("task-goal")
    goal.fill("Before.")
    # A file copied in a file manager carries its own name as text: that is not text, and the paste is claimed.
    assert _paste_into(goal, _files(page, [("copied.png", png(200, 200), "image/png")], "copied.png"))
    expect(goal).to_have_value(re.compile(r"^Before\.\n\n!\[copied\.png\]\(attachment:att_local\d+\)$"), timeout=20_000)
    assert console_errors == []


@pytest.mark.ui
def test_attach_images_is_a_labelled_button_that_inserts_where_the_person_was_writing(
    page: Page, web_url: str, org: dict, forge_project: dict, console_errors: list
):
    _open_new_task(page, web_url, org)
    attach = page.get_by_role("button", name="Attach images")
    expect(attach).to_be_visible()
    expect(attach).to_have_attribute("data-testid", "task-attach")
    _shoot(page, "5-task-inline-empty")
    attach.hover()
    expect(page.get_by_role("tooltip")).to_contain_text("up to 10 MB each")
    # Last written in: the criteria. The button inserts there.
    page.get_by_test_id("task-criteria").fill("- [ ] Matches ")
    page.get_by_test_id("task-criteria").press("End")
    # Pressed from the keyboard, it opens the file picker.
    attach.focus()
    expect(attach).to_be_focused()
    with page.expect_file_chooser() as chooser:
        page.keyboard.press("Enter")
    chooser.value.set_files([{"name": "picked.png", "mimeType": "image/png", "buffer": png(200, 120)}])
    expect(page.get_by_test_id("task-criteria")).to_have_value(re.compile(r"^- \[ \] Matches !\[picked\.png\]\(attachment:att_local\d+\)$"), timeout=20_000)
    expect(page.get_by_test_id("task-goal")).to_have_value("")
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
    expect(goal).to_have_value("")
    # Outside the dialog, on its backdrop, too.
    body = page.locator("body")
    assert _drag(body, "dragover", dt)
    assert _drag(body, "drop", dt)
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
    # A caption pasted with an image goes into the field; the image to the tray.
    assert not _paste_into(field, _files(page, [("both.png", png(200, 200), "image/png")], "the total is cut off"))
    expect(chips).to_have_count(4)
    _paste(page, [("pasted.png", png(90, 90), "image/png")])
    expect(chips).to_have_count(5)
    for i in range(5):
        expect(chips.nth(i)).to_have_attribute("data-state", "ready", timeout=20_000)
    assert console_errors == []
