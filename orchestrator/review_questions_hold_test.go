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
)

const heldQuestion = "Is a retry expected after a timeout?"

// questionDoc is a reviewer's reply's question document.
const questionDoc = "```yaml\n---\nquestion: " + heldQuestion + "\nneeded_from: operations\n```\n"

// twoQuestionsDoc asks heldQuestion and a second question.
const twoQuestionsDoc = "```yaml\n---\nquestion: " + heldQuestion + "\nneeded_from: operations\n" +
	"---\nquestion: Must a retry keep the request's id?\nneeded_from: product\n```\n"

// asksOnce has the first review ask heldQuestion (with the scripted
// agent's blocking finding too, when finding), and every later review
// judge the fix as fixed.
func (w *world) asksOnce(finding bool) { w.asksFirst(finding, questionDoc) }

// asksFirst is asksOnce asking doc's questions.
func (w *world) asksFirst(finding bool, doc string) {
	var reviews int
	w.lux.Decide = func(spec map[string]any) fakelux.Behaviour {
		labels, _ := spec["labels"].(map[string]any)
		switch labels["dude.phase"] {
		case "review":
			if reviews++; reviews > 1 {
				return fakelux.Behaviour{Reply: "Reviewed the fix.\n\n" + fakeagent.Verdict}
			}
			reply := "One thing I could not establish.\n\n" + doc
			if finding {
				reply = "One problem:\n\n```yaml\n" + fakeagent.Finding + "```\n\n" + doc
			}
			return fakelux.Behaviour{Reply: reply}
		case "implement", "fix":
			return fakelux.Behaviour{Commit: map[string]string{"FACTORY.md": fmt.Sprint(labels["dude.run"]) + "\n"}, Message: "work"}
		case "simplify":
			return fakelux.Behaviour{Commit: map[string]string{"SIMPLE.md": "s\n"}, Message: "simplify"}
		}
		return fakelux.Behaviour{Reply: "Done."}
	}
}

// heldAtThePullRequest waits for the delivery to be held at the pull
// request step by an open question: the task waiting on a person, no pull
// request opened, and stays so across more passes.
func (w *world) heldAtThePullRequest(wi string) {
	w.t.Helper()
	w.until("the delivery held at the pull request, or a pull request", func() bool {
		return len(w.gh.Pulls()) > 0 ||
			w.count(`SELECT count(*) FROM workflow_runs WHERE task_id = $1 AND step = 'openPullRequest' AND status = 'waiting'`, wi) == 1
	})
	for range 5 {
		w.pump()
	}
	if n := len(w.gh.Pulls()); n != 0 {
		w.t.Fatalf("%d pull requests opened with a question open", n)
	}
	if n := w.count(`SELECT count(*) FROM pull_requests WHERE task_id = $1`, wi); n != 0 {
		w.t.Fatalf("%d pull request rows with a question open", n)
	}
	if s := w.taskStatus(wi); s != "awaiting_input" {
		w.t.Fatalf("task %s while held, want awaiting_input", s)
	}
	if s := w.escalationReason(wi); s != "" {
		w.t.Fatalf("the delivery escalated %s instead of holding", s)
	}
}

// reviewQuestionOf is the task's one reviewer question's id.
func (w *world) reviewQuestionOf(wi string) string {
	w.t.Helper()
	ids := w.reviewQuestionIDs(wi)
	if len(ids) != 1 {
		w.t.Fatalf("questions %v, want one", ids)
	}
	return ids[0]
}

// reviewQuestionIDs are the task's reviewer questions' ids, as asked.
func (w *world) reviewQuestionIDs(wi string) []string {
	w.t.Helper()
	rows, err := w.owner.Query(context.Background(), `SELECT id FROM review_questions WHERE task_id = $1 ORDER BY created_at, n`, wi)
	if err != nil {
		w.t.Fatal(err)
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		w.t.Fatal(err)
	}
	return ids
}

// settle answers (verb answer) or withdraws a reviewer question as person.
func (w *world) settle(person, id, verb string) {
	w.t.Helper()
	body := map[string]any{"answer": "Exactly one retry."}
	if verb == "withdraw" {
		body = map[string]any{"reason": "The retry was removed."}
	}
	if status, out := w.callAs(person, "/internal/review-questions/"+id+"/"+verb, body); status != 200 {
		w.t.Fatalf("%s %s: %d %v", verb, id, status, out)
	}
}

// With two questions open, settling one re-reads the rest: still held
// until the last is settled, at the pull request (none opened) and at a
// pull request fix's promotion (its branch unmoved).
func TestTheHoldWaitsForTheLastOfTwoQuestions(t *testing.T) {
	t.Run("pull request", func(t *testing.T) {
		w := newWorld(t)
		w.asksFirst(false, twoQuestionsDoc)
		wi := w.task()
		ana := w.person("Ana")
		w.assignOwner(wi, ana)
		w.deliver(wi)
		w.heldAtThePullRequest(wi)
		ids := w.reviewQuestionIDs(wi)
		if len(ids) != 2 {
			t.Fatalf("questions %v, want two", ids)
		}
		w.settle(ana, ids[0], "answer")
		w.heldAtThePullRequest(wi)
		w.settle(ana, ids[1], "withdraw")
		w.until("the pull request", func() bool { return len(w.gh.Pulls()) == 1 && w.taskStatus(wi) == "review" })
	})
	t.Run("promotion", func(t *testing.T) {
		w := newWorld(t)
		ana := w.person("Ana")
		wi, before := w.heldAtPromotion(ana, twoQuestionsDoc)
		ids := w.reviewQuestionIDs(wi)
		if len(ids) != 2 {
			t.Fatalf("questions %v, want two", ids)
		}
		w.settle(ana, ids[0], "answer")
		for range 5 {
			w.sync()
			w.pump()
		}
		w.notPromoted(wi, before)
		if s := w.taskStatus(wi); s != "awaiting_input" {
			t.Fatalf("task %s with a question still open, want awaiting_input", s)
		}
		w.settle(ana, ids[1], "withdraw")
		w.until("the promotion", func() bool { w.sync(); return w.head(wi) != before })
		w.until("the pull request in review", func() bool { w.sync(); return w.taskStatus(wi) == "review" })
	})
}

// The owner's decision 1: a policy delivery whose reviewer asked a question
// and raised no finding goes through its loop but waits at the pull request:
// no pull request, the task waiting on a person. Answered, or withdrawn, the
// delivery goes on and opens it.
func TestAnOpenQuestionHoldsThePullRequest(t *testing.T) {
	for _, verb := range []string{"answer", "withdraw"} {
		t.Run(verb, func(t *testing.T) {
			w := newWorld(t)
			w.asksOnce(false)
			wi := w.task()
			ana := w.person("Ana")
			w.assignOwner(wi, ana)
			w.deliver(wi)
			w.heldAtThePullRequest(wi)
			if n := w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'fix'`, wi); n != 0 {
				t.Errorf("%d fix Runs for a question alone", n)
			}
			body := map[string]any{"answer": "Exactly one retry."}
			if verb == "withdraw" {
				body = map[string]any{"reason": "The retry was removed."}
			}
			if status, out := w.callAs(ana, "/internal/review-questions/"+w.reviewQuestionOf(wi)+"/"+verb, body); status != 200 {
				t.Fatalf("%s: %d %v", verb, status, out)
			}
			w.until("the pull request", func() bool { return len(w.gh.Pulls()) == 1 && w.taskStatus(wi) == "review" })
		})
	}
}

// With a blocking finding and a question, the fix loop runs as before (the
// fixer is sent the finding, never the question), and the delivery then
// holds at the pull request until the question is answered.
func TestTheFixLoopRunsAndThenTheQuestionHoldsThePullRequest(t *testing.T) {
	w := newWorld(t)
	w.onModel("fixer", "llm-fix")
	w.asksOnce(true)
	wi := w.task()
	ana := w.person("Ana")
	w.assignOwner(wi, ana)
	w.deliver(wi)
	w.heldAtThePullRequest(wi)
	rows, err := w.owner.Query(context.Background(), `SELECT cardinality(finding_ids) FROM runs WHERE task_id = $1 AND phase = 'fix'`, wi)
	if err != nil {
		t.Fatal(err)
	}
	sent, err := pgx.CollectRows(rows, pgx.RowTo[int])
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(sent) != "[1]" {
		t.Fatalf("fix Runs were sent %v findings, want one fix sent the finding", sent)
	}
	for _, lr := range w.luxRunsOf("fix") {
		if strings.Contains(lr.Prompt(), heldQuestion) {
			t.Errorf("the fixer was given the reviewer's question:\n%s", lr.Prompt())
		}
	}
	if n := w.count(`SELECT count(*) FROM review_findings WHERE task_id = $1 AND status = 'open'`, wi); n != 0 {
		t.Errorf("%d findings still open: the hold is not the fix loop's", n)
	}
	if status, out := w.callAs(ana, "/internal/review-questions/"+w.reviewQuestionOf(wi)+"/answer", map[string]any{"answer": "Yes, once."}); status != 200 {
		t.Fatalf("answer: %d %v", status, out)
	}
	w.until("the pull request", func() bool { return len(w.gh.Pulls()) == 1 })
}

// heldAtPromotion is a delivery with its pull request open whose fix, for
// a person's comment, was reviewed by a reviewer who asked heldQuestion:
// held at promoteCandidate, the pull request's branch still at before.
// owner owns the task; doc is the reviewer's questions.
func (w *world) heldAtPromotion(owner, doc string) (wi, before string) {
	w.t.Helper()
	wi = w.reviewing()
	w.assignOwner(wi, owner)
	before = w.head(wi)
	// The fix's own review asks; the review of the simplifier's diff after it does not.
	scripted := w.lux.Decide
	var reviews int
	w.lux.Decide = func(spec map[string]any) fakelux.Behaviour {
		b := scripted(spec)
		if labels, _ := spec["labels"].(map[string]any); labels["dude.phase"] == "review" {
			if reviews++; reviews == 1 {
				b.Reply += "\n" + doc
			}
		}
		return b
	}
	w.gh.Comment(1, "alice", "Please rename the greeting.")
	w.until("the candidate held at its promotion, or promoted", func() bool {
		w.sync()
		return w.head(wi) != before || w.count(`SELECT count(*) FROM workflow_runs WHERE task_id = $1 AND step = 'promoteCandidate' AND status = 'waiting'`, wi) == 1
	})
	for range 5 {
		w.pump()
	}
	w.notPromoted(wi, before)
	if s := w.taskStatus(wi); s != "awaiting_input" {
		w.t.Errorf("task %s while held, want awaiting_input", s)
	}
	return wi, before
}

// notPromoted fails unless the pull request's branch is still at before,
// with no promotion recorded.
func (w *world) notPromoted(wi, before string) {
	w.t.Helper()
	if got := w.head(wi); got != before {
		w.t.Fatalf("the pull request's branch moved from %s to %s with a question open", before, got)
	}
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = $2`, wi, delivery.EvPullRequestFixMoved); n != 0 {
		w.t.Fatalf("%d promotions recorded with a question open", n)
	}
}

// A pull request merged or closed while its fix is held by a question ends
// the delivery as the pull request loop ends it (done, aborted), with the
// question unanswered and the held candidate never published.
func TestAPullRequestEndedWhileItsPromotionIsHeldEndsTheDelivery(t *testing.T) {
	for _, c := range []struct {
		how, status string
		end         func(*world)
	}{
		{"merged", "done", func(w *world) { w.gh.Merge(1) }},
		{"closed", "aborted", func(w *world) { w.gh.Close(1) }},
	} {
		t.Run(c.how, func(t *testing.T) {
			w := newWorld(t)
			wi, before := w.heldAtPromotion(w.person("Ana"), questionDoc)
			c.end(w)
			w.until("the task "+c.status, func() bool { w.sync(); return w.taskStatus(wi) == c.status })
			w.until("the delivery ended", func() bool {
				return w.count(`SELECT count(*) FROM workflow_runs WHERE task_id = $1 AND status IN ('running', 'waiting')`, wi) == 0
			})
			w.notPromoted(wi, before)
			if n := w.count(`SELECT count(*) FROM review_questions WHERE task_id = $1 AND status = 'open'`, wi); n != 1 {
				t.Errorf("%d open questions, want the one nobody answered", n)
			}
		})
	}
}

// A question asked by the review of a pull request's fix holds that fix's
// promotion: the pull request's branch stays where it was until the
// question is answered, then moves to the checked candidate.
func TestAQuestionOnAPullRequestFixHoldsItsPromotion(t *testing.T) {
	w := newWorld(t)
	ana := w.person("Ana")
	wi, before := w.heldAtPromotion(ana, questionDoc)
	if status, out := w.callAs(ana, "/internal/review-questions/"+w.reviewQuestionOf(wi)+"/answer", map[string]any{"answer": "Yes, once."}); status != 200 {
		t.Fatalf("answer: %d %v", status, out)
	}
	w.until("the promotion", func() bool { w.sync(); return w.head(wi) != before })
	w.until("the pull request in review", func() bool { w.sync(); return w.taskStatus(wi) == "review" })
}

// Under the conductor the question holds the pull request too, and the
// conductor's own answer releases it: Open, answered, opens nothing while
// the question is open.
func TestTheConductorsAnswerReleasesTheHold(t *testing.T) {
	w, task, id := questioned(t)
	w.conductedHold(task)
	w.must(task, "answer_review_question", `{"id":"`+id+`","answer":"In UTC: config/billing.yml sets zone: UTC."}`)
	w.until("the pull request", func() bool { return len(w.gh.Pulls()) == 1 })
}

// conductedHold takes a conducted delivery whose reviewer asked a question
// to the pull request gate, has the person answer Open and the conductor
// decide to open it, and waits until it is held there by the question.
func (w *world) conductedHold(task string) {
	w.t.Helper()
	w.must(task, "decide", `{"action":"next"}`)
	w.finishSimplifierReview(task)
	w.until("before the pull request", func() bool { return w.decisionAt(task) == delivery.PointBeforePR })
	w.must(task, "decide", `{"action":"ask_person"}`)
	if status, out := w.chat(task, "Open"); status != 200 || out["questionId"] == nil {
		w.t.Fatalf("answer: %d %v", status, out)
	}
	w.must(task, "decide", `{"action":"open_pull_request"}`)
	w.until("held at the pull request, or a pull request", func() bool {
		return len(w.gh.Pulls()) > 0 || w.count(`SELECT count(*) FROM workflow_runs WHERE task_id = $1 AND step = 'openPullRequest' AND status = 'waiting'`, task) == 1
	})
	for range 5 {
		w.pump()
	}
	if n := len(w.gh.Pulls()); n != 0 {
		w.t.Fatalf("%d pull requests opened with a question open", n)
	}
	if s := w.taskStatus(task); s != "awaiting_input" {
		w.t.Fatalf("task %s while held, want awaiting_input", s)
	}
}

// Releasing the hold does not end another wait on a person: the conductor
// asks the owner something while the question holds the pull request; the
// reviewer's question answered, the task still waits on the conductor's,
// and nothing opens. Answered too, the delivery opens the pull request.
func TestReleasingTheHoldLeavesAnotherQuestionWaiting(t *testing.T) {
	w, task, id := questioned(t)
	w.conductedHold(task)
	ana := w.person("Ana")
	w.assignOwner(task, ana)
	w.must(task, "ask_person", `{"question":"May the export run at night?"}`)
	ask := w.conductorQuestion(task)
	if ask == "" {
		t.Fatal("the conductor's question was not recorded")
	}
	if status, out := w.callAs(ana, "/internal/review-questions/"+id+"/answer", map[string]any{"answer": "In UTC."}); status != 200 {
		t.Fatalf("answer: %d %v", status, out)
	}
	for range 5 {
		w.pump()
	}
	if s := w.taskStatus(task); s != "awaiting_input" {
		t.Fatalf("task %s with the conductor's question open, want awaiting_input", s)
	}
	if n := len(w.gh.Pulls()); n != 0 {
		t.Fatalf("%d pull requests opened with the conductor's question open", n)
	}
	if status, out := w.callAs(ana, "/internal/questions/"+ask+"/answer", map[string]any{"text": "Yes."}); status != 200 {
		t.Fatalf("answer the conductor: %d %v", status, out)
	}
	w.until("the pull request in review", func() bool { return len(w.gh.Pulls()) == 1 && w.taskStatus(task) == "review" })
}

// A question left open by attempt 1 does not hold attempt 2: the task,
// held and then stopped, is started over; the new attempt's review asks
// nothing and its pull request opens, with attempt 1's question still open.
func TestAnEarlierAttemptsQuestionHoldsNothingAfterStartOver(t *testing.T) {
	w := newWorld(t)
	w.asksOnce(false)
	wi := w.task()
	w.deliver(wi)
	w.heldAtThePullRequest(wi)
	// Stopped as a person's abort leaves it; Start over takes an aborted task.
	mustExec(t, w.owner, `UPDATE tasks SET status = 'aborted' WHERE id = $1`, wi)
	if status, body := w.call("/internal/tasks/"+wi+"/recover", map[string]any{"action": "restart"}); status != 200 {
		t.Fatalf("start over: %d %v", status, body)
	}
	w.until("attempt 2's pull request", func() bool { return len(w.gh.Pulls()) == 1 && w.taskStatus(wi) == "review" })
	if n := w.count(`SELECT count(*) FROM review_questions q JOIN runs r ON r.id = q.run_id
		WHERE q.task_id = $1 AND q.status = 'open' AND r.attempt = 1`, wi); n != 1 {
		t.Errorf("%d attempt-1 questions open, want the one", n)
	}
}
