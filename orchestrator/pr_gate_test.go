package orchestrator_test

// Before the pull request: a project's or task's Ask me first holds the
// delivery for its owner before the first pull request opens; Open as draft
// opens a draft; Open it is the delivery as it always was.

import (
	"bytes"
	"encoding/json"
	"net/http"
	"sync"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
)

// putAs puts to the orchestrator's internal API as one person.
func (w *world) putAs(actor, path string, body any) (int, map[string]any) {
	w.t.Helper()
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest("PUT", w.api+path, bytes.NewReader(b))
	req.Header.Set("Authorization", "Bearer svc")
	req.Header.Set("X-Dude-Organization", w.org)
	req.Header.Set("X-Dude-Actor", actor)
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		w.t.Fatal(err)
	}
	defer res.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(res.Body).Decode(&out)
	return res.StatusCode, out
}

// projectChoice sets the project's choice before the pull request.
func (w *world) projectChoice(choice string) {
	w.t.Helper()
	mustExec(w.t, w.owner, `UPDATE projects SET delivery_policy = delivery_policy || jsonb_build_object('pullRequest', $2::text) WHERE id = $1`,
		w.project, choice)
}

// heldBeforePR says the task's delivery is held at the gate, untaken.
func (w *world) heldBeforePR(task string) bool {
	return w.count(`SELECT count(*) FROM workflow_runs WHERE task_id = $1 AND status = 'waiting' AND step = 'conductorDecision'
		AND state->'decision'->>'point' = 'before_pull_request' AND NOT state->'decision' ? 'taken'`, task) == 1
}

// readyForReview counts the task's moves to awaiting_input for its owner's review.
func (w *world) readyForReview(task string) int {
	return w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'task.status_changed'
		AND payload->>'status' = 'awaiting_input' AND payload->>'reason' = $2`, task, delivery.ReasonReadyForReview)
}

// atTheHold delivers a task its owner (ana) asked to see first, and waits
// for the hold: no pull request, the task ready for her review.
func (w *world) atTheHold(ana string) string {
	w.t.Helper()
	task := w.task()
	w.assignOwner(task, ana)
	w.deliver(task)
	// Set once the delivery has started: the choice is read at the gate.
	w.projectChoice(delivery.PullRequestAsk)
	w.until("the hold before the pull request", func() bool { return w.heldBeforePR(task) || len(w.gh.Pulls()) > 0 })
	for range 3 {
		w.pump()
	}
	if n := len(w.gh.Pulls()); n != 0 {
		w.t.Fatalf("%d pull requests opened under Ask me first", n)
	}
	if s := w.taskStatus(task); s != "awaiting_input" || w.readyForReview(task) != 1 {
		w.t.Fatalf("task %s, %d moves to ready for review", s, w.readyForReview(task))
	}
	return task
}

// Ask me first, set on the project after the delivery started, holds the
// first pull request until its owner opens it; nobody else can. Once open,
// a person's feedback on it is fixed with no hold.
func TestAskMeFirstHoldsTheFirstPullRequestForItsOwner(t *testing.T) {
	w := newWorld(t)
	ana, bo := w.person("Ana"), w.person("Bo")
	task := w.atTheHold(ana)
	// The hold carries when it parked, the time its event has.
	if n := w.count(`SELECT count(*) FROM workflow_runs w JOIN events e ON e.task_id = w.task_id
		WHERE w.task_id = $1 AND e.event_type = $2 AND e.payload->>'point' = 'before_pull_request'
		  AND (w.state->'hold'->>'since')::timestamptz = e.occurred_at`, task, delivery.EvDecisionAwaited); n != 1 {
		t.Fatalf("the hold's since matches %d of its events", n)
	}
	if status, out := w.callAs(bo, "/internal/tasks/"+task+"/decider", map[string]any{"decider": "policy", "openPullRequest": true}); status != 403 {
		t.Fatalf("someone else's Open: %d %v", status, out)
	}
	if status, out := w.callAs(ana, "/internal/tasks/"+task+"/decider", map[string]any{"decider": "policy"}); status != 409 || !gateRefused(out) {
		t.Fatalf("an Open with no answer: %d %v", status, out)
	}
	if status, out := w.callAs(ana, "/internal/tasks/"+task+"/decider", map[string]any{"decider": "policy", "openPullRequest": true}); status != 200 {
		t.Fatalf("the owner's Open: %d %v", status, out)
	}
	w.until("the pull request", func() bool { return len(w.gh.Pulls()) == 1 })
	if w.gh.Pulls()[0].Draft {
		t.Error("Open opened a draft")
	}
	w.until("the pull request on record", func() bool {
		return w.count(`SELECT count(*) FROM workflow_runs WHERE task_id = $1 AND state ? 'pullRequestIds'`, task) == 1
	})
	if n := w.count(`SELECT count(*) FROM workflow_runs WHERE task_id = $1 AND state ? 'hold'`, task); n != 0 {
		t.Error("the hold outlived the pull request")
	}
	if code, out := w.putAs(ana, "/internal/tasks/"+task+"/pull-request-choice", map[string]any{"choice": "draft"}); code != 409 {
		t.Fatalf("a change once the pull request is open: %d %v", code, out)
	}

	// A later fix of the open pull request is never held.
	if code, body := w.feedback(ana, task, "fbi_after", "Log the retry count at info, not debug."); code != 201 {
		t.Fatalf("submit: %d %v", code, body)
	}
	w.until("the pull request's fix rounds spent", func() bool { w.sync(); return w.escalationReason(task) == "pr_loop_exhausted" })
	if n := w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'fix' AND 'fbi_after' = ANY(feedback_item_ids)`, task); n == 0 {
		t.Fatal("no fixer was given the item")
	}
	if n := w.readyForReview(task); n != 1 {
		t.Fatalf("held %d times: a fix of the open pull request was held", n)
	}
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = $2 AND payload->>'point' = 'before_pull_request'`,
		task, delivery.EvDecisionAwaited); n != 1 {
		t.Fatalf("%d holds before the pull request", n)
	}
}

// Open as draft, the task's own over the project's Open it, opens a draft
// with nobody asked.
func TestOpenAsDraftOpensADraftWithNobodyAsked(t *testing.T) {
	w := newWorld(t)
	ana := w.person("Ana")
	task := w.task()
	w.assignOwner(task, ana)
	w.deliver(task)
	if code, out := w.putAs(ana, "/internal/tasks/"+task+"/pull-request-choice", map[string]any{"choice": "draft"}); code != 200 {
		t.Fatalf("the owner's draft: %d %v", code, out)
	}
	w.until("the pull request", func() bool { return len(w.gh.Pulls()) == 1 })
	if !w.gh.Pulls()[0].Draft {
		t.Error("Open as draft opened a pull request ready for review")
	}
	if n := w.count(`SELECT count(*) FROM questions WHERE task_id = $1`, task) + w.readyForReview(task); n != 0 {
		t.Errorf("Open as draft asked: %d", n)
	}
}

// Open it, the default, opens as before: no hold, no question, not a draft.
func TestOpenItOpensThePullRequestAsBefore(t *testing.T) {
	w := newWorld(t)
	task := w.task()
	w.deliver(task)
	w.until("the pull request", func() bool { return len(w.gh.Pulls()) == 1 })
	if w.gh.Pulls()[0].Draft || w.readyForReview(task) != 0 || w.count(`SELECT count(*) FROM questions WHERE task_id = $1`, task) != 0 {
		t.Error("Open it held, asked or opened a draft")
	}
	if w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = $2`, task, delivery.EvDecisionAwaited) != 0 {
		t.Error("Open it parked on a decision")
	}
}

// The owner changes Ask me first to Open it while held: the hold reads the
// choice again and opens.
func TestChangingAskMeFirstWhileHeldOpensThePullRequest(t *testing.T) {
	w := newWorld(t)
	ana := w.person("Ana")
	task := w.atTheHold(ana)
	if code, out := w.putAs(ana, "/internal/tasks/"+task+"/pull-request-choice", map[string]any{"choice": "open"}); code != 200 {
		t.Fatalf("the owner's change: %d %v", code, out)
	}
	w.until("the pull request", func() bool { return len(w.gh.Pulls()) == 1 })
}

// The owner changes Ask me first to Open it while Deliver is entering the
// hold: no hold is on record yet to wake, so the step's commit reads the
// choice again and opens instead of holding.
func TestAChoiceChangedWhileEnteringTheHoldOpensThePullRequest(t *testing.T) {
	w := newWorld(t)
	ana := w.person("Ana")
	task := w.task()
	w.assignOwner(task, ana)
	var once sync.Once
	armed := w.gated(func(step, next string) {
		if step != "openPullRequest" || next != "conductorDecision" {
			return
		}
		once.Do(func() {
			if code, out := w.putAs(ana, "/internal/tasks/"+task+"/pull-request-choice", map[string]any{"choice": "open"}); code != 200 {
				t.Errorf("the owner's change while entering the hold: %d %v", code, out)
			}
		})
	})
	armed.Store(true)
	w.deliver(task)
	w.projectChoice(delivery.PullRequestAsk)
	w.until("the pull request", func() bool { return len(w.gh.Pulls()) == 1 })
	if w.gh.Pulls()[0].Draft {
		t.Error("Open it opened a draft")
	}
	if w.heldBeforePR(task) {
		t.Error("the delivery is still held")
	}
	if s := w.taskStatus(task); s == "awaiting_input" {
		t.Errorf("task %s once its pull request opened", s)
	}
}

// The owner's Open authorizes the heads they saw. Heads that moved before
// the opening step ran are not opened: the hold asks again.
func TestAHoldWhoseHeadsMovedAsksAgain(t *testing.T) {
	w := newWorld(t)
	ana := w.person("Ana")
	task := w.atTheHold(ana)
	if status, out := w.callAs(ana, "/internal/tasks/"+task+"/decider", map[string]any{"decider": "policy", "openPullRequest": true}); status != 200 {
		t.Fatalf("the owner's Open: %d %v", status, out)
	}
	// The heads move before the step that opens runs.
	mustExec(t, w.owner, `UPDATE workflow_runs SET state = jsonb_set(state, '{heads,target}', '"0123456789abcdef0123456789abcdef01234567"')
		WHERE task_id = $1`, task)
	for range 4 {
		w.pump()
	}
	if n := len(w.gh.Pulls()); n != 0 {
		t.Fatalf("%d pull requests opened at heads the owner did not see", n)
	}
	if !w.heldBeforePR(task) {
		t.Fatal("the delivery is not held at the new heads")
	}
}
