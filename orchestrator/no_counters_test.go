package orchestrator_test

// No counter under the conductor: how many review rounds a conducted loop
// runs, and how many fixes a finding gets, is the conductor's judgement,
// with the round wake's facts. Nothing of the review loop's refuses its
// tools, and no conducted round escalates on a count.

import (
	"context"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
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
