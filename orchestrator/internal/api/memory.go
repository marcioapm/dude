package api

// Memory, for the settings pages: search with why each result ranked where
// it did, the memories themselves, and the index behind both. The backend
// has decided who may do what (docs/design/memory.md); it names the person
// acting (X-Dude-Person) and whether they are an admin (X-Dude-Admin), and
// this refuses an edit of another's memory to anyone else.

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/ledger"
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
	return memory.Actor{Type: ledger.ActorHuman, ID: actor(r)}
}

func list(v string) []string {
	var out []string
	for _, s := range strings.Split(v, ",") {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
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
	err := s.DB.InOrg(r.Context(), org, func(tx pgx.Tx) error {
		var err error
		out, err = memory.Search(r.Context(), tx, s.Embedder, memory.Query{
			Text: q.Get("q"), Project: q.Get("project"), Types: list(q.Get("types")), About: list(q.Get("about")), Limit: limit,
		})
		if err != nil {
			return err
		}
		return labelResults(r.Context(), tx, out.Results)
	})
	if err != nil {
		return err
	}
	write(w, http.StatusOK, out)
	return nil
}

// labelResults puts a task's key before its title and its status beside it,
// as the tree shows tasks.
func labelResults(ctx context.Context, tx pgx.Tx, results []memory.Result) error {
	var refs []memory.Ref
	for _, r := range results {
		if r.Type == "task" {
			refs = append(refs, memory.Ref{Type: "task", ID: r.ID})
		}
	}
	labels, err := memory.Labels(ctx, tx, refs)
	if err != nil {
		return err
	}
	for i, r := range results {
		if l, ok := labels["task/"+r.ID]; ok {
			results[i].Key, results[i].Status = l.Label, l.Status
			results[i].Title = strings.TrimPrefix(r.Title, l.Label+" ")
		}
	}
	return nil
}

func (s *Server) memoryList(w http.ResponseWriter, r *http.Request, org string) error {
	q := r.URL.Query()
	var out []memory.Memory
	err := s.DB.InOrg(r.Context(), org, func(tx pgx.Tx) error {
		var err error
		out, err = memory.List(r.Context(), tx, memory.ListQuery{
			Project: q.Get("project"), Scope: q.Get("scope"), Author: q.Get("author"),
			Text: q.Get("q"), Archived: q.Get("archived") == "true",
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
		m, err = memory.Get(r.Context(), tx, r.PathValue("id"))
		return err
	})
	if err != nil {
		return memoryError(err)
	}
	write(w, http.StatusOK, m)
	return nil
}

type memoryBody struct {
	ProjectID *string       `json:"projectId"`
	Title     *string       `json:"title"`
	Content   *string       `json:"content"`
	Kind      *string       `json:"kind"`
	About     *[]memory.Ref `json:"about"`
}

func (s *Server) memoryCreate(w http.ResponseWriter, r *http.Request, org string) error {
	var b memoryBody
	if err := read(r, &b); err != nil {
		return err
	}
	who := r.Header.Get("X-Dude-Person")
	n := memory.New{Author: memory.Author{Kind: "person", PersonID: who}}
	if b.ProjectID != nil {
		n.ProjectID = *b.ProjectID
	}
	if b.Title != nil {
		n.Title = *b.Title
	}
	if b.Content != nil {
		n.Content = *b.Content
	}
	if b.Kind != nil {
		n.Kind = *b.Kind
	}
	if b.About != nil {
		n.About = *b.About
	}
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
	if r.Header.Get("X-Dude-Admin") == "true" {
		return nil
	}
	if p := r.Header.Get("X-Dude-Person"); p != "" && m.Author.Kind == "person" && m.Author.PersonID == p {
		return nil
	}
	return fail(http.StatusForbidden, "not_admin", "only an organisation admin changes a memory someone else wrote")
}

func (s *Server) memoryUpdate(w http.ResponseWriter, r *http.Request, org string) error {
	var b memoryBody
	if err := read(r, &b); err != nil {
		return err
	}
	var m memory.Memory
	err := s.DB.InOrg(r.Context(), org, func(tx pgx.Tx) error {
		cur, err := memory.Get(r.Context(), tx, r.PathValue("id"))
		if err != nil {
			return err
		}
		if err := mayChange(r, cur); err != nil {
			return err
		}
		m, err = memory.Update(r.Context(), tx, org, cur.ID, memory.Patch{
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
		cur, err := memory.Get(r.Context(), tx, r.PathValue("id"))
		if err != nil {
			return err
		}
		if err := mayChange(r, cur); err != nil {
			return err
		}
		m, err = memory.Archive(r.Context(), tx, org, cur.ID, action == "archive", person(r))
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
		out, err = memory.IndexStatus(r.Context(), tx, s.Embedder, r.URL.Query().Get("project"))
		return err
	})
	if err != nil {
		return err
	}
	write(w, http.StatusOK, out)
	return nil
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
	s.kick()
	write(w, http.StatusOK, map[string]any{"due": n})
	return nil
}

func (s *Server) memoryReindex(w http.ResponseWriter, r *http.Request, org string) error {
	if r.Header.Get("X-Dude-Admin") != "true" {
		return fail(http.StatusForbidden, "not_admin", "only an organisation admin reindexes")
	}
	var n int64
	err := s.DB.InOrg(r.Context(), org, func(tx pgx.Tx) error {
		var err error
		n, err = memory.Reindex(r.Context(), tx)
		return err
	})
	if err != nil {
		return err
	}
	s.kick()
	write(w, http.StatusOK, map[string]any{"due": n})
	return nil
}
