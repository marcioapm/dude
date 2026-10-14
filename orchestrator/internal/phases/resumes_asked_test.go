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
			// it stopped waiting, or was seen waiting with less than four
			// fifths of that left, before the batch was let apply its
			// frames (a scheduling delay) did not reach the interleaving:
			// its batch is rolled back and it is tried again.
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

// Another transaction holding the Run's row FOR UPDATE past
// timingLockTimeout costs a resume only its timing: resumeAsked gives up
// on its KEY SHARE, records no row and still returns the epoch foreseen;
// whilePaused then asks lux, and takes the Run back to running once the
// holder lets go.
func TestAResumeBehindAHeldRunSkipsItsTimingAndStillResumes(t *testing.T) {
	w := newResumeWorld(t)
	logs := &logSink{}
	w.s.Log = slog.New(slog.NewTextHandler(logs, nil))
	base := time.Now().Add(-time.Minute).UTC()
	w.resumable()
	w.lux.set(stoppedOnHost1(base), nil)
	ctx, cancel := context.WithTimeout(w.ctx, 30*time.Second)
	release := sync.OnceFunc(w.holding(`SELECT 1 FROM runs WHERE id = $1 FOR UPDATE`, w.run.ID))
	var workers sync.WaitGroup
	// LIFO: runs before holding's own cleanup, so no worker still waits on
	// the holder when it is joined.
	t.Cleanup(func() {
		cancel()
		release()
		workers.Wait()
	})
	timedOut := func(what string) {
		t.Helper()
		if got := logs.String(); !strings.Contains(got, "step=resume") || !strings.Contains(got, "lock timeout") {
			t.Errorf("%s: want the resume step's lock timeout logged, got:\n%s", what, got)
		}
		if n := w.count(`SELECT count(*) FROM run_resumes WHERE run_id = $1`); n != 0 {
			t.Errorf("%s: %d resume rows recorded behind the held Run, want none", what, n)
		}
	}

	asked := make(chan int, 1)
	workers.Add(1)
	go func() {
		defer workers.Done()
		asked <- w.s.resumeAsked(ctx, w.run, stoppedOnHost1(base))
	}()
	select {
	case epoch := <-asked:
		if epoch != 2 {
			t.Errorf("resumeAsked foresaw epoch %d, want 2", epoch)
		}
	case <-ctx.Done():
		t.Fatalf("resumeAsked did not return behind the held Run\nlogs:\n%s", logs)
	}
	timedOut("resumeAsked")

	logs.Reset()
	answering := &releasingLux{placementLux: w.lux, release: release}
	w.s.Lux = answering
	r := w.run
	r.Resumable = true
	paused := make(chan error, 1)
	workers.Add(1)
	go func() {
		defer workers.Done()
		_, err := w.s.whilePaused(ctx, r)
		paused <- err
	}()
	select {
	case err := <-paused:
		if err != nil {
			t.Fatalf("whilePaused: %v\nlogs:\n%s", err, logs)
		}
	case <-ctx.Done():
		t.Fatalf("whilePaused did not return\nlogs:\n%s", logs)
	}
	if n := answering.resumes(); n != 1 {
		t.Fatalf("lux was asked to resume %d times, want 1", n)
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'running' AND lux_state = 'resuming'`); n != 1 {
		t.Errorf("the Run was not taken back to running once the holder let go")
	}
	timedOut("whilePaused")
}

// releasingLux answers a resume as resuming, on the stopped epoch, after
// letting go of the Run's row held elsewhere: resumeAsked has returned by
// then, so it ran with the row held throughout.
type releasingLux struct {
	*placementLux
	release func()
	mu      sync.Mutex
	asked   int
}

func (f *releasingLux) Resume(context.Context, string, lux.ResumeInput) (lux.Run, error) {
	f.mu.Lock()
	f.asked++
	f.mu.Unlock()
	f.release()
	return lux.Run{ID: "lrun_1", State: "resuming", Epoch: 1}, nil
}

func (f *releasingLux) resumes() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.asked
}

// stampWhileTheResumeWaits is one attempt. reached is false when
// resumeAsked stopped waiting on the batch, or was seen waiting too late,
// before the batch was let apply its frames; the batch is then rolled
// back. Once let go, the batch commits its frames at once. resumeAsked
// waiting on the batch while holding the row the batch stamps, the batch
// waiting on resumeAsked, and resumeAsked failing a timing statement once
// the batch was let go (a cycle its lock_timeout broke, sampled or not)
// are returned as errors, never as an attempt to try again.
func (w *resumeWorld) stampWhileTheResumeWaits(ctx context.Context, workers *sync.WaitGroup, logs *logSink,
	base time.Time) (reached bool, err error) {
	tr := &translator{run: w.run}
	tr.run.Status, tr.run.LuxState = statusRunning, "resuming"
	warm := &translator{run: tr.run}
	if err := w.s.DB.InOrg(ctx, w.run.Org, func(tx pgx.Tx) error {
		return errors.Join(tr.load(ctx, tx), warm.load(ctx, tx))
	}); err != nil {
		return false, err
	}
	frames := []lux.Frame{session(2), running(2), busy(2), spoke(2)}
	applyAll := func(tr *translator, tx pgx.Tx) error {
		for _, f := range frames {
			if err := tr.apply(ctx, tx, w.s, f); err != nil {
				return err
			}
		}
		return tr.save(ctx, tx, "", 0)
	}

	locked := make(chan int32, 1)
	apply := make(chan struct{})
	// Closed on every return, so a batch not let go never outlives the
	// attempt: it is rolled back.
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
			// The same frames once, undone, before resumeAsked starts: the
			// connection has their statements prepared, so the batch let
			// go below spends its time applying, not preparing.
			sp, err := tx.Begin(ctx)
			if err != nil {
				return err
			}
			if err := errors.Join(applyAll(warm, sp), sp.Rollback(ctx)); err != nil {
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
			return applyAll(tr, tx)
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
	// The batch's transaction has ended, committed or rolled back, before
	// the attempt returns.
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
	untilAsked := func() error {
		select {
		case <-asked:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	// The backend waiting on the batch's locks, 0 when none is.
	waitingOnTheBatch := func() (int32, error) {
		var pid int32
		err := w.owner.QueryRow(ctx, `SELECT COALESCE((SELECT pid FROM pg_stat_activity
			WHERE $1 = ANY(pg_blocking_pids(pid)) LIMIT 1), 0)`, follower).Scan(&pid)
		return pid, err
	}
	// resumeAsked ended, or is left to, before the batch was let apply its
	// frames, so before the batch could wait on it: too late for this
	// attempt, if it waited on the batch and ran out of time.
	lost := func(why string) (bool, error) {
		w.t.Logf("before the batch was let go, %s", why)
		if err := errors.Join(untilAsked(), ended()); err != nil {
			return false, err
		}
		if !strings.Contains(logs.String(), "lock timeout") {
			return false, errors.New("resumeAsked ended without waiting on the batch's lock on the Run")
		}
		return false, nil
	}
	var asker int32
	for asker == 0 {
		if asker, err = waitingOnTheBatch(); err != nil {
			return fail(err)
		}
		if asker != 0 {
			break
		}
		select {
		case <-asked:
			return lost("resumeAsked ended without being seen waiting")
		case <-ctx.Done():
			return fail(ctx.Err())
		case <-time.After(time.Millisecond):
		}
	}

	// How long resumeAsked has waited, by the database's clock, and whether
	// it holds epoch 2's resume row (its xmax one of resumeAsked's
	// (sub)transactions): the half of the cycle the batch would wait on.
	// Blocked, resumeAsked takes no lock until it is let go, so this is
	// read at a barrier, not sampled. pg_locks.waitstart is NULL for a
	// moment after a wait begins: until it is set, the age is unknown, and
	// read again.
	var waitedMs *float64
	var holdsRow bool
	for waitedMs == nil {
		if err := w.owner.QueryRow(ctx, `SELECT
			(SELECT max(EXTRACT(EPOCH FROM clock_timestamp() - waitstart) * 1000) FROM pg_locks
				WHERE pid = $1 AND NOT granted),
			EXISTS (SELECT 1 FROM run_resumes rr JOIN pg_locks l ON l.locktype = 'transactionid'
				AND l.transactionid = rr.xmax AND l.granted AND l.pid = $1
				WHERE rr.run_id = $2 AND rr.epoch = 2)`, asker, w.run.ID).Scan(&waitedMs, &holdsRow); err != nil {
			return fail(err)
		}
		if waitedMs != nil {
			break
		}
		select {
		case <-asked:
			return lost("resumeAsked ended before its wait's start was known")
		case <-ctx.Done():
			return fail(ctx.Err())
		case <-time.After(time.Millisecond):
		}
	}
	if holdsRow {
		return fail(fmt.Errorf("resumeAsked (pid %d) waits on the batch's lock on the Run holding the resume row the batch stamps",
			asker))
	}
	// Seen late, too little of resumeAsked's lock_timeout is left for the
	// batch's frames: not let go, the batch waits for resumeAsked to time
	// out and the attempt is tried again.
	waited := time.Duration(*waitedMs * float64(time.Millisecond))
	if waited > timingLockTimeout/5 {
		return lost(fmt.Sprintf("resumeAsked was seen waiting only after %s", waited))
	}

	start := time.Now()
	close(apply)
	for done := false; !done; {
		select {
		case err := <-followed:
			if err != nil {
				return false, errors.Join(err, untilAsked())
			}
			done = true
		case <-ctx.Done():
			return fail(ctx.Err())
		// Sparse enough to leave the batch the database's CPU.
		case <-time.After(5 * time.Millisecond):
			var waits bool
			if err := w.owner.QueryRow(ctx, `SELECT $2::int = ANY(pg_blocking_pids($1))`, follower, asker).Scan(&waits); err != nil {
				return fail(err)
			}
			if waits {
				return fail(fmt.Errorf("deadlock: the batch (pid %d) waits on resumeAsked (pid %d), which waits on the batch",
					follower, asker))
			}
		}
	}
	committed := time.Since(start)
	if err := untilAsked(); err != nil {
		return false, err
	}
	// The batch was let go: resumeAsked failing a statement now, a lock
	// timeout included, is a failure, whether or not a reverse wait was
	// sampled.
	if got := logs.String(); got != "" {
		return false, fmt.Errorf("resumeAsked failed a timing statement after the batch was let go, having waited %s; "+
			"the batch committed %s later (lock_timeout %s)", waited, committed, timingLockTimeout)
	}
	return true, nil
}
