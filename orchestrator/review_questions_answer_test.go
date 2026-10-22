package orchestrator_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/fakeagent"
	"github.com/marciomartins/dude/orchestrator/internal/fakelux"
	"github.com/marciomartins/dude/orchestrator/internal/phases"
)

// askedQuestions stores the review Run's questions on a task of the world's
// as its reviewer's reply would, and returns their ids in order.
func (w *world) askedQuestions(task string, questions ...string) []string {
	w.t.Helper()
	run := "run_rv_" + task
	mustExec(w.t, w.owner, `INSERT INTO runs (id, organization_id, project_id, task_id, attempt, status, phase, role, category, kind)
		SELECT $2, organization_id, project_id, id, 1, 'completed', 'review', 'reviewer', 'correctness', 'agent' FROM tasks WHERE id = $1`,
		task, run)
	var doc strings.Builder
	for _, q := range questions {
		fmt.Fprintf(&doc, "---\nquestion: %s\nneeded_from: product\n", q)
	}
	if err := phases.RecordFindings(context.Background(), w.app, w.org, run, nil, nil, delivery.ParseQuestions(doc.String())); err != nil {
		w.t.Fatal(err)
	}
	rows, err := w.owner.Query(context.Background(), `SELECT id FROM review_questions WHERE run_id = $1 ORDER BY n`, run)
	if err != nil {
		w.t.Fatal(err)
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		w.t.Fatal(err)
	}
	return ids
}

// taskDecisions is what the task's later phases are told people decided:
// question, then answer.
func (w *world) taskDecisions(task string) []string {
	w.t.Helper()
	rows, err := w.owner.Query(context.Background(), `SELECT prompt || ' => ' || answer FROM questions
		WHERE task_id = $1 AND status = 'answered' ORDER BY answered_at`, task)
	if err != nil {
		w.t.Fatal(err)
	}
	out, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		w.t.Fatal(err)
	}
	return out
}

// errorCode is the code of an API error body.
func errorCode(body map[string]any) string {
	e, _ := body["error"].(map[string]any)
	code, _ := e["code"].(string)
	return code
}

// Only the task's owner answers or withdraws a reviewer's question, and
// only an open one: a second answer, or an answer to a withdrawn one, is a
// 409 that changes nothing. The owner's answer is one of the task's
// decisions; a withdrawal is not. One event per transition, none for a
// refusal.
func TestTheOwnerAnswersOrWithdrawsAnOpenReviewQuestionOnce(t *testing.T) {
	w := newWorld(t)
	ana, bo := w.person("Ana"), w.person("Bo")
	wi := w.task()
	w.assignOwner(wi, ana)
	qs := w.askedQuestions(wi, "Do invoices close in UTC?", "Is a retry expected?")
	answer := func(as, id, verb string, body map[string]any) (int, map[string]any) {
		return w.callAs(as, "/internal/review-questions/"+id+"/"+verb, body)
	}

	if status, body := answer(bo, qs[0], "answer", map[string]any{"answer": "UTC"}); status != 403 || errorCode(body) != "not_owner" {
		t.Fatalf("a non-owner's answer: %d %v", status, body)
	}
	if status, body := answer(bo, qs[1], "withdraw", map[string]any{"reason": "moot"}); status != 403 {
		t.Fatalf("a non-owner's withdrawal: %d %v", status, body)
	}
	if status, body := answer(ana, qs[0], "answer", map[string]any{"answer": "  "}); status != 400 {
		t.Fatalf("an empty answer: %d %v", status, body)
	}
	if status, body := answer(ana, qs[1], "withdraw", map[string]any{}); status != 400 {
		t.Fatalf("a withdrawal without a reason: %d %v", status, body)
	}
	if status, body := answer(ana, "rvq_nope", "answer", map[string]any{"answer": "x"}); status != 404 {
		t.Fatalf("an unknown question: %d %v", status, body)
	}
	if got := w.reviewQuestions(wi); got[0].Status != "open" || got[1].Status != "open" {
		t.Fatalf("a refused call changed a question: %+v", got)
	}

	if status, body := answer(ana, qs[0], "answer", map[string]any{"answer": "In UTC, always."}); status != 200 || body["status"] != "answered" {
		t.Fatalf("the owner's answer: %d %v", status, body)
	}
	if status, body := answer(ana, qs[0], "answer", map[string]any{"answer": "No, local."}); status != 409 || errorCode(body) != "conflict" {
		t.Fatalf("a second answer: %d %v", status, body)
	}
	if status, body := answer(ana, qs[1], "withdraw", map[string]any{"reason": "The retry code is gone."}); status != 200 || body["status"] != "withdrawn" {
		t.Fatalf("the owner's withdrawal: %d %v", status, body)
	}
	if status, body := answer(ana, qs[1], "answer", map[string]any{"answer": "Yes."}); status != 409 {
		t.Fatalf("an answer to a withdrawn question: %d %v", status, body)
	}
	if status, body := answer(ana, qs[1], "withdraw", map[string]any{"reason": "again"}); status != 409 {
		t.Fatalf("a second withdrawal: %d %v", status, body)
	}

	got := w.reviewQuestions(wi)
	if got[0].Status != "answered" || got[0].Answer != "In UTC, always." || got[1].Status != "withdrawn" || got[1].Answer != "" {
		t.Errorf("questions after: %+v", got)
	}
	var byPerson, byRun int
	_ = w.owner.QueryRow(context.Background(), `SELECT count(answered_by_person), count(answered_by_run) FROM review_questions
		WHERE task_id = $1`, wi).Scan(&byPerson, &byRun)
	if byPerson != 2 || byRun != 0 {
		t.Errorf("answered by %d people and %d Runs, want both by the person", byPerson, byRun)
	}
	if d := w.taskDecisions(wi); len(d) != 1 || d[0] != "Do invoices close in UTC? => In UTC, always." {
		t.Errorf("decisions = %q, want the answer alone", d)
	}
	if n := w.events(wi, delivery.EvReviewQuestionAnswered); n != 1 {
		t.Errorf("%d %s events, want one", n, delivery.EvReviewQuestionAnswered)
	}
	if n := w.events(wi, delivery.EvReviewQuestionWithdrawn); n != 1 {
		t.Errorf("%d %s events, want one", n, delivery.EvReviewQuestionWithdrawn)
	}
}

// The owner's answer, given while the delivery is between review rounds,
// reaches the next reviewer of the category as a person's answer and the
// next fix Run as one of the task's decisions.
func TestAnAnsweredReviewQuestionReachesTheNextReviewerAndTheFixer(t *testing.T) {
	const asked, answered = "Is a retry expected after a timeout?", "Exactly one retry, after 30 seconds."
	w := newWorld(t)
	// Real models' names, so lux is given each prompt itself.
	w.onModel("reviewer", "llm-review")
	w.onModel("fixer", "llm-fix")
	var reviews int
	w.lux.Decide = func(spec map[string]any) fakelux.Behaviour {
		labels, _ := spec["labels"].(map[string]any)
		switch labels["dude.phase"] {
		case "review":
			switch reviews++; reviews {
			case 1:
				return fakelux.Behaviour{Reply: "One thing I could not establish.\n\n```yaml\nquestion: " + asked + "\nneeded_from: operations\n```\n"}
			case 2:
				return fakelux.Behaviour{Reply: "One problem:\n\n```yaml\n" + fakeagent.Finding + "```\n"}
			}
			return fakelux.Behaviour{Reply: "Reviewed the fix.\n\n" + fakeagent.Verdict}
		case "implement", "fix":
			return fakelux.Behaviour{Commit: map[string]string{"FACTORY.md": fmt.Sprint(labels["dude.run"]) + "\n"}, Message: "work"}
		case "simplify":
			return fakelux.Behaviour{Commit: map[string]string{"SIMPLE.md": "s\n"}, Message: "simplify"}
		}
		return fakelux.Behaviour{Reply: "Done."}
	}
	wi := w.task()
	ana := w.person("Ana")
	w.assignOwner(wi, ana)
	w.deliver(wi)
	// The simplifier starts once the first round is over: the question is
	// stored, and no later review Run exists before the next pass.
	w.until("the simplifier", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'simplify'`, wi) == 1
	})
	qs := w.reviewQuestions(wi)
	if len(qs) != 1 {
		t.Fatalf("questions = %+v, want the first reviewer's", qs)
	}
	var id string
	_ = w.owner.QueryRow(context.Background(), `SELECT id FROM review_questions WHERE task_id = $1`, wi).Scan(&id)
	if status, body := w.callAs(ana, "/internal/review-questions/"+id+"/answer", map[string]any{"answer": answered}); status != 200 {
		t.Fatalf("answer: %d %v", status, body)
	}
	w.until("a pull request, or a stop", func() bool {
		return len(w.gh.Pulls()) == 1 || w.taskStatus(wi) == "awaiting_input"
	})
	if s := w.escalationReason(wi); s != "" {
		t.Fatalf("the delivery stopped for a person: %s", s)
	}
	reviewsRun := w.luxRunsOf("review")
	if len(reviewsRun) < 2 {
		t.Fatalf("%d review Runs on lux, want the simplifier's review too", len(reviewsRun))
	}
	second := reviewsRun[1].Prompt()
	if !strings.Contains(second, asked) || !strings.Contains(second, "Answered by a person: "+answered) {
		t.Errorf("the next reviewer was not shown the person's answer:\n%s", second)
	}
	fixes := w.luxRunsOf("fix")
	if len(fixes) != 1 {
		t.Fatalf("%d fix Runs on lux, want one", len(fixes))
	}
	if fix := fixes[0].Prompt(); !strings.Contains(fix, "Q: "+asked) || !strings.Contains(fix, "A: "+answered) {
		t.Errorf("the fixer was not told the answer as a decision:\n%s", fix)
	}
}
