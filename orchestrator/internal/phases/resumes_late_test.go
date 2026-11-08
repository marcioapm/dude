package phases

import (
	"fmt"
	"strings"
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
	t := &translator{run: w.run}
	t.run.Status, t.run.LuxState = statusRunning, "resuming"
	if err := w.s.DB.InOrg(w.ctx, w.run.Org, func(tx pgx.Tx) error {
		if err := t.load(w.ctx, tx); err != nil {
			return err
		}
		for _, f := range frames {
			if err := t.apply(w.ctx, tx, w.s, f); err != nil {
				return err
			}
		}
		return t.save(w.ctx, tx, "", 0)
	}); err != nil {
		w.t.Fatalf("applying the frames failed the Run's batch: %v", err)
	}
	if resumes := t.committed(); resumes != nil {
		w.s.resumeFollowUp(w.run, resumes)
	}
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

				var stalled int
				_ = w.owner.QueryRow(w.ctx, `SELECT last_value FROM slow_stamp_seen`).Scan(&stalled)
				if stalled < 1 {
					t.Fatalf("no stamp of %s was slowed: the test proves nothing", col)
				}
				row := w.row(2)
				running, _ := row["running_at"].(time.Time)
				busyAt, okBusy := row["busy_at"].(time.Time)
				output, okOutput := row["first_output_at"].(time.Time)
				if !okBusy || !okOutput {
					t.Fatalf("busy_at %v, first_output_at %v: want both\nlogs:\n%s", row["busy_at"], row["first_output_at"], logs)
				}
				if busyAt.Before(running) || output.Before(busyAt) {
					t.Errorf("running_at %v, busy_at %v, first_output_at %v: want them in order", running, busyAt, output)
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
				if strings.Count(got, "\n") != 1 || !strings.Contains(got, "writing it after the batch") ||
					!strings.Contains(got, "step="+col) || !strings.Contains(got, "level=WARN") {
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
