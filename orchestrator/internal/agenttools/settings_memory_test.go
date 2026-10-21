package agenttools_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/marciomartins/dude/orchestrator/internal/agenttools"
	"github.com/marciomartins/dude/orchestrator/internal/embeddings"
	"github.com/marciomartins/dude/orchestrator/internal/memory"
)

// countingTransport fails and counts every request: settings must send none.
type countingTransport struct{ calls atomic.Int32 }

func (c *countingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	c.calls.Add(1)
	return nil, http.ErrHandlerTimeout
}

// failingIndexer is an indexer whose embedder is failing, with an error
// naming a canary.
type failingIndexer struct{ since, retry time.Time }

func (x failingIndexer) Health() memory.Health {
	return memory.Health{Error: "HEALTH-ERROR-CANARY", Since: &x.since, Retry: &x.retry}
}
func (failingIndexer) Resume() {}

// The memory section's model and dimensions are what GET /v1/memory/index
// shows a member for the same embedder; the endpoint and the indexer's
// health error are not returned, and nothing is embedded.
func TestSettingsMemoryReportsConfigurationNotInventedHealth(t *testing.T) {
	transport := &countingTransport{}
	e := &embeddings.Client{BaseURL: "https://embed-endpoint-canary.internal/v1", Key: "EMBED-KEY-CANARY", ModelName: "gemini-embedding-2",
		Dims: 768, HTTPClient: &http.Client{Transport: transport}}
	f := setupWith(t, &agenttools.Server{Embedder: e})
	token := f.conductor(t)
	var raw json.RawMessage
	if status := f.postAs(t, token, "settings", `{"section":"memory"}`, &raw); status != 200 {
		t.Fatalf("settings: %d %s", status, raw)
	}
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	m := field(out, "memory")
	if field(m, "embeddingState") != "configured" || field(m, "dynamicStatus") != "unavailable" || field(m, "source") != "operator" ||
		field(m, "memberEditable") != false {
		t.Errorf("memory: %v", m)
	}
	if transport.calls.Load() != 0 {
		t.Fatal("settings embedded text")
	}
	mustExec(t, f.owner, `INSERT INTO people(id,organization_id,name,role) VALUES('psn_member',$1,'Member','member')`, f.org)
	now := time.Now().UTC().Truncate(time.Second)
	route := f.settingsRouteWith(t, "/v1/memory/index", e, failingIndexer{since: now.Add(-time.Hour), retry: now.Add(time.Minute)})
	rules := map[string]coverageRule{
		"model":        {Tool: "model"},
		"dimensions":   {Tool: "dimensions"},
		"endpoint":     {Excluded: "where text is sent: the operator's deployment, not a setting"},
		"health.error": {Excluded: "the indexer's last error body: live health is not wired to tools"},
		"health.since": {Excluded: "live indexer health is not wired to tools"},
		"health.retry": {Excluded: "live indexer health is not wired to tools"},
		"total":        {Excluded: "index document counts, not configuration"}, "waiting": {Excluded: "historical queue status"}, "failed": {Excluded: "historical queue status"},
		"kinds.*.type": {Excluded: "historical index inventory"}, "kinds.*.total": {Excluded: "historical index inventory"}, "kinds.*.embedded": {Excluded: "historical index inventory"}, "kinds.*.waiting": {Excluded: "historical index inventory"}, "kinds.*.failed": {Excluded: "historical index inventory"},
		"failures": {Excluded: "failure history/content/error bodies not configuration"},
	}
	if err := classifyResponse(route, rules); err != nil {
		t.Fatal(err)
	}
	if model, _ := field(m, "model").(string); !strings.HasPrefix(model, "⟦external ") || unwrapped(model) != route["model"] {
		t.Errorf("model: tool %v, route %v", field(m, "model"), route["model"])
	}
	if field(m, "dimensions") != route["dimensions"] {
		t.Errorf("dimensions: tool %v, route %v", field(m, "dimensions"), route["dimensions"])
	}
	if _, ok := m.(map[string]any)["endpoint"]; ok {
		t.Error("endpoint key returned")
	}
	for _, canary := range []string{"HEALTH-ERROR-CANARY", "embed-endpoint-canary", "EMBED-KEY-CANARY"} {
		if strings.Contains(string(raw), canary) {
			t.Errorf("settings shows %s", canary)
		}
	}
}

// With no embedder, memory is words-only search: not configured, which is
// not off, and its live status stays unavailable.
func TestSettingsMemoryWithoutAnEmbedderIsNotConfigured(t *testing.T) {
	f := setup(t)
	m := field(f.settingsOf(t, f.conductor(t), `{"section":"memory"}`), "memory")
	if field(m, "embeddingState") != "not-configured" || field(m, "dynamicStatus") != "unavailable" || field(m, "model") != nil {
		t.Errorf("memory: %v", m)
	}
}
