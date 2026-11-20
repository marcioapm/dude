package orchestrator_test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/fakeagent"
	"github.com/marciomartins/dude/orchestrator/internal/fakegithub"
)

// A pull request fixer's "fixed" reply and thread
// resolution must not reach GitHub while its fix is unpublished. Its calls
// are captured; dude posts them itself once the published fix is on the
// pull request, whoever decides the delivery, and wakes a conductor with
// what it posted.

const fixerReplyText = "Fixed: it explains now."

// earlyFixer delivers a task to its pull request, then has a person's line
// comment send it a fixer whose feedback has it reply and resolve that
// thread through the tool server, on its first turn: before its checks,
// which it reports passing over a failing command (SetFixerChecks with
// ChecksFixedModel), so the conductor sends it back and nothing is
// published then. holdPushes has lux hold its pushes too. It returns the
// world, the task, the line comment and the fixer's Run once its first
// turn, calls made, has ended.
func earlyFixer(t *testing.T, holdPushes bool) (*world, string, int64, string) {
	w := defaultsConducting(t)
	task := w.task()
	w.deliverByAPI(task)
	w.until("the pull request wait", func() bool {
		return len(w.gh.Pulls()) == 1 && w.count(`SELECT count(*) FROM workflow_runs WHERE task_id = $1 AND step = 'awaitPullRequest' AND status = 'waiting'`, task) == 1
	})
	// The pull request's fixer, from here on, claims its check passes on
	// its first turn, and is right only once sent back.
	w.lux.SetFixerChecks(fakeagent.ChecksFixedModel)
	w.lux.SetHoldPushes(holdPushes)
	own := w.gh.LineComment(1, "ana", "FACTORY.md", "Please explain.")
	w.gh.EditComment(1, own, fmt.Sprintf("Please explain.\n"+
		"tool: reply_on_pull_request {\"pr\":\"1\",\"text\":%q,\"in_reply_to\":\"line-comment-%d\"}\n"+
		"tool: resolve_thread {\"pr\":\"1\",\"thread\":\"line-comment-%d\"}\nThanks.", fixerReplyText, own, own))
	var fixer string
	w.until("the fixer's first turn, its calls made, to end", func() bool {
		// The feedback's quiet passed: the conductor's turn comes now.
		mustExec(t, w.owner, `UPDATE conductor_wakes SET created_at = now() - make_interval(secs => $2)
			WHERE task_id = $1 AND kind = $3 AND delivered_at IS NULL`, task, (delivery.FeedbackQuiet + time.Second).Seconds(), delivery.WakeFeedback)
		w.sync()
		fixer = w.str(`SELECT COALESCE((SELECT id FROM runs WHERE task_id = $1 AND phase = 'fix' AND pr_feedback::text LIKE '%Please explain%'
			ORDER BY created_at DESC LIMIT 1), '')`, task)
		return fixer != "" && w.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = $2`, fixer, delivery.EvRunChecksAsked) > 0
	})
	return w, task, own, fixer
}

// fixerCaptured says the fixer's reply and resolution both reached the
// tool server and were captured on its Run, its reply's text with it.
func (w *world) fixerCaptured(fixer string) bool {
	return w.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = $2
		AND (payload->>'kind' = 'resolve_thread' OR payload->>'text' = $3)`, fixer, delivery.EvAnswerHeld, fixerReplyText) == 2
}

// threadState is the fake GitHub's replies in line comment own's thread
// that start with text, and whether the thread is resolved.
func (w *world) threadState(own int64, text string) (int, bool) {
	n, resolved := 0, false
	p := w.gh.Pull(1)
	w.gh.Set(func(s *fakegithub.Server) {
		for _, c := range p.LineComments {
			if c.InReplyTo == own && strings.HasPrefix(c.Body, text) {
				n++
			}
		}
		resolved = s.ResolvedThreads[s.ThreadID(own)]
	})
	return n, resolved
}

// A conductor-decided delivery. A fixer replies and resolves its thread on
// its first turn, before its checks are backed: both are captured, and
// nothing reaches GitHub, however many syncs pass. Sent back, it backs its
// checks and its fix is published: dude posts its reply once and resolves
// its thread, and the conductor is woken naming the fixer, its published
// commit and what was posted.
func TestAFixersRepliesPostOnceItsFixIsPublished(t *testing.T) {
	w, task, own, fixer := earlyFixer(t, false)
	w.syncs(3)
	if n, resolved := w.threadState(own, fixerReplyText); n != 0 || resolved {
		t.Fatalf("before publication: %d replies posted, resolved %v", n, resolved)
	}
	if !w.fixerCaptured(fixer) {
		t.Error("the fixer's reply and resolution were not both captured")
	}
	if d := w.str(`SELECT COALESCE(state->>'decider', '') FROM workflow_runs WHERE task_id = $1 ORDER BY created_at DESC LIMIT 1`, task); d != delivery.DeciderConductor {
		t.Errorf("decided by %q, want the conductor", d)
	}
	w.until("the fixer's fix published and its answer posted", func() bool {
		w.sync()
		n, resolved := w.threadState(own, fixerReplyText)
		return n > 0 && resolved
	})
	if got := w.ids(`SELECT CASE WHEN (payload->>'verified')::bool THEN 'verified' ELSE 'unverified' END FROM events
		WHERE run_id = $1 AND event_type = $2 ORDER BY cursor`, fixer, delivery.EvRunChecksJudged); strings.Join(got, ",") != "unverified,verified" {
		t.Errorf("the fixer judged %v, want sent back once, then verified", got)
	}
	head := w.str(`SELECT COALESCE((SELECT payload->>'headSha' FROM events WHERE run_id = $1 AND event_type = $2 ORDER BY cursor LIMIT 1), '')`,
		fixer, delivery.EvGitCommitCreated)
	if head == "" {
		t.Fatal("its answer was posted with no fix of its published")
	}
	w.syncs(3)
	if n, resolved := w.threadState(own, fixerReplyText); n != 1 || !resolved {
		t.Errorf("after publication: %d of the fixer's replies posted, resolved %v; want one, resolved", n, resolved)
	}
	wake := w.str(`SELECT COALESCE(string_agg(line, ' | '), '') FROM conductor_wakes WHERE task_id = $1 AND kind = $2 AND key LIKE 'answers:' || $3 || ':%'`,
		task, delivery.WakeRound, fixer)
	for _, want := range []string{fixer, "target@" + head[:7], fmt.Sprintf("replied to line-comment-%d", own), fmt.Sprintf("resolved line-comment-%d", own)} {
		if !strings.Contains(wake, want) {
			t.Errorf("the conductor's wake %q does not name %q", wake, want)
		}
	}
}

// A policy-decided delivery (no conductor model: Deliver decides). A
// person's line comment, and a fixer that answers it and resolves its
// thread: before its fix is published (lux holding its push) nothing is on
// GitHub; once published, the reply is there once and the thread is
// resolved, with no conductor to relay it.
func TestAPolicyDeliverysFixerAnswersArePostedAtPublication(t *testing.T) {
	w := newWorld(t)
	w.withTools()
	task := w.reviewing()
	// Deliver decides, as for a task whose conductor has no model or whose
	// person accepted the defaults (delivery.DeciderPolicy).
	mustExec(t, w.owner, `UPDATE workflow_runs SET state = jsonb_set(state, '{decider}', to_jsonb($2::text)) WHERE task_id = $1`,
		task, delivery.DeciderPolicy)
	w.lux.SetHoldPushes(true)
	own := w.gh.LineComment(1, "ana", "FACTORY.md", "Please explain.")
	w.gh.EditComment(1, own, fmt.Sprintf("Please explain.\n"+
		"tool: reply_on_pull_request {\"pr\":\"1\",\"text\":%q,\"in_reply_to\":\"line-comment-%d\"}\n"+
		"tool: resolve_thread {\"pr\":\"1\",\"thread\":\"line-comment-%d\"}\nThanks.", fixerReplyText, own, own))
	var fixer string
	w.until("the fixer's calls captured", func() bool {
		w.sync()
		fixer = w.str(`SELECT COALESCE((SELECT id FROM runs WHERE task_id = $1 AND phase = 'fix' AND pr_feedback::text LIKE '%Please explain%'
			ORDER BY created_at DESC LIMIT 1), '')`, task)
		return fixer != "" && w.fixerCaptured(fixer)
	})
	w.syncs(3)
	if n, resolved := w.threadState(own, fixerReplyText); n != 0 || resolved {
		t.Fatalf("before publication: %d replies posted, resolved %v", n, resolved)
	}
	w.lux.SetHoldPushes(false)
	w.lux.ReleasePushes()
	w.until("the fix published and its answer posted", func() bool {
		w.sync()
		n, resolved := w.threadState(own, fixerReplyText)
		return n > 0 && resolved
	})
	w.syncs(3)
	if n, resolved := w.threadState(own, fixerReplyText); n != 1 || !resolved {
		t.Errorf("after publication: %d replies posted, resolved %v; want one, resolved", n, resolved)
	}
	if n := w.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = $2`, fixer, delivery.EvGitCommitCreated); n == 0 {
		t.Error("answered with no fix published")
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND role = 'conductor'`, task); n != 0 {
		t.Errorf("%d conductor Runs on a policy delivery", n)
	}
}

// A fixer aborted before its fix is published (lux holding its push) has
// its answers captured and never posted: after the abort, the held push
// released and more syncs, GitHub has no reply and no resolution of its.
func TestAnAbandonedFixerHasNoDelayedReplies(t *testing.T) {
	w, _, own, fixer := earlyFixer(t, true)
	if !w.fixerCaptured(fixer) {
		t.Error("the fixer's reply and resolution were not both captured")
	}
	mustExec(t, w.owner, `UPDATE runs SET control = 'abort' WHERE id = $1`, fixer)
	w.until("the fixer aborted", func() bool {
		w.sync()
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status IN ('aborted', 'failed')`, fixer) == 1
	})
	w.lux.SetHoldPushes(false)
	w.lux.ReleasePushes()
	w.syncs(5)
	if n := w.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = $2`, fixer, delivery.EvGitCommitCreated); n != 0 {
		t.Errorf("the aborted fixer published %d times", n)
	}
	if n, resolved := w.threadState(own, fixerReplyText); n != 0 || resolved {
		t.Errorf("after the abort: %d replies posted, resolved %v", n, resolved)
	}
}

// The conductor's own reply, while no fixer is involved, is posted at once
// (with a fixer live: TestReviewerEveryFixerReplyHeldConductorImmediateDuringFix).
func TestTheConductorsReplyIsNotHeld(t *testing.T) {
	w := conducting(t)
	task := w.reviewing()
	if status, out := w.chat(task, "Look after the pull request for me."); status >= 300 {
		t.Fatalf("chat: %d %v", status, out)
	}
	line := w.gh.LineComment(1, "ana", "FACTORY.md", "Why?")
	w.syncs(1)
	out := w.must(task, "reply_on_pull_request", fmt.Sprintf(`{"pr":"1","text":"Because it reads better.","in_reply_to":"line-comment-%d"}`, line))
	if out["url"] == "" {
		t.Errorf("answered %v", out)
	}
	if n, _ := w.threadState(line, "Because it reads better."); n != 1 {
		t.Errorf("%d replies on GitHub, want the one at once", n)
	}
}
