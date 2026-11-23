package agenttools_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/marciomartins/dude/orchestrator/internal/agenttools"
	"github.com/marciomartins/dude/orchestrator/internal/capabilities"
	"github.com/marciomartins/dude/orchestrator/internal/forge"
	"github.com/marciomartins/dude/orchestrator/internal/lux"
	"github.com/marciomartins/dude/orchestrator/internal/servers"
)

// previewFixture serves the tools with the real preview service on the
// fixture's database: a project with a server marked to start in previews,
// its first repository on a fake GitHub that has the branch "feature", a
// conductor on the fixture's task and a brainstorm whose session is linked
// to the project.
type previewFixture struct {
	*fixture
	conductor, brainstorm string
	// The requests the fake GitHub has been sent.
	githubReads *atomic.Int64
}

func setupPreviews(t *testing.T) *previewFixture {
	t.Helper()
	reads := &atomic.Int64{}
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reads.Add(1)
		if r.URL.Path == "/repos/acme/web/git/ref/heads/feature" {
			_, _ = w.Write([]byte(`{"object":{"sha":"0123456789abcdef0123456789abcdef01234567"}}`))
			return
		}
		if r.URL.Path == "/repos/acme/web" {
			_, _ = w.Write([]byte(`{"id":1}`))
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(gh.Close)
	f := setupWithPreviews(t, previewLux{}, &agenttools.Server{
		Forges: forgesOf{forge.NewGitHub(forge.Credential{Auth: "pat", Secret: t.Name(), APIBaseURL: gh.URL})}})
	mustExec(t, f.owner, `INSERT INTO repositories (id, organization_id, project_id, name, url) VALUES ('repo_'||$1, $1, $2, 'web', 'https://github.com/acme/web')`,
		f.org, f.project)
	return &previewFixture{fixture: f, conductor: f.conductor(t), brainstorm: f.linkedSession(t, "ssn_preview"), githubReads: reads}
}

// setupWithPreviews serves srv with the real preview service, on lx, and
// gives the fixture's project a server "web" that starts in previews.
func setupWithPreviews(t *testing.T, lx lux.Client, srv *agenttools.Server) *fixture {
	t.Helper()
	svc := &servers.Service{PreviewDomain: "preview.example", Lux: lx, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	srv.Previews = servers.AgentPreviews{Service: svc}
	f := setupWith(t, srv)
	svc.DB = f.app
	mustExec(t, f.owner, `INSERT INTO project_servers (project_id, organization_id, name, port, command, autostart_in_previews)
		VALUES ($1, $2, 'web', 3000, 'npm start', true)`, f.project, f.org)
	return f
}

// linkedSession is a brainstorm's token in a new session id, linked to the
// fixture's project.
func (f *fixture) linkedSession(t *testing.T, id string) string {
	t.Helper()
	token := f.session(t, id, "", "")
	mustExec(t, f.owner, `INSERT INTO session_projects (session_id, project_id, organization_id) VALUES ($1, $2, $3)`, id, f.project, f.org)
	return token
}

// notGiven fails t unless token's Run neither lists tool, on GET /tools or
// MCP tools/list, nor can call it over POST or MCP. It returns the MCP list.
func (f *fixture) notGiven(t *testing.T, who, token, tool, args string) (mcpList map[string]bool) {
	t.Helper()
	jsonAPI, mcpList := f.listed(t, token)
	if jsonAPI[tool] || mcpList[tool] {
		t.Errorf("%s lists %s: GET /tools %v, MCP %v", who, tool, jsonAPI[tool], mcpList[tool])
	}
	if status, out := f.post(t, token, tool, args); status != 404 || out["error"] != "no tool "+tool+" for this Run" {
		t.Errorf("%s POST %s: %d %v", who, tool, status, out)
	}
	var mcpArgs map[string]any
	_ = json.Unmarshal([]byte(args), &mcpArgs)
	if _, refused := f.mcpCall(t, token, tool, mcpArgs); refused == "" {
		t.Errorf("%s MCP tools/call %s succeeded", who, tool)
	}
	return mcpList
}

// previewLux answers a preview's server read: its one server, asleep, at
// its URL. Anything else it is asked panics (nil lux.Client).
type previewLux struct{ lux.Client }

func (previewLux) ListServers(_ context.Context, _ string, labels ...string) ([]lux.TenantServer, error) {
	raw := []byte(`{"id":"srv_1","name":"web","url":"https://web-text-p.preview.example","state":"asleep","process":"stopped","port":3000}`)
	var s lux.TenantServer
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, err
	}
	return []lux.TenantServer{s}, nil
}

// listed are the tools a token is given on both list surfaces: GET /tools
// and MCP tools/list.
func (f *fixture) listed(t *testing.T, token string) (jsonAPI, mcpList map[string]bool) {
	t.Helper()
	jsonAPI = map[string]bool{}
	for name := range f.getTools(t, token) {
		jsonAPI[name] = true
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
	mcpList = map[string]bool{}
	for _, tl := range list.Tools {
		mcpList[tl.Name] = true
	}
	return jsonAPI, mcpList
}

// mcpCall calls a tool over MCP: its result decoded, or the refusal's text
// (protocol error or tool error) as refused.
func (f *fixture) mcpCall(t *testing.T, token, tool string, args map[string]any) (out map[string]any, refused string) {
	t.Helper()
	cs, err := f.connect(t, token)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: tool, Arguments: args})
	if err != nil {
		return nil, err.Error()
	}
	if res.IsError {
		return nil, res.Content[0].(*mcp.TextContent).Text
	}
	raw, _ := json.Marshal(res.StructuredContent)
	_ = json.Unmarshal(raw, &out)
	return out, ""
}

// The conductor lists and calls start_preview, on every surface, and a
// repeat is the same preview; a brainstorm is given none of it, listed or
// called (refused as a tool its Run does not have). The preview is the
// caller's task's, created by the agent.
func TestAConductorStartsItsTasksPreviewAndABrainstormCannot(t *testing.T) {
	f := setupPreviews(t)
	// The binding decision: the brainstorm's role is not granted the
	// conductor's preview.task.
	if g := capabilities.Default("brainstorm"); g.Has(capabilities.PreviewTask) || !capabilities.Default("conductor").Has(capabilities.PreviewTask) {
		t.Errorf("preview.task granted: conductor %v, brainstorm %v; want conductor only",
			capabilities.Default("conductor").Has(capabilities.PreviewTask), g.Has(capabilities.PreviewTask))
	}
	jsonAPI, mcpList := f.listed(t, f.conductor)
	if !jsonAPI["start_preview"] || !mcpList["start_preview"] {
		t.Fatalf("conductor: GET /tools %v, MCP tools/list %v; want start_preview on both", jsonAPI["start_preview"], mcpList["start_preview"])
	}
	if jsonAPI["preview_branch"] || mcpList["preview_branch"] {
		t.Errorf("conductor lists preview_branch: GET /tools %v, MCP %v", jsonAPI["preview_branch"], mcpList["preview_branch"])
	}
	status, first := f.post(t, f.conductor, "start_preview", `{}`)
	if status != 200 || first["id"] == nil || first["id"] == "" {
		t.Fatalf("POST start_preview: %d %v", status, first)
	}
	if sv, _ := first["servers"].([]any); len(sv) != 1 || sv[0].(map[string]any)["url"] != "https://web-text-p.preview.example" {
		t.Errorf("start_preview servers: %v", first["servers"])
	}
	status, again := f.post(t, f.conductor, "start_preview", ``)
	viaMCP, refused := f.mcpCall(t, f.conductor, "start_preview", map[string]any{})
	if status != 200 || refused != "" || again["id"] != first["id"] || viaMCP["id"] != first["id"] {
		t.Errorf("repeat: POST %d %v, MCP %v %q; want %v each time", status, again["id"], viaMCP["id"], refused, first["id"])
	}
	var previews int
	var task, actorType, actorID string
	if err := f.owner.QueryRow(context.Background(), `SELECT count(*) OVER (), r.task_id, e.actor_type, e.actor_id
		FROM runs r JOIN events e ON e.run_id = r.id AND e.event_type = 'run.created'
		WHERE r.kind = 'preview' AND r.organization_id = $1`, f.org).Scan(&previews, &task, &actorType, &actorID); err != nil {
		t.Fatal(err)
	}
	if previews != 1 || task != f.item || actorType != "agent" || actorID != "run_cond" || first["id"] == "" {
		t.Errorf("previews %d on %s, created by %s %s; want one on %s by agent run_cond", previews, task, actorType, actorID, f.item)
	}
	f.notGiven(t, "brainstorm", f.brainstorm, "start_preview", `{}`)
}

// The brainstorm lists and calls preview_branch, on every surface, and a
// repeat is the same preview of the linked project's branch, created by the
// agent with no task; a conductor is given none of it.
func TestABrainstormStartsALinkedProjectsBranchPreviewAndAConductorCannot(t *testing.T) {
	f := setupPreviews(t)
	// The binding decision: the conductor's role is not granted the
	// brainstorm's preview.branch.
	if g := capabilities.Default("conductor"); g.Has(capabilities.PreviewBranch) || !capabilities.Default("brainstorm").Has(capabilities.PreviewBranch) {
		t.Errorf("preview.branch granted: brainstorm %v, conductor %v; want brainstorm only",
			capabilities.Default("brainstorm").Has(capabilities.PreviewBranch), g.Has(capabilities.PreviewBranch))
	}
	jsonAPI, mcpList := f.listed(t, f.brainstorm)
	if !jsonAPI["preview_branch"] || !mcpList["preview_branch"] {
		t.Fatalf("brainstorm: GET /tools %v, MCP tools/list %v; want preview_branch on both", jsonAPI["preview_branch"], mcpList["preview_branch"])
	}
	status, first := f.post(t, f.brainstorm, "preview_branch", `{"project":"TEXT","branch":"feature"}`)
	if status != 200 || first["id"] == nil || first["branch"] != "feature" {
		t.Fatalf("POST preview_branch: %d %v", status, first)
	}
	status, again := f.post(t, f.brainstorm, "preview_branch", `{"branch":"feature"}`)
	viaMCP, refused := f.mcpCall(t, f.brainstorm, "preview_branch", map[string]any{"project": "TEXT", "branch": "feature"})
	if status != 200 || refused != "" || again["id"] != first["id"] || viaMCP["id"] != first["id"] {
		t.Errorf("repeat: POST %d %v, MCP %v %q; want %v each time", status, again["id"], viaMCP["id"], refused, first["id"])
	}
	var previews int
	var project, branch, actorType, actorID string
	var task *string
	if err := f.owner.QueryRow(context.Background(), `SELECT count(*) OVER (), r.project_id, r.preview_branch, r.task_id, e.actor_type, e.actor_id
		FROM runs r JOIN events e ON e.run_id = r.id AND e.event_type = 'run.created'
		WHERE r.kind = 'preview' AND r.organization_id = $1`, f.org).Scan(&previews, &project, &branch, &task, &actorType, &actorID); err != nil {
		t.Fatal(err)
	}
	if previews != 1 || project != f.project || branch != "feature" || task != nil || actorType != "agent" || actorID != "run_ssn_preview" {
		t.Errorf("previews %d of %s %s task %v, created by %s %s", previews, project, branch, task, actorType, actorID)
	}
	if status, out := f.post(t, f.brainstorm, "preview_branch", `{"branch":"gone"}`); status != 422 || out["code"] != "branch_not_found" {
		t.Errorf("a branch GitHub does not have: %d %v", status, out)
	}
	f.notGiven(t, "conductor", f.conductor, "preview_branch", `{"branch":"feature"}`)
}

// A brainstorm asking again and again for a branch GitHub does not have is
// refused each time, and each refusal spends its calls of the minute: once
// they are spent it is refused before GitHub is asked, so the reads stop.
func TestRefusedBranchPreviewsSpendTheMinutesCalls(t *testing.T) {
	f := setupPreviews(t)
	var limited int
	var readsAtLimit int64
	for i := range 70 {
		status, out := f.post(t, f.brainstorm, "preview_branch", `{"branch":"gone"}`)
		switch {
		case status == 422 && out["code"] == "branch_not_found":
			if limited > 0 {
				t.Fatalf("call %d reached GitHub after the budget was spent: %v", i+1, out)
			}
		case strings.HasPrefix(fmt.Sprint(out["error"]), "too many calls"):
			if limited == 0 {
				readsAtLimit = f.githubReads.Load()
			}
			limited++
		default:
			t.Fatalf("call %d: %d %v", i+1, status, out)
		}
	}
	if limited == 0 {
		t.Fatalf("70 refused calls in a minute were never limited; GitHub was asked %d times", f.githubReads.Load())
	}
	if reads := f.githubReads.Load(); reads != readsAtLimit {
		t.Errorf("GitHub reads grew from %d to %d after the budget was spent", readsAtLimit, reads)
	}
}

// agentLux is previewLux with an implementer's lux Run at work: its Run
// and its own server "api", which is not the preview's.
type agentLux struct{ previewLux }

func (agentLux) Get(_ context.Context, runID string) (lux.Run, error) {
	return lux.Run{ID: runID, State: "running", Host: "host-1"}, nil
}

func (agentLux) Servers(context.Context, string) ([]lux.Server, error) {
	var s lux.Server
	if err := json.Unmarshal([]byte(`{"name":"api","port":8080,"state":"ready","url":"https://api-impl.example"}`), &s); err != nil {
		return nil, err
	}
	return []lux.Server{s}, nil
}

// With an implementer at work on the task the task's Servers tab shows its
// Run, and the preview behind it: the conductor's start_preview still
// answers with the preview, its id and servers, not the implementer's, and
// starts one preview however often it is called.
func TestStartPreviewBehindAWorkingAgentAnswersWithThePreview(t *testing.T) {
	f := setupWithPreviews(t, agentLux{}, &agenttools.Server{})
	f.run(t, "run_impl", "implementer", "running")
	mustExec(t, f.owner, `UPDATE runs SET lux_run_id = 'lux_impl' WHERE id = 'run_impl'`)
	token := f.conductor(t)

	for i := range 2 {
		status, out := f.post(t, token, "start_preview", `{}`)
		var preview string
		if err := f.owner.QueryRow(context.Background(), `SELECT id FROM runs WHERE kind = 'preview' AND task_id = $1`, f.item).Scan(&preview); err != nil {
			t.Fatal(err)
		}
		if status != 200 || out["id"] != preview || out["id"] == "run_impl" {
			t.Fatalf("call %d: %d %v; want the preview %s", i+1, status, out, preview)
		}
		sv, _ := out["servers"].([]any)
		if len(sv) != 1 || sv[0].(map[string]any)["name"] != "web" || sv[0].(map[string]any)["url"] != "https://web-text-p.preview.example" {
			t.Errorf("call %d servers: %v; want the preview's web", i+1, out["servers"])
		}
	}
	var previews int
	if err := f.owner.QueryRow(context.Background(), `SELECT count(*) FROM runs WHERE kind = 'preview' AND organization_id = $1`, f.org).Scan(&previews); err != nil {
		t.Fatal(err)
	}
	if previews != 1 {
		t.Errorf("%d preview rows; want 1", previews)
	}
}

// recordingPreviews is a preview service that records what it is asked,
// and whether the tool's transaction had committed when it was asked
// (hooked to the Server by serve).
type recordingPreviews struct {
	mu    sync.Mutex
	calls []string
	// committed: the call's tool transaction is over (AfterTransaction ran
	// since BeforeCall); afterCommit, its value at each call.
	committed   bool
	afterCommit []bool
}

// serve hooks r to srv's transaction and returns srv.
func (r *recordingPreviews) serve(srv *agenttools.Server) *agenttools.Server {
	srv.Previews = r
	srv.BeforeCall = func(string) { r.mu.Lock(); r.committed = false; r.mu.Unlock() }
	srv.AfterTransaction = func(context.Context, string) { r.mu.Lock(); r.committed = true; r.mu.Unlock() }
	return srv
}

func (r *recordingPreviews) record(call string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, call)
	r.afterCommit = append(r.afterCommit, r.committed)
}

func (r *recordingPreviews) seen() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.calls...)
}

// outsideTx fails t for a call the service was given inside the tool's
// transaction.
func (r *recordingPreviews) outsideTx(t *testing.T) {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	for i, after := range r.afterCommit {
		if !after {
			t.Errorf("call %d (%s) reached the service inside the tool's transaction", i+1, r.calls[i])
		}
	}
}

func (r *recordingPreviews) StartPreview(_ context.Context, org, taskID, actor string, identity ...string) (agenttools.Preview, error) {
	r.record("task " + taskID + " by " + actor + " as " + strings.Join(identity, ","))
	return agenttools.Preview{ID: "run_preview_" + taskID, State: "pending"}, nil
}

func (r *recordingPreviews) StartBranchPreview(_ context.Context, _ *forge.GitHub, org, projectID, branch, actor string, identity ...string) (agenttools.Preview, error) {
	r.record("branch " + projectID + ":" + branch + " by " + actor + " as " + strings.Join(identity, ","))
	return agenttools.Preview{ID: "run_branch", State: "pending", Branch: branch}, nil
}

// A project not linked to the session is refused before the preview service
// is asked, whether named by key or by name; a linked one reaches it, with
// the agent as actor.
func TestPreviewBranchRefusesAnUnlinkedProjectBeforeTheService(t *testing.T) {
	rec := &recordingPreviews{}
	f := setupWith(t, rec.serve(&agenttools.Server{}))
	mustExec(t, f.owner, `INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES ('prj_other_'||$1, $1, 'Other', 'other_'||$1, 'OTH')`, f.org)
	token := f.linkedSession(t, "ssn_unlinked")
	for _, project := range []string{"OTH", "Other"} {
		status, out := f.post(t, token, "preview_branch", `{"project":"`+project+`","branch":"feature"}`)
		if status != 422 || out["error"] != "project "+project+" is not linked to this session" {
			t.Errorf("%s: %d %v", project, status, out)
		}
		if _, refused := f.mcpCall(t, token, "preview_branch", map[string]any{"project": project, "branch": "feature"}); !strings.Contains(refused, "is not linked to this session") {
			t.Errorf("%s over MCP: %q", project, refused)
		}
	}
	if calls := rec.seen(); len(calls) != 0 {
		t.Fatalf("the service was asked for an unlinked project: %q", calls)
	}
	if status, out := f.post(t, token, "preview_branch", `{"project":"TEXT","branch":"feature"}`); status != 200 || out["id"] != "run_branch" {
		t.Fatalf("linked: %d %v", status, out)
	}
	if calls := rec.seen(); len(calls) != 1 || calls[0] != "branch "+f.project+":feature by run_ssn_unlinked as agent," {
		t.Errorf("calls %q", calls)
	}
	rec.outsideTx(t)
}

// start_preview has no input for a task: one named is refused as an
// argument it does not take, and the service is only ever asked for the
// caller's own task, whichever task exists beside it.
func TestStartPreviewReachesOnlyTheCallersTask(t *testing.T) {
	rec := &recordingPreviews{}
	f := setupWith(t, rec.serve(&agenttools.Server{}))
	mustExec(t, f.owner, `INSERT INTO tasks (id, organization_id, project_id, number, title, status)
		VALUES ('wi_other_'||$1, $1, $2, 2, 'Other', 'running')`, f.org, f.project)
	token := f.conductor(t)
	other := "wi_other_" + f.org
	for _, args := range []string{`{"task":"` + other + `"}`, `{"taskId":"` + other + `"}`, `{"task_id":"TEXT-2"}`} {
		if status, out := f.post(t, token, "start_preview", args); status != 422 || !strings.Contains(out["error"].(string), "unknown field") {
			t.Errorf("%s: %d %v", args, status, out)
		}
	}
	if _, refused := f.mcpCall(t, token, "start_preview", map[string]any{"task": other}); refused == "" {
		t.Error("MCP start_preview with a task succeeded")
	}
	if calls := rec.seen(); len(calls) != 0 {
		t.Fatalf("a call naming a task reached the service: %q", calls)
	}
	if status, out := f.post(t, token, "start_preview", `{}`); status != 200 || out["id"] != "run_preview_"+f.item {
		t.Fatalf("start_preview: %d %v", status, out)
	}
	if calls := rec.seen(); len(calls) != 1 || calls[0] != "task "+f.item+" by run_cond as agent," {
		t.Errorf("calls %q", calls)
	}
	rec.outsideTx(t)
}

// Taken away, preview.task and preview.branch take their tools off every
// surface: neither role lists or calls its preview tool, and the rest of
// each role's tools are as its defaults give them. The reduced lists, as
// MCP tools/list gives them, are tool-lists-without-previews.golden.md.
func TestARunWithoutAPreviewGrantIsNotGivenItsPreviewTool(t *testing.T) {
	rec := &recordingPreviews{}
	srv := &agenttools.Server{Previews: rec}
	srv.ResolveCapabilitiesWith(func(_ context.Context, _ pgx.Tx, a capabilities.Agent) (capabilities.Grants, error) {
		return capabilities.Default(a.Role).Without(capabilities.PreviewTask, capabilities.PreviewBranch), nil
	})
	f := setupWith(t, srv)
	tokens := map[string]string{"conductor": f.conductor(t), "brainstorm": f.linkedSession(t, "ssn_less")}
	calls := map[string]string{"conductor": "start_preview", "brainstorm": "preview_branch"}
	var b strings.Builder
	for _, role := range []string{"conductor", "brainstorm"} {
		token, tool := tokens[role], calls[role]
		mcpList := f.notGiven(t, role+" without its preview grant", token, tool, `{"branch":"feature"}`)
		names := make([]string, 0, len(mcpList))
		for name := range mcpList {
			names = append(names, name)
		}
		slices.Sort(names)
		fmt.Fprintf(&b, "%q: %s\n", role, strings.Join(names, " "))
	}
	if got := rec.seen(); len(got) != 0 {
		t.Errorf("the service was asked: %q", got)
	}
	agenttools.Golden(t, "tool-lists-without-previews", b.String())
}
