package delivery

import "testing"

// The simplifier runs on the final reviewed code: a reviewed fix that moved
// the work past where the simplifier last left it goes through it again —
// a pull request's fix before its round is done, a fix of the simplifier's
// own diff before the test — and not once it ran there, nor at a head a
// person accepted, nor with the policy's simplifier off.
func TestAfterReviewSimplifiesAgainOnceAFixMovedTheWork(t *testing.T) {
	on := DefaultPolicy()
	on.Simplify = true
	off := on
	off.Simplify = false
	heads := map[string]string{"api": "fixed"}
	for _, c := range []struct {
		name string
		st   State
		want string
	}{
		{"a pull request fix, simplifier never ran", State{Policy: on, Heads: heads, FixRange: map[string]string{"api": "a"}}, "simplify"},
		{"a pull request fix, simplified before it", State{Policy: on, Heads: heads, FixRange: map[string]string{"api": "a"},
			SimplifiedHeads: map[string]string{"api": "a"}}, "simplify"},
		{"a pull request fix, simplified since", State{Policy: on, Heads: heads, FixRange: map[string]string{"api": "a"},
			SimplifiedHeads: map[string]string{"api": "fixed"}}, "prFixDone"},
		{"a pull request fix, simplifier off", State{Policy: off, Heads: heads, FixRange: map[string]string{"api": "a"}}, "prFixDone"},
		{"a fix of the simplifier's diff", State{Policy: on, Heads: heads, SimplifyBase: map[string]string{"api": "s0"},
			SimplifiedHeads: map[string]string{"api": "s1"}}, "simplify"},
		{"the simplifier's diff reviewed as it was", State{Policy: on, Heads: heads, SimplifyBase: map[string]string{"api": "s0"},
			SimplifiedHeads: map[string]string{"api": "fixed"}}, "test"},
		{"before any simplifier", State{Policy: on, Heads: heads}, "simplify"},
		{"a pull request fix a person accepted", State{Policy: on, Heads: heads, FixRange: map[string]string{"api": "a"},
			AcceptedHeads: heads}, "prFixDone"},
		{"before any simplifier, a person accepted the head", State{Policy: on, Heads: heads, AcceptedHeads: heads}, "test"},
		{"a person accepted an earlier head", State{Policy: on, Heads: heads, FixRange: map[string]string{"api": "a"},
			AcceptedHeads: map[string]string{"api": "a"}}, "simplify"},
	} {
		if got := c.st.afterReview(); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}

// A person accepting a round records the head of every repository with a
// pull request, merged or closed ones too: a merged repository's head left
// out kept simplifyDue true for good.
func TestAcceptingHeadsCoversMergedAndClosedPullRequests(t *testing.T) {
	on := DefaultPolicy()
	on.Simplify = true
	st := State{Policy: on, Heads: map[string]string{"api": "a1", "web": "w1", "docs": "d1"}}
	pr := func(repo, state string) PullRequestState {
		p := PullRequestState{Repo: repo}
		p.State = state
		return p
	}
	st.acceptHeadsOf([]PullRequestState{pr("api", "open"), pr("web", "merged"), pr("docs", "closed")})
	if st.simplifyDue() {
		t.Fatalf("simplifier due after a person accepted every head: accepted %v", st.AcceptedHeads)
	}
}
