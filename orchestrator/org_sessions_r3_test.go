package orchestrator_test

import (
	"sync"
	"testing"
	"time"

	"github.com/marciomartins/dude/orchestrator/internal/notify"
)

// waitsOnARow polls until some backend of this database waits on a row
// (a transactionid or tuple lock), failing if done reports first.
func (s *sessionWorld) waitsOnARow(what string, done <-chan int) {
	s.t.Helper()
	for deadline := time.Now().Add(10 * time.Second); ; {
		if s.count(`SELECT count(*) FROM pg_locks l JOIN pg_stat_activity a ON a.pid = l.pid
			WHERE a.datname = current_database() AND NOT l.granted AND l.locktype IN ('transactionid', 'tuple')`) > 0 {
			return
		}
		select {
		case status := <-done:
			s.t.Fatalf("%s finished (%d) without waiting on a row", what, status)
		default:
		}
		if time.Now().After(deadline) {
			s.t.Fatalf("%s never waited on a row", what)
		}
	}
}

// The departed owner accepts an invitation back while an admin takes the
// session over: take-ownership waits on the owner's row the accept is
// updating, then reads them as back and refuses. Their row stays, the
// session keeps one owner, and it is theirs again.
func TestOrgSessionTakeOwnershipWaitsForTheDepartedOwnersReturn(t *testing.T) {
	s := newSessionWorld(t)
	id := s.session()
	s.join(id, s.ana, "chat")
	s.ownerLeaves(id)

	// The invitation accept's statements (the backend's returnSessions), held open.
	accept, err := s.owner.Begin(t0())
	if err != nil {
		t.Fatal(err)
	}
	defer accept.Rollback(t0())
	if _, err := accept.Exec(t0(), `UPDATE people SET removed_at = NULL WHERE id = $1`, s.marcio); err != nil {
		t.Fatal(err)
	}
	if _, err := accept.Exec(t0(), `UPDATE sessions SET owned_by_admins = false WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}
	result := make(chan int, 1)
	go func() {
		status, _ := s.as(s.admin, "POST", "/internal/sessions/"+id+"/take-ownership", nil)
		result <- status
	}()
	s.waitsOnARow("take-ownership", result)
	if err := accept.Commit(t0()); err != nil {
		t.Fatal(err)
	}
	select {
	case status := <-result:
		if status != 409 {
			t.Errorf("take-ownership while the owner came back: %d, want 409", status)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("take-ownership never finished")
	}
	if n := s.count(`SELECT count(*) FROM session_people WHERE session_id = $1 AND person_id = $2 AND role = 'owner'`, id, s.marcio); n != 1 {
		t.Errorf("the returned owner's row: %d, want 1", n)
	}
	if n := s.count(`SELECT count(*) FROM session_people WHERE session_id = $1 AND role = 'owner'`, id); n != 1 {
		t.Errorf("owners: %d, want 1", n)
	}
	if n := s.count(`SELECT count(*) FROM sessions WHERE id = $1 AND owned_by_admins`, id); n != 0 {
		t.Errorf("the session is still its admins'")
	}
}

// Two orchestrators sweep the same owner-left at once: both read it, both
// wait on the session's lock; the agent is told once.
func TestOrgSessionTwoOwnerSweepsTellTheAgentOnce(t *testing.T) {
	s := newSessionWorld(t)
	id := s.session()
	s.join(id, s.ana, "chat")
	s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/chat", map[string]any{"text": "let's plan"})
	s.started(id)
	s.ownerLeaves(id)
	s.recorded(id, "session.owner_left", map[string]any{"owner": s.marcio, "ownerName": "Márcio Martins"})

	holder, err := s.owner.Begin(t0())
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Rollback(t0())
	if _, err := holder.Exec(t0(), `SELECT pg_advisory_xact_lock(hashtext('session:' || $1))`, id); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			n := &notify.Notifier{DB: s.app, Log: quiet, Subject: "ops@example.com"}
			_, _ = n.Sweep(t0())
		}()
	}
	for deadline := time.Now().Add(10 * time.Second); s.count(`SELECT count(*) FROM pg_locks l JOIN pg_stat_activity a ON a.pid = l.pid
		WHERE a.datname = current_database() AND NOT l.granted AND l.locktype = 'advisory'`) < 2; {
		if time.Now().After(deadline) {
			t.Fatal("the two sweeps never both waited on the session's lock")
		}
	}
	if err := holder.Commit(t0()); err != nil {
		t.Fatal(err)
	}
	wg.Wait()
	if n := s.count(`SELECT count(*) FROM events WHERE session_id = $1 AND event_type = 'session.told'
		AND starts_with(payload->>'text', 'Márcio Martins left the organisation')`, id); n != 1 {
		t.Errorf("owner-left told %d times by two sweeps, want once", n)
	}
}
