package orchestrator_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
)

// A Run is not parked for a person while dude has a note for it the agent
// has not heard (requestPause, due's ParkNow): the note goes first, its
// turn runs, and the Run is parked once it waits again, after the grace.
// A note held for an answer (HeldBack) does not hold the park off, and one
// that arrives after the park waits for the answer, as before.

// noteFor is the directive carrying the wake note that told reason key,
// "" for none yet.
func (w *world) noteFor(task, key string) string {
	w.t.Helper()
	return w.str(`SELECT COALESCE((SELECT a.directive_id FROM conductor_wake_attempts a JOIN conductor_wakes k ON k.id = a.wake_id
		WHERE k.task_id = $1 AND k.key = $2 AND a.directive_id IS NOT NULL ORDER BY a.created_at DESC LIMIT 1), '')`, task, key)
}

// parks is how many times run was parked for a person.
func (w *world) parks(run string) int {
	w.t.Helper()
	return w.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = 'run.parked' AND payload->>'reason' = 'person'`, run)
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
	w.waitingOn(old, q, time.Hour)
	w.reason(task, "test:news", "CI failed on the base", time.Minute)

	var note string
	w.until("the note delivered, or the Run parked", func() bool {
		w.questionOpen(q, "before the note was heard")
		note = w.noteFor(task, "test:news")
		return w.parks(old) > 0 || w.count(`SELECT count(*) FROM runs WHERE id = $1 AND control = 'pause_graceful'`, old) > 0 ||
			(note != "" && w.count(`SELECT count(*) FROM directives WHERE id = $1 AND delivered_at IS NOT NULL`, note) == 1)
	})
	if note == "" || w.count(`SELECT count(*) FROM directives WHERE id = $1 AND delivered_at IS NOT NULL`, note) != 1 {
		t.Fatalf("parked for the person with dude's note unheard (directive %q):\n%s", note, w.describeRuns())
	}
	// The turn that read it ended at or after the read: the fake agent
	// reads it and ends the turn in one batch, at the same now().
	w.until("the note's turn over, waiting on the person again", func() bool {
		w.questionOpen(q, "during the note's turn")
		return len(w.said(old)) > said && w.count(`SELECT count(*) FROM runs WHERE id = $1 AND waiting_since IS NOT NULL
			AND waiting_since >= (SELECT delivered_at FROM directives WHERE id = $2)`, old, note) == 1 ||
			w.parks(old) > 0
	})
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
	if n := w.count(`SELECT count(*) FROM runs r, events p WHERE r.id = $1 AND p.run_id = r.id AND p.event_type = 'run.parked'
		AND (p.payload->>'parkedAt')::timestamptz >= r.waiting_since + make_interval(secs => $2)`, old, grace.Seconds()); n != 1 {
		t.Errorf("parked inside the grace of its wait after the note: %s", w.str(`SELECT r.waiting_since::text || ' / ' ||
			(SELECT payload->>'parkedAt' FROM events WHERE run_id = r.id AND event_type = 'run.parked') FROM runs r WHERE r.id = $1`, old))
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

// waitingOn sets run waiting on the person for as long as waited, its
// question q asked now: past its grace, inside the ceiling of unheardNews.
// What it was sent before is moved back as far, so it was still read
// before the wait began.
func (w *world) waitingOn(run, q string, waited time.Duration) {
	w.t.Helper()
	mustExec(w.t, w.owner, `UPDATE runs SET waiting_since = waiting_since - make_interval(secs => $2) WHERE id = $1`, run, waited.Seconds())
	mustExec(w.t, w.owner, `UPDATE directives SET sent_at = sent_at - make_interval(secs => $2),
		delivered_at = delivered_at - make_interval(secs => $2) WHERE run_id = $1`, run, waited.Seconds())
	mustExec(w.t, w.owner, `UPDATE questions SET asked_at = now() WHERE id = $1`, q)
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
	w.waitingOn(old, q, time.Hour)
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

// A Run that keeps getting notes, each before its grace ends, would never
// park: each note's turn resets waiting_since. The hold has a ceiling, two
// graces from when the wait began (the question asked): the Run is parked
// once, by then, and notes after the park wait for the answer.
func TestAPersonParkComesByTheCeilingWhileNotesKeepComing(t *testing.T) {
	w := conducting(t)
	task, old, q := w.waitingAtGate(`{"action":"ask_person"}`)
	w.onFailureShowsDirectives(old)
	const grace = 2 * time.Second
	w.syncer.ParkAfter = grace
	w.waitingOn(old, q, 0)
	notes, last := 0, time.Time{}
	w.until("the Run parked, notes arriving every half grace", func() bool {
		w.questionOpen(q, "while notes came")
		if time.Since(last) >= grace/2 {
			notes++
			w.reason(task, fmt.Sprintf("test:news%d", notes), fmt.Sprintf("news %d", notes), time.Minute)
			last = time.Now()
		}
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'paused' AND dude_pause = 'person'`, old) == 1
	})
	w.untilParked(old)
	// By the ceiling: two graces from the ask, and a sweep or two after.
	if n := w.count(`SELECT count(*) FROM events p, questions q WHERE p.run_id = $1 AND p.event_type = 'run.parked' AND q.id = $2
		AND (p.payload->>'parkedAt')::timestamptz <= q.asked_at + make_interval(secs => $3)`, old, q, (2*grace + 3*time.Second).Seconds()); n != 1 {
		t.Errorf("parked past the ceiling: %s", w.str(`SELECT (SELECT payload->>'parkedAt' FROM events WHERE run_id = $1 AND event_type = 'run.parked')
			|| ' asked ' || (SELECT asked_at::text FROM questions WHERE id = $2)`, old, q))
	}
	if n := w.count(`SELECT count(*) FROM directives WHERE run_id = $1 AND delivered_at IS NOT NULL
		AND created_at >= (SELECT asked_at FROM questions WHERE id = $2)`, old, q); n < 2 {
		t.Errorf("%d notes heard before the park, want the hold to have delivered some", n)
	}
	w.reason(task, "test:late", "the base moved on", time.Minute)
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

// heldNote queues a directive for run held for question q's answer
// (directives.held_for, delivery.HeldBack).
func (w *world) heldNote(task, run, q, text string) string {
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
		return w.count(`SELECT count(*) FROM directives WHERE id = $1 AND delivered_at IS NOT NULL`, note) == 1
	})
	if resumed, _ := w.luxResumes(old); resumed != 1 {
		t.Errorf("resumed %d times, want the answer's once", resumed)
	}
}
