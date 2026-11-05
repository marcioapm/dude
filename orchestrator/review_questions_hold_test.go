package orchestrator_test

import (
	"fmt"
	"strings"
	"testing"

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
// request opened, no escalation, and stays so across more passes.
func (w *world) heldAtThePullRequest(wi string) { w.heldAtThePullRequestBy(wi, false) }

// heldAtThePullRequestBy is heldAtThePullRequest; with conductor, every
// open question is the conductor's: the task waits on nobody.
func (w *world) heldAtThePullRequestBy(wi string, conductor bool) {
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
	if s := w.taskStatus(wi); (s == "awaiting_input") == conductor {
		w.t.Fatalf("task %s while held (by the conductor: %v)", s, conductor)
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
	return column[string](w, `SELECT id FROM review_questions WHERE task_id = $1 ORDER BY created_at, n`, wi)
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
// until the last is settled, at the pull request (none opened). A pull
// request fix is pushed directly, with no promotion to hold.
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
			w.settle(ana, w.reviewQuestionOf(wi), verb)
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
	sent := column[int](w, `SELECT cardinality(finding_ids) FROM runs WHERE task_id = $1 AND phase = 'fix'`, wi)
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

// Under the conductor the question holds the pull request too (Open
// answered, open_pull_request decided), and any settlement releases it:
// the conductor's answer, the owner's answer, the owner's withdrawal. The
// pull request opens and the task is in review, with no further decision
// asked of the conductor.
func TestASettledQuestionReleasesAConductedHold(t *testing.T) {
	for _, c := range []struct{ by, verb string }{{"conductor", "answer"}, {"owner", "answer"}, {"owner", "withdraw"}} {
		t.Run(c.by+" "+c.verb, func(t *testing.T) {
			w, task, id := questioned(t)
			w.conductedHold(task)
			awaited := w.events(task, delivery.EvDecisionAwaited)
			if c.by == "conductor" {
				w.must(task, "answer_review_question", `{"id":"`+id+`","answer":"In UTC: config/billing.yml sets zone: UTC."}`)
			} else {
				ana := w.person("Ana")
				w.assignOwner(task, ana)
				w.settle(ana, id, c.verb)
			}
			w.until("the pull request in review", func() bool { return len(w.gh.Pulls()) == 1 && w.taskStatus(task) == "review" })
			if n := w.events(task, delivery.EvDecisionAwaited) - awaited; n != 0 {
				t.Errorf("%d more decisions asked of the conductor after the settlement", n)
			}
		})
	}
}

// conductedHold takes a conducted delivery whose reviewer asked a question
// to the pull request gate, has the person answer Open and the conductor
// decide to open it, and waits until it is held there by the question,
// which is the conductor's: the task waits on nobody.
func (w *world) conductedHold(task string) { w.conductedHoldAfter(task, func() {}) }

// conductedHoldAfter is conductedHold running beforeOpen just before the
// conductor decides to open the pull request.
func (w *world) conductedHoldAfter(task string, beforeOpen func()) {
	w.conductedHoldBy(task, beforeOpen, true)
}

// conductedHoldBy is conductedHoldAfter; held by the conductor or, when
// beforeOpen forwarded the question, by the person.
func (w *world) conductedHoldBy(task string, beforeOpen func(), conductor bool) {
	w.t.Helper()
	w.must(task, "decide", `{"action":"next"}`)
	w.finishSimplifierReview(task)
	w.until("before the pull request", func() bool { return w.decisionAt(task) == delivery.PointBeforePR })
	w.must(task, "decide", `{"action":"ask_person"}`)
	if status, out := w.chat(task, "Open"); status != 200 || out["questionId"] == nil {
		w.t.Fatalf("answer: %d %v", status, out)
	}
	beforeOpen()
	w.must(task, "decide", `{"action":"open_pull_request"}`)
	w.heldAtThePullRequestBy(task, conductor)
}

// Two waits on a person, the reviewer's question holding the pull request
// (the conductor forwarded it) and a question the conductor asked the owner
// before deciding to open it, end only together, in either order: after the
// first answer the task stays awaiting_input with no status change and
// nothing opens; after the second, one pull request opens in review.
func TestReleasingTheHoldLeavesAnotherQuestionWaiting(t *testing.T) {
	for _, askFirst := range []bool{false, true} {
		t.Run(fmt.Sprintf("askFirst=%v", askFirst), func(t *testing.T) {
			w, task, id := questioned(t)
			ana := w.person("Ana")
			w.conductedHoldBy(task, func() {
				w.assignOwner(task, ana)
				w.must(task, "forward_review_question", `{"id":"`+id+`"}`)
				w.must(task, "ask_person", `{"question":"May the export run at night?"}`)
			}, false)
			ask := w.conductorQuestion(task)
			if ask == "" {
				t.Fatal("the conductor's question was not recorded")
			}
			answerReviewer := func() {
				if status, out := w.callAs(ana, "/internal/review-questions/"+id+"/answer", map[string]any{"answer": "In UTC."}); status != 200 {
					t.Fatalf("answer: %d %v", status, out)
				}
			}
			answerConductor := func() {
				if status, out := w.callAs(ana, "/internal/questions/"+ask+"/answer", map[string]any{"text": "Yes."}); status != 200 {
					t.Fatalf("answer the conductor: %d %v", status, out)
				}
			}
			first, second := answerReviewer, answerConductor
			if askFirst {
				first, second = answerConductor, answerReviewer
			}
			changes := w.events(task, delivery.EvTaskStatusChanged)
			first()
			for range 5 {
				w.pump()
			}
			// A restore re-held by the step's next pass ends at awaiting_input too.
			if n := w.events(task, delivery.EvTaskStatusChanged) - changes; n != 0 {
				t.Fatalf("%d task status changes with one question still open, want none", n)
			}
			if s := w.taskStatus(task); s != "awaiting_input" {
				t.Fatalf("task %s with one question still open, want awaiting_input", s)
			}
			if n := len(w.gh.Pulls()); n != 0 {
				t.Fatalf("%d pull requests opened with one question still open", n)
			}
			second()
			w.until("the pull request in review", func() bool { return len(w.gh.Pulls()) == 1 && w.taskStatus(task) == "review" })
		})
	}
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
	// Seeds an aborted task so restart starts attempt 2 beside attempt 1's open question.
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

// Conductor-first (A10): under the conductor a reviewer's question is the
// conductor's: held at the pull request with the task running, no
// question.asked. forward_review_question gives it to the person: one
// question.asked, the task waiting on them; a second forward is refused.
// The owner answers it (or, unforwarded, overrides the conductor), and the
// pull request opens.
func TestAForwardedReviewQuestionWaitsOnThePerson(t *testing.T) {
	for _, forward := range []bool{true, false} {
		t.Run(fmt.Sprintf("forward=%v", forward), func(t *testing.T) {
			w, task, id := questioned(t)
			if n := w.count(`SELECT count(*) FROM review_questions WHERE id = $1 AND addressee = 'conductor'`, id); n != 1 {
				t.Fatalf("the question is not the conductor's")
			}
			w.conductedHold(task)
			ana := w.ownedBy(task, "Ana")
			asked := func() int {
				return w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'question.asked' AND payload->>'questionId' = $2`, task, id)
			}
			if n := asked(); n != 0 {
				t.Fatalf("%d question.asked for a conductor's question", n)
			}
			if forward {
				w.must(task, "forward_review_question", `{"id":"`+id+`"}`)
				w.refused(task, "forward_review_question", `{"id":"`+id+`"}`, "held by you")
				w.until("the task waiting on the person", func() bool { return w.taskStatus(task) == "awaiting_input" })
				if n := asked(); n != 1 {
					t.Fatalf("%d question.asked after forwarding, want one", n)
				}
			}
			w.settle(ana, id, "answer")
			w.until("the pull request in review", func() bool { return len(w.gh.Pulls()) == 1 && w.taskStatus(task) == "review" })
		})
	}
}
