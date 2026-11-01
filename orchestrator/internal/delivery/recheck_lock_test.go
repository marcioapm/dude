package delivery

import (
	"context"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/workflow"
)

// recheck locks the delivery's row in the transition's transaction. These
// tests hold the other side of each lock it must or must not conflict with
// in an open transaction, start recheck once it is held, and let the holder
// commit once recheck waits on a lock, so each shape runs the same way on
// every run.

// recheckRace runs recheck in its own transaction in a goroutine, against
// holder, an open transaction that has taken its locks. Once recheck waits
// on a lock, then (holder's rest) runs and holder commits; recheck's
// transaction commits after. Returns recheck's result, changed, and the two
// transactions' errors.
func (w *childWorld) recheckRace(holder pgx.Tx, sc workflow.StepContext, res workflow.Result,
	then func(ctx context.Context, tx pgx.Tx) error) (out workflow.Result, changed bool, recheckErr, holderErr error) {
	w.t.Helper()
	ctx, cancel := context.WithTimeout(w.ctx, raceTimeout)
	defer cancel()
	ended := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer close(ended)
		recheckErr = w.s.DB.InOrg(ctx, w.org, func(tx pgx.Tx) error {
			var err error
			out, changed, err = (&steps{s: w.s}).recheck(ctx, tx, sc, res)
			return err
		})
	}()
	if err := w.waitForLockWaiter(ctx, ended); err != nil {
		_ = holder.Rollback(context.Background())
		wg.Wait()
		w.t.Fatalf("%v (recheck: %v)", err, recheckErr)
	}
	holderErr = then(ctx, holder)
	if holderErr == nil {
		holderErr = holder.Commit(ctx)
	} else {
		_ = holder.Rollback(context.Background())
	}
	wg.Wait()
	return out, changed, recheckErr, holderErr
}

func (w *childWorld) beginInOrg() pgx.Tx {
	w.t.Helper()
	tx, err := w.s.DB.Pool.Begin(w.ctx)
	if err != nil {
		w.t.Fatal(err)
	}
	w.t.Cleanup(func() { _ = tx.Rollback(context.Background()) })
	if _, err := tx.Exec(w.ctx, `SELECT set_config('app.organization_id', $1, true)`, w.org); err != nil {
		w.t.Fatal(err)
	}
	return tx
}

// A writer holding the task that signals the delivery (EndConductorWait:
// answering a question, a repository decision) while a policy delivery
// parks at its Ask me first: recheck holds the delivery's row and waits on
// the task's; the signal's insert takes KEY SHARE on the delivery's row,
// which recheck's lock lets through. Both commit, no 40P01.
func TestRecheckAtAskMeFirstLetsATaskHoldersSignalThrough(t *testing.T) {
	w := newChildWorld(t)
	holder := w.beginInOrg()
	if err := LockTaskTx(w.ctx, holder, "wi_life"); err != nil {
		t.Fatal(err)
	}
	st := &State{TaskID: "wi_life", ProjectID: "prj_life", Attempt: 3, ChoiceGate: true,
		Decision: &Pending{Point: PointBeforePR, Policy: "openPullRequest"}}
	sc := workflow.StepContext{WorkflowRunID: "wf_life", OrganizationID: w.org, Step: "awaitPullRequest"}
	_, _, recheckErr, holderErr := w.recheckRace(holder, sc, workflow.Result{Next: "conductorDecision", State: st},
		func(ctx context.Context, tx pgx.Tx) error {
			return workflow.SignalTx(ctx, tx, w.org, "wf_life", SignalReviewQuestion, map[string]any{}, "")
		})
	if isDeadlock(recheckErr) || isDeadlock(holderErr) {
		t.Fatalf("deadlock: recheck %v, signaller %v", recheckErr, holderErr)
	}
	if recheckErr != nil || holderErr != nil {
		t.Fatalf("recheck %v, signaller %v", recheckErr, holderErr)
	}
	if n := w.count(`workflow_signals WHERE workflow_run_id = 'wf_life'`); n != 1 {
		t.Fatalf("signals %d, want 1", n)
	}
}
