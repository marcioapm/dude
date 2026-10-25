package orchestrator_test

import (
	"testing"
)

// A member removed and invited back before the owner sweep reads her
// person.removed answers what was put to her: the question stays open, and
// the change is marked done all the same.
func TestOrgSessionTheOwnerSweepLeavesAQuestionToSomeoneBackByThen(t *testing.T) {
	s := newSessionWorld(t)
	s.withTools()
	id := s.session()
	s.join(id, s.ana, "chat")
	s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/chat", map[string]any{"text": "let's plan"})
	run := s.started(id)
	asked := s.askedBy(run, "Ana")
	mustExec(t, s.owner, `UPDATE people SET removed_at = now() WHERE id = $1`, s.ana)
	s.recorded("", "person.removed", map[string]any{"personId": s.ana})
	removed := s.count(`SELECT max(cursor) FROM events WHERE event_type = 'person.removed'`)
	mustExec(t, s.owner, `UPDATE people SET removed_at = NULL WHERE id = $1`, s.ana)
	s.sweepOwners()
	if got := s.questionStatus(asked); got != "open" {
		t.Errorf("the question to someone back before the sweep is %q, want open", got)
	}
	if n := s.count(`SELECT count(*) FROM events WHERE session_id = $1 AND event_type = 'question.closed'`, id); n != 0 {
		t.Errorf("question.closed: %d, want none", n)
	}
	if n := s.count(`SELECT count(*) FROM session_owner_done WHERE cursor = $1`, removed); n != 1 {
		t.Errorf("the person.removed of someone back is not marked done (%d)", n)
	}
}

// A person.removed whose questions are withdrawn is marked done.
func TestOrgSessionTheOwnerSweepMarksAWithdrawalDone(t *testing.T) {
	s := newSessionWorld(t)
	s.withTools()
	id := s.session()
	s.join(id, s.ana, "chat")
	s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/chat", map[string]any{"text": "let's plan"})
	asked := s.askedBy(s.started(id), "Ana")
	mustExec(t, s.owner, `UPDATE people SET removed_at = now() WHERE id = $1`, s.ana)
	s.recorded("", "person.removed", map[string]any{"personId": s.ana})
	removed := s.count(`SELECT max(cursor) FROM events WHERE event_type = 'person.removed'`)
	s.sweepOwners()
	if got := s.questionStatus(asked); got != "cancelled" {
		t.Errorf("the question to a removed member is %q", got)
	}
	if n := s.count(`SELECT count(*) FROM session_owner_done WHERE cursor = $1`, removed); n != 1 {
		t.Errorf("the person.removed is not marked done (%d)", n)
	}
}

// "Changed since" for each kind of later owner change: an owner_left
// followed by the owner's return is not told (the return is); an
// owner_returned followed by the owner leaving again, or by a handover, is
// not told (the leaving is; the handover tells its own).
func TestOrgSessionTheOwnerSweepSkipsAChangeSupersededByAnyLaterOne(t *testing.T) {
	s := newSessionWorld(t)
	left := "Márcio Martins left the organisation"
	back := "Márcio Martins is back in the organisation"
	live := func() string {
		id := s.session()
		s.join(id, s.ana, "chat")
		s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/chat", map[string]any{"text": "let's plan"})
		s.started(id)
		return id
	}
	told := func(id, prefix string) int {
		return s.count(`SELECT count(*) FROM events WHERE session_id = $1 AND event_type = 'session.told'
			AND starts_with(payload->>'text', $2)`, id, prefix)
	}
	returns := func(id string) {
		mustExec(t, s.owner, `UPDATE people SET removed_at = NULL WHERE id = $1`, s.marcio)
		mustExec(t, s.owner, `UPDATE sessions SET owned_by_admins = false WHERE id = $1`, id)
		s.recorded(id, "session.owner_returned", map[string]any{"owner": s.marcio})
	}
	leaves := func(id string) {
		s.ownerLeaves(id)
		s.recorded(id, "session.owner_left", map[string]any{"owner": s.marcio, "ownerName": "Márcio Martins"})
	}

	// Left, then back before the sweep: only the return is told.
	given := live()
	leaves(given)
	returns(given)
	s.sweepOwners()
	if l, b := told(given, left), told(given, back); l != 0 || b != 1 {
		t.Errorf("left then given back: told left %d, back %d; want 0, 1", l, b)
	}

	// Back, then gone again before the sweep: only the leaving is told.
	again := live()
	leaves(again)
	s.sweepOwners()
	returns(again)
	leaves(again)
	s.sweepOwners()
	if l, b := told(again, left), told(again, back); l != 2 || b != 0 {
		t.Errorf("back then gone again: told left %d, back %d; want 2, 0", l, b)
	}
	mustExec(t, s.owner, `UPDATE people SET removed_at = NULL WHERE id = $1`, s.marcio)

	// Back, then handed over to João before the sweep: "is back" is not told.
	handed := live()
	leaves(handed)
	s.sweepOwners()
	returns(handed)
	s.ok(s.marcio, "POST", "/internal/sessions/"+handed+"/owner", map[string]any{"person": s.joao})
	s.ok(s.joao, "POST", "/internal/sessions/"+handed+"/accept", nil)
	if n := s.count(`SELECT count(*) FROM events WHERE session_id = $1 AND event_type = 'session.owner_changed'`, handed); n != 1 {
		t.Fatalf("the handover recorded %d owner changes, want 1", n)
	}
	s.sweepOwners()
	if b := told(handed, back); b != 0 {
		t.Errorf("back then handed over: told back %d, want 0", b)
	}
}
