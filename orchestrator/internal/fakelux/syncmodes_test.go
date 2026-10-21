package fakelux_test

// Sync modes (lux's fast-forward and fetch), through dude's real lux
// client: what each does to a checkout, the remote-tracking ref it leaves,
// and the git.sync event saying so; and a lux without them refusing.

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/fakelux"
	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// syncRepo is a Run on a repository with a branch "work" one commit
// ahead of where the Run checked out (main), and its checkout.
func syncRepo(t *testing.T, noModes bool) (*fakelux.Server, *lux.HTTPClient, string, string, string) {
	t.Helper()
	repo := t.TempDir()
	gitInit(t, repo)
	fake := fakelux.New(repo, "k", nil)
	fake.NoSyncModes = noModes
	_, c, runID := startedWith(t, fake, lux.Spec{Image: lux.Image{Ref: "x"}, Workload: lux.Workload{Adapter: "generic"},
		Git: &lux.Git{Repositories: []lux.Repository{{Name: "app", URL: "file://" + repo, Ref: "main"}}}})
	work, err := fake.Checkout(runID, "app")
	if err != nil {
		t.Fatal(err)
	}
	gitIn(t, repo, "branch", "work", "main")
	gitIn(t, repo, "checkout", "-q", "work")
	gitIn(t, repo, "-c", "user.name=t", "-c", "user.email=t@x", "commit", "-q", "--allow-empty", "-m", "B")
	gitIn(t, repo, "checkout", "-q", "main")
	// The checkout on a branch named as the sync's target, where lux's
	// fast-forward may move it.
	gitIn(t, work, "checkout", "-q", "-b", "work")
	return fake, c, runID, repo, work
}

func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// lastSync is the Run's latest git.sync event's payload.
func lastSync(t *testing.T, c *lux.HTTPClient, runID string) map[string]any {
	t.Helper()
	var last map[string]any
	waitFor(t, "a git.sync", func() bool {
		frames, _ := c.Events(context.Background(), runID, 0)
		for _, f := range frames {
			if f.EventType == "git.sync" {
				last = nil
				_ = json.Unmarshal(f.EventData, &last)
			}
		}
		return last != nil
	})
	return last
}

func TestAFastForwardSyncMovesOnlyWhenNothingIsLost(t *testing.T) {
	ctx := context.Background()
	t.Run("clean and behind: moved", func(t *testing.T) {
		_, c, runID, repo, work := syncRepo(t, false)
		if err := c.SyncRun(ctx, runID, "s1", []lux.SyncRef{{Repo: "app", Ref: "work", Mode: lux.SyncFastForward}}, nil); err != nil {
			t.Fatal(err)
		}
		ev := lastSync(t, c, runID)
		want := gitIn(t, repo, "rev-parse", "work")
		if ev["status"] != "fast-forward" || ev["mode"] != "fast-forward" || ev["behind"] != 0.0 || ev["ahead"] != 0.0 {
			t.Errorf("git.sync %v", ev)
		}
		if got := gitIn(t, work, "rev-parse", "HEAD"); got != want {
			t.Errorf("HEAD %s, want %s", got, want)
		}
		if got := gitIn(t, work, "rev-parse", "refs/remotes/lux/work"); got != want {
			t.Errorf("lux/work %s, want %s", got, want)
		}
	})
	t.Run("on another branch: kept, never switched", func(t *testing.T) {
		_, c, runID, _, work := syncRepo(t, false)
		gitIn(t, work, "checkout", "-q", "main")
		before := gitIn(t, work, "rev-parse", "HEAD")
		if err := c.SyncRun(ctx, runID, "s1", []lux.SyncRef{{Repo: "app", Ref: "work", Mode: lux.SyncFastForward}}, nil); err != nil {
			t.Fatal(err)
		}
		ev := lastSync(t, c, runID)
		if ev["status"] != lux.SyncKept || ev["behind"] != 1.0 {
			t.Errorf("git.sync %v", ev)
		}
		if gitIn(t, work, "rev-parse", "HEAD") != before || gitIn(t, work, "symbolic-ref", "HEAD") != "refs/heads/main" {
			t.Error("the checkout moved or switched")
		}
	})
	t.Run("local changes: kept, saying how far behind", func(t *testing.T) {
		_, c, runID, _, work := syncRepo(t, false)
		gitIn(t, work, "-c", "user.name=t", "-c", "user.email=t@x", "commit", "-q", "--allow-empty", "-m", "seed")
		if err := os.WriteFile(filepath.Join(work, "f.txt"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		gitIn(t, work, "add", "f.txt")
		if err := c.SyncRun(ctx, runID, "s1", []lux.SyncRef{{Repo: "app", Ref: "work", Mode: lux.SyncFastForward}}, nil); err != nil {
			t.Fatal(err)
		}
		ev := lastSync(t, c, runID)
		if ev["status"] != lux.SyncKept || ev["dirty"] != true || ev["diverged"] != true || ev["behind"] != 1.0 || ev["ahead"] != 1.0 {
			t.Errorf("git.sync %v", ev)
		}
		if gitIn(t, work, "rev-parse", "refs/remotes/lux/work") == gitIn(t, work, "rev-parse", "HEAD") {
			t.Error("the checkout moved")
		}
	})
	t.Run("local commits on top: ahead", func(t *testing.T) {
		_, c, runID, repo, work := syncRepo(t, false)
		gitIn(t, work, "fetch", "-q", "origin", "work")
		gitIn(t, work, "merge", "-q", "--ff-only", gitIn(t, repo, "rev-parse", "work"))
		gitIn(t, work, "-c", "user.name=t", "-c", "user.email=t@x", "commit", "-q", "--allow-empty", "-m", "mine")
		mine := gitIn(t, work, "rev-parse", "HEAD")
		if err := c.SyncRun(ctx, runID, "s1", []lux.SyncRef{{Repo: "app", Ref: "work", Mode: lux.SyncFastForward}}, nil); err != nil {
			t.Fatal(err)
		}
		ev := lastSync(t, c, runID)
		if ev["status"] != lux.SyncAhead || ev["ahead"] != 1.0 || ev["behind"] != 0.0 {
			t.Errorf("git.sync %v", ev)
		}
		if gitIn(t, work, "rev-parse", "HEAD") != mine {
			t.Error("the checkout moved")
		}
	})
	t.Run("fetch never moves; on a resume too", func(t *testing.T) {
		_, c, runID, repo, work := syncRepo(t, false)
		before := gitIn(t, work, "rev-parse", "HEAD")
		if err := c.Stop(ctx, runID); err != nil {
			t.Fatal(err)
		}
		waitFor(t, "stopped", func() bool { r, _ := c.Get(ctx, runID); return r.State == "stopped" })
		if _, err := c.Resume(ctx, runID, lux.ResumeInput{Sync: []lux.SyncRef{{Repo: "app", Ref: "work", Mode: lux.SyncFetch}}}); err != nil {
			t.Fatal(err)
		}
		ev := lastSync(t, c, runID)
		if ev["status"] != "fetched" || ev["mode"] != "fetch" || ev["ahead"] != 0.0 || ev["behind"] != 1.0 ||
			gitIn(t, work, "rev-parse", "HEAD") != before {
			t.Errorf("git.sync %v", ev)
		}
		if gitIn(t, work, "rev-parse", "refs/remotes/lux/work") != gitIn(t, repo, "rev-parse", "work") {
			t.Error("no lux/work")
		}
	})
	t.Run("detached, a branch target: kept", func(t *testing.T) {
		_, c, runID, _, work := syncRepo(t, false)
		gitIn(t, work, "checkout", "-q", "--detach")
		before := gitIn(t, work, "rev-parse", "HEAD")
		if err := c.SyncRun(ctx, runID, "s1", []lux.SyncRef{{Repo: "app", Ref: "work", Mode: lux.SyncFastForward}}, nil); err != nil {
			t.Fatal(err)
		}
		ev := lastSync(t, c, runID)
		if ev["status"] != lux.SyncKept || ev["behind"] != 1.0 || gitIn(t, work, "rev-parse", "HEAD") != before {
			t.Errorf("detached, a branch target: %v", ev)
		}
	})
	t.Run("detached, a sha target: fast-forwarded", func(t *testing.T) {
		_, c, runID, repo, work := syncRepo(t, false)
		gitIn(t, work, "checkout", "-q", "--detach")
		want := gitIn(t, repo, "rev-parse", "work")
		if err := c.SyncRun(ctx, runID, "s1", []lux.SyncRef{{Repo: "app", Ref: want, Mode: lux.SyncFastForward}}, nil); err != nil {
			t.Fatal(err)
		}
		ev := lastSync(t, c, runID)
		if ev["status"] != "fast-forward" || gitIn(t, work, "rev-parse", "HEAD") != want {
			t.Errorf("detached, a sha target: %v", ev)
		}
	})
	t.Run("already there: up-to-date", func(t *testing.T) {
		_, c, runID, repo, work := syncRepo(t, false)
		gitIn(t, work, "fetch", "-q", "origin", "work")
		gitIn(t, work, "merge", "-q", "--ff-only", gitIn(t, repo, "rev-parse", "work"))
		if err := c.SyncRun(ctx, runID, "s1", []lux.SyncRef{{Repo: "app", Ref: "work", Mode: lux.SyncFastForward}}, nil); err != nil {
			t.Fatal(err)
		}
		ev := lastSync(t, c, runID)
		if ev["status"] != "up-to-date" || ev["ahead"] != 0.0 || ev["behind"] != 0.0 {
			t.Errorf("git.sync %v", ev)
		}
	})
	t.Run("a ref that does not exist: failed", func(t *testing.T) {
		_, c, runID, _, work := syncRepo(t, false)
		before := gitIn(t, work, "rev-parse", "HEAD")
		if err := c.SyncRun(ctx, runID, "s1", []lux.SyncRef{{Repo: "app", Ref: "nowhere", Mode: lux.SyncFastForward}}, nil); err != nil {
			t.Fatal(err)
		}
		ev := lastSync(t, c, runID)
		if ev["status"] != "failed" || ev["error"] == "" || gitIn(t, work, "rev-parse", "HEAD") != before {
			t.Errorf("git.sync %v", ev)
		}
	})
	t.Run("a fast-forward git refuses: failed, naming no commit", func(t *testing.T) {
		// An untracked file in the checkout that the target's commit adds:
		// clean as lux reads it, and git will not overwrite it.
		_, c, runID, repo, work := syncRepo(t, false)
		gitIn(t, repo, "checkout", "-q", "work")
		if err := os.WriteFile(filepath.Join(repo, "new.txt"), []byte("theirs\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		gitIn(t, repo, "add", "new.txt")
		gitIn(t, repo, "-c", "user.name=t", "-c", "user.email=t@x", "commit", "-q", "-m", "C")
		gitIn(t, repo, "checkout", "-q", "main")
		if err := os.WriteFile(filepath.Join(work, "new.txt"), []byte("mine\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		before := gitIn(t, work, "rev-parse", "HEAD")
		if err := c.SyncRun(ctx, runID, "s1", []lux.SyncRef{{Repo: "app", Ref: "work", Mode: lux.SyncFastForward}}, nil); err != nil {
			t.Fatal(err)
		}
		ev := lastSync(t, c, runID)
		if _, named := ev["to"]; ev["status"] != "failed" || named || gitIn(t, work, "rev-parse", "HEAD") != before {
			t.Errorf("git.sync %v", ev)
		}
	})
}

// A writer's checkout after it pushed is where the agent committed: on
// the branch it was checked out on, or detached. A fast-forward sync to a
// later sha moves only the detached one, as lux's.
func TestAWritersCheckoutFastForwardsToAShaOnlyWhenDetached(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name, want string
		detached   bool
	}{
		{"on its branch: kept", lux.SyncKept, false},
		{"detached: fast-forwarded", "fast-forward", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := t.TempDir()
			gitInit(t, repo)
			ref := "main"
			if tc.detached {
				ref = gitIn(t, repo, "rev-parse", "main")
			}
			fake := fakelux.New(repo, "k", nil)
			_, c, runID := startedWith(t, fake, lux.Spec{Image: lux.Image{Ref: "x"}, Workload: lux.Workload{Adapter: "generic"},
				Git: &lux.Git{Repositories: []lux.Repository{{Name: "app", URL: "file://" + repo, Ref: ref}},
					Push: &lux.Push{Branch: "pub"}}})
			work, err := fake.Checkout(runID, "app")
			if err != nil {
				t.Fatal(err)
			}
			commitFile(t, work, "mine\n", "mine")
			if res := pushN(t, c, runID, 1); res.Status != "pushed" {
				t.Fatalf("push: %+v", res)
			}
			// Someone else's commit on top of the push (a simplifier's).
			gitIn(t, repo, "checkout", "-q", "pub")
			gitIn(t, repo, "-c", "user.name=t", "-c", "user.email=t@x", "commit", "-q", "--allow-empty", "-m", "S")
			gitIn(t, repo, "checkout", "-q", "main")
			target := gitIn(t, repo, "rev-parse", "pub")
			before := gitIn(t, work, "rev-parse", "HEAD")
			if err := c.SyncRun(ctx, runID, "s1", []lux.SyncRef{{Repo: "app", Ref: target, Mode: lux.SyncFastForward}}, nil); err != nil {
				t.Fatal(err)
			}
			ev := lastSync(t, c, runID)
			if ev["status"] != tc.want || ev["behind"] != 1.0 && tc.want == lux.SyncKept {
				t.Fatalf("git.sync %v, want %s", ev, tc.want)
			}
			at := gitIn(t, work, "rev-parse", "HEAD")
			if tc.detached && at != target || !tc.detached && (at != before || gitIn(t, work, "symbolic-ref", "HEAD") != "refs/heads/main") {
				t.Fatalf("HEAD at %s (was %s), target %s", at, before, target)
			}
		})
	}
}

// A checkout first made after the Run pushed a scripted commit is cloned
// at the ref's current tip. When main gained a file after the push, that
// file is in the clone's HEAD and absent from the pushed commit: the
// checkout is left exactly at the pushed commit, so a sync to it is
// up-to-date, as lux reports for the checkout the agent committed in.
func TestACheckoutMadeAfterThePushIsAtThePushedCommitWhenMainGainedAFile(t *testing.T) {
	ctx := context.Background()
	repo := t.TempDir()
	gitInit(t, repo)
	fake := fakelux.New(repo, "k", func(map[string]any) fakelux.Behaviour {
		return fakelux.Behaviour{Commit: map[string]string{"mine.txt": "mine\n"}, Hang: true}
	})
	_, c, runID := startedWith(t, fake, lux.Spec{Image: lux.Image{Ref: "x"}, Workload: lux.Workload{Adapter: "opencode", Prompt: "go"},
		Git: &lux.Git{Repositories: []lux.Repository{{Name: "app", URL: "file://" + repo, Ref: "main"}},
			Push: &lux.Push{Branch: "pub"}}})
	res := pushN(t, c, runID, 1)
	if res.Status != "pushed" {
		t.Fatalf("push: %+v", res)
	}
	if err := os.WriteFile(filepath.Join(repo, "late.txt"), []byte("late\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, repo, "add", "late.txt")
	gitIn(t, repo, "-c", "user.name=t", "-c", "user.email=t@x", "commit", "-q", "-m", "late")
	work, err := fake.Checkout(runID, "app")
	if err != nil {
		t.Fatalf("checkout after main gained a file: %v", err)
	}
	if got := gitIn(t, work, "rev-parse", "HEAD"); got != res.Commit {
		t.Fatalf("HEAD %s, want the pushed %s", got, res.Commit)
	}
	if st := gitIn(t, work, "status", "--porcelain"); st != "" {
		t.Fatalf("checkout not at the pushed commit: %q", st)
	}
	if err := c.SyncRun(ctx, runID, "s1", []lux.SyncRef{{Repo: "app", Ref: res.Commit, Mode: lux.SyncFastForward}}, nil); err != nil {
		t.Fatal(err)
	}
	if ev := lastSync(t, c, runID); ev["status"] != "up-to-date" || ev["ahead"] != 0.0 || ev["behind"] != 0.0 || ev["dirty"] != nil {
		t.Errorf("git.sync %v, want up-to-date", ev)
	}
}

// A lux from before sync modes refuses a safe mode with a 409, on a sync
// and a resume, and still takes move.
func TestALuxWithoutSyncModesRefusesThem(t *testing.T) {
	ctx := context.Background()
	_, c, runID, _, _ := syncRepo(t, true)
	err := c.SyncRun(ctx, runID, "s1", []lux.SyncRef{{Repo: "app", Ref: "work", Mode: lux.SyncFastForward}}, nil)
	if !lux.SyncModesRefused(err) {
		t.Errorf("a fast-forward sync: %v", err)
	}
	if err := c.SyncRun(ctx, runID, "s2", []lux.SyncRef{{Repo: "app", Ref: "work"}}, nil); err != nil {
		t.Errorf("a move: %v", err)
	}
	if err := c.Stop(ctx, runID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "stopped", func() bool { r, _ := c.Get(ctx, runID); return r.State == "stopped" })
	_, err = c.Resume(ctx, runID, lux.ResumeInput{Sync: []lux.SyncRef{{Repo: "app", Ref: "work", Mode: lux.SyncFastForward}}})
	if !lux.SyncModesRefused(err) {
		t.Errorf("a resume's fast-forward: %v", err)
	}
	if lux.SyncModesRefused(&lux.Error{Status: 409, Code: "not_running"}) {
		t.Error("not_running read as a refused mode")
	}
}
