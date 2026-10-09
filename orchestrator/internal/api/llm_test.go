package api

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/dbtest"
	"github.com/marciomartins/dude/orchestrator/internal/llm"
)

// A test message through the internal API, called as the backend calls it:
// with the service token and no person (the backend has already decided
// only an admin may send one). One request for the tier's settings, with
// the proxy's answer passed on; the models it lists likewise.
func TestATestMessageIsServedToTheBackendAndReachesTheProxy(t *testing.T) {
	app, owner := dbtest.Open(t)
	org := dbtest.Org(t, owner)
	var mu sync.Mutex
	var efforts []any
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			_, _ = w.Write([]byte(`{"data":[{"id":"gpt-5.6-sol"}]}`))
			return
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		reasoning, _ := body["reasoning"].(map[string]any)
		mu.Lock()
		efforts = append(efforts, reasoning["effort"])
		mu.Unlock()
		_, _ = w.Write([]byte("data: {\"type\":\"response.completed\"}\n\n"))
	}))
	t.Cleanup(proxy.Close)
	s := &Server{DB: app, Token: "svc", Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		LLM: llm.Client{URL: proxy.URL + "/v1", Key: "sk"}}
	h := s.Handler()
	call := func(method, path, token, body string) (int, map[string]any) {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("X-Dude-Organization", org)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		var out map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, out
	}
	if code, _ := call("POST", "/internal/llm/test", "not-the-token", `{"model":"gpt-5.6-sol","effort":null}`); code != 401 {
		t.Errorf("without the service token: %d, want 401", code)
	}
	if len(efforts) != 0 {
		t.Fatalf("a refused test reached the proxy")
	}
	code, out := call("POST", "/internal/llm/test", "svc", `{"model":"gpt-5.6-sol","effort":"max","options":{"reasoningSummary":"detailed"}}`)
	result, _ := out["result"].(map[string]any)
	if code != 200 || result["ok"] != true || len(efforts) != 1 || efforts[0] != "max" {
		t.Fatalf("the backend's test: %d %v (proxy saw %v)", code, out, efforts)
	}
	if code, _ := call("POST", "/internal/llm/test", "svc", `{"model":"gpt-5.6-sol","effort":"extreme"}`); code != 400 {
		t.Errorf("an unknown effort: %d, want 400", code)
	}
	code, out = call("GET", "/internal/llm/models", "svc", "")
	if code != 200 || out["problem"] != nil || out["source"] != strings.TrimPrefix(proxy.URL, "http://")+"/v1" {
		t.Errorf("models: %d %v", code, out)
	}
	if models, _ := out["models"].([]any); len(models) != 1 || models[0] != "gpt-5.6-sol" {
		t.Errorf("models = %v", out["models"])
	}
}
