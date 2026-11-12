package orchestrator_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
)

// started says the start of a review-only task was taken: since the
// verdict step, a review-only task starts as any task does.
func started(t *testing.T, what string, status int, out map[string]any) {
	t.Helper()
	if status >= 300 {
		t.Fatalf("%s of a review-only task: %d %v, want it started", what, status, out)
	}
}

// A review-only task starts from Deliver (PR 2 refused every start until
// the verdict step): one delivery, which reaches its lenses and never a
// writer.
func TestAReviewOnlyTaskStartsAndReachesReview(t *testing.T) {
	w := newWorld(t)
	n := w.gh.OpenAsPerson("alice", "feature/greet", "main", "Greet")
	wi := w.adopted("pr", n, "feature/greet", "main", delivery.ModeReviewOnly)
	ana := w.person("Ana")
	w.assignOwner(wi, ana)
	status, out := w.call("/internal/tasks/"+wi+"/deliver", map[string]any{})
	started(t, "Deliver", status, out)
	w.agentsDone(wi, "its lenses ran", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'review'`, wi) > 0
	})
	if n := w.count(`SELECT count(*) FROM workflow_runs WHERE task_id = $1`, wi); n != 1 {
		t.Fatalf("%d deliveries", n)
	}
	if n := w.writers(wi); n != 0 {
		t.Fatalf("%d writer Runs on a review-only task", n)
	}
}

// A stopped review-only task is picked back up as any task is (PR 2
// refused it until the verdict step): Start over makes a new delivery.
func TestAStoppedReviewOnlyTaskIsPickedBackUp(t *testing.T) {
	w := newWorld(t)
	n := w.gh.OpenAsPerson("alice", "feature/greet", "main", "Greet")
	ana := w.person("Ana")
	wi, _ := w.holding(n, "feature/greet", delivery.ModeReviewOnly, ana)
	w.stop(ana, wi, map[string]any{})
	status, out := w.callAs(ana, "/internal/tasks/"+wi+"/recover", map[string]any{"action": "restart"})
	started(t, "Start over", status, out)
	if n := w.count(`SELECT count(*) FROM workflow_runs WHERE task_id = $1 AND status IN ('running', 'waiting')`, wi); n != 1 {
		t.Fatalf("%d live deliveries after Start over", n)
	}
}

// A person's Update branch on a review-only task's pull request is
// refused: the task changes no code, not even a merge of its base onto
// the author's branch. On a task in a writing mode it goes to GitHub.
func TestUpdateBranchIsRefusedOnAReviewOnlyTasksPullRequest(t *testing.T) {
	for _, mode := range []string{delivery.ModeReviewOnly, delivery.ModeReview} {
		t.Run(mode, func(t *testing.T) {
			w := newWorld(t)
			n := w.gh.OpenAsPerson("alice", "feature/greet", "main", "Greet")
			ana := w.person("Ana")
			wi, _ := w.holding(n, "feature/greet", mode, ana)
			prID := "pr_" + wi
			mustExec(t, w.owner, `INSERT INTO pull_requests (id, organization_id, project_id, task_id, repository_id, number, url,
				head_branch, base_branch, head_sha, title, state, checks)
				SELECT $1, organization_id, project_id, $2, id, $3::int, url || '/pull/' || $3::text, 'feature/greet', 'main', $4, 'Greet', 'open', 'passing'
				FROM repositories WHERE id = $5`, prID, wi, n, w.gh.SHA("feature/greet"), w.repoID)
			status, out := w.callAs(ana, "/internal/pull-requests/"+prID+"/update-branch", map[string]any{})
			if mode == delivery.ModeReviewOnly {
				e, _ := out["error"].(map[string]any)
				if status != 409 || e["code"] != "review_only" || !strings.Contains(fmt.Sprint(e["message"]), "update-branch is not dude's to take here") {
					t.Fatalf("Update branch on a review-only task: %d %v", status, out)
				}
				if w.updateCalls() != 0 {
					t.Fatal("GitHub was asked to update the author's branch")
				}
				return
			}
			if status != 200 || w.updateCalls() != 1 {
				t.Fatalf("Update branch on a %s task: %d %v, %d GitHub updates", mode, status, out, w.updateCalls())
			}
		})
	}
}

// Picking a stopped review-only task back up beside another live task on
// its pull request answers the same whichever way: Resume and Try again
// hold no branch for it, as Start over (NewState) holds none, so none is
// refused superseded_required.
func TestAStoppedReviewOnlyTaskBesideAHolderIsAnsweredAlikeEveryWay(t *testing.T) {
	for _, action := range []string{"restart", "resume", "retry"} {
		t.Run(action, func(t *testing.T) {
			w := newWorld(t)
			n := w.gh.OpenAsPerson("alice", "feature/greet", "main", "Greet")
			ana := w.person("Ana")
			wi, _ := w.holding(n, "feature/greet", delivery.ModeReviewOnly, ana)
			w.stop(ana, wi, map[string]any{})
			w.holding(n, "feature/greet", delivery.ModeReview, ana)
			status, out := w.callAs(ana, "/internal/tasks/"+wi+"/recover", map[string]any{"action": action})
			if e, _ := out["error"].(map[string]any); e != nil && e["code"] == "superseded_required" {
				t.Fatalf("%s beside a holder: %d %v", action, status, out)
			}
		})
	}
}
