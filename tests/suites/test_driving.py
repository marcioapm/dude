"""Who drives a task, end to end through the deployed processes and the built UI.

A task Deliver delivers shows Deliver beside its status. Its owner gives
the decisions to the conductor from the header's switch; the header and
Chat then both say the conductor decides. Let Deliver finish it, from
Chat, hands them back: the composer says messages are answered, and
Deliver decides. A colleague sees the same chip with nothing to switch.
On a conducted task waiting on the pull request question, letting
Deliver finish asks first: keeping on deciding in Chat opens nothing.

Screenshots: with DUDE_SHOTS=<dir> set, the header, its menu, Chat and
the board are saved there, at 1440x900 light and dark and 390x844.
"""

from __future__ import annotations

import json
import os
from pathlib import Path

from playwright.sync_api import Page, expect

from fake_github import FakeGitHub
from helpers import ApiClient, provision_person, sign_in, wait_until

SHOTS = Path(os.environ["DUDE_SHOTS"]) if os.environ.get("DUDE_SHOTS") else None


def _shoot(page: Page, name: str) -> None:
    if not SHOTS:
        return
    SHOTS.mkdir(parents=True, exist_ok=True)
    size = page.viewport_size or {"width": 1440, "height": 900}
    for width, height, themes in ((1440, 900, ("dark", "light")), (390, 844, ("dark",))):
        page.set_viewport_size({"width": width, "height": height})
        for theme in themes:
            page.evaluate("t => document.documentElement.setAttribute('data-theme', t)", theme)
            page.wait_for_timeout(200)
            page.screenshot(path=str(SHOTS / f"{name}-{width}-{theme}.png"))
    page.set_viewport_size(size)
    page.evaluate("() => document.documentElement.removeAttribute('data-theme')")


def _task(client: ApiClient, task_id: str) -> dict:
    return client.get(f"/v1/tasks/{task_id}").json()


def _tool(name: str, args: dict | None = None) -> str:
    return f"tool: {name} {json.dumps(args or {})}"


def _say(client: ApiClient, task_id: str, *lines: str) -> None:
    resp = client.post(f"/v1/tasks/{task_id}/chat", {"text": "\n".join(lines)})
    assert resp.status_code in (200, 201), resp.text


def _waiting_on(client: ApiClient, task_id: str, point: str) -> None:
    wait_until(lambda: (_task(client, task_id)["awaitingDecision"] or {}).get("point") == point, timeout=90,
               message=f"the delivery never waited on {point}")


def test_the_owner_switches_who_drives_and_header_and_chat_agree(
    page: Page, web_url: str, env, client: ApiClient, org: dict, forge_project: dict, console_errors: list,
):
    models = {**forge_project["agentModels"], **client.on_models({"implementer": "fake/hang", "conductor": "fake/scripted"})}
    assert client.patch(f"/v1/projects/{forge_project['id']}", {"agentModels": models}).status_code == 200
    task = client.create_task(forge_project["id"], "Greet, driven")
    assert client.post(f"/v1/tasks/{task['id']}/deliver").status_code == 201
    wait_until(lambda: any(r["phase"] == "implement" and r["status"] == "running" for r in client.task_runs(task["id"])),
               timeout=60, message="the implementer never ran")

    page.set_viewport_size({"width": 1440, "height": 900})
    sign_in(page, web_url, org["api_key"])
    # The board: the card in progress carries Deliver's glyph, said in its name.
    card = page.locator(f"[data-board-key='task:{task['id']}']")
    expect(card.get_by_test_id("driver-glyph")).to_have_attribute("data-driver", "deliver", timeout=30_000)
    expect(card).to_have_attribute("aria-label", f"{task['key']} Greet, driven; Deliver, runs to the pull request on its own")
    expect(card.locator("[data-testid=driver-glyph] button")).to_have_count(0)
    _shoot(page, "board")

    page.goto(f"{web_url}#/task/{task['id']}")
    header = page.get_by_test_id("task-header")
    chip = header.get_by_test_id("driver")
    expect(chip).to_have_attribute("data-driver", "deliver", timeout=30_000)
    expect(chip).to_contain_text("Deliver · runs to the pull request on its own")
    _shoot(page, "header-deliver")

    # The owner's switch: Deliver ticked; Give decisions to the conductor.
    chip.click()
    menu = page.get_by_role("menu")
    expect(menu.get_by_role("menuitemradio", checked=True)).to_contain_text("Deliver decides")
    expect(menu).to_contain_text("It takes over when the implementer finishes; the implementer keeps running.")
    _shoot(page, "menu-deliver")
    menu.get_by_role("menuitemradio").filter(has_text="Give decisions to the conductor").click()
    expect(chip).to_have_attribute("data-driver", "conductor", timeout=30_000)
    assert _task(client, task["id"])["decider"] == "conductor"
    # The implementer was not restarted: the same one is still at work.
    assert [r["status"] for r in client.task_runs(task["id"]) if r["phase"] == "implement"] == ["running"]

    # Chat agrees, and hands back from there.
    page.goto(f"{web_url}#/task/{task['id']}/chat")
    line = page.get_by_test_id("decider-line")
    expect(line).to_have_attribute("data-decider", "conductor", timeout=30_000)
    _shoot(page, "chat-conductor")
    line.get_by_test_id("let-deliver-finish").click()
    expect(line).to_have_attribute("data-decider", "policy", timeout=30_000)
    expect(chip).to_have_attribute("data-driver", "deliver")
    expect(line).to_contain_text("messages here are answered, and take no decisions back")
    expect(page.get_by_test_id("composer-to")).to_have_text("To Conductor · answers only, Deliver decides")
    _shoot(page, "chat-handed-back")
    t = _task(client, task["id"])
    assert (t["decider"], t["handedBack"]) == ("policy", True)

    # Activity: who did each, in the words of why.
    page.goto(f"{web_url}#/task/{task['id']}/activity")
    activity = page.get_by_test_id("activity")
    expect(activity).to_contain_text("e2e user let Deliver finish it", timeout=30_000)
    expect(activity).to_contain_text("e2e user gave the decisions to the conductor")

    # A colleague: the same chip, no menu, and why.
    _, bo = provision_person(client, env, "Bo", f"bo-{org['id']}@acme.dev")
    # A browser of his own: the owner's key stays the owner's.
    page = page.context.new_page()
    # The console_errors fixture's listeners, on his page too.
    def record(text: str) -> None:
        if "fonts" not in text and "rsms.me" not in text:
            console_errors.append(text)
    page.on("console", lambda m: record(m.text) if m.type == "error" else None)
    page.on("pageerror", lambda e: record(str(e)))
    page.set_viewport_size({"width": 1440, "height": 900})
    sign_in(page, web_url, bo.api_key, at=f"#/task/{task['id']}")
    chip = page.get_by_test_id("task-header").get_by_test_id("driver")
    expect(chip).to_have_attribute("data-switchable", "false", timeout=30_000)
    expect(chip).to_have_attribute("aria-label", "Deliver: runs to the pull request on its own. Only the owner can change who decides. e2e owns this task.")
    chip.focus()
    expect(page.get_by_role("tooltip")).to_contain_text("Only the owner can change who decides.")
    _shoot(page, "header-member")
    assert console_errors == []


def test_letting_deliver_finish_at_the_pull_request_question_asks_first(
    page: Page, web_url: str, client: ApiClient, org: dict, forge_project: dict, fake_github: FakeGitHub, console_errors: list,
):
    models = {**forge_project["agentModels"], **client.on_models({"conductor": "fake/scripted"})}
    assert client.patch(f"/v1/projects/{forge_project['id']}", {"agentModels": models}).status_code == 200
    assert client.patch(f"/v1/projects/{forge_project['id']}/settings", {"delivery": {"conductorWarmMinutes": 30}}).status_code == 200
    task = client.create_task(forge_project["id"], "Greet, gated")
    assert client.post(f"/v1/tasks/{task['id']}/talk").status_code == 201
    _waiting_on(client, task["id"], "start")
    _say(client, task["id"], "Go.", _tool("start_phase", {"phase": "implement"}))
    _waiting_on(client, task["id"], "after_implement")
    _say(client, task["id"], "Simplify, then the PR.", _tool("start_phase", {"phase": "simplify"}))
    _waiting_on(client, task["id"], "before_pull_request")

    page.set_viewport_size({"width": 1440, "height": 900})
    sign_in(page, web_url, org["api_key"], at=f"#/task/{task['id']}/chat")
    chip = page.get_by_test_id("task-header").get_by_test_id("driver")
    expect(chip).to_contain_text("Conductor · waiting on it: open the PR?", timeout=30_000)
    line = page.get_by_test_id("decider-line")
    line.get_by_test_id("let-deliver-finish").click()
    dialog = page.get_by_role("dialog")
    expect(dialog).to_contain_text("Deliver will open the pull request now.")
    _shoot(page, "gate-confirm")
    # Keep deciding in Chat: nothing changes, nothing opens.
    dialog.get_by_test_id("hand-back-keep").click()
    expect(page.get_by_role("dialog")).to_have_count(0)
    assert _task(client, task["id"])["decider"] == "conductor"
    assert client.get("/v1/pull-requests", params={"taskId": task["id"]}).json()["pullRequests"] == []
    expect(chip).to_have_attribute("data-driver", "conductor")

    # The same from the header's switch, confirmed: Deliver opens it.
    chip.click()
    page.get_by_role("menu").get_by_role("menuitemradio").filter(has_text="Let Deliver finish it").click()
    page.get_by_role("dialog").get_by_test_id("hand-back-open").click()
    wait_until(lambda: client.get("/v1/pull-requests", params={"taskId": task["id"]}).json()["pullRequests"], timeout=90,
               message="the confirmed hand-back never opened the pull request")
    assert _task(client, task["id"])["decider"] == "policy"
    # The gate's refusals (409 pull_request_gate) are the browser's only complaints: one per unconfirmed hand-back.
    assert console_errors == ["Failed to load resource: the server responded with a status of 409 (Conflict)"] * 2, console_errors
