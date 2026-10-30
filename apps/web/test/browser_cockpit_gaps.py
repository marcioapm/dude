"""Task cockpit Changes gaps in a real browser against fixtures.

Run: uv run --with playwright python apps/web/test/browser_cockpit_gaps.py
"""
import os
import unittest
from pathlib import Path

from playwright.sync_api import expect, sync_playwright

from vite_server import start_vite, stop_vite

PORT = int(os.environ.get("DUDE_COCKPIT_PORT", "56952"))
URL = f"http://127.0.0.1:{PORT}"
TASK = "tsk_01j9x4kqf8b2m7e3"
SHOTS = os.environ.get("DUDE_COCKPIT_SHOTS")
MUTATIONS = "/feedback/drafts|/feedback/submit|/feedback/.+/(reply|resolve|reopen)"


class CockpitGaps(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.server = start_vite(PORT)
        cls.addClassCleanup(stop_vite, cls.server)
        cls.playwright = sync_playwright().start()
        cls.addClassCleanup(cls.playwright.stop)
        channel = os.environ.get("DUDE_TEST_BROWSER_CHANNEL", "")
        cls.browser = cls.playwright.chromium.launch(**({"channel": channel} if channel else {}), headless=True)
        cls.addClassCleanup(cls.browser.close)

    def page(self, width=1440, height=900):
        context = self.browser.new_context(viewport={"width": width, "height": height}, service_workers="block")
        self.addCleanup(context.close)
        page = context.new_page()
        errors = []
        page.on("pageerror", lambda e: errors.append(f"pageerror: {e}"))
        page.on("console", lambda m: errors.append(f"console: {m.text}") if m.type == "error" else None)
        self.addCleanup(lambda: self.assertEqual(errors, []))
        return page

    def shot(self, page, name):
        if SHOTS:
            Path(SHOTS).mkdir(parents=True, exist_ok=True)
            page.screenshot(path=str(Path(SHOTS) / f"{name}.png"))

    def side_by_side(self, page):
        """Whether the diff is split: an old line with a new line beside it on the same row."""
        return page.evaluate("""() => {
          const row = document.querySelector('[data-testid=diff-section] [data-cside=old][data-cline]');
          const right = row && [...document.querySelectorAll('[data-testid=diff-section] [data-cside=new][data-cline]')]
            .find(n => Math.abs(n.getBoundingClientRect().top - row.getBoundingClientRect().top) < 2 && n.getBoundingClientRect().left > row.getBoundingClientRect().left + 100);
          return Boolean(right);
        }""")

    def overflow(self, page):
        return page.evaluate("() => document.documentElement.scrollWidth - document.documentElement.clientWidth")

    def test_historical_comments_are_visible_navigable_read_only_and_current_restores(self):
        for width, height, theme in ((1440, 900, "dark"), (1440, 900, "light"), (390, 844, "dark")):
            page = self.page(width, height)
            calls = []
            page.on("request", lambda r: calls.append(r.url) if r.method != "GET" else None)
            page.goto(f"{URL}/?fixtures=a&branch=older#/task/{TASK}/changes?attempt=1")
            page.evaluate("t => document.documentElement.setAttribute('data-theme', t)", theme)
            expect(page.get_by_test_id("changes-read-only")).to_contain_text("Attempt 1")
            expect(page.get_by_text("Earlier attempt: keep annual invoice wording.")).to_be_visible()
            mark = page.locator('[data-testid=comment-mark][data-where=head][data-path="src/checkout/PaymentStep.tsx"]')
            mark.focus()
            self.assertEqual(page.evaluate("() => document.activeElement?.dataset.testid"), "comment-mark")
            mark.press("Enter")
            self.assertEqual(page.evaluate("() => document.activeElement?.dataset.fbItem"), "fbi_fx_old3")
            mark.press("Enter")
            self.assertEqual(page.evaluate("() => document.activeElement?.dataset.fbItem"), "fbi_fx_old1")
            self.assertEqual(page.locator("textarea").count(), 0)
            self.assertEqual(page.get_by_role("button", name="Reply").count(), 0)
            self.assertEqual(calls, [])
            self.assertEqual(self.overflow(page), 0)
            if width == 1440:
                self.assertLess(page.get_by_test_id("diff-section").first.bounding_box()["y"], 420)
            self.shot(page, f"historical-{width}-{theme}")
            if width == 1440 and theme == "dark":
                page.get_by_test_id("go-current").click()
                expect(page.get_by_text("Would a missing plan id break")).to_be_visible()
                self.assertEqual(page.get_by_text("Earlier draft: preserve the plan event.").count(), 0)

    def test_pending_checkout_states(self):
        for state, words in (("populated", "2 files"), ("empty", "No checkout changes recorded"), ("error", "Checkout changes unavailable")):
            for width, height, theme in ((1440, 900, "dark"), (1440, 900, "light"), (390, 844, "dark")):
                page = self.page(width, height)
                page.goto(f"{URL}/?fixtures=a&checkout={state}#/task/{TASK}/changes")
                page.evaluate("t => document.documentElement.setAttribute('data-theme', t)", theme)
                page.get_by_test_id("changes-scope").get_by_role("button", name="Pending work").click()
                expect(page.get_by_test_id("changes-pending")).to_contain_text("Session checkout changes (uncommitted work included)")
                expect(page.get_by_test_id("checkout-summary").first).to_contain_text(words)
                if state == "error":
                    retry = page.get_by_role("button", name="Retry").first
                    retry.focus()
                    expect(retry).to_be_focused()
                self.assertEqual(self.overflow(page), 0)
                self.shot(page, f"pending-{state}-{width}-{theme}")

    def test_snapshot_details_close_on_escape_outside_click_and_focus_leaving(self):
        page = self.page(1440, 900)
        page.goto(f"{URL}/?fixtures=a&branch=populated#/task/{TASK}/changes")
        line = page.get_by_test_id("snapshot-line")
        details = page.get_by_test_id("snapshot-details")
        # Escape closes it and gives focus back to the line.
        line.focus()
        line.press("Enter")
        expect(details).to_be_visible()
        expect(details.get_by_role("button", name="Close")).to_be_focused()
        page.keyboard.press("Escape")
        expect(details).to_have_count(0)
        expect(line).to_be_focused()
        # Tab past its last control closes it.
        line.press("Enter")
        expect(details.get_by_role("button", name="Close")).to_be_focused()
        page.keyboard.press("Tab")
        expect(details).to_have_count(0)
        # A click outside closes it.
        line.click()
        expect(details).to_be_visible()
        # A click on its text (focus leaves Close for nothing focusable) keeps it open.
        details.locator("dd").first.click()
        expect(details).to_be_visible()
        page.get_by_test_id("diff-section").first.click(position={"x": 400, "y": 60})
        expect(details).to_have_count(0)

    def test_the_first_diff_header_is_near_the_top_at_1440(self):
        # The bar, and the slim "New changes" line when there is one, are all that stand above the diff.
        for state in ("populated", "updated"):
            page = self.page(1440, 900)
            page.goto(f"{URL}/?fixtures=a&branch={state}#/task/{TASK}/changes")
            page.get_by_test_id("diff-section").first.wait_for()
            if state == "updated":
                expect(page.get_by_test_id("changes-newer")).to_be_visible()
            top = page.get_by_test_id("diff-section").first.bounding_box()["y"]
            self.assertLess(top, 420, state)

    def test_phone_changes_are_unified_only_on_a_bar_of_at_most_two_rows(self):
        page = self.page(390, 844)
        page.goto(f"{URL}/?fixtures=a&branch=populated#/task/{TASK}/changes")
        page.get_by_test_id("diff-section").first.wait_for()
        bar = page.get_by_test_id("changes-bar")
        expect(page.get_by_test_id("diff-view")).to_be_hidden()
        self.assertFalse(self.side_by_side(page))
        expect(bar.locator(".snapshotTotals")).to_be_hidden()
        # Two rows of toolbar-sized controls at most: the Segmented's height is one row.
        row = page.get_by_test_id("changes-scope").bounding_box()["height"]
        self.assertLessEqual(bar.bounding_box()["height"], 2 * row + 24, (bar.bounding_box(), row))
        self.assertEqual(self.overflow(page), 0)

    def test_split_chosen_on_desktop_turns_unified_on_a_phone(self):
        page = self.page(1440, 900)
        page.goto(f"{URL}/?fixtures=a&branch=populated#/task/{TASK}/changes")
        page.get_by_test_id("diff-view").get_by_role("button", name="Split").click()
        split = "[data-testid=diff-section] [data-cside=old][data-cline]"
        page.locator(split).first.wait_for()
        self.assertTrue(self.side_by_side(page))
        page.set_viewport_size({"width": 390, "height": 844})
        expect(page.get_by_test_id("diff-view")).to_be_hidden()
        page.wait_for_timeout(200)
        self.assertFalse(self.side_by_side(page))
        self.assertEqual(self.overflow(page), 0)

    def test_phone_header_actions_do_not_overlap_the_title(self):
        # Overview (where a task opens) and Findings scroll the whole page; Changes is a fixed pane.
        for tab in ("", "/findings", "/changes"):
            page = self.page(390, 844)
            page.goto(f"{URL}/?fixtures=a#/task/{TASK}{tab}")
            expect(page.get_by_test_id("edit-task")).to_be_visible()
            boxes = page.evaluate("""() => {
              const title = document.querySelector('h1')?.getBoundingClientRect();
              const buttons = [...document.querySelectorAll('button')].filter(b => /^(Stop|Edit)$/.test(b.textContent.trim()))
                .map(b => b.getBoundingClientRect());
              return { title, buttons };
            }""")
            title = boxes["title"]
            self.assertGreaterEqual(len(boxes["buttons"]), 2, (tab, boxes))
            for b in boxes["buttons"]:
                # The actions wrap onto their own line above the title: the title starts below them.
                self.assertGreaterEqual(title["top"], b["bottom"], (tab, boxes))
            self.assertEqual(self.overflow(page), 0)


if __name__ == "__main__":
    unittest.main()
