package orchestrator_test

// dude's own closes are not a pull request ending on GitHub: Start over
// closes the last attempt's open pull requests after the next attempt has
// started on a branch of its own, and Close with closePullRequest closes
// them after the task has ended. The pull request syncer finds no live
// delivery for either (prs.go: the delivery on the pull request's branch),
// so neither attempt hears of it.

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/fakelux"
)

// attemptDelivery is the status of the task's delivery on attempt n's
// branch, and whether it holds an escalation.
func (w *world) attemptDelivery(task string, n int) (status string, escalated bool) {
	_ = w.owner.QueryRow(context.Background(), `SELECT status::text, state ? 'escalation' FROM workflow_runs
		WHERE task_id = $1 AND state->>'branch' = $2`, task, delivery.BranchFor(task, n)).Scan(&status, &escalated)
	return status, escalated
}

// assertStartedOverCleanly: attempt 1's pull request closed on GitHub and its
// delivery completed; attempt 2's delivery live, not escalated, never told
// of attempt 1's pull request, and with a pull request of its own; and the
// task never ended or waited on a person since the start over (since, the
// cursor before it).
func (w *world) assertStartedOverCleanly(task string, since int64) {
	w.t.Helper()
	for range 6 {
		w.sync()
		w.pump()
	}
	if p := w.gh.Pull(1); p.State != "closed" || p.MergedAt != nil {
		w.t.Errorf("attempt 1's pull request is %s", p.State)
	}
	w.until("attempt 2's pull request", func() bool { w.sync(); return len(w.gh.Pulls()) == 2 })
	for range 3 {
		w.sync()
		w.pump()
	}
	if p := w.gh.Pull(2); p.Head != delivery.BranchFor(task, 2) || p.State != "open" {
		w.t.Errorf("attempt 2's pull request: %s from %s", p.State, p.Head)
	}
	if s, _ := w.attemptDelivery(task, 1); s != "completed" {
		w.t.Errorf("attempt 1's delivery is %s, want completed", s)
	}
	if s, escalated := w.attemptDelivery(task, 2); (s != "waiting" && s != "running") || escalated {
		w.t.Errorf("attempt 2's delivery is %s (escalated %v), want live", s, escalated)
	}
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND cursor > $2 AND event_type = 'task.status_changed'
		AND (payload->>'status' IN ('aborted', 'done', 'awaiting_input')
		  OR payload->>'reason' IN ('pull requests merged', 'pull request closed without merging'))`, task, since); n != 0 {
		w.t.Errorf("%d status changes ending the task or waiting on a person since the start over: %s", n,
			w.str(`SELECT string_agg(payload::text, ' ') FROM events WHERE task_id = $1 AND cursor > $2
				AND event_type = 'task.status_changed'`, task, since))
	}
	// Attempt 1's close reached no delivery of the task.
	if n := w.count(`SELECT count(*) FROM workflow_signals s JOIN workflow_runs r ON r.id = s.workflow_run_id
		WHERE r.task_id = $1 AND s.name IN ('pr.ended', 'pr.feedback') AND (s.payload->>'number')::int = 1
		AND s.received_at > (SELECT occurred_at FROM events WHERE cursor = $2)`, task, since); n != 0 {
		w.t.Errorf("attempt 1's pull request signalled a delivery %d times after the start over", n)
	}
	if s := w.taskStatus(task); s != "review" && s != "ready_to_merge" {
		w.t.Errorf("task is %s, want attempt 2 in review", s)
	}
}

// lastCursor is the task's newest event's cursor.
func (w *world) lastCursor(task string) int64 {
	var c int64
	_ = w.owner.QueryRow(context.Background(), `SELECT COALESCE(max(cursor), 0) FROM events WHERE task_id = $1`, task).Scan(&c)
	return c
}

// Start over (the pick-up's restart, and Stop with andRestart) closes
// attempt 1's open pull request wherever attempt 1 was parked: on the pull
// request, on the conductor's decision about its feedback, or on a pull
// request fixer at work. The close wakes neither attempt: attempt 1 stays
// completed, attempt 2 goes on to a pull request of its own.
func TestStartingOverClosesAttemptOnesPullRequestWithoutEndingTheTask(t *testing.T) {
	type parked struct {
		name string
		// at parks attempt 1 and returns its task; hang is set while
		// attempt 1's pull request fixer should hang.
		at func(w *world, hang *atomic.Bool) string
	}
	for _, p := range []parked{
		{"awaitPullRequest", func(w *world, _ *atomic.Bool) string {
			task := w.task()
			w.deliver(task)
			w.until("the pull request", func() bool { return len(w.gh.Pulls()) == 1 && w.waitingOnPR(task) })
			return task
		}},
		{"conductorDecision", func(w *world, _ *atomic.Bool) string { return w.conductedFeedback() }},
		{"pull request fixer", func(w *world, hang *atomic.Bool) string {
			task := w.task()
			w.deliver(task)
			w.until("the pull request", func() bool { return len(w.gh.Pulls()) == 1 && w.waitingOnPR(task) })
			hang.Store(true)
			w.gh.Comment(1, "alice", "Please rename the greeting.")
			w.until("the fixer at work", func() bool {
				w.sync()
				return w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'fix' AND status = 'running'
					AND pr_feedback <> '[]'::jsonb`, task) == 1
			})
			hang.Store(false)
			return task
		}},
	} {
		for _, via := range []string{"recover", "stop"} {
			t.Run(p.name+"/"+via, func(t *testing.T) {
				w := conducting(t)
				var hang atomic.Bool
				scripted := w.lux.Decide
				w.lux.Decide = func(spec map[string]any) fakelux.Behaviour {
					b := scripted(spec)
					if labels, _ := spec["labels"].(map[string]any); labels["dude.phase"] == "fix" && hang.Load() {
						b.Hang = true
					}
					return b
				}
				task := p.at(w, &hang)
				ana := w.ownedBy(task, "Ana")
				var since int64
				// Attempt 2 under Deliver, so it opens its pull request unasked.
				if via == "stop" {
					w.stop(ana, task, map[string]any{"andRestart": true, "decider": "policy"})
					// Stop's own aborted move is the person's, before the start over.
					since = int64(w.count(`SELECT max(cursor) FROM events WHERE task_id = $1 AND event_type = 'task.stopped'`, task))
				} else {
					w.stop(ana, task, map[string]any{})
					since = w.lastCursor(task)
					if status, body := w.callAs(ana, "/internal/tasks/"+task+"/recover",
						map[string]any{"action": "restart", "decider": "policy"}); status != 200 {
						t.Fatalf("restart: %d %v", status, body)
					}
				}
				w.assertStartedOverCleanly(task, since)
			})
		}
	}
}

// Closing a task and its pull request (Close with closePullRequest) ends it
// once: the pull request's close, read back by the syncer, adds no status
// change, no escalation, and no "merged" or "closed without merging".
func TestClosingATaskWithItsPullRequestEndsItOnce(t *testing.T) {
	w := newWorld(t)
	task := w.task()
	w.deliver(task)
	w.until("the pull request", func() bool { return len(w.gh.Pulls()) == 1 && w.waitingOnPR(task) })
	ana := w.ownedBy(task, "Ana")
	since := w.lastCursor(task)
	if status, body := w.callAs(ana, "/internal/tasks/"+task+"/close", map[string]any{"resolution": "wont_do",
		"closePullRequest": true}); status != 200 {
		t.Fatalf("close: %d %v", status, body)
	}
	if p := w.gh.Pull(1); p.State != "closed" {
		t.Fatalf("the pull request is %s", p.State)
	}
	for range 6 {
		w.sync()
		w.pump()
	}
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'pull_request.closed' AND cursor > $2`, task, since); n != 1 {
		t.Errorf("%d pull_request.closed read back, want the close's one", n)
	}
	if got := w.str(`SELECT COALESCE(string_agg(payload->>'status' || ': ' || (payload->>'reason'), '; ' ORDER BY cursor), '')
		FROM events WHERE task_id = $1 AND event_type = 'task.status_changed' AND cursor > $2`, task, since); got != "aborted: closed as won't do by Ana" {
		t.Errorf("status changes since the close: %q, want the close's one", got)
	}
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND cursor > $2 AND event_type = 'question.asked'`, task, since); n != 0 {
		t.Errorf("%d escalations after the close", n)
	}
	if s := w.taskStatus(task); s != "aborted" {
		t.Errorf("task is %s", s)
	}
}
