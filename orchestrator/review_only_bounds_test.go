package orchestrator_test

import (
	"strings"
	"testing"
	"time"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/fakelux"
)

// A push while a round runs never aborts it: its verdict is abandoned
// (no review at the stale head), and the next round, after the debounce,
// reviews the newest head.
func TestAPushDuringARoundAbandonsItsVerdictThenReReviews(t *testing.T) {
	w := newWorld(t)
	w.reRounds(time.Second)
	wi, n, _ := w.approvedAtH1()
	var h3 string
	scripted := w.lux.Decide
	w.lux.Decide = func(spec map[string]any) fakelux.Behaviour {
		if labels, _ := spec["labels"].(map[string]any); labels["dude.phase"] == "review" && h3 == "" {
			h3 = w.gh.CommitFiles("feature/greet", "alice's third", map[string]string{"third.md": "third\n"})
			w.sync() // signalled mid-round, and never again
		}
		return scripted(spec)
	}
	h2 := w.push("second.md")
	w.until("reviewed H3", func() bool {
		w.advanced()
		for _, id := range w.pendingPhaseRuns(wi) {
			w.finished(id)
		}
		return len(reviewsAt(w.dudeReviews(wi, n), h3)) == 1 && w.waitingOnPR(wi)
	})
	if got := reviewsAt(w.dudeReviews(wi, n), h2); len(got) != 0 {
		t.Fatalf("a review at the stale head H2: %+v", got)
	}
	if e := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = $2 AND payload->>'head' = $3`,
		wi, delivery.EvReviewAbandoned, h2); e == 0 {
		t.Fatal("round 2's verdict at H2 was not abandoned")
	}
	if e := w.events(wi, delivery.EvReviewRoundStarted); e != 2 {
		t.Fatalf("%d rounds started, want 2 (H2, then H3)", e)
	}
}

// A force-push: the new head does not descend from the reviewed one. The
// next round is fresh over the whole branch: no lens goes on from its
// predecessor, even with rounds resuming.
func TestAForcePushIsReviewedWholeAndFresh(t *testing.T) {
	w := newWorld(t)
	w.reRounds(time.Second)
	mustExec(t, w.owner, `UPDATE projects SET delivery_policy = delivery_policy || '{"resumeRounds":true}' WHERE id = $1`, w.project)
	wi, n, _ := w.approvedAtH1()
	// Rewritten from main: H1 is no ancestor of the new head.
	if err := w.gh.DeleteBranch("feature/greet"); err != nil {
		t.Fatal(err)
	}
	w.gh.Branch("feature/greet", "main", "alice's rewrite")
	w.sync()
	h2 := w.gh.SHA("feature/greet")
	w.agentsDone(wi, "reviewed the rewrite", func() bool { return len(reviewsAt(w.dudeReviews(wi, n), h2)) == 1 && w.waitingOnPR(wi) })
	if r := w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'review' AND base_refs->>'target' = $2
		AND (continues_run_id IS NOT NULL OR review_range IS NOT NULL)`, wi, h2); r != 0 {
		t.Fatalf("%d lens Runs at the rewrite went on from H1 or reviewed a range", r)
	}
	if e := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = $2 AND payload->>'fresh' = 'true'`,
		wi, delivery.EvReviewRoundStarted); e != 1 {
		t.Fatalf("the round at the rewrite is not fresh (%d)", e)
	}
}

// Bounds: with maxReviewRounds 2, the third push starts no round, and the
// status comment says so; the task keeps waiting.
func TestTheRoundsBoundStopsReReviewing(t *testing.T) {
	w := newWorld(t)
	w.reRounds(500 * time.Millisecond)
	mustExec(t, w.owner, `UPDATE forge_credentials SET settings = '{"maxReviewRounds":2}' WHERE organization_id = $1`, w.org)
	wi, n, _ := w.approvedAtH1()
	h2 := w.push("second.md")
	w.agentsDone(wi, "reviewed H2", func() bool { return len(reviewsAt(w.dudeReviews(wi, n), h2)) == 1 && w.waitingOnPR(wi) })
	runs := w.count(`SELECT count(*) FROM runs WHERE task_id = $1`, wi)
	w.push("third.md")
	w.until("stopped", func() bool { w.advanced(); return w.events(wi, delivery.EvReviewRoundsStopped) == 1 })
	time.Sleep(time.Second)
	w.sync()
	w.advanced()
	if r := w.count(`SELECT count(*) FROM runs WHERE task_id = $1`, wi); r != runs {
		t.Fatalf("%d Runs after the bound", r-runs)
	}
	if c := w.verdictComments(wi, n); len(c) != 1 || !strings.Contains(c[0].Body, "dude stopped re-reviewing after 2 rounds; re-request to continue") {
		t.Fatalf("status comment %+v", c)
	}
	if s := w.taskStatus(wi); s != "review" || !w.waitingOnPR(wi) {
		t.Fatalf("the task is %s: the bound must not end it", s)
	}
	// Still stale-approval safe past the bound.
	if e := w.events(wi, delivery.EvReviewDismissed); e != 2 {
		t.Fatalf("%d dismissals, want H1's and H2's", e)
	}
}

// Bounds: the attempt's Runs at the cost cap: no round on a push.
func TestTheCostCapStopsReReviewing(t *testing.T) {
	w := newWorld(t)
	w.reRounds(500 * time.Millisecond)
	mustExec(t, w.owner, `UPDATE forge_credentials SET settings = '{"reviewCostCap":1.5}' WHERE organization_id = $1`, w.org)
	wi, n, _ := w.approvedAtH1()
	mustExec(t, w.owner, `UPDATE runs SET agent_cost_usd = 2 WHERE task_id = $1 AND phase = 'review'`, wi)
	runs := w.count(`SELECT count(*) FROM runs WHERE task_id = $1`, wi)
	w.push("second.md")
	w.until("stopped", func() bool { w.advanced(); return w.events(wi, delivery.EvReviewRoundsStopped) == 1 })
	time.Sleep(time.Second)
	w.advanced()
	if r := w.count(`SELECT count(*) FROM runs WHERE task_id = $1`, wi); r != runs {
		t.Fatalf("%d Runs past the cost cap", r-runs)
	}
	if c := w.verdictComments(wi, n); len(c) != 1 || !strings.Contains(c[0].Body, "cost cap") {
		t.Fatalf("status comment %+v", c)
	}
}

// T-N1: failing CI at H1 wakes the conductor; after a push, failing CI at
// H2 wakes it again (CIAsked is not carried across heads), and each head
// gets its own review (a new claim).
func TestRedCIAtANewHeadWakesTheConductorAgain(t *testing.T) {
	w := newWorld(t)
	w.reRounds(time.Second)
	w.gh.SetChecks("failure")
	wi, n := w.reviewOnly()
	h1 := w.gh.SHA("feature/greet")
	w.agentsDone(wi, "commented at H1", func() bool { return len(reviewsAt(w.dudeReviews(wi, n), h1)) == 1 && w.waitingOnPR(wi) })
	h2 := w.push("second.md")
	w.agentsDone(wi, "commented at H2", func() bool { return len(reviewsAt(w.dudeReviews(wi, n), h2)) == 1 && w.waitingOnPR(wi) })
	if d := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'conductor.decision_awaited'
		AND payload->>'point' = $2`, wi, delivery.PointPRFeedback); d != 2 {
		t.Fatalf("%d decisions about failing checks, want one per head", d)
	}
	if c := w.count(`SELECT count(DISTINCT payload->>'claimId') FROM events WHERE task_id = $1 AND event_type = $2`,
		wi, delivery.EvReviewSubmitted); c != 2 {
		t.Fatalf("%d claims for two heads", c)
	}
}

// T-N1: a deferral at H1 (CI pending a long time there) never caps out the
// verdict at H2: its deferral clock starts again.
func TestADeferralAtH1NeverCapsH2(t *testing.T) {
	w := newWorld(t)
	w.withDeliveries(&delivery.Store{ReviewDebounce: time.Second, VerdictDeferCap: 4 * time.Second})
	w.gh.SetChecks("pending")
	wi, n := w.reviewOnly()
	w.agentsDone(wi, "deferred at H1", func() bool { return w.events(wi, delivery.EvVerdictDeferred) > 0 && w.waitingOnPR(wi) })
	time.Sleep(3 * time.Second)
	h2 := w.push("second.md")
	w.agentsDone(wi, "deferred at H2", func() bool {
		return w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = $2 AND payload->>'head' = $3`,
			wi, delivery.EvVerdictDeferred, h2) > 0 && w.waitingOnPR(wi)
	})
	if got := w.dudeReviews(wi, n); len(got) != 0 {
		t.Fatalf("H2's verdict capped by H1's deferral: %+v", got)
	}
	w.gh.SetChecks("success")
	w.until("approved at H2", func() bool { w.sync(); w.advanced(); return len(reviewsAt(w.dudeReviews(wi, n), h2)) == 1 })
	if got := reviewsAt(w.dudeReviews(wi, n), h2); got[0].State != "APPROVED" {
		t.Fatalf("at H2: %+v", got)
	}
}
