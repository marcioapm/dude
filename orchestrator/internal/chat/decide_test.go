package chat_test

import (
	"strings"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/chat"
)

func TestChatEpicTasksProblemRefusesARepeatedBlocker(t *testing.T) {
	tasks := []chat.NewTask{{Ref: "a", Title: "A"}, {Ref: "b", Title: "B", BlockedBy: []string{"a", "a"}}}
	if p := chat.EpicTasksProblem(tasks); !strings.Contains(p, "twice") {
		t.Fatalf("a repeated blocker: %q", p)
	}
	tasks[1].BlockedBy = []string{"a"}
	if p := chat.EpicTasksProblem(tasks); p != "" {
		t.Fatalf("a valid epic: %q", p)
	}
}

// A model's clarifying question may offer a task or a session: an epic or
// resume choice carries nothing to make, so dude asks its own question.
func TestChatModelClarifyChoicesAreTasksOrSessions(t *testing.T) {
	w := chat.World{Projects: []chat.Project{{ID: "p1", Key: "ABS", Name: "Billing"}}, Kind: chat.KindPublic, Conversation: "C1"}
	for _, route := range []string{chat.RouteEpic, chat.RouteResume} {
		a := chat.Answer{Route: chat.RouteClarify, Confidence: 0.9, Question: &chat.Asked{Text: "Which?", Choices: []chat.AskedChoice{
			{ID: "x", Label: "X", Route: route, Project: "ABS"}, {ID: "s", Label: "A session", Route: chat.RouteBrainstorm}}}}
		if d := chat.Decide(a, w, 0, false, "hm"); d.Route != chat.RouteBrainstorm || !strings.Contains(d.Problem, route) {
			t.Fatalf("%s choice: %+v", route, d)
		}
	}
}
