package delivery

import (
	"errors"
	"strings"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/forge"
)

// gateAt is a verdict gate at head whose items hold but for those given.
func gateAt(failing ...GateItem) PRGate {
	g := PRGate{Head: "a8f3e1c0ffee"}
	for _, k := range ReadyKeys {
		it := GateItem{Key: k, OK: true, Detail: "ok"}
		for _, f := range failing {
			if f.Key == k {
				it = f
			}
		}
		g.Items = append(g.Items, it)
	}
	return g
}

// Every row of the verdict rule (design §4.4, row 5 as the owner decided
// it), first applicable row winning.
func TestDecideVerdictCoversEveryRow(t *testing.T) {
	failing := GateItem{Key: ReadyChecks, Detail: "failing: build"}
	pending := GateItem{Key: ReadyChecks, Detail: "pending: build"}
	finding := GateItem{Key: ReadyFindings, Detail: "1 open finding(s) at the fix bar introduced by the pull request: F1"}
	for _, c := range []struct {
		name          string
		in            verdictInput
		action, event string
		headline      string
		unrelated     bool
	}{
		{"0 head moved", verdictInput{Gate: gateAt(GateItem{Key: ReadyHead, Detail: "GitHub shows b"})}, verdictAbandon, "", "", false},
		{"0 closed", verdictInput{Gate: gateAt(GateItem{Key: ReadyOpen, Detail: "it is closed"})}, verdictAbandon, "", "", false},
		{"1 GitHub silent", verdictInput{Gate: PRGate{Err: errors.New("503")}}, verdictRetry, "", "", false},
		{"2 lens failed", verdictInput{Gate: gateAt(), Unfinished: []string{"security"}}, verdictPost, forge.EventComment, "Could not complete the review at a8f3e1c: security did not finish", false},
		{"2 not reviewed", verdictInput{Gate: gateAt(GateItem{Key: ReadyReviewed, Detail: VerdictNoLens})}, verdictPost, forge.EventComment, "Could not complete the review", false},
		{"3 finding", verdictInput{Gate: gateAt(finding, failing)}, verdictPost, forge.EventRequestChanges, "Changes requested at a8f3e1c", false},
		{"4 question", verdictInput{Gate: gateAt(GateItem{Key: ReadyQuestions, Detail: "1 question open"})}, verdictPost, forge.EventComment, "Waiting on an answer", false},
		{"5 failing, not looked at", verdictInput{Gate: gateAt(failing)}, verdictAskCI, "", "", false},
		{"5 failing, unrelated", verdictInput{Gate: gateAt(failing), CIAsked: true}, verdictPost, forge.EventComment, "not approved while CI fails", true},
		{"6 pending", verdictInput{Gate: gateAt(pending)}, verdictDefer, "", "", false},
		{"6 pending, capped", verdictInput{Gate: gateAt(pending), Capped: true}, verdictPost, forge.EventComment, "could not confirm CI at a8f3e1c", false},
		{"6 pending, stuck", verdictInput{Gate: gateAt(pending), Stuck: true}, verdictPost, forge.EventComment, "past the organization's patience", false},
		{"7 unreadable", verdictInput{Gate: gateAt(GateItem{Key: ReadyChecks, Detail: "checks unreadable: GitHub refused"})}, verdictPost, forge.EventComment, "could not read CI", false},
		{"8 draft", verdictInput{Gate: gateAt(GateItem{Key: ReadyOpen, Detail: VerdictDraft})}, verdictPost, forge.EventComment, "Draft", false},
		{"9 approve", verdictInput{Gate: gateAt()}, verdictPost, forge.EventApprove, "Approved at a8f3e1c", false},
		// Decision 2 in order: unrelated red CI comments, then green at H approves.
		{"5 then green", verdictInput{Gate: gateAt(), CIAsked: true}, verdictPost, forge.EventApprove, "Approved", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			d := decideVerdict(c.in)
			if d.Action != c.action || d.Event != c.event || !strings.Contains(d.Headline, c.headline) || d.Unrelated != c.unrelated {
				t.Fatalf("%+v, want %s %s %q unrelated=%v", d, c.action, c.event, c.headline, c.unrelated)
			}
			if d.Event == forge.EventApprove && len(d.Notes) > 0 {
				t.Fatalf("approved with failing items: %v", d.Notes)
			}
		})
	}
	// 3'. A failed review-only test, no finding of its open: named, never approved.
	if d := decideVerdict(verdictInput{Gate: gateAt(), Test: "the test run_t failed criteria [1]"}); d.Event != forge.EventComment ||
		d.Reason != reasonTestFailed || !strings.Contains(d.Headline, "run_t failed") {
		t.Fatalf("a failed test: %+v", d)
	}
	// Each COMMENT's reason is its own claim: the cap's COMMENT after an
	// unrelated-red one at the same head is a review of its own.
	unrelated := decideVerdict(verdictInput{Gate: gateAt(failing), CIAsked: true})
	capped := decideVerdict(verdictInput{Gate: gateAt(pending), Capped: true})
	st := &State{TaskID: "t", Attempt: 1}
	if !transientReason(unrelated.Reason) || !transientReason(capped.Reason) ||
		claimID(st, "h", unrelated.Event, unrelated.Reason) == claimID(st, "h", capped.Event, capped.Reason) {
		t.Fatalf("unrelated %q and capped %q share a claim, or are not transient", unrelated.Reason, capped.Reason)
	}
	// L-A: the cap and ci_stuck are the same claim (CI not confirmed at
	// H), each with its own headline.
	stuck := decideVerdict(verdictInput{Gate: gateAt(pending), Stuck: true})
	if claimID(st, "h", stuck.Event, stuck.Reason) != claimID(st, "h", capped.Event, capped.Reason) || stuck.Headline == capped.Headline {
		t.Fatalf("stuck %+v and capped %+v: want one claim, two headlines", stuck, capped)
	}
	// A related failure: the conductor's lens files it, so row 3 holds and
	// the failing checks are listed in the body too.
	d := decideVerdict(verdictInput{Gate: gateAt(finding, failing), CIAsked: true})
	if d.Event != forge.EventRequestChanges || !strings.Contains(strings.Join(d.Notes, "\n"), "failing: build") {
		t.Fatalf("a related failure: %+v", d)
	}
}

// The review's body: findings on the base listed, never inline; blocking
// ones inline when they name a line; non-blocking ones marked nits.
func TestVerdictReviewPlacesFindings(t *testing.T) {
	no, yes := false, true
	body, inline := verdictReview(verdictDecision{Headline: "Changes requested"}, "a8f3e1c0ffee", "main", []string{"correctness"},
		[]verdictFinding{
			{Title: "Off by one", Severity: "high", File: "a.go", Line: 3, Introduced: &yes},
			{Title: "Old typo", Severity: "medium", File: "b.go", Line: 9, Introduced: &no},
			{Title: "Naming", Severity: "low", File: "a.go", Line: 5},
			{Title: "No place", Severity: "medium"},
		}, "https://dude.test/t")
	if len(inline) != 2 || inline[0].Path != "a.go" || strings.HasPrefix(inline[0].Body, "nit") || !strings.HasPrefix(inline[1].Body, "nit: ") {
		t.Fatalf("inline %+v", inline)
	}
	for _, want := range []string{"**Already on main (not blocking)**\n- Old typo (medium) `b.go:9`", "No place", "by correctness", "https://dude.test/t"} {
		if !strings.Contains(body, want) {
			t.Errorf("body lacks %q:\n%s", want, body)
		}
	}
}
