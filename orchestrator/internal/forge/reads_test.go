package forge

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

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

	p, err := gh.PullRequestRead(ctx, "acme/target", 1, 100)
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
	if len(p.Files) != 1 || p.Files[0].Path != "rollup.go" || p.Files[0].Additions != 3 || !strings.Contains(p.Files[0].Patch, "+func Count()") {
		t.Errorf("files %+v", p.Files)
	}
}

// Once GitHub says less than a fifth of the token's limit is left, an
// agent's read is refused before any request, until it resets; delivery's
// own reads go on.
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
