package orchestrator_test

import (
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/agenttools"
	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/fakegithub"
)

// #485: a fixer's "fixed" replies and thread resolutions reach GitHub only
// once its fix is on the pull request.

// heldFixer starts a fixer whose feedback has it reply "Fixed in …" and
// resolve the thread through the tool server, with lux holding its push:
// its work is not on the pull request. It returns the task, the line
// comment, and the fixer's Run once both calls were made.
func heldFixer(t *testing.T) (*world, string, int64, string) {
	w := newWorld(t)
	w.withTools()
	task := w.reviewing()
	w.lux.HoldPushes = true
	own := w.gh.LineComment(1, "ana", "FACTORY.md", "Please explain.")
	w.gh.EditComment(1, own, fmt.Sprintf("Please explain.\n"+
		"tool: reply_on_pull_request {\"pr\":\"1\",\"text\":\"Fixed in 579e9d9: it explains now.\",\"in_reply_to\":\"line-comment-%d\"}\n"+
		"tool: resolve_thread {\"pr\":\"1\",\"thread\":\"line-comment-%d\"}\nThanks.", own, own))
	var fixer string
	w.until("the fixer to make both calls with its push held", func() bool {
		w.sync()
		fixer = w.str(`SELECT COALESCE((SELECT id FROM runs WHERE task_id = $1 AND phase = 'fix' AND pr_feedback <> '[]'::jsonb
			AND push_request_id IS NOT NULL ORDER BY created_at DESC LIMIT 1), '')`, task)
		return fixer != "" && w.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = $2
			AND payload->>'tool' IN ('reply_on_pull_request', 'resolve_thread')`, fixer, agenttools.EventType) == 2
	})
	return w, task, own, fixer
}

// fixerReplies are the fake GitHub's replies in the thread of line comment
// own, and whether its thread is resolved.
func (w *world) fixerReplies(own int64) (int, bool) {
	n, resolved := 0, false
	p := w.gh.Pull(1)
	w.gh.Set(func(s *fakegithub.Server) {
		for _, c := range p.LineComments {
			if c.InReplyTo == own && strings.HasPrefix(c.Body, "Fixed in 579e9d9") {
				n++
			}
		}
		resolved = s.ResolvedThreads[s.ThreadID(own)]
	})
	return n, resolved
}

// A fixer replies "Fixed in …" and resolves its thread while its push is
// held: nothing reaches GitHub, and both calls answer queued. Once the
// push lands on the pull request, the reply and the resolution are
// posted, once each and in the order they were made, and stay so through
// later syncs.
func TestAFixersRepliesWaitForItsPush(t *testing.T) {
	w, task, own, fixer := heldFixer(t)
	w.syncs(3)
	if n, resolved := w.fixerReplies(own); n != 0 || resolved {
		t.Fatalf("before the push: %d replies posted, resolved %v", n, resolved)
	}
	if n := w.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = $2
		AND payload->'result'->>'queued' = $3`, fixer, agenttools.EventType, delivery.HeldQueued); n != 2 {
		t.Errorf("%d calls answered queued, want 2", n)
	}

	// Bound even a broken sweep that keeps claiming the same reply: let
	// its first post through, then stop duplicate attempts with a transient
	// error. Otherwise PostHeld can loop inside pump, beyond until's deadline.
	var attempts atomic.Int32
	w.gh.Set(func(s *fakegithub.Server) {
		s.Intercept = func(r *http.Request) int {
			if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, fmt.Sprintf("/comments/%d/replies", own)) {
				if attempts.Add(1) > 1 {
					return http.StatusBadGateway
				}
			}
			return 0
		}
	})
	w.lux.HoldPushes = false
	w.lux.ReleasePushes()
	w.until("the first reply posted", func() bool {
		n, _ := w.fixerReplies(own)
		return n >= 1
	})
	w.syncs(3)
	w.until("the fixer's work on the pull request", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'completed'`, fixer) == 1
	})
	if n, resolved := w.fixerReplies(own); n != 1 || !resolved {
		t.Errorf("%d replies posted, resolved %v; want one reply and a resolution", n, resolved)
	}
	if n := attempts.Load(); n != 1 {
		t.Errorf("%d reply post attempts, want exactly one through later syncs", n)
	}
	if got := w.ids(`SELECT CASE WHEN event_type = 'chat.message' THEN 'reply' ELSE payload->>'action' END FROM events
		WHERE run_id = $1 AND (event_type = 'chat.message' AND payload->>'by' = 'fixer' OR event_type = 'pull_request.action')
		ORDER BY cursor`, fixer); strings.Join(got, ",") != "reply,resolve_thread" {
		t.Errorf("recorded %v, want the reply then the resolution", got)
	}
	if n := w.count(`SELECT count(*) FROM pr_held_actions WHERE run_id = $1 AND posted_at IS NOT NULL`, fixer); n != 2 {
		t.Errorf("%d held calls posted, want 2", n)
	}
	if n := w.count(`SELECT count(*) FROM conductor_wakes WHERE task_id = $1 AND kind = $2`, task, delivery.WakeHeldDropped); n != 0 {
		t.Errorf("the conductor was told of %d drops", n)
	}
}

// A fixer whose batch is abandoned before its push lands posts nothing,
// ever; its conductor is told once.
func TestAnAbandonedFixersRepliesAreDropped(t *testing.T) {
	w, task, own, fixer := heldFixer(t)
	mustExec(t, w.owner, `UPDATE runs SET control = 'abort' WHERE id = $1`, fixer)
	w.until("the fixer aborted", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status IN ('aborted', 'failed')`, fixer) == 1
	})
	w.lux.HoldPushes = false
	w.lux.ReleasePushes()
	w.syncs(5)
	if n, resolved := w.fixerReplies(own); n != 0 || resolved {
		t.Errorf("after the abort: %d replies posted, resolved %v", n, resolved)
	}
	if n := w.count(`SELECT count(*) FROM pr_held_actions WHERE run_id = $1 AND dropped_at IS NOT NULL AND posted_at IS NULL`, fixer); n != 2 {
		t.Errorf("%d held calls dropped, want 2", n)
	}
	if n := w.count(`SELECT count(*) FROM conductor_wakes WHERE task_id = $1 AND kind = $2`, task, delivery.WakeHeldDropped); n != 1 {
		t.Errorf("the conductor was told %d times, want once", n)
	}
}

// The conductor's own reply, outside any fix batch, is posted at once.
func TestTheConductorsReplyIsNotHeld(t *testing.T) {
	w := conducting(t)
	task := w.reviewing()
	if status, out := w.chat(task, "Look after the pull request for me."); status >= 300 {
		t.Fatalf("chat: %d %v", status, out)
	}
	line := w.gh.LineComment(1, "ana", "FACTORY.md", "Why?")
	w.syncs(1)
	out := w.must(task, "reply_on_pull_request", fmt.Sprintf(`{"pr":"1","text":"Because it reads better.","in_reply_to":"line-comment-%d"}`, line))
	if out["queued"] != nil || out["url"] == "" {
		t.Errorf("answered %v", out)
	}
	posted := 0
	p := w.gh.Pull(1)
	w.gh.Set(func(s *fakegithub.Server) {
		for _, c := range p.LineComments {
			if c.InReplyTo == line && strings.HasPrefix(c.Body, "Because it reads better.") {
				posted++
			}
		}
	})
	if posted != 1 {
		t.Errorf("%d replies on GitHub, want the one at once", posted)
	}
	if n := w.count(`SELECT count(*) FROM pr_held_actions WHERE task_id = $1`, task); n != 0 {
		t.Errorf("%d held", n)
	}
}
