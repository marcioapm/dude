package delivery

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/db"
	"github.com/marciomartins/dude/orchestrator/internal/ids"
	"github.com/marciomartins/dude/orchestrator/internal/ledger"
)

// Filing: what a member's File click does with a session's proposal. It
// acts as that member, exactly as if they had typed it — a task or epic is
// theirs, an edit is allowed only on a task they own that has not started,
// a comment is theirs — and carries nothing of the session: no marker, no
// link, nothing in the work's events. What was filed is recorded on the
// session's side only (session_filings).

// Filer is the member pressing File: the ledger's actor and their person.
type Filer struct {
	ActorType, ActorID, Person string
}

// event is a ledger event by the filer, as the control plane writes a
// person's own (source control-plane, the task's correlation).
func (f Filer) event(org, typ, projectID, taskID string, payload map[string]any) ledger.Event {
	return ledger.Event{Type: typ, OrganizationID: org, ProjectID: projectID, TaskID: taskID, ActorType: f.ActorType,
		ActorID: f.ActorID, Source: ledger.SourceOrchestrator, CorrelationID: taskID, Payload: payload}
}

// EvTaskComment is a person's comment on a task, shown in its Activity.
const EvTaskComment = "task.comment"

// CreateEpicTx makes an epic in a project, last in its order, as the
// filer. Returns its id.
func CreateEpicTx(ctx context.Context, tx pgx.Tx, org, projectID string, f Filer, title, description string) (string, error) {
	// The project row serialises changes to its epics' order.
	if _, err := tx.Exec(ctx, `SELECT 1 FROM projects WHERE id = $1 FOR UPDATE`, projectID); err != nil {
		return "", err
	}
	id := ids.New(ids.Epic)
	if _, err := tx.Exec(ctx, `INSERT INTO epics (id, organization_id, project_id, title, description, position)
		VALUES ($1, $2, $3, $4, $5, (SELECT COALESCE(max(position) + 1, 0) FROM epics WHERE project_id = $3))`,
		id, org, projectID, title, description); err != nil {
		return "", err
	}
	_, err := ledger.Append(ctx, tx, f.event(org, "epic.created", projectID, "", map[string]any{"epicId": id, "title": title}))
	return id, err
}

// CreateTaskTx makes a task in a project, owned by the filer, not started:
// what the control plane's create does. Returns its id and key.
func CreateTaskTx(ctx context.Context, tx pgx.Tx, org, projectID string, f Filer, epicID, title, goal string,
	criteria []string) (string, string, error) {
	var number int
	var prefix string
	if err := tx.QueryRow(ctx, `UPDATE projects SET next_task_number = next_task_number + 1
		WHERE id = $1 RETURNING next_task_number - 1, key_prefix`, projectID).Scan(&number, &prefix); err != nil {
		return "", "", err
	}
	id := ids.New(ids.Task)
	raw, _ := json.Marshal(db.NonNil(criteria))
	if _, err := tx.Exec(ctx, `INSERT INTO tasks (id, organization_id, project_id, number, epic_id, title, goal,
			acceptance_criteria, status)
		VALUES ($1, $2, $3, $4, NULLIF($5, ''), $6, $7, $8::jsonb, 'received')`,
		id, org, projectID, number, epicID, title, goal, raw); err != nil {
		return "", "", err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO task_people (task_id, person_id, organization_id, position) VALUES ($1, $2, $3, 0)`,
		id, f.Person, org); err != nil {
		return "", "", err
	}
	if err := SyncTaskImagesTx(ctx, tx, id, goal, criteria); err != nil {
		return "", "", err
	}
	if _, err := ledger.Append(ctx, tx, f.event(org, "task.created", projectID, id, map[string]any{"title": title, "goal": goal})); err != nil {
		return "", "", err
	}
	return id, fmt.Sprintf("%s-%d", prefix, number), nil
}

// TaskOwnerNamed is TaskOwner with the owner's name.
func TaskOwnerNamed(ctx context.Context, tx pgx.Tx, taskID string) (id, name string, err error) {
	err = tx.QueryRow(ctx, `SELECT p.id, p.name FROM task_people tp JOIN people p ON p.id = tp.person_id
		WHERE tp.task_id = $1 AND p.removed_at IS NULL ORDER BY tp.position, tp.person_id LIMIT 1`, taskID).Scan(&id, &name)
	if db.IsNotFound(err) {
		return "", "", nil
	}
	return id, name, err
}

// TaskStarted is the control plane's rule for a task's text: fixed once a
// delivery exists, unless it stopped (aborted or failed).
func TaskStarted(ctx context.Context, tx pgx.Tx, taskID string) (bool, string, error) {
	var started bool
	var status string
	err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM workflow_runs WHERE task_id = t.id)
			AND t.status NOT IN ('aborted', 'failed'), t.status::text
		FROM tasks t WHERE t.id = $1 FOR UPDATE`, taskID).Scan(&started, &status)
	return started, status, err
}

// EditTaskTx writes a new goal and/or criteria into a task as the filer,
// recorded as a person's edit is (task.updated with the fields changed).
// The caller checked the filer owns it and it has not started.
func EditTaskTx(ctx context.Context, tx pgx.Tx, org, taskID string, f Filer, text TaskText) error {
	var projectID, goal string
	var raw []byte
	if err := tx.QueryRow(ctx, `SELECT project_id, goal, acceptance_criteria FROM tasks WHERE id = $1 FOR UPDATE`, taskID).
		Scan(&projectID, &goal, &raw); err != nil {
		return err
	}
	var criteria []string
	_ = json.Unmarshal(raw, &criteria)
	payload := map[string]any{}
	if text.Goal != nil && strings.TrimSpace(*text.Goal) != goal {
		goal = strings.TrimSpace(*text.Goal)
		payload["goal"] = goal
	}
	if text.AcceptanceCriteria != nil {
		next, _ := json.Marshal(db.NonNil(*text.AcceptanceCriteria))
		if string(next) != string(raw) {
			criteria = *text.AcceptanceCriteria
			payload["acceptanceCriteria"] = db.NonNil(criteria)
		}
	}
	if len(payload) == 0 {
		return nil
	}
	next, _ := json.Marshal(db.NonNil(criteria))
	if _, err := tx.Exec(ctx, `UPDATE tasks SET goal = $2, acceptance_criteria = $3::jsonb, updated_at = now() WHERE id = $1`,
		taskID, goal, next); err != nil {
		return err
	}
	if err := SyncTaskImagesTx(ctx, tx, taskID, goal, criteria); err != nil {
		return err
	}
	_, err := ledger.Append(ctx, tx, f.event(org, EvTaskUpdated, projectID, taskID, payload))
	return err
}

// CommentTx posts a comment on a task as the filer.
func CommentTx(ctx context.Context, tx pgx.Tx, org, taskID string, f Filer, text string) error {
	var projectID string
	if err := tx.QueryRow(ctx, `SELECT project_id FROM tasks WHERE id = $1`, taskID).Scan(&projectID); err != nil {
		return err
	}
	_, err := ledger.Append(ctx, tx, f.event(org, EvTaskComment, projectID, taskID, map[string]any{"text": text}))
	return err
}
