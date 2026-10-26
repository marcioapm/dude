package api

import (
	"net/http"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
)

// Before the pull request: the task owner's own choice of what Deliver does
// before the task's first pull request (delivery.SetPullRequestChoiceTx).

func (s *Server) pullRequestGateRoutes(mux *http.ServeMux) {
	mux.Handle("PUT /internal/tasks/{id}/pull-request-choice", s.auth(s.setPullRequestChoice))
}

func (s *Server) setPullRequestChoice(w http.ResponseWriter, r *http.Request, org string) error {
	taskID := r.PathValue("id")
	var body struct {
		// open, draft, ask; null for the project's.
		Choice *string `json:"choice"`
	}
	if err := read(r, &body); err != nil {
		return err
	}
	choice := ""
	if body.Choice != nil {
		choice = *body.Choice
		if choice == "" {
			return fail(http.StatusBadRequest, "bad_request", "choice is open, draft, ask or null")
		}
	}
	p := principalOf(r)
	var changed bool
	var wake string
	err := s.DB.InOrg(r.Context(), org, func(tx pgx.Tx) error {
		var err error
		changed, err = delivery.SetPullRequestChoiceTx(r.Context(), tx, org, taskID, choice,
			delivery.Actor{ActorType: p.ActorType, ActorID: p.Actor, Person: p.Person})
		if err != nil || !changed {
			return choiceFailure(err)
		}
		// Held by its Ask me first: the hold reads the choice again.
		d, err := delivery.ReadDelivery(r.Context(), tx, taskID)
		if err == nil && d.HeldByChoice() {
			wake = d.WorkflowID
		}
		return err
	})
	if err != nil {
		return err
	}
	if wake != "" {
		if err := s.Workflow.Signal(r.Context(), org, wake, delivery.SignalConductorDecision, map[string]any{"action": "choice"}, ""); err != nil {
			return err
		}
	}
	s.kick()
	var out any
	if choice != "" {
		out = choice
	}
	write(w, http.StatusOK, map[string]any{"taskId": taskID, "pullRequestChoice": out, "changed": changed})
	return nil
}

// choiceFailure answers a refused change with its status and code.
func choiceFailure(err error) error {
	refused, ok := delivery.IsChoiceRefusal(err)
	if !ok {
		return err
	}
	status := http.StatusConflict
	switch refused.Code {
	case "bad_request":
		status = http.StatusBadRequest
	case "not_found":
		status = http.StatusNotFound
	case "not_owner":
		status = http.StatusForbidden
	}
	return &httpError{status: status, code: refused.Code, message: refused.Msg}
}
