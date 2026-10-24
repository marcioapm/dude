package automations

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/agenttools"
	"github.com/marciomartins/dude/orchestrator/internal/db"
	"github.com/marciomartins/dude/orchestrator/internal/dbtest"
	"github.com/marciomartins/dude/orchestrator/internal/fakelux"
	"github.com/marciomartins/dude/orchestrator/internal/lux"
	"github.com/marciomartins/dude/orchestrator/internal/phases"
)

// harness is a database, a fake lux and a runner over them, with one
// organisation, one project, one owner and a scripted model tier.
type harness struct {
	t       *testing.T
	app     *db.DB
	owner   *pgx.Conn
	org     string
	project string
	person  string
	tier    string
	fake    *fakelux.Server
	lux     *lux.HTTPClient
	runner  *Runner
	skew    atomic.Int64
	frozen  atomic.Int64
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	app, owner := dbtest.Open(t)
	h := &harness{t: t, app: app, owner: owner, org: dbtest.Org(t, owner)}
	h.project, h.person, h.tier = h.org+"_p", h.org+"_ana", h.org+"_tier"
	exec(t, owner, `INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES ($1, $2, 'web', 'web', 'WEB')`, h.project, h.org)
	// A remote repository: the fake lux checks out nothing for it unless a run names it.
	exec(t, owner, `INSERT INTO repositories (id, organization_id, project_id, name, url) VALUES ($1 || '_r', $2, $1, 'web', 'https://github.com/acme/web.git')`, h.project, h.org)
	exec(t, owner, `INSERT INTO people (id, organization_id, name, role) VALUES ($1, $2, 'Ana', 'admin')`, h.person, h.org)
	exec(t, owner, `INSERT INTO model_tiers (id, organization_id, name, model) VALUES ($1, $2, 'Scripted', 'fake/scripted')`, h.tier, h.org)
	h.fake = fakelux.New("", "k", nil)
	h.fake.Workspaces = t.TempDir()
	srv := httptest.NewServer(h.fake.Handler())
	t.Cleanup(srv.Close)
	t.Cleanup(h.fake.Close)
	h.lux = lux.New(srv.URL, "k")
	h.runner = &Runner{DB: app, Lux: h.lux, Agent: phases.AgentConfig{DefaultImage: "agent:1"},
		Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	h.runner.Now = func() time.Time {
		if f := h.frozen.Load(); f != 0 {
			return time.Unix(0, f)
		}
		return time.Now().Add(time.Duration(h.skew.Load()))
	}
	// Background follows and POSTs end before the database and fake go.
	t.Cleanup(h.runner.Wait)
	return h
}

// advance moves the runner's clock on by d.
func (h *harness) advance(d time.Duration) {
	if f := h.frozen.Load(); f != 0 {
		h.frozen.Store(f + int64(d))
		return
	}
	h.skew.Add(int64(d))
}

// at stops the runner's clock at t.
func (h *harness) at(t time.Time) { h.frozen.Store(t.UnixNano()) }

// automation adds an enabled automation of the project's, owned by Ana.
func (h *harness) automation(id, kind string, trigger, extra map[string]any) {
	h.t.Helper()
	tr, _ := json.Marshal(trigger)
	cols := map[string]any{"session_mode": "ephemeral", "expiry_days": nil, "concurrency": "queue", "agent": nil, "command": nil,
		"server": nil, "webhook": nil, "webhook_secret": nil, "project_id": h.project}
	for k, v := range extra {
		cols[k] = v
	}
	js := func(v any) any {
		if v == nil {
			return nil
		}
		b, _ := json.Marshal(v)
		return string(b)
	}
	exec(h.t, h.owner, `INSERT INTO automations (id, organization_id, project_id, name, kind, enabled, owner_id, trigger, session_mode,
		expiry_days, concurrency, agent, command, server, webhook, webhook_secret, repos)
		VALUES ($1, $2, $3, $1, $4, true, $5, $6, $7, $8, $9, $10::jsonb, $11, $12::jsonb, $13::jsonb, $14, '"none"')`,
		id, h.org, cols["project_id"], kind, h.person, string(tr), cols["session_mode"], cols["expiry_days"], cols["concurrency"],
		js(cols["agent"]), cols["command"], js(cols["server"]), js(cols["webhook"]), cols["webhook_secret"])
}

func (h *harness) deliver(id, event string, body map[string]any) {
	h.t.Helper()
	b, _ := json.Marshal(body)
	exec(h.t, h.owner, `INSERT INTO webhook_deliveries (id, organization_id, event, payload) VALUES ($1, $2, $3, $4)`, id, h.org, event, string(b))
}

type runRow struct {
	ID, Status, Reason, Source, Session, LuxRunID, StopReason string
	Payloads                                                  int
	Log                                                       string
}

func (h *harness) runs(automation string) []runRow {
	h.t.Helper()
	rows, err := h.owner.Query(context.Background(), `SELECT id, status, COALESCE(reason, ''), source, COALESCE(session, ''),
		COALESCE(lux_run_id, ''), COALESCE(stop_reason, ''), jsonb_array_length(payloads), COALESCE(log_tail, '')
		FROM automation_runs WHERE automation_id = $1 ORDER BY created_at, id`, automation)
	if err != nil {
		h.t.Fatal(err)
	}
	out, err := pgx.CollectRows(rows, pgx.RowToStructByPos[runRow])
	if err != nil {
		h.t.Fatal(err)
	}
	return out
}

// until sweeps until cond holds, failing after a few seconds.
func (h *harness) until(what string, cond func() bool) {
	h.t.Helper()
	for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if _, err := h.runner.Sweep(context.Background()); err != nil {
			h.t.Fatal(err)
		}
		if cond() {
			return
		}
	}
	h.t.Fatalf("never: %s", what)
}

func prBody(action, repo, sender string) map[string]any {
	return map[string]any{"action": action, "repository": map[string]any{"full_name": repo}, "sender": map[string]any{"login": sender},
		"pull_request": map[string]any{"number": 7, "title": "Add the thing", "head": map[string]any{"ref": "feat/x"}, "labels": []any{}}}
}

func (h *harness) luxState(id string) string {
	h.t.Helper()
	got, err := h.lux.Get(context.Background(), id)
	if err != nil {
		h.t.Fatal(err)
	}
	return got.State
}

// serve puts app behind the fake lux's preview listener, as a real lux's
// server URL is, and points every Server run at it.
func (h *harness) serve(app http.Handler) *httptest.Server {
	h.t.Helper()
	srv := httptest.NewServer(h.fake.Preview(app))
	h.t.Cleanup(srv.Close)
	h.runner.ServerURL = func(context.Context, string, int) (string, error) { return srv.URL, nil }
	return srv
}

// count is how many of calls are call.
func count(calls []string, call string) int {
	n := 0
	for _, c := range calls {
		if c == call {
			n++
		}
	}
	return n
}

func TestAutomationIssuesStableScopedToken(t *testing.T) {
	h := newHarness(t)
	h.runner.Agent.ToolsURL = "https://example.com/agent-tools"
	h.runner.Agent.ToolsKey = []byte("automation-test-key")
	h.automation("aut_token", "agent", map[string]any{"kind": "github", "event": "pull_request"}, map[string]any{"project_id": nil, "agent": map[string]any{"own": map[string]any{"tierId": h.tier, "prompt": "Triage"}}})
	body, _ := json.Marshal(prBody("opened", "acme/web", "ana"))
	payloads := []Payload{{Event: "pull_request", Body: body}}
	ctx := context.Background()
	var a Automation
	var id string
	if err := h.app.InOrg(ctx, h.org, func(tx pgx.Tx) (err error) {
		a, err = Load(ctx, tx, "aut_token")
		if err != nil {
			return err
		}
		fired, err := Fire(ctx, tx, a, SourceEvent, payloads, "", nil)
		id = fired.Run
		return err
	}); err != nil {
		t.Fatal(err)
	}
	spec, why, err := h.runner.spec(ctx, a, id, payloads, nil)
	if err != nil || why != "" {
		t.Fatalf("spec: %s %v", why, err)
	}
	if len(spec.Workload.MCPServers) != 1 {
		t.Fatalf("MCP servers: %v", spec.Workload.MCPServers)
	}
	token, hash := agenttools.RunToken(h.runner.Agent.ToolsKey, id, 0)
	found := false
	for _, secret := range spec.Secrets {
		if secret.Name == "DUDE_TOOLS_AUTH" && secret.Value == "Bearer "+token {
			found = true
		}
	}
	if !found {
		t.Fatal("tools token missing from spec")
	}
	retry, err := h.runner.toolsToken(ctx, a, id, payloads)
	if err != nil || retry != token {
		t.Fatalf("retry: %v", err)
	}
	var stored string
	var scope agenttools.AutomationScope
	if err := h.owner.QueryRow(ctx, `SELECT mcp_token_hash,tools_scope FROM automation_runs WHERE id=$1`, id).Scan(&stored, &scope); err != nil {
		t.Fatal(err)
	}
	if stored != hash || scope.ProjectID != h.project || scope.OwnerID != h.person || len(scope.Repos) != 1 || scope.Repos[0] != "acme/web" || scope.EventNumber != 7 {
		t.Fatalf("scope: %+v hash matches: %v", scope, stored == hash)
	}
	var runs int
	if err := h.owner.QueryRow(ctx, `SELECT count(*) FROM runs`).Scan(&runs); err != nil || runs != 0 {
		t.Fatalf("task runs: %d %v", runs, err)
	}

	// An organisation automation's filter limits the token's repositories:
	// a second project's acme/docs is outside it, and an event from there
	// names no project for create_task.
	exec(t, h.owner, `INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES ($1 || '_docs', $2, 'docs', 'docs', 'DOC')`, h.project, h.org)
	exec(t, h.owner, `INSERT INTO repositories (id, organization_id, project_id, name, url) VALUES ($1 || '_docs_r', $2, $1 || '_docs', 'docs', 'https://github.com/acme/docs.git')`, h.project, h.org)
	exec(t, h.owner, `UPDATE automations SET repo_filter = '{"mode":"except","repos":["acme/docs"]}' WHERE id = 'aut_token'`)
	scoped := func(repo string) agenttools.AutomationScope {
		t.Helper()
		b, _ := json.Marshal(prBody("opened", repo, "ana"))
		p := []Payload{{Event: "pull_request", Body: b}}
		var a Automation
		var fired Fired
		if err := h.app.InOrg(ctx, h.org, func(tx pgx.Tx) (err error) {
			if a, err = Load(ctx, tx, "aut_token"); err != nil {
				return err
			}
			fired, err = Fire(ctx, tx, a, SourceTest, p, "", nil)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := h.runner.toolsToken(ctx, a, fired.Run, p); err != nil {
			t.Fatal(err)
		}
		var s agenttools.AutomationScope
		if err := h.owner.QueryRow(ctx, `SELECT tools_scope FROM automation_runs WHERE id=$1`, fired.Run).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	if s := scoped("acme/web"); len(s.Repos) != 1 || s.Repos[0] != "acme/web" || s.ProjectID != h.project {
		t.Errorf("filtered scope from acme/web: %+v", s)
	}
	if s := scoped("acme/docs"); len(s.Repos) != 1 || s.Repos[0] != "acme/web" || s.ProjectID != "" {
		t.Errorf("filtered scope from acme/docs: %+v", s)
	}
}

// A preset's override of each field reaches the run's spec; without one,
// the role's resolved value does.
func TestAutomationPresetOverridesEachField(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	exec(t, h.owner, `INSERT INTO model_tiers (id, organization_id, name, model) VALUES ($1 || '_big', $1, 'Big', 'acme-large')`, h.org)
	exec(t, h.owner, `INSERT INTO machine_sizes (id, organization_id, name, cpus, memory_mib, disk_gib) VALUES
		($1 || '_s', $1, 'Role size', 1, 1024, 10), ($1 || '_l', $1, 'Override size', 8, 8192, 80)`, h.org)
	for _, img := range []string{"role", "own"} {
		id := h.org + "_img_" + img
		exec(t, h.owner, `INSERT INTO images (id, organization_id, name) VALUES ($1, $2, $3)`, id, h.org, img)
		exec(t, h.owner, `INSERT INTO image_versions (id, organization_id, image_id, number, containerfile, state, user_ref)
			VALUES ($1 || '_v', $2, $1, 1, 'FROM debian', 'published', 'registry.example.com/' || $3 || ':1')`, id, h.org, img)
		exec(t, h.owner, `UPDATE images SET published_version_id = $1 || '_v' WHERE id = $1`, id)
		exec(t, h.owner, `INSERT INTO image_finals (organization_id, image_version_id, layer_ref, final_ref)
			VALUES ($2, $1 || '_v', 'registry.example.com/dude-layer:1', 'registry.example.com/' || $3 || ':1')`, id, h.org, img)
	}
	h.runner.Agent.Layer = "registry.example.com/dude-layer:1"
	models, _ := json.Marshal(map[string]any{"qa_browser": map[string]any{"tier": h.tier, "harness": "opencode", "timeLimitMinutes": 60,
		"machineSize": h.org + "_s", "image": h.org + "_img_role"}})
	exec(t, h.owner, `UPDATE organizations SET default_agent_models = $2 WHERE id = $1`, h.org, string(models))
	overrides := map[string]map[string]any{
		"none":      {},
		"harness":   {"harness": "claude-code"},
		"tier":      {"tierId": h.org + "_big"},
		"timeLimit": {"timeLimitMinutes": 240},
		"machine":   {"machineSizeId": h.org + "_l"},
		"image":     {"imageId": h.org + "_img_own"},
	}
	type got struct {
		harness, model, timeout, image string
		cpus                           float64
	}
	results := map[string]got{}
	for name, o := range overrides {
		id := "aut_ovr_" + name
		h.automation(id, "agent", map[string]any{"kind": "schedule", "cron": "0 * * * *", "tz": "UTC"},
			map[string]any{"agent": map[string]any{"preset": map[string]any{"role": "qa_browser", "overrides": o}, "brief": "Check"}})
		var a Automation
		if err := h.app.InOrg(ctx, h.org, func(tx pgx.Tx) (err error) { a, err = Load(ctx, tx, id); return err }); err != nil {
			t.Fatal(err)
		}
		spec, why, err := h.runner.spec(ctx, a, "arn_"+name, nil, nil)
		if err != nil || why != "" {
			t.Fatalf("%s: %s %v", name, why, err)
		}
		g := got{harness: spec.Labels["dude.harness"], model: spec.Labels["dude.model"], timeout: spec.Timeout, image: spec.Image.Ref}
		if spec.Resources != nil {
			g.cpus = spec.Resources.CPUs
		}
		results[name] = g
	}
	base := results["none"]
	if base.model != "fake/scripted" || base.timeout != "60m" || base.cpus != 1 || !strings.Contains(base.image, "role:1") {
		t.Fatalf("role's values: %+v", base)
	}
	for name, check := range map[string]func(got) bool{
		"harness":   func(g got) bool { return g.harness == "claude-code" && g.harness != base.harness },
		"tier":      func(g got) bool { return g.model == "acme-large" },
		"timeLimit": func(g got) bool { return g.timeout == "240m" },
		"machine":   func(g got) bool { return g.cpus == 8 },
		"image":     func(g got) bool { return strings.Contains(g.image, "own:1") },
	} {
		if !check(results[name]) {
			t.Errorf("%s override not applied: %+v (role's %+v)", name, results[name], base)
		}
	}
}

// An agent on a GitHub event, resumable: the first matching delivery
// starts a session it keeps (stopped, not cancelled), the next resumes it,
// a non-matching one fires nothing, and a test event gets a fresh,
// ephemeral session of its own that leaves the kept one alone (Q21).
func TestAutomationAgentOnAnEventKeepsAndResumesItsSession(t *testing.T) {
	h := newHarness(t)
	h.automation("aut_triage", "agent", map[string]any{"kind": "github", "event": "pull_request", "actions": []string{"opened"}},
		map[string]any{"session_mode": "resumable", "expiry_days": 7,
			"agent": map[string]any{"own": map[string]any{"tierId": h.tier, "prompt": "Triage {{pr.title}} in {{repository}} for {{sender}}."}}})
	h.deliver("d-closed", "pull_request", prBody("closed", "acme/web", "ana"))
	h.deliver("d-1", "pull_request", prBody("opened", "acme/web", "ana"))
	h.until("the first run succeeds", func() bool {
		r := h.runs("aut_triage")
		return len(r) == 1 && r[0].Status == "succeeded"
	})
	first := h.runs("aut_triage")[0]
	if first.Session != "new" || first.Source != "event" || first.LuxRunID == "" {
		t.Fatalf("first run: %+v", first)
	}
	h.until("the kept session is stopped, not cancelled", func() bool { return h.luxState(first.LuxRunID) == "stopped" })

	h.deliver("d-2", "pull_request", prBody("opened", "acme/web", "bo"))
	h.until("the second run succeeds", func() bool {
		r := h.runs("aut_triage")
		return len(r) == 2 && r[1].Status == "succeeded"
	})
	second := h.runs("aut_triage")[1]
	if second.Session != "resumed" || second.LuxRunID != first.LuxRunID {
		t.Fatalf("second run did not resume the kept session: %+v (first %s)", second, first.LuxRunID)
	}
	inputs := h.fake.ResumeInputs(first.LuxRunID)
	if len(inputs) == 0 || !strings.Contains(inputs[len(inputs)-1], "Triage Add the thing in acme/web for bo.") {
		t.Errorf("resume inputs: %q", inputs)
	}

	// A test event: its own run, fresh and ephemeral; the kept session stays.
	body, _ := json.Marshal(prBody("opened", "acme/web", "ana"))
	runID, v, err := h.runner.RunNow(context.Background(), h.org, "aut_triage", h.person, "", &Payload{DeliveryID: "test-1", Event: "pull_request", Body: body})
	if err != nil || !v.Matches {
		t.Fatalf("run now: %v %+v", err, v)
	}
	h.until("the test run succeeds", func() bool {
		for _, r := range h.runs("aut_triage") {
			if r.ID == runID {
				return r.Status == "succeeded"
			}
		}
		return false
	})
	test := h.runs("aut_triage")[2]
	if test.Source != "test" || test.Session != "ephemeral" || test.LuxRunID == first.LuxRunID {
		t.Errorf("test run: %+v", test)
	}
	var kept string
	if err := h.owner.QueryRow(context.Background(), `SELECT lux_run_id FROM automation_sessions WHERE automation_id = 'aut_triage'`).Scan(&kept); err != nil || kept != first.LuxRunID {
		t.Errorf("kept session %q %v, want %s", kept, err, first.LuxRunID)
	}
	h.until("the test's container is ended", func() bool { return lux.Terminated(h.luxState(test.LuxRunID)) })

	// A written event the filters would skip is refused with the filter.
	closed, _ := json.Marshal(prBody("closed", "acme/web", "ana"))
	_, v, err = h.runner.RunNow(context.Background(), h.org, "aut_triage", h.person, "", &Payload{DeliveryID: "test-2", Event: "pull_request", Body: closed})
	if err != nil || v.Matches || v.Filter != "actions" {
		t.Errorf("a skipped test event: %+v %v", v, err)
	}

	// After its expiry days without a run, the kept session is cleaned up,
	// whether on or off (Q10, I5); the runner's clock decides.
	exec(t, h.owner, `UPDATE automations SET enabled = false WHERE id = 'aut_triage'`)
	var last time.Time
	if err := h.owner.QueryRow(context.Background(), `SELECT last_run_at FROM automation_sessions WHERE automation_id = 'aut_triage'`).Scan(&last); err != nil {
		t.Fatal(err)
	}
	sessions := func() int {
		var n int
		_ = h.owner.QueryRow(context.Background(), `SELECT count(*) FROM automation_sessions`).Scan(&n)
		return n
	}
	h.at(last.Add(6*24*time.Hour + 23*time.Hour))
	for range 2 {
		if _, err := h.runner.Sweep(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if sessions() != 1 || h.luxState(first.LuxRunID) != "stopped" {
		t.Fatalf("at 6 days 23 hours: %d sessions, lux %s", sessions(), h.luxState(first.LuxRunID))
	}
	// A running (non-test) run holds the session past its expiry; this one is
	// still starting (no session yet), so nothing follows it meanwhile.
	exec(t, h.owner, `INSERT INTO automation_runs (id, organization_id, automation_id, status, source) VALUES ('arn_hold', $1, 'aut_triage', 'running', 'event')`, h.org)
	h.advance(time.Hour + time.Minute)
	for range 2 {
		if _, err := h.runner.Sweep(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if sessions() != 1 {
		t.Fatal("a session expired under a running run")
	}
	exec(t, h.owner, `UPDATE automation_runs SET status = 'succeeded', ended_at = now() WHERE id = 'arn_hold'`)
	h.until("the kept session expires at 7 days and a minute", func() bool { return sessions() == 0 })
	h.until("its container is ended", func() bool { return lux.Terminated(h.luxState(first.LuxRunID)) })
}

// lux keeps no secret's value and refuses a resume that does not give
// every declared secret again. A resumable Server declares its event file
// and an agent its chosen project secrets: the next firing resumes the kept
// container with each of them (the event file holding the new event), not
// a fresh one.
func TestAutomationResumeGivesEveryDeclaredSecretAgain(t *testing.T) {
	h := newHarness(t)
	h.serve(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }))
	exec(t, h.owner, `INSERT INTO project_secrets (organization_id, project_id, name, value, hint) VALUES ($1, $2, 'ACME_TOKEN', 'acme-1', 'e-1')`, h.org, h.project)
	h.automation("aut_rsrv", "server", map[string]any{"kind": "github", "event": "pull_request"},
		map[string]any{"session_mode": "resumable", "expiry_days": 7,
			"server": map[string]any{"command": "serve", "port": 8080, "path": "/events", "health": nil}})
	h.automation("aut_ragent", "agent", map[string]any{"kind": "github", "event": "pull_request"},
		map[string]any{"session_mode": "resumable", "expiry_days": 7,
			"agent": map[string]any{"own": map[string]any{"tierId": h.tier, "prompt": "Triage {{pr.title}}."}}})
	exec(t, h.owner, `UPDATE automations SET secrets = '{ACME_TOKEN}' WHERE id IN ('aut_rsrv', 'aut_ragent')`)
	h.deliver("d-1", "pull_request", prBody("opened", "acme/web", "ana"))
	h.until("the agent's first run succeeds", func() bool { r := h.runs("aut_ragent"); return len(r) == 1 && r[0].Status == "succeeded" })
	h.until("the server's first event is POSTed", func() bool {
		var posted int
		_ = h.owner.QueryRow(context.Background(), `SELECT posted FROM automation_runs WHERE automation_id = 'aut_rsrv'`).Scan(&posted)
		return posted == 1
	})
	h.runner.Wait()
	h.advance(ServerIdle + time.Second)
	h.until("the server's first run ends idle", func() bool { r := h.runs("aut_rsrv"); return len(r) == 1 && r[0].Status == "succeeded" })
	first := map[string]runRow{"aut_rsrv": h.runs("aut_rsrv")[0], "aut_ragent": h.runs("aut_ragent")[0]}
	for id, r := range first {
		h.until(id+"'s kept container is stopped", func() bool { return h.luxState(r.LuxRunID) == "stopped" })
	}

	h.deliver("d-2", "pull_request", prBody("synchronize", "acme/web", "bo"))
	for id, r := range first {
		h.until(id+"'s second run starts", func() bool { rs := h.runs(id); return len(rs) == 2 && rs[1].Session != "" })
		second := h.runs(id)[1]
		if second.Session != "resumed" || second.LuxRunID != r.LuxRunID {
			t.Errorf("%s's second run: %+v, want the kept %s resumed", id, second, r.LuxRunID)
		}
	}
	h.runner.Wait()
}

// A command on a schedule: the due schedule fires once, the command runs
// to its end in the automation's image with the event-free env, and the
// next time is set. A failing command fails the run and tells the owner.
func TestAutomationCommandOnASchedule(t *testing.T) {
	h := newHarness(t)
	h.automation("aut_report", "command", map[string]any{"kind": "schedule", "cron": "0 3 * * *", "tz": "Europe/Lisbon"},
		map[string]any{"command": `echo "report for $DUDE_AUTOMATION"`})
	exec(t, h.owner, `UPDATE automations SET next_run_at = now() - interval '1 minute' WHERE id = 'aut_report'`)
	h.until("the scheduled run succeeds", func() bool {
		r := h.runs("aut_report")
		return len(r) == 1 && r[0].Status == "succeeded"
	})
	run := h.runs("aut_report")[0]
	if run.Source != "schedule" || !strings.Contains(run.Log, "report for aut_report") {
		t.Errorf("run: %+v", run)
	}
	var next time.Time
	if err := h.owner.QueryRow(context.Background(), `SELECT next_run_at FROM automations WHERE id = 'aut_report'`).Scan(&next); err != nil {
		t.Fatal(err)
	}
	lisbon, _ := time.LoadLocation("Europe/Lisbon")
	if l := next.In(lisbon); l.Hour() != 3 || l.Minute() != 0 || !next.After(time.Now()) {
		t.Errorf("next run %v", next.In(lisbon))
	}

	h.automation("aut_broken", "command", map[string]any{"kind": "schedule", "cron": "0 3 * * *", "tz": "UTC"},
		map[string]any{"command": "echo boom; exit 3"})
	exec(t, h.owner, `UPDATE automations SET next_run_at = now() - interval '1 minute' WHERE id = 'aut_broken'`)
	h.until("the broken run fails", func() bool {
		r := h.runs("aut_broken")
		return len(r) == 1 && r[0].Status == "failed"
	})
	var to []string
	if err := h.owner.QueryRow(context.Background(), `SELECT ARRAY(SELECT jsonb_array_elements_text(payload->'to')) FROM events
		WHERE event_type = 'automation.failed' AND payload->>'automationId' = 'aut_broken'`).Scan(&to); err != nil || len(to) != 1 || to[0] != h.person {
		t.Errorf("failure told %v %v, want the owner", to, err)
	}

	// Held after its owner left: every current admin is told.
	other := h.org + "_bo"
	exec(t, h.owner, `INSERT INTO people (id, organization_id, name, role) VALUES ($1, $2, 'Bo', 'admin'), ($1 || '_gone', $2, 'Gone', 'admin'),
		($1 || '_member', $2, 'Member', 'member')`, other, h.org)
	exec(t, h.owner, `UPDATE people SET removed_at = now() WHERE id = $1 || '_gone'`, other)
	h.automation("aut_held", "command", map[string]any{"kind": "schedule", "cron": "0 3 * * *", "tz": "UTC"},
		map[string]any{"command": "exit 4"})
	exec(t, h.owner, `UPDATE automations SET owner_id = NULL, owner_left_at = now(), next_run_at = now() - interval '1 minute' WHERE id = 'aut_held'`)
	h.until("the held run fails", func() bool {
		r := h.runs("aut_held")
		return len(r) == 1 && r[0].Status == "failed"
	})
	if err := h.owner.QueryRow(context.Background(), `SELECT ARRAY(SELECT jsonb_array_elements_text(payload->'to') ORDER BY 1) FROM events
		WHERE event_type = 'automation.failed' AND payload->>'automationId' = 'aut_held'`).Scan(&to); err != nil ||
		strings.Join(to, ",") != strings.Join([]string{h.person, other}, ",") && strings.Join(to, ",") != strings.Join([]string{other, h.person}, ",") {
		t.Errorf("a held automation's failure told %v %v, want every current admin", to, err)
	}

	// Run now on a schedule while a run is live queues behind it (Q7).
	g := newGate(t)
	t.Cleanup(func() { g.release(t) })
	h.automation("aut_slow", "command", map[string]any{"kind": "schedule", "cron": "0 3 * * *", "tz": "UTC"}, map[string]any{"command": g.hold("echo done")})
	first, _, err := h.runner.RunNow(context.Background(), h.org, "aut_slow", h.person, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	h.until("the first is running", func() bool { r := h.runs("aut_slow"); return len(r) == 1 && r[0].Status == "running" })
	if _, _, err := h.runner.RunNow(context.Background(), h.org, "aut_slow", h.person, "", nil); err != nil {
		t.Fatal(err)
	}
	r := h.runs("aut_slow")
	if r[0].ID != first || r[1].Status != "queued" || r[1].Reason != "waiting for the run before it" {
		t.Errorf("runs: %+v", r)
	}
}

// A command on an event reads the event from $DUDE_EVENT_PATH and DUDE_EVENT_*.
func TestAutomationCommandGetsTheEvent(t *testing.T) {
	h := newHarness(t)
	h.automation("aut_cmd", "command", map[string]any{"kind": "github", "event": "push"},
		map[string]any{"command": `echo "$DUDE_EVENT_NAME $DUDE_DELIVERY_ID"; cat "$DUDE_EVENT_PATH"`})
	h.deliver("d-push", "push", map[string]any{"ref": "refs/heads/main", "repository": map[string]any{"full_name": "acme/web"}})
	// From a repository outside the project: matched, read, and no run.
	h.deliver("d-other", "push", map[string]any{"ref": "refs/heads/main", "repository": map[string]any{"full_name": "other/web"}})
	h.until("the run ends", func() bool {
		r := h.runs("aut_cmd")
		return len(r) == 1 && r[0].Status != "running" && r[0].Status != "queued"
	})
	h.until("both deliveries are read", func() bool {
		var n int
		_ = h.owner.QueryRow(context.Background(), `SELECT count(*) FROM webhook_deliveries WHERE automations_at IS NOT NULL`).Scan(&n)
		return n == 2
	})
	if r := h.runs("aut_cmd"); len(r) != 1 || r[0].Status != "succeeded" {
		t.Fatalf("runs: %+v", r)
	}
	log := h.runs("aut_cmd")[0].Log
	if !strings.Contains(log, "push d-push") || !strings.Contains(log, `"refs/heads/main"`) {
		t.Errorf("log: %q", log)
	}
}

// A Server: the event starts the container, dude POSTs it once the server
// answers, a second event while it is up goes to the same container, a 5xx
// is retried once, and after the idle time with nothing in flight it is
// stopped: one run, one request row per POST.
func TestAutomationServerPostsEventsAndStopsWhenIdle(t *testing.T) {
	h := newHarness(t)
	var mu sync.Mutex
	var got []http.Header
	var bodies []string
	fails := map[string]int{"d-2": 1}
	h.serve(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		defer mu.Unlock()
		got, bodies = append(got, r.Header.Clone()), append(bodies, string(b))
		if d := r.Header.Get("X-GitHub-Delivery"); fails[d] > 0 {
			fails[d]--
			w.WriteHeader(500)
			return
		}
		w.WriteHeader(202)
	}))
	if ServerIdle != 30*time.Second {
		t.Fatalf("ServerIdle %v, the decision says 30 s (Q3, Q16)", ServerIdle)
	}
	h.automation("aut_srv", "server", map[string]any{"kind": "github", "event": "pull_request"},
		map[string]any{"server": map[string]any{"command": "bun run serve", "port": 8080, "path": "/events", "health": nil}})
	h.deliver("d-1", "pull_request", prBody("opened", "acme/web", "ana"))
	h.until("the first POST", func() bool { mu.Lock(); defer mu.Unlock(); return len(got) == 1 })
	// The clock stands still: nothing idles out before d-2 reaches the same container.
	h.deliver("d-2", "pull_request", prBody("synchronize", "acme/web", "ana"))
	h.until("both POSTed, the second after a retry", func() bool {
		var posted int
		_ = h.owner.QueryRow(context.Background(), `SELECT posted FROM automation_runs WHERE automation_id = 'aut_srv'`).Scan(&posted)
		return posted == 2
	})
	h.runner.Wait()
	var last time.Time
	if err := h.owner.QueryRow(context.Background(), `SELECT last_request_at FROM automation_runs WHERE automation_id = 'aut_srv'`).Scan(&last); err != nil {
		t.Fatal(err)
	}
	h.at(last.Add(29 * time.Second))
	for range 3 {
		if _, err := h.runner.Sweep(context.Background()); err != nil {
			t.Fatal(err)
		}
		h.runner.Wait()
	}
	if r := h.runs("aut_srv"); len(r) != 1 || r[0].Status != "running" || lux.Terminated(h.luxState(r[0].LuxRunID)) || h.luxState(r[0].LuxRunID) == "stopping" {
		t.Fatalf("29 s after the last answer: %+v", r)
	}
	h.advance(time.Second)
	h.until("the run ends idle", func() bool { r := h.runs("aut_srv"); return len(r) == 1 && r[0].Status != "running" })
	r := h.runs("aut_srv")[0]
	if r.Status != "succeeded" || r.StopReason != "idle" || r.Payloads != 2 {
		t.Fatalf("run: %+v", r)
	}
	if got[0].Get("X-Dude-Automation") != "aut_srv" || got[0].Get("X-Dude-Run") != r.ID || got[0].Get("X-GitHub-Event") != "pull_request" ||
		got[0].Get("Content-Type") != "application/json" || !strings.Contains(bodies[0], `"opened"`) {
		t.Errorf("first POST headers %v body %s", got[0], bodies[0])
	}
	rows, _ := h.owner.Query(context.Background(), `SELECT delivery_id, try, http_status FROM automation_run_requests WHERE run_id = $1 ORDER BY at, try`, r.ID)
	type req struct {
		Delivery string
		Try      int
		Status   int
	}
	reqs, _ := pgx.CollectRows(rows, pgx.RowToStructByPos[req])
	want := []req{{"d-1", 1, 202}, {"d-2", 1, 500}, {"d-2", 2, 202}}
	if len(reqs) != 3 || reqs[0] != want[0] || reqs[1] != want[1] || reqs[2] != want[2] {
		t.Errorf("requests %+v, want %+v", reqs, want)
	}
	h.until("the container is ended", func() bool { return lux.Terminated(h.luxState(r.LuxRunID)) })
}

// A real lux's preview listener answers its own 401 sign-in page to any
// request without its cookie, which a preview ticket spent on /.lux/auth
// sets. A Server run signs in before its first request, so the event is
// never sent into the sign-in page; when lux stops taking the cookie, it
// signs in again and the app still gets each event once.
func TestAutomationServerSignsInToItsLuxPreview(t *testing.T) {
	h := newHarness(t)
	var mu sync.Mutex
	var posts []string
	h.serve(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			mu.Lock()
			posts = append(posts, r.Header.Get("X-GitHub-Delivery"))
			mu.Unlock()
		}
		w.WriteHeader(200)
	}))
	h.automation("aut_preview", "server", map[string]any{"kind": "github", "event": "pull_request"},
		map[string]any{"server": map[string]any{"command": "serve", "port": 8080, "path": "/events", "health": "/health"}})
	h.deliver("d-1", "pull_request", prBody("opened", "acme/web", "ana"))
	h.until("the first POST answered 2xx", func() bool { mu.Lock(); defer mu.Unlock(); return len(posts) == 1 })
	h.runner.Wait()
	luxRun := h.runs("aut_preview")[0].LuxRunID
	if calls := h.fake.PreviewCalls(luxRun); len(calls) < 2 || calls[0] != "sign-in" {
		t.Errorf("the preview's calls %q, want a sign-in before the first request", calls)
	}
	if refused := h.fake.PreviewCalls(""); len(refused) != 0 {
		t.Errorf("lux refused %q: a request went out before signing in", refused)
	}
	// lux no longer takes the cookie it gave: the next event signs in again.
	h.fake.ForgetPreviewCookies()
	h.deliver("d-2", "pull_request", prBody("synchronize", "acme/web", "ana"))
	h.until("the second event POSTed", func() bool {
		var posted int
		_ = h.owner.QueryRow(context.Background(), `SELECT posted FROM automation_runs WHERE automation_id = 'aut_preview'`).Scan(&posted)
		return posted == 2
	})
	h.runner.Wait()
	r := h.runs("aut_preview")
	if len(r) != 1 || r[0].Status != "running" {
		t.Fatalf("runs: %+v", r)
	}
	rows, _ := h.owner.Query(context.Background(), `SELECT delivery_id, try, http_status FROM automation_run_requests WHERE run_id = $1 ORDER BY at, try`, r[0].ID)
	type req struct {
		Delivery string
		Try      int
		Status   int
	}
	reqs, _ := pgx.CollectRows(rows, pgx.RowToStructByPos[req])
	want := []req{{"d-1", 1, 200}, {"d-2", 1, 200}}
	if len(reqs) != 2 || reqs[0] != want[0] || reqs[1] != want[1] {
		t.Errorf("requests %+v, want %+v", reqs, want)
	}
	mu.Lock()
	defer mu.Unlock()
	if strings.Join(posts, ",") != "d-1,d-2" {
		t.Errorf("the app got %q, want each event once", posts)
	}
	if n := count(h.fake.PreviewCalls(luxRun), "sign-in"); n != 2 {
		t.Errorf("signed in %d times, want once at the start and once after lux refused the cookie", n)
	}
	if n := count(h.fake.CallsOf(luxRun), "ticket preview"); n != 2 {
		t.Errorf("%d tickets minted, want 2", n)
	}
}

// An app that answers 401 itself (it checks a signature or its own auth)
// has answered: the event is POSTed to it once, no ticket is minted for
// it, and the request row says 401. Its health answering 401 mints no
// ticket either, sweep after sweep, until the run fails unhealthy.
func TestAutomationAnAppsOwn401IsItsAnswer(t *testing.T) {
	h := newHarness(t)
	var posts atomic.Int32
	h.serve(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			posts.Add(1)
		}
		w.WriteHeader(401)
	}))
	h.automation("aut_refuses", "server", map[string]any{"kind": "github", "event": "pull_request"},
		map[string]any{"server": map[string]any{"command": "serve", "port": 8080, "path": "/events", "health": nil}})
	h.deliver("d-1", "pull_request", prBody("opened", "acme/web", "ana"))
	h.until("the event is POSTed", func() bool {
		var posted int
		_ = h.owner.QueryRow(context.Background(), `SELECT posted FROM automation_runs WHERE automation_id = 'aut_refuses'`).Scan(&posted)
		return posted == 1
	})
	h.runner.Wait()
	// Signed in by now: the next event goes with the cookie lux takes.
	h.deliver("d-2", "pull_request", prBody("synchronize", "acme/web", "ana"))
	h.until("the second event is POSTed", func() bool {
		var posted int
		_ = h.owner.QueryRow(context.Background(), `SELECT posted FROM automation_runs WHERE automation_id = 'aut_refuses'`).Scan(&posted)
		return posted == 2
	})
	h.runner.Wait()
	run := h.runs("aut_refuses")[0]
	if n := posts.Load(); n != 2 {
		t.Errorf("the app got two events %d times", n)
	}
	var reqs []string
	rows, _ := h.owner.Query(context.Background(), `SELECT delivery_id || ':' || try || ':' || http_status FROM automation_run_requests WHERE run_id = $1 ORDER BY at`, run.ID)
	reqs, _ = pgx.CollectRows(rows, pgx.RowTo[string])
	if strings.Join(reqs, ",") != "d-1:1:401,d-2:1:401" {
		t.Errorf("request rows %q, want one 401 per event", reqs)
	}
	if n := count(h.fake.CallsOf(run.LuxRunID), "ticket preview"); n != 1 {
		t.Errorf("%d tickets minted, want only the first request's sign-in", n)
	}

	// Its health answers 401: a minute of sweeps mints no more tickets.
	h.automation("aut_unwell", "server", map[string]any{"kind": "github", "event": "push"},
		map[string]any{"server": map[string]any{"command": "serve", "port": 8080, "path": "/events", "health": "/health"}})
	h.deliver("d-push", "push", map[string]any{"ref": "refs/heads/main", "repository": map[string]any{"full_name": "acme/web"}})
	h.until("the unwell server's container starts", func() bool { r := h.runs("aut_unwell"); return len(r) == 1 && r[0].LuxRunID != "" })
	unwell := h.runs("aut_unwell")[0]
	for range 61 {
		if _, err := h.runner.Sweep(context.Background()); err != nil {
			t.Fatal(err)
		}
		h.runner.Wait()
		h.advance(time.Second)
	}
	h.until("the unwell run fails", func() bool { r := h.runs("aut_unwell"); return r[0].Status == "failed" })
	if r := h.runs("aut_unwell")[0]; r.Reason != "its server was not healthy within 60 seconds" {
		t.Errorf("unwell run: %+v", r)
	}
	if n := count(h.fake.CallsOf(unwell.LuxRunID), "ticket preview"); n != 1 {
		t.Errorf("%d tickets minted over a minute of 401s from the app's health, want 1", n)
	}
	if n := posts.Load(); n != 2 {
		t.Errorf("the app got %d POSTs in all", n)
	}
}

// Webhook only: one signed POST per firing, never queued or merged (Q13).
func TestAutomationWebhookSignsEachPost(t *testing.T) {
	h := newHarness(t)
	var mu sync.Mutex
	var sigs, bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		sigs, bodies = append(sigs, r.Header.Get("X-Dude-Signature-256")), append(bodies, string(b))
		mu.Unlock()
		w.WriteHeader(200)
	}))
	t.Cleanup(srv.Close)
	h.automation("aut_hook", "webhook", map[string]any{"kind": "github", "event": "release"},
		map[string]any{"webhook": map[string]any{"url": srv.URL + "/hook"}, "webhook_secret": "s3cret-signing-key", "concurrency": "skip"})
	h.deliver("d-a", "release", map[string]any{"action": "published", "repository": map[string]any{"full_name": "acme/web"}})
	h.deliver("d-b", "release", map[string]any{"action": "published", "repository": map[string]any{"full_name": "acme/web"}})
	h.until("two runs succeed", func() bool {
		r := h.runs("aut_hook")
		return len(r) == 2 && r[0].Status == "succeeded" && r[1].Status == "succeeded"
	})
	mu.Lock()
	defer mu.Unlock()
	for i := range bodies {
		if sigs[i] != Sign("s3cret-signing-key", []byte(bodies[i])) {
			t.Errorf("POST %d signature %q does not sign its body", i, sigs[i])
		}
	}
	if len(bodies) != 2 {
		t.Errorf("%d POSTs, want one per firing", len(bodies))
	}
}

// The signature is GitHub's scheme: sha256= and the hex HMAC-SHA256 of the body.
func TestSignIsHMACSHA256OfTheBody(t *testing.T) {
	// printf '{"a":1}' | openssl dgst -sha256 -hmac key
	if got := Sign("key", []byte(`{"a":1}`)); got != "sha256=88a67f24bbcdaed0e6c997404bb79a743baf44c6bab2f4c27328e3009d22e342" {
		t.Errorf("got %s", got)
	}
}

// Skip records a skipped run with its reason; merge hands the waiting run
// every payload, as a list (Q2), in its event file.
func TestAutomationConcurrencyRows(t *testing.T) {
	h := newHarness(t)
	g := newGate(t)
	t.Cleanup(func() { g.release(t) })
	h.automation("aut_skip", "command", map[string]any{"kind": "github", "event": "push"},
		map[string]any{"command": g.hold("echo done"), "concurrency": "skip"})
	h.automation("aut_merge", "command", map[string]any{"kind": "github", "event": "push"},
		map[string]any{"command": g.hold(`cat "$DUDE_EVENT_PATH"`), "concurrency": "merge"})
	push := map[string]any{"ref": "refs/heads/main", "repository": map[string]any{"full_name": "acme/web"}}
	h.deliver("p-1", "push", push)
	h.until("both running", func() bool {
		a, b := h.runs("aut_skip"), h.runs("aut_merge")
		return len(a) == 1 && a[0].Status == "running" && len(b) == 1 && b[0].Status == "running"
	})
	h.deliver("p-2", "push", push)
	h.deliver("p-3", "push", push)
	h.until("the deliveries are read", func() bool { return len(h.runs("aut_skip")) == 3 })
	skip := h.runs("aut_skip")
	if skip[1].Status != "skipped" || skip[1].Reason != "a run was still going (Concurrency: skip)" || skip[2].Status != "skipped" {
		t.Errorf("skip: %+v", skip)
	}
	merge := h.runs("aut_merge")
	if len(merge) != 2 || merge[1].Payloads != 2 {
		t.Fatalf("merge: %+v", merge)
	}
	// The merged run's event file is the list of both payloads.
	g.release(t)
	h.until("the merged run ends", func() bool { r := h.runs("aut_merge"); return r[1].Status == "succeeded" })
	log := h.runs("aut_merge")[1].Log
	var list []Payload
	if err := json.Unmarshal([]byte(strings.TrimSpace(log)), &list); err != nil || len(list) != 2 || list[0].DeliveryID != "p-2" || list[1].DeliveryID != "p-3" {
		t.Errorf("merged run's event file %q: %v", log, err)
	}
}

// hangTier is a model tier whose agent stays busy until stopped.
func (h *harness) hangTier() string {
	id := h.org + "_hang"
	exec(h.t, h.owner, `INSERT INTO model_tiers (id, organization_id, name, model) VALUES ($1, $2, 'Hang', 'fake/hang') ON CONFLICT DO NOTHING`, id, h.org)
	return id
}

// Concurrency cancel: a newer event cancels the live run and its lux Run
// ends with it; for a resumable agent the kept session is stopped, not
// ended, and the new run resumes it.
func TestAutomationConcurrencyCancel(t *testing.T) {
	h := newHarness(t)
	g := newGate(t)
	push := map[string]any{"ref": "refs/heads/main", "repository": map[string]any{"full_name": "acme/web"}}
	h.automation("aut_cancel", "command", map[string]any{"kind": "github", "event": "push"},
		map[string]any{"command": g.hold("echo released"), "concurrency": "cancel"})
	h.automation("aut_cancel_kept", "agent", map[string]any{"kind": "github", "event": "push"},
		map[string]any{"concurrency": "cancel", "session_mode": "resumable", "expiry_days": 7,
			"agent": map[string]any{"own": map[string]any{"tierId": h.hangTier(), "prompt": "Look at {{event.delivery_id}}"}}})
	h.deliver("c-1", "push", push)
	h.until("both running", func() bool {
		a, b := h.runs("aut_cancel"), h.runs("aut_cancel_kept")
		return len(a) == 1 && a[0].Status == "running" && len(b) == 1 && b[0].Status == "running"
	})
	firstCmd, firstAgent := h.runs("aut_cancel")[0], h.runs("aut_cancel_kept")[0]
	h.deliver("c-2", "push", push)
	h.until("the second runs are running", func() bool {
		a, b := h.runs("aut_cancel"), h.runs("aut_cancel_kept")
		return len(a) == 2 && a[1].Status == "running" && len(b) == 2 && b[1].Status == "running"
	})
	a, b := h.runs("aut_cancel"), h.runs("aut_cancel_kept")
	if a[0].Status != "cancelled" || a[0].Reason != "a newer event restarted it (Concurrency: cancel)" || a[1].LuxRunID == firstCmd.LuxRunID {
		t.Errorf("ephemeral cancel: %+v", a)
	}
	h.until("the cancelled command's container is ended", func() bool { return lux.Terminated(h.luxState(firstCmd.LuxRunID)) })
	if b[0].Status != "cancelled" || b[1].Session != "resumed" || b[1].LuxRunID != firstAgent.LuxRunID {
		t.Errorf("resumable cancel: %+v", b)
	}
	if in := h.fake.ResumeInputs(firstAgent.LuxRunID); len(in) != 1 || !strings.Contains(in[0], "Look at c-2") {
		t.Errorf("resume inputs %q", in)
	}
	g.release(t)
}

// Concurrency send: an event arriving while the agent works is given to
// that agent as input, with the automation's prompt for that event, once.
func TestAutomationConcurrencySendReachesTheAgent(t *testing.T) {
	h := newHarness(t)
	h.automation("aut_send", "agent", map[string]any{"kind": "github", "event": "push"},
		map[string]any{"concurrency": "send", "session_mode": "resumable", "expiry_days": 7,
			"agent": map[string]any{"own": map[string]any{"tierId": h.hangTier(), "prompt": "Look at {{event.delivery_id}}"}}})
	push := map[string]any{"ref": "refs/heads/main", "repository": map[string]any{"full_name": "acme/web"}}
	h.deliver("s-1", "push", push)
	h.until("running", func() bool { r := h.runs("aut_send"); return len(r) == 1 && r[0].Status == "running" })
	run := h.runs("aut_send")[0]
	h.deliver("s-2", "push", push)
	requestID := "automation-" + run.ID + "-1"
	h.until("the second event reaches the agent", func() bool { return len(h.fake.InputBodies(run.LuxRunID, requestID)) > 0 })
	for range 3 {
		if _, err := h.runner.Sweep(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	h.runner.Wait()
	bodies := h.fake.InputBodies(run.LuxRunID, requestID)
	if len(bodies) != 1 || !strings.Contains(bodies[0], "Look at s-2") || strings.Contains(bodies[0], "s-1") {
		t.Errorf("input bodies %q", bodies)
	}
	if r := h.runs("aut_send"); len(r) != 1 || r[0].Payloads != 2 {
		t.Errorf("runs %+v", r)
	}
	var sent int
	if err := h.owner.QueryRow(context.Background(), `SELECT inputs_sent FROM automation_runs WHERE id = $1`, run.ID).Scan(&sent); err != nil || sent != 2 {
		t.Errorf("inputs_sent %d %v", sent, err)
	}
}

// A kept session that stays busy (resume answers 409) holds the next run
// for ResumeConflictMax at most: then the kept Run is ended and the run
// starts fresh, saying so.
func TestAutomationResumeConflictIsBounded(t *testing.T) {
	h := newHarness(t)
	h.automation("aut_busy", "agent", map[string]any{"kind": "github", "event": "push"},
		map[string]any{"session_mode": "resumable", "expiry_days": 7,
			"agent": map[string]any{"own": map[string]any{"tierId": h.hangTier(), "prompt": "Look"}}})
	push := map[string]any{"ref": "refs/heads/main", "repository": map[string]any{"full_name": "acme/web"}}
	h.deliver("b-1", "push", push)
	h.until("running", func() bool { r := h.runs("aut_busy"); return len(r) == 1 && r[0].Status == "running" })
	first := h.runs("aut_busy")[0]
	// The run ends in dude while its lux Run is still busy: the kept
	// session cannot be resumed.
	exec(t, h.owner, `UPDATE automation_runs SET status = 'succeeded', ended_at = now() WHERE id = $1`, first.ID)
	h.deliver("b-2", "push", push)
	h.until("the second run tried to resume", func() bool {
		var since *time.Time
		r := h.runs("aut_busy")
		if len(r) != 2 {
			return false
		}
		_ = h.owner.QueryRow(context.Background(), `SELECT resume_conflict_since FROM automation_runs WHERE id = $1`, r[1].ID).Scan(&since)
		return since != nil
	})
	if _, err := h.runner.Sweep(context.Background()); err != nil {
		t.Fatal(err)
	}
	if r := h.runs("aut_busy"); r[1].Status != "queued" {
		t.Fatalf("within the bound: %+v", r[1])
	}
	h.advance(ResumeConflictMax + time.Second)
	h.until("the second run starts fresh", func() bool { r := h.runs("aut_busy"); return r[1].Status == "running" })
	second := h.runs("aut_busy")[1]
	if second.Session != "new" || second.LuxRunID == first.LuxRunID ||
		second.Reason != "its kept session was still busy after 2m0s, so it started fresh" {
		t.Errorf("second run %+v", second)
	}
	h.until("the busy kept Run is ended", func() bool { return lux.Terminated(h.luxState(first.LuxRunID)) })
}
