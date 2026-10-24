package chat_test

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
)

// The bot token is used for the provider's calls and nothing else: a
// mention that makes a session, a provider answer that echoes the token
// back in its error, and a delivery that fails on it leave it in no log
// line, no stored failure, no session, run, event, directive, request or
// model-call record, and in no answer dude gives about the installation.
func TestChatTheBotTokenReachesNothingButTheProvider(t *testing.T) {
	w := setup(t)
	var logs bytes.Buffer
	w.svc.Log = slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	token := w.one(`SELECT bot_token FROM chat_installations WHERE organization_id = $1`, w.org)
	w.model.queue(answer(map[string]any{"route": "brainstorm", "confidence": 0.9, "reasoning": "a chat", "title": "Exports"}))
	root := w.mention("Márcia", "C1", "", "why are exports slow?")
	// Slack refuses the next lookup with an answer naming the token.
	w.fake.FailNext("conversations.info", "invalid_auth "+token)
	w.mention("Gus", "C1", root, "and me?")
	w.fake.FailTimes("conversations.info", "invalid_auth "+token, 100)
	w.mention("Eli", "G1", "", "a private one")
	if !strings.Contains(logs.String(), "[REDACTED:bot_token]") {
		t.Fatalf("the echoed failure was not logged at all:\n%s", logs.String())
	}
	if strings.Contains(logs.String(), token) {
		t.Fatalf("the bot token was logged:\n%s", logs.String())
	}
	for _, q := range []string{
		`SELECT COALESCE(string_agg(COALESCE(last_failure, ''), ' '), '') FROM chat_installations`,
		`SELECT COALESCE(string_agg(COALESCE(last_error, '') || payload::text || COALESCE(decision::text, ''), ' '), '') FROM chat_events`,
		`SELECT COALESCE(string_agg(to_jsonb(r)::text, ' '), '') FROM chat_requests r`,
		`SELECT COALESCE(string_agg(to_jsonb(s)::text, ' '), '') FROM sessions s`,
		`SELECT COALESCE(string_agg(to_jsonb(r)::text, ' '), '') FROM runs r`,
		`SELECT COALESCE(string_agg(to_jsonb(d)::text, ' '), '') FROM directives d`,
		`SELECT COALESCE(string_agg(payload::text, ' '), '') FROM events`,
		`SELECT COALESCE(string_agg(to_jsonb(m)::text, ' '), '') FROM model_calls m`,
	} {
		if got := w.one(q); strings.Contains(got, token) || strings.Contains(got, "xoxb") {
			t.Errorf("%s holds the token: %s", q, got)
		}
	}
	if w.one(`SELECT last_failure FROM chat_installations WHERE organization_id = $1`, w.org) == "" {
		t.Fatal("the failure was not recorded")
	}
	st, err := w.svc.ConnectState(w.ctx, w.org, "slack", w.people["Gus"][0], promptState(t, w))
	if err != nil {
		t.Fatal(err)
	}
	if raw, _ := json.Marshal(st); strings.Contains(string(raw), "xoxb") {
		t.Fatalf("the connect page's answer holds the token: %s", raw)
	}
}
