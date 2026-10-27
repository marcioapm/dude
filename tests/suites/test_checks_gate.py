"""Checks-gate delivery through the real API and web stack.

The fake lux runs one of the scripted checks models. The organisation's
judgement tier is configured through the public judgements API; the fake
LLM proxy is the suite's configured endpoint, so the suite uses the same
control plane and orchestrator processes as other E2E tests.
"""

from __future__ import annotations

import re

import pytest
from playwright.sync_api import Page, expect

from fake_github import FakeGitHub
from helpers import ApiClient, sign_in, wait_until


CHECKS_MODELS = {
    "honest": "fake/checks-honest",
    "unbacked": "fake/checks-unbacked",
    "fixed": "fake/checks-fixed",
    "base-shown": "fake/checks-base-shown",
}


def _project(client: ApiClient, fake_github: FakeGitHub, model: str) -> dict:
    return client.create_project(
        name="Checks gate",
        slug=f"checks-{fake_github.api_port}-{model.replace('/', '-')}",
        agentModels=client.on_models({
            "implementer": model,
            "reviewer": "fake/scripted",
            "simplifier": "fake/scripted",
        }),
        deliveryPolicy={"requiredReviewers": ["correctness"], "test": False},
        repositories=[{"name": "checks", "url": fake_github.clone_url, "defaultBranch": "main"}],
    )


def _judge(client: ApiClient, fake_github: FakeGitHub, model: str = "judge-e2e") -> None:
    credential = client.post("/v1/forge/credential", {"auth": "pat", "secret": "fake-token", "apiBaseUrl": fake_github.api_url})
    assert credential.status_code == 200, credential.text
    tier = client.tier_for(model)
    resp = client.put("/v1/judgements/settings", {"tierId": tier})
    assert resp.status_code == 200, resp.text


def _start(client: ApiClient, fake_github: FakeGitHub, model: str) -> dict:
    # The forge credential is needed for publishing and for its checksFixRounds setting.
    credential = client.post("/v1/forge/credential", {"auth": "pat", "secret": "fake-token", "apiBaseUrl": fake_github.api_url})
    assert credential.status_code in (200, 409), credential.text
    project = _project(client, fake_github, model)
    task = client.create_task(project["id"], "Verify the checks gate")
    started = client.post(f"/v1/tasks/{task['id']}/deliver")
    assert started.status_code == 201, started.text
    return task


def _implementer_runs(client: ApiClient, task_id: str) -> list[dict]:
    return [r for r in client.task_runs(task_id) if r["phase"] == "implement"]


def _implementer(client: ApiClient, task_id: str) -> dict:
    return _implementer_runs(client, task_id)[0]


def _checks_events(client: ApiClient, run_id: str) -> list[dict]:
    return [e for e in client.events(runId=run_id) if e["eventType"] == "run.checks.judged"]


def _checks_for_task(client: ApiClient, task_id: str) -> list[dict]:
    runs = _implementer_runs(client, task_id)
    return _checks_events(client, runs[0]["id"]) if runs else []


def _wait_pr(client: ApiClient, fake_github: FakeGitHub, task_id: str) -> None:
    wait_until(
        lambda: len(client.get("/v1/pull-requests", params={"taskId": task_id}).json()["pullRequests"]) == 1,
        timeout=90,
        message="checks-gated work did not publish a pull request",
    )
    assert len(fake_github.pulls) == 1


def _checks_section(body: str) -> str:
    """The checks account inside the PR body's marked verification section."""
    marked = body.split("<!-- dude:verification -->", 1)[1].split("<!-- /dude:verification -->", 1)[0]
    assert "### Checks\n" in marked, body
    return marked.split("### Checks\n", 1)[1].rstrip("\n")


def _rows(checks: str) -> tuple[str, list[tuple[str, str, list[tuple[str, str]]]]]:
    """The account's headline (to its blank line) and each gated Run's (role, status, [(command, result)]), in order.

    A check's judge reasoning and call id are the proxy's and are left out."""
    headline, _, rest = checks.partition("\n\n")
    rows: list[tuple[str, str, list[tuple[str, str]]]] = []
    for line in rest.split("\n"):
        run = re.fullmatch(r"- \*\*(\w+)\*\* — (.+)", line)
        check = re.match(r"  - `([^`]*)` — \*\*([^*]+)\*\*", line)
        if run:
            rows.append((run[1], run[2], []))
        else:
            assert check and rows, checks
            rows[-1][2].append((check[1], check[2]))
    return headline, rows

@pytest.mark.e2e
def test_backed_report_judged_yes_publishes_and_is_visible(client: ApiClient, fake_github: FakeGitHub):
    _judge(client, fake_github)
    task = _start(client, fake_github, CHECKS_MODELS["honest"])
    _wait_pr(client, fake_github, task["id"])

    run = _implementer(client, task["id"])
    verdicts = _checks_events(client, run["id"])
    assert verdicts and verdicts[-1]["payload"]["verified"] is True
    assert all(v["payload"].get("checks") for v in verdicts)
    activity = client.events(taskId=task["id"])
    assert any(e["eventType"] == "run.checks.judged" for e in activity)
    assert any("checks" in str(e["payload"]).lower() for e in activity)
    body = fake_github.pulls[1].body
    # The implementer's backed command is listed, not only the last Run's verdict.
    assert _rows(_checks_section(body)) == ("Checks were verified against the Run's recorded commands.", [
        ("Implementer", "backed", [("make check", "pass"), ("missing checks", "pass")]),
        ("Fixer", "backed", [("missing checks", "pass")]),
        ("Simplifier", "backed", [("missing checks", "pass")]),
    ])


@pytest.mark.e2e
def test_failing_command_is_sent_back_then_corrected_turn_publishes(client: ApiClient, fake_github: FakeGitHub):
    _judge(client, fake_github)
    task = _start(client, fake_github, CHECKS_MODELS["fixed"])
    wait_until(lambda: len(_checks_for_task(client, task["id"])) >= 2,
               timeout=90, message="checks report was not judged twice")
    _wait_pr(client, fake_github, task["id"])

    events = _checks_events(client, _implementer(client, task["id"])["id"])
    assert events[0]["payload"]["verified"] is False
    assert events[1]["payload"]["verified"] is True
    assert "make check" in str(events[0]["payload"])
    run_events = client.events(runId=_implementer(client, task["id"])["id"])
    assert any(e["eventType"] == "run.directive.delivered" for e in run_events)


@pytest.mark.e2e
def test_exhausted_rounds_escalates_and_reasoned_waive_publishes(client: ApiClient, fake_github: FakeGitHub):
    _judge(client, fake_github)
    assert client.patch("/v1/forge/settings", {"checksFixRounds": 1}).status_code == 200
    task = _start(client, fake_github, CHECKS_MODELS["unbacked"])
    wait_until(lambda: client.get(f"/v1/tasks/{task['id']}").json()["status"] == "awaiting_input",
               timeout=90, message="checks gate did not escalate")
    detail = client.get(f"/v1/tasks/{task['id']}").json()
    assert "checks_unverified" in str(detail)
    refused = client.post(f"/v1/tasks/{task['id']}/decide", {"action": "waive", "note": ""})
    assert refused.status_code == 400, refused.text
    accepted = client.post(f"/v1/tasks/{task['id']}/decide", {"action": "waive", "note": "CI hardware was unavailable; owner reviewed the risk."})
    assert accepted.status_code == 200, accepted.text
    _wait_pr(client, fake_github, task["id"])
    # Later backed fixer and simplifier reports do not hide the waiver or its reason.
    assert _rows(_checks_section(fake_github.pulls[1].body)) == (
        "Checks were waived by a person:\n- Implementer: CI hardware was unavailable; owner reviewed the risk.", [
            ("Implementer", "waived", []),
            ("Fixer", "backed", [("missing checks", "pass")]),
            ("Simplifier", "backed", [("missing checks", "pass")]),
        ])
    assert any(e["eventType"] == "run.checks.decided" and e["payload"].get("action") == "waive" for e in client.events(taskId=task["id"]))


@pytest.mark.e2e
def test_reported_preexisting_failure_passes_without_a_person(client: ApiClient, fake_github: FakeGitHub):
    # No send-back allowed: a failure the judge did not pass would ask a person at once.
    _judge(client, fake_github)
    assert client.patch("/v1/forge/settings", {"checksFixRounds": 0}).status_code == 200
    task = _start(client, fake_github, CHECKS_MODELS["base-shown"])
    _wait_pr(client, fake_github, task["id"])

    events = _checks_events(client, _implementer(client, task["id"])["id"])
    assert len(events) == 1 and events[0]["payload"]["verified"] is True, events
    first = events[0]["payload"]["checks"][0]
    assert first["command"] == "make test" and first["result"] == "pre-existing" and first["callId"], first
    activity = client.events(taskId=task["id"])
    assert not any(e["eventType"] == "question.asked" for e in activity)
    assert not any(e["eventType"] == "run.checks.decided" for e in activity)
    headline, rows = _rows(_checks_section(fake_github.pulls[1].body))
    assert "the judge passed failures that fail the same at the starting commit (pre-existing)" in headline, headline
    assert rows[0] == ("Implementer", "backed; the rest passed by the judge", [("make test", "pre-existing"), ("missing checks", "pass")])


@pytest.mark.e2e
@pytest.mark.ui
def test_no_judgement_tier_publishes_with_skip_advice(page: Page, web_url: str, client: ApiClient, fake_github: FakeGitHub):
    # Off is the fixture's default. Make that explicit so this remains true
    # when the suite's organization fixture gains a default tier later.
    assert client.put("/v1/judgements/settings", {"tierId": None}).status_code == 200
    task = _start(client, fake_github, CHECKS_MODELS["unbacked"])
    _wait_pr(client, fake_github, task["id"])
    run = _implementer(client, task["id"])
    events = _checks_events(client, run["id"])
    assert events and events[-1]["payload"].get("skipped") is True
    body = fake_github.pulls[1].body
    assert _rows(_checks_section(body)) == ("Checks not model-checked, set a judgement tier in the organisation's settings.", [
        ("Implementer", "not model-checked", []),
        ("Fixer", "not model-checked", []),
        ("Simplifier", "not model-checked", []),
    ])

    # The same outcome is visible in the actual task screen's Activity tab.
    sign_in(page, web_url, client.api_key, at=f"#/tasks/{task['id']}", board=False)
    activity = page.get_by_text(re.compile("not model-checked|checks", re.IGNORECASE))
    expect(activity.first).to_be_visible(timeout=30_000)
