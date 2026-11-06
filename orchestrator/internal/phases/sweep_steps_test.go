package phases

import (
	"context"
	"testing"
	"time"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
)

// The combined sweep probes both kinds of held ask without writing on idle
// reads, then hands them over once even when no earlier sweep step ran.
func TestHeldAskProbeAndSweepPreserveBothKinds(t *testing.T) {
	ctx := context.Background()
	w := newWakeSweepWorld(t)
	org := w.org
	w.exec(`INSERT INTO tasks (id, organization_id, project_id, number, title, status)
		VALUES ('wi_held', $1, 'prj_'||$1, 1, 'Held', 'running')`, org)
	w.exec(`INSERT INTO workflow_runs (id, organization_id, task_id, workflow_type, idempotency_key, step, status, state)
		VALUES ('wf_held', $1, 'wi_held', $2, 'held', 'decide', 'waiting', '{"decider":"conductor","voice":"chatty"}')`, org, delivery.WorkflowType)
	w.exec(`INSERT INTO runs (id, organization_id, project_id, task_id, attempt, phase, role, status)
		VALUES ('run_held', $1, 'prj_'||$1, 'wi_held', 1, 'review', 'reviewer', 'completed')`, org)
	w.exec(`INSERT INTO repositories (id, organization_id, project_id, name, url, default_branch)
		VALUES ('repo_held', $1, 'prj_'||$1, 'held', 'git://h/held.git', 'main')`, org)
	w.exec(`INSERT INTO review_questions (id, organization_id, task_id, run_id, n, category, question, addressee)
		VALUES ('rvq_held', $1, 'wi_held', 'run_held', 1, 'correctness', 'Which?', 'conductor')`, org)
	w.exec(`INSERT INTO repository_requests (id, organization_id, task_id, run_id, repository_id, access, reason, addressee)
		VALUES ('rrq_held', $1, 'wi_held', 'run_held', 'repo_held', 'read', 'Needed', 'conductor')`, org)
	w.s.HeldAskWait = time.Hour
	if p := w.s.pending(ctx); p.heldAsks || p.handovers || p.brainstorms {
		t.Fatalf("young asks should leave the sweep idle: %+v", p)
	}
	if n := w.count(`SELECT count(*) FROM events WHERE task_id='wi_held'`); n != 0 {
		t.Fatalf("idle probe wrote %d events", n)
	}
	w.exec(`UPDATE review_questions SET created_at=now()-interval '2 hours' WHERE id='rvq_held'`)
	if p := w.s.pending(ctx); !p.heldAsks || p.handovers || p.brainstorms {
		t.Fatalf("review question missing from held-only probe: %+v", p)
	}
	w.exec(`UPDATE review_questions SET created_at=now() WHERE id='rvq_held'`)
	w.exec(`UPDATE repository_requests SET created_at=now()-interval '2 hours' WHERE id='rrq_held'`)
	if p := w.s.pending(ctx); !p.heldAsks || p.handovers || p.brainstorms {
		t.Fatalf("repository request missing from held-only probe: %+v", p)
	}
	w.exec(`UPDATE review_questions SET created_at=now()-interval '2 hours' WHERE id='rvq_held'`)
	for range 2 {
		if err := w.s.beforeDue(ctx); err != nil {
			t.Fatal(err)
		}
	}
	for _, kind := range []string{"question.asked", "repository.requested"} {
		if n := w.count(`SELECT count(*) FROM events WHERE task_id='wi_held' AND event_type=$1`, kind); n != 1 {
			t.Errorf("%s emitted %d times, want 1", kind, n)
		}
	}
	if p := w.s.pending(ctx); p.heldAsks || p.handovers {
		t.Fatalf("settled asks still due: %+v", p)
	}
}

// An otherwise idle sweep records a conductor-started Run's failure once.
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

// A stall report reaches its conductor in the same sweep, not the next pass.
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
	if _, err := w.s.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	if n := count(`SELECT count(*) FROM conductor_wakes WHERE task_id = 'wi_s' AND kind = 'stalled' AND delivered_at IS NOT NULL`); n != 1 {
		t.Fatalf("%d stall reasons delivered to the conductor in the pass that reported it, want 1", n)
	}
	if n := count(`SELECT count(*) FROM directives WHERE run_id = 'run_cond' AND text LIKE '%run_rev%no progress%'`); n != 1 {
		t.Fatalf("the conductor has %d notes naming the stalled Run, want 1", n)
	}
}
