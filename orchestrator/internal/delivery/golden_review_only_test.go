package delivery

import "testing"

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
