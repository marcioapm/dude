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

// #485: a pull request fixer's "fixed" reply and thread resolution must
// not reach GitHub while its fix is unpublished. The fixer is refused and
// reports its intended answers; the conductor posts and resolves after it
// verifies the published fix.

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
	w.lux.HoldPushes = holdPushes
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

// fixerToldRefused says the fixer's agent was answered 422 for both its
// reply and its resolution (the scripted agent says so in its message).
func (w *world) fixerToldRefused(fixer string) bool {
	return w.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = 'agent.message'
		AND strpos(payload->>'text', 'dude reply_on_pull_request answered 422') > 0
		AND strpos(payload->>'text', 'dude resolve_thread answered 422') > 0`, fixer) == 1
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

// A fixer replies and resolves its thread on its first turn, before its
// checks are backed: both are refused saying its fix is not on the pull
// request, and nothing reaches GitHub, however many syncs pass. Sent back,
// it backs its checks and its fix is published; still nothing was posted
// for it. The conductor then answers and resolves, and that is what GitHub
// shows, once.
func TestAFixersRepliesAreRefusedUntilTheConductorVerifiesPublication(t *testing.T) {
	w, task, own, fixer := earlyFixer(t, false)
	w.syncs(3)
	if n, resolved := w.threadState(own, fixerReplyText); n != 0 || resolved {
		t.Fatalf("before publication: %d replies posted, resolved %v", n, resolved)
	}
	if !w.fixerToldRefused(fixer) {
		t.Error("the fixer was not refused both its reply and its resolution")
	}
	w.until("the fixer's fix published", func() bool {
		w.sync()
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'completed'`, fixer) == 1
	})
	if got := w.ids(`SELECT CASE WHEN (payload->>'verified')::bool THEN 'verified' ELSE 'unverified' END FROM events
		WHERE run_id = $1 AND event_type = $2 ORDER BY cursor`, fixer, delivery.EvRunChecksJudged); strings.Join(got, ",") != "unverified,verified" {
		t.Errorf("the fixer judged %v, want sent back once, then verified", got)
	}
	if n := w.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = $2`, fixer, delivery.EvGitCommitCreated); n == 0 {
		t.Fatal("the fixer's fix was not published")
	}
	w.syncs(3)
	if n, resolved := w.threadState(own, fixerReplyText); n != 0 || resolved {
		t.Fatalf("after publication: %d of the fixer's replies posted, resolved %v; nothing is posted later for it", n, resolved)
	}
	// The conductor verified the published fix: it answers and resolves.
	const answer = "Published: the explanation is in the pull request's head."
	if out := w.must(task, "reply_on_pull_request", fmt.Sprintf(`{"pr":"1","text":%q,"in_reply_to":"line-comment-%d"}`, answer, own)); out["url"] == "" {
		t.Errorf("answered %v", out)
	}
	w.must(task, "resolve_thread", fmt.Sprintf(`{"pr":"1","thread":"line-comment-%d"}`, own))
	if n, resolved := w.threadState(own, answer); n != 1 || !resolved {
		t.Errorf("%d of the conductor's replies posted, resolved %v; want one and resolved", n, resolved)
	}
	if n, _ := w.threadState(own, fixerReplyText); n != 0 {
		t.Errorf("%d of the fixer's replies posted", n)
	}
}

// A fixer aborted before its fix is published (lux holding its push) has
// nothing waiting to go out: after the abort, the held push released and
// more syncs, GitHub has no reply and no resolution of its.
func TestAnAbandonedFixerHasNoDelayedReplies(t *testing.T) {
	w, _, own, fixer := earlyFixer(t, true)
	if !w.fixerToldRefused(fixer) {
		t.Error("the fixer was not refused both its reply and its resolution")
	}
	mustExec(t, w.owner, `UPDATE runs SET control = 'abort' WHERE id = $1`, fixer)
	w.until("the fixer aborted", func() bool {
		w.sync()
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status IN ('aborted', 'failed')`, fixer) == 1
	})
	w.lux.HoldPushes = false
	w.lux.ReleasePushes()
	w.syncs(5)
	if n := w.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = $2`, fixer, delivery.EvGitCommitCreated); n != 0 {
		t.Errorf("the aborted fixer published %d times", n)
	}
	if n, resolved := w.threadState(own, fixerReplyText); n != 0 || resolved {
		t.Errorf("after the abort: %d replies posted, resolved %v", n, resolved)
	}
}

// The conductor's own reply, while no fixer is involved, is posted at once.
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
