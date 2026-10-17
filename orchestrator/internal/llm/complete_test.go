package llm

import (
	"context"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"
)

func okAnswer(w http.ResponseWriter, _ seen) {
	_, _ = w.Write([]byte(`{"model":"gpt-6-sol-2026","choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"yes"}}],"usage":{"prompt_tokens":12,"completion_tokens":3}}`))
}

// One Chat Completions request, with dude's key, the tier's headers and
// exactly the parameters the call names.
func TestACompletionIsOneChatCompletionsRequest(t *testing.T) {
	f := &fakeProxy{}
	srv := f.serve(t, okAnswer)
	temp := 0.0
	c := Client{URL: srv.URL + "/v1", Key: "sk-proxy"}
	got, err := c.Complete(context.Background(), Call{
		Model: "claude-opus-5-5", Effort: "high", Headers: map[string]string{"X-Team": "acme"},
		Messages:  []Message{{Role: "system", Content: "be brief"}, {Role: "user", Content: "is it?"}},
		MaxTokens: 64, Temperature: &temp,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(f.seen) != 1 {
		t.Fatalf("%d requests, want 1", len(f.seen))
	}
	s := f.seen[0]
	if s.Path != "/v1/chat/completions" {
		t.Errorf("path = %s", s.Path)
	}
	if s.Headers.Get("Authorization") != "Bearer sk-proxy" || s.Headers.Get("X-Team") != "acme" || s.Headers.Get("Content-Type") != "application/json" {
		t.Errorf("headers = %v", s.Headers)
	}
	want := map[string]any{
		"model": "claude-opus-5-5", "stream": false, "max_tokens": float64(64), "temperature": float64(0), "reasoning_effort": "high",
		"messages": []any{
			map[string]any{"role": "system", "content": "be brief"},
			map[string]any{"role": "user", "content": "is it?"},
		},
	}
	if !reflect.DeepEqual(s.Body, want) {
		t.Errorf("body = %v\nwant   %v", s.Body, want)
	}
	if got.Text != "yes" || got.FinishReason != "stop" || got.ServedModel != "gpt-6-sol-2026" ||
		got.InputTokens != 12 || got.OutputTokens != 3 || got.Status == nil || *got.Status != 200 {
		t.Errorf("completion = %+v", got)
	}
}

// Unset parameters are left out; effort none is sent as none.
func TestACompletionSendsOnlyWhatTheCallNames(t *testing.T) {
	f := &fakeProxy{}
	srv := f.serve(t, okAnswer)
	c := Client{URL: srv.URL, Key: "k"}
	if _, err := c.Complete(context.Background(), Call{Model: "m", Messages: []Message{{Role: "user", Content: "x"}}}); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"max_tokens", "temperature", "reasoning_effort"} {
		if _, has := f.seen[0].Body[key]; has {
			t.Errorf("%s sent unasked: %v", key, f.seen[0].Body)
		}
	}
	if _, err := c.Complete(context.Background(), Call{Model: "m", Effort: "none"}); err != nil {
		t.Fatal(err)
	}
	if f.seen[1].Body["reasoning_effort"] != "none" {
		t.Errorf("effort none: body = %v", f.seen[1].Body)
	}
}

// The tier's headers cannot replace dude's key.
func TestATiersHeadersDoNotReplaceTheKey(t *testing.T) {
	f := &fakeProxy{}
	srv := f.serve(t, okAnswer)
	_, _ = Client{URL: srv.URL, Key: "sk-dude"}.Complete(context.Background(), Call{Model: "m", Headers: map[string]string{"Authorization": "Bearer other"}})
	if got := f.seen[0].Headers.Get("Authorization"); got != "Bearer sk-dude" {
		t.Errorf("Authorization = %q", got)
	}
}

// Content in parts is the text parts joined; usage absent is zero tokens.
func TestArrayContentIsItsTextPartsJoined(t *testing.T) {
	f := &fakeProxy{}
	srv := f.serve(t, func(w http.ResponseWriter, _ seen) {
		_, _ = w.Write([]byte(`{"choices":[{"finish_reason":"length","message":{"content":[{"type":"text","text":"Hello, "},{"type":"image_url","image_url":{"url":"x"}},{"type":"text","text":"world"}]}}]}`))
	})
	got, err := Client{URL: srv.URL, Key: "k"}.Complete(context.Background(), Call{Model: "m"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Text != "Hello, world" || got.FinishReason != "length" || got.InputTokens != 0 || got.OutputTokens != 0 {
		t.Errorf("completion = %+v", got)
	}
}

// A refusal carries the proxy's status and message, in either shape.
func TestARefusedCompletionIsTheProxysError(t *testing.T) {
	f := &fakeProxy{}
	for _, tc := range []struct {
		status int
		body   string
		want   string
	}{
		{400, `{"error":{"message":"model 'gpt-9' is not served","type":"invalid_request_error"}}`, "the LLM proxy answered 400: model 'gpt-9' is not served"},
		{401, `{"error":"bad key"}`, "the LLM proxy answered 401: bad key"},
		{502, `upstream down`, "the LLM proxy answered 502: upstream down"},
	} {
		srv := f.serve(t, func(w http.ResponseWriter, _ seen) { w.WriteHeader(tc.status); _, _ = w.Write([]byte(tc.body)) })
		got, err := Client{URL: srv.URL, Key: "k"}.Complete(context.Background(), Call{Model: "m"})
		if err == nil || err.Error() != tc.want {
			t.Errorf("%d: err = %v, want %q", tc.status, err, tc.want)
		}
		if got.Status == nil || *got.Status != tc.status || got.Text != "" {
			t.Errorf("%d: completion = %+v", tc.status, got)
		}
	}
}

// A proxy that does not answer in time is an error naming the bound, with
// the time spent and no status.
func TestACompletionIsBounded(t *testing.T) {
	release := make(chan struct{})
	f := &fakeProxy{}
	srv := f.serve(t, func(w http.ResponseWriter, _ seen) { <-release })
	t.Cleanup(func() { close(release) })
	start := time.Now()
	got, err := Client{URL: srv.URL, Key: "k"}.Complete(context.Background(), Call{Model: "m", Timeout: 200 * time.Millisecond})
	if took := time.Since(start); took > 5*time.Second {
		t.Errorf("took %s", took)
	}
	if err == nil || err.Error() != "no answer from the LLM proxy in 200ms" {
		t.Errorf("err = %v", err)
	}
	if got.Status != nil || got.LatencyMs < 150 {
		t.Errorf("completion = %+v", got)
	}
}

// A 2xx with no choices is an error, keeping status and usage.
func TestACompletionWithNoChoicesIsAnError(t *testing.T) {
	f := &fakeProxy{}
	srv := f.serve(t, func(w http.ResponseWriter, _ seen) {
		_, _ = w.Write([]byte(`{"model":"m-1","choices":[],"usage":{"prompt_tokens":5,"completion_tokens":0}}`))
	})
	got, err := Client{URL: srv.URL, Key: "k"}.Complete(context.Background(), Call{Model: "m"})
	if err == nil || err.Error() != "the LLM proxy answered with no choices" {
		t.Errorf("err = %v", err)
	}
	if got.Status == nil || *got.Status != 200 || got.InputTokens != 5 || got.ServedModel != "m-1" {
		t.Errorf("completion = %+v", got)
	}
}

func TestACompletionNeedsTheProxy(t *testing.T) {
	_, err := Client{}.Complete(context.Background(), Call{Model: "m"})
	if err == nil || !strings.Contains(err.Error(), "DUDE_LLM_URL") {
		t.Errorf("err = %v", err)
	}
}
