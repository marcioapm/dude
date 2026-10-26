package agenttools

import (
	"context"
	"slices"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/dbtest"
)

// Migrations 147 through 202 rebuild their indexes over the events already
// there: the counted-attempt indexes (197-199) are valid, on their scope
// and time, hold every counted tool attempt and nothing else, the indexes
// before them are gone, and the hourly counts checkGitHubRead and
// chat_history run count each scope's reads of the last hour along them.
func TestTheGitHubReadIndexesAreBuiltAndTheHourlyCountsCountReads(t *testing.T) {
	owner, apply := dbtest.Upgrade(t, "147")
	ctx := context.Background()
	if _, err := owner.Exec(ctx, `INSERT INTO organizations (id, name, slug) VALUES ('org_g', 'G', 'g')`); err != nil {
		t.Fatal(err)
	}
	type event struct {
		id, typ, tool, session, task, age string
	}
	events := []event{
		{"evt_s1", "agent.tool.attempted", "github_search", "ses_a", "", "1 minute"},
		{"evt_s2", "agent.tool.attempted", "github_issue", "ses_a", "", "30 minutes"},
		{"evt_s3", "agent.tool.attempted", "github_pull_request", "ses_a", "", "59 minutes"},
		{"evt_s4", "agent.tool.attempted", "github_check_log", "ses_a", "", "2 minutes"},
		{"evt_s5", "agent.tool.attempted", "refresh_pull_requests", "ses_a", "", "3 minutes"},
		{"evt_s6", "agent.tool.attempted", "github_run_jobs", "ses_a", "", "4 minutes"},
		{"evt_s_old", "agent.tool.attempted", "github_issue", "ses_a", "", "61 minutes"},
		{"evt_s_chat", "agent.tool.attempted", "chat_history", "ses_a", "", "1 minute"},
		{"evt_s_result", "agent.tool.dude", "github_issue", "ses_a", "", "1 minute"},
		{"evt_s_read", "github.read", "github_issue", "ses_a", "", "1 minute"},
		{"evt_s_b", "agent.tool.attempted", "github_issue", "ses_b", "", "1 minute"},
		{"evt_t1", "agent.tool.attempted", "github_issue", "", "tsk_a", "5 minutes"},
		{"evt_t2", "agent.tool.attempted", "github_search", "", "tsk_a", "50 minutes"},
		{"evt_t3", "agent.tool.attempted", "github_check_log", "", "tsk_a", "5 minutes"},
		{"evt_t4", "agent.tool.attempted", "refresh_pull_requests", "", "tsk_a", "5 minutes"},
		{"evt_t_refresh_result", "agent.tool.dude", "refresh_pull_requests", "", "tsk_a", "5 minutes"},
		{"evt_t_old", "agent.tool.attempted", "github_search", "", "tsk_a", "2 hours"},
		{"evt_t_other_tool", "agent.tool.attempted", "request_review", "", "tsk_a", "5 minutes"},
		{"evt_t_b", "agent.tool.attempted", "github_pull_request", "", "tsk_b", "5 minutes"},
	}
	var attempts []string
	for _, e := range events {
		if _, err := owner.Exec(ctx, `INSERT INTO events (id, organization_id, event_type, session_id, task_id, occurred_at,
			actor_type, actor_id, source, payload)
			VALUES ($1, 'org_g', $2, NULLIF($3, ''), NULLIF($4, ''), now() - $5::interval, 'agent', 'run_x', 'agent',
				jsonb_build_object('tool', $6::text, 'automationId', CASE WHEN $3 = 'ses_a' THEN 'aut_a' END))`,
			e.id, e.typ, e.session, e.task, e.age, e.tool); err != nil {
			t.Fatal(err)
		}
		if e.typ == "agent.tool.attempted" {
			attempts = append(attempts, e.id)
		}
	}
	// The same scopes' other events, many of them: a scan of the scope's
	// own index (events_session_cursor_idx and the like) costs far more
	// than the partial index's few rows, so the plan is not a coin toss.
	if _, err := owner.Exec(ctx, `INSERT INTO events (id, organization_id, event_type, session_id, task_id, occurred_at,
		actor_type, actor_id, source, payload)
		SELECT 'evt_noise_' || n, 'org_g', 'agent.tool.dude', 'ses_a', 'tsk_a', now() - make_interval(mins => n % 120), 'agent', 'run_x', 'agent',
			jsonb_build_object('tool', 'github_issue', 'automationId', 'aut_a')
		FROM generate_series(1, 20000) n`); err != nil {
		t.Fatal(err)
	}
	apply()
	if _, err := owner.Exec(ctx, `VACUUM ANALYZE events`); err != nil {
		t.Fatal(err)
	}

	for _, ix := range []struct{ name, scope string }{
		{"events_session_tool_attempts_idx", "session_id"},
		{"events_task_tool_attempts_idx", "task_id"},
		{"events_automation_tool_attempts_idx", ""},
	} {
		var columns []string
		var predicate string
		if !valid(t, owner, ix.name) {
			t.Errorf("%s is not valid", ix.name)
		}
		if err := owner.QueryRow(ctx, `SELECT
				ARRAY(SELECT COALESCE(a.attname::text, '') FROM unnest(i.indkey) WITH ORDINALITY k(attnum, n)
					LEFT JOIN pg_attribute a ON a.attrelid = i.indrelid AND a.attnum = k.attnum ORDER BY k.n),
				pg_get_expr(i.indpred, i.indrelid)
			FROM pg_index i JOIN pg_class c ON c.oid = i.indexrelid WHERE c.relname = $1`, ix.name).
			Scan(&columns, &predicate); err != nil {
			t.Fatalf("%s: %v", ix.name, err)
		}
		if !slices.Equal(columns, []string{ix.scope, "occurred_at"}) {
			t.Errorf("%s: columns %v", ix.name, columns)
		}
		// The index's own predicate, run over the events: every counted
		// tool attempt, of any tool and age, and no other event. pg_index
		// holds it as text; there is no other way to evaluate it.
		var held []string
		rows, err := owner.Query(ctx, `SELECT id FROM events WHERE organization_id = 'org_g' AND (`+predicate+`) ORDER BY id`)
		if err != nil {
			t.Fatalf("%s predicate %q: %v", ix.name, predicate, err)
		}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				t.Fatal(err)
			}
			held = append(held, id)
		}
		rows.Close()
		if want := slices.Sorted(slices.Values(attempts)); !slices.Equal(held, want) {
			t.Errorf("%s holds %v, want %v", ix.name, held, want)
		}
	}
	var old int
	if err := owner.QueryRow(ctx, `SELECT count(*) FROM pg_class WHERE relname IN ('events_session_github_reads_idx', 'events_task_github_reads_idx',
		'events_session_github_reads_v2_idx', 'events_task_github_reads_v2_idx', 'events_automation_github_reads_idx')`).
		Scan(&old); err != nil || old != 0 {
		t.Errorf("%d of the tool-list indexes remain (%v)", old, err)
	}

	byAutomation := `SELECT count(*) FROM events WHERE payload->>'automationId' = $1` + githubReadsInHour
	for _, c := range []struct {
		query, scope, index string
		want                int
	}{
		{githubReadsBySession, "ses_a", "events_session_tool_attempts_idx", 6},
		{githubReadsBySession, "ses_b", "events_session_tool_attempts_idx", 1},
		{githubReadsBySession, "ses_none", "events_session_tool_attempts_idx", 0},
		{githubReadsByTask, "tsk_a", "events_task_tool_attempts_idx", 4},
		{githubReadsByTask, "tsk_b", "events_task_tool_attempts_idx", 1},
		{githubReadsByTask, "tsk_none", "events_task_tool_attempts_idx", 0},
		{byAutomation, "aut_a", "events_automation_tool_attempts_idx", 6},
		{chatReadsInHour, "ses_a", "events_session_tool_attempts_idx", 1},
	} {
		var n int
		if err := owner.QueryRow(ctx, c.query, c.scope).Scan(&n); err != nil || n != c.want {
			t.Errorf("reads of %s in the last hour: %d (%v), want %d", c.scope, n, err, c.want)
		}
		if !planUses(t, owner, c.index, c.query, c.scope) {
			t.Errorf("the hourly count of %s does not read %s", c.scope, c.index)
		}
	}
}
