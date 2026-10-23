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
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Idle: 150 * time.Millisecond}
	h.runner.Now = func() time.Time { return time.Now().Add(time.Duration(h.skew.Load())) }
	// Background follows and POSTs end before the database and fake go.
	t.Cleanup(h.runner.Wait)
	return h
}

// advance moves the runner's clock on by d.
func (h *harness) advance(d time.Duration) { h.skew.Add(int64(d)) }

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
	got, err := h.lux.Get(context.Background(), id)
	if err != nil {
		h.t.Fatal(err)
	}
	return got.State
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
		id, err = Fire(ctx, tx, a, SourceEvent, payloads, "", nil)
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

	// After its expiry without a run, the kept session is cleaned up, whether on or off (Q10).
	exec(t, h.owner, `UPDATE automations SET enabled = false WHERE id = 'aut_triage'`)
	exec(t, h.owner, `UPDATE automation_sessions SET last_run_at = now() - interval '8 days'`)
	h.until("the kept session expires", func() bool {
		var n int
		_ = h.owner.QueryRow(context.Background(), `SELECT count(*) FROM automation_sessions`).Scan(&n)
		return n == 0
	})
	h.until("its container is ended", func() bool { return lux.Terminated(h.luxState(first.LuxRunID)) })
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
	h.automation("aut_slow", "command", map[string]any{"kind": "schedule", "cron": "0 3 * * *", "tz": "UTC"}, map[string]any{"command": "sleep 1"})
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
	h.until("the run ends", func() bool {
		r := h.runs("aut_cmd")
		return len(r) == 1 && r[0].Status != "running" && r[0].Status != "queued"
	})
	if r := h.runs("aut_cmd")[0]; r.Status != "succeeded" {
		t.Fatalf("run: %+v", r)
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
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	t.Cleanup(srv.Close)
	h.runner.ServerURL = func(context.Context, string, int) (string, error) { return srv.URL, nil }
	h.automation("aut_srv", "server", map[string]any{"kind": "github", "event": "pull_request"},
		map[string]any{"server": map[string]any{"command": "bun run serve", "port": 8080, "path": "/events", "health": nil}})
	h.deliver("d-1", "pull_request", prBody("opened", "acme/web", "ana"))
	h.until("the first POST", func() bool { mu.Lock(); defer mu.Unlock(); return len(got) == 1 })
	h.deliver("d-2", "pull_request", prBody("synchronize", "acme/web", "ana"))
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
// every payload, as a list (Q2).
func TestAutomationConcurrencyRows(t *testing.T) {
	h := newHarness(t)
	h.automation("aut_skip", "command", map[string]any{"kind": "github", "event": "push"},
		map[string]any{"command": "sleep 4", "concurrency": "skip"})
	h.automation("aut_merge", "command", map[string]any{"kind": "github", "event": "push"},
		map[string]any{"command": "sleep 4", "concurrency": "merge"})
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
		t.Errorf("merge: %+v", merge)
	}
}
