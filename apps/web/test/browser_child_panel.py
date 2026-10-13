"""An opened subagent sits beside its parent transcript, sized to the session, laid out by a real browser.

On a desktop window the panel takes at most 40% of the session and scrolls in
itself; below 960px it opens as a dialog instead. happy-dom does not lay out,
so this needs Chrome. Against the fixture client; no backend or external
network required.

Run with: python3 apps/web/test/browser_child_panel.py
Requires installed Playwright and system Chrome.
"""
import unittest

from playwright.sync_api import expect, sync_playwright

from vite_server import start_vite, stop_vite

PORT = 5198
URL = f"http://127.0.0.1:{PORT}"
RUN = "run_01j9x5m2q7k8e4t1"
TITLE = "Layout probe"
# A task call, its child, and a message long enough that the child panel overflows.
EMIT_CHILD = """async ([runId, title]) => {
  const {emit} = await import('/src/fixtures/client.ts');
  const base = {runId, organizationId: 'org_test', projectId: null, taskId: null, sessionId: null,
    workflowRunId: null, actor: {type: 'agent', id: 'test'}, source: 'runner', correlationId: null,
    causationId: null, occurredAt: new Date().toISOString()};
  emit({...base, eventType: 'agent.tool.called', payload: {tool: 'task', callId: 'probe_call', input: {description: title}}});
  emit({...base, eventType: 'agent.child.started', payload: {childId: 'probe_child', callId: 'probe_call', kind: 'task', title}});
  emit({...base, eventType: 'agent.child.message', payload: {childId: 'probe_child', callId: 'probe_call',
    text: Array.from({length: 200}, (_, i) => 'Line ' + i + '\\n\\n').join('')}});
}"""
# Widths of the session, the panel and the parent transcript beside it, and
# what scrolling the panel by 300px does to the parent and the page.
MEASURE = """() => {
  const panel = document.querySelector('aside:has(> [data-testid="child-panel"])');
  const session = panel.parentElement;
  const parent = session.firstElementChild;
  const scroller = panel.querySelector('[data-testid="child-panel"]').lastElementChild;
  const parentBefore = parent.scrollTop;
  scroller.scrollTop = 300;
  const page = document.scrollingElement;
  return {
    session: session.getBoundingClientRect().width,
    child: panel.getBoundingClientRect().width,
    parent: parent.getBoundingClientRect().width,
    childScroll: scroller.scrollTop,
    parentBefore,
    parentAfter: parent.scrollTop,
    pageOverflow: page.scrollHeight - page.clientHeight,
  };
}"""


class ChildPanelLayout(unittest.TestCase):
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

    def open_child(self, width):
        """The fixture run at `width`, with its one subagent opened."""
        context = self.browser.new_context(viewport={"width": width, "height": 900}, service_workers="block")
        self.addCleanup(context.close)
        page = context.new_page()
        page.add_init_script("localStorage.setItem('dude.fixtures', 'a')")
        page.goto(f"{URL}/#/session/{RUN}")
        expect(page.get_by_test_id("run-screen")).to_be_visible(timeout=15000)
        page.evaluate(EMIT_CHILD, [RUN, TITLE])
        row = page.get_by_test_id("child-thread").filter(has=page.get_by_text(TITLE, exact=True))
        row.get_by_role("button").click()
        expect(page.get_by_test_id("child-panel")).to_be_visible()
        return page

    def test_desktop_panel_takes_at_most_two_fifths_of_the_session(self):
        for width in (960, 1280, 1920):
            with self.subTest(width=width):
                page = self.open_child(width)
                expect(page.get_by_role("dialog")).to_have_count(0)
                m = page.evaluate(MEASURE)
                self.assertLessEqual(m["child"], m["session"] * 0.40 + 1, m)
                self.assertGreaterEqual(m["parent"], m["session"] * 0.60 - 1, m)
                self.assertEqual(m["childScroll"], 300, m)
                self.assertEqual(m["parentAfter"], m["parentBefore"], m)
                self.assertEqual(m["pageOverflow"], 0, m)

    def test_narrow_window_opens_the_panel_as_a_dialog(self):
        page = self.open_child(959)
        expect(page.get_by_role("dialog")).to_be_visible()
        expect(page.locator('aside:has(> [data-testid="child-panel"])')).to_have_count(0)


if __name__ == "__main__":
    unittest.main()
