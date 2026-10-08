#!/usr/bin/env python3
"""Measure how long a resume of a Run takes on a real lux, end to end.

Brings up dude against a real lux as the contract suite does (the lux of
DUDE_TEST_LUX_ENV, or the latest `run_tests.py --serve` in lux's
repository), makes a project on a stand-in GitHub that lux's hosts can
reach, and runs cycles of:

    a Run's agent is working → a person pauses it (POST /v1/runs/:id/pause)
    → lux reports it stopped → the person resumes it (POST /v1/runs/:id/resume)
    → dude's run.resume.timed for it

Each resume is timed by dude itself (run_resumes, run.resume.timed): from
the person's Resume to the agent's first output, split into phases. This
prints each cycle's phases and totals, then p50/p95/max per phase and for
the totals, same host and moved apart. --json prints the same for a
machine.

    uv run --project tests python tests/measure_resume.py --cycles 10
    uv run --project tests python tests/measure_resume.py --model llm-anthropic/claude-sonnet-5 \\
        --image registry/agent:tag          # with DUDE_LLM_URL and DUDE_LLM_KEY set
    uv run --project tests python tests/measure_resume.py --keep   # leave it up, reuse it next time
    uv run --project tests python tests/measure_resume.py --down   # take a kept one down

With a scripted model (fake/...) the implementer is `fake/live`: lux-fake
writes files and keeps working until it is stopped, so there is something
working to pause; the scripted implementer finishes its turn at once. The
agent takes the resume's "carry on" and finishes, so each cycle is a task
of its own. A real model keeps working after a resume, and its Run is
paused again for the next cycle while it works.

DUDE_LLM_URL and DUDE_LLM_KEY, when set in this process's environment, are
passed to the orchestrator, so a real model can be measured. The key is
never printed.
"""

from __future__ import annotations

import argparse
import json
import math
import os
import signal
import sys
import time
from pathlib import Path

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

import requests  # noqa: E402

from build import build  # noqa: E402
from env import TestEnvironment, llm_env, lux_env, require_bun  # noqa: E402
from fake_github import FakeGitHub  # noqa: E402
from helpers import ApiClient, create_api_key, create_organization, query  # noqa: E402

# A resume's phases, in order, as run.resume.timed names them, and how a
# person reads them.
PHASES = (
    ("react", "dude asked lux"),
    ("schedule", "lux placed it"),
    ("image", "image ready"),
    ("restore", "restored"),
    ("start", "started"),
    ("reload", "agent reloaded"),
    ("take", "took its input"),
    ("firstOutput", "first words"),
)
TOTALS = (("totalMs", "until it said something"), ("untilBusyMs", "until it took its input"))
ROLES = ("implementer", "reviewer", "simplifier")
STATE = Path(os.environ.get("DUDE_MEASURE_STATE", "/tmp/dude-measure-resume.json"))
# workflow.Runtime's lease (leaseDuration): a step's claim lapses at most
# this long after the step returns. Cleanup waits that, and a margin, for
# claimed steps to finish.
LEASE = 60
CLAIM_WAIT = LEASE + 15

# Why --move cannot be done with a tenant's key, from lux's docs/openapi.yaml
# and docs/cli.md.
NO_TENANT_MOVE = (
    "--move skipped: lux's tenant API has no way to place a resume on another host. "
    "POST /v1/runs/{id}/resume's `to` and POST /v1/runs/{id}/migrate need an operator key; "
    "POST /v1/hosts/{id}/drain needs the admin scope, applies only to the tenant's own hosts, "
    "and has no undrain in the API, so it would leave the host drained. "
    "Resumes lux moves on its own are still reported apart (moved)."
)


def log(msg: str) -> None:
    print(msg, file=sys.stderr, flush=True)


def wait(what: str, check, timeout: float, interval: float = 0.5):
    deadline = time.time() + timeout
    while True:
        got = check()
        if got:
            return got
        if time.time() > deadline:
            raise SystemExit(f"timed out after {timeout:.0f}s waiting for {what}")
        time.sleep(interval)


# -- the environment ----------------------------------------------------------


def bring_up(keep: bool) -> TestEnvironment:
    """A kept environment that still answers, or a new one."""
    if keep and STATE.exists():
        env = reuse(json.loads(STATE.read_text()))
        if env:
            log(f"reusing the environment in {STATE} (logs: {env.log_dir}); "
                "its orchestrator keeps the LLM settings it was started with")
            return env
        log(f"the environment in {STATE} is gone; starting another")
    require_bun()
    build()
    env = TestEnvironment(real_lux=lux_env(), orchestrator_env=llm_env())
    log(f"dude on lux {env.real_lux['luxd_url']}: control plane {env.control_plane_url}, logs {env.log_dir}")
    if llm_env():
        log("passing " + " and ".join(sorted(llm_env())) + " to the orchestrator")
    env.setup()
    if not env.wait_healthy(timeout=60):
        env.teardown()
        raise SystemExit(f"dude did not come up; see {env.log_dir}")
    if keep:
        STATE.write_text(json.dumps({
            "run_id": env.run_id, "control_plane_port": env.control_plane_port,
            "orchestrator_port": env.orchestrator_port, "log_dir": str(env.log_dir),
            "lux_env": os.environ.get("DUDE_TEST_LUX_ENV", ""),
            "pids": [p.pid for p in (env.control_plane_proc, env.orchestrator_proc) if p],
        }))
    return env


def reuse(state: dict) -> TestEnvironment | None:
    _state_env(state)
    os.environ["DUDE_TEST_REAL_LUX"] = "1"
    if state.get("lux_env"):
        os.environ["DUDE_TEST_LUX_ENV"] = state["lux_env"]
    env = TestEnvironment.from_env()
    return env if env.wait_healthy(timeout=3) else None


def _state_env(state: dict) -> None:
    """The variables TestEnvironment.from_env rebuilds a kept environment from."""
    os.environ.update({
        "DUDE_TEST_RUN_ID": state["run_id"], "DUDE_TEST_CONTROL_PLANE_PORT": str(state["control_plane_port"]),
        "DUDE_TEST_ORCHESTRATOR_PORT": str(state["orchestrator_port"]), "DUDE_TEST_LOG_DIR": state["log_dir"],
    })


def take_down() -> None:
    """A kept environment's processes stopped and its database, bucket and
    files dropped, as a run of the suite drops them. Local only: lux is not
    asked anything, so this works with lux down too."""
    if not STATE.exists():
        raise SystemExit(f"no kept environment ({STATE})")
    state = json.loads(STATE.read_text())
    for pid in state.get("pids", []):
        try:
            os.kill(pid, signal.SIGTERM)
        except ProcessLookupError:
            pass
    _state_env(state)
    os.environ.pop("DUDE_TEST_REAL_LUX", None)
    TestEnvironment.from_env().teardown()
    STATE.unlink()
    log(f"taken down; logs kept in {state['log_dir']}")


def project(env: TestEnvironment, gh: FakeGitHub, image: str, model: str) -> tuple[ApiClient, dict, str]:
    """An organization of its own, and a project on the stand-in GitHub
    (which lux's hosts reach at their network's gateway)."""
    org = create_organization(env.owner_dsn, f"measure{os.urandom(3).hex()}")
    client = ApiClient(env.control_plane_url, create_api_key(env.owner_dsn, org))
    resp = client.post("/v1/forge/credential", {"auth": "pat", "secret": "fake-token", "apiBaseUrl": gh.api_url})
    assert resp.status_code == 200, resp.text
    implementer = "fake/live" if model.startswith("fake/") else model
    models = {r: {"model": model} for r in ROLES} | {"implementer": {"model": implementer}}
    proj = client.create_project(name="Resume timing", slug=f"resume-{os.urandom(3).hex()}", runtimeImage=image,
                                 agentModels=models,
                                 repositories=[{"name": "target", "url": gh.clone_url, "defaultBranch": "main"}])
    return client, proj, implementer


# -- one cycle -----------------------------------------------------------------


def timed_for(events: list[dict], stopped_epoch: int) -> dict | None:
    """The run.resume.timed of the resume out of stopped_epoch: the first
    timing of a later epoch."""
    return next((e for e in events if e["eventType"] == "run.resume.timed"
                 and e["payload"].get("epoch", 0) > stopped_epoch), None)


class Cycler:
    def __init__(self, env: TestEnvironment, client: ApiClient, proj: dict, timeout: float) -> None:
        self.env, self.client, self.project, self.timeout = env, client, proj, timeout
        self.run: dict | None = None
        # Every task delivered, so cleanup finds each of their Runs.
        self.tasks: list[str] = []
        # Each Run's events read so far, from the API's cursor on.
        self.seen: dict[str, list[dict]] = {}
        self.cursor: dict[str, int] = {}

    def lux_run(self, lux_run_id: str) -> dict:
        r = requests.get(f"{self.env.lux_url}/v1/runs/{lux_run_id}",
                         headers={"authorization": f"Bearer {self.env.lux_key}"}, timeout=10)
        r.raise_for_status()
        return r.json()

    def row(self, run_id: str) -> dict:
        return query(self.env.owner_dsn, """SELECT status::text, COALESCE(lux_run_id, '') AS lux_run_id,
            COALESCE(lux_state, '') AS lux_state, agent_busy_at IS NOT NULL AND turn_done_at IS NULL AS working
            FROM runs WHERE id = %s""", (run_id,))[0]

    def events(self, run_id: str) -> list[dict]:
        """The Run's events, every page, read on from where the last call
        left off."""
        seen = self.seen.setdefault(run_id, [])
        while True:
            resp = self.client.get("/v1/events", params={"runId": run_id, "after": self.cursor.get(run_id, 0),
                                                          "limit": 1000})
            assert resp.status_code == 200, resp.text
            page = resp.json()
            seen.extend(page["events"])
            self.cursor[run_id] = page["nextCursor"]
            if not page["events"]:
                return seen

    def working_run(self) -> dict:
        """The Run of the last cycle while its agent still works, else a new task's implementer."""
        if self.run:
            r = self.row(self.run["id"])
            if r["status"] == "running" and r["working"]:
                return self.run
            self.client.post(f"/v1/runs/{self.run['id']}/abort", {"reason": "measured"})
        task = self.client.create_task(self.project["id"], f"Resume timing {os.urandom(2).hex()}")
        self.tasks.append(task["id"])
        assert self.client.post(f"/v1/tasks/{task['id']}/deliver").status_code == 201
        self.run = wait("the implementer to start", lambda: next(
            (r for r in self.client.task_runs(task["id"]) if r["phase"] == "implement"), None), self.timeout)
        return self.run

    def cycle(self) -> dict:
        run = self.working_run()
        wait("the agent to be working", lambda: (lambda r: r["status"] == "running" and r["lux_state"] == "running"
                                                 and r["working"])(self.row(run["id"])), self.timeout)
        resp = self.client.post(f"/v1/runs/{run['id']}/pause", {})
        assert resp.status_code == 200, resp.text
        lux_run_id = self.row(run["id"])["lux_run_id"]
        stopped = wait("lux to report it stopped", lambda: (lambda r: r["state"] == "stopped" and r)(
            self.lux_run(lux_run_id)), self.timeout)
        wait("dude to see it paused", lambda: self.row(run["id"])["status"] == "paused", self.timeout)
        resp = self.client.post(f"/v1/runs/{run['id']}/resume", {})
        assert resp.status_code == 200, resp.text
        event = wait("the resume's run.resume.timed", lambda: timed_for(self.events(run["id"]), stopped["epoch"]),
                     self.timeout)
        return {"runId": run["id"], **event["payload"]}

    def stop_workflows(self) -> None:
        """Every delivered task's workflow stopped, so it starts no further
        phase. No API stops a task's delivery (a Run's abort stops its
        workflow only while that Run is live), so it is stopped through the
        owner's connection, with the status, reason and wake the
        orchestrator's own Abort sets. Unlike Abort, the lease
        (locked_by, locked_until) is left as it is: it is how
        wait_for_claimed_steps sees a step still running."""
        if not self.tasks:
            return
        stopped = query(self.env.owner_dsn, """UPDATE workflow_runs SET status = 'aborted', last_error = 'measured',
            wake_at = NULL
            WHERE task_id = ANY(%s) AND status IN ('running', 'waiting') RETURNING id""", (self.tasks,))
        log(f"stopped {len(stopped)} delivery workflow(s)")

    def claimed_steps(self) -> list[str]:
        """The tasks' workflows whose step a poller still holds: its lease
        runs on while the step does (workflow.Runtime renews it), and lapses
        within one lease once the step returns."""
        if not self.tasks:
            return []
        return [r["id"] for r in query(self.env.owner_dsn, """SELECT id FROM workflow_runs
            WHERE task_id = ANY(%s) AND locked_until > now()""", (self.tasks,))]

    def wait_for_claimed_steps(self, bound: float) -> None:
        """Until no step of the tasks' workflows is claimed: a step already
        running when its workflow was stopped can still create a phase.
        At most bound seconds; then said, and cleanup goes on."""
        deadline = time.time() + bound
        while claimed := self.claimed_steps():
            if time.time() >= deadline:
                log(f"workflow step(s) still claimed after {bound:.0f}s: {', '.join(claimed)}")
                return
            time.sleep(1)

    def task_run_ids(self) -> list[str]:
        ids = []
        for task_id in self.tasks:
            try:
                ids += [r["id"] for r in self.client.task_runs(task_id)]
            except Exception as err:  # noqa: BLE001 - cleanup goes on
                log(f"could not list task {task_id}'s Runs: {err}")
        return ids

    def end(self, run_id: str, timeout: float) -> None:
        """A Run aborted unless it has ended, and waited on until lux has
        ended it too."""
        row = self.row(run_id)
        if row["status"] not in ("completed", "failed", "aborted"):
            self.client.post(f"/v1/runs/{run_id}/abort", {"reason": "measured"})
        if row["lux_run_id"]:
            ended = lux_ended(row["status"])
            wait(f"lux to end {row['lux_run_id']}", lambda: self.lux_run(row["lux_run_id"])["state"] in ended, timeout)

    def abort_all(self, timeout: float = 120, claim_wait: float = CLAIM_WAIT) -> None:
        """Every Run this measured ended on lux, while dude is still up to
        tell lux: the tasks' workflows stopped first, so they start no new
        phase, then each Run aborted and waited on. Then, once no workflow
        step that was already running is left, the Runs listed once more,
        for a phase such a step created. Best effort: a failure is said,
        and the rest still cleaned up."""
        try:
            self.stop_workflows()
        except Exception as err:  # noqa: BLE001 - cleanup goes on
            log(f"could not stop the delivery workflows: {err}")
        done: set[str] = set()
        self.end_listed(done, timeout)
        try:
            self.wait_for_claimed_steps(claim_wait)
        except Exception as err:  # noqa: BLE001 - cleanup goes on
            log(f"could not see the workflows' claimed steps: {err}")
        self.end_listed(done, timeout)

    def end_listed(self, done: set[str], timeout: float) -> None:
        """Each of the tasks' Runs not in done ended, and added to it."""
        for run_id in self.task_run_ids():
            if run_id in done:
                continue
            done.add(run_id)
            try:
                self.end(run_id, timeout)
            except (Exception, SystemExit) as err:  # noqa: BLE001 - cleanup goes on
                log(f"could not end Run {run_id} on lux: {err}")


def lux_ended(status: str) -> tuple[str, ...]:
    """The lux states that end a dude Run of this status. lux's failed and
    succeeded are final; dude cancels nothing in them. Stopped is resumable,
    so it ends only a completed Run, which dude stops on purpose."""
    ended = ("cancelled", "terminated", "succeeded", "failed")
    return ended + ("stopped",) if status == "completed" else ended


# -- the numbers ------------------------------------------------------------------


def percentile(values: list[float], p: float) -> float | None:
    """Nearest rank: the smallest value at least p of them are no greater than."""
    if not values:
        return None
    ordered = sorted(values)
    return ordered[max(0, math.ceil(p / 100 * len(ordered)) - 1)]


def value(cycle: dict, key: str):
    """A total, or a phase; None when the resume did not know it."""
    return cycle.get(key) if key in dict(TOTALS) else cycle["phases"].get(key)


def summarize(cycles: list[dict]) -> dict:
    groups = {"all": cycles, "sameHost": [c for c in cycles if c.get("moved") is False],
              "moved": [c for c in cycles if c.get("moved") is True]}
    out = {}
    for name, group in groups.items():
        stats = {}
        for key, _ in (*TOTALS, *PHASES):
            values = [v for v in (value(c, key) for c in group) if v is not None]
            stats[key] = {"n": len(values), "p50": percentile(values, 50), "p95": percentile(values, 95),
                          "max": max(values) if values else None}
        out[name] = {"cycles": len(group), "stats": stats}
    return out


def ms(v) -> str:
    if v is None:
        return "—"
    return f"{v:.0f}ms" if abs(v) < 1000 else f"{v / 1000:.1f}s"


def print_text(cycles: list[dict], summary: dict, notes: list[str]) -> None:
    for note in notes:
        print(note)
    for i, c in enumerate(cycles, 1):
        where = f"{c.get('hostName') or '?'}{', moved' if c.get('moved') else ''}"
        print(f"\ncycle {i}: {ms(c.get('totalMs'))} until it said something, "
              f"{ms(c.get('untilBusyMs'))} until it took its input ({where})")
        for key, label in PHASES:
            print(f"  {label:<16} {ms(c['phases'].get(key)):>8}")
    for name, title in (("sameHost", "same host"), ("moved", "moved")):
        group = summary[name]
        print(f"\n{title}: {group['cycles']} resume(s)")
        if not group["cycles"]:
            continue
        print(f"  {'':<24} {'p50':>8} {'p95':>8} {'max':>8}")
        for key, label in (*TOTALS, *PHASES):
            s = group["stats"][key]
            print(f"  {label:<24} {ms(s['p50']):>8} {ms(s['p95']):>8} {ms(s['max']):>8}")


def main() -> None:
    parser = argparse.ArgumentParser(description="Measure resumes of a Run on a real lux, end to end.")
    parser.add_argument("--cycles", type=int, default=5, help="pause/resume cycles to measure (default 5)")
    parser.add_argument("--image", default="localhost/lux-fake:test", help="the agents' image (default lux-fake)")
    parser.add_argument("--model", default="fake/scripted", help="the model for every role (default fake/scripted)")
    parser.add_argument("--move", action="store_true", help="place each resume on another host than it stopped on")
    parser.add_argument("--keep", action="store_true", help=f"leave the environment up, and reuse a kept one ({STATE})")
    parser.add_argument("--down", action="store_true", help="take a kept environment down, and exit")
    parser.add_argument("--timeout", type=float, default=300, help="seconds to wait for each step (default 300)")
    parser.add_argument("--json", action="store_true", help="print JSON instead of a table")
    args = parser.parse_args()
    if args.down:
        take_down()
        return

    notes = []
    if args.move:
        notes.append(NO_TENANT_MOVE)
        log(NO_TENANT_MOVE)
    env = bring_up(args.keep)
    gh = None
    cycler = None
    cycles: list[dict] = []
    try:
        gh = FakeGitHub(env.git_root, owner=f"m{os.urandom(4).hex()}", listen=env.real_lux["gateway"])
        gh.start()
        client, proj, implementer = project(env, gh, args.image, args.model)
        if implementer != args.model:
            notes.append(f"the implementer runs {implementer}: {args.model}'s finishes its turn at once, "
                         "leaving nothing working to pause")
        cycler = Cycler(env, client, proj, args.timeout)
        for i in range(args.cycles):
            c = cycler.cycle()
            cycles.append(c)
            log(f"cycle {i + 1}/{args.cycles}: {ms(c.get('totalMs'))}")
    finally:
        # Whatever happened, no Run is left working on lux: aborted while
        # dude and the stand-in GitHub are still up, and seen cancelled.
        if cycler:
            cycler.abort_all()
        if gh:
            gh.stop()
        if not args.keep:
            env.teardown()

    summary = summarize(cycles)
    if args.json:
        print(json.dumps({"image": args.image, "model": args.model, "moveSupported": False if args.move else None,
                          "notes": notes, "cycles": cycles, "summary": summary}, indent=2, default=str))
    else:
        print_text(cycles, summary, notes)


if __name__ == "__main__":
    main()
