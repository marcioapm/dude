"""The page holds still while a session's list and rail scroll, laid out by a real browser.

A screen-reader-only word (`ds-sr-only`) in them must not lengthen the page;
happy-dom does not lay out, so this needs Chrome.
Against the fixture client; no backend or external network required.

Run with: python3 apps/web/test/browser_layout.py
Requires installed Playwright and system Chrome (DUDE_TEST_BROWSER_CHANNEL= for Playwright's Chromium).
"""
import os
import unittest
from pathlib import Path

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
# Where the drafts tray sits in the task page's scroll area: its edges against
# the area's, and the last feedback card's bottom with the area scrolled to its
# start and to its end.
TRAY = """async () => {
  const area = document.querySelector('[data-testid=task-screen]');
  const frame = () => new Promise((r) => requestAnimationFrame(() => requestAnimationFrame(r)));
  const measure = () => {
    const a = area.getBoundingClientRect();
    const tray = document.querySelector('[data-testid=fb-tray]')?.getBoundingClientRect() ?? null;
    const cards = document.querySelectorAll('[data-testid=feedback-tab] article');
    const last = cards[cards.length - 1].getBoundingClientRect();
    return { areaBottom: a.bottom, trayTop: tray?.top ?? null, trayBottom: tray?.bottom ?? null, lastBottom: last.bottom };
  };
  area.scrollTop = 0;
  await frame();
  const top = measure();
  area.scrollTop = area.scrollHeight;
  await frame();
  return { top, end: measure() };
}"""
SHOTS = os.environ.get("DUDE_TRAY_SHOTS")


class PageHoldsStill(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        # Each cleanup is registered as its resource is acquired: unittest
        # runs class cleanups even when setUpClass raises, tearDownClass not.
        cls.server = start_vite(PORT)
        cls.addClassCleanup(stop_vite, cls.server)
        cls.playwright = sync_playwright().start()
        cls.addClassCleanup(cls.playwright.stop)
        # System Chrome, as CI's e2e runs; DUDE_TEST_BROWSER_CHANNEL set but empty is Playwright's bundled Chromium (tests/helpers.py).
        channel = os.environ.get("DUDE_TEST_BROWSER_CHANNEL", "chrome")
        cls.browser = cls.playwright.chromium.launch(**({"channel": channel} if channel else {}), headless=True)
        cls.addClassCleanup(cls.browser.close)

    def page_overflow(self, scenario, box):
        """How far the page scrolls once `box`, on the fixture task's Sessions tab in `scenario`, is overfilled."""
        context = self.browser.new_context(viewport={"width": 1440, "height": 900}, service_workers="block")
        self.addCleanup(context.close)
        page = context.new_page()
        page.goto(f"{URL}/?fixtures={scenario}#/task/{TASK}")
        page.get_by_role("tab", name="Sessions").click()
        if box == '[data-testid="sessions"]':
            page.get_by_test_id("search-selector-trigger").click()
        page.locator(f"{box} .ds-sr-only").first.wait_for(state="attached", timeout=15000)
        return page.evaluate(OVERFILL, box)

    def test_a_long_session_list_scrolls_in_itself(self):
        # Scenario d has a completed session, whose mark is an sr-only word.
        self.assertEqual(self.page_overflow("d", '[data-testid="sessions"]'), 0)

    def test_a_long_rail_scrolls_in_itself(self):
        # The rail's terminal link says, in an sr-only word, that it opens a new tab.
        self.assertEqual(self.page_overflow("a", ".runRail"), 0)

    def test_the_overview_fills_its_pane_and_its_folds_their_box(self):
        # Overview has no rail: two columns once its own pane is 1000px wide, one below; its
        # figures spread evenly across the summary, and relations, the full metrics, the goal
        # and each criterion, opened, span their fold.
        context = self.browser.new_context(viewport={"width": 1920, "height": 1200}, service_workers="block")
        self.addCleanup(context.close)
        page = context.new_page()
        page.goto(f"{URL}/?fixtures=d#/task/{TASK}")
        page.get_by_test_id("overview-summary").wait_for()
        for fold in ("time-cost", "goal-criteria", "relations"):
            page.get_by_test_id(f"overview-{fold}").evaluate("d => { d.open = true; }")
        page.get_by_test_id("task-metrics").wait_for()
        columns = page.evaluate("""() => {
          const grid = document.querySelector('.overviewGrid');
          return getComputedStyle(grid).gridTemplateColumns.split(' ').length;
        }""")
        self.assertEqual(columns, 2)
        self.assertIsNone(page.query_selector("[data-testid=task-overview] .railPanel, [data-testid=task-overview] [aria-label='Pull requests, machines and servers']"))
        widths = page.evaluate("""() => {
          const tiles = [...document.querySelectorAll('[data-testid=overview-summary] > div')];
          const group = tiles[0].parentElement;
          const relations = document.querySelector('[data-testid=relations]');
          const metrics = [...document.querySelectorAll('[data-testid=task-metrics] [class*="_group_"] > div')];
          return { relations: relations.getBoundingClientRect().width, parent: relations.parentElement.getBoundingClientRect().width,
            tiles: tiles.map(t => t.getBoundingClientRect().width), right: tiles.at(-1).getBoundingClientRect().right,
            edge: group.getBoundingClientRect().right,
            metricTiles: metrics.map(t => t.getBoundingClientRect().width),
            metricsRight: metrics.at(-1).getBoundingClientRect().right, metricsEdge: metrics[0].parentElement.getBoundingClientRect().right };
        }""")
        self.assertGreater(widths["parent"], 820)
        self.assertAlmostEqual(widths["relations"], widths["parent"], delta=1)
        self.assertAlmostEqual(widths["right"], widths["edge"], delta=1)
        self.assertLess(max(widths["tiles"]) - min(widths["tiles"]), 1)
        self.assertAlmostEqual(widths["metricsRight"], widths["metricsEdge"], delta=1)
        self.assertLess(max(widths["metricTiles"]) - min(widths["metricTiles"]), 1)
        # The Goal's and each criterion's Markdown span their box: no measure cap stops them short.
        markdown = page.evaluate("""() => {
          const content = (el) => { const s = getComputedStyle(el);
            return el.getBoundingClientRect().width - parseFloat(s.paddingLeft) - parseFloat(s.paddingRight)
              - parseFloat(s.borderLeftWidth) - parseFloat(s.borderRightWidth); };
          const goal = document.querySelector('section[aria-label=Goal]');
          const items = [...document.querySelectorAll('ul[aria-label="Acceptance criteria"] > li')];
          return { goal: [goal.querySelector('[data-testid="overview-goal"]').getBoundingClientRect().width, content(goal)],
            criteria: items.map(li => [li.querySelector('[data-testid="overview-criterion"]').getBoundingClientRect().width, content(li)]) };
        }""")
        self.assertGreater(markdown["goal"][1], 820)
        self.assertAlmostEqual(*markdown["goal"], delta=1)
        self.assertGreater(len(markdown["criteria"]), 0)

        for block, box in markdown["criteria"]:
            self.assertGreater(box, 820)
            self.assertAlmostEqual(block, box, delta=1)
        page.get_by_role("tab", name="Activity").click()
        page.get_by_test_id("activity-item").first.wait_for()
        timeline = page.get_by_test_id("activity")
        sizes = timeline.evaluate("""el => {
          const p = el.parentElement; const s = getComputedStyle(p);
          return [el.getBoundingClientRect().width, p.getBoundingClientRect().width - parseFloat(s.paddingLeft) - parseFloat(s.paddingRight)];
        }""")
        self.assertGreater(sizes[1], 820)
        self.assertAlmostEqual(sizes[0], sizes[1], delta=1)

    def test_the_overview_stacks_when_its_pane_is_narrow_and_never_overflows_a_phone(self):
        for width, height in ((1100, 900), (390, 844)):
            context = self.browser.new_context(viewport={"width": width, "height": height}, service_workers="block")
            self.addCleanup(context.close)
            page = context.new_page()
            page.goto(f"{URL}/?fixtures=d#/task/{TASK}")
            page.get_by_test_id("overview-summary").wait_for()
            at = page.evaluate("""() => ({
              columns: getComputedStyle(document.querySelector('.overviewGrid')).gridTemplateColumns.split(' ').length,
              overflow: document.documentElement.scrollWidth - document.documentElement.clientWidth,
              pane: document.querySelector('.taskOverviewPane').getBoundingClientRect().width })""")
            # At 1100 the sidebar leaves the pane under 1000px: the pane decides, not the window.
            self.assertLess(at["pane"], 1000, f"{width}: {at}")
            self.assertEqual(at["columns"], 1, f"{width}: {at}")
            self.assertEqual(at["overflow"], 0, f"{width}: {at}")

    def test_rail_notes_use_muted_ink(self):
        context = self.browser.new_context(viewport={"width": 1440, "height": 900}, service_workers="block")
        self.addCleanup(context.close)
        page = context.new_page()
        page.goto(f"{URL}/?fixtures=d#/task/{TASK}/findings")
        note = page.get_by_text("Counts cover every finding in this attempt, before filters.", exact=True)
        note.wait_for()
        self.assertTrue(note.evaluate("""el => {
          const probe = document.createElement('span');
          probe.style.color = 'var(--ds-color-text-muted)';
          el.append(probe);
          const muted = getComputedStyle(probe).color;
          probe.remove();
          return getComputedStyle(el).color === muted;
        }"""))

    def test_the_drafts_tray_sits_at_the_foot_of_the_scroll_area(self):
        # The owner's report: on Feedback, the tray drew over the middle of the page with cards below it.
        # 2 drafts at 1440x900 is the owner's: the page then scrolls, and the tray stayed at the window's foot as the page moved.
        for width, height in ((1440, 900), (390, 844)):
            context = self.browser.new_context(viewport={"width": width, "height": height}, service_workers="block")
            self.addCleanup(context.close)
            page = context.new_page()
            page.goto(f"{URL}/?fixtures=a&run=comment#/task/{TASK}/feedback")
            page.get_by_test_id("feedback-needs").wait_for(timeout=15000)
            for drafts in (0, 1, 2, 9):
                while page.locator("[data-testid=fb-draft]").count() < drafts:
                    n = page.locator("[data-testid=fb-draft]").count()
                    page.get_by_test_id("general-open").click()
                    page.get_by_label("Comment").fill(f"Draft {n}: " + "a line long enough to wrap on a phone " * 4)
                    page.get_by_test_id("fb-save").click()
                    page.wait_for_function("n => document.querySelectorAll('[data-testid=fb-draft]').length === n", arg=n + 1)
                for review in ((False, True) if drafts else (False,)):
                    if drafts:
                        toggle = page.get_by_test_id("fb-tray-toggle")
                        if (toggle.get_attribute("aria-expanded") == "true") != review:
                            toggle.click()
                        page.get_by_test_id("fb-tray-panel").wait_for(state="visible" if review else "detached")
                    for theme in ("light", "dark"):
                        page.evaluate("t => document.documentElement.setAttribute('data-theme', t)", theme)
                        at = page.evaluate(TRAY)
                        where = f"{width}px, {drafts} drafts, review {'open' if review else 'closed'}, {theme}"
                        if drafts == 0:
                            self.assertIsNone(at["end"]["trayTop"], where)
                            self.assertLessEqual(at["end"]["lastBottom"], at["end"]["areaBottom"] + 0.5, where)
                        else:
                            for edge in ("top", "end"):
                                self.assertAlmostEqual(at[edge]["trayBottom"], at[edge]["areaBottom"], delta=0.5, msg=f"{where}, scrolled to {edge}")
                            # Scrolled to the end, the last card is wholly above the tray: nothing is left under it.
                            self.assertLessEqual(at["end"]["lastBottom"], at["end"]["trayTop"] + 0.5, where)
                        if SHOTS:
                            Path(SHOTS).mkdir(parents=True, exist_ok=True)
                            page.screenshot(path=str(Path(SHOTS) / f"tray-{drafts}-{'open' if review else 'closed'}-{width}-{theme}.png"))

    def test_the_open_session_is_drawn_in_the_selector(self):
        # Open the third session, reopen the list: the keyboard's row is the
        # first, the open one is the third, and only the open one is washed.
        context = self.browser.new_context(viewport={"width": 1440, "height": 900}, service_workers="block")
        self.addCleanup(context.close)
        page = context.new_page()
        page.goto(f"{URL}/?fixtures=d#/task/{TASK}")
        page.get_by_role("tab", name="Sessions").click()
        trigger = page.get_by_test_id("search-selector-trigger")
        trigger.click()
        options = page.get_by_test_id("sessions").get_by_role("option")
        options.nth(2).wait_for()
        options.nth(2).dispatch_event("mousedown")
        trigger.click()
        options.nth(2).wait_for()
        page.mouse.move(0, 0)
        self.assertEqual(options.nth(2).get_attribute("aria-current"), "true")
        washes = page.evaluate("""() => [...document.querySelectorAll('[data-testid="sessions"] [role=option]')]
          .map((o) => getComputedStyle(o).backgroundColor)""")
        clear = washes[1]
        self.assertNotIn(washes[2], (clear, washes[0]))
        self.assertEqual([w for i, w in enumerate(washes) if i not in (0, 2)], [clear] * (len(washes) - 2))

    def test_the_held_panel_fills_the_notice_row(self):
        # Each repository row spans the panel's body, and Done without a pull
        # request ends where the rows end, at 1440 and at 1024.
        for width in (1440, 1024):
            context = self.browser.new_context(viewport={"width": width, "height": 900}, service_workers="block")
            self.addCleanup(context.close)
            page = context.new_page()
            page.goto(f"{URL}/?fixtures=d&run=held#/task/{TASK}")
            page.get_by_test_id("ready-for-review").wait_for()
            at = page.evaluate("""() => {
              const p = document.querySelector('[data-testid=ready-for-review]');
              const body = p.querySelector('[aria-label="What would be opened"]').parentElement;
              const done = [...p.querySelectorAll('button')].find((b) => b.textContent.trim() === 'Done without a pull request');
              const right = (el) => el.getBoundingClientRect().right;
              return { panel: right(p) - parseFloat(getComputedStyle(p).paddingRight), body: right(body),
                       rows: [...p.querySelectorAll('[aria-label="What would be opened"] li')].map(right), done: right(done) };
            }""")
            where = f"{width}: {at}"
            self.assertAlmostEqual(at["body"], at["panel"], delta=1, msg=where)
            for row in at["rows"]:
                self.assertAlmostEqual(row, at["body"], delta=1, msg=where)
            self.assertAlmostEqual(at["done"], at["body"], delta=1, msg=where)


if __name__ == "__main__":
    unittest.main()
