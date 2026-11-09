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
// (a follower batch, say: lock_timeout, 55P03), behind a Run held past
// resumeAsked's own lock budget, and behind an insert the database times
// out once (a checkpoint, say: statement_timeout, 57014), the row is made,
// trying once more where it must, and the resume is timed once its frames
// come.
//
// Sequenced on the database and on resumeAsked's own log, not on sleeps:
// each case's budget (Syncer.resumeBudget) is wide enough that nothing
// but the step the case makes slow can time out, however loaded the
// machine.
func TestAResumeOnASlowDatabaseIsStillTimed(t *testing.T) {
	for _, c := range []struct {
		name   string
		budget resumeBudget
		// slow makes the database slow for resumeAsked, and returns what
		// to do once resumeAsked has started; an error fails the test.
		slow func(w *resumeWorld, logs *logSink) (started func(returned <-chan struct{}, logs *logSink) error)
		// How many times resumeAsked tries again.
		retries int
		// resumeAsked takes at least this long: it waited the database out.
		minTook time.Duration
	}{
		// Past the stream's lock budget, within resumeAsked's: the Run is
		// let go only once resumeAsked was seen waiting on it for three
		// times timingLockTimeout, on the database's clock. resumeAsked's
		// lock and statement budgets (a lock wait counts toward both) are
		// far past that, so it never needs a second try; a resumeAsked
		// under the stream's budget would time out twice.
		{"held run", resumeBudget{lock: 30 * time.Second, statement: 30 * time.Second, whole: time.Minute},
			func(w *resumeWorld, _ *logSink) func(<-chan struct{}, *logSink) error {
				holder, release := w.holdingWithPid(`SELECT 1 FROM runs WHERE id = $1 FOR UPDATE`, w.run.ID)
				return func(returned <-chan struct{}, logs *logSink) error {
					if err := w.waitedOn(holder, 3*timingLockTimeout, returned); err != nil {
						return fmt.Errorf("%w\nlogs:\n%s", err, logs)
					}
					release()
					return nil
				}
			}, 0, 3 * timingLockTimeout},
		// Past resumeAsked's own lock budget too: its first attempt times
		// out, the Run held throughout, and the Run is let go as resumeAsked
		// logs that it tries again: on its own goroutine, before the second
		// attempt starts, so that attempt waits on nothing. With no second
		// attempt, resumeAsked returns without the line or the row, and
		// this fails.
		{"run held past the resume's budget", resumeBudget{whole: time.Minute},
			func(w *resumeWorld, logs *logSink) func(<-chan struct{}, *logSink) error {
				holder, release := w.holdingWithPid(`SELECT 1 FROM runs WHERE id = $1 FOR UPDATE`, w.run.ID)
				logs.onLine = func(line string) {
					if strings.Contains(line, retried) {
						release()
					}
				}
				return func(returned <-chan struct{}, logs *logSink) error {
					if _, err := w.blockedBy(holder, time.Time{}, returned); err != nil {
						return fmt.Errorf("the first attempt: %w\nlogs:\n%s", err, logs)
					}
					return nil
				}
			}, 1, resumeLockTimeout},
		// The database times the first insert out (57014), and the one
		// more it tries gets the row in. Raised by the insert itself, not
		// slept into, and the whole call's bound wide: the retry has its
		// statements' full budget whatever the load.
		{"slow insert", resumeBudget{whole: time.Minute}, func(w *resumeWorld, _ *logSink) func(<-chan struct{}, *logSink) error {
			w.timesOutFirstInsert()
			return func(<-chan struct{}, *logSink) error { return nil }
		}, 1, 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := newResumeWorld(t)
			w.s.resumeBudget = c.budget
			logs := w.captureTimingLogs()
			base := time.Now().Add(-time.Minute).UTC()
			started := c.slow(w, logs)

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
			case <-time.After(2 * time.Minute):
				t.Fatalf("resumeAsked did not return\nlogs:\n%s", logs)
			}
			if a.epoch != 2 {
				t.Fatalf("resumeAsked foresaw epoch %d, want 2", a.epoch)
			}
			if n := w.count(`SELECT count(*) FROM run_resumes WHERE run_id = $1`); n != 1 {
				t.Fatalf("%d resume rows after resumeAsked took %s on a slow database, want 1\nlogs:\n%s", n, a.took, logs)
			}
			if a.took < c.minTook {
				t.Errorf("resumeAsked returned after %s, want it to have waited the database out (%s)", a.took, c.minTook)
			}
			// The retry's Warn line, once per retry, and nothing else.
			got := logs.String()
			lines := logLines(got, "recording a resume's timing", "step=")
			if n := len(logLines(got, retried)); n != c.retries || len(lines) != c.retries || !allWarn(lines) ||
				len(logLines(got, "step=resume")) != c.retries {
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
			if lines := logLines(logs.String(), "recording a resume's timing", "step="); len(lines) != 0 {
				t.Errorf("a timing statement failed:\n%s", logs)
			}
		})
	}
}

// timesOutFirstInsert makes the first insert into run_resumes fail as
// Postgres's statement_timeout fails it (57014), at once; later ones run.
// A sequence counts, as it is not rolled back with the insert.
func (w *resumeWorld) timesOutFirstInsert() {
	w.t.Helper()
	w.exec(`CREATE SEQUENCE slow_insert_seen`)
	w.exec(`GRANT USAGE ON SEQUENCE slow_insert_seen TO PUBLIC`)
	w.exec(`CREATE FUNCTION slow_resume() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
		IF nextval('slow_insert_seen') = 1 THEN RAISE EXCEPTION 'slow' USING ERRCODE = 'query_canceled'; END IF;
		RETURN NEW; END $$`)
	w.exec(`CREATE TRIGGER slow_resume BEFORE INSERT ON run_resumes FOR EACH ROW EXECUTE FUNCTION slow_resume()`)
}

// slowInserts makes every insert into run_resumes sleep for sleep.
func (w *resumeWorld) slowInserts(sleep time.Duration) {
	w.t.Helper()
	w.exec(fmt.Sprintf(`CREATE FUNCTION slow_resume() RETURNS trigger LANGUAGE plpgsql AS
		$$ BEGIN PERFORM pg_sleep(%.3f); RETURN NEW; END $$`, sleep.Seconds()))
	w.exec(`CREATE TRIGGER slow_resume BEFORE INSERT ON run_resumes FOR EACH ROW EXECUTE FUNCTION slow_resume()`)
}

// A database too slow for every attempt costs the resume path no more than
// resumeAsked's whole budget, both attempts together: the resume goes on
// untimed, and says so. Here each statement may take 3s, the whole call
// half a second, and every insert sleeps 10s: bounded as a whole,
// resumeAsked returns in about half a second; bounded only per statement,
// it would take two statement timeouts, 6s, far past the 2s of slack
// given for a loaded machine.
func TestAResumeOnADatabaseTooSlowForItIsBoundedAsAWhole(t *testing.T) {
	const slack = 2 * time.Second
	w := newResumeWorld(t)
	w.s.resumeBudget = resumeBudget{lock: 3 * time.Second, statement: 3 * time.Second, whole: 500 * time.Millisecond}
	logs := w.captureTimingLogs()
	w.slowInserts(10 * time.Second)
	start := time.Now()
	epoch := w.s.resumeAsked(w.ctx, w.run, stoppedOnHost1(time.Now().Add(-time.Minute).UTC()))
	took := time.Since(start)
	if epoch != 2 {
		t.Errorf("resumeAsked foresaw epoch %d, want 2", epoch)
	}
	if took > w.s.resumeBudget.whole+slack {
		t.Errorf("resumeAsked took %s, want within its bound of %s (and %s of slack)", took, w.s.resumeBudget.whole, slack)
	}
	if n := w.count(`SELECT count(*) FROM run_resumes WHERE run_id = $1`); n != 0 {
		t.Errorf("%d resume rows, want none", n)
	}
	got := logs.String()
	retries := len(logLines(got, retried))
	lines := logLines(got, "recording a resume's timing", "step=")
	if retries > 1 || len(lines) != retries+1 || !allWarn(lines) ||
		len(logLines(got, "recording a resume's timing failed")) != 1 {
		t.Errorf("want at most one Warn that it tries again, one that it failed, and nothing else; got:\n%s", got)
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

// waitedOn waits until a backend has waited on holder's locks for at least
// d, on Postgres's clock. It fails if resumeAsked returns first, or after
// 30s.
func (w *resumeWorld) waitedOn(holder int32, d time.Duration, returned <-chan struct{}) error {
	ctx, cancel := context.WithTimeout(w.ctx, 30*time.Second)
	defer cancel()
	for {
		var waited bool
		if err := w.owner.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_stat_activity a
			JOIN pg_locks l ON l.pid = a.pid AND NOT l.granted
			WHERE $1::int = ANY(pg_blocking_pids(a.pid)) AND l.waitstart <= clock_timestamp() - make_interval(secs => $2))`,
			holder, d.Seconds()).Scan(&waited); err != nil {
			return err
		}
		if waited {
			return nil
		}
		select {
		case <-returned:
			return fmt.Errorf("resumeAsked returned before it waited %s on the holder (pid %d)", d, holder)
		case <-ctx.Done():
			return fmt.Errorf("nothing waited %s on the holder (pid %d): %w", d, holder, ctx.Err())
		case <-time.After(5 * time.Millisecond):
		}
	}
}
