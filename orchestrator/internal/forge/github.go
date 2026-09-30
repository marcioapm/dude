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
	// A review's own text, approving or commenting: kept, because "approved,
	// but rename this" is a request too.
	KindReview = "review"
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
	// The app that reported it: "github-actions" for Actions, whose check
	// run is a job, re-run through the Actions API.
	App string `json:"app,omitempty"`
}

// Failed says the check finished and failed: what wakes a fixer.
func (c Check) Failed() bool { return checkRunState(c.Status, c.Conclusion) == ChecksFailing }

// Review is one reviewer's latest word on a pull request: APPROVED,
// CHANGES_REQUESTED, COMMENTED, DISMISSED, or REQUESTED for one asked and
// yet to answer.
type Review struct {
	Login       string  `json:"login"`
	State       string  `json:"state"`
	SubmittedAt *string `json:"submittedAt"`
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
	cred Credential
	http *http.Client
	// How the organization wants dude to behave on GitHub.
	Settings Settings
}

func NewGitHub(c Credential) *GitHub {
	if c.APIBaseURL == "" {
		c.APIBaseURL = "https://api.github.com"
	}
	c.APIBaseURL = strings.TrimRight(c.APIBaseURL, "/")
	return &GitHub{cred: c, http: &http.Client{Timeout: requestTimeout}, Settings: DefaultSettings()}
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
	localGit := clone.Scheme == "git" && base.Scheme == "http" && (base.Hostname() == "127.0.0.1" || base.Hostname() == "localhost") && clone.Hostname() == base.Hostname()
	if !localGit && (clone.Scheme != base.Scheme || !strings.EqualFold(clone.Host, base.Host)) {
		return fmt.Errorf("repository origin does not match the configured GitHub origin")
	}
	slug := strings.TrimSuffix(strings.TrimPrefix(clone.Path, "/"), ".git")
	if !regexp.MustCompile(`^[A-Za-z0-9_-]+/[A-Za-z0-9_.-]+$`).MatchString(slug) || strings.HasSuffix(slug, "/.") || strings.HasSuffix(slug, "/..") {
		return fmt.Errorf("invalid GitHub repository path")
	}
	base.Path = "/" + slug + ".git/info/refs"
	base.RawQuery = "service=git-receive-pack"
	token, err := g.Token()
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
	if res.StatusCode == http.StatusForbidden && (res.Header.Get("Retry-After") != "" || res.Header.Get("X-RateLimit-Remaining") == "0") {
		refusal.Message = "Git receive-pack discovery rate limit: " + message
	}
	if !Transient(refusal) && (res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusForbidden) {
		refusal.Message = "Git push access denied: grant Contents: Read and write to the fine-grained PAT for this repository (classic PAT: repo or public_repo), and check repository selection, owner access and organization approval/SSO"
	}
	return refusal
}

func (g *GitHub) do(ctx context.Context, method, path string, body, out any) error {
	return g.doURL(ctx, method, g.cred.APIBaseURL+path, body, out)
}

func (g *GitHub) doURL(ctx context.Context, method, target string, body, out any) error {
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
	req, err := http.NewRequestWithContext(ctx, method, target, reader)
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
			e.Message = fmt.Sprintf("%s %s failed", method, target)
		}
		return &Error{Status: res.StatusCode, Message: e.Message}
	}
	if out != nil && len(data) > 0 {
		return json.Unmarshal(data, out)
	}
	return nil
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
	Head           struct {
		SHA string `json:"sha"`
	} `json:"head"`
	Base struct {
		Ref string `json:"ref"`
	} `json:"base"`
	RequestedReviewers []struct {
		Login string `json:"login"`
	} `json:"requested_reviewers"`
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
	if st.Checks, st.CheckList, err = g.checks(ctx, slug, p.Head.SHA); err != nil {
		return Status{}, err
	}
	reviews, err := pages[ghReview](ctx, g, fmt.Sprintf("/repos/%s/pulls/%d/reviews", slug, number))
	if err != nil {
		return Status{}, err
	}
	st.Review, st.Reviews, st.reviews = reviewState(reviews), latestReviews(reviews, p.RequestedReviewers), reviews
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
// such endpoint (404): no check runs. A token without Checks: read is
// refused (403): there may be CI it cannot see, so the checks are never
// read as passing — pending, which holds readiness back without waking a
// fixer; the token needs Checks: read. A rate limit is also a 403, and is
// neither: it fails the sync, to be tried again.
func (g *GitHub) checks(ctx context.Context, slug, sha string) (string, []Check, error) {
	var combined struct {
		State      string `json:"state"`
		TotalCount int    `json:"total_count"`
		Statuses   []struct {
			Context   string `json:"context"`
			State     string `json:"state"`
			TargetURL string `json:"target_url"`
			CreatedAt string `json:"created_at"`
			UpdatedAt string `json:"updated_at"`
		} `json:"statuses"`
	}
	if err := g.do(ctx, "GET", fmt.Sprintf("/repos/%s/commits/%s/status", slug, sha), nil, &combined); err != nil {
		return "", nil, err
	}
	rollup := checkState(combined.State, combined.TotalCount)
	var list []Check
	for _, s := range combined.Statuses {
		c := Check{Name: s.Context, URL: s.TargetURL, Status: "completed", Conclusion: s.State,
			DurationMs: elapsed(s.CreatedAt, s.UpdatedAt)}
		if s.State == "pending" {
			c.Status, c.Conclusion, c.DurationMs = "in_progress", "", 0
		}
		list = append(list, c)
	}
	for page, seen := 1, 0; ; page++ {
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
			}
			break
		}
		if err != nil {
			return "", nil, err
		}
		for _, r := range runs.CheckRuns {
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
	return rollup, list, nil
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
	sep := "?"
	if strings.Contains(path, "?") {
		sep = "&"
	}
	var all []T
	// Bounded, so an endpoint that ignores paging cannot loop forever.
	for page := 1; page <= 50; page++ {
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
		var out struct {
			Data *struct {
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
			} `json:"data"`
			Errors []struct {
				Type    string `json:"type"`
				Message string `json:"message"`
			} `json:"errors"`
		}
		err := g.doURL(ctx, "POST", g.graphqlURL(), map[string]any{"query": query,
			"variables": map[string]any{"owner": owner, "name": name, "number": number, "after": after}}, &out)
		if err != nil {
			if Transient(err) {
				return 0, false, err
			}
			return 0, true, nil
		}
		for _, e := range out.Errors {
			if e.Type == "RATE_LIMITED" {
				return 0, false, &Error{Status: 429, Message: "GraphQL: " + e.Message}
			}
		}
		if len(out.Errors) > 0 || out.Data == nil || out.Data.Repository == nil || out.Data.Repository.PullRequest == nil {
			return 0, true, nil
		}
		threads := out.Data.Repository.PullRequest.ReviewThreads
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
		out = append(out, Feedback{ID: fmt.Sprintf("issue-comment-%d", c.ID), Author: login(c.User), Body: c.Body, CreatedAt: c.CreatedAt, Kind: KindComment})
	}
	for _, c := range line {
		out = append(out, Feedback{ID: fmt.Sprintf("line-comment-%d", c.ID), Author: login(c.User), Body: c.Body, Path: c.Path, CreatedAt: c.CreatedAt, Kind: KindLineComment})
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
		out = append(out, Feedback{ID: fmt.Sprintf("review-%d", r.ID), Author: login(r.User), Body: r.Body, CreatedAt: *r.SubmittedAt, Kind: kind})
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

// Head is a pull request's head commit as GitHub has it now: where a fix
// must start, whoever moved it last.
func (g *GitHub) Head(ctx context.Context, slug string, number int) (string, error) {
	var p ghPull
	if err := g.do(ctx, "GET", fmt.Sprintf("/repos/%s/pulls/%d", slug, number), nil, &p); err != nil {
		return "", err
	}
	return p.Head.SHA, nil
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
		if c.App == "github-actions" {
			path = fmt.Sprintf("/repos/%s/actions/jobs/%d/rerun", slug, c.RunID)
		}
		if err := g.do(ctx, "POST", path, map[string]any{}, nil); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// RequestReviewers asks people for a review on a pull request.
func (g *GitHub) RequestReviewers(ctx context.Context, slug string, number int, logins []string) error {
	return g.do(ctx, "POST", fmt.Sprintf("/repos/%s/pulls/%d/requested_reviewers", slug, number),
		map[string]any{"reviewers": logins}, nil)
}

// CheckOutput is what a failed check run says about why: its summary and
// text, and its annotations (file, line, message). Truncated: a fixer
// wants the reason, not the whole log.
func (g *GitHub) CheckOutput(ctx context.Context, slug string, runID int64, limit int) (string, error) {
	var run struct {
		Output struct {
			Title   string `json:"title"`
			Summary string `json:"summary"`
			Text    string `json:"text"`
		} `json:"output"`
	}
	if err := g.do(ctx, "GET", fmt.Sprintf("/repos/%s/check-runs/%d", slug, runID), nil, &run); err != nil {
		return "", err
	}
	var annotations []struct {
		Path    string `json:"path"`
		Line    int    `json:"start_line"`
		Level   string `json:"annotation_level"`
		Message string `json:"message"`
	}
	if err := g.do(ctx, "GET", fmt.Sprintf("/repos/%s/check-runs/%d/annotations?per_page=50", slug, runID), nil, &annotations); err != nil {
		var e *Error
		if !asError(err, &e) || Transient(err) {
			return "", err
		}
	}
	var b strings.Builder
	for _, part := range []string{run.Output.Title, run.Output.Summary, run.Output.Text} {
		if part = strings.TrimSpace(part); part != "" {
			b.WriteString(part + "\n")
		}
	}
	for _, a := range annotations {
		fmt.Fprintf(&b, "%s:%d: %s: %s\n", a.Path, a.Line, a.Level, a.Message)
	}
	return truncate(strings.TrimSpace(b.String()), limit), nil
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
func latestReviews(reviews []ghReview, requested []struct {
	Login string `json:"login"`
}) []Review {
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
		latest[r.User.Login] = Review{Login: r.User.Login, State: r.State, SubmittedAt: r.SubmittedAt}
	}
	out := make([]Review, 0, len(order)+len(requested))
	for _, l := range order {
		out = append(out, latest[l])
	}
	// Asked again after reviewing: GitHub lists them as requested once more.
	for _, r := range requested {
		if _, seen := latest[r.Login]; !seen {
			out = append(out, Review{Login: r.Login, State: "REQUESTED"})
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
