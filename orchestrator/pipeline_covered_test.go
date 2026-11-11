package orchestrator_test

// A review queued behind a simplifier that committed nothing is dropped
// when each of its reviewers completed at these heads already: it would
// cover no change. The wake says so; next goes where the policy goes (the
// test or the pull request). Reviewers that have not run at these heads
// still get the review. After a fix, where next reviews either way, the
// queued review is kept, so next runs the one the conductor queued.

import (
	"fmt"
	"strings"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/fakelux"
)

// roundOneLens is a reviewer round 1 ran to completion at the delivery's
// heads, and one it did not run.
func (w *world) roundOneLenses(task string) (ran, notRan string) {
	w.t.Helper()
	ran = w.str(`SELECT r.category FROM runs r JOIN workflow_runs wf ON wf.task_id = r.task_id
		WHERE r.task_id = $1 AND r.phase = 'review' AND r.status = 'completed' AND r.review_round = 1
		AND r.base_refs = wf.state->'heads' ORDER BY r.category LIMIT 1`, task)
	for _, c := range []string{"performance", "security", "quality", "tests", "correctness"} {
		if w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'review' AND category = $2`, task, c) == 0 {
			return ran, c
		}
	}
	w.t.Fatal("round 1 ran every reviewer")
	return
}

// simplifiedNothingThen starts the simplifier, which commits nothing, with
// review queued behind it, and waits for the decision after it.
func (w *world) simplifiedNothingThen(task string, review ...string) {
	w.t.Helper()
	simplifies := w.phaseRuns(task, "simplify")
	w.must(task, "start_phase", fmt.Sprintf(`{"phase":"simplify","then":{"review":["%s"]}}`, strings.Join(review, `","`)))
	w.until("after the simplifier", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'simplify' AND status = 'completed'`, task) == simplifies+1 &&
			w.untaken(task)
	})
}

func TestAQueuedReviewOfNoChangeAtReviewedHeadsIsDropped(t *testing.T) {
	w := roundsWorld(t)
	w.fixPlays(func(spec map[string]any) *fakelux.Behaviour {
		if labels, _ := spec["labels"].(map[string]any); labels["dude.phase"] == "simplify" {
			return &fakelux.Behaviour{Reply: "Simple enough."}
		}
		return nil
	})
	task := w.task()
	w.toRoundOne(task)
	ran, _ := w.roundOneLenses(task)
	reviews := w.phaseRuns(task, "review")
	w.simplifiedNothingThen(task, ran)
	if w.decisionAt(task) != delivery.PointFixed || w.count(`SELECT count(*) FROM workflow_runs WHERE task_id = $1
		AND state->'decision' ? 'queued'`, task) != 0 {
		t.Fatalf("parked at %q with a review still queued\n%s", w.decisionAt(task), w.describeRuns())
	}
	fact := delivery.MustText("wakes/facts/pipeline-covered").Render(struct{ Review []string }{[]string{ran}})
	if n := w.count(`SELECT count(*) FROM conductor_wakes WHERE task_id = $1 AND kind = $3 AND strpos(line, $2) > 0`,
		task, fact, delivery.WakeRound); n != 1 {
		t.Errorf("wakes saying %q: %d, want 1", fact, n)
	}
	w.must(task, "decide", `{"action":"next"}`)
	w.until("the policy's next step", func() bool {
		return w.phaseRuns(task, "test") > 0 || w.phaseRuns(task, "review") > reviews || w.decisionAt(task) == delivery.PointBeforePR
	})
	if n := w.phaseRuns(task, "review"); n != reviews {
		t.Errorf("%d review Runs over no change since the last review at these heads\n%s", n-reviews, w.describeRuns())
	}
}

// After a fix that committed nothing the queued review is kept, even when
// its reviewer completed at these heads: there next goes to a review
// either way (the policy's), so it runs exactly the one the conductor
// queued, with nothing else, as the conductor's instructions say.
func TestAQueuedReviewAfterAFixOfNoChangeIsKept(t *testing.T) {
	w := roundsWorld(t)
	w.fixPlays(func(map[string]any) *fakelux.Behaviour { return &fakelux.Behaviour{Reply: "Nothing to change."} })
	task := w.task()
	w.toRoundOne(task)
	ran, _ := w.roundOneLenses(task)
	w.must(task, "start_phase", fmt.Sprintf(`{"phase":"fix","then":{"review":[%q]}}`, ran))
	w.until("after the fix", func() bool { return w.untaken(task) && w.decisionAt(task) == delivery.PointFixed })
	if !w.fixedWith(task, fmt.Sprintf(`[%q]`, ran)) {
		t.Fatalf("parked at %q without the queued review\n%s", w.decisionAt(task), w.describeRuns())
	}
	covered := delivery.MustText("wakes/facts/pipeline-covered").Render(struct{ Review []string }{[]string{ran}})
	if n := w.count(`SELECT count(*) FROM conductor_wakes WHERE task_id = $1 AND strpos(line, $2) > 0`, task, covered); n != 0 {
		t.Errorf("a wake says the review is dropped (%q) while next runs it", covered)
	}
	if fact := stoppedFact("nothing", "done", "", ran); w.count(`SELECT count(*) FROM conductor_wakes WHERE task_id = $1
		AND kind = $3 AND strpos(line, $2) > 0`, task, fact, delivery.WakeRound) != 1 {
		t.Errorf("no wake says %q", fact)
	}
	from := w.cursor(task)
	w.must(task, "decide", `{"action":"next"}`)
	w.until("the queued review", func() bool { return len(w.reviewersSince(task, from)) > 0 })
	if got := w.reviewersSince(task, from); fmt.Sprint(got) != fmt.Sprintf(`[[%q]]`, ran) {
		t.Errorf("review rounds after next: %v, want only the queued [%q]", got, ran)
	}
}

// "Completed at these heads" is a completed review Run of this attempt:
// where the queued reviewer's only Run at these heads failed or was
// stopped, or is of another attempt or phase, the review is still offered.
func TestAQueuedReviewRunsWhereItsReviewerDidNotCompleteHere(t *testing.T) {
	for _, c := range []struct{ name, set string }{
		{"failed", `status = 'failed'`},
		{"stopped", `status = 'aborted'`},
		{"another attempt", `attempt = attempt + 1`},
		{"another phase", `phase = 'fix'`},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := roundsWorld(t)
			w.fixPlays(func(spec map[string]any) *fakelux.Behaviour {
				if labels, _ := spec["labels"].(map[string]any); labels["dude.phase"] == "simplify" {
					return &fakelux.Behaviour{Reply: "Simple enough."}
				}
				return nil
			})
			task := w.task()
			w.toRoundOne(task)
			ran, _ := w.roundOneLenses(task)
			mustExec(t, w.owner, `UPDATE runs SET `+c.set+` WHERE task_id = $1 AND phase = 'review' AND category = $2`, task, ran)
			w.simplifiedNothingThen(task, ran)
			if !w.fixedWith(task, fmt.Sprintf(`[%q]`, ran)) {
				t.Fatalf("parked at %q, want the review offered\n%s", w.decisionAt(task), w.describeRuns())
			}
		})
	}
}

// Only a writer that committed nothing covers nothing new: a simplifier
// that failed keeps the review queued behind it, and its wake says it
// failed, even where the reviewer completed at these heads.
func TestAQueuedReviewBehindAFailedSimplifierIsKept(t *testing.T) {
	w := roundsWorld(t)
	w.fixPlays(func(spec map[string]any) *fakelux.Behaviour {
		if labels, _ := spec["labels"].(map[string]any); labels["dude.phase"] == "simplify" {
			return &fakelux.Behaviour{FailToStart: true}
		}
		return nil
	})
	task := w.task()
	w.toRoundOne(task)
	ran, _ := w.roundOneLenses(task)
	w.must(task, "start_phase", fmt.Sprintf(`{"phase":"simplify","then":{"review":[%q]}}`, ran))
	w.until("after the simplifier", func() bool { return w.untaken(task) && w.decisionAt(task) == delivery.PointFixed })
	if !w.fixedWith(task, fmt.Sprintf(`[%q]`, ran)) {
		t.Fatalf("parked at %q without the queued review\n%s", w.decisionAt(task), w.describeRuns())
	}
	covered := delivery.MustText("wakes/facts/pipeline-covered").Render(struct{ Review []string }{[]string{ran}})
	if n := w.count(`SELECT count(*) FROM conductor_wakes WHERE task_id = $1 AND strpos(line, $2) > 0`, task, covered); n != 0 {
		t.Errorf("a failed simplifier's wake says it committed nothing (%q)", covered)
	}
}

func TestAQueuedReviewStillRunsForReviewersNotRunOrHeadsMoved(t *testing.T) {
	nothing := func(spec map[string]any) *fakelux.Behaviour {
		if labels, _ := spec["labels"].(map[string]any); labels["dude.phase"] == "simplify" {
			return &fakelux.Behaviour{Reply: "Simple enough."}
		}
		return nil
	}
	t.Run("a reviewer the last round did not run", func(t *testing.T) {
		w := roundsWorld(t)
		w.fixPlays(nothing)
		task := w.task()
		w.toRoundOne(task)
		ran, notRan := w.roundOneLenses(task)
		w.simplifiedNothingThen(task, ran, notRan)
		if !w.fixedWith(task, fmt.Sprintf(`[%q, %q]`, ran, notRan)) {
			t.Fatalf("parked at %q, want the review offered\n%s", w.decisionAt(task), w.describeRuns())
		}
		reviews := w.phaseRuns(task, "review")
		w.must(task, "decide", `{"action":"next"}`)
		w.until("the queued review", func() bool { return w.phaseRuns(task, "review") > reviews })
	})
	t.Run("heads moved since the review", func(t *testing.T) {
		w := roundsWorld(t)
		w.fixPlays(nothing)
		task := w.task()
		w.toRoundOne(task)
		ran, _ := w.roundOneLenses(task)
		// A fix commits past round 1's heads; nobody reviews it.
		w.must(task, "start_phase", `{"phase":"fix"}`)
		w.until("after the fix", func() bool { return w.decisionAt(task) == delivery.PointFixed })
		w.simplifiedNothingThen(task, ran)
		if !w.fixedWith(task, fmt.Sprintf(`[%q]`, ran)) {
			t.Fatalf("parked at %q, want the review offered\n%s", w.decisionAt(task), w.describeRuns())
		}
	})
}
