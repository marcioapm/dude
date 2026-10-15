package delivery

import (
	"slices"
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
	got := JudgeAcceptance(criteria, reported, published)
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
	got = JudgeAcceptance(criteria[:1], reported[:1], map[string]bool{"walkthrough.webm": true})
	if got[0].Status != VerdictUnverified || got[0].Problem != "01-cart.png is not a file this test published." {
		t.Errorf("unpublished evidence = %+v, want unverified", got[0])
	}
	// A pass that names nothing.
	got = JudgeAcceptance(criteria[:1], []Acceptance{{Criterion: 1, Status: "pass"}}, published)
	if got[0].Status != VerdictUnverified {
		t.Errorf("a pass with no evidence = %+v", got[0])
	}
	// Not applicable needs its reason.
	got = JudgeAcceptance(criteria[:1], []Acceptance{{Criterion: 1, Status: "not-applicable"}}, published)
	if got[0].Status != VerdictBlocked {
		t.Errorf("not-applicable with no reason = %+v", got[0])
	}
	// Silence is blocked, for each criterion it leaves out.
	got = JudgeAcceptance(criteria[:2], nil, published)
	if len(got) != 2 || got[0].Status != VerdictBlocked || got[1].Status != VerdictBlocked {
		t.Errorf("no verdict = %+v, want both blocked", got)
	}
}

func TestJudgeAcceptanceWithoutCriteriaTakesTheDerivedChecks(t *testing.T) {
	got := JudgeAcceptance(nil, nil, nil)
	if len(got) != 1 || got[0].Status != VerdictBlocked {
		t.Errorf("no criteria and no verdict = %+v, want one blocked check", got)
	}
	got = JudgeAcceptance(nil, ParseAcceptance("criterion: 1\ncheck: The page says hello\nstatus: pass\nevidence: hello.png"),
		map[string]bool{"hello.png": true})
	if len(got) != 1 || got[0].Text != "The page says hello" || got[0].Status != VerdictPass {
		t.Errorf("a derived check = %+v", got)
	}
}
