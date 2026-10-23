package orchestrator_test

import (
	"context"
	"strings"
	"testing"
)

// A handover the owner offered before they left voids with them: accepting
// it while the admins own the session makes the invitee an ordinary member,
// and taking the session over keeps them. Take-ownership never deletes an
// owner still in the organisation.
func TestOrgSessionHandoverOfADepartedOwnerIsVoid(t *testing.T) {
	s := newSessionWorld(t)
	id := s.session()
	s.join(id, s.ana, "chat")
	s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/owner", map[string]any{"person": s.joao})
	s.ownerLeaves(id)
	s.ok(s.joao, "POST", "/internal/sessions/"+id+"/accept", nil)
	if n := s.count(`SELECT count(*) FROM session_people WHERE session_id = $1 AND person_id = $2 AND role = 'owner'`, id, s.joao); n != 0 {
		t.Fatalf("a removed owner's handover took effect while its admins own the session")
	}
	if n := s.count(`SELECT count(*) FROM session_people WHERE session_id = $1 AND person_id = $2 AND role = 'chat'
		AND accepted_at IS NOT NULL`, id, s.joao); n != 1 {
		t.Errorf("João did not join as an ordinary member")
	}
	s.ok(s.admin, "POST", "/internal/sessions/"+id+"/take-ownership", nil)
	if n := s.count(`SELECT count(*) FROM session_people WHERE session_id = $1 AND person_id = $2`, id, s.joao); n != 1 {
		t.Errorf("take-ownership deleted an active member (João)")
	}
	if n := s.count(`SELECT count(*) FROM session_people WHERE session_id = $1 AND role = 'owner' AND person_id = $2`, id, s.admin); n != 1 {
		t.Errorf("the admin is not its owner")
	}

	// Its flag set by hand while the owner is still here: refused, nobody removed.
	mustExec(t, s.owner, `UPDATE people SET removed_at = NULL WHERE id = $1`, s.marcio)
	other := s.session()
	mustExec(t, s.owner, `UPDATE sessions SET owned_by_admins = true WHERE id = $1`, other)
	if status, out := s.as(s.admin, "POST", "/internal/sessions/"+other+"/take-ownership", nil); status != 409 {
		t.Errorf("an admin took over a session whose owner is still here: %d %v", status, out)
	}
	if n := s.count(`SELECT count(*) FROM session_people WHERE session_id = $1 AND person_id = $2 AND role = 'owner'`, other, s.marcio); n != 1 {
		t.Errorf("the active owner was removed")
	}
}

// Taking a session over tells its live agent who owns it now, through the
// owner-changed notice; the departed owner's questions go nowhere.
func TestOrgSessionTakeOwnershipTellsTheAgent(t *testing.T) {
	s := newSessionWorld(t)
	id := s.session()
	s.join(id, s.ana, "chat")
	s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/chat", map[string]any{"text": "let's plan"})
	run := s.started(id)
	s.ownerLeaves(id)
	s.ok(s.admin, "POST", "/internal/sessions/"+id+"/take-ownership", nil)
	var text string
	_ = s.owner.QueryRow(context.Background(), `SELECT text FROM directives WHERE run_id = $1 ORDER BY created_at DESC LIMIT 1`, run).Scan(&text)
	if !strings.Contains(text, "Ada Admin owns this session now") || !strings.Contains(text, "Márcio Martins left it") {
		t.Errorf("the agent was told %q", text)
	}
	if n := s.count(`SELECT count(*) FROM events WHERE session_id = $1 AND event_type = 'session.told' AND payload->>'text' = $2`, id, text); n != 1 {
		t.Errorf("session.told for the notice: %d", n)
	}
}

// An agent may put a question by name to a reader of an organisation
// session (they can chat there), not to a reader of a private one.
func TestOrgSessionAgentAsksAReaderOfAnOpenSession(t *testing.T) {
	s := newSessionWorld(t)
	s.withTools()
	id := s.session()
	s.join(id, s.joao, "read")
	s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/chat", map[string]any{"text": "let's plan"})
	run := s.started(id)
	if status, out := s.tool(run, "ask_person", `{"question":"Which plan?","to":"João"}`); status != 422 {
		t.Fatalf("asked a reader of a private session: %d %v", status, out)
	}
	s.ok(s.marcio, "PUT", "/internal/sessions/"+id+"/access", map[string]any{"access": "organisation"})
	if status, out := s.tool(run, "ask_person", `{"question":"Which plan?","to":"João"}`); status != 200 {
		t.Fatalf("could not ask a reader of an organisation session: %d %v", status, out)
	}
	if n := s.count(`SELECT count(*) FROM questions q JOIN runs r ON r.id = q.run_id WHERE r.session_id = $1 AND q.to_person = $2`, id, s.joao); n != 1 {
		t.Errorf("the question is not João's")
	}
}

// Share and send with someone already invited and not yet in: they are
// accepted as chat and mentioned. A removed person in shareWith is dropped.
func TestOrgSessionShareForMentionAcceptsAPendingInvitee(t *testing.T) {
	s := newSessionWorld(t)
	id := s.session()
	s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/people", map[string]any{"people": []string{s.ana}, "role": "read"})
	mustExec(t, s.owner, `UPDATE people SET removed_at = now() WHERE id = $1`, s.outsider)
	s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/chat", map[string]any{"text": "@Ana Nunes look",
		"mentions": []string{s.ana}, "shareWith": []string{s.ana, s.outsider}})
	if n := s.count(`SELECT count(*) FROM session_people WHERE session_id = $1 AND person_id = $2 AND role = 'chat'
		AND accepted_at IS NOT NULL`, id, s.ana); n != 1 {
		t.Errorf("Ana's pending invitation was not accepted as chat")
	}
	if n := s.count(`SELECT count(*) FROM session_mentions WHERE session_id = $1 AND person_id = $2`, id, s.ana); n != 1 {
		t.Errorf("Ana was not mentioned")
	}
	if n := s.count(`SELECT count(*) FROM session_people WHERE session_id = $1 AND person_id = $2`, id, s.outsider); n != 0 {
		t.Errorf("a removed person was added")
	}
	if n := s.count(`SELECT count(*) FROM events WHERE session_id = $1 AND event_type = 'session.shared'
		AND payload->'people' ? $2`, id, s.outsider); n != 0 {
		t.Errorf("a removed person is named in session.shared")
	}
}

// Renaming takes an explicit membership: an organisation member who never
// joined gets a 403 and is not joined; a chat member and the owner rename;
// an admin renames a session its admins own. Sending, answering and Try
// again join (TestOrgSessionWritingJoinsTheSession for sending).
func TestOrgSessionRenamingNeedsAMembership(t *testing.T) {
	s := newSessionWorld(t)
	id := s.openToOrg()
	if status, out := s.as(s.ana, "POST", "/internal/sessions/"+id+"/title", map[string]any{"title": "Mine now"}); status != 403 {
		t.Errorf("a non-member renamed an organisation session: %d %v", status, out)
	}
	if n := s.count(`SELECT count(*) FROM session_people WHERE session_id = $1 AND person_id = $2`, id, s.ana); n != 0 {
		t.Errorf("a refused rename joined Ana")
	}
	// João's read row counts as chat here, but is not a chat membership.
	if status, _ := s.as(s.joao, "POST", "/internal/sessions/"+id+"/title", map[string]any{"title": "Reader's"}); status != 403 {
		t.Errorf("a reader row renamed it: %d", status)
	}
	s.ok(s.ana, "POST", "/internal/sessions/"+id+"/chat", map[string]any{"text": "hi"})
	s.ok(s.ana, "POST", "/internal/sessions/"+id+"/title", map[string]any{"title": "Ana's name"})
	s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/title", map[string]any{"title": "Owner's name"})
	s.ownerLeaves(id)
	s.ok(s.admin, "POST", "/internal/sessions/"+id+"/title", map[string]any{"title": "Admin's name"})
	if n := s.count(`SELECT count(*) FROM session_people WHERE session_id = $1 AND person_id = $2`, id, s.admin); n != 0 {
		t.Errorf("an admin renaming an admin-owned session joined it")
	}
}

// Archiving is for one of its people: a non-member of an organisation
// session gets a 409 that says so, not "not found".
func TestOrgSessionArchiveByANonMemberSaysWhy(t *testing.T) {
	s := newSessionWorld(t)
	id := s.openToOrg()
	if status, out := s.as(s.ana, "POST", "/internal/sessions/"+id+"/archive", nil); status != 409 {
		t.Errorf("a non-member archiving: %d %v", status, out)
	}
	if status, _ := s.as(s.outsider, "POST", "/internal/sessions/"+s.session()+"/archive", nil); status != 404 {
		t.Errorf("someone outside a private session archiving: %d", status)
	}
}

// A non-member of an organisation session looking at it (session_viewers)
// is not pushed and has nothing waiting; once they leave, the mention
// waits. "Open" is a heartbeat within 90 seconds: one at 89 s is open, one
// at 91 s is not.
func TestOrgSessionMentionOfAViewerAndTheNinetySecondBound(t *testing.T) {
	s := newSessionWorld(t)
	id := s.openToOrg()
	viewer := func(ago string) {
		mustExec(t, s.owner, `INSERT INTO session_viewers (session_id, person_id, organization_id, open_at)
			VALUES ($1, $2, $3, now() - $4::interval)
			ON CONFLICT (session_id, person_id) DO UPDATE SET open_at = EXCLUDED.open_at`, id, s.ana, s.org, ago)
	}
	pushes := func() int {
		return s.count(`SELECT count(*) FROM events WHERE session_id = $1 AND event_type = 'session.mentioned' AND payload->>'person' = $2`, id, s.ana)
	}
	waiting := func() int { return len(s.ok(s.ana, "GET", "/internal/sessions", nil)["mentions"].([]any)) }

	viewer("0 seconds")
	s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/chat", map[string]any{"text": "@Ana Nunes one", "mentions": []string{s.ana}})
	if pushes() != 0 || waiting() != 0 {
		t.Errorf("a viewer looking at it: %d pushes, %d waiting", pushes(), waiting())
	}
	if n := s.count(`SELECT count(*) FROM session_mentions WHERE session_id = $1 AND person_id = $2 AND seen_at IS NULL`, id, s.ana); n != 1 {
		t.Errorf("unseen rows: %d, want 1", n)
	}
	mustExec(t, s.owner, `UPDATE session_viewers SET open_at = NULL WHERE session_id = $1 AND person_id = $2`, id, s.ana)
	if waiting() != 1 {
		t.Errorf("the viewer left: %d waiting, want 1", waiting())
	}

	mustExec(t, s.owner, `UPDATE session_mentions SET seen_at = now() WHERE person_id = $1`, s.ana)
	viewer("89 seconds")
	s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/chat", map[string]any{"text": "@Ana Nunes two", "mentions": []string{s.ana}})
	if pushes() != 0 || waiting() != 0 {
		t.Errorf("a heartbeat 89 s ago is open: %d pushes, %d waiting", pushes(), waiting())
	}
	mustExec(t, s.owner, `UPDATE session_mentions SET seen_at = now() WHERE person_id = $1`, s.ana)
	viewer("91 seconds")
	s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/chat", map[string]any{"text": "@Ana Nunes three", "mentions": []string{s.ana}})
	if pushes() != 1 || waiting() != 1 {
		t.Errorf("a heartbeat 91 s ago is not open: %d pushes, %d waiting", pushes(), waiting())
	}

	// A member's own heartbeat on session_people, at the same bound.
	priv := s.session()
	s.join(priv, s.joao, "chat")
	mustExec(t, s.owner, `UPDATE session_people SET open_at = now() - interval '91 seconds' WHERE session_id = $1 AND person_id = $2`, priv, s.joao)
	s.ok(s.marcio, "POST", "/internal/sessions/"+priv+"/chat", map[string]any{"text": "@João Reis", "mentions": []string{s.joao}})
	if n := s.count(`SELECT count(*) FROM events WHERE session_id = $1 AND event_type = 'session.mentioned'`, priv); n != 1 {
		t.Errorf("a member's heartbeat 91 s ago: %d pushes, want 1", n)
	}
}

// A mention of a session you can no longer open does not wait on you.
func TestOrgSessionMentionOfARemovedMemberStopsWaiting(t *testing.T) {
	s := newSessionWorld(t)
	id := s.session()
	s.join(id, s.ana, "chat")
	s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/chat", map[string]any{"text": "@Ana Nunes look", "mentions": []string{s.ana}})
	if n := len(s.ok(s.ana, "GET", "/internal/sessions", nil)["mentions"].([]any)); n != 1 {
		t.Fatalf("Ana's mentions: %d, want 1", n)
	}
	s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/people/"+s.ana+"/remove", nil)
	if n := len(s.ok(s.ana, "GET", "/internal/sessions", nil)["mentions"].([]any)); n != 0 {
		t.Errorf("a mention of a session Ana can no longer open still waits: %d", n)
	}
}

// The route's negatives: PUT access by someone outside a private session is
// a 404; take-ownership by a removed admin or another organisation's is a
// 403 (the principal is refused), and by a member with a role on an owned
// session a 409; /open leaves out a removed caller; more than 20 mentions or
// shares is a 400 at the orchestrator.
func TestOrgSessionRouteRefusals(t *testing.T) {
	s := newSessionWorld(t)
	id := s.session()
	s.join(id, s.ana, "chat")
	if status, _ := s.as(s.joao, "PUT", "/internal/sessions/"+id+"/access", map[string]any{"access": "organisation"}); status != 404 {
		t.Errorf("PUT access by a non-member of a private session: %d", status)
	}
	if status, _ := s.as(s.ana, "POST", "/internal/sessions/"+id+"/take-ownership", nil); status != 409 {
		t.Errorf("take-ownership by a member of an owned session: %d", status)
	}
	many := make([]string, 21)
	for i := range many {
		many[i] = s.ana
	}
	for _, field := range []string{"mentions", "shareWith"} {
		if status, _ := s.as(s.marcio, "POST", "/internal/sessions/"+id+"/chat", map[string]any{"text": "x", field: many}); status != 400 {
			t.Errorf("21 %s: %d", field, status)
		}
	}
	open := s.openToOrg()
	s.ownerLeaves(id)
	mustExec(t, s.owner, `UPDATE people SET removed_at = NULL WHERE id = $1`, s.marcio)
	mustExec(t, s.owner, `UPDATE people SET role = 'admin' WHERE id = $1`, s.outsider)
	mustExec(t, s.owner, `UPDATE people SET removed_at = now() WHERE id = $1`, s.outsider)
	if status, _ := s.as(s.outsider, "POST", "/internal/sessions/"+id+"/take-ownership", nil); status != 403 {
		t.Errorf("take-ownership by a removed admin: %d", status)
	}
	if status, _ := s.as(s.outsider, "GET", "/internal/sessions/open", nil); status != 403 {
		t.Errorf("a removed caller read the open list: %d", status)
	}
	if n := len(s.ok(s.joao, "GET", "/internal/sessions/open", nil)["sessions"].([]any)); n != 1 {
		t.Errorf("João's open list: %d, want 1 (%s)", n, open)
	}
}
