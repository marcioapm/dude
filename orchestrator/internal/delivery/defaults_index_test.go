package delivery

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/dbtest"
)

// Migration 238 indexes the events defaultsInARowTx reads: on a task with
// 50k events after its decisions, in an organisation whose other tasks
// have 20k decision events of their own, the newest reset is read
// backwards on events_task_decisions_idx, and neither half of the
// statement walks every event of the task.
func TestTheDefaultsInARowReadTheirIndex(t *testing.T) {
	owner, apply := dbtest.Upgrade(t, "238")
	ctx := context.Background()
	org := dbtest.Org(t, owner)
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := owner.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	// The task's decisions first: a hand-back, a default, a banner decision
	// that is not a retry (no reset), another default. Then the task's
	// work, 50k events after them, as a long delivery's are.
	exec(`INSERT INTO events (id, organization_id, event_type, task_id, actor_type, actor_id, source, payload)
		VALUES ('evt_back', $1, $2, 'tsk_busy', 'system', 'x', 'orchestrator', '{}'),
			('evt_def1', $1, $3, 'tsk_busy', 'system', 'x', 'orchestrator', '{"workflowId":"wf_1","decision":4}'),
			('evt_banner', $1, $4, 'tsk_busy', 'person', 'x', 'api', '{"reason":"stuck","action":"retry"}'),
			('evt_def2', $1, $3, 'tsk_busy', 'system', 'x', 'orchestrator', '{"workflowId":"wf_1","decision":5}')`,
		org, EvDeciderChanged, EvDecisionDefaulted, EvTaskDecided)
	// The organisation's other tasks decide too: its decision events by
	// type (events_type_idx) are not this task's few.
	exec(`INSERT INTO events (id, organization_id, event_type, task_id, actor_type, actor_id, source, payload)
		SELECT 'evt_other_' || n, $1, (ARRAY[$2, $3, $4, $5])[n % 4 + 1], 'tsk_other_' || (n % 2000),
			'system', 'x', 'orchestrator', jsonb_build_object('workflowId', 'wf_o' || (n % 2000), 'decision', n)
		FROM generate_series(1, 20000) n`, org, EvDeciderChanged, EvDecisionDefaulted, EvDecisionTaken, EvTaskDecided)
	exec(`INSERT INTO events (id, organization_id, event_type, task_id, actor_type, actor_id, source, payload)
		SELECT 'evt_noise_' || n, $1, (ARRAY['agent.tool.dude', 'agent.thought', 'agent.message'])[n % 3 + 1],
			'tsk_busy', 'agent', 'run_x', 'agent', jsonb_build_object('text', 'x')
		FROM generate_series(1, 50000) n`, org)
	apply()
	exec(`ANALYZE events`)
	exec(`SET ROLE dude_app`)
	exec(`SELECT set_config('app.organization_id', $1, false)`, org)
	defer exec(`RESET ROLE`)

	var inARow int
	var said bool
	if err := owner.QueryRow(ctx, defaultsInARowSQL, "tsk_busy", "wf_1", 5, ReasonConductorUnavailable).Scan(&inARow, &said); err != nil {
		t.Fatal(err)
	}
	if inARow != 2 || !said {
		t.Fatalf("%d in a row (said %v), want 2 and said", inARow, said)
	}
	var plan []byte
	if err := owner.QueryRow(ctx, `EXPLAIN (FORMAT JSON) `+defaultsInARowSQL, "tsk_busy", "wf_1", 5,
		ReasonConductorUnavailable).Scan(&plan); err != nil {
		t.Fatal(err)
	}
	var nodes []struct{ Plan map[string]any }
	if err := json.Unmarshal(plan, &nodes); err != nil {
		t.Fatal(err)
	}
	init := map[string]int{}
	used := map[string]int{}
	var walk func(map[string]any, map[string]int)
	walk = func(node map[string]any, into map[string]int) {
		if name, ok := node["Index Name"].(string); ok {
			into[name]++
			used[name]++
		}
		if node["Node Type"] == "Seq Scan" {
			used["seq scan"]++
		}
		children, _ := node["Plans"].([]any)
		for _, child := range children {
			c := child.(map[string]any)
			if c["Parent Relationship"] == "InitPlan" {
				walk(c, init)
				continue
			}
			walk(c, into)
		}
	}
	for _, n := range nodes {
		walk(n.Plan, map[string]int{})
	}
	// The newest reset is read on 238's index; the defaults on it or on
	// the organisation's defaults by type: neither on every event of the
	// task (events_task_cursor_idx, the primary key backwards, a seq scan).
	if init["events_task_decisions_idx"] != 1 {
		t.Errorf("the newest reset reads %v, want events_task_decisions_idx; plan %s", init, plan)
	}
	for name := range used {
		if name != "events_task_decisions_idx" && name != "events_type_idx" {
			t.Errorf("the defaults in a row read %s, every event of the task; plan %s", name, plan)
		}
	}
}
