package orchestrator_test

import (
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/fakegithub"
	"github.com/marciomartins/dude/orchestrator/internal/fakelux"
)

// reRounds is a world whose review-only debounce is debounce.
func (w *world) reRounds(debounce time.Duration) {
	w.withDeliveries(&delivery.Store{ReviewDebounce: debounce})
}

// approvedAtH1 starts a review-only task whose lenses find nothing, and
// waits for its APPROVE at the pull request's head; task, number, head.
func (w *world) approvedAtH1() (string, int, string) {
	w.t.Helper()
	w.gh.SetChecks("success")
	wi, n := w.reviewOnly()
	h1 := w.gh.SHA("feature/greet")
	w.agentsDone(wi, "approved at H1", func() bool { return len(w.dudeReviews(wi, n)) == 1 && w.waitingOnPR(wi) })
	if got := w.dudeReviews(wi, n); got[0].State != "APPROVED" || got[0].CommitID != h1 {
		w.t.Fatalf("at H1: %+v", got)
	}
	return wi, n, h1
}

// push is the author's push to the pull request's branch, synced; its head.
func (w *world) push(file string) string {
	w.gh.CommitFiles("feature/greet", "alice's "+file, map[string]string{file: file + "\n"})
	w.sync()
	return w.gh.SHA("feature/greet")
}

func reviewsAt(rs []fakegithub.Review, head string) []fakegithub.Review {
	var out []fakegithub.Review
	for _, r := range rs {
		if r.CommitID == head {
			out = append(out, r)
		}
	}
	return out
}

// G3: dude approved H1; the author pushes H2. The approval at H1 is
// dismissed at once ("Head moved…"); after the debounce round 2 reviews
// H2 and exactly one review is posted there; nothing runs again after.
func TestAnAuthorsPushDismissesTheApprovalAndReReviews(t *testing.T) {
	w := newWorld(t)
	w.reRounds(2 * time.Second)
	wi, n, h1 := w.approvedAtH1()
	h2 := w.push("second.md")
	w.until("the approval at H1 dismissed", func() bool {
		w.advanced()
		return w.events(wi, delivery.EvReviewDismissed) == 1
	})
	if got := w.dudeReviews(wi, n); got[0].State != "DISMISSED" {
		t.Fatalf("H1's approval after the push: %+v", got)
	}
	if got := reviewsAt(w.dudeReviews(wi, n), h2); len(got) != 0 {
		t.Fatalf("reviewed H2 before the debounce: %+v", got)
	}
	if c := w.verdictComments(wi, n); len(c) != 1 || !strings.Contains(c[0].Body, "Waiting 2s for more pushes") {
		t.Fatalf("status comment during the debounce: %+v", c)
	}
	w.agentsDone(wi, "approved at H2", func() bool { return len(reviewsAt(w.dudeReviews(wi, n), h2)) == 1 && w.waitingOnPR(wi) })
	if got := reviewsAt(w.dudeReviews(wi, n), h2); got[0].State != "APPROVED" {
		t.Fatalf("at H2: %+v", got)
	}
	if r := w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'review' AND base_refs->>'target' = $2`, wi, h2); r == 0 {
		t.Fatal("no lens reviewed H2")
	}
	if e := w.events(wi, delivery.EvReviewRoundStarted); e != 1 {
		t.Fatalf("%d rounds started, want 1", e)
	}
	// Replayed: nothing more is posted or dismissed.
	runs := w.count(`SELECT count(*) FROM runs WHERE task_id = $1`, wi)
	w.sync()
	w.advanced()
	time.Sleep(2500 * time.Millisecond)
	w.sync()
	w.advanced()
	if got := w.dudeReviews(wi, n); len(got) != 2 {
		t.Fatalf("%d reviews after replay, want 2: %+v", len(got), got)
	}
	if e := w.events(wi, delivery.EvReviewDismissed); e != 1 {
		t.Fatalf("%d dismissals", e)
	}
	if r := w.count(`SELECT count(*) FROM runs WHERE task_id = $1`, wi); r != runs {
		t.Fatalf("%d Runs made after the round", r-runs)
	}
	if c := w.writers(wi); c != 0 {
		t.Fatalf("%d writers", c)
	}
	_ = h1
}

// Decision 3: GitHub refuses the dismissal (branch protection). Recorded
// once, never retried; the round still runs and its verdict is posted;
// the status comment names the refusal.
func TestARefusedDismissalNeverBlocksTheRound(t *testing.T) {
	w := newWorld(t)
	w.reRounds(time.Second)
	wi, n, h1 := w.approvedAtH1()
	w.gh.Set(func(s *fakegithub.Server) { s.RefuseDismissal = true })
	h2 := w.push("second.md")
	w.agentsDone(wi, "approved at H2", func() bool { return len(reviewsAt(w.dudeReviews(wi, n), h2)) == 1 && w.waitingOnPR(wi) })
	w.sync()
	w.advanced()
	if e := w.events(wi, delivery.EvReviewDismissRefused); e != 1 {
		t.Fatalf("%d refusals recorded, want exactly 1", e)
	}
	if e := w.events(wi, delivery.EvReviewDismissed); e != 0 {
		t.Fatalf("%d dismissals", e)
	}
	if c := w.verdictComments(wi, n); len(c) != 1 || !strings.Contains(c[0].Body, "Could not dismiss the approval at "+h1[:7]) {
		t.Fatalf("status comment %+v", c)
	}
}

// The author reverts to H1 inside the debounce: the round owed at H2 is
// dropped, and the wait never wakes on a due time already past.
func TestARevertInsideTheDebounceLeavesNoStaleWake(t *testing.T) {
	w := newWorld(t)
	w.reRounds(time.Second)
	wi, _, h1 := w.approvedAtH1()
	w.push("second.md")
	w.advanced()
	if out, err := exec.Command("git", "-C", w.gh.Repo, "update-ref", "refs/heads/feature/greet", h1).CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	w.sync()
	w.advanced()
	time.Sleep(1500 * time.Millisecond)
	steps := 0
	for range 8 {
		n, err := w.runtime.Tick(t.Context(), 10)
		if err != nil {
			t.Fatal(err)
		}
		steps += n
		time.Sleep(250 * time.Millisecond)
	}
	var wake *time.Time
	if err := w.owner.QueryRow(t.Context(), `SELECT wake_at FROM workflow_runs WHERE task_id = $1`, wi).Scan(&wake); err != nil {
		t.Fatal(err)
	}
	if steps > 1 || wake != nil && wake.Before(time.Now()) {
		t.Fatalf("%d steps over 2s with nothing new; wake_at %v", steps, wake)
	}
}

// A push to a pull request turned draft dismisses dude's approval at the
// old head at once, the pull request still a draft.
func TestAPushToADraftDismissesTheApproval(t *testing.T) {
	w := newWorld(t)
	w.reRounds(time.Minute)
	wi, n, _ := w.approvedAtH1()
	pull := w.gh.Pull(n)
	w.gh.Set(func(*fakegithub.Server) { pull.Draft = true })
	w.push("second.md")
	w.until("H1's approval dismissed", func() bool { w.advanced(); return w.events(wi, delivery.EvReviewDismissed) == 1 })
	if got := w.dudeReviews(wi, n); got[0].State != "DISMISSED" || !w.gh.Pull(n).Draft {
		t.Fatalf("after the push to the draft: %+v", got)
	}
}

// GitHub fails every dismissal until round 2's lens runs: the verdict at
// H2 dismisses H1's approval after it is posted.
func TestATransientDismissalFailureIsDismissedAfterTheVerdict(t *testing.T) {
	w := newWorld(t)
	w.reRounds(time.Second)
	wi, n, _ := w.approvedAtH1()
	w.gh.Set(func(s *fakegithub.Server) { s.DismissFail5xx = 1000 })
	scripted := w.lux.Decide
	w.lux.Decide = func(spec map[string]any) fakelux.Behaviour {
		if labels, _ := spec["labels"].(map[string]any); labels["dude.phase"] == "review" {
			w.gh.Set(func(s *fakegithub.Server) { s.DismissFail5xx = 0 })
		}
		return scripted(spec)
	}
	h2 := w.push("second.md")
	w.agentsDone(wi, "approved at H2", func() bool { return len(reviewsAt(w.dudeReviews(wi, n), h2)) == 1 && w.waitingOnPR(wi) })
	if got := w.dudeReviews(wi, n); got[0].State != "DISMISSED" || w.events(wi, delivery.EvReviewDismissed) != 1 {
		t.Fatalf("H1's approval after the verdict at H2: %+v", got)
	}
}

// Debounce: two pushes a moment apart are one round, at the second head.
func TestTwoPushesInsideTheDebounceAreOneRound(t *testing.T) {
	w := newWorld(t)
	w.reRounds(3 * time.Second)
	wi, n, _ := w.approvedAtH1()
	h2 := w.push("second.md")
	w.advanced()
	h3 := w.push("third.md")
	w.agentsDone(wi, "reviewed H3", func() bool { return len(reviewsAt(w.dudeReviews(wi, n), h3)) == 1 && w.waitingOnPR(wi) })
	if got := reviewsAt(w.dudeReviews(wi, n), h2); len(got) != 0 {
		t.Fatalf("reviewed H2 inside the debounce: %+v", got)
	}
	if e := w.events(wi, delivery.EvReviewRoundStarted); e != 1 {
		t.Fatalf("%d rounds started, want 1", e)
	}
	if r := w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'review' AND base_refs->>'target' = $2`, wi, h2); r != 0 {
		t.Fatalf("%d lens Runs at H2", r)
	}
}
