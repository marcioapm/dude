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
import json
import subprocess
from pathlib import Path

import pytest
import requests
from playwright.sync_api import BrowserType, Page, expect

from env import FAKE_PUBLIC_URL
from helpers import ApiClient, browser_channel, provision_person, sign_in, wait_until

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
  window.__messages = [];
  window.addEventListener('message', (e) => window.__heard.push(e.origin));
  window.addEventListener('message', (e) => window.__messages.push(e.data));
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
    """The Servers tab's server URL clicked, and the preview window it opens with the comment tool, connected to dude."""
    comment = page.get_by_test_id("server-url").first
    expect(comment).to_be_visible(timeout=20_000)
    with ctx.expect_page() as opened:
        comment.click()
    pv = opened.value
    pv.wait_for_function("() => window.__ovRoot?.querySelector('[data-connection=connected]')", timeout=15_000)
    return pv


def _back_to_the_preview(client: ApiClient, env, task: dict, item_id: str) -> dict:
    """The servers read showing the preview again, its web server ready: the web server as it says.

    Submitting the draft starts a delivery, and while its agents work the
    servers read is their Run, where dude's links open no comment tool. Once they are
    done the read is the preview's again; one that went to sleep meanwhile is
    woken as a visitor would.
    """
    # Between two of the delivery's Runs the read is briefly the preview's again, so wait for where it ends: the
    # scripted fixer never answers the item, and after its one fix (maxAttemptsPerFinding 1) the review loop escalates
    # it as stuck (workflow.go escalate), the only state here that starts no further Run until a person decides. The
    # API shows an escalation only while nothing has decided it.
    def stuck_on_the_item() -> bool:
        e = client.get(f"/v1/tasks/{task['id']}").json().get("escalation") or {}
        return e.get("reason") == "stuck" and (e.get("detail") or {}).get("feedbackIds") == [item_id]

    wait_until(stuck_on_the_item, timeout=180, message="the delivery never stopped for a person on the unanswered item within 180 s")
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
    # One fix per finding: the unanswered item is escalated after its first fix.
    assert client.patch(f"/v1/projects/{forge_project['id']}/settings",
                        {"delivery": {"maxAttemptsPerFinding": 1}}).status_code == 200
    task, web = _ready_preview(client, env, forge_project, "Comment on the checkout")
    assert web["commenting"] == {"available": True} and web["needsRestart"] is False, web
    browser = browser_type.launch(**browser_channel(), headless=True, args=LAUNCH_ARGS)
    try:
        ctx, errors = _context(browser, web, web_url, env)
        page = ctx.new_page()
        sign_in(page, web_url, org["api_key"], at=f"#/task/{task['id']}/servers")
        # A plain click opens the commenting window with its opener; a modified click is the browser's, and that tab drops the opener.
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
        web = _back_to_the_preview(client, env, task, listed[0]["id"])
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


def test_taskless_preview_connect_overlay_screenshot_then_cli_management(
    browser_type: BrowserType, web_url: str, client: ApiClient, org: dict, forge_project: dict, fake_github, env, tmp_path, access
):
    """Real overlay UI/postMessage -> authenticated relay -> API/storage -> actual CLI subprocess."""
    pid = forge_project["id"]
    assert client.put(f"/v1/projects/{pid}/servers/web", WEB).status_code == 200
    fake_github.push_branch("feature/server-feedback")
    made = client.post(f"/v1/projects/{pid}/branch-previews", {"branch": "feature/server-feedback"})
    assert made.status_code == 201, made.text
    rid = made.json()["id"]
    asleep = wait_until(lambda: (p := client.get(f"/v1/branch-previews/{rid}").json())["servers"] and p,
                        timeout=60, message="branch servers allocated")
    assert client.post(f"/v1/runs/{rid}/servers/web/start").status_code == 200
    ready = wait_until(lambda: (p := client.get(f"/v1/runs/{rid}/servers").json())["servers"]
                       and p["servers"][0]["state"] == "ready" and p, timeout=60, message="branch preview ready")
    web = ready["servers"][0]
    png = bytes.fromhex("89504e470d0a1a0a0000000d494844520000000a0000000a000000000000000000")
    uploaded = requests.post(f"{client.base_url}/v1/branch-previews/{rid}/servers/web/attachments",
                              headers={"authorization": f"Bearer {client.api_key}"}, timeout=30,
                              files={part: ("example.png", png, "image/png") for part in ["original", "delivered"]},
                              data={"originalType": "image/png", "deliveredType": "image/png"})
    assert uploaded.status_code == 201, uploaded.text
    private_attachment = uploaded.json()["id"]
    seeded = client.post(f"/v1/branch-previews/{rid}/servers/web/feedback", {
        "submissionKey": "private-seeded", "body": "Colleague private saved feedback",
        "anchor": {"kind": "preview", "url": web["url"].rstrip("/") + "/private", "selector": "#private-anchor",
                   "attachmentId": private_attachment},
    })
    assert seeded.status_code == 201, seeded.text
    private = seeded.json()
    email = f"preview-{rid}@example.com"
    provision_person(client, env, "Ana Example", email)
    binary = Path(__file__).resolve().parents[2] / "orchestrator" / "bin" / "dude-cli"
    cli_env = {k: v for k, v in os.environ.items() if not k.startswith("DUDE_")}
    cli_env.update(HOME=str(tmp_path), XDG_CONFIG_HOME=str(tmp_path), DUDE_URL=env.control_plane_url, DUDE_API_KEY=client.api_key)

    def cli(*args):
        result = subprocess.run([str(binary), "preview", *args], cwd=tmp_path, env=cli_env,
                                capture_output=True, text=True, input="", timeout=30)
        assert result.returncode == 0, (args, result.stdout, result.stderr)
        return json.loads(result.stdout)

    browser = browser_type.launch(**browser_channel(), headless=True, args=LAUNCH_ARGS)
    try:
        ctx, errors = _context(browser, web, access.url, env)
        feedback_reads = []
        submissions = []
        screenshot_uploads = []
        ctx.on("request", lambda r: feedback_reads.append(r.url) if r.method == "GET" and
               ("/preview-feedback/" in r.url or re.search(r"/branch-previews/[^/]+/(?:servers/[^/]+/)?feedback", r.url)) else None)
        ctx.on("request", lambda r: screenshot_uploads.append(r.url) if r.method == "POST" and
               f"/branch-previews/{rid}/servers/web/attachments" in r.url else None)

        def lose_committed_response(route):
            if route.request.method != "POST":
                route.continue_()
                return
            submissions.append(route.request.post_data_json)
            response = route.fetch()
            assert response.status == 201
            if len(submissions) == 1:
                # Commit happened, but the browser cannot decode the response body.
                route.fulfill(status=201, content_type="application/json", body="{")
            else:
                route.fulfill(response=response)

        ctx.route(f"**/v1/branch-previews/{rid}/servers/web/feedback", lose_committed_response)
        login = ctx.new_page()
        access.sign_in(login, email)
        preview = ctx.new_page(); preview.goto(web["url"])
        _wait_ov(preview, "ov-connect")
        with ctx.expect_page() as opened:
            _ov(preview, "r => r.querySelector('[data-testid=ov-connect]').click()")
        relay = opened.value
        expect(relay.get_by_test_id("preview-connect")).to_have_attribute("data-step", "connected", timeout=20_000)
        preview.wait_for_function("() => window.__ovRoot?.querySelector('[data-connection=connected]')")
        assert "preview-connect" in relay.url and "/task/" not in relay.url
        # A valid account/token without organization membership is not preview authorization.
        outsider = access.session(f"outsider-{rid}@example.com")
        denied = outsider.get(f"/v1/preview-servers/lookup?origin={web['url'].rstrip('/')}")
        assert denied.status_code == 403, denied.text
        assert _ov(preview, "r => r.querySelectorAll('[data-testid=ov-pin], [data-testid=ov-pins]').length") == 0
        for n in range(3):
            _ov(preview, "r => r.querySelector('[data-testid=ov-comment]').click()")
            preview.hover("button.pay")
            box = preview.locator("button.pay").bounding_box()
            preview.mouse.click(box["x"] + box["width"] / 2, box["y"] + box["height"] / 2)
            _wait_ov(preview, "ov-comment-box")
            preview.wait_for_function("() => window.__ovRoot.querySelector('img.thumb')", timeout=15_000)
            preview.keyboard.type(f"Server-only feedback {n}")
            assert _ov(preview, "r => r.querySelector('[data-testid=ov-save]').textContent") == "Submit feedback"
            preview.keyboard.press("Control+Enter")
            if n == 0:
                preview.wait_for_function("() => { const b = window.__ovRoot.querySelector('[data-testid=ov-save]'); return b && !b.disabled; }")
                assert len(cli("list-feedback", rid)["feedback"]) == 2
                preview.keyboard.press("Control+Enter")
            _wait_ov(preview, "ov-saved")
            assert "Feedback submitted" in _ov(preview, "r => r.querySelector('[data-testid=ov-saved]').textContent")
        assert len(submissions) == 4 and len(screenshot_uploads) == 3
        assert submissions[0] == submissions[1], submissions[:2]
        first = cli("list-feedback", rid, "--limit", "2")
        assert len(first["feedback"]) == 2 and first["nextCursor"]
        second = cli("list-feedback", rid, "--after", first["nextCursor"], "--limit", "2")
        assert len(second["feedback"]) == 2 and second["nextCursor"] is None
        item = cli("show-feedback", next(f["id"] for f in first["feedback"] + second["feedback"] if f["id"] != private["id"]))
        assert item["previewRunId"] == rid and item["serverName"] == "web" and item["captureSha"] is None
        aid = item["anchor"]["attachmentId"]
        picture = client.get(f"/v1/attachments/{aid}")
        assert picture.status_code == 200 and picture.content[:4] == b"\x89PNG"
        assert picture.headers["cache-control"] == "private, no-store"
        cli("resolve-feedback", item["id"], "--note", "Checked on preview")
        assert len(cli("list-feedback", rid)["feedback"]) == 3
        assert len(cli("list-feedback", rid, "--status", "resolved", "--server", "web")["feedback"]) == 1
        assert cli("list-feedback", rid, "--server", "api")["feedback"] == []
        cli("reopen-feedback", item["id"], "--note", "Check again")
        assert len(cli("list-feedback", rid)["feedback"]) == 4
        assert len(cli("show-feedback", item["id"])["history"]) == 2
        # A reload/reconnect does not load anyone's saved feedback into the overlay.
        relay.reload()
        expect(relay.get_by_test_id("preview-connect")).to_have_attribute("data-step", "connected", timeout=20_000)
        assert _ov(preview, "r => r.querySelectorAll('[data-testid=ov-pin], [data-testid=ov-pins]').length") == 0
        expect(relay.get_by_test_id("preview-connect")).not_to_contain_text("Server-only feedback")
        preview.evaluate("() => history.pushState({}, '', '/next')")
        preview.wait_for_function("() => (window.__messages ?? []).filter(m => m?.type === 'session').length >= 2")
        messages_before_reload = preview.evaluate("() => window.__messages")
        preview.reload()
        _wait_ov(preview, "ov-connect")
        _ov(preview, "r => r.querySelector('[data-testid=ov-connect]').click()")
        preview.wait_for_function("() => window.__ovRoot?.querySelector('[data-connection=connected]')", timeout=15_000)
        sessions = preview.evaluate("() => window.__messages.filter(m => m?.type === 'session').length")
        preview.wait_for_function("n => window.__messages.filter(m => m?.type === 'session').length > n", arg=sessions, timeout=10_000)
        messages = json.dumps(messages_before_reload + preview.evaluate("() => window.__messages"))
        for secret in [private["id"], private["body"], "#private-anchor", "/private", private_attachment]:
            assert secret not in messages, (secret, messages)
        assert feedback_reads == [], feedback_reads
        _evil_posted(relay)
        assert len(cli("list-feedback", rid)["feedback"]) == 4
        cli("stop", rid)
        assert cli("list-feedback", rid, "--status", "all")["feedback"] == []
        assert client.get(f"/v1/attachments/{aid}").status_code == 404
        assert errors == [], errors
        ctx.close()
    finally:
        browser.close()
