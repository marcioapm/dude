package orchestrator_test

// Stop and Close of a task whose delivery is held at the pull request by a
// reviewer's open question (delivery.holdForQuestions): no Run is live, the
// question stays open (attempt-scoped, it holds nothing once the delivery
// ends), and neither an answer after the end nor the conductor publishes
// anything or starts a conductor.

import (
	"strings"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
)

// questionHeld is a delivery held at the pull request by one open reviewer
// question, under the policy or (conducted) the conductor: the task, its
// owner and the question's id.
func questionHeld(t *testing.T, conducted bool) (w *world, task, ana, id string) {
	t.Helper()
	if conducted {
		w, task, id = questioned(t)
		w.conductedHold(task)
		ana = w.ownedBy(task, "Ana")
	} else {
		w = newWorld(t)
		w.asksOnce(false)
		task = w.task()
		ana = w.ownedBy(task, "Ana")
		w.deliver(task)
		w.heldAtThePullRequest(task)
		id = w.reviewQuestionOf(task)
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND role <> 'conductor'
		AND status NOT IN ('completed', 'failed', 'aborted')`, task); n != 0 {
		t.Fatalf("%d phase Runs live while held\n%s", n, w.describeRuns())
	}
	return w, task, ana, id
}

// answeredAfterTheEnd has the owner answer the held question once the task
// ended: the answer is recorded (the question is no longer open, so a pick-up
// is not held by it again), and, pumped, nothing is published, no conductor
// is woken or started, and the task keeps status.
func (w *world) answeredAfterTheEnd(task, ana, id, status string) {
	w.t.Helper()
	conductors := len(w.conductorRuns(task))
	woken := w.events(task, "conductor.woken")
	w.settle(ana, id, "answer")
	if n := w.count(`SELECT count(*) FROM review_questions WHERE id = $1 AND status = 'answered'`, id); n != 1 {
		w.t.Fatalf("the answer after the end was not recorded")
	}
	for range 5 {
		w.sync()
		w.pump()
	}
	if n := len(w.gh.Pulls()); n != 0 {
		w.t.Fatalf("%d pull requests opened after the task ended", n)
	}
	if s := w.taskStatus(task); s != status {
		w.t.Fatalf("after the answer the task is %s, want %s", s, status)
	}
	if n := len(w.conductorRuns(task)) - conductors; n != 0 {
		w.t.Errorf("%d conductors started for an answer on an ended task", n)
	}
	if n := w.events(task, "conductor.woken") - woken; n != 0 {
		w.t.Errorf("a conductor was woken %d times for an answer on an ended task", n)
	}
	if n := w.count(`SELECT count(*) FROM conductor_wakes WHERE task_id = $1 AND key LIKE 'review-question:%'
		AND delivered_at IS NULL`, task); n != 0 {
		w.t.Errorf("%d reviewer-question wakes pending for an ended task", n)
	}
}

// Stop of a held delivery: aborted, its workflow aborted, the question left
// open and nothing published at once; under the conductor, the conductor
// can no longer answer it. The owner's answer after the Stop is recorded
// and publishes nothing, wakes no conductor; Try again then goes on past
// the answered question and opens the pull request.
func TestStoppingATaskHeldByAReviewerQuestion(t *testing.T) {
	for _, conducted := range []bool{false, true} {
		name := map[bool]string{false: "policy", true: "conductor"}[conducted]
		t.Run(name, func(t *testing.T) {
			w, task, ana, id := questionHeld(t, conducted)
			out := w.stop(ana, task, map[string]any{"note": "hold it"})
			if out["status"] != "aborted" || w.taskStatus(task) != "aborted" {
				t.Fatalf("stop answered %v, task %s", out, w.taskStatus(task))
			}
			if n := w.count(`SELECT count(*) FROM workflow_runs WHERE task_id = $1 AND status = 'aborted'`, task); n != 1 {
				t.Fatalf("the delivery was not aborted: %s", w.str(`SELECT status FROM workflow_runs WHERE task_id = $1`, task))
			}
			if n := w.count(`SELECT count(*) FROM review_questions WHERE id = $1 AND status = 'open'`, id); n != 1 {
				t.Errorf("the reviewer's question is no longer open after the Stop")
			}
			for range 4 {
				w.sync()
				w.pump()
			}
			if n := len(w.gh.Pulls()); n != 0 {
				t.Fatalf("%d pull requests opened by the Stop", n)
			}
			if conducted {
				// The kept conductor's Run is aborted: the tools server refuses its token.
				if status, body := w.as(task, "answer_review_question", `{"id":"`+id+`","answer":"In UTC."}`); status != 401 {
					t.Fatalf("the stopped task's conductor answering the question: %d %v, want 401", status, body)
				}
				if n := w.events(task, delivery.EvReviewQuestionAnswered); n != 0 {
					t.Fatalf("%d answers recorded for the stopped conductor", n)
				}
			}
			want := "retry restart"
			if conducted {
				want = "resume retry restart"
			}
			if got, _ := w.options(task); strings.Join(got, " ") != want {
				t.Fatalf("a task stopped while held offers %v, want %s", got, want)
			}
			w.answeredAfterTheEnd(task, ana, id, "aborted")
			if got, _ := w.options(task); strings.Join(got, " ") != want {
				t.Fatalf("after the answer the stopped task offers %v, want %s", got, want)
			}
			w.pickUp(ana, task, map[string]any{"action": "retry"})
			if !conducted {
				w.until("the pull request", func() bool { return len(w.gh.Pulls()) == 1 && w.taskStatus(task) == "review" })
			}
		})
	}
}

// Close (Won't do) of a held delivery: closed, its workflow completed, the
// question left open, and the owner's answer afterwards publishes nothing
// and wakes no conductor.
func TestClosingATaskHeldByAReviewerQuestion(t *testing.T) {
	for _, conducted := range []bool{false, true} {
		name := map[bool]string{false: "policy", true: "conductor"}[conducted]
		t.Run(name, func(t *testing.T) {
			w, task, ana, id := questionHeld(t, conducted)
			if status, body := w.callAs(ana, "/internal/tasks/"+task+"/close", map[string]any{"resolution": "wont_do"}); status != 200 {
				t.Fatalf("close: %d %v", status, body)
			}
			if s, r := w.taskStatus(task), w.resolutionOf(task); s != "aborted" || r != "wont_do" {
				t.Fatalf("closed: %s %s", s, r)
			}
			if n := w.count(`SELECT count(*) FROM workflow_runs WHERE task_id = $1 AND status = 'completed'`, task); n != 1 {
				t.Fatalf("the delivery did not end: %s", w.str(`SELECT status FROM workflow_runs WHERE task_id = $1`, task))
			}
			if n := w.count(`SELECT count(*) FROM review_questions WHERE id = $1 AND status = 'open'`, id); n != 1 {
				t.Errorf("the reviewer's question is no longer open after the Close")
			}
			w.answeredAfterTheEnd(task, ana, id, "aborted")
			if r := w.resolutionOf(task); r != "wont_do" {
				t.Errorf("after the answer the closed task's resolution is %s", r)
			}
		})
	}
}
