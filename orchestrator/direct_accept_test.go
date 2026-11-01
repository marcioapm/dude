package orchestrator_test

import (
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/fakeagent"
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
	w, task := conductedDirectWorld(t)
	cond, _, _ := w.conductor(task)
	w.until("the conductor to answer", func() bool { return len(w.said(cond)) >= 1 })
	return &editing{world: w, task: task, cond: cond, branch: w.gh.Pull(1).Head}
}

// conductedDirectWorld is a conducted delivery frozen to push its pull
// request fixes directly, its conductor asked to look after the pull
// request, waiting on it: the world and the task.
func conductedDirectWorld(t *testing.T) (*world, string) {
	t.Helper()
	w := conducting(t)
	w.toolsWithSync()
	task := w.prOpen(w.task(), prAt{direct: true})
	if status, out := w.chat(task, "Look after the pull request for me."); status >= 300 {
		t.Fatalf("chat: %d %v", status, out)
	}
	w.until("waiting on the pull request", func() bool { return w.waitingOnPR(task) })
	return w, task
}

// feedbackToDecide is a comment on the pull request, the conductor asked
// to decide on it.
func (e *editing) feedbackToDecide() {
	e.t.Helper()
	e.gh.Comment(1, "alice", "Please rename the greeting.")
	e.until("feedback to decide on", func() bool { e.sync(); return e.decisionAt(e.task) == delivery.PointPRFeedback })
}

// publishUnreviewed publishes the conductor's commit to the pull request,
// which no review has covered yet, and waits for the pull request to show
// it; it is the published head.
func (e *editing) publishUnreviewed() string {
	e.t.Helper()
	e.current()
	e.commit(map[string]string{"README.md": "# target\n\nthe conductor's fix\n"})
	id := e.publish()
	if status, why := e.published(id); status != delivery.PublishPublished {
		e.t.Fatalf("publish %s: %s", status, why)
	}
	published := e.gh.SHA(e.branch)
	e.until("the pull request read at the publish", func() bool {
		e.sync()
		return e.count(`SELECT count(*) FROM pull_requests WHERE task_id = $1 AND head_sha = $2`, e.task, published) == 1
	})
	return published
}

// reviewedBefore says a review of published completed before the first of
// the task's Runs f matching where started.
func (e *editing) reviewedBefore(published, where string) bool {
	return e.count(`SELECT count(*) FROM runs k WHERE k.task_id = $1 AND k.phase = 'review' AND k.status = 'completed'
		AND k.base_refs->>'target' = $2 AND k.ended_at <= (SELECT min(f.created_at) FROM runs f WHERE f.task_id = $1
		AND `+where+`)`, e.task, published) > 0
}

// The conductor publishes to the pull request (its last push was
// reviewed), then a comment arrives and the conductor has it fixed: the
// publish is reviewed before the fixer starts from it, so dude never
// pushes twice with no review between.
func TestDirectAPullRequestFixFirstReviewsAnUnreviewedPushOfDudes(t *testing.T) {
	e := conductedDirect(t)
	published := e.publishUnreviewed()
	e.feedbackToDecide()
	e.must(e.task, "decide", `{"action":"next","note":"NOTE-XYZZY"}`)
	e.until("the fix on the pull request, and waiting again", func() bool {
		e.sync()
		e.nextAtRound(e.task)
		return e.fixes(e.task) == 1 && e.gh.SHA(e.branch) != published && e.waitingOnPR(e.task)
	})
	if !e.reviewedBefore(published, `f.phase = 'fix' AND f.pr_feedback <> '[]'::jsonb`) {
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
	published := e.publishUnreviewed()
	e.conflictWithMovedBase()
	e.until("the conflict to decide on", func() bool { e.sync(); return e.decisionAt(e.task) == delivery.PointPRFeedback })
	e.must(e.task, "decide", `{"action":"next"}`)
	e.until("the integration on the pull request", func() bool {
		e.sync()
		e.nextAtRound(e.task)
		return e.count(integrationRuns+` AND status = 'completed'`, e.task) == 1
	})
	if !e.reviewedBefore(published, `f.pr_feedback::text LIKE '%Base integration%'`) {
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
	e.until("waiting on the pull request again", func() bool { e.sync(); e.nextAtRound(e.task); return e.waitingOnPR(e.task) })
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

// The conductor publishes to the pull request from a delivery that had no
// head on record for the repository (its git.commit_created names no
// baseSha), then a comment arrives and the conductor has it fixed: the
// review of that push is still narrow, a range of its own, never the
// whole branch. Back waiting, nothing of that fix is held: a later fix
// runs as it is told.
func TestDirectAnUnreviewedPushWithNoBaseOnRecordIsReviewedOverARange(t *testing.T) {
	e := conductedDirect(t)
	// No head on record for the repository: the publish is measured from
	// the default branch, the whole of the task's work (within limits
	// raised for it).
	mustExec(t, e.owner, `UPDATE workflow_runs SET state = state - 'heads' WHERE task_id = $1`, e.task)
	mustExec(t, e.owner, `UPDATE projects SET delivery_policy = delivery_policy || '{"conductorEditFiles":50,"conductorEditLines":5000}'
		WHERE id = (SELECT project_id FROM tasks WHERE id = $1)`, e.task)
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
	e.must(e.task, "decide", `{"action":"next","note":"NOTE-FIRST"}`)
	e.until("the review of the conductor's push", func() bool {
		e.sync()
		return e.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'review' AND status = 'completed'
			AND base_refs->>'target' = $2`, e.task, published) > 0 && e.decisionAt(e.task) != "" || e.fixes(e.task) > 0
	})
	if n := e.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'review' AND base_refs->>'target' = $2
		AND review_range IS NULL`, e.task, published); n != 0 {
		t.Errorf("%d reviews of the conductor's push over the whole branch\n%s", n, e.describeRuns())
	}
	e.until("waiting on the pull request again", func() bool { e.sync(); e.nextAtRound(e.task); return e.waitingOnPR(e.task) })
	if n := e.count(`SELECT count(*) FROM workflow_runs WHERE task_id = $1 AND state ? 'prFixAfter'`, e.task); n != 0 {
		t.Errorf("a held fix is kept while waiting on the pull request")
	}
	fixes := e.fixes(e.task)
	e.gh.Comment(1, "bob", "Please also fix the farewell.")
	e.until("feedback to decide on", func() bool { e.sync(); return e.decisionAt(e.task) == delivery.PointPRFeedback })
	e.must(e.task, "decide", `{"action":"next","note":"NOTE-LATER"}`)
	e.until("the later fix", func() bool { e.sync(); return e.fixes(e.task) > fixes })
	if got := e.ids(`SELECT COALESCE(conductor_note, '') FROM runs WHERE task_id = $1 AND phase = 'fix'
		AND pr_feedback <> '[]'::jsonb ORDER BY created_at DESC LIMIT 1`, e.task); len(got) != 1 || got[0] != "NOTE-LATER" {
		t.Errorf("the later fix was told %q\n%s", got, e.describeRuns())
	}
}

// notConvergingDirect is a conducted direct delivery whose fix's narrow
// review keeps judging its blocking finding still until the loop
// escalates not_converging, as editing on it, that escalation waiting.
func notConvergingDirect(t *testing.T) *editing {
	t.Helper()
	e := conductedDirect(t)
	setPolicy(e.world, e.task, `{"maxAttemptsPerFinding":4,"maxStillRounds":2,"maxPrFixReviewIterations":9}`)
	var armed atomic.Bool
	var reviews atomic.Int32
	scripted := e.lux.Decide
	e.lux.Decide = func(spec map[string]any) fakelux.Behaviour {
		labels, _ := spec["labels"].(map[string]any)
		if !armed.Load() {
			return scripted(spec)
		}
		switch labels["dude.phase"] {
		case "review":
			if reviews.Add(1) == 1 {
				return fakelux.Behaviour{Reply: "```yaml\n" + fakeagent.Finding + "```\n"}
			}
			return fakelux.Behaviour{Reply: "```yaml\nverdicts:\n  F1: still\n```\n"}
		case "fix":
			return fakelux.Behaviour{Commit: map[string]string{"FACTORY.md": fmt.Sprint(labels["dude.run"]) + "\n"}, Message: "work", Checks: fakeagent.NoChecks}
		}
		return scripted(spec)
	}
	e.feedbackToDecide()
	armed.Store(true)
	e.must(e.task, "decide", `{"action":"next"}`)
	e.until("the narrow loop not converging", func() bool {
		e.sync()
		e.nextAtRound(e.task)
		return e.escalationReason(e.task) == delivery.ReasonNotConverging
	})
	return e
}

// A direct fix's narrow review keeps judging its blocking finding still
// until the loop escalates not_converging, and a person accepts going on:
// the conductor's publish reaches the pull request (the escalated head is
// the last round's, which reviewed it); that publish is a new unreviewed
// push of dude's, so the next is refused again.
func TestDirectAPersonsAcceptOfANotConvergingNarrowLoopCountsAsItsReview(t *testing.T) {
	e := notConvergingDirect(t)
	fixed := e.gh.SHA(e.branch)
	ana := e.person("Ana")
	e.assignOwner(e.task, ana)
	if code, body := e.decide(ana, e.task, "accept", "The record file is this pull request's own convention."); code != 200 {
		t.Fatalf("accept: %d %v", code, body)
	}
	e.until("waiting on the pull request again", func() bool { e.sync(); e.nextAtRound(e.task); return e.waitingOnPR(e.task) })
	if e.gh.SHA(e.branch) != fixed {
		t.Fatalf("the branch moved past the accepted head\n%s", e.describeRuns())
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

// A direct fix's narrow loop escalates not_converging and, while that
// waits, the conductor publishes to the pull request; then a person
// accepts going on. The accept covers the head the conductor's publish
// left (else no publish could reach the pull request after it): the next
// publish reaches it, and that one, a new unreviewed push of dude's, has
// the one after refused again.
func TestDirectAPersonsAcceptOfANotConvergingNarrowLoopCoversAPublishWhileItWaited(t *testing.T) {
	e := notConvergingDirect(t)
	e.current()
	e.commit(map[string]string{"README.md": "# target\n\npublished while the escalation waits\n"})
	id := e.publish()
	if status, why := e.published(id); status != delivery.PublishPublished {
		t.Fatalf("publish while the escalation waits %s: %s", status, why)
	}
	waited := e.gh.SHA(e.branch)
	e.until("the pull request read at the publish", func() bool {
		e.sync()
		return e.count(`SELECT count(*) FROM pull_requests WHERE task_id = $1 AND head_sha = $2`, e.task, waited) == 1
	})
	if r := e.escalationReason(e.task); r != delivery.ReasonNotConverging {
		t.Fatalf("the escalation is %q, not %s, after the publish", r, delivery.ReasonNotConverging)
	}
	ana := e.person("Ana")
	e.assignOwner(e.task, ana)
	if code, body := e.decide(ana, e.task, "accept", "The record file is this pull request's own convention."); code != 200 {
		t.Fatalf("accept: %d %v", code, body)
	}
	e.until("waiting on the pull request again", func() bool { e.sync(); e.nextAtRound(e.task); return e.waitingOnPR(e.task) })
	if e.gh.SHA(e.branch) != waited {
		t.Fatalf("the branch moved past the conductor's publish\n%s", e.describeRuns())
	}
	e.current()
	e.commit(map[string]string{"README.md": "# target\n\nfixed by the conductor\n"})
	id = e.publish()
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

// The conductor publishes to the pull request, then a comment arrives and
// the conductor starts the fix with then at the pull request's feedback
// decision: it is not refused for the unreviewed publish (reviewFirst's
// path), the publish is reviewed first, then the fix runs as directed —
// with its note, and then its queued reviewers.
func TestDirectAConductedFixWithThenAtFeedbackFirstReviewsAnUnreviewedPushOfDudes(t *testing.T) {
	e := conductedDirect(t)
	published := e.publishUnreviewed()
	e.feedbackToDecide()
	e.must(e.task, "start_phase", `{"phase":"fix","note":"NOTE-XYZZY","then":{"review":["tests"]}}`)
	queued := `SELECT count(*) FROM events q WHERE q.task_id = $1 AND q.event_type = 'run.created'
		AND q.payload->>'phase' = 'review' AND q.payload->>'reviewers' = '["tests"]'
		AND q.cursor > (SELECT min(f.cursor) FROM events f WHERE f.task_id = $1 AND f.event_type = 'run.created'
		AND f.payload->>'phase' = 'fix')`
	e.until("the fix on the pull request, then its queued review", func() bool {
		e.sync()
		if e.fixes(e.task) == 0 {
			e.nextAtRound(e.task)
		}
		return e.count(queued, e.task) > 0
	})
	if !e.reviewedBefore(published, `f.phase = 'fix' AND f.pr_feedback <> '[]'::jsonb`) {
		t.Fatalf("no review of the conductor's push %s before the fixer started from it\n%s", published, e.describeRuns())
	}
	if got := e.ids(`SELECT COALESCE(conductor_note, '') FROM runs WHERE task_id = $1 AND phase = 'fix'
		AND pr_feedback <> '[]'::jsonb ORDER BY created_at`, e.task); len(got) != 1 || got[0] != "NOTE-XYZZY" {
		t.Errorf("the pull request fix's note: %q", got)
	}
}

// The integration variant: the conductor publishes, the base moves into a
// conflict, and at that pull_request_feedback decision the conductor
// starts the fix itself: not refused for the unreviewed publish, which is
// reviewed first; then the integration runs, once.
func TestDirectAConductedFixAtAConflictFirstReviewsAnUnreviewedPushOfDudes(t *testing.T) {
	e := conductedDirect(t)
	published := e.publishUnreviewed()
	e.conflictWithMovedBase()
	e.until("the conflict to decide on", func() bool { e.sync(); return e.decisionAt(e.task) == delivery.PointPRFeedback })
	e.must(e.task, "start_phase", `{"phase":"fix","note":"Keep both."}`)
	e.until("the integration on the pull request", func() bool {
		e.sync()
		e.nextAtRound(e.task)
		return e.count(integrationRuns+` AND status = 'completed'`, e.task) == 1
	})
	if !e.reviewedBefore(published, `f.pr_feedback::text LIKE '%Base integration%'`) {
		t.Fatalf("no review of the conductor's push %s before the integration started from it\n%s", published, e.describeRuns())
	}
}
