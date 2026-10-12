package delivery

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/db"
	"github.com/marciomartins/dude/orchestrator/internal/ledger"
	"github.com/marciomartins/dude/orchestrator/internal/workflow"
)

// Task relations (docs/design/task-relations.md): a `blocks` edge A → B
// holds B's start until A is merged — done, with a merged pull request.
// task_blocked_by (migration 111) is the rule; RefuseBlockedTx is every
// start path's guard over it.

// Events about relations and starts that wait on them.
const (
	EvRelationAdded        = "task.relation_added"
	EvRelationRemoved      = "task.relation_removed"
	EvTaskUnblocked        = "task.unblocked"
	EvBlockerUnmerged      = "task.blocker_unmerged"
	EvAutoStartSet         = "task.auto_start_set"
	EvAutoStartCancelled   = "task.auto_start_cancelled"
	EvAutoStarted          = "task.auto_started"
	EvAutoStartRefused     = "task.auto_start_refused"
	EvStartRefused         = "task.start_refused"
	EvStartOverridden      = "task.start_overridden"
	EvRelationsChanged     = "relations.changed"
	SignalRelationsUnblock = "relations.unblocked"
	// WakeUnblocked is the conductor's reason when its task can start.
	WakeUnblocked = "unblocked"
)

// Blocker is an open blocker of a task, as task_blocked_by returns it.
type Blocker struct {
	ID     string `json:"-"`
	Key    string `json:"key"`
	Status string `json:"status"`
}

// Unmerged says the blocker is done with nothing merged: settled for its
// owner, and it will never release anything.
func (b Blocker) Unmerged() bool { return b.Status == "done" }

// stateWord names a blocker's state the way every refusal does.
var stateWord = map[string]string{
	"review": "in review", "running": "in progress", "awaiting_input": "needs a person", "ready_to_merge": "ready to merge",
	"failed": "failed", "aborted": "aborted", "received": "not started", "queued": "not started", "intake": "being shaped",
	"awaiting_confirmation": "plan to confirm", "done": "done, nothing merged",
}

// Words is "CP-41 (in review)".
func (b Blocker) Words() string {
	w := stateWord[b.Status]
	if w == "" {
		w = strings.ReplaceAll(b.Status, "_", " ")
	}
	return fmt.Sprintf("%s (%s)", b.Key, w)
}

// KeyList is "CP-41 and CP-43", "CP-40, CP-41 and CP-43".
func KeyList(keys []string) string {
	switch len(keys) {
	case 0:
		return ""
	case 1:
		return keys[0]
	}
	return strings.Join(keys[:len(keys)-1], ", ") + " and " + keys[len(keys)-1]
}

// OpenBlockers is the task's blockers not yet merged.
func OpenBlockers(ctx context.Context, tx pgx.Tx, taskID string) ([]Blocker, error) {
	rows, err := tx.Query(ctx, `SELECT id, key, status::text FROM task_blocked_by($1)`, taskID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Blocker, error) {
		var b Blocker
		err := r.Scan(&b.ID, &b.Key, &b.Status)
		return b, err
	})
}

// BlockedError is a start refused because the task waits on unmerged work.
// Its text is the same on every start path.
type BlockedError struct {
	TaskKey  string
	Blockers []Blocker
}

func (e BlockedError) Error() string {
	words := make([]string, len(e.Blockers))
	for i, b := range e.Blockers {
		words[i] = b.Words()
	}
	once := "they're"
	if len(e.Blockers) == 1 {
		once = "that's"
	}
	return fmt.Sprintf("%s is blocked by %s: it can start once %s merged.", e.TaskKey, KeyList(words), once)
}

// AgentRefusal is what a conductor is told when a start of its task is
// refused as blocked.
func (e BlockedError) AgentRefusal() string {
	return e.Error() + " You can't start it anyway; a person can. Tell the person, and end your turn: you are woken when it can start."
}

// BlockersPayload is the blockers as events carry them: [{key, status}].
func BlockersPayload(bs []Blocker) []map[string]any {
	out := make([]map[string]any, len(bs))
	for i, b := range bs {
		out[i] = map[string]any{"key": b.Key, "status": b.Status}
	}
	return out
}

// TaskKeyOf is a task's key (CP-44).
func TaskKeyOf(ctx context.Context, tx pgx.Tx, taskID string) (string, error) {
	var key string
	err := tx.QueryRow(ctx, `SELECT p.key_prefix || '-' || t.number FROM tasks t JOIN projects p ON p.id = t.project_id
		WHERE t.id = $1`, taskID).Scan(&key)
	return key, err
}

// blockedRecheck is how often a delivery parked on its blockers looks
// again without a signal: the backstop for a blocker released some way
// that sends none (its edge removed).
const blockedRecheck = 5 * time.Minute

// parkIfBlocked is path 5: the implement step of a delivery whose task
// gained an open blocker after it started. The task goes back to queued,
// saying what it waits on, and the step parks on relations.unblocked;
// nothing fails. The refusal is recorded once per blocker set.
func (s *Store) parkIfBlocked(ctx context.Context, org string, st *State) (bool, error) {
	parked := false
	err := s.DB.InOrg(ctx, org, func(tx pgx.Tx) error {
		err := RefuseBlockedTx(ctx, tx, st.TaskID)
		blocked, ok := AsBlocked(err)
		if !ok {
			return err
		}
		ran, err := implementedOnAttempt(ctx, tx, st)
		if err != nil || ran {
			return err
		}
		parked = true
		keys := make([]string, len(blocked.Blockers))
		for i, b := range blocked.Blockers {
			keys[i] = b.Key
		}
		moved, err := SetTaskStatusTx(ctx, tx, org, st.ProjectID, st.TaskID, "", "queued", "blocked by "+KeyList(keys))
		if err != nil || !moved {
			return err
		}
		return RecordStartRefusedTx(ctx, tx, org, st.TaskID, "implement", "", "", blocked.Blockers)
	})
	return parked, err
}

// refuseBlockedStart is path 4: the conductor's first implementer on a
// task that gained an open blocker while it planned. A person's override
// lets it through; otherwise the conductor is told, as a Refusal. (The
// refusal rolls the tool call back, so no task.start_refused is kept here.)
func refuseBlockedStart(ctx context.Context, tx pgx.Tx, ref RunRef, st *State) error {
	if st.Overridden {
		return nil
	}
	err := RefuseBlockedTx(ctx, tx, ref.TaskID)
	if blocked, ok := AsBlocked(err); ok {
		return Refusal{blocked.AgentRefusal()}
	}
	return err
}

// OverrideParkedTx is Start anyway on a task whose delivery is parked at
// its implement step on blockers (path 5): recorded, marked overridden,
// and woken. Returns the delivery's id, "" when it is not parked there.
func OverrideParkedTx(ctx context.Context, tx pgx.Tx, org, taskID string, d *Delivery, o Override) (string, error) {
	if d.Step != "implementRun" || d.Status != "waiting" {
		return "", nil
	}
	blockers, err := OpenBlockers(ctx, tx, taskID)
	if err != nil {
		return "", err
	}
	if err := RecordOverrideTx(ctx, tx, org, taskID, blockers, o); err != nil {
		return "", err
	}
	if _, err := tx.Exec(ctx, `UPDATE workflow_runs SET state = state || '{"overridden": true}'::jsonb WHERE id = $1`, d.WorkflowID); err != nil {
		return "", err
	}
	return d.WorkflowID, workflow.SignalTx(ctx, tx, org, d.WorkflowID, SignalRelationsUnblock, map[string]any{"override": true}, "")
}

// RefuseBlockedTx is the guard every start path takes: the task's row
// locked (after its delivery's, as LockEscalationTx orders them), then its
// open blockers read; a BlockedError when there is any.
func RefuseBlockedTx(ctx context.Context, tx pgx.Tx, taskID string) error {
	if _, err := tx.Exec(ctx, `SELECT 1 FROM tasks WHERE id = $1 FOR NO KEY UPDATE`, taskID); err != nil {
		return err
	}
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

// AsBlocked is err's BlockedError, if it is one.
func AsBlocked(err error) (BlockedError, bool) {
	var b BlockedError
	ok := errors.As(err, &b)
	return b, ok
}

// RecordStartRefusedTx records a refused start (task.start_refused), so
// refusals are auditable; never shown as a line.
func RecordStartRefusedTx(ctx context.Context, tx pgx.Tx, org, taskID, path, actorType, actorID string, blockers []Blocker) error {
	var projectID string
	if err := tx.QueryRow(ctx, `SELECT project_id FROM tasks WHERE id = $1`, taskID).Scan(&projectID); err != nil {
		return err
	}
	if actorType == "" {
		actorType, actorID = ledger.ActorSystem, "dude"
	}
	_, err := ledger.Append(ctx, tx, ledger.Event{Type: EvStartRefused, OrganizationID: org, ProjectID: projectID, TaskID: taskID,
		ActorType: actorType, ActorID: actorID, Source: ledger.SourceOrchestrator, CorrelationID: taskID,
		Payload: map[string]any{"path": path, "blockers": BlockersPayload(blockers)}})
	return err
}

// Override is a person starting a blocked task anyway: who, how and why.
type Override struct {
	ActorType, ActorID string
	How, Note          string
}

// RecordOverrideTx records a person's start past the blockers
// (task.start_overridden), for Activity.
func RecordOverrideTx(ctx context.Context, tx pgx.Tx, org, taskID string, blockers []Blocker, o Override) error {
	var projectID string
	if err := tx.QueryRow(ctx, `SELECT project_id FROM tasks WHERE id = $1`, taskID).Scan(&projectID); err != nil {
		return err
	}
	payload := map[string]any{"blockers": BlockersPayload(blockers), "how": o.How}
	if n := strings.TrimSpace(o.Note); n != "" {
		payload["note"] = clip(n, 2000)
	}
	_, err := ledger.Append(ctx, tx, ledger.Event{Type: EvStartOverridden, OrganizationID: org, ProjectID: projectID, TaskID: taskID,
		ActorType: o.ActorType, ActorID: o.ActorID, Source: ledger.SourceOrchestrator, CorrelationID: taskID, Payload: payload})
	return err
}

// GuardStartTx is a start's guard with a person's override: nil when
// nothing blocks it; with override, nothing blocks it once recorded;
// otherwise the BlockedError.
func GuardStartTx(ctx context.Context, tx pgx.Tx, org, taskID string, override *Override) error {
	err := RefuseBlockedTx(ctx, tx, taskID)
	blocked, ok := AsBlocked(err)
	if !ok || override == nil {
		return err
	}
	return RecordOverrideTx(ctx, tx, org, taskID, blocked.Blockers, *override)
}

// implementedOnAttempt says an implementer has run on the delivery's
// attempt: past that, path 5's guard no longer applies.
func implementedOnAttempt(ctx context.Context, tx pgx.Tx, st *State) (bool, error) {
	var ran bool
	err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM runs WHERE task_id = $1 AND phase = 'implement'
		AND attempt = COALESCE(NULLIF($2, 0), (SELECT max(attempt) FROM runs WHERE task_id = $1)))`, st.TaskID, st.Attempt).Scan(&ran)
	if db.IsNotFound(err) {
		return false, nil
	}
	return ran, err
}
