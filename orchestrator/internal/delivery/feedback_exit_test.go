package delivery

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/dbtest"
	"github.com/marciomartins/dude/orchestrator/internal/workflow"
)

// A review loop out of rounds with only a person's item blocking names it:
// accepting the escalation resolves it, and the loop and the pull request
// gate then find nothing to send back to a fixer.
func TestAcceptingAnExhaustedLoopResolvesThePeoplesFeedback(t *testing.T) {
	app, owner := dbtest.Open(t)
	org := dbtest.Org(t, owner)
	ctx := context.Background()
	for _, sql := range []string{
		`INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES ('prj_x', $1, 'P', 'p', 'P')`,
		`INSERT INTO tasks (id, organization_id, project_id, number, title, goal, status)
			VALUES ('wi_x', $1, 'prj_x', 1, 'Greet', 'greet', 'running')`,
		`INSERT INTO people (id, organization_id, name) VALUES ('per_ana_'||$1, $1, 'Ana')`,
		`INSERT INTO feedback_batches (id, organization_id, task_id, attempt, author_person_id, submission_key)
			VALUES ('fbt_x', $1, 'wi_x', 1, 'per_ana_'||$1, 'k')`,
		`INSERT INTO feedback_items (id, organization_id, task_id, attempt, batch_id, author_person_id, body, anchor_kind, anchor,
				status, submitted_at, fix_attempts)
			VALUES ('fbi_x', $1, 'wi_x', 1, 'fbt_x', 'per_ana_'||$1, 'Rename it', 'general', '{"kind":"general"}', 'open', now(), 1)`,
	} {
		if _, err := owner.Exec(ctx, sql, org); err != nil {
			t.Fatal(err)
		}
	}
	w := &steps{s: &Store{DB: app}}
	policy := DefaultPolicy()
	policy.MaxReviewIterations, policy.MaxAttemptsPerFinding, policy.Simplify = 3, 2, false
	st := &State{TaskID: "wi_x", ProjectID: "prj_x", Attempt: 1, Policy: policy, Iteration: 3}
	findings, err := w.loopFindings(ctx, org, st)
	if err != nil {
		t.Fatal(err)
	}
	exit := w.loopExit(st, findings)
	if exit == nil || exit.Reason != "exhausted" {
		t.Fatalf("the loop's exit: %+v", exit)
	}
	sc := workflow.StepContext{WorkflowRunID: "wf_x", OrganizationID: org, Step: "awaitReview"}
	res, err := w.afterLoop(ctx, sc, st, exit)
	if err != nil || res.Next != "decide" {
		t.Fatalf("escalated to %q: %v", res.Next, err)
	}
	st.Escalation.Decided = &HumanDecision{Action: "accept", Person: "per_ana_" + org}
	raw, _ := json.Marshal(st)
	sc.Step, sc.State = "decide", raw
	res, err = w.decide(ctx, sc)
	if err != nil || res.Next != "test" {
		t.Fatalf("accepted, went to %q: %v", res.Next, err)
	}
	var status string
	var by *string
	if err := owner.QueryRow(ctx, `SELECT status, resolved_by_person_id FROM feedback_items WHERE id = 'fbi_x'`).Scan(&status, &by); err != nil {
		t.Fatal(err)
	}
	if status != "resolved" || by == nil || *by != "per_ana_"+org {
		t.Fatalf("after accepting: %s by %v", status, by)
	}
	after := res.State.(*State)
	if findings, err = w.loopFindings(ctx, org, after); err != nil {
		t.Fatal(err)
	}
	if exit := w.loopExit(after, findings); exit == nil || exit.Reason != "clear" {
		t.Errorf("the loop after the acceptance: %+v", exit)
	}
	if open, err := w.s.OpenFeedbackIDs(ctx, org, after); err != nil || len(open) != 0 {
		t.Errorf("open for the pull request gate: %v %v", open, err)
	}
}

// A person's item open as the step that opens the pull request runs (one
// submitted after the review loop's exit): a fixer first, no pull request.
func TestNoPullRequestOpensPastAnOpenItem(t *testing.T) {
	app, owner := dbtest.Open(t)
	org := dbtest.Org(t, owner)
	ctx := context.Background()
	for _, sql := range []string{
		`INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES ('prj_x', $1, 'P', 'p', 'P')`,
		`INSERT INTO tasks (id, organization_id, project_id, number, title, goal, status)
			VALUES ('wi_x', $1, 'prj_x', 1, 'Greet', 'greet', 'running')`,
		`INSERT INTO people (id, organization_id, name) VALUES ('per_ana_'||$1, $1, 'Ana')`,
		`INSERT INTO feedback_batches (id, organization_id, task_id, attempt, author_person_id, submission_key)
			VALUES ('fbt_x', $1, 'wi_x', 1, 'per_ana_'||$1, 'k')`,
		`INSERT INTO feedback_items (id, organization_id, task_id, attempt, batch_id, author_person_id, body, anchor_kind, anchor,
				status, submitted_at)
			VALUES ('fbi_late', $1, 'wi_x', 1, 'fbt_x', 'per_ana_'||$1, 'Late', 'general', '{"kind":"general"}', 'open', now())`,
	} {
		if _, err := owner.Exec(ctx, sql, org); err != nil {
			t.Fatal(err)
		}
	}
	w := &steps{s: &Store{DB: app}}
	st := &State{TaskID: "wi_x", ProjectID: "prj_x", Attempt: 1, Policy: DefaultPolicy(), Heads: map[string]string{"target": "abc"}}
	raw, _ := json.Marshal(st)
	res, err := w.openPullRequest(ctx, workflow.StepContext{WorkflowRunID: "wf_x", OrganizationID: org, Step: "openPullRequest", State: raw})
	if err != nil || res.Next != "fix" {
		t.Fatalf("openPullRequest with an item open went to %q: %v", res.Next, err)
	}
	var prs int
	if err := owner.QueryRow(ctx, `SELECT count(*) FROM pull_requests WHERE task_id = 'wi_x'`).Scan(&prs); err != nil || prs != 0 {
		t.Errorf("%d pull requests opened: %v", prs, err)
	}
}
