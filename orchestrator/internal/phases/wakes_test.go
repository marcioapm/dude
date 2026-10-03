package phases

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/marciomartins/dude/orchestrator/internal/dbtest"
)

// At the batch's boundary: 200 tasks whose reasons must wait (their
// conductors mid-turn), 200 sleeping conductors whose Run in flight has
// woken them already, and one task whose conductor can hear its reason.
// The ready one is told within one sweep, and a repeat sweep records no
// safety wake twice.
func TestAReadyWakeIsNotCrowdedOutOfTheSweep(t *testing.T) {
	ctx := context.Background()
	app, owner := dbtest.Open(t)
	org := dbtest.Org(t, owner)
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := owner.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	count := func(sql string, args ...any) int {
		t.Helper()
		var n int
		if err := owner.QueryRow(ctx, sql, args...).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	exec(`INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES ('prj_'||$1, $1, 'P', 'prj_'||$1, 'P')`, org)
	exec(`INSERT INTO tasks (id, organization_id, project_id, number, title, goal)
		SELECT 'wi_'||n, $1, 'prj_'||$1, n, 'T', 'G' FROM generate_series(1, 400) n`, org)
	// The ready task's id sorts after every other's: no accident of order
	// brings it into a batch the others fill.
	exec(`INSERT INTO tasks (id, organization_id, project_id, number, title, goal) VALUES ('wi_zready', $1, 'prj_'||$1, 401, 'T', 'G'),
		('wi_zsafe', $1, 'prj_'||$1, 402, 'T', 'G')`, org)
	// One more asleep, more recently than those 200, whose Run in flight
	// has not woken it yet: its safety reason is due.
	exec(`INSERT INTO runs (id, organization_id, project_id, task_id, attempt, role, kind, status, dude_pause, turn_done_at)
		VALUES ('run_snew', $1, 'prj_'||$1, 'wi_zsafe', 1, 'conductor', 'agent', 'paused', 'conductor', now() - interval '1 hour')`, org)
	exec(`INSERT INTO runs (id, organization_id, project_id, task_id, attempt, phase, role, kind, status, conductor_run_id)
		VALUES ('run_knew', $1, 'prj_'||$1, 'wi_zsafe', 1, 'implement', 'implementer', 'agent', 'running', 'run_snew')`, org)
	// 1–200: conductors mid-turn, with older reasons pending.
	exec(`INSERT INTO runs (id, organization_id, project_id, task_id, attempt, role, kind, status, lux_run_id, lux_state)
		SELECT 'run_b'||n, $1, 'prj_'||$1, 'wi_'||n, 1, 'conductor', 'agent', 'running', 'lux_b'||n, 'running'
		FROM generate_series(1, 200) n`, org)
	exec(`INSERT INTO conductor_wakes (id, organization_id, task_id, kind, key, line, created_at)
		SELECT 'cwk_b'||n, $1, 'wi_'||n, 'decision', 'k', 'busy', now() - interval '1 hour'
		FROM generate_series(1, 200) n`, org)
	// 201–400: asleep for long, a Run of their own in flight, already
	// woken for it once.
	exec(`INSERT INTO runs (id, organization_id, project_id, task_id, attempt, role, kind, status, dude_pause, turn_done_at)
		SELECT 'run_s'||n, $1, 'prj_'||$1, 'wi_'||n, 1, 'conductor', 'agent', 'paused', 'conductor', now() - interval '2 hours'
		FROM generate_series(201, 400) n`, org)
	exec(`INSERT INTO runs (id, organization_id, project_id, task_id, attempt, phase, role, kind, status, conductor_run_id)
		SELECT 'run_k'||n, $1, 'prj_'||$1, 'wi_'||n, 1, 'implement', 'implementer', 'agent', 'running', 'run_s'||n
		FROM generate_series(201, 400) n`, org)
	exec(`INSERT INTO conductor_wakes (id, organization_id, task_id, kind, key, line, created_at, delivered_at)
		SELECT 'cwk_s'||n, $1, 'wi_'||n, 'safety', 'safety:run_k'||n, 'in flight', now() - interval '1 hour', now() - interval '1 hour'
		FROM generate_series(201, 400) n`, org)
	// The ready one: parked between turns, its reason settled, the newest.
	exec(`INSERT INTO runs (id, organization_id, project_id, task_id, attempt, role, kind, status, dude_pause, turn_done_at)
		VALUES ('run_ready', $1, 'prj_'||$1, 'wi_zready', 1, 'conductor', 'agent', 'paused', 'conductor', now())`, org)
	exec(`INSERT INTO conductor_wakes (id, organization_id, task_id, kind, key, line, created_at)
		VALUES ('cwk_ready', $1, 'wi_zready', 'decision', 'k', 'ready to decide', now() - interval '1 minute')`, org)

	s := &Syncer{DB: app, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), WakeWindow: 15 * time.Second, SafetyAfter: time.Minute}
	if err := s.wakeConductors(ctx); err != nil {
		t.Fatal(err)
	}
	if n := count(`SELECT count(*) FROM directives WHERE run_id = 'run_ready' AND text LIKE '%ready to decide%'`); n != 1 {
		t.Fatalf("the ready conductor has %d notes after one sweep, want 1", n)
	}
	if n := count(`SELECT count(*) FROM conductor_wakes WHERE id = 'cwk_ready' AND delivered_at IS NOT NULL`); n != 1 {
		t.Errorf("the ready reason is still pending")
	}
	if n := count(`SELECT count(*) FROM conductor_wakes WHERE task_id <> 'wi_zready' AND kind = 'decision' AND delivered_at IS NOT NULL`); n != 0 {
		t.Errorf("%d reasons delivered to conductors mid-turn", n)
	}
	if n := count(`SELECT count(*) FROM conductor_wakes WHERE key = 'safety:run_knew'`); n != 1 {
		t.Errorf("the new Run in flight has %d safety reasons after one sweep, want 1: woken ones crowd it out", n)
	}
	if err := s.wakeConductors(ctx); err != nil {
		t.Fatal(err)
	}
	if n := count(`SELECT count(*) FROM conductor_wakes WHERE kind = 'safety'`); n != 201 {
		t.Errorf("%d safety reasons after a repeat sweep, want the 201 recorded once", n)
	}
	if n := count(`SELECT count(*) FROM directives WHERE run_id = 'run_ready'`); n != 1 {
		t.Errorf("%d notes for the ready conductor after a repeat sweep, want 1", n)
	}
}
