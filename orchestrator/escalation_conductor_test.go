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
				AND payload->>'action' = $2 AND payload->>'questionId' = $3 AND NOT payload ? 'by'
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
		ana := w.person("Ana")
		w.assignOwner(task, ana)
		w.must(task, "ask_person", escalationQuestion)
		if status, out := w.answerAs(ana, task, "Your call."); status != 200 {
			t.Fatalf("the answer: %d %v", status, out)
		}
		// The owner decides this one on the banner; the retry gets stuck again.
		if status, out := w.callAs(ana, "/internal/tasks/"+task+"/decide", map[string]any{"action": "retry"}); status != 200 {
			t.Fatalf("the banner: %d %v", status, out)
		}
		w.until("after the retry's fix", func() bool { return w.decisionAt(task) == delivery.PointFixed })
		w.must(task, "decide", `{"action":"next"}`)
		w.until("the second escalation waiting at decide", func() bool {
			return w.count(`SELECT count(*) FROM workflow_runs WHERE task_id = $1 AND step = 'decide'
				AND (state->>'escalations')::int = 2 AND state->'escalation' IS NOT NULL
				AND NOT state->'escalation' ? 'decided'`, task) == 1
		})
		// Ana's answer to the first escalation's question is no answer to this one.
		w.refused(task, "decide_escalation", `{"action":"retry"}`, "the owner has not answered your question about this escalation")
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
		// Everything else holds: an undecided escalation, and the current
		// owner's answer to this conductor's question about it.
		w, task, _, _ := handedOver(t)
		w.conductorSpecOf(task)
		// Waiting, as at decide, but at another step; nothing signals it.
		mustExec(t, w.owner, `UPDATE workflow_runs SET step = 'review' WHERE task_id = $1`, task)
		w.refused(task, "decide_escalation", `{"action":"retry"}`, "no escalation waits on a decision now")
		if d := w.decided(task); d != nil {
			t.Errorf("decided away from decide: %v", d)
		}
	})
}

// The banner, used while the conductor's question about the escalation is
// open, settles the question: closed, never left waiting, and the conductor
// told.
func TestTheBannerSettlesTheConductorsEscalationQuestion(t *testing.T) {
	w, task := stuck(t)
	w.must(task, "ask_person", escalationQuestion)
	q := w.conductorQuestion(task)
	// A tester's finding of the attempt is the next test's to judge, not
	// accepted with the review's.
	mustExec(t, w.owner, `INSERT INTO runs (id, organization_id, project_id, task_id, attempt, status, phase, role, kind)
		SELECT 'run_tested', organization_id, project_id, task_id, attempt, 'completed', 'test', 'qa_browser', 'agent'
		FROM runs WHERE task_id = $1 AND phase = 'implement'`, task)
	mustExec(t, w.owner, `INSERT INTO review_findings (id, organization_id, task_id, run_id, category, severity, title, description)
		SELECT 'fnd_tested', organization_id, task_id, id, 'acceptance', 'blocking', 'Criterion 1 is not met', 'No greeting.'
		FROM runs WHERE id = 'run_tested'`)
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
	if n := w.count(`SELECT count(*) FROM review_findings WHERE id = 'fnd_tested' AND status = 'open'`); n != 1 {
		t.Errorf("accepting the review's findings settled the tester's")
	}
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

// While an escalation waits, the conductor's question is its one question:
// several in one call are refused, saying to ask that one first.
func TestAnEscalationsQuestionIsOneQuestion(t *testing.T) {
	w, task := stuck(t)
	w.refused(task, "ask_person", `{"questions":[{"header":"Escalation","question":"Retry?","choices":[{"label":"Retry"},{"label":"Stop"}]},
		{"header":"Also","question":"Anything else?"}]}`, "not 2 questions")
	if q := w.conductorQuestion(task); q != "" {
		t.Errorf("question %s recorded with several items at an escalation", q)
	}
	// One item in the new shape is the old shape still: it needs actions.
	w.refused(task, "ask_person", `{"questions":[{"header":"Escalation","question":"Retry?","choices":[{"label":"Retry"},{"label":"Stop"}]}]}`,
		"actions")
	w.must(task, "ask_person", escalationQuestion)
}

// Choices that read as the same answer, case and spaces aside, are refused:
// an answer picks the choice it equals so, and the second would take the
// first's action.
func TestTheEscalationsChoicesAreDistinct(t *testing.T) {
	w, task := stuck(t)
	w.refused(task, "ask_person", `{"question":"Q","choices":["Continue"," continue "],"actions":["retry","stop"]}`,
		"read as the same answer")
	if q := w.conductorQuestion(task); q != "" {
		t.Errorf("question %s recorded with colliding choices", q)
	}
}

// A conductor that took over from the one the owner answered may not decide
// on that answer: it must ask again itself.
func TestAReplacementConductorDecidesOnlyOnItsOwnQuestion(t *testing.T) {
	w, task := stuck(t)
	ana := w.person("Ana")
	w.assignOwner(task, ana)
	w.must(task, "ask_person", escalationQuestion)
	if status, out := w.answerAs(ana, task, "Your call."); status != 200 {
		t.Fatalf("the answer: %d %v", status, out)
	}
	first, _, _ := w.conductor(task)
	w.stopped(first)
	w.until("the stopped conductor to be ended", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'completed'`, first) == 1
	})
	// Its successor: started by this message, or already by a wake.
	if status, out := w.callAs(ana, "/internal/tasks/"+task+"/chat", map[string]any{"text": "Are you there?"}); status != 200 && status != 201 {
		t.Fatalf("the next conductor: %d %v", status, out)
	}
	if next, _, _ := w.conductor(task); next == first {
		t.Fatal("no conductor took over")
	}
	w.refused(task, "decide_escalation", `{"action":"stop"}`, "an earlier conductor's question, not yours: ask them again")
	if d := w.decided(task); d != nil {
		t.Errorf("decided on the earlier conductor's answer: %v", d)
	}
}

// testBlocked is a conducted delivery whose test could not run the app:
// escalated test_blocked, the conductor told.
func testBlocked(t *testing.T, owner func(w *world, task string)) (*world, string) {
	w := conducting(t)
	w.onModel("qa_browser", fakeagent.TestBlockedModel)
	task := w.task()
	owner(w, task)
	w.talk(task)
	w.must(task, "start_phase", `{"phase":"implement"}`)
	w.until("after implement", func() bool { return w.decisionAt(task) == delivery.PointImplemented })
	w.must(task, "start_phase", `{"phase":"test"}`)
	w.until("blocked", func() bool { return w.escalationReason(task) == delivery.ReasonTestBlocked })
	w.wokenWith(task, "Escalated to a person: test blocked")
	return w, task
}

const blockedQuestion = `{"question":"The app would not start. Test again, or stop?",
	"choices":["Test again","Stop"],"actions":["retry","stop"]}`

// waiverNote is the note recorded on the task's waived criteria, "" for none.
func (w *world) waiverNote(task string) string {
	var note string
	_ = w.owner.QueryRow(context.Background(), `SELECT COALESCE(max(decision_note), '') FROM acceptance_verdicts
		WHERE task_id = $1 AND decision = 'waived'`, task).Scan(&note)
	return note
}

// A waiver is never one of the conductor's choices: the person's own words
// are its reason, so the conductor's decide_escalation records them even
// with no note of its own.
func TestAWaiverCarriesTheOwnersOwnWords(t *testing.T) {
	var ana string
	w, task := testBlocked(t, func(w *world, task string) { ana = w.person("Ana"); w.assignOwner(task, ana) })
	w.refused(task, "ask_person", `{"question":"The app would not start. Test again, or ship it unchecked?",
		"choices":["Test again","Waive"],"actions":["retry","waive"]}`, "own words")
	w.must(task, "ask_person", blockedQuestion)
	if status, out := w.answerAs(ana, task, "Ship it, I checked it by hand on staging."); status != 200 {
		t.Fatalf("the answer: %d %v", status, out)
	}
	w.must(task, "decide_escalation", `{"action":"waive"}`)
	w.until("the waiver on the verdict", func() bool { return w.waiverNote(task) != "" })
	if note := w.waiverNote(task); !strings.Contains(note, "The owner answered: Ship it, I checked it by hand on staging.") {
		t.Errorf("the waiver's note %q is not the owner's words", note)
	}
}

// An owner's answer that is a choice of the question — one asked before
// waivers were refused as choices — is no reason: the conductor may not
// waive on it, note or not.
func TestAWaiverPickedFromTheConductorsQuestionDecidesNothing(t *testing.T) {
	var ana string
	w, task := testBlocked(t, func(w *world, task string) { ana = w.person("Ana"); w.assignOwner(task, ana) })
	w.must(task, "ask_person", blockedQuestion)
	mustExec(t, w.owner, `UPDATE questions SET options = '["Test again","Waive"]', actions = '["retry","waive"]'
		WHERE id = $1`, w.conductorQuestion(task))
	if status, out := w.answerAs(ana, task, "Waive"); status != 200 {
		t.Fatalf("the answer: %d %v", status, out)
	}
	if d := w.decided(task); d != nil {
		t.Fatalf("a waiver with no reason of the owner's decided the escalation: %v", d)
	}
	w.refused(task, "decide_escalation", `{"action":"waive"}`, "not their reason")
	w.refused(task, "decide_escalation", `{"action":"waive","note":"Ana said to."}`, "not their reason")
	if n := w.count(`SELECT count(*) FROM acceptance_verdicts WHERE task_id = $1 AND decision IS NOT NULL`, task); n != 0 {
		t.Errorf("%d criteria waived with no reason", n)
	}
}

// Several boxes ticked are still picks, not the person's words: the conductor
// cannot waive on them.
func TestAWaiverOnSeveralPicksIsNotTheOwnersWords(t *testing.T) {
	var ana string
	w, task := testBlocked(t, func(w *world, task string) { ana = w.person("Ana"); w.assignOwner(task, ana) })
	w.must(task, "ask_person", `{"questions":[{"header":"Next","question":"Test again, or stop?","multiple":true,
		"choices":[{"label":"Test again"},{"label":"Stop"}]}],"actions":["retry","stop"]}`)
	if status, out := w.callAs(ana, "/internal/questions/"+w.conductorQuestion(task)+"/answer",
		map[string]any{"answers": []map[string]any{{"choices": []int{0, 1}}}}); status != 200 {
		t.Fatalf("the answer: %d %v", status, out)
	}
	w.refused(task, "decide_escalation", `{"action":"waive"}`, "not their reason")
	// A choice's label typed back beside a pick is not a reason either.
	w.must(task, "ask_person", `{"questions":[{"header":"Next","question":"Test again, or stop?","multiple":true,
		"choices":[{"label":"Test again"},{"label":"Stop"}]}],"actions":["retry","stop"]}`)
	if status, out := w.callAs(ana, "/internal/questions/"+w.conductorQuestion(task)+"/answer",
		map[string]any{"answers": []map[string]any{{"choices": []int{0}, "text": "Stop"}}}); status != 200 {
		t.Fatalf("the second answer: %d %v", status, out)
	}
	w.refused(task, "decide_escalation", `{"action":"waive"}`, "not their reason")
	if n := w.count(`SELECT count(*) FROM acceptance_verdicts WHERE task_id = $1 AND decision IS NOT NULL`, task); n != 0 {
		t.Errorf("%d criteria waived on picks alone", n)
	}
}

// On a task nobody owns, a waiver through the conductor is an admin's, as
// on the banner: a member's answer does not hand it over.
func TestAWaiverOnAnOwnerlessTaskIsAnAdminsThroughTheConductorToo(t *testing.T) {
	w, task := testBlocked(t, func(*world, string) {})
	bo, cy := w.person("Bo"), w.person("Cy")
	mustExec(t, w.owner, `UPDATE people SET role = CASE WHEN name = 'Cy' THEN 'admin' ELSE 'member' END
		WHERE organization_id = $1`, w.org)
	w.must(task, "ask_person", blockedQuestion)
	if status, out := w.answerAs(bo, task, "Just ship it, I checked it."); status != 200 {
		t.Fatalf("the member's answer: %d %v", status, out)
	}
	w.refused(task, "decide_escalation", `{"action":"waive","note":"Bo checked."}`, "owner or an organisation admin")
	if d := w.decided(task); d != nil {
		t.Fatalf("a member's answer waived an ownerless task: %v", d)
	}
	w.must(task, "ask_person", blockedQuestion)
	if status, out := w.answerAs(cy, task, "Waive it, checked on staging."); status != 200 {
		t.Fatalf("the admin's answer: %d %v", status, out)
	}
	w.must(task, "decide_escalation", `{"action":"waive","note":"Cy checked."}`)
	w.until("the waiver on the verdict", func() bool { return w.waiverNote(task) != "" })
}
