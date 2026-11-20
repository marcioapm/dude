package delivery

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/db"
	"github.com/marciomartins/dude/orchestrator/internal/dbtest"
	"github.com/marciomartins/dude/orchestrator/internal/forge"
)

// heldFixture is a task delivering to two pull requests, target#1 and
// web#2, and an ended fixer with one reply held on each.
type heldFixture struct {
	app   *db.DB
	owner *pgx.Conn
	ref   RunRef
	held  []heldOf
}

func newHeldFixture(t *testing.T) *heldFixture {
	t.Helper()
	ctx := context.Background()
	app, owner := dbtest.Open(t)
	org := dbtest.Org(t, owner)
	for _, q := range []string{
		`INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES ('prj_h', $1, 'P', 'p', 'PH')`,
		`INSERT INTO repositories (id, organization_id, project_id, name, url, default_branch) VALUES
			('repo_t', $1, 'prj_h', 'target', 'https://github.com/acme/target.git', 'main'),
			('repo_w', $1, 'prj_h', 'web', 'https://github.com/acme/web.git', 'main')`,
		`INSERT INTO tasks (id, organization_id, project_id, number, title, goal, status) VALUES ('wi_h', $1, 'prj_h', 1, 'T', 'g', 'review')`,
		`INSERT INTO runs (id, organization_id, project_id, task_id, attempt, status, phase, role, kind)
			VALUES ('run_f', $1, 'prj_h', 'wi_h', 1, 'completed', 'fix', 'implementer', 'agent')`,
		`INSERT INTO pull_requests (id, organization_id, project_id, task_id, repository_id, number, url, head_branch, base_branch, head_sha, title) VALUES
			('pr_t', $1, 'prj_h', 'wi_h', 'repo_t', 1, 'https://github.com/acme/target/pull/1', 'dude/wi_h', 'main', 'h1', 'T'),
			('pr_w', $1, 'prj_h', 'wi_h', 'repo_w', 2, 'https://github.com/acme/web/pull/2', 'dude/wi_h', 'main', 'h2', 'T')`,
	} {
		if _, err := owner.Exec(ctx, q, org); err != nil {
			t.Fatal(err)
		}
	}
	f := &heldFixture{app: app, owner: owner, ref: RunRef{Org: org, ProjectID: "prj_h", TaskID: "wi_h", RunID: "run_f"}}
	for _, a := range []heldAnswer{
		{Kind: answerReply, Repo: "target", Number: 1, PRID: "pr_t", InReplyTo: "issue-comment-1", Text: "Fixed."},
		{Kind: answerReply, Repo: "web", Number: 2, PRID: "pr_w", InReplyTo: "issue-comment-2", Text: "Fixed too."},
	} {
		if err := app.InOrg(ctx, org, func(tx pgx.Tx) error {
			id, err := holdAnswerTx(ctx, tx, f.ref, a)
			f.held = append(f.held, heldOf{ID: id, heldAnswer: a})
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	return f
}

func (f *heldFixture) count(t *testing.T, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := f.owner.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// A pull request's end settles the fixer's held answers only once none of
// the task's pull requests is open: target#1 merged while web#2 is open
// withholds nothing, as web#2's fix may still land. Both ended: both
// withheld.
func TestAPartialEndWithholdsNothing(t *testing.T) {
	f := newHeldFixture(t)
	ctx := context.Background()
	w := &steps{s: &Store{DB: f.app}}
	state := func(id, repo string, n int, s string) PullRequestState {
		return PullRequestState{Status: forge.Status{PullRequestRef: forge.PullRequestRef{State: s, Number: n}}, ID: id, Repo: repo}
	}
	failed := func() int {
		return f.count(t, `SELECT count(*) FROM events WHERE run_id = 'run_f' AND event_type = $1`, EvAnswerFailed)
	}
	if err := w.settleHeldOnEnd(ctx, f.ref.Org, f.ref.TaskID, []PullRequestState{
		state("pr_t", "target", 1, forge.StateMerged), state("pr_w", "web", 2, forge.StateOpen)}); err != nil {
		t.Fatal(err)
	}
	if n := failed(); n != 0 {
		t.Fatalf("%d answers withheld with web#2 still open, want none", n)
	}
	if err := w.settleHeldOnEnd(ctx, f.ref.Org, f.ref.TaskID, []PullRequestState{
		state("pr_t", "target", 1, forge.StateMerged), state("pr_w", "web", 2, forge.StateClosed)}); err != nil {
		t.Fatal(err)
	}
	if n := f.count(t, `SELECT count(*) FROM events WHERE run_id = 'run_f' AND event_type = $1 AND payload->>'withheld' = 'true'`,
		EvAnswerFailed); n != 2 {
		t.Fatalf("%d answers withheld once both ended, want 2", n)
	}
}

// A reconciliation is settled once: the pass settles it withheld, then its
// poster, stalled past the reconciliation's due time, has GitHub refuse
// the write and arrives late (Store.failed). It stays withheld, said once.
func TestAReconciliationIsSettledOnce(t *testing.T) {
	f := newHeldFixture(t)
	ctx := context.Background()
	s := &Store{DB: f.app}
	h := f.held[0]
	claimed := time.Now().Add(-time.Hour)
	if err := f.app.InOrg(ctx, f.ref.Org, func(tx pgx.Tx) error {
		if err := appendClaimTx(ctx, tx, f.ref, h.ID, false, claimed); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO answer_reconciliations (answer_id, organization_id, ended, claimed_at, due_at)
			VALUES ($1, $2, $3, $4, $5)`, h.ID, f.ref.Org, withheldMerged, claimed, claimed.Add(answerReconcileAfter))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	// The pass's withheld settlement, as reconcile's settle does it.
	if err := f.app.InOrg(ctx, f.ref.Org, func(tx pgx.Tx) error {
		if err := LockChat(ctx, tx, f.ref.TaskID); err != nil {
			return err
		}
		if err := appendFailedTx(ctx, tx, f.ref, h, withheldBeforePosted, reconciledWithheld); err != nil {
			return err
		}
		return settleReconciliationTx(ctx, tx, f.ref, h.ID, reconciledWithheld, "")
	}); err != nil {
		t.Fatal(err)
	}
	// The poster, late: GitHub refused its write.
	var done FixerAnswers
	if err := s.failed(ctx, f.ref, h, "github 404: Not Found", &done); err != nil {
		t.Fatal(err)
	}
	var outcome string
	if err := f.owner.QueryRow(ctx, `SELECT outcome FROM answer_reconciliations WHERE answer_id = $1`, h.ID).Scan(&outcome); err != nil {
		t.Fatal(err)
	}
	if outcome != reconciledWithheld {
		t.Errorf("the reconciliation ended %q, want %q", outcome, reconciledWithheld)
	}
	if n := f.count(t, `SELECT count(*) FROM events WHERE task_id = 'wi_h' AND event_type = $1 AND payload->>'about' = 'answer_reconciled'
		AND payload->>'answerId' = $2`, EvChatNotice, h.ID); n != 1 {
		t.Errorf("%d notices of the reconciliation, want one", n)
	}
	if n := f.count(t, `SELECT count(*) FROM events WHERE run_id = 'run_f' AND event_type = $1 AND causation_id = $2`, EvAnswerFailed, h.ID); n != 1 {
		t.Errorf("the answer is settled %d times, want once", n)
	}
}
