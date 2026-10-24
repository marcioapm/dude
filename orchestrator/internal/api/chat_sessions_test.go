package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/marciomartins/dude/orchestrator/internal/api"
	"github.com/marciomartins/dude/orchestrator/internal/chat"
	"github.com/marciomartins/dude/orchestrator/internal/chat/slack"
	"github.com/marciomartins/dude/orchestrator/internal/dbtest"
	"github.com/marciomartins/dude/orchestrator/internal/fakeslack"
	"github.com/marciomartins/dude/orchestrator/internal/judge"
	"github.com/marciomartins/dude/orchestrator/internal/llm"
)

// A session a mention made opens on the people's session page
// (/internal/sessions/{id}, the backend's /v1/brainstorms/{id}) for whom
// the conversation shares it with: anyone in the organisation from a
// public channel; a private channel's connected members, not the others.
func TestChatMadeSessionsOpenForWhomTheirConversationSharesThemWith(t *testing.T) {
	app, owner := dbtest.Open(t)
	ctx := context.Background()
	org := dbtest.Org(t, owner)
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := owner.Exec(ctx, q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	var tier string
	if err := owner.QueryRow(ctx, `UPDATE model_tiers SET model = 'judge-model' WHERE organization_id = $1 AND name = 'Fast' RETURNING id`, org).Scan(&tier); err != nil {
		t.Fatal(err)
	}
	exec(`UPDATE organizations SET judgement_tier_id = $1 WHERE id = $2`, tier, org)
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		out, _ := json.Marshal(map[string]any{"model": "judge", "usage": map[string]any{"prompt_tokens": 1, "completion_tokens": 1},
			"choices": []any{map[string]any{"finish_reason": "stop", "message": map[string]any{"role": "assistant",
				"content": `{"route":"brainstorm","confidence":0.9,"reasoning":"talk","title":"Talk"}`}}}})
		_, _ = w.Write(out)
	}))
	t.Cleanup(model.Close)
	fake := fakeslack.New("cid", "csecret")
	fake.AddTeam("T1", "Acme Slack")
	slackSrv := httptest.NewServer(fake)
	t.Cleanup(slackSrv.Close)

	people := map[string]string{}
	for _, who := range []struct{ name, user string }{{"Marcia", "U1"}, {"Eli", "U2"}, {"Gus", "U3"}} {
		person, account := fmt.Sprintf("per_%s_%s", who.user, org), fmt.Sprintf("acc_%s_%s", who.user, org)
		exec(`INSERT INTO accounts (id, email, name) VALUES ($1, $2, $3)`, account, account+"@example.com", who.name)
		exec(`INSERT INTO people (id, organization_id, name, role, account_id) VALUES ($1, $2, $3, 'member', $4)`, person, org, who.name, account)
		fake.AddUser(fakeslack.User{ID: who.user, Team: "T1", Name: strings.ToLower(who.name), RealName: who.name})
		people[who.name] = person
		if who.user != "U3" {
			exec(`INSERT INTO chat_identities (organization_id, provider, external_workspace_id, external_user_id, account_id, person_id)
				VALUES ($1, 'slack', 'T1', $2, $3, $4)`, org, who.user, account, person)
		}
	}
	exec(`INSERT INTO chat_installations (id, organization_id, provider, external_workspace_id, workspace_name, bot_user_id, bot_token)
		VALUES ($1, $2, 'slack', 'T1', 'Acme Slack', 'UBT1', $3)`, "chi_"+org, org, fake.Token("T1"))
	fake.AddConversation(fakeslack.Conversation{ID: "C1", Team: "T1", Name: "support", Kind: "public", Members: []string{"U1", "U2", "U3", "UBT1"}})
	fake.AddConversation(fakeslack.Conversation{ID: "G1", Team: "T1", Name: "design", Kind: "private", Members: []string{"U1", "U2", "U3", "UBT1"}})

	svc := &chat.Service{DB: app, PublicURL: "https://dude.example.com",
		Providers: chat.Registry{"slack": slack.New(slack.Config{SigningSecret: "s", ClientID: "cid", ClientSecret: "csecret", APIURL: slackSrv.URL + "/api"})},
		Judge:     &judge.Service{DB: app, LLM: llm.Client{URL: model.URL + "/v1", Key: "k"}}}
	srv := httptest.NewServer((&api.Server{DB: app, Token: "svc", Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Chat: svc}).Handler())
	t.Cleanup(srv.Close)

	mention := func(channel, event string) string {
		t.Helper()
		ts := fake.Post(channel, "", "U1", "<@UBT1> talk")
		raw, _ := json.Marshal(fakeslack.Mention("T1", event, channel, "", ts, "U1", "talk"))
		req, _ := fakeslack.SignedRequest("http://x/", "s", "application/json", raw, time.Now())
		if _, err := svc.Receive(ctx, "slack", false, req.Header, raw); err != nil {
			t.Fatal(err)
		}
		for range 5 {
			if _, err := svc.Sweep(ctx); err != nil {
				t.Fatal(err)
			}
			svc.Wait()
		}
		var session string
		if err := owner.QueryRow(ctx, `SELECT result_id FROM chat_requests WHERE event_id = $1 AND result_kind = 'session'`, event).Scan(&session); err != nil {
			t.Fatalf("no session from %s: %v", event, err)
		}
		return session
	}
	open := func(session, person string) int {
		t.Helper()
		req, _ := http.NewRequest(http.MethodGet, srv.URL+"/internal/sessions/"+session, nil)
		req.Header.Set("Authorization", "Bearer svc")
		req.Header.Set("X-Dude-Organization", org)
		req.Header.Set("X-Dude-Credential-Kind", "person")
		req.Header.Set("X-Dude-Person", person)
		req.Header.Set("X-Dude-Actor", person)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res.StatusCode
	}
	public, private := mention("C1", "EvPublic"), mention("G1", "EvPrivate")
	for _, c := range []struct {
		name, session, person string
		want                  int
	}{
		{"Eli, never added, a public channel's session", public, people["Eli"], 200},
		{"Gus, not connected, a public channel's session", public, people["Gus"], 200},
		{"Eli, connected, a private channel's session", private, people["Eli"], 200},
		{"Gus, not connected, a private channel's session", private, people["Gus"], 404},
	} {
		if got := open(c.session, c.person); got != c.want {
			t.Errorf("%s: %d, want %d", c.name, got, c.want)
		}
	}
}
