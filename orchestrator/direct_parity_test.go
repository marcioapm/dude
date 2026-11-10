package orchestrator_test

import (
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/fakegithub"
	"github.com/marciomartins/dude/orchestrator/internal/fakelux"
)

// A person stops the task while a direct fix's narrow review works, then
// resumes it: the same reviewer goes on over the fix's range, and the
// round ends as it would have — reviewers asked again, back to waiting
// (ready to merge, once the gate holds at the fix).
func TestDirectAStopAndResumeMidNarrowLoopFinishesTheRound(t *testing.T) {
	w := newPRFixWorldDirect(t)
	ana := w.ownedBy(w.wi, "Ana")
	w.askingFor("jerv")
	w.gh.Review(1, "jerv", "COMMENTED")
	w.play = func(phase string, n int, b *fakelux.Behaviour) {
		if phase == "review" && n == 1 {
			*b = fakelux.Behaviour{Hang: true, Reply: "Reviewed the fix after the resume; no problems."}
		}
	}
	w.comment("Please rename the greeting.")
	var review string
	w.until("the narrow review at work", func() bool {
		w.sync()
		review = w.str(`SELECT COALESCE(max(id), '') FROM runs WHERE task_id = $1 AND phase = 'review' AND status = 'running'
			AND lux_state = 'running' AND review_range->>'of' = $2`, w.wi, delivery.RangePullRequestFix)
		return review != ""
	})
	fixed := w.gh.SHA(w.branch)
	w.stop(ana, w.wi, map[string]any{"note": "hold on"})
	w.until("the reviewer kept", func() bool { return w.keptRun(review) })
	if status, body := w.callAs(ana, "/internal/tasks/"+w.wi+"/recover", map[string]any{"action": "resume"}); status != 200 {
		t.Fatalf("resume: %d %v", status, body)
	}
	// Done and waiting: the round's fix reviewed and simplified, every
	// ready gate item holds at the fix's head, so ready to merge.
	w.until("the round done and waiting", func() bool {
		w.sync()
		s := w.taskStatus(w.wi)
		return w.waiting() && s == "ready_to_merge"
	})
	if n := w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'review' AND review_range->>'of' = $2`,
		w.wi, delivery.RangePullRequestFix); n != 1 {
		t.Errorf("%d narrow reviews: the resume made another\n%s", n, w.describeRuns())
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'completed'`, review); n != 1 {
		t.Errorf("the resumed reviewer did not complete")
	}
	if got := w.gh.SHA(w.branch); got != fixed {
		t.Errorf("the pull request moved from the fix %s to %s", fixed, got)
	}
	if events := w.rerequests(w.wi); len(events) != 1 || events[0]["head"] != fixed {
		t.Errorf("re-requests %v, want one at %s", events, fixed)
	}
	if w.state("fixRange") != "" {
		t.Errorf("the fix's range is still in state")
	}
}

// A direct fix across two repositories: each pull request's branch takes
// its repository's push, the narrow review covers both from where each
// stood, and each pull request's reviewers are asked again once.
func TestDirectAFixAcrossTwoRepositoriesIsReviewedOverBothRanges(t *testing.T) {
	w := newPRFixWorldAt(t, prAt{test: true, web: true})
	w.askingFor("jerv")
	w.gh.Review(1, "jerv", "COMMENTED")
	w.web.Review(1, "jerv", "COMMENTED")
	w.play = func(phase string, n int, b *fakelux.Behaviour) {
		if phase == "fix" {
			b.Commit = map[string]string{"FIXED.md": "fixed\n", "web:WEB.md": "fixed\n"}
		}
	}
	w.comment("Please rename the greeting everywhere.")
	w.until("the fix reviewed and waiting", func() bool {
		w.sync()
		return w.gh.SHA(w.branch) != w.before && w.web.SHA(w.branch) != w.webBefore && w.waiting()
	})
	target, web := w.gh.SHA(w.branch), w.web.SHA(w.branch)
	if n := w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'review' AND review_range->>'of' = $2
		AND review_range->'base' = jsonb_build_object('target', $3::text, 'web', $4::text)
		AND base_refs->>'target' = $5 AND base_refs->>'web' = $6`,
		w.wi, delivery.RangePullRequestFix, w.before, w.webBefore, target, web); n != 1 {
		t.Errorf("%d reviews over both repositories' pushes\n%s", n, w.describeRuns())
	}
	byRepo := map[string]string{}
	for _, e := range w.rerequests(w.wi) {
		repo, _ := e["repo"].(string)
		head, _ := e["head"].(string)
		if _, twice := byRepo[repo]; twice {
			t.Errorf("%s's reviewers asked again twice", repo)
		}
		byRepo[repo] = head
	}
	if byRepo["target"] != target || byRepo["web"] != web || len(byRepo) != 2 {
		t.Errorf("re-requests %v, want target at %s and web at %s", byRepo, target, web)
	}
	if w.candidate() != "" || w.web.SHA(delivery.CandidateBranch(w.wi, 1)) != "" {
		t.Errorf("a candidate branch was written")
	}
}

// The pull request's description after a direct fix's narrow loop: its
// Review section shows what the narrow review found (and that it was
// fixed), about the head the pull request shows.
func TestDirectThePullRequestBodyAfterTheRoundShowsItsNarrowReview(t *testing.T) {
	w := newPRFixWorldDirect(t)
	const title = "The fix leaves the greeting untranslated"
	w.play = func(phase string, n int, b *fakelux.Behaviour) {
		if phase == "review" && n == 1 {
			b.Reply = "One problem:\n\n" + reviewFinding(title)
		}
	}
	w.comment("Please rename the greeting.")
	w.until("the loop done and waiting", func() bool {
		w.sync()
		return w.waiting() && w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'review'
			AND review_range->>'of' = $2 AND status = 'completed'`, w.wi, delivery.RangePullRequestFix) == 2
	})
	body := w.gh.Pull(1).Body
	review := body[strings.Index(body, "<!-- dude:review -->")+1:]
	if !strings.Contains(review, title) {
		t.Errorf("the description's Review section lacks the narrow review's finding %q:\n%s", title, body)
	}
}

// GitHub answers the description update after a direct fix's narrow loop
// with a 429: the round's refresh is tried again after a durable wait,
// and the description shows the narrow review.
func TestDirectTheRoundsDescriptionRefreshIsTriedAgain(t *testing.T) {
	w := newPRFixWorldDirect(t)
	const title = "The fix leaves the greeting untranslated"
	var armed atomic.Bool
	var patches atomic.Int32
	w.gh.Set(func(s *fakegithub.Server) {
		s.Intercept = func(r *http.Request) int {
			if !armed.Load() || r.Method != "PATCH" || !bodyPatch.MatchString(r.URL.Path) {
				return 0
			}
			if patches.Add(1) == 1 {
				return http.StatusTooManyRequests
			}
			return 0
		}
	})
	w.play = func(phase string, n int, b *fakelux.Behaviour) {
		switch {
		case phase == "review" && n == 1:
			b.Reply = "One problem:\n\n" + reviewFinding(title)
		case phase == "review" && n == 2:
			// Only the round's own refresh, once its loop is done.
			armed.Store(true)
		}
	}
	w.comment("Please rename the greeting.")
	w.until("the narrow loop's second review", func() bool {
		w.sync()
		return w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'review' AND review_range->>'of' = $2
			AND status = 'completed'`, w.wi, delivery.RangePullRequestFix) == 2
	})
	w.elapseRefreshWait(w.wi, 1)
	w.settledBackOnThePullRequest()
	body := w.gh.Pull(1).Body
	if i := strings.Index(body, "<!-- dude:review -->"); i < 0 || !strings.Contains(body[i:], title) {
		t.Errorf("the description's Review section lacks the narrow review's finding:\n%s", body)
	}
	if n := patches.Load(); n != 2 {
		t.Errorf("%d description updates, want the refused one and its retry", n)
	}
}
