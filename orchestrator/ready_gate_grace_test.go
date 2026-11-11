package orchestrator_test

// The CI grace ending is no move the syncer sees: a pull request whose
// workflows may not run on it (a filtered pull_request trigger), or one an
// external status reported on before Actions, reads the same to every sync
// once the grace is over. The workflow reads the gate again when it ends.

import (
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/fakegithub"
)

// graceEnds has the head's CI grace over, as an hour passing would: the
// head first seen an hour ago, and any timer the delivery set elapsed.
func (w *world) graceEnds(wi string) {
	w.t.Helper()
	mustExec(w.t, w.owner, `UPDATE pull_requests SET head_seen_at = now() - interval '1 hour' WHERE task_id = $1`, wi)
	mustExec(w.t, w.owner, `UPDATE workflow_runs SET wake_at = CASE WHEN wake_at IS NOT NULL THEN now() END,
		wait_until = CASE WHEN wait_until IS NOT NULL THEN now() END WHERE task_id = $1`, wi)
}

// withinGrace syncs and pumps a few times, the head new: the task is not
// ready to merge, its checks pending within the grace.
func (w *world) withinGrace(wi string) {
	w.t.Helper()
	for range 5 {
		w.sync()
		w.pump()
	}
	if s := w.taskStatus(wi); s != "review" {
		w.t.Fatalf("%s within the CI grace, want review", s)
	}
}

// A workflow whose pull_request trigger has a filter may leave this head
// out: dude cannot tell, and nothing reports. Past the grace that is no CI
// for this head, and the task is ready to merge with no further sync
// moving anything.
func TestAFilteredWorkflowWithNoChecksIsReadyAfterTheGrace(t *testing.T) {
	for name, workflow := range map[string]string{
		"branches": "on:\n  pull_request:\n    branches: [develop]\njobs: {}\n",
		"paths":    "on:\n  pull_request:\n    paths: ['docs/**']\njobs: {}\n",
	} {
		t.Run(name, func(t *testing.T) {
			w := newWorld(t)
			w.gh.SetChecks("none")
			wi := w.prOpen(w.task(), prAt{seed: map[string]string{".github/workflows/ci.yml": workflow}})
			w.withinGrace(wi)
			w.graceEnds(wi)
			w.until("ready to merge", func() bool { w.sync(); return w.taskStatus(wi) == "ready_to_merge" })
		})
	}
}

// Workflows that run on every pull request: an external status reports
// first, which holds the gate until Actions does. Actions then completes
// green before any sync saw it in progress, the rollup passing throughout:
// the task is ready to merge.
func TestActionsReportingAfterAnExternalStatusIsReady(t *testing.T) {
	w := newWorld(t)
	w.gh.Set(func(s *fakegithub.Server) { s.StatusID = 9 })
	w.gh.SetChecks("success")
	wi := w.prOpen(w.task(), prAt{seed: map[string]string{".github/workflows/ci.yml": "on: pull_request\njobs: {}\n"}})
	w.withinGrace(wi)
	w.gh.SetChecks("run:success")
	for range 3 {
		w.sync()
		w.pump()
	}
	w.graceEnds(wi)
	w.until("ready to merge", func() bool { w.sync(); return w.taskStatus(wi) == "ready_to_merge" })
}
