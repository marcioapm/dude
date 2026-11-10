package delivery

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/dbtest"
)

// prLoopRetryRefusal's read of the earlier retries is on
// events_task_decisions_idx (migration 238) under a generic plan, the plan
// production's pool settles on: on a task with 50k events of its work, in
// an organisation whose other tasks decide too, it reads neither every
// event of the task nor every decision of the organisation.
func TestThePRLoopRetriesReadTheirIndexUnderAGenericPlan(t *testing.T) {
	_, owner := dbtest.Open(t)
	ctx := context.Background()
	org := dbtest.Org(t, owner)
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := owner.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%v\n%s", err, sql)
		}
	}
	exec(`INSERT INTO events (id, organization_id, event_type, task_id, actor_type, actor_id, source, payload)
		VALUES ('evt_retry', $1, $2, 'tsk_busy', 'agent', 'run_c', 'orchestrator',
			'{"reason":"pr_loop_exhausted","action":"retry","note":"The changelog is left."}')`, org, EvTaskDecided)
	exec(`INSERT INTO events (id, organization_id, event_type, task_id, actor_type, actor_id, source, payload)
		SELECT 'evt_other_' || n, $1, $2, 'tsk_other_' || (n % 2000), 'system', 'x', 'orchestrator',
			jsonb_build_object('reason', 'pr_loop_exhausted', 'action', 'retry', 'note', 'n' || n)
		FROM generate_series(1, 20000) n`, org, EvTaskDecided)
	exec(`INSERT INTO events (id, organization_id, event_type, task_id, actor_type, actor_id, source, payload)
		SELECT 'evt_noise_' || n, $1, (ARRAY['agent.tool.dude', 'agent.thought', 'agent.message'])[n % 3 + 1],
			'tsk_busy', 'agent', 'run_x', 'agent', jsonb_build_object('text', 'x')
		FROM generate_series(1, 50000) n`, org)
	exec(`ANALYZE events`)
	exec(`SET ROLE dude_app`)
	exec(`SELECT set_config('app.organization_id', $1, false)`, org)
	defer exec(`RESET ROLE`)
	exec(`SET plan_cache_mode = force_generic_plan`)
	defer exec(`RESET plan_cache_mode`)

	exec(`PREPARE pr_loop_retries AS ` + prLoopRetriesSQL)
	defer exec(`DEALLOCATE pr_loop_retries`)
	var plan []byte
	if err := owner.QueryRow(ctx, `EXPLAIN (FORMAT JSON) EXECUTE pr_loop_retries('tsk_busy', 'wf_1')`).Scan(&plan); err != nil {
		t.Fatal(err)
	}
	var nodes []struct{ Plan map[string]any }
	if err := json.Unmarshal(plan, &nodes); err != nil {
		t.Fatal(err)
	}
	used := map[string]int{}
	var walk func(map[string]any, bool)
	walk = func(node map[string]any, init bool) {
		// The InitPlan reads the delivery's workflow_runs row, by its key.
		if name, ok := node["Index Name"].(string); ok && !init {
			used[name]++
		}
		if node["Node Type"] == "Seq Scan" && node["Relation Name"] == "events" {
			used["seq scan"]++
		}
		children, _ := node["Plans"].([]any)
		for _, child := range children {
			c := child.(map[string]any)
			walk(c, init || c["Parent Relationship"] == "InitPlan")
		}
	}
	for _, n := range nodes {
		walk(n.Plan, false)
	}
	if len(used) != 1 || used["events_task_decisions_idx"] != 1 {
		t.Fatalf("the earlier retries read %v, want events_task_decisions_idx alone; plan %s", used, plan)
	}
}
