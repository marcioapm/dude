package phases

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

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
		// to do once resumeAsked has started.
		slow func(w *resumeWorld) (started func())
	}{
		{"held run", func(w *resumeWorld) func() {
			release := sync.OnceFunc(w.holding(`SELECT 1 FROM runs WHERE id = $1 FOR UPDATE`, w.run.ID))
			w.t.Cleanup(release)
			// Let go of the Run well past the stream's lock budget.
			return func() {
				time.Sleep(3 * timingLockTimeout)
				release()
			}
		}},
		// Past resumeAsked's own lock budget too (a second): its first
		// attempt times out, and the one more it makes gets the row in.
		// A literal, so the test builds without resumeLockTimeout.
		{"run held past the resume's budget", func(w *resumeWorld) func() {
			release := sync.OnceFunc(w.holding(`SELECT 1 FROM runs WHERE id = $1 FOR UPDATE`, w.run.ID))
			w.t.Cleanup(release)
			return func() {
				time.Sleep(1300 * time.Millisecond)
				release()
			}
		}},
		{"slow insert", func(w *resumeWorld) func() {
			sleep := timingStatementTimeout + 150*time.Millisecond
			w.exec(fmt.Sprintf(`CREATE FUNCTION slow_resume() RETURNS trigger LANGUAGE plpgsql AS
				$$ BEGIN PERFORM pg_sleep(%.3f); RETURN NEW; END $$`, sleep.Seconds()))
			w.exec(`CREATE TRIGGER slow_resume BEFORE INSERT ON run_resumes FOR EACH ROW EXECUTE FUNCTION slow_resume()`)
			return func() {}
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := newResumeWorld(t)
			logs := w.captureTimingLogs()
			base := time.Now().Add(-time.Minute).UTC()
			started := c.slow(w)

			type answer struct {
				epoch int
				took  time.Duration
			}
			asked := make(chan answer, 1)
			go func() {
				start := time.Now()
				epoch := w.s.resumeAsked(w.ctx, w.run, stoppedOnHost1(base))
				asked <- answer{epoch, time.Since(start)}
			}()
			started()
			var epoch int
			var took time.Duration
			select {
			case a := <-asked:
				epoch, took = a.epoch, a.took
			case <-time.After(30 * time.Second):
				t.Fatalf("resumeAsked did not return\nlogs:\n%s", logs)
			}
			if epoch != 2 {
				t.Fatalf("resumeAsked foresaw epoch %d, want 2", epoch)
			}
			if n := w.count(`SELECT count(*) FROM run_resumes WHERE run_id = $1`); n != 1 {
				t.Fatalf("%d resume rows after resumeAsked took %s on a slow database, want 1\nlogs:\n%s", n, took, logs)
			}
			// resumeAsked waited the database out, past the stream's budget.
			if took < 2*timingLockTimeout {
				t.Errorf("resumeAsked gave up after %s, within the stream's budget", took)
			}

			w.accepted(epoch, lux.Run{})
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
