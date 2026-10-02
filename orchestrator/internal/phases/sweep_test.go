package phases

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/marciomartins/dude/orchestrator/internal/dbtest"
)

// A thousand warm, quiet conductors — each with its turn done, nothing
// queued, its warm period not over, its container up — are not work: a
// newer pending Run is in the sweep's batch ahead of them. Their finished
// turns are followed, not acted on, until one of those changes, while a
// finished phase Run is still taken first.
func TestQuietWarmConductorsDoNotCrowdTheSweep(t *testing.T) {
	ctx := context.Background()
	app, owner := dbtest.Open(t)
	org := dbtest.Org(t, owner)
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := owner.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES ('prj_'||$1, $1, 'P', 'prj_'||$1, 'P')`, org)
	exec(`INSERT INTO tasks (id, organization_id, project_id, number, title, goal)
		SELECT 'wi_'||n, $1, 'prj_'||$1, n, 'T', 'G' FROM generate_series(1, 1002) n`, org)
	// One conductor per task (runs_live_conductor_idx), oldest first.
	exec(`INSERT INTO runs (id, organization_id, project_id, task_id, attempt, role, kind, status, lux_run_id, lux_state,
			turn_done_at, created_at)
		SELECT 'run_c'||n, $1, 'prj_'||$1, 'wi_'||n, 1, 'conductor', 'agent', 'running', 'lux_c'||n, 'running',
			now() - interval '1 second', now() - interval '1 hour' + n * interval '1 millisecond'
		FROM generate_series(1, 1000) n`, org)
	exec(`INSERT INTO runs (id, organization_id, project_id, task_id, attempt, phase, role, kind, status, created_at)
		VALUES ('run_pending', $1, 'prj_'||$1, 'wi_1001', 1, 'implement', 'implementer', 'agent', 'pending', now())`, org)
	// A phase Run whose turn finished: collected, so work, however new.
	exec(`INSERT INTO runs (id, organization_id, project_id, task_id, attempt, phase, role, kind, status, lux_run_id, lux_state,
			turn_done_at, created_at)
		VALUES ('run_done', $1, 'prj_'||$1, 'wi_1002', 1, 'review', 'reviewer', 'agent', 'running', 'lux_done', 'running',
			now(), now())`, org)

	s := &Syncer{DB: app, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), ConductorWarm: time.Hour}
	picked := func() map[string]int {
		t.Helper()
		runs, err := s.due(ctx)
		if err != nil {
			t.Fatal(err)
		}
		at := map[string]int{}
		for i, r := range runs {
			at[r.ID] = i
		}
		if len(runs) != sweepBatch {
			t.Fatalf("the sweep took %d Runs, want a full batch of %d", len(runs), sweepBatch)
		}
		return at
	}
	at := picked()
	for _, id := range []string{"run_pending", "run_done"} {
		if i, ok := at[id]; !ok || i > 1 {
			t.Errorf("%s is not taken first (at %d, taken %v)", id, i, ok)
		}
	}

	// A message queued for the newest conductor makes it work.
	exec(`INSERT INTO directives (id, organization_id, task_id, run_id, text) VALUES ('dir_q', $1, 'wi_1000', 'run_c1000', 'hi')`, org)
	if i, ok := picked()["run_c1000"]; !ok || i > 2 {
		t.Errorf("a conductor with a message queued is not taken first (at %d, taken %v)", i, ok)
	}
}
