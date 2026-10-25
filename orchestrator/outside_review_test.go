package orchestrator_test

// ABS-1 (absmartly/abs#5375, 2026-10-12): a review bot outside dude
// (jervasion-absmartly, asked for by the conductor) requested changes with
// two line comments while the task waited on its pull request. The pull
// request loop handles this; this pins the whole path, so a regression
// shows as one test: the review and its comments are one fix round's
// feedback (each with the id the fixer answers and resolves by, which
// TestAPullRequestFixerIsToldHowToAnswerItsFeedback and
// TestAFixerResolvesOnlyTheThreadsItWasSent cover), the fix is pushed, and
// the reviewer is asked to look again.

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/fakegithub"
)

func TestAnOutsideReviewRequestingChangesIsFixedAnsweredAndAskedAgain(t *testing.T) {
	w := newWorld(t)
	w.withTools()
	wi := w.reviewing()
	const reviewer = "jervasion-absmartly"
	w.gh.RequestReview(1, reviewer)
	w.syncs(1)
	branch := w.gh.Pull(1).Head
	before := w.gh.SHA(branch)

	first := w.gh.LineComment(1, reviewer, "FACTORY.md", "[P2] Preserve the unit-type name.")
	second := w.gh.LineComment(1, reviewer, "FACTORY.md", "[P2] Prevent bin counts from losing observations.")
	w.gh.ReviewSaying(1, reviewer, "CHANGES_REQUESTED", "**CHANGES REQUESTED** - two paths can return wrong distributions.")

	w.until("dude's fix pushed and the task back in review", func() bool {
		w.sync()
		return w.fixes(wi) == 1 && w.gh.SHA(branch) != before && w.taskStatus(wi) == "review"
	})
	batch := w.fixBatch(wi)
	var ids []string
	for _, f := range batch {
		if f.Author != reviewer || f.Source != "review" {
			t.Errorf("batch item %+v, want the reviewer's", f)
		}
		ids = append(ids, f.ID)
	}
	want := []string{fmt.Sprintf("line-comment-%d", first), fmt.Sprintf("line-comment-%d", second)}
	if len(ids) != 3 || !slices.Contains(ids, want[0]) || !slices.Contains(ids, want[1]) || !strings.HasPrefix(ids[2], "review-") {
		t.Fatalf("the fixer's feedback %v, want both line comments and the review", ids)
	}
	reqs := w.reviewRequests()
	if len(reqs) == 0 || !slices.Contains(reqs[len(reqs)-1].Logins, reviewer) {
		t.Errorf("review requests %v, want %s asked again after the fix", reqs, reviewer)
	}
	if p := w.gh.Pull(1); !slices.Contains(p.Requested, reviewer) {
		t.Errorf("requested %v", p.Requested)
	}
}

// GitHub reports a pull request's new head a moment after its branch
// moves. A re-request that reads the old head right after dude's fix is
// not "someone pushed on top": it waits for GitHub, then asks. Before, it
// recorded the head as nobody's to ask about, and the reviewer was never
// asked again (abs#5375's fix of 2026-10-22).
func TestAReviewerIsAskedAgainWhenGitHubReportsTheFixLate(t *testing.T) {
	w := newWorld(t)
	wi := w.reviewing()
	const reviewer = "jervasion-absmartly"
	w.gh.Review(1, reviewer, "CHANGES_REQUESTED")
	w.syncs(1)
	branch := w.gh.Pull(1).Head
	stale := w.gh.SHA(branch)
	w.gh.Set(func(s *fakegithub.Server) {
		s.HeadLag, s.LaggingHead = map[int]int{1: 1 << 30}, map[int]string{1: stale}
	})
	w.gh.Comment(1, "alice", "Please rename the greeting.")
	w.until("the fix on the branch", func() bool { w.sync(); return w.gh.SHA(branch) != stale })
	for range 5 {
		w.pump()
	}
	if n := len(w.rerequests(wi)); n != 0 {
		t.Fatalf("%d re-requests while GitHub still showed the old head", n)
	}
	w.gh.Set(func(s *fakegithub.Server) { s.HeadLag[1] = 0 })
	w.until("the reviewer asked again", func() bool {
		ev := w.rerequests(wi)
		return len(ev) == 1 && slices.Contains(strs(ev[0]["reviewersRequested"]), reviewer)
	})
}

// GitHub that never shows dude's fix is waited for only so long: past
// that, the head it shows is taken as someone else's, and nobody is asked.
func TestARerequestStopsWaitingForAHeadGitHubNeverShows(t *testing.T) {
	w := newWorld(t)
	wi := w.reviewing()
	w.gh.Review(1, "jerv", "CHANGES_REQUESTED")
	w.syncs(1)
	branch := w.gh.Pull(1).Head
	stale := w.gh.SHA(branch)
	w.gh.Set(func(s *fakegithub.Server) {
		s.HeadLag, s.LaggingHead = map[int]int{1: 1 << 30}, map[int]string{1: stale}
	})
	w.gh.Comment(1, "alice", "Please rename the greeting.")
	w.until("the re-request waiting on GitHub", func() bool {
		w.sync()
		return w.count(`SELECT count(*) FROM pull_request_rerequests WHERE task_id = $1 AND outcome ? 'lagSince' AND finished_at IS NULL`, wi) == 1
	})
	mustExec(t, w.owner, `UPDATE pull_request_rerequests SET outcome = jsonb_build_object('lagSince', (now() - interval '1 hour')::text)
		WHERE task_id = $1`, wi)
	w.until("the re-request given up", func() bool {
		return w.count(`SELECT count(*) FROM pull_request_rerequests WHERE task_id = $1 AND finished_at IS NOT NULL`, wi) == 1
	})
	w.until("the task back in review", func() bool { return w.taskStatus(wi) == "review" })
	if n := len(w.rerequests(wi)); n != 0 {
		t.Errorf("%d re-requests of a head GitHub never showed", n)
	}
}
