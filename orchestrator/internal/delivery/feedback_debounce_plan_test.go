package delivery

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/dbtest"
)

// Feedback debouncing is read once per task, not once per pending row and
// again per row inside (step 3b, PR C2, round 1). 100 tasks whose conductor
// is between turns hold feedback that is not due, and the Wakeable probe,
// run every sweep, rules them out:
//   - a burst, 20 reasons each within the quiet: no row is a candidate
//     (none can make its task due), so the burst is not read at all;
//   - a trickle, 25 reasons each a minute apart, the newest just in: the
//     rows past the quiet are candidates, and each asks whether its task's
//     feedback is due with bounded probes on conductor_wakes_pending_idx,
//     not an aggregate over the whole trickle.
//
// Before the repair the burst read about 150k buffers and the trickle
// about 400k (a few dozen per pending row, read again per row).
func TestDebouncingFeedbackIsNotReadAgainForEachOfItsReasons(t *testing.T) {
	_, owner := dbtest.Open(t)
	ctx := context.Background()
	org := dbtest.Org(t, owner)
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := owner.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES ('prj_p', $1, 'P', 'prj_p', 'P')`, org)
	exec(`INSERT INTO tasks (id, organization_id, project_id, number, title, goal)
		SELECT 'tsk_'||n, $1, 'prj_p', n, 'T', 'G' FROM generate_series(1, 100) n`, org)
	exec(`INSERT INTO runs (id, organization_id, project_id, task_id, attempt, role, kind, status, turn_done_at)
		SELECT 'run_c'||n, $1, 'prj_p', 'tsk_'||n, 1, 'conductor', 'agent', 'running', now()
		FROM generate_series(1, 100) n`, org)
	probe := `SELECT EXISTS (SELECT 1 FROM conductor_wakes c WHERE ` + Wakeable + `)`
	buffers := func(name string, most float64) {
		t.Helper()
		exec(`ANALYZE conductor_wakes`)
		var wakeable bool
		if err := owner.QueryRow(ctx, probe, 15.0).Scan(&wakeable); err != nil {
			t.Fatal(err)
		}
		if wakeable {
			t.Fatalf("%s: feedback not yet due is wakeable", name)
		}
		var plan []byte
		if err := owner.QueryRow(ctx, `EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON) `+probe, 15.0).Scan(&plan); err != nil {
			t.Fatal(err)
		}
		var nodes []struct{ Plan map[string]any }
		if err := json.Unmarshal(plan, &nodes); err != nil {
			t.Fatal(err)
		}
		hits, _ := nodes[0].Plan["Shared Hit Blocks"].(float64)
		reads, _ := nodes[0].Plan["Shared Read Blocks"].(float64)
		t.Logf("%s: %.0f buffers", name, hits+reads)
		if hits+reads > most {
			t.Errorf("%s: the probe read %.0f buffers over 100 debouncing tasks, want at most %.0f; plan %s", name, hits+reads, most, plan)
		}
	}
	exec(`INSERT INTO conductor_wakes (id, organization_id, task_id, kind, key, line, created_at)
		SELECT 'cwk_'||n||'_'||k, $1, 'tsk_'||n, $2, 'feedback:'||n||'_'||k, 'fb', now() - make_interval(secs => k * 20)
		FROM generate_series(1, 100) n, generate_series(1, 20) k`, org, WakeFeedback)
	buffers("a burst", 2000)
	exec(`DELETE FROM conductor_wakes`)
	exec(`INSERT INTO conductor_wakes (id, organization_id, task_id, kind, key, line, created_at)
		SELECT 'cwk_'||n||'_'||k, $1, 'tsk_'||n, $2, 'feedback:'||n||'_'||k, 'fb', now() - make_interval(secs => k * 60 + 5)
		FROM generate_series(1, 100) n, generate_series(0, 24) k`, org, WakeFeedback)
	buffers("a trickle", 20000)
}
