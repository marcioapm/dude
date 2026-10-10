package orchestrator_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
)

// listed is the ids in the caller's session list (with ?archived=1 when
// all), each with whether it says it is archived.
func (s *sessionWorld) listed(person string, all bool) map[string]bool {
	s.t.Helper()
	path := "/internal/sessions"
	if all {
		path += "?archived=1"
	}
	out := map[string]bool{}
	for _, row := range s.ok(person, "GET", path, nil)["sessions"].([]any) {
		m := row.(map[string]any)
		archived, ok := m["archived"].(bool)
		if !ok {
			s.t.Fatalf("a listed session carries no archived: %v", m)
		}
		out[m["id"].(string)] = archived
	}
	return out
}

// sessionState is everything about a session but its members' archive
// marks: the session's row, its people's rows, its Runs, directives,
// proposals and ledger, as JSON to compare.
func (s *sessionWorld) sessionState(id string) string {
	s.t.Helper()
	var state string
	if err := s.owner.QueryRow(context.Background(), `SELECT json_build_object(
			'session', (SELECT row_to_json(x) FROM sessions x WHERE x.id = $1),
			'people', (SELECT json_agg(to_jsonb(sp) - 'archived_at' ORDER BY sp.person_id) FROM session_people sp WHERE sp.session_id = $1),
			'runs', (SELECT json_agg(json_build_object('id', r.id, 'status', r.status, 'pause', r.dude_pause) ORDER BY r.id)
				FROM runs r WHERE r.session_id = $1),
			'directives', (SELECT count(*) FROM directives d JOIN runs r ON r.id = d.run_id WHERE r.session_id = $1),
			'proposals', (SELECT json_agg(p.id ORDER BY p.id) FROM session_proposals p WHERE p.session_id = $1),
			'events', (SELECT json_agg(e.id ORDER BY e.cursor) FROM events e WHERE e.session_id = $1))::text`, id).Scan(&state); err != nil {
		s.t.Fatal(err)
	}
	return state
}

// Any accepted member archives a session for themselves — the owner, a
// member who can chat, a reader — and unarchives it. It leaves their list
// by default and comes back with ?archived=1, marked; nobody else's list
// changes. Archiving or unarchiving twice is the same as once.
func TestAMemberArchivesASessionForThemselvesAlone(t *testing.T) {
	s := newSessionWorld(t)
	id := s.session()
	s.join(id, s.ana, "chat")
	s.join(id, s.joao, "read")
	people := map[string]string{s.marcio: "the owner", s.ana: "a member who can chat", s.joao: "a reader"}
	for who, name := range people {
		for range 2 {
			if out := s.ok(who, "POST", "/internal/sessions/"+id+"/archive", nil); out["archived"] != true {
				t.Errorf("%s archiving: %v", name, out)
			}
		}
		if _, ok := s.listed(who, false)[id]; ok {
			t.Errorf("%s still lists the session they archived", name)
		}
		if archived, ok := s.listed(who, true)[id]; !ok || !archived {
			t.Errorf("%s with ?archived=1: listed %v, archived %v", name, ok, archived)
		}
		if you := s.ok(who, "GET", "/internal/sessions/"+id, nil)["you"].(map[string]any); you["archived"] != true {
			t.Errorf("%s's session says archived %v", name, you["archived"])
		}
		for other, otherName := range people {
			if other == who {
				continue
			}
			if archived, ok := s.listed(other, false)[id]; !ok || archived {
				t.Errorf("%s archiving it changed %s's list: listed %v, archived %v", name, otherName, ok, archived)
			}
		}
		for range 2 {
			if out := s.ok(who, "POST", "/internal/sessions/"+id+"/unarchive", nil); out["archived"] != false {
				t.Errorf("%s unarchiving: %v", name, out)
			}
		}
		if archived, ok := s.listed(who, false)[id]; !ok || archived {
			t.Errorf("%s after unarchiving: listed %v, archived %v", name, ok, archived)
		}
		if you := s.ok(who, "GET", "/internal/sessions/"+id, nil)["you"].(map[string]any); you["archived"] != false {
			t.Errorf("%s's session after unarchiving says archived %v", name, you["archived"])
		}
	}
}

// Archiving again keeps the time it was first archived.
func TestArchivingTwiceKeepsTheFirstTime(t *testing.T) {
	s := newSessionWorld(t)
	id := s.session()
	s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/archive", nil)
	mustExec(t, s.owner, `UPDATE session_people SET archived_at = '2026-01-01T00:00:00Z' WHERE session_id = $1 AND person_id = $2`, id, s.marcio)
	s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/archive", nil)
	if n := s.count(`SELECT count(*) FROM session_people WHERE session_id = $1 AND person_id = $2
		AND archived_at = '2026-01-01T00:00:00Z'`, id, s.marcio); n != 1 {
		t.Errorf("archiving again moved the time it was archived")
	}
}

// Someone not in the session — not a member, an invitee who has not
// accepted, an admin — gets the same 404 as every other route, and marks
// nothing.
func TestOnlyAMemberArchivesASession(t *testing.T) {
	s := newSessionWorld(t)
	id := s.session()
	s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/people", map[string]any{"people": []string{s.ana}, "role": "chat"})
	for _, who := range []struct{ name, id string }{{"someone else", s.outsider}, {"an invitee", s.ana}, {"an admin", s.admin}} {
		for _, verb := range []string{"archive", "unarchive"} {
			status, out := s.as(who.id, "POST", "/internal/sessions/"+id+"/"+verb, nil)
			if status != 404 {
				t.Errorf("%s: %s = %d %v, want 404", who.name, verb, status, out)
			}
			if b, _ := json.Marshal(out); strings.Contains(string(b), "Usage-based billing") {
				t.Errorf("%s's 404 names the session: %s", who.name, b)
			}
		}
	}
	if status, _ := s.as(s.marcio, "POST", "/internal/sessions/ssn_nothing/archive", nil); status != 404 {
		t.Errorf("no such session: %d, want 404", status)
	}
	if n := s.count(`SELECT count(*) FROM session_people WHERE session_id = $1 AND archived_at IS NOT NULL`, id); n != 0 {
		t.Errorf("%d archive marks after non-members tried", n)
	}
	// The invitation still waits for Ana.
	if b, _ := json.Marshal(s.ok(s.ana, "GET", "/internal/sessions", nil)["invitations"]); !strings.Contains(string(b), id) {
		t.Errorf("the invitee's invitations: %s", b)
	}
}

// Archiving changes nothing but the archiver's mark: the session, its
// people, its agent, its proposals and its ledger are as they were — no
// event is recorded — and a question its agent put to the archiver still
// waits in their inbox. A message in it afterwards does not unarchive it.
func TestArchivingChangesNothingElse(t *testing.T) {
	s := newSessionWorld(t)
	s.withTools()
	id := s.session()
	s.join(id, s.ana, "chat")
	s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/chat", map[string]any{"text": "how long is the window?"})
	run := s.started(id)
	status, out := s.tool(run, "ask_person", `{"question":"Grow the 24h window?","choices":["Yes","No"],"to":"Ana"}`)
	if status != 200 {
		t.Fatalf("ask_person: %d %v", status, out)
	}
	qid := out["questionId"].(string)
	s.proposal(id, []delivery.ProposalItem{{Kind: "task", Project: "BL", Title: "Meter runs", Goal: "Count experiment runs per org per day"}})

	before := s.sessionState(id)
	s.ok(s.ana, "POST", "/internal/sessions/"+id+"/archive", nil)
	if after := s.sessionState(id); after != before {
		t.Errorf("archiving changed the session:\nbefore %s\nafter  %s", before, after)
	}
	if b, _ := json.Marshal(s.ok(s.ana, "GET", "/internal/sessions", nil)["questions"]); !strings.Contains(string(b), qid) {
		t.Errorf("the question to Ana left her inbox when she archived: %s", b)
	}
	s.ok(s.ana, "POST", "/internal/sessions/"+id+"/unarchive", nil)
	if after := s.sessionState(id); after != before {
		t.Errorf("unarchiving changed the session:\nbefore %s\nafter  %s", before, after)
	}

	s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/archive", nil)
	s.ok(s.ana, "POST", "/internal/sessions/"+id+"/chat", map[string]any{"text": "every kind, then"})
	if _, ok := s.listed(s.marcio, false)[id]; ok {
		t.Errorf("a message in the session unarchived it")
	}
}
