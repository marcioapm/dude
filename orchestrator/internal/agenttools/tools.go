package agenttools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"unicode/utf16"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/db"
	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/ids"
	"github.com/marciomartins/dude/orchestrator/internal/ledger"
)

// Who may create work: the roles whose job can turn up more of it.
var creators = []string{"implementer", "investigator", "conductor"}

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
		[]string{"implementer", "investigator", "conductor"}, askPerson),
	define("emit_event", "Record an event on your run for the people following it: progress (type progress, "+
		"data like {\"done\": 3, \"of\": 10, \"step\": \"tests\"}), a milestone, a measurement. It shows in "+
		"your chat and the run's events.", nil, emitEvent).limit(eventsPerRun),
	define("list_repositories", "The project's repositories: which this work has checked out (and whether it may "+
		"change them), and which it could ask for with request_repository. Not for waiting on a request: you are "+
		"told when one is decided.", nil, listRepositories),
	define("request_repository", "Ask for another of the project's repositories when this work needs it — "+
		"to read code this depends on, or (an implementer) to change it too. A person decides. Carry on meanwhile if "+
		"you can; if you cannot go on without it, ask with wait: true and end your turn — you are resumed with it, "+
		"or told it was declined.",
		nil, requestRepository).limit(requestsPerRun),
	define("create_task", fmt.Sprintf("Record a piece of work you found that is outside your task — a bug, a "+
		"follow-up, a part to split out — as a new task in this project. It is not started: a person reads it "+
		"and decides. The goal is required (at least %d characters): say why it matters and what should change.",
		GoalMin), creators, createTask).limit(createsPerRun),
	define("search_memory", "Search what is known here: memories people and agents saved (facts, procedures, "+
		"notes), and this project's tasks, epics and the project itself — by words and by meaning, best first. "+
		"Search before you investigate something that may already be known, and before you remember something.",
		nil, searchMemory).before(embedQuery),
	define("get_memory", "Read one memory in full, by the id search_memory gave, with where it was learned "+
		"and what it is about.", nil, getMemory),
	define("remember", "Save something worth knowing next time, for every agent and person on this project: "+
		"a fact that holds (\"the billing API paginates by cursor\"), a procedure that works, a trap and its way "+
		"around. It is live at once, and marked as yours. Search first so you do not save it twice; do not save "+
		"what the code or the task already says.", nil, remember).limit(remembersPerRun),
	define("run_diff", "What a Run of your task changed: this Run's checkout, uncommitted work included, against "+
		"the commit it started from — one snapshot, no history. Without paths, the changed files, most changed "+
		"first, with line counts and no lines (paged by limit and offset; hasMore says there is another page). "+
		"With paths, those files' changes as unified diff text, at most 2,000 lines in all.", nil, runDiff),
	define("findings", "Your task's review findings, open and most severe first: each one's id, severity, "+
		"category, file:line, status and how it was settled (fixed by which Run, accepted by a person, or open after "+
		"so many fix attempts). Name ids (at most 20) to read those in full: title, description, suggested fix and "+
		"the resolution note.", conductors, findings),
	define("pull_requests", "Your task's pull requests: state, head commit, checks (each one's status), review "+
		"(each reviewer's word), unresolved threads, and the feedback people left — author, kind, path, a short "+
		"excerpt, and whether a fixer was sent it.", conductors, pullRequests),
	define("start_phase", "Take the decision the delivery waits on by starting a phase from the task's head: "+
		"implement, review (some or all categories), fix (some or all open findings; at pull request feedback, the "+
		"feedback), simplify or test. The workflow creates the Runs as it creates its own, waits for them, and wakes "+
		"you at the next decision. Refused when you do not take the task's decisions, or past the policy's bounds: then "+
		"ask the person.", conductors, startPhase),
	define("steer", "Steer a running phase Run of this task (implement, review, fix, simplify, test) as a person would: "+
		"your words reach it at its next step, without stopping the work it is doing. Steer an agent going the wrong "+
		"way, or to add something the person just said; start another phase only once a Run has ended. interrupt "+
		"stops its turn so it hears this now: only when its current work is wasted. You are woken when it reads it, "+
		"or if it never will. Allowed whoever takes the decisions; refused for a Run that ended, another task's or an "+
		"earlier attempt's.", conductors, steer),
	define("decide", "Take the decision the delivery waits on: next (what Deliver would do now), ask_person (a "+
		"question for the person in the note; before the pull request, the fixed question Open / Draft / Show me the "+
		"diff / Another round), wait (at pull request feedback: leave it), or open_pull_request — accepted only after "+
		"the person answered Open or Draft to that question, at the task's current head.", conductors, decide),
	define("dismiss_finding", "Leave an open review finding as it is, with the reason — shown with the finding, as a "+
		"person accepting one is. Only while you take the task's decisions.", conductors, dismissFinding),
	define("decide_escalation", "Decide the escalation the delivery is stopped at (retry, accept, resume, done, wait or "+
		"stop, as it offers), with a note: only after the task's owner answered your question about this escalation in "+
		"their own words, handing it to you. A choice they pick decides it without you; otherwise the person decides on "+
		"the banner. Allowed once per escalation.", conductors, decideEscalation),
	define("update_task", "Write what Chat settled into the task: its goal, its acceptance criteria, or both "+
		"(criteria replace the list). Do it before you start the implementer, whenever you and the person agreed "+
		"something the task's text does not say, so the implementer's prompt has it. Refused once an implementer has "+
		"started on this attempt.", conductors, updateTask),
	define("reply_on_pull_request", "Answer on one of your task's pull requests, as dude's GitHub login: for a "+
		"question that came from there (a Chat message from a GitHub person names the pull request and the comment). "+
		"in_reply_to a line-comment-… answers in its review thread; any other comment, or none, posts on the pull "+
		"request's conversation, quoting the first line of the comment answered. It changes nothing and wakes nobody. "+
		"Refused for another task's pull request; GitHub refusing it is said, and nothing is posted.",
		conductors, replyOnPullRequest).before(forgeFor),
	define("publish", "Publish what you committed in your checkout to the task branch, as a phase's work is: lux "+
		"pushes it, then dude fast-forwards the task branch to it. Only for small, well-understood changes while you "+
		"take the decisions: refused while an implementer or fixer is at work, when your checkout is behind the task "+
		"branch (git merge lux/<branch> first), with nothing committed, or past the project's limit of changed lines "+
		"and files (then delegate: start_phase implement). Answers at once; you are woken when it is published or "+
		"refused. Run a review before the pull request: the gate refuses an unreviewed commit of yours.",
		conductors, publish),
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
		ORDER BY w.number`, c.ProjectID, c.TaskID, db.LikeLiteral(strings.TrimSpace(in.Text)))
	if err != nil {
		return out, err
	}
	out.Tasks, err = pgx.CollectRows(rows, pgx.RowToStructByPos[taskOut])
	return out, err
}

// ---- create_task -----------------------------------------------------

type createTaskIn struct {
	Title              string   `json:"title" jsonschema:"what should change, in one line"`
	Goal               string   `json:"goal" jsonschema:"required: why it matters, what should change, and any detail another agent or a person needs; see the tool's description for the minimum"`
	AcceptanceCriteria []string `json:"acceptanceCriteria,omitempty" jsonschema:"things that must be true when it is done"`
	Epic               string   `json:"epic,omitempty" jsonschema:"an existing epic's title to put it in (see list_tasks); none leaves it outside any"`
}

type createTaskOut struct {
	Key string `json:"key"`
}

// The control plane's task limits, in UTF-16 code units as its `.length`
// counts them: a goal, and all the criteria together. GoalMin is counted on
// the goal trimmed, and must agree with TASK_GOAL_MIN in
// packages/domain/src/hierarchy.ts: a task an agent makes is one a person
// could save.
const (
	GoalMin     = 16
	GoalMax     = 65_536
	CriteriaMax = 16_384
)

func utf16Len(s string) int {
	n := 0
	for _, r := range s {
		n += utf16.RuneLen(r)
	}
	return n
}

// isJSSpace is ECMAScript's WhiteSpace and LineTerminator set, what the
// control plane's String.prototype.trim() strips. It differs from
// unicode.IsSpace: it includes U+FEFF and excludes U+0085.
func isJSSpace(r rune) bool {
	switch r {
	case '\t', '\n', '\v', '\f', '\r', ' ', '\u00a0', '\u1680', '\u2028', '\u2029', '\u202f', '\u205f',
		'\u3000', '\ufeff':
		return true
	}
	return r >= '\u2000' && r <= '\u200a'
}

func criteriaLength(criteria []string) int {
	n := 0
	for _, c := range criteria {
		n += utf16Len(c)
	}
	return n
}

func createTask(ctx context.Context, tx pgx.Tx, c Caller, in createTaskIn) (createTaskOut, error) {
	title := strings.TrimSpace(in.Title)
	switch {
	case title == "":
		return createTaskOut{}, refuse("a title is required")
	case utf16Len(strings.TrimFunc(in.Goal, isJSSpace)) < GoalMin:
		return createTaskOut{}, refuse("a task needs a goal of at least %d characters: say why it matters and what should "+
			"change, so a person can decide on it without asking you", GoalMin)
	case len(title) > 500 || utf16Len(in.Goal) > GoalMax || len(in.AcceptanceCriteria) > 50:
		return createTaskOut{}, refuse("too long: a title of at most 500 characters, a goal of %d, at most 50 criteria", GoalMax)
	case criteriaLength(in.AcceptanceCriteria) > CriteriaMax:
		return createTaskOut{}, refuse("acceptance criteria too long: at most %d characters in all", CriteriaMax)
	}
	var epicID *string
	if e := strings.TrimSpace(in.Epic); e != "" {
		id, err := epicByTitle(ctx, tx, c.ProjectID, e)
		if err != nil {
			return createTaskOut{}, err
		}
		if id == "" {
			return createTaskOut{}, refuse("no epic called %q in this project (list_tasks shows them)", e)
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
			acceptance_criteria, status, created_by_run_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8::jsonb, 'received', $9)`,
		id, c.Org, c.ProjectID, number, epicID, title, strings.TrimSpace(in.Goal), criteria, c.RunID); err != nil {
		return createTaskOut{}, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO task_people (task_id, person_id, organization_id, position)
		SELECT $1, p.id, $2, 0 FROM task_people tp JOIN people p ON p.id = tp.person_id
		WHERE tp.task_id = $3 AND p.removed_at IS NULL
		ORDER BY tp.position, tp.person_id LIMIT 1`, id, c.Org, c.TaskID); err != nil {
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

// epicByTitle finds an epic as an agent names it: by its title, in its
// project, whatever the case. "" when there is none.
func epicByTitle(ctx context.Context, tx pgx.Tx, project, title string) (string, error) {
	var id string
	err := tx.QueryRow(ctx, `SELECT id FROM epics WHERE project_id = $1 AND lower(title) = lower($2) LIMIT 1`, project, title).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return id, err
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
	Actions  []string `json:"actions,omitempty" jsonschema:"a conductor's, while an escalation waits on a person: the escalation action each choice stands for, one per choice, in order (retry, accept, resume, done, wait, stop); the owner picking a choice decides the escalation with it"`
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
	// A conductor's question while its task's escalation waits on a person
	// is that escalation's question.
	var escalation string
	if c.Role == delivery.RoleConductor {
		if escalation, err = conducted(delivery.EscalationQuestion(ctx, tx, c.run(), in.Choices, in.Actions)); err != nil {
			return askOut{}, err
		}
	} else if len(in.Actions) > 0 {
		return askOut{}, refuse("actions are a conductor's, for a question about an escalation")
	}
	id, err := delivery.AskTx(ctx, tx, c.run(), q, in.Choices)
	if err != nil {
		return askOut{}, err
	}
	if escalation != "" {
		acts, _ := json.Marshal(in.Actions)
		if _, err := tx.Exec(ctx, `UPDATE questions SET escalation = $2, actions = $3::jsonb WHERE id = $1`,
			id, escalation, acts); err != nil {
			return askOut{}, err
		}
		return askOut{QuestionID: id, Next: "End your turn now. The owner picking a choice decides the escalation, as the " +
			"banner does; an answer in their own words is your next message, and lets you decide it with decide_escalation."}, nil
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
		LIMIT 1`, c.ProjectID, name, db.LikeLiteral(name)).Scan(&repo.ID, &repo.Name)
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
