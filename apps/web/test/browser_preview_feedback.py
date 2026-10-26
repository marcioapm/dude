"""Commenting on a preview, in a real browser, against the Vite dev server and the fixture client.

dude's tab is the web app at http://127.0.0.1:PORT (fixtures, `?run=comment`: the
task's branch preview is up and its web server carries the comment tool). The
preview is another origin, https://web-shop.preview.test, answered by Playwright
with preview_page.html plus the tag lux would inject, which loads the overlay from
dude's origin. Everything between them is the browser's own postMessage.

It drives: the Servers tab's server link -> pick an element -> save -> the draft in
dude's tray and on the Feedback tab with its picture -> Show on preview; a page
opened directly offers Connect to dude, which opens dude, finds the task and
connects; dude's tab reloading reconnects by itself; a message from another
origin is ignored; Restart and the unavailable reason. Screenshots go to
DUDE_PREVIEW_SHOTS when set (1440x900 dark and light, 390x844).

Run with: python3 apps/web/test/browser_preview_feedback.py
Requires installed Playwright and Chrome (system Chrome, or Playwright's cached Chromium).
"""
import os
import re
import socket
import unittest
from pathlib import Path

from playwright.sync_api import sync_playwright, expect

from vite_server import start_vite, stop_vite


def free_port():
    """A port nothing listens on now: the other browser checks keep their fixed ones."""
    with socket.socket() as s:
        s.bind(("127.0.0.1", 0))
        return s.getsockname()[1]


PORT = free_port()
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
    anchor: { kind: "preview", url: location.origin + "/", selector: "h1" } };
  window.opener && window.opener.postMessage(draft, "*");
  document.title = "posted";
</script>"""

# In dude's tab: the origin of every message it is sent, heard before the app's own listener.
HEARD = """(() => {
  window.__heard = [];
  window.addEventListener('message', (e) => window.__heard.push(e.origin));
})()"""

# --ds-color-accent in each theme (tokens.css), as getComputedStyle writes it.
ACCENT = {"dark": "rgb(39, 121, 188)", "light": "rgb(22, 109, 175)"}
# --ds-color-overlay: the toolbar's fill.
OVERLAY = {"dark": "rgb(55, 56, 58)", "light": "rgb(255, 255, 255)"}


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
        ctx.add_init_script(HEARD)

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
        # Every load a preview page makes, by the frame that made it (Chrome sends no Referer from https to dude's http):
        # subresources by their frame, a sub-frame's navigation by its parent. The window's own navigation is not the page's.
        try:
            frame = request.frame
            by = frame.parent_frame if request.is_navigation_request() else frame
        except Exception:  # noqa: BLE001 - a request with no frame (a service worker's) is no preview page's
            return
        if by is not None and by.url.startswith(PREVIEW):
            self.overlay_requests.append(request.url)

    def watch(self, p):
        p.on("console", lambda m: m.type == "error" and self.errors.append(f"{p.url}: {m.text}"))
        p.on("pageerror", lambda e: self.errors.append(f"{p.url}: {e}"))

    def test_comment_pick_save_then_show_on_preview(self):
        ctx, page = self.context()
        self.watch(page)
        comment = page.get_by_test_id("server-url").first
        expect(comment).to_be_visible(timeout=20_000)
        expect(page.get_by_test_id("server-preview").first).to_have_attribute("rel", "noopener noreferrer")
        shoot(page, "dude-servers-comment")

        with ctx.expect_page() as opened:
            comment.click()
        pv = opened.value
        pv.wait_for_load_state("domcontentloaded")
        pv.wait_for_function("() => window.__ovRoot?.querySelector('[data-connection=connected]')", timeout=20_000)
        expect(page.get_by_test_id("commenting-notice")).to_have_attribute("data-state", "open")
        # Ana's submitted comment is pinned on the heading, the one element its selector finds.
        wait_ov(pv, "ov-pin")
        self.assertEqual(ov(pv, "r => [...r.querySelectorAll('[data-testid=ov-pin]')].map(p => p.dataset.id)"), ["fbk_ana"])
        shoot(pv, "overlay-connected", themes=False)

        # The page's own CSS never reaches the overlay: Comment is the design system's accent in the theme it draws.
        bg = ov(pv, "r => getComputedStyle(r.querySelector('[data-testid=ov-comment]')).backgroundColor")
        drawn = ov(pv, "r => r.querySelector('[data-overlay]').getAttribute('data-theme')")
        self.assertEqual(bg, ACCENT[drawn])

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

        # Page opens the item's exact URL with commenting too (not a plain noopener tab).
        page_link = draft.get_by_test_id("fb-preview-page")
        page_link.click()
        pv.wait_for_function("() => window.__ovRoot?.querySelector('[data-connection=connected]')", timeout=20_000)
        self.assertEqual(pv.url, page_link.get_attribute("href"))
        self.assertEqual(len([p for p in ctx.pages if p.url.startswith(PREVIEW)]), 1)
        page.bring_to_front()

        # Show on preview marks it on the page that is open (the tray's list, opened above, is put away first: it floats over the page's foot).
        tray.get_by_role("button", name="Hide").click()
        draft.get_by_test_id("fb-show-on-preview").click()
        pv.wait_for_function("() => /Shown from dude/.test(window.__ovRoot.querySelector('[data-testid=ov-popover]')?.textContent ?? '')")
        shoot(pv, "overlay-show-on-preview", themes=False)

        # A page on another origin posting to dude is not heard: no draft.
        page.evaluate("() => window.open('https://evil.preview.test/', 'evil')")
        page.wait_for_function("() => window.__heard.includes('https://evil.preview.test')", timeout=10_000)
        self.assertEqual(page.get_by_test_id("fb-tray").get_attribute("data-count"), "1")

        # The preview loaded the overlay from dude's origin and its own status frame: nothing else, ever.
        self.assertEqual(set(self.overlay_requests), {f"{DUDE}/feedback/overlay.js", "https://status.widgets.test/"}, self.overlay_requests)
        self.assertEqual(self.errors, [])

    def center(self, page, selector):
        b = page.locator(selector).bounding_box()
        return b["x"] + b["width"] / 2, b["y"] + b["height"] / 2

    def test_the_picture_draws_a_pseudo_element_a_canvas_and_a_video_frame(self):
        ctx, page = self.context()
        self.watch(page)
        comment = page.get_by_test_id("server-url").first
        expect(comment).to_be_visible(timeout=20_000)
        with ctx.expect_page() as opened:
            comment.click()
        pv = opened.value
        pv.wait_for_function("() => window.__ovRoot?.querySelector('[data-connection=connected]')", timeout=20_000)
        pv.wait_for_function("() => document.querySelector('.promo video').readyState >= 2", timeout=10_000)
        ov_click(pv, "ov-comment")
        wait_ov(pv, "ov-hint")
        # The promo's own padding, beside its children: the pointer is on the promo itself.
        b = pv.locator(".promo").bounding_box()
        pv.mouse.move(b["x"] + 6, b["y"] + 6)
        pv.wait_for_function("() => /Promo/.test(window.__ovRoot.querySelector('[data-testid=ov-label]')?.textContent ?? '')")
        pv.mouse.click(b["x"] + 6, b["y"] + 6)
        wait_ov(pv, "ov-comment-box")
        pv.wait_for_function("() => window.__ovRoot.querySelector('[data-testid=ov-comment-box] img.thumb') || window.__ovRoot.querySelector('[data-testid=ov-picture-failed]')", timeout=10_000)
        self.assertEqual(ov_count(pv, "ov-picture-failed"), 0, ov_text(pv, "ov-picture-failed"))
        # Each pure colour the promo shows, its share of a 64x32 CSS-pixel box that must show, and what draws it.
        colours = {
            "red": ([230, 0, 0], 1 / 2, "the ::before badge"),
            "magenta": ([200, 0, 200], 1 / 2, "the ::after data: SVG icon"),
            "blue": ([0, 0, 230], 1 / 2, "the canvas"),
            "green": ([0, 200, 0], 1 / 4, "the video's left half"),
            "yellow": ([240, 240, 0], 1 / 4, "the video's right half"),
        }
        # The picture, decoded in the page: pixels of each colour counted.
        counts = ov(pv, """async (r, { want, width }) => {
          const img = new Image();
          img.src = r.querySelector('[data-testid=ov-comment-box] img.thumb').src;
          await img.decode();
          const c = document.createElement('canvas');
          c.width = img.naturalWidth; c.height = img.naturalHeight;
          const g = c.getContext('2d');
          g.drawImage(img, 0, 0);
          const d = g.getImageData(0, 0, c.width, c.height).data;
          const n = { scale: c.width / width };
          for (const k in want) n[k] = 0;
          for (let i = 0; i < d.length; i += 4)
            for (const [k, [R, G, B]] of Object.entries(want))
              if (Math.abs(d[i] - R) < 24 && Math.abs(d[i + 1] - G) < 24 && Math.abs(d[i + 2] - B) < 24) n[k]++;
          return n;
        }""", {"want": {k: rgb for k, (rgb, _, _) in colours.items()}, "width": int(b["width"] + 16)})
        area = 64 * 32 * counts["scale"] ** 2
        for colour, (_, share, what) in colours.items():
            self.assertGreater(counts[colour], area * share, f"{what} is missing: {counts}")
        self.assertEqual(set(self.overlay_requests), {f"{DUDE}/feedback/overlay.js", "https://status.widgets.test/"}, self.overlay_requests)
        self.assertEqual(self.errors, [])

    def test_an_app_that_redirects_before_dude_answers_still_connects(self):
        ctx, page = self.context()
        self.watch(page)
        # The app's router moves / to /home as soon as the overlay is on the page, before dude's session can arrive.
        ctx.add_init_script(f"""if (location.origin === '{PREVIEW}') new MutationObserver((_, o) => {{
          if (document.querySelector('dude-preview-overlay')) {{ o.disconnect(); history.replaceState({{}}, '', '/home'); }}
        }}).observe(document, {{ childList: true, subtree: true }})""")
        comment = page.get_by_test_id("server-url").first
        expect(comment).to_be_visible(timeout=20_000)
        with ctx.expect_page() as opened:
            comment.click()
        pv = opened.value
        pv.wait_for_function("() => window.__ovRoot?.querySelector('[data-connection=connected]')", timeout=20_000)
        self.assertEqual(pv.evaluate("() => location.pathname"), "/home")
        expect(page.get_by_test_id("commenting-notice")).to_have_attribute("data-state", "open")
        # Ana's comment is on /, not /home: no pin here.
        self.assertEqual(ov_count(pv, "ov-pin"), 0)
        self.assertEqual(self.errors, [])

    def test_go_to_dude_brings_dudes_tab_forward(self):
        ctx, page = self.context()
        self.watch(page)
        comment = page.get_by_test_id("server-url").first
        expect(comment).to_be_visible(timeout=20_000)
        with ctx.expect_page() as opened:
            comment.click()
        pv = opened.value
        # Playwright emulates focus for every page by default, so each says it has it: off, the browser's own focus is read.
        for p in (page, pv):
            ctx.new_cdp_session(p).send("Emulation.setFocusEmulationEnabled", {"enabled": False})
        pv.wait_for_function("() => window.__ovRoot?.querySelector('[data-connection=connected]')", timeout=20_000)
        pv.bring_to_front()
        pv.wait_for_function("() => document.hasFocus()")
        self.assertFalse(page.evaluate("() => document.hasFocus()"))
        dude_url = page.url
        ov_click(pv, "ov-comment")
        pv.hover("button.greet")
        pv.mouse.click(*self.center(pv, "button.greet"))
        wait_ov(pv, "ov-comment-box")
        pv.keyboard.type("Go to dude after this.")
        pv.keyboard.press("Control+Enter")
        wait_ov(pv, "ov-saved")
        pages = len(ctx.pages)
        ov_click(pv, "ov-go-dude")
        # dude's tab has the focus now, the preview not; nothing was opened and dude was not navigated.
        page.wait_for_function("() => document.hasFocus()", timeout=5_000)
        self.assertFalse(pv.evaluate("() => document.hasFocus()"))
        self.assertEqual(len(ctx.pages), pages)
        self.assertEqual(page.url, dude_url)
        expect(page.get_by_test_id("fb-tray")).to_have_attribute("data-count", "1")
        self.assertEqual(self.errors, [])

    def test_enter_on_the_link_opens_it_and_a_second_click_brings_it_forward_unreloaded(self):
        ctx, page = self.context()
        self.watch(page)
        link = page.get_by_test_id("server-url").first
        expect(link).to_be_visible(timeout=20_000)
        # Keyboard activation: Enter on the focused link is a click the comment tool takes, not a plain tab.
        link.focus()
        with ctx.expect_page() as opened:
            page.keyboard.press("Enter")
        pv = opened.value
        pv.wait_for_function("() => window.__ovRoot?.querySelector('[data-connection=connected]')", timeout=20_000)
        self.assertIsNotNone(pv.evaluate("() => window.opener"))
        expect(page.get_by_test_id("commenting-notice")).to_have_attribute("data-state", "open")
        # The person moves on in the preview and starts a comment there.
        pv.evaluate("() => { window.__marker = 'kept'; history.pushState({}, '', '/cart'); }")
        ov_click(pv, "ov-comment")
        pv.hover("button.greet")
        pv.mouse.click(*self.center(pv, "button.greet"))
        wait_ov(pv, "ov-comment-box")
        pv.keyboard.type("Half a thought")
        url = pv.url
        pages = len(ctx.pages)
        # Back in dude, the server's links again: the window comes forward as it is.
        page.bring_to_front()
        for testid in ("server-url", "server-preview"):
            page.get_by_test_id(testid).first.click()
            page.wait_for_timeout(500)
            self.assertEqual(len(ctx.pages), pages, testid)
            self.assertEqual(pv.url, url, testid)
            self.assertEqual(pv.evaluate("() => window.__marker"), "kept", testid)
            self.assertEqual(ov(pv, "r => r.querySelector('[data-testid=ov-comment-box] textarea').value"), "Half a thought", testid)
        self.assertEqual(self.errors, [])

    def test_a_preview_opened_directly_offers_connect_to_dude(self):
        ctx, page = self.context()
        pv = ctx.new_page()
        self.watch(pv)
        pv.goto(f"{PREVIEW}/")
        wait_ov(pv, "ov-no-opener")
        self.assertIn("Connect to dude to comment: dude opens and finds this preview's task", ov_text(pv, "ov-no-opener"))
        self.assertEqual(ov_count(pv, "ov-comment"), 0)
        self.assertEqual(ov_count(pv, "ov-save"), 0)
        # Connect to dude is a button, not a link: nothing to copy, and nothing opened noopener.
        self.assertEqual(ov(pv, "r => r.querySelector('[data-testid=ov-connect]').localName"), "button")
        self.assertEqual(ov(pv, "r => r.querySelectorAll('a[data-k=open-dude]').length"), 0)
        shoot(pv, "overlay-no-opener", themes=False)
        self.assertEqual(self.errors, [])

    def test_connect_to_dude_opens_dude_finds_the_task_and_connects(self):
        ctx, page = self.context()
        # The fixtures and the run are remembered once dude's page has read them; then no dude tab is open.
        expect(page.get_by_test_id("server-url").first).to_be_visible(timeout=20_000)
        page.close()
        pv = ctx.new_page()
        self.watch(pv)
        pv.goto(f"{PREVIEW}/")
        wait_ov(pv, "ov-connect")
        with ctx.expect_page() as opened:
            ov_click(pv, "ov-connect")
        dude = opened.value
        self.watch(dude)
        # The new tab is dude's preview-connect page with this preview as its opener.
        dude.wait_for_url(f"{DUDE}/**", wait_until="domcontentloaded", timeout=20_000)
        self.assertTrue(dude.evaluate("() => window.opener !== null"))
        # dude finds the task, goes to it in the same tab, and the overlay connects through that tab.
        dude.wait_for_function(f"() => location.hash.includes('/task/{TASK}/')", timeout=20_000)
        pv.wait_for_function("() => window.__ovRoot?.querySelector('[data-connection=connected]')", timeout=20_000)
        self.assertEqual(ov_count(pv, "ov-no-opener"), 0)
        expect(dude.get_by_test_id("commenting-notice")).to_have_attribute("data-state", "open")
        # A draft saved from here lands in that tab.
        ov_click(pv, "ov-comment")
        pv.hover("button.greet")
        pv.mouse.click(*self.center(pv, "button.greet"))
        wait_ov(pv, "ov-comment-box")
        pv.keyboard.type("Saved through Connect to dude.")
        pv.keyboard.press("Control+Enter")
        wait_ov(pv, "ov-saved")
        expect(dude.get_by_test_id("fb-tray")).to_have_attribute("data-count", "1")
        self.assertEqual(self.errors, [])

    def test_dude_reloading_reconnects_by_itself_and_a_draft_saves(self):
        ctx, page = self.context()
        self.watch(page)
        comment = page.get_by_test_id("server-url").first
        expect(comment).to_be_visible(timeout=20_000)
        with ctx.expect_page() as opened:
            comment.click()
        pv = opened.value
        pv.wait_for_function("() => window.__ovRoot?.querySelector('[data-connection=connected]')", timeout=20_000)
        # dude's tab reloads: its new document holds no handle on the preview.
        page.reload()
        expect(page.get_by_test_id("server-url").first).to_be_visible(timeout=20_000)
        # The overlay's heartbeat reaches the reloaded tab, which adopts the window it opened before.
        expect(page.get_by_test_id("commenting-notice")).to_have_attribute("data-state", "open", timeout=8_000)
        pv.wait_for_function("() => window.__ovRoot?.querySelector('[data-connection=connected]')", timeout=8_000)
        ov_click(pv, "ov-comment")
        pv.hover("button.greet")
        pv.mouse.click(*self.center(pv, "button.greet"))
        wait_ov(pv, "ov-comment-box")
        pv.keyboard.type("Saved after dude reloaded.")
        pv.keyboard.press("Control+Enter")
        wait_ov(pv, "ov-saved")
        expect(page.get_by_test_id("fb-tray")).to_have_attribute("data-count", "1")
        self.assertEqual(self.errors, [])

    def test_the_overlay_follows_the_os_then_dudes_theme_live(self):
        ctx, page = self.context()
        self.watch(page)
        # A preview opened directly: no dude to ask, so the OS's scheme, switched live.
        direct = ctx.new_page()
        self.watch(direct)
        direct.emulate_media(color_scheme="light")
        direct.goto(f"{PREVIEW}/")
        wait_ov(direct, "ov-no-opener")
        theme = lambda p: ov(p, "r => r.querySelector('[data-overlay]').getAttribute('data-theme')")  # noqa: E731
        bar = lambda p: ov(p, "r => getComputedStyle(r.querySelector('[data-testid=ov-toolbar]')).backgroundColor")  # noqa: E731
        self.assertEqual((theme(direct), bar(direct)), ("light", OVERLAY["light"]))
        direct.emulate_media(color_scheme="dark")
        direct.wait_for_function("() => window.__ovRoot.querySelector('[data-overlay]').getAttribute('data-theme') === 'dark'")
        self.assertEqual(bar(direct), OVERLAY["dark"])
        direct.close()

        # Opened from dude: dude's resolved theme wins over the preview's OS, and follows dude when it changes.
        page.emulate_media(color_scheme="dark")
        comment = page.get_by_test_id("server-url").first
        expect(comment).to_be_visible(timeout=20_000)
        with ctx.expect_page() as opened:
            comment.click()
        pv = opened.value
        pv.emulate_media(color_scheme="light")
        pv.wait_for_function("() => window.__ovRoot?.querySelector('[data-connection=connected]')", timeout=20_000)
        pv.wait_for_function("() => window.__ovRoot.querySelector('[data-overlay]').getAttribute('data-theme') === 'dark'", timeout=10_000)
        self.assertEqual(ov(pv, "r => getComputedStyle(r.querySelector('[data-testid=ov-comment]')).backgroundColor"), ACCENT["dark"])
        # The picked element's outline and label take the accent of the theme dude sent, before and after dude switches.
        ov_click(pv, "ov-comment")
        wait_ov(pv, "ov-hint")
        pv.hover("button.greet")
        wait_ov(pv, "ov-label")
        picked = lambda p: (  # noqa: E731
            ov(p, "r => getComputedStyle(r.querySelector('[data-testid=ov-highlight]')).borderTopColor"),
            ov(p, "r => getComputedStyle(r.querySelector('[data-testid=ov-label]')).backgroundColor"),
        )
        self.assertEqual(picked(pv), (ACCENT["dark"], ACCENT["dark"]))
        page.emulate_media(color_scheme="light")
        pv.wait_for_function("() => window.__ovRoot.querySelector('[data-overlay]').getAttribute('data-theme') === 'light'", timeout=10_000)
        self.assertEqual(picked(pv), (ACCENT["light"], ACCENT["light"]))
        pv.keyboard.press("Escape")
        pv.wait_for_function("() => !window.__ovRoot.querySelector('[data-testid=ov-hint]')")
        self.assertEqual(ov(pv, "r => getComputedStyle(r.querySelector('[data-testid=ov-comment]')).backgroundColor"), ACCENT["light"])
        # The preview's own OS switching no longer moves it: dude decides now.
        # Wait until the page has seen the change, then one frame for any listener's work to land.
        before_requests = list(self.overlay_requests)
        pv.emulate_media(color_scheme="dark")
        pv.wait_for_function("() => window.matchMedia('(prefers-color-scheme: dark)').matches === true", timeout=2_000)
        pv.evaluate("() => new Promise((r) => requestAnimationFrame(() => r()))")
        self.assertEqual(theme(pv), "light")
        self.assertEqual(self.overlay_requests, before_requests)
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
        expect(page2.get_by_test_id("server-url").first).to_have_attribute("rel", "noopener noreferrer")
        shoot(page2, "dude-commenting-unavailable")
        page2.goto(f"{DUDE}/#/task/{TASK}/overview")
        expect(page2.get_by_test_id("servers-summary")).to_be_visible()
        self.assertEqual(self.errors, [])

    def test_narrow_overlay_is_a_sheet(self):
        ctx, page = self.context()
        page.set_viewport_size({"width": 390, "height": 844})
        self.watch(page)
        comment = page.get_by_test_id("server-url").first
        expect(comment).to_be_visible(timeout=20_000)
        with ctx.expect_page() as opened:
            comment.click()
        pv = opened.value
        pv.set_viewport_size({"width": 390, "height": 844})
        pv.wait_for_function("() => window.__ovRoot?.querySelector('[data-connection=connected]')", timeout=20_000)
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
