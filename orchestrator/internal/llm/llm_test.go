package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"sync"
	"testing"
	"time"
)

func TestAModelGoesThroughAProviderByItsName(t *testing.T) {
	for model, want := range map[string]string{
		"claude-opus-5-5": ProviderAnthropic, "claude-haiku-4.5": ProviderAnthropic,
		"gpt-5.6-sol": ProviderOpenAI, "gemini-3.8-pro": ProviderOpenAI, "my-claude": ProviderOpenAI, "Claude-x": ProviderOpenAI,
		"claudette-1": ProviderOpenAI,
	} {
		if got := Provider(model); got != want {
			t.Errorf("Provider(%q) = %s, want %s", model, got, want)
		}
	}
}

// fakeProxy records what it was sent and answers as handle says.
type fakeProxy struct {
	mu   sync.Mutex
	seen []seen
}

type seen struct {
	Path    string
	Headers http.Header
	Body    map[string]any
}

func (f *fakeProxy) serve(t *testing.T, handle func(w http.ResponseWriter, s seen)) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		s := seen{Path: r.URL.Path, Headers: r.Header.Clone()}
		_ = json.Unmarshal(raw, &s.Body)
		f.mu.Lock()
		f.seen = append(f.seen, s)
		f.mu.Unlock()
		handle(w, s)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// An OpenAI model is tested as the agent requests it: one streamed
// Responses API request, tiny, with dude's key, the tier's reasoning (max as
// max) and summary, and the tier's headers.
func TestAnOpenAIModelIsTestedThroughTheResponsesAPI(t *testing.T) {
	f := &fakeProxy{}
	srv := f.serve(t, func(w http.ResponseWriter, _ seen) {
		_, _ = w.Write([]byte("event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{}}\n\n"))
	})
	c := Client{URL: srv.URL + "/v1", Key: "sk-proxy"}
	r := c.Test(context.Background(), TestTier{Model: "gpt-6-sol", Effort: "max", Headers: map[string]string{"X-Team": "dude"}})
	if !r.OK || *r.Status != 200 || r.Error != nil {
		t.Fatalf("result = %+v", r)
	}
	if len(f.seen) != 1 {
		t.Fatalf("%d requests, want 1", len(f.seen))
	}
	s := f.seen[0]
	if s.Path != "/v1/responses" || s.Headers.Get("Authorization") != "Bearer sk-proxy" || s.Headers.Get("X-Team") != "dude" ||
		s.Body["model"] != "gpt-6-sol" || s.Body["max_output_tokens"] != float64(testMaxTokens) || s.Body["stream"] != true || s.Body["store"] != false {
		t.Errorf("request = %+v", s)
	}
	want := map[string]any{"effort": "max", "summary": "auto"}
	if !reflect.DeepEqual(s.Body["reasoning"], want) || !reflect.DeepEqual(r.Sent, map[string]any{"reasoning": want}) {
		t.Errorf("reasoning sent %v, result says %v; want %v", s.Body["reasoning"], r.Sent, want)
	}
}

// A refusal inside a 200 stream (gpt-6-sol and effort none) is a failed
// test, with the proxy's words.
func TestARefusalInsideAStreamIsTheTestsError(t *testing.T) {
	f := &fakeProxy{}
	const msg = "Unsupported value: 'none' is not supported with the 'gpt-6.1-sol' model."
	srv := f.serve(t, func(w http.ResponseWriter, _ seen) {
		_, _ = w.Write([]byte("event: error\ndata: {\"type\":\"error\",\"error\":{\"message\":\"" + msg + "\",\"code\":\"unsupported_value\"}}\n\n"))
	})
	r := Client{URL: srv.URL, Key: "k"}.Test(context.Background(), TestTier{Model: "gpt-6-sol", Effort: "none"})
	if r.OK || r.Status == nil || *r.Status != 200 || r.Error == nil || *r.Error != msg {
		t.Errorf("result = %+v", r)
	}
	if _, has := f.seen[0].Body["reasoning"].(map[string]any)["effort"]; has {
		t.Errorf("effort none went on the wire as an effort: %v", f.seen[0].Body)
	}
}

// A Claude model is sent as Anthropic Messages, with x-api-key, and the
// tier's thinking and effort as the agent's provider writes them; the
// tier's options win over its effort.
func TestAClaudeModelIsTestedAsAnthropicMessages(t *testing.T) {
	f := &fakeProxy{}
	srv := f.serve(t, func(w http.ResponseWriter, _ seen) { _, _ = w.Write([]byte(`{"content":[]}`)) })
	c := Client{URL: srv.URL + "/v1/", Key: "sk-proxy"}
	r := c.Test(context.Background(), TestTier{Model: "claude-opus-5-5", Effort: "high", Options: map[string]any{"effort": "xhigh"}})
	if !r.OK {
		t.Fatalf("result = %+v", r)
	}
	s := f.seen[0]
	if s.Path != "/v1/messages" || s.Headers.Get("x-api-key") != "sk-proxy" || s.Headers.Get("anthropic-version") != "2023-06-01" ||
		s.Headers.Get("Authorization") != "" || s.Body["model"] != "claude-opus-5-5" || s.Body["max_tokens"] != float64(testMaxTokens) {
		t.Errorf("request = %+v", s)
	}
	if !reflect.DeepEqual(s.Body["thinking"], map[string]any{"type": "adaptive", "display": "summarized"}) ||
		!reflect.DeepEqual(s.Body["output_config"], map[string]any{"effort": "xhigh"}) {
		t.Errorf("body = %v", s.Body)
	}
	if _, has := s.Body["effort"]; has {
		t.Errorf("effort sent as a top-level key: %v", s.Body)
	}
	// None turns thinking off and sends no effort.
	c.Test(context.Background(), TestTier{Model: "claude-opus-5-5", Effort: "none"})
	if s := f.seen[1]; !reflect.DeepEqual(s.Body["thinking"], map[string]any{"type": "disabled"}) || s.Body["output_config"] != nil {
		t.Errorf("none: body = %v", s.Body)
	}
}

// The proxy's refusal is passed on as it came: its status and its message.
func TestAProxysRefusalIsPassedOnVerbatim(t *testing.T) {
	f := &fakeProxy{}
	srv := f.serve(t, func(w http.ResponseWriter, _ seen) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":{"type":"not_found_error","message":"model 'gpt-9' is not served by this proxy"}}`))
	})
	r := Client{URL: srv.URL, Key: "k"}.Test(context.Background(), TestTier{Model: "gpt-9", Effort: "high"})
	if r.OK || r.Status == nil || *r.Status != 404 || r.Error == nil || *r.Error != "model 'gpt-9' is not served by this proxy" {
		t.Errorf("result = %+v", r)
	}
	// A body that is not JSON is the body.
	srv2 := f.serve(t, func(w http.ResponseWriter, _ seen) { w.WriteHeader(502); _, _ = w.Write([]byte("upstream down")) })
	r = Client{URL: srv2.URL, Key: "k"}.Test(context.Background(), TestTier{Model: "gpt-9"})
	if *r.Status != 502 || *r.Error != "upstream down" {
		t.Errorf("result = %+v", r)
	}
}

// A proxy that does not answer in time is a result saying so, not a hang.
func TestATestMessageIsBounded(t *testing.T) {
	release := make(chan struct{})
	f := &fakeProxy{}
	srv := f.serve(t, func(w http.ResponseWriter, _ seen) { <-release })
	// After serve's: cleanups run last first, and the server's Close waits on the handler.
	t.Cleanup(func() { close(release) })
	start := time.Now()
	r := Client{URL: srv.URL, Key: "k", Timeout: 200 * time.Millisecond}.Test(context.Background(), TestTier{Model: "gpt-x"})
	if took := time.Since(start); took > 5*time.Second {
		t.Errorf("took %s", took)
	}
	if r.OK || r.Status != nil || r.Error == nil || *r.Error != "no answer from the LLM proxy in 200ms" {
		t.Errorf("result = %+v", r)
	}
}

func TestTheProxysModelsAreItsIDs(t *testing.T) {
	f := &fakeProxy{}
	srv := f.serve(t, func(w http.ResponseWriter, _ seen) {
		_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"claude-opus-5-5"},{"id":"gpt-5.6-sol"},{"id":""}]}`))
	})
	got, err := Client{URL: srv.URL + "/v1", Key: "sk"}.Models(context.Background())
	if err != nil || !slices.Equal(got, []string{"claude-opus-5-5", "gpt-5.6-sol"}) {
		t.Errorf("models = %v, %v", got, err)
	}
	if f.seen[0].Path != "/v1/models" || f.seen[0].Headers.Get("Authorization") != "Bearer sk" {
		t.Errorf("request = %+v", f.seen[0])
	}
	srv2 := f.serve(t, func(w http.ResponseWriter, _ seen) { w.WriteHeader(401); _, _ = w.Write([]byte(`{"error":"bad key"}`)) })
	if _, err := (Client{URL: srv2.URL, Key: "sk"}).Models(context.Background()); err == nil || err.Error() != "the LLM proxy answered 401: bad key" {
		t.Errorf("err = %v", err)
	}
}
