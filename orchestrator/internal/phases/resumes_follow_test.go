package phases

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/forge"
	"github.com/marciomartins/dude/orchestrator/internal/ledger"
	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// streamLux is lux streaming a Run's output from a channel, and answering
// Get as placementLux does.
type streamLux struct {
	*placementLux
	frames chan lux.Frame
	// Output was called: the stream is open.
	opened chan struct{}
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

// holding runs sql in a transaction on another connection, holding its
// locks until the returned func is called or the test ends. Each resource
// is let go at the test's end from when it is acquired, so a failed step
// leaks nothing; letting go twice is a no-op.
func (w *resumeWorld) holding(sql string, args ...any) func() {
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
	if _, err := tx.Exec(w.ctx, sql, args...); err != nil {
		w.t.Fatal(err)
	}
	released := false
	return func() {
		if !released {
			released = true
			_ = tx.Rollback(w.ctx)
			_ = other.Close(w.ctx)
		}
	}
}

// lockResume holds the resume row's lock from another connection.
func (w *resumeWorld) lockResume(epoch int) func() {
	w.t.Helper()
	return w.holding(`SELECT 1 FROM run_resumes WHERE run_id = $1 AND epoch = $2 FOR UPDATE`, w.run.ID, epoch)
}

// whilePaused runs the real whilePaused on w's Run, resumable.
func (w *resumeWorld) whilePaused() {
	w.t.Helper()
	r := w.run
	r.Resumable = true
	if _, err := w.s.whilePaused(w.ctx, r); err != nil {
		w.t.Fatal(err)
	}
}

// A resume row locked elsewhere holds up nothing of the Run: the batch
// carrying lux's running state and the agent's first busy commits its
// frames and cursor at once, without the timing it could not write, and so
// does an output batch while it is still locked. A batch after the lock is
// gone looks for the first output again, and records it.
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
	if luxState, busyAt := w.runState(); luxState != "running" || busyAt == nil {
		t.Errorf("the Run's own state was not recorded: lux_state %q busy %v", luxState, busyAt)
	}
	st.frames <- cursorFrame(spoke(2), "locked-output")
	w.committed("locked-output", 5*time.Second)
	release()
	if row := w.row(2); row["running_at"] != nil || row["busy_at"] != nil || row["first_output_at"] != nil {
		t.Errorf("timing written though its row was locked: running %v busy %v first output %v",
			row["running_at"], row["busy_at"], row["first_output_at"])
	}

	st.frames <- cursorFrame(spoke(2), "c2")
	if took := w.committed("c2", 5*time.Second); took > 600*time.Millisecond {
		t.Errorf("the later batch took %s", took)
	}
	if w.row(2)["first_output_at"] == nil {
		t.Errorf("the later batch's first output was not recorded")
	}
}

// lux streams a resumed placement's first frames before its answer to the
// resume is back: dude's resume has the row in before it asks, so they
// are timed.
func TestFramesStreamedBeforeLuxAnswersTheResumeAreTimed(t *testing.T) {
	w := newResumeWorld(t)
	base := time.Now().Add(-time.Minute).UTC()
	w.sessionOn(1)
	w.resumable()
	w.lux.set(stoppedOnHost1(base), nil)
	w.s.Lux = &earlyFramesLux{streamLux: w.following(), w: w, epoch: 2}
	if _, _, err := w.s.resume(w.ctx, w.run, ""); err != nil {
		t.Fatalf("resume: %v", err)
	}
	row := w.row(2)
	for _, col := range []string{"requested_at", "running_at", "busy_at", "first_output_at"} {
		if row[col] == nil {
			t.Errorf("%s not recorded from frames lux streamed before its answer", col)
		}
	}
}

// earlyFramesLux resumes the Run into epoch, streaming that placement's
// session, busy and first words, and having them committed, before it
// answers.
type earlyFramesLux struct {
	*streamLux
	w     *resumeWorld
	epoch int
}

func (f *earlyFramesLux) Resume(context.Context, string, lux.ResumeInput) (lux.Run, error) {
	f.frames <- session(f.epoch)
	f.frames <- cursorFrame(busy(f.epoch), "e1")
	f.w.committed("e1", 5*time.Second)
	f.frames <- cursorFrame(spoke(f.epoch), "e2")
	f.w.committed("e2", 5*time.Second)
	return lux.Run{ID: "lrun_1", State: "resuming", Epoch: f.epoch}, nil
}

// The resume's row is in before lux is asked, so the new placement's
// first frames, streamed before lux's answer is back, are timed.
func TestFramesBeforeLuxAnswersTheResumeAreTimed(t *testing.T) {
	w := newResumeWorld(t)
	base := time.Now().Add(-time.Minute).UTC()
	w.lux.set(runningAgain(base, "host-a"), nil)
	w.sessionOn(1)
	st := w.following()
	w.s.resumeAsked(w.ctx, w.run, stoppedOnHost1(base))
	asked := w.row(2)["requested_at"]
	if asked == nil {
		t.Fatal("no requested_at once lux was asked")
	}
	// lux's answer is not in yet: the Run is still paused.
	st.frames <- session(2)
	st.frames <- cursorFrame(busy(2), "c1")
	w.committed("c1", 5*time.Second)
	st.frames <- cursorFrame(spoke(2), "c2")
	w.committed("c2", 5*time.Second)
	w.resumeAnswered(stoppedOnHost1(base), lux.Run{})
	row := w.row(2)
	for _, col := range []string{"running_at", "busy_at", "first_output_at"} {
		if row[col] == nil {
			t.Errorf("%s not recorded from a frame lux streamed before its answer", col)
		}
	}
	if !row["requested_at"].(time.Time).Before(row["running_at"].(time.Time)) {
		t.Errorf("requested_at %v is not before running_at %v", row["requested_at"], row["running_at"])
	}
}

// lux resuming into another epoch than dude foresaw, before any of that
// epoch's frames came: the row moves to lux's epoch, and its frames,
// streamed after, are timed.
func TestAResumeIntoAnotherEpochIsTimedOnceItsRowMoves(t *testing.T) {
	w := newResumeWorld(t)
	base := time.Now().Add(-time.Minute).UTC()
	st := w.following()
	foreseen := w.s.resumeAsked(w.ctx, w.run, stoppedOnHost1(base))
	if foreseen != 2 {
		t.Fatalf("foreseen epoch %d, want 2", foreseen)
	}
	w.resumeAnswered(stoppedOnHost1(base), lux.Run{Epoch: 3, State: "resuming"})
	if n := w.count(`SELECT count(*) FROM run_resumes WHERE run_id = $1 AND epoch = 2`); n != 0 {
		t.Errorf("the row stayed under the foreseen epoch")
	}
	st.frames <- cursorFrame(busy(3), "c2")
	st.frames <- cursorFrame(spoke(3), "c3")
	w.committed("c3", 5*time.Second)
	row := w.row(3)
	if row["busy_at"] == nil || row["first_output_at"] == nil {
		t.Errorf("epoch 3's frames were not timed: busy %v, first output %v", row["busy_at"], row["first_output_at"])
	}
}

// lux streams the first frames of another epoch than dude foresaw — its
// session, busy and a single chunk, all committed while the row is still
// under the foreseen one — and then answers with that epoch, and nothing
// more comes. When those frames came is recorded nowhere: the resume is
// timed once, without them, and no later frame's time stands in for its
// first output.
func TestFirstFramesOfAnotherEpochBeforeTheRowMovesAreNotTakenFromALaterOne(t *testing.T) {
	w := newResumeWorld(t)
	base := time.Now().Add(-time.Minute).UTC()
	w.sessionOn(1)
	w.resumable()
	w.lux.set(stoppedOnHost1(base), nil)
	st := w.following()
	w.s.Lux = &earlyFramesLux{streamLux: st, w: w, epoch: 3}
	w.whilePaused()
	w.untilTimed()
	if n := len(w.timed()); n != 1 {
		t.Fatalf("%d run.resume.timed with no frame after lux's answer, want 1", n)
	}
	// A later running state, busy and chunk, were any taken as the first,
	// would stamp the row now.
	st.frames <- running(3)
	st.frames <- cursorFrame(busy(3), "later-busy")
	w.committed("later-busy", 5*time.Second)
	st.frames <- cursorFrame(spoke(3), "o3")
	w.committed("o3", 5*time.Second)
	time.Sleep(100 * time.Millisecond)
	got := w.timed()
	if len(got) != 1 || got[0]["epoch"] != 3.0 {
		t.Fatalf("run.resume.timed %v, want one for epoch 3", got)
	}
	phases := got[0]["phases"].(map[string]any)
	for _, unknown := range []string{"reload", "take", "firstOutput"} {
		if _, ok := phases[unknown]; ok {
			t.Errorf("phase %s reported from frames whose time was never recorded: %v", unknown, phases)
		}
	}
	if _, ok := got[0]["totalMs"]; ok {
		t.Errorf("totalMs %v reported though the first output's time is unknown", got[0]["totalMs"])
	}
	if _, ok := got[0]["untilBusyMs"]; ok {
		t.Errorf("untilBusyMs %v reported though the first busy's time is unknown", got[0]["untilBusyMs"])
	}
	if row := w.row(3); row["first_output_at"] != nil || row["busy_at"] != nil || row["running_at"] != nil {
		t.Errorf("stamped from a later frame: running %v busy %v first output %v",
			row["running_at"], row["busy_at"], row["first_output_at"])
	}
}

// A trailing running state of the stopped epoch, committed before the row
// moves to the epoch lux names, is no frame of that epoch: the moved row
// is not marked frames_missed, and its own session, busy and output,
// after the move, stamp it.
func TestAnOlderEpochsRunningStateBeforeTheMoveMarksNothingMissed(t *testing.T) {
	w := newResumeWorld(t)
	base := time.Now().Add(-time.Minute).UTC()
	w.sessionOn(1)
	if foreseen := w.s.resumeAsked(w.ctx, w.run, stoppedOnHost1(base)); foreseen != 2 {
		t.Fatalf("foreseen epoch %d, want 2", foreseen)
	}
	w.follow(running(1))
	if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND lux_state = 'running'`); n != 1 {
		t.Fatalf("epoch 1's running state was not committed")
	}
	w.resumeAnswered(stoppedOnHost1(base), lux.Run{Epoch: 3, State: "resuming"})
	if row := w.row(3); row["frames_missed"] != false {
		t.Errorf("frames_missed = %v on epoch 3's row after only an epoch-1 frame, want false", row["frames_missed"])
	}
	w.follow(session(3), busy(3), spoke(3))
	row := w.row(3)
	if row["busy_at"] == nil || row["first_output_at"] == nil {
		t.Errorf("epoch 3's frames after the move were not stamped: busy %v, first output %v",
			row["busy_at"], row["first_output_at"])
	}
}

// A trailing busy of the stopped epoch, committed after the resume was
// asked and before the row moves to the epoch lux names, is no frame of
// that epoch: the agent's activity it records carries no epoch. The moved
// row is not marked frames_missed, and its own frames stamp it.
func TestAnOlderEpochsActivityAfterTheAskMarksNothingMissed(t *testing.T) {
	w := newResumeWorld(t)
	base := time.Now().Add(-time.Minute).UTC()
	w.sessionOn(1)
	foreseen := w.s.resumeAsked(w.ctx, w.run, stoppedOnHost1(base))
	if foreseen != 2 {
		t.Fatalf("foreseen epoch %d, want 2", foreseen)
	}
	w.follow(busy(1))
	if n := w.count(`SELECT count(*) FROM runs r JOIN run_resumes rr ON rr.run_id = r.id
		WHERE r.id = $1 AND rr.epoch = 2 AND r.agent_active_at >= rr.requested_at`); n != 1 {
		t.Fatalf("epoch 1's busy is not committed at or after the resume was asked")
	}
	w.accepted(foreseen, lux.Run{Epoch: 3, State: "resuming"})
	if row := w.row(3); row["frames_missed"] != false {
		t.Errorf("frames_missed = %v on epoch 3's row after only epoch 1's activity, want false", row["frames_missed"])
	}
	w.follow(session(3), busy(3), spoke(3))
	if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND agent_session_epoch = 3`); n != 1 {
		t.Errorf("epoch 3's session was not recorded")
	}
	if row := w.row(3); row["busy_at"] == nil || row["first_output_at"] == nil {
		t.Errorf("epoch 3's frames after the move were not stamped: busy %v, first output %v",
			row["busy_at"], row["first_output_at"])
	}
}

// lux answers a resume with the Run's current epoch, still the one it was
// stopped in: the new placement's epoch is set when lux's scheduler
// assigns it, after the answer. The row stays under the foreseen epoch,
// is not marked frames_missed, and is timed from that epoch's frames,
// once, with every phase; run.unparked carries the foreseen epoch.
func TestALuxAnswerCarryingTheStoppedEpochLeavesTheRowAtTheForeseenOne(t *testing.T) {
	w := newResumeWorld(t)
	base := time.Now().Add(-time.Minute).UTC().Truncate(time.Millisecond)
	// The session was established on epoch 1, the one it was stopped in.
	w.sessionOn(1)
	// Parked before the question was answered (2s ago), so the answer woke it.
	if err := w.s.DB.InOrg(w.ctx, w.run.Org, func(tx pgx.Tx) error {
		return w.s.event(w.ctx, tx, w.run, evParked, ledger.ActorSystem,
			map[string]any{"reason": "question", "parkedAt": time.Now().Add(-10 * time.Second).UTC()})
	}); err != nil {
		t.Fatal(err)
	}
	w.resumable()
	w.lux.set(stoppedOnHost1(base), nil)
	st := w.following()
	w.s.Lux = &stoppedEpochLux{streamLux: st, base: base}
	w.whilePaused()
	if n := w.count(`SELECT count(*) FROM run_resumes WHERE run_id = $1 AND epoch = 1`); n != 0 {
		t.Errorf("the row moved to the stopped epoch 1")
	}
	if row := w.row(2); row["frames_missed"] != false {
		t.Errorf("frames_missed = %v on the foreseen epoch's row, want false", row["frames_missed"])
	}
	var unparked int
	if err := w.owner.QueryRow(w.ctx, `SELECT (payload->>'epoch')::int FROM events
		WHERE run_id = $1 AND event_type = 'run.unparked'`, w.run.ID).Scan(&unparked); err != nil || unparked != 2 {
		t.Errorf("run.unparked epoch %d (%v), want the foreseen 2", unparked, err)
	}

	// lux assigns epoch 2, and its placement's frames come.
	st.frames <- session(2)
	st.frames <- running(2)
	st.frames <- busy(2)
	st.frames <- cursorFrame(spoke(2), "c1")
	w.committed("c1", 5*time.Second)
	w.untilTimed()
	time.Sleep(100 * time.Millisecond)
	got := w.timed()
	if len(got) != 1 {
		t.Fatalf("%d run.resume.timed, want 1: %v", len(got), got)
	}
	phases, _ := got[0]["phases"].(map[string]any)
	if got[0]["epoch"] != 2.0 || len(phases) != 8 || got[0]["totalMs"] == nil || got[0]["untilBusyMs"] == nil {
		t.Errorf("run.resume.timed %v, want epoch 2 with all 8 phases, totalMs and untilBusyMs", got[0])
	}
	row := w.row(2)
	for _, col := range []string{"running_at", "busy_at", "first_output_at"} {
		if row[col] == nil {
			t.Errorf("%s not stamped from epoch 2's frames", col)
		}
	}
}

// stoppedEpochLux answers a resume as lux does: "resuming", with the Run's
// current epoch, the stopped 1. Its Gets report epoch 2 assigned and
// running from then on.
type stoppedEpochLux struct {
	*streamLux
	base time.Time
}

func (f *stoppedEpochLux) Resume(context.Context, string, lux.ResumeInput) (lux.Run, error) {
	f.placementLux.set(runningAgain(f.base, "host-a"), nil)
	return lux.Run{ID: "lrun_1", State: "resuming", Epoch: 1}, nil
}

// A resume lux refuses for good leaves no row, and the Run fails; one it
// may take later keeps its row, with the attempt it takes as
// requested_at.
func TestARefusedResumeLeavesNoRowAndARetriedOneIsTimedFromTheAttemptLuxTook(t *testing.T) {
	w := newResumeWorld(t)
	base := time.Now().Add(-time.Minute).UTC()
	w.resumable()
	refusing := &refusingLux{placementLux: w.lux, err: &lux.Error{Status: 503, Code: "unavailable"}}
	w.s.Lux = refusing
	w.lux.set(stoppedOnHost1(base), nil)
	w.whilePaused()
	first := w.row(2)["requested_at"].(time.Time)
	time.Sleep(20 * time.Millisecond)
	w.whilePaused()
	if again := w.row(2)["requested_at"].(time.Time); !again.After(first) {
		t.Errorf("a retry after lux kept the Run stopped left requested_at at the first attempt: %v", again)
	}

	refusing.err = &lux.Error{Status: 400, Code: "invalid", Message: "no"}
	w.whilePaused()
	if n := w.count(`SELECT count(*) FROM run_resumes WHERE run_id = $1`); n != 0 {
		t.Errorf("%d rows for a resume lux refused for good", n)
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'failed'`); n != 1 {
		t.Errorf("the Run lux refused to resume did not fail")
	}
}

// resumable makes w's Run one whilePaused resumes: a model to build its
// spec, and no forge.
func (w *resumeWorld) resumable() {
	w.t.Helper()
	w.exec(`UPDATE projects SET agent_models = '{"implementer":{"model":"llm/impl"}}'::jsonb WHERE id = $1`, w.run.ProjectID)
	w.s.Forges = forge.Resolver{DB: w.s.DB}
}

// refusingLux refuses every resume with err.
type refusingLux struct {
	*placementLux
	err error
}

func (f *refusingLux) Resume(context.Context, string, lux.ResumeInput) (lux.Run, error) {
	return lux.Run{}, f.err
}

func (f *refusingLux) Cancel(context.Context, string) error { return nil }

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
	defer w.holding(`LOCK TABLE run_resumes IN ACCESS EXCLUSIVE MODE`)()
	st := w.following()
	select {
	case <-st.opened:
	case <-time.After(time.Second):
		t.Fatal("the follower did not open lux's stream while the resumes table was locked")
	}
}
