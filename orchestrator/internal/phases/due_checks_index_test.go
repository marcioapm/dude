package phases

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/dbtest"
)

// Migrations 251 and 252 index the checks asks ChecksWaiting reads in
// OpenAsk, which dueQuery evaluates for every live Run on every sweep: with
// 300 live Runs of 300 events each and a few asks, the ask is probed on
// events_run_checks_asked_idx and what settles it on
// events_run_checks_settled_idx, on a generic plan as well as a custom one,
// never among every event of each Run (events_run_cursor_idx).
func TestDueReadsChecksAsksOnTheirIndex(t *testing.T) {
	owner, apply := dbtest.Upgrade(t, "251")
	ctx := context.Background()
	org := dbtest.Org(t, owner)
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := owner.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO sessions (id, organization_id, titled_by) SELECT 'ses_' || n, $1, NULL FROM generate_series(1, 300) n`, org)
	exec(`INSERT INTO runs (id, organization_id, session_id, attempt, status, control, role, kind, lux_state, turn_done_at, created_at)
		SELECT 'run_' || n, $1, 'ses_' || n, 1, 'running', 'none', 'brainstorm', 'agent', 'running', now(), now() - make_interval(secs => n)
		FROM generate_series(1, 300) n`, org)
	exec(`INSERT INTO events (id, organization_id, run_id, event_type, actor_type, actor_id, source, payload)
		SELECT 'evt_' || r || '_' || e, $1, 'run_' || r, (ARRAY['agent.tool.called', 'agent.tool.completed', 'agent.message', 'agent.session.stopped'])[e % 4 + 1],
			'agent', 'x', 'agent', '{"text":"x"}'
		FROM generate_series(1, 300) r, generate_series(1, 300) e`, org)
	// A few Runs asked about their latest turn; some of those settled.
	exec(`INSERT INTO events (id, organization_id, run_id, event_type, actor_type, actor_id, source, payload)
		SELECT 'evt_ask_' || r, $1, 'run_' || r, 'run.checks.asked', 'system', 'dude', 'orchestrator',
			jsonb_build_object('turn', (SELECT max(cursor) FROM events WHERE run_id = 'run_' || r AND event_type = 'agent.session.stopped'),
				'deadline', now() + interval '10 minutes')
		FROM generate_series(1, 300, 30) r`, org)
	exec(`INSERT INTO events (id, organization_id, run_id, event_type, actor_type, actor_id, source, payload)
		SELECT 'evt_ver_' || r, $1, 'run_' || r, 'run.checks.conducted', 'agent', 'x', 'orchestrator',
			jsonb_build_object('turn', (SELECT max(cursor) FROM events WHERE run_id = 'run_' || r AND event_type = 'agent.session.stopped'))
		FROM generate_series(1, 300, 60) r`, org)
	s := &Syncer{}
	args := append(s.limits(), s.RetryAhead.Seconds(), sweepBatch, s.baseWait().Seconds())
	explain := func(mode string, analyze bool) (plan []byte, ms float64) {
		t.Helper()
		tx, err := owner.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback(ctx)
		if _, err := tx.Exec(ctx, `SELECT set_config('role', 'dude_sweeper', true), set_config('jit', 'off', true),
			set_config('plan_cache_mode', $1, true)`, mode); err != nil {
			t.Fatal(err)
		}
		opts := "FORMAT JSON"
		if analyze {
			opts = "ANALYZE, FORMAT JSON"
		}
		if err := tx.QueryRow(ctx, `EXPLAIN (`+opts+`) `+dueQuery, args...).Scan(&plan); err != nil {
			t.Fatal(err)
		}
		var top []struct {
			ExecutionTime float64 `json:"Execution Time"`
		}
		_ = json.Unmarshal(plan, &top)
		if len(top) > 0 {
			ms = top[0].ExecutionTime
		}
		return plan, ms
	}
	best := func() float64 {
		b := 0.0
		for i := range 5 {
			if _, ms := explain("force_custom_plan", true); i == 0 || ms < b {
				b = ms
			}
		}
		return b
	}
	exec(`ANALYZE`)
	// dueQuery reads runs.news_hold_at (259): there for the measurement
	// before 251, and dropped again for 259 to add it.
	exec(`ALTER TABLE runs ADD COLUMN news_hold_at timestamptz`)
	before := best()
	exec(`ALTER TABLE runs DROP COLUMN news_hold_at`)
	apply()
	exec(`ANALYZE events`)
	after := best()
	t.Logf("dueQuery over 300 live Runs × 300 events: %.1f ms before 251/252, %.1f ms after (best of 5)", before, after)
	for _, mode := range []string{"force_custom_plan", "force_generic_plan"} {
		plan, _ := explain(mode, false)
		used := map[string]map[string]int{}
		// Each scan of events by its alias: the index it reads (a bitmap
		// heap scan's, from its bitmap index scans), or its node type.
		var walk func(node map[string]any, alias string)
		walk = func(node map[string]any, alias string) {
			if node["Relation Name"] == "events" {
				alias, _ = node["Alias"].(string)
				if node["Node Type"] != "Bitmap Heap Scan" {
					name, _ := node["Index Name"].(string)
					if name == "" {
						name = fmt.Sprint(node["Node Type"])
					}
					if used[alias] == nil {
						used[alias] = map[string]int{}
					}
					used[alias][name]++
					alias = ""
				}
			} else if name, ok := node["Index Name"].(string); ok && alias != "" {
				if used[alias] == nil {
					used[alias] = map[string]int{}
				}
				used[alias][name]++
			} else if node["Node Type"] != "BitmapOr" && node["Node Type"] != "BitmapAnd" {
				alias = ""
			}
			children, _ := node["Plans"].([]any)
			for _, c := range children {
				walk(c.(map[string]any), alias)
			}
		}
		var nodes []struct{ Plan map[string]any }
		if err := json.Unmarshal(plan, &nodes); err != nil {
			t.Fatal(err)
		}
		for _, n := range nodes {
			walk(n.Plan, "")
		}
		if len(used["a"]) != 1 || used["a"]["events_run_checks_asked_idx"] == 0 {
			t.Errorf("%s: ChecksWaiting's ask is read on %v, want events_run_checks_asked_idx only; plan %s", mode, used["a"], plan)
		}
		for name := range used["v"] {
			if name != "events_run_checks_settled_idx" {
				t.Errorf("%s: what settles an ask is read on %s, want events_run_checks_settled_idx; plan %s", mode, name, plan)
			}
		}
	}
}
