package forge

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// graphQLServer answers /graphql with `answer` for the variables it was
// sent, and records what REST was sent.
func graphQLServer(t *testing.T, answer func(vars map[string]any) string) (*GitHub, *[]map[string]any) {
	t.Helper()
	var posted []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var in map[string]any
		_ = json.Unmarshal(b, &in)
		if r.URL.Path == "/graphql" {
			vars, _ := in["variables"].(map[string]any)
			_, _ = io.WriteString(w, answer(vars))
			return
		}
		posted = append(posted, in)
		w.WriteHeader(201)
		_, _ = io.WriteString(w, "{}")
	}))
	t.Cleanup(srv.Close)
	return NewGitHub(Credential{Auth: "pat", Secret: "x", APIBaseURL: srv.URL}), &posted
}

// Before any words: GitHub's suggestions, why each, without the author,
// and those already asked marked.
func TestReviewerSuggestionsForAPullRequest(t *testing.T) {
	gh, _ := graphQLServer(t, func(v map[string]any) string {
		if v["pr"] != true || v["search"] != false || v["number"] != float64(7) {
			t.Errorf("variables = %v", v)
		}
		return `{"data":{"repository":{"pullRequest":{"author":{"login":"dude-bot"},
			"suggestedReviewers":[
				{"isAuthor":false,"isCommenter":false,"reviewer":{"login":"ana","name":"Ana Ribeiro","avatarUrl":"https://a/ana"}},
				{"isAuthor":false,"isCommenter":true,"reviewer":{"login":"tom","name":"Tom Okafor","avatarUrl":"https://a/tom"}},
				{"isAuthor":true,"isCommenter":false,"reviewer":{"login":"dude-bot","name":"","avatarUrl":""}}],
			"reviewRequests":{"nodes":[{"requestedReviewer":{"__typename":"User","login":"Tom"}}]}}}}}`
	})
	got, err := gh.ReviewerCandidates(context.Background(), "acme/api", 7, "  ")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Login != "ana" || got[0].Reason != "changed" || got[0].Name != "Ana Ribeiro" || got[0].Requested ||
		got[1].Login != "tom" || got[1].Reason != "commented" || !got[1].Requested {
		t.Errorf("candidates = %+v", got)
	}
}

// With words: the repository's assignable people, then the owner's teams
// as "org/slug"; a person asked already is marked, a team too.
func TestReviewerSearchFindsPeopleAndTeams(t *testing.T) {
	gh, _ := graphQLServer(t, func(v map[string]any) string {
		if v["q"] != "an" || v["search"] != true {
			t.Errorf("variables = %v", v)
		}
		return `{"data":{"repository":{"pullRequest":{"author":{"login":"ana"},"suggestedReviewers":[],
			"reviewRequests":{"nodes":[{"requestedReviewer":{"__typename":"Team","slug":"platform"}}]}},
			"assignableUsers":{"nodes":[{"login":"ana","name":"Ana Ribeiro"},{"login":"hanna","name":"Hanna Lindqvist","avatarUrl":"https://a/h"}]}},
			"organization":{"teams":{"nodes":[{"slug":"platform","name":"Platform","members":{"totalCount":7}}]}}}}`
	})
	got, err := gh.ReviewerCandidates(context.Background(), "acme/api", 7, "an")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Login != "hanna" || got[0].Kind != "user" ||
		got[1].Login != "acme/platform" || got[1].Kind != "team" || got[1].Members != 7 || !got[1].Requested {
		t.Errorf("candidates = %+v", got)
	}
}

// A repository a user owns has no teams: GitHub says so about the
// organization only, and the people still come back. A rate limit is
// GitHub's to say again; any other error is a refusal.
func TestReviewerSearchErrors(t *testing.T) {
	for name, c := range map[string]struct {
		body      string
		n         int
		transient bool
		refused   bool
	}{
		"user-owned": {`{"data":{"repository":{"pullRequest":null,"assignableUsers":{"nodes":[{"login":"cy"}]}},"organization":null},
			"errors":[{"type":"NOT_FOUND","path":["organization"],"message":"Could not resolve to an Organization"}]}`, 1, false, false},
		// A path through a list has numbers in it: still the organization's.
		"a team hidden by SSO": {`{"data":{"repository":{"pullRequest":null,"assignableUsers":{"nodes":[{"login":"cy"}]}},"organization":{"teams":{"nodes":[]}}},
			"errors":[{"type":"FORBIDDEN","path":["organization","teams","nodes",0,"members"],"message":"SSO"}]}`, 1, false, false},
		"rate limited": {`{"errors":[{"type":"RATE_LIMITED","message":"API rate limit exceeded"}]}`, 0, true, false},
		"no repository": {`{"data":{"repository":null},"errors":[{"type":"NOT_FOUND","path":["repository"],"message":"Could not resolve"}]}`,
			0, false, true},
	} {
		gh, _ := graphQLServer(t, func(map[string]any) string { return c.body })
		got, err := gh.ReviewerCandidates(context.Background(), "cy/dots", 0, "c")
		if len(got) != c.n || Transient(err) != c.transient || Refused(err) != c.refused {
			t.Errorf("%s: %+v, %v", name, got, err)
		}
	}
}

// No pull request and no words: nothing to suggest, and GitHub is not asked.
func TestNoSuggestionsWithoutAPullRequest(t *testing.T) {
	gh, _ := graphQLServer(t, func(map[string]any) string {
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
	gh, posted := graphQLServer(t, func(map[string]any) string { return "{}" })
	if err := gh.RequestReviewers(context.Background(), "acme/api", 7, []string{"ana", "acme/platform", "other/platform"}); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal((*posted)[0])
	if !strings.Contains(string(b), `"reviewers":["ana"]`) || !strings.Contains(string(b), `"team_reviewers":["platform"]`) {
		t.Errorf("posted %s", b)
	}
}
