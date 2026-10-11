"""A talker's new agent is told the conversation so far, end to end against a real lux.

A talker is a brainstorm (a session's agent) or a task's conductor. An
agent that starts without its harness transcript — a new Run after lux
terminated the last, or a resume lux could only give a blank session — is
briefed with "## The conversation so far": what people said and what the
agent before it answered, from dude's ledger, oldest first, from the
newest compaction summary on when there is one.

With lux-fake as the agent, the prompt lux receives for a new Run is
fakeagent's script, written from dude's briefing (phases.buildSpec), not
the briefing itself: those tests read the briefing from the Run's row
(runs.prompt, which session.briefed / conductor.briefed record), and check
that lux received the script that answers that briefing's message. A
re-brief is a directive lux takes as written, so its text is read from
lux's own lux.input record. The real-model tests read the first prompt
from lux's record of it, and ask the agent what it was told.

Opt-in with `run_tests.py --lux`; the real-model tests also need
DUDE_LLM_KEY and DUDE_LLM_URL, and DUDE_TEST_RUNTIME_IMAGE preloaded on
lux's hosts. The fixtures, lux helpers and real-model setup are
test_lux_restart_contract's.
"""

from __future__ import annotations

import time

import pytest

from helpers import ApiClient, query, wait_until
from suites.test_lux_restart_contract import (  # noqa: F401  (fixtures: lux_project, _terminated_after)
    ASK_WORD, KINDS, WORD, Talker, _answers_word, _compacted, _first_line, _log, _lose_transcript, _lux, _lux_run,
    _real_brainstorm, _real_conductor, _records, _started, _talker, _terminated_after, _turn_over, kill_container,
    lux_project, needs_model)

pytestmark = pytest.mark.lux

REPLAY = "## The conversation so far"
SUMMARY = "## Earlier, as the agent summarised it"
SINCE = "## Since then"
TOOK_OVER = "[A new agent took over here.]"


def _prompt(t: Talker, run: str) -> str:
    """The briefing dude started the Run with (runs.prompt)."""
    return query(t.env.owner_dsn, "SELECT prompt FROM runs WHERE id = %s", (run,))[0]["prompt"]


def _briefed(t: Talker, run: str) -> list[str]:
    ev = "session.briefed" if t.kind == "brainstorm" else "conductor.briefed"
    return [e["payload"].get("text", "") for e in t.run_events(run) if e["eventType"] == ev]


def _message_at(t: Talker, prompt: str) -> int:
    """Where the briefing's message section's heading begins."""
    if t.kind == "brainstorm":
        return prompt.rfind("## The first message")
    message = prompt.rfind("'s message\n\n")
    return -1 if message < 0 else prompt.rfind("\n\n## ", 0, message) + 2


def _replay(t: Talker, prompt: str) -> str:
    """The briefing's conversation so far: from its heading to the message."""
    start, end = prompt.find(REPLAY), _message_at(t, prompt)
    assert start >= 0, f"no {REPLAY!r} in:\n{prompt}"
    assert end > start, f"the conversation is not before the message:\n{prompt}"
    return prompt[start:end]


def _paragraphs(replay: str) -> list[str]:
    return [p.strip() for p in replay.split("\n\n") if p.strip()]


def _person_line(paragraphs: list[str], text: str) -> list[int]:
    """The paragraphs that are a person saying text (not the agent quoting it)."""
    return [i for i, p in enumerate(paragraphs) if p.endswith(": " + text) and not p.startswith("You")]


def _agent_line(paragraphs: list[str], answer: str) -> int:
    """Where the agent's answer is (paragraphs are stripped, as the answer is here)."""
    want = "You: " + answer.strip()
    assert want in paragraphs, (want, paragraphs)
    return paragraphs.index(want)


def _lux_prompt(env, lux_id: str) -> str:
    """The first prompt lux handed the agent: its lux.input record for request
    id `prompt` (the text up to lux's 64 KiB), else the stored spec's."""
    def found():
        for r in _records(env, lux_id):
            if r["type"] == "lux.input" and r["data"].get("requestId") == "prompt" and r["data"].get("phase") == "accepted":
                assert not r["data"].get("truncated"), "lux cut the first prompt's record"
                return r["data"].get("text")
        return None
    return wait_until(found, timeout=120, interval=1, message=f"lux never recorded {lux_id}'s first prompt")


def _terminate(t: Talker, env, run: str) -> str:
    """lux ends the talker's Run for good; dude ends it (completed). Its lux id."""
    lux_id = t.lux_id(run)
    res = _lux(env, "POST", f"/v1/runs/{lux_id}/terminate")
    assert res.status_code in (200, 202), res.text
    wait_until(lambda: _lux_run(env, lux_id)["state"] == "terminated", timeout=120, interval=1, message="lux never terminated it")
    wait_until(lambda: t.row(run)["status"] == "completed", timeout=120, interval=1,
               message=f"the terminated talker was never ended: {t.row(run)}")
    return lux_id


def _next_run(t: Talker, env, run: str, text: str) -> str:
    """run terminated, text starts a new Run, which answers it."""
    _terminate(t, env, run)
    nxt = t.say(text)
    assert nxt["status"] == 201 and nxt.get("created") and nxt["runId"] != run, nxt
    t.answered(nxt["runId"], text)
    return nxt["runId"]


def _received_the_script_for(t: Talker, env, run: str, text: str) -> str:
    """lux received, as the new Run's first prompt, fakeagent's script
    answering text: the script written from the briefing dude recorded."""
    received = _lux_prompt(env, t.lux_id(run))
    assert received.startswith("echo ") and "You asked: " in received and text in received, received
    assert REPLAY not in received, received
    return received


def _delivered(client: ApiClient, task: dict) -> list[dict]:
    """The task delivered by the policy to its pull request: its phase Runs."""
    assert client.post(f"/v1/tasks/{task['id']}/deliver").status_code == 201
    wait_until(lambda: client.get("/v1/pull-requests", params={"taskId": task["id"]}).json()["pullRequests"],
               timeout=600, interval=2, message=f"no pull request: {[(r['phase'], r['status']) for r in client.task_runs(task['id'])]}")
    phases = [r for r in client.task_runs(task["id"]) if r["phase"]]
    assert phases and all(r["status"] == "completed" for r in phases), phases
    return phases


# ---------------------------------------------------------------------------
# 1. Terminated: the new Run is briefed with the conversation so far
# ---------------------------------------------------------------------------


@pytest.mark.timeout(1200)
@pytest.mark.parametrize("kind", KINDS)
def test_replay_a_terminated_talkers_new_run_is_briefed_with_the_conversation_so_far(
        client: ApiClient, env, lux_project, kind: str):
    started = time.time()
    t = _talker(client, env, kind, lux_project)
    phase_said: list[str] = []
    if t.task:
        # A phase Run's words are the task's, not its conductor's conversation.
        for r in _delivered(client, t.task):
            phase_said += [s for s in t.said(r["id"]) if s.strip()]
        assert phase_said
    run = _started(t)
    assert REPLAY not in _prompt(t, run), _prompt(t, run)
    answer = t.said(run)[0]
    nxt = _next_run(t, env, run, "again, after it ended")

    prompt = _prompt(t, nxt)
    replay = _replay(t, prompt)
    paragraphs = _paragraphs(replay)
    said, answered = _person_line(paragraphs, "hello there, first message"), _agent_line(paragraphs, answer)
    _log(f"r1/{kind}", {"run": run, "luxRunId": t.lux_id(run), "next": nxt, "nextLux": t.lux_id(nxt),
                        "replay": replay, "luxReceived": _lux_prompt(env, t.lux_id(nxt)), "seconds": round(time.time() - started)})
    assert len(said) == 1 and said[0] < answered, paragraphs
    assert "again, after it ended" not in replay, replay
    assert prompt.count("again, after it ended") == 1 and prompt.find("again, after it ended") > _message_at(t, prompt), prompt
    for s in phase_said:
        assert s not in replay, (s, replay)
    assert _briefed(t, nxt) == [prompt]
    _received_the_script_for(t, env, nxt, "again, after it ended")
    assert t.runs_created() == [run, nxt]


# ---------------------------------------------------------------------------
# 2. A resume in a new harness session: the re-brief carries the conversation
# ---------------------------------------------------------------------------


@pytest.mark.timeout(600)
@pytest.mark.parametrize("kind", KINDS)
def test_replay_a_talker_resumed_in_a_new_session_is_briefed_again_with_the_conversation_so_far(
        client: ApiClient, env, lux_project, kind: str):
    started = time.time()
    t = _talker(client, env, kind, lux_project)
    run = _started(t)
    answer = t.said(run)[0]
    lux_id = t.lux_id(run)
    _lose_transcript(env, lux_id)
    kill_container(env, lux_id)
    wait_until(lambda: t.row(run)["status"] == "paused", timeout=120, interval=1, message="never parked")
    msg = t.say(_first_line(kind, "echo after the new session"))
    assert msg["runId"] == run, msg
    t.answered(run, "after the new session", timeout=240)
    replaced = wait_until(lambda: [e for e in t.run_events(run) if e["eventType"] == "agent.session.replaced"],
                          timeout=60, interval=1, message="no agent.session.replaced")
    brief = replaced[0]["payload"].get("directiveId")
    assert brief, replaced

    def received():
        return next((r["data"] for r in _records(env, lux_id) if r["type"] == "lux.input"
                     and r["data"].get("requestId") == brief and r["data"].get("phase") == "accepted"), None)
    record = wait_until(received, timeout=120, interval=1, message="lux never accepted the re-brief")
    text = record.get("text") or ""
    _log(f"r2/{kind}", {"run": run, "luxRunId": lux_id, "replaced": replaced[0]["payload"], "rebrief": text,
                        "seconds": round(time.time() - started)})
    assert not record.get("truncated"), record
    assert "restarted without its earlier conversation" in text, text
    stored = query(env.owner_dsn, "SELECT text FROM directives WHERE id = %s", (brief,))[0]["text"]
    assert text == stored, (text, stored)
    start = text.find(REPLAY)
    assert start >= 0, text
    # A session's re-brief has the conversation last, before how it works;
    # a conductor's, before dude's message.
    end = text.find("## How you work", start) if kind == "brainstorm" else _message_at(t, text)
    replay = text[start:end] if end > start else text[start:]
    paragraphs = _paragraphs(replay)
    said, answered = _person_line(paragraphs, "hello there, first message"), _agent_line(paragraphs, answer)
    assert len(said) == 1 and said[0] < answered, paragraphs
    # The message the resume carries comes after the re-brief, unheard when it was written.
    assert "after the new session" not in replay, replay
    assert t.runs_created() == [run]


# ---------------------------------------------------------------------------
# 3. After a compaction: the replay starts from the agent's summary
# ---------------------------------------------------------------------------


@pytest.mark.timeout(600)
@pytest.mark.parametrize("kind", KINDS)
def test_replay_after_a_compaction_the_new_run_is_briefed_from_the_summary_on(
        client: ApiClient, env, lux_project, kind: str):
    started = time.time()
    # lux-fake reports a compaction through Claude Code's protocol (PR A's scenario 5).
    t = _talker(client, env, kind, lux_project, harness="claude-code")
    run = _started(t)
    lux_id = t.lux_id(run)
    t.say(_first_line(kind, "echo remember periwinkle"))
    t.answered(run, "remember periwinkle")
    t.say(_first_line(kind, "compact\necho compacted now"))
    t.answered(run, "compacted now")
    compacted = wait_until(lambda: _compacted(t, run), timeout=60, interval=1, message="no agent.context.compacted")
    summary = compacted[-1]["payload"]["summary"]
    luxs = [r["data"]["summary"] for r in _records(env, lux_id) if r["type"] == "lux.compacted"]
    assert luxs == [summary], (luxs, summary)
    t.say(_first_line(kind, "echo after the compaction"))
    t.answered(run, "after the compaction")
    nxt = _next_run(t, env, run, "and now, a new agent")

    prompt = _prompt(t, nxt)
    replay = _replay(t, prompt)
    body = replay.split("\n\n", 2)[2]  # past the heading and the lead
    _log(f"r3/{kind}", {"run": run, "luxRunId": lux_id, "next": nxt, "nextLux": t.lux_id(nxt), "summary": summary,
                        "replay": replay, "seconds": round(time.time() - started)})
    assert body.startswith(SUMMARY + "\n\n" + summary.strip() + "\n\n" + SINCE + "\n\n"), body
    since = _paragraphs(body.split(SINCE, 1)[1])
    # Only what was said after the compaction: the agent's answer that
    # finished the compacting turn, then the next exchange.
    assert "You: compacted now" in since, since
    assert any(p.endswith("echo after the compaction") and not p.startswith("You") for p in since), since
    assert "You: after the compaction" in since, since
    for before in ("hello there, first message", "remember periwinkle"):
        assert not [p for p in since if before in p], (before, since)
    assert "and now, a new agent" not in replay, replay
    _received_the_script_for(t, env, nxt, "and now, a new agent")


# ---------------------------------------------------------------------------
# 4. A message never read when the Run ended: briefed once, replayed once
# ---------------------------------------------------------------------------


@pytest.mark.timeout(900)
@pytest.mark.parametrize("kind", KINDS)
def test_replay_a_message_handed_over_unread_is_briefed_once_and_replayed_once(
        client: ApiClient, env, lux_project, kind: str):
    started = time.time()
    # Claude Code's protocol: a message sent mid-turn is accepted with a
    # receipt and read only at the turn's next step, which `sleep` holds off.
    t = _talker(client, env, kind, lux_project, harness="claude-code")
    run = _started(t)
    t.say(_first_line(kind, "sleep 600"))
    wait_until(lambda: query(env.owner_dsn, "SELECT 1 FROM runs WHERE id = %s AND agent_busy_at IS NOT NULL AND turn_done_at IS NULL", (run,)),
               timeout=60, interval=0.5, message="the long turn never started")
    unread = t.say("did the build pass?")
    assert unread["runId"] == run, unread
    directive = unread["directiveId"]
    wait_until(lambda: query(env.owner_dsn, "SELECT 1 FROM directives WHERE id = %s AND sent_at IS NOT NULL", (directive,)),
               timeout=60, interval=0.5, message="the message was never sent to lux")
    at_end = query(env.owner_dsn, "SELECT sent_at, accepted_at, delivered_at, failed_at FROM directives WHERE id = %s", (directive,))[0]
    assert at_end["delivered_at"] is None and at_end["failed_at"] is None, at_end
    lux_id = _terminate(t, env, run)

    def settled():
        later = [r for r in t.runs_created() if r != run]
        if later and any("did the build pass?" in s for s in t.said(later[0])):
            return later
        row = query(env.owner_dsn, "SELECT failed_at, error FROM directives WHERE id = %s", (directive,))[0]
        return None if row["failed_at"] is None or later else ["failed"]
    got = wait_until(settled, timeout=180, interval=1, message="the unread message was neither handed on nor failed")
    if got == ["failed"]:
        time.sleep(10)  # the syncer's hand-over pass, had it anything left to hand on
        inputs = [(r["type"], r["data"].get("phase"), r["data"].get("error")) for r in _records(env, lux_id)
                  if r["type"].startswith("lux.input") and r["data"].get("requestId") == directive]
        failed = [e["payload"] for e in t.run_events(run) if e["eventType"] == "run.directive.failed"]
        _log(f"r4/{kind}/lost", {"run": run, "luxRunId": lux_id, "directive": directive, "directiveAtTerminate": at_end,
                                 "directiveAfter": query(env.owner_dsn, "SELECT delivered_at, failed_at, error FROM directives WHERE id = %s",
                                                         (directive,))[0],
                                 "luxInputs": inputs, "dudeFailed": failed, "runsCreated": t.runs_created()})
        # Real lux fails an accepted, unread input when the Run stops
        # (lux.input.failed); dude fails the directive with lux's error
        # before the Run's end is seen, so HandOver finds nothing unsettled.
        pytest.xfail(f"lux failed the unread message as the Run stopped ({inputs}); dude failed it ({failed}) "
                     f"and handed nothing on: runs {t.runs_created()}")
    nxt = got[0]
    second = _prompt(t, nxt)
    answer = next(s for s in t.said(nxt) if "did the build pass?" in s)
    third = _next_run(t, env, nxt, "and the docs?")
    third_prompt = _prompt(t, third)
    replay = _replay(t, third_prompt)
    paragraphs = _paragraphs(replay)
    _log(f"r4/{kind}", {"run": run, "luxRunId": lux_id, "directive": directive, "directiveAtTerminate": at_end,
                        "directiveAfter": query(env.owner_dsn, "SELECT delivered_at, failed_at, error FROM directives WHERE id = %s", (directive,))[0],
                        "next": nxt, "nextLux": t.lux_id(nxt), "secondBriefingTail": second[_message_at(t, second):],
                        "third": third, "thirdLux": t.lux_id(third), "thirdReplay": replay, "seconds": round(time.time() - started)})
    # The second Run: briefed with it as its message, once, not in its replay.
    assert second.count("did the build pass?") == 1 and second.find("did the build pass?") > _message_at(t, second), second
    assert "did the build pass?" not in _replay(t, second)
    _received_the_script_for(t, env, nxt, "did the build pass?")
    # The third Run: the message once, a person's line, before the answer to it.
    said = _person_line(paragraphs, "did the build pass?")
    assert len(said) == 1 and said[0] < _agent_line(paragraphs, answer), paragraphs
    assert TOOK_OVER in paragraphs[:said[0]], paragraphs
    assert t.runs_created() == [run, nxt, third]


# ---------------------------------------------------------------------------
# Real models: what the agent before said, known after a terminate
# ---------------------------------------------------------------------------

REMEMBER = f"Remember the code word {WORD}: I will ask you for it later. Reply with just: noted. Do nothing else."


def _replay_received(t: Talker, env, run: str) -> tuple[str, str]:
    """The new Run's first prompt as lux received it, and the conversation
    so far in it: the briefing dude recorded, within the prompt it wrapped."""
    received = _lux_prompt(env, t.lux_id(run))
    briefing = _prompt(t, run)
    assert briefing in received, "lux's first prompt does not carry the Run's briefing"
    return received, _replay(t, briefing)


@needs_model
@pytest.mark.timeout(1500)
@pytest.mark.parametrize("harness", ["claude-code", "codex", "opencode"])
def test_replay_a_real_model_knows_the_word_after_a_terminate(client: ApiClient, env, harness: str):
    started = time.time()
    t = _real_brainstorm(client, env, harness)
    first = t.say(REMEMBER)
    run = first["runId"]
    t.lux_running(run)
    _turn_over(t, run)
    lux_id = _terminate(t, env, run)
    nxt = t.say(ASK_WORD)
    assert nxt["status"] == 201 and nxt["runId"] != run, nxt
    new = nxt["runId"]
    said = _answers_word(t, new, 0, WORD)
    received, replay = _replay_received(t, env, new)
    _log(f"r5/{harness}", {"run": run, "luxRunId": lux_id, "next": new, "nextLux": t.lux_id(new), "firstAnswer": t.said(run),
                           "replay": replay, "receivedChars": len(received), "answer": said, "seconds": round(time.time() - started)})
    assert t.lux_id(new) != lux_id
    assert _person_line(_paragraphs(replay), REMEMBER), replay
    assert ASK_WORD not in replay, replay
    assert t.runs_created() == [run, new]


@needs_model
@pytest.mark.timeout(1800)
@pytest.mark.parametrize("harness", ["claude-code", "codex", "opencode"])
def test_replay_a_real_model_knows_the_word_after_a_compaction_and_a_terminate(client: ApiClient, env, lux_project, harness: str):
    started = time.time()
    # Compacted as PR A's real-model test compacts each harness: /compact
    # to a conductor (a brainstorm hears "Name: /compact", no command);
    # Codex compacts on its own at a low auto-compact limit.
    options = {"args": ["-c", "model_auto_compact_token_limit=2000"]} if harness == "codex" else None
    t = _real_conductor(client, env, lux_project, harness, options)
    first = t.say(REMEMBER)
    run = first["runId"]
    t.lux_running(run)
    _turn_over(t, run)
    t.say("Reply with just: ok. Do nothing else.")
    _turn_over(t, run)
    t.say("/compact" if harness != "codex" else "Reply with just: ok. Do nothing else.")
    wait_until(lambda: _compacted(t, run), timeout=600, interval=3,
               message=f"no agent.context.compacted; row {t.row(run)}; said {t.said(run)[-3:]}")
    _turn_over(t, run)
    summary = [e["payload"].get("summary") for e in _compacted(t, run) if (e["payload"].get("summary") or "").strip()][-1]
    lux_id = _terminate(t, env, run)
    nxt = t.say(ASK_WORD)
    assert nxt["status"] == 201 and nxt["runId"] != run, nxt
    new = nxt["runId"]
    said = _answers_word(t, new, 0, WORD)
    received, replay = _replay_received(t, env, new)
    body = replay.split("\n\n", 2)[2]
    _log(f"r6/{harness}", {"run": run, "luxRunId": lux_id, "next": new, "nextLux": t.lux_id(new),
                           "compactions": len(_compacted(t, run)), "replay": replay, "answer": said,
                           "seconds": round(time.time() - started)})
    assert body.startswith(SUMMARY + "\n\n" + summary.strip()), body
    assert not _person_line(_paragraphs(body.split(SINCE, 1)[-1]), REMEMBER), body
    assert ASK_WORD not in replay, replay
    assert t.runs_created() == [run, new]
