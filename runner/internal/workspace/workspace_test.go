package workspace

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// newFixtureRepo creates a small git repository to clone from.
func newFixtureRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	for _, args := range [][]string{
		{"init", "--initial-branch=main"},
		{"config", "user.email", "test@example.com"},
		{"config", "user.name", "Test"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
		}
	}

	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("fixture\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"add", "."},
		{"commit", "-m", "initial"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
		}
	}
	return dir
}

func TestCreateMaterializesRepoAndLayout(t *testing.T) {
	origin := newFixtureRepo(t)
	m := NewManager(t.TempDir())

	wsPath, repos, err := m.Create(context.Background(), "org_1", "run_1", []Repository{
		{Name: "fixture", URL: origin, DefaultBranch: "main"},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Every directory the container and uploader rely on must exist.
	for _, dir := range []string{DirRepos, DirAgentState, DirScratch, DirArtifactsStaging} {
		if _, err := os.Stat(filepath.Join(wsPath, dir)); err != nil {
			t.Errorf("missing workspace dir %s: %v", dir, err)
		}
	}
	if _, err := os.Stat(filepath.Join(wsPath, FileManifest)); err != nil {
		t.Errorf("missing manifest: %v", err)
	}

	if len(repos) != 1 {
		t.Fatalf("expected 1 repo, got %d", len(repos))
	}
	if repos[0].HeadSHA == "" {
		t.Error("expected a resolved HEAD sha")
	}
	if repos[0].Branch != "main" {
		t.Errorf("branch = %q, want main", repos[0].Branch)
	}

	content, err := os.ReadFile(filepath.Join(wsPath, DirRepos, "fixture", "README.md"))
	if err != nil || string(content) != "fixture\n" {
		t.Errorf("repo content not materialized: %v", err)
	}
}

func TestMaterializePointsOriginAtRealRemote(t *testing.T) {
	origin := newFixtureRepo(t)
	m := NewManager(t.TempDir())

	wsPath, _, err := m.Create(context.Background(), "org_1", "run_1", []Repository{
		{Name: "fixture", URL: origin, DefaultBranch: "main"},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// The clone comes from the local mirror, but pushes must reach the real
	// remote — the mirror is an implementation detail.
	cmd := exec.Command("git", "remote", "get-url", "origin")
	cmd.Dir = filepath.Join(wsPath, DirRepos, "fixture")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git remote get-url: %v: %s", err, out)
	}
	if got := strings.TrimSpace(string(out)); got != origin {
		t.Errorf("origin = %q, want %q (the mirror must not leak)", got, origin)
	}
}

func TestCreateIsIdempotentAndPreservesUncommittedWork(t *testing.T) {
	origin := newFixtureRepo(t)
	m := NewManager(t.TempDir())
	ctx := context.Background()

	wsPath, _, err := m.Create(ctx, "org_1", "run_1", []Repository{
		{Name: "fixture", URL: origin, DefaultBranch: "main"},
	})
	if err != nil {
		t.Fatalf("first Create: %v", err)
	}

	// Simulate work in progress that a replacement container must still see.
	scratch := filepath.Join(wsPath, DirRepos, "fixture", "in-progress.txt")
	if err := os.WriteFile(scratch, []byte("work"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, _, err := m.Create(ctx, "org_1", "run_1", []Repository{
		{Name: "fixture", URL: origin, DefaultBranch: "main"},
	}); err != nil {
		t.Fatalf("second Create: %v", err)
	}

	if _, err := os.Stat(scratch); err != nil {
		t.Error("re-materializing destroyed uncommitted work; a replacement container would lose it")
	}
}

func TestConcurrentMirrorUpdatesAreSerialized(t *testing.T) {
	origin := newFixtureRepo(t)
	m := NewManager(t.TempDir())

	// Concurrent fetches into one bare mirror corrupt it, so EnsureMirror
	// must serialize per repository URL.
	var wg sync.WaitGroup
	errs := make([]error, 8)
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = m.EnsureMirror(context.Background(), Repository{
				Name: "fixture", URL: origin, DefaultBranch: "main",
			})
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("concurrent EnsureMirror[%d]: %v", i, err)
		}
	}
}

func TestRemoveKeepsMirrorCache(t *testing.T) {
	origin := newFixtureRepo(t)
	m := NewManager(t.TempDir())
	ctx := context.Background()

	if _, _, err := m.Create(ctx, "org_1", "run_1", []Repository{
		{Name: "fixture", URL: origin, DefaultBranch: "main"},
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	if err := m.Remove("run_1"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := os.Stat(m.WorkspacePath("run_1")); !os.IsNotExist(err) {
		t.Error("workspace should be gone")
	}

	// The mirror is shared across Runs and is what makes the next startup
	// fast, so removing one workspace must not evict it.
	if got := m.CachedRepositories(); len(got) != 1 {
		t.Errorf("mirror cache = %v, want it preserved after workspace removal", got)
	}
}

func TestMirrorPathAvoidsBasenameCollisions(t *testing.T) {
	m := NewManager(t.TempDir())

	// Two different remotes sharing a basename must not share a mirror.
	a := m.mirrorPath("https://github.com/one/api")
	b := m.mirrorPath("https://gitlab.com/two/api")
	if a == b {
		t.Errorf("distinct URLs mapped to the same mirror path: %s", a)
	}
}
