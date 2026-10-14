package phases

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// logSink is a log writer goroutines may share.
type logSink struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *logSink) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}

func (l *logSink) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.String()
}

func (l *logSink) Reset() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.buf.Reset()
}

// errAttemptLost rolls back a follower batch whose interleaving was not
// reached, so the next attempt starts from the same state.
var errAttemptLost = errors.New("interleaving not reached")

// A follower batch holds the Run's row FOR UPDATE, as followOutput takes
// it, while resumeAsked records a resume: the first time, inserting its
// row, or again while lux still reports the Run stopped, updating it.
// Once resumeAsked waits on the batch, the batch applies the new epoch's
// session, running state, busy and first output, each stamping that
// epoch's row, and commits. Neither may wait on the other: the batch
// commits while resumeAsked still waits, resumeAsked then records the
// resume, and no timing statement fails.
//
// The first time, the row is not in for the batch to stamp. Again, the
// batch stamps it, and its running_at then keeps resumeAsked from moving
// requested_at.
func TestAFollowerBatchAndAResumeRecordingTheSameEpochDoNotDeadlock(t *testing.T) {
	for _, c := range []struct {
		name  string
		again bool
	}{{"first", false}, {"again", true}} {
		t.Run(c.name, func(t *testing.T) {
			w := newResumeWorld(t)
			logs := &logSink{}
			w.s.Log = slog.New(slog.NewTextHandler(logs, nil))
			ctx, cancel := context.WithTimeout(w.ctx, 30*time.Second)
			var workers sync.WaitGroup
			// LIFO: the workers are joined before the fixture closes its database.
			t.Cleanup(func() {
				cancel()
				workers.Wait()
			})
			base := time.Now().Add(-time.Minute).UTC()
			w.sessionOn(1)
			var before time.Time
			if c.again {
				w.s.resumeAsked(ctx, w.run, stoppedOnHost1(base))
				before = w.row(2)["requested_at"].(time.Time)
			}

			// resumeAsked waits at most timingLockTimeout. An attempt where
			// it stopped waiting before the batch had applied its frames, a
			// scheduling delay longer than that, did not reach the
			// interleaving: its batch is rolled back and it is tried again.
			const attempts = 5
			for attempt := 1; ; attempt++ {
				reached, err := w.stampWhileTheResumeWaits(ctx, &workers, logs, base)
				if err != nil {
					t.Fatalf("attempt %d: %v\nlogs:\n%s", attempt, err, logs)
				}
				if reached {
					break
				}
				if attempt == attempts {
					t.Fatalf("in %d attempts the batch never applied its frames while the resume waited on it\nlogs:\n%s",
						attempts, logs)
				}
				t.Logf("attempt %d did not reach the interleaving:\n%s", attempt, logs)
				logs.Reset()
			}

			if got := logs.String(); got != "" {
				t.Errorf("a timing statement failed:\n%s", got)
			}
			row := w.row(2)
			asked, ok := row["requested_at"].(time.Time)
			if !ok || c.again && !asked.Equal(before) {
				t.Errorf("requested_at is %v; want it set, and kept at %v once running_at is in", row["requested_at"], before)
			}
			for _, col := range []string{"running_at", "busy_at", "first_output_at"} {
				if stamped := row[col] != nil; stamped != c.again {
					t.Errorf("%s stamped: %v, want %v", col, stamped, c.again)
				}
			}
			if luxState, busyAt := w.runState(); luxState != "running" || busyAt == nil {
				t.Errorf("the batch did not commit: lux_state %q, busy %v", luxState, busyAt)
			}
		})
	}
}

// resumeAsked's lock on the Run admits a writer that leaves its key alone
// (an UPDATE of the Run, the artifacts sweep's lock): with one holding the
// Run's row, a resume is recorded, the first time and again, without
// waiting for it.
func TestAResumeIsRecordedWhileAnotherTransactionUpdatesTheRun(t *testing.T) {
	w := newResumeWorld(t)
	logs := &logSink{}
	w.s.Log = slog.New(slog.NewTextHandler(logs, nil))
	base := time.Now().Add(-time.Minute).UTC()
	release := w.holding(`SELECT 1 FROM runs WHERE id = $1 FOR NO KEY UPDATE`, w.run.ID)
	defer release()

	w.s.resumeAsked(w.ctx, w.run, stoppedOnHost1(base))
	first, ok := w.row(2)["requested_at"].(time.Time)
	if !ok {
		t.Fatalf("no requested_at with the Run's row held FOR NO KEY UPDATE\nlogs:\n%s", logs)
	}
	w.s.resumeAsked(w.ctx, w.run, stoppedOnHost1(base))
	if again := w.row(2)["requested_at"].(time.Time); !again.After(first) {
		t.Errorf("the retry left requested_at at %v", again)
	}
	if got := logs.String(); got != "" {
		t.Errorf("a timing statement failed:\n%s", got)
	}
}

// stampWhileTheResumeWaits is one attempt. reached is false when
// resumeAsked stopped waiting on the batch before the batch had applied
// its frames, without the batch waiting on it; the batch is then rolled
// back. The batch waiting on resumeAsked while resumeAsked waits on the
// batch is a deadlock, returned as an error.
func (w *resumeWorld) stampWhileTheResumeWaits(ctx context.Context, workers *sync.WaitGroup, logs *logSink,
	base time.Time) (reached bool, err error) {
	tr := &translator{run: w.run}
	tr.run.Status, tr.run.LuxState = statusRunning, "resuming"
	if err := w.s.DB.InOrg(ctx, w.run.Org, func(tx pgx.Tx) error { return tr.load(ctx, tx) }); err != nil {
		return false, err
	}

	locked := make(chan int32, 1)
	apply := make(chan struct{})
	applied := make(chan error, 1)
	// Closed once the attempt's outcome is known, with keep set; every
	// return closes it, so the batch never outlives the attempt.
	var keep bool
	finish := make(chan struct{})
	release := sync.OnceFunc(func() { close(finish) })
	defer release()
	followed := make(chan error, 1)
	workers.Add(1)
	go func() {
		defer workers.Done()
		followed <- w.s.DB.InOrg(ctx, w.run.Org, func(tx pgx.Tx) error {
			// The batch's first statement, as followOutput takes it.
			var pid int32
			if err := tx.QueryRow(ctx, `SELECT pg_backend_pid() FROM runs WHERE id = $1 FOR UPDATE`, w.run.ID).Scan(&pid); err != nil {
				return err
			}
			locked <- pid
			select {
			case <-apply:
			case <-finish:
				return errAttemptLost
			case <-ctx.Done():
				return ctx.Err()
			}
			var err error
			for _, f := range []lux.Frame{session(2), running(2), busy(2), spoke(2)} {
				if err = tr.apply(ctx, tx, w.s, f); err != nil {
					break
				}
			}
			if err == nil {
				err = tr.save(ctx, tx, "", 0)
			}
			applied <- err
			if err != nil {
				return err
			}
			select {
			case <-finish:
			case <-ctx.Done():
				return ctx.Err()
			}
			if !keep {
				return errAttemptLost
			}
			return nil
		})
	}()
	var follower int32
	select {
	case follower = <-locked:
	case err := <-followed:
		return false, err
	case <-ctx.Done():
		return false, ctx.Err()
	}
	// The batch's transaction has ended, kept or rolled back, before the
	// attempt returns.
	ended := func() error {
		release()
		select {
		case err := <-followed:
			if errors.Is(err, errAttemptLost) {
				return nil
			}
			return err
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	fail := func(err error) (bool, error) { return false, errors.Join(err, ended()) }

	asked := make(chan struct{})
	workers.Add(1)
	go func() {
		defer workers.Done()
		defer close(asked)
		w.s.resumeAsked(ctx, w.run, stoppedOnHost1(base))
	}()
	// The backend waiting on blocker's locks, 0 when none is.
	waitingOn := func(blocker int32) (int32, error) {
		var pid int32
		err := w.owner.QueryRow(ctx, `SELECT COALESCE((SELECT pid FROM pg_stat_activity
			WHERE $1 = ANY(pg_blocking_pids(pid)) LIMIT 1), 0)`, blocker).Scan(&pid)
		return pid, err
	}
	// resumeAsked ended without the batch waiting on it: too late for
	// this attempt, if it waited on the batch and ran out of time.
	lost := func() (bool, error) {
		if err := ended(); err != nil {
			return false, err
		}
		if !strings.Contains(logs.String(), "lock timeout") {
			return false, errors.New("resumeAsked ended without waiting on the batch's lock on the Run")
		}
		return false, nil
	}
	var asker int32
	for asker == 0 {
		if asker, err = waitingOn(follower); err != nil {
			return fail(err)
		}
		if asker != 0 {
			break
		}
		select {
		case <-asked:
			return lost()
		case <-ctx.Done():
			return fail(ctx.Err())
		case <-time.After(time.Millisecond):
		}
	}

	close(apply)
	for done := false; !done; {
		select {
		case err := <-applied:
			if err != nil {
				return fail(err)
			}
			done = true
		case <-ctx.Done():
			return fail(ctx.Err())
		case <-time.After(time.Millisecond):
			blocked, err := waitingOn(asker)
			if err != nil {
				return fail(err)
			}
			if blocked == follower {
				return fail(fmt.Errorf("deadlock: the batch (pid %d) waits on resumeAsked (pid %d), which waits on the batch",
					follower, asker))
			}
		}
	}
	if strings.Contains(logs.String(), "deadlock detected") {
		return fail(errors.New("deadlock detected"))
	}
	still, err := waitingOn(follower)
	if err != nil {
		return fail(err)
	}
	if still != asker {
		select {
		case <-asked:
		case <-ctx.Done():
			return fail(ctx.Err())
		}
		return lost()
	}
	keep = true
	if err := ended(); err != nil {
		return false, err
	}
	select {
	case <-asked:
	case <-ctx.Done():
		return false, ctx.Err()
	}
	return true, nil
}
