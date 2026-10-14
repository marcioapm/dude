package phases

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/dbtest"
	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// heldWorld is one running implementer whose turn broke on its agent's
// process exiting, held (harness_state.brokenTurn) since heldFor ago.
type heldWorld struct {
	t     *testing.T
	ctx   context.Context
	s     *Syncer
	owner *pgx.Conn
	id    string
}

func newHeldWorld(t *testing.T, broken string, heldFor time.Duration) *heldWorld {
	t.Helper()
	ctx := context.Background()
	app, owner := dbtest.Open(t)
	org := dbtest.Org(t, owner)
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES ('prj_'||$1, $1, 'P', 'prj_'||$1, 'P')`, nil},
		{`INSERT INTO tasks (id, organization_id, project_id, number, title, goal) VALUES ('wi_'||$1, $1, 'prj_'||$1, 1, 'T', 'G')`, nil},
		{`INSERT INTO runs (id, organization_id, project_id, task_id, attempt, phase, status, lux_run_id, lux_state, harness_state)
			VALUES ('run_'||$1, $1, 'prj_'||$1, 'wi_'||$1, 1, 'implement', 'running', 'lrun_1', 'running',
				jsonb_build_object('brokenTurn', $2::jsonb || jsonb_build_object('at', now() - make_interval(secs => $3))))`,
			[]any{broken, heldFor.Seconds()}},
	} {
		if _, err := owner.Exec(ctx, q.sql, append([]any{org}, q.args...)...); err != nil {
			t.Fatal(err)
		}
	}
	s := &Syncer{DB: app, Lux: &eventsLux{}, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	t.Cleanup(s.Stop)
	return &heldWorld{t: t, ctx: ctx, s: s, owner: owner, id: "run_" + org}
}

// eventsLux is lux as far as a held turn's decision goes: Events answers
// the page set (none by default), or err.
type eventsLux struct {
	lux.Client
	mu    sync.Mutex
	page  []lux.Frame
	err   error
	reads int
}

func (f *eventsLux) Events(context.Context, string, int64) ([]lux.Frame, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads++
	return f.page, f.err
}

// Output, for a follower the decision starts: nothing more, until stopped.
func (f *eventsLux) Output(ctx context.Context, _, _ string, _ int64, _ func(lux.Frame) error) error {
	<-ctx.Done()
	return ctx.Err()
}

// snapshot is the Run as a sweep takes it up.
func (w *heldWorld) snapshot() phaseRun {
	w.t.Helper()
	runs, err := w.s.due(w.ctx)
	if err != nil {
		w.t.Fatal(err)
	}
	for _, r := range runs {
		if r.ID == w.id {
			return r
		}
	}
	w.t.Fatalf("the sweep does not take up %s", w.id)
	return phaseRun{}
}

func (w *heldWorld) status() (status, errText string, failed int) {
	w.t.Helper()
	if err := w.owner.QueryRow(w.ctx, `SELECT status::text, COALESCE(error, ''),
			(SELECT count(*) FROM events WHERE run_id = $1 AND event_type = 'run.failed')
		FROM runs WHERE id = $1`, w.id).Scan(&status, &errText, &failed); err != nil {
		w.t.Fatal(err)
	}
	return status, errText, failed
}

const heldExit = `{"error": "the agent's turn failed: session/prompt: process exited (0)",
	"agentError": "session/prompt: process exited (0)", "epoch": 1}`

// The sweep takes up a hold overdue by heldWait while lux's preemption,
// read by the follower in a batch holding the Run's row, makes it a move:
// the failure waits for that batch, then finds the move's longer bound in
// force and fails nothing.
func TestAnOverdueHoldLuxJustCalledAMoveIsNotFailedAtTheShorterBound(t *testing.T) {
	w := newHeldWorld(t, heldExit, 2*time.Minute)
	r := w.snapshot()
	if !w.s.brokenOverdue(r) {
		t.Fatalf("the snapshot's hold is not overdue: %+v", r.Broken)
	}
	tx, err := w.owner.Begin(w.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(w.ctx) }()
	if _, err := tx.Exec(w.ctx, `UPDATE runs SET harness_state = jsonb_set(harness_state, '{brokenTurn,moved}', '"preempt"')
		WHERE id = $1`, w.id); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := w.s.advance(w.ctx, r)
		done <- err
	}()
	// The failure is waiting on the batch's row lock.
	for deadline := time.Now().Add(5 * time.Second); ; {
		var waiting bool
		if err := tx.QueryRow(w.ctx, `SELECT EXISTS (SELECT 1 FROM pg_locks WHERE NOT granted AND locktype IN ('transactionid', 'tuple'))`).
			Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the failure never waited for the batch holding the Run")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err := tx.Commit(w.ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if status, errText, failed := w.status(); status != "running" || errText != "" || failed != 0 {
		t.Errorf("run %s %q, %d run.failed; want it running, its move given its 20 minutes", status, errText, failed)
	}
}

// A hold overdue by its own bound fails, saying the move it was.
func TestAMoveOverdueByItsOwnBoundFailsNamingIt(t *testing.T) {
	w := newHeldWorld(t, `{"error": "the agent's turn failed: session/prompt: process exited (0)",
		"agentError": "session/prompt: process exited (0)", "epoch": 1, "moved": "drain"}`, 21*time.Minute)
	if _, err := w.s.advance(w.ctx, w.snapshot()); err != nil {
		t.Fatal(err)
	}
	const want = "lux stopped the Run to move it: its host was drained, and it was not running again within 20m0s; " +
		"the agent's turn failed: session/prompt: process exited (0)"
	if status, errText, failed := w.status(); status != "failed" || errText != want || failed != 1 {
		t.Errorf("run %s %q, %d run.failed; want failed once with %q", status, errText, failed, want)
	}
}

// A pause asked for after the sweep took the overdue hold up wins.
func TestAPauseAskedAfterTheSweepTookTheHoldUpWins(t *testing.T) {
	w := newHeldWorld(t, heldExit, 2*time.Minute)
	r := w.snapshot()
	if _, err := w.owner.Exec(w.ctx, `UPDATE runs SET control = 'pause_hard' WHERE id = $1`, w.id); err != nil {
		t.Fatal(err)
	}
	if err := w.s.failBroken(w.ctx, r, 0); err != nil {
		t.Fatal(err)
	}
	if status, _, failed := w.status(); status != "running" || failed != 0 {
		t.Errorf("run %s, %d run.failed; want the pause left to act", status, failed)
	}
}

// An overdue hold with lux events dude has not read yet — a restart, its
// follower not caught up — is not failed: the Run is followed, and the
// hold decided once they are read. Read and still overdue, it fails.
func TestAnOverdueHoldWithLuxEventsUnreadWaitsForThem(t *testing.T) {
	w := newHeldWorld(t, heldExit, 2*time.Minute)
	fake := w.s.Lux.(*eventsLux)
	fake.page = []lux.Frame{luxState(2, "running")}
	acted, err := w.s.advance(w.ctx, w.snapshot())
	if err != nil {
		t.Fatal(err)
	}
	if status, _, failed := w.status(); acted || status != "running" || failed != 0 {
		t.Fatalf("acted %v, run %s, %d run.failed; want it followed, not failed", acted, status, failed)
	}
	w.s.mu.Lock()
	_, followed := w.s.following[w.id]
	w.s.mu.Unlock()
	if !followed {
		t.Errorf("the Run is not followed, so its unread events never will be")
	}
	fake.mu.Lock()
	fake.page = nil
	fake.mu.Unlock()
	if _, err := w.s.advance(w.ctx, w.snapshot()); err != nil {
		t.Fatal(err)
	}
	if status, errText, failed := w.status(); status != "failed" || errText != "the agent's turn failed: session/prompt: process exited (0)" || failed != 1 {
		t.Errorf("run %s %q, %d run.failed; want failed once with the agent's error", status, errText, failed)
	}
}

// A failure decided on lux having said nothing more is called off when
// the follower reads more of lux before it commits.
func TestAnOverdueHoldIsNotFailedOnceTheFollowerReadMore(t *testing.T) {
	w := newHeldWorld(t, heldExit, 2*time.Minute)
	r := w.snapshot()
	if _, err := w.owner.Exec(w.ctx, `UPDATE runs SET lux_after_event = 7 WHERE id = $1`, w.id); err != nil {
		t.Fatal(err)
	}
	if err := w.s.failBroken(w.ctx, r, 0); err != nil {
		t.Fatal(err)
	}
	if status, _, failed := w.status(); status != "running" || failed != 0 {
		t.Errorf("run %s, %d run.failed; want it left to what the follower read", status, failed)
	}
}

// A failed read of lux's events defers the decision by the usual retry
// spacing: the sweeps before it ask lux nothing.
func TestAnOverdueHoldLuxCannotBeReadForIsRetriedLater(t *testing.T) {
	w := newHeldWorld(t, heldExit, 2*time.Minute)
	fake := w.s.Lux.(*eventsLux)
	fake.err = errors.New("lux unreachable")
	if _, err := w.s.advance(w.ctx, w.snapshot()); err != nil {
		t.Fatal(err)
	}
	if runs, err := w.s.due(w.ctx); err != nil || len(runs) != 0 {
		t.Errorf("the next sweep takes up %d runs (%v); want none until the retry", len(runs), err)
	}
	if status, _, failed := w.status(); status != "running" || failed != 0 || fake.reads != 1 {
		t.Errorf("run %s, %d run.failed, %d reads; want running, one read", status, failed, fake.reads)
	}
}
