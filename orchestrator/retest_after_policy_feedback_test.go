package orchestrator_test

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/marciomartins/dude/orchestrator/internal/delivery"
)

// Feedback noticed at opening (not at a hold) takes the policy through
// fix and review without a conductor decision or a person's retry. The
// review round alone must make the next test visit different.
func TestPolicyFeedbackAfterATestRunsANewTester(t *testing.T) {
	for _, simplify := range []bool{false, true} {
		name := "without simplify"
		if simplify {
			name = "with simplify"
		}
		t.Run(name, func(t *testing.T) {
			w := newWorld(t)
			ana := w.person("Ana")
			task := w.task()
			p := scriptedPolicy()
			p.Test, p.Simplify = true, simplify
			if err := w.app.InOrg(context.Background(), w.org, func(tx pgx.Tx) error {
				return delivery.NameOnlyRepository(context.Background(), tx, task)
			}); err != nil {
				t.Fatal(err)
			}
			w.start(task, p)
			first := w.upTo(task, "test")
			w.finished(first)
			if code, body := w.feedback(ana, task, "fbi_late357", "Use a clearer greeting."); code != 201 {
				t.Fatalf("feedback: %d %v", code, body)
			}
			fix := w.upTo(task, "fix")
			w.finished(fix)
			if code, body := w.callAs(ana, "/internal/feedback/fbi_late357/resolve", map[string]any{}); code != 200 {
				t.Fatalf("resolve: %d %v", code, body)
			}
			for range 10 {
				w.advanced()
				for _, id := range w.pendingPhaseRuns(task) {
					w.finished(id)
				}
				w.advanced()
				if len(w.gh.Pulls()) > 0 {
					break
				}
			}
			var step string
			var decisions, retries int
			if err := w.owner.QueryRow(context.Background(), `SELECT step,
				COALESCE((state->>'decisions')::int, 0), COALESCE((state->>'retries')::int, 0)
				FROM workflow_runs WHERE task_id = $1`, task).Scan(&step, &decisions, &retries); err != nil {
				t.Fatal(err)
			}
			if decisions != 0 || retries != 0 {
				t.Fatalf("the policy path used %d decisions and %d retries, want neither", decisions, retries)
			}
			testers := w.ids(`SELECT id FROM runs WHERE task_id = $1 AND phase = 'test' ORDER BY created_at, id`, task)
			if len(testers) != 2 || len(w.gh.Pulls()) != 1 {
				t.Fatalf("%d testers, %d pull requests; step %s, first tester %s\n%s",
					len(testers), len(w.gh.Pulls()), step, first, w.describeRuns())
			}
			var testedAt, heads string
			if err := w.owner.QueryRow(context.Background(), `SELECT base_refs->>'target' FROM runs WHERE id = $1`, testers[1]).Scan(&testedAt); err != nil {
				t.Fatal(err)
			}
			if err := w.owner.QueryRow(context.Background(), `SELECT state->'heads'->>'target' FROM workflow_runs WHERE task_id = $1`, task).Scan(&heads); err != nil {
				t.Fatal(err)
			}
			if testedAt == "" || testedAt != heads {
				t.Errorf("new tester at %q, want current head %q", testedAt, heads)
			}
		})
	}
}
