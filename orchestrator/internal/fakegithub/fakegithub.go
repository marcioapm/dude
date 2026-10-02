// Package fakegithub is a stand-in for the GitHub REST endpoints dude uses,
// backed by a real bare git repository, for the orchestrator's tests.
//
// Branch updates obey GitHub's rules — a non-forced update must be a
// fast-forward — because that rule is what keeps dude from overwriting a
// person's commits, and a fake that allowed anything would hide a bug there.
package fakegithub

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type Pull struct {
	Number   int
	Head     string
	Base     string
	Title    string
	Body     string
	State    string
	Draft    bool
	MergedAt *string
	// Logins asked for a review.
	Requested []string
	Comments  []Comment
	// Reviews submitted, oldest first: each keeps its id, as GitHub's do.
	Reviews []Review
}

type Review struct {
	ID          int64
	Login       string
	State, Body string
	SubmittedAt string
}

type Comment struct {
	ID        int64
	Author    string
	Body      string
	Path      string
	CreatedAt string
}

type Server struct {
	Repo  string // bare repository path
	Slug  string // owner/repo
	mu    sync.Mutex
	pulls map[int]*Pull
	Hooks []map[string]any
	// The combined status every commit reports; "" is success.
	checks string
	// Repository permission by login; one not named has write access, as
	// the people in these tests are the team.
	Permissions map[string]string
	// Pull requests GitHub says conflict with their base.
	Conflicting map[int]bool
	// Review threads left unresolved, by pull request.
	Unresolved map[int]int
	// Update-branch and merge requests received, by pull request.
	Updates, Merges []int
	// Check runs asked to run again: through the checks API, and Actions
	// jobs through the Actions API.
	Rerequested, JobsRerun []int64
	// GitHub has not worked out whether it merges yet (after a push).
	MergeableUnknown map[int]bool
	// The collaborator permission endpoint refuses (a token without the
	// scope).
	PermissionRefused bool
	// Update-branch requests fail as GitHub does when it is down.
	UpdateDown bool
	// The check-runs listing refuses, as for a token without Checks: read.
	CheckRunsForbidden bool
	// When set, the check-runs listing answers 403 with this message and
	// no rate-limit headers, as GitHub's abuse-detection limit does.
	CheckRunsForbiddenMessage string
	ReceiveStatus             int
	ReceiveBody               string
	ReceiveHeaders            http.Header
	ReceiveToken              string
	ReceiveRequests           []string
	ReceiveDisconnect         bool
}

// Comment and review ids, unique across repositories as GitHub's are.
var nextID atomic.Int64

func New(repo, slug string) *Server {
	return &Server{Repo: repo, Slug: slug, pulls: map[int]*Pull{},
		Permissions: map[string]string{}, Conflicting: map[int]bool{}, Unresolved: map[int]int{}, MergeableUnknown: map[int]bool{}}
}

// Set changes the fake's state under its lock.
func (s *Server) Set(f func(s *Server)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f(s)
}

// Reopen reopens a pull request closed without merging, as a person would.
func (s *Server) Reopen(number int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pulls[number].State = "open"
}

// AdvanceBase commits to a pull request's base, as someone merging other
// work does: the pull request falls behind.
func (s *Server) AdvanceBase(base, message string) string {
	return s.CommitOnTop(base, message)
}

func (s *Server) git(args ...string) (string, error) {
	return s.gitAs("", args...)
}

// gitAs runs git with the given person as a commit's author and committer:
// commit-tree takes them from the environment, not from -c.
func (s *Server) gitAs(name string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", s.Repo}, args...)...)
	if name != "" {
		email := strings.ToLower(strings.Fields(name)[0]) + "@example.com"
		cmd.Env = append(cmd.Environ(), "GIT_AUTHOR_NAME="+name, "GIT_AUTHOR_EMAIL="+email,
			"GIT_COMMITTER_NAME="+name, "GIT_COMMITTER_EMAIL="+email)
	}
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// SHA of a branch, or "".
func (s *Server) SHA(branch string) string {
	out, err := s.git("rev-parse", "--verify", "-q", "refs/heads/"+branch)
	if err != nil {
		return ""
	}
	return out
}

// CommitOnTop adds an empty commit to a branch, as a person on GitHub
// does with "Update branch" or a committed suggestion; its SHA.
func (s *Server) CommitOnTop(branch, message string) string {
	tree, _ := s.git("rev-parse", "refs/heads/"+branch+"^{tree}")
	sha, err := s.gitAs("Alice", "commit-tree", tree, "-p", "refs/heads/"+branch, "-m", message)
	if err != nil {
		return ""
	}
	if _, err := s.git("update-ref", "refs/heads/"+branch, sha); err != nil {
		return ""
	}
	return sha
}

// Log lists a branch's commit subjects, newest first.
func (s *Server) Log(branch string) []string {
	out, _ := s.git("log", "--format=%s", "refs/heads/"+branch)
	if out == "" {
		return nil
	}
	return strings.Split(out, "\n")
}

func (s *Server) Pull(number int) *Pull {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pulls[number]
}

func (s *Server) Pulls() []*Pull {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []*Pull
	for i := 1; i <= len(s.pulls); i++ {
		out = append(out, s.pulls[i])
	}
	return out
}

// Comment leaves a comment as a person would.
func (s *Server) Comment(number int, author, body string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pulls[number].Comments = append(s.pulls[number].Comments,
		Comment{ID: nextID.Add(1), Author: author, Body: body, CreatedAt: time.Now().UTC().Format(time.RFC3339)})
}

// Review records a reviewer's verdict: "APPROVED" or "CHANGES_REQUESTED".
func (s *Server) Review(number int, login, verdict string) {
	s.ReviewSaying(number, login, verdict, "")
}

// ReviewSaying submits a review with words in its box.
func (s *Server) ReviewSaying(number int, login, verdict, body string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.pulls[number]
	p.Reviews = append(p.Reviews, Review{ID: nextID.Add(1), Login: login, State: verdict, Body: body,
		SubmittedAt: time.Now().UTC().Format(time.RFC3339)})
}

// SetChecks sets the combined status of every commit: "success", "failure",
// "pending"; "none" for no CI at all; "run:<conclusion>" to report through
// a check run instead.
func (s *Server) SetChecks(state string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.checks = state
}

// Close closes a pull request without merging it, as a person would.
func (s *Server) Close(number int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pulls[number].State = "closed"
}

func (s *Server) Merge(number int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC().Format(time.RFC3339)
	s.pulls[number].State, s.pulls[number].MergedAt = "closed", &now
}

func (s *Server) Handler() http.Handler {
	prefix := "/repos/" + s.Slug
	mux := http.NewServeMux()
	mux.HandleFunc("GET /"+s.Slug+".git/info/refs", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RawQuery != "service=git-receive-pack" {
			fail(w, 400, "expected receive-pack discovery")
			return
		}
		user, token, ok := r.BasicAuth()
		s.mu.Lock()
		s.ReceiveRequests = append(s.ReceiveRequests, token)
		status, expected, disconnect := s.ReceiveStatus, s.ReceiveToken, s.ReceiveDisconnect
		body, headers := s.ReceiveBody, s.ReceiveHeaders.Clone()
		s.mu.Unlock()
		if disconnect {
			conn, _, _ := w.(http.Hijacker).Hijack()
			_ = conn.Close()
			return
		}
		if !ok || user != "x-access-token" || token == "" || expected != "" && token != expected {
			status = 401
		}
		if status == 0 {
			status = 200
		}
		w.Header().Set("Content-Type", "application/x-git-receive-pack-advertisement")
		for name, values := range headers {
			w.Header()[name] = values
		}
		w.WriteHeader(status)
		if body == "" {
			body = "001f# service=git-receive-pack\n0000"
		}
		_, _ = w.Write([]byte(body))
	})
	mux.HandleFunc("POST "+prefix+"/pulls", s.openPull)
	mux.HandleFunc("GET "+prefix+"/pulls/{n}", s.getPull)
	mux.HandleFunc("PATCH "+prefix+"/pulls/{n}", func(w http.ResponseWriter, r *http.Request) {
		p := s.number(w, r)
		if p == nil {
			return
		}
		var in struct{ State string }
		_ = json.NewDecoder(r.Body).Decode(&in)
		s.mu.Lock()
		defer s.mu.Unlock()
		if in.State == "closed" && p.MergedAt == nil {
			p.State = "closed"
		}
		write(w, 200, s.pullJSON(p))
	})
	mux.HandleFunc("GET "+prefix+"/pulls/{n}/reviews", s.reviews)
	mux.HandleFunc("GET "+prefix+"/pulls/{n}/comments", func(w http.ResponseWriter, r *http.Request) { write(w, 200, []any{}) })
	mux.HandleFunc("GET "+prefix+"/issues/{n}/comments", s.comments)
	mux.HandleFunc("GET "+prefix+"/commits/{sha}/status", s.status)
	mux.HandleFunc("GET "+prefix+"/commits/{sha}/check-runs", s.checkRuns)
	mux.HandleFunc("GET "+prefix+"/compare/{spec}", s.compare)
	mux.HandleFunc("PATCH "+prefix+"/git/refs/heads/{branch...}", s.updateRef)
	mux.HandleFunc("POST "+prefix+"/git/refs", s.createRef)
	mux.HandleFunc("DELETE "+prefix+"/git/refs/heads/{branch...}", s.deleteRef)
	mux.HandleFunc("GET "+prefix+"/collaborators/{login}/permission", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		p, ok := s.Permissions[r.PathValue("login")]
		refused := s.PermissionRefused
		s.mu.Unlock()
		if refused {
			fail(w, 403, "Resource not accessible by personal access token")
			return
		}
		if !ok {
			p = "write"
		}
		if p == "none" {
			fail(w, 404, "Not a collaborator")
			return
		}
		write(w, 200, map[string]string{"permission": p, "role_name": p})
	})
	mux.HandleFunc("GET "+prefix+"/commits/{sha}", func(w http.ResponseWriter, r *http.Request) {
		out, err := s.git("log", "-1", "--format=%an", r.PathValue("sha"))
		if err != nil {
			fail(w, 404, "No commit found")
			return
		}
		write(w, 200, map[string]any{"author": nil, "commit": map[string]any{"author": map[string]string{"name": out}}})
	})
	mux.HandleFunc("PUT "+prefix+"/pulls/{n}/update-branch", func(w http.ResponseWriter, r *http.Request) {
		p := s.number(w, r)
		if p == nil {
			return
		}
		s.mu.Lock()
		conflicting, down := s.Conflicting[p.Number], s.UpdateDown
		s.Updates = append(s.Updates, p.Number)
		s.mu.Unlock()
		if down {
			fail(w, 502, "Server Error")
			return
		}
		if conflicting {
			fail(w, 422, "merge conflict between base and head")
			return
		}
		// A merge commit of the base into the head, as GitHub makes.
		tree, _ := s.git("rev-parse", "refs/heads/"+p.Head+"^{tree}")
		sha, err := s.gitAs("GitHub", "commit-tree", tree,
			"-p", "refs/heads/"+p.Head, "-p", "refs/heads/"+p.Base, "-m", "Merge branch '"+p.Base+"' into "+p.Head)
		if err == nil {
			_, err = s.git("update-ref", "refs/heads/"+p.Head, sha)
		}
		if err != nil {
			fail(w, 422, sha)
			return
		}
		write(w, 202, map[string]string{"message": "Updating pull request branch."})
	})
	mux.HandleFunc("PUT "+prefix+"/pulls/{n}/merge", func(w http.ResponseWriter, r *http.Request) {
		p := s.number(w, r)
		if p == nil {
			return
		}
		var in struct {
			SHA string `json:"sha"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		if in.SHA != "" && in.SHA != s.SHA(p.Head) {
			fail(w, 409, "Head branch was modified. Review and try the merge again.")
			return
		}
		s.mu.Lock()
		s.Merges = append(s.Merges, p.Number)
		s.mu.Unlock()
		s.Merge(p.Number)
		write(w, 200, map[string]any{"merged": true, "sha": s.SHA(p.Head)})
	})
	mux.HandleFunc("POST "+prefix+"/pulls/{n}/requested_reviewers", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Reviewers []string `json:"reviewers"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		if p := s.number(w, r); p != nil {
			s.mu.Lock()
			defer s.mu.Unlock()
			p.Requested = append(p.Requested, in.Reviewers...)
			write(w, 201, s.pullJSON(p))
		}
	})
	mux.HandleFunc("POST "+prefix+"/check-runs/{id}/rerequest", func(w http.ResponseWriter, r *http.Request) {
		id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
		s.mu.Lock()
		s.Rerequested = append(s.Rerequested, id)
		s.mu.Unlock()
		write(w, 201, map[string]any{})
	})
	mux.HandleFunc("POST "+prefix+"/actions/jobs/{id}/rerun", func(w http.ResponseWriter, r *http.Request) {
		id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
		s.mu.Lock()
		s.JobsRerun = append(s.JobsRerun, id)
		s.mu.Unlock()
		write(w, 201, map[string]any{})
	})
	mux.HandleFunc("GET "+prefix+"/check-runs/{id}", func(w http.ResponseWriter, r *http.Request) {
		write(w, 200, map[string]any{"output": map[string]string{"title": "1 test failed",
			"summary": "TestGreeting: expected hello, got hi", "text": ""}})
	})
	mux.HandleFunc("GET "+prefix+"/check-runs/{id}/annotations", func(w http.ResponseWriter, r *http.Request) {
		write(w, 200, []any{map[string]any{"path": "greet.go", "start_line": 12, "annotation_level": "failure",
			"message": "expected hello"}})
	})
	mux.HandleFunc("GET "+prefix+"/hooks", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		write(w, 200, s.Hooks)
	})
	mux.HandleFunc("POST "+prefix+"/hooks", func(w http.ResponseWriter, r *http.Request) {
		var h map[string]any
		_ = json.NewDecoder(r.Body).Decode(&h)
		s.mu.Lock()
		defer s.mu.Unlock()
		h["id"] = len(s.Hooks) + 1
		s.Hooks = append(s.Hooks, h)
		write(w, 201, h)
	})
	return mux
}

func (s *Server) openPull(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Title, Body, Head, Base string
		Draft                   bool
	}
	_ = json.NewDecoder(r.Body).Decode(&in)
	if s.SHA(in.Head) == "" {
		fail(w, 422, "head branch does not exist")
		return
	}
	s.mu.Lock()
	for _, p := range s.pulls {
		if p.Head == in.Head && p.State == "open" {
			s.mu.Unlock()
			fail(w, 422, "A pull request already exists for "+in.Head)
			return
		}
	}
	p := &Pull{Number: len(s.pulls) + 1, Head: in.Head, Base: in.Base, Title: in.Title, Body: in.Body, State: "open", Draft: in.Draft}
	s.pulls[p.Number] = p
	out := s.pullJSON(p)
	s.mu.Unlock()
	write(w, 201, out)
}

func (s *Server) pullJSON(p *Pull) map[string]any {
	requested := []any{}
	for _, l := range p.Requested {
		requested = append(requested, map[string]string{"login": l})
	}
	var mergeable any = true
	state := "clean"
	if s.Conflicting[p.Number] {
		mergeable, state = false, "dirty"
	}
	if s.MergeableUnknown[p.Number] {
		mergeable, state = nil, "unknown"
	}
	return map[string]any{
		"number": p.Number, "node_id": fmt.Sprintf("PR_%d", p.Number),
		"html_url": fmt.Sprintf("https://github.test/%s/pull/%d", s.Slug, p.Number),
		"draft":    p.Draft, "state": p.State, "merged_at": p.MergedAt,
		"head": map[string]string{"sha": s.SHA(p.Head)}, "base": map[string]string{"ref": p.Base},
		"mergeable": mergeable, "mergeable_state": state, "requested_reviewers": requested,
	}
}

// Graphql answers the one query dude sends — a pull request's review
// threads, Unresolved of them unresolved — for whichever of servers holds
// the repository the query names.
func Graphql(servers ...*Server) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Variables struct {
				Owner, Name string
				Number      int
			} `json:"variables"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		for _, s := range servers {
			if s.Slug != in.Variables.Owner+"/"+in.Variables.Name {
				continue
			}
			s.mu.Lock()
			n := s.Unresolved[in.Variables.Number]
			s.mu.Unlock()
			nodes := []any{map[string]bool{"isResolved": true}}
			for range n {
				nodes = append(nodes, map[string]bool{"isResolved": false})
			}
			write(w, 200, map[string]any{"data": map[string]any{"repository": map[string]any{"pullRequest": map[string]any{
				"reviewThreads": map[string]any{"nodes": nodes, "pageInfo": map[string]any{"hasNextPage": false}}}}}})
			return
		}
		write(w, 200, map[string]any{"errors": []any{map[string]string{"message": "Could not resolve to a Repository"}}})
	}
}

func (s *Server) number(w http.ResponseWriter, r *http.Request) *Pull {
	n, _ := strconv.Atoi(r.PathValue("n"))
	p := s.Pull(n)
	if p == nil {
		fail(w, 404, "Not Found")
	}
	return p
}

func (s *Server) getPull(w http.ResponseWriter, r *http.Request) {
	if p := s.number(w, r); p != nil {
		s.mu.Lock()
		defer s.mu.Unlock()
		write(w, 200, s.pullJSON(p))
	}
}

func (s *Server) comments(w http.ResponseWriter, r *http.Request) {
	p := s.number(w, r)
	if p == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []any{}
	for _, c := range p.Comments {
		out = append(out, map[string]any{"id": c.ID, "body": c.Body, "created_at": c.CreatedAt, "user": map[string]string{"login": c.Author}})
	}
	write(w, 200, out)
}

func (s *Server) status(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	state := s.checks
	s.mu.Unlock()
	if strings.HasPrefix(state, "run:") || state == "none" {
		// Actions only, or no CI on this commit: no commit statuses at all.
		write(w, 200, map[string]any{"state": "pending", "total_count": 0})
		return
	}
	if state == "" {
		state = "success"
	}
	write(w, 200, map[string]any{"state": state, "total_count": 1})
}

// checkRuns: GitHub Actions' way of reporting. SetChecks("run:failure")
// reports through check runs instead of statuses.
func (s *Server) checkRuns(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	state, forbidden, message := s.checks, s.CheckRunsForbidden, s.CheckRunsForbiddenMessage
	s.mu.Unlock()
	if message != "" {
		fail(w, 403, message)
		return
	}
	if forbidden {
		fail(w, 403, "Resource not accessible by personal access token")
		return
	}
	runs := []any{}
	if conclusion, ok := strings.CutPrefix(state, "run:"); ok {
		status := "completed"
		if conclusion == "pending" {
			status, conclusion = "in_progress", ""
		}
		runs = append(runs, map[string]any{"id": 77, "name": "e2e", "status": status, "conclusion": conclusion,
			"app":      map[string]string{"slug": "github-actions"},
			"html_url": "https://github.test/" + s.Slug + "/runs/77", "started_at": "2026-09-28T10:00:00Z",
			"completed_at": "2026-09-28T10:04:12Z"})
	}
	write(w, 200, map[string]any{"total_count": len(runs), "check_runs": runs})
}

func (s *Server) reviews(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []any{}
	n, _ := strconv.Atoi(r.PathValue("n"))
	if p := s.pulls[n]; p != nil {
		for _, rv := range p.Reviews {
			out = append(out, map[string]any{"id": rv.ID, "user": map[string]any{"login": rv.Login}, "state": rv.State,
				"body": rv.Body, "submitted_at": rv.SubmittedAt})
		}
	}
	write(w, 200, out)
}

func (s *Server) compare(w http.ResponseWriter, r *http.Request) {
	base, head, ok := strings.Cut(r.PathValue("spec"), "...")
	if !ok {
		fail(w, 404, "Not Found")
		return
	}
	out, err := s.git("diff", "--name-only", base, head)
	if err != nil {
		fail(w, 404, out)
		return
	}
	files := []any{}
	for _, f := range strings.Split(out, "\n") {
		if f != "" {
			files = append(files, map[string]string{"filename": f})
		}
	}
	// Commits on base that head lacks: how far behind head is.
	behind, _ := s.git("rev-list", "--count", head+".."+base)
	n, _ := strconv.Atoi(behind)
	write(w, 200, map[string]any{"files": files, "behind_by": n})
}

func (s *Server) updateRef(w http.ResponseWriter, r *http.Request) {
	branch := r.PathValue("branch")
	var in struct {
		SHA   string `json:"sha"`
		Force bool   `json:"force"`
	}
	_ = json.NewDecoder(r.Body).Decode(&in)
	cur := s.SHA(branch)
	if cur == "" {
		fail(w, 422, "Reference does not exist")
		return
	}
	if !in.Force {
		if _, err := s.git("merge-base", "--is-ancestor", cur, in.SHA); err != nil {
			fail(w, 422, "Update is not a fast forward")
			return
		}
	}
	if out, err := s.git("update-ref", "refs/heads/"+branch, in.SHA); err != nil {
		fail(w, 422, out)
		return
	}
	write(w, 200, map[string]any{"ref": "refs/heads/" + branch})
}

func (s *Server) createRef(w http.ResponseWriter, r *http.Request) {
	var in struct{ Ref, SHA string }
	_ = json.NewDecoder(r.Body).Decode(&in)
	if out, err := s.git("update-ref", in.Ref, in.SHA, ""); err != nil {
		fail(w, 422, "Reference already exists: "+out)
		return
	}
	write(w, 201, map[string]any{"ref": in.Ref})
}

func (s *Server) deleteRef(w http.ResponseWriter, r *http.Request) {
	if _, err := s.git("update-ref", "-d", "refs/heads/"+r.PathValue("branch")); err != nil {
		fail(w, 422, "Reference does not exist")
		return
	}
	w.WriteHeader(204)
}

func write(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func fail(w http.ResponseWriter, status int, message string) {
	write(w, status, map[string]string{"message": message})
}
