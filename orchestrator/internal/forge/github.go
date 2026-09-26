// Package forge talks to GitHub: pull requests, branches, and the webhooks
// that report on them.
//
// An interface-shaped client rather than calls scattered through the
// workflow, because authentication will move from a personal access token to
// a GitHub App, and because a forge is the one dependency here that rate
// limits and has outages — the thing a test must be able to replace.
package forge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
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
)

// Credential is how an organization authenticates to GitHub.
type Credential struct {
	Auth       string // "pat" or "github_app"
	Secret     string
	APIBaseURL string
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
}

// Feedback is one thing a person left on a pull request. Conversation
// comments, line comments and a changes-requested review body arrive in one
// shape because, to a fixer, they are the same: something to be different.
type Feedback struct {
	ID        string
	Author    string
	Body      string
	Path      string
	CreatedAt string
	Kind      string
}

// Error is GitHub refusing a request.
type Error struct {
	Status  int
	Message string
}

func (e *Error) Error() string { return fmt.Sprintf("github %d: %s", e.Status, e.Message) }

// AlreadyExists: GitHub refused a PR because one is open for the branch.
// Only that; other 422s are validation errors that mean something else.
func (e *Error) AlreadyExists() bool {
	return e.Status == 422 && strings.Contains(strings.ToLower(e.Message), "already exists")
}

// Transient says whether trying again later could succeed: the forge was
// unreachable, rate-limiting or failing, rather than refusing.
func Transient(err error) bool {
	var e *Error
	if errors.As(err, &e) {
		return e.Status == 429 || e.Status >= 500 || e.Status == 403 && strings.Contains(strings.ToLower(e.Message), "rate limit")
	}
	// No answer from GitHub at all: the request never completed.
	var u *Unreachable
	return errors.As(err, &u)
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
	cred Credential
	http *http.Client
}

func NewGitHub(c Credential) *GitHub {
	if c.APIBaseURL == "" {
		c.APIBaseURL = "https://api.github.com"
	}
	c.APIBaseURL = strings.TrimRight(c.APIBaseURL, "/")
	return &GitHub{cred: c, http: &http.Client{Timeout: requestTimeout}}
}

// Token is the credential to hand lux for cloning and pushing. With a PAT it
// is the PAT; with a GitHub App it will be a fresh installation token, which
// is why callers must never cache it.
func (g *GitHub) Token() (string, error) {
	if g.cred.Auth == "pat" || g.cred.Auth == "" {
		return g.cred.Secret, nil
	}
	return "", fmt.Errorf("github_app authentication is not implemented yet")
}

func (g *GitHub) do(ctx context.Context, method, path string, body, out any) error {
	token, err := g.Token()
	if err != nil {
		return err
	}
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, g.cred.APIBaseURL+path, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := g.http.Do(req)
	if err != nil {
		return &Unreachable{err}
	}
	defer res.Body.Close()
	data, err := io.ReadAll(res.Body)
	if err != nil {
		return &Unreachable{err}
	}
	if res.StatusCode >= 300 {
		var e struct {
			Message string `json:"message"`
		}
		_ = json.Unmarshal(data, &e)
		if e.Message == "" {
			e.Message = fmt.Sprintf("%s %s failed", method, path)
		}
		return &Error{Status: res.StatusCode, Message: e.Message}
	}
	if out != nil && len(data) > 0 {
		return json.Unmarshal(data, out)
	}
	return nil
}

type ghPull struct {
	Number   int     `json:"number"`
	NodeID   string  `json:"node_id"`
	HTMLURL  string  `json:"html_url"`
	Draft    bool    `json:"draft"`
	State    string  `json:"state"`
	MergedAt *string `json:"merged_at"`
	Head     struct {
		SHA string `json:"sha"`
	} `json:"head"`
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

func (g *GitHub) OpenPullRequest(ctx context.Context, in OpenPullRequest) (PullRequestRef, error) {
	var p ghPull
	err := g.do(ctx, "POST", "/repos/"+in.Slug+"/pulls", map[string]any{
		"title": in.Title, "body": in.Body, "head": in.Head, "base": in.Base, "draft": in.Draft,
	}, &p)
	return p.ref(), err
}

// PullRequest reads a pull request's state, checks and review verdict.
func (g *GitHub) PullRequest(ctx context.Context, slug string, number int) (Status, error) {
	var p ghPull
	if err := g.do(ctx, "GET", fmt.Sprintf("/repos/%s/pulls/%d", slug, number), nil, &p); err != nil {
		return Status{}, err
	}
	// The combined status rolls up both the legacy status API and checks,
	// which is the only reading right whichever one a project's CI uses.
	var combined struct {
		State      string `json:"state"`
		TotalCount int    `json:"total_count"`
	}
	if err := g.do(ctx, "GET", fmt.Sprintf("/repos/%s/commits/%s/status", slug, p.Head.SHA), nil, &combined); err != nil {
		return Status{}, err
	}
	var reviews []ghReview
	if err := g.do(ctx, "GET", fmt.Sprintf("/repos/%s/pulls/%d/reviews", slug, number), nil, &reviews); err != nil {
		return Status{}, err
	}
	return Status{PullRequestRef: p.ref(), Checks: checkState(combined.State, combined.TotalCount), Review: reviewState(reviews)}, nil
}

type ghComment struct {
	ID        int64  `json:"id"`
	Body      string `json:"body"`
	Path      string `json:"path"`
	CreatedAt string `json:"created_at"`
	User      *struct {
		Login string `json:"login"`
	} `json:"user"`
}

type ghReview struct {
	ID          int64   `json:"id"`
	Body        string  `json:"body"`
	State       string  `json:"state"`
	SubmittedAt *string `json:"submitted_at"`
	User        *struct {
		Login string `json:"login"`
	} `json:"user"`
}

func login(u *struct {
	Login string `json:"login"`
}) string {
	if u == nil {
		return "unknown"
	}
	return u.Login
}

// Feedback lists what people left on a pull request since `since`, oldest
// first. Inclusive of `since` on purpose: GitHub timestamps are whole
// seconds, so a strict comparison drops a comment posted in the same second
// as the last one seen. Callers dedupe by id, which is exact.
func (g *GitHub) Feedback(ctx context.Context, slug string, number int, since string) ([]Feedback, error) {
	q := ""
	if since != "" {
		q = "?since=" + url.QueryEscape(since)
	}
	var issue, line []ghComment
	var reviews []ghReview
	if err := g.do(ctx, "GET", fmt.Sprintf("/repos/%s/issues/%d/comments%s", slug, number, q), nil, &issue); err != nil {
		return nil, err
	}
	if err := g.do(ctx, "GET", fmt.Sprintf("/repos/%s/pulls/%d/comments%s", slug, number, q), nil, &line); err != nil {
		return nil, err
	}
	if err := g.do(ctx, "GET", fmt.Sprintf("/repos/%s/pulls/%d/reviews", slug, number), nil, &reviews); err != nil {
		return nil, err
	}
	var out []Feedback
	for _, c := range issue {
		out = append(out, Feedback{ID: fmt.Sprintf("issue-comment-%d", c.ID), Author: login(c.User), Body: c.Body, CreatedAt: c.CreatedAt, Kind: KindComment})
	}
	for _, c := range line {
		out = append(out, Feedback{ID: fmt.Sprintf("line-comment-%d", c.ID), Author: login(c.User), Body: c.Body, Path: c.Path, CreatedAt: c.CreatedAt, Kind: KindLineComment})
	}
	for _, r := range reviews {
		// A reviewer who writes only "please rename this" in the review box
		// would otherwise be invisible.
		if r.State == "CHANGES_REQUESTED" && r.Body != "" && r.SubmittedAt != nil {
			out = append(out, Feedback{ID: fmt.Sprintf("review-%d", r.ID), Author: login(r.User), Body: r.Body, CreatedAt: *r.SubmittedAt, Kind: KindChangesRequested})
		}
	}
	filtered := out[:0]
	for _, f := range out {
		if since == "" || f.CreatedAt >= since {
			filtered = append(filtered, f)
		}
	}
	sort.SliceStable(filtered, func(i, j int) bool { return filtered[i].CreatedAt < filtered[j].CreatedAt })
	return filtered, nil
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

// FastForward moves branch to sha, creating it if it does not exist.
//
// Never forced: GitHub refuses the update unless sha descends from the
// branch's current commit, so a person who pushed to the branch in the
// meantime is never overwritten. That refusal is reported as an error.
func (g *GitHub) FastForward(ctx context.Context, slug, branch, sha string) error {
	err := g.do(ctx, "PATCH", "/repos/"+slug+"/git/refs/heads/"+branch, map[string]any{"sha": sha, "force": false}, nil)
	var e *Error
	if asError(err, &e) && (e.NotFound() || e.Status == 422 && strings.Contains(e.Message, "does not exist")) {
		return g.do(ctx, "POST", "/repos/"+slug+"/git/refs", map[string]any{"ref": "refs/heads/" + branch, "sha": sha}, nil)
	}
	return err
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
// URL is reused.
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
	for _, h := range hooks {
		if h.Config.URL == target {
			return fmt.Sprint(h.ID), nil
		}
	}
	var created struct {
		ID int64 `json:"id"`
	}
	err := g.do(ctx, "POST", "/repos/"+slug+"/hooks", map[string]any{
		"name":   "web",
		"active": true,
		"events": WebhookEvents,
		"config": map[string]any{"url": target, "content_type": "json", "secret": secret, "insecure_ssl": "0"},
	}, &created)
	return fmt.Sprint(created.ID), err
}

// WebhookEvents are the events that can change what dude does about a PR.
var WebhookEvents = []string{
	"pull_request", "pull_request_review", "pull_request_review_comment",
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
