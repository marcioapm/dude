package orchestrator_test

import (
	"strings"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/fakegithub"
)

// "Can't tell" is never silence (#465): a pass that fails to settle what
// it read (here GitHub answers a run attempt no int4 holds, so storing it
// fails, as the run id once did) counts as a failed read, and
// DispatchCannotSeeReads of them wake the conductor "cannot see" with the
// error, once.
func TestASupervisorPassThatFailsToSettleWakesCannotSee(t *testing.T) {
	const runID = 43000003100
	w, task, _ := dispatchWorld(t)
	w.gh.Set(func(s *fakegithub.Server) { s.DispatchRunID = runID })
	id := w.dispatched(task, `{"workflow":"ci.yml"}`)
	w.gh.UpdateWorkflowRun(runID, func(r *fakegithub.WorkflowRun) { r.Status = "in_progress" })
	w.gh.Set(func(s *fakegithub.Server) {
		run := s.WorkflowRuns[runID]
		run.Attempt = 3_000_000_000
		s.WorkflowRuns[runID] = run
	})
	for range delivery.DispatchCannotSeeReads - 1 {
		w.supervise()
	}
	if got := w.str(`SELECT state || ' ' || failed_reads || ' ' || (failing_since IS NOT NULL)::text || ' ' || COALESCE(last_error LIKE '%int4%', false)::text
		FROM workflow_dispatches WHERE id = $1`, id); got != "bound 2 true true" {
		t.Fatalf("after failing passes: %s", got)
	}
	if n := len(w.workflowWakes(task)); n != 0 {
		t.Fatalf("%d wakes before the bound", n)
	}
	w.supervise()
	wakes := w.workflowWakes(task)
	if len(wakes) != 1 || !strings.Contains(wakes[0], "dude cannot see this workflow run") || !strings.Contains(wakes[0], "int4") {
		t.Fatalf("wakes %q", wakes)
	}
	w.supervise()
	if n := len(w.workflowWakes(task)); n != 1 {
		t.Fatalf("%d cannot-see wakes for one stretch", n)
	}
}
