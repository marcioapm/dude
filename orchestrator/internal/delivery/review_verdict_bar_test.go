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
		"a_test.go": {Known: true, Hunks: []forge.Hunk{{Left: 10, LeftLines: 3, Right: 10, RightLines: 8}}, Added: map[int]struct{}{12: {}}},
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
			{Checked: true, Title: "Unbounded query", Severity: "medium", File: "a.go", Line: 3, Introduced: &yes, earlier: &inlinePost{Head: "1111111aaaa", URL: "u1"}},
			{Checked: true, Title: "Dup helper", Severity: "low", Trivial: true, File: "a.go", Line: 5, earlier: &inlinePost{Head: "1111111aaaa", URL: "u2"}},
		}, "")
	open, sugg, ok := strings.Cut(body, "**Suggestions (not blocking)**")
	if !ok || !strings.Contains(open, "**Still open from 1111111**\n- Unbounded query (medium)") || strings.Contains(open, "Dup helper") ||
		!strings.Contains(sugg, "- Dup helper (low) `a.go:5`: u2") {
		t.Fatalf("body:\n%s", body)
	}
}

// keptAs is a finding's resolution note after the conductor's reason.
func keptAs(reason string) string { return DismissedByConductor + reason }

// A finding the conductor kept as a suggestion holds nothing, whatever
// its severity as filed; it is listed under the suggestions with the gate
// it fails and why, never as withdrawn; first raised at this head, it is
// commented on inline, marked as a suggestion. Another open introduced
// medium still holds the verdict, and alone is named.
func TestAFindingKeptAsASuggestionHoldsNothing(t *testing.T) {
	yes := true
	kept := verdictFinding{ID: "f1", Checked: true, Title: "Local mode leaks", Severity: "high", File: "a.go", Line: 3, Introduced: &yes,
		Judgement: "still", Kept: keptAs("suggestion: gate 2: only DEV_MODE=1 reaches it, cmd/dev.go:12.")}
	open := verdictFinding{ID: "f2", Checked: true, Title: "Unbounded query", Severity: "medium", File: "b.go", Line: 9, Introduced: &yes, Judgement: "still"}
	if kept.blocks() || kept.uncheckedBlocks() {
		t.Fatal("a finding kept as a suggestion blocks")
	}
	if it := verdictFindingsItem([]verdictFinding{kept}); !it.OK {
		t.Fatalf("a finding kept as a suggestion holds the verdict: %+v", it)
	}
	it := verdictFindingsItem([]verdictFinding{kept, open})
	if it.OK || !strings.Contains(it.Detail, "1 open finding(s)") || !strings.Contains(it.Detail, "f2 (medium) Unbounded query") || strings.Contains(it.Detail, "f1") {
		t.Fatalf("next to an open medium: %+v", it)
	}
	body, inline, ids := verdictReviewOf(verdictDecision{Headline: "Approved at a8f3e1c"}, "a8f3e1c0ffee", "main", nil, []verdictFinding{kept}, "")
	_, sugg, ok := strings.Cut(body, "**Suggestions (not blocking)**")
	if !ok || !strings.Contains(sugg, "- Local mode leaks (high) `a.go:3`: kept as a suggestion (shipped path): only DEV_MODE=1 reaches it, cmd/dev.go:12") ||
		strings.Contains(body, "Withdrawn") || strings.Contains(body, "wrong") {
		t.Fatalf("body:\n%s", body)
	}
	if len(inline) != 1 || len(ids) != 1 || ids[0] != "f1" || !strings.HasPrefix(inline[0].Body, "Suggestion, not blocking: **Local mode leaks** (high)") ||
		!strings.Contains(inline[0].Body, "Kept as a suggestion (shipped path): only DEV_MODE=1 reaches it, cmd/dev.go:12.") {
		t.Fatalf("inline: %+v %v", inline, ids)
	}
	// Commented on earlier: listed with its link, not commented on again.
	kept.earlier = &inlinePost{Head: "1111111aaaa", URL: "u1"}
	body, inline, _ = verdictReviewOf(verdictDecision{Headline: "Approved"}, "a8f3e1c0ffee", "main", nil, []verdictFinding{kept}, "")
	if len(inline) != 0 || strings.Contains(body, "Still open") || !strings.Contains(body, "(shipped path): only DEV_MODE=1 reaches it, cmd/dev.go:12 u1") {
		t.Fatalf("earlier: %+v\n%s", inline, body)
	}
}

// "suggestion: gate <N>: <why>": N one of 2-7, why saying something.
// Anything else is no suggestion; a wrong or duplicate note is never one.
func TestSuggestionGrounds(t *testing.T) {
	for reason, want := range map[string]string{
		"suggestion: gate 2: only DEV_MODE reaches it":     "shipped path",
		"Suggestion: Gate 7: the scenario left at abc1234": "current",
		"suggestion:gate 4:  the test claims nothing here": "test quality",
		"suggestion: gate 6: rare, noticed, cheap to fix.": "proportionate",
		"suggestion: no gate given here at all":            "",
		"suggestion: gate 1: it is introduced, medium":     "",
		"suggestion: gate 8: it is a gate past the rubric": "",
		"suggestion: gate x: a gate that is no number":     "",
		"suggestion: gate 22: a gate of two digits here":   "",
		"suggestion: gate 3:":                              "",
		"suggestion: gate 3: ----------------":             "",
		"suggestion: gate 3 the colon is missing here":     "",
		"wrong: suggestion: gate 2: nested in a reason":    "",
		"duplicate: f-2": "",
	} {
		gate, _, ok := keptSuggestion(keptAs(reason))
		if gate != want || ok != (want != "") {
			t.Errorf("keptSuggestion(%q) = %q %v, want %q", reason, gate, ok, want)
		}
	}
	if _, _, ok := keptSuggestion("suggestion: gate 2: a person's note, not the conductor's"); ok {
		t.Error("a note not the conductor's read as a suggestion")
	}
	if got := threadReplyText("accepted", keptAs("suggestion: gate 5: the description says it is intended."), "abc1234"); got !=
		"Kept as a suggestion (declared behaviour): the description says it is intended." {
		t.Errorf("reply %q", got)
	}
	for note, want := range map[string]string{
		keptAs("wrong: the caller checks it."): "Withdrawn: the caller checks it.",
		keptAs("duplicate: f-2"):               "Withdrawn: duplicate of f-2.",
	} {
		if got := threadReplyText("accepted", note, "abc1234"); got != want {
			t.Errorf("reply %q, want %q", got, want)
		}
	}
}
