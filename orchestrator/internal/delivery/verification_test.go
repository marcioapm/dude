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

// wantMarked is dude's section as a body carries it, between its markers.
func wantMarked(section, nl string) string {
	return strings.ReplaceAll("<!-- dude:verification -->\n"+section+"\n<!-- /dude:verification -->", "\n", nl)
}

// Only the section between dude's markers, outside any fence, is dude's:
// everything else in the body is a person's and comes back byte for byte.
func TestTheBodyRefreshTouchesOnlyDudesMarkedSection(t *testing.T) {
	section := "## Verification\nTested at `def5678`.\n\n- **pass** — it greets"
	old := wantMarked("## Verification\nTested at `abc1234`.\n\n- **fail** — it greets", "\n")
	fencedExample := "Example:\n\n```md\n<!-- dude:verification -->\n## Verification\nexample text\n<!-- /dude:verification -->\n```\n"
	tildeExample := "~~~~\n<!-- dude:verification -->\n~~~\n<!-- /dude:verification -->\n~~~~\n"
	cases := []struct{ name, body, want string }{
		{"a legacy unmarked section stays, dude's is added",
			"Intro.\n\n## Verification\nOld verdict.\n\n## Notes\nA note.\n",
			"Intro.\n\n## Verification\nOld verdict.\n\n## Notes\nA note.\n\n" + wantMarked(section, "\n")},
		{"a person's own section beside dude's",
			"## Verification\nMy own manual check.\n\n" + old + "\n\n## Notes\nA note.\n",
			"## Verification\nMy own manual check.\n\n" + wantMarked(section, "\n") + "\n\n## Notes\nA note.\n"},
		{"a fenced example holding the heading and both markers",
			fencedExample,
			fencedExample + "\n" + wantMarked(section, "\n")},
		{"a fenced example before dude's section",
			fencedExample + "\n" + old + "\n\nAfter.\n",
			fencedExample + "\n" + wantMarked(section, "\n") + "\n\nAfter.\n"},
		{"a tilde fence a shorter run does not close",
			tildeExample + "\n" + old,
			tildeExample + "\n" + wantMarked(section, "\n")},
		{"a CRLF body",
			"Intro.\r\n\r\n" + wantMarked("## Verification\r\nOld.", "\r\n") + "\r\n\r\n## Notes\r\nA note.\r\n",
			"Intro.\r\n\r\n" + wantMarked(section, "\r\n") + "\r\n\r\n## Notes\r\nA note.\r\n"},
		{"a CRLF fence with the markers in it",
			"```\r\n<!-- dude:verification -->\r\n<!-- /dude:verification -->\r\n```\r\n",
			"```\r\n<!-- dude:verification -->\r\n<!-- /dude:verification -->\r\n```\r\n\r\n" + wantMarked(section, "\r\n")},
		{"a start marker left without its end is a person's text",
			"<!-- dude:verification -->\nHalf.\n\n" + old,
			"<!-- dude:verification -->\nHalf.\n\n" + wantMarked(section, "\n")},
		{"a lone start marker with no pair anywhere",
			"<!-- dude:verification -->\nHalf.",
			"<!-- dude:verification -->\nHalf.\n\n" + wantMarked(section, "\n")},
		{"an unclosed backtick fence",
			"```md\nExample.",
			"```md\nExample.\n```\n\n" + wantMarked(section, "\n")},
		{"an unclosed tilde fence",
			"~~~~\nExample.\n",
			"~~~~\nExample.\n~~~~\n\n" + wantMarked(section, "\n")},
		{"an empty body", "", wantMarked(section, "\n")},
	}
	for _, c := range cases {
		got := replaceVerification(c.body, section)
		if got != c.want {
			t.Errorf("%s:\n got %q\nwant %q", c.name, got, c.want)
		}
		if again := replaceVerification(got, section); again != got {
			t.Errorf("%s: a second refresh changed the body:\n%q", c.name, again)
		}
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
