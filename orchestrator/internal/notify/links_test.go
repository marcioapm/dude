package notify

import "testing"

// Every link a notification carries opens in the ask's organisation
// (`#/o/<slug>/…`); one built without a slug stays bare, for the app's
// locate path.
func TestANotificationLinksIntoItsOrganisation(t *testing.T) {
	for _, c := range []struct {
		name string
		a    ask
		want string
	}{
		{"a Run's question", ask{Slug: "acme", Type: "question.asked", RunID: "run_1", TaskID: "wi_1",
			Payload: []byte(`{"kind":"agent","prompt":"Which?"}`)}, "#/o/acme/run/run_1"},
		{"a task's ask with no Run", ask{Slug: "acme", Type: "task.ready_to_merge", TaskID: "wi_1", Payload: []byte(`{}`)},
			"#/o/acme/task/wi_1"},
		{"a stall", ask{Slug: "northwind", Type: "run.stalled", RunID: "run_2", TaskID: "wi_2",
			Payload: []byte(`{"conducted":false,"text":"stuck"}`)}, "#/o/northwind/task/wi_2"},
		{"a brainstorm's question", ask{Slug: "acme", Type: "question.asked", RunID: "run_3", Session: "ses_1",
			Payload: []byte(`{"prompt":"Which?"}`)}, "#/o/acme/sessions/ses_1"},
		{"no slug", ask{Type: "question.asked", RunID: "run_1", Payload: []byte(`{"prompt":"Which?"}`)}, "#/run/run_1"},
	} {
		m, ok := messageFor(c.a)
		if !ok || m.URL != c.want {
			t.Errorf("%s: %v %q, want %q", c.name, ok, m.URL, c.want)
		}
	}
}
