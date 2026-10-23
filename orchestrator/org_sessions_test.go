package orchestrator_test

import (
	"context"
	"testing"
)

// openToOrg makes a session of Márcio's, shares it with João as a reader,
// and opens it to the organisation.
func (s *sessionWorld) openToOrg() string {
	s.t.Helper()
	id := s.session()
	s.join(id, s.joao, "read")
	s.ok(s.marcio, "PUT", "/internal/sessions/"+id+"/access", map[string]any{"access": "organisation"})
	return id
}

// role is what the session's detail says the person may do, and whether
// they are one of its people; 0 and "" when it refuses them.
func (s *sessionWorld) role(person, id string) (int, string, bool) {
	s.t.Helper()
	status, out := s.as(person, "GET", "/internal/sessions/"+id, nil)
	if status != 200 {
		return status, "", false
	}
	you := out["you"].(map[string]any)
	return status, you["role"].(string), you["member"].(bool)
}

// A private session is its people's alone. Opened to the organisation,
// every active member may open it and chat, a reader counts as chat, and
// someone removed or of another organisation still gets the 404.
func TestOrgSessionAccessLetsTheOrganisationChat(t *testing.T) {
	s := newSessionWorld(t)
	id := s.session()
	s.join(id, s.joao, "read")
	for _, who := range []string{s.ana, s.admin} {
		if status, _, _ := s.role(who, id); status != 404 {
			t.Errorf("%s opened a private session: %d", who, status)
		}
	}
	if status, out := s.as(s.joao, "POST", "/internal/sessions/"+id+"/chat", map[string]any{"text": "hi"}); status != 403 {
		t.Errorf("a reader wrote in a private session: %d %v", status, out)
	}

	s.ok(s.marcio, "PUT", "/internal/sessions/"+id+"/access", map[string]any{"access": "organisation"})
	if n := s.count(`SELECT count(*) FROM events WHERE session_id = $1 AND event_type = 'session.access_changed'
		AND payload->>'access' = 'organisation' AND actor_id = $2`, id, s.marcio); n != 1 {
		t.Errorf("session.access_changed: %d, want 1", n)
	}
	for who, want := range map[string]string{s.joao: "chat", s.ana: "chat", s.outsider: "chat", s.marcio: "owner"} {
		if status, role, _ := s.role(who, id); status != 200 || role != want {
			t.Errorf("%s: %d %q, want %q", who, status, role, want)
		}
	}

	// Removed from the organisation, or another organisation's person asserted
	// here: the API refuses the principal before any session code, and the
	// role itself gives them nothing (the ledger's visibility reads it too).
	other := "org_far_" + s.org
	mustExec(t, s.owner, `INSERT INTO organizations (id, name, slug) VALUES ($1, $1, $1)`, other)
	far := "per_far_" + s.org
	mustExec(t, s.owner, `INSERT INTO people (id, organization_id, name) VALUES ($1, $2, 'Far Away')`, far, other)
	mustExec(t, s.owner, `UPDATE people SET removed_at = now() WHERE id = $1`, s.outsider)
	for _, who := range []string{s.outsider, far} {
		if status, _, _ := s.role(who, id); status != 403 {
			t.Errorf("%s opened it: %d", who, status)
		}
		if status, _ := s.as(who, "POST", "/internal/sessions/"+id+"/chat", map[string]any{"text": "hi"}); status != 403 {
			t.Errorf("%s wrote in it: %d", who, status)
		}
		if n := s.count(`SELECT count(*) FROM (SELECT session_role($1, $2) r) x WHERE r IS NOT NULL`, id, who); n != 0 {
			t.Errorf("%s has a role in it", who)
		}
	}
}

// A member of the organisation who was never added writes in an open
// session: their message goes in, and they join it as chat, once, with
// session.joined via organisation. Reading it alone joins nobody.
func TestOrgSessionWritingJoinsTheSession(t *testing.T) {
	s := newSessionWorld(t)
	id := s.openToOrg()
	if status, _, member := s.role(s.ana, id); status != 200 || member {
		t.Fatalf("Ana before writing: %d member=%v", status, member)
	}
	if n := s.count(`SELECT count(*) FROM session_people WHERE session_id = $1 AND person_id = $2`, id, s.ana); n != 0 {
		t.Fatalf("reading it made Ana one of its people")
	}
	s.ok(s.ana, "POST", "/internal/sessions/"+id+"/chat", map[string]any{"text": "the metering table is huge"})
	s.ok(s.ana, "POST", "/internal/sessions/"+id+"/chat", map[string]any{"text": "and slow"})
	if n := s.count(`SELECT count(*) FROM session_people WHERE session_id = $1 AND person_id = $2 AND role = 'chat'
		AND accepted_at IS NOT NULL`, id, s.ana); n != 1 {
		t.Errorf("Ana's membership after writing: %d, want 1 chat", n)
	}
	if n := s.count(`SELECT count(*) FROM events WHERE session_id = $1 AND event_type = 'session.joined'
		AND payload->>'person' = $2 AND payload->>'via' = 'organisation' AND actor_id = $2`, id, s.ana); n != 1 {
		t.Errorf("session.joined via organisation: %d, want 1", n)
	}
	if n := s.count(`SELECT count(*) FROM events WHERE session_id = $1 AND event_type = 'chat.message' AND actor_id = $2`, id, s.ana); n != 2 {
		t.Errorf("Ana's messages: %d, want 2", n)
	}
	// The reader João, lifted to chat, writes as chat without a second row.
	s.ok(s.joao, "POST", "/internal/sessions/"+id+"/chat", map[string]any{"text": "agreed"})
	if n := s.count(`SELECT count(*) FROM session_people WHERE session_id = $1 AND person_id = $2 AND role = 'read'`, id, s.joao); n != 1 {
		t.Errorf("João's read row changed: %d", n)
	}
	if n := s.count(`SELECT count(*) FROM events WHERE session_id = $1 AND event_type = 'session.joined' AND payload->>'person' = $2
		AND payload->>'via' = 'organisation'`, id, s.joao); n != 0 {
		t.Errorf("João, already in it, joined again: %d", n)
	}
	if _, ok := s.listed(s.ana, false)[id]; !ok {
		t.Errorf("a session Ana joined by writing is not in her list")
	}
}

// Access is one way and the owner's: anything but organisation is a 409,
// a chat member may not open it, and the database refuses going back.
func TestOrgSessionAccessIsOneWayAndTheOwners(t *testing.T) {
	s := newSessionWorld(t)
	id := s.session()
	s.join(id, s.ana, "chat")
	if status, out := s.as(s.ana, "PUT", "/internal/sessions/"+id+"/access", map[string]any{"access": "organisation"}); status != 403 {
		t.Errorf("a chat member opened it: %d %v", status, out)
	}
	if status, out := s.as(s.marcio, "PUT", "/internal/sessions/"+id+"/access", map[string]any{"access": "private"}); status != 409 {
		t.Errorf("private: %d %v", status, out)
	}
	s.ok(s.marcio, "PUT", "/internal/sessions/"+id+"/access", map[string]any{"access": "organisation"})
	for _, access := range []string{"private", "everyone", ""} {
		if status, out := s.as(s.marcio, "PUT", "/internal/sessions/"+id+"/access", map[string]any{"access": access}); status != 409 {
			t.Errorf("%q after opening: %d %v", access, status, out)
		}
	}
	if out := s.ok(s.marcio, "PUT", "/internal/sessions/"+id+"/access", map[string]any{"access": "organisation"}); out["changed"] != false {
		t.Errorf("opening it again changed it: %v", out)
	}
	_, err := s.owner.Exec(context.Background(), `UPDATE sessions SET access = 'private' WHERE id = $1`, id)
	if err == nil {
		t.Errorf("the database let an organisation session go private")
	}
	if n := s.count(`SELECT count(*) FROM sessions WHERE id = $1 AND access = 'organisation'`, id); n != 1 {
		t.Errorf("access after the refusals: %d, want organisation", n)
	}
}

// "Open to everyone" lists every open session with joined per row, and the
// count is those joined and not archived; a private session is never listed.
func TestOrgSessionOpenListCountsTheJoined(t *testing.T) {
	s := newSessionWorld(t)
	joined := s.openToOrg()
	notJoined := s.openToOrg()
	archived := s.openToOrg()
	private := s.session()
	for _, id := range []string{joined, archived} {
		s.ok(s.ana, "POST", "/internal/sessions/"+id+"/chat", map[string]any{"text": "here"})
	}
	s.ok(s.ana, "POST", "/internal/sessions/"+archived+"/archive", nil)
	out := s.ok(s.ana, "GET", "/internal/sessions/open", nil)
	if out["joined"] != float64(1) {
		t.Errorf("joined count %v, want 1", out["joined"])
	}
	got := map[string]bool{}
	for _, row := range out["sessions"].([]any) {
		m := row.(map[string]any)
		got[m["id"].(string)] = m["joined"].(bool)
	}
	want := map[string]bool{joined: true, notJoined: false, archived: false}
	if len(got) != len(want) || got[joined] != true || got[notJoined] != false || got[archived] != false {
		t.Errorf("open list %v, want %v", got, want)
	}
	if _, ok := got[private]; ok {
		t.Errorf("a private session is listed as open")
	}
	// Sessions is yours alone: one you never joined is not in it.
	mine := s.listed(s.ana, false)
	if _, ok := mine[notJoined]; ok {
		t.Errorf("a not-joined organisation session is in Ana's Sessions")
	}
	// The sidebar's numbers ride on Sessions: three open, one joined and active.
	if got := s.ok(s.ana, "GET", "/internal/sessions", nil)["organisation"]; got == nil ||
		got.(map[string]any)["count"] != float64(3) || got.(map[string]any)["joined"] != float64(1) {
		t.Errorf("Ana's organisation summary %v, want count 3 joined 1", got)
	}
	// Its owner archives one: it is archived by owner for everyone, and still listed.
	s.ok(s.marcio, "POST", "/internal/sessions/"+notJoined+"/archive", nil)
	byOwner := map[string]bool{}
	for _, row := range s.ok(s.ana, "GET", "/internal/sessions/open", nil)["sessions"].([]any) {
		m := row.(map[string]any)
		byOwner[m["id"].(string)] = m["archivedByOwner"].(bool)
	}
	if len(byOwner) != 3 || !byOwner[notJoined] || byOwner[joined] || byOwner[archived] {
		t.Errorf("archived by owner %v, want only %s", byOwner, notJoined)
	}
}

// The Sessions list is the caller's own sessions and, for an admin, those
// its admins own; never another's private session, nor an admin-owned one
// for a member, nor one of another organisation.
func TestOrgSessionListIsYoursAndTheAdminOwned(t *testing.T) {
	s := newSessionWorld(t)
	mine := s.session()
	s.join(mine, s.ana, "chat")
	theirs := s.session()
	adminOwned := s.session()
	s.join(adminOwned, s.joao, "chat")
	mustExec(t, s.owner, `UPDATE sessions SET owned_by_admins = true WHERE id = $1`, adminOwned)
	for who, want := range map[string][]string{
		s.ana:   {mine},
		s.admin: {adminOwned},
		s.joao:  {adminOwned},
	} {
		got := s.listed(who, false)
		if len(got) != len(want) {
			t.Errorf("%s lists %v, want %v", who, got, want)
		}
		for _, id := range want {
			if _, ok := got[id]; !ok {
				t.Errorf("%s does not list %s", who, id)
			}
		}
		if _, ok := got[theirs]; ok {
			t.Errorf("%s lists a private session they are not in", who)
		}
	}
}

// ownerLeaves marks Márcio removed from the organisation and his session
// owned by its admins, as the backend's member removal does.
func (s *sessionWorld) ownerLeaves(id string) {
	s.t.Helper()
	mustExec(s.t, s.owner, `UPDATE people SET removed_at = now() WHERE id = $1`, s.marcio)
	mustExec(s.t, s.owner, `UPDATE sessions SET owned_by_admins = true WHERE id = $1`, id)
}

// While its admins own a session, an admin acts as owner — opens it, even a
// private one shared with others, and may open it to the organisation —
// and Make me owner makes them its owner and clears the flag. A member
// cannot; a session with an owner refuses it with 409.
func TestOrgSessionAdminsOwnItAfterTheOwnerLeft(t *testing.T) {
	s := newSessionWorld(t)
	id := s.session()
	s.join(id, s.ana, "chat")
	if status, out := s.as(s.admin, "POST", "/internal/sessions/"+id+"/take-ownership", nil); status != 404 {
		t.Errorf("an admin took over a private session with an owner: %d %v", status, out)
	}
	s.ownerLeaves(id)
	if status, role, member := s.role(s.admin, id); status != 200 || role != "owner" || member {
		t.Errorf("the admin on an admin-owned session: %d %q member=%v", status, role, member)
	}
	if _, ok := s.listed(s.admin, false)[id]; !ok {
		t.Errorf("an admin-owned session is not in the admin's Sessions")
	}
	if status, _, _ := s.role(s.joao, id); status != 404 {
		t.Errorf("a member not in it opened an admin-owned private session: %d", status)
	}
	if status, out := s.as(s.ana, "POST", "/internal/sessions/"+id+"/take-ownership", nil); status != 403 {
		t.Errorf("a chat member took it over: %d %v", status, out)
	}
	if status, out := s.as(s.admin, "POST", "/internal/sessions/"+id+"/owner", map[string]any{"person": s.ana}); status != 409 {
		t.Errorf("an admin handed an admin-owned session on without owning it: %d %v", status, out)
	}
	s.ok(s.admin, "PUT", "/internal/sessions/"+id+"/access", map[string]any{"access": "organisation"})

	s.ok(s.admin, "POST", "/internal/sessions/"+id+"/take-ownership", nil)
	if n := s.count(`SELECT count(*) FROM sessions WHERE id = $1 AND NOT owned_by_admins`, id); n != 1 {
		t.Errorf("the flag is still set")
	}
	if n := s.count(`SELECT count(*) FROM session_people WHERE session_id = $1 AND role = 'owner' AND person_id = $2`, id, s.admin); n != 1 {
		t.Errorf("the admin is not its owner")
	}
	if n := s.count(`SELECT count(*) FROM session_people WHERE session_id = $1 AND person_id = $2`, id, s.marcio); n != 0 {
		t.Errorf("the departed owner's row stayed")
	}
	if n := s.count(`SELECT count(*) FROM events WHERE session_id = $1 AND event_type = 'session.owner_taken'
		AND payload->>'from' = $2 AND payload->>'to' = $3`, id, s.marcio, s.admin); n != 1 {
		t.Errorf("session.owner_taken: %d, want 1", n)
	}
	if status, out := s.as(s.admin, "POST", "/internal/sessions/"+id+"/take-ownership", nil); status != 409 {
		t.Errorf("taking over a session with an owner again: %d %v", status, out)
	}
}

// mentionsOn counts the session's chat.message by the person with the text,
// and gives the mentions it carries ("" for none).
func (s *sessionWorld) mentionsOn(id, person, text string) (int, string) {
	s.t.Helper()
	var n int
	var mentions *string
	_ = s.owner.QueryRow(context.Background(), `SELECT count(*), max(payload->>'mentions') FROM events
		WHERE session_id = $1 AND event_type = 'chat.message' AND actor_id = $2 AND payload->>'text' = $3`, id, person, text).
		Scan(&n, &mentions)
	if mentions == nil {
		return n, ""
	}
	return n, *mentions
}

// In an organisation session anyone in the organisation may be mentioned:
// each gets an unseen mention, a session.mentioned for the push, and the
// message's chips name them; the sender, an unknown id and another
// organisation's person are dropped without a word. The words reach the
// agent as written.
func TestOrgSessionMentionNotifiesWhoMayOpenIt(t *testing.T) {
	s := newSessionWorld(t)
	id := s.openToOrg()
	text := "@Ana Nunes what do you think?"
	s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/chat", map[string]any{"text": text,
		"mentions": []string{s.ana, s.marcio, "per_nobody", s.ana}})
	if n, mentions := s.mentionsOn(id, s.marcio, text); n != 1 || mentions != `["`+s.ana+`"]` {
		t.Errorf("the message: %d, mentions %s", n, mentions)
	}
	if n := s.count(`SELECT count(*) FROM session_mentions WHERE session_id = $1`, id); n != 1 {
		t.Errorf("mention rows: %d, want 1 (Ana's)", n)
	}
	if n := s.count(`SELECT count(*) FROM events WHERE session_id = $1 AND event_type = 'session.mentioned'
		AND payload->>'person' = $2 AND payload->>'byName' = 'Márcio Martins'`, id, s.ana); n != 1 {
		t.Errorf("session.mentioned for Ana: %d, want 1", n)
	}
	if n := s.count(`SELECT count(*) FROM runs WHERE session_id = $1 AND prompt LIKE '%' || $2 || '%'`, id, "Márcio Martins: "+text); n != 1 {
		t.Errorf("the agent was not briefed with the words as written")
	}
	lines := s.ok(s.ana, "GET", "/internal/sessions", nil)["mentions"].([]any)
	if len(lines) != 1 {
		t.Fatalf("Ana's mentions: %v", lines)
	}
	line := lines[0].(map[string]any)
	if line["sessionId"] != id || line["text"] != text || line["member"] != false || line["access"] != "organisation" {
		t.Errorf("Ana's mention line: %v", line)
	}
	// Opened since: seen, and no longer waiting.
	mustExec(t, s.owner, `UPDATE session_mentions SET seen_at = now() WHERE person_id = $1`, s.ana)
	if lines := s.ok(s.ana, "GET", "/internal/sessions", nil)["mentions"].([]any); len(lines) != 0 {
		t.Errorf("a seen mention still waits: %v", lines)
	}
}

// Mentioned while looking at the session: no push. It waits on them only
// once they no longer have it open.
func TestOrgSessionMentionWhileOpenSendsNoPush(t *testing.T) {
	s := newSessionWorld(t)
	id := s.session()
	s.join(id, s.ana, "chat")
	mustExec(t, s.owner, `UPDATE session_people SET open_at = now() WHERE session_id = $1 AND person_id = $2`, id, s.ana)
	s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/chat", map[string]any{"text": "@Ana Nunes look", "mentions": []string{s.ana}})
	if n := s.count(`SELECT count(*) FROM session_mentions WHERE session_id = $1 AND person_id = $2 AND seen_at IS NULL`, id, s.ana); n != 1 {
		t.Errorf("Ana's mention: %d, want 1 unseen", n)
	}
	if n := s.count(`SELECT count(*) FROM events WHERE session_id = $1 AND event_type = 'session.mentioned'`, id); n != 0 {
		t.Errorf("a push was asked for while Ana had it open: %d", n)
	}
	if lines := s.ok(s.ana, "GET", "/internal/sessions", nil)["mentions"].([]any); len(lines) != 0 {
		t.Errorf("waiting on Ana while she has it open: %v", lines)
	}
	mustExec(t, s.owner, `UPDATE session_people SET open_at = NULL WHERE session_id = $1 AND person_id = $2`, id, s.ana)
	if lines := s.ok(s.ana, "GET", "/internal/sessions", nil)["mentions"].([]any); len(lines) != 1 {
		t.Errorf("Ana left without reading it, and it does not wait on her: %v", lines)
	}
}

// In a private session only its people may be mentioned; the owner's
// shareWith adds a person as chat in the same request, and they are then
// mentioned. Anyone else's shareWith is a 403 and nothing is sent.
func TestOrgSessionMentionInAPrivateSessionSharesOnlyFromTheOwner(t *testing.T) {
	s := newSessionWorld(t)
	id := s.session()
	s.join(id, s.joao, "chat")
	s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/chat", map[string]any{"text": "@Ana Nunes plain", "mentions": []string{s.ana}})
	if n, mentions := s.mentionsOn(id, s.marcio, "@Ana Nunes plain"); n != 1 || mentions != "" {
		t.Errorf("a mention of a non-member went out: %d %s", n, mentions)
	}
	if n := s.count(`SELECT count(*) FROM session_mentions WHERE session_id = $1`, id); n != 0 {
		t.Errorf("a non-member was mentioned in a private session")
	}

	if status, out := s.as(s.joao, "POST", "/internal/sessions/"+id+"/chat", map[string]any{"text": "@Ana Nunes come",
		"mentions": []string{s.ana}, "shareWith": []string{s.ana}}); status != 403 {
		t.Errorf("a chat member shared it: %d %v", status, out)
	}
	if n, _ := s.mentionsOn(id, s.joao, "@Ana Nunes come"); n != 0 {
		t.Errorf("the refused message was sent")
	}
	if n := s.count(`SELECT count(*) FROM session_people WHERE session_id = $1 AND person_id = $2`, id, s.ana); n != 0 {
		t.Errorf("the refused share added Ana")
	}

	s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/chat", map[string]any{"text": "@Ana Nunes join us",
		"mentions": []string{s.ana}, "shareWith": []string{s.ana, "per_nobody"}})
	if n := s.count(`SELECT count(*) FROM session_people WHERE session_id = $1 AND person_id = $2 AND role = 'chat'
		AND accepted_at IS NOT NULL`, id, s.ana); n != 1 {
		t.Errorf("Ana was not added as chat")
	}
	if n := s.count(`SELECT count(*) FROM session_people WHERE session_id = $1`, id); n != 3 {
		t.Errorf("people: %d, want 3 (an unknown id is dropped)", n)
	}
	if n, mentions := s.mentionsOn(id, s.marcio, "@Ana Nunes join us"); n != 1 || mentions != `["`+s.ana+`"]` {
		t.Errorf("the shared mention: %d %s", n, mentions)
	}
	if n := s.count(`SELECT count(*) FROM events WHERE session_id = $1 AND event_type = 'session.shared' AND payload->>'forMention' = 'true'`, id); n != 1 {
		t.Errorf("session.shared for the mention: %d, want 1", n)
	}
}
