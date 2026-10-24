package orchestrator_test

// A person who chose to wait after one pull request of two closed is not
// asked about that close again: not by the close's own pr.feedback still
// waiting when the delivery goes back to the pull requests, and not by a
// later round on the one still open. A further end is asked about, or ends
// the task.

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/fakelux"
)

// askedAboutCloses counts the question.asked a pull request closing raised.
const askedAboutCloses = `SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'question.asked'
	AND payload->>'reason' = 'pull_request_closed'`

// mixedEnd is a delivery across target and web whose pull requests are
// open, each pull request fixer committing web's file (and target's, once
// touchTarget is set), the first one held at work until its turn is ended.
type mixedEnd struct {
	*world
	task        string
	fixers      atomic.Int32
	touchTarget atomic.Bool
}

func newMixedEnd(t *testing.T) *mixedEnd {
	return newMixedEndIn(t, prAt{web: true})
}

// newMixedEndIn is newMixedEnd with the repositories at says.
func newMixedEndIn(t *testing.T, at prAt) *mixedEnd {
	m := &mixedEnd{world: newWorld(t)}
	scripted := m.lux.Decide
	m.lux.Decide = func(spec map[string]any) fakelux.Behaviour {
		b := scripted(spec)
		switch labels, _ := spec["labels"].(map[string]any); labels["dude.phase"] {
		case "fix":
			n := m.fixers.Add(1)
			b.Hang = n == 1
			b.Commit = map[string]string{"web:TITLE.md": fmt.Sprintf("the page title, fixed %d\n", n)}
			if m.touchTarget.Load() {
				b.Commit["target:SHARED.md"] = fmt.Sprintf("shared, fixed %d\n", n)
			}
		case "simplify":
			b.Commit = nil
		}
		return b
	}
	m.task = m.prOpen(m.world.task(), at)
	return m
}

// waitPastTargetsClose closes target's pull request while web's fixer is at
// work, so its terminal pr.feedback waits unread while the fix lands and
// the person is asked (prFixLanded); the person waits.
func (m *mixedEnd) waitPastTargetsClose() {
	m.t.Helper()
	m.web.Comment(1, "alice", "Please fix the page title.")
	m.until("web's fixer at work", func() bool { m.sync(); return m.count(runningFixers, m.task) == 1 })
	luxID := m.runningFixerLuxID(m.task)
	m.gh.Close(1)
	m.afterEnd()
	m.lux.EndTurn(luxID)
	m.until("the person asked", func() bool { m.sync(); m.pump(); return m.taskStatus(m.task) == "awaiting_input" })
	if r := m.escalationReason(m.task); r != "pull_request_closed" {
		m.t.Fatalf("escalated %q\n%s", r, m.describeRuns())
	}
	// The close's own terminal pr.feedback waits unread for awaitPullRequest.
	if n := m.count(`SELECT count(*) FROM workflow_signals s JOIN workflow_runs r ON r.id = s.workflow_run_id
		WHERE r.task_id = $1 AND s.name = 'pr.feedback' AND s.payload->>'kind' = 'terminal' AND s.consumed_at IS NULL`, m.task); n != 1 {
		m.t.Fatalf("%d unread terminal pr.feedback signals before the decision, want target's", n)
	}
	if code, body := m.decide(m.person("owner"), m.task, "wait", ""); code != 200 {
		m.t.Fatalf("decide: %d %v", code, body)
	}
	// Back on the pull requests, or asked again.
	m.until("the wait carried out", func() bool {
		m.pump()
		return m.waitingOnPR(m.task) || m.count(askedAboutCloses, m.task) > 1
	})
	m.afterEnd()
}

// assertAskedOnce: one question about the closes, and the task waiting on
// web's pull request.
func (m *mixedEnd) assertAskedOnce() {
	m.t.Helper()
	if n := m.count(askedAboutCloses, m.task); n != 1 {
		m.t.Fatalf("asked %d times about target's close, want once; task %s", n, m.taskStatus(m.task))
	}
	if s := m.taskStatus(m.task); s != "review" && s != "ready_to_merge" {
		m.t.Errorf("task is %s, want review", s)
	}
	if !m.waitingOnPR(m.task) {
		st, step, _ := m.deliveryStatus(m.task)
		m.t.Errorf("delivery %s at %s, want waiting on the pull requests", st, step)
	}
}

// One closed beside one open, and the person waits: asked once, and the
// task waits on web's pull request.
func TestWaitingPastAMixedEndIsNotAskedAgain(t *testing.T) {
	m := newMixedEnd(t)
	m.waitPastTargetsClose()
	m.assertAskedOnce()
}

// laterRound comments on web's pull request again and waits for that
// round's fix to land on it (prFixLanded).
func (m *mixedEnd) laterRound() {
	m.t.Helper()
	webHead := func() string { return m.web.SHA(m.web.Pull(1).Head) }
	before := webHead()
	m.web.Comment(1, "alice", "And the subtitle, please.")
	m.until("the later round's fix on web's pull request", func() bool {
		m.sync()
		m.pump()
		return webHead() != before && m.count(runningFixers, m.task) == 0 && m.waitingOnPR(m.task) ||
			m.count(askedAboutCloses, m.task) > 1
	})
	m.afterEnd()
	if n := m.fixers.Load(); n != 2 {
		m.t.Errorf("%d pull request fixers, want the first round's and this one's\n%s", n, m.describeRuns())
	}
}

// A later round on web's pull request, its fix landing: target's close,
// which the person waited past, is not asked about again.
func TestALaterRoundAfterWaitingPastAMixedEndIsNotAskedAgain(t *testing.T) {
	m := newMixedEnd(t)
	m.waitPastTargetsClose()
	m.laterRound()
	m.assertAskedOnce()
}

// A later round's fix for web that also changes target opens no pull
// request in target, whose close the person waited past.
func TestALaterFixOpensNoPullRequestWhereThePersonWaitedPastAClose(t *testing.T) {
	m := newMixedEnd(t)
	m.waitPastTargetsClose()
	m.touchTarget.Store(true)
	m.laterRound()
	m.assertAskedOnce()
	if n := len(m.gh.Pulls()); n != 1 {
		t.Errorf("%d target pull requests, want the closed one", n)
	}
	if n := m.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'pull_request.action'
		AND payload->>'action' = 'not-opened' AND payload->>'repo' = 'target' AND (payload->>'number')::int = 1`, m.task); n != 1 {
		t.Errorf("%d activity lines for target's change left without a pull request, want 1", n)
	}
}

// Target's pull request reopened and merged after the person waited past
// its close: no longer closed, so a later fix that changes target opens a
// pull request there, and no activity line says it did not.
func TestALaterFixOpensAPullRequestWhereTheWaitedPastCloseWasReopenedAndMerged(t *testing.T) {
	m := newMixedEnd(t)
	m.waitPastTargetsClose()
	m.gh.Reopen(1)
	m.sync()
	m.gh.Merge(1)
	m.until("target's pull request recorded merged", func() bool {
		m.sync()
		return m.count(`SELECT count(*) FROM pull_requests p JOIN repositories r ON r.id = p.repository_id
			WHERE r.name = 'target' AND p.number = 1 AND p.state = 'merged'`) == 1
	})
	m.afterEnd()
	m.touchTarget.Store(true)
	m.laterRound()
	if n := len(m.gh.Pulls()); n != 2 {
		t.Errorf("%d target pull requests, want the merged one and the later fix's\n%s", n, m.describeRuns())
	}
	if n := m.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'pull_request.action'
		AND payload->>'action' = 'not-opened'`, m.task); n != 0 {
		t.Errorf("%d activity lines for a change left without a pull request, want none", n)
	}
}

// A reviewer's question on a later round's fix, after the person waited
// past target's close: the question holds the fix's promotion while web is
// open, and the accepted close neither ends the delivery nor is asked about.
func TestAQuestionAfterWaitingPastAMixedEndHoldsThePromotion(t *testing.T) {
	m := newMixedEnd(t)
	m.waitPastTargetsClose()
	scripted := m.lux.Decide
	var asked atomic.Bool
	m.lux.Decide = func(spec map[string]any) fakelux.Behaviour {
		b := scripted(spec)
		if labels, _ := spec["labels"].(map[string]any); labels["dude.phase"] == "review" && asked.CompareAndSwap(false, true) {
			b.Reply += "\n" + questionDoc
		}
		return b
	}
	m.web.Comment(1, "alice", "And the subtitle, please.")
	webHead := func() string { return m.web.SHA(m.web.Pull(1).Head) }
	before := webHead()
	m.until("the later fix held at its promotion", func() bool {
		m.sync()
		m.pump()
		return m.count(`SELECT count(*) FROM workflow_runs WHERE task_id = $1 AND step = 'promoteCandidate' AND status = 'waiting'`, m.task) == 1 ||
			delivery.Ended(m.taskStatus(m.task)) || m.count(askedAboutCloses, m.task) > 1
	})
	m.afterEnd()
	if s := m.taskStatus(m.task); s != "awaiting_input" {
		t.Fatalf("task %s, want awaiting_input on the reviewer's question\n%s", s, m.describeRuns())
	}
	if n := m.count(`SELECT count(*) FROM review_questions WHERE task_id = $1 AND status = 'open'`, m.task); n != 1 {
		t.Errorf("%d open reviewer questions, want 1", n)
	}
	if n := m.count(`SELECT count(*) FROM workflow_runs WHERE task_id = $1 AND step = 'promoteCandidate' AND status = 'waiting'`, m.task); n != 1 {
		t.Errorf("delivery not held at promoteCandidate\n%s", m.describeRuns())
	}
	if n := m.count(askedAboutCloses, m.task); n != 1 {
		t.Errorf("asked %d times about target's close, want once", n)
	}
	// Answered, the held fix is promoted: the hold kept it, not endLoop's
	// abandoning it over the close already waited past.
	m.settle("key_owner_"+m.org, m.reviewQuestionOf(m.task), "answer")
	m.until("the held fix promoted to web's pull request", func() bool { m.sync(); m.pump(); return webHead() != before })
}

// Work past the review loop after the person waited past target's close,
// its fix changing target and web: target's pull request will not open, so
// the delivery goes back to web's without asking Open or Draft, and says
// target's change got none.
func TestWorkPastTheReviewLoopAfterWaitingPastACloseAsksNoOpenOrDraft(t *testing.T) {
	w := conducting(t)
	arm := w.firstReviewAfter()
	inLoop := w.lux.Decide
	w.lux.Decide = func(spec map[string]any) fakelux.Behaviour {
		b := inLoop(spec)
		if labels, _ := spec["labels"].(map[string]any); labels["dude.phase"] == "fix" {
			b.Commit = map[string]string{"target:FIX.md": "fix\n", "web:FIX.md": "fix\n"}
		}
		return b
	}
	task := w.prOpen(w.task(), prAt{web: true})
	setPolicy(w, task, `{"maxReviewIterations":9}`)
	if status, out := w.chat(task, "Look after the pull request for me."); status >= 300 {
		t.Fatalf("chat: %d %v", status, out)
	}
	w.until("waiting on the pull requests", func() bool { return w.waitingOnPR(task) })
	w.gh.Close(1)
	w.until("the person asked", func() bool { w.sync(); w.pump(); return w.taskStatus(task) == "awaiting_input" })
	if r := w.escalationReason(task); r != "pull_request_closed" {
		t.Fatalf("escalated %q\n%s", r, w.describeRuns())
	}
	if code, body := w.decide(w.person("owner"), task, "wait", ""); code != 200 {
		t.Fatalf("decide: %d %v", code, body)
	}
	w.until("waiting on web's pull request", func() bool { w.pump(); return w.waitingOnPR(task) })
	arm()

	w.must(task, "start_phase", `{"phase":"review","categories":["correctness"]}`)
	w.until("the review's decision", func() bool { return w.decisionAt(task) == delivery.PointReviewed })
	w.must(task, "start_phase", `{"phase":"fix","note":"Fix the finding the review raised."}`)
	w.until("after the fix", func() bool { return w.decisionAt(task) == delivery.PointFixed })
	w.must(task, "start_phase", `{"phase":"review","categories":["correctness"]}`)
	webBefore := len(w.web.BranchLog(w.web.Pull(1).Head))
	w.until("waiting on web's pull request again, or the decision before the pull request", func() bool {
		switch w.decisionAt(task) {
		case delivery.PointReviewed, delivery.PointFixed:
			w.must(task, "decide", `{"action":"next"}`)
		case delivery.PointBeforePR:
			return true
		}
		return w.waitingOnPR(task) && len(w.web.BranchLog(w.web.Pull(1).Head)) > webBefore
	})
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = $2 AND payload->>'point' = $3`,
		task, delivery.EvDecisionAwaited, delivery.PointBeforePR); n != 0 {
		t.Errorf("%d decisions awaited before the pull request, want none\n%s", n, w.describeRuns())
	}
	if n := len(w.gh.Pulls()); n != 1 {
		t.Errorf("%d target pull requests, want the closed one", n)
	}
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'pull_request.action'
		AND payload->>'action' = 'not-opened' AND payload->>'repo' = 'target'`, task); n != 1 {
		t.Errorf("%d activity lines for target's change left without a pull request, want 1", n)
	}
}

// Web closing too, after the person waited past target's close: no pull
// request is left open, so the task ends (aborted) and nothing is asked.
func TestTheOtherClosingAfterWaitingPastAMixedEndEndsTheTask(t *testing.T) {
	m := newMixedEnd(t)
	m.waitPastTargetsClose()
	m.web.Close(1)
	m.until("the task to end", func() bool { m.sync(); m.pump(); return m.taskStatus(m.task) == "aborted" })
	m.afterEnd()
	m.assertEnded(m.task, "aborted")
	if n := m.count(askedAboutCloses, m.task); n != 1 {
		t.Errorf("asked %d times about the closes, want once", n)
	}
}

// Web merging instead: what is open is taken, and the closed part was the
// person's to let go, so the task is done.
func TestTheOtherMergingAfterWaitingPastAMixedEndFinishesTheTask(t *testing.T) {
	m := newMixedEnd(t)
	m.waitPastTargetsClose()
	m.web.Merge(1)
	m.until("the task to finish", func() bool { m.sync(); m.pump(); return m.taskStatus(m.task) == "done" })
	m.afterEnd()
	m.assertEnded(m.task, "done")
	if n := m.count(askedAboutCloses, m.task); n != 1 {
		t.Errorf("asked %d times about the closes, want once", n)
	}
}

// Web closing while the person is still asked about target's close ends
// the task and withdraws the question.
func TestTheOtherClosingWhileAskedAboutAMixedEndWithdrawsTheQuestion(t *testing.T) {
	m := newMixedEnd(t)
	m.gh.Close(1)
	m.until("the person asked", func() bool { m.sync(); m.pump(); return m.taskStatus(m.task) == "awaiting_input" })
	m.web.Close(1)
	m.until("the task to end", func() bool { m.sync(); m.pump(); return m.taskStatus(m.task) == "aborted" })
	m.afterEnd()
	m.assertEnded(m.task, "aborted")
	if n := m.count(askedAboutCloses, m.task); n != 1 {
		t.Errorf("asked %d times about the closes, want once", n)
	}
	if n := m.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'task.decided'
		AND payload->>'reason' = 'pull_request_closed' AND actor_type = 'system'`, m.task); n != 1 {
		t.Errorf("%d withdrawals of the question, want 1", n)
	}
}

// Three repositories: target closes and the person waits; then api closes
// too, beside web still open. That close is new, so the person is asked
// again, about both.
func TestANewCloseAfterWaitingPastAMixedEndIsAskedAbout(t *testing.T) {
	m := newMixedEndIn(t, prAt{web: true, api: true})
	m.waitPastTargetsClose()
	m.assertAskedOnce()
	m.apiRepo.Close(1)
	m.until("the person asked again", func() bool {
		m.sync()
		m.pump()
		return m.count(askedAboutCloses, m.task) == 2 && m.taskStatus(m.task) == "awaiting_input"
	})
	m.assertLastAskedAbout(2, 1)
	m.afterEnd()
	if n := m.count(askedAboutCloses, m.task); n != 2 {
		t.Errorf("asked %d times, want twice: target's close, then api's", n)
	}
}

// assertLastAskedAbout checks the closed and open counts on the latest
// question about the closes.
func (m *mixedEnd) assertLastAskedAbout(wantClosed, wantOpen int) {
	m.t.Helper()
	var closed, open int
	if err := m.owner.QueryRow(context.Background(), `SELECT (payload->'detail'->>'closed')::int, (payload->'detail'->>'open')::int
		FROM events WHERE task_id = $1 AND event_type = 'question.asked' AND payload->>'reason' = 'pull_request_closed'
		ORDER BY cursor DESC LIMIT 1`, m.task).Scan(&closed, &open); err != nil {
		m.t.Fatal(err)
	}
	if closed != wantClosed || open != wantOpen {
		m.t.Errorf("asked about %d closed and %d open, want %d and %d", closed, open, wantClosed, wantOpen)
	}
}

// Api closing while the person is still asked about target's close: the
// wait covers only the close they were asked about, so they are asked
// again, about both.
func TestACloseWhileAskedIsNotWaitedPast(t *testing.T) {
	m := newMixedEndIn(t, prAt{web: true, api: true})
	m.gh.Close(1)
	m.until("the person asked", func() bool { m.sync(); m.pump(); return m.taskStatus(m.task) == "awaiting_input" })
	m.apiRepo.Close(1)
	m.afterEnd()
	if code, body := m.decide(m.person("owner"), m.task, "wait", ""); code != 200 {
		t.Fatalf("decide: %d %v", code, body)
	}
	m.until("the person asked again", func() bool {
		m.sync()
		m.pump()
		return m.count(askedAboutCloses, m.task) == 2 && m.taskStatus(m.task) == "awaiting_input"
	})
	m.assertLastAskedAbout(2, 1)
}
