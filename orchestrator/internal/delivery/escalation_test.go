package delivery

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/dbtest"
	"github.com/marciomartins/dude/orchestrator/internal/forge"
	"github.com/marciomartins/dude/orchestrator/internal/workflow"
)

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
		if e := Exit(st.Policy, []FindingState{finding("blocking", "open", 0, "a")}, st.Iteration+1); e != nil {
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
