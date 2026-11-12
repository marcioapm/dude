package orchestrator_test

import (
	"strings"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/fakegithub"
)

// The run id a live trial was given by GitHub, and could never follow
// (#465): past int4, which a parameter Postgres inferred from a literal 0
// was. Dispatched, then completed on GitHub, it wakes the conductor with
// its outcome exactly once.
func TestADispatchedRunWithARealRunIDIsFollowedToItsOutcome(t *testing.T) {
	const runID = 43314330828
	w, task, _ := dispatchWorld(t)
	w.gh.Set(func(s *fakegithub.Server) { s.DispatchRunID = runID })
	id := w.dispatched(task, `{"workflow":"ci.yml"}`)
	w.supervise()
	if got := w.str(`SELECT state || ' ' || COALESCE(run_id::text, 'none') || ' ' || COALESCE(run_status, 'none') || ' ' || failed_reads
		FROM workflow_dispatches WHERE id = $1`, id); got != "bound 43314330828 queued 0" {
		t.Fatalf("after a pass: %s", got)
	}
	w.gh.UpdateWorkflowRun(runID, func(r *fakegithub.WorkflowRun) { r.Status, r.Conclusion = "completed", "success" })
	w.supervise()
	w.supervise()
	wakes := w.workflowWakes(task)
	if len(wakes) != 1 || !strings.Contains(wakes[0], "completed: success, attempt 1 (https://github.com/acme/target/actions/runs/43314330828)") {
		t.Fatalf("wakes %q", wakes)
	}
	if got := w.str(`SELECT state || ' ' || outcome || ' ' || woken_outcome FROM workflow_dispatches WHERE id = $1`, id); got != "completed completed completed" {
		t.Fatalf("settled: %s", got)
	}
}
