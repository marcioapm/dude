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
	w := conducting(t)
	w.toolsWithSync()
	task := w.prOpen(w.task(), prAt{direct: true})
	if status, out := w.chat(task, "Look after the pull request for me."); status >= 300 {
		t.Fatalf("chat: %d %v", status, out)
	}
	w.until("waiting on the pull request", func() bool { return w.waitingOnPR(task) })
	scripted := w.lux.Decide
	w.lux.Decide = func(spec map[string]any) fakelux.Behaviour {
		if labels, _ := spec["labels"].(map[string]any); labels["dude.phase"] == "review" && crash.Load() {
			return fakelux.Behaviour{Crash: true}
		}
		return scripted(spec)
	}
	w.gh.Comment(1, "alice", "Please rename the greeting.")
	w.until("feedback to decide on", func() bool { w.sync(); return w.decisionAt(task) == delivery.PointPRFeedback })
	cond, _, _ := w.conductor(task)
	return &editing{world: w, task: task, cond: cond, branch: w.gh.Pull(1).Head}
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
