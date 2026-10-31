package orchestrator_test

import (
	"strings"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
)

// directEditing is editing (a conducted task, implemented, its conductor
// woken after implement with a commit to publish) in a delivery that pushes
// its pull request fixes directly, with a pull request open on its branch
// at the implementer's head: a push dude made, not yet reviewed.
func directEditing(t *testing.T) (*editing, string) {
	t.Helper()
	e := newEditing(t)
	setPolicy(e.world, e.task, `{"pullRequestFixes":"direct"}`)
	e.wokenWith(e.task, "after implement")
	head := e.pullRequestRow("open")
	return e, head
}

// unreviewedWords is the start of the I-3 refusal (pull-request-push-unreviewed).
const unreviewedWords = "which dude pushed and no review has covered yet"

// The pull request's last push, dude's, is unreviewed: the conductor's
// publish is refused saying so, naming start_phase review; once a review
// has started from that head, the publish goes to the pull request.
func TestDirectAConductorPublishOnAnOpenPullRequestIsAllowedOnceItsLastPushWasReviewed(t *testing.T) {
	e, head := directEditing(t)
	e.current()
	e.commit(map[string]string{"README.md": "# target\n\nfixed\n"})
	e.refused(e.task, "publish", `{"message":"a small fix"}`, unreviewedWords)
	e.refused(e.task, "publish", `{"message":"a small fix"}`, "start_phase review")
	e.refused(e.task, "publish", `{"message":"a small fix"}`, head[:7])
	if n := e.count(`SELECT count(*) FROM conductor_publishes WHERE task_id = $1`, e.task); n != 0 {
		t.Errorf("%d publishes recorded", n)
	}
	e.carriedOut(e.task, "decide", `{"action":"next"}`)
	if n := e.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'review' AND status = 'completed'
		AND base_refs->>'target' = $2`, e.task, head); n == 0 {
		t.Fatalf("no review of %s\n%s", head, e.describeRuns())
	}
	id := e.publish()
	if status, why := e.published(id); status != delivery.PublishPublished {
		t.Fatalf("publish %s: %s", status, why)
	}
	moved := e.gh.SHA(e.branch)
	if moved == head || e.count(`SELECT count(*) FROM pull_requests WHERE id = 'pr_edit' AND head_sha = $1`, moved) != 1 {
		t.Fatalf("the pull request's head did not move to the publish: %s", moved)
	}
	// That publish is a push nobody reviewed: the next is refused.
	e.current()
	e.commit(map[string]string{"README.md": "# target\n\nfixed again\n"})
	e.refused(e.task, "publish", `{"message":"another"}`, unreviewedWords)
}

// A writer child the conductor starts is refused the same way while the
// pull request's last push is dude's and unreviewed, and admitted once a
// review covered it.
func TestDirectAWriterChildIsRefusedWhileThePullRequestsLastPushIsUnreviewed(t *testing.T) {
	e, head := directEditing(t)
	e.onModel("implementer", "fake/scripted")
	e.refused(e.task, "start_agent", `{"profile":"implementer","prompt":"Rename the greeting.","key":"rename"}`, unreviewedWords)
	e.carriedOut(e.task, "decide", `{"action":"next"}`)
	if n := e.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'review' AND status = 'completed'
		AND base_refs->>'target' = $2`, e.task, head); n == 0 {
		t.Fatalf("no review of %s\n%s", head, e.describeRuns())
	}
	out := e.must(e.task, "start_agent", `{"profile":"implementer","prompt":"Rename the greeting.","key":"rename"}`)
	if id, _ := out["runId"].(string); id == "" {
		t.Fatalf("start_agent: %v", out)
	}
}

// A person's push is their own review: once the pull request shows a head
// a person pushed, the conductor publishes on it with no review of it.
func TestDirectAPersonsHeadIsNeverAnUnreviewedDudeHead(t *testing.T) {
	e, _ := directEditing(t)
	theirs := e.gh.CommitOnTop(e.branch, "A person's own fix")
	mustExec(t, e.owner, `UPDATE pull_requests SET head_sha = $1 WHERE id = 'pr_edit'`, theirs)
	e.current()
	e.commit(map[string]string{"README.md": "# target\n\nfixed\n"})
	id := e.publish()
	if status, why := e.published(id); status != delivery.PublishPublished {
		t.Fatalf("publish %s: %s", status, why)
	}
	if log := e.gh.Log(e.branch); !strings.HasPrefix(log[1], "A person's own fix") {
		t.Errorf("the publish is not on the person's commit: %v", log)
	}
}
