package api

import (
	"errors"
	"net/http"
	"sync"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/db"
	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/forge"
)

func (s *Server) sourceRoutes(mux *http.ServeMux) {
	mux.Handle("POST /internal/projects/{id}/sources/check", s.auth(s.checkSource))
	mux.Handle("GET /internal/projects/{id}/sources/repositories", s.auth(s.sourceRepositories))
	mux.Handle("GET /internal/projects/{id}/sources/repositories/{repo}/branches", s.auth(s.sourceBranches))
}

// branchesShown bounds a branch lookup's answer.
const branchesShown = 50

// connectedGitHub is the organisation's GitHub connection, or 502
// github_unavailable when there is none to ask.
func (s *Server) connectedGitHub(r *http.Request, org string) (*forge.GitHub, error) {
	if s.Forges == nil {
		return nil, fail(http.StatusBadGateway, "github_unavailable", "GitHub is not set up here")
	}
	gh, err := s.Forges.For(r.Context(), org)
	if err != nil {
		return nil, err
	}
	if gh == nil {
		return nil, fail(http.StatusBadGateway, "github_unavailable", "GitHub is not connected for this organisation")
	}
	return gh, nil
}

// sourceRepositories is the project's repositories with whether dude may
// push to each with the organisation's GitHub connection: where existing
// work can be taken over from. Each is asked of GitHub; one it could not
// be asked about says why.
func (s *Server) sourceRepositories(w http.ResponseWriter, r *http.Request, org string) error {
	gh, err := s.connectedGitHub(r, org)
	if err != nil {
		return err
	}
	type repo struct{ ID, Name, URL string }
	var repos []repo
	if err := s.DB.InOrg(r.Context(), org, func(tx pgx.Tx) error {
		rows, err := tx.Query(r.Context(), `SELECT id, name, url FROM repositories WHERE project_id = $1 ORDER BY name LIMIT 50`,
			r.PathValue("id"))
		if err != nil {
			return err
		}
		repos, err = pgx.CollectRows(rows, pgx.RowToStructByPos[repo])
		return err
	}); err != nil {
		return err
	}
	// Checked four at a time, after the read's transaction has ended: one
	// by one, fifty of them outlast the backend's deadline.
	out := make([]map[string]any, len(repos))
	next := make(chan int)
	var wg sync.WaitGroup
	for range min(4, len(repos)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range next {
				rp := repos[i]
				entry := map[string]any{"id": rp.ID, "name": rp.Name, "writable": false}
				if forge.SlugFromURL(rp.URL) == "" {
					entry["reason"] = "not on GitHub"
				} else if err := gh.CheckPushAccess(r.Context(), rp.URL); err != nil {
					entry["reason"] = err.Error()
				} else {
					entry["writable"] = true
				}
				out[i] = entry
			}
		}()
	}
	for i := range repos {
		if r.Context().Err() != nil {
			break
		}
		next <- i
	}
	close(next)
	wg.Wait()
	if err := r.Context().Err(); err != nil {
		return err
	}
	write(w, http.StatusOK, map[string]any{"repositories": out})
	return nil
}

// sourceBranches lists a project repository's branches containing q, at
// most branchesShown, read from GitHub in bounded pages.
func (s *Server) sourceBranches(w http.ResponseWriter, r *http.Request, org string) error {
	gh, err := s.connectedGitHub(r, org)
	if err != nil {
		return err
	}
	var url string
	err = s.DB.InOrg(r.Context(), org, func(tx pgx.Tx) error {
		return tx.QueryRow(r.Context(), `SELECT url FROM repositories WHERE id = $1 AND project_id = $2`,
			r.PathValue("repo"), r.PathValue("id")).Scan(&url)
	})
	if db.IsNotFound(err) {
		return fail(http.StatusNotFound, "not_in_project", "that repository is not one of this project's")
	}
	if err != nil {
		return err
	}
	slug := forge.SlugFromURL(url)
	if slug == "" {
		return fail(http.StatusUnprocessableEntity, "not_github", "that repository is not on GitHub")
	}
	names, more, err := gh.Branches(r.Context(), slug, r.URL.Query().Get("q"), branchesShown)
	if err != nil {
		if forge.Refused(err) {
			return fail(http.StatusUnprocessableEntity, "no_access", "%v", err)
		}
		return fail(http.StatusBadGateway, "github_unavailable", "GitHub could not be asked about it: %v", err)
	}
	write(w, http.StatusOK, map[string]any{"branches": db.NonNil(names), "more": more})
	return nil
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
