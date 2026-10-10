package orchestrator_test

// Several questions in one ask_person, answered together through the
// question's form: validated against what was asked, kept whole, and told
// to the agent as one message.

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/dbtest"
	"github.com/marciomartins/dude/orchestrator/internal/fakelux"
)

const fourAsked = `{"questions":[
	{"header":"Retry scope","question":"Which failures should the payment call retry?","choices":[
		{"label":"5xx and network errors only","description":"A 4xx means our request is wrong.","recommended":true},
		{"label":"Everything (current behaviour)"},{"label":"Nothing — show the error"}]},
	{"header":"Old route","question":"The legacy /pay route has its own retry. What should happen to it?",
		"choices":[{"label":"Fold it into this change"},{"label":"Leave it; file a task"},{"label":"Delete the route"}]},
	{"header":"Tests","question":"Which layers should cover the split?","multiple":true,
		"choices":[{"label":"Unit (PaymentSplitter)"},{"label":"API contract"},{"label":"Browser e2e on checkout"}]},
	{"header":"Button","question":"What should the split button say?","choices":[{"label":"Split payment"},{"label":"Pay in parts"}]}]}`

// askingFour starts a delivery whose implementer asks fourAsked.
func (w *world) askingFour() (wi, runID, qid string) {
	w.withTools()
	w.lux.Decide = func(spec map[string]any) fakelux.Behaviour {
		if labels, _ := spec["labels"].(map[string]any); labels["dude.phase"] != "implement" {
			return fakelux.Behaviour{Hang: true}
		}
		return fakelux.Behaviour{Ask: fourAsked, Reply: "Done.", Commit: map[string]string{"a.md": "x\n"}, Message: "work"}
	}
	wi = w.task()
	w.deliver(wi)
	w.until("the questions to reach a person", func() bool { return w.taskStatus(wi) == "awaiting_input" })
	_ = w.owner.QueryRow(context.Background(), `SELECT id FROM runs WHERE task_id = $1`, wi).Scan(&runID)
	return wi, runID, w.questionID(wi)
}

func TestFourQuestionsAreAnsweredTogetherAndToldAsOneMessage(t *testing.T) {
	w := newWorld(t)
	wi, runID, qid := w.askingFour()
	var prompt string
	_ = w.owner.QueryRow(context.Background(), `SELECT prompt FROM questions WHERE id = $1`, qid).Scan(&prompt)
	if prompt != "4 questions: Retry scope, Old route, Tests, Button" {
		t.Errorf("prompt %q", prompt)
	}
	path := "/internal/questions/" + qid + "/answer"
	full := []map[string]any{{"choices": []int{0}}, {"choices": []int{1}}, {"choices": []int{1, 0}}, {"choices": []int{}, "text": "Pay with two cards"}}

	// Refused, readably, and nothing settled: one text for several, too
	// few answers, one unanswered, out of range, two picks where one goes.
	for _, c := range []struct {
		body map[string]any
		want string
	}{
		{map[string]any{"text": "yes"}, "answer each"},
		{map[string]any{"answers": full[:3]}, "3 answers for 4 questions"},
		{map[string]any{"answers": []map[string]any{full[0], {"choices": []int{}}, full[2], full[3]}}, "question 2 (Old route): not answered"},
		{map[string]any{"answers": []map[string]any{{"choices": []int{5}}, full[1], full[2], full[3]}}, "not one of its 3 choices"},
		{map[string]any{"answers": []map[string]any{{"choices": []int{0, 1}}, full[1], full[2], full[3]}}, "does not take several"},
		{map[string]any{"answers": []map[string]any{full[0], full[1], full[2], {"choices": []int{0}, "text": "both"}}}, "not both"},
		{map[string]any{"answers": full, "note": strings.Repeat("n", 4001)}, "a note of at most 4000"},
	} {
		status, body := w.call(path, c.body)
		if status != 422 || !strings.Contains(fmt.Sprint(body["error"]), c.want) {
			t.Errorf("%v: %d %v, want 422 saying %q", c.body, status, body, c.want)
		}
	}
	if n := w.count(`SELECT count(*) FROM questions WHERE id = $1 AND status = 'open'`, qid); n != 1 {
		t.Fatal("a refused answer settled the question")
	}

	status, body := w.call(path, map[string]any{"answers": full, "note": "Keep the retry budget under 10s total — checkout times out at 15."})
	if status != 200 {
		t.Fatalf("answer: %d %v", status, body)
	}
	w.until("the implementer to finish", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'completed'`, runID) == 1
	})
	want := `Answers to your 4 questions:

1. Retry scope — Which failures should the payment call retry?
   → 5xx and network errors only
2. Old route — The legacy /pay route has its own retry. What should happen to it?
   → Leave it; file a task
3. Tests — Which layers should cover the split? (several allowed)
   → Unit (PaymentSplitter); API contract
4. Button — What should the split button say?
   → in their own words: "Pay with two cards"

Also from `
	in := w.lux.Runs()[0].Inputs
	if len(in) != 1 || !strings.HasPrefix(in[0], want) || !strings.HasSuffix(in[0], ":\nKeep the retry budget under 10s total — checkout times out at 15.") {
		t.Errorf("the agent was given %q", in)
	}
	var answers []map[string]any
	var answer string
	_ = w.owner.QueryRow(context.Background(), `SELECT answer, answers FROM questions WHERE id = $1`, qid).Scan(&answer, &answers)
	if !strings.HasPrefix(answer, "1. Retry scope") || len(answers) != 4 || answers[3]["text"] != "Pay with two cards" {
		t.Errorf("kept %q %v", answer, answers)
	}
	var payload map[string]any
	_ = w.owner.QueryRow(context.Background(), `SELECT payload FROM events WHERE task_id = $1 AND event_type = 'question.answered'`, wi).Scan(&payload)
	if a, _ := payload["answers"].([]any); len(a) != 4 || payload["note"] == nil {
		t.Errorf("question.answered %v", payload)
	}
	if got := w.taskStatus(wi); got == "awaiting_input" {
		t.Errorf("task still %s after the answers", got)
	}
}

// One question answered through the form is the same answer, told the same
// way, as one answered by its text.
func TestOneQuestionThroughTheFormIsTheSameAnswer(t *testing.T) {
	w := newWorld(t)
	wi, runID := w.asking()
	qid := w.questionID(wi)
	if status, body := w.call("/internal/questions/"+qid+"/answer", map[string]any{"answers": []map[string]any{{"choices": []int{0}}},
		"note": "and this"}); status != 422 {
		t.Errorf("a note on one question: %d %v", status, body)
	}
	if status, body := w.call("/internal/questions/"+qid+"/answer", map[string]any{"answers": []map[string]any{{"choices": []int{0}}}}); status != 200 {
		t.Fatalf("answer: %d %v", status, body)
	}
	w.until("the implementer to finish", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'completed'`, runID) == 1
	})
	if in := w.lux.Runs()[0].Inputs; len(in) != 1 || in[0] != "Answer to your question \"Should FACTORY.md be in English?\":\n\nyes" {
		t.Errorf("the agent was given %q", in)
	}
	var answer string
	_ = w.owner.QueryRow(context.Background(), `SELECT answer FROM questions WHERE id = $1`, qid).Scan(&answer)
	if answer != "yes" {
		t.Errorf("answer kept as %q", answer)
	}
}

// A session's question put to one member: they answer the whole ask
// through its form; another member cannot, and what they write waits for
// the answer.
func TestASessionsFourQuestionsAreTheNamedMembersToAnswer(t *testing.T) {
	s := newSessionWorld(t)
	s.withTools()
	id := s.session()
	s.join(id, s.ana, "chat")
	s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/chat", map[string]any{"text": "plan the split"})
	run := s.started(id)
	asked := strings.TrimSuffix(strings.TrimSpace(fourAsked), "}") + `,"to":"Ana"}`
	status, out := s.tool(run, "ask_person", asked)
	if status != 200 {
		t.Fatalf("ask_person: %d %v", status, out)
	}
	qid := out["questionId"].(string)
	path := "/internal/sessions/" + id + "/questions/" + qid + "/answer"
	full := []map[string]any{{"choices": []int{0}}, {"choices": []int{1}}, {"choices": []int{0}}, {"choices": []int{1}}}
	if status, body := s.as(s.marcio, "POST", path, map[string]any{"answers": full}); status != 403 {
		t.Errorf("another member answered Ana's questions: %d %v", status, body)
	}
	// A message from Ana is not an answer to several: it waits with others'.
	s.ok(s.ana, "POST", "/internal/sessions/"+id+"/chat", map[string]any{"text": "one sec"})
	var st string
	_ = s.owner.QueryRow(context.Background(), `SELECT status::text FROM questions WHERE id = $1`, qid).Scan(&st)
	if st != "open" {
		t.Fatalf("a chat message settled four questions: %s", st)
	}
	if status, body := s.as(s.ana, "POST", path, map[string]any{"answers": full[:2]}); status != 422 {
		t.Errorf("two answers for four: %d %v", status, body)
	}
	s.ok(s.ana, "POST", path, map[string]any{"answers": full, "note": "Ship it small."})
	s.until("the answers to reach the agent", func() bool {
		v := s.luxRun(run)
		return slices.ContainsFunc(append(v.inputs, v.resumes...), func(in string) bool {
			return strings.HasPrefix(in, "Ana Nunes answered your 4 questions:\n\n1. Retry scope") &&
				strings.HasSuffix(in, "Also from Ana Nunes:\nShip it small.")
		})
	})
}

// A task's conductor asking four questions: a Chat message is not their
// answer (aside or not, it waits beside them), and its form's answers
// through Chat, naming the question, are.
func TestAConductorsFourQuestionsAreAnsweredThroughChat(t *testing.T) {
	w := conductorWorld(t)
	w.withTools()
	scripted := w.lux.Decide
	asked := false
	w.lux.Decide = func(spec map[string]any) fakelux.Behaviour {
		labels, _ := spec["labels"].(map[string]any)
		if labels["dude.role"] == "conductor" && !asked {
			asked = true
			return fakelux.Behaviour{Ask: fourAsked, Reply: "Thanks."}
		}
		return scripted(spec)
	}
	task, _ := w.delivered()
	_, out := w.chat(task, "can you change it?")
	runID, _ := out["runId"].(string)
	w.until("its questions", func() bool { return w.conductorQuestion(task) != "" })
	qid := w.conductorQuestion(task)

	// A message is never the answer to four: it goes beside them, as an aside does.
	if status, body := w.chat(task, "Yes"); status != 200 || body["questionId"] != nil {
		t.Errorf("a message while four questions wait: %d %v", status, body)
	}
	// Aside: a message beside them, the questions still open.
	if status, body := w.call("/internal/tasks/"+task+"/chat", map[string]any{"text": "what does it touch?", "aside": true}); status != 200 || body["questionId"] != nil {
		t.Errorf("aside: %d %v", status, body)
	}
	if q := w.conductorQuestion(task); q != qid {
		t.Fatalf("an aside settled the questions")
	}
	full := []map[string]any{{"choices": []int{0}}, {"choices": []int{1}}, {"choices": []int{0}}, {"choices": []int{1}}}
	if status, body := w.call("/internal/tasks/"+task+"/chat", map[string]any{"questionId": "qst_other", "answers": full}); status != 409 {
		t.Errorf("answers to another question: %d %v", status, body)
	}
	status, body := w.call("/internal/tasks/"+task+"/chat", map[string]any{"questionId": qid, "answers": full, "note": "Small, please."})
	if status != 200 || body["questionId"] != qid {
		t.Fatalf("answers through Chat: %d %v", status, body)
	}
	var told string
	_ = w.owner.QueryRow(context.Background(), `SELECT text FROM directives WHERE run_id = $1 ORDER BY created_at DESC LIMIT 1`, runID).Scan(&told)
	if !strings.HasPrefix(told, "Answers to your 4 questions:\n\n1. Retry scope") || !strings.HasSuffix(told, ":\nSmall, please.") {
		t.Errorf("the conductor is told %q", told)
	}
}

// 104 gives every question already asked its one item, from its prompt and
// options, and a question written without items gets the same.
func TestEveryQuestionHasItsItemsAfterTheUpgrade(t *testing.T) {
	owner, apply := dbtest.Upgrade(t, "104")
	mustExec(t, owner, `INSERT INTO organizations (id, name, slug) VALUES ('org_q', 'Q', 'q')`)
	mustExec(t, owner, `INSERT INTO questions (id, organization_id, prompt, options, status, answer)
		VALUES ('q_choices', 'org_q', 'Proceed?', '["Yes","No"]', 'answered', 'Yes'), ('q_free', 'org_q', 'Why?', '[]', 'open', NULL)`)
	apply()
	items := func(id string) string {
		var raw string
		if err := owner.QueryRow(context.Background(), `SELECT items::text FROM questions WHERE id = $1`, id).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		return raw
	}
	var got []map[string]any
	_ = json.Unmarshal([]byte(items("q_choices")), &got)
	if len(got) != 1 || got[0]["question"] != "Proceed?" || got[0]["multiple"] != false || got[0]["header"] != "" {
		t.Fatalf("q_choices items %v", got)
	}
	choices, _ := got[0]["choices"].([]any)
	if len(choices) != 2 || choices[1].(map[string]any)["label"] != "No" || choices[0].(map[string]any)["recommended"] != false {
		t.Errorf("q_choices choices %v", choices)
	}
	if raw := items("q_free"); raw != `[{"header": "", "choices": [], "multiple": false, "question": "Why?"}]` {
		t.Errorf("q_free items %s", raw)
	}
	mustExec(t, owner, `INSERT INTO questions (id, organization_id, prompt, options) VALUES ('q_new', 'org_q', 'Later?', '["A"]')`)
	if raw := items("q_new"); !strings.Contains(raw, `"label": "A"`) {
		t.Errorf("a question written without items: %s", raw)
	}
	for _, bad := range []string{`'[]'`, `'[{},{},{},{},{}]'`, `'{}'`} {
		if _, err := owner.Exec(context.Background(), `INSERT INTO questions (id, organization_id, prompt, items) VALUES ('q_bad', 'org_q', 'x', `+bad+`)`); err == nil {
			t.Errorf("items %s accepted", bad)
		}
	}
	if _, err := owner.Exec(context.Background(), `UPDATE questions SET answers = '[{},{}]' WHERE id = 'q_free'`); err == nil {
		t.Error("two answers for one question accepted")
	}
}
