package phases

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// slowStampOnce makes the first stamp of col under the stream's budget
// (timingStatementTimeout) fail as that budget's statement_timeout fails
// it (57014), at once, so the batch's stamp of col times out whatever the
// machine's speed; later ones, and any under another budget (the late
// write's), run. A sequence counts, as it is not rolled back with the
// stamp's savepoint.
func (w *resumeWorld) slowStampOnce(col string) {
	w.t.Helper()
	w.exec(`CREATE SEQUENCE slow_stamp_seen`)
	w.exec(`GRANT USAGE ON SEQUENCE slow_stamp_seen TO PUBLIC`)
	w.exec(fmt.Sprintf(`CREATE FUNCTION slow_stamp() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
		IF NEW.%[1]s IS DISTINCT FROM OLD.%[1]s AND current_setting('statement_timeout') = '%[2]dms' THEN
			IF nextval('slow_stamp_seen') = 1 THEN RAISE EXCEPTION 'slow' USING ERRCODE = 'query_canceled'; END IF;
		END IF;
		RETURN NEW; END $$`, col, timingStatementTimeout.Milliseconds()))
	w.exec(`CREATE TRIGGER slow_stamp BEFORE UPDATE ON run_resumes FOR EACH ROW EXECUTE FUNCTION slow_stamp()`)
}

// resumedOnHostB records the Run's resume from epoch 1, accepted as
// foreseen, with lux to report it running on host-b once read.
func (w *resumeWorld) resumedOnHostB() {
	w.t.Helper()
	base := time.Now().Add(-time.Minute).UTC()
	w.resume(stoppedOnHost1(base))
	w.lux.set(runningAgain(base, "host-b"), nil)
}

// keepLate sets the Run's late stamps the Syncer keeps to late, as
// batches that could not write them would have left them.
func (w *resumeWorld) keepLate(late ...lateStamp) {
	w.s.lateMu.Lock()
	defer w.s.lateMu.Unlock()
	w.s.late = map[string][]lateStamp{w.run.ID: late}
}

// timedWithBusy fails the test unless resume 2 is timed, once, with its
// busy: in the row and in run.resume.timed, no late stamp landing nowhere.
func (w *resumeWorld) timedWithBusy(logs *logSink) {
	w.t.Helper()
	row := w.row(2)
	if row["timed_at"] == nil || row["busy_at"] == nil {
		w.t.Fatalf("timed_at %v, busy_at %v: want the resume timed with its busy\nlogs:\n%s", row["timed_at"], row["busy_at"], logs)
	}
	timed := w.timed()
	if len(timed) != 1 {
		w.t.Fatalf("%d run.resume.timed, want 1\nlogs:\n%s", len(timed), logs)
	}
	if _, ok := timed[0]["untilBusyMs"]; !ok {
		w.t.Errorf("run.resume.timed has no untilBusyMs: %v\nlogs:\n%s", timed[0], logs)
	}
	if lines := logLines(logs.String(), "landed nowhere"); len(lines) != 0 {
		w.t.Errorf("a late stamp landed nowhere:\n%s", logs)
	}
}

// keptBusy fails the test unless a batch kept late includes a busy_at:
// without it, the test proves nothing.
func (w *resumeWorld) keptBusy(late []lateStamp, logs *logSink) {
	w.t.Helper()
	if !slices.ContainsFunc(late, func(l lateStamp) bool { return l.col == "busy_at" }) {
		w.t.Fatalf("the batch kept %v, want its busy_at: the test proves nothing\nlogs:\n%s", late, logs)
	}
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
				w.resumedOnHostB()
				w.slowStampOnce(col)
				logs := w.captureTimingLogs()

				frames := []lux.Frame{session(2), running(2), busy(2), spoke(2)}
				if together {
					w.followBatch(frames...)
				} else {
					w.follow(frames...)
				}
				w.untilTimed()

				// The stamp of col was deferred: slowed here, or timed out on
				// its own on a loaded machine.
				deferred := logLines(logs.String(), "writing it after the batch")
				if len(logLines(strings.Join(deferred, "\n"), "step="+col)) != 1 {
					t.Fatalf("the stamp of %s was not written after the batch: the test proves nothing\nlogs:\n%s", col, logs)
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
				// The batch's timeout is said, and nothing failed: every line
				// is a Warn that a stamp is written after the batch (col's,
				// and on a loaded machine another column's, which the
				// stream's own budget timed out), once per column.
				got := logs.String()
				lines := logLines(got, "recording a resume's timing", "landed nowhere", "step=")
				seen := map[string]bool{}
				for _, l := range lines {
					step := l[strings.Index(l, "step="):]
					if !strings.Contains(l, "writing it after the batch") || seen[step] {
						t.Errorf("want only one Warn per stamp written after the batch, and nothing else; got:\n%s", got)
						break
					}
					seen[step] = true
				}
				if !allWarn(lines) {
					t.Errorf("a line is not a Warn:\n%s", got)
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
	w.resumedOnHostB()
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
	w.keepLate(lateStamp{"busy_at", 2, time.Now()})
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
	w.resumedOnHostB()
	w.follow(session(2), running(2))
	running, _ := w.row(2)["running_at"].(time.Time)
	w.slowStampOnce("busy_at")
	logs := w.captureTimingLogs()

	// A late stamp an earlier batch kept, for the timing to write.
	w.keepLate(lateStamp{"running_at", 2, running})
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
	w.resumedOnHostB()
	w.follow(session(2), running(2), spoke(2))
	w.untilTimed()
	logs := w.captureTimingLogs()
	w.keepLate(lateStamp{"busy_at", 2, time.Now()})
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
	w.resumedOnHostB()
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
	// Each kept stamp owes its resume to the batch's follow-up, busy_at
	// too: recovery must leave it to the follow-up that carries it. The
	// first batch owes 2 once (the later ones keep nothing); the last owes
	// 2 and 3, each once a batch.
	if owed := s.owed("run_1"); !owed[2] || !owed[3] || len(owed) != 2 {
		t.Errorf("owed %v, want 2 and 3", owed)
	}
	if n := s.owing[owedResume{"run_1", 2}]; n != 2 {
		t.Errorf("2 owed %d times, want twice: the first batch and the last", n)
	}
}

// lux's running state still lands on a resume once it is timed: it trails
// the agent's records, and more than timing reads it. Here a resume is
// timed from its first output alone, and its running state comes after.
func TestRunningAfterTheResumeIsTimedIsStillStamped(t *testing.T) {
	w := newResumeWorld(t)
	w.resumedOnHostB()
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
	w.resumedOnHostB()
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

// A Run's late stamps go once its follower is over, but for those of a
// resume a follow-up is still owed: nothing else would drop them while
// the process runs, and an owed one is still to be timed, by any of the
// Run's follow-ups. Here 2's stamp is owed to no one and goes; 3's is
// owed and stays.
func TestALateStampGoesWithItsFollower(t *testing.T) {
	w := newResumeWorld(t)
	now := time.Now()
	w.keepLate(lateStamp{"busy_at", 2, now}, lateStamp{"busy_at", 3, now})
	w.s.owe(w.run.ID, 3)
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
	if left := w.s.lateStamps(w.run.ID); len(left) != 1 || left[0].epoch != 3 {
		t.Errorf("late stamps %v after the follower ended, want only the owed resume 3's", left)
	}
	w.s.paid(w.run.ID, 3)
	w.s.forgetLate(w.run.ID)
	if left := w.s.lateStamps(w.run.ID); left != nil {
		t.Errorf("late stamps %v once nothing is owed, want none", left)
	}
}

// A follow-up whose every timing failed, its Run no longer followed,
// forgets the stamps it carried: nothing else would. With the Run still
// followed they stay, for its next timing, until the follower ends.
func TestAFailedFollowUpOfAnUnfollowedRunLeavesNoLateStamp(t *testing.T) {
	for _, followed := range []bool{false, true} {
		t.Run(fmt.Sprintf("followed %v", followed), func(t *testing.T) {
			w := newResumeWorld(t)
			w.exec(`INSERT INTO run_resumes (run_id, organization_id, epoch, cause, requested_at, first_output_at)
				VALUES ($1, $2, 2, 'person', now() - interval '1 second', now())`, w.run.ID, w.run.Org)
			w.exec(`CREATE FUNCTION fail_timing() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
				IF NEW.timed_at IS NOT NULL THEN RAISE EXCEPTION 'every timing fails'; END IF;
				RETURN NEW; END $$`)
			w.exec(`CREATE TRIGGER fail_timing BEFORE UPDATE ON run_resumes FOR EACH ROW EXECUTE FUNCTION fail_timing()`)
			logs := w.captureTimingLogs()
			if followed {
				w.s.mu.Lock()
				w.s.following = map[string]*follower{w.run.ID: {}}
				w.s.mu.Unlock()
			}
			// As a batch that kept busy_at leaves it: in the Syncer's copy,
			// and owed to its follow-up.
			tr := &translator{run: w.run}
			tr.deferStamp(w.s, "busy_at", 2, time.Now())
			resumes, late := tr.committed()
			w.s.resumeFollowUp(w.run, resumes, late...)
			if got := len(logLines(logs.String(), "every timing fails")); got != 1 {
				t.Fatalf("%d timings failed, want 1: the test proves nothing\nlogs:\n%s", got, logs)
			}
			left := w.s.lateStamps(w.run.ID)
			if followed && len(left) != 1 {
				t.Errorf("late stamps %v, want the busy_at kept while the Run is followed", left)
			}
			if !followed && left != nil {
				t.Errorf("late stamps %v kept with no follower left", left)
			}
		})
	}
}

// Late stamps a timing had no time left to write are kept for the Run's
// next timing, not forgotten unwritten.
func TestALateStampNotTriedIsKept(t *testing.T) {
	w := newResumeWorld(t)
	kept := []lateStamp{{"busy_at", 2, time.Now()}}
	w.keepLate(kept...)
	done, cancel := context.WithCancel(w.ctx)
	cancel()
	var written func()
	if err := w.s.DB.InOrg(w.ctx, w.run.Org, func(tx pgx.Tx) error {
		written = w.s.writeLateStamps(done, tx, w.run, nil)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	written()
	if left := w.s.lateStamps(w.run.ID); len(left) != 1 {
		t.Errorf("late stamps %v after a write that never ran, want %v kept", left, kept)
	}
}

// A batch that keeps a stamp owes its resume to its own follow-up, busy_at
// included: once the Run's follower is over (forgetLate), that follow-up
// is the only one holding the stamp, so recovery must leave the resume to
// it. Here the follower ended between the batch and its follow-up, and
// recovery runs first: the resume is still timed with its busy.
func TestARecoveryBeforeTheFollowUpLeavesAKeptBusyToIt(t *testing.T) {
	w := newResumeWorld(t)
	w.resumedOnHostB()
	w.follow(session(2), running(2))
	w.slowStampOnce("busy_at")
	logs := w.captureTimingLogs()

	resumes, late := w.commitBatch(busy(2), spoke(2))
	w.keptBusy(late, logs)
	w.s.forgetLate(w.run.ID)
	if err := w.s.timeUnowned(w.ctx, w.run, 0); err != nil {
		t.Fatal(err)
	}
	w.s.resumeFollowUp(w.run, resumes, late...)

	w.timedWithBusy(logs)
}

// A committed batch's kept stamps are its follow-up's to write whatever
// becomes of the Syncer's copy: the follower over (forgetLate) before the
// follow-up runs, the follow-up still writes them, and the event carries
// them.
func TestACommittedBatchsStampOutlivesItsFollower(t *testing.T) {
	w := newResumeWorld(t)
	w.resumedOnHostB()
	w.follow(session(2), running(2))
	w.slowStampOnce("busy_at")
	logs := w.captureTimingLogs()

	resumes, late := w.commitBatch(busy(2), spoke(2))
	w.keptBusy(late, logs)
	w.s.forgetLate(w.run.ID)
	w.s.resumeFollowUp(w.run, resumes, late...)

	w.timedWithBusy(logs)
}

// A stamp a batch kept is still written when another batch's follow-up
// times its resume first, after the follower is over: the Syncer's copy
// outlives the follower while its resume is owed. Here batch A keeps
// busy_at (owing 2 to its follow-up), batch B commits the first output
// (its follow-up owes nothing and carries no stamp), the follower ends,
// and B's follow-up times 2 before A's runs: still with its busy.
func TestAnotherBatchsFollowUpTimesAKeptBusyWithIt(t *testing.T) {
	w := newResumeWorld(t)
	w.resumedOnHostB()
	w.follow(session(2), running(2))
	w.slowStampOnce("busy_at")
	logs := w.captureTimingLogs()

	resA, lateA := w.commitBatch(busy(2))
	resB, lateB := w.commitBatch(spoke(2))
	if !resA[2] || len(lateA) != 1 || lateA[0].col != "busy_at" {
		t.Fatalf("batch A: follow-up %v, kept %v; want 2 read and its busy_at: the test proves nothing\nlogs:\n%s",
			resA, lateA, logs)
	}
	if read, ok := resB[2]; !ok || read || len(lateB) != 0 {
		t.Fatalf("batch B: follow-up %v, kept %v; want 2 timed only, no stamp: the test proves nothing\nlogs:\n%s",
			resB, lateB, logs)
	}
	w.s.forgetLate(w.run.ID)
	w.s.resumeFollowUp(w.run, resB, lateB...)
	w.s.resumeFollowUp(w.run, resA, lateA...)

	w.timedWithBusy(logs)
	if left := w.s.lateStamps(w.run.ID); len(left) != 0 {
		t.Errorf("late stamps kept after they were written: %v", left)
	}
}

// Of two late stamps of one column and resume, the follow-up's own and the
// Syncer's, the earlier is written: the earlier frame's.
func TestTheEarliestLateStampIsWritten(t *testing.T) {
	w := newResumeWorld(t)
	w.resumedOnHostB()
	w.follow(session(2), running(2), spoke(2))
	// Untimed again, its busy still to come.
	w.exec(`UPDATE run_resumes SET timed_at = NULL WHERE run_id = $1`, w.run.ID)
	t1 := time.Now().Add(-10 * time.Second).UTC().Truncate(time.Microsecond)
	t2 := t1.Add(5 * time.Second)
	w.keepLate(lateStamp{"busy_at", 2, t2})
	if err := w.s.timeResumes(w.ctx, w.run, 2, lateStamp{"busy_at", 2, t1}); err != nil {
		t.Fatal(err)
	}
	if got, _ := w.row(2)["busy_at"].(time.Time); !got.Equal(t1) {
		t.Errorf("busy_at %v, want the earlier stamp %v (not %v)", w.row(2)["busy_at"], t1, t2)
	}
}

// A late running_at for a resume already timed is written in a step of
// its own, after the late write proper, which touches only the untimed
// resumes it holds (lockUntimed): roundSyncLanded reads it, and no later
// running frame may come to stamp it. Resume 3's busy lands whether or
// not a batch holds the timed resume 2; 2's running_at lands when its row
// is free, and when it is held only that step fails, said once.
func TestALateRunningOfATimedResumeLeavesTheLateWriteAlone(t *testing.T) {
	for _, held := range []bool{false, true} {
		t.Run(fmt.Sprintf("held %v", held), func(t *testing.T) {
			w := newResumeWorld(t)
			// Short, so a write that did wait on the held resume fails here.
			w.s.resumeBudget = resumeBudget{lock: 100 * time.Millisecond}
			w.resumedOnHostB()
			w.follow(spoke(2))
			w.untilTimed()
			if row := w.row(2); row["timed_at"] == nil || row["running_at"] != nil {
				t.Fatalf("timed_at %v, running_at %v: want it timed without running_at", row["timed_at"], row["running_at"])
			}
			w.exec(`INSERT INTO run_resumes (run_id, organization_id, epoch, cause, requested_at) VALUES ($1, $2, 3, 'person', now())`,
				w.run.ID, w.run.Org)
			logs := w.captureTimingLogs()
			release := func() {}
			if held {
				release = w.lockResume(2)
			}
			now := time.Now().UTC().Truncate(time.Microsecond)
			w.keepLate(lateStamp{"running_at", 2, now}, lateStamp{"busy_at", 3, now})
			if err := w.s.timeUnowned(w.ctx, w.run, 0); err != nil {
				t.Fatal(err)
			}
			release()
			if got := w.row(3)["busy_at"]; got == nil {
				t.Errorf("busy_at of resume 3 not written beside timed resume 2\nlogs:\n%s", logs)
			}
			running, _ := w.row(2)["running_at"].(time.Time)
			lines := logLines(logs.String(), "recording a resume's timing", "landed nowhere")
			if held {
				if !running.IsZero() {
					t.Errorf("running_at %v written on timed resume 2 while a batch held it", running)
				}
				if len(lines) != 1 || !allWarn(lines) || !strings.Contains(lines[0], "step=\"late running_at\"") {
					t.Errorf("want one Warn that the late running_at step failed, and nothing else; got:\n%s", logs)
				}
				return
			}
			if !running.Equal(now) {
				t.Errorf("running_at of timed resume 2 %v, want the late stamp's %v\nlogs:\n%s", w.row(2)["running_at"], now, logs)
			}
			if len(lines) != 0 {
				t.Errorf("want no timing line; got:\n%s", logs)
			}
		})
	}
}

// A late stamp of a frame with no epoch is for the latest resume as the
// frame is applied: here two are untimed, and it is the later one's.
func TestALateStampWithNoEpochIsForTheLatestResume(t *testing.T) {
	w := newResumeWorld(t)
	w.resumedOnHostB()
	w.follow(session(2), running(2))
	w.exec(`INSERT INTO run_resumes (run_id, organization_id, epoch, cause, requested_at) VALUES ($1, $2, 3, 'person', now())`,
		w.run.ID, w.run.Org)
	w.slowStampOnce("busy_at")
	tr := &translator{run: w.run}
	if err := w.s.DB.InOrg(w.ctx, w.run.Org, func(tx pgx.Tx) error {
		tr.stamp(w.ctx, tx, w.s, "busy_at", 0)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(tr.late) != 1 || tr.late[0].epoch != 3 {
		t.Fatalf("kept %v, want the busy_at stamp of resume 3", tr.late)
	}
	if err := w.s.timeUnowned(w.ctx, w.run, 0); err != nil {
		t.Fatal(err)
	}
	if got := w.row(3)["busy_at"]; got == nil {
		t.Errorf("busy_at of resume 3 not written")
	}
	if got := w.row(2)["busy_at"]; got != nil {
		t.Errorf("busy_at %v on resume 2, not the latest", got)
	}
}

// followerGate holds the first follower's first statement, as it starts
// on the pool, until let go, whatever its context: a follower told to stop
// that has not ended yet. It holds every recovery timing (lockUntimed's
// statement, before any late stamp is read) until the test ends, so none
// writes, and forgets, the late stamps the test looks at.
type followerGate struct {
	first     atomic.Bool
	entered   chan struct{}
	letGo     chan struct{}
	testEnded chan struct{}
}

func (g *followerGate) TraceQueryStart(ctx context.Context, _ *pgx.Conn, q pgx.TraceQueryStartData) context.Context {
	switch {
	case strings.HasPrefix(q.SQL, "UPDATE runs SET child_output_generation") && g.first.CompareAndSwap(false, true):
		close(g.entered)
		<-g.letGo
	case strings.HasPrefix(q.SQL, "SELECT 1 FROM run_resumes WHERE run_id = $1 AND timed_at IS NULL"):
		<-g.testEnded
	}
	return ctx
}

func (*followerGate) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

// A follower that ends after a newer one took the Run leaves the Run's
// late stamps alone: they may be the newer one's. Here the older follower
// is told to stop, a newer one starts and keeps a stamp, and only then
// does the older one end.
func TestAnOlderFollowerEndingKeepsTheNewerOnesLateStamps(t *testing.T) {
	w := newResumeWorld(t)
	g := &followerGate{entered: make(chan struct{}), letGo: make(chan struct{}), testEnded: make(chan struct{})}
	w.traced(g)
	letGo := sync.OnceFunc(func() { close(g.letGo) })
	// LIFO: before the traced pool closes, which waits on held statements.
	t.Cleanup(func() {
		letGo()
		close(g.testEnded)
		_ = w.s.unfollowWait(w.ctx, w.run.ID)
	})
	st := newStreamLux(w.lux)
	w.s.Lux = st
	r := w.run
	r.Status, r.LuxState = statusRunning, "resuming"
	w.s.follow(r)
	select {
	case <-g.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("the first follower never started")
	}
	w.s.mu.Lock()
	older := w.s.following[w.run.ID]
	w.s.mu.Unlock()
	w.s.unfollow(w.run.ID)
	w.s.follow(r)
	select {
	case <-st.opened:
	case <-time.After(10 * time.Second):
		t.Fatal("the newer follower never opened the stream")
	}
	// The newer follower's batch keeps a stamp.
	(&translator{run: w.run}).deferStamp(w.s, "busy_at", 2, time.Now())
	letGo()
	select {
	case <-older.done:
	case <-time.After(10 * time.Second):
		t.Fatal("the older follower never ended")
	}
	if left := w.s.lateStamps(w.run.ID); len(left) != 1 {
		t.Errorf("late stamps %v once the older follower ended, want the newer one's kept", left)
	}
}

// A follow-up whose first timing fails keeps its batch's stamps for the
// next resume it times: the failed one's transaction took them back.
// Here the first timing fails, whichever resume it is, and the stamp the
// Syncer forgot is still written by the second.
func TestAFollowUpKeepsItsStampsPastAFailedTiming(t *testing.T) {
	w := newResumeWorld(t)
	for _, epoch := range []int{2, 3} {
		w.exec(`INSERT INTO run_resumes (run_id, organization_id, epoch, cause, requested_at, first_output_at)
			VALUES ($1, $2, $3, 'person', now() - interval '1 second', now())`, w.run.ID, w.run.Org, epoch)
	}
	w.exec(`CREATE SEQUENCE timing_seen`)
	w.exec(`GRANT USAGE ON SEQUENCE timing_seen TO PUBLIC`)
	w.exec(`CREATE FUNCTION fail_timing() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
		IF NEW.timed_at IS NOT NULL AND OLD.timed_at IS NULL AND nextval('timing_seen') = 1 THEN
			RAISE EXCEPTION 'the first timing fails'; END IF;
		RETURN NEW; END $$`)
	w.exec(`CREATE TRIGGER fail_timing BEFORE UPDATE ON run_resumes FOR EACH ROW EXECUTE FUNCTION fail_timing()`)
	logs := w.captureTimingLogs()
	busyAt := time.Now().Add(-500 * time.Millisecond).UTC().Truncate(time.Microsecond)
	w.s.resumeFollowUp(w.run, map[int]bool{2: false, 3: false}, lateStamp{"busy_at", 3, busyAt})
	if got := len(logLines(logs.String(), "the first timing fails")); got != 1 {
		t.Fatalf("%d timings failed, want 1: the test proves nothing\nlogs:\n%s", got, logs)
	}
	if got, _ := w.row(3)["busy_at"].(time.Time); !got.Equal(busyAt) {
		t.Errorf("busy_at of resume 3 %v, want the batch's %v\nlogs:\n%s", w.row(3)["busy_at"], busyAt, logs)
	}
}

// A stamp a batch got in before the late write wins over the late one: it
// is the earlier frame's, or the same frame replayed.
func TestALateStampLeavesAStampAlreadyIn(t *testing.T) {
	w := newResumeWorld(t)
	w.resumedOnHostB()
	w.follow(session(2), running(2), busy(2))
	in, ok := w.row(2)["busy_at"].(time.Time)
	if !ok {
		t.Fatal("no busy_at stamped: the test proves nothing")
	}
	w.keepLate(lateStamp{"busy_at", 2, in.Add(-time.Second)})
	if err := w.s.timeUnowned(w.ctx, w.run, 0); err != nil {
		t.Fatal(err)
	}
	if got, _ := w.row(2)["busy_at"].(time.Time); !got.Equal(in) {
		t.Errorf("busy_at %v, want the batch's %v kept", w.row(2)["busy_at"], in)
	}
}
