package forge

import (
	"encoding/json"
	"context"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"strconv"
	"testing"
	"time"

	"github.com/marciomartins/dude/orchestrator/internal/fakegithub"
)

// readsWorld is a fake GitHub holding two repositories behind one API, as
// an App installed on both answers, and a client of it.
func readsWorld(t *testing.T) (target, other *fakegithub.Server, gh *GitHub) {
	t.Helper()
	bare, seed := filepath.Join(t.TempDir(), "r.git"), t.TempDir()
	for _, args := range [][]string{{"init", "-q", "--bare", "-b", "main", bare}, {"init", "-q", "-b", "main", seed},
		{"-C", seed, "-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "--allow-empty", "-m", "initial"},
		{"-C", seed, "push", "-q", bare, "main"}} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	target, other = fakegithub.New(bare, "acme/target"), fakegithub.New(bare, "acme/other")
	mux := http.NewServeMux()
	mux.Handle("GET /repos/acme/target", target.Handler())
	mux.Handle("/repos/acme/target/", target.Handler())
	mux.Handle("/repos/acme/other/", other.Handler())
	mux.Handle("GET /search/issues", fakegithub.Search(target, other))
	mux.Handle("POST /graphql", fakegithub.Graphql(target, other))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return target, other, NewGitHub(Credential{Auth: "pat", Secret: "ghp_reads_" + t.Name(), APIBaseURL: srv.URL})
}

// A search names its repository, and words naming another one add none of
// its items: GitHub ORs repo: qualifiers, so the read drops them.
func TestASearchListsOnlyItsRepository(t *testing.T) {
	target, other, gh := readsWorld(t)
	target.OpenIssue("ana", "Rollup double counts", "Retries count twice.", "bug")
	other.OpenIssue("bo", "Rollup elsewhere", "Not this repository.")
	lines, total, err := gh.SearchIssues(context.Background(), "acme/target", "rollup repo:acme/other", "issue", "", 30)
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 1 || lines[0].Title != "Rollup double counts" || !slices.Equal(lines[0].Labels, []string{"bug"}) ||
		lines[0].Author != "ana" || lines[0].State != "open" || lines[0].PullRequest {
		t.Fatalf("lines %+v (total %d)", lines, total)
	}
}

// A pull request read has its text, reviews, unresolved threads with their
// words, checks and changed files with their patches.
func TestAPullRequestReadIsWhatAReaderSees(t *testing.T) {
	target, _, gh := readsWorld(t)
	target.Branch("feature/rollup", "main", "Start")
	target.WriteFile("feature/rollup", "rollup.go", "package rollup\n\nfunc Count() int { return 1 }\n", "Count once")
	ctx := context.Background()
	if _, err := gh.OpenPullRequest(ctx, OpenPullRequest{Slug: "acme/target", Title: "Count once", Body: "Dedupe on run id.",
		Head: "feature/rollup", Base: "main"}); err != nil {
		t.Fatal(err)
	}
	target.ReviewSaying(1, "ana", "CHANGES_REQUESTED", "Say why.")
	open := target.LineCommentAt(1, "bo", "rollup.go", 3, "Why one?")
	resolved := target.LineCommentAt(1, "cy", "rollup.go", 1, "Fine.")
	target.Set(func(s *fakegithub.Server) { s.ResolvedThreads = map[string]bool{s.ThreadID(resolved): true} })
	target.SetChecks("run:failure")

	p, err := gh.PullRequestRead(ctx, "acme/target", 1, 100, "rollup.go")
	if err != nil {
		t.Fatal(err)
	}
	if p.Title != "Count once" || p.Body != "Dedupe on run id." || p.Head != "feature/rollup" || p.Base != "main" || p.State != StateOpen {
		t.Errorf("read %+v", p.IssueLine)
	}
	if len(p.Reviews) != 1 || p.Reviews[0].Body != "Say why." || p.Reviews[0].State != "CHANGES_REQUESTED" {
		t.Errorf("reviews %+v", p.Reviews)
	}
	if len(p.Threads) != 1 || p.Threads[0].Path != "rollup.go" || p.Threads[0].Line != 3 || p.Threads[0].Comments[0].Body != "Why one?" {
		t.Errorf("threads %+v (open comment %d)", p.Threads, open)
	}
	if len(p.Checks) != 1 || p.Checks[0].Name != "e2e" || p.Checks[0].Conclusion != "failure" {
		t.Errorf("checks %+v", p.Checks)
	}
	pWithoutPatch, err := gh.PullRequestRead(ctx, "acme/target", 1, 100)
	if err != nil || len(pWithoutPatch.Files) != 1 || pWithoutPatch.Files[0].Patch != "" {
		t.Fatalf("unrequested patches retained: %+v err=%v", pWithoutPatch.Files, err)
	}
	if len(p.Files) != 1 || p.Files[0].Path != "rollup.go" || p.Files[0].Additions != 3 || !strings.Contains(p.Files[0].Patch, "+func Count()") {
		t.Errorf("files %+v", p.Files)
	}
}

// Once GitHub says less than a fifth of the token's limit is left, an
// agent's read is refused before any request, until it resets; delivery's
// own reads go on.
func TestAgentReadRefusesRepositoryRedirect(t *testing.T) {
	forbiddenCalls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/repos/acme/allowed/issues/1" {
			http.Redirect(w, r, "/repos/acme/forbidden/issues/1", http.StatusFound)
			return
		}
		forbiddenCalls++
		_, _ = w.Write([]byte(`{"title":"private-outside-canary","body":"private-outside-canary"}`))
	}))
	defer srv.Close()
	gh := NewGitHub(Credential{Secret: t.Name(), APIBaseURL: srv.URL})
	result, err := gh.Issue(context.Background(), "acme/allowed", 1, 50)
	if err == nil || forbiddenCalls != 0 || strings.Contains(result.Title+result.Body, "private-outside-canary") {
		t.Fatalf("redirect: err=%v forbidden calls=%d result=%+v", err, forbiddenCalls, result)
	}
}

func TestAgentReadRechecksReserveBetweenRequests(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("X-RateLimit-Remaining", "19")
		w.Header().Set("X-RateLimit-Limit", "100")
		w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10))
		if strings.HasSuffix(r.URL.Path, "/comments") {
			_, _ = w.Write([]byte(`[]`))
		} else {
			_, _ = w.Write([]byte(`{"number":1,"comments":100}`))
		}
	}))
	defer srv.Close()
	gh := NewGitHub(Credential{Secret: t.Name(), APIBaseURL: srv.URL})
	_, err := gh.Issue(context.Background(), "acme/allowed", 1, 50)
	if !RateLimited(err) || !strings.Contains(err.Error(), "kept for delivery") || calls != 1 {
		t.Fatalf("reserve: err=%v calls=%d", err, calls)
	}
}

func TestAgentChecksStopAtPageCap(t *testing.T) {
	pages := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "check-runs") {
			pages++
			if pages > 4 {
				_, _ = w.Write([]byte(`{"total_count":10000,"check_runs":[]}`))
				return
			}
			_, _ = w.Write([]byte(`{"total_count":10000,"check_runs":[{"name":"ci","status":"completed","conclusion":"success"}]}`))
		} else {
			_, _ = w.Write([]byte(`{"state":"success","statuses":[]}`))
		}
	}))
	defer srv.Close()
	gh := NewGitHub(Credential{Secret: t.Name(), APIBaseURL: srv.URL})
	_, checks, err := gh.checks(agentReadContext(context.Background()), "acme/allowed", "abc")
	if err != nil || pages > 3 || len(checks) == 0 || checks[len(checks)-1].Diagnostic != "…cut: check runs incomplete" {
		t.Fatalf("checks cap: pages=%d checks=%+v err=%v", pages, checks, err)
	}
}

func TestCappedResolvedThreadsRemainUnknown(t *testing.T) {
	pages := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pages++
		nodes := []map[string]any{{"isResolved": pages <= 5, "path": "tail.go"}}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"repository": map[string]any{"pullRequest": map[string]any{"reviewThreads": map[string]any{
			"nodes": nodes, "pageInfo": map[string]any{"hasNextPage": pages <= 5, "endCursor": strconv.Itoa(pages)},
		}}}}})
	}))
	defer srv.Close()
	gh := NewGitHub(Credential{Secret: t.Name(), APIBaseURL: srv.URL})
	threads, unknown, err := gh.openThreads(agentReadContext(context.Background()), "acme/allowed", 1)
	if err != nil || pages != 5 || len(threads) != 0 || !unknown {
		t.Fatalf("threads=%+v unknown=%v pages=%d err=%v", threads, unknown, pages, err)
	}
}

func TestRateReadingsExpireOnWrite(t *testing.T) {
	old := rateKey("https://expiry.test", "old-token", "core")
	rateReadings.Store(old, rateReading{remaining: 1, limit: 100, reset: time.Now().Add(-time.Minute)})
	h := http.Header{}
	h.Set("X-RateLimit-Remaining", "90")
	h.Set("X-RateLimit-Limit", "100")
	h.Set("X-RateLimit-Reset", strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10))
	noteRate("https://expiry.test", "new-token", h)
	if _, ok := rateReadings.Load(old); ok {
		t.Fatal("expired old-token reading retained")
	}
}

func TestSearchUsesCanonicalRepositoryName(t *testing.T) {
	resolved := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/acme/old":
			resolved++
			_, _ = w.Write([]byte(`{"full_name":"acme/new"}`))
		case "/search/issues":
			_, _ = w.Write([]byte(`{"total_count":2,"items":[{"number":1,"title":"renamed","repository_url":"https://api.github.com/repos/acme/new"},{"number":2,"repository_url":"https://api.github.com/repos/acme/forbidden"}]}`))
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
		}
	}))
	defer srv.Close()
	gh := NewGitHub(Credential{Secret: t.Name(), APIBaseURL: srv.URL})
	items, _, err := gh.SearchIssues(context.Background(), "acme/old", "", "issue", "", 30)
	if err != nil || resolved != 1 || len(items) != 1 || items[0].Title != "renamed" {
		t.Fatalf("canonical search: resolved=%d items=%+v err=%v", resolved, items, err)
	}
}

func TestAgentReviewsFetchOnlyBoundedHistory(t *testing.T) {
	pages := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/reviews"):
			pages++
			page, _ := strconv.Atoi(r.URL.Query().Get("page"))
			size, _ := strconv.Atoi(r.URL.Query().Get("per_page"))
			batch := []map[string]any{}
			for i := (page-1)*size; i < min(page*size, 120); i++ {
				batch = append(batch, map[string]any{"state": "COMMENTED", "body": "review", "submitted_at": "2026-01-01T00:00:00Z"})
			}
			_ = json.NewEncoder(w).Encode(batch)
		case strings.HasSuffix(r.URL.Path, "/files"):
			_, _ = w.Write([]byte(`[]`))
		case r.URL.Path == "/graphql":
			_, _ = w.Write([]byte(`{"data":{"repository":{"pullRequest":{"reviewThreads":{"nodes":[],"pageInfo":{"hasNextPage":false}}}}}}`))
		default:
			_, _ = w.Write([]byte(`{"number":1}`))
		}
	}))
	defer srv.Close()
	gh := NewGitHub(Credential{Secret: t.Name(), APIBaseURL: srv.URL})
	p, err := gh.PullRequestRead(context.Background(), "acme/allowed", 1, 300)
	if err != nil || pages > 2 || len(p.Reviews) != 50 || !p.ReviewsCut {
		t.Fatalf("reviews=%d pages=%d err=%v", len(p.Reviews), pages, err)
	}
}

func TestAgentReadRefusesOversizeBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"body":"` + strings.Repeat("x", 5<<20) + `"}`))
	}))
	defer srv.Close()
	gh := NewGitHub(Credential{Secret: t.Name(), APIBaseURL: srv.URL})
	result, err := gh.Issue(context.Background(), "acme/allowed", 1, 50)
	if err == nil || !strings.Contains(err.Error(), "response exceeds") || result.Body != "" {
		t.Fatalf("oversize body: err=%v bytes=%d", err, len(result.Body))
	}
}

func TestAgentReadsLeaveDeliveryItsShareOfTheRateLimit(t *testing.T) {
	target, _, gh := readsWorld(t)
	n := target.OpenIssue("ana", "Rollup", "Body")
	ctx := context.Background()
	target.Set(func(s *fakegithub.Server) { s.RateRemaining = map[string]int{"core": 1001} })
	if _, err := gh.Issue(ctx, "acme/target", n, 10); err != nil {
		t.Fatalf("with 1001 of 5000 left: %v", err)
	}
	target.Set(func(s *fakegithub.Server) { s.RateRemaining = map[string]int{"core": 999} })
	if _, err := gh.Issue(ctx, "acme/target", n, 10); err != nil {
		t.Fatalf("the read that learns 999 are left is made: %v", err)
	}
	before := len(target.Requests())
	_, err := gh.Issue(ctx, "acme/target", n, 10)
	if !RateLimited(err) || RetryAfter(err) <= 0 || !strings.Contains(err.Error(), "kept for delivery") {
		t.Fatalf("with 999 left: %v", err)
	}
	if after := len(target.Requests()); after != before {
		t.Errorf("a refused read reached GitHub: %v", target.Requests()[before:])
	}
	if _, err := gh.Head(ctx, "acme/target", 1); err == nil || RateLimited(err) {
		t.Errorf("delivery's read: %v, want GitHub's own answer (no pull request 1)", err)
	}
}
