"""Existing owner override panel in a real browser; fixture transport, not server authorization."""
import unittest
from playwright.sync_api import sync_playwright, expect
from vite_server import start_vite, stop_vite


class OwnerOverride(unittest.TestCase):
    def test_running_conductor_escalation_override(self):
        server = start_vite(56961)
        try:
            with sync_playwright() as p:
                browser = p.chromium.launch(headless=True)
                page = browser.new_page(service_workers="block")
                def fixture(route):
                    response = route.fetch()
                    source = response.text() + """
const originalTask = FixtureClient.prototype.getTask;
FixtureClient.prototype.getTask = async function(id) {
  const task = await originalTask.call(this, id);
  return {...task, status:'running', decider:'conductor',
    escalation: window.overrideDone ? null : {reason:'implement_failed', holder:'conductor', status:'pending',
      detail:null, actions:['retry','stop'], at:'2026-11-01T00:00:00Z'}};
};
FixtureClient.prototype.decide = async function(id, action) {
  window.overrideDone = action === 'stop'; return {action};
};
"""
                    route.fulfill(response=response, body=source)
                page.route("**/src/fixtures/client.ts", fixture)
                page.goto("http://127.0.0.1:56961/?fixtures=d#/task/tsk_01j9x4kqf8b2m7e3")
                expect(page.get_by_test_id("escalation")).to_be_visible()
                expect(page.get_by_test_id("escalation")).to_contain_text("The conductor is handling this")
                page.get_by_test_id("escalation-stop").click()
                page.wait_for_function("window.overrideDone === true")
                expect(page.get_by_test_id("escalation")).to_have_count(0)
                browser.close()
        finally:
            stop_vite(server)


if __name__ == "__main__":
    unittest.main()
