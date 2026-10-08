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
	"github.com/marciomartins/dude/orchestrator/internal/fakelux"
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

// Two projects of a session are never under one key: the organisation's
// keys are unique ignoring case (projects_key_idx), so billing-worker
// cannot be BILL beside billing-api. Linked together, each holding a
// repository named api, they get distinct spec names and checkout paths,
// both under their own key, and the agent starts.
func TestTwoLinkedProjectsAlwaysGetDistinctSpecNamesAndPaths(t *testing.T) {
	s := newSessionWorld(t)
	mustExec(t, s.owner, `UPDATE projects SET key_prefix = 'BILL', name = 'Billing API' WHERE id = $1`, s.project)
	for _, key := range []string{"BILL", "bill", "Bill"} {
		_, err := s.owner.Exec(context.Background(), `UPDATE projects SET key_prefix = $2 WHERE id = $1`, s.webProject, key)
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "23505" || pgErr.ConstraintName != "projects_key_idx" {
			t.Fatalf("a second project keyed %s: %v, want projects_key_idx to refuse it", key, err)
		}
	}
	mustExec(t, s.owner, `UPDATE projects SET key_prefix = 'BWOR', name = 'Billing Worker' WHERE id = $1`, s.webProject)
	mustExec(t, s.owner, `UPDATE repositories SET name = 'api' WHERE id = ANY($1)`, []string{s.repoID, s.webRepo})
	id := s.ok(s.marcio, "POST", "/internal/sessions", map[string]any{"title": "Billing", "projects": []map[string]any{
		{"projectId": s.project, "repositoryIds": []string{s.repoID}},
		{"projectId": s.webProject, "repositoryIds": []string{s.webRepo}}}})["id"].(string)
	s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/chat", map[string]any{"text": "where does metering go?"})
	v := s.luxRun(s.started(id))
	if v == nil || v.spec.Git == nil {
		t.Fatalf("spec %+v", v)
	}
	var names, paths []string
	for _, r := range v.spec.Git.Repositories {
		names, paths = append(names, r.Name), append(paths, r.Path)
	}
	if want := []string{lux.SpecName("BILL-api"), lux.SpecName("BWOR-api")}; !slices.Equal(names, want) || names[0] == names[1] {
		t.Errorf("spec names %v, want %v, distinct", names, want)
	}
	if !slices.Equal(paths, []string{"/workspace/repos/BILL/api", "/workspace/repos/BWOR/api"}) {
		t.Errorf("checked out at %v", paths)
	}
}

// A task's Run names each of its project's repositories by lux_name, and
// no two can share one: the project's repositories are unique by it
// (migration 093), so whatever names a project holds, its Run's spec has
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
// takes it and starts its agent in that checkout, the checkout, push and
// fast-forward match it back, and what the ledger and the pull request say
// name the repository as the task does. The implementer's work is done as
// an agent does it: a file written and committed by a command run in the
// spec's workdir, through lux's exec, and it is that commit the publish
// pushes and the pull request carries.
func TestATaskRepositoryLuxWouldRefuseByNameIsDelivered(t *testing.T) {
	w := newWorld(t)
	w.lux.Workspaces = t.TempDir()
	const name = "Target.API"
	mustExec(t, w.owner, `UPDATE repositories SET name = $2 WHERE id = $1`, w.repoID, name)
	scripted := w.lux.Decide
	w.lux.Decide = func(spec map[string]any) fakelux.Behaviour {
		b := scripted(spec)
		if labels, _ := spec["labels"].(map[string]any); labels["dude.phase"] == "implement" {
			// It works until told it is done; what it commits is its own.
			b.Commit, b.Hang, b.WakeOnInput = nil, true, true
		}
		return b
	}
	wi := w.task()
	w.deliver(wi)
	var implementer string
	w.until("the implementer to be working", func() bool {
		_ = w.owner.QueryRow(context.Background(), `SELECT id FROM runs WHERE task_id = $1 AND phase = 'implement' AND status = 'running'`,
			wi).Scan(&implementer)
		return implementer != ""
	})
	var spec lux.Spec
	for _, r := range w.lux.Runs() {
		if r.ID == w.luxRunOf(implementer) {
			_ = json.Unmarshal(r.Spec, &spec)
		}
	}
	cmd := "cd " + spec.Workload.Workdir + " && printf 'from its workdir\\n' > AGENT.md && git add AGENT.md" +
		" && git -c user.name=agent -c user.email=agent@x commit -q -m 'Agent edit from its workdir'"
	res, err := w.syncer.Lux.Exec(context.Background(), w.luxRunOf(implementer), []string{"sh", "-c", cmd})
	if err != nil || res.ExitCode != 0 {
		t.Fatalf("committing in the agent's workdir %s: exit %d, %v: %s", spec.Workload.Workdir, res.ExitCode, err, res.Stderr)
	}
	// Told to finish, interrupting the turn it keeps open.
	mustExec(t, w.owner, `INSERT INTO directives (id, organization_id, task_id, run_id, text, interrupt)
		VALUES ('dir_done', $1, $2, $3, 'done', true)`, w.org, wi, implementer)
	w.until("a pull request", func() bool { return len(w.gh.Pulls()) == 1 })
	if log := strings.Join(w.gh.Log(delivery.BranchFor(wi, 1)), "\n"); !strings.Contains(log, "Agent edit from its workdir") {
		t.Errorf("the task branch does not carry the agent's commit:\n%s", log)
	}
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
		if spec.Workload.Workdir != got.Path {
			t.Errorf("%s: the agent starts in %s, its checkout is at %s", spec.Labels["dude.phase"], spec.Workload.Workdir, got.Path)
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
