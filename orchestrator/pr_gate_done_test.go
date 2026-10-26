package orchestrator_test

// Done without a pull request: the owner's answer to Deliver's hold before
// the pull request.

import (
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
)

// The owner finishes a held task without a pull request: Done, its branch
// still on GitHub, none opened, the delivery ended with nothing live, and
// the answer recorded. Someone else is refused; a task not held, or one
// with a pull request on record, is refused too.
func TestDoneWithoutAPullRequestKeepsTheBranchAndFinishes(t *testing.T) {
	w := newWorld(t)
	ana, bo := w.person("Ana"), w.person("Bo")
	task := w.atTheHold(ana)
	branch := delivery.BranchFor(task, 1)
	if w.gh.SHA(branch) == "" {
		t.Fatalf("the branch %s is not on GitHub at the hold", branch)
	}
	path := "/internal/tasks/" + task + "/done-without-pull-request"
	if status, out := w.callAs(bo, path, map[string]any{}); status != 403 {
		t.Fatalf("someone else's Done without: %d %v", status, out)
	}
	if status, out := w.callAs(ana, path, map[string]any{}); status != 200 {
		t.Fatalf("the owner's Done without: %d %v", status, out)
	}
	for range 3 {
		w.pump()
	}
	if s := w.taskStatus(task); s != "done" {
		t.Fatalf("task %s, want done", s)
	}
	if len(w.gh.Pulls()) != 0 || w.gh.SHA(branch) == "" {
		t.Fatalf("%d pull requests; branch kept: %v", len(w.gh.Pulls()), w.gh.SHA(branch) != "")
	}
	if n := w.count(`SELECT count(*) FROM workflow_runs WHERE task_id = $1 AND status IN ('running', 'waiting')`, task); n != 0 {
		t.Errorf("%d deliveries still live", n)
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND status IN ('pending', 'scheduled', 'starting', 'running', 'paused')`, task); n != 0 {
		t.Errorf("%d Runs still live", n)
	}
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = $2 AND payload->>'branch' = $3`,
		task, delivery.EvDoneWithoutPullRequest, branch); n != 1 {
		t.Errorf("%d done-without events", n)
	}
	if status, out := w.callAs(ana, path, map[string]any{}); status != 409 {
		t.Fatalf("again, once done: %d %v", status, out)
	}

	// A task whose pull request is open is never finished this way.
	open := w.reviewing()
	w.assignOwner(open, ana)
	if status, out := w.callAs(ana, "/internal/tasks/"+open+"/done-without-pull-request", map[string]any{}); status != 409 {
		t.Fatalf("with a pull request open: %d %v", status, out)
	}
	if s := w.taskStatus(open); s == "done" {
		t.Fatal("a task with an open pull request was finished without it")
	}
}

// A pull request on record for a task held at the gate refuses it with
// "The pull request is already open."
func TestDoneWithoutAPullRequestIsRefusedOnceOneIsOnRecord(t *testing.T) {
	w := newWorld(t)
	ana := w.person("Ana")
	task := w.atTheHold(ana)
	mustExec(t, w.owner, `INSERT INTO pull_requests (id, organization_id, project_id, task_id, repository_id, number, url, head_branch,
			base_branch, head_sha, title, state)
		VALUES ('pr_rec_'||$1, $2, $3, $1, $4, 9, 'https://x/9', 'b', 'main', 'h', 'T', 'closed')`, task, w.org, w.project, w.repoID)
	status, out := w.callAs(ana, "/internal/tasks/"+task+"/done-without-pull-request", map[string]any{})
	e, _ := out["error"].(map[string]any)
	if status != 409 || e["code"] != "pull_request_open" || e["message"] != delivery.PullRequestOpenText {
		t.Fatalf("with a pull request on record: %d %v", status, out)
	}
	if s := w.taskStatus(task); s == "done" {
		t.Fatal("finished")
	}
}
