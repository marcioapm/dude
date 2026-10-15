package orchestrator_test

import (
	"strings"
	"testing"
)

// A project that says nothing of testing runs the tester.
func TestTheDefaultPolicyRunsTheTester(t *testing.T) {
	w := newWorld(t)
	mustExec(t, w.owner, `UPDATE projects SET delivery_policy = '{"requiredReviewers":["correctness"]}' WHERE id = $1`, w.project)
	wi := w.task()
	if status, body := w.call("/internal/tasks/"+wi+"/deliver", map[string]any{}); status != 201 {
		t.Fatalf("deliver: %d %v", status, body)
	}
	w.until("a pull request", func() bool { return len(w.gh.Pulls()) == 1 })
	if n := w.phaseRuns(wi, "test"); n != 1 {
		t.Errorf("%d tests, want 1", n)
	}
	if !strings.Contains(w.gh.Pull(1).Body, "## Verification") {
		t.Errorf("no verification in the pull request:\n%s", w.gh.Pull(1).Body)
	}
}
