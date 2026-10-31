package delivery

import (
	"reflect"
	"testing"
)

func TestTheLastChecksReportIsRead(t *testing.T) {
	reply := "Done.\n\n```yaml\nchecks:\n  - command: make old\n    result: fail\n```\n\nThen I fixed it.\n\n" +
		"```yaml\nchecks:\n  - command: \"go test ./...\"\n    result: pass\n    evidence: ok  pkg 0.1s\n" +
		"  - command: npm run lint\n    result: not-run\n    evidence: the linter needs a network\n      it does not have\n```\n"
	got, ok := ParseChecks(reply)
	want := []Check{
		{Command: "go test ./...", Result: CheckPass, Evidence: "ok  pkg 0.1s"},
		{Command: "npm run lint", Result: CheckNotRun, Evidence: "the linter needs a network it does not have"},
	}
	if !ok || !reflect.DeepEqual(got, want) {
		t.Fatalf("ParseChecks = %#v, %v", got, ok)
	}
}

func TestAMissingOrUnreadableChecksReportIsNone(t *testing.T) {
	for name, reply := range map[string]string{
		"none":               "Implemented it.",
		"template":           "checks:\n  - command: the exact command\n    result: pass | fail | not-run\n",
		"no command":         "checks:\n  - result: pass\n",
		"not a list":         "checks:\n  command: make\n  result: pass\n",
		"empty":              "checks:\n\nThat is all.",
		"one of two invalid": "checks:\n  - command: a\n    result: pass\n  - command: b\n    result: maybe\n",
	} {
		if got, ok := ParseChecks(reply); ok {
			t.Errorf("%s: read %#v", name, got)
		}
	}
	if got, ok := ParseChecks("checks: []\n"); !ok || len(got) != 0 {
		t.Errorf("checks: [] = %#v, %v; want a report of none", got, ok)
	}
}

func TestOnlyImplementFixAndSimplifyAreGated(t *testing.T) {
	for phase, want := range map[string]bool{PhaseImplement: true, PhaseFix: true, PhaseSimplify: true,
		PhaseReview: false, PhaseTest: false, PhaseInvestigate: false} {
		if ChecksGated[phase] != want {
			t.Errorf("%s gated = %v", phase, ChecksGated[phase])
		}
	}
}

// A writer's outcome: done when it says nothing, partial or blocked with
// its note when it says so, partial for a value of its own.
func TestAWritersOutcomeIsReadFromItsChecksDocument(t *testing.T) {
	for _, c := range []struct{ reply, outcome, note string }{
		{"Done.\n```yaml\nchecks:\n  - command: go test ./...\n    result: pass\n    evidence: ok\n```\n", "done", ""},
		{"No checks at all.", "done", ""},
		{"```yaml\noutcome: partial\noutcome_note: the second endpoint is left\nchecks:\n  - command: go test\n    result: pass\n    evidence: ok\n```", "partial", "the second endpoint is left"},
		{"```yaml\nchecks:\n  - command: go test\n    result: not_run\n    evidence: no Go\noutcome: blocked\noutcome_note: needs a key\n```", "blocked", "needs a key"},
		{"```yaml\noutcome: mostly\nchecks: []\n```", "partial", ""},
		{"```yaml\noutcome: DONE\nchecks: []\n```", "done", ""},
	} {
		if o, n := WriterOutcome(c.reply); o != c.outcome || n != c.note {
			t.Errorf("%q: %q %q, want %q %q", c.reply, o, n, c.outcome, c.note)
		}
	}
}
