package agenttools_test

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/marciomartins/dude/orchestrator/internal/agenttools"
	"github.com/marciomartins/dude/orchestrator/internal/capabilities"
)

// wait_for over the JSON API: a wait opened, retried as the same wait, and
// what it refuses, writing nothing.
func TestWaitForOpensAWaitAndRefusesWhatItCannotWaitOn(t *testing.T) {
	f := setup(t)
	token := f.conductor(t)
	f.run(t, "run_wf_impl", "implementer", "running")
	// Another task's Run.
	mustExec(t, f.owner, `INSERT INTO tasks (id, organization_id, project_id, number, title, status)
		VALUES ('wi_wf_other', $1, $2, 2, 'Other', 'running')`, f.org, f.project)
	mustExec(t, f.owner, `INSERT INTO runs (id, organization_id, project_id, task_id, attempt, status, role, phase)
		VALUES ('run_wf_other', $1, $2, 'wi_wf_other', 1, 'running', 'reviewer', 'review')`, f.org, f.project)
	waits := func() int { return f.count(t, `SELECT count(*) FROM conductor_waits`) }
	for name, body := range map[string]string{
		"no deadline":          `{"conditions":[{"runsEnded":["run_wf_impl"]}]}`,
		"deadline over a day":  `{"deadline":"25h"}`,
		"deadline too soon":    `{"deadline":"10s"}`,
		"unknown key":          `{"deadline":"1h","conditions":[{"prMerged":{"pr":"1"}}]}`,
		"two keys in one item": `{"deadline":"1h","conditions":[{"runsEnded":["run_wf_impl"],"prChecksFinal":{"pr":"1"}}]}`,
		"another task's run":   `{"deadline":"1h","conditions":[{"runsEnded":["run_wf_other"]}]}`,
		"no such pr":           `{"deadline":"1h","conditions":[{"prChecksFinal":{"pr":"api#12"}}]}`,
		"more than 8":          `{"deadline":"1h","conditions":[{"runsEnded":["a","b","c","d","e","f","g","h","i"]}]}`,
		"wake some":            `{"deadline":"1h","wake":"some"}`,
	} {
		var out map[string]any
		if status := f.postAs(t, token, "wait_for", body, &out); status != 422 {
			t.Errorf("%s: %d %v", name, status, out)
		}
	}
	if waits() != 0 {
		t.Fatal("a refused wait_for wrote a wait")
	}
	var out map[string]any
	if status := f.postAs(t, token, "wait_for", `{"conditions":[{"runsEnded":["run_wf_impl"]}],"deadline":"45m","note":"then review"}`, &out); status != 200 ||
		out["waitId"] == nil || out["deadlineAt"] == nil || out["next"] == nil {
		t.Fatalf("wait_for: %d %v", status, out)
	}
	if waits() != 1 {
		t.Fatalf("%d waits", waits())
	}
	// It holds already: told so, nothing opened.
	mustExec(t, f.owner, `UPDATE runs SET status = 'completed' WHERE id = 'run_wf_impl'`)
	out = nil
	if status := f.postAs(t, token, "wait_for", `{"conditions":[{"runsEnded":["run_wf_impl"]}],"deadline":"45m"}`, &out); status != 200 ||
		out["holds"] != true || out["waitId"] != nil || out["digest"] == nil {
		t.Fatalf("an already-held wait_for: %d %v", status, out)
	}
	if waits() != 1 {
		t.Fatalf("an already-held wait opened one: %d waits", waits())
	}
}

// wait_for is the conductor's alone: any other role is refused it as a
// tool it does not have, and a conductor without conduct.wait is not given
// it on any surface.
func TestWaitForIsConductorOnly(t *testing.T) {
	srv := &agenttools.Server{}
	srv.ResolveCapabilitiesWith(func(_ context.Context, _ pgx.Tx, a capabilities.Agent) (capabilities.Grants, error) {
		if a.Run == "run_cond_nowait" {
			return capabilities.Default(a.Role).Without(capabilities.Wait), nil
		}
		return capabilities.Default(a.Role), nil
	})
	f := setupWith(t, srv)
	for _, role := range []string{"implementer", "reviewer", "simplifier", "qa_browser", "investigator"} {
		token := f.run(t, "run_wf_"+role, role, "running")
		if status, out := f.post(t, token, "wait_for", `{"deadline":"1h"}`); status != 404 || out["error"] != "no tool wait_for for this Run" {
			t.Errorf("%s: %d %v", role, status, out)
		}
		if _, ok := f.getTools(t, token)["wait_for"]; ok {
			t.Errorf("%s lists wait_for", role)
		}
	}
	if _, ok := f.getTools(t, f.conductor(t))["wait_for"]; !ok {
		t.Error("the conductor lacks wait_for")
	}
	mustExec(t, f.owner, `INSERT INTO tasks (id, organization_id, project_id, number, title, status)
		VALUES ('wi_nowait', $1, $2, 2, 'Other', 'running')`, f.org, f.project)
	less, hash := agenttools.NewToken()
	mustExec(t, f.owner, `INSERT INTO runs (id, organization_id, project_id, task_id, attempt, status, role, mcp_token_hash)
		VALUES ('run_cond_nowait', $1, $2, 'wi_nowait', 1, 'running', 'conductor', $3)`, f.org, f.project, hash)
	if status, out := f.post(t, less, "wait_for", `{"deadline":"1h"}`); status != 404 || out["error"] != "no tool wait_for for this Run" {
		t.Errorf("without conduct.wait: POST %d %v", status, out)
	}
	if _, ok := f.getTools(t, less)["wait_for"]; ok {
		t.Error("without conduct.wait: GET /tools lists wait_for")
	}
	cs, err := f.connect(t, less)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	list, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tl := range list.Tools {
		if tl.Name == "wait_for" {
			t.Error("without conduct.wait: MCP tools/list lists wait_for")
		}
	}
	if _, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "wait_for", Arguments: map[string]any{"deadline": "1h"}}); err == nil {
		t.Error("without conduct.wait: MCP tools/call wait_for succeeded")
	}
}
