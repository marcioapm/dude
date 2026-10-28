package agenttools_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/agenttools"
	"github.com/marciomartins/dude/orchestrator/internal/chat"
	"github.com/marciomartins/dude/orchestrator/internal/chat/slack"
	"github.com/marciomartins/dude/orchestrator/internal/fakeslack"
)

// threadFixture is a brainstorm whose session a Slack thread in #support
// started, against the fake Slack, with Slack installed or not.
type threadFixture struct {
	*fixture
	fake  *fakeslack.Server
	token string
	root  string
}

const chatOrigin = `{"provider":"slack","workspaceId":"T1","conversationId":"C0SUPPORT","conversationName":"#support",
	"conversationKind":"public","threadId":"%s","requestedBy":"Ana"}`

func setupThread(t *testing.T, installed bool, beforeCall func(string)) threadFixture {
	t.Helper()
	return setupThreadWithHandler(t, installed, beforeCall, nil)
}

func setupThreadWithHandler(t *testing.T, installed bool, beforeCall func(string), wrap func(http.Handler) http.Handler) threadFixture {
	t.Helper()
	fake := fakeslack.New("cid", "csecret")
	fake.AddTeam("T1", "Acme")
	fake.AddConversation(fakeslack.Conversation{ID: "C0SUPPORT", Team: "T1", Name: "support", Kind: "public", Members: []string{"UBT1", "U1"}})
	fake.AddConversation(fakeslack.Conversation{ID: "C0RANDOMXX", Team: "T1", Name: "random", Kind: "public", Members: []string{"UBT1"}})
	fake.AddConversation(fakeslack.Conversation{ID: "D0ANA", Team: "T1", Kind: "dm", Members: []string{"UBT1", "U1"}})
	var handler http.Handler = fake
	if wrap != nil {
		handler = wrap(handler)
	}
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	p := slack.New(slack.Config{ClientID: "cid", ClientSecret: "csecret", APIURL: srv.URL + "/api"})
	f := setupWith(t, &agenttools.Server{ChatProviders: chat.Registry{"slack": p}, BeforeCall: beforeCall})
	if installed {
		mustExec(t, f.owner, `INSERT INTO chat_installations (id, organization_id, provider, external_workspace_id, workspace_name,
			workspace_domain, bot_user_id, bot_token) VALUES ($1, $2, 'slack', 'T1', 'Acme', 'acme', 'UBT1', $3)`,
			"chi_"+f.org, f.org, fake.Token("T1"))
	}
	root := fake.Post("C0SUPPORT", "", "U1", "@dude what time is it in Berlin?")
	token := f.session(t, "ssn_thread", "", "")
	mustExec(t, f.owner, `UPDATE sessions SET origin = $1::jsonb WHERE id = 'ssn_thread'`, fmt.Sprintf(chatOrigin, root))
	th := threadFixture{fixture: f, fake: fake, token: token, root: root}
	// The mention that started the session: its first turn.
	th.personSaysIn(t, "C0SUPPORT", "#support", root, "what time is it in Berlin?")
	return th
}

func (th threadFixture) reply(t *testing.T, args string) (int, map[string]any) {
	t.Helper()
	return th.post(t, th.token, "reply_in_thread", args)
}

// personSays records a member's message to the session from its own
// thread: a new turn.
func (th threadFixture) personSays(t *testing.T, text string) {
	t.Helper()
	th.personSaysIn(t, "C0SUPPORT", "#support", th.root, text)
}

// personSaysIn records a member's message to the session from a chat
// conversation and thread, as the chat writes it (chat.writerFrom).
func (th threadFixture) personSaysIn(t *testing.T, conversation, name, thread, text string) {
	t.Helper()
	th.says(t, "person", map[string]string{"text": text, "via": "slack", "provider": "slack", "workspaceId": "T1",
		"conversationId": conversation, "conversationName": name, "threadId": thread})
}

// says records a chat.message on the session by actorType, with payload.
func (th threadFixture) says(t *testing.T, actorType string, payload map[string]string) {
	t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, th.owner, `INSERT INTO events (id, organization_id, session_id, run_id, event_type, actor_type, actor_id, source, payload)
		VALUES ($1, $2, 'ssn_thread', 'run_ssn_thread', 'chat.message', $3, 'person_ana', 'orchestrator', $4::jsonb)`,
		fmt.Sprintf("evt_%d", time.Now().UnixNano()), th.org, actorType, string(raw))
}

func (th threadFixture) count(t *testing.T, query string, args ...any) int {
	t.Helper()
	var n int
	if err := th.owner.QueryRow(context.Background(), query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestReplyInThreadPostsInTheSessionsOwnThreadOnly(t *testing.T) {
	th := setupThread(t, true, nil)
	status, out := th.reply(t, `{"text":"It is *22:15* in Berlin"}`)
	if status != 200 {
		t.Fatalf("reply: %d %v", status, out)
	}
	msgs := th.fake.BotMessages()
	if len(msgs) != 1 || msgs[0].Channel != "C0SUPPORT" || msgs[0].ThreadTS != th.root || msgs[0].Text != "It is *22:15* in Berlin" {
		t.Fatalf("Slack got %+v", msgs)
	}
	if out["channel"] != "#support" || !strings.Contains(fmt.Sprint(out["permalink"]), "archives/C0SUPPORT") {
		t.Errorf("answer %v", out)
	}
	if n := th.count(t, `SELECT count(*) FROM events WHERE session_id = 'ssn_thread' AND event_type = $1 AND payload->>'channel' = '#support'
		AND payload->>'threadId' = $2`, agenttools.EvThreadReplied, th.root); n != 1 {
		t.Errorf("%d posted events on the session", n)
	}
	// The agent names no channel: one it tries is refused and nothing goes there.
	if status, _ := th.reply(t, `{"text":"hi","channel":"C0RANDOMXX"}`); status != 422 {
		t.Errorf("a channel named by the agent: %d", status)
	}
	if msgs := th.fake.BotMessages(); len(msgs) != 1 {
		t.Errorf("Slack got %+v", msgs)
	}
}

// A session no chat started, and any task agent, has no reply_in_thread:
// not listed, and not found when called.
func TestReplyInThreadIsAbsentForASessionNotFromChat(t *testing.T) {
	th := setupThread(t, true, nil)
	web := th.session(t, "ssn_web", "", "")
	if status, _ := th.post(t, web, "reply_in_thread", `{"text":"hello"}`); status != 404 {
		t.Errorf("a web session's call: %d, want 404", status)
	}
	if listed := toolNames(t, th.fixture, web); strings.Contains(listed, "reply_in_thread") || !strings.Contains(listed, "chat_history") {
		t.Errorf("a web session lists %s", listed)
	}
	if listed := toolNames(t, th.fixture, th.token); !strings.Contains(listed, "reply_in_thread") {
		t.Errorf("a chat-born session lists %s", listed)
	}
	if status, _ := th.post(t, th.run(t, "run_impl", "implementer", "running"), "reply_in_thread", `{"text":"hello"}`); status != 404 {
		t.Errorf("an implementer's call: %d, want 404", status)
	}
	if n := len(th.fake.BotMessages()); n != 0 {
		t.Errorf("%d posts reached Slack", n)
	}
}

func TestReplyInThreadRedactsTheRunsSecretValues(t *testing.T) {
	th := setupThread(t, true, nil)
	mustExec(t, th.owner, `INSERT INTO run_project_secrets (run_id, organization_id, name, value) VALUES ('run_ssn_thread', $1, 'API_KEY', 'sk-acme-s3cr3t-value')`, th.org)
	if status, out := th.reply(t, `{"text":"Used sk-acme-s3cr3t-value to check"}`); status != 200 {
		t.Fatalf("reply: %d %v", status, out)
	}
	msgs := th.fake.BotMessages()
	if len(msgs) != 1 || strings.Contains(msgs[0].Text, "sk-acme-s3cr3t-value") || strings.Contains(string(msgs[0].Blocks), "sk-acme-s3cr3t-value") ||
		!strings.Contains(msgs[0].Text, "[REDACTED:API_KEY] to check") {
		t.Fatalf("Slack got %+v", msgs)
	}
	if n := th.count(t, `SELECT count(*) FROM events WHERE organization_id = $1 AND payload::text LIKE '%sk-acme-s3cr3t-value%'`, th.org); n != 0 {
		t.Errorf("%d ledger events carry the secret", n)
	}
}

// A session started in #support and resumed from a DM answers that turn in
// the DM, never in #support; the next message from #support's thread
// brings the answer back there.
func TestReplyInThreadAnswersInTheDMThatResumedTheSession(t *testing.T) {
	th := setupThread(t, true, nil)
	th.personSaysIn(t, "D0ANA", "a direct message", "", "and what time is it in Lisbon?")
	status, out := th.reply(t, `{"text":"21:15 in Lisbon"}`)
	if status != 200 || out["channel"] != "a direct message" {
		t.Fatalf("reply: %d %v", status, out)
	}
	msgs := th.fake.BotMessages()
	// At the DM's top level, as dude's own reply there: its own thread.
	if len(msgs) != 1 || msgs[0].Channel != "D0ANA" || msgs[0].ThreadTS != msgs[0].TS || msgs[0].Text != "21:15 in Lisbon" {
		t.Fatalf("Slack got %+v", msgs)
	}
	if n := th.count(t, `SELECT count(*) FROM events WHERE session_id = 'ssn_thread' AND event_type = $1 AND payload->>'conversationId' = 'D0ANA'
		AND payload->>'threadId' = ''`, agenttools.EvThreadReplied); n != 1 {
		t.Errorf("%d posted events name the DM", n)
	}
	th.personSays(t, "thanks, back here")
	if status, out := th.reply(t, `{"text":"You are welcome"}`); status != 200 {
		t.Fatalf("reply: %d %v", status, out)
	}
	msgs = th.fake.BotMessages()
	if len(msgs) != 2 || msgs[1].Channel != "C0SUPPORT" || msgs[1].ThreadTS != th.root {
		t.Fatalf("Slack got %+v", msgs)
	}
	for _, m := range msgs[:1] {
		if m.Channel == "C0SUPPORT" {
			t.Errorf("the DM's answer reached #support: %+v", m)
		}
	}
}

// A thread in another conversation bound to the session by a resume gets
// the answer to a message from it.
func TestReplyInThreadAnswersInABoundThreadOfAnotherConversation(t *testing.T) {
	th := setupThread(t, true, nil)
	other := th.fake.Post("C0RANDOMXX", "", "U1", "@dude continue the Berlin session")
	th.personSaysIn(t, "C0RANDOMXX", "#random", other, "continue the Berlin session")
	if status, out := th.reply(t, `{"text":"Picked it up here"}`); status != 200 || out["channel"] != "#random" {
		t.Fatalf("reply: %d %v", status, out)
	}
	msgs := th.fake.BotMessages()
	if len(msgs) != 1 || msgs[0].Channel != "C0RANDOMXX" || msgs[0].ThreadTS != other {
		t.Fatalf("Slack got %+v", msgs)
	}
}

// A message typed in dude to a chat-born session starts a turn with no
// thread to answer: the reply is refused and nothing is posted.
func TestReplyInThreadRefusesATurnTypedInDude(t *testing.T) {
	th := setupThread(t, true, nil)
	th.says(t, "person", map[string]string{"text": "what about Tokyo?"})
	status, out := th.reply(t, `{"text":"06:15 in Tokyo"}`)
	if status != 422 || !strings.Contains(fmt.Sprint(out["error"]), "typed in dude, not in the chat") {
		t.Fatalf("a web turn's reply: %d %v", status, out)
	}
	if n := len(th.fake.BotMessages()); n != 0 {
		t.Errorf("%d posts reached Slack", n)
	}
	if n := th.count(t, `SELECT count(*) FROM events WHERE session_id = 'ssn_thread' AND event_type = $1`, agenttools.EvThreadReplyRefused); n != 1 {
		t.Errorf("%d refusals on the session", n)
	}
	// An input from another workspace than the installation's is not posted either.
	th.says(t, "person", map[string]string{"text": "hi", "provider": "slack", "workspaceId": "T9", "conversationId": "C0SUPPORT", "threadId": th.root})
	if status, out := th.reply(t, `{"text":"hi"}`); status != 422 || !strings.Contains(fmt.Sprint(out["error"]), "no longer connected") {
		t.Errorf("another workspace's input: %d %v", status, out)
	}
	if n := len(th.fake.BotMessages()); n != 0 {
		t.Errorf("%d posts reached Slack", n)
	}
}

// The agent's text is escaped once, as every other reply: a disguised link
// or a person's mention arrives as literal text, in the text and the block.
func TestReplyInThreadEscapesTheAgentsMarkup(t *testing.T) {
	th := setupThread(t, true, nil)
	status, out := th.reply(t, `{"text":"Fixed in <https://evil.example|https://github.com/acme/api/pull/12> & <@U123> *now*"}`)
	if status != 200 {
		t.Fatalf("reply: %d %v", status, out)
	}
	want := "Fixed in &lt;https://evil.example|https://github.com/acme/api/pull/12&gt; &amp; &lt;@U123&gt; *now*"
	msgs := th.fake.BotMessages()
	if len(msgs) != 1 || msgs[0].Text != want {
		t.Fatalf("Slack got %+v", msgs)
	}
	var blocks []struct {
		Type string `json:"type"`
		Text struct {
			Text string `json:"text"`
		} `json:"text"`
	}
	if err := json.Unmarshal(msgs[0].Blocks, &blocks); err != nil || len(blocks) == 0 || blocks[0].Type != "section" || blocks[0].Text.Text != want {
		t.Fatalf("blocks %s (%v)", msgs[0].Blocks, err)
	}
}

func TestReplyInThreadRefusesABroadcastAndAnOverlongText(t *testing.T) {
	th := setupThread(t, true, nil)
	for _, b := range []string{"<!channel>", "<!here>", "<!everyone>", "<!here|here>", "<!subteam^S1|@eng>"} {
		status, out := th.reply(t, fmt.Sprintf(`{"text":"Heads up %s: it is fixed"}`, b))
		if status != 422 || !strings.Contains(fmt.Sprint(out["error"]), "never notifies a whole channel") {
			t.Errorf("%s: %d %v", b, status, out)
		}
	}
	status, out := th.reply(t, fmt.Sprintf(`{"text":%q}`, strings.Repeat("a", 3001)))
	if said := fmt.Sprint(out["error"]); status != 422 || !strings.Contains(said, "1 to 3000 characters; this one is 3001") || !strings.Contains(said, "summarise") {
		t.Errorf("3001 characters: %d %v", status, out)
	}
	if status, _ := th.reply(t, `{"text":"   "}`); status != 422 {
		t.Errorf("a blank reply: %d", status)
	}
	if n := len(th.fake.BotMessages()); n != 0 {
		t.Errorf("%d posts reached Slack", n)
	}
	if n := th.count(t, `SELECT count(*) FROM events WHERE session_id = 'ssn_thread' AND event_type = $1`, agenttools.EvThreadReplyRefused); n != 7 {
		t.Errorf("%d refusals recorded on the session, want 7", n)
	}
	// None of them spent the turn's budget.
	for i := range 3 {
		if status, out := th.reply(t, fmt.Sprintf(`{"text":"post %d"}`, i)); status != 200 {
			t.Fatalf("post %d after refusals: %d %v", i, status, out)
		}
	}
}

// Three posts a turn; the fourth is refused, and a person's next message
// starts a turn with three more. Counted from the session's events, so a
// restarted server counts the same.
func TestReplyInThreadPostsAtMostThreeATurn(t *testing.T) {
	th := setupThread(t, true, nil)
	th.personSays(t, "what time is it in Berlin?")
	for i := range 3 {
		if status, out := th.reply(t, fmt.Sprintf(`{"text":"answer %d"}`, i)); status != 200 {
			t.Fatalf("post %d: %d %v", i+1, status, out)
		}
	}
	status, out := th.reply(t, `{"text":"answer 4"}`)
	if status != 422 || !strings.Contains(fmt.Sprint(out["error"]), "at most 3 thread replies per turn") {
		t.Fatalf("the 4th post: %d %v", status, out)
	}
	if n := len(th.fake.BotMessages()); n != 3 {
		t.Fatalf("%d posts reached Slack, want 3", n)
	}
	// Only a person's input starts a turn: an agent's or dude's chat.message does not.
	th.says(t, "agent", map[string]string{"text": "still thinking"})
	th.says(t, "system", map[string]string{"text": "dude's note"})
	if status, out := th.reply(t, `{"text":"answer 5"}`); status != 422 || !strings.Contains(fmt.Sprint(out["error"]), "at most 3 thread replies per turn") {
		t.Fatalf("after an agent's message: %d %v", status, out)
	}
	if n := len(th.fake.BotMessages()); n != 3 {
		t.Fatalf("%d posts reached Slack, want 3", n)
	}
	th.personSays(t, "and in Lisbon?")
	if status, out := th.reply(t, `{"text":"21:15 in Lisbon"}`); status != 200 {
		t.Fatalf("a new turn's post: %d %v", status, out)
	}
	if n := len(th.fake.BotMessages()); n != 4 {
		t.Errorf("%d posts reached Slack, want 4", n)
	}
}

// Slack refusing the post: the agent is told why, the session shows the
// refusal, and the call still spent its place in the turn.
func TestReplyInThreadSlackRefusalIsRecordedAndSaid(t *testing.T) {
	th := setupThread(t, true, nil)
	th.fake.FailNext("chat.postMessage", "is_archived")
	status, out := th.reply(t, `{"text":"It is 22:15 in Berlin"}`)
	if said := fmt.Sprint(out["error"]); status != 422 || !strings.Contains(said, "the chat refused the reply") || !strings.Contains(said, "is_archived") {
		t.Fatalf("refused by Slack: %d %v", status, out)
	}
	if n := th.count(t, `SELECT count(*) FROM events WHERE session_id = 'ssn_thread' AND event_type = $1 AND payload->>'error' LIKE '%is_archived%'`,
		agenttools.EvThreadReplyRefused); n != 1 {
		t.Errorf("%d refusals on the session", n)
	}
	th.fake.FailNext("chat.postMessage", "ratelimited")
	if status, out := th.reply(t, `{"text":"again"}`); status != 422 || !strings.Contains(fmt.Sprint(out["error"]), "try again in a minute") {
		t.Errorf("rate limited: %d %v", status, out)
	}
	if n := len(th.fake.BotMessages()); n != 0 {
		t.Errorf("%d posts reached Slack", n)
	}
}

func TestReplyInThreadWithoutTheInstallationIsRefused(t *testing.T) {
	th := setupThread(t, false, nil)
	status, out := th.reply(t, `{"text":"It is 22:15 in Berlin"}`)
	if status != 422 || !strings.Contains(fmt.Sprint(out["error"]), "no longer connected") {
		t.Fatalf("no installation: %d %v", status, out)
	}
	if n := th.count(t, `SELECT count(*) FROM events WHERE session_id = 'ssn_thread' AND event_type = $1`, agenttools.EvThreadReplyRefused); n != 1 {
		t.Errorf("%d refusals on the session", n)
	}
	// Another workspace's installation is not the thread's.
	mustExec(t, th.owner, `INSERT INTO chat_installations (id, organization_id, provider, external_workspace_id, workspace_name,
		workspace_domain, bot_user_id, bot_token) VALUES ($1, $2, 'slack', 'T9', 'Other', 'other', 'UBT9', 'xoxb-other')`, "chi_"+th.org, th.org)
	if status, _ := th.reply(t, `{"text":"again"}`); status != 422 {
		t.Errorf("another workspace's installation: %d", status)
	}
}

// The provider is called with no transaction open: a probe holding the
// session's run row and the session row FOR UPDATE NOWAIT succeeds while
// Slack holds the post.
func TestReplyInThreadHoldsNoTransactionAcrossThePost(t *testing.T) {
	held := make(chan struct{})
	release := make(chan struct{})
	th := setupThreadWithHandler(t, true, nil, func(handler http.Handler) http.Handler {
		return holdPosts(handler, held, release)
	})
	f := th.fixture
	done := make(chan int, 1)
	go func() {
		status, _ := th.reply(t, `{"text":"It is 22:15"}`)
		done <- status
	}()
	select {
	case <-held:
	case <-time.After(10 * time.Second):
		t.Fatal("the post never reached Slack")
	}
	ctx := context.Background()
	var open int
	if err := f.owner.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity WHERE datname = current_database() AND pid <> pg_backend_pid()
		AND state LIKE 'idle in transaction%'`).Scan(&open); err != nil {
		t.Fatal(err)
	}
	locked := pgx.BeginFunc(ctx, f.owner, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT 1 FROM sessions WHERE id = 'ssn_thread' FOR UPDATE NOWAIT`); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('thread_replies:ssn_thread'))`)
		return err
	})
	close(release)
	if open != 0 || locked != nil {
		t.Errorf("while Slack held the post: %d transactions open, locking the session: %v", open, locked)
	}
	if status := <-done; status != 200 {
		t.Errorf("reply: %d", status)
	}
}

// toolNames is what GET /tools lists for token, sorted.
func toolNames(t *testing.T, f *fixture, token string) string {
	t.Helper()
	var names []string
	for name := range f.getTools(t, token) {
		names = append(names, name)
	}
	slices.Sort(names)
	return strings.Join(names, " ")
}

// holdPosts serves fake, holding each chat.postMessage until release is
// closed, after saying so on held.
func holdPosts(fake http.Handler, held chan<- struct{}, release <-chan struct{}) http.Handler {
	var once sync.Once
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/chat.postMessage") {
			once.Do(func() { close(held) })
			<-release
		}
		fake.ServeHTTP(w, r)
	})
}
