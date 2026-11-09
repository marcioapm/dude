package phases

import "testing"

// waitingWorld is a resumeWorld whose Run runs, waiting on its open
// question past any grace: what the sweep read as ParkNow. The question
// was asked a minute ago, inside unheardNews's ceiling.
func waitingWorld(t *testing.T) (*resumeWorld, phaseRun) {
	w := newResumeWorld(t)
	w.exec(`UPDATE questions SET status = 'open', answer = NULL, answered_at = NULL, asked_at = now() - interval '1 minute'
		WHERE run_id = $1`, w.run.ID)
	w.exec(`UPDATE runs SET status = 'running', lux_state = 'running', dude_pause = NULL, lux_stop_reason = NULL,
		waiting_since = now() - interval '1 hour' WHERE id = $1`, w.run.ID)
	r := w.run
	r.Status, r.LuxState, r.DudePause, r.ParkNow = statusRunning, "running", "", true
	return w, r
}

// note queues directive 'dir_'||org for w's Run, held for its question's
// answer if held.
func (w *resumeWorld) note(held bool) {
	w.t.Helper()
	w.exec(`INSERT INTO directives (id, organization_id, task_id, run_id, text, held_for)
		VALUES ('dir_'||$1, $1, $2, $3, 'news', CASE WHEN $4 THEN 'q_'||$1 END)`, w.run.Org, w.run.TaskID, w.run.ID, held)
}

// parkForPerson runs the person park as advance does, and returns how many
// run.parked events the Run has.
func (w *resumeWorld) parkForPerson(r phaseRun) int {
	w.t.Helper()
	if err := w.s.requestPause(w.ctx, r, "person", "parked while it waits for a person"); err != nil {
		w.t.Fatal(err)
	}
	return w.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = 'run.parked'`)
}

// The sweep read the Run as due a park for its person; a note was queued
// before the park's own transaction. The park re-checks and does not
// happen: the note goes first (unheardNews). One held for the answer does
// not stop it; nor one whose send has been failing for longer than
// delivery.UnreadGraceSecs since its first try (claimed_at), so a send lux
// keeps refusing cannot keep the container running.
func TestAPersonParkRechecksForANoteQueuedSinceTheSweepRead(t *testing.T) {
	for _, c := range []struct {
		name    string
		held    bool
		claimed string
		parks   int
	}{
		{"a note to deliver", false, "NULL", 0},
		{"a note held for the answer", true, "NULL", 1},
		{"a note first tried just now, unsent", false, "now()", 0},
		{"a note first tried 121s ago, still unsent", false, "now() - interval '121 seconds'", 1},
	} {
		t.Run(c.name, func(t *testing.T) {
			w, r := waitingWorld(t)
			w.note(c.held)
			w.exec(`UPDATE directives SET claimed_at = `+c.claimed+` WHERE id = 'dir_'||$1`, w.run.Org)
			if n := w.parkForPerson(r); n != c.parks {
				t.Errorf("%d run.parked events, want %d", n, c.parks)
			}
			if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND control = 'pause_graceful' AND dude_pause = 'person'`); n != c.parks {
				t.Errorf("%d pauses for the person, want %d", n, c.parks)
			}
		})
	}
}

// A note sent, its turn not under way yet (unread, or read after the wait
// began), holds the park off too; one read before the wait, or in the batch
// that began it (the turn that read it is over), does not, nor one sent
// longer ago than delivery.UnreadGraceSecs.
func TestAPersonParkWaitsForASentNotesTurn(t *testing.T) {
	for _, c := range []struct {
		name, waiting, sent, delivered string
		parks                          int
	}{
		{"sent, unread", "now() - interval '1 hour'", "now()", "NULL", 0},
		{"read after the wait began", "now() - interval '1 hour'", "now()", "now()", 0},
		// Inside the unread grace: only the read's order with the wait parks it.
		{"read before the wait began", "now() - interval '30 seconds'", "now() - interval '60 seconds'", "now() - interval '50 seconds'", 1},
		{"read in the batch that began the wait", "now() - interval '30 seconds'", "now() - interval '31 seconds'", "(SELECT waiting_since FROM runs WHERE id = run_id)", 1},
		{"sent past the unread grace, unread", "now() - interval '1 hour'", "now() - interval '1 hour'", "NULL", 1},
	} {
		t.Run(c.name, func(t *testing.T) {
			w, r := waitingWorld(t)
			w.exec(`UPDATE runs SET waiting_since = `+c.waiting+` WHERE id = $1`, w.run.ID)
			w.note(false)
			w.exec(`UPDATE directives SET claimed_at = `+c.sent+`, sent_at = `+c.sent+`, delivered_at = `+c.delivered+`
				WHERE id = 'dir_'||$1`, w.run.Org)
			if n := w.parkForPerson(r); n != c.parks {
				t.Errorf("%d run.parked events, want %d", n, c.parks)
			}
		})
	}
}

// The hold has a ceiling: two park graces (newsCeiling) from when the wait
// began (waitBegan: the oldest open ask, or the last resume), not from
// waiting_since, which each note's turn resets. Past it a Run with news is
// due its park as soon as it waits, however recent waiting_since, and the
// park does not wait for the news; one without keeps its grace. The
// factory's grace here is DefaultPolicy's 10 minutes.
func TestAPersonParksPastTheCeilingWithNewsUnheard(t *testing.T) {
	for _, c := range []struct {
		name, asked, resumed, waiting string
		news, parkNow                 bool
	}{
		{"asked inside the ceiling, waiting inside the grace", "now() - interval '19 minutes'", "", "now() - interval '1 minute'", true, false},
		{"asked past the ceiling, waiting inside the grace", "now() - interval '21 minutes'", "", "now() - interval '1 minute'", true, true},
		{"asked past the ceiling, resumed since", "now() - interval '21 minutes'", "now() - interval '5 minutes'", "now() - interval '1 minute'", true, false},
		// No news: its grace, as always.
		{"asked past the ceiling, no news, waiting inside the grace", "now() - interval '21 minutes'", "", "now() - interval '1 minute'", false, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			w, _ := waitingWorld(t)
			w.exec(`UPDATE questions SET asked_at = `+c.asked+` WHERE run_id = $1`, w.run.ID)
			w.exec(`UPDATE runs SET waiting_since = `+c.waiting+` WHERE id = $1`, w.run.ID)
			if c.resumed != "" {
				w.exec(`INSERT INTO run_resumes (run_id, organization_id, epoch, cause, requested_at)
					VALUES ($1, $2, 2, 'answer', `+c.resumed+`)`, w.run.ID, w.run.Org)
			}
			if c.news {
				w.note(false)
			}
			runs, err := w.s.due(w.ctx)
			if err != nil {
				t.Fatal(err)
			}
			var got *phaseRun
			for i := range runs {
				if runs[i].ID == w.run.ID {
					got = &runs[i]
				}
			}
			if got == nil {
				t.Fatal("the Run is not due")
			}
			if got.ParkNow != c.parkNow {
				t.Errorf("ParkNow = %v, want %v", got.ParkNow, c.parkNow)
			}
			want := 0
			if c.parkNow {
				want = 1
			}
			if n := w.parkForPerson(*got); got.ParkNow && n != want {
				t.Errorf("%d run.parked events, want %d", n, want)
			}
		})
	}
}
