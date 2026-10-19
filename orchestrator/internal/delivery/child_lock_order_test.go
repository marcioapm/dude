package delivery

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// The task's row and its delivery's are locked delivery first, task second
// by every transaction that takes both (LockEscalationTx). These tests hold
// a child start or restart between its two locks while a person's Close,
// Archive or a status change to an ended status runs, so an inversion shows
// as Postgres's deadlock (40P01) on every run rather than under load.

func isDeadlock(err error) bool {
	var pg *pgconn.PgError
	return errors.As(err, &pg) && pg.Code == "40P01"
}

// waitForLockWaiter returns once a backend in the test's database waits on
// a heavyweight lock: the ender is queued behind the held transaction.
func (w *childWorld) waitForLockWaiter() {
	w.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		var n int
		if err := w.owner.QueryRow(w.ctx, `SELECT count(*) FROM pg_stat_activity
			WHERE datname = current_database() AND wait_event_type = 'Lock'`).Scan(&n); err != nil {
			w.t.Fatal(err)
		}
		if n > 0 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	w.t.Fatal("the ender never waited on a lock")
}

// The two orders themselves, with nothing of dude's code: Postgres refuses
// one side with 40P01. What the code-path tests below would see on an
// inversion.
func TestOpposedTaskAndDeliveryLocksDeadlock(t *testing.T) {
	w := newChildWorld(t)
	begin := func() pgx.Tx {
		tx, err := w.s.DB.Pool.Begin(w.ctx)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = tx.Rollback(context.Background()) })
		if _, err := tx.Exec(w.ctx, `SELECT set_config('app.organization_id', $1, true)`, w.org); err != nil {
			t.Fatal(err)
		}
		return tx
	}
	taskFirst, deliveryFirst := begin(), begin()
	if _, err := taskFirst.Exec(w.ctx, `SELECT 1 FROM tasks WHERE id = 'wi_life' FOR UPDATE`); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadDelivery(w.ctx, deliveryFirst, "wi_life"); err != nil {
		t.Fatal(err)
	}
	errs := make(chan error, 2)
	go func() {
		_, err := LoadDelivery(w.ctx, taskFirst, "wi_life")
		errs <- err
	}()
	w.waitForLockWaiter()
	go func() {
		_, err := deliveryFirst.Exec(w.ctx, `SELECT 1 FROM tasks WHERE id = 'wi_life' FOR NO KEY UPDATE`)
		errs <- err
	}()
	deadlocks := 0
	for range 2 {
		err := <-errs
		if isDeadlock(err) {
			deadlocks++
			continue
		}
		if err != nil {
			t.Fatalf("not a deadlock: %v", err)
		}
	}
	if deadlocks != 1 {
		t.Fatalf("deadlocks %d, want 1", deadlocks)
	}
}

// enders are the person's and the workflow's ways of ending a task that
// lock its delivery and then its row.
var enders = []struct {
	name string
	end  func(ctx context.Context, tx pgx.Tx, org string) error
}{
	{"close", func(ctx context.Context, tx pgx.Tx, org string) error {
		_, err := CloseTx(ctx, tx, org, CloseInput{TaskID: "wi_life", Resolution: ResolutionWontDo,
			Actor: Actor{ActorType: "system", ActorID: "test"}})
		return err
	}},
	{"archive", func(ctx context.Context, tx pgx.Tx, org string) error {
		_, err := ArchiveTaskTx(ctx, tx, org, ArchiveInput{CloseInput: CloseInput{TaskID: "wi_life",
			Resolution: ResolutionWontDo, Actor: Actor{ActorType: "system", ActorID: "test"}}})
		return err
	}},
	{"set_status", func(ctx context.Context, tx pgx.Tx, org string) error {
		_, err := SetTaskStatusTx(ctx, tx, org, "prj_life", "wi_life", "", "aborted", "a person stopped it")
		return err
	}},
}

// raceEnder runs hold in a transaction that pauses at its hook, starts the
// ender once the hook is reached, lets hold go on once the ender waits on
// a lock, and returns both transactions' errors.
func (w *childWorld) raceEnder(end func(context.Context, pgx.Tx, string) error,
	hold func(tx pgx.Tx, between func()) error) (holdErr, endErr error) {
	w.t.Helper()
	reached, release := make(chan struct{}), make(chan struct{})
	holdDone, endDone := make(chan error, 1), make(chan error, 1)
	go func() {
		holdDone <- w.s.DB.InOrg(w.ctx, w.org, func(tx pgx.Tx) error {
			return hold(tx, func() { close(reached); <-release })
		})
	}()
	select {
	case <-reached:
	case err := <-holdDone:
		w.t.Fatalf("held transaction ended before its hook: %v", err)
	}
	go func() {
		endDone <- w.s.DB.InOrg(w.ctx, w.org, func(tx pgx.Tx) error { return end(w.ctx, tx, w.org) })
	}()
	w.waitForLockWaiter()
	close(release)
	return <-holdDone, <-endDone
}

// A conductor's start_agent and a person ending the task at once: the
// start, between the locks it takes, is never refused for a deadlock, nor
// is the ending. The start goes first and makes its child; the ending,
// waiting on it, sees that child working (Close and Archive refuse it,
// running) or ends it as the task ends (a status change aborts children).
func TestAChildStartAndATaskEndingSerialiseWithoutADeadlock(t *testing.T) {
	for _, e := range enders {
		t.Run(e.name, func(t *testing.T) {
			w := newChildWorld(t)
			// Parked at its start with no pull request open, so Close and
			// Archive get past the task's own state to its agents.
			w.exec(`UPDATE tasks SET status = 'queued' WHERE id = 'wi_life'`)
			w.exec(`UPDATE pull_requests SET state = 'closed' WHERE id = 'pr_life'`)
			in := w.in
			in.Key = "raced"
			var child string
			startErr, endErr := w.raceEnder(e.end, func(tx pgx.Tx, between func()) error {
				var err error
				child, err = startChildTx(w.ctx, tx, in, between)
				return err
			})
			if isDeadlock(startErr) || isDeadlock(endErr) {
				t.Fatalf("deadlock: start %v, %s %v", startErr, e.name, endErr)
			}
			if startErr != nil || child == "" {
				t.Fatalf("start: %q %v", child, startErr)
			}
			assertEnderAfterChild(t, w, e.name, endErr, child)
		})
	}
}

// The same for a conductor restarting its writer child: the restart's
// writer admission locks the delivery.
func TestAChildRestartAndATaskEndingSerialiseWithoutADeadlock(t *testing.T) {
	for _, e := range enders {
		t.Run(e.name, func(t *testing.T) {
			w := newChildWorld(t)
			in := w.in
			in.Profile, in.Capabilities.PublishCode, in.Key = "implementer", true, "writer"
			writer, err := w.s.StartChild(w.ctx, in)
			if err != nil {
				t.Fatal(err)
			}
			w.exec(`UPDATE tasks SET status = 'queued' WHERE id = 'wi_life'`)
			w.exec(`UPDATE pull_requests SET state = 'closed' WHERE id = 'pr_life'`)
			var next string
			restartErr, endErr := w.raceEnder(e.end, func(tx pgx.Tx, between func()) error {
				var err error
				next, err = restartChildTx(w.ctx, tx, w.org, "wi_life", writer, "again", between)
				return err
			})
			if isDeadlock(restartErr) || isDeadlock(endErr) {
				t.Fatalf("deadlock: restart %v, %s %v", restartErr, e.name, endErr)
			}
			if restartErr != nil || next == "" {
				t.Fatalf("restart: %q %v", next, restartErr)
			}
			assertEnderAfterChild(t, w, e.name, endErr, next)
		})
	}
}

func assertEnderAfterChild(t *testing.T, w *childWorld, ender string, endErr error, child string) {
	t.Helper()
	if ender == "set_status" {
		if endErr != nil {
			t.Fatalf("set_status: %v", endErr)
		}
		var stop string
		if err := w.owner.QueryRow(w.ctx, `SELECT COALESCE(child_stop_reason, '') FROM runs WHERE id = $1`, child).Scan(&stop); err != nil {
			t.Fatal(err)
		}
		if stop != "parent-ended" {
			t.Fatalf("child after the task ended: stop reason %q", stop)
		}
		return
	}
	var busy LifecycleRefusal
	if !errors.As(endErr, &busy) || busy.Code != "running" {
		t.Fatalf("%s with the child working: %v", ender, endErr)
	}
}
