package agenttools_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/marciomartins/dude/orchestrator/internal/agenttools"
	"github.com/marciomartins/dude/orchestrator/internal/capabilities"
	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/forge"
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

// after over the JSON API: under 30m or at the deadline refused, writing
// nothing; 30m before a 1h deadline opens a wait keeping its time.
func TestWaitForAfterOverTheTool(t *testing.T) {
	f := setup(t)
	token := f.conductor(t)
	waits := func() int { return f.count(t, `SELECT count(*) FROM conductor_waits`) }
	for name, body := range map[string]string{
		"29m":                `{"deadline":"1h","conditions":[{"after":"29m"}]}`,
		"after the deadline": `{"deadline":"1h","conditions":[{"after":"90m"}]}`,
	} {
		var out map[string]any
		if status := f.postAs(t, token, "wait_for", body, &out); status != 422 {
			t.Errorf("%s: %d %v", name, status, out)
		}
	}
	if waits() != 0 {
		t.Fatal("a refused after wrote a wait")
	}
	var out map[string]any
	if status := f.postAs(t, token, "wait_for", `{"deadline":"1h","conditions":[{"after":"30m"}]}`, &out); status != 200 ||
		out["waitId"] == nil || out["holds"] != nil {
		t.Fatalf("wait_for after: %d %v", status, out)
	}
	if n := f.count(t, `SELECT count(*) FROM conductor_waits
		WHERE conditions->0->>'kind' = 'after' AND (conditions->0->>'at')::timestamptz > now() + interval '29 minutes'`); n != 1 {
		t.Fatalf("stored after: %d", n)
	}
}

// siblingTask over the JSON API: refused off the epic or for an unknown
// becomes, writing nothing; a sibling on the epic opens a wait that the
// sibling starting holds.
func TestWaitForSiblingTaskOverTheTool(t *testing.T) {
	f := setup(t)
	token := f.conductor(t)
	epic := "epc_" + f.org
	mustExec(t, f.owner, `UPDATE tasks SET epic_id = $1 WHERE id = $2`, epic, f.item)
	mustExec(t, f.owner, `INSERT INTO tasks (id, organization_id, project_id, number, epic_id, title, status)
		VALUES ('wi_wf_sib', $1, $2, 3, $3, 'Sibling', 'queued')`, f.org, f.project, epic)
	mustExec(t, f.owner, `INSERT INTO tasks (id, organization_id, project_id, number, title, status)
		VALUES ('wi_wf_loose', $1, $2, 4, 'Loose', 'queued')`, f.org, f.project)
	waits := func() int { return f.count(t, `SELECT count(*) FROM conductor_waits`) }
	for name, body := range map[string]string{
		"off the epic":   `{"deadline":"1h","conditions":[{"siblingTask":{"task":"TEXT-4"}}]}`,
		"own task":       `{"deadline":"1h","conditions":[{"siblingTask":{"task":"TEXT-1"}}]}`,
		"unknown value":  `{"deadline":"1h","conditions":[{"siblingTask":{"task":"TEXT-3","becomes":["merged"]}}]}`,
		"no such task":   `{"deadline":"1h","conditions":[{"siblingTask":{"task":"TEXT-9"}}]}`,
		"no task at all": `{"deadline":"1h","conditions":[{"siblingTask":{}}]}`,
	} {
		var out map[string]any
		if status := f.postAs(t, token, "wait_for", body, &out); status != 422 {
			t.Errorf("%s: %d %v", name, status, out)
		}
	}
	if waits() != 0 {
		t.Fatal("a refused siblingTask wrote a wait")
	}
	var out map[string]any
	if status := f.postAs(t, token, "wait_for", `{"deadline":"1h","conditions":[{"siblingTask":{"task":"text-3","becomes":["started"]}}]}`, &out); status != 200 ||
		out["waitId"] == nil || out["holds"] != nil {
		t.Fatalf("wait_for siblingTask: %d %v", status, out)
	}
	if err := f.app.InOrg(context.Background(), f.org, func(tx pgx.Tx) error {
		if _, err := delivery.SetTaskStatusTx(context.Background(), tx, f.org, f.project, "wi_wf_sib", "", "running", "test"); err != nil {
			return err
		}
		_, err := delivery.EvaluateWaitTx(context.Background(), tx, f.org, f.item, out["waitId"].(string))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if n := f.count(t, `SELECT count(*) FROM conductor_waits WHERE ended_by = 'held'`); n != 1 {
		t.Fatalf("the sibling started: %d held", n)
	}
}

// prEnded over the JSON API: a pull request dude does not track is read
// once before the call's transaction, counted as one GitHub read of the
// task; one GitHub cannot see is refused, writing nothing.
func TestWaitForPREndedOverTheTool(t *testing.T) {
	// While GitHub answers the pre-read, no transaction is open: counted
	// from a connection of its own, as chat's notices test does.
	var openTxs func() (int, error)
	var watched, open atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/other/lib/pulls/41":
			n, err := openTxs()
			if err != nil {
				n = 1
			}
			watched.Add(1)
			open.Add(int32(n))
			_, _ = w.Write([]byte(`{"number":41,"state":"open","title":"Bump","head":{"sha":"abc"}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"message":"Not Found"}`))
		}
	}))
	defer srv.Close()
	f := setupWith(t, &agenttools.Server{Forges: forgesOf{forge.NewGitHub(forge.Credential{Auth: "pat", Secret: "ghp", APIBaseURL: srv.URL})}})
	openTxs = func() (int, error) {
		conn, err := pgx.Connect(context.Background(), f.owner.Config().ConnString())
		if err != nil {
			return 0, err
		}
		defer func() { _ = conn.Close(context.Background()) }()
		var n int
		err = conn.QueryRow(context.Background(), `SELECT count(*) FROM pg_stat_activity WHERE datname = current_database()
			AND pid <> pg_backend_pid() AND state LIKE 'idle in transaction%'`).Scan(&n)
		return n, err
	}
	token := f.conductor(t)
	var out map[string]any
	if status := f.postAs(t, token, "wait_for", `{"deadline":"1h","conditions":[{"prEnded":{"pr":"other/lib#99"}}]}`, &out); status != 422 {
		t.Fatalf("not found: %d %v", status, out)
	}
	if n := f.count(t, `SELECT count(*) FROM conductor_waits`); n != 0 {
		t.Fatal("a refused prEnded wrote a wait")
	}
	out = nil
	if status := f.postAs(t, token, "wait_for", `{"deadline":"1h","conditions":[{"prEnded":{"pr":"other/lib#41","how":"merged"}}]}`, &out); status != 200 ||
		out["waitId"] == nil || out["holds"] != nil {
		t.Fatalf("wait_for prEnded: %d %v", status, out)
	}
	if n := f.count(t, `SELECT count(*) FROM events WHERE event_type = 'github.read' AND payload->>'tool' = 'wait_for' AND payload->>'repo' = 'other/lib'`); n != 1 {
		t.Fatalf("%d github.read events", n)
	}
	if watched.Load() == 0 || open.Load() != 0 {
		t.Fatalf("%d transactions open over %d pre-reads of the pull request", open.Load(), watched.Load())
	}
}
