package api_test

import (
	"context"
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
)

// The chat webhook routes take the service token alone, and give the
// provider only the headers the backend passed as X-Chat-Header-*: a raw
// X-Slack-Signature the caller set is not the provider's. A refusal keeps
// its status.
func TestChatWebhooksTakeTheServiceTokenAndOnlyTheProvidersPassedHeaders(t *testing.T) {
	app, owner := dbtest.Open(t)
	_ = owner
	svc := &chat.Service{DB: app, Providers: chat.Registry{"slack": slack.New(slack.Config{SigningSecret: "s3cret", ClientID: "c", ClientSecret: "x"})}}
	srv := httptest.NewServer((&api.Server{DB: app, Token: "svc", Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Chat: svc}).Handler())
	t.Cleanup(srv.Close)
	body := `{"type":"url_verification","challenge":"c-9"}`
	signed, _ := fakeslack.SignedRequest("http://x/", "s3cret", "application/json", []byte(body), time.Now())
	post := func(path, token string, header http.Header) int {
		t.Helper()
		req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL+path, strings.NewReader(body))
		for k, v := range header {
			req.Header[k] = v
		}
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := io.ReadAll(res.Body)
		res.Body.Close()
		if res.StatusCode == 200 && !strings.Contains(string(raw), "c-9") {
			t.Fatalf("answer %s", raw)
		}
		return res.StatusCode
	}
	passed := http.Header{}
	bare := http.Header{}
	for _, h := range []string{"X-Slack-Signature", "X-Slack-Request-Timestamp", "Content-Type"} {
		passed.Set("X-Chat-Header-"+h, signed.Header.Get(h))
		bare.Set(h, signed.Header.Get(h))
	}
	cases := []struct {
		name, path, token string
		header            http.Header
		want              int
	}{
		{"no service token", "/internal/chat/slack/events", "", passed, 401},
		{"a wrong service token", "/internal/chat/slack/events", "nope", passed, 401},
		{"signed, as the backend passes it", "/internal/chat/slack/events", "svc", passed, 200},
		{"the same signature as bare headers", "/internal/chat/slack/events", "svc", bare, 401},
		{"an unknown provider", "/internal/chat/discord/events", "svc", passed, 404},
	}
	for _, c := range cases {
		if got := post(c.path, c.token, c.header); got != c.want {
			t.Errorf("%s: %d, want %d", c.name, got, c.want)
		}
	}
}
