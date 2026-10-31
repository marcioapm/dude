package delivery

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/db"
	"github.com/marciomartins/dude/orchestrator/internal/ledger"
)

// WakeRecovered is a recovery of the conductor's that ended: pushed,
// failed, or refused for a secret.
const WakeRecovered = "recovered"

// EvRunRecovered is the ledger event of a recovery's end, on the Run whose
// work it recovered. Payload: {recoveryId, branch, status, results, dirty,
// error, by}.
const EvRunRecovered = "run.recovered"

// RecoveryEnd is how a recovery ended, as the worker found it.
type RecoveryEnd struct {
	ID, Org, ProjectID, TaskID, RunID, Branch string
	// The worker's claim on it.
	Claim  string
	Status string
	// Per repository: what was pushed, or why not.
	Results []RecoveredRepo
	// Repositories whose uncommitted work was not captured.
	Dirty []string
	Error string
}

// RecoveredRepo is one repository's outcome: pushed with its commit,
// up-to-date, rejected or failed with why.
type RecoveredRepo struct {
	Repo   string `json:"repo"`
	Status string `json:"status"`
	Commit string `json:"commit,omitempty"`
	Error  string `json:"error,omitempty"`
}

// RecoveryEndedTx records a recovery's end: its status and results, the
// ledger's run.recovered on the Run it recovered, and — unless the task
// has ended meanwhile — one wake of its conductor (key recover:<id>).
// ErrClaimLost when the worker no longer holds it, or it has ended.
// Locks: Chat, then the recovery's row.
func RecoveryEndedTx(ctx context.Context, tx pgx.Tx, end RecoveryEnd) error {
	if err := LockChat(ctx, tx, end.TaskID); err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `UPDATE run_recoveries SET status = $3, result = $4::jsonb, dirty = $5, error = NULLIF($6, ''),
			ended_at = now(), next_attempt_at = NULL, claim_token = NULL, claimed_until = NULL
		WHERE id = $1 AND claim_token = $2 AND ended_at IS NULL`,
		end.ID, end.Claim, end.Status, db.NonNil(end.Results), db.NonNil(end.Dirty), clip(end.Error, 1000))
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrClaimLost
	}
	if _, err := ledger.Append(ctx, tx, ledger.Event{Type: EvRunRecovered, OrganizationID: end.Org, ProjectID: end.ProjectID,
		TaskID: end.TaskID, RunID: end.RunID, ActorType: ledger.ActorSystem, ActorID: "recovery", Source: ledger.SourceOrchestrator,
		CorrelationID: end.TaskID, Payload: map[string]any{"recoveryId": end.ID, "branch": end.Branch, "status": end.Status,
			"results": db.NonNil(end.Results), "dirty": db.NonNil(end.Dirty), "error": end.Error, "by": RoleConductor}}); err != nil {
		return err
	}
	var taskStatus string
	if err := tx.QueryRow(ctx, `SELECT status::text FROM tasks WHERE id = $1`, end.TaskID).Scan(&taskStatus); err != nil {
		return err
	}
	if Ended(taskStatus) {
		// A task a person stopped for a pick-up still hears it, once picked
		// up (WakeConductorTx holds it till then); one that ended for good
		// has nobody left to tell: the push and its event are the record.
		halted, err := HaltedTx(ctx, tx, end.TaskID)
		if err != nil || !halted {
			return err
		}
	}
	_, err = RecordWakeTx(ctx, tx, end.Org, end.TaskID, WakeRecovered, "recover:"+end.ID, recoveredLine(end))
	return err
}

// recoveredLine tells the conductor how its recovery ended.
func recoveredLine(end RecoveryEnd) string {
	var pushed, other []string
	for _, r := range end.Results {
		switch r.Status {
		case "pushed", "up-to-date":
			pushed = append(pushed, r.Repo+" at "+shortSHA(r.Commit))
		default:
			other = append(other, fmt.Sprintf("%s %s: %s", r.Repo, r.Status, r.Error))
		}
	}
	return render(recoveredTmpl, struct {
		ID, Run, Branch, Status, Error string
		Pushed, Other, Dirty           []string
	}{end.ID, end.RunID, end.Branch, end.Status, end.Error, pushed, other, end.Dirty})
}

func shortSHA(sha string) string { return sha[:min(len(sha), 12)] }
