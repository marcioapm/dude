package orchestrator_test

// The quiet voice (step 3b): a conductor that decides a delivery quietly
// takes no turn at its start, and its pull request opens by Deliver's own
// rules, through the orchestrator's real code: the API, the workflow, the
// syncer, and the fake lux and GitHub.

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/workflow"
)

// startQuiet starts the task's delivery decided by its conductor, quietly,
// as the world's deliver starts one Deliver decides.
func (w *world) startQuiet(task string) string {
	w.t.Helper()
	ctx := context.Background()
	var st delivery.State
	if err := w.app.InOrg(ctx, w.org, func(tx pgx.Tx) error {
		if err := delivery.NameOnlyRepository(ctx, tx, task); err != nil {
			return err
		}
		var err error
		st, err = delivery.NewState(ctx, tx, task, w.project, scriptedPolicy(), 1)
		return err
	}); err != nil {
		w.t.Fatal(err)
	}
	st.Decider, st.HadConductor, st.Voice = delivery.DeciderConductor, true, delivery.VoiceQuiet
	id, _, err := w.runtime.Start(ctx, workflow.StartOptions{
		Type: delivery.WorkflowType, OrganizationID: w.org, IdempotencyKey: "delivery:" + task, TaskID: task, Input: st,
	})
	if err != nil {
		w.t.Fatal(err)
	}
	return id
}

// quietly makes the task's live delivery quiet under its conductor, as a
// hand-back writes it, before its next step.
func (w *world) quietly(task string) {
	w.t.Helper()
	mustExec(w.t, w.owner, `UPDATE workflow_runs SET state = state || '{"decider":"conductor","hadConductor":true,"voice":"quiet"}'::jsonb
		WHERE task_id = $1`, task)
}

// startedQuietly says the task's implementer started with no conductor
// turn: no reason to wake one recorded, no conductor Run, and no planning
// with one.
func (w *world) startedQuietly(task string) {
	w.t.Helper()
	w.until("the implementer", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'implement'`, task) == 1
	})
	if n := w.count(`SELECT count(*) FROM conductor_wakes WHERE task_id = $1`, task); n != 0 {
		w.t.Errorf("%d reasons to wake the conductor recorded at a quiet start", n)
	}
	if id, _, _ := w.conductor(task); id != "" {
		w.t.Errorf("a conductor Run %s at a quiet start", id)
	}
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = $2`, task, delivery.EvDecisionAwaited); n != 0 {
		w.t.Errorf("%d decisions awaited at a quiet start", n)
	}
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'task.status_changed'
		AND payload->>'reason' = 'planning with the conductor'`, task); n != 0 {
		w.t.Errorf("a quiet start said it plans with the conductor")
	}
}

// A quiet conductor takes no turn at the start: the implementer starts at
// once, as under Deliver, with no reason to wake anyone.
func TestAQuietStartRunsTheImplementerWithNoWake(t *testing.T) {
	w := conductorWorld(t)
	task := w.task()
	w.startQuiet(task)
	w.startedQuietly(task)
}

// Work to continue, taken over quietly, starts its implementer at once
// too: the start is routed through the same point (adopt), whose wake
// would be settled, and so heard by nobody.
func TestAQuietContinueAdoptedStartRunsTheImplementerWithNoWake(t *testing.T) {
	w := conductorWorld(t)
	n := w.gh.OpenAsPerson("alice", "feature/half", "main", "Half done")
	wi := w.adopted("pr", n, "feature/half", "main", delivery.ModeContinue)
	w.startQuiet(wi)
	w.startedQuietly(wi)
}

// quietToTheOpening talks the task through to after its implementer, makes
// it quiet there, and has its conductor go on to the simplifier and past
// its review: the delivery reaches its opening with the conductor deciding
// quietly. Returns the task.
func (w *world) quietToTheOpening(task string) {
	w.t.Helper()
	w.talk(task)
	w.implemented(task)
	w.quietly(task)
	w.carriedOut(task, "start_phase", `{"phase":"simplify"}`)
	w.agentsDone(task, "the simplifier's narrow review", func() bool { return w.decisionAt(task) == delivery.PointReviewed })
	w.finishSimplifierReview(task)
}

// conductorAskedAtTheOpening counts the decisions the conductor was asked
// for before the pull request: none under a quiet one, whose opening is
// Deliver's (Deliver's own hold offers the conductor no action).
func (w *world) conductorAskedAtTheOpening(task string) int {
	return w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = $2
		AND payload->>'point' = 'before_pull_request' AND jsonb_array_length(payload->'actions') > 0`, task, delivery.EvDecisionAwaited) +
		w.count(`SELECT count(*) FROM conductor_wakes WHERE task_id = $1 AND kind = 'decision'
			AND created_at > (SELECT max(occurred_at) FROM events WHERE task_id = $1 AND event_type = 'conductor.decision_taken')`, task)
}

// A quiet conductor's delivery opens its pull request as the task's choice
// says, by Deliver's own rules, with no conductor turn before it: Open it
// opens, Open as draft opens a draft, and Ask me first holds it for the
// owner, whose Open on the gate's question opens it.
func TestAQuietDeliveryOpensItsPullRequestByTheChoice(t *testing.T) {
	for _, c := range []struct{ choice string }{
		{delivery.PullRequestOpen}, {delivery.PullRequestDraft}, {delivery.PullRequestAsk},
	} {
		t.Run(c.choice, func(t *testing.T) {
			w := conducting(t)
			ana := w.person("Ana")
			task := w.task()
			w.assignOwner(task, ana)
			w.projectChoice(c.choice)
			w.quietToTheOpening(task)
			if c.choice == delivery.PullRequestAsk {
				w.until("Deliver's hold, asking", func() bool {
					return w.heldBeforePR(task) && w.conductorQuestion(task) != "" || len(w.gh.Pulls()) > 0
				})
				if n := len(w.gh.Pulls()); n != 0 {
					t.Fatalf("%d pull requests opened under Ask me first", n)
				}
				if s := w.taskStatus(task); s != "awaiting_input" {
					t.Errorf("task %s at Deliver's hold, want awaiting_input for its owner", s)
				}
				// The question is Deliver's, not one the conductor asked first.
				if p := w.str(`SELECT prompt FROM questions WHERE id = $1`, w.conductorQuestion(task)); strings.Contains(p, "conductor") {
					t.Errorf("the quiet hold's question says the conductor asked: %q", p)
				}
				if status, out := w.answerAs(ana, task, "Open"); status != 200 && status != 201 {
					t.Fatalf("the owner's Open: %d %v", status, out)
				}
			}
			w.until("the pull request", func() bool { return len(w.gh.Pulls()) == 1 })
			if got := w.gh.Pulls()[0].Draft; got != (c.choice == delivery.PullRequestDraft) {
				t.Errorf("draft %v under %s", got, c.choice)
			}
			if n := w.conductorAskedAtTheOpening(task); n != 0 {
				t.Errorf("the quiet conductor was asked %d times before the pull request", n)
			}
			if d := w.decider(task); d != delivery.DeciderConductor {
				t.Errorf("decider %s", d)
			}
		})
	}
}

// At Deliver's hold of a quiet delivery the opening is the person's: the
// conductor's decide and start_phase are refused there, and nothing moves.
func TestTheQuietConductorIsRefusedAtDeliversHold(t *testing.T) {
	w := conducting(t)
	ana := w.person("Ana")
	task := w.task()
	w.assignOwner(task, ana)
	w.projectChoice(delivery.PullRequestAsk)
	w.quietToTheOpening(task)
	w.until("Deliver's hold", func() bool { return w.heldBeforePR(task) || len(w.gh.Pulls()) > 0 })
	for _, call := range []struct{ tool, args string }{
		{"decide", `{"action":"ask_person"}`},
		{"start_phase", `{"phase":"review","categories":["correctness"]}`},
	} {
		if status, out := w.as(task, call.tool, call.args); status != 422 {
			t.Errorf("%s at Deliver's hold: %d %v, want refused", call.tool, status, out)
		}
	}
	for range 3 {
		w.pump()
	}
	if !w.heldBeforePR(task) || len(w.gh.Pulls()) != 0 || w.phaseRuns(task, "review") != w.count(`SELECT count(*) FROM runs
		WHERE task_id = $1 AND phase = 'review' AND status IN ('completed', 'failed')`, task) {
		t.Fatalf("the refused calls moved the hold\n%s", w.describeRuns())
	}
}
