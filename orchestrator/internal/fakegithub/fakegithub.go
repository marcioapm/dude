// Package fakegithub is a stand-in for the GitHub REST endpoints dude uses,
// backed by a real bare git repository, for the orchestrator's tests.
//
// Branch updates obey GitHub's rules — a non-forced update must be a
// fast-forward — because that rule is what keeps dude from overwriting a
// person's commits, and a fake that allowed anything would hide a bug there.
package fakegithub

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/marciomartins/dude/orchestrator/internal/fakegit"
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
	// Who opened it: OpenAs for the creating token, else the poster.
	Author string
	// Logins asked for a review.
	Requested []string
	Comments  []Comment
	// Line comments, in review threads: a reply names the comment it
	// answers (InReplyTo).
	LineComments []Comment
	// Reviews submitted, oldest first: each keeps its id, as GitHub's do.
	Reviews []Review
	// GitHub refuses comments on it, as on a locked conversation.
	Locked bool
	// The repository its head is in, owner/name, when not this one (a fork).
	HeadRepo string
	Labels   []string
}

type Review struct {
	ID          int64
	Login       string
	State, Body string
	SubmittedAt string
	// The head it was submitted on.
	CommitID string
}

type Comment struct {
	ID        int64
	Author    string
	Body      string
	Path      string
	CreatedAt string
	// Set when the comment is edited, as GitHub's updated_at.
	UpdatedAt string
	InReplyTo int64
	// A line comment's line in the file; 0 for none.
	Line int
}

type Server struct {
	Repo  string // bare repository path
	Slug  string // owner/repo
	mu    sync.Mutex
	pulls map[int]*Pull
	Hooks []map[string]any
	// The combined status every commit reports; "" is success.
	checks string
	// Who the token comments as; "" is Login. A personal access token
	// comments as its owner, often a person rather than a bot.
	Poster string
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
	// The check-runs listing refuses, as for a fine-grained token.
	CheckRunsForbidden bool
	// When set, the check-runs listing answers 403 with this message and
	// no rate-limit headers, as GitHub's abuse-detection limit does.
	CheckRunsForbiddenMessage string
	// Authorization headers on POST /pulls, and an optional refusal by token.
	OpenAuth   []string
	OpenRefuse map[string]int
	// The login a bearer token opens pull requests as (a person's OAuth
	// token opens as that person); a token not named opens as poster().
	OpenAs map[string]string
	// LoseOpenResponses creates the pull request, then answers 502.
	LoseOpenResponses int
	// LoseMergeResponses makes the merge POST /merges asks for, then
	// answers 502.
	LoseMergeResponses int
	// CompareAnswer, when set, may change a compare's answer before it is
	// sent: base and head as the request named them.
	CompareAnswer     func(base, head string, answer map[string]any)
	ReceiveStatus     int
	ReceiveBody       string
	ReceiveHeaders    http.Header
	ReceiveToken      string
	ReceiveRequests   []string
	ReceiveDisconnect bool
	// Intercept, when set, sees each API request before the fake answers
	// it, without the fake's lock: it may block (a slow GitHub), or return
	// a status to fail the request with (0 answers it as usual).
	Intercept func(r *http.Request) int
	// Every review request received, refused or not, in order.
	ReviewRequests []ReviewRequest
	// When set, review requests are refused with this status.
	ReviewRequestStatus int
	// A user's GitHub type ("User", "Bot") when its login does not say.
	UserTypes map[string]string
	// Branches GitHub reports as protected (a rule or a ruleset applies).
	Protected map[string]bool
	// Review threads resolved, by thread id (ThreadID), and draft pull
	// requests marked ready, through GraphQL.
	ResolvedThreads map[string]bool
	// Review threads GitHub reports as outdated, by thread id.
	OutdatedThreads map[string]bool
	MarkedReady     []int
	// When set, the review-thread query (with comment ids) fails with it.
	ThreadsStatus int
	// Issues (reads.go), and every API request received (Requests).
	issues   map[int]*Issue
	requests []string
	// Every head a branch was moved to through the API, in order
	// (BranchLog).
	moves []move
	// Requests left of GitHub's hourly limit, by resource (core, graphql,
	// search): answers carry them as X-RateLimit headers. Unset, none.
	RateRemaining map[string]int
	// merge-tree's and diff's answers, by the commits they were asked of.
	memo memo
	// Check runs by id, as GitHub reports them (joblogs.go); one not named
	// reports a failed test in its output.
	CheckRuns map[int64]CheckRun
	// Actions job logs by id, the download host they redirect to, and a
	// status the logs endpoint answers instead (403: no permission).
	JobLogs      map[int64]string
	LogHost      string
	LogStatus    int
	downloadAuth []string
}

// NodeID is a pull request's GraphQL id.
func (s *Server) NodeID(number int) string {
	return fmt.Sprintf("PR_%s_%d", strings.ReplaceAll(s.Slug, "/", "__"), number)
}

// ThreadID is the GraphQL id of the review thread a root line comment
// opened.
func (s *Server) ThreadID(rootComment int64) string {
	return fmt.Sprintf("RT_%s_%d", strings.ReplaceAll(s.Slug, "/", "__"), rootComment)
}

// OpenAsPerson opens a pull request from head into base as a person does
// on GitHub, head branched from base with one commit; its number.
func (s *Server) OpenAsPerson(author, head, base, title string) int {
	s.Branch(head, base, "Start "+title)
	s.mu.Lock()
	defer s.mu.Unlock()
	p := &Pull{Number: len(s.pulls) + 1, Head: head, Base: base, Title: title, State: "open", Author: author}
	s.pulls[p.Number] = p
	return p.Number
}

// Branch makes branch from base with one commit of a person's on it, as
// pushing a branch to GitHub does; its SHA.
func (s *Server) Branch(branch, from, message string) string {
	if _, err := s.git("update-ref", "refs/heads/"+branch, "refs/heads/"+from, ""); err != nil {
		return ""
	}
	return s.CommitOnTop(branch, message)
}

// ReviewRequest is one POST to a pull request's requested_reviewers.
type ReviewRequest struct {
	Number int
	Logins []string
	// The request's Authorization header.
	Auth string
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
		cmd.Env = append(cmd.Environ(), "GIT_AUTHOR_NAME="+name, "GIT_AUTHOR_EMAIL="+email(name),
			"GIT_COMMITTER_NAME="+name, "GIT_COMMITTER_EMAIL="+email(name))
	}
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// SHA of a branch, or "".
func (s *Server) SHA(branch string) string {
	if r := s.repo(); r != nil {
		h, err := r.Resolve("refs/heads/" + branch)
		r.Close()
		switch {
		case err == nil:
			return h.String()
		case errors.Is(err, fakegit.ErrNotFound):
			return ""
		}
	}
	out, err := s.git("rev-parse", "--verify", "-q", "refs/heads/"+branch)
	if err != nil {
		return ""
	}
	return out
}

// CommitOnTop adds an empty commit to a branch, as a person on GitHub
// does with "Update branch" or a committed suggestion; its SHA.
func (s *Server) CommitOnTop(branch, message string) string {
	tree, _ := s.treeOf("refs/heads/" + branch)
	sha, err := s.commitTree("Alice", tree, message, "refs/heads/"+branch)
	if err != nil {
		return ""
	}
	if _, err := s.git("update-ref", "refs/heads/"+branch, sha); err != nil {
		return ""
	}
	return sha
}

// CommitFiles commits files (path → content) on top of a branch, as a
// person pushing their own work does; its SHA.
func (s *Server) CommitFiles(branch, message string, files map[string]string) string {
	cur := s.SHA(branch)
	tree, err := s.treeWith(cur, files)
	if err != nil {
		return ""
	}
	sha, err := s.commitTree("Alice", tree, message, cur)
	if err != nil {
		return ""
	}
	if _, err := s.git("update-ref", "refs/heads/"+branch, sha, cur); err != nil {
		return ""
	}
	return sha
}

// MergeInto merges from into branch on GitHub, as its Update branch button
// does: a merge commit whose first parent is the branch's head and whose
// second is from's. resolved overrides files of the merged tree, as a
// person resolving a conflict in GitHub's editor does. Its SHA, "" when
// git cannot merge them and resolved leaves the conflict.
func (s *Server) MergeInto(branch, from string, resolved map[string]string) string {
	return s.mergeAs("GitHub", branch, s.SHA(from), "Merge branch '"+from+"' into "+branch, resolved)
}

// MergeLocally is MergeInto made by a person on their own machine and
// pushed: the same parents, but not GitHub's commit.
func (s *Server) MergeLocally(branch, from string, resolved map[string]string) string {
	return s.mergeAs("Alice", branch, s.SHA(from), "Merge branch '"+from+"' into "+branch, resolved)
}

// MergeCommit merges the commit sha into branch on GitHub, as POST
// /merges does; its SHA, "" when they conflict.
func (s *Server) MergeCommit(branch, sha string) string {
	return s.mergeAs("GitHub", branch, sha, "Merge "+sha+" into "+branch, nil)
}

// MergeCommitResolved is MergeCommit with files of the merged tree
// overridden by resolved: GitHub's merge commit carrying other content.
func (s *Server) MergeCommitResolved(branch, sha string, resolved map[string]string) string {
	return s.mergeAs("GitHub", branch, sha, "Merge "+sha+" into "+branch, resolved)
}

func (s *Server) mergeAs(name, branch, other, message string, resolved map[string]string) string {
	head := s.SHA(branch)
	out, err := s.mergeTree(head, other)
	tree, _, _ := strings.Cut(out, "\n")
	if err != nil && len(resolved) == 0 {
		return ""
	}
	if tree, err = s.treeWith(tree, resolved); err != nil {
		return ""
	}
	sha, err := s.commitTree(name, tree, message, head, other)
	if err != nil {
		return ""
	}
	if _, err := s.git("update-ref", "refs/heads/"+branch, sha, head); err != nil {
		return ""
	}
	return sha
}

// treeWith is the tree of treeish with files written over it.
func (s *Server) treeWith(treeish string, files map[string]string) (string, error) {
	if r := s.repo(); r != nil {
		h, err := r.TreeWith(treeish, files)
		r.Close()
		if err == nil {
			return h.String(), nil
		}
	}
	index := s.Repo + "/index-" + strconv.FormatInt(nextID.Add(1), 10)
	defer os.Remove(index)
	env := []string{"GIT_INDEX_FILE=" + index}
	if _, err := s.gitEnv(env, "read-tree", treeish); err != nil {
		return "", err
	}
	for path, content := range files {
		blob, err := s.gitIn(content, "hash-object", "-w", "--stdin")
		if err != nil {
			return "", err
		}
		if _, err := s.gitEnv(env, "update-index", "--add", "--cacheinfo", "100644,"+blob+","+path); err != nil {
			return "", err
		}
	}
	return s.gitEnv(env, "write-tree")
}

func (s *Server) gitEnv(env []string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", s.Repo}, args...)...)
	cmd.Env = append(cmd.Environ(), env...)
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func (s *Server) gitIn(stdin string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", s.Repo}, args...)...)
	cmd.Stdin = strings.NewReader(stdin)
	out, err := cmd.Output()
	return strings.TrimSpace(string(out)), err
}

// Parents lists a commit's parents, first parent first.
func (s *Server) Parents(sha string) []string {
	if r := s.repo(); r != nil {
		c, err := r.Commit(sha)
		r.Close()
		if err == nil {
			out := []string{}
			for _, p := range c.ParentHashes {
				out = append(out, p.String())
			}
			return out
		}
	}
	out, err := s.git("rev-list", "--parents", "-n", "1", sha)
	if err != nil {
		return nil
	}
	return strings.Fields(out)[1:]
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

// Comment leaves a comment as a person would; its id.
func (s *Server) Comment(number int, author, body string) int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := nextID.Add(1)
	s.pulls[number].Comments = append(s.pulls[number].Comments,
		Comment{ID: id, Author: author, Body: body, CreatedAt: time.Now().UTC().Format(time.RFC3339)})
	return id
}

// LineComment leaves a comment on a line of a file, in a review thread of
// its own; its id.
func (s *Server) LineComment(number int, author, path, body string) int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := nextID.Add(1)
	s.pulls[number].LineComments = append(s.pulls[number].LineComments,
		Comment{ID: id, Author: author, Body: body, Path: path, CreatedAt: time.Now().UTC().Format(time.RFC3339)})
	return id
}

// EditComment changes a conversation or line comment's words, as its
// author editing it on GitHub does.
func (s *Server) EditComment(number int, id int64, body string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.pulls[number]
	for _, list := range [][]Comment{p.Comments, p.LineComments} {
		for i := range list {
			if list[i].ID == id {
				list[i].Body, list[i].UpdatedAt = body, time.Now().UTC().Format(time.RFC3339)
			}
		}
	}
}

// Login is who the token comments as: what a comment dude posts is by.
const Login = "dude-bot"

// Review records a reviewer's verdict: "APPROVED" or "CHANGES_REQUESTED".
func (s *Server) Review(number int, login, verdict string) {
	s.ReviewSaying(number, login, verdict, "")
}

// ReviewSaying submits a review with words in its box, on the pull
// request's head. As GitHub does, a submitted review satisfies the request
// for it: the reviewer leaves requested_reviewers.
func (s *Server) ReviewSaying(number int, login, verdict, body string) {
	sha := s.SHA(s.Pull(number).Head)
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.pulls[number]
	p.Reviews = append(p.Reviews, Review{ID: nextID.Add(1), Login: login, State: verdict, Body: body,
		SubmittedAt: time.Now().UTC().Format(time.RFC3339), CommitID: sha})
	p.Requested = slices.DeleteFunc(p.Requested, func(l string) bool { return strings.EqualFold(l, login) })
}

// RequestReview asks login for a review, as a person does on GitHub.
func (s *Server) RequestReview(number int, login string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pulls[number].Requested = append(s.pulls[number].Requested, login)
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

// LockConversation locks a pull request's conversation: comments on it
// are refused.
func (s *Server) LockConversation(number int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pulls[number].Locked = true
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
	mux.HandleFunc("GET "+prefix, func(w http.ResponseWriter, r *http.Request) {
		write(w, 200, map[string]any{"default_branch": "main", "full_name": s.Slug})
	})
	mux.HandleFunc("POST "+prefix+"/merges", func(w http.ResponseWriter, r *http.Request) {
		var in struct{ Base, Head string }
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			fail(w, 400, err.Error())
			return
		}
		from := s.SHA(in.Base)
		if from == "" {
			fail(w, 404, "Base not found")
			return
		}
		if s.isAncestor(in.Head, from) {
			w.WriteHeader(204)
			return
		}
		tree, err := s.mergeTree(from, in.Head)
		if err != nil {
			fail(w, 409, "Merge conflict")
			return
		}
		sha, err := s.commitTree("GitHub", strings.TrimSpace(tree), "Merge branch '"+in.Head+"' into "+in.Base, from, in.Head)
		if err == nil {
			_, err = s.git("update-ref", "refs/heads/"+in.Base, sha, from)
		}
		if err != nil {
			fail(w, 409, "Branch moved")
			return
		}
		s.moved(in.Base, sha)
		s.mu.Lock()
		lost := s.LoseMergeResponses > 0
		if lost {
			s.LoseMergeResponses--
		}
		s.mu.Unlock()
		if lost {
			fail(w, 502, "response lost")
			return
		}
		// As GitHub answers: the merge commit, with its parents.
		parents := []any{}
		for _, p := range s.Parents(sha) {
			parents = append(parents, map[string]string{"sha": p})
		}
		write(w, 201, map[string]any{"sha": sha, "parents": parents})
	})
	mux.HandleFunc("POST "+prefix+"/pulls", s.openPull)
	mux.HandleFunc("GET "+prefix+"/pulls", func(w http.ResponseWriter, r *http.Request) {
		owner, _, _ := strings.Cut(s.Slug, "/")
		head := strings.TrimPrefix(r.URL.Query().Get("head"), owner+":")
		s.mu.Lock()
		defer s.mu.Unlock()
		out := []map[string]any{}
		for _, p := range s.pulls {
			if p.State == "open" && p.Head == head && (r.URL.Query().Get("base") == "" || p.Base == r.URL.Query().Get("base")) {
				out = append(out, s.pullJSON(p))
			}
		}
		write(w, 200, out)
	})
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
	mux.HandleFunc("GET "+prefix+"/pulls/{n}/comments", s.lineComments)
	mux.HandleFunc("POST "+prefix+"/pulls/{n}/comments/{id}/replies", s.postComment)
	mux.HandleFunc("PATCH "+prefix+"/issues/{n}", func(w http.ResponseWriter, r *http.Request) {
		p := s.number(w, r)
		if p == nil {
			return
		}
		var in struct{ Body string }
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			fail(w, 400, err.Error())
			return
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		p.Body = in.Body
		write(w, 200, s.pullJSON(p))
	})
	mux.HandleFunc("GET "+prefix+"/issues/{n}/comments", func(w http.ResponseWriter, r *http.Request) {
		if !s.issueComments(w, r) {
			s.comments(w, r)
		}
	})
	mux.HandleFunc("GET "+prefix+"/issues/{n}", s.getIssue)
	mux.HandleFunc("GET "+prefix+"/pulls/{n}/files", s.pullFiles)
	mux.HandleFunc("POST "+prefix+"/issues/{n}/comments", s.postComment)
	mux.HandleFunc("PATCH "+prefix+"/issues/comments/{id}", s.patchComment)
	// Who the token is: the account its comments are by.
	mux.HandleFunc("GET /user", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		write(w, 200, map[string]any{"login": s.poster()})
	})
	mux.HandleFunc("GET "+prefix+"/commits/{sha}/status", s.status)
	mux.HandleFunc("GET "+prefix+"/commits/{sha}/check-runs", s.checkRuns)
	mux.HandleFunc("GET "+prefix+"/compare/{spec...}", s.compare)
	mux.HandleFunc("PATCH "+prefix+"/git/refs/heads/{branch...}", s.updateRef)
	mux.HandleFunc("GET "+prefix+"/git/ref/heads/{branch...}", func(w http.ResponseWriter, r *http.Request) {
		sha := s.SHA(r.PathValue("branch"))
		if sha == "" {
			fail(w, 404, "Not Found")
			return
		}
		write(w, 200, map[string]any{"ref": "refs/heads/" + r.PathValue("branch"), "object": map[string]string{"sha": sha}})
	})
	mux.HandleFunc("POST "+prefix+"/git/refs", s.createRef)
	mux.HandleFunc("GET "+prefix+"/branches", func(w http.ResponseWriter, r *http.Request) {
		out, _ := s.git("for-each-ref", "--format=%(refname:short)", "refs/heads")
		names := []map[string]any{}
		for _, n := range strings.Fields(out) {
			names = append(names, map[string]any{"name": n})
		}
		write(w, 200, page(r, names))
	})
	mux.HandleFunc("GET "+prefix+"/branches/{branch...}", func(w http.ResponseWriter, r *http.Request) {
		branch := r.PathValue("branch")
		sha := s.SHA(branch)
		if sha == "" {
			fail(w, 404, "Branch not found")
			return
		}
		s.mu.Lock()
		protected := s.Protected[branch]
		s.mu.Unlock()
		write(w, 200, map[string]any{"name": branch, "protected": protected, "commit": map[string]string{"sha": sha}})
	})
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
		out, err := s.authorOf(r.PathValue("sha"))
		if err != nil {
			fail(w, 404, "No commit found")
			return
		}
		committer := map[string]string{"name": "Alice"}
		verified := false
		if out == "GitHub" {
			committer = map[string]string{"name": "GitHub"}
			verified = true
		}
		write(w, 200, map[string]any{"author": nil, "committer": map[string]string{"login": map[bool]string{true: "web-flow", false: ""}[verified]}, "commit": map[string]any{
			"author": map[string]string{"name": out}, "committer": committer,
			"verification": map[string]bool{"verified": verified}}})
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
		var in struct {
			ExpectedHeadSHA string `json:"expected_head_sha"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			fail(w, 400, err.Error())
			return
		}
		head := s.SHA(p.Head)
		if in.ExpectedHeadSHA != "" && in.ExpectedHeadSHA != head {
			fail(w, 422, "Head branch was modified. Review and try again.")
			return
		}
		base := s.SHA(p.Base)
		tree, err := s.mergeTree(head, base)
		if err != nil {
			fail(w, 422, "merge conflict between base and head")
			return
		}
		// The old head guards the ref update even if it moves during the merge.
		sha, err := s.commitTree("GitHub", tree, "Merge branch '"+p.Base+"' into "+p.Head, head, base)
		if err == nil {
			_, err = s.git("update-ref", "refs/heads/"+p.Head, sha, head)
		}
		if err != nil {
			fail(w, 422, sha)
			return
		}
		s.moved(p.Head, sha)
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
			s.ReviewRequests = append(s.ReviewRequests, ReviewRequest{Number: p.Number, Logins: in.Reviewers,
				Auth: r.Header.Get("Authorization")})
			if s.ReviewRequestStatus != 0 {
				fail(w, s.ReviewRequestStatus, "Validation Failed")
				return
			}
			// As GitHub: one login it will not ask refuses the whole request.
			for _, l := range in.Reviewers {
				switch {
				case strings.EqualFold(l, p.Author):
					fail(w, 422, "Review cannot be requested from pull request author.")
					return
				case s.Permissions[l] == "none" || strings.HasSuffix(l, "[bot]"):
					fail(w, 422, "Reviews may only be requested from collaborators. One or more of the users or teams you specified is not a collaborator of the "+s.Slug+" repository.")
					return
				}
			}
			for _, l := range in.Reviewers {
				if !slices.Contains(p.Requested, l) {
					p.Requested = append(p.Requested, l)
				}
			}
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
		// As GitHub: the job re-run is queued again, not failed.
		if strings.HasPrefix(s.checks, "run:") {
			s.checks = "run:pending"
		}
		s.mu.Unlock()
		write(w, 201, map[string]any{})
	})
	mux.HandleFunc("GET "+prefix+"/actions/jobs/{id}/logs", s.jobLogs)
	mux.HandleFunc("GET "+prefix+"/check-runs/{id}", s.checkRun)
	mux.HandleFunc("GET "+prefix+"/check-runs/{id}/annotations", s.checkRunAnnotations)
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
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.logRequest(r)
		s.mu.Lock()
		intercept := s.Intercept
		s.rateHeaders(w, "core")
		s.mu.Unlock()
		if intercept != nil {
			if status := intercept(r); status != 0 {
				fail(w, status, http.StatusText(status))
				return
			}
		}
		mux.ServeHTTP(w, r)
	})
}

func (s *Server) openPull(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Title, Body, Head, Base string
		Draft                   bool
	}
	_ = json.NewDecoder(r.Body).Decode(&in)
	s.mu.Lock()
	auth := r.Header.Get("Authorization")
	s.OpenAuth = append(s.OpenAuth, auth)
	refused := s.OpenRefuse[strings.TrimPrefix(auth, "Bearer ")]
	s.mu.Unlock()
	if refused != 0 {
		fail(w, refused, "Resource not accessible by integration")
		return
	}
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
	author := s.OpenAs[strings.TrimPrefix(auth, "Bearer ")]
	if author == "" {
		author = s.poster()
	}
	p := &Pull{Number: len(s.pulls) + 1, Head: in.Head, Base: in.Base, Title: in.Title, Body: in.Body, State: "open", Draft: in.Draft,
		Author: author}
	s.pulls[p.Number] = p
	out := s.pullJSON(p)
	lost := s.LoseOpenResponses > 0
	if lost {
		s.LoseOpenResponses--
	}
	s.mu.Unlock()
	if lost {
		fail(w, http.StatusBadGateway, "response lost")
		return
	}
	write(w, 201, out)
}

func (s *Server) pullJSON(p *Pull) map[string]any {
	requested := []any{}
	for _, l := range p.Requested {
		requested = append(requested, map[string]string{"login": l})
	}
	var mergeable any = true
	state := "clean"
	if _, err := s.mergeTree(p.Head, p.Base); err != nil {
		mergeable, state = false, "dirty"
	}
	if s.Conflicting[p.Number] {
		mergeable, state = false, "dirty"
	}
	if s.MergeableUnknown[p.Number] {
		mergeable, state = nil, "unknown"
	}
	headRepo := s.Slug
	if p.HeadRepo != "" {
		headRepo = p.HeadRepo
	}
	return map[string]any{
		"number": p.Number, "node_id": s.NodeID(p.Number),
		"html_url": fmt.Sprintf("https://github.test/%s/pull/%d", s.Slug, p.Number),
		"draft":    p.Draft, "state": p.State, "merged_at": p.MergedAt, "title": p.Title, "body": p.Body,
		"head":      map[string]any{"sha": s.SHA(p.Head), "ref": p.Head, "repo": map[string]string{"full_name": headRepo}},
		"base":      map[string]any{"ref": p.Base, "repo": map[string]string{"full_name": s.Slug}},
		"mergeable": mergeable, "mergeable_state": state, "requested_reviewers": requested,
		"user": s.userJSON(p.Author), "labels": labelsJSON(p.Labels),
	}
}

// userJSON is a GitHub user object: of type Bot for an App's account
// ("<slug>[bot]") or a login UserTypes names so, else User.
func (s *Server) userJSON(login string) map[string]any {
	kind := "User"
	if strings.HasSuffix(login, "[bot]") {
		kind = "Bot"
	}
	if t := s.UserTypes[login]; t != "" {
		kind = t
	}
	return map[string]any{"login": login, "type": kind}
}

// reviewers is who the fake says can review: the people dude's reviewer
// picker finds. The first two are GitHub's suggestions. It has no teams.
var reviewers = []map[string]any{
	{"login": "ana", "name": "Ana Ribeiro", "avatarUrl": ""},
	{"login": "tom", "name": "Tom Okafor", "avatarUrl": ""},
	{"login": "hanna", "name": "Hanna Lindqvist", "avatarUrl": ""},
}

// Graphql answers the queries dude sends — a pull request's review
// threads, Unresolved of them unresolved, and who can review it — for
// whichever of servers holds the repository the query names.
func Graphql(servers ...*Server) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Query     string `json:"query"`
			Variables struct {
				Owner, Name string
				Number      int
				Q           string
				ID          string
			} `json:"variables"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		// The owner's teams: none, as for a repository a user owns.
		if strings.Contains(in.Query, "teams(") {
			write(w, 200, map[string]any{"data": map[string]any{"organization": nil},
				"errors": []any{map[string]string{"type": "NOT_FOUND", "message": "Could not resolve to an Organization"}}})
			return
		}
		// By node id: a review thread, or a pull request to mark ready.
		if in.Variables.ID != "" {
			for _, s := range servers {
				if out, ok := s.byNode(in.Query, in.Variables.ID); ok {
					write(w, 200, out)
					return
				}
			}
			write(w, 200, map[string]any{"data": map[string]any{"node": nil},
				"errors": []any{map[string]string{"type": "NOT_FOUND", "message": "Could not resolve to a node with the global id of '" + in.Variables.ID + "'"}}})
			return
		}
		for _, s := range servers {
			if s.Slug != in.Variables.Owner+"/"+in.Variables.Name {
				continue
			}
			s.logRequest(r)
			s.mu.Lock()
			s.rateHeaders(w, "graphql")
			s.mu.Unlock()
			if strings.Contains(in.Query, "totalCount") {
				write(w, 200, s.threadsRead(in.Variables.Number))
				return
			}
			if strings.Contains(in.Query, "suggestedReviewers") {
				write(w, 200, s.reviewerCandidates(in.Variables.Number, in.Variables.Q))
				return
			}
			if strings.Contains(in.Query, "databaseId") {
				s.mu.Lock()
				fail := s.ThreadsStatus
				s.mu.Unlock()
				if fail != 0 {
					write(w, fail, map[string]any{"message": http.StatusText(fail)})
					return
				}
				write(w, 200, s.threads(in.Variables.Number))
				return
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

// threads is pull request number's review threads with their comments'
// ids: one per root line comment, its replies in it.
func (s *Server) threads(number int) map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	nodes := []any{}
	if p := s.pulls[number]; p != nil {
		for _, c := range p.LineComments {
			if c.InReplyTo != 0 {
				continue
			}
			ids := []any{map[string]any{"databaseId": c.ID}}
			for _, r := range p.LineComments {
				if r.InReplyTo == c.ID {
					ids = append(ids, map[string]any{"databaseId": r.ID})
				}
			}
			id := s.ThreadID(c.ID)
			nodes = append(nodes, map[string]any{"id": id, "isResolved": s.ResolvedThreads[id], "isOutdated": s.OutdatedThreads[id],
				"comments": map[string]any{"nodes": ids}})
		}
	}
	return map[string]any{"data": map[string]any{"repository": map[string]any{"pullRequest": map[string]any{
		"reviewThreads": map[string]any{"nodes": nodes, "pageInfo": map[string]any{"hasNextPage": false}}}}}}
}

// byNode answers a query or mutation by node id when the node is one of
// this repository's: a review thread (read or resolved) or a pull request
// (marked ready). Resolving a thread takes one off Unresolved, as GitHub's
// count would.
func (s *Server) byNode(query, id string) (map[string]any, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, p := range s.pulls {
		if strings.Contains(query, "markPullRequestReadyForReview") && id == s.NodeID(p.Number) {
			p.Draft = false
			s.MarkedReady = append(s.MarkedReady, p.Number)
			return map[string]any{"data": map[string]any{"markPullRequestReadyForReview": map[string]any{
				"pullRequest": map[string]any{"id": id, "isDraft": false}}}}, true
		}
		for _, c := range p.LineComments {
			if c.InReplyTo != 0 || id != s.ThreadID(c.ID) {
				continue
			}
			if strings.Contains(query, "resolveReviewThread") {
				if s.ResolvedThreads == nil {
					s.ResolvedThreads = map[string]bool{}
				}
				if !s.ResolvedThreads[id] && s.Unresolved[p.Number] > 0 {
					s.Unresolved[p.Number]--
				}
				s.ResolvedThreads[id] = true
				return map[string]any{"data": map[string]any{"resolveReviewThread": map[string]any{
					"thread": map[string]any{"id": id, "isResolved": true}}}}, true
			}
			comments := []any{map[string]any{"databaseId": c.ID}}
			for _, r := range p.LineComments {
				if r.InReplyTo == c.ID {
					comments = append(comments, map[string]any{"databaseId": r.ID})
				}
			}
			return map[string]any{"data": map[string]any{"node": map[string]any{"id": id, "isResolved": s.ResolvedThreads[id],
				"pullRequest": map[string]any{"number": p.Number, "repository": map[string]any{"nameWithOwner": s.Slug}},
				"comments":    map[string]any{"nodes": comments}}}}, true
		}
	}
	return nil, false
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
	write(w, 200, linkedPage(w, r, s.commentsJSON(p, p.Comments, "issuecomment")))
}

func (s *Server) lineComments(w http.ResponseWriter, r *http.Request) {
	p := s.number(w, r)
	if p == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	write(w, 200, s.commentsJSON(p, p.LineComments, "discussion_r"))
}

func (s *Server) commentsJSON(p *Pull, comments []Comment, anchor string) []any {
	out := []any{}
	for _, c := range comments {
		m := map[string]any{"id": c.ID, "body": c.Body, "created_at": c.CreatedAt, "user": map[string]string{"login": c.Author},
			"html_url": fmt.Sprintf("https://github.test/%s/pull/%d#%s-%d", s.Slug, p.Number, anchor, c.ID)}
		m["updated_at"] = c.CreatedAt
		if c.UpdatedAt != "" {
			m["updated_at"] = c.UpdatedAt
		}
		if c.Path != "" {
			m["path"] = c.Path
		}
		if c.InReplyTo != 0 {
			m["in_reply_to_id"] = c.InReplyTo
		}
		out = append(out, m)
	}
	return out
}

// poster is who the token's comments are by. The caller holds s.mu.
func (s *Server) poster() string {
	if s.Poster != "" {
		return s.Poster
	}
	return Login
}

// postComment is a comment the token posts, as Login: on a pull request's
// conversation, or (with {id}) a reply in that line comment's thread. A
// locked pull request is refused, as GitHub does.
func (s *Server) postComment(w http.ResponseWriter, r *http.Request) {
	p := s.number(w, r)
	if p == nil {
		return
	}
	var in struct {
		Body string `json:"body"`
	}
	_ = json.NewDecoder(r.Body).Decode(&in)
	s.mu.Lock()
	defer s.mu.Unlock()
	if p.Locked {
		fail(w, 403, "Unable to create comment because issue is locked.")
		return
	}
	c := Comment{Author: s.poster(), Body: in.Body, CreatedAt: time.Now().UTC().Format(time.RFC3339)}
	if r.PathValue("id") == "" {
		c.ID = nextID.Add(1)
		p.Comments = append(p.Comments, c)
		write(w, 201, s.commentsJSON(p, []Comment{c}, "issuecomment")[0])
		return
	}
	c.InReplyTo, _ = strconv.ParseInt(r.PathValue("id"), 10, 64)
	var parent *Comment
	for i := range p.LineComments {
		if p.LineComments[i].ID == c.InReplyTo {
			parent = &p.LineComments[i]
		}
	}
	if parent == nil {
		fail(w, 404, "Not Found")
		return
	}
	c.ID, c.Path = nextID.Add(1), parent.Path
	p.LineComments = append(p.LineComments, c)
	write(w, 201, s.commentsJSON(p, []Comment{c}, "discussion_r")[0])
}

// patchComment edits a conversation comment by its id, wherever it is, as
// GitHub's issue comment endpoint does: 404 for one that is not there (or
// was deleted), 403 when only its author may and the token is not.
func (s *Server) patchComment(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	var in struct {
		Body string `json:"body"`
	}
	_ = json.NewDecoder(r.Body).Decode(&in)
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, p := range s.pulls {
		for i := range p.Comments {
			c := &p.Comments[i]
			if c.ID != id {
				continue
			}
			if c.Author != s.poster() {
				fail(w, 403, "Resource not accessible by personal access token")
				return
			}
			c.Body, c.UpdatedAt = in.Body, time.Now().UTC().Format(time.RFC3339)
			write(w, 200, s.commentsJSON(p, []Comment{*c}, "issuecomment")[0])
			return
		}
	}
	fail(w, 404, "Not Found")
}

// DeleteComment removes a conversation comment, as a person deleting it on
// GitHub does.
func (s *Server) DeleteComment(number int, id int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.pulls[number]
	p.Comments = slices.DeleteFunc(p.Comments, func(c Comment) bool { return c.ID == id })
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
			out = append(out, map[string]any{"id": rv.ID, "user": s.userJSON(rv.Login), "state": rv.State,
				"body": rv.Body, "submitted_at": rv.SubmittedAt, "commit_id": rv.CommitID})
		}
	}
	write(w, 200, page(r, out))
}

// page is one page of a list, as GitHub pages it: `per_page` clamped to
// 1..100, 30 when absent or invalid; `page` from 1.
func page[T any](r *http.Request, all []T) []T {
	size, n := pageOf(r)
	start := min((n-1)*size, len(all))
	return all[start:min(start+size, len(all))]
}

func pageOf(r *http.Request) (size, n int) {
	size, err := strconv.Atoi(r.URL.Query().Get("per_page"))
	if err != nil || size < 1 {
		size = 30
	}
	size = min(size, 100)
	n, err = strconv.Atoi(r.URL.Query().Get("page"))
	if err != nil || n < 1 {
		n = 1
	}
	return size, n
}

// linkedPage is page that also sets GitHub's Link header when the list has
// more than one page: prev/first before the page, next/last after it.
func linkedPage[T any](w http.ResponseWriter, r *http.Request, all []T) []T {
	size, n := pageOf(r)
	if last := max(1, (len(all)+size-1)/size); last > 1 {
		at := func(p int, rel string) string {
			q := r.URL.Query()
			q.Set("per_page", strconv.Itoa(size))
			q.Set("page", strconv.Itoa(p))
			return fmt.Sprintf(`<http://%s%s?%s>; rel="%s"`, r.Host, r.URL.Path, q.Encode(), rel)
		}
		var links []string
		if n > 1 {
			links = append(links, at(n-1, "prev"), at(1, "first"))
		}
		if n < last {
			links = append(links, at(n+1, "next"), at(last, "last"))
		}
		if links != nil {
			w.Header().Set("Link", strings.Join(links, ", "))
		}
	}
	return page(r, all)
}

// compareFileCap is the most files GitHub's compare lists
// (forge.CompareFileCap; forge's tests import this package).
const compareFileCap = 300

func (s *Server) compare(w http.ResponseWriter, r *http.Request) {
	base, head, ok := strings.Cut(r.PathValue("spec"), "...")
	if !ok {
		fail(w, 404, "Not Found")
		return
	}
	// As GitHub's: from the merge base (three dots), renames and copies
	// found, at most forge.CompareFileCap files, each with its status and
	// blob sha (the old one for a file removed), and a patch only where
	// lines changed and git can show them: none for a binary file, nor for
	// one whose content did not change.
	repo := s.repo()
	if repo != nil {
		defer repo.Close()
	}
	out, err := s.diff(repo, base, head, "-z", "-M", "-C", "--numstat")
	if err != nil {
		fail(w, 404, out)
		return
	}
	raw, _ := s.diff(repo, base, head, "-z", "-M", "-C", "--raw", "--no-abbrev")
	status, sha := map[string]string{}, map[string]string{}
	for rt := strings.Split(raw, "\x00"); len(rt) > 1; {
		// ":<mode> <mode> <old sha> <new sha> <status>", then its paths.
		f := strings.Fields(rt[0])
		if len(f) != 5 {
			break
		}
		n := 2
		if f[4][0] == 'R' || f[4][0] == 'C' {
			n = 3
		}
		if len(rt) < n {
			break
		}
		path := rt[n-1]
		status[path] = map[byte]string{'A': "added", 'D': "removed", 'M': "modified", 'R': "renamed", 'C': "copied", 'T': "changed"}[f[4][0]]
		sha[path] = f[3]
		if f[4][0] == 'D' {
			sha[path] = f[2]
		}
		rt = rt[n:]
	}
	files := []any{}
	for tokens := strings.Split(out, "\x00"); len(tokens) > 0 && tokens[0] != ""; {
		// "<added>\t<deleted>\t<path>", or an empty path and a rename's two
		// paths next; "-" for a binary file's counts.
		parts := strings.SplitN(tokens[0], "\t", 3)
		if len(parts) != 3 {
			break
		}
		path, prev := parts[2], ""
		tokens = tokens[1:]
		if path == "" && len(tokens) >= 2 {
			prev, path, tokens = tokens[0], tokens[1], tokens[2:]
		}
		added, _ := strconv.Atoi(parts[0])
		deleted, _ := strconv.Atoi(parts[1])
		f := map[string]any{"filename": path, "status": status[path], "sha": sha[path], "additions": added, "deletions": deleted}
		if prev != "" {
			f["previous_filename"] = prev
		}
		if added+deleted > 0 {
			f["patch"] = fmt.Sprintf("@@ -1,%d +1,%d @@", deleted, added)
		}
		if len(files) < compareFileCap {
			files = append(files, f)
		}
	}
	// Commits on base that head lacks: how far behind head is; and the
	// reverse.
	behind := s.count(head, base)
	ahead := s.count(base, head)
	n, _ := strconv.Atoi(behind)
	a, _ := strconv.Atoi(ahead)
	// Head's commits base lacks, oldest first, each with its parents: at
	// most compareCommitCap, as GitHub lists them.
	commits := []any{}
	if out := s.revList(base, head); out != "" {
		for _, line := range strings.Split(out, "\n") {
			f := strings.Fields(line)
			parents := []any{}
			for _, p := range f[1:] {
				parents = append(parents, map[string]string{"sha": p})
			}
			if len(commits) < compareCommitCap {
				commits = append(commits, map[string]any{"sha": f[0], "parents": parents})
			}
		}
	}
	mergeBase, _ := s.git("merge-base", base, head)
	answer := map[string]any{"files": files, "behind_by": n, "ahead_by": a, "commits": commits, "total_commits": a,
		"merge_base_commit": map[string]any{"sha": mergeBase}}
	s.mu.Lock()
	rewrite := s.CompareAnswer
	s.mu.Unlock()
	if rewrite != nil {
		rewrite(base, head, answer)
	}
	write(w, 200, answer)
}

// compareCommitCap is the most commits GitHub's compare lists
// (forge.CompareCommitCap).
const compareCommitCap = 250

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
		if !s.isAncestor(cur, in.SHA) {
			fail(w, 422, "Update is not a fast forward")
			return
		}
	}
	// Only from the commit judged: a branch moved since is refused, as
	// GitHub's update is atomic.
	if _, err := s.git("update-ref", "refs/heads/"+branch, in.SHA, cur); err != nil {
		fail(w, 422, "Update is not a fast forward")
		return
	}
	s.moved(branch, in.SHA)
	write(w, 200, map[string]any{"ref": "refs/heads/" + branch})
}

func (s *Server) createRef(w http.ResponseWriter, r *http.Request) {
	var in struct{ Ref, SHA string }
	_ = json.NewDecoder(r.Body).Decode(&in)
	if out, err := s.git("update-ref", in.Ref, in.SHA, ""); err != nil {
		fail(w, 422, "Reference already exists: "+out)
		return
	}
	s.moved(strings.TrimPrefix(in.Ref, "refs/heads/"), in.SHA)
	write(w, 201, map[string]any{"ref": in.Ref})
}

type move struct{ branch, sha string }

// moved records that the API moved branch to sha.
func (s *Server) moved(branch, sha string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.moves = append(s.moves, move{branch, sha})
}

// BranchLog is every head the API moved branch to (a fast-forward, a
// create, a merge into it, an update-branch), oldest first: what anyone
// reading the branch on GitHub could have seen it at. Git pushes through
// the smart HTTP path are not recorded.
func (s *Server) BranchLog(branch string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, m := range s.moves {
		if m.branch == branch {
			out = append(out, m.sha)
		}
	}
	return out
}

func (s *Server) DeleteBranch(branch string) error {
	_, err := s.git("update-ref", "-d", "refs/heads/"+branch)
	return err
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

// reviewerCandidates answers as GitHub does: suggestions only without
// words (the query's @include(if:$suggest)), people matching them with.
func (s *Server) reviewerCandidates(number int, q string) map[string]any {
	suggested := []any{}
	if q == "" {
		for i, u := range reviewers[:2] {
			suggested = append(suggested, map[string]any{"isCommenter": i == 1, "reviewer": u})
		}
	}
	found := []any{}
	for _, u := range reviewers {
		if strings.Contains(strings.ToLower(u["login"].(string)+" "+u["name"].(string)), strings.ToLower(q)) {
			found = append(found, u)
		}
	}
	return map[string]any{"data": map[string]any{
		"repository": map[string]any{
			"pullRequest":     map[string]any{"author": map[string]any{"login": "dude-bot"}, "suggestedReviewers": suggested},
			"assignableUsers": map[string]any{"nodes": found},
		},
	}}
}
