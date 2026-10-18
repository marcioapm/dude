package orchestrator_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func (w *world) checksPendingSince(wi string) (since *time.Time) {
	if err := w.owner.QueryRow(context.Background(), `SELECT checks_pending_since FROM pull_requests WHERE task_id = $1`, wi).Scan(&since); err != nil && err != pgx.ErrNoRows {
		w.t.Fatal(err)
	}
	return since
}

// A head seen long ago with its checks green: a check that starts now is
// pending for as long as it has run, not since the head was first seen.
func TestCIStuckCountsFromWhenChecksWentPending(t *testing.T) {
	w := newWorld(t)
	w.gh.SetChecks("success")
	wi := w.reviewing()
	w.sync()
	mustExec(t, w.owner, `UPDATE pull_requests SET head_seen_at = now() - interval '2 hours' WHERE task_id = $1`, wi)
	if w.checksPendingSince(wi) != nil {
		t.Fatal("green checks recorded as pending")
	}
	w.gh.SetChecks("pending")
	for range 3 {
		w.sync()
		w.pump()
	}
	if s, r := w.taskStatus(wi), w.escalationReason(wi); s != "review" || r != "" {
		t.Fatalf("a check pending for a moment escalated: status %s, %q", s, r)
	}
	if w.checksPendingSince(wi) == nil {
		t.Fatal("when the checks went pending was not recorded")
	}
	// Pending past the organization's patience since it went pending.
	mustExec(t, w.owner, `UPDATE pull_requests SET checks_pending_since = now() - interval '61 minutes' WHERE task_id = $1`, wi)
	w.until("escalated", func() bool { w.sync(); return w.taskStatus(wi) == "awaiting_input" })
	if r := w.escalationReason(wi); r != "ci_stuck" {
		t.Fatalf("escalated %q, want ci_stuck", r)
	}
	// Green again: nothing is pending.
	w.gh.SetChecks("success")
	w.sync()
	if w.checksPendingSince(wi) != nil {
		t.Error("checks no longer pending are still recorded as pending")
	}
}

// Checks that finish while a ci_stuck escalation is open take the task
// back to waiting on its pull request, as a person's wait would, with no
// person's answer: decided by dude, saying the checks finished.
func TestCIStuckClearsItselfWhenTheChecksFinish(t *testing.T) {
	w := newWorld(t)
	w.gh.SetChecks("pending")
	wi := w.reviewing()
	w.sync()
	mustExec(t, w.owner, `UPDATE pull_requests SET checks_pending_since = now() - interval '61 minutes' WHERE task_id = $1`, wi)
	w.until("escalated", func() bool { w.sync(); return w.taskStatus(wi) == "awaiting_input" })
	if r := w.escalationReason(wi); r != "ci_stuck" {
		t.Fatalf("escalated %q, want ci_stuck", r)
	}
	w.gh.SetChecks("success")
	w.until("waiting on the pull request again", func() bool {
		w.sync()
		return w.taskStatus(wi) == "review" &&
			w.count(`SELECT count(*) FROM workflow_runs WHERE task_id = $1 AND step = 'awaitPullRequest' AND status = 'waiting'`, wi) == 1
	})
	var actorType, action, note string
	if err := w.owner.QueryRow(context.Background(), `SELECT actor_type, payload->>'action', payload->>'note' FROM events
		WHERE task_id = $1 AND event_type = 'task.decided'`, wi).Scan(&actorType, &action, &note); err != nil {
		t.Fatal(err)
	}
	if actorType != "system" || action != "wait" || note == "" {
		t.Errorf("decided by %s: %s %q, want the system's wait with a note", actorType, action, note)
	}
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'task.decided'`, wi); n != 1 {
		t.Errorf("%d decisions, want one", n)
	}
	// Another sync decides nothing more.
	for range 3 {
		w.sync()
		w.pump()
	}
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'task.decided'`, wi); n != 1 {
		t.Errorf("%d decisions after more syncs, want one", n)
	}
}

func TestCIStuckHeadChangeStartsANewClock(t *testing.T) {
	for _, dude := range []bool{false, true} {
		name := "github"
		if dude {
			name = "dude"
		}
		t.Run(name, func(t *testing.T) {
			w := newPRFixWorld(t)
			w.gh.SetChecks("pending")
			w.sync()
			mustExec(t, w.owner, `UPDATE pull_requests SET checks_pending_since = now() - interval '61 minutes' WHERE task_id = $1`, w.wi)
			if dude {
				w.comment("Please rename the greeting.")
				w.sync()
				w.until("the candidate promoted", func() bool {
					return w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'pull_request.fix_moved'`, w.wi) == 1
				})
				if since := w.checksPendingSince(w.wi); since != nil {
					t.Fatalf("dude's new head kept the pending clock: %v", since)
				}
			} else {
				w.gh.CommitOnTop(w.branch, "A person's new head")
			}
			w.sync()
			if since := w.checksPendingSince(w.wi); since == nil || time.Since(*since) > time.Minute {
				t.Fatalf("new head's pending clock = %v, want fresh", since)
			}
			w.pump()
			if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'question.asked' AND payload->>'reason' = 'ci_stuck'`, w.wi); n != 0 {
				t.Fatalf("new head immediately escalated %d times", n)
			}
		})
	}
}

func TestCIStuckRerunOnTheSameHeadEscalatesAfterOnePatience(t *testing.T) {
	w := newWorld(t)
	w.gh.SetChecks("pending")
	wi := w.reviewing()
	w.sync()
	first := time.Now().Add(-62 * time.Minute).Truncate(time.Second)
	mustExec(t, w.owner, `UPDATE pull_requests SET checks_pending_since = $2 WHERE task_id = $1`, wi, first)
	w.until("first stuck episode", func() bool { w.sync(); return w.taskStatus(wi) == "awaiting_input" })
	w.gh.SetChecks("success")
	w.until("auto-cleared", func() bool { w.sync(); return w.taskStatus(wi) == "review" })
	w.gh.SetChecks("pending")
	w.sync()
	mustExec(t, w.owner, `UPDATE pull_requests SET checks_pending_since = $2 WHERE task_id = $1`, wi, first.Add(time.Minute))
	for range 5 {
		w.sync()
		w.pump()
	}
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'question.asked' AND payload->>'reason' = 'ci_stuck'`, wi); n != 2 {
		t.Fatalf("%d stuck episodes escalated, want 2 after one patience each", n)
	}
	if s := w.taskStatus(wi); s != "awaiting_input" {
		t.Fatalf("second episode status %s, want awaiting_input", s)
	}
}
