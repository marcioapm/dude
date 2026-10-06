package orchestrator_test

// An escalation under the conductor: the person still decides, and the
// owner's answer to the conductor's question about it counts — a choice
// decides it as the banner does; a free answer lets the conductor decide it
// (decide_escalation), once, on that answer.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/fakeagent"
	"github.com/marciomartins/dude/orchestrator/internal/fakelux"
)

// escalationQuestion is the conductor's question about a stuck review, its
// choices each naming the escalation action it stands for.
const escalationQuestion = `{"question":"Stuck on the finding. I propose one more narrow fix round.",
	"choices":["Retry as proposed","Accept as it is","Stop"],"actions":["retry","accept","stop"]}`

// stuck is a conducted delivery whose review is stuck on its finding (one
// fix attempt per finding): escalated, the conductor told.
func stuck(t *testing.T) (*world, string) {
	w := conducting(t)
	scripted := w.lux.Decide
	w.lux.Decide = func(spec map[string]any) fakelux.Behaviour {
		labels, _ := spec["labels"].(map[string]any)
		switch labels["dude.phase"] {
		case "review":
			// Raised once, then still there after every fix.
			if fmt.Sprint(labels["dude.run"]) == w.firstReview() {
				return fakelux.Behaviour{Reply: "```yaml\n" + fakeagent.Finding + "```\n"}
			}
			return fakelux.Behaviour{Reply: "```yaml\nverdicts:\n  F1: still\n```\n"}
		case "implement", "fix":
			return fakelux.Behaviour{Commit: map[string]string{"FACTORY.md": fmt.Sprint(labels["dude.run"]) + "\n"}, Message: "work"}
		}
		return scripted(spec)
	}
	task := w.task()
	w.talk(task)
	setPolicy(w, task, `{"maxAttemptsPerFinding":1,"maxReviewIterations":9}`)
	w.must(task, "start_phase", `{"phase":"implement"}`)
	w.until("after implement", func() bool { return w.decisionAt(task) == delivery.PointImplemented })
	w.must(task, "decide", `{"action":"next"}`)
	w.until("round 1", func() bool { return w.decisionAt(task) == delivery.PointReviewed })
	w.must(task, "start_phase", `{"phase":"fix"}`)
	w.until("after the fix", func() bool { return w.decisionAt(task) == delivery.PointFixed })
	w.must(task, "decide", `{"action":"next"}`)
	w.until("stuck", func() bool { return w.escalationReason(task) == "stuck" })
	w.wokenWith(task, "Escalated to a person: stuck")
	return w, task
}

// firstReview is the first review Run of the world's organization.
func (w *world) firstReview() string {
	var id string
	_ = w.owner.QueryRow(context.Background(), `SELECT id FROM runs WHERE organization_id = $1 AND phase = 'review'
		ORDER BY created_at, id LIMIT 1`, w.org).Scan(&id)
	return id
}

// decided is the decision taken on the task's escalation, as the workflow
// keeps it, "" before one; and the escalation, while there is one.
func (w *world) decided(task string) map[string]any {
	var raw []byte
	_ = w.owner.QueryRow(context.Background(), `SELECT COALESCE(state->'escalation'->'decided', 'null') FROM workflow_runs
		WHERE task_id = $1 ORDER BY created_at DESC LIMIT 1`, task).Scan(&raw)
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	return out
}

// answerAs answers the conductor's open question as a person.
func (w *world) answerAs(person, task, text string) (int, map[string]any) {
	w.t.Helper()
	q := w.conductorQuestion(task)
	if q == "" {
		w.t.Fatal("the conductor has no open question")
	}
	return w.callAs(person, "/internal/questions/"+q+"/answer", map[string]any{"text": text})
}

// The owner picks one of the conductor's choices: the escalation is decided
// exactly as from the banner — retry starts the next fix round, accept goes
// on past the findings — and the decision says it came from that answer.
func TestTheOwnersChoiceDecidesTheEscalation(t *testing.T) {
	for _, c := range []struct{ choice, action, phase string }{
		{"Retry as proposed", "retry", "fix"},
		{"Accept as it is", "accept", "simplify"},
	} {
		t.Run(c.action, func(t *testing.T) {
			w, task := stuck(t)
			ana := w.person("Ana")
			w.assignOwner(task, ana)
			before := w.phaseRuns(task, c.phase)
			w.must(task, "ask_person", escalationQuestion)
			q := w.conductorQuestion(task)
			if status, out := w.answerAs(ana, task, c.choice); status != 200 {
				t.Fatalf("the answer: %d %v", status, out)
			}
			w.until("the delivery to go on", func() bool { return w.phaseRuns(task, c.phase) == before+1 })
			if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'task.decided'
				AND payload->>'action' = $2 AND payload->>'questionId' = $3 AND payload->>'by' = 'person'
				AND payload->>'answeredBy' = (SELECT person_id FROM api_keys WHERE id = $4)
				AND strpos(payload->>'note', 'The owner answered: '||$5) > 0
				AND strpos(payload->>'note', 'one more narrow fix round') > 0`, task, c.action, q, ana, c.choice); n != 1 {
				t.Errorf("%d task.decided events for the answer", n)
			}
			if w.taskStatus(task) == "awaiting_input" {
				t.Errorf("the task still waits on a person")
			}
		})
	}
}

// A non-owner's answer is refused, as any answer of theirs is, and decides
// nothing.
func TestANonOwnersChoiceDecidesNothing(t *testing.T) {
	w, task := stuck(t)
	ana, bo := w.person("Ana"), w.person("Bo")
	w.assignOwner(task, ana)
	w.must(task, "ask_person", escalationQuestion)
	if status, _ := w.answerAs(bo, task, "Retry as proposed"); status != 403 {
		t.Fatalf("a non-owner's answer: %d", status)
	}
	if d := w.decided(task); d != nil {
		t.Fatalf("decided by a non-owner: %v", d)
	}
	if w.escalationReason(task) != "stuck" {
		t.Fatalf("the escalation is gone")
	}
}

// The run's scenario: stuck, the conductor asks, the owner answers "Your
// call", and the conductor retries with decide_escalation. The decision is
// recorded as the conductor's on that answer, and the next fix round starts,
// coming back to the conductor once it is done.
func TestAYourCallAnswerHandsTheConductorTheDecision(t *testing.T) {
	w, task := stuck(t)
	ana := w.person("Ana")
	w.assignOwner(task, ana)
	// Before any answer: refused, pointing at the banner.
	w.refused(task, "decide_escalation", `{"action":"retry"}`, "banner")
	w.must(task, "ask_person", escalationQuestion)
	w.refused(task, "decide_escalation", `{"action":"retry"}`, "banner")
	q := w.conductorQuestion(task)
	if status, out := w.answerAs(ana, task, "Your call, use your judgement."); status != 200 {
		t.Fatalf("the answer: %d %v", status, out)
	}
	if d := w.decided(task); d != nil {
		t.Fatalf("a free answer decided the escalation: %v", d)
	}
	w.refused(task, "decide_escalation", `{"action":"resume"}`, "not a way to go on")
	// The other ways to act stay refused, naming the way that works.
	w.refused(task, "start_phase", `{"phase":"fix"}`, "decide_escalation")
	w.refused(task, "dismiss_finding", `{"id":"x","reason":"r"}`, "banner")
	fixes := w.phaseRuns(task, "fix")
	w.must(task, "decide_escalation", `{"action":"retry","note":"One narrow round on the finding."}`)
	w.refused(task, "decide_escalation", `{"action":"stop"}`, "banner")
	w.until("the next fix round", func() bool { return w.phaseRuns(task, "fix") == fixes+1 })
	w.until("back to the conductor after the fix", func() bool { return w.decisionAt(task) == delivery.PointFixed })
	conductor, _, _ := w.conductor(task)
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'task.decided'
		AND payload->>'by' = 'conductor' AND payload->>'questionId' = $2 AND actor_id = $3
		AND payload->>'answeredBy' = (SELECT person_id FROM api_keys WHERE id = $4)`, task, q, conductor, ana); n != 1 {
		t.Errorf("%d task.decided events by the conductor on the owner's answer", n)
	}
	if n := w.count(`SELECT count(*) FROM questions WHERE task_id = $1 AND run_id IS NULL
		AND strpos(answer, 'One narrow round on the finding.') > 0`, task); n != 1 {
		t.Errorf("the conductor's note is not one of the task's decisions")
	}
}

// decide_escalation is refused for an answer to an earlier escalation's
// question, for an answer by someone who is no longer the owner, and when
// the delivery is not at decide.
func TestDecideEscalationNeedsTheOwnersAnswerToThisEscalation(t *testing.T) {
	t.Run("an earlier escalation's answer", func(t *testing.T) {
		w, task := stuck(t)
		w.must(task, "ask_person", escalationQuestion)
		if status, out := w.chat(task, "Your call."); status != 200 {
			t.Fatalf("the answer: %d %v", status, out)
		}
		// The banner decides this one; the retry gets stuck again.
		if status, out := w.call("/internal/tasks/"+task+"/decide", map[string]any{"action": "retry"}); status != 200 {
			t.Fatalf("the banner: %d %v", status, out)
		}
		w.until("after the retry's fix", func() bool { return w.decisionAt(task) == delivery.PointFixed })
		w.must(task, "decide", `{"action":"next"}`)
		w.until("stuck again", func() bool { return w.escalationReason(task) == "stuck" && w.decided(task) == nil })
		w.refused(task, "decide_escalation", `{"action":"retry"}`, "banner")
	})
	t.Run("an answer by someone no longer the owner", func(t *testing.T) {
		w, task := stuck(t)
		ana, bo := w.person("Ana"), w.person("Bo")
		w.assignOwner(task, ana)
		w.must(task, "ask_person", escalationQuestion)
		if status, out := w.answerAs(ana, task, "Your call."); status != 200 {
			t.Fatalf("the answer: %d %v", status, out)
		}
		w.assignOwner(task, bo)
		w.refused(task, "decide_escalation", `{"action":"retry"}`, "banner")
	})
	t.Run("not at decide", func(t *testing.T) {
		w := conducting(t)
		task := w.task()
		w.talk(task)
		w.refused(task, "decide_escalation", `{"action":"retry"}`, "no escalation")
	})
}

// The banner, used while the conductor's question about the escalation is
// open, settles the question: closed, never left waiting, and the conductor
// told.
func TestTheBannerSettlesTheConductorsEscalationQuestion(t *testing.T) {
	w, task := stuck(t)
	w.must(task, "ask_person", escalationQuestion)
	q := w.conductorQuestion(task)
	if status, out := w.call("/internal/tasks/"+task+"/decide", map[string]any{"action": "accept"}); status != 200 {
		t.Fatalf("the banner: %d %v", status, out)
	}
	if n := w.count(`SELECT count(*) FROM questions WHERE id = $1 AND status = 'cancelled'`, q); n != 1 {
		t.Errorf("the conductor's question is still open")
	}
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'question.closed'
		AND payload->>'questionId' = $2 AND payload->>'by' = 'decision'`, task, q); n != 1 {
		t.Errorf("%d question.closed events", n)
	}
	note := w.wokenWith(task, "banner")
	if !strings.Contains(note, "accept") {
		t.Errorf("the conductor was told %q", note)
	}
	w.until("the delivery to go on", func() bool { return w.phaseRuns(task, "simplify") == 1 })
}

// ask_person while an escalation waits is its question: each choice must
// name one of the escalation's actions.
func TestTheEscalationsQuestionNamesItsActions(t *testing.T) {
	w, task := stuck(t)
	w.refused(task, "ask_person", `{"question":"Q","choices":["Retry","Stop"]}`, "actions")
	w.refused(task, "ask_person", `{"question":"Q","choices":["Retry","Stop"],"actions":["retry","resume"]}`, "resume")
	w.must(task, "ask_person", escalationQuestion)
	var escalation string
	_ = w.owner.QueryRow(context.Background(), `SELECT COALESCE(escalation, '') FROM questions WHERE id = $1`,
		w.conductorQuestion(task)).Scan(&escalation)
	if !strings.HasSuffix(escalation, ":1") {
		t.Errorf("the question belongs to escalation %q, want the first", escalation)
	}
}
