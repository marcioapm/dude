package api

// GitHub, on a person's behalf: registering the organization's webhooks,
// and the pull request actions GitHub's own page gives — merge, update the
// branch, re-run failed checks, ask for a review. Each changes GitHub,
// records who asked, then reads the pull request back (prs.Syncer), so what
// dude shows and decides on is what GitHub now says.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/db"
	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/forge"
)

func (s *Server) githubRoutes(mux *http.ServeMux) {
	mux.Handle("POST /internal/webhooks/register", s.auth(s.registerWebhooks))
	mux.Handle("POST /internal/pull-requests/{id}/{action}", s.auth(s.pullRequestAction))
	mux.Handle("GET /internal/github-defaults", s.auth(func(w http.ResponseWriter, r *http.Request, _ string) error {
		write(w, http.StatusOK, forge.DefaultSettings())
		return nil
	}))
}

func (s *Server) forge(ctx context.Context, org string) (*forge.GitHub, error) {
	if s.Forges == nil {
		return nil, fail(http.StatusServiceUnavailable, "unavailable", "GitHub is not set up here")
	}
	gh, err := s.Forges.For(ctx, org)
	if err != nil {
		return nil, err
	}
	if gh == nil {
		return nil, fail(http.StatusConflict, "not_connected", "GitHub is not connected: add a token in settings")
	}
	return gh, nil
}

// registerWebhooks registers dude's webhook on each of the organization's
// GitHub repositories — or the one named — delivering to url, signed with
// the organization's secret. Each repository's outcome is recorded on it,
// so settings can say which are registered and why one is not.
func (s *Server) registerWebhooks(w http.ResponseWriter, r *http.Request, org string) error {
	var body struct {
		URL          string `json:"url"`
		RepositoryID string `json:"repositoryId"`
	}
	if err := read(r, &body); err != nil {
		return err
	}
	if !strings.HasPrefix(body.URL, "http://") && !strings.HasPrefix(body.URL, "https://") {
		return fail(http.StatusBadRequest, "bad_request", "url must be where GitHub can reach this dude")
	}
	gh, err := s.forge(r.Context(), org)
	if err != nil {
		return err
	}
	type repo struct{ ID, Name, URL string }
	var secret string
	var repos []repo
	if err := s.DB.InOrg(r.Context(), org, func(tx pgx.Tx) error {
		if err := tx.QueryRow(r.Context(), `SELECT COALESCE(webhook_secret, '') FROM forge_credentials WHERE forge = 'github'`).
			Scan(&secret); err != nil {
			return err
		}
		rows, err := tx.Query(r.Context(), `SELECT id, name, url FROM repositories
			WHERE ($1 = '' OR id = $1) ORDER BY name`, body.RepositoryID)
		if err != nil {
			return err
		}
		repos, err = pgx.CollectRows(rows, pgx.RowToStructByPos[repo])
		return err
	}); err != nil {
		return err
	}
	if secret == "" {
		return fail(http.StatusConflict, "conflict", "no webhook secret: connect GitHub first")
	}
	type result struct {
		RepositoryID string `json:"repositoryId"`
		Name         string `json:"name"`
		Slug         string `json:"slug"`
		HookID       string `json:"hookId,omitempty"`
		Error        string `json:"error,omitempty"`
	}
	results := []result{}
	for _, rp := range repos {
		slug := forge.SlugFromURL(rp.URL)
		if slug == "" {
			continue // a local repository: nothing on GitHub to hook
		}
		res := result{RepositoryID: rp.ID, Name: rp.Name, Slug: slug}
		id, err := gh.EnsureWebhook(r.Context(), slug, body.URL, secret)
		if err != nil {
			if !forge.Transient(err) && !isForgeRefusal(err) {
				return err
			}
			res.Error = err.Error()
		}
		res.HookID = id
		if err := s.DB.InOrg(r.Context(), org, func(tx pgx.Tx) error {
			_, err := tx.Exec(r.Context(), `UPDATE repositories SET
				webhook_id = COALESCE(NULLIF($2, ''), webhook_id),
				webhook_registered_at = CASE WHEN $3 = '' THEN now() ELSE webhook_registered_at END,
				webhook_error = NULLIF($3, '') WHERE id = $1`, rp.ID, id, res.Error)
			return err
		}); err != nil {
			return err
		}
		results = append(results, res)
	}
	write(w, http.StatusOK, map[string]any{"repositories": results})
	return nil
}

func isForgeRefusal(err error) bool {
	var e *forge.Error
	return errors.As(err, &e)
}

// pullRequestAction does to a pull request what a person asked, on GitHub.
func (s *Server) pullRequestAction(w http.ResponseWriter, r *http.Request, org string) error {
	id, action := r.PathValue("id"), r.PathValue("action")
	if !slices.Contains([]string{"merge", "update-branch", "rerun-failed", "reviewers"}, action) {
		return fail(http.StatusNotFound, "not_found", "no pull request action %q", action)
	}
	var body struct {
		Method string   `json:"method"`
		Logins []string `json:"logins"`
	}
	if err := read(r, &body); err != nil {
		return err
	}
	var pr struct {
		Number                                       int
		State, HeadSHA, URL, Repo, ProjectID, TaskID string
		Checks                                       []forge.Check
	}
	var checks []byte
	err := s.DB.InOrg(r.Context(), org, func(tx pgx.Tx) error {
		return tx.QueryRow(r.Context(), `SELECT pr.number, pr.state::text, COALESCE(pr.head_sha, ''), r.url, r.name,
				pr.project_id, pr.task_id, pr.checks_json
			FROM pull_requests pr JOIN repositories r ON r.id = pr.repository_id WHERE pr.id = $1`, id).
			Scan(&pr.Number, &pr.State, &pr.HeadSHA, &pr.URL, &pr.Repo, &pr.ProjectID, &pr.TaskID, &checks)
	})
	if db.IsNotFound(err) {
		return fail(http.StatusNotFound, "not_found", "pull request %s not found", id)
	}
	if err != nil {
		return err
	}
	if pr.State != forge.StateOpen && pr.State != forge.StateDraft {
		return fail(http.StatusConflict, "conflict", "the pull request is %s", pr.State)
	}
	slug := forge.SlugFromURL(pr.URL)
	gh, err := s.forge(r.Context(), org)
	if err != nil {
		return err
	}
	out := map[string]any{"pullRequestId": id, "action": action}
	switch action {
	case "merge":
		method := body.Method
		if method == "" {
			method = gh.Settings.MergeMethod
		}
		if !slices.Contains(forge.MergeMethods, method) {
			return fail(http.StatusBadRequest, "bad_request", "method must be one of %s", strings.Join(forge.MergeMethods, ", "))
		}
		// The head dude last read: merging a commit nobody here has seen is
		// refused by GitHub (409), and said so.
		sha, err := gh.Merge(r.Context(), slug, pr.Number, method, pr.HeadSHA)
		if err != nil {
			return forgeRefusal(err, "GitHub would not merge it")
		}
		out["method"], out["sha"] = method, sha
	case "update-branch":
		if err := gh.UpdateBranch(r.Context(), slug, pr.Number, pr.HeadSHA); err != nil {
			return forgeRefusal(err, "GitHub would not update the branch")
		}
	case "rerun-failed":
		_ = json.Unmarshal(checks, &pr.Checks)
		n, err := gh.RerunFailed(r.Context(), slug, pr.Checks)
		if err != nil {
			return forgeRefusal(err, "GitHub would not re-run the checks")
		}
		if n == 0 {
			return fail(http.StatusConflict, "conflict", "no failed check to re-run")
		}
		out["rerun"] = n
	case "reviewers":
		if len(body.Logins) == 0 {
			return fail(http.StatusBadRequest, "bad_request", "logins: whom to ask for a review")
		}
		if err := gh.RequestReviewers(r.Context(), slug, pr.Number, body.Logins); err != nil {
			return forgeRefusal(err, "GitHub would not request the review")
		}
		out["logins"] = body.Logins
	}
	// Who did it, for the task's activity: GitHub will say only that it
	// happened, as the token's owner.
	out["number"], out["repo"] = pr.Number, pr.Repo
	if err := s.DB.InOrg(r.Context(), org, func(tx pgx.Tx) error {
		return humanEvent(r.Context(), tx, org, "", runInfo{ProjectID: pr.ProjectID, TaskID: pr.TaskID},
			delivery.EvPullRequestAction, actor(r), out)
	}); err != nil {
		return err
	}
	// What GitHub says now. A read that fails is not the action failing:
	// the webhook GitHub sends, or the reconciler, reads it later.
	if s.PRs != nil {
		if err := s.PRs.Sync(r.Context(), org, id); err != nil {
			s.Log.Warn("reading a pull request back after an action failed", "pr", id, "action", action, "error", err)
		}
	}
	s.kick()
	write(w, http.StatusOK, out)
	return nil
}

// forgeRefusal passes GitHub's refusal on as the person's answer: why it
// would not, in GitHub's words. A forge that could not answer is the
// caller's problem, as any error.
func forgeRefusal(err error, what string) error {
	var e *forge.Error
	if errors.As(err, &e) && !forge.Transient(err) {
		status := http.StatusConflict
		if e.Status == 403 || e.Status == 404 {
			status = http.StatusForbidden
		}
		return fail(status, "github_refused", "%s: %s", what, e.Message)
	}
	return err
}
