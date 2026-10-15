package orchestrator_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/fakegithub"
)

// checkSource asks the orchestrator whether a task may start from source,
// as the backend does on a task's create or update.
func (w *world) checkSource(source map[string]any) (int, map[string]any) {
	w.t.Helper()
	return w.call("/internal/projects/"+w.project+"/sources/check", source)
}

func refusedAs(t *testing.T, status int, out map[string]any, wantStatus int, code string) {
	t.Helper()
	e, _ := out["error"].(map[string]any)
	if status != wantStatus || e == nil || e["code"] != code {
		t.Errorf("answered %d %v, want %d %s", status, out, wantStatus, code)
	}
}

// An open pull request in the project's repository, from a branch in it, is
// adopted with GitHub's head, base and title; a branch with the base a
// person chose, or the repository's default.
func TestASourceIsResolvedFromGitHub(t *testing.T) {
	w := newWorld(t)
	n := w.gh.OpenAsPerson("alice", "feature/greet", "main", "Greet in Portuguese")
	status, out := w.checkSource(map[string]any{"kind": "pr", "repositoryId": w.repoID,
		"pullRequest": w.ghURL + "/acme/target/pull/1"})
	if status != http.StatusOK || out["headBranch"] != "feature/greet" || out["baseBranch"] != "main" ||
		out["title"] != "Greet in Portuguese" || out["number"] != float64(n) || out["repository"] != "target" {
		t.Fatalf("pull request source = %d %v", status, out)
	}

	w.gh.Branch("stack/base", "main", "A base")
	w.gh.Branch("stack/top", "stack/base", "On top")
	status, out = w.checkSource(map[string]any{"kind": "branch", "repositoryId": w.repoID, "branch": "stack/top", "base": "stack/base"})
	if status != http.StatusOK || out["headBranch"] != "stack/top" || out["baseBranch"] != "stack/base" || out["number"] != nil {
		t.Fatalf("branch source = %d %v", status, out)
	}
	status, out = w.checkSource(map[string]any{"kind": "branch", "repositoryId": w.repoID, "branch": "stack/top"})
	if status != http.StatusOK || out["baseBranch"] != "main" {
		t.Fatalf("branch source with no base = %d %v, want main", status, out)
	}
}

// Each rule a source can break is refused with its own reason, before
// anything is kept.
func TestASourceDudeMustNotTouchIsRefused(t *testing.T) {
	w := newWorld(t)
	open := w.gh.OpenAsPerson("alice", "feature/open", "main", "Open")
	merged := w.gh.OpenAsPerson("alice", "feature/merged", "main", "Merged")
	w.gh.Merge(merged)
	closed := w.gh.OpenAsPerson("alice", "feature/closed", "main", "Closed")
	w.gh.Close(closed)
	fork := w.gh.OpenAsPerson("mallory", "feature/fork", "main", "From a fork")
	forked := w.gh.Pull(fork)
	w.gh.Set(func(*fakegithub.Server) { forked.HeadRepo = "mallory/target" })
	protectedPR := w.gh.OpenAsPerson("alice", "feature/locked", "main", "Protected head")
	w.gh.Branch("release/1.2", "main", "Release")
	w.gh.Set(func(s *fakegithub.Server) { s.Protected = map[string]bool{"feature/locked": true} })

	mustExec(t, w.owner, `INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES ('prj_other', $1, 'O', 'o', 'O')`, w.org)
	mustExec(t, w.owner, `INSERT INTO repositories (id, organization_id, project_id, name, url, default_branch)
		VALUES ('repo_other', $1, 'prj_other', 'other', 'git://127.0.0.1/acme/web.git', 'main')`, w.org)

	cases := []struct {
		name   string
		source map[string]any
		status int
		code   string
	}{
		{"wrong repository URL", map[string]any{"kind": "pr", "repositoryId": w.repoID, "pullRequest": w.ghURL + "/acme/other/pull/1"}, 422, "bad_pull_request"},
		{"wrong host URL", map[string]any{"kind": "pr", "repositoryId": w.repoID, "pullRequest": "https://example.com/acme/target/pull/1"}, 422, "bad_pull_request"},
		{"not in project", map[string]any{"kind": "pr", "repositoryId": "repo_other", "pullRequest": "1"}, 422, "not_in_project"},
		{"merged", map[string]any{"kind": "pr", "repositoryId": w.repoID, "pullRequest": "#2"}, 422, "pr_merged"},
		{"closed", map[string]any{"kind": "pr", "repositoryId": w.repoID, "pullRequest": "3"}, 422, "pr_closed"},
		{"fork", map[string]any{"kind": "pr", "repositoryId": w.repoID, "pullRequest": "4"}, 422, "pr_fork"},
		{"no such pull request", map[string]any{"kind": "pr", "repositoryId": w.repoID, "pullRequest": "99"}, 422, "pr_not_found"},
		{"protected head", map[string]any{"kind": "pr", "repositoryId": w.repoID, "pullRequest": "5"}, 422, "protected"},
		{"default branch", map[string]any{"kind": "branch", "repositoryId": w.repoID, "branch": "main", "base": "feature/open"}, 422, "default_branch"},
		// Its own base by default, or named so: what it is, not that the two are one.
		{"default branch, no base", map[string]any{"kind": "branch", "repositoryId": w.repoID, "branch": "main"}, 422, "default_branch"},
		{"default branch, itself as base", map[string]any{"kind": "branch", "repositoryId": w.repoID, "branch": "main", "base": "main"}, 422, "default_branch"},
		{"release branch, itself as base", map[string]any{"kind": "branch", "repositoryId": w.repoID, "branch": "release/1.2", "base": "release/1.2"}, 422, "release_branch"},
		{"protected branch, itself as base", map[string]any{"kind": "branch", "repositoryId": w.repoID, "branch": "feature/locked", "base": "feature/locked"}, 422, "protected"},
		{"same branch", map[string]any{"kind": "branch", "repositoryId": w.repoID, "branch": "feature/open", "base": "feature/open"}, 422, "same_branch"},
		{"release branch", map[string]any{"kind": "branch", "repositoryId": w.repoID, "branch": "release/1.2"}, 422, "release_branch"},
		{"no such branch", map[string]any{"kind": "branch", "repositoryId": w.repoID, "branch": "nope"}, 422, "branch_not_found"},
	}
	_ = open
	_ = protectedPR
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			status, out := w.checkSource(c.source)
			refusedAs(t, status, out, c.status, c.code)
		})
	}

	// Another live task's branch: adopted as its source, or its delivery's own.
	other := w.task()
	mustExec(t, w.owner, `INSERT INTO task_sources (task_id, organization_id, repository_id, kind, number, head_branch, base_branch)
		VALUES ($1, $2, $3, 'pr', 1, 'feature/open', 'main')`, other, w.org, w.repoID)
	status, out := w.checkSource(map[string]any{"kind": "pr", "repositoryId": w.repoID, "pullRequest": "1"})
	refusedAs(t, status, out, 422, "branch_taken")
	// Its own source is not another task's.
	if status, out := w.checkSource(map[string]any{"kind": "pr", "repositoryId": w.repoID, "pullRequest": "1", "taskId": other}); status != 200 {
		t.Errorf("a task's own source refused: %d %v", status, out)
	}
	// A task over gives its branch up.
	mustExec(t, w.owner, `UPDATE tasks SET status = 'done' WHERE id = $1`, other)
	if status, out := w.checkSource(map[string]any{"kind": "pr", "repositoryId": w.repoID, "pullRequest": "1"}); status != 200 {
		t.Errorf("a done task's branch still held: %d %v", status, out)
	}
	live := w.task()
	w.deliver(live)
	w.gh.Branch(delivery.BranchFor(live, 1), "main", "Its own")
	status, out = w.checkSource(map[string]any{"kind": "branch", "repositoryId": w.repoID, "branch": delivery.BranchFor(live, 1)})
	refusedAs(t, status, out, 422, "branch_taken")
}

// The dialog's pickers: the project's repositories with whether dude may
// push to each, and a repository's branches matching what was typed.
func TestSourcePickersAskGitHub(t *testing.T) {
	w := newWorld(t)
	w.gh.Branch("feature/greet", "main", "Greet")
	w.gh.Branch("feature/farewell", "main", "Bye")
	status, body := w.get("/internal/projects/"+w.project+"/sources/repositories/"+w.repoID+"/branches?q=GREET", w.org)
	var branches struct {
		Branches []string
		More     bool
	}
	_ = json.Unmarshal([]byte(body), &branches)
	if status != 200 || !slices.Equal(branches.Branches, []string{"feature/greet"}) || branches.More {
		t.Fatalf("branches %d %s", status, body)
	}
	if status, _ := w.get("/internal/projects/"+w.project+"/sources/repositories/repo_elsewhere/branches", w.org); status != 404 {
		t.Fatalf("another project's repository: %d", status)
	}

	status, body = w.get("/internal/projects/"+w.project+"/sources/repositories", w.org)
	if status != 200 || !strings.Contains(body, `"writable":true`) {
		t.Fatalf("repositories %d %s", status, body)
	}
	w.gh.Set(func(s *fakegithub.Server) { s.ReceiveStatus = 403 })
	status, body = w.get("/internal/projects/"+w.project+"/sources/repositories", w.org)
	if status != 200 || !strings.Contains(body, `"writable":false`) || !strings.Contains(body, "push access denied") {
		t.Fatalf("repositories without write %d %s", status, body)
	}
}

// Fifty repositories, each push-access check 320 ms, are answered inside
// the backend's 15 s deadline, at most four checks at a time, in name order.
func TestFiftyRepositoriesAreCheckedInsideTheBackendsDeadline(t *testing.T) {
	w := newWorld(t)
	var inFlight, most, arrived atomic.Int32
	// The first four checks are held until all four have arrived: the
	// overlap is shown, not left to scheduling. Each then takes 320ms, so
	// checked one by one the fifty outlast the 15s deadline below.
	four := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		n := inFlight.Add(1)
		defer inFlight.Add(-1)
		for m := most.Load(); n > m && !most.CompareAndSwap(m, n); m = most.Load() {
		}
		if arrived.Add(1) == 4 {
			close(four)
		}
		select {
		case <-four:
		case <-r.Context().Done():
			return
		}
		select {
		case <-time.After(320 * time.Millisecond):
		case <-r.Context().Done():
			return
		}
		rw.Header().Set("Content-Type", "application/x-git-receive-pack-advertisement")
		fmt.Fprint(rw, "001f# service=git-receive-pack\n0000")
	}))
	defer srv.Close()
	mustExec(t, w.owner, `UPDATE forge_credentials SET api_base_url = $1 WHERE organization_id = $2`, srv.URL, w.org)
	mustExec(t, w.owner, `UPDATE repositories SET name = 'r00', url = $1 WHERE id = $2`, srv.URL+"/acme/r0.git", w.repoID)
	for i := 1; i < 50; i++ {
		mustExec(t, w.owner, `INSERT INTO repositories (id, organization_id, project_id, name, url, default_branch)
			VALUES ($1, $2, $3, $4, $5, 'main')`, fmt.Sprintf("repo_many_%d", i), w.org, w.project, fmt.Sprintf("r%02d", i),
			fmt.Sprintf("%s/acme/r%d.git", srv.URL, i))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", w.api+"/internal/projects/"+w.project+"/sources/repositories", nil)
	req.Header.Set("Authorization", "Bearer svc")
	req.Header.Set("X-Dude-Organization", w.org)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("past the deadline: %v", err)
	}
	defer res.Body.Close()
	var out struct {
		Repositories []struct {
			Name     string
			Writable bool
		}
	}
	_ = json.NewDecoder(res.Body).Decode(&out)
	if res.StatusCode != 200 || len(out.Repositories) != 50 {
		t.Fatalf("%d, %d repositories", res.StatusCode, len(out.Repositories))
	}
	for i, r := range out.Repositories {
		if r.Name != fmt.Sprintf("r%02d", i) || !r.Writable {
			t.Fatalf("repository %d is %+v", i, r)
		}
	}
	if m := most.Load(); m != 4 {
		t.Fatalf("%d checks at once, want 4", m)
	}
}

// No write access, and GitHub not answering, are each said as such.
func TestASourceGitHubWillNotConfirmIsRefused(t *testing.T) {
	w := newWorld(t)
	w.gh.OpenAsPerson("alice", "feature/open", "main", "Open")
	w.gh.Set(func(s *fakegithub.Server) { s.ReceiveStatus = 403 })
	status, out := w.checkSource(map[string]any{"kind": "pr", "repositoryId": w.repoID, "pullRequest": "1"})
	refusedAs(t, status, out, 422, "no_write")

	w.gh.Set(func(s *fakegithub.Server) {
		s.ReceiveStatus = 0
		s.Intercept = func(r *http.Request) int {
			if r.Method == "GET" && r.URL.Path == "/repos/acme/target/pulls/1" {
				return http.StatusServiceUnavailable
			}
			return 0
		}
	})
	status, out = w.checkSource(map[string]any{"kind": "pr", "repositoryId": w.repoID, "pullRequest": "1"})
	refusedAs(t, status, out, 502, "github_unavailable")

	mustExec(t, w.owner, `DELETE FROM forge_credentials WHERE organization_id = $1`, w.org)
	status, out = w.checkSource(map[string]any{"kind": "pr", "repositoryId": w.repoID, "pullRequest": "1"})
	refusedAs(t, status, out, 502, "github_unavailable")
}
