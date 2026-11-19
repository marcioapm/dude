package orchestrator_test

import (
	"context"
	"strings"
	"testing"
)

// firstReviewAt runs a first review-only review whose lens raises finding
// at H, and checks it was a complete review: the raising Run judged the
// finding by raising it, though its finding_ids (what it was given to
// judge) do not list it. Dude's review.
func (w *world) firstReviewAt(finding string) (state, body string) {
	w.t.Helper()
	wi, _, got := w.reviewOnlyFinding(finding)
	var raised, listed bool
	if err := w.owner.QueryRow(context.Background(), `SELECT count(*) > 0,
		COALESCE(bool_or(f.id = ANY(COALESCE(r.finding_ids, '{}'))), false)
		FROM review_findings f JOIN runs r ON r.id = f.run_id WHERE f.task_id = $1 AND r.phase = 'review'`, wi).
		Scan(&raised, &listed); err != nil {
		w.t.Fatal(err)
	}
	if !raised || listed {
		w.t.Fatalf("raised=%v, raising run lists its own finding=%v", raised, listed)
	}
	for _, not := range []string{"Could not complete the review", "Not re-checked at ", "authoritatively judged"} {
		if strings.Contains(got.Body, not) {
			w.t.Fatalf("a complete first review reads as incomplete (%q), %s:\n%s", not, got.State, got.Body)
		}
	}
	return got.State, got.Body
}

// A first review at H raising a new medium the pull request wrote requests
// changes: its own raising Run checked it, so the review is complete.
func TestAReviewOnlyFirstReviewRaisingAMediumRequestsChanges(t *testing.T) {
	w := newWorld(t)
	state, body := w.firstReviewAt(barFinding("severity: medium\nintroduced: yes", "long.md", 20, "The query is unbounded"))
	if state != "CHANGES_REQUESTED" || !strings.Contains(body, "The query is unbounded") {
		t.Fatalf("dude's review %s, want CHANGES_REQUESTED:\n%s", state, body)
	}
}

// A first review at H raising only a low approves: complete, and a low
// never holds the merge.
func TestAReviewOnlyFirstReviewRaisingOnlyALowApproves(t *testing.T) {
	w := newWorld(t)
	state, body := w.firstReviewAt(barFinding("severity: low\nintroduced: yes", "long.md", 21, "The test does not assert the checkbox clears"))
	if state != "APPROVED" {
		t.Fatalf("dude's review %s, want APPROVED:\n%s", state, body)
	}
}
