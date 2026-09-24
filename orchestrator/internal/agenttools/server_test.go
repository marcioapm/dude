package agenttools_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/marciomartins/dude/orchestrator/internal/agenttools"
	"github.com/marciomartins/dude/orchestrator/internal/dbtest"
)

// A Run with a token, in a project with one work item already.
type fixture struct {
	owner         *pgx.Conn
	url           string
	org           string
	project, item string
}

func setup(t *testing.T) *fixture {
	t.Helper()
	app, owner := dbtest.Open(t)
	org := dbtest.Org(t, owner)
	f := &fixture{owner: owner, org: org, project: "prj_" + org, item: "wi_" + org}
	exec(t, owner, `INSERT INTO projects (id, organization_id, name, slug, key_prefix, next_work_item_number)
		VALUES ($1, $2, 'P', $1, 'TEXT', 2)`, f.project, org)
	exec(t, owner, `INSERT INTO epics (id, organization_id, project_id, title) VALUES ($1, $2, $3, 'Text utilities')`,
		"epc_"+org, org, f.project)
	exec(t, owner, `INSERT INTO work_items (id, organization_id, project_id, number, title, status)
		VALUES ($1, $2, $3, 1, 'Truncate long words', 'running')`, f.item, org, f.project)
	srv := httptest.NewServer((&agenttools.Server{DB: app}).Handler())
	t.Cleanup(srv.Close)
	f.url = srv.URL
	return f
}

// run makes a phase Run with a role and status, and returns its token.
func (f *fixture) run(t *testing.T, id, role, status string) string {
	t.Helper()
	token, hash := agenttools.NewToken()
	exec(t, f.owner, `INSERT INTO runs (id, organization_id, project_id, work_item_id, attempt, status, phase, role, mcp_token_hash)
		VALUES ($1, $2, $3, $4, 1, $5::run_status, 'implement', $6::agent_role, $7)`, id, f.org, f.project, f.item, status, role, hash)
	return token
}

func (f *fixture) connect(t *testing.T, token string) (*mcp.ClientSession, error) {
	t.Helper()
	client := mcp.NewClient(&mcp.Implementation{Name: "test-agent", Version: "1"}, nil)
	return client.Connect(context.Background(), &mcp.StreamableClientTransport{
		Endpoint: f.url, DisableStandaloneSSE: true, MaxRetries: -1,
		HTTPClient: &http.Client{Transport: bearer{token}},
	}, nil)
}

type bearer struct{ token string }

func (b bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	return http.DefaultTransport.RoundTrip(r)
}

func TestAnImplementerRecordsWorkItFoundAndSeesIt(t *testing.T) {
	f := setup(t)
	token := f.run(t, "run_impl", "implementer", "running")
	cs, err := f.connect(t, token)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()

	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "create_work_item", Arguments: map[string]any{
		"title": "Hyphenated words split wrongly", "goal": "Found while truncating: `re-enter` becomes `re-`.",
		"acceptanceCriteria": []string{"hyphenated words stay whole"}, "epic": "text utilities",
	}})
	if err != nil || res.IsError {
		t.Fatalf("create_work_item: %v %+v", err, res)
	}
	var created struct{ Key string }
	raw, _ := json.Marshal(res.StructuredContent)
	_ = json.Unmarshal(raw, &created)
	if created.Key != "TEXT-2" {
		t.Errorf("key = %q, want TEXT-2", created.Key)
	}

	// Marked as the agent's, in the epic, not started.
	var status, byRun, epic string
	if err := f.owner.QueryRow(context.Background(), `SELECT w.status::text, w.created_by_run_id, e.title
		FROM work_items w JOIN epics e ON e.id = w.epic_id WHERE w.number = 2 AND w.project_id = $1`, f.project).
		Scan(&status, &byRun, &epic); err != nil {
		t.Fatal(err)
	}
	if status != "received" || byRun != "run_impl" || epic != "Text utilities" {
		t.Errorf("created: status %s by %s in %s", status, byRun, epic)
	}
	// The call is in the ledger, on the calling Run.
	var calls int
	_ = f.owner.QueryRow(context.Background(), `SELECT count(*) FROM events WHERE run_id = 'run_impl'
		AND event_type = $1 AND payload->>'tool' = 'create_work_item'`, agenttools.EventType).Scan(&calls)
	if calls != 1 {
		t.Errorf("%d ledger events for the call", calls)
	}

	res, err = cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "list_work", Arguments: map[string]any{}})
	if err != nil || res.IsError {
		t.Fatalf("list_work: %v %+v", err, res)
	}
	raw, _ = json.Marshal(res.StructuredContent)
	for _, want := range []string{`"key":"TEXT-1"`, `"yours":true`, `"key":"TEXT-2"`, `"createdBy":"TEXT-1"`, `"Text utilities"`} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("list_work lacks %s: %s", want, raw)
		}
	}

	// A bad request is the agent's to fix, said plainly.
	res, err = cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "create_work_item",
		Arguments: map[string]any{"title": "x", "goal": "y", "epic": "no such epic"}})
	if err != nil || !res.IsError {
		t.Fatalf("an unknown epic was accepted: %v %+v", err, res)
	}
}

func TestAReviewerCannotCreateWork(t *testing.T) {
	f := setup(t)
	cs, err := f.connect(t, f.run(t, "run_rev", "reviewer", "running"))
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	list, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range list.Tools {
		names = append(names, tool.Name)
	}
	if strings.Join(names, ",") != "list_work" {
		t.Errorf("a reviewer sees %v", names)
	}
}

func TestOnlyALiveRunsTokenGetsIn(t *testing.T) {
	f := setup(t)
	ended := f.run(t, "run_done", "implementer", "completed")
	for name, token := range map[string]string{"no token": "", "a stranger's": "dude_run_nope", "an ended Run's": ended} {
		if _, err := f.connect(t, token); err == nil {
			t.Errorf("%s token was let in", name)
		}
	}
	req, _ := http.NewRequest("POST", f.url, strings.NewReader(`{}`))
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Errorf("no token: %d", res.StatusCode)
	}
}

func exec(t *testing.T, c *pgx.Conn, sql string, args ...any) {
	t.Helper()
	if _, err := c.Exec(context.Background(), sql, args...); err != nil {
		t.Fatal(err)
	}
}
