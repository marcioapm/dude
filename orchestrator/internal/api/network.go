package api

import (
	"net/http"
	"net/url"
)

// networkRoutes are what the Network settings page shows under every
// organisation's and project's list, which only the orchestrator knows:
// the operator's own list (agent.egress), and what every Run reaches with
// no list naming it — the model's host and dude's tools, in words. model is
// that host alone (null with none): a Run with nothing listed anywhere and
// no model is unrestricted.
func (s *Server) networkRoutes(mux *http.ServeMux) {
	mux.Handle("GET /internal/network/defaults", s.auth(func(w http.ResponseWriter, r *http.Request, _ string) error {
		always := []string{}
		var model *string
		if u, err := url.Parse(s.LLM.URL); err == nil && u.Hostname() != "" {
			host := u.Hostname()
			always, model = append(always, host), &host
		}
		operator := s.AgentEgress
		if operator == nil {
			operator = []string{}
		}
		write(w, http.StatusOK, map[string]any{"operator": operator, "always": append(always, "dude’s tools"), "model": model})
		return nil
	}))
}
