package delivery

import (
	"slices"
	"testing"
)

// TestPersonOnly: every action on every reason, against the table the
// conductor-first design and its owner's answers settle; NeedsReason
// always within it.
func TestPersonOnly(t *testing.T) {
	personOnly := map[string][]string{
		"review_failed":          {"accept"},
		"stuck":                  {"accept"},
		"exhausted":              {"accept"},
		ReasonNotConverging:      {"accept", "amend"},
		ReasonTestFailedCriteria: {"accept"},
		ReasonTestBlocked:        {ActionWaive},
		ReasonChecksUnverified:   {ActionWaive},
		"pull_request_closed":    {"done"},
	}
	actions := []string{"resume", "retry", "accept", "amend", ActionWaive, "done", "wait", "stop"}
	reasons := []string{"implement_failed", "fix_failed", "test_failed", "pr_fix_failed", "review_failed", "no_changes",
		"stuck", "exhausted", ReasonNotConverging, ReasonTestFailedCriteria, ReasonTestBlocked, ReasonChecksUnverified,
		"pull_request_conflict", "ci_stuck", ReasonPullRequestMoved, ReasonStoppedMidPromotion, "pull_request_closed",
		"pr_loop_exhausted", "source_gone", "source_changed", ReasonChoiceUnresolved}
	for _, r := range reasons {
		for _, a := range actions {
			want := slices.Contains(personOnly[r], a)
			if got := PersonOnly(r, a); got != want {
				t.Errorf("PersonOnly(%s, %s) = %v, want %v", r, a, got, want)
			}
			if NeedsReason(r, a) && !PersonOnly(r, a) {
				t.Errorf("NeedsReason(%s, %s) but not PersonOnly", r, a)
			}
		}
	}
	for _, a := range actions {
		if !PersonOnly(ReasonConductorUnavailable, a) {
			t.Errorf("PersonOnly(conductor_unavailable, %s): no conductor holds it", a)
		}
	}
	// Retry and stop on a stalled loop are the conductor's (owner, Q4).
	for _, r := range []string{"stuck", "exhausted", ReasonNotConverging} {
		for _, a := range []string{"retry", "stop"} {
			if PersonOnly(r, a) {
				t.Errorf("PersonOnly(%s, %s): the conductor's", r, a)
			}
		}
	}
	// Write access to a repository is the owner's alone (owner, Q3).
	if !RepositoryAccessOwnerOnly("write") || RepositoryAccessOwnerOnly("read") {
		t.Error("RepositoryAccessOwnerOnly: write is the owner's, read is not")
	}
}

// TestHeldByReadsEmptyAsPerson: an escalation with no Holder is a
// person's; heldBy records its holder and time.
func TestHeldByReadsEmptyAsPerson(t *testing.T) {
	if got := (&Escalation{}).HeldBy(); got != HolderPerson {
		t.Fatalf("HeldBy of a holderless escalation = %q", got)
	}
	e := (&Escalation{Reason: "stuck"}).heldBy(HolderPerson)
	if e.Holder != HolderPerson || e.RaisedAt == nil || e.RaisedAt.IsZero() {
		t.Fatalf("heldBy: %+v", e)
	}
}
