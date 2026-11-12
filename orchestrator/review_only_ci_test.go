package orchestrator_test

import (
	"strings"
	"testing"
	"time"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/forge"
)

// verdictCap has the world's deliveries cap a deferred verdict at cap.
func (w *world) verdictCap(cap time.Duration) {
	store := &delivery.Store{DB: w.app, SyncPullRequest: w.prs.Sync, VerdictDeferCap: cap}
	w.runtime.Register(delivery.Workflow(store, forge.Resolver{DB: w.app}))
}

// C-M1: CI pending at H (deferred), then failing (the conductor judges it
// unrelated: COMMENT), then rerun and pending past the cap (the cap is the
// rerun's, not measured from before the failure: a deferral first, then a
// COMMENT of its own), then green at H: exactly one APPROVE at H follows
// the COMMENTs, and the status comment names only reviews posted.
func TestAnUnrelatedRedThenASlowRerunStillApproves(t *testing.T) {
	w := newWorld(t)
	const cap = 3 * time.Second
	w.verdictCap(cap)
	w.gh.SetChecks("pending")
	wi, n := w.reviewOnly()
	head := w.gh.SHA("feature/greet")
	w.agentsDone(wi, "deferred while pending", func() bool { return w.events(wi, delivery.EvVerdictDeferred) > 0 && w.waitingOnPR(wi) })
	time.Sleep(cap) // the first deferral is older than the cap by the rerun

	w.gh.SetChecks("failure")
	w.agentsDone(wi, "commented on unrelated red CI", func() bool { return len(w.dudeReviews(wi, n)) == 1 && w.waitingOnPR(wi) })
	if got := w.dudeReviews(wi, n); got[0].State != "COMMENTED" || !strings.Contains(got[0].Body, "not approved while CI fails") {
		t.Fatalf("with red CI: %+v", got)
	}
	deferred := w.events(wi, delivery.EvVerdictDeferred)

	w.gh.SetChecks("pending") // the rerun
	w.agentsDone(wi, "the rerun deferred, not capped at once", func() bool {
		return w.events(wi, delivery.EvVerdictDeferred) > deferred || len(w.dudeReviews(wi, n)) > 1
	})
	if got := w.dudeReviews(wi, n); len(got) != 1 {
		t.Fatalf("the rerun was capped from the first deferral, before the failure: %+v", got)
	}
	w.agentsDone(wi, "commented at the rerun's cap", func() bool { return len(w.dudeReviews(wi, n)) == 2 && w.waitingOnPR(wi) })
	if got := w.dudeReviews(wi, n); got[1].State != "COMMENTED" || !strings.Contains(got[1].Body, "could not confirm CI") {
		t.Fatalf("at the cap: %+v", got)
	}

	w.gh.SetChecks("success")
	w.agentsDone(wi, "approved once green at H", func() bool { return len(w.dudeReviews(wi, n)) == 3 })
	got := w.dudeReviews(wi, n)
	if got[2].State != "APPROVED" || got[2].CommitID != head {
		t.Fatalf("after CI passed at H: %+v", got)
	}
	w.sync()
	w.advanced()
	if got := w.dudeReviews(wi, n); len(got) != 3 {
		t.Fatalf("%d reviews, want exactly one APPROVE after the COMMENTs", len(got))
	}
	if c := w.verdictComments(wi, n); len(c) != 1 || !strings.Contains(c[0].Body, "Approved at") {
		t.Fatalf("status comments %+v", c)
	}
	if c := w.writers(wi); c != 0 {
		t.Fatalf("%d writers", c)
	}
}
