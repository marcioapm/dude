package phases

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// streamLux is lux streaming a Run's output from a channel, and answering
// Get as placementLux does — after gate is closed, when there is one.
type streamLux struct {
	*placementLux
	frames chan lux.Frame
	// Output was called: the stream is open.
	opened chan struct{}
	// Get waits for gate, and says it is waiting on entered.
	gate, entered chan struct{}
}

func newStreamLux(p *placementLux) *streamLux {
	return &streamLux{placementLux: p, frames: make(chan lux.Frame), opened: make(chan struct{}, 1)}
}

func (f *streamLux) Output(ctx context.Context, _, _ string, _ int64, fn func(lux.Frame) error) error {
	select {
	case f.opened <- struct{}{}:
	default:
	}
	for {
		select {
		case fr := <-f.frames:
			if err := fn(fr); err != nil {
				return err
			}
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (f *streamLux) Get(ctx context.Context, id string) (lux.Run, error) {
	if f.gate != nil {
		select {
		case f.entered <- struct{}{}:
		default:
		}
		select {
		case <-f.gate:
		case <-ctx.Done():
			return lux.Run{}, ctx.Err()
		}
	}
	return f.placementLux.Get(ctx, id)
}

// following starts the real follower on w's Run with lux streaming from
// the returned stub; it stops when the test ends.
func (w *resumeWorld) following() *streamLux {
	w.t.Helper()
	st := newStreamLux(w.lux)
	w.s.Lux = st
	r := w.run
	r.Status, r.LuxState = statusRunning, "resuming"
	ctx, cancel := context.WithCancel(w.ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = w.s.followOutput(ctx, r)
	}()
	w.t.Cleanup(func() { cancel(); <-done })
	return st
}

// cursorFrame is f, carrying the record cursor c the follower saves with
// its batch.
func cursorFrame(f lux.Frame, c string) lux.Frame {
	f.Cursor = c
	return f
}

// committed waits for the follower to have saved cursor c, and says how
// long that took.
func (w *resumeWorld) committed(c string, within time.Duration) time.Duration {
	w.t.Helper()
	start := time.Now()
	for time.Since(start) < within {
		var got string
		_ = w.owner.QueryRow(w.ctx, `SELECT COALESCE(lux_cursor, '') FROM runs WHERE id = $1`, w.run.ID).Scan(&got)
		if got == c {
			return time.Since(start)
		}
		time.Sleep(5 * time.Millisecond)
	}
	w.t.Fatalf("the batch up to cursor %s did not commit within %s", c, within)
	return 0
}

// lockResume holds the resume row's lock from another connection until
// the returned func is called.
func (w *resumeWorld) lockResume(epoch int) func() {
	w.t.Helper()
	other, err := pgx.Connect(w.ctx, w.owner.Config().ConnString())
	if err != nil {
		w.t.Fatal(err)
	}
	tx, err := other.Begin(w.ctx)
	if err != nil {
		w.t.Fatal(err)
	}
	if _, err := tx.Exec(w.ctx, `SELECT 1 FROM run_resumes WHERE run_id = $1 AND epoch = $2 FOR UPDATE`, w.run.ID, epoch); err != nil {
		w.t.Fatal(err)
	}
	released := false
	release := func() {
		if !released {
			released = true
			_ = tx.Rollback(w.ctx)
			_ = other.Close(w.ctx)
		}
	}
	w.t.Cleanup(release)
	return release
}

// A resume row locked elsewhere holds up nothing of the Run: the batch
// carrying lux's running state and the agent's first busy commits its
// frames and cursor at once, without the timing it could not write, and a
// later batch is recorded as usual.
func TestALockedResumeRowDoesNotHoldUpTheRunsStream(t *testing.T) {
	w := newResumeWorld(t)
	base := time.Now().Add(-time.Minute).UTC()
	w.resume(stoppedOnHost1(base))
	w.lux.set(runningAgain(base, "host-a"), nil)
	release := w.lockResume(2)
	st := w.following()

	st.frames <- running(2)
	st.frames <- cursorFrame(busy(2), "c1")
	if took := w.committed("c1", 5*time.Second); took > 600*time.Millisecond {
		t.Errorf("the batch took %s to commit behind a locked resume row", took)
	}
	var luxState string
	var busyAt *time.Time
	_ = w.owner.QueryRow(w.ctx, `SELECT lux_state, agent_busy_at FROM runs WHERE id = $1`, w.run.ID).Scan(&luxState, &busyAt)
	if luxState != "running" || busyAt == nil {
		t.Errorf("the Run's own state was not recorded: lux_state %q busy %v", luxState, busyAt)
	}
	release()
	if row := w.row(2); row["running_at"] != nil || row["busy_at"] != nil {
		t.Errorf("timing written though its row was locked: running %v busy %v", row["running_at"], row["busy_at"])
	}

	st.frames <- cursorFrame(spoke(2), "c2")
	if took := w.committed("c2", 5*time.Second); took > 600*time.Millisecond {
		t.Errorf("the later batch took %s", took)
	}
	if w.row(2)["first_output_at"] == nil {
		t.Errorf("the later batch's first output was not recorded")
	}
}

// A timing statement waiting on a lock while the Run's batch has a short
// deadline fails on its own budget, inside that deadline: the Run's
// update after it goes through on the same transaction.
func TestATimingLockWaitInsideTheRunsDeadlineLeavesTheRunsTransactionUsable(t *testing.T) {
	w := newResumeWorld(t)
	w.resume(stoppedOnHost1(time.Now()))
	w.lockResume(2)
	tr := &translator{run: w.run}
	ctx, cancel := context.WithTimeout(w.ctx, 100*time.Millisecond)
	defer cancel()
	err := w.s.DB.InOrg(ctx, w.run.Org, func(tx pgx.Tx) error {
		tr.resumeBusy(ctx, tx, w.s, 2)
		_, err := tx.Exec(ctx, `UPDATE runs SET agent_busy_at = now() WHERE id = $1`, w.run.ID)
		return err
	})
	if err != nil {
		t.Fatalf("the Run's update failed after a timing statement waited on a lock: %v", err)
	}
	var busyAt *time.Time
	_ = w.owner.QueryRow(w.ctx, `SELECT agent_busy_at FROM runs WHERE id = $1`, w.run.ID).Scan(&busyAt)
	if busyAt == nil {
		t.Errorf("the Run's update did not commit")
	}
}

// The timing budget is the timing statement's alone: whether it succeeds
// or fails, the Run's transaction has its own lock_timeout and
// statement_timeout back afterwards.
func TestTheTimingBudgetIsPutBackForTheRunsTransaction(t *testing.T) {
	w := newResumeWorld(t)
	for _, c := range []struct {
		name string
		fn   func(context.Context, pgx.Tx) error
	}{
		{"succeeded", func(ctx context.Context, tx pgx.Tx) error { _, err := tx.Exec(ctx, `SELECT 1`); return err }},
		{"failed", func(ctx context.Context, tx pgx.Tx) error { _, err := tx.Exec(ctx, `SELECT 1/0`); return err }},
		{"timed out", func(ctx context.Context, tx pgx.Tx) error { _, err := tx.Exec(ctx, `SELECT pg_sleep(1)`); return err }},
	} {
		var inside, lock, statement string
		if err := w.s.DB.InOrg(w.ctx, w.run.Org, func(tx pgx.Tx) error {
			if _, err := tx.Exec(w.ctx, `SET LOCAL lock_timeout = '7s'; SET LOCAL statement_timeout = '9s'`); err != nil {
				return err
			}
			w.s.bestEffort(w.ctx, tx, w.run, c.name, func(ctx context.Context, tx pgx.Tx) error {
				_ = tx.QueryRow(ctx, `SELECT current_setting('lock_timeout') || ' ' || current_setting('statement_timeout')`).Scan(&inside)
				return c.fn(ctx, tx)
			})
			return tx.QueryRow(w.ctx, `SELECT current_setting('lock_timeout'), current_setting('statement_timeout')`).
				Scan(&lock, &statement)
		}); err != nil {
			t.Fatalf("%s: the Run's transaction failed: %v", c.name, err)
		}
		if inside != "100ms 250ms" {
			t.Errorf("%s: timing ran with lock_timeout and statement_timeout %q, want 100ms 250ms", c.name, inside)
		}
		if lock != "7s" || statement != "9s" {
			t.Errorf("%s: the Run's transaction has lock_timeout %s, statement_timeout %s after timing, want 7s, 9s",
				c.name, lock, statement)
		}
	}
}

// A follower starting on a Run opens lux's stream at once, whatever the
// timing of its earlier resumes waits on.
func TestAFollowerOpensTheStreamWithoutWaitingOnTiming(t *testing.T) {
	w := newResumeWorld(t)
	other, err := pgx.Connect(w.ctx, w.owner.Config().ConnString())
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close(w.ctx)
	tx, err := other.Begin(w.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(w.ctx)
	if _, err := tx.Exec(w.ctx, `LOCK TABLE run_resumes IN ACCESS EXCLUSIVE MODE`); err != nil {
		t.Fatal(err)
	}
	st := w.following()
	select {
	case <-st.opened:
	case <-time.After(time.Second):
		t.Fatal("the follower did not open lux's stream while the resumes table was locked")
	}
}
