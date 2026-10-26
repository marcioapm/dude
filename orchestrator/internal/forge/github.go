// Package forge talks to GitHub: pull requests, branches, and the webhooks
// that report on them.
//
// An interface-shaped client rather than calls scattered through the
// workflow, because authentication will move from a personal access token to
// a GitHub App, and because a forge is the one dependency here that rate
// limits and has outages — the thing a test must be able to replace.
package forge

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Pull request states, in dude's vocabulary.
const (
	StateDraft  = "draft"
	StateOpen   = "open"
	StateMerged = "merged"
	StateClosed = "closed"
)

// Check rollups. Coarser than GitHub's on purpose: the question a person
// asks is "can this merge", not which of twelve conclusions applies.
const (
	ChecksPending = "pending"
	ChecksPassing = "passing"
	ChecksFailing = "failing"
	ChecksUnknown = "unknown"

	ReviewPending          = "pending"
	ReviewApproved         = "approved"
	ReviewChangesRequested = "changes_requested"
)

// Feedback kinds.
const (
	KindComment          = "comment"
	KindLineComment      = "line_comment"
	KindChangesRequested = "changes_requested"
	// A review's own text, approving or commenting: kept, because "approved,
	// but rename this" is a request too.
	KindReview = "review"
)

// Credential is how an organization authenticates to GitHub.
type Credential struct {
	Auth string // "pat" or "github_app"
	// A PAT, or a GitHub App's PEM private key.
	Secret     string
	APIBaseURL string
	// A GitHub App's: its id (the JWT's issuer), and an installation used
	// when GitHub names none for an owner; "" resolves every owner's.
	AppID, InstallationID string
}

type PullRequestRef struct {
	Number  int
	NodeID  string
	URL     string
	State   string
	HeadSHA string
}

type Status struct {
	PullRequestRef
	Checks string
	Review string // pending | approved | changes_requested
	// What a person reads beyond the rollups: every check by name, each
	// reviewer's latest verdict, whether it merges, how far main has moved
	// ahead, and the review threads nobody has resolved.
	CheckList         []Check
	Reviews           []Review
	Mergeable         string // clean | behind | conflicting | unknown
	BehindBy          int
	UnresolvedThreads int
	// GitHub would not count the threads (an old Enterprise, a token
	// without the scope, a GraphQL error): UnresolvedThreads is not a
	// reading, and whoever holds the last one keeps it.
	ThreadsUnknown bool
	// Every review as GitHub listed it: Feedback reads their words from
	// the same listing rather than asking again.
	reviews []ghReview
}

// Check is one check on a pull request's head: a check run (GitHub
// Actions, an app) or a commit status, by the name GitHub shows.
type Check struct {
	Name       string `json:"name"`
	Status     string `json:"status"`     // queued | in_progress | completed
	Conclusion string `json:"conclusion"` // success | failure | … ; "" while running
	URL        string `json:"url"`
	DurationMs int64  `json:"durationMs"`
	// A check run's id, for its log and for running it again; 0 for a
	// commit status, which has neither.
	RunID int64 `json:"runId,omitempty"`
	// A commit status's id; 0 for a check run. Each status posted is a new
	// one, so a re-run's is not the failure before it. Never a RunID: it
	// has no log and is not run again through the checks API.
	StatusID int64 `json:"statusId,omitempty"`
	// The app that reported it: "github-actions" for Actions, whose check
	// run is a job, re-run through the Actions API.
	App string `json:"app,omitempty"`
	// Set only on an entry that is not a check but a source dude could not
	// read (CheckRunsForbidden). Its Status is CheckUnavailable, which
	// readers without this field take as pending: blocking, never failed.
	Diagnostic string `json:"diagnostic,omitempty"`
}

// CheckRunsForbidden: GitHub refused the check-runs listing (403, not a
// rate limit). CI may exist that the token cannot see.
const (
	CheckRunsForbidden = "check_runs_forbidden"
	CheckUnavailable   = "unavailable"
)

// Failed says the check finished and failed: what wakes a fixer.
func (c Check) Failed() bool { return checkRunState(c.Status, c.Conclusion) == ChecksFailing }

// CheckDiagnostic is the first diagnostic code in a check list, or "".
func CheckDiagnostic(checks []Check) string {
	for _, c := range checks {
		if c.Diagnostic != "" {
			return c.Diagnostic
		}
	}
	return ""
}

// Review is one reviewer's latest word on a pull request: APPROVED,
// CHANGES_REQUESTED, COMMENTED, DISMISSED, or REQUESTED for one asked and
// yet to answer.
type Review struct {
	Login       string  `json:"login"`
	State       string  `json:"state"`
	SubmittedAt *string `json:"submittedAt"`
	AvatarURL   string  `json:"avatarUrl,omitempty"`
	// A team asked for a review ("org/slug"), not a person.
	Team bool `json:"team,omitempty"`
	// Asked again since this verdict: it stands on GitHub, but they owe
	// another look.
	Rerequested bool `json:"rerequested,omitempty"`
}

// Mergeable states.
const (
	MergeClean       = "clean"
	MergeBehind      = "behind"
	MergeConflicting = "conflicting"
	MergeUnknown     = "unknown"
)

// Feedback is one thing a person left on a pull request. Conversation
// comments, line comments and a changes-requested review body arrive in one
// shape because, to a fixer, they are the same: something to be different.
type Feedback struct {
	ID        string
	Author    string
	Body      string
	Path      string
	CreatedAt string
	// When a comment was last edited, as GitHub says (RFC 3339); equal to
	// CreatedAt for one never edited, empty for a review.
	UpdatedAt string
	Kind      string
	// Where a person reads it on GitHub, when GitHub said.
	URL string
}

// Error is GitHub refusing a request.
type Error struct {
	Status  int
	Message string
	// How long GitHub asked to be left alone (Retry-After, or the primary
	// limit's reset), capped at MaxRetryAfter; 0 when it did not say.
	RetryAfter time.Duration
	// Refusal is dude's own reason for refusing an agent's read (one of the
	// Refusal* kinds), "" when GitHub's answer is the refusal.
	Refusal string
	// AcceptedPermissions is a 403's X-Accepted-GitHub-Permissions: the
	// permission sets that would have been let through, sets split by "; ",
	// each "name=access" joined by ",". "" when GitHub sent none, or sent
	// anything else (see acceptedPermissions).
	AcceptedPermissions string
}

// acceptedPermissions is h's X-Accepted-GitHub-Permissions, normalised,
// when every entry is a lowercase permission name and read, write or
// admin; else "". Only such a value may reach an agent or the ledger.
func acceptedPermissions(h http.Header) string {
	raw := strings.TrimSpace(h.Get("X-Accepted-GitHub-Permissions"))
	if raw == "" || len(raw) > 512 {
		return ""
	}
	var sets []string
	for _, set := range strings.Split(raw, ";") {
		var perms []string
		for _, p := range strings.Split(set, ",") {
			name, access, ok := strings.Cut(strings.TrimSpace(p), "=")
			if !ok || !permissionName.MatchString(name) || access != "read" && access != "write" && access != "admin" {
				return ""
			}
			perms = append(perms, name+"="+access)
		}
		sets = append(sets, strings.Join(perms, ","))
	}
	return strings.Join(sets, "; ")
}

var permissionName = regexp.MustCompile(`^[a-z][a-z_]{0,63}$`)

// Why dude refused an agent's read on its own account. Message then holds
// dude's words only, never what GitHub sent.
const (
	RefusalMoved       = "moved"        // a permanent redirect of /repos/{slug} itself
	RefusalMovedItem   = "moved_item"   // one of an item under it, its repository where it was
	RefusalRedirect    = "redirect"     // any other redirect
	RefusalRateReserve = "rate_reserve" // the rest of the rate limit is delivery's
	RefusalTooLarge    = "too_large"    // an answer past the agent read limit
)

// Moved says GitHub answered an agent's read of a repository itself with a
// permanent redirect: renamed or transferred.
func Moved(err error) bool {
	var e *Error
	return errors.As(err, &e) && e.Refusal == RefusalMoved
}

// itemMoved is err, for a permanent redirect of an item of slug's (an
// issue, a pull request): RefusalMoved when the repository's own path
// redirects too, as for a rename, else the item's RefusalMovedItem.
func (g *GitHub) itemMoved(ctx context.Context, slug string, err error) error {
	var e *Error
	if !errors.As(err, &e) || e.Refusal != RefusalMovedItem {
		return err
	}
	var repo struct{}
	if repoErr := g.do(ctx, "GET", "/repos/"+slug, nil, &repo); Moved(repoErr) {
		return repoErr
	}
	return err
}

// MaxRetryAfter bounds a wait GitHub asks for, so a garbled header cannot
// park a Run for days.
const MaxRetryAfter = time.Hour

// RetryAfter is the wait a forge error carries, or 0.
func RetryAfter(err error) time.Duration {
	var e *Error
	if errors.As(err, &e) {
		return e.RetryAfter
	}
	return 0
}

// retryAfter reads GitHub's wait from a limited response: Retry-After in
// seconds, else X-RateLimit-Reset (epoch seconds) once no requests remain.
func retryAfter(h http.Header, now time.Time) time.Duration {
	var wait time.Duration
	if s, err := strconv.ParseInt(strings.TrimSpace(h.Get("Retry-After")), 10, 64); err == nil {
		wait = time.Duration(min(s, int64(MaxRetryAfter/time.Second))) * time.Second
	} else if h.Get("X-RateLimit-Remaining") == "0" {
		if reset, err := strconv.ParseInt(strings.TrimSpace(h.Get("X-RateLimit-Reset")), 10, 64); err == nil {
			wait = time.Unix(reset, 0).Sub(now)
		}
	}
	return max(0, min(wait, MaxRetryAfter))
}

func (e *Error) Error() string { return fmt.Sprintf("github %d: %s", e.Status, e.Message) }

// AlreadyExists: GitHub refused a PR because one is open for the branch.
// Only that; other 422s are validation errors that mean something else.
func (e *Error) AlreadyExists() bool {
	return e.Status == 422 && strings.Contains(strings.ToLower(e.Message), "already exists")
}

// Transient says whether trying again later could succeed: the forge was
// unreachable, rate-limiting or failing, rather than refusing. GitHub's
// older secondary-limit 403 says "abuse detection" instead of "rate limit".
func Transient(err error) bool {
	var e *Error
	if errors.As(err, &e) {
		msg := strings.ToLower(e.Message)
		return e.Status == 429 || e.Status >= 500 ||
			e.Status == 403 && (strings.Contains(msg, "rate limit") || strings.Contains(msg, "abuse detection"))
	}
	// No answer from GitHub at all: the request never completed.
	var u *Unreachable
	return errors.As(err, &u)
}

// RateLimited says GitHub refused for its rate limit: a 429, or a 403
// it marks as one (markRateLimit, or its own message).
func RateLimited(err error) bool {
	var e *Error
	return errors.As(err, &e) && (e.Status == 429 || e.Status == 403 && Transient(err))
}

// Refused says GitHub answered, and would not: a refusal (403, 404, 422…)
// asking again will not mend, as opposed to a failure that may pass.
func Refused(err error) bool {
	var e *Error
	return errors.As(err, &e) && !Transient(err)
}

// Unreachable is a request that got no answer: refused, timed out, cut off.
type Unreachable struct{ Err error }

func (u *Unreachable) Error() string { return "github unreachable: " + u.Err.Error() }
func (u *Unreachable) Unwrap() error { return u.Err }

// NotFound: a branch or repository that is not there.
func (e *Error) NotFound() bool { return e.Status == 404 }

// How long one request may take. fetch-style clients wait forever by
// default, and one forge that stops answering must not stall everything
// queued behind it.
const requestTimeout = 15 * time.Second

type GitHub struct {
	cred        Credential
	http        *http.Client
	testGitHost string
	// How the organization wants dude to behave on GitHub.
	Settings Settings
	// The clock installation tokens' expiry is read against.
	now func() time.Time
}

// WithClock replaces the clock a GitHub App's tokens are judged by.
func WithClock(now func() time.Time) func(*GitHub) {
	return func(g *GitHub) { g.now = now }
}

// WithTestGitHost permits the test git daemon's separate port only on this
// exact HTTP API host. Discovery and authentication still run against the API.
func WithTestGitHost(host string) func(*GitHub) {
	return func(g *GitHub) { g.testGitHost = host }
}

// WithTransport sends every request through rt instead of the default transport.
func WithTransport(rt http.RoundTripper) func(*GitHub) {
	return func(g *GitHub) { g.http.Transport = rt }
}

func NewGitHub(c Credential, options ...func(*GitHub)) *GitHub {
	if c.APIBaseURL == "" {
		c.APIBaseURL = "https://api.github.com"
	}
	c.APIBaseURL = strings.TrimRight(c.APIBaseURL, "/")
	g := &GitHub{cred: c, http: &http.Client{Timeout: requestTimeout}, Settings: DefaultSettings(), now: time.Now}
	for _, option := range options {
		option(g)
	}
	return g
}

// Token is the credential to hand lux for cloning and pushing slug
// ("owner/repo"). With a PAT it is the PAT; with a GitHub App, a token of
// the installation covering slug's owner, good for at least
// tokenRefreshMargin: callers fetch it for each operation and never keep it.
func (g *GitHub) Token(ctx context.Context, slug string) (string, error) {
	switch g.cred.Auth {
	case "pat", "":
		return g.cred.Secret, nil
	case "github_app":
		return g.appToken(ctx, slug)
	}
	return "", &Error{Status: http.StatusUnauthorized, Message: fmt.Sprintf("unknown GitHub authentication %q", g.cred.Auth)}
}

// CheckPushAccess discovers receive-pack without publishing a ref. It does not
// establish permission to change workflow files or bypass branch rules.
func (g *GitHub) CheckPushAccess(ctx context.Context, repository string) error {
	base, err := url.Parse(g.cred.APIBaseURL)
	if err != nil || base.Host == "" || base.User != nil || base.RawQuery != "" || base.Fragment != "" || (base.Scheme != "https" && base.Scheme != "http") {
		return fmt.Errorf("invalid GitHub APIBaseURL for push preflight")
	}
	switch base.Path {
	case "", "/api/v3":
		base.Path = ""
	default:
		return fmt.Errorf("GitHub APIBaseURL must be an origin or end in /api/v3")
	}
	if base.Host == "api.github.com" {
		if base.Scheme != "https" {
			return fmt.Errorf("GitHub requires HTTPS")
		}
		base.Host = "github.com"
	}
	if strings.HasPrefix(repository, "git@") {
		host, path, ok := strings.Cut(strings.TrimPrefix(repository, "git@"), ":")
		if !ok || !strings.EqualFold(host, base.Hostname()) || base.Port() != "" {
			return fmt.Errorf("SSH repository host does not match the configured GitHub origin")
		}
		repository = base.Scheme + "://" + base.Host + "/" + path
	}
	clone, err := url.Parse(repository)
	if err == nil && clone.Scheme == "ssh" && clone.User != nil && clone.User.String() == "git" && strings.EqualFold(clone.Host, base.Host) {
		clone.Scheme, clone.User = base.Scheme, nil
	}
	if err != nil || clone.User != nil || clone.RawQuery != "" || clone.Fragment != "" || clone.Host == "" {
		return fmt.Errorf("invalid repository URL for GitHub push preflight")
	}
	// The local git-daemon harness uses a separate port on the API's host.
	localGit := clone.Scheme == "git" && base.Scheme == "http" && (base.Hostname() == "127.0.0.1" || base.Hostname() == "localhost" || (g.testGitHost != "" && base.Hostname() == g.testGitHost)) && clone.Hostname() == base.Hostname()
	if !localGit && (clone.Scheme != base.Scheme || !strings.EqualFold(clone.Host, base.Host)) {
		return fmt.Errorf("repository origin does not match the configured GitHub origin")
	}
	slug := strings.TrimSuffix(strings.TrimSuffix(strings.TrimPrefix(clone.Path, "/"), "/"), ".git")
	if !regexp.MustCompile(`^[A-Za-z0-9_-]+/[A-Za-z0-9_.-]+$`).MatchString(slug) || strings.HasSuffix(slug, "/.") || strings.HasSuffix(slug, "/..") {
		return fmt.Errorf("invalid GitHub repository path")
	}
	base.Path = "/" + slug + ".git/info/refs"
	base.RawQuery = "service=git-receive-pack"
	token, err := g.Token(ctx, slug)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base.String(), nil)
	if err != nil {
		return err
	}
	req.SetBasicAuth("x-access-token", token)
	client := *g.http
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	res, err := client.Do(req)
	if err != nil {
		return &Unreachable{err}
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusOK {
		if strings.TrimSpace(strings.Split(res.Header.Get("Content-Type"), ";")[0]) != "application/x-git-receive-pack-advertisement" {
			return &Error{Status: http.StatusBadGateway, Message: "Git receive-pack discovery returned no Git advertisement (possibly an authentication gateway)"}
		}
		// Reach EOF for connection reuse, but leave oversized advertisements unread.
		if _, err := io.Copy(io.Discard, io.LimitReader(res.Body, (1<<20)+1)); err != nil {
			return &Unreachable{err}
		}
		return nil
	}
	// Error bodies are diagnostic, not an unbounded Git advertisement.
	data, err := io.ReadAll(io.LimitReader(res.Body, 64<<10))
	if err != nil {
		return &Unreachable{err}
	}
	var response struct {
		Message string `json:"message"`
	}
	_ = json.Unmarshal(data, &response)
	message := response.Message
	if message == "" {
		message = strings.TrimSpace(string(data))
	}
	if message == "" {
		message = "Git receive-pack discovery failed"
	}
	refusal := &Error{Status: res.StatusCode, Message: message}
	markRateLimit(refusal, res.Header, "Git receive-pack discovery rate limit: ")
	if Transient(refusal) {
		refusal.RetryAfter = retryAfter(res.Header, time.Now())
	}
	if !Transient(refusal) && (res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusForbidden) {
		refusal.Message = "Git push access denied: grant Contents: Read and write to the fine-grained PAT for this repository (classic PAT: repo or public_repo), and check repository selection, owner access and organization approval/SSO"
	}
	return refusal
}

// markRateLimit makes a 403 that GitHub marks as a rate limit only by its
// headers (a secondary limit's Retry-After, or no requests remaining)
// Transient, keeping GitHub's message after the prefix.
func markRateLimit(e *Error, h http.Header, prefix string) {
	if e.Status == http.StatusForbidden && (h.Get("Retry-After") != "" || h.Get("X-RateLimit-Remaining") == "0") {
		e.Message = prefix + e.Message
	}
}

func (g *GitHub) do(ctx context.Context, method, path string, body, out any) error {
	return g.doURL(ctx, scopeOf(path), method, g.cred.APIBaseURL+path, body, out)
}

// scopeOf is the account a REST path is about, which picks a GitHub App's
// installation: "owner/repo" under /repos/, an organization under /orgs/,
// else "" (any installation).
func scopeOf(path string) string {
	path, _, _ = strings.Cut(path, "?")
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	switch {
	case len(parts) >= 3 && parts[0] == "repos":
		return parts[1] + "/" + parts[2]
	case len(parts) >= 2 && parts[0] == "orgs":
		return parts[1]
	}
	return ""
}

// doURL makes one request authenticated for scope (see Token).
func (g *GitHub) doURL(ctx context.Context, scope, method, target string, body, out any) error {
	token, err := g.Token(ctx, scope)
	if err != nil {
		return err
	}
	return g.send(ctx, method, target, "Bearer "+token, body, out)
}

// getLinked is do's GET that also answers the response's Link header.
func (g *GitHub) getLinked(ctx context.Context, path string, out any) (string, error) {
	token, err := g.Token(ctx, scopeOf(path))
	if err != nil {
		return "", err
	}
	header, err := g.exchange(ctx, "GET", g.cred.APIBaseURL+path, "Bearer "+token, nil, out)
	if err != nil {
		return "", err
	}
	return header.Get("Link"), nil
}

// lastPage is the page number of a Link header's rel="last" URL, 0 when
// there is none or it does not parse: a positive `page` query parameter of
// at most 100000 (a bound keeping page arithmetic far from overflow).
func lastPage(link string) int {
	for _, part := range strings.Split(link, ",") {
		target, params, ok := strings.Cut(strings.TrimSpace(part), ";")
		target = strings.TrimSpace(target)
		if !ok || !strings.HasPrefix(target, "<") || !strings.HasSuffix(target, ">") {
			continue
		}
		// rel is a space-separated list of relation types, compared, like
		// the parameter name, case-insensitively (RFC 8288).
		last := false
		for _, p := range strings.Split(params, ";") {
			if name, rels, ok := strings.Cut(strings.TrimSpace(p), "="); ok && strings.EqualFold(name, "rel") {
				last = last || slices.ContainsFunc(strings.Fields(strings.Trim(rels, `"`)), func(r string) bool {
					return strings.EqualFold(r, "last")
				})
			}
		}
		if !last {
			continue
		}
		u, err := url.Parse(target[1 : len(target)-1])
		if err != nil {
			return 0
		}
		n, err := strconv.Atoi(u.Query().Get("page"))
		if err != nil || n < 1 || n > 100000 {
			return 0
		}
		return n
	}
	return 0
}

type ghPull struct {
	Number         int     `json:"number"`
	NodeID         string  `json:"node_id"`
	HTMLURL        string  `json:"html_url"`
	Draft          bool    `json:"draft"`
	State          string  `json:"state"`
	MergedAt       *string `json:"merged_at"`
	Mergeable      *bool   `json:"mergeable"`
	MergeableState string  `json:"mergeable_state"`
	Title          string  `json:"title"`
	Head           struct {
		SHA  string  `json:"sha"`
		Ref  string  `json:"ref"`
		Repo *ghRepo `json:"repo"`
	} `json:"head"`
	Base struct {
		Ref  string  `json:"ref"`
		Repo *ghRepo `json:"repo"`
	} `json:"base"`
	RequestedReviewers []ghUser `json:"requested_reviewers"`
	RequestedTeams     []struct {
		Slug string `json:"slug"`
	} `json:"requested_teams"`
	// The pull request's author.
	User *ghUser `json:"user"`
}

type ghUser struct {
	Login     string `json:"login"`
	AvatarURL string `json:"avatar_url"`
	// "User", "Bot" (a GitHub App's account) or "Organization".
	Type string `json:"type"`
}

func (p ghPull) ref() PullRequestRef {
	state := StateOpen
	switch {
	case p.MergedAt != nil:
		state = StateMerged
	case p.State == "closed":
		state = StateClosed
	case p.Draft:
		state = StateDraft
	}
	return PullRequestRef{Number: p.Number, NodeID: p.NodeID, URL: p.HTMLURL, State: state, HeadSHA: p.Head.SHA}
}

type OpenPullRequest struct {
	Slug, Title, Body, Head, Base string
	Draft                         bool
}

// FindPullRequest returns the open pull request from head into base, if
// there is one.
func (g *GitHub) FindPullRequest(ctx context.Context, slug, head, base string) (*PullRequestRef, error) {
	owner, _, _ := strings.Cut(slug, "/")
	var pulls []ghPull
	q := url.Values{"state": {"open"}, "head": {owner + ":" + head}, "base": {base}}
	if err := g.do(ctx, "GET", "/repos/"+slug+"/pulls?"+q.Encode(), nil, &pulls); err != nil {
		return nil, err
	}
	if len(pulls) == 0 {
		return nil, nil
	}
	ref := pulls[0].ref()
	return &ref, nil
}

func (g *GitHub) UsesApp() bool { return g.cred.Auth == "github_app" }

func (g *GitHub) OpenPullRequestAs(ctx context.Context, in OpenPullRequest, token string) (PullRequestRef, error) {
	if token == "" {
		return PullRequestRef{}, OwnerAuthorizationError{}
	}
	creation := NewGitHub(Credential{Auth: "pat", Secret: token, APIBaseURL: g.cred.APIBaseURL})
	creation.http = g.http
	return creation.OpenPullRequest(ctx, in)
}

func (g *GitHub) PullRequestBody(ctx context.Context, slug string, number int) (string, error) {
	var p struct {
		Body string `json:"body"`
	}
	err := g.do(ctx, "GET", fmt.Sprintf("/repos/%s/pulls/%d", slug, number), nil, &p)
	return p.Body, err
}

func (g *GitHub) EditPullRequest(ctx context.Context, slug string, number int, body string) error {
	return g.do(ctx, "PATCH", fmt.Sprintf("/repos/%s/issues/%d", slug, number), map[string]string{"body": body}, nil)
}

func (g *GitHub) OpenPullRequest(ctx context.Context, in OpenPullRequest) (PullRequestRef, error) {
	var p ghPull
	err := g.do(ctx, "POST", "/repos/"+in.Slug+"/pulls", map[string]any{
		"title": in.Title, "body": in.Body, "head": in.Head, "base": in.Base, "draft": in.Draft,
	}, &p)
	return p.ref(), err
}

// PullRequest reads a pull request as a person sees it on GitHub: its
// state, every check by name, each reviewer's verdict, whether it merges
// and how far behind its base it is, and its unresolved review threads.
func (g *GitHub) PullRequest(ctx context.Context, slug string, number int) (Status, error) {
	var p ghPull
	if err := g.do(ctx, "GET", fmt.Sprintf("/repos/%s/pulls/%d", slug, number), nil, &p); err != nil {
		return Status{}, err
	}
	st := Status{PullRequestRef: p.ref()}
	var err error
	if st.Checks, st.CheckList, _, err = g.checks(ctx, slug, p.Head.SHA); err != nil {
		return Status{}, err
	}
	if err := g.readReviews(ctx, slug, number, p, &st); err != nil {
		return Status{}, err
	}
	if st.State != StateOpen && st.State != StateDraft {
		return st, nil
	}
	// Only an open pull request can be behind or in conflict, or have
	// threads left to resolve that anyone is waiting on.
	if p.Base.Ref != "" && p.Head.SHA != "" {
		var cmp struct {
			BehindBy int `json:"behind_by"`
		}
		err := g.do(ctx, "GET", fmt.Sprintf("/repos/%s/compare/%s...%s", slug, url.PathEscape(p.Base.Ref), p.Head.SHA), nil, &cmp)
		var e *Error
		if err != nil && !(asError(err, &e) && e.NotFound()) {
			return Status{}, err
		}
		st.BehindBy = cmp.BehindBy
	}
	st.Mergeable = mergeable(p.Mergeable, p.MergeableState, st.BehindBy)
	if st.UnresolvedThreads, st.ThreadsUnknown, err = g.unresolvedThreads(ctx, slug, number); err != nil {
		return Status{}, err
	}
	return st, nil
}

// Discussion reads a pull request without its checks or mergeability: its
// state, head and reviews, which is what Feedback needs to list what was
// said on it. For a pull request that has ended, read for a mention.
func (g *GitHub) Discussion(ctx context.Context, slug string, number int) (Status, error) {
	var p ghPull
	if err := g.do(ctx, "GET", fmt.Sprintf("/repos/%s/pulls/%d", slug, number), nil, &p); err != nil {
		return Status{}, err
	}
	st := Status{PullRequestRef: p.ref()}
	return st, g.readReviews(ctx, slug, number, p, &st)
}

func (g *GitHub) readReviews(ctx context.Context, slug string, number int, p ghPull, st *Status) error {
	reviews, err := pages[ghReview](ctx, g, fmt.Sprintf("/repos/%s/pulls/%d/reviews", slug, number))
	if err != nil {
		return err
	}
	st.Review, st.Reviews, st.reviews = reviewState(reviews), latestReviews(reviews, p.RequestedReviewers), reviews
	owner, _, _ := strings.Cut(slug, "/")
	for _, t := range p.RequestedTeams {
		st.Reviews = append(st.Reviews, Review{Login: owner + "/" + t.Slug, State: "REQUESTED", Team: true})
	}
	return nil
}

// Mergeability is a pull request's state, head and whether it merges, from
// GET /pulls/{n} alone: no checks, reviews, compare or threads. Behind is
// not told apart from clean.
func (g *GitHub) Mergeability(ctx context.Context, slug string, number int) (PullRequestRef, string, error) {
	var p ghPull
	if err := g.do(ctx, "GET", fmt.Sprintf("/repos/%s/pulls/%d", slug, number), nil, &p); err != nil {
		return PullRequestRef{}, "", err
	}
	return p.ref(), mergeable(p.Mergeable, p.MergeableState, 0), nil
}

// mergeable reads GitHub's two mergeable fields and the distance from the
// base: GitHub computes mergeability lazily, so right after a push it is
// null — unknown, not clean — and the next read (a webhook will prompt one)
// has it.
func mergeable(ok *bool, state string, behindBy int) string {
	switch {
	case state == "dirty" || ok != nil && !*ok:
		return MergeConflicting
	case ok == nil:
		return MergeUnknown
	case behindBy > 0 || state == "behind":
		return MergeBehind
	}
	return MergeClean
}

// checks reads the checks on a commit, both ways CI reports: commit
// statuses and check runs (GitHub Actions, apps). The combined status does
// not include check runs, so both are read, and the worse of the two is the
// rollup.
//
// Every page of check runs: a matrix build has more than one holds, and the
// failing one may be on the last. GitHub Enterprise without Actions has no
// such endpoint (404): no check runs. A token that may not read check runs
// (any fine-grained token, a classic one without repo) is
// refused (403): there may be CI it cannot see, so the checks are never
// read as passing — pending, which holds readiness back without waking a
// fixer — and the list says why with a CheckRunsForbidden entry beside the
// checks it could read. A rate limit is also a 403, and is neither: it
// fails the sync, to be tried again.
func (g *GitHub) checks(ctx context.Context, slug, sha string) (string, []Check, bool, error) {
	var combined struct {
		State      string `json:"state"`
		TotalCount int    `json:"total_count"`
		Statuses   []struct {
			ID        int64  `json:"id"`
			Context   string `json:"context"`
			State     string `json:"state"`
			TargetURL string `json:"target_url"`
			CreatedAt string `json:"created_at"`
			UpdatedAt string `json:"updated_at"`
		} `json:"statuses"`
	}
	if err := g.do(ctx, "GET", fmt.Sprintf("/repos/%s/commits/%s/status", slug, sha), nil, &combined); err != nil {
		return "", nil, false, err
	}
	rollup := checkState(combined.State, combined.TotalCount)
	var list []Check
	for _, s := range combined.Statuses {
		c := Check{Name: s.Context, URL: s.TargetURL, Status: "completed", Conclusion: s.State,
			DurationMs: elapsed(s.CreatedAt, s.UpdatedAt), StatusID: s.ID}
		if s.State == "pending" {
			c.Status, c.Conclusion, c.DurationMs = "in_progress", "", 0
		}
		list = append(list, c)
	}
	// An agent's read stops at 3 pages or 300 checks in all, saying it cut.
	cut := false
pages:
	for page, seen := 1, 0; ; page++ {
		if isAgentRead(ctx) && (page > 3 || seen >= 300) {
			cut = true
			break
		}
		var runs struct {
			TotalCount int `json:"total_count"`
			CheckRuns  []struct {
				ID          int64  `json:"id"`
				Name        string `json:"name"`
				Status      string `json:"status"`
				Conclusion  string `json:"conclusion"`
				HTMLURL     string `json:"html_url"`
				DetailsURL  string `json:"details_url"`
				StartedAt   string `json:"started_at"`
				CompletedAt string `json:"completed_at"`
				App         *struct {
					Slug string `json:"slug"`
				} `json:"app"`
			} `json:"check_runs"`
		}
		err := g.do(ctx, "GET", fmt.Sprintf("/repos/%s/commits/%s/check-runs?per_page=100&page=%d", slug, sha, page), nil, &runs)
		var e *Error
		if err != nil && asError(err, &e) && !Transient(err) && (e.NotFound() || e.Status == 403) {
			if e.Status == 403 {
				rollup = worseChecks(rollup, ChecksPending)
				list = append(list, Check{Name: "GitHub check runs", Status: CheckUnavailable, Diagnostic: CheckRunsForbidden})
			}
			break
		}
		if err != nil {
			return "", nil, false, err
		}
		for _, r := range runs.CheckRuns {
			if isAgentRead(ctx) && len(list) >= 300 {
				cut = true
				break pages
			}
			rollup = worseChecks(rollup, checkRunState(r.Status, r.Conclusion))
			link := r.HTMLURL
			if link == "" {
				link = r.DetailsURL
			}
			c := Check{Name: r.Name, Status: r.Status, Conclusion: r.Conclusion, URL: link,
				DurationMs: elapsed(r.StartedAt, r.CompletedAt), RunID: r.ID}
			if r.App != nil {
				c.App = r.App.Slug
			}
			list = append(list, c)
		}
		seen += len(runs.CheckRuns)
		if len(runs.CheckRuns) == 0 || seen >= runs.TotalCount {
			break
		}
	}
	return rollup, list, cut, nil
}

// elapsed is the milliseconds between two GitHub timestamps, or 0.
func elapsed(from, to string) int64 {
	a, err1 := time.Parse(time.RFC3339, from)
	b, err2 := time.Parse(time.RFC3339, to)
	if err1 != nil || err2 != nil || b.Before(a) {
		return 0
	}
	return b.Sub(a).Milliseconds()
}

// pages reads every page of a list endpoint. GitHub pages lists at 30 by
// default and 100 at most; a pull request with a long review has more.
func pages[T any](ctx context.Context, g *GitHub, path string) ([]T, error) {
	// Bounded, so an endpoint that ignores paging cannot loop forever.
	return pagesUpTo[T](ctx, g, path, 50)
}

// pagesUpTo reads at most most pages of 100 of a list endpoint.
func pagesUpTo[T any](ctx context.Context, g *GitHub, path string, most int) ([]T, error) {
	sep := "?"
	if strings.Contains(path, "?") {
		sep = "&"
	}
	var all []T
	for page := 1; page <= most; page++ {
		var batch []T
		if err := g.do(ctx, "GET", fmt.Sprintf("%s%sper_page=100&page=%d", path, sep, page), nil, &batch); err != nil {
			return nil, err
		}
		all = append(all, batch...)
		if len(batch) < 100 {
			break
		}
	}
	return all, nil
}

// unresolvedThreads counts the pull request's review threads nobody has
// resolved — only GitHub's GraphQL API says. GraphQL answers 200 with
// "errors" for most failures, so those are read too: a rate limit fails
// the sync, to be tried again; anything else (an old Enterprise without
// GraphQL, a token refused it, a repository it cannot see) is unknown —
// never zero, which would let an unread thread count as resolved.
func (g *GitHub) unresolvedThreads(ctx context.Context, slug string, number int) (n int, unknown bool, err error) {
	owner, name, _ := strings.Cut(slug, "/")
	const query = `query($owner:String!,$name:String!,$number:Int!,$after:String){repository(owner:$owner,name:$name){` +
		`pullRequest(number:$number){reviewThreads(first:100,after:$after){nodes{isResolved}pageInfo{hasNextPage endCursor}}}}}`
	var after *string
	for range 20 {
		var data struct {
			Repository *struct {
				PullRequest *struct {
					ReviewThreads struct {
						Nodes []struct {
							IsResolved bool `json:"isResolved"`
						} `json:"nodes"`
						PageInfo struct {
							HasNextPage bool   `json:"hasNextPage"`
							EndCursor   string `json:"endCursor"`
						} `json:"pageInfo"`
					} `json:"reviewThreads"`
				} `json:"pullRequest"`
			} `json:"repository"`
		}
		errs, err := g.graphql(ctx, query, map[string]any{"owner": owner, "name": name, "number": number, "after": after}, &data)
		if err != nil {
			if Transient(err) {
				return 0, false, err
			}
			return 0, true, nil
		}
		if len(errs) > 0 || data.Repository == nil || data.Repository.PullRequest == nil {
			return 0, true, nil
		}
		threads := data.Repository.PullRequest.ReviewThreads
		for _, t := range threads.Nodes {
			if !t.IsResolved {
				n++
			}
		}
		if !threads.PageInfo.HasNextPage {
			return n, false, nil
		}
		c := threads.PageInfo.EndCursor
		after = &c
	}
	// More threads than it reads: at least n, which holds readiness back
	// if any is open, as all of them would.
	return n, false, nil
}

// gqlError is one of the errors GraphQL answers 200 with.
type gqlError struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

// graphql sends a query and decodes its data into `data`. GraphQL answers
// most failures with 200 and "errors": a rate limit among them is an
// error, GitHub's to say again later; the rest are returned for the caller
// to judge, beside whatever data came with them.
func (g *GitHub) graphql(ctx context.Context, query string, vars map[string]any, data any) ([]gqlError, error) {
	scope, _ := vars["owner"].(string)
	if name, ok := vars["name"].(string); ok && scope != "" {
		scope += "/" + name
	}
	return g.graphqlIn(ctx, scope, query, vars, data)
}

// graphqlIn is graphql authenticated for scope (owner/name), for a query
// whose variables do not name the repository.
func (g *GitHub) graphqlIn(ctx context.Context, scope, query string, vars map[string]any, data any) ([]gqlError, error) {
	var out struct {
		Data   json.RawMessage `json:"data"`
		Errors []gqlError      `json:"errors"`
	}
	if err := g.doURL(ctx, scope, "POST", g.graphqlURL(), map[string]any{"query": query, "variables": vars}, &out); err != nil {
		return nil, err
	}
	for _, e := range out.Errors {
		if e.Type == "RATE_LIMITED" {
			return nil, &Error{Status: 429, Message: "GraphQL: " + e.Message}
		}
	}
	if len(out.Data) > 0 && string(out.Data) != "null" {
		if err := json.Unmarshal(out.Data, data); err != nil {
			return nil, err
		}
	}
	return out.Errors, nil
}

// graphqlURL: github.com's GraphQL is beside its REST root; Enterprise's
// REST is under /api/v3 and its GraphQL at /api/graphql.
func (g *GitHub) graphqlURL() string {
	if base, ok := strings.CutSuffix(g.cred.APIBaseURL, "/api/v3"); ok {
		return base + "/api/graphql"
	}
	return g.cred.APIBaseURL + "/graphql"
}

// checkRunState reads one check run: still running is pending; finished,
// its conclusion decides. Neutral and skipped runs block nothing. One a
// person cancelled, or waiting on a person's approval, or superseded, is
// not a failure an agent could fix: not passing, but no fixer either.
func checkRunState(status, conclusion string) string {
	if status != "completed" {
		return ChecksPending
	}
	switch conclusion {
	case "success", "neutral", "skipped":
		return ChecksPassing
	case "cancelled", "action_required", "stale":
		return ChecksPending
	}
	return ChecksFailing
}

// worseChecks combines two readings: failing over pending over passing
// over unknown (no CI at all).
var checksRank = map[string]int{ChecksUnknown: 0, ChecksPassing: 1, ChecksPending: 2, ChecksFailing: 3}

func worseChecks(a, b string) string {
	if checksRank[b] > checksRank[a] {
		return b
	}
	return a
}

type ghComment struct {
	ID        int64   `json:"id"`
	Body      string  `json:"body"`
	Path      string  `json:"path"`
	CreatedAt string  `json:"created_at"`
	UpdatedAt string  `json:"updated_at"`
	HTMLURL   string  `json:"html_url"`
	User      *ghUser `json:"user"`
}

type ghReview struct {
	ID          int64   `json:"id"`
	Body        string  `json:"body"`
	State       string  `json:"state"`
	SubmittedAt *string `json:"submitted_at"`
	HTMLURL     string  `json:"html_url"`
	User        *ghUser `json:"user"`
	// The head the review was submitted on.
	CommitID string `json:"commit_id"`
}

func login(u *ghUser) string {
	if u == nil {
		return "unknown"
	}
	return u.Login
}

// Feedback lists what people left on a pull request since `since`, oldest
// first: conversation comments, line comments, and the text of every
// review — one that requests changes, and one that approves or comments
// ("approved, but rename this" asks for something too), from the reviews
// PullRequest already listed. Every page of each.
// Inclusive of `since` on purpose: GitHub timestamps are whole seconds, so a
// strict comparison drops a comment posted in the same second as the last
// one seen. Callers dedupe by id, which is exact.
func (g *GitHub) Feedback(ctx context.Context, slug string, st Status, since string) ([]Feedback, error) {
	number := st.Number
	q := ""
	if since != "" {
		q = "?since=" + url.QueryEscape(since)
	}
	issue, err := pages[ghComment](ctx, g, fmt.Sprintf("/repos/%s/issues/%d/comments%s", slug, number, q))
	if err != nil {
		return nil, err
	}
	line, err := pages[ghComment](ctx, g, fmt.Sprintf("/repos/%s/pulls/%d/comments%s", slug, number, q))
	if err != nil {
		return nil, err
	}
	reviews := st.reviews
	var out []Feedback
	for _, c := range issue {
		out = append(out, Feedback{ID: fmt.Sprintf("issue-comment-%d", c.ID), Author: login(c.User), Body: c.Body, CreatedAt: c.CreatedAt,
			UpdatedAt: c.UpdatedAt, Kind: KindComment, URL: c.HTMLURL})
	}
	for _, c := range line {
		out = append(out, Feedback{ID: fmt.Sprintf("line-comment-%d", c.ID), Author: login(c.User), Body: c.Body, Path: c.Path,
			CreatedAt: c.CreatedAt, UpdatedAt: c.UpdatedAt, Kind: KindLineComment, URL: c.HTMLURL})
	}
	for _, r := range reviews {
		// A reviewer who writes only "please rename this" in the review box
		// would otherwise be invisible. An empty body says nothing: the
		// verdict is in Status.
		if r.Body == "" || r.SubmittedAt == nil {
			continue
		}
		kind := KindReview
		if r.State == "CHANGES_REQUESTED" {
			kind = KindChangesRequested
		}
		out = append(out, Feedback{ID: fmt.Sprintf("review-%d", r.ID), Author: login(r.User), Body: r.Body, CreatedAt: *r.SubmittedAt,
			Kind: kind, URL: r.HTMLURL})
	}
	// GitHub's `since` is by last update: a comment edited since is listed
	// with its current words, however long ago it was made.
	filtered := out[:0]
	for _, f := range out {
		if since == "" || f.CreatedAt >= since || f.UpdatedAt >= since {
			filtered = append(filtered, f)
		}
	}
	sort.SliceStable(filtered, func(i, j int) bool { return filtered[i].CreatedAt < filtered[j].CreatedAt })
	return filtered, nil
}

// Head is a pull request's head commit as GitHub has it now: where a fix
// must start, whoever moved it last.
func (g *GitHub) Head(ctx context.Context, slug string, number int) (string, error) {
	var p ghPull
	if err := g.do(ctx, "GET", fmt.Sprintf("/repos/%s/pulls/%d", slug, number), nil, &p); err != nil {
		return "", err
	}
	return p.Head.SHA, nil
}

// CommitProvenance is the identity GitHub verified for a commit.
type CommitProvenance struct {
	Login    string
	Verified bool
}

// CommitProvenance reads the commit identity and signature verification GitHub
// returned for sha. Web-flow is GitHub's identity for merges made through the
// web/API merge flow.
func (g *GitHub) CommitProvenance(ctx context.Context, slug, sha string) (CommitProvenance, error) {
	var c struct {
		Committer *struct {
			Login string `json:"login"`
		} `json:"committer"`
		Commit struct {
			Verification struct {
				Verified bool `json:"verified"`
			} `json:"verification"`
		} `json:"commit"`
	}
	if err := g.do(ctx, "GET", fmt.Sprintf("/repos/%s/commits/%s", slug, sha), nil, &c); err != nil {
		return CommitProvenance{}, err
	}
	var login string
	if c.Committer != nil {
		login = c.Committer.Login
	}
	return CommitProvenance{Login: login, Verified: c.Commit.Verification.Verified}, nil
}

// CommitAuthor is who made a commit, by GitHub login when GitHub knows
// the address, else by the name in the commit.
func (g *GitHub) CommitAuthor(ctx context.Context, slug, sha string) (string, error) {
	var c struct {
		Author *struct {
			Login string `json:"login"`
		} `json:"author"`
		Commit struct {
			Author struct {
				Name string `json:"name"`
			} `json:"author"`
		} `json:"commit"`
	}
	if err := g.do(ctx, "GET", fmt.Sprintf("/repos/%s/commits/%s", slug, sha), nil, &c); err != nil {
		return "", err
	}
	if c.Author != nil && c.Author.Login != "" {
		return c.Author.Login, nil
	}
	return c.Commit.Author.Name, nil
}

// Permission is what a login may do in a repository, as GitHub's
// collaborator permission endpoint says: admin, maintain, write, triage,
// read, or none for someone who is not a collaborator at all.
func (g *GitHub) Permission(ctx context.Context, slug, login string) (string, error) {
	var out struct {
		Permission string `json:"permission"`
		RoleName   string `json:"role_name"`
	}
	err := g.do(ctx, "GET", fmt.Sprintf("/repos/%s/collaborators/%s/permission", slug, url.PathEscape(login)), nil, &out)
	var e *Error
	if asError(err, &e) && e.NotFound() {
		return "none", nil
	}
	if err != nil {
		return "", err
	}
	// role_name is the finer answer (maintain, triage); permission folds
	// those into write and read.
	if out.RoleName != "" {
		return out.RoleName, nil
	}
	return out.Permission, nil
}

// UserID is a GitHub account's numeric id, by login: what its noreply
// address is made of. An unknown login is a 404 *Error.
func (g *GitHub) UserID(ctx context.Context, login string) (int64, error) {
	var u struct {
		ID int64 `json:"id"`
	}
	if err := g.do(ctx, "GET", "/users/"+url.PathEscape(login), nil, &u); err != nil {
		return 0, err
	}
	return u.ID, nil
}

var logins sync.Map // sessionKey → login

// Login is the account the credential's comments are posted as: a GitHub
// App's bot ("<slug>[bot]"), else the PAT's owner from GET /user. Read once
// per credential and process; a failed read is not kept.
func (g *GitHub) Login(ctx context.Context) (string, error) {
	key := sessionKey(g.cred)
	if l, ok := logins.Load(key); ok {
		return l.(string), nil
	}
	var login string
	if g.IsApp() {
		bot, err := g.Bot(ctx)
		if err != nil {
			return "", err
		}
		login = bot.Name
	} else {
		var u struct {
			Login string `json:"login"`
		}
		if err := g.do(ctx, "GET", "/user", nil, &u); err != nil {
			return "", err
		}
		login = u.Login
	}
	if login == "" {
		return "", &Error{Status: http.StatusBadGateway, Message: "GitHub named no login for the credential"}
	}
	logins.Store(key, login)
	return login, nil
}

// CanWrite says whether a permission lets its holder push: what "a
// collaborator with write access" means.
func CanWrite(permission string) bool {
	switch permission {
	case "admin", "maintain", "write":
		return true
	}
	return false
}

// Member says whether a login is a member of a GitHub organization. A user
// account (not an organization) has no members: only its owner counts.
func (g *GitHub) Member(ctx context.Context, org, login string) (bool, error) {
	if strings.EqualFold(org, login) {
		return true, nil
	}
	err := g.do(ctx, "GET", fmt.Sprintf("/orgs/%s/members/%s", url.PathEscape(org), url.PathEscape(login)), nil, nil)
	var e *Error
	if asError(err, &e) && (e.NotFound() || e.Status == 302) {
		return false, nil
	}
	return err == nil, err
}

// UpdateBranch merges the base into a pull request's branch on GitHub, as
// its "Update branch" button does. expectedHead guards against updating a
// head someone moved meanwhile; GitHub refuses (422) a branch that
// conflicts.
func (g *GitHub) UpdateBranch(ctx context.Context, slug string, number int, expectedHead string) error {
	body := map[string]any{}
	if expectedHead != "" {
		body["expected_head_sha"] = expectedHead
	}
	return g.do(ctx, "PUT", fmt.Sprintf("/repos/%s/pulls/%d/update-branch", slug, number), body, nil)
}

// MergeBase merges head, an exact commit, into branch on GitHub without a
// pull request: the merge commit's first parent is branch's head, its
// second head. "" with no error when branch already contains head (204);
// GitHub refuses a conflict with 409.
func (g *GitHub) MergeBase(ctx context.Context, slug, branch, head string) (string, error) {
	m, err := g.MergeCommit(ctx, slug, branch, head)
	return m.SHA, err
}

// Merged is a merge commit GitHub made, with its parents, first parent
// first.
type Merged struct {
	SHA     string
	Parents []string
}

// MergeCommit is MergeBase with the merge's parents, as GitHub answers
// them: the first is the head branch was at when GitHub merged.
func (g *GitHub) MergeCommit(ctx context.Context, slug, branch, head string) (Merged, error) {
	var out struct {
		SHA     string `json:"sha"`
		Parents []struct {
			SHA string `json:"sha"`
		} `json:"parents"`
	}
	err := g.do(ctx, "POST", "/repos/"+slug+"/merges", map[string]string{"base": branch, "head": head}, &out)
	m := Merged{SHA: out.SHA}
	for _, p := range out.Parents {
		m.Parents = append(m.Parents, p.SHA)
	}
	return m, err
}

// ClosePullRequest closes a pull request without merging it. One already
// closed or merged is left as it is.
func (g *GitHub) ClosePullRequest(ctx context.Context, slug string, number int) error {
	return g.do(ctx, "PATCH", fmt.Sprintf("/repos/%s/pulls/%d", slug, number), map[string]any{"state": "closed"}, nil)
}

// Merge methods, as GitHub names them.
var MergeMethods = []string{"squash", "merge", "rebase"}

// Merge merges a pull request by the given method. sha guards against
// merging a head nobody looked at: GitHub refuses (409) when it moved.
func (g *GitHub) Merge(ctx context.Context, slug string, number int, method, sha string) (string, error) {
	body := map[string]any{"merge_method": method}
	if sha != "" {
		body["sha"] = sha
	}
	var out struct {
		SHA string `json:"sha"`
	}
	err := g.do(ctx, "PUT", fmt.Sprintf("/repos/%s/pulls/%d/merge", slug, number), body, &out)
	return out.SHA, err
}

// RerunFailed asks GitHub to run each failed check run again. An Actions
// check run is a job, re-run through the Actions API (its id is the
// job's). Any other app's is re-requested through the checks API, which
// GitHub allows only the app that owns it: with a token, GitHub refuses,
// and says so.
func (g *GitHub) RerunFailed(ctx context.Context, slug string, checks []Check) (int, error) {
	n := 0
	for _, c := range checks {
		if !c.Failed() || c.RunID == 0 {
			continue
		}
		path := fmt.Sprintf("/repos/%s/check-runs/%d/rerequest", slug, c.RunID)
		if c.App == ActionsApp {
			path = fmt.Sprintf("/repos/%s/actions/jobs/%d/rerun", slug, c.RunID)
		}
		if err := g.do(ctx, "POST", path, map[string]any{}, nil); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// RequestReviewers asks people, and teams named "org/slug", for a review
// on a pull request.
func (g *GitHub) RequestReviewers(ctx context.Context, slug string, number int, logins []string) error {
	users, teams := reviewerSlugs(slug, logins)
	return g.do(ctx, "POST", fmt.Sprintf("/repos/%s/pulls/%d/requested_reviewers", slug, number),
		map[string]any{"reviewers": users, "team_reviewers": teams}, nil)
}

// truncate keeps the end of a log, where the failure usually is.
func truncate(s string, limit int) string {
	if limit <= 0 || len(s) <= limit {
		return s
	}
	cut := s[len(s)-limit:]
	if i := strings.IndexByte(cut, '\n'); i >= 0 && i < len(cut)-1 {
		cut = cut[i+1:]
	}
	return "…\n" + cut
}

// ChangedFiles lists the paths that differ between two commits. What a phase
// changed decides which reviewers run and which findings a fix retired.
func (g *GitHub) ChangedFiles(ctx context.Context, slug, base, head string) ([]string, error) {
	var cmp struct {
		Files []struct {
			Filename string `json:"filename"`
		} `json:"files"`
	}
	if err := g.do(ctx, "GET", fmt.Sprintf("/repos/%s/compare/%s...%s", slug, base, head), nil, &cmp); err != nil {
		return nil, err
	}
	paths := make([]string, 0, len(cmp.Files))
	for _, f := range cmp.Files {
		paths = append(paths, f.Filename)
	}
	return paths, nil
}

// Comparison is head against base as GitHub's compare says it: the
// commits head has that base lacks (AheadBy) and the reverse (BehindBy:
// base is an ancestor of head only at 0), each changed file's lines, and
// the merge base the files are diffed from.
type Comparison struct {
	AheadBy  int            `json:"ahead_by"`
	BehindBy int            `json:"behind_by"`
	Files    []ComparedFile `json:"files"`
	// GitHub sends a commit object here, not a sha.
	MergeBase struct {
		SHA string `json:"sha"`
	} `json:"merge_base_commit"`
}

// CommitComparison is the ancestry-bearing form used by phase advancement.
type CommitComparison struct {
	Comparison
	Commits      []ComparedCommit `json:"commits"`
	TotalCommits int              `json:"total_commits"`
}

// ComparedCommit is one commit of a comparison, with its parents, first
// parent first.
type ComparedCommit struct {
	SHA     string `json:"sha"`
	Parents []struct {
		SHA string `json:"sha"`
	} `json:"parents"`
}

// CompareCommitCap is the most commits GitHub's compare lists.
const CompareCommitCap = 250

// ComparedFile is one file of a Comparison; PreviousFilename is a rename's
// or copy's other side.
type ComparedFile struct {
	Filename         string `json:"filename"`
	PreviousFilename string `json:"previous_filename,omitempty"`
	// added, removed, modified, renamed, copied, changed or unchanged.
	Status    string `json:"status"`
	Additions int    `json:"additions"`
	Deletions int    `json:"deletions"`
	// The file's blob.
	SHA string `json:"sha"`
	// The diff's text; GitHub omits it for a binary file, a change of no
	// lines, and a diff too large to show.
	Patch string `json:"patch,omitempty"`
}

// CompareFileCap is the most files GitHub's compare lists: at it, the list
// may be cut short.
const CompareFileCap = 300

// Truncated says the comparison may list fewer files than changed.
func (c Comparison) Truncated() bool { return len(c.Files) >= CompareFileCap }

// Compare compares head with base, two commits.
func (g *GitHub) Compare(ctx context.Context, slug, base, head string) (Comparison, error) {
	var cmp Comparison
	err := g.do(ctx, "GET", fmt.Sprintf("/repos/%s/compare/%s...%s", slug, base, head), nil, &cmp)
	return cmp, err
}

// FileAt is path's content at ref (a commit), read raw from GitHub's
// contents API. Content over limit bytes is an *Error with Refusal
// RefusalTooLarge, read no further; a path absent at ref is GitHub's 404.
// A redirect is followed as JobLogTail follows one, without the token
// unless it stays on the same host.
func (g *GitHub) FileAt(ctx context.Context, slug, path, ref string, limit int64) ([]byte, error) {
	segments := strings.Split(path, "/")
	for i, s := range segments {
		segments[i] = url.PathEscape(s)
	}
	target := fmt.Sprintf("%s/repos/%s/contents/%s?ref=%s", g.cred.APIBaseURL, slug, strings.Join(segments, "/"), url.QueryEscape(ref))
	token, err := g.Token(ctx, slug)
	if err != nil {
		return nil, err
	}
	authorization := "Bearer " + token
	if isAgentRead(ctx) {
		if err := reserveAgentRequest(target, authorization, "core"); err != nil {
			return nil, err
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", authorization)
	req.Header.Set("Accept", "application/vnd.github.raw")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	client := *g.http
	client.CheckRedirect = func(next *http.Request, via []*http.Request) error {
		if len(via) >= 3 {
			return errors.New("too many redirects reading a file's content")
		}
		if next.Response != nil {
			noteRate(target, authorization, next.Response.Header)
		}
		// A renamed repository's 301 stays on GitHub's host and needs the token.
		if next.URL.Host != via[0].URL.Host {
			next.Header.Del("Authorization")
		}
		if next.URL.User != nil || (via[0].URL.Scheme == "https" && next.URL.Scheme != "https") {
			return errors.New("unsafe redirect reading a file's content")
		}
		return nil
	}
	res, err := client.Do(req)
	if err != nil {
		return nil, &Unreachable{err}
	}
	defer res.Body.Close()
	if res.Request != nil && res.Request.URL.Host == req.URL.Host {
		noteRate(target, authorization, res.Header)
	}
	if res.StatusCode >= 300 {
		data, _ := io.ReadAll(io.LimitReader(res.Body, 64<<10))
		var e struct {
			Message string `json:"message"`
		}
		_ = json.Unmarshal(data, &e)
		if e.Message == "" {
			e.Message = fmt.Sprintf("GET contents of %s at %s failed", path, ref)
		}
		refusal := &Error{Status: res.StatusCode, Message: e.Message}
		markRateLimit(refusal, res.Header, "rate limit: ")
		if Transient(refusal) {
			refusal.RetryAfter = retryAfter(res.Header, time.Now())
		}
		return nil, refusal
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, limit+1))
	if err != nil {
		return nil, &Unreachable{err}
	}
	if int64(len(data)) > limit {
		return nil, &Error{Status: http.StatusRequestEntityTooLarge, Refusal: RefusalTooLarge,
			Message: fmt.Sprintf("%s at %s is over %d bytes", path, ref, limit)}
	}
	return data, nil
}

// CompareCommits returns the ancestry-bearing compare response for phase
// advancement. Ordinary callers use Compare and do not materialize commits.
func (g *GitHub) CompareCommits(ctx context.Context, slug, base, head string) (CommitComparison, error) {
	var cmp CommitComparison
	err := g.do(ctx, "GET", fmt.Sprintf("/repos/%s/compare/%s...%s", slug, base, head), nil, &cmp)
	return cmp, err
}

// Never forced: GitHub refuses the update unless sha descends from the
// branch's current commit, so a person who pushed to the branch in the
// meantime is never overwritten. That refusal is reported as an error.
func (g *GitHub) FastForward(ctx context.Context, slug, branch, sha string) error {
	err := g.FastForwardExisting(ctx, slug, branch, sha)
	var e *Error
	if asError(err, &e) && (e.NotFound() || e.Status == 422 && strings.Contains(e.Message, "does not exist")) {
		return g.CreateBranch(ctx, slug, branch, sha)
	}
	return err
}

// CreateBranch creates branch at sha, never moving one that exists: GitHub
// answers 422 "Reference already exists" for it.
func (g *GitHub) CreateBranch(ctx context.Context, slug, branch, sha string) error {
	return g.do(ctx, "POST", "/repos/"+slug+"/git/refs", map[string]any{"ref": "refs/heads/" + branch, "sha": sha}, nil)
}

// FastForwardExisting never recreates a branch deleted before the update.
func (g *GitHub) FastForwardExisting(ctx context.Context, slug, branch, sha string) error {
	return g.do(ctx, "PATCH", "/repos/"+slug+"/git/refs/heads/"+branch, map[string]any{"sha": sha, "force": false}, nil)
}

// BranchSHA is the commit a branch is at, "" when it does not exist.
func (g *GitHub) BranchSHA(ctx context.Context, slug, branch string) (string, error) {
	var ref struct {
		Object struct {
			SHA string `json:"sha"`
		} `json:"object"`
	}
	err := g.do(ctx, "GET", "/repos/"+slug+"/git/ref/heads/"+branch, nil, &ref)
	var e *Error
	if asError(err, &e) && e.NotFound() {
		return "", nil
	}
	return ref.Object.SHA, err
}

// DeleteBranch removes a branch; one already gone is not an error.
func (g *GitHub) DeleteBranch(ctx context.Context, slug, branch string) error {
	err := g.do(ctx, "DELETE", "/repos/"+slug+"/git/refs/heads/"+branch, nil, nil)
	var e *Error
	if asError(err, &e) && (e.NotFound() || e.Status == 422) {
		return nil
	}
	return err
}

// EnsureWebhook registers a repository webhook delivering the pull request
// events dude acts on, and returns its id. A hook already there for the same
// URL is updated rather than duplicated: its secret may have been rotated.
func (g *GitHub) EnsureWebhook(ctx context.Context, slug, target, secret string) (string, error) {
	var hooks []struct {
		ID     int64 `json:"id"`
		Config struct {
			URL string `json:"url"`
		} `json:"config"`
	}
	if err := g.do(ctx, "GET", "/repos/"+slug+"/hooks", nil, &hooks); err != nil {
		return "", err
	}
	hook := map[string]any{
		"active": true,
		"events": WebhookEvents,
		"config": map[string]any{"url": target, "content_type": "json", "secret": secret, "insecure_ssl": "0"},
	}
	for _, h := range hooks {
		if h.Config.URL == target {
			return fmt.Sprint(h.ID), g.do(ctx, "PATCH", fmt.Sprintf("/repos/%s/hooks/%d", slug, h.ID), hook, nil)
		}
	}
	hook["name"] = "web"
	var created struct {
		ID int64 `json:"id"`
	}
	if err := g.do(ctx, "POST", "/repos/"+slug+"/hooks", hook, &created); err != nil {
		return "", err
	}
	return fmt.Sprint(created.ID), nil
}

// HookEvents is the events of the repository's webhook delivering to
// target; nil when it has none.
func (g *GitHub) HookEvents(ctx context.Context, slug, target string) ([]string, error) {
	var hooks []struct {
		Events []string `json:"events"`
		Config struct {
			URL string `json:"url"`
		} `json:"config"`
	}
	if err := g.do(ctx, "GET", "/repos/"+slug+"/hooks", nil, &hooks); err != nil {
		return nil, err
	}
	for _, h := range hooks {
		if h.Config.URL == target {
			return h.Events, nil
		}
	}
	return nil, nil
}

// WebhookEvents are the events that can change what dude does about a PR.
var WebhookEvents = []string{
	"pull_request", "pull_request_review", "pull_request_review_comment",
	// A thread resolved or reopened: whether it is ready to merge.
	"pull_request_review_thread",
	"issue_comment", "check_suite", "check_run", "status",
}

func checkState(state string, total int) string {
	if total == 0 {
		// A repository with no CI reports unknown, not passing.
		return ChecksUnknown
	}
	switch state {
	case "success":
		return ChecksPassing
	case "failure", "error":
		return ChecksFailing
	}
	return ChecksPending
}

// reviewState: the latest verdict per person decides, because a reviewer
// who requested changes and then approved has approved.
func reviewState(reviews []ghReview) string {
	latest := map[string]string{}
	for _, r := range reviews {
		// COMMENTED reviews carry no verdict; counting them would let a
		// question overwrite an approval.
		if r.User == nil || r.State == "COMMENTED" {
			continue
		}
		latest[r.User.Login] = r.State
	}
	approved := false
	for _, v := range latest {
		if v == "CHANGES_REQUESTED" {
			return ReviewChangesRequested
		}
		approved = approved || v == "APPROVED"
	}
	if approved {
		return ReviewApproved
	}
	return ReviewPending
}

// latestReviews is each reviewer's latest word, oldest first, and those
// asked for a review who have not given one yet. A COMMENTED review does
// not replace a verdict: a question after an approval is still an approval.
// One asked again after reviewing keeps their word, marked Rerequested.
func latestReviews(reviews []ghReview, requested []ghUser) []Review {
	var order []string
	latest := map[string]Review{}
	for _, r := range reviews {
		if r.User == nil || r.State == "PENDING" {
			continue
		}
		prior, seen := latest[r.User.Login]
		if !seen {
			order = append(order, r.User.Login)
		}
		if r.State == "COMMENTED" && seen && prior.State != "COMMENTED" {
			continue
		}
		latest[r.User.Login] = Review{Login: r.User.Login, State: r.State, SubmittedAt: r.SubmittedAt, AvatarURL: r.User.AvatarURL}
	}
	// Asked again after reviewing: GitHub lists them as requested once more.
	asked := map[string]bool{}
	for _, r := range requested {
		asked[r.Login] = true
	}
	out := make([]Review, 0, len(order)+len(requested))
	for _, l := range order {
		r := latest[l]
		r.Rerequested = asked[l]
		out = append(out, r)
	}
	for _, r := range requested {
		if _, seen := latest[r.Login]; !seen {
			out = append(out, Review{Login: r.Login, State: "REQUESTED", AvatarURL: r.AvatarURL})
		}
	}
	return out
}

func asError(err error, target **Error) bool {
	e, ok := err.(*Error)
	if ok {
		*target = e
	}
	return ok
}

var slugPattern = regexp.MustCompile(`[:/]([^/:]+)/([^/]+?)(?:\.git)?/?$`)
var remotePattern = regexp.MustCompile(`^(?:https?://|ssh://|git://|[^@/\s]+@[^:/\s]+:)`)

// SlugFromURL is `owner/repo` from a clone URL, or "" for anything that is
// not a remote. A local path is a legitimate repository URL, and deriving a
// plausible slug from one would send a PR to a repository that does not exist.
func SlugFromURL(u string) string {
	if !remotePattern.MatchString(u) {
		return ""
	}
	m := slugPattern.FindStringSubmatch(u)
	if m == nil {
		return ""
	}
	return m[1] + "/" + m[2]
}

// ---- Reads for agents: issues and pull requests, as anyone can write them --

// What these return is what people (anyone who may comment) wrote on
// GitHub: titles, bodies, comments, review text, diffs. Callers hand it to
// an agent only marked as someone else's words.

// IssueLine is an issue or pull request as a search lists it.
type IssueLine struct {
	Number      int
	Title       string
	Author      string
	State       string // open | closed; a pull request's also draft | merged
	Labels      []string
	UpdatedAt   string
	URL         string
	PullRequest bool
}

// IssueComment is one comment: on an issue's conversation, or in a review
// thread.
type IssueComment struct {
	Author, Body, CreatedAt string
}

// IssueRead is an issue (or a pull request's conversation) with its
// comments, oldest first, at most the number asked for; CommentCount is how
// many it has.
type IssueRead struct {
	IssueLine
	Body         string
	Comments     []IssueComment
	CommentCount int
}

// ReviewRead is one submitted review: its verdict and the text in its box.
type ReviewRead struct {
	Author, State, Body, SubmittedAt string
}

// ThreadRead is an unresolved review thread: where it is and what was said
// in it, oldest first; CommentCount is how many comments it has.
type ThreadRead struct {
	Path         string
	Line         int
	Outdated     bool
	Comments     []IssueComment
	CommentCount int
}

// FileRead is one file a pull request changes, with its unified diff hunks
// as GitHub gives them (none for a binary or a very large file).
type FileRead struct {
	Path                 string
	Status               string
	Additions, Deletions int
	Patch                string
}

// PullRequestRead is a pull request as a reader on GitHub sees it.
type PullRequestRead struct {
	IssueLine
	Body       string
	Draft      bool
	Head, Base string
	Reviews    []ReviewRead
	ReviewsCut bool
	// Only the threads nobody resolved; ThreadsUnknown when GitHub would
	// not say (see unresolvedThreads).
	Threads        []ThreadRead
	ThreadsUnknown bool
	Checks         []Check
	// ChecksCut: the agent read's cap left checks out.
	ChecksCut bool
	Files     []FileRead
	// The conversation's newest comments, oldest first (see
	// PullRequestRead); CommentCount is how many it has.
	Comments     []IssueComment
	CommentCount int
}

type ghLabel struct {
	Name string `json:"name"`
}

type ghFile struct {
	Filename  string `json:"filename"`
	Status    string `json:"status"`
	Additions int    `json:"additions"`
	Deletions int    `json:"deletions"`
	Patch     string `json:"patch"`
}

type ghIssue struct {
	Number        int       `json:"number"`
	Title         string    `json:"title"`
	Body          string    `json:"body"`
	State         string    `json:"state"`
	HTMLURL       string    `json:"html_url"`
	UpdatedAt     string    `json:"updated_at"`
	User          *ghUser   `json:"user"`
	Labels        []ghLabel `json:"labels"`
	Comments      int       `json:"comments"`
	RepositoryURL string    `json:"repository_url"`
	PullRequest   *struct {
		MergedAt *string `json:"merged_at"`
	} `json:"pull_request"`
	Draft bool `json:"draft"`
}

func labelNames(ls []ghLabel) []string {
	out := make([]string, 0, len(ls))
	for _, l := range ls {
		out = append(out, l.Name)
	}
	return out
}

func (i ghIssue) line() IssueLine {
	state := i.State
	if i.PullRequest != nil && i.PullRequest.MergedAt != nil {
		state = StateMerged
	} else if i.PullRequest != nil && i.Draft && state == StateOpen {
		state = StateDraft
	}
	return IssueLine{Number: i.Number, Title: i.Title, Author: login(i.User), State: state, Labels: labelNames(i.Labels),
		UpdatedAt: i.UpdatedAt, URL: i.HTMLURL, PullRequest: i.PullRequest != nil}
}

type agentReadKey struct{}

func agentReadContext(ctx context.Context) context.Context {
	return context.WithValue(ctx, agentReadKey{}, true)
}

func isAgentRead(ctx context.Context) bool {
	return ctx.Value(agentReadKey{}) == true
}

// SearchIssues lists slug's issues or pull requests (kind "issue" or "pr")
// matching words, in state open, closed or "" for both, most recently
// updated first, at most limit; and how many match in all. GitHub's search
// ORs repo: qualifiers, so words naming another repository would widen it:
// an item of any other repository is dropped.
func (g *GitHub) SearchIssues(ctx context.Context, slug, words, kind, state string, limit int) ([]IssueLine, int, error) {
	ctx = agentReadContext(ctx)
	if err := g.readBudget(ctx, slug, "core", "search"); err != nil {
		return nil, 0, err
	}
	var repo struct {
		FullName string `json:"full_name"`
	}
	if err := g.do(ctx, "GET", "/repos/"+slug, nil, &repo); err != nil {
		return nil, 0, err
	}
	if repo.FullName == "" {
		return nil, 0, &Error{Status: http.StatusBadGateway, Message: "GitHub did not identify the repository"}
	}
	canonical := repo.FullName
	q := "repo:" + canonical + " is:" + kind
	if state == "open" || state == "closed" {
		q += " is:" + state
	}
	if w := strings.TrimSpace(words); w != "" {
		q += " " + w
	}
	v := url.Values{"q": {q}, "sort": {"updated"}, "order": {"desc"}, "per_page": {strconv.Itoa(limit)}}
	var out struct {
		TotalCount int       `json:"total_count"`
		Items      []ghIssue `json:"items"`
	}
	if err := g.doURL(ctx, slug, "GET", g.cred.APIBaseURL+"/search/issues?"+v.Encode(), nil, &out); err != nil {
		return nil, 0, err
	}
	lines := []IssueLine{}
	for _, it := range out.Items {
		if !strings.HasSuffix(strings.ToLower(it.RepositoryURL), "/repos/"+strings.ToLower(canonical)) {
			continue
		}
		if len(lines) < limit {
			lines = append(lines, it.line())
		}
	}
	return lines, out.TotalCount, nil
}

// Issue reads an issue, or a pull request's conversation, with its first
// maxComments comments.
func (g *GitHub) Issue(ctx context.Context, slug string, number, maxComments int) (_ IssueRead, err error) {
	ctx = agentReadContext(ctx)
	defer func() { err = g.itemMoved(ctx, slug, err) }()
	if err := g.readBudget(ctx, slug, "core"); err != nil {
		return IssueRead{}, err
	}
	var i ghIssue
	if err := g.do(ctx, "GET", fmt.Sprintf("/repos/%s/issues/%d", slug, number), nil, &i); err != nil {
		return IssueRead{}, err
	}
	out := IssueRead{IssueLine: i.line(), Body: i.Body, CommentCount: i.Comments, Comments: []IssueComment{}}
	if i.Comments == 0 || maxComments <= 0 {
		return out, nil
	}
	comments, err := pagesUpTo[ghComment](ctx, g, fmt.Sprintf("/repos/%s/issues/%d/comments", slug, number), (maxComments+99)/100)
	if err != nil {
		return IssueRead{}, err
	}
	for _, c := range comments[:min(len(comments), maxComments)] {
		out.Comments = append(out.Comments, IssueComment{Author: login(c.User), Body: c.Body, CreatedAt: c.CreatedAt})
	}
	out.CommentCount = max(out.CommentCount, len(comments))
	return out, nil
}

// PullRequestRead reads a pull request whole: its text, head and base,
// every review with its text, the threads nobody resolved, the checks on
// its head, the files it changes (at most maxFiles), each file's diff
// with it, and the newest maxComments comments of its conversation.
func (g *GitHub) PullRequestRead(ctx context.Context, slug string, number, maxFiles, maxComments int, requested ...string) (_ PullRequestRead, err error) {
	ctx = agentReadContext(ctx)
	defer func() { err = g.itemMoved(ctx, slug, err) }()
	if err := g.readBudget(ctx, slug, "core", "graphql"); err != nil {
		return PullRequestRead{}, err
	}
	var p struct {
		ghPull
		Body      string    `json:"body"`
		UpdatedAt string    `json:"updated_at"`
		Labels    []ghLabel `json:"labels"`
	}
	if err := g.do(ctx, "GET", fmt.Sprintf("/repos/%s/pulls/%d", slug, number), nil, &p); err != nil {
		return PullRequestRead{}, err
	}
	ref := p.ref()
	out := PullRequestRead{
		IssueLine: IssueLine{Number: p.Number, Title: p.Title, Author: login(p.User), State: ref.State, Labels: labelNames(p.Labels),
			UpdatedAt: p.UpdatedAt, URL: p.HTMLURL, PullRequest: true},
		Body: p.Body, Draft: p.Draft, Head: p.Head.Ref, Base: p.Base.Ref,
		Reviews: []ReviewRead{}, Threads: []ThreadRead{}, Checks: []Check{}, Files: []FileRead{},
	}
	var reviews []ghReview
	if err := g.do(ctx, "GET", fmt.Sprintf("/repos/%s/pulls/%d/reviews?per_page=60&page=1", slug, number), nil, &reviews); err != nil {
		return PullRequestRead{}, err
	}
	out.ReviewsCut = len(reviews) >= 60
	for _, r := range reviews {
		if r.SubmittedAt == nil || r.State == "PENDING" {
			continue
		}
		if len(out.Reviews) == 50 {
			out.ReviewsCut = true
			break
		}
		out.Reviews = append(out.Reviews, ReviewRead{Author: login(r.User), State: r.State, Body: r.Body, SubmittedAt: *r.SubmittedAt})
	}
	if out.Threads, out.ThreadsUnknown, err = g.openThreads(ctx, slug, number); err != nil {
		return PullRequestRead{}, err
	}
	if p.Head.SHA != "" {
		if _, out.Checks, out.ChecksCut, err = g.checks(ctx, slug, p.Head.SHA); err != nil {
			return PullRequestRead{}, err
		}
		if out.Checks == nil {
			out.Checks = []Check{}
		}
	}
	var files []ghFile
	for page := 1; page <= (maxFiles+99)/100; page++ {
		var batch []ghFile
		if err := g.do(ctx, "GET", fmt.Sprintf("/repos/%s/pulls/%d/files?per_page=100&page=%d", slug, number, page), nil, &batch); err != nil {
			return PullRequestRead{}, err
		}
		for i := range batch {
			if !slices.Contains(requested, batch[i].Filename) {
				batch[i].Patch = ""
			}
		}
		files = append(files, batch...)
		if len(batch) < 100 {
			break
		}
	}
	for _, f := range files[:min(len(files), maxFiles)] {
		out.Files = append(out.Files, FileRead{Path: f.Filename, Status: f.Status, Additions: f.Additions, Deletions: f.Deletions, Patch: f.Patch})
	}
	if out.Comments, out.CommentCount, err = g.conversationTail(ctx, slug, number, maxComments); err != nil {
		return PullRequestRead{}, err
	}
	return out, nil
}

// PullFiles is the paths a pull request changes, up to max (GitHub lists
// at most 3000).
func (g *GitHub) PullFiles(ctx context.Context, slug string, number, max int) ([]string, error) {
	var out []string
	for page := 1; page <= (max+99)/100; page++ {
		var batch []ghFile
		if err := g.do(ctx, "GET", fmt.Sprintf("/repos/%s/pulls/%d/files?per_page=100&page=%d", slug, number, page), nil, &batch); err != nil {
			return nil, err
		}
		for _, f := range batch {
			out = append(out, f.Filename)
		}
		if len(batch) < 100 {
			break
		}
	}
	return out[:min(len(out), max)], nil
}

// conversationTail reads the newest conversation comments and derives their
// total from the paginated list rather than the pull request's comment count.
// It reads page 1, the last page from its Link header, and at most one page
// before that. An empty last page is stepped back once. keep must be at most
// 100, so one prepended page holds the rest.
func (g *GitHub) conversationTail(ctx context.Context, slug string, number int, keep int) ([]IssueComment, int, error) {
	if keep <= 0 {
		return []IssueComment{}, 0, nil
	}
	path := fmt.Sprintf("/repos/%s/issues/%d/comments?per_page=100&page=", slug, number)
	var first []ghComment
	link, err := g.getLinked(ctx, path+"1", &first)
	if err != nil {
		return nil, 0, err
	}
	read := func(page int) ([]ghComment, error) {
		if page == 1 {
			return first, nil
		}
		var batch []ghComment
		err := g.do(ctx, "GET", path+strconv.Itoa(page), nil, &batch)
		return batch, err
	}
	all, last := first, max(lastPage(link), 1)
	lastWasEmpty := false
	if last > 1 {
		if all, err = read(last); err != nil {
			return nil, 0, err
		}
		if len(all) == 0 {
			last--
			lastWasEmpty = true
			if all, err = read(last); err != nil {
				return nil, 0, err
			}
		}
	}
	total := (last-1)*100 + len(all)
	// After an empty last page, only the held page 1 avoids a fourth request.
	if len(all) < keep && last > 1 && (!lastWasEmpty || last == 2) {
		prev, err := read(last - 1)
		if err != nil {
			return nil, 0, err
		}
		all = append(slices.Clip(prev), all...)
	}
	out := []IssueComment{}
	for _, c := range all[max(0, len(all)-keep):] {
		out = append(out, IssueComment{Author: login(c.User), Body: c.Body, CreatedAt: c.CreatedAt})
	}
	return out, total, nil
}

// openThreads is a pull request's unresolved review threads with their
// comments, at most 5 pages of 50 threads and 20 comments each. Like
// unresolvedThreads, a rate limit is an error and any other GraphQL
// failure is unknown.
func (g *GitHub) openThreads(ctx context.Context, slug string, number int) ([]ThreadRead, bool, error) {
	owner, name, _ := strings.Cut(slug, "/")
	const query = `query($owner:String!,$name:String!,$number:Int!,$after:String){repository(owner:$owner,name:$name){` +
		`pullRequest(number:$number){reviewThreads(first:50,after:$after){nodes{isResolved isOutdated path line ` +
		`comments(first:20){totalCount nodes{author{login} body createdAt}}}pageInfo{hasNextPage endCursor}}}}}`
	out := []ThreadRead{}
	var after *string
	for range 5 {
		var data struct {
			Repository *struct {
				PullRequest *struct {
					ReviewThreads struct {
						Nodes []struct {
							IsResolved bool   `json:"isResolved"`
							IsOutdated bool   `json:"isOutdated"`
							Path       string `json:"path"`
							Line       int    `json:"line"`
							Comments   struct {
								TotalCount int `json:"totalCount"`
								Nodes      []struct {
									Author *struct {
										Login string `json:"login"`
									} `json:"author"`
									Body      string `json:"body"`
									CreatedAt string `json:"createdAt"`
								} `json:"nodes"`
							} `json:"comments"`
						} `json:"nodes"`
						PageInfo struct {
							HasNextPage bool   `json:"hasNextPage"`
							EndCursor   string `json:"endCursor"`
						} `json:"pageInfo"`
					} `json:"reviewThreads"`
				} `json:"pullRequest"`
			} `json:"repository"`
		}
		errs, err := g.graphql(ctx, query, map[string]any{"owner": owner, "name": name, "number": number, "after": after}, &data)
		if err != nil {
			if Transient(err) {
				return nil, false, err
			}
			return []ThreadRead{}, true, nil
		}
		if len(errs) > 0 || data.Repository == nil || data.Repository.PullRequest == nil {
			return []ThreadRead{}, true, nil
		}
		threads := data.Repository.PullRequest.ReviewThreads
		for _, n := range threads.Nodes {
			if n.IsResolved {
				continue
			}
			t := ThreadRead{Path: n.Path, Line: n.Line, Outdated: n.IsOutdated, CommentCount: n.Comments.TotalCount}
			for _, c := range n.Comments.Nodes {
				author := "unknown"
				if c.Author != nil {
					author = c.Author.Login
				}
				t.Comments = append(t.Comments, IssueComment{Author: author, Body: c.Body, CreatedAt: c.CreatedAt})
			}
			out = append(out, t)
		}
		if !threads.PageInfo.HasNextPage {
			return out, false, nil
		}
		c := threads.PageInfo.EndCursor
		after = &c
	}
	return out, true, nil
}

// The share of a token's hourly limit kept for delivery: once GitHub says
// no more than limit/agentReadReserve are left, an agent's read is
// refused until the limit resets. Delivery opens, reads back and fixes
// pull requests on the same App installation; an agent reading issues
// must never be what leaves it none.
const agentReadReserve = 5

// rateReading is what GitHub last said of a token's limit on one resource
// (core, graphql, search), from its X-RateLimit headers.
type rateReading struct {
	remaining, limit int
	reset            time.Time
}

var rateReadings sync.Map // rateKey → rateReading
var rateAdmission sync.Mutex

func reserveAgentRequest(target, authorization, resource string) error {
	rateAdmission.Lock()
	defer rateAdmission.Unlock()
	key := rateKey(target, authorization, resource)
	v, ok := rateReadings.Load(key)
	if !ok {
		return nil
	}
	r := v.(rateReading)
	now := time.Now()
	if r.reserved(now) {
		return r.refusal(now, "GitHub's rate limit is kept for delivery")
	}
	if !r.reset.After(now) {
		return nil
	}
	r.remaining--
	rateReadings.Store(key, r)
	return nil
}

// reserved says the reading, still current at now, leaves no more than
// delivery's reserve.
func (r rateReading) reserved(now time.Time) bool {
	return r.reset.After(now) && r.remaining <= r.limit/agentReadReserve
}

// refusal is an agent read's 429 for a reserved reading, carrying the wait
// until its reset as GitHub's own limit would.
func (r rateReading) refusal(now time.Time, message string) *Error {
	return &Error{Status: http.StatusTooManyRequests, RetryAfter: min(r.reset.Sub(now), MaxRetryAfter), Refusal: RefusalRateReserve,
		Message: message}
}

// rateKey names one token's limit on one resource at one GitHub (its
// API's host), never holding the token itself.
func rateKey(target, authorization, resource string) string {
	host := target
	if u, err := url.Parse(target); err == nil {
		host = u.Host
	}
	sum := sha256.Sum256([]byte(host + "\x00" + authorization))
	return hex.EncodeToString(sum[:12]) + "\x00" + resource
}

// noteRate keeps the rate limit an answer reports for the token that asked.
func noteRate(target, authorization string, h http.Header) {
	remaining, err1 := strconv.Atoi(h.Get("X-RateLimit-Remaining"))
	limit, err2 := strconv.Atoi(h.Get("X-RateLimit-Limit"))
	reset, err3 := strconv.ParseInt(h.Get("X-RateLimit-Reset"), 10, 64)
	if err1 != nil || err2 != nil || err3 != nil {
		return
	}
	resource := h.Get("X-RateLimit-Resource")
	if resource == "" {
		resource = "core"
	}
	rateAdmission.Lock()
	defer rateAdmission.Unlock()
	now := time.Now()
	rateReadings.Range(func(key, value any) bool {
		if !value.(rateReading).reset.After(now) {
			rateReadings.Delete(key)
		}
		return true
	})
	rateReadings.Store(rateKey(target, authorization, resource), rateReading{remaining: remaining, limit: limit, reset: time.Unix(reset, 0)})
}

// readBudget refuses an agent's read, before any request, while the token
// for scope has no more of a resource left than delivery's reserve: a 429
// *Error carrying the wait, as GitHub's own limit would be.
func (g *GitHub) readBudget(ctx context.Context, scope string, resources ...string) error {
	token, err := g.Token(ctx, scope)
	if err != nil {
		return err
	}
	now := time.Now()
	for _, resource := range resources {
		v, ok := rateReadings.Load(rateKey(g.cred.APIBaseURL, "Bearer "+token, resource))
		if !ok {
			continue
		}
		if r := v.(rateReading); r.reserved(now) {
			return r.refusal(now, fmt.Sprintf("GitHub's %s rate limit has %d of %d requests left until %s: they are kept for delivery",
				resource, r.remaining, r.limit, r.reset.UTC().Format(time.RFC3339)))
		}
	}
	return nil
}
