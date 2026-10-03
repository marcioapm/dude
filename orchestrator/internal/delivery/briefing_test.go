package delivery_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/dbtest"
	"github.com/marciomartins/dude/orchestrator/internal/delivery"
)

func exec(t *testing.T, c *pgx.Conn, sql string, args ...any) {
	t.Helper()
	if _, err := c.Exec(context.Background(), sql, args...); err != nil {
		t.Fatal(err)
	}
}

// The conductor's briefing is bounded whatever the task's history: a task
// with 40 Runs, 30 findings with long text, a goal of 10,000 characters
// and a long diff on record briefs in a capped note — one line per Run and
// finding, capped, counts for the rest — that carries no diff, no file
// list and no finding's text; and the person's message last, whole.
func TestTheBriefingIsBoundedAndCarriesNoDiffOrFindingText(t *testing.T) {
	app, owner := dbtest.Open(t)
	org := dbtest.Org(t, owner)
	ctx := context.Background()
	exec(t, owner, `INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES ('prj_b', $1, 'P', 'p', 'SDK')`, org)
	exec(t, owner, `INSERT INTO repositories (id, organization_id, project_id, name, url, default_branch)
		VALUES ('repo_b', $1, 'prj_b', 'sdk', 'git://x/sdk.git', 'main')`, org)
	goal := strings.Repeat("retry on 429 with backoff. ", 400)
	exec(t, owner, `INSERT INTO tasks (id, organization_id, project_id, number, title, goal, acceptance_criteria, status)
		VALUES ('wi_b', $1, 'prj_b', 31, 'Retry on 429 with backoff', $2, '["retries 429","caps the backoff at 8s"]', 'done')`, org, goal)
	exec(t, owner, `INSERT INTO task_repositories (task_id, repository_id, organization_id, access) VALUES ('wi_b', 'repo_b', $1, 'write')`, org)
	for i := range 40 {
		phase := []string{"implement", "review", "fix", "simplify"}[i%4]
		role := map[string]string{"implement": "implementer", "review": "reviewer", "fix": "implementer", "simplify": "simplifier"}[phase]
		exec(t, owner, `INSERT INTO runs (id, organization_id, project_id, task_id, attempt, status, phase, role, category, created_at, ended_at,
				heads, branch)
			VALUES ($1, $2, 'prj_b', 'wi_b', 1, 'completed', $3::run_phase, $4::agent_role, CASE WHEN $3 = 'review' THEN 'correctness' END,
				now() - make_interval(mins => 100 - $5::int), now() - make_interval(mins => 99 - $5::int),
				CASE WHEN $3 = 'simplify' THEN jsonb_build_object('sdk', jsonb_build_object('sha', 'abcdef1234567890' || $5::text)) ELSE '{}' END,
				CASE WHEN $3 = 'simplify' THEN 'dude/wi_b/attempt-1' END)`,
			fmt.Sprintf("run_b%02d", i), org, phase, role, i)
		exec(t, owner, `INSERT INTO events (id, organization_id, event_type, run_id, task_id, actor_type, actor_id, source, payload)
			VALUES ($1, $2, 'agent.message', $3, 'wi_b', 'agent', $3, 'test', jsonb_build_object('text', $4::text))`,
			fmt.Sprintf("ev_b%02d", i), org, fmt.Sprintf("run_b%02d", i),
			fmt.Sprintf("Did step %d.\n```diff\n+++ b/src/http/retry.ts\n+const SECRET_DIFF_LINE = %d\n```", i, i))
		exec(t, owner, `INSERT INTO run_diffs (run_id, organization_id, base, files, checksum)
			VALUES ($1, $2, 'abc', '[{"path":"src/SECRET_FILE_LIST.ts","status":"M","additions":1,"deletions":0,"hunks":[]}]', 'x')`,
			fmt.Sprintf("run_b%02d", i), org)
	}
	for i := range 30 {
		status := []string{"open", "resolved", "accepted"}[i%3]
		exec(t, owner, `INSERT INTO review_findings (id, organization_id, task_id, run_id, category, severity, status, file, line,
				title, description, suggested_fix, resolved_by_run_id)
			VALUES ($1, $2, 'wi_b', 'run_b01', 'correctness', $3::finding_severity, $4::finding_status, 'src/http/retry.ts', $5,
				'FINDING TITLE TEXT', 'FINDING DESCRIPTION TEXT', 'FINDING FIX TEXT', CASE WHEN $4 = 'resolved' THEN 'run_b02' END)`,
			fmt.Sprintf("fnd_b%02d", i), org, []string{"blocking", "high", "medium", "low", "note"}[i%5], status, i+1)
	}
	exec(t, owner, `INSERT INTO pull_requests (id, organization_id, project_id, task_id, repository_id, number, url, head_branch,
			base_branch, head_sha, title, state, checks, review)
		VALUES ('pr_b', $1, 'prj_b', 'wi_b', 'repo_b', 88, 'https://x/pull/88', 'dude/wi_b/attempt-1', 'main', '5d1e0aa77777',
			'Retry', 'merged', 'passing', 'approved')`, org)
	for i := range 14 {
		exec(t, owner, `INSERT INTO events (id, organization_id, event_type, task_id, actor_type, actor_id, source, payload)
			VALUES ($1, $2, 'pull_request.commented', 'wi_b', 'integration', 'github', 'test',
				jsonb_build_object('number', 88, 'repo', 'sdk', 'author', 'ana', 'body', 'PR COMMENT BODY', 'kind', 'comment'))`,
			fmt.Sprintf("ev_c%02d", i), org)
	}

	message := "why is the max backoff 8s and not 30?"
	var note string
	if err := app.InOrg(ctx, org, func(tx pgx.Tx) (err error) {
		note, err = delivery.Briefing(ctx, tx, "wi_b", "run_conductor", "Márcio", message)
		return err
	}); err != nil {
		t.Fatal(err)
	}

	if len(note) > 12_000 {
		t.Errorf("the briefing is %d bytes; it must stay bounded", len(note))
	}
	for _, leak := range []string{"SECRET_DIFF_LINE", "SECRET_FILE_LIST", "```diff", "FINDING TITLE TEXT", "FINDING DESCRIPTION TEXT",
		"FINDING FIX TEXT", "PR COMMENT BODY", strings.Repeat("retry on 429 with backoff. ", 200)} {
		if strings.Contains(note, leak) {
			t.Errorf("the briefing carries %q", leak[:min(len(leak), 40)])
		}
	}
	for _, want := range []string{
		"Márcio wrote in the Chat of SDK-31",
		"## The task\n\nSDK-31 · wi_b · status done",
		"- caps the backoff at 8s",
		"## Runs (40)", "The latest 20",
		"run_b39 · simplify · simplifier · completed · Did step 39.",
		"## Findings (30: 10 accepted, 10 open, 10 resolved)", "The first 20",
		"fixed, by run_b02", "accepted by a person", "src/http/retry.ts:",
		"sdk#88 · merged · head 5d1e0aa · checks passing · review approved · 14 feedback items",
		"- sdk: 5d1e0aa on dude/wi_b/attempt-1",
	} {
		if !strings.Contains(note, want) {
			t.Errorf("the briefing lacks %q", want)
		}
	}
	if strings.Contains(note, "run_b19 ·") || !strings.Contains(note, "run_b20 ·") {
		t.Errorf("the Runs shown are not the latest 20")
	}
	if n := strings.Count(note, "\n- run_b"); n != 20 {
		t.Errorf("%d Run lines, want 20", n)
	}
	if n := strings.Count(note, "\n- fnd_b"); n != 20 {
		t.Errorf("%d finding lines, want 20", n)
	}
	// Open findings first.
	if i, j := strings.Index(note, "· open"), strings.Index(note, "fixed, by"); i < 0 || j < 0 || i > j {
		t.Errorf("open findings are not first")
	}
	if !strings.HasSuffix(note, "## Márcio's message\n\n"+message) {
		t.Errorf("the message is not last, whole:\n%s", note[max(0, len(note)-200):])
	}
}

// A task nothing has happened on yet briefs as such: no Runs, no
// findings, no pull request, its repository at the default branch.
func TestTheBriefingOfATaskNotStarted(t *testing.T) {
	app, owner := dbtest.Open(t)
	org := dbtest.Org(t, owner)
	ctx := context.Background()
	exec(t, owner, `INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES ('prj_n', $1, 'P', 'p', 'NEW')`, org)
	exec(t, owner, `INSERT INTO repositories (id, organization_id, project_id, name, url, default_branch)
		VALUES ('repo_n', $1, 'prj_n', 'app', 'git://x/app.git', 'main')`, org)
	exec(t, owner, `INSERT INTO tasks (id, organization_id, project_id, number, title, goal)
		VALUES ('wi_n', $1, 'prj_n', 1, 'New', 'A goal long enough for a task.')`, org)
	exec(t, owner, `INSERT INTO task_repositories (task_id, repository_id, organization_id, access) VALUES ('wi_n', 'repo_n', $1, 'write')`, org)
	var note string
	if err := app.InOrg(ctx, org, func(tx pgx.Tx) (err error) {
		note, err = delivery.Briefing(ctx, tx, "wi_n", "run_c", "", "what will this touch?")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Someone wrote in the Chat of NEW-1", "status received", "## Runs (0)\n\nNone yet",
		"## Findings (0)\n\nNone.", "## Pull requests\n\nNone.", "- app: the default branch (nothing published yet)"} {
		if !strings.Contains(note, want) {
			t.Errorf("the briefing lacks %q:\n%s", want, note)
		}
	}
}
