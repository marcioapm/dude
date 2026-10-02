package phases

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/forge"
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

// lux streams a resumed placement's first frames before its answer to the
// resume is back: dude's resume has the row in before it asks, so they
// are timed.
func TestFramesStreamedBeforeLuxAnswersTheResumeAreTimed(t *testing.T) {
	w := newResumeWorld(t)
	base := time.Now().Add(-time.Minute).UTC()
	if _, err := w.owner.Exec(w.ctx, `UPDATE runs SET agent_session_epoch = 1 WHERE id = $1`, w.run.ID); err != nil {
		t.Fatal(err)
	}
	w.resumable()
	w.lux.set(stoppedOnHost1(base), nil)
	st := w.following()
	eager := &eagerLux{streamLux: st, w: w}
	w.s.Lux = eager
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

// eagerLux streams the resumed placement's session, busy and first words,
// and has them committed, before it answers the resume.
type eagerLux struct {
	*streamLux
	w *resumeWorld
}

func (f *eagerLux) Resume(context.Context, string, lux.ResumeInput) (lux.Run, error) {
	f.frames <- record(2, "lux.session", map[string]any{"sessionId": "s1"})
	f.frames <- cursorFrame(busy(2), "e1")
	f.w.committed("e1", 5*time.Second)
	f.frames <- cursorFrame(spoke(2), "e2")
	f.w.committed("e2", 5*time.Second)
	return lux.Run{ID: "lrun_1", State: "resuming", Epoch: 2}, nil
}

// The resume's row is in before lux is asked, so the new placement's
// first frames, streamed before lux's answer is back, are timed.
func TestFramesBeforeLuxAnswersTheResumeAreTimed(t *testing.T) {
	w := newResumeWorld(t)
	base := time.Now().Add(-time.Minute).UTC()
	w.lux.set(runningAgain(base, "host-a"), nil)
	// The agent's session was established on epoch 1.
	if _, err := w.owner.Exec(w.ctx, `UPDATE runs SET agent_session_epoch = 1 WHERE id = $1`, w.run.ID); err != nil {
		t.Fatal(err)
	}
	st := w.following()
	w.s.resumeAsked(w.ctx, w.run, stoppedOnHost1(base))
	asked := w.row(2)["requested_at"]
	if asked == nil {
		t.Fatal("no requested_at once lux was asked")
	}
	// lux's answer is not in yet: the Run is still paused.
	st.frames <- record(2, "lux.session", map[string]any{"sessionId": "s1"})
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

// lux resuming into another epoch than dude foresaw: the row moves to
// lux's epoch, and the frames of that epoch streamed before it moved do
// not keep the later ones from being timed.
func TestAResumeIntoAnotherEpochIsTimedOnceItsRowMoves(t *testing.T) {
	w := newResumeWorld(t)
	base := time.Now().Add(-time.Minute).UTC()
	st := w.following()
	foreseen := w.s.resumeAsked(w.ctx, w.run, stoppedOnHost1(base))
	if foreseen != 2 {
		t.Fatalf("foreseen epoch %d, want 2", foreseen)
	}
	// Epoch 3's first frames, while the row is still under epoch 2.
	st.frames <- cursorFrame(busy(3), "c1")
	st.frames <- cursorFrame(spoke(3), "c1b")
	w.committed("c1b", 5*time.Second)
	w.resumeAnswered(stoppedOnHost1(base), lux.Run{Epoch: 3, State: "resuming"})
	if n := w.count(`SELECT count(*) FROM run_resumes WHERE run_id = $1 AND epoch = 2`); n != 0 {
		t.Errorf("the row stayed under the foreseen epoch")
	}
	st.frames <- cursorFrame(busy(3), "c2")
	st.frames <- cursorFrame(spoke(3), "c3")
	w.committed("c3", 5*time.Second)
	row := w.row(3)
	if row["busy_at"] == nil || row["first_output_at"] == nil {
		t.Errorf("epoch 3's later frames were not timed: busy %v, first output %v", row["busy_at"], row["first_output_at"])
	}
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
	r := w.run
	r.Resumable = true
	if _, err := w.s.whilePaused(w.ctx, r); err != nil {
		t.Fatal(err)
	}
	first := w.row(2)["requested_at"].(time.Time)
	time.Sleep(20 * time.Millisecond)
	if _, err := w.s.whilePaused(w.ctx, r); err != nil {
		t.Fatal(err)
	}
	if again := w.row(2)["requested_at"].(time.Time); !again.After(first) {
		t.Errorf("a retry after lux kept the Run stopped left requested_at at the first attempt: %v", again)
	}

	refusing.err = &lux.Error{Status: 400, Code: "invalid", Message: "no"}
	if _, err := w.s.whilePaused(w.ctx, r); err != nil {
		t.Fatal(err)
	}
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
	if _, err := w.owner.Exec(w.ctx, `UPDATE projects SET agent_models = '{"implementer":{"model":"llm/impl"}}'::jsonb
		WHERE id = $1`, w.run.ProjectID); err != nil {
		w.t.Fatal(err)
	}
	w.s.Forges = forge.Resolver{DB: w.s.DB}
}

// refusingLux refuses every resume with err.
type refusingLux struct {
	*placementLux
	err error
}

func (f *refusingLux) Resume(context.Context, string, lux.ResumeInput) (lux.Run, error) { return lux.Run{}, f.err }
func (f *refusingLux) Cancel(context.Context, string) error                              { return nil }

func (w *resumeWorld) count(sql string) int {
	w.t.Helper()
	var n int
	if err := w.owner.QueryRow(w.ctx, sql, w.run.ID).Scan(&n); err != nil {
		w.t.Fatal(err)
	}
	return n
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
