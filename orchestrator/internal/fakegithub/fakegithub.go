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
	"time"
)

type Pull struct {
	Number   int
	Head     string
	Base     string
	Title    string
	Body     string
	State    string
	MergedAt *string
	Comments []Comment
	Checks   string
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
	next  int64
	Hooks []map[string]any
}

func New(repo, slug string) *Server {
	return &Server{Repo: repo, Slug: slug, pulls: map[int]*Pull{}, next: 1000}
}

func (s *Server) git(args ...string) (string, error) {
	out, err := exec.Command("git", append([]string{"-C", s.Repo}, args...)...).CombinedOutput()
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
	s.next++
	s.pulls[number].Comments = append(s.pulls[number].Comments,
		Comment{ID: s.next, Author: author, Body: body, CreatedAt: time.Now().UTC().Format(time.RFC3339)})
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
	mux.HandleFunc("POST "+prefix+"/pulls", s.openPull)
	mux.HandleFunc("GET "+prefix+"/pulls/{n}", s.getPull)
	mux.HandleFunc("GET "+prefix+"/pulls/{n}/reviews", func(w http.ResponseWriter, r *http.Request) { write(w, 200, []any{}) })
	mux.HandleFunc("GET "+prefix+"/pulls/{n}/comments", func(w http.ResponseWriter, r *http.Request) { write(w, 200, []any{}) })
	mux.HandleFunc("GET "+prefix+"/issues/{n}/comments", s.comments)
	mux.HandleFunc("GET "+prefix+"/commits/{sha}/status", s.status)
	mux.HandleFunc("GET "+prefix+"/compare/{spec}", s.compare)
	mux.HandleFunc("PATCH "+prefix+"/git/refs/heads/{branch...}", s.updateRef)
	mux.HandleFunc("POST "+prefix+"/git/refs", s.createRef)
	mux.HandleFunc("DELETE "+prefix+"/git/refs/heads/{branch...}", s.deleteRef)
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
	var in struct{ Title, Body, Head, Base string }
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
	p := &Pull{Number: len(s.pulls) + 1, Head: in.Head, Base: in.Base, Title: in.Title, Body: in.Body, State: "open", Checks: "success"}
	s.pulls[p.Number] = p
	s.mu.Unlock()
	write(w, 201, s.pullJSON(p))
}

func (s *Server) pullJSON(p *Pull) map[string]any {
	return map[string]any{
		"number": p.Number, "node_id": fmt.Sprintf("PR_%d", p.Number),
		"html_url": fmt.Sprintf("https://github.test/%s/pull/%d", s.Slug, p.Number),
		"draft":    false, "state": p.State, "merged_at": p.MergedAt,
		"head": map[string]string{"sha": s.SHA(p.Head)},
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
	write(w, 200, map[string]any{"state": "success", "total_count": 1})
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
	write(w, 200, map[string]any{"files": files})
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
