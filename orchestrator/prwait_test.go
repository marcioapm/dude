package orchestrator_test

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/fakeagent"
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
	if got := w.phasesRun(task)[phases+1:]; len(got) == 0 || got[0] != "review" || slices.Contains(got, "simplify") ||
		slices.Contains(got, "test") {
		t.Fatalf("phases after start_phase review: %v", got)
	}
	if len(w.gh.Pulls()) != 1 {
		t.Fatalf("%d pull requests", len(w.gh.Pulls()))
	}
}

// A person pushed while the pull request waited: the conductor's review and
// a completed test start from GitHub's head, not the delivery's stale one.
func TestAPhaseStartedAtThePullRequestWaitStartsFromAPersonsPush(t *testing.T) {
	w := conducting(t)
	w.onModel("qa_browser", "fake/scripted")
	task := w.reviewing()
	if status, out := w.chat(task, "Look after the pull request for me."); status >= 300 {
		t.Fatalf("chat: %d %v", status, out)
	}
	w.until("waiting on the pull request", func() bool { return w.waitingOnPR(task) })
	branch := w.gh.Pull(1).Head

	pushed := w.gh.CommitOnTop(branch, "A person's change")
	w.must(task, "start_phase", `{"phase":"review","categories":["correctness"]}`)
	w.until("the review's decision", func() bool { return w.decisionAt(task) == "after_review" })
	if n := w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'review' AND base_refs->>'target' = $2`, task, pushed); n != 1 {
		t.Fatalf("%d reviews started from the person's push %s", n, pushed)
	}
	w.must(task, "decide", `{"action":"next"}`)
	w.until("waiting again", func() bool { return w.waitingOnPR(task) })

	pushed = w.gh.CommitOnTop(branch, "Another change")
	w.must(task, "start_phase", `{"phase":"test"}`)
	w.until("the tester completed", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'test' AND base_refs->>'target' = $2 AND status = 'completed'`,
			task, pushed) == 1
	})
}

// A test the conductor starts while the pull request waits, once it
// passes, waits on that pull request again: the verification gate's pass
// asks no Open or Draft of a pull request already open.
func TestATestStartedAtThePullRequestWaitWaitsOnItAgainOncePassed(t *testing.T) {
	w := conducting(t)
	w.onModel("qa_browser", "fake/scripted")
	task := w.reviewing()
	if status, out := w.chat(task, "Look after the pull request for me."); status >= 300 {
		t.Fatalf("chat: %d %v", status, out)
	}
	w.until("waiting on the pull request", func() bool { return w.waitingOnPR(task) })

	w.must(task, "start_phase", `{"phase":"test"}`)
	w.until("the test's verdict, then a wait or a decision", func() bool {
		return w.count(`SELECT count(*) FROM acceptance_verdicts WHERE task_id = $1`, task) > 0 &&
			(w.waitingOnPR(task) || w.decisionAt(task) != "")
	})
	if point := w.decisionAt(task); point != "" {
		t.Fatalf("parked on the conductor's %q decision, want waiting on the open pull request again", point)
	}
	if len(w.gh.Pulls()) != 1 {
		t.Fatalf("%d pull requests", len(w.gh.Pulls()))
	}
}

// fixFailedTestAtPRWait carries a failed test's fix round through as a
// conductor deciding next at each point does: the fix, its review, then the
// test that judges the fix. Fails if the delivery waits on the pull request
// again with the failure untested.
func (w *world) fixFailedTestAtPRWait(task string) {
	w.t.Helper()
	w.until("the failed test's decision", func() bool { return w.decisionAt(task) == delivery.PointReviewed })
	tests := w.phaseRuns(task, "test")
	for _, at := range []string{delivery.PointReviewed, delivery.PointFixed, delivery.PointReviewed} {
		w.until("the "+at+" decision", func() bool { return w.decisionAt(task) == at })
		w.must(task, "decide", `{"action":"next"}`)
	}
	w.until("a re-test, or the wait", func() bool { return w.phaseRuns(task, "test") > tests || w.waitingOnPR(task) })
	if w.phaseRuns(task, "test") == tests {
		w.t.Fatalf("waiting on the pull request again after a failed test's fix, with no re-test; verdicts %v", w.verdicts(task))
	}
}

// A test the conductor starts while the pull request waits, failing, sends
// its fix round through review and then tests again, as any failed test
// does; the re-test passing, the delivery waits on that pull request again.
func TestAFailedTestAtThePullRequestWaitIsTestedAgainAfterItsFix(t *testing.T) {
	w := conducting(t)
	w.onModel("qa_browser", fakeagent.TestFailOnceModel)
	task := w.reviewing()
	if status, out := w.chat(task, "Look after the pull request for me."); status >= 300 {
		t.Fatalf("chat: %d %v", status, out)
	}
	w.until("waiting on the pull request", func() bool { return w.waitingOnPR(task) })
	phases := len(w.phasesRun(task))

	w.must(task, "start_phase", `{"phase":"test"}`)
	w.fixFailedTestAtPRWait(task)
	w.until("the re-test, then waiting again", func() bool { return w.waitingOnPR(task) })
	if got := w.phasesRun(task)[phases:]; !slices.Equal(got, []string{"test", "fix", "review", "test"}) {
		t.Errorf("phases after start_phase test: %v", got)
	}
	if got := w.verdicts(task); !slices.Equal(got, []string{"pass"}) {
		t.Errorf("the re-test's verdicts = %v, want passed", got)
	}
	if len(w.gh.Pulls()) != 1 {
		t.Errorf("%d pull requests", len(w.gh.Pulls()))
	}
	if n := w.count(`SELECT count(*) FROM workflow_runs WHERE task_id = $1 AND (state ? 'actingOnPr' OR state ? 'retestDue')`, task); n != 0 {
		t.Errorf("waiting again still acting on the pull request, or a re-test still pending")
	}
}

// A test at the pull request wait that keeps failing stops for a person
// after its fix rounds, as anywhere else.
func TestATestAtThePullRequestWaitThatKeepsFailingEscalates(t *testing.T) {
	w := conducting(t)
	w.onModel("qa_browser", fakeagent.TestFailModel)
	task := w.reviewing()
	if status, out := w.chat(task, "Look after the pull request for me."); status >= 300 {
		t.Fatalf("chat: %d %v", status, out)
	}
	w.until("waiting on the pull request", func() bool { return w.waitingOnPR(task) })
	tests := w.phaseRuns(task, "test")

	w.must(task, "start_phase", `{"phase":"test"}`)
	for range delivery.MaxTestFixRounds {
		w.fixFailedTestAtPRWait(task)
	}
	w.until("the gate to stop", func() bool { return w.taskStatus(task) == "awaiting_input" })
	if r := w.escalationReason(task); r != delivery.ReasonTestFailedCriteria {
		t.Fatalf("escalated %q, want %s\n%s", r, delivery.ReasonTestFailedCriteria, w.describeRuns())
	}
	if n := w.phaseRuns(task, "test") - tests; n != delivery.MaxTestFixRounds+1 {
		t.Errorf("%d tests, want the first and one after each of %d fix rounds", n, delivery.MaxTestFixRounds)
	}
	if len(w.gh.Pulls()) != 1 {
		t.Errorf("%d pull requests", len(w.gh.Pulls()))
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

func TestAnEndedConductorCannotStartAtThePullRequestWait(t *testing.T) {
	w := conducting(t)
	task := w.reviewing()
	if status, out := w.chat(task, "Look after the pull request."); status >= 300 {
		t.Fatalf("chat: %d %v", status, out)
	}
	w.conductorSpecOf(task)
	run, _, _ := w.conductor(task)
	mustExec(t, w.owner, `UPDATE runs SET status = 'completed', ended_at = now() WHERE id = $1`, run)
	err := w.app.InOrg(context.Background(), w.org, func(tx pgx.Tx) error {
		_, err := delivery.ConductStartPhase(context.Background(), tx,
			delivery.RunRef{Org: w.org, ProjectID: w.project, TaskID: task, RunID: run}, delivery.StartPhase{Phase: "review"})
		return err
	})
	if err == nil || !strings.Contains(err.Error(), "no longer this task's conductor") {
		t.Fatalf("ended conductor start: %v", err)
	}
	if !w.waitingOnPR(task) {
		t.Fatal("a refused start moved the delivery")
	}
}
