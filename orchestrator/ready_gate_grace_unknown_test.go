package orchestrator_test

// GitHub working mergeability out while the CI grace is still running:
// the gate holds on two items, neither of which any sync sees move once
// it clears (the syncer keeps its last mergeable reading; the grace ends
// on a timer). The workflow reads the gate again on its own.

import (
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/fakegithub"
)

// filteredWorkflow is a pull_request trigger this head is left out of: no
// checks ever report, so only the grace ending lets the gate through.
const filteredWorkflow = "on:\n  pull_request:\n    branches: [develop]\njobs: {}\n"

// timersElapse has any timer the delivery set (a Wait, a ReweighAt) due
// now, the head's CI grace left as it is.
func (w *world) timersElapse(wi string) {
	w.t.Helper()
	mustExec(w.t, w.owner, `UPDATE workflow_runs SET wake_at = CASE WHEN wake_at IS NOT NULL THEN now() END,
		wait_until = CASE WHEN wait_until IS NOT NULL THEN now() END WHERE task_id = $1`, wi)
}

// unknownThenClean has GitHub's mergeability come back clean, the first
// timer the gate armed for it elapse with the grace still running, and
// then the grace end: the task reaches ready to merge with no sync seeing
// anything move.
func (w *world) unknownThenClean(wi string) {
	w.t.Helper()
	w.gh.Set(func(s *fakegithub.Server) { delete(s.MergeableUnknown, 1) })
	w.timersElapse(wi)
	for range 3 {
		w.pump()
	}
	if s := w.taskStatus(wi); s != "review" {
		w.t.Fatalf("%s with the CI grace still running, want review", s)
	}
	w.graceEnds(wi)
	w.until("ready to merge", func() bool { w.sync(); return w.taskStatus(wi) == "ready_to_merge" })
}

// An approval lands within the grace while GitHub has not worked out the
// pull request's mergeability.
func TestAnApprovalWhileMergeabilityIsUnknownWithinTheGraceIsReady(t *testing.T) {
	w := newWorld(t)
	w.gh.SetChecks("none")
	wi := w.prOpen(w.task(), prAt{seed: map[string]string{".github/workflows/ci.yml": filteredWorkflow}})
	w.withinGrace(wi)
	w.gh.Set(func(s *fakegithub.Server) { s.MergeableUnknown[1] = true })
	w.gh.Review(1, "alice", "APPROVED")
	w.withinGrace(wi)
	w.unknownThenClean(wi)
}

// A fix pushed for a person's comment, its new head in the grace, while
// GitHub has not worked out the pull request's mergeability.
func TestAFixPushedWhileMergeabilityIsUnknownWithinTheGraceIsReady(t *testing.T) {
	w := newWorld(t)
	w.gh.SetChecks("none")
	wi := w.prOpen(w.task(), prAt{seed: map[string]string{".github/workflows/ci.yml": filteredWorkflow}})
	w.withinGrace(wi)
	w.gh.Set(func(s *fakegithub.Server) { s.MergeableUnknown[1] = true })
	w.gh.Comment(1, "alice", "Please also rename foo to bar.")
	w.until("a fixer", func() bool { w.sync(); return w.taskStatus(wi) == "running" })
	// The fix, its review, the simplifier past it and that one's review.
	w.until("the fix back in review", func() bool {
		w.sync()
		w.pump()
		return w.taskStatus(wi) == "review" &&
			w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'simplify'`, wi) == 2 &&
			w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND status NOT IN ('completed', 'failed', 'aborted')`, wi) == 0
	})
	w.withinGrace(wi)
	w.unknownThenClean(wi)
}

// The control: mergeability known throughout, the approval within the
// grace is ready once the grace ends.
func TestAnApprovalWithMergeabilityKnownWithinTheGraceIsReady(t *testing.T) {
	w := newWorld(t)
	w.gh.SetChecks("none")
	wi := w.prOpen(w.task(), prAt{seed: map[string]string{".github/workflows/ci.yml": filteredWorkflow}})
	w.withinGrace(wi)
	w.gh.Review(1, "alice", "APPROVED")
	w.withinGrace(wi)
	w.graceEnds(wi)
	w.until("ready to merge", func() bool { w.sync(); return w.taskStatus(wi) == "ready_to_merge" })
}
