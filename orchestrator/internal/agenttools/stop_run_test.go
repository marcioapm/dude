package agenttools_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/marciomartins/dude/orchestrator/internal/agenttools"
	"github.com/marciomartins/dude/orchestrator/internal/capabilities"
)

// stop_run is conduct.stop's, which only the conductor has: an implementer,
// and a conductor whose conduct.stop is withheld, are neither listed it
// over JSON or MCP nor let call it ("no tool"); the conductor with its
// defaults is listed it.
func TestStopRunIsNotGrantedOutsideTheConductor(t *testing.T) {
	srv := &agenttools.Server{}
	srv.ResolveCapabilitiesWith(func(_ context.Context, _ pgx.Tx, a capabilities.Agent) (capabilities.Grants, error) {
		if a.Run == "run_reduced" {
			return capabilities.Default(a.Role).Without(capabilities.RunStop), nil
		}
		return capabilities.Default(a.Role), nil
	})
	f := setupWith(t, srv)
	if _, ok := f.getTools(t, f.run(t, "run_conducts", "conductor", "running"))["stop_run"]; !ok {
		t.Error("the conductor is not listed stop_run")
	}
	// One live conductor a task: the reduced one takes its place.
	mustExec(t, f.owner, `UPDATE runs SET status = 'completed' WHERE id = 'run_conducts'`)
	for _, c := range []struct{ run, role string }{{"run_impl", "implementer"}, {"run_reduced", "conductor"}} {
		token := f.run(t, c.run, c.role, "running")
		if _, ok := f.getTools(t, token)["stop_run"]; ok {
			t.Errorf("%s: JSON lists stop_run", c.run)
		}
		cs, err := f.connect(t, token)
		if err != nil {
			t.Fatal(err)
		}
		list, err := cs.ListTools(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		for _, tool := range list.Tools {
			if tool.Name == "stop_run" {
				t.Errorf("%s: MCP lists stop_run", c.run)
			}
		}
		if code, body := f.post(t, token, "stop_run", `{"run":"run_x","reason":"stuck"}`); code != 404 || !strings.Contains(fmt.Sprint(body["error"]), "no tool stop_run") {
			t.Errorf("%s: JSON stop_run: %d %s", c.run, code, body)
		}
		if res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "stop_run",
			Arguments: map[string]any{"run": "run_x", "reason": "stuck"}}); err == nil && !res.IsError {
			t.Errorf("%s: MCP allowed stop_run", c.run)
		}
		cs.Close()
	}
}
