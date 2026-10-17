"""Commenting on a branch preview, in the browser, against the real API, orchestrator and fake lux.

The preview's servers live under the fake lux's preview domain, which no
resolver answers: the browser context answers those https URLs itself with a
small app page carrying the tag lux injects (the orchestrator's own, read back
from the fake lux, with dude's origin rewritten to where this run's web app is
served). That page is a second origin to dude's tab, and everything between
them is the browser's own window.open and postMessage.

It drives: Comment on the Servers tab opens the preview in a window dude keeps;
an element is picked and a comment saved there; the draft is in dude's tray
with its anchor and its picture (a task attachment the API serves); submitted
from the tray; a colleague's Comment shows it pinned on the same page; a page
opened directly offers no Save; a page on another origin posting to dude is not
heard.

Screenshots go to DUDE_PREVIEW_SHOTS when it names a directory.
"""

from __future__ import annotations

import os
import re
from pathlib import Path

import pytest
import requests
from playwright.sync_api import BrowserType, Page, expect

from env import FAKE_PUBLIC_URL
from helpers import ApiClient, provision_person, sign_in, wait_until

pytestmark = pytest.mark.ui

WEB = {"name": "web", "port": 3000, "command": "npm run dev -- --port 3000", "workdir": "", "setup": None, "env": [], "autostartInPreviews": True}
SHOTS = os.environ.get("DUDE_PREVIEW_SHOTS")
# dude is served on loopback here and the preview has a public name: Chrome's Local
# Network Access would refuse the overlay's load, which a deployed dude never meets.
LAUNCH_ARGS = ["--disable-features=LocalNetworkAccessChecks,BlockInsecurePrivateNetworkRequests"]

APP = """<!doctype html><html><head><meta charset="utf-8"><title>Shop</title>
<style>* { color: rgb(200, 0, 0) !important; } button { background: #1f7a5a; padding: 12px 22px; border: 0; border-radius: 8px; }</style></head>
<body><main><h1>Checkout</h1><form id="checkout"><button type="button" class="pay">Pay now</button></form></main>
%TAG%</body></html>"""

EVIL = """<!doctype html><script>
  window.opener && window.opener.postMessage({ type: "draft", v: 1, nonce: "x", body: "From another site",
    anchor: { kind: "preview", url: location.origin + "/", selector: "h1" } }, "*");
</script>"""

# The overlay's root is closed; the suite keeps what attachShadow returns to read and point at it.
HOLD_ROOT = """(() => {
  const attach = Element.prototype.attachShadow;
  Element.prototype.attachShadow = function (init) {
    const r = attach.call(this, init);
    if (this.localName === 'dude-preview-overlay') window.__ovRoot = r;
    return r;
  };
})()"""

# In dude's tab: the origin of every message it is sent, heard before the app's own listener.
HEARD = """(() => {
  window.__heard = [];
  window.addEventListener('message', (e) => window.__heard.push(e.origin));
})()"""


def _lux(env, method: str, path: str) -> dict:
    res = requests.request(method, env.lux_url + path, headers={"Authorization": f"Bearer {env.lux_key}"}, timeout=10)
    res.raise_for_status()
    return res.json()


def _ready_preview(client: ApiClient, env, project: dict, title: str) -> tuple[dict, dict]:
    """A task with its branch preview awake and web ready: the task and web as the servers read says it."""
    assert client.put(f"/v1/projects/{project['id']}/servers/web", WEB).status_code == 200
    task = client.create_task(project["id"], title)
    assert client.post(f"/v1/tasks/{task['id']}/preview").status_code == 201
    asleep = wait_until(lambda: (s := client.get(f"/v1/tasks/{task['id']}/servers").json())["run"].get("asleep") and s,
                        timeout=60, message="the preview never went to sleep")
    _lux(env, "POST", f"/fake/servers/{asleep['servers'][0]['id']}/request?path=/")
    ready = wait_until(lambda: (s := client.get(f"/v1/tasks/{task['id']}/servers").json())["run"]["previewStage"] == "ready" and s,
                       timeout=60, message="the preview never woke")
    [web] = ready["servers"]
    return task, web


def _context(browser, web: dict, web_url: str, env):
    """A browser context whose preview origin answers with the app page and the tag lux was given."""
    inject = _lux(env, "GET", f"/v1/servers/{web['id']}")["inject"]
    assert inject == f'<script src="{FAKE_PUBLIC_URL}/feedback/overlay.js" data-dude-origin="{FAKE_PUBLIC_URL}" async></script>', inject
    # The orchestrator's tag, at the origin this run's web app is really served from.
    tag = inject.replace(FAKE_PUBLIC_URL, web_url)
    ctx = browser.new_context(viewport={"width": 1440, "height": 900}, service_workers="block")
    ctx.add_init_script(HOLD_ROOT)
    ctx.add_init_script(HEARD)
    origin = re.sub(r"/$", "", web["url"])
    ctx.route(f"{origin}/**", lambda r: r.fulfill(status=200, content_type="text/html", body=APP.replace("%TAG%", tag)))
    ctx.route("https://evil.preview.dude.test/**", lambda r: r.fulfill(status=200, content_type="text/html", body=EVIL))
    errors: list[str] = []

    def watch(p):
        p.on("console", lambda m: m.type == "error" and errors.append(f"{p.url}: {m.text}"))
        p.on("pageerror", lambda e: errors.append(f"{p.url}: {e}"))
    ctx.on("page", watch)
    return ctx, errors


def _ov(page: Page, js: str, arg=None):
    return page.evaluate(f"([a]) => {{ const root = window.__ovRoot; return ({js})(root, a); }}", [arg])


def _wait_ov(page: Page, testid: str) -> None:
    page.wait_for_function("id => window.__ovRoot && window.__ovRoot.querySelector(`[data-testid='${id}']`)", arg=testid, timeout=15_000)


def _shoot(page: Page, name: str) -> None:
    if SHOTS:
        Path(SHOTS).mkdir(parents=True, exist_ok=True)
        page.screenshot(path=str(Path(SHOTS) / f"e2e-{name}.png"))


def _open_comment(ctx, page: Page) -> Page:
    """Comment on the Servers tab, and the preview window it opens, connected to dude."""
    comment = page.get_by_test_id("comment-on-server").first
    expect(comment).to_be_visible(timeout=20_000)
    with ctx.expect_page() as opened:
        comment.click()
    pv = opened.value
    pv.wait_for_function("() => window.__ovRoot?.querySelector('[data-connection=connected]')", timeout=15_000)
    return pv


def _back_to_the_preview(client: ApiClient, env, task: dict) -> dict:
    """The servers read showing the preview again, its web server ready: the web server as it says.

    Submitting the draft starts a delivery, and while its agents work the
    servers read is their Run, where dude offers no Comment. Once they are
    done the read is the preview's again; one that went to sleep meanwhile is
    woken as a visitor would.
    """
    # Between two of the delivery's Runs the read is briefly the preview's again: wait for the delivery to rest first.
    wait_until(lambda: any(r["phase"] == "simplify" and r["status"] == "completed" for r in client.task_runs(task["id"]))
               and not any(r["status"] in ("scheduled", "starting", "running") for r in client.task_runs(task["id"]) if r.get("kind") != "preview"),
               timeout=180, message="the delivery's agents were still at work after 180 s")
    read = wait_until(lambda: (s := client.get(f"/v1/tasks/{task['id']}/servers").json())["run"] and s["run"]["kind"] == "preview" and s,
                      timeout=60, message="the servers read never came back to the preview after the delivery rested")
    if read["run"].get("asleep"):
        _lux(env, "POST", f"/fake/servers/{read['servers'][0]['id']}/request?path=/")
    read = wait_until(lambda: (s := client.get(f"/v1/tasks/{task['id']}/servers").json())["run"] and s["run"]["kind"] == "preview"
                      and s["run"]["previewStage"] == "ready" and s["servers"][0]["state"] == "ready" and s,
                      timeout=60, message="the preview never woke after the delivery")
    [web] = read["servers"]
    return web


def _evil_posted(page: Page) -> None:
    """Opens the page on another origin that posts a draft to dude, and waits until dude's tab has heard it.

    HEARD (an init script in dude's tab) records each message's origin in a
    listener added before the app's, so once it lists the evil origin the
    app's own listener has run on that message too; its origin check refuses
    synchronously.
    """
    page.evaluate("() => window.open('https://evil.preview.dude.test/', 'evil')")
    page.wait_for_function("() => (window.__heard ?? []).includes('https://evil.preview.dude.test')", timeout=15_000)


def test_a_comment_picked_on_the_preview_is_a_draft_in_dude_then_a_pin_for_a_colleague(
    browser_type: BrowserType, web_url: str, client: ApiClient, org: dict, forge_project: dict, env
):
    task, web = _ready_preview(client, env, forge_project, "Comment on the checkout")
    assert web["commenting"] == {"available": True} and web["needsRestart"] is False, web
    browser = browser_type.launch(channel="chrome", headless=True, args=LAUNCH_ARGS)
    try:
        ctx, errors = _context(browser, web, web_url, env)
        page = ctx.new_page()
        sign_in(page, web_url, org["api_key"], at=f"#/task/{task['id']}/servers")
        # The existing Preview link still drops the opener.
        expect(page.get_by_test_id("server-preview").first).to_have_attribute("rel", "noopener noreferrer")
        pv = _open_comment(ctx, page)
        expect(page.get_by_test_id("commenting-notice")).to_have_attribute("data-state", "open")

        # Pick the button, write, save.
        _ov(pv, "r => r.querySelector('[data-testid=ov-comment]').click()")
        _wait_ov(pv, "ov-hint")
        pv.hover("button.pay")
        _wait_ov(pv, "ov-label")
        assert "button.pay" in _ov(pv, "r => r.querySelector('[data-testid=ov-label]').textContent")
        box = pv.locator("button.pay").bounding_box()
        pv.mouse.click(box["x"] + box["width"] / 2, box["y"] + box["height"] / 2)
        _wait_ov(pv, "ov-comment-box")
        pv.wait_for_function("() => window.__ovRoot.querySelector('img.thumb') || window.__ovRoot.querySelector('[data-testid=ov-picture-failed]')", timeout=15_000)
        pv.keyboard.type("Pay now should say the total.")
        _shoot(pv, "overlay-box")
        pv.keyboard.press("Control+Enter")
        _wait_ov(pv, "ov-saved")
        _shoot(pv, "overlay-saved")

        # In dude: a private draft on the page's element, with its picture.
        page.bring_to_front()
        tray = page.get_by_test_id("fb-tray")
        expect(tray).to_have_attribute("data-count", "1")
        listed = client.get(f"/v1/tasks/{task['id']}/feedback").json()["feedback"]
        assert len(listed) == 1 and listed[0]["status"] == "draft", listed
        anchor = listed[0]["anchor"]
        assert anchor["kind"] == "preview" and anchor["url"].startswith(web["url"].rstrip("/")) and anchor["selector"] == "button.pay", anchor
        assert anchor["viewport"] == {"width": 1440, "height": 900} and anchor["text"] == "Pay now", anchor
        assert anchor.get("attachmentId"), "the picture was not attached"
        picture = client.get(f"/v1/attachments/{anchor['attachmentId']}")
        assert picture.status_code == 200 and picture.content[:4] == b"\x89PNG", picture.status_code
        tray.get_by_role("button", name="Review").click()
        expect(tray.get_by_test_id("fb-tray-row")).to_contain_text("Preview")
        _shoot(page, "dude-tray")

        # Submitted as any draft is.
        tray.get_by_test_id("fb-submit").click()
        page.get_by_role("dialog").get_by_test_id("fb-confirm").click()
        wait_until(lambda: client.get(f"/v1/tasks/{task['id']}/feedback").json()["feedback"][0]["status"] != "draft",
                   timeout=15, message="the draft was never submitted")

        # A page opened directly offers no way to save.
        direct = ctx.new_page()
        direct.goto(web["url"])
        _wait_ov(direct, "ov-no-opener")
        assert _ov(direct, "r => r.querySelectorAll('[data-testid=ov-save], [data-testid=ov-comment]').length") == 0
        _shoot(direct, "overlay-no-opener")

        # A page on another origin posting a draft to dude is not heard.
        _evil_posted(page)
        assert len(client.get(f"/v1/tasks/{task['id']}/feedback").json()["feedback"]) == 1
        assert errors == [], errors
        ctx.close()

        # A colleague's Comment shows the submitted item pinned on the button, once the delivery's agents leave the preview to it.
        web = _back_to_the_preview(client, env, task)
        _, colleague = provision_person(client, env, "Ana Ribeiro", f"ana-{task['id']}@example.com")
        ctx2, errors2 = _context(browser, web, web_url, env)
        page2 = ctx2.new_page()
        sign_in(page2, web_url, colleague.api_key, at=f"#/task/{task['id']}/servers")
        pv2 = _open_comment(ctx2, page2)
        _wait_ov(pv2, "ov-pin")
        assert _ov(pv2, "r => [...r.querySelectorAll('[data-testid=ov-pin]')].map(p => p.dataset.id)") == [listed[0]["id"]]
        _shoot(pv2, "overlay-colleague-pin")
        assert errors2 == [], errors2
        ctx2.close()
    finally:
        browser.close()
