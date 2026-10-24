package slack_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/marciomartins/dude/orchestrator/internal/chat"
	"github.com/marciomartins/dude/orchestrator/internal/chat/slack"
	"github.com/marciomartins/dude/orchestrator/internal/fakeslack"
)

const secret = "signing-secret"

func signed(t *testing.T, body string, at time.Time) http.Header {
	t.Helper()
	req, err := fakeslack.SignedRequest("http://x/", secret, "application/json", []byte(body), at)
	if err != nil {
		t.Fatal(err)
	}
	return req.Header
}

func TestVerifyRequestRefusesAForgedStaleOrUnsignedRequest(t *testing.T) {
	p := slack.New(slack.Config{SigningSecret: secret})
	now := time.Unix(1_800_000_000, 0)
	body := `{"type":"event_callback"}`
	cases := []struct {
		name   string
		header func() http.Header
		ok     bool
	}{
		{"signed now", func() http.Header { return signed(t, body, now) }, true},
		{"signed 4 minutes ago", func() http.Header { return signed(t, body, now.Add(-4*time.Minute)) }, true},
		{"signed 6 minutes ago: a replay", func() http.Header { return signed(t, body, now.Add(-6*time.Minute)) }, false},
		{"signed 6 minutes ahead", func() http.Header { return signed(t, body, now.Add(6*time.Minute)) }, false},
		{"another body", func() http.Header { return signed(t, body+" ", now) }, false},
		{"no signature", func() http.Header {
			h := signed(t, body, now)
			h.Del("X-Slack-Signature")
			return h
		}, false},
		{"no timestamp", func() http.Header {
			h := signed(t, body, now)
			h.Del("X-Slack-Request-Timestamp")
			return h
		}, false},
		{"another secret", func() http.Header {
			req, _ := fakeslack.SignedRequest("http://x/", "other", "application/json", []byte(body), now)
			return req.Header
		}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := p.VerifyRequest(c.header(), []byte(body), now)
			if c.ok != (err == nil) {
				t.Fatalf("ok=%v, err=%v", c.ok, err)
			}
			if err != nil && !errors.Is(err, chat.ErrUnverified) {
				t.Fatalf("err %v is not ErrUnverified", err)
			}
		})
	}
	if err := slack.New(slack.Config{}).VerifyRequest(signed(t, body, now), []byte(body), now); err == nil {
		t.Fatal("verified with no signing secret configured")
	}
}

func TestParseEventReadsMentionsDMsAndTheAppsRemoval(t *testing.T) {
	p := slack.New(slack.Config{})
	raw := func(v map[string]any) []byte {
		b, _ := jsonMarshal(v)
		return b
	}
	mention := fakeslack.Mention("T1", "Ev1", "C1", "", "1.1", "U1", "file a task please")
	inThread := fakeslack.Mention("T1", "Ev2", "C1", "1.0", "1.2", "U1", "and this")
	dm := fakeslack.DirectMessage("T1", "Ev3", "D1", "1.3", "U1", "start a session")
	botDM := fakeslack.DirectMessage("T1", "Ev4", "D1", "1.4", "U1", "x")
	botDM["event"].(map[string]any)["bot_id"] = "B1"
	edited := fakeslack.DirectMessage("T1", "Ev5", "D1", "1.5", "U1", "x")
	edited["event"].(map[string]any)["subtype"] = "message_changed"
	channelMsg := fakeslack.DirectMessage("T1", "Ev6", "C1", "1.6", "U1", "x")
	channelMsg["event"].(map[string]any)["channel_type"] = "channel"
	uninstalled := map[string]any{"type": "event_callback", "team_id": "T1", "event_id": "Ev7", "event": map[string]any{"type": "app_uninstalled"}}
	revoked := map[string]any{"type": "event_callback", "team_id": "T1", "event_id": "Ev8", "event": map[string]any{"type": "tokens_revoked",
		"tokens": map[string]any{"oauth": []string{"U2"}, "bot": []string{"UBT1"}}}}
	userRevoked := map[string]any{"type": "event_callback", "team_id": "T1", "event_id": "Ev9", "event": map[string]any{"type": "tokens_revoked",
		"tokens": map[string]any{"oauth": []string{"U2"}}}}

	cases := []struct {
		name string
		body []byte
		want chat.Event
	}{
		{"url verification", []byte(`{"type":"url_verification","challenge":"abc"}`), chat.Event{Kind: chat.EventURLVerification, Challenge: "abc"}},
		{"app mention starts a thread", raw(mention), chat.Event{Kind: chat.EventMention, ID: "Ev1", WorkspaceID: "T1", Mention: &chat.Mention{
			Provider: "slack", WorkspaceID: "T1", EventID: "Ev1", ConversationID: "C1", ThreadID: "1.1", MessageID: "1.1", UserID: "U1",
			Text: "file a task please"}}},
		{"app mention in a thread", raw(inThread), chat.Event{Kind: chat.EventMention, ID: "Ev2", WorkspaceID: "T1", Mention: &chat.Mention{
			Provider: "slack", WorkspaceID: "T1", EventID: "Ev2", ConversationID: "C1", ThreadID: "1.0", MessageID: "1.2", UserID: "U1",
			Text: "and this"}}},
		{"a DM", raw(dm), chat.Event{Kind: chat.EventMention, ID: "Ev3", WorkspaceID: "T1", Mention: &chat.Mention{
			Provider: "slack", WorkspaceID: "T1", EventID: "Ev3", ConversationID: "D1", ThreadID: "1.3", MessageID: "1.3", UserID: "U1",
			Text: "start a session", Direct: true}}},
		{"a bot's DM", raw(botDM), chat.Event{Kind: chat.EventIgnored, ID: "Ev4", WorkspaceID: "T1"}},
		{"an edit", raw(edited), chat.Event{Kind: chat.EventIgnored, ID: "Ev5", WorkspaceID: "T1"}},
		{"a channel message", raw(channelMsg), chat.Event{Kind: chat.EventIgnored, ID: "Ev6", WorkspaceID: "T1"}},
		{"uninstalled", raw(uninstalled), chat.Event{Kind: chat.EventUninstalled, ID: "Ev7", WorkspaceID: "T1"}},
		{"tokens revoked", raw(revoked), chat.Event{Kind: chat.EventTokensRevoked, ID: "Ev8", WorkspaceID: "T1",
			RevokedBots: []string{"UBT1"}, RevokedUsers: []string{"U2"}}},
		{"a person's token revoked", raw(userRevoked), chat.Event{Kind: chat.EventTokensRevoked, ID: "Ev9", WorkspaceID: "T1",
			RevokedUsers: []string{"U2"}}},
		{"another envelope", []byte(`{"type":"app_rate_limited"}`), chat.Event{Kind: chat.EventIgnored}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := p.ParseEvent(c.body)
			if err != nil {
				t.Fatal(err)
			}
			if got.Kind != c.want.Kind || got.ID != c.want.ID || got.WorkspaceID != c.want.WorkspaceID || got.Challenge != c.want.Challenge ||
				strings.Join(got.RevokedBots, ",") != strings.Join(c.want.RevokedBots, ",") ||
				strings.Join(got.RevokedUsers, ",") != strings.Join(c.want.RevokedUsers, ",") {
				t.Fatalf("got %+v, want %+v", got, c.want)
			}
			if (got.Mention == nil) != (c.want.Mention == nil) || (got.Mention != nil && *got.Mention != *c.want.Mention) {
				t.Fatalf("mention %+v, want %+v", got.Mention, c.want.Mention)
			}
		})
	}
	if _, err := p.ParseEvent([]byte(`{"type":"event_callback","event":{"type":"app_mention"}}`)); err == nil {
		t.Fatal("an event with no event_id parsed")
	}
	if _, err := p.ParseEvent([]byte(`not json`)); err == nil {
		t.Fatal("not JSON parsed")
	}
}

func TestParseInteractionReadsAClickedChoice(t *testing.T) {
	p := slack.New(slack.Config{})
	payload, _ := jsonMarshal(fakeslack.Click("T1", "C1", "1.5", "1.0", "U1", "chr_1:abs"))
	in, err := p.ParseInteraction([]byte(url.Values{"payload": {string(payload)}}.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	if in.WorkspaceID != "T1" || in.ConversationID != "C1" || in.ThreadID != "1.0" || in.MessageID != "1.5" || in.User.ID != "U1" ||
		in.Value != "chr_1:abs" || !strings.HasPrefix(in.ID, "T1:U1:") {
		t.Fatalf("interaction %+v", in)
	}
	for _, bad := range []string{"", "payload=not-json", "payload=" + url.QueryEscape(`{"type":"view_submission"}`)} {
		if _, err := p.ParseInteraction([]byte(bad)); err == nil {
			t.Fatalf("%q parsed", bad)
		}
	}
}

func world(t *testing.T) (*fakeslack.Server, *slack.Provider, chat.Installation) {
	t.Helper()
	fake := fakeslack.New("cid", "csecret")
	fake.AddTeam("T1", "Acme")
	fake.AddUser(fakeslack.User{ID: "U1", Team: "T1", Name: "marcia", RealName: "Márcia Lopes"})
	fake.AddUser(fakeslack.User{ID: "U2", Team: "T1", Name: "eli", RealName: "Eli Park"})
	fake.AddConversation(fakeslack.Conversation{ID: "C1", Team: "T1", Name: "support", Kind: "public", Members: []string{"U1", "U2", "UBT1"}})
	fake.AddConversation(fakeslack.Conversation{ID: "G1", Team: "T1", Name: "design-private", Kind: "private", Members: []string{"U1", "UBT1"}})
	fake.AddConversation(fakeslack.Conversation{ID: "M1", Team: "T1", Name: "mpdm-x", Kind: "group", Members: []string{"U1", "U2", "UBT1"}})
	fake.AddConversation(fakeslack.Conversation{ID: "D1", Team: "T1", Kind: "dm", Members: []string{"U1", "UBT1"}})
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	p := slack.New(slack.Config{ClientID: "cid", ClientSecret: "csecret", APIURL: srv.URL + "/api"})
	return fake, p, chat.Installation{WorkspaceID: "T1", WorkspaceDomain: "acme", Token: fake.Token("T1")}
}

func TestConversationKindMembersAndThreads(t *testing.T) {
	fake, p, inst := world(t)
	ctx := context.Background()
	for conv, want := range map[string]chat.ConversationInfo{
		"C1": {Kind: chat.KindPublic, Name: "#support"}, "G1": {Kind: chat.KindPrivate, Name: "#design-private"},
		"M1": {Kind: chat.KindGroup, Name: "a group message"}, "D1": {Kind: chat.KindDM, Name: "a direct message"},
	} {
		got, err := p.Conversation(ctx, inst, conv)
		if err != nil || got != want {
			t.Fatalf("%s: %+v, %v; want %+v", conv, got, err, want)
		}
	}
	members, err := p.ListMembers(ctx, inst, "C1")
	if err != nil || strings.Join(members, ",") != "U1,U2,UBT1" {
		t.Fatalf("members %v, %v", members, err)
	}
	root := fake.Post("C1", "", "U2", "exports time out for 40 meters")
	second := fake.Post("C1", root, "U1", "@dude look at this")
	third := fake.Post("C1", root, "U2", "still slow")
	all, err := p.FetchThread(ctx, inst, "C1", root, "")
	if err != nil || len(all) != 3 || all[0].ID != root || all[0].UserID != "U2" || all[2].ID != third {
		t.Fatalf("thread %+v, %v", all, err)
	}
	since, err := p.FetchThread(ctx, inst, "C1", root, second)
	if err != nil || len(since) != 1 || since[0].ID != third {
		t.Fatalf("since %+v, %v", since, err)
	}
	if _, err := p.FetchThread(ctx, inst, "C1", "9.9", ""); err == nil {
		t.Fatal("a thread that is not there was read")
	}
	u, err := p.User(ctx, inst, "U1")
	if err != nil || u.Name() != "Márcia Lopes" || u.Handle != "marcia" {
		t.Fatalf("user %+v, %v", u, err)
	}
	if _, err := p.Conversation(ctx, chat.Installation{Token: "xoxb-forged"}, "C1"); err == nil {
		t.Fatal("a forged token was accepted")
	}
	if got := p.Permalink(inst, "C1", "1700000000.000002", "1700000000.000001"); got !=
		"https://acme.slack.com/archives/C1/p1700000000000002?thread_ts=1700000000.000001&cid=C1" {
		t.Fatalf("permalink %s", got)
	}
}

func TestRepliesEphemeralsQuestionsAndReactions(t *testing.T) {
	fake, p, inst := world(t)
	ctx := context.Background()
	root := fake.Post("C1", "", "U1", "@dude hi")
	ts, err := p.PostReply(ctx, inst, "C1", root, chat.Reply{Text: "Started a session", LinkURL: "https://dude.example.com/s/1"})
	if err != nil || ts == "" {
		t.Fatal(err)
	}
	msgs := fake.BotMessages()
	if len(msgs) != 1 || msgs[0].ThreadTS != root || !strings.Contains(string(msgs[0].Blocks), "Open in dude") ||
		!strings.Contains(string(msgs[0].Blocks), "https://dude.example.com/s/1") {
		t.Fatalf("reply %+v", msgs)
	}
	if err := p.PostEphemeral(ctx, inst, "C1", root, "U2", chat.Reply{Text: "Connect your account"}); err != nil {
		t.Fatal(err)
	}
	if err := p.PostEphemeral(ctx, inst, "G1", root, "U2", chat.Reply{Text: "x"}); err == nil {
		t.Fatal("an ephemeral reached someone not in the channel")
	}
	if e := fake.Ephemerals(); len(e) != 1 || e[0].User != "U2" || e[0].Text != "Connect your account" {
		t.Fatalf("ephemerals %+v", e)
	}
	q, err := p.PostQuestion(ctx, inst, "C1", root, chat.Question{Text: "Which project?", Choices: []chat.Choice{
		{Label: "ABS", Value: "chr_1:0"}, {Label: "A session for me", Value: "chr_1:1"}}})
	if err != nil {
		t.Fatal(err)
	}
	if b := string(fake.BotMessages()[1].Blocks); !strings.Contains(b, `"value":"chr_1:0"`) || !strings.Contains(b, "A session for me") {
		t.Fatalf("question blocks %s", b)
	}
	if err := p.UpdateQuestion(ctx, inst, "C1", q, chat.ExternalUser{DisplayName: "Márcia"}, "ABS"); err != nil {
		t.Fatal(err)
	}
	if u := fake.Updates(); len(u) != 1 || u[0].Text != "Márcia chose ABS" {
		t.Fatalf("updates %+v", u)
	}
	// 👀, then ✅ in its place; a second 👀 is no failure.
	for _, r := range []chat.Reaction{chat.ReactionWorking, chat.ReactionWorking} {
		if err := p.React(ctx, inst, "C1", root, r); err != nil {
			t.Fatal(err)
		}
	}
	if got := fake.Reactions("C1", root); strings.Join(got, ",") != "eyes" {
		t.Fatalf("reactions %v", got)
	}
	if err := p.React(ctx, inst, "C1", root, chat.ReactionDone); err != nil {
		t.Fatal(err)
	}
	if got := fake.Reactions("C1", root); strings.Join(got, ",") != "white_check_mark" {
		t.Fatalf("reactions %v", got)
	}
	// Done with no working mark to take off: still done.
	other := fake.Post("C1", "", "U1", "x")
	if err := p.React(ctx, inst, "C1", other, chat.ReactionFailed); err != nil {
		t.Fatal(err)
	}
	if got := fake.Reactions("C1", other); strings.Join(got, ",") != "x" {
		t.Fatalf("reactions %v", got)
	}
}

// follow requests a URL without following its redirect, and returns where it points.
func follow(t *testing.T, u string) *url.URL {
	t.Helper()
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, err := client.Get(u)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	_, _ = io.Copy(io.Discard, res.Body)
	loc, err := url.Parse(res.Header.Get("Location"))
	if err != nil || res.StatusCode != http.StatusFound {
		t.Fatalf("status %d, location %v", res.StatusCode, err)
	}
	return loc
}

func TestInstallIdentityAndRevoke(t *testing.T) {
	fake, p, _ := world(t)
	ctx := context.Background()
	fake.AuthorizeAs("U1", "U2")
	back := follow(t, p.InstallURL("st-1", "https://dude.example.com/v1/chat/slack/install/callback"))
	if back.Query().Get("state") != "st-1" || back.Query().Get("code") == "" {
		t.Fatalf("install callback %s", back)
	}
	inst, err := p.CompleteInstall(ctx, back.Query().Get("code"), "https://dude.example.com/v1/chat/slack/install/callback")
	if err != nil {
		t.Fatal(err)
	}
	if inst.WorkspaceID != "T1" || inst.WorkspaceName != "Acme" || inst.WorkspaceDomain != "t1" || inst.BotUserID != "UBT1" ||
		!strings.HasPrefix(inst.Token, "xoxb-") || len(inst.Scopes) != len(slack.BotScopes) {
		t.Fatalf("installation %+v", inst)
	}
	// A code works once.
	if _, err := p.CompleteInstall(ctx, back.Query().Get("code"), "https://dude.example.com/v1/chat/slack/install/callback"); err == nil {
		t.Fatal("a code was exchanged twice")
	}
	idBack := follow(t, p.IdentityURL("st-2", "https://dude.example.com/v1/chat/slack/connect/callback", "T1"))
	// The exchange repeats the redirect the sign-in was sent to.
	if _, err := p.CompleteIdentity(ctx, idBack.Query().Get("code"), "https://elsewhere.example.com/cb"); err == nil {
		t.Fatal("a sign-in code was exchanged with another redirect")
	}
	idBack = follow(t, p.IdentityURL("st-2b", "https://dude.example.com/v1/chat/slack/connect/callback", "T1"))
	who, err := p.CompleteIdentity(ctx, idBack.Query().Get("code"), "https://dude.example.com/v1/chat/slack/connect/callback")
	if err != nil || who.ID != "U2" || who.WorkspaceID != "T1" || who.DisplayName != "Eli Park" {
		t.Fatalf("identity %+v, %v", who, err)
	}
	// An install code is not a sign-in code.
	back = follow(t, p.InstallURL("st-3", "https://dude.example.com/cb"))
	if _, err := p.CompleteIdentity(ctx, back.Query().Get("code"), "https://dude.example.com/cb"); err == nil {
		t.Fatal("an install code signed someone in")
	}
	// Nobody to consent: access_denied, no code.
	fake.AuthorizeAs("", "")
	if denied := follow(t, p.IdentityURL("st-4", "https://dude.example.com/cb", "")); denied.Query().Get("error") != "access_denied" ||
		denied.Query().Get("code") != "" {
		t.Fatalf("denied %s", denied)
	}
	if err := p.Revoke(ctx, inst); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Conversation(ctx, inst, "C1"); err == nil {
		t.Fatal("a revoked token still works")
	}
	if got := fake.Revoked(); len(got) != 1 || got[0] != inst.Token {
		t.Fatalf("revoked %v", got)
	}
}

// The fake refuses as Slack does where dude could get it wrong: a method
// whose scope the token lacks, and a private channel the bot is not in.
func TestTheFakeRefusesMissingScopesAndChannelsTheBotIsNotIn(t *testing.T) {
	fake, p, inst := world(t)
	ctx := context.Background()
	narrow := inst
	narrow.Token = fake.Token("T1", "chat:write")
	var apiErr *slack.APIError
	if _, err := p.Conversation(ctx, narrow, "C1"); !errors.As(err, &apiErr) || apiErr.Code != "missing_scope" {
		t.Fatalf("conversations.info without channels:read: %v", err)
	}
	fake.AddConversation(fakeslack.Conversation{ID: "G9", Team: "T1", Name: "secret", Kind: "private", Members: []string{"U1"}})
	if _, err := p.ListMembers(ctx, inst, "G9"); !errors.As(err, &apiErr) || apiErr.Code != "channel_not_found" {
		t.Fatalf("a private channel the bot is not in: %v", err)
	}
	fake.AddConversation(fakeslack.Conversation{ID: "C9", Team: "T1", Name: "open", Kind: "public", Members: []string{"U1"}})
	if _, err := p.PostReply(ctx, inst, "C9", "", chat.Reply{Text: "x"}); !errors.As(err, &apiErr) || apiErr.Code != "not_in_channel" {
		t.Fatalf("posting in a public channel the bot is not in: %v", err)
	}
	// The scopes dude asks for at install are each needed by a call it makes.
	for _, scope := range []string{"channels:read", "groups:read", "im:read", "mpim:read"} {
		var kept []string
		for _, s := range slack.BotScopes {
			if s != scope {
				kept = append(kept, s)
			}
		}
		without := inst
		without.Token = fake.Token("T1", kept...)
		conv := map[string]string{"channels:read": "C1", "groups:read": "G1", "im:read": "D1", "mpim:read": "M1"}[scope]
		if _, err := p.Conversation(ctx, without, conv); !errors.As(err, &apiErr) || apiErr.Code != "missing_scope" {
			t.Errorf("without %s, conversations.info on %s: %v", scope, conv, err)
		}
	}
}
