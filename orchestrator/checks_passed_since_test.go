package orchestrator_test

// At a pull request feedback decision whose failing checks have since
// passed at the same head, nothing is left to fix: what the pull request
// wait allows may start (the review its ready gate names), and next starts
// no empty fix. With a check still failing, the decision is as before.

import (
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
)

// checksFailedThenRerun is a conducted delivery parked at pull request
// feedback for a failing check; when green, the check reran passing at the
// same head, recorded before the conductor acts.
func checksFailedThenRerun(t *testing.T, green bool) (*world, string, string) {
	w, task := conductedDirectWorld(t)
	head := w.gh.SHA(w.gh.Pull(1).Head)
	w.gh.SetChecks("run:failure")
	w.until("the failing check to decide on", func() bool { w.sync(); return w.decisionAt(task) == delivery.PointPRFeedback })
	if green {
		w.gh.SetChecks("run:success")
		w.untilChecks(task, "passing")
	}
	if got := w.gh.SHA(w.gh.Pull(1).Head); got != head {
		t.Fatalf("the head moved: %s, was %s", got, head)
	}
	return w, task, head
}

func TestAReviewStartsAtAFeedbackDecisionWhoseFailingCheckPassedSince(t *testing.T) {
	w, task, head := checksFailedThenRerun(t, true)
	fixes := w.phaseRuns(task, "fix")
	out := w.must(task, "start_phase", `{"phase":"review","categories":["performance"]}`)
	if next, _ := out["next"].(string); next == "" {
		t.Fatalf("start_phase review: %v", out)
	}
	w.until("the review", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'review' AND base_refs->>'target' = $2`, task, head) > 0
	})
	if n := w.phaseRuns(task, "fix"); n != fixes {
		t.Errorf("%d fix Runs started for a check that passed since", n-fixes)
	}
}

func TestNextAtAFeedbackDecisionWhoseFailingCheckPassedSinceStartsNoFix(t *testing.T) {
	w, task, _ := checksFailedThenRerun(t, true)
	fixes := w.phaseRuns(task, "fix")
	w.must(task, "decide", `{"action":"next"}`)
	w.until("waiting on the pull request again, or a fix", func() bool {
		return w.waitingOnPR(task) || w.phaseRuns(task, "fix") != fixes
	})
	w.syncRounds(2)
	if n := w.phaseRuns(task, "fix"); n != fixes {
		t.Errorf("%d fix Runs started by next for a check that passed since", n-fixes)
	}
}

func TestAReviewAtAFeedbackDecisionWithACheckStillFailingIsRefused(t *testing.T) {
	w, task, _ := checksFailedThenRerun(t, false)
	w.refused(task, "start_phase", `{"phase":"review","categories":["performance"]}`,
		"review cannot start at this decision (pull request feedback); it can start fix")
}
