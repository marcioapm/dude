package delivery

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/dbtest"
)

// The held-answer queries' real plans at production volume (#485): opt in,
// as it seeds 1.7M events (~2 minutes):
//
//	DUDE_ANSWER_PLAN_TEST=1 DUDE_ANSWER_PLAN_OUT=/some/dir go test ./internal/delivery -run TestTheAnswerQueriesPlans -v
//
// One organization with 100,000 Runs on 2,000 tasks (2,000 of them fixers)
// and 1.7M events: 20,000 held answers, 18,000 of them settled, 20,000
// claims, a publication per fixer, 20,000 settled reconciliations and 50
// open. The target fixer carries 30,000 claims of its own (the claim
// lookup's worst case). The records name their answer in causation_id
// (migration 273), as the writers do. Each query is the source's own SQL, run as the app
// under RLS (dude_app, InOrg's organization) — the sweep's as dude_sweeper,
// as InSystem runs it — through PREPARE/EXPLAIN EXECUTE under
// plan_cache_mode force_generic_plan and force_custom_plan, so the generic
// plan is the plan cache's. It asserts index access (each query's partial
// indexes, no sequential scan or a Run's or the organization's every event)
// and bounded filtering, never time. Plans and index sizes are written to
// DUDE_ANSWER_PLAN_OUT.
func TestTheAnswerQueriesPlans(t *testing.T) {
	if os.Getenv("DUDE_ANSWER_PLAN_TEST") != "1" {
		t.Skip("opt in: DUDE_ANSWER_PLAN_TEST=1 (seeds 1.7M events)")
	}
	out := os.Getenv("DUDE_ANSWER_PLAN_OUT")
	if out == "" {
		out = t.TempDir()
	}
	if err := os.MkdirAll(out, 0o755); err != nil {
		t.Fatal(err)
	}
	app, owner := dbtest.Open(t)
	ctx := context.Background()
	org := dbtest.Org(t, owner)
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := owner.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%v\n%s", err, sql)
		}
	}
	start := time.Now()
	exec(`INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES ('prj_v', $1, 'V', 'v', 'V')`, org)
	exec(`INSERT INTO tasks (id, organization_id, project_id, number, title, goal, status)
		SELECT 'wi_' || n, $1, 'prj_v', n + 1, 'T', 'g', 'review' FROM generate_series(0, 1999) n`, org)
	exec(`INSERT INTO runs (id, organization_id, project_id, task_id, attempt, status, phase, role, kind, ended_at)
		SELECT 'run_' || n, $1, 'prj_v', 'wi_' || (n % 2000), 1, 'completed',
			(CASE WHEN n % 50 = 0 THEN 'fix' ELSE 'implement' END)::run_phase, 'implementer', 'agent', now()
		FROM generate_series(0, 99999) n`, org)
	exec(`INSERT INTO tasks (id, organization_id, project_id, number, title, goal, status) VALUES ('wi_t', $1, 'prj_v', 9999, 'T', 'g', 'review')`, org)
	exec(`INSERT INTO runs (id, organization_id, project_id, task_id, attempt, status, phase, role, kind, ended_at)
		VALUES ('run_t', $1, 'prj_v', 'wi_t', 1, 'completed', 'fix', 'implementer', 'agent', now())`, org)
	// Seeding only: no NOTIFY per row, no session lookups.
	exec(`ALTER TABLE events DISABLE TRIGGER USER`)
	defer func() { _, _ = owner.Exec(ctx, `ALTER TABLE events ENABLE TRIGGER USER`) }()
	ins := `INSERT INTO events (id, organization_id, event_type, project_id, task_id, run_id, actor_type, actor_id, source, payload) `
	// Claims and settling records name their answer in causation_id too, as
	// the writers do (answerEvent).
	insC := `INSERT INTO events (id, organization_id, event_type, project_id, task_id, run_id, actor_type, actor_id, source, causation_id, payload) `
	// Filler: 1.61M of a Run's every-day events, spread over every Run.
	for from := 0; from < 1_610_000; from += 230_000 {
		exec(ins+`SELECT 'evt_f_' || n, $1, (ARRAY['agent.message', 'agent.tool.called', 'agent.tool.completed', 'chat.message', 'pull_request.action'])[n % 5 + 1],
				'prj_v', 'wi_' || (n % 100000 % 2000), 'run_' || (n % 100000), 'agent', 'x', 'orchestrator',
				jsonb_build_object('text', repeat('x', 120), 'n', n)
			FROM generate_series($2::int, $3::int) n`, org, from, from+229_999)
	}
	// Each fixer (run_0, run_50, …): one publication and ten held answers;
	// 18,000 answers settled by their reply's chat.message; a claim on each.
	fixer := `'run_' || ((k % 2000) * 50)`
	task := `'wi_' || ((k % 2000) * 50 % 2000)`
	exec(ins+`SELECT 'evt_c_' || k, $1, 'git.commit_created', 'prj_v', `+task+`, `+fixer+`, 'agent', 'x', 'orchestrator',
			jsonb_build_object('repo', 'target', 'headSha', md5(k::text)) FROM generate_series(0, 1999) k`, org)
	exec(ins+`SELECT 'evt_h_' || k, $1, '`+EvAnswerHeld+`', 'prj_v', `+task+`, `+fixer+`, 'agent', 'x', 'orchestrator',
			jsonb_build_object('kind', 'reply', 'repo', 'target', 'number', 1, 'inReplyTo', 'issue-comment-' || k, 'text', 'Fixed.')
		FROM generate_series(0, 19999) k`, org)
	exec(insC+`SELECT 'evt_k_' || k, $1, '`+EvAnswerClaimed+`', 'prj_v', `+task+`, `+fixer+`, 'system', 'x', 'orchestrator', 'evt_h_' || k,
			jsonb_build_object('answerId', 'evt_h_' || k) FROM generate_series(0, 19999) k`, org)
	exec(insC+`SELECT 'evt_s_' || k, $1, 'chat.message', 'prj_v', `+task+`, `+fixer+`, 'agent', 'x', 'orchestrator', 'evt_h_' || k,
			jsonb_build_object('text', 'Fixed.', 'by', 'fixer', 'github', jsonb_build_object('answerId', 'evt_h_' || k))
		FROM generate_series(0, 17999) k`, org)
	// Ended deliveries: one per fixer's task.
	exec(ins+`SELECT 'evt_e_' || k, $1, '`+EvAnswersEnded+`', 'prj_v', 'wi_' || k, NULL, 'system', 'dude', 'orchestrator',
			'{"why":"the delivery was stopped"}' FROM generate_series(0, 1999, 50) k`, org)
	// The target fixer: its publication, ten held answers, and 30,000
	// claims on others before the one on its first answer.
	exec(ins+`SELECT 'evt_tc', $1, 'git.commit_created', 'prj_v', 'wi_t', 'run_t', 'agent', 'x', 'orchestrator', '{"repo":"target","headSha":"abc"}'`, org)
	exec(ins+`SELECT 'evt_th_' || k, $1, '`+EvAnswerHeld+`', 'prj_v', 'wi_t', 'run_t', 'agent', 'x', 'orchestrator',
			jsonb_build_object('kind', 'reply', 'repo', 'target', 'number', 1, 'inReplyTo', 'issue-comment-' || k, 'text', 'Fixed.')
		FROM generate_series(0, 9) k`, org)
	exec(insC+`SELECT 'evt_tk_' || k, $1, '`+EvAnswerClaimed+`', 'prj_v', 'wi_t', 'run_t', 'system', 'x', 'orchestrator', 'evt_other_' || k,
			jsonb_build_object('answerId', 'evt_other_' || k) FROM generate_series(0, 29999) k`, org)
	exec(insC+`SELECT 'evt_tk_target', $1, '`+EvAnswerClaimed+`', 'prj_v', 'wi_t', 'run_t', 'system', 'x', 'orchestrator', 'evt_th_0', '{"answerId":"evt_th_0"}'`, org)
	// Every other task's delivery ended after the target's answers were
	// made: the organization's ends since then are many, the task's one.
	exec(ins+`SELECT 'evt_oe_' || k, $1, '`+EvAnswersEnded+`', 'prj_v', 'wi_' || k, NULL, 'system', 'dude', 'orchestrator',
			'{"why":"the pull request was merged"}' FROM generate_series(0, 1999) k`, org)
	exec(ins+`SELECT 'evt_te', $1, '`+EvAnswersEnded+`', 'prj_v', 'wi_t', NULL, 'system', 'dude', 'orchestrator', '{"why":"the delivery was stopped"}'`, org)
	// Reconciliations: 20,000 settled long ago, 50 open and due.
	exec(`INSERT INTO answer_reconciliations (answer_id, organization_id, task_id, run_id, ended, label, due_at, settled_at, outcome)
		SELECT 'evt_h_' || k, $1, 'wi_' || ((k % 2000) * 50 % 2000), 'run_' || ((k % 2000) * 50), 'the delivery was stopped', 'issue-comment-' || k,
			now() - interval '1 day', CASE WHEN k < 19950 THEN now() - interval '1 day' END, CASE WHEN k < 19950 THEN 'posted' END
		FROM generate_series(0, 19999) k`, org)
	exec(`ALTER TABLE events ENABLE TRIGGER USER`)
	exec(`VACUUM (ANALYZE) events`)
	exec(`ANALYZE runs`)
	exec(`ANALYZE answer_reconciliations`)
	var events int
	if err := owner.QueryRow(ctx, `SELECT count(*) FROM events WHERE organization_id = $1`, org).Scan(&events); err != nil {
		t.Fatal(err)
	}
	t.Logf("seeded %d events, 100,001 Runs, 20,000 answers in %s", events, time.Since(start).Round(time.Second))
	if events < 1_700_000 {
		t.Fatalf("seeded %d events, want 1.7M", events)
	}

	type query struct {
		name, sql, types string
		args             []string
		system           bool
		// Indexes the plan must use, and the most rows its scans of events
		// may filter out.
		want    []string
		removed int
		// It reads answer_reconciliations by a key (its primary key or
		// answer_reconciliations_run_idx: one row per answer probed).
		recon bool
	}
	queries := []query{
		{name: "selection", sql: fixersToPostSQL, types: "text", args: []string{"'wi_t'"},
			want: []string{"runs_task_idx", "events_run_commit_created_idx", "events_run_answer_held_idx", "events_run_answer_causation_idx"}, recon: true, removed: 100},
		{name: "unsettled", sql: unsettledSQL, types: "text", args: []string{"'run_t'"},
			want: []string{"events_run_answer_held_idx", "events_run_answer_causation_idx"}, recon: true, removed: 100},
		{name: "settled", sql: settledSQL, types: "text, text", args: []string{"'run_50'", "'evt_h_1'"},
			want: []string{"events_run_answer_causation_idx"}, removed: 100},
		{name: "claim-30k", sql: claimLookupSQL, types: "text, text", args: []string{"'run_t'", "'evt_th_0'"},
			want: []string{"events_run_answer_claims_idx"}, removed: 100},
		{name: "in-flight-30k", sql: inFlightSQL, types: "text, text, float8", args: []string{"'run_t'", "'evt_th_0'", "120"},
			want: []string{"events_run_answer_claims_idx"}, recon: true, removed: 100},
		{name: "ended-after", sql: endedAfterSQL, types: "text, text", args: []string{"'wi_t'", "'evt_th_0'"},
			want: []string{"events_task_answers_ended_idx", "events_id_key"}, removed: 100},
		{name: "end-read", sql: endHeldSQL, types: "text", args: []string{"'wi_t'"},
			want: []string{"runs_task_idx", "events_run_answer_held_idx", "events_run_answer_causation_idx", "events_run_answer_claims_idx", "events_run_commit_created_idx"}, removed: 100},
		{name: "publication-busy-fixer", sql: fixersToPostSQL, types: "text", args: []string{"'wi_50'"},
			want: []string{"runs_task_idx", "events_run_commit_created_idx", "events_run_answer_held_idx"}, removed: 100},
		{name: "sweep-due", sql: answerReconcileDue, types: "int, timestamptz", args: []string{"20", "now()"}, system: true,
			want: []string{"answer_reconciliations_due_idx"}, removed: 100},
	}
	var report strings.Builder
	for _, q := range queries {
		for _, mode := range []string{"force_generic_plan", "force_custom_plan"} {
			var raw []byte
			var generic bool
			run := func(tx pgx.Tx) error {
				if _, err := tx.Exec(ctx, `SELECT set_config('plan_cache_mode', $1, true), set_config('jit', 'off', true)`, mode); err != nil {
					return err
				}
				if _, err := tx.Exec(ctx, `PREPARE plan_q (`+q.types+`) AS `+q.sql); err != nil {
					return err
				}
				defer func() { _, _ = tx.Exec(ctx, `DEALLOCATE plan_q`) }()
				if err := tx.QueryRow(ctx, `EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON) EXECUTE plan_q (`+strings.Join(q.args, ", ")+`)`).Scan(&raw); err != nil {
					return err
				}
				return tx.QueryRow(ctx, `SELECT generic_plans > 0 FROM pg_prepared_statements WHERE name = 'plan_q'`).Scan(&generic)
			}
			var err error
			if q.system {
				err = app.InSystem(ctx, "answer-plan", run)
			} else {
				err = app.InOrg(ctx, org, run)
			}
			if err != nil {
				t.Fatalf("%s %s: %v", q.name, mode, err)
			}
			if err := os.WriteFile(filepath.Join(out, q.name+"."+mode+".json"), raw, 0o644); err != nil {
				t.Fatal(err)
			}
			var plans []struct {
				Plan map[string]any `json:"Plan"`
			}
			if err := json.Unmarshal(raw, &plans); err != nil || len(plans) != 1 {
				t.Fatalf("%s %s: %v", q.name, mode, err)
			}
			var nodes []map[string]any
			var walk func(n map[string]any)
			walk = func(n map[string]any) {
				nodes = append(nodes, n)
				kids, _ := n["Plans"].([]any)
				for _, k := range kids {
					if m, ok := k.(map[string]any); ok {
						walk(m)
					}
				}
			}
			walk(plans[0].Plan)
			used := map[string]bool{}
			removed, total := 0.0, 0.0
			for _, n := range nodes {
				if idx, ok := n["Index Name"].(string); ok {
					used[idx] = true
				}
				if n["Relation Name"] == "answer_reconciliations" && n["Node Type"] == "Seq Scan" && !q.system {
					t.Errorf("%s %s: a sequential scan of answer_reconciliations", q.name, mode)
				}
				if n["Relation Name"] == "events" {
					if n["Node Type"] == "Seq Scan" {
						t.Errorf("%s %s: a sequential scan of events", q.name, mode)
					}
					// Per loop, as EXPLAIN reports it: what one probe
					// filters, however many answers are probed.
					r, _ := n["Rows Removed by Filter"].(float64)
					loops, _ := n["Actual Loops"].(float64)
					removed = max(removed, r)
					total += r * max(loops, 1)
				}
			}
			if q.recon && !used["answer_reconciliations_pkey"] && !used["answer_reconciliations_run_idx"] {
				t.Errorf("%s %s: answer_reconciliations not read by a key (uses %v)", q.name, mode, keys(used))
			}
			for _, idx := range q.want {
				if !used[idx] {
					t.Errorf("%s %s: does not use %s (uses %v)", q.name, mode, idx, keys(used))
				}
			}
			for _, wide := range []string{"events_run_cursor_idx", "events_type_idx", "events_org_cursor_idx", "events_pkey", "events_task_cursor_idx"} {
				if used[wide] {
					t.Errorf("%s %s: reads a Run's, task's or the organization's every event (%s)", q.name, mode, wide)
				}
			}
			if int(total) > 1000 {
				t.Errorf("%s %s: %d event rows filtered out in all, want at most 1000", q.name, mode, int(total))
			}
			if int(removed) > q.removed {
				t.Errorf("%s %s: %d event rows filtered out by one probe, want at most %d", q.name, mode, int(removed), q.removed)
			}
			if (mode == "force_generic_plan") != generic {
				t.Errorf("%s %s: the plan cache's plan was generic: %v", q.name, mode, generic)
			}
			fmt.Fprintf(&report, "%-24s %-19s generic=%-5v removed/probe=%-6d removed=%-6d indexes=%v\n", q.name, mode, generic, int(removed), int(total), keys(used))
		}
	}
	rows, err := owner.Query(ctx, `SELECT c.relname, pg_relation_size(c.oid) FROM pg_class c
		WHERE c.relname IN ('events_run_answer_held_idx', 'events_run_answer_causation_idx', 'events_run_commit_created_idx',
			'events_task_answers_ended_idx', 'answer_reconciliations_pkey', 'answer_reconciliations_due_idx', 'answer_reconciliations_task_idx', 'answer_reconciliations_run_idx',
			'events_run_cursor_idx', 'events', 'events_run_answer_settled_idx', 'events_run_answer_claims_idx') ORDER BY c.relname`)
	if err != nil {
		t.Fatal(err)
	}
	report.WriteString("\nsizes (bytes):\n")
	for rows.Next() {
		var name string
		var size int64
		if err := rows.Scan(&name, &size); err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(&report, "%-36s %d\n", name, size)
	}
	rows.Close()
	t.Log("\n" + report.String())
	if err := os.WriteFile(filepath.Join(out, "summary.txt"), []byte(fmt.Sprintf("events=%d\n%s", events, report.String())), 0o644); err != nil {
		t.Fatal(err)
	}
}

func keys(m map[string]bool) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}
