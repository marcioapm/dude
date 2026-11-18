package delivery

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/dbtest"
	"github.com/marciomartins/dude/orchestrator/internal/workflow"
)

// An integration that failed escalates as what happened: its Run's failure
// as integration_failed, with the Run's error in the conductor's wake and
// retry offered; a merge that did not resolve the conflict, or none
// published, or a pair already tried, as pull_request_conflict, worded as
// a conflict.
func TestAnIntegrationFailureEscalatesAsItsCause(t *testing.T) {
	const runErr = "provider 429: rate limited, retried then failed"
	for _, c := range []struct {
		cause, problem, reason string
		want                   []string
	}{
		{causeRunFailed, runErr, "integration_failed",
			[]string{"Escalation: integration failed. Actions: retry, wait, stop.", "The base merge Run run_int failed: " + runErr + ".", "Retry the integration"}},
		{causeStillConflicting, reasonStillConflicting, "pull_request_conflict", []string{"Escalation: pull request conflict. Actions: retry, wait, stop."}},
		{causeNoMerge, reasonNoMerge, "pull_request_conflict", []string{"Escalation: pull request conflict. Actions: retry, wait, stop."}},
		{causePairAttempted, reasonPairAttempted, "pull_request_conflict", []string{"Escalation: pull request conflict. Actions: retry, wait, stop."}},
	} {
		t.Run(c.cause, func(t *testing.T) {
			app, owner := dbtest.Open(t)
			org := dbtest.Org(t, owner)
			ctx := context.Background()
			for _, q := range []string{
				`INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES ('prj_i', $1, 'P', 'p', 'P')`,
				`INSERT INTO tasks (id, organization_id, project_id, number, title, goal, status) VALUES ('wi_i', $1, 'prj_i', 1, 'Greet', 'greet', 'running')`,
				`INSERT INTO runs (id, organization_id, project_id, task_id, attempt, status, role, kind, phase, keep, kept_until, lux_run_id, lux_stop_reason)
					VALUES ('run_int', $1, 'prj_i', 'wi_i', 1, 'failed', 'implementer', 'agent', 'fix', true, now() + interval '1 day', 'lux_int', 'kept')`,
			} {
				if _, err := owner.Exec(ctx, q, org); err != nil {
					t.Fatal(err)
				}
			}
			w := &steps{s: &Store{DB: app}}
			st := &State{TaskID: "wi_i", ProjectID: "prj_i",
				Integration: &Integration{Repo: "web", Number: 4, Base: "main", Head: "aaa", BaseHead: "bbb", RunID: "run_int"}}
			sc := workflow.StepContext{WorkflowRunID: "wf_i", OrganizationID: org, Step: "awaitPRFix"}
			if _, err := w.integrationFailure(ctx, sc, st, c.cause, c.problem); err != nil {
				t.Fatal(err)
			}
			e := st.Escalation
			if e.Reason != c.reason {
				t.Fatalf("reason %s, want %s", e.Reason, c.reason)
			}
			if e.At != "" {
				t.Fatalf("resume at %q: a kept integration Run must be retried from fresh heads, not resumed", e.At)
			}
			if e.Step != "prFix" || !strings.Contains(strings.Join(e.Actions(), " "), "retry") {
				t.Fatalf("retry to %q, actions %v: the integration cannot be retried", e.Step, e.Actions())
			}
			// What the web reads: the persisted escalation's reason, its cause and error.
			var reason, cause, problem string
			if err := app.InOrg(ctx, org, func(tx pgx.Tx) error {
				return tx.QueryRow(ctx, `SELECT payload->>'reason', payload->'detail'->>'cause', payload->'detail'->>'error' FROM events
					WHERE task_id = 'wi_i' AND event_type = $1`, EvQuestionAsked).Scan(&reason, &cause, &problem)
			}); err != nil {
				t.Fatal(err)
			}
			if reason != c.reason || cause != c.cause || problem != c.problem {
				t.Fatalf("the web reads %s/%s/%q, want %s/%s/%q", reason, cause, problem, c.reason, c.cause, c.problem)
			}
			line := escalatedLine(e)
			for _, want := range c.want {
				if !strings.Contains(line, want) {
					t.Errorf("wake %q\nmissing %q", line, want)
				}
			}
			if failed := strings.Contains(line, "base merge Run"); failed != (c.cause == causeRunFailed) {
				t.Errorf("wake %q names a failed Run: %v", line, failed)
			}
			if conflict := strings.Contains(line, "conflict."); conflict == (c.cause == causeRunFailed) {
				t.Errorf("wake %q names a conflict: %v", line, conflict)
			}
		})
	}
}
