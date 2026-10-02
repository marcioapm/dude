"""tests/measure_resume.py's stop_workflows against the suite's database:
the rows it leaves, for its own tasks' workflows and for those it must not
touch. Needs the environment run_tests.py starts:

    python tests/run_tests.py suites/test_measure_resume_db.py
"""
import os

import measure_resume as m
from helpers import execute, query

COLUMNS = "id, status::text AS status, last_error, wake_at, locked_by, locked_until, step, state, attempt"


def workflow(dsn: str, org: str, task: str, status: str, last_error: str | None = None, leased: bool = True) -> str:
    """A delivery workflow of task in status; a live one parked on a timer
    and leased to a poller, an ended one with neither, as the runtime
    leaves it."""
    wf = f"wf_{os.urandom(8).hex()}"
    execute(dsn, """INSERT INTO workflow_runs (id, organization_id, workflow_type, idempotency_key, status, step,
            task_id, last_error, wake_at, locked_by, locked_until)
        VALUES (%s, %s, 'task.delivery', %s, %s::workflow_run_status, 'review', %s, %s,
            CASE WHEN %s THEN now() + interval '1 hour' END, CASE WHEN %s THEN 'poller-1' END,
            CASE WHEN %s THEN now() + interval '1 hour' END)""",
            (wf, org, wf, status, task, last_error, leased, leased, leased))
    return wf


def rows(dsn: str, ids: list[str]) -> dict[str, dict]:
    return {r["id"]: r for r in query(dsn, f"SELECT {COLUMNS} FROM workflow_runs WHERE id = ANY(%s)", (ids,))}


def test_stop_workflows_aborts_its_tasks_live_workflows_and_nothing_else(env, org, client, project):
    tracked = client.create_task(project["id"], "measured")["id"]
    other = client.create_task(project["id"], "not measured")["id"]
    running = workflow(env.owner_dsn, org["id"], tracked, "running")
    waiting = workflow(env.owner_dsn, org["id"], tracked, "waiting")
    done = workflow(env.owner_dsn, org["id"], tracked, "completed", last_error="finished before", leased=False)
    unrelated = workflow(env.owner_dsn, org["id"], other, "running")
    before = rows(env.owner_dsn, [running, waiting, done, unrelated])

    cycler = m.Cycler(env, client, project, timeout=5)
    cycler.tasks = [tracked]
    cycler.stop_workflows()

    after = rows(env.owner_dsn, [running, waiting, done, unrelated])
    for wf in (running, waiting):
        # Stopped as workflow.Runtime.Abort stops it; its lease is kept, for
        # wait_for_claimed_steps to see a step still running.
        assert after[wf] == before[wf] | {"status": "aborted", "last_error": "measured", "wake_at": None}, wf
    assert after[done] == before[done]
    assert after[unrelated] == before[unrelated]
    # The leases are the claimed steps cleanup waits on, its own tasks' only.
    assert sorted(cycler.claimed_steps()) == sorted([running, waiting])
