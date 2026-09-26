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

// Append writes one event in the caller's transaction, so it commits or
// rolls back with the state change that produced it.
func Append(ctx context.Context, tx pgx.Tx, e Event) (string, error) {
	id := ids.New(ids.Event)
	payload := []byte("{}")
	var err error
	if e.Payload != nil {
		payload, err = json.Marshal(e.Payload)
	}
	if err != nil {
		return "", err
	}
	occurred := e.OccurredAt
	if occurred.IsZero() {
		occurred = time.Now()
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO events (id, organization_id, event_type, occurred_at,
		                    project_id, task_id, run_id, session_id, workflow_run_id,
		                    actor_type, actor_id, source, correlation_id, causation_id, payload)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15::jsonb)`,
		id, e.OrganizationID, e.Type, occurred,
		db.Nullable(e.ProjectID), db.Nullable(e.TaskID), db.Nullable(e.RunID), db.Nullable(e.SessionID), db.Nullable(e.WorkflowRunID),
		e.ActorType, e.ActorID, e.Source, db.Nullable(e.CorrelationID), db.Nullable(e.CausationID), payload)
	return id, err
}
