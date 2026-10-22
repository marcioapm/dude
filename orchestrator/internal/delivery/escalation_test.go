package delivery

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/dbtest"
	"github.com/marciomartins/dude/orchestrator/internal/forge"
	"github.com/marciomartins/dude/orchestrator/internal/workflow"
)

func TestConvergenceDecisionNotesAndScope(t *testing.T) {
	for _, action := range []string{"retry", "accept", "amend"} {
		t.Run(action, func(t *testing.T) {
			app, owner := dbtest.Open(t)
			org := dbtest.Org(t, owner)
			ctx := context.Background()
			for _, query := range []string{
				`INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES ('prj_e', $1, 'P', 'p', 'P')`,
				`INSERT INTO tasks (id, organization_id, project_id, number, title, goal, status, acceptance_criteria) VALUES ('wi_e', $1, 'prj_e', 1, 'Greet', 'greet', 'awaiting_input', '["original"]')`,
				`INSERT INTO runs (id, organization_id, project_id, task_id, attempt, phase, status) VALUES ('review_e', $1, 'prj_e', 'wi_e', 1, 'review', 'completed'), ('old_e', $1, 'prj_e', 'wi_e', 2, 'review', 'completed'), ('test_e', $1, 'prj_e', 'wi_e', 1, 'test', 'completed')`,
				`INSERT INTO review_findings (id, organization_id, task_id, run_id, category, severity, title, description, acceptance) VALUES ('named', $1, 'wi_e', 'review_e', 'correctness', 'high', 'Named', 'problem', 'before'), ('other', $1, 'wi_e', 'review_e', 'correctness', 'high', 'Other', 'problem', 'before'), ('old', $1, 'wi_e', 'old_e', 'correctness', 'high', 'Old', 'problem', 'before'), ('tester', $1, 'wi_e', 'test_e', 'acceptance', 'blocking', 'Tester', 'problem', 'before')`,
			} {
				if _, err := owner.Exec(ctx, query, org); err != nil {
					t.Fatal(err)
				}
			}
			// A tester finding named in the escalation stays the next test's to judge.
			st := &State{TaskID: "wi_e", ProjectID: "prj_e", Attempt: 1, Policy: DefaultPolicy(), Iteration: 5,
				Escalation: &Escalation{Reason: ReasonNotConverging, Step: "fix", Detail: LoopExit{FindingIDs: []string{"named", "old", "tester"}}}}
			raw, _ := json.Marshal(st)
			if _, err := owner.Exec(ctx, `INSERT INTO workflow_runs (id, organization_id, workflow_type, idempotency_key, task_id, status, step, state) VALUES ('wf_e', $1, $2, 'e', 'wi_e', 'waiting', 'decide', $3)`, org, WorkflowType, raw); err != nil {
				t.Fatal(err)
			}
			decide := func(note string) error {
				return app.InOrg(ctx, org, func(tx pgx.Tx) error {
					return DecideEscalationTx(ctx, tx, org, "wi_e", EscalationDecision{Action: action, Note: note, ActorType: "system", ActorID: "test"})
				})
			}
			if err := decide(" \n "); err == nil {
				t.Fatal("blank direction accepted")
			}
			note := "Different observable behaviour"
			if err := decide(note); err != nil {
				t.Fatal(err)
			}
			if err := owner.QueryRow(ctx, `SELECT state FROM workflow_runs WHERE id = 'wf_e'`).Scan(&raw); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(raw, st); err != nil {
				t.Fatal(err)
			}
			at := st.Escalation.Decided.At
			if at.IsZero() || time.Since(at) > time.Minute {
				t.Fatalf("missing decision timestamp: %v", at)
			}
			w := &steps{s: &Store{DB: app}}
			res, err := w.decide(ctx, workflow.StepContext{OrganizationID: org, Step: "decide", State: raw})
			if err != nil {
				t.Fatal(err)
			}
			after := res.State.(*State)
			if !after.ConvergenceFrom.Equal(at) {
				t.Fatalf("floor %v != decision %v", after.ConvergenceFrom, at)
			}
			want := "fix"
			if action == "accept" {
				want = "reviewExit"
			} else if after.Iteration != 0 || after.ExtraFixAttempts != st.Policy.MaxAttemptsPerFinding {
				t.Fatalf("budget not replenished: %+v", after)
			}
			if res.Next != want {
				t.Fatalf("next=%s want=%s", res.Next, want)
			}
			for _, id := range []string{"named", "other", "old", "tester"} {
				var status, acceptance string
				if err := owner.QueryRow(ctx, `SELECT status, acceptance FROM review_findings WHERE id = $1`, id).Scan(&status, &acceptance); err != nil {
					t.Fatal(err)
				}
				wantStatus, wantAcceptance := "open", "before"
				if id == "named" && action == "accept" {
					wantStatus = "accepted"
				}
				if id == "named" && action == "amend" {
					wantAcceptance = note
				}
				if status != wantStatus || acceptance != wantAcceptance {
					t.Fatalf("%s: %s %q", id, status, acceptance)
				}
			}
			var criteria string
			if err := owner.QueryRow(ctx, `SELECT acceptance_criteria::text FROM tasks WHERE id = 'wi_e'`).Scan(&criteria); err != nil || criteria != `["original"]` {
				t.Fatalf("task criteria changed: %s %v", criteria, err)
			}
			if action == "accept" {
				nextRaw, _ := json.Marshal(after)
				next, err := w.reviewExit(ctx, workflow.StepContext{OrganizationID: org, Step: "reviewExit", State: nextRaw})
				if err != nil || next.Next != "decide" {
					t.Fatalf("unrelated blocker bypassed bounds: %s %v", next.Next, err)
				}
			}
		})
	}
}

func TestStuckRetryNeedsANoteAtTheDecision(t *testing.T) {
	app, owner := dbtest.Open(t)
	org := dbtest.Org(t, owner)
	ctx := context.Background()
	for _, query := range []string{
		`INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES ('prj_e', $1, 'P', 'p', 'P')`,
		`INSERT INTO tasks (id, organization_id, project_id, number, title, goal, status) VALUES ('wi_e', $1, 'prj_e', 1, 'Greet', 'greet', 'awaiting_input')`,
	} {
		if _, err := owner.Exec(ctx, query, org); err != nil {
			t.Fatal(err)
		}
	}
	st := &State{TaskID: "wi_e", ProjectID: "prj_e", Attempt: 1, Policy: DefaultPolicy(),
		Escalation: &Escalation{Reason: "stuck", Step: "fix", Detail: LoopExit{Reason: "stuck", FindingIDs: []string{"f"}}}}
	raw, _ := json.Marshal(st)
	if _, err := owner.Exec(ctx, `INSERT INTO workflow_runs (id, organization_id, workflow_type, idempotency_key, task_id, status, step, state) VALUES ('wf_e', $1, $2, 'e', 'wi_e', 'waiting', 'decide', $3)`, org, WorkflowType, raw); err != nil {
		t.Fatal(err)
	}
	decide := func(action, note string) error {
		return app.InOrg(ctx, org, func(tx pgx.Tx) error {
			return DecideEscalationTx(ctx, tx, org, "wi_e", EscalationDecision{Action: action, Note: note, ActorType: "system", ActorID: "test"})
		})
	}
	decided := func() bool {
		var n int
		if err := owner.QueryRow(ctx, `SELECT count(*) FROM workflow_runs WHERE id = 'wf_e' AND state->'escalation' ? 'decided'`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n == 1
	}
	for _, note := range []string{"", " \n\t"} {
		var e EscalationError
		if err := decide("retry", note); !errors.As(err, &e) || e.Kind != "bad_request" {
			t.Fatalf("stuck retry with note %q: %v, want bad_request", note, err)
		}
		if decided() {
			t.Fatalf("refused stuck retry was recorded")
		}
	}
	if err := decide("retry", "Split the parser change from the fix."); err != nil {
		t.Fatalf("stuck retry with a note: %v", err)
	}
	if !decided() {
		t.Fatal("stuck retry not recorded")
	}
	var n int
	if err := owner.QueryRow(ctx, `SELECT count(*) FROM questions WHERE task_id = 'wi_e' AND status = 'answered'
		AND answer = 'Split the parser change from the fix.'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("stuck retry direction recorded as %d task decisions: %v", n, err)
	}
}

func TestRequiredNotesDoNotGrantAdminAuthorization(t *testing.T) {
	for _, action := range []string{"retry", "accept", "amend"} {
		if !NeedsNote(ReasonNotConverging, action) || NeedsReason(ReasonNotConverging, action) {
			t.Fatalf("incorrect convergence note/authorization for %s", action)
		}
	}
	if !NeedsNote("stuck", "retry") || NeedsReason("stuck", "retry") || NeedsNote(ReasonNotConverging, "stop") {
		t.Fatal("incorrect stuck retry or stop note rules")
	}
	if !NeedsReason(ReasonTestFailedCriteria, "accept") || !NeedsReason(ReasonTestBlocked, ActionWaive) {
		t.Fatal("test-waiver authorization changed")
	}
}

func TestEscalationStatusAndQuestionShareTransaction(t *testing.T) {
	app, owner := dbtest.Open(t)
	org := dbtest.Org(t, owner)
	ctx := context.Background()
	for _, sql := range []string{
		`INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES ('prj_e', $1, 'P', 'p', 'P')`,
		`INSERT INTO tasks (id, organization_id, project_id, number, title, goal, status)
			VALUES ('wi_e', $1, 'prj_e', 1, 'Greet', 'greet', 'running')`,
	} {
		if _, err := owner.Exec(ctx, sql, org); err != nil {
			t.Fatal(err)
		}
	}
	w := &steps{s: &Store{DB: app}}
	st := &State{TaskID: "wi_e", ProjectID: "prj_e"}
	sc := workflow.StepContext{WorkflowRunID: "wf_e", OrganizationID: org, Step: "pr_wait"}
	if _, err := w.escalate(ctx, sc, st, "pull_request_conflict", map[string]any{"repo": "target", "number": 1}); err != nil {
		t.Fatal(err)
	}
	var status, task, asked string
	// xmin identifies the writing transaction; this checks atomicity, not race timing.
	if err := app.InOrg(ctx, org, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT status::text, xmin::text FROM tasks WHERE id = $1`, st.TaskID).Scan(&status, &task); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `SELECT xmin::text FROM events WHERE task_id = $1 AND event_type = $2`, st.TaskID, EvQuestionAsked).Scan(&asked)
	}); err != nil {
		t.Fatal(err)
	}
	if status != "awaiting_input" {
		t.Errorf("task status %s, want awaiting_input", status)
	}
	if task != asked {
		t.Errorf("awaiting_input committed in transaction %s, its question.asked in %s", task, asked)
	}
}

func TestAwaitPRFixReplayChargesOnce(t *testing.T) {
	app, owner := dbtest.Open(t)
	org := dbtest.Org(t, owner)
	ctx := context.Background()
	for _, query := range []string{
		`INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES ('prj_e', $1, 'P', 'p', 'P')`,
		`INSERT INTO tasks (id, organization_id, project_id, number, title, goal) VALUES ('wi_e', $1, 'prj_e', 1, 'Greet', 'greet')`,
		`INSERT INTO repositories (id, organization_id, project_id, name, url) VALUES ('repo_e', $1, 'prj_e', 'target', 'https://github.com/acme/target')`,
		`INSERT INTO pull_requests (id, organization_id, project_id, task_id, repository_id, number, url, head_branch, base_branch, head_sha, title) VALUES ('pr_e', $1, 'prj_e', 'wi_e', 'repo_e', 1, 'https://github.com/acme/target/pull/1', 'work', 'main', 'abc', 'Greet')`,
		`INSERT INTO runs (id, organization_id, project_id, task_id, attempt, phase, status, heads) VALUES ('run_e', $1, 'prj_e', 'wi_e', 1, 'fix', 'completed', '{"target":{"sha":"def","base":"abc"}}')`,
	} {
		if _, err := owner.Exec(ctx, query, org); err != nil {
			t.Fatal(err)
		}
	}
	w := &steps{s: &Store{DB: app}}
	st := &State{TaskID: "wi_e", ProjectID: "prj_e", PullRequestIDs: []string{"pr_e"}, PRFixKey: 1, PRFixCharged: &PRFixCharge{Round: 1, Pending: true}, Candidate: &Candidate{From: map[string]string{"target": "abc"}}}
	for range 2 {
		st.PendingRunIDs = []string{"run_e"}
		raw, err := json.Marshal(st)
		if err != nil {
			t.Fatal(err)
		}
		res, err := w.awaitPRFix(ctx, workflow.StepContext{OrganizationID: org, State: raw, Signals: []workflow.Signal{{Name: SignalPhaseFinished, Payload: json.RawMessage(`{"runId":"run_e"}`)}}})
		if err != nil {
			t.Fatal(err)
		}
		if res.Next != "review" {
			t.Fatalf("next = %s, want review", res.Next)
		}
		st = res.State.(*State)
	}
	if n := st.PRFixes["target"]; n != 1 {
		t.Fatalf("replayed landing charged %d, want 1", n)
	}
}

// A refused loop that opened with its simplifier retries with a new review
// budget, as one that opened with a fix does: the retry's first review round
// is not the last the policy allows.
func TestARefusedLoopsRetryGetsANewReviewBudget(t *testing.T) {
	for _, step := range []string{"fix", "simplify"} {
		st := &State{Policy: DefaultPolicy()}
		st.Iteration = st.Policy.MaxReviewIterations - 1
		retry(st, step, &Escalation{Reason: ReasonPullRequestMoved})
		// review counts its round, then weighs the loop's exit.
		if e := Exit(st.Policy, []FindingState{finding("blocking", "open", 0, "a")}, st.Iteration+1, Convergence{}); e != nil {
			t.Errorf("%s retry: its first review round exits %q at iteration %d", step, e.Reason, st.Iteration+1)
		}
	}
}

func TestLegacyMovedPRFixRefundsItsStartCharge(t *testing.T) {
	st := &State{PRFixKey: 3, PRIteration: 1, PRFixes: map[string]int{"target": 5}, PRFeedback: []forge.ActionableFeedback{{Repo: "target"}}}
	retry(st, "prFix", &Escalation{Reason: ReasonPullRequestMoved})
	if n := st.PRFixes["target"]; n != 4 {
		t.Fatalf("legacy retry left %d charged, want 4", n)
	}
}

func TestChecksFinishedRechecksTheEscalationAndCurrentChecks(t *testing.T) {
	for _, tc := range []struct {
		name, reason, checks string
		decided              bool
	}{
		{"another reason", "pull_request_conflict", "passing", false},
		{"new pending reading", "ci_stuck", "pending", false},
		{"unknown reading", "ci_stuck", "unknown", false},
		{"finished", "ci_stuck", "passing", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app, owner := dbtest.Open(t)
			org := dbtest.Org(t, owner)
			ctx := context.Background()
			for _, query := range []string{
				`INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES ('prj_e', $1, 'P', 'p', 'P')`,
				`INSERT INTO tasks (id, organization_id, project_id, number, title, goal, status) VALUES ('wi_e', $1, 'prj_e', 1, 'Greet', 'greet', 'awaiting_input')`,
				`INSERT INTO repositories (id, organization_id, project_id, name, url) VALUES ('repo_e', $1, 'prj_e', 'target', 'https://github.com/acme/target')`,
				`INSERT INTO pull_requests (id, organization_id, project_id, task_id, repository_id, number, url, head_branch, base_branch, head_sha, title) VALUES ('pr_e', $1, 'prj_e', 'wi_e', 'repo_e', 1, 'https://github.com/acme/target/pull/1', 'work', 'main', 'abc', 'Greet')`,
			} {
				if _, err := owner.Exec(ctx, query, org); err != nil {
					t.Fatal(err)
				}
			}
			st := &State{TaskID: "wi_e", ProjectID: "prj_e", Escalation: &Escalation{Reason: tc.reason, Detail: map[string]any{"repo": "target", "number": 1}}}
			raw, err := json.Marshal(st)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := owner.Exec(ctx, `INSERT INTO workflow_runs (id, organization_id, workflow_type, idempotency_key, task_id, status, step, state) VALUES ('wf_e', $1, $2, 'e', 'wi_e', 'waiting', 'decide', $3)`, org, WorkflowType, raw); err != nil {
				t.Fatal(err)
			}
			if _, err := owner.Exec(ctx, `UPDATE pull_requests SET checks = $1::check_state WHERE id = 'pr_e'`, tc.checks); err != nil {
				t.Fatal(err)
			}
			if err := app.InOrg(ctx, org, func(tx pgx.Tx) error { return ChecksFinishedTx(ctx, tx, org, "wi_e", "target", 1) }); err != nil {
				t.Fatal(err)
			}
			var decided bool
			if err := owner.QueryRow(ctx, `SELECT state->'escalation'->'decided' IS NOT NULL FROM workflow_runs WHERE id = 'wf_e'`).Scan(&decided); err != nil {
				t.Fatal(err)
			}
			if decided != tc.decided {
				t.Fatalf("decided = %v, want %v", decided, tc.decided)
			}
		})
	}
}
