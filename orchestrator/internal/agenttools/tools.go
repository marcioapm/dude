package agenttools

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/db"
	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/ids"
	"github.com/marciomartins/dude/orchestrator/internal/ledger"
)

// Who may create work: the roles whose job can turn up more of it.
var creators = []string{"implementer", "investigator", "orchestrator"}

// Who may ask to change another repository: the roles that change code.
var writers = []string{"implementer"}

// How many repository requests a Run may have waiting at once.
const maxPendingRequests = 3

// The tools, and which roles have them (docs/design/agent-tools.md).
var tools = []tool{
	define("list_tasks", "The project's epics and tasks, with their keys (like TEXT-12), status and who "+
		"asked for them — optionally only those mentioning some text. Use it before creating work, to find what "+
		"already exists.", nil, listTasks),
	define("list_epics", "The project's epics, in priority order, with how many tasks each has and how many "+
		"are still open.", nil, listEpics),
	define("ask_person", "Ask a person something only a person can decide — the task is ambiguous in a way that "+
		"changes what you build, or two reasonable readings conflict. After calling it, end your turn: the answer "+
		"is your next message. Do not ask about anything you can decide or find out yourself.",
		[]string{"implementer", "investigator"}, askPerson),
	define("emit_event", "Record an event on your run for the people following it: progress (type progress, "+
		"data like {\"done\": 3, \"of\": 10, \"step\": \"tests\"}), a milestone, a measurement. It shows in "+
		"your chat and the run's events.", nil, emitEvent),
	define("list_repositories", "The project's repositories: which this work has checked out (and whether it may "+
		"change them), and which it could ask for with request_repository. Not for waiting on a request: you are "+
		"told when one is decided.", nil, listRepositories),
	define("request_repository", "Ask for another of the project's repositories when this work needs it — "+
		"to read code this depends on, or (an implementer) to change it too. A person decides. Carry on meanwhile if "+
		"you can; if you cannot go on without it, ask with wait: true and end your turn — you are resumed with it, "+
		"or told it was declined.",
		nil, requestRepository),
	define("create_task", "Record a piece of work you found that is outside your task — a bug, a "+
		"follow-up, a part to split out — as a new task in this project. It is not started: a person reads it "+
		"and decides. Say what and why in the goal.", creators, createTask),
	define("search_memory", "Search what is known here: memories people and agents saved (facts, procedures, "+
		"notes), and this project's tasks, epics and the project itself — by words and by meaning, best first. "+
		"Search before you investigate something that may already be known, and before you remember something.",
		nil, searchMemory),
	define("get_memory", "Read one memory in full, by the id search_memory gave, with where it was learned "+
		"and what it is about.", nil, getMemory),
	define("remember", "Save something worth knowing next time, for every agent and person on this project: "+
		"a fact that holds (\"the billing API paginates by cursor\"), a procedure that works, a trap and its way "+
		"around. It is live at once, and marked as yours. Search first so you do not save it twice; do not save "+
		"what the code or the task already says.", nil, remember),
}

// ---- list_tasks --------------------------------------------------------------

type listTasksIn struct {
	Text string `json:"text,omitempty" jsonschema:"only tasks whose title or goal mention this (case-insensitive); empty lists everything"`
}

type taskOut struct {
	Key    string `json:"key"`
	Title  string `json:"title"`
	Status string `json:"status"`
	Epic   string `json:"epic,omitempty"`
	// "person", or the key of the task whose agent created it.
	CreatedBy string `json:"createdBy"`
	// This task is the caller's own.
	Yours bool `json:"yours,omitempty"`
}

type listTasksOut struct {
	Epics []string  `json:"epics"`
	Tasks []taskOut `json:"tasks"`
}

func listTasks(ctx context.Context, tx pgx.Tx, c Caller, in listTasksIn) (listTasksOut, error) {
	out := listTasksOut{Epics: []string{}, Tasks: []taskOut{}}
	rows, err := tx.Query(ctx, `SELECT title FROM epics WHERE project_id = $1 ORDER BY position, created_at`, c.ProjectID)
	if err != nil {
		return out, err
	}
	if out.Epics, err = pgx.CollectRows(rows, pgx.RowTo[string]); err != nil {
		return out, err
	}
	rows, err = tx.Query(ctx, `
		SELECT p.key_prefix || '-' || w.number, w.title, w.status::text, COALESCE(e.title, ''),
		       COALESCE((SELECT p.key_prefix || '-' || src.number FROM runs r JOIN tasks src ON src.id = r.task_id
		                 WHERE r.id = w.created_by_run_id), 'person'),
		       w.id = $2
		FROM tasks w JOIN projects p ON p.id = w.project_id LEFT JOIN epics e ON e.id = w.epic_id
		WHERE w.project_id = $1
		  AND ($3 = '' OR w.title ILIKE '%' || $3 || '%' ESCAPE '\' OR w.goal ILIKE '%' || $3 || '%' ESCAPE '\')
		ORDER BY w.number`, c.ProjectID, c.TaskID, likeLiteral(strings.TrimSpace(in.Text)))
	if err != nil {
		return out, err
	}
	out.Tasks, err = pgx.CollectRows(rows, pgx.RowToStructByPos[taskOut])
	return out, err
}

// ---- create_task -----------------------------------------------------

type createTaskIn struct {
	Title              string   `json:"title" jsonschema:"what should change, in one line"`
	Goal               string   `json:"goal" jsonschema:"why, and any detail another agent or a person needs"`
	AcceptanceCriteria []string `json:"acceptanceCriteria,omitempty" jsonschema:"things that must be true when it is done"`
	Epic               string   `json:"epic,omitempty" jsonschema:"an existing epic's title to put it in (see list_tasks); none leaves it outside any"`
}

type createTaskOut struct {
	Key string `json:"key"`
}

func createTask(ctx context.Context, tx pgx.Tx, c Caller, in createTaskIn) (createTaskOut, error) {
	title := strings.TrimSpace(in.Title)
	switch {
	case title == "":
		return createTaskOut{}, refuse("a title is required")
	case len(title) > 500 || len(in.Goal) > 10_000 || len(in.AcceptanceCriteria) > 50:
		return createTaskOut{}, refuse("too long: a title of at most 500 characters, a goal of 10000, at most 50 criteria")
	}
	var epicID *string
	if e := strings.TrimSpace(in.Epic); e != "" {
		var id string
		if err := tx.QueryRow(ctx, `SELECT id FROM epics WHERE project_id = $1 AND lower(title) = lower($2) LIMIT 1`,
			c.ProjectID, e).Scan(&id); err != nil {
			if err == pgx.ErrNoRows {
				return createTaskOut{}, refuse("no epic called %q in this project (list_tasks shows them)", e)
			}
			return createTaskOut{}, err
		}
		epicID = &id
	}
	var number int
	var prefix string
	// The project's next number, locked so two creates cannot take the same.
	if err := tx.QueryRow(ctx, `UPDATE projects SET next_task_number = next_task_number + 1
		WHERE id = $1 RETURNING next_task_number - 1, key_prefix`, c.ProjectID).Scan(&number, &prefix); err != nil {
		return createTaskOut{}, err
	}
	criteria, _ := json.Marshal(db.NonNil(in.AcceptanceCriteria))
	id := ids.New(ids.Task)
	// Owned by whoever drives the task the agent is working on: the person
	// who would hear about it, and decide whether it is worth doing.
	if _, err := tx.Exec(ctx, `INSERT INTO tasks (id, organization_id, project_id, number, epic_id, title, goal,
			acceptance_criteria, status, created_by_run_id, owner_key_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8::jsonb, 'received', $9,
			(SELECT t.owner_key_id FROM runs r JOIN tasks t ON t.id = r.task_id WHERE r.id = $9))`,
		id, c.Org, c.ProjectID, number, epicID, title, strings.TrimSpace(in.Goal), criteria, c.RunID); err != nil {
		return createTaskOut{}, err
	}
	// The same event a person creating one records, on the new task
	// (its own correlation), so its history starts with who made it.
	created := c.event("task.created", map[string]any{"title": title, "goal": strings.TrimSpace(in.Goal),
		"createdByTaskId": c.TaskID})
	created.TaskID, created.CorrelationID = id, id
	if _, err := ledger.Append(ctx, tx, created); err != nil {
		return createTaskOut{}, err
	}
	return createTaskOut{Key: fmt.Sprintf("%s-%d", prefix, number)}, nil
}

// likeLiteral escapes text for LIKE: % and _ match themselves.
func likeLiteral(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

// ---- list_epics -------------------------------------------------------------

type listEpicsIn struct{}

type epicOut struct {
	Title       string `json:"title"`
	Description string `json:"description,omitempty"`
	Tasks       int    `json:"tasks"`
	Open        int    `json:"open"`
}

func listEpics(ctx context.Context, tx pgx.Tx, c Caller, _ listEpicsIn) ([]epicOut, error) {
	rows, err := tx.Query(ctx, `SELECT e.title, e.description,
			(SELECT count(*) FROM tasks w WHERE w.epic_id = e.id),
			(SELECT count(*) FROM tasks w WHERE w.epic_id = e.id AND w.status NOT IN ('done', 'aborted', 'failed'))
		FROM epics e WHERE e.project_id = $1 ORDER BY e.position, e.created_at`, c.ProjectID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[epicOut])
}

// ---- ask_person -------------------------------------------------------------

type askIn struct {
	Question string   `json:"question" jsonschema:"what you need a person to decide, with enough context to answer it"`
	Choices  []string `json:"choices,omitempty" jsonschema:"answers to offer, when there are some"`
}

type askOut struct {
	QuestionID string `json:"questionId"`
	Next       string `json:"next"`
}

// askPerson records a question for a person. The agent ends its turn; the
// answer arrives as its next message — parked in between if the person
// takes longer than the project's grace period.
func askPerson(ctx context.Context, tx pgx.Tx, c Caller, in askIn) (askOut, error) {
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
	id, err := delivery.AskTx(ctx, tx, c.run(), q, in.Choices)
	if err != nil {
		return askOut{}, err
	}
	return askOut{QuestionID: id, Next: "End your turn now. The person's answer will be your next message."}, nil
}

// ---- emit_event -------------------------------------------------------------

type emitIn struct {
	Type string `json:"type" jsonschema:"what kind of event: lowercase words and dots, like progress or tests.finished"`
	// Any JSON value. (Not json.RawMessage: its schema reads as an array of
	// bytes, and a model follows the schema.)
	Data any `json:"data,omitempty" jsonschema:"anything to go with it: an object like {\"done\": 3, \"of\": 10} (at most 16 KB)"`
}

type emitOut struct {
	EventType string `json:"eventType"`
}

// CustomPrefix is where custom events live: a namespace no one else writes,
// so an agent cannot pass one off as dude's own.
const CustomPrefix = "agent.custom."

var eventType = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*){0,3}$`)

// emitEvent records a custom event on the Run: progress, a milestone,
// something dude reacts to. Shown in the chat and the events unless dude
// handles its type.
func emitEvent(ctx context.Context, tx pgx.Tx, c Caller, in emitIn) (emitOut, error) {
	if !eventType.MatchString(in.Type) || len(in.Type) > 64 {
		return emitOut{}, refuse("type %q: lowercase words joined by dots, like progress or tests.finished", in.Type)
	}
	data := in.Data
	if data == nil {
		data = map[string]any{}
	}
	// Models often send an object as the JSON text of one: take the object.
	if text, ok := data.(string); ok {
		var decoded any
		if json.Unmarshal([]byte(text), &decoded) == nil {
			if _, isObject := decoded.(map[string]any); isObject {
				data = decoded
			}
		}
	}
	if raw, _ := json.Marshal(data); len(raw) > 16<<10 {
		return emitOut{}, refuse("data too large: at most 16 KB")
	}
	typ := CustomPrefix + in.Type
	_, err := ledger.Append(ctx, tx, c.event(typ, map[string]any{"type": in.Type, "data": data}))
	return emitOut{EventType: typ}, err
}

// ---- request_repository ---------------------------------------------------

type requestRepoIn struct {
	Repository string `json:"repository" jsonschema:"the repository's name, or owner/name, as the organization knows it"`
	Write      bool   `json:"write,omitempty" jsonschema:"you need to change it (it gets its own pull request); otherwise read only"`
	Reason     string `json:"reason" jsonschema:"why this work needs it, for the person deciding"`
	Wait       bool   `json:"wait,omitempty" jsonschema:"you cannot go on without it: end your turn after asking, and you are resumed when a person decides (otherwise carry on; a later phase gets it if approved)"`
}

type requestRepoOut struct {
	RequestID string `json:"requestId"`
	Status    string `json:"status"`
	Next      string `json:"next"`
}

// requestRepository asks a person for another of the project's
// repositories. The agent carries on; approved, it is given the repository
// without losing its conversation. Only the project's own: reaching into
// another project is a person's decision to make, not an agent's to ask.
func requestRepository(ctx context.Context, tx pgx.Tx, c Caller, in requestRepoIn) (requestRepoOut, error) {
	name := strings.TrimSpace(in.Repository)
	reason := strings.TrimSpace(in.Reason)
	switch {
	case name == "" || reason == "":
		return requestRepoOut{}, refuse("name the repository and say why you need it")
	case len(reason) > 2000:
		return requestRepoOut{}, refuse("reason too long: at most 2000 characters")
	case in.Write && !slices.Contains(writers, c.Role):
		return requestRepoOut{}, refuse("a %s may ask to read a repository, not to change it", c.Role)
	}
	// The project's repositories, by name or owner/name (the end of its URL).
	var repo struct{ ID, Name string }
	err := tx.QueryRow(ctx, `SELECT id, name FROM repositories
		WHERE project_id = $1
		  AND (lower(name) = lower($2) OR lower(regexp_replace(url, '(\.git)?/*$', '')) LIKE '%/' || lower($3) ESCAPE '\')
		LIMIT 1`, c.ProjectID, name, likeLiteral(name)).Scan(&repo.ID, &repo.Name)
	if err == pgx.ErrNoRows {
		return requestRepoOut{}, refuse("no repository %q in this project (a person can add one to the project)", name)
	}
	if err != nil {
		return requestRepoOut{}, err
	}
	var clash, pending int
	if err := tx.QueryRow(ctx, `SELECT
			(SELECT count(*) FROM task_repositories wr JOIN repositories r ON r.id = wr.repository_id
			 WHERE wr.task_id = $1 AND (wr.repository_id = $2 OR r.name = $3)),
			(SELECT count(*) FROM repository_requests WHERE run_id = $4 AND status = 'pending')`,
		c.TaskID, repo.ID, repo.Name, c.RunID).Scan(&clash, &pending); err != nil {
		return requestRepoOut{}, err
	}
	switch {
	case clash > 0:
		return requestRepoOut{}, refuse("%s is already checked out for this work", repo.Name)
	case pending >= maxPendingRequests:
		return requestRepoOut{}, refuse("you have %d requests waiting for a person; wait for them first", pending)
	}
	access := "read"
	if in.Write {
		access = "write"
	}
	id := ids.New(ids.RepoRequest)
	tag, err := tx.Exec(ctx, `INSERT INTO repository_requests (id, organization_id, task_id, run_id, repository_id, access, reason, blocking)
		VALUES ($1, $2, $3, $4, $5, $6::repository_access, $7, $8) ON CONFLICT DO NOTHING`,
		id, c.Org, c.TaskID, c.RunID, repo.ID, access, reason, in.Wait)
	if err != nil {
		return requestRepoOut{}, err
	}
	if tag.RowsAffected() == 0 {
		return requestRepoOut{}, refuse("you already asked for %s; a person has not decided yet", repo.Name)
	}
	if _, err := ledger.Append(ctx, tx, c.event(delivery.EvRepositoryRequested,
		map[string]any{"requestId": id, "repository": repo.Name, "repositoryId": repo.ID, "access": access, "reason": reason})); err != nil {
		return requestRepoOut{}, err
	}
	next := "Do not wait or check for it: a person may take a while. Carry on with what you can without " +
		repo.Name + ". If they approve while you work, you will be paused and resumed with it checked out, and " +
		"told where, in a message; if not, a message will say so."
	if in.Wait {
		next = "End your turn now. When a person decides, you are resumed: with " + repo.Name +
			" checked out, and told where, or told it was declined."
	}
	return requestRepoOut{RequestID: id, Status: "pending", Next: next}, nil
}

// ---- list_repositories ----------------------------------------------------

type listReposIn struct{}

type repoOut struct {
	Name          string `json:"name"`
	URL           string `json:"url"`
	DefaultBranch string `json:"defaultBranch"`
	// "write" or "read" when this work has it checked out; "" when it could
	// be requested.
	Access string `json:"access,omitempty"`
	// A request for it waiting on a person.
	Requested bool `json:"requested,omitempty"`
}

// listRepositories is what an agent may work on: the project's
// repositories, which this work has, and which it could ask for.
func listRepositories(ctx context.Context, tx pgx.Tx, c Caller, _ listReposIn) ([]repoOut, error) {
	rows, err := tx.Query(ctx, `SELECT r.name, r.url, r.default_branch, COALESCE(wr.access::text, ''),
			EXISTS (SELECT 1 FROM repository_requests q WHERE q.run_id = $2 AND q.repository_id = r.id AND q.status = 'pending')
		FROM repositories r
		LEFT JOIN task_repositories wr ON wr.repository_id = r.id AND wr.task_id = $3
		WHERE r.project_id = $1 ORDER BY r.name`, c.ProjectID, c.RunID, c.TaskID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[repoOut])
}
