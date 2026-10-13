"""A Run's child sessions (subagents), end to end through the deployed processes.

The implementer hangs (fake/hang) on each harness in turn. The fake lux then
says a child of its agent as that harness does (POST /fake/runs/{id}/child):
the parent's task or spawn call, the child's started, one tool call, one
message, a permission it waits on (OpenCode only: its harness is the one that
asks and never forwards), its end. The ledger has the agent.child.* events
nested under the parent's call, none of the child's calls as the Run's own,
and the Run's transcript draws the child's thread under the call, its waiting
state while it waits, folded once done.
"""

from __future__ import annotations

import pytest
import requests
from playwright.sync_api import Page, expect

from helpers import ApiClient, query, sign_in, wait_until

# Each drives the Run's transcript in a browser.
pytestmark = pytest.mark.ui

CALL, CHILD = "call_child_1", "kid_1"
TITLE = "Inspect test capture setup"
PERMISSION = "external_directory"
PATTERN = "/opt/ui-tests/lib/python3.14/site-packages/*"


def _child(env, lux_run: str, op: str, **fields) -> None:
    requests.post(f"{env.fake_lux_url}/fake/runs/{lux_run}/child", headers={"authorization": f"Bearer {env.lux_key}"},
                  json={"op": op, "callId": CALL, "childId": CHILD, "title": TITLE, "agentType": "explore", **fields},
                  timeout=10).raise_for_status()


def _events(client: ApiClient, run_id: str, prefix: str = "agent.") -> list[dict]:
    return [e for e in client.events(runId=run_id, limit=1000) if e["eventType"].startswith(prefix)]


def _deliver_implementer(client: ApiClient, project: dict, title: str, model: str | dict) -> dict:
    models = {**project["agentModels"], **client.on_models({"implementer": model})}
    assert client.patch(f"/v1/projects/{project['id']}", {"agentModels": models}).status_code == 200
    task = client.create_task(project["id"], title)
    assert client.post(f"/v1/tasks/{task['id']}/deliver").status_code == 201
    return wait_until(
        lambda: next((r for r in client.task_runs(task["id"]) if r["phase"] == "implement" and r["status"] == "running"), None),
        timeout=60, message="the implementer never ran")


@pytest.mark.parametrize("harness", ["opencode", "claude-code", "codex"])
def test_a_child_session_is_recorded_and_drawn_under_its_call(page: Page, web_url: str, env, client: ApiClient, org: dict,
                                                              forge_project: dict, owner_dsn: str, console_errors: list,
                                                              harness: str):
    role = {"model": "fake/hang"} if harness == "opencode" else {"model": "fake/hang", "harness": harness}
    run = _deliver_implementer(client, forge_project, f"Delegate on {harness}", role)
    wait_until(lambda: any(e["eventType"] == "agent.session.started" for e in _events(client, run["id"])),
               timeout=30, message="the implementer's session never started")
    lux_run = query(owner_dsn, "SELECT lux_run_id FROM runs WHERE id = %s", (run["id"],))[0]["lux_run_id"]

    _child(env, lux_run, "start")
    _child(env, lux_run, "tool", toolCallId="k1", tool="bash", input={"command": "ls /opt/ui-tests"}, done=True, output="lib")
    _child(env, lux_run, "message", text="No viewport is set in the repo.")
    waits = harness == "opencode"
    if waits:
        _child(env, lux_run, "tool", toolCallId="k2", tool="grep", input={"pattern": "viewport", "path": PATTERN})
        _child(env, lux_run, "wait", toolCallId="k2", permission=PERMISSION, patterns=[PATTERN])
        wait_until(lambda: any(e["eventType"] == "agent.child.waiting" for e in _events(client, run["id"])),
                   timeout=30, message="the child's wait never reached the ledger")
        children = query(owner_dsn, "SELECT children FROM runs WHERE id = %s", (run["id"],))[0]["children"]
        assert children[CALL]["childId"] == CHILD and children[CALL]["waitingOn"]["permission"] == PERMISSION, children

    sign_in(page, web_url, org["api_key"])
    page.goto(f"{web_url}#/session/{run['id']}")
    thread = page.locator(f"[data-testid=child-thread][data-child='{CHILD}']")
    expect(thread).to_have_attribute("data-status", "running", timeout=30_000)
    expect(thread.locator("[data-role=subagent]").first).to_be_visible()
    expect(thread).to_contain_text(TITLE)
    expect(thread).to_contain_text("No viewport is set in the repo.")
    if waits:
        expect(thread).to_have_attribute("data-waiting", "true")
        expect(thread.get_by_test_id("chat-thread-waiting")).to_contain_text("on a permission")
        expect(thread.get_by_test_id("child-waiting-notice")).to_contain_text(
            f"Waiting on a permission: {PERMISSION} for {PATTERN}. Nothing in this Run can answer it.")

    _child(env, lux_run, "end", text="Playwright's 1280×720 default applies.")
    expect(thread).to_have_attribute("data-status", "completed", timeout=30_000)
    # Done, it folds to its summary.
    expect(thread.locator("[role=group]")).to_have_count(0)
    expect(thread).not_to_have_attribute("data-waiting", "true")
    # Claude Code's end carries its summary; the others' is the child's last message.
    expect(thread).to_contain_text("Playwright's 1280×720 default applies." if harness == "claude-code" else "No viewport is set in the repo.")

    events = _events(client, run["id"])
    child = [e for e in events if e["eventType"].startswith("agent.child.")]
    kinds = [e["eventType"] for e in child]
    want = ["agent.child.started", "agent.child.tool.called", "agent.child.tool.completed", "agent.child.message"]
    if waits:
        want += ["agent.child.tool.called", "agent.child.waiting"]
    assert kinds == want + ["agent.child.ended"], kinds
    assert all(e["payload"]["childId"] == CHILD for e in child), child
    started = child[0]["payload"]
    assert started["callId"] == CALL and started["title"] == TITLE, started
    assert child[1]["payload"]["parentCallId"] == CALL and child[1]["payload"]["tool"] == "bash", child[1]
    own = {e["payload"].get("callId") for e in events if e["eventType"] in ("agent.tool.called", "agent.tool.completed")}
    assert "k1" not in own and "k2" not in own, own
    parent = next(e for e in events if e["eventType"] == "agent.tool.called" and e["payload"].get("callId") == CALL)
    assert parent["payload"]["tool"] == "task", parent
    assert query(owner_dsn, "SELECT children FROM runs WHERE id = %s", (run["id"],))[0]["children"] == {}
    assert console_errors == []


def test_the_scripted_subagents_ask_is_answered_by_lux_and_its_child_ends(page: Page, web_url: str, client: ApiClient, org: dict,
                                                                         forge_project: dict, owner_dsn: str, console_errors: list):
    """fake/subagent-ask, lux-fake's `subagent ask` as lux relays it: a
    child with lux-fake's ids reads a file, its bash call asks permission
    bash for "cat /etc/hostname", lux allows it and warns, and the child
    runs the command and ends. The ask holds nothing; the thread folds."""
    run = _deliver_implementer(client, forge_project, "Delegate and ask", "fake/subagent-ask")
    def ended():
        events = _events(client, run["id"], "agent.child.")
        return events if any(e["eventType"] == "agent.child.ended" for e in events) else None

    child = wait_until(ended, timeout=30, message="the child never ended")
    assert [e["eventType"] for e in child] == [
        "agent.child.started", "agent.child.tool.called", "agent.child.tool.completed", "agent.child.tool.called",
        "agent.child.waiting", "agent.child.tool.completed", "agent.child.message", "agent.child.ended"], child
    p = child[4]["payload"]
    assert (p["permission"], p["patterns"], p["held"], p["answered"], p["childCallId"]) == \
        ("bash", ["cat /etc/hostname"], False, "allow", "toolu_bash1"), p
    assert p["callId"] == "toolu_task1" and p["childId"].endswith("_child1"), p
    warnings = [e["payload"]["message"] for e in _events(client, run["id"]) if e["eventType"] == "agent.warning"]
    assert any("lux allowed it" in w for w in warnings), warnings
    assert query(owner_dsn, "SELECT children FROM runs WHERE id = %s", (run["id"],))[0]["children"] == {}

    sign_in(page, web_url, org["api_key"])
    page.goto(f"{web_url}#/session/{run['id']}")
    thread = page.get_by_test_id("child-thread")
    expect(thread).to_have_attribute("data-status", "completed", timeout=30_000)
    expect(thread).not_to_have_attribute("data-waiting", "true")
    expect(thread).to_contain_text("the child read a.txt: hello")
    assert console_errors == []
