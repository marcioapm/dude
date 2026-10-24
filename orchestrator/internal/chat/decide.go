package chat

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// The fixed facts of routing: the same for every provider, and not
// settings (a provider cannot change them).
const (
	ConfidenceBar  = 0.75
	ClarifyRounds  = 2
	EpicTasksMax   = 12
	resumeLookback = 14 // days of chat-born sessions offered to resume
)

// Routes the routing judgement may answer, and those a request is logged with.
const (
	RouteBrainstorm   = "brainstorm"
	RouteTask         = "task"
	RouteEpic         = "epic"
	RouteResume       = "resume"
	RouteClarify      = "clarify"
	RouteTaskLink     = "task_link"
	RouteNotConnected = "not_connected"
	RouteNotMember    = "not_member"
	RouteFailed       = "failed"
)

// BuiltInInstructions is what the routing judgement is told first, before
// the organisation's additional instructions and the provider's.
const BuiltInInstructions = `You route a message someone wrote to dude, a software-delivery system, from a team chat. dude can:
- brainstorm: start a session with an agent that discusses, investigates and reproduces (it starts read-only investigators itself). The default when in doubt, and for "investigate", "reproduce", "look into", questions and discussion.
- task: file one task in a project (or an epic of it), in the Backlog, not started, owned by the person. For a clear, single piece of work.
- epic: open an epic in a project and file the tasks it needs (at most 12), each in the Backlog, not started, with blocked_by naming other tasks of the same answer by ref. For work that needs several tasks.
- resume: send the message into one of the person's recent chat sessions it continues (by its id).
- clarify: ask one question with 2 to 4 choices, when the message could go two ways and a wrong guess would cost.

Use only the projects and epics listed. Never invent a project, an epic or a session id.

Answer with only a JSON object, nothing before or after it:
{"route":"brainstorm"|"task"|"epic"|"resume"|"clarify",
 "confidence":<0 to 1: how sure you are of the route>,
 "reasoning":"<one or two sentences, shown to the organisation's admins>",
 "project":"<project key>"|null,
 "epic":"<epic title, an existing epic of that project, task route only>"|null,
 "title":"<a short title for the session or task>",
 "goal":"<markdown: what the task must achieve, task route only>",
 "criteria":["<acceptance criterion>", …],
 "new_epic":{"title":"…","description":"<markdown>","tasks":[{"ref":"t1","title":"…","goal":"…","criteria":["…"],"blocked_by":[]}, …]}|null,
 "session":"<session id, resume route only>"|null,
 "question":{"text":"…","choices":[{"id":"…","label":"…","route":"task","project":"<key>"}, …]}|null}`

// Answer is the routing judgement's JSON answer.
type Answer struct {
	Route      string   `json:"route"`
	Confidence float64  `json:"confidence"`
	Reasoning  string   `json:"reasoning"`
	Project    string   `json:"project"`
	Epic       string   `json:"epic"`
	Title      string   `json:"title"`
	Goal       string   `json:"goal"`
	Criteria   []string `json:"criteria"`
	NewEpic    *NewEpic `json:"new_epic"`
	Session    string   `json:"session"`
	Question   *Asked   `json:"question"`
}

// NewEpic is the epic route's epic and its tasks.
type NewEpic struct {
	Title       string    `json:"title"`
	Description string    `json:"description"`
	Tasks       []NewTask `json:"tasks"`
}

// NewTask is one task of a new epic; BlockedBy names others by Ref.
type NewTask struct {
	Ref       string   `json:"ref"`
	Title     string   `json:"title"`
	Goal      string   `json:"goal"`
	Criteria  []string `json:"criteria"`
	BlockedBy []string `json:"blocked_by"`
}

// Asked is a clarifying question as dude keeps it: its text and choices.
type Asked struct {
	Text    string        `json:"text"`
	Choices []AskedChoice `json:"choices"`
}

// AskedChoice is one choice: where picking it routes. Truncate files only
// the first EpicTasksMax tasks of a longer epic.
type AskedChoice struct {
	ID       string `json:"id"`
	Label    string `json:"label"`
	Route    string `json:"route"`
	Project  string `json:"project,omitempty"`
	Epic     string `json:"epic,omitempty"`
	Truncate bool   `json:"truncate,omitempty"`
}

// ParseAnswer reads the routing judgement's object; a route it does not
// know, or a confidence outside 0..1, is no answer.
func ParseAnswer(obj []byte) (Answer, error) {
	var a Answer
	if err := json.Unmarshal(obj, &a); err != nil {
		return a, err
	}
	a.Route = strings.ToLower(strings.TrimSpace(a.Route))
	switch a.Route {
	case RouteBrainstorm, RouteTask, RouteEpic, RouteResume, RouteClarify:
	default:
		return a, fmt.Errorf("route %q is not one dude takes", a.Route)
	}
	if a.Confidence < 0 || a.Confidence > 1 {
		return a, fmt.Errorf("confidence %v is not between 0 and 1", a.Confidence)
	}
	a.Reasoning = clip(strings.TrimSpace(a.Reasoning), 1000)
	return a, nil
}

// Project is a project the person may file in, with its epics.
type Project struct {
	ID, Key, Name string
	Epics         []EpicRef
}

// EpicRef is an epic of a project.
type EpicRef struct{ ID, Title string }

// SessionRef is one of the person's chat-born sessions.
type SessionRef struct{ ID, Title string }

// World is what an answer is checked against: the person's projects and
// recent sessions.
type World struct {
	Projects []Project
	Sessions []SessionRef
}

func (w World) project(key string) (Project, bool) {
	key = strings.TrimSpace(key)
	for _, p := range w.Projects {
		if strings.EqualFold(p.Key, key) || strings.EqualFold(p.Name, key) || p.ID == key {
			return p, true
		}
	}
	return Project{}, false
}

func (p Project) epic(title string) (EpicRef, bool) {
	title = strings.TrimSpace(title)
	for _, e := range p.Epics {
		if strings.EqualFold(e.Title, title) || e.ID == title {
			return e, true
		}
	}
	return EpicRef{}, false
}

// Decision is what dude does with a mention after checking the answer:
// make something (Route brainstorm, task, epic, resume) or ask (clarify).
type Decision struct {
	Route string
	// What the model answered when dude went another way.
	AskedRoute string
	Project    *Project
	Epic       *EpicRef
	Session    string
	Title      string
	Goal       string
	Criteria   []string
	NewEpic    *NewEpic
	Question   *Asked
	// Why dude went another way; "" when it took the answer.
	Problem string
}

// Check is one of an answer's failures, as Decide reports it.
var errNoChoices = errors.New("no choices can be built")

// problem is why a (possibly chosen) answer cannot be made as it stands; "".
func (a Answer) problem(w World) string {
	switch a.Route {
	case RouteBrainstorm:
		if a.Project != "" {
			if _, ok := w.project(a.Project); !ok {
				return fmt.Sprintf("project %q is not one of the person's", a.Project)
			}
		}
	case RouteTask:
		p, ok := w.project(a.Project)
		if !ok {
			return fmt.Sprintf("project %q is not one of the person's", a.Project)
		}
		if a.Epic != "" {
			if _, ok := p.epic(a.Epic); !ok {
				return fmt.Sprintf("epic %q is not one of %s's", a.Epic, p.Key)
			}
		}
	case RouteEpic:
		if _, ok := w.project(a.Project); !ok {
			return fmt.Sprintf("project %q is not one of the person's", a.Project)
		}
		if a.NewEpic == nil || strings.TrimSpace(a.NewEpic.Title) == "" {
			return "the epic route names no epic"
		}
		if p := EpicTasksProblem(a.NewEpic.Tasks); p != "" {
			return p
		}
	case RouteResume:
		for _, s := range w.Sessions {
			if s.ID == a.Session {
				return ""
			}
		}
		return fmt.Sprintf("session %q is not one the person can chat in", a.Session)
	case RouteClarify:
		if a.Question == nil || len(a.Question.Choices) < 2 || len(a.Question.Choices) > 4 || strings.TrimSpace(a.Question.Text) == "" {
			return "a clarifying question has 2 to 4 choices"
		}
		for _, c := range a.Question.Choices {
			if strings.TrimSpace(c.Label) == "" {
				return "a choice has no label"
			}
			switch c.Route {
			case RouteBrainstorm, RouteTask, RouteEpic, RouteResume:
			default:
				return fmt.Sprintf("choice %q routes to %q", c.Label, c.Route)
			}
		}
	}
	return ""
}

// EpicTasksProblem is why an epic's tasks cannot be filed: none, more than
// EpicTasksMax, a missing or repeated ref, a blocked_by naming no task of
// the answer, or a cycle. "" when they can.
func EpicTasksProblem(tasks []NewTask) string {
	if len(tasks) == 0 {
		return "an epic needs at least one task"
	}
	if len(tasks) > EpicTasksMax {
		return fmt.Sprintf("an epic has at most %d tasks, not %d", EpicTasksMax, len(tasks))
	}
	edges := map[string][]string{}
	for _, t := range tasks {
		if t.Ref == "" || edges[t.Ref] != nil || strings.TrimSpace(t.Title) == "" {
			return fmt.Sprintf("task %q has no title, or its ref is missing or repeated", t.Ref)
		}
		edges[t.Ref] = append([]string{}, t.BlockedBy...)
	}
	for _, t := range tasks {
		for _, b := range t.BlockedBy {
			if _, ok := edges[b]; !ok {
				return fmt.Sprintf("task %s is blocked by %q, no task of the epic", t.Ref, b)
			}
		}
	}
	state := map[string]int{}
	var visit func(string) bool
	visit = func(ref string) bool {
		switch state[ref] {
		case 1:
			return true
		case 2:
			return false
		}
		state[ref] = 1
		for _, b := range edges[ref] {
			if visit(b) {
				return true
			}
		}
		state[ref] = 2
		return false
	}
	for _, t := range tasks {
		if visit(t.Ref) {
			return "the tasks' blocked_by form a cycle"
		}
	}
	return ""
}

// Decide checks an answer and says what dude does: the answer as it came,
// or, when a check fails or (unless the person chose it) the confidence is
// under ConfidenceBar, a clarifying question while rounds are left and
// choices can be built, else a brainstorm.
func Decide(a Answer, w World, rounds int, chosen bool, text string) Decision {
	problem := a.problem(w)
	if problem == "" && !chosen && a.Confidence < ConfidenceBar && a.Route != RouteBrainstorm {
		problem = fmt.Sprintf("%.0f%% sure, under the %.0f%% bar", a.Confidence*100, ConfidenceBar*100)
	}
	if problem == "" {
		return made(a, w, text)
	}
	d := Decision{Route: RouteBrainstorm, AskedRoute: a.Route, Problem: problem, Title: a.Title}
	if p, ok := w.project(a.Project); ok {
		d.Project = &p
	}
	if rounds < ClarifyRounds {
		if q, err := choices(a, w); err == nil {
			d.Route, d.Question = RouteClarify, q
		}
	}
	return d
}

// made is a checked answer as a decision.
func made(a Answer, w World, text string) Decision {
	d := Decision{Route: a.Route, Title: strings.TrimSpace(a.Title), Goal: strings.TrimSpace(a.Goal), Criteria: a.Criteria,
		Session: a.Session, NewEpic: a.NewEpic, Question: a.Question}
	if p, ok := w.project(a.Project); ok {
		d.Project = &p
		if e, ok := p.epic(a.Epic); ok && a.Route == RouteTask {
			d.Epic = &e
		}
	}
	if d.Route == RouteTask {
		if d.Title == "" {
			d.Title = clip(trimmed(text), 80)
		}
		if d.Goal == "" {
			d.Goal = text
		}
	}
	return d
}

// choices are a clarifying question for an answer that could not be made
// as it stood: the model's own question when it is a good one; for an epic
// over the cap, the first 12 or a talk; for a project dude does not know,
// the person's projects; under the bar, the route it guessed or a session.
// Every set ends with a session for the person, and has 2 to 4 choices.
func choices(a Answer, w World) (*Asked, error) {
	session := AskedChoice{ID: "session", Label: "A session for me", Route: RouteBrainstorm}
	if a.Route == RouteClarify && a.problem(w) == "" {
		return a.Question, nil
	}
	if a.Route == RouteEpic && a.NewEpic != nil && len(a.NewEpic.Tasks) > EpicTasksMax {
		if _, ok := w.project(a.Project); ok {
			return &Asked{Text: fmt.Sprintf("That is %d tasks. File the first %d, or talk it through?", len(a.NewEpic.Tasks), EpicTasksMax),
				Choices: []AskedChoice{{ID: "first", Label: fmt.Sprintf("File the first %d", EpicTasksMax), Route: RouteEpic,
					Project: a.Project, Truncate: true}, {ID: "talk", Label: "Talk it through", Route: RouteBrainstorm}}}, nil
		}
	}
	if (a.Route == RouteTask || a.Route == RouteEpic) && (a.Route != RouteEpic || a.NewEpic != nil) {
		what := "the task"
		if a.Route == RouteEpic {
			what = "the epic"
		}
		if p, ok := w.project(a.Project); ok {
			if a.problem(w) != "" {
				return nil, errNoChoices
			}
			return &Asked{Text: fmt.Sprintf("Should I file %s in %s, or start a session?", what, p.Key),
				Choices: []AskedChoice{{ID: "file", Label: "File it in " + p.Key, Route: a.Route, Project: p.Key}, session}}, nil
		}
		var cs []AskedChoice
		for _, p := range w.Projects {
			if len(cs) == 3 {
				break
			}
			cs = append(cs, AskedChoice{ID: p.Key, Label: p.Key, Route: a.Route, Project: p.Key})
		}
		if len(cs) == 0 {
			return nil, errNoChoices
		}
		return &Asked{Text: fmt.Sprintf("Which project should %s go in?", what), Choices: append(cs, session)}, nil
	}
	return nil, errNoChoices
}

// Choose applies a picked choice to the answer the question was asked
// about: its route, project and epic, and the cap when it truncates.
func Choose(a Answer, c AskedChoice) Answer {
	a.Route = c.Route
	if c.Project != "" {
		a.Project = c.Project
	}
	if c.Epic != "" {
		a.Epic = c.Epic
	}
	if c.Truncate && a.NewEpic != nil && len(a.NewEpic.Tasks) > EpicTasksMax {
		kept := map[string]bool{}
		tasks := append([]NewTask{}, a.NewEpic.Tasks[:EpicTasksMax]...)
		for _, t := range tasks {
			kept[t.Ref] = true
		}
		for i := range tasks {
			var by []string
			for _, b := range tasks[i].BlockedBy {
				if kept[b] {
					by = append(by, b)
				}
			}
			tasks[i].BlockedBy = by
		}
		epic := *a.NewEpic
		epic.Tasks = tasks
		a.NewEpic = &epic
	}
	a.Confidence = 1
	return a
}
