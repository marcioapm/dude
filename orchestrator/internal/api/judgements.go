package api

import (
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/db"
	"github.com/marciomartins/dude/orchestrator/internal/judge"
)

// judgementRoutes: the Judgements page's test question. The backend has
// decided who may ask one (POST /v1/judgements/test: admins), and names
// the tier as the page has it picked, saved or not.
func (s *Server) judgementRoutes(mux *http.ServeMux) {
	mux.Handle("POST /internal/judgements/test", s.auth(func(w http.ResponseWriter, r *http.Request, org string) error {
		var in struct {
			TierID string `json:"tierId"`
		}
		if err := read(r, &in); err != nil {
			return err
		}
		if in.TierID == "" {
			return fail(http.StatusBadRequest, "bad_request", "a test question names a tier")
		}
		var tier judge.Tier
		err := s.DB.InOrg(r.Context(), org, func(tx pgx.Tx) (err error) {
			tier, err = judge.LoadTier(r.Context(), tx, in.TierID)
			return err
		})
		switch {
		case db.IsNotFound(err):
			return fail(http.StatusNotFound, "not_found", "no model tier %s", in.TierID)
		case errors.Is(err, judge.ErrNoModel):
			return fail(http.StatusBadRequest, "bad_request", "%s names no model", tier.Name)
		case err != nil:
			return err
		}
		if s.LLM.URL == "" {
			return fail(http.StatusServiceUnavailable, "unavailable", "the LLM proxy is not configured (DUDE_LLM_URL)")
		}
		// Asked as Judge asks, and recorded nowhere.
		j, done, err := judge.Test(r.Context(), s.LLM, tier)
		out := map[string]any{
			"ok": err == nil, "decision": nil, "confidence": nil, "reasoning": nil, "error": nil,
			"model": tier.Model, "latencyMs": done.LatencyMs, "inputTokens": done.InputTokens, "outputTokens": done.OutputTokens,
		}
		if err != nil {
			out["error"] = err.Error()
		} else {
			out["decision"], out["confidence"], out["reasoning"] = j.Decision, j.Confidence, j.Reasoning
		}
		write(w, http.StatusOK, out)
		return nil
	}))
}
