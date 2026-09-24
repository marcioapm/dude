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
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

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
	t       *testing.T
	app     *db.DB
	owner   *pgx.Conn
	org     string
	project string
	repoID  string
	gh      *fakegithub.Server
	lux     *fakelux.Server
	runtime *workflow.Runtime
	syncer  *phases.Syncer
	prs     *prs.Syncer
	// The orchestrator's internal API, as the backend calls it.
	api string
}

func newWorld(t *testing.T) *world {
	t.Helper()
	app, owner := dbtest.Open(t)
	w := &world{t: t, app: app, owner: owner, org: dbtest.Org(t, owner)}
	ctx := context.Background()

	// A bare repository with one commit on main, as GitHub would hold it.
	dir := t.TempDir()
	bare := filepath.Join(dir, "target.git")
	seed := filepath.Join(dir, "seed")
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

	w.gh = fakegithub.New(bare, "acme/target")
	ghSrv := httptest.NewServer(w.gh.Handler())
	t.Cleanup(ghSrv.Close)

	// dude's scripted agent (internal/fakeagent), played by the fake lux.
	w.lux = fakelux.New(bare, "lux-key", nil)
	luxSrv := httptest.NewServer(w.lux.Handler())
	t.Cleanup(luxSrv.Close)

	w.project, w.repoID = "prj_"+w.org, "repo_"+w.org
	models := `{"implementer":{"model":"fake/scripted"},"reviewer":{"model":"fake/scripted"},"simplifier":{"model":"fake/scripted"}}`
	mustExec(t, owner, `INSERT INTO projects (id, organization_id, name, slug, agent_models, runtime_image)
		VALUES ($1, $2, 'P', $1, $3::jsonb, 'agent:test')`, w.project, w.org, models)
	mustExec(t, owner, `INSERT INTO repositories (id, organization_id, project_id, name, url, default_branch)
		VALUES ($1, $2, $3, 'target', 'https://github.com/acme/target.git', 'main')`, w.repoID, w.org, w.project)
	mustExec(t, owner, `INSERT INTO forge_credentials (id, organization_id, auth, secret, api_base_url)
		VALUES ($1, $2, 'pat', 'ghp_test', $3)`, "forge_"+w.org, w.org, ghSrv.URL)

	forges := forge.Resolver{DB: app}
	w.runtime = workflow.New(app, "test", quiet)
	w.runtime.Register(delivery.Workflow(&delivery.Store{DB: app}, forges))
	w.syncer = &phases.Syncer{DB: app, Lux: lux.New(luxSrv.URL, "lux-key"), Forges: forges, Log: quiet,
		Agent: phases.AgentConfig{DefaultImage: "default:img", OpenCodeAuth: `{"k":"secret-key"}`,
			OpenCodeProviders: json.RawMessage(`{"llm":{"options":{"baseURL":"https://llm.example/v1"}}}`), Timeout: "1h"}}
	t.Cleanup(w.syncer.Stop)
	w.prs = &prs.Syncer{DB: app, Forges: forges, Log: quiet,
		Signal: func(ctx context.Context, org, wf, name string, payload any, key string) error {
			return w.runtime.Signal(ctx, org, wf, name, payload, key)
		}}
	apiSrv := httptest.NewServer((&api.Server{DB: app, Workflow: w.runtime, Token: "svc", Log: quiet, Kick: func() {}}).Handler())
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

func mustExec(t *testing.T, c *pgx.Conn, sql string, args ...any) {
	t.Helper()
	if _, err := c.Exec(context.Background(), sql, args...); err != nil {
		t.Fatal(err)
	}
}

func (w *world) workItem() string {
	id := fmt.Sprintf("wi_%d", time.Now().UnixNano())
	mustExec(w.t, w.owner, `INSERT INTO work_items (id, organization_id, project_id, title, goal, acceptance_criteria)
		VALUES ($1, $2, $3, 'Greet people', 'Say hello', '["it greets"]'::jsonb)`, id, w.org, w.project)
	return id
}

func (w *world) deliver(workItemID string) string {
	id, _, err := w.runtime.Start(context.Background(), workflow.StartOptions{
		Type: delivery.WorkflowType, OrganizationID: w.org, IdempotencyKey: "delivery:" + workItemID, WorkItemID: workItemID,
		Input: delivery.State{WorkItemID: workItemID, ProjectID: w.project, RepositoryID: w.repoID,
			Policy: delivery.DefaultPolicy(), Branch: delivery.BranchFor(workItemID, 1)},
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

func (w *world) workItemStatus(id string) string {
	var s string
	_ = w.owner.QueryRow(context.Background(), `SELECT status::text FROM work_items WHERE id = $1`, id).Scan(&s)
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
	wi := w.workItem()
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
	if s := w.workItemStatus(wi); s != "review" {
		t.Errorf("work item = %s, want review (waiting on people)", s)
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
	if n := w.count(`SELECT count(*) FROM review_findings WHERE work_item_id = $1 AND status = 'open'`, wi); n != 0 {
		t.Errorf("%d findings still open", n)
	}

	w.gh.Merge(1)
	// No webhook in this test: the reconciler is the backstop that notices.
	w.until("the work item to finish", func() bool {
		_, _ = w.prs.Reconcile(context.Background(), 0)
		return w.workItemStatus(wi) == "done"
	})
}

func TestWhatDudeSendsLux(t *testing.T) {
	w := newWorld(t)
	// A real model, so the spec is the one a real agent gets.
	mustExec(t, w.owner, `UPDATE projects SET agent_models = '{"implementer":{"model":"llm/impl"}}'::jsonb WHERE id = $1`, w.project)
	w.lux.Decide = func(map[string]any) fakelux.Behaviour { return fakelux.Behaviour{Hang: true} }
	wi := w.workItem()
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
	if repo.URL != "https://github.com/acme/target.git" || repo.Ref != "main" || repo.Credential != "GIT_TOKEN" {
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
	if s := secrets["opencode_config"]; s.As != "file" || !strings.Contains(s.Value, `"model":"llm/impl"`) {
		t.Errorf("opencode config = %+v, want the implementer's model as a file secret", s)
	}
	if spec.Network == nil || len(spec.Network.Egress) != 1 || spec.Network.Egress[0].Host != "llm.example" {
		t.Errorf("network = %+v, want egress to the model provider only", spec.Network)
	}
	// The tools it runs colour their output, which the chat renders.
	if spec.Env["FORCE_COLOR"] != "1" || spec.Env["TERM"] == "" || spec.Env["GIT_CONFIG_VALUE_0"] != "always" {
		t.Errorf("env = %v, want colour forced", spec.Env)
	}
	if spec.Labels["dude.run"] == "" || spec.Labels["dude.workItem"] != wi {
		t.Errorf("labels = %v", spec.Labels)
	}

	// The key is the dude Run id: a resubmission returns the same lux Run.
	var runID string
	_ = w.owner.QueryRow(context.Background(), `SELECT id FROM runs WHERE work_item_id = $1 AND phase = 'implement'`, wi).Scan(&runID)
	if runID != spec.Labels["dude.run"] {
		t.Errorf("label run = %s, row = %s", spec.Labels["dude.run"], runID)
	}
}

func TestTheAgentsWorkReachesTheLedgerAsAConversation(t *testing.T) {
	w := newWorld(t)
	wi := w.workItem()
	w.deliver(wi)
	w.until("the implementer to finish", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE work_item_id = $1 AND phase = 'implement' AND status = 'completed'`, wi) == 1
	})
	var runID string
	_ = w.owner.QueryRow(context.Background(), `SELECT id FROM runs WHERE work_item_id = $1 AND phase = 'implement'`, wi).Scan(&runID)

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
	_ = w.owner.QueryRow(context.Background(), `SELECT changed_paths FROM runs WHERE id = $1`, runID).Scan(&changed)
	if len(changed) != 1 || changed[0] != "FACTORY.md" {
		t.Errorf("changed paths = %v", changed)
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
	wi := w.workItem()
	w.deliver(wi)
	w.until("a pull request", func() bool { return len(w.gh.Pulls()) == 1 })
	// The fixer is a real model, so its spec carries the prompt a real agent gets.
	mustExec(t, w.owner, `UPDATE projects SET agent_models = agent_models || '{"implementer":{"model":"llm/impl"}}'::jsonb
		WHERE id = $1`, w.project)

	w.gh.Comment(1, "reviewer-person", "Please rename the greeting.")
	w.until("a fix for the comment", func() bool {
		_, _ = w.prs.Reconcile(context.Background(), 0)
		return w.count(`SELECT count(*) FROM runs WHERE work_item_id = $1 AND phase = 'fix'`, wi) == 1
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
	wi := w.workItem()
	w.deliver(wi)
	w.until("the implementer to finish", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE work_item_id = $1 AND phase = 'implement' AND status = 'completed'`, wi) == 1
	})
	ctx := context.Background()
	var runID, harness, model string
	var ctxTokens, outTokens int64
	_ = w.owner.QueryRow(ctx, `SELECT id, harness, model, context_tokens, output_tokens FROM runs
		WHERE work_item_id = $1 AND phase = 'implement'`, wi).Scan(&runID, &harness, &model, &ctxTokens, &outTokens)
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

func TestAnAgentThatAsksWaitsForTheAnswerAndCarriesOn(t *testing.T) {
	w := newWorld(t)
	question := "I need a decision.\n\n```question\nShould the table be sorted?\n- yes\n- no\n```\n"
	w.lux.Decide = func(spec map[string]any) fakelux.Behaviour {
		if labels, _ := spec["labels"].(map[string]any); labels["dude.phase"] != "implement" {
			return fakelux.Behaviour{Hang: true}
		}
		return fakelux.Behaviour{Ask: question, Reply: "Sorted it, as asked.",
			Commit: map[string]string{"a.md": "x\n"}, Message: "work"}
	}
	wi := w.workItem()
	w.deliver(wi)
	w.until("the question to reach a person", func() bool { return w.workItemStatus(wi) == "awaiting_input" })

	var qid, prompt string
	var options []string
	_ = w.owner.QueryRow(context.Background(), `SELECT id, prompt, ARRAY(SELECT jsonb_array_elements_text(options))
		FROM questions WHERE work_item_id = $1`, wi).Scan(&qid, &prompt, &options)
	if prompt != "Should the table be sorted?" || len(options) != 2 {
		t.Fatalf("question = %q %v", prompt, options)
	}
	// Waiting is not finishing: the phase must not complete, or be pushed,
	// with its task unanswered.
	if n := w.count(`SELECT count(*) FROM runs WHERE work_item_id = $1 AND status = 'running'`, wi); n != 1 {
		t.Fatalf("the asking run is not still running")
	}
	if len(w.lux.Runs()) != 1 || w.lux.Runs()[0].Pushed != "" {
		t.Fatalf("the run was pushed before its question was answered")
	}

	if status, body := w.call("/internal/questions/"+qid+"/answer", map[string]any{"text": "yes"}); status != 200 {
		t.Fatalf("answer: %d %v", status, body)
	}
	w.until("the implementer to finish", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE work_item_id = $1 AND phase = 'implement' AND status = 'completed'`, wi) == 1
	})
	if in := w.lux.Runs()[0].Inputs; len(in) != 1 || !strings.Contains(in[0], "Should the table be sorted?") || !strings.HasSuffix(in[0], "yes") {
		t.Errorf("the agent was given %v, want the answer quoting its question", in)
	}
	if status, _ := w.call("/internal/questions/"+qid+"/answer", map[string]any{"text": "no"}); status != 409 {
		t.Errorf("a second answer was accepted: %d", status)
	}
	for _, typ := range []string{"question.asked", "question.answered"} {
		if n := w.count(`SELECT count(*) FROM events WHERE work_item_id = $1 AND event_type = $2`, wi, typ); n != 1 {
			t.Errorf("%s: %d events", typ, n)
		}
	}
}

// asking starts a delivery whose implementer stops on a question.
func (w *world) asking() (wi, runID string) {
	w.lux.Decide = func(spec map[string]any) fakelux.Behaviour {
		if labels, _ := spec["labels"].(map[string]any); labels["dude.phase"] != "implement" {
			return fakelux.Behaviour{Hang: true}
		}
		return fakelux.Behaviour{Ask: fakeagent.Question, Reply: "Done.", Commit: map[string]string{"a.md": "x\n"}, Message: "work"}
	}
	wi = w.workItem()
	w.deliver(wi)
	w.until("the question to reach a person", func() bool { return w.workItemStatus(wi) == "awaiting_input" })
	_ = w.owner.QueryRow(context.Background(), `SELECT id FROM runs WHERE work_item_id = $1`, wi).Scan(&runID)
	return wi, runID
}

func TestAbortingARunThatAskedCancelsItsQuestion(t *testing.T) {
	w := newWorld(t)
	wi, runID := w.asking()
	if status, body := w.call("/internal/runs/"+runID+"/abort", map[string]any{}); status != 200 {
		t.Fatalf("abort: %d %v", status, body)
	}
	if n := w.count(`SELECT count(*) FROM questions WHERE work_item_id = $1 AND status = 'open'`, wi); n != 0 {
		t.Errorf("an aborted Run's question is still waiting for a person")
	}
}

func TestAnAgentThatDiesWhileWaitingFailsItsRun(t *testing.T) {
	w := newWorld(t)
	wi, _ := w.asking()
	w.lux.Crash(w.lux.Runs()[0].ID)
	w.until("the run to fail", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE work_item_id = $1 AND status = 'failed'`, wi) == 1
	})
	if n := w.count(`SELECT count(*) FROM questions WHERE work_item_id = $1 AND status = 'open'`, wi); n != 0 {
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
	wi := w.workItem()
	w.deliver(wi)
	w.until("a second review", func() bool { return reviews >= 2 && w.count(`SELECT count(*) FROM runs
		WHERE work_item_id = $1 AND phase = 'review' AND status = 'completed'`, wi) >= 2 })

	if n := w.count(`SELECT count(*) FROM review_findings WHERE work_item_id = $1 AND status = 'open'`, wi); n != 1 {
		t.Errorf("open findings = %d; a finding the reviewer says is still there must stay open", n)
	}
	if !strings.Contains(shown[1], "FACTORY.md does not record the fix") || !strings.Contains(shown[1], "F1: fixed | still") {
		t.Errorf("the re-review was not shown the finding to judge:\n%s", shown[1])
	}
}

func TestAReviewersFindingsAreRecorded(t *testing.T) {
	w := newWorld(t)
	wi := w.workItem()
	w.deliver(wi)
	w.until("a finding", func() bool {
		return w.count(`SELECT count(*) FROM review_findings WHERE work_item_id = $1`, wi) >= 1
	})
	var sev, title, file string
	var line int
	_ = w.owner.QueryRow(context.Background(), `SELECT severity::text, title, file, line FROM review_findings WHERE work_item_id = $1
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
	wi := w.workItem()
	policy := delivery.DefaultPolicy()
	policy.BlockingSeverities = []string{"blocking", "high", "medium"}
	if _, _, err := w.runtime.Start(context.Background(), workflow.StartOptions{
		Type: delivery.WorkflowType, OrganizationID: w.org, IdempotencyKey: "delivery:" + wi, WorkItemID: wi,
		Input: delivery.State{WorkItemID: wi, ProjectID: w.project, RepositoryID: w.repoID,
			Policy: policy, Branch: delivery.BranchFor(wi, 1)},
	}); err != nil {
		t.Fatal(err)
	}
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
	w := newWorld(t)
	w.lux.Decide = func(map[string]any) fakelux.Behaviour { return fakelux.Behaviour{Hang: true} }
	wi := w.workItem()
	w.deliver(wi)
	w.until("the agent to be working", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE work_item_id = $1 AND status = 'running'`, wi) == 1
	})
	var runID string
	_ = w.owner.QueryRow(context.Background(), `SELECT id FROM runs WHERE work_item_id = $1`, wi).Scan(&runID)

	// An agent that cannot take a message mid-turn holds it until the turn
	// ends: sent, but not yet delivered.
	mustExec(t, w.owner, `INSERT INTO directives (id, organization_id, work_item_id, run_id, text) VALUES ('dir_1', $1, $2, $3, 'also add a test')`,
		w.org, wi, runID)
	w.until("the directive to be sent", func() bool {
		return w.count(`SELECT count(*) FROM directives WHERE id = 'dir_1' AND sent_at IS NOT NULL`) == 1
	})
	if n := w.count(`SELECT count(*) FROM directives WHERE id = 'dir_1' AND delivered_at IS NOT NULL`); n != 0 {
		t.Errorf("a directive to a busy agent was reported delivered before the agent had it")
	}

	// One that interrupts is heard now, and so is everything queued before it.
	mustExec(t, w.owner, `INSERT INTO directives (id, organization_id, work_item_id, run_id, text, interrupt) VALUES ('dir_2', $1, $2, $3, 'stop and listen', true)`,
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

func TestPauseKeepsTheRunAndResumeContinuesIt(t *testing.T) {
	w := newWorld(t)
	w.lux.Decide = func(map[string]any) fakelux.Behaviour {
		return fakelux.Behaviour{Hang: true, Reply: "Done after resume.", Commit: map[string]string{"A.md": "a\n"}}
	}
	wi := w.workItem()
	w.deliver(wi)
	w.until("the agent to be working", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE work_item_id = $1 AND status = 'running'`, wi) == 1
	})
	var runID string
	_ = w.owner.QueryRow(context.Background(), `SELECT id FROM runs WHERE work_item_id = $1`, wi).Scan(&runID)

	mustExec(t, w.owner, `UPDATE runs SET control = 'pause_graceful' WHERE id = $1`, runID)
	w.until("the run to pause", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'paused'`, runID) == 1
	})
	if r := w.lux.Runs()[0]; r.Stopped != 1 || r.State != "stopped" {
		t.Errorf("lux run stopped=%d state=%s", r.Stopped, r.State)
	}

	// A directive given while paused, then the request to resume.
	mustExec(t, w.owner, `INSERT INTO directives (id, organization_id, work_item_id, run_id, text) VALUES ('dir_r', $1, $2, $3, 'carry on')`,
		w.org, wi, runID)
	mustExec(t, w.owner, `UPDATE runs SET control = 'resume' WHERE id = $1`, runID)
	w.until("the resumed run to finish its turn", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'completed'`, runID) == 1
	})
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
	wi := w.workItem()
	w.deliver(wi)
	w.until("the agent to be working", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE work_item_id = $1 AND status = 'running'`, wi) == 1
	})
	mustExec(t, w.owner, `UPDATE runs SET status = 'aborted', control = 'abort' WHERE work_item_id = $1`, wi)
	w.until("the lux run to be cancelled", func() bool { return w.lux.Runs()[0].Cancelled })
}

func TestAnAgentThatDiesFailsItsPhaseAndEscalates(t *testing.T) {
	w := newWorld(t)
	w.lux.Decide = func(map[string]any) fakelux.Behaviour { return fakelux.Behaviour{Crash: true} }
	wi := w.workItem()
	w.deliver(wi)
	w.until("the work item to need a person", func() bool { return w.workItemStatus(wi) == "awaiting_input" })
	if n := w.count(`SELECT count(*) FROM runs WHERE work_item_id = $1 AND status = 'failed'`, wi); n != 1 {
		t.Errorf("failed runs = %d", n)
	}
}

func TestARunLuxNoLongerHasFailsItsPhase(t *testing.T) {
	w := newWorld(t)
	w.lux.Decide = func(map[string]any) fakelux.Behaviour { return fakelux.Behaviour{Hang: true} }
	wi := w.workItem()
	w.deliver(wi)
	w.until("the agent to be working", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE work_item_id = $1 AND status = 'running'`, wi) == 1
	})
	w.lux.Forget()
	w.until("the work item to need a person", func() bool { return w.workItemStatus(wi) == "awaiting_input" })
}

func TestAnImplementerThatChangesNothingIsEscalated(t *testing.T) {
	w := newWorld(t)
	w.lux.Decide = func(map[string]any) fakelux.Behaviour { return fakelux.Behaviour{Reply: "Nothing to do."} }
	wi := w.workItem()
	w.deliver(wi)
	w.until("escalation", func() bool { return w.workItemStatus(wi) == "awaiting_input" })
	var reason string
	_ = w.owner.QueryRow(context.Background(), `SELECT payload->>'reason' FROM events WHERE work_item_id = $1
		AND event_type = 'work_item.status_changed' ORDER BY cursor DESC LIMIT 1`, wi).Scan(&reason)
	if reason != "no_changes" {
		t.Errorf("reason = %s", reason)
	}
}

func TestLuxRefusingASpecFailsThePhaseRatherThanRetryingForever(t *testing.T) {
	w := newWorld(t)
	mustExec(t, w.owner, `UPDATE projects SET agent_models = '{}'::jsonb WHERE id = $1`, w.project)
	wi := w.workItem()
	w.deliver(wi)
	w.until("escalation", func() bool { return w.workItemStatus(wi) == "awaiting_input" })
	var errText string
	_ = w.owner.QueryRow(context.Background(), `SELECT error FROM runs WHERE work_item_id = $1`, wi).Scan(&errText)
	if !strings.Contains(errText, "no model is configured for the implementer role") {
		t.Errorf("error = %q", errText)
	}
	if len(w.lux.Runs()) != 0 {
		t.Errorf("a run with no model reached lux")
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
func TestAFinishingRunIsFollowedAfterARestart(t *testing.T) {
	w := newWorld(t)
	wi := w.workItem()
	w.deliver(wi)
	w.until("the implementer's push to be asked for", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE work_item_id = $1 AND phase = 'implement' AND push_request_id IS NOT NULL`, wi) == 1
	})
	// A new orchestrator: nothing is following anything.
	w.syncer.Stop()
	w.syncer = &phases.Syncer{DB: w.syncer.DB, Lux: w.syncer.Lux, Forges: w.syncer.Forges, Log: quiet, Agent: w.syncer.Agent}
	t.Cleanup(w.syncer.Stop)
	w.until("the implementer to complete", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE work_item_id = $1 AND phase = 'implement' AND status = 'completed'`, wi) == 1
	})
}

// An abort that lands while the Run is being submitted wins. Found in
// review: submit set the Run back to scheduled, and its lux Run carried on.
func TestAnAbortDuringSubmitIsNotUndone(t *testing.T) {
	w := newWorld(t)
	w.lux.Decide = func(map[string]any) fakelux.Behaviour { return fakelux.Behaviour{Hang: true} }
	wi := w.workItem()
	w.deliver(wi)
	// Create the Run without submitting it, then abort it and let the
	// submit happen: the row the submit sees is already aborted.
	for range 5 {
		if _, err := w.runtime.Tick(context.Background(), 10); err != nil {
			t.Fatal(err)
		}
	}
	mustExec(t, w.owner, `UPDATE runs SET status = 'aborted', control = 'abort' WHERE work_item_id = $1`, wi)
	var runID string
	_ = w.owner.QueryRow(context.Background(), `SELECT id FROM runs WHERE work_item_id = $1`, wi).Scan(&runID)
	// Submit as the sweep would have, had it read the row a moment earlier.
	mustExec(t, w.owner, `UPDATE runs SET status = 'pending' WHERE id = $1`, runID)
	go func() {
		time.Sleep(5 * time.Millisecond)
		_, _ = w.owner.Exec(context.Background(), `UPDATE runs SET status = 'aborted' WHERE id = $1`, runID)
	}()
	w.until("the lux run to be cancelled", func() bool {
		return len(w.lux.Runs()) == 1 && w.lux.Runs()[0].Cancelled
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
	wi := w.workItem()
	w.deliver(wi)
	w.until("the agent to be working", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE work_item_id = $1 AND status = 'running'`, wi) == 1
	})
	var runID string
	_ = w.owner.QueryRow(context.Background(), `SELECT id FROM runs WHERE work_item_id = $1`, wi).Scan(&runID)
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
	wi := w.workItem()
	w.deliver(wi)
	w.until("the agent to be working", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE work_item_id = $1 AND status = 'running'`, wi) == 1
	})
	mustExec(t, w.owner, `UPDATE runs SET control = 'pause_hard' WHERE work_item_id = $1`, wi)
	w.until("the run to pause", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE work_item_id = $1 AND status = 'paused'`, wi) == 1
	})
	for range 5 {
		w.pump()
	}
	if n := w.count(`SELECT count(*) FROM events WHERE work_item_id = $1 AND event_type = 'run.failed'`, wi); n != 0 {
		t.Errorf("a paused run was recorded as failed")
	}
}
