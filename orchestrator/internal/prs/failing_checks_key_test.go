package prs

import (
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/forge"
)

// The failing checks' part of a signal's key: the same checks in any
// order are the same key, a different one is a different key, and a check
// run, a commit status and a name never stand for each other.
func TestFailingChecksKey(t *testing.T) {
	ci := func(checks ...forge.FailingCheck) forge.ActionableFeedback {
		return forge.ActionableFeedback{Source: "checks", Checks: checks}
	}
	run := func(id int64) forge.FailingCheck { return forge.FailingCheck{Name: "e2e", RunID: id} }
	status := func(id int64) forge.FailingCheck { return forge.FailingCheck{Name: "ci", StatusID: id} }
	named := func(name string) forge.FailingCheck { return forge.FailingCheck{Name: name} }
	key := func(f ...forge.ActionableFeedback) string { return failingChecksKey(f) }

	if k := key(); k != "" {
		t.Errorf("no feedback: %q", k)
	}
	if k := key(forge.ActionableFeedback{Source: "review", Body: "Rename it."}, forge.ActionableFeedback{Source: forge.SourceBot}); k != "" {
		t.Errorf("feedback with no checks: %q", k)
	}
	one := key(ci(run(77)))
	if len(one) != 32 {
		t.Errorf("key %q is not a fixed-length digest", one)
	}
	if key(ci(run(10), run(9))) != key(ci(run(9), run(10))) {
		t.Error("the same checks listed in another order are another key")
	}
	if key(ci(run(9)), ci(run(10))) != key(ci(run(10), run(9))) {
		t.Error("the same checks across two feedback items are another key")
	}
	distinct := map[string]string{
		"run 77": one, "run 78": key(ci(run(78))), "status 77": key(ci(status(77))),
		"status 78": key(ci(status(78))), "named ci": key(ci(named("ci"))),
		"named 77": key(ci(named("77"))), "runs 77 and 78": key(ci(run(77), run(78))),
		"named a, run 5": key(ci(named("a"), run(5))), `named "a\nrun:5"`: key(ci(named("a\nrun:5"))),
	}
	seen := map[string]string{}
	for what, k := range distinct {
		if other, ok := seen[k]; ok {
			t.Errorf("%s and %s have the same key", what, other)
		}
		seen[k] = what
	}
	many := make([]forge.FailingCheck, 500)
	for i := range many {
		many[i] = run(40_000_000_000 + int64(i))
	}
	if k := key(ci(many...)); len(k) != 32 {
		t.Errorf("500 failing checks: a key of %d characters", len(k))
	}
}
