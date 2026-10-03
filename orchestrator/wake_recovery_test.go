package orchestrator_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// recordReason records a reason to wake the task's conductor, aged past
// the window.
func (w *world) recordReason(task, key, line string) {
	w.t.Helper()
	w.reason(task, key, line, time.Minute)
}

// noLiveConductor ends the task's conductor as Chat's next message would
// after it stopped: none is live, so a wake starts one with its note.
func (w *world) noLiveConductor(task string) {
	w.t.Helper()
	id, _, _ := w.conductor(task)
	mustExec(w.t, w.owner, `UPDATE runs SET status = 'completed', ended_at = now(), lux_stop_reason = 'complete' WHERE id = $1`, id)
}

// A new conductor started with a wake note fails before its agent starts
// (its tier names no model: it is never submitted). Its reasons are
// pending again, and the next conductor is told them. One that heard its
// briefing is not told again.
func TestAWakeBriefingNeverHeardIsToldAgain(t *testing.T) {
	w := conducting(t)
	task := w.task()
	first := w.talk(task)
	w.noLiveConductor(task)
	// The conductor's tier names no model: the next one fails to submit.
	mustExec(t, w.owner, `UPDATE model_tiers SET model = NULL WHERE organization_id = $1`, w.org)
	w.recordReason(task, "test:brief", "the decision after the briefing")
	var failed string
	w.until("the briefed conductor to fail", func() bool {
		_ = w.owner.QueryRow(context.Background(), `SELECT id FROM runs WHERE task_id = $1 AND role = 'conductor' AND id <> $2
			AND status = 'failed'`, task, first).Scan(&failed)
		return failed != ""
	})
	if w.luxRunOf(failed) != "" {
		t.Fatalf("the conductor reached lux: not the failure before its agent starts")
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND strpos(prompt, 'the decision after the briefing') > 0`, failed); n != 1 {
		t.Fatalf("the failed conductor was not briefed with the reason")
	}

	// The model is back: a sweep starts a conductor with the note again,
	// which hears it.
	mustExec(t, w.owner, `UPDATE model_tiers SET model = 'fake/scripted' WHERE organization_id = $1`, w.org)
	w.until("a conductor that hears it", func() bool {
		mustExec(t, w.owner, `UPDATE conductor_wakes SET created_at = now() - interval '1 minute' WHERE task_id = $1`, task)
		id, status, _ := w.conductor(task)
		return id != failed && id != first && status != "failed" && w.luxRunOf(id) != ""
	})
	w.wokenWith(task, "the decision after the briefing")
	heard, _, _ := w.conductor(task)
	if n := w.count(`SELECT count(*) FROM conductor_wake_attempts WHERE conductor_run_id = $1 AND heard_at IS NOT NULL`, heard); n != 1 {
		t.Errorf("the briefing heard is not recorded")
	}

	// Heard, then ended: nothing is told again.
	notes := len(w.woken(task))
	mustExec(t, w.owner, `UPDATE runs SET status = 'completed', ended_at = now(), lux_stop_reason = 'complete' WHERE id = $1`, heard)
	for range 3 {
		w.pump()
	}
	if n := w.count(`SELECT count(*) FROM conductor_wakes WHERE task_id = $1 AND delivered_at IS NULL`, task); n != 0 {
		t.Errorf("%d reasons pending after their briefing was heard", n)
	}
	if n := len(w.woken(task)); n != notes {
		t.Errorf("told again after the briefing was heard: %d notes, want %d", n, notes)
	}
}

// A wake note queued for conductor A, sent and unread when A's container
// stops. Chat's next message replaces A with B: the note is not inherited
// as a person's message; its reasons go back to pending and B is told them
// once, as dude's note.
func TestAnUnreadWakeNoteIsHandedOverAsDudesNote(t *testing.T) {
	w := conducting(t)
	task := w.task()
	a := w.talk(task)
	w.lux.InputGate = make(chan struct{})
	w.recordReason(task, "test:handed", "the decision A never read")
	var note string
	w.until("the note sent to A", func() bool {
		_ = w.owner.QueryRow(context.Background(), `SELECT COALESCE(payload->>'directiveId', '') FROM events WHERE task_id = $1
			AND event_type = 'conductor.woken' AND run_id = $2`, task, a).Scan(&note)
		return note != "" && w.count(`SELECT count(*) FROM directives WHERE id = $1 AND sent_at IS NOT NULL`, note) == 1
	})
	w.stopped(a)
	close(w.lux.InputGate)
	w.lux.InputGate = nil
	// Chat ends A, hands over what it never read, and starts B for the
	// message (or queues it for the B hand-over started).
	if status, out := w.chat(task, "are you still there?"); status != 201 && status != 200 {
		t.Fatalf("chat: %d %v", status, out)
	}
	b, _, _ := w.conductor(task)
	if b == a {
		t.Fatal("A was not replaced")
	}
	w.until("a note to B", func() bool {
		mustExec(t, w.owner, `UPDATE conductor_wakes SET created_at = now() - interval '1 minute' WHERE task_id = $1`, task)
		return w.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = 'conductor.woken'`, b) > 0
	})
	got := w.wokenWith(task, "the decision A never read")
	if !strings.HasPrefix(got, "dude woke you") {
		t.Errorf("B's note: %q", got)
	}
	for range 3 {
		w.pump()
	}
	var toB int
	if err := w.app.InOrg(context.Background(), w.org, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(), `SELECT count(*) FROM events WHERE run_id = $1 AND event_type = 'conductor.woken'
			AND payload->>'text' LIKE '%the decision A never read%'`, b).Scan(&toB)
	}); err != nil {
		t.Fatal(err)
	}
	if toB != 1 {
		t.Errorf("B was told the reason %d times, want once", toB)
	}
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'chat.message'
		AND payload->>'text' LIKE '%the decision A never read%'`, task); n != 0 {
		t.Errorf("the wake note was handed on as a person's message")
	}
	if n := w.count(`SELECT count(*) FROM directives WHERE run_id = $1 AND text LIKE '%the decision A never read%'
		AND supersedes = $2`, b, note); n != 0 {
		t.Errorf("B inherited A's wake note as a queued message")
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND strpos(prompt, 'the decision A never read') > 0`, b); n != 0 {
		t.Errorf("B's briefing carries the note as the person's message")
	}
}
