package delivery

import (
	"strings"
	"testing"
)

func TestTheVerificationSectionSaysWhatWasTestedAndWhenItIsStale(t *testing.T) {
	v := &prVerification{Heads: map[string]string{"api": "0123456789abcdef"}, Criteria: []prCriterion{
		{Text: "it greets", Status: "pass"},
		{Text: "it pays", Status: "blocked", Decision: "waived", DecidedBy: "Cy", Note: "Checked\non staging."},
	}}
	got := verificationSection(v, map[string]string{"api": "0123456789abcdef"}, "https://dude.example/#/o/acme/task/t1")
	for _, want := range []string{"## Verification\nTested at `0123456`.\n", "- **pass** — it greets",
		"- **blocked, waived by Cy: Checked on staging.** — it pays", "on the task in dude: https://dude.example/#/o/acme/task/t1"} {
		if !strings.Contains(got, want) {
			t.Errorf("no %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "Stale") {
		t.Errorf("a verdict at the current heads is not stale:\n%s", got)
	}
	// The simplifier, or a pull request fix, moved the branch since.
	for _, heads := range []map[string]string{{"api": "fedcba9876543210"}, {"api": "0123456789abcdef", "web": "aaaaaaa"}} {
		if got := verificationSection(v, heads, ""); !strings.Contains(got, "**Stale:**") {
			t.Errorf("heads %v moved past the test, yet:\n%s", heads, got)
		}
	}
	if got := verificationSection(nil, nil, ""); got != "" {
		t.Errorf("no test, yet %q", got)
	}
}

func TestTheGateFailsBeforeItBlocksAndHonoursAPersonsDecision(t *testing.T) {
	p := DefaultPolicy()
	cases := []struct {
		name string
		in   gateInput
		want string
	}{
		{"all pass", gateInput{Verdicts: []StoredVerdict{{Criterion: 1, Status: "pass"}, {Criterion: 2, Status: "not-applicable"}}}, "pass"},
		{"a failure and a blocked check", gateInput{Verdicts: []StoredVerdict{{Criterion: 1, Status: "fail"}, {Criterion: 2, Status: "blocked"}}}, "fail"},
		{"unverified", gateInput{Verdicts: []StoredVerdict{{Criterion: 1, Status: "unverified"}}}, "blocked"},
		{"waived", gateInput{Verdicts: []StoredVerdict{{Criterion: 1, Status: "blocked", Decision: "waived"}}}, "pass"},
		{"accepted", gateInput{Verdicts: []StoredVerdict{{Criterion: 1, Status: "fail", Decision: "accepted"}}}, "pass"},
		{"a blocking finding on passing criteria", gateInput{Verdicts: []StoredVerdict{{Criterion: 1, Status: "pass"}},
			Findings: []TestFinding{{ID: "f", Severity: "high", Title: "Console error on load"}}}, "fail"},
		{"a note is not a failure", gateInput{Verdicts: []StoredVerdict{{Criterion: 1, Status: "pass"}},
			Findings: []TestFinding{{ID: "f", Severity: "low", Title: "Slow"}}}, "pass"},
		{"the environment", gateInput{Verdicts: []StoredVerdict{{Criterion: 1, Status: "pass"}},
			Findings: []TestFinding{{ID: "f", Severity: "blocking", Title: "Test environment: no database"}}}, "blocked"},
		{"an earlier test's finding, this one unable to run", gateInput{RunID: "t2",
			Verdicts: []StoredVerdict{{Criterion: 1, Status: "blocked"}, {Criterion: 2, Status: "pass"}},
			Findings: []TestFinding{{ID: "f1", Severity: "blocking", Title: "Criterion 1 is not met", RunID: "t1"},
				{ID: "f2", Severity: "blocking", Title: "Test environment: no database", RunID: "t2"}}}, "blocked"},
		{"an earlier test's finding, this one ran", gateInput{RunID: "t2",
			Verdicts: []StoredVerdict{{Criterion: 1, Status: "pass"}},
			Findings: []TestFinding{{ID: "f1", Severity: "blocking", Title: "Criterion 1 is not met", RunID: "t1"}}}, "fail"},
		{"an earlier test's finding, this one judged nothing", gateInput{RunID: "t2",
			Verdicts: []StoredVerdict{{Criterion: 1, Status: "blocked"}, {Criterion: 2, Status: "blocked"}},
			Findings: []TestFinding{{ID: "f1", Severity: "blocking", Title: "Criterion 1 is not met", RunID: "t1"}}}, "blocked"},
	}
	for _, c := range cases {
		if got := judgeGate(p, c.in).Outcome; got != c.want {
			t.Errorf("%s: %s, want %s", c.name, got, c.want)
		}
	}
}
