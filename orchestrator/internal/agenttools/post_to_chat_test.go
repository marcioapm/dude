package agenttools_test

import (
	"context"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/agenttools"
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
	fake := fakeslack.New("cid", "csecret")
	fake.AddTeam("T1", "Acme")
	fake.AddConversation(fakeslack.Conversation{ID: "C0ENGINEER", Team: "T1", Name: "engineering", Kind: "public", Members: []string{"UBT1"}})
	fake.AddConversation(fakeslack.Conversation{ID: "C0RANDOMXX", Team: "T1", Name: "random", Kind: "public"})
	fake.AddConversation(fakeslack.Conversation{ID: "G0SECRETXX", Team: "T1", Name: "hiring", Kind: "private"})
	srv := httptest.NewServer(fake.WithChannelList())
	t.Cleanup(srv.Close)
	p := slack.New(slack.Config{ClientID: "cid", ClientSecret: "csecret", APIURL: srv.URL + "/api"})
	f := setupWith(t, &agenttools.Server{Chat: agenttools.ChatProviders{"slack": p}})
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
		WHERE event_type = $1 AND payload->>'automationRunId' = 'arn_digest'`, agenttools.EvChatPosted).Scan(&link); err != nil || link != want {
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
	var said string
	if err := c.owner.QueryRow(context.Background(), `SELECT payload->>'error' FROM events WHERE event_type = $1
		AND payload->>'automationRunId' = 'arn_digest'`, agenttools.EvChatPostRefused).Scan(&said); err != nil ||
		!strings.HasPrefix(said, "Slack is not connected for Acme") {
		t.Errorf("the refusal on the run: %q %v", said, err)
	}
}

func TestPostToChatOutsideTheBotsChannelsSaysHowToInviteIt(t *testing.T) {
	c := setupChat(t, true)
	for _, tc := range []struct{ channel, want string }{
		{"#random", "dude's Slack bot is not in #random: ask someone in the channel to run /invite @dude"},
		{"C0RANDOMXX", "dude's Slack bot is not in #random: ask someone in the channel to run /invite @dude"},
		{"#hiring", "there is no channel #hiring that dude's Slack bot can see"},
		{"#nowhere", "there is no channel #nowhere that dude's Slack bot can see"},
		{"C0NOSUCHXX", "there is no channel C0NOSUCHXX that dude's Slack bot can see"},
	} {
		status, out := c.postToChat(t, tc.channel, "hello")
		if status != 422 || !strings.HasPrefix(fmt.Sprint(out["error"]), tc.want) {
			t.Errorf("%s: %d %v, want %q", tc.channel, status, out, tc.want)
		}
	}
	if n := len(c.fake.BotMessages()); n != 0 {
		t.Errorf("%d messages reached Slack", n)
	}
	// Each refused post is recorded on the run, with why.
	var refused int
	if err := c.owner.QueryRow(context.Background(), `SELECT count(*) FROM events WHERE event_type = $1
		AND payload->>'automationRunId' = 'arn_digest'`, agenttools.EvChatPostRefused).Scan(&refused); err != nil || refused != 5 {
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
	mustExec(t, c.owner, `UPDATE automations SET secrets = '{GH_TOKEN}' WHERE id = 'aut_digest'`)
	if status, out := c.postToChat(t, "#engineering", "Used ghp-digest-s3cr3t-value to read the stats"); status != 200 {
		t.Fatalf("post: %d %v", status, out)
	}
	msgs := c.fake.BotMessages()
	if len(msgs) != 1 || strings.Contains(msgs[0].Text, "ghp-digest-s3cr3t-value") || strings.Contains(string(msgs[0].Blocks), "ghp-digest-s3cr3t-value") ||
		!strings.Contains(msgs[0].Text, "to read the stats") {
		t.Fatalf("Slack got %+v", msgs)
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
	// At the limit it posts: past a block's 3,000 characters, as plain text.
	long := strings.Repeat("é", 40000)
	if status, out := c.postToChat(t, "#engineering", long); status != 200 {
		t.Fatalf("at the limit: %d %v", status, out)
	}
	if msgs := c.fake.BotMessages(); len(msgs) != 1 || msgs[0].Text != long {
		t.Fatalf("Slack got %d messages", len(msgs))
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
