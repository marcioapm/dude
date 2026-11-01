package orchestrator_test

// The conductor's fix-then-review (design step 3, §2): start_phase's then
// queues a review behind a fix or a simplifier, which dude starts as soon
// as it has committed, without a decision between.

import (
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
)

// untaken says the task's delivery is parked on a decision not yet taken.
func (w *world) untaken(task string) bool {
	return w.count(`SELECT count(*) FROM workflow_runs WHERE task_id = $1 AND step = 'conductorDecision'
		AND state->'decision' ? 'point' AND NOT state->'decision' ? 'taken'`, task) == 1
}

// start_phase's then is for a fix or a simplifier, naming known reviewers;
// round is for a review, a fix or a simplifier. Each refused call takes no
// decision; the decision is still the conductor's after it, and a valid
// then is taken, its then and round on conductor.decided.
func TestThenIsOnlyForFixOrSimplify(t *testing.T) {
	w := roundsWorld(t)
	task := w.task()
	w.talk(task)
	refused := func(args string) {
		t.Helper()
		if status, out := w.as(task, "start_phase", args); status != 422 {
			t.Errorf("start_phase %s: %d %v, want refused", args, status, out)
		}
		if !w.untaken(task) {
			t.Fatalf("start_phase %s took the decision", args)
		}
	}
	refused(`{"phase":"implement","then":{"review":["tests"]}}`)
	refused(`{"phase":"implement","round":{"scope":"whole"}}`)
	w.implemented(task)
	w.must(task, "decide", `{"action":"next"}`)
	w.until("round 1", func() bool { return w.decisionAt(task) == delivery.PointReviewed })
	refused(`{"phase":"review","then":{"review":["tests"]}}`)
	refused(`{"phase":"test","then":{"review":["tests"]}}`)
	refused(`{"phase":"fix","then":{"review":["tests","spelling"]}}`)
	refused(`{"phase":"fix","then":{"review":[]}}`)
	refused(`{"phase":"fix","then":{"review":["tests"],"wake":"later"}}`)
	refused(`{"phase":"fix","round":{"scope":"part"}}`)
	w.must(task, "start_phase", `{"phase":"fix","then":{"review":["tests"],"wake":"after_fix","note":"N"},"round":{"bar":"B"}}`)
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = $2 AND payload->>'phase' = 'fix'
		AND payload->'then' = '{"review":["tests"],"wake":"after_fix","note":"N"}'::jsonb AND payload->'round' = '{"bar":"B"}'::jsonb`,
		task, delivery.EvDecisionTaken); n != 1 {
		t.Errorf("conductor.decided with then and round: %d, want 1", n)
	}
}

// then.wake is after_review when not said.
func TestThenWakesAfterTheReviewByDefault(t *testing.T) {
	w := roundsWorld(t)
	task := w.task()
	w.toRoundOne(task)
	w.must(task, "start_phase", `{"phase":"fix","then":{"review":["tests"]}}`)
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = $2
		AND payload->'then'->>'wake' = $3 AND NOT payload ? 'round'`, task, delivery.EvDecisionTaken, delivery.WakeAfterReview); n != 1 {
		t.Errorf("conductor.decided with then.wake after_review: %d, want 1", n)
	}
}
