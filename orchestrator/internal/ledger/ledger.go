// Package ledger appends to the event log both processes share.
//
// The orchestrator writes most events, but the browser is fed by the
// backend. A trigger on `events` (migration 015) announces every insert with
// NOTIFY, so the backend sees events no matter which process wrote them,
// without either side calling the other.
package ledger

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/db"
	"github.com/marciomartins/dude/orchestrator/internal/ids"
)

// Source values the events table uses (eventSourceSchema in the domain package).
const (
	SourceOrchestrator = "control-plane"
	SourceRunner       = "runner"
	SourceGitHub       = "github"
)

// Actor types.
const (
	ActorSystem = "system"
	ActorAgent  = "agent"
	ActorHuman  = "human"
	// Someone outside dude, through an integration: a GitHub login.
	ActorIntegration = "integration"
)

type Event struct {
	Type           string
	OrganizationID string
	ProjectID      string
	TaskID         string
	RunID          string
	SessionID      string
	WorkflowRunID  string
	ActorType      string
	ActorID        string
	Source         string
	CorrelationID  string
	CausationID    string
	OccurredAt     time.Time
	Payload        map[string]any
}

const insertColumns = `INSERT INTO events (id, organization_id, event_type, occurred_at,
		                    project_id, task_id, run_id, session_id, workflow_run_id,
		                    actor_type, actor_id, source, correlation_id, causation_id, payload)`

// Append writes one event in the caller's transaction, so it commits or
// rolls back with the state change that produced it.
func Append(ctx context.Context, tx pgx.Tx, e Event) (string, error) {
	id, args, err := row(e)
	if err != nil {
		return "", err
	}
	_, err = tx.Exec(ctx, insertColumns+`
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15::jsonb)`, args...)
	return id, err
}

// AppendOncePerEpoch writes e unless e's Run already has an event of e's
// type whose payload epoch is epoch, in one statement; it says whether it
// wrote. The check takes no lock: two transactions appending the same
// epoch at once could both write, so a caller must be the Run's only
// writer of that type.
func AppendOncePerEpoch(ctx context.Context, tx pgx.Tx, e Event, epoch int) (bool, error) {
	_, args, err := row(e)
	if err != nil {
		return false, err
	}
	tag, err := tx.Exec(ctx, insertColumns+`
		SELECT $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15::jsonb
		WHERE NOT EXISTS (SELECT 1 FROM events WHERE run_id = $7 AND event_type = $3 AND (payload->>'epoch')::int = $16)`,
		append(args, epoch)...)
	return tag.RowsAffected() == 1, err
}

// row is e's new id and the 15 values of insertColumns, in order.
func row(e Event) (string, []any, error) {
	id := ids.New(ids.Event)
	payload := []byte("{}")
	if e.Payload != nil {
		var err error
		if payload, err = json.Marshal(e.Payload); err != nil {
			return "", nil, err
		}
	}
	occurred := e.OccurredAt
	if occurred.IsZero() {
		occurred = time.Now()
	}
	return id, []any{id, e.OrganizationID, e.Type, occurred,
		db.Nullable(e.ProjectID), db.Nullable(e.TaskID), db.Nullable(e.RunID), db.Nullable(e.SessionID), db.Nullable(e.WorkflowRunID),
		e.ActorType, e.ActorID, e.Source, db.Nullable(e.CorrelationID), db.Nullable(e.CausationID), payload}, nil
}
