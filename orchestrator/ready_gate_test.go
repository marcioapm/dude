package orchestrator_test

// The ready gate decides ready to merge (delivery.ReadyGate), through the
// real workflow, syncer and fake GitHub: an approval does not make a pull
// request ready when a commit on it was never reviewed, and no approval is
// needed where branch protection asks for none.

import (
	"context"
	"encoding/json"
	"slices"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
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

// GET /internal/pull-requests/{id}/ready-gate answers the pull request's
// gate, every item in order, read now; it changes nothing. Another
// organization's pull request is not found.
func TestTheReadyGateEndpointAnswersEveryItem(t *testing.T) {
	w := newWorld(t)
	wi := w.reviewing()
	var id string
	if err := w.owner.QueryRow(context.Background(), `SELECT id FROM pull_requests WHERE task_id = $1`, wi).Scan(&id); err != nil {
		t.Fatal(err)
	}
	events := w.count(`SELECT count(*) FROM events WHERE task_id = $1`, wi)
	w.gh.Set(func(s *fakegithub.Server) { s.Unresolved[1] = 1 })
	status, body := w.get("/internal/pull-requests/"+id+"/ready-gate", w.org)
	if status != 200 {
		t.Fatalf("ready-gate: %d %s", status, body)
	}
	var out struct {
		PullRequestID, TaskID, Repo, Head string
		Number                            int
		Ready                             bool
		Items                             []delivery.GateItem
	}
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatal(err)
	}
	if out.PullRequestID != id || out.TaskID != wi || out.Repo != "target" || out.Number != 1 || out.Ready || len(out.Items) != len(delivery.ReadyKeys) {
		t.Fatalf("ready-gate: %s", body)
	}
	for i, it := range out.Items {
		want := it.Key != delivery.ReadyThreads
		if it.Key != delivery.ReadyKeys[i] || it.OK != want {
			t.Errorf("item %d: %+v, want %s ok %v", i, it, delivery.ReadyKeys[i], want)
		}
	}
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1`, wi); n != events {
		t.Errorf("reading the gate recorded %d events", n-events)
	}
	if status, _ := w.get("/internal/pull-requests/"+id+"/ready-gate", "org_someone_else"); status != 404 {
		t.Errorf("another organization's read: %d, want 404", status)
	}
	if status, _ := w.get("/internal/pull-requests/pr_nope/ready-gate", w.org); status != 404 {
		t.Errorf("an unknown pull request: %d, want 404", status)
	}
}

// Under the default policy, the simplifier on: a person's comment on a
// pull request that was ready to merge is fixed, the fix reviewed, the
// simplifier runs on it by itself and its commit is reviewed, and the task
// is ready to merge again. Nobody starts a phase by hand.
func TestAPullRequestFixIsSimplifiedAndReadyAgainUnderTheDefaultPolicy(t *testing.T) {
	w := newWorld(t)
	wi := w.reviewing()
	w.until("ready to merge", func() bool { w.sync(); return w.taskStatus(wi) == "ready_to_merge" })
	phases := len(w.phaseOrder(wi))

	w.gh.Comment(1, "alice", "Please also rename foo to bar.")
	w.until("a fixer", func() bool { w.sync(); return w.taskStatus(wi) == "running" })
	w.until("ready to merge again", func() bool { w.sync(); w.pump(); return w.taskStatus(wi) == "ready_to_merge" })

	if got := w.phaseOrder(wi)[phases:]; !slices.Equal(got, []string{"fix", "review", "simplify", "review"}) {
		t.Fatalf("phases after the comment: %v, want the fix, its review, the simplifier and its review", got)
	}
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'task.ready_to_merge'`, wi); n != 2 {
		t.Errorf("%d ready-to-merge events, want two", n)
	}
}

// GitHub works mergeability out lazily: after the base is merged in, it
// reads unknown for a moment. That is no reading: the task is not moved
// out of ready to merge for it, and once GitHub says clean it is ready to
// merge, though the syncer, keeping its last reading in unknown's place,
// sees no move.
func TestUnknownMergeabilityAfterABaseMergeComesBackToReady(t *testing.T) {
	w := newWorld(t)
	wi := w.reviewing()
	w.until("ready to merge", func() bool { w.sync(); return w.taskStatus(wi) == "ready_to_merge" })
	w.gh.CommitFiles("main", "Other work", map[string]string{"OTHER.md": "other\n"})
	w.gh.Set(func(s *fakegithub.Server) { s.MergeableUnknown[1] = true })
	if w.gh.MergeInto(w.gh.Pull(1).Head, "main", nil) == "" {
		t.Fatal("could not merge main in")
	}
	for range 5 {
		w.sync()
		w.pump()
	}
	w.gh.Set(func(s *fakegithub.Server) { delete(s.MergeableUnknown, 1) })
	w.until("ready to merge again", func() bool { w.sync(); return w.taskStatus(wi) == "ready_to_merge" })
}

// A pull request green and reviewed, held only by a reviewer's question:
// once its owner answers it, the task is ready to merge, with nothing
// changed on GitHub and no sync.
func TestAnsweringTheLastQuestionMakesThePullRequestReady(t *testing.T) {
	w := newWorld(t)
	ana := w.person("Ana")
	wi := w.reviewing()
	w.assignOwner(wi, ana)
	qs := w.askedQuestions(wi, "Do invoices close in UTC?")
	for range 5 {
		w.sync()
		w.pump()
	}
	if s := w.taskStatus(wi); s != "review" {
		t.Fatalf("%s with a reviewer's question open", s)
	}
	requests := len(w.gh.Requests())
	if status, body := w.callAs(ana, "/internal/review-questions/"+qs[0]+"/answer", map[string]any{"answer": "UTC"}); status != 200 {
		t.Fatalf("answer: %d %v", status, body)
	}
	w.until("ready to merge", func() bool { return w.taskStatus(wi) == "ready_to_merge" })
	if len(w.gh.Requests()) == requests {
		t.Fatal("ready with no read of GitHub")
	}
}
