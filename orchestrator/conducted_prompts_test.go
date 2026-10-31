package orchestrator_test

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
)

// A reviewer's instruction file asks for each finding's evidence only in a
// conducted loop: a delivery frozen reviewLoop conductor whose conductor
// decides as the reviewer is submitted. The same conducted delivery frozen
// reviewLoop policy briefs its reviewer as before.
func TestAConductedLoopsReviewerIsAskedForEvidence(t *testing.T) {
	const asked = "evidence_kind: failing_test | mutation | command | query_plan | trace"
	for _, c := range []struct {
		loop string
		want bool
	}{{delivery.ReviewLoopConductor, true}, {delivery.ReviewLoopPolicy, false}} {
		t.Run(c.loop, func(t *testing.T) {
			w := conducting(t)
			task := w.task()
			w.talk(task)
			setPolicy(w, task, `{"reviewLoop":"`+c.loop+`"}`)
			w.implemented(task)
			w.must(task, "start_phase", `{"phase":"review","categories":["correctness"]}`)
			var review string
			w.until("the reviewer on lux", func() bool {
				_ = w.owner.QueryRow(context.Background(), `SELECT id FROM runs WHERE task_id = $1 AND phase = 'review'`, task).Scan(&review)
				return review != "" && w.luxRunOf(review) != ""
			})
			if got := strings.Contains(w.instructionsOf(review), asked); got != c.want {
				t.Errorf("reviewLoop %s: the reviewer is asked for evidence: %v", c.loop, got)
			}
		})
	}
}

// A Deliver delivery frozen reviewLoop conductor has no conductor deciding:
// its reviewers are briefed as the policy loop's.
func TestADeliverDeliverysReviewerIsBriefedAsThePolicyLoops(t *testing.T) {
	w := newWorld(t)
	task := w.task()
	if err := w.app.InOrg(context.Background(), w.org, func(tx pgx.Tx) error {
		return delivery.NameOnlyRepository(context.Background(), tx, task)
	}); err != nil {
		t.Fatal(err)
	}
	p := scriptedPolicy()
	p.ReviewLoop = delivery.ReviewLoopConductor
	w.start(task, p)
	var review string
	w.until("the reviewer on lux", func() bool {
		_ = w.owner.QueryRow(context.Background(), `SELECT id FROM runs WHERE task_id = $1 AND phase = 'review'`, task).Scan(&review)
		return review != "" && w.luxRunOf(review) != ""
	})
	if strings.Contains(w.instructionsOf(review), "evidence_kind: failing_test | mutation | command | query_plan | trace") {
		t.Error("an un-conducted delivery's reviewer is asked for evidence")
	}
}
