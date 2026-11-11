package orchestrator_test

// After the implementer has started, update_task writes one thing into the
// task: a person's answer on it, into the acceptance criteria, naming the
// answered question. The tester then judges the criteria as amended.

import (
	"strings"
	"testing"
)

// acceptedRed is the criterion a person's answer settles: CI red on a job
// that fails on main too, accepted.
const acceptedRed = "CI is green on the PR, except unit-386, which fails on main too and the owner accepted red"

// askedAndAnswered is a conductor's question to the person on task, and
// their answer to it: its id.
func (w *world) askedAndAnswered(task string) string {
	w.t.Helper()
	out := w.must(task, "ask_person", `{"question":"unit-386 fails on main too. How should criterion 1 be met?","choices":["Accept a red unit-386 on this PR","Fix unit-386 here"]}`)
	q, _ := out["questionId"].(string)
	if q == "" {
		q = w.conductorQuestion(task)
	}
	if status, body := w.call("/internal/questions/"+q+"/answer", map[string]any{"text": "Accept a red unit-386 on this PR"}); status != 200 {
		w.t.Fatalf("the person's answer: %d %v", status, body)
	}
	return q
}

// Past the implementer's start, the criteria take a person's answered
// question on this task, recorded with what they were and the question;
// nothing else does.
func TestAfterTheImplementerTheCriteriaTakeOnlyAPersonsAnswer(t *testing.T) {
	w := conducting(t)
	task := w.task()
	w.talk(task)
	w.implemented(task)
	criteria := `"acceptanceCriteria":["` + acceptedRed + `"]`

	// No question: today's refusal.
	w.refused(task, "update_task", `{`+criteria+`}`, "an implementer has started")
	// A question nobody has.
	w.refused(task, "update_task", `{`+criteria+`,"questionId":"q_nope"}`, "q_nope is not a question of this task a person has answered")
	// Asked, not answered yet.
	out := w.must(task, "ask_person", `{"question":"How should criterion 1 be met?","choices":["Accept red","Fix it"]}`)
	open, _ := out["questionId"].(string)
	if open == "" {
		open = w.conductorQuestion(task)
	}
	w.refused(task, "update_task", `{`+criteria+`,"questionId":"`+open+`"}`, open+" is not a question of this task a person has answered")
	// Another task's answered question.
	other := w.task()
	mustExec(t, w.owner, `INSERT INTO questions (id, organization_id, task_id, prompt, status, answer, answered_at, addressee)
		VALUES ('q_other', $1, $2, 'Accept red?', 'answered', 'Accept red', now(), 'person')`, w.org, other)
	w.refused(task, "update_task", `{`+criteria+`,"questionId":"q_other"}`, "q_other is not a question of this task a person has answered")
	// A question for the conductor, answered, on this task.
	mustExec(t, w.owner, `INSERT INTO questions (id, organization_id, task_id, prompt, status, answer, answered_at, addressee)
		VALUES ('q_conductor', $1, $2, 'Which file?', 'answered', 'a.md', now(), 'conductor')`, w.org, task)
	w.refused(task, "update_task", `{`+criteria+`,"questionId":"q_conductor"}`, "q_conductor is not a question of this task a person has answered")
	if got := w.str(`SELECT acceptance_criteria::text FROM tasks WHERE id = $1`, task); got != `["it greets"]` {
		t.Fatalf("a refused update changed the criteria: %s", got)
	}

	// The person answers: their answer goes into the criteria.
	if status, body := w.call("/internal/questions/"+open+"/answer", map[string]any{"text": "Accept red"}); status != 200 {
		t.Fatalf("the person's answer: %d %v", status, body)
	}
	// The goal is not what an answer changes.
	w.refused(task, "update_task", `{"goal":"Say hello, red CI accepted.","questionId":"`+open+`"}`, "the acceptance criteria only")
	w.must(task, "update_task", `{`+criteria+`,"questionId":"`+open+`"}`)
	if got := w.str(`SELECT acceptance_criteria->>0 FROM tasks WHERE id = $1`, task); got != acceptedRed {
		t.Fatalf("the criteria read %q", got)
	}
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'task.updated'
		AND payload->>'questionId' = $2 AND payload->'before'->>'acceptanceCriteria' = '["it greets"]'
		AND payload->'acceptanceCriteria'->>0 = $3 AND payload->>'by' = 'conductor'`, task, open, acceptedRed); n != 1 {
		t.Errorf("%d task.updated events with the before, the after and the question", n)
	}
}

// The tester's next judgement is of the criteria as the person's answer
// amended them.
func TestTheTesterJudgesTheCriteriaAsThePersonsAnswerAmendedThem(t *testing.T) {
	w := conducting(t)
	// A real model's tester is given the composed prompt, and the fake lux
	// judges the criteria it lists.
	w.onModel("qa_browser", "claude-opus-5-5")
	task := w.task()
	w.talk(task)
	w.implemented(task)
	q := w.askedAndAnswered(task)
	w.must(task, "update_task", `{"acceptanceCriteria":["it greets","`+acceptedRed+`"],"questionId":"`+q+`"}`)
	w.must(task, "start_phase", `{"phase":"test"}`)
	w.until("the tester", func() bool { return w.specOf("test") != nil })
	if p := w.specOf("test").Workload.Prompt; !strings.Contains(p, acceptedRed) {
		t.Errorf("the tester's prompt lacks the amended criterion:\n%s", p)
	}
	w.until("the tester's verdicts", func() bool {
		return w.count(`SELECT count(*) FROM acceptance_verdicts WHERE task_id = $1`, task) == 2
	})
	if got := w.str(`SELECT text FROM acceptance_verdicts WHERE task_id = $1 AND criterion = 2`, task); got != acceptedRed {
		t.Errorf("criterion 2 judged as %q, want the amended one", got)
	}
}
