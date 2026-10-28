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

// baseSyncInScope (SQL, over base_sync_requests q, $1 the task): a pending
// request on a pull request the task's live delivery tracks. A request on
// an earlier attempt's pull request is no later writer's to integrate.
const baseSyncInScope = `q.task_id=$1 AND q.completed_at IS NULL AND q.pull_request_id IN (
 SELECT jsonb_array_elements_text(COALESCE(w.state->'pullRequestIds','[]')) FROM (SELECT state, status FROM workflow_runs
 WHERE task_id=$1 AND workflow_type='` + WorkflowType + `' ORDER BY created_at DESC LIMIT 1) w WHERE w.status IN ('running','waiting'))`

// How a writer was told of a request (base_sync_requests.via).
const (
	baseSyncViaPrompt    = "prompt"
	baseSyncViaDirective = "directive"
)

// QueueBaseSyncTx records the observed base, not the PR head: the same head
// can need another integration when its base advances. Caller holds delivery.
// The task's current writer hears it now: a live one, or one created whose
// prompt may already be built (pending, scheduled), by directive, which waits
// until it runs.
func QueueBaseSyncTx(ctx context.Context, tx pgx.Tx, org, taskID, prID, base, sha string) error {
	_, err := tx.Exec(ctx, `INSERT INTO base_sync_requests(organization_id,task_id,pull_request_id,base,base_sha)
 VALUES($1,$2,$3,$4,$5) ON CONFLICT DO NOTHING`, org, taskID, prID, base, sha)
	if err != nil {
		return err
	}
	var run, role, project string
	err = tx.QueryRow(ctx, `SELECT id, role, project_id FROM runs WHERE task_id=$1 AND phase IN `+writerPhases+`
 AND status IN ('pending','scheduled','starting','running','paused') ORDER BY created_at DESC LIMIT 1`, taskID).Scan(&run, &role, &project)
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

type baseSyncRequest struct{ pr, base, sha, repo string }

// lockBaseSyncTx locks the task's in-scope pending requests that match
// which (SQL over q, $2 the Run), in order.
func lockBaseSyncTx(ctx context.Context, tx pgx.Tx, taskID, runID, which string) ([]baseSyncRequest, error) {
	rows, err := tx.Query(ctx, `SELECT q.pull_request_id,q.base,q.base_sha,repo.name FROM base_sync_requests q
 JOIN pull_requests p ON p.id=q.pull_request_id JOIN repositories repo ON repo.id=p.repository_id
 WHERE `+baseSyncInScope+` AND `+which+` ORDER BY q.created_at, q.base_sha FOR UPDATE OF q`, taskID, runID)
	if err != nil {
		return nil, err
	}
	var out []baseSyncRequest
	for rows.Next() {
		var q baseSyncRequest
		if err := rows.Scan(&q.pr, &q.base, &q.sha, &q.repo); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, q)
	}
	rows.Close()
	return out, rows.Err()
}

// RouteBaseSyncTx queues once per writer; requests remain pending until trusted
// forge ancestry confirms publication. A replacement writer gets them again.
// Each request routed is marked as this writer's in the same transaction
// as its directive.
func RouteBaseSyncTx(ctx context.Context, tx pgx.Tx, r RunRef) error {
	requests, err := lockBaseSyncTx(ctx, tx, r.TaskID, r.RunID, `q.writer_run_id IS DISTINCT FROM $2`)
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
		if _, err = tx.Exec(ctx, `UPDATE base_sync_requests SET writer_run_id=$3, via=$4 WHERE pull_request_id=$1 AND base_sha=$2`,
			q.pr, q.sha, r.RunID, baseSyncViaDirective); err != nil {
			return err
		}
	}
	return nil
}

// ClaimBaseSyncPromptTx is a fresh capable writer's initial briefing: the
// requests its prompt carries, each marked as told to it by prompt — exactly
// those, locked as read. A request routed to it as a directive is not
// repeated; one committed after this read is left to the directive path
// (QueueBaseSyncTx finds this writer). A rebuilt prompt carries the same.
func ClaimBaseSyncPromptTx(ctx context.Context, tx pgx.Tx, r RunRef) (string, error) {
	requests, err := lockBaseSyncTx(ctx, tx, r.TaskID, r.RunID,
		`(q.writer_run_id IS DISTINCT FROM $2 OR q.via IS NOT DISTINCT FROM '`+baseSyncViaPrompt+`')`)
	if err != nil {
		return "", err
	}
	lines := make([]string, 0, len(requests))
	for _, q := range requests {
		if _, err := tx.Exec(ctx, `UPDATE base_sync_requests SET writer_run_id=$3, via=$4 WHERE pull_request_id=$1 AND base_sha=$2`,
			q.pr, q.sha, r.RunID, baseSyncViaPrompt); err != nil {
			return "", err
		}
		lines = append(lines, baseSyncInstruction(q.repo, q.base, q.sha))
	}
	return strings.Join(lines, "\n\n"), nil
}

// PendingBaseSyncPrompt is what a capable writer's briefing would carry of
// the task's in-scope pending requests, read only.
func PendingBaseSyncPrompt(ctx context.Context, tx pgx.Tx, taskID string) (string, error) {
	rows, err := tx.Query(ctx, `SELECT repo.name,q.base,q.base_sha FROM base_sync_requests q
 JOIN pull_requests p ON p.id=q.pull_request_id JOIN repositories repo ON repo.id=p.repository_id
 WHERE `+baseSyncInScope+` ORDER BY q.created_at, q.base_sha`, taskID)
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
