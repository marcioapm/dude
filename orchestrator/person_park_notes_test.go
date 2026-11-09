package orchestrator_test

import (
	"context"
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

// The issue's case: a conductor waiting on the person, past its grace, and
// a wake note due in the same sweep. The reason is recorded only once the
// conductor waits with everything heard, so the note and the park are due
// together.
func TestAPersonParkWaitsUntilDudesNoteIsHeard(t *testing.T) {
	w := conducting(t)
	task, old, q := w.waitingAtGate(`{"action":"ask_person"}`)
	said := len(w.said(old))
	// Past any grace, and the reason past the wake window: both due now.
	const grace = time.Second
	w.syncer.ParkAfter = grace
	mustExec(t, w.owner, `UPDATE runs SET waiting_since = now() - interval '1 hour' WHERE id = $1`, old)
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
	w.until("the note's turn over, waiting on the person again", func() bool {
		w.questionOpen(q, "during the note's turn")
		return len(w.said(old)) > said && w.count(`SELECT count(*) FROM runs WHERE id = $1 AND waiting_since IS NOT NULL
			AND waiting_since > (SELECT delivered_at FROM directives WHERE id = $2)`, old, note) == 1
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
