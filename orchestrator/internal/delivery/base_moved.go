package delivery

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/ids"
	"github.com/marciomartins/dude/orchestrator/internal/ledger"
)

// NotifyBaseMovedTx tells other conducted deliveries that a tracked pull
// request merged into the base they measure against. The conductor wake and
// Chat notice share one durable key per task and merged pull request.
func NotifyBaseMovedTx(ctx context.Context, tx pgx.Tx, org, prID string) error {
	var mergingTask, repoID, repo, base, key string
	var number int
	if err := tx.QueryRow(ctx, `SELECT pr.task_id, pr.repository_id, r.name, pr.base_branch, pr.number,
			p.key_prefix || '-' || t.number
		FROM pull_requests pr JOIN repositories r ON r.id = pr.repository_id
		JOIN tasks t ON t.id = pr.task_id JOIN projects p ON p.id = t.project_id
		WHERE pr.id = $1 AND pr.state = 'merged'`, prID).Scan(&mergingTask, &repoID, &repo, &base, &number, &key); err != nil {
		return err
	}
	rows, err := tx.Query(ctx, `SELECT t.id, t.project_id
		FROM task_repositories tr JOIN repositories r ON r.id = tr.repository_id
		JOIN tasks t ON t.id = tr.task_id
		JOIN LATERAL (SELECT w.state FROM workflow_runs w WHERE w.task_id = t.id AND w.workflow_type = $4
			AND w.status IN ('running', 'waiting') ORDER BY w.created_at DESC LIMIT 1) w ON true
		WHERE tr.repository_id = $1 AND tr.access = 'write' AND t.id <> $2
		  AND t.status NOT IN ('done', 'aborted', 'failed')
		  AND COALESCE(NULLIF(w.state->'bases'->>r.name, ''), r.default_branch) = $3
		  AND EXISTS (SELECT 1 FROM runs c WHERE c.task_id = t.id AND c.role = 'conductor' AND c.kind = 'agent'
		    AND c.status IN ('pending', 'scheduled', 'starting', 'running', 'paused'))
		ORDER BY t.id`, repoID, mergingTask, base, WorkflowType)
	if err != nil {
		return err
	}
	type target struct{ TaskID, ProjectID string }
	targets, err := pgx.CollectRows(rows, pgx.RowToStructByPos[target])
	if err != nil {
		return err
	}
	line := fmt.Sprintf("`%s` moved: %s merged (#%d). Call sync_base if your work depends on it.", base, key, number)
	wakeKey := heldWakePrefix + prID
	for _, t := range targets {
		tag, err := tx.Exec(ctx, `INSERT INTO conductor_wakes (id, organization_id, task_id, kind, key, line)
			VALUES ($1, $2, $3, 'checkout', $4, $5) ON CONFLICT (task_id, key) DO NOTHING`, ids.New("cwk"), org, t.TaskID, wakeKey, line)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			continue
		}
		if _, err := ledger.Append(ctx, tx, ledger.Event{Type: EvChatNotice, OrganizationID: org, ProjectID: t.ProjectID,
			TaskID: t.TaskID, ActorType: ledger.ActorSystem, ActorID: "dude", Source: ledger.SourceOrchestrator,
			CorrelationID: t.TaskID, Payload: map[string]any{"about": "base_moved", "text": line, "repo": repo,
				"base": base, "number": number, "mergedTask": key}}); err != nil {
			return err
		}
	}
	return nil
}
