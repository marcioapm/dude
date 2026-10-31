package orchestrator_test

import (
	"context"
	"strings"
	"testing"
)

// askedForEvidence is the line of a reviewer's finding format that asks
// for each finding's evidence (contracts/finding-format).
const askedForEvidence = "evidence_kind: failing_test | mutation | command | query_plan | trace"

// A reviewer's instruction file asks for each finding's evidence only in a
// conducted loop: a delivery whose conductor decides as the reviewer is
// submitted. An un-conducted (Deliver) delivery briefs its reviewer as
// before.
func TestAConductedLoopsReviewerIsAskedForEvidence(t *testing.T) {
	for _, c := range []struct {
		name string
		want bool
	}{{"conducted", true}, {"deliver", false}} {
		t.Run(c.name, func(t *testing.T) {
			w := conducting(t)
			task := w.task()
			if c.want {
				w.talk(task)
				w.implemented(task)
				w.must(task, "start_phase", `{"phase":"review","categories":["correctness"]}`)
			} else {
				w.deliver(task)
			}
			review := w.reviewOnLux(task)
			if got := strings.Contains(w.instructionsOf(review), askedForEvidence); got != c.want {
				t.Errorf("%s: the reviewer is asked for evidence: %v", c.name, got)
			}
		})
	}
}

// A Deliver delivery has no conductor deciding: its reviewers are briefed
// as the policy loop's.
func TestADeliverDeliverysReviewerIsBriefedAsThePolicyLoops(t *testing.T) {
	w := newWorld(t)
	task := w.task()
	w.deliver(task)
	review := w.reviewOnLux(task)
	if strings.Contains(w.instructionsOf(review), askedForEvidence) {
		t.Error("an un-conducted delivery's reviewer is asked for evidence")
	}
}

// reviewOnLux is the task's review Run, once lux runs it.
func (w *world) reviewOnLux(task string) string {
	w.t.Helper()
	var review string
	w.until("the reviewer on lux", func() bool {
		_ = w.owner.QueryRow(context.Background(), `SELECT id FROM runs WHERE task_id = $1 AND phase = 'review'`, task).Scan(&review)
		return review != "" && w.luxRunOf(review) != ""
	})
	return review
}
