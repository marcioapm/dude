package orchestrator_test

import (
	"fmt"
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

// draft turns pull request n into a draft, or marks it ready for review.
func (w *world) draft(n int, on bool) {
	pull := w.gh.Pull(n)
	w.gh.Set(func(*fakegithub.Server) { pull.Draft = on })
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

// G3: dude approved H1; the author pushes H2, then H3 inside the
// debounce. The approval at H1 is dismissed at once ("Head moved…"); H3's
// push resets the timer, and after it one round reviews H3 (none H2), its
// lens going on from H1's (resumeRounds), and exactly one review is
// posted there; nothing runs again after.
func TestAnAuthorsPushDismissesTheApprovalAndReReviews(t *testing.T) {
	w := newWorld(t)
	w.reRounds(2 * time.Second)
	mustExec(t, w.owner, `UPDATE projects SET delivery_policy = delivery_policy || '{"resumeRounds":true}' WHERE id = $1`, w.project)
	wi, n, _ := w.approvedAtH1()
	h2 := w.push("second.md")
	w.until("the approval at H1 dismissed", func() bool {
		w.advanced()
		return w.events(wi, delivery.EvReviewDismissed) == 1
	})
	if got := w.dudeReviews(wi, n); got[0].State != "DISMISSED" {
		t.Fatalf("H1's approval after the push: %+v", got)
	}
	h3 := w.push("third.md")
	w.advanced()
	if got := reviewsAt(w.dudeReviews(wi, n), h3); len(got) != 0 {
		t.Fatalf("reviewed H3 before the debounce: %+v", got)
	}
	if c := w.verdictComments(wi, n); len(c) != 1 || !strings.Contains(c[0].Body, "Waiting 2s for more pushes before re-reviewing "+h3[:7]) {
		t.Fatalf("status comment during the debounce: %+v", c)
	}
	w.agentsDone(wi, "approved at H3", func() bool { return len(reviewsAt(w.dudeReviews(wi, n), h3)) == 1 && w.waitingOnPR(wi) })
	if got := reviewsAt(w.dudeReviews(wi, n), h3); got[0].State != "APPROVED" {
		t.Fatalf("at H3: %+v", got)
	}
	if r := w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'review' AND base_refs->>'target' = $2
		AND continues_run_id IS NOT NULL`, wi, h3); r == 0 {
		t.Fatal("no lens went on from its round at H1")
	}
	if r := w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'review' AND base_refs->>'target' = $2`, wi, h2); r != 0 {
		t.Fatalf("%d lens Runs at H2", r)
	}
	if e := w.events(wi, delivery.EvReviewRoundStarted); e != 1 {
		t.Fatalf("%d rounds started, want 1", e)
	}
	// H3's push reset the timer: its round is due later than H2's.
	if later := w.count(`SELECT count(*) FROM events a, events b WHERE a.task_id = $1 AND b.task_id = $1 AND a.event_type = $2
		AND b.event_type = $2 AND a.payload->>'head' = $3 AND b.payload->>'head' = $4
		AND (b.payload->>'dueAt')::timestamptz > (a.payload->>'dueAt')::timestamptz`, wi, delivery.EvReviewRoundDue, h2, h3); later != 1 {
		t.Fatal("the push of H3 did not reset the debounce")
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
}

// Decision 3: GitHub refuses the dismissal (branch protection). Recorded
// once, never retried; the round still runs and its verdict is posted;
// the status comment names the refusal. Failing transiently (5xx) until
// round 2's lens runs, H1's approval is dismissed after H2's verdict.
func TestADismissalGitHubFailsNeverBlocksTheRound(t *testing.T) {
	for _, refuse := range []bool{true, false} {
		t.Run(fmt.Sprint("refused=", refuse), func(t *testing.T) {
			w := newWorld(t)
			w.reRounds(time.Second)
			wi, n, h1 := w.approvedAtH1()
			w.gh.Set(func(s *fakegithub.Server) { s.RefuseDismissal, s.DismissFail5xx = refuse, 1000 })
			scripted := w.lux.Decide
			w.lux.Decide = func(spec map[string]any) fakelux.Behaviour {
				w.gh.Set(func(s *fakegithub.Server) { s.DismissFail5xx = 0 })
				return scripted(spec)
			}
			h2 := w.push("second.md")
			w.agentsDone(wi, "approved at H2", func() bool { return len(reviewsAt(w.dudeReviews(wi, n), h2)) == 1 && w.waitingOnPR(wi) })
			w.sync()
			w.advanced()
			refused, dismissed := w.events(wi, delivery.EvReviewDismissRefused), w.events(wi, delivery.EvReviewDismissed)
			if !refuse {
				if got := w.dudeReviews(wi, n); got[0].State != "DISMISSED" || dismissed != 1 || refused != 0 {
					t.Fatalf("H1's approval after the verdict at H2: %+v", got)
				}
				return
			}
			if refused != 1 || dismissed != 0 {
				t.Fatalf("%d refusals recorded, want exactly 1; %d dismissals", refused, dismissed)
			}
			if c := w.verdictComments(wi, n); len(c) != 1 || !strings.Contains(c[0].Body, "Could not dismiss the approval at "+h1[:7]) {
				t.Fatalf("status comment %+v", c)
			}
		})
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
		n, _ := w.runtime.Tick(t.Context(), 10)
		steps += n
		time.Sleep(250 * time.Millisecond)
	}
	if stale := w.count(`SELECT count(*) FROM workflow_runs WHERE task_id = $1 AND wake_at < now()`, wi); steps > 1 || stale != 0 {
		t.Fatalf("%d steps over 2s with nothing new; %d wake_at in the past", steps, stale)
	}
}

// A push to a pull request turned draft dismisses dude's approval at the
// old head at once; the round at the draft comments, once however often
// the wait wakes; marked ready at that head, it is approved, once. A task
// started on a draft is approved alike once it is marked ready.
func TestADraftIsApprovedOnceMarkedReady(t *testing.T) {
	for _, first := range []bool{false, true} {
		t.Run(fmt.Sprint("first=", first), func(t *testing.T) {
			w := newWorld(t)
			w.reRounds(time.Second)
			var wi, h string
			var n int
			if first {
				w.gh.SetChecks("success")
				wi, n = w.reviewOnly()
				w.draft(n, true) // before its first round's lens runs
				h = w.gh.SHA("feature/greet")
			} else {
				wi, n, _ = w.approvedAtH1()
				w.draft(n, true)
				h = w.push("second.md")
				w.until("H1's approval dismissed", func() bool { w.advanced(); return w.events(wi, delivery.EvReviewDismissed) == 1 })
				if got := w.dudeReviews(wi, n); got[0].State != "DISMISSED" || !w.gh.Pull(n).Draft {
					t.Fatalf("after the push to the draft: %+v", got)
				}
			}
			w.agentsDone(wi, "commented at the draft", func() bool { return len(reviewsAt(w.dudeReviews(wi, n), h)) == 1 && w.waitingOnPR(wi) })
			status := w.verdictComments(wi, n)
			for range 3 { // the draft's wait woken again: nothing more posted or edited
				w.sync()
				w.advanced()
				time.Sleep(300 * time.Millisecond)
			}
			if got := reviewsAt(w.dudeReviews(wi, n), h); len(got) != 1 || got[0].State != "COMMENTED" {
				t.Fatalf("at the draft: %+v", got)
			}
			if c := w.verdictComments(wi, n); len(c) != 1 || c[0].Body != status[0].Body {
				t.Fatalf("status comment while a draft: %+v, was %+v", c, status)
			}
			w.draft(n, false)
			w.agentsDone(wi, "approved once ready", func() bool { return len(reviewsAt(w.dudeReviews(wi, n), h)) == 2 && w.waitingOnPR(wi) })
			w.sync()
			w.advanced()
			if got := reviewsAt(w.dudeReviews(wi, n), h); len(got) != 2 || got[1].State != "APPROVED" {
				t.Fatalf("after ready: %+v", got)
			}
		})
	}
}
