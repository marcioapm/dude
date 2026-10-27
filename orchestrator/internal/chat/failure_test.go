package chat_test

import (
	"net/http"
	"strings"
	"testing"
)

// Requests reads why a failed request failed from chat_requests.failure,
// apart from the model's reasoning: the proxy's refusal of the routing call
// (its key redacted) with the brainstorm made instead, Slack's refusal of
// the reply (the bot token redacted) with what was made kept, a refusal
// before anything was made, and nothing for a request that succeeded.
func TestChatAFailedRequestKeepsItsCauseForRequests(t *testing.T) {
	t.Run("the routing call answered 400", func(t *testing.T) {
		w := setup(t)
		const key = "sk-proxy-test-key-0123456789"
		w.svc.Judge.LLM.Key = key
		w.model.refuseNext(http.StatusBadRequest, `{"error":{"message":"invalid request: bad credentials `+key+`"}}`)
		w.mention("Eli", "C1", "", "why are exports slow?")
		got := w.requests()
		if len(got) != 1 || got[0].Route != "failed" || got[0].ResultKind != "session" {
			t.Fatalf("requests %+v", got)
		}
		f := got[0].Failure
		if !strings.HasPrefix(f, "the routing call failed: ") || !strings.Contains(f, "the LLM proxy answered 400: invalid request") {
			t.Fatalf("failure %q", f)
		}
		if strings.Contains(f, key) || strings.Contains(got[0].Reasoning, key) || !strings.Contains(f, "[REDACTED:DUDE_LLM_KEY]") {
			t.Fatalf("the proxy key is not redacted: failure %q, reasoning %q", f, got[0].Reasoning)
		}
	})
	t.Run("Slack refused the reply", func(t *testing.T) {
		w := setup(t)
		token := w.one(`SELECT bot_token FROM chat_installations WHERE organization_id = $1`, w.org)
		w.model.queue(answer(map[string]any{"route": "task", "confidence": 0.9, "reasoning": "a clear task", "project": "BIL",
			"title": "Stream it", "goal": "g"}))
		w.fake.FailNext("chat.postMessage", "invalid_arguments "+token)
		w.mention("Eli", "C1", "", "file: stream it")
		got := w.requests()
		if len(got) != 1 || got[0].Route != "failed" || got[0].AskedRoute != "task" || got[0].ResultKind != "task" {
			t.Fatalf("requests %+v", got)
		}
		f := got[0].Failure
		if !strings.HasPrefix(f, "its reply was not posted: slack chat.postMessage: invalid_arguments") || strings.Contains(f, token) ||
			!strings.Contains(f, "[REDACTED:bot_token]") {
			t.Fatalf("failure %q", f)
		}
		// The model's own words stay the reasoning, first.
		if !strings.HasPrefix(got[0].Reasoning, "a clear task") {
			t.Fatalf("reasoning %q", got[0].Reasoning)
		}
	})
	t.Run("refused before anything was made", func(t *testing.T) {
		w := setup(t)
		w.fake.FailNext("conversations.info", "missing_scope")
		w.receive("Eli", "C1", "", "file it")
		w.once()
		got := w.requests()
		if len(got) != 1 || got[0].Route != "failed" || got[0].ResultKind != "" ||
			!strings.HasPrefix(got[0].Failure, "dude could not act on it: ") || !strings.Contains(got[0].Failure, "missing_scope") {
			t.Fatalf("requests %+v", got)
		}
	})
	t.Run("a request that succeeded has none", func(t *testing.T) {
		w := setup(t)
		w.model.queue(answer(map[string]any{"route": "task", "confidence": 0.9, "project": "BIL", "title": "Stream it", "goal": "g"}))
		w.mention("Eli", "C1", "", "file: stream it")
		if got := w.requests(); len(got) != 1 || got[0].Route != "task" || got[0].Failure != "" {
			t.Fatalf("requests %+v", got)
		}
	})
}
