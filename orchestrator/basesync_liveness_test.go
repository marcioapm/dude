package orchestrator_test

// A publish whose accounting cannot be had: retried while its container
// runs, with a back-off, and never waited on once lux says it is over.

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
)

// pushingWithSharedBase is a task whose implementer has asked lux to push,
// its push held; main moved on the implementer's own file and the
// implementer fetched it, so the accounting needs the checkout's evidence.
func pushingWithSharedBase(t *testing.T) (*world, string, string) {
	w := newWorld(t)
	w.lux.HoldPushes = true
	wi := w.task()
	w.deliver(wi)
	w.until("the implementer pushing", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'implement' AND push_request_id IS NOT NULL`, wi) == 1
	})
	w.commitFileIn(w.gh.Repo, "main", "FACTORY.md", "someone else's\n", "Someone else's merge")
	run := w.phaseRunOf(wi, "implement")
	w.fetchBase(run)
	return w, wi, run
}

// execsOf is how many execs lux was asked for on the Run's lux Run.
func (w *world) execsOf(runID string) int {
	w.t.Helper()
	id := w.luxRunOf(runID)
	for _, r := range w.lux.Runs() {
		if r != nil && r.ID == id {
			return countOf(slices.Clone(r.Calls), "exec")
		}
	}
	return 0
}

// The implementer's container is lost after lux pushed and before its work
// could be accounted: the Run fails, kept, saying so, and does not wait.
func TestAPhaseRunWhoseContainerStoppedAfterItsPushFails(t *testing.T) {
	w, wi, run := pushingWithSharedBase(t)
	w.syncer.RetryAhead = time.Minute
	w.lux.DropExecs("DUDE_ACCOUNTING", 1)
	w.lux.HoldPushes = false
	w.lux.ReleasePushes()
	w.until("the accounting met the outage", func() bool { return w.execsOf(run) >= 1 })
	w.lux.Crash(w.luxRunOf(run))
	w.until("the implementer failed", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'failed'`, run) == 1
	})
	if e := w.runError(wi, "implement"); e != delivery.RefusedUnaccounted {
		t.Fatalf("error %q, want %q", e, delivery.RefusedUnaccounted)
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND keep`, run); n != 1 {
		t.Fatal("the failed Run's container was not kept for a resume")
	}
}

// A hung accounting Exec holds one sweep for its bound, then waits out a
// back-off that doubles: the passes between do not ask lux again.
func TestAHungAccountingHoldsOneSweepAndBacksOff(t *testing.T) {
	w, _, run := pushingWithSharedBase(t)
	const bound = 2 * time.Second
	w.syncer.AccountingTimeout = bound
	w.lux.HangExecs("DUDE_ACCOUNTING", 100)
	w.lux.HoldPushes = false
	w.lux.ReleasePushes()
	execs := func() int { return w.execsOf(run) }
	sweep := func() time.Duration {
		start := time.Now()
		if _, err := w.syncer.Sweep(context.Background()); err != nil {
			t.Fatal(err)
		}
		return time.Since(start)
	}
	for deadline := time.Now().Add(20 * time.Second); execs() == 0; {
		if took := sweep(); took > bound+3*time.Second {
			t.Fatalf("a sweep took %s, past the accounting's bound of %s", took, bound)
		}
		if time.Now().After(deadline) {
			t.Fatal("the accounting was never asked")
		}
	}
	before := execs()
	for range 3 {
		if took := sweep(); took >= bound {
			t.Fatalf("a sweep in the back-off took %s", took)
		}
	}
	if n := execs(); n != before {
		t.Fatalf("%d more accounting execs within the back-off", n-before)
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND next_attempt_at > now() + interval '4 seconds'`, run); n != 1 {
		t.Fatal("the first back-off is not 5 s")
	}
	mustExec(t, w.owner, `UPDATE runs SET next_attempt_at = now() WHERE id = $1`, run)
	if took := sweep(); took < bound || took > bound+3*time.Second {
		var waits int
		var status, next string
		_ = w.owner.QueryRow(context.Background(), `SELECT accounting_waits, status::text, COALESCE((next_attempt_at - now())::text, '') FROM runs WHERE id = $1`, run).Scan(&waits, &status, &next)
		t.Fatalf("the second try took %s (waits %d, %s, next in %s, execs %d)", took, waits, status, next, execs())
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND accounting_waits = 2
		AND next_attempt_at > now() + interval '9 seconds'`, run); n != 1 {
		t.Fatal("the second back-off did not double to 10 s")
	}
}

// A conductor's publish whose accounting cannot be had is refused once its
// container is over, or once it has been tried enough, through the
// refusal the conductor hears; never waited on for ever.
func TestAConductorPublishWhoseAccountingCannotBeHadIsRefused(t *testing.T) {
	setup := func(t *testing.T) (*editing, string) {
		e, _ := newCommitted(t)
		e.commitFileIn(e.gh.Repo, "main", "README.md", "# base\n", "Base moved")
		e.fetchBase(e.cond)
		e.lux.DropExecs("DUDE_ACCOUNTING", 1000)
		id := e.publish()
		e.until("the accounting met the outage", func() bool {
			return e.count(`SELECT count(*) FROM conductor_publishes WHERE id = $1 AND failures >= 1`, id) == 1
		})
		return e, id
	}
	refused := func(t *testing.T, e *editing, id string) {
		t.Helper()
		if status, why := e.published(id); status != delivery.PublishRefused || why != delivery.RefusedUnaccounted {
			t.Fatalf("publish %s: %q, want refused saying %q", status, why, delivery.RefusedUnaccounted)
		}
		e.wokenWith(e.task, delivery.RefusedUnaccounted)
	}
	t.Run("its container stopped after the push", func(t *testing.T) {
		e, id := setup(t)
		e.lux.Crash(e.luxRunOf(e.cond))
		e.until("lux's word recorded", func() bool {
			return e.count(`SELECT count(*) FROM runs WHERE id = $1 AND lux_state = 'failed'`, e.cond) == 1
		})
		mustExec(t, e.owner, `UPDATE conductor_publishes SET next_attempt_at = now() WHERE id = $1`, id)
		refused(t, e, id)
	})
	t.Run("past its tries", func(t *testing.T) {
		e, id := setup(t)
		mustExec(t, e.owner, `UPDATE conductor_publishes SET failures = 8, next_attempt_at = now() WHERE id = $1`, id)
		refused(t, e, id)
	})
}
