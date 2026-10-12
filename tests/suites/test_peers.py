"""Two agents on one epic coordinating, end to end through the deployed
backend, orchestrator, tool server and fake lux.

Both tasks' implementers play fakeagent's fake/peer. B is delivered first
and, with no peer live, hangs answering what it reads. A finds B live, so
it messages B and runs wait_for_reply on that message. B reads the
question at its next step and replies to it ({{inbox.last}}). The reply
resumes A, which completes. Through the public API the thread is
answered, its wait closed by the reply, and the project page's epic card
lists no one waiting.
"""

from __future__ import annotations

from helpers import ApiClient, wait_until

QUESTION = "Which body does your route take?"
ANSWER = "It takes {metric, from, period}."


def test_a_question_waited_on_is_answered_and_the_asker_completes(client: ApiClient, forge_project: dict):
    pid = forge_project["id"]
    assert client.patch(f"/v1/projects/{pid}", {"agentModels": client.on_models({
        "implementer": "fake/peer", "reviewer": "fake/hang", "simplifier": "fake/scripted"})}).status_code == 200
    epic = client.post(f"/v1/projects/{pid}/epics", {"title": "Metric preview"}).json()
    a = client.create_task(pid, "Frontend: preview panel", epicId=epic["id"])
    b = client.create_task(pid, "Backend: preview endpoints", epicId=epic["id"])

    assert client.post(f"/v1/tasks/{b['id']}/deliver").status_code == 201
    wait_until(lambda: any(r["phase"] == "implement" and r["status"] == "running" for r in client.task_runs(b["id"])),
               timeout=60, message="B's implementer never ran")
    assert client.post(f"/v1/tasks/{a['id']}/deliver").status_code == 201
    asker = wait_until(lambda: next((r for r in client.task_runs(a["id"]) if r["phase"] == "implement"), None),
                       timeout=60, message="A's implementer never started")

    wait_until(lambda: client.get_run(asker["id"])["status"] == "completed", timeout=120,
               message="A's implementer never completed after the reply")

    # A resumed with B's reply as its input: it read it there.
    received = [e["payload"] for e in client.events(runId=asker["id"], limit=1000) if e["eventType"] == "agent.message.received"]
    assert len(received) == 1, received

    threads = client.get(f"/v1/epics/{epic['id']}/threads").json()
    assert threads["waiting"] == 0
    [thread] = threads["threads"]
    assert thread["state"] == "answered", thread
    assert [m["text"] for m in thread["messages"]] == [QUESTION, ANSWER]
    assert thread["pair"][0]["taskId"] == a["id"] and thread["pair"][1]["taskId"] == b["id"]
    assert thread["wait"]["how"] == "wait" and thread["wait"]["closedBy"] == "reply", thread["wait"]
    assert thread["messages"][1]["readAt"] is not None

    assert client.get(f"/v1/epics/{epic['id']}/waiting").json()["waiting"] == 0
    overview = client.get(f"/v1/projects/{pid}/overview").json()
    card = next(e for e in overview["epics"] if e["id"] == epic["id"])
    assert card["waiting"] == [], card
