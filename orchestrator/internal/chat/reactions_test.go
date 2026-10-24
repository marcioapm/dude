package chat_test

import (
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/chat"
	"github.com/marciomartins/dude/orchestrator/internal/delivery"
)

// settle runs one chat-reactions pass.
func (w *world) settle() int {
	w.t.Helper()
	n, err := w.svc.SettleMarks(w.ctx)
	if err != nil {
		w.t.Fatal(err)
	}
	return n
}

// agentSays records an event of the session's agent as the phase syncer
// does (Syncer.event): its latest brainstorm Run's, on the session, from
// the runner.
func (w *world) agentSays(session, eventType string) {
	w.t.Helper()
	w.exec(`INSERT INTO events (id, organization_id, event_type, run_id, session_id, actor_type, actor_id, source, payload)
		SELECT 'evt_' || md5(random()::text), $1, $2, r.id, $3, 'agent', r.id, 'runner', '{"text":"On it."}'
		FROM runs r WHERE r.session_id = $3 AND r.role = 'brainstorm' ORDER BY r.created_at DESC LIMIT 1`, w.org, eventType, session)
}

// childSays starts a read-only investigator of the session's agent, as
// delivery.StartSessionChild does, and records its tool call on the
// session, as the syncer does for a child.
func (w *world) childSays(session, eventType string) {
	w.t.Helper()
	child := "run_child_" + session
	w.exec(`INSERT INTO runs (id, organization_id, session_id, attempt, status, phase, role, parent_run_id, parent_session_id,
			source_kind, child_profile, child_capabilities, creation_key, base_refs)
		SELECT $1, $2, $3, 1, 'running', 'investigate', 'investigator', r.id, $3, 'session', 'investigator', '{}'::jsonb, $1, '{}'::jsonb
		FROM runs r WHERE r.session_id = $3 AND r.role = 'brainstorm' ORDER BY r.created_at DESC LIMIT 1
		ON CONFLICT DO NOTHING`, child, w.org, session)
	w.exec(`INSERT INTO events (id, organization_id, event_type, run_id, session_id, actor_type, actor_id, source, payload)
		VALUES ('evt_' || md5(random()::text), $1, $2, $3, $4, 'agent', $3, 'runner', '{"text":"Reading."}')`, w.org, eventType, child, session)
}

func (w *world) marks(channel, ts string) string {
	w.t.Helper()
	return strings.Join(w.fake.Reactions(channel, ts), ",")
}

// A session's mention keeps its 👀 after the reply, and swaps it for ✅
// once, on the agent's first reply.
func TestChatASessionsMarkFollowsItsAgentsFirstReply(t *testing.T) {
	w := setup(t)
	w.model.queue(answer(map[string]any{"route": "brainstorm", "confidence": 0.9, "reasoning": "a chat", "title": "Exports"}))
	ts := w.mention("Márcia", "C1", "", "why are exports slow?")
	got := w.requests()
	if len(got) != 1 || got[0].ResultKind != "session" || len(w.fake.BotMessages()) != 1 {
		t.Fatalf("requests %+v, %d replies", got, len(w.fake.BotMessages()))
	}
	session := got[0].ResultID
	if m := w.marks("C1", ts); m != "eyes" {
		t.Fatalf("right after routing: %s", m)
	}
	// An event of the session that is not the agent's output settles nothing.
	w.agentSays(session, "agent.thought")
	if n := w.settle(); n != 0 || w.marks("C1", ts) != "eyes" {
		t.Fatalf("settled %d before the agent spoke: %s", n, w.marks("C1", ts))
	}
	w.agentSays(session, "agent.message")
	if n := w.settle(); n != 1 || w.marks("C1", ts) != "white_check_mark" {
		t.Fatalf("settled %d after the reply: %s", n, w.marks("C1", ts))
	}
	if s := w.one(`SELECT mark_settled FROM chat_requests WHERE organization_id = $1`, w.org); s != "done" {
		t.Fatalf("recorded %q", s)
	}
	// Again, and from a restarted process: nothing is put twice.
	adds, removes := w.fake.Calls("reactions.add"), w.fake.Calls("reactions.remove")
	w.agentSays(session, "agent.message")
	restarted := &chat.Service{DB: w.svc.DB, Providers: w.svc.Providers}
	for _, s := range []*chat.Service{w.svc, restarted} {
		if n, err := s.SettleMarks(w.ctx); err != nil || n != 0 {
			t.Fatalf("a second pass settled %d: %v", n, err)
		}
	}
	if w.fake.Calls("reactions.add") != adds || w.fake.Calls("reactions.remove") != removes {
		t.Fatal("a settled mark was put again")
	}
	// A mention into the bound session waits for output after it: the
	// replies before it do not count.
	second := w.mention("Eli", "C1", ts, "and the CSV?")
	if got := w.requests(); len(got) != 2 || got[1].Route != "resume" {
		t.Fatalf("requests %+v", got)
	}
	if n := w.settle(); n != 0 || w.marks("C1", second) != "eyes" {
		t.Fatalf("the resumed session's mark settled %d on earlier output: %s", n, w.marks("C1", second))
	}
	w.agentSays(session, "agent.tool.called")
	if n := w.settle(); n != 1 || w.marks("C1", second) != "white_check_mark" {
		t.Fatalf("settled %d after a tool use: %s", n, w.marks("C1", second))
	}
}

// The session's agent is its brainstorm Run: an investigator it started
// acting is not the agent answering.
func TestChatASessionChildsOutputSettlesNothing(t *testing.T) {
	w := setup(t)
	w.model.queue(answer(map[string]any{"route": "brainstorm", "confidence": 0.9, "reasoning": "a chat", "title": "Exports"}))
	ts := w.mention("Márcia", "C1", "", "why are exports slow?")
	session := w.requests()[0].ResultID
	w.childSays(session, "agent.tool.called")
	w.childSays(session, "agent.message")
	if n := w.settle(); n != 0 || w.marks("C1", ts) != "eyes" {
		t.Fatalf("settled %d on a child's output: %s", n, w.marks("C1", ts))
	}
	w.agentSays(session, "agent.tool.called")
	if n := w.settle(); n != 1 || w.marks("C1", ts) != "white_check_mark" {
		t.Fatalf("settled %d on the agent's tool use: %s", n, w.marks("C1", ts))
	}
}

// A session agent that ends holding the mention's message unread is not
// the end: the syncer hands the message to a successor, whose answer is ✅.
func TestChatAMessageHandedToASuccessorWaitsForItsAnswer(t *testing.T) {
	w := setup(t)
	w.model.queue(answer(map[string]any{"route": "brainstorm", "confidence": 0.9, "reasoning": "a chat", "title": "Exports"}))
	root := w.mention("Márcia", "C1", "", "why are exports slow?")
	session := w.requests()[0].ResultID
	w.agentSays(session, "agent.message")
	if n := w.settle(); n != 1 {
		t.Fatalf("settled %d", n)
	}
	w.exec(`UPDATE directives SET delivered_at = now() WHERE run_id IN (SELECT id FROM runs WHERE session_id = $1)`, session)
	second := w.mention("Eli", "C1", root, "and the CSV?")
	if w.count(`SELECT count(*) FROM directives d JOIN runs r ON r.id = d.run_id WHERE r.session_id = $1 AND d.delivered_at IS NULL`, session) != 1 {
		t.Fatal("the message was not queued for the live agent")
	}
	// The syncer fails the agent and keeps the message for its successor.
	w.exec(`UPDATE runs SET status = 'failed', ended_at = now() WHERE session_id = $1`, session)
	if n := w.settle(); n != 0 || w.marks("C1", second) != "eyes" {
		t.Fatalf("settled %d before the hand-over: %s", n, w.marks("C1", second))
	}
	var ended string
	if err := w.svc.DB.InOrg(w.ctx, w.org, func(tx pgx.Tx) error {
		if err := tx.QueryRow(w.ctx, `SELECT id FROM runs WHERE session_id = $1 AND role = 'brainstorm'`, session).Scan(&ended); err != nil {
			return err
		}
		_, err := delivery.SessionHandOver(w.ctx, tx, delivery.RunRef{Org: w.org, SessionID: session, RunID: ended})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if n := w.settle(); n != 0 || w.marks("C1", second) != "eyes" {
		t.Fatalf("settled %d while the successor starts: %s", n, w.marks("C1", second))
	}
	w.agentSays(session, "agent.message")
	if n := w.settle(); n != 1 || w.marks("C1", second) != "white_check_mark" {
		t.Fatalf("settled %d after the successor answered: %s", n, w.marks("C1", second))
	}
}

// A session request whose reply never went out is failed for good: its ❌
// is put at once, and the agent's later output does not turn it ✅.
func TestChatASessionReplyNotPostedKeepsItsFailedMark(t *testing.T) {
	brainstorm := answer(map[string]any{"route": "brainstorm", "confidence": 0.9, "reasoning": "a chat", "title": "Exports"})
	after := func(t *testing.T, w *world, ts string) {
		t.Helper()
		got := w.requests()
		if len(got) != 1 || got[0].Route != "failed" || got[0].ResultKind != "session" {
			t.Fatalf("requests %+v", got)
		}
		w.agentSays(got[0].ResultID, "agent.message")
		if n := w.settle(); n != 0 || w.marks("C1", ts) != "x" {
			t.Fatalf("settled %d after the agent spoke: %s", n, w.marks("C1", ts))
		}
		if s := w.one(`SELECT mark_settled FROM chat_requests WHERE organization_id = $1`, w.org); s != "failed" {
			t.Fatalf("recorded %q", s)
		}
	}
	t.Run("refused", func(t *testing.T) {
		w := setup(t)
		w.model.queue(brainstorm)
		w.fake.FailNext("chat.postMessage", "msg_too_long")
		ts, _ := w.receive("Márcia", "C1", "", "why are exports slow?")
		w.once()
		if m := w.marks("C1", ts); m != "x" {
			t.Fatalf("right after the refusal: %s", m)
		}
		after(t, w, ts)
	})
	t.Run("given up", func(t *testing.T) {
		w := setup(t)
		w.model.queue(brainstorm)
		w.fake.FailTimes("chat.postMessage", "internal_error", 100)
		ts, ev := w.receive("Márcia", "C1", "", "why are exports slow?")
		for range chat.MaxAttempts + 1 {
			w.due()
			w.once()
		}
		if e := w.event(ev); !e.Processed || w.marks("C1", ts) != "x" {
			t.Fatalf("not given up: %+v, %s", e, w.marks("C1", ts))
		}
		after(t, w, ts)
	})
}

// A session picked from a clarifying question gets its 👀 back (the
// question cleared it), and ✅ from there on its agent's answer.
func TestChatASessionPickedByClickIsMarkedAgain(t *testing.T) {
	w := setup(t)
	w.model.queue(answer(map[string]any{"route": "task", "confidence": 0.6, "reasoning": "might be a task", "project": "BIL",
		"title": "Fix the export", "goal": "Fix it."}))
	root := w.mention("Eli", "C1", "", "the export is broken")
	if m := w.marks("C1", root); m != "" {
		t.Fatalf("with the question: %q", m)
	}
	q := lastBot(t, w.fake)
	w.click("Eli", "C1", q.TS, root, buttonValue(t, q, 1))
	got := w.requests()
	if len(got) != 2 || got[1].Route != "brainstorm" || got[1].ResultKind != "session" {
		t.Fatalf("requests %+v", got)
	}
	if m := w.marks("C1", root); m != "eyes" {
		t.Fatalf("after the click: %q", m)
	}
	w.agentSays(got[1].ResultID, "agent.message")
	if n := w.settle(); n != 1 || w.marks("C1", root) != "white_check_mark" {
		t.Fatalf("settled %d after the reply: %s", n, w.marks("C1", root))
	}
}

// A session whose agent ends before saying or doing anything gets ❌.
func TestChatASessionsAgentEndingSilentGetsTheFailedMark(t *testing.T) {
	w := setup(t)
	w.model.queue(answer(map[string]any{"route": "brainstorm", "confidence": 0.9, "reasoning": "a chat", "title": "Exports"}))
	ts := w.mention("Márcia", "G1", "", "let's think about exports")
	session := w.requests()[0].ResultID
	if n := w.settle(); n != 0 || w.marks("G1", ts) != "eyes" {
		t.Fatalf("settled %d while the agent starts: %s", n, w.marks("G1", ts))
	}
	w.exec(`UPDATE runs SET status = 'failed' WHERE session_id = $1`, session)
	if n := w.settle(); n != 1 || w.marks("G1", ts) != "x" {
		t.Fatalf("settled %d after the agent failed: %s", n, w.marks("G1", ts))
	}
	if got := w.requests(); got[0].Route != "brainstorm" || got[0].ResultKind != "session" {
		t.Fatalf("the mark changed the request: %+v", got)
	}
}

// A mark waits 24 hours at most: past that the 👀 is left, and nothing
// else is invented.
func TestChatASessionsMarkStopsWaitingAfterADay(t *testing.T) {
	w := setup(t)
	w.model.queue(answer(map[string]any{"route": "brainstorm", "confidence": 0.9, "reasoning": "a chat", "title": "Exports"}))
	ts := w.mention("Márcia", "C1", "", "why are exports slow?")
	w.exec(`UPDATE chat_requests SET created_at = now() - interval '25 hours' WHERE organization_id = $1`, w.org)
	adds := w.fake.Calls("reactions.add")
	if n := w.settle(); n != 1 || w.marks("C1", ts) != "eyes" || w.fake.Calls("reactions.add") != adds {
		t.Fatalf("settled %d: %s", n, w.marks("C1", ts))
	}
	w.agentSays(w.requests()[0].ResultID, "agent.message")
	if n := w.settle(); n != 0 || w.marks("C1", ts) != "eyes" {
		t.Fatalf("an expired mark settled %d: %s", n, w.marks("C1", ts))
	}
	if s := w.one(`SELECT mark_settled FROM chat_requests WHERE organization_id = $1`, w.org); s != "expired" {
		t.Fatalf("recorded %q", s)
	}
}

// A failed mark is noted, never failed: the request stays what it made.
func TestChatAFailedSessionMarkIsOnlyNoted(t *testing.T) {
	w := setup(t)
	w.model.queue(answer(map[string]any{"route": "brainstorm", "confidence": 0.9, "reasoning": "a chat", "title": "Exports"}))
	w.mention("Márcia", "C1", "", "why are exports slow?")
	session := w.requests()[0].ResultID
	w.agentSays(session, "agent.message")
	w.fake.FailTimes("reactions.add", "internal_error", 100)
	if n := w.settle(); n != 1 {
		t.Fatalf("settled %d", n)
	}
	if got := w.requests(); got[0].Route != "brainstorm" || w.count(`SELECT failed_today FROM chat_installations WHERE organization_id = $1`, w.org) == 0 {
		t.Fatalf("requests %+v", got)
	}
	if n := w.settle(); n != 0 {
		t.Fatalf("a failed mark was tried again: %d", n)
	}
}

// Nothing runs for a task, an epic or a bound thread's link: ✅ with the
// reply, and the reactions pass has nothing to wait on.
func TestChatATaskOrEpicIsMarkedDoneWithItsReply(t *testing.T) {
	w := setup(t)
	w.model.queue(answer(map[string]any{"route": "task", "confidence": 0.9, "project": "BIL", "title": "Stream it", "goal": "g"}))
	root := w.mention("Eli", "C1", "", "file: stream it")
	if m := w.marks("C1", root); m != "white_check_mark" {
		t.Fatalf("task: %s", m)
	}
	link := w.mention("Márcia", "C1", root, "status?")
	if m := w.marks("C1", link); m != "white_check_mark" {
		t.Fatalf("task link: %s", m)
	}
	w.model.queue(answer(map[string]any{"route": "epic", "confidence": 0.9, "project": "BIL", "reasoning": "a plan",
		"new_epic": map[string]any{"title": "Faster exports", "description": "d",
			"tasks": []any{map[string]any{"ref": "a", "title": "Measure", "goal": "g", "blocked_by": []string{}}}}}))
	epic := w.mention("Eli", "C1", "", "open an epic for faster exports")
	if got := w.requests(); got[2].Route != "epic" || w.marks("C1", epic) != "white_check_mark" {
		t.Fatalf("epic: %+v, %s", got[2], w.marks("C1", epic))
	}
	if n := w.settle(); n != 0 {
		t.Fatalf("settled %d", n)
	}
}

// A mention kept for a Connect loses its 👀 with the Connect prompt: routed
// once the person links, it is marked 👀 again, once, and ✅ when the
// session's agent answers.
func TestChatAKeptMentionIsMarkedAgainOnce(t *testing.T) {
	w := setup(t)
	ts := w.mention("Gus", "C1", "", "let's think about exports")
	if m := w.marks("C1", ts); m != "" {
		t.Fatalf("after the Connect prompt: %q", m)
	}
	prompt := promptState(t, w)
	eyes := w.fake.Calls("reactions.add")
	w.fake.AuthorizeAs("", "U3")
	start, err := w.svc.BeginConnect(w.ctx, w.org, chat.Begin{Provider: "slack", Person: w.people["Gus"][0], BrowserHash: "g",
		Redirect: "https://dude.example.com/v1/chat/slack/connect/callback", Prompt: prompt})
	if err != nil {
		t.Fatal(err)
	}
	back := follow(t, start)
	w.model.queue(answer(map[string]any{"route": "brainstorm", "confidence": 0.9, "reasoning": "a chat", "title": "Exports"}))
	if l, err := w.svc.CompleteConnect(w.ctx, chat.Callback{Provider: "slack", State: back.Get("state"), Code: back.Get("code"),
		BrowserHash: "g"}); err != nil || l.Outcome != "connected" {
		t.Fatalf("connect %+v, %v", l, err)
	}
	w.sweep()
	got := w.requests()
	if len(got) != 2 || got[1].ResultKind != "session" {
		t.Fatalf("requests %+v", got)
	}
	if n := w.fake.Calls("reactions.add"); n != eyes+1 || w.marks("C1", ts) != "eyes" {
		t.Fatalf("%d marks put on the kept mention: %q", n-eyes, w.marks("C1", ts))
	}
	w.agentSays(got[1].ResultID, "agent.message")
	if n := w.settle(); n != 1 || w.marks("C1", ts) != "white_check_mark" {
		t.Fatalf("settled %d after the reply: %q", n, w.marks("C1", ts))
	}
}
