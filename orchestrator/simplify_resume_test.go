package orchestrator_test

// The simplifier the gate asks for after a pull request fix, failing twice,
// stops the delivery for a decision (pr_fix_failed). Resuming the failed
// Run is taken back up; if it fails again a person is asked again, never
// left with a delivery parked on a Run that already failed.

import (
	"context"
	"slices"
	"testing"
)

// simplifierEscalated is a task ready to merge whose pull request fix's
// simplifier failed twice: the delivery waits on a person for
// pr_fix_failed, the last failed simplify Run kept for a resume.
func (w *world) simplifierEscalated() (wi, runID string) {
	w.t.Helper()
	wi = w.reviewing()
	w.until("ready to merge", func() bool { w.sync(); return w.taskStatus(wi) == "ready_to_merge" })
	w.crashSimplifiers(2)
	w.gh.Comment(1, "alice", "Please also rename foo to bar.")
	w.until("a fixer", func() bool { w.sync(); return w.taskStatus(wi) == "running" })
	w.until("escalated", func() bool { w.sync(); return w.taskStatus(wi) == "awaiting_input" })
	if r := w.escalationReason(wi); r != "pr_fix_failed" {
		w.t.Fatalf("escalated %q, want pr_fix_failed", r)
	}
	if err := w.owner.QueryRow(context.Background(), `SELECT id FROM runs WHERE task_id = $1 AND phase = 'simplify'
		ORDER BY created_at DESC LIMIT 1`, wi).Scan(&runID); err != nil {
		w.t.Fatal(err)
	}
	w.until("the failed simplifier to be kept", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'failed' AND lux_stop_reason = 'kept'`, runID) == 1
	})
	return wi, runID
}

// prFixFailures counts the pr_fix_failed escalations raised for wi.
func (w *world) prFixFailures(wi string) int {
	return w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'question.asked'
		AND payload->>'kind' = 'escalation' AND payload->>'reason' = 'pr_fix_failed'`, wi)
}

// Resumed, the failed simplifier fails again: a person is asked again.
// Trying again then makes a new simplify Run, which succeeds, and the task
// is ready to merge.
func TestAResumedSimplifierThatFailsAgainAfterAPullRequestFixEscalatesAgain(t *testing.T) {
	w := newWorld(t)
	wi, runID := w.simplifierEscalated()
	raised := w.prFixFailures(wi)
	simplifies := w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'simplify'`, wi)
	if status, body := w.call("/internal/tasks/"+wi+"/decide", map[string]any{"action": "resume"}); status != 200 {
		t.Fatalf("resume: %d %v", status, body)
	}
	w.until("the simplifier taken back up", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'running'`, runID) == 1
	})
	luxID := w.luxRunOf(runID)
	if !slices.Contains(w.lux.CallsOf(luxID), "resume") {
		t.Fatalf("the failed simplifier was not resumed: calls %v", w.lux.CallsOf(luxID))
	}
	w.lux.Crash(luxID)
	w.until("a new escalation", func() bool { w.sync(); return w.prFixFailures(wi) > raised })
	if s := w.taskStatus(wi); s != "awaiting_input" {
		t.Fatalf("%s after the resumed simplifier failed, want awaiting_input", s)
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'simplify'`, wi); n != simplifies {
		t.Errorf("%d simplify Runs after the resume, want the %d before it", n, simplifies)
	}

	if status, body := w.call("/internal/tasks/"+wi+"/decide", map[string]any{"action": "retry"}); status != 200 {
		t.Fatalf("retry: %d %v", status, body)
	}
	w.until("ready to merge again", func() bool { w.sync(); w.pump(); return w.taskStatus(wi) == "ready_to_merge" })
	if n := w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'simplify' AND status = 'completed'`, wi); n != 2 {
		t.Errorf("%d completed simplify Runs, want the first and the retry's", n)
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'simplify'`, wi); n != simplifies+1 {
		t.Errorf("%d simplify Runs, want one new for the retry", n)
	}
}
