package orchestrator_test

import (
	"context"
	"slices"
	"sync/atomic"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/fakeagent"
	"github.com/marciomartins/dude/orchestrator/internal/fakelux"
)

// A re-test resumed the first tester's lux Run, and lux stopped it at its
// time limit: the delivery escalates test_failed. A person's Try again
// starts a tester on a lux Run of its own, given the earlier rounds'
// conversation; the timed-out lux Run is never resumed again.
func TestTryAgainAfterATestLuxTimedOutIsNotResumedOntoIt(t *testing.T) {
	w := resuming(t)
	w.onModel("qa_browser", fakeagent.TestFailOnceModel)
	var tests atomic.Int32
	scripted := w.lux.Decide
	w.lux.Decide = func(spec map[string]any) fakelux.Behaviour {
		labels, _ := spec["labels"].(map[string]any)
		if labels["dude.phase"] == "test" && tests.Add(1) == 2 {
			// The re-test, on the first tester's lux Run, runs until lux
			// stops it.
			return fakelux.Behaviour{Hang: true, NeverWake: true}
		}
		return scripted(spec)
	}
	wi := w.task()
	mustExec(t, w.owner, `UPDATE tasks SET acceptance_criteria = '["it greets", "it says goodbye"]' WHERE id = $1`, wi)
	w.names(wi, w.repoID)
	owner, _, _ := w.people(wi)
	p := resumingPolicy()
	p.Test = true
	w.start(wi, p)

	var retest, luxID string
	w.until("the re-test to be running on the first tester's lux Run", func() bool {
		_ = w.owner.QueryRow(context.Background(), `SELECT id, lux_run_id FROM runs WHERE task_id = $1 AND phase = 'test'
			AND continues_how = 'resume' AND lux_state = 'running' AND lux_run_id IS NOT NULL`, wi).Scan(&retest, &luxID)
		return luxID != ""
	})
	w.until("the delivery to stop for a person", func() bool {
		w.lux.Timeout(luxID)
		return w.taskStatus(wi) == "awaiting_input"
	})
	if got := w.escalationReason(wi); got != "test_failed" {
		t.Fatalf("escalated for %q, want test_failed", got)
	}
	resumes := func() int {
		return len(slices.DeleteFunc(w.lux.CallsOf(luxID), func(c string) bool { return c != "resume" }))
	}
	before := resumes()
	// Not kept: its running time is spent, so Resume is not offered.
	if status, body := w.decide(owner, wi, "resume", ""); status < 400 {
		t.Fatalf("resuming the timed-out tester: %d %v, want refused", status, body)
	}

	if status, body := w.decide(owner, wi, "retry", "Test it again."); status != 200 {
		t.Fatalf("retry: %d %v", status, body)
	}
	var third, thirdLux, how, continues string
	w.until("a third tester on lux", func() bool {
		_ = w.owner.QueryRow(context.Background(), `SELECT id, COALESCE(lux_run_id, ''), COALESCE(continues_how, ''), COALESCE(continues_run_id, '')
			FROM runs WHERE task_id = $1 AND phase = 'test' AND id <> $2 AND created_at > (SELECT created_at FROM runs WHERE id = $2)`,
			wi, retest).Scan(&third, &thirdLux, &how, &continues)
		return thirdLux != ""
	})
	// The latest completed round is the first tester: its lux Run went to
	// the re-test, so it is replayed, not taken over.
	var first string
	_ = w.owner.QueryRow(context.Background(), `SELECT continues_run_id FROM runs WHERE id = $1`, retest).Scan(&first)
	if thirdLux == luxID || how != delivery.ContinuesReplay || continues != first {
		t.Fatalf("the tester after Try again is on lux Run %s (the timed-out one is %s), continuing %q how %q; want a replay on its own",
			thirdLux, luxID, continues, how)
	}
	if n := resumes(); n != before {
		t.Errorf("the timed-out lux Run was resumed %d more times after Try again", n-before)
	}
	if luxTests := w.luxRunsOf("test"); len(luxTests) != 2 {
		t.Errorf("%d testers submitted to lux, want the first and the one after Try again", len(luxTests))
	}
}
