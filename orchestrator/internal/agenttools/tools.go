package agenttools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/marciomartins/dude/orchestrator/internal/ids"
	"github.com/marciomartins/dude/orchestrator/internal/ledger"
)

// The tools, and which roles have them (docs/design/agent-tools.md).
var tools = []tool{
	{name: "list_work", add: func(s *Server, srv *mcp.Server, c Caller) {
		bind(s, srv, c, &mcp.Tool{Name: "list_work", Description: "The project's epics and work items, with their keys " +
			"(like TEXT-12), status and who asked for them. Use it before creating work, to find what already exists."}, listWork)
	}},
	{name: "create_work_item", roles: []string{"implementer", "investigator", "orchestrator"}, add: func(s *Server, srv *mcp.Server, c Caller) {
		bind(s, srv, c, &mcp.Tool{Name: "create_work_item", Description: "Record a piece of work you found that is " +
			"outside your task — a bug, a follow-up, a part to split out — as a new work item in this project. It is " +
			"not started: a person reads it and decides. Say what and why in the goal."}, createWorkItem)
	}},
}

// ---- list_work --------------------------------------------------------------

type listWorkIn struct{}

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

func listWork(ctx context.Context, tx pgx.Tx, c Caller, _ listWorkIn) (listWorkOut, error) {
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
		WHERE w.project_id = $1 ORDER BY w.number`, c.ProjectID, c.WorkItemID)
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
