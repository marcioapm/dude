package servers

import (
	"cmp"
	"context"
	"log/slog"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/db"
	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/ledger"
)

// announceReady posts "Ready for your look" for live preview runID, if an
// agent started it: a task preview's by the task's latest conductor, a
// branch preview's by the session agent that started it. Feed.Apply calls
// it in the transaction that first sets preview_ready_at, so it runs once
// per preview.
func announceReady(ctx context.Context, tx pgx.Tx, log *slog.Logger, org, runID string) error {
	var starter, projectID, taskID, branch string
	// run.created is a preview Run's first event (inserted with its runs
	// row): one events_run_cursor_idx probe, whatever its history.
	err := tx.QueryRow(ctx, `SELECT e.actor_id, r.project_id, COALESCE(r.task_id, ''), COALESCE(r.preview_branch, '')
		FROM runs r JOIN events e ON e.cursor = (SELECT min(cursor) FROM events WHERE run_id = $1)
			AND e.event_type = 'run.created' AND e.actor_type = $2
		WHERE r.id = $1 AND `+livePreview, runID, ledger.ActorAgent).Scan(&starter, &projectID, &taskID, &branch)
	if db.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	speaker, speakerTask, sessionID := "", taskID, ""
	if taskID != "" {
		err = tx.QueryRow(ctx, `SELECT id FROM runs
			WHERE task_id = $1 AND role = 'conductor' AND phase IS NULL AND session_id IS NULL
			ORDER BY created_at DESC, id DESC LIMIT 1`, taskID).Scan(&speaker)
	} else {
		err = tx.QueryRow(ctx, `SELECT id, session_id, COALESCE(task_id, '') FROM runs
			WHERE id = $1 AND session_id IS NOT NULL`, starter).Scan(&speaker, &sessionID, &speakerTask)
	}
	if db.IsNotFound(err) {
		if taskID != "" {
			log.Debug("a preview's ready is not announced: its task has no conductor", "run", runID, "task", taskID)
		}
		return nil
	}
	if err != nil {
		return err
	}
	rows, err := tx.Query(ctx, `SELECT name, COALESCE(url, '') FROM preview_servers
		WHERE run_id = $1 AND deleted_at IS NULL ORDER BY name`, runID)
	if err != nil {
		return err
	}
	list, err := pgx.CollectRows(rows, pgx.RowToStructByPos[delivery.PreviewReadyServer])
	if err != nil {
		return err
	}
	_, err = ledger.Append(ctx, tx, ledger.Event{Type: "agent.message", OrganizationID: org, ProjectID: projectID,
		TaskID: speakerTask, RunID: speaker, SessionID: sessionID, ActorType: ledger.ActorAgent, ActorID: speaker,
		Source: ledger.SourceOrchestrator, CorrelationID: cmp.Or(speakerTask, sessionID),
		Payload: map[string]any{"text": delivery.PreviewReady(list, taskID != "", branch), "previewReady": runID}})
	return err
}
