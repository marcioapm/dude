package fakelux_test

// A Run's checkpoints, as lux keeps them: per repository, its committed
// HEAD at the end of every placement, listed by GET /checkpoints after the
// Run ended for good; a Run submitted from one starts at that head.

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/fakelux"
	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

func checkpointRun(t *testing.T, fake *fakelux.Server, repo string) (*lux.HTTPClient, string, string) {
	t.Helper()
	_, c, runID := startedWith(t, fake, lux.Spec{Image: lux.Image{Ref: "x"}, Workload: lux.Workload{Adapter: "generic"},
		Git: &lux.Git{Repositories: []lux.Repository{{Name: "app", URL: "file://" + repo, Ref: "main"}},
			Push: &lux.Push{Branch: "pub"}}})
	work, err := fake.Checkout(runID, "app")
	if err != nil {
		t.Fatal(err)
	}
	return c, runID, work
}

func terminate(t *testing.T, c *lux.HTTPClient, runID string) {
	t.Helper()
	if err := c.Cancel(context.Background(), runID); err != nil {
		t.Fatal(err)
	}
}

func TestAnEndedRunHasACheckpointOfItsCommittedHead(t *testing.T) {
	repo := t.TempDir()
	gitInit(t, repo)
	fake := fakelux.New(repo, "k", nil)
	c, runID, work := checkpointRun(t, fake, repo)
	commitFile(t, work, "one\n", "one")
	committed := gitIn(t, work, "rev-parse", "HEAD")
	terminate(t, c, runID)

	cps, err := c.Checkpoints(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	want := lux.Checkpoint{Repo: "app", Head: committed, Uploaded: true, Branch: "main"}
	if len(cps) != 1 || cps[0] != want {
		t.Fatalf("checkpoints %+v, want [%+v]", cps, want)
	}
}

func TestUncommittedWorkMarksTheCheckpointDirty(t *testing.T) {
	repo := t.TempDir()
	gitInit(t, repo)
	fake := fakelux.New(repo, "k", nil)
	c, runID, work := checkpointRun(t, fake, repo)
	commitFile(t, work, "one\n", "one")
	committed := gitIn(t, work, "rev-parse", "HEAD")
	if err := os.WriteFile(filepath.Join(work, "loose.txt"), []byte("not committed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	terminate(t, c, runID)
	cps, err := c.Checkpoints(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	if len(cps) != 1 || !cps[0].Dirty || cps[0].Head != committed {
		t.Fatalf("checkpoints %+v: want dirty at %s", cps, committed)
	}
}

func TestASubmitFromACheckpointClonesItsHead(t *testing.T) {
	repo := t.TempDir()
	gitInit(t, repo)
	fake := fakelux.New(repo, "k", nil)
	c, runID, work := checkpointRun(t, fake, repo)
	commitFile(t, work, "one\n", "one")
	committed := gitIn(t, work, "rev-parse", "HEAD")
	terminate(t, c, runID)

	pub, err := c.Submit(context.Background(), lux.Spec{Image: lux.Image{Ref: "x"},
		Workload: lux.Workload{Adapter: "generic", Command: []string{"sleep", "infinity"}}, ResumePolicy: "never",
		Git: &lux.Git{Repositories: []lux.Repository{{Name: "app", From: &lux.RepositoryFrom{Run: runID, Repo: "app"}}},
			Push: &lux.Push{Branch: "recovered"}}}, "recover:1")
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the publisher to run", func() bool {
		r, _ := c.Get(context.Background(), pub.ID)
		return r.State == "running"
	})
	clone, err := fake.Checkout(pub.ID, "app")
	if err != nil {
		t.Fatal(err)
	}
	if got := gitIn(t, clone, "rev-parse", "HEAD"); got != committed {
		t.Fatalf("the clone is at %s, want the checkpoint's %s", got, committed)
	}
	if res := pushN(t, c, pub.ID, 1); res.Status != "pushed" || res.Commit != committed {
		t.Fatalf("push %+v", res)
	}
	if got := gitIn(t, repo, "rev-parse", "recovered"); got != committed {
		t.Fatalf("recovered at %s, want %s", got, committed)
	}
}

func TestNoCheckpointsAndANotUploadedOne(t *testing.T) {
	repo := t.TempDir()
	gitInit(t, repo)
	none := fakelux.New(repo, "k", nil)
	none.NoCheckpoints = true
	c, runID, work := checkpointRun(t, none, repo)
	commitFile(t, work, "one\n", "one")
	terminate(t, c, runID)
	if cps, err := c.Checkpoints(context.Background(), runID); err != nil || len(cps) != 0 {
		t.Fatalf("NoCheckpoints listed %+v %v", cps, err)
	}

	fake := fakelux.New(repo, "k", nil)
	c, runID, work = checkpointRun(t, fake, repo)
	fake.CheckpointNotUploaded(runID)
	commitFile(t, work, "two\n", "two")
	terminate(t, c, runID)
	cps, err := c.Checkpoints(context.Background(), runID)
	if err != nil || len(cps) != 1 || cps[0].Uploaded {
		t.Fatalf("checkpoints %+v %v: want one, not uploaded", cps, err)
	}
	_, err = c.Submit(context.Background(), lux.Spec{Image: lux.Image{Ref: "x"}, Workload: lux.Workload{Adapter: "generic"},
		Git: &lux.Git{Repositories: []lux.Repository{{Name: "app", From: &lux.RepositoryFrom{Run: runID, Repo: "app"}}},
			Push: &lux.Push{Branch: "recovered"}}}, "recover:2")
	if le, ok := lux.AsError(err); !ok || le.Status != 422 {
		t.Fatalf("a submit from a checkpoint not uploaded: %v", err)
	}
}
