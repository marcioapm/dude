package fakelux_test

// A checkout stopped mid-operation (a conflicting rebase, merge,
// cherry-pick, revert, or a multi-commit pick between picks): its sync
// names the operation and keeps the checkout, and its push is refused,
// nothing bundled.

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

// conflictRepo is a Run whose checkout is on "work" with a commit of its
// own writing f.txt, while the repository's "work" wrote f.txt otherwise
// (lux/work in the checkout): every way of taking one into the other
// conflicts.
func conflictRepo(t *testing.T) (*lux.HTTPClient, string, string, string) {
	t.Helper()
	repo := t.TempDir()
	gitInit(t, repo)
	fake := fakelux.New(repo, "k", nil)
	_, c, runID := startedWith(t, fake, lux.Spec{Image: lux.Image{Ref: "x"}, Workload: lux.Workload{Adapter: "generic"},
		Git: &lux.Git{Repositories: []lux.Repository{{Name: "app", URL: "file://" + repo, Ref: "main"}},
			Push: &lux.Push{Branch: "pub"}}})
	work, err := fake.Checkout(runID, "app")
	if err != nil {
		t.Fatal(err)
	}
	gitIn(t, repo, "checkout", "-q", "-b", "work")
	commitFile(t, repo, "theirs\n", "theirs")
	gitIn(t, repo, "checkout", "-q", "main")
	gitIn(t, work, "checkout", "-q", "-b", "work")
	commitFile(t, work, "mine\n", "mine")
	gitIn(t, work, "fetch", "-q", "origin", "work:refs/remotes/lux/work")
	return c, runID, repo, work
}

func commitFile(t *testing.T, dir, content, msg string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, dir, "add", "f.txt")
	gitIn(t, dir, "-c", "user.name=t", "-c", "user.email=t@x", "commit", "-q", "-m", msg)
}

// gitFails runs a git command expected to stop on a conflict.
func gitFails(t *testing.T, dir string, args ...string) {
	t.Helper()
	all := append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@x"}, args...)
	if out, err := exec.Command("git", all...).CombinedOutput(); err == nil {
		t.Fatalf("git %v did not stop: %s", args, out)
	}
}

// lastPush is the Run's latest git.push event's data.
func lastPush(t *testing.T, c *lux.HTTPClient, runID string) lux.PushResult {
	t.Helper()
	var last *lux.PushResult
	waitFor(t, "a git.push", func() bool {
		frames, _ := c.Events(context.Background(), runID, 0)
		for _, f := range frames {
			if f.EventType == "git.push" {
				last = &lux.PushResult{}
				_ = json.Unmarshal(f.EventData, last)
			}
		}
		return last != nil
	})
	return *last
}

func TestACheckoutMidOperationIsKeptNamingItAndItsPushIsRefused(t *testing.T) {
	ctx := context.Background()
	for _, c := range []struct {
		op    string
		start func(t *testing.T, work string)
	}{
		{lux.OperationRebase, func(t *testing.T, work string) { gitFails(t, work, "rebase", "lux/work") }},
		{lux.OperationMerge, func(t *testing.T, work string) { gitFails(t, work, "merge", "lux/work") }},
		{lux.OperationCherryPick, func(t *testing.T, work string) { gitFails(t, work, "cherry-pick", "lux/work") }},
		{lux.OperationRevert, func(t *testing.T, work string) {
			// Reverting the first f.txt after a second rewrote it.
			commitFile(t, work, "mine again\n", "again")
			gitFails(t, work, "revert", "--no-edit", "HEAD~1")
		}},
		{lux.OperationSequencer, func(t *testing.T, work string) {
			// Two picks, the first conflicting and then committed by hand:
			// the sequencer waits for --continue with no pick in progress.
			gitIn(t, work, "branch", "two", "lux/work")
			gitIn(t, work, "checkout", "-q", "two")
			gitIn(t, work, "-c", "user.name=t", "-c", "user.email=t@x", "commit", "-q", "--allow-empty", "-m", "second")
			gitIn(t, work, "checkout", "-q", "work")
			gitFails(t, work, "cherry-pick", "two~1", "two")
			commitFile(t, work, "resolved\n", "resolved")
		}},
	} {
		t.Run(c.op, func(t *testing.T) {
			cl, runID, repo, work := conflictRepo(t)
			c.start(t, work)
			// The task branch moves on meanwhile: the checkout is behind it.
			gitIn(t, repo, "checkout", "-q", "work")
			gitIn(t, repo, "-c", "user.name=t", "-c", "user.email=t@x", "commit", "-q", "--allow-empty", "-m", "later")
			gitIn(t, repo, "checkout", "-q", "main")
			before := gitIn(t, work, "rev-parse", "HEAD")
			for _, mode := range []string{lux.SyncFastForward, lux.SyncFetch} {
				if err := cl.SyncRun(ctx, runID, "s-"+mode, []lux.SyncRef{{Repo: "app", Ref: "work", Mode: mode}}); err != nil {
					t.Fatal(err)
				}
				var ev lux.SyncResult
				raw, _ := json.Marshal(lastSync(t, cl, runID))
				_ = json.Unmarshal(raw, &ev)
				wantStatus := lux.SyncKept
				if mode == lux.SyncFetch {
					wantStatus = "fetched"
				}
				if ev.Operation != c.op || ev.Status != wantStatus || ev.Behind == 0 {
					t.Errorf("%s: git.sync %+v", mode, ev)
				}
			}
			if gitIn(t, work, "rev-parse", "HEAD") != before {
				t.Error("the sync moved the checkout")
			}
			if err := cl.Push(ctx, runID, "p1"); err != nil {
				t.Fatal(err)
			}
			push := lastPush(t, cl, runID)
			if len(push.Results) != 1 || push.Results[0].Status != lux.PushRefused || push.Results[0].Operation != c.op ||
				!strings.Contains(push.Results[0].Error, "a "+c.op+" is in progress in the checkout: finish or abort it, then push") {
				t.Errorf("git.push %+v", push)
			}
			if out, err := exec.Command("git", "-C", repo, "rev-parse", "--verify", "-q", "refs/heads/pub").CombinedOutput(); err == nil {
				t.Errorf("pushed anyway: %s", out)
			}
		})
	}
}

// With the operation finished, the same checkout syncs with no operation
// and pushes.
func TestAFinishedRebaseSyncsAndPushes(t *testing.T) {
	ctx := context.Background()
	cl, runID, repo, work := conflictRepo(t)
	gitFails(t, work, "rebase", "lux/work")
	if err := os.WriteFile(filepath.Join(work, "f.txt"), []byte("both\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, work, "add", "f.txt")
	c := exec.Command("git", "-C", work, "-c", "user.name=t", "-c", "user.email=t@x", "rebase", "--continue")
	c.Env = append(os.Environ(), "GIT_EDITOR=true")
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("rebase --continue: %v %s", err, out)
	}
	if err := cl.SyncRun(ctx, runID, "s1", []lux.SyncRef{{Repo: "app", Ref: "work", Mode: lux.SyncFastForward}}); err != nil {
		t.Fatal(err)
	}
	if ev := lastSync(t, cl, runID); ev["operation"] != nil || ev["status"] != lux.SyncAhead {
		t.Errorf("git.sync %v", ev)
	}
	if err := cl.Push(ctx, runID, "p1"); err != nil {
		t.Fatal(err)
	}
	if push := lastPush(t, cl, runID); len(push.Results) != 1 || push.Results[0].Status != "pushed" ||
		push.Results[0].Commit != gitIn(t, repo, "rev-parse", "pub") {
		t.Errorf("git.push %+v", push)
	}
}
