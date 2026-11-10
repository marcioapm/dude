package delivery

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/dbtest"
	"github.com/marciomartins/dude/orchestrator/internal/fakegithub"
	"github.com/marciomartins/dude/orchestrator/internal/forge"
)

// gateFixture is a task with one pull request from feature into main on a
// fake GitHub over a real repository, and the rows the ready gate reads:
// the delivery, its Runs, dude's pushes, findings and questions. Each test
// builds the history it judges with its helpers.
type gateFixture struct {
	t     *testing.T
	ctx   context.Context
	org   string
	owner *pgx.Conn
	store *Store
	gh    *fakegithub.Server
	forge *forge.GitHub
	n     int
	st    State
	runs  int
}

func newGateFixture(t *testing.T) *gateFixture {
	t.Helper()
	app, owner := dbtest.Open(t)
	f := &gateFixture{t: t, ctx: context.Background(), org: dbtest.Org(t, owner), owner: owner, store: &Store{DB: app}}
	f.gh = fakegithub.New(bareRepo(t), "acme/repo")
	f.n = f.gh.OpenAsPerson("dude-bot", "feature", "main", "Greet")
	f.gh.Set(func(s *fakegithub.Server) { s.StatusID = 9 })
	mux := http.NewServeMux()
	mux.Handle("/repos/acme/repo", f.gh.Handler())
	mux.Handle("/repos/acme/repo/", f.gh.Handler())
	mux.Handle("POST /graphql", fakegithub.Graphql(f.gh))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	f.forge = forge.NewGitHub(forge.Credential{Auth: "pat", Secret: "ghp", APIBaseURL: srv.URL})
	p := DefaultPolicy()
	p.RequiredReviewers = []string{"correctness"}
	f.st = State{TaskID: "wi_g", ProjectID: "prj_g", Attempt: 1, Policy: p, Branch: "feature", Decider: DeciderConductor}
	for _, q := range []string{
		`INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES ('prj_g', $1, 'P', 'p', 'P')`,
		`INSERT INTO tasks (id, organization_id, project_id, number, title, goal, status)
			VALUES ('wi_g', $1, 'prj_g', 1, 'Greet', 'greet', 'review')`,
		`INSERT INTO repositories (id, organization_id, project_id, name, url, default_branch)
			VALUES ('repo_g', $1, 'prj_g', 'repo', 'https://github.com/acme/repo.git', 'main')`,
		`INSERT INTO people (id, organization_id, name) VALUES ('per_g_'||$1, $1, 'Ana')`,
	} {
		f.exec(q)
	}
	f.exec(`INSERT INTO pull_requests (id, organization_id, project_id, task_id, repository_id, number, url, head_branch,
		base_branch, head_sha, title, body, state, checks)
		VALUES ('pr_g', $1, 'prj_g', 'wi_g', 'repo_g', $2, 'https://github.com/acme/repo/pull/1', 'feature', 'main', $3, 'Greet', '', 'open', 'passing')`,
		f.n, f.gh.SHA("feature"))
	f.saveState()
	return f
}

func (f *gateFixture) exec(sql string, args ...any) {
	f.t.Helper()
	if _, err := f.owner.Exec(f.ctx, sql, append([]any{f.org}, args...)...); err != nil {
		f.t.Fatal(err)
	}
}

// saveState writes the delivery's state, waiting on its pull request.
func (f *gateFixture) saveState() {
	f.t.Helper()
	raw, _ := json.Marshal(f.st)
	f.exec(`INSERT INTO workflow_runs (id, organization_id, workflow_type, idempotency_key, status, step, state, task_id)
		VALUES ('wf_g', $1, 'task.delivery', 'g', 'waiting', 'awaitPullRequest', $2, 'wi_g')
		ON CONFLICT (id) DO UPDATE SET state = EXCLUDED.state`, string(raw))
}

// synced records the branch's head as the pull request's, as a sync does.
func (f *gateFixture) synced() string {
	head := f.gh.SHA("feature")
	f.exec(`UPDATE pull_requests SET head_sha = $2 WHERE id = 'pr_g' AND organization_id = $1`, head)
	return head
}

// run records a completed Run of the attempt; heads is runs.heads.
func (f *gateFixture) run(phase, category, startedAt string, heads map[string]any) string {
	f.t.Helper()
	f.runs++
	id := fmt.Sprintf("run_g%02d", f.runs)
	role := RoleForPhase[phase]
	h, _ := json.Marshal(heads)
	if heads == nil {
		h = []byte("{}")
	}
	var cat any
	if category != "" {
		cat = category
	}
	f.exec(`INSERT INTO runs (id, organization_id, project_id, task_id, attempt, status, phase, role, kind, category,
		base_refs, heads, created_at, ended_at)
		VALUES ($2, $1, 'prj_g', 'wi_g', 1, 'completed', $3::run_phase, $4::agent_role, 'agent', $5,
		jsonb_build_object('repo', $6::text), $7::jsonb, now() + $8::int * interval '1 ms', now() + $8::int * interval '1 ms')`,
		id, phase, role, cat, startedAt, string(h), f.runs*10)
	return id
}

// pushed records dude's push of head by run (git.commit_created).
func (f *gateFixture) pushed(run, base, head string, paths ...string) {
	f.t.Helper()
	p, _ := json.Marshal(map[string]any{"repo": "repo", "baseSha": base, "headSha": head, "branch": "feature", "changedPaths": paths})
	f.exec(`INSERT INTO events (id, organization_id, event_type, run_id, task_id, actor_type, actor_id, source, payload)
		VALUES ('ev_'||$2||'_'||$3, $1, 'git.commit_created', $2, 'wi_g', 'agent', $2, 'test', $4::jsonb)`, run, head[:8], string(p))
}

// implemented commits the implementer's change and records it; its head.
func (f *gateFixture) implemented(files map[string]string) string {
	f.t.Helper()
	start := f.gh.SHA("feature")
	head := f.gh.CommitFiles("feature", "Greet", files)
	var paths []string
	for p := range files {
		paths = append(paths, p)
	}
	f.pushed(f.run(PhaseImplement, "", start, map[string]any{"repo": map[string]any{"sha": head, "base": start}}), start, head, paths...)
	return head
}

// reviewed records a completed review at head by each category.
func (f *gateFixture) reviewed(head string, categories ...string) {
	if len(categories) == 0 {
		categories = []string{"correctness"}
	}
	for _, c := range categories {
		f.run(PhaseReview, c, head, nil)
	}
}

// simplifiedEmpty records a simplify Run from start that committed nothing.
func (f *gateFixture) simplifiedEmpty(start string) { f.run(PhaseSimplify, "", start, nil) }

// simplified commits the simplifier's change on the branch and records it;
// its head.
func (f *gateFixture) simplified() string {
	start := f.gh.SHA("feature")
	head := f.gh.CommitFiles("feature", "Simplify", map[string]string{"SIMPLE.md": "simple\n"})
	f.pushed(f.run(PhaseSimplify, "", start, map[string]any{"repo": map[string]any{"sha": head, "base": start}}), start, head, "SIMPLE.md")
	return head
}

// ready is a fully ready pull request: implemented, reviewed, simplified
// with nothing to change, synced, CI green.
func (f *gateFixture) ready() string {
	head := f.implemented(map[string]string{"app/greet.go": "package app\n"})
	f.reviewed(head)
	f.simplifiedEmpty(head)
	f.synced()
	return head
}

func (f *gateFixture) gate() PRGate {
	f.t.Helper()
	gates, err := f.store.ReadyGateFor(f.ctx, f.org, f.forge, "wi_g", nil)
	if err != nil || len(gates) != 1 {
		f.t.Fatalf("gate: %v %v", gates, err)
	}
	if gates[0].Err != nil {
		f.t.Fatalf("gate unread: %v", gates[0].Err)
	}
	if len(gates[0].Items) != len(ReadyKeys) {
		f.t.Fatalf("gate has %d items, want %d: %+v", len(gates[0].Items), len(ReadyKeys), gates[0].Items)
	}
	for i, it := range gates[0].Items {
		if it.Key != ReadyKeys[i] {
			f.t.Fatalf("item %d is %s, want %s", i, it.Key, ReadyKeys[i])
		}
	}
	return gates[0]
}

func (g PRGate) item(key string) GateItem {
	for _, it := range g.Items {
		if it.Key == key {
			return it
		}
	}
	return GateItem{}
}

// holds says the gate's item key holds (ok) with a detail containing want,
// and, when ok, that the gate is ready only if nothing else fails.
func holds(t *testing.T, g PRGate, key string, ok bool, want string) {
	t.Helper()
	it := g.item(key)
	if it.OK != ok || !strings.Contains(it.Detail, want) {
		t.Errorf("%s: ok %v %q, want ok %v containing %q\nall: %+v", key, it.OK, it.Detail, ok, want, g.Items)
	}
	if !ok && g.Ready {
		t.Errorf("ready with %s failing", key)
	}
}
