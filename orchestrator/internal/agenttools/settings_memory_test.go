package agenttools_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/agenttools"
	"github.com/marciomartins/dude/orchestrator/internal/embeddings"
)

type settingsEmbedder struct {
	embeddings.Fake
	calls int
}

func (e *settingsEmbedder) Embed(ctx context.Context, texts []string, p embeddings.Purpose) ([][]float32, error) {
	e.calls++
	return e.Fake.Embed(ctx, texts, p)
}

func TestSettingsMemoryReportsConfigurationNotInventedHealth(t *testing.T) {
	e := &settingsEmbedder{Fake: embeddings.Fake{Dims: 768}}
	f := setupWith(t, &agenttools.Server{Embedder: e})
	token := f.conductor(t)
	out := f.settingsOf(t, token, `{"section":"memory"}`)
	m := field(out, "memory")
	if field(m, "embeddingState") != "configured" || field(m, "dimensions") != 768.0 || unwrapped(field(m, "model").(string)) != e.Model() || field(m, "dynamicStatus") != "unavailable" || field(m, "source") != "operator" || field(m, "memberEditable") != false {
		t.Errorf("memory: %v", m)
	}
	if e.calls != 0 {
		t.Fatal("settings embedded text")
	}
	// The actual member index response with no live indexer has no model/dimensions.
	// Its dynamic data is explicitly excluded rather than synthesized by settings.
	mustExec(t, f.owner, `INSERT INTO people(id,organization_id,name,role) VALUES('psn_member',$1,'Member','member')`, f.org)
	route := f.settingsRoute(t, "/v1/memory/index")
	rules := map[string]coverageRule{
		"health": {Excluded: "live indexer health unavailable to tools; empty in fixture"},
		"total":  {Excluded: "index document counts, not configuration"}, "waiting": {Excluded: "historical queue status"}, "failed": {Excluded: "historical queue status"},
		"kinds.*.type": {Excluded: "historical index inventory"}, "kinds.*.total": {Excluded: "historical index inventory"}, "kinds.*.embedded": {Excluded: "historical index inventory"}, "kinds.*.waiting": {Excluded: "historical index inventory"}, "kinds.*.failed": {Excluded: "historical index inventory"},
		"failures": {Excluded: "failure history/content/error bodies not configuration"},
	}
	if err := classifyResponse(route, rules); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(out)
	if strings.Contains(string(raw), "endpoint") && field(m, "endpoint") != nil {
		t.Fatal("endpoint leaked")
	}
}
