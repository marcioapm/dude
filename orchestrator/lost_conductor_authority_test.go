package orchestrator_test

// A lost conductor (#404) under the decision points of #406: the gate's
// question asked again carries its choices and actions; a quiet decision's
// deadline stays suspended while the person holds a question moved to the
// placeholder or asked again on the fresh conductor; and an owner's open
// answer given before the loss still authorizes the opening after it.

import (
	"strings"
	"testing"
	"time"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
)

// Try again asks the lost gate question again with the gate's choices, Open
// recommended as the conductor asked it, and the actions they carry: the
// person's Open on it opens the pull request.
func TestTryAgainReasksTheGateQuestionWithItsChoicesAndActions(t *testing.T) {
	w := conducting(t)
	task, _, q := w.failedAsOnMain()
	_, lostChoices, lostActions := w.questionShape(q)
	status, out := w.call("/internal/tasks/"+task+"/retry", map[string]any{})
	if status != 201 {
		t.Fatalf("Try again: %d %v", status, out)
	}
	next, _ := out["runId"].(string)
	fresh := w.str(`SELECT COALESCE((SELECT id FROM questions WHERE run_id = $1 AND status = 'open' AND pr_gate_heads IS NOT NULL), '')`, next)
	if fresh == "" {
		t.Fatalf("the gate question was not asked again:\n%s", w.describeRuns())
	}
	options, choices, actions := w.questionShape(fresh)
	if len(options) != len(delivery.GateChoices) || len(choices) != len(delivery.GateChoices) {
		t.Fatalf("options %v, choices %+v: want the gate's %v", options, choices, delivery.GateChoices)
	}
	for i, c := range choices {
		if c.Label != delivery.GateChoices[i] || c.Recommended != lostChoices[i].Recommended {
			t.Errorf("choice %d %+v, want %q recommended=%v as asked", i, c, delivery.GateChoices[i], lostChoices[i].Recommended)
		}
	}
	if strings.Join(actions, ",") != strings.Join(lostActions, ",") || len(actions) < 2 || actions[0] != "open" || actions[1] != "draft" {
		t.Errorf("actions %v, want %v (open, draft)", actions, lostActions)
	}
}

// A quiet decision whose conductor asked the person (#406: its deadline
// suspended while the question is open) and lux lost the conductor: the
// question moved to the placeholder, or asked again on the fresh conductor
// by Try again, still holds the deadline. Deliver's defaults never fire
// while the person has it.
func TestTheDecisionDeadlineWaitsForAQuestionOfALostConductor(t *testing.T) {
	setup := func(t *testing.T) (*world, string, string) {
		w := conducting(t)
		w.waitingFor(time.Hour)
		w.syncer.ParkAfter = time.Hour
		task := w.task()
		w.quietAfterImplement(task)
		w.until("the conductor woken", func() bool { id, _, _ := w.conductor(task); return id != "" })
		w.conductorSpecOf(task)
		w.must(task, "ask_person", `{"question":"Criterion 1 cannot be measured here. Open the PR so CI measures?","choices":["Yes","No"]}`)
		q := w.conductorQuestion(task)
		old := w.str(`SELECT run_id FROM questions WHERE id = $1`, q)
		// Started (Try again's Reask is for a conductor that ran) and
		// waiting on the person.
		w.until("the conductor waiting on the person", func() bool {
			return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'running' AND started_at IS NOT NULL
				AND waiting_since IS NOT NULL`, old) == 1
		})
		return w, task, old
	}
	held := func(t *testing.T, w *world, task, on string) {
		t.Helper()
		for range 3 {
			w.lapse(task)
			w.pump()
		}
		if n := w.defaulted(task, ""); n != 0 || w.phaseRuns(task, "review") != 0 || w.decisionAt(task) != delivery.PointImplemented {
			t.Fatalf("with the person's answer awaited on %s: %d defaulted, %d reviews, at %q\n%s", on, n,
				w.phaseRuns(task, "review"), w.decisionAt(task), w.describeRuns())
		}
		if n := w.count(`SELECT count(*) FROM questions WHERE task_id = $1 AND run_id = $2 AND status = 'open' AND addressee = 'person'`,
			task, on); n != 1 {
			t.Fatalf("%d open questions on %s:\n%s", n, on, w.describeRuns())
		}
	}
	t.Run("moved to the placeholder", func(t *testing.T) {
		w, task, old := setup(t)
		w.lux.ForgetRun(w.luxRunOf(old))
		w.untilLost(old)
		next := w.replacementOf(old)
		if next == "" {
			t.Fatalf("not replaced:\n%s", w.describeRuns())
		}
		held(t, w, task, next)
	})
	t.Run("asked again by Try again", func(t *testing.T) {
		w, task, old := setup(t)
		w.until("all conductor input heard", func() bool {
			return w.count(`SELECT count(*) FROM directives WHERE run_id=$1 AND delivered_at IS NULL AND failed_at IS NULL`, old) == 0
		})
		mustExec(t, w.owner, `UPDATE runs SET status = 'failed', error = 'lux no longer has this Run', ended_at = now() WHERE id = $1`, old)
		status, out := w.call("/internal/tasks/"+task+"/retry", map[string]any{})
		if status != 201 {
			t.Fatalf("Try again: %d %v", status, out)
		}
		next, _ := out["runId"].(string)
		if n := w.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = 'run.created' AND payload->>'retries' = $2`, next, old); n != 1 {
			t.Fatalf("no fresh conductor retrying %s", old)
		}
		held(t, w, task, next)
	})
}

// The owner's open answer, given before lux lost the conductor, is the
// same delivery's and attempt's after Try again (#406's rule): it still
// authorizes the opening, cited, with no second question.
func TestAnOpenAnswerGivenBeforeTheLossStillAuthorizesAfterTryAgain(t *testing.T) {
	w := conducting(t)
	task := w.task()
	q := w.askedAndAnsweredOpen(task, "open")
	old, _, _ := w.conductor(task)
	w.until("all conductor input heard", func() bool {
		return w.count(`SELECT count(*) FROM directives WHERE run_id=$1 AND delivered_at IS NULL AND failed_at IS NULL`, old) == 0
	})
	mustExec(t, w.owner, `UPDATE runs SET status = 'failed', error = 'lux no longer has this Run', ended_at = now() WHERE id = $1`, old)
	status, out := w.call("/internal/tasks/"+task+"/retry", map[string]any{})
	if status != 201 {
		t.Fatalf("Try again: %d %v", status, out)
	}
	next, _ := out["runId"].(string)
	if n := w.count(`SELECT count(*) FROM questions WHERE run_id = $1`, next); n != 0 {
		t.Fatalf("%d questions asked again for an answered one", n)
	}
	if n := w.count(`SELECT count(*) FROM workflow_runs WHERE task_id = $1 AND state->'openAuthority'->>'questionId' = $2
		AND NOT state->'openAuthority' ? 'voided'`, task, q); n != 1 {
		t.Fatalf("the authority is gone or void after Try again: %s",
			w.str(`SELECT COALESCE(state->>'openAuthority', '') FROM workflow_runs WHERE task_id = $1`, task))
	}
	w.until("the fresh conductor", func() bool { id, _, _ := w.conductor(task); return id == next })
	w.carriedOut(task, "start_phase", `{"phase":"simplify"}`)
	w.agentsDone(task, "the simplifier's narrow review", func() bool { return w.decisionAt(task) == delivery.PointReviewed })
	w.finishSimplifierReview(task)
	w.until("before the pull request", func() bool { return w.decisionAt(task) == delivery.PointBeforePR })
	out = w.must(task, "decide", `{"action":"open_pull_request"}`)
	if by, _ := out["authorizedBy"].(map[string]any); by["questionId"] != q || by["choice"] != "open" {
		t.Errorf("open_pull_request's result: %v", out)
	}
	w.until("the pull request", func() bool { return len(w.gh.Pulls()) == 1 })
	if n := w.gateQuestions(task); n != 0 {
		t.Errorf("the person was asked the gate's question %d times", n)
	}
}
