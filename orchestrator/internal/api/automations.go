package api

// Automations' endpoints that need GitHub or the orchestrator's matcher;
// the settings themselves are the backend's (routes/automations.ts).

import (
	"net/http"
	"time"

	"github.com/marciomartins/dude/orchestrator/internal/automations"
)

// eventSubscriptions is the events this organisation's GitHub sends dude,
// for the event picker, and where that was read.
func (s *Server) eventSubscriptions(w http.ResponseWriter, r *http.Request, org string) error {
	sub, err := automations.ReadSubscriptions(r.Context(), s.DB, s.Forges, org, time.Now())
	if err != nil {
		return err
	}
	write(w, http.StatusOK, sub)
	return nil
}
