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
	"github.com/marciomartins/dude/orchestrator/internal/redact"
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
// rolls back with the state change that produced it. An event of a Run is
// redacted with that Run's secret values first (Redactor).
func Append(ctx context.Context, tx pgx.Tx, e Event) (string, error) {
	if err := redactRun(ctx, tx, &e); err != nil {
		return "", err
	}
	id, args, err := row(e)
	if err != nil {
		return "", err
	}
	_, err = tx.Exec(ctx, insertColumns+`
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15::jsonb)`, args...)
	return id, err
}

// Redactor is the redactor of a Run's text: its secret values, as
// run_secret_values (migration 129) reads them in tx. Empty for a Run given
// none, and in a transaction whose role may not read them.
func Redactor(ctx context.Context, tx pgx.Tx, runID string) (*redact.Redactor, error) {
	if runID == "" {
		return nil, nil
	}
	rows, err := tx.Query(ctx, `SELECT name, value FROM run_secret_values($1)`, runID)
	if err != nil {
		return nil, err
	}
	values, err := pgx.CollectRows(rows, pgx.RowToStructByPos[redact.Secret])
	if err != nil || len(values) == 0 {
		return nil, err
	}
	return redact.FromList(values), nil
}

// redactRun redacts e's payload with its Run's values, through its JSON:
// a payload may carry structs as well as maps and strings.
func redactRun(ctx context.Context, tx pgx.Tx, e *Event) error {
	if e.RunID == "" || e.Payload == nil {
		return nil
	}
	r, err := Redactor(ctx, tx, e.RunID)
	if err != nil || r.Empty() {
		return err
	}
	raw, err := json.Marshal(e.Payload)
	if err != nil {
		return err
	}
	if _, found := r.Contains(string(raw)); !found {
		return nil
	}
	var payload map[string]any
	if err := json.Unmarshal(r.JSON(raw), &payload); err != nil {
		return err
	}
	e.Payload = payload
	return nil
}

// AppendOncePerEpoch writes e unless e's Run already has an event of e's
// type whose payload epoch is epoch, in one statement; it says whether it
// wrote. The check takes no lock: two transactions appending the same
// epoch at once could both write, so a caller must be the Run's only
// writer of that type.
func AppendOncePerEpoch(ctx context.Context, tx pgx.Tx, e Event, epoch int) (bool, error) {
	if err := redactRun(ctx, tx, &e); err != nil {
		return false, err
	}
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
