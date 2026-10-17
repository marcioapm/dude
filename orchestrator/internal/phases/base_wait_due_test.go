package phases

import (
	"context"
	"io"
	"log/slog"
	"slices"
	"testing"
	"time"

	"github.com/marciomartins/dude/orchestrator/internal/dbtest"
)

// A finished phase turn waiting for its checkout's base is followed, not
// worked: it is in the sweep's batch, but behind a newer pending Run. It is
// work again once the base arrives, the wait is over, or lux reports the
// Run over.
func TestRunsWaitingForTheirBaseDoNotCrowdTheSweep(t *testing.T) {
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
		SELECT 'wi_'||n, $1, 'prj_'||$1, n, 'T', 'G' FROM generate_series(1, 5) n`, org)
	// Three implementers whose turns ended a second ago, unbased; oldest first.
	exec(`INSERT INTO runs (id, organization_id, project_id, task_id, attempt, phase, role, kind, status, lux_run_id, lux_state,
			push_branch, lux_pushes, turn_done_at, created_at)
		SELECT 'run_w'||n, $1, 'prj_'||$1, 'wi_'||n, 1, 'implement', 'implementer', 'agent', 'running', 'lux_w'||n, 'running',
			'dude/wi_'||n, '{target,web}', now() - interval '1 second', now() - interval '1 hour' + n * interval '1 millisecond'
		FROM generate_series(1, 3) n`, org)
	exec(`INSERT INTO runs (id, organization_id, project_id, task_id, attempt, phase, role, kind, status, created_at)
		VALUES ('run_pending', $1, 'prj_'||$1, 'wi_4', 1, 'implement', 'implementer', 'agent', 'pending', now())`, org)
	// A conductor whose turn ended a second ago, with nothing queued for it,
	// older than every other Run: actionable, it would lead the batch.
	exec(`INSERT INTO runs (id, organization_id, project_id, task_id, attempt, role, kind, status, lux_run_id, lux_state,
			turn_done_at, created_at)
		VALUES ('run_conductor', $1, 'prj_'||$1, 'wi_5', 1, 'conductor', 'agent', 'running', 'lux_c', 'running',
			now() - interval '1 second', now() - interval '2 hours')`, org)

	s := &Syncer{DB: app, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), BaseWait: time.Minute}
	// first returns the IDs of the first limit Runs due takes after change,
	// made in a transaction that is rolled back.
	first := func(limit int, change string) []string {
		t.Helper()
		tx, err := owner.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback(ctx)
		if change != "" {
			if _, err := tx.Exec(ctx, change); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := tx.Exec(ctx, `SELECT set_config('role', 'dude_sweeper', true)`); err != nil {
			t.Fatal(err)
		}
		runs, err := s.dueIn(ctx, tx, limit)
		if err != nil {
			t.Fatal(err)
		}
		var ids []string
		for _, r := range runs {
			ids = append(ids, r.ID)
		}
		return ids
	}
	same := func(got []string, want ...string) bool { return slices.Equal(got, want) }

	if got := first(1, ""); !same(got, "run_pending") {
		t.Errorf("with three Runs waiting for their base and a warm conductor, the sweep's first Run is %v, want the pending one", got)
	}
	if got := first(sweepBatch, ""); !same(got, "run_pending", "run_conductor", "run_w1", "run_w2", "run_w3") {
		t.Errorf("a full batch takes %v, want the pending Run, then the warm conductor and every waiting Run, oldest first", got)
	}
	for _, c := range []struct {
		what, change string
		want         []string
	}{
		{"one base of two arrived", `UPDATE runs SET base_shas = '{"target":"abc"}' WHERE id = 'run_w2'`, []string{"run_pending"}},
		{"a microsecond inside the wait", `UPDATE runs SET turn_done_at = now() - interval '1 minute' + interval '1 microsecond' WHERE id = 'run_w2'`, []string{"run_pending"}},
		{"every base arrived", `UPDATE runs SET base_shas = '{"target":"abc","web":"def"}' WHERE id = 'run_w2'`, []string{"run_w2"}},
		{"the wait is over", `UPDATE runs SET turn_done_at = now() - interval '1 minute' WHERE id = 'run_w2'`, []string{"run_w2"}},
		{"lux reports it over", `UPDATE runs SET lux_state = 'lost' WHERE id = 'run_w2'`, []string{"run_w2"}},
		// A conductor past its warm period is work; the phase Runs waiting
		// for their base, whose turns ended as recently, are not.
		{"the conductor's warm period is over", `UPDATE runs SET turn_done_at = now() - interval '1 hour' WHERE id = 'run_conductor'`,
			[]string{"run_conductor", "run_pending", "run_w1", "run_w2", "run_w3"}},
		{"a project that keeps no conductor warm", `UPDATE projects SET delivery_policy = '{"conductorWarmMinutes":0}'`,
			[]string{"run_conductor", "run_pending", "run_w1", "run_w2", "run_w3"}},
	} {
		if got := first(len(c.want), c.change); !same(got, c.want...) {
			t.Errorf("%s: the sweep's first Runs are %v, want %v", c.what, got, c.want)
		}
	}
}
