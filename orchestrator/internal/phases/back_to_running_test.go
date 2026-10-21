package phases

import (
	"testing"
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

// A Run never away from running (a fresh start: scheduled, starting, then
// running) announces nothing on running: run.started is its end.
func TestAFreshStartsRunningIsNotAnnouncedAsBackToRunning(t *testing.T) {
	w := newResumeWorld(t)
	w.exec(`UPDATE runs SET status = 'running', lux_state = 'scheduled', dude_pause = NULL, lux_stop_reason = NULL,
		left_running_at = NULL WHERE id = $1`, w.run.ID)
	w.follow(luxState(1, "starting"), running(1), running(1))
	if n := w.backToRunning(); n != 0 {
		t.Fatalf("a Run never away announced running %d times, want 0", n)
	}
}
