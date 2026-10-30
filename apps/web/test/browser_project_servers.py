"""The project Servers page's windows, in a real browser, against the Vite dev server and the fixture client.

dude's tab is the web app at http://127.0.0.1:PORT (fixtures `a`, the Servers page's world of
fixtures/projectServers.ts). The branch previews are other origins answered by Playwright with
preview_page.html plus the tag lux would inject, which loads the overlay from dude's origin.

It drives: two servers of one branch preview connected to one dude tab at once, each in its own
window; Preview in a second dude tab on a window the first holds, which moves it there (the first
shows it moved, the overlay talks to the second); and a branch preview's Connect to dude, which
lands on the Servers page connected, with the server's saved feedback drawn as pins on its page.

Run with: python3 apps/web/test/browser_project_servers.py
Requires installed Playwright and Chrome (system Chrome, or Playwright's cached Chromium).
"""
import socket
import unittest
from pathlib import Path

from playwright.sync_api import sync_playwright, expect

from vite_server import start_vite, stop_vite


def free_port():
    with socket.socket() as s:
        s.bind(("127.0.0.1", 0))
        return s.getsockname()[1]


PORT = free_port()
DUDE = f"http://127.0.0.1:{PORT}"
PROJECT = "p_webconsole"
WEB = "https://web-pv-7h2k.preview.acme.dev"
ADMIN = "https://admin-pv-7h2k.preview.acme.dev"
HERE = Path(__file__).resolve().parent
PAGE = (HERE / "preview_page.html").read_text()
TAG = f'<script src="{DUDE}/feedback/overlay.js" data-dude-origin="{DUDE}" async></script>'
# The fixture's saved feedback names these elements: the page has them, so its pins are drawn on them.
ELEMENTS = '<button id="pay-button">Pay</button><p id="delivery-note">Arrives Tuesday</p>'
LAUNCH_ARGS = ["--disable-features=LocalNetworkAccessChecks,BlockInsecurePrivateNetworkRequests"]

HOLD_ROOT = """(() => {
  const attach = Element.prototype.attachShadow;
  Element.prototype.attachShadow = function (init) {
    const r = attach.call(this, init);
    if (this.localName === 'dude-preview-overlay') window.__ovRoot = r;
    return r;
  };
})()"""


def ov(page, js, arg=None):
    return page.evaluate(f"([a]) => {{ const root = window.__ovRoot; return ({js})(root, a); }}", [arg])


def wait_ov(page, selector, timeout=15_000):
    page.wait_for_function("s => window.__ovRoot && window.__ovRoot.querySelector(s)", arg=selector, timeout=timeout)


def ov_click(page, selector):
    wait_ov(page, selector)
    box = ov(page, """(r, s) => { const b = r.querySelector(s).getBoundingClientRect();
      return { x: b.left + b.width / 2, y: b.top + b.height / 2 }; }""", selector)
    page.mouse.click(box["x"], box["y"])


def stop_server(server):
    try:
        stop_vite(server)
    except PermissionError:
        server.wait(timeout=10)


class ProjectServersWindows(unittest.TestCase):
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

    def context(self):
        ctx = self.browser.new_context(viewport={"width": 1440, "height": 900}, service_workers="block")
        self.addCleanup(ctx.close)
        ctx.add_init_script(HOLD_ROOT)
        # Every dude tab of this browser answers from the fixtures, the one Connect to dude opens too.
        ctx.add_init_script(f"if (location.origin === '{DUDE}') {{ localStorage.setItem('dude.fixtures', 'a'); localStorage.removeItem('dude.fixtures.servers'); }}")
        for origin in (WEB, ADMIN, "https://web-pv-3m8q.preview.acme.dev"):
            ctx.route(f"{origin}/**", lambda r: r.fulfill(status=200, content_type="text/html",
                                                          body=PAGE.replace("<!--OVERLAY-->", ELEMENTS + TAG)))
        self.errors = []
        ctx.on("page", lambda p: p.on("pageerror", lambda e: self.errors.append(f"{p.url}: {e}")))
        return ctx

    def servers_page(self, ctx_or_page):
        page = ctx_or_page.new_page() if hasattr(ctx_or_page, "new_page") else ctx_or_page
        page.goto(f"{DUDE}/?fixtures=a&servers=off#/project/{PROJECT}/servers")
        expect(page.get_by_test_id("server-group").first).to_be_visible(timeout=20_000)
        return page

    def preview(self, ctx, page, key):
        with ctx.expect_page() as opened:
            page.locator(f"[data-key='{key}'] [data-testid=server-preview]").click()
        win = opened.value
        wait_ov(win, "[data-testid=ov-conn]")
        win.wait_for_function("() => window.__ovRoot.querySelector('[data-testid=ov-conn]').textContent === 'connected to dude'", timeout=15_000)
        return win

    def test_two_servers_connected_to_one_tab(self):
        ctx = self.context()
        page = self.servers_page(ctx)
        web = self.preview(ctx, page, "run_pv_checkout/web")
        page.bring_to_front()
        admin = self.preview(ctx, page, "run_pv_checkout/admin")
        self.assertIsNot(web, admin)
        page.bring_to_front()
        connected = page.get_by_test_id("servers-connected")
        expect(connected.locator("[data-testid=link-here]")).to_have_count(2)
        expect(connected.locator("[data-key='run_pv_checkout/web']")).to_be_visible()
        expect(connected.locator("[data-key='run_pv_checkout/admin']")).to_be_visible()
        # Each window says it is connected, and each is on its own server.
        for win, origin in ((web, WEB), (admin, ADMIN)):
            self.assertTrue(win.url.startswith(origin))
            self.assertEqual(ov(win, "r => r.querySelector('[data-testid=ov-conn]').textContent"), "connected to dude")
        self.assertEqual(self.errors, [])

    def test_preview_in_another_of_my_tabs_moves_my_window_there(self):
        ctx = self.context()
        first = self.servers_page(ctx)
        admin = self.preview(ctx, first, "run_pv_checkout/admin")
        # The second dude tab is opened from the first (one browsing context group, as a person's tabs are).
        with ctx.expect_page() as opened:
            first.evaluate(f"() => window.open('{DUDE}/#/project/{PROJECT}/servers', '_blank')")
        second = self.servers_page(opened.value)
        pages_before = len(ctx.pages)
        second.locator("[data-key='run_pv_checkout/admin'] [data-testid=server-preview]").click()
        expect(second.get_by_test_id("servers-moved")).to_contain_text("feat/checkout-polish / admin", timeout=15_000)
        # Claimed, not opened again: no new window, the same page in it.
        self.assertEqual(len(ctx.pages), pages_before)
        self.assertTrue(admin.url.startswith(ADMIN))
        expect(second.locator("[data-testid=servers-connected] [data-key='run_pv_checkout/admin'] [data-testid=link-here]")).to_be_visible()
        # The first tab shows it moved and no longer connected.
        first.bring_to_front()
        expect(first.locator("[data-key='run_pv_checkout/admin'] [data-testid=link-moved]")).to_be_visible(timeout=10_000)
        expect(first.get_by_test_id("servers-connected")).to_have_count(0)
        self.assertEqual(self.errors, [])

    def test_branch_connect_lands_connected_and_the_page_shows_saved_pins(self):
        ctx = self.context()
        preview = ctx.new_page()
        preview.goto(f"{WEB}/checkout")
        with ctx.expect_page() as dude:
            ov_click(preview, "[data-testid=ov-connect]")
        page = dude.value
        expect(page.get_by_test_id("servers-landed")).to_contain_text("feat/checkout-polish / web is connected to this tab", timeout=20_000)
        expect(page.locator("[data-testid=servers-connected] [data-testid=project-server]").first).to_have_attribute("data-key", "run_pv_checkout/web")
        # The page's saved feedback, open and resolved, as pins, with no API-only words.
        preview.wait_for_function("() => window.__ovRoot.querySelectorAll('[data-testid=ov-pin]').length === 2", timeout=15_000)
        statuses = ov(preview, "r => [...r.querySelectorAll('[data-testid=ov-pin]')].map((p) => p.dataset.status).sort()")
        self.assertEqual(statuses, ["open", "resolved"])
        self.assertNotIn("only through the CLI and API", ov(preview, "r => r.textContent"))
        self.assertEqual(self.errors, [])


if __name__ == "__main__":
    unittest.main()
