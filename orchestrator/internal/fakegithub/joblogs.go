package fakegithub

import (
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
)

// CheckRun is how GitHub reports one check run, when a test sets one.
type CheckRun struct {
	App                  string
	Title, Summary, Text string
	Annotations          []Annotation
}

type Annotation struct {
	Path, Level, Message string
	Line                 int
}

// ActionsJob is a GitHub Actions job that failed by its exit code, as the
// check run of its id reports it.
func ActionsJob() CheckRun {
	return CheckRun{App: "github-actions", Annotations: []Annotation{{Path: ".github", Line: 442, Level: "failure",
		Message: "Process completed with exit code 1."}}}
}

// SetJobLog gives job id a log, served from downloads (LogDownloads' URL);
// "" takes the log away, as GitHub does once it expires (410).
func (s *Server) SetJobLog(id int64, log, downloads string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.JobLogs == nil {
		s.JobLogs = map[int64]string{}
	}
	s.JobLogs[id], s.LogHost = log, downloads
}

// DownloadAuthorization is the Authorization header of each log download,
// "" for none, in order.
func (s *Server) DownloadAuthorization() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.downloadAuth)
}

func (s *Server) checkRun(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	s.mu.Lock()
	run, ok := s.CheckRuns[id]
	s.mu.Unlock()
	if !ok {
		write(w, 200, map[string]any{"output": map[string]string{"title": "1 test failed",
			"summary": "TestGreeting: expected hello, got hi", "text": ""}})
		return
	}
	out := map[string]any{"id": id, "app": map[string]string{"slug": run.App}, "output": nil}
	if run.Title != "" || run.Summary != "" || run.Text != "" {
		out["output"] = map[string]string{"title": run.Title, "summary": run.Summary, "text": run.Text}
	}
	write(w, 200, out)
}

func (s *Server) checkRunAnnotations(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	s.mu.Lock()
	run, ok := s.CheckRuns[id]
	s.mu.Unlock()
	if !ok {
		write(w, 200, []any{map[string]any{"path": "greet.go", "start_line": 12, "annotation_level": "failure",
			"message": "expected hello"}})
		return
	}
	out := []any{}
	for _, a := range run.Annotations {
		out = append(out, map[string]any{"path": a.Path, "start_line": a.Line, "annotation_level": a.Level, "message": a.Message})
	}
	write(w, 200, out)
}

// jobLogs answers as GitHub does: 302 to the download host, 410 for a job
// whose log is gone, 404 for no such job, or LogStatus when set.
func (s *Server) jobLogs(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	s.mu.Lock()
	log, ok := s.JobLogs[id]
	host, status := s.LogHost, s.LogStatus
	s.mu.Unlock()
	switch {
	case status != 0:
		fail(w, status, http.StatusText(status))
	case !ok:
		fail(w, 404, "Not Found")
	case log == "":
		fail(w, 410, "Logs have expired")
	default:
		w.Header().Set("Location", fmt.Sprintf("%s/%s/jobs/%d/log?sig=short-lived", host, s.Slug, id))
		w.WriteHeader(http.StatusFound)
	}
}

// LogDownloads is the host job logs download from, as GitHub's blob
// storage: it records the Authorization each download carried.
func (s *Server) LogDownloads() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.downloadAuth = append(s.downloadAuth, r.Header.Get("Authorization"))
		var log string
		rest, ok := strings.CutPrefix(r.URL.Path, "/"+s.Slug+"/jobs/")
		if id, err := strconv.ParseInt(strings.TrimSuffix(rest, "/log"), 10, 64); ok && err == nil {
			log = s.JobLogs[id]
		}
		s.mu.Unlock()
		if log == "" || r.URL.Query().Get("sig") == "" {
			http.Error(w, "AuthenticationFailed", http.StatusForbidden)
			return
		}
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte(log))
	})
}
