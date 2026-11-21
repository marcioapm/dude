package delivery

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/marciomartins/dude/orchestrator/internal/dbtest"
)

// RunCallsQuery reads only events_run_tool_calls_idx (migration 270), for
// both its calls and their completions, under the generic plan
// production's pool settles on: on a busy Run with 50k events of other
// kinds and 5k tool calls before the commit, in an organisation whose
// other Runs call tools too, it never scans events nor walks the Run's
// every event (events_run_cursor_idx).
func TestTheRunsCallsAtACommitReadTheirIndex(t *testing.T) {
	_, owner := dbtest.Open(t)
	ctx := context.Background()
	org := dbtest.Org(t, owner)
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := owner.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%v\n%s", err, sql)
		}
	}
	exec(`INSERT INTO events (id, organization_id, event_type, run_id, occurred_at, actor_type, actor_id, source, payload)
		SELECT 'evt_noise_' || n, $1, (ARRAY['agent.message', 'agent.thought', 'agent.tool.dude'])[n % 3 + 1],
			'run_busy', now() - interval '2 hours' + make_interval(secs => n / 10.0), 'agent', 'run_busy', 'agent', jsonb_build_object('text', 'x')
		FROM generate_series(1, 50000) n`, org)
	exec(`INSERT INTO events (id, organization_id, event_type, run_id, occurred_at, actor_type, actor_id, source, payload)
		SELECT 'evt_call_' || n || '_' || k, $1, CASE WHEN k = 0 THEN 'agent.tool.called' ELSE 'agent.tool.completed' END,
			CASE WHEN n % 4 = 0 THEN 'run_busy' ELSE 'run_other_' || (n % 50) END,
			now() - interval '2 hours' + make_interval(secs => n + k), 'agent', 'run_busy', 'agent',
			CASE WHEN k = 0 THEN jsonb_build_object('callId', 'c' || n, 'input', jsonb_build_object('command', 'make test ' || n))
			ELSE jsonb_build_object('callId', 'c' || n, 'exitCode', 0, 'output', jsonb_build_object('head', 'ok')) END
		FROM generate_series(1, 20000) n, generate_series(0, 1) k`, org)
	exec(`ANALYZE events`)
	exec(`SET ROLE dude_app`)
	exec(`SELECT set_config('app.organization_id', $1, false)`, org)
	defer exec(`RESET ROLE`)
	exec(`SET plan_cache_mode = force_generic_plan`)
	defer exec(`RESET plan_cache_mode`)
	exec(`PREPARE run_calls(text, timestamptz, int, text) AS ` + RunCallsQuery)
	defer exec(`DEALLOCATE run_calls`)

	since := time.Now().Add(-3 * time.Hour)
	var plan []byte
	if err := owner.QueryRow(ctx, `EXPLAIN (FORMAT JSON) EXECUTE run_calls('run_busy', '`+since.UTC().Format(time.RFC3339Nano)+`', 31, '')`).Scan(&plan); err != nil {
		t.Fatal(err)
	}
	var nodes []struct{ Plan map[string]any }
	if err := json.Unmarshal(plan, &nodes); err != nil {
		t.Fatal(err)
	}
	used := map[string]int{}
	var walk func(map[string]any)
	walk = func(node map[string]any) {
		if name, ok := node["Index Name"].(string); ok {
			used[name]++
		}
		if node["Node Type"] == "Seq Scan" && node["Relation Name"] == "events" {
			used["seq scan"]++
		}
		if node["Node Type"] == "Incremental Sort" {
			used["incremental sort"]++
		}
		if node["Node Type"] == "Sort" {
			keys, _ := node["Sort Key"].([]any)
			if len(keys) == 1 && keys[0] == "e.cursor" {
				t.Fatalf("completion lookup sorts the whole tail by cursor; plan %s", plan)
			}
		}
		children, _ := node["Plans"].([]any)
		for _, child := range children {
			walk(child.(map[string]any))
		}
	}
	for _, n := range nodes {
		walk(n.Plan)
	}
	if used["incremental sort"] == 0 {
		t.Fatalf("completion lookup must sort only equal-time groups, not the whole tail; plan %s", plan)
	}
	delete(used, "incremental sort")
	if len(used) != 1 || used["events_run_tool_calls_idx"] == 0 {
		t.Fatalf("the calls at a commit read %v, want events_run_tool_calls_idx alone; plan %s", used, plan)
	}
}
