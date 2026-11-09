package phases

import "testing"

// waitingWorld is a resumeWorld whose Run runs, waiting on its open
// question past any grace: what the sweep read as ParkNow.
func waitingWorld(t *testing.T) (*resumeWorld, phaseRun) {
	w := newResumeWorld(t)
	w.exec(`UPDATE questions SET status = 'open', answer = NULL, answered_at = NULL WHERE run_id = $1`, w.run.ID)
	w.exec(`UPDATE runs SET status = 'running', lux_state = 'running', dude_pause = NULL, lux_stop_reason = NULL,
		waiting_since = now() - interval '1 hour' WHERE id = $1`, w.run.ID)
	r := w.run
	r.Status, r.LuxState, r.DudePause, r.ParkNow = statusRunning, "running", "", true
	return w, r
}

// note queues a directive for w's Run, held for its question's answer if held.
func (w *resumeWorld) note(id string, held bool) {
	w.t.Helper()
	w.exec(`INSERT INTO directives (id, organization_id, task_id, run_id, text, held_for)
		VALUES ($1, $2, $3, $4, 'news', CASE WHEN $5 THEN 'q_'||$2 END)`, id, w.run.Org, w.run.TaskID, w.run.ID, held)
}

// The sweep read the Run as due a park for its person; a note was queued
// before the park's own transaction. The park re-checks and does not
// happen: the note goes first (unheardNews). One held for the answer does
// not stop it.
func TestAPersonParkRechecksForANoteQueuedSinceTheSweepRead(t *testing.T) {
	for _, c := range []struct {
		name  string
		held  bool
		parks int
	}{
		{"a note to deliver", false, 0},
		{"a note held for the answer", true, 1},
	} {
		t.Run(c.name, func(t *testing.T) {
			w, r := waitingWorld(t)
			w.note("dir_"+w.run.Org, c.held)
			if err := w.s.requestPause(w.ctx, r, "person", "parked while it waits for a person"); err != nil {
				t.Fatal(err)
			}
			if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND control = 'pause_graceful' AND dude_pause = 'person'`); n != c.parks {
				t.Errorf("%d pauses for the person, want %d", n, c.parks)
			}
			if n := w.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = 'run.parked'`); n != c.parks {
				t.Errorf("%d run.parked events, want %d", n, c.parks)
			}
		})
	}
}

// A note sent, its turn not under way yet (unread, or read after the wait
// began), holds the park off too; one read before the wait does not, nor
// one sent longer ago than delivery.UnreadGraceSecs.
func TestAPersonParkWaitsForASentNotesTurn(t *testing.T) {
	for _, c := range []struct {
		name, sent, delivered string
		parks                 int
	}{
		{"sent, unread", "now()", "NULL", 0},
		{"read after the wait began", "now()", "now()", 0},
		{"read before the wait began", "now() - interval '2 hours'", "now() - interval '2 hours'", 1},
		{"sent past the unread grace, unread", "now() - interval '1 hour'", "NULL", 1},
	} {
		t.Run(c.name, func(t *testing.T) {
			w, r := waitingWorld(t)
			w.note("dir_"+w.run.Org, false)
			w.exec(`UPDATE directives SET sent_at = `+c.sent+`, delivered_at = `+c.delivered+` WHERE id = 'dir_'||$1`, w.run.Org)
			if err := w.s.requestPause(w.ctx, r, "person", "parked while it waits for a person"); err != nil {
				t.Fatal(err)
			}
			if n := w.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = 'run.parked'`); n != c.parks {
				t.Errorf("%d run.parked events, want %d", n, c.parks)
			}
		})
	}
}
