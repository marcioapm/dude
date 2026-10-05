package orchestrator_test

// Registering dude's webhook on an organization's repositories, as
// Settings → Connect GitHub asks: in the background, only where it is
// missing, and repaired a few at a time by the reconciler.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/api"
	"github.com/marciomartins/dude/orchestrator/internal/db"
	"github.com/marciomartins/dude/orchestrator/internal/dbtest"
	"github.com/marciomartins/dude/orchestrator/internal/forge"
	"github.com/marciomartins/dude/orchestrator/internal/prs"
)

// hookGitHub is GitHub's repository hooks API for any acme/* repository,
// each request taking `latency`, rate limited once `limit` hooks were
// created (0: never).
type hookGitHub struct {
	mu      sync.Mutex
	latency time.Duration
	limit   int
	created int
	hooks   map[string][]map[string]any // by repository
	calls   []string                    // "METHOD repo"
}

func (g *hookGitHub) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/acme/{repo}/hooks", g.serve)
	mux.HandleFunc("/repos/acme/{repo}/hooks/{id}", g.serve)
	return mux
}

func (g *hookGitHub) serve(w http.ResponseWriter, r *http.Request) {
	time.Sleep(g.latency)
	g.mu.Lock()
	defer g.mu.Unlock()
	repo := r.PathValue("repo")
	g.calls = append(g.calls, r.Method+" "+repo)
	w.Header().Set("Content-Type", "application/json")
	switch r.Method {
	case "GET":
		_ = json.NewEncoder(w).Encode(append([]map[string]any{}, g.hooks[repo]...))
	case "POST":
		if g.limit > 0 && g.created >= g.limit {
			w.Header().Set("X-RateLimit-Remaining", "0")
			w.Header().Set("X-RateLimit-Reset", fmt.Sprint(time.Now().Add(time.Minute).Unix()))
			w.WriteHeader(403)
			_, _ = w.Write([]byte(`{"message":"API rate limit exceeded"}`))
			return
		}
		var hook map[string]any
		_ = json.NewDecoder(r.Body).Decode(&hook)
		g.created++
		hook["id"] = g.created
		g.hooks[repo] = append(g.hooks[repo], hook)
		w.WriteHeader(201)
		_ = json.NewEncoder(w).Encode(hook)
	case "PATCH":
		_, _ = w.Write([]byte(`{}`))
	}
}

func (g *hookGitHub) callsSoFar() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]string(nil), g.calls...)
}

type hookWorld struct {
	t     *testing.T
	app   *db.DB
	owner *pgx.Conn
	org   string
	gh    *hookGitHub
	prs   *prs.Syncer
	api   string
}

const hookURL = "https://dude.example.com/v1/webhooks/github/org"

// hookingWorld is an organization with n GitHub repositories and no hooks.
func hookingWorld(t *testing.T, n int) *hookWorld {
	app, owner := dbtest.Open(t)
	w := &hookWorld{t: t, app: app, owner: owner, org: dbtest.Org(t, owner), gh: &hookGitHub{hooks: map[string][]map[string]any{}}}
	srv := httptest.NewServer(w.gh.handler())
	t.Cleanup(srv.Close)
	mustExec(t, owner, `INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES ($1, $2, 'P', $1, 'P')`, "prj_"+w.org, w.org)
	for i := range n {
		mustExec(t, owner, `INSERT INTO repositories (id, organization_id, project_id, name, url, default_branch)
			VALUES ($1, $2, $3, $4, $5, 'main')`, fmt.Sprintf("repo_%s_%02d", w.org, i), w.org, "prj_"+w.org,
			fmt.Sprintf("r%02d", i), fmt.Sprintf("https://github.test/acme/r%02d.git", i))
	}
	mustExec(t, owner, `INSERT INTO forge_credentials (id, organization_id, auth, secret, api_base_url, webhook_secret)
		VALUES ($1, $2, 'pat', 'ghp_test', $3, 'whsec')`, "forge_"+w.org, w.org, srv.URL)
	forges := forge.Resolver{DB: app}
	w.prs = &prs.Syncer{DB: app, Forges: forges, Log: quiet}
	apiSrv := httptest.NewServer((&api.Server{DB: app, Token: "svc", Log: quiet, Kick: func() {}, Forges: forges, PRs: w.prs}).Handler())
	t.Cleanup(apiSrv.Close)
	w.api = apiSrv.URL
	return w
}

func (w *hookWorld) register(body map[string]any) (int, time.Duration) {
	w.t.Helper()
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest("POST", w.api+"/internal/webhooks/register", bytes.NewReader(b))
	req.Header.Set("Authorization", "Bearer svc")
	req.Header.Set("X-Dude-Organization", w.org)
	req.Header.Set("Content-Type", "application/json")
	start := time.Now()
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		w.t.Fatal(err)
	}
	res.Body.Close()
	return res.StatusCode, time.Since(start)
}

func (w *hookWorld) healthy() int {
	var n int
	_ = w.owner.QueryRow(context.Background(), `SELECT count(*) FROM repositories WHERE organization_id = $1
		AND webhook_id IS NOT NULL AND webhook_registered_at IS NOT NULL AND webhook_error IS NULL`, w.org).Scan(&n)
	return n
}

func (w *hookWorld) until(what string, cond func() bool) {
	w.t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	w.t.Fatalf("timed out waiting for %s", what)
}

// Forty repositories, each GitHub call taking 100ms: connecting returns
// at once, and every repository is registered in the background. A
// reconnect asks GitHub nothing about the repositories already registered.
func TestConnectingRegistersWebhooksInTheBackgroundAndOnlyWhereMissing(t *testing.T) {
	w := hookingWorld(t, 40)
	w.gh.latency = 100 * time.Millisecond
	status, took := w.register(map[string]any{"url": hookURL, "background": true})
	if status != http.StatusAccepted || took > time.Second {
		t.Fatalf("connecting answered %d after %s: it must not wait on GitHub", status, took)
	}
	w.until("every repository registered", func() bool { return w.healthy() == 40 })
	if n := len(w.gh.callsSoFar()); n != 80 {
		t.Errorf("%d GitHub calls for 40 repositories, want a read and a create each", n)
	}

	// One repository's hook failed since; a reconnect mends only it.
	mustExec(t, w.owner, `UPDATE repositories SET webhook_error = 'gone' WHERE id = $1`, fmt.Sprintf("repo_%s_07", w.org))
	before := len(w.gh.callsSoFar())
	if status, _ := w.register(map[string]any{"url": hookURL, "background": true}); status != http.StatusAccepted {
		t.Fatalf("reconnecting: %d", status)
	}
	w.until("the failed one mended", func() bool { return w.healthy() == 40 })
	time.Sleep(300 * time.Millisecond)
	if calls := w.gh.callsSoFar()[before:]; strings.Join(calls, ",") != "GET r07,PATCH r07" {
		t.Errorf("a reconnect asked GitHub %v; want only r07's hook read and updated", calls)
	}
}

// A repository whose registration failed before attempts were timed (as
// migration 079 left it: an error, no attempt time) is due at once: after
// the token is replaced, both the repair and a reconnect register its hook.
func TestARegistrationFailedBeforeAttemptsWereTimedIsRetried(t *testing.T) {
	legacy := func(t *testing.T) *hookWorld {
		w := hookingWorld(t, 2)
		mustExec(t, w.owner, `UPDATE repositories SET webhook_error = 'github 403: missing permission',
			webhook_attempted_at = NULL WHERE organization_id = $1`, w.org)
		mustExec(t, w.owner, `UPDATE forge_credentials SET secret = 'ghp_replaced', public_url = 'https://dude.example.com',
			webhook_url = $2 WHERE organization_id = $1`, w.org, hookURL)
		return w
	}
	t.Run("repair", func(t *testing.T) {
		w := legacy(t)
		if err := w.prs.RepairWebhooks(context.Background()); err != nil {
			t.Fatal(err)
		}
		if n := w.healthy(); n != 2 {
			t.Errorf("the repair registered %d of 2 legacy failures; GitHub calls %v", n, w.gh.callsSoFar())
		}
	})
	t.Run("reconnect", func(t *testing.T) {
		w := legacy(t)
		if status, _ := w.register(map[string]any{"url": hookURL, "background": true}); status != http.StatusAccepted {
			t.Fatalf("reconnecting: %d", status)
		}
		w.until("both legacy failures registered", func() bool { return w.healthy() == 2 })
	})
}

// The reconciler's repair registers a few repositories per pass where an
// organization has a public URL and a repository has no healthy hook;
// GitHub's rate limit stops the pass, and the next pass goes on.
func TestTheRepairRegistersAFewPerPassAndStopsAtARateLimit(t *testing.T) {
	w := hookingWorld(t, 12)
	mustExec(t, w.owner, `UPDATE forge_credentials SET public_url = 'https://dude.example.com', webhook_url = $2
		WHERE organization_id = $1`, w.org, hookURL)
	w.gh.limit = 3
	if err := w.prs.RepairWebhooks(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := w.healthy(); n != 3 {
		t.Errorf("%d registered before the rate limit, want 3", n)
	}
	calls := w.gh.callsSoFar()
	if len(calls) != 8 || !strings.HasPrefix(calls[7], "POST ") {
		t.Errorf("the pass went on after the rate limit: %v", calls)
	}
	var stamped int
	_ = w.owner.QueryRow(context.Background(), `SELECT count(*) FROM repositories WHERE organization_id = $1
		AND webhook_error IS NOT NULL`, w.org).Scan(&stamped)
	if stamped != 0 {
		t.Errorf("%d repositories marked failed by a rate limit", stamped)
	}

	w.gh.mu.Lock()
	w.gh.limit = 0
	w.gh.mu.Unlock()
	if err := w.prs.RepairWebhooks(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := w.healthy(); n != 3+prs.RepairPerPass {
		t.Errorf("%d registered after the next pass, want %d", n, 3+prs.RepairPerPass)
	}
	if err := w.prs.RepairWebhooks(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := w.healthy(); n != 12 {
		t.Errorf("%d registered after the third pass, want all 12", n)
	}
	// Nothing is left to mend: a pass asks GitHub nothing.
	before := len(w.gh.callsSoFar())
	if err := w.prs.RepairWebhooks(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := len(w.gh.callsSoFar()) - before; n != 0 {
		t.Errorf("%d GitHub calls with every hook registered", n)
	}
}
