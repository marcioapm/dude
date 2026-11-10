package orchestrator_test

// The ready gate decides ready to merge (delivery.ReadyGate), through the
// real workflow, syncer and fake GitHub: an approval does not make a pull
// request ready when a commit on it was never reviewed, and no approval is
// needed where branch protection asks for none.

import (
	"context"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/fakegithub"
)

// Approved, green, and a person's commit on top that no review covered:
// not ready to merge. Before that commit it was.
func TestAnApprovedGreenPullRequestWithAnUnreviewedCommitIsNotReady(t *testing.T) {
	w := newWorld(t)
	wi := w.reviewing()
	w.gh.Review(1, "alice", "APPROVED")
	w.until("ready to merge", func() bool { w.sync(); return w.taskStatus(wi) == "ready_to_merge" })

	sha := w.gh.CommitFiles(w.gh.Pull(1).Head, "A tweak", map[string]string{"FACTORY.md": "tweaked\n"})
	w.gh.Review(1, "alice", "APPROVED")
	w.until("out of ready", func() bool { w.sync(); return w.taskStatus(wi) != "ready_to_merge" })
	for range 5 {
		w.sync()
		w.pump()
	}
	if s := w.taskStatus(wi); s == "ready_to_merge" {
		t.Fatalf("ready to merge with %s unreviewed, approved and green", sha[:7])
	}
	var checks, review string
	if err := w.owner.QueryRow(context.Background(), `SELECT checks::text, review::text FROM pull_requests WHERE task_id = $1`, wi).
		Scan(&checks, &review); err != nil {
		t.Fatal(err)
	}
	if checks != "passing" || review != "approved" {
		t.Fatalf("checks %s, review %s: the test is about an approved green pull request", checks, review)
	}
}

// Nobody approved it, branch protection asks nobody to, and everything
// else holds: ready to merge. Where protection asks for an approval, not
// until someone approves.
func TestAPullRequestNobodyApprovedIsReadyWhereProtectionAsksForNoApproval(t *testing.T) {
	t.Run("no approval required", func(t *testing.T) {
		w := newWorld(t)
		wi := w.reviewing()
		w.until("ready to merge", func() bool { w.sync(); return w.taskStatus(wi) == "ready_to_merge" })
		if w.gh.Pull(1).State != "open" || len(w.gh.Merges) != 0 {
			t.Fatal("the factory merged it")
		}
	})
	t.Run("an approval required", func(t *testing.T) {
		w := newWorld(t)
		w.gh.Set(func(s *fakegithub.Server) { s.RequiredApprovals = map[string]int{"main": 1} })
		wi := w.reviewing()
		for range 5 {
			w.sync()
			w.pump()
		}
		if s := w.taskStatus(wi); s == "ready_to_merge" {
			t.Fatal("ready to merge without the approval protection requires")
		}
		w.gh.Review(1, "alice", "APPROVED")
		w.until("ready once approved", func() bool { w.sync(); return w.taskStatus(wi) == "ready_to_merge" })
	})
}
