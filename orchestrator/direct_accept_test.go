package orchestrator_test

import (
	"sync/atomic"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/fakelux"
)

// conductedDirectFix is a conducted delivery frozen to push its pull
// request fixes directly, waiting on its open pull request: a comment
// arrives and the conductor decides to fix it. The fix's narrow review
// crashes while crash holds. Returns the world as editing on that task.
func conductedDirectFix(t *testing.T, crash *atomic.Bool) *editing {
	t.Helper()
	e := conductedDirect(t)
	scripted := e.lux.Decide
	e.lux.Decide = func(spec map[string]any) fakelux.Behaviour {
		if labels, _ := spec["labels"].(map[string]any); labels["dude.phase"] == "review" && crash.Load() {
			return fakelux.Behaviour{Crash: true}
		}
		return scripted(spec)
	}
	e.feedbackToDecide()
	return e
}

// conductedDirect is a conducted delivery frozen to push its pull request
// fixes directly, waiting on its open pull request, as editing on it.
func conductedDirect(t *testing.T) *editing {
	t.Helper()
	w := conducting(t)
	w.toolsWithSync()
	task := w.prOpen(w.task(), prAt{direct: true})
	if status, out := w.chat(task, "Look after the pull request for me."); status >= 300 {
		t.Fatalf("chat: %d %v", status, out)
	}
	w.until("waiting on the pull request", func() bool { return w.waitingOnPR(task) })
	cond, _, _ := w.conductor(task)
	w.until("the conductor to answer", func() bool { return len(w.said(cond)) >= 1 })
	return &editing{world: w, task: task, cond: cond, branch: w.gh.Pull(1).Head}
}

// feedbackToDecide is a comment on the pull request, the conductor asked
// to decide on it.
func (e *editing) feedbackToDecide() {
	e.t.Helper()
	e.gh.Comment(1, "alice", "Please rename the greeting.")
	e.until("feedback to decide on", func() bool { e.sync(); return e.decisionAt(e.task) == delivery.PointPRFeedback })
}

// The conductor publishes to the pull request (its last push was
// reviewed), then a comment arrives and the conductor has it fixed: the
// publish is reviewed before the fixer starts from it, so dude never
// pushes twice with no review between.
func TestDirectAPullRequestFixFirstReviewsAnUnreviewedPushOfDudes(t *testing.T) {
	e := conductedDirect(t)
	e.current()
	e.commit(map[string]string{"README.md": "# target\n\nthe conductor's fix\n"})
	id := e.publish()
	if status, why := e.published(id); status != delivery.PublishPublished {
		t.Fatalf("publish %s: %s", status, why)
	}
	published := e.gh.SHA(e.branch)
	e.until("the pull request read at the publish", func() bool {
		e.sync()
		return e.count(`SELECT count(*) FROM pull_requests WHERE task_id = $1 AND head_sha = $2`, e.task, published) == 1
	})
	e.feedbackToDecide()
	e.must(e.task, "decide", `{"action":"next","note":"NOTE-XYZZY"}`)
	e.until("the fix on the pull request, and waiting again", func() bool {
		e.sync()
		return e.fixes(e.task) == 1 && e.gh.SHA(e.branch) != published && e.waitingOnPR(e.task)
	})
	if n := e.count(`SELECT count(*) FROM runs k WHERE k.task_id = $1 AND k.phase = 'review' AND k.status = 'completed'
		AND k.base_refs->>'target' = $2 AND k.ended_at <= (SELECT min(f.created_at) FROM runs f WHERE f.task_id = $1
		AND f.phase = 'fix' AND f.pr_feedback <> '[]'::jsonb)`, e.task, published); n == 0 {
		t.Fatalf("no review of the conductor's push %s before the fixer started from it\n%s", published, e.describeRuns())
	}
	// The fix was told what the conductor said, kept across the review.
	if got := e.ids(`SELECT COALESCE(conductor_note, '') FROM runs WHERE task_id = $1 AND phase = 'fix'
		AND pr_feedback <> '[]'::jsonb ORDER BY created_at`, e.task); len(got) != 1 || got[0] != "NOTE-XYZZY" {
		t.Errorf("the pull request fix's note: %q", got)
	}
}

// The conductor publishes to the pull request, then the base moves into
// a conflict and the conductor decides next: the publish is reviewed
// first, then the integration it decided on runs, once.
func TestDirectAnIntegrationFirstReviewsAnUnreviewedPushOfDudes(t *testing.T) {
	e := conductedDirect(t)
	e.current()
	e.commit(map[string]string{"README.md": "# target\n\nthe conductor's fix\n"})
	id := e.publish()
	if status, why := e.published(id); status != delivery.PublishPublished {
		t.Fatalf("publish %s: %s", status, why)
	}
	published := e.gh.SHA(e.branch)
	e.until("the pull request read at the publish", func() bool {
		e.sync()
		return e.count(`SELECT count(*) FROM pull_requests WHERE task_id = $1 AND head_sha = $2`, e.task, published) == 1
	})
	e.conflictWithMovedBase()
	e.until("the conflict to decide on", func() bool { e.sync(); return e.decisionAt(e.task) == delivery.PointPRFeedback })
	e.must(e.task, "decide", `{"action":"next"}`)
	e.until("the integration on the pull request", func() bool {
		e.sync()
		return e.count(integrationRuns+` AND status = 'completed'`, e.task) == 1
	})
	if n := e.count(`SELECT count(*) FROM runs k WHERE k.task_id = $1 AND k.phase = 'review' AND k.status = 'completed'
		AND k.base_refs->>'target' = $2 AND k.ended_at <= (SELECT min(f.created_at) FROM runs f WHERE f.task_id = $1
		AND f.pr_feedback::text LIKE '%Base integration%')`, e.task, published); n == 0 {
		t.Fatalf("no review of the conductor's push %s before the integration started from it\n%s", published, e.describeRuns())
	}
	e.syncRounds(3)
	if n := e.count(integrationRuns, e.task); n != 1 {
		t.Errorf("%d integration Runs\n%s", n, e.describeRuns())
	}
}

// A direct fix's narrow review fails and a person accepts going on without
// it: the head they accepted counts as reviewed, so the conductor's publish
// reaches the pull request (else neither a publish nor the review it would
// be pointed to could). The publish is a new push of dude's that nobody
// reviewed or accepted: the next is refused again.
func TestDirectAPersonsAcceptOfAFailedNarrowReviewCountsAsItsReview(t *testing.T) {
	var crash atomic.Bool
	crash.Store(true)
	e := conductedDirectFix(t, &crash)
	before := e.gh.SHA(e.branch)
	e.must(e.task, "decide", `{"action":"next"}`)
	e.until("the narrow review failed", func() bool { e.sync(); return e.escalationReason(e.task) == "review_failed" })
	fixed := e.gh.SHA(e.branch)
	if fixed == before {
		t.Fatalf("the fix did not reach the pull request\n%s", e.describeRuns())
	}
	crash.Store(false)
	ana := e.person("Ana")
	e.assignOwner(e.task, ana)
	if code, body := e.decide(ana, e.task, "accept", "Read the fix myself."); code != 200 {
		t.Fatalf("accept: %d %v", code, body)
	}
	e.until("waiting on the pull request again", func() bool { e.sync(); return e.waitingOnPR(e.task) })
	if n := e.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'review' AND status = 'completed'
		AND base_refs->>'target' = $2`, e.task, fixed); n != 0 {
		t.Fatalf("%d completed reviews of the accepted head", n)
	}
	e.current()
	e.commit(map[string]string{"README.md": "# target\n\nfixed by the conductor\n"})
	id := e.publish()
	if status, why := e.published(id); status != delivery.PublishPublished {
		t.Fatalf("publish after the person's accept %s: %s", status, why)
	}
	moved := e.gh.SHA(e.branch)
	e.until("the pull request read at the publish", func() bool {
		e.sync()
		return e.count(`SELECT count(*) FROM pull_requests WHERE task_id = $1 AND head_sha = $2`, e.task, moved) == 1
	})
	e.current()
	e.commit(map[string]string{"README.md": "# target\n\nfixed again\n"})
	e.refused(e.task, "publish", `{"message":"another"}`, unreviewedWords)
}
