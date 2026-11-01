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
// transaction commits after. One that ends without waiting fails the test
// unless waitOptional, when holder goes on all the same. Returns recheck's
// result, changed, and the two transactions' errors.
func (w *childWorld) recheckRace(holder pgx.Tx, sc workflow.StepContext, res workflow.Result, waitOptional bool,
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
	if err := w.waitForLockWaiter(ctx, ended); err != nil && !(waitOptional && ctx.Err() == nil) {
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
	_, _, recheckErr, holderErr := w.recheckRace(holder, sc, workflow.Result{Next: "conductorDecision", State: st}, false,
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

// A step that escalated while a person hands the delivery to the conductor
// (LoadDelivery, SetDeciderTx, not committed yet): recheck waits for the
// handover and reads the conductor as the decider, so the escalation is
// told once, not committed under the conductor untold. Without the lock,
// recheck reads the policy and returns unchanged, before the handover
// commits.
func TestRecheckWaitsForAHandoverAndTellsTheEscalation(t *testing.T) {
	w := newChildWorld(t)
	holder := w.beginInOrg()
	d, err := LoadDelivery(w.ctx, holder, "wi_life")
	if err != nil || d == nil {
		t.Fatalf("delivery %v, %v", d, err)
	}
	if changed, err := SetDeciderTx(w.ctx, holder, w.org, d.WorkflowID, &d.State, DeciderConductor, "test",
		"system", "test"); err != nil || !changed {
		t.Fatalf("set decider %v, %v", changed, err)
	}
	st := &State{TaskID: "wi_life", ProjectID: "prj_life", Attempt: 3,
		Escalation: &Escalation{Reason: "stuck", Step: "awaitPullRequest"}}
	sc := workflow.StepContext{WorkflowRunID: "wf_life", OrganizationID: w.org, Step: "awaitPullRequest"}
	out, changed, recheckErr, holderErr := w.recheckRace(holder, sc, workflow.Result{Next: "decide", State: st}, true,
		func(context.Context, pgx.Tx) error { return nil })
	if recheckErr != nil || holderErr != nil {
		t.Fatalf("recheck %v, handover %v", recheckErr, holderErr)
	}
	got, _ := out.State.(*State)
	if !changed || got == nil || got.Escalation == nil || got.Escalation.Told != 1 || got.Decider != DeciderConductor {
		var e *Escalation
		var decider string
		if got != nil {
			e, decider = got.Escalation, got.Decider
		}
		t.Fatalf("changed %v, decider %q, escalation %+v", changed, decider, e)
	}
	if n := w.count(`conductor_wakes WHERE task_id = 'wi_life' AND kind = 'escalation'`); n != 1 {
		t.Fatalf("escalation wakes %d, want 1", n)
	}
}
