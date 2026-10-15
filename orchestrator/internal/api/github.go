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
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/db"
	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/forge"
	"github.com/marciomartins/dude/orchestrator/internal/prs"
)

func (s *Server) githubRoutes(mux *http.ServeMux) {
	mux.Handle("POST /internal/webhooks/register", s.auth(s.registerWebhooks))
	mux.Handle("POST /internal/pull-requests/{id}/{action}", s.auth(s.pullRequestAction))
	mux.Handle("GET /internal/pull-requests/{id}/reviewer-candidates", s.auth(s.pullRequestReviewerCandidates))
	mux.Handle("GET /internal/reviewer-candidates", s.auth(s.organizationReviewerCandidates))
	mux.Handle("GET /internal/github-settings", s.auth(s.githubSettings))
	mux.Handle("PATCH /internal/github-settings", s.auth(s.updateGithubSettings))
	mux.Handle("GET /internal/github/users/{login}", s.auth(s.githubUser))
	mux.Handle("POST /internal/github/verify-app", s.auth(s.verifyGithubApp))
}

// verifyGithubApp mints an installation token for one of the
// organisation's GitHub repositories, the first by project and name, and
// reads the App's bot: whether the App's key, id and installation work, or
// GitHub's reason they do not. Always 200, the outcome in the body.
func (s *Server) verifyGithubApp(w http.ResponseWriter, r *http.Request, org string) error {
	gh, err := s.forge(r.Context(), org)
	if err != nil {
		return err
	}
	if !gh.IsApp() {
		return fail(http.StatusConflict, "conflict", "GitHub is connected with a token, not an App")
	}
	var slug string
	if err := s.DB.InOrg(r.Context(), org, func(tx pgx.Tx) error {
		rows, err := tx.Query(r.Context(), `SELECT r.url FROM repositories r JOIN projects p ON p.id = r.project_id ORDER BY p.name, r.name`)
		if err != nil {
			return err
		}
		urls, err := pgx.CollectRows(rows, pgx.RowTo[string])
		for _, u := range urls {
			if slug = forge.SlugFromURL(u); slug != "" {
				break
			}
		}
		return err
	}); err != nil {
		return err
	}
	out := map[string]any{"ok": false, "repository": nil}
	if slug == "" {
		out["reason"] = "no GitHub repository is linked to a project yet: add one, then verify"
		write(w, http.StatusOK, out)
		return nil
	}
	out["repository"] = slug
	if _, err := gh.Token(r.Context(), slug); err != nil {
		out["reason"] = "GitHub would not give the App a token for " + slug + ": " + err.Error()
		write(w, http.StatusOK, out)
		return nil
	}
	bot, err := gh.Bot(r.Context())
	if err != nil {
		out["reason"] = "the App has a token for " + slug + ", but GitHub would not say who its bot is: " + err.Error()
		write(w, http.StatusOK, out)
		return nil
	}
	out["ok"], out["bot"] = true, bot.Name
	write(w, http.StatusOK, out)
	return nil
}

// githubUser is a GitHub login's numeric id, read with the organisation's
// credential: what a person's profile keeps beside their login. 404
// github_user_unknown only for GitHub's 404 on the user itself; a forge
// that cannot be asked (none connected, GitHub down, an App with no
// installation to ask as) is 502 github_unavailable, which the caller
// stores the login without an id for.
func (s *Server) githubUser(w http.ResponseWriter, r *http.Request, org string) error {
	gh, err := s.forge(r.Context(), org)
	if err != nil {
		return err
	}
	if _, err := gh.Token(r.Context(), ""); err != nil {
		return fail(http.StatusBadGateway, "github_unavailable", "GitHub could not be asked: %s", err.Error())
	}
	login := r.PathValue("login")
	id, err := gh.UserID(r.Context(), login)
	var e *forge.Error
	if errors.As(err, &e) && e.NotFound() {
		return fail(http.StatusNotFound, "github_user_unknown", "GitHub has no user %q", login)
	}
	if err != nil {
		return fail(http.StatusBadGateway, "github_unavailable", "GitHub could not be asked: %s", err.Error())
	}
	write(w, http.StatusOK, map[string]any{"login": login, "id": id})
	return nil
}

// githubSettings is how dude behaves on GitHub for the organization, every
// value filled in: what is stored, over the defaults.
func (s *Server) githubSettings(w http.ResponseWriter, r *http.Request, org string) error {
	raw, err := s.storedGithubSettings(r.Context(), org)
	if err != nil {
		return err
	}
	write(w, http.StatusOK, forge.ReadSettings(raw))
	return nil
}

func (s *Server) storedGithubSettings(ctx context.Context, org string) ([]byte, error) {
	var raw []byte
	err := s.DB.InOrg(ctx, org, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT settings FROM forge_credentials WHERE forge = 'github'`).Scan(&raw)
	})
	if db.IsNotFound(err) {
		return nil, fail(http.StatusConflict, "not_connected", "GitHub is not connected: add a token first")
	}
	return raw, err
}

// updateGithubSettings changes the settings named, and only those. A value
// dude does not know is refused, not stored: ReadSettings would read it as
// the default, and a person would think it saved.
func (s *Server) updateGithubSettings(w http.ResponseWriter, r *http.Request, org string) error {
	var change map[string]json.RawMessage
	if err := read(r, &change); err != nil {
		return err
	}
	raw, err := s.storedGithubSettings(r.Context(), org)
	if err != nil {
		return err
	}
	stored := map[string]json.RawMessage{}
	_ = json.Unmarshal(raw, &stored)
	for k, v := range change {
		if !slices.Contains(forge.SettingKeys(), k) {
			return fail(http.StatusBadRequest, "bad_request", "no GitHub setting %q", k)
		}
		stored[k] = v
	}
	merged, _ := json.Marshal(stored)
	settings, err := forge.ParseSettings(merged)
	if err != nil {
		return fail(http.StatusBadRequest, "bad_request", "%s", err.Error())
	}
	if err := s.DB.InOrg(r.Context(), org, func(tx pgx.Tx) error {
		_, err := tx.Exec(r.Context(), `UPDATE forge_credentials SET settings = $1::jsonb, updated_at = now() WHERE forge = 'github'`, merged)
		return err
	}); err != nil {
		return err
	}
	write(w, http.StatusOK, settings)
	return nil
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
// so settings can say which are registered and why one is not; the url is
// recorded on the organization, for the reconciler to repair hooks that
// are missing (prs.RepairWebhooks).
//
// background answers at once and registers afterwards, only where a
// repository has no healthy hook to url: what connecting GitHub asks, which
// must not wait on two GitHub calls per repository.
func (s *Server) registerWebhooks(w http.ResponseWriter, r *http.Request, org string) error {
	var body struct {
		URL          string `json:"url"`
		RepositoryID string `json:"repositoryId"`
		Background   bool   `json:"background"`
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
	var hasSecret bool
	if err := s.DB.InOrg(r.Context(), org, func(tx pgx.Tx) error {
		return tx.QueryRow(r.Context(), `UPDATE forge_credentials SET webhook_url = $1 WHERE forge = 'github'
			RETURNING COALESCE(webhook_secret, '') <> ''`, body.URL).Scan(&hasSecret)
	}); err != nil {
		return err
	}
	if !hasSecret {
		return fail(http.StatusConflict, "conflict", "no webhook secret: connect GitHub first")
	}
	reg := prs.Registration{URL: body.URL, RepositoryID: body.RepositoryID}
	if body.Background {
		reg.OnlyMissing = true
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), backgroundRegistration)
			defer cancel()
			// What this leaves undone, the reconciler's repair takes up.
			if _, _, err := prs.RegisterWebhooks(ctx, s.DB, gh, org, reg); err != nil {
				s.Log.Warn("registering webhooks in the background failed", "organization", org, "error", err)
			}
		}()
		write(w, http.StatusAccepted, map[string]any{"background": true})
		return nil
	}
	results, _, err := prs.RegisterWebhooks(r.Context(), s.DB, gh, org, reg)
	if err != nil {
		return err
	}
	write(w, http.StatusOK, map[string]any{"repositories": results})
	return nil
}

// backgroundRegistration bounds a connect's background registration.
const backgroundRegistration = 10 * time.Minute

// pullRequestAction does to a pull request what a person asked, on GitHub.
func (s *Server) pullRequestAction(w http.ResponseWriter, r *http.Request, org string) error {
	id, action := r.PathValue("id"), r.PathValue("action")
	if !slices.Contains([]string{"merge", "update-branch", "rerun-failed", "reviewers", "resolve-conflicts"}, action) {
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
		Mergeable                                    string
		Checks                                       []forge.Check
	}
	var checks []byte
	err := s.DB.InOrg(r.Context(), org, func(tx pgx.Tx) error {
		return tx.QueryRow(r.Context(), `SELECT pr.number, pr.state::text, COALESCE(pr.head_sha, ''), r.url, r.name,
				pr.project_id, pr.task_id, pr.checks_json, pr.mergeable_state
			FROM pull_requests pr JOIN repositories r ON r.id = pr.repository_id WHERE pr.id = $1`, id).
			Scan(&pr.Number, &pr.State, &pr.HeadSHA, &pr.URL, &pr.Repo, &pr.ProjectID, &pr.TaskID, &checks, &pr.Mergeable)
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
	if action == "resolve-conflicts" {
		return s.resolveConflicts(w, r, org, id, pr.TaskID, pr.Repo, pr.Number, pr.HeadSHA, pr.Mergeable)
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
		// Merged only as dude would call it ready, read from GitHub now: a
		// button a person pressed on a stale page must not merge what CI
		// has since failed. GitHub's own branch protection applies too.
		now, err := gh.PullRequest(r.Context(), slug, pr.Number)
		if err != nil {
			return forgeRefusal(err, "GitHub would not say how the pull request stands")
		}
		if now.State != forge.StateOpen {
			return fail(http.StatusConflict, "not_ready", "the pull request is %s", now.State)
		}
		if blockers := forge.Blockers(now); len(blockers) > 0 {
			return fail(http.StatusConflict, "not_ready", "not ready to merge: %s", strings.Join(blockers, "; "))
		}
		// The head dude showed the person: one pushed since is theirs to
		// look at first, and GitHub refuses (409) a head that moves now.
		if pr.HeadSHA != "" && now.HeadSHA != pr.HeadSHA {
			return fail(http.StatusConflict, "not_ready", "the pull request changed since it was last read: look again")
		}
		sha, err := gh.Merge(r.Context(), slug, pr.Number, method, now.HeadSHA)
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
			delivery.EvPullRequestAction, principalOf(r), out)
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

// resolveConflicts asks the task's workflow to integrate the pull request's
// base, as the owner deciding its escalation would. Only a pull request dude
// last read as conflicting; the workflow checks GitHub again before it
// starts the integration Run.
func (s *Server) resolveConflicts(w http.ResponseWriter, r *http.Request, org, id, taskID, repo string, number int,
	head, mergeable string) error {
	if mergeable != forge.MergeConflicting {
		return fail(http.StatusConflict, "conflict", "the pull request does not conflict with its base")
	}
	p := principalOf(r)
	out := map[string]any{"pullRequestId": id, "action": "resolve-conflicts", "number": number, "repo": repo}
	err := s.DB.InOrg(r.Context(), org, func(tx pgx.Tx) error {
		if err := ownerOnly(r.Context(), tx, taskID, p.Person, "resolve conflicts on"); err != nil {
			return err
		}
		if err := escalationFailure(delivery.ResolveConflictsTx(r.Context(), tx, org, taskID, id, repo, number, head,
			delivery.EscalationDecision{ActorType: p.ActorType, ActorID: p.Actor, Person: p.Person})); err != nil {
			return err
		}
		var projectID string
		if err := tx.QueryRow(r.Context(), `SELECT project_id FROM tasks WHERE id = $1`, taskID).Scan(&projectID); err != nil {
			return err
		}
		return humanEvent(r.Context(), tx, org, "", runInfo{ProjectID: projectID, TaskID: taskID},
			delivery.EvPullRequestAction, p, out)
	})
	if err != nil {
		return err
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
	if forge.Refused(err) && errors.As(err, &e) {
		status := http.StatusConflict
		if e.Status == 403 || e.Status == 404 {
			status = http.StatusForbidden
		}
		return fail(status, "github_refused", "%s: %s", what, e.Message)
	}
	return err
}
