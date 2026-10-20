package agenttools_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/marciomartins/dude/orchestrator/internal/agenttools"
	"github.com/marciomartins/dude/orchestrator/internal/capabilities"
)

// A Run resolved without memory is not given search_memory on any of the
// server's surfaces (GET /tools, POST /tools/{name}, MCP tools/list and
// tools/call), and is refused it as it is refused a tool its role never
// had (start_phase); what it is granted it still lists and calls.
func TestARunWithoutACapabilityIsNotGivenItsToolsOnAnySurface(t *testing.T) {
	srv := &agenttools.Server{}
	srv.ResolveCapabilitiesWith(func(_ context.Context, _ pgx.Tx, a capabilities.Agent) (capabilities.Grants, error) {
		return capabilities.Default(a.Role).Without(capabilities.Memory), nil
	})
	f := setupWith(t, srv)
	token := f.run(t, "run_nomem", "implementer", "running")

	listed := f.getTools(t, token)
	if _, ok := listed["search_memory"]; ok {
		t.Error("GET /tools lists search_memory")
	}
	if _, ok := listed["list_tasks"]; !ok {
		t.Errorf("GET /tools lacks list_tasks: %v", listed)
	}

	status, out := f.post(t, token, "search_memory", `{"query":"truncate"}`)
	neverStatus, never := f.post(t, token, "start_phase", `{}`)
	if status != 404 || neverStatus != 404 {
		t.Errorf("POST search_memory %d, start_phase %d; want 404 for both", status, neverStatus)
	}
	if got, want := out["error"], "no tool search_memory for this Run"; got != want ||
		strings.Replace(never["error"].(string), "start_phase", "search_memory", 1) != want {
		t.Errorf("POST search_memory %q, start_phase %q", got, never["error"])
	}
	if status, out := f.post(t, token, "list_tasks", `{}`); status != 200 {
		t.Errorf("POST list_tasks: %d %v", status, out)
	}

	cs, err := f.connect(t, token)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	list, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, tl := range list.Tools {
		names[tl.Name] = true
	}
	if names["search_memory"] || !names["list_tasks"] {
		t.Errorf("MCP tools/list: search_memory %v, list_tasks %v", names["search_memory"], names["list_tasks"])
	}
	call := func(name string) string {
		_, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: map[string]any{}})
		if err == nil {
			t.Fatalf("MCP tools/call %s succeeded", name)
		}
		return err.Error()
	}
	if got, never := call("search_memory"), call("start_phase"); strings.Replace(never, "start_phase", "search_memory", -1) != got {
		t.Errorf("MCP tools/call search_memory %q, start_phase %q", got, never)
	}
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "list_tasks", Arguments: map[string]any{}})
	if err != nil || res.IsError {
		t.Fatalf("MCP list_tasks: %v %+v", err, res)
	}
	if raw, _ := json.Marshal(res.StructuredContent); !strings.Contains(string(raw), `"key":"TEXT-1"`) {
		t.Errorf("MCP list_tasks: %s", raw)
	}
}
