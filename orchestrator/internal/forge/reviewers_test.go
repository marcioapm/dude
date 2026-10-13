package forge

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/fakegithub"
)

// graphQLServer answers /graphql with `answer` for the query and variables
// it was sent, and records what REST was sent.
func graphQLServer(t *testing.T, answer func(query string, vars map[string]any) string) (*GitHub, *[]map[string]any) {
	t.Helper()
	var mu sync.Mutex
	var posted []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var in map[string]any
		_ = json.Unmarshal(b, &in)
		if r.URL.Path == "/graphql" {
			vars, _ := in["variables"].(map[string]any)
			q, _ := in["query"].(string)
			_, _ = io.WriteString(w, answer(q, vars))
			return
		}
		mu.Lock()
		posted = append(posted, in)
		mu.Unlock()
		w.WriteHeader(201)
		_, _ = io.WriteString(w, "{}")
	}))
	t.Cleanup(srv.Close)
	return NewGitHub(Credential{Auth: "pat", Secret: "x", APIBaseURL: srv.URL}), &posted
}

const noTeams = `{"data":{"organization":null},"errors":[{"type":"NOT_FOUND","message":"Could not resolve to an Organization"}]}`

// Before any words: GitHub's suggestions, why each, without the author; no
// teams are asked for.
func TestReviewerSuggestionsForAPullRequest(t *testing.T) {
	gh, _ := graphQLServer(t, func(q string, v map[string]any) string {
		if strings.Contains(q, "teams") {
			t.Error("asked for teams without words")
		}
		if v["pr"] != true || v["suggest"] != true || v["search"] != false || v["number"] != float64(7) {
			t.Errorf("variables = %v", v)
		}
		return `{"data":{"repository":{"pullRequest":{"author":{"login":"dude-bot"},
			"suggestedReviewers":[
				{"isCommenter":false,"reviewer":{"login":"ana","name":"Ana Ribeiro","avatarUrl":"https://a/ana"}},
				{"isCommenter":true,"reviewer":{"login":"tom","name":"Tom Okafor","avatarUrl":"https://a/tom"}},
				{"isCommenter":false,"reviewer":{"login":"dude-bot","name":"","avatarUrl":""}}]}}}}`
	})
	got, err := gh.ReviewerCandidates(context.Background(), "acme/api", 7, "  ")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Login != "ana" || got[0].Reason != "changed" || got[0].Name != "Ana Ribeiro" ||
		got[1].Login != "tom" || got[1].Reason != "commented" {
		t.Errorf("candidates = %+v", got)
	}
}

// With words: the repository's assignable people, then the owner's teams
// as "org/slug", the author left out; suggestions are not asked for.
func TestReviewerSearchFindsPeopleAndTeams(t *testing.T) {
	gh, _ := graphQLServer(t, func(q string, v map[string]any) string {
		if v["q"] != "an" {
			t.Errorf("variables = %v", v)
		}
		if strings.Contains(q, "teams") {
			return `{"data":{"organization":{"teams":{"nodes":[{"slug":"platform","name":"Platform","members":{"totalCount":7}}]}}}}`
		}
		if v["suggest"] != false || v["search"] != true {
			t.Errorf("variables = %v", v)
		}
		return `{"data":{"repository":{"pullRequest":{"author":{"login":"ana"}},
			"assignableUsers":{"nodes":[{"login":"ana","name":"Ana Ribeiro"},{"login":"hanna","name":"Hanna Lindqvist","avatarUrl":"https://a/h"}]}}}}`
	})
	got, err := gh.ReviewerCandidates(context.Background(), "acme/api", 7, "an")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Login != "hanna" || got[0].Kind != "user" ||
		got[1].Login != "acme/platform" || got[1].Kind != "team" || got[1].Members != 7 {
		t.Errorf("candidates = %+v", got)
	}
}

// Teams GitHub will not give — a user owns the repository, the token has
// no read:org, a team hidden by SSO — are no teams; the people still come
// back. A rate limit is GitHub's to say again; another error about the
// people is a refusal.
func TestReviewerSearchErrors(t *testing.T) {
	people := `{"data":{"repository":{"pullRequest":null,"assignableUsers":{"nodes":[{"login":"cy"}]}}}}`
	for name, c := range map[string]struct {
		people, teams string
		n             int
		transient     bool
		refused       bool
	}{
		"user-owned": {people, noTeams, 1, false, false},
		"no read:org": {people, `{"errors":[{"type":"INSUFFICIENT_SCOPES","message":"requires one of the following scopes: ['read:org']"}]}`,
			1, false, false},
		"a team hidden by SSO": {people, `{"data":{"organization":{"teams":{"nodes":[null]}}},
			"errors":[{"type":"FORBIDDEN","path":["organization","teams","nodes",0,"members"],"message":"SSO"}]}`, 1, false, false},
		"rate limited":  {`{"errors":[{"type":"RATE_LIMITED","message":"API rate limit exceeded"}]}`, noTeams, 0, true, false},
		"no repository": {`{"data":{"repository":null},"errors":[{"type":"NOT_FOUND","path":["repository"],"message":"Could not resolve"}]}`, noTeams, 0, false, true},
	} {
		gh, _ := graphQLServer(t, func(q string, _ map[string]any) string {
			if strings.Contains(q, "teams") {
				return c.teams
			}
			return c.people
		})
		got, err := gh.ReviewerCandidates(context.Background(), "cy/dots", 0, "c")
		if len(got) != c.n || Transient(err) != c.transient || Refused(err) != c.refused {
			t.Errorf("%s: %+v, %v", name, got, err)
		}
	}
}

// No pull request and no words: nothing to suggest, and GitHub is not asked.
func TestNoSuggestionsWithoutAPullRequest(t *testing.T) {
	gh, _ := graphQLServer(t, func(string, map[string]any) string {
		t.Error("asked GitHub")
		return "{}"
	})
	if got, err := gh.ReviewerCandidates(context.Background(), "acme/api", 0, ""); err != nil || len(got) != 0 {
		t.Errorf("%+v, %v", got, err)
	}
}

// A team is asked as a team: "org/slug" goes to team_reviewers, and only
// the repository owner's — another owner's team cannot review here.
func TestRequestReviewersSplitsTeams(t *testing.T) {
	gh, posted := graphQLServer(t, func(string, map[string]any) string { return "{}" })
	if err := gh.RequestReviewers(context.Background(), "acme/api", 7, []string{"ana", "acme/platform", "other/platform"}); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal((*posted)[0])
	if !strings.Contains(string(b), `"reviewers":["ana"]`) || !strings.Contains(string(b), `"team_reviewers":["platform"]`) {
		t.Errorf("posted %s", b)
	}
}

// stalePull is pull request #1 on the fake GitHub, opened by its poster
// (fakegithub.Login) from a branch two commits long: the head, and the
// commit before it (old) that earlier reviews were submitted on.
func stalePull(t *testing.T) (fake *fakegithub.Server, gh *GitHub, head, old string) {
	t.Helper()
	bare := filepath.Join(t.TempDir(), "r.git")
	seed := filepath.Join(t.TempDir(), "seed")
	run := func(args ...string) string {
		out, err := exec.Command("git", args...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	run("init", "-q", "--bare", "-b", "main", bare)
	run("init", "-q", "-b", "feature", seed)
	for _, m := range []string{"old", "head"} {
		run("-C", seed, "-c", "user.name=t", "-c", "user.email=t@x", "commit", "-q", "--allow-empty", "-m", m)
	}
	run("-C", seed, "push", "-q", bare, "feature")
	fake = fakegithub.New(bare, "acme/api")
	srv := httptest.NewServer(fake.Handler())
	t.Cleanup(srv.Close)
	gh = NewGitHub(Credential{Auth: "pat", Secret: "x", APIBaseURL: srv.URL})
	if _, err := gh.OpenPullRequest(context.Background(), OpenPullRequest{Slug: "acme/api", Title: "T", Head: "feature", Base: "main"}); err != nil {
		t.Fatal(err)
	}
	return fake, gh, run("-C", seed, "rev-parse", "HEAD"), run("-C", seed, "rev-parse", "HEAD~1")
}

// reviewed appends reviews to #1, oldest first, as GitHub lists them.
func reviewed(fake *fakegithub.Server, rs ...fakegithub.Review) {
	p := fake.Pull(1)
	fake.Set(func(*fakegithub.Server) { p.Reviews = append(p.Reviews, rs...) })
}

func on(login, state, commit string) fakegithub.Review {
	return fakegithub.Review{Login: login, State: state, CommitID: commit}
}

func staleOf(t *testing.T, gh *GitHub) StaleReviews {
	t.Helper()
	got, err := gh.StaleReviewers(context.Background(), "acme/api", 1)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// Every page of reviews is read. Of n reviews, ana's first is on the old
// head and her last, the n-th, on the head; bo's only one is the one before
// it; the rest are other reviewers' of the head. With 45, GitHub's default
// page of 30 misses both; with 150, so does a single page of 100.
func TestStaleReviewersReadsEveryPage(t *testing.T) {
	for _, n := range []int{45, 150} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			fake, gh, head, old := stalePull(t)
			rs := []fakegithub.Review{on("ana", "COMMENTED", old)}
			for i := len(rs); i < n-2; i++ {
				rs = append(rs, on(fmt.Sprintf("u%d", i), "APPROVED", head))
			}
			rs = append(rs, on("bo", "COMMENTED", old), on("ana", "APPROVED", head))
			reviewed(fake, rs...)
			got := staleOf(t, gh)
			if got.Head != head || !slices.Equal(got.Logins, []string{"bo"}) {
				t.Errorf("stale %+v, want only bo on head %s", got, head)
			}
		})
	}
}

// The fake pages as GitHub does: 30 by default, at most 100.
func TestTheFakePagesReviewsAsGitHubDoes(t *testing.T) {
	fake, gh, head, _ := stalePull(t)
	for i := range 130 {
		reviewed(fake, on(fmt.Sprintf("u%d", i), "APPROVED", head))
	}
	for _, c := range []struct {
		query string
		want  int
	}{{"", 30}, {"?per_page=0", 30}, {"?per_page=500", 100}, {"?per_page=100&page=2", 30}, {"?page=5", 10}, {"?page=6", 0}} {
		var got []ghReview
		if err := gh.do(context.Background(), "GET", "/repos/acme/api/pulls/1/reviews"+c.query, nil, &got); err != nil {
			t.Fatal(err)
		}
		if len(got) != c.want {
			t.Errorf("%q: %d reviews, want %d", c.query, len(got), c.want)
		}
	}
}

// A reviewer's latest submitted review decides, whatever came before it.
// A draft (PENDING) review is no review. A dismissed review still says
// which commit its reviewer looked at, so it counts as their latest: on an
// old head they are asked again, on the head they are not.
func TestStaleReviewersWeighEachReviewersLatestReview(t *testing.T) {
	fake, gh, head, old := stalePull(t)
	reviewed(fake,
		on("ana", "CHANGES_REQUESTED", old), on("ana", "APPROVED", head), // looked at the head last: not stale
		on("cy", "APPROVED", head), on("cy", "COMMENTED", old), // last looked at the old head: stale
		on("dee", "COMMENTED", old), on("dee", "PENDING", head), // a draft on the head does not count
		on("eve", "PENDING", old), // only a draft: never reviewed
		on("fay", "DISMISSED", old),
		on("gus", "DISMISSED", head),
	)
	if got := staleOf(t, gh); !slices.Equal(got.Logins, []string{"cy", "dee", "fay"}) {
		t.Errorf("stale %v, want cy, dee and fay", got.Logins)
	}
}

// Never the author, an App's account (by "[bot]" or by type Bot), or
// anyone asked already; logins match whatever their case.
func TestStaleReviewersLeaveOutWhoCannotOrNeedNotBeAsked(t *testing.T) {
	fake, gh, head, old := stalePull(t)
	fake.Set(func(s *fakegithub.Server) { s.UserTypes = map[string]string{"renovate": "Bot"} })
	reviewed(fake,
		on(strings.ToUpper(fakegithub.Login), "COMMENTED", old),
		on("coderabbitai[bot]", "COMMENTED", old),
		on("renovate", "COMMENTED", old),
		on("hal", "COMMENTED", old),
		on("Ian", "COMMENTED", old), on("ian", "APPROVED", head),
		on("jerv-acme", "COMMENTED", old),
	)
	fake.RequestReview(1, "HAL")
	if got := staleOf(t, gh); !slices.Equal(got.Logins, []string{"jerv-acme"}) {
		t.Errorf("stale %v, want only jerv-acme", got.Logins)
	}
}
