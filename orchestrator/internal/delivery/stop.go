package delivery

import (
	"context"
	"encoding/json"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/db"
	"github.com/marciomartins/dude/orchestrator/internal/workflow"
)

// Stopping a task's live work: a person's abort of one Run, their Stop of
// the whole task, and Close or Archive, which stop what is live before the
// task ends. Kept Runs (keep) are stopped and held in lux for a resume
// (phases.Syncer.end); the others are let go.

// liveStatus (SQL, over runs): a Run not yet ended.
const liveStatus = `status IN ('pending', 'scheduled', 'starting', 'running', 'paused')`

// LockLiveWorkTx takes the task's Chat lock and its live conductor's row,
// the order a conductor's children and Chat take them in, before the
// caller locks the delivery and the task (LockEscalationTx). Returns the
// live conductor, "" for none.
func LockLiveWorkTx(ctx context.Context, tx pgx.Tx, taskID string) (string, error) {
	if err := LockChat(ctx, tx, taskID); err != nil {
		return "", err
	}
	var id string
	err := tx.QueryRow(ctx, `SELECT r.id FROM runs r WHERE r.task_id = $1 AND `+LiveConductor+`
		AND r.source_kind IS NULL FOR UPDATE`, taskID).Scan(&id)
	if db.IsNotFound(err) {
		return "", nil
	}
	return id, err
}

// StopPhaseRunsTx aborts the task's live phase Runs, and runID with them
// when given. It selects by task, not attempt: a phase Run still live on an
// older attempt is stopped with the current one's. What they never read
// fails, and a pull request fix being
// checked on its candidate stops short of the pull request (its candidate
// stays in the state, so a recovery may still promote it). With keep, each
// lux has is kept to resume. Returns the Runs aborted.
func StopPhaseRunsTx(ctx context.Context, tx pgx.Tx, org, projectID, taskID, runID, reason string, keep bool) ([]string, error) {
	rows, err := tx.Query(ctx, `UPDATE runs SET status = 'aborted', control = 'abort', control_requested_at = now(),
		control_reason = $3, ended_at = now(), keep = $4 AND lux_run_id IS NOT NULL
		WHERE (id = $1 OR task_id = $2 AND kind = 'agent' AND phase IS NOT NULL) AND source_kind IS NULL AND `+liveStatus+`
		RETURNING id`, runID, taskID, db.Nullable(reason), keep)
	if err != nil {
		return nil, err
	}
	aborted, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, err
	}
	for _, id := range aborted {
		if err := FailUnreadTx(ctx, tx, RunRef{Org: org, ProjectID: projectID, TaskID: taskID, RunID: id}, time.Time{}, UnreadRunAborted); err != nil {
			return nil, err
		}
	}
	if d, err := ReadDelivery(ctx, tx, taskID); err != nil {
		return nil, err
	} else if d != nil && d.Live() && d.State.Candidate != nil {
		if err := AbandonCandidateTx(ctx, tx, org, taskID, d.State.Attempt, d.State.Candidate.Branch); err != nil {
			return nil, err
		}
	}
	return aborted, nil
}

// AbortConductorTx ends the task's conductor as aborted: its publishes
// that moved nothing are refused (ErrPublishMoving while one moves the
// task branch). With keep, lux keeps it for a resume to go on with the
// same conversation; without, the next message in Chat starts another.
// Its children are the caller's (StopChildrenTx, or let go as its parent
// ended).
func AbortConductorTx(ctx context.Context, tx pgx.Tx, ref RunRef, reason string, keep bool) error {
	if _, err := tx.Exec(ctx, `SELECT id FROM runs WHERE id = $1 FOR NO KEY UPDATE`, ref.RunID); err != nil {
		return err
	}
	if err := RefuseUnmovedTx(ctx, tx, ref, "your conductor was stopped before it was published"); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `UPDATE runs SET status = 'aborted', control = 'abort', control_requested_at = now(),
		control_reason = $2, ended_at = now(), keep = $3 AND lux_run_id IS NOT NULL WHERE id = $1`,
		ref.RunID, db.Nullable(reason), keep)
	return err
}

// StopChildrenTx aborts every live child the task's conductors started
// (AbortChildTx). Returns them.
func StopChildrenTx(ctx context.Context, tx pgx.Tx, taskID string) ([]string, error) {
	rows, err := tx.Query(ctx, `SELECT id FROM runs WHERE task_id = $1 AND source_kind = 'task_conductor'
		AND child_stop_reason IS NULL AND `+liveStatus+` ORDER BY created_at, id`, taskID)
	if err != nil {
		return nil, err
	}
	children, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, err
	}
	for _, id := range children {
		if err := AbortChildTx(ctx, tx, taskID, id); err != nil {
			return nil, err
		}
	}
	return children, nil
}

// Stopped is what StopAllTx stopped, and the task as it locked it.
type Stopped struct {
	ProjectID, TaskStatus string
	// The task's latest delivery, its row locked; nil for none.
	Delivery *Delivery
	// The conductor stopped, "" for none; its children; the phase Runs.
	Conductor           string
	Children, PhaseRuns []string
}

// All is every Run stopped: the phase Runs, the conductor, its children.
func (s Stopped) All() []string {
	out := slices.Clone(s.PhaseRuns)
	if s.Conductor != "" {
		out = append(out, s.Conductor)
	}
	return append(out, s.Children...)
}

// StopAllTx stops every live Run of the task, whatever its attempt — its
// phase Runs, its conductor and the conductors' children — for a person's
// Stop (keep: the phase Runs and the conductor kept to resume) or Close and
// Archive (nothing kept).
// Locks as the writers it races take them: the Chat and the conductor's
// row (lockChild's prefix), the delivery and the task (LockEscalationTx),
// then the phase Runs (RestartRunTx's order after the task) and each child.
func StopAllTx(ctx context.Context, tx pgx.Tx, org, taskID, reason string, keep bool) (Stopped, error) {
	var out Stopped
	if err := tx.QueryRow(ctx, `SELECT project_id FROM tasks WHERE id = $1`, taskID).Scan(&out.ProjectID); err != nil {
		return out, err
	}
	conductor, err := LockLiveWorkTx(ctx, tx, taskID)
	if err != nil {
		return out, err
	}
	if _, out.Delivery, err = LockEscalationTx(ctx, tx, taskID); err != nil {
		return out, err
	}
	if err := tx.QueryRow(ctx, `SELECT status::text FROM tasks WHERE id = $1`, taskID).Scan(&out.TaskStatus); err != nil {
		return out, err
	}
	if out.PhaseRuns, err = StopPhaseRunsTx(ctx, tx, org, out.ProjectID, taskID, "", reason, keep); err != nil {
		return out, err
	}
	if out.Children, err = StopChildrenTx(ctx, tx, taskID); err != nil {
		return out, err
	}
	if conductor != "" {
		if err := AbortConductorTx(ctx, tx, RunRef{Org: org, ProjectID: out.ProjectID, TaskID: taskID, RunID: conductor}, reason, keep); err != nil {
			return out, err
		}
		out.Conductor = conductor
	}
	return out, nil
}

// HaltDeliveryTx stops the task's live delivery for a person's Stop, so a
// pick-up can carry it on (Recover): the conductor stopped with it named
// (Halt), an escalation waiting on a person stopped as Stop decides one
// (its questions closed, State.Stopped), and the workflow aborted. The
// caller holds the delivery's row (LoadDelivery).
func HaltDeliveryTx(ctx context.Context, tx pgx.Tx, org string, d *Delivery, conductor, why, actorType, actorID string) error {
	st := d.State
	st.Halt = &Halt{Conductor: conductor}
	if len(st.PendingRunIDs) == 0 {
		st.Halt.At = d.Step
	}
	if e := st.Escalation; e != nil && e.Decided == nil {
		if err := closeEscalationQuestionsTx(ctx, tx, org, st.ProjectID, st.TaskID, EscalationKey(d.WorkflowID, st.Escalations),
			EscalationDecision{Action: "stop", ActorType: actorType, ActorID: actorID}); err != nil {
			return err
		}
		st.Stopped, st.Escalation = e, nil
	}
	raw, err := json.Marshal(st)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE workflow_runs SET state = $2::jsonb WHERE id = $1`, d.WorkflowID, raw); err != nil {
		return err
	}
	d.State = st
	return workflow.AbortTx(ctx, tx, d.WorkflowID, why)
}
