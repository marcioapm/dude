package api

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/llm"
)

// What the Network page shows under every list: the operator's floor, and
// what every Run reaches without a list naming it.
func TestTheNetworkDefaultsAreTheOperatorsListAndWhatIsAlwaysReachable(t *testing.T) {
	s := &Server{Token: "svc", Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		LLM: llm.Client{URL: "https://llmproxy.example.com:8443/v1"}, AgentEgress: []string{"mirror.internal"}}
	r := httptest.NewRequest("GET", "/internal/network/defaults", nil)
	r.Header.Set("Authorization", "Bearer svc")
	r.Header.Set("X-Dude-Organization", "org_1")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	var out struct {
		Operator []string `json:"operator"`
		Always   []string `json:"always"`
		Model    *string  `json:"model"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	if w.Code != 200 || !reflect.DeepEqual(out.Operator, []string{"mirror.internal"}) ||
		!reflect.DeepEqual(out.Always, []string{"llmproxy.example.com", "dude’s tools"}) || out.Model == nil || *out.Model != "llmproxy.example.com" {
		t.Errorf("defaults: %d %s", w.Code, w.Body)
	}
	// No model configured: only dude's tools, and nothing from the operator.
	s = &Server{Token: "svc", Log: s.Log}
	w = httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Body.String() != `{"always":["dude’s tools"],"model":null,"operator":[]}`+"\n" {
		t.Errorf("defaults with nothing configured: %s", w.Body)
	}
}
