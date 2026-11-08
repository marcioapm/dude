package phases

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// slowStampOnce makes the first UPDATE of run_resumes that changes col
// sleep past the stream's statement budget, so the batch's stamp of col
// times out (57014); later ones run at once. A sequence counts, as it is
// not rolled back with the stamp's savepoint.
func (w *resumeWorld) slowStampOnce(col string) {
	w.t.Helper()
	sleep := timingStatementTimeout + 150*time.Millisecond
	w.exec(`CREATE SEQUENCE slow_stamp_seen`)
	w.exec(`GRANT USAGE ON SEQUENCE slow_stamp_seen TO PUBLIC`)
	w.exec(fmt.Sprintf(`CREATE FUNCTION slow_stamp() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
		IF NEW.%[1]s IS DISTINCT FROM OLD.%[1]s AND nextval('slow_stamp_seen') = 1 THEN PERFORM pg_sleep(%.3[2]f); END IF;
		RETURN NEW; END $$`, col, sleep.Seconds()))
	w.exec(`CREATE TRIGGER slow_stamp BEFORE UPDATE ON run_resumes FOR EACH ROW EXECUTE FUNCTION slow_stamp()`)
}

// followBatch applies frames in one batch, as followOutput does when they
// come together, and runs its follow-up once it commits.
func (w *resumeWorld) followBatch(frames ...lux.Frame) {
	w.t.Helper()
	if resumes, late := w.commitBatch(frames...); resumes != nil {
		w.s.resumeFollowUp(w.run, resumes, late...)
	}
}

// commitBatch applies frames in one batch, as followOutput does, and
// returns what committed returns, for the caller to follow up.
func (w *resumeWorld) commitBatch(frames ...lux.Frame) (map[int]bool, []lateStamp) {
	w.t.Helper()
	t, err := w.applyFrames(false, frames...)
	if err != nil {
		w.t.Fatalf("applying the frames failed the Run's batch: %v", err)
	}
	return t.committed()
}

// errBatchFailed fails a batch after its frames, as a failed save would.
var errBatchFailed = errors.New("the batch failed after its frames")

// applyFrames applies frames in one batch; with fail, the batch fails once
// they are applied, and is rolled back. It returns the batch's translator.
func (w *resumeWorld) applyFrames(fail bool, frames ...lux.Frame) (*translator, error) {
	w.t.Helper()
	t := &translator{run: w.run}
	t.run.Status, t.run.LuxState = statusRunning, "resuming"
	return t, w.s.DB.InOrg(w.ctx, w.run.Org, func(tx pgx.Tx) error {
		if err := t.load(w.ctx, tx); err != nil {
			return err
		}
		for _, f := range frames {
			if err := t.apply(w.ctx, tx, w.s, f); err != nil {
				return err
			}
		}
		if fail {
			return errBatchFailed
		}
		return t.save(w.ctx, tx, "", 0)
	})
}

// logLines are the lines of got that carry one of marks.
func logLines(got string, marks ...string) []string {
	var lines []string
	for _, l := range strings.Split(got, "\n") {
		for _, m := range marks {
			if m != "" && strings.Contains(l, m) {
				lines = append(lines, l)
				break
			}
		}
	}
	return lines
}

// allWarn is whether every line is a Warn.
func allWarn(lines []string) bool {
	for _, l := range lines {
		if !strings.Contains(l, "level=WARN") {
			return false
		}
	}
	return true
}

// A stamp the Run's batch could not write in the stream's budget is not
// lost: the agent's first busy, or its first output, comes once a resume
// (its next comes only on its next turn, after the resume was timed), so
// its time is kept and written once the batch commits, before the resume
// is timed. The row has it, at the frame's time on Postgres's clock (not
// later than the first output), and run.resume.timed carries the phases
// it ends: until the agent took its input, and its first words.
func TestAStampTheBatchCouldNotWriteInTimeIsStillTimed(t *testing.T) {
	for _, col := range []string{"busy_at", "first_output_at"} {
		for _, together := range []bool{false, true} {
			name := col + ", a batch per frame"
			if together {
				name = col + ", one batch"
			}
			t.Run(name, func(t *testing.T) {
				w := newResumeWorld(t)
				base := time.Now().Add(-time.Minute).UTC()
				w.resume(stoppedOnHost1(base))
				w.lux.set(runningAgain(base, "host-b"), nil)
				w.slowStampOnce(col)
				logs := w.captureTimingLogs()

				frames := []lux.Frame{session(2), running(2), busy(2), spoke(2)}
				if together {
					w.followBatch(frames...)
				} else {
					w.follow(frames...)
				}
				w.untilTimed()

				var stalled bool
				_ = w.owner.QueryRow(w.ctx, `SELECT is_called FROM slow_stamp_seen`).Scan(&stalled)
				if !stalled {
					t.Fatalf("no stamp of %s was slowed: the test proves nothing", col)
				}
				row := w.row(2)
				running, _ := row["running_at"].(time.Time)
				busyAt, okBusy := row["busy_at"].(time.Time)
				output, okOutput := row["first_output_at"].(time.Time)
				if !okBusy || !okOutput {
					t.Fatalf("busy_at %v, first_output_at %v: want both\nlogs:\n%s", row["busy_at"], row["first_output_at"], logs)
				}
				// Strictly: each is its own frame's time on the database's
				// clock, not the batch's.
				if !running.Before(busyAt) || !busyAt.Before(output) {
					t.Errorf("running_at %v, busy_at %v, first_output_at %v: want them strictly in order", running, busyAt, output)
				}
				timed := w.timed()
				if len(timed) != 1 {
					t.Fatalf("%d run.resume.timed, want 1\nlogs:\n%s", len(timed), logs)
				}
				phases, _ := timed[0]["phases"].(map[string]any)
				for _, k := range []string{"take", "firstOutput"} {
					if _, ok := phases[k]; !ok {
						t.Errorf("run.resume.timed has no phases.%s: %v", k, timed[0])
					}
				}
				for _, k := range []string{"untilBusyMs", "totalMs"} {
					if _, ok := timed[0][k]; !ok {
						t.Errorf("run.resume.timed has no %s: %v", k, timed[0])
					}
				}
				// The batch's timeout is said, once, and nothing failed.
				got := logs.String()
				lines := logLines(got, "recording a resume's timing", "landed nowhere", "step=")
				if len(lines) != 1 || !allWarn(lines) || !strings.Contains(lines[0], "writing it after the batch") ||
					!strings.Contains(lines[0], "step="+col) {
					t.Errorf("want one Warn that the %s stamp is written after the batch, and nothing else; got:\n%s", col, got)
				}
				if left := w.s.lateStamps(w.run.ID); len(left) != 0 {
					t.Errorf("late stamps kept after they were written: %v", left)
				}
			})
		}
	}
}

// Nothing lands on a resume once it is timed: the agent's next turn's busy
// (the first one lost) leaves the timed row alone, and so does a late
// stamp kept for it, which would contradict what run.resume.timed said.
func TestAStampAfterTheResumeIsTimedChangesNothing(t *testing.T) {
	w := newResumeWorld(t)
	base := time.Now().Add(-time.Minute).UTC()
	w.resume(stoppedOnHost1(base))
	w.lux.set(runningAgain(base, "host-b"), nil)
	// Timed without its first busy.
	w.follow(session(2), running(2), spoke(2))
	w.untilTimed()
	if n := len(w.timed()); n != 1 {
		t.Fatalf("%d run.resume.timed, want 1", n)
	}
	before := w.row(2)
	if before["busy_at"] != nil {
		t.Fatalf("busy_at %v before any busy", before["busy_at"])
	}

	// The agent's next turn.
	w.follow(busy(2), spoke(2))
	// A late stamp kept for the resume, written by the Run's next timing.
	w.s.lateMu.Lock()
	w.s.late = map[string][]lateStamp{w.run.ID: {{"busy_at", 2, time.Now()}}}
	w.s.lateMu.Unlock()
	if err := w.s.timeUnowned(w.ctx, w.run, 0); err != nil {
		t.Fatal(err)
	}

	after := w.row(2)
	for col, was := range before {
		if fmt.Sprint(after[col]) != fmt.Sprint(was) {
			t.Errorf("%s moved from %v to %v after the resume was timed", col, was, after[col])
		}
	}
	if n := len(w.timed()); n != 1 {
		t.Errorf("%d run.resume.timed, want 1", n)
	}
}

// onFirstSavepoint runs fn, once, as the first savepoint's set-up
// statement (savepointAt's) starts on the pool, before it runs: in a
// timing, its late write, just after the late stamps were read.
type onFirstSavepoint struct {
	claimed atomic.Bool
	fn      func()
}

func (g *onFirstSavepoint) TraceQueryStart(ctx context.Context, _ *pgx.Conn, q pgx.TraceQueryStartData) context.Context {
	if strings.HasPrefix(q.SQL, "SELECT current_setting('lock_timeout')") && g.claimed.CompareAndSwap(false, true) {
		g.fn()
	}
	return ctx
}

func (*onFirstSavepoint) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

// A timing reads the late stamps kept only once it holds the resume's row:
// a batch that stamps it holds that row until it commits, and keeps what
// it could not write before then. Read before, a batch could keep busy_at
// and commit first_output_at between the read and the timing's UPDATE,
// and the resume be timed without its busy for good: the batch's own
// follow-up finds it timed. Here a timing with a late stamp to write
// (another batch's) is just past reading them when a batch keeps the
// agent's first busy (its stamp too slow) and writes its first output; the
// resume is still timed with its busy, in the row and in the event.
func TestABusyKeptWhileAResumeIsBeingTimedIsTimedWithIt(t *testing.T) {
	w := newResumeWorld(t)
	base := time.Now().Add(-time.Minute).UTC()
	w.resume(stoppedOnHost1(base))
	w.lux.set(runningAgain(base, "host-b"), nil)
	w.follow(session(2), running(2))
	running, _ := w.row(2)["running_at"].(time.Time)
	w.slowStampOnce("busy_at")
	logs := w.captureTimingLogs()

	// A late stamp an earlier batch kept, for the timing to write.
	w.s.lateMu.Lock()
	w.s.late = map[string][]lateStamp{w.run.ID: {{"running_at", 2, running}}}
	w.s.lateMu.Unlock()
	var resumes map[int]bool
	var late []lateStamp
	w.traced(&onFirstSavepoint{fn: func() {
		resumes, late = w.commitBatch(busy(2), spoke(2))
	}})
	if err := w.s.timeResumes(w.ctx, w.run, 2); err != nil {
		t.Fatal(err)
	}
	if resumes == nil {
		t.Fatal("the batch did not run inside the timing: the test proves nothing")
	}
	if len(late) == 0 {
		t.Fatalf("the batch kept no stamp: the test proves nothing\nlogs:\n%s", logs)
	}
	w.s.resumeFollowUp(w.run, resumes, late...)

	row := w.row(2)
	if row["timed_at"] == nil || row["busy_at"] == nil || row["first_output_at"] == nil {
		t.Fatalf("timed_at %v, busy_at %v, first_output_at %v: want the resume timed with its busy\nlogs:\n%s",
			row["timed_at"], row["busy_at"], row["first_output_at"], logs)
	}
	timed := w.timed()
	if len(timed) != 1 {
		t.Fatalf("%d run.resume.timed, want 1\nlogs:\n%s", len(timed), logs)
	}
	phases, _ := timed[0]["phases"].(map[string]any)
	if _, ok := phases["take"]; !ok {
		t.Errorf("run.resume.timed has no phases.take: %v\nlogs:\n%s", timed[0], logs)
	}
	if _, ok := timed[0]["untilBusyMs"]; !ok {
		t.Errorf("run.resume.timed has no untilBusyMs: %v", timed[0])
	}
	if lines := logLines(logs.String(), "landed nowhere"); len(lines) != 0 {
		t.Errorf("a late stamp landed nowhere:\n%s", logs)
	}
}

// A late stamp that lands nowhere, its column still empty, is said: here
// one for a resume already timed without its busy.
func TestALateStampThatLandsNowhereIsSaid(t *testing.T) {
	w := newResumeWorld(t)
	base := time.Now().Add(-time.Minute).UTC()
	w.resume(stoppedOnHost1(base))
	w.lux.set(runningAgain(base, "host-b"), nil)
	w.follow(session(2), running(2), spoke(2))
	w.untilTimed()
	logs := w.captureTimingLogs()
	w.s.lateMu.Lock()
	w.s.late = map[string][]lateStamp{w.run.ID: {{"busy_at", 2, time.Now()}}}
	w.s.lateMu.Unlock()
	if err := w.s.timeUnowned(w.ctx, w.run, 0); err != nil {
		t.Fatal(err)
	}
	lines := logLines(logs.String(), "landed nowhere")
	if len(lines) != 1 || !allWarn(lines) || !strings.Contains(lines[0], "column=busy_at") {
		t.Errorf("want one Warn that the busy_at stamp landed nowhere; got:\n%s", logs)
	}
	if left := w.s.lateStamps(w.run.ID); len(left) != 0 {
		t.Errorf("late stamps kept after they were tried: %v", left)
	}
}

// A batch rolled back keeps none of its late stamps: its frames are read
// again, and the replay's stamps are the ones the resume is timed with.
func TestABatchRolledBackKeepsNoLateStamp(t *testing.T) {
	w := newResumeWorld(t)
	base := time.Now().Add(-time.Minute).UTC()
	w.resume(stoppedOnHost1(base))
	w.lux.set(runningAgain(base, "host-b"), nil)
	w.slowStampOnce("busy_at")
	frames := []lux.Frame{session(2), running(2), busy(2), spoke(2)}

	tr, err := w.applyFrames(true, frames...)
	if !errors.Is(err, errBatchFailed) {
		t.Fatalf("the batch: %v, want it failed", err)
	}
	if len(tr.late) == 0 {
		t.Fatal("the failed batch kept no stamp: the test proves nothing")
	}
	tr.rolledBack(w.s)
	if left := w.s.lateStamps(w.run.ID); left != nil {
		t.Fatalf("late stamps kept after the batch rolled back: %v", left)
	}

	var replayed time.Time
	if err := w.owner.QueryRow(w.ctx, `SELECT clock_timestamp()`).Scan(&replayed); err != nil {
		t.Fatal(err)
	}
	w.followBatch(frames...)
	w.untilTimed()
	timed := w.timed()
	if len(timed) != 1 {
		t.Fatalf("%d run.resume.timed, want 1", len(timed))
	}
	busyAt, ok := w.row(2)["busy_at"].(time.Time)
	if !ok || busyAt.Before(replayed) {
		t.Errorf("busy_at %v, want the replay's (after %v)", w.row(2)["busy_at"], replayed)
	}
	if _, ok := timed[0]["untilBusyMs"]; !ok {
		t.Errorf("run.resume.timed has no untilBusyMs: %v", timed[0])
	}
}

// A Run whose resume stays locked keeps one late stamp per column and
// resume, the earliest, and follows it up once: each contended batch after
// the first adds nothing.
func TestALateStampIsKeptOncePerColumnAndResume(t *testing.T) {
	s := &Syncer{}
	first := time.Now()
	for i := range 3 {
		tr := &translator{run: phaseRun{ID: "run_1"}}
		tr.deferStamp(s, "busy_at", 2, first.Add(time.Duration(i)*time.Second))
		if resumes, late := tr.committed(); (i == 0) != (resumes != nil) || (i == 0) != (len(late) == 1) {
			t.Errorf("batch %d: follow-up %v, kept %v; want one only for the first", i, resumes, late)
		}
	}
	tr := &translator{run: phaseRun{ID: "run_1"}}
	tr.deferStamp(s, "first_output_at", 2, first)
	tr.deferStamp(s, "busy_at", 3, first)
	got := s.lateStamps("run_1")
	if len(got) != 3 || !got[0].at.Equal(first) {
		t.Errorf("kept %v, want busy_at of 2 at %v, first_output_at of 2 and busy_at of 3", got, first)
	}
	if s.owed("run_1")[2] != true || len(s.owed("run_1")) != 1 {
		t.Errorf("owed %v, want the read of 2 once", s.owed("run_1"))
	}
}

// lux's running state still lands on a resume once it is timed: it trails
// the agent's records, and more than timing reads it. Here a resume is
// timed from its first output alone, and its running state comes after.
func TestRunningAfterTheResumeIsTimedIsStillStamped(t *testing.T) {
	w := newResumeWorld(t)
	base := time.Now().Add(-time.Minute).UTC()
	w.resume(stoppedOnHost1(base))
	w.lux.set(runningAgain(base, "host-b"), nil)
	w.follow(spoke(2))
	w.untilTimed()
	if row := w.row(2); row["timed_at"] == nil || row["running_at"] != nil {
		t.Fatalf("timed_at %v, running_at %v: want it timed without running_at", row["timed_at"], row["running_at"])
	}
	w.follow(running(2))
	if row := w.row(2); row["running_at"] == nil {
		t.Errorf("running_at not stamped after the resume was timed")
	}
}

// A late stamp of a frame with no epoch is for the resume that was the
// latest as the frame was applied, not when it is written: a resume asked
// for in between keeps its own columns empty.
func TestALateStampWithNoEpochIsForTheResumeItWasAbout(t *testing.T) {
	w := newResumeWorld(t)
	base := time.Now().Add(-time.Minute).UTC()
	w.resume(stoppedOnHost1(base))
	w.lux.set(runningAgain(base, "host-b"), nil)
	w.follow(session(2), running(2))
	w.slowStampOnce("busy_at")
	tr := &translator{run: w.run}
	if err := w.s.DB.InOrg(w.ctx, w.run.Org, func(tx pgx.Tx) error {
		tr.stamp(w.ctx, tx, w.s, "busy_at", 0)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(tr.late) != 1 {
		t.Fatalf("kept %v, want the busy_at stamp: the test proves nothing", tr.late)
	}
	// A later resume's row, before the stamp is written.
	w.exec(`INSERT INTO run_resumes (run_id, organization_id, epoch, cause, requested_at) VALUES ($1, $2, 3, 'person', now())`,
		w.run.ID, w.run.Org)
	if err := w.s.timeUnowned(w.ctx, w.run, 0); err != nil {
		t.Fatal(err)
	}
	if got := w.row(2)["busy_at"]; got == nil {
		t.Errorf("busy_at of resume 2 not written")
	}
	if got := w.row(3)["busy_at"]; got != nil {
		t.Errorf("busy_at %v on resume 3, asked for after the frame", got)
	}
}

// A Run's late stamps go once its follower is over: nothing else would
// drop them while the process runs.
func TestALateStampGoesWithItsFollower(t *testing.T) {
	w := newResumeWorld(t)
	w.s.lateMu.Lock()
	w.s.late = map[string][]lateStamp{w.run.ID: {{"busy_at", 2, time.Now()}}}
	w.s.lateMu.Unlock()
	st := newStreamLux(w.lux)
	w.s.Lux = st
	r := w.run
	r.Status, r.LuxState = statusRunning, "resuming"
	w.s.follow(r)
	select {
	case <-st.opened:
	case <-time.After(10 * time.Second):
		t.Fatal("the follower never opened the stream")
	}
	if err := w.s.unfollowWait(w.ctx, w.run.ID); err != nil {
		t.Fatal(err)
	}
	if left := w.s.lateStamps(w.run.ID); left != nil {
		t.Errorf("late stamps kept after the follower ended: %v", left)
	}
}
