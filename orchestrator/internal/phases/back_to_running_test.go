package phases

import (
	"testing"
	"time"

	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// Back to running from away is announced once as servers.changed
// {change: state, luxState: running}: the web's move or resume start ends
// on it when the agent is idle and says nothing (#262).

// backToRunning counts the Run's servers.changed saying lux has it running.
func (w *resumeWorld) backToRunning() int {
	w.t.Helper()
	return w.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = 'servers.changed'
		AND payload->>'change' = 'state' AND payload->>'luxState' = 'running'`)
}

// lux moves a running Run (stopping, resuming), then places it on another
// host (scheduled) and has it running: the step into running is not from
// a wait, and is announced once; later running frames announce nothing.
func TestAMovedRunBackToRunningFromScheduledIsAnnouncedOnce(t *testing.T) {
	w := newResumeWorld(t)
	w.exec(`UPDATE runs SET status = 'running', lux_state = 'running', dude_pause = NULL, lux_stop_reason = NULL,
		left_running_at = NULL WHERE id = $1`, w.run.ID)
	w.follow(luxState(1, "stopping"), luxState(1, "resuming"), luxState(2, "scheduled"), luxState(2, "starting"))
	if n := w.backToRunning(); n != 0 {
		t.Fatalf("%d servers.changed running before lux had the Run running", n)
	}
	w.follow(running(2))
	if n := w.backToRunning(); n != 1 {
		t.Fatalf("back to running announced %d times, want 1", n)
	}
	w.follow(running(2), running(2))
	if n := w.backToRunning(); n != 1 {
		t.Fatalf("repeated running frames: %d servers.changed running, want still 1", n)
	}
}

// A fresh start (scheduled, starting, then running) was never away: its
// running is announced by run.started alone, once, and not as back to
// running.
func TestAFreshStartToRunningIsNotAnnouncedAsBackToRunning(t *testing.T) {
	w := newResumeWorld(t)
	w.exec(`UPDATE runs SET status = 'scheduled', lux_state = 'scheduled', dude_pause = NULL, lux_stop_reason = NULL,
		left_running_at = NULL WHERE id = $1`, w.run.ID)
	w.followFrom(statusScheduled, "scheduled", luxState(1, "starting"), running(1), running(1))
	if n := w.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = 'run.started'`); n != 1 {
		t.Fatalf("a fresh start wrote run.started %d times, want 1", n)
	}
	if n := w.backToRunning(); n != 0 {
		t.Fatalf("a Run never away announced running %d times, want 0", n)
	}
}

// A person's resume (the real whilePaused, which records lux's
// "resuming" answer and stamps left_running_at) that lux then places
// through scheduled and starting: back to running is announced once, so an
// idle agent's "resuming" start ends too.
func TestAResumedRunBackToRunningFromScheduledIsAnnouncedOnce(t *testing.T) {
	w := newResumeWorld(t)
	base := time.Now().Add(-time.Minute).UTC()
	w.resumable()
	w.lux.set(stoppedOnHost1(base), nil)
	st := w.followingAs(func(st *streamLux) lux.Client { return &stoppedEpochLux{streamLux: st, base: base} })
	w.whilePaused()
	if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'running' AND left_running_at IS NOT NULL`); n != 1 {
		t.Fatalf("whilePaused did not leave the Run resuming and away from running")
	}
	st.frames <- luxState(2, "scheduled")
	st.frames <- luxState(2, "starting")
	st.frames <- running(2)
	st.frames <- running(2)
	// A record after them, carrying a cursor: once it is saved, every
	// frame before it has been applied.
	st.frames <- cursorFrame(busy(2), "after-running")
	w.committed("after-running", 10*time.Second)
	if n := w.backToRunning(); n != 1 {
		t.Fatalf("a person's resume back to running announced %d times, want 1", n)
	}
}
