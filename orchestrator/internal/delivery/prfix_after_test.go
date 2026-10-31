package delivery

import "testing"

// A fix held for the review of dude's push (reviewFirst) is never kept
// past a return to waiting on the pull request: a later fix would be
// given back the old one's direction, integration and iteration.
func TestWaitingOnThePullRequestKeepsNoHeldFix(t *testing.T) {
	st := &State{PRFixAfter: &PRFixAfter{Head: "abc", Directed: &Directed{Note: "stale"}, Iteration: 2}}
	if waitForPR(st); st.PRFixAfter != nil {
		t.Fatalf("held fix kept: %+v", st.PRFixAfter)
	}
}
