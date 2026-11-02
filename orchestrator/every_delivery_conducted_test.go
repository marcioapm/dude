package orchestrator_test

// Every delivery has a conductor (step 3b, PR C1): Deliver's start, and
// every other start but Talk it through, starts a delivery its conductor
// decides quietly — no conductor turn at the start, its first wake after
// the implementer. Through the real API, workflow, syncer and fake lux.

import (
	"strings"
	"testing"
	"time"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
)

// deliverByAPI is Deliver pressed on a task not started: the API's start.
func (w *world) deliverByAPI(task string) {
	w.t.Helper()
	if status, body := w.call("/internal/tasks/"+task+"/deliver", map[string]any{}); status != 201 {
		w.t.Fatalf("deliver: %d %v", status, body)
	}
}

// Deliver starts a delivery its conductor decides quietly, and says so:
// the implementer starts with no conductor turn, and the conductor's first
// wake comes after it, the conductor started by it.
func TestDeliverStartsADeliveryItsConductorDecidesQuietly(t *testing.T) {
	w := conducting(t)
	task := w.task()
	w.deliverByAPI(task)
	if got := w.deciderState(task); got != "conductor/quiet/false" {
		t.Errorf("Deliver started a delivery decided %s", got)
	}
	if n := w.count(`SELECT count(*) FROM workflow_runs WHERE task_id = $1 AND (state->>'hadConductor')::boolean`, task); n != 1 {
		t.Errorf("Deliver's delivery has no conductor's fix bar (hadConductor)")
	}
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'task.decider_changed'
		AND payload->>'from' IS NULL AND payload->>'to' = 'conductor' AND payload->>'why' = 'deliver'
		AND payload->>'voice' = 'quiet'`, task); n != 1 {
		t.Errorf("Deliver's start recorded %d decider changes saying deliver, quiet", n)
	}
	w.startedQuietly(task)
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'conductor.woken'`, task); n != 0 {
		t.Errorf("the conductor was woken %d times before the implementer finished", n)
	}
	// The fake lux plays the implementer; its end is the first decision.
	w.until("the conductor's first wake, after the implementer", func() bool {
		return w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'conductor.woken'
			AND (payload->>'started')::boolean`, task) == 1
	})
	if n := w.count(`SELECT count(*) FROM conductor_wakes WHERE task_id = $1 AND line LIKE '%'||$2||'%'`, task,
		delivery.PointImplemented); n != 1 {
		t.Errorf("the conductor's first wake is not the decision after implement (%d)", n)
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'implement' AND status = 'completed'`, task); n != 1 {
		t.Errorf("the conductor was woken before the implementer completed")
	}
}

// Talk it through still starts a chatty delivery, its start recorded so.
func TestTalkItThroughStartsAChattyDelivery(t *testing.T) {
	w := conducting(t)
	task := w.task()
	w.talk(task)
	if got := w.deciderState(task); got != "conductor/chatty/false" {
		t.Errorf("Talk it through started a delivery decided %s", got)
	}
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'task.decider_changed'
		AND payload->>'why' = 'talk it through' AND payload->>'voice' = 'chatty'`, task); n != 1 {
		t.Errorf("Talk it through's start recorded %d decider changes saying chatty", n)
	}
}

// acceptedDelivery is a task its owner handed to Deliver's policy by
// accepting conductor_unavailable: the task and its owner.
func (w *world) acceptedDelivery() (task, owner string) {
	w.t.Helper()
	w.waitingFor(100 * time.Millisecond)
	task = w.task()
	owner, _, _ = w.people(task)
	w.quietUnavailable(task)
	if status, out := w.decide(owner, task, "accept", ""); status != 200 {
		w.t.Fatalf("accept: %d %v", status, out)
	}
	if got := w.deciderState(task); !strings.HasPrefix(got, "policy/") {
		w.t.Fatalf("accepted: %s", got)
	}
	return task, owner
}

// A delivery whose person accepted Deliver's defaults is Deliver's: a Stop
// and Start over, or a pick-up's Start over, that does not name who
// decides keeps attempt 2 Deliver's policy, never a conductor silently;
// naming the conductor hands it to a chatty one.
func TestStartingOverAnAcceptedDeliveryKeepsItDeliversUnlessAsked(t *testing.T) {
	for _, c := range []struct {
		via, decider, want string
	}{
		{"stop", "", "policy/"},
		{"recover", "", "policy/"},
		{"recover", "policy", "policy/"},
		{"stop", "conductor", "conductor/chatty"},
	} {
		t.Run(c.via+"/"+c.decider, func(t *testing.T) {
			w := conducting(t)
			task, owner := w.acceptedDelivery()
			body := map[string]any{"note": "again"}
			if c.decider != "" {
				body["decider"] = c.decider
			}
			if c.via == "stop" {
				body["andRestart"] = true
				if out := w.stop(owner, task, body); out["attempt"] != float64(2) {
					t.Fatalf("stop and start over answered %v", out)
				}
			} else {
				w.stop(owner, task, map[string]any{"note": "wrong"})
				body["action"] = "restart"
				w.pickUp(owner, task, body)
			}
			got := w.str(`SELECT COALESCE(state->>'decider', 'policy') || '/' || COALESCE(state->>'voice', '')
				FROM workflow_runs WHERE task_id = $1 AND (state->>'attempt')::int = 2`, task)
			if !strings.HasPrefix(got, c.want) {
				t.Errorf("attempt 2 decided %s, want %s", got, c.want)
			}
			if c.want == "policy/" {
				if n := w.count(`SELECT count(*) FROM workflow_runs WHERE task_id = $1 AND (state->>'attempt')::int = 2
					AND (state->>'hadConductor')::boolean`, task); n != 1 {
					t.Errorf("attempt 2 lost the conductor's fix bar")
				}
				if n := w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND role = 'conductor' AND attempt = 2`, task); n != 0 {
					t.Errorf("attempt 2 of an accepted delivery has %d conductors", n)
				}
			}
		})
	}
}
