"""The conductor's decisions, end to end through the deployed processes.

The scripted conductor (fakeagent) carries out the tool calls a message
names — a line "tool: NAME {json}" — as a real conductor would decide to
from what the person said: so the test talks to it in Chat, and it
decides. Between its decisions the delivery does the mechanics and wakes
it with a note.

Talk it through: a task not started gets a delivery its conductor decides,
waiting on the start; the person and the conductor agree a criterion,
written into the task; the conductor starts the implementer, then a
review, triages, and before the pull request asks the gate's question;
the person answers Open, and the pull request opens. Taking over: the
first message in a delivered task's Chat hands its next decision to the
conductor, which the pull request's feedback then waits on. Parked: with
the default warm period the conductor parks between decisions, and each
resumed conductor's decision still lands.
"""

from __future__ import annotations

import json

from fake_github import FakeGitHub
from helpers import ApiClient, finish_simplifier_review, query, wait_until


def _conducting(client: ApiClient, forge_project: dict, model: str = "fake/scripted") -> dict:
    """The project's conductor on model, warm. fake/scripted decides only
    what a message names; a delivery Deliver starts, quiet, needs one that
    takes Deliver's defaults (fake/defaults), or it waits out each decision."""
    models = {**forge_project["agentModels"], **client.on_models({"conductor": model})}
    resp = client.patch(f"/v1/projects/{forge_project['id']}", {"agentModels": models})
    assert resp.status_code == 200, resp.text
    # Warm throughout: each decision reaches a live conductor.
    assert client.patch(f"/v1/projects/{forge_project['id']}/settings",
                        {"delivery": {"conductorWarmMinutes": 30}}).status_code == 200
    return resp.json()


def _tool(name: str, args: dict | None = None) -> str:
    return f"tool: {name} {json.dumps(args or {})}"


def _say(client: ApiClient, task_id: str, *lines: str) -> dict:
    resp = client.post(f"/v1/tasks/{task_id}/chat", {"text": "\n".join(lines)})
    assert resp.status_code in (200, 201), resp.text
    return resp.json()


def _task(client: ApiClient, task_id: str) -> dict:
    return client.get(f"/v1/tasks/{task_id}").json()


def _waiting_on(client: ApiClient, task_id: str, point: str, message: str) -> None:
    wait_until(lambda: (_task(client, task_id)["awaitingDecision"] or {}).get("point") == point,
               timeout=90, message=message)


def _decisions(client: ApiClient, task_id: str) -> list[tuple[str, str]]:
    """The conductor's decisions on the task, oldest first: point and action."""
    return [(e["payload"]["point"], e["payload"]["action"]) for e in client.events(taskId=task_id, limit=1000)
            if e["eventType"] == "conductor.decided"]


def _phases(client: ApiClient, task_id: str, phase: str) -> list[dict]:
    return [r for r in client.task_runs(task_id) if r["phase"] == phase]


def _woken(client: ApiClient, task_id: str) -> list[str]:
    return [e["payload"]["text"] for e in client.events(taskId=task_id, limit=1000) if e["eventType"] == "conductor.woken"]


def _said(client: ApiClient, run_id: str) -> list[str]:
    return [e["payload"]["text"] for e in client.events(runId=run_id, limit=1000) if e["eventType"] == "agent.message"]


def test_talk_it_through_to_a_pull_request(client: ApiClient, forge_project: dict, fake_github: FakeGitHub, owner_dsn: str):
    project = _conducting(client, forge_project)
    task = client.create_task(project["id"], "Greet in Portuguese")

    # Talk it through: the conductor, a delivery it decides, and nothing built.
    resp = client.post(f"/v1/tasks/{task['id']}/talk")
    assert resp.status_code == 201, resp.text
    conductor = resp.json()["runId"]
    assert resp.json()["decider"] == "conductor"
    _waiting_on(client, task["id"], "start", "the delivery never waited on the conductor to start")
    assert _task(client, task["id"])["decider"] == "conductor"
    assert [r for r in client.task_runs(task["id"]) if r["phase"]] == []

    # Planned: the agreed criterion is written into the task, then the implementer starts.
    _say(client, task["id"], "Agreed: it says olá.",
         _tool("update_task", {"acceptanceCriteria": ["it says olá"]}),
         _tool("start_phase", {"phase": "implement", "note": "one file"}))
    wait_until(lambda: _phases(client, task["id"], "implement"), timeout=60, message="the implementer never started")
    assert _task(client, task["id"])["acceptanceCriteria"] == ["it says olá"]
    implementer = _phases(client, task["id"], "implement")[0]
    assert implementer["conductorRunId"] == conductor

    # After implement: dude wakes the conductor with a bounded note; it asks for a review.
    _waiting_on(client, task["id"], "after_implement", "the delivery never came back after the implementer")
    wait_until(lambda: any("after implement" in n for n in _woken(client, task["id"])), timeout=60,
               message="the conductor was never woken after implement")
    note = next(n for n in _woken(client, task["id"]) if "after implement" in n)
    assert implementer["id"] in note and "FACTORY.md" not in note, note
    _say(client, task["id"], "Review it.", _tool("decide", {"action": "next"}))

    # A review round: the conductor fixes the finding, and has it reviewed again.
    _waiting_on(client, task["id"], "after_review", "the review round never came back to the conductor")
    _say(client, task["id"], "Fix it.", _tool("start_phase", {"phase": "fix"}))
    _waiting_on(client, task["id"], "after_fix", "the fix never came back to the conductor")
    _say(client, task["id"], "Again.", _tool("start_phase", {"phase": "review", "categories": ["correctness"]}))
    wait_until(lambda: len(_phases(client, task["id"], "review")) == 2, timeout=60, message="no second review")
    _waiting_on(client, task["id"], "after_review", "the clean round never came back to the conductor")
    _say(client, task["id"], "Clean; go on.", _tool("decide", {"action": "next"}))
    # The simplifier's diff is reviewed whoever decides; the conductor triages that round too.
    narrow = finish_simplifier_review(client, owner_dsn, task["id"])

    # Before the pull request: refused without the person's word; the gate's question; Open.
    _waiting_on(client, task["id"], "before_pull_request", "the delivery never waited before the pull request")
    _say(client, task["id"], "Open it.", _tool("decide", {"action": "open_pull_request"}))
    # Refused (the fake says the tool answered 422): the delivery still waits, and nothing opened.
    wait_until(lambda: any("dude decide answered 422" in t for t in _said(client, conductor)), timeout=60,
               message="the refused open never came back")
    assert (_task(client, task["id"])["awaitingDecision"] or {}).get("point") == "before_pull_request"
    assert client.get("/v1/pull-requests", params={"taskId": task["id"]}).json()["pullRequests"] == []
    _say(client, task["id"], "Ask me.", _tool("decide", {"action": "ask_person", "note": "Reviewed clean."}))
    question = wait_until(
        lambda: query(owner_dsn, "SELECT id, options FROM questions WHERE task_id = %s AND pr_gate_heads IS NOT NULL", (task["id"],)),
        timeout=60, message="the gate's question was never asked")[0]
    assert question["options"] == ["Open", "Draft", "Show me the diff", "Another round"]
    assert _say(client, task["id"], "Open")["questionId"] == question["id"]
    _say(client, task["id"], "Opening.", _tool("decide", {"action": "open_pull_request"}))
    prs = wait_until(lambda: client.get("/v1/pull-requests", params={"taskId": task["id"]}).json()["pullRequests"],
                     timeout=90, message="the pull request never opened")
    assert len(prs) == 1
    wait_until(lambda: _task(client, task["id"])["status"] == "review", timeout=60, message="the task never went to review")
    # Every phase Run was the conductor's, except the review of the
    # simplifier's diff, which the workflow runs whoever decides; and each decision was recorded.
    runs = client.task_runs(task["id"])
    assert all(r["conductorRunId"] == conductor for r in runs if r["phase"] and r["id"] not in narrow)
    assert [r for r in runs if r["id"] in narrow and r["reviewOf"] == "simplify" and r["conductorRunId"] is None], runs
    decided = [e["payload"]["action"] for e in client.events(taskId=task["id"], limit=1000) if e["eventType"] == "conductor.decided"]
    assert decided == ["start_phase", "next", "start_phase", "start_phase", "next", "next", "open_pull_request"], decided


def _parked(client: ApiClient, run_id: str, message: str) -> None:
    wait_until(lambda: (r := client.get_run(run_id))["status"] == "paused" and r["dudePause"] == "conductor",
               timeout=60, message=message)


def test_a_conductor_parked_between_decisions_still_decides(client: ApiClient, forge_project: dict, fake_github: FakeGitHub,
                                                            owner_dsn: str):
    """The default warm period, the suite's seconds (DUDE_CONDUCTOR_WARM):
    the conductor parks after each turn, and each decision is made by a
    resumed conductor. Every start of its Run has a new tools token, which
    its tool calls must carry."""
    models = {**forge_project["agentModels"], **client.on_models({"conductor": "fake/scripted"})}
    assert client.patch(f"/v1/projects/{forge_project['id']}", {"agentModels": models}).status_code == 200
    task = client.create_task(forge_project["id"], "Greet in Portuguese, decided by a parked conductor")
    conductor = client.post(f"/v1/tasks/{task['id']}/talk").json()["runId"]
    _waiting_on(client, task["id"], "start", "the delivery never waited on the conductor to start")
    _parked(client, conductor, "the conductor never parked before its first decision")

    _say(client, task["id"], "Go.", _tool("start_phase", {"phase": "implement"}))
    wait_until(lambda: _phases(client, task["id"], "implement"), timeout=60,
               message=f"the resumed conductor's start never landed: {_said(client, conductor)}")
    _waiting_on(client, task["id"], "after_implement", "the implementer never came back")
    # The wake resumed it; past its turn it parks again before the next decision.
    wait_until(lambda: any("after implement" in n for n in _woken(client, task["id"])), timeout=60,
               message="the conductor was never woken after implement")
    _parked(client, conductor, "the conductor never parked again after its wake")

    _say(client, task["id"], "Review it.", _tool("decide", {"action": "next"}))
    wait_until(lambda: _phases(client, task["id"], "review"), timeout=60,
               message=f"the twice-resumed conductor's decision never landed: {_said(client, conductor)}")
    assert not [t for t in _said(client, conductor) if "answered 401" in t], _said(client, conductor)
    decided = [e["payload"]["action"] for e in client.events(taskId=task["id"], limit=1000) if e["eventType"] == "conductor.decided"]
    assert decided == ["start_phase", "next"], decided
    # Both decisions were made by a resumed conductor, each start with its own token.
    assert len(query(owner_dsn, "SELECT 1 FROM run_resumes WHERE run_id = %s", (conductor,))) >= 2


def test_taking_over_a_delivered_task(client: ApiClient, forge_project: dict, fake_github: FakeGitHub):
    project = _conducting(client, forge_project, "fake/defaults")
    task = client.create_task(project["id"], "Respond to review, with the conductor")
    assert client.post(f"/v1/tasks/{task['id']}/deliver").status_code == 201
    pr = wait_until(lambda: client.get("/v1/pull-requests", params={"taskId": task["id"]}).json()["pullRequests"],
                    timeout=90, message="the delivery never opened a pull request")[0]
    wait_until(lambda: _task(client, task["id"])["status"] == "review", timeout=60, message="never in review")
    # Every delivery has a conductor: Deliver's decides quietly.
    started = _task(client, task["id"])
    assert (started["decider"], started["voice"]) == ("conductor", "quiet"), started
    # Quiet, it took Deliver's defaults at each decision it was woken for.
    quietly = _decisions(client, task["id"])
    assert quietly and all(action == "next" for _, action in quietly), quietly

    # The first message takes it over: the conductor talks it through now.
    resp = _say(client, task["id"], "I'll steer from here.")
    assert resp["decider"] == "conductor"
    assert _task(client, task["id"])["voice"] == "chatty"
    changes = [e["payload"] for e in client.events(taskId=task["id"], limit=1000) if e["eventType"] == "task.decider_changed"]
    assert changes == [{"from": None, "to": "conductor", "voice": "quiet", "why": "deliver"},
                       {"from": "conductor", "to": "conductor", "voice": "chatty", "why": "a person wrote in Chat"}], changes

    # Feedback now waits on the conductor: no fixer until it decides.
    before = {r["id"] for r in _phases(client, task["id"], "fix")}
    fixes = len(before)
    fake_github.comment(pr["number"], "Please also note the date in FIXED.md.", path="FIXED.md")
    _waiting_on(client, task["id"], "pull_request_feedback", "the feedback never waited on the conductor")
    assert len(_phases(client, task["id"], "fix")) == fixes
    wait_until(lambda: any("pull request feedback" in n for n in _woken(client, task["id"])), timeout=60,
               message="the conductor was never woken for the feedback")
    _say(client, task["id"], "Fix it.", _tool("start_phase", {"phase": "fix"}))
    wait_until(lambda: len(_phases(client, task["id"], "fix")) == fixes + 1, timeout=60, message="the conductor's fix never ran")
    # The new fixer is the conductor's, and its decision was recorded.
    conducted = [r for r in _phases(client, task["id"], "fix") if r["id"] not in before]
    assert len(conducted) == 1 and conducted[0]["conductorRunId"] == resp["runId"], conducted
    decisions = _decisions(client, task["id"])[len(quietly):]
    assert decisions == [("pull_request_feedback", "start_phase")], decisions
    before |= {conducted[0]["id"]}

    # Let it finish quietly: the conductor still decides, quiet, and is told so;
    # the next feedback is still its decision, and a later message keeps it quiet.
    wait_until(lambda: _task(client, task["id"])["status"] == "review", timeout=60, message="not back in review")
    assert client.post(f"/v1/tasks/{task['id']}/decider", {"decider": "policy"}).status_code == 200
    after = _task(client, task["id"])
    assert (after["decider"], after["voice"], after["handedBack"]) == ("conductor", "quiet", True), after
    changes = [e["payload"] for e in client.events(taskId=task["id"], limit=1000) if e["eventType"] == "task.decider_changed"]
    assert changes[-1] == {"from": "conductor", "to": "conductor", "voice": "quiet", "why": "a person let it finish quietly"}, changes
    fake_github.comment(pr["number"], "And the time, please.", path="FIXED.md")
    _waiting_on(client, task["id"], "pull_request_feedback", "the feedback never waited on the quiet conductor")
    assert len(_phases(client, task["id"], "fix")) == fixes + 1
    wait_until(lambda: any("This delivery is quiet now" in n for n in _woken(client, task["id"])), timeout=60,
               message="the conductor was never told it is quiet")
    _say(client, task["id"], "Fix it too.", _tool("start_phase", {"phase": "fix"}))
    wait_until(lambda: len(_phases(client, task["id"], "fix")) == fixes + 2, timeout=60,
               message="the quiet conductor's fix never ran")
    quiet_fix = [r for r in _phases(client, task["id"], "fix") if r["id"] not in before]
    assert len(quiet_fix) == 1 and quiet_fix[0]["conductorRunId"] == resp["runId"], quiet_fix
    assert _task(client, task["id"])["voice"] == "quiet"


def test_your_call_on_an_escalation_lets_the_conductor_retry(client: ApiClient, forge_project: dict, fake_github: FakeGitHub,
                                                            owner_dsn: str):
    """Legacy policy/person-held stuck escalation handed to the conductor:
    it retries with its own direction and no prior owner answer. The initial
    no-model setup is intentional coverage of live legacy person holders."""
    project = _conducting(client, forge_project)
    # The conductor on a tier with no model (the organisation's Fast, unset):
    # Deliver starts the delivery on Deliver's policy, as before every
    # delivery had a conductor, and its bounds escalate the stuck review.
    fast = {t["name"]: t for t in client.get("/v1/models/tiers").json()["tiers"]}["Fast"]
    assert not fast["model"], fast
    models = {**project["agentModels"], **client.on_models({"reviewer": "fake/stuck"}), "conductor": {"tier": fast["id"]}}
    assert client.patch(f"/v1/projects/{project['id']}", {"agentModels": models}).status_code == 200
    assert client.patch(f"/v1/projects/{project['id']}/settings",
                        {"delivery": {"maxAttemptsPerFinding": 1}}).status_code == 200
    task = client.create_task(project["id"], "Greet, and get stuck on review")
    assert client.post(f"/v1/tasks/{task['id']}/deliver").status_code == 201
    started = _task(client, task["id"])
    assert (started["decider"], started["voice"]) == ("policy", None), started
    changes = [e["payload"] for e in client.events(taskId=task["id"], limit=1000) if e["eventType"] == "task.decider_changed"]
    assert changes == [{"from": None, "to": "policy", "voice": "quiet", "why": "the conductor has no model"}], changes
    # The conductor back on the scripted model, for the person's Chat.
    models = {**models, **client.on_models({"conductor": "fake/scripted"})}
    assert client.patch(f"/v1/projects/{project['id']}", {"agentModels": models}).status_code == 200
    wait_until(lambda: (_task(client, task["id"]).get("escalation") or {}).get("reason") == "stuck", timeout=180,
               message="Deliver's review never got stuck")
    assert _task(client, task["id"])["decider"] == "policy"

    # The person's first message hands the waiting delivery to the conductor.
    resp = _say(client, task["id"], "What happened?")
    assert resp["decider"] == "conductor", resp
    conductor = resp["runId"]
    assert _task(client, task["id"])["decider"] == "conductor"

    # Stuck: escalated to a person, the conductor told how it works.
    wait_until(lambda: (_task(client, task["id"]).get("escalation") or {}).get("reason") == "stuck", timeout=90,
               message="the review never got stuck")
    wait_until(lambda: any("Escalation: stuck" in n and "decide_escalation" in n for n in _woken(client, task["id"])),
               timeout=60, message="the conductor was never told of the escalation")
    # Retry is delegated: no question or owner answer is needed, even for
    # this legacy person-held escalation handed to the conductor.
    fixes = len(_phases(client, task["id"], "fix"))
    decided_before = len([e for e in client.events(taskId=task["id"], limit=1000) if e["eventType"] == "task.decided"])
    _say(client, task["id"], "Retry.", _tool("decide_escalation", {"action": "retry", "note": "One narrow round."}))
    wait_until(lambda: len(_phases(client, task["id"], "fix")) == fixes + 1, timeout=90,
               message="the conductor's retry never started the next fix round")
    decided = [e for e in client.events(taskId=task["id"], limit=1000) if e["eventType"] == "task.decided"][decided_before:]
    assert len(decided) == 1 and decided[0]["payload"]["by"] == "conductor", decided
    assert "questionId" not in decided[0]["payload"] and decided[0]["actor"]["id"] == conductor, decided
    assert not query(owner_dsn, "SELECT id FROM questions WHERE task_id=%s AND escalation IS NOT NULL", (task["id"],))
    # The retry's fix goes back to the conductor, at its next decision.
    _waiting_on(client, task["id"], "after_fix", "the retry's fix never came back to the conductor")


def test_a_checks_escalation_of_the_conductors_own_agent_returns_to_its_decision(
        client: ApiClient, forge_project: dict, fake_github: FakeGitHub, owner_dsn: str):
    """While the delivery waits on the conductor after the implementer, an
    implementer the conductor started itself (start_agent) is held at the
    checks gate and escalates; the person waives it, and the conductor's next
    decision is carried out: the review runs, and nothing is refused as taken
    already."""
    project = _conducting(client, forge_project)
    models = {**project["agentModels"], **client.on_models({"implementer": "fake/checks-unbacked"})}
    assert client.patch(f"/v1/projects/{project['id']}", {"agentModels": models}).status_code == 200
    assert client.put("/v1/judgements/settings", {"tierId": client.tier_for("judge-e2e")}).status_code == 200
    assert client.patch("/v1/forge/settings", {"checksFixRounds": 1}).status_code == 200
    task = client.create_task(project["id"], "Greet, with a helper the checks gate holds")
    conductor = client.post(f"/v1/tasks/{task['id']}/talk").json()["runId"]
    _waiting_on(client, task["id"], "start", "the delivery never waited on the conductor to start")
    # The delivery's own implementer is held too: waived, it waits on the conductor after it.
    _say(client, task["id"], "Test script decides checks explicitly.")
    _say(client, task["id"], "Go.", _tool("start_phase", {"phase": "implement"}))
    _waived(client, task["id"], "the delivery's implementer", owner_dsn)
    _waiting_on(client, task["id"], "after_implement", "the implementer never came back")

    # The conductor's own implementer, which the delivery does not wait on, is held and escalates.
    _say(client, task["id"], "Have a helper write it.", _tool("start_agent", {"profile": "implementer", "key": "helper"}))
    child = wait_until(lambda: query(owner_dsn, "SELECT id FROM runs WHERE task_id = %s AND source_kind = 'task_conductor'",
                                     (task["id"],)), timeout=60, message="the conductor's implementer never started")[0]["id"]
    _waived(client, task["id"], "the conductor's implementer", owner_dsn)
    wait_until(lambda: client.get_run(child)["status"] == "completed", timeout=120, message="the waived helper never finished")
    _waiting_on(client, task["id"], "after_implement", "the delivery did not go back to the conductor's decision")

    reviews = len(_phases(client, task["id"], "review"))
    _say(client, task["id"], "Review it.", _tool("decide", {"action": "next"}))
    wait_until(lambda: len(_phases(client, task["id"], "review")) > reviews, timeout=90,
               message=f"the conductor's decision after the escalation never ran: {_said(client, conductor)}")
    assert not [t for t in _said(client, conductor) if "taken already" in t], _said(client, conductor)
    _waiting_on(client, task["id"], "after_review", "the review never came back to the conductor")
    decided = [e["payload"]["action"] for e in client.events(taskId=task["id"], limit=1000) if e["eventType"] == "conductor.decided"]
    assert decided == ["start_phase", "next"], decided


def _waived(client: ApiClient, task_id: str, what: str, owner_dsn: str) -> None:
    """Waits for the checks gate to escalate and waives it, as the task's owner.

    The checks gate asks the conductor about each report (run.checks.asked);
    fake/scripted decides only what a message names, so the person's Chat
    has it decide each one through decide_checks: the reported pass is not
    backed by the recorded calls (make check exited 1)."""
    told: set[tuple[str, int]] = set()

    def escalated():
        for ask in query(owner_dsn, """SELECT a.run_id, a.payload FROM events a WHERE a.task_id = %s
                         AND a.event_type = 'run.checks.asked' AND NOT EXISTS (SELECT 1 FROM events c
                           WHERE c.run_id = a.run_id AND c.event_type = 'run.checks.conducted'
                           AND c.payload->>'turn' = a.payload->>'turn')""", (task_id,)):
            key = (ask["run_id"], int(ask["payload"]["turn"]))
            if key in told:
                continue
            told.add(key)
            checks = [{"command": c["command"], "verdict": "unbacked" if c["result"] == "pass" else "reject",
                       "reason": "the recorded call of it exited 1"} for c in ask["payload"]["checks"]]
            _say(client, task_id, "Decide the checks report.", _tool("decide_checks", {
                "runId": key[0], "turn": key[1], "checks": checks, "missing": {"verdict": "none", "reason": "nothing obvious"}}))
        return query(owner_dsn, """SELECT id FROM workflow_runs WHERE task_id=%s
               AND status='waiting' AND step='decide' AND state->'escalation'->>'reason'='checks_unverified'
               AND NOT state->'escalation' ? 'decided'""", (task_id,))

    wait_until(escalated, timeout=90, message=f"the checks gate never reached the conductor for {what}")
    resp = client.post(f"/v1/tasks/{task_id}/decide", {"action": "waive", "note": "CI runs the check; the owner reviewed it."})
    assert resp.status_code == 200, resp.text
    wait_until(lambda: _task(client, task_id).get("escalation") is None, timeout=60,
               message=f"the waiver of {what} was never carried out")
