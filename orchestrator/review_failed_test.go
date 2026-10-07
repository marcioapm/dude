package orchestrator_test

// A review round is complete only when every reviewer finished: one that
// failed found nothing because it read nothing, and must not count as a
// reviewer that found nothing.

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/fakelux"
)

// reviewerFails delivers a change two reviewers read (correctness and
// security); the first reviewer lux starts dies, every other one finds
// nothing. Returns the task and the failed reviewer's category.
func (w *world) reviewerFails() (string, string) {
	w.t.Helper()
	var reviews atomic.Int32
	w.lux.Decide = func(spec map[string]any) fakelux.Behaviour {
		labels, _ := spec["labels"].(map[string]any)
		switch labels["dude.phase"] {
		case "implement":
			return fakelux.Behaviour{Commit: map[string]string{"auth/session.go": "package auth\n"}, Message: "work"}
		case "review":
			if reviews.Add(1) == 1 {
				return fakelux.Behaviour{Crash: true}
			}
		}
		return fakelux.Behaviour{Reply: "Looks good."}
	}
	wi := w.task()
	w.deliver(wi)
	w.until("the round to end", func() bool {
		return w.taskStatus(wi) == "awaiting_input" || len(w.gh.Pulls()) > 0
	})
	var category string
	_ = w.owner.QueryRow(context.Background(), `SELECT category FROM runs WHERE task_id = $1 AND phase = 'review'
		AND status = 'failed'`, wi).Scan(&category)
	return wi, category
}

func TestAFailedReviewerIsNotAClearReview(t *testing.T) {
	w := newWorld(t)
	wi, category := w.reviewerFails()
	if n := len(w.gh.Pulls()); n != 0 {
		t.Fatalf("a round with a failed reviewer opened %d pull requests", n)
	}
	if got := w.escalationReason(wi); got != "review_failed" {
		t.Fatalf("escalated for %q, want review_failed", got)
	}
	var detailCategory, actions string
	_ = w.owner.QueryRow(context.Background(), `SELECT payload->'detail'->>'category', payload->>'actions' FROM events
		WHERE task_id = $1 AND event_type = 'question.asked' AND payload->>'kind' = 'escalation'`, wi).Scan(&detailCategory, &actions)
	if category == "" || detailCategory != category {
		t.Errorf("the escalation names category %q, the failed reviewer was %q", detailCategory, category)
	}
	if actions != `["resume", "retry", "accept", "stop"]` {
		t.Errorf("a failed reviewer offers %s", actions)
	}
}

// Retry runs the failed reviewer's category again, and only it.
func TestRetryingAFailedReviewRunsOnlyThatReviewer(t *testing.T) {
	w := newWorld(t)
	wi, category := w.reviewerFails()
	if status, body := w.call("/internal/tasks/"+wi+"/decide", map[string]any{"action": "retry"}); status != 200 {
		t.Fatalf("retry: %d %v", status, body)
	}
	w.until("a pull request", func() bool { return len(w.gh.Pulls()) == 1 })
	if n := w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'review'`, wi); n != 3 {
		t.Errorf("%d reviewers, want the two of the round and the one retried", n)
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'review' AND category = $2 AND status = 'completed'`,
		wi, category); n != 1 {
		t.Errorf("the %s reviewer did not run again", category)
	}
}

// Accept goes on without that reviewer: a person's choice, recorded as theirs.
func TestAcceptingAFailedReviewGoesOnWithoutIt(t *testing.T) {
	w := newWorld(t)
	ana := w.person("Ana")
	wi, _ := w.reviewerFails()
	w.assignOwner(wi, ana)
	if status, body := w.callAs(ana, "/internal/tasks/"+wi+"/decide", map[string]any{"action": "accept"}); status != 200 {
		t.Fatalf("accept: %d %v", status, body)
	}
	w.until("a pull request", func() bool { return len(w.gh.Pulls()) == 1 })
	if n := w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'review'`, wi); n != 2 {
		t.Errorf("%d reviewers: accepting ran another", n)
	}
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'task.decided'
		AND payload->>'reason' = 'review_failed' AND payload->>'action' = 'accept' AND actor_id = $2`, wi, ana); n != 1 {
		t.Errorf("the acceptance is not recorded as Ana's")
	}
}
