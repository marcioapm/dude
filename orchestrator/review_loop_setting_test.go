package orchestrator_test

import (
	"context"
	"testing"
)

// A project that never set reviewLoop freezes conductor into every new
// delivery, Deliver's as Talk it through's; one that set policy freezes
// policy.
func TestEveryNewDeliveryFreezesTheConductorsReviewLoop(t *testing.T) {
	for _, c := range []struct{ set, want string }{{"", "conductor"}, {"policy", "policy"}} {
		t.Run("set "+c.set, func(t *testing.T) {
			w := newWorld(t)
			mustExec(t, w.owner, `UPDATE projects SET delivery_policy = delivery_policy - 'reviewLoop' WHERE id = $1`, w.project)
			if c.set != "" {
				mustExec(t, w.owner, `UPDATE projects SET delivery_policy = delivery_policy || jsonb_build_object('reviewLoop', $2::text)
					WHERE id = $1`, w.project, c.set)
			}
			wi := w.task()
			if status, body := w.call("/internal/tasks/"+wi+"/deliver", map[string]any{}); status != 201 {
				t.Fatalf("deliver: %d %v", status, body)
			}
			var frozen string
			_ = w.owner.QueryRow(context.Background(), `SELECT COALESCE(state->'policy'->>'reviewLoop', '') FROM workflow_runs WHERE task_id = $1`, wi).Scan(&frozen)
			if frozen != c.want {
				t.Errorf("the delivery froze reviewLoop %q, want %q", frozen, c.want)
			}
		})
	}
}
