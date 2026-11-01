package orchestrator_test

// No counter under the conductor: how many review rounds a conducted loop
// runs, and how many fixes a finding gets, is the conductor's judgement,
// with the round wake's facts. Nothing of the review loop's refuses its
// tools, and no conducted round escalates on a count.

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/fakeagent"
	"github.com/marciomartins/dude/orchestrator/internal/fakelux"
)

// Past the policy's review iterations, review, fix and next are all still
// the conductor's to take, at a decision and at the pull request's wait.
func TestNoReviewRoundBoundRefusesAConductedLoop(t *testing.T) {
	t.Run("at a decision", func(t *testing.T) {
		w := conducting(t)
		task := w.task()
		w.talk(task)
		setPolicy(w, task, `{"maxReviewIterations":2}`)
		w.implemented(task)
		w.must(task, "decide", `{"action":"next"}`)
		w.until("round 1", func() bool { return w.decisionAt(task) == delivery.PointReviewed })
		w.must(task, "start_phase", `{"phase":"fix"}`)
		w.until("after the fix", func() bool { return w.decisionAt(task) == delivery.PointFixed })
		w.must(task, "start_phase", `{"phase":"review"}`)
		w.until("round 2, clean", func() bool { return w.decisionAt(task) == delivery.PointReviewed && w.phaseRuns(task, "review") == 2 })
		// Both rounds spent: a third review round is the conductor's call.
		w.must(task, "start_phase", `{"phase":"review"}`)
		w.until("round 3", func() bool { return w.decisionAt(task) == delivery.PointReviewed && w.phaseRuns(task, "review") == 3 })
		if got := w.iteration(task); got != 3 {
			t.Fatalf("round %d, want 3", got)
		}
		// A finding of round 3's, to fix past the bound.
		mustExec(t, w.owner, `INSERT INTO review_findings (id, organization_id, task_id, run_id, category, severity, title,
				description, suggested_fix)
			SELECT 'fnd_r3_'||$1, organization_id, task_id, id, 'correctness', 'high', 'R3', 'r3', 'r3'
			FROM runs WHERE task_id = $1 AND phase = 'review' ORDER BY created_at DESC LIMIT 1`, task)
		w.must(task, "start_phase", `{"phase":"fix"}`)
		w.until("after the fix", func() bool { return w.decisionAt(task) == delivery.PointFixed })
		w.must(task, "decide", `{"action":"next"}`)
	})
	t.Run("at the pull request's wait", func(t *testing.T) {
		w := conducting(t)
		task := w.reviewing()
		if status, out := w.chat(task, "Look after the pull request for me."); status >= 300 {
			t.Fatalf("chat: %d %v", status, out)
		}
		w.until("waiting on the pull request", func() bool { return w.waitingOnPR(task) })
		mustExec(t, w.owner, `UPDATE workflow_runs SET state = jsonb_set(state, '{iteration}', to_jsonb((state->'policy'->>'maxReviewIterations')::int))
			WHERE task_id = $1`, task)
		reviews := w.phaseRuns(task, "review")
		w.must(task, "start_phase", `{"phase":"review"}`)
		w.until("the review it started", func() bool { return w.phaseRuns(task, "review") > reviews })
	})
}

// A finding fixed once, at one fix attempt per finding, is the conductor's
// to send to the fixer again; the delivery does not escalate stuck.
func TestNoFixAttemptBoundRefusesAConductedLoop(t *testing.T) {
	w := conducting(t)
	task := w.task()
	w.talk(task)
	setPolicy(w, task, `{"maxAttemptsPerFinding":1,"maxReviewIterations":9}`)
	w.implemented(task)
	w.must(task, "decide", `{"action":"next"}`)
	w.until("round 1", func() bool { return w.decisionAt(task) == delivery.PointReviewed })
	var a string
	_ = w.owner.QueryRow(context.Background(), `SELECT id FROM review_findings WHERE task_id = $1`, task).Scan(&a)
	w.must(task, "start_phase", `{"phase":"fix","findings":["`+a+`"]}`)
	w.until("after the fix", func() bool { return w.decisionAt(task) == delivery.PointFixed })
	w.must(task, "start_phase", `{"phase":"fix","findings":["`+a+`"]}`)
	w.until("after the second fix", func() bool { return w.decisionAt(task) == delivery.PointFixed && w.phaseRuns(task, "fix") == 2 })
	if r := w.escalationReason(task); r != "" {
		t.Errorf("escalated %s on the finding's second fix", r)
	}
}

// iteration is the task's delivery's review rounds so far.
func (w *world) iteration(task string) int {
	return w.count(`SELECT COALESCE((state->>'iteration')::int, 0) FROM workflow_runs WHERE task_id = $1
		ORDER BY created_at DESC LIMIT 1`, task)
}

// stillReviews makes the world's reviews raise Finding once, then judge it
// still after every fix; a fix commits. With regress, each review after the
// first judges it fixed instead, and raises a new blocker the fix caused.
func stillReviews(w *world, regress bool) {
	scripted := w.lux.Decide
	var reviews atomic.Int32
	w.lux.Decide = func(spec map[string]any) fakelux.Behaviour {
		labels, _ := spec["labels"].(map[string]any)
		switch labels["dude.phase"] {
		case "review":
			n := reviews.Add(1)
			if n == 1 {
				return fakelux.Behaviour{Reply: "```yaml\n" + fakeagent.Finding + "```\n"}
			}
			if regress {
				return fakelux.Behaviour{Reply: "```yaml\nverdicts:\n  F1: fixed\n---\n" +
					evidencedFinding("severity: high", fmt.Sprintf("Regression %d", n-1)) + "caused_by_fix: yes\n```\n"}
			}
			return fakelux.Behaviour{Reply: fakeagent.StillVerdict}
		case "implement", "fix":
			return fakelux.Behaviour{Commit: map[string]string{"FACTORY.md": fmt.Sprint(labels["dude.run"]) + "\n"}, Message: "work"}
		}
		return scripted(spec)
	}
}

// A conducted round past the policy's still and regression bounds parks for
// the conductor at after_review, never escalating not_converging: whether
// the loop converges is the conductor's to judge, on the round wake's facts.
func TestAConductedLoopNeverEscalatesNotConverging(t *testing.T) {
	for _, c := range []struct {
		name    string
		regress bool
		policy  string
	}{
		{"still", false, `{"maxStillRounds":1,"maxAttemptsPerFinding":9,"maxReviewIterations":9}`},
		{"regression", true, `{"maxStillRounds":0,"maxRegressionRounds":1,"maxAttemptsPerFinding":9,"maxReviewIterations":9}`},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := conducting(t)
			stillReviews(w, c.regress)
			task := w.task()
			w.talk(task)
			setPolicy(w, task, c.policy)
			w.implemented(task)
			w.carriedOut(task, "decide", `{"action":"next"}`)
			for round := 2; round <= 3; round++ {
				w.until(fmt.Sprintf("round %d's decision", round-1), func() bool { return w.decisionAt(task) == delivery.PointReviewed })
				w.carriedOut(task, "start_phase", `{"phase":"fix"}`)
				w.until("after the fix", func() bool { return w.decisionAt(task) == delivery.PointFixed })
				w.carriedOut(task, "decide", `{"action":"next"}`)
				w.until(fmt.Sprintf("round %d parked, or escalated", round), func() bool {
					return w.escalationReason(task) != "" || w.decisionAt(task) == delivery.PointReviewed && w.iteration(task) == round
				})
				if r := w.escalationReason(task); r != "" {
					t.Fatalf("round %d escalated %s\n%s", round, r, w.describeRuns())
				}
			}
		})
	}
}

// A conducted direct fix's narrow loop parks for the conductor after each
// round past maxPrFixReviewIterations, and never escalates.
func TestAConductedNarrowLoopParksPastItsBound(t *testing.T) {
	e := conductedDirect(t)
	setPolicy(e.world, e.task, `{"maxPrFixReviewIterations":1,"maxAttemptsPerFinding":1,"maxStillRounds":1}`)
	stillReviews(e.world, false)
	e.feedbackToDecide()
	e.must(e.task, "decide", `{"action":"next"}`)
	for round := 1; round <= 3; round++ {
		e.until(fmt.Sprintf("narrow round %d parked, or escalated", round), func() bool {
			e.sync()
			return e.escalationReason(e.task) != "" || e.decisionAt(e.task) == delivery.PointReviewed && e.phaseRuns(e.task, "review") >= round
		})
		if r := e.escalationReason(e.task); r != "" {
			t.Fatalf("narrow round %d escalated %s\n%s", round, r, e.describeRuns())
		}
		e.must(e.task, "start_phase", `{"phase":"fix","then":{"review":["correctness"]}}`)
		e.until("the fix's review started", func() bool { e.sync(); return e.phaseRuns(e.task, "review") > round })
	}
}

// A delivery a conductor had, handed back to Deliver, is decided by the
// policy again and keeps its exits: one fix attempt per finding, its finding
// still open after its fix, escalates stuck.
func TestAHandedBackDeliveryKeepsThePolicysExits(t *testing.T) {
	w := conducting(t)
	stillReviews(w, false)
	task := w.task()
	w.talk(task)
	setPolicy(w, task, `{"maxAttemptsPerFinding":1,"maxReviewIterations":9,"maxStillRounds":0}`)
	w.implemented(task)
	w.carriedOut(task, "decide", `{"action":"next"}`)
	w.until("round 1", func() bool { return w.decisionAt(task) == delivery.PointReviewed })
	if status, out := w.handBack(task, map[string]any{"decider": "policy"}); status != 200 {
		t.Fatalf("hand back: %d %v", status, out)
	}
	w.until("stuck", func() bool { w.pump(); return w.escalationReason(task) == "stuck" })
}
