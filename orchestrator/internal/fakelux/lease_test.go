package fakelux_test

// A push's lease, as lux's: the push branch must be where this Run last
// pushed it, or absent before its first push; otherwise the push is
// rejected and the branch is left as it is.

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/fakelux"
	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// pushN pushes the Run and returns that push's result for its one repository.
func pushN(t *testing.T, c *lux.HTTPClient, runID string, n int) lux.PushRepoResult {
	t.Helper()
	id := fmt.Sprintf("p%d", n)
	if err := c.Push(context.Background(), runID, id, nil); err != nil {
		t.Fatal(err)
	}
	var got *lux.PushResult
	waitFor(t, "git.push "+id, func() bool {
		frames, _ := c.Events(context.Background(), runID, 0)
		for _, f := range frames {
			var p lux.PushResult
			if f.EventType == "git.push" && json.Unmarshal(f.EventData, &p) == nil && p.RequestID == id {
				got = &p
			}
		}
		return got != nil
	})
	if len(got.Results) != 1 {
		t.Fatalf("push %s: %+v", id, got)
	}
	return got.Results[0]
}

func TestAPushIsRejectedUnlessTheBranchIsWhereThisRunLastPushedIt(t *testing.T) {
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

	commitFile(t, work, "one\n", "one")
	if res := pushN(t, c, runID, 1); res.Status != "pushed" || res.Commit != gitIn(t, repo, "rev-parse", "pub") {
		t.Fatalf("first push (branch absent): %+v", res)
	}
	commitFile(t, work, "two\n", "two")
	if res := pushN(t, c, runID, 2); res.Status != "pushed" || res.Commit != gitIn(t, repo, "rev-parse", "pub") {
		t.Fatalf("second push (branch at its last push): %+v", res)
	}

	// Deleted after its push: the lease (its last push) no longer holds.
	gitIn(t, repo, "update-ref", "-d", "refs/heads/pub")
	commitFile(t, work, "three\n", "three")
	if res := pushN(t, c, runID, 3); res.Status != "rejected" || res.Error == "" {
		t.Fatalf("push after the branch was deleted: %+v, want rejected", res)
	}
	if out := gitIn(t, repo, "branch", "--list", "pub"); out != "" {
		t.Fatalf("a rejected push created the branch: %q", out)
	}

	// Moved by someone else: rejected, the branch left where they put it.
	gitIn(t, repo, "branch", "pub", "main")
	if res := pushN(t, c, runID, 4); res.Status != "rejected" {
		t.Fatalf("push over someone else's branch: %+v, want rejected", res)
	}
	if got, want := gitIn(t, repo, "rev-parse", "pub"), gitIn(t, repo, "rev-parse", "main"); got != want {
		t.Fatalf("pub at %s, want %s", got, want)
	}
}
