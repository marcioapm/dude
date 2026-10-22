package agenttools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

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
// instructions. The names its capabilities grant are answered by these
// definitions where one exists (find).
var brainstormTools = []tool{
	defineSession("list_tasks", sessionListTasks),
	defineSession("list_epics", sessionListEpics),
	defineSession("list_repositories", sessionListRepositories),
	defineSession("pull_requests", sessionPullRequests),
	defineSession("findings", sessionFindings),
	defineSession("search_memory", sessionSearchMemory).before(embedQuery),
	defineSession("get_memory", sessionGetMemory),
	defineSession("remember", sessionRemember).limit(remembersPerRun),
	defineSession("propose", propose).limit(createsPerRun),
	defineSession("relate", relate).limit(createsPerRun),
	defineSession("ask_person", sessionAsk),
	defineSession("name_session", nameSession).limit(namesPerRun),
	defineSession("refresh_checkout", refreshCheckout).limit(checkoutsPerRun),
	defineSession("start_agent", startAgent),
}

// namesPerRun bounds how often one Run renames its session.
const namesPerRun = 20

type nameSessionIn struct {
	Title string `json:"title"`
}

type nameSessionOut struct {
	Title string `json:"title"`
}

// nameSession names the agent's session, unless a person has: their title
// wins from then on, and the agent is told so.
func nameSession(ctx context.Context, tx pgx.Tx, c Caller, in nameSessionIn) (nameSessionOut, error) {
	title := delivery.OneLineTitle(in.Title)
	if title == "" || utf8.RuneCountInString(title) > delivery.AgentTitleMax {
		return nameSessionOut{}, refuse(rTitle1CharactersOnOneLine, delivery.AgentTitleMax)
	}
	_, err := delivery.RenameSession(ctx, tx, c.run(), title, delivery.ByAgent, ledger.ActorAgent, c.RunID)
	if errors.Is(err, delivery.ErrNamedByPerson) {
		return nameSessionOut{}, refuseText(rMemberNamedThisSessionTheirName)
	}
	return nameSessionOut{Title: title}, err
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
			return delivery.LinkedProject{}, refuseText(rNoProjectLinkedThisSessionYet)
		case 1:
			return projects[0], nil
		}
		keys := make([]string, len(projects))
		for i, p := range projects {
			keys[i] = p.Key
		}
		return delivery.LinkedProject{}, refuse(rNameProject, strings.Join(keys, ", "))
	}
	for _, p := range projects {
		if strings.EqualFold(p.Key, key) || strings.EqualFold(p.Name, key) {
			return p, nil
		}
	}
	return delivery.LinkedProject{}, refuse(rProjectNotLinkedThisSession, key)
}

// orgProject is the project of the session's organisation a link_project
// item names, by key or name, linked or not, among projects (OrgProjects:
// row security keeps any other organisation's out of the read).
func orgProject(projects []delivery.OrgProject, key string) (delivery.OrgProject, error) {
	key = strings.TrimSpace(key)
	var unlinked []string
	for _, p := range projects {
		if key != "" && (strings.EqualFold(p.Key, key) || strings.EqualFold(p.Name, key)) {
			return p, nil
		}
		if !p.Linked {
			unlinked = append(unlinked, p.Key)
		}
	}
	list := strings.Join(unlinked, ", ")
	if list == "" {
		list = "all linked already"
	}
	return delivery.OrgProject{}, refuse(rNoProjectInOrganisation, key, list)
}

// inProject is the caller as a tool for one project sees it.
func inProject(c Caller, projectID string) Caller {
	c.ProjectID, c.TaskID = projectID, ""
	return c
}

type projectIn struct {
	Project string `json:"project,omitempty"`
}

type sessionListTasksIn struct {
	Project string `json:"project,omitempty"`
	Text    string `json:"text,omitempty"`
}

func sessionListTasks(ctx context.Context, tx pgx.Tx, c Caller, in sessionListTasksIn) (listTasksOut, error) {
	p, err := linkedProject(ctx, tx, c, in.Project)
	if err != nil {
		return listTasksOut{}, err
	}
	return projectTasks(ctx, tx, inProject(c, p.ID), listTasksIn{Text: in.Text})
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
	Project string   `json:"project,omitempty"`
	Task    string   `json:"task,omitempty"`
	IDs     []string `json:"ids,omitempty"`
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
		return c, p, false, refuse(rNoTaskIn, in.Task, p.Key)
	}
	scoped := inProject(c, p.ID)
	scoped.TaskID = id
	return scoped, p, true, nil
}

type sessionPullRequestsIn struct {
	taskIn
	Feedback []string `json:"feedback,omitempty"`
}

func sessionPullRequests(ctx context.Context, tx pgx.Tx, c Caller, in sessionPullRequestsIn) (any, error) {
	scoped, p, one, err := taskOf(ctx, tx, c, in.taskIn)
	if err != nil {
		return nil, err
	}
	if one {
		return pullRequests(ctx, tx, scoped, pullRequestsIn{Feedback: in.Feedback})
	}
	if len(in.Feedback) > 0 {
		return nil, refuseText(rNameTaskReadFeedbackInFull)
	}
	rows, err := tx.Query(ctx, `SELECT repo.name, pr.number, pr.state::text, pr.checks::text, pr.review::text,
			COALESCE($2 || '-' || t.number, ''), pr.last_polled_at
		FROM pull_requests pr JOIN repositories repo ON repo.id = pr.repository_id LEFT JOIN tasks t ON t.id = pr.task_id
		WHERE repo.project_id = $1 ORDER BY pr.updated_at DESC LIMIT 50`, p.ID, p.Key)
	if err != nil {
		return nil, err
	}
	type line struct {
		Repo         string     `json:"repo"`
		Number       int        `json:"number"`
		State        string     `json:"state"`
		Checks       string     `json:"checks"`
		Review       string     `json:"review"`
		Task         string     `json:"task,omitempty"`
		LastPolledAt *time.Time `json:"lastPolledAt"`
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
		return nil, refuseText(rNameTaskReadFindingsInFull)
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
	Project string   `json:"project,omitempty"`
	Query   string   `json:"query"`
	Types   []string `json:"types,omitempty"`
	Limit   int      `json:"limit,omitempty"`
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

func sessionGetMemory(ctx context.Context, tx pgx.Tx, c Caller, in getMemoryIn) (any, error) {
	if id := strings.TrimSpace(in.ID); strings.HasPrefix(id, docPrefix) {
		return getDoc(ctx, tx, c, id)
	}
	m, err := memory.Get(ctx, tx, strings.TrimSpace(in.ID), memory.Viewer{Session: c.SessionID})
	if err != nil {
		return memory.Memory{}, refuse(rNoMemory, in.ID)
	}
	if m.ProjectID != "" {
		projects, err := delivery.SessionProjects(ctx, tx, c.SessionID)
		if err != nil {
			return memory.Memory{}, err
		}
		if !slices.ContainsFunc(projects, func(p delivery.LinkedProject) bool { return p.ID == m.ProjectID }) {
			return memory.Memory{}, refuse(rNoMemory, in.ID)
		}
	}
	c.ProjectID = m.ProjectID
	return getOneMemory(ctx, tx, c, in)
}

// sessionRememberIn has no scope or project: what a session's agent
// remembers is the session's alone (its members, its agent), never the
// organisation's or a project's — that would export a private
// conversation without anyone choosing to.
type sessionRememberIn struct {
	Title   string   `json:"title"`
	Content string   `json:"content"`
	Kind    string   `json:"kind,omitempty"`
	About   []string `json:"about,omitempty"`
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
	return memory.Ref{}, refuse(rNotTaskOrEpicLinkedProjects, name)
}

type proposeIn struct {
	Items []delivery.ProposalItem `json:"items"`
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
		return proposeOut{}, refuseText(rPropose130Items)
	}
	projects, err := delivery.SessionProjects(ctx, tx, c.SessionID)
	if err != nil {
		return proposeOut{}, err
	}
	linkedIDs := make([]string, len(projects))
	for i, p := range projects {
		linkedIDs[i] = p.ID
	}
	// The organisation's projects, read once for every link_project item.
	var orgProjects []delivery.OrgProject
	if slices.ContainsFunc(in.Items, func(it delivery.ProposalItem) bool { return strings.TrimSpace(it.Kind) == "link_project" }) {
		if orgProjects, err = delivery.OrgProjects(ctx, tx, c.SessionID); err != nil {
			return proposeOut{}, err
		}
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
				return proposeOut{}, refuse(rItem, n, err)
			}
			item.Project = p.Key
			if item.Title == "" || len(item.Title) > 500 {
				return proposeOut{}, refuse(rItemTitle1500Characters, n)
			}
			if item.Kind == "epic" {
				if len(item.Title) > 200 || len(item.Description) > 10_000 {
					return proposeOut{}, refuse(rItemEpicTitleAtMost200, n)
				}
				if len(item.Notes) > delivery.NotesMax {
					return proposeOut{}, refuse(rItemEpicHoldsAtMostNotes, n, delivery.NotesMax)
				}
				for j, text := range item.Notes {
					if item.Notes[j] = strings.TrimSpace(text); item.Notes[j] == "" || utf8.RuneCountInString(item.Notes[j]) > delivery.NoteTextMax {
						return proposeOut{}, refuse(rItemNote1Characters, n, j+1, delivery.NoteTextMax)
					}
				}
				newEpics[strings.ToLower(p.Key+"/"+item.Title)] = true
				break
			}
			if len(item.Notes) > 0 {
				return proposeOut{}, refuse(rItemNotesGoOnEpicOr, n)
			}
			if err := checkTaskText(&item.Goal, &item.AcceptanceCriteria, true); err != nil {
				return proposeOut{}, refuse(rItem, n, err)
			}
			if e := strings.TrimSpace(item.Epic); e != "" && !newEpics[strings.ToLower(p.Key+"/"+e)] {
				id, err := epicByTitle(ctx, tx, p.ID, e)
				if err != nil {
					return proposeOut{}, err
				}
				if id == "" {
					return proposeOut{}, refuse(rItemNoEpicInAndNone, n, e, p.Key)
				}
			}
		case "edit", "comment":
			id, _, err := delivery.TaskByKey(ctx, tx, linkedIDs, item.Task)
			if err != nil {
				return proposeOut{}, err
			}
			if id == "" {
				return proposeOut{}, refuse(rItemNotTaskLinkedProject, n, item.Task)
			}
			item.Task = strings.ToUpper(strings.TrimSpace(item.Task))
			if item.Kind == "comment" {
				item.Text = strings.TrimSpace(item.Text)
				if item.Text == "" || len(item.Text) > delivery.ChatMessageMax {
					return proposeOut{}, refuse(rItemComment1Bytes, n, delivery.ChatMessageMax)
				}
				break
			}
			if item.After == nil || item.After.Goal == nil && item.After.AcceptanceCriteria == nil {
				return proposeOut{}, refuse(rItemEditGivesAfterNewGoal, n)
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
				return proposeOut{}, refuse(rItem, n, err)
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
		case "note":
			p, err := linkedProject(ctx, tx, c, item.Project)
			if err != nil {
				return proposeOut{}, refuse(rItem, n, err)
			}
			item.Project = p.Key
			item.Epic, item.Text = strings.TrimSpace(item.Epic), strings.TrimSpace(item.Text)
			if item.Text == "" || utf8.RuneCountInString(item.Text) > delivery.NoteTextMax {
				return proposeOut{}, refuse(rItemNote1Characters2, n, delivery.NoteTextMax)
			}
			if item.Replaces < 0 {
				return proposeOut{}, refuse(rItemReplacesNoteNumber, n)
			}
			if item.Epic == "" {
				return proposeOut{}, refuse(rItemNoteNamesItsEpic, n)
			}
			if !newEpics[strings.ToLower(p.Key+"/"+item.Epic)] {
				id, err := epicByTitle(ctx, tx, p.ID, item.Epic)
				if err != nil {
					return proposeOut{}, err
				}
				if id == "" {
					return proposeOut{}, refuse(rItemNoEpicInAndNone, n, item.Epic, p.Key)
				}
			} else if item.Replaces != 0 {
				return proposeOut{}, refuse(rItemNewEpicHasNoNote, n, item.Replaces)
			}
		case "link_project":
			p, err := orgProject(orgProjects, item.Project)
			if err != nil {
				return proposeOut{}, refuse(rItem, n, err)
			}
			if p.Linked {
				return proposeOut{}, refuse(rItemProjectAlreadyLinked, n, p.Key)
			}
			// Filing one project twice would fail the whole card at setLinks.
			if j := slices.IndexFunc(items[:i], func(o delivery.ProposalItem) bool {
				return o.Kind == "link_project" && o.Project == p.Key
			}); j >= 0 {
				return proposeOut{}, refuse(rItemProjectProposedTwice, n, p.Key, j+1)
			}
			item.Project, item.ProjectName = p.Key, p.Name
			if item.Reason = strings.TrimSpace(item.Reason); item.Reason == "" || utf8.RuneCountInString(item.Reason) > delivery.ReasonMax {
				return proposeOut{}, refuse(rItemLinkReason, n, delivery.ReasonMax)
			}
		default:
			return proposeOut{}, refuse(rItemKindEpicTaskEditComment, n)
		}
		items[i] = item
	}
	if err := checkProposalRelations(ctx, tx, linkedIDs, items); err != nil {
		return proposeOut{}, err
	}
	id, err := delivery.RecordProposal(ctx, tx, c.run(), items)
	return proposeOut{ProposalID: id, Next: proposedNext}, err
}

// checkProposalRelations holds a proposal's relations to what filing can
// write: a task item's refs name an existing task of a linked project or
// another task item of the proposal, never itself; and the proposal's own
// blocks edges make no loop, so a card never shows one.
func checkProposalRelations(ctx context.Context, tx pgx.Tx, linked []string, items []delivery.ProposalItem) error {
	for i := range items {
		it := &items[i]
		n := i + 1
		for _, side := range []struct {
			name string
			refs []delivery.ProposalRef
		}{{"blockedBy", it.BlockedBy}, {"blocks", it.Blocks}, {"related", it.Related}} {
			if len(side.refs) > 0 && it.Kind != "task" {
				return refuse(rItemOnlyTaskItemHasRelations, n)
			}
			for j, r := range side.refs {
				switch {
				case r.Item != 0 && r.Key != "":
					return refuse(rItemNamesTaskByKeyOr, n, side.name)
				case r.Item == n:
					return refuse(rItemNamesItself, n, side.name)
				case r.Item != 0 && (r.Item < 0 || r.Item > len(items) || items[r.Item-1].Kind != "task"):
					return refuse(rItemNamesItemWhichNotTask, n, side.name, r.Item)
				case r.Item == 0:
					id, _, err := delivery.TaskByKey(ctx, tx, linked, r.Key)
					if err != nil {
						return err
					}
					if id == "" {
						return refuse(rItemNamesWhichNotTaskLinked, n, side.name, r.Key)
					}
					k, _ := delivery.TaskKey(r.Key)
					side.refs[j].Key = k
				}
			}
		}
	}
	// A loop among the items: walk each item's in-proposal blockers.
	state := make([]int, len(items)) // 0 unseen, 1 on the path, 2 done
	var path []int
	var visit func(i int) []int
	visit = func(i int) []int {
		state[i] = 1
		path = append(path, i)
		for _, b := range delivery.ItemBlockers(items, i) {
			if state[b] == 1 {
				start := slices.Index(path, b)
				return append(slices.Clone(path[start:]), b)
			}
			if state[b] == 0 {
				if loop := visit(b); loop != nil {
					return loop
				}
			}
		}
		path = path[:len(path)-1]
		state[i] = 2
		return nil
	}
	for i := range items {
		if state[i] != 0 {
			continue
		}
		if loop := visit(i); loop != nil {
			words := make([]string, len(loop))
			for k, x := range loop {
				words[k] = fmt.Sprintf("item %d", x+1)
			}
			return refuse(rItemsWaitOnEachOtherIn, strings.Join(words, " waits on "))
		}
	}
	return nil
}

// ---- relate (brainstorm) ----------------------------------------------

type relateIn struct {
	From string `json:"from"`
	To   string `json:"to"`
	Kind string `json:"kind"`
	Why  string `json:"why,omitempty"`
}

// relate proposes a relation between existing tasks, as a card item a
// member files: a brainstorm never writes one itself. Each call is a card
// of its own, as propose's are.
func relate(ctx context.Context, tx pgx.Tx, c Caller, in relateIn) (proposeOut, error) {
	projects, err := delivery.SessionProjects(ctx, tx, c.SessionID)
	if err != nil {
		return proposeOut{}, err
	}
	linked := make([]string, len(projects))
	for i, p := range projects {
		linked[i] = p.ID
	}
	kind := strings.TrimSpace(in.Kind)
	if kind != "blocks" && kind != "related" {
		return proposeOut{}, refuseText(rKindBlocksOrRelated)
	}
	var keys [2]string
	var taskIDs [2]string
	for i, k := range []string{in.From, in.To} {
		id, _, err := delivery.TaskByKey(ctx, tx, linked, k)
		if err != nil {
			return proposeOut{}, err
		}
		if id == "" {
			return proposeOut{}, refuse(rNotTaskLinkedProject, k)
		}
		keys[i], _ = delivery.TaskKey(k)
		taskIDs[i] = id
	}
	if taskIDs[0] == taskIDs[1] {
		return proposeOut{}, refuseText(rTaskCanTBeRelatedItself)
	}
	if kind == "blocks" {
		var loop []string
		if err := tx.QueryRow(ctx, `SELECT task_relation_would_cycle($1, $2)`, taskIDs[0], taskIDs[1]).Scan(&loop); err != nil {
			return proposeOut{}, err
		}
		if loop != nil {
			return proposeOut{}, refuse(rThatWouldMakeLoopTaskCan, strings.Join(loop, " → "))
		}
	}
	why := strings.TrimSpace(in.Why)
	if len(why) > 2000 {
		return proposeOut{}, refuseText(rWhyAtMost2000Characters)
	}
	item := delivery.ProposalItem{Kind: "relation", From: keys[0], To: keys[1], RelationKind: kind, Why: why}
	id, err := delivery.RecordProposal(ctx, tx, c.run(), []delivery.ProposalItem{item})
	return proposeOut{ProposalID: id, Next: relationProposedNext}, err
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
	Question  string      `json:"question,omitempty"`
	Choices   []string    `json:"choices,omitempty"`
	Questions []askItemIn `json:"questions,omitempty"`
	To        string      `json:"to,omitempty"`
}

// sessionAsk records a question — or up to four — for the session's
// members, or one member who can chat. The turn ends; the Run waits for the
// answer.
func sessionAsk(ctx context.Context, tx pgx.Tx, c Caller, in sessionAskIn) (askOut, error) {
	items, err := askItems(in.Question, in.Choices, in.Questions)
	if err != nil {
		return askOut{}, err
	}
	open, err := delivery.HasOpenQuestion(ctx, tx, c.RunID)
	if err != nil {
		return askOut{}, err
	}
	if open {
		return askOut{}, refuseText(rYouAlreadyHaveQuestionWaitingFor)
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
			return askOut{}, refuse(rNamesOneMemberWhoCanChat, name, len(found))
		}
		to, toName = found[0].ID, found[0].Name
	}
	id := ids.New(ids.Question)
	prompt, options := delivery.AskPrompt(items), delivery.AskOptions(items)
	opts, _ := json.Marshal(options)
	rawItems, _ := json.Marshal(items)
	if _, err := tx.Exec(ctx, `INSERT INTO questions (id, organization_id, run_id, prompt, options, items, to_person)
		VALUES ($1, $2, $3, $4, $5::jsonb, $6::jsonb, NULLIF($7, ''))`, id, c.Org, c.RunID, prompt, opts, rawItems, to); err != nil {
		return askOut{}, err
	}
	payload := map[string]any{"kind": "agent", "questionId": id, "prompt": prompt, "options": options, "items": items}
	if to != "" {
		payload["to"], payload["toName"] = to, toName
	}
	if _, err := ledger.Append(ctx, tx, c.event(delivery.EvQuestionAsked, payload)); err != nil {
		return askOut{}, err
	}
	return askOut{QuestionID: id, Next: sessionAskNext(toName)}, nil
}

// sessionAskNext is what the session's ask_person tells the agent to do
// next; to, the one member who answers, "" for any.
func sessionAskNext(to string) string { return sessionAskResult.Render(struct{ To string }{to}) }
