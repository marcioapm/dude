package orchestrator_test

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/fakelux"
)

const conductedQuestion = "Do invoices close in UTC or in the organisation's zone?"

// questioned is a conducted delivery whose first review asked one question
// and raised no finding: parked after the review round for its conductor.
// Returns the task and the question's id.
func questioned(t *testing.T) (*world, string, string) {
	w := conducting(t)
	scripted := w.lux.Decide
	w.lux.Decide = func(spec map[string]any) fakelux.Behaviour {
		labels, _ := spec["labels"].(map[string]any)
		if labels["dude.phase"] == "review" {
			return fakelux.Behaviour{Reply: "Nothing wrong; one thing I could not establish.\n\n```yaml\nquestion: " +
				conductedQuestion + "\nneeded_from: product\n```\n"}
		}
		return scripted(spec)
	}
	task := w.task()
	w.talk(task)
	w.implemented(task)
	w.carriedOut(task, "decide", `{"action":"next"}`)
	w.until("round 1", func() bool { return w.decisionAt(task) == delivery.PointReviewed })
	var id string
	_ = w.owner.QueryRow(context.Background(), `SELECT id FROM review_questions WHERE task_id = $1`, task).Scan(&id)
	if id == "" {
		t.Fatal("the reviewer's question was not stored")
	}
	return w, task, id
}

// The conductor answers a reviewer's question with answer_review_question:
// the row records its Run, no decision of a person's is recorded, and one
// event says so. It is refused a question already settled, another task's,
// and a call that neither answers nor withdraws.
func TestTheConductorAnswersAReviewQuestionWithoutMakingItADecision(t *testing.T) {
	w, task, id := questioned(t)
	conductor, _, _ := w.conductor(task)

	w.refused(task, "answer_review_question", `{"id":"`+id+`"}`, "one of the two")
	w.refused(task, "answer_review_question", `{"id":"`+id+`","answer":"UTC","withdraw":true,"reason":"r"}`, "one of the two")
	other := w.task()
	theirs := w.askedQuestions(other, "Is a retry expected?")
	w.refused(task, "answer_review_question", `{"id":"`+theirs[0]+`","answer":"Yes"}`, "is not a reviewer question of this task")
	if n := w.events(task, delivery.EvReviewQuestionAnswered) + w.events(other, delivery.EvReviewQuestionAnswered); n != 0 {
		t.Fatalf("%d answered events after refusals, want none", n)
	}

	out := w.must(task, "answer_review_question", `{"id":"`+id+`","answer":"In UTC: config/billing.yml sets zone: UTC."}`)
	if out["status"] != "answered" {
		t.Fatalf("answer_review_question: %v", out)
	}
	w.refused(task, "answer_review_question", `{"id":"`+id+`","withdraw":true,"reason":"moot"}`, "is answered already")

	var byRun, byPerson string
	_ = w.owner.QueryRow(context.Background(), `SELECT COALESCE(answered_by_run, ''), COALESCE(answered_by_person, '')
		FROM review_questions WHERE id = $1`, id).Scan(&byRun, &byPerson)
	if byRun != conductor || byPerson != "" {
		t.Errorf("answered by run %q person %q, want the conductor %q alone", byRun, byPerson, conductor)
	}
	for _, d := range w.taskDecisions(task) {
		if strings.Contains(d, conductedQuestion) {
			t.Errorf("the conductor's answer was recorded as a person's decision: %q", d)
		}
	}
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = $2 AND run_id = $3
		AND payload->>'by' = 'conductor'`, task, delivery.EvReviewQuestionAnswered, conductor); n != 1 {
		t.Errorf("%d answered events by the conductor's Run, want one", n)
	}
	if n := w.events(task, delivery.EvReviewQuestionWithdrawn); n != 0 {
		t.Errorf("%d withdrawn events after a refused withdrawal", n)
	}
}

// The conductor withdraws a question as moot: withdrawn, no decision, one
// event with its reason.
func TestTheConductorWithdrawsAReviewQuestion(t *testing.T) {
	w, task, id := questioned(t)
	w.refused(task, "answer_review_question", `{"id":"`+id+`","withdraw":true}`, "needs its reason")
	if out := w.must(task, "answer_review_question", `{"id":"`+id+`","withdraw":true,"reason":"The export no longer closes months."}`); out["status"] != "withdrawn" {
		t.Fatalf("withdraw: %v", out)
	}
	if qs := w.reviewQuestions(task); qs[0].Status != "withdrawn" {
		t.Errorf("question %+v, want withdrawn", qs[0])
	}
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = $2
		AND payload->>'reason' = 'The export no longer closes months.'`, task, delivery.EvReviewQuestionWithdrawn); n != 1 {
		t.Errorf("%d withdrawn events with the reason, want one", n)
	}
	if d := w.taskDecisions(task); len(d) != 0 {
		t.Errorf("a withdrawal recorded decisions %q", d)
	}
}

// A reviewer's question wakes the conductor with its line, recorded once per
// question; the conductor's briefing lists it while it is open. A person's
// answer wakes the conductor again with the answer.
func TestAReviewQuestionWakesTheConductorAndItsAnswerToo(t *testing.T) {
	w, task, id := questioned(t)
	w.wokenWith(task, "asks "+id+", needed from product: "+conductedQuestion)
	if n := w.count(`SELECT count(*) FROM conductor_wakes WHERE task_id = $1 AND key = $2 AND kind = 'question'`,
		task, "review-question:"+id); n != 1 {
		t.Errorf("%d wakes for the question, want one", n)
	}
	brief := func() string {
		var b string
		if err := w.app.InOrg(context.Background(), w.org, func(tx pgx.Tx) (err error) {
			b, err = delivery.Briefing(context.Background(), tx, task, "run_next", "", "hello")
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return b
	}
	listed := "- " + id + " · correctness · needed from product · " + conductedQuestion
	if b := brief(); !strings.Contains(b, listed) {
		t.Errorf("the briefing does not list the open question:\n%s", b)
	}

	ana := w.person("Ana")
	w.assignOwner(task, ana)
	if status, body := w.callAs(ana, "/internal/review-questions/"+id+"/answer", map[string]any{"answer": "In UTC."}); status != 200 {
		t.Fatalf("the owner's answer: %d %v", status, body)
	}
	w.wokenWith(task, "The person answered reviewer question "+id+": In UTC.")
	if b := brief(); strings.Contains(b, listed) {
		t.Errorf("the briefing still lists the answered question:\n%s", b)
	}
}
