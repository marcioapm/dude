package delivery

import (
	"strings"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/forge"
)

// Middle-of-log evidence survives both workspace and prompt recuts.
func TestLogEndPreservesMiddleFailureEvidenceAcrossRecuts(t *testing.T) {
	noise := strings.Repeat("ordinary progress output\n", 5000)
	evidence := []string{
		"panic: test timed out after 10m0s",
		"running tests:",
		"TestDeliveryWait (9m59s)",
		"--- FAIL: TestDeliveryWait (600.00s)",
		"FAIL\texample.org/delivery\t600.001s",
		"##[error]Process completed with exit code 1.",
	}
	text := noise + strings.Join(evidence[:5], "\n") + "\n" + noise + evidence[5]
	full := LogEnd(text, CheckLogFileMax)
	for _, limit := range []int{checkLogLimit, CheckLogFileMax} {
		for _, source := range []string{text, full} {
			got := LogEnd(source, limit)
			if len(got) == 0 || len(got) > limit || got != LogEnd(source, limit) || got != LogEnd(got, limit) {
				t.Fatalf("limit %d: invalid bounds or nondeterministic recut (%d bytes)", limit, len(got))
			}
			for _, want := range append(evidence, "… [lines omitted]") {
				if !strings.Contains(got, want) {
					t.Errorf("limit %d: excerpt lacks %q", limit, want)
				}
			}
		}
	}
	marked := "…\n--- FAIL: TestDeliveryWait\n… [lines omitted]\nexit status 1"
	if got := LogEnd(marked, CheckLogFileMax); got != marked {
		t.Errorf("an already fitting excerpt lost omission boundaries: %q", got)
	}
}

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
