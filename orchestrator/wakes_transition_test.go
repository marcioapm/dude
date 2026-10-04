package orchestrator_test

import (
	"context"
	"sync"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
)

// A conducted step reaching a decision: its wake is recorded with the
// transition that parks the delivery, never before it. Swept while the
// transition is still uncommitted, nothing is told; the transition then
// loses its lease and rolls back, and the step's replay records the wake
// afresh, pending, so the conductor is told once the delivery waits.
func TestADecisionWakeCommitsWithItsTransition(t *testing.T) {
	w := conducting(t)
	task := w.task()
	var once sync.Once
	var lost int64
	armed := w.gated(func(step, next string) {
		if step != "awaitImplement" || next == step {
			return
		}
		once.Do(func() {
			mustExec(t, w.owner, `UPDATE conductor_wakes SET created_at = now() - interval '1 minute' WHERE task_id = $1`, task)
			w.sweep()
			if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'conductor.woken'
				AND payload->>'text' LIKE '%after implement%'`, task); n != 0 {
				t.Errorf("the conductor was told of the decision before the delivery waited on it")
			}
			// Another poller's now: this transition must not land.
			mustExec(t, w.owner, `UPDATE workflow_runs SET locked_until = now() - interval '1 second' WHERE task_id = $1`, task)
			_ = w.owner.QueryRow(context.Background(), `SELECT max(cursor) FROM events`).Scan(&lost)
		})
	})
	w.talk(task)
	armed.Store(true)
	w.must(task, "start_phase", `{"phase":"implement"}`)
	w.until("after implement", func() bool { return w.decisionAt(task) == delivery.PointImplemented })
	w.wokenWith(task, "after implement")
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'conductor.woken'
		AND payload->>'text' LIKE '%after implement%' AND cursor > $2`, task, lost); n != 1 {
		t.Errorf("%d notes of the decision after the lost transition, want the replay's 1", n)
	}
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'conductor.decision_awaited'
		AND payload->>'point' = 'after_implement'`, task); n != 1 {
		t.Errorf("%d decision_awaited events for after implement, want 1", n)
	}
}
