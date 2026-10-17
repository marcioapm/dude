package agenttools

import (
	"context"
	"slices"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/dbtest"
)

// Migrations 135 and 136 build their indexes over the events already
// there: each is valid, on its scope and time, holds the GitHub read
// attempts and nothing else, and the hourly counts checkGitHubRead runs
// count each scope's reads of the last hour.
func TestTheGitHubReadIndexesAreBuiltAndTheHourlyCountsCountReads(t *testing.T) {
	owner, apply := dbtest.Upgrade(t, "135")
	ctx := context.Background()
	if _, err := owner.Exec(ctx, `INSERT INTO organizations (id, name, slug) VALUES ('org_g', 'G', 'g')`); err != nil {
		t.Fatal(err)
	}
	type event struct {
		id, typ, tool, session, task, age string
		read                              bool
	}
	events := []event{
		{"evt_s1", "agent.tool.attempted", "github_search", "ses_a", "", "1 minute", true},
		{"evt_s2", "agent.tool.attempted", "github_issue", "ses_a", "", "30 minutes", true},
		{"evt_s3", "agent.tool.attempted", "github_pull_request", "ses_a", "", "59 minutes", true},
		{"evt_s_old", "agent.tool.attempted", "github_issue", "ses_a", "", "61 minutes", true},
		{"evt_s_other_tool", "agent.tool.attempted", "pull_requests", "ses_a", "", "1 minute", false},
		{"evt_s_result", "agent.tool.dude", "github_issue", "ses_a", "", "1 minute", false},
		{"evt_s_read", "github.read", "github_issue", "ses_a", "", "1 minute", false},
		{"evt_s_b", "agent.tool.attempted", "github_issue", "ses_b", "", "1 minute", true},
		{"evt_t1", "agent.tool.attempted", "github_issue", "", "tsk_a", "5 minutes", true},
		{"evt_t2", "agent.tool.attempted", "github_search", "", "tsk_a", "50 minutes", true},
		{"evt_t_old", "agent.tool.attempted", "github_search", "", "tsk_a", "2 hours", true},
		{"evt_t_other_tool", "agent.tool.attempted", "request_review", "", "tsk_a", "5 minutes", false},
		{"evt_t_b", "agent.tool.attempted", "github_pull_request", "", "tsk_b", "5 minutes", true},
	}
	var reads []string
	for _, e := range events {
		if _, err := owner.Exec(ctx, `INSERT INTO events (id, organization_id, event_type, session_id, task_id, occurred_at,
			actor_type, actor_id, source, payload)
			VALUES ($1, 'org_g', $2, NULLIF($3, ''), NULLIF($4, ''), now() - $5::interval, 'agent', 'run_x', 'agent',
				jsonb_build_object('tool', $6::text))`, e.id, e.typ, e.session, e.task, e.age, e.tool); err != nil {
			t.Fatal(err)
		}
		if e.read {
			reads = append(reads, e.id)
		}
	}
	apply()

	for _, ix := range []struct{ name, scope string }{
		{"events_session_github_reads_idx", "session_id"},
		{"events_task_github_reads_idx", "task_id"},
	} {
		var valid bool
		var columns []string
		var predicate string
		if err := owner.QueryRow(ctx, `SELECT i.indisvalid,
				ARRAY(SELECT a.attname::text FROM unnest(i.indkey) WITH ORDINALITY k(attnum, n)
					JOIN pg_attribute a ON a.attrelid = i.indrelid AND a.attnum = k.attnum ORDER BY k.n),
				pg_get_expr(i.indpred, i.indrelid)
			FROM pg_index i JOIN pg_class c ON c.oid = i.indexrelid WHERE c.relname = $1`, ix.name).
			Scan(&valid, &columns, &predicate); err != nil {
			t.Fatalf("%s: %v", ix.name, err)
		}
		if !valid || !slices.Equal(columns, []string{ix.scope, "occurred_at"}) {
			t.Errorf("%s: valid=%v columns=%v", ix.name, valid, columns)
		}
		// The index's own predicate, run over the events: the GitHub read
		// attempts, of any age, and no other event. pg_index holds it as
		// text; there is no other way to evaluate it.
		rows, err := owner.Query(ctx, `SELECT id FROM events WHERE organization_id = 'org_g' AND (`+predicate+`) ORDER BY id`)
		if err != nil {
			t.Fatalf("%s predicate %q: %v", ix.name, predicate, err)
		}
		var held []string
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				t.Fatal(err)
			}
			held = append(held, id)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		rows.Close()
		if want := slices.Sorted(slices.Values(reads)); !slices.Equal(held, want) {
			t.Errorf("%s holds %v, want %v", ix.name, held, want)
		}
	}

	for _, c := range []struct {
		query, scope string
		want         int
	}{
		{githubReadsBySession, "ses_a", 3},
		{githubReadsBySession, "ses_b", 1},
		{githubReadsBySession, "ses_none", 0},
		{githubReadsByTask, "tsk_a", 2},
		{githubReadsByTask, "tsk_b", 1},
		{githubReadsByTask, "tsk_none", 0},
	} {
		var n int
		if err := owner.QueryRow(ctx, c.query, c.scope).Scan(&n); err != nil || n != c.want {
			t.Errorf("reads of %s in the last hour: %d (%v), want %d", c.scope, n, err, c.want)
		}
	}
}
