package delivery

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/db"
	"github.com/marciomartins/dude/orchestrator/internal/ledger"
)

// WhyEveryDelivery is why the boot sweep hands a delivery in flight to a
// quiet conductor (ConvertToQuietTx).
const WhyEveryDelivery = "every delivery has a conductor"

// What ConvertToQuietTx did with a delivery.
const (
	// Converted: its conductor decides it now, quietly.
	ConvertConverted = "converted"
	// ConvertAccepted: left Deliver's, as its person accepted Deliver's
	// defaults (AcceptedDefaultsTx).
	ConvertAccepted = "accepted"
	// ConvertMoving: the conductor's publish moves its branch now; the
	// next sweep converts it.
	ConvertMoving = "moving"
	// ConvertNone: nothing to convert — no live delivery, or one a
	// conductor decides already (another sweep, a Chat takeover).
	ConvertNone = "none"
)

// ConvertToQuietTx hands the task's live delivery, in flight from before
// every delivery had a conductor, to a quiet conductor, as a person's
// hand-back would (SetDeciderTx): an escalation it waits at is told, the
// change recorded. A delivery whose person accepted Deliver's defaults
// stays Deliver's. One parked on an untaken decision of the conductor's
// that is not Deliver's own (the start, the hold before the pull request,
// which a quiet conductor leaves to Deliver) gets the decision's wake,
// keyed as toConductorKindTx keys it, and its step run so its wait starts.
// Idempotent: it locks the task's Chat, then the delivery's row, and does
// nothing once converted. The caller commits tx.
func ConvertToQuietTx(ctx context.Context, tx pgx.Tx, org, taskID string) (string, error) {
	if err := LockChat(ctx, tx, taskID); err != nil {
		return "", err
	}
	d, err := LoadDelivery(ctx, tx, taskID)
	if err != nil || d == nil || !d.Live() || d.State.conducted() {
		return ConvertNone, err
	}
	accepted, err := AcceptedDefaultsTx(ctx, tx, taskID)
	if err != nil {
		return "", err
	}
	if accepted {
		return ConvertAccepted, nil
	}
	st := &d.State
	if _, err := SetDeciderTx(ctx, tx, org, d.WorkflowID, st, DeciderConductor, VoiceQuiet, WhyEveryDelivery,
		ledger.ActorSystem, "dude"); err != nil {
		if errors.Is(err, ErrPublishMoving) {
			return ConvertMoving, nil
		}
		return "", err
	}
	p := st.Decision
	if d.Step != "conductorDecision" || p == nil || p.Taken != nil || st.quietAt(p.Point) {
		return ConvertConverted, nil
	}
	// Parked at a decision Deliver's policy left for nobody: the quiet
	// conductor is woken for it, once, and its wait starts now.
	note := decisionWaitingNote(p.Point, "")
	added, err := recordWakeTx(ctx, tx, org, taskID, "decision", decisionWakeKey(d.WorkflowID, st.Decisions), note, false)
	if err != nil {
		return "", err
	}
	if added {
		if err := emitTx(ctx, tx, org, st, EvDecisionAwaited, map[string]any{"point": p.Point, "policy": p.Policy,
			"actions": p.Actions(), "phases": p.Phases(), "note": note}); err != nil {
			return "", err
		}
	}
	_, err = tx.Exec(ctx, `UPDATE workflow_runs SET wake_at = now() WHERE id = $1 AND status = 'waiting'`, d.WorkflowID)
	return ConvertConverted, err
}

// Converted counts what a sweep of ConvertToQuietTx did.
type Converted struct {
	Converted, Accepted, Moving int
}

// ConvertInFlight is the one-shot sweep every orchestrator runs at boot,
// and an operator may run again (dude-orchestrator convert-deliveries):
// every live delivery no conductor decides goes to a quiet one
// (ConvertToQuietTx), each in its own transaction. A delivery a person
// handed to Deliver by accepting its defaults is skipped; one whose branch
// the conductor's publish moves now is skipped, for the next sweep.
func ConvertInFlight(ctx context.Context, database *db.DB) (Converted, error) {
	var out Converted
	type row struct{ Org, TaskID string }
	var rows []row
	if err := database.InSystem(ctx, "convert-deliveries", func(tx pgx.Tx) error {
		r, err := tx.Query(ctx, `SELECT organization_id, task_id FROM workflow_runs
			WHERE workflow_type = $1 AND status IN ('running', 'waiting') AND task_id IS NOT NULL
			  AND COALESCE(state->>'decider', '') <> $2
			ORDER BY created_at`, WorkflowType, DeciderConductor)
		if err != nil {
			return err
		}
		rows, err = pgx.CollectRows(r, pgx.RowToStructByPos[row])
		return err
	}); err != nil {
		return out, err
	}
	for _, r := range rows {
		var did string
		if err := database.InOrg(ctx, r.Org, func(tx pgx.Tx) (err error) {
			did, err = ConvertToQuietTx(ctx, tx, r.Org, r.TaskID)
			return err
		}); err != nil {
			return out, fmt.Errorf("task %s: %w", r.TaskID, err)
		}
		switch did {
		case ConvertConverted:
			out.Converted++
		case ConvertAccepted:
			out.Accepted++
		case ConvertMoving:
			out.Moving++
		}
	}
	return out, nil
}
