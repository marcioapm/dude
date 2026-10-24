package automations

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/marciomartins/dude/orchestrator/internal/fakegithub"
	"github.com/marciomartins/dude/orchestrator/internal/forge"
	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// keptServer gives the harness a resumable Server automation checking out
// the project's repository with the dude App's tokens, runs it once and
// waits for its container to be stopped and kept. Each token the App
// issues lasts 5 minutes, under forge's refresh margin, so every token
// dude asks for is minted: the App's count is dude's asks.
func keptServer(t *testing.T, h *harness, id string) (*fakegithub.App, runRow) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	app := fakegithub.NewApp(9001, "dude-acme", &key.PublicKey)
	app.Set(func(a *fakegithub.App) { a.Installations["acme"], a.TokenLifetime = 71, 5*time.Minute })
	srv := httptest.NewServer(app.Handler())
	t.Cleanup(srv.Close)
	pemKey := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	h.runner.Forges = oneForge{forge.NewGitHub(forge.Credential{Auth: "github_app", Secret: string(pemKey), APIBaseURL: srv.URL, AppID: "9001"})}
	h.serve(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }))
	h.automation(id, "server", map[string]any{"kind": "github", "event": "pull_request"},
		map[string]any{"session_mode": "resumable", "expiry_days": 7,
			"server": map[string]any{"command": "serve", "port": 8080, "path": "/events", "health": nil}})
	exec(t, h.owner, `UPDATE automations SET repos = '"all"' WHERE id = $1`, id)
	h.deliver("d-1", "pull_request", prBody("opened", "acme/web", "ana"))
	h.until("the first event is POSTed", func() bool {
		var posted int
		_ = h.owner.QueryRow(context.Background(), `SELECT posted FROM automation_runs WHERE automation_id = $1`, id).Scan(&posted)
		return posted == 1
	})
	h.runner.Wait()
	h.advance(ServerIdle + time.Second)
	h.until("the first run ends idle", func() bool { r := h.runs(id); return len(r) == 1 && r[0].Status == "succeeded" })
	first := h.runs(id)[0]
	h.until("the kept container is stopped", func() bool { return h.luxState(first.LuxRunID) == "stopped" })
	return app, first
}

// A resume mints each forge token once, from the kept Run's stored spec:
// the rest of what it gives again mints none.
func TestAutomationResumeMintsItsTokensOnce(t *testing.T) {
	h := newHarness(t)
	app, first := keptServer(t, h, "aut_mint")
	before := app.ExchangesFor(71)
	h.deliver("d-2", "pull_request", prBody("synchronize", "acme/web", "bo"))
	h.until("the second run resumes", func() bool { r := h.runs("aut_mint"); return len(r) == 2 && r[1].Session != "" })
	if r := h.runs("aut_mint")[1]; r.Session != "resumed" || r.LuxRunID != first.LuxRunID {
		t.Fatalf("second run %+v", r)
	}
	if n := app.ExchangesFor(71) - before; n != 1 {
		t.Errorf("the resume minted %d installation tokens, want 1", n)
	}
	h.runner.Wait()
}

// What fails a fresh start as well (its image has no published version)
// fails the run and keeps the session: its container is neither ended nor
// forgotten, and the next firing, the image fixed, resumes it.
func TestAutomationResumeThatCannotRunKeepsTheSession(t *testing.T) {
	h := newHarness(t)
	_, first := keptServer(t, h, "aut_build")
	exec(t, h.owner, `INSERT INTO images (id, organization_id, name) VALUES ($1 || '_img_new', $1, 'new')`, h.org)
	exec(t, h.owner, `UPDATE automations SET image_id = $1 || '_img_new' WHERE id = 'aut_build'`, h.org)
	h.runner.Agent.Layer = "registry.example.com/dude-layer:1"
	h.deliver("d-2", "pull_request", prBody("synchronize", "acme/web", "bo"))
	h.until("the second run fails", func() bool { r := h.runs("aut_build"); return len(r) == 2 && r[1].Status == "failed" })
	if r := h.runs("aut_build")[1]; r.Reason != "its image new has no published version: build and publish one first" || r.LuxRunID != "" {
		t.Errorf("second run %+v", r)
	}
	var kept string
	if err := h.owner.QueryRow(context.Background(), `SELECT lux_run_id FROM automation_sessions WHERE automation_id = 'aut_build'`).Scan(&kept); err != nil || kept != first.LuxRunID {
		t.Fatalf("kept session %q %v, want %s", kept, err, first.LuxRunID)
	}
	if s := h.luxState(first.LuxRunID); s != "stopped" {
		t.Errorf("the kept container is %s", s)
	}
	exec(t, h.owner, `UPDATE automations SET image_id = NULL WHERE id = 'aut_build'`)
	h.deliver("d-3", "pull_request", prBody("synchronize", "acme/web", "cy"))
	h.until("the third run resumes", func() bool { r := h.runs("aut_build"); return len(r) == 3 && r[2].Session != "" })
	if r := h.runs("aut_build")[2]; r.Session != "resumed" || r.LuxRunID != first.LuxRunID {
		t.Errorf("third run %+v", r)
	}
	h.runner.Wait()
}

// A kept container lux will not resume is ended before the run starts
// fresh: no stopped Run is left on lux with no session naming it.
func TestAutomationUnresumableSessionIsEndedNotOrphaned(t *testing.T) {
	h := newHarness(t)
	_, first := keptServer(t, h, "aut_orphan")
	h.runner.Lux = refuseResume{h.lux}
	h.deliver("d-2", "pull_request", prBody("synchronize", "acme/web", "bo"))
	h.until("the second run starts fresh", func() bool { r := h.runs("aut_orphan"); return len(r) == 2 && r[1].Session != "" })
	if r := h.runs("aut_orphan")[1]; r.Session != "new" || r.LuxRunID == first.LuxRunID {
		t.Errorf("second run %+v", r)
	}
	h.until("the unresumable container is ended", func() bool { return lux.Terminated(h.luxState(first.LuxRunID)) })
	h.runner.Wait()
}

// An agent on a real model declares its harness's secrets (the LLM key,
// and Codex's config.toml): a resume gives each again, so the next firing
// takes up the kept session on each harness rather than starting fresh.
func TestAutomationAgentResumeGivesItsHarnessSecrets(t *testing.T) {
	h := newHarness(t)
	h.runner.Agent.LLMKey = "sk-acme"
	h.runner.Agent.LLMURL = "https://llm.example.com/v1"
	exec(t, h.owner, `INSERT INTO model_tiers (id, organization_id, name, model) VALUES ($1 || '_large', $1, 'Large', 'acme-large')`, h.org)
	want := map[string][]string{
		"opencode":    {"DUDE_LLM_KEY"},
		"claude-code": {"ANTHROPIC_API_KEY"},
		"codex":       {"CODEX_CONFIG", "OPENAI_API_KEY"},
	}
	for harness := range want {
		id := "aut_h_" + strings.ReplaceAll(harness, "-", "_")
		h.automation(id, "agent", map[string]any{"kind": "github", "event": "pull_request"},
			map[string]any{"session_mode": "resumable", "expiry_days": 7,
				"agent": map[string]any{"own": map[string]any{"tierId": h.org + "_large", "harness": harness, "prompt": "Triage {{pr.title}}."}}})
	}
	h.deliver("d-1", "pull_request", prBody("opened", "acme/web", "ana"))
	first := map[string]runRow{}
	for harness := range want {
		id := "aut_h_" + strings.ReplaceAll(harness, "-", "_")
		h.until(id+"'s first run ends", func() bool { r := h.runs(id); return len(r) == 1 && r[0].Status != "running" })
		first[harness] = h.runs(id)[0]
		h.until(id+"'s kept container is stopped", func() bool { return h.luxState(first[harness].LuxRunID) == "stopped" })
	}
	h.deliver("d-2", "pull_request", prBody("synchronize", "acme/web", "bo"))
	for harness, names := range want {
		id := "aut_h_" + strings.ReplaceAll(harness, "-", "_")
		h.until(id+"'s second run starts", func() bool { rs := h.runs(id); return len(rs) == 2 && rs[1].Session != "" })
		second := h.runs(id)[1]
		if second.Session != "resumed" || second.LuxRunID != first[harness].LuxRunID {
			t.Errorf("%s: second run %+v, want the kept %s resumed", harness, second, first[harness].LuxRunID)
			continue
		}
		resumes := h.fake.ResumeSecretsOf(second.LuxRunID)
		if len(resumes) != 1 {
			t.Fatalf("%s: %d resumes, want 1", harness, len(resumes))
		}
		given := map[string]string{}
		for _, s := range resumes[0] {
			given[s.Name] = s.Value
		}
		for _, n := range names {
			switch {
			case n == "CODEX_CONFIG":
				if !strings.Contains(given[n], "https://llm.example.com/v1") {
					t.Errorf("%s: the resume gave CODEX_CONFIG %q", harness, given[n])
				}
			case given[n] != "sk-acme":
				t.Errorf("%s: the resume gave %s %q, want the LLM key", harness, n, given[n])
			}
		}
	}
	h.runner.Wait()
}

// failCancel is a lux whose cancel fails while down is set.
type failCancel struct {
	lux.Client
	down *atomic.Bool
}

func (f failCancel) Cancel(ctx context.Context, id string) error {
	if f.down.Load() {
		return &lux.Error{Status: 0, Code: "unreachable", Message: "dial tcp: connection refused"}
	}
	return f.Client.Cancel(ctx, id)
}

// Start fresh lets go of the session only once its container is ended: a
// failed cancel keeps the row naming the stopped Run, so trying again
// ends it rather than leaving it with nothing naming it.
func TestAutomationStartFreshKeepsTheSessionUntilItsCancelSucceeds(t *testing.T) {
	h := newHarness(t)
	_, first := keptServer(t, h, "aut_fresh")
	f := failCancel{Client: h.lux, down: new(atomic.Bool)}
	f.down.Store(true)
	h.runner.Lux = f
	kept := func() string {
		var id string
		_ = h.owner.QueryRow(context.Background(), `SELECT lux_run_id FROM automation_sessions WHERE automation_id = 'aut_fresh'`).Scan(&id)
		return id
	}
	if err := h.runner.StartFresh(context.Background(), h.org, "aut_fresh"); err == nil {
		t.Fatal("Start fresh with the cancel failing answered no error")
	}
	if got := kept(); got != first.LuxRunID {
		t.Fatalf("after a failed cancel the session names %q, want %s", got, first.LuxRunID)
	}
	if s := h.luxState(first.LuxRunID); s != "stopped" {
		t.Fatalf("the kept container is %s", s)
	}
	f.down.Store(false)
	if err := h.runner.StartFresh(context.Background(), h.org, "aut_fresh"); err != nil {
		t.Fatal(err)
	}
	if got := kept(); got != "" {
		t.Errorf("after the cancel the session still names %q", got)
	}
	h.until("the container is ended", func() bool { return lux.Terminated(h.luxState(first.LuxRunID)) })
	h.runner.Wait()
}

// refuseResume is a lux that will not resume any Run, as one refuses a Run
// whose snapshot is gone.
type refuseResume struct{ lux.Client }

func (refuseResume) Resume(context.Context, string, lux.ResumeInput) (lux.Run, error) {
	return lux.Run{}, &lux.Error{Status: http.StatusUnprocessableEntity, Code: "not_resumable", Message: "the run's snapshot is gone"}
}

// outage is a lux out of reach on resume (unreachable) or answering its
// Get with 503, while down is set.
type outage struct {
	lux.Client
	down     *atomic.Bool
	viaGet   bool
	attempts *atomic.Int32
}

func (o outage) Get(ctx context.Context, id string) (lux.Run, error) {
	if o.viaGet && o.down.Load() {
		o.attempts.Add(1)
		return lux.Run{}, &lux.Error{Status: http.StatusServiceUnavailable, Code: "unavailable", Message: "luxd is restarting"}
	}
	return o.Client.Get(ctx, id)
}

func (o outage) Resume(ctx context.Context, id string, in lux.ResumeInput) (lux.Run, error) {
	if !o.viaGet && o.down.Load() {
		o.attempts.Add(1)
		return lux.Run{}, &lux.Error{Status: 0, Code: "unreachable", Message: "dial tcp: connection refused"}
	}
	return o.Client.Resume(ctx, id, in)
}

// A lux out of reach, or answering 5xx, while a run resumes is not lux
// refusing the resume: the kept session and its stopped container stay,
// the run waits queued, and the next pass with lux back resumes it.
func TestAutomationResumeDuringALuxOutageKeepsTheSession(t *testing.T) {
	for _, viaGet := range []bool{false, true} {
		name := map[bool]string{false: "resume unreachable", true: "get 503"}[viaGet]
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			id := map[bool]string{false: "aut_out_resume", true: "aut_out_get"}[viaGet]
			_, first := keptServer(t, h, id)
			o := outage{Client: h.lux, down: new(atomic.Bool), viaGet: viaGet, attempts: new(atomic.Int32)}
			o.down.Store(true)
			h.runner.Lux = o
			h.deliver("d-2", "pull_request", prBody("synchronize", "acme/web", "bo"))
			for i := 0; i < 50 && o.attempts.Load() < 3; i++ {
				_, _ = h.runner.Sweep(context.Background())
				time.Sleep(10 * time.Millisecond)
			}
			if o.attempts.Load() < 3 {
				t.Fatalf("%d resume attempts during the outage, want 3", o.attempts.Load())
			}
			rs := h.runs(id)
			if len(rs) != 2 || rs[1].Status != "queued" || rs[1].Session != "" {
				t.Fatalf("runs during the outage: %+v", rs)
			}
			var kept string
			if err := h.owner.QueryRow(context.Background(), `SELECT lux_run_id FROM automation_sessions WHERE automation_id = $1`, id).Scan(&kept); err != nil || kept != first.LuxRunID {
				t.Fatalf("kept session %q %v, want %s", kept, err, first.LuxRunID)
			}
			if s := h.luxState(first.LuxRunID); s != "stopped" {
				t.Fatalf("the kept container is %s during the outage", s)
			}
			o.down.Store(false)
			h.until("the run resumes once lux is back", func() bool { r := h.runs(id); return len(r) == 2 && r[1].Session != "" })
			if r := h.runs(id)[1]; r.Session != "resumed" || r.LuxRunID != first.LuxRunID {
				t.Errorf("second run %+v, want the kept %s resumed", r, first.LuxRunID)
			}
			h.runner.Wait()
		})
	}
}
