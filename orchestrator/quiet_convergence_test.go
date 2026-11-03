package orchestrator_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/fakeagent"
)

// A quiet delivery's loop that is not converging stops for a person as
// Deliver's policy's does: a reviewer judging its finding still every
// round (fake/stuck), with attempts and rounds to spare, escalates
// not_converging after two fixes, quiet as on Deliver's policy for a
// conductor with no model, not exhausted after five.
func TestAQuietDeliveryNotConvergingStopsAsDeliversPolicyDoes(t *testing.T) {
	for _, quiet := range []bool{true, false} {
		name := map[bool]string{true: "quiet", false: "no model"}[quiet]
		t.Run(name, func(t *testing.T) {
			w := conducting(t)
			if quiet {
				w.onModel("conductor", fakeagent.DefaultsModel)
				w.waitingFor(time.Hour)
			} else {
				w.noConductorModel()
			}
			w.onModel("reviewer", fakeagent.StuckModel)
			p := delivery.DefaultPolicy()
			p.MaxAttemptsPerFinding, p.MaxReviewIterations = 99, 6
			p.MaxStillRounds, p.MaxRegressionRounds = 2, 2
			raw, err := json.Marshal(p)
			if err != nil {
				t.Fatal(err)
			}
			mustExec(t, w.owner, `UPDATE projects SET delivery_policy = $2 WHERE id = $1`, w.project, raw)
			task := w.task()
			w.deliverByAPI(task)
			want := map[bool]string{true: "conductor/quiet/false", false: "policy/quiet/false"}[quiet]
			if got := w.deciderState(task); got != want {
				t.Fatalf("decided %s, want %s", got, want)
			}
			w.until("the loop's escalation", func() bool { return w.escalationReason(task) != "" })
			if r := w.escalationReason(task); r != delivery.ReasonNotConverging {
				t.Errorf("%s delivery escalated %s, want %s", name, r, delivery.ReasonNotConverging)
			}
			if n := w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'fix'`, task); n != 2 {
				t.Errorf("%s delivery ran %d fixes, want 2", name, n)
			}
		})
	}
}
