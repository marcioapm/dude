package fakegithub

import (
	"fmt"
	"net/http"
	"os"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
)

// What an agent reads through dude: issues, search, a pull request's files
// and diff, its review threads with their words. Issues are numbered from
// 101, apart from pull requests (numbered from 1), so a test's numbers do
// not depend on the order it opens them in.

type Issue struct {
	Number   int
	Title    string
	Body     string
	Author   string
	State    string
	Labels   []string
	Comments []Comment
}

// OpenIssue opens an issue as a person does on GitHub; its number.
func (s *Server) OpenIssue(author, title, body string, labels ...string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.issues == nil {
		s.issues = map[int]*Issue{}
	}
	i := &Issue{Number: 101 + len(s.issues), Title: title, Body: body, Author: author, State: "open", Labels: labels}
	s.issues[i.Number] = i
	return i.Number
}

// IssueComment comments on an issue as a person does; its id.
func (s *Server) IssueComment(number int, author, body string) int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := nextID.Add(1)
	s.issues[number].Comments = append(s.issues[number].Comments,
		Comment{ID: id, Author: author, Body: body, CreatedAt: time.Now().UTC().Format(time.RFC3339)})
	return id
}

// LineCommentAt is LineComment on a line of the file; its id.
func (s *Server) LineCommentAt(number int, author, path string, line int, body string) int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := nextID.Add(1)
	s.pulls[number].LineComments = append(s.pulls[number].LineComments,
		Comment{ID: id, Author: author, Body: body, Path: path, Line: line, CreatedAt: time.Now().UTC().Format(time.RFC3339)})
	return id
}

// WriteFile commits a file to a branch, as a person pushing does; its SHA.
func (s *Server) WriteFile(branch, path, content, message string) string {
	blob, err := s.gitWith(content, nil, "hash-object", "-w", "--stdin")
	if err != nil {
		return ""
	}
	index := s.Repo + "/fake-index-" + strconv.FormatInt(nextID.Add(1), 10)
	defer os.Remove(index)
	env := []string{"GIT_INDEX_FILE=" + index}
	if _, err := s.gitWith("", env, "read-tree", "refs/heads/"+branch); err != nil {
		return ""
	}
	if _, err := s.gitWith("", env, "update-index", "--add", "--cacheinfo", "100644,"+blob+","+path); err != nil {
		return ""
	}
	tree, err := s.gitWith("", env, "write-tree")
	if err != nil {
		return ""
	}
	sha, err := s.gitAs("Alice", "commit-tree", tree, "-p", "refs/heads/"+branch, "-m", message)
	if err != nil {
		return ""
	}
	if _, err := s.git("update-ref", "refs/heads/"+branch, sha); err != nil {
		return ""
	}
	return sha
}

// Requests are the API requests the fake was sent, "METHOD path", in order:
// its own routes, and the search and GraphQL queries naming its repository.
func (s *Server) Requests() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.requests)
}

func (s *Server) logRequest(r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requests = append(s.requests, r.Method+" "+r.URL.Path)
}

// rateHeaders answers with the X-RateLimit headers of resource, when the
// test set RateRemaining for it. Callers hold s.mu.
func (s *Server) rateHeaders(w http.ResponseWriter, resource string) {
	remaining, ok := s.RateRemaining[resource]
	if !ok {
		return
	}
	limit := 5000
	if resource == "search" {
		limit = 30
	}
	h := w.Header()
	h.Set("X-RateLimit-Limit", strconv.Itoa(limit))
	h.Set("X-RateLimit-Remaining", strconv.Itoa(remaining))
	h.Set("X-RateLimit-Reset", strconv.FormatInt(time.Now().Add(30*time.Minute).Unix(), 10))
	h.Set("X-RateLimit-Resource", resource)
}

func (s *Server) issueURL(number int, pull bool) string {
	kind := "issues"
	if pull {
		kind = "pull"
	}
	return fmt.Sprintf("https://github.test/%s/%s/%d", s.Slug, kind, number)
}

func labelsJSON(labels []string) []any {
	out := []any{}
	for _, l := range labels {
		out = append(out, map[string]string{"name": l})
	}
	return out
}

// issueJSON is an issue as GitHub's issues and search APIs give it; a pull
// request's carries pull_request. Callers hold s.mu.
func (s *Server) issueJSON(host string, i *Issue, p *Pull) map[string]any {
	out := map[string]any{"repository_url": "http://" + host + "/repos/" + s.Slug,
		"updated_at": time.Now().UTC().Format(time.RFC3339)}
	if p != nil {
		out["number"], out["title"], out["body"], out["state"] = p.Number, p.Title, p.Body, p.State
		out["user"], out["labels"], out["comments"] = s.userJSON(p.Author), labelsJSON(p.Labels), len(p.Comments)
		out["html_url"], out["draft"] = s.issueURL(p.Number, true), p.Draft
		out["pull_request"] = map[string]any{"merged_at": p.MergedAt}
		return out
	}
	out["number"], out["title"], out["body"], out["state"] = i.Number, i.Title, i.Body, i.State
	out["user"], out["labels"], out["comments"] = s.userJSON(i.Author), labelsJSON(i.Labels), len(i.Comments)
	out["html_url"] = s.issueURL(i.Number, false)
	return out
}

// getIssue is GET /repos/{slug}/issues/{n}: an issue, or a pull request as
// the issues API sees it.
func (s *Server) getIssue(w http.ResponseWriter, r *http.Request) {
	n, _ := strconv.Atoi(r.PathValue("n"))
	s.mu.Lock()
	defer s.mu.Unlock()
	if p := s.pulls[n]; p != nil {
		write(w, 200, s.issueJSON(r.Host, nil, p))
		return
	}
	if i := s.issues[n]; i != nil {
		write(w, 200, s.issueJSON(r.Host, i, nil))
		return
	}
	fail(w, 404, "Not Found")
}

// issueComments answers an issue's comments, paged as GitHub does; false
// when number is no issue of the fake's (a pull request's are elsewhere).
func (s *Server) issueComments(w http.ResponseWriter, r *http.Request) bool {
	n, _ := strconv.Atoi(r.PathValue("n"))
	s.mu.Lock()
	defer s.mu.Unlock()
	i := s.issues[n]
	if i == nil {
		return false
	}
	out := []any{}
	for _, c := range i.Comments {
		out = append(out, map[string]any{"id": c.ID, "body": c.Body, "created_at": c.CreatedAt, "updated_at": c.CreatedAt,
			"user": map[string]string{"login": c.Author}, "html_url": fmt.Sprintf("%s#issuecomment-%d", s.issueURL(n, false), c.ID)})
	}
	write(w, 200, page(r, out))
	return true
}

// pullFiles is GET /repos/{slug}/pulls/{n}/files: each file the pull
// request changes against its base, with its patch (the diff's hunks, no
// header), paged as GitHub does.
func (s *Server) pullFiles(w http.ResponseWriter, r *http.Request) {
	p := s.number(w, r)
	if p == nil {
		return
	}
	spec := "refs/heads/" + p.Base + "...refs/heads/" + p.Head
	numstat, err := s.git("diff", "--numstat", spec)
	if err != nil {
		fail(w, 422, numstat)
		return
	}
	statuses, _ := s.git("diff", "--name-status", spec)
	status := map[string]string{}
	for _, line := range strings.Split(statuses, "\n") {
		if code, path, ok := strings.Cut(line, "\t"); ok {
			status[path] = map[string]string{"A": "added", "D": "removed", "M": "modified"}[code[:1]]
		}
	}
	files := []any{}
	for _, line := range strings.Split(numstat, "\n") {
		parts := strings.SplitN(line, "\t", 3)
		if len(parts) != 3 {
			continue
		}
		added, _ := strconv.Atoi(parts[0])
		deleted, _ := strconv.Atoi(parts[1])
		diff, _ := s.git("diff", spec, "--", parts[2])
		if i := strings.Index(diff, "\n@@"); i >= 0 {
			diff = diff[i+1:]
		}
		files = append(files, map[string]any{"filename": parts[2], "status": status[parts[2]], "additions": added,
			"deletions": deleted, "changes": added + deleted, "patch": diff})
	}
	write(w, 200, page(r, files))
}

// threadsRead is pull request number's review threads with what was said
// in each, as the query a reader sends has them. Callers do not hold s.mu.
func (s *Server) threadsRead(number int) map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	nodes := []any{}
	if p := s.pulls[number]; p != nil {
		for _, c := range p.LineComments {
			if c.InReplyTo != 0 {
				continue
			}
			comments := []any{}
			for _, r := range p.LineComments {
				if r.ID == c.ID || r.InReplyTo == c.ID {
					comments = append(comments, map[string]any{"author": map[string]string{"login": r.Author}, "body": r.Body,
						"createdAt": r.CreatedAt})
				}
			}
			id := s.ThreadID(c.ID)
			var line any
			if c.Line != 0 {
				line = c.Line
			}
			nodes = append(nodes, map[string]any{"isResolved": s.ResolvedThreads[id], "isOutdated": s.OutdatedThreads[id],
				"path": c.Path, "line": line, "comments": map[string]any{"totalCount": len(comments), "nodes": comments}})
		}
	}
	return map[string]any{"data": map[string]any{"repository": map[string]any{"pullRequest": map[string]any{
		"reviewThreads": map[string]any{"nodes": nodes, "pageInfo": map[string]any{"hasNextPage": false}}}}}}
}

// Search answers GET /search/issues for whichever of servers each repo:
// qualifier names. As GitHub does, several repo: qualifiers are ORed; is:pr
// or is:issue, is:open or is:closed narrow it, and the other words must
// each be in the title or the body. per_page as GitHub clamps it.
func Search(servers ...*Server) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var repos, words []string
		kind, state := "", ""
		for _, term := range strings.Fields(r.URL.Query().Get("q")) {
			switch {
			case strings.HasPrefix(term, "repo:"):
				repos = append(repos, strings.ToLower(strings.TrimPrefix(term, "repo:")))
			case term == "is:pr" || term == "is:issue":
				kind = strings.TrimPrefix(term, "is:")
			case term == "is:open" || term == "is:closed":
				state = strings.TrimPrefix(term, "is:")
			default:
				words = append(words, strings.ToLower(term))
			}
		}
		if len(repos) == 0 {
			fail(w, 422, "Validation Failed")
			return
		}
		type hit struct {
			number int
			item   map[string]any
		}
		var hits []hit
		matches := func(title, body, st string) bool {
			if state != "" && st != state {
				return false
			}
			text := strings.ToLower(title + "\n" + body)
			for _, word := range words {
				if !strings.Contains(text, word) {
					return false
				}
			}
			return true
		}
		for _, s := range servers {
			if !slices.Contains(repos, strings.ToLower(s.Slug)) {
				continue
			}
			s.logRequest(r)
			s.mu.Lock()
			s.rateHeaders(w, "search")
			if kind != "issue" {
				for _, p := range s.pulls {
					if matches(p.Title, p.Body, p.State) {
						hits = append(hits, hit{p.Number, s.issueJSON(r.Host, nil, p)})
					}
				}
			}
			if kind != "pr" {
				for _, i := range s.issues {
					if matches(i.Title, i.Body, i.State) {
						hits = append(hits, hit{i.Number, s.issueJSON(r.Host, i, nil)})
					}
				}
			}
			s.mu.Unlock()
		}
		// Most recently updated first: the fake's newest is its highest number.
		sort.Slice(hits, func(a, b int) bool { return hits[a].number > hits[b].number })
		items := []map[string]any{}
		for _, h := range hits {
			items = append(items, h.item)
		}
		write(w, 200, map[string]any{"total_count": len(items), "incomplete_results": false, "items": page(r, items)})
	}
}
