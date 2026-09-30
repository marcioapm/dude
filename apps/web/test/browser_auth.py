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
            route.fulfill(content_type="text/event-stream", body=": ready\n\n")
        elif "/v1/people" in req.url:
            route.fulfill(json={"people": [PERSON], "you": PERSON["id"]})
        elif "/v1/navigation" in req.url:
            route.fulfill(json={"projects": []})
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
