package api

// Memory, for the settings pages: search with why each result ranked where
// it did, the memories themselves, and the index behind both. The backend
// has decided who may reach each route (reindex: admins); what only the
// memory knows — whose it is — is checked here, from the principal the
// backend names (principalOf).

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/memory"
)

func (s *Server) memoryRoutes(mux *http.ServeMux) {
	mux.Handle("GET /internal/memory/search", s.auth(s.memorySearch))
	mux.Handle("GET /internal/memory/memories", s.auth(s.memoryList))
	mux.Handle("POST /internal/memory/memories", s.auth(s.memoryCreate))
	mux.Handle("GET /internal/memory/memories/{id}", s.auth(s.memoryGet))
	mux.Handle("PATCH /internal/memory/memories/{id}", s.auth(s.memoryUpdate))
	mux.Handle("POST /internal/memory/memories/{id}/{action}", s.auth(s.memoryArchive))
	mux.Handle("GET /internal/memory/index", s.auth(s.memoryIndex))
	mux.Handle("POST /internal/memory/index/retry", s.auth(s.memoryRetry))
	mux.Handle("POST /internal/memory/index/reindex", s.auth(s.memoryReindex))
}

func person(r *http.Request) memory.Actor {
	return memory.Actor{Type: principalOf(r).ActorType, ID: principalOf(r).Actor}
}

// viewer is the caller as the memory reads them: a session's memories are
// its accepted members' alone, an admin's included.
func viewer(r *http.Request) memory.Viewer {
	return memory.Viewer{Person: principalOf(r).Person}
}

// memoryError turns the package's refusals into the caller's.
func memoryError(err error) error {
	var bad *memory.Invalid
	switch {
	case errors.As(err, &bad):
		return fail(http.StatusUnprocessableEntity, "invalid", "%s", bad.Reason)
	case errors.Is(err, memory.ErrNotFound):
		return fail(http.StatusNotFound, "not_found", "no such memory")
	}
	return err
}

func (s *Server) memorySearch(w http.ResponseWriter, r *http.Request, org string) error {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	var out memory.Outcome
	// Embedded before the transaction: a slow embedder must not hold a
	// connection. mode=words is a lookup by name (the About picker): no
	// meaning, no embedding paid for.
	var emb memory.Embedded
	if q.Get("mode") != "words" {
		emb = memory.EmbedQuery(r.Context(), s.Embedder, q.Get("q"))
	}
	err := s.DB.InOrg(r.Context(), org, func(tx pgx.Tx) error {
		var err error
		out, err = memory.Ranked(r.Context(), tx, emb, memory.Query{
			Text: q.Get("q"), Project: q.Get("project"), Types: split(q.Get("types")), Limit: limit, Viewer: viewer(r),
		})
		return err
	})
	if err != nil {
		return err
	}
	write(w, http.StatusOK, out)
	return nil
}

func (s *Server) memoryList(w http.ResponseWriter, r *http.Request, org string) error {
	q := r.URL.Query()
	var out []memory.Memory
	err := s.DB.InOrg(r.Context(), org, func(tx pgx.Tx) error {
		var err error
		out, err = memory.List(r.Context(), tx, memory.ListQuery{
			Project: q.Get("project"), Scope: q.Get("scope"), Author: q.Get("author"),
			Text: q.Get("q"), Archived: q.Get("archived") == "true", Viewer: viewer(r),
		})
		return err
	})
	if err != nil {
		return err
	}
	write(w, http.StatusOK, map[string]any{"memories": out})
	return nil
}

func (s *Server) memoryGet(w http.ResponseWriter, r *http.Request, org string) error {
	var m memory.Memory
	err := s.DB.InOrg(r.Context(), org, func(tx pgx.Tx) error {
		var err error
		m, err = memory.Get(r.Context(), tx, r.PathValue("id"), viewer(r))
		return err
	})
	if err != nil {
		return memoryError(err)
	}
	write(w, http.StatusOK, m)
	return nil
}

// memoryPatch is an edit: what is absent stays as it is.
type memoryPatch struct {
	ProjectID *string       `json:"projectId"`
	Title     *string       `json:"title"`
	Content   *string       `json:"content"`
	Kind      *string       `json:"kind"`
	About     *[]memory.Ref `json:"about"`
}

func (s *Server) memoryCreate(w http.ResponseWriter, r *http.Request, org string) error {
	var b struct {
		ProjectID string       `json:"projectId"`
		Title     string       `json:"title"`
		Content   string       `json:"content"`
		Kind      string       `json:"kind"`
		About     []memory.Ref `json:"about"`
	}
	if err := read(r, &b); err != nil {
		return err
	}
	n := memory.New{ProjectID: b.ProjectID, Title: b.Title, Content: b.Content, Kind: b.Kind, About: b.About,
		Author: memory.Author{Kind: "person", PersonID: principalOf(r).Person}}
	var m memory.Memory
	err := s.DB.InOrg(r.Context(), org, func(tx pgx.Tx) error {
		var err error
		m, err = memory.Create(r.Context(), tx, org, n, person(r))
		return err
	})
	if err != nil {
		return memoryError(err)
	}
	s.kick()
	write(w, http.StatusCreated, m)
	return nil
}

// mayChange: a person changes their own memories; an admin, anyone's —
// an agent's and dude's too.
func mayChange(r *http.Request, m memory.Memory) error {
	p := principalOf(r)
	if p.Admin || p.Person != "" && m.Author.Kind == "person" && m.Author.PersonID == p.Person {
		return nil
	}
	return fail(http.StatusForbidden, "not_admin", "only an organisation admin changes a memory someone else wrote")
}

func (s *Server) memoryUpdate(w http.ResponseWriter, r *http.Request, org string) error {
	var b memoryPatch
	if err := read(r, &b); err != nil {
		return err
	}
	var m memory.Memory
	err := s.DB.InOrg(r.Context(), org, func(tx pgx.Tx) error {
		cur, err := memory.Get(r.Context(), tx, r.PathValue("id"), viewer(r))
		if err != nil {
			return err
		}
		if err := mayChange(r, cur); err != nil {
			return err
		}
		m, err = memory.Update(r.Context(), tx, org, cur, memory.Patch{
			Title: b.Title, Content: b.Content, Kind: b.Kind, ProjectID: b.ProjectID, About: b.About,
		}, person(r))
		return err
	})
	if err != nil {
		return memoryError(err)
	}
	s.kick()
	write(w, http.StatusOK, m)
	return nil
}

func (s *Server) memoryArchive(w http.ResponseWriter, r *http.Request, org string) error {
	action := r.PathValue("action")
	if action != "archive" && action != "restore" {
		return fail(http.StatusNotFound, "not_found", "no action %q", action)
	}
	var m memory.Memory
	err := s.DB.InOrg(r.Context(), org, func(tx pgx.Tx) error {
		cur, err := memory.Get(r.Context(), tx, r.PathValue("id"), viewer(r))
		if err != nil {
			return err
		}
		if err := mayChange(r, cur); err != nil {
			return err
		}
		m, err = memory.Archive(r.Context(), tx, org, cur, action == "archive", person(r))
		return err
	})
	if err != nil {
		return memoryError(err)
	}
	s.kick()
	write(w, http.StatusOK, m)
	return nil
}

func (s *Server) memoryIndex(w http.ResponseWriter, r *http.Request, org string) error {
	var out memory.Status
	err := s.DB.InOrg(r.Context(), org, func(tx pgx.Tx) error {
		var err error
		var health memory.Health
		if s.Indexer != nil {
			health = s.Indexer.Health()
		}
		out, err = memory.IndexStatus(r.Context(), tx, s.Embedder, health, r.URL.Query().Get("project"), viewer(r))
		return err
	})
	if err != nil {
		return err
	}
	write(w, http.StatusOK, out)
	return nil
}

// resumeIndexer ends the indexer's wait, so a person's Retry or Reindex is now.
func (s *Server) resumeIndexer() {
	if s.Indexer != nil {
		s.Indexer.Resume()
	}
	s.kick()
}

func (s *Server) memoryRetry(w http.ResponseWriter, r *http.Request, org string) error {
	var b struct {
		Type string `json:"type"`
		ID   string `json:"id"`
	}
	if err := read(r, &b); err != nil {
		return err
	}
	var n int64
	err := s.DB.InOrg(r.Context(), org, func(tx pgx.Tx) error {
		var err error
		n, err = memory.Retry(r.Context(), tx, b.Type, b.ID)
		return err
	})
	if err != nil {
		return err
	}
	s.resumeIndexer()
	write(w, http.StatusOK, map[string]any{"due": n})
	return nil
}

func (s *Server) memoryReindex(w http.ResponseWriter, r *http.Request, org string) error {
	var n int64
	err := s.DB.InOrg(r.Context(), org, func(tx pgx.Tx) error {
		var err error
		n, err = memory.Reindex(r.Context(), tx)
		return err
	})
	if err != nil {
		return err
	}
	s.resumeIndexer()
	write(w, http.StatusOK, map[string]any{"due": n})
	return nil
}
