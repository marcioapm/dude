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
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'task.decided'`, wi); n != 1 {
		t.Fatalf("%d decisions, want one", n)
	}
	var actorType, actorID, action, note string
	if err := w.owner.QueryRow(context.Background(), `SELECT actor_type, actor_id, payload->>'action', payload->>'note' FROM events
		WHERE task_id = $1 AND event_type = 'task.decided'`, wi).Scan(&actorType, &actorID, &action, &note); err != nil {
		t.Fatal(err)
	}
	if actorType != "system" || actorID != "forge" || action != "wait" || note != "The checks on target#1 finished (passing): waiting on the pull request again." {
		t.Errorf("decided by %s/%s: %s %q, want forge's wait with the checks-finished note", actorType, actorID, action, note)
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

func (w *world) syncPR(wi, repo string) {
	w.t.Helper()
	var id string
	if err := w.owner.QueryRow(context.Background(), `SELECT pr.id FROM pull_requests pr JOIN repositories r ON r.id = pr.repository_id WHERE pr.task_id = $1 AND r.name = $2`, wi, repo).Scan(&id); err != nil {
		w.t.Fatal(err)
	}
	if err := w.prs.Sync(context.Background(), w.org, id); err != nil {
		w.t.Fatalf("sync %s: %v", repo, err)
	}
}

func TestCIStuckPersonWaitsBeforeChecksFinish(t *testing.T) {
	w := newWorld(t)
	w.gh.SetChecks("pending")
	wi := w.reviewing()
	w.syncPR(wi, "target")
	mustExec(t, w.owner, `UPDATE pull_requests SET checks_pending_since = now() - interval '61 minutes' WHERE task_id = $1`, wi)
	w.until("stuck", func() bool { w.syncPR(wi, "target"); return w.taskStatus(wi) == "awaiting_input" })
	mustExec(t, w.owner, `UPDATE pull_requests SET checks_pending_since = now() - interval '121 minutes' WHERE task_id = $1`, wi)
	w.syncPR(wi, "target")
	owner := w.person("owner")
	if code, body := w.decide(owner, wi, "wait", ""); code != 200 {
		t.Fatalf("wait: %d %v", code, body)
	}
	w.gh.SetChecks("success")
	w.syncPR(wi, "target")
	w.until("waiting again", func() bool {
		w.syncPR(wi, "target")
		return w.taskStatus(wi) == "review" || w.taskStatus(wi) == "ready_to_merge"
	})
	for range 3 {
		w.pump()
	}
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'task.decided'`, wi); n != 1 {
		t.Fatalf("%d decisions, want person's one", n)
	}
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'task.decided' AND actor_id = $2 AND actor_type = 'human'`, wi, owner); n != 1 {
		t.Fatalf("%d decisions by the person, want 1", n)
	}
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'question.asked' AND payload->>'reason' = 'ci_stuck'`, wi); n != 1 {
		t.Fatalf("stale signal asked again: %d questions", n)
	}
}

func TestCIStuckUnknownChecksLeaveTheEscalationOpen(t *testing.T) {
	w := newWorld(t)
	w.gh.SetChecks("pending")
	wi := w.reviewing()
	w.syncPR(wi, "target")
	mustExec(t, w.owner, `UPDATE pull_requests SET checks_pending_since = now() - interval '61 minutes', head_seen_at = now() - interval '2 hours' WHERE task_id = $1`, wi)
	w.until("stuck", func() bool { w.syncPR(wi, "target"); return w.taskStatus(wi) == "awaiting_input" })
	w.gh.SetChecks("none")
	for range 3 {
		w.syncPR(wi, "target")
		w.pump()
	}
	if s := w.taskStatus(wi); s != "awaiting_input" {
		t.Fatalf("unknown checks closed escalation: %s", s)
	}
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'task.decided'`, wi); n != 0 {
		t.Fatalf("unknown checks caused %d decisions", n)
	}
}

func TestCIStuckFailingChecksClearAndGetAFix(t *testing.T) {
	w := newWorld(t)
	w.gh.SetChecks("pending")
	wi := w.reviewing()
	w.syncPR(wi, "target")
	mustExec(t, w.owner, `UPDATE pull_requests SET checks_pending_since = now() - interval '61 minutes' WHERE task_id = $1`, wi)
	w.until("stuck", func() bool { w.syncPR(wi, "target"); return w.taskStatus(wi) == "awaiting_input" })
	w.gh.SetChecks("run:failure")
	w.syncPR(wi, "target")
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'task.decided' AND actor_type = 'system' AND actor_id = 'forge' AND payload->>'action' = 'wait'`, wi); n != 1 {
		t.Fatalf("failing checks caused %d system waits, want 1", n)
	}
	w.until("failing check fixer", func() bool { return w.fixes(wi) > 0 })
	if n := w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'fix' AND pr_feedback @> '[{"source":"checks"}]'`, wi); n != 1 {
		t.Fatalf("%d CI fixers, want 1", n)
	}
}

func TestCIStuckSiblingGreenDoesNotClearWeb(t *testing.T) {
	w := newPRFixWorldIn(t, true)
	w.web.SetChecks("pending")
	w.syncPR(w.wi, "web")
	mustExec(t, w.owner, `UPDATE pull_requests SET checks_pending_since = now() - interval '61 minutes' WHERE task_id = $1 AND repository_id = (SELECT id FROM repositories WHERE project_id = $2 AND name = 'web')`, w.wi, w.project)
	w.until("web stuck", func() bool { w.syncPR(w.wi, "web"); return w.taskStatus(w.wi) == "awaiting_input" })
	w.gh.SetChecks("success")
	for range 3 {
		w.syncPR(w.wi, "target")
		w.pump()
	}
	if s := w.taskStatus(w.wi); s != "awaiting_input" {
		t.Fatalf("target's green closed web's escalation: %s", s)
	}
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'task.decided'`, w.wi); n != 0 {
		t.Fatalf("sibling checks caused %d decisions", n)
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
