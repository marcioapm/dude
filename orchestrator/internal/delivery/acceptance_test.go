package delivery

import (
	"fmt"
	"slices"
	"strings"
	"testing"
)

// A tester's reply as a model writes it: prose, a fenced finding, the
// criteria in one fence, a verdict document, and an unindented wrap.
const testerReply = "I ran the app with `npm start` and walked each criterion.\n\n" +
	"```yaml\n" +
	"severity: blocking\n" +
	"category: acceptance\n" +
	"title: Totals do not update after removing an item\n" +
	"description: Reproduced: removing the second row leaves the total at 42.\n" +
	"---\n" +
	"verdicts:\n" +
	"  F1: fixed\n" +
	"```\n\n" +
	"```yaml\n" +
	"criterion: 1\n" +
	"status: pass\n" +
	"evidence: walkthrough.webm, 01-cart.png\n" +
	"observed: The cart lists both items\n" +
	"with their prices.\n" +
	"---\n" +
	"criterion: 2\n" +
	"status: fail\n" +
	"evidence: [walkthrough.webm at 0:32, 03-totals.png]\n" +
	"observed: The total stays at 42.\n" +
	"---\n" +
	"criterion: 3\n" +
	"status: Blocked\n" +
	"observed: Payments need a sandbox key.\n" +
	"---\n" +
	"criterion: \"4\"\n" +
	"status: N/A\n" +
	"observed: There is no mobile layout in this app.\n" +
	"---\n" +
	"criterion: 5\n" +
	"status: mostly works\n" +
	"evidence:\n" +
	"  - 05-a.png\n" +
	"  - 05-b.png\n" +
	"---\n" +
	"criterion: 6\n" +
	"observed: Forgot the status.\n" +
	"```\n"

func TestParseAcceptanceReadsEachCriterionOfARealReply(t *testing.T) {
	got := ParseAcceptance(testerReply)
	if len(got) != 6 {
		t.Fatalf("got %d criteria, want 6: %+v", len(got), got)
	}
	want := []struct {
		n        int
		status   string
		evidence []string
	}{
		{1, "pass", []string{"walkthrough.webm", "01-cart.png"}},
		{2, "fail", []string{"walkthrough.webm", "03-totals.png"}},
		{3, "blocked", nil},
		{4, "not-applicable", nil},
		{5, "blocked", []string{"05-a.png", "05-b.png"}},
		{6, "blocked", nil},
	}
	for i, w := range want {
		a := got[i]
		if a.Criterion != w.n || a.Status != w.status || !slices.Equal(a.Evidence, w.evidence) {
			t.Errorf("criterion %d = %d %s %v, want %s %v", i+1, a.Criterion, a.Status, a.Evidence, w.status, w.evidence)
		}
	}
	if got[0].Observed != "The cart lists both items with their prices." {
		t.Errorf("an unindented wrap was lost: %q", got[0].Observed)
	}
	if got[4].Reported != "mostly works" || got[2].Reported != "" {
		t.Errorf("reported = %q / %q: an unknown status is kept to say why it was blocked, a known one is not",
			got[4].Reported, got[2].Reported)
	}
	if got[5].Reported != "" {
		t.Errorf("a missing status is blocked with nothing to quote, got %q", got[5].Reported)
	}
}

// The findings parser reads its documents and none of the criteria.
func TestParseFindingsIgnoresCriterionDocuments(t *testing.T) {
	findings := ParseFindings(testerReply)
	if len(findings) != 1 || findings[0].Title != "Totals do not update after removing an item" {
		t.Errorf("findings = %+v, want only the tester's one finding", findings)
	}
	if v := ParseVerdicts(testerReply); len(v) != 1 || !v[0] {
		t.Errorf("verdicts = %v, want F1 fixed", v)
	}
}

func TestParseAcceptanceIgnoresDocumentsWithoutACriterion(t *testing.T) {
	for _, reply := range []string{"", "All good.", "```yaml\ncriterion: zero\nstatus: pass\n```",
		"criterion: 0\nstatus: pass", "criterion: -1\nstatus: pass"} {
		if got := ParseAcceptance(reply); len(got) != 0 {
			t.Errorf("%q read as %+v", reply, got)
		}
	}
	got := ParseAcceptance("criterion: 1\nstatus: fail\n---\ncriterion: 1\nstatus: pass\nevidence: a.png")
	if len(got) != 1 || got[0].Status != "pass" {
		t.Errorf("a criterion reported twice = %+v, want the later report", got)
	}
}

func TestJudgeAcceptanceNeedsPublishedEvidenceAndEveryCriterion(t *testing.T) {
	criteria := []string{"lists items", "totals", "pays", "mobile", "exports", "imports"}
	reported := ParseAcceptance(testerReply)
	published := map[string]bool{"walkthrough.webm": true, "01-cart.png": true}
	got := JudgeAcceptance(criteria, reported, published, nil)
	statuses := make([]string, len(got))
	for i, v := range got {
		statuses[i] = v.Status
	}
	if want := []string{"pass", "fail", "blocked", "not-applicable", "blocked", "blocked"}; !slices.Equal(statuses, want) {
		t.Errorf("statuses = %v, want %v", statuses, want)
	}
	if got[0].Text != "lists items" {
		t.Errorf("text = %q", got[0].Text)
	}

	// The same pass, citing a file the Run never published.
	got = JudgeAcceptance(criteria[:1], reported[:1], map[string]bool{"walkthrough.webm": true}, nil)
	if got[0].Status != VerdictUnverified || got[0].Problem != "01-cart.png is not a file this test published." {
		t.Errorf("unpublished evidence = %+v, want unverified", got[0])
	}
	// A pass that names nothing.
	got = JudgeAcceptance(criteria[:1], []Acceptance{{Criterion: 1, Status: "pass"}}, published, nil)
	if got[0].Status != VerdictUnverified {
		t.Errorf("a pass with no evidence = %+v", got[0])
	}
	// Not applicable needs its reason.
	got = JudgeAcceptance(criteria[:1], []Acceptance{{Criterion: 1, Status: "not-applicable"}}, published, nil)
	if got[0].Status != VerdictBlocked {
		t.Errorf("not-applicable with no reason = %+v", got[0])
	}
	// Silence is blocked, for each criterion it leaves out.
	got = JudgeAcceptance(criteria[:2], nil, published, nil)
	if len(got) != 2 || got[0].Status != VerdictBlocked || got[1].Status != VerdictBlocked {
		t.Errorf("no verdict = %+v, want both blocked", got)
	}
}

func TestJudgeAcceptanceWithoutCriteriaTakesTheDerivedChecks(t *testing.T) {
	got := JudgeAcceptance(nil, nil, nil, nil)
	if len(got) != 1 || got[0].Status != VerdictBlocked {
		t.Errorf("no criteria and no verdict = %+v, want one blocked check", got)
	}
	got = JudgeAcceptance(nil, ParseAcceptance("criterion: 1\ncheck: The page says hello\nstatus: pass\nevidence: hello.png"),
		map[string]bool{"hello.png": true}, nil)
	if len(got) != 1 || got[0].Text != "The page says hello" || got[0].Status != VerdictPass {
		t.Errorf("a derived check = %+v", got)
	}
}

// carryFromPrev is a round that resumed run_prev, its heads descending
// from that round's checkouts, which recorded verdicts.
func carryFromPrev(verdicts ...CriterionVerdict) *Carry {
	c := &Carry{RunID: "run_prev", Descends: true, Verdicts: map[int]CriterionVerdict{}}
	for _, v := range verdicts {
		c.Verdicts[v.Criterion] = v
	}
	return c
}

// carryOfOne is a tester's carry of criterion 1 from run_prev, with its reason.
const carryOfOne = "criterion: 1\nstatus: carried\nfrom_round: run_prev\nobserved: Only CI changed.\n"

// A carried criterion is what dude says it is: the previous round's pass,
// with its evidence and where it came from, only when the reply names that
// round, gives a reason, and the round passed it; blocked otherwise. A
// fresh pass still needs a file this Run published.
func TestACarriedCriterionKeepsOnlyThePreviousRoundsPass(t *testing.T) {
	criteria := []string{"it greets", "it says goodbye", "it waves"}
	prev := carryFromPrev(
		CriterionVerdict{Criterion: 1, Text: "it greets", Status: VerdictPass, Evidence: []string{"greet.webm"}},
		CriterionVerdict{Criterion: 2, Text: "it says goodbye", Status: VerdictFail, Evidence: []string{"bye.png"}},
		CriterionVerdict{Criterion: 3, Text: "it waves", Status: VerdictBlocked, Decision: "waived"})
	reply := func(n int, from, observed string) string {
		return fmt.Sprintf("criterion: %d\nstatus: carried\nfrom_round: %s\nobserved: %s\n---\n", n, from, observed)
	}
	why := "The change touches only the CI workflow."
	noReason := "Carried without a reason why the change cannot affect it."
	cases := []struct {
		name, reply string
		carry       *Carry
		criterion   int
		want        CriterionVerdict
	}{
		// No `observed` line at all, and one that is only blanks: an empty
		// `observed:` would be read as a block scalar and swallow the next field.
		{"no reason", "criterion: 1\nstatus: carried\nfrom_round: run_prev\n", prev, 1,
			CriterionVerdict{Status: VerdictBlocked, Problem: noReason}},
		{"a blank reason", "criterion: 1\nstatus: carried\nobserved: \"   \"\nfrom_round: run_prev\n", prev, 1,
			CriterionVerdict{Status: VerdictBlocked, Problem: noReason}},
		{"a pass", reply(1, "run_prev", why), prev, 1,
			CriterionVerdict{Status: VerdictPass, Evidence: []string{"greet.webm"}, CarriedFrom: "run_prev"}},
		{"a waiver", reply(3, "run_prev", why), prev, 3,
			CriterionVerdict{Status: VerdictBlocked, Decision: "waived", Evidence: []string{}, CarriedFrom: "run_prev"}},
		{"another run", reply(1, "run_older", why), prev, 1, CriterionVerdict{Status: VerdictBlocked}},
		{"a failure", reply(2, "run_prev", why), prev, 2, CriterionVerdict{Status: VerdictBlocked}},
		{"a fresh round", reply(1, "run_prev", why), nil, 1, CriterionVerdict{Status: VerdictBlocked}},
		{"history that does not descend", reply(1, "run_prev", why),
			&Carry{RunID: "run_prev", Verdicts: prev.Verdicts}, 1, CriterionVerdict{Status: VerdictBlocked}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := JudgeAcceptance(criteria, ParseAcceptance(c.reply), nil, c.carry)[c.criterion-1]
			if got.Status != c.want.Status || got.CarriedFrom != c.want.CarriedFrom || got.Decision != c.want.Decision {
				t.Fatalf("verdict = %+v, want %+v", got, c.want)
			}
			if c.want.Status == VerdictBlocked && c.want.Decision == "" && got.Problem == "" {
				t.Error("a refused carry does not say why")
			}
			if c.want.Problem != "" && got.Problem != c.want.Problem {
				t.Errorf("problem = %q, want %q", got.Problem, c.want.Problem)
			}
			if c.want.Evidence != nil && !slices.Equal(got.Evidence, c.want.Evidence) {
				t.Errorf("evidence = %v, want %v", got.Evidence, c.want.Evidence)
			}
			if c.name == "a pass" && got.Observed != why {
				t.Errorf("observed = %q, want the tester's reason", got.Observed)
			}
		})
	}
	// A fresh pass beside a carried one is still checked against this Run's files.
	got := JudgeAcceptance(criteria[:2], ParseAcceptance(reply(1, "run_prev", why)+
		"criterion: 2\nstatus: pass\nevidence: bye.png\nobserved: It says goodbye."), map[string]bool{}, prev)
	if got[1].Status != VerdictUnverified || got[1].CarriedFrom != "" {
		t.Errorf("a fresh pass citing a file this Run did not publish = %+v, want unverified", got[1])
	}
}

// A carried verdict is on the text the previous round judged: a criterion
// edited since, or a derived check that now reads differently, is blocked
// and must be tested again.
func TestACarriedCriterionWhoseTextChangedIsBlocked(t *testing.T) {
	prev := carryFromPrev(CriterionVerdict{Criterion: 1, Text: "it greets", Status: VerdictPass, Evidence: []string{"greet.webm"}})
	for _, c := range []struct {
		name     string
		criteria []string
		reply    string
	}{
		{"an edited criterion", []string{"it greets by name"}, carryOfOne},
		{"a derived check that changed", nil, "check: The page says goodbye\n" + carryOfOne},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := JudgeAcceptance(c.criteria, ParseAcceptance(c.reply), nil, prev)
			if len(got) != 1 || got[0].Status != VerdictBlocked || got[0].CarriedFrom != "" ||
				!strings.Contains(got[0].Problem, "criterion 1 changed since run_prev") {
				t.Fatalf("verdict = %+v, want blocked: criterion 1 changed since run_prev", got)
			}
		})
	}
}

// A task without criteria has the tester's derived checks, named by their
// `check` line: a carry that repeats it keeps the pass, and one that omits
// it is a check dude cannot match to the one judged, so it is blocked.
func TestACarriedDerivedCheckNeedsItsCheckLine(t *testing.T) {
	prev := carryFromPrev(CriterionVerdict{Criterion: 1, Text: "The page loads", Status: VerdictPass, Evidence: []string{"load.png"}})
	got := JudgeAcceptance(nil, ParseAcceptance("check: The page loads\n"+carryOfOne), nil, prev)
	if len(got) != 1 || got[0].Status != VerdictPass || got[0].CarriedFrom != "run_prev" || got[0].Text != "The page loads" {
		t.Errorf("a derived check carried with its check line = %+v, want the pass from run_prev", got)
	}
	got = JudgeAcceptance(nil, ParseAcceptance(carryOfOne), nil, prev)
	if len(got) != 1 || got[0].Status != VerdictBlocked || got[0].CarriedFrom != "" ||
		got[0].Problem != "Carried, but criterion 1 changed since run_prev judged it: it must be tested again." {
		t.Errorf("a derived check carried without its check line = %+v, want blocked as changed", got)
	}
}

// A not-applicable verdict carries as a pass does, with the reason it was
// not applicable, not the tester's reason for carrying it.
func TestACarriedNotApplicableKeepsItsReason(t *testing.T) {
	prev := carryFromPrev(CriterionVerdict{Criterion: 1, Text: "it works offline", Status: VerdictNotApplicable, Evidence: []string{},
		Observed: "The app has no offline mode in this build."})
	got := JudgeAcceptance([]string{"it works offline"}, ParseAcceptance(carryOfOne), nil, prev)[0]
	if got.Status != VerdictNotApplicable || got.CarriedFrom != "run_prev" || got.Observed != prev.Verdicts[1].Observed {
		t.Errorf("carried not-applicable = %+v, want not-applicable from run_prev with its reason", got)
	}
}
