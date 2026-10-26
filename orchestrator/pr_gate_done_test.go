package orchestrator_test

// Done without a pull request: the owner's answer to Deliver's hold before
// the pull request.

import (
	"context"
	"encoding/json"
	"maps"
	"slices"
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

// A delivery still at work is not held: Done without a pull request is
// refused with not_held, and the delivery keeps working to its pull
// request.
func TestDoneWithoutAPullRequestIsRefusedWhileTheDeliveryWorks(t *testing.T) {
	w := newWorld(t)
	ana := w.person("Ana")
	task := w.task()
	w.assignOwner(task, ana)
	w.deliver(task)
	w.until("a Run at work", func() bool { return w.count(`SELECT count(*) FROM runs WHERE task_id = $1`, task) > 0 })
	status, out := w.callAs(ana, "/internal/tasks/"+task+"/done-without-pull-request", map[string]any{})
	e, _ := out["error"].(map[string]any)
	if status != 409 || e["code"] != "not_held" {
		t.Fatalf("Done without while the delivery works: %d %v", status, out)
	}
	w.until("the pull request", func() bool { return len(w.gh.Pulls()) == 1 })
	if s := w.taskStatus(task); s == "done" {
		t.Fatalf("task %s after a refused Done without", s)
	}
}

// Under the conductor, at its gate before the pull request, Done without a
// pull request is refused: the gate's answer is the conductor's question.
// Handed back at the gate, Deliver holds it and asks on the conductor; the
// owner's Done without then cancels that question, ends every reason to
// wake the conductor, and records the repositories and heads it kept.
func TestDoneWithoutAPullRequestUnderTheConductor(t *testing.T) {
	w := conducting(t)
	ana := w.person("Ana")
	task := w.task()
	w.assignOwner(task, ana)
	w.reachGate(task)
	path := "/internal/tasks/" + task + "/done-without-pull-request"
	status, out := w.callAs(ana, path, map[string]any{})
	if e, _ := out["error"].(map[string]any); status != 409 || e["code"] != "not_held" {
		t.Fatalf("Done without at the conductor's gate: %d %v", status, out)
	}
	if s := w.taskStatus(task); s == "done" {
		t.Fatal("finished under the conductor")
	}

	if status, out := w.handBack(task, map[string]any{"decider": "policy"}); status != 200 {
		t.Fatalf("hand-back at the gate: %d %v", status, out)
	}
	w.until("Deliver's gate question", func() bool { return w.conductorQuestion(task) != "" || len(w.gh.Pulls()) > 0 })
	if n := len(w.gh.Pulls()); n != 0 {
		t.Fatalf("%d pull requests opened with the gate unanswered", n)
	}
	var heads map[string]string
	_ = w.owner.QueryRow(context.Background(), `SELECT state->'heads' FROM workflow_runs WHERE task_id = $1`, task).Scan(&heads)
	if status, out := w.callAs(ana, path, map[string]any{}); status != 200 {
		t.Fatalf("Done without once handed back: %d %v", status, out)
	}
	if s := w.taskStatus(task); s != "done" {
		t.Fatalf("task %s, want done", s)
	}
	if n := w.count(`SELECT count(*) FROM questions WHERE task_id = $1 AND pr_gate_heads IS NOT NULL AND status = 'open'`, task); n != 0 {
		t.Errorf("%d gate questions still open", n)
	}
	if n := w.count(`SELECT count(*) FROM questions WHERE task_id = $1 AND pr_gate_heads IS NOT NULL AND status = 'cancelled'`, task); n != 1 {
		t.Errorf("%d gate questions cancelled, want 1", n)
	}
	if n := w.count(`SELECT count(*) FROM conductor_wakes WHERE task_id = $1 AND delivered_at IS NULL`, task); n != 0 {
		t.Errorf("%d reasons to wake the conductor still live", n)
	}
	var payload struct {
		Repositories []string          `json:"repositories"`
		Heads        map[string]string `json:"heads"`
	}
	var raw []byte
	if err := w.owner.QueryRow(context.Background(), `SELECT payload FROM events WHERE task_id = $1 AND event_type = $2`,
		task, delivery.EvDoneWithoutPullRequest).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	_ = json.Unmarshal(raw, &payload)
	if !slices.Equal(payload.Repositories, []string{"target"}) || !maps.Equal(payload.Heads, heads) || heads["target"] == "" {
		t.Errorf("event repositories %v heads %v, want [target] and %v", payload.Repositories, payload.Heads, heads)
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

// Feedback still open at the hold (a non-owner's, submitted before the
// delivery woke) refuses Done without a pull request as it refuses Mark
// done: the same code and message, and the task stays held.
func TestDoneWithoutAPullRequestIsRefusedWhileFeedbackIsOpen(t *testing.T) {
	w := newWorld(t)
	ana := w.person("Ana")
	task := w.atTheHold(ana)
	mustExec(t, w.owner, `INSERT INTO feedback_batches (id, organization_id, task_id, attempt, author_person_id, submission_key)
		SELECT 'fbt_dw', $1, $2, task_attempt($2), person_id, 'dw' FROM api_keys WHERE id = $3`, w.org, task, ana)
	for _, id := range []string{"fbi_dw1", "fbi_dw2"} {
		mustExec(t, w.owner, `INSERT INTO feedback_items (id, organization_id, task_id, attempt, batch_id, author_person_id, body, anchor_kind,
				anchor, status, submitted_at)
			SELECT $4, $1, $2, task_attempt($2), 'fbt_dw', person_id, 'Open', 'general', '{"kind":"general"}', 'open', now()
			FROM api_keys WHERE id = $3`, w.org, task, ana, id)
	}
	status, out := w.callAs(ana, "/internal/tasks/"+task+"/done-without-pull-request", map[string]any{})
	e, _ := out["error"].(map[string]any)
	if status != 409 || e["code"] != "feedback_open" || e["message"] != delivery.FeedbackNotHandledText(2) {
		t.Fatalf("Done without with two items open: %d %v", status, out)
	}
	if e["message"] != "2 feedback items from people are not handled yet" {
		t.Fatalf("message %q is not Mark done's", e["message"])
	}
	if s := w.taskStatus(task); s == "done" || !w.heldBeforePR(task) {
		t.Fatalf("task %s, held %v after the refusal", s, w.heldBeforePR(task))
	}
}
