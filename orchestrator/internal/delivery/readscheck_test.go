package delivery

import (
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/forge"
)

// Which failing checks a step reads from GitHub: a review or test step
// (no fix Run) never re-downloads a log it already has an excerpt of; a
// fix step re-reads it for the tail its workspace file needs, but only for
// the first CheckLogFiles checks, and never one whose log was unreadable.
func TestWhichFailingChecksAStepReads(t *testing.T) {
	excerpt := forge.FailingCheck{RunID: 7, Log: "AssertionError"}
	unread := forge.FailingCheck{RunID: 7, Log: "AssertionError", Unread: "the log has expired"}
	for _, tc := range []struct {
		name  string
		c     forge.FailingCheck
		tails bool
		files int
		want  bool
	}{
		{"never read before, review step", forge.FailingCheck{RunID: 7}, false, 0, true},
		{"never read before, fix step", forge.FailingCheck{RunID: 7}, true, 0, true},
		{"unreadable before, retried", forge.FailingCheck{RunID: 7, Unread: "GitHub was unavailable"}, false, 0, true},
		{"excerpt, review step: not re-downloaded", excerpt, false, 0, false},
		{"excerpt, fix step: re-read for its tail", excerpt, true, 0, true},
		{"excerpt, fix step, files full", excerpt, true, CheckLogFiles, false},
		{"excerpt whose log was unreadable, fix step", unread, true, 0, false},
		{"tail already in hand", forge.FailingCheck{RunID: 7, Log: "x", Full: "tail"}, true, 0, false},
		{"no check run id", forge.FailingCheck{Log: ""}, true, 0, false},
	} {
		if got := readsCheck(tc.c, tc.tails, tc.files); got != tc.want {
			t.Errorf("%s: readsCheck = %v, want %v", tc.name, got, tc.want)
		}
	}
}
