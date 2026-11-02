package orchestrator_test

// An escalation Deliver stopped at, handed to the conductor while it waits:
// the conductor is told, as it is of one it had when it escalated.

import (
	"sync"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/forge"
)

// stuckDelivered is a delivery Deliver decides whose review is stuck on its
// finding (one fix attempt per finding): escalated stuck, waiting on a
// person, no conductor yet.
func stuckDelivered(w *world) string {
	w.t.Helper()
	stillAfterFirstReview(w, "")
	task := w.task()
	deliveredStuck(w, task)
	return task
}

// deliveredStuck has Deliver decide the task, at one fix attempt per
// finding, until its review escalates stuck.
func deliveredStuck(w *world, task string) {
	w.t.Helper()
	w.deliver(task)
	setPolicy(w, task, `{"maxAttemptsPerFinding":1,"maxReviewIterations":9}`)
	w.until("stuck", func() bool { return w.escalationReason(task) == "stuck" })
}

// A delivery handed to the conductor while it waits at Deliver's escalation
// tells the conductor of it, once: a person's first Chat message hands it
// over and the conductor is woken with the escalation; another message, or
// the decisions handed back and given to it again, tell it nothing more.
func TestAnEscalationHandedToTheConductorTellsIt(t *testing.T) {
	w := conducting(t)
	task := stuckDelivered(w)
	if n := w.count(`SELECT count(*) FROM conductor_wakes WHERE task_id = $1`, task); n != 0 {
		t.Fatalf("%d wakes before the conductor had the delivery", n)
	}
	if status, out := w.chat(task, "What happened?"); status >= 300 {
		t.Fatalf("chat: %d %v", status, out)
	}
	if d := w.decider(task); d != delivery.DeciderConductor {
		t.Fatalf("decider %s after the Chat message", d)
	}
	w.wokenWith(task, "Escalated to a person: stuck")
	escalations := func() int {
		return w.count(`SELECT count(*) FROM conductor_wakes WHERE task_id = $1 AND kind = 'escalation'`, task)
	}
	if n := escalations(); n != 1 {
		t.Fatalf("%d escalation wakes, want 1", n)
	}
	// Handed over again: a second message, then back to Deliver and to the
	// conductor again (the explicit switch), the same escalation waiting.
	if status, out := w.chat(task, "And now?"); status >= 300 {
		t.Fatalf("chat: %d %v", status, out)
	}
	for _, to := range []string{delivery.DeciderPolicy, delivery.DeciderConductor} {
		if status, out := w.handBack(task, map[string]any{"decider": to}); status != 200 {
			t.Fatalf("decider %s: %d %v", to, status, out)
		}
	}
	w.pump()
	if w.escalationReason(task) != "stuck" {
		t.Fatalf("the escalation no longer waits: %q", w.escalationReason(task))
	}
	if n := escalations(); n != 1 {
		t.Fatalf("%d escalation wakes after handing it over again, want 1", n)
	}
}

// A delivery handed to the conductor while Deliver's step escalates: the
// person's first Chat message commits the conductor after the step read
// the delivery as Deliver's, before its escalation is committed. The
// conductor is told of the escalation, once, as of one handed over while
// it waits.
func TestAnEscalationHandedOverWhileItsStepRunsTellsTheConductor(t *testing.T) {
	w := conducting(t)
	stillAfterFirstReview(w, "")
	task := w.task()
	var once sync.Once
	def := delivery.Workflow(&delivery.Store{DB: w.app}, forge.Resolver{DB: w.app})
	def.BeforeCommit = func(_, step, next string) {
		if step == "awaitReview" && next == "decide" {
			once.Do(func() {
				if status, out := w.chat(task, "What happened?"); status != 201 || out["decider"] != "conductor" {
					t.Errorf("chat: %d %v", status, out)
				}
			})
		}
	}
	w.runtime.Register(def)
	deliveredStuck(w, task)
	if d := w.decider(task); d != delivery.DeciderConductor {
		t.Fatalf("decider %s after the Chat message", d)
	}
	w.wokenWith(task, "Escalated to a person: stuck")
	for range 3 {
		w.pump()
	}
	if n := w.count(`SELECT count(*) FROM conductor_wakes WHERE task_id = $1 AND kind = 'escalation'`, task); n != 1 {
		t.Fatalf("%d escalation wakes, want 1", n)
	}
	if n := w.count(`SELECT count(*) FROM workflow_runs WHERE task_id = $1
		AND (state->'escalation'->>'told')::int = 1 AND (state->>'escalations')::int = 1`, task); n != 1 {
		t.Fatalf("the escalation is not told once: %s", w.describeRuns())
	}
}
