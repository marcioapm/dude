package phases

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/marciomartins/dude/orchestrator/internal/fakelux"
	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// accountingRun is one running Run on a fake lux whose checkout of
// "target" is a real clone of origin, a bare repository standing in for
// GitHub. Accounting runs through lux's Exec as it does in production.
type accountingRun struct {
	t      *testing.T
	fake   *fakelux.Server
	client lux.Client
	luxID  string
	origin string
	work   string
	start  string
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

// syncBase is lux's fetch for sync_base: origin's main at lux/main.
func (a *accountingRun) syncBase() {
	a.t.Helper()
	git(a.t, a.work, "fetch", "-q", "origin", "+refs/heads/main:refs/remotes/lux/main")
}

func (a *accountingRun) account() accounting {
	a.t.Helper()
	acc, err := publicationAccounting(context.Background(), a.client, a.luxID, a.start, a.head(), "refs/remotes/lux/main", "target")
	if err != nil {
		a.t.Fatal(err)
	}
	return acc
}

func (a *accountingRun) head() string { return git(a.t, a.work, "rev-parse", "HEAD") }

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

// The base moved on GitHub while the Run worked. Without sync_base the
// checkout has no lux/main: every commit counts as the task's, and nothing
// fails. With it, the merged base is left out and the resolution kept.
func TestAccountingSurvivesABaseThatMovedDuringTheRun(t *testing.T) {
	t.Run("never synced", func(t *testing.T) {
		a := newAccountingRun(t)
		a.commit(map[string]string{"mine.go": "package mine\n"}, "task work")
		a.advanceBase(map[string]string{"base.go": lines(300, "base")})
		samePaths(t, a.account(), "mine.go")
	})
	t.Run("synced and merged", func(t *testing.T) {
		a := newAccountingRun(t)
		a.commit(map[string]string{"mine.go": "package mine\n", "shared.txt": "task\n"}, "task work")
		a.advanceBase(map[string]string{"base.go": lines(300, "base"), "shared.txt": "base\n"})
		a.syncBase()
		if _, err := exec.Command("git", "-C", a.work, "merge", "-q", "--no-edit", "lux/main").CombinedOutput(); err == nil {
			t.Fatal("the fixture's merge did not conflict")
		}
		write(t, filepath.Join(a.work, "shared.txt"), "task\nbase\n")
		git(t, a.work, "add", "shared.txt")
		git(t, a.work, "commit", "-q", "--no-edit")
		acc := a.account()
		samePaths(t, acc, "mine.go", "shared.txt")
		// mine.go's line, shared.txt's, and the resolution against git's
		// conflicted merge; never the base's 300.
		if acc.Lines > 10 {
			t.Fatalf("lines %d: the base's 300 were counted", acc.Lines)
		}
	})
	t.Run("synced again after merging", func(t *testing.T) {
		// lux/main moves on past the merged base: what was merged is still
		// reachable from it, so still left out.
		a := newAccountingRun(t)
		a.commit(map[string]string{"mine.go": "package mine\n"}, "task work")
		a.advanceBase(map[string]string{"base.go": lines(300, "base")})
		a.syncBase()
		git(t, a.work, "merge", "-q", "--no-edit", "lux/main")
		a.advanceBase(map[string]string{"later.go": lines(50, "later")})
		a.syncBase()
		samePaths(t, a.account(), "mine.go")
	})
}

// The checkout is the agent's: a replace ref, an attributes file, a diff
// driver or textconv in its config hide nothing.
func TestAccountingCannotBeFooledByTheCheckout(t *testing.T) {
	big := lines(500, "mine")
	t.Run("a replace ref", func(t *testing.T) {
		a := newAccountingRun(t)
		innocent := a.commit(map[string]string{"small.txt": "x\n"}, "innocent")
		git(t, a.work, "reset", "-q", "--hard", a.start)
		head := a.commit(map[string]string{"big.txt": big}, "big")
		git(t, a.work, "replace", head, innocent)
		acc := a.account()
		samePaths(t, acc, "big.txt")
		if acc.Lines != 500 {
			t.Fatalf("lines %d, want 500", acc.Lines)
		}
	})
	t.Run("a graft that makes a commit look like a base merge", func(t *testing.T) {
		a := newAccountingRun(t)
		a.advanceBase(map[string]string{"base.go": "base\n"})
		a.syncBase()
		head := a.commit(map[string]string{"big.txt": big}, "big")
		git(t, a.work, "replace", "--graft", head, a.start, "refs/remotes/lux/main")
		samePaths(t, a.account(), "big.txt")
	})
	for name, setup := range map[string]func(a *accountingRun){
		"info/attributes": func(a *accountingRun) {
			write(t, filepath.Join(a.work, ".git", "info", "attributes"), "* -diff\n")
		},
		"a committed .gitattributes": func(a *accountingRun) {
			a.commit(map[string]string{".gitattributes": "* -diff\n"}, "attributes")
			a.start = a.head()
		},
		"a diff driver and textconv in its config": func(a *accountingRun) {
			write(t, filepath.Join(a.work, ".git", "info", "attributes"), "* diff=hide\n")
			git(t, a.work, "config", "diff.hide.textconv", "true")
			git(t, a.work, "config", "diff.external", "true")
		},
	} {
		t.Run(name, func(t *testing.T) {
			a := newAccountingRun(t)
			setup(a)
			a.commit(map[string]string{"big.txt": big}, "big")
			acc := a.account()
			if acc.Lines != 500 || acc.Binary {
				t.Fatalf("lines %d binary %v, want 500 counted", acc.Lines, acc.Binary)
			}
		})
	}
	t.Run("a binary change is past any line limit", func(t *testing.T) {
		a := newAccountingRun(t)
		a.commit(map[string]string{"image.bin": "\x00\x01\x02"}, "binary")
		acc := a.account()
		samePaths(t, acc, "image.bin")
		if !acc.Binary {
			t.Fatal("a binary change was counted as 0 lines")
		}
	})
}

// Paths are what git's raw records name, unquoted: both sides of a rename,
// a directory rename's files, a non-ASCII name; a mode change is a path.
func TestAccountingPathsAreRealPaths(t *testing.T) {
	a := newAccountingRun(t)
	a.commit(map[string]string{"dir/a/f.txt": "f\n", "tool.sh": "echo\n"}, "seed more")
	a.start = a.head()
	git(t, a.work, "mv", "dir/a", "dir/b")
	write(t, filepath.Join(a.work, "café.txt"), "olá\n")
	if err := os.Chmod(filepath.Join(a.work, "tool.sh"), 0o755); err != nil {
		t.Fatal(err)
	}
	a.commit(nil, "rename, add, chmod")
	samePaths(t, a.account(), "café.txt", "dir/a/f.txt", "dir/b/f.txt", "tool.sh")
}

// Many small commits count their net change: a file added then removed is
// not a path, three rewrites of the same lines count once.
func TestAccountingCountsTheNetChange(t *testing.T) {
	a := newAccountingRun(t)
	for i := range 3 {
		a.commit(map[string]string{"churn.txt": lines(30, fmt.Sprint("v", i))}, "rewrite")
	}
	a.commit(map[string]string{"gone.txt": lines(20, "gone")}, "add")
	a.commit(map[string]string{"gone.txt": ""}, "remove")
	acc := a.account()
	samePaths(t, acc, "churn.txt")
	if acc.Lines != 30 {
		t.Fatalf("lines %d, want the net 30", acc.Lines)
	}
}

// A task branch fast-forwarded onto the base has nothing of its own.
func TestAccountingAFastForwardOntoTheBaseCountsNothing(t *testing.T) {
	a := newAccountingRun(t)
	a.advanceBase(map[string]string{"base.go": lines(100, "base")})
	a.syncBase()
	git(t, a.work, "merge", "-q", "--ff-only", "lux/main")
	acc := a.account()
	if len(acc.Paths) != 0 || acc.Lines != 0 {
		t.Fatalf("paths %q lines %d, want none", acc.Paths, acc.Lines)
	}
}

// A codemod done in 50 steps over 1500 files is one net diff, well inside
// what Exec keeps.
func TestAccountingOfALargeCodemodFitsInExec(t *testing.T) {
	if testing.Short() {
		t.Skip("writes 50 commits of 1500 files")
	}
	a := newAccountingRun(t)
	// Written with fast-import: 75 000 file writes through the worktree
	// take minutes.
	var stream strings.Builder
	for i := range 50 {
		fmt.Fprintf(&stream, "commit refs/heads/main\ncommitter Acme <a@example.com> %d +0000\ndata 7\nstep %02d\n", 1700000000+i, i)
		if i == 0 {
			fmt.Fprintf(&stream, "from %s\n", a.start)
		}
		for f := range 1500 {
			body := fmt.Sprintf("package src // step %d\n", i)
			fmt.Fprintf(&stream, "M 100644 inline src/f%04d.go\ndata %d\n%s", f, len(body), body)
		}
	}
	imp := exec.Command("git", "-C", a.work, "fast-import", "--quiet", "--force")
	imp.Stdin = strings.NewReader(stream.String())
	if out, err := imp.CombinedOutput(); err != nil {
		t.Fatalf("fast-import: %v %s", err, out)
	}
	git(t, a.work, "reset", "-q", "--hard", "main")
	res, err := a.client.Exec(context.Background(), a.luxID, []string{"sh", "-c", accountingScript, "account",
		"/workspace/repos/target", a.start, a.head(), "refs/remotes/lux/main"})
	if err != nil || res.ExitCode != 0 {
		t.Fatalf("exec: %v %d %s", err, res.ExitCode, res.Stderr)
	}
	if len(res.Stdout) > lux.ExecLimit/16 {
		t.Fatalf("%d bytes of evidence, near ExecLimit's %d", len(res.Stdout), lux.ExecLimit)
	}
	acc := a.account()
	if len(acc.Paths) != 1500 || acc.Lines != 1500 {
		t.Fatalf("%d paths, %d lines; want 1500 and 1500", len(acc.Paths), acc.Lines)
	}
}

// A lux that does not answer in time, cannot be reached, or whose Run is
// not running now is asked again later; bad evidence is not.
func TestAccountingThatMayPassIsRetried(t *testing.T) {
	release := make(chan struct{})
	hung := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer ws.CloseNow()
		<-release
	}))
	t.Cleanup(hung.Close)
	t.Cleanup(func() { close(release) })
	defer func(d time.Duration) { accountingTimeout = d }(accountingTimeout)
	accountingTimeout = 200 * time.Millisecond

	stopped := newAccountingRun(t)
	stopped.fake.Crash(stopped.luxID)

	for name, c := range map[string]struct {
		client lux.Client
		run    string
	}{
		"a lux that never answers": {lux.New(hung.URL, "k"), "run"},
		"an unreachable lux":       {lux.New("http://127.0.0.1:1", "k"), "run"},
		"a Run not running":        {stopped.client, stopped.luxID},
	} {
		t.Run(name, func(t *testing.T) {
			done := make(chan error, 1)
			go func() {
				_, err := publicationAccounting(context.Background(), c.client, c.run, "a", "b", "", "target")
				done <- err
			}()
			select {
			case err := <-done:
				if err == nil || !accountingTransient(err) {
					t.Fatalf("err %v, want one to retry", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("still waiting on lux after 5s")
			}
		})
	}
	t.Run("a completed Exec with bad evidence", func(t *testing.T) {
		a := newAccountingRun(t)
		_, err := publicationAccounting(context.Background(), a.client, a.luxID, a.start, strings.Repeat("0", 40), "", "target")
		if err == nil || accountingTransient(err) {
			t.Fatalf("err %v, want a failure that is final", err)
		}
	})
}

func TestParseAccountingFailsClosed(t *testing.T) {
	for name, text := range map[string]string{
		"missing evidence": "\n",
		"truncated":        "DUDE_ACCOUNTING\ta\tb\t\x00DUDE_SEGMENT\x001\t0\tfile\x00",
		"bad numstat":      "DUDE_ACCOUNTING\ta\tb\t\x00DUDE_SEGMENT\x001\tfile\x00DUDE_END\tb\x00",
		"bad raw":          "DUDE_ACCOUNTING\ta\tb\t\x00DUDE_SEGMENT\x00:100644 M\x00f\x00DUDE_END\tb\x00",
		"another head":     "DUDE_ACCOUNTING\ta\tc\t\x00DUDE_END\tc\x00",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := parseAccounting(text, "a", "b"); err == nil {
				t.Fatal("unverified evidence accepted")
			}
		})
	}
}
