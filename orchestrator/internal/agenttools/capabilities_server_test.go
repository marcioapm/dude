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

// A session's agent resolved without session.checkout has no
// refresh_checkout: refused as a tool the Run does not have, and not
// listed; with its defaults it lists it.
func TestASessionAgentWithoutSessionCheckoutHasNoRefreshCheckout(t *testing.T) {
	srv := &agenttools.Server{}
	srv.ResolveCapabilitiesWith(func(_ context.Context, _ pgx.Tx, a capabilities.Agent) (capabilities.Grants, error) {
		if a.Session == "ssn_nocheckout" {
			return capabilities.Default(a.Role).Without(capabilities.SessionCheckout), nil
		}
		return capabilities.Default(a.Role), nil
	})
	f := setupWith(t, srv)
	without := f.session(t, "ssn_nocheckout", "", "")
	with := f.session(t, "ssn_checkout", "", "")
	status, out := f.post(t, without, "refresh_checkout", `{}`)
	if status != 404 || out["error"] != "no tool refresh_checkout for this Run" {
		t.Errorf("without session.checkout: %d %v", status, out)
	}
	if _, ok := f.getTools(t, without)["refresh_checkout"]; ok {
		t.Error("GET /tools lists refresh_checkout without session.checkout")
	}
	if _, ok := f.getTools(t, with)["refresh_checkout"]; !ok {
		t.Error("GET /tools lacks refresh_checkout with the brainstorm's defaults")
	}
}

// A conductor and a brainstorm resolved without settings.read are not given
// settings on any surface (POST /tools/{name}, GET /tools, MCP tools/list
// and tools/call), while the same roles with their defaults are.
func TestARunWithoutSettingsReadIsNotGivenSettingsOnAnySurface(t *testing.T) {
	srv := &agenttools.Server{}
	srv.ResolveCapabilitiesWith(func(_ context.Context, _ pgx.Tx, a capabilities.Agent) (capabilities.Grants, error) {
		if a.Run == "run_cond_less" || a.Session == "ssn_less" {
			return capabilities.Default(a.Role).Without(capabilities.SettingsRead), nil
		}
		return capabilities.Default(a.Role), nil
	})
	f := setupWith(t, srv)
	// A second task, for the second conductor: a task has one live conductor.
	mustExec(t, f.owner, `INSERT INTO tasks (id, organization_id, project_id, number, title, status)
		VALUES ('wi_less', $1, $2, 2, 'Other', 'running')`, f.org, f.project)
	lessToken, hash := agenttools.NewToken()
	mustExec(t, f.owner, `INSERT INTO runs (id, organization_id, project_id, task_id, attempt, status, role, mcp_token_hash)
		VALUES ('run_cond_less', $1, $2, 'wi_less', 1, 'running', 'conductor', $3)`, f.org, f.project, hash)
	with := map[string]string{"conductor": f.conductor(t), "brainstorm": f.session(t, "ssn_more", "", "")}
	less := map[string]string{"conductor": lessToken, "brainstorm": f.session(t, "ssn_less", "", "")}
	for role, token := range less {
		if status, out := f.post(t, token, "settings", `{}`); status != 404 || out["error"] != "no tool settings for this Run" {
			t.Errorf("%s without settings.read: POST %d %v", role, status, out)
		}
		if _, ok := f.getTools(t, token)["settings"]; ok {
			t.Errorf("%s without settings.read: GET /tools lists settings", role)
		}
		cs, err := f.connect(t, token)
		if err != nil {
			t.Fatal(err)
		}
		list, err := cs.ListTools(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		for _, tl := range list.Tools {
			if tl.Name == "settings" {
				t.Errorf("%s without settings.read: MCP tools/list lists settings", role)
			}
		}
		if _, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "settings", Arguments: map[string]any{}}); err == nil {
			t.Errorf("%s without settings.read: MCP tools/call settings succeeded", role)
		}
		cs.Close()
	}
	for role, token := range with {
		if _, ok := f.getTools(t, token)["settings"]; !ok {
			t.Errorf("%s with its defaults: GET /tools lacks settings", role)
		}
		if status, out := f.post(t, token, "settings", `{"section":"capabilities"}`); status != 200 {
			t.Errorf("%s with its defaults: POST settings %d %v", role, status, out)
		}
	}
}

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
	// The client hands the structured content back decoded as untyped JSON;
	// a round-trip gives it the tool's result shape.
	raw, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var tasks struct {
		Tasks []struct{ Key, Title string }
	}
	if err := json.Unmarshal(raw, &tasks); err != nil {
		t.Fatalf("MCP list_tasks: %v: %s", err, raw)
	}
	if len(tasks.Tasks) != 1 || tasks.Tasks[0].Key != "TEXT-1" {
		t.Errorf("MCP list_tasks: tasks %+v, want TEXT-1 alone", tasks.Tasks)
	}
}
