package chat_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/dbtest"
	"github.com/marciomartins/dude/orchestrator/internal/fakeslack"
)

// resumeSetup starts the session "Nightly workflow failure" from a
// top-level mention in #support, as Márcia, and returns its id.
func resumeSetup(t *testing.T) (*world, string) {
	t.Helper()
	w := setup(t)
	w.model.queue(answer(map[string]any{"route": "brainstorm", "confidence": 0.9, "reasoning": "a failure to look into",
		"project": "BIL", "title": "Nightly workflow failure"}))
	w.mention("Márcia", "C1", "", "why did the nightly fail?")
	got := w.requests()
	if len(got) != 1 || got[0].ResultKind != "session" {
		t.Fatalf("requests %+v", got)
	}
	return w, got[0].ResultID
}

// askResume mentions dude anew with the model 72% sure the
// message continues session, and returns the question posted and its
// buttons' values.
func askResume(t *testing.T, w *world, session, conversation string) (fakeslack.Message, []string) {
	t.Helper()
	w.model.queue(answer(map[string]any{"route": "resume", "confidence": 0.72, "reasoning": "a similar session exists",
		"session": session, "title": "Fix the nightly"}))
	w.mention("Márcia", conversation, "", "fix the nightly workflow failure")
	q := lastBot(t, w.fake)
	var blocks []map[string]any
	if err := json.Unmarshal(q.Blocks, &blocks); err != nil || len(blocks) < 2 {
		t.Fatalf("question %+v", q)
	}
	var values []string
	elements, ok := blocks[1]["elements"].([]any)
	if !ok || len(elements) != 2 {
		t.Fatalf("expected two choice buttons: %+v", q)
	}
	for _, e := range elements {
		button, ok := e.(map[string]any)
		if !ok {
			t.Fatalf("expected a choice button, got %+v", e)
		}
		value, ok := button["value"].(string)
		if !ok || value == "" {
			t.Fatalf("choice button has no value: %+v", button)
		}
		values = append(values, value)
	}
	return q, values
}

func (w *world) sessions() int {
	return w.count(`SELECT count(*) FROM sessions WHERE organization_id = $1`, w.org)
}

func (w *world) messagesInto(session, text string) int {
	return w.count(`SELECT (SELECT count(*) FROM directives d JOIN runs r ON r.id = d.run_id WHERE r.session_id = $1 AND d.text LIKE '%' || $2 || '%')
		+ (SELECT count(*) FROM runs WHERE session_id = $1 AND prompt LIKE '%' || $2 || '%')`, session, text)
}

// An unsure resume asks; picking Continue sends the message into the
// session, and nothing new is started.
func TestChatAnUnsureResumeAsksAndContinueResumesTheSession(t *testing.T) {
	w, session := resumeSetup(t)
	q, values := askResume(t, w, session, "C1")
	if q.Text != "Continue “Nightly workflow failure”, or start a new session?" || !strings.Contains(string(q.Blocks), "Continue Nightly workflow failure") ||
		!strings.Contains(string(q.Blocks), "A new session") || len(values) != 2 {
		t.Fatalf("question %+v", q)
	}
	// The button carries only the request and the index, never the session.
	for _, v := range values {
		if strings.Contains(v, session) {
			t.Fatalf("a button's value %q names the session", v)
		}
	}
	questionID, _, ok := strings.Cut(values[0], ":")
	if !ok || questionID == "" {
		t.Fatalf("invalid Continue value %q", values[0])
	}
	requestByID := func(id string) req {
		t.Helper()
		var r req
		if err := w.owner.QueryRow(w.ctx, `SELECT route, COALESCE(asked_route, ''), COALESCE(result_kind, ''),
			COALESCE(result_id, ''), reasoning FROM chat_requests WHERE organization_id = $1 AND id = $2`, w.org, id).
			Scan(&r.Route, &r.AskedRoute, &r.ResultKind, &r.ResultID, &r.Reasoning); err != nil {
			t.Fatalf("request %q: %v; all requests %+v", id, err, w.requests())
		}
		return r
	}
	got := w.requests()
	if len(got) != 2 {
		t.Fatalf("want exactly 2 requests before Continue, got %d: %+v", len(got), got)
	}
	ask := requestByID(questionID)
	if ask.Route != "clarify" || ask.AskedRoute != "resume" || !strings.Contains(ask.Reasoning, "under the 75% bar") {
		t.Fatalf("original ask %+v; all requests %+v", ask, got)
	}
	before := w.sessions()
	w.click("Márcia", "C1", q.TS, q.ThreadTS, values[0])
	got = w.requests()
	if len(got) != 3 {
		t.Fatalf("want exactly 3 requests after Continue, got %d: %+v", len(got), got)
	}
	continuedID := w.one(`SELECT id FROM chat_requests WHERE organization_id = $1 AND event_id =
		(SELECT answered_by_event FROM chat_requests WHERE organization_id = $1 AND id = $2)`, w.org, questionID)
	continued := requestByID(continuedID)
	if continued.Route != "resume" || continued.ResultKind != "session" || continued.ResultID != session {
		t.Fatalf("Continue request %+v; all requests %+v", continued, got)
	}
	if w.sessions() != before {
		t.Fatal("Continue started a new session")
	}
	if w.messagesInto(session, "fix the nightly workflow failure") != 1 {
		t.Fatal("the message did not go into the session")
	}
	if n := w.count(`SELECT count(*) FROM events WHERE session_id = $1 AND event_type = 'chat.message'
		AND payload->>'text' = $2`, session, "fix the nightly workflow failure"); n != 1 {
		t.Fatalf("want exactly one event with the original message, got %d", n)
	}
	if u := w.fake.Updates(); len(u) != 1 || u[0].Text != "Márcia chose Continue Nightly workflow failure" {
		t.Fatalf("updates %+v", u)
	}
}

// Picking A new session starts one, and sends nothing into the old one.
func TestChatAnUnsureResumeAsksAndANewSessionStartsOne(t *testing.T) {
	w, session := resumeSetup(t)
	q, values := askResume(t, w, session, "C1")
	before := w.sessions()
	w.click("Márcia", "C1", q.TS, q.ThreadTS, values[1])
	got := w.requests()
	if len(got) != 3 || got[2].Route != "brainstorm" || got[2].ResultKind != "session" || got[2].ResultID == session {
		t.Fatalf("requests %+v", got)
	}
	if w.sessions() != before+1 || w.messagesInto(session, "fix the nightly workflow failure") != 0 {
		t.Fatal("A new session did not start one, or sent into the old one")
	}
	if title := w.one(`SELECT title FROM sessions WHERE id = $1`, got[2].ResultID); title != "Fix the nightly" {
		t.Fatalf("title %q", title)
	}
}

// A stored Continue choice whose session the asker cannot resume from
// here (one they cannot reach any more, a private one of another
// conversation, another organisation's) never sends into it: the pick is
// checked again and falls back to a new session, saying why.
func TestChatAForgedOrStaleResumeChoiceNeverResumesThatSession(t *testing.T) {
	w, session := resumeSetup(t)
	// A private session Márcia owns, made in #design-private: not open to
	// everyone in #support.
	w.model.queue(answer(map[string]any{"route": "brainstorm", "confidence": 0.9, "reasoning": "private", "title": "Payroll"}))
	w.mention("Márcia", "G1", "", "payroll talk")
	private := w.requests()[1].ResultID
	// Another organisation's session.
	other := dbtest.Org(t, w.owner)
	foreign := "ses_foreign_" + other
	w.exec(`INSERT INTO sessions (id, organization_id, title, access, origin) VALUES ($1, $2, 'Elsewhere', 'organisation',
		'{"provider":"slack","conversationId":"C1","conversationKind":"public"}')`, foreign, other)
	// Eli's private session in a DM: Márcia is not in it.
	elis := "ses_eli_" + w.org
	w.exec(`INSERT INTO sessions (id, organization_id, title, access, created_by, origin) VALUES ($1, $2, 'Eli alone', 'private', $3,
		'{"provider":"slack","conversationId":"D2","conversationKind":"dm"}')`, elis, w.org, w.people["Eli"][0])
	w.exec(`INSERT INTO session_people (session_id, person_id, organization_id, role, invited_by, accepted_at) VALUES ($1, $2, $3, 'owner', $2, now())`,
		elis, w.people["Eli"][0], w.org)

	for _, target := range []string{private, foreign, elis, "ses_missing"} {
		q, values := askResume(t, w, session, "C1")
		// The stored question is rewritten to name target, as a forged or stale row would.
		w.exec(`UPDATE chat_requests SET question = jsonb_set(question, '{question,choices,0,session}', to_jsonb($2::text))
			WHERE organization_id = $1 AND route = 'clarify' AND answered_at IS NULL`, w.org, target)
		before := w.sessions()
		beforeEvents := w.count(`SELECT count(*) FROM events WHERE session_id = $1 AND event_type = 'chat.message'`, target)
		w.click("Márcia", "C1", q.TS, q.ThreadTS, values[0])
		got := w.requests()
		last := got[len(got)-1]
		if last.Route != "brainstorm" || last.AskedRoute != "resume" || last.ResultKind != "session" || last.ResultID == target ||
			!strings.Contains(last.Reasoning, "chosen in the clarifying question") || !strings.Contains(last.Reasoning, target) {
			t.Fatalf("%s: request %+v", target, last)
		}
		if w.sessions() != before+1 {
			t.Fatalf("%s: no new session", target)
		}
		if after := w.count(`SELECT count(*) FROM events WHERE session_id = $1 AND event_type = 'chat.message'`, target); after != beforeEvents {
			t.Fatalf("%s: chat.message events changed from %d to %d", target, beforeEvents, after)
		}
		if w.count(`SELECT count(*) FROM directives d JOIN runs r ON r.id = d.run_id WHERE r.session_id = $1`, target) != 0 ||
			w.count(`SELECT count(*) FROM session_people WHERE session_id = $1 AND person_id = $2 AND role <> 'owner'`, target, w.people["Márcia"][0]) != 0 {
			t.Fatalf("%s: the message went into it, or Márcia joined it", target)
		}
	}
	// The private session was never written to: only its own opening.
	if w.messagesInto(private, "fix the nightly") != 0 {
		t.Fatal("the private session got the message")
	}
}

func TestChatAResumeChoiceWhoseAccessWasRevokedStartsANewSession(t *testing.T) {
	w := setup(t)
	w.model.queue(answer(map[string]any{"route": "brainstorm", "confidence": 0.9, "reasoning": "a failure to look into",
		"title": "Nightly workflow failure"}))
	w.mention("Márcia", "D1", "", "why did the nightly fail?")
	got := w.requests()
	if len(got) != 1 || got[0].ResultKind != "session" {
		t.Fatalf("requests %+v", got)
	}
	session := got[0].ResultID
	if access := w.one(`SELECT access FROM sessions WHERE id = $1`, session); access != "private" {
		t.Fatalf("session access %q, want private", access)
	}
	q, values := askResume(t, w, session, "D1")
	got = w.requests()
	if len(got) != 2 || got[1].Route != "clarify" || got[1].AskedRoute != "resume" {
		t.Fatalf("requests %+v", got)
	}
	// Transfer the existing owner row to retain exactly one owner while revoking access.
	w.exec(`UPDATE session_people SET person_id = $2 WHERE session_id = $1 AND person_id = $3 AND role = 'owner'`,
		session, w.people["Eli"][0], w.people["Márcia"][0])
	if role := w.one(`SELECT session_role($1, $2)`, session, w.people["Márcia"][0]); role != "" {
		t.Fatalf("asker's role after transfer %q, want no access", role)
	}
	queries := map[string]string{
		"chat.message events": `SELECT count(*) FROM events WHERE session_id = $1 AND event_type = 'chat.message'`,
		"prompts":             `SELECT count(*) FROM runs WHERE session_id = $1 AND prompt IS NOT NULL`,
		"directives":          `SELECT count(*) FROM directives d JOIN runs r ON r.id = d.run_id WHERE r.session_id = $1`,
		"memberships":         `SELECT count(*) FROM session_people WHERE session_id = $1`,
	}
	before := map[string]int{}
	for name, query := range queries {
		before[name] = w.count(query, session)
	}
	beforeSessions := w.sessions()
	w.click("Márcia", "D1", q.TS, q.ThreadTS, values[0])
	got = w.requests()
	if len(got) != 3 || got[2].Route != "brainstorm" || got[2].AskedRoute != "resume" || got[2].ResultKind != "session" ||
		got[2].ResultID == session || !strings.Contains(got[2].Reasoning, "chosen in the clarifying question") ||
		!strings.Contains(got[2].Reasoning, session) || !strings.Contains(got[2].Reasoning, "not one the person can chat in") {
		t.Errorf("requests %+v", got)
	}
	if after := w.sessions(); after != beforeSessions+1 {
		t.Errorf("sessions changed from %d to %d, want exactly one new session", beforeSessions, after)
	}
	for name, query := range queries {
		if after := w.count(query, session); after != before[name] {
			t.Errorf("original session's %s changed from %d to %d", name, before[name], after)
		}
	}
	if owner := w.one(`SELECT person_id FROM session_people WHERE session_id = $1 AND role = 'owner'`, session); owner != w.people["Eli"][0] {
		t.Errorf("original session's owner %q, want Eli", owner)
	}
	if n := w.count(`SELECT count(*) FROM session_people WHERE session_id = $1 AND person_id = $2`, session, w.people["Márcia"][0]); n != 0 {
		t.Errorf("asker regained %d memberships in original session", n)
	}
}
