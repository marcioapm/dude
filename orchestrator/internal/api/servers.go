package api

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/marciomartins/dude/orchestrator/internal/servers"
)

// serverRoutes are a task's servers and its branch preview. The backend
// has checked the caller may read (or act on) the task; the orchestrator
// holds lux's key, so it makes the calls.
func (s *Server) serverRoutes(mux *http.ServeMux) {
	h := func(fn func(w http.ResponseWriter, r *http.Request, org string) error) http.Handler {
		return s.auth(func(w http.ResponseWriter, r *http.Request, org string) error {
			if s.Servers == nil {
				return fail(http.StatusNotFound, "not_found", "servers are not set up")
			}
			var se *servers.Error
			if err := fn(w, r, org); errors.As(err, &se) {
				return fail(se.Status, se.Code, "%s", se.Message)
			} else {
				return err
			}
		})
	}
	mux.Handle("GET /internal/tasks/{id}/servers", h(func(w http.ResponseWriter, r *http.Request, org string) error {
		out, err := s.Servers.ForTask(r.Context(), org, r.PathValue("id"))
		return answer(w, http.StatusOK, out, err)
	}))
	mux.Handle("GET /internal/runs/{id}/servers", h(func(w http.ResponseWriter, r *http.Request, org string) error {
		out, err := s.Servers.ForRun(r.Context(), org, r.PathValue("id"))
		return answer(w, http.StatusOK, out, err)
	}))
	mux.Handle("POST /internal/runs/{id}/servers", h(func(w http.ResponseWriter, r *http.Request, org string) error {
		var in servers.AddInput
		if err := read(r, &in); err != nil {
			return err
		}
		out, err := s.Servers.Add(r.Context(), org, r.PathValue("id"), in)
		return answer(w, http.StatusCreated, out, err)
	}))
	for _, action := range []string{"start", "stop"} {
		mux.Handle("POST /internal/runs/{id}/servers/"+action+"-all", h(func(w http.ResponseWriter, r *http.Request, org string) error {
			out, err := s.Servers.All(r.Context(), org, r.PathValue("id"), action)
			return answer(w, http.StatusOK, out, err)
		}))
	}
	mux.Handle("POST /internal/runs/{id}/servers/{name}/{action}", h(func(w http.ResponseWriter, r *http.Request, org string) error {
		action := r.PathValue("action")
		if action != "start" && action != "stop" && action != "restart" {
			return fail(http.StatusNotFound, "not_found", "no server action %q", action)
		}
		out, err := s.Servers.Action(r.Context(), org, r.PathValue("id"), r.PathValue("name"), action)
		return answer(w, http.StatusOK, out, err)
	}))
	mux.Handle("DELETE /internal/runs/{id}/servers/{name}", h(func(w http.ResponseWriter, r *http.Request, org string) error {
		if err := s.Servers.Remove(r.Context(), org, r.PathValue("id"), r.PathValue("name")); err != nil {
			return err
		}
		w.WriteHeader(http.StatusNoContent)
		return nil
	}))
	mux.Handle("GET /internal/runs/{id}/servers/{name}/log", h(func(w http.ResponseWriter, r *http.Request, org string) error {
		tail := 200
		if v := r.URL.Query().Get("tail"); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil || n < 1 || n > 10000 {
				return fail(http.StatusBadRequest, "bad_request", "tail must be 1-10000")
			}
			tail = n
		}
		out, err := s.Servers.ServerLog(r.Context(), org, r.PathValue("id"), r.PathValue("name"), tail)
		return answer(w, http.StatusOK, out, err)
	}))
	mux.Handle("POST /internal/tasks/{id}/preview", h(func(w http.ResponseWriter, r *http.Request, org string) error {
		out, err := s.Servers.StartPreview(r.Context(), org, r.PathValue("id"), actor(r), principalOf(r).ActorType, principalOf(r).Person)
		return answer(w, http.StatusCreated, out, err)
	}))
	mux.Handle("DELETE /internal/tasks/{id}/preview", h(func(w http.ResponseWriter, r *http.Request, org string) error {
		out, err := s.Servers.StopPreview(r.Context(), org, r.PathValue("id"), actor(r), principalOf(r).ActorType, principalOf(r).Person)
		return answer(w, http.StatusOK, out, err)
	}))
}

// answer writes v, or returns err for the caller's error handling.
func answer(w http.ResponseWriter, status int, v any, err error) error {
	if err != nil {
		return err
	}
	write(w, status, v)
	return nil
}
