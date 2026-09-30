"""Where a session's lux terminal shows, laid out by a real browser.

The rail's terminal and the header's fallback are chosen by a container
query on the session's width (app.css), which happy-dom does not lay out.
Against the fixture client; no backend or external network required.

Run with: python3 apps/web/test/browser_terminal.py
Requires installed Playwright and system Chrome.
"""
import os
import re
import unittest
from unittest import mock
from urllib.request import urlopen

from playwright.sync_api import BrowserType, sync_playwright

from vite_server import start_vite, stop_vite

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
        # Each cleanup is registered as its resource is acquired: unittest
        # runs class cleanups even when setUpClass raises, tearDownClass not.
        cls.server = start_vite(PORT)
        cls.addClassCleanup(stop_vite, cls.server)
        cls.playwright = sync_playwright().start()
        cls.addClassCleanup(cls.playwright.stop)
        cls.browser = cls.playwright.chromium.launch(channel="chrome", headless=True)
        cls.addClassCleanup(cls.browser.close)
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
            # The terminal shows only for a running Run: once the header offers Resume, the paused Run has landed.
            screen.get_by_role("button", name="Resume").wait_for(timeout=10000)
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


class FailedSetup(unittest.TestCase):
    """A setup that fails after Vite is up still stops the Vite it started."""

    def test_a_browser_that_will_not_launch_leaves_no_server(self):
        suite = unittest.defaultTestLoader.loadTestsFromName("test_a_wide_conversation_has_it_in_the_rail_only", TerminalPlacement)
        with mock.patch.object(BrowserType, "launch", side_effect=RuntimeError("no Chrome")):
            result = unittest.TestResult()
            suite.run(result)
        server = TerminalPlacement.server
        # Should the cleanup be missing, stop the server here so the port is free for the next run.
        self.addCleanup(stop_vite, server)
        self.assertTrue(any("no Chrome" in e for _, e in result.errors), result.errors)
        self.assertIsNotNone(server.poll(), f"Vite (pid {server.pid}) still running")
        # Vite itself, a child of `bun run`, is gone too: nothing left in the group, nothing on the port.
        with self.assertRaises(ProcessLookupError):
            os.killpg(server.pid, 0)
        with self.assertRaises(OSError):
            urlopen(URL, timeout=1)


if __name__ == "__main__":
    unittest.main()
