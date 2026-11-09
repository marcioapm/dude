package phases

import (
	"strings"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
)

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

// The hold only holds off a park due without it, never adds one, and has a
// ceiling: two park graces (newsCeiling) from when the wait began
// (waitBegan: the oldest open ask, or the last resume — its run_resumes row
// or its run.unparked), not from waiting_since, which each note's turn
// resets. Inside the grace nothing parks, news or not, however old the ask:
// the note is delivered, as on main. Past the grace a Run with news inside
// the ceiling is held; past the ceiling it parks with its news unheard, as
// on main. Never mid-turn (waiting_since NULL). The factory's grace here is
// DefaultPolicy's 10 minutes.
func TestAPersonParkIsHeldForNewsOnlyPastTheGraceAndInsideTheCeiling(t *testing.T) {
	for _, c := range []struct {
		name, asked, waiting string
		// resumed: a run_resumes row; unparked: a run.unparked event.
		resumed, unparked string
		// other: another open ask of the Run (SQL, $1 the org, its Run
		// 'run_'||$1).
		other   string
		news    bool
		parkNow bool
	}{
		{name: "asked inside the ceiling, past the grace, news", asked: "19 minutes", waiting: pastGrace, news: true, parkNow: false},
		{name: "asked past the ceiling, past the grace, news", asked: "21 minutes", waiting: pastGrace, news: true, parkNow: true},
		{name: "asked past the ceiling, past the grace, no news", asked: "21 minutes", waiting: pastGrace, parkNow: true},
		{name: "asked inside the ceiling, past the grace, no news", asked: "19 minutes", waiting: pastGrace, parkNow: true},
		// Scenario 2: the agent asked, worked on, and its turn ended a minute ago.
		{name: "asked past the ceiling, inside the grace, news", asked: "21 minutes", waiting: "1 minute", news: true, parkNow: false},
		{name: "asked past the ceiling, inside the grace, no news", asked: "21 minutes", waiting: "1 minute", parkNow: false},
		// The mid-turn guard: no wait, no park, whatever the ask's age.
		{name: "asked past the ceiling, mid-turn, news", asked: "21 minutes", waiting: "", news: true, parkNow: false},
		// A resume begins a new wait: its run_resumes row, or its
		// run.unparked (takeUpLive writes no run_resumes row).
		{name: "asked past the ceiling, resumed since, past the grace, news", asked: "21 minutes", resumed: "15 minutes", waiting: pastGrace, news: true, parkNow: false},
		{name: "asked past the ceiling, unparked since, past the grace, news", asked: "21 minutes", unparked: "15 minutes", waiting: pastGrace, news: true, parkNow: false},
		// Scenario 1: resumed with the question open, a steer a minute later.
		{name: "asked past the ceiling, unparked since, inside the grace, news", asked: "21 minutes", unparked: "1 minute", waiting: "1 minute", news: true, parkNow: false},
		{name: "asked and unparked past the ceiling, past the grace, news", asked: "41 minutes", unparked: "21 minutes", waiting: pastGrace, news: true, parkNow: true},
		// The ceiling counts from the oldest open ask (OpenAskSince).
		{name: "question past the ceiling, a peer wait just opened, news", asked: "21 minutes", waiting: pastGrace, news: true, parkNow: true,
			other: `INSERT INTO tasks (id, organization_id, project_id, number, title, goal) VALUES ('wi_peer_'||$1, $1, 'prj_'||$1, 2, 'Peer', 'G');
				INSERT INTO peer_messages (id, organization_id, from_task_id, from_run_id, to_task_id, text) VALUES ('pm_'||$1, $1, 'wi_'||$1, 'run_'||$1, 'wi_peer_'||$1, 'hi');
				INSERT INTO peer_waits (id, organization_id, run_id, message_id, how, since) VALUES ('pw_'||$1, $1, 'run_'||$1, 'pm_'||$1, 'wait', now() - interval '1 minute')`},
		{name: "question past the ceiling, a blocking repository just requested, news", asked: "21 minutes", waiting: pastGrace, news: true, parkNow: true,
			other: `INSERT INTO repositories (id, organization_id, project_id, name, url) VALUES ('repo_'||$1, $1, 'prj_'||$1, 'r', 'git://x/r');
				INSERT INTO repository_requests (id, organization_id, task_id, run_id, repository_id, reason, blocking, created_at)
					VALUES ('rr_'||$1, $1, 'wi_'||$1, 'run_'||$1, 'repo_'||$1, 'needs it', true, now() - interval '1 minute')`},
		// An ask that does not say when (a checks escalation from before
		// raisedAt), alone: news does not hold its park, as on main.
		{name: "only a checks escalation without raisedAt, past the grace, news", asked: "", waiting: pastGrace, news: true, parkNow: true,
			other: `INSERT INTO workflow_runs (id, organization_id, task_id, workflow_type, idempotency_key, status, step, state)
				VALUES ('wf_'||$1, $1, 'wi_'||$1, '` + delivery.WorkflowType + `', 'k_'||$1, 'waiting', 'decide',
					jsonb_build_object('escalation', jsonb_build_object('reason', '` + delivery.ReasonChecksUnverified + `', 'detail', jsonb_build_object('runId', 'run_'||$1))))`},
	} {
		t.Run(c.name, func(t *testing.T) {
			w, _ := waitingWorld(t)
			if c.asked == "" {
				w.exec(`UPDATE questions SET status = 'answered', answer = 'yes', answered_at = now() WHERE run_id = $1`, w.run.ID)
			} else {
				w.exec(`UPDATE questions SET asked_at = now() - $2::interval WHERE run_id = $1`, w.run.ID, c.asked)
			}
			w.exec(`UPDATE runs SET waiting_since = now() - NULLIF($2, '')::interval WHERE id = $1`, w.run.ID, c.waiting)
			if c.resumed != "" {
				w.exec(`INSERT INTO run_resumes (run_id, organization_id, epoch, cause, requested_at)
					VALUES ($1, $2, 2, 'answer', now() - $3::interval)`, w.run.ID, w.run.Org, c.resumed)
			}
			if c.unparked != "" {
				w.exec(`INSERT INTO events (id, organization_id, run_id, event_type, actor_type, actor_id, source, payload, occurred_at)
					VALUES ('ev_unparked_'||$2, $2, $1, '`+evUnparked+`', 'system', 'dude', 'orchestrator', '{"reason":"person"}', now() - $3::interval)`,
					w.run.ID, w.run.Org, c.unparked)
			}
			if c.other != "" {
				for _, q := range strings.Split(c.other, ";") {
					w.exec(q, w.run.Org)
				}
			}
			if c.news {
				w.note(false)
			}
			got := w.dueRun()
			if got.ParkNow != c.parkNow {
				t.Errorf("ParkNow = %v, want %v", got.ParkNow, c.parkNow)
			}
			if c.waiting != pastGrace {
				return
			}
			// Past the grace, requestPause's re-check agrees with the
			// sweep's read. (Inside it the sweep never asks it.)
			want := 0
			if c.parkNow {
				want = 1
			}
			r := *got
			r.ParkNow = true
			if n := w.parkForPerson(r); n != want {
				t.Errorf("%d run.parked events, want %d", n, want)
			}
		})
	}
}

// pastGrace is a wait past the factory's grace (10 minutes).
const pastGrace = "11 minutes"

// dueRun is w's Run as the sweep reads it (due); the test fails if it is
// not due.
func (w *resumeWorld) dueRun() *phaseRun {
	w.t.Helper()
	runs, err := w.s.due(w.ctx)
	if err != nil {
		w.t.Fatal(err)
	}
	for i := range runs {
		if runs[i].ID == w.run.ID {
			return &runs[i]
		}
	}
	w.t.Fatal("the Run is not due")
	return nil
}

// requestPause's re-check reads the Run's own grace, as the sweep does: a
// project that lets an agent wait an hour holds its park for news until two
// hours from the ask, not the factory's twenty minutes.
func TestAPersonParkRechecksTheCeilingWithTheProjectsGrace(t *testing.T) {
	for _, c := range []struct {
		name, policy string
		parks        int
	}{
		{"the factory's grace", `{}`, 1},
		{"the project's hour", `{"parkAfterMinutes": 60}`, 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			w, r := waitingWorld(t)
			w.exec(`UPDATE projects SET delivery_policy = $2::jsonb WHERE id = $1`, w.run.ProjectID, c.policy)
			w.exec(`UPDATE questions SET asked_at = now() - interval '30 minutes' WHERE run_id = $1`, w.run.ID)
			w.note(false)
			if n := w.parkForPerson(r); n != c.parks {
				t.Errorf("%d run.parked events, want %d", n, c.parks)
			}
		})
	}
}
