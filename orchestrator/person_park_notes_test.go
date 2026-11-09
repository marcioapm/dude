package orchestrator_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
)

// A Run due its park for a person (past its grace) is not parked while dude
// has a note for it the agent has not heard (requestPause, due's ParkNow):
// the note goes first, its turn runs, and the Run is parked once it waits
// again, after the grace. Inside the grace nothing parks, as on main. A
// note held for an answer (HeldBack) does not hold the park off, and one
// that arrives after the park waits for the answer, as on main. The hold
// is bounded: by a note's first send, and by a ceiling from the wait's
// start, past which the park is as on main.

// noteFor is the directive carrying the wake note that told reason key,
// "" for none yet.
func (w *world) noteFor(task, key string) string {
	w.t.Helper()
	return w.str(`SELECT COALESCE((SELECT a.directive_id FROM conductor_wake_attempts a JOIN conductor_wakes k ON k.id = a.wake_id
		WHERE k.task_id = $1 AND k.key = $2 AND a.directive_id IS NOT NULL ORDER BY a.created_at DESC LIMIT 1), '')`, task, key)
}

// directiveDelivered says whether directive id was delivered to the agent.
func (w *world) directiveDelivered(id string) bool {
	w.t.Helper()
	return w.count(`SELECT count(*) FROM directives WHERE id = $1 AND delivered_at IS NOT NULL`, id) == 1
}

// parks is how many times run was parked for a person.
func (w *world) parks(run string) int {
	w.t.Helper()
	return w.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = 'run.parked' AND payload->>'reason' = 'person'`, run)
}

// waitsAfter says whether run waits again after directive id was
// delivered: the turn that read it is over.
func (w *world) waitsAfter(run, id string) bool {
	w.t.Helper()
	return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND waiting_since IS NOT NULL
		AND waiting_since >= (SELECT delivered_at FROM directives WHERE id = $2)`, run, id) == 1
}

// parkedPastGrace says whether run's park came at least grace into the
// wait it parked in.
func (w *world) parkedPastGrace(run string, grace time.Duration) bool {
	w.t.Helper()
	return w.count(`SELECT count(*) FROM runs r, events p WHERE r.id = $1 AND p.run_id = r.id AND p.event_type = 'run.parked'
		AND (p.payload->>'parkedAt')::timestamptz >= r.waiting_since + make_interval(secs => $2)`, run, grace.Seconds()) == 1
}

// questionOpen fails the test if q is no longer open.
func (w *world) questionOpen(q, when string) {
	w.t.Helper()
	if st := w.str(`SELECT status::text FROM questions WHERE id = $1`, q); st != "open" {
		w.t.Fatalf("the question is %s %s, want open", st, when)
	}
}

// onFailureShowsDirectives logs run's directives and wait if the test fails.
func (w *world) onFailureShowsDirectives(run string) {
	w.t.Cleanup(func() {
		if !w.t.Failed() {
			return
		}
		w.t.Logf("the Run: %s\nits directives:\n%s", w.str(`SELECT format('control=%s status=%s waiting_since=%s turn_done=%s busy=%s now=%s',
			control, status, waiting_since, turn_done_at, agent_busy_at, now()) FROM runs WHERE id = $1`, run),
			w.str(`SELECT COALESCE(string_agg(format('%s created=%s sent=%s accepted=%s delivered=%s failed=%s held_for=%s interrupt_only=%s %s',
				id, created_at, sent_at, accepted_at, delivered_at, failed_at, held_for, interrupt_only, left(text, 60)), E'\n' ORDER BY created_at), '')
				FROM directives WHERE run_id = $1`, run))
	})
}

// The issue's case: a conductor waiting on the person, past its grace, and
// a wake note due in the same sweep. The reason is recorded only once the
// conductor waits with everything heard, so the note and the park are due
// together.
func TestAPersonParkWaitsUntilDudesNoteIsHeard(t *testing.T) {
	w := conducting(t)
	task, old, q := w.waitingAtGate(`{"action":"ask_person"}`)
	w.onFailureShowsDirectives(old)
	said := len(w.said(old))
	// Past any grace, and the reason past the wake window: both due now.
	// The question asked now: the hold's ceiling (two graces) is ahead.
	const grace = 2 * time.Second
	w.syncer.ParkAfter = grace
	w.waitingOn(old, q, time.Hour, 0)
	w.reason(task, "test:news", "CI failed on the base", time.Minute)

	var note string
	w.until("the note delivered, or the Run parked", func() bool {
		w.questionOpen(q, "before the note was heard")
		note = w.noteFor(task, "test:news")
		return w.parks(old) > 0 || w.count(`SELECT count(*) FROM runs WHERE id = $1 AND control = 'pause_graceful'`, old) > 0 ||
			(note != "" && w.directiveDelivered(note))
	})
	if note == "" || !w.directiveDelivered(note) {
		t.Fatalf("parked for the person with dude's note unheard (directive %q):\n%s", note, w.describeRuns())
	}
	// The turn that read it ended at or after the read: the fake agent
	// reads it and ends the turn in one batch, at the same now().
	w.until("the note's turn over, waiting on the person again", func() bool {
		w.questionOpen(q, "during the note's turn")
		return len(w.said(old)) > said && w.waitsAfter(old, note) || w.parks(old) > 0
	})
	// The park was held for the note once in this wait (newsHeld): it
	// comes as soon as the note's turn is over, not a whole grace later.
	// An hour's grace from here would never end inside the test.
	w.syncer.ParkAfter = time.Hour
	w.untilParked(old)
	w.questionOpen(q, "once parked")
	// Parked after the note's turn, and after the grace of its new wait.
	if n := w.count(`SELECT count(*) FROM events p, events d WHERE p.run_id = $1 AND p.event_type = 'run.parked'
		AND d.run_id = $1 AND d.event_type = 'run.directive.delivered' AND d.payload->>'directiveId' = $2 AND d.cursor < p.cursor`,
		old, note); n != 1 {
		t.Errorf("%d parks after the note was delivered, want 1", n)
	}
	if n := w.count(`SELECT count(*) FROM events p, events m WHERE p.run_id = $1 AND p.event_type = 'run.parked'
		AND m.run_id = $1 AND m.event_type = 'agent.message' AND m.cursor < p.cursor
		AND m.cursor > (SELECT d.cursor FROM events d WHERE d.run_id = $1 AND d.event_type = 'run.directive.delivered'
			AND d.payload->>'directiveId' = $2)`, old, note); n == 0 {
		t.Error("parked before the agent's turn for the note said anything")
	}
	for range 5 {
		w.pump()
	}
	if n := w.parks(old); n != 1 {
		t.Errorf("parked %d times, want once", n)
	}
	if n := w.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = 'run.unparked'`, old); n != 0 {
		t.Errorf("%d unparks with the question unanswered", n)
	}
	if n := w.unheard(old); n != 0 {
		t.Errorf("%d directives unheard at the park", n)
	}
	w.questionOpen(q, "after the park")
}

// waitingOn moves run's wait on the person back by waited, and its
// question q's ask to asked ago (0: now, so the ceiling of unheardNews is
// ahead). What it was sent before is moved back as far, so it was still
// read before the wait began.
func (w *world) waitingOn(run, q string, waited, asked time.Duration) {
	w.t.Helper()
	mustExec(w.t, w.owner, `UPDATE runs SET waiting_since = waiting_since - make_interval(secs => $2) WHERE id = $1`, run, waited.Seconds())
	mustExec(w.t, w.owner, `UPDATE directives SET sent_at = sent_at - make_interval(secs => $2),
		delivered_at = delivered_at - make_interval(secs => $2) WHERE run_id = $1`, run, waited.Seconds())
	mustExec(w.t, w.owner, `UPDATE questions SET asked_at = now() - make_interval(secs => $2) WHERE id = $1`, q, asked.Seconds())
}

// lux refuses every input to the Run (a 503, as BeforeInput refuses): the
// note is claimed, its send fails and is retried, and it is never sent. Its
// hold on the park is bounded by delivery.UnreadGraceSecs from its first
// try (claimed_at): past it the Run is parked once, as without news.
func TestAPersonParkDoesNotWaitForANoteLuxKeepsRefusing(t *testing.T) {
	w := conducting(t)
	task, old, q := w.waitingAtGate(`{"action":"ask_person"}`)
	w.onFailureShowsDirectives(old)
	w.syncer.RetryAhead = time.Minute
	luxRun := w.luxRunOf(old)
	w.lux.BeforeInput = func(runID, _ string) bool { return runID != luxRun }
	// Past the grace, far inside the ceiling: only the claim's bound ends the hold.
	w.syncer.ParkAfter = time.Minute
	w.waitingOn(old, q, time.Hour, 0)
	w.reason(task, "test:news", "CI failed on the base", time.Minute)
	var note string
	w.until("the note tried and refused", func() bool {
		note = w.noteFor(task, "test:news")
		return note != "" && w.count(`SELECT count(*) FROM directives WHERE id = $1 AND claimed_at IS NOT NULL AND sent_at IS NULL`, note) == 1 &&
			w.count(`SELECT count(*) FROM runs WHERE id = $1 AND retry_failures > 0`, old) == 1
	})
	for range 3 {
		w.pump()
	}
	if n := w.parks(old); n != 0 {
		t.Fatalf("parked %d times inside the bound, with the note's send under way", n)
	}
	// The bound passes: first tried longer ago than delivery.UnreadGraceSecs.
	mustExec(t, w.owner, `UPDATE directives SET claimed_at = now() - interval '121 seconds' WHERE id = $1`, note)
	w.untilParked(old)
	for range 5 {
		w.pump()
	}
	if n := w.parks(old); n != 1 {
		t.Errorf("parked %d times, want once", n)
	}
	w.questionOpen(q, "once parked")
}

// Notes that keep coming, on a clock the test sets: the grace is a minute,
// so a wait is past it only when the test moves waiting_since (and the ask)
// back, and the sweep runs only when the test pumps. A note inside the
// grace, then the ask past the ceiling (two graces from the ask), a note
// inside the grace of the wait that turn ended in: each heard, not parked
// over, as on main, and the park never held. Then a note as that note's
// wait is past its grace: past the ceiling it does not hold the park, and
// the Run parks once, with it left for the answer, as without news. (A
// park held inside the ceiling ends sooner: the tests below.)
func TestAPersonParkComesByTheCeilingWhileNotesKeepComing(t *testing.T) {
	w := conducting(t)
	task, old, q := w.waitingAtGate(`{"action":"ask_person"}`)
	w.onFailureShowsDirectives(old)
	const grace, ε = time.Minute, time.Second

	w.syncer.ParkAfter = grace
	// Inside the grace, inside the ceiling: heard.
	w.waitingOn(old, q, grace/2, grace+ε)
	held := w.note(task, old, "news 1")
	w.until("the note inside the grace heard, its turn over, or the Run parked", func() bool { return w.parks(old) > 0 || w.waitsAfter(old, held) })
	if n := w.parks(old); n != 0 {
		t.Fatalf("parked %d times inside the grace", n)
	}

	// Inside the grace of the new wait, the ask past the ceiling: the note
	// is heard and the Run not parked over it.
	w.waitingOn(old, q, grace/4, 2*grace+ε)
	inside := w.note(task, old, "news 2")
	w.until("the note inside the grace heard, its turn over, or the Run parked", func() bool { return w.parks(old) > 0 || w.waitsAfter(old, inside) })
	if n := w.parks(old); n != 0 {
		t.Fatalf("parked %d times inside the grace, past the ceiling", n)
	}

	// Past the grace of that wait, past the ceiling: parked, the note left.
	w.waitingOn(old, q, grace+ε, 3*grace+ε)
	last := w.note(task, old, "news 3")
	w.untilParked(old)
	for range 5 {
		w.pump()
	}
	if n := w.parks(old); n != 1 {
		t.Errorf("parked %d times, want once", n)
	}
	for _, id := range []string{held, inside} {
		if !w.directiveDelivered(id) {
			t.Errorf("parked with %s, a note before the ceiling's park, unheard", id)
		}
	}
	if w.directiveDelivered(last) || w.unheard(old) != 1 {
		t.Errorf("the note past the ceiling heard, or %d notes left for the answer, want it alone", w.unheard(old))
	}
	if !w.parkedPastGrace(old, grace) {
		t.Errorf("parked inside the grace of its wait")
	}
	w.note(task, old, "the base moved on")
	for range 5 {
		w.pump()
	}
	if n := w.parks(old); n != 1 {
		t.Errorf("parked %d times, want once", n)
	}
	if resumed, _ := w.luxResumes(old); resumed != 0 {
		t.Errorf("resumed %d times for a note, the question unanswered", resumed)
	}
	w.questionOpen(q, "after the park")
}

// heldPast holds old's park for a note: its wait and ask moved past the
// grace (inside the ceiling), a note queued, and the sweep run until the
// note's turn is over or the Run parked. Returns the note.
func (w *world) heldPast(task, old, q string, grace time.Duration) string {
	w.t.Helper()
	w.waitingOn(old, q, grace+time.Second, grace+time.Second)
	held := w.note(task, old, "news before the park")
	w.until("the held note's turn over, or the Run parked", func() bool {
		return w.parks(old) > 0 || w.directiveDelivered(held) && w.waitsAfter(old, held)
	})
	if !w.directiveDelivered(held) {
		w.t.Fatalf("parked with the note due at the park unheard")
	}
	return held
}

// unsent says whether directive id is still queued: not sent, not settled.
func (w *world) unsent(id string) bool {
	w.t.Helper()
	return w.count(`SELECT count(*) FROM directives WHERE id = $1 AND sent_at IS NULL AND delivered_at IS NULL AND failed_at IS NULL`, id) == 1
}

// parkedAt is when run's last park began (run.parked's parkedAt).
func (w *world) parkedAt(run string) time.Time {
	w.t.Helper()
	var at time.Time
	if err := w.owner.QueryRow(context.Background(), `SELECT (payload->>'parkedAt')::timestamptz FROM events
		WHERE run_id = $1 AND event_type = 'run.parked' ORDER BY cursor DESC LIMIT 1`, run).Scan(&at); err != nil {
		w.t.Fatal(err)
	}
	return at
}

// The cost #438 left open, bounded: a park held for a note, then notes
// that each arrive half a grace into the wait the last one's turn ended
// in. Without the bound each is heard inside a new grace and the Run never
// parks. With it, the park was held once in this wait (newsHeld): the Run
// parks as soon as the held note's turn is over — main's park time, that
// turn, and a sweep — and the later notes wait for the answer, as on main.
// The clock is the test's: the grace is a minute, and only waitingOn moves
// a wait past part of it.
func TestAPersonParkHeldForNewsParksOnceItsNoteIsHeardThoughNotesKeepComing(t *testing.T) {
	w := conducting(t)
	task, old, q := w.waitingAtGate(`{"action":"ask_person"}`)
	w.onFailureShowsDirectives(old)
	const grace = time.Minute
	w.syncer.ParkAfter = grace
	held := w.heldPast(task, old, q, grace)

	var later []string
	for k := range 4 {
		if w.parks(old) == 0 {
			// Half a grace into the wait the last turn ended in.
			w.waitingOn(old, q, grace/2, grace+time.Second+time.Duration(k+1)*grace/2)
		}
		note := w.note(task, old, fmt.Sprintf("news %d after the hold", k+1))
		later = append(later, note)
		w.until("the note heard and its turn over, or the Run parked", func() bool {
			return w.parks(old) > 0 || w.directiveDelivered(note) && w.waitsAfter(old, note)
		})
	}
	w.untilParked(old)
	for range 5 {
		w.pump()
	}
	if n := w.parks(old); n != 1 {
		t.Fatalf("parked %d times, want once", n)
	}
	for _, id := range later {
		if !w.unsent(id) {
			t.Errorf("%s, queued after the held park, was sent before the answer", id)
		}
	}
	// By the held note's turn and a sweep: parked inside the grace of the
	// wait that turn ended in, never after a fresh one, and before anything
	// queued after it was heard.
	waited := w.str(`SELECT waiting_since::text FROM runs WHERE id = $1`, old)
	if w.count(`SELECT count(*) FROM runs WHERE id = $1 AND waiting_since + make_interval(secs => $2) > $3`, old, grace.Seconds(), w.parkedAt(old)) != 1 {
		t.Errorf("parked at %s, a grace or more after its wait began at %s", w.parkedAt(old), waited)
	}
	if n := w.count(`SELECT count(*) FROM events p, events d WHERE p.run_id = $1 AND p.event_type = 'run.parked'
		AND d.run_id = $1 AND d.event_type = 'run.directive.delivered' AND d.payload->>'directiveId' = $2 AND d.cursor < p.cursor`,
		old, held); n != 1 {
		t.Errorf("%d parks after the held note was delivered, want 1", n)
	}
	if resumed, _ := w.luxResumes(old); resumed != 0 {
		t.Errorf("resumed %d times for a note, the question unanswered", resumed)
	}
	w.questionOpen(q, "after the park")
	// The answer brings what was held for it.
	if status, out := w.call("/internal/questions/"+q+"/answer", map[string]any{"text": "Draft"}); status != 200 {
		t.Fatalf("answer: %d %v", status, out)
	}
	w.until("the held notes heard after the answer", func() bool {
		for _, id := range later {
			if !w.directiveDelivered(id) {
				return false
			}
		}
		return true
	})
}

// Unchanged from main: a Run whose notes each arrive inside the grace,
// half a grace into the wait the last one's turn ended in, from the start
// of its wait, never has its park held, and keeps running and hearing
// them, however old its ask grows. A rule that took a note delivered more
// than a grace after the ask for a held park would park it.
func TestNotesEachInsideTheGraceKeepAPersonsRunRunningAsOnMain(t *testing.T) {
	w := conducting(t)
	task, old, q := w.waitingAtGate(`{"action":"ask_person"}`)
	w.onFailureShowsDirectives(old)
	const grace = time.Minute
	w.syncer.ParkAfter = grace
	var notes []string
	for k := range 5 {
		w.waitingOn(old, q, grace/2, time.Duration(k+1)*grace/2)
		note := w.note(task, old, fmt.Sprintf("news %d", k+1))
		notes = append(notes, note)
		w.until("the note heard and its turn over, or the Run parked", func() bool {
			return w.parks(old) > 0 || w.directiveDelivered(note) && w.waitsAfter(old, note)
		})
		if n := w.parks(old); n != 0 {
			t.Fatalf("parked %d times at note %d, each inside the grace", n, k+1)
		}
	}
	for range 5 {
		w.pump()
	}
	if n := w.parks(old); n != 0 {
		t.Errorf("parked %d times, every note inside the grace", n)
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'running' AND control = 'none'`, old); n != 1 {
		t.Errorf("not running, every note inside the grace")
	}
	for _, id := range notes {
		if !w.directiveDelivered(id) {
			t.Errorf("%s unheard", id)
		}
	}
	w.questionOpen(q, "with every note heard")
}

// Each wait's park may be held once: a held park, then a person's resume
// with the question still open, begins a new wait. What was held for the
// answer is heard on the resume, as on main. A note as that wait is past
// its grace holds its park again, is heard, and the Run parks as soon as
// its turn is over, a second time.
func TestAPersonParkIsHeldOncePerWaitAResumeBeginsANewOne(t *testing.T) {
	w := conducting(t)
	task, old, q := w.waitingAtGate(`{"action":"ask_person"}`)
	w.onFailureShowsDirectives(old)
	const grace = time.Minute
	w.syncer.ParkAfter = grace
	w.heldPast(task, old, q, grace)
	w.untilParked(old)
	after := w.note(task, old, "news after the park")

	if status, out := w.call("/internal/runs/"+old+"/resume", map[string]any{}); status != 200 {
		t.Fatalf("resume: %d %v", status, out)
	}
	w.until("resumed, what was held heard, and waiting on the person again", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'running' AND lux_state = 'running'`, old) == 1 &&
			w.directiveDelivered(after) && w.waitsAfter(old, after)
	})
	if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND news_hold_at IS NULL`, old); n != 1 {
		t.Errorf("the first wait's hold outlived its park")
	}

	// The new wait, past its grace; the ask's age is the resume's business.
	mustExec(t, w.owner, `UPDATE runs SET waiting_since = waiting_since - make_interval(secs => $2) WHERE id = $1`, old, (grace + time.Second).Seconds())
	mustExec(t, w.owner, `UPDATE directives SET sent_at = sent_at - make_interval(secs => $2),
		delivered_at = delivered_at - make_interval(secs => $2) WHERE run_id = $1`, old, (grace + time.Second).Seconds())
	second := w.note(task, old, "news in the new wait")
	w.until("the new wait's note heard, or the Run parked", func() bool { return w.parks(old) > 1 || w.directiveDelivered(second) })
	if !w.directiveDelivered(second) {
		t.Fatalf("parked over the new wait's note, unheard: its hold was spent in the first wait")
	}
	w.untilParked(old)
	for range 5 {
		w.pump()
	}
	if n := w.parks(old); n != 2 {
		t.Errorf("parked %d times, want twice", n)
	}
	if n := w.unheard(old); n != 0 {
		t.Errorf("%d directives unheard at the second park", n)
	}
	w.questionOpen(q, "after the second park")
}

// The agent asked long ago (past the hold's ceiling) and worked on; its
// turn has just ended. A steer now is inside the grace of that wait: it is
// delivered and the Run is not parked over it, as on main. The park comes
// later, past the grace of the wait the steer's turn ends in.
func TestASteerInsideTheGraceOfAnOldAskIsHeardNotParkedOver(t *testing.T) {
	w := conducting(t)
	task, old, q := w.waitingAtGate(`{"action":"ask_person"}`)
	w.onFailureShowsDirectives(old)
	const grace = 2 * time.Second
	w.syncer.ParkAfter = grace
	// Asked three graces ago; the turn ended now.
	mustExec(t, w.owner, `UPDATE questions SET asked_at = now() - make_interval(secs => $2) WHERE id = $1`, q, (3 * grace).Seconds())
	mustExec(t, w.owner, `UPDATE runs SET waiting_since = now() WHERE id = $1`, old)
	steer := w.note(task, old, "use the other base")
	w.until("the steer heard, or the Run parked", func() bool {
		return w.parks(old) > 0 || w.directiveDelivered(steer)
	})
	if n := w.parks(old); n != 0 || !w.directiveDelivered(steer) {
		t.Fatalf("parked %d times inside the grace, the steer unheard", n)
	}
	w.untilParked(old)
	w.questionOpen(q, "once parked")
	if n := w.parks(old); n != 1 {
		t.Errorf("parked %d times, want once", n)
	}
	if n := w.unheard(old); n != 0 {
		t.Errorf("%d directives unheard at the park", n)
	}
}

// note queues a directive for run (a steer, a wake note), not held for
// any answer.
func (w *world) note(task, run, text string) string {
	w.t.Helper()
	ctx := context.Background()
	var id string
	if err := w.app.InOrg(ctx, w.org, func(tx pgx.Tx) (err error) {
		id, _, err = delivery.QueueDirective(ctx, tx, delivery.RunRef{Org: w.org, ProjectID: w.project, TaskID: task, RunID: run},
			delivery.Directive{Text: text, Scope: "run"})
		return err
	}); err != nil {
		w.t.Fatal(err)
	}
	return id
}

// heldNote queues a directive for run held for question q's answer
// (directives.held_for, delivery.HeldBack).
func (w *world) heldNote(task, run, q, text string) string {
	w.t.Helper()
	id := w.note(task, run, text)
	mustExec(w.t, w.owner, `UPDATE directives SET held_for = $2 WHERE id = $1`, id, q)
	return id
}

// A directive held for the person's answer is not news the agent must hear
// first: the Run is parked with it queued, and it stays unsent.
func TestAHeldBackDirectiveDoesNotHoldOffThePersonPark(t *testing.T) {
	w := conducting(t)
	task, old, q := w.waitingAtGate(`{"action":"ask_person"}`)
	w.onFailureShowsDirectives(old)
	held := w.heldNote(task, old, q, "after your answer: carry on")
	w.syncer.ParkAfter = time.Millisecond
	w.untilParked(old)
	w.questionOpen(q, "once parked")
	if n := w.count(`SELECT count(*) FROM directives WHERE id = $1 AND sent_at IS NULL AND delivered_at IS NULL AND failed_at IS NULL`, held); n != 1 {
		t.Errorf("the held directive was sent or settled before the answer")
	}
	if n := w.parks(old); n != 1 {
		t.Errorf("parked %d times, want once", n)
	}
}

// A note that arrives after the park is not in scope: it waits, unsent,
// with the Run parked, until the person answers; then it is heard.
func TestANoteAfterThePersonParkWaitsForTheAnswer(t *testing.T) {
	w := conducting(t)
	task, old, q := w.parkedAskingAtGate(`{"action":"ask_person"}`)
	w.reason(task, "test:late", "the base moved on", time.Minute)
	var note string
	w.until("the late note queued", func() bool { note = w.noteFor(task, "test:late"); return note != "" })
	for range 5 {
		w.pump()
	}
	if resumed, _ := w.luxResumes(old); resumed != 0 {
		t.Fatalf("resumed %d times for a note, the question unanswered", resumed)
	}
	if n := w.count(`SELECT count(*) FROM directives WHERE id = $1 AND sent_at IS NULL AND delivered_at IS NULL`, note); n != 1 {
		t.Fatalf("the late note was sent to a Run parked for the person")
	}
	w.questionOpen(q, "with the late note queued")
	if status, out := w.call("/internal/questions/"+q+"/answer", map[string]any{"text": "Draft"}); status != 200 {
		t.Fatalf("answer: %d %v", status, out)
	}
	w.until("the late note heard after the answer", func() bool {
		return w.directiveDelivered(note)
	})
	if resumed, _ := w.luxResumes(old); resumed != 1 {
		t.Errorf("resumed %d times, want the answer's once", resumed)
	}
}
