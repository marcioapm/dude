package agenttools

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/ids"
	"github.com/marciomartins/dude/orchestrator/internal/ledger"
)

// Who may create work: the roles whose job can turn up more of it.
var creators = []string{"implementer", "investigator", "orchestrator"}

// The tools, and which roles have them (docs/design/agent-tools.md).
var tools = []tool{
	define("list_work", "The project's epics and work items, with their keys (like TEXT-12), status and who "+
		"asked for them — optionally only those mentioning some text. Use it before creating work, to find what "+
		"already exists.", nil, listWork),
	define("list_epics", "The project's epics, in priority order, with how many work items each has and how many "+
		"are still open.", nil, listEpics),
	define("ask_person", "Ask a person something only a person can decide — the task is ambiguous in a way that "+
		"changes what you build, or two reasonable readings conflict. After calling it, end your turn: the answer "+
		"is your next message. Do not ask about anything you can decide or find out yourself.",
		[]string{"implementer", "investigator"}, askPerson),
	define("emit_event", "Record an event on your run for the people following it: progress (type progress, "+
		"data like {\"done\": 3, \"of\": 10, \"step\": \"tests\"}), a milestone, a measurement. It shows in "+
		"your chat and the run's events.", nil, emitEvent),
	define("create_work_item", "Record a piece of work you found that is outside your task — a bug, a "+
		"follow-up, a part to split out — as a new work item in this project. It is not started: a person reads it "+
		"and decides. Say what and why in the goal.", creators, createWorkItem),
}

// ---- list_work --------------------------------------------------------------

type listWorkIn struct {
	Text string `json:"text,omitempty" jsonschema:"only work items whose title or goal mention this (case-insensitive); empty lists everything"`
}

type workItemOut struct {
	Key    string `json:"key"`
	Title  string `json:"title"`
	Status string `json:"status"`
	Epic   string `json:"epic,omitempty"`
	// "person", or the key of the work item whose agent created it.
	CreatedBy string `json:"createdBy"`
	// This work item is the caller's own.
	Yours bool `json:"yours,omitempty"`
}

type listWorkOut struct {
	Epics     []string      `json:"epics"`
	WorkItems []workItemOut `json:"workItems"`
}

func listWork(ctx context.Context, tx pgx.Tx, c Caller, in listWorkIn) (listWorkOut, error) {
	out := listWorkOut{Epics: []string{}, WorkItems: []workItemOut{}}
	rows, err := tx.Query(ctx, `SELECT title FROM epics WHERE project_id = $1 ORDER BY position, created_at`, c.ProjectID)
	if err != nil {
		return out, err
	}
	if out.Epics, err = pgx.CollectRows(rows, pgx.RowTo[string]); err != nil {
		return out, err
	}
	rows, err = tx.Query(ctx, `
		SELECT p.key_prefix || '-' || w.number, w.title, w.status::text, COALESCE(e.title, ''),
		       COALESCE((SELECT p.key_prefix || '-' || src.number FROM runs r JOIN work_items src ON src.id = r.work_item_id
		                 WHERE r.id = w.created_by_run_id), 'person'),
		       w.id = $2
		FROM work_items w JOIN projects p ON p.id = w.project_id LEFT JOIN epics e ON e.id = w.epic_id
		WHERE w.project_id = $1
		  AND ($3 = '' OR w.title ILIKE '%' || $3 || '%' OR w.goal ILIKE '%' || $3 || '%')
		ORDER BY w.number`, c.ProjectID, c.WorkItemID, strings.TrimSpace(in.Text))
	if err != nil {
		return out, err
	}
	out.WorkItems, err = pgx.CollectRows(rows, pgx.RowToStructByPos[workItemOut])
	return out, err
}

// ---- create_work_item -----------------------------------------------------

type createWorkItemIn struct {
	Title              string   `json:"title" jsonschema:"what should change, in one line"`
	Goal               string   `json:"goal" jsonschema:"why, and any detail another agent or a person needs"`
	AcceptanceCriteria []string `json:"acceptanceCriteria,omitempty" jsonschema:"things that must be true when it is done"`
	Epic               string   `json:"epic,omitempty" jsonschema:"an existing epic's title to put it in (see list_work); none leaves it outside any"`
}

type createWorkItemOut struct {
	Key string `json:"key"`
}

func createWorkItem(ctx context.Context, tx pgx.Tx, c Caller, in createWorkItemIn) (createWorkItemOut, error) {
	title := strings.TrimSpace(in.Title)
	switch {
	case title == "":
		return createWorkItemOut{}, refuse("a title is required")
	case len(title) > 500 || len(in.Goal) > 10_000 || len(in.AcceptanceCriteria) > 50:
		return createWorkItemOut{}, refuse("too long: a title of at most 500 characters, a goal of 10000, at most 50 criteria")
	}
	var epicID *string
	if e := strings.TrimSpace(in.Epic); e != "" {
		var id string
		if err := tx.QueryRow(ctx, `SELECT id FROM epics WHERE project_id = $1 AND lower(title) = lower($2) LIMIT 1`,
			c.ProjectID, e).Scan(&id); err != nil {
			if err == pgx.ErrNoRows {
				return createWorkItemOut{}, refuse("no epic called %q in this project (list_work shows them)", e)
			}
			return createWorkItemOut{}, err
		}
		epicID = &id
	}
	var number int
	var prefix string
	// The project's next number, locked so two creates cannot take the same.
	if err := tx.QueryRow(ctx, `UPDATE projects SET next_work_item_number = next_work_item_number + 1
		WHERE id = $1 RETURNING next_work_item_number - 1, key_prefix`, c.ProjectID).Scan(&number, &prefix); err != nil {
		return createWorkItemOut{}, err
	}
	criteria, _ := json.Marshal(nonNil(in.AcceptanceCriteria))
	id := ids.New(ids.WorkItem)
	if _, err := tx.Exec(ctx, `INSERT INTO work_items (id, organization_id, project_id, number, epic_id, title, goal,
			acceptance_criteria, status, created_by_run_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8::jsonb, 'received', $9)`,
		id, c.Org, c.ProjectID, number, epicID, title, strings.TrimSpace(in.Goal), criteria, c.RunID); err != nil {
		return createWorkItemOut{}, err
	}
	// The same event a person creating one records, on the new work item,
	// so its own history starts with who made it.
	if _, err := ledger.Append(ctx, tx, ledger.Event{
		Type: "work_item.created", OrganizationID: c.Org, ProjectID: c.ProjectID, WorkItemID: id, RunID: c.RunID,
		ActorType: ledger.ActorAgent, ActorID: c.RunID, Source: ledger.SourceOrchestrator, CorrelationID: id,
		Payload: map[string]any{"title": title, "goal": strings.TrimSpace(in.Goal), "createdByWorkItemId": c.WorkItemID},
	}); err != nil {
		return createWorkItemOut{}, err
	}
	return createWorkItemOut{Key: fmt.Sprintf("%s-%d", prefix, number)}, nil
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// ---- list_epics -------------------------------------------------------------

type listEpicsIn struct{}

type epicOut struct {
	Title       string `json:"title"`
	Description string `json:"description,omitempty"`
	WorkItems   int    `json:"workItems"`
	Open        int    `json:"open"`
}

func listEpics(ctx context.Context, tx pgx.Tx, c Caller, _ listEpicsIn) ([]epicOut, error) {
	rows, err := tx.Query(ctx, `SELECT e.title, e.description,
			(SELECT count(*) FROM work_items w WHERE w.epic_id = e.id),
			(SELECT count(*) FROM work_items w WHERE w.epic_id = e.id AND w.status NOT IN ('done', 'aborted', 'failed'))
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
// answer arrives as its next message, through the same answer flow as a
// question block.
func askPerson(ctx context.Context, tx pgx.Tx, c Caller, in askIn) (askOut, error) {
	q := strings.TrimSpace(in.Question)
	switch {
	case q == "":
		return askOut{}, refuse("a question is required")
	case len(q) > 4000 || len(in.Choices) > 10:
		return askOut{}, refuse("too long: a question of at most 4000 characters, at most 10 choices")
	}
	var open bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM questions WHERE run_id = $1 AND status = 'open')`,
		c.RunID).Scan(&open); err != nil {
		return askOut{}, err
	}
	if open {
		return askOut{}, refuse("you already have a question waiting for an answer: end your turn and wait for it")
	}
	id := ids.New(ids.Question)
	choices, _ := json.Marshal(nonNil(in.Choices))
	if _, err := tx.Exec(ctx, `INSERT INTO questions (id, organization_id, work_item_id, run_id, prompt, options)
		VALUES ($1, $2, $3, $4, $5, $6::jsonb)`, id, c.Org, c.WorkItemID, c.RunID, q, choices); err != nil {
		return askOut{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE work_items SET status = 'awaiting_input', updated_at = now()
		WHERE id = $1 AND status NOT IN ('done', 'failed', 'aborted', 'awaiting_input')`, c.WorkItemID); err != nil {
		return askOut{}, err
	}
	for _, e := range []ledger.Event{
		{Type: "work_item.status_changed", Payload: map[string]any{"status": "awaiting_input", "reason": "the agent asked a question"}},
		{Type: "question.asked", Payload: map[string]any{"kind": "agent", "questionId": id, "prompt": q, "options": nonNil(in.Choices)}},
	} {
		e.OrganizationID, e.ProjectID, e.WorkItemID, e.RunID = c.Org, c.ProjectID, c.WorkItemID, c.RunID
		e.ActorType, e.ActorID, e.Source, e.CorrelationID = ledger.ActorAgent, c.RunID, ledger.SourceOrchestrator, c.WorkItemID
		if _, err := ledger.Append(ctx, tx, e); err != nil {
			return askOut{}, err
		}
	}
	return askOut{QuestionID: id, Next: "End your turn now. The person's answer will be your next message."}, nil
}

// ---- emit_event -------------------------------------------------------------

type emitIn struct {
	Type string          `json:"type" jsonschema:"what kind of event: lowercase words and dots, like progress or tests.finished"`
	Data json.RawMessage `json:"data,omitempty" jsonschema:"anything to go with it, as JSON (at most 16 KB)"`
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
	if len(in.Data) > 16<<10 {
		return emitOut{}, refuse("data too large: at most 16 KB")
	}
	var data any = map[string]any{}
	if len(in.Data) > 0 {
		if err := json.Unmarshal(in.Data, &data); err != nil {
			return emitOut{}, refuse("data is not JSON: %v", err)
		}
	}
	typ := CustomPrefix + in.Type
	_, err := ledger.Append(ctx, tx, ledger.Event{
		Type: typ, OrganizationID: c.Org, ProjectID: c.ProjectID, WorkItemID: c.WorkItemID, RunID: c.RunID,
		ActorType: ledger.ActorAgent, ActorID: c.RunID, Source: ledger.SourceOrchestrator, CorrelationID: c.WorkItemID,
		Payload: map[string]any{"type": in.Type, "data": data},
	})
	return emitOut{EventType: typ}, err
}
