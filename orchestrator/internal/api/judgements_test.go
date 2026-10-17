package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/dbtest"
	"github.com/marciomartins/dude/orchestrator/internal/judge"
	"github.com/marciomartins/dude/orchestrator/internal/llm"
)

// The test question through the internal API: asked of the tier named, on
// its model, as Judge asks; the answer passed back; nothing recorded. A tier
// of another organisation, or one naming no model, is refused unasked.
func TestTheTestQuestionIsAskedOfTheNamedTierAndNotRecorded(t *testing.T) {
	app, owner := dbtest.Open(t)
	ctx := context.Background()
	org, other := dbtest.Org(t, owner), dbtest.Org(t, owner)
	var mu sync.Mutex
	var sent []map[string]any
	answer := `{"decision":"yes","confidence":0.99,"reasoning":"Rayleigh scattering."}`
	status := http.StatusOK
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		sent = append(sent, body)
		mu.Unlock()
		if r.URL.Path != "/v1/chat/completions" {
			w.WriteHeader(404)
			return
		}
		w.WriteHeader(status)
		if status != http.StatusOK {
			_, _ = w.Write([]byte(`{"error":{"message":"model gpt-x is not served"}}`))
			return
		}
		out, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": answer}}},
			"usage": map[string]any{"prompt_tokens": 200, "completion_tokens": 20}})
		_, _ = w.Write(out)
	}))
	t.Cleanup(proxy.Close)
	var fast, thinker, theirs string
	if err := owner.QueryRow(ctx, `UPDATE model_tiers SET model = 'gpt-6-sol-mini' WHERE organization_id = $1 AND name = 'Fast' RETURNING id`, org).Scan(&fast); err != nil {
		t.Fatal(err)
	}
	if err := owner.QueryRow(ctx, `SELECT id FROM model_tiers WHERE organization_id = $1 AND name = 'Thinker'`, org).Scan(&thinker); err != nil {
		t.Fatal(err)
	}
	if err := owner.QueryRow(ctx, `UPDATE model_tiers SET model = 'gpt-6' WHERE organization_id = $1 AND name = 'Fast' RETURNING id`, other).Scan(&theirs); err != nil {
		t.Fatal(err)
	}
	s := &Server{DB: app, Token: "svc", Log: slog.New(slog.NewTextHandler(io.Discard, nil)), LLM: llm.Client{URL: proxy.URL + "/v1", Key: "sk"}}
	h := s.Handler()
	call := func(token, body string) (int, map[string]any) {
		r := httptest.NewRequest("POST", "/internal/judgements/test", strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("X-Dude-Organization", org)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		var out map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, out
	}
	if code, _ := call("nope", `{"tierId":"`+fast+`"}`); code != 401 {
		t.Errorf("without the service token: %d", code)
	}
	for tier, want := range map[string]int{theirs: 404, "mtr_nope": 404, thinker: 400, "": 400} {
		if code, out := call("svc", `{"tierId":"`+tier+`"}`); code != want {
			t.Errorf("tier %q: %d %v, want %d", tier, code, out, want)
		}
	}
	if len(sent) != 0 {
		t.Fatalf("a refused test reached the proxy: %v", sent)
	}
	code, out := call("svc", `{"tierId":"`+fast+`"}`)
	if code != 200 || out["ok"] != true || out["decision"] != "yes" || out["confidence"] != 0.99 || out["reasoning"] != "Rayleigh scattering." ||
		out["model"] != "gpt-6-sol-mini" || out["inputTokens"] != float64(200) || out["outputTokens"] != float64(20) || out["error"] != nil {
		t.Errorf("test: %d %v", code, out)
	}
	if len(sent) != 1 || sent[0]["model"] != "gpt-6-sol-mini" {
		t.Fatalf("sent %v", sent)
	}
	if user := sent[0]["messages"].([]any)[1].(map[string]any); user["content"] != judge.TestQuestion {
		t.Errorf("asked %v", user)
	}
	status = http.StatusNotFound
	code, out = call("svc", `{"tierId":"`+fast+`"}`)
	if code != 200 || out["ok"] != false || out["decision"] != nil || out["error"] != "the LLM proxy answered 404: model gpt-x is not served" {
		t.Errorf("refused: %d %v", code, out)
	}
	var n int
	if err := owner.QueryRow(ctx, `SELECT count(*) FROM model_calls`).Scan(&n); err != nil || n != 0 {
		t.Errorf("%d calls recorded (%v)", n, err)
	}
}

// With no proxy URL configured the test question is refused 503, after the
// tier is found and before anything is sent.
func TestTheTestQuestionIsRefusedWhenNoProxyIsConfigured(t *testing.T) {
	app, owner := dbtest.Open(t)
	org := dbtest.Org(t, owner)
	var fast string
	if err := owner.QueryRow(context.Background(), `UPDATE model_tiers SET model = 'gpt-6-sol-mini' WHERE organization_id = $1 AND name = 'Fast' RETURNING id`, org).Scan(&fast); err != nil {
		t.Fatal(err)
	}
	s := &Server{DB: app, Token: "svc", Log: slog.New(slog.NewTextHandler(io.Discard, nil)), LLM: llm.Client{}}
	r := httptest.NewRequest("POST", "/internal/judgements/test", strings.NewReader(`{"tierId":"`+fast+`"}`))
	r.Header.Set("Authorization", "Bearer svc")
	r.Header.Set("X-Dude-Organization", org)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), "the LLM proxy is not configured (DUDE_LLM_URL)") {
		t.Errorf("no proxy: %d %s", w.Code, w.Body.String())
	}
}
