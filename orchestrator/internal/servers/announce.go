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

// announceReady posts "Ready for your look" in the conversation of the
// agent that started preview runID: a task preview's in its task's Chat
// (on the task's latest conductor), a branch preview's in the session
// whose agent started it. Called in the transaction that recorded the
// preview's first ready (Feed.Apply: runs.preview_ready_at set from NULL,
// the Run row held), so a preview Run is announced once, whoever sees its
// ready and however often. A preview a person started, or one no longer
// live, is not announced. No network work: everything is read in tx.
func announceReady(ctx context.Context, tx pgx.Tx, log *slog.Logger, org, runID string) error {
	var starter, projectID, taskID, branch string
	// A Run has one run.created. Found by run_id (events_run_cursor_idx),
	// not in cursor order: an ordered scan of a Run with a long history may
	// be planned as a walk of events_pkey, here with the Run row held.
	err := tx.QueryRow(ctx, `SELECT e.actor_id, r.project_id, COALESCE(r.task_id, ''), COALESCE(r.preview_branch, '')
		FROM runs r JOIN events e ON e.run_id = $1 AND e.event_type = 'run.created' AND e.actor_type = $2
		WHERE r.id = $1 AND `+livePreview, runID, ledger.ActorAgent).Scan(&starter, &projectID, &taskID, &branch)
	if db.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	// Who speaks: a task preview's conductor (the task's latest, the one
	// its Chat writes to now), else the starting agent's own Run.
	var speaker, sessionID, speakerTask string
	err = tx.QueryRow(ctx, `SELECT a.id, COALESCE(a.session_id, ''), COALESCE(a.task_id, '')
		FROM runs a WHERE ($2 <> '' AND a.task_id = $2 AND a.role = 'conductor' AND a.phase IS NULL AND a.session_id IS NULL)
			OR ($2 = '' AND a.id = $1)
		ORDER BY a.created_at DESC, a.id DESC LIMIT 1`, starter, taskID).Scan(&speaker, &sessionID, &speakerTask)
	if db.IsNotFound(err) {
		if taskID != "" {
			log.Debug("a preview's ready is not announced: its task has no conductor", "run", runID, "task", taskID)
		}
		return nil
	}
	if err != nil {
		return err
	}
	if taskID == "" && sessionID == "" {
		// A branch preview is a session's: its agent alone may start one.
		return nil
	}
	rows, err := tx.Query(ctx, `SELECT name, COALESCE(url, '') FROM preview_servers
		WHERE run_id = $1 AND deleted_at IS NULL ORDER BY name`, runID)
	if err != nil {
		return err
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (delivery.PreviewReadyServer, error) {
		var s delivery.PreviewReadyServer
		return s, row.Scan(&s.Name, &s.URL)
	})
	if err != nil {
		return err
	}
	text := delivery.PreviewReady(list, taskID != "", branch)
	_, err = ledger.Append(ctx, tx, ledger.Event{Type: "agent.message", OrganizationID: org, ProjectID: projectID,
		TaskID: speakerTask, RunID: speaker, SessionID: sessionID, ActorType: ledger.ActorAgent, ActorID: speaker,
		Source: ledger.SourceOrchestrator, CorrelationID: cmp.Or(speakerTask, sessionID),
		Payload: map[string]any{"text": text, "previewReady": runID}})
	return err
}
