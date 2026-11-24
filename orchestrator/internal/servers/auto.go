package servers

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/db"
	"github.com/marciomartins/dude/orchestrator/internal/ledger"
)

// AutoPreviewActor is who prepares a task's preview when dude opens or
// reopens the task's pull request (run.created's actor id).
const AutoPreviewActor = "auto-preview"

// AutoPreview prepares task's preview, if automatic task previews are on
// for it (its own choice, else its project's: migration 272), when dude
// opens or reopens a pull request it opened for the task. The preview is
// startPreview's (StartPreview without its view: lux is not asked), as
// the system: asleep until someone opens its URL (agentWakes), never
// announced (announceReady), and the task's live one if it has one
// already — a repeat makes nothing.
//
// A project with nothing to start in previews (or no preview domain) gets
// none: StartPreview would make an eager one, whose Run starts at once.
// A task that is over or gone is a quiet no-op too. Callers run it after
// what they recorded committed; its error is logged here, for them to
// drop: the pull request's record is what matters.
func (s *Service) AutoPreview(ctx context.Context, org, taskID string) error {
	var on, wakeable bool
	err := s.DB.InOrg(ctx, org, func(tx pgx.Tx) error {
		var projectID string
		if err := tx.QueryRow(ctx, `SELECT t.project_id, COALESCE(t.auto_task_preview, p.auto_task_preview)
			FROM tasks t JOIN projects p ON p.id = t.project_id WHERE t.id = $1`, taskID).Scan(&projectID, &on); err != nil {
			return err
		}
		if !on {
			return nil
		}
		var err error
		wakeable, err = s.wakeable(ctx, tx, projectID)
		return err
	})
	if db.IsNotFound(err) || err == nil && !on {
		return nil
	}
	if err == nil && !wakeable {
		s.Log.Debug("no automatic task preview: the project has no server to start in previews", "task", taskID)
		return nil
	}
	if err == nil {
		// startPreview, not StartPreview: nobody reads the view, and building
		// it asks lux (ListServers) on the pull request's open/sync path.
		err = s.startPreview(ctx, org, taskID, AutoPreviewActor, ledger.ActorSystem, "")
	}
	var refusal *Error
	if errors.As(err, &refusal) {
		s.Log.Debug("no automatic task preview: "+refusal.Message, "task", taskID, "code", refusal.Code)
		return nil
	}
	if err != nil {
		s.Log.Warn("automatic task preview failed", "task", taskID, "error", err)
	}
	return err
}
