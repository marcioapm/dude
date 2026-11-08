package phases

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// retried is the line resumeAsked logs as its first attempt times out.
const retried = "recording a resume's timing timed out; trying again"

// askedAnswer is what resumeAsked returned, and how long it took.
type askedAnswer struct {
	epoch int
	took  time.Duration
}

// A slow database costs a resume no timing. resumeAsked makes the only
// run_resumes row a resume ever gets: rolled back by a lock wait or a
// statement past the stream's tight budget (timingLockTimeout,
// timingStatementTimeout), nothing would make it again and the resume
// would never be timed. Behind a Run held elsewhere past timingLockTimeout
// (a follower batch, say: lock_timeout, 55P03), and behind an insert
// slower than timingStatementTimeout (a checkpoint, say: statement_timeout,
// 57014), and behind a Run held past resumeAsked's own lock budget, the
// row is made, and the resume is timed once its frames come.
func TestAResumeOnASlowDatabaseIsStillTimed(t *testing.T) {
	for _, c := range []struct {
		name string
		// slow makes the database slow for resumeAsked, and returns what
		// to do once resumeAsked has started; an error fails the test.
		slow func(w *resumeWorld) (started func(returned <-chan struct{}, logs *logSink) error)
		// How many times resumeAsked tries again.
		retries int
	}{
		{"held run", func(w *resumeWorld) func(<-chan struct{}, *logSink) error {
			release := sync.OnceFunc(w.holding(`SELECT 1 FROM runs WHERE id = $1 FOR UPDATE`, w.run.ID))
			w.t.Cleanup(release)
			// Let go of the Run well past the stream's lock budget.
			return func(<-chan struct{}, *logSink) error {
				time.Sleep(3 * timingLockTimeout)
				release()
				return nil
			}
		}, 0},
		// Past resumeAsked's own lock budget too (a second): its first
		// attempt times out, and the one more it makes gets the row in.
		// Sequenced on the database, not on sleeps: the Run is let go only
		// once resumeAsked was seen waiting on it, its first attempt timed
		// out, and its second attempt was seen waiting on it afresh. With
		// no second attempt, resumeAsked returns first, and this fails.
		{"run held past the resume's budget", func(w *resumeWorld) func(<-chan struct{}, *logSink) error {
			holder, release := w.holdingWithPid(`SELECT 1 FROM runs WHERE id = $1 FOR UPDATE`, w.run.ID)
			return func(returned <-chan struct{}, logs *logSink) error {
				first, err := w.blockedBy(holder, time.Time{}, returned)
				if err != nil {
					return fmt.Errorf("the first attempt: %w\nlogs:\n%s", err, logs)
				}
				for !strings.Contains(logs.String(), retried) {
					select {
					case <-returned:
						return fmt.Errorf("resumeAsked returned without logging a retry (%q)\nlogs:\n%s", retried, logs)
					case <-time.After(time.Millisecond):
					}
				}
				if _, err := w.blockedBy(holder, first, returned); err != nil {
					return fmt.Errorf("the second attempt: %w\nlogs:\n%s", err, logs)
				}
				release()
				return nil
			}
		}, 1},
		{"slow insert", func(w *resumeWorld) func(<-chan struct{}, *logSink) error {
			sleep := timingStatementTimeout + 150*time.Millisecond
			w.exec(fmt.Sprintf(`CREATE FUNCTION slow_resume() RETURNS trigger LANGUAGE plpgsql AS
				$$ BEGIN PERFORM pg_sleep(%.3f); RETURN NEW; END $$`, sleep.Seconds()))
			w.exec(`CREATE TRIGGER slow_resume BEFORE INSERT ON run_resumes FOR EACH ROW EXECUTE FUNCTION slow_resume()`)
			return func(<-chan struct{}, *logSink) error { return nil }
		}, 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := newResumeWorld(t)
			logs := w.captureTimingLogs()
			base := time.Now().Add(-time.Minute).UTC()
			started := c.slow(w)

			// Buffered, so the worker never blocks on a test that stopped
			// reading; joined on every return, t.Fatalf's included, before
			// the holders' own cleanups (LIFO) let go of the database.
			asked := make(chan askedAnswer, 1)
			returned := make(chan struct{})
			var workers sync.WaitGroup
			t.Cleanup(workers.Wait)
			workers.Add(1)
			go func() {
				defer workers.Done()
				defer close(returned)
				start := time.Now()
				epoch := w.s.resumeAsked(w.ctx, w.run, stoppedOnHost1(base))
				asked <- askedAnswer{epoch, time.Since(start)}
			}()
			if err := started(returned, logs); err != nil {
				t.Fatal(err)
			}
			var a askedAnswer
			select {
			case a = <-asked:
			case <-time.After(30 * time.Second):
				t.Fatalf("resumeAsked did not return\nlogs:\n%s", logs)
			}
			if a.epoch != 2 {
				t.Fatalf("resumeAsked foresaw epoch %d, want 2", a.epoch)
			}
			if n := w.count(`SELECT count(*) FROM run_resumes WHERE run_id = $1`); n != 1 {
				t.Fatalf("%d resume rows after resumeAsked took %s on a slow database, want 1\nlogs:\n%s", n, a.took, logs)
			}
			// resumeAsked waited the database out, past the stream's budget,
			// and within its own bound.
			if a.took < 2*timingLockTimeout || a.took > resumeAskedTimeout+time.Second {
				t.Errorf("resumeAsked returned after %s, want past the stream's budget and within its own %s",
					a.took, resumeAskedTimeout)
			}
			// The retry's Warn line, once per retry, and nothing else.
			got := logs.String()
			if n := strings.Count(got, retried); n != c.retries || strings.Count(got, "\n") != c.retries ||
				c.retries > 0 && !strings.Contains(got, "level=WARN") || c.retries > 0 && !strings.Contains(got, "step=resume") {
				t.Errorf("want %d Warn %q lines, with step=resume, and nothing else; got:\n%s", c.retries, retried, got)
			}
			logs.Reset()

			w.accepted(a.epoch, lux.Run{})
			w.lux.set(runningAgain(base, "host-b"), nil)
			w.follow(session(2), running(2), busy(2), spoke(2))
			w.untilTimed()
			if n := len(w.timed()); n != 1 {
				t.Errorf("%d run.resume.timed, want 1\nlogs:\n%s", n, logs)
			}
			if got := logs.String(); got != "" {
				t.Errorf("a timing statement failed:\n%s", got)
			}
		})
	}
}

// holdingWithPid is holding, on a connection of its own, with that
// backend's pid; let go on the returned func or at the test's end.
func (w *resumeWorld) holdingWithPid(sql string, args ...any) (pid int32, release func()) {
	w.t.Helper()
	other, err := pgx.Connect(w.ctx, w.owner.Config().ConnString())
	if err != nil {
		w.t.Fatal(err)
	}
	w.t.Cleanup(func() { _ = other.Close(w.ctx) })
	tx, err := other.Begin(w.ctx)
	if err != nil {
		w.t.Fatal(err)
	}
	w.t.Cleanup(func() { _ = tx.Rollback(w.ctx) })
	if err := tx.QueryRow(w.ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
		w.t.Fatal(err)
	}
	if _, err := tx.Exec(w.ctx, sql, args...); err != nil {
		w.t.Fatal(err)
	}
	release = sync.OnceFunc(func() {
		_ = tx.Rollback(w.ctx)
		_ = other.Close(w.ctx)
	})
	w.t.Cleanup(release)
	return pid, release
}

// blockedBy waits until a backend waits on holder's locks in a wait that
// began after since, and returns when it began, on Postgres's clock. It
// fails if resumeAsked returns first, or after 10s.
func (w *resumeWorld) blockedBy(holder int32, since time.Time, returned <-chan struct{}) (time.Time, error) {
	ctx, cancel := context.WithTimeout(w.ctx, 10*time.Second)
	defer cancel()
	for {
		var began *time.Time
		if err := w.owner.QueryRow(ctx, `SELECT max(l.waitstart) FROM pg_stat_activity a
			JOIN pg_locks l ON l.pid = a.pid AND NOT l.granted
			WHERE $1::int = ANY(pg_blocking_pids(a.pid)) AND l.waitstart > $2`, holder, since).Scan(&began); err != nil {
			return time.Time{}, err
		}
		if began != nil {
			return *began, nil
		}
		select {
		case <-returned:
			return time.Time{}, fmt.Errorf("resumeAsked returned before a wait on the holder (pid %d) began after %v", holder, since)
		case <-ctx.Done():
			return time.Time{}, fmt.Errorf("no wait on the holder (pid %d) began after %v: %w", holder, since, ctx.Err())
		case <-time.After(time.Millisecond):
		}
	}
}
