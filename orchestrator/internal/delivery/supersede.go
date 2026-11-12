package delivery

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/ledger"
)

// Superseding: a person starts a task on a pull request or branch another
// live task holds, and confirms the other is to stop for it. Every mode
// either way (review only, review and fix, continue); nothing automatic
// supersedes. The API stops the old task as a person's Stop does, and this
// attaches the source and links the two, all in the caller's transaction.

// Supersede events, one on each task.
const (
	EvTaskSuperseded  = "task.superseded"
	EvTaskSupersedes  = "task.supersedes"
	RelationSupersede = "supersedes"
)

// HolderUnderLockTx is the live task holding branch in repo for taskID,
// read under the repository's row lock held to tx's end; nil for none.
// expect, when set, is the holder a person confirmed: another is refused
// (superseded_required) as a changed holder.
func HolderUnderLockTx(ctx context.Context, tx pgx.Tx, repo, branch, taskID, expect string) (*SourceHolder, error) {
	if _, err := tx.Exec(ctx, `SELECT id FROM repositories WHERE id = $1 FOR UPDATE`, repo); err != nil {
		return nil, err
	}
	return supersedeOrRefuseTx(ctx, tx, repo, branch, taskID, expect != "", expect)
}

// SetSourceTx keeps src as the task's source, as the control plane's
// setTaskSource does: its repository one the task works on, with write
// access, or read for review only.
func SetSourceTx(ctx context.Context, tx pgx.Tx, org, taskID string, src Source) error {
	access := "write"
	if src.ReviewOnly() {
		access = "read"
	}
	var number *int
	if src.Number != 0 {
		number = &src.Number
	}
	if _, err := tx.Exec(ctx, `DELETE FROM task_sources WHERE task_id = $1`, taskID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO task_sources (task_id, organization_id, repository_id, kind, number, title, head_branch, base_branch, mode)
		VALUES ($1, $2, $3, $4, $5, NULLIF($6, ''), $7, $8, $9)`,
		taskID, org, src.RepositoryID, src.Kind, number, src.Title, src.HeadBranch, src.BaseBranch, src.Mode); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `INSERT INTO task_repositories (organization_id, task_id, repository_id, access)
		VALUES ($1, $2, $3, $4::repository_access)
		ON CONFLICT (task_id, repository_id) DO UPDATE SET access = EXCLUDED.access`, org, taskID, src.RepositoryID, access)
	return err
}

// RecordSupersedeTx links newID to the oldID it superseded (a supersedes
// edge, new → old) and says so on both tasks: "superseded by NEW" on the
// old, "supersedes OLD" on the new, each with the edge's own
// relation_added, by the person who confirmed it.
func RecordSupersedeTx(ctx context.Context, tx pgx.Tx, org, newID, oldID, person, actorType, actorID string) error {
	from, err := relatedTask(ctx, tx, newID)
	if err != nil {
		return err
	}
	to, err := relatedTask(ctx, tx, oldID)
	if err != nil {
		return err
	}
	if err := lockRelations(ctx, tx, org); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO task_relations (organization_id, from_task_id, to_task_id, kind, created_by_person)
		VALUES ($1, $2, $3, 'supersedes', $4) ON CONFLICT DO NOTHING`, org, newID, oldID, person); err != nil {
		return err
	}
	if err := relationEventsTx(ctx, tx, org, EvRelationAdded, Edge{Person: person}, from, to,
		map[string]any{"kind": RelationSupersede, "from": from.Key, "to": to.Key}); err != nil {
		return err
	}
	for _, e := range []struct {
		typ   string
		t     RelatedTask
		other RelatedTask
	}{{EvTaskSuperseded, to, from}, {EvTaskSupersedes, from, to}} {
		if _, err := ledger.Append(ctx, tx, ledger.Event{Type: e.typ, OrganizationID: org, ProjectID: e.t.ProjectID, TaskID: e.t.ID,
			ActorType: actorType, ActorID: actorID, Source: ledger.SourceOrchestrator, CorrelationID: from.ID + ":" + to.ID,
			Payload: map[string]any{"task": e.other.Key, "taskId": e.other.ID, "by": person}}); err != nil {
			return err
		}
	}
	return nil
}

// SupersededTx is the key of the task this one supersedes, "" for none:
// what its conductor reads first.
func SupersededTx(ctx context.Context, tx pgx.Tx, taskID string) (string, error) {
	var key string
	err := tx.QueryRow(ctx, `SELECT COALESCE((SELECT p.key_prefix || '-' || o.number FROM task_relations r
		JOIN tasks o ON o.id = r.to_task_id JOIN projects p ON p.id = o.project_id
		WHERE r.from_task_id = $1 AND r.kind = 'supersedes' ORDER BY r.created_at DESC LIMIT 1), '')`, taskID).Scan(&key)
	return key, err
}
