package agenttools_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/marciomartins/dude/orchestrator/internal/agenttools"
	"github.com/marciomartins/dude/orchestrator/internal/chat"
	"github.com/marciomartins/dude/orchestrator/internal/chat/slack"
	"github.com/marciomartins/dude/orchestrator/internal/fakeslack"
)

// chatFixture is an automation run whose organisation may have Slack
// installed, against the fake Slack.
type chatFixture struct {
	*fixture
	fake  *fakeslack.Server
	token string
}

func setupChat(t *testing.T, installed bool) chatFixture {
	t.Helper()
	return setupChatWith(t, installed, chatOptions{})
}

// chatOptions vary setupChat: conversations.list pages of pageSize channels
// (0: as asked), more channels added before the fake serves, and the
// server's BeforeCall hook and outgoing chat.postMessage arguments.
type chatOptions struct {
	pageSize   int
	more       func(*fakeslack.Server)
	beforeCall func(tool string)
	postArgs   func(map[string]json.RawMessage)
}

func setupChatWith(t *testing.T, installed bool, o chatOptions) chatFixture {
	t.Helper()
	fake := fakeslack.New("cid", "csecret")
	fake.AddTeam("T1", "Acme")
	fake.AddConversation(fakeslack.Conversation{ID: "C0ENGINEER", Team: "T1", Name: "engineering", Kind: "public", Members: []string{"UBT1"}})
	fake.AddConversation(fakeslack.Conversation{ID: "C0RANDOMXX", Team: "T1", Name: "random", Kind: "public"})
	fake.AddConversation(fakeslack.Conversation{ID: "G0SECRETXX", Team: "T1", Name: "hiring", Kind: "private"})
	if o.more != nil {
		o.more(fake)
	}
	handler := fake.WithChannelPages(o.pageSize)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if o.postArgs != nil && r.URL.Path == "/api/chat.postMessage" {
			raw, err := io.ReadAll(r.Body)
			if err != nil {
				t.Error(err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(raw))
			var args map[string]json.RawMessage
			if err := json.Unmarshal(raw, &args); err != nil {
				t.Error(err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			o.postArgs(args)
		}
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	p := slack.New(slack.Config{ClientID: "cid", ClientSecret: "csecret", APIURL: srv.URL + "/api"})
	f := setupWith(t, &agenttools.Server{ChatProviders: chat.Registry{"slack": p}, BeforeCall: o.beforeCall})
	mustExec(t, f.owner, `UPDATE organizations SET name = 'Acme' WHERE id = $1`, f.org)
	if installed {
		mustExec(t, f.owner, `INSERT INTO chat_installations (id, organization_id, provider, external_workspace_id, workspace_name,
			workspace_domain, bot_user_id, bot_token) VALUES ($1, $2, 'slack', 'T1', 'Acme', 'acme', 'UBT1', $3)`,
			"chi_"+f.org, f.org, fake.Token("T1"))
	}
	person := "person_" + f.org
	mustExec(t, f.owner, `INSERT INTO people (id, organization_id, name, role) VALUES ($1,$2,'Ana','admin')`, person, f.org)
	mustExec(t, f.owner, `INSERT INTO automations (id, organization_id, project_id, name, kind, owner_id, trigger)
		VALUES ('aut_digest',$1,$2,'Weekly PR digest','agent',$3,'{"kind":"schedule","cron":"0 15 * * 5","tz":"UTC"}')`, f.org, f.project, person)
	token, hash := agenttools.NewToken()
	scope := agenttools.AutomationScope{ProjectID: f.project, OwnerID: person, Repos: []string{}}
	mustExec(t, f.owner, `INSERT INTO automation_runs (id, organization_id, automation_id, status, source, mcp_token_hash, tools_scope)
		VALUES ('arn_digest',$1,'aut_digest','running','schedule',$2,$3)`, f.org, hash, scope)
	return chatFixture{fixture: f, fake: fake, token: token}
}

func (c chatFixture) postToChat(t *testing.T, channel, text string) (int, map[string]any) {
	t.Helper()
	return c.post(t, c.token, "post_to_chat", fmt.Sprintf(`{"channel":%q,"text":%q}`, channel, text))
}

func TestPostToChatPostsToAChannelByNameAndAnswersItsPermalink(t *testing.T) {
	c := setupChat(t, true)
	status, out := c.postToChat(t, "#engineering", "*Weekly digest*: 12 merged")
	if status != 200 {
		t.Fatalf("post: %d %v", status, out)
	}
	msgs := c.fake.BotMessages()
	if len(msgs) != 1 || msgs[0].Channel != "C0ENGINEER" || msgs[0].Text != "*Weekly digest*: 12 merged" || msgs[0].ThreadTS != msgs[0].TS {
		t.Fatalf("Slack got %+v", msgs)
	}
	want := "https://acme.slack.com/archives/C0ENGINEER/p" + strings.ReplaceAll(msgs[0].TS, ".", "")
	if out["channel"] != "#engineering" || out["ts"] != msgs[0].TS || out["permalink"] != want {
		t.Errorf("answer %v, want #engineering, %s, %s", out, msgs[0].TS, want)
	}
	// The post is on the run's ledger, with its link, for the run's log.
	var link string
	if err := c.owner.QueryRow(context.Background(), `SELECT payload->>'permalink' FROM events
		WHERE event_type = $1 AND payload->>'automationRunId' = 'arn_digest'`, agenttools.EvSlackPosted).Scan(&link); err != nil || link != want {
		t.Errorf("ledger link %q %v", link, err)
	}
}

func TestPostToChatTakesAChannelByNameWithoutTheHashOrByID(t *testing.T) {
	c := setupChat(t, true)
	if status, out := c.postToChat(t, "engineering", "by name"); status != 200 {
		t.Fatalf("by name: %d %v", status, out)
	}
	status, out := c.postToChat(t, "C0ENGINEER", "by id")
	if status != 200 {
		t.Fatalf("by id: %d %v", status, out)
	}
	if out["channel"] != "#engineering" {
		t.Errorf("by id answers channel %v", out["channel"])
	}
	msgs := c.fake.BotMessages()
	if len(msgs) != 2 || msgs[0].Channel != "C0ENGINEER" || msgs[1].Channel != "C0ENGINEER" || msgs[1].Text != "by id" {
		t.Fatalf("Slack got %+v", msgs)
	}
}

func TestPostToChatWithoutSlackSaysItIsNotConnected(t *testing.T) {
	c := setupChat(t, false)
	status, out := c.postToChat(t, "#engineering", "hello")
	if status != 422 || !strings.HasPrefix(fmt.Sprint(out["error"]), "Slack is not connected for Acme") {
		t.Fatalf("no installation: %d %v", status, out)
	}
	if n := len(c.fake.BotMessages()); n != 0 {
		t.Errorf("%d messages reached Slack", n)
	}
	var said, code, reason string
	if err := c.owner.QueryRow(context.Background(), `SELECT payload->>'error', payload->>'code', payload->>'reason' FROM events WHERE event_type = $1
		AND payload->>'automationRunId' = 'arn_digest'`, agenttools.EvSlackPostRefused).Scan(&said, &code, &reason); err != nil ||
		!strings.HasPrefix(said, "Slack is not connected for Acme") || code != agenttools.PostNotConnected || reason != "Slack is not connected for Acme" {
		t.Errorf("the refusal on the run: %q %q %q %v", said, code, reason, err)
	}
	// The call's own record names that refusal, so a reader lists the call once.
	var refusals, records int
	if err := c.owner.QueryRow(context.Background(), `SELECT
		  (SELECT count(*) FROM events WHERE event_type = $1 AND automation_run_id = 'arn_digest'),
		  (SELECT count(*) FROM events d WHERE d.event_type = $2 AND d.automation_run_id = 'arn_digest'
		     AND d.payload->>'refusalEventId' = (SELECT id FROM events WHERE event_type = $1 AND automation_run_id = 'arn_digest'))`,
		agenttools.EvSlackPostRefused, agenttools.EventType).Scan(&refusals, &records); err != nil || refusals != 1 || records != 1 {
		t.Errorf("refusals %d, records naming it %d: %v", refusals, records, err)
	}
}

func TestPostToChatOutsideTheBotsChannelsSaysHowToInviteIt(t *testing.T) {
	c := setupChat(t, true)
	// posts is how many chat.postMessage calls a channel's refusal may make:
	// a channel by name is looked up first, so a refused one never reaches
	// chat.postMessage (which, in real Slack, takes a name and would post).
	for _, tc := range []struct {
		channel, want string
		posts         int
	}{
		{"#random", "dude's Slack bot is not in #random: ask someone in the channel to run /invite @dude", 0},
		{"C0RANDOMXX", "dude's Slack bot is not in #random: ask someone in the channel to run /invite @dude", 1},
		{"#hiring", "there is no channel #hiring that dude's Slack bot can see", 0},
		{"#nowhere", "there is no channel #nowhere that dude's Slack bot can see", 0},
		{"C0NOSUCHXX", "there is no channel C0NOSUCHXX that dude's Slack bot can see", 0},
	} {
		before := c.fake.Calls("chat.postMessage")
		status, out := c.postToChat(t, tc.channel, "hello")
		if status != 422 || !strings.HasPrefix(fmt.Sprint(out["error"]), tc.want) {
			t.Errorf("%s: %d %v, want %q", tc.channel, status, out, tc.want)
		}
		if n := c.fake.Calls("chat.postMessage") - before; n != tc.posts {
			t.Errorf("%s: %d chat.postMessage calls, want %d", tc.channel, n, tc.posts)
		}
	}
	if n := len(c.fake.BotMessages()); n != 0 {
		t.Errorf("%d messages reached Slack", n)
	}
	// Each refused post is recorded on the run, with why.
	var refused int
	if err := c.owner.QueryRow(context.Background(), `SELECT count(*) FROM events WHERE event_type = $1
		AND payload->>'automationRunId' = 'arn_digest'`, agenttools.EvSlackPostRefused).Scan(&refused); err != nil || refused != 5 {
		t.Errorf("refusals recorded: %d %v", refused, err)
	}
}

func TestPostToChatRefusesTheEleventhPostOfARun(t *testing.T) {
	c := setupChat(t, true)
	for i := range 10 {
		if status, out := c.postToChat(t, "#engineering", fmt.Sprintf("post %d", i+1)); status != 200 {
			t.Fatalf("post %d: %d %v", i+1, status, out)
		}
	}
	status, out := c.postToChat(t, "#engineering", "post 11")
	if status != 422 || !strings.HasPrefix(fmt.Sprint(out["error"]), "post_to_chat is limited to 10 messages per run") {
		t.Fatalf("11th: %d %v", status, out)
	}
	if n := len(c.fake.BotMessages()); n != 10 {
		t.Errorf("%d messages reached Slack, want 10", n)
	}
}

// Only a call that reached chat.postMessage spends one of the run's ten: a
// channel the lookup refused, or no Slack at all, spends none.
func TestPostToChatLookupRefusalsSpendNoPost(t *testing.T) {
	c := setupChat(t, true)
	for i := range 10 {
		if status, out := c.postToChat(t, "#nowhere", fmt.Sprintf("guess %d", i)); status != 422 {
			t.Fatalf("guess %d: %d %v", i, status, out)
		}
	}
	if status, _ := c.postToChat(t, "C0NOSUCHXX", "by an id Slack does not have"); status != 422 {
		t.Fatalf("unknown id: %d", status)
	}
	for i := range 10 {
		if status, out := c.postToChat(t, "#engineering", fmt.Sprintf("post %d", i+1)); status != 200 {
			t.Fatalf("post %d after the refused lookups: %d %v", i+1, status, out)
		}
	}
	if n := len(c.fake.BotMessages()); n != 10 {
		t.Errorf("%d messages reached Slack, want 10", n)
	}
}

// A lookup by name that reaches its page bound says so and asks for the id,
// posting nothing and spending none of the run's ten; the id then posts.
func TestPostToChatAskedForTheIDWhenTheLookupCannotFinish(t *testing.T) {
	c := setupChatWith(t, true, chatOptions{pageSize: 1, more: func(f *fakeslack.Server) {
		for i := range 60 {
			f.AddConversation(fakeslack.Conversation{ID: fmt.Sprintf("C1%08d", i), Team: "T1", Name: fmt.Sprintf("team-%02d", i), Kind: "public",
				Members: []string{"UBT1"}})
		}
	}})
	status, out := c.postToChat(t, "#team-59", "hello")
	if status != 422 || !strings.HasPrefix(fmt.Sprint(out["error"]), "could not finish looking up the channel #team-59; pass the channel id") {
		t.Fatalf("past the bound: %d %v", status, out)
	}
	if n := c.fake.Calls("chat.postMessage"); n != 0 {
		t.Errorf("%d posts", n)
	}
	if status, out := c.postToChat(t, "C100000059", "hello"); status != 200 || out["channel"] != "#team-59" {
		t.Fatalf("by id: %d %v", status, out)
	}
}

// A post Slack refuses still spends one of the run's ten.
func TestPostToChatCountsAPostSlackRefused(t *testing.T) {
	c := setupChat(t, true)
	c.fake.FailNext("chat.postMessage", "msg_too_long")
	if status, out := c.postToChat(t, "#engineering", "refused"); status != 422 || !strings.Contains(fmt.Sprint(out["error"]), "msg_too_long") {
		t.Fatalf("refused by Slack: %d %v", status, out)
	}
	for i := range 9 {
		if status, out := c.postToChat(t, "#engineering", fmt.Sprintf("post %d", i)); status != 200 {
			t.Fatalf("post %d: %d %v", i, status, out)
		}
	}
	if status, _ := c.postToChat(t, "#engineering", "one more"); status != 422 {
		t.Fatalf("the tenth after a refused one: %d", status)
	}
}

func TestPostToChatRedactsTheRunsSecretValues(t *testing.T) {
	c := setupChat(t, true)
	mustExec(t, c.owner, `INSERT INTO project_secrets (organization_id, project_id, name, value, hint) VALUES ($1, $2, 'GH_TOKEN', 'ghp-digest-s3cr3t-value', 'alue')`,
		c.org, c.project)
	mustExec(t, c.owner, `UPDATE automations SET secrets = '[{"source":"project","name":"GH_TOKEN"}]' WHERE id = 'aut_digest'`)
	if status, out := c.postToChat(t, "#engineering", "Used ghp-digest-s3cr3t-value to read the stats"); status != 200 {
		t.Fatalf("post: %d %v", status, out)
	}
	msgs := c.fake.BotMessages()
	if len(msgs) != 1 || strings.Contains(msgs[0].Text, "ghp-digest-s3cr3t-value") || strings.Contains(string(msgs[0].Blocks), "ghp-digest-s3cr3t-value") ||
		!strings.Contains(msgs[0].Text, "to read the stats") {
		t.Fatalf("Slack got %+v", msgs)
	}
	// A channel holding the value is redacted in the prepare step too: the
	// refusal names the marker, never the value.
	status, out := c.postToChat(t, "#ghp-digest-s3cr3t-value", "hello")
	if said := fmt.Sprint(out["error"]); status != 422 || !strings.Contains(said, "#[REDACTED:GH_TOKEN]") || strings.Contains(said, "ghp-digest-s3cr3t-value") {
		t.Errorf("a channel holding the secret: %d %v", status, out)
	}
	var leaked int
	if err := c.owner.QueryRow(context.Background(), `SELECT count(*) FROM events WHERE organization_id = $1 AND payload::text LIKE '%ghp-digest-s3cr3t-value%'`,
		c.org).Scan(&leaked); err != nil || leaked != 0 {
		t.Errorf("ledger events carrying the secret: %d %v", leaked, err)
	}
}

func TestPostToChatRefusesTextOverSlacksLimit(t *testing.T) {
	c := setupChat(t, true)
	status, out := c.postToChat(t, "#engineering", strings.Repeat("é", 40001))
	if status != 422 || !strings.HasPrefix(fmt.Sprint(out["error"]), "text is 1 to 40000 characters of Slack mrkdwn; this one is 40001") {
		t.Fatalf("over the limit: %d %v", status, out)
	}
	if status, out := c.postToChat(t, "#engineering", "   "); status != 422 {
		t.Fatalf("blank: %d %v", status, out)
	}
	if n := len(c.fake.BotMessages()); n != 0 {
		t.Errorf("%d messages reached Slack", n)
	}
	// At Slack's message text limit it still posts without section blocks.
	long := strings.Repeat("é", 40000)
	if status, out := c.postToChat(t, "#engineering", long); status != 200 {
		t.Fatalf("at the limit: %d %v", status, out)
	}
	if msgs := c.fake.BotMessages(); len(msgs) != 1 || msgs[0].Text != long {
		t.Fatalf("Slack got %d messages", len(msgs))
	}
}

// An installation Slack no longer honours is not connected: the post is
// refused with the code the run fails on, and the failure is on the
// installation's health as any chat call's is, whichever call met it.
func TestPostToChatWithARevokedInstallationSaysItIsNotConnected(t *testing.T) {
	for _, tc := range []struct{ code, method, channel string }{
		{"token_revoked", "chat.postMessage", "#engineering"},
		{"invalid_auth", "conversations.list", "#engineering"},
		{"account_inactive", "conversations.info", "C0ENGINEER"},
	} {
		t.Run(tc.code, func(t *testing.T) {
			c := setupChat(t, true)
			c.fake.FailNext(tc.method, tc.code)
			status, out := c.postToChat(t, tc.channel, "hello")
			if status != 422 || !strings.HasPrefix(fmt.Sprint(out["error"]), "Slack is not connected for Acme") {
				t.Fatalf("%s on %s: %d %v", tc.code, tc.method, status, out)
			}
			var code, reason string
			if err := c.owner.QueryRow(context.Background(), `SELECT payload->>'code', payload->>'reason' FROM events WHERE event_type = $1
				AND automation_run_id = 'arn_digest'`, agenttools.EvSlackPostRefused).Scan(&code, &reason); err != nil ||
				code != agenttools.PostNotConnected || reason != "Slack is not connected for Acme" {
				t.Errorf("the refusal on the run: %q %q %v", code, reason, err)
			}
			var failed int
			var last string
			if err := c.owner.QueryRow(context.Background(), `SELECT failed_today, COALESCE(last_failure, '') FROM chat_installations
				WHERE organization_id = $1`, c.org).Scan(&failed, &last); err != nil || failed != 1 || !strings.Contains(last, tc.code) {
				t.Errorf("the installation's health: %d %q %v", failed, last, err)
			}
		})
	}
}

// Any other Slack failure is on the installation's health too.
func TestPostToChatCountsSlacksRefusalOnTheInstallationsHealth(t *testing.T) {
	c := setupChat(t, true)
	c.fake.FailNext("chat.postMessage", "msg_too_long")
	if status, _ := c.postToChat(t, "#engineering", "refused"); status != 422 {
		t.Fatalf("refused by Slack: %d", status)
	}
	var last string
	if err := c.owner.QueryRow(context.Background(), `SELECT COALESCE(last_failure, '') FROM chat_installations WHERE organization_id = $1`,
		c.org).Scan(&last); err != nil || !strings.Contains(last, "msg_too_long") {
		t.Errorf("the installation's last failure: %q %v", last, err)
	}
}

// A channel given by id whose lookup fails for another reason than "not
// found" is still posted to, shown as the id it was given (no "#").
func TestPostToChatByIDWhoseLookupFailedShowsTheID(t *testing.T) {
	c := setupChat(t, true)
	c.fake.FailNext("conversations.info", "missing_scope")
	status, out := c.postToChat(t, "C0ENGINEER", "by id")
	if status != 200 || out["channel"] != "C0ENGINEER" || out["channelId"] != "C0ENGINEER" {
		t.Fatalf("by id, its name unread: %d %v", status, out)
	}
}

// The limit is on the agent's text: a secret value's marker, shorter than
// the value, does not bring an over-long text under it.
func TestPostToChatMeasuresTheTextBeforeRedactingIt(t *testing.T) {
	c := setupChat(t, true)
	mustExec(t, c.owner, `INSERT INTO project_secrets (organization_id, project_id, name, value, hint) VALUES ($1, $2, 'GH_TOKEN', 'ghp-digest-s3cr3t-value', 'alue')`,
		c.org, c.project)
	mustExec(t, c.owner, `UPDATE automations SET secrets = '[{"source":"project","name":"GH_TOKEN"}]' WHERE id = 'aut_digest'`)
	text := "ghp-digest-s3cr3t-value " + strings.Repeat("x", 40001-24)
	status, out := c.postToChat(t, "#engineering", text)
	if status != 422 || !strings.HasPrefix(fmt.Sprint(out["error"]), "text is 1 to 40000 characters of Slack mrkdwn; this one is 40001") {
		t.Fatalf("40,001 characters, a secret among them: %d %v", status, out)
	}
	if n := c.fake.Calls("chat.postMessage"); n != 0 {
		t.Errorf("%d posts", n)
	}
}

// Calls racing for the run's tenth post: held together just before their
// transactions open (BeforeCall), so each passed the prepare step's count
// of 9; the run's row lock lets exactly one of them post.
func TestPostToChatConcurrentCallsTakeTheTenthPostOnce(t *testing.T) {
	const racers = 5
	var armed atomic.Bool
	var arrived atomic.Int32
	release := make(chan struct{})
	c := setupChatWith(t, true, chatOptions{beforeCall: func(tool string) {
		if tool != "post_to_chat" || !armed.Load() {
			return
		}
		if arrived.Add(1) == racers {
			close(release)
		}
		select {
		case <-release:
		case <-time.After(10 * time.Second):
		}
	}})
	for i := range 9 {
		if status, out := c.postToChat(t, "#engineering", fmt.Sprintf("post %d", i+1)); status != 200 {
			t.Fatalf("post %d: %d %v", i+1, status, out)
		}
	}
	armed.Store(true)
	statuses := make([]int, racers)
	var wg sync.WaitGroup
	for i := range racers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			statuses[i], _ = c.postToChat(t, "#engineering", fmt.Sprintf("racer %d", i))
		}()
	}
	wg.Wait()
	if arrived.Load() != racers {
		t.Fatalf("%d of %d calls reached the barrier", arrived.Load(), racers)
	}
	ok := 0
	for _, s := range statuses {
		if s == 200 {
			ok++
		}
	}
	if ok != 1 {
		t.Errorf("%d of the racing calls posted (%v), want 1", ok, statuses)
	}
	if n := len(c.fake.BotMessages()); n != 10 {
		t.Errorf("%d messages reached Slack, want 10", n)
	}
}

// Intentional mrkdwn is posted without section blocks at every message length.
func TestPostToChatPostsTextWithoutBlocks(t *testing.T) {
	c := setupChat(t, true)
	long := strings.Repeat("é", 3001)
	if status, out := c.postToChat(t, "#engineering", long); status != 200 {
		t.Fatalf("3,001 characters: %d %v", status, out)
	}
	if status, out := c.postToChat(t, "#engineering", "short"); status != 200 {
		t.Fatalf("short: %d %v", status, out)
	}
	msgs := c.fake.BotMessages()
	if len(msgs) != 2 || msgs[0].Text != long || len(msgs[0].Blocks) != 0 && string(msgs[0].Blocks) != "null" {
		t.Fatalf("the long post: %d messages, blocks %.80s", len(msgs), msgs[0].Blocks)
	}
	if msgs[1].Text != "short" || len(msgs[1].Blocks) != 0 && string(msgs[1].Blocks) != "null" {
		t.Errorf("the short post: text %q, blocks %s", msgs[1].Text, msgs[1].Blocks)
	}
}

func TestPostToChatPreservesIntentionalMrkdwnLinksAtEveryLength(t *testing.T) {
	const link = "<https://github.com/acme/repo/pull/1|repo#1>"
	for _, length := range []int{len(link), 3000, 3001} {
		t.Run(fmt.Sprint(length), func(t *testing.T) {
			text := link + strings.Repeat("x", length-len(link))
			var posts atomic.Int32
			c := setupChatWith(t, true, chatOptions{postArgs: func(args map[string]json.RawMessage) {
				posts.Add(1)
				var got string
				if err := json.Unmarshal(args["text"], &got); err != nil || got != text {
					t.Errorf("intentional labelled link not preserved: text %.100q, want %.100q: %v", got, text, err)
				}
				if string(args["mrkdwn"]) != "true" || string(args["unfurl_links"]) != "false" {
					t.Errorf("Slack options: mrkdwn=%s unfurl_links=%s", args["mrkdwn"], args["unfurl_links"])
				}
				if blocks := args["blocks"]; len(blocks) != 0 && string(blocks) != "null" {
					t.Errorf("unexpected blocks: %.100s", blocks)
				}
			}})
			if status, out := c.postToChat(t, "#engineering", text); status != 200 {
				t.Fatalf("post: %d %v", status, out)
			}
			msgs := c.fake.BotMessages()
			if posts.Load() != 1 || len(msgs) != 1 {
				t.Fatalf("Slack received %d requests and %d messages", posts.Load(), len(msgs))
			}
			if msgs[0].Text != text || msgs[0].Channel != "C0ENGINEER" || msgs[0].ThreadTS != msgs[0].TS {
				t.Errorf("Slack message: channel %q, thread %q, ts %q, text %.100q", msgs[0].Channel, msgs[0].ThreadTS, msgs[0].TS, msgs[0].Text)
			}
		})
	}
}

// A transient Slack failure (rate-limited) says to try again, and the
// attempt still counts.
func TestPostToChatSaysToTryAgainWhenSlackIsBusy(t *testing.T) {
	c := setupChat(t, true)
	c.fake.FailRetryAfter("chat.postMessage", "ratelimited", 5, 1)
	status, out := c.postToChat(t, "#engineering", "busy")
	if status != 422 || !strings.HasPrefix(fmt.Sprint(out["error"]), "Slack did not take the post (") ||
		!strings.HasSuffix(fmt.Sprint(out["error"]), "nothing was posted; try again in a minute") {
		t.Fatalf("rate-limited: %d %v", status, out)
	}
	var attempts int
	if err := c.owner.QueryRow(context.Background(), `SELECT count(*) FROM events WHERE automation_run_id = 'arn_digest' AND event_type = $1
		AND payload->>'tool' = 'post_to_chat'`, agenttools.AttemptEventType).Scan(&attempts); err != nil || attempts != 1 {
		t.Errorf("attempts recorded: %d %v", attempts, err)
	}
}

// A post Slack refuses because the bot is no longer in the channel, or the
// channel is gone, drops the remembered lookup: the next call looks the
// channel up again rather than spending another post on the stale id.
func TestPostToChatLooksTheChannelUpAgainAfterSlackSaysItIsGone(t *testing.T) {
	for _, code := range []string{"not_in_channel", "channel_not_found"} {
		t.Run(code, func(t *testing.T) {
			c := setupChat(t, true)
			if status, out := c.postToChat(t, "#engineering", "first"); status != 200 {
				t.Fatalf("first: %d %v", status, out)
			}
			c.fake.FailNext("chat.postMessage", code)
			if status, _ := c.postToChat(t, "#engineering", "refused"); status != 422 {
				t.Fatalf("%s: %d", code, status)
			}
			if status, out := c.postToChat(t, "#engineering", "again"); status != 200 {
				t.Fatalf("again: %d %v", status, out)
			}
			if n := c.fake.Calls("conversations.list"); n != 2 {
				t.Errorf("%d lookups, want 2 (the first, and one after Slack's %s)", n, code)
			}
		})
	}
}

// A channel the lookup refused writes no call, but still counts toward the
// run's 60 calls a minute: the 61st is refused before Slack is asked.
func TestPostToChatLookupRefusalsCountTowardTheMinutesCalls(t *testing.T) {
	c := setupChat(t, true)
	for i := range 60 {
		if status, out := c.postToChat(t, "#nowhere", fmt.Sprintf("guess %d", i)); status != 422 ||
			!strings.HasPrefix(fmt.Sprint(out["error"]), "there is no channel #nowhere") {
			t.Fatalf("guess %d: %d %v", i, status, out)
		}
	}
	before := c.fake.Calls("conversations.list")
	status, out := c.postToChat(t, "#nowhere", "one more")
	if status != 422 || !strings.HasPrefix(fmt.Sprint(out["error"]), "too many calls: at most 60 a minute") {
		t.Fatalf("the 61st: %d %v", status, out)
	}
	if n := c.fake.Calls("conversations.list") - before; n != 0 {
		t.Errorf("the 61st looked the channel up: %d conversations.list calls", n)
	}
}

// Only lookup refusals are added to the minute's calls: a post Slack refused
// is counted once, by its attempt, not again by its refusal.
func TestPostToChatCountsAPostSlackRefusedOnceTowardTheMinute(t *testing.T) {
	c := setupChat(t, true)
	for i := range 30 {
		mustExec(t, c.owner, `INSERT INTO events (id, organization_id, event_type, actor_type, actor_id, source, automation_run_id, payload) VALUES
			($1, $3, $4, 'agent', 'arn_digest', 'orchestrator', 'arn_digest', '{"tool":"github_comment"}'),
			($2, $3, $5, 'agent', 'arn_digest', 'orchestrator', 'arn_digest', '{"tool":"post_to_chat","channel":"#engineering"}')`,
			fmt.Sprintf("evt_att%02d", i), fmt.Sprintf("evt_ref%02d", i), c.org, agenttools.AttemptEventType, agenttools.EvSlackPostRefused)
	}
	if status, out := c.postToChat(t, "#engineering", "30 calls this minute"); status != 200 {
		t.Fatalf("after 30 calls and their 30 refusals: %d %v", status, out)
	}
}

// Calls past the run's ten posts are refused, and recorded, until they have
// spent the run's 60 calls a minute; past those they write nothing.
func TestPostToChatRefusedPastTenPostsSpendTheMinutesCalls(t *testing.T) {
	c := setupChat(t, true)
	for i := range 10 {
		if status, out := c.postToChat(t, "#engineering", fmt.Sprintf("post %d", i+1)); status != 200 {
			t.Fatalf("post %d: %d %v", i+1, status, out)
		}
	}
	perRun, limited := 0, 0
	for i := range 120 {
		status, out := c.postToChat(t, "#engineering", fmt.Sprintf("more %d", i))
		said := fmt.Sprint(out["error"])
		switch {
		case status == 422 && strings.HasPrefix(said, "post_to_chat is limited to 10 messages per run"):
			perRun++
		case status == 422 && strings.HasPrefix(said, "too many calls: at most 60 a minute"):
			limited++
		default:
			t.Fatalf("call %d past the ten: %d %v", i, status, out)
		}
	}
	var refusals, records int
	if err := c.owner.QueryRow(context.Background(), `SELECT
		count(*) FILTER (WHERE event_type = $1),
		count(*) FILTER (WHERE event_type = $2 AND payload->>'attempted' = 'true' AND payload ? 'error')
		FROM events WHERE automation_run_id = 'arn_digest' AND occurred_at > now() - interval '1 minute'`,
		agenttools.EvSlackPostRefused, agenttools.EventType).Scan(&refusals, &records); err != nil {
		t.Fatal(err)
	}
	if perRun+10 > 60 || limited != 120-perRun || refusals != perRun || records != perRun {
		t.Fatalf("120 calls past the ten: %d refused per run, %d for the budget; %d refusals and %d records written",
			perRun, limited, refusals, records)
	}
	if n := len(c.fake.BotMessages()); n != 10 {
		t.Errorf("%d messages reached Slack, want 10", n)
	}
}

// Only an automation's agent has the tool.
func TestPostToChatIsAnAutomationsTool(t *testing.T) {
	c := setupChat(t, true)
	token := c.run(t, "run_impl", "implementer", "running")
	if status, _ := c.post(t, token, "post_to_chat", `{"channel":"#engineering","text":"hi"}`); status != 404 {
		t.Fatalf("an implementer's call: %d", status)
	}
}
