package servers

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/db"
	"github.com/marciomartins/dude/orchestrator/internal/ledger"
)

// AutoPreviewActor is who prepares a task's preview when the task's pull
// request opens or reopens (run.created's actor id).
const AutoPreviewActor = "auto-preview"

// AutoPreview prepares task's preview when its pull request opens (dude
// opens it, or takes over a person's) or reopens, if automatic task
// previews are on for it (its own choice, else its project's). As the
// system, the preview sleeps until someone opens its URL (agentWakes) and
// is never announced (announceReady); a live one is kept.
//
// A project with nothing to start in previews (or no preview domain) gets
// none: startPreview would make an eager one, whose Run starts at once.
// A task that is over or gone is a quiet no-op. Callers run it after their
// record commits and drop its error, which is logged here.
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
		// Not StartPreview: its view asks lux, and nobody reads it.
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
