"""Where a session's lux terminal shows, laid out by a real browser.

The rail's terminal and the header's fallback are chosen by a container
query on the session's width (app.css), which happy-dom does not lay out.
Against the fixture client; no backend or external network required.

Run with: python3 apps/web/test/browser_terminal.py
Requires installed Playwright and system Chrome.
"""
import re
import subprocess
import unittest
from pathlib import Path
from time import sleep
from urllib.request import urlopen

from playwright.sync_api import sync_playwright

WEB = Path(__file__).resolve().parents[1]
PORT = 5198
URL = f"http://127.0.0.1:{PORT}"
RUN = "run_01j9x5m2q7k8e4t1"
TERMINAL = "https://lux.example.com/runs/run_k3jq7x2mfa9vbn4z/terminal"
# The session's width at which the rail hides and the header takes the terminal.
RAIL_MIN_EXCLUSIVE = 820
# Viewports whose session lands on each side of that width: asserted, not assumed.
WIDE = (1440, 900)
NARROW = [(1024, 768), (375, 812)]


class TerminalPlacement(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.server = subprocess.Popen(
            ["bun", "run", "dev", "--host", "127.0.0.1", "--port", str(PORT), "--strictPort"],
            cwd=WEB, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        for _ in range(100):
            if cls.server.poll() is not None:
                raise RuntimeError("Local Vite server failed to start")
            try:
                with urlopen(URL, timeout=1):
                    break
            except OSError:
                sleep(0.1)
        else:
            cls.server.terminate()
            raise RuntimeError("Local Vite server did not become ready")
        cls.playwright = sync_playwright().start()
        cls.browser = cls.playwright.chromium.launch(channel="chrome", headless=True)

    @classmethod
    def tearDownClass(cls):
        cls.browser.close()
        cls.playwright.stop()
        cls.server.terminate()
        cls.server.wait(timeout=10)

    def session(self, viewport, run_as=None, view="Conversation"):
        """The fixture Run's session at `viewport`; `run_as` is the fixtures' run knob ("paused")."""
        width, height = viewport
        context = self.browser.new_context(viewport={"width": width, "height": height}, service_workers="block")
        self.addCleanup(context.close)
        init = "localStorage.setItem('dude.fixtures', 'a');"
        init += f"localStorage.setItem('dude.fixtures.run', '{run_as}');" if run_as else "localStorage.removeItem('dude.fixtures.run');"
        context.add_init_script(init)
        page = context.new_page()
        page.goto(f"{URL}/#/session/{RUN}")
        screen = page.get_by_test_id("run-screen")
        screen.wait_for(timeout=15000)
        if view != "Conversation":
            # Events carries its count in the label: "Events 23".
            page.get_by_test_id("session-view").get_by_text(re.compile(rf"^{view}\s*\d*$")).click()
            page.wait_for_selector("[data-testid=run-screen]:not([data-view=chat])", timeout=5000)
        if run_as is None:
            # The URL is read after the Run: wait for the link to exist, visible or not.
            page.wait_for_selector("[data-testid=run-screen] [data-testid=terminal-link], [data-testid=run-screen] [data-testid=terminal-icon]",
                                   state="attached", timeout=10000)
        else:
            # Nothing to wait for that says "no terminal": wait for the view, then for the Run's reads to land.
            page.get_by_test_id("session-rail" if view == "Conversation" else "event-log").wait_for(state="attached", timeout=10000)
            page.wait_for_timeout(500)
        return page, screen.evaluate("e => e.getBoundingClientRect().width")

    @staticmethod
    def visible_terminals(page):
        """Every terminal link in the session a person can see: (where, href)."""
        links = page.locator("[data-testid=run-screen] [data-testid=terminal-link], [data-testid=run-screen] [data-testid=terminal-icon]")
        return [
            ("rail" if links.nth(i).evaluate("e => !!e.closest('[data-testid=session-rail]')") else "header", links.nth(i).get_attribute("href"))
            for i in range(links.count()) if links.nth(i).is_visible()
        ]

    def test_a_wide_conversation_has_it_in_the_rail_only(self):
        page, width = self.session(WIDE)
        self.assertGreater(width, RAIL_MIN_EXCLUSIVE)
        self.assertEqual(self.visible_terminals(page), [("rail", TERMINAL)])

    def test_a_narrow_conversation_has_it_in_the_header_only(self):
        for viewport in NARROW:
            with self.subTest(viewport=viewport):
                page, width = self.session(viewport)
                self.assertLessEqual(width, RAIL_MIN_EXCLUSIVE)
                self.assertFalse(page.get_by_test_id("session-rail").is_visible())
                self.assertEqual(self.visible_terminals(page), [("header", TERMINAL)])

    def test_changes_and_events_have_it_in_the_header_at_any_width(self):
        for view in ("Changes", "Events"):
            for viewport in [WIDE, *NARROW]:
                with self.subTest(view=view, viewport=viewport):
                    page, _ = self.session(viewport, view=view)
                    self.assertEqual(self.visible_terminals(page), [("header", TERMINAL)])

    def test_a_paused_run_has_none(self):
        for view in ("Conversation", "Events"):
            for viewport in [WIDE, *NARROW]:
                with self.subTest(view=view, viewport=viewport):
                    page, _ = self.session(viewport, run_as="paused", view=view)
                    self.assertEqual(self.visible_terminals(page), [])


if __name__ == "__main__":
    unittest.main()
