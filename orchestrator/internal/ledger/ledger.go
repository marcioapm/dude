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

	"github.com/marciomartins/dude/orchestrator/internal/ids"
)

// Source values the events table uses (eventSourceSchema in the domain package).
const (
	SourceOrchestrator = "control-plane"
	SourceRunner       = "runner"
	SourceHarness      = "harness"
	SourceGitHub       = "github"
)

// Actor types.
const (
	ActorSystem      = "system"
	ActorAgent       = "agent"
	ActorHuman       = "human"
	ActorIntegration = "integration"
)

type Event struct {
	Type           string
	OrganizationID string
	ProjectID      string
	WorkItemID     string
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
	payload, err := json.Marshal(nonNil(e.Payload))
	if err != nil {
		return "", err
	}
	occurred := e.OccurredAt
	if occurred.IsZero() {
		occurred = time.Now()
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO events (id, organization_id, event_type, occurred_at,
		                    project_id, work_item_id, run_id, session_id, workflow_run_id,
		                    actor_type, actor_id, source, correlation_id, causation_id, payload)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15::jsonb)`,
		id, e.OrganizationID, e.Type, occurred,
		null(e.ProjectID), null(e.WorkItemID), null(e.RunID), null(e.SessionID), null(e.WorkflowRunID),
		e.ActorType, e.ActorID, e.Source, null(e.CorrelationID), null(e.CausationID), payload)
	return id, err
}

func nonNil(m map[string]any) map[string]any {
	if m == nil {
		return map[string]any{}
	}
	return m
}

// null turns "" into SQL NULL: the scope columns are nullable, and an empty
// string would read as "belongs to the entity with no id".
func null(s string) any {
	if s == "" {
		return nil
	}
	return s
}
