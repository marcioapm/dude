package delivery

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/forge"
	"github.com/marciomartins/dude/orchestrator/internal/workflow"
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
		// A numeric string is not a number (tests L4).
		{`{"authorQuestionTimeoutMinutes":"5"}`, 72 * time.Hour},
		{`{"authorQuestionTimeoutMinutes":5.0}`, 5 * time.Minute},
		// Out of float8's range: the default, not an error (correctness L4).
		{`{"authorQuestionTimeoutMinutes":1e400}`, 72 * time.Hour},
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

// Two open conversation questions to alice, at T1 < T2: her comment since
// the older one is an answer, so Conversation keeps T1 (tests L1).
func TestAuthorAsksTxKeepsTheOldestConversationQuestion(t *testing.T) {
	w := newGoldenWorld(t)
	w.exec(t, `INSERT INTO runs (id, organization_id, project_id, task_id, attempt, status, role, kind)
		VALUES ('run_cond1', $1, 'prj_g', 'wi_g', 1, 'running', 'conductor', 'agent')`)
	w.exec(t, `INSERT INTO task_sources (task_id, organization_id, repository_id, kind, mode, number, head_branch, base_branch)
		VALUES ('wi_g', $1, 'repo_api', 'pr', $2, 12, 'feature/x', 'main')`, ModeReviewOnly)
	w.delivery(t, "awaitPullRequest", `{"taskId":"wi_g","projectId":"prj_g","attempt":1}`)
	t1, t2 := time.Date(2026, 11, 15, 10, 0, 0, 0, time.UTC), time.Date(2026, 11, 15, 11, 0, 0, 0, time.UTC)
	// The newer inserted first: the fold, not the insertion, picks T1.
	w.exec(t, `INSERT INTO review_questions (id, organization_id, task_id, run_id, n, category, question, addressee, asked_of, created_at)
		VALUES ('rvq_2', $1, 'wi_g', 'run_cond1', 1, 'conductor', 'Q2?', 'author', 'alice', $2),
		('rvq_1', $1, 'wi_g', 'run_cond1', 2, 'conductor', 'Q1?', 'author', 'alice', $3)`, t2, t1)
	var asks *AuthorAsks
	w.in(t, func(ctx context.Context, tx pgx.Tx) (_ string, err error) {
		asks, err = AuthorAsksTx(ctx, tx, "wi_g")
		return "", err
	})
	if asks == nil || !asks.Conversation["alice"].Equal(t1) || len(asks.Open) != 2 {
		t.Fatalf("the open questions: %+v", asks)
	}
	between := forge.Feedback{Author: "alice", Body: "No.", Kind: forge.KindComment, CreatedAt: "2026-11-15T10:30:00Z"}
	if !asks.Answers(between) {
		t.Error("alice's comment between the two questions is not an answer")
	}
}

// withAskTimer wakes the wait for its own attempt's open author question
// only: attempt 1's sets no timer in attempt 2 (tests L3).
func TestAskTimerIsTheAttempts(t *testing.T) {
	w := newGoldenWorld(t)
	w.exec(t, `INSERT INTO runs (id, organization_id, project_id, task_id, attempt, status, role, kind)
		VALUES ('run_cond1', $1, 'prj_g', 'wi_g', 1, 'completed', 'conductor', 'agent')`)
	w.exec(t, `INSERT INTO review_questions (id, organization_id, task_id, run_id, n, category, question, addressee, asked_of, created_at)
		VALUES ('rvq_1', $1, 'wi_g', 'run_cond1', 1, 'conductor', 'Q?', 'author', 'alice', '2026-11-15 10:00Z')`)
	s := &steps{s: &Store{DB: w.app}}
	sc := workflow.StepContext{OrganizationID: w.org, WorkflowRunID: "wfr_g"}
	for _, c := range []struct {
		attempt int
		want    time.Time
	}{{1, time.Date(2026, 11, 18, 10, 0, 0, 0, time.UTC)}, {2, time.Time{}}} {
		st := &State{TaskID: "wi_g", ProjectID: "prj_g", Attempt: c.attempt}
		res, err := s.withAskTimer(context.Background(), sc, st, workflow.Result{AwaitSignals: []string{SignalPRFeedback}})
		if err != nil {
			t.Fatal(err)
		}
		if !res.SleepUntil.Equal(c.want) {
			t.Errorf("attempt %d: sleeps until %s, want %s", c.attempt, res.SleepUntil, c.want)
		}
	}
}

// A writing task started over keeps attempt 1's reviewer questions to the
// person and the conductor open in attempt 2 (the 248 trigger withdraws
// conductor rows only on a handover): attempt 2's conductor settles them,
// as at main. Only an author row is scoped to its attempt (tests O2).
func TestAWritingConductorSettlesAnEarlierAttemptsReviewerQuestion(t *testing.T) {
	w := newGoldenWorld(t)
	w.exec(t, `INSERT INTO runs (id, organization_id, project_id, task_id, attempt, status, role, kind)
		VALUES ('run_rev1', $1, 'prj_g', 'wi_g', 1, 'completed', 'reviewer', 'agent'),
		('run_cond2', $1, 'prj_g', 'wi_g', 2, 'running', 'conductor', 'agent')`)
	w.exec(t, `INSERT INTO review_questions (id, organization_id, task_id, run_id, n, category, question, addressee)
		VALUES ('rvq_p', $1, 'wi_g', 'run_rev1', 1, 'security', 'Who may export?', 'person'),
		('rvq_c', $1, 'wi_g', 'run_rev1', 2, 'security', 'UTC months?', 'conductor')`)
	w.delivery(t, "review", `{"taskId":"wi_g","projectId":"prj_g","attempt":2,"decider":"conductor"}`)
	ref := RunRef{Org: w.org, ProjectID: "prj_g", TaskID: "wi_g", RunID: "run_cond2"}
	for _, c := range []struct {
		id, answer string
		withdraw   bool
		want       string
	}{{"rvq_p", "Only its own.", false, "answered"}, {"rvq_c", "", true, "withdrawn"}} {
		w.in(t, func(ctx context.Context, tx pgx.Tx) (string, error) {
			out, err := ConductAnswerReviewQuestion(ctx, tx, ref, c.id, c.answer, c.withdraw, "moot")
			if err == nil && out.Status != c.want {
				t.Errorf("%s: %s, want %s", c.id, out.Status, c.want)
			}
			return "", err
		})
	}
	if n := w.count(t, `SELECT count(*) FROM review_questions WHERE task_id = 'wi_g' AND status = 'open'`); n != 0 {
		t.Errorf("%d of attempt 1's reviewer questions still open", n)
	}
}

// A lost ask's own comment, recorded by an orchestrator whose clock runs
// 30s behind the database's, is still found: RecoverAsksTx's grace
// (tests O5).
func TestRecoverAsksTakesAnOwnCommentWithinTheGrace(t *testing.T) {
	w := newGoldenWorld(t)
	w.exec(t, `INSERT INTO runs (id, organization_id, project_id, task_id, attempt, status, role, kind)
		VALUES ('run_cond1', $1, 'prj_g', 'wi_g', 1, 'running', 'conductor', 'agent')`)
	w.exec(t, `INSERT INTO review_questions (id, organization_id, task_id, run_id, n, category, question, addressee, asked_of, created_at)
		VALUES ('rvq_l', $1, 'wi_g', 'run_cond1', 1, 'conductor', 'Q?', 'author', 'alice', '2026-11-15 10:00:00Z')`)
	w.exec(t, `INSERT INTO events (id, organization_id, event_type, task_id, actor_type, actor_id, source, payload, occurred_at)
		VALUES ('ev_own', $1, $2, 'wi_g', 'integration', 'github', 'test',
			jsonb_build_object('own', true, 'feedbackId', 'c77', 'url', 'https://x/c77', 'body', '@alice Q?'||chr(10)||$3::text),
			'2026-11-15 09:59:30Z')`, EvPullRequestCommented, askMarker("rvq_l"))
	w.in(t, func(ctx context.Context, tx pgx.Tx) (string, error) { return "", RecoverAsksTx(ctx, tx, "wi_g") })
	if n := w.count(t, `SELECT count(*) FROM review_questions WHERE id = 'rvq_l' AND github_comment_id = 'c77' AND github_url = 'https://x/c77'`); n != 1 {
		t.Error("the comment 30s before the question's created_at was not recovered")
	}
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
