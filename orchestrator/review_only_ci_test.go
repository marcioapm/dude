package orchestrator_test

import (
	"strings"
	"testing"
	"time"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/fakegithub"
	"github.com/marciomartins/dude/orchestrator/internal/forge"
)

// verdictCap has the world's deliveries cap a deferred verdict at cap.
func (w *world) verdictCap(cap time.Duration) {
	store := &delivery.Store{DB: w.app, SyncPullRequest: w.prs.Sync, VerdictDeferCap: cap}
	w.runtime.Register(delivery.Workflow(store, forge.Resolver{DB: w.app}))
}

// L-A: the cap's COMMENT, then ci_stuck for the same pending CI at H, is
// the same claim ("CI not confirmed at H"): exactly one COMMENT at H, and
// the verdict is still owed there, so green CI then approves.
func TestTheCapThenCIStuckPostsOneComment(t *testing.T) {
	w := newWorld(t)
	w.verdictCap(time.Nanosecond)
	w.gh.SetChecks("pending")
	wi, n := w.reviewOnly()
	head := w.gh.SHA("feature/greet")
	w.agentsDone(wi, "commented at the cap", func() bool { return len(w.dudeReviews(wi, n)) == 1 && w.waitingOnPR(wi) })
	if got := w.dudeReviews(wi, n); got[0].State != "COMMENTED" || !strings.Contains(got[0].Body, "could not confirm CI") {
		t.Fatalf("at the cap: %+v", got)
	}
	// The sync's patience runs out on the same pending CI: ci_stuck.
	mustExec(t, w.owner, `UPDATE pull_requests SET checks_pending_since = now() - interval '61 minutes' WHERE task_id = $1`, wi)
	stuckSignalled := func() bool {
		return w.count(`SELECT count(*) FROM workflow_signals WHERE workflow_run_id = (SELECT id FROM workflow_runs WHERE task_id = $1)
			AND idempotency_key LIKE '%:stuck:%' AND consumed_at IS NOT NULL`, wi) > 0
	}
	w.agentsDone(wi, "ci_stuck consumed by the wait", func() bool { return stuckSignalled() && w.waitingOnPR(wi) })
	w.advanced()
	var atHead []fakegithub.Review
	for _, r := range w.dudeReviews(wi, n) {
		if r.CommitID == head {
			atHead = append(atHead, r)
		}
	}
	if len(atHead) != 1 {
		t.Fatalf("%d COMMENTs at the same head for the same pending CI, want 1: %+v", len(atHead), atHead)
	}
	// Still owed at H: green CI there approves.
	w.gh.SetChecks("success")
	w.agentsDone(wi, "approved once green at H", func() bool { return len(w.dudeReviews(wi, n)) == 2 })
	if got := w.dudeReviews(wi, n); got[1].State != "APPROVED" || got[1].CommitID != head {
		t.Fatalf("after CI passed at H: %+v", got)
	}
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
