package delivery

import (
	"cmp"
	"context"
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/db"
	"github.com/marciomartins/dude/orchestrator/internal/dbtest"
)

// goldenWorld is project GLD with task GLD-7 on epic "Billing export",
// whose peers are GLD-8 and GLD-9, and a brainstorm session linking GLD
// (two repositories) and OPS (none). Every time it writes is fixed, so a
// briefing renders the same on every run; the digest's "through", which
// is the database's now(), is checked against now() and masked (maskNow).
type goldenWorld struct {
	app   *db.DB
	owner *pgx.Conn
	org   string
}

func (w goldenWorld) exec(t *testing.T, sql string, args ...any) {
	t.Helper()
	// $1 is the organisation wherever a statement names it.
	if strings.Contains(sql, "$1") {
		args = append([]any{w.org}, args...)
	}
	if _, err := w.owner.Exec(context.Background(), sql, args...); err != nil {
		t.Fatalf("%v\n%s", err, sql)
	}
}

func (w goldenWorld) in(t *testing.T, fn func(ctx context.Context, tx pgx.Tx) (string, error)) string {
	t.Helper()
	ctx := context.Background()
	var out string
	if err := w.app.InOrg(ctx, w.org, func(tx pgx.Tx) (err error) {
		out, err = fn(ctx, tx)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return out
}

// inAt is in, and the database's now() in the same transaction: the time
// the digest ends at.
func (w goldenWorld) inAt(t *testing.T, fn func(ctx context.Context, tx pgx.Tx) (string, error)) (string, time.Time) {
	t.Helper()
	var now time.Time
	out := w.in(t, func(ctx context.Context, tx pgx.Tx) (string, error) {
		if err := tx.QueryRow(ctx, `SELECT now()`).Scan(&now); err != nil {
			return "", err
		}
		return fn(ctx, tx)
	})
	return out, now
}

// goldenNow is the digest's end, the only time a briefing takes from the
// clock.
var goldenNow = regexp.MustCompile(`→ (\d\d:\d\d) UTC\)`)

// maskNow checks that the digest in s ends at now, within a minute, and
// masks that time.
func maskNow(t *testing.T, s string, now time.Time) string {
	t.Helper()
	ms := goldenNow.FindAllStringSubmatch(s, -1)
	if len(ms) == 0 {
		t.Fatalf("no digest end time (→ HH:MM UTC)) in:\n%s", s)
	}
	for _, m := range ms {
		ok := false
		for _, d := range []time.Duration{0, -time.Minute, time.Minute} {
			ok = ok || m[1] == now.Add(d).UTC().Format("15:04")
		}
		if !ok {
			t.Errorf("the digest ends at %s, the database's now() is %s", m[1], now.UTC().Format("15:04"))
		}
	}
	return goldenNow.ReplaceAllString(s, "→ NOW UTC)")
}

// requireTestDatabase fails where dbtest.Open would skip: under -update a
// skipped briefing golden would be left as it was while the run reports ok.
// The address and credentials are dbtest's.
func requireTestDatabase(t *testing.T) {
	t.Helper()
	host := cmp.Or(os.Getenv("DUDE_TEST_PG"), "localhost:5433")
	conn, err := pgx.Connect(context.Background(), "postgres://dude:dude@"+host+"/postgres")
	if err != nil {
		t.Fatalf("-update needs the test database (DUDE_TEST_PG, default localhost:5433) to rewrite the briefing goldens: %v", err)
	}
	conn.Close(context.Background())
}

func newGoldenWorld(t *testing.T) goldenWorld {
	if *updateGoldens {
		requireTestDatabase(t)
	}
	app, owner := dbtest.Open(t)
	w := goldenWorld{app: app, owner: owner, org: dbtest.Org(t, owner)}
	w.exec(t, `INSERT INTO people (id, organization_id, name) VALUES ('per_ana', $1, 'Ana'), ('per_bo', $1, 'Bo'), ('per_cy', $1, 'Cy')`)
	w.exec(t, `INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES ('prj_g', $1, 'Billing', 'billing', 'GLD'),
		('prj_o', $1, 'Operations', 'ops', 'OPS')`)
	w.exec(t, `INSERT INTO repositories (id, organization_id, project_id, name, url, default_branch)
		VALUES ('repo_api', $1, 'prj_g', 'api', 'git://x/api.git', 'main'), ('repo_web', $1, 'prj_g', 'web', 'git://x/web.git', 'trunk')`)
	w.exec(t, `INSERT INTO epics (id, organization_id, project_id, title) VALUES ('epc_g', $1, 'prj_g', 'Billing
		export')`)
	w.exec(t, `INSERT INTO tasks (id, organization_id, project_id, number, epic_id, title, goal, acceptance_criteria, status)
		VALUES ('wi_g', $1, 'prj_g', 7, 'epc_g', 'Export invoices
		as CSV', '  Let an accountant download a month as CSV, as in ![the toolbar](attachment:att_toolbar).  ',
		'["A CSV downloads", "Columns:\n- number\n- total"]', 'running'),
		('wi_p1', $1, 'prj_g', 8, 'epc_g', 'Invoice API', 'Serve invoices.', '[]', 'running'),
		('wi_p2', $1, 'prj_g', 9, 'epc_g', 'Totals view', 'Show totals.', '[]', 'review')`)
	w.exec(t, `INSERT INTO task_repositories (task_id, repository_id, organization_id, access)
		VALUES ('wi_g', 'repo_api', $1, 'write'), ('wi_g', 'repo_web', $1, 'write')`)
	return w
}

// history gives GLD-7 Runs, findings, a pull request and a conductor's
// earlier conversation.
func (w goldenWorld) history(t *testing.T) {
	w.exec(t, `INSERT INTO runs (id, organization_id, project_id, task_id, attempt, status, phase, role, category, kind, error,
			created_at, ended_at, heads, branch)
		VALUES
		('run_impl', $1, 'prj_g', 'wi_g', 1, 'completed', 'implement', 'implementer', NULL, 'agent', NULL,
			'2026-01-02 10:00Z', '2026-01-02 10:30Z', '{"api":{"sha":"1111111aaaaaaa"}}', 'dude/wi_g/attempt-1'),
		('run_rev', $1, 'prj_g', 'wi_g', 1, 'failed', 'review', 'reviewer', 'security', 'agent', 'lux: the host went away',
			'2026-01-02 10:40Z', '2026-01-02 10:50Z', '{}', NULL),
		('run_fix', $1, 'prj_g', 'wi_g', 1, 'completed', 'fix', 'implementer', NULL, 'agent', NULL,
			'2026-01-02 11:00Z', '2026-01-02 11:20Z', '{"web":{"sha":"2222222bbbbbbb"}}', 'dude/wi_g/attempt-1'),
		('run_cond0', $1, 'prj_g', 'wi_g', 1, 'completed', NULL, 'conductor', NULL, 'agent', NULL,
			'2026-01-02 09:00Z', '2026-01-02 09:30Z', '{}', NULL)`)
	w.exec(t, `INSERT INTO events (id, organization_id, event_type, run_id, task_id, actor_type, actor_id, source, payload, occurred_at)
		VALUES
		('ev_g1', $1, 'agent.message', 'run_impl', 'wi_g', 'agent', 'run_impl', 'test', '{"text":"\n  Added the export.\nAnd tests."}', '2026-01-02 10:29Z'),
		('ev_g2', $1, 'agent.message', 'run_fix', 'wi_g', 'agent', 'run_fix', 'test', jsonb_build_object('text', 'Fixed the rounding ' || repeat('and more ', 30)), '2026-01-02 11:19Z'),
		('ev_g3', $1, 'chat.message', 'run_cond0', 'wi_g', 'human', 'per_ana', 'test', '{"text":"Is the export streamed?"}', '2026-01-02 09:01Z'),
		('ev_g4', $1, 'agent.message', 'run_cond0', 'wi_g', 'agent', 'run_cond0', 'test', '{"text":"Not yet: it buffers the month."}', '2026-01-02 09:02Z'),
		('ev_g5', $1, 'pull_request.commented', NULL, 'wi_g', 'integration', 'github', 'test', '{"number":12,"repo":"api","author":"bo","body":"x"}', '2026-01-02 12:00Z'),
		('ev_g6', $1, 'pull_request.commented', NULL, 'wi_g', 'integration', 'github', 'test', '{"number":12,"repo":"api","author":"bo","body":"y"}', '2026-01-02 12:01Z')`)
	w.exec(t, `INSERT INTO review_findings (id, organization_id, task_id, run_id, category, severity, status, file, line,
			title, description, suggested_fix, resolved_by_run_id, fix_attempts, resolution_note, created_at)
		VALUES
		('fnd_1', $1, 'wi_g', 'run_rev', 'security', 'high', 'open', 'api/export.go', 40, 'T', 'D', 'F', NULL, 2, '', '2026-01-02 10:45Z'),
		('fnd_2', $1, 'wi_g', 'run_rev', 'correctness', 'blocking', 'resolved', 'api/round.go', NULL, 'T', 'D', 'F', 'run_fix', 1, '', '2026-01-02 10:46Z'),
		('fnd_3', $1, 'wi_g', 'run_rev', 'tests', 'low', 'accepted', NULL, NULL, 'T', 'D', 'F', NULL, 0, 'Dismissed by the conductor: out of scope for this task', '2026-01-02 10:47Z'),
		('fnd_4', $1, 'wi_g', 'run_rev', 'tests', 'note', 'accepted', NULL, NULL, 'T', 'D', 'F', NULL, 0, 'person says fine', '2026-01-02 10:48Z'),
		('fnd_5', $1, 'wi_g', 'run_rev', 'api', 'medium', 'superseded', NULL, NULL, 'T', 'D', 'F', NULL, 0, '', '2026-01-02 10:49Z'),
		('fnd_6', $1, 'wi_g', 'run_rev', 'database', 'medium', 'open', 'migrations/`+strings.Repeat("deep/", 20)+`x.sql', 3, 'T', 'D', 'F', NULL, 0, '', '2026-01-02 10:50Z'),
		('fnd_7', $1, 'wi_g', 'run_rev', 'correctness', 'high', 'resolved', NULL, NULL, 'T', 'D', 'F', NULL, 1, '', '2026-01-02 10:51Z')`)
	w.exec(t, `INSERT INTO pull_requests (id, organization_id, project_id, task_id, repository_id, number, url, head_branch,
			base_branch, head_sha, title, state, checks, review, created_at)
		VALUES ('pr_g', $1, 'prj_g', 'wi_g', 'repo_api', 12, 'https://x/pull/12', 'dude/wi_g/attempt-1', 'main', '3333333ccccccc',
			'Export', 'open', 'failing', 'changes_requested', '2026-01-02 11:30Z'),
		('pr_g2', $1, 'prj_g', 'wi_g', 'repo_web', 4, 'https://x/pull/4', 'dude/wi_g/attempt-1', 'trunk', '',
			'Export', 'draft', 'pending', 'pending', '2026-01-02 11:31Z')`)
}

// coordination gives the epic notes, messages held for GLD-7 and what its
// peers did since its conductor last looked.
func (w goldenWorld) coordination(t *testing.T) {
	w.exec(t, `INSERT INTO sessions (id, organization_id, title, titled_by) VALUES ('ses_n', $1, 'Notes', 'person')`)
	w.exec(t, `INSERT INTO runs (id, organization_id, project_id, task_id, attempt, status, phase, role, kind, created_at)
		VALUES ('run_p1', $1, 'prj_g', 'wi_p1', 1, 'running', 'implement', 'implementer', 'agent', '2026-01-02 08:00Z'),
		('run_p2', $1, 'prj_g', 'wi_p2', 1, 'running', NULL, 'conductor', 'agent', '2026-01-02 08:00Z'),
		('run_gq', $1, 'prj_g', 'wi_g', 1, 'completed', 'investigate', 'investigator', 'agent', '2026-01-02 08:00Z')`)
	w.exec(t, `INSERT INTO epic_notes (id, organization_id, epic_id, n, text, by_run_id, by_person, from_session_id, by_label,
			replaces_id, replaced_at, retired_by, retired_at, created_at)
		VALUES
		('epn_1', $1, 'epc_g', 1, 'Amounts are integer cents.', NULL, 'per_ana', NULL, 'Ana', NULL, NULL, NULL, NULL, '2026-01-02 08:10Z'),
		('epn_2', $1, 'epc_g', 2, 'Old column names.', 'run_p1', NULL, NULL, 'GLD-8''s implementer', NULL, '2026-01-02 13:05Z', NULL, NULL, '2026-01-02 08:20Z'),
		('epn_3', $1, 'epc_g', 3, 'Columns: number, total
then currency.', 'run_p1', NULL, NULL, 'GLD-8''s implementer', 'epn_2', NULL, NULL, NULL, '2026-01-02 13:05Z'),
		('epn_4', $1, 'epc_g', 4, 'Ship behind a flag.', NULL, 'per_bo', 'ses_n', 'Bo', NULL, NULL, NULL, NULL, '2026-01-02 08:40Z'),
		('epn_5', $1, 'epc_g', 5, 'Use UTC.', NULL, 'per_cy', NULL, 'Cy', NULL, NULL, 'per_ana', '2026-01-02 13:10Z', '2026-01-02 08:50Z')`)
	w.exec(t, `INSERT INTO peer_messages (id, organization_id, from_task_id, from_run_id, to_task_id, reply_to, text, created_at)
		VALUES
		('pm_q', $1, 'wi_g', 'run_gq', 'wi_p1', NULL, 'Which endpoint serves a month?', '2026-01-02 13:00Z'),
		('pm_a', $1, 'wi_p1', 'run_p1', 'wi_g', 'pm_q', 'GET /invoices?month=YYYY-MM.
It pages by 500.', '2026-01-02 13:01Z'),
		('pm_h', $1, 'wi_p2', 'run_p2', 'wi_g', NULL, 'Do you round per line? '`+` || repeat('Long. ', 50), '2026-01-02 13:02Z'),
		('pm_x', $1, 'wi_p1', 'run_p1', 'wi_p2', NULL, 'Not about GLD-7.', '2026-01-02 13:03Z')`)
	w.exec(t, `INSERT INTO peer_messages (id, organization_id, from_task_id, from_run_id, to_task_id, reply_to, text, created_at)
		VALUES ('pm_r', $1, 'wi_p1', 'run_p1', 'wi_g', 'pm_h', 'Per invoice.', '2026-01-02 13:04Z')`)
	w.exec(t, `INSERT INTO events (id, organization_id, event_type, task_id, actor_type, actor_id, source, payload, occurred_at)
		VALUES ('ev_s1', $1, 'task.status_changed', 'wi_p2', 'system', 'dude', 'test', '{"status":"awaiting_input"}', '2026-01-02 13:06Z'),
		('ev_s2', $1, 'task.status_changed', 'wi_p2', 'system', 'dude', 'test', '{"status":"review"}', '2026-01-02 13:07Z')`)
	w.exec(t, `INSERT INTO conductor_digests (task_id, organization_id, through) VALUES ('wi_g', $1, '2026-01-02 12:30Z')`)
}

func (w goldenWorld) delivery(t *testing.T, step, state string) {
	w.exec(t, `INSERT INTO workflow_runs (id, organization_id, workflow_type, idempotency_key, task_id, status, step, state)
		VALUES ('wfr_g', $1, $2, 'wfr_g', 'wi_g', 'waiting', $3, $4::jsonb)`, WorkflowType, step, state)
}

func briefWith(why briefedFor, person, message string) func(ctx context.Context, tx pgx.Tx) (string, error) {
	return func(ctx context.Context, tx pgx.Tx) (string, error) {
		return briefing(ctx, tx, "wi_g", "run_cond1", person, message, why)
	}
}

func TestGoldenConductorBriefings(t *testing.T) {
	t.Run("full", func(t *testing.T) {
		w := newGoldenWorld(t)
		w.history(t)
		w.coordination(t)
		w.delivery(t, "conductor.decision", `{"decider":"conductor","decision":{"point":"before_pull_request","policy":"openPullRequest"}}`)
		got, now := w.inAt(t, briefWith(byPerson, "Ana", "Why is the export\nnot streamed?"))
		got = maskNow(t, got, now)
		golden(t, "briefing-conductor-full", got)
		// The conductor's prompt around it, as the syncer composes it.
		in := goldenTask()
		in.Tools = true
		in.Repositories = twoRepos()
		in.TaskBranch = "dude/wi_g/attempt-1"
		golden(t, "conductor-prompt-with-briefing", ConductorPrompt(got, in))
		// Told once: briefed again, it has no digest.
		again := w.in(t, briefWith(byWake, "", "dude woke you; one thing:\n- A decision waits."))
		golden(t, "briefing-conductor-wake", again)
	})
	t.Run("rebrief", func(t *testing.T) {
		w := newGoldenWorld(t)
		w.history(t)
		w.delivery(t, "review", `{"decider":"conductor"}`)
		got := w.in(t, func(ctx context.Context, tx pgx.Tx) (string, error) {
			return Rebriefing(ctx, tx, RunRef{Org: w.org, TaskID: "wi_g", RunID: "run_cond1"}, "session\nnot found")
		})
		golden(t, "briefing-conductor-rebrief", got)
		got = w.in(t, func(ctx context.Context, tx pgx.Tx) (string, error) {
			return Rebriefing(ctx, tx, RunRef{Org: w.org, TaskID: "wi_g", RunID: "run_cond1"}, "")
		})
		golden(t, "briefing-conductor-rebrief-no-reason", got)
	})
	t.Run("deliver-decides", func(t *testing.T) {
		w := newGoldenWorld(t)
		w.delivery(t, "review", `{}`)
		golden(t, "briefing-conductor-deliver-decides", w.in(t, briefWith(byPerson, "Bo", "Status?")))
	})
	t.Run("conductor-at-step", func(t *testing.T) {
		w := newGoldenWorld(t)
		w.delivery(t, "review", `{"decider":"conductor","decision":{"point":"after_review","policy":"fix","taken":{}}}`)
		golden(t, "briefing-conductor-at-step", w.in(t, briefWith(byPerson, "Bo", "Status?")))
	})
	t.Run("not-started", func(t *testing.T) {
		w := newGoldenWorld(t)
		w.exec(t, `UPDATE tasks SET epic_id = NULL, acceptance_criteria = '[]', status = 'received' WHERE id = 'wi_g'`)
		w.exec(t, `DELETE FROM task_repositories WHERE task_id = 'wi_g'`)
		golden(t, "briefing-conductor-not-started", w.in(t, briefWith(byPerson, "", "What will this touch?")))
	})
	t.Run("bounded", func(t *testing.T) {
		w := newGoldenWorld(t)
		w.exec(t, `UPDATE tasks SET epic_id = NULL, status = 'done', goal = $2, acceptance_criteria = $3::jsonb WHERE id = 'wi_g' AND organization_id = $1`,
			strings.Repeat("Stream the rows. ", 300)+"\n```go\nfor rows.Next() {",
			`[`+strings.Repeat(`"`+strings.Repeat("criterion ", 40)+`",`, 22)+`"last"]`)
		for i := range 23 {
			w.exec(t, `INSERT INTO runs (id, organization_id, project_id, task_id, attempt, status, phase, role, kind, created_at)
				VALUES ($2, $1, 'prj_g', 'wi_g', 1, 'completed', 'review', 'reviewer', 'agent', $3::timestamptz)`,
				fmt.Sprintf("run_b%02d", i), fmt.Sprintf("2026-01-03 10:%02d:00Z", i))
		}
		for i := range 22 {
			w.exec(t, `INSERT INTO review_findings (id, organization_id, task_id, run_id, category, severity, status, title, description, created_at)
				VALUES ($2, $1, 'wi_g', 'run_b00', 'tests', 'low', 'open', 'T', 'D', $3::timestamptz)`,
				fmt.Sprintf("fnd_b%02d", i), fmt.Sprintf("2026-01-03 11:%02d:00Z", i))
		}
		golden(t, "briefing-conductor-bounded", w.in(t, briefWith(byPerson, "Cy", "Is it done?")))
	})
}

func TestGoldenEpicBriefings(t *testing.T) {
	w := newGoldenWorld(t)
	w.coordination(t)
	w.exec(t, `INSERT INTO runs (id, organization_id, project_id, task_id, attempt, status, phase, role, kind)
		VALUES ('run_new_impl', $1, 'prj_g', 'wi_g', 1, 'pending', 'implement', 'implementer', 'agent'),
		('run_new_rev', $1, 'prj_g', 'wi_g', 1, 'pending', 'review', 'reviewer', 'agent')`)
	for _, c := range []struct{ run, role string }{{"run_new_rev", "reviewer"}, {"run_new_impl", "implementer"}, {"", RoleConductor}} {
		golden(t, "epic-"+c.role, w.in(t, func(ctx context.Context, tx pgx.Tx) (string, error) {
			return EpicBriefing(ctx, tx, w.org, "wi_g", c.run, c.role)
		}))
	}
	alone := newGoldenWorld(t)
	alone.exec(t, `UPDATE tasks SET epic_id = NULL WHERE id <> 'wi_g'`)
	golden(t, "epic-alone", alone.in(t, func(ctx context.Context, tx pgx.Tx) (string, error) {
		return EpicBriefing(ctx, tx, alone.org, "wi_g", "", "fixer")
	}))
}

// session sets up the brainstorm session ses_g: its title as given, its
// people, and, with linked, GLD (two repositories) and OPS (none).
func (w goldenWorld) session(t *testing.T, title, titledBy string, linked bool) {
	w.exec(t, `INSERT INTO sessions (id, organization_id, title, titled_by) VALUES ('ses_g', $1, NULLIF($2, ''), NULLIF($3, ''))`, title, titledBy)
	w.exec(t, `INSERT INTO session_people (session_id, person_id, organization_id, role, invited_at, accepted_at)
		VALUES ('ses_g', 'per_bo', $1, 'chat', '2026-01-01 10:01Z', '2026-01-01 10:05Z'),
		('ses_g', 'per_ana', $1, 'owner', '2026-01-01 10:00Z', '2026-01-01 10:00Z'),
		('ses_g', 'per_cy', $1, 'read', '2026-01-01 10:02Z', NULL)`)
	if linked {
		w.exec(t, `INSERT INTO session_projects (session_id, project_id, organization_id) VALUES ('ses_g', 'prj_g', $1), ('ses_g', 'prj_o', $1)`)
		w.exec(t, `INSERT INTO session_repositories (session_id, repository_id, organization_id) VALUES ('ses_g', 'repo_web', $1), ('ses_g', 'repo_api', $1)`)
	}
}

func TestGoldenSessionBriefings(t *testing.T) {
	for _, c := range []struct {
		name, title, by string
		linked, replay  bool
	}{
		{"untitled", "", "", false, false},
		{"person-named", "Invoice \"PDF\"\nexport", "person", true, false},
		{"agent-named", "Usage export", "agent", true, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := newGoldenWorld(t)
			w.session(t, c.title, c.by, c.linked)
			if c.replay {
				w.exec(t, `INSERT INTO runs (id, organization_id, session_id, attempt, status, kind, role, base_refs)
					VALUES ('run_s1', $1, 'ses_g', 1, 'completed', 'agent', 'brainstorm', '{}')`)
				w.exec(t, `INSERT INTO events (id, organization_id, event_type, run_id, actor_type, actor_id, source, payload)
					VALUES ('ev_s1', $1, 'chat.message', 'run_s1', 'human', 'per_ana', 'test', '{"text":"Where would usage live?"}'),
					('ev_s2', $1, 'agent.message', 'run_s1', 'agent', 'run_s1', 'test', '{"text":"In the billing API."}')`)
			}
			got := w.in(t, func(ctx context.Context, tx pgx.Tx) (string, error) {
				return sessionBriefing(ctx, tx, "ses_g", Attributed("Ana", "Let us plan the usage export."))
			})
			golden(t, "briefing-session-"+c.name, got)
			if c.linked {
				repos := []PromptRepo{{Name: "api", Path: SessionRepoPath("GLD", "api")}, {Name: "web", Path: SessionRepoPath("GLD", "web")}}
				golden(t, "brainstorm-prompt-with-briefing-"+c.name, BrainstormPrompt(got, PromptInput{Tools: true, Repositories: repos}))
			}
			rebrief := w.in(t, func(ctx context.Context, tx pgx.Tx) (string, error) {
				return Rebriefing(ctx, tx, RunRef{Org: w.org, SessionID: "ses_g", RunID: "run_s2"}, "lux restarted the agent")
			})
			golden(t, "briefing-session-rebrief-"+c.name, rebrief)
		})
	}
}

func TestGoldenQuestionWithdrawn(t *testing.T) {
	w := newGoldenWorld(t)
	w.session(t, "Usage", "agent", false)
	w.exec(t, `INSERT INTO runs (id, organization_id, session_id, attempt, status, kind, role, base_refs)
		VALUES ('run_s1', $1, 'ses_g', 1, 'running', 'agent', 'brainstorm', '{}')`)
	w.exec(t, `INSERT INTO questions (id, organization_id, run_id, prompt, to_person)
		VALUES ('qst_g', $1, 'run_s1', 'Which month
first?', 'per_bo')`)
	got := w.in(t, func(ctx context.Context, tx pgx.Tx) (string, error) {
		if err := WithdrawQuestionsTo(ctx, tx, w.org, "ses_g", "per_bo"); err != nil {
			return "", err
		}
		var text string
		err := tx.QueryRow(ctx, `SELECT text FROM directives WHERE run_id = 'run_s1'`).Scan(&text)
		return text, err
	})
	golden(t, "brainstorm-question-withdrawn", got)
}

// The digest's bounds: the newest ten messages, the rest counted, and one
// with no reply yet.
func TestGoldenDigestBounds(t *testing.T) {
	w := newGoldenWorld(t)
	w.exec(t, `INSERT INTO runs (id, organization_id, project_id, task_id, attempt, status, phase, role, kind)
		VALUES ('run_p1', $1, 'prj_g', 'wi_p1', 1, 'running', 'implement', 'implementer', 'agent')`)
	for i := range 12 {
		w.exec(t, `INSERT INTO peer_messages (id, organization_id, from_task_id, from_run_id, to_task_id, text, created_at)
			VALUES ($2, $1, 'wi_p1', 'run_p1', 'wi_g', $3, $4::timestamptz)`,
			fmt.Sprintf("pm_%02d", i), fmt.Sprintf("Question %d?", i), fmt.Sprintf("2026-01-02 14:%02d:00Z", i))
	}
	w.exec(t, `INSERT INTO conductor_digests (task_id, organization_id, through) VALUES ('wi_g', $1, '2026-01-02 12:30Z')`)
	got, now := w.inAt(t, func(ctx context.Context, tx pgx.Tx) (string, error) {
		return ConductorDigestTx(ctx, tx, w.org, "wi_g", "", "briefing")
	})
	golden(t, "digest-bounded", maskNow(t, got, now))
}
