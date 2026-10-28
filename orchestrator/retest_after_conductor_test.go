package orchestrator_test

import (
	"context"
	"slices"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/fakelux"
)

// A conducted delivery tested once reaches the test step again by a
// conductor's later decisions: a new tester runs on the heads the delivery
// is at then, and the delivery comes to the decision before the pull
// request, never waiting on the first tester, whose phase.finished was
// taken long ago.
//   - another round: the conductor sends it back for a review, a fix and a
//     clean review, and decides next (the simplifier changes nothing);
//   - a simplify: at the gate, the conductor starts a simplifier that
//     changes nothing, which goes on to the test with no review round
//     between, so only the decision tells the visits apart.
func TestAConductedDeliveryReachingTheTestAgainRunsANewTester(t *testing.T) {
	for name, back := range map[string]func(w *world, task string){
		"another round": func(w *world, task string) {
			w.must(task, "start_phase", `{"phase":"review","categories":["correctness"]}`)
			w.agentsDone(task, "the review sent back", func() bool { return w.decisionAt(task) == delivery.PointReviewed })
			w.must(task, "start_phase", `{"phase":"fix"}`)
			w.agentsDone(task, "the fix", func() bool { return w.decisionAt(task) == delivery.PointFixed })
			w.must(task, "start_phase", `{"phase":"review","categories":["correctness"]}`)
			w.agentsDone(task, "the clean review", func() bool { return w.decisionAt(task) == delivery.PointReviewed })
			w.finishReview(task)
		},
		"a simplify": func(w *world, task string) {
			w.must(task, "start_phase", `{"phase":"simplify"}`)
		},
	} {
		t.Run(name, func(t *testing.T) { testedAgain(t, back) })
	}
}

func testedAgain(t *testing.T, back func(w *world, task string)) {
	w := conducting(t)
	scripted := w.lux.Decide
	w.lux.Decide = func(spec map[string]any) fakelux.Behaviour {
		b := scripted(spec)
		if labels, _ := spec["labels"].(map[string]any); labels["dude.phase"] == "simplify" {
			// Nothing to simplify: the workflow goes straight to the test.
			b.Commit = nil
		}
		return b
	}
	task := w.task()
	w.talk(task)
	// The scripted writers' Markdown files are product changes here, so a
	// later visit runs a tester rather than carrying the first's verdicts.
	setPolicy(w, task, `{"test":true,"simplify":true,"nonProductPaths":[".github/**"]}`)
	w.implemented(task)
	w.carriedOut(task, "decide", `{"action":"next"}`)
	if at := w.decisionAt(task); at != delivery.PointReviewed {
		t.Fatalf("at %q, want after the review\n%s", at, w.describeRuns())
	}
	w.finishReview(task)
	w.agentsDone(task, "the first test", func() bool { return w.decisionAt(task) == delivery.PointBeforePR })
	first := w.ids(`SELECT id FROM runs WHERE task_id = $1 AND phase = 'test'`, task)
	if len(first) != 1 {
		t.Fatalf("%d testers before the pull request, want 1\n%s", len(first), w.describeRuns())
	}

	back(w, task)

	// The simplifier and the tester are finished as their agents would;
	// the first tester's phase.finished is never sent again.
	for range 6 {
		w.advanced()
		for _, id := range w.pendingPhaseRuns(task) {
			w.finished(id)
		}
		w.advanced()
		if w.decisionAt(task) == delivery.PointBeforePR {
			break
		}
	}
	var step string
	var pending []string
	_ = w.owner.QueryRow(context.Background(), `SELECT step, COALESCE(ARRAY(SELECT jsonb_array_elements_text(state->'pendingRunIds')), '{}')
		FROM workflow_runs WHERE task_id = $1`, task).Scan(&step, &pending)
	testers := w.ids(`SELECT id FROM runs WHERE task_id = $1 AND phase = 'test' ORDER BY created_at, id`, task)
	if len(testers) != 2 {
		t.Fatalf("%d testers after the conductor's decision, want a new one; the delivery is at %s (%q) waiting on %v (the first tester %s), skipped %d\n%s",
			len(testers), step, w.decisionAt(task), pending, first[0],
			w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = $2`, task, delivery.EvTestSkipped), w.describeRuns())
	}
	if slices.Contains(pending, first[0]) || step == "awaitTest" {
		t.Fatalf("the delivery is at %s waiting on %v, the first tester %s among them", step, pending, first[0])
	}
	if at := w.decisionAt(task); at != delivery.PointBeforePR {
		t.Fatalf("at %q (step %s), want before the pull request\n%s", at, step, w.describeRuns())
	}
	var secondAt, heads string
	_ = w.owner.QueryRow(context.Background(), `SELECT base_refs->>'target' FROM runs WHERE id = $1`, testers[1]).Scan(&secondAt)
	_ = w.owner.QueryRow(context.Background(), `SELECT state->'heads'->>'target' FROM workflow_runs WHERE task_id = $1`, task).Scan(&heads)
	if secondAt == "" || secondAt != heads {
		t.Errorf("the new tester ran on %q, want the delivery's heads %q", secondAt, heads)
	}
}
