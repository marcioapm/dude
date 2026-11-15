package delivery

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/forge"
)

// The project's authorQuestionTimeoutMinutes: unset, zero, negative or not
// a whole number of minutes is the default 3 days, and never an error (a
// wait pass reads it every time); a positive whole number is minutes.
func TestAuthorQuestionTimeoutFromThePolicy(t *testing.T) {
	w := newGoldenWorld(t)
	s := &Store{}
	for _, c := range []struct {
		policy string
		want   time.Duration
	}{
		{`{}`, 72 * time.Hour},
		{`{"authorQuestionTimeoutMinutes":5}`, 5 * time.Minute},
		{`{"authorQuestionTimeoutMinutes":0}`, 72 * time.Hour},
		{`{"authorQuestionTimeoutMinutes":-3}`, 72 * time.Hour},
		{`{"authorQuestionTimeoutMinutes":"x"}`, 72 * time.Hour},
		{`{"authorQuestionTimeoutMinutes":"1.5"}`, 72 * time.Hour},
		{`{"authorQuestionTimeoutMinutes":1.5}`, 72 * time.Hour},
		{`{"authorQuestionTimeoutMinutes":null}`, 72 * time.Hour},
		{`{"authorQuestionTimeoutMinutes":99999999999}`, 72 * time.Hour},
	} {
		w.exec(t, `UPDATE projects SET delivery_policy = $2::jsonb WHERE id = 'prj_g' AND organization_id = $1`, c.policy)
		var got time.Duration
		w.in(t, func(ctx context.Context, tx pgx.Tx) (_ string, err error) {
			got, err = s.authorQuestionTimeoutTx(ctx, tx, "prj_g")
			return "", err
		})
		if got != c.want {
			t.Errorf("%s: %s, want %s", c.policy, got, c.want)
		}
	}
	// The Store's own (tests) over the project's.
	w.exec(t, `UPDATE projects SET delivery_policy = '{"authorQuestionTimeoutMinutes":5}' WHERE id = 'prj_g' AND organization_id = $1`)
	s.AuthorQuestionTimeout = time.Second
	w.in(t, func(ctx context.Context, tx pgx.Tx) (string, error) {
		got, err := s.authorQuestionTimeoutTx(ctx, tx, "prj_g")
		if got != time.Second {
			t.Errorf("the Store's override: %s", got)
		}
		return "", err
	})
}

// CheckAskAuthorTx's refusals, each with no question recorded: a writing
// task (the second layer behind the tool's absence), an empty or too long
// question, a pull request whose author dude does not know, and a finding
// with no thread of dude's.
func TestCheckAskAuthorRefusals(t *testing.T) {
	w := newGoldenWorld(t)
	w.exec(t, `UPDATE repositories SET url = 'https://github.com/acme/api' WHERE id = 'repo_api'`)
	w.exec(t, `INSERT INTO runs (id, organization_id, project_id, task_id, attempt, status, role, kind)
		VALUES ('run_cond1', $1, 'prj_g', 'wi_g', 1, 'running', 'conductor', 'agent')`)
	w.exec(t, `INSERT INTO pull_requests (id, organization_id, project_id, task_id, repository_id, number, url, head_branch,
			base_branch, head_sha, title, state, checks)
		VALUES ('pr_g', $1, 'prj_g', 'wi_g', 'repo_api', 12, 'https://github.com/acme/api/pull/12', 'feature/x', 'main', 'abc', 'X', 'open', 'passing')`)
	ref := RunRef{Org: w.org, ProjectID: "prj_g", TaskID: "wi_g", RunID: "run_cond1"}
	gh := &forge.GitHub{}
	check := func(in AskAuthor) error {
		var err error
		_ = w.app.InOrg(context.Background(), w.org, func(tx pgx.Tx) error {
			_, err = CheckAskAuthorTx(context.Background(), tx, ref, gh, in)
			return nil
		})
		return err
	}
	ask := AskAuthor{PR: "12", Question: "Why lower case?"}
	if err := check(ask); err == nil || err.Error() != rAskAuthorReviewOnly {
		t.Fatalf("a writing task: %v", err)
	}
	w.exec(t, `INSERT INTO task_sources (task_id, organization_id, repository_id, kind, mode, number, head_branch, base_branch)
		VALUES ('wi_g', $1, 'repo_api', 'pr', $2, 12, 'feature/x', 'main')`, ModeReviewOnly)
	w.delivery(t, "awaitPullRequest", `{"taskId":"wi_g","projectId":"prj_g","attempt":1}`)
	for _, c := range []struct {
		in   AskAuthor
		want string
	}{
		{AskAuthor{PR: "12", Question: "  "}, rAskAuthorQuestionRequired},
		{AskAuthor{PR: "12", Question: strings.Repeat("x", maxAuthorQuestion+1)}, "Ask one thing, in at most 4000 bytes"},
		{ask, "dude does not know who opened pull request #12"},
	} {
		if err := check(c.in); err == nil || !IsRefusal(err) || !strings.HasPrefix(err.Error(), c.want) {
			t.Errorf("%.40q: %v, want %q", c.in.Question, err, c.want)
		}
	}
	w.exec(t, `UPDATE workflow_runs SET state = state || '{"source":{"kind":"pr","repositoryId":"repo_api","repository":"api","headBranch":"feature/x","baseBranch":"main","author":"alice"}}'::jsonb WHERE id = 'wfr_g'`)
	if err := check(AskAuthor{PR: "12", Question: "Why?", FindingID: "fnd_none"}); err == nil || !IsRefusal(err) ||
		!strings.HasPrefix(err.Error(), "fnd_none has no thread of dude's on the pull request") {
		t.Errorf("a finding with no thread: %v", err)
	}
	var n int
	if err := w.owner.QueryRow(context.Background(), `SELECT count(*) FROM review_questions WHERE task_id = 'wi_g'`).Scan(&n); err != nil || n != 0 {
		t.Errorf("%d questions recorded by refusals (%v)", n, err)
	}
	// And asked: the one that passes every check is recorded open.
	if err := check(ask); err != nil {
		t.Fatalf("a question that passes: %v", err)
	}
}
