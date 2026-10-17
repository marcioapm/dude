"""Commenting on a preview, in a real browser, against the Vite dev server and the fixture client.

dude's tab is the web app at http://127.0.0.1:PORT (fixtures, `?run=comment`: the
task's branch preview is up and its web server carries the comment tool). The
preview is another origin, https://web-shop.preview.test, answered by Playwright
with preview_page.html plus the tag lux would inject, which loads the overlay from
dude's origin. Everything between them is the browser's own postMessage.

It drives: Comment on the Servers tab -> pick an element -> save -> the draft in
dude's tray and on the Feedback tab with its picture -> Show on preview; a page
opened directly shows the no-opener panel; a message from another origin is
ignored; Restart and the unavailable reason. Screenshots go to
DUDE_PREVIEW_SHOTS when set (1440x900 dark and light, 390x844).

Run with: python3 apps/web/test/browser_preview_feedback.py
Requires installed Playwright and Chrome (system Chrome, or Playwright's cached Chromium).
"""
import os
import re
import unittest
from pathlib import Path

from playwright.sync_api import sync_playwright, expect

from vite_server import start_vite, stop_vite

PORT = 5197
DUDE = f"http://127.0.0.1:{PORT}"
PREVIEW = "https://web-shop.preview.test"
TASK = "tsk_01j9x4kqf8b2m7e3"
HERE = Path(__file__).resolve().parent
PAGE = (HERE / "preview_page.html").read_text()
TAG = f'<script src="{DUDE}/feedback/overlay.js" data-dude-origin="{DUDE}" async></script>'
SHOTS = os.environ.get("DUDE_PREVIEW_SHOTS")
# Here dude is on loopback and the preview a public name: Chrome's Local Network
# Access would refuse the overlay's load, which a deployed dude (public https) never meets.
LAUNCH_ARGS = ["--disable-features=LocalNetworkAccessChecks,BlockInsecurePrivateNetworkRequests"]

# The overlay's root is closed: nothing in the page can reach it. The test keeps
# the root attachShadow returns, as the unit tests do, to read and point at it.
HOLD_ROOT = """(() => {
  const attach = Element.prototype.attachShadow;
  Element.prototype.attachShadow = function (init) {
    const r = attach.call(this, init);
    if (this.localName === 'dude-preview-overlay') window.__ovRoot = r;
    return r;
  };
})()"""

EVIL = """<!doctype html><title>evil</title><script>
  const draft = { type: "draft", v: 1, nonce: "x1", body: "From another site",
    anchor: { kind: "preview", url: "https://web-shop.preview.test/", selector: "h1" } };
  window.opener && window.opener.postMessage(draft, "*");
  document.title = "posted";
</script>"""


def ov(page, js, arg=None):
    """Evaluate `js` (a function of the overlay's root and arg) in the preview page."""
    return page.evaluate(f"([a]) => {{ const root = window.__ovRoot; return ({js})(root, a); }}", [arg])


def ov_count(page, testid):
    return ov(page, "(r, id) => r ? r.querySelectorAll(`[data-testid='${id}']`).length : 0", testid)


def ov_text(page, testid):
    return ov(page, "(r, id) => r?.querySelector(`[data-testid='${id}']`)?.textContent ?? null", testid)


def ov_click(page, testid):
    """A real pointer click at the middle of an overlay control."""
    box = ov(page, """(r, id) => { const b = r.querySelector(`[data-testid='${id}']`).getBoundingClientRect();
      return { x: b.left + b.width / 2, y: b.top + b.height / 2 }; }""", testid)
    page.mouse.click(box["x"], box["y"])


def wait_ov(page, testid, timeout=10_000):
    page.wait_for_function("id => window.__ovRoot && window.__ovRoot.querySelector(`[data-testid='${id}']`)", arg=testid, timeout=timeout)


def shoot(page, name, themes=True):
    if not SHOTS:
        return
    out = Path(SHOTS)
    out.mkdir(parents=True, exist_ok=True)
    size = page.viewport_size
    if themes:
        for theme in ("dark", "light"):
            page.evaluate("t => document.documentElement.setAttribute('data-theme', t)", theme)
            page.wait_for_timeout(150)
            page.screenshot(path=str(out / f"{name}-1440-{theme}.png"))
        page.evaluate("() => document.documentElement.setAttribute('data-theme', 'dark')")
    else:
        page.screenshot(path=str(out / f"{name}-1440.png"))
    page.set_viewport_size({"width": 390, "height": 844})
    page.wait_for_timeout(250)
    page.screenshot(path=str(out / f"{name}-390{'-dark' if themes else ''}.png"))
    page.set_viewport_size(size)
    if themes:
        page.evaluate("() => document.documentElement.removeAttribute('data-theme')")
    page.wait_for_timeout(150)


def stop_server(server):
    # On macOS killpg of a group whose processes all exited after SIGTERM can raise EPERM.
    try:
        stop_vite(server)
    except PermissionError:
        server.wait(timeout=10)


class CommentingOnAPreview(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.server = start_vite(PORT)
        cls.addClassCleanup(stop_server, cls.server)
        cls.playwright = sync_playwright().start()
        cls.addClassCleanup(cls.playwright.stop)
        try:
            cls.browser = cls.playwright.chromium.launch(channel="chrome", headless=True, args=LAUNCH_ARGS)
        except Exception:  # noqa: BLE001 - no system Chrome: the cached Chromium
            cls.browser = cls.playwright.chromium.launch(headless=True, args=LAUNCH_ARGS)
        cls.addClassCleanup(cls.browser.close)

    def context(self, run="comment"):
        ctx = self.browser.new_context(viewport={"width": 1440, "height": 900}, service_workers="block")
        self.addCleanup(ctx.close)
        self.errors = []
        self.overlay_requests = []
        ctx.add_init_script(f"if (location.origin === '{DUDE}') localStorage.setItem('dude.fixtures.previewUrl', '{PREVIEW}')")
        ctx.add_init_script(HOLD_ROOT)

        def preview(route):
            route.fulfill(status=200, content_type="text/html", body=PAGE.replace("<!--OVERLAY-->", TAG))
        ctx.route(f"{PREVIEW}/**", preview)
        ctx.route("https://status.widgets.test/**", lambda r: r.fulfill(status=200, content_type="text/html", body="<p>All systems normal</p>"))
        ctx.route("https://evil.preview.test/**", lambda r: r.fulfill(status=200, content_type="text/html", body=EVIL))
        ctx.on("request", self.record_request)
        ctx.on("page", self.watch)
        page = ctx.new_page()
        page.goto(f"{DUDE}/?fixtures=a&run={run}#/task/{TASK}/servers")
        return ctx, page

    def record_request(self, request):
        # What a preview page asks of dude's origin: the overlay script, and nothing else, ever.
        if request.url.startswith(DUDE) and (request.headers.get("referer") or "").startswith(PREVIEW):
            self.overlay_requests.append(request.url)

    def watch(self, p):
        p.on("console", lambda m: m.type == "error" and self.errors.append(f"{p.url}: {m.text}"))
        p.on("pageerror", lambda e: self.errors.append(f"{p.url}: {e}"))

    def test_comment_pick_save_then_show_on_preview(self):
        ctx, page = self.context()
        self.watch(page)
        comment = page.get_by_test_id("comment-on-server").first
        expect(comment).to_be_visible(timeout=20_000)
        expect(page.get_by_test_id("server-preview").first).to_have_attribute("rel", "noopener noreferrer")
        shoot(page, "dude-servers-comment")

        with ctx.expect_page() as opened:
            comment.click()
        pv = opened.value
        pv.wait_for_load_state("domcontentloaded")
        pv.wait_for_function("() => window.__ovRoot?.querySelector('[data-connection=connected]')", timeout=10_000)
        expect(page.get_by_test_id("commenting-notice")).to_have_attribute("data-state", "open")
        # Ana's submitted comment is pinned on the heading, the one element its selector finds.
        wait_ov(pv, "ov-pin")
        self.assertEqual(ov(pv, "r => [...r.querySelectorAll('[data-testid=ov-pin]')].map(p => p.dataset.id)"), ["fbk_ana"])
        shoot(pv, "overlay-connected", themes=False)

        # The page's own CSS never reaches the overlay: its buttons stay the overlay's colours.
        bg = ov(pv, "r => getComputedStyle(r.querySelector('[data-testid=ov-comment]')).backgroundColor")
        self.assertEqual(bg, "rgb(124, 92, 255)")

        ov_click(pv, "ov-comment")
        wait_ov(pv, "ov-hint")
        pv.hover("button.greet")
        wait_ov(pv, "ov-label")
        self.assertIn("<GreetButton>", ov_text(pv, "ov-label"))
        shoot(pv, "overlay-hover", themes=False)
        # The narrow shot moved the pointer's element: back over the button.
        pv.hover("header.app-header")
        pv.hover("button.greet")
        pv.wait_for_function("() => /GreetButton/.test(window.__ovRoot.querySelector('[data-testid=ov-label]').textContent)")
        pv.keyboard.press("ArrowUp")
        pv.wait_for_function("() => /GreetForm/.test(window.__ovRoot.querySelector('[data-testid=ov-label]').textContent)")
        self.assertRegex(ov_text(pv, "ov-label"), r"<GreetForm> div")
        # A cross-origin frame is seen, not read (the overlay's catcher lies over it while picking).
        pv.mouse.move(*self.center(pv, "aside.status iframe"))
        pv.wait_for_function("() => /Can't pick inside this frame/.test(window.__ovRoot.querySelector('[data-testid=ov-label]')?.textContent ?? '')")
        shoot(pv, "overlay-frame", themes=False)

        pv.hover("button.greet")
        pv.wait_for_function("() => /GreetButton/.test(window.__ovRoot.querySelector('[data-testid=ov-label]').textContent)")
        pv.mouse.click(*self.center(pv, "button.greet"))
        wait_ov(pv, "ov-comment-box")
        # The click picked: the page's own submit never ran.
        self.assertEqual(pv.locator("ul.greetings li").count(), 3)
        # The picture is drawn from the page.
        pv.wait_for_function("() => window.__ovRoot.querySelector('[data-testid=ov-comment-box] img.thumb') || window.__ovRoot.querySelector('[data-testid=ov-picture-failed]')", timeout=10_000)
        self.assertEqual(ov_count(pv, "ov-picture-failed"), 0, ov_text(pv, "ov-picture-failed"))
        pv.keyboard.type("Keep Greet disabled until a name is typed.")
        ov(pv, "r => r.querySelector('[data-testid=ov-what-dude-sends]').open = true")
        shoot(pv, "overlay-comment-box", themes=False)
        pv.keyboard.press("Control+Enter")
        wait_ov(pv, "ov-saved")
        self.assertIn("with its picture", ov_text(pv, "ov-saved"))
        shoot(pv, "overlay-saved", themes=False)

        # Back in dude: the draft is in the tray, with its anchor, and on the Feedback tab with its picture.
        page.bring_to_front()
        tray = page.get_by_test_id("fb-tray")
        expect(tray).to_have_attribute("data-count", "1")
        expect(page.get_by_test_id("commenting-notice")).to_contain_text("1 draft saved from it so far")
        shoot(page, "dude-commenting-open")
        tray.get_by_role("button", name="Review").click()
        expect(tray.get_by_test_id("fb-tray-row")).to_contain_text("Preview")
        shoot(page, "dude-tray-preview")
        page.get_by_role("tab", name=re.compile("^Feedback")).click()
        draft = page.locator("[data-testid=fb-draft]").filter(has_text="Keep Greet disabled")
        expect(draft).to_have_class(re.compile("feedbackHighlight"))
        expect(draft.locator("[data-testid=fb-preview]")).to_contain_text("button.greet")
        expect(draft.locator("[data-testid=fb-preview] img")).to_have_count(1)
        expect(draft.locator("[data-testid=fb-preview] img")).to_have_attribute("src", re.compile("^blob:"))
        shoot(page, "dude-feedback-tab")

        # Show on preview marks it on the page that is open.
        draft.get_by_test_id("fb-show-on-preview").click()
        pv.wait_for_function("() => /Shown from dude/.test(window.__ovRoot.querySelector('[data-testid=ov-popover]')?.textContent ?? '')")
        shoot(pv, "overlay-show-on-preview", themes=False)

        # A page on another origin posting to dude is not heard: no draft.
        page.evaluate("() => window.open('https://evil.preview.test/', 'evil')")
        page.wait_for_timeout(800)
        self.assertEqual(page.get_by_test_id("fb-tray").get_attribute("data-count"), "1")

        # The overlay asked dude's origin for its script and nothing else.
        self.assertTrue(all(u.endswith("/feedback/overlay.js") for u in self.overlay_requests), self.overlay_requests)
        self.assertEqual(self.errors, [])

    def center(self, page, selector):
        b = page.locator(selector).bounding_box()
        return b["x"] + b["width"] / 2, b["y"] + b["height"] / 2

    def test_a_preview_opened_directly_says_to_open_it_from_dude(self):
        ctx, page = self.context()
        pv = ctx.new_page()
        self.watch(pv)
        pv.goto(f"{PREVIEW}/")
        wait_ov(pv, "ov-no-opener")
        self.assertIn("Open this preview from dude to comment", ov_text(pv, "ov-no-opener"))
        self.assertEqual(ov_count(pv, "ov-comment"), 0)
        self.assertEqual(ov_count(pv, "ov-save"), 0)
        shoot(pv, "overlay-no-opener", themes=False)
        self.assertEqual(self.errors, [])

    def test_restart_and_the_unavailable_reason(self):
        ctx, page = self.context("comment-restart")
        self.watch(page)
        notice = page.get_by_test_id("commenting-restart")
        expect(notice).to_contain_text("without the comment tool")
        expect(notice.get_by_role("button", name="Restart web")).to_be_visible()
        shoot(page, "dude-needs-restart")
        ctx2, page2 = self.context("comment-off")
        self.watch(page2)
        expect(page2.get_by_test_id("commenting-unavailable").first).to_have_text("Commenting on previews needs auth.public_url")
        self.assertEqual(page2.get_by_test_id("comment-on-server").count(), 0)
        shoot(page2, "dude-commenting-unavailable")
        page2.goto(f"{DUDE}/#/task/{TASK}/overview")
        expect(page2.get_by_test_id("servers-summary")).to_be_visible()
        self.assertEqual(self.errors, [])

    def test_narrow_overlay_is_a_sheet(self):
        ctx, page = self.context()
        page.set_viewport_size({"width": 390, "height": 844})
        self.watch(page)
        comment = page.get_by_test_id("comment-on-server").first
        expect(comment).to_be_visible(timeout=20_000)
        with ctx.expect_page() as opened:
            comment.click()
        pv = opened.value
        pv.set_viewport_size({"width": 390, "height": 844})
        pv.wait_for_function("() => window.__ovRoot?.querySelector('[data-connection=connected]')", timeout=10_000)
        ov_click(pv, "ov-comment")
        pv.hover("button.greet")
        pv.mouse.click(*self.center(pv, "button.greet"))
        wait_ov(pv, "ov-comment-box")
        width = ov(pv, "r => r.querySelector('[data-testid=ov-comment-box]').getBoundingClientRect().width")
        self.assertGreaterEqual(width, 360)
        if SHOTS:
            pv.screenshot(path=str(Path(SHOTS) / "overlay-narrow-390.png"))
        self.assertEqual(self.errors, [])


if __name__ == "__main__":
    unittest.main()
