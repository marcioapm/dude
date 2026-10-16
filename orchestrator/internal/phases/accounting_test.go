package phases

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/marciomartins/dude/orchestrator/internal/fakegithub"
	"github.com/marciomartins/dude/orchestrator/internal/fakelux"
	"github.com/marciomartins/dude/orchestrator/internal/forge"
	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// accountingRun is one running Run on a fake lux whose checkout of
// "target" is a real clone of origin, a bare repository standing in for
// GitHub, which a fake GitHub over origin compares.
type accountingRun struct {
	t      *testing.T
	fake   *fakelux.Server
	client lux.Client
	luxID  string
	origin string
	work   string
	start  string
	// The base lux reported fetching (syncBase), "" before any.
	base string
	// Sees each request to the fake GitHub of published.
	intercept func(*http.Request) int
	pushed    string
}

func newAccountingRun(t *testing.T) *accountingRun {
	t.Helper()
	dir := t.TempDir()
	seed := filepath.Join(dir, "seed")
	git(t, dir, "init", "-q", "-b", "main", seed)
	write(t, filepath.Join(seed, "README.md"), "# target\n")
	git(t, seed, "add", ".")
	git(t, seed, "commit", "-qm", "seed")
	origin := filepath.Join(dir, "origin.git")
	git(t, dir, "clone", "-q", "--bare", seed, origin)
	fake := fakelux.New(origin, "k", func(map[string]any) fakelux.Behaviour { return fakelux.Behaviour{Hang: true} })
	fake.Workspaces = dir
	srv := httptest.NewServer(fake.Handler())
	t.Cleanup(func() { srv.Close(); fake.Close() })
	client := lux.New(srv.URL, "k")
	spec := lux.Spec{Name: "t", Image: lux.Image{Ref: "img"}, Workload: lux.Workload{Adapter: "opencode", Prompt: "go"},
		Git: &lux.Git{Repositories: []lux.Repository{{Name: "target", URL: "git://127.0.0.1/acme/target.git", Ref: "main"}}}}
	lr, err := client.Submit(context.Background(), spec, "run_accounting")
	if err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		if got, _ := client.Get(context.Background(), lr.ID); got.State == "running" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the fake lux Run did not start")
		}
	}
	work, err := fake.Checkout(lr.ID, "target")
	if err != nil {
		t.Fatal(err)
	}
	return &accountingRun{t: t, fake: fake, client: client, luxID: lr.ID, origin: origin, work: work,
		start: git(t, work, "rev-parse", "HEAD")}
}

// commit writes files in the checkout and commits them; "" deletes one.
func (a *accountingRun) commit(files map[string]string, message string) string {
	a.t.Helper()
	for path, content := range files {
		if content == "" {
			git(a.t, a.work, "rm", "-q", path)
			continue
		}
		write(a.t, filepath.Join(a.work, path), content)
	}
	git(a.t, a.work, "add", "-A")
	git(a.t, a.work, "commit", "-qm", message)
	return git(a.t, a.work, "rev-parse", "HEAD")
}

// advanceBase commits on origin's main, as another task's merge would.
func (a *accountingRun) advanceBase(files map[string]string) string {
	a.t.Helper()
	other := filepath.Join(a.t.TempDir(), "other")
	git(a.t, a.t.TempDir(), "clone", "-q", a.origin, other)
	for path, content := range files {
		write(a.t, filepath.Join(other, path), content)
	}
	git(a.t, other, "add", "-A")
	git(a.t, other, "commit", "-qm", "base work")
	git(a.t, other, "push", "-q", "origin", "main")
	return git(a.t, other, "rev-parse", "HEAD")
}

// syncBase is lux's fetch for sync_base: origin's main at lux/main, and
// the commit lux reports fetching, which is the trusted base offered.
func (a *accountingRun) syncBase() {
	a.t.Helper()
	git(a.t, a.work, "fetch", "-q", "origin", "+refs/heads/main:refs/remotes/lux/main")
	a.base = git(a.t, a.work, "rev-parse", "refs/remotes/lux/main")
}

func (a *accountingRun) head() string { return git(a.t, a.work, "rev-parse", "HEAD") }

// published is a publish of the checkout's HEAD as dude accounts it: lux
// pushes it to origin (GitHub, here a fake over origin; push), GitHub
// compares it with where the Run started, and the base lux reported
// fetching (a.base) is checked against GitHub's main.
func (a *accountingRun) published() accounting {
	a.t.Helper()
	acc, err := a.publishedErr()
	if err != nil {
		a.t.Fatal(err)
	}
	return acc
}

func (a *accountingRun) publishedErr() (accounting, error) {
	a.t.Helper()
	a.push()
	head := a.head()
	gh := a.github()
	ctx := context.Background()
	task, err := gh.Compare(ctx, "acme/target", a.start, head)
	if err != nil {
		a.t.Fatal(err)
	}
	trusted, why, err := verifyBase(ctx, gh, "acme/target", "main", a.base)
	if err != nil {
		return accounting{}, err
	}
	return accountPublish(ctx, gh, "acme/target", "main", a.start, head, trusted, why, task)
}

// push is lux's push of the checkout's HEAD, once per head.
func (a *accountingRun) push() {
	a.t.Helper()
	if head := a.head(); head != a.pushed {
		git(a.t, a.work, "push", "-q", "-f", a.origin, "HEAD:refs/heads/dude/run")
		a.pushed = head
	}
}

// github is a fake GitHub over origin, its requests seen by intercept.
func (a *accountingRun) github() *forge.GitHub {
	a.t.Helper()
	fake := fakegithub.New(a.origin, "acme/target")
	fake.Intercept = a.intercept
	srv := httptest.NewServer(fake.Handler())
	a.t.Cleanup(srv.Close)
	return forge.NewGitHub(forge.Credential{Auth: "pat", Secret: "ghp_x", APIBaseURL: srv.URL})
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=Acme", "-c", "user.email=a@example.com"}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func lines(n int, word string) string { return strings.Repeat(word+"\n", n) }

func samePaths(t *testing.T, got accounting, want ...string) {
	t.Helper()
	if !slices.Equal(got.Paths, want) {
		t.Fatalf("paths %q, want %q (lines %d)", got.Paths, want, got.Lines)
	}
}
