package delivery

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/workflow"
)

// prLoopExhausted is a conducted delivery waiting at decide on a
// pr_loop_exhausted escalation, held by holder (the task's status as that
// holder leaves it), its round's fixes and api's pull request budget spent.
func prLoopExhausted(t *testing.T, holder string) goldenWorld {
	t.Helper()
	w := newGoldenWorld(t)
	w.exec(t, `INSERT INTO runs (id, organization_id, project_id, task_id, attempt, status, role, kind)
		VALUES ('run_cond1', $1, 'prj_g', 'wi_g', 1, 'running', 'conductor', 'agent')`)
	if holder == HolderPerson {
		w.exec(t, `UPDATE tasks SET status = 'awaiting_input' WHERE id = 'wi_g'`)
	}
	w.delivery(t, "decide", fmt.Sprintf(`{"taskId":"wi_g","projectId":"prj_g","attempt":1,"decider":"conductor",
		"prIteration":3,"prFixKey":3,"prFixesByRepo":{"api":5},"policy":{"maxPrFixIterations":3},
		"escalation":{"reason":"pr_loop_exhausted","holder":%q,"step":"prFix",
			"detail":{"iterations":3,"perRound":3,"total":5,"spent":["api"]}}}`, holder))
	return w
}

var conductorRef = RunRef{ProjectID: "prj_g", TaskID: "wi_g", RunID: "run_cond1"}

// conductorDecides is decide_escalation by the task's conductor.
func (w goldenWorld) conductorDecides(t *testing.T, action, note string) error {
	t.Helper()
	ref := conductorRef
	ref.Org = w.org
	return w.app.InOrg(context.Background(), w.org, func(tx pgx.Tx) error {
		_, err := ConductDecideEscalation(context.Background(), tx, ref, action, note)
		return err
	})
}

// decided is the task's latest task.decided: its payload and actor.
func (w goldenWorld) decided(t *testing.T) (payload map[string]any, actorType, actorID string) {
	t.Helper()
	var raw []byte
	err := w.owner.QueryRow(context.Background(), `SELECT payload, actor_type, actor_id FROM events
		WHERE task_id = 'wi_g' AND event_type = $1 ORDER BY cursor DESC LIMIT 1`, EvTaskDecided).Scan(&raw, &actorType, &actorID)
	if err == pgx.ErrNoRows {
		return nil, "", ""
	}
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	return payload, actorType, actorID
}

func (w goldenWorld) count(t *testing.T, sql string) int {
	t.Helper()
	var n int
	if err := w.owner.QueryRow(context.Background(), sql).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// carriedOut runs the decide step on the decision taken, and returns where
// it goes and the state it leaves.
func (w goldenWorld) carriedOut(t *testing.T) (string, State) {
	t.Helper()
	ctx := context.Background()
	var raw []byte
	if err := w.owner.QueryRow(ctx, `SELECT state FROM workflow_runs WHERE id = 'wfr_g'`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	s := &steps{s: &Store{DB: w.app}}
	res, err := s.decide(ctx, workflow.StepContext{OrganizationID: w.org, WorkflowRunID: "wfr_g", Step: "decide", State: raw})
	if err != nil {
		t.Fatal(err)
	}
	st, ok := res.State.(*State)
	if !ok {
		t.Fatalf("decide left %T", res.State)
	}
	return res.Next, *st
}

// wantPRFixesReset: a retry of pr_loop_exhausted goes back to prFix with
// another set of fix rounds, the spent pull request's budget anew.
func wantPRFixesReset(t *testing.T, next string, st State) {
	t.Helper()
	if next != "prFix" || st.PRIteration != 0 || st.PRFixes["api"] != 0 || st.Escalation != nil {
		t.Fatalf("retry went to %q with prIteration %d, api's fixes %d, escalation %+v; want prFix with both reset",
			next, st.PRIteration, st.PRFixes["api"], st.Escalation)
	}
}

// The conductor decides another set of pull request fix rounds itself,
// with its reason: recorded on task.decided as the conductor's, and carried
// out as a person's retry is.
func TestTheConductorRetriesAnExhaustedPRLoopWithItsReason(t *testing.T) {
	w := prLoopExhausted(t, HolderConductor)
	if err := w.conductorDecides(t, "retry", "  "); err == nil || !strings.Contains(err.Error(), "say why another") {
		t.Fatalf("a retry with no reason: %v, want refused asking why", err)
	}
	if p, _, _ := w.decided(t); p != nil {
		t.Fatalf("a refused retry recorded %v", p)
	}
	const why = "The reviewer's last two comments are new: the rename is done, only the changelog is left."
	if err := w.conductorDecides(t, "retry", why); err != nil {
		t.Fatalf("the conductor's retry: %v", err)
	}
	p, actorType, actorID := w.decided(t)
	if p["reason"] != "pr_loop_exhausted" || p["action"] != "retry" || p["by"] != "conductor" || p["note"] != why ||
		actorType != "agent" || actorID != "run_cond1" {
		t.Fatalf("task.decided %v by %s %s, want the conductor's retry with its reason", p, actorType, actorID)
	}
	if n := w.count(t, `SELECT count(*) FROM questions WHERE task_id = 'wi_g'`); n != 0 {
		t.Fatalf("%d questions: the conductor's own retry asks nobody", n)
	}
	next, st := w.carriedOut(t)
	wantPRFixesReset(t, next, st)
}

// A second exhaustion in the same delivery: a retry whose reason is an
// earlier retry's again is refused, for the conductor to stop or ask the
// person; one that says what differs is taken. A retry of an earlier
// delivery is not this one's.
func TestASecondPRLoopRetryWithAnUnchangedReasonIsRefused(t *testing.T) {
	const first = "The reviewer's last two comments are new: the rename is done, only the changelog is left."
	for _, by := range []string{"conductor", "person"} {
		t.Run("after the "+by+"'s retry", func(t *testing.T) {
			w := prLoopExhausted(t, HolderConductor)
			payload := `jsonb_build_object('reason','pr_loop_exhausted','action','retry','note',$2::text)`
			if by == "conductor" {
				payload = `jsonb_build_object('reason','pr_loop_exhausted','action','retry','note',$2::text,'by','conductor')`
			}
			// An earlier delivery's retry, with another reason, is not counted.
			w.exec(t, `INSERT INTO events (id, organization_id, event_type, task_id, actor_type, actor_id, source, payload, occurred_at)
				VALUES ('ev_old', $1, 'task.decided', 'wi_g', 'agent', 'run_old', 'test',
				jsonb_build_object('reason','pr_loop_exhausted','action','retry','note','Split the changelog out.'), now() - interval '1 day')`)
			w.exec(t, `INSERT INTO events (id, organization_id, event_type, task_id, actor_type, actor_id, source, payload)
				VALUES ('ev_retry1', $1, 'task.decided', 'wi_g', 'agent', 'run_cond1', 'test', `+payload+`)`, first)
			for _, same := range []string{first, "  the reviewer's LAST two comments are new -- the rename is done; only the changelog is left  "} {
				err := w.conductorDecides(t, "retry", same)
				if err == nil || !strings.Contains(err.Error(), "stop, or ask the person") {
					t.Fatalf("a retry with an earlier retry's reason: %v, want refused", err)
				}
			}
			if p, _, _ := w.decided(t); p["note"] != first {
				t.Fatalf("a refused retry recorded %v", p)
			}
			const changed = "Split the changelog out."
			if err := w.conductorDecides(t, "retry", changed); err != nil {
				t.Fatalf("a retry saying what differs: %v", err)
			}
			next, st := w.carriedOut(t)
			wantPRFixesReset(t, next, st)
		})
	}
}

// A person's retry of pr_loop_exhausted is as it was: no reason needed, as
// often as they like, and it resets the pull request fix rounds.
func TestAPersonsRetryOfAnExhaustedPRLoopStillWorks(t *testing.T) {
	w := prLoopExhausted(t, HolderPerson)
	w.exec(t, `INSERT INTO events (id, organization_id, event_type, task_id, actor_type, actor_id, source, payload)
		VALUES ('ev_retry1', $1, 'task.decided', 'wi_g', 'human', 'per_ana', 'test',
		jsonb_build_object('reason','pr_loop_exhausted','action','retry','note',''))`)
	if err := w.app.InOrg(context.Background(), w.org, func(tx pgx.Tx) error {
		return DecideEscalationTx(context.Background(), tx, w.org, "wi_g", EscalationDecision{Action: "retry",
			ActorType: "human", ActorID: "per_ana", Person: "per_ana"})
	}); err != nil {
		t.Fatalf("a person's retry: %v", err)
	}
	p, actorType, _ := w.decided(t)
	if p["action"] != "retry" || p["by"] != nil || actorType != "human" {
		t.Fatalf("task.decided %v by %s, want the person's retry", p, actorType)
	}
	next, st := w.carriedOut(t)
	wantPRFixesReset(t, next, st)
}

// What stays the person's: shipping past failed criteria, waiving checks,
// accepting on review_failed, stuck or exhausted. The conductor's own
// decision of any is refused, and records nothing.
func TestShippingPastAFailureStaysPersonOnly(t *testing.T) {
	for _, c := range []struct{ reason, action string }{
		{ReasonTestFailedCriteria, "accept"},
		{ReasonTestBlocked, ActionWaive},
		{ReasonChecksUnverified, ActionWaive},
		{"review_failed", "accept"},
		{"stuck", "accept"},
		{"exhausted", "accept"},
	} {
		t.Run(c.reason+"/"+c.action, func(t *testing.T) {
			if !PersonOnly(c.reason, c.action) {
				t.Fatalf("PersonOnly(%s, %s) = false", c.reason, c.action)
			}
			w := newGoldenWorld(t)
			w.exec(t, `INSERT INTO runs (id, organization_id, project_id, task_id, attempt, status, role, kind)
				VALUES ('run_cond1', $1, 'prj_g', 'wi_g', 1, 'running', 'conductor', 'agent')`)
			w.delivery(t, "decide", fmt.Sprintf(`{"taskId":"wi_g","projectId":"prj_g","attempt":1,"decider":"conductor",
				"escalation":{"reason":%q,"holder":"conductor","step":"fix"}}`, c.reason))
			err := w.conductorDecides(t, c.action, "It is fine to ship as it is.")
			if err == nil || !strings.Contains(err.Error(), "only a person decides it") {
				t.Fatalf("the conductor's %s on %s: %v, want refused", c.action, c.reason, err)
			}
			if p, _, _ := w.decided(t); p != nil {
				t.Fatalf("a refused %s recorded %v", c.action, p)
			}
		})
	}
}
