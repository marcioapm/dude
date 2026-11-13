package delivery

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/dbtest"
)

// deferredFixture is a task with an implementer whose turn 1 deferred
// bench, and one open pull request on repository web at a head the test
// moves (setHead); ask asks the conductor to keep it at the current head.
type deferredFixture struct {
	t     *testing.T
	app   interface{ InOrg(context.Context, string, func(pgx.Tx) error) error }
	owner *pgx.Conn
	org   string
}

func newDeferredFixture(t *testing.T) *deferredFixture {
	app, owner := dbtest.Open(t)
	org := dbtest.Org(t, owner)
	for _, q := range []string{
		`INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES ('prj_d', $1, 'P', 'p', 'PD')`,
		`INSERT INTO repositories (id, organization_id, project_id, name, url, default_branch) VALUES ('repo_d', $1, 'prj_d', 'web', 'git://x/web.git', 'main')`,
		`INSERT INTO tasks (id, organization_id, project_id, number, title, goal, status) VALUES ('wi_d', $1, 'prj_d', 1, 'T', 'g', 'review')`,
		`INSERT INTO runs (id, organization_id, project_id, task_id, attempt, status, phase, role, kind)
			VALUES ('run_d', $1, 'prj_d', 'wi_d', 1, 'completed', 'implement', 'implementer', 'agent')`,
		`INSERT INTO events (id, organization_id, project_id, run_id, task_id, event_type, actor_type, actor_id, source, payload)
			VALUES ('evt_d_j', $1, 'prj_d', 'run_d', 'wi_d', 'run.checks.judged', 'system', 'dude', 'orchestrator',
			'{"turn": 1, "verified": true, "checks": [{"command": "bench", "result": "deferred", "reason": "later"}]}')`,
		`INSERT INTO pull_requests (id, organization_id, project_id, task_id, repository_id, number, url, head_branch, base_branch, head_sha, title)
			VALUES ('pr_d', $1, 'prj_d', 'wi_d', 'repo_d', 1, 'https://x/pull/1', 'dude/wi_d', 'main', 'h1', 'T')`,
	} {
		if _, err := owner.Exec(context.Background(), q, org); err != nil {
			t.Fatal(err)
		}
	}
	return &deferredFixture{t: t, app: app, owner: owner, org: org}
}

func (f *deferredFixture) setHead(head string) {
	f.t.Helper()
	if _, err := f.owner.Exec(context.Background(), `UPDATE pull_requests SET head_sha = $1 WHERE id = 'pr_d'`, head); err != nil {
		f.t.Fatal(err)
	}
}

// ask asks at head (moving the pull request there first); the ask's key.
func (f *deferredFixture) ask(head string) string {
	f.t.Helper()
	f.setHead(head)
	heads := map[string]string{"web": head}
	ctx := context.Background()
	if err := f.app.InOrg(ctx, f.org, func(tx pgx.Tx) error {
		s, err := readDeferredTx(ctx, tx, "wi_d", 1, true)
		if err != nil {
			return err
		}
		_, err = askDeferredTx(ctx, tx, f.org, "wi_d", 1, heads, s)
		return err
	}); err != nil {
		f.t.Fatal(err)
	}
	return DischargeKey("run_d", 1, 1, heads)
}

// answer answers the ask key backed, as the conductor.
func (f *deferredFixture) answer(key string) error {
	ctx := context.Background()
	return f.app.InOrg(ctx, f.org, func(tx pgx.Tx) error {
		_, err := conductDischargeTx(ctx, tx, RunRef{Org: f.org, ProjectID: "prj_d", TaskID: "wi_d", RunID: "run_c"},
			DecideChecks{RunID: "run_d", Turn: 1, Ask: key, Checks: []ConductedCheck{{Command: "bench", Verdict: VerdictBacked, Reason: "bench at h1: success"}}},
			"prj_d")
		return err
	})
}

// standing is where bench stands at head, read afresh.
func (f *deferredFixture) standing(head string) string {
	f.t.Helper()
	var out string
	ctx := context.Background()
	if err := f.app.InOrg(ctx, f.org, func(tx pgx.Tx) error {
		s, err := readDeferredTx(ctx, tx, "wi_d", 1, true)
		if err != nil {
			return err
		}
		if len(s.checks) != 1 {
			return fmt.Errorf("%d deferred checks", len(s.checks))
		}
		out = s.standing(s.checks[0], headsID(map[string]string{"web": head}))
		return nil
	}); err != nil {
		f.t.Fatal(err)
	}
	return out
}

// Heads that move H1, H2, H1: the ask at H1 is the one the gate stands on
// again (no new ask is made there, the key is the same), so an answer to it
// is accepted and keeps the check at H1. The ask at H2 is the one refused.
func TestAnAskAtHeadsMovedAwayAndBackIsAnswerable(t *testing.T) {
	f := newDeferredFixture(t)
	k1 := f.ask("h1")
	k2 := f.ask("h2")
	if again := f.ask("h1"); again != k1 {
		t.Fatalf("asked again at H1 as %s, want %s", again, k1)
	}
	if n := countEvents(t, f.owner, `SELECT count(*) FROM events WHERE task_id = 'wi_d' AND event_type = 'run.checks.asked'`); n != 2 {
		t.Fatalf("%d asks, want one at H1 and one at H2", n)
	}
	if got := f.standing("h1"); got != deferredAsked {
		t.Fatalf("at H1 before the answer: %s", got)
	}
	var r Refusal
	if err := f.answer(k2); !errors.As(err, &r) || !strings.Contains(r.Msg, fmt.Sprintf("ask %q is superseded", k2)) {
		t.Fatalf("the answer to the ask at H2, the pull request at H1: %v", err)
	}
	if err := f.answer(k1); err != nil {
		t.Fatalf("the answer to the ask at H1, the pull request at H1: %v", err)
	}
	if got := f.standing("h1"); got != deferredKept {
		t.Errorf("at H1 after the answer: %s", got)
	}
	if got := f.standing("h2"); got == deferredKept {
		t.Errorf("kept at H2 on an answer about H1")
	}
}

func countEvents(t *testing.T, c *pgx.Conn, sql string) int {
	t.Helper()
	var n int
	if err := c.QueryRow(context.Background(), sql).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
