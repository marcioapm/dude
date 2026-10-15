// Reading the tester's verdict on each acceptance criterion, and checking
// it against what the test Run published.
//
// The tester ends its reply with one flat YAML document per criterion
// (testerContract), in the same style as findings. What it says is a
// report, not a result: a pass is only a pass when the evidence it names
// is a file the same Run published, and a criterion it says nothing about
// is blocked. Nothing unchecked reads as a pass.

package delivery

import (
	"fmt"
	"strconv"
	"strings"
)

// Verdict statuses. A tester may report the first four; unverified is
// dude's, for a pass whose evidence is not there.
const (
	VerdictPass          = "pass"
	VerdictFail          = "fail"
	VerdictBlocked       = "blocked"
	VerdictNotApplicable = "not-applicable"
	VerdictUnverified    = "unverified"
)

// Acceptance is one criterion as the tester reported it.
type Acceptance struct {
	// 1-based, into the task's acceptance criteria.
	Criterion int
	// The derived check's text, when the task has no criteria.
	Check string
	// pass, fail, blocked or not-applicable; anything else reads blocked.
	Status string
	// What it said, when Status is not that.
	Reported string
	Evidence []string
	Observed string
}

/*
ParseAcceptance reads every criterion document in a tester's reply:

	criterion: 2
	status: pass
	evidence: walkthrough.webm, 03-totals.png
	observed: The totals row shows 42.

A document is one when its `criterion` is a positive number; findings,
verdicts and prose around them are left alone. A criterion reported twice
keeps the later report.
*/
func ParseAcceptance(output string) []Acceptance {
	byCriterion := map[int]int{}
	var out []Acceptance
	// A fence ends a document as `---` does: a model often fences each
	// criterion on its own.
	for _, doc := range strings.Split(fenceLine.ReplaceAllString(output, "\n---"), "\n---") {
		if isVerdicts(doc) {
			continue
		}
		fields := readFields(doc)
		n, err := strconv.Atoi(strings.Trim(fields["criterion"], `"' `))
		if err != nil || n <= 0 {
			continue
		}
		a := Acceptance{Criterion: n, Check: fields["check"], Evidence: evidenceNames(fields["evidence"]),
			Observed: fields["observed"]}
		a.Status = acceptanceStatus(fields["status"])
		if a.Status == VerdictBlocked && !strings.EqualFold(strings.TrimSpace(fields["status"]), VerdictBlocked) {
			a.Reported = fields["status"]
		}
		if i, seen := byCriterion[n]; seen {
			out[i] = a
			continue
		}
		byCriterion[n] = len(out)
		out = append(out, a)
	}
	return out
}

// acceptanceStatus reads a reported status; one it does not know is
// blocked, never a pass.
func acceptanceStatus(s string) string {
	switch strings.ToLower(strings.Trim(strings.TrimSpace(s), "`\"'")) {
	case "pass", "passed":
		return VerdictPass
	case "fail", "failed":
		return VerdictFail
	case "not-applicable", "not applicable", "not_applicable", "n/a", "na":
		return VerdictNotApplicable
	}
	return VerdictBlocked
}

// evidenceNames reads an evidence field — a comma list, a `[a, b]` flow
// list, or a block list that reads as "- a - b" — as the published names
// it gives. A word with no dot ("at", "0:32") is not a file name.
func evidenceNames(s string) []string {
	var out []string
	for _, part := range strings.Split(strings.Trim(strings.TrimSpace(s), "[]"), ",") {
		for _, word := range strings.Fields(part) {
			word = strings.Trim(word, "`\"'()[];,")
			word = strings.TrimSuffix(word, ".")
			if word == "" || word == "-" || !strings.Contains(word, ".") || strings.Contains(word, "://") {
				continue
			}
			out = append(out, word)
		}
	}
	return out
}

// CriterionVerdict is a criterion's verdict as dude records it.
type CriterionVerdict struct {
	Criterion int      `json:"criterion"`
	Text      string   `json:"text"`
	Status    string   `json:"status"`
	Evidence  []string `json:"evidence"`
	Observed  string   `json:"observed"`
	// Why dude's status is not what the tester said.
	Problem string `json:"problem,omitempty"`
	// A person's decision on it: waived or accepted.
	Decision string `json:"decision,omitempty"`
}

// JudgeAcceptance is the verdict on each criterion: the tester's report
// checked against what the Run published (by name). With criteria, every
// one has a verdict, blocked when unreported; without, each check the
// tester derived does, and none at all is one blocked check of the goal.
func JudgeAcceptance(criteria []string, reported []Acceptance, published map[string]bool) []CriterionVerdict {
	byN := map[int]Acceptance{}
	for _, a := range reported {
		byN[a.Criterion] = a
	}
	var out []CriterionVerdict
	if len(criteria) > 0 {
		for i, text := range criteria {
			a, ok := byN[i+1]
			if !ok {
				out = append(out, CriterionVerdict{Criterion: i + 1, Text: text, Status: VerdictBlocked,
					Problem: "The tester reported nothing on this criterion."})
				continue
			}
			out = append(out, checked(i+1, text, a, published))
		}
		return out
	}
	for _, a := range reported {
		text := strings.TrimSpace(a.Check)
		if text == "" {
			text = fmt.Sprintf("Check %d", a.Criterion)
		}
		out = append(out, checked(a.Criterion, text, a, published))
	}
	if len(out) == 0 {
		out = append(out, CriterionVerdict{Criterion: 1, Text: "The task's goal", Status: VerdictBlocked,
			Problem: "The tester reported no verdict."})
	}
	return out
}

func checked(n int, text string, a Acceptance, published map[string]bool) CriterionVerdict {
	v := CriterionVerdict{Criterion: n, Text: text, Status: a.Status, Evidence: a.Evidence, Observed: a.Observed}
	if v.Evidence == nil {
		v.Evidence = []string{}
	}
	switch {
	case a.Reported != "":
		v.Problem = fmt.Sprintf("The tester's status %q is not one dude reads; taken as blocked.", a.Reported)
	case a.Status == VerdictPass && len(a.Evidence) == 0:
		v.Status, v.Problem = VerdictUnverified, "A pass that names no evidence."
	case a.Status == VerdictPass:
		for _, name := range a.Evidence {
			if !published[name] {
				v.Status, v.Problem = VerdictUnverified, fmt.Sprintf("%s is not a file this test published.", name)
				break
			}
		}
	case a.Status == VerdictNotApplicable && strings.TrimSpace(a.Observed) == "":
		v.Status, v.Problem = VerdictBlocked, "Not applicable, without a reason."
	}
	return v
}
