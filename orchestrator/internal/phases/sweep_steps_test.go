package phases

import (
	"context"
	"testing"
	"time"
)

// A Run a conductor started fails while nothing else in the factory needs
// the sweep: the next Sweep records the failure as the conductor's reason
// to wake, once, however many sweeps follow.
func TestASweepRecordsAConductorsFailedRunOnce(t *testing.T) {
	ctx := context.Background()
	w := newWakeSweepWorld(t)
	org, exec, count := w.org, w.exec, w.count
	exec(`INSERT INTO tasks (id, organization_id, project_id, number, title, goal) VALUES ('wi_f', $1, 'prj_'||$1, 1, 'T', 'G')`, org)
	// The conductor is parked between turns, with nothing queued: no
	// sweep step has anything to do for it.
	exec(`INSERT INTO runs (id, organization_id, project_id, task_id, attempt, role, kind, status, dude_pause, turn_done_at)
		VALUES ('run_cond', $1, 'prj_'||$1, 'wi_f', 1, 'conductor', 'agent', 'paused', 'conductor', now())`, org)
	exec(`INSERT INTO runs (id, organization_id, project_id, task_id, attempt, phase, status, conductor_run_id, error, ended_at)
		VALUES ('run_impl', $1, 'prj_'||$1, 'wi_f', 1, 'implement', 'failed', 'run_cond', 'the build broke', now())`, org)

	for range 2 {
		if _, err := w.s.Sweep(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if n := count(`SELECT count(*) FROM conductor_wakes WHERE task_id = 'wi_f' AND key = 'failed:run_impl'
		AND line LIKE '%run_impl failed: the build broke%'`); n != 1 {
		t.Fatalf("%d reasons recorded for the failed Run after two sweeps, want 1", n)
	}
}

// A conducted Run stalls while nothing else is due: the pass that reports
// it also tells its conductor, as a stall report joins the note it is due
// with, rather than leaving the note to a later pass.
func TestAStallReportReachesTheConductorInTheSamePass(t *testing.T) {
	ctx := context.Background()
	w := newWakeSweepWorld(t)
	org, exec, count := w.org, w.exec, w.count
	exec(`INSERT INTO tasks (id, organization_id, project_id, number, title, goal) VALUES ('wi_s', $1, 'prj_'||$1, 1, 'T', 'G')`, org)
	exec(`INSERT INTO runs (id, organization_id, project_id, task_id, attempt, role, kind, status, dude_pause, turn_done_at)
		VALUES ('run_cond', $1, 'prj_'||$1, 'wi_s', 1, 'conductor', 'agent', 'paused', 'conductor', now())`, org)
	// Silent for an hour: past a conducted task's window (StallWindow).
	exec(`INSERT INTO runs (id, organization_id, project_id, task_id, attempt, phase, status, lux_state, started_at)
		VALUES ('run_rev', $1, 'prj_'||$1, 'wi_s', 1, 'review', 'running', 'running', now() - interval '1 hour')`, org)
	w.s.WakeWindow = time.Nanosecond
	if err := w.s.beforeDue(ctx); err != nil {
		t.Fatal(err)
	}
	if n := count(`SELECT count(*) FROM conductor_wakes WHERE task_id = 'wi_s' AND kind = 'stalled' AND delivered_at IS NOT NULL`); n != 1 {
		t.Fatalf("%d stall reasons delivered to the conductor in the pass that reported it, want 1", n)
	}
	if n := count(`SELECT count(*) FROM directives WHERE run_id = 'run_cond' AND text LIKE '%run_rev%no progress%'`); n != 1 {
		t.Fatalf("the conductor has %d notes naming the stalled Run, want 1", n)
	}
}
