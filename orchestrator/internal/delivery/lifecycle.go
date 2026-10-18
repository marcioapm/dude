package delivery

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/db"
	"github.com/marciomartins/dude/orchestrator/internal/ledger"
)

// Lifecycle (docs/design/tracking-work.md, Epic 1): a person closes a task
// with a resolution, and archives what is finished. Neither is a status:
// delivery still writes status; these columns are a person's.
//
// The start rule lives here: RefuseStartTx, the one guard every start path
// takes, reads task_start_refusal (migration 145) — archived, then closed,
// then blocked.

// Resolutions, as task_resolution stores them.
const (
	ResolutionWontDo         = "wont_do"
	ResolutionDuplicate      = "duplicate"
	ResolutionCantReproduce  = "cant_reproduce"
	ResolutionNothingToMerge = "nothing_to_merge"
)

// ResolutionLabel is each resolution as people read it.
var ResolutionLabel = map[string]string{
	ResolutionWontDo: "Won't do", ResolutionDuplicate: "Duplicate", ResolutionCantReproduce: "Can't reproduce",
	ResolutionNothingToMerge: "Nothing to merge",
}

// Why a start is refused, as task.start_refused and the 409's code say it.
const (
	RefusedArchived = "archived"
	RefusedClosed   = "closed"
	RefusedBlocked  = "blocked"
)

// ArchivedError is a start of an archived task: never overridden.
type ArchivedError struct{ TaskKey string }

func (e ArchivedError) Error() string {
	return fmt.Sprintf(mLifecycleArchived, e.TaskKey)
}

// ClosedError is a start of a task a person closed: never overridden.
// PickUp words it for Resume and Try again.
type ClosedError struct {
	TaskKey, Resolution string
	PickUp              bool
}

func (e ClosedError) Error() string {
	if e.PickUp {
		return fmt.Sprintf(mLifecycleClosedPickUp, e.TaskKey)
	}
	return fmt.Sprintf(mLifecycleClosed, e.TaskKey, ResolutionLabel[e.Resolution])
}

// AgentRefusal is what a conductor is told when its task's start is
// refused as archived or closed.
func agentLifecycleRefusal(err error) string {
	return fmt.Sprintf(mLifecycleAgent, err.Error())
}

// StartRefusalReason is err's reason when it is a start refused by the
// rule ("archived", "closed", "blocked"), else "".
func StartRefusalReason(err error) string {
	var a ArchivedError
	var c ClosedError
	var b BlockedError
	switch {
	case errors.As(err, &a):
		return RefusedArchived
	case errors.As(err, &c):
		return RefusedClosed
	case errors.As(err, &b):
		return RefusedBlocked
	}
	return ""
}

// lifecycleRefusal is err when it is an archived or closed refusal, nil otherwise.
func lifecycleRefusal(err error) error {
	switch StartRefusalReason(err) {
	case RefusedArchived, RefusedClosed:
		return err
	}
	return nil
}

// IsLifecycleRefusal says err refuses a start as archived or closed.
func IsLifecycleRefusal(err error) bool { return lifecycleRefusal(err) != nil }

// RefuseStartTx is the guard every start path takes: the task's row locked
// (after its delivery's, as LockEscalationTx orders them), then, in order,
// ArchivedError, ClosedError, BlockedError. Only the last is overridable,
// by a person (GuardStartTx).
func RefuseStartTx(ctx context.Context, tx pgx.Tx, taskID string) error {
	return startRefusalTx(ctx, tx, taskID, true)
}

// RefuseLifecycleTx is RefuseStartTx's first two steps alone: Resume and
// Try again (path 7) carry on the same attempt, which blockers don't hold,
// but a person's Close or Archive does.
func RefuseLifecycleTx(ctx context.Context, tx pgx.Tx, taskID string) error {
	return startRefusalTx(ctx, tx, taskID, false)
}

func startRefusalTx(ctx context.Context, tx pgx.Tx, taskID string, blocked bool) error {
	if _, err := tx.Exec(ctx, `SELECT 1 FROM tasks WHERE id = $1 FOR NO KEY UPDATE`, taskID); err != nil {
		return err
	}
	var refusal *string
	var key, resolution string
	err := tx.QueryRow(ctx, `SELECT task_start_refusal(t.id), p.key_prefix || '-' || t.number, COALESCE(t.resolution::text, '')
		FROM tasks t JOIN projects p ON p.id = t.project_id WHERE t.id = $1`, taskID).Scan(&refusal, &key, &resolution)
	if db.IsNotFound(err) {
		return nil
	}
	if err != nil || refusal == nil {
		return err
	}
	switch *refusal {
	case RefusedArchived:
		return ArchivedError{TaskKey: key}
	case RefusedClosed:
		return ClosedError{TaskKey: key, Resolution: resolution}
	}
	if !blocked {
		return nil
	}
	return refuseBlockedOnlyTx(ctx, tx, taskID)
}

// refuseBlockedOnlyTx is the third step alone, the row already locked.
func refuseBlockedOnlyTx(ctx context.Context, tx pgx.Tx, taskID string) error {
	blockers, err := OpenBlockers(ctx, tx, taskID)
	if err != nil || len(blockers) == 0 {
		return err
	}
	key, err := TaskKeyOf(ctx, tx, taskID)
	if err != nil {
		return err
	}
	return BlockedError{TaskKey: key, Blockers: blockers}
}

// RecordLifecycleRefusedTx records a start refused as archived or closed
// (task.start_refused {path, reason}); never shown as a line.
func RecordLifecycleRefusedTx(ctx context.Context, tx pgx.Tx, org, taskID, path, reason, actorType, actorID string) error {
	var projectID string
	if err := tx.QueryRow(ctx, `SELECT project_id FROM tasks WHERE id = $1`, taskID).Scan(&projectID); err != nil {
		return err
	}
	if actorType == "" {
		actorType, actorID = ledger.ActorSystem, "dude"
	}
	_, err := ledger.Append(ctx, tx, ledger.Event{Type: EvStartRefused, OrganizationID: org, ProjectID: projectID, TaskID: taskID,
		ActorType: actorType, ActorID: actorID, Source: ledger.SourceOrchestrator, CorrelationID: taskID,
		Payload: map[string]any{"path": path, "reason": reason}})
	return err
}

// RecordRefusalTx records any start the rule refused: blocked with its
// blockers, archived or closed with its reason.
func RecordRefusalTx(ctx context.Context, tx pgx.Tx, org, taskID, path, actorType, actorID string, err error) error {
	if b, ok := AsBlocked(err); ok {
		return RecordStartRefusedTx(ctx, tx, org, taskID, path, actorType, actorID, b.Blockers)
	}
	if reason := StartRefusalReason(err); reason != "" {
		return RecordLifecycleRefusedTx(ctx, tx, org, taskID, path, reason, actorType, actorID)
	}
	return nil
}
