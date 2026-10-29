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

// resumeWorld holds one session the person can resume from #C1 and one
// private session made in another conversation, which they cannot.
func resumeWorld() chat.World {
	return chat.World{Kind: chat.KindPublic, Conversation: "C1", Sessions: []chat.SessionRef{
		{ID: "ses_open", Title: "Nightly workflow failure", Access: "organisation", OriginConversation: "C1", OriginKind: chat.KindPublic},
		{ID: "ses_private", Title: "Payroll", Access: "private", OriginConversation: "G9", OriginKind: chat.KindPrivate}}}
}

// An unsure resume of a session the person can resume asks whether to
// continue it or start anew; each choice carries what it makes.
func TestChatAnUnsureResumeAsksToContinueOrStartANewSession(t *testing.T) {
	a := chat.Answer{Route: chat.RouteResume, Confidence: 0.72, Session: "ses_open", Title: "Fix the nightly"}
	d := chat.Decide(a, resumeWorld(), 0, false, "fix the nightly workflow failure")
	if d.Route != chat.RouteClarify || d.AskedRoute != chat.RouteResume || d.Question == nil {
		t.Fatalf("decision %+v", d)
	}
	if d.Question.Text != "Continue “Nightly workflow failure”, or start a new session?" {
		t.Fatalf("question %q", d.Question.Text)
	}
	want := []chat.AskedChoice{{ID: "continue", Label: "Continue Nightly workflow failure", Route: chat.RouteResume, Session: "ses_open"},
		{ID: "new", Label: "A new session", Route: chat.RouteBrainstorm}}
	if len(d.Question.Choices) != 2 || d.Question.Choices[0] != want[0] || d.Question.Choices[1] != want[1] {
		t.Fatalf("choices %+v", d.Question.Choices)
	}
	// Out of rounds: no question, a new session.
	if d := chat.Decide(a, resumeWorld(), chat.ClarifyRounds, false, "x"); d.Route != chat.RouteBrainstorm || d.Question != nil {
		t.Fatalf("out of rounds: %+v", d)
	}
	// A long title is clipped in the button, which Slack caps at 75 characters.
	w := resumeWorld()
	w.Sessions[0].Title = strings.Repeat("a", 200)
	if d := chat.Decide(a, w, 0, false, "x"); len([]rune(d.Question.Choices[0].Label)) > 75 {
		t.Fatalf("label %q", d.Question.Choices[0].Label)
	}
}

// At or over the bar, a resume is made as it came.
func TestChatASureResumeResumes(t *testing.T) {
	a := chat.Answer{Route: chat.RouteResume, Confidence: chat.ConfidenceBar, Session: "ses_open"}
	if d := chat.Decide(a, resumeWorld(), 0, false, "x"); d.Route != chat.RouteResume || d.Session != "ses_open" || d.Problem != "" {
		t.Fatalf("decision %+v", d)
	}
}

// A resume of a session the person may not resume from here, or does not
// have, falls back to a new session with no question, unsure or not.
func TestChatAResumeOfAnUnresumableSessionStartsANewOneUnasked(t *testing.T) {
	for _, session := range []string{"ses_private", "ses_unknown"} {
		for _, confidence := range []float64{0.72, 0.95} {
			a := chat.Answer{Route: chat.RouteResume, Confidence: confidence, Session: session}
			if d := chat.Decide(a, resumeWorld(), 0, false, "x"); d.Route != chat.RouteBrainstorm || d.Question != nil || d.Problem == "" {
				t.Fatalf("%s at %v: %+v", session, confidence, d)
			}
		}
	}
	// Chosen in a question, the same session is still refused.
	picked := chat.Choose(chat.Answer{Route: chat.RouteResume}, chat.AskedChoice{Route: chat.RouteResume, Session: "ses_private"})
	if d := chat.Decide(picked, resumeWorld(), 1, true, "x"); d.Route != chat.RouteBrainstorm || d.Session != "" {
		t.Fatalf("a chosen unresumable session: %+v", d)
	}
}

// A model's own question may not carry a session, whatever its route.
func TestChatAModelQuestionNamingASessionIsRefused(t *testing.T) {
	a := chat.Answer{Route: chat.RouteClarify, Confidence: 0.9, Question: &chat.Asked{Text: "Which?", Choices: []chat.AskedChoice{
		{ID: "x", Label: "X", Route: chat.RouteBrainstorm, Session: "ses_open"}, {ID: "s", Label: "A session", Route: chat.RouteBrainstorm}}}}
	if d := chat.Decide(a, resumeWorld(), 0, false, "x"); d.Route != chat.RouteBrainstorm || !strings.Contains(d.Problem, "names a session") {
		t.Fatalf("decision %+v", d)
	}
}

// Choose copies the picked choice's session onto the answer.
func TestChatChooseCopiesTheSession(t *testing.T) {
	a := chat.Choose(chat.Answer{Route: chat.RouteResume, Confidence: 0.72, Title: "t"},
		chat.AskedChoice{ID: "continue", Route: chat.RouteResume, Session: "ses_open"})
	if a.Route != chat.RouteResume || a.Session != "ses_open" || a.Confidence != 1 {
		t.Fatalf("answer %+v", a)
	}
	if d := chat.Decide(a, resumeWorld(), 1, true, "x"); d.Route != chat.RouteResume || d.Session != "ses_open" {
		t.Fatalf("decision %+v", d)
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
