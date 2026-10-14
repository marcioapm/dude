package orchestrator_test

import (
	"slices"
	"testing"
)

// waitingOnPR says the task's delivery is parked on its pull requests.
func (w *world) waitingOnPR(task string) bool {
	return w.count(`SELECT count(*) FROM workflow_runs WHERE task_id = $1 AND step = 'awaitPullRequest' AND status = 'waiting'`, task) == 1
}

// While the delivery waits on its pull request, the conductor may start a
// fix, a review or a test: the Run goes to work on the pull request's
// branch, and the delivery waits on it again after — no second pull
// request, no simplify, no Open or Draft. Implement and simplify are not
// for then, nor is a fix with nothing to do.
func TestTheConductorStartsAPhaseWhileThePullRequestWaits(t *testing.T) {
	w := conducting(t)
	task := w.reviewing()
	if status, out := w.chat(task, "Look after the pull request for me."); status >= 300 {
		t.Fatalf("chat: %d %v", status, out)
	}
	w.until("waiting on the pull request", func() bool { return w.waitingOnPR(task) })
	branch := w.gh.Pull(1).Head
	before := w.gh.SHA(branch)
	phases := len(w.phasesRun(task))

	w.refused(task, "start_phase", `{"phase":"implement"}`, "cannot start while the delivery waits on its pull requests")
	w.refused(task, "start_phase", `{"phase":"simplify"}`, "cannot start while the delivery waits on its pull requests")
	w.refused(task, "start_phase", `{"phase":"fix"}`, "say what the fix is to do")

	w.must(task, "start_phase", `{"phase":"fix","note":"Rename greet to welcome, as Ana asked in Chat."}`)
	w.until("the fix on the branch, and waiting again", func() bool {
		return w.gh.SHA(branch) != before && w.waitingOnPR(task)
	})
	if got := w.phasesRun(task)[phases:]; !slices.Equal(got, []string{"fix"}) {
		t.Fatalf("phases after start_phase fix: %v", got)
	}
	if len(w.gh.Pulls()) != 1 {
		t.Fatalf("%d pull requests", len(w.gh.Pulls()))
	}

	w.must(task, "start_phase", `{"phase":"review","categories":["correctness"]}`)
	// Its findings are the conductor's to triage, as after any review.
	w.until("the review's decision", func() bool { return w.decisionAt(task) == "after_review" })
	w.must(task, "decide", `{"action":"next"}`)
	w.until("waiting again", func() bool { return w.waitingOnPR(task) })
	if got := w.phasesRun(task)[phases+1:]; len(got) == 0 || got[0] != "review" || slices.Contains(got, "simplify") {
		t.Fatalf("phases after start_phase review: %v", got)
	}
	if len(w.gh.Pulls()) != 1 {
		t.Fatalf("%d pull requests", len(w.gh.Pulls()))
	}
}

// Refused while a phase that publishes is at work (one writer), and past
// the pull request's fix bound.
func TestStartingAPhaseWhileThePullRequestWaitsHasTheSameBounds(t *testing.T) {
	w := conducting(t)
	task := w.reviewing()
	if status, out := w.chat(task, "Look after the pull request for me."); status >= 300 {
		t.Fatalf("chat: %d %v", status, out)
	}
	w.until("waiting on the pull request", func() bool { return w.waitingOnPR(task) })

	mustExec(t, w.owner, `INSERT INTO runs (id, organization_id, project_id, task_id, attempt, status, phase, role)
		VALUES ('run_writer', $1, $2, $3, 1, 'running', 'simplify', 'simplifier')`, w.org, w.project, task)
	w.refused(task, "start_phase", `{"phase":"review"}`, "one writer at a time")
	mustExec(t, w.owner, `UPDATE runs SET status = 'completed', ended_at = now() WHERE id = 'run_writer'`)

	mustExec(t, w.owner, `UPDATE workflow_runs SET state = jsonb_set(state, '{prIteration}', to_jsonb((state->'policy'->>'maxPrFixIterations')::int))
		WHERE task_id = $1`, task)
	w.refused(task, "start_phase", `{"phase":"fix","note":"Rename it."}`, "at their bound")
	mustExec(t, w.owner, `UPDATE workflow_runs SET state = jsonb_set(state, '{iteration}', to_jsonb((state->'policy'->>'maxReviewIterations')::int))
		WHERE task_id = $1`, task)
	w.refused(task, "start_phase", `{"phase":"review"}`, "at its bound")
	if !w.waitingOnPR(task) {
		t.Error("a refused start moved the delivery")
	}
}

// Under Deliver, the conductor starts nothing while the pull request waits.
func TestADeliveryDeliverDecidesTakesNoStartWhileThePullRequestWaits(t *testing.T) {
	w := conducting(t)
	task := w.reviewing()
	if status, out := w.chat(task, "What is this pull request about?"); status >= 300 {
		t.Fatalf("chat: %d %v", status, out)
	}
	mustExec(t, w.owner, `UPDATE workflow_runs SET state = jsonb_set(state, '{decider}', '"policy"') WHERE task_id = $1`, task)
	w.refused(task, "start_phase", `{"phase":"review"}`, "Deliver takes this task's decisions")
}
