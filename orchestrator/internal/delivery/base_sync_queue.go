package delivery

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/marciomartins/dude/orchestrator/internal/capabilities"
	"github.com/marciomartins/dude/orchestrator/internal/db"
	"github.com/marciomartins/dude/orchestrator/internal/ledger"
)

// QueueBaseSyncTx records the observed base, not the PR head: the same head
// can need another integration when its base advances. Caller holds delivery.
func QueueBaseSyncTx(ctx context.Context, tx pgx.Tx, org, taskID, prID, base, sha string) error {
	_, err := tx.Exec(ctx, `INSERT INTO base_sync_requests(organization_id,task_id,pull_request_id,base,base_sha)
 VALUES($1,$2,$3,$4,$5) ON CONFLICT DO NOTHING`, org, taskID, prID, base, sha)
	if err != nil {
		return err
	}
	var run, role, project string
	err = tx.QueryRow(ctx, `SELECT id, role, project_id FROM runs WHERE task_id=$1 AND phase IN `+writerPhases+`
 AND status IN ('starting','running','paused') ORDER BY created_at LIMIT 1`, taskID).Scan(&run, &role, &project)
	if db.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	grants, err := capabilities.Resolve(ctx, tx, capabilities.Agent{Role: role, Org: org, Project: project, Run: run})
	if err != nil || !grants.Has(capabilities.BaseSync) {
		return err
	}
	return RouteBaseSyncTx(ctx, tx, RunRef{Org: org, ProjectID: project, TaskID: taskID, RunID: run})
}

func baseSyncInstruction(repo, base, sha string) string {
	return fmt.Sprintf("Queued conflict resolution for %s (requested base %s): call sync_base, merge lux/%s into your current writer branch, resolve conflicts preserving both intents, run affected tests, and commit. Do not rebase. Keep candidate review gates; this does not authorize promotion or claim the public PR has changed.", repo, sha, base)
}

// RouteBaseSyncTx queues once per writer; requests remain pending until trusted
// forge ancestry confirms publication. A replacement writer gets them again.
func RouteBaseSyncTx(ctx context.Context, tx pgx.Tx, r RunRef) error {
	rows, err := tx.Query(ctx, `SELECT q.pull_request_id,q.base,q.base_sha,repo.name FROM base_sync_requests q
 JOIN pull_requests p ON p.id=q.pull_request_id JOIN repositories repo ON repo.id=p.repository_id
 WHERE q.task_id=$1 AND q.completed_at IS NULL AND q.writer_run_id IS DISTINCT FROM $2
 AND EXISTS(SELECT 1 FROM workflow_runs WHERE task_id=$1 AND workflow_type='task.delivery' AND status IN ('running','waiting'))
 FOR UPDATE OF q`, r.TaskID, r.RunID)
	if err != nil {
		return err
	}
	type request struct{ pr, base, sha, repo string }
	var requests []request
	for rows.Next() {
		var q request
		if err = rows.Scan(&q.pr, &q.base, &q.sha, &q.repo); err != nil {
			rows.Close()
			return err
		}
		requests = append(requests, q)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, q := range requests {
		if _, _, err = QueueDirective(ctx, tx, r, Directive{Text: baseSyncInstruction(q.repo, q.base, q.sha), Scope: "run"}); err != nil {
			return err
		}
		if _, err := ledger.Append(ctx, tx, r.Event(EvPullRequestAction, ledger.ActorSystem, map[string]any{"action": "base-sync-writing", "repo": q.repo, "runId": r.RunID, "base": q.base, "baseSha": q.sha})); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `UPDATE base_sync_requests SET writer_run_id=$3 WHERE pull_request_id=$1 AND base_sha=$2`, q.pr, q.sha, r.RunID); err != nil {
			return err
		}
	}
	return nil
}

// PendingBaseSyncPrompt is only for a capable writer's initial briefing.
func PendingBaseSyncPrompt(ctx context.Context, tx pgx.Tx, taskID string) (string, error) {
	rows, err := tx.Query(ctx, `SELECT repo.name,q.base,q.base_sha FROM base_sync_requests q
 JOIN pull_requests p ON p.id=q.pull_request_id JOIN repositories repo ON repo.id=p.repository_id
 WHERE q.task_id=$1 AND q.completed_at IS NULL ORDER BY q.created_at`, taskID)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	var lines []string
	for rows.Next() {
		var repo, base, sha string
		if err := rows.Scan(&repo, &base, &sha); err != nil {
			return "", err
		}
		lines = append(lines, baseSyncInstruction(repo, base, sha))
	}
	return strings.Join(lines, "\n\n"), rows.Err()
}
