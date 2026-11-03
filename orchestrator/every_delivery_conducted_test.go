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
	"github.com/marciomartins/dude/orchestrator/internal/fakeagent"
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

// defaultsConducting is a conducting world whose conductor is on the
// defaults-taking scripted model (fake/defaults) and whose decisions wait
// an hour: any decision left to the wait shows as one not taken.
func defaultsConducting(t *testing.T) *world {
	w := conducting(t)
	w.onModel("conductor", fakeagent.DefaultsModel)
	w.waitingFor(time.Hour)
	return w
}

// A conductor on the defaults-taking scripted model (fake/defaults, the
// e2e suites' conductor) decides each decision it is woken for by
// Deliver's defaults at once: Deliver's quiet delivery reaches its pull
// request with no decision left to its wait, and none defaulted.
func TestADefaultsTakingConductorDeliversToThePullRequest(t *testing.T) {
	w := defaultsConducting(t)
	task := w.task()
	w.deliverByAPI(task)
	w.until("the pull request", func() bool { return len(w.gh.Pulls()) == 1 })
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = $2
		AND payload->>'action' = 'next'`, task, delivery.EvDecisionTaken); n < 2 {
		t.Errorf("the conductor took %d decisions, want at least after implement and after review", n)
	}
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = $2`, task, delivery.EvDecisionDefaulted); n != 0 {
		t.Errorf("%d decisions left to the wait", n)
	}
}

// Feedback on a quiet delivery goes to its conductor (step 3b, PR C1), and
// reaches the work: the defaults-taking conductor (fake/defaults, the e2e
// suites') steers the working implementer with the person's items, as
// Deliver's policy did before every delivery had one.
func TestFeedbackOnAQuietDeliveryReachesTheWorkingImplementerThroughItsConductor(t *testing.T) {
	w := conducting(t)
	w.onModel("conductor", fakeagent.DefaultsModel)
	w.onModel("implementer", "fake/hang")
	w.waitingFor(time.Hour)
	ana := w.person("Ana")
	task := w.task()
	w.deliverByAPI(task)
	w.until("the implementer at work", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'implement' AND status = 'running'`, task) == 1
	})
	impl := w.str(`SELECT id FROM runs WHERE task_id = $1 AND phase = 'implement'`, task)
	if code, body := w.feedback(ana, task, "fbi_working", "Keep the old endpoint as an alias."); code != 201 || body["routedTo"] != "conductor" {
		t.Fatalf("submit: %d %v", code, body)
	}
	w.until("the conductor's steer of the implementer", func() bool {
		return w.count(`SELECT count(*) FROM directives WHERE run_id = $1 AND strpos(text, 'Keep the old endpoint as an alias.') > 0`, impl) == 1
	})
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'agent.tool.dude'
		AND payload->>'tool' = 'steer' AND payload->'arguments'->>'run' = $2`, task, impl); n != 1 {
		t.Errorf("%d conductor steers of the implementer", n)
	}
}

// handBackAfterAccept hands an accepted delivery to its conductor, then back
// to it quietly (POST /decider), as its owner may: the accept is not
// its latest change of decider.
func (w *world) handBackAfterAccept(owner, task string) {
	w.t.Helper()
	for _, d := range []string{"conductor", "policy"} {
		if status, out := w.callAs(owner, "/internal/tasks/"+task+"/decider", map[string]any{"decider": d}); status != 200 {
			w.t.Fatalf("hand to %s: %d %v", d, status, out)
		}
	}
	if got := w.deciderState(task); !strings.HasPrefix(got, "conductor/quiet/") {
		w.t.Fatalf("handed back quietly after the accept: %s", got)
	}
}

// Accepted, then handed to the conductor quietly: the accept is not the
// latest change of decider, so a Stop and Start over on Deliver (policy)
// starts attempt 2 with a quiet conductor, not Deliver's policy.
func TestStartingOverAfterAnAcceptHandedBackIsConductedQuietly(t *testing.T) {
	w := conducting(t)
	task, owner := w.acceptedDelivery()
	w.handBackAfterAccept(owner, task)
	if out := w.stop(owner, task, map[string]any{"note": "again", "andRestart": true, "decider": "policy"}); out["attempt"] != float64(2) {
		t.Fatalf("stop and start over answered %v", out)
	}
	got := w.str(`SELECT COALESCE(state->>'decider', 'policy') || '/' || COALESCE(state->>'voice', '')
		FROM workflow_runs WHERE task_id = $1 AND (state->>'attempt')::int = 2`, task)
	if got != "conductor/quiet" {
		t.Errorf("attempt 2 decided %s, want conductor/quiet", got)
	}
}
