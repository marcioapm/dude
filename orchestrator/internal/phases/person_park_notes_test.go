package phases

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/lux"
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

// peerWait opens a wait of w's Run on a peer's reply, since ago (SQL
// interval): an ask (delivery.OpenAsk), not a person's (PersonAsk).
func (w *resumeWorld) peerWait(since string) {
	w.t.Helper()
	w.exec(`INSERT INTO tasks (id, organization_id, project_id, number, title, goal) VALUES ('wi_peer_'||$1, $1, 'prj_'||$1, 2, 'Peer', 'G')`, w.run.Org)
	w.exec(`INSERT INTO peer_messages (id, organization_id, from_task_id, from_run_id, to_task_id, text)
		VALUES ('pm_'||$1, $1, 'wi_'||$1, 'run_'||$1, 'wi_peer_'||$1, 'hi')`, w.run.Org)
	w.exec(`INSERT INTO peer_waits (id, organization_id, run_id, message_id, how, since)
		VALUES ('pw_'||$1, $1, 'run_'||$1, 'pm_'||$1, 'wait', now() - $2::interval)`, w.run.Org, since)
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
// longer ago than delivery.UnreadGraceSecs. Nor one that failed, sent or
// never tried, nor one sent as the interrupt alone (interrupt_only): there
// is no note for the agent to take a turn for.
func TestAPersonParkWaitsForASentNotesTurn(t *testing.T) {
	for _, c := range []struct {
		name, waiting, sent, delivered string
		// also: more of the directive to set (SQL, after a comma).
		also  string
		parks int
	}{
		{"sent, unread", "now() - interval '1 hour'", "now()", "NULL", "", 0},
		{"read after the wait began", "now() - interval '1 hour'", "now()", "now()", "", 0},
		// Inside the unread grace: only the read's order with the wait parks it.
		{"read before the wait began", "now() - interval '30 seconds'", "now() - interval '60 seconds'", "now() - interval '50 seconds'", "", 1},
		{"read in the batch that began the wait", "now() - interval '30 seconds'", "now() - interval '31 seconds'", "(SELECT waiting_since FROM runs WHERE id = run_id)", "", 1},
		{"sent past the unread grace, unread", "now() - interval '1 hour'", "now() - interval '1 hour'", "NULL", "", 1},
		{"a failed note, never tried", "now() - interval '1 hour'", "NULL", "NULL", ", failed_at = now()", 1},
		{"sent, then failed", "now() - interval '1 hour'", "now()", "NULL", ", failed_at = now()", 1},
		{"sent, interrupt only, unread", "now() - interval '1 hour'", "now()", "NULL", ", interrupt_only = true", 1},
	} {
		t.Run(c.name, func(t *testing.T) {
			w, r := waitingWorld(t)
			w.exec(`UPDATE runs SET waiting_since = `+c.waiting+` WHERE id = $1`, w.run.ID)
			w.note(false)
			w.exec(`UPDATE directives SET claimed_at = `+c.sent+`, sent_at = `+c.sent+`, delivered_at = `+c.delivered+c.also+`
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
			if c.waiting != pastGrace && c.waiting != "" {
				return
			}
			// Past the grace or mid-turn, requestPause's re-check agrees
			// with the sweep's read. (Inside the grace it is never asked.)
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

// A person resumes a Run parked for a person whose question is still open,
// lux having placed it again itself (takeUpLive: no run_resumes row, no
// run.unparked, so the wait still counts from the ask, past the ceiling).
// A steer a minute later is inside the grace of its new wait: delivered,
// not parked over, as on main.
func TestASteerAfterALiveTakeUpIsHeardNotParkedOver(t *testing.T) {
	w := newResumeWorld(t)
	w.exec(`UPDATE questions SET status = 'open', answer = NULL, answered_at = NULL, asked_at = now() - interval '21 minutes'
		WHERE run_id = $1`, w.run.ID)
	w.exec(`UPDATE runs SET control = 'resume', lux_state = 'running' WHERE id = $1`, w.run.ID)
	r := w.run
	r.Control, r.Waiting = "resume", true
	if taken, changes, err := w.s.takeUpLive(w.ctx, r, "running"); err != nil || !taken || changes {
		t.Fatalf("taken %v, changes %v, err %v; want taken", taken, changes, err)
	}
	w.exec(`UPDATE runs SET waiting_since = now() - interval '1 minute' WHERE id = $1`, w.run.ID)
	w.note(false)
	if got := w.dueRun(); got.ParkNow {
		t.Errorf("ParkNow inside the grace of the resumed wait, the steer unheard")
	}
}

// A park held for news in this wait (runs.news_hold_at, newsHeld), row by
// row, as due and requestPause read it. The wait began when the question
// was asked, 3 minutes ago (inside the ceiling); its grace is the
// factory's 10 minutes. Held in this wait, the Run is due its park as soon
// as it waits with what was queued by the hold heard, grace or not; a note
// queued after the hold does not hold the park off (it waits for the
// answer: not HasDirectives), but one queued before does. A hold from
// before the wait began (an older ask's wait, a resume since) is spent:
// the wait is as on main and #438, its park due only past its grace, and
// held for a note then (HoldNews). A held park whose person's ask was
// answered leaves the Run's notes to deliver. Mid-turn, nothing parks.
func TestAPersonParkHeldForNewsRowByRow(t *testing.T) {
	for _, c := range []struct {
		name string
		// hold: news_hold_at (SQL interval ago, "" for NULL); waiting: how
		// long since waiting_since ("" mid-turn); note: when a note was
		// queued ("" none); resumed: a run_resumes row that long ago.
		hold, waiting, note, resumed string
		// also: more of the note to set (SQL, after a comma).
		also string
		// answered: the question answered a minute ago; asked: a new
		// question since, that long ago; peer: a peer wait open since 5
		// minutes ago, older than the question.
		answered                      bool
		asked                         string
		peer                          bool
		parkNow, holdNews, directives bool
	}{
		{name: "never held, inside the grace, a note", waiting: "1 minute", note: "now()", directives: true},
		{name: "never held, past the grace, a note", waiting: pastGrace, note: "now()", holdNews: true, directives: true},
		{name: "never held, past the grace, no note", waiting: pastGrace, parkNow: true},
		{name: "held, inside the grace, no note", hold: "2 minutes", waiting: "1 minute", parkNow: true},
		{name: "held, inside the grace, a note queued after the hold", hold: "2 minutes", waiting: "1 minute", note: "now()", parkNow: true},
		{name: "held, inside the grace, a note queued before the hold", hold: "2 minutes", waiting: "1 minute",
			note: "now() - interval '150 seconds'", directives: true},
		{name: "held, mid-turn, a note queued after the hold", hold: "2 minutes", note: "now()"},
		{name: "held before a resume, inside the grace, a note", hold: "2 minutes", resumed: "1 minute", waiting: "1 minute",
			note: "now()", directives: true},
		{name: "held before a resume, past the grace, a note", hold: "2 minutes", resumed: "1 minute", waiting: pastGrace,
			note: "now()", holdNews: true, directives: true},
		{name: "held before the ask, inside the grace, a note", hold: "4 minutes", waiting: "1 minute", note: "now()", directives: true},
		{name: "held, answered since, a note queued after the hold", hold: "2 minutes", waiting: "1 minute", note: "now()",
			answered: true, directives: true},
		// Interrupts and turn retries are not held for the answer, and do
		// not hold the park off (newsCutoff).
		{name: "held, inside the grace, an interrupt queued after the hold", hold: "2 minutes", waiting: "1 minute", note: "now()",
			also: "interrupt = true", parkNow: true, directives: true},
		{name: "held, inside the grace, a retry queued after the hold, due", hold: "2 minutes", waiting: "1 minute", note: "now()",
			also: "deliver_after = now() - interval '1 second'", parkNow: true, directives: true},
		// What was queued by the hold holds the park off, at the hold too.
		{name: "held, inside the grace, a note queued at the hold, unsent and unclaimed", hold: "2 minutes", waiting: "1 minute",
			note: "(SELECT news_hold_at FROM runs WHERE id = run_id)", directives: true},
		// The wait begins with the question: a hold made then is this wait's.
		{name: "held as the question was asked, inside the grace, no note", hold: "3 minutes", waiting: "1 minute", parkNow: true},
		// Waiting only on a peer: what comes after the hold is heard, as a
		// parked Run is resumed for it (resumable), not parked over.
		{name: "held, waiting only on an older peer wait, a note queued after the hold", hold: "2 minutes", waiting: "1 minute",
			note: "now()", answered: true, peer: true, directives: true},
		// A hold with nothing open has no effect: nothing to park for.
		{name: "held, nothing open, a resume before the hold, a note queued after the hold", hold: "2 minutes", resumed: "5 minutes",
			waiting: "1 minute", note: "now()", answered: true, directives: true},
		// The reviewer's repro: the held question answered, its answer
		// heard (a note sent after the hold spends it, holdSpent), an
		// older peer wait still open so waitBegan does not move. The
		// answer's turn ends in a wait with a full grace, as on main;
		// so does a new question's.
		{name: "held, answered, older peer wait still open, no new question", hold: "2 minutes", waiting: "10 seconds",
			note: "now() - interval '1 minute'", also: "sent_at = now() - interval '55 seconds', delivered_at = now() - interval '50 seconds'",
			answered: true, peer: true},
		{name: "held, answered, older peer wait still open, a new question", hold: "2 minutes", waiting: "10 seconds",
			note: "now() - interval '1 minute'", also: "sent_at = now() - interval '55 seconds', delivered_at = now() - interval '50 seconds'",
			answered: true, peer: true, asked: "30 seconds"},
		// Not spent by what serves the held note's turn: an interrupt of
		// it, a retry of it, sent after the hold.
		{name: "held, inside the grace, an interrupt sent after the hold", hold: "2 minutes", waiting: "10 seconds",
			note: "now() - interval '1 minute'", also: "interrupt = true, sent_at = now() - interval '55 seconds', delivered_at = now() - interval '50 seconds'",
			parkNow: true},
		{name: "held, inside the grace, a retry sent after the hold", hold: "2 minutes", waiting: "10 seconds",
			note: "now() - interval '1 minute'", also: "deliver_after = now() - interval '58 seconds', sent_at = now() - interval '55 seconds', delivered_at = now() - interval '50 seconds'",
			parkNow: true},
		// Nor held off by one, unread: only what was queued by the hold
		// holds the park off (newsCutoff); the stop un-sends it for the
		// answer (unsendUnread).
		{name: "held, inside the grace, an interrupt sent after the hold, unread", hold: "2 minutes", waiting: "10 seconds",
			note: "now() - interval '1 minute'", also: "interrupt = true, claimed_at = now() - interval '5 seconds', sent_at = now() - interval '5 seconds'",
			parkNow: true},
	} {
		t.Run(c.name, func(t *testing.T) {
			w, _ := waitingWorld(t)
			w.exec(`UPDATE questions SET asked_at = now() - interval '3 minutes' WHERE run_id = $1`, w.run.ID)
			if c.answered {
				w.exec(`UPDATE questions SET status = 'answered', answer = 'yes', answered_at = now() - interval '1 minute' WHERE run_id = $1`, w.run.ID)
			}
			if c.asked != "" {
				w.exec(`INSERT INTO questions (id, organization_id, task_id, run_id, prompt, status, asked_at)
					VALUES ('q2_'||$1, $1, 'wi_'||$1, 'run_'||$1, 'And now?', 'open', now() - $2::interval)`, w.run.Org, c.asked)
			}
			if c.peer {
				w.peerWait("5 minutes")
			}
			// A hold of "3 minutes" is the question's asked_at exactly.
			w.exec(`UPDATE runs SET waiting_since = now() - NULLIF($2, '')::interval,
				news_hold_at = CASE WHEN $3 = '3 minutes' THEN (SELECT asked_at FROM questions WHERE id = 'q_'||organization_id)
					ELSE now() - NULLIF($3, '')::interval END WHERE id = $1`, w.run.ID, c.waiting, c.hold)
			if c.resumed != "" {
				w.exec(`INSERT INTO run_resumes (run_id, organization_id, epoch, cause, requested_at)
					VALUES ($1, $2, 2, 'answer', now() - $3::interval)`, w.run.ID, w.run.Org, c.resumed)
			}
			if c.note != "" {
				w.note(false)
				also := ""
				if c.also != "" {
					also = ", " + c.also
				}
				w.exec(`UPDATE directives SET created_at = `+c.note+also+` WHERE id = 'dir_'||$1`, w.run.Org)
			}
			got := w.dueRun()
			if got.ParkNow != c.parkNow || got.HoldNews != c.holdNews || got.HasDirectives != c.directives {
				t.Errorf("ParkNow %v, HoldNews %v, HasDirectives %v; want %v, %v, %v",
					got.ParkNow, got.HoldNews, got.HasDirectives, c.parkNow, c.holdNews, c.directives)
			}
			if c.answered {
				return
			}
			// requestPause's re-check agrees with the sweep's read; it is
			// asked only of a Run the sweep read as due.
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

// holdForNews records the hold once per wait, only for a Run due the park
// main would make (past its grace, an open ask); pause and resume clear it.
func TestAHoldForNewsIsRecordedOncePerWaitAndClearedByAPark(t *testing.T) {
	w, r := waitingWorld(t)
	w.note(false)
	got := w.dueRun()
	if !got.HoldNews || got.ParkNow {
		t.Fatalf("HoldNews %v, ParkNow %v; want held, not parked", got.HoldNews, got.ParkNow)
	}
	if err := w.s.holdForNews(w.ctx, *got); err != nil {
		t.Fatal(err)
	}
	first := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND news_hold_at IS NOT NULL`)
	if first != 1 {
		t.Fatalf("the hold was not recorded")
	}
	var at1, at2 string
	_ = w.owner.QueryRow(w.ctx, `SELECT news_hold_at::text FROM runs WHERE id = $1`, w.run.ID).Scan(&at1)
	if err := w.s.holdForNews(w.ctx, *got); err != nil {
		t.Fatal(err)
	}
	_ = w.owner.QueryRow(w.ctx, `SELECT news_hold_at::text FROM runs WHERE id = $1`, w.run.ID).Scan(&at2)
	if at1 != at2 {
		t.Errorf("the hold moved from %s to %s within its wait", at1, at2)
	}
	if got := w.dueRun(); got.HoldNews || got.ParkNow || !got.HasDirectives {
		t.Errorf("held, its note pending: HoldNews %v, ParkNow %v, HasDirectives %v; want the note delivered first",
			got.HoldNews, got.ParkNow, got.HasDirectives)
	}
	// Inside the grace the hold is never recorded.
	w2, _ := waitingWorld(t)
	w2.exec(`UPDATE runs SET waiting_since = now() - interval '1 minute' WHERE id = $1`, w2.run.ID)
	if err := w2.s.holdForNews(w2.ctx, w2.run); err != nil {
		t.Fatal(err)
	}
	if n := w2.count(`SELECT count(*) FROM runs WHERE id = $1 AND news_hold_at IS NOT NULL`); n != 0 {
		t.Errorf("a hold recorded inside the grace")
	}
	// Nor past the grace with no ask open: nothing to park for.
	w3, _ := waitingWorld(t)
	w3.exec(`UPDATE questions SET status = 'answered', answer = 'yes', answered_at = now() WHERE run_id = $1`, w3.run.ID)
	if err := w3.s.holdForNews(w3.ctx, w3.run); err != nil {
		t.Fatal(err)
	}
	if n := w3.count(`SELECT count(*) FROM runs WHERE id = $1 AND news_hold_at IS NOT NULL`); n != 0 {
		t.Errorf("a hold recorded with no ask open")
	}
	// Spent by a note sent after it (holdSpent) while the turn it starts
	// has not come yet: the Run still waits in the idle the hold was taken
	// in, past its grace, that note unheard (HoldNews). The hold is not
	// taken again: one recorded now would come after that note, which
	// would no longer spend it, and the Run would park as soon as its turn
	// ended, a grace earlier than main. A later idle's may.
	w4, _ := waitingWorld(t)
	w4.exec(`UPDATE runs SET news_hold_at = now() - interval '30 seconds' WHERE id = $1`, w4.run.ID)
	w4.note(false)
	w4.exec(`UPDATE directives SET created_at = now() - interval '20 seconds', claimed_at = now() - interval '10 seconds',
		sent_at = now() - interval '10 seconds' WHERE id = 'dir_'||$1`, w4.run.Org)
	spent := w4.dueRun()
	if !spent.HoldNews {
		t.Fatalf("spent, past the grace, its note unheard: HoldNews %v, ParkNow %v; want HoldNews", spent.HoldNews, spent.ParkNow)
	}
	if err := w4.s.holdForNews(w4.ctx, *spent); err != nil {
		t.Fatal(err)
	}
	if n := w4.count(`SELECT count(*) FROM runs WHERE id = $1 AND news_hold_at < now() - interval '25 seconds'`); n != 1 {
		t.Errorf("a spent hold was taken again before its note's turn")
	}
	w4.exec(`UPDATE runs SET waiting_since = now() - interval '11 minutes' + interval '5 seconds',
		news_hold_at = now() - interval '11 minutes' WHERE id = $1`, w4.run.ID)
	if err := w4.s.holdForNews(w4.ctx, *spent); err != nil {
		t.Fatal(err)
	}
	if n := w4.count(`SELECT count(*) FROM runs WHERE id = $1 AND news_hold_at > now() - interval '5 seconds'`); n != 1 {
		t.Errorf("a hold from an earlier idle kept the later one from being recorded")
	}
	// The park clears it.
	w.exec(`UPDATE directives SET sent_at = now() - interval '1 hour', delivered_at = now() - interval '1 hour' WHERE run_id = $1`, w.run.ID)
	w.exec(`UPDATE runs SET control = 'pause_graceful' WHERE id = $1`, w.run.ID)
	// lux has it stopped already: pause asks lux nothing.
	r.Control, r.LuxState = "pause_graceful", "stopped"
	if err := w.s.pause(w.ctx, r); err != nil {
		t.Fatal(err)
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'paused' AND news_hold_at IS NULL`); n != 1 {
		t.Errorf("paused with its wait's hold kept")
	}
}

// Every other way a wait ends clears the hold too, each starting from one
// recorded: lux stopping the Run itself (parkStopped), a resume
// (whilePaused), and a take-up of a Run lux placed again (takeUpLive).
func TestAHoldForNewsIsClearedByAStopAResumeAndATakeUp(t *testing.T) {
	held := func(w *resumeWorld) {
		w.t.Helper()
		w.exec(`UPDATE runs SET news_hold_at = now() - interval '1 minute' WHERE id = $1`, w.run.ID)
	}
	cleared := func(w *resumeWorld, what string) {
		w.t.Helper()
		if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND news_hold_at IS NULL`); n != 1 {
			w.t.Errorf("%s kept the wait's hold for news", what)
		}
	}
	t.Run("parkStopped", func(t *testing.T) {
		w, r := waitingWorld(t)
		w.exec(`UPDATE runs SET lux_state = 'stopped', agent_session_epoch = 1 WHERE id = $1`, w.run.ID)
		held(w)
		r.LuxState = "stopped"
		if parked, err := w.s.parkStopped(w.ctx, r); err != nil || !parked {
			t.Fatalf("parked %v, err %v; want parked", parked, err)
		}
		cleared(w, "parkStopped")
	})
	t.Run("whilePaused's resume", func(t *testing.T) {
		w := newResumeWorld(t)
		w.resumable()
		w.lux.set(stoppedOnHost1(time.Now().Add(-time.Minute).UTC()), nil)
		w.s.Lux = &releasingLux{placementLux: w.lux, release: func() {}}
		held(w)
		w.whilePaused()
		if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'running'`); n != 1 {
			t.Fatalf("not resumed")
		}
		cleared(w, "the resume")
	})
	t.Run("takeUpLive", func(t *testing.T) {
		w := newResumeWorld(t)
		w.exec(`UPDATE questions SET status = 'open', answer = NULL, answered_at = NULL WHERE run_id = $1`, w.run.ID)
		w.exec(`UPDATE runs SET control = 'resume', lux_state = 'running' WHERE id = $1`, w.run.ID)
		held(w)
		r := w.run
		r.Control, r.Waiting = "resume", true
		if taken, _, err := w.s.takeUpLive(w.ctx, r, "running"); err != nil || !taken {
			t.Fatalf("taken %v, err %v; want taken", taken, err)
		}
		cleared(w, "takeUpLive")
	})
}

// inputLux records what deliverDirectives sends.
type inputLux struct {
	*placementLux
	sent []string
}

func (f *inputLux) Input(_ context.Context, _ string, in lux.InputRequest) error {
	f.sent = append(f.sent, in.RequestID)
	return nil
}

// deliverDirectives re-checks the hold itself, not only through the
// sweep's HasDirectives: a note queued after the park was held for news,
// in its wait, with the person's question open, is not sent — it waits for
// the answer — though the sweep read the Run with something to send (the
// note queued before the hold, since heard). One queued before the hold
// is sent, and once the question is answered so is the later one.
func TestDeliverDirectivesHoldsWhatCameAfterTheHoldForNews(t *testing.T) {
	w, r := waitingWorld(t)
	fake := &inputLux{placementLux: w.lux}
	w.s.Lux = fake
	w.exec(`UPDATE questions SET asked_at = now() - interval '3 minutes' WHERE run_id = $1`, w.run.ID)
	w.exec(`UPDATE runs SET news_hold_at = now() - interval '2 minutes', lux_run_id = 'lrun_1' WHERE id = $1`, w.run.ID)
	w.exec(`INSERT INTO directives (id, organization_id, task_id, run_id, text, created_at)
		VALUES ('dir_before_'||$1, $1, $2, $3, 'before', now() - interval '150 seconds'),
		       ('dir_after_'||$1, $1, $2, $3, 'after', now() - interval '1 minute')`, w.run.Org, w.run.TaskID, w.run.ID)
	r.HasDirectives = true
	if _, err := w.s.deliverDirectives(w.ctx, r); err != nil {
		t.Fatal(err)
	}
	if want := []string{"dir_before_" + w.run.Org}; !slices.Equal(fake.sent, want) {
		t.Fatalf("sent %v, want %v: only what came before the hold", fake.sent, want)
	}
	w.exec(`UPDATE questions SET status = 'answered', answer = 'yes', answered_at = now() WHERE run_id = $1`, w.run.ID)
	if _, err := w.s.deliverDirectives(w.ctx, r); err != nil {
		t.Fatal(err)
	}
	if want := []string{"dir_before_" + w.run.Org, "dir_after_" + w.run.Org}; !slices.Equal(fake.sent, want) {
		t.Errorf("sent %v, want %v: the answer brings what was held for it", fake.sent, want)
	}
}

// What deliverDirectives does not hold for the answer, though queued after
// the hold with the question open: an interrupt and a due turn retry
// (they serve the turn under way). And with a hold from before the wait
// began (the question asked since), nothing is held: that hold is spent.
func TestDeliverDirectivesSendsInterruptsRetriesAndWhatAStaleHoldDoesNotHold(t *testing.T) {
	for _, c := range []struct {
		name string
		// hold: news_hold_at, ago; also: more of the note queued a minute
		// ago (SQL, after a comma).
		hold, also string
	}{
		{"an interrupt queued after the hold", "2 minutes", ", interrupt = true"},
		{"a retry queued after the hold, due", "2 minutes", ", deliver_after = now() - interval '1 second'"},
		{"a note after a hold from before the question", "4 minutes", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			w, r := waitingWorld(t)
			fake := &inputLux{placementLux: w.lux}
			w.s.Lux = fake
			w.exec(`UPDATE questions SET asked_at = now() - interval '3 minutes' WHERE run_id = $1`, w.run.ID)
			w.exec(`UPDATE runs SET news_hold_at = now() - $2::interval, lux_run_id = 'lrun_1' WHERE id = $1`, w.run.ID, c.hold)
			w.note(false)
			w.exec(`UPDATE directives SET created_at = now() - interval '1 minute'`+c.also+` WHERE id = 'dir_'||$1`, w.run.Org)
			r.HasDirectives = true
			if _, err := w.s.deliverDirectives(w.ctx, r); err != nil {
				t.Fatal(err)
			}
			if want := []string{"dir_" + w.run.Org}; !slices.Equal(fake.sent, want) {
				t.Errorf("sent %v, want %v", fake.sent, want)
			}
		})
	}
}
