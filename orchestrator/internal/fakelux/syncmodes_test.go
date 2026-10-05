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
		if err := c.SyncRun(ctx, runID, "s1", []lux.SyncRef{{Repo: "app", Ref: "work", Mode: lux.SyncFastForward}}); err != nil {
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
	t.Run("local changes: kept, saying how far behind", func(t *testing.T) {
		_, c, runID, _, work := syncRepo(t, false)
		gitIn(t, work, "-c", "user.name=t", "-c", "user.email=t@x", "commit", "-q", "--allow-empty", "-m", "seed")
		if err := os.WriteFile(filepath.Join(work, "f.txt"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		gitIn(t, work, "add", "f.txt")
		if err := c.SyncRun(ctx, runID, "s1", []lux.SyncRef{{Repo: "app", Ref: "work", Mode: lux.SyncFastForward}}); err != nil {
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
		if err := c.SyncRun(ctx, runID, "s1", []lux.SyncRef{{Repo: "app", Ref: "work", Mode: lux.SyncFastForward}}); err != nil {
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
		if ev["mode"] != "fetch" || ev["behind"] != 1.0 || gitIn(t, work, "rev-parse", "HEAD") != before {
			t.Errorf("git.sync %v", ev)
		}
		if gitIn(t, work, "rev-parse", "refs/remotes/lux/work") != gitIn(t, repo, "rev-parse", "work") {
			t.Error("no lux/work")
		}
	})
}

// A lux from before sync modes refuses a safe mode with a 409, on a sync
// and a resume, and still takes move.
func TestALuxWithoutSyncModesRefusesThem(t *testing.T) {
	ctx := context.Background()
	_, c, runID, _, _ := syncRepo(t, true)
	err := c.SyncRun(ctx, runID, "s1", []lux.SyncRef{{Repo: "app", Ref: "work", Mode: lux.SyncFastForward}})
	if !lux.SyncModesRefused(err) {
		t.Errorf("a fast-forward sync: %v", err)
	}
	if err := c.SyncRun(ctx, runID, "s2", []lux.SyncRef{{Repo: "app", Ref: "work"}}); err != nil {
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
