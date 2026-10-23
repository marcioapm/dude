package orchestrator_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/marciomartins/dude/orchestrator/internal/notify"
)

// system calls the orchestrator's internal API as the backend does for
// itself: no person, no key.
func (s *sessionWorld) system(method, path string, body any) (int, map[string]any) {
	s.t.Helper()
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest(method, s.api+path, bytes.NewReader(b))
	req.Header.Set("Authorization", "Bearer svc")
	req.Header.Set("X-Dude-Organization", s.org)
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		s.t.Fatal(err)
	}
	defer res.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(res.Body).Decode(&out)
	return res.StatusCode, out
}

// askedBy has the session's agent put a question to `to` by name, and
// returns its id.
func (s *sessionWorld) askedBy(run, to string) string {
	s.t.Helper()
	if status, out := s.tool(run, "ask_person", `{"question":"Which plan?","to":"`+to+`"}`); status != 200 {
		s.t.Fatalf("ask_person to %s: %d %v", to, status, out)
	}
	var q string
	_ = s.owner.QueryRow(t0(), `SELECT id FROM questions WHERE run_id = $1 AND status = 'open' ORDER BY asked_at DESC LIMIT 1`, run).Scan(&q)
	return q
}

func (s *sessionWorld) questionStatus(q string) string {
	var status string
	_ = s.owner.QueryRow(t0(), `SELECT status::text FROM questions WHERE id = $1`, q).Scan(&status)
	return status
}

// heldFor is what the latest directive of the run waits on, "" for nothing.
func (s *sessionWorld) heldFor(run string) string {
	var held *string
	_ = s.owner.QueryRow(t0(), `SELECT held_for FROM directives WHERE run_id = $1 ORDER BY created_at DESC LIMIT 1`, run).Scan(&held)
	if held == nil {
		return ""
	}
	return *held
}

// A question the agent put by name to the owner before they left would
// hold every later message for ever (nobody else may answer it). Taking the
// session over withdraws it; so does the owner sweep reading the removal
// from the ledger, before anyone takes it over.
func TestOrgSessionAQuestionToTheDepartedOwnerIsWithdrawn(t *testing.T) {
	s := newSessionWorld(t)
	s.withTools()
	for _, how := range []string{"take-ownership", "left"} {
		id := s.session()
		s.join(id, s.ana, "chat")
		s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/chat", map[string]any{"text": "let's plan"})
		run := s.started(id)
		q := s.askedBy(run, "Márcio")
		s.ownerLeaves(id)
		if how == "take-ownership" {
			s.ok(s.admin, "POST", "/internal/sessions/"+id+"/take-ownership", nil)
		} else {
			s.recorded(id, "session.owner_left", map[string]any{"owner": s.marcio, "ownerName": "Márcio Martins"})
			s.recorded("", "person.removed", map[string]any{"personId": s.marcio})
			s.sweepOwners()
		}
		if got := s.questionStatus(q); got != "cancelled" {
			t.Errorf("%s: the departed owner's question is %q", how, got)
		}
		if n := s.count(`SELECT count(*) FROM events WHERE session_id = $1 AND event_type = 'question.closed'
			AND payload->>'questionId' = $2 AND payload->>'by' = 'withdrawn'`, id, q); n != 1 {
			t.Errorf("%s: question.closed withdrawn: %d", how, n)
		}
		s.ok(s.ana, "POST", "/internal/sessions/"+id+"/chat", map[string]any{"text": "the metering one"})
		if held := s.heldFor(run); held != "" {
			t.Errorf("%s: Ana's message is held for %s", how, held)
		}
		mustExec(t, s.owner, `UPDATE people SET removed_at = NULL WHERE id = $1`, s.marcio)
	}
}

// The owner sweep, from the ledger: a member's removal withdraws what was
// put to them by name in a session they never owned (no session of theirs
// is named anywhere); an owner's leaving tells the agent once, across a
// restart; their return tells it they are back; and a change recorded
// after one (an admin took it over) means that one is not told.
func TestOrgSessionTheOwnerSweepTellsTheAgentAndWithdrawsEverywhere(t *testing.T) {
	s := newSessionWorld(t)
	s.withTools()
	id := s.session()
	s.join(id, s.ana, "chat")
	s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/chat", map[string]any{"text": "let's plan"})
	run := s.started(id)
	asked := s.askedBy(run, "Ana")
	told := func(prefix string) int {
		return s.count(`SELECT count(*) FROM events WHERE session_id = $1 AND event_type = 'session.told'
			AND starts_with(payload->>'text', $2)`, id, prefix)
	}
	// Ana owns nothing: only person.removed names her.
	mustExec(t, s.owner, `UPDATE people SET removed_at = now() WHERE id = $1`, s.ana)
	s.recorded("", "person.removed", map[string]any{"personId": s.ana})
	s.sweepOwners()
	if got := s.questionStatus(asked); got != "cancelled" {
		t.Errorf("the question to a removed member, in a session she never owned, is %q", got)
	}
	if n := s.count(`SELECT count(*) FROM events WHERE session_id = $1 AND event_type = 'question.closed' AND payload->>'by' = 'withdrawn'`, id); n != 1 {
		t.Errorf("question.closed withdrawn: %d, want 1", n)
	}
	if n := told("Ana Nunes left"); n != 0 {
		t.Errorf("a member's removal told the agent she owned it: %d", n)
	}

	left := "Márcio Martins left the organisation. Its admins own this session now"
	s.ownerLeaves(id)
	s.recorded(id, "session.owner_left", map[string]any{"owner": s.marcio, "ownerName": "Márcio Martins"})
	s.sweepOwners()
	s.sweepOwners()
	var text string
	_ = s.owner.QueryRow(t0(), `SELECT text FROM directives WHERE run_id = $1 ORDER BY created_at DESC LIMIT 1`, run).Scan(&text)
	if !strings.HasPrefix(text, left) {
		t.Errorf("the agent was told %q", text)
	}
	if n := told(left); n != 1 {
		t.Errorf("owner-left told %d times across two sweeps, want once", n)
	}

	// Back, and told so.
	mustExec(t, s.owner, `UPDATE people SET removed_at = NULL WHERE id = $1`, s.marcio)
	mustExec(t, s.owner, `UPDATE sessions SET owned_by_admins = false WHERE id = $1`, id)
	s.recorded(id, "session.owner_returned", map[string]any{"owner": s.marcio})
	s.sweepOwners()
	_ = s.owner.QueryRow(t0(), `SELECT text FROM directives WHERE run_id = $1 ORDER BY created_at DESC LIMIT 1`, run).Scan(&text)
	if !strings.HasPrefix(text, "Márcio Martins is back in the organisation and owns this session again") {
		t.Errorf("the agent was told %q on his return", text)
	}
	if n := told("Márcio Martins is back"); n != 1 {
		t.Errorf("owner-returned told %d times, want once", n)
	}

	// Gone again, and an admin takes it over before the sweep reads it:
	// the owner-left is not told, only the take-over.
	s.ownerLeaves(id)
	s.recorded(id, "session.owner_left", map[string]any{"owner": s.marcio, "ownerName": "Márcio Martins"})
	s.ok(s.admin, "POST", "/internal/sessions/"+id+"/take-ownership", nil)
	s.sweepOwners()
	if n := told(left); n != 1 {
		t.Errorf("a session taken over since was told its owner left: %d owner-left notices, want the first only", n)
	}
}

// recorded appends an event as the backend does (its removal, an
// invitation accept), committed now.
func (s *sessionWorld) recorded(session, typ string, payload map[string]any) {
	s.t.Helper()
	b, _ := json.Marshal(payload)
	mustExec(s.t, s.owner, `INSERT INTO events (id, organization_id, event_type, session_id, actor_type, actor_id, source, payload)
		VALUES ('evt_' || md5(random()::text), $1, $2, NULLIF($3, ''), 'system', 'dude', 'control-plane', $4::jsonb)`,
		s.org, typ, session, string(b))
}

// sweepOwners runs the notifier's sweep as a fresh orchestrator would.
func (s *sessionWorld) sweepOwners() {
	s.t.Helper()
	n := &notify.Notifier{DB: s.app, Log: quiet, Subject: "ops@example.com"}
	if _, err := n.Sweep(t0()); err != nil {
		s.t.Fatal(err)
	}
}

// A handover to someone being removed: the removal holds their row FOR
// UPDATE (the backend's removePerson) while the handover's acceptance runs
// under the session's lock. The acceptance waits on that row and, once the
// removal commits, makes nobody removed the owner. Accepted first instead,
// the removal's re-read sees the session as theirs (the backend's test of
// that order: org-sessions.test.ts).
func TestOrgSessionAHandoverRacingTheRemovalOfItsInvitee(t *testing.T) {
	s := newSessionWorld(t)
	for _, route := range []string{"accept", "take-ownership"} {
		t.Run(route, func(t *testing.T) {
			id := s.session()
			who := s.ana
			if route == "accept" {
				s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/owner", map[string]any{"person": s.ana})
			} else {
				s.join(id, s.ana, "chat")
				s.ownerLeaves(id)
				who = s.admin
			}
			removal, err := s.owner.Begin(t0())
			if err != nil {
				t.Fatal(err)
			}
			defer removal.Rollback(t0())
			if _, err := removal.Exec(t0(), `SELECT 1 FROM people WHERE id = $1 FOR UPDATE`, who); err != nil {
				t.Fatal(err)
			}
			result := make(chan int, 1)
			go func() {
				status, _ := s.as(who, "POST", "/internal/sessions/"+id+"/"+route, nil)
				result <- status
			}()
			// The request waits on the person's row (a tuple lock), not the session's.
			for deadline := time.Now().Add(10 * time.Second); ; {
				if s.count(`SELECT count(*) FROM pg_locks l JOIN pg_stat_activity a ON a.pid = l.pid
					WHERE a.datname = current_database() AND NOT l.granted AND l.locktype IN ('transactionid', 'tuple')`) > 0 {
					break
				}
				select {
				case status := <-result:
					t.Fatalf("%s finished (%d) without waiting on the person's row", route, status)
				default:
				}
				if time.Now().After(deadline) {
					t.Fatalf("%s never waited on the person's row", route)
				}
			}
			if _, err := removal.Exec(t0(), `UPDATE people SET removed_at = now() WHERE id = $1`, who); err != nil {
				t.Fatal(err)
			}
			if err := removal.Commit(t0()); err != nil {
				t.Fatal(err)
			}
			select {
			case status := <-result:
				if status < 400 {
					t.Errorf("%s by someone removed meanwhile: %d", route, status)
				}
			case <-time.After(10 * time.Second):
				t.Fatalf("%s never finished", route)
			}
			if n := s.count(`SELECT count(*) FROM session_people WHERE session_id = $1 AND person_id = $2 AND role = 'owner'`, id, who); n != 0 {
				t.Errorf("a removed person became the owner by %s", route)
			}
			mustExec(t, s.owner, `UPDATE people SET removed_at = NULL WHERE id IN ($1, $2)`, who, s.marcio)
		})
	}
}

// Taken over while the departed owner's handover is still pending: the
// invitation is an ordinary one from then on (not an ownership offer in the
// invitee's list), and accepting it makes them a chat member.
func TestOrgSessionTakeOwnershipVoidsAPendingHandover(t *testing.T) {
	s := newSessionWorld(t)
	id := s.session()
	s.join(id, s.ana, "chat")
	s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/owner", map[string]any{"person": s.joao})
	s.ownerLeaves(id)
	s.ok(s.admin, "POST", "/internal/sessions/"+id+"/take-ownership", nil)
	if n := s.count(`SELECT count(*) FROM session_people WHERE session_id = $1 AND person_id = $2 AND becomes_owner`, id, s.joao); n != 0 {
		t.Errorf("the departed owner's handover is still pending")
	}
	invited := false
	for _, inv := range s.ok(s.joao, "GET", "/internal/sessions", nil)["invitations"].([]any) {
		if i := inv.(map[string]any); i["id"] == id {
			invited = true
			if i["becomesOwner"] != false {
				t.Errorf("João's invitation still reads as an ownership offer: %v", i)
			}
		}
	}
	if !invited {
		t.Errorf("João's invitation is not listed")
	}
	s.ok(s.joao, "POST", "/internal/sessions/"+id+"/accept", nil)
	if status, role, member := s.role(s.joao, id); status != 200 || role != "chat" || !member {
		t.Errorf("João after accepting: %d %q member=%v", status, role, member)
	}
}

// The session's detail says whether the caller may rename it, by the rule
// the rename route applies: a read row lifted to chat in an organisation
// session may not, nor someone who never joined; the owner, a chat member
// and an admin of an admin-owned session may.
func TestOrgSessionDetailSaysWhoMayRename(t *testing.T) {
	s := newSessionWorld(t)
	id := s.openToOrg()
	s.join(id, s.ana, "chat")
	may := func(who string) bool {
		status, out := s.as(who, "GET", "/internal/sessions/"+id, nil)
		if status != 200 {
			t.Fatalf("%s reading it: %d", who, status)
		}
		return out["you"].(map[string]any)["mayRename"].(bool)
	}
	for who, want := range map[string]bool{s.marcio: true, s.ana: true, s.joao: false, s.outsider: false} {
		if got := may(who); got != want {
			t.Errorf("%s may rename: %v, want %v", who, got, want)
		}
		status, _ := s.as(who, "POST", "/internal/sessions/"+id+"/title", map[string]any{"title": "Renamed"})
		if (status == 200) != want {
			t.Errorf("%s renaming: %d, the detail said %v", who, status, want)
		}
	}
	s.ownerLeaves(id)
	if !may(s.admin) {
		t.Errorf("an admin of an admin-owned session may not rename it")
	}
}
