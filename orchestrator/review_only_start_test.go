package orchestrator_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
)

// notYet says the answer is the review-only start refusal.
func notYet(t *testing.T, what string, status int, out map[string]any) {
	t.Helper()
	e, _ := out["error"].(map[string]any)
	msg, _ := e["message"].(string)
	if status != 409 || e["code"] != "review_only_not_yet" ||
		!strings.Contains(msg, "can be created, but reviewing it arrives with the verdict step") {
		t.Fatalf("%s of a review-only task: %d %v, want 409 review_only_not_yet", what, status, out)
	}
}

// Until the verdict step that ends its delivery arrives, a review-only
// task is created but never started, whatever starts it: Deliver, Talk it
// through, a Chat message, Start when unblocked. No delivery is made.
func TestAReviewOnlyTaskIsRefusedAtEveryStart(t *testing.T) {
	w := newWorld(t)
	n := w.gh.OpenAsPerson("alice", "feature/greet", "main", "Greet")
	wi := w.adopted("pr", n, "feature/greet", "main", delivery.ModeReviewOnly)
	ana := w.person("Ana")
	w.assignOwner(wi, ana)
	status, out := w.call("/internal/tasks/"+wi+"/deliver", map[string]any{})
	notYet(t, "Deliver", status, out)
	status, out = w.callAs(ana, "/internal/tasks/"+wi+"/talk", map[string]any{})
	notYet(t, "Talk it through", status, out)
	status, out = w.callAs(ana, "/internal/tasks/"+wi+"/chat", map[string]any{"text": "review it please"})
	notYet(t, "a Chat start", status, out)
	w.autoStart(wi, "deliver")
	mustExec(t, w.owner, `UPDATE task_auto_starts SET due_at = now() WHERE task_id = $1`, wi)
	w.fireAutoStarts()
	if n := w.count(`SELECT count(*) FROM task_auto_starts WHERE task_id = $1`, wi); n != 0 {
		t.Errorf("the start when unblocked is still standing")
	}
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'task.auto_start_cancelled'
		AND payload->>'error' LIKE '%verdict step%'`, wi); n != 1 {
		t.Errorf("the start when unblocked did not end with the refusal")
	}
	if n := w.count(`SELECT count(*) FROM workflow_runs WHERE task_id = $1`, wi); n != 0 {
		t.Fatalf("%d deliveries made for a review-only task", n)
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE task_id = $1`, wi); n != 0 {
		t.Fatalf("%d Runs made for a review-only task", n)
	}
	if s := w.taskStatus(wi); !delivery.NotStarted(s) {
		t.Fatalf("the refused task is %s", s)
	}
}

// A review-only task stopped (one from before the refusal) is not picked
// back up either: Stop and start over, Start over, Resume and Try again
// are refused, and no other delivery is made.
func TestAStoppedReviewOnlyTaskIsNotPickedBackUp(t *testing.T) {
	w := newWorld(t)
	n := w.gh.OpenAsPerson("alice", "feature/greet", "main", "Greet")
	ana := w.person("Ana")
	wi, _ := w.holding(n, "feature/greet", delivery.ModeReviewOnly, ana)
	status, out := w.callAs(ana, "/internal/tasks/"+wi+"/stop", map[string]any{"andRestart": true})
	notYet(t, "Stop and start over", status, out)
	if s := w.taskStatus(wi); s != "running" {
		t.Fatalf("a refused Stop and start over left the task %s", s)
	}
	w.stop(ana, wi, map[string]any{})
	for _, action := range []string{"restart", "resume", "retry"} {
		status, out := w.callAs(ana, "/internal/tasks/"+wi+"/recover", map[string]any{"action": action})
		notYet(t, action, status, out)
	}
	if n := w.count(`SELECT count(*) FROM workflow_runs WHERE task_id = $1`, wi); n != 1 {
		t.Fatalf("%d deliveries, want the stopped one alone", n)
	}
	if n := w.count(`SELECT count(*) FROM workflow_runs WHERE task_id = $1 AND status IN ('running', 'waiting')`, wi); n != 0 {
		t.Fatalf("the stopped delivery was picked back up")
	}
	if s := w.taskStatus(wi); s != "aborted" {
		t.Fatalf("the refused task is %s", s)
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
// hold no branch for it, as Start over (NewState) holds none, so neither
// is refused superseded_required while the other is not.
func TestAStoppedReviewOnlyTaskBesideAHolderIsAnsweredAlikeEveryWay(t *testing.T) {
	w := newWorld(t)
	n := w.gh.OpenAsPerson("alice", "feature/greet", "main", "Greet")
	ana := w.person("Ana")
	wi, _ := w.holding(n, "feature/greet", delivery.ModeReviewOnly, ana)
	w.stop(ana, wi, map[string]any{})
	w.holding(n, "feature/greet", delivery.ModeReview, ana)
	for _, action := range []string{"restart", "resume", "retry"} {
		status, out := w.callAs(ana, "/internal/tasks/"+wi+"/recover", map[string]any{"action": action})
		notYet(t, action, status, out)
	}
}
