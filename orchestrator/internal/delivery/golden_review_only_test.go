package delivery

import (
	"slices"
	"strings"
	"testing"
)

// reviewOnlyConductorRefusals are the refusals only a review-only task's
// conductor meets (PR 5b).
var reviewOnlyConductorRefusals = []string{"dismiss-grounds", "dismiss-wrong-evidence", "dismiss-duplicate-of"}

func reviewOnlyConductorRefusal(key string) bool {
	name, ok := strings.CutPrefix(key, "refusals/delivery/review-only/")
	return ok && slices.Contains(reviewOnlyConductorRefusals, name)
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
