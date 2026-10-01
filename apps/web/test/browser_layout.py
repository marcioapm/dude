"""The page holds still while a session's list and rail scroll, laid out by a real browser.

A screen-reader-only word (`ds-sr-only`) is absolutely positioned. Deep in a
scrolling list with no positioned box around it, it was placed against the
page instead, as far down as that list's content ran: the page itself grew a
scrollbar onto empty canvas. happy-dom does not lay out, so this needs Chrome.
Against the fixture client; no backend or external network required.

Run with: python3 apps/web/test/browser_layout.py
Requires installed Playwright and system Chrome.
"""
import unittest

from playwright.sync_api import sync_playwright

from vite_server import start_vite, stop_vite

PORT = 5196
URL = f"http://127.0.0.1:{PORT}"
TASK = "tsk_01j9x4kqf8b2m7e3"
# Clones a box's children until it holds far more than it shows; returns how
# far past the window the page itself then scrolls.
OVERFILL = """(selector) => {
  const box = document.querySelector(selector);
  const items = [...box.children];
  for (let i = 0; i < 12; i++) for (const item of items) box.append(item.cloneNode(true));
  if (box.scrollHeight <= box.clientHeight) throw new Error(`${selector} did not overflow`);
  const page = document.scrollingElement;
  return page.scrollHeight - page.clientHeight;
}"""


class PageHoldsStill(unittest.TestCase):
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

    def sessions(self, scenario):
        """The fixture task's Sessions tab in `scenario`, at a desktop window."""
        context = self.browser.new_context(viewport={"width": 1440, "height": 900}, service_workers="block")
        self.addCleanup(context.close)
        page = context.new_page()
        page.goto(f"{URL}/?fixtures={scenario}#/task/{TASK}")
        page.get_by_role("tab", name="Sessions").click()
        page.get_by_test_id("session-rail").wait_for(timeout=15000)
        return page

    def test_a_long_session_list_scrolls_in_itself(self):
        # Scenario d has a completed session, whose mark is an sr-only word.
        page = self.sessions("d")
        page.locator(".taskSessionList .ds-sr-only").first.wait_for(state="attached")
        self.assertEqual(page.evaluate(OVERFILL, ".taskSessionList"), 0)

    def test_a_long_rail_scrolls_in_itself(self):
        # The rail's terminal link says, in an sr-only word, that it opens a new tab.
        page = self.sessions("a")
        page.locator(".runRail .ds-sr-only").first.wait_for(state="attached")
        self.assertEqual(page.evaluate(OVERFILL, ".runRail"), 0)


if __name__ == "__main__":
    unittest.main()
