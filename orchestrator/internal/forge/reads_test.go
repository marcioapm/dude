package forge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
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

	p, err := gh.PullRequestRead(ctx, "acme/target", 1, 100, 50, "rollup.go")
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
	pWithoutPatch, err := gh.PullRequestRead(ctx, "acme/target", 1, 100, 50)
	if err != nil || len(pWithoutPatch.Files) != 1 || pWithoutPatch.Files[0].Patch != "" {
		t.Fatalf("unrequested patches retained: %+v err=%v", pWithoutPatch.Files, err)
	}
	if len(p.Files) != 1 || p.Files[0].Path != "rollup.go" || p.Files[0].Additions != 3 || !strings.Contains(p.Files[0].Patch, "+func Count()") {
		t.Errorf("files %+v", p.Files)
	}
	if p.CommentCount != 0 || p.Comments == nil || len(p.Comments) != 0 {
		t.Errorf("comments %+v (count %d), want none", p.Comments, p.CommentCount)
	}
	target.Comment(1, "dee", "Ship it, but the metric name is wrong.")
	p, err = gh.PullRequestRead(ctx, "acme/target", 1, 100, 50)
	if err != nil {
		t.Fatal(err)
	}
	if c := p.Comments; p.CommentCount != 1 || len(c) != 1 || c[0].Author != "dee" ||
		c[0].Body != "Ship it, but the metric name is wrong." || c[0].CreatedAt == "" {
		t.Errorf("conversation %+v (count %d)", c, p.CommentCount)
	}
}

// A pull request's conversation keeps its newest comments, oldest first,
// with how many it has. Page 1's Link header names the last page, which is
// read, with the page before it when the last holds fewer than are kept.
func TestAPullRequestReadKeepsTheNewestConversation(t *testing.T) {
	for _, tc := range []struct {
		comments  int
		wantPages []string
	}{
		{0, []string{"1"}},
		{1, []string{"1"}},
		{50, []string{"1"}},
		{51, []string{"1"}},
		{100, []string{"1"}},
		{101, []string{"1", "2"}},
		{150, []string{"1", "2"}},
		{200, []string{"1", "2"}},
		{210, []string{"1", "3", "2"}},
		{250, []string{"1", "3"}},
	} {
		t.Run(strconv.Itoa(tc.comments), func(t *testing.T) {
			target, _, gh := readsWorld(t)
			n := target.OpenAsPerson("ana", "feature/talk", "main", "Talk")
			for i := 1; i <= tc.comments; i++ {
				target.Comment(n, "bo", fmt.Sprintf("comment %d", i))
			}
			var pages []string
			target.Set(func(s *fakegithub.Server) {
				s.Intercept = func(r *http.Request) int {
					if strings.HasSuffix(r.URL.Path, "/issues/1/comments") {
						pages = append(pages, r.URL.Query().Get("page"))
					}
					return 0
				}
			})
			p, err := gh.PullRequestRead(context.Background(), "acme/target", n, 100, 50)
			if err != nil {
				t.Fatal(err)
			}
			keep := min(tc.comments, 50)
			if p.CommentCount != tc.comments || len(p.Comments) != keep {
				t.Fatalf("%d comments of %d, want %d of %d", len(p.Comments), p.CommentCount, keep, tc.comments)
			}
			for i, c := range p.Comments {
				if want := fmt.Sprintf("comment %d", tc.comments-keep+1+i); c.Body != want || c.Author != "bo" {
					t.Fatalf("comment %d: %+v, want %q by bo", i, c, want)
				}
			}
			if !slices.Equal(pages, tc.wantPages) {
				t.Errorf("pages read %v, want %v", pages, tc.wantPages)
			}
		})
	}
}

// The conversation's length is the list's, whatever the pull request's own
// count says, and comments deleted between pages or a Link header that does
// not parse cost at most 3 requests.
func TestAPullRequestConversationIsCountedByItsPages(t *testing.T) {
	const deleted = -1
	for _, tc := range []struct {
		name string
		// The pull request's comments count; deleted for none in its answer.
		reported int
		// The conversation, and what is left of it after page 1 is read.
		comments, afterFirst int
		// Replaces page 1's Link header when set.
		link                        string
		wantPages                   []string
		wantTotal                   int
		wantFirstBody, wantLastBody int
	}{
		{name: "count stale at zero", reported: 0, comments: 1, afterFirst: 1,
			wantPages: []string{"1"}, wantTotal: 1, wantFirstBody: 1, wantLastBody: 1},
		{name: "count behind", reported: 1, comments: 650, afterFirst: 650,
			wantPages: []string{"1", "7"}, wantTotal: 650, wantFirstBody: 601, wantLastBody: 650},
		{name: "count ahead", reported: 350, comments: 150, afterFirst: 150,
			wantPages: []string{"1", "2"}, wantTotal: 150, wantFirstBody: 101, wantLastBody: 150},
		{name: "no count", reported: deleted, comments: 130, afterFirst: 130,
			wantPages: []string{"1", "2"}, wantTotal: 130, wantFirstBody: 81, wantLastBody: 130},
		{name: "last page emptied", reported: 250, comments: 250, afterFirst: 180,
			wantPages: []string{"1", "3", "2"}, wantTotal: 180, wantFirstBody: 131, wantLastBody: 180},
		// One step back only: page 3 is taken as the last page, empty or not.
		{name: "last two pages emptied", reported: 350, comments: 350, afterFirst: 120,
			wantPages: []string{"1", "4", "3"}, wantTotal: 200, wantFirstBody: deleted, wantLastBody: deleted},
		{name: "last page emptied to page 2", reported: 250, comments: 250, afterFirst: 120,
			wantPages: []string{"1", "3", "2"}, wantTotal: 120, wantFirstBody: 71, wantLastBody: 120},
		// Page 1 was read whole before the deletion; it is the last page.
		{name: "last page emptied, page 1 held", reported: 120, comments: 120, afterFirst: 90,
			wantPages: []string{"1", "2"}, wantTotal: 100, wantFirstBody: 51, wantLastBody: 100},
		{name: "link without a page", reported: 250, comments: 250, afterFirst: 250, link: `<https://api.github.com/x?per_page=100>; rel="last"`,
			wantPages: []string{"1"}, wantTotal: 100, wantFirstBody: 51, wantLastBody: 100},
		{name: "link page not a number", reported: 250, comments: 250, afterFirst: 250, link: `<https://api.github.com/x?page=3x>; rel="last"`,
			wantPages: []string{"1"}, wantTotal: 100, wantFirstBody: 51, wantLastBody: 100},
		{name: "link page zero", reported: 250, comments: 250, afterFirst: 250, link: `<https://api.github.com/x?page=0>; rel="last"`,
			wantPages: []string{"1"}, wantTotal: 100, wantFirstBody: 51, wantLastBody: 100},
		// A relation list naming last among others (RFC 8288 section 3.3).
		{name: "link rel list", reported: 1, comments: 250, afterFirst: 250, link: `<https://api.github.com/x?page=2>; rel="next", <https://api.github.com/x?page=3>; rel="next last"`,
			wantPages: []string{"1", "3"}, wantTotal: 250, wantFirstBody: 201, wantLastBody: 250},
		{name: "link rel lookalike", reported: 250, comments: 250, afterFirst: 250, link: `<https://api.github.com/x?page=3>; rel="lastly"`,
			wantPages: []string{"1"}, wantTotal: 100, wantFirstBody: 51, wantLastBody: 100},
		{name: "link unbracketed", reported: 250, comments: 250, afterFirst: 250, link: `https://api.github.com/x?page=3; rel="last"`,
			wantPages: []string{"1"}, wantTotal: 100, wantFirstBody: 51, wantLastBody: 100},
	} {
		t.Run(tc.name, func(t *testing.T) {
			left := tc.comments
			var pages []string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case strings.HasSuffix(r.URL.Path, "/issues/1/comments"):
					page, _ := strconv.Atoi(r.URL.Query().Get("page"))
					pages = append(pages, strconv.Itoa(page))
					batch := []map[string]any{}
					for i := (page-1)*100 + 1; i <= min(page*100, left); i++ {
						batch = append(batch, map[string]any{"body": fmt.Sprintf("comment %d", i), "user": map[string]string{"login": "bo"},
							"created_at": "2026-01-01T00:00:00Z"})
					}
					if last := (left + 99) / 100; page == 1 && tc.link != "" {
						w.Header().Set("Link", tc.link)
					} else if page < last {
						w.Header().Set("Link", fmt.Sprintf(`<http://%s%s?per_page=100&page=%d>; rel="next", <http://%[1]s%[2]s?per_page=100&page=%[4]d>; rel="last"`,
							r.Host, r.URL.Path, page+1, last))
					}
					if page == 1 {
						left = tc.afterFirst
					}
					_ = json.NewEncoder(w).Encode(batch)
				case strings.HasSuffix(r.URL.Path, "/reviews"), strings.HasSuffix(r.URL.Path, "/files"):
					_, _ = w.Write([]byte(`[]`))
				case r.URL.Path == "/graphql":
					_, _ = w.Write([]byte(`{"data":{"repository":{"pullRequest":{"reviewThreads":{"nodes":[],"pageInfo":{"hasNextPage":false}}}}}}`))
				case tc.reported == deleted:
					_, _ = w.Write([]byte(`{"number":1}`))
				default:
					_, _ = fmt.Fprintf(w, `{"number":1,"comments":%d}`, tc.reported)
				}
			}))
			defer srv.Close()
			p, err := NewGitHub(Credential{Secret: t.Name(), APIBaseURL: srv.URL}).PullRequestRead(context.Background(), "acme/allowed", 1, 300, 50)
			if err != nil {
				t.Fatal(err)
			}
			if len(pages) > 3 {
				t.Fatalf("%d conversation requests %v, want at most 3", len(pages), pages)
			}
			if p.CommentCount != tc.wantTotal {
				t.Errorf("total %d, want %d", p.CommentCount, tc.wantTotal)
			}
			if tc.wantFirstBody == deleted {
				if len(p.Comments) != 0 {
					t.Errorf("%d comments, want none: %+v", len(p.Comments), p.Comments[0])
				}
			} else if want := tc.wantLastBody - tc.wantFirstBody + 1; len(p.Comments) != want ||
				p.Comments[0].Body != fmt.Sprintf("comment %d", tc.wantFirstBody) ||
				p.Comments[want-1].Body != fmt.Sprintf("comment %d", tc.wantLastBody) {
				t.Errorf("%d comments, want %d from comment %d to %d", len(p.Comments), want, tc.wantFirstBody, tc.wantLastBody)
			}
			if !slices.Equal(pages, tc.wantPages) {
				t.Errorf("pages read %v, want %v", pages, tc.wantPages)
			}
		})
	}
}

// A read GitHub refuses with 403 keeps which permissions it would have
// taken, when the header is a list of them; anything else in the header is
// dropped, and a rate limit's 403 keeps none.
func TestAForbiddenReadKeepsTheAcceptedPermissions(t *testing.T) {
	for _, tc := range []struct {
		name, header string
		rateLimited  bool
		want         string
	}{
		{"one", "issues=read", false, "issues=read"},
		{"alternatives", " pull_requests=read,contents=read ;issues=read", false, "pull_requests=read,contents=read; issues=read"},
		{"not a permission", "issues=read; see https://example.test", false, ""},
		{"unknown access", "issues=everything", false, ""},
		{"rate limit", "issues=read", true, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("X-Accepted-GitHub-Permissions", tc.header)
				if tc.rateLimited {
					w.Header().Set("Retry-After", "30")
				}
				w.WriteHeader(http.StatusForbidden)
				_, _ = w.Write([]byte(`{"message":"Resource not accessible by integration"}`))
			}))
			defer srv.Close()
			_, err := NewGitHub(Credential{Secret: t.Name(), APIBaseURL: srv.URL}).Issue(context.Background(), "acme/target", 7, 50)
			var e *Error
			if !errors.As(err, &e) || e.Status != http.StatusForbidden || e.AcceptedPermissions != tc.want {
				t.Fatalf("err=%v (%#v), want accepted permissions %q", err, e, tc.want)
			}
		})
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

// A redirect's body is another location's answer: an agent's read refuses
// it in dude's words, whatever the body says, and a repository's permanent
// redirect says the repository moved.
func TestAgentReadRedirectBodiesNeverReachTheRefusal(t *testing.T) {
	const canary = "private-redirect-body-canary"
	for _, tc := range []struct {
		name, path string
		status     int
		read       func(gh *GitHub) error
		refusal    string
	}{
		{"canonical lookup 301", "/repos/acme/old", http.StatusMovedPermanently, func(gh *GitHub) error {
			_, _, err := gh.SearchIssues(context.Background(), "acme/old", "", "issue", "", 30)
			return err
		}, RefusalMoved},
		{"issue 308", "/repos/acme/old/issues/1", http.StatusPermanentRedirect, func(gh *GitHub) error {
			_, err := gh.Issue(context.Background(), "acme/old", 1, 50)
			return err
		}, RefusalMovedItem},
		{"issue 302", "/repos/acme/old/issues/1", http.StatusFound, func(gh *GitHub) error {
			_, err := gh.Issue(context.Background(), "acme/old", 1, 50)
			return err
		}, RefusalRedirect},
		{"issue 303", "/repos/acme/old/issues/1", http.StatusSeeOther, func(gh *GitHub) error {
			_, err := gh.Issue(context.Background(), "acme/old", 1, 50)
			return err
		}, RefusalRedirect},
		{"issue 307", "/repos/acme/old/issues/1", http.StatusTemporaryRedirect, func(gh *GitHub) error {
			_, err := gh.Issue(context.Background(), "acme/old", 1, 50)
			return err
		}, RefusalRedirect},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/repos/acme/old" && tc.path != r.URL.Path {
					// The repository is where it was: only the item moved.
					_, _ = w.Write([]byte(`{"full_name":"acme/old"}`))
					return
				}
				if r.URL.Path != tc.path {
					t.Errorf("unexpected request %s", r.URL.Path)
				}
				w.Header().Set("Location", "/repositories/42/"+canary)
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(`{"message":"` + canary + `","url":"/repositories/42"}`))
			}))
			defer srv.Close()
			err := tc.read(NewGitHub(Credential{Secret: t.Name(), APIBaseURL: srv.URL}))
			var e *Error
			if !errors.As(err, &e) || e.Refusal != tc.refusal || e.Status != tc.status || strings.Contains(err.Error(), canary) {
				t.Fatalf("err=%v (%#v), want refusal %q without the body", err, e, tc.refusal)
			}
		})
	}
}

// A permanent redirect of an issue or pull request says the repository
// moved only when the repository's own path redirects too; when the
// repository answers where it was, the item moved (an issue transferred).
func TestAMovedItemIsNotAMovedRepository(t *testing.T) {
	for _, tc := range []struct {
		name       string
		renamed    bool
		read       func(gh *GitHub) error
		want, item string
	}{
		{"transferred issue", false, func(gh *GitHub) error { _, err := gh.Issue(context.Background(), "acme/old", 7, 50); return err },
			RefusalMovedItem, "/repos/acme/old/issues/7"},
		{"transferred pull request's path", false, func(gh *GitHub) error {
			_, err := gh.PullRequestRead(context.Background(), "acme/old", 7, 300, 50)
			return err
		}, RefusalMovedItem, "/repos/acme/old/pulls/7"},
		{"renamed repository, issue", true, func(gh *GitHub) error { _, err := gh.Issue(context.Background(), "acme/old", 7, 50); return err },
			RefusalMoved, "/repos/acme/old/issues/7"},
		{"renamed repository, pull request", true, func(gh *GitHub) error {
			_, err := gh.PullRequestRead(context.Background(), "acme/old", 7, 300, 50)
			return err
		}, RefusalMoved, "/repos/acme/old/pulls/7"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var paths []string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				paths = append(paths, r.URL.Path)
				if r.URL.Path == "/repos/acme/old" && !tc.renamed {
					_, _ = w.Write([]byte(`{"full_name":"acme/old"}`))
					return
				}
				w.Header().Set("Location", "/repositories/42")
				w.WriteHeader(http.StatusMovedPermanently)
			}))
			defer srv.Close()
			err := tc.read(NewGitHub(Credential{Secret: t.Name(), APIBaseURL: srv.URL}))
			var e *Error
			if !errors.As(err, &e) || e.Refusal != tc.want || e.Status != http.StatusMovedPermanently || Moved(err) != (tc.want == RefusalMoved) {
				t.Fatalf("err=%v (%#v), want refusal %q", err, e, tc.want)
			}
			if want := []string{tc.item, "/repos/acme/old"}; !slices.Equal(paths, want) {
				t.Errorf("requests %v, want %v", paths, want)
			}
		})
	}
}

// A repository whose name looks like another resource's path is still
// read on the core limit: its second request is refused once core is down
// to delivery's share.
func TestAgentReadOfLookalikeRepositoriesIsGatedOnCore(t *testing.T) {
	for _, slug := range []string{"graphql/graphql-spec", "acme/search", "acme/graphql-x"} {
		t.Run(slug, func(t *testing.T) {
			calls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				w.Header().Set("X-RateLimit-Remaining", "19")
				w.Header().Set("X-RateLimit-Limit", "100")
				w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10))
				w.Header().Set("X-RateLimit-Resource", "core")
				if strings.HasSuffix(r.URL.Path, "/comments") {
					_, _ = w.Write([]byte(`[]`))
				} else {
					_, _ = w.Write([]byte(`{"number":1,"comments":100}`))
				}
			}))
			defer srv.Close()
			gh := NewGitHub(Credential{Secret: t.Name(), APIBaseURL: srv.URL})
			_, err := gh.Issue(context.Background(), slug, 1, 50)
			if !RateLimited(err) || calls != 1 {
				t.Fatalf("err=%v calls=%d, want the comments request refused on core", err, calls)
			}
		})
	}
}

// The search endpoint is gated on the search limit, apart from core's.
func TestAgentSearchIsGatedOnTheSearchLimit(t *testing.T) {
	searches := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reset := strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10)
		w.Header().Set("X-RateLimit-Limit", "100")
		w.Header().Set("X-RateLimit-Reset", reset)
		if r.URL.Path == "/search/issues" {
			searches++
			w.Header().Set("X-RateLimit-Resource", "search")
			w.Header().Set("X-RateLimit-Remaining", "19")
			_, _ = w.Write([]byte(`{"total_count":0,"items":[]}`))
			return
		}
		w.Header().Set("X-RateLimit-Resource", "core")
		w.Header().Set("X-RateLimit-Remaining", "90")
		_, _ = w.Write([]byte(`{"full_name":"acme/search"}`))
	}))
	defer srv.Close()
	gh := NewGitHub(Credential{Secret: t.Name(), APIBaseURL: srv.URL})
	if _, _, err := gh.SearchIssues(context.Background(), "acme/search", "", "issue", "", 30); err != nil {
		t.Fatal(err)
	}
	_, _, err := gh.SearchIssues(context.Background(), "acme/search", "", "issue", "", 30)
	if !RateLimited(err) || searches != 1 {
		t.Fatalf("err=%v searches=%d, want the second search refused on the search limit", err, searches)
	}
}

// At exactly delivery's share left, a read is refused at its entry, saying
// how many requests are left and until when, before any request.
func TestAgentReadAtTheReserveIsRefusedAtEntry(t *testing.T) {
	calls := 0
	reset := time.Now().Add(time.Hour)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_, _ = w.Write([]byte(`{"number":1}`))
	}))
	defer srv.Close()
	gh := NewGitHub(Credential{Secret: t.Name(), APIBaseURL: srv.URL})
	rateReadings.Store(rateKey(srv.URL, "Bearer "+t.Name(), "core"), rateReading{remaining: 20, limit: 100, reset: reset})
	_, err := gh.Issue(context.Background(), "acme/allowed", 1, 50)
	want := "GitHub's core rate limit has 20 of 100 requests left until " + reset.UTC().Format(time.RFC3339)
	if !RateLimited(err) || !strings.Contains(err.Error(), want) || calls != 0 {
		t.Fatalf("err=%v calls=%d, want %q before any request", err, calls, want)
	}
	rateReadings.Store(rateKey(srv.URL, "Bearer "+t.Name(), "core"), rateReading{remaining: 21, limit: 100, reset: reset})
	if _, err := gh.Issue(context.Background(), "acme/allowed", 1, 50); err != nil || calls != 1 {
		t.Fatalf("one above the reserve: err=%v calls=%d", err, calls)
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
	_, checks, cut, err := gh.checks(agentReadContext(context.Background()), "acme/allowed", "abc")
	if err != nil || pages > 3 || len(checks) == 0 || !cut {
		t.Fatalf("checks cap: pages=%d checks=%+v cut=%v err=%v", pages, checks, cut, err)
	}
}

// Commit statuses fill most of the list: the check runs past its cap are
// left out, and the list says it is cut, whatever GitHub's total.
func TestAgentChecksSayCutWhenStatusesFillTheList(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "check-runs") {
			statuses := []map[string]any{}
			for i := range 100 {
				statuses = append(statuses, map[string]any{"context": "status-" + strconv.Itoa(i), "state": "success"})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"state": "success", "total_count": 100, "statuses": statuses})
			return
		}
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		runs := []map[string]any{}
		for i := (page - 1) * 100; i < min(page*100, 250); i++ {
			conclusion := "success"
			if i == 249 {
				conclusion = "failure"
			}
			runs = append(runs, map[string]any{"name": "run-" + strconv.Itoa(i), "status": "completed", "conclusion": conclusion})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"total_count": 250, "check_runs": runs})
	}))
	defer srv.Close()
	gh := NewGitHub(Credential{Secret: t.Name(), APIBaseURL: srv.URL})
	_, checks, cut, err := gh.checks(agentReadContext(context.Background()), "acme/allowed", "abc")
	if err != nil || !cut || len(checks) > 300 {
		t.Fatalf("checks=%d cut=%v err=%v, want the list marked cut", len(checks), cut, err)
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
			for i := (page - 1) * size; i < min(page*size, 120); i++ {
				batch = append(batch, map[string]any{"state": "COMMENTED", "body": "review", "submitted_at": "2026-01-01T00:00:00Z"})
			}
			_ = json.NewEncoder(w).Encode(batch)
		case strings.HasSuffix(r.URL.Path, "/files"), strings.HasSuffix(r.URL.Path, "/comments"):
			_, _ = w.Write([]byte(`[]`))
		case r.URL.Path == "/graphql":
			_, _ = w.Write([]byte(`{"data":{"repository":{"pullRequest":{"reviewThreads":{"nodes":[],"pageInfo":{"hasNextPage":false}}}}}}`))
		default:
			_, _ = w.Write([]byte(`{"number":1}`))
		}
	}))
	defer srv.Close()
	gh := NewGitHub(Credential{Secret: t.Name(), APIBaseURL: srv.URL})
	p, err := gh.PullRequestRead(context.Background(), "acme/allowed", 1, 300, 50)
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
