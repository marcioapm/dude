package orchestrator_test

// Stop and Close of a task whose delivery is parked waiting on a person at
// the pull request gate (delivery.parkAtGate): its phases are done and no
// Run is live, yet the person can end it, and nothing is published after.

import (
	"strings"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
)

// parkedAtGate is a branch taken over under Deliver, reviewed and
// simplified, parked for its owner's Open or Draft: the task and its owner.
func (w *world) parkedAtGate() (task, ana string) {
	w.t.Helper()
	w.gh.Branch("feature/parked", "main", "Parked")
	task = w.adopted("branch", 0, "feature/parked", "main", delivery.ModeReview)
	ana = w.ownedBy(task, "Ana")
	w.startDelivery(task)
	w.until("the person's Open or Draft", func() bool { return w.taskStatus(task) == "awaiting_input" })
	for range 3 {
		w.pump()
	}
	if n := len(w.gh.Pulls()); n != 0 {
		w.t.Fatalf("%d pull requests opened before the Open or Draft", n)
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND status NOT IN ('completed', 'failed', 'aborted')`, task); n != 0 {
		w.t.Fatalf("%d Runs live at the gate\n%s", n, w.describeRuns())
	}
	if n := w.count(`SELECT count(*) FROM workflow_runs WHERE task_id = $1 AND status = 'waiting'
		AND state->'decision'->>'point' = $2`, task, delivery.PointBeforePR); n != 1 {
		w.t.Fatalf("the delivery is not parked at the gate: %s",
			w.str(`SELECT status || ' ' || step || ' ' || state::text FROM workflow_runs WHERE task_id = $1`, task))
	}
	return task, ana
}

// nothingPublished pumps the syncer and workflow, then asks the gate's
// Open: no pull request is opened for the ended task.
func (w *world) nothingPublished(task string) {
	w.t.Helper()
	for range 4 {
		w.pump()
	}
	status, out := w.handBack(task, map[string]any{"decider": "policy", "openPullRequest": true})
	for range 4 {
		w.pump()
	}
	if n := len(w.gh.Pulls()); n != 0 {
		w.t.Fatalf("%d pull requests opened after the task ended (Open answered %d %v)", n, status, out)
	}
	if status == 200 {
		w.t.Errorf("Open or Draft at the gate of an ended task was taken: %v", out)
	}
}

// Stop at the gate: the task is aborted, its delivery aborted, every way
// back offered, and the Open never opens the pull request.
func TestStoppingATaskParkedAtThePullRequestGate(t *testing.T) {
	w := newWorld(t)
	task, ana := w.parkedAtGate()
	out := w.stop(ana, task, map[string]any{"note": "not this branch"})
	if out["status"] != "aborted" {
		t.Fatalf("stop answered %v", out)
	}
	if s := w.taskStatus(task); s != "aborted" {
		t.Fatalf("a stopped task is %s", s)
	}
	if n := w.count(`SELECT count(*) FROM workflow_runs WHERE task_id = $1 AND status = 'aborted'`, task); n != 1 {
		t.Errorf("the delivery was not aborted: %s", w.str(`SELECT status FROM workflow_runs WHERE task_id = $1`, task))
	}
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'task.stopped'
		AND payload->>'note' = 'not this branch' AND actor_id = $2`, task, ana); n != 1 {
		t.Errorf("task.stopped was not recorded")
	}
	// No Run was live to keep: nothing to resume.
	if got, _ := w.options(task); strings.Join(got, " ") != "retry restart" {
		t.Fatalf("a task stopped at the gate offers %v", got)
	}
	w.nothingPublished(task)
	if s := w.taskStatus(task); s != "aborted" {
		t.Fatalf("after the Open the stopped task is %s", s)
	}
	// Try again takes it back to the gate, which still waits for the person.
	w.pickUp(ana, task, map[string]any{"action": "retry"})
	w.until("the gate again", func() bool {
		return w.taskStatus(task) == "awaiting_input" && w.count(`SELECT count(*) FROM workflow_runs WHERE task_id = $1
			AND status = 'waiting' AND state->'decision'->>'point' = $2`, task, delivery.PointBeforePR) == 1
	})
	for range 3 {
		w.pump()
	}
	if n := len(w.gh.Pulls()); n != 0 {
		t.Fatalf("%d pull requests opened by Try again without an Open or Draft", n)
	}
}

// Close (Won't do) at the gate: the task is closed, its delivery ended,
// and the Open never opens the pull request.
func TestClosingATaskParkedAtThePullRequestGate(t *testing.T) {
	w := newWorld(t)
	task, ana := w.parkedAtGate()
	if status, body := w.callAs(ana, "/internal/tasks/"+task+"/close", map[string]any{"resolution": "wont_do"}); status != 200 {
		t.Fatalf("close: %d %v", status, body)
	}
	if s, r := w.taskStatus(task), w.resolutionOf(task); s != "aborted" || r != "wont_do" {
		t.Fatalf("closed: %s %s", s, r)
	}
	if n := w.count(`SELECT count(*) FROM workflow_runs WHERE task_id = $1 AND status = 'completed'`, task); n != 1 {
		t.Errorf("the delivery did not end: %s", w.str(`SELECT status FROM workflow_runs WHERE task_id = $1`, task))
	}
	w.nothingPublished(task)
	if s, r := w.taskStatus(task), w.resolutionOf(task); s != "aborted" || r != "wont_do" {
		t.Errorf("after the Open the closed task is %s %s", s, r)
	}
}
