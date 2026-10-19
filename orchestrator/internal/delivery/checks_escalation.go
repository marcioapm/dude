package delivery

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/marciomartins/dude/orchestrator/internal/ledger"
)

const ReasonChecksUnverified = "checks_unverified"

// EscalateChecksTx holds the delivery at its existing wait step without
// ending the Run or losing its session. A waiver releases that same turn.
func EscalateChecksTx(ctx context.Context, tx pgx.Tx, org, taskID, runID, message string, rounds int) error {
	d, err := LoadDelivery(ctx, tx, taskID)
	if err != nil {
		return err
	}
	if d == nil {
		return fmt.Errorf("checks gate has no delivery for Run %s", runID)
	}
	st := d.State
	if st.Escalation != nil {
		return nil
	}
	st.Escalation = &Escalation{Reason: ReasonChecksUnverified, At: d.Step, Detail: map[string]any{"runId": runID, "message": message, "rounds": rounds}}
	st.Escalations++
	raw, err := json.Marshal(st)
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE workflow_runs SET state=$2::jsonb,step='decide',status='waiting',awaiting_signals='["human.decision"]',wake_at=NULL WHERE id=$1`, d.WorkflowID, raw); err != nil {
		return err
	}
	if _, err = SetTaskStatusTx(ctx, tx, org, st.ProjectID, taskID, "", "awaiting_input", ReasonChecksUnverified); err != nil {
		return err
	}
	return emitTx(ctx, tx, org, &st, EvQuestionAsked, map[string]any{"kind": "escalation", "reason": ReasonChecksUnverified, "detail": st.Escalation.Detail, "actions": st.Escalation.Actions()})
}

func settleChecksTx(ctx context.Context, tx pgx.Tx, org, projectID, taskID string, e *Escalation, action, note string) error {
	id := e.RunID()
	if action == ActionWaive {
		var turn int64
		if err := tx.QueryRow(ctx, `SELECT COALESCE(max(cursor),0) FROM events WHERE run_id=$1 AND event_type='agent.session.stopped'`, id).Scan(&turn); err != nil {
			return err
		}
		_, err := ledger.Append(ctx, tx, RunRef{Org: org, ProjectID: projectID, TaskID: taskID, RunID: id}.Event("run.checks.judged", ledger.ActorHuman, map[string]any{"turn": turn, "verified": true, "waived": true, "message": note}))
		return err
	}
	if action == "retry" {
		var detail struct {
			Message string `json:"message"`
		}
		e.decode(&detail)
		_, _, err := QueueDirective(ctx, tx, RunRef{Org: org, ProjectID: projectID, TaskID: taskID, RunID: id}, Directive{Text: detail.Message, Scope: "run"})
		return err
	}
	return nil
}
