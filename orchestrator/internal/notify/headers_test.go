package notify

import "testing"

// An agent asking several questions reads as how many and their headers;
// one question as its text, as before.
func TestAnAskOfSeveralQuestionsReadsAsTheirHeaders(t *testing.T) {
	several := ask{Type: "question.asked", RunID: "run_1", TaskID: "task_1", Task: "WC-214", Role: "implementer",
		Payload: []byte(`{"kind":"agent","prompt":"4 questions: Retry scope, Old route, Tests, Button","items":[
			{"header":"Retry scope"},{"header":"Old route"},{"header":"Tests"},{"header":"Button"}]}`)}
	m, ok := messageFor(several)
	if !ok || m.Title != "WC-214 · Implementer asks 4 questions" || m.Body != "Retry scope, Old route, Tests, Button" {
		t.Errorf("several: %v %+v", ok, m)
	}
	one := several
	one.Payload = []byte(`{"kind":"agent","prompt":"Which locale?","items":[{"header":"","question":"Which locale?"}]}`)
	if m, _ := messageFor(one); m.Title != "WC-214 · Implementer asks" || m.Body != "Which locale?" {
		t.Errorf("one: %+v", m)
	}
	session := several
	session.Session = "ses_1"
	if m, _ := messageFor(session); m.Title != "The brainstorm asks you 4 questions" || m.Body != "Retry scope, Old route, Tests, Button" {
		t.Errorf("a session's: %+v", m)
	}
}
