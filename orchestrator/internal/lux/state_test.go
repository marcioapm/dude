package lux

import "testing"

// lux before the rename ends a Run for good as "cancelled", after it as
// "terminated": both are over and neither resumes.
func TestTerminatedAndCancelledAreTheSameEnd(t *testing.T) {
	for _, c := range []struct {
		state                string
		terminal, terminated bool
	}{
		{"terminated", true, true},
		{"cancelled", true, true},
		{"succeeded", true, false},
		{"stopped", true, false},
		{"failed", true, false},
		{"lost", true, false},
		{"running", false, false},
		{"stopping", false, false},
	} {
		if got := Terminal(c.state); got != c.terminal {
			t.Errorf("Terminal(%q) = %v", c.state, got)
		}
		if got := Terminated(c.state); got != c.terminated {
			t.Errorf("Terminated(%q) = %v", c.state, got)
		}
	}
}
