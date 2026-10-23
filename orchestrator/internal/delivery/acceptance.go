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
	"cmp"
	"fmt"
	"strconv"
	"strings"
)

// Verdict statuses. A tester may report the first four and carried;
// unverified is dude's, for a pass whose evidence is not there. Carried
// is never recorded: dude records the verdict it carries, or blocked.
const (
	VerdictPass          = "pass"
	VerdictFail          = "fail"
	VerdictBlocked       = "blocked"
	VerdictNotApplicable = "not-applicable"
	VerdictUnverified    = "unverified"
	VerdictCarried       = "carried"
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
	// A carried criterion's: the test Run whose verdict it keeps.
	FromRound string
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
		a := Acceptance{Criterion: n, Check: fields["check"], Status: acceptanceStatus(fields["status"]),
			Evidence: evidenceNames(fields["evidence"]), Observed: fields["observed"],
			FromRound: strings.Trim(strings.TrimSpace(fields["from_round"]), "`\"'")}
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
	case "carried":
		return VerdictCarried
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
	// The earlier test Run whose pass this verdict carries forward.
	CarriedFrom string `json:"carriedFrom,omitempty"`
}

// Carry is what a carried criterion may rest on: the test round the Run
// resumed and its recorded verdicts, by criterion. Descends says that
// round is this attempt's previous one and this round's heads descend from
// its checkouts (State.CarryFrom). A nil Carry is a round that resumed
// none: it tests every criterion.
type Carry struct {
	RunID    string
	Descends bool
	Verdicts map[int]CriterionVerdict
}

// JudgeAcceptance is the verdict on each criterion: the tester's report
// checked against what the Run published (by name), and a carried one
// against carry. With criteria, every one has a verdict, blocked when
// unreported; without, each check the tester derived does, and none at
// all is one blocked check of the goal.
func JudgeAcceptance(criteria []string, reported []Acceptance, published map[string]bool, carry *Carry) []CriterionVerdict {
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
			out = append(out, checked(i+1, text, a, published, carry))
		}
		return out
	}
	for _, a := range reported {
		text := strings.TrimSpace(a.Check)
		if text == "" {
			text = fmt.Sprintf("Check %d", a.Criterion)
		}
		out = append(out, checked(a.Criterion, text, a, published, carry))
	}
	if len(out) == 0 {
		out = append(out, CriterionVerdict{Criterion: 1, Text: "The task's goal", Status: VerdictBlocked,
			Problem: "The tester reported no verdict."})
	}
	return out
}

func checked(n int, text string, a Acceptance, published map[string]bool, carry *Carry) CriterionVerdict {
	v := CriterionVerdict{Criterion: n, Text: text, Status: a.Status, Evidence: a.Evidence, Observed: a.Observed}
	if v.Evidence == nil {
		v.Evidence = []string{}
	}
	switch {
	case a.Reported != "":
		v.Problem = fmt.Sprintf("The tester's status %q is not one dude reads; taken as blocked.", a.Reported)
	case a.Status == VerdictCarried:
		return carried(v, a, carry)
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

// carried is a criterion the tester kept from its previous round: that
// round's verdict on it, with its evidence and any person's decision, when
// the round may carry (carry), names that round, and gives a reason, and
// the verdict it keeps is a pass, a waiver or an acceptance, of the
// criterion as it reads now. Anything else
// is blocked: dude, not the tester, says what a carried verdict is.
func carried(v CriterionVerdict, a Acceptance, carry *Carry) CriterionVerdict {
	v.Status = VerdictBlocked
	prev, ok := CriterionVerdict{}, false
	if carry != nil {
		prev, ok = carry.Verdicts[v.Criterion]
	}
	switch {
	case carry == nil:
		v.Problem = "Carried, but this round tests every criterion afresh: nothing carries into it."
	case a.FromRound != carry.RunID:
		v.Problem = fmt.Sprintf("Carried from %q, which is not this tester's previous round %s.", a.FromRound, carry.RunID)
	case !carry.Descends:
		v.Problem = fmt.Sprintf("Carried, but this round's commits do not descend from those %s tested: nothing it judged carries.", carry.RunID)
	case strings.TrimSpace(a.Observed) == "":
		v.Problem = "Carried without a reason why the change cannot affect it."
	case !ok || prev.Status != VerdictPass && prev.Decision == "":
		v.Problem = fmt.Sprintf("Carried, but %s did not pass it: it must be tested again.", carry.RunID)
	case prev.Text != v.Text:
		// Criteria edited while the task was stopped, or a derived check
		// renumbered: the verdict was on another statement.
		v.Problem = fmt.Sprintf("Carried, but criterion %d changed since %s judged it: it must be tested again.", v.Criterion, carry.RunID)
	default:
		v.Status, v.Evidence, v.Decision = prev.Status, prev.Evidence, prev.Decision
		// The Run whose files the evidence names, through any earlier carry.
		v.CarriedFrom = cmp.Or(prev.CarriedFrom, carry.RunID)
	}
	return v
}
