package api

import (
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/db"
	"github.com/marciomartins/dude/orchestrator/internal/delivery"
)

// The owner's answers to a stalled Run's banner on a plain delivery:
// Restart (the conductor's restart_run, with the person as the actor) and
// Leave it. Stop is the abort.
func (s *Server) stallRoutes(mux *http.ServeMux) {
	mux.Handle("POST /internal/runs/{id}/restart", s.auth(s.restartRun))
	mux.Handle("POST /internal/runs/{id}/leave", s.auth(s.leaveStalled))
}

// runTask is the task a Run belongs to, which never changes, read unlocked
// so the task's locks can be taken in their order before the Run's.
func runTask(r *http.Request, tx pgx.Tx, runID string) (string, error) {
	var taskID string
	err := tx.QueryRow(r.Context(), `SELECT COALESCE(task_id, '') FROM runs WHERE id = $1`, runID).Scan(&taskID)
	if db.IsNotFound(err) {
		return "", fail(http.StatusNotFound, "not_found", "run %s not found", runID)
	}
	if err == nil && taskID == "" {
		return "", fail(http.StatusConflict, "conflict", "run %s is not a task's phase Run", runID)
	}
	return taskID, err
}

// restartRun: the owner restarts a live phase Run their delivery waits on,
// in its slot, with an optional note its new agent is told.
func (s *Server) restartRun(w http.ResponseWriter, r *http.Request, org string) error {
	runID := r.PathValue("id")
	var body struct{ Note, Tier string }
	if err := read(r, &body); err != nil {
		return err
	}
	p := principalOf(r)
	var newID string
	err := s.DB.InOrg(r.Context(), org, func(tx pgx.Tx) error {
		taskID, err := runTask(r, tx, runID)
		if err != nil {
			return err
		}
		if err := delivery.LockChat(r.Context(), tx, taskID); err != nil {
			return err
		}
		// Who may: checked under the task's locks, which restart takes first.
		if _, _, err := delivery.LockEscalationTx(r.Context(), tx, taskID); err != nil {
			return escalationFailure(err)
		}
		if err := ownerOnly(r.Context(), tx, taskID, p.Person, "restart"); err != nil {
			return err
		}
		newID, err = delivery.RestartRunTx(r.Context(), tx, org, taskID, delivery.Restart{RunID: runID, Note: body.Note,
			Tier: body.Tier, Actor: delivery.Writer{ActorType: p.ActorType, ActorID: p.Actor}})
		return refusal(err)
	})
	if err != nil {
		return err
	}
	s.kick()
	write(w, http.StatusOK, map[string]any{"runId": newID, "replaced": runID})
	return nil
}

// leaveStalled: the owner leaves a stalled Run as it is; nothing is asked
// again for it.
func (s *Server) leaveStalled(w http.ResponseWriter, r *http.Request, org string) error {
	runID := r.PathValue("id")
	p := principalOf(r)
	err := s.DB.InOrg(r.Context(), org, func(tx pgx.Tx) error {
		ri, err := loadRun(r.Context(), tx, runID)
		if err != nil {
			return err
		}
		if err := ownerOnly(r.Context(), tx, ri.TaskID, p.Person, "leave"); err != nil {
			return err
		}
		return delivery.LeaveStalledTx(r.Context(), tx, delivery.RunRef{Org: org, ProjectID: ri.ProjectID, TaskID: ri.TaskID,
			RunID: runID}, delivery.Writer{ActorType: p.ActorType, ActorID: p.Actor})
	})
	if err != nil {
		return err
	}
	write(w, http.StatusOK, map[string]any{"runId": runID, "left": true})
	return nil
}

// refusal answers a delivery refusal as a conflict saying why.
func refusal(err error) error {
	var r delivery.Refusal
	if errors.As(err, &r) {
		return fail(http.StatusConflict, "conflict", "%s", r.Msg)
	}
	return err
}
