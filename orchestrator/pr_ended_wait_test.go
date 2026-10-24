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
	var closed, open int
	if err := m.owner.QueryRow(context.Background(), `SELECT (payload->'detail'->>'closed')::int, (payload->'detail'->>'open')::int
		FROM events WHERE task_id = $1 AND event_type = 'question.asked' AND payload->>'reason' = 'pull_request_closed'
		ORDER BY cursor DESC LIMIT 1`, m.task).Scan(&closed, &open); err != nil {
		t.Fatal(err)
	}
	if closed != 2 || open != 1 {
		t.Errorf("asked about %d closed and %d open, want 2 and 1", closed, open)
	}
	m.afterEnd()
	if n := m.count(askedAboutCloses, m.task); n != 2 {
		t.Errorf("asked %d times, want twice: target's close, then api's", n)
	}
}
