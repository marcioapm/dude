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

// A cycle in blocked_by is refused before anything is filed: two tasks
// blocking each other, a three-task loop, and a task blocking itself.
func TestChatEpicTasksProblemRefusesACycle(t *testing.T) {
	for name, tasks := range map[string][]chat.NewTask{
		"two": {{Ref: "a", Title: "A", BlockedBy: []string{"b"}}, {Ref: "b", Title: "B", BlockedBy: []string{"a"}}},
		"three": {{Ref: "a", Title: "A", BlockedBy: []string{"c"}}, {Ref: "b", Title: "B", BlockedBy: []string{"a"}},
			{Ref: "c", Title: "C", BlockedBy: []string{"b"}}},
		"itself": {{Ref: "a", Title: "A", BlockedBy: []string{"a"}}},
	} {
		if p := chat.EpicTasksProblem(tasks); p != "the tasks' blocked_by form a cycle" {
			t.Fatalf("%s: %q", name, p)
		}
	}
	// A diamond is no cycle.
	diamond := []chat.NewTask{{Ref: "a", Title: "A"}, {Ref: "b", Title: "B", BlockedBy: []string{"a"}},
		{Ref: "c", Title: "C", BlockedBy: []string{"a"}}, {Ref: "d", Title: "D", BlockedBy: []string{"b", "c"}}}
	if p := chat.EpicTasksProblem(diamond); p != "" {
		t.Fatalf("a diamond: %q", p)
	}
}

// A model's clarifying question may offer a task or a session: an epic or
// resume choice carries nothing to make, so dude asks its own question.
func TestChatModelClarifyChoicesAreTasksOrSessions(t *testing.T) {
	w := chat.World{Projects: []chat.Project{{ID: "p1", Key: "BIL", Name: "Billing"}}, Kind: chat.KindPublic, Conversation: "C1"}
	for _, route := range []string{chat.RouteEpic, chat.RouteResume} {
		a := chat.Answer{Route: chat.RouteClarify, Confidence: 0.9, Question: &chat.Asked{Text: "Which?", Choices: []chat.AskedChoice{
			{ID: "x", Label: "X", Route: route, Project: "BIL"}, {ID: "s", Label: "A session", Route: chat.RouteBrainstorm}}}}
		if d := chat.Decide(a, w, 0, false, "hm"); d.Route != chat.RouteBrainstorm || !strings.Contains(d.Problem, route) {
			t.Fatalf("%s choice: %+v", route, d)
		}
	}
}

// maybe_projects are read for a brainstorm with no project only, each one
// a project of the person's world (which holds no hidden one), once, at most 3.
func TestChatMaybeProjectsAreOnlyKnownProjectsOfABrainstormWithNone(t *testing.T) {
	w := chat.World{Projects: []chat.Project{{ID: "p1", Key: "BIL", Name: "Billing"}, {ID: "p2", Key: "ABS", Name: "Acme"},
		{ID: "p3", Key: "SDK", Name: "SDK"}, {ID: "p4", Key: "OPS", Name: "Ops"}}, Kind: chat.KindPublic, Conversation: "C1"}
	keys := func(ps []chat.Project) string {
		var out []string
		for _, p := range ps {
			out = append(out, p.Key)
		}
		return strings.Join(out, ",")
	}
	a := chat.Answer{Route: chat.RouteBrainstorm, Confidence: 0.4, MaybeProjects: []string{"ABS", "SEC", "abs", "NOPE", "Billing", "SDK", "OPS"}}
	if d := chat.Decide(a, w, 0, false, "x"); keys(d.Maybe) != "ABS,BIL,SDK" {
		t.Fatalf("maybe %q", keys(d.Maybe))
	}
	a.Project = "OPS"
	if d := chat.Decide(a, w, 0, false, "x"); len(d.Maybe) != 0 {
		t.Fatalf("a brainstorm in OPS has maybe %q", keys(d.Maybe))
	}
	task := chat.Answer{Route: chat.RouteTask, Confidence: 0.9, Project: "NOPE", MaybeProjects: []string{"ABS"}}
	if d := chat.Decide(task, w, 2, false, "x"); d.Route != chat.RouteBrainstorm || len(d.Maybe) != 0 {
		t.Fatalf("a task's fallback has maybe %+v", d)
	}
	parsed, err := chat.ParseAnswer([]byte(`{"route":"brainstorm","confidence":0.5,"maybe_projects":["ABS","SDK"]}`))
	if err != nil || strings.Join(parsed.MaybeProjects, ",") != "ABS,SDK" {
		t.Fatalf("parsed %+v, %v", parsed, err)
	}
}
