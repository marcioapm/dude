package orchestrator_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// A session linking a project whose key and repository name lux would
// refuse as a spec name — an uppercase key, a name past 32 characters —
// starts: its spec names the checkout as lux takes it, at the same
// repos/<KEY>/<name> path; the agent is told that path; and a repository
// linked later, under the same kind of name, is added by the resume and
// held by the Run under the name the spec gave it.
func TestASessionLinkingAnyProjectGetsAValidSpec(t *testing.T) {
	s := newSessionWorld(t)
	const key, long = "BILL", "Billing-API.payments-ledger-service"
	mustExec(t, s.owner, `UPDATE projects SET key_prefix = $2 WHERE id = $1`, s.project, key)
	mustExec(t, s.owner, `UPDATE repositories SET name = $2 WHERE id = $1`, s.repoID, long)
	mustExec(t, s.owner, `UPDATE repositories SET name = 'Web' WHERE id = $1`, s.webRepo)
	id := s.ok(s.marcio, "POST", "/internal/sessions", map[string]any{"title": "Billing",
		"projects": []map[string]any{{"projectId": s.project, "repositoryIds": []string{s.repoID}}}})["id"].(string)
	s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/chat", map[string]any{"text": "where does metering go?"})
	run := s.started(id)

	v := s.luxRun(run)
	if v == nil || v.spec.Git == nil || len(v.spec.Git.Repositories) != 1 {
		t.Fatalf("spec %+v", v)
	}
	got := v.spec.Git.Repositories[0]
	want := delivery.SessionRepo{Key: key, Name: long}.SpecName()
	if got.Name != want || !lux.NameRe.MatchString(got.Name) || !strings.HasPrefix(got.Name, "bill-billing-api-") {
		t.Errorf("spec name %q, want %q, valid for lux", got.Name, want)
	}
	if got.Path != "/workspace/repos/BILL/"+long {
		t.Errorf("checked out at %s", got.Path)
	}
	// The briefing (the scripted agent's spec carries its script instead).
	if n := s.count(`SELECT count(*) FROM runs WHERE id = $1 AND strpos(prompt, $2) > 0`, run, "`/workspace/repos/BILL/"+long+"`"); n != 1 {
		t.Errorf("the agent is not told where its checkout is")
	}
	if n := s.count(`SELECT count(*) FROM runs WHERE id = $1 AND lux_repositories = ARRAY[$2]`, run, want); n != 1 {
		t.Errorf("the Run does not hold %s", want)
	}
	// Held already: nothing for a resume to bring.
	for range 3 {
		s.pump()
	}
	if v := s.luxRun(run); v.resumed != 0 {
		t.Errorf("resumed %d times for a repository it holds", v.resumed)
	}

	// Linked later: added by a resume, under a name lux takes, and settled.
	s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/link", map[string]any{"projects": []map[string]any{
		{"projectId": s.project, "repositoryIds": []string{s.repoID}},
		{"projectId": s.webProject, "repositoryIds": []string{s.webRepo}}}})
	web := delivery.SessionRepo{Key: "WC", Name: "Web"}.SpecName()
	s.until("the web checkout", func() bool {
		return s.count(`SELECT count(*) FROM runs WHERE id = $1 AND $2 = ANY (lux_repositories) AND status = 'running'`, run, web) == 1
	})
	v = s.luxRun(run)
	i := slices.IndexFunc(v.spec.Git.Repositories, func(r lux.Repository) bool { return r.Name == web })
	if i < 0 || v.spec.Git.Repositories[i].Path != "/workspace/repos/WC/Web" || !lux.NameRe.MatchString(web) {
		t.Errorf("added %+v, want %s at /workspace/repos/WC/Web", v.spec.Git.Repositories, web)
	}
	if n := s.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = 'repository.clone_failed'`, run); n != 0 {
		t.Errorf("%d clones failed", n)
	}
	if n := s.count(`SELECT count(*) FROM session_repositories WHERE session_id = $1`, id); n != 2 {
		t.Errorf("%d repositories linked, want both", n)
	}
	if _, status := s.brainstorm(id); status == "failed" {
		t.Errorf("the session's agent failed")
	}
}

// A session whose linked projects share a key — two "api" repositories
// under BILL would be one spec name and one checkout path — never reaches
// lux: its agent fails before submit, saying which projects and which key.
// The control plane refuses such links; this is the orchestrator's guard
// for any that get past it.
func TestASessionWhoseProjectsShareAKeyIsRefusedBeforeSubmit(t *testing.T) {
	s := newSessionWorld(t)
	mustExec(t, s.owner, `UPDATE projects SET key_prefix = 'BILL', name = 'Billing API' WHERE id = $1`, s.project)
	mustExec(t, s.owner, `UPDATE projects SET key_prefix = 'bill', name = 'Billing Worker' WHERE id = $1`, s.webProject)
	mustExec(t, s.owner, `UPDATE repositories SET name = 'api' WHERE id = ANY($1)`, []string{s.repoID, s.webRepo})
	id := s.ok(s.marcio, "POST", "/internal/sessions", map[string]any{"title": "Billing", "projects": []map[string]any{
		{"projectId": s.project, "repositoryIds": []string{s.repoID}},
		{"projectId": s.webProject, "repositoryIds": []string{s.webRepo}}}})["id"].(string)
	s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/chat", map[string]any{"text": "where does metering go?"})
	var run string
	s.until("the session's agent to fail", func() bool {
		var status string
		run, status = s.brainstorm(id)
		return status == "failed"
	})
	want := "Billing API and Billing Worker both use the key BILL; a session tells its projects apart by key, so link one of them."
	if n := s.count(`SELECT count(*) FROM runs WHERE id = $1 AND error = $2 AND lux_run_id IS NULL`, run, want); n != 1 {
		var why string
		_ = s.owner.QueryRow(context.Background(), `SELECT COALESCE(error, '') FROM runs WHERE id = $1`, run).Scan(&why)
		t.Errorf("failed with %q, want %q and no lux Run", why, want)
	}
	if v := s.luxRun(run); v != nil {
		t.Errorf("submitted to lux: %+v", v.spec.Git)
	}
}

// A session linking two projects with distinct keys starts, each project's
// checkout under its own key.
func TestASessionWhoseProjectsHaveDistinctKeysStarts(t *testing.T) {
	s := newSessionWorld(t)
	mustExec(t, s.owner, `UPDATE repositories SET name = 'api' WHERE id = ANY($1)`, []string{s.repoID, s.webRepo})
	id := s.ok(s.marcio, "POST", "/internal/sessions", map[string]any{"title": "Billing", "projects": []map[string]any{
		{"projectId": s.project, "repositoryIds": []string{s.repoID}},
		{"projectId": s.webProject, "repositoryIds": []string{s.webRepo}}}})["id"].(string)
	s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/chat", map[string]any{"text": "where does metering go?"})
	v := s.luxRun(s.started(id))
	if v == nil || v.spec.Git == nil {
		t.Fatalf("spec %+v", v)
	}
	var paths []string
	for _, r := range v.spec.Git.Repositories {
		paths = append(paths, r.Path)
	}
	if !slices.Equal(paths, []string{"/workspace/repos/BL/api", "/workspace/repos/WC/api"}) {
		t.Errorf("checked out at %v", paths)
	}
}

// A task's Run names each of its project's repositories by lux_name, and
// no two can share one: the project's repositories are unique by it
// (migration 091), so whatever names a project holds, its Run's spec has
// distinct names and checkout paths, and lux takes it. Each name below is
// tried with its own rewritten name beside it; the database refuses
// exactly the ones that would share a checkout with one it holds.
func TestATaskRunsSpecNamesAreDistinctWhateverItsProjectsRepositoriesAreCalled(t *testing.T) {
	w := newWorld(t)
	var tried []string
	for _, n := range []string{"Target", "TARGET", "target.api", "Target.API", "target-api", "Web", "web", ".", "_x", "-x",
		"Ü-ß", strings.Repeat("A", 40), strings.Repeat("a", 33)} {
		tried = append(tried, n, lux.SpecName(n))
	}
	held := map[string]string{"target": "target"} // lux name → the repository holding it
	wi := w.task()
	mustExec(t, w.owner, `INSERT INTO task_repositories (organization_id, task_id, repository_id, access) VALUES ($1, $2, $3, 'write')`,
		w.org, wi, w.repoID)
	for i, name := range tried {
		id := fmt.Sprintf("repo_%d_%s", i, w.org)
		_, err := w.owner.Exec(context.Background(), `INSERT INTO repositories (id, organization_id, project_id, name, url, default_branch)
			VALUES ($1, $2, $3, $4, 'git://127.0.0.1/acme/target.git', 'main')`, id, w.org, w.project, name)
		other, clash := held[lux.SpecName(name)]
		// The same name twice is the older (project_id, name) key's to refuse.
		want := "repositories_lux_name_idx"
		if other == name {
			want = "repositories_project_id_name_key"
		}
		var pgErr *pgconn.PgError
		switch {
		case clash && !(errors.As(err, &pgErr) && pgErr.ConstraintName == want):
			t.Errorf("%q is checked out as %s, as %q is, and was not refused: %v", name, lux.SpecName(name), other, err)
		case !clash && err != nil:
			t.Errorf("%q refused: %v", name, err)
		case !clash:
			held[lux.SpecName(name)] = name
			mustExec(t, w.owner, `INSERT INTO task_repositories (organization_id, task_id, repository_id, access) VALUES ($1, $2, $3, 'read')`,
				w.org, wi, id)
		}
	}
	w.deliver(wi)
	var spec lux.Spec
	w.until("the implementer accepted by lux", func() bool {
		for _, r := range w.lux.Runs() {
			if err := json.Unmarshal(r.Spec, &spec); err == nil && spec.Labels["dude.phase"] == "implement" {
				return true
			}
		}
		if n := w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND status = 'failed'`, wi); n > 0 {
			var why string
			_ = w.owner.QueryRow(context.Background(), `SELECT error FROM runs WHERE task_id = $1 AND status = 'failed'`, wi).Scan(&why)
			t.Fatalf("the implementer failed: %s", why)
		}
		return false
	})
	names, paths := map[string]bool{}, map[string]bool{}
	for _, r := range spec.Git.Repositories {
		if names[r.Name] || paths[r.Path] || !lux.NameRe.MatchString(r.Name) {
			t.Errorf("repository %s at %s: invalid, or named or placed twice", r.Name, r.Path)
		}
		names[r.Name], paths[r.Path] = true, true
	}
	if len(names) != len(held) {
		t.Errorf("%d repositories in the spec, want the %d the project holds", len(names), len(held))
	}
}

// A task's repository named in a way lux refuses as a spec name (upper
// case, a dot) is delivered as any: every phase's spec names it as lux
// takes it, the checkout, push and fast-forward match it back, and what
// the ledger and the pull request say name the repository as the task does.
func TestATaskRepositoryLuxWouldRefuseByNameIsDelivered(t *testing.T) {
	w := newWorld(t)
	const name = "Target.API"
	mustExec(t, w.owner, `UPDATE repositories SET name = $2 WHERE id = $1`, w.repoID, name)
	wi := w.task()
	w.deliver(wi)
	w.until("a pull request", func() bool { return len(w.gh.Pulls()) == 1 })
	for _, r := range w.lux.Runs() {
		var spec lux.Spec
		_ = json.Unmarshal(r.Spec, &spec)
		if spec.Git == nil || len(spec.Git.Repositories) != 1 {
			t.Fatalf("%s: git %+v", spec.Labels["dude.phase"], spec.Git)
		}
		got := spec.Git.Repositories[0]
		if got.Name != lux.SpecName(name) || !lux.NameRe.MatchString(got.Name) || got.Path != "/workspace/repos/"+got.Name {
			t.Errorf("%s: repository %s at %s", spec.Labels["dude.phase"], got.Name, got.Path)
		}
	}
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'git.commit_created' AND payload->>'repo' = $2`,
		wi, name); n == 0 {
		t.Errorf("no commit recorded on %s", name)
	}
	if n := w.count(`SELECT count(*) FROM pull_requests WHERE task_id = $1 AND repository_id = $2`, wi, w.repoID); n != 1 {
		t.Errorf("%d pull requests on the repository", n)
	}
}
