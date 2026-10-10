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
from pathlib import Path

import pytest
from playwright.sync_api import Page, expect

from helpers import ApiClient, query, sign_in, wait_until

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


def _shoot(page: Page, name: str) -> None:
    if SHOTS is None:
        return
    SHOTS.mkdir(parents=True, exist_ok=True)
    for theme in ("light", "dark"):
        page.evaluate("t => document.documentElement.setAttribute('data-theme', t)", theme)
        page.wait_for_timeout(200)
        page.screenshot(path=str(SHOTS / f"{name}-{theme}.png"))
    page.evaluate("() => document.documentElement.setAttribute('data-theme', 'light')")


def _asking(client: ApiClient, project: dict, title: str, ask: dict) -> tuple[dict, str]:
    """A task whose conductor asks `ask` (Talk it through, then the scripted
    conductor calls ask_person as the message says), and its conductor Run."""
    models = {**project["agentModels"], **client.on_models({"conductor": "fake/scripted"})}
    assert client.patch(f"/v1/projects/{project['id']}", {"agentModels": models}).status_code == 200
    assert client.patch(f"/v1/projects/{project['id']}/settings", {"delivery": {"conductorWarmMinutes": 30}}).status_code == 200
    task = client.create_task(project["id"], title)
    sent = client.post(f"/v1/tasks/{task['id']}/chat", {"text": f"Plan it.\ntool: ask_person {json.dumps(ask)}"})
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
    sign_in(page, web_url, org["api_key"], at=f"#/task/{task['id']}")
    turn = page.get_by_test_id("question-turn").last
    expect(turn.get_by_role("tablist", name="Questions")).to_be_visible(timeout=30_000)
    expect(page.get_by_test_id("composer-waiting")).to_contain_text("The conductor is waiting for your answer above.")
    tabs = turn.get_by_role("tab")
    expect(tabs).to_have_text(["Retry scope", "Old route", "Tests", "Button", "Send · 0/4"])
    expect(turn).to_contain_text("agent suggests")
    _shoot(page, "4-many")

    # A pick does not send; Next moves on.
    turn.get_by_role("radio", name="5xx and network errors only").click()
    turn.get_by_role("button", name="Next").click()
    expect(tabs.nth(1)).to_have_attribute("aria-selected", "true")
    # Keys: 2 picks the second, Enter is Next.
    page.keyboard.press("2")
    expect(turn.get_by_role("radio", name="Leave it; file a task")).to_have_attribute("aria-checked", "true")
    page.keyboard.press("Enter")
    expect(tabs.nth(2)).to_have_attribute("aria-selected", "true")
    turn.get_by_role("checkbox", name="Unit (PaymentSplitter)").click()
    turn.get_by_role("checkbox", name="API contract").click()
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
    expect(turn.get_by_role("checkbox", name="API contract")).to_have_attribute("aria-checked", "true")
    assert client.get(f"/v1/questions?runId={run}").json()["questions"][0]["status"] == "open"

    # The review, one missing: Send is off.
    tabs.nth(4).click()
    expect(turn.get_by_test_id("review-answer")).to_have_text(
        ["5xx and network errors only", "Leave it; file a task", "Unit (PaymentSplitter); API contract", "Not answered yet"])
    expect(turn.get_by_role("button", name="Send answers")).to_be_disabled()
    _shoot(page, "5-review")

    # Their own words for the last, then the note, and Send.
    turn.get_by_role("button", name="Answer: Button").click()
    turn.get_by_role("radio", name="Something else…").click()
    field = turn.get_by_label("Something else, in your own words")
    expect(field).to_be_focused()
    field.fill("Pay with two cards")
    field.press("Enter")
    expect(tabs.nth(4)).to_have_text("Send · 4/4")
    turn.get_by_label("A note for the agent (optional)").fill("Keep the retry budget under 10s total — checkout times out at 15.")
    turn.get_by_role("button", name="Send answers").click()

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
    sign_in(page, web_url, org["api_key"], at=f"#/task/{task['id']}")
    turn = page.get_by_test_id("question-turn").last
    expect(turn.get_by_role("radio", name="Retry 5xx and network only")).to_be_visible(timeout=30_000)
    expect(turn.get_by_role("tablist")).to_have_count(0)
    _shoot(page, "1-one")
    # Something else… opens a field in place; leave it and pick a choice instead.
    turn.get_by_role("radio", name="Something else…").click()
    field = turn.get_by_label("Something else, in your own words")
    expect(field).to_be_focused()
    field.fill("Retry 5xx; for 409 re-fetch the cart and retry once")
    _shoot(page, "2-one-own")
    field.fill("")
    turn.get_by_role("radio", name="Retry 5xx and network only").click()
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
def test_write_to_the_agent_instead_leaves_the_question_open(
    page: Page, web_url: str, client: ApiClient, org: dict, forge_project: dict, console_errors: list
):
    task, run = _asking(client, forge_project, "Ask, then a word aside", ONE)
    page.set_viewport_size({"width": 1440, "height": 1000})
    sign_in(page, web_url, org["api_key"], at=f"#/task/{task['id']}")
    page.get_by_test_id("write-instead").click()
    composer = page.get_by_placeholder("Ask about this task…")
    expect(composer).to_be_focused()
    composer.fill("Which file has the retry?")
    composer.press("Enter")
    wait_until(lambda: any(e["eventType"] == "chat.message" and e["payload"].get("text") == "Which file has the retry?"
                           for e in client.events(runId=run, limit=1000)), timeout=30, message="the message never went")
    assert client.get(f"/v1/questions?runId={run}").json()["questions"][0]["status"] == "open"
    assert console_errors == []
