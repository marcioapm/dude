package api

import (
	"errors"
	"net/http"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
)

func (s *Server) sourceRoutes(mux *http.ServeMux) {
	mux.Handle("POST /internal/projects/{id}/sources/check", s.auth(s.checkSource))
}

// checkSource checks a task's source against the project and GitHub, and
// answers it resolved — the pull request's title, head and base — or why
// dude will not adopt it (422, the rule's code), or that GitHub could not be
// asked (502). It writes nothing: the backend keeps the source with the
// task.
func (s *Server) checkSource(w http.ResponseWriter, r *http.Request, org string) error {
	var in delivery.SourceRequest
	if err := read(r, &in); err != nil {
		return err
	}
	if s.Forges == nil {
		return fail(http.StatusBadGateway, "github_unavailable", "GitHub is not set up here")
	}
	gh, err := s.Forges.For(r.Context(), org)
	if err != nil {
		return err
	}
	out, err := delivery.CheckSource(r.Context(), s.DB, gh, org, r.PathValue("id"), in)
	var refusal delivery.SourceRefusal
	var unreachable delivery.SourceUnreachable
	switch {
	case errors.As(err, &refusal):
		return fail(http.StatusUnprocessableEntity, refusal.Code, "%s", refusal.Msg)
	case errors.As(err, &unreachable):
		return fail(http.StatusBadGateway, "github_unavailable", "%s", unreachable.Error())
	case err != nil:
		return err
	}
	write(w, http.StatusOK, out)
	return nil
}
