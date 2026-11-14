package delivery

import (
	"context"
	"strings"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/dbtest"
)

// The deferred read (deferredRead) runs on every weigh of a delivery's
// gate: on a writer Run with 30,000 events it finds the judgements, asks,
// answers and waivers on their partial indexes, on a generic plan as well
// as a custom one, never among every event of the Run
// (events_run_cursor_idx).
func TestTheDeferredReadUsesThePartialIndexes(t *testing.T) {
	_, owner := dbtest.Open(t)
	ctx := context.Background()
	org := dbtest.Org(t, owner)
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := owner.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES ('prj_p', $1, 'P', 'p', 'P')`, org)
	exec(`INSERT INTO tasks (id, organization_id, project_id, number, title, goal, status) VALUES ('wi_p', $1, 'prj_p', 1, 'T', 'g', 'review')`, org)
	exec(`INSERT INTO runs (id, organization_id, project_id, task_id, attempt, status, phase, role, kind)
		VALUES ('run_p', $1, 'prj_p', 'wi_p', 1, 'completed', 'implement', 'implementer', 'agent')`, org)
	exec(`INSERT INTO events (id, organization_id, run_id, task_id, event_type, actor_type, actor_id, source, payload)
		SELECT 'evt_p_' || n, $1, 'run_p', 'wi_p', (ARRAY['agent.tool.called', 'agent.tool.completed', 'agent.message'])[n % 3 + 1],
			'agent', 'x', 'agent', '{"discharge":{},"text":"x"}' FROM generate_series(1, 30000) n`, org)
	exec(`INSERT INTO events (id, organization_id, run_id, task_id, event_type, actor_type, actor_id, source, payload)
		VALUES ('evt_p_j', $1, 'run_p', 'wi_p', 'run.checks.judged', 'system', 'dude', 'orchestrator',
			'{"turn": 1, "checks": [{"command": "bench", "result": "deferred", "reason": "later"}]}'),
		('evt_p_a', $1, 'run_p', 'wi_p', 'run.checks.asked', 'system', 'dude', 'orchestrator',
			'{"turn": 1, "discharge": {"heads": {"r": "h"}, "key": "k"}, "checks": [{"command": "bench"}], "deadline": "2026-01-01T00:00:00Z"}')`, org)
	exec(`ANALYZE events`)
	for _, mode := range []string{"force_custom_plan", "force_generic_plan"} {
		tx, err := owner.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, `SELECT set_config('plan_cache_mode', $1, true), set_config('jit', 'off', true)`, mode); err != nil {
			t.Fatal(err)
		}
		var plan []byte
		if err := tx.QueryRow(ctx, `EXPLAIN (FORMAT JSON) `+deferredRead, "wi_p", 1).Scan(&plan); err != nil {
			t.Fatal(err)
		}
		_ = tx.Rollback(ctx)
		text := string(plan)
		for _, idx := range []string{"events_run_checks_settled_idx", "events_run_checks_asked_idx", "events_run_checks_decided_idx"} {
			if !strings.Contains(text, idx) {
				t.Errorf("%s: the deferred read does not use %s:\n%s", mode, idx, plan)
			}
		}
		if strings.Contains(text, "events_run_cursor_idx") {
			t.Errorf("%s: the deferred read scans a Run's every event (events_run_cursor_idx):\n%s", mode, plan)
		}
	}
	s, err := func() (*deferredState, error) {
		tx, err := owner.Begin(ctx)
		if err != nil {
			return nil, err
		}
		defer tx.Rollback(ctx)
		return readDeferredTx(ctx, tx, "wi_p", 1, true)
	}()
	if err != nil || len(s.checks) != 1 || s.standing(s.checks[0], headsID(map[string]string{"r": "h"})) != deferredOverdue {
		t.Fatalf("read %+v %v", s, err)
	}
}
