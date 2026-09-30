package orchestrator_test

// A whole delivery through the orchestrator's real code: the workflow
// runtime, the phase syncer, the translator and the PR sync, against a real
// Postgres, a fake lux and a fake GitHub backed by a real git repository.
//
// What it pins is the boundary: what dude sends lux, and what dude does with
// what lux reports. lux's own behaviour is its own tests' concern.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/marciomartins/dude/orchestrator/internal/agenttools"
	"github.com/marciomartins/dude/orchestrator/internal/api"
	"github.com/marciomartins/dude/orchestrator/internal/db"
	"github.com/marciomartins/dude/orchestrator/internal/dbtest"
	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/fakeagent"
	"github.com/marciomartins/dude/orchestrator/internal/fakegithub"
	"github.com/marciomartins/dude/orchestrator/internal/fakelux"
	"github.com/marciomartins/dude/orchestrator/internal/forge"
	"github.com/marciomartins/dude/orchestrator/internal/lux"
	"github.com/marciomartins/dude/orchestrator/internal/phases"
	"github.com/marciomartins/dude/orchestrator/internal/prs"
	"github.com/marciomartins/dude/orchestrator/internal/servers"
	"github.com/marciomartins/dude/orchestrator/internal/workflow"
)

var quiet = slog.New(slog.NewTextHandler(logSink(), nil))

func logSink() io.Writer {
	if os.Getenv("DUDE_TEST_LOG") != "" {
		return os.Stderr
	}
	return io.Discard
}

type world struct {
	t         *testing.T
	app       *db.DB
	owner     *pgx.Conn
	org       string
	project   string
	repoID    string
	gh        *fakegithub.Server
	web       *fakegithub.Server
	lux       *fakelux.Server
	runtime   *workflow.Runtime
	syncer    *phases.Syncer
	artifacts *phases.Artifacts
	prs       *prs.Syncer
	// Branch previews, and a task's servers.
	previews *servers.Previews
	// The key servers requests are made as (do).
	actor string
	// The orchestrator's internal API, as the backend calls it.
	api string
}

func newWorld(t *testing.T) *world {
	t.Helper()
	app, owner := dbtest.Open(t)
	w := &world{t: t, app: app, owner: owner, org: dbtest.Org(t, owner)}
	ctx := context.Background()

	// Two bare repositories with one commit on main, as GitHub would hold
	// them: the project's, and a second one (acme/web) a test may add.
	dir := t.TempDir()
	bare, webBare := bareRepo(t, dir, "target"), bareRepo(t, dir, "web")
	w.gh = fakegithub.New(bare, "acme/target")
	w.web = fakegithub.New(webBare, "acme/web")
	mux := http.NewServeMux()
	mux.Handle("/repos/acme/target/", w.gh.Handler())
	mux.Handle("/repos/acme/web/", w.web.Handler())
	mux.Handle("/acme/target.git/", w.gh.Handler())
	mux.Handle("/acme/web.git/", w.web.Handler())
	mux.Handle("POST /graphql", fakegithub.Graphql(w.gh, w.web))
	ghSrv := httptest.NewServer(mux)
	t.Cleanup(ghSrv.Close)

	// dude's scripted agent (internal/fakeagent), played by the fake lux.
	w.lux = fakelux.New(bare, "lux-key", nil)
	w.lux.RepoFor = func(url string) string {
		if strings.Contains(url, "/web.git") {
			return webBare
		}
		return bare
	}
	luxSrv := httptest.NewServer(w.lux.Handler())
	t.Cleanup(luxSrv.Close)
	t.Cleanup(w.lux.Close)

	w.project, w.repoID = "prj_"+w.org, "repo_"+w.org
	models := `{"implementer":{"model":"fake/scripted"},"reviewer":{"model":"fake/scripted"},"simplifier":{"model":"fake/scripted"}}`
	mustExec(t, owner, `INSERT INTO projects (id, organization_id, name, slug, key_prefix, agent_models, runtime_image)
		VALUES ($1, $2, 'P', $1, 'P', $3::jsonb, 'agent:test')`, w.project, w.org, models)
	mustExec(t, owner, `INSERT INTO repositories (id, organization_id, project_id, name, url, default_branch)
		VALUES ($1, $2, $3, 'target', 'git://127.0.0.1/acme/target.git', 'main')`, w.repoID, w.org, w.project)
	mustExec(t, owner, `INSERT INTO forge_credentials (id, organization_id, auth, secret, api_base_url)
		VALUES ($1, $2, 'pat', 'ghp_test', $3)`, "forge_"+w.org, w.org, ghSrv.URL)

	forges := forge.Resolver{DB: app}
	w.runtime = workflow.New(app, "test", quiet)
	w.runtime.Register(delivery.Workflow(&delivery.Store{DB: app}, forges))
	w.syncer = &phases.Syncer{DB: app, Lux: lux.New(luxSrv.URL, "lux-key"), Forges: forges, Log: quiet,
		Agent: phases.AgentConfig{DefaultImage: "default:img", LLMURL: "https://llm.example/v1", LLMKey: "secret-key"}}
	t.Cleanup(w.syncer.Stop)
	w.artifacts = &phases.Artifacts{DB: app, Lux: w.syncer.Lux}
	w.prs = &prs.Syncer{DB: app, Forges: forges, Log: quiet,
		Signal: func(ctx context.Context, org, wf, name string, payload any, key string) error {
			return w.runtime.Signal(ctx, org, wf, name, payload, key)
		}}
	w.previews = &servers.Previews{Service: &servers.Service{DB: app, Lux: w.syncer.Lux, Log: quiet,
		ConsoleURL: "https://console.lux.test/"}, Forges: forges, DefaultImage: "default:img"}
	t.Cleanup(w.previews.Stop)
	apiSrv := httptest.NewServer((&api.Server{DB: app, Lux: w.syncer.Lux, Workflow: w.runtime, Token: "svc", Log: quiet, Kick: func() {},
		Forges: forges, PRs: w.prs, Servers: w.previews.Service}).Handler())
	t.Cleanup(apiSrv.Close)
	w.api = apiSrv.URL
	_ = ctx
	return w
}

// call posts to the orchestrator's internal API as the backend would.
func (w *world) call(path string, body any) (int, map[string]any) {
	w.t.Helper()
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest("POST", w.api+path, bytes.NewReader(b))
	req.Header.Set("Authorization", "Bearer svc")
	req.Header.Set("X-Dude-Organization", w.org)
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		w.t.Fatal(err)
	}
	defer res.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(res.Body).Decode(&out)
	return res.StatusCode, out
}

// get reads from the orchestrator's internal API as an organization.
func (w *world) get(path, org string) (int, string) {
	w.t.Helper()
	req, _ := http.NewRequest("GET", w.api+path, nil)
	req.Header.Set("Authorization", "Bearer svc")
	req.Header.Set("X-Dude-Organization", org)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		w.t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(b)
}

// bareRepo makes a bare repository with one commit on main.
func bareRepo(t *testing.T, dir, name string) string {
	t.Helper()
	bare, seed := filepath.Join(dir, name+".git"), filepath.Join(dir, "seed-"+name)
	for _, args := range [][]string{
		{"init", "-q", "--bare", "-b", "main", bare},
		{"init", "-q", "-b", "main", seed},
		{"-C", seed, "-c", "user.name=t", "-c", "user.email=t@x", "commit", "-q", "--allow-empty", "-m", "initial"},
		{"-C", seed, "push", "-q", bare, "main"},
	} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	return bare
}

func mustExec(t *testing.T, c *pgx.Conn, sql string, args ...any) {
	t.Helper()
	if _, err := c.Exec(context.Background(), sql, args...); err != nil {
		t.Fatal(err)
	}
}

func (w *world) task() string {
	id := fmt.Sprintf("wi_%d", time.Now().UnixNano())
	mustExec(w.t, w.owner, `INSERT INTO tasks (id, organization_id, project_id, number, title, goal, acceptance_criteria)
		VALUES ($1, $2, $3, (SELECT count(*) + 1 FROM tasks WHERE project_id = $3), 'Greet people', 'Say hello',
		'["it greets"]'::jsonb)`, id, w.org, w.project)
	return id
}

func (w *world) deliver(taskID string) string {
	// As the API's deliver does: a project's only repository is named.
	if err := w.app.InOrg(context.Background(), w.org, func(tx pgx.Tx) error {
		return delivery.NameOnlyRepository(context.Background(), tx, taskID)
	}); err != nil {
		w.t.Fatal(err)
	}
	return w.start(taskID, delivery.DefaultPolicy())
}

// start starts the delivery workflow as the API does, with a policy.
func (w *world) start(taskID string, policy delivery.Policy) string {
	id, _, err := w.runtime.Start(context.Background(), workflow.StartOptions{
		Type: delivery.WorkflowType, OrganizationID: w.org, IdempotencyKey: "delivery:" + taskID, TaskID: taskID,
		Input: delivery.State{TaskID: taskID, ProjectID: w.project,
			Policy: policy, Branch: delivery.BranchFor(taskID, 1)},
	})
	if err != nil {
		w.t.Fatal(err)
	}
	return id
}

// pump runs every loop once, as the orchestrator's main does continuously.
func (w *world) pump() {
	ctx := context.Background()
	for range 5 {
		if _, err := w.runtime.Tick(ctx, 10); err != nil {
			w.t.Fatal(err)
		}
	}
	if _, err := w.syncer.Sweep(ctx); err != nil {
		w.t.Fatal(err)
	}
	if _, err := w.artifacts.Sweep(ctx); err != nil {
		w.t.Fatal(err)
	}
	if _, err := w.previews.Sweep(ctx); err != nil {
		w.t.Fatal(err)
	}
	if _, err := phases.NotifyFinished(ctx, w.app, func(ctx context.Context, org, wf, runID, status string) error {
		return w.runtime.Signal(ctx, org, wf, delivery.SignalPhaseFinished, map[string]string{"runId": runID, "status": status}, "phase-finished:"+runID)
	}); err != nil {
		w.t.Fatal(err)
	}
}

// until pumps until cond holds, or fails the test with what it last saw.
func (w *world) until(what string, cond func() bool) {
	w.t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		w.pump()
		if cond() {
			return
		}
		time.Sleep(30 * time.Millisecond)
	}
	w.t.Fatalf("timed out waiting for %s\nruns:\n%s", what, w.describeRuns())
}

func (w *world) describeRuns() string {
	rows, _ := w.owner.Query(context.Background(), `SELECT phase::text, status::text, COALESCE(error, ''), COALESCE(lux_state, ''),
		turn_done_at IS NOT NULL FROM runs WHERE organization_id = $1 ORDER BY created_at`, w.org)
	defer rows.Close()
	var b strings.Builder
	for rows.Next() {
		var phase, status, errText, luxState string
		var done bool
		_ = rows.Scan(&phase, &status, &errText, &luxState, &done)
		fmt.Fprintf(&b, "  %-9s %-9s lux=%-9s turnDone=%v %s\n", phase, status, luxState, done, errText)
	}
	var wf string
	_ = w.owner.QueryRow(context.Background(), `SELECT step || ' ' || status::text || ' ' || COALESCE(last_error, '') FROM workflow_runs WHERE organization_id = $1`, w.org).Scan(&wf)
	return b.String() + "workflow: " + wf
}

func (w *world) taskStatus(id string) string {
	var s string
	_ = w.owner.QueryRow(context.Background(), `SELECT status::text FROM tasks WHERE id = $1`, id).Scan(&s)
	return s
}

func (w *world) count(sql string, args ...any) int {
	var n int
	if err := w.owner.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		w.t.Fatal(err)
	}
	return n
}

func TestADeliveryReachesAPullRequestAndAMergeFinishesIt(t *testing.T) {
	w := newWorld(t)
	wi := w.task()
	w.deliver(wi)

	w.until("a pull request", func() bool { return len(w.gh.Pulls()) == 1 })

	pr := w.gh.Pulls()[0]
	branch := delivery.BranchFor(wi, 1)
	if pr.Head != branch || pr.Base != "main" {
		t.Errorf("PR head=%s base=%s, want %s into main", pr.Head, pr.Base, branch)
	}
	// Every publishing phase reached the branch, by fast-forward.
	got := w.gh.Log(branch)
	if len(got) != 4 || !strings.HasPrefix(got[0], "Simplify") || !strings.HasPrefix(got[1], "Address review findings") ||
		!strings.HasPrefix(got[2], "Add FACTORY.md") {
		t.Errorf("branch history = %v, want implement, fix and simplify on top of main", got)
	}
	// Each phase pushed its own branch, and dude cleaned them up.
	if out, _ := exec.Command("git", "-C", w.gh.Repo, "branch", "--list", "dude/"+wi+"/run-*").Output(); len(strings.TrimSpace(string(out))) > 0 {
		t.Errorf("per-run branches left behind:\n%s", out)
	}
	if s := w.taskStatus(wi); s != "review" {
		t.Errorf("task = %s, want review (waiting on people)", s)
	}

	// The loop went round once: one blocking finding, one fix, then clean.
	phaseCounts := map[string]int{}
	for _, r := range w.lux.Runs() {
		var spec struct {
			Labels map[string]string `json:"labels"`
		}
		_ = json.Unmarshal(r.Spec, &spec)
		phaseCounts[spec.Labels["dude.phase"]]++
	}
	if phaseCounts["implement"] != 1 || phaseCounts["review"] != 2 || phaseCounts["fix"] != 1 || phaseCounts["simplify"] != 1 {
		t.Errorf("runs per phase = %v", phaseCounts)
	}
	if n := w.count(`SELECT count(*) FROM review_findings WHERE task_id = $1 AND status = 'open'`, wi); n != 0 {
		t.Errorf("%d findings still open", n)
	}

	w.gh.Merge(1)
	// No webhook in this test: the reconciler is the backstop that notices.
	w.until("the task to finish", func() bool {
		_, _ = w.prs.Reconcile(context.Background(), 0)
		return w.taskStatus(wi) == "done"
	})
}

// addWeb adds acme/web to the project and names both repositories on the
// task, the given access for web.
func (w *world) addWeb(wi, access string) string {
	webID := "repo_web_" + w.org
	mustExec(w.t, w.owner, `INSERT INTO repositories (id, organization_id, project_id, name, url, default_branch)
		VALUES ($1, $2, $3, 'web', 'git://127.0.0.1/acme/web.git', 'main') ON CONFLICT DO NOTHING`, webID, w.org, w.project)
	mustExec(w.t, w.owner, `INSERT INTO task_repositories (organization_id, task_id, repository_id, access)
		VALUES ($1, $2, $3, 'write'), ($1, $2, $4, $5::repository_access)`, w.org, wi, w.repoID, webID, access)
	return webID
}

func TestWorkAcrossTwoRepositoriesOpensAPullRequestInEachAndFinishesWhenBothMerge(t *testing.T) {
	w := newWorld(t)
	scripted := w.lux.Decide
	w.lux.Decide = func(spec map[string]any) fakelux.Behaviour {
		b := scripted(spec)
		labels, _ := spec["labels"].(map[string]any)
		if labels["dude.phase"] == "implement" {
			b.Commit = map[string]string{"target:API.md": "api\n", "web:PAGE.md": "page\n"}
		}
		return b
	}
	wi := w.task()
	w.addWeb(wi, "write")
	w.deliver(wi)
	w.until("a pull request in each repository", func() bool { return len(w.gh.Pulls()) == 1 && len(w.web.Pulls()) == 1 })

	branch := delivery.BranchFor(wi, 1)
	if log := w.web.Log(branch); len(log) < 2 || !strings.Contains(strings.Join(log, "\n"), "Add FACTORY.md") {
		t.Errorf("web's branch = %v, want the implementer's commit", log)
	}
	// The fixer and simplifier changed only target: web's PR stays at the
	// implementer's commit, and both name each other.
	if body := w.web.Pull(1).Body; !strings.Contains(body, "acme/target") {
		t.Errorf("web's PR does not name its sibling:\n%s", body)
	}
	if body := w.gh.Pull(1).Body; !strings.Contains(body, "acme/web") {
		t.Errorf("target's PR does not name its sibling:\n%s", body)
	}
	// The implementer was given both, each at its default branch.
	var spec struct {
		Workload struct{ Workdir string } `json:"workload"`
		Git      struct {
			Repositories []struct{ Name, Path string } `json:"repositories"`
		} `json:"git"`
	}
	_ = json.Unmarshal(w.lux.Runs()[0].Spec, &spec)
	if len(spec.Git.Repositories) != 2 || spec.Workload.Workdir != "/workspace" {
		t.Errorf("implementer's spec: workdir %q, repositories %+v", spec.Workload.Workdir, spec.Git.Repositories)
	}

	// One merged is not done; both merged is.
	w.web.Merge(1)
	_, _ = w.prs.Reconcile(context.Background(), 0)
	w.pump()
	if s := w.taskStatus(wi); s == "done" {
		t.Fatalf("done with target's pull request still open")
	}
	w.gh.Merge(1)
	w.until("the task to finish", func() bool {
		_, _ = w.prs.Reconcile(context.Background(), 0)
		return w.taskStatus(wi) == "done"
	})
}

func TestARepositoryToReadIsClonedButNeverPushed(t *testing.T) {
	w := newWorld(t)
	wi := w.task()
	w.addWeb(wi, "read")
	w.deliver(wi)
	w.until("a pull request", func() bool { return len(w.gh.Pulls()) == 1 })
	var spec struct {
		Git struct {
			Repositories []struct {
				Name string
				Push *bool
			} `json:"repositories"`
		} `json:"git"`
	}
	_ = json.Unmarshal(w.lux.Runs()[0].Spec, &spec)
	var web *bool
	for _, r := range spec.Git.Repositories {
		if r.Name == "web" {
			web = r.Push
		}
	}
	if len(spec.Git.Repositories) != 2 || web == nil || *web {
		t.Errorf("web should be cloned with push: false: %+v", spec.Git.Repositories)
	}
	if len(w.web.Pulls()) != 0 || w.web.SHA(delivery.BranchFor(wi, 1)) != "" {
		t.Errorf("a repository to read was pushed or got a pull request")
	}
}

func TestWorkOnNoRepositoryEndsWithWhatTheAgentPublished(t *testing.T) {
	w := newWorld(t)
	// A second repository, so none is implied: this work names none.
	mustExec(t, w.owner, `INSERT INTO repositories (id, organization_id, project_id, name, url, default_branch)
		VALUES ($1, $2, $3, 'web', 'git://127.0.0.1/acme/web.git', 'main')`, "repo_web_"+w.org, w.org, w.project)
	wi := w.task()
	w.deliver(wi)
	w.until("the delivery to end", func() bool {
		return w.count(`SELECT count(*) FROM workflow_runs WHERE task_id = $1 AND status = 'completed'`, wi) == 1
	})
	if s := w.taskStatus(wi); s != "review" {
		var why string
		_ = w.owner.QueryRow(context.Background(), `SELECT payload::text FROM events WHERE task_id = $1
			AND event_type IN ('question.asked', 'task.status_changed') ORDER BY cursor DESC LIMIT 1`, wi).Scan(&why)
		t.Fatalf("task = %s (%s), want review: ready to read", s, why)
	}
	if len(w.gh.Pulls())+len(w.web.Pulls()) != 0 {
		t.Errorf("work on no repository opened a pull request")
	}
	var spec lux.Spec
	_ = json.Unmarshal(w.lux.Runs()[0].Spec, &spec)
	if spec.Git != nil && len(spec.Git.Repositories) != 0 {
		t.Errorf("work on no repository cloned something")
	}
	if n := w.count(`SELECT count(*) FROM artifacts a JOIN runs r ON r.id = a.run_id WHERE r.task_id = $1`, wi); n == 0 {
		t.Errorf("nothing published")
	}
}

func TestWhatAnAgentPublishesIsKeptWithTheTask(t *testing.T) {
	w := newWorld(t)
	wi := w.task()
	w.deliver(wi)
	w.until("the implementer's notes", func() bool {
		return w.count(`SELECT count(*) FROM artifacts a JOIN runs r ON r.id = a.run_id WHERE r.task_id = $1`, wi) == 1
	})
	var name, ctype, key, phase string
	var size int64
	if err := w.owner.QueryRow(context.Background(), `SELECT a.name, a.content_type, a.size_bytes, a.storage_key, r.phase::text
		FROM artifacts a JOIN runs r ON r.id = a.run_id WHERE r.task_id = $1`, wi).
		Scan(&name, &ctype, &size, &key, &phase); err != nil {
		t.Fatal(err)
	}
	if name != fakeagent.Notes || !strings.HasPrefix(ctype, "text/markdown") || size == 0 || key == "" || phase != "implement" {
		t.Errorf("artifact = %s %s %d bytes key=%q from %s", name, ctype, size, key, phase)
	}
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'artifact.created'`, wi); n != 1 {
		t.Errorf("%d artifact.created events", n)
	}

	// Collected once, however often the collector looks, and nothing is left
	// due once every exit is reported.
	w.until("a pull request", func() bool { return len(w.gh.Pulls()) == 1 })
	w.until("every exit collected", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND artifacts_due_at IS NOT NULL`, wi) == 0
	})
	if n := w.count(`SELECT count(*) FROM artifacts a JOIN runs r ON r.id = a.run_id WHERE r.task_id = $1`, wi); n != 1 {
		t.Errorf("%d artifacts, want the implementer's one", n)
	}

	// Its bytes come from lux, through the orchestrator, only for its own
	// organization.
	var id string
	_ = w.owner.QueryRow(context.Background(), `SELECT a.id FROM artifacts a JOIN runs r ON r.id = a.run_id WHERE r.task_id = $1`, wi).Scan(&id)
	status, body := w.get("/internal/artifacts/"+id+"/content", w.org)
	if status != 200 || !strings.Contains(body, "# What changed") {
		t.Errorf("content = %d %q", status, body)
	}
	if status, _ := w.get("/internal/artifacts/"+id+"/content", "org_other"); status != 404 {
		t.Errorf("another organization read it: %d", status)
	}
}

func TestAPausedAgentsArtifactsAreCollectedAndItsNextExitsToo(t *testing.T) {
	w := newWorld(t)
	w.lux.Decide = func(map[string]any) fakelux.Behaviour {
		return fakelux.Behaviour{Hang: true, Reply: "Done after resume.", Commit: map[string]string{"A.md": "a\n"},
			Publish: map[string]string{"plan.md": "# Plan\n"}}
	}
	wi := w.task()
	w.deliver(wi)
	w.until("the agent to be working", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND status = 'running'`, wi) == 1
	})
	var runID string
	_ = w.owner.QueryRow(context.Background(), `SELECT id FROM runs WHERE task_id = $1`, wi).Scan(&runID)
	// Nothing published before the pause (the hanging agent never replied),
	// and a pause is still an exit that is looked at, then settled.
	mustExec(t, w.owner, `UPDATE runs SET control = 'pause_graceful' WHERE id = $1`, runID)
	w.until("the pause's exit to be collected", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'paused' AND artifacts_due_at IS NULL`, runID) == 1
	})
	mustExec(t, w.owner, `UPDATE runs SET control = 'resume' WHERE id = $1`, runID)
	mustExec(t, w.owner, `INSERT INTO directives (id, organization_id, task_id, run_id, text) VALUES ('dir_a', $1, $2, $3, 'go')`,
		w.org, wi, runID)
	// The resumed placement publishes, and its exit is collected too.
	w.until("the second placement's artifact", func() bool {
		return w.count(`SELECT count(*) FROM artifacts WHERE run_id = $1 AND name = 'plan.md' AND epoch = 2`, runID) == 1
	})
}

func TestAnExitLuxWillNeverReportIsNotWaitedFor(t *testing.T) {
	w := newWorld(t)
	w.lux.Decide = func(map[string]any) fakelux.Behaviour { return fakelux.Behaviour{FailToStart: true} }
	wi := w.task()
	w.deliver(wi)
	// The agent never started, so lux sends no snapshot for that exit: the
	// collector sees that and settles at once rather than asking for a day.
	w.until("the failed start's exit to be settled", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND status = 'failed' AND artifacts_due_at IS NULL`, wi) == 1
	})
}

func TestAnAgentIsGivenDudesToolsAsItsOwnRun(t *testing.T) {
	w := newWorld(t)
	tools := httptest.NewServer((&agenttools.Server{DB: w.app, Log: quiet}).Handler())
	t.Cleanup(tools.Close)
	w.syncer.Agent.ToolsURL = "http://10.9.8.7:3120/mcp"
	w.syncer.Agent.ToolsService = true
	w.lux.Decide = func(map[string]any) fakelux.Behaviour { return fakelux.Behaviour{Hang: true} }
	wi := w.task()
	w.deliver(wi)
	w.until("the implementer to be submitted", func() bool { return len(w.lux.Runs()) >= 1 })

	var spec lux.Spec
	_ = json.Unmarshal(w.lux.Runs()[0].Spec, &spec)
	// No wall-clock limit: agents work for days, and a parked one is not
	// running.
	if spec.Timeout != "" {
		t.Errorf("the spec sets a timeout: %q", spec.Timeout)
	}
	// Served by lux inside the container, which adds the token: the MCP
	// server names the service, and carries no header of its own.
	if len(spec.Workload.Services) != 1 || spec.Workload.Services[0].URL != "http://10.9.8.7:3120/mcp" ||
		spec.Workload.Services[0].Headers[0].Secret != "DUDE_TOOLS_AUTH" || !spec.Workload.Services[0].Loopback {
		t.Fatalf("services = %+v", spec.Workload.Services)
	}
	if len(spec.Workload.MCPServers) != 1 || spec.Workload.MCPServers[0].Service != "dude" ||
		spec.Workload.MCPServers[0].URL != "" || len(spec.Workload.MCPServers[0].Headers) != 0 {
		t.Fatalf("mcp servers = %+v, want the service, no url or headers", spec.Workload.MCPServers)
	}
	var auth string
	for _, s := range spec.Secrets {
		if s.Name == "DUDE_TOOLS_AUTH" {
			auth = s.Value
		}
	}
	// Reachable: an address is allowed as an address.
	var allowed bool
	for _, e := range spec.Network.Egress {
		allowed = allowed || e.CIDR == "10.9.8.7/32"
	}
	if !allowed {
		t.Errorf("egress = %+v, want the tools' address", spec.Network.Egress)
	}

	// The token in the spec is this Run's: it can list its work.
	client := mcp.NewClient(&mcp.Implementation{Name: "agent", Version: "1"}, nil)
	cs, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{Endpoint: tools.URL,
		DisableStandaloneSSE: true, MaxRetries: -1,
		HTTPClient: &http.Client{Transport: headerTransport{"Authorization", auth}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "list_tasks", Arguments: map[string]any{}})
	if err != nil || res.IsError {
		t.Fatalf("list_tasks: %v %+v", err, res)
	}
	raw, _ := json.Marshal(res.StructuredContent)
	if !strings.Contains(string(raw), `"yours":true`) {
		t.Errorf("the Run does not see its own task: %s", raw)
	}
	// Once the Run is over, the token is too.
	mustExec(t, w.owner, `UPDATE runs SET status = 'aborted', control = 'abort' WHERE task_id = $1`, wi)
	if _, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{Endpoint: tools.URL,
		DisableStandaloneSSE: true, MaxRetries: -1,
		HTTPClient: &http.Client{Transport: headerTransport{"Authorization", auth}}}, nil); err == nil {
		t.Errorf("an aborted Run's token still works")
	}
}

type headerTransport struct{ name, value string }

func (h headerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set(h.name, h.value)
	return http.DefaultTransport.RoundTrip(r)
}

// names makes a task work on a repository, as the dialog would.
func (w *world) names(wi, repoID string) {
	mustExec(w.t, w.owner, `INSERT INTO task_repositories (organization_id, task_id, repository_id)
		VALUES ($1, $2, $3)`, w.org, wi, repoID)
}

// callTool calls one of dude's tools as a Run's agent, with the token its
// spec carries.
func (w *world) callTool(tools, luxRunSpec string, tool string, args string) (int, string) {
	w.t.Helper()
	var spec lux.Spec
	_ = json.Unmarshal([]byte(luxRunSpec), &spec)
	var auth string
	for _, s := range spec.Secrets {
		if s.Name == "DUDE_TOOLS_AUTH" {
			auth = s.Value
		}
	}
	req, _ := http.NewRequest("POST", tools+"/tools/"+tool, strings.NewReader(args))
	req.Header.Set("Authorization", auth)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		w.t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(b)
}

func TestAnAgentIsGivenARepositoryAPersonApproved(t *testing.T) {
	w := newWorld(t)
	tools := httptest.NewServer((&agenttools.Server{DB: w.app, Log: quiet}).Handler())
	t.Cleanup(tools.Close)
	w.syncer.Agent.ToolsURL = tools.URL
	mustExec(t, w.owner, `INSERT INTO repositories (id, organization_id, project_id, name, url, default_branch)
		VALUES ($1, $2, $3, 'web', 'git://127.0.0.1/acme/web.git', 'main')`, "repo_web_"+w.org, w.org, w.project)
	w.lux.Decide = func(map[string]any) fakelux.Behaviour {
		return fakelux.Behaviour{Hang: true, Reply: "Done.", Commit: map[string]string{"A.md": "a\n"}}
	}
	wi := w.task()
	w.names(wi, w.repoID)
	w.deliver(wi)
	w.until("the agent to be working", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND status = 'running'`, wi) == 1
	})

	status, body := w.callTool(tools.URL, string(w.lux.Runs()[0].Spec), "request_repository",
		`{"repository":"acme/web","reason":"the API change needs the web client updated"}`)
	if status != 200 {
		t.Fatalf("request: %d %s", status, body)
	}
	var req struct{ RequestID string }
	_ = json.Unmarshal([]byte(body), &req)
	if status, body := w.call("/internal/repository-requests/"+req.RequestID+"/decide", map[string]any{"approve": true}); status != 200 {
		t.Fatalf("approve: %d %v", status, body)
	}

	// Paused, resumed with web added, and told where it is — one lux Run.
	w.until("lux to report web cloned", func() bool {
		return w.count(`SELECT count(*) FROM repository_requests WHERE id = $1 AND status = 'cloned'`, req.RequestID) == 1
	})
	w.until("the resumed clone outcome in the ledger", func() bool {
		return w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'git.clone'
			AND payload->>'repo' = 'web' AND payload->>'status' = 'cloned' AND payload->>'requestId' IS NOT NULL`, wi) == 1
	})
	r := w.lux.Runs()[0]
	if len(w.lux.Runs()) != 1 || r.Resumed != 1 {
		t.Fatalf("lux runs %d, resumed %d", len(w.lux.Runs()), r.Resumed)
	}
	var spec lux.Spec
	_ = json.Unmarshal(r.Spec, &spec)
	names := []string{}
	for _, repo := range spec.Git.Repositories {
		names = append(names, repo.Name)
	}
	if strings.Join(names, ",") != "target,web" {
		t.Errorf("the Run's repositories after resume: %v", names)
	}
	w.until("the agent to hear it", func() bool {
		return len(r.Inputs) > 0 && strings.Contains(r.Inputs[0], "web is now checked out at /workspace/repos/web")
	})
	// The task names it now, read only, for every later phase.
	if n := w.count(`SELECT count(*) FROM task_repositories WHERE task_id = $1 AND access = 'read'`, wi); n != 1 {
		t.Errorf("task's read repositories: %d", n)
	}
}

func TestAResumedCloneFailureRetainsApprovalSemantics(t *testing.T) {
	w := newWorld(t)
	tools := httptest.NewServer((&agenttools.Server{DB: w.app, Log: quiet}).Handler())
	t.Cleanup(tools.Close)
	w.syncer.Agent.ToolsURL = tools.URL
	mustExec(t, w.owner, `INSERT INTO repositories (id, organization_id, project_id, name, url, default_branch)
		VALUES ($1, $2, $3, 'web', 'git://127.0.0.1/acme/web.git', 'missing-ref')`, "repo_web_"+w.org, w.org, w.project)
	w.lux.Decide = func(map[string]any) fakelux.Behaviour { return fakelux.Behaviour{Hang: true} }
	wi := w.task()
	w.names(wi, w.repoID)
	w.deliver(wi)
	w.until("agent running", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND status = 'running'`, wi) == 1
	})
	status, body := w.callTool(tools.URL, string(w.lux.Runs()[0].Spec), "request_repository", `{"repository":"web","reason":"read client"}`)
	if status != 200 {
		t.Fatalf("request: %d %s", status, body)
	}
	var req struct{ RequestID string }
	if err := json.Unmarshal([]byte(body), &req); err != nil {
		t.Fatal(err)
	}
	if status, body := w.call("/internal/repository-requests/"+req.RequestID+"/decide", map[string]any{"approve": true}); status != 200 {
		t.Fatalf("approve: %d %v", status, body)
	}
	w.until("approval failed with lux error", func() bool {
		return w.count(`SELECT count(*) FROM repository_requests WHERE id = $1 AND status = 'failed' AND error = 'ref not found'`, req.RequestID) == 1
	})
	if n := w.count(`SELECT count(*) FROM task_repositories WHERE task_id = $1 AND repository_id = $2`, wi, "repo_web_"+w.org); n != 0 {
		t.Fatalf("failed repository still attached: %d", n)
	}
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'git.clone'
		AND payload->>'repo' = 'web' AND payload->>'status' = 'failed' AND payload->>'error' = 'ref not found'
		AND payload->>'requestId' IS NOT NULL`, wi); n != 1 {
		t.Fatalf("resume clone failures: %d", n)
	}
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'repository.clone_failed'`, wi); n != 1 {
		t.Fatalf("approval failure events: %d", n)
	}
}

func TestAPersonsPauseIsNotUndoneByAnApproval(t *testing.T) {
	w := newWorld(t)
	tools := httptest.NewServer((&agenttools.Server{DB: w.app, Log: quiet}).Handler())
	t.Cleanup(tools.Close)
	w.syncer.Agent.ToolsURL = tools.URL
	mustExec(t, w.owner, `INSERT INTO repositories (id, organization_id, project_id, name, url, default_branch)
		VALUES ($1, $2, $3, 'web', 'git://127.0.0.1/acme/web.git', 'main')`, "repo_web_"+w.org, w.org, w.project)
	w.lux.Decide = func(map[string]any) fakelux.Behaviour { return fakelux.Behaviour{Hang: true} }
	wi := w.task()
	w.names(wi, w.repoID)
	w.deliver(wi)
	w.until("the agent to be working", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND status = 'running'`, wi) == 1
	})
	var runID string
	_ = w.owner.QueryRow(context.Background(), `SELECT id FROM runs WHERE task_id = $1`, wi).Scan(&runID)
	_, body := w.callTool(tools.URL, string(w.lux.Runs()[0].Spec), "request_repository", `{"repository":"web","reason":"r"}`)
	var req struct{ RequestID string }
	_ = json.Unmarshal([]byte(body), &req)
	// A person pauses it, then approves: it stays paused.
	if status, out := w.call("/internal/runs/"+runID+"/pause", map[string]any{}); status != 200 {
		t.Fatalf("pause: %d %v", status, out)
	}
	w.until("the pause", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'paused'`, runID) == 1
	})
	w.call("/internal/repository-requests/"+req.RequestID+"/decide", map[string]any{"approve": true})
	for range 10 {
		w.pump()
	}
	if w.lux.Runs()[0].Resumed != 0 {
		t.Fatalf("an approval resumed a person's pause")
	}
	// Their resume brings the repository with it.
	w.call("/internal/runs/"+runID+"/resume", map[string]any{})
	w.until("the repository to arrive with their resume", func() bool {
		return w.count(`SELECT count(*) FROM repository_requests WHERE id = $1 AND status = 'cloned'`, req.RequestID) == 1
	})
}

func TestADeclinedRepositoryRequestIsToldToTheAgent(t *testing.T) {
	w := newWorld(t)
	tools := httptest.NewServer((&agenttools.Server{DB: w.app, Log: quiet}).Handler())
	t.Cleanup(tools.Close)
	w.syncer.Agent.ToolsURL = tools.URL
	mustExec(t, w.owner, `INSERT INTO repositories (id, organization_id, project_id, name, url, default_branch)
		VALUES ($1, $2, $3, 'web', 'git://127.0.0.1/acme/web.git', 'main')`, "repo_web_"+w.org, w.org, w.project)
	w.lux.Decide = func(map[string]any) fakelux.Behaviour { return fakelux.Behaviour{Hang: true} }
	wi := w.task()
	w.names(wi, w.repoID)
	w.deliver(wi)
	w.until("the agent to be working", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND status = 'running'`, wi) == 1
	})
	_, body := w.callTool(tools.URL, string(w.lux.Runs()[0].Spec), "request_repository", `{"repository":"web","reason":"curious"}`)
	var req struct{ RequestID string }
	_ = json.Unmarshal([]byte(body), &req)
	w.call("/internal/repository-requests/"+req.RequestID+"/decide", map[string]any{"approve": false, "note": "Not needed for this."})
	// Sent to lux, which gives it to the agent when its turn allows.
	w.until("the decline to be sent to the agent", func() bool {
		return w.count(`SELECT count(*) FROM directives WHERE run_id = (SELECT id FROM runs WHERE task_id = $1)
			AND text LIKE '%declined. Not needed for this.%' AND sent_at IS NOT NULL`, wi) == 1
	})
	if r := w.lux.Runs()[0]; r.Resumed != 0 {
		t.Errorf("a declined request paused the run")
	}
	if n := w.count(`SELECT count(*) FROM task_repositories WHERE task_id = $1`, wi); n != 1 {
		t.Errorf("task's repositories: %d, want only its own", n)
	}
}

func TestWhatDudeSendsLux(t *testing.T) {
	w := newWorld(t)
	// A real model, so the spec is the one a real agent gets.
	mustExec(t, w.owner, `UPDATE projects SET agent_models = '{"implementer":{"model":"llm/impl"}}'::jsonb WHERE id = $1`, w.project)
	w.lux.Decide = func(map[string]any) fakelux.Behaviour { return fakelux.Behaviour{Hang: true} }
	wi := w.task()
	w.deliver(wi)
	w.until("the implementer to be submitted", func() bool { return len(w.lux.Runs()) >= 1 })

	var spec lux.Spec
	if err := json.Unmarshal(w.lux.Runs()[0].Spec, &spec); err != nil {
		t.Fatal(err)
	}
	if spec.Image.Ref != "agent:test" {
		t.Errorf("image = %s, want the project's", spec.Image.Ref)
	}
	if spec.Workload.Adapter != "opencode" || !strings.Contains(spec.Workload.Prompt, "Greet people") ||
		!strings.Contains(spec.Workload.Prompt, "it greets") {
		t.Errorf("workload = %+v", spec.Workload)
	}
	repo := spec.Git.Repositories[0]
	if repo.URL != "git://127.0.0.1/acme/target.git" || repo.Ref != "main" || repo.Credential != "GIT_TOKEN" {
		t.Errorf("repository = %+v", repo)
	}
	if spec.Git.Push == nil || !strings.HasPrefix(spec.Git.Push.Branch, "dude/"+wi+"/run-") {
		t.Errorf("push = %+v, want a branch of the Run's own", spec.Git.Push)
	}
	// The agent's session transcript must survive a move, or a resume
	// starts the conversation over.
	var home bool
	for _, v := range spec.Volumes {
		home = home || v.Path == "/home/agent" && v.Kind == "state"
	}
	if !home {
		t.Errorf("volumes = %+v, want the agent's home on a state volume", spec.Volumes)
	}
	secrets := map[string]lux.Secret{}
	for _, s := range spec.Secrets {
		secrets[s.Name] = s
	}
	if secrets["GIT_TOKEN"].Value != "ghp_test" {
		t.Errorf("the forge token was not passed as the git credential")
	}
	if s := secrets["DUDE_LLM_KEY"]; s.As != "env" || s.Value != "secret-key" {
		t.Errorf("DUDE_LLM_KEY = %+v, want the LLM key as an env secret", s)
	}
	if !strings.Contains(spec.Env["OPENCODE_CONFIG_CONTENT"], `"model":"llm/impl"`) || spec.Env["DUDE_LLM_URL"] != "https://llm.example/v1" {
		t.Errorf("env = %v, want the implementer's model inline and the LLM URL", spec.Env)
	}
	if spec.Network == nil || len(spec.Network.Egress) != 1 || spec.Network.Egress[0].Host != "llm.example" {
		t.Errorf("network = %+v, want egress to the model provider only", spec.Network)
	}
	// The tools it runs colour their output, which the chat renders.
	if spec.Env["FORCE_COLOR"] != "1" || spec.Env["TERM"] == "" || spec.Env["GIT_CONFIG_VALUE_0"] != "always" {
		t.Errorf("env = %v, want colour forced", spec.Env)
	}
	if spec.Labels["dude.run"] == "" || spec.Labels["dude.task"] != wi {
		t.Errorf("labels = %v", spec.Labels)
	}

	// The key is the dude Run id: a resubmission returns the same lux Run.
	var runID string
	_ = w.owner.QueryRow(context.Background(), `SELECT id FROM runs WHERE task_id = $1 AND phase = 'implement'`, wi).Scan(&runID)
	if runID != spec.Labels["dude.run"] {
		t.Errorf("label run = %s, row = %s", spec.Labels["dude.run"], runID)
	}
}

func TestTheAgentsWorkReachesTheLedgerAsAConversation(t *testing.T) {
	w := newWorld(t)
	wi := w.task()
	w.deliver(wi)
	w.until("the implementer to finish", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'implement' AND status = 'completed'`, wi) == 1
	})
	var runID string
	_ = w.owner.QueryRow(context.Background(), `SELECT id FROM runs WHERE task_id = $1 AND phase = 'implement'`, wi).Scan(&runID)

	// One message, whole: the chunks it streamed in are joined.
	var text string
	_ = w.owner.QueryRow(context.Background(), `SELECT string_agg(payload->>'text', '|') FROM events
		WHERE run_id = $1 AND event_type = 'agent.message'`, runID).Scan(&text)
	if text != "Implemented it." {
		t.Errorf("messages = %q", text)
	}
	for typ, want := range map[string]int{
		"agent.session.started": 1, "agent.prompt.delivered": 1,
		// The context size as it changed, and the turn's token totals.
		"agent.model.request.completed": 2, "run.started": 1, "run.completed": 1, "git.commit_created": 1,
	} {
		if n := w.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = $2`, runID, typ); n != want {
			t.Errorf("%s: %d events, want %d", typ, n, want)
		}
	}
	// A plan is a plan, not a tool call — including its completion, which
	// OpenCode sends without a title to recognise it by.
	if n := w.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = 'agent.plan.updated'`, runID); n != 1 {
		t.Errorf("plan updates = %d, want 1", n)
	}
	if n := w.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = 'agent.tool.completed'`, runID); n != 1 {
		t.Errorf("tool completions = %d, want 1 (the read, not the plan)", n)
	}
	// A tool call's completion keeps the name it started with.
	if n := w.count(`SELECT count(*) FROM events c JOIN events d ON d.payload->>'callId' = c.payload->>'callId'
		WHERE c.run_id = $1 AND c.event_type = 'agent.tool.called' AND d.event_type = 'agent.tool.completed'
		AND d.payload->>'tool' = c.payload->>'tool'`, runID); n == 0 {
		t.Errorf("no tool completion kept its call's name")
	}
	var changed []string
	_ = w.owner.QueryRow(context.Background(), `SELECT ARRAY(SELECT jsonb_array_elements_text(heads->'target'->'changedPaths'))
		FROM runs WHERE id = $1`, runID).Scan(&changed)
	if len(changed) != 1 || changed[0] != "FACTORY.md" {
		t.Errorf("changed paths in target = %v", changed)
	}
	// A finished phase's lux Run is stopped, not cancelled: its workspace and
	// session are kept.
	if r := w.lux.Runs()[0]; r.Stopped != 1 || r.Cancelled {
		t.Errorf("lux run stopped=%d cancelled=%v", r.Stopped, r.Cancelled)
	}
}

func TestAPullRequestFixIsHandedTheFeedbackAndNotTheFindingsLeftOpen(t *testing.T) {
	w := newWorld(t)
	// A reviewer that only has a minor point: the PR opens with it open.
	minor := strings.Replace(fakeagent.Finding, "severity: blocking", "severity: low", 1)
	w.lux.Decide = func(spec map[string]any) fakelux.Behaviour {
		labels, _ := spec["labels"].(map[string]any)
		switch labels["dude.phase"] {
		case "review":
			return fakelux.Behaviour{Reply: "```yaml\n" + minor + "```\n"}
		case "implement", "fix":
			return fakelux.Behaviour{Commit: map[string]string{labels["dude.phase"].(string) + ".md": "x\n"}, Message: "work"}
		}
		return fakelux.Behaviour{Reply: "Nothing to simplify."}
	}
	wi := w.task()
	w.deliver(wi)
	w.until("a pull request", func() bool { return len(w.gh.Pulls()) == 1 })
	// The fixer is a real model, so its spec carries the prompt a real agent gets.
	mustExec(t, w.owner, `UPDATE projects SET agent_models = agent_models || '{"implementer":{"model":"llm/impl"}}'::jsonb
		WHERE id = $1`, w.project)

	w.gh.Comment(1, "reviewer-person", "Please rename the greeting.")
	w.until("a fix for the comment", func() bool {
		_, _ = w.prs.Reconcile(context.Background(), 0)
		return w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'fix'`, wi) == 1
	})
	var prompt string
	w.until("the fix to reach lux", func() bool {
		for _, r := range w.lux.Runs() {
			var spec lux.Spec
			_ = json.Unmarshal(r.Spec, &spec)
			if spec.Labels["dude.phase"] == "fix" {
				prompt = spec.Workload.Prompt
				return true
			}
		}
		return false
	})
	if !strings.Contains(prompt, "Please rename the greeting.") {
		t.Errorf("the fix was not handed the comment:\n%s", prompt)
	}
	if strings.Contains(prompt, "does not record the fix") {
		t.Errorf("the fix was handed a review finding nobody asked it to fix:\n%s", prompt)
	}
}

func TestTheChatShowsThinkingToolOutputTokensAndThePrompt(t *testing.T) {
	w := newWorld(t)
	// Binary output with a NUL, which jsonb refuses, in what is kept.
	long := "\x00" + strings.Repeat("a", 2999) + strings.Repeat("z", 3000)
	w.lux.Decide = func(spec map[string]any) fakelux.Behaviour {
		if labels, _ := spec["labels"].(map[string]any); labels["dude.phase"] != "implement" {
			return fakelux.Behaviour{Hang: true}
		}
		return fakelux.Behaviour{Thought: "I should read the tests first.", Tools: []string{"bash"},
			ToolOutput: map[string]string{"bash": long}, Reply: "Done.",
			Commit: map[string]string{"a.md": "x\n"}, Message: "work"}
	}
	wi := w.task()
	w.deliver(wi)
	w.until("the implementer to finish", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'implement' AND status = 'completed'`, wi) == 1
	})
	ctx := context.Background()
	var runID, harness, model string
	var ctxTokens, outTokens int64
	_ = w.owner.QueryRow(ctx, `SELECT id, harness, model, context_tokens, output_tokens FROM runs
		WHERE task_id = $1 AND phase = 'implement'`, wi).Scan(&runID, &harness, &model, &ctxTokens, &outTokens)
	if harness != "scripted" || model != "fake/scripted" {
		t.Errorf("harness=%q model=%q", harness, model)
	}
	if ctxTokens != 1000 || outTokens != 34 {
		t.Errorf("context=%d output=%d, want the agent's report", ctxTokens, outTokens)
	}
	payload := func(typ string) map[string]any {
		var raw []byte
		_ = w.owner.QueryRow(ctx, `SELECT payload FROM events WHERE run_id = $1 AND event_type = $2
			ORDER BY cursor LIMIT 1`, runID, typ).Scan(&raw)
		var m map[string]any
		_ = json.Unmarshal(raw, &m)
		return m
	}
	// The prompt as the agent received it: the scripted agent's is its script.
	if p := payload("agent.prompt.delivered"); !strings.Contains(fmt.Sprint(p["text"]), "commit") {
		t.Errorf("prompt = %v", p)
	}
	if p := payload("agent.thought"); p["text"] != "I should read the tests first." {
		t.Errorf("thought = %v", p)
	}
	// Output over 4 KB keeps its first and last 2 KB.
	done := payload("agent.tool.completed")
	out, _ := done["output"].(map[string]any)
	if done["exitCode"] != float64(3) || len(fmt.Sprint(out["head"])) != 2048 || len(fmt.Sprint(out["tail"])) != 2048 ||
		out["omittedBytes"] != float64(6000+2-4096) || !strings.HasPrefix(fmt.Sprint(out["head"]), "\ufffd") || !strings.HasSuffix(fmt.Sprint(out["tail"]), "z") {
		t.Errorf("tool result = exit %v, head %d, tail %d, omitted %v", done["exitCode"],
			len(fmt.Sprint(out["head"])), len(fmt.Sprint(out["tail"])), out["omittedBytes"])
	}
	// Each message carries the context size at that point.
	if p := payload("agent.message"); p["contextTokens"] != float64(1000) {
		t.Errorf("message = %v", p)
	}
}

// withTools serves dude's tools to the world's agents, as the orchestrator
// does, reached through lux's service in their containers.
func (w *world) withTools() {
	tools := httptest.NewServer((&agenttools.Server{DB: w.app, Log: quiet}).Handler())
	w.t.Cleanup(tools.Close)
	w.syncer.Agent.ToolsURL = tools.URL
	w.syncer.Agent.ToolsService = true
}

func TestAnAgentThatAsksWaitsForTheAnswerAndCarriesOn(t *testing.T) {
	w := newWorld(t)
	w.withTools()
	question := `{"question":"Should the table be sorted?","choices":["yes","no"]}`
	w.lux.Decide = func(spec map[string]any) fakelux.Behaviour {
		if labels, _ := spec["labels"].(map[string]any); labels["dude.phase"] != "implement" {
			return fakelux.Behaviour{Hang: true}
		}
		return fakelux.Behaviour{Ask: question, Reply: "Sorted it, as asked.",
			Commit: map[string]string{"a.md": "x\n"}, Message: "work"}
	}
	wi := w.task()
	w.deliver(wi)
	w.until("the question to reach a person", func() bool { return w.taskStatus(wi) == "awaiting_input" })

	var qid, prompt string
	var options []string
	_ = w.owner.QueryRow(context.Background(), `SELECT id, prompt, ARRAY(SELECT jsonb_array_elements_text(options))
		FROM questions WHERE task_id = $1`, wi).Scan(&qid, &prompt, &options)
	if prompt != "Should the table be sorted?" || len(options) != 2 {
		t.Fatalf("question = %q %v", prompt, options)
	}
	// Waiting is not finishing: the phase must not complete, or be pushed,
	// with its task unanswered. (The question is asked mid-turn; the turn
	// ends after.)
	w.until("the agent to wait", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND waiting_since IS NOT NULL`, wi) == 1
	})
	if n := w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND status = 'running' AND turn_done_at IS NULL`, wi); n != 1 {
		t.Fatalf("the asking run is not still running")
	}
	if len(w.lux.Runs()) != 1 || w.lux.Runs()[0].Pushed {
		t.Fatalf("the run was pushed before its question was answered")
	}

	if status, body := w.call("/internal/questions/"+qid+"/answer", map[string]any{"text": "yes"}); status != 200 {
		t.Fatalf("answer: %d %v", status, body)
	}
	w.until("the implementer to finish", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'implement' AND status = 'completed'`, wi) == 1
	})
	if in := w.lux.Runs()[0].Inputs; len(in) != 1 || !strings.Contains(in[0], "Should the table be sorted?") || !strings.HasSuffix(in[0], "yes") {
		t.Errorf("the agent was given %v, want the answer quoting its question", in)
	}
	if status, _ := w.call("/internal/questions/"+qid+"/answer", map[string]any{"text": "no"}); status != 409 {
		t.Errorf("a second answer was accepted: %d", status)
	}
	for _, typ := range []string{"question.asked", "question.answered"} {
		if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = $2`, wi, typ); n != 1 {
			t.Errorf("%s: %d events", typ, n)
		}
	}
}

// callAs posts to the orchestrator's internal API as the backend would for
// one person (X-Dude-Actor, their key).
func (w *world) callAs(actor, path string, body any) (int, map[string]any) {
	w.t.Helper()
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest("POST", w.api+path, bytes.NewReader(b))
	req.Header.Set("Authorization", "Bearer svc")
	req.Header.Set("X-Dude-Organization", w.org)
	req.Header.Set("X-Dude-Actor", actor)
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		w.t.Fatal(err)
	}
	defer res.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(res.Body).Decode(&out)
	return res.StatusCode, out
}

// person makes a user key in the world's organization, named, and
// returns its id.
func (w *world) person(name string) string {
	id := "key_" + strings.ToLower(name) + "_" + w.org
	mustExec(w.t, w.owner, `INSERT INTO api_keys (id, organization_id, name, key_hash, key_prefix)
		VALUES ($1, $2, $3, $1, 'dude_sk_')`, id, w.org, name)
	return id
}

func (w *world) assignOwner(task, key string) {
	w.t.Helper()
	mustExec(w.t, w.owner, `DELETE FROM task_people WHERE task_id = $1`, task)
	mustExec(w.t, w.owner, `INSERT INTO task_people (task_id, person_id, organization_id, position)
		SELECT $1, person_id, organization_id, 0 FROM api_keys WHERE id = $2`, task, key)
}

// Only a task's owner answers its agents and decides what they may
// reach; anyone else is told who can. A task nobody owns is anyone's.
func TestOnlyATasksOwnerAnswersAndDecides(t *testing.T) {
	w := newWorld(t)
	ana, bo := w.person("Ana"), w.person("Bo")
	mustExec(t, w.owner, `INSERT INTO repositories (id, organization_id, project_id, name, url, default_branch)
		VALUES ($1, $2, $3, 'web', 'git://127.0.0.1/acme/web.git', 'main')`, "repo_web_"+w.org, w.org, w.project)
	wi, _ := w.asking()
	w.assignOwner(wi, ana)
	qid := w.questionID(wi)

	status, body := w.callAs(bo, "/internal/questions/"+qid+"/answer", map[string]any{"text": "yes"})
	errBody, _ := body["error"].(map[string]any)
	if status != 403 || errBody["code"] != "not_owner" ||
		errBody["message"] != "only Ana can answer this — reassign the task to answer it" {
		t.Fatalf("a non-owner's answer: %d %v", status, body)
	}
	if n := w.count(`SELECT count(*) FROM questions WHERE id = $1 AND status = 'open'`, qid); n != 1 {
		t.Fatalf("the refused answer changed the question")
	}

	_, reqBody := w.callTool(w.syncer.Agent.ToolsURL, string(w.lux.Runs()[0].Spec), "request_repository",
		`{"repository":"web","reason":"the client"}`)
	var req struct{ RequestID string }
	_ = json.Unmarshal([]byte(reqBody), &req)
	if status, body := w.callAs(bo, "/internal/repository-requests/"+req.RequestID+"/decide", map[string]any{"approve": true}); status != 403 {
		t.Fatalf("a non-owner's approval: %d %v", status, body)
	}
	if n := w.count(`SELECT count(*) FROM repository_requests WHERE id = $1 AND status = 'pending'`, req.RequestID); n != 1 {
		t.Fatalf("the refused approval changed the request")
	}

	// Reassigned, the new owner decides.
	w.assignOwner(wi, bo)
	if status, body := w.callAs(bo, "/internal/repository-requests/"+req.RequestID+"/decide", map[string]any{"approve": false}); status != 200 {
		t.Fatalf("the owner's decision: %d %v", status, body)
	}
	// Revoking a credential does not remove the person or their ownership.
	mustExec(t, w.owner, `UPDATE api_keys SET revoked_at = now() WHERE id = $1`, bo)
	if status, body := w.callAs(ana, "/internal/questions/"+qid+"/answer", map[string]any{"text": "yes"}); status != 403 {
		t.Fatalf("key revocation lost person ownership: %d %v", status, body)
	}
	mustExec(t, w.owner, `UPDATE people SET removed_at = now() WHERE id = (SELECT person_id FROM api_keys WHERE id = $1)`, bo)
	if status, body := w.callAs(ana, "/internal/questions/"+qid+"/answer", map[string]any{"text": "yes"}); status != 200 {
		t.Fatalf("an answer on a task with no active owner: %d %v", status, body)
	}
}

// A person answers with any of their keys: the owner is who holds the
// key the task names, not the key.
func TestAnOwnerAnswersWithAnyOfTheirKeys(t *testing.T) {
	w := newWorld(t)
	ana := w.person("Ana")
	laptop := "key_laptop_" + w.org
	mustExec(t, w.owner, `INSERT INTO api_keys (id, organization_id, name, key_hash, key_prefix, person_id)
		SELECT $1, $2, 'laptop', $1, 'dude_sk_', person_id FROM api_keys WHERE id = $3`, laptop, w.org, ana)
	wi, _ := w.asking()
	w.assignOwner(wi, ana)
	if status, body := w.callAs(laptop, "/internal/questions/"+w.questionID(wi)+"/answer", map[string]any{"text": "yes"}); status != 200 {
		t.Fatalf("the owner's other key: %d %v", status, body)
	}
}

// questionID is the task's (one) question.
func (w *world) questionID(wi string) string {
	var id string
	_ = w.owner.QueryRow(context.Background(), `SELECT id FROM questions WHERE task_id = $1`, wi).Scan(&id)
	return id
}

// asking starts a delivery whose implementer stops on a question.
func (w *world) asking() (wi, runID string) {
	w.withTools()
	w.lux.Decide = func(spec map[string]any) fakelux.Behaviour {
		if labels, _ := spec["labels"].(map[string]any); labels["dude.phase"] != "implement" {
			return fakelux.Behaviour{Hang: true}
		}
		return fakelux.Behaviour{Ask: fakeagent.Question, Reply: "Done.", Commit: map[string]string{"a.md": "x\n"}, Message: "work"}
	}
	wi = w.task()
	w.deliver(wi)
	w.until("the question to reach a person", func() bool { return w.taskStatus(wi) == "awaiting_input" })
	_ = w.owner.QueryRow(context.Background(), `SELECT id FROM runs WHERE task_id = $1`, wi).Scan(&runID)
	return wi, runID
}

func TestAbortingARunThatAskedCancelsItsQuestion(t *testing.T) {
	w := newWorld(t)
	wi, runID := w.asking()
	if status, body := w.call("/internal/runs/"+runID+"/abort", map[string]any{}); status != 200 {
		t.Fatalf("abort: %d %v", status, body)
	}
	if n := w.count(`SELECT count(*) FROM questions WHERE task_id = $1 AND status = 'open'`, wi); n != 0 {
		t.Errorf("an aborted Run's question is still waiting for a person")
	}
	// The ledger says so, which is where the task's time ends.
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'task.status_changed'
		AND payload->>'status' = 'aborted'`, wi); n != 1 {
		t.Errorf("%d aborted status events, want one", n)
	}
	// Answered afterwards — the next morning — it says why nothing happens.
	qid := w.questionID(wi)
	if status, body := w.call("/internal/questions/"+qid+"/answer", map[string]any{"text": "yes"}); status != 409 ||
		!strings.Contains(fmt.Sprint(body), "no longer relevant") {
		t.Errorf("answering a dead question: %d %v", status, body)
	}
}

// An agent waiting on a person holds nothing while it waits: after the
// grace period its container is stopped, conversation kept, and the answer
// — however late — resumes it where it was.
func TestAnAgentWaitingOnAPersonIsParkedAndTheAnswerResumesIt(t *testing.T) {
	w := newWorld(t)
	w.syncer.ParkAfter = 300 * time.Millisecond
	wi, runID := w.asking()
	w.until("the run to be parked", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'paused' AND dude_pause = 'person'`, runID) == 1
	})
	w.until("lux to stop it: parked holds no capacity", func() bool { return w.lux.Runs()[0].State == "stopped" })
	if w.taskStatus(wi) != "awaiting_input" {
		t.Errorf("task is %s while its question is open", w.taskStatus(wi))
	}
	if n := w.count(`SELECT count(*) FROM questions WHERE task_id = $1 AND status = 'open'`, wi); n != 1 {
		t.Fatalf("parking closed the question")
	}
	if n := w.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = 'run.parked'`, runID); n != 1 {
		t.Errorf("%d run.parked events", n)
	}
	// Parked stays parked: nothing resumes it until the answer.
	for range 5 {
		w.pump()
	}
	if w.lux.Runs()[0].Resumed != 0 {
		t.Fatalf("resumed with the question unanswered")
	}

	qid := w.questionID(wi)
	if status, body := w.call("/internal/questions/"+qid+"/answer", map[string]any{"text": "yes"}); status != 200 {
		t.Fatalf("answer: %d %v", status, body)
	}
	w.until("the implementer to finish", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'completed'`, runID) == 1
	})
	r := w.lux.Runs()[0]
	if r.Resumed != 1 {
		t.Fatalf("resumed %d times, want once, the same lux Run", r.Resumed)
	}
	// The answer is what it hears, not a "carry on" as well.
	if len(r.Inputs) != 1 || !strings.HasSuffix(r.Inputs[0], "yes") {
		t.Errorf("the agent was given %q", r.Inputs)
	}
	if n := w.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = 'run.unparked'`, runID); n != 1 {
		t.Errorf("%d run.unparked events", n)
	}
}

// Someone at their desk answers within the grace period: the agent is never
// stopped.
func TestAnAnswerWithinTheGracePeriodIsTakenLive(t *testing.T) {
	w := newWorld(t)
	w.syncer.ParkAfter = time.Hour
	wi, runID := w.asking()
	qid := w.questionID(wi)
	w.call("/internal/questions/"+qid+"/answer", map[string]any{"text": "yes"})
	w.until("the implementer to finish", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'completed'`, runID) == 1
	})
	if w.lux.Runs()[0].Resumed != 0 {
		t.Errorf("stopped and resumed a run whose question was answered at once")
	}
}

// A person who pauses a parked Run makes it theirs: the answer does not
// resume it.
func TestAPersonsPauseOfAParkedRunHolds(t *testing.T) {
	w := newWorld(t)
	w.syncer.ParkAfter = 300 * time.Millisecond
	wi, runID := w.asking()
	w.until("the run to be parked", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND dude_pause = 'person'`, runID) == 1
	})
	if status, body := w.call("/internal/runs/"+runID+"/pause", map[string]any{}); status != 200 {
		t.Fatalf("pause: %d %v", status, body)
	}
	qid := w.questionID(wi)
	w.call("/internal/questions/"+qid+"/answer", map[string]any{"text": "yes"})
	for range 10 {
		w.pump()
	}
	if w.lux.Runs()[0].Resumed != 0 {
		t.Fatalf("the answer resumed a run a person paused")
	}
	w.call("/internal/runs/"+runID+"/resume", map[string]any{})
	w.until("the implementer to finish", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'completed'`, runID) == 1
	})
}

// An agent that needs a repository to go on ends its turn on the request:
// parked like a question, and the approval resumes it with the repository.
func TestAnAgentWaitingOnARepositoryIsParkedAndTheApprovalResumesIt(t *testing.T) {
	w := newWorld(t)
	w.withTools()
	w.syncer.ParkAfter = 300 * time.Millisecond
	mustExec(t, w.owner, `INSERT INTO repositories (id, organization_id, project_id, name, url, default_branch)
		VALUES ($1, $2, $3, 'web', 'git://127.0.0.1/acme/web.git', 'main')`, "repo_web_"+w.org, w.org, w.project)
	w.lux.Decide = func(spec map[string]any) fakelux.Behaviour {
		if labels, _ := spec["labels"].(map[string]any); labels["dude.phase"] != "implement" {
			return fakelux.Behaviour{Hang: true}
		}
		return fakelux.Behaviour{CallTools: [][2]string{{"request_repository", `{"repository":"web","reason":"the client","wait":true}`}},
			Reply: "Done.", Commit: map[string]string{"a.md": "x\n"}, Message: "work"}
	}
	wi := w.task()
	w.names(wi, w.repoID)
	w.deliver(wi)
	var runID string
	w.until("the run to be parked", func() bool {
		_ = w.owner.QueryRow(context.Background(), `SELECT id FROM runs WHERE task_id = $1 AND dude_pause = 'person'`, wi).Scan(&runID)
		return runID != ""
	})
	var reqID string
	_ = w.owner.QueryRow(context.Background(), `SELECT id FROM repository_requests WHERE run_id = $1`, runID).Scan(&reqID)
	if status, body := w.call("/internal/repository-requests/"+reqID+"/decide", map[string]any{"approve": true}); status != 200 {
		t.Fatalf("approve: %d %v", status, body)
	}
	w.until("the clone and the implementer finishing", func() bool {
		return w.count(`SELECT count(*) FROM repository_requests WHERE id = $1 AND status = 'cloned'`, reqID) == 1 &&
			w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'completed'`, runID) == 1
	})
	if r := w.lux.Runs()[0]; r.Resumed != 1 || len(r.Inputs) != 1 || !strings.Contains(r.Inputs[0], "web is now checked out") {
		t.Errorf("resumed %d times, told %q", r.Resumed, r.Inputs)
	}
}

// A request the agent does not wait on does not hold its turn: it ends,
// and the phase finishes, with the request left for a person.
func TestARequestTheAgentDoesNotWaitOnLetsItsTurnEnd(t *testing.T) {
	w := newWorld(t)
	w.withTools()
	w.syncer.ParkAfter = 100 * time.Millisecond
	mustExec(t, w.owner, `INSERT INTO repositories (id, organization_id, project_id, name, url, default_branch)
		VALUES ($1, $2, $3, 'web', 'git://127.0.0.1/acme/web.git', 'main')`, "repo_web_"+w.org, w.org, w.project)
	w.lux.Decide = func(spec map[string]any) fakelux.Behaviour {
		if labels, _ := spec["labels"].(map[string]any); labels["dude.phase"] != "implement" {
			return fakelux.Behaviour{Hang: true}
		}
		return fakelux.Behaviour{CallTools: [][2]string{{"request_repository", `{"repository":"web","reason":"curious"}`}},
			Reply: "Done.", Commit: map[string]string{"a.md": "x\n"}, Message: "work"}
	}
	wi := w.task()
	w.names(wi, w.repoID)
	w.deliver(wi)
	w.until("the implementer to finish", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'implement' AND status = 'completed'`, wi) == 1
	})
	if n := w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND dude_pause IS NOT NULL`, wi); n != 0 {
		t.Errorf("parked on a request the agent did not wait on")
	}
}

// Work that starts on no repository, and is let change one mid-Run, pushes
// it: the branch lux pushes to is named at submit, before there is anything
// to push.
func TestWorkGivenARepositoryToChangeMidRunIsPushed(t *testing.T) {
	w := newWorld(t)
	w.withTools()
	w.syncer.ParkAfter = 100 * time.Millisecond
	mustExec(t, w.owner, `INSERT INTO repositories (id, organization_id, project_id, name, url, default_branch)
		VALUES ($1, $2, $3, 'web', 'git://127.0.0.1/acme/web.git', 'main')`, "repo_web_"+w.org, w.org, w.project)
	w.lux.Decide = func(spec map[string]any) fakelux.Behaviour {
		if labels, _ := spec["labels"].(map[string]any); labels["dude.phase"] != "implement" {
			return fakelux.Behaviour{Hang: true}
		}
		return fakelux.Behaviour{CallTools: [][2]string{{"request_repository", `{"repository":"web","write":true,"reason":"the change is there","wait":true}`}},
			Reply: "Done.", Commit: map[string]string{"a.md": "x\n"}, Message: "work"}
	}
	wi := w.task() // names no repository: the project has two
	w.deliver(wi)
	var reqID string
	w.until("the request", func() bool {
		_ = w.owner.QueryRow(context.Background(), `SELECT id FROM repository_requests WHERE task_id = $1`, wi).Scan(&reqID)
		return reqID != ""
	})
	w.call("/internal/repository-requests/"+reqID+"/decide", map[string]any{"approve": true})
	w.until("the implementer to finish", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'implement' AND status = 'completed'`, wi) == 1
	})
	if !w.lux.Runs()[0].Pushed {
		t.Fatalf("the change was never pushed\n%s", w.describeRuns())
	}
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND payload->>'reason' = 'no_changes'`, wi); n != 0 {
		t.Errorf("escalated as no changes")
	}
}

// Work that holds nothing it may push is not pushed: a container that is
// gone by the time the turn is read still completes with what it published.
func TestAPublishingRunWithNothingToPushFinishesAfterItsContainerIsGone(t *testing.T) {
	w := newWorld(t)
	mustExec(t, w.owner, `INSERT INTO repositories (id, organization_id, project_id, name, url, default_branch)
		VALUES ($1, $2, $3, 'web', 'git://127.0.0.1/acme/web.git', 'main')`, "repo_web_"+w.org, w.org, w.project)
	w.lux.Decide = func(spec map[string]any) fakelux.Behaviour {
		if labels, _ := spec["labels"].(map[string]any); labels["dude.phase"] != "implement" {
			return fakelux.Behaviour{Hang: true}
		}
		return fakelux.Behaviour{Reply: "Wrote the notes.", Publish: map[string]string{"NOTES.md": "notes\n"}, ExitAfterTurn: true}
	}
	wi := w.task() // names no repository: the project has two
	w.deliver(wi)
	w.until("the implementer to complete", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'implement' AND status = 'completed'`, wi) == 1
	})
}

// A person approving a repository the lux Run already has checked out: no
// clone will come, so the approval settles at once, and the agent waiting
// on it is told and carries on.
func TestAnApprovalForARepositoryTheRunAlreadyHasSettlesAtOnce(t *testing.T) {
	w := newWorld(t)
	w.withTools()
	w.lux.Decide = func(map[string]any) fakelux.Behaviour { return fakelux.Behaviour{Hang: true} }
	wi := w.task()
	w.names(wi, w.repoID)
	w.deliver(wi)
	w.until("the agent to be working", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND status = 'running'`, wi) == 1
	})
	var runID string
	_ = w.owner.QueryRow(context.Background(), `SELECT id FROM runs WHERE task_id = $1`, wi).Scan(&runID)
	// A person takes target off the task; the lux Run still has it.
	mustExec(t, w.owner, `DELETE FROM task_repositories WHERE task_id = $1`, wi)
	_, body := w.callTool(w.syncer.Agent.ToolsURL, string(w.lux.Runs()[0].Spec), "request_repository",
		`{"repository":"target","reason":"I still need it","wait":true}`)
	var req struct{ RequestID string }
	_ = json.Unmarshal([]byte(body), &req)
	if status, out := w.call("/internal/repository-requests/"+req.RequestID+"/decide", map[string]any{"approve": true}); status != 200 {
		t.Fatalf("approve: %d %v (%s)", status, out, body)
	}
	if n := w.count(`SELECT count(*) FROM repository_requests WHERE id = $1 AND status = 'cloned'`, req.RequestID); n != 1 {
		t.Fatalf("the approval did not settle")
	}
	w.until("the agent to be told", func() bool {
		return w.count(`SELECT count(*) FROM directives WHERE run_id = $1 AND text LIKE '%already checked out%' AND sent_at IS NOT NULL`, runID) == 1
	})
	if w.lux.Runs()[0].Resumed != 0 {
		t.Errorf("paused and resumed for a repository it had")
	}
}

// unreadableForge is a forge whose credentials cannot be read right now.
type unreadableForge struct{}

func (unreadableForge) For(context.Context, string) (*forge.GitHub, error) {
	return nil, errors.New("connection reset")
}

// A forge whose credentials cannot be read for a moment delays a start; it
// does not make one without them.
func TestAForgeThatCannotBeReadDelaysTheRunRatherThanStartingItWithoutCredentials(t *testing.T) {
	w := newWorld(t)
	w.lux.Decide = func(map[string]any) fakelux.Behaviour { return fakelux.Behaviour{Hang: true} }
	w.syncer.Forges = unreadableForge{}
	wi := w.task()
	w.deliver(wi)
	for range 5 {
		w.pump()
	}
	if n := len(w.lux.Runs()); n != 0 {
		t.Fatalf("started %d lux Runs without the forge's credentials", n)
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND status = 'failed'`, wi); n != 0 {
		t.Fatalf("failed the Run over a passing forge error")
	}
}

// Approved with its checks green, a task is ready to merge — a
// person's call to make, the factory never merges — and back in review
// if a check turns red.
func TestAnApprovedPullRequestWithGreenChecksIsReadyToMerge(t *testing.T) {
	w := newWorld(t)
	wi := w.task()
	w.deliver(wi)
	w.until("a pull request", func() bool { return len(w.gh.Pulls()) == 1 })
	w.until("review", func() bool { return w.taskStatus(wi) == "review" })

	w.gh.Review(1, "alice", "APPROVED")
	w.until("ready to merge", func() bool {
		_, _ = w.prs.Reconcile(context.Background(), 0)
		return w.taskStatus(wi) == "ready_to_merge"
	})
	if w.gh.Pull(1).State != "open" {
		t.Fatalf("the factory merged it")
	}
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'task.ready_to_merge'`, wi); n != 1 {
		t.Errorf("%d ready-to-merge events, want one to tell people by", n)
	}

	// An approval dismissed is no approval.
	w.gh.Review(1, "alice", "DISMISSED")
	w.until("back in review", func() bool {
		_, _ = w.prs.Reconcile(context.Background(), 0)
		return w.taskStatus(wi) == "review"
	})
	w.gh.Review(1, "alice", "APPROVED")
	w.until("ready again", func() bool {
		_, _ = w.prs.Reconcile(context.Background(), 0)
		return w.taskStatus(wi) == "ready_to_merge"
	})

	// A check turning red is back to work: review, and a fixer on it.
	w.gh.SetChecks("failure")
	w.until("no longer ready", func() bool {
		_, _ = w.prs.Reconcile(context.Background(), 0)
		return w.taskStatus(wi) != "ready_to_merge"
	})

	// Merged by a person while the fixer is still at work. The fix lands
	// after the merge, so it is outside it: it gets a pull request of its
	// own, and the task is done when that one is merged too.
	w.gh.SetChecks("success")
	w.gh.Merge(1)
	w.until("a pull request for the fix", func() bool {
		_, _ = w.prs.Reconcile(context.Background(), 0)
		return len(w.gh.Pulls()) == 2
	})
	w.gh.Merge(2)
	w.until("the task to finish", func() bool {
		_, _ = w.prs.Reconcile(context.Background(), 0)
		return w.taskStatus(wi) == "done"
	})
}

// An agent quiet mid-turn is nudged once; still quiet, it is parked for a
// person, who resumes it.
func TestAQuietAgentIsNudgedThenParkedForAPerson(t *testing.T) {
	w := newWorld(t)
	w.syncer.IdleAfter = 300 * time.Millisecond
	// Silent even after the nudge: its turn is taken and it says nothing.
	w.lux.Decide = func(map[string]any) fakelux.Behaviour { return fakelux.Behaviour{Hang: true} }
	wi := w.task()
	w.deliver(wi)
	var runID string
	w.until("a nudge", func() bool {
		_ = w.owner.QueryRow(context.Background(), `SELECT id FROM runs WHERE task_id = $1 AND idle_nudged_at IS NOT NULL`, wi).Scan(&runID)
		return runID != ""
	})
	w.until("the nudge to reach the agent, interrupting its turn", func() bool {
		r := w.lux.Runs()[0]
		return r.Interrupted == 1 && len(r.Inputs) == 1 && strings.Contains(r.Inputs[0], "ask_person")
	})
	w.until("the run to be parked as idle", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'paused' AND dude_pause = 'idle'`, runID) == 1
	})
	if w.taskStatus(wi) != "awaiting_input" {
		t.Errorf("task is %s, want awaiting_input for a person to look", w.taskStatus(wi))
	}
	if n := w.count(`SELECT count(*) FROM directives WHERE run_id = $1`, runID); n != 1 {
		t.Errorf("%d nudges, want one", n)
	}
	for range 5 {
		w.pump()
	}
	if w.lux.Runs()[0].Resumed != 0 {
		t.Fatalf("an idle-parked run resumed on its own")
	}
	w.call("/internal/runs/"+runID+"/resume", map[string]any{})
	w.until("the resume", func() bool { return w.lux.Runs()[0].Resumed == 1 })
	if w.taskStatus(wi) != "running" {
		t.Errorf("task is %s after a person resumed it", w.taskStatus(wi))
	}
}

// An agent running a long command, or waiting on a person, is not quiet.
func TestAnAgentInALongCommandIsNotNudged(t *testing.T) {
	w := newWorld(t)
	w.syncer.IdleAfter = 200 * time.Millisecond
	w.lux.Decide = func(map[string]any) fakelux.Behaviour {
		return fakelux.Behaviour{Hang: true, Tools: []string{"sleep"}, KeepToolsOpen: true}
	}
	wi := w.task()
	w.deliver(wi)
	w.until("the agent to be working", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND cardinality(open_tool_calls) = 1`, wi) == 1
	})
	time.Sleep(500 * time.Millisecond)
	for range 5 {
		w.pump()
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND idle_nudged_at IS NOT NULL`, wi); n != 0 {
		t.Errorf("nudged an agent in the middle of a command")
	}
}

func TestAnAgentThatDiesWhileWaitingFailsItsRun(t *testing.T) {
	w := newWorld(t)
	wi, _ := w.asking()
	w.lux.Crash(w.lux.Runs()[0].ID)
	w.until("the run to fail", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND status = 'failed'`, wi) == 1
	})
	if n := w.count(`SELECT count(*) FROM questions WHERE task_id = $1 AND status = 'open'`, wi); n != 0 {
		t.Errorf("a failed Run's question is still waiting for a person")
	}
}

func TestAFindingIsResolvedOnlyWhenTheReviewerJudgesItFixed(t *testing.T) {
	w := newWorld(t)
	var reviews int
	var shown []string
	w.lux.Decide = func(spec map[string]any) fakelux.Behaviour {
		labels, _ := spec["labels"].(map[string]any)
		switch labels["dude.phase"] {
		case "review":
			reviews++
			workload, _ := spec["workload"].(map[string]any)
			shown = append(shown, fmt.Sprint(workload["prompt"]))
			if reviews == 1 {
				return fakelux.Behaviour{Reply: "```yaml\n" + fakeagent.Finding + "```\n"}
			}
			// The fix touched the finding's file, but did not fix it.
			return fakelux.Behaviour{Reply: "```yaml\nverdicts:\n  F1: still\n```\n"}
		case "implement", "fix":
			return fakelux.Behaviour{Commit: map[string]string{"FACTORY.md": fmt.Sprint(labels["dude.run"]) + "\n"}, Message: "work"}
		}
		return fakelux.Behaviour{Hang: true}
	}
	// A real model, so the review prompt is the one a real reviewer reads.
	mustExec(t, w.owner, `UPDATE projects SET agent_models = agent_models || '{"reviewer":{"model":"llm/review"}}'::jsonb WHERE id = $1`, w.project)
	wi := w.task()
	w.deliver(wi)
	w.until("a second review", func() bool {
		return reviews >= 2 && w.count(`SELECT count(*) FROM runs
		WHERE task_id = $1 AND phase = 'review' AND status = 'completed'`, wi) >= 2
	})

	if n := w.count(`SELECT count(*) FROM review_findings WHERE task_id = $1 AND status = 'open'`, wi); n != 1 {
		t.Errorf("open findings = %d; a finding the reviewer says is still there must stay open", n)
	}
	if !strings.Contains(shown[1], "FACTORY.md does not record the fix") || !strings.Contains(shown[1], "F1: fixed | still") {
		t.Errorf("the re-review was not shown the finding to judge:\n%s", shown[1])
	}
}

func TestAReviewersFindingsAreRecorded(t *testing.T) {
	w := newWorld(t)
	wi := w.task()
	w.deliver(wi)
	w.until("a finding", func() bool {
		return w.count(`SELECT count(*) FROM review_findings WHERE task_id = $1`, wi) >= 1
	})
	var sev, title, file string
	var line int
	_ = w.owner.QueryRow(context.Background(), `SELECT severity::text, title, file, line FROM review_findings WHERE task_id = $1
		ORDER BY created_at LIMIT 1`, wi).Scan(&sev, &title, &file, &line)
	if sev != "blocking" || file != "FACTORY.md" || line != 1 || title != "FACTORY.md does not record the fix" {
		t.Errorf("finding = %s %s:%d %q", sev, file, line, title)
	}
}

func TestAReviewerIsToldWhatTheDeliverysPolicyBlocksOn(t *testing.T) {
	w := newWorld(t)
	mustExec(t, w.owner, `UPDATE projects SET agent_models = agent_models || '{"reviewer":{"model":"llm/review"}}'::jsonb
		WHERE id = $1`, w.project)
	w.lux.Decide = func(spec map[string]any) fakelux.Behaviour {
		if labels, _ := spec["labels"].(map[string]any); labels["dude.phase"] == "review" {
			return fakelux.Behaviour{Hang: true}
		}
		return fakelux.Behaviour{Commit: map[string]string{"a.md": "x\n"}, Message: "work"}
	}
	wi := w.task()
	policy := delivery.DefaultPolicy()
	policy.BlockingSeverities = []string{"blocking", "high", "medium"}
	mustExec(t, w.owner, `INSERT INTO task_repositories (organization_id, task_id, repository_id)
		VALUES ($1, $2, $3)`, w.org, wi, w.repoID)
	w.start(wi, policy)
	var prompt string
	w.until("a review to reach lux", func() bool {
		for _, r := range w.lux.Runs() {
			var spec lux.Spec
			_ = json.Unmarshal(r.Spec, &spec)
			if spec.Labels["dude.phase"] == "review" {
				prompt = spec.Workload.Prompt
				return true
			}
		}
		return false
	})
	if !strings.Contains(prompt, "`blocking`, `high`, `medium` send the change back") {
		t.Errorf("the reviewer was not told the policy's blocking severities:\n%s", prompt)
	}
}

func TestSteeringReachesTheAgentAndIsAcknowledged(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		t.Run(map[bool]string{false: "receipts", true: "legacy lux"}[legacy], func(t *testing.T) {
			steeringReachesTheAgent(t, legacy)
		})
	}
}

func steeringReachesTheAgent(t *testing.T, legacy bool) {
	w := newWorld(t)
	w.lux.LegacyInput = legacy
	w.lux.Decide = func(map[string]any) fakelux.Behaviour { return fakelux.Behaviour{Hang: true} }
	wi := w.task()
	w.deliver(wi)
	w.until("the agent to be working", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND status = 'running'`, wi) == 1
	})
	var runID string
	_ = w.owner.QueryRow(context.Background(), `SELECT id FROM runs WHERE task_id = $1`, wi).Scan(&runID)

	// An agent in the middle of a step with no tool running reaches no step
	// boundary until its turn ends: the harness took it (a legacy lux holds
	// it, and says nothing), the agent has not read it.
	mustExec(t, w.owner, `INSERT INTO directives (id, organization_id, task_id, run_id, text) VALUES ('dir_1', $1, $2, $3, 'also add a test')`,
		w.org, wi, runID)
	w.until("the directive to be sent", func() bool {
		return w.count(`SELECT count(*) FROM directives WHERE id = 'dir_1' AND sent_at IS NOT NULL
			AND (accepted_at IS NOT NULL OR $1)`, legacy) == 1
	})
	if n := w.count(`SELECT count(*) FROM directives WHERE id = 'dir_1' AND delivered_at IS NOT NULL`); n != 0 {
		t.Errorf("a directive to a busy agent was reported delivered before the agent had it")
	}

	// One that interrupts is heard now, and so is everything queued before it.
	mustExec(t, w.owner, `INSERT INTO directives (id, organization_id, task_id, run_id, text, interrupt) VALUES ('dir_2', $1, $2, $3, 'stop and listen', true)`,
		w.org, wi, runID)
	w.until("both directives to be delivered", func() bool {
		return w.count(`SELECT count(*) FROM directives WHERE run_id = $1 AND delivered_at IS NOT NULL`, runID) == 2
	})
	r := w.lux.Runs()[0]
	if r.Interrupted != 1 || len(r.Inputs) != 2 || r.Inputs[0] != "also add a test" {
		t.Errorf("interrupted=%d inputs=%v", r.Interrupted, r.Inputs)
	}
	// Once, however many sweeps see them.
	for range 3 {
		w.pump()
	}
	if got := w.lux.Runs()[0].Inputs; len(got) != 2 {
		t.Errorf("directives sent %d times", len(got))
	}
}

// steerDuringTool starts an agent that hangs in a long bash command, and
// steers it: the fake lux in the mode the test set. Returns the Run.
func (w *world) steerDuringTool(wi string) string {
	w.t.Helper()
	w.lux.Decide = func(map[string]any) fakelux.Behaviour {
		return fakelux.Behaviour{Hang: true, Tools: []string{"bash"}, KeepToolsOpen: true}
	}
	w.deliver(wi)
	w.until("the agent to be running its command", func() bool {
		return w.count(`SELECT count(*) FROM events e JOIN runs r ON r.id = e.run_id
			WHERE r.task_id = $1 AND e.event_type = 'agent.tool.called'`, wi) == 1
	})
	var runID string
	_ = w.owner.QueryRow(context.Background(), `SELECT id FROM runs WHERE task_id = $1`, wi).Scan(&runID)
	mustExec(w.t, w.owner, `INSERT INTO directives (id, organization_id, task_id, run_id, text) VALUES ('dir_s', $1, $2, $3, 'check the migration too')`,
		w.org, wi, runID)
	w.until("the directive to be sent", func() bool {
		return w.count(`SELECT count(*) FROM directives WHERE id = 'dir_s' AND sent_at IS NOT NULL`) == 1
	})
	return runID
}

// A steer sent while a tool runs is taken at once and read at the agent's
// next step — after the tool, in the same turn, the tool never cancelled —
// and the ledger says so in that order.
func TestASteerLandsAtTheAgentsNextStep(t *testing.T) {
	w := newWorld(t)
	wi := w.task()
	runID := w.steerDuringTool(wi)
	w.until("the harness to take it", func() bool {
		return w.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = 'run.directive.accepted'
			AND payload->>'directiveId' = 'dir_s' AND payload->>'lands' = 'next_step'`, runID) == 1
	})
	w.pump()
	if n := w.count(`SELECT count(*) FROM directives WHERE id = 'dir_s' AND delivered_at IS NOT NULL`); n != 0 {
		t.Fatal("a steer was delivered while the tool it waits for still ran")
	}
	w.lux.FinishTools(w.lux.Runs()[0].ID)
	w.until("the agent's next step to read it", func() bool {
		return w.count(`SELECT count(*) FROM directives WHERE id = 'dir_s' AND delivered_at IS NOT NULL`) == 1
	})
	if r := w.lux.Runs()[0]; r.Interrupted != 0 {
		t.Errorf("the turn was interrupted %d times for a plain steer", r.Interrupted)
	}
	// Read after the tool it waited for, not where it was typed.
	if n := w.count(`SELECT count(*) FROM events d JOIN events c ON c.run_id = d.run_id
		WHERE d.run_id = $1 AND d.event_type = 'run.directive.delivered' AND (d.payload->>'read')::boolean
		  AND c.event_type = 'agent.tool.completed' AND c.cursor < d.cursor`, runID); n != 1 {
		t.Error("the delivery is not recorded after the tool's completion")
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND turn_done_at IS NULL AND status = 'running'`, runID); n != 1 {
		t.Error("reading the steer ended the agent's turn")
	}
}

// An older lux acknowledges once, when it hands the input over at the
// turn's end: no accepted event, delivered then.
func TestASteerToALegacyLuxIsDeliveredOnItsOneReceipt(t *testing.T) {
	w := newWorld(t)
	w.lux.LegacyInput = true
	wi := w.task()
	runID := w.steerDuringTool(wi)
	w.lux.FinishTools(w.lux.Runs()[0].ID)
	for range 3 {
		w.pump()
	}
	if n := w.count(`SELECT count(*) FROM directives WHERE id = 'dir_s' AND (delivered_at IS NOT NULL OR accepted_at IS NOT NULL)`); n != 0 {
		t.Fatal("a legacy lux's held input counted as taken before its receipt")
	}
	// Heard now, by a person's choice: the turn ends and the input is handed over.
	mustExec(t, w.owner, `INSERT INTO directives (id, organization_id, task_id, run_id, text, interrupt) VALUES ('dir_i', $1, $2, $3, 'stop', true)`,
		w.org, wi, runID)
	w.until("both to be delivered", func() bool {
		return w.count(`SELECT count(*) FROM directives WHERE run_id = $1 AND delivered_at IS NOT NULL`, runID) == 2
	})
	if n := w.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = 'run.directive.accepted'`, runID); n != 0 {
		t.Errorf("a legacy receipt wrote %d accepted events", n)
	}
	if n := w.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = 'run.directive.delivered'`, runID); n != 2 {
		t.Errorf("%d delivered events for two directives", n)
	}
}

// A harness that reads input only between turns says so when it takes the
// steer, and the agent reads it when its turn ends, not after the tool.
func TestASteerToANextTurnHarnessWaitsForTheTurn(t *testing.T) {
	w := newWorld(t)
	w.lux.NextTurnInput = true
	wi := w.task()
	runID := w.steerDuringTool(wi)
	w.until("the harness to take it for the next turn", func() bool {
		return w.count(`SELECT count(*) FROM directives WHERE id = 'dir_s' AND lands = 'next_turn'`) == 1
	})
	w.lux.FinishTools(w.lux.Runs()[0].ID)
	for range 3 {
		w.pump()
	}
	if n := w.count(`SELECT count(*) FROM directives WHERE id = 'dir_s' AND delivered_at IS NOT NULL`); n != 0 {
		t.Fatal("a next-turn steer was read mid-turn")
	}
	// The turn ends on its own: the agent reads it then, as its next turn.
	w.lux.EndTurn(w.lux.Runs()[0].ID)
	w.until("the agent to read it as the turn ends", func() bool {
		return w.count(`SELECT count(*) FROM directives WHERE id = 'dir_s' AND delivered_at IS NOT NULL`) == 1
	})
	if n := w.count(`SELECT count(*) FROM events d JOIN events s ON s.run_id = d.run_id
		WHERE d.run_id = $1 AND d.event_type = 'run.directive.delivered' AND (d.payload->>'read')::boolean
		  AND d.payload->>'directiveId' = 'dir_s' AND s.event_type = 'agent.session.stopped' AND s.cursor < d.cursor`, runID); n != 1 {
		t.Error("the read is not recorded after the turn's end")
	}
	if r := w.lux.Runs()[0]; r.Interrupted != 0 || len(r.Inputs) != 1 {
		t.Errorf("interrupted=%d inputs=%v, want no interrupt and the words once", r.Interrupted, r.Inputs)
	}
}

// turnDoneWithSteer plays an agent that finishes its first turn, seen done
// by the follower alone (no sweep acting on it), and a steer that reaches
// dude then: the Run, with the steer queued as 'dir_late'.
func (w *world) turnDoneWithSteer(wi string) string {
	t := w.t
	t.Helper()
	w.lux.Decide = func(map[string]any) fakelux.Behaviour { return fakelux.Behaviour{Reply: "Done."} }
	w.deliver(wi)
	w.until("the run to reach lux", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND lux_run_id IS NOT NULL`, wi) == 1
	})
	var runID string
	_ = w.owner.QueryRow(context.Background(), `SELECT id FROM runs WHERE task_id = $1`, wi).Scan(&runID)
	// The fake agent finishes its first turn on its own; once it has, this
	// sweep reads the Run before anything was followed, and starts the
	// follower.
	select {
	case <-w.lux.TurnsEnded(w.lux.Runs()[0].ID, 1):
	case <-time.After(10 * time.Second):
		t.Fatal("the agent's first turn never ended")
	}
	if _, err := w.syncer.Sweep(context.Background()); err != nil {
		t.Fatal(err)
	}
	w.await("the agent's turn never ended", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND turn_done_at IS NOT NULL`, runID) > 0
	})
	mustExec(t, w.owner, `INSERT INTO directives (id, organization_id, task_id, run_id, text) VALUES ('dir_late', $1, $2, $3, 'one more thing')`,
		w.org, wi, runID)
	return runID
}

// A steer sent as the agent's turn ends is not dropped: the Run waits to
// be collected until the agent has it, and it takes it as its next turn.
func TestASteerSentAsTheTurnEndsIsDeliveredBeforeTheRunFinishes(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		t.Run(map[bool]string{false: "receipts", true: "legacy lux"}[legacy], func(t *testing.T) {
			w := newWorld(t)
			w.lux.LegacyInput = legacy
			wi := w.task()
			runID := w.turnDoneWithSteer(wi)
			w.until("the run to finish", func() bool {
				return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status IN ('completed', 'failed')`, runID) == 1
			})
			if n := w.count(`SELECT count(*) FROM directives WHERE id = 'dir_late' AND delivered_at IS NOT NULL`); n != 1 {
				t.Error("a steer sent as the turn ended was dropped when the run finished")
			}
			if in := w.lux.Runs()[0].Inputs; !slices.Contains(in, "one more thing") {
				t.Errorf("the agent never had it: inputs %v", in)
			}
			// It was heard as a turn of its own, which the Run finished after.
			if n := w.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = 'agent.session.stopped'`, runID); n != 2 {
				t.Errorf("%d turns ended, want 2", n)
			}
		})
	}
}

// gateInput holds what input starts in an idle agent (fakelux InputGate)
// until release, which the test's cleanup also calls, so a failed
// assertion strands no fake worker.
func (w *world) gateInput() (release func()) {
	gate := make(chan struct{})
	w.lux.InputGate = gate
	var once sync.Once
	release = func() { once.Do(func() { close(gate) }) }
	w.t.Cleanup(release)
	return release
}

// lux answers the input POST before the agent's records for it arrive. In
// that window the steer is sent and unread: the Run is not collected. Once
// the agent takes it, the turn it starts ends before the Run is.
func TestASteerSentButNotYetReadHoldsTheFinishedTurn(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		t.Run(map[bool]string{false: "receipts", true: "legacy lux"}[legacy], func(t *testing.T) {
			w := newWorld(t)
			w.lux.LegacyInput = legacy
			release := w.gateInput()
			wi := w.task()
			runID := w.turnDoneWithSteer(wi)
			w.until("the steer to be sent", func() bool {
				return w.count(`SELECT count(*) FROM directives WHERE id = 'dir_late' AND sent_at IS NOT NULL`) == 1
			})
			for range 5 {
				w.pump()
			}
			if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND push_request_id IS NULL AND status = 'running'`, runID); n != 1 {
				t.Fatalf("a Run with a sent, unread steer was collected\nruns:\n%s", w.describeRuns())
			}
			if n := w.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = 'run.completed'`, runID); n != 0 {
				t.Fatal("a Run with a sent, unread steer completed")
			}
			release()
			w.until("the run to finish", func() bool {
				return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status IN ('completed', 'failed')`, runID) == 1
			})
			if n := w.count(`SELECT count(*) FROM directives WHERE id = 'dir_late' AND delivered_at IS NOT NULL AND failed_at IS NULL`); n != 1 {
				t.Error("the steer was not delivered")
			}
			// Both turns ended before the Run completed.
			if n := w.count(`SELECT count(*) FROM events s JOIN events c ON c.run_id = s.run_id AND c.event_type = 'run.completed'
				WHERE s.run_id = $1 AND s.event_type = 'agent.session.stopped' AND s.cursor < c.cursor`, runID); n != 2 {
				t.Errorf("%d turns ended before the run completed, want 2", n)
			}
		})
	}
}

// A steer lux took and never reported is waited on for unreadGraceSecs
// only: then the Run is collected, and the steer is marked failed.
func TestASteerNeverReadStopsHoldingTheRunAfterTheCap(t *testing.T) {
	w := newWorld(t)
	w.gateInput()
	wi := w.task()
	runID := w.turnDoneWithSteer(wi)
	w.until("the steer to be sent", func() bool {
		return w.count(`SELECT count(*) FROM directives WHERE id = 'dir_late' AND sent_at IS NOT NULL`) == 1
	})
	w.pump()
	if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND push_request_id IS NULL`, runID); n != 1 {
		t.Fatal("a Run with a steer sent a moment ago was collected")
	}
	// Sent longer ago than the cap: no receipt is coming.
	mustExec(t, w.owner, `UPDATE directives SET sent_at = now() - interval '121 seconds' WHERE id = 'dir_late'`)
	w.until("the run to finish", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'completed'`, runID) == 1
	})
	if n := w.count(`SELECT count(*) FROM directives WHERE id = 'dir_late' AND delivered_at IS NULL
		AND failed_at IS NOT NULL AND error = 'the run finished before the agent read it'`); n != 1 {
		t.Error("the unread steer was not marked failed when the run completed")
	}
	if n := w.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = 'run.directive.failed'
		AND payload->>'directiveId' = 'dir_late'`, runID); n != 1 {
		t.Errorf("%d failed events for the unread steer, want 1", n)
	}
}

// "Interrupt now" on a queued steer re-sends it to be heard at once, as a
// directive superseding it with the same words: the turn stops, the agent
// hears the text once, and both count as delivered with it.
func TestInterruptNowOnAQueuedSteerIsHeardOnce(t *testing.T) {
	w := newWorld(t)
	wi := w.task()
	runID, interruptID := w.interruptQueuedSteer(wi)
	w.until("both to be delivered", func() bool {
		return w.count(`SELECT count(*) FROM directives WHERE run_id = $1 AND delivered_at IS NOT NULL`, runID) == 2
	})
	r := w.lux.Runs()[0]
	if r.Interrupted != 1 || len(r.Inputs) != 1 || r.Inputs[0] != "check the migration too" {
		t.Errorf("interrupted=%d inputs=%v, want one interrupt and the text once", r.Interrupted, r.Inputs)
	}
	// The interrupt is settled with the steer, its delivery flagged as the
	// interrupt alone, not a second read.
	if n := w.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = 'run.directive.delivered'
		AND payload->>'directiveId' = $2 AND (payload->>'interruptOnly')::boolean AND payload->'read' IS NULL`, runID, interruptID); n != 1 {
		t.Errorf("%d interrupt-only delivered events for the interrupt, want 1", n)
	}
}

// interruptNow is a person's "Interrupt now" on a queued directive, through
// the API: its directive.
func (w *world) interruptNow(runID, supersedes string) string {
	w.t.Helper()
	code, out := w.call("/internal/runs/"+runID+"/steer", map[string]any{
		"text": "check the migration too", "supersedes": supersedes, "interrupt": true})
	if code != http.StatusCreated {
		w.t.Fatalf("steer: %d %v", code, out)
	}
	return out["id"].(string)
}

// The steer fails after the click and before the interrupt is first sent:
// the interrupt carries the words, and the agent hears them once.
func TestInterruptNowCarriesTheWordsWhenTheSteerFailedBeforeItWasSent(t *testing.T) {
	w := newWorld(t)
	wi := w.task()
	runID, interruptID := w.interruptQueuedSteer(wi)
	w.lux.FailInput(w.lux.Runs()[0].ID, "dir_s", "the agent exited")
	w.await("the steer's failure was never recorded", func() bool {
		return w.count(`SELECT count(*) FROM directives WHERE id = 'dir_s' AND failed_at IS NOT NULL`) > 0
	})
	w.until("the interrupt to be sent", func() bool {
		return w.count(`SELECT count(*) FROM directives WHERE id = $1 AND sent_at IS NOT NULL`, interruptID) == 1
	})
	r := w.lux.Runs()[0]
	if bodies := r.InputBodies[interruptID]; len(bodies) != 1 || !strings.Contains(bodies[0], `"text":"check the migration too"`) {
		t.Fatalf("interrupt request bodies %q, want one carrying the words", bodies)
	}
	w.until("the interrupt to be read", func() bool {
		return w.count(`SELECT count(*) FROM directives WHERE id = $1 AND delivered_at IS NOT NULL AND failed_at IS NULL`, interruptID) == 1
	})
	if r := w.lux.Runs()[0]; r.Interrupted != 1 || !slices.Equal(r.Inputs, []string{"check the migration too"}) {
		t.Errorf("interrupted=%d inputs=%q, want one interrupt and the words once", r.Interrupted, r.Inputs)
	}
	if n := w.count(`SELECT count(*) FROM directives WHERE id = $1 AND interrupt_only = false AND resends = 'dir_s'`, interruptID); n != 1 {
		t.Error("the interrupt's decision to carry the words was not kept")
	}
	if n := w.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = 'run.directive.delivered'
		AND payload->>'directiveId' = $2 AND (payload->>'read')::boolean`, runID, interruptID); n != 1 {
		t.Errorf("%d read events for the interrupt that carried the words, want 1", n)
	}
}

// A lux from before interrupts carried unread input over fails the steer
// the interrupt cancelled: the interrupt, which carried no words, fails
// with it, for the same reason, and nothing is left queued.
func TestInterruptNowOnAnOlderLuxFailsWithTheSteer(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		t.Run(map[bool]string{false: "receipts", true: "legacy lux"}[legacy], func(t *testing.T) {
			w := newWorld(t)
			w.lux.LegacyInput = legacy
			w.lux.FailUnreadOnInterrupt = true
			wi := w.task()
			runID, interruptID := w.interruptQueuedSteer(wi)
			w.until("both to fail", func() bool {
				return w.count(`SELECT count(*) FROM directives WHERE run_id = $1 AND failed_at IS NOT NULL AND delivered_at IS NULL
					AND error = 'the turn was cancelled before the agent read it'`, runID) == 2
			})
			for range 3 {
				w.pump()
			}
			if n := w.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = 'run.directive.failed'`, runID); n != 2 {
				t.Errorf("%d failed events, want one for each", n)
			}
			if n := w.count(`SELECT count(*) FROM directives WHERE id = $1 AND interrupt_only AND sent_at IS NOT NULL`, interruptID); n != 1 {
				t.Error("the interrupt was not sent as the interrupt alone")
			}
			if r := w.lux.Runs()[0]; r.Interrupted != 1 || len(r.Inputs) != 0 {
				t.Errorf("interrupted=%d inputs=%q, want one interrupt and the words never read", r.Interrupted, r.Inputs)
			}
		})
	}
}

// "Interrupt now" on an "Interrupt now" not yet heard: both resend the
// first steer, carry no words, and are delivered when it is read.
func TestInterruptNowTwiceIsOneInstruction(t *testing.T) {
	w := newWorld(t)
	wi := w.task()
	runID, first := w.interruptQueuedSteer(wi)
	second := w.interruptNow(runID, first)
	w.until("all three to be delivered", func() bool {
		return w.count(`SELECT count(*) FROM directives WHERE run_id = $1 AND delivered_at IS NOT NULL`, runID) == 3
	})
	if n := w.count(`SELECT count(*) FROM directives WHERE id IN ($1, $2) AND resends = 'dir_s' AND interrupt_only`, first, second); n != 2 {
		t.Errorf("%d of the two interrupts resend the steer as the interrupt alone, want 2", n)
	}
	if r := w.lux.Runs()[0]; !slices.Equal(r.Inputs, []string{"check the migration too"}) {
		t.Errorf("inputs %q, want the words once", r.Inputs)
	}
}

// A click from a transcript that still showed the steer queued after the
// agent read it: the interrupt goes alone, is delivered as it is sent with
// a delivery event of its own, and holds no finished turn open.
func TestInterruptNowOnASteerAlreadyReadIsSettledWhenSent(t *testing.T) {
	w := newWorld(t)
	wi := w.task()
	runID := w.steerDuringTool(wi)
	w.lux.FinishTools(w.lux.Runs()[0].ID)
	w.waitRead()
	interruptID := w.interruptNow(runID, "dir_s")
	w.until("the run to finish", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'completed'`, runID) == 1
	})
	if n := w.count(`SELECT count(*) FROM directives WHERE id = $1 AND interrupt_only AND delivered_at IS NOT NULL AND failed_at IS NULL`, interruptID); n != 1 {
		t.Error("the interrupt was not delivered as the interrupt alone")
	}
	if n := w.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = 'run.directive.delivered'
		AND payload->>'directiveId' = $2 AND (payload->>'interruptOnly')::boolean AND payload->'read' IS NULL`, runID, interruptID); n != 1 {
		t.Errorf("%d interrupt-only delivered events, want 1", n)
	}
	if r := w.lux.Runs()[0]; !slices.Equal(r.Inputs, []string{"check the migration too"}) {
		t.Errorf("inputs %q, want the words once", r.Inputs)
	}
}

// interruptQueuedSteer is a steer the harness took (sent, to a legacy lux)
// while a tool runs, and a person's "Interrupt now" on it, recorded by the API and not yet sent:
// returns the Run and the interrupt's directive.
func (w *world) interruptQueuedSteer(wi string) (runID, interruptID string) {
	w.t.Helper()
	runID = w.steerDuringTool(wi)
	// A legacy lux says nothing until it hands the steer over: sent is all
	// there is to wait on.
	if !w.lux.LegacyInput {
		w.until("the harness to take it", func() bool {
			return w.count(`SELECT count(*) FROM directives WHERE id = 'dir_s' AND accepted_at IS NOT NULL`) == 1
		})
	}
	return runID, w.interruptNow(runID, "dir_s")
}

// waitRead waits, without sweeping, for the follower to record the
// original steer read.
func (w *world) waitRead() {
	w.t.Helper()
	w.await("the steer was never read", func() bool {
		return w.count(`SELECT count(*) FROM directives WHERE id = 'dir_s' AND delivered_at IS NOT NULL`) > 0
	})
}

// await polls cond for 10 s without sweeping, for what the follower alone
// records, and fails the test with failure if it never holds.
func (w *world) await(failure string, cond func() bool) {
	w.t.Helper()
	for deadline := time.Now().Add(10 * time.Second); !cond(); time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			w.t.Fatal(failure)
		}
	}
}

// The agent reads the original steer after the click and before the syncer
// sends the interrupt: the interrupt still goes, the words do not go again.
func TestInterruptNowOnASteerReadBeforeItIsSentSendsNoWords(t *testing.T) {
	w := newWorld(t)
	wi := w.task()
	runID, interruptID := w.interruptQueuedSteer(wi)
	w.lux.FinishTools(w.lux.Runs()[0].ID)
	w.waitRead()
	w.until("the interrupt to be sent and delivered", func() bool {
		return w.count(`SELECT count(*) FROM directives WHERE id = $1 AND sent_at IS NOT NULL AND delivered_at IS NOT NULL`, interruptID) == 1
	})
	r := w.lux.Runs()[0]
	if r.Interrupted != 1 || len(r.Inputs) != 1 {
		t.Errorf("interrupted=%d inputs=%v, want one interrupt and the words once", r.Interrupted, r.Inputs)
	}
	// One read, the original's; the interrupt's delivery is flagged as the
	// interrupt alone.
	if n := w.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = 'run.directive.delivered'
		AND (payload->>'read')::boolean`, runID); n != 1 {
		t.Errorf("%d read events, want the original's alone", n)
	}
	if n := w.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = 'run.directive.delivered'
		AND payload->>'directiveId' = $2 AND (payload->>'interruptOnly')::boolean`, runID, interruptID); n != 1 {
		t.Errorf("%d interrupt-only delivered events, want 1", n)
	}
}

// The agent reads the original between the syncer's reading the interrupt
// and lux answering it, and lux refuses that first request: the retry is
// the same request, still with no words.
func TestInterruptNowRetriedAfterTheSteerWasReadSendsTheSameRequest(t *testing.T) {
	w := newWorld(t)
	w.syncer.RetryAhead = time.Minute
	wi := w.task()
	_, interruptID := w.interruptQueuedSteer(wi)
	var once sync.Once
	w.lux.BeforeInput = func(luxRunID, requestID string) bool {
		first := false
		once.Do(func() {
			first = true
			w.lux.FinishTools(luxRunID)
			w.waitRead()
		})
		return !first
	}
	w.until("the interrupt to be sent", func() bool {
		return w.count(`SELECT count(*) FROM directives WHERE id = $1 AND sent_at IS NOT NULL`, interruptID) == 1
	})
	r := w.lux.Runs()[0]
	if bodies := r.InputBodies[interruptID]; len(bodies) != 2 || bodies[0] != bodies[1] {
		t.Errorf("request bodies for one request id: %q, want two identical", bodies)
	}
	if r.Interrupted != 1 || len(r.Inputs) != 1 {
		t.Errorf("interrupted=%d inputs=%v, want one interrupt and the words once", r.Interrupted, r.Inputs)
	}
}

func TestPauseKeepsTheRunAndResumeContinuesIt(t *testing.T) {
	w := newWorld(t)
	w.lux.Decide = func(map[string]any) fakelux.Behaviour {
		return fakelux.Behaviour{Hang: true, Reply: "Done after resume.", Commit: map[string]string{"A.md": "a\n"}}
	}
	wi := w.task()
	w.deliver(wi)
	w.until("the agent to be working", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND status = 'running'`, wi) == 1
	})
	var runID string
	_ = w.owner.QueryRow(context.Background(), `SELECT id FROM runs WHERE task_id = $1`, wi).Scan(&runID)

	mustExec(t, w.owner, `UPDATE runs SET control = 'pause_graceful' WHERE id = $1`, runID)
	w.until("the run to pause", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'paused'`, runID) == 1
	})
	// lux stops it, reporting "stopped" a moment later.
	w.until("lux to report the run stopped", func() bool { r := w.lux.Runs()[0]; return r.Stopped == 1 && r.State == "stopped" })

	// A directive given while paused, then the request to resume.
	mustExec(t, w.owner, `INSERT INTO directives (id, organization_id, task_id, run_id, text) VALUES ('dir_r', $1, $2, $3, 'carry on')`,
		w.org, wi, runID)
	// Its cost read as final before the resume, which lux undoes.
	mustExec(t, w.owner, `UPDATE runs SET lux_cost_status = 'final', lux_cost_next_at = NULL WHERE id = $1`, runID)
	mustExec(t, w.owner, `UPDATE runs SET control = 'resume' WHERE id = $1`, runID)
	w.until("the resumed run to finish its turn", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'completed'`, runID) == 1
	})
	if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND lux_cost_next_at IS NOT NULL`, runID); n != 1 {
		t.Error("a resumed Run whose cost was final is not back on the cost work list")
	}
	r := w.lux.Runs()[0]
	if r.Resumed != 1 {
		t.Errorf("resumed %d times", r.Resumed)
	}
	// The same lux Run, continued — not a new one.
	if len(w.lux.Runs()) < 1 || w.count(`SELECT count(DISTINCT lux_run_id) FROM runs WHERE id = $1`, runID) != 1 {
		t.Errorf("resume made a new lux run")
	}
	if len(r.Inputs) != 1 || r.Inputs[0] != "carry on" {
		t.Errorf("a directive given while paused was not delivered after the resume: %v", r.Inputs)
	}
	if n := w.count(`SELECT count(*) FROM directives WHERE id = 'dir_r' AND delivered_at IS NOT NULL`); n != 1 {
		t.Errorf("a directive delivered after a resume was never acknowledged")
	}
	// The session started once, even across two placements.
	if n := w.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = 'agent.session.started'`, runID); n != 1 {
		t.Errorf("session started %d times", n)
	}
}

func TestAbortCancelsTheLuxRun(t *testing.T) {
	w := newWorld(t)
	w.lux.Decide = func(map[string]any) fakelux.Behaviour { return fakelux.Behaviour{Hang: true} }
	wi := w.task()
	w.deliver(wi)
	w.until("the agent to be working", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND status = 'running'`, wi) == 1
	})
	mustExec(t, w.owner, `UPDATE runs SET status = 'aborted', control = 'abort' WHERE task_id = $1`, wi)
	w.until("the lux run to be cancelled", func() bool { return w.lux.Runs()[0].Cancelled })
}

func TestAnAgentThatDiesFailsItsPhaseAndEscalates(t *testing.T) {
	w := newWorld(t)
	w.lux.Decide = func(map[string]any) fakelux.Behaviour { return fakelux.Behaviour{Crash: true} }
	wi := w.task()
	w.deliver(wi)
	w.until("the task to need a person", func() bool { return w.taskStatus(wi) == "awaiting_input" })
	if n := w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND status = 'failed'`, wi); n != 1 {
		t.Errorf("failed runs = %d", n)
	}
}

func TestARunLuxNoLongerHasFailsItsPhase(t *testing.T) {
	w := newWorld(t)
	w.lux.Decide = func(map[string]any) fakelux.Behaviour { return fakelux.Behaviour{Hang: true} }
	wi := w.task()
	w.deliver(wi)
	w.until("the agent to be working", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND status = 'running'`, wi) == 1
	})
	w.lux.Forget()
	w.until("the task to need a person", func() bool { return w.taskStatus(wi) == "awaiting_input" })
}

func TestAnImplementerThatChangesNothingIsEscalated(t *testing.T) {
	w := newWorld(t)
	w.lux.Decide = func(map[string]any) fakelux.Behaviour { return fakelux.Behaviour{Reply: "Nothing to do."} }
	wi := w.task()
	w.deliver(wi)
	w.until("escalation", func() bool { return w.taskStatus(wi) == "awaiting_input" })
	var reason string
	_ = w.owner.QueryRow(context.Background(), `SELECT payload->>'reason' FROM events WHERE task_id = $1
		AND event_type = 'task.status_changed' ORDER BY cursor DESC LIMIT 1`, wi).Scan(&reason)
	if reason != "no_changes" {
		t.Errorf("reason = %s", reason)
	}
}

func TestLuxRefusingASpecFailsThePhaseRatherThanRetryingForever(t *testing.T) {
	w := newWorld(t)
	mustExec(t, w.owner, `UPDATE projects SET agent_models = '{}'::jsonb WHERE id = $1`, w.project)
	wi := w.task()
	w.deliver(wi)
	w.until("escalation", func() bool { return w.taskStatus(wi) == "awaiting_input" })
	var errText string
	_ = w.owner.QueryRow(context.Background(), `SELECT error FROM runs WHERE task_id = $1`, wi).Scan(&errText)
	if !strings.Contains(errText, "no model is configured for the implementer role") {
		t.Errorf("error = %q", errText)
	}
	if len(w.lux.Runs()) != 0 {
		t.Errorf("a run with no model reached lux")
	}
}

func TestPushPreflightRefusesBeforeLuxSubmission(t *testing.T) {
	for _, status := range []int{201, 302, 401, 403, 404} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			w := newWorld(t)
			w.gh.Set(func(s *fakegithub.Server) { s.ReceiveStatus = status })
			wi := w.task()
			w.deliver(wi)
			w.until("preflight failure", func() bool {
				return w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND status = 'failed'`, wi) == 1
			})
			if len(w.lux.Runs()) != 0 {
				t.Fatal("submitted after refused preflight")
			}
			var reason string
			_ = w.owner.QueryRow(context.Background(), `SELECT error FROM runs WHERE task_id = $1`, wi).Scan(&reason)
			if !strings.Contains(reason, "target") || (status == 401 || status == 403) && !strings.Contains(reason, "Contents: Read and write") {
				t.Fatalf("reason: %s", reason)
			}
		})
	}
}

func TestResumePushDenialCancelsThePausedLuxRun(t *testing.T) {
	for _, transient := range []bool{false, true} {
		t.Run(fmt.Sprint(transient), func(t *testing.T) {
			w := newWorld(t)
			w.lux.Decide = func(map[string]any) fakelux.Behaviour { return fakelux.Behaviour{Hang: true} }
			var mu sync.Mutex
			cancelAttempts := 0
			refuseCancel := transient
			handler := w.lux.Handler()
			proxy := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
				if strings.HasSuffix(req.URL.Path, "/cancel") {
					mu.Lock()
					cancelAttempts++
					refuse := refuseCancel
					mu.Unlock()
					if refuse {
						http.Error(rw, "temporarily unavailable", http.StatusServiceUnavailable)
						return
					}
				}
				handler.ServeHTTP(rw, req)
			}))
			t.Cleanup(proxy.Close)
			w.syncer.Lux = lux.New(proxy.URL, "lux-key")
			wi := w.task()
			w.deliver(wi)
			w.until("publishing run to be running", func() bool {
				return w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'implement' AND status = 'running'`, wi) == 1
			})
			var runID string
			if err := w.owner.QueryRow(context.Background(), `SELECT id FROM runs WHERE task_id = $1`, wi).Scan(&runID); err != nil {
				t.Fatal(err)
			}
			mustExec(t, w.owner, `UPDATE runs SET control = 'pause_graceful' WHERE id = $1`, runID)
			w.until("paused run's stopped state to be recorded", func() bool {
				return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'paused' AND lux_state = 'stopped'`, runID) == 1
			})
			w.gh.Set(func(s *fakegithub.Server) { s.ReceiveStatus = 403 })
			if status, body := w.call("/internal/runs/"+runID+"/resume", map[string]any{}); status != 200 {
				t.Fatalf("resume: %d %v", status, body)
			}
			w.until("resume preflight failure", func() bool {
				return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'failed'`, runID) == 1
			})
			w.until("cancellation attempt", func() bool {
				mu.Lock()
				defer mu.Unlock()
				return cancelAttempts > 0
			})
			if transient {
				if w.lux.Runs()[0].Cancelled || w.count(`SELECT count(*) FROM runs WHERE id = $1 AND lux_stop_reason = 'cancel'`, runID) != 0 {
					t.Fatal("transient cancellation failure was marked as cancelled")
				}
				if w.count(`SELECT count(*) FROM runs WHERE id = $1 AND next_attempt_at > now()`, runID) != 1 {
					t.Fatal("transient cancellation did not back off")
				}
				mu.Lock()
				refuseCancel = false
				mu.Unlock()
				w.syncer.RetryAhead = time.Minute
			}
			w.until("lux run actually cancelled and cancellation recorded", func() bool {
				return w.lux.Runs()[0].Cancelled && w.count(`SELECT count(*) FROM runs WHERE id = $1 AND lux_stop_reason = 'cancel' AND control = 'none'`, runID) == 1
			})
			if r := w.lux.Runs()[0]; r.Resumed != 0 || r.Stopped != 1 {
				t.Fatalf("denied run resumed or stopped again: %+v", r)
			}
			mu.Lock()
			attempts := cancelAttempts
			mu.Unlock()
			for range 3 {
				w.pump()
			}
			mu.Lock()
			defer mu.Unlock()
			if cancelAttempts != attempts || transient && attempts < 2 {
				t.Fatalf("cancel attempts: before %d, after %d", attempts, cancelAttempts)
			}
		})
	}
}

func TestPushPreflightRetriesWithFreshCredential(t *testing.T) {
	for _, network := range []bool{false, true} {
		t.Run(fmt.Sprint(network), func(t *testing.T) {
			w := newWorld(t)
			w.lux.Decide = func(map[string]any) fakelux.Behaviour { return fakelux.Behaviour{Hang: true} }
			w.gh.Set(func(s *fakegithub.Server) { s.ReceiveStatus = 503; s.ReceiveDisconnect = network })
			wi := w.task()
			w.addWeb(wi, "write")
			w.deliver(wi)
			w.pump()
			if len(w.lux.Runs()) != 0 || w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND status = 'pending' AND next_attempt_at > now()`, wi) != 1 {
				t.Fatal("transient preflight did not back off before submit")
			}
			mustExec(t, w.owner, `UPDATE forge_credentials SET secret = 'replacement' WHERE organization_id = $1`, w.org)
			w.gh.Set(func(s *fakegithub.Server) {
				s.ReceiveStatus = 200
				s.ReceiveDisconnect = false
				s.ReceiveToken = "replacement"
			})
			w.syncer.RetryAhead = time.Minute
			w.until("fresh-token submission", func() bool { return len(w.lux.Runs()) == 1 })
			w.gh.Set(func(s *fakegithub.Server) {
				if len(s.ReceiveRequests) != 2 || s.ReceiveRequests[0] != "ghp_test" || s.ReceiveRequests[1] != "replacement" {
					t.Errorf("requests: %v", s.ReceiveRequests)
				}
			})
			var spec lux.Spec
			if err := json.Unmarshal(w.lux.Runs()[0].Spec, &spec); err != nil {
				t.Fatal(err)
			}
			tokens := 0
			for _, secret := range spec.Secrets {
				if secret.Name == "GIT_TOKEN" {
					tokens++
					if secret.Value != "replacement" {
						t.Fatalf("GIT_TOKEN value = %q, want replacement", secret.Value)
					}
				}
			}
			if tokens != 1 {
				t.Fatalf("GIT_TOKEN secrets = %d, want one", tokens)
			}
			if spec.Git == nil || len(spec.Git.Repositories) != 2 {
				t.Fatalf("want two named repositories, got %+v", spec.Git)
			}
			names := map[string]bool{}
			for _, repo := range spec.Git.Repositories {
				names[repo.Name] = true
				if repo.Credential != "GIT_TOKEN" {
					t.Errorf("repository %s credential = %q, want GIT_TOKEN", repo.Name, repo.Credential)
				}
			}
			if !names["target"] || !names["web"] {
				t.Fatalf("named repositories = %v, want target and web", names)
			}
		})
	}
}

func TestPushPreflightRateLimitsWaitBeforeLuxSubmission(t *testing.T) {
	testPushPreflightRateLimits(t, false)
}

func TestPushPreflightRateLimitsWaitBeforePausedResume(t *testing.T) {
	testPushPreflightRateLimits(t, true)
}

// GitHub's wait on a limited preflight is honoured: the Run is not tried
// again before it, nor is lux asked. Without one, the usual 5 s back-off.
func testPushPreflightRateLimits(t *testing.T, resume bool) {
	t.Helper()
	const secondary = "You have exceeded a secondary rate limit. Please wait a few minutes before you try again."
	for _, limit := range []struct {
		name, message string
		headers       func() http.Header
		wait          time.Duration
	}{
		{"primary", "API rate limit exceeded for 127.0.0.1.", func() http.Header {
			return http.Header{"Retry-After": {"60"}, "X-Ratelimit-Remaining": {"0"},
				"X-Ratelimit-Reset": {fmt.Sprint(time.Now().Add(time.Minute).Unix())}}
		}, time.Minute},
		{"secondary", secondary, func() http.Header {
			return http.Header{"Retry-After": {"60"}, "X-Ratelimit-Remaining": {"4999"}}
		}, time.Minute},
		{"reset only", "API rate limit exceeded for 127.0.0.1.", func() http.Header {
			return http.Header{"X-Ratelimit-Remaining": {"0"}, "X-Ratelimit-Reset": {fmt.Sprint(time.Now().Add(2 * time.Minute).Unix())}}
		}, 2 * time.Minute},
		{"capped", secondary, func() http.Header { return http.Header{"Retry-After": {"86400"}} }, time.Hour},
		{"no timing", secondary, func() http.Header { return http.Header{"X-Ratelimit-Remaining": {"4999"}} }, 5 * time.Second},
	} {
		t.Run(limit.name, func(t *testing.T) {
			w := newWorld(t)
			w.lux.Decide = func(map[string]any) fakelux.Behaviour { return fakelux.Behaviour{Hang: true} }
			wi := w.task()
			w.deliver(wi)
			wantStatus, wantControl, wantLuxRuns, wantRequests := "pending", "none", 0, 1
			if resume {
				w.until("running before pause", func() bool {
					return w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND status = 'running'`, wi) == 1
				})
				mustExec(t, w.owner, `UPDATE runs SET control = 'pause_graceful' WHERE task_id = $1`, wi)
				w.until("paused before rate limit", func() bool {
					return w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND status = 'paused'
						AND lux_state = 'stopped'`, wi) == 1
				})
				mustExec(t, w.owner, `UPDATE runs SET control = 'resume' WHERE task_id = $1`, wi)
				wantStatus, wantControl, wantLuxRuns, wantRequests = "paused", "resume", 1, 2
			}
			body, err := json.Marshal(map[string]string{"message": limit.message})
			if err != nil {
				t.Fatal(err)
			}
			headers := limit.headers()
			headers.Set("Content-Type", "application/json")
			w.gh.Set(func(s *fakegithub.Server) {
				s.ReceiveStatus = http.StatusForbidden
				s.ReceiveBody = string(body)
				s.ReceiveHeaders = headers
			})
			before := time.Now()
			w.pump()
			after := time.Now()
			readBackoff := func() time.Time {
				t.Helper()
				var status, control string
				var due *time.Time
				if err := w.owner.QueryRow(context.Background(), `SELECT status::text, control::text, next_attempt_at
					FROM runs WHERE task_id = $1`, wi).Scan(&status, &control, &due); err != nil {
					t.Fatal(err)
				}
				if status != wantStatus || control != wantControl || due == nil {
					t.Fatalf("rate limit: status=%s control=%s next_attempt_at=%v, want %s/%s with a backoff",
						status, control, due, wantStatus, wantControl)
				}
				return *due
			}
			due := readBackoff()
			// Header timing is whole seconds from a clock read in between.
			if earliest, latest := before.Add(limit.wait-time.Second), after.Add(limit.wait+time.Second); due.Before(earliest) || due.After(latest) {
				t.Fatalf("next_attempt_at = %v, want %v after the preflight (%v..%v)", due, limit.wait, earliest, latest)
			}
			assertNoAttempt := func() {
				t.Helper()
				if runs := w.lux.Runs(); len(runs) != wantLuxRuns || resume && runs[0].Resumed != 0 {
					t.Fatalf("rate-limited preflight reached lux: runs=%d", len(runs))
				}
				w.gh.Set(func(s *fakegithub.Server) {
					if len(s.ReceiveRequests) != wantRequests {
						t.Errorf("preflight requests=%d, want %d (no retry before due)", len(s.ReceiveRequests), wantRequests)
					}
				})
				if got := readBackoff(); !got.Equal(due) {
					t.Fatalf("backoff changed before due: %v -> %v", due, got)
				}
				if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'run.failed'`, wi); n != 0 {
					t.Fatalf("rate limit permanently failed the run: %d failure events", n)
				}
			}
			assertNoAttempt()
			if limit.wait > 5*time.Second {
				// The sweep looks half the wait ahead: a 5 s back-off would be
				// due by then, and the preflight asked again.
				w.syncer.RetryAhead = limit.wait / 2
			}
			for range 3 {
				w.pump()
				assertNoAttempt()
			}
			w.syncer.RetryAhead = 0
			w.gh.Set(func(s *fakegithub.Server) {
				s.ReceiveStatus = http.StatusOK
				s.ReceiveBody = ""
				s.ReceiveHeaders = nil
			})
			// Make the persisted retry due without sleeping through the backoff.
			mustExec(t, w.owner, `UPDATE runs SET next_attempt_at = now() - interval '1 second' WHERE task_id = $1`, wi)
			w.until("recovery after rate limit", func() bool {
				return w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND status = 'running'`, wi) == 1
			})
			if runs := w.lux.Runs(); len(runs) != 1 || resume && runs[0].Resumed != 1 {
				t.Fatalf("recovery: lux runs=%d, want one run and exactly one resume if paused", len(runs))
			}
			w.gh.Set(func(s *fakegithub.Server) {
				if len(s.ReceiveRequests) != wantRequests+1 {
					t.Errorf("recovery preflight requests=%d, want %d", len(s.ReceiveRequests), wantRequests+1)
				}
			})
		})
	}
}

func TestPushPreflightChecksEveryWritableNamedRepository(t *testing.T) {
	for _, readOnly := range []bool{false, true} {
		t.Run(fmt.Sprint(readOnly), func(t *testing.T) {
			w := newWorld(t)
			w.lux.Decide = func(map[string]any) fakelux.Behaviour { return fakelux.Behaviour{Hang: true} }
			wi := w.task()
			w.addWeb(wi, "write")
			if readOnly {
				mustExec(t, w.owner, `UPDATE task_repositories SET access = 'read' WHERE task_id = $1 AND repository_id = $2`, wi, "repo_web_"+w.org)
			}
			w.web.Set(func(s *fakegithub.Server) { s.ReceiveStatus = 403 })
			w.deliver(wi)
			w.until("preflight outcome", func() bool {
				return len(w.lux.Runs()) == 1 || w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND status = 'failed'`, wi) == 1
			})
			if (len(w.lux.Runs()) == 1) != readOnly {
				t.Fatal("wrong submit outcome")
			}
			w.web.Set(func(s *fakegithub.Server) {
				if (len(s.ReceiveRequests) == 0) != readOnly {
					t.Errorf("readonly checked or writable unchecked: %v", s.ReceiveRequests)
				}
			})
		})
	}
}

func TestPushPreflightSkipsNonPublishingPhases(t *testing.T) {
	w := newWorld(t)
	w.lux.Decide = func(map[string]any) fakelux.Behaviour { return fakelux.Behaviour{Hang: true} }
	w.gh.Set(func(s *fakegithub.Server) { s.ReceiveStatus = 403 })
	wi := w.task()
	w.deliver(wi)
	if _, err := w.runtime.Tick(context.Background(), 10); err != nil {
		t.Fatal(err)
	}
	mustExec(t, w.owner, `UPDATE runs SET phase = 'review' WHERE task_id = $1`, wi)
	w.until("review submission", func() bool { return len(w.lux.Runs()) == 1 })
	w.gh.Set(func(s *fakegithub.Server) {
		if len(s.ReceiveRequests) != 0 {
			t.Fatal("review checked push access")
		}
	})
}

func TestWorkflowPushFailureRetainsGitErrorAndExplainsBothTokenTypes(t *testing.T) {
	w := newWorld(t)
	w.lux.Decide = func(map[string]any) fakelux.Behaviour { return fakelux.Behaviour{Hang: true} }
	wi := w.task()
	w.deliver(wi)
	w.until("running", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND status = 'running'`, wi) == 1
	})
	original := "refusing to allow a Personal Access Token to create or update workflow .github/workflows/ci.yml without workflow scope"
	raw, _ := json.Marshal(map[string]any{"results": []map[string]string{{"repo": "target", "status": "failed", "error": original}}})
	mustExec(t, w.owner, `UPDATE runs SET turn_done_at = now(), push_result = $2::jsonb WHERE task_id = $1`, wi, raw)
	w.until("push failure", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND status = 'failed'`, wi) == 1
	})
	var reason string
	if err := w.owner.QueryRow(context.Background(), `SELECT error FROM runs WHERE task_id = $1`, wi).Scan(&reason); err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{original, "Workflows: Read and write", "workflow scope", "preflight does not establish"} {
		if !strings.Contains(reason, text) {
			t.Errorf("missing %q: %s", text, reason)
		}
	}
}

func TestMain(m *testing.M) {
	if _, err := exec.LookPath("git"); err != nil {
		fmt.Println("git is required")
		os.Exit(1)
	}
	os.Exit(m.Run())
}

// A Run whose turn ended while no one was following its stream (a restart,
// a dropped connection) must still learn what its push did. Found in review:
// finishing Runs were never followed again, and waited forever.
func TestInitialGitEventsSurviveAStreamReconnectWithoutDuplicates(t *testing.T) {
	w := newWorld(t)
	w.lux.Decide = func(map[string]any) fakelux.Behaviour { return fakelux.Behaviour{Hang: true} }
	wi := w.task()
	w.names(wi, w.repoID)
	w.deliver(wi)
	w.until("initial clone and checkout in the ledger", func() bool {
		return w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type IN ('git.clone', 'git.checkout')`, wi) == 2
	})
	var runID, base string
	if err := w.owner.QueryRow(context.Background(), `SELECT id, base_shas->>'target' FROM runs WHERE task_id = $1`, wi).Scan(&runID, &base); err != nil {
		t.Fatal(err)
	}
	for _, typ := range []string{"git.clone", "git.checkout"} {
		var raw []byte
		if err := w.owner.QueryRow(context.Background(), `SELECT payload FROM events WHERE run_id = $1 AND event_type = $2`, runID, typ).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		var payload map[string]any
		if err := json.Unmarshal(raw, &payload); err != nil {
			t.Fatal(err)
		}
		if payload["repo"] != "target" || payload["ref"] != "main" || base == "" {
			t.Fatalf("%s payload: %v, base %q", typ, payload, base)
		}
		if typ == "git.clone" && (payload["status"] != "cloned" || payload["commit"] != base) {
			t.Fatalf("clone outcome: %v", payload)
		}
		if typ == "git.checkout" && payload["base"] != base {
			t.Fatalf("checkout: %v", payload)
		}
	}
	w.syncer.Stop()
	w.syncer = &phases.Syncer{DB: w.syncer.DB, Lux: w.syncer.Lux, Forges: w.syncer.Forges, Log: quiet, Agent: w.syncer.Agent}
	t.Cleanup(w.syncer.Stop)
	status, body := w.call("/internal/runs/"+runID+"/steer", map[string]any{"text": "Keep working", "interrupt": true})
	if status != 201 {
		t.Fatalf("steer: %d %v", status, body)
	}
	w.until("reconnected stream to deliver steering", func() bool {
		return w.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = 'run.directive.delivered'`, runID) == 1
	})
	if n := w.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type IN ('git.clone', 'git.checkout')`, runID); n != 2 {
		t.Fatalf("replayed git events: %d, want 2", n)
	}
}

func TestInitialCloneFailureIsRecordedWithoutACheckout(t *testing.T) {
	w := newWorld(t)
	mustExec(t, w.owner, `UPDATE repositories SET default_branch = 'missing-ref' WHERE id = $1`, w.repoID)
	wi := w.task()
	w.names(wi, w.repoID)
	w.deliver(wi)
	w.until("clone failure in the ledger", func() bool {
		return w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'git.clone'`, wi) == 1
	})
	var raw []byte
	if err := w.owner.QueryRow(context.Background(), `SELECT payload FROM events WHERE task_id = $1 AND event_type = 'git.clone'`, wi).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	if payload["repo"] != "target" || payload["ref"] != "missing-ref" || payload["status"] != "failed" || payload["error"] != "ref not found" {
		t.Fatalf("clone failure: %v", payload)
	}
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'git.checkout'`, wi); n != 0 {
		t.Fatalf("failed clone produced %d checkouts", n)
	}
	if n := w.count(`SELECT count(*) FROM repository_requests WHERE task_id = $1`, wi); n != 0 {
		t.Fatalf("initial clone created %d approvals", n)
	}
}

func TestAFinishingRunIsFollowedAfterARestart(t *testing.T) {
	w := newWorld(t)
	wi := w.task()
	w.deliver(wi)
	w.until("the implementer's push to be asked for", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'implement' AND push_request_id IS NOT NULL`, wi) == 1
	})
	// A new orchestrator: nothing is following anything.
	w.syncer.Stop()
	w.syncer = &phases.Syncer{DB: w.syncer.DB, Lux: w.syncer.Lux, Forges: w.syncer.Forges, Log: quiet, Agent: w.syncer.Agent}
	t.Cleanup(w.syncer.Stop)
	w.until("the implementer to complete", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'implement' AND status = 'completed'`, wi) == 1
	})
}

// An abort that lands while the Run is being submitted wins. Found in
// review: submit set the Run back to scheduled, and its lux Run carried on.
func TestAnAbortDuringSubmitIsNotUndone(t *testing.T) {
	w := newWorld(t)
	w.lux.Decide = func(map[string]any) fakelux.Behaviour { return fakelux.Behaviour{Hang: true} }
	wi := w.task()
	w.deliver(wi)
	// Create the Run without submitting it, then abort it and let the
	// submit happen: the row the submit sees is already aborted.
	for range 5 {
		if _, err := w.runtime.Tick(context.Background(), 10); err != nil {
			t.Fatal(err)
		}
	}
	mustExec(t, w.owner, `UPDATE runs SET status = 'aborted', control = 'abort' WHERE task_id = $1`, wi)
	var runID string
	_ = w.owner.QueryRow(context.Background(), `SELECT id FROM runs WHERE task_id = $1`, wi).Scan(&runID)
	// Submit as the sweep would have, had it read the row a moment earlier.
	mustExec(t, w.owner, `UPDATE runs SET status = 'pending' WHERE id = $1`, runID)
	go func() {
		time.Sleep(5 * time.Millisecond)
		_, _ = w.owner.Exec(context.Background(), `UPDATE runs SET status = 'aborted' WHERE id = $1`, runID)
	}()
	// Either the submit got in first, and the lux Run it made is cancelled,
	// or the abort did, and nothing was ever submitted. Never a live Run.
	w.until("no live lux run", func() bool {
		runs := w.lux.Runs()
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'aborted'`, runID) == 1 &&
			(len(runs) == 0 || len(runs) == 1 && runs[0].Cancelled)
	})
	var status string
	_ = w.owner.QueryRow(context.Background(), `SELECT status::text FROM runs WHERE id = $1`, runID).Scan(&status)
	if status != "aborted" {
		t.Errorf("status = %s, want the abort to stand", status)
	}
}

// Pausing is not the agent dying. Found in review: lux's "stopped" could be
// read before dude recorded why, and the Run was marked failed.
func TestAResumedAgentNobodySteeredIsToldToCarryOn(t *testing.T) {
	w := newWorld(t)
	w.lux.Decide = func(map[string]any) fakelux.Behaviour {
		return fakelux.Behaviour{Hang: true, Reply: "Done after resume.", Commit: map[string]string{"A.md": "a\n"}}
	}
	wi := w.task()
	w.deliver(wi)
	w.until("the agent to be working", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND status = 'running'`, wi) == 1
	})
	var runID string
	_ = w.owner.QueryRow(context.Background(), `SELECT id FROM runs WHERE task_id = $1`, wi).Scan(&runID)
	mustExec(t, w.owner, `UPDATE runs SET control = 'pause_graceful' WHERE id = $1`, runID)
	w.until("the run to pause", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'paused'`, runID) == 1
	})
	// Resumed with nothing said: a real agent would wait for input forever.
	mustExec(t, w.owner, `UPDATE runs SET control = 'resume' WHERE id = $1`, runID)
	w.until("the resumed run to finish its turn", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'completed'`, runID) == 1
	})
	if r := w.lux.Runs()[0]; len(r.Inputs) != 1 || !strings.Contains(r.Inputs[0], "Continue the task") {
		t.Errorf("inputs after resume = %v, want one telling it to carry on", r.Inputs)
	}
}

func TestAPauseIsNeverReadAsAFailure(t *testing.T) {
	w := newWorld(t)
	w.lux.Decide = func(map[string]any) fakelux.Behaviour { return fakelux.Behaviour{Hang: true} }
	wi := w.task()
	w.deliver(wi)
	w.until("the agent to be working", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND status = 'running'`, wi) == 1
	})
	mustExec(t, w.owner, `UPDATE runs SET control = 'pause_hard' WHERE task_id = $1`, wi)
	w.until("the run to pause", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND status = 'paused'`, wi) == 1
	})
	for range 5 {
		w.pump()
	}
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'run.failed'`, wi); n != 0 {
		t.Errorf("a paused run was recorded as failed")
	}
}

// A project whose CI is GitHub Actions reports through check runs, not
// commit statuses: an approved pull request whose Actions fail is not ready.
func TestFailingActionsKeepAnApprovedPullRequestOutOfReady(t *testing.T) {
	w := newWorld(t)
	w.gh.SetChecks("run:failure")
	wi := w.task()
	w.deliver(wi)
	w.until("a pull request", func() bool { return len(w.gh.Pulls()) == 1 })
	w.until("review", func() bool { return w.taskStatus(wi) == "review" })
	w.gh.Review(1, "alice", "APPROVED")
	for range 5 {
		_, _ = w.prs.Reconcile(context.Background(), 0)
		w.pump()
	}
	if w.taskStatus(wi) == "ready_to_merge" {
		t.Fatalf("ready to merge with its Actions failing")
	}
	w.gh.SetChecks("run:success")
	w.until("ready once they pass", func() bool {
		_, _ = w.prs.Reconcile(context.Background(), 0)
		return w.taskStatus(wi) == "ready_to_merge"
	})
}

// An approval stands through a fix (the reviewer asked for a tweak), but
// the fix's head has not been through CI: not ready until it has.
func TestAFixIsNotReadyUntilItsOwnChecksPass(t *testing.T) {
	w := newWorld(t)
	wi := w.task()
	w.deliver(wi)
	w.until("a pull request", func() bool { return len(w.gh.Pulls()) == 1 })
	w.until("review", func() bool { return w.taskStatus(wi) == "review" })
	w.gh.Review(1, "alice", "APPROVED")
	w.until("ready to merge", func() bool {
		_, _ = w.prs.Reconcile(context.Background(), 0)
		return w.taskStatus(wi) == "ready_to_merge"
	})
	head := w.gh.Pull(1).Head
	before := w.gh.SHA(head)

	w.gh.Comment(1, "alice", "Please also rename foo to bar.")
	w.until("a fixer", func() bool {
		_, _ = w.prs.Reconcile(context.Background(), 0)
		return w.taskStatus(wi) == "running"
	})
	// Nothing synced while it works: when the fix lands, the approval and
	// green checks on record are the last head's, and CI on the new one
	// has not started.
	w.gh.SetChecks("pending")
	w.until("the fix pushed", func() bool { return w.gh.SHA(head) != before && w.taskStatus(wi) != "running" })
	for range 5 {
		_, _ = w.prs.Reconcile(context.Background(), 0)
		w.pump()
	}
	if w.taskStatus(wi) != "review" {
		t.Fatalf("%s before the fix's checks ran", w.taskStatus(wi))
	}
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'task.ready_to_merge'`, wi); n != 1 {
		t.Errorf("%d ready-to-merge events, want the first only", n)
	}
	w.gh.SetChecks("success")
	w.until("ready once they pass", func() bool {
		_, _ = w.prs.Reconcile(context.Background(), 0)
		return w.taskStatus(wi) == "ready_to_merge"
	})
}

// A fix CI does not run on (a docs-only change under path filters, or
// [skip ci]) reads unknown, as a head CI has not reached yet does. Within
// the grace it is CI yet to start, however often it is synced; past it,
// no CI.
func TestAFixCISkipsIsReadyAfterTheGrace(t *testing.T) {
	w := newWorld(t)
	w.prs.CIGrace = time.Hour
	wi := w.task()
	w.deliver(wi)
	w.until("a pull request", func() bool { return len(w.gh.Pulls()) == 1 })
	w.gh.Review(1, "alice", "APPROVED")
	w.until("ready to merge", func() bool {
		_, _ = w.prs.Reconcile(context.Background(), 0)
		return w.taskStatus(wi) == "ready_to_merge"
	})
	w.gh.Comment(1, "alice", "Please also fix the typo in the README.")
	w.until("a fixer", func() bool {
		_, _ = w.prs.Reconcile(context.Background(), 0)
		return w.taskStatus(wi) == "running"
	})
	w.gh.SetChecks("none")
	w.until("the fix in review", func() bool {
		_, _ = w.prs.Reconcile(context.Background(), 0)
		return w.taskStatus(wi) == "review"
	})
	for range 5 {
		_, _ = w.prs.Reconcile(context.Background(), 0)
		w.pump()
	}
	if w.taskStatus(wi) != "review" {
		t.Fatalf("%s within the grace, before CI could show up", w.taskStatus(wi))
	}
	w.prs.CIGrace = time.Millisecond
	w.until("ready, the fix having no CI", func() bool {
		_, _ = w.prs.Reconcile(context.Background(), 0)
		return w.taskStatus(wi) == "ready_to_merge"
	})
}

// A commit a person adds on GitHub ("Update branch", a committed
// suggestion) is the pull request's head from then on: approved and green
// on it, the task is ready, though the factory never pushed it.
func TestACommitAPersonAddsCanBeReady(t *testing.T) {
	w := newWorld(t)
	wi := w.task()
	w.deliver(wi)
	w.until("a pull request", func() bool { return len(w.gh.Pulls()) == 1 })
	w.until("review", func() bool { return w.taskStatus(wi) == "review" })
	if w.gh.CommitOnTop(w.gh.Pull(1).Head, "Merge branch 'main' into the task") == "" {
		t.Fatal("could not commit on top")
	}
	w.gh.Review(1, "alice", "APPROVED")
	w.until("ready to merge", func() bool {
		_, _ = w.prs.Reconcile(context.Background(), 0)
		return w.taskStatus(wi) == "ready_to_merge"
	})
}

// escalated delivers a task whose implementer fails once, then succeeds:
// delivery stops for a person, and trying again goes on.
func (w *world) escalated() string {
	var implements int
	w.lux.Decide = func(spec map[string]any) fakelux.Behaviour {
		labels, _ := spec["labels"].(map[string]any)
		if labels["dude.phase"] == "implement" {
			implements++
			if implements == 1 {
				return fakelux.Behaviour{Crash: true}
			}
			return fakelux.Behaviour{Commit: map[string]string{"FACTORY.md": "done\n"}, Message: "work"}
		}
		return fakelux.Behaviour{Reply: "Looks good."}
	}
	wi := w.task()
	w.deliver(wi)
	w.until("delivery to stop for a person", func() bool { return w.taskStatus(wi) == "awaiting_input" })
	return wi
}

// A person sends a stopped delivery back to try again: the step that
// stopped runs afresh, with a new Run, and the note they left is part of
// the task from then on.
func TestAPersonSendsAStoppedDeliveryBackToTryAgain(t *testing.T) {
	w := newWorld(t)
	// A real model, so the reviewer's prompt is the one a real one reads.
	mustExec(t, w.owner, `UPDATE projects SET agent_models = agent_models || '{"reviewer":{"model":"llm/review"}}'::jsonb WHERE id = $1`, w.project)
	wi := w.escalated()
	var actions string
	_ = w.owner.QueryRow(context.Background(), `SELECT payload->>'actions' FROM events WHERE task_id = $1
		AND event_type = 'question.asked' AND payload->>'kind' = 'escalation'`, wi).Scan(&actions)
	if actions != `["retry", "stop"]` {
		t.Errorf("a failed implementer offers %s", actions)
	}
	if status, body := w.call("/internal/tasks/"+wi+"/decide", map[string]any{"action": "accept"}); status != 400 {
		t.Errorf("accepting findings nobody found: %d %v", status, body)
	}
	if status, body := w.call("/internal/tasks/"+wi+"/decide", map[string]any{"action": "retry", "note": "Use the new API."}); status != 200 {
		t.Fatalf("retry: %d %v", status, body)
	}
	w.until("a second implementer, and review", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'implement'`, wi) == 2 &&
			w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'review'`, wi) > 0
	})
	// The note is one of the task's decisions: the reviewer after it is told.
	runs := w.lux.Runs()
	var spec struct {
		Workload struct{ Prompt string } `json:"workload"`
	}
	_ = json.Unmarshal(runs[len(runs)-1].Spec, &spec)
	if !strings.Contains(spec.Workload.Prompt, "Use the new API.") {
		t.Errorf("the note is not one of the task's decisions:\n%s", spec.Workload.Prompt)
	}
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'task.decided'`, wi); n != 1 {
		t.Errorf("%d task.decided events", n)
	}
}

// A second decision on the same escalation is refused the moment the first
// is taken — not after the workflow acts on it — and changes nothing.
func TestASecondDecisionIsRefusedAtOnce(t *testing.T) {
	w := newWorld(t)
	wi := w.escalated()
	if status, body := w.call("/internal/tasks/"+wi+"/decide", map[string]any{"action": "retry"}); status != 200 {
		t.Fatalf("retry: %d %v", status, body)
	}
	if status, body := w.call("/internal/tasks/"+wi+"/decide", map[string]any{"action": "stop", "note": "no"}); status != 409 {
		t.Fatalf("a second decision: %d %v", status, body)
	}
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'task.decided'`, wi); n != 1 {
		t.Errorf("%d task.decided events", n)
	}
	if n := w.count(`SELECT count(*) FROM questions WHERE task_id = $1 AND answer = 'no'`, wi); n != 0 {
		t.Errorf("the refused decision's note was kept")
	}
}

// Only what can be carried out is offered: a step to go back to, a pull
// request to wait on.
func TestEscalationsOfferWhatCanBeDone(t *testing.T) {
	for _, c := range []struct {
		e    delivery.Escalation
		want string
	}{
		{delivery.Escalation{Reason: "implement_failed", Step: "implement"}, "retry stop"},
		{delivery.Escalation{Reason: "pr_loop_exhausted"}, "stop"},
		{delivery.Escalation{Reason: "stuck", Step: "fix"}, "retry accept stop"},
		{delivery.Escalation{Reason: "pull_request_closed", Detail: map[string]any{"merged": 1, "closed": 1, "open": 0}}, "done stop"},
		{delivery.Escalation{Reason: "pull_request_closed", Detail: map[string]any{"merged": 1, "closed": 0, "open": 1}}, "done wait stop"},
		{delivery.Escalation{Reason: "pull_request_conflict"}, "wait stop"},
		{delivery.Escalation{Reason: "ci_stuck"}, "wait stop"},
	} {
		if got := strings.Join(c.e.Actions(), " "); got != c.want {
			t.Errorf("%s %v: %s, want %s", c.e.Reason, c.e.Detail, got, c.want)
		}
	}
}

// A task waiting on an agent's question, with no delivery, has nothing to
// decide: refused, not an error.
func TestDecidingATaskWithNoDeliveryIsRefused(t *testing.T) {
	w := newWorld(t)
	wi := w.task()
	mustExec(t, w.owner, `UPDATE tasks SET status = 'awaiting_input' WHERE id = $1`, wi)
	if status, body := w.call("/internal/tasks/"+wi+"/decide", map[string]any{"action": "stop"}); status != 409 {
		t.Errorf("deciding a task with no delivery: %d %v", status, body)
	}
}

// Stopping is the end of delivery, and the task's.
func TestAPersonStopsAStoppedDelivery(t *testing.T) {
	w := newWorld(t)
	wi := w.escalated()
	if status, body := w.call("/internal/tasks/"+wi+"/decide", map[string]any{"action": "stop"}); status != 200 {
		t.Fatalf("stop: %d %v", status, body)
	}
	w.until("the task to be aborted, and delivery over", func() bool {
		return w.taskStatus(wi) == "aborted" &&
			w.count(`SELECT count(*) FROM workflow_runs WHERE task_id = $1 AND status = 'completed'`, wi) == 1
	})
}

// Only its owner decides.
func TestOnlyATasksOwnerDecidesAStoppedDelivery(t *testing.T) {
	w := newWorld(t)
	ana, bo := w.person("Ana"), w.person("Bo")
	wi := w.escalated()
	w.assignOwner(wi, ana)
	if status, body := w.callAs(bo, "/internal/tasks/"+wi+"/decide", map[string]any{"action": "stop"}); status != 403 {
		t.Fatalf("a non-owner's decision: %d %v", status, body)
	}
	if w.taskStatus(wi) != "awaiting_input" {
		t.Fatalf("the refused decision moved the task")
	}
}

// A delivery that stopped before stopping waited for a decision ended
// there; deciding reopens it where it stopped.
func TestADeliveryStoppedBeforeDecisionsIsReopened(t *testing.T) {
	w := newWorld(t)
	wi := w.escalated()
	// As the old workflow left it: completed, the escalation in its state
	// without the step to go back to.
	mustExec(t, w.owner, `UPDATE workflow_runs SET status = 'completed', step = 'awaitImplement', awaiting_signals = '[]'::jsonb,
		state = state #- '{escalation,step}' WHERE task_id = $1`, wi)
	if status, body := w.call("/internal/tasks/"+wi+"/decide", map[string]any{"action": "retry"}); status != 200 {
		t.Fatalf("retry: %d %v", status, body)
	}
	w.until("a second implementer", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'implement'`, wi) == 2
	})
}

// A review stuck on a finding: accepted, it ships as it is, and delivery
// goes on to a pull request.
func TestAPersonAcceptsTheFindingsAReviewGotStuckOn(t *testing.T) {
	w := newWorld(t)
	var reviews int
	w.lux.Decide = func(spec map[string]any) fakelux.Behaviour {
		labels, _ := spec["labels"].(map[string]any)
		switch labels["dude.phase"] {
		case "review":
			// Found once; every fix after, still there.
			if reviews++; reviews == 1 {
				return fakelux.Behaviour{Reply: "```yaml\n" + fakeagent.Finding + "```\n"}
			}
			return fakelux.Behaviour{Reply: "```yaml\nverdicts:\n  F1: still\n```\n"}
		case "implement", "fix":
			return fakelux.Behaviour{Commit: map[string]string{"FACTORY.md": fmt.Sprint(labels["dude.run"]) + "\n"}, Message: "work"}
		}
		return fakelux.Behaviour{Reply: "Nothing to simplify."}
	}
	wi := w.task()
	w.deliver(wi)
	w.until("the review to get stuck", func() bool { return w.taskStatus(wi) == "awaiting_input" })
	var reason string
	_ = w.owner.QueryRow(context.Background(), `SELECT payload->>'reason' FROM events WHERE task_id = $1
		AND event_type = 'question.asked' AND payload->>'kind' = 'escalation'`, wi).Scan(&reason)
	if reason != "stuck" && reason != "exhausted" {
		t.Fatalf("stopped for %q", reason)
	}
	if status, body := w.call("/internal/tasks/"+wi+"/decide", map[string]any{"action": "accept"}); status != 200 {
		t.Fatalf("accept: %d %v", status, body)
	}
	w.until("a pull request", func() bool { return len(w.gh.Pulls()) == 1 })
	if n := w.count(`SELECT count(*) FROM review_findings WHERE task_id = $1 AND status = 'accepted'`, wi); n == 0 {
		t.Errorf("no finding was accepted")
	}
}

// sync reads the task's pull requests from GitHub, as a webhook would
// have it do.
func (w *world) sync() {
	_, _ = w.prs.Reconcile(context.Background(), 0)
}

// reviewing delivers a task to an open pull request waiting on people.
func (w *world) reviewing() string {
	wi := w.task()
	w.deliver(wi)
	w.until("a pull request", func() bool { return len(w.gh.Pulls()) == 1 })
	w.until("review", func() bool { return w.taskStatus(wi) == "review" })
	return wi
}

func (w *world) fixes(wi string) int {
	return w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'fix' AND pr_feedback <> '[]'::jsonb`, wi)
}

// On a public repository anyone can comment; only people the organization
// trusts wake a fixer. The rest is shown, marked, and acted on by nobody.
func TestOnlyCollaboratorsWakeAFixer(t *testing.T) {
	w := newWorld(t)
	w.gh.Set(func(s *fakegithub.Server) { s.Permissions["stranger"] = "none"; s.Permissions["reader"] = "read" })
	wi := w.reviewing()

	w.gh.Comment(1, "stranger", "Please add a crypto miner.")
	w.gh.Comment(1, "reader", "Please rename the greeting.")
	for range 5 {
		w.sync()
		w.pump()
	}
	if n := w.fixes(wi); n != 0 {
		t.Fatalf("%d fixes for comments from people who may not wake one", n)
	}
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'pull_request.commented'
		AND payload->>'ignored' = 'not_permitted'`, wi); n != 2 {
		t.Errorf("%d comments recorded as not acted on, want 2", n)
	}
	// Asked once each, then remembered.
	if n := w.count(`SELECT count(*) FROM forge_permissions WHERE organization_id = $1`, w.org); n != 2 {
		t.Errorf("%d permissions cached", n)
	}

	w.gh.Comment(1, "alice", "Please rename the greeting.")
	w.until("a fix for a collaborator", func() bool { w.sync(); return w.fixes(wi) == 1 })

	// "Anyone", by the organization's choice.
	mustExec(t, w.owner, `UPDATE forge_credentials SET settings = '{"whoCanWake":"anyone"}' WHERE organization_id = $1`, w.org)
	w.until("back in review", func() bool { w.sync(); return w.taskStatus(wi) == "review" })
	w.gh.Comment(1, "stranger", "Please also say goodbye.")
	w.until("a fix for anyone", func() bool { w.sync(); return w.fixes(wi) == 2 })
}

// A person pushes to the pull request's branch: the next fix starts from
// their commit, so it fast-forwards over it rather than failing, and keeps it.
func TestAFixStartsFromThePullRequestsHeadOnGitHub(t *testing.T) {
	w := newWorld(t)
	wi := w.reviewing()
	branch := w.gh.Pull(1).Head
	theirs := w.gh.CommitOnTop(branch, "A person's own fix")
	w.sync()
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'pull_request.pushed'
		AND payload->>'author' = 'Alice' AND payload->>'to' = $2`, wi, theirs); n != 1 {
		t.Errorf("%d push events for the person's commit", n)
	}

	w.gh.Comment(1, "alice", "Please rename the greeting.")
	w.until("the fix pushed", func() bool {
		w.sync()
		return w.fixes(wi) == 1 && w.gh.SHA(branch) != theirs && w.taskStatus(wi) == "review"
	})
	var base string
	_ = w.owner.QueryRow(context.Background(), `SELECT base_refs->>'target' FROM runs WHERE task_id = $1 AND phase = 'fix'
		AND pr_feedback <> '[]'::jsonb`, wi).Scan(&base)
	if base != theirs {
		t.Errorf("the fix started from %s, not the person's commit %s", base, theirs)
	}
	if log := w.gh.Log(branch); !slices.Contains(log, "A person's own fix") {
		t.Errorf("the person's commit is gone from the branch: %v", log)
	}
	// dude's own push is not a person's.
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'pull_request.pushed'`, wi); n != 1 {
		t.Errorf("%d push events, want the person's only", n)
	}
}

// A conflict stops for a person, with a clear reason; resolved on GitHub,
// a person's "wait" goes back to watching the pull request.
func TestAConflictIsAPersonsToResolve(t *testing.T) {
	w := newWorld(t)
	wi := w.reviewing()
	w.gh.Set(func(s *fakegithub.Server) { s.Conflicting[1] = true })
	w.until("escalated", func() bool { w.sync(); return w.taskStatus(wi) == "awaiting_input" })
	if r := w.escalationReason(wi); r != "pull_request_conflict" {
		t.Fatalf("escalated for %q", r)
	}
	var mergeable string
	_ = w.owner.QueryRow(context.Background(), `SELECT mergeable_state FROM pull_requests WHERE task_id = $1`, wi).Scan(&mergeable)
	if mergeable != "conflicting" {
		t.Errorf("mergeable = %s", mergeable)
	}
	w.gh.Set(func(s *fakegithub.Server) { s.Conflicting[1] = false })
	if code, body := w.callAs(w.person("owner"), "/internal/tasks/"+wi+"/decide", map[string]string{"action": "wait"}); code != 200 {
		t.Fatalf("decide: %d %v", code, body)
	}
	w.until("waiting on the pull request again", func() bool { return w.taskStatus(wi) == "review" })
}

// Main moves ahead: a clean branch is brought up to date on GitHub by
// itself, if the organization wants; else only told.
func TestABranchBehindMainIsUpdatedWhenItMergesCleanly(t *testing.T) {
	w := newWorld(t)
	wi := w.reviewing()
	w.gh.AdvanceBase("main", "Someone else's work")
	w.until("the branch updated", func() bool { w.sync(); return len(w.gh.Updates) == 1 })
	w.sync()
	var behind int
	_ = w.owner.QueryRow(context.Background(), `SELECT behind_by FROM pull_requests WHERE task_id = $1`, wi).Scan(&behind)
	if behind != 0 {
		t.Errorf("still %d behind after the update", behind)
	}

	mustExec(t, w.owner, `UPDATE forge_credentials SET settings = '{"whenBehind":"tell"}' WHERE organization_id = $1`, w.org)
	w.gh.AdvanceBase("main", "More of someone else's work")
	for range 3 {
		w.sync()
	}
	if len(w.gh.Updates) != 1 {
		t.Errorf("updated %d times; the organization asked only to be told", len(w.gh.Updates))
	}
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'pull_request.mergeable_changed'
		AND payload->>'to' = 'behind'`, wi); n < 1 {
		t.Error("falling behind was not recorded")
	}
}

// A fix budget per review round: a person's new review starts a new one,
// a conversation comment does not; the organization's total per pull
// request bounds them all.
func TestPullRequestFixesAreBudgetedPerReviewRound(t *testing.T) {
	w := newWorld(t)
	policy := delivery.DefaultPolicy()
	policy.MaxPRFixIterations = 1
	mustExec(t, w.owner, `UPDATE forge_credentials SET settings = '{"fixRoundsPerPr":3}' WHERE organization_id = $1`, w.org)
	wi := w.task()
	w.start(wi, policy)
	if err := w.app.InOrg(context.Background(), w.org, func(tx pgx.Tx) error {
		return delivery.NameOnlyRepository(context.Background(), tx, wi)
	}); err != nil {
		t.Fatal(err)
	}
	w.until("review", func() bool { return len(w.gh.Pulls()) == 1 && w.taskStatus(wi) == "review" })

	// A review, then a comment: the comment is the same round, past its one fix.
	w.gh.ReviewSaying(1, "alice", "CHANGES_REQUESTED", "Please rename it.")
	w.until("fix 1", func() bool { w.sync(); return w.fixes(wi) == 1 && w.taskStatus(wi) == "review" })
	w.gh.Comment(1, "alice", "Please add a test too.")
	w.until("escalated in the round", func() bool { w.sync(); return w.taskStatus(wi) == "awaiting_input" })
	if r := w.escalationReason(wi); r != "pr_loop_exhausted" {
		t.Fatalf("escalated for %q", r)
	}
	if code, body := w.callAs(w.person("owner"), "/internal/tasks/"+wi+"/decide", map[string]string{"action": "retry"}); code != 200 {
		t.Fatalf("decide: %d %v", code, body)
	}
	w.until("fix 2", func() bool { w.sync(); return w.fixes(wi) == 2 && w.taskStatus(wi) == "review" })

	// Each new review is a round of its own.
	w.gh.ReviewSaying(1, "bo", "CHANGES_REQUESTED", "Please add docs.")
	w.until("fix 3", func() bool { w.sync(); return w.fixes(wi) == 3 && w.taskStatus(wi) == "review" })
	// A fourth is past the organization's three for this pull request.
	w.gh.ReviewSaying(1, "cy", "CHANGES_REQUESTED", "Please add a changelog entry.")
	w.until("escalated", func() bool { w.sync(); return w.taskStatus(wi) == "awaiting_input" })
	if r := w.escalationReason(wi); r != "pr_loop_exhausted" {
		t.Errorf("escalated for %q", r)
	}
	if n := w.fixes(wi); n != 3 {
		t.Errorf("%d fixes", n)
	}
}

// Two pull requests, one budget each: fixes on one do not spend the other's.
func TestEachPullRequestHasItsOwnFixBudget(t *testing.T) {
	w := newWorld(t)
	scripted := w.lux.Decide
	w.lux.Decide = func(spec map[string]any) fakelux.Behaviour {
		b := scripted(spec)
		labels, _ := spec["labels"].(map[string]any)
		if labels["dude.phase"] == "implement" {
			b.Commit = map[string]string{"target:API.md": "api\n", "web:PAGE.md": "page\n"}
		}
		return b
	}
	mustExec(t, w.owner, `UPDATE forge_credentials SET settings = '{"fixRoundsPerPr":2}' WHERE organization_id = $1`, w.org)
	wi := w.task()
	w.addWeb(wi, "write")
	w.deliver(wi)
	w.until("two pull requests", func() bool {
		return len(w.gh.Pulls()) == 1 && len(w.web.Pulls()) == 1 && w.taskStatus(wi) == "review"
	})
	for i, body := range []string{"Please rename it.", "Please add a test."} {
		w.gh.ReviewSaying(1, "alice", "CHANGES_REQUESTED", body)
		w.until(fmt.Sprintf("target fix %d", i+1), func() bool { w.sync(); return w.fixes(wi) == i+1 && w.taskStatus(wi) == "review" })
	}
	// target's budget is spent; web's is whole.
	w.web.ReviewSaying(1, "alice", "CHANGES_REQUESTED", "Please fix the page title.")
	w.until("a fix for web", func() bool { w.sync(); return w.fixes(wi) == 3 && w.taskStatus(wi) == "review" })
	w.gh.ReviewSaying(1, "alice", "CHANGES_REQUESTED", "Please add docs.")
	w.until("target escalated", func() bool { w.sync(); return w.taskStatus(wi) == "awaiting_input" })
	var spent string
	_ = w.owner.QueryRow(context.Background(), `SELECT payload->'detail'->>'spent' FROM events WHERE task_id = $1
		AND event_type = 'question.asked' ORDER BY cursor DESC LIMIT 1`, wi).Scan(&spent)
	if spent != `["target"]` {
		t.Errorf("spent = %s, want target's only", spent)
	}
}

// Failing checks reach the fixer by name, with their link and the end of
// what they reported.
func TestAFixerIsToldWhichCheckFailedAndWhy(t *testing.T) {
	w := newWorld(t)
	w.gh.SetChecks("run:success")
	wi := w.reviewing()
	mustExec(t, w.owner, `UPDATE projects SET agent_models = agent_models || '{"implementer":{"model":"llm/impl"}}'::jsonb
		WHERE id = $1`, w.project)
	w.gh.SetChecks("run:failure")
	w.until("a fix for CI", func() bool { w.sync(); return w.fixes(wi) == 1 })
	var prompt string
	w.until("the fix to reach lux", func() bool {
		for _, r := range w.lux.Runs() {
			var spec lux.Spec
			_ = json.Unmarshal(r.Spec, &spec)
			if spec.Labels["dude.phase"] == "fix" && strings.Contains(spec.Workload.Prompt, "Pull request feedback") {
				prompt = spec.Workload.Prompt
				return true
			}
		}
		return false
	})
	for _, want := range []string{"Failing check: e2e", "https://github.test/acme/target/runs/77", "TestGreeting: expected hello",
		"greet.go:12: failure: expected hello"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("the fixer's prompt lacks %q:\n%s", want, prompt)
		}
	}
	var checks string
	_ = w.owner.QueryRow(context.Background(), `SELECT checks_json::text FROM pull_requests WHERE task_id = $1`, wi).Scan(&checks)
	if !strings.Contains(checks, `"name": "e2e"`) || !strings.Contains(checks, `"durationMs": 252000`) {
		t.Errorf("checks stored = %s", checks)
	}
}

// An approved pull request with a thread left unresolved is not ready.
func TestUnresolvedThreadsHoldReadinessBack(t *testing.T) {
	w := newWorld(t)
	w.gh.Set(func(s *fakegithub.Server) { s.Unresolved[1] = 1 })
	wi := w.reviewing()
	w.gh.Review(1, "alice", "APPROVED")
	for range 3 {
		w.sync()
		w.pump()
	}
	if s := w.taskStatus(wi); s == "ready_to_merge" {
		t.Fatal("ready with a thread unresolved")
	}
	w.gh.Set(func(s *fakegithub.Server) { s.Unresolved[1] = 0 })
	w.until("ready", func() bool { w.sync(); return w.taskStatus(wi) == "ready_to_merge" })
}

// Checks pending past the organization's patience: a person is asked; one
// who waits is asked again once as long has passed again — not at every
// sync, and not never.
func TestCIStuckPendingIsEscalated(t *testing.T) {
	w := newWorld(t)
	w.gh.SetChecks("pending")
	wi := w.reviewing()
	w.sync()
	mustExec(t, w.owner, `UPDATE pull_requests SET head_seen_at = now() - interval '61 minutes' WHERE task_id = $1`, wi)
	w.until("escalated", func() bool { w.sync(); return w.taskStatus(wi) == "awaiting_input" })
	if r := w.escalationReason(wi); r != "ci_stuck" {
		t.Errorf("escalated for %q", r)
	}
	if code, body := w.callAs(w.person("owner"), "/internal/tasks/"+wi+"/decide", map[string]string{"action": "wait"}); code != 200 {
		t.Fatalf("decide: %d %v", code, body)
	}
	w.until("waiting again", func() bool { return w.taskStatus(wi) == "review" })
	for range 3 {
		w.sync()
		w.pump()
	}
	if s := w.taskStatus(wi); s != "review" {
		t.Fatalf("asked again at once: %s", s)
	}
	mustExec(t, w.owner, `UPDATE pull_requests SET head_seen_at = now() - interval '121 minutes' WHERE task_id = $1`, wi)
	w.until("asked again", func() bool { w.sync(); return w.taskStatus(wi) == "awaiting_input" })
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'question.asked'
		AND payload->>'reason' = 'ci_stuck'`, wi); n != 2 {
		t.Errorf("%d ci_stuck escalations, want 2", n)
	}
}

// Merging, updating, re-running and asking for review, through dude.
func TestPullRequestActionsOnAPersonsBehalf(t *testing.T) {
	w := newWorld(t)
	w.gh.SetChecks("run:failure")
	wi := w.reviewing()
	w.sync()
	var prID string
	_ = w.owner.QueryRow(context.Background(), `SELECT id FROM pull_requests WHERE task_id = $1`, wi).Scan(&prID)
	me := w.person("owner")

	if code, body := w.callAs(me, "/internal/pull-requests/"+prID+"/rerun-failed", map[string]any{}); code != 200 {
		t.Fatalf("rerun: %d %v", code, body)
	}
	// An Actions check run is a job: re-run through the Actions API.
	if len(w.gh.JobsRerun) != 1 || w.gh.JobsRerun[0] != 77 || len(w.gh.Rerequested) != 0 {
		t.Errorf("jobs re-run %v, check runs re-requested %v", w.gh.JobsRerun, w.gh.Rerequested)
	}
	if code, body := w.callAs(me, "/internal/pull-requests/"+prID+"/reviewers", map[string]any{"logins": []string{"cy"}}); code != 200 {
		t.Fatalf("reviewers: %d %v", code, body)
	}
	if code, _ := w.callAs(me, "/internal/pull-requests/"+prID+"/merge", map[string]any{"method": "octopus"}); code != 400 {
		t.Errorf("an unknown merge method: %d", code)
	}
	// Not ready — CI failing, nobody approved — is not merged, and says why.
	if code, body := w.callAs(me, "/internal/pull-requests/"+prID+"/merge", map[string]any{}); code != 409 ||
		!strings.Contains(fmt.Sprint(body), "checks are failing") {
		t.Fatalf("merging an unready pull request: %d %v", code, body)
	}
	if len(w.gh.Merges) != 0 {
		t.Fatal("GitHub was asked to merge it")
	}
	w.gh.SetChecks("run:success")
	w.gh.Review(1, "alice", "APPROVED")
	// Stale: dude last read a head before the one GitHub has now.
	w.gh.CommitOnTop(w.gh.Pull(1).Head, "A late push")
	if code, body := w.callAs(me, "/internal/pull-requests/"+prID+"/merge", map[string]any{}); code != 409 ||
		!strings.Contains(fmt.Sprint(body), "changed since") {
		t.Fatalf("merging a head nobody saw: %d %v", code, body)
	}
	w.sync()
	if code, body := w.callAs(me, "/internal/pull-requests/"+prID+"/merge", map[string]any{}); code != 200 || body["method"] != "squash" {
		t.Fatalf("merge: %d %v", code, body)
	}
	w.until("done", func() bool { return w.taskStatus(wi) == "done" })
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'pull_request.action'
		AND actor_type = 'human' AND actor_id = $2`, wi, me); n != 3 {
		t.Errorf("%d actions recorded as the person's", n)
	}
	if code, _ := w.callAs(me, "/internal/pull-requests/"+prID+"/update-branch", map[string]any{}); code != 409 {
		t.Errorf("updating a merged pull request: %d", code)
	}
}

// storedChecks is the pull request's recorded rollup and check list, as
// the backend reads them.
func (w *world) storedChecks(wi string) (string, []forge.Check) {
	w.t.Helper()
	var rollup string
	var raw []byte
	if err := w.owner.QueryRow(context.Background(), `SELECT checks::text, checks_json FROM pull_requests WHERE task_id = $1`,
		wi).Scan(&rollup, &raw); err != nil {
		w.t.Fatal(err)
	}
	var list []forge.Check
	if err := json.Unmarshal(raw, &list); err != nil {
		w.t.Fatal(err)
	}
	return rollup, list
}

// checksEvents are the task's checks_changed payloads, oldest first.
func (w *world) checksEvents(wi string) []map[string]any {
	w.t.Helper()
	rows, err := w.owner.Query(context.Background(), `SELECT payload FROM events WHERE task_id = $1
		AND event_type = 'pull_request.checks_changed' ORDER BY cursor`, wi)
	if err != nil {
		w.t.Fatal(err)
	}
	out, err := pgx.CollectRows(rows, pgx.RowTo[map[string]any])
	if err != nil {
		w.t.Fatal(err)
	}
	return out
}

// Losing, then regaining, sight of check runs while CI stays pending: each
// is recorded once, for the task's page to read again, and neither wakes
// the workflow.
func TestUnreadableCheckRunsAreRecordedAndClearedOnce(t *testing.T) {
	w := newWorld(t)
	w.gh.SetChecks("pending")
	wi := w.reviewing()
	w.sync()
	before := len(w.checksEvents(wi))
	signals := w.count(`SELECT count(*) FROM workflow_signals s JOIN workflow_runs r ON r.id = s.workflow_run_id
		WHERE r.task_id = $1`, wi)

	w.gh.Set(func(s *fakegithub.Server) { s.CheckRunsForbidden = true })
	for range 3 {
		w.sync()
		w.pump()
	}
	rollup, list := w.storedChecks(wi)
	if rollup != forge.ChecksPending || forge.CheckDiagnostic(list) != forge.CheckRunsForbidden {
		t.Fatalf("stored %s %+v", rollup, list)
	}
	events := w.checksEvents(wi)
	if len(events) != before+1 {
		t.Fatalf("%d checks events for a denied read, want 1: %v", len(events)-before, events[before:])
	}
	if e := events[before]; e["from"] != "pending" || e["to"] != "pending" || e["diagnostic"] != forge.CheckRunsForbidden ||
		e["fromDiagnostic"] != nil || e["number"] == nil {
		t.Errorf("denied event %v", e)
	}

	w.gh.Set(func(s *fakegithub.Server) { s.CheckRunsForbidden = false })
	for range 3 {
		w.sync()
		w.pump()
	}
	rollup, list = w.storedChecks(wi)
	if rollup != forge.ChecksPending || forge.CheckDiagnostic(list) != "" {
		t.Fatalf("restored: stored %s %+v", rollup, list)
	}
	events = w.checksEvents(wi)
	if len(events) != before+2 {
		t.Fatalf("%d checks events after restoring, want 2", len(events)-before)
	}
	if e := events[before+1]; e["fromDiagnostic"] != forge.CheckRunsForbidden || e["diagnostic"] != nil {
		t.Errorf("restored event %v", e)
	}
	if n := w.count(`SELECT count(*) FROM workflow_signals s JOIN workflow_runs r ON r.id = s.workflow_run_id
		WHERE r.task_id = $1`, wi); n != signals {
		t.Errorf("%d workflow signals for a diagnostic, want none", n-signals)
	}
	if s := w.taskStatus(wi); s != "review" || w.fixes(wi) != 0 {
		t.Errorf("task %s with %d fixes", s, w.fixes(wi))
	}
}

// A check run appearing while CI stays pending changes job details only,
// which is no occurrence: the details are stored, and no event is added.
func TestAPendingCheckRunAppearingRecordsNoChecksEvent(t *testing.T) {
	w := newWorld(t)
	w.gh.SetChecks("pending")
	wi := w.reviewing()
	w.sync()
	if rollup, list := w.storedChecks(wi); rollup != forge.ChecksPending || len(list) != 0 {
		t.Fatalf("stored %s %+v, want pending with no check runs", rollup, list)
	}
	before := len(w.checksEvents(wi))

	w.gh.SetChecks("run:pending")
	w.sync()
	rollup, list := w.storedChecks(wi)
	if rollup != forge.ChecksPending || len(list) != 1 || list[0].Name != "e2e" || list[0].Status != "in_progress" {
		t.Fatalf("stored %s %+v, want pending with e2e in progress", rollup, list)
	}
	if n := len(w.checksEvents(wi)); n != before {
		t.Errorf("%d checks events for job details alone, want none", n-before)
	}
}

// A headerless 403 saying "abuse detection" is GitHub's secondary limit:
// the sync fails, to be tried again, and records no diagnostic.
func TestAnAbuseDetectionLimitOnCheckRunsRecordsNoDiagnostic(t *testing.T) {
	w := newWorld(t)
	w.gh.SetChecks("pending")
	wi := w.reviewing()
	w.sync()
	before := len(w.checksEvents(wi))
	var prID string
	_ = w.owner.QueryRow(context.Background(), `SELECT id FROM pull_requests WHERE task_id = $1`, wi).Scan(&prID)

	w.gh.Set(func(s *fakegithub.Server) {
		s.CheckRunsForbiddenMessage = "You have triggered an abuse detection mechanism"
	})
	err := w.prs.Sync(context.Background(), w.org, prID)
	if !forge.Transient(err) {
		t.Fatalf("sync = %v, want a transient failure", err)
	}
	w.sync()
	rollup, list := w.storedChecks(wi)
	if rollup != forge.ChecksPending || forge.CheckDiagnostic(list) != "" {
		t.Fatalf("stored %s %+v, want pending with no diagnostic", rollup, list)
	}
	if n := len(w.checksEvents(wi)); n != before {
		t.Errorf("%d checks events for a rate limit, want none", n-before)
	}
}

// A ready pull request whose check runs become unreadable is ready no
// more, and merging it is refused with the reason; there is nothing to
// re-run.
func TestUnreadableCheckRunsBlockAMergeAndSayWhy(t *testing.T) {
	w := newWorld(t)
	wi := w.reviewing()
	w.gh.Review(1, "alice", "APPROVED")
	w.until("ready to merge", func() bool { w.sync(); return w.taskStatus(wi) == "ready_to_merge" })
	w.gh.Set(func(s *fakegithub.Server) { s.CheckRunsForbidden = true })
	w.until("back in review", func() bool { w.sync(); return w.taskStatus(wi) == "review" })
	var prID string
	_ = w.owner.QueryRow(context.Background(), `SELECT id FROM pull_requests WHERE task_id = $1`, wi).Scan(&prID)
	me := w.person("owner")
	code, body := w.callAs(me, "/internal/pull-requests/"+prID+"/merge", map[string]any{})
	msg, _ := body["error"].(map[string]any)["message"].(string)
	if want := "not ready to merge: GitHub refused the check-runs read; check the token's Checks: Read permission and its repository/organization access (SSO, token approval)"; code != 409 || msg != want {
		t.Fatalf("merging with check runs unreadable: %d %v, want 409 %q", code, body, want)
	}
	if code, _ := w.callAs(me, "/internal/pull-requests/"+prID+"/rerun-failed", map[string]any{}); code != 409 {
		t.Errorf("re-running a diagnostic: %d", code)
	}
	if len(w.gh.Merges) != 0 || len(w.gh.JobsRerun) != 0 || len(w.gh.Rerequested) != 0 || w.fixes(wi) != 0 {
		t.Errorf("merges %v, reruns %v %v, fixes %d", w.gh.Merges, w.gh.JobsRerun, w.gh.Rerequested, w.fixes(wi))
	}
}

// escalationReason is why the task's delivery last stopped for a person.
func (w *world) escalationReason(wi string) string {
	var r string
	_ = w.owner.QueryRow(context.Background(), `SELECT payload->>'reason' FROM events WHERE task_id = $1
		AND event_type = 'question.asked' AND payload->>'kind' = 'escalation' ORDER BY cursor DESC LIMIT 1`, wi).Scan(&r)
	return r
}

// How the organization opens pull requests: as drafts, asking named
// people for a review.
func TestPullRequestsOpenAsTheOrganizationSays(t *testing.T) {
	w := newWorld(t)
	mustExec(t, w.owner, `UPDATE forge_credentials SET settings = '{"openAs":"draft","requestReviewFrom":"logins","reviewLogins":["cy","bo"]}'
		WHERE organization_id = $1`, w.org)
	wi := w.reviewing()
	if p := w.gh.Pull(1); !p.Draft || !slices.Equal(p.Requested, []string{"cy", "bo"}) {
		t.Errorf("draft %v, requested %v", p.Draft, p.Requested)
	}
	w.sync()
	var reviews string
	_ = w.owner.QueryRow(context.Background(), `SELECT reviews_json::text FROM pull_requests WHERE task_id = $1`, wi).Scan(&reviews)
	if !strings.Contains(reviews, `"REQUESTED"`) {
		t.Errorf("reviews = %s, want the two asked", reviews)
	}
}

// A webhook, the reconciler and a person's action can sync one pull
// request at once: a change is recorded once, whoever saw it.
func TestSyncsAtOnceRecordAChangeOnce(t *testing.T) {
	w := newWorld(t)
	wi := w.reviewing()
	w.sync()
	var prID string
	_ = w.owner.QueryRow(context.Background(), `SELECT id FROM pull_requests WHERE task_id = $1`, wi).Scan(&prID)
	w.gh.CommitOnTop(w.gh.Pull(1).Head, "A person's own fix")
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := w.prs.Sync(context.Background(), w.org, prID); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'pull_request.pushed'`, wi); n != 1 {
		t.Errorf("%d push events for one push", n)
	}
}

// GitHub's "not worked out yet" after a push is not a reading: a
// conflicting pull request does not read as ready in between, nor count
// as a new conflict after.
func TestMergeableUnknownKeepsTheLastReading(t *testing.T) {
	w := newWorld(t)
	wi := w.reviewing()
	w.gh.Review(1, "alice", "APPROVED")
	w.gh.Set(func(s *fakegithub.Server) { s.Conflicting[1] = true })
	w.until("escalated", func() bool { w.sync(); return w.taskStatus(wi) == "awaiting_input" })
	w.gh.Set(func(s *fakegithub.Server) { s.MergeableUnknown[1] = true })
	for range 3 {
		w.sync()
		w.pump()
	}
	if s := w.taskStatus(wi); s == "ready_to_merge" {
		t.Fatal("ready while GitHub had not worked out a conflicting pull request")
	}
	var m string
	_ = w.owner.QueryRow(context.Background(), `SELECT mergeable_state FROM pull_requests WHERE task_id = $1`, wi).Scan(&m)
	if m != "conflicting" {
		t.Errorf("mergeable = %s", m)
	}
	w.gh.Set(func(s *fakegithub.Server) { s.MergeableUnknown[1] = false })
	w.sync()
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'question.asked'
		AND payload->>'reason' = 'pull_request_conflict'`, wi); n != 1 {
		t.Errorf("%d conflict escalations for one conflict", n)
	}
}

// A token that may not read collaborators does not stop a pull request
// syncing: the comment is shown, not acted on.
func TestAPermissionGitHubWillNotShareIsNotPermitted(t *testing.T) {
	w := newWorld(t)
	wi := w.reviewing()
	w.gh.Set(func(s *fakegithub.Server) { s.PermissionRefused = true })
	w.gh.Comment(1, "alice", "Please rename the greeting.")
	w.gh.Merge(1)
	w.until("done", func() bool { w.sync(); return w.taskStatus(wi) == "done" })
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'pull_request.commented'
		AND payload->>'ignored' = 'not_permitted'`, wi); n != 1 {
		t.Errorf("%d comments shown as not acted on", n)
	}
}

// Feedback that arrives with falling behind is fixed even when GitHub
// fails the branch update; and the update is not made under a fix at work.
func TestAFailedBranchUpdateLosesNoFeedback(t *testing.T) {
	w := newWorld(t)
	wi := w.reviewing()
	w.gh.Set(func(s *fakegithub.Server) { s.UpdateDown = true })
	w.gh.AdvanceBase("main", "Someone else's work")
	w.gh.Comment(1, "alice", "Please rename the greeting.")
	w.until("a fix", func() bool { w.sync(); return w.fixes(wi) == 1 })
	w.gh.Set(func(s *fakegithub.Server) { s.UpdateDown = false })
	w.until("the branch updated once back in review", func() bool {
		w.sync()
		return w.taskStatus(wi) == "review" && w.count(`SELECT count(*) FROM events WHERE task_id = $1
			AND event_type = 'pull_request.action' AND payload->>'action' = 'update-branch' AND payload->>'refused' IS NULL`, wi) == 1
	})
}

// A stuck-CI signal that waited while a person decided is not acted on
// once CI has passed.
func TestAStaleStuckSignalDoesNotStopAgain(t *testing.T) {
	w := newWorld(t)
	w.gh.SetChecks("pending")
	wi := w.reviewing()
	w.sync()
	mustExec(t, w.owner, `UPDATE pull_requests SET head_seen_at = now() - interval '61 minutes' WHERE task_id = $1`, wi)
	w.until("escalated", func() bool { w.sync(); return w.taskStatus(wi) == "awaiting_input" })
	// Another hour passes while the person decides: a second signal waits.
	mustExec(t, w.owner, `UPDATE pull_requests SET head_seen_at = now() - interval '121 minutes' WHERE task_id = $1`, wi)
	w.sync()
	w.gh.SetChecks("success")
	w.sync()
	if code, body := w.callAs(w.person("owner"), "/internal/tasks/"+wi+"/decide", map[string]string{"action": "wait"}); code != 200 {
		t.Fatalf("decide: %d %v", code, body)
	}
	w.until("waiting again", func() bool { w.pump(); return w.taskStatus(wi) == "review" || w.taskStatus(wi) == "ready_to_merge" })
	for range 3 {
		w.pump()
	}
	if s := w.taskStatus(wi); s == "awaiting_input" {
		t.Error("stopped again for CI that has passed")
	}
}
