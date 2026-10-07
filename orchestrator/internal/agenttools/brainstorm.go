package agenttools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/db"
	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/ids"
	"github.com/marciomartins/dude/orchestrator/internal/ledger"
	"github.com/marciomartins/dude/orchestrator/internal/memory"
)

// A session's agent (role brainstorm) has these tools and no others: it
// reads the projects linked to its session, proposes work for a member to
// file, remembers, and asks. Each is scoped by the Run's session, never a
// task; one naming a project refuses any not linked to the session now.
// Nothing here creates, edits or comments on work, starts or steers a Run,
// or decides anything: that is read-only by the tool list, not by the
// instructions.
var brainstorms = []string{delivery.RoleBrainstorm}

var brainstormTools = []tool{
	define("list_tasks", "A linked project's epics and tasks, with their keys (like BL-12), status and who asked for "+
		"them — optionally only those mentioning some text.", brainstorms, sessionListTasks),
	define("list_epics", "A linked project's epics, in priority order, with how many tasks each has and how many are "+
		"still open.", brainstorms, sessionListEpics),
	define("list_repositories", "A linked project's repositories: which are checked out for this session (read only, "+
		"and where), and which are not.", brainstorms, sessionListRepositories),
	define("pull_requests", "A linked project's pull requests: with task, that task's in full (checks, reviews, "+
		"feedback); without, the project's latest, one line each.", brainstorms, sessionPullRequests),
	define("findings", "Review findings in a linked project: with task, that task's (name ids to read some in full); "+
		"without, the project's open findings, one line each.", brainstorms, sessionFindings),
	define("search_memory", "Search what is known in a linked project — memories, tasks, epics — and the "+
		"organisation's own memories, by words and by meaning, best first.", brainstorms, sessionSearchMemory).before(embedQuery),
	define("get_memory", "Read one memory in full, by the id search_memory gave.", brainstorms, sessionGetMemory),
	define("remember", "Save something worth knowing next time, for a linked project (or, scope organization, for "+
		"everyone): a fact that holds, a procedure that works, a trap and its way around. Search first.",
		brainstorms, sessionRemember).limit(remembersPerRun),
	define("propose", "Put work on the session's proposal card, for a member to file with a click, as themselves: "+
		"new epics and tasks in linked projects, edits to a task's goal or criteria (before/after), comments on a task. "+
		"You never create, edit or comment yourself. A new proposal replaces the card.",
		brainstorms, propose).limit(createsPerRun),
	define("ask_person", "Ask the session's members something only they can decide, then end your turn: the answer is "+
		"your next message. With to (a member's name), only that member can answer; what others say meanwhile reaches "+
		"you with the answer.", brainstorms, sessionAsk),
}

// linkedProject is the project a tool names, by key: one linked to the
// session now; with none named, the only one linked.
func linkedProject(ctx context.Context, tx pgx.Tx, c Caller, key string) (delivery.LinkedProject, error) {
	projects, err := delivery.SessionProjects(ctx, tx, c.SessionID)
	if err != nil {
		return delivery.LinkedProject{}, err
	}
	key = strings.TrimSpace(key)
	if key == "" {
		switch len(projects) {
		case 0:
			return delivery.LinkedProject{}, refuse("no project is linked to this session yet: a member links one")
		case 1:
			return projects[0], nil
		}
		keys := make([]string, len(projects))
		for i, p := range projects {
			keys[i] = p.Key
		}
		return delivery.LinkedProject{}, refuse("name a project: %s", strings.Join(keys, ", "))
	}
	for _, p := range projects {
		if strings.EqualFold(p.Key, key) || strings.EqualFold(p.Name, key) {
			return p, nil
		}
	}
	return delivery.LinkedProject{}, refuse("project %s is not linked to this session", key)
}

// inProject is the caller as a tool for one project sees it.
func inProject(c Caller, projectID string) Caller {
	c.ProjectID, c.TaskID = projectID, ""
	return c
}

type projectIn struct {
	Project string `json:"project,omitempty" jsonschema:"a linked project's key (like BL); optional when only one is linked"`
}

type sessionListTasksIn struct {
	Project string `json:"project,omitempty" jsonschema:"a linked project's key (like BL); optional when only one is linked"`
	Text    string `json:"text,omitempty" jsonschema:"only tasks whose title or goal mention this"`
}

func sessionListTasks(ctx context.Context, tx pgx.Tx, c Caller, in sessionListTasksIn) (listTasksOut, error) {
	p, err := linkedProject(ctx, tx, c, in.Project)
	if err != nil {
		return listTasksOut{}, err
	}
	return listTasks(ctx, tx, inProject(c, p.ID), listTasksIn{Text: in.Text})
}

func sessionListEpics(ctx context.Context, tx pgx.Tx, c Caller, in projectIn) ([]epicOut, error) {
	p, err := linkedProject(ctx, tx, c, in.Project)
	if err != nil {
		return nil, err
	}
	return listEpics(ctx, tx, inProject(c, p.ID), listEpicsIn{})
}

type sessionRepoOut struct {
	Name          string `json:"name"`
	DefaultBranch string `json:"defaultBranch"`
	// Where it is checked out, read only; "" when it is not.
	Path string `json:"path,omitempty"`
}

func sessionListRepositories(ctx context.Context, tx pgx.Tx, c Caller, in projectIn) ([]sessionRepoOut, error) {
	p, err := linkedProject(ctx, tx, c, in.Project)
	if err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, `SELECT r.name, r.default_branch,
			CASE WHEN EXISTS (SELECT 1 FROM session_repositories sr WHERE sr.session_id = $2 AND sr.repository_id = r.id)
			THEN '/workspace/repos/' || $3 || '/' || r.name ELSE '' END
		FROM repositories r WHERE r.project_id = $1 ORDER BY r.name`, p.ID, c.SessionID, p.Key)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[sessionRepoOut])
}

// taskIn names a task of a linked project.
type taskIn struct {
	Project string   `json:"project,omitempty" jsonschema:"a linked project's key (like BL); optional when only one is linked"`
	Task    string   `json:"task,omitempty" jsonschema:"a task's key (like BL-58) in that project"`
	IDs     []string `json:"ids,omitempty" jsonschema:"findings: with task, those to read in full (fnd_…)"`
}

// taskOf is the task a tool names in a linked project, as a caller of the
// task agents' tools would be.
func taskOf(ctx context.Context, tx pgx.Tx, c Caller, in taskIn) (Caller, delivery.LinkedProject, bool, error) {
	p, err := linkedProject(ctx, tx, c, in.Project)
	if err != nil {
		return c, p, false, err
	}
	if strings.TrimSpace(in.Task) == "" {
		return inProject(c, p.ID), p, false, nil
	}
	id, _, err := delivery.TaskByKey(ctx, tx, []string{p.ID}, in.Task)
	if err != nil {
		return c, p, false, err
	}
	if id == "" {
		return c, p, false, refuse("no task %s in %s", in.Task, p.Key)
	}
	scoped := inProject(c, p.ID)
	scoped.TaskID = id
	return scoped, p, true, nil
}

func sessionPullRequests(ctx context.Context, tx pgx.Tx, c Caller, in taskIn) (any, error) {
	scoped, p, one, err := taskOf(ctx, tx, c, in)
	if err != nil {
		return nil, err
	}
	if one {
		return pullRequests(ctx, tx, scoped, pullRequestsIn{})
	}
	rows, err := tx.Query(ctx, `SELECT repo.name, pr.number, pr.state::text, pr.checks::text, pr.review::text,
			COALESCE($2 || '-' || t.number, '')
		FROM pull_requests pr JOIN repositories repo ON repo.id = pr.repository_id LEFT JOIN tasks t ON t.id = pr.task_id
		WHERE repo.project_id = $1 ORDER BY pr.updated_at DESC LIMIT 50`, p.ID, p.Key)
	if err != nil {
		return nil, err
	}
	type line struct {
		Repo   string `json:"repo"`
		Number int    `json:"number"`
		State  string `json:"state"`
		Checks string `json:"checks"`
		Review string `json:"review"`
		Task   string `json:"task,omitempty"`
	}
	prs, err := pgx.CollectRows(rows, pgx.RowToStructByPos[line])
	return map[string]any{"pullRequests": db.NonNil(prs)}, err
}

func sessionFindings(ctx context.Context, tx pgx.Tx, c Caller, in taskIn) (any, error) {
	scoped, p, one, err := taskOf(ctx, tx, c, in)
	if err != nil {
		return nil, err
	}
	if one {
		return findings(ctx, tx, scoped, findingsIn{IDs: in.IDs})
	}
	if len(in.IDs) > 0 {
		return nil, refuse("name the task to read findings in full")
	}
	rows, err := tx.Query(ctx, `SELECT $2 || '-' || t.number, f.id, f.severity::text, f.category, f.status::text
		FROM review_findings f JOIN tasks t ON t.id = f.task_id
		WHERE t.project_id = $1 AND f.status = 'open'
		ORDER BY array_position(ARRAY['blocking','high','medium','low','note'], f.severity::text), f.created_at LIMIT $3`,
		p.ID, p.Key, findingsMax)
	if err != nil {
		return nil, err
	}
	type line struct {
		Task     string `json:"task"`
		ID       string `json:"id"`
		Severity string `json:"severity"`
		Category string `json:"category"`
		Status   string `json:"status"`
	}
	fs, err := pgx.CollectRows(rows, pgx.RowToStructByPos[line])
	return map[string]any{"findings": db.NonNil(fs)}, err
}

type sessionSearchIn struct {
	Project string   `json:"project,omitempty" jsonschema:"a linked project's key; optional when only one is linked"`
	Query   string   `json:"query" jsonschema:"what you want to know, in words"`
	Types   []string `json:"types,omitempty" jsonschema:"only these: memory, task, epic, project (default all)"`
	Limit   int      `json:"limit,omitempty" jsonschema:"how many results, 1 to 20 (default 8)"`
}

// sessionSearchMemory searches a linked project and its organisation; with
// nothing linked, the organisation's own memories only.
func sessionSearchMemory(ctx context.Context, tx pgx.Tx, c Caller, in sessionSearchIn) ([]searchHit, error) {
	scope := memory.Query{OrgOnly: true}
	projects, err := delivery.SessionProjects(ctx, tx, c.SessionID)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(in.Project) != "" || len(projects) > 0 {
		p, err := linkedProject(ctx, tx, c, in.Project)
		if err != nil {
			return nil, err
		}
		scope = memory.Query{Project: p.ID}
	}
	return searchScoped(ctx, tx, c, searchMemoryIn{Query: in.Query, Types: in.Types, Limit: in.Limit}, scope)
}

func sessionGetMemory(ctx context.Context, tx pgx.Tx, c Caller, in getMemoryIn) (memory.Memory, error) {
	m, err := memory.Get(ctx, tx, strings.TrimSpace(in.ID), memory.Viewer{Session: c.SessionID})
	if err != nil {
		return memory.Memory{}, refuse("no memory %s", in.ID)
	}
	if m.ProjectID != "" {
		projects, err := delivery.SessionProjects(ctx, tx, c.SessionID)
		if err != nil {
			return memory.Memory{}, err
		}
		if !slices.ContainsFunc(projects, func(p delivery.LinkedProject) bool { return p.ID == m.ProjectID }) {
			return memory.Memory{}, refuse("no memory %s", in.ID)
		}
	}
	c.ProjectID = m.ProjectID
	return getMemory(ctx, tx, c, in)
}

// sessionRememberIn has no scope or project: what a session's agent
// remembers is the session's alone (its members, its agent), never the
// organisation's or a project's — that would export a private
// conversation without anyone choosing to.
type sessionRememberIn struct {
	Title   string   `json:"title" jsonschema:"one line that says what it is"`
	Content string   `json:"content" jsonschema:"the fact, procedure or note, in Markdown"`
	Kind    string   `json:"kind,omitempty" jsonschema:"fact (default), procedure or note"`
	About   []string `json:"about,omitempty" jsonschema:"task keys in a linked project, or epic titles there (project:Title when more than one is linked)"`
}

func sessionRemember(ctx context.Context, tx pgx.Tx, c Caller, in sessionRememberIn) (rememberOut, error) {
	var about []memory.Ref
	for _, a := range in.About {
		ref, err := sessionAbout(ctx, tx, c, a)
		if err != nil {
			return rememberOut{}, err
		}
		about = append(about, ref)
	}
	m, err := memory.Create(ctx, tx, c.Org, memory.New{SessionID: c.SessionID, Title: in.Title, Content: in.Content, Kind: in.Kind,
		About: about, Author: memory.Author{Kind: "agent", RunID: c.RunID}},
		memory.Actor{Type: ledger.ActorAgent, ID: c.RunID, RunID: c.RunID})
	var bad *memory.Invalid
	if errors.As(err, &bad) {
		return rememberOut{}, refuse("%s", bad.Reason)
	}
	if err != nil {
		return rememberOut{}, err
	}
	if c.env.kick != nil {
		c.env.kick()
	}
	return rememberOut{ID: m.ID}, nil
}

// sessionAbout resolves what a session memory is about: a task by its key
// in a linked project, or an epic by title ("KEY:Title" when more than one
// project is linked).
func sessionAbout(ctx context.Context, tx pgx.Tx, c Caller, name string) (memory.Ref, error) {
	name = strings.TrimSpace(name)
	projects, err := delivery.SessionProjects(ctx, tx, c.SessionID)
	if err != nil {
		return memory.Ref{}, err
	}
	ids := make([]string, len(projects))
	for i, p := range projects {
		ids[i] = p.ID
	}
	if task, _, err := delivery.TaskByKey(ctx, tx, ids, name); err != nil || task != "" {
		return memory.Ref{Type: "task", ID: task}, err
	}
	key, title, named := strings.Cut(name, ":")
	if !named {
		key, title = "", name
	}
	p, err := linkedProject(ctx, tx, c, key)
	if err != nil {
		return memory.Ref{}, err
	}
	if id, err := epicByTitle(ctx, tx, p.ID, strings.TrimSpace(title)); err != nil || id != "" {
		return memory.Ref{Type: "epic", ID: id}, err
	}
	return memory.Ref{}, refuse("%q is not a task or epic of the linked projects", name)
}

type proposeIn struct {
	Items []delivery.ProposalItem `json:"items" jsonschema:"the card's items in order: {kind: epic, project, title, description}; {kind: task, project, epic?, title, goal, acceptanceCriteria}; {kind: edit, task, after: {goal?, acceptanceCriteria?}}; {kind: comment, task, text}"`
}

type proposeOut struct {
	ProposalID string `json:"proposalId"`
	Next       string `json:"next"`
}

// propose fills the session's proposal card. Each item is checked as a
// person's own would be (create_task's limits), against the projects linked
// now; who may file it is the card's to say, for whoever presses File.
func propose(ctx context.Context, tx pgx.Tx, c Caller, in proposeIn) (proposeOut, error) {
	if len(in.Items) == 0 || len(in.Items) > 30 {
		return proposeOut{}, refuse("propose 1 to 30 items")
	}
	projects, err := delivery.SessionProjects(ctx, tx, c.SessionID)
	if err != nil {
		return proposeOut{}, err
	}
	linkedIDs := make([]string, len(projects))
	for i, p := range projects {
		linkedIDs[i] = p.ID
	}
	newEpics := map[string]bool{}
	items := make([]delivery.ProposalItem, len(in.Items))
	for i, item := range in.Items {
		n := i + 1
		item.Kind = strings.TrimSpace(item.Kind)
		item.Title = strings.TrimSpace(item.Title)
		switch item.Kind {
		case "epic", "task":
			p, err := linkedProject(ctx, tx, c, item.Project)
			if err != nil {
				return proposeOut{}, refuse("item %d: %v", n, err)
			}
			item.Project = p.Key
			if item.Title == "" || len(item.Title) > 500 {
				return proposeOut{}, refuse("item %d: a title of 1 to 500 characters", n)
			}
			if item.Kind == "epic" {
				if len(item.Title) > 200 || len(item.Description) > 10_000 {
					return proposeOut{}, refuse("item %d: an epic's title is at most 200 characters, its description 10,000", n)
				}
				newEpics[strings.ToLower(p.Key+"/"+item.Title)] = true
				break
			}
			if err := checkTaskText(&item.Goal, &item.AcceptanceCriteria, true); err != nil {
				return proposeOut{}, refuse("item %d: %v", n, err)
			}
			if e := strings.TrimSpace(item.Epic); e != "" && !newEpics[strings.ToLower(p.Key+"/"+e)] {
				id, err := epicByTitle(ctx, tx, p.ID, e)
				if err != nil {
					return proposeOut{}, err
				}
				if id == "" {
					return proposeOut{}, refuse("item %d: no epic %q in %s, and none proposed before it", n, e, p.Key)
				}
			}
		case "edit", "comment":
			id, _, err := delivery.TaskByKey(ctx, tx, linkedIDs, item.Task)
			if err != nil {
				return proposeOut{}, err
			}
			if id == "" {
				return proposeOut{}, refuse("item %d: %s is not a task of a linked project", n, item.Task)
			}
			item.Task = strings.ToUpper(strings.TrimSpace(item.Task))
			if item.Kind == "comment" {
				item.Text = strings.TrimSpace(item.Text)
				if item.Text == "" || len(item.Text) > delivery.ChatMessageMax {
					return proposeOut{}, refuse("item %d: a comment of 1 to %d bytes", n, delivery.ChatMessageMax)
				}
				break
			}
			if item.After == nil || item.After.Goal == nil && item.After.AcceptanceCriteria == nil {
				return proposeOut{}, refuse("item %d: an edit gives after: a new goal, new acceptance criteria, or both", n)
			}
			var crit []string
			if item.After.AcceptanceCriteria != nil {
				crit = *item.After.AcceptanceCriteria
			}
			goal := ""
			if item.After.Goal != nil {
				goal = *item.After.Goal
			}
			if err := checkTaskText(&goal, &crit, item.After.Goal != nil); err != nil {
				return proposeOut{}, refuse("item %d: %v", n, err)
			}
			// The card shows the task as it is now beside the edit.
			var nowGoal string
			var raw []byte
			if err := tx.QueryRow(ctx, `SELECT goal, acceptance_criteria FROM tasks WHERE id = $1`, id).Scan(&nowGoal, &raw); err != nil {
				return proposeOut{}, err
			}
			var nowCrit []string
			_ = json.Unmarshal(raw, &nowCrit)
			item.Before = &delivery.TaskText{Goal: &nowGoal, AcceptanceCriteria: &nowCrit}
		default:
			return proposeOut{}, refuse("item %d: kind is epic, task, edit or comment", n)
		}
		items[i] = item
	}
	id, err := delivery.RecordProposal(ctx, tx, c.run(), items)
	return proposeOut{ProposalID: id, Next: "The card is in the session. A member files what they keep, as themselves; " +
		"you are not told unless they say so."}, err
}

// checkTaskText holds a goal and criteria to what a person may save
// (create_task's rules): the goal, when given, at least GoalMin.
func checkTaskText(goal *string, criteria *[]string, goalGiven bool) error {
	switch {
	case goalGiven && utf16Len(strings.TrimFunc(*goal, isJSSpace)) < GoalMin:
		return fmt.Errorf("a goal of at least %d characters: say why it matters and what should change", GoalMin)
	case utf16Len(*goal) > GoalMax || len(*criteria) > 50:
		return fmt.Errorf("too long: a goal of at most %d characters, at most 50 criteria", GoalMax)
	case criteriaLength(*criteria) > CriteriaMax:
		return fmt.Errorf("acceptance criteria too long: at most %d characters in all", CriteriaMax)
	}
	*goal = strings.TrimSpace(*goal)
	return nil
}

type sessionAskIn struct {
	Question string   `json:"question" jsonschema:"what you need decided, with enough context to answer it"`
	Choices  []string `json:"choices,omitempty" jsonschema:"answers to offer"`
	To       string   `json:"to,omitempty" jsonschema:"a member's name: only they answer; others' messages meanwhile come with the answer"`
}

// sessionAsk records a question for the session's members, or one member
// who can chat. The turn ends; the Run waits for the answer.
func sessionAsk(ctx context.Context, tx pgx.Tx, c Caller, in sessionAskIn) (askOut, error) {
	q := strings.TrimSpace(in.Question)
	switch {
	case q == "":
		return askOut{}, refuse("a question is required")
	case len(q) > 4000 || len(in.Choices) > 10:
		return askOut{}, refuse("too long: a question of at most 4000 characters, at most 10 choices")
	}
	open, err := delivery.HasOpenQuestion(ctx, tx, c.RunID)
	if err != nil {
		return askOut{}, err
	}
	if open {
		return askOut{}, refuse("you already have a question waiting for an answer: end your turn and wait for it")
	}
	to, toName := "", ""
	if name := strings.TrimSpace(in.To); name != "" {
		rows, err := tx.Query(ctx, `SELECT p.id, p.name FROM session_people sp JOIN people p ON p.id = sp.person_id
			WHERE sp.session_id = $1 AND sp.accepted_at IS NOT NULL AND sp.role IN ('owner', 'chat')
			  AND (p.id = $2 OR lower(p.name) = lower($2) OR lower(split_part(p.name, ' ', 1)) = lower($2))`, c.SessionID, name)
		if err != nil {
			return askOut{}, err
		}
		found, err := pgx.CollectRows(rows, pgx.RowToStructByPos[struct{ ID, Name string }])
		if err != nil {
			return askOut{}, err
		}
		if len(found) != 1 {
			return askOut{}, refuse("to names one member who can chat; %q is %d of them", name, len(found))
		}
		to, toName = found[0].ID, found[0].Name
	}
	id := ids.New(ids.Question)
	opts, _ := json.Marshal(db.NonNil(in.Choices))
	if _, err := tx.Exec(ctx, `INSERT INTO questions (id, organization_id, run_id, prompt, options, to_person)
		VALUES ($1, $2, $3, $4, $5::jsonb, NULLIF($6, ''))`, id, c.Org, c.RunID, q, opts, to); err != nil {
		return askOut{}, err
	}
	payload := map[string]any{"kind": "agent", "questionId": id, "prompt": q, "options": db.NonNil(in.Choices)}
	if to != "" {
		payload["to"], payload["toName"] = to, toName
	}
	if _, err := ledger.Append(ctx, tx, c.event(delivery.EvQuestionAsked, payload)); err != nil {
		return askOut{}, err
	}
	next := "End your turn now. The answer will be your next message."
	if to != "" {
		next = "End your turn now. Only " + toName + " answers it; what others say meanwhile comes with the answer."
	}
	return askOut{QuestionID: id, Next: next}, nil
}
