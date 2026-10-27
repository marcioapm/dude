"""post_to_chat end to end: the deployed control plane, orchestrator, fake lux and fake Slack.

An organisation installs dude's Slack app (OAuth at the fake Slack). An
organisation agent automation whose scripted agent calls post_to_chat (its
prompt's "tool:" lines, fakeagent.AutomationScript) is run now: the message
reaches the fake Slack's #engineering as the bot, a post to a channel the bot
is not in is refused saying how to invite it, and the run's detail lists both
calls, the first with its channel and the link to the message. Without an
installation the same run fails on the call, saying Slack is not connected,
and the call shows why. (Redacting a secret value in the text is the
agenttools unit tests'.)
"""

from __future__ import annotations

import json
import os

import requests
from playwright.sync_api import Page, expect

from helpers import ApiClient, execute, provision_person, sign_in, wait_until

ENDED = ("succeeded", "failed", "cancelled", "skipped")


class _Slack:
    """The fake Slack, as this suite drives it: one workspace of its own."""

    def __init__(self, env, team: str):
        self.base = env.fake_slack_url
        self.team = team
        self._post("team", {"ID": team, "Name": f"Acme {team}"})

    def _post(self, what: str, body: dict) -> dict:
        resp = requests.post(f"{self.base}/_fake/{what}", json=body, timeout=10)
        assert resp.ok, resp.text
        return resp.json()

    def user(self, uid: str, name: str) -> None:
        self._post("user", {"ID": uid, "Team": self.team, "Name": name, "RealName": name})

    def channel(self, cid: str, name: str, bot: bool) -> None:
        self._post("conversation", {"ID": cid, "Team": self.team, "Name": name, "Kind": "public",
                                    "Members": [f"UB{self.team}"] if bot else []})

    def messages(self, channel: str) -> list[dict]:
        resp = requests.get(f"{self.base}/_fake/state", timeout=10)
        assert resp.ok, resp.text
        return [m for m in (resp.json()["messages"] or []) if m["Channel"] == channel]


def _install(client: ApiClient, slack: _Slack, installer: str) -> None:
    """An admin installs dude's Slack app: consent at the fake Slack, back through the callback."""
    slack._post("authorize-as", {"Installer": installer, "Signer": ""})
    headers = {"authorization": f"Bearer {client.api_key}", "content-type": "application/json"}
    started = requests.post(f"{client.base_url}/v1/chat/slack/install", json={}, headers=headers, timeout=30)
    assert started.status_code == 200, started.text
    cookie = started.headers["set-cookie"].split(";", 1)[0]
    consent = requests.get(started.json()["url"], allow_redirects=False, timeout=10)
    assert consent.status_code == 302, consent.text
    back = requests.get(consent.headers["location"], allow_redirects=False, timeout=30, headers={"cookie": cookie})
    assert back.status_code == 303 and "outcome=installed" in back.headers["location"], back.headers.get("location")


def _digest(client: ApiClient, prompt: str) -> dict:
    tier = client.tier_for("fake/scripted")
    created = client.post("/v1/automations", {"name": "Weekly PR digest", "kind": "agent", "trigger": "schedule"})
    assert created.status_code == 201, created.text
    a = created.json()
    saved = client.put(f"/v1/automations/{a['id']}", {"kind": "agent", "name": "Weekly PR digest",
                                                       "trigger": {"kind": "schedule", "cron": "0 15 * * 5", "tz": "Europe/Lisbon"},
                                                       "agent": {"own": {"tierId": tier, "prompt": prompt}}})
    assert saved.status_code == 200, saved.text
    return saved.json()


def _run_now(client: ApiClient, a: dict) -> dict:
    started = client.post(f"/v1/automations/{a['id']}/runs", {})
    assert started.status_code == 202, started.text
    run_id = started.json()["runId"]

    def ended():
        runs = client.get(f"/v1/automations/{a['id']}/runs").json()["runs"]
        return next((r for r in runs if r["id"] == run_id and r["status"] in ENDED), None)
    wait_until(ended, timeout=120, interval=1, message="the digest's run never ended")
    detail = client.get(f"/v1/automations/{a['id']}/runs/{run_id}")
    assert detail.status_code == 200, detail.text
    return detail.json()


def _post_line(channel: str, text: str) -> str:
    return "tool: post_to_chat " + json.dumps({"channel": channel, "text": text})


def test_a_post_reaches_the_channel_and_the_run_shows_it_with_its_link(client: ApiClient, env, org: dict, page: Page, web_url: str, console_errors: list):
    team = "T" + os.urandom(3).hex().upper()
    slack = _Slack(env, team)
    slack.user(f"U1{team}", "marcia")
    slack.channel(f"C{team}", "engineering", bot=True)
    slack.channel(f"R{team}", "random", bot=False)
    _, marcia = provision_person(client, env, "Márcia Costa", f"marcia-{os.urandom(2).hex()}@example.com", "admin")
    _install(marcia, slack, f"U1{team}")

    a = _digest(client, "\n".join([
        "Post the digest.",
        _post_line("#engineering", "*Weekly digest* :bar_chart: 12 merged"),
        _post_line("#random", "not here"),
    ]))
    run = _run_now(client, a)
    assert run["status"] == "succeeded", run

    sent = slack.messages(f"C{team}")
    assert [m["Text"] for m in sent] == ["*Weekly digest* :bar_chart: 12 merged"], sent
    assert sent[0]["ThreadTS"] == sent[0]["TS"], "a post starts its own thread"
    assert sent[0]["User"] == f"UB{team}" and sent[0]["Bot"], "the post is the workspace's dude bot's"
    assert slack.messages(f"R{team}") == []

    posts = [c for c in run["toolCalls"] if c["tool"] == "post_to_chat"]
    assert [(c["status"], c["args"].get("channel")) for c in posts] == [("completed", "#engineering"), ("failed", "#random")], posts
    done, refused = posts
    assert done["posted"]["channel"] == "#engineering", done
    link = done["posted"]["permalink"]
    assert link.startswith("https://") and f"/archives/C{team}/p{sent[0]['TS'].replace('.', '')}" in link, link
    assert "text" not in done["args"], done
    assert refused["error"].startswith("dude's Slack bot is not in #random: ask someone in the channel to run /invite @dude"), refused

    # The same calls, not a second log of dude's tools: harness call and completion in the run's activity.
    activity = client.get(f"/v1/events?automationRunId={run['id']}")
    assert activity.status_code == 200, activity.text
    events = activity.json()["events"]
    calls = [e for e in events if e["eventType"] == "agent.tool.called"]
    completions = [e for e in events if e["eventType"] == "agent.tool.completed"]
    assert len(calls) >= 2 and len(completions) >= 2, events
    posted = next(e for e in completions if e["payload"].get("dude", {}).get("posted"))
    assert posted["payload"]["callId"] in [e["payload"]["callId"] for e in calls]
    assert posted["payload"]["dude"]["posted"]["permalink"] == link
    assert all(e.get("automationRunId") == run["id"] and e["runId"] is None for e in events)

    sign_in(page, web_url, client.api_key, board=False)
    page.goto(f"{web_url}/#/org/settings/automations/{a['id']}/runs/{run['id']}")
    feed = page.get_by_test_id("run-feed")
    expect(feed).to_be_visible(timeout=30_000)
    expect(feed.locator('[data-tool="post_to_chat"]')).to_have_count(2)
    expect(feed.get_by_role("link", name="Open the message in Slack")).to_have_attribute("href", link)
    expect(page.get_by_test_id("run-tools")).to_have_count(0)


def test_without_slack_the_run_fails_on_the_call_and_says_so(client: ApiClient, env, org: dict):
    execute(env.owner_dsn, "UPDATE organizations SET name = 'Acme' WHERE id = %s", (org["id"],))
    a = _digest(client, "\n".join(["Post the digest.", _post_line("#engineering", "*Weekly digest*")]))
    run = _run_now(client, a)
    assert run["status"] == "failed", run
    assert run["reason"] == "post_to_chat failed: Slack is not connected for Acme", run
    [call] = [c for c in run["toolCalls"] if c["tool"] == "post_to_chat"]
    assert call["status"] == "failed" and call["posted"] is None, call
    assert call["error"].startswith("Slack is not connected for Acme: an organisation admin adds dude's Slack app"), call
    assert run["failedCallId"] == call["id"], run
