package api

import (
	"net/http"
	"net/url"
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
	// A check, never a gate: an admin's, as everything that spends the
	// organization's tokens on its own.
	mux.Handle("POST /internal/llm/test", s.auth(func(w http.ResponseWriter, r *http.Request, _ string) error {
		if p := principalOf(r); p.Person != "" && !p.Admin {
			return fail(http.StatusForbidden, "not_admin", "only an organization admin can send a test message")
		}
		var in struct {
			Model   string    `json:"model"`
			Efforts []*string `json:"efforts"`
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
		if len(in.Efforts) > 4 {
			return fail(http.StatusBadRequest, "bad_request", "at most four efforts")
		}
		efforts := make([]string, 0, len(in.Efforts))
		for _, e := range in.Efforts {
			switch {
			case e == nil:
				efforts = append(efforts, "")
			case *e == "low" || *e == "medium" || *e == "high" || *e == "max":
				efforts = append(efforts, *e)
			default:
				return fail(http.StatusBadRequest, "bad_request", "no effort %q", *e)
			}
		}
		if len(efforts) == 0 {
			efforts = []string{""}
		}
		write(w, http.StatusOK, map[string]any{"model": in.Model, "results": s.LLM.Test(r.Context(), in.Model, efforts)})
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
