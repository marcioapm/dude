"""dude#404 on the task page, end to end through the deployed processes and
the built web app: "Needs you" follows the questions as the server holds
them, and a conductor lost while it waited on the person is recoverable.

(a) lux loses the conductor's Run while its question waits on the person:
the question moves to the Run that replaces it and stays open, so the
Overview keeps "Needs you"; the person answers from there (in Chat, on the
old Run's turn), the replacement hears it, and "Needs you" clears.

(b) The conductor fails with nothing unread while it waits (main's end
state for the issue: the question cancelled with the Run, no event): the
Overview does not say "Needs you" for it; it shows the lost-asker card with
the question and Try again, which starts a fresh conductor that dude asks
again, and "Needs you" returns.

Screenshots go to $DUDE_TEST_SHOTS when it is set.
"""

from __future__ import annotations

import json
import os
from pathlib import Path

import pytest
import requests
from playwright.sync_api import Page, expect

from helpers import ApiClient, execute, query, sign_in, wait_until

SHOTS = Path(os.environ["DUDE_TEST_SHOTS"]) if os.environ.get("DUDE_TEST_SHOTS") else None

ASK = {"question": "Keep the invoice option on monthly plans?", "choices": ["Keep it", "Drop it"]}


def _shoot(page: Page, name: str) -> None:
    if SHOTS is not None:
        SHOTS.mkdir(parents=True, exist_ok=True)
        page.screenshot(path=str(SHOTS / f"{name}.png"), full_page=False)


def _asking(client: ApiClient, project: dict, owner_dsn: str, title: str) -> tuple[dict, str, str]:
    """A task whose conductor asked ASK and ended its turn waiting on the
    person, everything it was sent heard: the task, its conductor, the question."""
    models = {**project["agentModels"], **client.on_models({"conductor": "fake/scripted"})}
    assert client.patch(f"/v1/projects/{project['id']}", {"agentModels": models}).status_code == 200
    assert client.patch(f"/v1/projects/{project['id']}/settings", {"delivery": {"conductorWarmMinutes": 30}}).status_code == 200
    task = client.create_task(project["id"], title)
    sent = client.post(f"/v1/tasks/{task['id']}/chat", {"text": f"Plan it.\ntool: ask_person {json.dumps(ASK)}"})
    assert sent.status_code in (200, 201), sent.text
    run = sent.json()["runId"]
    q = wait_until(lambda: query(owner_dsn, """SELECT q.id FROM questions q JOIN runs r ON r.id = q.run_id
        WHERE q.run_id = %s AND q.status = 'open' AND q.addressee = 'person' AND r.waiting_since IS NOT NULL
          AND NOT EXISTS (SELECT 1 FROM directives d WHERE d.run_id = r.id AND d.delivered_at IS NULL AND d.failed_at IS NULL)""",
                                   (run,)), timeout=60, message="the conductor never asked and waited")[0]["id"]
    return task, run, q


def _needs_you(page: Page):
    return page.get_by_test_id("needs-you")


@pytest.mark.ui
def test_a_question_moved_off_a_lost_conductor_still_needs_you_and_its_answer_is_heard(
    page: Page, web_url: str, client: ApiClient, org: dict, forge_project: dict, owner_dsn: str, env, console_errors: list
):
    task, old, question = _asking(client, forge_project, owner_dsn, "Invoice option, its conductor lost")
    page.set_viewport_size({"width": 1440, "height": 1000})
    sign_in(page, web_url, org["api_key"], at=f"#/task/{task['id']}")
    expect(_needs_you(page)).to_be_visible(timeout=30_000)
    expect(_needs_you(page)).to_contain_text("Keep the invoice option on monthly plans?")
    _shoot(page, "a-1-needs-you-before")

    # lux loses the conductor's Run: the question moves to the Run in its slot, still open.
    lux_run = query(owner_dsn, "SELECT lux_run_id FROM runs WHERE id = %s", (old,))[0]["lux_run_id"]
    requests.post(f"{env.fake_lux_url}/fake/runs/{lux_run}/forget",
                  headers={"authorization": f"Bearer {env.lux_key}"}, timeout=10).raise_for_status()
    wait_until(lambda: client.get_run(old)["status"] == "failed", timeout=60, message="the lost conductor never ended")
    held = query(owner_dsn, "SELECT status::text AS status, run_id FROM questions WHERE id = %s", (question,))[0]
    assert held["status"] == "open" and held["run_id"] != old, held
    placeholder = held["run_id"]

    # The page still says it needs you, for the same question; no lost-asker card.
    page.reload()
    expect(_needs_you(page)).to_be_visible(timeout=30_000)
    expect(_needs_you(page)).to_have_attribute("data-question", question)
    expect(page.get_by_test_id("lost-asker")).to_have_count(0)
    _shoot(page, "a-2-needs-you-after-loss")

    # Answered from the Overview: its Answer in Chat, then the question's own turn.
    page.get_by_test_id("needs-you-answer").click()
    turn = page.locator(f"[data-testid=question-turn][data-question='{question}']")
    choice = turn.get_by_role("radio", name="Keep it", exact=True)
    expect(choice).to_be_visible(timeout=30_000)
    choice.click()
    wait_until(lambda: query(owner_dsn, """SELECT 1 FROM directives WHERE run_id = %s AND delivered_at IS NOT NULL
        AND text LIKE %s""", (placeholder, "%Keep it%")), timeout=90, message="the answer never reached the replacement")
    assert query(owner_dsn, "SELECT status::text AS status FROM questions WHERE id = %s", (question,)) == [{"status": "answered"}]

    # Heard: nothing needs the person any more.
    page.goto(f"{web_url}/#/task/{task['id']}")
    expect(page.get_by_test_id("task-overview")).to_be_visible(timeout=30_000)
    expect(_needs_you(page)).to_have_count(0, timeout=30_000)
    _shoot(page, "a-3-needs-you-cleared")
    assert console_errors == []


@pytest.mark.ui
def test_a_conductor_failed_with_its_question_offers_try_again_which_asks_again(
    page: Page, web_url: str, client: ApiClient, org: dict, forge_project: dict, owner_dsn: str, console_errors: list
):
    task, old, question = _asking(client, forge_project, owner_dsn, "Invoice option, its conductor failed")
    # Failed as main left the issue's task: lux's 404, the question cancelled with the Run, no event, nothing unread.
    execute(owner_dsn, "UPDATE runs SET status = 'failed', error = 'lux no longer has this Run', ended_at = now() WHERE id = %s", (old,))
    assert query(owner_dsn, "SELECT status::text AS status FROM questions WHERE id = %s", (question,)) == [{"status": "cancelled"}]
    recover = client.get(f"/v1/tasks/{task['id']}/recover").json()
    assert recover["conductor"]["retry"] is True, recover

    page.set_viewport_size({"width": 1440, "height": 1000})
    sign_in(page, web_url, org["api_key"], at=f"#/task/{task['id']}")
    card = page.get_by_test_id("task-overview").get_by_test_id("lost-asker")
    expect(card).to_be_visible(timeout=30_000)
    # Not "Needs you": the question is not open.
    expect(_needs_you(page)).to_have_count(0)
    expect(page.get_by_test_id("header-status")).not_to_contain_text("Needs you")
    expect(card).to_contain_text("Keep the invoice option on monthly plans?")
    expect(card).to_contain_text("dude lost the conductor that asked this — your answer wasn’t heard.")
    expect(card).to_contain_text("lux no longer has it")
    _shoot(page, "b-1-lost-asker-overview")

    # Chat shows the same card, below the ended turn.
    page.goto(f"{web_url}/#/task/{task['id']}/chat")
    expect(page.get_by_test_id("task-chat").get_by_test_id("lost-asker")).to_be_visible(timeout=30_000)
    expect(page.locator(f"[data-testid=question-turn][data-question='{question}']")).to_contain_text("Not heard: dude lost the conductor that asked this")
    _shoot(page, "b-2-lost-asker-chat")

    # Try again: a fresh conductor, which dude asks again; "Needs you" returns.
    page.goto(f"{web_url}/#/task/{task['id']}")
    card = page.get_by_test_id("task-overview").get_by_test_id("lost-asker")
    card.get_by_test_id("lost-asker-retry").click()
    asked = wait_until(lambda: query(owner_dsn, """SELECT q.id, q.run_id FROM questions q
        WHERE q.task_id = %s AND q.status = 'open' AND q.addressee = 'person' AND q.run_id <> %s""", (task["id"], old)),
                       timeout=60, message="Try again never asked again")[0]
    expect(_needs_you(page)).to_be_visible(timeout=30_000)
    expect(_needs_you(page)).to_have_attribute("data-question", asked["id"])
    expect(_needs_you(page)).to_contain_text("Keep the invoice option on monthly plans?")
    expect(page.get_by_test_id("lost-asker")).to_have_count(0)
    _shoot(page, "b-3-needs-you-after-try-again")
    assert console_errors == []
