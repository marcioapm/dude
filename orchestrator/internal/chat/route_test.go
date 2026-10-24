package chat_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/chat"
	"github.com/marciomartins/dude/orchestrator/internal/chat/slack"
	"github.com/marciomartins/dude/orchestrator/internal/db"
	"github.com/marciomartins/dude/orchestrator/internal/dbtest"
	"github.com/marciomartins/dude/orchestrator/internal/fakeslack"
	"github.com/marciomartins/dude/orchestrator/internal/judge"
	"github.com/marciomartins/dude/orchestrator/internal/llm"
)

const signingSecret = "test-signing-secret"

// model is a stand-in LLM proxy: it answers each call with the next queued
// text (no answer, a closed connection, once the queue is empty), and keeps
// what it was sent.
type model struct {
	mu      sync.Mutex
	answers []string
	sent    []map[string]any
}

func (m *model) queue(answers ...string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.answers = append(m.answers, answers...)
}

func (m *model) calls() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.sent)
}

func (m *model) last() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	raw, _ := json.Marshal(m.sent[len(m.sent)-1])
	return string(raw)
}

func (m *model) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	var body map[string]any
	_ = json.Unmarshal(raw, &body)
	m.mu.Lock()
	m.sent = append(m.sent, body)
	var answer string
	ok := len(m.answers) > 0
	if ok {
		answer, m.answers = m.answers[0], m.answers[1:]
	}
	m.mu.Unlock()
	if !ok {
		w.WriteHeader(http.StatusBadGateway)
		return
	}
	out, _ := json.Marshal(map[string]any{"model": "judge", "choices": []any{map[string]any{"finish_reason": "stop",
		"message": map[string]any{"role": "assistant", "content": answer}}}, "usage": map[string]any{"prompt_tokens": 1, "completion_tokens": 1}})
	_, _ = w.Write(out)
}

type world struct {
	t       *testing.T
	ctx     context.Context
	svc     *chat.Service
	owner   *pgx.Conn
	fake    *fakeslack.Server
	model   *model
	org     string
	project string
	seq     int
	// people by name: their person id and Slack user id.
	people map[string][2]string
}

// setup is Acme with Slack installed in workspace T1, the project ABS (with
// the epic Exports) on the Judgements tier, and people: Márcia (admin) and
// Eli connected; Gus in Acme but not connected; Ana connected but removed
// from Acme; Zed a Slack user dude has never heard of.
func setup(t *testing.T) *world {
	t.Helper()
	app, owner := dbtest.Open(t)
	ctx := context.Background()
	w := &world{t: t, ctx: ctx, owner: owner, model: &model{}, people: map[string][2]string{}}
	w.org = dbtest.Org(t, owner)
	w.exec(`UPDATE organizations SET slug = 'acme', name = 'Acme' WHERE id = $1`, w.org)
	var tier string
	if err := owner.QueryRow(ctx, `UPDATE model_tiers SET model = 'judge-model' WHERE organization_id = $1 AND name = 'Fast' RETURNING id`,
		w.org).Scan(&tier); err != nil {
		t.Fatal(err)
	}
	w.exec(`UPDATE organizations SET judgement_tier_id = $1 WHERE id = $2`, tier, w.org)
	w.project = "prj_abs_" + w.org
	w.exec(`INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES ($1, $2, 'Billing', 'abs', 'ABS')`, w.project, w.org)
	w.exec(`INSERT INTO epics (id, organization_id, project_id, title) VALUES ($1, $2, $3, 'Exports')`, "epc_exp_"+w.org, w.org, w.project)

	w.fake = fakeslack.New("cid", "csecret")
	w.fake.AddTeam("T1", "Acme Slack")
	slackSrv := httptest.NewServer(w.fake)
	t.Cleanup(slackSrv.Close)
	modelSrv := httptest.NewServer(w.model)
	t.Cleanup(modelSrv.Close)
	p := slack.New(slack.Config{SigningSecret: signingSecret, ClientID: "cid", ClientSecret: "csecret", APIURL: slackSrv.URL + "/api"})
	w.svc = &chat.Service{DB: app, Providers: chat.Registry{"slack": p}, PublicURL: "https://dude.example.com",
		Judge: &judge.Service{DB: app, LLM: llm.Client{URL: modelSrv.URL + "/v1", Key: "k"}}}

	for _, who := range []struct {
		name, slackID, role string
		connected, removed  bool
	}{{"Márcia", "U1", "admin", true, false}, {"Eli", "U2", "member", true, false}, {"Gus", "U3", "member", false, false},
		{"Ana", "U4", "member", true, true}} {
		person, account := fmt.Sprintf("per_%s_%s", who.slackID, w.org), fmt.Sprintf("acc_%s_%s", who.slackID, w.org)
		w.exec(`INSERT INTO accounts (id, email, name) VALUES ($1, $2, $3)`, account, account+"@example.com", who.name)
		w.exec(`INSERT INTO people (id, organization_id, name, role, account_id) VALUES ($1, $2, $3, $4, $5)`, person, w.org, who.name, who.role, account)
		if who.removed {
			w.exec(`UPDATE people SET removed_at = now() WHERE id = $1`, person)
		}
		w.fake.AddUser(fakeslack.User{ID: who.slackID, Team: "T1", Name: strings.ToLower(who.name), RealName: who.name})
		if who.connected {
			w.exec(`INSERT INTO chat_identities (organization_id, provider, external_workspace_id, external_user_id, account_id, person_id,
				handle, display_name) VALUES ($1, 'slack', 'T1', $2, $3, $4, $5, $6)`, w.org, who.slackID, account, person, strings.ToLower(who.name), who.name)
		}
		w.people[who.name] = [2]string{person, who.slackID}
	}
	w.fake.AddUser(fakeslack.User{ID: "U9", Team: "T1", Name: "zed", RealName: "Zed Outside"})
	w.exec(`INSERT INTO chat_installations (id, organization_id, provider, external_workspace_id, workspace_name, workspace_domain,
		bot_user_id, bot_token, installed_by) VALUES ($1, $2, 'slack', 'T1', 'Acme Slack', 'acme', 'UBT1', $3, $4)`,
		"chi_"+w.org, w.org, w.fake.Token("T1"), w.people["Márcia"][0])
	w.fake.AddConversation(fakeslack.Conversation{ID: "C1", Team: "T1", Name: "support", Kind: "public", Members: []string{"U1", "U2", "U3", "U4", "U9", "UBT1"}})
	w.fake.AddConversation(fakeslack.Conversation{ID: "G1", Team: "T1", Name: "design-private", Kind: "private",
		Members: []string{"U1", "U2", "U3", "U9", "UBT1"}})
	w.fake.AddConversation(fakeslack.Conversation{ID: "D1", Team: "T1", Kind: "dm", Members: []string{"U1", "UBT1"}})
	return w
}

func (w *world) exec(sql string, args ...any) {
	w.t.Helper()
	if _, err := w.owner.Exec(w.ctx, sql, args...); err != nil {
		w.t.Fatalf("%s: %v", sql, err)
	}
}

func (w *world) one(sql string, args ...any) string {
	w.t.Helper()
	var out *string
	if err := w.owner.QueryRow(w.ctx, sql, args...).Scan(&out); err != nil {
		w.t.Fatalf("%s: %v", sql, err)
	}
	if out == nil {
		return ""
	}
	return *out
}

func (w *world) count(sql string, args ...any) int {
	w.t.Helper()
	var n int
	if err := w.owner.QueryRow(w.ctx, sql, args...).Scan(&n); err != nil {
		w.t.Fatalf("%s: %v", sql, err)
	}
	return n
}

// deliver receives payload as Slack would send it, signed now, then runs
// the chat loop until it is idle.
func (w *world) deliver(payload map[string]any) any {
	w.t.Helper()
	raw, _ := json.Marshal(payload)
	req, _ := fakeslack.SignedRequest("http://x/", signingSecret, "application/json", raw, time.Now())
	out, err := w.svc.Receive(w.ctx, "slack", false, req.Header, raw)
	if err != nil {
		w.t.Fatalf("receive: %v", err)
	}
	w.sweep()
	return out
}

func (w *world) sweep() {
	w.t.Helper()
	for range 5 {
		n, err := w.svc.Sweep(w.ctx)
		if err != nil {
			w.t.Fatalf("sweep: %v", err)
		}
		if n == 0 {
			return
		}
	}
}

// mention posts text by name in channel (in thread, "" for a new one) and
// delivers its app_mention.
func (w *world) mention(name, channel, thread, text string) (ts string) {
	w.t.Helper()
	user := w.people[name][1]
	if user == "" {
		user = "U9"
	}
	ts = w.fake.Post(channel, thread, user, "<@UBT1> "+text)
	w.seq++
	w.deliver(fakeslack.Mention("T1", fmt.Sprintf("Ev%d", w.seq), channel, thread, ts, user, text))
	return ts
}

func (w *world) click(name, channel, msg, thread, value string) {
	w.t.Helper()
	raw, _ := json.Marshal(fakeslack.Click("T1", channel, msg, thread, w.people[name][1], value))
	body := []byte(url.Values{"payload": {string(raw)}}.Encode())
	req, _ := fakeslack.SignedRequest("http://x/", signingSecret, "application/x-www-form-urlencoded", body, time.Now())
	if _, err := w.svc.Receive(w.ctx, "slack", true, req.Header, body); err != nil {
		w.t.Fatal(err)
	}
	w.sweep()
}

func answer(v map[string]any) string {
	raw, _ := json.Marshal(v)
	return string(raw)
}

type req struct {
	Route, AskedRoute, ResultKind, ResultID, Kind, Person, Reasoning string
	Added, Skipped                                                   []string
	ModelCall                                                        string
}

func (w *world) requests() []req {
	w.t.Helper()
	rows, err := w.owner.Query(w.ctx, `SELECT route, COALESCE(asked_route, ''), COALESCE(result_kind, ''), COALESCE(result_id, ''),
			COALESCE(conversation_kind, ''), COALESCE(person_id, ''), reasoning, shared_added, shared_skipped, COALESCE(model_call_id, '')
		FROM chat_requests WHERE organization_id = $1 ORDER BY created_at, id`, w.org)
	if err != nil {
		w.t.Fatal(err)
	}
	out, err := pgx.CollectRows(rows, pgx.RowToStructByPos[req])
	if err != nil {
		w.t.Fatal(err)
	}
	return out
}

func lastBot(t *testing.T, f *fakeslack.Server) fakeslack.Message {
	t.Helper()
	msgs := f.BotMessages()
	if len(msgs) == 0 {
		t.Fatal("dude posted nothing")
	}
	return msgs[len(msgs)-1]
}

func TestChatReceiveVerifiesAnswersTheChallengeAndStoresOnce(t *testing.T) {
	w := setup(t)
	raw := []byte(`{"type":"url_verification","challenge":"c-123"}`)
	stale, _ := fakeslack.SignedRequest("http://x/", signingSecret, "application/json", raw, time.Now().Add(-10*time.Minute))
	if _, err := w.svc.Receive(w.ctx, "slack", false, stale.Header, raw); err == nil || err.(*chat.Refusal).Status != 401 {
		t.Fatalf("a stale request: %v", err)
	}
	ok, _ := fakeslack.SignedRequest("http://x/", signingSecret, "application/json", raw, time.Now())
	out, err := w.svc.Receive(w.ctx, "slack", false, ok.Header, raw)
	if err != nil || out.(map[string]any)["challenge"] != "c-123" {
		t.Fatalf("challenge: %v, %v", out, err)
	}
	if _, err := w.svc.Receive(w.ctx, "discord", false, ok.Header, raw); err != chat.ErrUnknownProvider {
		t.Fatalf("an unknown provider: %v", err)
	}
	// The same event delivered twice is stored once and routed once.
	ts := w.fake.Post("C1", "", "U1", "<@UBT1> hello")
	ev := fakeslack.Mention("T1", "EvDup", "C1", "", ts, "U1", "hello")
	w.model.queue(answer(map[string]any{"route": "brainstorm", "confidence": 0.9, "reasoning": "a chat", "title": "Hello"}))
	raw, _ = json.Marshal(ev)
	for range 2 {
		r, _ := fakeslack.SignedRequest("http://x/", signingSecret, "application/json", raw, time.Now())
		if _, err := w.svc.Receive(w.ctx, "slack", false, r.Header, raw); err != nil {
			t.Fatal(err)
		}
	}
	if n := w.count(`SELECT count(*) FROM chat_events WHERE organization_id = $1`, w.org); n != 1 {
		t.Fatalf("%d events stored", n)
	}
	w.sweep()
	// Handled again (a crash between the work and marking it done): nothing twice.
	w.exec(`UPDATE chat_events SET processed_at = NULL WHERE organization_id = $1`, w.org)
	w.sweep()
	if got := w.requests(); len(got) != 1 || w.count(`SELECT count(*) FROM sessions WHERE organization_id = $1`, w.org) != 1 || w.model.calls() != 1 {
		t.Fatalf("requests %+v, %d model calls", got, w.model.calls())
	}
	// A workspace no organisation installed: nothing stored.
	other, _ := json.Marshal(fakeslack.Mention("T9", "EvOther", "C1", "", ts, "U1", "x"))
	r, _ := fakeslack.SignedRequest("http://x/", signingSecret, "application/json", other, time.Now())
	if _, err := w.svc.Receive(w.ctx, "slack", false, r.Header, other); err != nil {
		t.Fatal(err)
	}
	if n := w.count(`SELECT count(*) FROM chat_events WHERE event_id = 'EvOther'`); n != 0 {
		t.Fatalf("an uninstalled workspace's event was stored")
	}
}

func TestChatPublicMentionMakesAnOrganisationSessionAndBindsTheThread(t *testing.T) {
	w := setup(t)
	root := w.fake.Post("C1", "", "U9", "CSV export times out for 40 meters")
	w.fake.Post("C1", root, "U2", "same for me")
	w.model.queue(answer(map[string]any{"route": "brainstorm", "confidence": 0.82, "reasoning": "needs investigating",
		"project": "ABS", "title": "CSV export timeout"}))
	ts := w.mention("Márcia", "C1", root, "can you reproduce this?")

	got := w.requests()
	if len(got) != 1 || got[0].Route != "brainstorm" || got[0].ResultKind != "session" || got[0].Kind != "public" ||
		got[0].Person != w.people["Márcia"][0] || got[0].ModelCall == "" {
		t.Fatalf("requests %+v", got)
	}
	session := got[0].ResultID
	if a := w.one(`SELECT access FROM sessions WHERE id = $1`, session); a != "organisation" {
		t.Fatalf("access %s", a)
	}
	if o := w.one(`SELECT person_id FROM session_people WHERE session_id = $1 AND role = 'owner'`, session); o != w.people["Márcia"][0] {
		t.Fatalf("owner %s", o)
	}
	if n := w.count(`SELECT count(*) FROM session_projects WHERE session_id = $1 AND project_id = $2`, session, w.project); n != 1 {
		t.Fatal("the session is not linked to ABS")
	}
	origin := w.one(`SELECT origin::text FROM sessions WHERE id = $1`, session)
	for _, want := range []string{`"provider": "slack"`, `"conversationName": "#support"`, `"Zed Outside"`, `"inDude": false`, `"by": "Eli"`} {
		if !strings.Contains(origin, want) {
			t.Fatalf("origin %s lacks %s", origin, want)
		}
	}
	// The agent's briefing quotes the thread, the non-member marked.
	if b := w.one(`SELECT prompt FROM runs WHERE session_id = $1`, session); !strings.Contains(b, "Zed Outside (not in dude): CSV export") ||
		!strings.Contains(b, "Márcia: Earlier in the thread") {
		t.Fatalf("briefing %s", b)
	}
	// The model was told the thread and the projects, on the Judgements tier, and it was recorded as a judgement.
	if sent := w.model.last(); !strings.Contains(sent, "Zed Outside (not in dude)") || !strings.Contains(sent, `ABS (Billing), epics: \"Exports\"`) ||
		!strings.Contains(sent, "judge-model") {
		t.Fatalf("sent %s", sent)
	}
	if a := w.one(`SELECT asker FROM model_calls WHERE id = $1 AND purpose = 'judgement'`, got[0].ModelCall); a != "chat.route" {
		t.Fatalf("asker %s", a)
	}
	reply := lastBot(t, w.fake)
	if reply.ThreadTS != root || !strings.Contains(reply.Text, "open to everyone in Acme") ||
		!strings.Contains(string(reply.Blocks), "https://dude.example.com/#/o/acme/sessions/"+session) || !strings.Contains(string(reply.Blocks), "Open in dude") {
		t.Fatalf("reply %+v", reply)
	}
	if r := w.fake.Reactions("C1", ts); strings.Join(r, ",") != "white_check_mark" {
		t.Fatalf("reactions %v", r)
	}
	if b := w.one(`SELECT session_id FROM chat_threads WHERE conversation_id = 'C1' AND thread_id = $1`, root); b != session {
		t.Fatalf("bound to %s", b)
	}

	// Eli mentions dude in the bound thread: his message, with what was said
	// since, goes into the session, with no model call; he joins it.
	w.fake.Post("C1", root, "U9", "it also fails for 20")
	calls := w.model.calls()
	w.mention("Eli", "C1", root, "here's another case")
	if w.model.calls() != calls {
		t.Fatal("a bound thread asked the model")
	}
	got = w.requests()
	if len(got) != 2 || got[1].Route != "resume" || got[1].ResultID != session {
		t.Fatalf("requests %+v", got)
	}
	if n := w.count(`SELECT count(*) FROM session_people WHERE session_id = $1 AND person_id = $2 AND role = 'chat'`, session, w.people["Eli"][0]); n != 1 {
		t.Fatal("Eli did not join the organisation session")
	}
	if d := w.one(`SELECT text FROM directives WHERE run_id = (SELECT id FROM runs WHERE session_id = $1) ORDER BY created_at DESC LIMIT 1`,
		session); !strings.Contains(d, "Zed Outside (not in dude): it also fails for 20") || !strings.Contains(d, "here's another case") {
		t.Fatalf("directive %s", d)
	}
	if !strings.Contains(lastBot(t, w.fake).Text, "Added to your session “CSV export timeout”") {
		t.Fatalf("reply %+v", lastBot(t, w.fake))
	}
}

func TestChatPrivateChannelSharesTheSessionWithConnectedMembers(t *testing.T) {
	w := setup(t)
	w.model.queue(answer(map[string]any{"route": "brainstorm", "confidence": 0.9, "reasoning": "a discussion", "title": "Onboarding"}))
	root := w.mention("Márcia", "G1", "", "let's think about onboarding")
	got := w.requests()
	if len(got) != 1 || got[0].Kind != "private" || got[0].ResultKind != "session" {
		t.Fatalf("requests %+v", got)
	}
	session := got[0].ResultID
	if a := w.one(`SELECT access FROM sessions WHERE id = $1`, session); a != "private" {
		t.Fatalf("access %s", a)
	}
	// Eli (connected) is added as chat; Gus (not connected) and Zed are skipped; the bot is neither.
	if strings.Join(got[0].Added, ",") != w.people["Eli"][0] || strings.Join(got[0].Skipped, ",") != "U3,U9" {
		t.Fatalf("added %v, skipped %v", got[0].Added, got[0].Skipped)
	}
	if r := w.one(`SELECT session_role($1, $2)`, session, w.people["Eli"][0]); r != "chat" {
		t.Fatalf("Eli's role %s", r)
	}
	if r := w.one(`SELECT session_role($1, $2)`, session, w.people["Gus"][0]); r != "" {
		t.Fatalf("Gus's role %s", r)
	}
	if text := lastBot(t, w.fake).Text; !strings.Contains(text, "shared with 1 person from #design-private; 2 are not connected") {
		t.Fatalf("reply %s", text)
	}
	if o := w.one(`SELECT origin->'routing'->>'skipped' FROM sessions WHERE id = $1`, session); o != `["U3", "U9"]` {
		t.Fatalf("routing line skipped %s", o)
	}
	if o := w.one(`SELECT origin->'routing'->>'skippedNames' FROM sessions WHERE id = $1`, session); o != `["Gus", "Zed Outside"]` {
		t.Fatalf("routing line skipped names %s", o)
	}

	// Gus connects later and mentions dude in the thread: he is added, and
	// his message goes in. Eli leaving the channel keeps his access.
	gusAccount := w.one(`SELECT account_id FROM people WHERE id = $1`, w.people["Gus"][0])
	w.exec(`INSERT INTO chat_identities (organization_id, provider, external_workspace_id, external_user_id, account_id, person_id)
		VALUES ($1, 'slack', 'T1', 'U3', $2, $3)`, w.org, gusAccount, w.people["Gus"][0])
	w.fake.SetMembers("G1", []string{"U1", "U3", "UBT1"})
	w.mention("Gus", "G1", root, "I have ideas")
	got = w.requests()
	if len(got) != 2 || got[1].Route != "resume" || strings.Join(got[1].Added, ",") != w.people["Gus"][0] {
		t.Fatalf("requests %+v", got)
	}
	for _, name := range []string{"Gus", "Eli"} {
		if r := w.one(`SELECT session_role($1, $2)`, session, w.people[name][0]); r != "chat" {
			t.Fatalf("%s's role %q", name, r)
		}
	}
}

func TestChatTaskRouteFilesInTheBacklogAndTheBoundThreadLinksIt(t *testing.T) {
	w := setup(t)
	w.model.queue(answer(map[string]any{"route": "task", "confidence": 0.9, "reasoning": "one clear change", "project": "ABS",
		"epic": "Exports", "title": "Stream the CSV response", "goal": "Stream it.", "criteria": []string{"40 meters under 60 s"}}))
	root := w.mention("Eli", "C1", "", "please file: stream the CSV export")
	got := w.requests()
	if len(got) != 1 || got[0].Route != "task" || got[0].ResultKind != "task" {
		t.Fatalf("requests %+v", got)
	}
	task := got[0].ResultID
	var status, epic, origin, owner string
	if err := w.owner.QueryRow(w.ctx, `SELECT t.status::text, COALESCE(t.epic_id, ''), t.origin::text, tp.person_id FROM tasks t
		JOIN task_people tp ON tp.task_id = t.id WHERE t.id = $1`, task).Scan(&status, &epic, &origin, &owner); err != nil {
		t.Fatal(err)
	}
	if status != "received" || epic != "epc_exp_"+w.org || owner != w.people["Eli"][0] || !strings.Contains(origin, `"requestedBy": "Eli"`) {
		t.Fatalf("task %s %s %s %s", status, epic, owner, origin)
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE task_id = $1`, task); n != 0 {
		t.Fatal("the task was started")
	}
	if text := lastBot(t, w.fake).Text; text != "Filed ABS-1 Stream the CSV response in Backlog" {
		t.Fatalf("reply %q", text)
	}
	calls := w.model.calls()
	w.mention("Márcia", "C1", root, "what's the status?")
	got = w.requests()
	if w.model.calls() != calls || len(got) != 2 || got[1].Route != "task_link" || got[1].ResultID != task {
		t.Fatalf("requests %+v, model calls %d", got, w.model.calls())
	}
	if reply := lastBot(t, w.fake); reply.Text != "This thread is ABS-1 Stream the CSV response" ||
		!strings.Contains(string(reply.Blocks), "#/o/acme/task/"+task) {
		t.Fatalf("reply %+v", reply)
	}
}

func TestChatEpicRouteFilesTheEpicAndItsTasksWithTheirOrder(t *testing.T) {
	w := setup(t)
	tasks := []map[string]any{
		{"ref": "t1", "title": "Measure export time", "goal": "g", "criteria": []string{"c"}, "blocked_by": []string{}},
		{"ref": "t2", "title": "Batch the readings query", "goal": "g", "blocked_by": []string{"t1"}},
		{"ref": "t3", "title": "Stream the response", "goal": "g", "blocked_by": []string{"t1"}},
		{"ref": "t4", "title": "Regression test", "goal": "g", "blocked_by": []string{"t2", "t3"}},
	}
	w.model.queue(answer(map[string]any{"route": "epic", "confidence": 0.88, "reasoning": "several pieces", "project": "ABS",
		"new_epic": map[string]any{"title": "CSV exports", "description": "Make exports fast.", "tasks": tasks}}))
	w.mention("Márcia", "C1", "", "open an epic for fast CSV exports")
	got := w.requests()
	if len(got) != 1 || got[0].Route != "epic" || got[0].ResultKind != "epic" {
		t.Fatalf("requests %+v", got)
	}
	epic := got[0].ResultID
	if n := w.count(`SELECT count(*) FROM tasks WHERE epic_id = $1 AND status = 'received' AND origin->>'provider' = 'slack'`, epic); n != 4 {
		t.Fatalf("%d tasks in the epic's Backlog", n)
	}
	if o := w.one(`SELECT origin->>'conversationName' FROM epics WHERE id = $1`, epic); o != "#support" {
		t.Fatalf("epic origin %s", o)
	}
	edges := w.one(`SELECT string_agg(f.title || ' → ' || t.title, '; ' ORDER BY f.title, t.title) FROM task_relations r
		JOIN tasks f ON f.id = r.from_task_id JOIN tasks t ON t.id = r.to_task_id WHERE r.kind = 'blocks' AND t.epic_id = $1`, epic)
	if edges != "Batch the readings query → Regression test; Measure export time → Batch the readings query; Measure export time → Stream the response; Stream the response → Regression test" {
		t.Fatalf("edges %s", edges)
	}
	if text := lastBot(t, w.fake).Text; text != "Opened epic ABS › CSV exports with 4 tasks in Backlog" {
		t.Fatalf("reply %q", text)
	}
}

func TestChatRefusesTheUnconnectedAndNonMembersPrivately(t *testing.T) {
	w := setup(t)
	// Zed: never connected. A private Connect button, the mention kept.
	ts := w.mention("Zed", "C1", "", "help me")
	eph := w.fake.Ephemerals()
	if len(eph) != 1 || eph[0].User != "U9" || !strings.Contains(eph[0].Text, "Connect your Slack account") ||
		!strings.Contains(string(eph[0].Blocks), "https://dude.example.com/#/o/acme/connect/slack?state=") {
		t.Fatalf("ephemerals %+v", eph)
	}
	if n := w.count(`SELECT count(*) FROM chat_connect_states WHERE kind = 'prompt' AND external_user_id = 'U9'
		AND pending_mention->>'text' = 'help me' AND expires_at <= now() + interval '15 minutes'`); n != 1 {
		t.Fatal("the mention was not kept against a 15-minute state")
	}
	if r := w.fake.Reactions("C1", ts); len(r) != 0 {
		t.Fatalf("a refusal left %v", r)
	}
	// Ana: connected, but removed from Acme. A generic refusal naming nothing of Acme.
	w.mention("Ana", "C1", "", "help me")
	eph = w.fake.Ephemerals()
	if len(eph) != 2 || eph[1].User != "U4" || strings.Contains(eph[1].Text, "Acme") || strings.Contains(eph[1].Text, "ABS") {
		t.Fatalf("ephemerals %+v", eph)
	}
	got := w.requests()
	if len(got) != 2 || got[0].Route != "not_connected" || got[1].Route != "not_member" || got[1].Person != "" {
		t.Fatalf("requests %+v", got)
	}
	if w.model.calls() != 0 || len(w.fake.BotMessages()) != 0 {
		t.Fatalf("a refusal asked the model or replied in public")
	}
}

func TestChatLowConfidenceAsksAndTheAskersChoiceFiles(t *testing.T) {
	w := setup(t)
	w.model.queue(answer(map[string]any{"route": "task", "confidence": 0.6, "reasoning": "might be a task", "project": "ABS",
		"title": "Fix the export", "goal": "Fix it."}))
	root := w.mention("Eli", "C1", "", "the export is broken")
	q := lastBot(t, w.fake)
	if q.Text != "Should I file the task in ABS, or start a session?" || !strings.Contains(string(q.Blocks), "File it in ABS") ||
		!strings.Contains(string(q.Blocks), "A session for me") {
		t.Fatalf("question %+v", q)
	}
	got := w.requests()
	if len(got) != 1 || got[0].Route != "clarify" || got[0].AskedRoute != "task" || !strings.Contains(got[0].Reasoning, "under the 75% bar") {
		t.Fatalf("requests %+v", got)
	}
	var value string
	var blocks []map[string]any
	_ = json.Unmarshal(q.Blocks, &blocks)
	value = blocks[1]["elements"].([]any)[0].(map[string]any)["value"].(string)
	// Someone else's click is refused privately and changes nothing.
	w.click("Márcia", "C1", q.TS, root, value)
	if len(w.requests()) != 1 || len(w.fake.Updates()) != 0 {
		t.Fatal("another person answered the question")
	}
	w.click("Eli", "C1", q.TS, root, value)
	if u := w.fake.Updates(); len(u) != 1 || u[0].Text != "Eli chose File it in ABS" {
		t.Fatalf("updates %+v", u)
	}
	got = w.requests()
	if len(got) != 2 || got[1].Route != "task" || got[1].ResultKind != "task" {
		t.Fatalf("requests %+v", got)
	}
	if text := lastBot(t, w.fake).Text; text != "Filed ABS-1 Fix the export in Backlog" {
		t.Fatalf("reply %q", text)
	}
	// A second click on the answered question does nothing.
	w.click("Eli", "C1", q.TS, root, value)
	if len(w.requests()) != 2 {
		t.Fatal("an answered question routed again")
	}
}

func TestChatFallsBackToABrainstorm(t *testing.T) {
	w := setup(t)
	twelve := func(n int) []map[string]any {
		var out []map[string]any
		for i := range n {
			out = append(out, map[string]any{"ref": fmt.Sprintf("t%d", i), "title": fmt.Sprintf("Task %d", i), "blocked_by": []string{}})
		}
		return out
	}
	cases := []struct {
		name, answer string
		route, asked string
		question     string
	}{
		{"no answer from the proxy", "", "failed", "", ""},
		{"not JSON", "I think a task", "failed", "", ""},
		{"a project dude does not know, no projects to offer instead", answer(map[string]any{"route": "task", "confidence": 0.9,
			"project": "NOPE", "title": "x"}), "clarify", "task", "Which project should the task go in?"},
		{"a cycle", answer(map[string]any{"route": "epic", "confidence": 0.9, "project": "ABS", "new_epic": map[string]any{"title": "E",
			"tasks": []map[string]any{{"ref": "a", "title": "A", "blocked_by": []string{"b"}}, {"ref": "b", "title": "B", "blocked_by": []string{"a"}}}}}),
			"brainstorm", "epic", ""},
		{"13 tasks", answer(map[string]any{"route": "epic", "confidence": 0.9, "project": "ABS", "new_epic": map[string]any{"title": "E",
			"tasks": twelve(13)}}), "clarify", "epic", "That is 13 tasks. File the first 12, or talk it through?"},
		{"a session the person cannot chat in", answer(map[string]any{"route": "resume", "confidence": 0.95, "session": "ssn_nope"}),
			"brainstorm", "resume", ""},
		{"a question with one choice", answer(map[string]any{"route": "clarify", "confidence": 0.9, "question": map[string]any{"text": "?",
			"choices": []map[string]any{{"id": "a", "label": "A", "route": "task"}}}}), "brainstorm", "clarify", ""},
	}
	for i, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if c.answer != "" {
				w.model.queue(c.answer)
			}
			before := len(w.requests())
			w.mention("Márcia", "C1", "", fmt.Sprintf("case %d", i))
			got := w.requests()[before:]
			if len(got) != 1 || got[0].Route != c.route || got[0].AskedRoute != c.asked {
				t.Fatalf("requests %+v", got)
			}
			if c.question != "" {
				if q := lastBot(t, w.fake).Text; q != c.question {
					t.Fatalf("question %q", q)
				}
				return
			}
			if got[0].ResultKind != "session" {
				t.Fatalf("not a brainstorm: %+v", got[0])
			}
		})
	}
}

func TestChatAsksAtMostTwoRoundsThenBrainstorms(t *testing.T) {
	w := setup(t)
	unsure := answer(map[string]any{"route": "task", "confidence": 0.5, "project": "ABS", "title": "x"})
	w.model.queue(unsure, unsure, unsure)
	root := w.mention("Eli", "C1", "", "hmm")
	w.mention("Eli", "C1", root, "hmm again")
	w.mention("Eli", "C1", root, "and again")
	got := w.requests()
	if len(got) != 3 || got[0].Route != "clarify" || got[1].Route != "clarify" || got[2].Route != "brainstorm" || got[2].ResultKind != "session" {
		t.Fatalf("requests %+v", got)
	}
}

func TestChatADirectMessageMakesAPrivateSessionAndBindsNothing(t *testing.T) {
	w := setup(t)
	w.model.queue(answer(map[string]any{"route": "brainstorm", "confidence": 0.9, "title": "Ideas"}))
	ts := w.fake.Post("D1", "", "U1", "start a session about ideas")
	w.deliver(fakeslack.DirectMessage("T1", "EvDM", "D1", ts, "U1", "start a session about ideas"))
	got := w.requests()
	if len(got) != 1 || got[0].Kind != "dm" || got[0].ResultKind != "session" {
		t.Fatalf("requests %+v", got)
	}
	if a := w.one(`SELECT access FROM sessions WHERE id = $1`, got[0].ResultID); a != "private" {
		t.Fatalf("access %s", a)
	}
	if n := w.count(`SELECT count(*) FROM chat_threads WHERE organization_id = $1`, w.org); n != 0 {
		t.Fatal("a DM was bound")
	}
	if r := lastBot(t, w.fake); r.Channel != "D1" || r.ThreadTS != r.TS {
		t.Fatalf("a DM reply went to %+v", r)
	}
}

func TestChatInstructionsAreToldInOrderAndTheAppsRemovalUninstalls(t *testing.T) {
	w := setup(t)
	w.exec(`UPDATE organizations SET chat_instructions = 'Acme files billing work in ABS.' WHERE id = $1`, w.org)
	w.exec(`UPDATE chat_installations SET settings = '{"additional_instructions": "From Slack, prefer sessions."}' WHERE organization_id = $1`, w.org)
	w.model.queue(answer(map[string]any{"route": "brainstorm", "confidence": 0.9, "title": "x"}))
	w.mention("Márcia", "C1", "", "hello")
	sent := w.model.last()
	b, o, p := strings.Index(sent, "You route a message"), strings.Index(sent, "Acme's additional instructions"), strings.Index(sent, "Additional instructions for Slack")
	if b < 0 || o < b || p < o || !strings.Contains(sent, "prefer sessions") {
		t.Fatalf("instructions out of order: %d %d %d", b, o, p)
	}
	w.deliver(map[string]any{"type": "event_callback", "team_id": "T1", "event_id": "EvGone", "event": map[string]any{"type": "app_uninstalled"}})
	if n := w.count(`SELECT count(*) FROM chat_installations WHERE organization_id = $1`, w.org); n != 0 {
		t.Fatal("the app's removal left the installation")
	}
}

// install runs an admin's install against the fake's OAuth page.
func follow(t *testing.T, u string) url.Values {
	t.Helper()
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, err := client.Get(u)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	loc, err := url.Parse(res.Header.Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	return loc.Query()
}

func TestChatInstallBindsOneWorkspaceToOneOrganisation(t *testing.T) {
	w := setup(t)
	w.exec(`DELETE FROM chat_installations WHERE organization_id = $1`, w.org)
	admin, member := w.people["Márcia"][0], w.people["Eli"][0]
	redirect := "https://dude.example.com/v1/chat/slack/install/callback"
	if _, err := w.svc.BeginInstall(w.ctx, w.org, chat.Begin{Provider: "slack", Person: member, BrowserHash: "b", Redirect: redirect}); err == nil {
		t.Fatal("a member began an install")
	}
	w.fake.AuthorizeAs("U1", "")
	start, err := w.svc.BeginInstall(w.ctx, w.org, chat.Begin{Provider: "slack", Person: admin, BrowserHash: "b", Redirect: redirect, Admin: true})
	if err != nil {
		t.Fatal(err)
	}
	back := follow(t, start)
	// Finished in another browser: refused, and the state is spent.
	if l, _ := w.svc.CompleteInstall(w.ctx, chat.Callback{Provider: "slack", State: back.Get("state"), Code: back.Get("code"), BrowserHash: "x"}); l.Outcome != "invalid_or_expired" {
		t.Fatalf("another browser: %+v", l)
	}
	start, _ = w.svc.BeginInstall(w.ctx, w.org, chat.Begin{Provider: "slack", Person: admin, BrowserHash: "b", Redirect: redirect, Admin: true})
	back = follow(t, start)
	l, err := w.svc.CompleteInstall(w.ctx, chat.Callback{Provider: "slack", State: back.Get("state"), Code: back.Get("code"), BrowserHash: "b"})
	if err != nil || l.Outcome != "installed" || l.Slug != "acme" {
		t.Fatalf("install: %+v, %v", l, err)
	}
	if n := w.count(`SELECT count(*) FROM chat_installations WHERE organization_id = $1 AND external_workspace_id = 'T1'
		AND workspace_name = 'Acme Slack' AND installed_by = $2`, w.org, admin); n != 1 {
		t.Fatal("not installed")
	}
	// Another organisation installing the same workspace is refused.
	other := dbtest.Org(t, w.owner)
	w.exec(`INSERT INTO people (id, organization_id, name, role) VALUES ($1, $2, 'Other admin', 'admin')`, "per_o_"+other, other)
	start, _ = w.svc.BeginInstall(w.ctx, other, chat.Begin{Provider: "slack", Person: "per_o_" + other, BrowserHash: "o", Redirect: redirect, Admin: true})
	back = follow(t, start)
	if l, err := w.svc.CompleteInstall(w.ctx, chat.Callback{Provider: "slack", State: back.Get("state"), Code: back.Get("code"), BrowserHash: "o"}); err != nil ||
		l.Outcome != "workspace_taken" {
		t.Fatalf("a second organisation: %+v, %v", l, err)
	}
	// Denied at Slack.
	start, _ = w.svc.BeginInstall(w.ctx, w.org, chat.Begin{Provider: "slack", Person: admin, BrowserHash: "b", Redirect: redirect, Admin: true})
	if l, _ := w.svc.CompleteInstall(w.ctx, chat.Callback{Provider: "slack", State: follow(t, start).Get("state"), Error: "access_denied",
		BrowserHash: "b"}); l.Outcome != "authorization_denied" {
		t.Fatalf("denied: %+v", l)
	}
	// Disconnect: an admin's, revoking the token.
	if err := w.svc.Disconnect(w.ctx, w.org, "slack", false); err == nil {
		t.Fatal("a member disconnected")
	}
	if err := w.svc.Disconnect(w.ctx, w.org, "slack", true); err != nil {
		t.Fatal(err)
	}
	if n := w.count(`SELECT count(*) FROM chat_installations WHERE organization_id = $1`, w.org); n != 0 || len(w.fake.Revoked()) != 1 {
		t.Fatalf("disconnect left %d, revoked %v", n, w.fake.Revoked())
	}
}

func TestChatConnectLinksOnceAndRoutesTheKeptMention(t *testing.T) {
	w := setup(t)
	redirect := "https://dude.example.com/v1/chat/slack/connect/callback"
	gus := w.people["Gus"][0]
	// Gus mentions dude before connecting: a Connect prompt keeps the mention.
	w.mention("Gus", "C1", "", "file a task: tidy the export page")
	eph := w.fake.Ephemerals()
	link, _ := url.Parse(strings.TrimPrefix(regexpFind(string(eph[0].Blocks)), ""))
	prompt := link.Query().Get("state")
	if prompt == "" {
		t.Fatalf("no state in %s", eph[0].Blocks)
	}
	if st, err := w.svc.ConnectState(w.ctx, w.org, "slack", gus, prompt); err != nil || st["status"] != "ready" || st["workspaceName"] != "Acme Slack" {
		t.Fatalf("connect state %+v, %v", st, err)
	}
	other := dbtest.Org(t, w.owner)
	if st, _ := w.svc.ConnectState(w.ctx, other, "slack", gus, prompt); st["status"] != "wrong_organization" {
		t.Fatalf("signed in elsewhere: %+v", st)
	}
	// Eli's Slack account signs in on Gus's behalf: Eli's is linked already, so already_linked.
	w.fake.AuthorizeAs("", "U2")
	start, err := w.svc.BeginConnect(w.ctx, w.org, chat.Begin{Provider: "slack", Person: gus, BrowserHash: "g", Redirect: redirect})
	if err != nil {
		t.Fatal(err)
	}
	back := follow(t, start)
	if l, _ := w.svc.CompleteConnect(w.ctx, chat.Callback{Provider: "slack", State: back.Get("state"), Code: back.Get("code"), BrowserHash: "g"}); l.Outcome != "already_linked" {
		t.Fatalf("Eli's account for Gus: %+v", l)
	}
	// Gus signs in as himself from the prompt's connect page: linked, and his kept mention is routed.
	w.fake.AuthorizeAs("", "U3")
	w.model.queue(answer(map[string]any{"route": "task", "confidence": 0.9, "project": "ABS", "title": "Tidy the export page", "goal": "g"}))
	start, err = w.svc.BeginConnect(w.ctx, w.org, chat.Begin{Provider: "slack", Person: gus, BrowserHash: "g", Redirect: redirect, Prompt: prompt})
	if err != nil {
		t.Fatal(err)
	}
	back = follow(t, start)
	l, err := w.svc.CompleteConnect(w.ctx, chat.Callback{Provider: "slack", State: back.Get("state"), Code: back.Get("code"), BrowserHash: "g"})
	if err != nil || l.Outcome != "connected" || l.ReturnTo != "connect" {
		t.Fatalf("connect: %+v, %v", l, err)
	}
	w.sweep()
	got := w.requests()
	if len(got) != 2 || got[1].Route != "task" || got[1].Person != gus {
		t.Fatalf("requests %+v", got)
	}
	if text := lastBot(t, w.fake).Text; text != "Filed ABS-1 Tidy the export page in Backlog" {
		t.Fatalf("reply %q", text)
	}
	// The prompt's state was spent.
	if _, err := w.svc.BeginConnect(w.ctx, w.org, chat.Begin{Provider: "slack", Person: gus, BrowserHash: "g", Redirect: redirect, Prompt: prompt}); err == nil {
		t.Fatal("a prompt's state was used twice")
	}
	if err := w.svc.Unlink(w.ctx, w.org, "slack", gus); err != nil {
		t.Fatal(err)
	}
	if n := w.count(`SELECT count(*) FROM chat_identities WHERE person_id = $1`, gus); n != 0 {
		t.Fatal("not unlinked")
	}
}

// regexpFind is the Connect URL in a message's blocks.
func regexpFind(blocks string) string {
	i := strings.Index(blocks, "https://dude.example.com/#/o/acme/connect/slack?state=")
	if i < 0 {
		return ""
	}
	rest := blocks[i:]
	end := strings.IndexByte(rest, '"')
	u := rest[:end]
	// The link's query is after the hash: parse it as the page would.
	_, q, _ := strings.Cut(u, "?")
	return "https://x/?" + q
}

var _ = db.IsNotFound
