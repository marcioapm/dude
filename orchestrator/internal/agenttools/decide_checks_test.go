package agenttools_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/marciomartins/dude/orchestrator/internal/agenttools"
	"github.com/marciomartins/dude/orchestrator/internal/capabilities"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// decide_checks is the conductor's alone: withdrawn, it is refused and
// unlisted on every surface (HTTP list and call, MCP list and call), and
// the caller's own settings view no longer grants it.
func TestDecideChecksGrantWithdrawnOnEverySurface(t *testing.T) {
	srv := &agenttools.Server{}
	srv.ResolveCapabilitiesWith(func(_ context.Context, _ pgx.Tx, a capabilities.Agent) (capabilities.Grants, error) {
		return capabilities.Default(a.Role).Without(capabilities.ChecksDecide), nil
	})
	f := setupWith(t, srv)
	token := f.run(t, "run_conductor", "conductor", "running")
	cs, err := f.connect(t, token)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	listed, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := f.getTools(t, token)["decide_checks"]; ok {
		t.Error("HTTP lists decide_checks")
	}
	for _, tool := range listed.Tools {
		if tool.Name == "decide_checks" {
			t.Error("MCP lists decide_checks")
		}
	}
	if status, _ := f.post(t, token, "decide_checks", `{}`); status != 404 {
		t.Errorf("HTTP decide_checks=%d", status)
	}
	if _, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "decide_checks", Arguments: map[string]any{}}); err == nil {
		t.Error("MCP admitted decide_checks")
	}
	var out map[string]any
	if status := f.postAs(t, token, "settings", `{"section":"capabilities"}`, &out); status != 200 {
		t.Fatal(status)
	}
	raw, _ := json.Marshal(out)
	var got struct {
		Capabilities []struct {
			Yours   bool
			Granted []struct{ Key capabilities.Key }
		}
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	for _, role := range got.Capabilities {
		for _, g := range role.Granted {
			if role.Yours && g.Key == capabilities.ChecksDecide {
				t.Fatal("settings grants the withdrawn checks.decide")
			}
		}
	}
}

// A writer, which has no checks.decide, cannot decide its own report.
func TestAWriterCannotDecideChecks(t *testing.T) {
	f := setup(t)
	token := f.run(t, "run_writer", "implementer", "running")
	if status, _ := f.post(t, token, "decide_checks", `{"runId":"run_writer","turn":1,"checks":[],"missing":{"verdict":"none","reason":"x"}}`); status != 404 {
		t.Fatalf("a writer's decide_checks=%d", status)
	}
}
