package delivery

import (
	"strings"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/forge"
)

// The review-only bar is the severity as filed: medium or above blocks; a
// low never does, trivial or not (the fix bar, atFixBar, would promote a
// trivial low to medium: dude's own to fix in a writing delivery).
func TestTheVerdictBlocksOnMediumOrAboveAsFiled(t *testing.T) {
	no, yes := false, true
	for _, c := range []struct {
		f    verdictFinding
		want bool
	}{
		{verdictFinding{Severity: "low", Trivial: true}, false},
		{verdictFinding{Severity: "low", Trivial: true, Introduced: &yes}, false},
		{verdictFinding{Severity: "low"}, false},
		{verdictFinding{Severity: "note"}, false},
		{verdictFinding{Severity: "medium"}, true},
		{verdictFinding{Severity: "high", Introduced: &yes}, true},
		{verdictFinding{Severity: "blocking"}, true},
		{verdictFinding{Severity: "high", Introduced: &no}, false},
	} {
		if got := c.f.blocks(); got != c.want {
			t.Errorf("%+v blocks = %v, want %v", c.f, got, c.want)
		}
	}
	item := verdictFindingsItem([]verdictFinding{
		{ID: "f1", Severity: "low", Trivial: true, Title: "Comment says as before", Checked: true},
		{ID: "f2", Severity: "medium", Title: "Unbounded query", Checked: true},
		{ID: "f3", Severity: "high", Title: "Not re-checked", Checked: false},
	})
	if item.OK || !strings.Contains(item.Detail, "1 open finding(s)") || !strings.Contains(item.Detail, "f2 (medium) Unbounded query") ||
		strings.Contains(item.Detail, "f1") || strings.Contains(item.Detail, "f3") {
		t.Fatalf("the findings item: %+v", item)
	}
	if it := verdictFindingsItem([]verdictFinding{{ID: "f1", Severity: "low", Trivial: true, Checked: true}}); !it.OK {
		t.Fatalf("a trivial low holds the verdict: %+v", it)
	}
	if it := verdictFindingsItem([]verdictFinding{{ID: "f1", Severity: "high", Title: "Unchecked", Checked: false}}); !it.OK {
		t.Fatalf("an unchecked finding holds the verdict: %+v", it)
	}
}

// A finding filed on the base at a line inside a hunk the pull request
// changes is the pull request's; outside every hunk, in a file it does not
// change, with no line, or one GitHub sent no patch for, it stays as
// filed. A writing delivery's findings are never read so.
func TestAPullRequestsOwnLinesAreNeverOnTheBase(t *testing.T) {
	diff := map[string]forge.DiffLines{
		"a_test.go": {Known: true, Hunks: []forge.Hunk{{Left: 10, LeftLines: 3, Right: 10, RightLines: 8}}},
		"big.go":    {},
	}
	no := func() *bool { b := false; return &b }
	fs := []verdictFinding{
		{ID: "in", File: "a_test.go", Line: 12, Severity: "medium", Introduced: no()},
		{ID: "out", File: "a_test.go", Line: 40, Severity: "medium", Introduced: no()},
		{ID: "other", File: "b.go", Line: 12, Severity: "medium", Introduced: no()},
		{ID: "noline", File: "a_test.go", Severity: "medium", Introduced: no()},
		{ID: "unknown", File: "big.go", Line: 1, Severity: "medium", Introduced: no()},
	}
	if !onBaseCandidates(fs) {
		t.Fatal("no candidate seen")
	}
	writing := append([]verdictFinding(nil), fs...)
	writing[0].Introduced = no()
	ownLines(writing, diff, false)
	if *writing[0].Introduced {
		t.Fatal("a writing delivery's finding was read against the diff")
	}
	ownLines(fs, diff, true)
	for _, f := range fs {
		if want := f.ID == "in"; *f.Introduced != want {
			t.Errorf("%s introduced = %v, want %v", f.ID, *f.Introduced, want)
		}
	}
}

// Non-blocking findings commented on earlier are suggestions, never
// "Still open": that heading lists what holds the verdict.
func TestStillOpenListsOnlyBlockingFindings(t *testing.T) {
	yes := true
	body, _, _ := verdictReviewOf(verdictDecision{Headline: "Changes requested"}, "a8f3e1c0ffee", "main", nil,
		[]verdictFinding{
			{Title: "Unbounded query", Severity: "medium", File: "a.go", Line: 3, Introduced: &yes, earlier: &inlinePost{Head: "1111111aaaa", URL: "u1"}},
			{Title: "Dup helper", Severity: "low", Trivial: true, File: "a.go", Line: 5, earlier: &inlinePost{Head: "1111111aaaa", URL: "u2"}},
		}, "")
	open, sugg, ok := strings.Cut(body, "**Suggestions (not blocking)**")
	if !ok || !strings.Contains(open, "**Still open from 1111111**\n- Unbounded query (medium)") || strings.Contains(open, "Dup helper") ||
		!strings.Contains(sugg, "- Dup helper (low) `a.go:5`: u2") {
		t.Fatalf("body:\n%s", body)
	}
}
