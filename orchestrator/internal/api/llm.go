package api

import (
	"net/http"
	"net/url"
	"slices"

	"github.com/marciomartins/dude/orchestrator/internal/llm"
)

// llmRoutes are the LLM proxy as the Models page uses it: the models it
// lists, as suggestions, and a test message to one. The proxy's key stays
// here; the backend never has it.
func (s *Server) llmRoutes(mux *http.ServeMux) {
	mux.Handle("GET /internal/llm/models", s.auth(func(w http.ResponseWriter, r *http.Request, _ string) error {
		out := map[string]any{"models": []string{}, "source": nil, "problem": nil}
		if s.LLM.URL != "" {
			out["source"] = proxySource(s.LLM.URL)
		}
		models, err := s.LLM.Models(r.Context())
		if err != nil {
			out["problem"] = err.Error()
		} else {
			out["models"] = models
		}
		write(w, http.StatusOK, out)
		return nil
	}))
	// A check, never a gate. The backend has decided who may send one
	// (POST /v1/models/test: admins), as for memory reindex, and sends the
	// tier's settings as the dialog has them (saved or not).
	mux.Handle("POST /internal/llm/test", s.auth(func(w http.ResponseWriter, r *http.Request, _ string) error {
		var in struct {
			Model   string            `json:"model"`
			Effort  *string           `json:"effort"`
			Options map[string]any    `json:"options"`
			Headers map[string]string `json:"headers"`
		}
		if err := read(r, &in); err != nil {
			return err
		}
		if in.Model == "" {
			return fail(http.StatusBadRequest, "bad_request", "a test message names a model")
		}
		if s.LLM.URL == "" {
			return fail(http.StatusServiceUnavailable, "unavailable", "the LLM proxy is not configured (DUDE_LLM_URL)")
		}
		t := llm.TestTier{Model: in.Model, Options: in.Options, Headers: in.Headers}
		if in.Effort != nil {
			if !slices.Contains(llm.Efforts, *in.Effort) {
				return fail(http.StatusBadRequest, "bad_request", "no effort %q", *in.Effort)
			}
			t.Effort = *in.Effort
		}
		write(w, http.StatusOK, map[string]any{"model": in.Model, "result": s.LLM.Test(r.Context(), t)})
		return nil
	}))
}

// proxySource is the proxy's address as a person reads it: its host and
// path, never a query that could carry a secret.
func proxySource(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return ""
	}
	return u.Host + u.Path
}
