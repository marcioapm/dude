package orchestrator_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
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
// session over withdraws it; so does the backend telling the orchestrator
// the owner left, before anyone takes it over.
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
		} else if status, out := s.system("POST", "/internal/people/"+s.marcio+"/sessions",
			map[string]any{"change": "left", "sessions": []string{id}}); status != 200 {
			t.Fatalf("left: %d %v", status, out)
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

// The backend's "left" tells each session passed to the admins, and only
// those still theirs, through the owner-left notice; a person's principal
// may not call it; a person still here is not "left".
func TestOrgSessionLeftTellsTheAgentAndWithdrawsEverywhere(t *testing.T) {
	s := newSessionWorld(t)
	s.withTools()
	id := s.session()
	s.join(id, s.ana, "chat")
	s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/chat", map[string]any{"text": "let's plan"})
	run := s.started(id)
	// A session Ana does not own: a question to her there is withdrawn too.
	asked := s.askedBy(run, "Ana")

	if status, _ := s.as(s.admin, "POST", "/internal/people/"+s.marcio+"/sessions", map[string]any{"change": "left"}); status != 403 {
		t.Errorf("a person called the backend's own route: %d", status)
	}
	if status, _ := s.system("POST", "/internal/people/"+s.ana+"/sessions", map[string]any{"change": "left"}); status != 409 {
		t.Errorf("left for someone still here: %d", status)
	}
	mustExec(t, s.owner, `UPDATE people SET removed_at = now() WHERE id = $1`, s.ana)
	if status, out := s.system("POST", "/internal/people/"+s.ana+"/sessions", map[string]any{"change": "left", "sessions": []string{id}}); status != 200 {
		t.Fatalf("left: %d %v", status, out)
	} else if told := out["told"].([]any); len(told) != 0 {
		t.Errorf("a session Ana did not own was told she left: %v", told)
	}
	if got := s.questionStatus(asked); got != "cancelled" {
		t.Errorf("the question to a removed member is %q", got)
	}

	s.ownerLeaves(id)
	if status, out := s.system("POST", "/internal/people/"+s.marcio+"/sessions", map[string]any{"change": "left", "sessions": []string{id}}); status != 200 {
		t.Fatalf("left: %d %v", status, out)
	}
	var text string
	_ = s.owner.QueryRow(t0(), `SELECT text FROM directives WHERE run_id = $1 ORDER BY created_at DESC LIMIT 1`, run).Scan(&text)
	if !strings.HasPrefix(text, "Márcio Martins left the organisation. Its admins own this session now") {
		t.Errorf("the agent was told %q", text)
	}
	if n := s.count(`SELECT count(*) FROM events WHERE session_id = $1 AND event_type = 'session.told' AND payload->>'text' = $2`, id, text); n != 1 {
		t.Errorf("session.told for the notice: %d", n)
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
