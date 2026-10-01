package api

// Who can be asked for a review: GitHub's suggestions for a pull request,
// or a search by name and login, for the Request review picker and the
// setting that names reviewers for every pull request. Answers are kept a
// minute, so a person typing and backspacing costs GitHub one query per
// distinct word, not per key.

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/db"
	"github.com/marciomartins/dude/orchestrator/internal/forge"
)

const candidatesTTL = time.Minute

type candidateCache struct {
	mu   sync.Mutex
	seen map[string]cachedCandidates
	// How many times each repository's answers were forgotten: an answer
	// asked for before the latest is not kept, as it may not show who was
	// just asked.
	forgot map[string]uint64
}

type cachedCandidates struct {
	at   time.Time
	list []forge.Candidate
}

var candidates = candidateCache{seen: map[string]cachedCandidates{}, forgot: map[string]uint64{}}

// get answers from the cache, else asks GitHub and keeps the answer.
func (c *candidateCache) get(ctx context.Context, repo, key string, ask func(context.Context) ([]forge.Candidate, error)) ([]forge.Candidate, error) {
	now := time.Now()
	c.mu.Lock()
	hit, ok := c.seen[key]
	generation := c.forgot[repo]
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
	// Old answers go as new ones come: the cache never outgrows a minute's typing.
	for k, v := range c.seen {
		if now.Sub(v.at) >= candidatesTTL {
			delete(c.seen, k)
		}
	}
	if c.forgot[repo] == generation {
		c.seen[key] = cachedCandidates{at: now, list: list}
	}
	return list, nil
}

// forget drops what was kept for a repository: once someone is asked, the
// next look must show them asked.
func (c *candidateCache) forget(org, slug string) {
	repo := org + "\x00" + slug
	prefix := repo + "\x00"
	c.mu.Lock()
	defer c.mu.Unlock()
	c.forgot[repo]++
	for k := range c.seen {
		if strings.HasPrefix(k, prefix) {
			delete(c.seen, k)
		}
	}
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
	var urls []string
	if err := s.DB.InOrg(r.Context(), org, func(tx pgx.Tx) error {
		rows, err := tx.Query(r.Context(), `SELECT url FROM repositories ORDER BY created_at, name`)
		if err != nil {
			return err
		}
		urls, err = pgx.CollectRows(rows, pgx.RowTo[string])
		return err
	}); err != nil {
		return err
	}
	for _, u := range urls {
		if slug := forge.SlugFromURL(u); slug != "" {
			return s.reviewerCandidates(w, r, org, slug, 0)
		}
	}
	write(w, http.StatusOK, map[string]any{"candidates": []forge.Candidate{}})
	return nil
}

func (s *Server) reviewerCandidates(w http.ResponseWriter, r *http.Request, org, slug string, number int) error {
	if slug == "" {
		return fail(http.StatusConflict, "conflict", "the repository is not on GitHub")
	}
	words := strings.TrimSpace(r.URL.Query().Get("q"))
	if len(words) > 100 {
		return fail(http.StatusBadRequest, "bad_request", "q: at most 100 characters")
	}
	gh, err := s.forge(r.Context(), org)
	if err != nil {
		return err
	}
	repo := org + "\x00" + slug
	key := strings.Join([]string{repo, strings.ToLower(words), strconv.Itoa(number)}, "\x00")
	list, err := candidates.get(r.Context(), repo, key, func(ctx context.Context) ([]forge.Candidate, error) {
		return gh.ReviewerCandidates(ctx, slug, number, words)
	})
	if err != nil {
		return forgeRefusal(err, "GitHub would not list reviewers")
	}
	write(w, http.StatusOK, map[string]any{"candidates": list})
	return nil
}
