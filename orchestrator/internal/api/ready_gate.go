package api

import (
	"net/http"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/db"
	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/forge"
)

// readyGate is a pull request's ready gate (delivery.ReadyGate), read now:
// whether merging it is the only act left, and each item in order with
// what it found. Read only. 404 for a pull request the organization does
// not have; 502 when GitHub did not answer, which is no judgement either
// way. With no GitHub connected, its GitHub items read unreadable.
func (s *Server) readyGate(w http.ResponseWriter, r *http.Request, org string) error {
	id := r.PathValue("id")
	var taskID string
	var check delivery.ReadyGateCheck
	err := s.DB.InOrg(r.Context(), org, func(tx pgx.Tx) error {
		if err := tx.QueryRow(r.Context(), `SELECT task_id FROM pull_requests WHERE id = $1`, id).Scan(&taskID); err != nil {
			return err
		}
		var err error
		check, err = delivery.CheckReadyGateTx(r.Context(), tx, taskID, []string{id})
		return err
	})
	if db.IsNotFound(err) {
		return fail(http.StatusNotFound, "not_found", "pull request %s not found", id)
	}
	if err != nil {
		return err
	}
	var gh *forge.GitHub
	if s.Forges != nil {
		if gh, err = s.Forges.For(r.Context(), org); err != nil {
			return err
		}
	}
	gates := delivery.DoReadyGate(r.Context(), gh, check)
	if len(gates) != 1 {
		return fail(http.StatusNotFound, "not_found", "pull request %s not found", id)
	}
	g := gates[0]
	if g.Err != nil {
		return fail(http.StatusBadGateway, "github_unavailable", "GitHub did not answer: %s", g.Err.Error())
	}
	write(w, http.StatusOK, map[string]any{"pullRequestId": g.ID, "taskId": taskID, "repo": g.Repo, "number": g.Number,
		"head": g.Head, "ready": g.Ready, "items": g.Items})
	return nil
}
