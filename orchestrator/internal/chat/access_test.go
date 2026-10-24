package chat_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/fakeslack"
)

// dm sends a direct message from name to dude in D1, as a new thread.
func (w *world) dm(name, text string) {
	w.t.Helper()
	user := w.people[name][1]
	ts := w.fake.Post("D1", "", user, text)
	w.seq++
	w.deliver(fakeslack.DirectMessage("T1", fmt.Sprintf("Ev%d", w.seq), "D1", ts, user, text))
}

func (w *world) last() req {
	w.t.Helper()
	got := w.requests()
	if len(got) == 0 {
		w.t.Fatal("no requests")
	}
	return got[len(got)-1]
}

func resume(session string) string {
	return answer(map[string]any{"route": "resume", "confidence": 0.95, "reasoning": "continues it", "session": session})
}

// A session is resumed only from a conversation all of whose connected
// members can already chat in it; a 1:1 DM's session from that DM alone.
// Anything else is a fresh session, made as the conversation makes one.
func TestChatResumesOnlyASessionTheWholeConversationCanChatIn(t *testing.T) {
	w := setup(t)
	marcia := w.people["Márcia"][0]

	w.model.queue(answer(map[string]any{"route": "brainstorm", "confidence": 0.9, "title": "My ideas"}))
	w.dm("Márcia", "my private ideas")
	private := w.last().ResultID

	w.model.queue(answer(map[string]any{"route": "brainstorm", "confidence": 0.9, "title": "Design"}))
	w.mention("Márcia", "G1", "", "design talk")
	design := w.last().ResultID
	w.model.queue(answer(map[string]any{"route": "brainstorm", "confidence": 0.9, "title": "Pair"}))
	w.mention("Márcia", "M1", "", "pair talk")
	pair := w.last().ResultID
	// A private channel's session made while Márcia was its only member: Eli cannot chat in it.
	w.fake.SetMembers("G1", []string{"U1", "UBT1"})
	w.model.queue(answer(map[string]any{"route": "brainstorm", "confidence": 0.9, "title": "Alone"}))
	w.mention("Márcia", "G1", "", "alone talk")
	alone := w.last().ResultID
	w.fake.SetMembers("G1", []string{"U1", "U2", "U3", "U9", "UBT1"})
	cases := []struct {
		name, conv, session string
		resumed             bool
	}{
		// The DM's session, from the DM itself.
		{"dm into its own dm session", "D1", private, true},
		// From a DM, any session the asker chats in: nobody else is told.
		{"dm into a private channel's session", "D1", design, true},
		// The DM's session is never reachable from another conversation.
		{"private channel into a dm session", "G1", private, false},
		{"group into a dm session", "M1", private, false},
		{"public channel into a dm session", "C1", private, false},
		// A private channel's session, from a group whose connected members (Eli) all chat in it.
		{"group into a session every member chats in", "M1", design, true},
		// A group's session, from a private channel whose connected members (Márcia, Eli) both chat in it.
		{"private channel into a session every member chats in", "G1", pair, true},
		// A public channel shares with the organisation: only an organisation session.
		{"public channel into a private channel's session", "C1", design, false},
		// A connected member of the group (Eli) cannot chat in it: not resumable from there.
		{"group into a session not every member chats in", "M1", alone, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			before := w.count(`SELECT count(*) FROM sessions WHERE organization_id = $1`, w.org)
			w.model.queue(resume(c.session))
			if c.conv == "D1" {
				w.dm("Márcia", "continue that")
			} else {
				w.mention("Márcia", c.conv, "", "continue that")
			}
			got := w.last()
			after := w.count(`SELECT count(*) FROM sessions WHERE organization_id = $1`, w.org)
			if c.resumed {
				if got.Route != "resume" || got.ResultID != c.session || after != before {
					t.Fatalf("not resumed: %+v", got)
				}
				return
			}
			if got.Route != "brainstorm" || got.AskedRoute != "resume" || got.ResultID == c.session || after != before+1 ||
				!strings.Contains(got.Reasoning, "is not open to everyone") {
				t.Fatalf("resumed what the conversation cannot all chat in: %+v", got)
			}
		})
	}
	// Nobody but Márcia was ever given her DM session.
	if n := w.count(`SELECT count(*) FROM session_people WHERE session_id = $1 AND person_id <> $2`, private, marcia); n != 0 {
		t.Fatalf("%d others in the DM session", n)
	}
	// Once Gus connects, the private channel G1 has a connected member who
	// is not in the group's session: it is no longer resumable from there.
	gusAccount := w.one(`SELECT account_id FROM people WHERE id = $1`, w.people["Gus"][0])
	w.exec(`INSERT INTO chat_identities (organization_id, provider, external_workspace_id, external_user_id, account_id, person_id)
		VALUES ($1, 'slack', 'T1', 'U3', $2, $3)`, w.org, gusAccount, w.people["Gus"][0])
	w.model.queue(resume(pair))
	w.mention("Márcia", "G1", "", "continue that")
	if got := w.last(); got.Route != "brainstorm" || got.AskedRoute != "resume" || got.ResultID == pair {
		t.Fatalf("private channel resumed a session Gus cannot chat in: %+v", got)
	}
	if r := w.one(`SELECT session_role($1, $2)`, pair, w.people["Gus"][0]); r != "" {
		t.Fatalf("Gus got %q in the group's session", r)
	}
	// The model is offered only what the conversation may resume.
	w.model.queue(answer(map[string]any{"route": "brainstorm", "confidence": 0.9, "title": "x"}))
	w.mention("Márcia", "C1", "", "anything")
	if sent := w.model.last(); strings.Contains(sent, private) || strings.Contains(sent, design) {
		t.Fatalf("a public channel was offered private sessions: %s", sent)
	}
}

// The routing context offers exactly the recent chat-born sessions
// session_role lets the person own or chat in: an organisation session,
// one they were given chat in, an admin-owned one to an admin, their own;
// never a pending invitation, a read-only private seat, an old session or
// one not born in chat. The context's prefilter is a superset that only
// narrows what session_role is asked about; this proves it is never
// narrower, not that it is as narrow as it could be.
func TestChatRoutingContextOffersWhatSessionRoleAllows(t *testing.T) {
	w := setup(t)
	marcia, eli, gus, ana := w.people["Márcia"][0], w.people["Eli"][0], w.people["Gus"][0], w.people["Ana"][0]
	origin := `{"provider": "slack", "conversationId": "C1", "conversationKind": "public"}`
	sessions := []struct {
		name, access, owner string
		admins, chatBorn    bool
		age                 string
		seats               [][3]string // person, role, accepted ('y'/'n')
	}{
		{"org", "organisation", gus, false, true, "1 day", nil},
		{"chat-seat", "private", gus, false, true, "2 days", [][3]string{{eli, "chat", "y"}}},
		{"admin-owned", "private", ana, true, true, "3 days", nil},
		{"pending", "private", gus, false, true, "4 days", [][3]string{{eli, "chat", "n"}}},
		{"read-seat", "private", gus, false, true, "5 days", [][3]string{{eli, "read", "y"}}},
		{"org-read-seat", "organisation", gus, false, true, "6 days", [][3]string{{eli, "read", "y"}}},
		{"eli-own", "private", eli, false, true, "7 days", nil},
		{"old", "organisation", gus, false, true, "20 days", nil},
		{"not-chat-born", "organisation", gus, false, false, "1 day", nil},
	}
	ids := map[string]string{}
	for _, s := range sessions {
		id := "ssn_" + strings.ReplaceAll(s.name, "-", "_") + "_" + w.org
		ids[s.name] = id
		var o any
		if s.chatBorn {
			o = origin
		}
		w.exec(`INSERT INTO sessions (id, organization_id, title, origin, access, owned_by_admins, created_at)
			VALUES ($1, $2, $3, $4::jsonb, $5, $6, now() - $7::interval)`, id, w.org, s.name, o, s.access, s.admins, s.age)
		w.exec(`INSERT INTO session_people (session_id, person_id, organization_id, role, accepted_at) VALUES ($1, $2, $3, 'owner', now())`,
			id, s.owner, w.org)
		for _, seat := range s.seats {
			w.exec(`INSERT INTO session_people (session_id, person_id, organization_id, role, accepted_at)
				VALUES ($1, $2, $3, $4, CASE WHEN $5 = 'y' THEN now() END)`, id, seat[0], w.org, seat[1], seat[2])
		}
	}
	want := map[string][]string{
		eli:    {"org", "chat-seat", "org-read-seat", "eli-own"},
		marcia: {"org", "admin-owned", "org-read-seat"},
		gus:    {"org", "chat-seat", "pending", "read-seat", "org-read-seat"},
	}
	for person, names := range want {
		var expected []string
		for _, n := range names {
			expected = append(expected, ids[n])
		}
		// session_role alone, over every recent chat-born session.
		rows, err := w.owner.Query(w.ctx, `SELECT id FROM sessions WHERE organization_id = $1 AND origin IS NOT NULL
			AND created_at > now() - interval '14 days' AND session_role(id, $2) IN ('owner', 'chat') ORDER BY created_at DESC`, w.org, person)
		if err != nil {
			t.Fatal(err)
		}
		oracle, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			t.Fatal(err)
		}
		got, err := w.svc.RoutingSessions(w.ctx, w.org, person)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Join(oracle, ",") != strings.Join(expected, ",") || strings.Join(got, ",") != strings.Join(oracle, ",") {
			t.Fatalf("%s: offered %v, session_role allows %v, want %v", person, got, oracle, expected)
		}
	}
}

// A thread a resume bound in another conversation sends into the session
// only for who can chat there: its members are not added to it.
func TestChatAThreadBoundByAResumeAddsNobody(t *testing.T) {
	w := setup(t)
	w.model.queue(answer(map[string]any{"route": "brainstorm", "confidence": 0.9, "title": "Design"}))
	w.mention("Márcia", "G1", "", "design talk")
	design := w.last().ResultID
	// From the group M1 (Márcia, Eli, both in design): resumed, and the thread bound.
	w.model.queue(resume(design))
	root := w.mention("Márcia", "M1", "", "continue that")
	if got := w.last(); got.Route != "resume" || got.ResultID != design {
		t.Fatalf("not resumed: %+v", got)
	}
	// Gus connects and joins M1, then mentions dude in the bound thread: he is
	// not added to a session G1 made, and is refused.
	gusAccount := w.one(`SELECT account_id FROM people WHERE id = $1`, w.people["Gus"][0])
	w.exec(`INSERT INTO chat_identities (organization_id, provider, external_workspace_id, external_user_id, account_id, person_id)
		VALUES ($1, 'slack', 'T1', 'U3', $2, $3)`, w.org, gusAccount, w.people["Gus"][0])
	w.fake.SetMembers("M1", []string{"U1", "U2", "U3", "UBT1"})
	w.mention("Gus", "M1", root, "let me in")
	if r := w.one(`SELECT session_role($1, $2)`, design, w.people["Gus"][0]); r != "" {
		t.Fatalf("Gus got %q in G1's session through M1", r)
	}
	if got := w.last(); got.Route != "not_member" {
		t.Fatalf("request %+v", got)
	}
}

// A task filed from a thread keeps the thread, as it was filed, on its
// origin: the task page quotes it.
func TestChatATaskKeepsTheThreadItWasFiledFrom(t *testing.T) {
	w := setup(t)
	root := w.fake.Post("C1", "", "U9", "the export times out")
	w.model.queue(answer(map[string]any{"route": "task", "confidence": 0.9, "project": "ABS", "title": "Fix it", "goal": "g"}))
	w.mention("Eli", "C1", root, "file it")
	got := w.last()
	if o := w.one(`SELECT origin->'thread'->0->>'text' FROM tasks WHERE id = $1`, got.ResultID); o != "the export times out" {
		t.Fatalf("thread on the origin: %q", o)
	}
}

// A confident clarify is capped like an unsure answer: on the third
// question in a thread, a brainstorm.
func TestChatCapsConfidentClarifyingQuestions(t *testing.T) {
	w := setup(t)
	clarify := answer(map[string]any{"route": "clarify", "confidence": 0.9, "reasoning": "two ways", "project": "ABS",
		"question": map[string]any{"text": "Task or session?", "choices": []map[string]any{
			{"id": "a", "label": "A task", "route": "task", "project": "ABS"}, {"id": "b", "label": "A session", "route": "brainstorm"}}}})
	w.model.queue(clarify, clarify, clarify, clarify)
	root := w.mention("Eli", "C1", "", "hmm")
	w.mention("Eli", "C1", root, "hmm again")
	w.mention("Eli", "C1", root, "and again")
	got := w.requests()
	if len(got) != 3 || got[0].Route != "clarify" || got[1].Route != "clarify" || got[2].Route != "brainstorm" ||
		got[2].AskedRoute != "clarify" || got[2].ResultKind != "session" {
		t.Fatalf("requests %+v", got)
	}
}
