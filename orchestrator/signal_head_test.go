package orchestrator_test

import (
	"context"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/forge"
)

// prWaitSignal tells the delivery parked at its pull request wait that
// checks failed on target's pull request at head.
func (w *world) checksFailedAt(wi, head, key string) {
	w.t.Helper()
	var wf string
	if err := w.owner.QueryRow(context.Background(), `SELECT id FROM workflow_runs WHERE task_id = $1`, wi).Scan(&wf); err != nil {
		w.t.Fatal(err)
	}
	sig := forge.Signal{Kind: "actionable", Repo: "target", Number: 1, Head: head,
		Feedback: []forge.ActionableFeedback{{Source: "checks", Repo: "target", Body: "CI failed",
			Checks: []forge.FailingCheck{{Name: "ci"}}}}}
	if err := w.runtime.Signal(context.Background(), w.org, wf, delivery.SignalPRFeedback, sig, key); err != nil {
		w.t.Fatal(err)
	}
}

// The pull request syncer names the head it read a signal at.
func TestASignalNamesTheHeadItWasReadAt(t *testing.T) {
	w := newWorld(t)
	wi := w.task()
	w.inReview(wi)
	head := w.gh.SHA(w.gh.Pull(1).Head)
	w.gh.SetChecks("failure")
	w.until("checks failing signalled", func() bool {
		w.sync()
		return w.count(`SELECT count(*) FROM workflow_signals s JOIN workflow_runs r ON r.id = s.workflow_run_id
			WHERE r.task_id = $1 AND s.name = $2 AND s.payload->>'head' = $3`, wi, delivery.SignalPRFeedback, head) > 0
	})
}

// Checks failing on a head the pull request no longer shows start no fix
// (a fix pushed since: its own checks are the next sync's); on the head it
// shows, they do.
func TestChecksFailingOnAnOlderHeadDoNotStartAFix(t *testing.T) {
	w := newWorld(t)
	wi := w.task()
	w.inReview(wi)
	head := w.gh.SHA(w.gh.Pull(1).Head)
	w.checksFailedAt(wi, "0123456789abcdef0123456789abcdef01234567", "old-head")
	for range 3 {
		w.pump()
	}
	if n := w.fixes(wi); n != 0 {
		t.Fatalf("%d fixes for checks on a head the pull request no longer shows\n%s", n, w.describeRuns())
	}
	if n := w.count(`SELECT count(*) FROM workflow_runs WHERE task_id = $1 AND step = 'awaitPullRequest'`, wi); n != 1 {
		t.Fatalf("not waiting on the pull request\n%s", w.describeRuns())
	}
	w.checksFailedAt(wi, head, "this-head")
	w.until("a fix for checks on the head shown", func() bool {
		w.pump()
		return w.fixes(wi) == 1
	})
}
