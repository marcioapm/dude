package orchestrator_test

// lux's git.checkout, which records where each checkout started
// (runs.base_shas), trails the agent's records. A turn whose end is read
// before it waits for it: the work is pushed once it arrives, and never
// without it. The wait ends, failing the Run kept, when its deadline
// passes or lux reports the Run over first.

import (
	"context"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

const noBase = "lux never reported where the checkout started"

// holdCheckouts holds every lux Run's stream before its git.checkout until
// the returned release is called (also at cleanup). A held stream sends
// nothing after it, so the turn's end is read first.
func (w *world) holdCheckouts() (release func()) {
	held := make(chan struct{})
	release = sync.OnceFunc(func() { close(held) })
	w.lux.BeforeEvent(func(_ string, _ int64, typ string) {
		if typ == "git.checkout" {
			<-held
		}
	})
	w.t.Cleanup(func() { w.lux.BeforeEvent(nil); release() })
	return release
}

// untilTurnReadUnbased delivers task and waits until dude has read its
// implementer's turn's end and lux's running, with no base recorded; its
// Run and lux Run.
func (w *world) untilTurnReadUnbased(task string) (runID, luxID string) {
	w.t.Helper()
	w.deliver(task)
	w.until("the turn's end read with no base", func() bool {
		_ = w.owner.QueryRow(context.Background(), `SELECT id, lux_run_id FROM runs WHERE task_id = $1 AND phase = 'implement'
			AND turn_done_at IS NOT NULL AND lux_state = 'running' AND base_shas = '{}'::jsonb`, task).Scan(&runID, &luxID)
		return runID != ""
	})
	return runID, luxID
}

// assertWaiting checks, over several sweeps, that the Run is neither ended
// nor pushed.
func (w *world) assertWaiting(runID string) {
	w.t.Helper()
	for range 20 {
		w.pump()
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'running' AND push_request_id IS NULL`, runID); n != 1 {
		w.t.Fatalf("the Run did not wait for its base\nruns:\n%s", w.describeRuns())
	}
	if n := len(w.luxRunFor(runID).PushCredentials); n != 0 {
		w.t.Fatalf("lux was asked to push %d times before the base arrived", n)
	}
}

// readsHistory records the bases each history scan dude ran was given.
type readsHistory struct {
	lux.Client
	mu    sync.Mutex
	bases [][]string
}

func (c *readsHistory) Exec(ctx context.Context, runID string, command []string) (lux.ExecResult, error) {
	if len(command) > 3 && command[3] == "dude-history" {
		c.mu.Lock()
		c.bases = append(c.bases, slices.Clone(command[4:]))
		c.mu.Unlock()
	}
	return c.Client.Exec(ctx, runID, command)
}

// (a) The base arrives after the turn's end, inside the wait: the Run
// waits, unpushed, then its history is scanned from that base and it is
// pushed and completed as usual.
func TestATurnEndedBeforeItsBaseWaitsForIt(t *testing.T) {
	w := newWorld(t)
	history := &readsHistory{Client: w.syncer.Lux}
	w.syncer.Lux = history
	w.grant("IMPL_TOKEN", implToken, "implementer")
	release := w.holdCheckouts()
	task := w.task()
	runID, _ := w.untilTurnReadUnbased(task)
	w.assertWaiting(runID)

	release()
	w.until("the implementer completed", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'completed'`, runID) == 1
	})
	if lr := w.luxRunFor(runID); len(lr.PushCredentials) != 1 || !lr.Pushed {
		t.Errorf("lux was asked to push %d times, pushed %v", len(lr.PushCredentials), lr.Pushed)
	}
	var base string
	_ = w.owner.QueryRow(context.Background(), `SELECT base_shas->>'target' FROM runs WHERE id = $1`, runID).Scan(&base)
	history.mu.Lock()
	defer history.mu.Unlock()
	if len(history.bases) != 1 || base == "" || !slices.Equal(history.bases[0][:1], []string{"target"}) ||
		history.bases[0][len(history.bases[0])-1] != base {
		t.Errorf("history scans %v, want one of target from %q", history.bases, base)
	}
}

// (b) The base never arrives: past the wait the Run fails kept, refused
// for the missing base, and lux is never asked to push.
func TestATurnWhoseBaseNeverArrivesFailsAfterTheWait(t *testing.T) {
	w := newWorld(t)
	w.syncer.BaseWait = time.Second
	w.grant("IMPL_TOKEN", implToken, "implementer")
	w.holdCheckouts()
	task := w.task()
	runID, _ := w.untilTurnReadUnbased(task)
	w.until("the implementer failed", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'failed'`, runID) == 1
	})
	assertRefusedForNoBase(t, w, runID)
}

// dropsCheckouts is a lux that never reports where a checkout started.
type dropsCheckouts struct{ lux.Client }

func (c dropsCheckouts) Output(ctx context.Context, runID, cursor string, afterEvent int64, fn func(lux.Frame) error) error {
	return c.Client.Output(ctx, runID, cursor, afterEvent, func(f lux.Frame) error {
		if f.Kind == "lux" && f.EventType == "git.checkout" {
			return nil
		}
		return fn(f)
	})
}

// (c) lux reports the Run over while it waits for its base: it fails kept
// at once, refused for the missing base, never pushed. No project secret,
// so nothing but the wait stands between the turn's end and a push.
func TestATurnWaitingForItsBaseFailsWhenLuxEndsTheRun(t *testing.T) {
	w := newWorld(t)
	w.syncer.Lux = dropsCheckouts{w.syncer.Lux}
	task := w.task()
	runID, luxID := w.untilTurnReadUnbased(task)
	w.assertWaiting(runID)

	w.lux.Crash(luxID)
	w.until("the implementer failed", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'failed'`, runID) == 1
	})
	assertRefusedForNoBase(t, w, runID)
}

func assertRefusedForNoBase(t *testing.T, w *world, runID string) {
	t.Helper()
	var reason string
	var keep bool
	_ = w.owner.QueryRow(context.Background(), `SELECT error, keep FROM runs WHERE id = $1`, runID).Scan(&reason, &keep)
	if !strings.Contains(reason, noBase) {
		t.Errorf("reason %q, want it to say %q", reason, noBase)
	}
	if !keep {
		t.Error("the Run is not kept for its owner to resume")
	}
	if lr := w.luxRunFor(runID); len(lr.PushCredentials) != 0 || lr.Pushed {
		t.Errorf("lux was asked to push %d times", len(lr.PushCredentials))
	}
}

// holdLaterCheckouts holds every lux Run's stream before any git.checkout
// but the Run's first, until the returned release is called (also at
// cleanup): one checkout's base is recorded, the next is not.
func (w *world) holdLaterCheckouts() (release func()) {
	held := make(chan struct{})
	release = sync.OnceFunc(func() { close(held) })
	var mu sync.Mutex
	first := map[string]int64{}
	w.lux.BeforeEvent(func(runID string, eventID int64, typ string) {
		if typ != "git.checkout" {
			return
		}
		mu.Lock()
		if _, ok := first[runID]; !ok {
			first[runID] = eventID
		}
		later := first[runID] != eventID
		mu.Unlock()
		if later {
			<-held
		}
	})
	w.t.Cleanup(func() { w.lux.BeforeEvent(nil); release() })
	return release
}

// (d) Two repositories to push, and only one's base arrives: the Run
// waits with one base recorded, never pushed. Once the other arrives both
// bases are recorded and it is pushed once.
func TestATurnWithOneOfTwoBasesWaitsForTheOther(t *testing.T) {
	w := newWorld(t)
	release := w.holdLaterCheckouts()
	task := w.task()
	w.addWeb(task, "write")
	w.start(task, scriptedPolicy())
	var runID string
	w.until("the turn's end read with one of two bases", func() bool {
		_ = w.owner.QueryRow(context.Background(), `SELECT id FROM runs WHERE task_id = $1 AND phase = 'implement'
			AND turn_done_at IS NOT NULL AND lux_state = 'running' AND lux_pushes @> '{target,web}'
			AND (SELECT count(*) FROM jsonb_object_keys(base_shas)) = 1`, task).Scan(&runID)
		return runID != ""
	})
	w.assertWaiting(runID)

	release()
	w.until("the implementer completed", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'completed'`, runID) == 1
	})
	if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND base_shas ? 'target' AND base_shas ? 'web'`, runID); n != 1 {
		t.Error("both bases are not recorded")
	}
	if lr := w.luxRunFor(runID); len(lr.PushCredentials) != 1 || !lr.Pushed {
		t.Errorf("lux was asked to push %d times, pushed %v", len(lr.PushCredentials), lr.Pushed)
	}
}

// untilWithoutSweeps polls the database until sql counts one row, running
// no sweep in between: only the fakes and dude's followers move meanwhile.
func (w *world) untilWithoutSweeps(what, sql string, args ...any) {
	w.t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if w.count(sql, args...) == 1 {
			return
		}
		time.Sleep(untilPoll)
	}
	w.t.Fatalf("timed out waiting for %s\nruns:\n%s", what, w.describeRuns())
}

// (e) The base arrives after the wait's deadline but before the sweep
// that would have failed the Run: that sweep reads it, so the Run is
// pushed and completed, not refused for a missing base.
func TestABaseArrivingPastTheWaitBeforeTheNextSweepIsPushed(t *testing.T) {
	w := newWorld(t)
	release := w.holdCheckouts()
	task := w.task()
	runID, _ := w.untilTurnReadUnbased(task)
	// Shortened only now: untilTurnReadUnbased sweeps, and one of its sweeps
	// past a 1 s deadline would fail the Run before the base is released.
	w.syncer.BaseWait = time.Second

	w.untilWithoutSweeps("the base wait's deadline on the database clock", `SELECT count(*) FROM runs
		WHERE id = $1 AND turn_done_at <= now() - interval '1 second' AND base_shas = '{}'::jsonb AND status = 'running'`, runID)
	release()
	w.untilWithoutSweeps("the base recorded", `SELECT count(*) FROM runs WHERE id = $1 AND base_shas ? 'target' AND status = 'running'`, runID)
	w.pump()
	if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'running' AND push_request_id IS NOT NULL`, runID); n != 1 {
		t.Fatalf("the sweep after the base arrived did not ask lux to push\nruns:\n%s", w.describeRuns())
	}
	w.until("the implementer completed", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status IN ('completed', 'failed')`, runID) == 1
	})
	var status, reason string
	_ = w.owner.QueryRow(context.Background(), `SELECT status::text, COALESCE(error, '') FROM runs WHERE id = $1`, runID).Scan(&status, &reason)
	if status != "completed" || strings.Contains(reason, noBase) {
		t.Errorf("the Run is %s (%q), want completed", status, reason)
	}
	if lr := w.luxRunFor(runID); len(lr.PushCredentials) != 1 || !lr.Pushed {
		t.Errorf("lux was asked to push %d times, pushed %v", len(lr.PushCredentials), lr.Pushed)
	}
}
