package delivery

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/ledger"
)

// ReasonChecksUnverified: a gated phase's checks report was sent back as
// many times as the organisation allows and is still not verified. Its
// Run is held, its work unpushed, until a person retries, waives or stops.
const ReasonChecksUnverified = "checks_unverified"

// Ledger events of the checks gate, on the Run: each judgement of a
// turn's report (payload {turn, round, verified, checks, message,
// skipped?, escalated?, waived?}), and each person's decision on a held
// report ({action, note}).
const (
	EvRunChecksJudged  = "run.checks.judged"
	EvRunChecksDecided = "run.checks.decided"
)

// EscalateChecksTx stops the delivery for a person at the step waiting on
// runID, whose report the gate will not pass: the Run keeps its session
// and its unpushed work, and the workflow resumes that wait once decided.
// A delivery already waiting on another escalation keeps it.
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
	st.Escalation = &Escalation{Reason: ReasonChecksUnverified, At: d.Step,
		Detail: map[string]any{"runId": runID, "message": message, "rounds": rounds}}
	if st.conducted() {
		st.Escalations++
		if _, err := RecordWakeTx(ctx, tx, org, taskID, "escalation", fmt.Sprintf("%s:escalation:%d", d.WorkflowID, st.Escalations),
			escalatedLine(ReasonChecksUnverified, st.Escalation.Actions())); err != nil {
			return err
		}
	}
	raw, err := json.Marshal(st)
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE workflow_runs SET state = $2::jsonb, step = 'decide', status = 'waiting',
		awaiting_signals = '["human.decision"]', wake_at = NULL WHERE id = $1`, d.WorkflowID, raw); err != nil {
		return err
	}
	if _, err = SetTaskStatusTx(ctx, tx, org, st.ProjectID, taskID, "", "awaiting_input", ReasonChecksUnverified); err != nil {
		return err
	}
	return emitTx(ctx, tx, org, &st, EvQuestionAsked, map[string]any{"kind": "escalation", "reason": ReasonChecksUnverified,
		"detail": st.Escalation.Detail, "actions": st.Escalation.Actions()})
}

// settleChecksTx carries a person's decision to the held Run, in the
// decision's transaction: a waiver passes the turn it was held at, so the
// gate lets its work be pushed with no further agent turn; a retry sends
// the report back once more, with the configured rounds again; stop ends
// the Run, its work unpushed.
func settleChecksTx(ctx context.Context, tx pgx.Tx, org, projectID, taskID string, e *Escalation, action, note string) error {
	id := e.RunID()
	ref := RunRef{Org: org, ProjectID: projectID, TaskID: taskID, RunID: id}
	if _, err := ledger.Append(ctx, tx, ref.Event(EvRunChecksDecided, ledger.ActorHuman,
		map[string]any{"action": action, "note": note})); err != nil {
		return err
	}
	switch action {
	case ActionWaive:
		turn, err := LatestTurnTx(ctx, tx, id)
		if err != nil {
			return err
		}
		_, err = ledger.Append(ctx, tx, ref.Event(EvRunChecksJudged, ledger.ActorHuman,
			map[string]any{"turn": turn, "verified": true, "waived": true, "message": note, "checks": []any{}}))
		return err
	case "retry":
		var detail struct {
			Message string `json:"message"`
		}
		e.decode(&detail)
		_, _, err := QueueDirective(ctx, tx, ref, Directive{Text: detail.Message, Scope: "run"})
		return err
	case "stop":
		_, err := tx.Exec(ctx, `UPDATE runs SET control = 'abort', control_requested_at = now(), control_reason = $2
			WHERE id = $1 AND status IN ('pending', 'scheduled', 'starting', 'running', 'paused')`, id, "a person stopped it at the checks gate")
		return err
	}
	return nil
}
