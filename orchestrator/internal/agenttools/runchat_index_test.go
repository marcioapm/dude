package agenttools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/dbtest"
)

// planUses says EXPLAIN's JSON plan of query with args reads index.
func planUses(t *testing.T, owner *pgx.Conn, index, query string, args ...any) bool {
	t.Helper()
	var plan []byte
	if err := owner.QueryRow(context.Background(), `EXPLAIN (FORMAT JSON) `+query, args...).Scan(&plan); err != nil {
		t.Fatal(err)
	}
	var nodes []struct{ Plan map[string]any }
	if err := json.Unmarshal(plan, &nodes); err != nil {
		t.Fatal(err)
	}
	var uses func(map[string]any) bool
	uses = func(node map[string]any) bool {
		if node["Index Name"] == index {
			return true
		}
		children, _ := node["Plans"].([]any)
		for _, child := range children {
			if uses(child.(map[string]any)) {
				return true
			}
		}
		return false
	}
	if len(nodes) != 1 || !uses(nodes[0].Plan) {
		t.Logf("plan: %s", plan)
		return false
	}
	return true
}

// asApp runs owner's next queries as the app does, dude_app under org's
// row security, until the returned func resets it: a plan read as the
// owner has no organisation qual and is not the plan production runs.
func asApp(t *testing.T, owner *pgx.Conn, org string) func() {
	t.Helper()
	ctx := context.Background()
	if _, err := owner.Exec(ctx, `SET ROLE dude_app`); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx, `SELECT set_config('app.organization_id', $1, false)`, org); err != nil {
		t.Fatal(err)
	}
	return func() {
		if _, err := owner.Exec(ctx, `RESET ROLE`); err != nil {
			t.Fatal(err)
		}
	}
}

// invalidate marks index INVALID, as a cancelled CREATE INDEX CONCURRENTLY
// leaves it (the test database's owner is its superuser).
func invalidate(t *testing.T, owner *pgx.Conn, index string) {
	t.Helper()
	if _, err := owner.Exec(context.Background(), `UPDATE pg_index SET indisvalid = false WHERE indexrelid = to_regclass($1)`, index); err != nil {
		t.Fatal(err)
	}
}

// readMigration is migrations/name's SQL, to run it again.
func readMigration(t *testing.T, name string) string {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	sql, err := os.ReadFile(filepath.Join(filepath.Dir(file), "../../../migrations", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(sql)
}

func valid(t *testing.T, owner *pgx.Conn, index string) bool {
	t.Helper()
	var ok bool
	if err := owner.QueryRow(context.Background(), `SELECT indisvalid FROM pg_index WHERE indexrelid = to_regclass($1)`, index).Scan(&ok); err != nil {
		t.Fatalf("%s: %v", index, err)
	}
	return ok
}

// Migration 168 indexes a Run's conversation: on a Run with thousands of
// tool calls and thoughts and a few messages, run_chat's count and page
// read events_run_chat_idx and count only the messages, a blank agent
// message not among them. 169 rebuilds the index a cancelled build left
// INVALID.
func TestRunChatReadsItsConversationIndex(t *testing.T) {
	owner, apply := dbtest.Upgrade(t, "168")
	ctx := context.Background()
	if _, err := owner.Exec(ctx, `INSERT INTO organizations (id, name, slug) VALUES ('org_c', 'C', 'c')`); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx, `INSERT INTO events (id, organization_id, event_type, run_id, actor_type, actor_id, source, payload)
		SELECT 'evt_noise_' || n, 'org_c', (ARRAY['agent.tool.dude', 'agent.thought', 'agent.tool.attempted'])[n % 3 + 1],
			'run_busy', 'agent', 'run_busy', 'agent', jsonb_build_object('text', 'x')
		FROM generate_series(1, 20000) n`); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx, `INSERT INTO events (id, organization_id, event_type, run_id, actor_type, actor_id, source, payload)
		VALUES ('evt_m1', 'org_c', 'agent.message', 'run_busy', 'agent', 'run_busy', 'agent', '{"text":"done"}'),
			('evt_m2', 'org_c', 'agent.message', 'run_busy', 'agent', 'run_busy', 'agent', '{"text":"  "}'),
			('evt_m3', 'org_c', 'chat.message', 'run_busy', 'human', 'per_a', 'api', '{"text":"thanks"}'),
			('evt_m4', 'org_c', 'run.steered', 'run_busy', 'human', 'per_a', 'api', '{"text":"also"}'),
			('evt_m5', 'org_c', 'question.answered', 'run_busy', 'human', 'per_a', 'api', '{"answer":"yes"}')`); err != nil {
		t.Fatal(err)
	}
	apply()
	if _, err := owner.Exec(ctx, `ANALYZE events`); err != nil {
		t.Fatal(err)
	}
	var total int
	if err := owner.QueryRow(ctx, runChatCount, "run_busy").Scan(&total); err != nil || total != 4 {
		t.Errorf("run_chat's total %d (%v), want 4", total, err)
	}
	page := `SELECT * FROM events WHERE ` + runChatShown + ` ORDER BY cursor DESC LIMIT $2`
	check := func(when string) {
		if !planUses(t, owner, "events_run_chat_idx", runChatCount, "run_busy") {
			t.Errorf("%s: run_chat's count does not read events_run_chat_idx", when)
		}
		if !planUses(t, owner, "events_run_chat_idx", page, "run_busy", 30) {
			t.Errorf("%s: run_chat's page does not read events_run_chat_idx", when)
		}
	}
	check("after 168")

	invalidate(t, owner, "events_run_chat_idx")
	if _, err := owner.Exec(ctx, `DELETE FROM schema_migrations WHERE version = '169'`); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx, readMigration(t, "169_recover_events_run_chat_idx.sql")); err != nil {
		t.Fatal(err)
	}
	if !valid(t, owner, "events_run_chat_idx") {
		t.Error("169 left events_run_chat_idx invalid")
	}
	check("after 169's recovery")
}

// Migration 167 rebuilds a v2 GitHub reads index a cancelled 163 or 164
// left INVALID; 197 to 202 then replace both with the counted-attempt
// indexes, which the hourly counts read.
func TestAnInvalidGitHubReadIndexIsRebuiltBy167AndReplaced(t *testing.T) {
	owner, apply := dbtest.Upgrade(t, "167")
	ctx := context.Background()
	if _, err := owner.Exec(ctx, `INSERT INTO organizations (id, name, slug) VALUES ('org_g', 'G', 'g')`); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx, `INSERT INTO events (id, organization_id, event_type, session_id, task_id, occurred_at, actor_type, actor_id, source, payload)
		SELECT 'evt_' || n, 'org_g', CASE WHEN n % 50 = 0 THEN 'agent.tool.attempted' ELSE 'agent.tool.dude' END,
			'ses_' || n % 7, 'tsk_' || n % 7, now() - make_interval(mins => n % 90), 'agent', 'run_x', 'agent',
			jsonb_build_object('tool', CASE WHEN n % 100 = 0 THEN 'github_run_jobs' ELSE 'github_issue' END, 'automationId', 'aut_' || n % 7)
		FROM generate_series(1, 5000) n`); err != nil {
		t.Fatal(err)
	}
	for _, ix := range []string{"events_session_github_reads_v2_idx", "events_task_github_reads_v2_idx"} {
		invalidate(t, owner, ix)
	}
	apply()
	if _, err := owner.Exec(ctx, `ANALYZE events`); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ index, query, scope string }{
		{"events_session_tool_attempts_idx", githubReadsBySession, "ses_0"},
		{"events_task_tool_attempts_idx", githubReadsByTask, "tsk_0"},
		{"events_automation_tool_attempts_idx", githubReadsByAutomation, "aut_0"},
	} {
		if !valid(t, owner, c.index) {
			t.Errorf("%s is invalid", c.index)
		}
		reset := asApp(t, owner, "org_g")
		var n int
		if err := owner.QueryRow(ctx, c.query, c.scope).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if !planUses(t, owner, c.index, c.query, c.scope) {
			t.Errorf("the hourly count of %s does not read %s", c.scope, c.index)
		}
		reset()
	}
}
