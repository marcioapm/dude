package delivery

import (
	"slices"
	"strings"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/forge"
)

// reviewOnlyConductorRefusals are the refusals only a review-only task's
// conductor meets (PR 5b).
var reviewOnlyConductorRefusals = []string{"dismiss-grounds", "dismiss-wrong-evidence", "dismiss-duplicate-of", "no-own-comment"}

func reviewOnlyConductorRefusal(key string) bool {
	name, ok := strings.CutPrefix(key, "refusals/delivery/review-only/")
	return ok && slices.Contains(reviewOnlyConductorRefusals, name)
}

// A review-only task's reviewer is told what holds the author's merge
// (contracts/severity under ReviewOnly): medium or above, rated by what
// happens if it merges, a low never; and introduced: no only for lines the
// pull request did not write. The writing reviewer's file
// (instructions-reviewer-builtin) carries none of it.
func TestGoldenReviewOnlyReviewerInstructions(t *testing.T) {
	in := goldenTask()
	in.Tools, in.CLI, in.BlockingSeverities, in.ReviewOnly = true, true, fixBarSeverities, true
	in.Repositories = readOnlyRepos()
	got := Instructions("reviewer", "correctness", in.InstructionsInput())
	for _, want := range []string{"Only a finding at `medium` or above that this pull request introduced holds the author's merge",
		"`introduced: no` only for a problem in lines this pull request did not write"} {
		if !strings.Contains(got, want) {
			t.Errorf("the review-only reviewer's file lacks %q", want)
		}
	}
	in.ReviewOnly = false
	if w := Instructions("reviewer", "correctness", in.InstructionsInput()); strings.Contains(w, "holds the author's merge") ||
		!strings.Contains(w, "plus `low` ones marked `trivial: yes`") {
		t.Error("the writing reviewer's file has the review-only severity guidance")
	}
	golden(t, "instructions-reviewer-review-only", got)
}

// The review-only conductor's refusals, in their own golden.
func TestGoldenReviewOnlyRefusals(t *testing.T) {
	var lines [][2]string
	for _, l := range refusalLines("refusals/delivery/review-only/", "refusals-review-only") {
		if reviewOnlyConductorRefusal(l[0]) {
			lines = append(lines, l)
		}
	}
	if len(lines) != len(reviewOnlyConductorRefusals) {
		t.Fatalf("%d of the review-only conductor's refusals defined, want %d", len(lines), len(reviewOnlyConductorRefusals))
	}
	goldenLines(t, "refusals-review-only", lines)
}

// A review-only task's conductor is briefed with its contract
// (contracts/review-only) after who decides: what its role's fixing,
// publishing and opening do not apply to. The writing-mode briefings
// (TestGoldenConductorBriefings) carry none of it.
func TestGoldenReviewOnlyConductorBriefing(t *testing.T) {
	w := newGoldenWorld(t)
	w.exec(t, `INSERT INTO task_sources (task_id, organization_id, repository_id, kind, number, head_branch, base_branch, mode)
		VALUES ('wi_g', $1, 'repo_api', 'pr', 12, 'feature/export', 'main', 'review_only')`)
	w.delivery(t, "conductorDecision", `{"decider":"conductor","decision":{"point":"after_review","policy":"reviewExit","reviewOnly":true}}`)
	golden(t, "briefing-conductor-review-only", w.in(t, briefWith(byWake, "", "dude woke you; one thing:\n- A decision waits.")))
}

// The wake of the author's reply in dude's thread on a finding (review
// only): a mention's text, then the finding and dude's comment. With no
// finding it is a mention's, byte for byte (delivery-lines' mention).
func TestGoldenReviewOnlyThreadReply(t *testing.T) {
	m := Mention{Repo: "api", Number: 12, Feedback: forge.Feedback{ID: "line-comment-9", Author: "alice",
		URL: "https://x/9", Path: "api/x.go", InReplyTo: 3}}
	f := &verdictFinding{ID: "rf_1", Severity: "medium", Title: "Nil is not checked", Description: "greet(nil) panics.",
		Fix: "Check for nil."}
	if m.toldAbout("x", nil) != m.told("x") {
		t.Fatal("a reply with no finding is told differently from a mention")
	}
	goldenTexts(t, "wake-review-only-thread-reply", [][2]string{
		{"reply on a finding", m.toldAbout("This is intentional: the caller already checks for nil.", f)}})
}
