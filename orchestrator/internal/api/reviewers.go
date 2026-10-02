package api

// Who can be asked for a review: GitHub's suggestions for a pull request,
// or a search by name and login, for the Request review picker and the
// setting that names reviewers for every pull request. Who was already
// asked is not part of the answer: the page has it, from the pull
// request's reviews. Answers are kept a minute, so a person typing and
// backspacing costs GitHub one query per distinct word, not per key.

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/db"
	"github.com/marciomartins/dude/orchestrator/internal/forge"
)

const candidatesTTL = time.Minute

// candidateCache keeps GitHub's answers a minute, by what was asked.
type candidateCache struct {
	mu   sync.Mutex
	kept map[candidateQuery]cachedCandidates
}

type candidateQuery struct {
	org, slug, words string
	number           int
}

type cachedCandidates struct {
	at   time.Time
	list []forge.Candidate
}

// get answers from the cache, else asks GitHub and keeps the answer.
func (c *candidateCache) get(ctx context.Context, q candidateQuery, ask func(context.Context) ([]forge.Candidate, error)) ([]forge.Candidate, error) {
	now := time.Now()
	c.mu.Lock()
	hit, ok := c.kept[q]
	c.mu.Unlock()
	if ok && now.Sub(hit.at) < candidatesTTL {
		return hit.list, nil
	}
	list, err := ask(ctx)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.kept == nil {
		c.kept = map[candidateQuery]cachedCandidates{}
	}
	// Old answers go as new ones come: the cache never outgrows a minute's typing.
	for k, v := range c.kept {
		if now.Sub(v.at) >= candidatesTTL {
			delete(c.kept, k)
		}
	}
	c.kept[q] = cachedCandidates{at: now, list: list}
	return list, nil
}

// pullRequestReviewerCandidates: for a pull request, GitHub's suggestions
// (no `q`), or who in its repository matches `q`.
func (s *Server) pullRequestReviewerCandidates(w http.ResponseWriter, r *http.Request, org string) error {
	id := r.PathValue("id")
	var number int
	var url string
	err := s.DB.InOrg(r.Context(), org, func(tx pgx.Tx) error {
		return tx.QueryRow(r.Context(), `SELECT pr.number, r.url FROM pull_requests pr
			JOIN repositories r ON r.id = pr.repository_id WHERE pr.id = $1`, id).Scan(&number, &url)
	})
	if db.IsNotFound(err) {
		return fail(http.StatusNotFound, "not_found", "pull request %s not found", id)
	}
	if err != nil {
		return err
	}
	return s.reviewerCandidates(w, r, org, forge.SlugFromURL(url), number)
}

// organizationReviewerCandidates: who matches `q` in the organization's
// GitHub repositories — the first one's, as GitHub has no search of
// everyone across an organization's repositories; a team or a member of
// the owner is assignable in each.
func (s *Server) organizationReviewerCandidates(w http.ResponseWriter, r *http.Request, org string) error {
	var slug string
	if err := s.DB.InOrg(r.Context(), org, func(tx pgx.Tx) error {
		rows, err := tx.Query(r.Context(), `SELECT url FROM repositories ORDER BY created_at, name`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() && slug == "" {
			var url string
			if err := rows.Scan(&url); err != nil {
				return err
			}
			slug = forge.SlugFromURL(url)
		}
		return rows.Err()
	}); err != nil {
		return err
	}
	if slug == "" {
		write(w, http.StatusOK, map[string]any{"candidates": []forge.Candidate{}})
		return nil
	}
	return s.reviewerCandidates(w, r, org, slug, 0)
}

func (s *Server) reviewerCandidates(w http.ResponseWriter, r *http.Request, org, slug string, number int) error {
	if slug == "" {
		return fail(http.StatusConflict, "conflict", "the repository is not on GitHub")
	}
	words := strings.TrimSpace(r.URL.Query().Get("q"))
	if len(words) > 100 {
		return fail(http.StatusBadRequest, "bad_request", "q: at most 100 characters")
	}
	q := candidateQuery{org: org, slug: slug, words: strings.ToLower(words), number: number}
	list, err := s.candidates.get(r.Context(), q, func(ctx context.Context) ([]forge.Candidate, error) {
		// GitHub only on a miss: a cached word costs no credential read.
		gh, err := s.forge(ctx, org)
		if err != nil {
			return nil, err
		}
		list, err := gh.ReviewerCandidates(ctx, slug, number, words)
		if err != nil {
			return nil, forgeRefusal(err, "GitHub would not list reviewers")
		}
		return list, nil
	})
	if err != nil {
		return err
	}
	write(w, http.StatusOK, map[string]any{"candidates": list})
	return nil
}
