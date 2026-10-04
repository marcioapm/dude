package orchestrator_test

// The pull request gate across a change of decider: what a hand-back
// authorises survives a step committing the state it claimed before, and a
// gate the conductor entered is not dropped by handing back while entering.

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/forge"
	"github.com/marciomartins/dude/orchestrator/internal/workflow"
)

// gated registers the delivery workflow with hook called before each
// transition commits, armed only once the test says so.
func (w *world) gated(hook func(step, next string)) *atomic.Bool {
	armed := &atomic.Bool{}
	def := delivery.Workflow(&delivery.Store{DB: w.app}, forge.Resolver{DB: w.app})
	def.BeforeCommit = func(_, step, next string) {
		if armed.Load() {
			hook(step, next)
		}
	}
	w.runtime.Register(def)
	return armed
}

func (w *world) handBack(task string, body map[string]any) (int, map[string]any) {
	return w.call("/internal/tasks/"+task+"/decider", body)
}

func gateRefused(out map[string]any) bool {
	e, _ := out["error"].(map[string]any)
	return e["code"] == "pull_request_gate"
}

// The person answered at the gate. A pending conductor.decision signal has
// the parked step claim the untaken decision; while it runs, the person
// hands back, which writes the opening's authorization: their Draft, or
// their confirmation past another answer. The step's transition, written
// from the state it claimed, keeps it.
func TestAHandBackDraftSurvivesAClaimedGateTick(t *testing.T) {
	for _, c := range []struct {
		answer  string
		confirm bool
		draft   bool
	}{{"Draft", false, true}, {"Show me the diff", true, false}} {
		t.Run(c.answer, func(t *testing.T) {
			w := conducting(t)
			task := w.task()
			var once sync.Once
			armed := w.gated(func(step, next string) {
				if step != "conductorDecision" {
					return
				}
				once.Do(func() {
					if status, out := w.handBack(task, map[string]any{"decider": "policy", "openPullRequest": c.confirm}); status != 200 {
						t.Errorf("hand-back: %d %v", status, out)
					}
				})
			})
			w.reachGate(task)
			w.must(task, "decide", `{"action":"ask_person"}`)
			w.chat(task, c.answer)
			ctx := context.Background()
			var wf string
			if err := w.owner.QueryRow(ctx, `SELECT id FROM workflow_runs WHERE task_id = $1`, task).Scan(&wf); err != nil {
				t.Fatal(err)
			}
			armed.Store(true)
			if err := w.runtime.Signal(ctx, w.org, wf, delivery.SignalConductorDecision, map[string]any{"action": "policy"}, ""); err != nil {
				t.Fatal(err)
			}
			w.until("the pull request", func() bool { return len(w.gh.Pulls()) == 1 })
			if w.gh.Pulls()[0].Draft != c.draft {
				t.Errorf("draft %v, want %v", w.gh.Pulls()[0].Draft, c.draft)
			}
		})
	}
}

// A conducted step chose the gate; the person hands back before its
// transition commits. The hand-back is taken, but the gate the conductor
// entered stays: Deliver asks, and opens only on the person's Open, or on
// their confirmation at a hand-back.
func TestAHandBackWhileEnteringTheGateStillAsks(t *testing.T) {
	for _, how := range []string{"answer Open", "confirm the opening"} {
		t.Run(how, func(t *testing.T) {
			w := conducting(t)
			task := w.task()
			var once sync.Once
			// The step's result is the policy's (openPullRequest); the
			// transition's checkpoint parks it at the conductor's gate.
			armed := w.gated(func(step, next string) {
				if step != "test" || next == step {
					return
				}
				once.Do(func() {
					if status, out := w.handBack(task, map[string]any{"decider": "policy"}); status != 200 {
						t.Errorf("hand-back while entering the gate: %d %v", status, out)
					}
				})
			})
			w.talk(task)
			w.must(task, "start_phase", `{"phase":"implement"}`)
			w.until("after implement", func() bool { return w.decisionAt(task) == delivery.PointImplemented })
			armed.Store(true)
			w.must(task, "start_phase", `{"phase":"simplify"}`)
			w.until("the gate's question", func() bool { return w.conductorQuestion(task) != "" || len(w.gh.Pulls()) > 0 })
			for range 3 {
				w.pump()
			}
			if n := len(w.gh.Pulls()); n != 0 {
				t.Fatalf("%d pull requests opened with the gate unanswered", n)
			}
			if w.decider(task) != "policy" {
				t.Fatalf("decider %s after the hand-back", w.decider(task))
			}
			if status, out := w.handBack(task, map[string]any{"decider": "policy"}); status != 409 || !gateRefused(out) {
				t.Fatalf("hand-back at the gate, unanswered: %d %v", status, out)
			}
			if how == "answer Open" {
				w.chat(task, "Open")
			} else if status, out := w.handBack(task, map[string]any{"decider": "policy", "openPullRequest": true}); status != 200 {
				t.Fatalf("confirmed hand-back: %d %v", status, out)
			}
			w.until("the pull request", func() bool { return len(w.gh.Pulls()) == 1 })
		})
	}
}

// The person's Open or Draft authorizes the heads it was given at. Handed
// back at the gate and taken back before the opening, the conductor starts
// a simplifier that moves the head, and hands back again: Deliver asks
// again at the new head, opens nothing until it is answered, and then
// opens as that answer says.
func TestAGateAuthorizationIsForItsHeads(t *testing.T) {
	for _, c := range []struct {
		first, again string
		draft        bool
	}{{"Open", "Open", false}, {"Draft", "Draft", true}, {"Draft", "Open", false}} {
		t.Run(c.first+" then "+c.again, func(t *testing.T) {
			w := conducting(t)
			task := w.task()
			w.reachGate(task)
			w.must(task, "decide", `{"action":"ask_person"}`)
			w.chat(task, c.first)
			if status, out := w.handBack(task, map[string]any{"decider": "policy"}); status != 200 {
				t.Fatalf("hand-back at the gate: %d %v", status, out)
			}
			if status, out := w.handBack(task, map[string]any{"decider": "conductor"}); status != 200 {
				t.Fatalf("taking the decisions back: %d %v", status, out)
			}
			w.pump()
			if n := len(w.gh.Pulls()); n != 0 {
				t.Fatalf("%d pull requests opened under the conductor", n)
			}
			simplifiers := w.phaseRuns(task, "simplify")
			w.must(task, "start_phase", `{"phase":"simplify"}`)
			if status, out := w.handBack(task, map[string]any{"decider": "policy"}); status != 200 {
				t.Fatalf("hand-back during the simplifier: %d %v", status, out)
			}
			w.until("Deliver's gate question at the new head", func() bool {
				return w.phaseRuns(task, "simplify") == simplifiers+1 && w.conductorQuestion(task) != "" || len(w.gh.Pulls()) > 0
			})
			for range 3 {
				w.pump()
			}
			if n := len(w.gh.Pulls()); n != 0 {
				t.Fatalf("%d pull requests opened at a head the person did not authorize", n)
			}
			if status, out := w.chat(task, c.again); status != 200 || out["questionId"] == nil {
				t.Fatalf("answer at the new head: %d %v", status, out)
			}
			w.until("the pull request", func() bool { return len(w.gh.Pulls()) == 1 })
			if w.gh.Pulls()[0].Draft != c.draft {
				t.Errorf("draft %v, want %v", w.gh.Pulls()[0].Draft, c.draft)
			}
		})
	}
}

// Deliver holds the gate the conductor entered and the person answers
// Open. A worker claims the parked step, reads that answer, and stalls
// before its transition commits; its lease lapses. The person gives the
// decisions back to the conductor, which asks again; they answer Draft and
// hand back. The stale worker's commit must not replace that newer Draft
// with the Open it read: the pull request opens as a draft.
func TestAStaleStepCannotAuthorizeTheGateOverANewerAnswer(t *testing.T) {
	w := conducting(t)
	task := w.task()
	ctx := context.Background()
	var once sync.Once
	armed := w.gated(func(step, next string) {
		if step != "test" || next == step {
			return
		}
		once.Do(func() {
			if status, out := w.handBack(task, map[string]any{"decider": "policy"}); status != 200 {
				t.Errorf("hand-back while entering the gate: %d %v", status, out)
			}
		})
	})
	w.talk(task)
	w.must(task, "start_phase", `{"phase":"implement"}`)
	w.until("after implement", func() bool { return w.decisionAt(task) == delivery.PointImplemented })
	armed.Store(true)
	w.must(task, "start_phase", `{"phase":"simplify"}`)
	w.until("Deliver's gate question", func() bool { return w.conductorQuestion(task) != "" || len(w.gh.Pulls()) > 0 })
	w.pump()
	if n := len(w.gh.Pulls()); n != 0 {
		t.Fatalf("%d pull requests opened with the gate unanswered", n)
	}
	armed.Store(false)

	// The stale worker: its own runtime, paused once its step has read the
	// gate's answer.
	reached, release := make(chan struct{}), make(chan struct{})
	var paused sync.Once
	store := &delivery.Store{DB: w.app}
	store.GateRead = func() {
		paused.Do(func() {
			close(reached)
			<-release
		})
	}
	stale := workflow.New(w.app, "stale", quiet)
	stale.Register(delivery.Workflow(store, forge.Resolver{DB: w.app}))

	if status, out := w.chat(task, "Open"); status != 200 {
		t.Fatalf("answer Open: %d %v", status, out)
	}
	done := make(chan error, 1)
	go func() {
		_, err := stale.Tick(ctx, 1)
		done <- err
	}()
	select {
	case <-reached:
	case <-time.After(20 * time.Second):
		close(release)
		t.Fatal("the stale worker never ran the gate's step")
	}
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	// Its lease lapses. Cleared rather than backdated, so the stalled
	// worker's renewal (guarded on its poller id) cannot extend it again.
	mustExec(t, w.owner, `UPDATE workflow_runs SET locked_by = NULL, locked_until = NULL WHERE task_id = $1`, task)

	if status, out := w.handBack(task, map[string]any{"decider": "conductor"}); status != 200 {
		t.Fatalf("giving the decisions back to the conductor: %d %v", status, out)
	}
	w.until("the gate parked for the conductor", func() bool { return w.decisionAt(task) == delivery.PointBeforePR })
	w.must(task, "decide", `{"action":"ask_person"}`)
	if status, out := w.chat(task, "Draft"); status != 200 {
		t.Fatalf("answer Draft: %d %v", status, out)
	}
	if status, out := w.handBack(task, map[string]any{"decider": "policy"}); status != 200 {
		t.Fatalf("hand-back on Draft: %d %v", status, out)
	}
	w.stepTo(task, "openPullRequest")
	if n := len(w.gh.Pulls()); n != 0 {
		t.Fatalf("%d pull requests opened before the stale worker resumed", n)
	}

	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	w.until("the pull request", func() bool { return len(w.gh.Pulls()) == 1 })
	if !w.gh.Pulls()[0].Draft {
		t.Errorf("the pull request opened ready for review: the stale worker's Open replaced the person's newer Draft")
	}
}

// Deliver from the start, never conducted: no gate, no question.
func TestADeliveryNeverConductedOpensWithoutAsking(t *testing.T) {
	w := conducting(t)
	task := w.task()
	w.deliver(task)
	w.until("the pull request", func() bool { return len(w.gh.Pulls()) == 1 })
	if n := w.count(`SELECT count(*) FROM questions WHERE task_id = $1`, task); n != 0 {
		t.Errorf("%d questions on a delivery never conducted", n)
	}
}
