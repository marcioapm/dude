"""Local browser auth regressions; no backend or external network required.

Run with: python3 apps/web/test/browser_auth.py
Requires installed Playwright and system Chrome.
"""
import json
import subprocess
import unittest
from pathlib import Path
from urllib.request import urlopen
from time import sleep

from playwright.sync_api import expect, sync_playwright

WEB = Path(__file__).resolve().parents[1]
URL = "http://127.0.0.1:5197"
PERSON = {"id": "person", "name": "Local Person", "email": "local@example.invalid",
          "role": "admin", "photoUrl": None, "online": True, "lastSeenAt": None,
          "lastSeenWhere": None}


class BrowserAuth(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.server = subprocess.Popen(
            ["bun", "run", "dev", "--host", "127.0.0.1", "--port", "5197", "--strictPort"],
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

    def setUp(self):
        self.context = self.browser.new_context(service_workers="block")
        self.page = self.context.new_page()
        self.probes = []
        self.streams = []
        self.responses = []
        # Answers to /v1/navigation in order, then the empty tree.
        self.nav_responses = []
        self.nav_calls = []
        # What /v1/navigation answers with when nothing else is queued.
        self.tree = []
        # /v1/navigation requests answered "hold": fulfilled by the test itself.
        self.held = []
        # Statuses for the next event-stream requests; otherwise a stream that ends and asks
        # EventSource to reconnect after 50 ms.
        self.stream_failures = []
        self.quick_streams = False
        self.still_streams = False
        # Only streams whose URL contains this are failed.
        self.failing_scope = ""
        self.navigation = []
        self.context.route("**/*", self.route)
        self.page.on("request", lambda req: self.navigation.append(req.url)
                     if req.is_navigation_request() else None)

    def tearDown(self):
        self.context.close()

    def route(self, route):
        req = route.request
        if not req.url.startswith(URL + "/"):
            route.abort()
        elif "/cdn-cgi/access/logout" in req.url:
            route.fulfill(content_type="text/html", body="<h1>Signed out</h1>")
        elif "/v1/me" in req.url:
            self.probes.append(req.headers.get("authorization"))
            response = self.responses.pop(0) if self.responses else 401
            if response == "network":
                route.abort()
            elif isinstance(response, int):
                route.fulfill(status=response, content_type="text/html", body="Refused")
            else:
                route.fulfill(json={"person": PERSON, "organization": {"id": "org", "name": "Local"},
                                    **response})
        elif "/v1/events/stream" in req.url:
            self.streams.append(req.url)
            if self.stream_failures and self.failing_scope in req.url:
                route.fulfill(status=self.stream_failures.pop(0), content_type="text/html", body="Refused")
            elif self.quick_streams:
                route.fulfill(content_type="text/event-stream", body=": ready\nretry: 50\n\n")
            elif self.still_streams:
                # EventSource's own reconnect runs on the browser's clock, not the page's
                # fake one: a day keeps it out of a test's connection count.
                route.fulfill(content_type="text/event-stream", body=": ready\nretry: 86400000\n\n")
            else:
                route.fulfill(content_type="text/event-stream", body=": ready\n\n")
        elif "/v1/people" in req.url:
            route.fulfill(json={"people": [PERSON], "you": PERSON["id"]})
        elif "/v1/navigation" in req.url:
            self.nav_calls.append(req.headers.get("authorization"))
            response = self.nav_responses.pop(0) if self.nav_responses else None
            if response == "hold":
                self.held.append(route)
            elif response is None:
                route.fulfill(json={"projects": self.tree})
            else:
                route.fulfill(status=response, content_type="application/json",
                              body=json.dumps({"error": {"code": "refused", "message": "Refused"}}))
        elif "/v1/tasks/" in req.url:
            route.fulfill(status=404, content_type="application/json",
                          body=json.dumps({"error": {"code": "not_found", "message": "No such task."}}))
        elif "/v1/pull-requests" in req.url:
            route.fulfill(json={"pullRequests": []})
        elif "/v1/" in req.url:
            route.fulfill(json={})
        else:
            route.continue_()

    def open(self, key=None, suffix="/"):
        if key:
            self.context.add_init_script(f"localStorage.setItem('dude.apiKey', {json.dumps(key)})")
        self.page.goto(URL + suffix)

    def shell(self):
        expect(self.page.get_by_test_id("shell")).to_be_visible()

    def prompt(self):
        expect(self.page.get_by_label("API key", exact=True)).to_be_visible()
        expect(self.page.get_by_test_id("shell")).to_have_count(0)

    def test_keyless_access_and_fixed_logout_without_remount(self):
        self.responses = [{"authMethod": "cloudflare_access", "logoutUrl": "https://untrusted.invalid"}]
        with self.page.expect_request("**/v1/events/stream*") as stream:
            self.open()
            self.shell()
        self.assertEqual(self.probes, [None])
        self.assertNotIn("key=", stream.value.url)
        self.page.get_by_test_id("sign-out").click()
        expect(self.page.get_by_role("heading", name="Signed out")).to_be_visible()
        self.assertEqual(self.page.url, URL + "/cdn-cgi/access/logout")
        self.assertEqual(self.probes, [None])
        self.assertIsNone(self.page.evaluate("localStorage.getItem('dude.apiKey')"))

    def test_valid_stored_key_and_manual_signout(self):
        self.responses = [{"authMethod": "api_key"}]
        self.open("valid")
        self.shell()
        self.assertEqual(self.probes, ["Bearer valid"])
        self.page.get_by_test_id("sign-out").click()
        self.prompt()
        self.assertEqual(self.probes, ["Bearer valid"])
        self.assertIsNone(self.page.evaluate("localStorage.getItem('dude.apiKey')"))
        self.assertEqual(len(self.navigation), 1)

    def test_refused_stored_key_falls_back_to_access_once(self):
        for status in (401, 403):
            with self.subTest(status=status):
                self.responses = [status, {"authMethod": "cloudflare_access"}]
                self.probes.clear()
                self.open("bad")
                self.shell()
                self.assertEqual(self.probes, ["Bearer bad", None])
                self.assertIsNone(self.page.evaluate("localStorage.getItem('dude.apiKey')"))

    def test_app_refusal_of_verified_key_falls_back_to_access_once(self):
        for status in (401, 403):
            with self.subTest(status=status):
                self.responses = [{"authMethod": "api_key"}, {"authMethod": "cloudflare_access"}]
                self.nav_responses = [status]
                self.probes.clear()
                self.nav_calls.clear()
                # The app's tree read by cookie happens only after the refusal and re-probe.
                with self.page.expect_request(lambda r: "/v1/navigation" in r.url
                                              and r.headers.get("authorization") is None):
                    self.open("valid")
                self.shell()
                self.assertEqual(self.probes, ["Bearer valid", None])
                self.assertEqual(self.nav_calls[0], "Bearer valid")
                self.assertIsNone(self.page.evaluate("localStorage.getItem('dude.apiKey')"))

    def test_app_refusal_then_refused_cookie_shows_stable_prompt(self):
        self.responses = [{"authMethod": "api_key"}, 401]
        # Dev StrictMode runs the app's first load twice; refuse both.
        self.nav_responses = [401, 401]
        self.open("valid")
        self.prompt()
        self.page.wait_for_timeout(500)
        self.prompt()
        self.assertEqual(self.probes, ["Bearer valid", None])
        self.assertTrue(self.nav_calls)
        self.assertEqual(set(self.nav_calls), {"Bearer valid"})
        self.assertIsNone(self.page.evaluate("localStorage.getItem('dude.apiKey')"))

    def settle(self):
        # Two frames: long enough for a fulfilled fetch's handler to have re-rendered.
        self.page.evaluate("new Promise(r => requestAnimationFrame(() => requestAnimationFrame(r)))")

    def test_stale_refusal_after_cookie_recovery_keeps_access_session(self):
        self.responses = [{"authMethod": "api_key"}, {"authMethod": "cloudflare_access"}]
        # Dev StrictMode issues the first tree read twice with the key: refuse one, hold the other.
        self.nav_responses = [401, "hold"]
        with self.page.expect_request(lambda r: "/v1/navigation" in r.url
                                      and r.headers.get("authorization") is None):
            self.open("valid")
        self.shell()
        self.assertEqual(self.probes, ["Bearer valid", None])
        self.assertEqual(len(self.held), 1)
        with self.page.expect_response(lambda r: "/v1/navigation" in r.url and r.status == 401):
            self.held.pop().fulfill(status=401, content_type="application/json",
                                    body=json.dumps({"error": {"code": "refused", "message": "Refused"}}))
        self.settle()
        self.shell()
        expect(self.page.get_by_label("API key", exact=True)).to_have_count(0)
        self.assertEqual(self.probes, ["Bearer valid", None])

    def refuse_next_stream(self, status, probes):
        """Signs in by Access, then answers the next stream reconnect with `status`
        and the probes that follow it with `probes`."""
        self.responses = [{"authMethod": "cloudflare_access"}]
        self.quick_streams = True
        self.open()
        self.shell()
        self.responses = list(probes)
        self.stream_failures = [status]
        # The refused reconnect makes the app check its session.
        self.wait_until(lambda: len(self.probes) >= 2)

    def test_refused_stream_reconnect_recovers_access_session(self):
        for status in (401, 403):
            with self.subTest(status=status):
                self.probes.clear()
                self.streams.clear()
                self.refuse_next_stream(status, [401, {"authMethod": "cloudflare_access"}])
                # Reprobed by cookie; the session is kept and its stream reopens and goes live.
                self.wait_until(lambda: len(self.probes) == 3)
                self.shell()
                # The harness ends every stream at once, so a live EventSource keeps coming back.
                refused_at = len(self.streams)
                self.wait_until(lambda: len(self.streams) > refused_at + 1)
                expect(self.page.get_by_label("API key", exact=True)).to_have_count(0)
                self.assertEqual(self.probes, [None, None, None])

    def test_refused_stream_reconnect_with_refused_cookie_shows_prompt(self):
        self.refuse_next_stream(401, [401, 401])
        self.prompt()
        self.assertEqual(self.probes, [None, None, None])
        streams = len(self.streams)
        self.page.wait_for_timeout(300)
        self.assertEqual(len(self.streams), streams)

    def test_failed_stream_reconnect_with_valid_session_reopens(self):
        self.refuse_next_stream(503, [{"authMethod": "cloudflare_access"}])
        refused_at = len(self.streams)
        # Not an auth refusal: the stream is reopened after a delay, in the same session.
        self.wait_until(lambda: len(self.streams) > refused_at + 1, timeout=5)
        self.shell()
        self.assertEqual(self.probes, [None, None])

    def wait_until(self, condition, timeout=5):
        for _ in range(int(timeout * 20)):
            if condition():
                return
            self.page.wait_for_timeout(50)
        self.fail("condition not reached")

    def clocked_shell(self, suffix="/"):
        """Signs in by key on a paused page clock, with streams that open and then
        leave reconnecting to the browser for a day."""
        self.page.clock.install()
        self.responses = [{"authMethod": "api_key"}]
        self.still_streams = True
        self.open("valid", suffix)
        self.shell()
        self.page.clock.pause_at(self.page.evaluate("Date.now()") + 1_000)
        self.quiet()

    def quiet(self):
        # Real time, not the page's: the fake clock also holds requestAnimationFrame.
        self.page.wait_for_timeout(150)

    def scoped(self):
        return [url for url in self.streams if self.failing_scope in url]

    def fail_streams(self, statuses, me):
        """Answers the next (re)connects in the failing scope with `statuses` and the
        session checks they cause with `me`, then takes the network down and back so
        the scope's stream reconnects into the first of them."""
        self.stream_failures = list(statuses)
        self.responses = list(me)
        streams, probes = len(self.scoped()), len(self.probes)
        self.page.evaluate("dispatchEvent(new Event('offline')); dispatchEvent(new Event('online'))")
        self.wait_until(lambda: len(self.scoped()) == streams + 1 and len(self.probes) == probes + 1)
        self.quiet()

    def next_connection_after(self, delay_ms):
        """No connection in the failing scope 1 ms before `delay_ms` on the page clock,
        exactly one at it; if that one fails, waits for the session check it causes."""
        streams, probes = len(self.scoped()), len(self.probes)
        fails = bool(self.stream_failures)
        self.page.clock.run_for(delay_ms - 1)
        self.page.wait_for_timeout(150)
        self.assertEqual(len(self.scoped()), streams, f"reconnected before {delay_ms} ms")
        self.page.clock.run_for(1)
        self.wait_until(lambda: len(self.scoped()) == streams + 1)
        if fails:
            self.wait_until(lambda: len(self.probes) == probes + 1)
        self.quiet()
        self.assertEqual(len(self.scoped()), streams + 1)

    def test_failed_stream_retries_back_off_to_a_cap(self):
        self.clocked_shell()
        ok = {"authMethod": "api_key"}
        # Seven terminal failures in a row with the session valid throughout; the
        # eighth connection opens.
        self.fail_streams([503] * 7, [ok] * 7)
        for delay in (1_000, 2_000, 4_000, 8_000, 16_000, 30_000, 30_000):
            self.next_connection_after(delay)
        self.assertEqual(self.stream_failures, [])
        self.shell()
        self.assertEqual(self.probes, ["Bearer valid"] * 8)

    def test_an_open_stream_resets_the_retry_delay(self):
        self.clocked_shell()
        ok = {"authMethod": "api_key"}
        self.fail_streams([503, 503], [ok, ok])
        self.next_connection_after(1_000)
        self.next_connection_after(2_000)  # opens
        self.fail_streams([503], [ok])
        self.next_connection_after(1_000)

    def test_session_check_failure_reopens_without_clearing_the_key(self):
        for failure in (503, "network"):
            with self.subTest(failure=failure):
                self.clocked_shell()
                self.fail_streams([503], [failure])
                self.next_connection_after(1_000)
                self.shell()
                self.assertEqual(self.page.evaluate("localStorage.getItem('dude.apiKey')"), "valid")
                self.assertEqual(self.probes, ["Bearer valid", "Bearer valid"])
                self.tearDown()
                self.setUp()

    def test_a_pending_retry_dies_with_its_stream(self):
        ok = {"authMethod": "api_key"}
        # "reopen": the network comes back while the retry is pending, so the same
        # mounted stream reopens at once and the pending retry must not add another.
        for leave in ("unmount", "sign-out", "reopen"):
            with self.subTest(leave=leave):
                # A task's screen has a stream of its own; leaving the screen unmounts it.
                self.failing_scope = "taskId=" if leave == "unmount" else ""
                self.tree = [{"id": "prj_retry", "name": "Retry", "epics": [], "tasks": [
                    {"id": "tsk_retry", "key": "R-1", "title": "Retry", "status": "running"}]}] \
                    if leave == "unmount" else []
                self.clocked_shell("/#/task/tsk_retry" if leave == "unmount" else "/")
                self.wait_until(lambda: len(self.scoped()) >= 1)
                self.fail_streams([503], [ok])
                if leave == "unmount":
                    self.page.evaluate("location.hash = '#/waiting'")
                    expect(self.page.get_by_test_id("shell")).to_be_visible()
                elif leave == "sign-out":
                    self.page.get_by_test_id("sign-out").click()
                    self.prompt()
                else:
                    streams = len(self.streams)
                    self.page.evaluate("dispatchEvent(new Event('online'))")
                    self.wait_until(lambda: len(self.streams) == streams + 1)
                self.quiet()
                streams = len(self.streams)
                self.page.clock.run_for(30_000)
                self.page.wait_for_timeout(300)
                self.assertEqual(len(self.streams), streams)
                self.tearDown()
                self.setUp()

    def test_keyless_refusal_shows_prompt(self):
        self.responses = [401]
        self.open()
        self.prompt()
        self.assertEqual(self.probes, [None])

    def test_transient_failure_preserves_key_and_retry(self):
        for failure in (503, "network"):
            with self.subTest(failure=failure):
                self.responses = [failure, {"authMethod": "api_key"}]
                self.probes.clear()
                self.open("valid")
                expect(self.page.get_by_role("button", name="Retry", exact=True)).to_be_visible()
                expect(self.page.get_by_test_id("shell")).to_have_count(0)
                self.assertEqual(self.page.evaluate("localStorage.getItem('dude.apiKey')"), "valid")
                self.page.get_by_role("button", name="Retry", exact=True).click()
                self.shell()
                self.assertEqual(self.probes, ["Bearer valid", "Bearer valid"])

    def test_verified_manual_submission_replaces_document_without_query(self):
        self.responses = [401, {"authMethod": "api_key"}, {"authMethod": "api_key"}]
        self.open(suffix="/?sensitive=value#/settings/me")
        self.prompt()
        self.page.evaluate("window.loginDocumentMarker = 'original'")
        self.page.get_by_label("API key", exact=True).fill("  valid  ")
        self.page.get_by_role("button", name="Continue", exact=True).click()
        self.shell()
        self.assertEqual(self.page.url, URL + "/#/settings/me")
        self.assertIsNone(self.page.evaluate("window.loginDocumentMarker"))
        self.assertEqual(self.page.evaluate("localStorage.getItem('dude.apiKey')"), "valid")
        self.assertEqual(self.navigation, [URL + "/?sensitive=value", URL + "/"])
        self.assertEqual(self.probes, [None, "Bearer valid", "Bearer valid"])

    def test_manual_refusal_and_network_failure_remain_retryable(self):
        for failure in (403, 503, "network"):
            with self.subTest(failure=failure):
                self.responses = [401, failure]
                self.probes.clear()
                self.open()
                self.prompt()
                self.page.evaluate("window.loginDocumentMarker = 'original'")
                count = len(self.navigation)
                self.page.get_by_label("API key", exact=True).fill("candidate")
                self.page.get_by_role("button", name="Continue", exact=True).click()
                message = "That key was not accepted" if failure == 403 else "Could not check that key"
                expect(self.page.get_by_text(message, exact=False)).to_be_visible()
                expect(self.page.get_by_role("button", name="Continue", exact=True)).to_be_enabled()
                self.assertIsNone(self.page.evaluate("localStorage.getItem('dude.apiKey')"))
                self.assertEqual(self.page.evaluate("window.loginDocumentMarker"), "original")
                self.assertEqual(len(self.navigation), count)

    def test_fixtures_bypass_auth_probe(self):
        self.open(suffix="/?fixtures=a")
        self.shell()
        self.assertEqual(self.probes, [])


if __name__ == "__main__":
    unittest.main(verbosity=2)
