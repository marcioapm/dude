package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
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

func ok(w http.ResponseWriter, _ seen) {
	_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"OK"}}]}`))
}

// efforts reads a result's efforts, "" for none.
func efforts(r Result) []string {
	out := []string{}
	for _, e := range r.Efforts {
		if e == nil {
			out = append(out, "")
		} else {
			out = append(out, *e)
		}
	}
	return out
}

// A test message is one Chat Completions request per distinct
// reasoning_effort (max goes as high, so high and max are one), tiny, with
// dude's key; each result names every effort it covers.
func TestATestMessageIsSentOncePerDistinctRequest(t *testing.T) {
	f := &fakeProxy{}
	srv := f.serve(t, ok)
	c := Client{URL: srv.URL + "/v1", Key: "sk-proxy"}
	results := c.Test(context.Background(), "gpt-5.6-sol", []string{"high", "max", "", "high"})
	if len(results) != 2 || !results[0].OK || !results[1].OK || *results[0].Status != 200 || results[0].Error != nil {
		t.Fatalf("results = %+v", results)
	}
	if got := efforts(results[0]); !slices.Equal(got, []string{"high", "max"}) || results[0].Sent == nil || *results[0].Sent != "high" {
		t.Errorf("first result covers %v, sent %v; want high and max, sent high", got, results[0].Sent)
	}
	if got := efforts(results[1]); !slices.Equal(got, []string{""}) || results[1].Sent != nil {
		t.Errorf("second result covers %v, sent %v; want none, sent none", got, results[1].Sent)
	}
	var sent []string
	for _, s := range f.seen {
		if s.Path != "/v1/chat/completions" || s.Headers.Get("Authorization") != "Bearer sk-proxy" ||
			s.Body["model"] != "gpt-5.6-sol" || s.Body["max_tokens"] != float64(testMaxTokens) {
			t.Errorf("request = %+v", s)
		}
		e, _ := s.Body["reasoning_effort"].(string)
		sent = append(sent, e)
	}
	slices.Sort(sent)
	if !slices.Equal(sent, []string{"", "high"}) {
		t.Errorf("reasoning_effort sent = %q, want one request with none and one with high", sent)
	}
}

// A Claude model is sent as Anthropic Messages, with x-api-key and no
// effort (OpenCode's Anthropic provider sends none for reasoningEffort), so
// any efforts asked for are one request, and its result names them all.
func TestAClaudeModelIsTestedAsAnthropicMessages(t *testing.T) {
	f := &fakeProxy{}
	srv := f.serve(t, func(w http.ResponseWriter, _ seen) { _, _ = w.Write([]byte(`{"content":[]}`)) })
	c := Client{URL: srv.URL + "/v1/", Key: "sk-proxy"}
	results := c.Test(context.Background(), "claude-opus-5-5", []string{"high", ""})
	if len(results) != 1 || !results[0].OK || results[0].Sent != nil || !slices.Equal(efforts(results[0]), []string{"high", ""}) {
		t.Fatalf("results = %+v", results)
	}
	if len(f.seen) != 1 {
		t.Fatalf("%d requests, want 1", len(f.seen))
	}
	s := f.seen[0]
	if s.Path != "/v1/messages" || s.Headers.Get("x-api-key") != "sk-proxy" || s.Headers.Get("anthropic-version") != "2023-06-01" ||
		s.Headers.Get("Authorization") != "" || s.Body["model"] != "claude-opus-5-5" {
		t.Errorf("request = %+v", s)
	}
	for _, key := range []string{"reasoning_effort", "effort", "output_config"} {
		if _, has := s.Body[key]; has {
			t.Errorf("an Anthropic request carried %s: %v", key, s.Body)
		}
	}
}

// The proxy's refusal is passed on as it came: its status and its message.
func TestAProxysRefusalIsPassedOnVerbatim(t *testing.T) {
	f := &fakeProxy{}
	srv := f.serve(t, func(w http.ResponseWriter, _ seen) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":{"type":"not_found_error","message":"model 'gpt-9' is not served by this proxy"}}`))
	})
	r := Client{URL: srv.URL, Key: "k"}.Test(context.Background(), "gpt-9", []string{"high"})[0]
	if r.OK || r.Status == nil || *r.Status != 404 || r.Error == nil || *r.Error != "model 'gpt-9' is not served by this proxy" {
		t.Errorf("result = %+v", r)
	}
	// A body that is not JSON is the body.
	srv2 := f.serve(t, func(w http.ResponseWriter, _ seen) { w.WriteHeader(502); _, _ = w.Write([]byte("upstream down")) })
	r = Client{URL: srv2.URL, Key: "k"}.Test(context.Background(), "gpt-9", []string{""})[0]
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
	r := Client{URL: srv.URL, Key: "k", Timeout: 200 * time.Millisecond}.Test(context.Background(), "gpt-x", []string{""})[0]
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
