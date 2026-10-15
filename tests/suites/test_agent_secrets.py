"""Project secrets given to agents, end to end through the public API,
the built web app, the deployed orchestrator and the fake lux. Every value
here is a fake credential.

SNOWFLAKE_PASSWORD is given to Previews and the Tester (the default): the
tester's Run and a preview get it, the implementer and the conductor do
not. The tester printing it shows [REDACTED:SNOWFLAKE_PASSWORD] in its
session, as stored and as the web app shows it. A value rotated while an
implementer that was given it waits on a question is still redacted in
what it says after. An implementer committing a value is refused before
anything is pushed, its owner told the secret's name, never the value.

The scripted models: `fake/print-secrets` prints its env secrets in a bash
call (and as an implementer asks a question first, then quotes them);
`fake/commit-secrets` commits them to config/secrets.env.
"""

from __future__ import annotations

import base64
import os

import pytest
import requests
from playwright.sync_api import Page, expect

from fake_github import FakeGitHub
from helpers import ApiClient, query, sign_in, wait_for_escalation, wait_until

NAME = "SNOWFLAKE_PASSWORD"
MARK = f"[REDACTED:{NAME}]"


def _value() -> str:
    return f"sf-test-not-a-real-password-{os.urandom(4).hex()}"


def _lux(env, path: str) -> dict:
    res = requests.get(env.lux_url + path, headers={"Authorization": f"Bearer {env.lux_key}"}, timeout=10)
    res.raise_for_status()
    return res.json()


def _lux_spec(client: ApiClient, env, run_id: str) -> dict:
    lux_run = wait_until(lambda: (rows := query(env.owner_dsn, "SELECT lux_run_id FROM runs WHERE id = %s", (run_id,)))
                         and rows[0]["lux_run_id"], timeout=60, message=f"run {run_id} never reached lux")
    return _lux(env, f"/v1/runs/{lux_run}")["spec"]


def _declares(spec: dict) -> list[str]:
    return [s["name"] for s in spec.get("secrets", [])]


def _events_text(client: ApiClient, run_id: str) -> str:
    return "\n".join(repr(e.get("payload")) for e in client.events(runId=run_id, limit=1000))


def _events_of(client: ApiClient, run_id: str, typ: str) -> list[dict]:
    return [e for e in client.events(runId=run_id, limit=1000) if e["eventType"] == typ]


def _models(client: ApiClient, project: dict, **models: str) -> None:
    base = {"implementer": "fake/scripted", "reviewer": "fake/scripted", "simplifier": "fake/scripted", "qa_browser": "fake/scripted"}
    base.update(models)
    resp = client.patch(f"/v1/projects/{project['id']}", {
        "deliveryPolicy": {"requiredReviewers": ["correctness"], "test": True},
        "agentModels": client.on_models(base),
    })
    assert resp.status_code == 200, resp.text


def _deliver_printing_tester(client: ApiClient, project: dict, value: str) -> tuple[dict, dict]:
    """A delivery whose tester prints its env secrets, with SNOWFLAKE_PASSWORD
    added at its default recipients; the task, and its finished tester."""
    pid = project["id"]
    added = client.post(f"/v1/projects/{pid}/secrets", {"name": NAME, "value": value})
    assert added.status_code == 201, added.text
    assert added.json()["recipients"] == ["previews", "tester"] and value not in added.text
    _models(client, project, qa_browser="fake/print-secrets")
    task = client.create_task(pid, "Load the warehouse", acceptanceCriteria=["It loads the rows"])
    assert client.post(f"/v1/tasks/{task['id']}/deliver").status_code == 201
    tester = wait_until(lambda: next((r for r in client.task_runs(task["id"]) if r["phase"] == "test" and r["status"] == "completed"), None),
                        timeout=180, message="the tester never finished")
    return task, tester


def test_previews_and_tester_secret_reaches_the_tester_and_a_preview_redacted_never_the_implementer_or_conductor(
        client: ApiClient, forge_project: dict, env):
    value = _value()
    pid = forge_project["id"]
    task, tester = _deliver_printing_tester(client, forge_project, value)
    implementer = next(r for r in client.task_runs(task["id"]) if r["phase"] == "implement")

    assert NAME in _declares(_lux_spec(client, env, tester["id"]))
    assert NAME not in _declares(_lux_spec(client, env, implementer["id"]))
    assert client.get_run(tester["id"])["projectSecretNames"] == [NAME]
    assert client.get_run(implementer["id"])["projectSecretNames"] == []
    given = _events_of(client, tester["id"], "run.secrets.given")
    assert [e["payload"] for e in given] == [{"names": [NAME]}], given

    # What the tester printed is stored redacted, every form of the value gone.
    text = _events_text(client, tester["id"])
    assert f"{NAME}={MARK}" in text, text[:2000]
    for leak in (value, base64.b64encode(value.encode()).decode()):
        assert leak not in text

    # The conductor, asked in Chat, is given nothing.
    client.patch(f"/v1/projects/{pid}", {"agentModels": {**client.get(f"/v1/projects/{pid}").json()["agentModels"],
                                                         **client.on_models({"conductor": "fake/scripted"})}})
    assert client.post(f"/v1/tasks/{task['id']}/chat", {"text": "what is this task about?"}).status_code == 201
    conductor = wait_until(lambda: next((r for r in client.task_runs(task["id"]) if r["role"] == "conductor"), None),
                           timeout=60, message="no conductor")
    spec = _lux_spec(client, env, conductor["id"])
    assert NAME not in _declares(spec) and value not in repr(spec)
    assert client.get_run(conductor["id"])["projectSecretNames"] == []

    # A preview gets it: a task's preview, woken by a request.
    recipe = {"name": "web", "port": 3000, "command": "npm run dev", "workdir": "", "env": [{"name": "PORT", "value": "3000"}],
              "autostartInPreviews": True}
    assert client.put(f"/v1/projects/{pid}/servers/web", recipe).status_code == 200
    other = client.create_task(pid, "Preview the warehouse page")
    assert client.post(f"/v1/tasks/{other['id']}/preview").status_code == 201
    asleep = wait_until(lambda: (s := client.get(f"/v1/tasks/{other['id']}/servers").json())["run"].get("asleep") and s,
                        timeout=60, message="the preview never went to sleep")
    res = requests.post(env.lux_url + f"/fake/servers/{asleep['servers'][0]['id']}/request?path=/",
                        headers={"Authorization": f"Bearer {env.lux_key}"}, timeout=10)
    res.raise_for_status()
    preview = wait_until(lambda: client.get(f"/v1/tasks/{other['id']}/servers").json()["run"].get("luxRunId"),
                         timeout=60, message="the preview never reached lux")
    assert NAME in _declares(_lux(env, f"/v1/runs/{preview}")["spec"])


@pytest.mark.ui
def test_the_testers_session_shows_the_marker_and_its_secrets_chip_never_the_value(
        page: Page, web_url: str, client: ApiClient, org: dict, forge_project: dict, console_errors: list):
    value = _value()
    _, tester = _deliver_printing_tester(client, forge_project, value)
    sign_in(page, web_url, org["api_key"])
    page.goto(f"{web_url}#/session/{tester['id']}")
    expect(page.get_by_test_id("run-secrets")).to_have_text("1 secret")
    expect(page.get_by_text(MARK).first).to_be_visible()
    assert value not in page.content()


def test_a_value_rotated_while_an_agent_waits_is_still_redacted(
        client: ApiClient, forge_project: dict, env):
    """The implementer prints the value it was given, then waits on a
    question; the value is rotated meanwhile. Its reply after the answer
    quotes the value it holds: redacted from the Run's copy though the
    project has another value now. (A resume supplying the new value and
    replacing the copy is TestAResumeSuppliesCurrentValuesAndReplacesTheCopy.)"""
    old, new = _value(), _value()
    pid = forge_project["id"]
    added = client.post(f"/v1/projects/{pid}/secrets", {"name": NAME, "value": old, "recipients": ["implementer"]})
    assert added.status_code == 201, added.text
    _models(client, forge_project, implementer="fake/print-secrets")
    task = client.create_task(pid, "Rotate mid-run")
    assert client.post(f"/v1/tasks/{task['id']}/deliver").status_code == 201
    question = wait_until(lambda: next((q for q in client.get("/v1/questions", params={"taskId": task["id"]}).json().get("questions", [])
                                        if q["status"] == "open"), None),
                          timeout=90, message="the implementer never asked")
    run_id = question["runId"]

    # Rotated while it waits on the answer: what it says after is redacted
    # with the value it was given.
    assert client.put(f"/v1/projects/{pid}/secrets/{NAME}", {"value": new}).status_code == 200
    assert client.post(f"/v1/questions/{question['id']}/answer", {"text": "yes"}).status_code == 200
    wait_until(lambda: any("Connected with these" in repr(e["payload"]) for e in _events_of(client, run_id, "agent.message")),
               timeout=120, message="the implementer never answered")
    text = _events_text(client, run_id)
    assert old not in text and new not in text
    assert MARK in text


def test_an_implementer_committing_a_secret_is_refused_before_the_push(
        client: ApiClient, forge_project: dict, fake_github: FakeGitHub, env):
    value = _value()
    pid = forge_project["id"]
    assert client.post(f"/v1/projects/{pid}/secrets", {"name": NAME, "value": value,
                                                      "recipients": ["implementer"]}).status_code == 201
    _models(client, forge_project, implementer="fake/commit-secrets")
    task = client.create_task(pid, "Hard-code the password")
    assert client.post(f"/v1/tasks/{task['id']}/deliver").status_code == 201
    try:
        escalated = wait_for_escalation(client, task["id"], timeout=120)
    except AssertionError:
        raise AssertionError(f"never escalated: {[(r['phase'], r['status'], r.get('error')) for r in client.task_runs(task['id'])]}")
    assert escalated["escalation"]["reason"] == "implement_failed", escalated["escalation"]
    run = next(r for r in client.task_runs(task["id"]) if r["phase"] == "implement")
    assert run["status"] == "failed"
    assert f"{NAME} in config/secrets.env" in run["error"] and value not in run["error"], run["error"]
    refused = _events_of(client, run["id"], "run.push.refused")
    assert [e["payload"] for e in refused] == [{"secrets": [NAME], "files": ["config/secrets.env"]}], refused
    # Nothing reached GitHub: no branch of this Run's.
    assert not [b for b in fake_github.branches() if run["id"] in b], fake_github.branches()
    assert value not in _events_text(client, run["id"])
