"""tests/measure_resume.py's numbers: percentiles, groups, unknown phases,
which timing is a cycle's, how a Run's events are paged, and what --down
needs.

Pure checks with fixed cycles and a stubbed API; they need no environment
and no lux:

    uv run --directory tests pytest suites/test_measure_resume.py
"""
import json

import pytest

import measure_resume as m


def cycle(total, moved, phases=None, until_busy=None, epoch=2):
    c = {"epoch": epoch, "cause": "person", "moved": moved, "hostName": "h", "phases": phases or {}}
    if total is not None:
        c["totalMs"] = total
    if until_busy is not None:
        c["untilBusyMs"] = until_busy
    return c


def test_nearest_rank_percentiles():
    assert m.percentile([], 50) is None
    assert m.percentile([7], 50) == 7 and m.percentile([7], 95) == 7
    twenty = [float(v) for v in range(20, 0, -1)]  # 20..1, out of order
    assert m.percentile(twenty, 50) == 10
    assert m.percentile(twenty, 95) == 19
    assert m.percentile(twenty, 100) == 20


def test_an_empty_run_has_no_numbers():
    s = m.summarize([])
    for group in ("all", "sameHost", "moved"):
        assert s[group]["cycles"] == 0
        assert s[group]["stats"]["totalMs"] == {"n": 0, "p50": None, "p95": None, "max": None}


def test_resumes_are_grouped_by_whether_lux_moved_them_and_unknown_goes_in_neither():
    cycles = [cycle(100, False), cycle(300, True), cycle(200, None), cycle(50, False)]
    s = m.summarize(cycles)
    assert (s["all"]["cycles"], s["sameHost"]["cycles"], s["moved"]["cycles"]) == (4, 2, 1)
    assert s["sameHost"]["stats"]["totalMs"] == {"n": 2, "p50": 50, "p95": 100, "max": 100}
    assert s["moved"]["stats"]["totalMs"] == {"n": 1, "p50": 300, "p95": 300, "max": 300}
    assert s["all"]["stats"]["totalMs"]["max"] == 300


def test_a_phase_lux_did_not_report_is_left_out_but_a_zero_is_counted():
    cycles = [cycle(100, False, {"image": 0, "react": 5}), cycle(200, False, {"react": 7}),
              cycle(None, False, {"react": 9}, until_busy=40)]
    stats = m.summarize(cycles)["sameHost"]["stats"]
    assert stats["image"] == {"n": 1, "p50": 0, "p95": 0, "max": 0}
    assert stats["restore"] == {"n": 0, "p50": None, "p95": None, "max": None}
    assert stats["react"]["n"] == 3
    assert stats["totalMs"]["n"] == 2 and stats["untilBusyMs"]["n"] == 1


def test_the_table_says_unknown_rather_than_zero(capsys):
    m.print_text([cycle(1500, False, {"image": 0})], m.summarize([cycle(1500, False, {"image": 0})]), [])
    out = capsys.readouterr().out
    assert "image ready" in out and " 0ms" in out
    assert "restored" in out and "—" in out


def test_a_cycles_timing_is_the_first_of_an_epoch_after_the_one_it_stopped():
    events = [
        {"eventType": "run.resume.timed", "payload": {"epoch": 2, "totalMs": 1}},
        {"eventType": "agent.message", "payload": {}},
        {"eventType": "run.resume.timed", "payload": {"epoch": 3, "totalMs": 2}},
    ]
    assert m.timed_for(events, 1)["payload"]["epoch"] == 2
    assert m.timed_for(events, 2)["payload"]["epoch"] == 3
    assert m.timed_for(events, 3) is None


class PagedApi:
    """/v1/events as the API pages it: `after` a cursor, at most `limit`,
    and the cursor to go on from."""

    def __init__(self, events, page=2):
        self.events, self.page, self.asked = events, page, []

    def get(self, path, params):
        assert path == "/v1/events"
        self.asked.append(params["after"])
        after = [e for e in self.events if e["cursor"] > params["after"]][: self.page]
        body = {"events": after, "nextCursor": after[-1]["cursor"] if after else params["after"]}
        return type("R", (), {"status_code": 200, "json": lambda self: body, "text": ""})()


def test_a_runs_events_are_read_page_by_page_from_the_cursor_on():
    api = PagedApi([{"cursor": n, "eventType": "agent.message", "payload": {}} for n in range(1, 6)])
    cycler = m.Cycler.__new__(m.Cycler)
    cycler.client, cycler.seen, cycler.cursor = api, {}, {}
    assert [e["cursor"] for e in cycler.events("run_1")] == [1, 2, 3, 4, 5]
    assert api.asked == [0, 2, 4, 5]
    api.events.append({"cursor": 6, "eventType": "run.resume.timed", "payload": {"epoch": 2}})
    assert m.timed_for(cycler.events("run_1"), 1)["payload"]["epoch"] == 2
    assert api.asked[4:] == [5, 6]


class Cleanup:
    """A Cycler whose dude and lux are stubs: rows by Run id as (status,
    lux Run id), lux states by lux Run id (each read takes the next, the
    last staying), and task_runs answering each call from `listings` in
    turn. Records what was asked, in order."""

    def __init__(self, monkeypatch, rows, states, listings, failing=(), claims=((),), after_clear=()):
        self.asked, self.logged = [], []
        cycler = m.Cycler.__new__(m.Cycler)
        asked, listings, claims = self.asked, list(listings), list(claims)
        cleared = []

        class Client:
            def task_runs(self, task_id):
                asked.append("list")
                listed = listings.pop(0) if len(listings) > 1 else listings[0]
                return [{"id": r} for r in [*listed, *(after_clear if cleared else ())]]

            def post(self, path, body):
                run = path.split("/")[3]
                asked.append(f"abort {run}")
                if run in failing:
                    raise ConnectionError("dude went away")

        def claimed_steps():
            asked.append("claimed?")
            claimed = list(claims.pop(0) if len(claims) > 1 else claims[0])
            if not claimed:
                cleared.append(True)
            return claimed

        cycler.client, cycler.tasks = Client(), ["task_1"]
        cycler.stop_workflows = lambda: asked.append("stop workflows")
        cycler.claimed_steps = claimed_steps
        cycler.row = lambda run_id: {"status": rows[run_id][0], "lux_run_id": rows[run_id][1]}
        cycler.lux_run = lambda lux_id: {"state": states[lux_id].pop(0) if len(states[lux_id]) > 1 else states[lux_id][0]}
        monkeypatch.setattr(m.time, "sleep", lambda s: None)
        monkeypatch.setattr(m, "log", self.logged.append)
        self.cycler, self.states = cycler, states


def test_cleanup_stops_the_workflows_then_aborts_every_run_still_going_and_waits_for_lux_to_cancel_it(monkeypatch):
    c = Cleanup(monkeypatch, rows={"run_a": ("running", "lrun_a"), "run_b": ("completed", "lrun_b")},
                states={"lrun_a": ["running", "running", "cancelled"], "lrun_b": ["stopped"]},
                listings=[["run_a", "run_b"]])
    c.cycler.abort_all(timeout=5)
    assert c.asked[:2] == ["stop workflows", "list"]
    assert [a for a in c.asked if a.startswith("abort")] == ["abort run_a"]
    assert c.states["lrun_a"] == ["cancelled"]
    assert not [line for line in c.logged if "could not" in line], c.logged


def test_a_failed_run_lux_failed_has_ended_at_once(monkeypatch):
    c = Cleanup(monkeypatch, rows={"run_f": ("failed", "lrun_f"), "run_x": ("aborted", "lrun_x")},
                states={"lrun_f": ["failed"], "lrun_x": ["succeeded"]}, listings=[["run_f", "run_x"]])
    c.cycler.abort_all(timeout=0)
    assert not [a for a in c.asked if a.startswith("abort")]
    assert not [line for line in c.logged if "could not" in line], c.logged


def test_an_aborted_run_lux_stopped_has_not_ended(monkeypatch):
    c = Cleanup(monkeypatch, rows={"run_x": ("aborted", "lrun_x")}, states={"lrun_x": ["stopped"]}, listings=[["run_x"]])
    c.cycler.abort_all(timeout=0)
    assert [line for line in c.logged if "could not end Run run_x" in line], c.logged


def test_a_run_whose_abort_fails_does_not_keep_the_next_from_being_aborted(monkeypatch):
    c = Cleanup(monkeypatch, rows={"run_c": ("running", "lrun_c"), "run_a": ("running", "lrun_a")},
                states={"lrun_c": ["running"], "lrun_a": ["running", "cancelled"]},
                listings=[["run_c", "run_a"]], failing=("run_c",))
    c.cycler.abort_all(timeout=5)
    assert [a for a in c.asked if a.startswith("abort")] == ["abort run_c", "abort run_a"]
    assert c.states["lrun_a"] == ["cancelled"]
    assert [line for line in c.logged if "could not end Run run_c" in line], c.logged


def test_a_run_a_workflow_step_created_after_the_first_listing_is_aborted_too(monkeypatch):
    c = Cleanup(monkeypatch, rows={"run_i": ("completed", "lrun_i"), "run_r": ("running", "lrun_r")},
                states={"lrun_i": ["stopped"], "lrun_r": ["running", "cancelled"]},
                listings=[["run_i"], ["run_i", "run_r"]])
    c.cycler.abort_all(timeout=5)
    assert [a for a in c.asked if a.startswith("abort")] == ["abort run_r"]
    assert c.asked.count("list") == 2
    assert c.states["lrun_r"] == ["cancelled"]


def test_a_run_a_claimed_step_creates_once_it_finishes_is_aborted_too(monkeypatch):
    # The step is still claimed at the first poll and clears at the second;
    # its Run is listed only once it has.
    c = Cleanup(monkeypatch, rows={"run_i": ("completed", "lrun_i"), "run_r": ("running", "lrun_r")},
                states={"lrun_i": ["stopped"], "lrun_r": ["running", "cancelled"]},
                listings=[["run_i"]], claims=[["wf_1"], []], after_clear=["run_r"])
    c.cycler.abort_all(timeout=5, claim_wait=60)
    assert [a for a in c.asked if a.startswith("abort")] == ["abort run_r"]
    assert c.states["lrun_r"] == ["cancelled"]
    assert c.asked.count("claimed?") == 2
    assert not [line for line in c.logged if "still claimed" in line], c.logged


def test_a_step_claimed_past_the_bound_is_said_and_cleanup_goes_on(monkeypatch):
    c = Cleanup(monkeypatch, rows={"run_r": ("running", "lrun_r")}, states={"lrun_r": ["running", "cancelled"]},
                listings=[["run_r"]], claims=[["wf_1"]])
    c.cycler.abort_all(timeout=5, claim_wait=0)
    assert [line for line in c.logged if "still claimed after 0s: wf_1" in line], c.logged
    assert c.asked.count("list") == 2


def test_down_needs_no_lux(tmp_path, monkeypatch):
    state = tmp_path / "state.json"
    state.write_text(json.dumps({"run_id": "abc12345", "control_plane_port": 1, "orchestrator_port": 2,
                                 "log_dir": str(tmp_path / "logs"), "lux_env": "/nowhere/env.json", "pids": []}))
    monkeypatch.setattr(m, "STATE", state)
    monkeypatch.setenv("DUDE_TEST_REAL_LUX", "1")
    torn = []
    monkeypatch.setattr(m.TestEnvironment, "teardown", lambda self, keep=False: torn.append(self.run_id))

    def no_lux():
        raise AssertionError("--down asked lux")

    monkeypatch.setattr("env.lux_env", no_lux)
    m.take_down()
    assert torn == ["abc12345"] and not state.exists()


@pytest.mark.parametrize("v,shown", [(None, "—"), (0, "0ms"), (999, "999ms"), (1500, "1.5s"), (-40, "-40ms")])
def test_ms(v, shown):
    assert m.ms(v) == shown
