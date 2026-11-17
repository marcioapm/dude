package orchestrator_test

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/fakegithub"
)

// A review-only verdict's CI note follows CI at the reviewed head: a
// review posted listing failing checks has its body edited in place once
// they pass there (same review, same state, one status comment edited), no
// new round. A review that noted nothing of CI is never touched by a
// checks signal, nor is a writing delivery's pull request.

// reviewEdits are the PUTs of a review's body the fake GitHub was sent.
func (w *world) reviewEdits() []string {
	var out []string
	edit := regexp.MustCompile(`^PUT .*/pulls/\d+/reviews/\d+$`)
	for _, r := range w.gh.Requests() {
		if edit.MatchString(r) {
			out = append(out, r)
		}
	}
	return out
}

// greenAtTheHead turns CI green at the head and has the sync's checks
// signal reach the task's wait.
func (w *world) greenAtTheHead(wi string) {
	w.t.Helper()
	before := w.count(`SELECT count(*) FROM workflow_signals WHERE workflow_run_id = (SELECT id FROM workflow_runs WHERE task_id = $1) AND consumed_at IS NOT NULL`, wi)
	w.gh.SetChecks("success")
	w.agentsDone(wi, "the checks signal consumed by the wait", func() bool {
		return w.count(`SELECT count(*) FROM workflow_signals WHERE workflow_run_id = (SELECT id FROM workflow_runs WHERE task_id = $1) AND consumed_at IS NOT NULL`, wi) > before &&
			w.waitingOnPR(wi)
	})
	w.syncs(3)
	w.advanced()
}

// s5u: REQUEST_CHANGES on an open medium finding while CI fails at H for
// an unrelated reason, its body listing the failing checks. CI then turns
// green at H: the same review's body no longer lists them, still
// CHANGES_REQUESTED, still the one review at H; the status comment is
// edited; no new round; re-syncs change nothing more.
func TestAReviewListingFailingChecksIsEditedOnceCIPassesAtTheHead(t *testing.T) {
	w := newWorld(t)
	wi, n, h1 := w.findingThenVerdictWith("failure", func() string { return "still" })
	rs := reviewsAt(w.dudeReviews(wi, n), h1)
	if len(rs) != 1 || rs[0].State != "CHANGES_REQUESTED" || !strings.Contains(rs[0].Body, "failing: ") {
		t.Fatalf("at H1 with red CI: %+v", rs)
	}
	id := rs[0].ID
	// The body a submit with CI green would have made: the original, the
	// CI note's line alone gone.
	ciNote := regexp.MustCompile(`\n- failing: [^\n]*`)
	if m := ciNote.FindAllString(rs[0].Body, -1); len(m) != 1 {
		t.Fatalf("the CI note's lines in %q: %q", rs[0].Body, m)
	}
	want := ciNote.ReplaceAllString(rs[0].Body, "")
	inline := len(w.dudeLineComments(n))
	comments := w.verdictComments(wi, n)
	if len(comments) != 1 {
		t.Fatalf("the status comments with red CI: %+v", comments)
	}
	if !strings.Contains(comments[0].Body, "failing: ") {
		t.Errorf("the status comment does not name the failing checks: %q", comments[0].Body)
	}
	statusBefore := comments[0].Body
	lenses := w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'review'`, wi)
	claims := w.events(wi, delivery.EvReviewClaimed)

	w.greenAtTheHead(wi)
	rs = reviewsAt(w.dudeReviews(wi, n), h1)
	if len(rs) != 1 || rs[0].ID != id {
		t.Fatalf("reviews at H1 after CI passed: %+v, want the one review %d", rs, id)
	}
	if rs[0].Body != want {
		t.Fatalf("the edited review's body:\n%s\nwant the original without its CI note:\n%s", rs[0].Body, want)
	}
	if c := len(w.dudeLineComments(n)); c != inline {
		t.Fatalf("%d inline comments after the edit, were %d", c, inline)
	}
	if rs[0].State != "CHANGES_REQUESTED" || !strings.Contains(rs[0].Body, "Changes requested at "+h1[:7]) {
		t.Fatalf("the edited review: %+v", rs[0])
	}
	if !strings.Contains(rs[0].Body, "<!-- dude:review task="+wi+" claim=") {
		t.Fatalf("the edited review lost its marker: %q", rs[0].Body)
	}
	if c := w.verdictComments(wi, n); len(c) != 1 || c[0].Body == statusBefore || strings.Contains(c[0].Body, "failing: ") {
		t.Fatalf("the status comment after CI passed: %+v (was %q)", c, statusBefore)
	}
	if e := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = $2 AND payload->>'reviewId' = $3
		AND payload->>'head' = $4 AND payload->>'why' = 'checks: passing' AND payload ? 'claimId'`,
		wi, delivery.EvReviewEdited, fmt.Sprint(id), h1); e != 1 {
		t.Fatalf("%d review_edited events, want 1", e)
	}
	if l := w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'review'`, wi); l != lenses {
		t.Fatalf("%d lens Runs started: a new round", l-lenses)
	}
	if c := w.events(wi, delivery.EvReviewClaimed); c != claims {
		t.Fatalf("%d new claims", c-claims)
	}
	edits := len(w.reviewEdits())
	if edits != 1 {
		t.Fatalf("%d review edits sent, want 1", edits)
	}
	// Re-synced, and CI's signal again: nothing more.
	w.syncs(4)
	w.advanced()
	w.recheck(wi, "recheck:after-green")
	if got := reviewsAt(w.dudeReviews(wi, n), h1); len(got) != 1 || got[0].Body != rs[0].Body {
		t.Fatalf("a re-sync changed the review: %+v", got)
	}
	if e := len(w.reviewEdits()); e != edits {
		t.Fatalf("%d more review edits", e-edits)
	}
	if e := w.events(wi, delivery.EvReviewEdited); e != 1 {
		t.Fatalf("%d review_edited events after re-syncs", e)
	}
}

// A verdict posted with CI green at H noted nothing of CI: checks
// signals at H (red, then green again) edit nothing, post nothing.
func TestAReviewThatNotedNoCIIsNotRefreshedByChecks(t *testing.T) {
	w := newWorld(t)
	wi, n, h1 := w.findingThenVerdict(func() string { return "still" })
	before := reviewsAt(w.dudeReviews(wi, n), h1)
	if len(before) != 1 || strings.Contains(before[0].Body, "failing: ") {
		t.Fatalf("at H1 with green CI: %+v", before)
	}
	claims := w.events(wi, delivery.EvReviewClaimed)
	w.gh.SetChecks("failure")
	w.syncs(3)
	w.advanced()
	w.greenAtTheHead(wi)
	after := reviewsAt(w.dudeReviews(wi, n), h1)
	if len(after) != 1 || after[0].Body != before[0].Body || after[0].State != before[0].State {
		t.Fatalf("after checks signals: %+v, want %+v", after, before)
	}
	if e := w.reviewEdits(); len(e) != 0 {
		t.Fatalf("review edits sent: %v", e)
	}
	if e := w.events(wi, delivery.EvReviewEdited); e != 0 {
		t.Fatalf("%d review_edited events", e)
	}
	if c := w.events(wi, delivery.EvReviewClaimed); c != claims {
		t.Fatalf("%d new claims", c-claims)
	}
}

// 6b: the author-question COMMENT posted while CI fails at H lists the
// failing checks; CI green at H refreshes its body: still a COMMENT, the
// question still listed.
func TestAnAuthorQuestionCommentIsRefreshedOnceCIPasses(t *testing.T) {
	w := newWorld(t)
	w.withTools()
	wi, n, h1 := w.approvedAtH1()
	w.gh.SetChecks("failure")
	w.syncs(3)
	w.advanced()
	if got := reviewsAt(w.dudeReviews(wi, n), h1); len(got) != 1 || len(w.reviewEdits()) != 0 {
		t.Fatalf("red CI after an approval that noted none: %+v", got)
	}
	w.must(wi, "ask_author", fmt.Sprintf(`{"pr":"%d","question":"Should greet also handle names?"}`, n))
	w.agentsDone(wi, "a COMMENT at H1", func() bool { return len(reviewsAt(w.dudeReviews(wi, n), h1)) == 2 && w.waitingOnPR(wi) })
	rs := reviewsAt(w.dudeReviews(wi, n), h1)
	if rs[1].State != "COMMENTED" || !strings.Contains(rs[1].Body, "Should greet also handle names?") || !strings.Contains(rs[1].Body, "failing: ") {
		t.Fatalf("the question's COMMENT with red CI: %+v", rs[1])
	}
	id := rs[1].ID
	w.greenAtTheHead(wi)
	rs = reviewsAt(w.dudeReviews(wi, n), h1)
	if len(rs) != 2 || rs[1].ID != id {
		t.Fatalf("reviews at H1 after CI passed: %+v", rs)
	}
	if rs[1].State != "COMMENTED" || strings.Contains(rs[1].Body, "failing: ") || !strings.Contains(rs[1].Body, "Should greet also handle names?") {
		t.Fatalf("the refreshed COMMENT: %+v", rs[1])
	}
	if e := w.events(wi, delivery.EvReviewEdited); e != 1 {
		t.Fatalf("%d review_edited events", e)
	}
}

// A review whose inline comment GitHub refused, so that its submit folded
// it into the body, keeps it folded there once refreshed: the edit drops
// the CI note alone, never a folded comment.
func TestARefreshedReviewKeepsTheCommentsItsSubmitFolded(t *testing.T) {
	w := newWorld(t)
	w.gh.Set(func(s *fakegithub.Server) { s.NotInDiffOnce = true })
	wi, n, h1 := w.findingRequested("failure", func() string { return "still" })
	if c := w.dudeLineComments(n); len(c) != 0 {
		t.Fatalf("inline comments after GitHub refused them: %+v", c)
	}
	rs := reviewsAt(w.dudeReviews(wi, n), h1)
	if len(rs) != 1 || !strings.Contains(rs[0].Body, "**Not on a changed line**") || !strings.Contains(rs[0].Body, "\n- failing: ") {
		t.Fatalf("at H1, folded with red CI: %+v", rs)
	}
	// FoldComments trims the body it folds into: blank lines compared as one.
	blank := regexp.MustCompile(`\n{3,}`)
	want := blank.ReplaceAllString(regexp.MustCompile(`\n- failing: [^\n]*`).ReplaceAllString(rs[0].Body, ""), "\n\n")
	w.greenAtTheHead(wi)
	rs = reviewsAt(w.dudeReviews(wi, n), h1)
	if len(rs) != 1 || blank.ReplaceAllString(rs[0].Body, "\n\n") != want {
		t.Fatalf("the refreshed review:\n%q\nwant the body:\n%q", rs, want)
	}
	if e := len(w.reviewEdits()); e != 1 {
		t.Fatalf("%d review edits, want 1", e)
	}
}

// The writing delivery is untouched: its pull request's checks going red
// at its wait wake a fixer at PointPRFeedback, as they always did, and
// going green after are taken by the writing wait; no review-only step
// ever runs, no review-only status comment, no review edit.
func TestAWritingDeliverysGreenChecksEditNoReview(t *testing.T) {
	w := newWorld(t)
	var mu sync.Mutex
	var steps []string
	w.gated(func(step, _ string) {
		mu.Lock()
		defer mu.Unlock()
		steps = append(steps, step)
	}).Store(true)
	wi := w.reviewing()
	n := w.gh.Pulls()[0].Number
	consumed := func() int {
		return w.count(`SELECT count(*) FROM workflow_signals s JOIN workflow_runs r ON r.id = s.workflow_run_id
			WHERE r.task_id = $1 AND s.name = $2 AND s.consumed_at IS NOT NULL`, wi, delivery.SignalPRFeedback)
	}
	w.gh.SetChecks("failure")
	w.until("red checks wake a fixer", func() bool { w.sync(); w.pump(); return w.fixes(wi) == 1 })
	if consumed() == 0 {
		t.Fatal("the writing wait did not consume the checks signal")
	}
	w.gh.SetChecks("success")
	w.syncs(4)
	w.advanced()
	if c := w.verdictComments(wi, n); len(c) != 0 {
		t.Fatalf("a review-only status comment on a writing delivery: %+v", c)
	}
	mu.Lock()
	ran := slices.Clone(steps)
	mu.Unlock()
	if slices.Contains(ran, "verdict") {
		t.Fatalf("a writing delivery ran the review-only verdict step: %v", ran)
	}
	if !slices.Contains(ran, "awaitPullRequest") {
		t.Fatalf("the writing wait never ran: %v", ran)
	}
	if e := w.reviewEdits(); len(e) != 0 {
		t.Fatalf("review edits sent for a writing delivery: %v", e)
	}
	if e := w.events(wi, delivery.EvReviewEdited); e != 0 {
		t.Fatalf("%d review_edited events on a writing delivery", e)
	}
	if f := w.fixes(wi); f != 1 {
		t.Fatalf("%d fixes after CI went green, want the one red woke", f)
	}
}
