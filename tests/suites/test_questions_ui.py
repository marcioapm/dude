"""Several questions in one ask, end to end through the deployed processes
and the built web app, in system Chrome.

An agent asks four questions at once (ask_person questions); the person
answers them in the agent's own turn — tabs, choices, their own words, the
review, a note — and the Run is told every answer in one message. A single
question answered with one click on a choice still works the same way, and
its answer is told exactly as before.

Screenshots of each screen the mockup has, light and dark, go to
$DUDE_TEST_SHOTS when it is set; none are taken otherwise.
"""

from __future__ import annotations

import json
import os
import re
import threading
import ssl
import subprocess
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path

import pytest
from playwright.sync_api import Page, expect

from helpers import ApiClient, create_api_key, create_organization, query, relay_writer_question, sign_in, wait_until, webhook_secret
from env import TestEnvironment as Environment
from fake_github import FakeGitHub
from suites.test_delivery import (_stopped_epoch, _timed_resume, _resume_row_in_order,
                                 _unparked_and_timed_name_the_placement_after)

SHOTS = Path(os.environ["DUDE_TEST_SHOTS"]) if os.environ.get("DUDE_TEST_SHOTS") else None

FOUR = {"questions": [
    {"header": "Retry scope", "question": "Which failures should the payment call retry?", "choices": [
        {"label": "5xx and network errors only", "description": "A 4xx means our request is wrong; retrying it repeats the mistake.",
         "recommended": True},
        {"label": "Everything (current behaviour)", "description": "What PaymentClient does today, 4xx included."},
        {"label": "Nothing — show the error", "description": "No retry; the person sees the failure and can try again."}]},
    {"header": "Old route", "question": "The legacy `/pay` route has its own retry. What should happen to it?", "choices": [
        {"label": "Fold it into this change", "description": "One retry policy for both routes; touches 3 more files."},
        {"label": "Leave it; file a task", "description": "This change stays small; the duplication is tracked.", "recommended": True},
        {"label": "Delete the route", "description": "Nothing has called it in 30 days (access logs)."}]},
    {"header": "Tests", "question": "Which layers should cover the split?", "multiple": True, "choices": [
        {"label": "Unit (PaymentSplitter)", "description": "Amount rounding, the 2-card limit, currency mismatch."},
        {"label": "API contract", "description": "POST /checkout/split against the recorded provider responses."},
        {"label": "Browser e2e on checkout", "description": "Adds ~40s to CI."}]},
    {"header": "Button", "question": "What should the split button say?", "choices": [{"label": "Split payment"}, {"label": "Pay in parts"}]},
]}

ONE = {"question": "Should 4xx responses be retried? The existing code retries everything, but a 4xx usually means our request is wrong.",
       "choices": ["Retry 5xx and network only", "Retry everything (current behaviour)"]}


def _choice(label: str) -> re.Pattern[str]:
    """A choice row by its label. The row's accessible name continues with its description (and "agent suggests"),
    so exact=True cannot match it; anchored at the start, no other row's label can."""
    return re.compile("^" + re.escape(label))


def _shoot(page: Page, name: str) -> None:
    if SHOTS is None:
        return
    SHOTS.mkdir(parents=True, exist_ok=True)
    for theme in ("light", "dark"):
        page.evaluate("t => document.documentElement.setAttribute('data-theme', t)", theme)
        page.wait_for_timeout(200)
        page.screenshot(path=str(SHOTS / f"{name}-{theme}.png"))
    page.evaluate("() => document.documentElement.setAttribute('data-theme', 'light')")


def _asking(client: ApiClient, project: dict, title: str, ask: dict, context: str = "Plan it.") -> tuple[dict, str]:
    """A task whose conductor asks `ask` (Talk it through, then the scripted
    conductor calls ask_person as the message says), and its conductor Run."""
    models = {**project["agentModels"], **client.on_models({"conductor": "fake/scripted"})}
    assert client.patch(f"/v1/projects/{project['id']}", {"agentModels": models}).status_code == 200
    assert client.patch(f"/v1/projects/{project['id']}/settings", {"delivery": {"conductorWarmMinutes": 30}}).status_code == 200
    task = client.create_task(project["id"], title)
    sent = client.post(f"/v1/tasks/{task['id']}/chat", {"text": f"{context}\ntool: ask_person {json.dumps(ask)}"})
    assert sent.status_code in (200, 201), sent.text
    run = sent.json()["runId"]
    wait_until(lambda: client.get(f"/v1/questions?runId={run}").json()["questions"], timeout=60, message="the conductor never asked")
    return task, run


def _directive(owner_dsn: str, run: str) -> str:
    rows = query(owner_dsn, "SELECT text FROM directives WHERE run_id = %s ORDER BY created_at DESC LIMIT 1", (run,))
    return rows[0]["text"] if rows else ""


@pytest.mark.ui
def test_four_questions_are_answered_in_the_agents_turn_and_told_as_one_message(
    page: Page, web_url: str, client: ApiClient, org: dict, forge_project: dict, owner_dsn: str, console_errors: list
):
    task, run = _asking(client, forge_project, "Checkout v2: split payment step", FOUR)
    page.set_viewport_size({"width": 1440, "height": 1000})
    sign_in(page, web_url, org["api_key"], at=f"#/task/{task['id']}/chat")
    turn = page.get_by_test_id("question-turn").last
    expect(turn.get_by_role("tablist", name="Questions", exact=True)).to_be_visible(timeout=30_000)
    expect(page.get_by_test_id("composer-waiting")).to_contain_text("The conductor is waiting for your answer above.")
    tabs = turn.get_by_role("tab")
    expect(tabs).to_have_text(["Retry scope", "Old route", "Tests", "Button", "Send · 0/4"])
    expect(turn).to_contain_text("agent suggests")
    _shoot(page, "4-many")

    # A pick does not send; Next moves on.
    turn.get_by_role("radio", name=_choice("5xx and network errors only")).click()
    turn.get_by_role("button", name="Next", exact=True).click()
    expect(tabs.nth(1)).to_have_attribute("aria-selected", "true")
    # Keys: 2 picks the second, Enter is Next.
    page.keyboard.press("2")
    expect(turn.get_by_role("radio", name=_choice("Leave it; file a task"))).to_have_attribute("aria-checked", "true")
    page.keyboard.press("Enter")
    expect(tabs.nth(2)).to_have_attribute("aria-selected", "true")
    turn.get_by_role("checkbox", name=_choice("Unit (PaymentSplitter)")).click()
    turn.get_by_role("checkbox", name=_choice("API contract")).click()
    _shoot(page, "4-many-midway")
    # A phone: the same turn, the tabs down to their marks and the current one's name.
    page.set_viewport_size({"width": 390, "height": 844})
    turn.scroll_into_view_if_needed()
    page.wait_for_timeout(300)
    _shoot(page, "7-phone")
    page.set_viewport_size({"width": 1440, "height": 1000})
    # A reload keeps the picks: they are this browser's until sent.
    page.reload()
    turn = page.get_by_test_id("question-turn").last
    tabs = turn.get_by_role("tab")
    expect(tabs.nth(2)).to_have_attribute("aria-selected", "true", timeout=30_000)
    expect(turn.get_by_role("checkbox", name=_choice("API contract"))).to_have_attribute("aria-checked", "true")
    assert client.get(f"/v1/questions?runId={run}").json()["questions"][0]["status"] == "open"

    # The review, one missing: Send is off.
    tabs.nth(4).click()
    expect(turn.get_by_test_id("review-answer")).to_have_text(
        ["5xx and network errors only", "Leave it; file a task", "Unit (PaymentSplitter); API contract", "Not answered yet"])
    expect(turn.get_by_role("button", name="Send answers", exact=True)).to_be_disabled()
    _shoot(page, "5-review")

    # Their own words for the last, then the note, and Send.
    turn.get_by_role("button", name="Answer: Button", exact=True).click()
    turn.get_by_role("radio", name="Something else…", exact=True).click()
    field = turn.get_by_label("Something else, in your own words")
    expect(field).to_be_focused()
    field.fill("Pay with two cards")
    field.press("Enter")
    expect(tabs.nth(4)).to_have_text("Send · 4/4")
    turn.get_by_label("A note for the agent (optional)").fill("Keep the retry budget under 10s total — checkout times out at 15.")
    turn.get_by_role("button", name="Send answers", exact=True).click()

    record = page.get_by_test_id("question-record").last
    answers = record.get_by_test_id("record-answer")
    expect(answers).to_have_count(4, timeout=30_000)
    expect(answers.nth(0)).to_have_text("5xx and network errors only")
    expect(answers.nth(1)).to_have_text("Leave it; file a task")
    expect(answers.nth(2)).to_have_text("Unit (PaymentSplitter); API contract")
    expect(answers.nth(3)).to_have_text("“Pay with two cards”in e2e's words")
    expect(page.get_by_test_id("composer-waiting")).to_have_count(0)
    told = wait_until(lambda: _directive(owner_dsn, run).startswith("Answers to your 4 questions") and _directive(owner_dsn, run),
                      timeout=30, message="the conductor was never told the answers")
    assert told.startswith(
        "Answers to your 4 questions:\n\n"
        "1. Retry scope — Which failures should the payment call retry?\n   → 5xx and network errors only\n"
        "2. Old route — The legacy `/pay` route has its own retry. What should happen to it?\n   → Leave it; file a task\n"
        "3. Tests — Which layers should cover the split? (several allowed)\n   → Unit (PaymentSplitter); API contract\n"
        "4. Button — What should the split button say?\n   → in their own words: \"Pay with two cards\"\n\n"
        "Also from "), told
    assert told.endswith(":\nKeep the retry budget under 10s total — checkout times out at 15."), told
    _shoot(page, "6-many-answered")
    assert console_errors == []


@pytest.mark.ui
def test_one_question_is_answered_with_one_click(
    page: Page, web_url: str, client: ApiClient, org: dict, forge_project: dict, owner_dsn: str, console_errors: list
):
    task, run = _asking(client, forge_project, "Retry on 4xx", ONE)
    page.set_viewport_size({"width": 1440, "height": 1000})
    sign_in(page, web_url, org["api_key"], at=f"#/task/{task['id']}/chat")
    turn = page.get_by_test_id("question-turn").last
    expect(turn.get_by_role("radio", name="Retry 5xx and network only", exact=True)).to_be_visible(timeout=30_000)
    expect(turn.get_by_role("tablist")).to_have_count(0)
    _shoot(page, "1-one")
    # Something else… opens a field in place; leave it and pick a choice instead.
    turn.get_by_role("radio", name="Something else…", exact=True).click()
    field = turn.get_by_label("Something else, in your own words")
    expect(field).to_be_focused()
    field.fill("Retry 5xx; for 409 re-fetch the cart and retry once")
    _shoot(page, "2-one-own")
    field.fill("")
    turn.get_by_role("radio", name="Retry 5xx and network only", exact=True).click()
    expect(page.get_by_test_id("question-record").last.get_by_test_id("record-answer")).to_have_text(["Retry 5xx and network only"],
                                                                                                    timeout=30_000)
    told = wait_until(lambda: _directive(owner_dsn, run).startswith("Answer to your question") and _directive(owner_dsn, run),
                      timeout=30, message="the conductor was never told the answer")
    # One question is told exactly as it always was.
    assert told == f"Answer to your question {json.dumps(ONE['question'])}:\n\nRetry 5xx and network only", told
    assert query(owner_dsn, "SELECT answer FROM questions WHERE run_id = %s", (run,)) == [{"answer": "Retry 5xx and network only"}]
    _shoot(page, "3-one-answered")
    assert console_errors == []


@pytest.mark.ui
@pytest.mark.parametrize("gesture", ["wheel", "keyboard"])
def test_open_question_is_visible_and_new_content_preserves_upward_reading(
    page: Page, web_url: str, client: ApiClient, org: dict, forge_project: dict, gesture: str
):
    context = "Plan the retry change.\n\n" + "\n\n".join(
        f"Context {i}: preserve the payment retry budget and explain the provider failures." for i in range(24)
    )
    task, run = _asking(client, forge_project, "Read context above the open question", ONE, context)
    page.set_viewport_size({"width": 1280, "height": 720})
    sign_in(page, web_url, org["api_key"], at=f"#/task/{task['id']}/chat")
    turn = page.get_by_test_id("question-turn").last
    expect(turn.get_by_text(ONE["question"], exact=True)).to_be_in_viewport(timeout=30_000)
    for label in [*ONE["choices"], "Something else…"]:
        expect(turn.get_by_role("radio", name=label, exact=True)).to_be_in_viewport()
    viewport = page.get_by_role("log")
    initial = viewport.evaluate("el => el.scrollTop")
    assert initial > 100, "fixture needs earlier context above the question"
    if gesture == "wheel":
        viewport.hover()
        page.mouse.wheel(0, -200)
    else:
        viewport.focus()
        page.keyboard.press("PageUp")
    wait_until(lambda: viewport.evaluate("el => el.scrollTop") < initial - 50,
               timeout=5, message=f"{gesture} did not scroll the viewport upward")
    # Wait for browser scrolling to settle, not for new content to arrive.
    page.wait_for_function("""() => {
        const el = document.querySelector('[role=log]');
        const now = performance.now();
        if (window.readingTop !== el.scrollTop) {
            window.readingTop = el.scrollTop;
            window.readingSince = now;
        }
        return now - window.readingSince >= 100;
    }""")
    reading = viewport.evaluate("el => el.scrollTop")
    text = "New context while the question is still open."
    response = client.post(f"/v1/tasks/{task['id']}/chat", {"text": text, "aside": True})
    assert response.status_code in (200, 201), response.text
    expect(page.get_by_test_id("human-turn").filter(has_text=text)).to_have_count(1, timeout=30_000)
    page.evaluate("() => new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve)))")
    assert client.get(f"/v1/questions?runId={run}").json()["questions"][0]["status"] == "open"
    assert viewport.evaluate("el => el.scrollTop") == pytest.approx(reading, abs=2)


@pytest.mark.ui
def test_write_to_the_agent_instead_leaves_the_question_open(
    page: Page, web_url: str, client: ApiClient, org: dict, forge_project: dict, console_errors: list
):
    task, run = _asking(client, forge_project, "Ask, then a word aside", ONE)
    page.set_viewport_size({"width": 1440, "height": 1000})
    sign_in(page, web_url, org["api_key"], at=f"#/task/{task['id']}/chat")
    page.get_by_test_id("write-instead").click()
    composer = page.get_by_placeholder("Ask about this task…")
    expect(composer).to_be_focused()
    composer.fill("Which file has the retry?")
    composer.press("Enter")
    wait_until(lambda: any(e["eventType"] == "chat.message" and e["payload"].get("text") == "Which file has the retry?"
                           for e in client.events(runId=run, limit=1000)), timeout=30, message="the message never went")
    assert client.get(f"/v1/questions?runId={run}").json()["questions"][0]["status"] == "open"
    assert console_errors == []


@pytest.fixture
def deadline_stack(tmp_path):
    """Only this isolated process opts into a 30s wait. Real TLS push delivery
    goes to a local stand-in provider, with valid browser-generated keys."""
    cert, key = tmp_path / "push.crt", tmp_path / "push.key"
    subprocess.run(["openssl", "req", "-x509", "-newkey", "rsa:2048", "-nodes",
                    "-keyout", str(key), "-out", str(cert), "-days", "1",
                    "-subj", "/CN=localhost", "-addext", "subjectAltName=DNS:localhost"],
                   check=True, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    pushes = []

    class PushService(BaseHTTPRequestHandler):
        def do_POST(self):
            pushes.append({"body": self.rfile.read(int(self.headers["content-length"])),
                           "authorization": self.headers.get("authorization")})
            self.send_response(201)
            self.end_headers()

        def log_message(self, *_args):
            pass

    server = ThreadingHTTPServer(("127.0.0.1", 0), PushService)
    tls = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
    tls.load_cert_chain(cert, key)
    server.socket = tls.wrap_socket(server.socket, server_side=True)
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()
    # run_tests.py exports its log directory for fixture reconstruction. This
    # second live stack must not share fake-lux/slack address files with it.
    with pytest.MonkeyPatch.context() as patch:
        patch.setenv("DUDE_TEST_LOG_DIR", str(tmp_path / "deadline-logs"))
        stack = Environment(orchestrator_env={"DUDE_TEST_CONDUCTOR_ANSWER_WAIT": "30s", "SSL_CERT_FILE": str(cert)})
    forge = FakeGitHub(stack.git_root, owner="deadline")
    try:
        stack.setup()
        assert stack.wait_healthy(timeout=30)
        forge.start()
        org_id = create_organization(stack.owner_dsn, "deadline-owner")
        api_key = create_api_key(stack.owner_dsn, org_id)
        api = ApiClient(stack.control_plane_url, api_key)
        credential = api.post("/v1/forge/credential", {"auth": "pat", "secret": "fake-token", "apiBaseUrl": forge.api_url})
        assert credential.status_code == 200, credential.text
        forge.webhook_url = stack.control_plane_url + credential.json()["webhookPath"]
        forge.webhook_secret = webhook_secret(stack.owner_dsn, org_id)
        project = api.create_project(name="Deadline", slug="deadline", agentModels=api.on_models({
            "conductor": "fake/scripted", "implementer": "fake/ask",
            "reviewer": "fake/scripted", "simplifier": "fake/scripted"}),
            deliveryPolicy={"requiredReviewers": ["correctness"], "test": False},
            repositories=[{"name": "greeter", "url": forge.clone_url, "defaultBranch": "main"}])
        yield stack, api, api_key, project, f"https://localhost:{server.server_port}/push", pushes
    finally:
        forge.stop()
        stack.teardown()
        server.shutdown()
        server.server_close()
        thread.join(timeout=5)


def _writer_asks(client: ApiClient, project: dict, owner_dsn: str) -> tuple[dict, dict, dict]:
    # Explicit conductor tool call, no defaults-taking/auto-authorizing model.
    models = {**project["agentModels"], **client.on_models({"conductor": "fake/scripted", "implementer": "fake/ask"})}
    assert client.patch(f"/v1/projects/{project['id']}", {"agentModels": models}).status_code == 200
    task = client.create_task(project["id"], "Writer asks before editing")
    started = client.post(f"/v1/tasks/{task['id']}/chat", {"text": 'tool: start_phase {"phase":"implement"}'})
    assert started.status_code in (200, 201), started.text
    routed = wait_until(lambda: next((e for e in client.events(taskId=task["id"], limit=1000)
                                      if e["eventType"] == "question.routed"), None), timeout=60)
    writer = client.get_run(routed["runId"])
    assert writer["phase"] == "implement" and writer["attempt"] == 1, writer
    assert query(owner_dsn, "SELECT id, run_id, addressee, status FROM questions WHERE id = %s",
                 (routed["payload"]["questionId"],)) == [{"id": routed["payload"]["questionId"],
                 "run_id": writer["id"], "addressee": "conductor", "status": "open"}]
    assert client.get(f"/v1/tasks/{task['id']}").json()["status"] != "awaiting_input"
    return task, writer, routed


def _held_in_browser(page: Page, web_url: str, api_key: str, task: dict) -> None:
    sign_in(page, web_url, api_key, at=f"#/task/{task['id']}")
    nav = page.locator(f'[data-nav-key="task:{task["id"]}"]')
    expect(nav).to_be_visible(timeout=30_000)
    expect(nav).not_to_have_attribute("data-triage", "needs_you")
    page.goto(f"{web_url}#/waiting")
    expect(page.get_by_test_id("inbox").get_by_role("listitem")).to_have_count(0)


def _assert_human_resume(client: ApiClient, env, owner_dsn: str, task: dict, writer: dict,
                         question_id: str, stopped_epoch: int, *, linked: bool = False) -> None:
    timed = _timed_resume(client, writer["id"], "answer")
    _unparked_and_timed_name_the_placement_after(client, env, owner_dsn, writer["id"], timed, stopped_epoch)
    _resume_row_in_order(owner_dsn, writer["id"], timed)
    events = client.events(runId=writer["id"], limit=1000)
    answered = [e for e in events if e["eventType"] == "question.answered" and e["payload"]["questionId"] == question_id]
    assert len(answered) == 1, answered
    if linked:
        assert answered[0]["payload"]["by"] == "person", answered
    attribution = query(owner_dsn, "SELECT answered_by_person, answered_by_run FROM questions WHERE id = %s", (question_id,))[0]
    assert attribution["answered_by_person"] and attribution["answered_by_run"] is None, attribution
    assert query(owner_dsn, "SELECT actor_type FROM events WHERE cursor = %s", (answered[0]["cursor"],))[0]["actor_type"] != "agent"
    delivered = [e for e in events if e["eventType"] == "run.directive.delivered"
                 and e["payload"].get("directiveId") == answered[0]["payload"]["directiveId"]]
    assert len(delivered) == 1, delivered
    directives = query(owner_dsn, "SELECT text FROM directives WHERE run_id = %s", (writer["id"],))
    assert len(directives) == 1, directives
    if linked:
        assert "It is their decision" in directives[0]["text"], directives
    current = client.get_run(writer["id"])
    assert current["phase"] == writer["phase"] and current["attempt"] == writer["attempt"]
    assert current["taskId"] == task["id"] and timed["runId"] == writer["id"]
    assert len([e for e in events if e["eventType"] == "run.unparked"]) == 1


@pytest.mark.ui
def test_linked_writer_relay_has_one_owner_card_and_one_human_resume(
    page: Page, web_url: str, client: ApiClient, org: dict, forge_project: dict, owner_dsn: str, env, console_errors: list
):
    task, run, routed = _writer_asks(client, forge_project, owner_dsn)
    _held_in_browser(page, web_url, org["api_key"], task)
    wait_until(lambda: client.get_run(run["id"])["status"] == "paused", timeout=30)
    stopped = _stopped_epoch(env, owner_dsn, run["id"])
    writer, human = relay_writer_question(client, task["id"], run["id"], explicit_relay=True)
    assert writer["id"] == routed["payload"]["questionId"]
    assert query(owner_dsn, "SELECT relays FROM questions WHERE id = %s", (human["id"],)) == [{"relays": writer["id"]}]
    page.reload()
    rows = page.get_by_test_id("inbox").get_by_role("listitem")
    expect(rows).to_have_count(1, timeout=30_000)
    expect(rows).to_contain_text("Should FACTORY.md be in English?")
    expect(page.locator(f'[data-nav-key="task:{task["id"]}"]')).to_have_attribute("data-triage", "needs_you")
    rows.get_by_role("button", name="Answer", exact=True).click()
    expect(page.get_by_test_id("question-turn")).to_have_count(1)
    page.get_by_test_id("question-turn").get_by_role("radio", name="yes", exact=True).click()
    expect(page.get_by_test_id("question-record").last).to_contain_text("yes", timeout=30_000)
    assert query(owner_dsn, "SELECT status, answer, answered_by_run FROM questions WHERE id IN (%s, %s) ORDER BY id",
                 (writer["id"], human["id"])) == [
                     {"status": "answered", "answer": "yes", "answered_by_run": None},
                     {"status": "answered", "answer": "yes", "answered_by_run": None}]
    _assert_human_resume(client, env, owner_dsn, task, run, writer["id"], stopped, linked=True)
    asked = [e for e in client.events(taskId=task["id"], limit=1000) if e["eventType"] == "question.asked"]
    assert len(asked) == 1 and asked[0]["payload"]["questionId"] == human["id"]
    page.goto(f"{web_url}#/waiting")
    expect(page.get_by_test_id("inbox").get_by_role("listitem")).to_have_count(0)
    assert console_errors == []


@pytest.mark.ui
def test_writer_deadline_hands_original_question_to_owner_and_pushes_once(page: Page, deadline_stack, console_errors: list):
    stack, client, api_key, project, endpoint, pushes = deadline_stack
    web_url = stack.start_web()
    sign_in(page, web_url, api_key)
    keys = page.evaluate("""async () => {
        const pair = await crypto.subtle.generateKey({name:'ECDH', namedCurve:'P-256'}, true, ['deriveBits']);
        const publicKey = new Uint8Array(await crypto.subtle.exportKey('raw', pair.publicKey));
        const enc = a => btoa(String.fromCharCode(...a)).replaceAll('+','-').replaceAll('/','_').replaceAll('=','');
        return {p256dh: enc(publicKey), auth: enc(crypto.getRandomValues(new Uint8Array(16)))};
    }""")
    registered = client.post("/v1/push/subscriptions", {"endpoint": endpoint, "keys": keys})
    assert registered.status_code == 201, registered.text
    task, run, routed = _writer_asks(client, project, stack.owner_dsn)
    qid = routed["payload"]["questionId"]
    _held_in_browser(page, web_url, api_key, task)
    assert pushes == []
    wait_until(lambda: client.get_run(run["id"])["status"] == "paused", timeout=20)
    stopped = _stopped_epoch(stack, stack.owner_dsn, run["id"])
    wait_until(lambda: query(stack.owner_dsn, "SELECT id FROM questions WHERE id = %s AND addressee = 'person'", (qid,)),
               timeout=45, message="runtime sweep never handed the original question to the person")
    expect(page.locator(f'[data-nav-key="task:{task["id"]}"]')).to_have_attribute("data-triage", "needs_you", timeout=30_000)
    rows = page.get_by_test_id("inbox").get_by_role("listitem")
    expect(rows).to_have_count(1, timeout=30_000)
    expect(rows).to_contain_text("Should FACTORY.md be in English?")
    wait_until(lambda: pushes, timeout=15, message="no actual HTTP push arrived")
    assert len(pushes) == 1 and pushes[0]["body"] and pushes[0]["authorization"].startswith("vapid ")
    handed = [e for e in client.events(taskId=task["id"], limit=1000) if e["eventType"] == "question.asked"]
    assert len(handed) == 1 and handed[0]["runId"] == run["id"]
    assert handed[0]["payload"]["questionId"] == qid and handed[0]["payload"]["handedOverFrom"] == "conductor"
    assert query(stack.owner_dsn, "SELECT id, run_id FROM questions WHERE task_id = %s", (task["id"],)) == [{"id": qid, "run_id": run["id"]}]
    rows.get_by_role("button", name="Answer", exact=True).click()
    page.get_by_test_id("question-turn").get_by_role("radio", name="yes", exact=True).click()
    _assert_human_resume(client, stack, stack.owner_dsn, task, run, qid, stopped)
    # Later sweeps occur during the resume: no duplicate handover or push.
    assert len(pushes) == 1
    assert len([e for e in client.events(taskId=task["id"], limit=1000) if e["eventType"] == "question.asked"]) == 1
    assert console_errors == []
