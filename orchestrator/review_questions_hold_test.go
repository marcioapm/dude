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

// asksOnce has the first review ask heldQuestion (with the scripted
// agent's blocking finding too, when finding), and every later review
// judge the fix as fixed.
func (w *world) asksOnce(finding bool) {
	var reviews int
	w.lux.Decide = func(spec map[string]any) fakelux.Behaviour {
		labels, _ := spec["labels"].(map[string]any)
		switch labels["dude.phase"] {
		case "review":
			if reviews++; reviews > 1 {
				return fakelux.Behaviour{Reply: "Reviewed the fix.\n\n" + fakeagent.Verdict}
			}
			reply := "One thing I could not establish.\n\n" + questionDoc
			if finding {
				reply = "One problem:\n\n```yaml\n" + fakeagent.Finding + "```\n\n" + questionDoc
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
	rows, err := w.owner.Query(context.Background(), `SELECT id FROM review_questions WHERE task_id = $1`, wi)
	if err != nil {
		w.t.Fatal(err)
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil || len(ids) != 1 {
		w.t.Fatalf("questions %v (%v), want one", ids, err)
	}
	return ids[0]
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

// A question asked by the review of a pull request's fix holds that fix's
// promotion: the pull request's branch stays where it was until the
// question is answered, then moves to the checked candidate.
func TestAQuestionOnAPullRequestFixHoldsItsPromotion(t *testing.T) {
	w := newWorld(t)
	ana := w.person("Ana")
	wi := w.reviewing()
	w.assignOwner(wi, ana)
	before := w.head(wi)
	// The fix's own review asks; the review of the simplifier's diff after it does not.
	scripted := w.lux.Decide
	var reviews int
	w.lux.Decide = func(spec map[string]any) fakelux.Behaviour {
		b := scripted(spec)
		if labels, _ := spec["labels"].(map[string]any); labels["dude.phase"] == "review" {
			if reviews++; reviews == 1 {
				b.Reply += "\n" + questionDoc
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
	if got := w.head(wi); got != before {
		t.Fatalf("the pull request's branch moved from %s to %s with a question open", before, got)
	}
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = $2`, wi, delivery.EvPullRequestFixMoved); n != 0 {
		t.Fatalf("%d promotions recorded with a question open", n)
	}
	if s := w.taskStatus(wi); s != "awaiting_input" {
		t.Errorf("task %s while held, want awaiting_input", s)
	}
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
	w.must(task, "decide", `{"action":"next"}`)
	w.finishSimplifierReview(task)
	w.until("before the pull request", func() bool { return w.decisionAt(task) == delivery.PointBeforePR })
	w.must(task, "decide", `{"action":"ask_person"}`)
	if status, out := w.chat(task, "Open"); status != 200 || out["questionId"] == nil {
		t.Fatalf("answer: %d %v", status, out)
	}
	w.must(task, "decide", `{"action":"open_pull_request"}`)
	w.until("held at the pull request, or a pull request", func() bool {
		return len(w.gh.Pulls()) > 0 || w.count(`SELECT count(*) FROM workflow_runs WHERE task_id = $1 AND step = 'openPullRequest' AND status = 'waiting'`, task) == 1
	})
	for range 5 {
		w.pump()
	}
	if n := len(w.gh.Pulls()); n != 0 {
		t.Fatalf("%d pull requests opened with a question open", n)
	}
	w.must(task, "answer_review_question", `{"id":"`+id+`","answer":"In UTC: config/billing.yml sets zone: UTC."}`)
	w.until("the pull request", func() bool { return len(w.gh.Pulls()) == 1 })
}
